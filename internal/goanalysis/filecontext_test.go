package goanalysis

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

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
