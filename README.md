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
Execution and gopls-powered code intelligence are exposed through one interface.

**Improved agent efficiency**
High-level semantic tools minimize redundant tool invocations, re-execution, and context-window clutter. This enables agents to retain more relevant information and achieve correct code more efficiently.*

* Empirical benchmarks for token usage, wall-clock time, and convergence rate have not yet been collected.

## Installation

Install Governor globally, like `gopls`:

```bash
go install github.com/deixis/governor/cmd/governor@latest
```

Requires Go 1.25 or later.

It is optional but highly recommended to install `gopls`. When gopls is available, Governor proxies it as `gov_*` MCP tools. If gopls is not present, these tools return an error along with installation instructions.

## CLI usage

Governor provides the following commands:

```
governor check [flags] [packages...]
governor audit [flags] [packages...]
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

### Code intelligence (via gopls)

| Tool | Description |
|---|---|
| `gov_diagnostics` | Workspace diagnostics |
| `gov_package_api` | Public API of packages |
| `gov_search` | Fuzzy symbol search |
| `gov_file_context` | Intra-package dependencies |
| `gov_symbol_references` | Find symbol references |
| `gov_rename_symbol` | Rename a symbol and return a diff |

## Configuration

Governor can read optional configuration settings from a `.governor` YAML file located at the repository root.

```yaml
version: 1
timeout: 5m
max_output: 1048576

test:
  args: ["-race", "-count=1"]

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
