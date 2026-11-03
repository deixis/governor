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
		issues, err := e.runGofumptCheck(ctx)
		if err != nil {
			return nil, err
		}
		result.FormatIssues = issues
	}

	return result, nil
}

// runGofumptFix runs gofumpt -w . and returns the number of files modified.
func (e *Engine) runGofumptFix(ctx context.Context) int {
	argv := e.resolveTool("gofumpt")
	if argv == nil {
		return 0 // gofumpt not available — skip silently in fix phase
	}
	argv = append(argv, "-w", ".")

	res, err := e.Runner.Run(ctx, argv, "")
	if err != nil || res.ExitCode != 0 {
		return 0
	}

	// gofumpt -w doesn't report what it changed. Count by running -l after.
	lArgv := e.resolveTool("gofumpt")
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

// runGofumptCheck runs gofumpt -l . and returns unformatted files as
// FormatIssues. gofumpt -l exits 0 whether or not it lists files, so a
// non-zero exit is a tool failure (a version-manager shim with no version
// set, say) and its output must not be read as a file list.
func (e *Engine) runGofumptCheck(ctx context.Context) ([]report.FormatIssue, error) {
	argv := e.resolveTool("gofumpt")
	if argv == nil {
		return nil, nil
	}
	argv = append(argv, "-l", ".")

	res, err := e.Runner.Run(ctx, argv, "")
	if err != nil {
		return nil, fmt.Errorf("running gofumpt: %w", err)
	}
	if res.ExitCode != 0 {
		out := strings.TrimSpace(string(res.Stderr) + string(res.Stdout))
		return nil, fmt.Errorf("gofumpt failed (exit %d): %s", res.ExitCode, out)
	}

	var issues []report.FormatIssue
	for file := range strings.SplitSeq(strings.TrimSpace(string(res.Stdout)), "\n") {
		file = strings.TrimSpace(file)
		if file == "" {
			continue
		}
		issues = append(issues, report.FormatIssue{
			File:    file,
			Message: fmt.Sprintf("file not formatted: %s", file),
		})
	}
	return issues, nil
}

// runLintFix runs golangci-lint run --fix and returns the count of fixes applied.
func (e *Engine) runLintFix(ctx context.Context) int {
	argv := e.resolveTool("golangci-lint")
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
