package mcp

import (
	"context"
	"encoding/json"

	"github.com/deixis/governor/internal/workflow"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// goplsToolDef holds the static definition for a proxied gopls tool.
type goplsToolDef struct {
	// govName is the tool name as registered in Governor (gov_*).
	govName string
	// goplsName is the upstream gopls tool name (go_*).
	goplsName string
	// description is the tool description shown to agents.
	description string
	// inputSchema is the JSON Schema for the tool's input.
	inputSchema map[string]any
}

// objectSchema returns a minimal JSON Schema for an object with no required fields.
func objectSchema(props map[string]any) map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": props,
	}
}

// requiredObjectSchema returns a JSON Schema for an object with required fields.
func requiredObjectSchema(props map[string]any, required []string) map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": props,
		"required":   required,
	}
}

// goplsTools defines the static set of gopls tools exposed through Governor.
// Each tool is registered as gov_<name> and proxied to the corresponding
// go_<name> tool in gopls.
var goplsTools = []goplsToolDef{
	{
		govName:   "gov_diagnostics",
		goplsName: "go_diagnostics",
		description: `Workspace-wide diagnostics (parse errors, build errors, analysis).

Optionally pass "files" (absolute paths) for additional linting on active files.
Proxied to gopls. Requires gopls to be installed.`,
		inputSchema: objectSchema(map[string]any{
			"files": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Optional absolute paths to active files for additional analysis.",
			},
		}),
	},
	{
		govName:   "gov_package_api",
		goplsName: "go_package_api",
		description: `Public API summary of one or more packages in Go syntax.

Pass "packagePaths" (Go import paths) to inspect.
Proxied to gopls. Requires gopls to be installed.`,
		inputSchema: requiredObjectSchema(map[string]any{
			"packagePaths": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Go import paths of the packages to summarise.",
			},
		}, []string{"packagePaths"}),
	},
	{
		govName:   "gov_search",
		goplsName: "go_search",
		description: `Fuzzy symbol search across the workspace.

Pass "query" to search. Returns symbol name, kind, and file location.
Proxied to gopls. Requires gopls to be installed.`,
		inputSchema: requiredObjectSchema(map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Fuzzy search query for symbol names.",
			},
		}, []string{"query"}),
	},
	{
		govName:   "gov_file_context",
		goplsName: "go_file_context",
		description: `Cross-file dependencies for a given file.

Pass "file" (absolute path). Returns what the file uses from other files and imports.
Proxied to gopls. Requires gopls to be installed.`,
		inputSchema: requiredObjectSchema(map[string]any{
			"file": map[string]any{
				"type":        "string",
				"description": "Absolute path to the file.",
			},
		}, []string{"file"}),
	},
	{
		govName:   "gov_symbol_references",
		goplsName: "go_symbol_references",
		description: `Find all references to a symbol.

Pass "file" (absolute path) and "symbol" (e.g. Foo, T.Method, pkg.Symbol).
Proxied to gopls. Requires gopls to be installed.`,
		inputSchema: requiredObjectSchema(map[string]any{
			"file": map[string]any{
				"type":        "string",
				"description": "Absolute path to the file containing the symbol.",
			},
			"symbol": map[string]any{
				"type":        "string",
				"description": "Symbol name (e.g. Foo, T.Method, pkg.Symbol).",
			},
		}, []string{"file", "symbol"}),
	},
	{
		govName:   "gov_rename_symbol",
		goplsName: "go_rename_symbol",
		description: `Rename a symbol and return a unified diff.

Pass "file" (absolute path), "symbol", and "new_name".
Proxied to gopls. Requires gopls to be installed.`,
		inputSchema: requiredObjectSchema(map[string]any{
			"file": map[string]any{
				"type":        "string",
				"description": "Absolute path to the file containing the symbol.",
			},
			"symbol": map[string]any{
				"type":        "string",
				"description": "Symbol name (e.g. Foo, T.Method, pkg.Symbol).",
			},
			"new_name": map[string]any{
				"type":        "string",
				"description": "The new name for the symbol.",
			},
		}, []string{"file", "symbol", "new_name"}),
	},
}

// registerGoplsTools registers static gov_* tools that proxy to gopls.
// When gopls is unavailable, each tool returns an error with install instructions.
func registerGoplsTools(s *sdkmcp.Server, h *handler) {
	for _, def := range goplsTools {
		d := def // capture for closure
		s.AddTool(
			&sdkmcp.Tool{
				Name:        d.govName,
				Description: d.description,
				InputSchema: d.inputSchema,
			},
			makeGoplsHandler(h, d.goplsName),
		)
	}
}

// makeGoplsHandler returns a ToolHandler that proxies to gopls or returns
// an error with install instructions when gopls is not available.
func makeGoplsHandler(h *handler, goplsName string) sdkmcp.ToolHandler {
	return func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		if h.gopls == nil {
			unavail := workflow.NewErrToolUnavailable("gopls")
			return &sdkmcp.CallToolResult{
				Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: unavail.Error()}},
				IsError: true,
			}, nil
		}

		// Unmarshal the raw JSON arguments into map[string]any for the
		// upstream CallTool API.
		var args map[string]any
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				return &sdkmcp.CallToolResult{
					Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "invalid arguments: " + err.Error()}},
					IsError: true,
				}, nil
			}
		}

		return h.gopls.call(ctx, goplsName, args)
	}
}
