package analysis

import (
	"context"
	"strings"
	"testing"
)

func TestSearch(t *testing.T) {
	ws := NewWorkspace(copyFixture(t, "sample"))
	ctx := context.Background()

	syms, err := ws.Search(ctx, "server")
	if err != nil {
		t.Fatal(err)
	}
	if len(syms) == 0 || syms[0].Name != "Server" || syms[0].Kind != "type" {
		t.Fatalf("expected Server type first, got %+v", syms)
	}
	text := FormatSymbols(syms)
	for _, want := range []string{"func      example.com/sample.NewServer", "server.go:11:6"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}

	// Method and field hits, scattered match.
	syms, err = ws.Search(ctx, "srvstrt")
	if err != nil {
		t.Fatal(err)
	}
	if len(syms) == 0 || syms[0].Name != "Server.Start" || syms[0].Kind != "method" {
		t.Fatalf("expected Server.Start, got %+v", syms)
	}

	// Test variants must not duplicate hits.
	syms, _ = ws.Search(ctx, "NewServer")
	n := 0
	for _, s := range syms {
		if s.Name == "NewServer" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("NewServer appears %d times, want 1", n)
	}

	if _, err := ws.Search(ctx, "  "); err == nil {
		t.Error("expected error for empty query")
	}
}

func TestFuzzyScore(t *testing.T) {
	cases := []struct {
		q, name string
		ok      bool
	}{
		{"server", "Server", true},
		{"srv", "Server", true},
		{"start", "Server.Start", true},
		{"xyz", "Server", false},
		{"serverx", "Server", false},
	}
	for _, c := range cases {
		if _, ok := fuzzyScore(c.q, c.name); ok != c.ok {
			t.Errorf("fuzzyScore(%q, %q) ok = %v, want %v", c.q, c.name, ok, c.ok)
		}
	}
	exact, _ := fuzzyScore("server", "Server")
	prefix, _ := fuzzyScore("serv", "Server")
	sub, _ := fuzzyScore("erve", "Server")
	scattered, _ := fuzzyScore("srv", "Server")
	if exact <= prefix || prefix <= sub || sub <= scattered {
		t.Errorf("ranking: exact=%d prefix=%d sub=%d scattered=%d", exact, prefix, sub, scattered)
	}
}
