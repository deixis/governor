package analysis

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"
)

// Diagnostic is one parse, build or type error.
type Diagnostic struct {
	// Position is file:line:col, relative to the workspace root when
	// the file lives under it.
	Position string
	// Package is the import path of the package reporting the error.
	Package string
	// Message is the error text.
	Message string
}

// Diagnostics returns parse, build and type errors for the workspace. When
// files is non-empty, only errors located in those files are returned.
// Errors without a position (e.g. module resolution failures) are always
// included.
func (w *Workspace) Diagnostics(ctx context.Context, files []string) ([]Diagnostic, error) {
	snap, err := w.load(ctx)
	if err != nil {
		return nil, err
	}
	filter := make(map[string]bool, len(files))
	for _, f := range files {
		filter[canonical(f)] = true
	}
	tagged := snap.diagnostics(filter)
	out := make([]Diagnostic, len(tagged))
	for i, d := range tagged {
		out[i] = d.Diagnostic
	}
	return out, nil
}

// fileDiagnostic is a Diagnostic tagged with the canonical path of its
// file, or "" when the error has no position.
type fileDiagnostic struct {
	file string
	Diagnostic
}

// diagnostics collects the snapshot's errors, sorted by position. When
// filter (canonical paths) is non-empty, errors located in other files are
// dropped; errors without a position are always kept.
func (s *snapshot) diagnostics(filter map[string]bool) []fileDiagnostic {
	resolved := make(map[string]string)
	canon := func(file string) string {
		c, ok := resolved[file]
		if !ok {
			c = canonical(file)
			resolved[file] = c
		}
		return c
	}

	seen := make(map[string]bool)
	var out []fileDiagnostic
	packages.Visit(s.pkgs, nil, func(p *packages.Package) {
		if isTestMain(p) {
			return
		}
		for _, e := range expandErrors(p, s.root) {
			file, key := "", e.Pos
			if f, ok := splitErrorPos(e); ok {
				file = canon(f)
				key = file + strings.TrimPrefix(e.Pos, f)
			}
			if len(filter) > 0 && file != "" && !filter[file] {
				continue
			}
			key += "\x00" + e.Msg
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, fileDiagnostic{file: file, Diagnostic: Diagnostic{
				Position: relativePos(s.root, e.Pos),
				Package:  p.PkgPath,
				Message:  e.Msg,
			}})
		}
	})

	sort.Slice(out, func(i, j int) bool {
		if out[i].Position != out[j].Position {
			return lessPos(out[i].Position, out[j].Position)
		}
		return out[i].Message < out[j].Message
	})
	return out
}

// expandErrors normalises p.Errors. `go list` reports compile failures as
// one positionless ListError whose message is "# pkg\nsub/file:l:c: msg\n..."
// with paths relative to the directory it ran in, the workspace root.
// Those are split into one error per line with absolute positions so they
// dedupe against the type checker's own report of the same problem.
func expandErrors(p *packages.Package, root string) []packages.Error {
	var out []packages.Error
	for _, e := range p.Errors {
		if e.Kind != packages.ListError || !strings.HasPrefix(e.Msg, "# ") {
			out = append(out, e)
			continue
		}
		lines := strings.Split(e.Msg, "\n")
		expanded := 0
		for _, line := range lines[1:] {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			pos, msg, ok := splitListErrorLine(line)
			if !ok {
				continue
			}
			if !filepath.IsAbs(pos) {
				// pos is relative to the go command's directory.
				file, suffix := splitFileSuffix(pos)
				pos = filepath.Join(root, file) + suffix
			}
			out = append(out, packages.Error{Pos: pos, Msg: msg, Kind: packages.TypeError})
			expanded++
		}
		if expanded == 0 {
			out = append(out, e)
		}
	}
	return out
}

// listErrorLine matches "./file.go:4:14: message" or "file.go:4: message".
var listErrorLine = regexp.MustCompile(`^(.+?\.go:\d+(?::\d+)?): (.*)$`)

// splitListErrorLine splits a go list error line into position and
// message. ok is false when the line has no "file:line" prefix.
func splitListErrorLine(line string) (pos, msg string, ok bool) {
	m := listErrorLine.FindStringSubmatch(line)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// splitFileSuffix separates "file:l:c" into "file" and ":l:c".
func splitFileSuffix(pos string) (file, suffix string) {
	file, ok := splitErrorPos(packages.Error{Pos: pos})
	if !ok {
		return pos, ""
	}
	return file, strings.TrimPrefix(pos, file)
}

// isTestMain reports whether p is a generated test main package, whose
// sources live in the build cache.
func isTestMain(p *packages.Package) bool {
	return p.Name == "main" && strings.HasSuffix(p.PkgPath, ".test")
}

// splitErrorPos extracts the absolute filename from a packages.Error
// position of the form file:line:col or file:line. ok is false when the
// error has no file position.
func splitErrorPos(e packages.Error) (file string, ok bool) {
	pos := e.Pos
	if pos == "" || pos == "-" {
		return "", false
	}
	// Strip :line[:col] suffixes. Filenames may contain ':' on Windows
	// drive letters, so trim from the right.
	for range 2 {
		i := strings.LastIndex(pos, ":")
		if i < 0 {
			break
		}
		if isDigits(pos[i+1:]) {
			pos = pos[:i]
		}
	}
	if pos == "" {
		return "", false
	}
	return pos, true
}

// lessPos orders "file:line:col" strings by file, then numerically by line
// and column, so line 9 sorts before line 10.
func lessPos(a, b string) bool {
	fa, la, ca := parsePos(a)
	fb, lb, cb := parsePos(b)
	if fa != fb {
		return fa < fb
	}
	if la != lb {
		return la < lb
	}
	return ca < cb
}

// parsePos splits "file:line:col" or "file:line". Missing numbers are 0;
// a string without a position is returned whole as file.
func parsePos(pos string) (file string, line, col int) {
	file, ok := splitErrorPos(packages.Error{Pos: pos})
	if !ok {
		return pos, 0, 0
	}
	nums := strings.Split(strings.TrimPrefix(strings.TrimPrefix(pos, file), ":"), ":")
	if len(nums) > 0 {
		line, _ = strconv.Atoi(nums[0])
	}
	if len(nums) > 1 {
		col, _ = strconv.Atoi(nums[1])
	}
	return file, line, col
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// relativePos makes the filename in a file:line:col string relative to
// root when possible.
func relativePos(root, pos string) string {
	if pos == "" || pos == "-" {
		return "<no position>"
	}
	file, ok := splitErrorPos(packages.Error{Pos: pos})
	if !ok {
		return pos
	}
	return relPath(root, file) + strings.TrimPrefix(pos, file)
}

// FormatDiagnostics renders diagnostics as plain text for an agent.
func FormatDiagnostics(diags []Diagnostic) string {
	if len(diags) == 0 {
		return "No diagnostics."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d diagnostic(s):\n", len(diags))
	for _, d := range diags {
		fmt.Fprintf(&b, "%s: %s", d.Position, d.Message)
		if d.Package != "" {
			fmt.Fprintf(&b, " [%s]", d.Package)
		}
		b.WriteByte('\n')
	}
	return b.String()
}
