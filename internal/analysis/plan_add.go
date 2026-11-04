package analysis

import (
	"context"
	"fmt"
	"go/ast"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"golang.org/x/tools/go/packages"
)

// fileTarget is where add puts its declarations or move sends its code:
// the package and the file, either of which may not exist yet.
type fileTarget struct {
	pkg     *packages.Package
	file    string // absolute path
	newFile bool
	// newPkg marks a package that does not exist yet: a directory under
	// the main module with no Go files, or an import path below the
	// module path. pkg is then a stand-in with an empty scope.
	newPkg bool
}

// where renders the target for a summary.
func (t fileTarget) where(root string) string {
	w := relPath(root, t.file)
	switch {
	case t.newPkg:
		w += " (new package)"
	case t.newFile:
		w += " (new file)"
	}
	return w
}

// resolveTarget accepts a package import path, an absolute directory, or
// an absolute file path. With a package or directory the file is newName
// inside it. A file that exists must belong to a loaded package. A
// directory or import path with no loaded package is a new package when
// it sits under the main module.
func (s *snapshot) resolveTarget(target, newName string) (fileTarget, error) {
	if target == "" {
		return fileTarget{}, fmt.Errorf("target (package import path or file path) is required")
	}
	var pkg *packages.Package
	abs := target
	if !filepath.IsAbs(target) {
		pkg = s.packageByPath(target)
		if pkg != nil {
			abs = filepath.Join(pkg.Dir, newName)
		} else {
			dir, ok := s.dirForImportPath(target)
			if !ok {
				return fileTarget{}, fmt.Errorf("package %s not found in workspace", target)
			}
			abs = filepath.Join(dir, newName)
		}
	}
	abs = canonical(abs)
	if st, err := os.Stat(abs); err == nil && st.IsDir() {
		abs = filepath.Join(abs, newName)
	}
	if pkgs := s.byFile[abs]; len(pkgs) > 0 {
		return fileTarget{pkg: pkgs[0], file: abs}, nil
	}
	if _, err := os.Stat(abs); err == nil {
		if mod := s.mainModule(); mod != nil {
			if nested := nestedModule(canonical(mod.Dir), canonical(filepath.Dir(abs))); nested != "" {
				return fileTarget{}, fmt.Errorf("file %s is in the module at %s, outside the workspace", relPath(s.root, abs), relPath(s.root, nested))
			}
		}
		return fileTarget{}, fmt.Errorf("file %s exists but is not part of any loaded package", relPath(s.root, abs))
	}
	if pkg == nil {
		dir := canonical(filepath.Dir(abs))
		for _, p := range s.pkgs {
			if p.ID == p.PkgPath && canonical(p.Dir) == dir {
				pkg = p
				break
			}
		}
		if pkg == nil {
			np, err := s.newPackage(dir)
			if err != nil {
				return fileTarget{}, err
			}
			return fileTarget{pkg: np, file: abs, newFile: true, newPkg: true}, nil
		}
	}
	return fileTarget{pkg: pkg, file: abs, newFile: true}, nil
}

// mainModule returns the module the workspace was loaded from, or nil.
func (s *snapshot) mainModule() *packages.Module {
	for _, p := range s.pkgs {
		if p.Module != nil && p.Module.Main {
			return p.Module
		}
	}
	return nil
}

// dirForImportPath maps an import path below the main module to its
// directory.
func (s *snapshot) dirForImportPath(path string) (string, bool) {
	mod := s.mainModule()
	if mod == nil || mod.Dir == "" {
		return "", false
	}
	if path == mod.Path {
		return mod.Dir, true
	}
	rest, ok := strings.CutPrefix(path, mod.Path+"/")
	if !ok || rest == "" || strings.Contains(rest, "..") {
		return "", false
	}
	return filepath.Join(mod.Dir, filepath.FromSlash(rest)), true
}

// newPackage builds a stand-in for a package that would be created in
// dir: import path from the main module, name from the directory.
func (s *snapshot) newPackage(dir string) (*packages.Package, error) {
	mod := s.mainModule()
	if mod == nil || mod.Dir == "" {
		return nil, fmt.Errorf("directory %s holds no loaded package", dir)
	}
	modDir := canonical(mod.Dir)
	rel, err := filepath.Rel(modDir, dir)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return nil, fmt.Errorf("directory %s is outside the main module %s", dir, mod.Path)
	}
	if nested := nestedModule(modDir, dir); nested != "" {
		return nil, fmt.Errorf("directory %s is in the module at %s, outside the workspace", relPath(s.root, dir), relPath(s.root, nested))
	}
	for el := range strings.SplitSeq(filepath.ToSlash(rel), "/") {
		if el == "testdata" || el == "vendor" || strings.HasPrefix(el, ".") || strings.HasPrefix(el, "_") {
			return nil, fmt.Errorf("directory %s cannot hold a package (%s)", dir, el)
		}
	}
	path := mod.Path + "/" + filepath.ToSlash(rel)
	name := strings.NewReplacer("-", "_", ".", "_").Replace(filepath.Base(dir))
	return &packages.Package{
		ID:      path,
		PkgPath: path,
		Name:    name,
		Dir:     dir,
		Types:   types.NewPackage(path, name),
		Imports: map[string]*packages.Package{},
		Module:  mod,
	}, nil
}

// nestedModule returns the directory of a go.mod between dir (included)
// and root (excluded), or "": a package there belongs to another module.
func nestedModule(root, dir string) string {
	for d := dir; d != root && strings.HasPrefix(d, root); d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
	}
	return ""
}

// fileNameFor derives a file name from the first declaration: snake case
// of its name, methods using the receiver type.
func fileNameFor(decls []ast.Decl) string {
	names := declNames(decls[0])
	if len(names) == 0 {
		return "new.go"
	}
	name := names[0]
	if after, ok := strings.CutPrefix(name, "method "); ok {
		name = after
		name = name[:strings.IndexByte(name, '.')]
	} else {
		name = bareName(name)
	}
	return snakeCase(name) + ".go"
}

// snakeCase lowers a Go name with underscores at word boundaries. A run
// of capitals is one word: HTTPClient is http_client, SG is sg.
func snakeCase(name string) string {
	rs := []rune(name)
	var b strings.Builder
	for i, r := range rs {
		if unicode.IsUpper(r) {
			prevLower := i > 0 && !unicode.IsUpper(rs[i-1]) && rs[i-1] != '_'
			nextLower := i+1 < len(rs) && unicode.IsLower(rs[i+1])
			if i > 0 && (prevLower || (nextLower && unicode.IsUpper(rs[i-1]))) {
				b.WriteByte('_')
			}
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (s *snapshot) planAdd(ctx context.Context, req Request) (*Report, error) {
	if strings.TrimSpace(req.Source) == "" {
		return nil, fmt.Errorf("source is required")
	}
	src, err := parseSource(req.Source)
	if err != nil {
		return nil, err
	}
	target := req.Target
	if target == "" {
		target = req.File
	}
	dest, err := s.resolveTarget(target, fileNameFor(src.decls))
	if err != nil {
		return nil, err
	}

	var names []string
	for _, d := range src.decls {
		names = append(names, declNames(d)...)
	}
	r := &Report{Op: "add", Subject: strings.Join(names, ", ")}

	scope := dest.pkg.Types.Scope()
	index := s.decls(dest.pkg)
	for _, d := range src.decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv != nil {
			recv := receiverName(fn.Recv)
			if recv == "" {
				r.block(fmt.Sprintf("method %s: receiver type %s belongs to another package", fn.Name.Name, printNode(fn.Recv.List[0].Type)),
					"declare the method in the package that declares the type, or wrap the type in a local one")
				continue
			}
			tn, ok := scope.Lookup(recv).(*types.TypeName)
			if !ok {
				r.block(fmt.Sprintf("method %s.%s: type %s is not declared in %s", recv, fn.Name.Name, recv, dest.pkg.PkgPath),
					"add the type first, or target the package that declares it")
				continue
			}
			for _, m := range methodsOf(index, tn) {
				if m.name.Name == fn.Name.Name {
					r.block(fmt.Sprintf("method %s.%s already exists at %s", recv, fn.Name.Name, s.position(s.root, m.name.Pos())),
						"use op replace to change it, or pick another name")
				}
			}
			continue
		}
		for _, labelled := range declNames(d) {
			name := bareName(labelled)
			if name == "init" || name == "_" {
				continue
			}
			if obj := scope.Lookup(name); obj != nil {
				r.block(fmt.Sprintf("%s already exists at %s", labelled, s.position(s.root, obj.Pos())),
					"use op replace to change it, or pick another name")
			}
		}
	}
	if !dest.newFile {
		if f := fileSyntax(dest.pkg, dest.file); f != nil && isGenerated(f) {
			r.block(dest.where(s.root)+" is generated code", "target another file of the package, or change the generator")
		}
	}
	if len(r.Blockers) == 0 {
		var orig []byte
		if !dest.newFile {
			orig, _ = os.ReadFile(dest.file)
		}
		ed := splice(orig, dest.pkg.Name, src.imports, src.body, -1, -1)
		for _, e := range s.checkSource(ctx, dest.pkg, dest.file, ed) {
			r.block("does not compile: "+e, compileFix)
		}
	}

	summary := fmt.Sprintf("adds %s to %s in package %s", strings.Join(names, ", "), dest.where(s.root), dest.pkg.PkgPath)
	if len(src.imports) > 0 {
		summary += "; imports " + strings.Join(src.imports, ", ")
	}
	r.Summary = summary
	r.settle(req.Force)
	return r, nil
}

const compileFix = "fix the source; a package it uses goes in an import block at the top of the source"
