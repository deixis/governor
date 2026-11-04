# Governor MCP server

Governor provides deterministic, structured execution for Go projects: code intelligence, validation, and auditing. Its intelligence tools run on a type-checked snapshot of the workspace, so they answer questions that text search cannot: what a symbol is, who calls it, which types satisfy an interface. Prefer them over grep and over reading whole files.

## Detecting a Go workspace

At the start of every session, you MUST use the `gov_workspace` tool to learn about the Go workspace. The rest of these instructions apply whenever that tool indicates that the user is in a Go workspace.

## Go programming workflows

These guidelines MUST be followed whenever working in a Go workspace. The 'Read Workflow' applies when the user asks a question about a Go workspace. The 'Write Workflow' applies when the user edits a Go workspace.

You may re-do parts of each workflow as necessary to recover from errors. However, you must not skip any steps.

### Read workflow

The goal of the read workflow is to understand the codebase with the fewest file reads.

1. **Understand the workspace layout**: Start with `gov_workspace` for the module path, Go version and package list.

2. **Find relevant symbols**: When looking for a type, function, variable, method or field by name, use `gov_search`. It is fuzzy and case-insensitive, and returns kind, qualified name and location. Do NOT grep for declarations.
   EXAMPLE: `gov_search({"query":"server"})`

3. **Survey before reading**: Before reading any Go file, call `gov_outline` on the file or its package. It lists every top-level declaration with line ranges and signatures, no bodies. Then read only the line ranges you need. For a file over 200 lines this is mandatory.
   EXAMPLE: `gov_outline({"target":"/path/to/server.go"})` or `gov_outline({"target":"example.com/internal/storage"})`

4. **Resolve a symbol**: When you meet an identifier and need to know what it is, use `gov_definition`. It returns the declaration location, the signature and the doc comment. Do NOT search for the declaration or open its file to find out.
   EXAMPLE: `gov_definition({"file":"/path/to/server.go","symbol":"Store.Put"})`
   Symbol forms accepted everywhere: `Foo`, `T.Method`, `T.Field`, `pkg.Symbol`, `pkg.T.Method`, where `pkg` is an import name in the given file.

5. **Follow an interface**: For any interface, use `gov_implementations` before assuming which concrete type is in play. For a concrete type, it lists the workspace interfaces the type satisfies. For a method, it lists the matching methods on the other side.
   EXAMPLE: `gov_implementations({"file":"/path/to/store.go","symbol":"Store"})`

6. **Follow the call graph**: To learn how a function is used, use `gov_callers`; each call site is reported with its enclosing function. To learn what a function does without reading its body, use `gov_callees`. Calls through an interface resolve to the interface method, so query that method to see dynamic dispatch sites.
   EXAMPLE: `gov_callers({"file":"/path/to/server.go","symbol":"Server.Start"})`

7. **Understand a file's dependencies**: After reading a Go file for the first time, call `gov_file_context`. It lists the same-package symbols the file uses from sibling files and the exported symbols it uses from each import.
   EXAMPLE: `gov_file_context({"file":"/path/to/server.go"})`

8. **Understand a package's public API**: To see what a package offers to external code, use `gov_package_api`. It prints the exported declarations in Go syntax with doc comments and without bodies.
   EXAMPLE: `gov_package_api({"packagePaths":["example.com/internal/storage"]})`

9. **Look into dependencies and the standard library the same way**: `gov_outline`, `gov_package_api` and `gov_definition` accept any import path the module resolves, and `pkg.Symbol` resolves through the file's imports. Do NOT run `go doc`, locate a module directory with `go list -m -f '{{.Dir}}'` to grep or read it, or open files under the module cache by hand.
   EXAMPLE: `gov_package_api({"packagePaths":["github.com/jackc/pgx/v5/pgtype"]})`, `gov_definition({"file":"/path/to/store.go","symbol":"pgtype.UUID.Scan"})`

10. **Find every use of a dependency**: Before replacing or upgrading a library, use `gov_symbol_references` with `pkg.Symbol` for each symbol in use, from a file that imports it. It is exact where grep over `pkg\.\w+` is not, and reports the enclosing package of each use.
   EXAMPLE: `gov_symbol_references({"file":"/path/to/trail.go","symbol":"uuid.NewSHA1"})`

### Write workflow

The editing workflow is iterative. Cycle through these steps until the task is complete.

1. **Read first**: Follow the Read Workflow to understand the request and the relevant code.

2. **Measure the blast radius**: Before modifying the definition of any symbol, use `gov_symbol_references` to find every reference. Before changing a function's signature or behaviour, also use `gov_callers`. Before changing an interface, or a method that satisfies one, use `gov_implementations` to find the other side. Read the files containing references to evaluate whether further edits are required.
   EXAMPLE: `gov_symbol_references({"file":"/path/to/server.go","symbol":"Server.Run"})`

3. **Make edits**: Make the required edits, including edits to the references and implementations you identified. Don't proceed until all planned edits are complete.

4. **Fix and check each edited file**: After every code modification, you MUST call `gov_fix` with the paths of the files you edited. It repairs missing and unused imports, formats with gofumpt, applies safe modernisation rewrites, and then reports the remaining build and type errors for those files. It changes the files on disk, so re-read any file it reports as changed before editing it again. Use `gov_fix` instead of `gov_diagnostics`; it is a superset.
   EXAMPLE: `gov_fix({"files":["/path/to/server.go"]})`

5. **Fix errors**: If `gov_fix` reports diagnostics, fix them and call `gov_fix` again. Note that Go diagnostic messages may contain a summary of the source code, which may not match its exact text. `gov_diagnostics` with no arguments reports errors for the whole workspace when you need to confirm nothing else broke.

6. **Check changes**: Once `gov_fix` reports no diagnostics (and ONLY then), you MUST call `gov_check` to verify correctness. It runs auto-fix, test, lint, and staticcheck in order, stopping on first failure. Pass `fix=false` to skip auto-fix. Do NOT run tests on `./...` unless the user explicitly requests it. Scope to the packages you changed.
   EXAMPLE: `gov_check({"packages": ["./pkg/foo/..."]})`

7. **Audit code quality**: Before considering a code modification done, you MUST call `gov_audit` to evaluate code quality and identify security risks. If your edits added or updated dependencies in `go.mod`, this step also checks that new dependencies do not introduce vulnerabilities.
   EXAMPLE: `gov_audit({"packages": ["./pkg/foo/..."]})`

8. **Inspect diagnostics**: Use `gov_inspect` to drill into a `gov_check` or `gov_audit` run. Do NOT re-run the command just to see more output.
   - `symbol` as an import path (e.g. `example.com/foo`) → all diagnostics for that package.
   - `symbol` as `importpath.Symbol` (e.g. `example.com/foo.TestAdd`) → diagnostics for that function.

## Rules

- Prefer Governor's intelligence tools over grep, find and reading whole files. They see types; text search does not.
- `gov_outline` before reading a file. `gov_definition` before opening a declaration. `gov_implementations` before assuming a concrete type.
- Dependencies and the standard library go through the same tools. No `go doc`, no grepping the module cache.
- `gov_fix` after every edit. `gov_check` before declaring work done.
- Use `gov_inspect` instead of re-running commands.
- Do NOT ignore test or lint failures unless the user explicitly instructs you to.
- Missing external tools are reported as `unavailable` with install instructions.
