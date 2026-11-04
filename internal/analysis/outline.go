package analysis

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// OutlineEntry is one top-level declaration.
type OutlineEntry struct {
	File      string
	StartLine int
	EndLine   int
	Kind      string
	// Summary is a one-line rendering: the signature for functions, the
	// name plus a shape hint for types, names plus type for values.
	Summary string
	// Standalone marks a file parsed on its own because it belongs to no
	// loaded package: syntax only, no type information behind it.
	Standalone bool
}

// Outline lists the top-level declarations of a file (absolute path) or
// a package (import path), with line ranges and one-line summaries.
// Bodies are never included. Test files are included for workspace
// packages. An import path outside the workspace (standard library,
// dependency) is loaded on demand without tests. A .go file or a
// directory outside any loaded package is parsed on its own.
func (w *Workspace) Outline(ctx context.Context, target string) ([]OutlineEntry, error) {
	snap, err := w.load(ctx)
	if err != nil {
		return nil, err
	}
	root := snap.root

	var files []*ast.File
	fset := snap.fset
	standalone := false
	if strings.HasSuffix(target, ".go") || filepath.IsAbs(target) {
		pkgs, err := snap.packagesForFile(target)
		if err == nil {
			// The test variant (if any) parses the same file; one copy suffices.
			f := fileSyntax(pkgs[0], target)
			if f == nil {
				return nil, fmt.Errorf("no syntax tree for %s", target)
			}
			files = append(files, f)
		} else {
			fset = token.NewFileSet()
			files, err = parseStandalone(fset, target)
			if err != nil {
				return nil, err
			}
			standalone = true
		}
	} else {
		seen := make(map[string]bool)
		for _, p := range snap.pkgs {
			if (p.PkgPath != target && p.PkgPath != target+"_test") || isTestMain(p) {
				continue
			}
			for i, name := range p.CompiledGoFiles {
				if seen[name] || i >= len(p.Syntax) {
					continue
				}
				seen[name] = true
				files = append(files, p.Syntax[i])
			}
		}
		if len(files) == 0 {
			// Not a workspace package: standard library or dependency.
			p, err := snap.externalPackage(ctx, target)
			if err != nil {
				return nil, err
			}
			files = p.Syntax
		}
	}

	var out []OutlineEntry
	for _, f := range files {
		file := relPath(root, fset.Position(f.Pos()).Filename)
		for _, d := range f.Decls {
			entries := outlineDecl(fset, file, d)
			for i := range entries {
				entries[i].Standalone = standalone
			}
			out = append(out, entries...)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].StartLine < out[j].StartLine
	})
	return out, nil
}

// parseStandalone parses a Go file, or every .go file directly inside a
// directory, without type-checking. For files outside any loaded
// package: a downloaded file, a cloned repository, a sibling module.
func parseStandalone(fset *token.FileSet, target string) ([]*ast.File, error) {
	info, err := os.Stat(target)
	if err != nil {
		return nil, fmt.Errorf("file %s is not part of any loaded package and cannot be read: %w", target, err)
	}
	var paths []string
	if info.IsDir() {
		matches, err := filepath.Glob(filepath.Join(target, "*.go"))
		if err != nil {
			return nil, err
		}
		paths = matches
		if len(paths) == 0 {
			return nil, fmt.Errorf("no Go files in %s", target)
		}
	} else {
		paths = []string{target}
	}
	var files []*ast.File
	for _, path := range paths {
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("parsing %s: %w", path, err)
		}
		files = append(files, f)
	}
	return files, nil
}

// outlineDecl renders one declaration. Grouped var/const/type blocks
// produce one entry per spec.
func outlineDecl(fset *token.FileSet, file string, d ast.Decl) []OutlineEntry {
	entry := func(n ast.Node, kind, summary string) OutlineEntry {
		return OutlineEntry{
			File:      file,
			StartLine: fset.Position(n.Pos()).Line,
			EndLine:   fset.Position(n.End()).Line,
			Kind:      kind,
			Summary:   summary,
		}
	}

	switch d := d.(type) {
	case *ast.FuncDecl:
		kind := "func"
		if d.Recv != nil {
			kind = "method"
		}
		sig := stripBody(d)
		sig.Doc = nil
		return []OutlineEntry{entry(d, kind, oneLine(fset, sig))}

	case *ast.GenDecl:
		if d.Tok == token.IMPORT {
			return nil
		}
		var out []OutlineEntry
		for _, spec := range d.Specs {
			switch s := spec.(type) {
			case *ast.TypeSpec:
				out = append(out, entry(s, "type", "type "+s.Name.Name+typeParams(fset, s)+" "+typeShape(fset, s.Type)))
			case *ast.ValueSpec:
				names := make([]string, len(s.Names))
				for i, n := range s.Names {
					names[i] = n.Name
				}
				summary := strings.ToLower(d.Tok.String()) + " " + strings.Join(names, ", ")
				if s.Type != nil {
					summary += " " + oneLine(fset, s.Type)
				} else if len(s.Values) > 0 && len(s.Values) == len(s.Names) && isLiteral(s.Values[0]) {
					summary += " = " + oneLine(fset, s.Values[0])
				}
				out = append(out, entry(s, strings.ToLower(d.Tok.String()), summary))
			}
		}
		return out
	}
	return nil
}

// typeShape summarises a type expression: struct and interface sizes,
// everything else printed as is.
func typeShape(fset *token.FileSet, t ast.Expr) string {
	switch x := t.(type) {
	case *ast.StructType:
		return fmt.Sprintf("struct (%d fields)", x.Fields.NumFields())
	case *ast.InterfaceType:
		return fmt.Sprintf("interface (%d methods)", x.Methods.NumFields())
	}
	return oneLine(fset, t)
}

func typeParams(fset *token.FileSet, s *ast.TypeSpec) string {
	if s.TypeParams == nil {
		return ""
	}
	var parts []string
	for _, f := range s.TypeParams.List {
		var names []string
		for _, n := range f.Names {
			names = append(names, n.Name)
		}
		parts = append(parts, strings.Join(names, ", ")+" "+oneLine(fset, f.Type))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func isLiteral(e ast.Expr) bool {
	_, ok := e.(*ast.BasicLit)
	return ok
}

// oneLine prints n with go/printer and collapses whitespace.
func oneLine(fset *token.FileSet, n ast.Node) string {
	var buf bytes.Buffer
	cfg := printer.Config{Mode: printer.RawFormat}
	if err := cfg.Fprint(&buf, fset, n); err != nil {
		return "?"
	}
	s := strings.Join(strings.Fields(buf.String()), " ")
	if r := []rune(s); len(r) > 160 {
		s = string(r[:157]) + "..."
	}
	return s
}

// FormatOutline renders outline entries grouped by file.
func FormatOutline(entries []OutlineEntry) string {
	if len(entries) == 0 {
		return "No declarations."
	}
	var b strings.Builder
	if entries[0].Standalone {
		b.WriteString("Parsed without type information: not part of any loaded package.\n\n")
	}
	current := ""
	for _, e := range entries {
		if e.File != current {
			if current != "" {
				b.WriteByte('\n')
			}
			current = e.File
			fmt.Fprintf(&b, "%s\n", e.File)
		}
		lines := fmt.Sprintf("%d", e.StartLine)
		if e.EndLine != e.StartLine {
			lines = fmt.Sprintf("%d-%d", e.StartLine, e.EndLine)
		}
		fmt.Fprintf(&b, "  %-9s %s\n", lines, e.Summary)
	}
	return b.String()
}
