package goanalysis

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
