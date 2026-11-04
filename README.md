# Governor

Governor is a **tool designed to facilitate Go project development for both human users and AI agents**.

It offers a unified, structured entry point for validating, analysing, and auditing Go code. Governor can be used as a standalone command-line interface, similar to go test, or as an **MCP server to support AI-assisted development**.

Governor executes tests, linting, static analysis, and code health checks in a predetermined, declarative sequence. It returns typed, machine-readable results rather than unstructured logs.

This approach enables AI agents to write and modify Go code with demonstrable correctness, eliminating the need to create custom workflows, omit validation steps, or misinterpret tool outputs.

## Why Governor

AI agents can write Go code.
However, these agents often encounter challenges in ensuring code correctness.

**The primary challenge lies not in model capability, but in the lack of structured execution**.

When agents depend on ad-hoc shell commands, several issues commonly arise:

* infer success from logs instead of facts
* skip or inconsistently re-run validation steps
* lose the link between a change and its effects
* pollute the context window with verbose, low-signal output

Consequently, the resulting code may appear correct but lacks reliable testing, analysis, and auditability.

Governor addresses these issues by serving as a semantic execution boundary between code generation and code validation.

### What Governor provides

**Deterministic validation**
Each execution follows a consistent, ordered pipeline, ensuring that every step is either completed or omitted in a transparent manner.

**Structured results**
Governor provides typed, bounded outcomes such as pass, fail, or unavailable, eliminating the need for log parsing.

**Enforced workflow completeness**
Tests, lint, static analysis, and audits are first-class steps, not optional commands.

**Unified execution and code intelligence**
Execution and type-checker-backed code intelligence are exposed through one interface.

**Improved agent efficiency**
High-level semantic tools minimize redundant tool invocations, re-execution, and context-window clutter. This enables agents to retain more relevant information and achieve correct code more efficiently.*

* Empirical benchmarks for token usage, wall-clock time, and convergence rate have not yet been collected.

## Installation

Install Governor globally, like `gopls`:

```bash
go install github.com/deixis/governor/cmd/governor@latest
```

Requires Go 1.26 or later. Code intelligence runs in process on `go/packages` and `go/types`; no gopls install is needed.

### Toolchain matching

Type-checking in process ties governor to the Go version that built it. A workspace whose `go.mod` selects a newer toolchain would fail to load, so governor checks before each run from the workspace directory and hands over to a matching build when needed:

1. The module declares governor as a tool (`go get -tool github.com/deixis/governor/cmd/governor`): `go tool governor` runs, with the version and toolchain pinned by the workspace.
2. The workspace toolchain is not newer than the one that built the binary: runs in process.
3. Otherwise governor installs its own version under the workspace toolchain into the user cache (`~/Library/Caches/governor/<go version>/<governor version>/` on macOS, `~/.cache/governor/...` on Linux) and runs that. Only the first run per toolchain builds.

Source builds without a module version skip step 3. Set `GOVERNOR_NO_REEXEC=1` to disable the handover. Editor and agent configurations keep calling `governor` on `PATH`; nothing per project is required.

## CLI usage

Governor provides the following commands:

```
governor check [flags] [packages...]
governor audit [flags] [packages...]
governor fix   [flags] [files...]
governor hook  bash [command...]
governor mcp   [flags]
governor version
```

Package arguments follow `go test` conventions (`./...`, subtrees, or import paths). When omitted, defaults to `./...`.

### governor check

Run the correctness pipeline: test, lint, staticcheck. Stops on first failure.

```bash
governor check ./...
governor check -fix ./pkg/api/...
governor check -json ./...
```

| Flag | Default | Description |
|---|---|---|
| `-fix` | off | Run gofumpt and golangci-lint --fix before checks |
| `-json` | off | Output the full RunResult as JSON |
| `-v` | off | Show detailed output on failure |
| `-timeout` | config | Override per-step timeout |

### governor audit

Run code health and security checks. Does not stop on failure.

```bash
governor audit ./...
governor audit -json ./...
```

| Flag | Default | Description |
|---|---|---|
| `-json` | off | Output the full RunResult as JSON |
| `-v` | off | Verbose output |
| `-timeout` | config | Override per-step timeout |

### governor hook

Agent harness hook for shell commands. `governor hook bash` reads a Claude Code PreToolUse event on stdin and prints the decision as JSON:

| Command | Becomes |
|---|---|
| `go test [flags] pkgs`, `go vet pkgs`, `staticcheck pkgs`, `go tool staticcheck pkgs`, `golangci-lint run pkgs` | `governor check pkgs` (`--fix` keeps `-fix`, `-timeout` and `-v` pass through) |
| `goimports -w`, `gofmt -w`, `gofumpt -w` on Go files | `governor fix files` |
| `sed -i`, `perl -i`, `sd`, `awk -i inplace` on a Go file | refused, with a pointer to the editor tool and `gov_diagnostics` |

Targeted test runs (`-run`, `-bench`, `-fuzz`, profiles, `-json`) and read-only formatter modes (`-l`, `-d`) run as is. Every command in a pipeline, `&&` chain, loop or substitution is inspected. Pass a command as arguments to see what would happen:

```bash
governor hook bash go test -race ./internal/...
```

### governor mcp

Start the MCP server for AI agents:

```bash
governor mcp
governor mcp -http :9090
governor mcp -instructions
```

## MCP setup (Cursor)

Add Governor once to your global MCP config (`~/.cursor/mcp.json`):

```json
{
  "mcpServers": {
    "governor": {
      "command": "governor",
      "args": ["mcp"]
    }
  }
}
```

The working directory is set to the workspace root automatically.

## MCP tools

### Native tools

| Tool | Description |
|---|---|
| `gov_check` | Run the full correctness pipeline |
| `gov_audit` | Run audit checks without stopping on failure |
| `gov_inspect` | Inspect results from a previous run |
| `gov_workspace` | Summarise the Go workspace |

### Code intelligence

Backed by an in-process, cached `go/packages` load of the workspace. The first call after a source change reloads; later calls hit the cache. `gov_outline`, `gov_package_api` and `gov_definition` also reach the standard library and go.mod dependencies: a package outside the workspace is loaded with source on first use and cached with the snapshot.

| Tool | Description |
|---|---|
| `gov_diagnostics` | Parse, build and type errors |
| `gov_package_api` | Exported API of packages in Go syntax, workspace or dependency |
| `gov_search` | Fuzzy symbol search |
| `gov_file_context` | Cross-file and cross-package dependencies of a file |
| `gov_symbol_references` | Find symbol references |
| `gov_definition` | Declaration site, signature and doc of a symbol |
| `gov_implementations` | Interface satisfaction, both directions, type or method level |
| `gov_callers` | Static call sites of a function with their enclosing function |
| `gov_callees` | Calls made inside a function body |
| `gov_outline` | Top-level declarations of a file or package with line ranges, workspace or dependency |
| `gov_fix` | Repair imports, gofumpt, modernize rewrites, then diagnostics for the files |

### Fix on every edit

`governor fix <files>` runs the same pipeline as `gov_fix` from the shell. Agents tend to keep using their own edit tools, so the reliable way to run it after every edit is a hook in the editor or agent harness rather than an instruction the agent may skip. Pass the edited path as an argument, or pipe paths one per line with `-stdin`. Non-Go files are ignored, and `-q` prints nothing when a file needed no change and has no diagnostics, so the hook is silent unless something happened.

```bash
governor fix -q path/to/edited.go
echo path/to/edited.go | governor fix -q -stdin
```

### Claude Code plugin

`plugins/claude-code` packages the integration for Claude Code: it registers the MCP server, ships a skill describing when to use each `gov_*` tool, adds a `PostToolUse` hook that runs `governor fix -q` on every edited `.go` file, and a `PreToolUse` hook on Bash that runs `governor hook bash`: `go test`, `go vet`, `golangci-lint`, `staticcheck` and `goimports -w` are rewritten to `governor check` or `governor fix` with a note to the agent, and `sed`, `perl`, `sd` or `awk` editing a Go file in place are refused. Install the binary first, then the plugin:

```bash
go install github.com/deixis/governor/cmd/governor@latest
```

```
/plugin install governor --marketplace deixis/governor
```

For local development of the plugin, start Claude Code with `claude --plugin-dir ./plugins/claude-code`.

## Configuration

Governor can read optional configuration settings from a `.governor`, `.governor.yaml` or `.governor.yml` file located at the repository root. Keep one; several is an error.

```yaml
version: 1
timeout: 5m
max_output: 1048576

test:
  args: ["-race", "-count=1"]
  # Package patterns left out of this step. Every step (test, lint,
  # staticcheck, and each audit check) takes its own exclude list.
  exclude: ["./e2e/scenario/..."]

lint:
  config: .golangci.yml

staticcheck:
  checks: ["all", "-ST1000"]

check:
  steps: ["test", "lint", "staticcheck"]

audit:
  steps: ["coverage", "complexity", "deadcode", "dupl", "vulncheck"]
```

Governor is **not** a CI system, task runner, or shell wrapper.

It is an **execution governor**: code generation remains flexible, but **correctness, structure, and auditability are enforced**.
