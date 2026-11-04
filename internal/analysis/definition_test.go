package analysis

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

	// Standard library symbol: export data gives the signature, the
	// on-demand source load gives the doc.
	def, err = ws.DefinitionOf(ctx, filepath.Join(dir, "server.go"), "fmt.Sprintf")
	if err != nil {
		t.Fatal(err)
	}
	if def.Package != "fmt" || !filepath.IsAbs(def.Position) || !strings.HasSuffix(strings.SplitN(def.Position, ":", 2)[0], "print.go") {
		t.Errorf("def = %+v", def)
	}
	if !strings.HasPrefix(def.Doc, "Sprintf formats according to a format specifier") {
		t.Errorf("doc = %q", def.Doc)
	}

	// Method of a standard library type.
	def, err = ws.DefinitionOf(ctx, filepath.Join(dir, "server.go"), "fmt.State.Write")
	if err != nil {
		t.Fatal(err)
	}
	if def.Kind != "method" || !strings.HasPrefix(def.Doc, "Write is the function to call") {
		t.Errorf("def = %+v", def)
	}
}
