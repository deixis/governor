package analysis

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/printer"
	"go/token"
	"go/types"
	"maps"
	"os"
	"slices"
	"strings"
)

func (s *snapshot) planReplace(ctx context.Context, req Request) (*Report, error) {
	if req.File == "" || req.Symbol == "" {
		return nil, fmt.Errorf("file and symbol are required")
	}
	if strings.TrimSpace(req.Source) == "" {
		return nil, fmt.Errorf("source is required")
	}
	set, err := s.removalSetFor(req.File, req.Symbol)
	if err != nil {
		return nil, err
	}
	src, err := parseSource(req.Source)
	if err != nil {
		return nil, err
	}
	decls, imports := src.decls, src.imports
	if len(decls) != 1 {
		return nil, fmt.Errorf("source must hold exactly one declaration, got %d", len(decls))
	}
	newNames := declNames(decls[0])
	if len(newNames) != 1 {
		return nil, fmt.Errorf("source must declare exactly one name, got %d", len(newNames))
	}
	old := set.decls[0]
	r := &Report{Op: "replace", Subject: req.Symbol}

	if old.syntaxLabel() != newNames[0] {
		r.block(fmt.Sprintf("source declares %s, the symbol is %s", newNames[0], old.syntaxLabel()),
			"keep the name and kind; to rename or change kind, use remove then add")
	}
	if old.gen != nil && len(old.gen.Specs) > 1 {
		if g, ok := decls[0].(*ast.GenDecl); ok && len(g.Specs) != 1 {
			r.block(req.Symbol+" is one spec inside a grouped declaration", "give one spec as source, without the group")
		}
	}
	if vs, ok := old.node.(*ast.ValueSpec); ok && len(vs.Names) > 1 {
		var others []string
		for _, n := range vs.Names {
			if n != old.name {
				others = append(others, n.Name)
			}
		}
		r.block(req.Symbol+" shares its spec with "+strings.Join(others, ", "),
			"split the spec so each name has its own, then replace")
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
		orig, _ := os.ReadFile(s.fset.Position(start).Filename)
		if orig != nil {
			// Inside a group the spec alone is spliced, not the keyword.
			text := src.body
			if g, ok := decls[0].(*ast.GenDecl); ok && old.gen != nil && len(old.gen.Specs) > 1 && len(g.Specs) == 1 {
				text = src.nodeText(g.Specs[0])
			}
			ed := splice(orig, old.pkg.Name, imports, text, s.fset.Position(start).Offset, s.fset.Position(end).Offset)
			for _, e := range s.checkSource(ctx, old.pkg, s.fset.Position(start).Filename, ed) {
				r.block("does not compile: "+e, compileFix)
			}
		}
	}
	if len(r.Blockers) == 0 {
		switch oldNode := old.node.(type) {
		case *ast.FuncDecl:
			newFn := decls[0].(*ast.FuncDecl)
			if signatureKey(oldNode) != signatureKey(newFn) {
				summary += fmt.Sprintf("; signature changes from %s to %s",
					printNode(signatureOnly(oldNode)), printNode(signatureOnly(newFn)))
				r.followUp("Update the callers", s.callerLines(old.obj.(*types.Func)))
				if m := old.obj.(*types.Func); m.Type().(*types.Signature).Recv() != nil {
					r.followUp("Re-check the interface methods it implemented", s.methodLinks(m))
				}
			}
		case *ast.TypeSpec:
			newSpec := decls[0].(*ast.GenDecl).Specs[0].(*ast.TypeSpec)
			if printNode(oldNode.Type) != printNode(newSpec.Type) {
				if ok, nk := typeKind(oldNode.Type), typeKind(newSpec.Type); ok != nk {
					summary += fmt.Sprintf("; kind changes from %s to %s", ok, nk)
				}
				diff := shapeDiff(oldNode.Type, newSpec.Type)
				summary += "; shape changes: " + strings.Join(diff, ", ")
				var changed []string
				for _, d := range diff {
					if name, ok := strings.CutPrefix(d, "removed "); ok {
						changed = append(changed, name)
					} else if name, ok := strings.CutPrefix(d, "changed "); ok {
						changed = append(changed, name)
					}
				}
				if sites := s.memberUseSites(set, old.obj.(*types.TypeName), changed); len(sites) > 0 {
					r.followUp("Update the uses of removed or changed members", sites)
				} else {
					uses, _ := s.usesOutside(set)
					var lines []string
					for _, path := range slices.Sorted(maps.Keys(uses)) {
						lines = append(lines, fmt.Sprintf("%s (%s)", path, plural(len(uses[path]), "use")))
					}
					r.followUp("Check the packages using it against the new shape", lines)
				}
				if links := s.interfaceLinks(old.obj.(*types.TypeName)); len(links) > 0 {
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
	cp := stripBody(fn)
	cp.Doc = nil
	return cp
}

// signatureKey renders what callers and interfaces see of a function:
// receiver, parameter and result types, without the names. Renaming a
// parameter is not a signature change.
func signatureKey(fn *ast.FuncDecl) string {
	cp := signatureOnly(fn)
	cp.Recv = unnamedFields(fn.Recv)
	ft := *fn.Type
	ft.Params = unnamedFields(fn.Type.Params)
	ft.Results = unnamedFields(fn.Type.Results)
	cp.Type = &ft
	return printNode(cp)
}

// unnamedFields copies a field list with one anonymous field per name,
// so (a, b int) and (x int, y int) render the same.
func unnamedFields(fl *ast.FieldList) *ast.FieldList {
	if fl == nil {
		return nil
	}
	cp := *fl
	cp.List = make([]*ast.Field, 0, len(fl.List))
	for _, f := range fl.List {
		for range max(len(f.Names), 1) {
			fc := *f
			fc.Names, fc.Doc, fc.Comment = nil, nil, nil
			cp.List = append(cp.List, &fc)
		}
	}
	return &cp
}

// printNode renders n on one line. An empty FileSet leaves every position
// unresolved, so the source layout of the node does not affect the text
// and nodes from different FileSets compare.
func printNode(n ast.Node) string {
	var b bytes.Buffer
	_ = printer.Fprint(&b, token.NewFileSet(), n)
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

// typeKind names the syntactic kind of a type expression.
func typeKind(t ast.Expr) string {
	switch t := t.(type) {
	case *ast.StructType:
		return "struct"
	case *ast.InterfaceType:
		return "interface"
	case *ast.FuncType:
		return "func"
	case *ast.MapType:
		return "map"
	case *ast.ArrayType:
		if t.Len == nil {
			return "slice"
		}
		return "array"
	case *ast.ChanType:
		return "chan"
	case *ast.StarExpr:
		return "pointer"
	}
	return "named"
}

// memberUseSites lists where fields or methods of tn with the given names
// are used, outside the set's own declarations.
func (s *snapshot) memberUseSites(set *removalSet, tn *types.TypeName, names []string) []string {
	if len(names) == 0 {
		return nil
	}
	wanted := toSet(names)
	targets := make(map[string]bool)
	if st, ok := tn.Type().Underlying().(*types.Struct); ok {
		for f := range st.Fields() {
			if wanted[f.Name()] {
				targets[objectKey(s.fset, f)] = true
			}
		}
	}
	for _, d := range methodsOf(set.index, tn) {
		if wanted[d.obj.Name()] {
			targets[objectKey(s.fset, d.obj)] = true
		}
	}
	if iface, ok := tn.Type().Underlying().(*types.Interface); ok {
		for m := range iface.Methods() {
			if wanted[m.Name()] {
				targets[objectKey(s.fset, m)] = true
			}
		}
	}
	member := make(map[string]string)
	var positions []string
	for _, p := range s.pkgs {
		if p.TypesInfo == nil || isTestMain(p) {
			continue
		}
		for id, obj := range p.TypesInfo.Uses {
			if obj == nil || !targets[objectKey(s.fset, obj)] {
				continue
			}
			if p.PkgPath == set.pkg.PkgPath && set.contains(id.Pos()) {
				continue
			}
			pos := s.position(s.root, id.Pos())
			member[pos] = obj.Name()
			positions = append(positions, pos)
		}
	}
	var out []string
	for _, pos := range sortedPositions(positions) {
		out = append(out, pos+"  ."+member[pos])
	}
	return out
}

// shapeDiff compares struct fields or interface methods by name.
func shapeDiff(oldT, newT ast.Expr) []string {
	oldM, newM := members(oldT), members(newT)
	if oldM == nil || newM == nil {
		return []string{printNode(oldT) + " becomes " + printNode(newT)}
	}
	var out []string
	for _, name := range slices.Sorted(maps.Keys(oldM)) {
		if n, ok := newM[name]; !ok {
			out = append(out, "removed "+name)
		} else if n != oldM[name] {
			out = append(out, "changed "+name)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(newM)) {
		if _, ok := oldM[name]; !ok {
			out = append(out, "added "+name)
		}
	}
	if len(out) == 0 {
		out = []string{"same members, different text"}
	}
	return out
}

func members(t ast.Expr) map[string]string {
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
		sig := printNode(f.Type)
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
