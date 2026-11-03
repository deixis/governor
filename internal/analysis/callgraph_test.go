package analysis

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

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
