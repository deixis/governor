package analysis

import (
	"context"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

// Call is one edge in the static call graph.
type Call struct {
	// Caller is the enclosing function of the call site, as Name or
	// T.Method. Calls inside function literals append " (func literal)";
	// calls in package-level initialisers use "init (package)".
	Caller string
	// Callee is the function being called, as Name, T.Method or
	// pkg.Name. Calls through function-typed values are prefixed
	// "dynamic:".
	Callee string
	// Position is the call site.
	Position string
	Package  string
	pos      token.Pos
}

// Callers returns every static call site of the function or method
// symbol across the workspace. Calls through interfaces resolve to the
// interface method, so a concrete method only lists direct calls.
func (w *Workspace) Callers(ctx context.Context, file, symbol string) ([]Call, error) {
	snap, obj, err := w.resolve(ctx, file, symbol)
	if err != nil {
		return nil, err
	}
	fn, ok := obj.(*types.Func)
	if !ok {
		return nil, fmt.Errorf("%s is a %s, expected a function or method", symbol, objectKind(obj))
	}
	root := w.Root()
	want := objectKey(snap.fset, fn)

	seen := make(map[string]bool)
	var out []Call
	for _, p := range snap.pkgs {
		if p.TypesInfo == nil || isTestMain(p) {
			continue
		}
		for _, f := range p.Syntax {
			walkCalls(f, func(call *ast.CallExpr, callee types.Object, enclosing string) {
				if callee == nil || objectKey(snap.fset, callee) != want {
					return
				}
				pos := snap.position(root, call.Pos())
				if seen[pos] {
					return
				}
				seen[pos] = true
				out = append(out, Call{
					Caller:   enclosing,
					Callee:   qualifiedName(fn),
					Position: pos,
					Package:  p.PkgPath,
					pos:      call.Pos(),
				})
			}, p)
		}
	}
	sortCalls(out)
	return out, nil
}

// Callees returns every call made inside the body of the function or
// method symbol, one entry per call site.
func (w *Workspace) Callees(ctx context.Context, file, symbol string) ([]Call, error) {
	snap, obj, err := w.resolve(ctx, file, symbol)
	if err != nil {
		return nil, err
	}
	fn, ok := obj.(*types.Func)
	if !ok {
		return nil, fmt.Errorf("%s is a %s, expected a function or method", symbol, objectKind(obj))
	}
	root := w.Root()

	// Locate the declaration.
	filename := snap.fset.Position(fn.Pos()).Filename
	pkgs := snap.byFile[filename]
	if len(pkgs) == 0 {
		return nil, fmt.Errorf("%s is declared outside the workspace", symbol)
	}
	p := pkgs[0]
	syntax := fileSyntax(p, filename)
	if syntax == nil {
		return nil, fmt.Errorf("no syntax for %s", filename)
	}
	var decl *ast.FuncDecl
	for _, d := range syntax.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Pos() == fn.Pos() {
			decl = fd
			break
		}
	}
	if decl == nil {
		return nil, fmt.Errorf("%s has no declaration in %s (interface method?)", symbol, filename)
	}
	if decl.Body == nil {
		return nil, nil
	}

	caller := qualifiedName(fn)
	var out []Call
	ast.Inspect(decl.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		callee, name := calleeOf(call, p)
		if name == "" {
			return true
		}
		if callee != nil && callee.Pkg() != nil && callee.Pkg().Path() != p.PkgPath {
			name = callee.Pkg().Name() + "." + name
		}
		out = append(out, Call{
			Caller:   caller,
			Callee:   name,
			Position: snap.position(root, call.Pos()),
			Package:  p.PkgPath,
			pos:      call.Pos(),
		})
		return true
	})
	sortCalls(out)
	return out, nil
}

// walkCalls visits every call expression in f, reporting the resolved
// callee object (nil for conversions, builtins and dynamic calls) and
// the name of the enclosing function.
func walkCalls(f *ast.File, visit func(*ast.CallExpr, types.Object, string), p *packages.Package) {
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Body == nil {
				continue
			}
			name := d.Name.Name
			if d.Recv != nil {
				name = receiverName(d.Recv) + "." + name
			}
			walkBody(d.Body, name, visit, p)
		case *ast.GenDecl:
			walkBody(d, "init (package)", visit, p)
		}
	}
}

func walkBody(root ast.Node, enclosing string, visit func(*ast.CallExpr, types.Object, string), p *packages.Package) {
	var inspect func(n ast.Node, enclosing string)
	inspect = func(n ast.Node, enclosing string) {
		ast.Inspect(n, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncLit:
				inspect(x.Body, enclosing+" (func literal)")
				return false
			case *ast.CallExpr:
				callee, _ := calleeOf(x, p)
				if fn, ok := callee.(*types.Func); ok {
					visit(x, fn, enclosing)
				}
			}
			return true
		})
	}
	inspect(root, enclosing)
}

// calleeOf resolves the callee of call. For static calls it returns the
// *types.Func and its qualified name. For calls through function-typed
// values it returns the *types.Var and "dynamic:name". Conversions,
// builtins and unresolvable callees return "".
func calleeOf(call *ast.CallExpr, p *packages.Package) (types.Object, string) {
	var id *ast.Ident
	switch fun := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		id = fun
	case *ast.SelectorExpr:
		id = fun.Sel
	case *ast.IndexExpr: // generic instantiation f[T](...)
		if x, ok := fun.X.(*ast.Ident); ok {
			id = x
		} else if sel, ok := fun.X.(*ast.SelectorExpr); ok {
			id = sel.Sel
		}
	case *ast.IndexListExpr:
		if x, ok := fun.X.(*ast.Ident); ok {
			id = x
		} else if sel, ok := fun.X.(*ast.SelectorExpr); ok {
			id = sel.Sel
		}
	}
	if id == nil {
		return nil, ""
	}
	obj := p.TypesInfo.Uses[id]
	switch o := obj.(type) {
	case *types.Func:
		return o, qualifiedName(o)
	case *types.Var:
		if _, ok := o.Type().Underlying().(*types.Signature); ok {
			return o, "dynamic:" + qualifiedName(o)
		}
	}
	return nil, ""
}

func sortCalls(calls []Call) {
	// Positions share one FileSet, so token.Pos orders by file then
	// offset. Sorting the string form would put col 21 before col 9.
	sort.Slice(calls, func(i, j int) bool {
		return calls[i].pos < calls[j].pos
	})
}

// FormatCallers renders call sites of one callee.
func FormatCallers(symbol string, calls []Call) string {
	if len(calls) == 0 {
		return fmt.Sprintf("No callers of %s.", symbol)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d call site(s) of %s:\n", len(calls), symbol)
	for _, c := range calls {
		fmt.Fprintf(&b, "%s  in %s  [%s]\n", c.Position, c.Caller, c.Package)
	}
	return b.String()
}

// FormatCallees renders calls made by one function.
func FormatCallees(symbol string, calls []Call) string {
	if len(calls) == 0 {
		return fmt.Sprintf("%s makes no calls.", symbol)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d call(s) from %s:\n", len(calls), symbol)
	for _, c := range calls {
		fmt.Fprintf(&b, "%s  %s\n", c.Position, c.Callee)
	}
	return b.String()
}
