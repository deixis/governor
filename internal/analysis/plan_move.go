package analysis

import (
	"fmt"
	"go/ast"
	"go/types"
	"slices"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

// moveAnalysis is the closure of a move: what goes, what it needs from
// the source package, what the source package needs from it.
type moveAnalysis struct {
	// pulled are unexported source-package declarations only the moved
	// code uses; they travel with it, a type with its methods.
	pulled []decl
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
	// package, test files included.
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
	target, err := s.resolveTarget(req.Target, snakeCase(set.decls[0].obj.Name())+".go")
	if err != nil {
		return nil, err
	}
	dest, src := target.pkg, set.pkg
	if dest.PkgPath == src.PkgPath {
		return nil, fmt.Errorf("%s is already in %s", req.Symbol, dest.PkgPath)
	}
	r := &Report{Op: "move", Subject: req.Symbol + " -> " + dest.PkgPath}
	for _, d := range set.decls {
		if isGenerated(d.file) {
			r.block(s.fileOf(d)+" is generated code", "change the generator instead")
		}
	}
	destFile := relPath(s.root, target.file)
	if !target.newFile {
		if f := fileSyntax(dest, target.file); f != nil && isGenerated(f) {
			r.block(destFile+" is generated code", "target another file of the package, or change the generator")
		}
	}
	moved := len(set.decls)
	fromFiles := s.setFiles(set)

	ma := s.analyseMove(set, req.Decisions, req.Force)
	destNeedsSource := len(ma.exportedStays) > 0 || len(req.Decisions.Export) > 0

	// Users left behind by a pulled helper. The agent chose to pull, so
	// they are follow-ups; when importing the helper back would close a
	// cycle, the title says so.
	var leftBehind []string
	for _, obj := range ma.forcedOrder {
		for _, pos := range ma.forcedPulled[obj] {
			leftBehind = append(leftBehind, fmt.Sprintf("%s  uses %s, now in %s", pos, obj.Name(), dest.Name))
		}
	}
	title := "Fix the users of helpers pulled along"
	if len(leftBehind) > 0 && (destNeedsSource || s.importsTransitively(dest, src.PkgPath)) {
		title += fmt.Sprintf(", without importing %s from %s (cycle)", dest.Name, src.Name)
	}
	r.followUp(title, leftBehind)

	// Destination collisions, moved and pulled alike: analyseMove added
	// the pulled declarations to the set.
	destScope := dest.Types.Scope()
	for _, d := range set.decls {
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
	if len(ma.incomingExported) > 0 {
		var why string
		switch {
		case destNeedsSource:
			why = fmt.Sprintf("the moved code still uses %s from %s", strings.Join(objectNames(firstN(ma.exportedStays, 3)), ", "), src.Name)
		case s.importsTransitively(dest, src.PkgPath):
			why = fmt.Sprintf("%s already imports %s", dest.Name, src.Name)
		}
		if why != "" {
			r.block(fmt.Sprintf("import cycle: %s uses the moved code at %s so %s would import %s, and %s",
				src.Name, strings.Join(firstN(ma.incomingExported, 3), ", "), src.Name, dest.Name, why),
				"move those users too, keep the moved code independent of "+src.Name+", or choose a destination that does not import "+src.Name)
		}
	} else if destNeedsSource && s.importsTransitively(src, dest.PkgPath) {
		r.block(fmt.Sprintf("import cycle: %s imports %s, and the moved code uses %s from %s so %s would import it back",
			src.Name, dest.Name, strings.Join(objectNames(firstN(ma.exportedStays, 3)), ", "), src.Name, dest.Name),
			"move what the code uses too, or choose a destination "+src.Name+" does not import")
	}

	// Other packages: they import the destination after the move, so none
	// of them may sit on an import path from the destination.
	uses, generated := s.usesOutside(set)
	delete(uses, src.PkgPath)
	paths := make([]string, 0, len(uses))
	total := 0
	for k, v := range uses {
		paths = append(paths, k)
		total += len(v)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if p != dest.PkgPath && s.importsTransitively(dest, p) {
			r.block(fmt.Sprintf("import cycle: %s uses the moved code at %s and %s imports it",
				p, strings.Join(firstN(uses[p], 3), ", "), dest.Name),
				"move those users too, or choose a destination that does not import "+p)
		}
	}
	if len(generated) > 0 {
		r.block("generated code references the moved symbol at "+strings.Join(firstN(generated, 3), ", "),
			"regenerate after the move instead of editing those files")
	}

	// Summary.
	parts := []string{fmt.Sprintf("moves %s from %s to %s", plural(moved, "declaration"), strings.Join(fromFiles, ", "), destFile)}
	if len(ma.pulled) > 0 {
		names := make([]string, len(ma.pulled))
		for i, d := range ma.pulled {
			names[i] = d.obj.Name()
		}
		parts = append(parts, "pulls "+strings.Join(names, ", ")+" along")
	}
	if len(ma.exportedStays) > 0 {
		parts = append(parts, fmt.Sprintf("keeps using %s from %s", strings.Join(objectNames(firstN(ma.exportedStays, 3)), ", "), src.Name))
	}
	if len(ma.incomingExported) > 0 {
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
		for _, l := range s.interfaceLinks(tn) {
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

// analyseMove computes the closure. Decisions move shared objects into
// the pulled set (pull) or treat them as exported stays (export); force
// pulls every undecided shared object.
func (s *snapshot) analyseMove(set *removalSet, decisions Decisions, force bool) *moveAnalysis {
	ma := &moveAnalysis{
		shared:       make(map[types.Object]sharedUse),
		forcedPulled: make(map[types.Object][]string),
	}
	src := set.pkg
	scope := src.Types.Scope()
	owners := fieldOwners(src.Types)
	pull := toSet(decisions.Pull)
	export := toSet(decisions.Export)

	queue := slices.Clone(set.decls)
	staySeen := make(map[types.Object]bool)
	// depend settles one package-level object of src the moved code
	// needs: it stays (exported, or exported by decision), travels
	// (unexported and used by nothing else, or pulled by decision or
	// force) or becomes a question.
	depend := func(obj types.Object, by decl) {
		if obj.Exported() || export[obj.Name()] {
			if !staySeen[obj] {
				staySeen[obj] = true
				ma.exportedStays = append(ma.exportedStays, obj)
			}
			return
		}
		if _, asked := ma.shared[obj]; asked {
			return
		}
		others := s.usersOutside(obj, set)
		if len(others) == 0 || pull[obj.Name()] || force {
			pd, ok := set.index[obj]
			if !ok {
				return
			}
			ma.pulled = append(ma.pulled, pd)
			for _, d := range set.withMethods(pd) {
				set.add(d)
				queue = append(queue, d)
			}
			if len(others) > 0 {
				ma.forcedPulled[obj] = others
				ma.forcedOrder = append(ma.forcedOrder, obj)
			}
			return
		}
		ma.shared[obj] = sharedUse{by: qualifiedName(by.obj), others: others}
	}
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
			if owner := ownerOf(owners, obj); owner != nil {
				// Reaching into an unexported type of the package is a
				// use of that type.
				if !set.has(owner) && !owner.Exported() && owner.Parent() == scope {
					depend(owner, d)
				}
				if !set.has(owner) && !obj.Exported() {
					ma.memberUses = append(ma.memberUses, fmt.Sprintf("%s.%s at %s", owner.Name(), obj.Name(), s.position(s.root, id.Pos())))
				}
				return
			}
			if obj.Parent() != scope {
				return
			}
			depend(obj, d)
		})
	}
	sort.Slice(ma.exportedStays, func(i, j int) bool { return ma.exportedStays[i].Name() < ma.exportedStays[j].Name() })

	// Uses of the moved set from the rest of the package, test files
	// included. Variants type-check the package separately, so objects
	// are matched by key, not identity.
	setKeys := make(map[string]bool)
	setNames := make(map[string]bool)
	for _, d := range set.decls {
		setKeys[objectKey(s.fset, d.obj)] = true
		setNames[d.obj.Name()] = true
	}
	forcedKeys := make(map[string]bool)
	for obj := range ma.forcedPulled {
		forcedKeys[objectKey(s.fset, obj)] = true
	}
	for _, p := range s.variantsOf(src) {
		owners := fieldOwners(p.Types)
		for id, obj := range p.TypesInfo.Uses {
			if obj == nil || obj.Pkg() != p.Types || set.contains(id.Pos()) {
				continue
			}
			target := obj
			if owner := ownerOf(owners, obj); owner != nil {
				target = owner
			}
			if !setNames[target.Name()] {
				continue
			}
			key := objectKey(s.fset, target)
			// Users of a helper pulled along by decision are reported as
			// follow-ups, not as blockers.
			if !setKeys[key] || forcedKeys[key] {
				continue
			}
			pos := s.position(s.root, id.Pos())
			if obj.Exported() {
				ma.incomingExported = append(ma.incomingExported, pos)
			} else {
				ma.incomingUnexported = append(ma.incomingUnexported, fmt.Sprintf("%s at %s", qualifiedName(obj), pos))
			}
		}
	}
	ma.incomingExported = sortedPositions(ma.incomingExported)
	sort.Strings(ma.incomingUnexported)
	ma.incomingUnexported = slices.Compact(ma.incomingUnexported)
	sort.Strings(ma.memberUses)
	ma.memberUses = slices.Compact(ma.memberUses)
	return ma
}

// usersOutside lists the uses of obj in its package, test files included,
// from outside the set and outside the methods of obj when it is a type:
// those travel with it.
func (s *snapshot) usersOutside(obj types.Object, set *removalSet) []string {
	want := objectKey(s.fset, obj)
	var own []span
	if d, ok := set.index[obj]; ok {
		for _, m := range set.withMethods(d)[1:] {
			start, end := m.span()
			own = append(own, span{start, end})
		}
	}
	var out []string
	for _, p := range s.variantsOf(set.pkg) {
		for id, o := range p.TypesInfo.Uses {
			if o == nil || o.Name() != obj.Name() || objectKey(s.fset, o) != want {
				continue
			}
			if set.contains(id.Pos()) || inSpans(own, id.Pos()) {
				continue
			}
			out = append(out, s.position(s.root, id.Pos()))
		}
	}
	return sortedPositions(out)
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

func toSet(names []string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}
