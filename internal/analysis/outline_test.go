package analysis

import (
	"context"
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
