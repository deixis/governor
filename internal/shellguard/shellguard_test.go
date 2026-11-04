package shellguard

import (
	"strings"
	"testing"
)

func TestInspect_Rewrites(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"go test ./internal/submission/", "governor check ./internal/submission/"},
		{"go test -race -count=1 ./a/ ./b/", "governor check ./a/ ./b/"},
		{"go test -v -timeout 5m ./...", "governor check -v -timeout 5m ./..."},
		{"go test -timeout=30s .", "governor check -timeout 30s ."},
		{"go test", "governor check ."},
		{"go vet", "governor check ."},
		{"golangci-lint run", "governor check"},
		{"go vet ./internal/...", "governor check ./internal/..."},
		{"go vet -tags integration ./x", "governor check ./x"},
		{"staticcheck ./internal/submission/", "governor check ./internal/submission/"},
		{"go tool staticcheck -checks all,-ST1000 ./a/ ./b/", "governor check ./a/ ./b/"},
		{"golangci-lint run ./...", "governor check ./..."},
		{"golangci-lint run --fix --timeout 5m ./pkg/", "governor check -fix ./pkg/"},
		{"goimports -w a.go b.go", "governor fix a.go b.go"},
		{"goimports -local example.com -w internal/x/a.go", "governor fix internal/x/a.go"},
		{"gofmt -w a.go", "governor fix a.go"},
		{"gofumpt -w a.go", "governor fix a.go"},
		// Compound forms: every command is inspected, the rest is kept.
		{
			"go vet ./internal/... && go test -race -count=1 ./internal/ 2>&1 | rg -v '^ok' | head -n 40",
			"governor check ./internal/... && governor check ./internal/ 2>&1 | rg -v '^ok' | head -n 40",
		},
		{"goimports -w a.go && go vet ./x/ 2>&1 | head -n 15", "governor fix a.go && governor check ./x/ 2>&1 | head -n 15"},
	}
	for _, c := range cases {
		d := Inspect(c.in)
		if d.Blocked != "" {
			t.Errorf("%q: blocked: %s", c.in, d.Blocked)
			continue
		}
		if d.Command != c.want {
			t.Errorf("%q:\n got %q\nwant %q", c.in, d.Command, c.want)
		}
		if len(d.Rewrites) == 0 {
			t.Errorf("%q: no rewrite recorded", c.in)
		}
	}
}

func TestInspect_Unchanged(t *testing.T) {
	cases := []string{
		"go test -run TestFoo ./x",
		"go test -run=TestFoo/sub -v ./x",
		"go test -bench . ./x",
		"go test -coverprofile=c.out ./x",
		"go test -json ./x",
		"go build ./...",
		"go run ./cmd/x",
		"gofmt -l .",
		"gofmt -d a.go",
		"goimports -w ./internal",
		"golangci-lint version",
		"golangci-lint linters",
		"rg -n 'func' a.go",
		"awk 'NR>=10 && NR<=20' a.go",
		"sed -n '10,20p' a.go",
		"sed -i '' 's/a/b/' README.md",
		"perl -pi -e 's/a/b/' notes.md",
		"sd 'a' 'b' README.md",
		"sd -p 'a' 'b' a.go",
		"git status",
		"",
	}
	for _, in := range cases {
		d := Inspect(in)
		if d.Changed() || d.Command != in {
			t.Errorf("%q changed: %+v", in, d)
		}
	}
}

func TestInspect_Blocks(t *testing.T) {
	cases := []string{
		"sed -i '' 's/a/b/' internal/x/a.go",
		"sed -i.bak 's/a/b/' a.go",
		"gsed --in-place 's/a/b/' a.go",
		"perl -pi -e 's/a/b/' a.go",
		"perl -0pi -e 's/x\\n/y/' internal/submission/service_test.go && rg -n 'x' internal/submission/service_test.go",
		"perl -i -pe 's/a/b/' a.go",
		"sd 'a' 'b' internal/lifecycle/gateway_test.go",
		"sd 'a' 'b' internal/lifecycle/gateway_test.go && go vet ./internal/...",
		"awk -i inplace '{print}' a.go",
		"gawk --inplace '{print}' a.go",
		"for f in a.go b.go; do sed -i '' 's/a/b/' $f; done",
		"sed -i '' 's/a/b/' $d/pkg/x/*.go",
	}
	for _, in := range cases {
		d := Inspect(in)
		if d.Blocked == "" {
			t.Errorf("%q not blocked: %+v", in, d)
			continue
		}
		if !strings.Contains(d.Blocked, "gov_diagnostics") || d.Command != in || len(d.Rewrites) != 0 {
			t.Errorf("%q: %+v", in, d)
		}
	}
}

func TestInspect_InvalidShell(t *testing.T) {
	in := "go test ./x && ("
	if d := Inspect(in); d.Changed() || d.Command != in {
		t.Errorf("invalid input changed: %+v", d)
	}
}

func TestContext(t *testing.T) {
	d := Inspect("go test ./x")
	ctx := d.Context()
	for _, want := range []string{"`go test ./x` -> `governor check ./x`", "Tell the user", "gov_check"} {
		if !strings.Contains(ctx, want) {
			t.Errorf("missing %q in:\n%s", want, ctx)
		}
	}
	if Inspect("git status").Context() != "" {
		t.Error("context for an unchanged command")
	}
}
