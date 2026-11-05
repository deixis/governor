package analysis

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"
)

// edited is the content of a file after a plan would change it, with the
// byte ranges that hold new text: the declaration source and the import
// block it brought.
type edited struct {
	content []byte
	ranges  []span
	// srcLine is the line of content where the declaration source starts,
	// so errors can be reported against the source's own line numbers.
	srcLine int
}

// splice builds the file content an add or replace produces. orig is the
// current file, or nil for a new file of package pkgName. [start, end)
// is the byte range the source replaces; start == end == -1 appends.
// Imports the file already has are not added again.
func splice(orig []byte, pkgName string, imports []string, src string, start, end int) edited {
	if orig == nil {
		orig = []byte("package " + pkgName + "\n")
		start, end = -1, -1
	}
	fset := token.NewFileSet()
	f, _ := parser.ParseFile(fset, "", orig, parser.ImportsOnly|parser.SkipObjectResolution)
	impAt := 0
	have := make(map[string]bool)
	if f != nil {
		impAt = fset.Position(f.Name.End()).Offset
		for _, d := range f.Decls {
			if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.IMPORT {
				impAt = fset.Position(g.End()).Offset
				for _, sp := range g.Specs {
					if p, err := strconv.Unquote(sp.(*ast.ImportSpec).Path.Value); err == nil {
						have[p] = true
					}
				}
			}
		}
	}
	var impText string
	var fresh []string
	for _, p := range imports {
		if !have[p] {
			fresh = append(fresh, p)
		}
	}
	if len(fresh) > 0 {
		impText = "\nimport (\n"
		for _, p := range fresh {
			impText += "\t" + strconv.Quote(p) + "\n"
		}
		impText += ")\n"
	}
	src = strings.TrimRight(src, "\n") + "\n"
	var b bytes.Buffer
	var out edited
	if start < 0 {
		b.Write(orig[:impAt])
		b.WriteString(impText)
		out.ranges = append(out.ranges, span{token.Pos(impAt), token.Pos(impAt + len(impText))})
		b.Write(orig[impAt:])
		if b.Len() > 0 && b.Bytes()[b.Len()-1] != '\n' {
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
		at := b.Len()
		out.srcLine = bytes.Count(b.Bytes(), []byte("\n")) + 1
		b.WriteString(src)
		out.ranges = append(out.ranges, span{token.Pos(at), token.Pos(at + len(src))})
	} else {
		b.Write(orig[:impAt])
		b.WriteString(impText)
		out.ranges = append(out.ranges, span{token.Pos(impAt), token.Pos(impAt + len(impText))})
		b.Write(orig[impAt:start])
		at := b.Len()
		out.srcLine = bytes.Count(b.Bytes(), []byte("\n")) + 1
		b.WriteString(src)
		out.ranges = append(out.ranges, span{token.Pos(at), token.Pos(at + len(src))})
		b.Write(orig[end:])
	}
	out.content = b.Bytes()
	return out
}

// checkSource type-checks pkg as it would be with path holding ed, and
// returns the errors that sit in the new text, one line each, with line
// numbers relative to the declaration source. Errors elsewhere are the
// callers and users a replace breaks; the plan reports those as
// follow-ups.
func (s *snapshot) checkSource(ctx context.Context, pkg *packages.Package, path string, ed edited) []string {
	pkg = s.variantForFile(pkg, path)
	fset := token.NewFileSet()
	var files []*ast.File
	want := canonical(path)
	replaced := false
	for _, name := range pkg.CompiledGoFiles {
		var src []byte
		if name == path || canonical(name) == want {
			src = ed.content
			replaced = true
		} else {
			var err error
			if src, err = os.ReadFile(name); err != nil {
				continue
			}
		}
		f, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
		if f == nil && err != nil {
			continue
		}
		files = append(files, f)
	}
	var newFile *ast.File
	if !replaced {
		f, err := parser.ParseFile(fset, path, ed.content, parser.SkipObjectResolution)
		if f == nil {
			return []string{err.Error()}
		}
		newFile = f
		files = append(files, f)
	} else {
		for _, f := range files {
			if fset.Position(f.Pos()).Filename == path || canonical(fset.Position(f.Pos()).Filename) == want {
				newFile = f
			}
		}
	}
	if newFile == nil {
		return nil
	}

	// An import the package does not have yet would come from a separate
	// load, whose types are distinct from those the package's own
	// dependencies refer to: url.URL from the load is not the url.URL
	// http.Request holds. The go tool checks such a file against one
	// consistent set of dependencies instead.
	for _, imp := range newFile.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil || p == "C" || p == "unsafe" {
			continue
		}
		if dep := pkg.Imports[p]; dep == nil || dep.Types == nil || !dep.Types.Complete() {
			if found, ok := s.checkWithOverlay(ctx, pkg, path, ed); ok {
				return found
			}
			break
		}
	}

	rec := newErrorRecorder(path, ed)
	cfg := types.Config{
		Importer:    &snapshotImporter{s: s, ctx: ctx, pkg: pkg},
		Sizes:       pkg.TypesSizes,
		FakeImportC: true,
		Error: func(err error) {
			if te, ok := err.(types.Error); ok {
				rec.add(fset.Position(te.Pos), te.Msg)
			}
		},
	}
	if pkg.Module != nil && pkg.Module.GoVersion != "" {
		cfg.GoVersion = "go" + pkg.Module.GoVersion
	}
	_, _ = cfg.Check(pkg.PkgPath, fset, files, nil)
	return firstN(rec.found, 10)
}

// checkWithOverlay loads pkg through the go tool with path holding ed,
// and returns the errors in the new text. ok is false when the load
// itself failed; the caller falls back to the in-memory check.
func (s *snapshot) checkWithOverlay(ctx context.Context, pkg *packages.Package, path string, ed edited) (found []string, ok bool) {
	fset := token.NewFileSet()
	cfg := &packages.Config{
		Context: ctx,
		Mode:    loadMode,
		Dir:     s.root,
		Fset:    fset,
		Tests:   strings.HasSuffix(path, "_test.go"),
		Overlay: map[string][]byte{path: ed.content},
	}
	pkgs, err := packages.Load(cfg, pkg.PkgPath)
	if err != nil {
		return nil, false
	}
	var target *packages.Package
	want := canonical(path)
	for _, p := range pkgs {
		for _, f := range p.CompiledGoFiles {
			if f == path || canonical(f) == want {
				target = p
			}
		}
		if target != nil && target.ID != target.PkgPath {
			// The in-package test variant compiles both kinds of file.
			break
		}
	}
	if target == nil {
		// The go tool left the file out: a list error says why, for
		// instance a module go.mod does not provide.
		for _, p := range pkgs {
			for _, e := range p.Errors {
				found = append(found, "import: "+strings.Join(strings.Fields(e.Msg), " "))
			}
		}
		return firstN(found, 10), len(found) > 0
	}
	rec := newErrorRecorder(path, ed)
	for _, te := range target.TypeErrors {
		rec.add(fset.Position(te.Pos), te.Msg)
	}
	for _, e := range target.Errors {
		if e.Kind == packages.ListError {
			rec.found = append(rec.found, "import: "+strings.Join(strings.Fields(e.Msg), " "))
		}
	}
	return firstN(rec.found, 10), true
}

// errorRecorder keeps the errors that sit in the new text of one file,
// with line numbers relative to the declaration source.
type errorRecorder struct {
	path, want string
	ed         edited
	found      []string
	seen       map[string]bool
}

func newErrorRecorder(path string, ed edited) *errorRecorder {
	return &errorRecorder{path: path, want: canonical(path), ed: ed, seen: make(map[string]bool)}
}

func (r *errorRecorder) add(p token.Position, msg string) {
	if p.Filename != r.path && canonical(p.Filename) != r.want {
		return
	}
	off := token.Pos(p.Offset)
	inNew := false
	for _, rg := range r.ed.ranges {
		if off >= rg.start && off < rg.end {
			inNew = true
		}
	}
	if !inNew {
		return
	}
	msg = strings.Join(strings.Fields(msg), " ")
	line := fmt.Sprintf("line %d: %s", p.Line-r.ed.srcLine+1, msg)
	if p.Line < r.ed.srcLine {
		line = "import: " + msg
	}
	if !r.seen[line] {
		r.seen[line] = true
		r.found = append(r.found, line)
	}
}

// variantForFile picks the package variant that compiles path: the test
// variant for a _test.go file when the package has one.
func (s *snapshot) variantForFile(pkg *packages.Package, path string) *packages.Package {
	if pkgs := s.byFile[canonical(path)]; len(pkgs) > 0 {
		for _, p := range pkgs {
			if p.PkgPath == pkg.PkgPath && !isTestMain(p) {
				return p
			}
		}
	}
	if strings.HasSuffix(path, "_test.go") {
		for _, v := range s.variantsOf(pkg) {
			if v.ID != v.PkgPath {
				return v
			}
		}
	}
	return pkg
}

// snapshotImporter resolves imports from the loaded packages, then from
// the snapshot's on-demand external loads, so new imports in the source
// resolve to anything the module can import.
type snapshotImporter struct {
	s   *snapshot
	ctx context.Context
	pkg *packages.Package
}

func (im *snapshotImporter) Import(path string) (*types.Package, error) {
	if path == "unsafe" {
		return types.Unsafe, nil
	}
	// Packages past the first level of imports are stubs with an empty
	// scope when the load left out NeedDeps, so only a complete package
	// counts.
	if p := im.pkg.Imports[path]; p != nil && p.Types != nil && p.Types.Complete() {
		return p.Types, nil
	}
	if p := im.s.packageByPath(path); p != nil && p.Types != nil && p.Types.Complete() {
		return p.Types, nil
	}
	var found *types.Package
	packages.Visit(im.s.pkgs, nil, func(p *packages.Package) {
		if found == nil && p.PkgPath == path && p.ID == p.PkgPath && p.Types != nil && p.Types.Complete() {
			found = p.Types
		}
	})
	if found != nil {
		return found, nil
	}
	ext, err := im.s.externalPackage(im.ctx, path)
	if err != nil {
		return nil, err
	}
	return ext.Types, nil
}
