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

// Store is satisfied by *Thing.
type Store interface {
	Hello() string
}

// Pair shares one spec.
var first, second = 1, 2

var (
	alpha = 1
	beta  = alpha + 1
)

// Chain uses outer and inner; outer alone uses inner.
type Chain struct{}

// Run runs.
func (Chain) Run() int { return outer() + inner() }

func outer() int { return inner() }

func inner() int { return beta }

// Tagged has a field read elsewhere.
type Tagged struct{ Tag string }
`,
		"foo/extra.go": `package foo

// Extra lives in another file.
func (t *Thing) Extra() string { return Exported() }
`,
		"bar/bar.go": `package bar

import "example.com/m/foo"

// Use depends on foo.
func Use() string { return foo.Exported() }

// Tag reads a tag.
func Tag(t foo.Tagged) string { return t.Tag }
`,
		"sub/go.mod": "module example.com/m/sub\n\ngo 1.25\n",
		"sub/sub.go": `package sub

// S is in a nested module.
type S struct{}
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

	// Pull decision: OK, Other.N to fix afterwards. Thing.Extra keeps
	// using foo.Exported, so Other.N cannot import base back from baz.
	r, text = plan(t, dir, Request{Op: "move", File: fooGo, Symbol: "Thing", Target: "example.com/m/baz", Decisions: Decisions{Pull: []string{"base"}}})
	wantVerdict(t, r, text, VerdictOK)
	wantAll(t, text, "pulls greet, base along", "After apply, you must:", "Fix the users of helpers pulled along, without importing baz from foo (cycle) (1)", "foo/foo.go:24:31  uses base, now in baz")

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
	// An import path below the module with no package yet is a new
	// package; one outside the module is an error.
	r, text = plan(t, dir, Request{Op: "move", File: fooGo, Symbol: "Widget", Target: "example.com/m/nope"})
	wantVerdict(t, r, text, VerdictOK)
	wantAll(t, text, "nope/widget.go (new package)")
	if _, err := ws.Plan(context.Background(), Request{Op: "move", File: fooGo, Symbol: "Thing", Target: "other.org/x"}); err == nil {
		t.Error("expected error for a destination outside the module")
	}
	if _, err := ws.Plan(context.Background(), Request{Op: "move", File: fooGo, Symbol: "Thing.Hello", Target: "example.com/m/baz"}); err == nil {
		t.Error("expected error moving a method")
	}
	if _, err := ws.Plan(context.Background(), Request{Op: "move", File: fooGo, Symbol: "Widget", Target: filepath.Join(dir, "baz", "x_test.go")}); err == nil {
		t.Error("expected error moving into a test file")
	}
	if _, err := ws.Plan(context.Background(), Request{Op: "move", File: fooGo, Symbol: "Widget", Target: "example.com/m/baz", Decisions: Decisions{Pull: []string{"nope"}}}); err == nil {
		t.Error("expected error for a decision naming nothing")
	}
}

// TestPlanMoveClosure covers the closure of a move: in-package test
// files as users, a pulled type with methods, and a cycle through a
// third package.
func TestPlanMoveClosure(t *testing.T) {
	dir := writeModule(t, map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.25\n",
		"foo/foo.go": `package foo

// Thing does things.
type Thing struct{ Name string }

// Hello greets.
func (t *Thing) Hello() string { return greet(t.Name) }

func greet(n string) string { return "hi " + n }

type helper struct{ n int }

func (h helper) do() int { return h.n }

// Big uses helper.
type Big struct{}

// Run runs.
func (Big) Run() int { return helper{n: 1}.do() }
`,
		"foo/foo_test.go": `package foo

import "testing"

func TestGreet(t *testing.T) { _ = greet("x") }
`,
		"bar/bar.go": `package bar

import "example.com/m/qux"

// Use depends on qux.
func Use() string { return qux.Hi() }
`,
		"baz/baz.go": "package baz\n",
		"qux/qux.go": `package qux

import "example.com/m/foo"

// T is a Thing.
var T foo.Thing

// Hi greets.
func Hi() string { return T.Hello() }
`,
	})
	fooGo := filepath.Join(dir, "foo", "foo.go")

	// greet is used by an in-package test file: a decision, not a pull.
	r, text := plan(t, dir, Request{Op: "move", File: fooGo, Symbol: "Thing", Target: "example.com/m/baz"})
	wantVerdict(t, r, text, VerdictDecisions)
	wantAll(t, text, "func greet (foo/foo.go:9:6) is unexported, used by Thing.Hello and by foo/foo_test.go:5:36 staying in foo")

	// Nor is it an orphan on removal.
	r, text = plan(t, dir, Request{Op: "remove", File: fooGo, Symbol: "Thing"})
	wantVerdict(t, r, text, VerdictOK)
	if strings.Contains(text, "greet") {
		t.Errorf("greet is used by foo_test.go:\n%s", text)
	}

	// A pulled type brings its methods; its own methods are not other
	// users, and reaching into its fields is not a blocker.
	r, text = plan(t, dir, Request{Op: "move", File: fooGo, Symbol: "Big", Target: "example.com/m/baz"})
	wantVerdict(t, r, text, VerdictOK)
	wantAll(t, text, "moves 2 declarations from foo/foo.go to baz/big.go (new file); pulls helper along")
	if n := strings.Count(text, "\n"); n != 2 {
		t.Errorf("OK report should be two lines, got %d:\n%s", n, text)
	}

	// qux uses Thing and bar imports qux: Thing cannot move into bar.
	r, text = plan(t, dir, Request{Op: "move", File: fooGo, Symbol: "Thing", Target: "example.com/m/bar", Decisions: Decisions{Export: []string{"greet"}}})
	wantVerdict(t, r, text, VerdictBlocked)
	wantAll(t, text, "import cycle: example.com/m/qux uses the moved code at qux/qux.go:6:11, qux/qux.go:9:29 and bar imports it", "Fix: move those users too")
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

	// An interface is a type to the source: same kind.
	r, text = plan(t, dir, Request{Op: "replace", File: fooGo, Symbol: "Store", Source: "type Store interface {\n\tHello() string\n\tCount() int\n}"})
	wantVerdict(t, r, text, VerdictOK)
	wantAll(t, text, "replaces interface Store at", "shape changes: added Count")

	// A receiver change is a signature change; renaming a parameter or
	// reflowing the declaration is not.
	r, text = plan(t, dir, Request{Op: "replace", File: fooGo, Symbol: "Thing.Hello", Source: "func (t Thing) Hello() string { return \"hi\" }"})
	wantVerdict(t, r, text, VerdictOK)
	wantAll(t, text, "signature changes from func (t *Thing) Hello() string to func (t Thing) Hello() string", "Update the callers (1)")
	r, text = plan(t, dir, Request{Op: "replace", File: fooGo, Symbol: "Thing.Hello", Source: "func (th *Thing) Hello(\n) string {\n\treturn \"hi\"\n}"})
	wantVerdict(t, r, text, VerdictOK)
	if strings.Contains(text, "signature changes") {
		t.Errorf("parameter names and layout are not the signature:\n%s", text)
	}

	// One name of a multi-name spec cannot be replaced alone.
	r, text = plan(t, dir, Request{Op: "replace", File: fooGo, Symbol: "first", Source: "var first = 3"})
	wantVerdict(t, r, text, VerdictBlocked)
	wantAll(t, text, "first shares its spec with second", "Fix: split the spec")

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

	// A package target whose derived file name exists adds to that file.
	r, text = plan(t, dir, Request{Op: "add", Target: "example.com/m/foo", Source: "func Extra() {}"})
	wantVerdict(t, r, text, VerdictOK)
	wantAll(t, text, "adds func Extra to foo/extra.go in package example.com/m/foo")
	if strings.Contains(text, "new file") {
		t.Errorf("foo/extra.go exists:\n%s", text)
	}

	r, text = plan(t, dir, Request{Op: "add", Target: "example.com/m/nope", Source: "const A = 1"})
	wantVerdict(t, r, text, VerdictOK)
	wantAll(t, text, "nope/a.go (new package)")
	if _, err := NewWorkspace(dir).Plan(context.Background(), Request{Op: "add", Target: "other.org/x", Source: "const A = 1"}); err == nil {
		t.Error("expected error for a package outside the module")
	}
	// A nested module is not a new package of this one.
	for _, target := range []string{"example.com/m/sub", filepath.Join(dir, "sub", "x.go"), filepath.Join(dir, "sub", "sub.go"), filepath.Join(dir, "sub", "deeper", "x.go")} {
		_, err := NewWorkspace(dir).Plan(context.Background(), Request{Op: "add", Target: target, Source: "const A = 1"})
		if err == nil || !strings.Contains(err.Error(), "module at sub") {
			t.Errorf("target %s: err = %v, want nested module error", target, err)
		}
	}
	for name, want := range map[string]string{"SG": "sg", "HTTPClient": "http_client", "PushWatcherConfig": "push_watcher_config", "thing": "thing", "ID": "id", "UserID": "user_id"} {
		if got := snakeCase(name); got != want {
			t.Errorf("snakeCase(%s) = %s, want %s", name, got, want)
		}
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

// TestPlanCompiles covers the type-check of add and replace source.
func TestPlanCompiles(t *testing.T) {
	dir := planModule(t)
	fooGo := filepath.Join(dir, "foo", "foo.go")

	r, text := plan(t, dir, Request{Op: "add", File: fooGo, Source: "func there() { nope() }"})
	wantVerdict(t, r, text, VerdictBlocked)
	wantAll(t, text, "does not compile: line 1: undefined: nope")

	r, text = plan(t, dir, Request{Op: "add", File: fooGo, Source: "// Env reads.\nfunc Env() string { return os.Getenv(\"A\") }"})
	wantVerdict(t, r, text, VerdictBlocked)
	wantAll(t, text, "line 2: undefined: os")

	r, text = plan(t, dir, Request{Op: "add", File: fooGo, Source: "import \"os\"\n\nfunc Env() string { return os.Getenv(\"A\") }"})
	wantVerdict(t, r, text, VerdictOK)
	wantAll(t, text, "imports os")

	// Lines count from the source as given, import block and package
	// clause included.
	r, text = plan(t, dir, Request{Op: "add", File: fooGo, Source: "package foo\n\nimport \"os\"\n\nfunc X() { _ = os.Args; nope() }"})
	wantVerdict(t, r, text, VerdictBlocked)
	wantAll(t, text, "line 5: undefined: nope")

	// A directory whose name is not an identifier cannot be a package.
	if _, err := NewWorkspace(dir).Plan(context.Background(), Request{Op: "add", Target: "example.com/m/go", Source: "func X() {}"}); err == nil || !strings.Contains(err.Error(), "not a valid package name") {
		t.Errorf("keyword directory: err = %v", err)
	}

	r, text = plan(t, dir, Request{Op: "add", File: fooGo, Source: "func fmt() {}"})
	wantVerdict(t, r, text, VerdictBlocked)
	wantAll(t, text, "already declared through import")

	r, text = plan(t, dir, Request{Op: "add", File: fooGo, Source: "import \"example.com/nope\"\n\nfunc X() { nope.Y() }"})
	wantVerdict(t, r, text, VerdictBlocked)
	wantAll(t, text, "could not import example.com/nope")

	r, text = plan(t, dir, Request{Op: "add", File: fooGo, Source: "func (s fmt.Stringer) X() {}"})
	wantVerdict(t, r, text, VerdictBlocked)
	wantAll(t, text, "receiver type fmt.Stringer belongs to another package")

	// A package clause is dropped; a directory is a package target.
	r, text = plan(t, dir, Request{Op: "add", Target: filepath.Join(dir, "foo"), Source: "package foo\n\nfunc there() {}"})
	wantVerdict(t, r, text, VerdictOK)
	wantAll(t, text, "foo/there.go (new file)")

	// Parse errors use the source's own line numbers.
	if _, err := NewWorkspace(dir).Plan(context.Background(), Request{Op: "add", File: fooGo, Source: "// c\nfunc ("}); err == nil || !strings.Contains(err.Error(), "line 2:") {
		t.Errorf("parse error = %v", err)
	}

	r, text = plan(t, dir, Request{Op: "replace", File: fooGo, Symbol: "greet", Source: "func greet(n string) string { return strings.ToUpper(n) }"})
	wantVerdict(t, r, text, VerdictBlocked)
	wantAll(t, text, "undefined: strings")

	// One spec of a group is spliced without the keyword.
	r, text = plan(t, dir, Request{Op: "replace", File: fooGo, Symbol: "alpha", Source: "var alpha = 3"})
	wantVerdict(t, r, text, VerdictOK)
	r, text = plan(t, dir, Request{Op: "replace", File: fooGo, Symbol: "alpha", Source: "var alpha = \"s\""})
	wantVerdict(t, r, text, VerdictOK)

	// A test file is checked with the test variant.
	r, text = plan(t, dir, Request{Op: "add", Target: filepath.Join(dir, "foo", "foo_test.go"), Source: "import \"testing\"\n\nfunc TestX(t *testing.T) { _ = greet(\"a\") }"})
	wantVerdict(t, r, text, VerdictOK)
}

// TestPlanCompilesNewImport covers source importing a package the target
// reaches only through a dependency: its types must be the ones the
// dependency refers to.
func TestPlanCompilesNewImport(t *testing.T) {
	dir := writeModule(t, map[string]string{
		"go.mod": "module example.com/h\n\ngo 1.25\n",
		"h/h.go": `package h

import "net/http"

// Path returns the request path.
func Path(r *http.Request) string { return r.URL.Path }
`,
	})
	hGo := filepath.Join(dir, "h", "h.go")
	r, text := plan(t, dir, Request{Op: "add", File: hGo, Source: "import \"net/url\"\n\n// URL returns the request URL.\nfunc URL(r *http.Request) *url.URL { return r.URL }"})
	wantVerdict(t, r, text, VerdictOK)

	r, text = plan(t, dir, Request{Op: "add", File: hGo, Source: "import \"net/url\"\n\nfunc URL(r *http.Request) *url.URL { return nope(r) }"})
	wantVerdict(t, r, text, VerdictBlocked)
	wantAll(t, text, "line 3: undefined: nope")
}

// TestPlanShape covers kind changes, member use sites and lost interface
// satisfaction.
func TestPlanShape(t *testing.T) {
	dir := planModule(t)
	fooGo := filepath.Join(dir, "foo", "foo.go")

	r, text := plan(t, dir, Request{Op: "replace", File: fooGo, Symbol: "Widget", Source: "type Widget interface{ W() }"})
	wantVerdict(t, r, text, VerdictOK)
	wantAll(t, text, "kind changes from struct to interface")

	r, text = plan(t, dir, Request{Op: "replace", File: fooGo, Symbol: "Tagged", Source: "type Tagged struct{ Label string }"})
	wantVerdict(t, r, text, VerdictOK)
	wantAll(t, text, "shape changes: removed Tag, added Label", "Update the uses of removed or changed members (1)", "bar/bar.go:9:", "  .Tag")

	r, text = plan(t, dir, Request{Op: "remove", File: fooGo, Symbol: "Thing.Hello"})
	wantVerdict(t, r, text, VerdictOK)
	wantAll(t, text, "Thing stops satisfying 1 interface", "foo.Store.Hello")
}

// TestPlanMoveDecisions covers exporting the moved symbol itself and the
// closure fixed point.
func TestPlanMoveDecisions(t *testing.T) {
	dir := planModule(t)
	fooGo := filepath.Join(dir, "foo", "foo.go")

	r, text := plan(t, dir, Request{Op: "move", File: fooGo, Symbol: "greet", Target: "example.com/m/baz"})
	wantVerdict(t, r, text, VerdictDecisions)
	wantAll(t, text, "func greet is unexported and used by foo/foo.go:12:41 staying in foo", `decisions.export ["greet"]: move it as Greet`)

	r, text = plan(t, dir, Request{Op: "move", File: fooGo, Symbol: "greet", Target: "example.com/m/baz", Decisions: Decisions{Export: []string{"greet"}}})
	wantVerdict(t, r, text, VerdictOK)
	wantAll(t, text, "exports greet as Greet; rewrites 1 use in foo to baz.X")

	// inner is used by Chain.Run and by outer; outer is pulled, so inner
	// has no other user left and is pulled too, then beta and alpha.
	r, text = plan(t, dir, Request{Op: "move", File: fooGo, Symbol: "Chain", Target: "example.com/m/baz"})
	wantVerdict(t, r, text, VerdictOK)
	wantAll(t, text, "pulls outer, inner, beta, alpha along")
}
