package analysis

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/printer"
	"go/token"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/packages"
)

// PackageAPI renders the exported declarations of each package in Go
// syntax, keeping doc comments and dropping function bodies. Import paths
// outside the workspace (standard library, dependencies) are loaded on
// demand. Unknown import paths produce an inline note rather than an
// error so one bad path does not hide the others.
func (w *Workspace) PackageAPI(ctx context.Context, importPaths []string) (string, error) {
	snap, err := w.load(ctx)
	if err != nil {
		return "", err
	}

	byPath := make(map[string]*packages.Package)
	for _, p := range snap.pkgs {
		// Plain variant only; the test variant duplicates every decl.
		if p.ID == p.PkgPath {
			byPath[p.PkgPath] = p
		}
	}

	var b strings.Builder
	for i, path := range importPaths {
		if i > 0 {
			b.WriteString("\n")
		}
		// A file path stands for its package.
		if filepath.IsAbs(path) {
			pkgs, err := snap.packagesForFile(path)
			if err != nil {
				fmt.Fprintf(&b, "// %v\n", err)
				continue
			}
			path = pkgs[0].PkgPath
		}
		p, ok := byPath[path]
		if !ok {
			ext, err := snap.externalPackage(ctx, path)
			if err != nil {
				fmt.Fprintf(&b, "// %v\n", err)
				continue
			}
			p = ext
		}
		fmt.Fprintf(&b, "// import %q\n", p.PkgPath)
		fmt.Fprintf(&b, "package %s\n\n", p.Name)
		if err := writeExportedDecls(&b, snap.fset, p); err != nil {
			return "", err
		}
	}
	return b.String(), nil
}

// writeExportedDecls prints every exported top-level declaration of p
// with its doc comment. Methods on exported types are printed after the
// type, bodies stripped.
func writeExportedDecls(b *strings.Builder, fset *token.FileSet, p *packages.Package) error {
	cfg := printer.Config{Mode: printer.UseSpaces | printer.TabIndent, Tabwidth: 8}

	// Collect methods by receiver type name so they print under the type.
	methods := make(map[string][]*ast.FuncDecl)
	var funcs []*ast.FuncDecl
	var gens []*ast.GenDecl

	for _, f := range p.Syntax {
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if !d.Name.IsExported() {
					continue
				}
				if d.Recv == nil {
					funcs = append(funcs, d)
					continue
				}
				recv := receiverName(d.Recv)
				if recv == "" || !token.IsExported(recv) {
					continue
				}
				methods[recv] = append(methods[recv], d)
			case *ast.GenDecl:
				if g := exportedGenDecl(d); g != nil {
					gens = append(gens, g)
				}
			}
		}
	}

	printDecl := func(d ast.Decl) error {
		var buf bytes.Buffer
		if err := cfg.Fprint(&buf, fset, d); err != nil {
			return err
		}
		b.Write(buf.Bytes())
		b.WriteString("\n\n")
		return nil
	}

	for _, g := range gens {
		if err := printDecl(g); err != nil {
			return err
		}
		if g.Tok != token.TYPE {
			continue
		}
		for _, spec := range g.Specs {
			ts := spec.(*ast.TypeSpec)
			for _, m := range methods[ts.Name.Name] {
				if err := printDecl(stripBody(m)); err != nil {
					return err
				}
			}
			delete(methods, ts.Name.Name)
		}
	}
	for _, f := range funcs {
		if err := printDecl(stripBody(f)); err != nil {
			return err
		}
	}
	return nil
}

// receiverName returns the bare type name of a method receiver, or "".
func receiverName(recv *ast.FieldList) string {
	if recv == nil || len(recv.List) == 0 {
		return ""
	}
	t := recv.List[0].Type
	for {
		switch x := t.(type) {
		case *ast.StarExpr:
			t = x.X
		case *ast.IndexExpr:
			t = x.X
		case *ast.IndexListExpr:
			t = x.X
		case *ast.Ident:
			return x.Name
		default:
			return ""
		}
	}
}

// stripBody returns a shallow copy of fn without its body.
func stripBody(fn *ast.FuncDecl) *ast.FuncDecl {
	c := *fn
	c.Body = nil
	return &c
}

// exportedGenDecl returns a copy of d holding only exported specs, with
// struct fields and interface methods filtered to exported ones. Returns
// nil when nothing is exported. Import declarations are dropped.
func exportedGenDecl(d *ast.GenDecl) *ast.GenDecl {
	if d.Tok == token.IMPORT {
		return nil
	}
	c := *d
	c.Specs = nil
	for _, spec := range d.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			if !s.Name.IsExported() {
				continue
			}
			cs := *s
			cs.Type = filterExportedType(s.Type)
			c.Specs = append(c.Specs, &cs)
		case *ast.ValueSpec:
			var names []*ast.Ident
			for _, n := range s.Names {
				if n.IsExported() {
					names = append(names, n)
				}
			}
			if len(names) == 0 {
				continue
			}
			cs := *s
			if len(names) != len(s.Names) {
				// Values are positional; dropping some names would misalign
				// them. Keep names only and let the type speak.
				cs.Values = nil
			}
			cs.Names = names
			c.Specs = append(c.Specs, &cs)
		}
	}
	if len(c.Specs) == 0 {
		return nil
	}
	if len(c.Specs) == 1 && d.Lparen.IsValid() {
		// Collapse "type ( X ... )" to "type X ..." for readability. The
		// spec's doc comment moves to the declaration: printed on the spec
		// it would split "const" from its name.
		c.Lparen = token.NoPos
		c.Rparen = token.NoPos
		switch s := c.Specs[0].(type) {
		case *ast.TypeSpec:
			if c.Doc == nil {
				c.Doc = s.Doc
			}
			s.Doc = nil
		case *ast.ValueSpec:
			if c.Doc == nil {
				c.Doc = s.Doc
			}
			s.Doc = nil
		}
	}
	return &c
}

// filterExportedType removes unexported fields from struct types and
// unexported methods from interface types, recursively.
func filterExportedType(t ast.Expr) ast.Expr {
	switch x := t.(type) {
	case *ast.StructType:
		c := *x
		fields := &ast.FieldList{Opening: x.Fields.Opening, Closing: x.Fields.Closing}
		hidden := 0
		for _, f := range x.Fields.List {
			if len(f.Names) == 0 {
				// Embedded field: exported if its type name is.
				if token.IsExported(embeddedName(f.Type)) {
					fields.List = append(fields.List, f)
				} else {
					hidden++
				}
				continue
			}
			var names []*ast.Ident
			for _, n := range f.Names {
				if n.IsExported() {
					names = append(names, n)
				} else {
					hidden++
				}
			}
			if len(names) == 0 {
				continue
			}
			cf := *f
			cf.Names = names
			cf.Type = filterExportedType(f.Type)
			fields.List = append(fields.List, &cf)
		}
		if hidden > 0 {
			fields.List = append(fields.List, &ast.Field{
				Names: []*ast.Ident{ast.NewIdent("_")},
				Type:  ast.NewIdent(fmt.Sprintf("struct{} // %d unexported field(s)", hidden)),
			})
		}
		c.Fields = fields
		return &c
	case *ast.InterfaceType:
		c := *x
		methods := &ast.FieldList{Opening: x.Methods.Opening, Closing: x.Methods.Closing}
		for _, m := range x.Methods.List {
			if len(m.Names) == 0 || m.Names[0].IsExported() {
				methods.List = append(methods.List, m)
			}
		}
		c.Methods = methods
		return &c
	}
	return t
}

// embeddedName returns the type name of an embedded field expression.
func embeddedName(t ast.Expr) string {
	for {
		switch x := t.(type) {
		case *ast.StarExpr:
			t = x.X
		case *ast.SelectorExpr:
			return x.Sel.Name
		case *ast.IndexExpr:
			t = x.X
		case *ast.IndexListExpr:
			t = x.X
		case *ast.Ident:
			return x.Name
		default:
			return ""
		}
	}
}
