package analysis

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"slices"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

// removalSet is the declarations a remove or move takes out of a
// package: the symbol plus, for a type, its methods.
type removalSet struct {
	pkg   *packages.Package
	index map[types.Object]decl
	decls []decl
	spans []span
}

type span struct{ start, end token.Pos }

func inSpans(spans []span, pos token.Pos) bool {
	for _, sp := range spans {
		if pos >= sp.start && pos < sp.end {
			return true
		}
	}
	return false
}

func (set *removalSet) contains(pos token.Pos) bool {
	return inSpans(set.spans, pos)
}

func (set *removalSet) has(obj types.Object) bool {
	for _, d := range set.decls {
		if d.obj == obj {
			return true
		}
	}
	return false
}

func (set *removalSet) add(d decl) {
	if set.has(d.obj) {
		return
	}
	set.decls = append(set.decls, d)
	start, end := d.span()
	set.spans = append(set.spans, span{start, end})
}

// withMethods returns d followed by, for a type, its methods from the
// index: a type never leaves its methods behind.
func (set *removalSet) withMethods(d decl) []decl {
	out := []decl{d}
	if tn, ok := d.obj.(*types.TypeName); ok {
		out = append(out, methodsOf(set.index, tn)...)
	}
	return out
}

// files lists the workspace-relative files the set touches.
func (s *snapshot) setFiles(set *removalSet) []string {
	seen := make(map[string]bool)
	var out []string
	for _, d := range set.decls {
		f := s.fileOf(d)
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

// removalSetFor resolves symbol in file to a package-level declaration
// of a workspace package and collects what goes with it.
func (s *snapshot) removalSetFor(file, symbol string) (*removalSet, error) {
	obj, err := s.lookup(file, symbol)
	if err != nil {
		return nil, err
	}
	if obj.Pkg() == nil {
		return nil, fmt.Errorf("%s is a universe symbol", symbol)
	}
	p := s.packageByPath(obj.Pkg().Path())
	if p == nil {
		return nil, fmt.Errorf("%s is declared outside the workspace (%s)", symbol, obj.Pkg().Path())
	}
	index := s.decls(p)
	d, ok := index[obj]
	if !ok {
		for _, v := range s.pkgs {
			if v.PkgPath == p.PkgPath && v.ID != v.PkgPath && !isTestMain(v) {
				if d, ok = s.decls(v)[obj]; ok {
					break
				}
			}
		}
	}
	if !ok {
		return nil, fmt.Errorf("%s is not a top-level declaration (a field, parameter or local)", symbol)
	}
	set := &removalSet{pkg: p, index: index}
	for _, d := range set.withMethods(d) {
		set.add(d)
	}
	return set, nil
}

// usesOutside lists every use of the set's objects, methods and fields
// included, from positions outside the set, grouped by package path.
// generated lists the uses that sit in generated files.
func (s *snapshot) usesOutside(set *removalSet) (byPkg map[string][]string, generated []string) {
	targets := make(map[string]bool)
	for _, d := range set.decls {
		targets[objectKey(s.fset, d.obj)] = true
		if tn, ok := d.obj.(*types.TypeName); ok {
			if st, ok := tn.Type().Underlying().(*types.Struct); ok {
				for f := range st.Fields() {
					targets[objectKey(s.fset, f)] = true
				}
			}
		}
	}
	byPkg = make(map[string][]string)
	generatedFiles := make(map[*ast.File]bool)
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
			byPkg[p.PkgPath] = append(byPkg[p.PkgPath], pos)
			if f := s.syntaxAt(p, id.Pos()); f != nil {
				gen, known := generatedFiles[f]
				if !known {
					gen = isGenerated(f)
					generatedFiles[f] = gen
				}
				if gen {
					generated = append(generated, pos)
				}
			}
		}
	}
	for k := range byPkg {
		byPkg[k] = sortedPositions(byPkg[k])
	}
	return byPkg, sortedPositions(generated)
}

// sortedPositions orders rendered positions and drops duplicates: a file
// compiled into two package variants reports each use twice.
func sortedPositions(in []string) []string {
	sort.Slice(in, func(i, j int) bool { return lessPos(in[i], in[j]) })
	return slices.Compact(in)
}

// inspectSpan walks the nodes of d's file inside d's span.
func inspectSpan(d decl, visit func(ast.Node)) {
	start, end := d.span()
	ast.Inspect(d.file, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		if n.End() <= start || n.Pos() >= end {
			_, isFile := n.(*ast.File)
			return isFile
		}
		visit(n)
		return true
	})
}

// orphans finds unexported package-level objects the set uses and nothing
// else does: candidates to remove in the same change.
func (s *snapshot) orphans(set *removalSet) []string {
	used := make(map[types.Object]bool)
	for _, d := range set.decls {
		inspectSpan(d, func(n ast.Node) {
			if id, ok := n.(*ast.Ident); ok {
				if obj := set.pkg.TypesInfo.Uses[id]; obj != nil && obj.Pkg() == set.pkg.Types && obj.Parent() == set.pkg.Types.Scope() {
					used[obj] = true
				}
			}
		})
	}
	var out []string
	for obj := range used {
		if _, inSet := set.index[obj]; !inSet || set.has(obj) || obj.Exported() {
			continue
		}
		if len(s.usersOutside(obj, set)) == 0 {
			out = append(out, fmt.Sprintf("%s %s  %s", objectKind(obj), obj.Name(), s.position(s.root, obj.Pos())))
		}
	}
	sort.Strings(out)
	return out
}

func (s *snapshot) planRemove(req Request) (*Report, error) {
	if req.File == "" || req.Symbol == "" {
		return nil, fmt.Errorf("file and symbol are required")
	}
	set, err := s.removalSetFor(req.File, req.Symbol)
	if err != nil {
		return nil, err
	}
	r := &Report{Op: "remove", Subject: req.Symbol}
	for _, d := range set.decls {
		if isGenerated(d.file) {
			r.block(s.fileOf(d)+" is generated code", "change the generator instead")
		}
	}

	summary := fmt.Sprintf("removes %s from %s", plural(len(set.decls), "declaration"), strings.Join(s.setFiles(set), ", "))

	uses, _ := s.usesOutside(set)
	if len(uses) > 0 {
		paths := make([]string, 0, len(uses))
		total := 0
		for k, v := range uses {
			paths = append(paths, k)
			total += len(v)
		}
		sort.Strings(paths)
		var lines []string
		for _, path := range paths {
			for _, pos := range uses[path] {
				lines = append(lines, pos+"  in "+path)
			}
		}
		summary += fmt.Sprintf("; breaks %s in %s", plural(total, "use"), plural(len(paths), "package"))
		r.followUp("Fix or remove the uses that break", lines)
	}
	if tn, ok := set.decls[0].obj.(*types.TypeName); ok {
		if links := s.interfaceLinks(tn); len(links) > 0 {
			summary += "; drops " + plural(len(links), "interface relation") + " (" + strings.Join(firstN(links, 3), ", ") + ")"
		}
	}
	if orphans := s.orphans(set); len(orphans) > 0 {
		r.followUp("Remove too, nothing else uses them", orphans)
	}
	r.Summary = summary
	r.settle(req.Force)
	return r, nil
}

// interfaceLinks lists workspace interfaces tn satisfies, and for an
// interface tn the workspace types satisfying it, as pkg.Name.
func (s *snapshot) interfaceLinks(tn *types.TypeName) []string {
	var out []string
	queryType := tn.Type()
	_, queryIsIface := queryType.Underlying().(*types.Interface)
	for _, p := range s.pkgs {
		if p.Types == nil || isTestMain(p) || p.ID != p.PkgPath {
			continue
		}
		scope := p.Types.Scope()
		for _, name := range scope.Names() {
			cand, ok := scope.Lookup(name).(*types.TypeName)
			if !ok || cand == tn || cand.IsAlias() {
				continue
			}
			candIface, candIsIface := cand.Type().Underlying().(*types.Interface)
			var match bool
			switch {
			case queryIsIface:
				match, _ = satisfies(cand.Type(), queryType.Underlying().(*types.Interface))
			case candIsIface && candIface.NumMethods() > 0:
				match, _ = satisfies(queryType, candIface)
			}
			if match {
				out = append(out, p.Types.Name()+"."+cand.Name())
			}
		}
	}
	sort.Strings(out)
	return out
}
