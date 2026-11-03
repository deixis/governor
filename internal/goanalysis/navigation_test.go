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

func TestImplementations(t *testing.T) {
	dir := copyFixture(t, "sample")
	ws := NewWorkspace(dir)
	ctx := context.Background()
	storeGo := filepath.Join(dir, "sub", "store.go")

	// Interface -> implementers. MemStore.Put has a pointer receiver.
	impls, err := ws.Implementations(ctx, storeGo, "Store")
	if err != nil {
		t.Fatal(err)
	}
	if len(impls) != 1 || impls[0].Name != "MemStore" || !impls[0].Pointer {
		t.Fatalf("Store impls = %+v", impls)
	}

	// Concrete -> interfaces.
	impls, err = ws.Implementations(ctx, storeGo, "MemStore")
	if err != nil {
		t.Fatal(err)
	}
	if len(impls) != 1 || impls[0].Name != "Store" || impls[0].Kind != "interface" {
		t.Fatalf("MemStore impls = %+v", impls)
	}

	// Method level, both directions.
	impls, err = ws.Implementations(ctx, storeGo, "Store.Put")
	if err != nil {
		t.Fatal(err)
	}
	if len(impls) != 1 || impls[0].Name != "MemStore.Put" || impls[0].Position != "sub/store.go:18:20" {
		t.Fatalf("Store.Put impls = %+v", impls)
	}
	impls, err = ws.Implementations(ctx, storeGo, "MemStore.Put")
	if err != nil {
		t.Fatal(err)
	}
	if len(impls) != 1 || impls[0].Name != "Store.Put" {
		t.Fatalf("MemStore.Put impls = %+v", impls)
	}

	// Handler has an unexported method; nothing implements it.
	impls, err = ws.Implementations(ctx, filepath.Join(dir, "server.go"), "Handler")
	if err != nil {
		t.Fatal(err)
	}
	if len(impls) != 0 {
		t.Errorf("Handler impls = %+v", impls)
	}

	if _, err := ws.Implementations(ctx, storeGo, "NewMemStore"); err == nil {
		t.Error("expected error for a plain function")
	}
}

func TestCallers(t *testing.T) {
	dir := copyFixture(t, "sample")
	ws := NewWorkspace(dir)
	ctx := context.Background()
	serverGo := filepath.Join(dir, "server.go")

	calls, err := ws.Callers(ctx, serverGo, "Server.Start")
	if err != nil {
		t.Fatal(err)
	}
	text := FormatCallers("Server.Start", calls)
	if len(calls) != 1 || !strings.Contains(text, "server_test.go:7:12  in TestServer") {
		t.Errorf("callers:\n%s", text)
	}

	if _, err := ws.Callers(ctx, serverGo, "sub.Store"); err == nil {
		t.Error("expected error: Store is a type")
	}

	// Interface method call site inside Start.
	calls, err = ws.Callers(ctx, filepath.Join(dir, "sub", "store.go"), "Store.Put")
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].Caller != "Server.Start" || calls[0].Package != "example.com/sample" {
		t.Errorf("Store.Put callers = %+v", calls)
	}

	// Unexported method called from another file.
	calls, err = ws.Callers(ctx, serverGo, "Server.reset")
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].Caller != "Dial" || calls[0].Position != "client.go:10:2" {
		t.Errorf("reset callers = %+v", calls)
	}
}

func TestCallees(t *testing.T) {
	dir := copyFixture(t, "sample")
	ws := NewWorkspace(dir)
	ctx := context.Background()

	calls, err := ws.Callees(ctx, filepath.Join(dir, "server.go"), "Server.Start")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range calls {
		names = append(names, c.Callee)
	}
	if got := strings.Join(names, ","); got != "sub.Store.Put,fmt.Sprintf" {
		t.Errorf("callees = %q", got)
	}

	// Interface methods have no body.
	if _, err := ws.Callees(ctx, filepath.Join(dir, "sub", "store.go"), "Store.Put"); err == nil {
		t.Error("expected error for interface method")
	}
}

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
