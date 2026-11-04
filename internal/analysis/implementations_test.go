package analysis

import (
	"context"
	"path/filepath"
	"testing"
)

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

// A method promoted from an embedded interface relates the queried type,
// not the embedded one.
func TestImplementations_PromotedMethod(t *testing.T) {
	dir := writeModule(t, map[string]string{
		"go.mod": "module example.com/e\n\ngo 1.25\n",
		"e.go": `package e

type Closer interface{ Close() error }

type ReadCloser interface {
	Closer
	Read() string
}

type Conn struct{}

func (Conn) Close() error { return nil }

type File struct{}

func (File) Read() string  { return "" }
func (File) Close() error { return nil }
`,
	})
	impls, err := NewWorkspace(dir).Implementations(context.Background(), filepath.Join(dir, "e.go"), "ReadCloser.Close")
	if err != nil {
		t.Fatal(err)
	}
	if len(impls) != 1 || impls[0].Name != "File.Close" {
		t.Errorf("ReadCloser.Close impls = %+v", impls)
	}
}

// A standard library interface whose methods mention types of its own
// package: the implementers inside that package are found.
func TestImplementations_External(t *testing.T) {
	dir := writeModule(t, map[string]string{
		"go.mod": "module example.com/e\n\ngo 1.25\n",
		"e.go": `package e

import "context"

var _ context.Context
`,
	})
	impls, err := NewWorkspace(dir).Implementations(context.Background(), filepath.Join(dir, "e.go"), "context.Context")
	if err != nil {
		t.Fatal(err)
	}
	var cancelCtx bool
	for _, im := range impls {
		if im.Package == "context" && im.Name == "cancelCtx" && im.Kind == "type" && im.Pointer {
			cancelCtx = true
		}
	}
	if !cancelCtx {
		t.Errorf("*cancelCtx not among context.Context implementers:\n%s", FormatImplementations("context.Context", impls))
	}
}
