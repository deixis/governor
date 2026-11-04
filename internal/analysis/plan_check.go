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

	var found []string
	seen := make(map[string]bool)
	cfg := types.Config{
		Importer:    &snapshotImporter{s: s, ctx: ctx, pkg: pkg},
		Sizes:       pkg.TypesSizes,
		FakeImportC: true,
		Error: func(err error) {
			te, ok := err.(types.Error)
			if !ok {
				return
			}
			p := fset.Position(te.Pos)
			if p.Filename != path && canonical(p.Filename) != want {
				return
			}
			off := token.Pos(p.Offset)
			inNew := false
			for _, r := range ed.ranges {
				if off >= r.start && off < r.end {
					inNew = true
				}
			}
			if !inNew {
				return
			}
			msg := strings.Join(strings.Fields(te.Msg), " ")
			line := fmt.Sprintf("line %d: %s", p.Line-ed.srcLine+1, msg)
			if p.Line < ed.srcLine {
				line = "import: " + msg
			}
			if !seen[line] {
				seen[line] = true
				found = append(found, line)
			}
		},
	}
	if pkg.Module != nil && pkg.Module.GoVersion != "" {
		cfg.GoVersion = "go" + pkg.Module.GoVersion
	}
	_, _ = cfg.Check(pkg.PkgPath, fset, files, nil)
	return firstN(found, 10)
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
