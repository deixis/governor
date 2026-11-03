package analysis

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/analysis/checker"
	"golang.org/x/tools/go/analysis/passes/modernize"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/imports"
	"mvdan.cc/gofumpt/format"
)

// FixResult reports what Fix did to one file.
type FixResult struct {
	File string
	// Changes lists applied rewrites, one line each.
	Changes []string
	// Diagnostics are the errors remaining in the file after fixing.
	Diagnostics []Diagnostic
	// Err is set when the file could not be processed at all.
	Err error
}

// Fix repairs and modernises files in place, then reports what is still
// broken. Per file, in order:
//
//  1. modernize rewrites from the type-checked snapshot (skipped when the
//     file's package has type errors)
//  2. import repair: add missing, drop unused (goimports)
//  3. gofumpt formatting
//  4. diagnostics for the file after reload
//
// Files that are not part of the workspace are reported with Err set.
func (w *Workspace) Fix(ctx context.Context, files []string) ([]FixResult, error) {
	if len(files) == 0 {
		return nil, fmt.Errorf("no files given")
	}
	results := make([]FixResult, len(files))
	targets := make(map[string]int, len(files))
	for i, f := range files {
		abs, err := filepath.Abs(f)
		if err != nil {
			results[i] = FixResult{File: f, Err: err}
			continue
		}
		if resolved, err := filepath.EvalSymlinks(abs); err == nil {
			abs = resolved
		}
		results[i].File = relPath(w.Root(), abs)
		targets[abs] = i
	}

	snap, err := w.load(ctx)
	if err != nil {
		return nil, err
	}

	// Step 1: modernize on the current snapshot.
	modernized, err := applyModernize(snap, targets)
	if err != nil {
		return nil, fmt.Errorf("modernize: %w", err)
	}
	for abs, changes := range modernized {
		results[targets[abs]].Changes = append(results[targets[abs]].Changes, changes...)
	}

	// Steps 2 and 3: pure text transforms, no type info needed.
	langVersion, modulePath := moduleInfo(snap)
	for abs, i := range targets {
		if results[i].Err != nil {
			continue
		}
		changes, err := repairAndFormat(abs, langVersion, modulePath)
		if err != nil {
			results[i].Err = err
			continue
		}
		results[i].Changes = append(results[i].Changes, changes...)
	}

	// Step 4: reload if anything changed, then collect diagnostics.
	for i := range results {
		if results[i].Err != nil {
			continue
		}
		diags, err := w.Diagnostics(ctx, []string{files[i]})
		if err != nil {
			return nil, err
		}
		results[i].Diagnostics = diags
	}
	return results, nil
}

// moduleInfo returns the Go language version and module path of the main
// module, for gofumpt.
func moduleInfo(snap *snapshot) (langVersion, modulePath string) {
	for _, p := range snap.pkgs {
		if p.Module != nil && p.Module.Main {
			if p.Module.GoVersion != "" {
				langVersion = "go" + p.Module.GoVersion
			}
			return langVersion, p.Module.Path
		}
	}
	return "", ""
}

// repairAndFormat runs goimports then gofumpt on one file, writing it
// back only when the content changed.
func repairAndFormat(abs, langVersion, modulePath string) ([]string, error) {
	src, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}

	var changes []string
	fixed, err := imports.Process(abs, src, &imports.Options{
		Comments:  true,
		TabIndent: true,
		TabWidth:  8,
	})
	if err != nil {
		// Syntax errors: leave the file alone, diagnostics will say why.
		return nil, nil
	}
	if !bytes.Equal(fixed, src) {
		changes = append(changes, importDelta(src, fixed)...)
	}

	formatted, err := format.Source(fixed, format.Options{
		LangVersion: langVersion,
		ModulePath:  modulePath,
	})
	if err != nil {
		return nil, fmt.Errorf("gofumpt: %w", err)
	}
	if !bytes.Equal(formatted, fixed) {
		changes = append(changes, "gofumpt: reformatted")
	}
	if !bytes.Equal(formatted, src) {
		if len(changes) == 0 {
			changes = append(changes, "gofmt: reformatted")
		}
		if err := os.WriteFile(abs, formatted, 0o644); err != nil {
			return nil, err
		}
	}
	return changes, nil
}

// importDelta describes import lines added or removed between two
// versions of a file.
func importDelta(before, after []byte) []string {
	b := importSet(before)
	a := importSet(after)
	var out []string
	for imp := range a {
		if !b[imp] {
			out = append(out, "import added: "+imp)
		}
	}
	for imp := range b {
		if !a[imp] {
			out = append(out, "import removed: "+imp)
		}
	}
	sort.Strings(out)
	if len(out) == 0 {
		return []string{"goimports: reformatted"}
	}
	return out
}

// importSet extracts quoted import paths from a source file by scanning
// import declarations textually. Good enough for a delta report.
func importSet(src []byte) map[string]bool {
	set := make(map[string]bool)
	inBlock := false
	for line := range strings.SplitSeq(string(src), "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "import ("):
			inBlock = true
			continue
		case inBlock && t == ")":
			inBlock = false
			continue
		case strings.HasPrefix(t, "import "):
			t = strings.TrimSpace(strings.TrimPrefix(t, "import"))
		case !inBlock:
			continue
		}
		if i := strings.IndexByte(t, '"'); i >= 0 {
			if j := strings.IndexByte(t[i+1:], '"'); j >= 0 {
				set[t[i+1:i+1+j]] = true
			}
		}
	}
	return set
}

// applyModernize runs the modernize suite on the packages owning the
// target files and applies every suggested fix located in those files.
// Returns a description of applied fixes per absolute file path.
func applyModernize(snap *snapshot, targets map[string]int) (map[string][]string, error) {
	// Packages to analyse: plain variants owning a target file.
	var pkgs []*packages.Package
	seen := make(map[*packages.Package]bool)
	for abs := range targets {
		for _, p := range snap.byFile[abs] {
			if isTestMain(p) || seen[p] {
				continue
			}
			// Prefer the test variant when the target is a test file,
			// otherwise the plain package; both contain the file, pick
			// the first (sorted by variantRank).
			seen[p] = true
			pkgs = append(pkgs, p)
			break
		}
	}
	if len(pkgs) == 0 {
		return nil, nil
	}

	graph, err := checker.Analyze(modernize.Suite, pkgs, &checker.Options{})
	if err != nil {
		return nil, err
	}

	// Gather edits per file.
	type edit struct {
		start, end int
		text       []byte
		desc       string
	}
	edits := make(map[string][]edit)
	for act := range graph.All() {
		if !act.IsRoot {
			continue
		}
		for _, d := range act.Diagnostics {
			dfile := snap.fset.File(d.Pos)
			if dfile == nil {
				continue
			}
			for _, fix := range d.SuggestedFixes {
				desc := fmt.Sprintf("%s: %s (line %d)", act.Analyzer.Name, fix.Message, dfile.Line(d.Pos))
				for _, te := range fix.TextEdits {
					file := snap.fset.File(te.Pos)
					if file == nil {
						continue
					}
					if _, ok := targets[file.Name()]; !ok {
						continue
					}
					end := te.End
					if !end.IsValid() {
						end = te.Pos
					}
					edits[file.Name()] = append(edits[file.Name()], edit{
						start: file.Offset(te.Pos),
						end:   file.Offset(end),
						text:  te.NewText,
						desc:  desc,
					})
				}
			}
		}
	}

	out := make(map[string][]string)
	for file, list := range edits {
		src, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		// Apply from the end so earlier offsets stay valid. Overlapping
		// edits (two analyzers touching one span) are skipped after the
		// first; the next Fix call picks them up.
		sort.Slice(list, func(i, j int) bool {
			if list[i].start != list[j].start {
				return list[i].start > list[j].start
			}
			return list[i].end > list[j].end
		})
		applied := make(map[string]bool)
		lastStart := len(src) + 1
		for _, e := range list {
			if e.end > lastStart || e.start < 0 || e.end > len(src) {
				continue
			}
			src = append(src[:e.start], append(append([]byte{}, e.text...), src[e.end:]...)...)
			lastStart = e.start
			if !applied[e.desc] {
				applied[e.desc] = true
				out[file] = append(out[file], e.desc)
			}
		}
		if len(out[file]) == 0 {
			continue
		}
		sort.Strings(out[file])
		if err := os.WriteFile(file, src, 0o644); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// FormatFixResults renders fix results as plain text.
func FormatFixResults(results []FixResult) string {
	var b strings.Builder
	for i, r := range results {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%s\n", r.File)
		if r.Err != nil {
			fmt.Fprintf(&b, "  error: %v\n", r.Err)
			continue
		}
		if len(r.Changes) == 0 {
			b.WriteString("  no changes\n")
		}
		for _, c := range r.Changes {
			fmt.Fprintf(&b, "  ~ %s\n", c)
		}
		if len(r.Diagnostics) == 0 {
			b.WriteString("  ok: no diagnostics\n")
		}
		for _, d := range r.Diagnostics {
			fmt.Fprintf(&b, "  ! %s: %s\n", d.Position, d.Message)
		}
	}
	return b.String()
}
