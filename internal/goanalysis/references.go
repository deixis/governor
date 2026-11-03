package goanalysis

import (
	"context"
	"fmt"
	"go/ast"
	"go/types"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

// Reference is one use or definition of a symbol.
type Reference struct {
	Position string
	Package  string
	// Definition is true for the declaring identifier.
	Definition bool
}

// References finds every use of symbol across the workspace. The symbol is
// resolved in the scope of file and may be Foo, T.Method, T.Field, or
// pkg.Symbol where pkg is an import name in that file.
func (w *Workspace) References(ctx context.Context, file, symbol string) ([]Reference, error) {
	snap, target, err := w.resolve(ctx, file, symbol)
	if err != nil {
		return nil, err
	}
	root := w.Root()

	want := objectKey(snap.fset, target)
	seen := make(map[string]bool)
	var refs []Reference
	record := func(p *packages.Package, id *ast.Ident, def bool) {
		pos := snap.position(root, id.Pos())
		if seen[pos] {
			return
		}
		seen[pos] = true
		refs = append(refs, Reference{Position: pos, Package: p.PkgPath, Definition: def})
	}

	for _, p := range snap.pkgs {
		if p.TypesInfo == nil || isTestMain(p) {
			continue
		}
		for id, obj := range p.TypesInfo.Defs {
			if obj != nil && objectKey(snap.fset, obj) == want {
				record(p, id, true)
			}
		}
		for id, obj := range p.TypesInfo.Uses {
			if objectKey(snap.fset, obj) == want {
				record(p, id, false)
			}
		}
	}

	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Definition != refs[j].Definition {
			return refs[i].Definition
		}
		return refs[i].Position < refs[j].Position
	})
	return refs, nil
}

// resolve loads the snapshot and resolves symbol as seen from file. Each
// package variant containing the file is tried in turn: a symbol declared
// in a _test.go file is only visible in the test variant.
func (w *Workspace) resolve(ctx context.Context, file, symbol string) (*snapshot, types.Object, error) {
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return nil, nil, fmt.Errorf("symbol must not be empty")
	}
	snap, err := w.load(ctx)
	if err != nil {
		return nil, nil, err
	}
	pkgs, err := snap.packagesForFile(file)
	if err != nil {
		return nil, nil, err
	}

	var target types.Object
	var resolveErr error
	for _, p := range pkgs {
		syntax := fileSyntax(p, file)
		if syntax == nil {
			continue
		}
		target, resolveErr = resolveSymbol(p, syntax, symbol)
		if target != nil {
			break
		}
	}
	if target == nil {
		if resolveErr == nil {
			resolveErr = fmt.Errorf("symbol %q not found in %s", symbol, file)
		}
		return nil, nil, resolveErr
	}
	return snap, target, nil
}

// fileSyntax returns the parsed file for path within p, or nil.
func fileSyntax(p *packages.Package, path string) *ast.File {
	for i, f := range p.CompiledGoFiles {
		if sameFile(f, path) && i < len(p.Syntax) {
			return p.Syntax[i]
		}
	}
	return nil
}

// resolveSymbol looks up symbol as seen from syntax within p.
func resolveSymbol(p *packages.Package, syntax *ast.File, symbol string) (types.Object, error) {
	parts := strings.Split(symbol, ".")
	if len(parts) > 3 {
		return nil, fmt.Errorf("symbol %q: expected Foo, T.Member, pkg.Symbol or pkg.T.Member", symbol)
	}
	scope := p.Types.Scope()
	pkgName := p.Types.Path()

	// A leading import name switches scope to that package.
	if len(parts) > 1 {
		if fileScope := p.TypesInfo.Scopes[syntax]; fileScope != nil {
			if pn, ok := fileScope.Lookup(parts[0]).(*types.PkgName); ok {
				scope = pn.Imported().Scope()
				pkgName = pn.Imported().Path()
				parts = parts[1:]
			}
		}
	}

	switch len(parts) {
	case 1:
		if obj := scope.Lookup(parts[0]); obj != nil {
			return obj, nil
		}
		if scope != p.Types.Scope() {
			return nil, fmt.Errorf("package %s has no symbol %q", pkgName, parts[0])
		}
		return nil, nil
	case 2:
		head, member := parts[0], parts[1]
		tn, ok := scope.Lookup(head).(*types.TypeName)
		if !ok {
			if scope != p.Types.Scope() {
				return nil, fmt.Errorf("package %s has no type %q", pkgName, head)
			}
			return nil, nil
		}
		if obj := lookupMember(tn.Type(), p.Types, member); obj != nil {
			return obj, nil
		}
		return nil, fmt.Errorf("type %s has no member %q", head, member)
	}
	return nil, fmt.Errorf("symbol %q: %s is not an import name in this file", symbol, parts[0])
}

// lookupMember finds a method or field named member on t. pkg is the
// package from which unexported members are visible.
func lookupMember(t types.Type, pkg *types.Package, member string) types.Object {
	obj, _, _ := types.LookupFieldOrMethod(t, true, pkg, member)
	if obj != nil {
		return obj
	}
	// Interfaces: LookupFieldOrMethod handles them, but be explicit for
	// embedded interface methods declared elsewhere.
	if iface, ok := t.Underlying().(*types.Interface); ok {
		for m := range iface.Methods() {
			if m.Name() == member {
				return m
			}
		}
	}
	return nil
}

// FormatReferences renders references as plain text.
func FormatReferences(symbol string, refs []Reference) string {
	if len(refs) == 0 {
		return fmt.Sprintf("No references to %s.", symbol)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d reference(s) to %s:\n", len(refs), symbol)
	for _, r := range refs {
		tag := "   "
		if r.Definition {
			tag = "def"
		}
		fmt.Fprintf(&b, "%s %s  [%s]\n", tag, r.Position, r.Package)
	}
	return b.String()
}
