package goanalysis

import (
	"context"
	"fmt"
	"go/types"
	"sort"
	"strings"
	"unicode"
)

// Symbol is one search hit.
type Symbol struct {
	// Name is the qualified name: Foo, T.Method, or T.Field.
	Name string
	// Kind is func, method, type, var, const, or field.
	Kind string
	// Package is the import path.
	Package string
	// Position is file:line:col relative to the workspace root.
	Position string
	score    int
}

// maxSearchResults caps the hits returned by Search.
const maxSearchResults = 50

// Search performs a fuzzy, case-insensitive subsequence match of query
// against every package-level symbol, method and struct field in the
// workspace. Results are ranked: exact match, prefix, then contiguous
// substring, then scattered subsequence.
func (w *Workspace) Search(ctx context.Context, query string) ([]Symbol, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("query must not be empty")
	}
	snap, err := w.load(ctx)
	if err != nil {
		return nil, err
	}
	root := w.Root()

	seen := make(map[string]bool)
	var hits []Symbol
	add := func(name, kind string, obj types.Object, pkgPath string) {
		key := pkgPath + "." + name
		if seen[key] {
			return
		}
		score, ok := fuzzyScore(query, name)
		if !ok {
			return
		}
		seen[key] = true
		hits = append(hits, Symbol{
			Name:     name,
			Kind:     kind,
			Package:  pkgPath,
			Position: snap.position(root, obj.Pos()),
			score:    score,
		})
	}

	for _, p := range snap.pkgs {
		if p.Types == nil || isTestMain(p) {
			continue
		}
		scope := p.Types.Scope()
		for _, name := range scope.Names() {
			obj := scope.Lookup(name)
			add(name, objectKind(obj), obj, p.PkgPath)

			tn, ok := obj.(*types.TypeName)
			if !ok {
				continue
			}
			// Methods: declared on the named type (not interface methods,
			// which are part of the type's definition).
			if named, ok := tn.Type().(*types.Named); ok {
				for m := range named.Methods() {
					add(name+"."+m.Name(), "method", m, p.PkgPath)
				}
			}
			if st, ok := tn.Type().Underlying().(*types.Struct); ok {
				for f := range st.Fields() {
					add(name+"."+f.Name(), "field", f, p.PkgPath)
				}
			}
		}
	}

	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		if len(hits[i].Name) != len(hits[j].Name) {
			return len(hits[i].Name) < len(hits[j].Name)
		}
		if hits[i].Package != hits[j].Package {
			return hits[i].Package < hits[j].Package
		}
		return hits[i].Name < hits[j].Name
	})
	if len(hits) > maxSearchResults {
		hits = hits[:maxSearchResults]
	}
	return hits, nil
}

// fuzzyScore matches query against name case-insensitively. Higher is
// better. ok is false when query is not a subsequence of name.
func fuzzyScore(query, name string) (int, bool) {
	q := strings.ToLower(query)
	n := strings.ToLower(name)

	// For qualified names (T.Method), also try the last segment alone so
	// "method" ranks T.Method as a prefix hit rather than a scattered one.
	last := n
	if i := strings.LastIndex(n, "."); i >= 0 {
		last = n[i+1:]
	}

	switch {
	case n == q || last == q:
		return 1000, true
	case strings.HasPrefix(last, q):
		return 800 - len(last), true
	case strings.HasPrefix(n, q):
		return 700 - len(n), true
	case strings.Contains(n, q):
		return 500 - len(n), true
	}

	// Scattered subsequence: reward matches on word boundaries
	// (uppercase in CamelCase, after '.' or '_').
	qi := 0
	bonus := 0
	for i, r := range name {
		if qi == len(q) {
			break
		}
		if unicode.ToLower(r) == rune(q[qi]) {
			if i == 0 || unicode.IsUpper(r) || name[i-1] == '.' || name[i-1] == '_' {
				bonus += 10
			}
			qi++
		}
	}
	if qi != len(q) {
		return 0, false
	}
	return 100 + bonus - len(n), true
}

// objectKind names the declaration kind of obj.
func objectKind(obj types.Object) string {
	switch o := obj.(type) {
	case *types.Func:
		if o.Type().(*types.Signature).Recv() != nil {
			return "method"
		}
		return "func"
	case *types.TypeName:
		if _, ok := o.Type().(*types.Alias); ok {
			return "alias"
		}
		if _, ok := o.Type().Underlying().(*types.Interface); ok {
			return "interface"
		}
		return "type"
	case *types.Var:
		if o.IsField() {
			return "field"
		}
		return "var"
	case *types.Const:
		return "const"
	case *types.PkgName:
		return "package"
	case *types.Label:
		return "label"
	case *types.Builtin:
		return "builtin"
	}
	return "symbol"
}

// FormatSymbols renders search hits as plain text.
func FormatSymbols(syms []Symbol) string {
	if len(syms) == 0 {
		return "No symbols found."
	}
	var b strings.Builder
	for _, s := range syms {
		fmt.Fprintf(&b, "%-9s %s.%s\t%s\n", s.Kind, s.Package, s.Name, s.Position)
	}
	return b.String()
}
