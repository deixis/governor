package workflow

import (
	"context"
	"errors"
	"fmt"

	"github.com/deixis/governor/internal/report"
	"github.com/google/uuid"
)

// AuditResult holds the full outcome of an audit run.
type AuditResult struct {
	RunResult *report.RunResult
	Steps     []AuditStepResult
}

// AuditStepResult holds the outcome of a single audit step.
type AuditStepResult struct {
	Name   string
	Status string // done, error, unavailable, skipped
	Detail string // error or unavailability message
	Output string // formatted summary (only when done)
}

// Audit runs all configured audit steps (coverage, complexity, deadcode,
// dupl, vulncheck) without stopping on failure.
func (e *Engine) Audit(ctx context.Context, packages []string) (*AuditResult, error) {
	runID := uuid.New().String()
	pkgs := e.ResolvePackages(packages)

	rr := &report.RunResult{ID: runID, Kind: report.Audit}

	steps := e.Config.AuditSteps()
	results := make([]AuditStepResult, len(steps))
	for i, step := range steps {
		results[i] = AuditStepResult{Name: step, Status: "skipped"}
	}

	// Run all steps — no fail-fast.
	for i, step := range steps {
		switch step {
		case "coverage":
			entries, err := e.runCoverage(ctx, pkgs)
			if err != nil {
				var unavail ErrToolUnavailable
				if errors.As(err, &unavail) {
					results[i] = AuditStepResult{Name: step, Status: "unavailable", Detail: err.Error()}
				} else {
					results[i] = AuditStepResult{Name: step, Status: "error", Detail: err.Error()}
				}
			} else {
				rr.Coverage = entries
				results[i] = AuditStepResult{Name: step, Status: "done", Output: FormatCoverageSummary(entries)}
			}

		case "complexity":
			entries, err := e.runComplexity(ctx, pkgs)
			if err != nil {
				var unavail ErrToolUnavailable
				if errors.As(err, &unavail) {
					results[i] = AuditStepResult{Name: step, Status: "unavailable", Detail: err.Error()}
				} else {
					results[i] = AuditStepResult{Name: step, Status: "error", Detail: err.Error()}
				}
			} else {
				rr.Complexity = entries
				results[i] = AuditStepResult{Name: step, Status: "done", Output: FormatComplexitySummary(entries)}
			}

		case "deadcode":
			funcs, err := e.runDeadcode(ctx, pkgs)
			if err != nil {
				var unavail ErrToolUnavailable
				if errors.As(err, &unavail) {
					results[i] = AuditStepResult{Name: step, Status: "unavailable", Detail: err.Error()}
				} else {
					results[i] = AuditStepResult{Name: step, Status: "error", Detail: err.Error()}
				}
			} else {
				rr.DeadFuncs = funcs
				results[i] = AuditStepResult{Name: step, Status: "done", Output: FormatDeadcodeSummary(funcs)}
			}

		case "dupl":
			duplicates, err := e.runDupl(ctx, pkgs)
			if err != nil {
				var unavail ErrToolUnavailable
				if errors.As(err, &unavail) {
					results[i] = AuditStepResult{Name: step, Status: "unavailable", Detail: err.Error()}
				} else {
					results[i] = AuditStepResult{Name: step, Status: "error", Detail: err.Error()}
				}
			} else {
				rr.Duplicates = duplicates
				results[i] = AuditStepResult{Name: step, Status: "done", Output: FormatDuplSummary(duplicates)}
			}

		case "vulncheck":
			vulns, err := e.runVulncheck(ctx, pkgs)
			if err != nil {
				var unavail ErrToolUnavailable
				if errors.As(err, &unavail) {
					results[i] = AuditStepResult{Name: step, Status: "unavailable", Detail: err.Error()}
				} else {
					results[i] = AuditStepResult{Name: step, Status: "error", Detail: err.Error()}
				}
			} else {
				rr.Vulns = vulns
				results[i] = AuditStepResult{Name: step, Status: "done", Output: FormatVulncheckSummary(vulns)}
			}

		default:
			results[i] = AuditStepResult{Name: step, Status: "error", Detail: fmt.Sprintf("unknown step: %s", step)}
		}
	}

	return &AuditResult{
		RunResult: rr,
		Steps:     results,
	}, nil
}
