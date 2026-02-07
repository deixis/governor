package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/deixis/governor/internal/report"
	"github.com/google/uuid"
)

// CheckResult holds the full outcome of a check run.
type CheckResult struct {
	RunResult *report.RunResult
	Steps     []StepResult
	FailedIdx int // -1 if all passed
}

// StepResult holds the outcome of a single check step.
type StepResult struct {
	Name   string
	Status string // pass, fail, skipped, unavailable
	Detail string // extra info (e.g. "golangci-lint not found")
	Output string // summary from the underlying tool (only on failure)
}

// Check runs the full check pipeline: optional fix phase, then
// configured check steps (test, lint, staticcheck) in sequence,
// stopping on first failure.
func (e *Engine) Check(ctx context.Context, packages []string, fix bool) (*CheckResult, error) {
	runID := uuid.New().String()
	pkgs := e.ResolvePackages(packages)

	rr := &report.RunResult{ID: runID, Kind: report.Check}

	// --- Fix phase ---
	fixRes, _ := e.RunFixPhase(ctx, fix)
	if fixRes != nil {
		rr.AutoFixes = fixRes.AutoFixes
		rr.FormatIssues = fixRes.FormatIssues
	}

	// If fix=false and there are format issues, treat as failure.
	if !fix && len(rr.FormatIssues) > 0 {
		return &CheckResult{
			RunResult: rr,
			FailedIdx: -2, // sentinel: format failure before steps ran
		}, nil
	}

	// --- Check phase ---
	steps := e.Config.CheckSteps()
	results := make([]StepResult, len(steps))
	for i, step := range steps {
		results[i] = StepResult{Name: step, Status: "skipped"}
	}

	failedIdx := -1
	for i, step := range steps {
		switch step {
		case "test":
			summary, err := e.runTest(ctx, pkgs)
			if err != nil {
				results[i] = StepResult{Name: step, Status: "fail", Output: err.Error()}
				failedIdx = i
			} else if summary.Status == "FAIL" {
				results[i] = StepResult{Name: step, Status: "fail", Output: summary.String()}
				failedIdx = i
				for _, f := range summary.Errors {
					msg := FirstLine(f.Output)
					rr.TestFailures = append(rr.TestFailures, report.TestFailure{
						Package: f.Package,
						Test:    f.Test,
						Message: msg,
						Output:  f.Output,
					})
				}
				for _, be := range summary.BuildErrors {
					rr.BuildErrors = append(rr.BuildErrors, report.BuildError{
						Package: be.ImportPath,
						Message: be.Output,
					})
				}
			} else {
				results[i] = StepResult{Name: step, Status: "pass"}
			}

		case "lint":
			summary, err := e.runLint(ctx, pkgs)
			if err != nil {
				var unavail ErrToolUnavailable
				if errors.As(err, &unavail) {
					results[i] = StepResult{Name: step, Status: "unavailable", Detail: err.Error()}
					failedIdx = i
				} else {
					results[i] = StepResult{Name: step, Status: "fail", Output: err.Error()}
					failedIdx = i
				}
			} else if len(summary.Issues) > 0 {
				results[i] = StepResult{Name: step, Status: "fail", Output: summary.String()}
				failedIdx = i
				for _, issue := range summary.Issues {
					rr.LintIssues = append(rr.LintIssues, report.LintIssue{
						File:    issue.File,
						Line:    issue.Line,
						Col:     issue.Column,
						Linter:  issue.Linter,
						Message: issue.Message,
					})
				}
			} else {
				results[i] = StepResult{Name: step, Status: "pass"}
			}

		case "staticcheck":
			scResult, err := e.runStaticcheck(ctx, pkgs)
			if err != nil {
				var unavail ErrToolUnavailable
				if errors.As(err, &unavail) {
					results[i] = StepResult{Name: step, Status: "unavailable", Detail: err.Error()}
					failedIdx = i
				} else {
					results[i] = StepResult{Name: step, Status: "fail", Output: err.Error()}
					failedIdx = i
				}
			} else if len(scResult.Issues) > 0 {
				results[i] = StepResult{Name: step, Status: "fail", Output: scResult.String()}
				failedIdx = i
				rr.StaticIssues = scResult.Issues
			} else {
				results[i] = StepResult{Name: step, Status: "pass"}
			}

		default:
			results[i] = StepResult{Name: step, Status: "fail", Output: fmt.Sprintf("unknown step: %s", step)}
			failedIdx = i
		}

		if failedIdx >= 0 {
			break
		}
	}

	return &CheckResult{
		RunResult: rr,
		Steps:     results,
		FailedIdx: failedIdx,
	}, nil
}

// FirstLine returns the first non-empty line of s, trimmed,
// skipping test framework boilerplate lines.
func FirstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "=== RUN") && !strings.HasPrefix(line, "--- FAIL") {
			return line
		}
	}
	return ""
}

// FormatFailureSymbols builds Go-qualified symbol references for failures.
func FormatFailureSymbols(rr *report.RunResult) []string {
	var out []string

	for _, f := range rr.TestFailures {
		sym := f.Package + "." + f.Test
		msg := f.Message
		if msg == "" {
			msg = "test failed"
		}
		out = append(out, fmt.Sprintf("%s — %s", sym, msg))
	}

	buildPkgs := make(map[string]int)
	for _, be := range rr.BuildErrors {
		buildPkgs[be.Package]++
	}
	for pkg, count := range buildPkgs {
		out = append(out, fmt.Sprintf("%s — %d build errors", pkg, count))
	}

	lintPkgs := make(map[string]int)
	for _, li := range rr.LintIssues {
		pkg := li.Package
		if pkg == "" {
			pkg = derivePackageFromFile(li.File)
		}
		lintPkgs[pkg]++
	}
	for pkg, count := range lintPkgs {
		out = append(out, fmt.Sprintf("%s — %d lint issues", pkg, count))
	}

	scPkgs := make(map[string]int)
	for _, si := range rr.StaticIssues {
		scPkgs[si.Package]++
	}
	for pkg, count := range scPkgs {
		out = append(out, fmt.Sprintf("%s — %d staticcheck issues", pkg, count))
	}

	return out
}
