// Package mcp provides the Governor MCP server, registering all tools
// and publishing model instructions.
package mcp

import (
	"context"
	_ "embed"
	"net/url"
	"time"

	"github.com/deixis/governor"
	"github.com/deixis/governor/internal/config"
	"github.com/deixis/governor/internal/report"
	"github.com/deixis/governor/internal/runner"
	"github.com/deixis/governor/internal/workflow"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

//go:embed instructions.md
var Instructions string

// handler holds shared dependencies for all tool handlers.
type handler struct {
	engine *workflow.Engine
	runner *runner.Runner // retained for workspace handler and updateWorkspaceFromRoots
	store  report.Store
	gopls  *goplsProxy // nil if gopls is not available
}

// NewServer creates an MCP server with all Governor tools registered.
// If a goplsProxy is provided, its tools are also registered.
func NewServer(cfg *config.Config, r *runner.Runner, store report.Store, workspace string, opts ...ServerOption) *mcp.Server {
	h := &handler{
		engine: &workflow.Engine{
			Config:    cfg,
			Runner:    r,
			Workspace: workspace,
			RepoRoot:  workspace, // MCP defaults to workspace; updated via roots
		},
		runner: r,
		store:  store,
	}

	var so serverOptions
	for _, o := range opts {
		o(&so)
	}
	h.gopls = so.gopls

	mcpOpts := &mcp.ServerOptions{
		Instructions: Instructions,
		Capabilities: &mcp.ServerCapabilities{
			Tools: &mcp.ToolCapabilities{ListChanged: false},
		},
		InitializedHandler: func(ctx context.Context, req *mcp.InitializedRequest) {
			h.updateWorkspaceFromRoots(ctx, req.Session)
		},
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "governor", Version: governor.Version}, mcpOpts)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "gov_workspace",
		Description: "Summarise the Go workspace: module path, Go version, and package list.",
	}, h.workspaceHandler)

	mcp.AddTool(s, &mcp.Tool{
		Name: "gov_check",
		Description: `Run the full check pipeline (auto-fix, test, lint, staticcheck) and stop on first failure.

Use this after making code changes. Runs gofumpt and golangci-lint --fix first (unless fix=false),
then tests, lint, and staticcheck in sequence. Results are stored for drill-down via gov_inspect.`,
	}, h.checkHandler)

	mcp.AddTool(s, &mcp.Tool{
		Name: "gov_audit",
		Description: `Run audit checks (coverage, complexity, deadcode, dupl, vulncheck) and return factual results.

Use this to assess code health and security. Runs all configured checks (does not stop on failure).
Results are stored for drill-down via gov_inspect. Returns raw facts without judgments.`,
	}, h.auditHandler)

	mcp.AddTool(s, &mcp.Tool{
		Name: "gov_inspect",
		Description: `Drill into results from a gov_check or gov_audit run.

Use the run_id and a Go-qualified symbol from the tool output.
Symbol can be an import path (e.g. example.com/foo) for all diagnostics in a package,
or importpath.Symbol (e.g. example.com/foo.TestAdd) for a specific function.`,
	}, h.inspectHandler)

	// Register static gopls proxy tools. Each tool returns an actionable
	// error when gopls is not installed, rather than silently disappearing.
	registerGoplsTools(s, h)

	return s
}

// ServerOption configures the Governor MCP server.
type ServerOption func(*serverOptions)

type serverOptions struct {
	gopls *goplsProxy
}

// WithGoplsProxy attaches a gopls proxy to the server.
func WithGoplsProxy(p *goplsProxy) ServerOption {
	return func(o *serverOptions) {
		o.gopls = p
	}
}

// updateWorkspaceFromRoots queries the client for MCP roots and updates the
// handler's engine, runner, and config if a valid root is returned.
// This is called during session initialization, before any tool calls.
func (h *handler) updateWorkspaceFromRoots(ctx context.Context, session *mcp.ServerSession) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	roots, err := session.ListRoots(ctx, &mcp.ListRootsParams{})
	if err != nil {
		return
	}
	if len(roots.Roots) == 0 {
		return
	}

	u, err := url.Parse(roots.Roots[0].URI)
	if err != nil || u.Scheme != "file" {
		return
	}
	workspace := u.Path

	loaded, err := config.Load(workspace)
	if err != nil {
		return
	}

	// Update runner.
	h.runner.Workspace = workspace
	h.runner.Timeout = loaded.Config.Timeout()
	h.runner.MaxOutput = loaded.Config.MaxOutputBytes()

	// Update engine.
	h.engine.Config = loaded.Config
	h.engine.Workspace = workspace
	h.engine.RepoRoot = loaded.RepoRoot
}

// textResult is a helper to build a text-only tool result.
func textResult(text string) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
	}, nil, nil
}

// errorResult is a helper to build an error tool result.
func errorResult(text string) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
		IsError: true,
	}, nil, nil
}
