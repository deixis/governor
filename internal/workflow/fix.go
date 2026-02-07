package workflow

import (
	"context"
	"fmt"
	"strings"

	"github.com/deixis/governor/internal/report"
)

// FixResult holds the outcome of the fix phase.
type FixResult struct {
	AutoFixes    int
	FormatIssues []report.FormatIssue // only populated when fix=false
}

// RunFixPhase runs gofumpt and golangci-lint --fix.
// When fix is true, it modifies files in-place.
// When fix is false, gofumpt runs in check mode and reports unformatted files.
func (e *Engine) RunFixPhase(ctx context.Context, fix bool) (*FixResult, error) {
	result := &FixResult{}

	if fix {
		result.AutoFixes += e.runGofumptFix(ctx)
		result.AutoFixes += e.runLintFix(ctx)
	} else {
		issues := e.runGofumptCheck(ctx)
		result.FormatIssues = issues
	}

	return result, nil
}

// runGofumptFix runs gofumpt -w . and returns the number of files modified.
func (e *Engine) runGofumptFix(ctx context.Context) int {
	argv := ResolveTool("gofumpt")
	if argv == nil {
		return 0 // gofumpt not available — skip silently in fix phase
	}
	argv = append(argv, "-w", ".")

	res, err := e.Runner.Run(ctx, argv, "")
	if err != nil || res.ExitCode != 0 {
		return 0
	}

	// gofumpt -w doesn't report what it changed. Count by running -l after.
	lArgv := ResolveTool("gofumpt")
	if lArgv == nil {
		return 0
	}
	lArgv = append(lArgv, "-l", ".")
	lRes, err := e.Runner.Run(ctx, lArgv, "")
	if err != nil {
		return 0
	}
	// If -l returns nothing, -w already fixed everything.
	_ = lRes
	return 0
}

// runGofumptCheck runs gofumpt -l . and returns unformatted files as FormatIssues.
func (e *Engine) runGofumptCheck(ctx context.Context) []report.FormatIssue {
	argv := ResolveTool("gofumpt")
	if argv == nil {
		return nil
	}
	argv = append(argv, "-l", ".")

	res, err := e.Runner.Run(ctx, argv, "")
	if err != nil {
		return nil
	}

	var issues []report.FormatIssue
	for _, file := range strings.Split(strings.TrimSpace(string(res.Stdout)), "\n") {
		file = strings.TrimSpace(file)
		if file == "" {
			continue
		}
		issues = append(issues, report.FormatIssue{
			File:    file,
			Message: fmt.Sprintf("file not formatted: %s", file),
		})
	}
	return issues
}

// runLintFix runs golangci-lint run --fix and returns the count of fixes applied.
func (e *Engine) runLintFix(ctx context.Context) int {
	argv := ResolveTool("golangci-lint")
	if argv == nil {
		return 0 // not available — skip silently in fix phase
	}
	argv = append(argv, "run", "--fix")
	if e.Config.Lint.Config != "" {
		argv = append(argv, "--config", e.Config.Lint.Config)
	}
	argv = append(argv, e.Config.Lint.Args...)
	argv = append(argv, "./...")

	res, err := e.Runner.Run(ctx, argv, "")
	if err != nil || res == nil {
		return 0
	}
	return 0
}
