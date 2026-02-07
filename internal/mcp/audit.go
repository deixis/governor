package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/deixis/governor/internal/workflow"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type auditParams struct {
	Packages []string `json:"packages,omitempty" jsonschema:"Go import paths of packages to analyse (e.g. example.com/foo/bar/...) or absolute directory paths. Defaults to all packages in the workspace."`
}

func (h *handler) auditHandler(ctx context.Context, req *mcp.CallToolRequest, params auditParams) (*mcp.CallToolResult, any, error) {
	result, err := h.engine.Audit(ctx, params.Packages)
	if err != nil {
		return errorResult(fmt.Sprintf("audit failed: %v", err))
	}

	// Save results for gov_inspect.
	_ = h.store.Save(result.RunResult)

	return textResult(formatAudit(result.RunResult.ID, result.Steps))
}

func formatAudit(runID string, results []workflow.AuditStepResult) string {
	var b strings.Builder

	completed := 0
	for _, r := range results {
		if r.Status == "done" {
			completed++
		}
	}

	fmt.Fprintf(&b, "Audit: %d/%d checks completed\n", completed, len(results))
	fmt.Fprintf(&b, "Run: %s\n", runID)
	fmt.Fprintln(&b)

	for _, r := range results {
		switch r.Status {
		case "done":
			fmt.Fprintf(&b, "%s:\n", r.Name)
			fmt.Fprint(&b, r.Output)
			fmt.Fprintln(&b)
		case "unavailable":
			fmt.Fprintf(&b, "%s: unavailable (%s)\n\n", r.Name, r.Detail)
		case "error":
			fmt.Fprintf(&b, "%s: error (%s)\n\n", r.Name, r.Detail)
		case "skipped":
			fmt.Fprintf(&b, "%s: skipped\n\n", r.Name)
		}
	}

	fmt.Fprintf(&b, "Inspect with gov_inspect(run_id=%q, symbol=\"<package or package.Symbol>\").\n", runID)

	return b.String()
}
