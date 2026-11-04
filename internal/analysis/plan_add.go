package analysis

import (
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
// the package and the file, which may not exist yet.
type fileTarget struct {
	pkg     *packages.Package
	file    string // absolute path
	newFile bool
}

// resolveTarget accepts a package import path, in which case the file is
// newName inside that package's directory, or an absolute file path in a
// loaded package directory. A file that exists must belong to a loaded
// package.
func (s *snapshot) resolveTarget(target, newName string) (fileTarget, error) {
	if target == "" {
		return fileTarget{}, fmt.Errorf("target (package import path or file path) is required")
	}
	var pkg *packages.Package
	abs := target
	if !filepath.IsAbs(target) {
		pkg = s.packageByPath(target)
		if pkg == nil {
			return fileTarget{}, fmt.Errorf("package %s not found in workspace", target)
		}
		abs = filepath.Join(pkg.Dir, newName)
	}
	abs = canonical(abs)
	if pkgs := s.byFile[abs]; len(pkgs) > 0 {
		return fileTarget{pkg: pkgs[0], file: abs}, nil
	}
	if _, err := os.Stat(abs); err == nil {
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
			return fileTarget{}, fmt.Errorf("directory %s holds no loaded package", dir)
		}
	}
	return fileTarget{pkg: pkg, file: abs, newFile: true}, nil
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

func snakeCase(name string) string {
	var b strings.Builder
	for i, r := range name {
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteByte('_')
			}
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (s *snapshot) planAdd(req Request) (*Report, error) {
	if strings.TrimSpace(req.Source) == "" {
		return nil, fmt.Errorf("source is required")
	}
	decls, imports, err := parseSource(req.Source)
	if err != nil {
		return nil, err
	}
	target := req.Target
	if target == "" {
		target = req.File
	}
	dest, err := s.resolveTarget(target, fileNameFor(decls))
	if err != nil {
		return nil, err
	}

	var names []string
	for _, d := range decls {
		names = append(names, declNames(d)...)
	}
	r := &Report{Op: "add", Subject: strings.Join(names, ", ")}

	scope := dest.pkg.Types.Scope()
	index := s.decls(dest.pkg)
	for _, d := range decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv != nil {
			recv := receiverName(fn.Recv)
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
			if obj := scope.Lookup(bareName(labelled)); obj != nil {
				r.block(fmt.Sprintf("%s already exists at %s", labelled, s.position(s.root, obj.Pos())),
					"use op replace to change it, or pick another name")
			}
		}
	}
	where := relPath(s.root, dest.file)
	if dest.newFile {
		where += " (new file)"
	} else if f := fileSyntax(dest.pkg, dest.file); f != nil && isGenerated(f) {
		r.block(where+" is generated code", "target another file of the package, or change the generator")
	}

	summary := fmt.Sprintf("adds %s to %s in package %s", strings.Join(names, ", "), where, dest.pkg.PkgPath)
	if len(imports) > 0 {
		summary += "; imports " + strings.Join(imports, ", ")
	}
	r.Summary = summary
	r.settle(req.Force)
	return r, nil
}
