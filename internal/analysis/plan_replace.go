package analysis

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/printer"
	"go/token"
	"go/types"
	"strings"
)

func (s *snapshot) planReplace(req Request) (*Report, error) {
	if req.File == "" || req.Symbol == "" {
		return nil, fmt.Errorf("file and symbol are required")
	}
	if strings.TrimSpace(req.Source) == "" {
		return nil, fmt.Errorf("source is required")
	}
	decls, imports, err := parseSource(req.Source)
	if err != nil {
		return nil, err
	}
	if len(decls) != 1 {
		return nil, fmt.Errorf("source must hold exactly one declaration, got %d", len(decls))
	}
	newNames := declNames(decls[0])
	if len(newNames) != 1 {
		return nil, fmt.Errorf("source must declare exactly one name, got %d", len(newNames))
	}

	set, err := s.removalSetFor(req.File, req.Symbol)
	if err != nil {
		return nil, err
	}
	old := set.decls[0]
	r := &Report{Op: "replace", Subject: req.Symbol}

	if old.label() != newNames[0] {
		r.block(fmt.Sprintf("source declares %s, the symbol is %s", newNames[0], old.label()),
			"keep the name and kind; to rename or change kind, use remove then add")
	}
	if old.gen != nil && len(old.gen.Specs) > 1 {
		if g, ok := decls[0].(*ast.GenDecl); ok && len(g.Specs) != 1 {
			r.block(req.Symbol+" is one spec inside a grouped declaration", "give one spec as source, without the group")
		}
	}
	if isGenerated(old.file) {
		r.block(s.fileOf(old)+" is generated code", "change the generator instead")
	}

	start, end := old.span()
	summary := fmt.Sprintf("replaces %s at %s-%d", old.label(), s.position(s.root, start), s.fset.Position(end).Line)
	if len(imports) > 0 {
		summary += "; imports " + strings.Join(imports, ", ")
	}

	if len(r.Blockers) == 0 {
		switch oldNode := old.node.(type) {
		case *ast.FuncDecl:
			newFn := decls[0].(*ast.FuncDecl)
			if printNode(s.fset, oldNode.Type) != printNode(token.NewFileSet(), newFn.Type) {
				summary += fmt.Sprintf("; signature changes from %s to %s",
					printNode(s.fset, signatureOnly(oldNode)), printNode(token.NewFileSet(), signatureOnly(newFn)))
				r.followUp("Update the callers", s.callerLines(old.obj.(*types.Func)))
				if m := old.obj.(*types.Func); m.Type().(*types.Signature).Recv() != nil {
					r.followUp("Re-check the interface methods it implemented", s.methodLinks(m))
				}
			}
		case *ast.TypeSpec:
			newSpec := decls[0].(*ast.GenDecl).Specs[0].(*ast.TypeSpec)
			if printNode(s.fset, oldNode.Type) != printNode(token.NewFileSet(), newSpec.Type) {
				diff := shapeDiff(s.fset, oldNode.Type, newSpec.Type)
				summary += "; shape changes: " + strings.Join(diff, ", ")
				uses := s.usesOutside(set)
				var lines []string
				for path, positions := range uses {
					lines = append(lines, fmt.Sprintf("%s (%s)", path, plural(len(positions), "use")))
				}
				r.followUp("Check the packages using it against the removed or changed members", lines)
				if links := s.interfaceLinks(set.pkg, old.obj.(*types.TypeName)); len(links) > 0 {
					r.followUp("Re-check interface satisfaction", links)
				}
			}
		}
	}
	r.Summary = summary
	r.settle(req.Force)
	return r, nil
}

// signatureOnly copies a FuncDecl without body or doc, for printing.
func signatureOnly(fn *ast.FuncDecl) *ast.FuncDecl {
	cp := *fn
	cp.Body = nil
	cp.Doc = nil
	return &cp
}

func printNode(fset *token.FileSet, n ast.Node) string {
	var b bytes.Buffer
	_ = printer.Fprint(&b, fset, n)
	return b.String()
}

// callerLines renders the call sites of fn across the workspace.
func (s *snapshot) callerLines(fn *types.Func) []string {
	want := objectKey(s.fset, fn)
	var out []string
	seen := make(map[string]bool)
	for _, p := range s.pkgs {
		if p.TypesInfo == nil || isTestMain(p) {
			continue
		}
		for _, f := range p.Syntax {
			walkCalls(f, func(call *ast.CallExpr, callee types.Object, enclosing string) {
				if callee == nil || callee.Name() != fn.Name() || objectKey(s.fset, callee) != want {
					return
				}
				pos := s.position(s.root, call.Pos())
				if !seen[pos] {
					seen[pos] = true
					out = append(out, fmt.Sprintf("%s  in %s", pos, enclosing))
				}
			}, p)
		}
	}
	return out
}

// methodLinks lists interface methods a concrete method implements, which
// a signature change may break.
func (s *snapshot) methodLinks(m *types.Func) []string {
	recv := m.Type().(*types.Signature).Recv().Type()
	var out []string
	for _, p := range s.pkgs {
		if p.Types == nil || isTestMain(p) || p.ID != p.PkgPath {
			continue
		}
		scope := p.Types.Scope()
		for _, name := range scope.Names() {
			tn, ok := scope.Lookup(name).(*types.TypeName)
			if !ok {
				continue
			}
			iface, ok := tn.Type().Underlying().(*types.Interface)
			if !ok || iface.NumMethods() == 0 {
				continue
			}
			if match, _ := satisfies(recv, iface); !match {
				continue
			}
			if im := lookupMember(tn.Type(), p.Types, m.Name()); im != nil {
				out = append(out, fmt.Sprintf("%s.%s.%s  %s", p.Types.Name(), tn.Name(), m.Name(), s.position(s.root, im.Pos())))
			}
		}
	}
	return out
}

// shapeDiff compares struct fields or interface methods by name.
func shapeDiff(fset *token.FileSet, oldT, newT ast.Expr) []string {
	oldM, newM := members(fset, oldT), members(fset, newT)
	if oldM == nil || newM == nil {
		return []string{printNode(fset, oldT) + " becomes " + printNode(token.NewFileSet(), newT)}
	}
	var out []string
	for name, sig := range oldM {
		if n, ok := newM[name]; !ok {
			out = append(out, "removed "+name)
		} else if n != sig {
			out = append(out, "changed "+name)
		}
	}
	for name := range newM {
		if _, ok := oldM[name]; !ok {
			out = append(out, "added "+name)
		}
	}
	if len(out) == 0 {
		out = []string{"same members, different text"}
	}
	return out
}

func members(fset *token.FileSet, t ast.Expr) map[string]string {
	var list *ast.FieldList
	switch t := t.(type) {
	case *ast.StructType:
		list = t.Fields
	case *ast.InterfaceType:
		list = t.Methods
	default:
		return nil
	}
	out := make(map[string]string)
	for _, f := range list.List {
		sig := printNode(fset, f.Type)
		if len(f.Names) == 0 {
			out[sig] = sig
			continue
		}
		for _, n := range f.Names {
			out[n.Name] = sig
		}
	}
	return out
}
