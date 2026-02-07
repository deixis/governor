package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type workspaceParams struct{}

func (h *handler) workspaceHandler(ctx context.Context, req *sdkmcp.CallToolRequest, _ workspaceParams) (*sdkmcp.CallToolResult, any, error) {
	var b strings.Builder

	// If gopls is available, merge its go_workspace output first.
	// This provides richer information (view type, diagnostics status, etc.).
	if h.gopls != nil {
		goplsInfo := h.gopls.callGoWorkspace(ctx)
		if goplsInfo != "" {
			fmt.Fprintln(&b, goplsInfo)
			fmt.Fprintln(&b)
			fmt.Fprintln(&b, "--- Governor ---")
			fmt.Fprintln(&b)
		}
	}

	// Module info via `go list -m -json`.
	modResult, err := h.engine.Runner.Run(ctx, []string{"go", "list", "-m", "-json"}, "")
	if err != nil {
		return errorResult(fmt.Sprintf("Failed to query module info: %v", err))
	}
	if modResult.ExitCode != 0 {
		return errorResult(fmt.Sprintf("go list -m -json failed:\n%s", string(modResult.Stderr)))
	}

	var mod moduleInfo
	if err := json.Unmarshal(modResult.Stdout, &mod); err != nil {
		return errorResult(fmt.Sprintf("Failed to parse module info: %v", err))
	}

	fmt.Fprintf(&b, "Module: %s\n", mod.Path)
	if mod.GoVersion != "" {
		fmt.Fprintf(&b, "Go: %s\n", mod.GoVersion)
	}
	fmt.Fprintf(&b, "Directory: %s\n", mod.Dir)
	fmt.Fprintln(&b)

	// Package list via `go list ./...`.
	pkgResult, err := h.engine.Runner.Run(ctx, []string{"go", "list", "./..."}, "")
	if err != nil {
		// Non-fatal: we still have module info.
		fmt.Fprintln(&b, "Packages: (failed to list)")
	} else if pkgResult.ExitCode != 0 {
		fmt.Fprintln(&b, "Packages: (failed to list)")
	} else {
		pkgs := strings.Split(strings.TrimSpace(string(pkgResult.Stdout)), "\n")
		fmt.Fprintf(&b, "Packages (%d):\n", len(pkgs))
		for _, pkg := range pkgs {
			if pkg != "" {
				fmt.Fprintf(&b, "  %s\n", pkg)
			}
		}
	}

	return textResult(b.String())
}

// moduleInfo holds the relevant fields from `go list -m -json`.
type moduleInfo struct {
	Path      string `json:"Path"`
	Dir       string `json:"Dir"`
	GoVersion string `json:"GoVersion"`
}
