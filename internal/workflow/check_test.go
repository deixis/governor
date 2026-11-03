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
	if n := len(result.RunResult.FormatIssues); n != 1 || result.RunResult.FormatIssues[0].File != "a.go" {
		t.Errorf("FormatIssues = %+v, want one issue for a.go", result.RunResult.FormatIssues)
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
