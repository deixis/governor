package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/deixis/governor/internal/report"
	"github.com/deixis/governor/internal/workflow"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type checkParams struct {
	Packages []string `json:"packages,omitempty" jsonschema:"Go import paths of packages to check (e.g. example.com/foo/bar/...) or absolute directory paths. Defaults to all packages in the workspace."`
	Fix      *bool    `json:"fix,omitempty" jsonschema:"Run auto-fix phase (gofumpt, golangci-lint --fix) before checks. Default: true."`
}

func (h *handler) checkHandler(ctx context.Context, req *mcp.CallToolRequest, params checkParams) (*mcp.CallToolResult, any, error) {
	// Default fix=true when nil (MCP default).
	fix := true
	if params.Fix != nil {
		fix = *params.Fix
	}

	result, err := h.engine.Check(ctx, params.Packages, fix)
	if err != nil {
		return errorResult(fmt.Sprintf("check failed: %v", err))
	}

	// Save results for gov_inspect.
	_ = h.store.Save(result.RunResult)

	// Format failure before steps ran (format issues with fix=false).
	if result.FailedIdx == -2 {
		return textResult(formatCheckWithFormatFailure(result.RunResult))
	}

	return textResult(formatCheck(result.RunResult.ID, result.RunResult, result.Steps, result.FailedIdx))
}

func formatCheck(runID string, rr *report.RunResult, results []workflow.StepResult, failedIdx int) string {
	var b strings.Builder

	allPassed := failedIdx < 0
	if allPassed {
		fmt.Fprintln(&b, "Status: PASS")
	} else {
		fmt.Fprintln(&b, "Status: FAIL")
	}
	fmt.Fprintf(&b, "Run: %s\n", runID)
	fmt.Fprintln(&b)

	if rr.AutoFixes > 0 {
		fmt.Fprintf(&b, "Auto-fixed: %d issues\n", rr.AutoFixes)
		fmt.Fprintln(&b)
	}

	fmt.Fprintln(&b, "Steps:")
	for _, r := range results {
		if r.Status == "unavailable" {
			fmt.Fprintf(&b, "  %s: unavailable (%s)\n", r.Name, r.Detail)
		} else {
			fmt.Fprintf(&b, "  %s: %s\n", r.Name, r.Status)
		}
	}
	fmt.Fprintln(&b)

	if !allPassed {
		failed := results[failedIdx]

		failures := workflow.FormatFailureSymbols(rr)
		if len(failures) > 0 {
			fmt.Fprintln(&b, "Failures:")
			for _, f := range failures {
				fmt.Fprintf(&b, "  %s\n", f)
			}
			fmt.Fprintln(&b)
		} else if failed.Output != "" {
			fmt.Fprintf(&b, "Failed step: %s\n", failed.Name)
			fmt.Fprintln(&b)
			fmt.Fprintln(&b, failed.Output)
			fmt.Fprintln(&b)
		}

		if failed.Status == "unavailable" {
			fmt.Fprintf(&b, "Action: %s is required but not installed. Install it and re-run gov_check.\n", failed.Name)
		} else {
			fmt.Fprintf(&b, "Inspect with gov_inspect(run_id=%q, symbol=\"<package or package.Symbol>\").\n", runID)
		}
	} else {
		fmt.Fprintln(&b, "All check steps passed.")
	}

	return b.String()
}

func formatCheckWithFormatFailure(rr *report.RunResult) string {
	var b strings.Builder

	fmt.Fprintln(&b, "Status: FAIL")
	fmt.Fprintf(&b, "Run: %s\n", rr.ID)
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "Formatting issues (%d files):\n", len(rr.FormatIssues))
	for _, f := range rr.FormatIssues {
		fmt.Fprintf(&b, "  %s\n", f.File)
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "Action: run gofumpt to format these files, or re-run gov_check with fix=true.")

	return b.String()
}
