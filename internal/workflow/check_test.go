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
