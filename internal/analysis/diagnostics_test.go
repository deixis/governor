package analysis

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiagnostics_Clean(t *testing.T) {
	ws := NewWorkspace(copyFixture(t, "sample"))
	diags, err := ws.Diagnostics(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(diags) != 0 {
		t.Fatalf("expected no diagnostics, got %+v", diags)
	}
}

func TestDiagnostics_TypeErrors(t *testing.T) {
	dir := copyFixture(t, "broken")
	ws := NewWorkspace(dir)
	ctx := context.Background()

	diags, err := ws.Diagnostics(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	text := FormatDiagnostics(diags)
	for _, want := range []string{"main.go:4:14", "undefined: undefinedFn", "cannot use"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}

	// File filter: other.go has no errors.
	diags, err = ws.Diagnostics(ctx, []string{filepath.Join(dir, "other.go")})
	if err != nil {
		t.Fatal(err)
	}
	if len(diags) != 0 {
		t.Errorf("expected no diagnostics for other.go, got %+v", diags)
	}
}
