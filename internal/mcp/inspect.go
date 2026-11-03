package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/deixis/governor/internal/report"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type inspectParams struct {
	RunID  string `json:"run_id" jsonschema:"the run ID from a gov_check or gov_audit result"`
	Symbol string `json:"symbol,omitempty" jsonschema:"Go-qualified symbol: import path for package scope (e.g. example.com/foo), or importpath.Symbol for a specific function (e.g. example.com/foo.TestAdd). Pass \"(no package)\" for diagnostics outside any package. Omit for every diagnostic in the run."`
}

func (h *handler) inspectHandler(ctx context.Context, req *mcp.CallToolRequest, params inspectParams) (*mcp.CallToolResult, any, error) {
	if params.RunID == "" {
		return errorResult("run_id is required")
	}

	result, err := h.store.Load(params.RunID)
	if err != nil {
		return errorResult(fmt.Sprintf("Failed to load run %s: %v", params.RunID, err))
	}

	var diagnostics []report.Diagnostic
	scope := params.Symbol
	if scope == "" {
		diagnostics = report.All(result)
		scope = "all"
	} else {
		diagnostics = report.BySymbol(result, params.Symbol)
	}
	if len(diagnostics) == 0 {
		return textResult(fmt.Sprintf("No diagnostics found for %s in run %s (%s).", scope, params.RunID, result.Kind))
	}

	return textResult(formatInspectOutput(params.RunID, result.Kind, scope, diagnostics))
}

func formatInspectOutput(runID string, kind report.Kind, symbol string, diagnostics []report.Diagnostic) string {
	var b strings.Builder

	// Run header.
	fmt.Fprintf(&b, "Run: %s (%s)\n", runID, kind)

	// Symbol header.
	if len(diagnostics) == 1 && diagnostics[0].Source == "test" {
		fmt.Fprintf(&b, "%s — FAIL\n", symbol)
	} else {
		// Group by source for the header.
		sources := make(map[string]int)
		for _, d := range diagnostics {
			sources[d.Source]++
		}
		var parts []string
		for source, count := range sources {
			parts = append(parts, fmt.Sprintf("%d %s", count, source))
		}
		fmt.Fprintf(&b, "%s — %s:\n", symbol, strings.Join(parts, ", "))
	}
	fmt.Fprintln(&b)

	// Group by file.
	type fileGroup struct {
		file        string
		diagnostics []report.Diagnostic
	}
	var groups []fileGroup
	seen := make(map[string]int)
	for _, d := range diagnostics {
		file := d.File
		if file == "" {
			file = "(unknown)"
		}
		if idx, ok := seen[file]; ok {
			groups[idx].diagnostics = append(groups[idx].diagnostics, d)
		} else {
			seen[file] = len(groups)
			groups = append(groups, fileGroup{file: file, diagnostics: []report.Diagnostic{d}})
		}
	}

	for _, g := range groups {
		for _, d := range g.diagnostics {
			if d.Line > 0 {
				if d.Col > 0 {
					fmt.Fprintf(&b, "%s:%d:%d: ", d.File, d.Line, d.Col)
				} else {
					fmt.Fprintf(&b, "%s:%d: ", d.File, d.Line)
				}
			} else if d.File != "" && d.File != "(unknown)" {
				fmt.Fprintf(&b, "%s: ", d.File)
			}

			// Source/detail tag.
			tag := d.Source
			if d.Detail != "" {
				tag = d.Source + "/" + d.Detail
			}
			fmt.Fprintf(&b, "[%s] %s\n", tag, d.Message)
		}
	}

	// For test failures, include full output.
	for _, d := range diagnostics {
		if d.Source == "test" && d.Output != "" {
			fmt.Fprintln(&b)
			fmt.Fprintln(&b, "Output:")
			for line := range strings.SplitSeq(strings.TrimRight(d.Output, "\n"), "\n") {
				fmt.Fprintf(&b, "    %s\n", line)
			}
		}
	}

	return b.String()
}
