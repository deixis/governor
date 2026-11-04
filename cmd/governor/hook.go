package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/deixis/governor/internal/shellguard"
)

func hookMain(args []string) error {
	fs := flag.NewFlagSet("hook", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, `Usage: governor hook bash [command...]

Agent harness hook for shell commands in a Go workspace. Reads a Claude
Code PreToolUse event on stdin and prints the decision as JSON: go test,
go vet, golangci-lint, staticcheck and in-place goimports/gofmt/gofumpt
become governor check or governor fix, and in-place edits of Go files
with sed, perl, sd or awk are refused. Prints nothing when the command
runs as is.

With a command given as arguments, prints what would happen instead of
reading an event.`)
	}
	_ = fs.Parse(args)
	rest := fs.Args()
	if len(rest) == 0 || rest[0] != "bash" {
		fs.Usage()
		os.Exit(2)
	}
	if len(rest) > 1 {
		d := shellguard.Inspect(strings.Join(rest[1:], " "))
		switch {
		case d.Blocked != "":
			fmt.Println(d.Blocked)
			os.Exit(1)
		case len(d.Rewrites) > 0:
			fmt.Println(d.Command)
			fmt.Println()
			fmt.Println(d.Context())
		default:
			fmt.Println("unchanged")
		}
		return nil
	}
	return shellguard.ClaudeCodeHook(os.Stdin, os.Stdout)
}
