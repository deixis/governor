package mcp

import (
	"context"
	"fmt"
	"slices"
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

// symbolSectionNames lists every section gov_symbol accepts, in output order.
var symbolSectionNames = []string{"definition", "references", "callers", "callees", "implementations"}

// symbolSections lists the sections after definition, in output order,
// with the function that renders each. A section that does not apply to
// the symbol's kind (callers of a type, callees of an interface method)
// renders its error in place rather than failing the call.
var symbolSections = []struct {
	name   string
	render func(ctx context.Context, w *analysis.Workspace, file, symbol string) (string, error)
}{
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
			if !slices.Contains(symbolSectionNames, s) {
				return errorResult(fmt.Sprintf("unknown show value %q; use %s", s, strings.Join(symbolSectionNames, ", ")))
			}
			show[s] = true
		}
	}

	// Resolve once up front so an unknown symbol is one error, not one
	// per section; the result is the definition section when asked for.
	def, err := h.intel.DefinitionOf(ctx, p.File, p.Symbol)
	if err != nil {
		return errorResult(fmt.Sprintf("symbol failed: %v", err))
	}

	var out []string
	if show["definition"] {
		out = append(out, analysis.FormatDefinition(def))
	}
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

type planParams struct {
	Op        string         `json:"op" jsonschema:"One of add, remove, replace, move."`
	File      string         `json:"file,omitempty" jsonschema:"Absolute path of a file where symbol is visible (remove, replace, move). For add without target: the file to add to."`
	Symbol    string         `json:"symbol,omitempty" jsonschema:"Foo, T.Method, T.Field or pkg.Symbol (remove, replace, move)."`
	Source    string         `json:"source,omitempty" jsonschema:"Go declaration text, no package clause, imports allowed first (add, replace)."`
	Target    string         `json:"target,omitempty" jsonschema:"Package import path or absolute file path: where add puts the code, where move sends the symbol."`
	Decisions *planDecisions `json:"decisions,omitempty" jsonschema:"Answers to a previous plan's questions (move)."`
	Force     bool           `json:"force,omitempty" jsonschema:"Proceed past blockers and unanswered questions; what was forced past is listed as follow-ups to fix afterwards."`
}

type planDecisions struct {
	Export []string `json:"export,omitempty" jsonschema:"Unexported source-package symbols to export in place (move)."`
	Pull   []string `json:"pull,omitempty" jsonschema:"Shared unexported symbols to move along anyway (move)."`
}

func (p planParams) request() analysis.Request {
	req := analysis.Request{
		Op:     p.Op,
		File:   p.File,
		Symbol: p.Symbol,
		Source: p.Source,
		Target: p.Target,
		Force:  p.Force,
	}
	if p.Decisions != nil {
		req.Decisions = analysis.Decisions{Export: p.Decisions.Export, Pull: p.Decisions.Pull}
	}
	return req
}

func (h *handler) planHandler(ctx context.Context, _ *sdkmcp.CallToolRequest, p planParams) (*sdkmcp.CallToolResult, any, error) {
	if p.Op == "" {
		return errorResult("op is required: add, remove, replace or move")
	}
	report, err := h.intel.Plan(ctx, p.request())
	if err != nil {
		return errorResult(fmt.Sprintf("plan failed: %v", err))
	}
	return textResult(analysis.FormatReport(report))
}

// registerIntelTools registers the code intelligence tools.
func registerIntelTools(s *sdkmcp.Server, h *handler) {
	sdkmcp.AddTool(s, &sdkmcp.Tool{
		Name: "gov_plan",
		Description: `Impact of an intended change, without changing anything. Run before gov_apply.

Pass "op" and the fields it needs:
  add:     "source" (declarations) and "target" (package path, or file path; a new file is
           named after the first declaration). Reports name collisions and imports.
  remove:  "file" and "symbol". A type takes its methods along. Reports every use that
           breaks, by package; interface relations lost; declarations left unused.
  replace: "file", "symbol", "source" (one declaration, same name and kind). Reports
           signature or shape changes, callers to update, interface methods affected.
  move:    "file", "symbol", "target" (destination package or file). Reports the moved
           set, unexported helpers pulled along, shared helpers needing a decision
           (export or pull), import cycles, references rewritten per package.
Verdicts: OK with a one-line summary and, when needed, what you must do after apply;
NEEDS DECISION (answer through "decisions" and plan again); BLOCKED (each cause comes
with a fix). "force": true turns blockers and questions into follow-ups and lets apply
proceed. Use this instead of chaining gov_symbol lookups by hand, and never a regex, for
a refactor.`,
	}, h.planHandler)

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
