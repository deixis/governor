---
name: governor
description: Use when working in a Go module with the Governor MCP server connected. Covers which gov_* tool answers which question, the mandatory gov_fix-after-edit loop (also enforced by the plugin hook), and gov_check before finishing.
---

# Governor for Go

Governor exposes a type-checked view of the workspace as `gov_*` MCP tools. They answer questions text search cannot. Reach for them before grep, find or reading whole files.

## Navigation

| Question | Tool |
|---|---|
| Where is something named like X? | `gov_search({"query":"X"})` |
| What does this file or package contain? | `gov_outline({"target": path-or-import-path})`, then read line ranges |
| What is this identifier, and where is it used? | `gov_symbol({"file", "symbol"})`: definition and references by default |
| Which types satisfy this interface? Which interfaces does this type satisfy? | `gov_symbol` with `"show":["implementations"]` |
| Who calls this? What does it call? | `gov_symbol` with `"show":["callers","callees"]` |
| Blast radius before a rename or signature change | `gov_symbol` with `"show":["references","callers","implementations"]`, one call |
| What does this file depend on? | `gov_file_context` |
| What does this package export? | `gov_outline` with `"api":true` |
| What does a dependency or stdlib package look like? | `gov_outline` with its import path, `"api":true` for the exported API; `gov_symbol` with `pkg.Symbol` |
| Where is a library symbol used, before swapping the library? | `gov_symbol` with `pkg.Symbol` and `"show":["references"]`, from a file importing it |
| How does a library use its own type, or what does a library function call? | `gov_symbol` with `pkg.Symbol`; references, callers, callees and implementations look inside the library's package too |
| What is in a file or directory of another repository? | `git clone --depth 1` it, then `gov_outline` on the file or directory. Syntax only, no `gh api` per file, no `rg '^func'` |

Symbol forms: `Foo`, `T.Method`, `T.Field`, `pkg.Symbol`, `pkg.T.Method`. `pkg` is an import name in the given file.

The tools resolve any import path the module can, so the standard library and every go.mod dependency are in reach. Do not run `go doc`, find a module directory with `go list -m -f '{{.Dir}}'` to grep it, or read module cache files by hand.

## Editing

1. Measure blast radius first: `gov_symbol` with `references`, plus `callers` for signature changes and `implementations` for interfaces, in one call.
2. Edit with the Edit or Write tool, or `sg` (ast-grep) for a repeated pattern. Never `sed`, `perl`, `sd` or `awk` on a Go file; the plugin hook refuses them. To migrate a type or signature, change the definition, run `gov_fix` on its file, then `gov_fix` with no files: it lists every site that must change.
3. The plugin hook runs `governor fix` on every edited `.go` file and shows the result. It repairs imports, formats with gofumpt, applies safe modernize rewrites, and lists remaining type errors. Because it rewrites the file, re-read before editing again. Without the hook, call `gov_fix({"files":[...]})` yourself.
4. Fix any reported diagnostics, then `gov_check({"packages":["./changed/pkg/..."]})` before declaring the work done. Use `gov_inspect` to drill into failures instead of re-running.
5. Do not run `go test`, `go vet`, `golangci-lint`, `staticcheck` or `goimports` in the shell. The plugin hook rewrites them to `governor check` or `governor fix` and tells you so; say so to the user when it happens. Test flags such as `-race` belong in `.governor.yaml` under `test.args`. A targeted run (`go test -run TestX ./pkg`) is allowed as is.

## Setup

Governor must be on PATH: `go install github.com/deixis/governor/cmd/governor@latest`. The MCP server is registered by the plugin; if `gov_workspace` is unavailable, check that the binary resolves from the project directory.
