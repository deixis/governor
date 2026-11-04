package analysis

import (
	"context"
	"fmt"
	"go/ast"
	"go/types"
	"path/filepath"
	"sort"
	"strings"
)

// FileContext describes what one file depends on beyond its own text.
type FileContext struct {
	File    string
	Package string
	// Imports lists import paths used by the file.
	Imports []string
	// SamePackage lists symbols the file uses that are declared in other
	// files of the same package, each as "Name  file:line:col".
	SamePackage []string
	// External maps import path to the exported symbols used from it.
	External map[string][]string
}

// FileContextFor reports the cross-file and cross-package dependencies of
// file: its imports, the same-package symbols it uses from sibling files,
// and the exported symbols it uses from each imported package.
func (w *Workspace) FileContextFor(ctx context.Context, file string) (*FileContext, error) {
	snap, err := w.load(ctx)
	if err != nil {
		return nil, err
	}
	root := snap.root

	pkgs, err := snap.packagesForFile(file)
	if err != nil {
		return nil, err
	}
	p := pkgs[0]

	syntax := fileSyntax(p, file)
	if syntax == nil {
		return nil, fmt.Errorf("no syntax tree for %s", file)
	}
	thisFile := snap.fset.File(syntax.Pos())

	fc := &FileContext{
		File:     relPath(root, file),
		Package:  p.PkgPath,
		External: make(map[string][]string),
	}
	for _, imp := range syntax.Imports {
		fc.Imports = append(fc.Imports, strings.Trim(imp.Path.Value, `"`))
	}

	sameSeen := make(map[string]bool)
	extSeen := make(map[string]bool)

	ast.Inspect(syntax, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		obj := p.TypesInfo.Uses[id]
		if obj == nil || obj.Pkg() == nil {
			return true
		}
		if _, isPkgName := obj.(*types.PkgName); isPkgName {
			return true
		}

		if obj.Pkg().Path() == p.PkgPath {
			// Same package: only count objects declared in another file.
			declFile := snap.fset.File(obj.Pos())
			if declFile == nil || declFile == thisFile {
				return true
			}
			name := qualifiedName(obj)
			if sameSeen[name] {
				return true
			}
			sameSeen[name] = true
			fc.SamePackage = append(fc.SamePackage,
				fmt.Sprintf("%s  %s", name, snap.position(root, obj.Pos())))
			return true
		}

		// External: only exported, top-level objects or methods.
		if !obj.Exported() {
			return true
		}
		name := qualifiedName(obj)
		key := obj.Pkg().Path() + "." + name
		if extSeen[key] {
			return true
		}
		extSeen[key] = true
		fc.External[obj.Pkg().Path()] = append(fc.External[obj.Pkg().Path()], name)
		return true
	})

	sort.Strings(fc.Imports)
	sort.Strings(fc.SamePackage)
	for k := range fc.External {
		sort.Strings(fc.External[k])
	}
	return fc, nil
}

// qualifiedName returns Name for package-level objects, T.Name for
// methods and fields, and Name for anything else.
func qualifiedName(obj types.Object) string {
	switch o := obj.(type) {
	case *types.Func:
		if recv := o.Type().(*types.Signature).Recv(); recv != nil {
			return recvTypeName(recv.Type()) + "." + o.Name()
		}
	case *types.Var:
		if o.IsField() {
			return "." + o.Name()
		}
	}
	return obj.Name()
}

// relPath makes file relative to root when it lives under it, comparing
// symlink-resolved forms when the paths as given do not match.
func relPath(root, file string) string {
	if strings.HasPrefix(file, "$") {
		// Export-data placeholder such as $GOROOT/src/fmt/print.go; making
		// it absolute would anchor it under the working directory.
		return file
	}
	abs, err := filepath.Abs(file)
	if err != nil {
		return file
	}
	if rel, err := filepath.Rel(root, abs); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	if rel, err := filepath.Rel(canonical(root), canonical(abs)); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return abs
}

// FormatFileContext renders a FileContext as plain text.
func FormatFileContext(fc *FileContext) string {
	var b strings.Builder
	fmt.Fprintf(&b, "File: %s\nPackage: %s\n", fc.File, fc.Package)

	fmt.Fprintf(&b, "\nImports (%d):\n", len(fc.Imports))
	for _, imp := range fc.Imports {
		fmt.Fprintf(&b, "  %s\n", imp)
	}

	fmt.Fprintf(&b, "\nUses from other files in package (%d):\n", len(fc.SamePackage))
	for _, s := range fc.SamePackage {
		fmt.Fprintf(&b, "  %s\n", s)
	}

	paths := make([]string, 0, len(fc.External))
	for k := range fc.External {
		paths = append(paths, k)
	}
	sort.Strings(paths)
	fmt.Fprintf(&b, "\nUses from imported packages (%d):\n", len(paths))
	for _, path := range paths {
		fmt.Fprintf(&b, "  %s: %s\n", path, strings.Join(fc.External[path], ", "))
	}
	return b.String()
}
