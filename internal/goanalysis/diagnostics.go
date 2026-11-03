package goanalysis

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
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
	root := w.Root()

	filter := make(map[string]bool, len(files))
	for _, f := range files {
		abs, err := filepath.Abs(f)
		if err != nil {
			return nil, err
		}
		if resolved, err := filepath.EvalSymlinks(abs); err == nil {
			abs = resolved
		}
		filter[abs] = true
	}

	seen := make(map[string]bool)
	var out []Diagnostic
	packages.Visit(snap.pkgs, nil, func(p *packages.Package) {
		if isTestMain(p) {
			return
		}
		for _, e := range expandErrors(p) {
			file, ok := splitErrorPos(e)
			if len(filter) > 0 && ok && !filter[file] {
				continue
			}
			key := e.Pos + "\x00" + e.Msg
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, Diagnostic{
				Position: relativePos(root, e.Pos),
				Package:  p.PkgPath,
				Message:  e.Msg,
			})
		}
	})

	sort.Slice(out, func(i, j int) bool {
		if out[i].Position != out[j].Position {
			return out[i].Position < out[j].Position
		}
		return out[i].Message < out[j].Message
	})
	return out, nil
}

// expandErrors normalises p.Errors. `go list` reports compile failures as
// one positionless ListError whose message is "# pkg\n./file:l:c: msg\n..."
// with paths relative to the package directory. Those are split into one
// error per line with absolute positions so they dedupe against the type
// checker's own report of the same problem.
func expandErrors(p *packages.Package) []packages.Error {
	dir := ""
	if len(p.GoFiles) > 0 {
		dir = filepath.Dir(p.GoFiles[0])
	} else if p.Module != nil {
		dir = p.Module.Dir
	}

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
			if !filepath.IsAbs(pos) && dir != "" {
				// pos is ./file:l:c; make the file absolute.
				file, suffix := splitFileSuffix(pos)
				pos = filepath.Join(dir, file) + suffix
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
	rel, err := filepath.Rel(root, file)
	if err != nil || strings.HasPrefix(rel, "..") {
		return pos
	}
	return rel + strings.TrimPrefix(pos, file)
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
