package analysis

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"slices"
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
// Files that are not part of the workspace are reported with Err set and
// left untouched.
func (w *Workspace) Fix(ctx context.Context, files []string) ([]FixResult, error) {
	if len(files) == 0 {
		return nil, fmt.Errorf("no files given")
	}
	snap, err := w.load(ctx)
	if err != nil {
		return nil, err
	}

	results := make([]FixResult, len(files))
	paths := make([]string, len(files)) // canonical, "" when not a target
	targets := make(map[string]int, len(files))
	for i, f := range files {
		abs := canonical(f)
		results[i].File = relPath(snap.root, f)
		if !snap.contains(abs) {
			results[i].Err = fmt.Errorf("not part of any package loaded from %s", snap.root)
			continue
		}
		paths[i] = abs
		targets[abs] = i
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
	for abs, i := range targets {
		langVersion, modulePath := moduleInfo(snap, abs)
		changes, err := repairAndFormat(abs, langVersion, modulePath)
		if err != nil {
			results[i].Err = err
			continue
		}
		results[i].Changes = append(results[i].Changes, changes...)
	}

	// Step 4: reload if anything changed, then diagnose every target in
	// one pass. Errors without a position apply to every file.
	snap, err = w.load(ctx)
	if err != nil {
		return nil, err
	}
	filter := make(map[string]bool, len(targets))
	for abs := range targets {
		filter[abs] = true
	}
	for _, d := range snap.diagnostics(filter) {
		for i, abs := range paths {
			if abs != "" && results[i].Err == nil && (d.file == "" || d.file == abs) {
				results[i].Diagnostics = append(results[i].Diagnostics, d.Diagnostic)
			}
		}
	}
	return results, nil
}

// moduleInfo returns the Go language version and module path of the
// module owning file, falling back to the main module, for gofumpt.
func moduleInfo(snap *snapshot, file string) (langVersion, modulePath string) {
	var mod *packages.Module
	for _, p := range snap.byFile[file] {
		if p.Module != nil {
			mod = p.Module
			break
		}
	}
	if mod == nil {
		for _, p := range snap.pkgs {
			if p.Module != nil && p.Module.Main {
				mod = p.Module
				break
			}
		}
	}
	if mod == nil {
		return "", ""
	}
	if mod.GoVersion != "" {
		langVersion = "go" + mod.GoVersion
	}
	return langVersion, mod.Path
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
	// Packages to analyse: one variant per target, the first by
	// variantRank (the test variant only for test files). Several targets
	// in one package share it.
	var pkgs []*packages.Package
	seen := make(map[*packages.Package]bool)
	for abs := range targets {
		for _, p := range snap.byFile[abs] {
			if isTestMain(p) {
				continue
			}
			if !seen[p] {
				seen[p] = true
				pkgs = append(pkgs, p)
			}
			break
		}
	}
	if len(pkgs) == 0 {
		return nil, nil
	}
	// Deterministic fix order, so overlapping fixes resolve the same way
	// on every run.
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].ID < pkgs[j].ID })

	graph, err := checker.Analyze(modernize.Suite, pkgs, &checker.Options{})
	if err != nil {
		return nil, err
	}

	// The fset names files as the loader reported them; targets are
	// canonical.
	names := make(map[string]string)
	canon := func(name string) string {
		c, ok := names[name]
		if !ok {
			c = canonical(name)
			names[name] = c
		}
		return c
	}

	// Gather fixes per file. A fix is kept only when every edit lands in
	// one target file: it is applied whole or not at all. sizes records the
	// length of each file as parsed, so edits are never applied to a file
	// that changed on disk since the snapshot was taken.
	fixes := make(map[string][]suggestedFix)
	sizes := make(map[string]int)
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
				sf := suggestedFix{desc: fmt.Sprintf("%s: %s (line %d)", act.Analyzer.Name, fix.Message, dfile.Line(d.Pos))}
				file, ok := "", len(fix.TextEdits) > 0
				for _, te := range fix.TextEdits {
					tf := snap.fset.File(te.Pos)
					if tf == nil {
						ok = false
						break
					}
					name := canon(tf.Name())
					if _, isTarget := targets[name]; !isTarget || (file != "" && name != file) {
						ok = false
						break
					}
					file = name
					sizes[name] = tf.Size()
					end := te.End
					if !end.IsValid() {
						end = te.Pos
					}
					sf.edits = append(sf.edits, textEdit{start: tf.Offset(te.Pos), end: tf.Offset(end), text: te.NewText})
				}
				if ok {
					fixes[file] = append(fixes[file], sf)
				}
			}
		}
	}

	out := make(map[string][]string)
	for file, list := range fixes {
		src, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		if len(src) != sizes[file] {
			// Edited since the load: offsets are stale. The next call
			// sees the new content and retries.
			continue
		}
		edits, descs := mergeFixes(list, len(src))
		if len(edits) == 0 {
			continue
		}
		if err := os.WriteFile(file, applyEdits(src, edits), 0o644); err != nil {
			return nil, err
		}
		out[file] = descs
	}
	return out, nil
}

// textEdit replaces src[start:end] with text.
type textEdit struct {
	start, end int
	text       []byte
}

// suggestedFix is one analyzer fix confined to a single file.
type suggestedFix struct {
	desc  string
	edits []textEdit
}

// mergeFixes picks the fixes to apply to a file of size n, in order. A fix
// is taken whole or not at all: one that overlaps an edit already taken is
// left for the next Fix call. An edit identical to one already taken (the
// same fix reported by two package variants, or two fixes adding the same
// import) is kept once. Returns the edits ordered for back-to-front
// application and the sorted descriptions of the fixes taken.
func mergeFixes(fixes []suggestedFix, n int) ([]textEdit, []string) {
	var taken []textEdit
	var descs []string
	seenDesc := make(map[string]bool)
	for _, f := range fixes {
		var add []textEdit
		ok := true
	edits:
		for _, e := range f.edits {
			if e.start < 0 || e.end < e.start || e.end > n {
				ok = false
				break
			}
			for _, t := range taken {
				if t.start == e.start && t.end == e.end && bytes.Equal(t.text, e.text) {
					continue edits
				}
				if e.start < t.end && t.start < e.end {
					ok = false
					break edits
				}
			}
			add = append(add, e)
		}
		if !ok || len(add) == 0 {
			continue
		}
		taken = append(taken, add...)
		if !seenDesc[f.desc] {
			seenDesc[f.desc] = true
			descs = append(descs, f.desc)
		}
	}

	// Back to front. For equal starts the longer edit goes first, so an
	// insertion lands before the text replaced at the same offset; of two
	// insertions at one offset the later-taken goes first, so the output
	// keeps the order they were taken in.
	slices.Reverse(taken)
	sort.SliceStable(taken, func(i, j int) bool {
		if taken[i].start != taken[j].start {
			return taken[i].start > taken[j].start
		}
		return taken[i].end > taken[j].end
	})
	sort.Strings(descs)
	return taken, descs
}

// applyEdits applies edits in the order given, as mergeFixes returns them.
func applyEdits(src []byte, edits []textEdit) []byte {
	for _, e := range edits {
		src = append(src[:e.start], append(append([]byte{}, e.text...), src[e.end:]...)...)
	}
	return src
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
