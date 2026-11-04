// Package analysis provides in-process Go code intelligence built on
// golang.org/x/tools/go/packages and go/types. It replaces the former
// gopls subprocess proxy: no external binary, one cached type-checked
// snapshot of the workspace per fingerprint.
//
// When a file here needs golang.org/x/tools/go/analysis directly, import
// it as goanalysis to keep the two names apart.
package analysis

import (
	"context"
	"fmt"
	"go/token"
	"go/types"
	"hash/fnv"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"golang.org/x/tools/go/packages"
)

// loadMode is the package information every tool needs. NeedDeps is
// omitted on purpose: dependencies come from export data, which keeps a
// cold load close to `go vet` instead of type-checking the stdlib.
const loadMode = packages.NeedName |
	packages.NeedFiles |
	packages.NeedCompiledGoFiles |
	packages.NeedImports |
	packages.NeedTypes |
	packages.NeedSyntax |
	packages.NeedTypesInfo |
	packages.NeedTypesSizes |
	packages.NeedModule

// snapshot is one type-checked view of the workspace.
type snapshot struct {
	// root is the directory the snapshot was loaded from. Tools use it
	// rather than Workspace.Root, which SetRoot may change mid-call.
	root        string
	fingerprint uint64
	fset        *token.FileSet
	pkgs        []*packages.Package
	// byFile maps absolute file path to the packages containing it. With
	// Tests enabled a file can belong to several package variants.
	byFile map[string][]*packages.Package
	// dirs holds every loaded package directory, so a file the loader
	// left out (unparsable package clause) is told apart from one outside
	// the workspace.
	dirs map[string]bool

	// external caches packages outside the workspace (standard library,
	// module dependencies) loaded with syntax on demand, by import path.
	// A failed load is cached too so a bad path costs one `go list`.
	extMu    sync.Mutex
	external map[string]externalLoad
}

// externalLoad is one cached on-demand load.
type externalLoad struct {
	pkg *packages.Package
	err error
}

// Workspace loads and caches type information for a Go module tree.
// It is safe for concurrent use.
type Workspace struct {
	mu   sync.Mutex
	root string
	snap *snapshot
}

// NewWorkspace returns a Workspace rooted at dir. Nothing is loaded until
// the first tool call.
func NewWorkspace(dir string) *Workspace {
	return &Workspace{root: dir}
}

// SetRoot changes the workspace directory and drops the cached snapshot.
func (w *Workspace) SetRoot(dir string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if dir == w.root {
		return
	}
	w.root = dir
	w.snap = nil
}

// Root returns the current workspace directory.
func (w *Workspace) Root() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.root
}

// load returns the current snapshot, reloading when any Go source or
// module file changed since the last load.
func (w *Workspace) load(ctx context.Context) (*snapshot, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	fp, err := fingerprint(w.root)
	if err != nil {
		return nil, fmt.Errorf("fingerprinting %s: %w", w.root, err)
	}
	if w.snap != nil && w.snap.fingerprint == fp {
		return w.snap, nil
	}

	fset := token.NewFileSet()
	cfg := &packages.Config{
		Context: ctx,
		Mode:    loadMode,
		Dir:     w.root,
		Fset:    fset,
		Tests:   true,
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, fmt.Errorf("loading packages: %w", err)
	}

	snap := &snapshot{
		root:        w.root,
		fingerprint: fp,
		fset:        fset,
		pkgs:        pkgs,
		byFile:      make(map[string][]*packages.Package),
		dirs:        make(map[string]bool),
	}
	// Index files under both the reported path and its symlink-resolved
	// form (macOS reports /var/... for a /private/var/... cwd).
	for _, p := range pkgs {
		if p.Dir != "" {
			snap.dirs[p.Dir] = true
			snap.dirs[canonical(p.Dir)] = true
		}
		for _, f := range p.CompiledGoFiles {
			snap.byFile[f] = append(snap.byFile[f], p)
			if resolved := canonical(f); resolved != f {
				snap.byFile[resolved] = append(snap.byFile[resolved], p)
			}
		}
	}
	// Prefer the plain package variant first for every file so lookups
	// that take the first match get stable results.
	for _, list := range snap.byFile {
		sort.SliceStable(list, func(i, j int) bool {
			return variantRank(list[i]) < variantRank(list[j])
		})
	}
	w.snap = snap
	return snap, nil
}

// externalPackage loads importPath with syntax, resolved from the
// workspace module so it reaches the standard library and every go.mod
// dependency. Tests are excluded. Workspace packages are not served here;
// callers check the snapshot first.
func (s *snapshot) externalPackage(ctx context.Context, importPath string) (*packages.Package, error) {
	s.extMu.Lock()
	defer s.extMu.Unlock()
	if s.external == nil {
		s.external = make(map[string]externalLoad)
	}
	if cached, ok := s.external[importPath]; ok {
		return cached.pkg, cached.err
	}
	pkg, err := loadExternal(ctx, s.root, s.fset, importPath)
	s.external[importPath] = externalLoad{pkg: pkg, err: err}
	return pkg, err
}

func loadExternal(ctx context.Context, root string, fset *token.FileSet, importPath string) (*packages.Package, error) {
	if importPath == "" || strings.HasPrefix(importPath, ".") || strings.ContainsAny(importPath, " \t\n") {
		return nil, fmt.Errorf("package %q: not an import path", importPath)
	}
	cfg := &packages.Config{
		Context: ctx,
		Mode:    loadMode,
		Dir:     root,
		Fset:    fset,
	}
	pkgs, err := packages.Load(cfg, importPath)
	if err != nil {
		return nil, fmt.Errorf("loading package %s: %w", importPath, err)
	}
	if len(pkgs) != 1 {
		return nil, fmt.Errorf("package %s: pattern matched %d packages", importPath, len(pkgs))
	}
	p := pkgs[0]
	if len(p.Syntax) == 0 {
		if len(p.Errors) > 0 {
			return nil, fmt.Errorf("package %s: %s", importPath, p.Errors[0].Msg)
		}
		return nil, fmt.Errorf("package %s: no Go source", importPath)
	}
	return p, nil
}

// variantRank orders package variants: plain package, then in-package
// test variant, then external test package, then test main.
func variantRank(p *packages.Package) int {
	switch {
	case p.ID == p.PkgPath:
		return 0
	case strings.HasSuffix(p.PkgPath, "_test"):
		return 2
	case strings.HasSuffix(p.ID, ".test"):
		return 3
	default:
		return 1
	}
}

// fingerprint hashes the path, size and mtime of every Go source and
// module file under root. Directories named vendor, testdata, or starting
// with "." or "_" are skipped, matching the `./...` pattern.
func fingerprint(root string) (uint64, error) {
	// WalkDir does not descend into a root that is itself a symlink.
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	h := fnv.New64a()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if path != root && (name == "vendor" || name == "testdata" ||
				strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") && name != "go.mod" && name != "go.sum" && name != "go.work" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		// hash.Hash writes never fail.
		_, _ = fmt.Fprintf(h, "%s\x00%d\x00%d\n", path, info.Size(), info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		return 0, err
	}
	return h.Sum64(), nil
}

// packagesForFile returns the packages containing file, cleaned to an
// absolute path. The plain package variant comes first.
func (s *snapshot) packagesForFile(file string) ([]*packages.Package, error) {
	abs, err := filepath.Abs(file)
	if err != nil {
		return nil, err
	}
	if pkgs := s.byFile[abs]; len(pkgs) > 0 {
		return pkgs, nil
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		if pkgs := s.byFile[resolved]; len(pkgs) > 0 {
			return pkgs, nil
		}
	}
	return nil, fmt.Errorf("file %s is not part of any loaded package", file)
}

// contains reports whether the canonical path file belongs to a loaded
// package or sits in a loaded package's directory.
func (s *snapshot) contains(file string) bool {
	return len(s.byFile[file]) > 0 || s.dirs[filepath.Dir(file)]
}

// canonical returns path made absolute and, when it exists, with symlinks
// resolved. Paths are compared in this form: the loader reports files
// under the root as given (/var/... on macOS) while callers may pass the
// resolved form (/private/var/...), or the reverse.
func canonical(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

// position formats pos as file:line:col relative to root when possible.
func (s *snapshot) position(root string, pos token.Pos) string {
	p := s.fset.Position(pos)
	if !p.IsValid() {
		return "<unknown>"
	}
	return fmt.Sprintf("%s:%d:%d", relPath(root, p.Filename), p.Line, p.Column)
}

// objectKey identifies a types.Object across package variants and across
// the source/export-data boundary, where pointer identity does not hold.
func objectKey(fset *token.FileSet, obj types.Object) string {
	if obj == nil {
		return ""
	}
	pkgPath := ""
	if obj.Pkg() != nil {
		pkgPath = obj.Pkg().Path()
	}
	switch o := obj.(type) {
	case *types.Func:
		if recv := o.Type().(*types.Signature).Recv(); recv != nil {
			return pkgPath + "." + recvTypeName(recv.Type()) + "." + o.Name()
		}
	case *types.PkgName:
		return "pkgname:" + o.Imported().Path()
	}
	if obj.Parent() != nil && obj.Pkg() != nil && obj.Parent() == obj.Pkg().Scope() {
		return pkgPath + "." + obj.Name()
	}
	// Local or field: position disambiguates. Variants share a FileSet so
	// positions compare across them.
	p := fset.Position(obj.Pos())
	return fmt.Sprintf("%s:%s:%d:%d:%s", pkgPath, p.Filename, p.Line, p.Column, obj.Name())
}

// recvTypeName returns the bare type name of a method receiver.
func recvTypeName(t types.Type) string {
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	switch n := t.(type) {
	case *types.Named:
		return n.Obj().Name()
	case *types.Alias:
		return n.Obj().Name()
	}
	return types.TypeString(t, nil)
}
