package goanalysis

import (
	"context"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"strings"
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
	return snap.definition(w.Root(), obj), nil
}

// definition builds a Definition for obj.
func (s *snapshot) definition(root string, obj types.Object) *Definition {
	pkgPath := ""
	if obj.Pkg() != nil {
		pkgPath = obj.Pkg().Path()
	}
	return &Definition{
		Name:      qualifiedName(obj),
		Kind:      objectKind(obj),
		Package:   pkgPath,
		Position:  s.position(root, obj.Pos()),
		Signature: signature(obj),
		Doc:       s.docFor(obj),
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

// docFor returns the doc comment attached to obj's declaration, or "".
// Objects outside the workspace have no syntax and yield "".
func (s *snapshot) docFor(obj types.Object) string {
	pos := obj.Pos()
	if !pos.IsValid() {
		return ""
	}
	filename := s.fset.Position(pos).Filename
	pkgs := s.byFile[filename]
	if len(pkgs) == 0 {
		return ""
	}
	syntax := fileSyntax(pkgs[0], filename)
	if syntax == nil {
		return ""
	}
	return declDoc(syntax, pos)
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
