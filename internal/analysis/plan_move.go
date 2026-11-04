package analysis

import (
	"fmt"
	"go/ast"
	"go/types"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

// moveAnalysis is the closure of a move: what goes, what it needs from
// the source package, what the source package needs from it.
type moveAnalysis struct {
	// pulled are unexported source-package declarations only the moved
	// code uses; they travel with it. chain says which moved declaration
	// needs each.
	pulled []decl
	chain  map[types.Object]string
	// shared are unexported source-package declarations the moved code
	// uses and other code uses too, with the moved user and the other
	// users.
	shared map[types.Object]sharedUse
	// exportedStays are exported source-package objects the moved code
	// keeps using as foo.X.
	exportedStays []types.Object
	// memberUses are unexported methods or fields of other source-package
	// types that the moved code reaches into.
	memberUses []string
	// incoming are uses of the moved set from code staying in the source
	// package.
	incomingExported   []string
	incomingUnexported []string
	// forcedPulled are shared helpers pulled along by Force or a pull
	// decision, with the users left behind.
	forcedPulled map[types.Object][]string
	forcedOrder  []types.Object
}

type sharedUse struct {
	by     string
	others []string
}

func (s *snapshot) planMove(req Request) (*Report, error) {
	if req.File == "" || req.Symbol == "" {
		return nil, fmt.Errorf("file and symbol are required")
	}
	if req.Target == "" {
		return nil, fmt.Errorf("target (destination package import path or file path) is required")
	}
	set, err := s.removalSetFor(req.File, req.Symbol)
	if err != nil {
		return nil, err
	}
	dest, destFile, err := s.resolveMoveTarget(req.Target, set)
	if err != nil {
		return nil, err
	}
	src := set.pkg
	if dest.PkgPath == src.PkgPath {
		return nil, fmt.Errorf("%s is already in %s", req.Symbol, dest.PkgPath)
	}
	r := &Report{Op: "move", Subject: req.Symbol + " -> " + dest.PkgPath}
	for _, d := range set.decls {
		if isGenerated(d.file) {
			r.block(s.fileOf(d)+" is generated code", "change the generator instead")
		}
	}
	moved := len(set.decls)
	fromFiles := s.setFiles(set)

	ma := s.analyseMove(set, req.Decisions, req.Force)
	if len(ma.forcedPulled) > 0 {
		var lines []string
		for _, obj := range ma.forcedOrder {
			for _, pos := range ma.forcedPulled[obj] {
				lines = append(lines, fmt.Sprintf("%s  uses %s, now in %s", pos, obj.Name(), dest.Name))
			}
		}
		r.followUp("Fix the users of helpers pulled along", lines)
	}

	// Destination collisions, moved and pulled alike.
	destScope := dest.Types.Scope()
	for _, d := range append(slices.Clone(set.decls), ma.pulled...) {
		if memberOwner(d.obj) != nil {
			continue
		}
		if obj := destScope.Lookup(d.obj.Name()); obj != nil {
			r.block(fmt.Sprintf("%s already exists in %s at %s", d.label(), dest.PkgPath, s.position(s.root, obj.Pos())),
				"rename one of them first, or pick another destination")
		}
	}

	// Shared helpers: the agent decides.
	sharedObjs := make([]types.Object, 0, len(ma.shared))
	for obj := range ma.shared {
		sharedObjs = append(sharedObjs, obj)
	}
	sort.Slice(sharedObjs, func(i, j int) bool { return sharedObjs[i].Name() < sharedObjs[j].Name() })
	for _, obj := range sharedObjs {
		use := ma.shared[obj]
		r.ask(fmt.Sprintf("%s %s (%s) is unexported, used by %s and by %s staying in %s",
			objectKind(obj), obj.Name(), s.position(s.root, obj.Pos()), use.by, strings.Join(firstN(use.others, 3), ", "), src.Name),
			fmt.Sprintf(`decisions.export ["%s"]: keep it in %s and export it, moved code uses %s.%s; or decisions.pull ["%s"]: move it along and fix its other users afterwards`,
				obj.Name(), src.Name, src.Name, exportedName(obj.Name()), obj.Name()))
	}

	for _, m := range ma.memberUses {
		r.block("moved code uses unexported "+m+", which stays in "+src.Name,
			"export that member, or move its type as well")
	}
	for _, u := range ma.incomingUnexported {
		r.block("code staying in "+src.Name+" uses unexported "+u+" of the moved type",
			"export that member before moving, or move the user too")
	}

	// Import direction after the move, and cycles.
	destNeedsSource := len(ma.exportedStays) > 0 || len(req.Decisions.Export) > 0
	sourceNeedsDest := len(ma.incomingExported) > 0
	if sourceNeedsDest {
		users := ma.incomingExported
		var why string
		switch {
		case destNeedsSource:
			why = fmt.Sprintf("the moved code still uses %s from %s", strings.Join(objectNames(firstN2(ma.exportedStays, 3)), ", "), src.Name)
		case s.importsTransitively(dest, src.PkgPath):
			why = fmt.Sprintf("%s already imports %s", dest.Name, src.Name)
		}
		if why != "" {
			r.block(fmt.Sprintf("import cycle: %s uses the moved code at %s so %s would import %s, and %s",
				src.Name, strings.Join(firstN(users, 3), ", "), src.Name, dest.Name, why),
				"move those users too, keep the moved code independent of "+src.Name+", or choose a destination that does not import "+src.Name)
		}
	} else if destNeedsSource && s.importsTransitively(src, dest.PkgPath) {
		r.block(fmt.Sprintf("import cycle: %s imports %s, and the moved code uses %s from %s so %s would import it back",
			src.Name, dest.Name, strings.Join(objectNames(firstN2(ma.exportedStays, 3)), ", "), src.Name, dest.Name),
			"move what the code uses too, or choose a destination "+src.Name+" does not import")
	}

	// Other packages.
	uses := s.usesOutside(set)
	delete(uses, src.PkgPath)
	paths := make([]string, 0, len(uses))
	total := 0
	var generated []string
	for k, v := range uses {
		paths = append(paths, k)
		total += len(v)
		for _, pos := range v {
			if f := s.fileAt(pos); f != nil && isGenerated(f) {
				generated = append(generated, pos)
			}
		}
	}
	sort.Strings(paths)
	if len(generated) > 0 {
		r.block("generated code references the moved symbol at "+strings.Join(firstN(generated, 3), ", "),
			"regenerate after the move instead of editing those files")
	}

	// Summary.
	parts := []string{fmt.Sprintf("moves %s from %s to %s", plural(moved, "declaration"), strings.Join(fromFiles, ", "), relPath(s.root, destFile))}
	if len(ma.pulled) > 0 {
		names := make([]string, len(ma.pulled))
		for i, d := range ma.pulled {
			names[i] = d.obj.Name()
		}
		parts = append(parts, "pulls "+strings.Join(names, ", ")+" along")
	}
	if len(ma.exportedStays) > 0 {
		parts = append(parts, fmt.Sprintf("keeps using %s from %s", strings.Join(objectNames(firstN2(ma.exportedStays, 3)), ", "), src.Name))
	}
	if sourceNeedsDest {
		parts = append(parts, fmt.Sprintf("rewrites %s in %s to %s.X", plural(len(ma.incomingExported), "use"), src.Name, dest.Name))
	}
	if total > 0 {
		var pkgs []string
		for _, p := range paths {
			note := ""
			if p == dest.PkgPath {
				note = ", unqualified"
			} else if alias := s.importAlias(p, src.PkgPath); alias != "" {
				note = ", aliased " + alias
			}
			pkgs = append(pkgs, fmt.Sprintf("%s (%d%s)", p, len(uses[p]), note))
		}
		parts = append(parts, "rewrites "+plural(total, "reference")+" in "+strings.Join(pkgs, ", "))
	}
	if tn, ok := set.decls[0].obj.(*types.TypeName); ok {
		var local []string
		for _, l := range s.interfaceLinks(src, tn) {
			if strings.HasPrefix(l, src.Name+".") {
				local = append(local, l)
			}
		}
		if len(local) > 0 {
			parts = append(parts, "still satisfies "+strings.Join(local, ", ")+" across the package boundary")
		}
	}
	r.Summary = strings.Join(parts, "; ")
	r.settle(req.Force)
	return r, nil
}

// resolveMoveTarget accepts a package import path or an absolute file
// path in a loaded package directory, returning the package and the file
// that would receive the code.
func (s *snapshot) resolveMoveTarget(target string, set *removalSet) (*packages.Package, string, error) {
	if !filepath.IsAbs(target) {
		p := s.packageByPath(target)
		if p == nil {
			return nil, "", fmt.Errorf("package %s not found in workspace", target)
		}
		return p, filepath.Join(p.Dir, snakeCase(set.decls[0].obj.Name())+".go"), nil
	}
	abs := canonical(target)
	if pkgs := s.byFile[abs]; len(pkgs) > 0 {
		return pkgs[0], abs, nil
	}
	dir := filepath.Dir(abs)
	for _, p := range s.pkgs {
		if p.ID == p.PkgPath && canonical(p.Dir) == dir {
			return p, abs, nil
		}
	}
	return nil, "", fmt.Errorf("directory %s holds no loaded package", dir)
}

// analyseMove computes the closure. Decisions move shared objects into
// the pulled set (pull) or treat them as exported stays (export); force
// pulls every undecided shared object.
func (s *snapshot) analyseMove(set *removalSet, decisions Decisions, force bool) *moveAnalysis {
	ma := &moveAnalysis{
		chain:        make(map[types.Object]string),
		shared:       make(map[types.Object]sharedUse),
		forcedPulled: make(map[types.Object][]string),
	}
	src := set.pkg
	scope := src.Types.Scope()
	pull := toSet(decisions.Pull)
	export := toSet(decisions.Export)

	queue := slices.Clone(set.decls)
	staySeen := make(map[types.Object]bool)
	for len(queue) > 0 {
		d := queue[0]
		queue = queue[1:]
		inspectSpan(d, func(n ast.Node) {
			id, ok := n.(*ast.Ident)
			if !ok {
				return
			}
			obj := src.TypesInfo.Uses[id]
			if obj == nil || obj.Pkg() != src.Types || set.has(obj) {
				return
			}
			if owner := memberOwner(obj); owner != nil {
				if !set.has(owner) && !obj.Exported() {
					ma.memberUses = append(ma.memberUses, fmt.Sprintf("%s.%s at %s", owner.Name(), obj.Name(), s.position(s.root, id.Pos())))
				}
				return
			}
			if obj.Parent() != scope {
				return
			}
			if obj.Exported() || export[obj.Name()] {
				if !staySeen[obj] {
					staySeen[obj] = true
					ma.exportedStays = append(ma.exportedStays, obj)
				}
				return
			}
			others := s.usersOutside(src, obj, set)
			if len(others) == 0 || pull[obj.Name()] || force {
				if pd, ok := set.index[obj]; ok {
					ma.pulled = append(ma.pulled, pd)
					ma.chain[obj] = qualifiedName(d.obj)
					set.add(pd)
					queue = append(queue, pd)
					if len(others) > 0 {
						ma.forcedPulled[obj] = others
						ma.forcedOrder = append(ma.forcedOrder, obj)
					}
				}
				return
			}
			if _, done := ma.shared[obj]; !done {
				ma.shared[obj] = sharedUse{by: qualifiedName(d.obj), others: others}
			}
		})
	}
	sort.Slice(ma.exportedStays, func(i, j int) bool { return ma.exportedStays[i].Name() < ma.exportedStays[j].Name() })

	for id, obj := range src.TypesInfo.Uses {
		if obj == nil || set.contains(id.Pos()) {
			continue
		}
		target := obj
		if owner := memberOwner(obj); owner != nil {
			target = owner
		}
		if !set.has(target) {
			continue
		}
		// Users of a helper pulled along by decision are reported as
		// follow-ups, not as blockers.
		if _, forced := ma.forcedPulled[target]; forced {
			continue
		}
		pos := s.position(s.root, id.Pos())
		if obj.Exported() {
			ma.incomingExported = append(ma.incomingExported, pos)
		} else {
			ma.incomingUnexported = append(ma.incomingUnexported, fmt.Sprintf("%s at %s", qualifiedName(obj), pos))
		}
	}
	sort.Slice(ma.incomingExported, func(i, j int) bool { return lessPos(ma.incomingExported[i], ma.incomingExported[j]) })
	ma.incomingExported = dedupe(ma.incomingExported)
	sort.Strings(ma.incomingUnexported)
	sort.Strings(ma.memberUses)
	return ma
}

// usersOutside lists uses of obj in p from outside the set.
func (s *snapshot) usersOutside(p *packages.Package, obj types.Object, set *removalSet) []string {
	var out []string
	for id, o := range p.TypesInfo.Uses {
		if o == obj && !set.contains(id.Pos()) {
			out = append(out, s.position(s.root, id.Pos()))
		}
	}
	sort.Slice(out, func(i, j int) bool { return lessPos(out[i], out[j]) })
	return out
}

// importsTransitively reports whether p imports path, directly or
// through other workspace packages. Dependencies outside the workspace
// cannot import it back, so the walk stops at them.
func (s *snapshot) importsTransitively(p *packages.Package, path string) bool {
	seen := make(map[string]bool)
	var walk func(q *packages.Package) bool
	walk = func(q *packages.Package) bool {
		if seen[q.PkgPath] {
			return false
		}
		seen[q.PkgPath] = true
		for imp := range q.Imports {
			if imp == path {
				return true
			}
			if w := s.packageByPath(imp); w != nil && walk(w) {
				return true
			}
		}
		return false
	}
	return walk(p)
}

// importAlias returns the name a package at path imports srcPath under
// when it differs from the package name, or "".
func (s *snapshot) importAlias(path, srcPath string) string {
	p := s.packageByPath(path)
	if p == nil {
		return ""
	}
	for _, f := range p.Syntax {
		for _, imp := range f.Imports {
			if strings.Trim(imp.Path.Value, `"`) == srcPath && imp.Name != nil {
				return imp.Name.Name
			}
		}
	}
	return ""
}

// fileAt returns the parsed file for a rendered position, or nil.
func (s *snapshot) fileAt(pos string) *ast.File {
	file, _, _ := parsePos(pos)
	if file == "" {
		return nil
	}
	if !filepath.IsAbs(file) {
		file = filepath.Join(s.root, file)
	}
	file = canonical(file)
	pkgs := s.byFile[file]
	if len(pkgs) == 0 {
		return nil
	}
	return fileSyntax(pkgs[0], file)
}

// exportedName capitalises the first letter.
func exportedName(name string) string {
	if name == "" {
		return name
	}
	return strings.ToUpper(name[:1]) + name[1:]
}

func objectNames(objs []types.Object) []string {
	out := make([]string, len(objs))
	for i, o := range objs {
		out[i] = o.Name()
	}
	return out
}

func firstN2(in []types.Object, n int) []types.Object {
	if len(in) <= n {
		return in
	}
	return in[:n]
}

func toSet(names []string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}
