package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/deixis/governor/internal/analysis"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Code intelligence tools, backed by the in-process analysis package.

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
	File   string   `json:"file" jsonschema:"Absolute path to a file in the package where the symbol is visible."`
	Symbol string   `json:"symbol" jsonschema:"Symbol name: Foo, T.Method, T.Field, or pkg.Symbol (pkg is an import name in the file)."`
	Show   []string `json:"show,omitempty" jsonschema:"Sections to return, any of: definition, references, callers, callees, implementations. Default: definition and references."`
}

// symbolSections lists the sections in output order with the function
// that renders each. A section that does not apply to the symbol's kind
// (callers of a type, callees of an interface method) renders its error
// in place rather than failing the call.
var symbolSections = []struct {
	name   string
	render func(ctx context.Context, w *analysis.Workspace, file, symbol string) (string, error)
}{
	{"definition", func(ctx context.Context, w *analysis.Workspace, file, symbol string) (string, error) {
		def, err := w.DefinitionOf(ctx, file, symbol)
		if err != nil {
			return "", err
		}
		return analysis.FormatDefinition(def), nil
	}},
	{"references", func(ctx context.Context, w *analysis.Workspace, file, symbol string) (string, error) {
		refs, err := w.References(ctx, file, symbol)
		if err != nil {
			return "", err
		}
		return analysis.FormatReferences(symbol, refs), nil
	}},
	{"callers", func(ctx context.Context, w *analysis.Workspace, file, symbol string) (string, error) {
		calls, err := w.Callers(ctx, file, symbol)
		if err != nil {
			return "", err
		}
		return analysis.FormatCallers(symbol, calls), nil
	}},
	{"callees", func(ctx context.Context, w *analysis.Workspace, file, symbol string) (string, error) {
		calls, err := w.Callees(ctx, file, symbol)
		if err != nil {
			return "", err
		}
		return analysis.FormatCallees(symbol, calls), nil
	}},
	{"implementations", func(ctx context.Context, w *analysis.Workspace, file, symbol string) (string, error) {
		impls, err := w.Implementations(ctx, file, symbol)
		if err != nil {
			return "", err
		}
		return analysis.FormatImplementations(symbol, impls), nil
	}},
}

func (h *handler) symbolHandler(ctx context.Context, _ *sdkmcp.CallToolRequest, p symbolParams) (*sdkmcp.CallToolResult, any, error) {
	if p.File == "" || p.Symbol == "" {
		return errorResult("file and symbol are required")
	}
	show := map[string]bool{"definition": true, "references": true}
	if len(p.Show) > 0 {
		show = make(map[string]bool, len(p.Show))
		for _, s := range p.Show {
			known := false
			for _, sec := range symbolSections {
				known = known || sec.name == s
			}
			if !known {
				return errorResult(fmt.Sprintf("unknown show value %q; use definition, references, callers, callees or implementations", s))
			}
			show[s] = true
		}
	}

	// Resolve once up front so an unknown symbol is one error, not one
	// per section.
	if _, err := h.intel.DefinitionOf(ctx, p.File, p.Symbol); err != nil {
		return errorResult(fmt.Sprintf("symbol failed: %v", err))
	}

	var out []string
	for _, sec := range symbolSections {
		if !show[sec.name] {
			continue
		}
		text, err := sec.render(ctx, h.intel, p.File, p.Symbol)
		if err != nil {
			text = fmt.Sprintf("%s: %v", sec.name, err)
		}
		out = append(out, text)
	}
	return textResult(strings.Join(out, "\n\n"))
}

type outlineParams struct {
	Target string `json:"target" jsonschema:"Absolute path to a Go file or directory, or a package import path."`
	API    bool   `json:"api,omitempty" jsonschema:"Render the exported API of the target's package in Go syntax with doc comments instead of the line-range outline."`
}

func (h *handler) outlineHandler(ctx context.Context, _ *sdkmcp.CallToolRequest, p outlineParams) (*sdkmcp.CallToolResult, any, error) {
	if p.Target == "" {
		return errorResult("target is required")
	}
	if p.API {
		out, err := h.intel.PackageAPI(ctx, []string{p.Target})
		if err != nil {
			return errorResult(fmt.Sprintf("outline failed: %v", err))
		}
		return textResult(out)
	}
	entries, err := h.intel.Outline(ctx, p.Target)
	if err != nil {
		return errorResult(fmt.Sprintf("outline failed: %v", err))
	}
	return textResult(analysis.FormatOutline(entries))
}

type fixParams struct {
	Files []string `json:"files,omitempty" jsonschema:"Absolute paths of the Go files to fix. Empty: report workspace diagnostics without changing anything."`
}

func (h *handler) fixHandler(ctx context.Context, _ *sdkmcp.CallToolRequest, p fixParams) (*sdkmcp.CallToolResult, any, error) {
	if len(p.Files) == 0 {
		diags, err := h.intel.Diagnostics(ctx, nil)
		if err != nil {
			return errorResult(fmt.Sprintf("diagnostics failed: %v", err))
		}
		return textResult(analysis.FormatDiagnostics(diags, h.engine.Config.MaxOutputBytes()))
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
		Name: "gov_symbol",
		Description: `Everything about one symbol: definition, references, callers, callees, implementations.

Pass "file" (absolute path) and "symbol" (Foo, T.Method, T.Field, or pkg.Symbol, where pkg
is an import name in the file; that reaches the standard library and dependencies). "show"
picks the sections; default is definition and references. Use it instead of grepping for
a declaration or its uses, and instead of go doc.
  definition:      declaration site, signature, doc comment.
  references:      every use across the workspace, definition first. For a library symbol,
                   uses inside the library too.
  callers:         static call sites with their enclosing function. Calls through an
                   interface resolve to the interface method; query that to see them.
  callees:         every call inside the body, in source order; "dynamic:" marks calls
                   through function values. Works on library functions.
  implementations: for an interface, the types satisfying it; for a type, the interfaces
                   it satisfies; for a method, the matching methods on the other side.
                   A leading * marks pointer-receiver-only satisfaction.
Before editing a symbol, ask for references, callers and implementations in one call.`,
	}, h.symbolHandler)

	sdkmcp.AddTool(s, &sdkmcp.Tool{
		Name: "gov_fix",
		Description: `Repair and modernise Go files in place, then report remaining errors.

Pass "files" (absolute paths). Applies, in order: modernize rewrites (safe suite),
missing/unused import repair, gofumpt. Returns what changed and the file's diagnostics.
Call after every edit. With no files it changes nothing and reports the parse, build
and type errors of the whole workspace.`,
	}, h.fixHandler)

	sdkmcp.AddTool(s, &sdkmcp.Tool{
		Name: "gov_outline",
		Description: `Top-level declarations of a file or package with line ranges, no bodies.

Pass "target": an absolute .go path or a package import path, including the standard
library and go.mod dependencies. A file or directory outside any loaded package (a
cloned repository, a downloaded file) is parsed on its own, syntax only. Read this
before opening a large file to decide which line range to read. With "api": true the
target's package is rendered as its exported API in Go syntax with doc comments, bodies
and unexported members omitted; use that instead of go doc.`,
	}, h.outlineHandler)
}
