package analysis

import (
	"context"
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
