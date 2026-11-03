package analysis

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestReferences(t *testing.T) {
	dir := copyFixture(t, "sample")
	ws := NewWorkspace(dir)
	ctx := context.Background()
	serverGo := filepath.Join(dir, "server.go")

	cases := []struct {
		symbol string
		want   []string
	}{
		{"Server", []string{"def server.go:11:6", "    server.go:20:30", "    server.go:21:10", "    server.go:25:10", "    server.go:30:10", "    client.go:5:7", "    client.go:9:16"}},
		{"Server.Start", []string{"def server.go:25:18", "    server_test.go:7:14"}},
		{"Server.Addr", []string{"def server.go:13:2", "    server.go:21:17", "    server.go:27:49", "    client.go:16:15"}},
		{"sub.Store", []string{"def sub/store.go:5:6", "    server.go:15:12"}},
		{"DefaultAddr", []string{"def server.go:40:2", "    client.go:20:6", "    server_test.go:6:17"}},
	}
	for _, c := range cases {
		refs, err := ws.References(ctx, serverGo, c.symbol)
		if err != nil {
			t.Errorf("%s: %v", c.symbol, err)
			continue
		}
		text := FormatReferences(c.symbol, refs)
		for _, want := range c.want {
			if !strings.Contains(text, want) {
				t.Errorf("%s: missing %q in:\n%s", c.symbol, want, text)
			}
		}
	}

	// Symbol declared only in a test file resolves through the test variant.
	refs, err := ws.References(ctx, filepath.Join(dir, "server_test.go"), "TestServer")
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || !refs[0].Definition {
		t.Errorf("TestServer refs = %+v", refs)
	}

	if _, err := ws.References(ctx, serverGo, "Nope"); err == nil {
		t.Error("expected error for unknown symbol")
	}
	if _, err := ws.References(ctx, serverGo, "Server.Nope"); err == nil {
		t.Error("expected error for unknown member")
	}
}
