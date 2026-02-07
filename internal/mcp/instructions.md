# Governor MCP server

Governor provides deterministic, structured execution for Go projects: validation, auditing, and code intelligence.

## Detecting a Go workspace

At the start of every session, you MUST use the `gov_workspace` tool to learn about the Go workspace. The rest of these instructions apply whenever that tool indicates that the user is in a Go workspace.

## Go programming workflows

These guidelines MUST be followed whenever working in a Go workspace. There are two workflows described below: the 'Read Workflow' must be followed when the user asks a question about a Go workspace. The 'Write Workflow' must be followed when the user edits a Go workspace.

You may re-do parts of each workflow as necessary to recover from errors. However, you must not skip any steps.

### Read workflow

The goal of the read workflow is to understand the codebase.

1. **Understand the workspace layout**: Start by using `gov_workspace` to understand the overall structure of the workspace, such as whether it's a module, a workspace, or a GOPATH project.

2. **Find relevant symbols**: If you're looking for a specific type, function, or variable, use `gov_search`. This is a fuzzy search that will help you locate symbols even if you don't know the exact name or location.
   EXAMPLE: search for the 'Server' type: `gov_search({"query":"server"})`

3. **Understand a file and its intra-package dependencies**: When you have a file path and want to understand its contents and how it connects to other files *in the same package*, use `gov_file_context`. This tool will show you a summary of the declarations from other files in the same package that are used by the current file. `gov_file_context` MUST be used immediately after reading any Go file for the first time, and MAY be re-used if dependencies have changed.
   EXAMPLE: to understand `server.go`'s dependencies on other files in its package: `gov_file_context({"file":"/path/to/server.go"})`

4. **Understand a package's public API**: When you need to understand what a package provides to external code (i.e., its public API), use `gov_package_api`. This is especially useful for understanding third-party dependencies or other packages in the same monorepo.
   EXAMPLE: to see the API of the `storage` package: `gov_package_api({"packagePaths":["example.com/internal/storage"]})`

### Write workflow

The editing workflow is iterative. You should cycle through these steps until the task is complete.

1. **Read first**: Before making any edits, follow the Read Workflow to understand the user's request and the relevant code.

2. **Find references**: Before modifying the definition of any symbol, use the `gov_symbol_references` tool to find all references to that identifier. This is critical for understanding the impact of your change. Read the files containing references to evaluate if any further edits are required.
   EXAMPLE: `gov_symbol_references({"file":"/path/to/server.go","symbol":"Server.Run"})`

3. **Make edits**: Make the required edits, including edits to references you identified in the previous step. Don't proceed to the next step until all planned edits are complete.

4. **Check for errors**: After every code modification, you MUST call the `gov_diagnostics` tool. Pass the paths of the files you have edited. This tool will report any build or analysis errors.
   EXAMPLE: `gov_diagnostics({"files":["/path/to/server.go"]})`

5. **Fix errors**: If `gov_diagnostics` reports any errors, fix them. The tool may provide suggested quick fixes in the form of diffs. You should review these diffs and apply them if they are correct. Once you've applied a fix, re-run `gov_diagnostics` to confirm that the issue is resolved. It is OK to ignore 'hint' or 'info' diagnostics if they are not relevant to the current task. Note that Go diagnostic messages may contain a summary of the source code, which may not match its exact text.

6. **Check changes**: Once `gov_diagnostics` reports no errors (and ONLY once there are no errors), you MUST call `gov_check` to verify correctness. It runs auto-fix, test, lint, and staticcheck in order, stopping on first failure. Pass `fix=false` to skip auto-fix. Do NOT run tests on `./...` unless the user explicitly requests it. Scope to the packages you changed.
   EXAMPLE: `gov_check({"packages": ["./pkg/foo/..."]})`

7. **Audit code quality**: Before considering a code modification done, you MUST call `gov_audit` to evaluate the code quality and identify any existing security risks. If your edits involved adding or updating dependencies in `go.mod`, this step also ensures that new dependencies do not introduce vulnerabilities.
   EXAMPLE: `gov_audit({"packages": ["./pkg/foo/..."]})`

8. **Inspect diagnostics**: Use `gov_inspect` to drill into a `gov_check` or `gov_audit` run. Do NOT re-run the command just to see more output.
   - `symbol` as an import path (e.g. `example.com/foo`) → all diagnostics for that package.
   - `symbol` as `importpath.Symbol` (e.g. `example.com/foo.TestAdd`) → diagnostics for that function.

## Rules

- Prefer `gov_check` over calling individual tools.
- Use `gov_inspect` instead of re-running commands.
- Do NOT ignore test or lint failures unless the user explicitly instructs you to.
- Missing external tools are reported as `unavailable` with install instructions.
