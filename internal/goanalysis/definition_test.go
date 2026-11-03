package goanalysis

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefinitionOf(t *testing.T) {
	dir := copyFixture(t, "sample")
	ws := NewWorkspace(dir)
	ctx := context.Background()
	clientGo := filepath.Join(dir, "client.go")

	def, err := ws.DefinitionOf(ctx, clientGo, "Server.Start")
	if err != nil {
		t.Fatal(err)
	}
	if def.Position != "server.go:25:18" || def.Kind != "method" {
		t.Errorf("def = %+v", def)
	}
	if def.Signature != "func (s *Server) Start() error" {
		t.Errorf("signature = %q", def.Signature)
	}
	if def.Doc != "Start starts the server." {
		t.Errorf("doc = %q", def.Doc)
	}

	// Grouped const: spec doc wins.
	def, err = ws.DefinitionOf(ctx, clientGo, "DefaultAddr")
	if err != nil {
		t.Fatal(err)
	}
	if def.Doc != "DefaultAddr is the default listen address." || !strings.Contains(def.Signature, "DefaultAddr") {
		t.Errorf("def = %+v", def)
	}

	// Field doc.
	def, err = ws.DefinitionOf(ctx, clientGo, "Server.Addr")
	if err != nil {
		t.Fatal(err)
	}
	if def.Doc != "Addr is the listen address." || def.Kind != "field" {
		t.Errorf("def = %+v", def)
	}

	// Cross-package via import name, in the file that imports it.
	def, err = ws.DefinitionOf(ctx, filepath.Join(dir, "server.go"), "sub.NewMemStore")
	if err != nil {
		t.Fatal(err)
	}
	if def.Position != "sub/store.go:15:6" || def.Package != "example.com/sample/sub" {
		t.Errorf("def = %+v", def)
	}

	// pkg.T.Method form.
	def, err = ws.DefinitionOf(ctx, filepath.Join(dir, "server.go"), "sub.MemStore.Put")
	if err != nil {
		t.Fatal(err)
	}
	if def.Position != "sub/store.go:18:20" || def.Signature != "func (m *MemStore) Put(s string) error" {
		t.Errorf("def = %+v", def)
	}

	if _, err := ws.DefinitionOf(ctx, filepath.Join(dir, "server.go"), "sub.Nope"); err == nil {
		t.Error("expected error for unknown symbol in imported package")
	}
}
