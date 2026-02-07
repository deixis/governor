// Command governor provides structured Go project tooling.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/deixis/governor"
	"github.com/deixis/governor/internal/config"
	govmcp "github.com/deixis/governor/internal/mcp"
	"github.com/deixis/governor/internal/report"
	"github.com/deixis/governor/internal/runner"
	"github.com/deixis/governor/internal/workflow"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("governor: ")

	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "mcp":
		err = mcpMain(args)
	case "check":
		err = checkMain(args)
	case "audit":
		err = auditMain(args)
	case "version":
		fmt.Println(governor.Version)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "governor: unknown command %q\n", cmd)
		usage()
		os.Exit(2)
	}

	if err != nil {
		log.Fatal(err)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `Usage: governor <command> [flags] [packages]

Commands:
  check       Run the check pipeline (fix, test, lint, staticcheck)
  audit       Run audit checks (coverage, complexity, deadcode, dupl, vulncheck)
  mcp         Start the MCP server
  version     Print the version
  help        Show this help

Use "governor <command> -h" for command-specific flags.`)
}

// --- mcp ---

func mcpMain(args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	instructions := fs.Bool("instructions", false, "print model instructions and exit")
	httpAddr := fs.String("http", "", "start HTTP server on address (e.g. :9090)")
	_ = fs.Parse(args)

	if *instructions {
		fmt.Print(govmcp.Instructions)
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	return serve(ctx, *httpAddr)
}

func serve(ctx context.Context, httpAddr string) error {
	workspace, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("determining workspace: %w", err)
	}

	loaded, err := config.Load(workspace)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	cfg := loaded.Config

	disk := report.NewDiskStore()
	store := report.NewLRUStore(5, disk)

	r := &runner.Runner{
		Workspace: workspace,
		Timeout:   cfg.Timeout(),
		MaxOutput: cfg.MaxOutputBytes(),
	}

	var opts []govmcp.ServerOption
	proxy, stopProxy, proxyErr := govmcp.StartGoplsProxy(ctx, workspace)
	if proxyErr != nil {
		log.Printf("gopls proxy failed to start: %v", proxyErr)
	}
	if proxy != nil {
		opts = append(opts, govmcp.WithGoplsProxy(proxy))
		defer stopProxy()
	}

	server := govmcp.NewServer(cfg, r, store, workspace, opts...)

	if httpAddr != "" {
		return serveHTTP(ctx, server, httpAddr)
	}
	return server.Run(ctx, &mcpsdk.StdioTransport{})
}

func serveHTTP(ctx context.Context, server *mcpsdk.Server, addr string) error {
	handler := mcpsdk.NewStreamableHTTPHandler(
		func(_ *http.Request) *mcpsdk.Server { return server },
		nil,
	)

	httpServer := &http.Server{
		Addr:    addr,
		Handler: handler,
	}

	go func() {
		<-ctx.Done()
		_ = httpServer.Close()
	}()

	log.Printf("listening on %s", addr)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("http server: %w", err)
	}
	return nil
}

// --- check ---

func checkMain(args []string) error {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	fixFlag := fs.Bool("fix", false, "run auto-fix phase before checks")
	jsonFlag := fs.Bool("json", false, "output results as JSON")
	verboseFlag := fs.Bool("v", false, "verbose output")
	timeoutFlag := fs.Duration("timeout", 0, "override configured timeout (e.g. 5m)")
	_ = fs.Parse(args)

	packages := fs.Args()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	eng, err := newEngine(*timeoutFlag)
	if err != nil {
		return err
	}

	result, err := eng.Check(ctx, packages, *fixFlag)
	if err != nil {
		return fmt.Errorf("check: %w", err)
	}

	failed := result.FailedIdx >= 0 || result.FailedIdx == -2

	if *jsonFlag {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(result.RunResult); err != nil {
			return err
		}
	} else {
		fmt.Print(formatCheckCLI(result, *verboseFlag))
	}

	if failed {
		os.Exit(1)
	}
	return nil
}

func formatCheckCLI(result *workflow.CheckResult, verbose bool) string {
	rr := result.RunResult
	var b []byte
	w := func(format string, args ...any) {
		b = fmt.Appendf(b, format, args...)
	}

	// Format failure: format issues before steps ran.
	if result.FailedIdx == -2 {
		w("FAIL\n\n")
		w("Formatting issues (%d files):\n", len(rr.FormatIssues))
		for _, f := range rr.FormatIssues {
			w("  %s\n", f.File)
		}
		w("\nRun gofumpt to format, or use -fix.\n")
		return string(b)
	}

	allPassed := result.FailedIdx < 0

	if allPassed {
		w("ok\n")
	} else {
		w("FAIL\n")
	}
	w("\n")

	if rr.AutoFixes > 0 {
		w("Auto-fixed: %d issues\n\n", rr.AutoFixes)
	}

	for _, s := range result.Steps {
		switch s.Status {
		case "pass":
			w("  %-15s ok\n", s.Name)
		case "fail":
			w("  %-15s FAIL\n", s.Name)
		case "unavailable":
			w("  %-15s unavailable\n", s.Name)
		case "skipped":
			w("  %-15s -\n", s.Name)
		}
	}
	w("\n")

	if !allPassed {
		failed := result.Steps[result.FailedIdx]

		failures := workflow.FormatFailureSymbols(rr)
		if len(failures) > 0 {
			for _, f := range failures {
				w("  %s\n", f)
			}
			w("\n")
		}

		if verbose && failed.Output != "" {
			w("%s\n", failed.Output)
		}
	}

	return string(b)
}

// --- audit ---

func auditMain(args []string) error {
	fs := flag.NewFlagSet("audit", flag.ExitOnError)
	jsonFlag := fs.Bool("json", false, "output results as JSON")
	verboseFlag := fs.Bool("v", false, "verbose output")
	timeoutFlag := fs.Duration("timeout", 0, "override configured timeout (e.g. 5m)")
	_ = fs.Parse(args)

	packages := fs.Args()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	eng, err := newEngine(*timeoutFlag)
	if err != nil {
		return err
	}

	result, err := eng.Audit(ctx, packages)
	if err != nil {
		return fmt.Errorf("audit: %w", err)
	}

	if *jsonFlag {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result.RunResult)
	}

	fmt.Print(formatAuditCLI(result, *verboseFlag))
	return nil
}

func formatAuditCLI(result *workflow.AuditResult, verbose bool) string {
	var b []byte
	w := func(format string, args ...any) {
		b = fmt.Appendf(b, format, args...)
	}

	completed := 0
	for _, r := range result.Steps {
		if r.Status == "done" {
			completed++
		}
	}

	w("Audit: %d/%d checks completed\n\n", completed, len(result.Steps))

	for _, r := range result.Steps {
		switch r.Status {
		case "done":
			w("%s:\n", r.Name)
			w("%s\n", r.Output)
		case "unavailable":
			w("%s: unavailable (%s)\n\n", r.Name, r.Detail)
		case "error":
			w("%s: error (%s)\n\n", r.Name, r.Detail)
		case "skipped":
			w("%s: skipped\n\n", r.Name)
		}
	}

	return string(b)
}

// --- shared ---

func newEngine(timeoutOverride time.Duration) (*workflow.Engine, error) {
	workspace, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("determining workspace: %w", err)
	}

	loaded, err := config.Load(workspace)
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}
	cfg := loaded.Config

	timeout := cfg.Timeout()
	if timeoutOverride > 0 {
		timeout = timeoutOverride
	}

	r := &runner.Runner{
		Workspace: loaded.RepoRoot,
		Timeout:   timeout,
		MaxOutput: cfg.MaxOutputBytes(),
	}

	return &workflow.Engine{
		Config:    cfg,
		Runner:    r,
		Workspace: workspace,
		RepoRoot:  loaded.RepoRoot,
	}, nil
}
