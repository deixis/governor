package mcp

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/deixis/governor"
	"github.com/deixis/governor/internal/workflow"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// goplsProxy manages a gopls MCP subprocess and forwards tool calls to it.
type goplsProxy struct {
	session *sdkmcp.ClientSession
	client  *sdkmcp.Client
}

// StartGoplsProxy starts a `gopls mcp` subprocess and connects to it
// as an MCP client. If gopls is not installed, it returns nil and logs
// a warning. The caller should call stop() when done.
//
// gopls is resolved using resolveTool, which checks "go tool gopls"
// first (Go 1.24+ tool directive), then falls back to PATH lookup.
func StartGoplsProxy(ctx context.Context, workspace string) (*goplsProxy, func(), error) {
	argv := workflow.ResolveTool("gopls")
	if argv == nil {
		return nil, func() {}, nil // gopls not installed — degrade gracefully
	}

	cmd := exec.Command(argv[0], append(argv[1:], "mcp")...)
	cmd.Dir = workspace

	transport := &sdkmcp.CommandTransport{Command: cmd}

	client := sdkmcp.NewClient(
		&sdkmcp.Implementation{Name: "governor", Version: governor.Version},
		nil,
	)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, func() {}, fmt.Errorf("connecting to gopls mcp: %w", err)
	}

	stop := func() {
		_ = session.Close()
	}

	return &goplsProxy{session: session, client: client}, stop, nil
}

// call forwards a tool call to gopls by name. Returns an error if the
// session is nil or the call fails.
func (p *goplsProxy) call(ctx context.Context, toolName string, args map[string]any) (*sdkmcp.CallToolResult, error) {
	return p.session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      toolName,
		Arguments: args,
	})
}

// callGoWorkspace calls gopls's go_workspace tool and returns the result text.
// Returns an empty string if the call fails.
func (p *goplsProxy) callGoWorkspace(ctx context.Context) string {
	result, err := p.call(ctx, "go_workspace", nil)
	if err != nil {
		return ""
	}
	return extractToolText(result)
}

// extractToolText extracts text content from a CallToolResult.
func extractToolText(r *sdkmcp.CallToolResult) string {
	if r == nil {
		return ""
	}
	for _, c := range r.Content {
		if tc, ok := c.(*sdkmcp.TextContent); ok {
			return tc.Text
		}
	}
	return ""
}
