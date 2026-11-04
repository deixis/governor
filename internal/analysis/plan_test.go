package analysis

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// planModule has one package with a movable type (Thing: no in-package
// users), a type with an in-package user (Widget), an unexported helper
// only Thing uses (greet), one shared with another type (base), and three
// other packages: bar imports foo, baz is unrelated, qux uses foo.Thing
// under an alias.
func planModule(t *testing.T) string {
	t.Helper()
	return writeModule(t, map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.25\n",
		"foo/foo.go": `package foo

import "fmt"

// Thing does things.
type Thing struct {
	Name  string
	count int
}

// Hello greets.
func (t *Thing) Hello() string { return greet(t.Name) }

// Count counts.
func (t *Thing) Count() int { return t.count + base }

func greet(n string) string { return fmt.Sprintf("hi %s", n) }

const base = 1

// Other is unrelated.
type Other struct{}

func (Other) N() int { return base }

// Exported is used by Thing.Extra.
func Exported() string { return "x" }

// Widget has an in-package user.
type Widget struct{}

// MakeWidget builds a Widget.
func MakeWidget() Widget { return Widget{} }
`,
		"foo/extra.go": `package foo

// Extra lives in another file.
func (t *Thing) Extra() string { return Exported() }
`,
		"bar/bar.go": `package bar

import "example.com/m/foo"

// Use depends on foo.
func Use() string { return foo.Exported() }
`,
		"baz/baz.go": `package baz

// Nothing is here.
func Nothing() {}
`,
		"qux/qux.go": `package qux

import f "example.com/m/foo"

// T is a Thing.
var T f.Thing

// Hi greets.
func Hi() string { return T.Hello() }
`,
	})
}

func plan(t *testing.T, dir string, req Request) (*Report, string) {
	t.Helper()
	r, err := NewWorkspace(dir).Plan(context.Background(), req)
	if err != nil {
		t.Fatalf("plan %+v: %v", req, err)
	}
	return r, FormatReport(r)
}

func wantAll(t *testing.T, text string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(text, w) {
			t.Errorf("missing %q in:\n%s", w, text)
		}
	}
}

func wantVerdict(t *testing.T, r *Report, text string, v Verdict) {
	t.Helper()
	if r.Verdict != v {
		t.Errorf("verdict = %s, want %s\n%s", r.Verdict, v, text)
	}
}

func TestPlanMove(t *testing.T) {
	dir := planModule(t)
	fooGo := filepath.Join(dir, "foo", "foo.go")
	ws := NewWorkspace(dir)

	// base is shared with Other.N: a decision.
	r, text := plan(t, dir, Request{Op: "move", File: fooGo, Symbol: "Thing", Target: "example.com/m/baz"})
	wantVerdict(t, r, text, VerdictDecisions)
	wantAll(t, text,
		"NEEDS DECISION: move Thing -> example.com/m/baz",
		"moves 4 declarations from foo/foo.go, foo/extra.go to baz/thing.go",
		"pulls greet along",
		"keeps using Exported from foo",
		"rewrites 2 references in example.com/m/qux (2, aliased f)",
		"const base (foo/foo.go:19:7) is unexported, used by Thing.Count and by foo/foo.go:24:31 staying in foo",
		`decisions.export ["base"]`,
		`decisions.pull ["base"]`,
	)
	if strings.Contains(text, "cycle") || strings.Contains(text, "After apply") {
		t.Errorf("unexpected content:\n%s", text)
	}

	// Export decision: OK, two lines.
	r, text = plan(t, dir, Request{Op: "move", File: fooGo, Symbol: "Thing", Target: "example.com/m/baz", Decisions: Decisions{Export: []string{"base"}}})
	wantVerdict(t, r, text, VerdictOK)
	if n := strings.Count(text, "\n"); n != 2 {
		t.Errorf("OK report should be two lines, got %d:\n%s", n, text)
	}

	// Pull decision: OK, Other.N to fix afterwards.
	r, text = plan(t, dir, Request{Op: "move", File: fooGo, Symbol: "Thing", Target: "example.com/m/baz", Decisions: Decisions{Pull: []string{"base"}}})
	wantVerdict(t, r, text, VerdictOK)
	wantAll(t, text, "pulls greet, base along", "After apply, you must:", "Fix the users of helpers pulled along (1)", "foo/foo.go:24:31  uses base, now in baz")

	// Force: same as pull, plus the forced marker.
	r, text = plan(t, dir, Request{Op: "move", File: fooGo, Symbol: "Thing", Target: "example.com/m/baz", Force: true})
	wantVerdict(t, r, text, VerdictOK)
	wantAll(t, text, "OK: move", "foo/foo.go:24:31  uses base, now in baz")
	if r.Forced {
		t.Error("force with nothing blocked should not be marked forced")
	}

	// Widget has an in-package user and bar imports foo: cycle, with a fix.
	r, text = plan(t, dir, Request{Op: "move", File: fooGo, Symbol: "Widget", Target: "example.com/m/bar"})
	wantVerdict(t, r, text, VerdictBlocked)
	wantAll(t, text,
		"BLOCKED: move Widget -> example.com/m/bar",
		"import cycle: foo uses the moved code at foo/foo.go:33:19, foo/foo.go:33:35 so foo would import bar, and bar already imports foo",
		"Fix: move those users too",
		"rewrites 2 uses in foo to bar.X",
	)

	// Forced past the cycle.
	r, text = plan(t, dir, Request{Op: "move", File: fooGo, Symbol: "Widget", Target: "example.com/m/bar", Force: true})
	wantVerdict(t, r, text, VerdictOK)
	wantAll(t, text, "OK (forced): move Widget", "Forced past (1), fix afterwards", "import cycle")
	if !r.Forced {
		t.Error("expected forced marker")
	}

	if _, err := ws.Plan(context.Background(), Request{Op: "move", File: fooGo, Symbol: "Thing", Target: "example.com/m/foo"}); err == nil {
		t.Error("expected error moving into its own package")
	}
	if _, err := ws.Plan(context.Background(), Request{Op: "move", File: fooGo, Symbol: "Thing.Name", Target: "example.com/m/baz"}); err == nil {
		t.Error("expected error moving a field")
	}
	if _, err := ws.Plan(context.Background(), Request{Op: "move", File: fooGo, Symbol: "Thing", Target: "example.com/m/nope"}); err == nil {
		t.Error("expected error for unknown destination")
	}
}

func TestPlanRemove(t *testing.T) {
	dir := planModule(t)
	fooGo := filepath.Join(dir, "foo", "foo.go")

	r, text := plan(t, dir, Request{Op: "remove", File: fooGo, Symbol: "Thing"})
	wantVerdict(t, r, text, VerdictOK)
	wantAll(t, text,
		"OK: remove Thing",
		"removes 4 declarations from foo/foo.go, foo/extra.go; breaks 2 uses in 1 package",
		"Fix or remove the uses that break (2)",
		"qux/qux.go:6:9  in example.com/m/qux",
		"Remove too, nothing else uses them (1)",
		"func greet  foo/foo.go:17:6",
	)
	if strings.Contains(text, "const base") {
		t.Errorf("base is still used by Other.N:\n%s", text)
	}

	r, text = plan(t, dir, Request{Op: "remove", File: fooGo, Symbol: "Other"})
	wantVerdict(t, r, text, VerdictOK)
	if strings.Contains(text, "After apply") {
		t.Errorf("Other has no users:\n%s", text)
	}
}

func TestPlanReplace(t *testing.T) {
	dir := planModule(t)
	fooGo := filepath.Join(dir, "foo", "foo.go")

	r, text := plan(t, dir, Request{
		Op: "replace", File: fooGo, Symbol: "Thing.Hello",
		Source: "// Hello greets, loudly on request.\nfunc (t *Thing) Hello(loud bool) string { return greet(t.Name) }\n",
	})
	wantVerdict(t, r, text, VerdictOK)
	wantAll(t, text,
		"replaces method Thing.Hello at foo/foo.go:11:1-12; signature changes from func (t *Thing) Hello() string to func (t *Thing) Hello(loud bool) string",
		"Update the callers (1)",
		"qux/qux.go:9:27  in Hi",
	)

	r, text = plan(t, dir, Request{
		Op: "replace", File: fooGo, Symbol: "Thing.Hello",
		Source: "func (t *Thing) Hello() string { return \"hi\" }",
	})
	wantVerdict(t, r, text, VerdictOK)
	if strings.Contains(text, "After apply") {
		t.Errorf("same signature needs no follow-up:\n%s", text)
	}

	r, text = plan(t, dir, Request{
		Op: "replace", File: fooGo, Symbol: "Thing.Hello",
		Source: "func (t *Thing) Greet() string { return \"hi\" }",
	})
	wantVerdict(t, r, text, VerdictBlocked)
	wantAll(t, text, "source declares method Thing.Greet, the symbol is method Thing.Hello", "Fix: keep the name and kind")

	r, text = plan(t, dir, Request{
		Op: "replace", File: fooGo, Symbol: "Thing.Hello",
		Source: "func (t *Thing) Greet() string { return \"hi\" }", Force: true,
	})
	wantVerdict(t, r, text, VerdictOK)
	wantAll(t, text, "OK (forced)", "Forced past (1)")

	r, text = plan(t, dir, Request{
		Op: "replace", File: fooGo, Symbol: "Thing",
		Source: "// Thing does things.\ntype Thing struct {\n\tName string\n\tLabel string\n}\n",
	})
	wantVerdict(t, r, text, VerdictOK)
	wantAll(t, text, "shape changes: ", "removed count", "added Label", "Check the packages using it", "example.com/m/qux (2 uses)")

	if _, err := NewWorkspace(dir).Plan(context.Background(), Request{Op: "replace", File: fooGo, Symbol: "Thing", Source: "func A() {}\nfunc B() {}"}); err == nil {
		t.Error("expected error for two declarations")
	}
}

func TestPlanAdd(t *testing.T) {
	dir := planModule(t)
	fooGo := filepath.Join(dir, "foo", "foo.go")

	r, text := plan(t, dir, Request{
		Op: "add", Target: "example.com/m/foo",
		Source: "import \"strings\"\n\n// NewThing builds a Thing.\nfunc NewThing(name string) *Thing { return &Thing{Name: strings.TrimSpace(name)} }\n",
	})
	wantVerdict(t, r, text, VerdictOK)
	wantAll(t, text, "OK: add func NewThing", "adds func NewThing to foo/new_thing.go (new file) in package example.com/m/foo; imports strings")

	r, text = plan(t, dir, Request{Op: "add", Target: fooGo, Source: "func (t *Thing) Hello() string { return \"\" }"})
	wantVerdict(t, r, text, VerdictBlocked)
	wantAll(t, text, "method Thing.Hello already exists at foo/foo.go:12:17", "Fix: use op replace", "to foo/foo.go in package")

	r, text = plan(t, dir, Request{Op: "add", File: fooGo, Source: "func Exported() string { return \"\" }\nfunc (Nope) M() {}"})
	wantVerdict(t, r, text, VerdictBlocked)
	wantAll(t, text, "func Exported already exists at foo/foo.go:27:6", "type Nope is not declared in example.com/m/foo")

	r, text = plan(t, dir, Request{Op: "add", Target: filepath.Join(dir, "baz", "more.go"), Source: "const Answer = 42"})
	wantVerdict(t, r, text, VerdictOK)
	wantAll(t, text, "baz/more.go (new file) in package example.com/m/baz")

	if _, err := NewWorkspace(dir).Plan(context.Background(), Request{Op: "add", Target: "example.com/m/nope", Source: "const A = 1"}); err == nil {
		t.Error("expected error for unknown package")
	}
	if _, err := NewWorkspace(dir).Plan(context.Background(), Request{Op: "add", Target: "example.com/m/foo", Source: "func ("}); err == nil {
		t.Error("expected parse error")
	}
}

func TestPlan_UnknownOp(t *testing.T) {
	if _, err := NewWorkspace(planModule(t)).Plan(context.Background(), Request{Op: "rename"}); err == nil {
		t.Error("expected error")
	}
}
