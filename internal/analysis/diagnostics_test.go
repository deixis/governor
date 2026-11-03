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

// go list reports a subpackage's compile errors relative to the root, not
// the package dir; the expanded error must dedupe against the type
// checker's.
func TestDiagnostics_SubpackageCompileError(t *testing.T) {
	dir := writeModule(t, map[string]string{
		"go.mod":        "module example.com/d\n\ngo 1.25\n",
		"main.go":       "package main\n\nimport \"example.com/d/sub/deep\"\n\nfunc main() { deep.F() }\n",
		"sub/deep/a.go": "package deep\n\nfunc F() { var x int = \"s\"; _ = x }\n",
	})
	diags, err := NewWorkspace(dir).Diagnostics(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(diags) != 1 || !strings.HasPrefix(diags[0].Position, filepath.Join("sub", "deep", "a.go")+":3:") {
		t.Errorf("diags = %+v", diags)
	}
}

func TestLessPos(t *testing.T) {
	ordered := []string{"<no position>", "a.go:3:9", "a.go:3:21", "a.go:10:1", "b.go:1:1"}
	for i := range len(ordered) - 1 {
		if !lessPos(ordered[i], ordered[i+1]) || lessPos(ordered[i+1], ordered[i]) {
			t.Errorf("want %q before %q", ordered[i], ordered[i+1])
		}
	}
}
