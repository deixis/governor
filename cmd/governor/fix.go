package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/deixis/governor/internal/goanalysis"
)

func fixMain(args []string) error {
	fs := flag.NewFlagSet("fix", flag.ExitOnError)
	stdinFlag := fs.Bool("stdin", false, "read file paths from stdin, one per line, in addition to arguments")
	quietFlag := fs.Bool("q", false, "print nothing when there are no changes and no diagnostics")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, `Usage: governor fix [flags] [files...]

Repairs each Go file in place (modernize rewrites, missing/unused imports,
gofumpt) and prints remaining diagnostics. Non-Go files are ignored. Exit
code is 1 when any file still has diagnostics.

Designed to run after every edit: wire it to an editor save hook or an
agent's post-edit hook and pass the edited path, or pipe paths with -stdin.`)
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)

	files := fs.Args()
	if *stdinFlag {
		more, err := readPaths(os.Stdin)
		if err != nil {
			return err
		}
		files = append(files, more...)
	}
	files = goFiles(files)
	if len(files) == 0 {
		if *stdinFlag || *quietFlag {
			return nil
		}
		fs.Usage()
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	workspace, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("determining workspace: %w", err)
	}
	ws := goanalysis.NewWorkspace(workspace)

	results, err := ws.Fix(ctx, files)
	if err != nil {
		return fmt.Errorf("fix: %w", err)
	}

	failed, noisy := false, false
	for _, r := range results {
		if r.Err != nil || len(r.Diagnostics) > 0 {
			failed = true
		}
		if len(r.Changes) > 0 || len(r.Diagnostics) > 0 || r.Err != nil {
			noisy = true
		}
	}
	if noisy || !*quietFlag {
		fmt.Print(goanalysis.FormatFixResults(results))
	}
	if failed {
		os.Exit(1)
	}
	return nil
}

// readPaths reads newline-separated paths, skipping blank lines.
func readPaths(r io.Reader) ([]string, error) {
	var out []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if p := strings.TrimSpace(sc.Text()); p != "" {
			out = append(out, p)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading paths from stdin: %w", err)
	}
	return out, nil
}

// goFiles keeps only .go paths.
func goFiles(paths []string) []string {
	var out []string
	for _, p := range paths {
		if filepath.Ext(p) == ".go" {
			out = append(out, p)
		}
	}
	return out
}
