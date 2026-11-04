package mcp

import (
	"context"
	"fmt"

	"github.com/deixis/governor/internal/analysis"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Code intelligence tools, backed by the in-process analysis package.

type diagnosticsParams struct {
	Files []string `json:"files,omitempty" jsonschema:"Optional absolute paths; when set, only errors in these files are reported."`
}

func (h *handler) diagnosticsHandler(ctx context.Context, _ *sdkmcp.CallToolRequest, p diagnosticsParams) (*sdkmcp.CallToolResult, any, error) {
	diags, err := h.intel.Diagnostics(ctx, p.Files)
	if err != nil {
		return errorResult(fmt.Sprintf("diagnostics failed: %v", err))
	}
	return textResult(analysis.FormatDiagnostics(diags, h.engine.Config.MaxOutputBytes()))
}

type packageAPIParams struct {
	PackagePaths []string `json:"packagePaths" jsonschema:"Go import paths of the packages to summarise."`
}

func (h *handler) packageAPIHandler(ctx context.Context, _ *sdkmcp.CallToolRequest, p packageAPIParams) (*sdkmcp.CallToolResult, any, error) {
	if len(p.PackagePaths) == 0 {
		return errorResult("packagePaths is required")
	}
	out, err := h.intel.PackageAPI(ctx, p.PackagePaths)
	if err != nil {
		return errorResult(fmt.Sprintf("package api failed: %v", err))
	}
	return textResult(out)
}

type searchParams struct {
	Query string `json:"query" jsonschema:"Fuzzy search query for symbol names."`
}

func (h *handler) searchHandler(ctx context.Context, _ *sdkmcp.CallToolRequest, p searchParams) (*sdkmcp.CallToolResult, any, error) {
	syms, err := h.intel.Search(ctx, p.Query)
	if err != nil {
		return errorResult(fmt.Sprintf("search failed: %v", err))
	}
	return textResult(analysis.FormatSymbols(syms))
}

type fileContextParams struct {
	File string `json:"file" jsonschema:"Absolute path to the file."`
}

func (h *handler) fileContextHandler(ctx context.Context, _ *sdkmcp.CallToolRequest, p fileContextParams) (*sdkmcp.CallToolResult, any, error) {
	if p.File == "" {
		return errorResult("file is required")
	}
	fc, err := h.intel.FileContextFor(ctx, p.File)
	if err != nil {
		return errorResult(fmt.Sprintf("file context failed: %v", err))
	}
	return textResult(analysis.FormatFileContext(fc))
}

type symbolParams struct {
	File   string `json:"file" jsonschema:"Absolute path to a file in the package where the symbol is visible."`
	Symbol string `json:"symbol" jsonschema:"Symbol name: Foo, T.Method, T.Field, or pkg.Symbol (pkg is an import name in the file)."`
}

func (p symbolParams) validate() string {
	if p.File == "" || p.Symbol == "" {
		return "file and symbol are required"
	}
	return ""
}

func (h *handler) referencesHandler(ctx context.Context, _ *sdkmcp.CallToolRequest, p symbolParams) (*sdkmcp.CallToolResult, any, error) {
	if msg := p.validate(); msg != "" {
		return errorResult(msg)
	}
	refs, err := h.intel.References(ctx, p.File, p.Symbol)
	if err != nil {
		return errorResult(fmt.Sprintf("references failed: %v", err))
	}
	return textResult(analysis.FormatReferences(p.Symbol, refs))
}

func (h *handler) definitionHandler(ctx context.Context, _ *sdkmcp.CallToolRequest, p symbolParams) (*sdkmcp.CallToolResult, any, error) {
	if msg := p.validate(); msg != "" {
		return errorResult(msg)
	}
	def, err := h.intel.DefinitionOf(ctx, p.File, p.Symbol)
	if err != nil {
		return errorResult(fmt.Sprintf("definition failed: %v", err))
	}
	return textResult(analysis.FormatDefinition(def))
}

func (h *handler) implementationsHandler(ctx context.Context, _ *sdkmcp.CallToolRequest, p symbolParams) (*sdkmcp.CallToolResult, any, error) {
	if msg := p.validate(); msg != "" {
		return errorResult(msg)
	}
	impls, err := h.intel.Implementations(ctx, p.File, p.Symbol)
	if err != nil {
		return errorResult(fmt.Sprintf("implementations failed: %v", err))
	}
	return textResult(analysis.FormatImplementations(p.Symbol, impls))
}

func (h *handler) callersHandler(ctx context.Context, _ *sdkmcp.CallToolRequest, p symbolParams) (*sdkmcp.CallToolResult, any, error) {
	if msg := p.validate(); msg != "" {
		return errorResult(msg)
	}
	calls, err := h.intel.Callers(ctx, p.File, p.Symbol)
	if err != nil {
		return errorResult(fmt.Sprintf("callers failed: %v", err))
	}
	return textResult(analysis.FormatCallers(p.Symbol, calls))
}

func (h *handler) calleesHandler(ctx context.Context, _ *sdkmcp.CallToolRequest, p symbolParams) (*sdkmcp.CallToolResult, any, error) {
	if msg := p.validate(); msg != "" {
		return errorResult(msg)
	}
	calls, err := h.intel.Callees(ctx, p.File, p.Symbol)
	if err != nil {
		return errorResult(fmt.Sprintf("callees failed: %v", err))
	}
	return textResult(analysis.FormatCallees(p.Symbol, calls))
}

type outlineParams struct {
	Target string `json:"target" jsonschema:"Absolute path to a Go file, or a package import path."`
}

func (h *handler) outlineHandler(ctx context.Context, _ *sdkmcp.CallToolRequest, p outlineParams) (*sdkmcp.CallToolResult, any, error) {
	if p.Target == "" {
		return errorResult("target is required")
	}
	entries, err := h.intel.Outline(ctx, p.Target)
	if err != nil {
		return errorResult(fmt.Sprintf("outline failed: %v", err))
	}
	return textResult(analysis.FormatOutline(entries))
}

type fixParams struct {
	Files []string `json:"files" jsonschema:"Absolute paths of the Go files to fix."`
}

func (h *handler) fixHandler(ctx context.Context, _ *sdkmcp.CallToolRequest, p fixParams) (*sdkmcp.CallToolResult, any, error) {
	if len(p.Files) == 0 {
		return errorResult("files is required")
	}
	results, err := h.intel.Fix(ctx, p.Files)
	if err != nil {
		return errorResult(fmt.Sprintf("fix failed: %v", err))
	}
	return textResult(analysis.FormatFixResults(results))
}

// registerIntelTools registers the code intelligence tools.
func registerIntelTools(s *sdkmcp.Server, h *handler) {
	sdkmcp.AddTool(s, &sdkmcp.Tool{
		Name: "gov_diagnostics",
		Description: `Workspace-wide parse, build and type errors.

Optionally pass "files" (absolute paths) to restrict output to those files.
Type-checked in process; the first call after a source change reloads the workspace.`,
	}, h.diagnosticsHandler)

	sdkmcp.AddTool(s, &sdkmcp.Tool{
		Name: "gov_package_api",
		Description: `Exported API of one or more packages, in Go syntax.

Pass "packagePaths" (Go import paths): workspace packages, the standard library or any
go.mod dependency. Function bodies and unexported members are omitted. Use this instead
of go doc or reading module cache sources.`,
	}, h.packageAPIHandler)

	sdkmcp.AddTool(s, &sdkmcp.Tool{
		Name: "gov_search",
		Description: `Fuzzy symbol search across the workspace.

Pass "query". Matches package-level symbols, methods and struct fields.
Returns kind, qualified name and file location.`,
	}, h.searchHandler)

	sdkmcp.AddTool(s, &sdkmcp.Tool{
		Name: "gov_file_context",
		Description: `Cross-file dependencies of a file.

Pass "file" (absolute path). Returns its imports, the same-package symbols it uses
from sibling files, and the exported symbols it uses from each imported package.`,
	}, h.fileContextHandler)

	sdkmcp.AddTool(s, &sdkmcp.Tool{
		Name: "gov_symbol_references",
		Description: `Find all references to a symbol across the workspace.

Pass "file" (absolute path) and "symbol" (Foo, T.Method, T.Field, or pkg.Symbol).
The definition is listed first. A symbol from the standard library or a dependency is
also searched inside its own package, so this answers how a library uses its own types.`,
	}, h.referencesHandler)

	sdkmcp.AddTool(s, &sdkmcp.Tool{
		Name: "gov_definition",
		Description: `Where a symbol is declared, with its signature and doc comment.

Pass "file" (absolute path) and "symbol" (Foo, T.Method, T.Field, or pkg.Symbol).
pkg.Symbol reaches into the standard library and dependencies imported by the file.
Use this instead of grepping for a declaration or running go doc.`,
	}, h.definitionHandler)

	sdkmcp.AddTool(s, &sdkmcp.Tool{
		Name: "gov_implementations",
		Description: `Interface satisfaction across the workspace.

Pass "file" and "symbol". For an interface: the types that satisfy it. For a concrete
type: the workspace interfaces it satisfies. For a method: the matching methods on the
other side. A leading * marks types that satisfy only via pointer receiver. A symbol
from the standard library or a dependency is also matched inside its own package.`,
	}, h.implementationsHandler)

	sdkmcp.AddTool(s, &sdkmcp.Tool{
		Name: "gov_callers",
		Description: `Static call sites of a function or method, with the enclosing function of each.

Pass "file" and "symbol". Calls through an interface resolve to the interface method,
so query the interface method to see dynamic dispatch sites. A function from the
standard library or a dependency also lists its callers inside its own package.`,
	}, h.callersHandler)

	sdkmcp.AddTool(s, &sdkmcp.Tool{
		Name: "gov_callees",
		Description: `Every call made inside a function or method body, in source order.

Pass "file" and "symbol". Calls through function-typed values are prefixed "dynamic:".
Works for standard library and dependency functions too; use it to learn what a library
function does without reading its source.`,
	}, h.calleesHandler)

	sdkmcp.AddTool(s, &sdkmcp.Tool{
		Name: "gov_fix",
		Description: `Repair and modernise Go files in place, then report remaining errors.

Pass "files" (absolute paths). Applies, in order: modernize rewrites (safe suite),
missing/unused import repair, gofumpt. Returns what changed and the file's diagnostics.
Call after every edit instead of gov_diagnostics; it is a superset.`,
	}, h.fixHandler)

	sdkmcp.AddTool(s, &sdkmcp.Tool{
		Name: "gov_outline",
		Description: `Top-level declarations of a file or package with line ranges, no bodies.

Pass "target": an absolute .go path or a package import path, including the standard
library and go.mod dependencies. Read this before opening a large file to decide which
line range to read.`,
	}, h.outlineHandler)
}
