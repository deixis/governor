package goanalysis

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFix(t *testing.T) {
	dir := copyFixture(t, "fixme")
	ws := NewWorkspace(dir)
	ctx := context.Background()
	mainGo := filepath.Join(dir, "main.go")

	// First pass: the package has a type error (fmt missing, undefinedCall),
	// so modernize is skipped. Imports are repaired: fmt added, os dropped.
	results, err := ws.Fix(ctx, []string{mainGo})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Err != nil {
		t.Fatalf("results = %+v", results)
	}
	text := FormatFixResults(results)
	for _, want := range []string{"import added: fmt", "import removed: os"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	// main.go itself is clean now; other.go still has the undefined call,
	// which must not be reported for main.go.
	if len(results[0].Diagnostics) != 0 {
		t.Errorf("unexpected diagnostics for main.go:\n%s", text)
	}

	// Fix other.go: its error remains, reported.
	otherGo := filepath.Join(dir, "other.go")
	results, err = ws.Fix(ctx, []string{otherGo})
	if err != nil {
		t.Fatal(err)
	}
	if len(results[0].Diagnostics) != 1 || !strings.Contains(results[0].Diagnostics[0].Message, "undefined: undefinedCall") {
		t.Errorf("other.go diagnostics = %+v", results[0].Diagnostics)
	}

	// Remove the error, then fix both: modernize applies.
	src, _ := os.ReadFile(otherGo)
	src = []byte(strings.Replace(string(src), "func broken() { undefinedCall() }\n", "", 1))
	if err := os.WriteFile(otherGo, src, 0o644); err != nil {
		t.Fatal(err)
	}
	results, err = ws.Fix(ctx, []string{mainGo, otherGo})
	if err != nil {
		t.Fatal(err)
	}
	text = FormatFixResults(results)
	for _, want := range []string{
		"rangeint: Replace for loop with range n (line 5)",
		"slicescontains: Replace loop by call to slices.Contains (line 17)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	for _, r := range results {
		if len(r.Diagnostics) != 0 {
			t.Errorf("%s: unexpected diagnostics %+v", r.File, r.Diagnostics)
		}
	}
	after, _ := os.ReadFile(mainGo)
	if !strings.Contains(string(after), "slices.Contains(items, want)") || !strings.Contains(string(after), "\"slices\"") {
		t.Errorf("slices.Contains not applied:\n%s", after)
	}
	after, _ = os.ReadFile(otherGo)
	if !strings.Contains(string(after), "for i := range n {") {
		t.Errorf("range-over-int not applied:\n%s", after)
	}

	// Idempotent.
	results, err = ws.Fix(ctx, []string{mainGo, otherGo})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if len(r.Changes) != 0 {
			t.Errorf("%s: second pass changed %v", r.File, r.Changes)
		}
	}
}

func TestFix_UnknownFile(t *testing.T) {
	dir := copyFixture(t, "fixme")
	ws := NewWorkspace(dir)
	results, err := ws.Fix(context.Background(), []string{filepath.Join(dir, "nope.go")})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Err == nil {
		t.Errorf("results = %+v", results)
	}
}
