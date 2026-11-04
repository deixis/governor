package analysis

import (
	"context"
	"fmt"
	"go/types"
	"sort"
	"strings"
)

// Implementation is one type or method related to a query symbol through
// interface satisfaction.
type Implementation struct {
	// Name is the type name, or T.Method for method queries.
	Name     string
	Kind     string // "type", "interface", or "method"
	Package  string
	Position string
	// Pointer is true when only *T satisfies the interface.
	Pointer bool
}

// Implementations relates symbol to the workspace through interface
// satisfaction:
//   - interface type: concrete and interface types that satisfy it
//   - concrete type: workspace interfaces it satisfies
//   - interface method: methods on satisfying types with that name
//   - concrete method: interface methods it implements
//
// The empty interface and interfaces with no methods are skipped. A
// symbol declared in a dependency or the standard library is also
// matched against the types of its own package.
func (w *Workspace) Implementations(ctx context.Context, file, symbol string) ([]Implementation, error) {
	snap, obj, err := w.resolve(ctx, file, symbol)
	if err != nil {
		return nil, err
	}
	root := snap.root

	// Method query: reduce to its type, then filter by name.
	methodName := ""
	if fn, ok := obj.(*types.Func); ok {
		recv := fn.Type().(*types.Signature).Recv()
		if recv == nil {
			return nil, fmt.Errorf("%s is a function, not a type or method", symbol)
		}
		methodName = fn.Name()
		obj = nil
		// The type named in the query, not the receiver: a promoted
		// method's receiver is the embedded type that declares it.
		symbol = strings.TrimSpace(symbol)
		if i := strings.LastIndex(symbol, "."); i > 0 {
			if t, err := snap.lookup(file, symbol[:i]); err == nil {
				if tn, ok := t.(*types.TypeName); ok {
					obj = tn
				}
			}
		}
		if obj == nil {
			t := recv.Type()
			if ptr, ok := t.(*types.Pointer); ok {
				t = ptr.Elem()
			}
			named, ok := t.(*types.Named)
			if !ok {
				return nil, fmt.Errorf("receiver of %s is not a named type", symbol)
			}
			obj = named.Obj()
		}
	}

	tn, ok := obj.(*types.TypeName)
	if !ok {
		return nil, fmt.Errorf("%s is a %s, expected a type or method", symbol, objectKind(obj))
	}
	queryType := tn.Type()
	queryKey := objectKey(snap.fset, tn)
	_, queryIsIface := queryType.Underlying().(*types.Interface)

	seen := make(map[string]bool)
	var out []Implementation
	for _, p := range snap.scanPackages(ctx, tn) {
		if p.Types == nil || isTestMain(p) {
			continue
		}
		scope := p.Types.Scope()
		for _, name := range scope.Names() {
			cand, ok := scope.Lookup(name).(*types.TypeName)
			if !ok || cand.IsAlias() {
				continue
			}
			key := objectKey(snap.fset, cand)
			if key == queryKey || seen[key] {
				continue
			}
			candType := cand.Type()
			candIface, candIsIface := candType.Underlying().(*types.Interface)

			var match, pointer bool
			switch {
			case queryIsIface:
				match, pointer = satisfies(candType, queryType.Underlying().(*types.Interface))
			case candIsIface:
				if candIface.NumMethods() == 0 {
					continue
				}
				match, pointer = satisfies(queryType, candIface)
			}
			if !match {
				continue
			}
			seen[key] = true

			if methodName != "" {
				m, _ := lookupMember(candType, p.Types, methodName).(*types.Func)
				if m == nil {
					continue
				}
				out = append(out, Implementation{
					Name:     cand.Name() + "." + methodName,
					Kind:     "method",
					Package:  p.PkgPath,
					Position: snap.position(root, m.Pos()),
					Pointer:  pointer,
				})
				continue
			}
			kind := "type"
			if candIsIface {
				kind = "interface"
			}
			out = append(out, Implementation{
				Name:     cand.Name(),
				Kind:     kind,
				Package:  p.PkgPath,
				Position: snap.position(root, cand.Pos()),
				Pointer:  pointer,
			})
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Package != out[j].Package {
			return out[i].Package < out[j].Package
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// satisfies reports whether t or *t implements iface. Interfaces with no
// methods never count. pointer is true when only *t does.
func satisfies(t types.Type, iface *types.Interface) (match, pointer bool) {
	if iface.NumMethods() == 0 {
		return false, false
	}
	if types.Implements(t, iface) {
		return true, false
	}
	if _, isIface := t.Underlying().(*types.Interface); isIface {
		return false, false
	}
	if types.Implements(types.NewPointer(t), iface) {
		return true, true
	}
	return false, false
}

// FormatImplementations renders implementations as plain text.
func FormatImplementations(symbol string, impls []Implementation) string {
	if len(impls) == 0 {
		return fmt.Sprintf("No implementations related to %s.", symbol)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d result(s) for %s:\n", len(impls), symbol)
	for _, im := range impls {
		name := im.Name
		if im.Pointer {
			name = "*" + name
		}
		fmt.Fprintf(&b, "%-9s %s.%s\t%s\n", im.Kind, im.Package, name, im.Position)
	}
	return b.String()
}
