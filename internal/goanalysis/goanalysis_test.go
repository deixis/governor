package goanalysis

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// copyFixture copies testdata/<name> into a temp dir, stripping the .txt
// suffix that keeps fixtures out of the real build.
func copyFixture(t *testing.T, name string) string {
	t.Helper()
	src := filepath.Join("testdata", name)
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, strings.TrimSuffix(rel, ".txt"))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copying fixture %s: %v", name, err)
	}
	// Resolve symlinks (macOS /var -> /private/var) so positions compare.
	if resolved, err := filepath.EvalSymlinks(dst); err == nil {
		dst = resolved
	}
	return dst
}

func TestDiagnostics_Clean(t *testing.T) {
	ws := NewWorkspace(copyFixture(t, "sample"))
	diags, err := ws.Diagnostics(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(diags) != 0 {
		t.Fatalf("expected no diagnostics, got %+v", diags)
	}
}

func TestDiagnostics_TypeErrors(t *testing.T) {
	dir := copyFixture(t, "broken")
	ws := NewWorkspace(dir)
	ctx := context.Background()

	diags, err := ws.Diagnostics(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	text := FormatDiagnostics(diags)
	for _, want := range []string{"main.go:4:14", "undefined: undefinedFn", "cannot use"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}

	// File filter: other.go has no errors.
	diags, err = ws.Diagnostics(ctx, []string{filepath.Join(dir, "other.go")})
	if err != nil {
		t.Fatal(err)
	}
	if len(diags) != 0 {
		t.Errorf("expected no diagnostics for other.go, got %+v", diags)
	}
}

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
		"not found in workspace",
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

func TestSearch(t *testing.T) {
	ws := NewWorkspace(copyFixture(t, "sample"))
	ctx := context.Background()

	syms, err := ws.Search(ctx, "server")
	if err != nil {
		t.Fatal(err)
	}
	if len(syms) == 0 || syms[0].Name != "Server" || syms[0].Kind != "type" {
		t.Fatalf("expected Server type first, got %+v", syms)
	}
	text := FormatSymbols(syms)
	for _, want := range []string{"func      example.com/sample.NewServer", "server.go:11:6"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}

	// Method and field hits, scattered match.
	syms, err = ws.Search(ctx, "srvstrt")
	if err != nil {
		t.Fatal(err)
	}
	if len(syms) == 0 || syms[0].Name != "Server.Start" || syms[0].Kind != "method" {
		t.Fatalf("expected Server.Start, got %+v", syms)
	}

	// Test variants must not duplicate hits.
	syms, _ = ws.Search(ctx, "NewServer")
	n := 0
	for _, s := range syms {
		if s.Name == "NewServer" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("NewServer appears %d times, want 1", n)
	}

	if _, err := ws.Search(ctx, "  "); err == nil {
		t.Error("expected error for empty query")
	}
}

func TestFileContext(t *testing.T) {
	dir := copyFixture(t, "sample")
	ws := NewWorkspace(dir)
	fc, err := ws.FileContextFor(context.Background(), filepath.Join(dir, "client.go"))
	if err != nil {
		t.Fatal(err)
	}
	if fc.Package != "example.com/sample" {
		t.Errorf("package = %q", fc.Package)
	}
	text := FormatFileContext(fc)
	for _, want := range []string{
		"Server  server.go:11:6",
		"Server.reset  server.go:30:18",
		"DefaultAddr  server.go:40:2",
		"version  server.go:44:5",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	// Field access on a same-package type.
	if !strings.Contains(text, ".Addr  server.go:13:2") {
		t.Errorf("missing field use in:\n%s", text)
	}

	fc, err = ws.FileContextFor(context.Background(), filepath.Join(dir, "server.go"))
	if err != nil {
		t.Fatal(err)
	}
	if got := fc.External["example.com/sample/sub"]; strings.Join(got, ",") != "NewMemStore,Store,Store.Put" {
		t.Errorf("sub uses = %v", got)
	}
	if got := fc.External["fmt"]; strings.Join(got, ",") != "Sprintf" {
		t.Errorf("fmt uses = %v", got)
	}
}

func TestReferences(t *testing.T) {
	dir := copyFixture(t, "sample")
	ws := NewWorkspace(dir)
	ctx := context.Background()
	serverGo := filepath.Join(dir, "server.go")

	cases := []struct {
		symbol string
		want   []string
	}{
		{"Server", []string{"def server.go:11:6", "    server.go:20:30", "    server.go:21:10", "    server.go:25:10", "    server.go:30:10", "    client.go:5:7", "    client.go:9:16"}},
		{"Server.Start", []string{"def server.go:25:18", "    server_test.go:7:14"}},
		{"Server.Addr", []string{"def server.go:13:2", "    server.go:21:17", "    server.go:27:49", "    client.go:16:15"}},
		{"sub.Store", []string{"def sub/store.go:5:6", "    server.go:15:12"}},
		{"DefaultAddr", []string{"def server.go:40:2", "    client.go:20:6", "    server_test.go:6:17"}},
	}
	for _, c := range cases {
		refs, err := ws.References(ctx, serverGo, c.symbol)
		if err != nil {
			t.Errorf("%s: %v", c.symbol, err)
			continue
		}
		text := FormatReferences(c.symbol, refs)
		for _, want := range c.want {
			if !strings.Contains(text, want) {
				t.Errorf("%s: missing %q in:\n%s", c.symbol, want, text)
			}
		}
	}

	// Symbol declared only in a test file resolves through the test variant.
	refs, err := ws.References(ctx, filepath.Join(dir, "server_test.go"), "TestServer")
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || !refs[0].Definition {
		t.Errorf("TestServer refs = %+v", refs)
	}

	if _, err := ws.References(ctx, serverGo, "Nope"); err == nil {
		t.Error("expected error for unknown symbol")
	}
	if _, err := ws.References(ctx, serverGo, "Server.Nope"); err == nil {
		t.Error("expected error for unknown member")
	}
}

func TestCacheInvalidation(t *testing.T) {
	dir := copyFixture(t, "sample")
	ws := NewWorkspace(dir)
	ctx := context.Background()

	if _, err := ws.Diagnostics(ctx, nil); err != nil {
		t.Fatal(err)
	}
	first := ws.snap

	if _, err := ws.Search(ctx, "x"); err != nil {
		t.Fatal(err)
	}
	if ws.snap != first {
		t.Fatal("snapshot reloaded without changes")
	}

	// Introduce an error; mtime granularity may be coarse, so bump it.
	path := filepath.Join(dir, "client.go")
	data, _ := os.ReadFile(path)
	data = append(data, []byte("\nfunc broken() { undefined() }\n")...)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(path, future, future)

	diags, err := ws.Diagnostics(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ws.snap == first {
		t.Fatal("snapshot not reloaded after change")
	}
	if len(diags) != 1 || !strings.Contains(diags[0].Message, "undefined: undefined") {
		t.Errorf("diags = %+v", diags)
	}
}

func TestFuzzyScore(t *testing.T) {
	cases := []struct {
		q, name string
		ok      bool
	}{
		{"server", "Server", true},
		{"srv", "Server", true},
		{"start", "Server.Start", true},
		{"xyz", "Server", false},
		{"serverx", "Server", false},
	}
	for _, c := range cases {
		if _, ok := fuzzyScore(c.q, c.name); ok != c.ok {
			t.Errorf("fuzzyScore(%q, %q) ok = %v, want %v", c.q, c.name, ok, c.ok)
		}
	}
	exact, _ := fuzzyScore("server", "Server")
	prefix, _ := fuzzyScore("serv", "Server")
	sub, _ := fuzzyScore("erve", "Server")
	scattered, _ := fuzzyScore("srv", "Server")
	if !(exact > prefix && prefix > sub && sub > scattered) {
		t.Errorf("ranking: exact=%d prefix=%d sub=%d scattered=%d", exact, prefix, sub, scattered)
	}
}
