package workflow

import (
	"context"
	"fmt"
	"testing"

	"github.com/deixis/governor/internal/config"
	"github.com/deixis/governor/internal/runner"
)

func TestAudit_AllDone(t *testing.T) {
	// Only test coverage step — it uses go test + go tool cover.
	fr := &fakeRunner{
		Results: map[string]*runner.Result{
			"go test": {ExitCode: 0},
			"go tool": {ExitCode: 0, Stdout: []byte("example.com/foo/bar.go:12:\tFuncA\t\t75.0%\n")},
		},
	}
	e := &Engine{
		Config:    &config.Config{Audit: config.AuditConfig{Steps: []string{"coverage"}}},
		Runner:    fr,
		Workspace: "/project",
		RepoRoot:  "/project",
	}

	result, err := e.Audit(context.Background(), nil)
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if len(result.Steps) != 1 {
		t.Fatalf("len(Steps) = %d, want 1", len(result.Steps))
	}
	if result.Steps[0].Status != "done" {
		t.Errorf("Steps[0].Status = %q, want done", result.Steps[0].Status)
	}
}

func TestAudit_StepError_NoFailFast(t *testing.T) {
	// Two steps: first errors, second should still run.
	fr := &fakeRunner{
		Results: map[string]*runner.Result{
			"go test": {ExitCode: 0},
			"go tool": {ExitCode: 0, Stdout: []byte("example.com/foo/bar.go:12:\tFuncA\t\t75.0%\n")},
		},
		Err: map[string]error{
			// gocognit will fail because resolveTool returns nil in test.
			// But we can make go test fail for coverage to test the pattern.
		},
	}

	// Use steps where the tool won't be found (gocognit, deadcode).
	// resolveTool will return nil for these in a test environment,
	// producing ErrToolUnavailable.
	e := &Engine{
		Config: &config.Config{Audit: config.AuditConfig{
			Steps: []string{"complexity", "coverage"},
		}},
		Runner:    fr,
		Workspace: "/project",
		RepoRoot:  "/project",
	}

	result, err := e.Audit(context.Background(), nil)
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if len(result.Steps) != 2 {
		t.Fatalf("len(Steps) = %d, want 2", len(result.Steps))
	}
	// Complexity should be unavailable (gocognit not in PATH during test).
	if result.Steps[0].Status != "unavailable" {
		t.Errorf("Steps[0].Status = %q, want unavailable", result.Steps[0].Status)
	}
	// Coverage should still have run despite prior unavailable step.
	if result.Steps[1].Status != "done" {
		t.Errorf("Steps[1].Status = %q, want done", result.Steps[1].Status)
	}
}

func TestAudit_MultipleErrors(t *testing.T) {
	fr := &fakeRunner{
		Results: map[string]*runner.Result{},
		Err: map[string]error{
			"go test": fmt.Errorf("connection refused"),
		},
	}
	e := &Engine{
		Config: &config.Config{Audit: config.AuditConfig{
			Steps: []string{"coverage", "complexity"},
		}},
		Runner:    fr,
		Workspace: "/project",
		RepoRoot:  "/project",
	}

	result, err := e.Audit(context.Background(), nil)
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	// Both should have error/unavailable status, neither should be skipped.
	for i, step := range result.Steps {
		if step.Status == "skipped" || step.Status == "done" {
			t.Errorf("Steps[%d].Status = %q, want error or unavailable", i, step.Status)
		}
	}
}

func TestAudit_UnknownStep(t *testing.T) {
	fr := &fakeRunner{Results: map[string]*runner.Result{}}
	e := &Engine{
		Config:    &config.Config{Audit: config.AuditConfig{Steps: []string{"bogus"}}},
		Runner:    fr,
		Workspace: "/project",
		RepoRoot:  "/project",
	}

	result, err := e.Audit(context.Background(), nil)
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if result.Steps[0].Status != "error" {
		t.Errorf("Steps[0].Status = %q, want error", result.Steps[0].Status)
	}
}
