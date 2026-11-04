package analysis

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestPackageAPI(t *testing.T) {
	ws := NewWorkspace(copyFixture(t, "sample"))
	out, err := ws.PackageAPI(context.Background(), []string{"example.com/sample", "example.com/nope"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"package sample",
		"type Server struct",
		"Addr string",
		"Store sub.Store",
		"func (s *Server) Start() error",
		"func NewServer(addr string) *Server",
		"type Handler interface",
		"Handle(req string) error",
		"DefaultAddr = \":8080\"",
		"// Server serves things.",
		"1 unexported field(s)",
		"// package example.com/nope: ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"reset()", "secret", "version", "internal()", "s.count++"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("unexpected %q in:\n%s", unwanted, out)
		}
	}
}

// A file, a _test.go file or a directory stands for the package in that
// directory.
func TestPackageAPI_Paths(t *testing.T) {
	dir := copyFixture(t, "sample")
	ws := NewWorkspace(dir)
	for _, target := range []string{
		filepath.Join(dir, "server.go"),
		filepath.Join(dir, "server_test.go"),
		dir,
		filepath.Join(dir, "sub"),
	} {
		out, err := ws.PackageAPI(context.Background(), []string{target})
		if err != nil {
			t.Fatal(err)
		}
		want := `// import "example.com/sample"`
		if filepath.Base(target) == "sub" {
			want = `// import "example.com/sample/sub"`
		}
		if !strings.Contains(out, want) {
			t.Errorf("%s: missing %q in:\n%s", target, want, out)
		}
	}
	out, err := ws.PackageAPI(context.Background(), []string{filepath.Join(dir, "nope")})
	if err != nil || !strings.Contains(out, "is not part of any loaded package") {
		t.Errorf("unknown path: %v\n%s", err, out)
	}
}

func TestPackageAPI_External(t *testing.T) {
	ws := NewWorkspace(copyFixture(t, "sample"))
	out, err := ws.PackageAPI(context.Background(), []string{"io", "golang.org/x/tools/go/packages"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"// import \"io\"\npackage io\n",
		"type Reader interface",
		"// Reader is the interface that wraps the basic Read method.",
		"// package golang.org/x/tools/go/packages: ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "func (r *LimitedReader) Read(p []byte) (n int, err error) {") {
		t.Errorf("body leaked:\n%s", out)
	}
}

// A go.mod dependency, resolved from this module's own go.mod.
func TestPackageAPI_Dependency(t *testing.T) {
	ws := NewWorkspace(filepath.Join("..", ".."))
	out, err := ws.PackageAPI(context.Background(), []string{"golang.org/x/tools/go/packages"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"package packages\n",
		"func Load(cfg *Config, patterns ...string) ([]*Package, error)",
		"// Load loads and returns the Go packages named by the given patterns.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
