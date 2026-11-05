package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/deixis/governor/internal/config"
	"github.com/deixis/governor/internal/runner"
)

// fakeRunner is a test double for CommandRunner. It returns predetermined
// results based on the first element of argv.
type fakeRunner struct {
	// Results maps a command key to the result it should return.
	// The key is derived from argv by the caller.
	Results map[string]*runner.Result
	Err     map[string]error
}

func (f *fakeRunner) Run(_ context.Context, argv []string, _ string) (*runner.Result, error) {
	key := fakeRunnerKey(argv)
	if err, ok := f.Err[key]; ok {
		return nil, err
	}
	if r, ok := f.Results[key]; ok {
		return r, nil
	}
	// Default: success with no output.
	return &runner.Result{ExitCode: 0}, nil
}

// fakeRunnerKey builds a lookup key from argv. Uses the command name
// (last element of the argv prefix before arguments starting with -).
func fakeRunnerKey(argv []string) string {
	// For "go test -json ./...", key is "go test"
	// For "golangci-lint run --out-format json", key is "golangci-lint"
	if len(argv) >= 2 && argv[0] == "go" {
		return "go " + argv[1]
	}
	if len(argv) > 0 {
		return argv[0]
	}
	return ""
}

func passingTestJSON() []byte {
	events := []test2jsonEvent{
		{Action: "pass", Package: "example.com/foo", Test: "TestAdd"},
	}
	var buf strings.Builder
	for _, ev := range events {
		data, _ := json.Marshal(ev)
		buf.Write(data)
		buf.WriteByte('\n')
	}
	return []byte(buf.String())
}

func failingTestJSON() []byte {
	events := []test2jsonEvent{
		{Action: "output", Package: "example.com/foo", Test: "TestAdd", Output: "expected 4, got 5\n"},
		{Action: "fail", Package: "example.com/foo", Test: "TestAdd"},
		{Action: "fail", Package: "example.com/foo"},
	}
	var buf strings.Builder
	for _, ev := range events {
		data, _ := json.Marshal(ev)
		buf.Write(data)
		buf.WriteByte('\n')
	}
	return []byte(buf.String())
}

func TestCheck_GofumptFailureIsAnError(t *testing.T) {
	// A version-manager shim with no version set prints a message and a
	// version list on a non-zero exit. That output is not a file list.
	shimOut := "No version is set for command gofumpt\nConsider adding one of the following versions in your config file at .tool-versions\ngofumpt 0.7.0\ngofumpt 0.11.0\n"
	fr := &fakeRunner{
		Results: map[string]*runner.Result{
			"go list":       {ExitCode: 0, Stdout: []byte("/project/a.go\n")},
			"/stub/gofumpt": {ExitCode: 126, Stderr: []byte(shimOut)},
		},
	}
	e := &Engine{
		Config:       &config.Config{Check: config.CheckConfig{Steps: []string{"test"}}},
		Runner:       fr,
		Workspace:    "/project",
		RepoRoot:     "/project",
		ToolResolver: func(name string) []string { return []string{"/stub/" + name} },
	}

	result, err := e.Check(context.Background(), nil, false)
	if err == nil {
		t.Fatalf("Check = %+v, want error", result)
	}
	if !strings.Contains(err.Error(), "gofumpt failed (exit 126)") || !strings.Contains(err.Error(), "No version is set") {
		t.Errorf("error = %q, want gofumpt failure with shim output", err)
	}
}

// argvRunner answers by full argv, for tests that must tell two `go list`
// invocations apart.
type argvRunner struct {
	calls   [][]string
	results map[string]*runner.Result
}

func (a *argvRunner) Run(_ context.Context, argv []string, _ string) (*runner.Result, error) {
	a.calls = append(a.calls, argv)
	if r, ok := a.results[strings.Join(argv, " ")]; ok {
		return r, nil
	}
	return &runner.Result{ExitCode: 0}, nil
}

func TestPackages_Exclude(t *testing.T) {
	ar := &argvRunner{results: map[string]*runner.Result{
		"go list -f {{.ImportPath}} ./...":     {Stdout: []byte("example.com/m/a\nexample.com/m/e2e\nexample.com/m/e2e/sub\n")},
		"go list -f {{.ImportPath}} ./e2e/...": {Stdout: []byte("example.com/m/e2e\nexample.com/m/e2e/sub\n")},
	}}
	e := &Engine{Config: &config.Config{}, Runner: ar, Workspace: "/project", RepoRoot: "/project"}

	got, err := e.Packages(context.Background(), nil, []string{"./e2e/..."})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "example.com/m/a" {
		t.Errorf("Packages = %v, want [example.com/m/a]", got)
	}
}

func TestPackages_NoExcludePassesThrough(t *testing.T) {
	ar := &argvRunner{}
	e := &Engine{Config: &config.Config{}, Runner: ar, Workspace: "/project", RepoRoot: "/project"}

	got, err := e.Packages(context.Background(), []string{"./pkg/..."}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "./pkg/..." {
		t.Errorf("Packages = %v, want [./pkg/...]", got)
	}
	if len(ar.calls) != 0 {
		t.Errorf("go list called %d times, want 0", len(ar.calls))
	}
}

func TestCheck_TestExcludeAppliesToTestStepOnly(t *testing.T) {
	ar := &argvRunner{results: map[string]*runner.Result{
		"go list -f {{.ImportPath}} ./...":     {Stdout: []byte("example.com/m/a\nexample.com/m/e2e\n")},
		"go list -f {{.ImportPath}} ./e2e/...": {Stdout: []byte("example.com/m/e2e\n")},
		"go test -json example.com/m/a":        {Stdout: passingTestJSON()},
	}}
	e := &Engine{
		Config: &config.Config{
			Test:  config.TestConfig{Exclude: []string{"./e2e/..."}},
			Check: config.CheckConfig{Steps: []string{"test", "staticcheck"}},
		},
		Runner:       ar,
		Workspace:    "/project",
		RepoRoot:     "/project",
		ToolResolver: func(name string) []string { return []string{"/stub/" + name} },
	}

	if _, err := e.Check(context.Background(), nil, false); err != nil {
		t.Fatal(err)
	}
	var test, staticcheck []string
	for _, call := range ar.calls {
		switch {
		case call[0] == "go" && call[1] == "test":
			test = call
		case strings.HasSuffix(call[0], "staticcheck"):
			staticcheck = call
		}
	}
	if test == nil || test[len(test)-1] != "example.com/m/a" {
		t.Errorf("go test ran on %v, want example.com/m/a only", test)
	}
	if staticcheck == nil || staticcheck[len(staticcheck)-1] != "./..." {
		t.Errorf("staticcheck ran on %v, want ./... (no exclude configured)", staticcheck)
	}
}

func TestCheck_GofumptCoversPackageFilesOnly(t *testing.T) {
	// Formatting must run on the files go list reports for the selected
	// packages, not on ".", which would sweep worktrees and vendored
	// copies under the workspace.
	ar := &argvRunner{results: map[string]*runner.Result{
		"go list -f " + goFilesTemplate + " ./...":          {Stdout: []byte("/project/a.go\n/project/a_test.go\n")},
		"/stub/gofumpt -l /project/a.go /project/a_test.go": {Stdout: []byte("/project/a.go\n")},
		"go list -e -f {{.Dir}}\t{{.ImportPath}} ./...":     {Stdout: []byte("/project\texample.com/m\n")},
	}}
	e := &Engine{
		Config:       &config.Config{Check: config.CheckConfig{Steps: []string{"test"}}},
		Runner:       ar,
		Workspace:    "/project",
		RepoRoot:     "/project",
		ToolResolver: func(name string) []string { return []string{"/stub/" + name} },
	}

	result, err := e.Check(context.Background(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.FailedIdx != -2 {
		t.Errorf("FailedIdx = %d, want -2 (format failure)", result.FailedIdx)
	}
	if n := len(result.RunResult.FormatIssues); n != 1 || result.RunResult.FormatIssues[0].File != "a.go" || result.RunResult.FormatIssues[0].Package != "example.com/m" {
		t.Errorf("FormatIssues = %+v, want one issue for a.go in example.com/m", result.RunResult.FormatIssues)
	}
	for _, call := range ar.calls {
		if strings.HasSuffix(call[0], "gofumpt") && call[len(call)-1] == "." {
			t.Errorf("gofumpt ran on '.': %v", call)
		}
	}
}

func TestCheck_AllPass(t *testing.T) {
	fr := &fakeRunner{
		Results: map[string]*runner.Result{
			"go test": {ExitCode: 0, Stdout: passingTestJSON()},
		},
	}
	e := &Engine{
		Config:    &config.Config{Check: config.CheckConfig{Steps: []string{"test"}}},
		Runner:    fr,
		Workspace: "/project",
		RepoRoot:  "/project",
	}

	result, err := e.Check(context.Background(), nil, false)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if result.FailedIdx != -1 {
		t.Errorf("FailedIdx = %d, want -1", result.FailedIdx)
	}
	if len(result.Steps) != 1 {
		t.Fatalf("len(Steps) = %d, want 1", len(result.Steps))
	}
	if result.Steps[0].Status != "pass" {
		t.Errorf("Steps[0].Status = %q, want pass", result.Steps[0].Status)
	}
	if result.RunResult.TestFailures != nil {
		t.Errorf("TestFailures = %v, want nil", result.RunResult.TestFailures)
	}
}

func TestCheck_TestFails(t *testing.T) {
	fr := &fakeRunner{
		Results: map[string]*runner.Result{
			"go test": {ExitCode: 1, Stdout: failingTestJSON()},
		},
	}
	e := &Engine{
		Config:    &config.Config{Check: config.CheckConfig{Steps: []string{"test", "lint"}}},
		Runner:    fr,
		Workspace: "/project",
		RepoRoot:  "/project",
	}

	result, err := e.Check(context.Background(), nil, false)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if result.FailedIdx != 0 {
		t.Errorf("FailedIdx = %d, want 0", result.FailedIdx)
	}
	if result.Steps[0].Status != "fail" {
		t.Errorf("Steps[0].Status = %q, want fail", result.Steps[0].Status)
	}
	// Lint step should be skipped.
	if result.Steps[1].Status != "skipped" {
		t.Errorf("Steps[1].Status = %q, want skipped", result.Steps[1].Status)
	}
	// RunResult should have test failures.
	if len(result.RunResult.TestFailures) != 1 {
		t.Errorf("len(TestFailures) = %d, want 1", len(result.RunResult.TestFailures))
	}
}

func TestCheck_UnknownStep(t *testing.T) {
	fr := &fakeRunner{
		Results: map[string]*runner.Result{
			"go test": {ExitCode: 0, Stdout: passingTestJSON()},
		},
	}
	e := &Engine{
		Config:    &config.Config{Check: config.CheckConfig{Steps: []string{"test", "bogus"}}},
		Runner:    fr,
		Workspace: "/project",
		RepoRoot:  "/project",
	}

	result, err := e.Check(context.Background(), nil, false)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if result.FailedIdx != 1 {
		t.Errorf("FailedIdx = %d, want 1", result.FailedIdx)
	}
	if !strings.Contains(result.Steps[1].Output, "unknown step") {
		t.Errorf("expected unknown step error, got %q", result.Steps[1].Output)
	}
}

func TestCheck_NilSlicesOnPass(t *testing.T) {
	fr := &fakeRunner{
		Results: map[string]*runner.Result{
			"go test": {ExitCode: 0, Stdout: passingTestJSON()},
		},
	}
	e := &Engine{
		Config:    &config.Config{Check: config.CheckConfig{Steps: []string{"test"}}},
		Runner:    fr,
		Workspace: "/project",
		RepoRoot:  "/project",
	}

	result, err := e.Check(context.Background(), nil, false)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	rr := result.RunResult
	if rr.TestFailures != nil {
		t.Errorf("TestFailures should be nil, got %v", rr.TestFailures)
	}
	if rr.BuildErrors != nil {
		t.Errorf("BuildErrors should be nil, got %v", rr.BuildErrors)
	}
	if rr.LintIssues != nil {
		t.Errorf("LintIssues should be nil, got %v", rr.LintIssues)
	}
	if rr.StaticIssues != nil {
		t.Errorf("StaticIssues should be nil, got %v", rr.StaticIssues)
	}
}

func TestCheck_StaticcheckIssuesAttributedToPackage(t *testing.T) {
	ar := &argvRunner{results: map[string]*runner.Result{
		"/stub/staticcheck -f json ./...": {ExitCode: 1, Stdout: []byte(
			`{"code":"U1000","severity":"error","location":{"file":"/project/internal/a/a.go","line":3,"column":6},"message":"func f is unused"}` + "\n" +
				`{"code":"compile","severity":"error","location":{"file":"","line":0,"column":0},"message":"load failed"}` + "\n",
		)},
		"go list -e -f {{.Dir}}\t{{.ImportPath}} ./...": {Stdout: []byte("/project/internal/a\texample.com/m/internal/a\n")},
	}}
	e := &Engine{
		Config:       &config.Config{Check: config.CheckConfig{Steps: []string{"staticcheck"}}},
		Runner:       ar,
		Workspace:    "/project",
		RepoRoot:     "/project",
		ToolResolver: func(name string) []string { return []string{"/stub/" + name} },
	}

	result, err := e.Check(context.Background(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	issues := result.RunResult.StaticIssues
	if len(issues) != 2 {
		t.Fatalf("StaticIssues = %+v, want 2", issues)
	}
	if issues[0].Package != "example.com/m/internal/a" || issues[0].File != "internal/a/a.go" {
		t.Errorf("issue = {Package: %q, File: %q}, want {example.com/m/internal/a, internal/a/a.go}", issues[0].Package, issues[0].File)
	}
	if issues[1].Package != "" {
		t.Errorf("file-less issue Package = %q, want empty", issues[1].Package)
	}

	failures := strings.Join(FormatFailureSymbols(result.RunResult), "\n")
	for _, want := range []string{"example.com/m/internal/a — 1 staticcheck issues", "(no package) — 1 staticcheck issues"} {
		if !strings.Contains(failures, want) {
			t.Errorf("failures missing %q:\n%s", want, failures)
		}
	}
}

func TestCheck_StaticcheckFailureWithoutIssuesFails(t *testing.T) {
	ar := &argvRunner{results: map[string]*runner.Result{
		"/stub/staticcheck -f json ./...": {ExitCode: 126, Stderr: []byte("No version is set for command staticcheck\n")},
	}}
	e := &Engine{
		Config:       &config.Config{Check: config.CheckConfig{Steps: []string{"staticcheck"}}},
		Runner:       ar,
		Workspace:    "/project",
		RepoRoot:     "/project",
		ToolResolver: func(name string) []string { return []string{"/stub/" + name} },
	}

	result, err := e.Check(context.Background(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	step := result.Steps[0]
	if step.Status != "fail" || !strings.Contains(step.Output, "No version is set") {
		t.Errorf("step = %+v, want fail with shim output", step)
	}
}

func TestCheck_LintFailureWithoutIssuesFails(t *testing.T) {
	ar := &argvRunner{results: map[string]*runner.Result{
		"/stub/golangci-lint run --output.json.path stdout ./...": {ExitCode: 126, Stderr: []byte("No version is set for command golangci-lint\n")},
	}}
	e := &Engine{
		Config:       &config.Config{Check: config.CheckConfig{Steps: []string{"lint"}}},
		Runner:       ar,
		Workspace:    "/project",
		RepoRoot:     "/project",
		ToolResolver: func(name string) []string { return []string{"/stub/" + name} },
	}

	result, err := e.Check(context.Background(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	step := result.Steps[0]
	if step.Status != "fail" || !strings.Contains(step.Output, "No version is set") {
		t.Errorf("step = %+v, want fail with shim output", step)
	}
}

// golangci-lint v2 rejects --out-format; its JSON output flag is
// --output.json.path. The version probe picks the flag: v1 only when the
// version reads as v1, v2 otherwise.
func TestCheck_LintOutputFlagByVersion(t *testing.T) {
	issues := []byte(`{"Issues":[{"FromLinter":"errcheck","Text":"unchecked","Pos":{"Filename":"internal/a/a.go","Line":5,"Column":2}}]}`)
	cases := []struct {
		name, version, run string
	}{
		{"v2", "golangci-lint has version 2.14.0 built with go1.26.0 from abc on 2026-01-01", "/stub/golangci-lint run --output.json.path stdout ./..."},
		{"v1", "golangci-lint has version v1.64.8 built with go1.24.1 from abc on 2025-03-17", "/stub/golangci-lint run --out-format json ./..."},
		{"unreadable", "", "/stub/golangci-lint run --output.json.path stdout ./..."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ar := &argvRunner{results: map[string]*runner.Result{
				"/stub/golangci-lint --version": {Stdout: []byte(c.version + "\n")},
				c.run:                           {ExitCode: 1, Stdout: issues},
			}}
			e := &Engine{
				Config:       &config.Config{Check: config.CheckConfig{Steps: []string{"lint"}}},
				Runner:       ar,
				Workspace:    "/project",
				RepoRoot:     "/project",
				ToolResolver: func(name string) []string { return []string{"/stub/" + name} },
			}
			result, err := e.Check(context.Background(), nil, false)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.RunResult.LintIssues) != 1 {
				t.Errorf("LintIssues = %+v, want one issue from %q", result.RunResult.LintIssues, c.run)
			}
		})
	}
}

// golangci-lint v2 takes every argument as a directory, so import-path
// patterns of workspace modules reach it as ./ patterns.
func TestCheck_LintImportPathPatterns(t *testing.T) {
	ar := &argvRunner{results: map[string]*runner.Result{
		"go list -m -f {{.Path}}\t{{.Dir}}": {Stdout: []byte("example.com/m\t/project\nexample.com/m/sub\t/project/sub\n")},
	}}
	e := &Engine{
		Config:       &config.Config{Check: config.CheckConfig{Steps: []string{"lint"}}},
		Runner:       ar,
		Workspace:    "/project",
		RepoRoot:     "/project",
		ToolResolver: func(name string) []string { return []string{"/stub/" + name} },
	}
	in := []string{"example.com/m/internal/route/...", "example.com/m/a", "example.com/m/...", "example.com/m", "example.com/m/sub/x", "./b/...", "golang.org/x/tools/..."}
	if _, err := e.Check(context.Background(), in, false); err != nil {
		t.Fatal(err)
	}
	want := "/stub/golangci-lint run --output.json.path stdout ./internal/route/... ./a ./... . ./sub/x ./b/... golang.org/x/tools/..."
	for _, call := range ar.calls {
		if got := strings.Join(call, " "); strings.HasPrefix(got, "/stub/golangci-lint run") {
			if got != want {
				t.Errorf("lint argv:\n got %s\nwant %s", got, want)
			}
			return
		}
	}
	t.Error("golangci-lint run not called")
}

func TestCheck_LintIssuesAttributedToPackage(t *testing.T) {
	ar := &argvRunner{results: map[string]*runner.Result{
		"/stub/golangci-lint run --output.json.path stdout ./...": {ExitCode: 1, Stdout: []byte(
			`{"Issues":[{"FromLinter":"errcheck","Text":"unchecked","Pos":{"Filename":"internal/a/a.go","Line":5,"Column":2}}]}`,
		)},
		"go list -e -f {{.Dir}}\t{{.ImportPath}} ./...": {Stdout: []byte("/project/internal/a\texample.com/m/internal/a\n")},
	}}
	e := &Engine{
		Config:       &config.Config{Check: config.CheckConfig{Steps: []string{"lint"}}},
		Runner:       ar,
		Workspace:    "/project",
		RepoRoot:     "/project",
		ToolResolver: func(name string) []string { return []string{"/stub/" + name} },
	}

	result, err := e.Check(context.Background(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	issues := result.RunResult.LintIssues
	if len(issues) != 1 || issues[0].Package != "example.com/m/internal/a" || issues[0].File != "internal/a/a.go" {
		t.Errorf("LintIssues = %+v, want one in example.com/m/internal/a at internal/a/a.go", issues)
	}
}
