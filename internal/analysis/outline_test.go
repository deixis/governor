package analysis

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOutline(t *testing.T) {
	dir := copyFixture(t, "sample")
	ws := NewWorkspace(dir)
	ctx := context.Background()

	entries, err := ws.Outline(ctx, filepath.Join(dir, "server.go"))
	if err != nil {
		t.Fatal(err)
	}
	text := FormatOutline(entries)
	for _, want := range []string{
		"server.go\n",
		"11-17     type Server struct (3 fields)",
		"20-22     func NewServer(addr string) *Server",
		"25-28     func (s *Server) Start() error",
		"30        func (s *Server) reset()",
		"33-36     type Handler interface (2 methods)",
		"40        const DefaultAddr = \":8080\"",
		"44        var version = \"1.0\"",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "s.count++") || strings.Contains(text, "import") {
		t.Errorf("body or import leaked:\n%s", text)
	}

	// Package form includes test files, each file once.
	entries, err = ws.Outline(ctx, "example.com/sample")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]int{}
	for _, e := range entries {
		if e.Summary == "func TestServer(t *testing.T)" {
			files[e.File]++
		}
	}
	if files["server_test.go"] != 1 {
		t.Errorf("TestServer entries = %v", files)
	}

	if _, err := ws.Outline(ctx, "example.com/nope"); err == nil {
		t.Error("expected error for unknown package")
	}
}

// A file or directory outside any loaded package is parsed on its own.
func TestOutline_Standalone(t *testing.T) {
	ws := NewWorkspace(copyFixture(t, "sample"))
	ctx := context.Background()

	loose := t.TempDir()
	src := "package loose\n\nimport \"fmt\"\n\n// Greeter greets.\ntype Greeter struct{ Name string }\n\nfunc (g Greeter) Hello() string { return fmt.Sprint(g.Name) }\n\nfunc helper() {}\n"
	if err := os.WriteFile(filepath.Join(loose, "a.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(loose, "b.go"), []byte("package loose\n\nconst Version = \"1\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := ws.Outline(ctx, filepath.Join(loose, "a.go"))
	if err != nil {
		t.Fatal(err)
	}
	text := FormatOutline(entries)
	for _, want := range []string{
		"Parsed without type information",
		"type Greeter struct (1 fields)",
		"func (g Greeter) Hello() string",
		"func helper()",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Version") {
		t.Errorf("sibling file leaked into single-file outline:\n%s", text)
	}

	entries, err = ws.Outline(ctx, loose)
	if err != nil {
		t.Fatal(err)
	}
	text = FormatOutline(entries)
	if !strings.Contains(text, "func helper()") || !strings.Contains(text, "const Version = \"1\"") {
		t.Errorf("directory outline:\n%s", text)
	}

	if _, err := ws.Outline(ctx, filepath.Join(loose, "missing.go")); err == nil {
		t.Error("expected error for missing file")
	}
}

func TestOutline_External(t *testing.T) {
	ws := NewWorkspace(copyFixture(t, "sample"))
	entries, err := ws.Outline(context.Background(), "strings")
	if err != nil {
		t.Fatal(err)
	}
	text := FormatOutline(entries)
	if !strings.Contains(text, "func (b *Builder) WriteString(s string) (int, error)") {
		t.Errorf("missing Builder.WriteString in:\n%s", text)
	}
	if strings.Contains(text, "func TestBuilder") {
		t.Errorf("test file leaked into external outline:\n%s", text)
	}
	for _, e := range entries {
		if !filepath.IsAbs(e.File) {
			t.Errorf("external file %q should be absolute", e.File)
			break
		}
	}
}
