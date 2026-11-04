package analysis

import (
	"context"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/packages"
)

// Definition describes where and how a symbol is declared.
type Definition struct {
	Name      string
	Kind      string
	Package   string
	Position  string
	Signature string
	Doc       string
}

// DefinitionOf resolves symbol as seen from file and returns its
// declaration site, signature and doc comment.
func (w *Workspace) DefinitionOf(ctx context.Context, file, symbol string) (*Definition, error) {
	snap, obj, err := w.resolve(ctx, file, symbol)
	if err != nil {
		return nil, err
	}
	return snap.definition(ctx, snap.root, obj), nil
}

// definition builds a Definition for obj.
func (s *snapshot) definition(ctx context.Context, root string, obj types.Object) *Definition {
	pkgPath := ""
	if obj.Pkg() != nil {
		pkgPath = obj.Pkg().Path()
	}
	pos, doc := s.declSite(ctx, obj)
	return &Definition{
		Name:      qualifiedName(obj),
		Kind:      objectKind(obj),
		Package:   pkgPath,
		Position:  s.position(root, pos),
		Signature: signature(obj),
		Doc:       doc,
	}
}

// signature renders obj in Go syntax, qualified relative to its package.
func signature(obj types.Object) string {
	var qual types.Qualifier
	if obj.Pkg() != nil {
		qual = types.RelativeTo(obj.Pkg())
	}
	// Methods: ObjectString yields "func (*T).M()"; render the source form.
	if fn, ok := obj.(*types.Func); ok {
		sig := fn.Type().(*types.Signature)
		if recv := sig.Recv(); recv != nil {
			recvStr := types.TypeString(recv.Type(), qual)
			if recv.Name() != "" && recv.Name() != "_" {
				recvStr = recv.Name() + " " + recvStr
			}
			return "func (" + recvStr + ") " + fn.Name() + strings.TrimPrefix(types.TypeString(sig, qual), "func")
		}
	}
	return types.ObjectString(obj, qual)
}

// declSite returns the position of obj's declaring identifier in parsed
// source and its doc comment ("" when none). Objects outside the
// workspace come from export data, which names files as $GOROOT/... or
// module cache paths and keeps the line but not the column; their
// package is loaded on demand and the declaration is found again by file,
// line and name. When no source is available the export-data position is
// returned as is.
func (s *snapshot) declSite(ctx context.Context, obj types.Object) (token.Pos, string) {
	pos := obj.Pos()
	if !pos.IsValid() {
		return pos, ""
	}
	p := s.fset.Position(pos)
	if pkgs := s.byFile[p.Filename]; len(pkgs) > 0 {
		if syntax := fileSyntax(pkgs[0], p.Filename); syntax != nil {
			return pos, declDoc(syntax, pos)
		}
		return pos, ""
	}
	if obj.Pkg() == nil {
		return pos, ""
	}
	ext, err := s.externalPackage(ctx, obj.Pkg().Path())
	if err != nil {
		return pos, ""
	}
	syntax := externalFileSyntax(ext, p.Filename)
	if syntax == nil {
		return pos, ""
	}
	id := declIdentAt(s.fset, syntax, p.Line, obj.Name())
	if id == nil {
		return pos, ""
	}
	return id.Pos(), declDoc(syntax, id.Pos())
}

// externalFileSyntax matches an export-data filename against the files
// of an on-demand loaded package. Export data keeps absolute paths for
// module cache files, $GOROOT/src/... for the standard library and
// module@version/... under -trimpath; the two trimmed forms are matched
// by suffix.
func externalFileSyntax(p *packages.Package, filename string) *ast.File {
	if filepath.IsAbs(filename) {
		return fileSyntax(p, filename)
	}
	rel := strings.TrimPrefix(filename, "$GOROOT/")
	for i, f := range p.CompiledGoFiles {
		if i < len(p.Syntax) && strings.HasSuffix(filepath.ToSlash(f), "/"+rel) {
			return p.Syntax[i]
		}
	}
	return nil
}

// declIdentAt finds the declaring identifier named name on line in f:
// a function, type, value or field name.
func declIdentAt(fset *token.FileSet, f *ast.File, line int, name string) *ast.Ident {
	var found *ast.Ident
	onLine := func(id *ast.Ident) bool {
		return id.Name == name && fset.Position(id.Pos()).Line == line
	}
	ast.Inspect(f, func(n ast.Node) bool {
		if found != nil || n == nil {
			return false
		}
		if fset.Position(n.End()).Line < line || fset.Position(n.Pos()).Line > line {
			return false
		}
		switch d := n.(type) {
		case *ast.FuncDecl:
			if onLine(d.Name) {
				found = d.Name
			}
		case *ast.TypeSpec:
			if onLine(d.Name) {
				found = d.Name
			}
		case *ast.ValueSpec:
			for _, id := range d.Names {
				if onLine(id) {
					found = id
				}
			}
		case *ast.Field:
			for _, id := range d.Names {
				if onLine(id) {
					found = id
				}
			}
		}
		return found == nil
	})
	return found
}

// declDoc finds the declaration whose name sits at pos and returns its
// doc comment. For specs inside a grouped declaration the spec's own doc
// wins, then the group's.
func declDoc(f *ast.File, pos token.Pos) string {
	var doc string
	ast.Inspect(f, func(n ast.Node) bool {
		if doc != "" || n == nil || pos < n.Pos() || pos >= n.End() {
			return false
		}
		switch d := n.(type) {
		case *ast.FuncDecl:
			if d.Name.Pos() == pos {
				doc = d.Doc.Text()
				return false
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch sp := spec.(type) {
				case *ast.TypeSpec:
					if sp.Name.Pos() == pos {
						doc = firstDoc(sp.Doc, d.Doc)
						return false
					}
				case *ast.ValueSpec:
					for _, name := range sp.Names {
						if name.Pos() == pos {
							doc = firstDoc(sp.Doc, d.Doc)
							return false
						}
					}
				}
			}
		case *ast.Field:
			for _, name := range d.Names {
				if name.Pos() == pos {
					doc = d.Doc.Text()
					if doc == "" && d.Comment != nil {
						doc = d.Comment.Text()
					}
					return false
				}
			}
		}
		return true
	})
	return strings.TrimSpace(doc)
}

func firstDoc(groups ...*ast.CommentGroup) string {
	for _, g := range groups {
		if g != nil && g.Text() != "" {
			return g.Text()
		}
	}
	return ""
}

// FormatDefinition renders a Definition as plain text.
func FormatDefinition(d *Definition) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n", d.Kind, d.Name)
	fmt.Fprintf(&b, "Package: %s\n", d.Package)
	fmt.Fprintf(&b, "Defined: %s\n", d.Position)
	fmt.Fprintf(&b, "\n%s\n", d.Signature)
	if d.Doc != "" {
		fmt.Fprintf(&b, "\n%s\n", d.Doc)
	}
	return b.String()
}
