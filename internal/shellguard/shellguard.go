// Package shellguard inspects shell commands an agent is about to run in
// a Go workspace and steers them to governor: tool chains such as go
// test, go vet, golangci-lint, staticcheck and goimports become governor
// check or governor fix, and in-place edits of Go files with sed, perl,
// sd or awk are blocked. It understands shell syntax, so every command in
// a pipeline, a && chain, a loop or a command substitution is inspected.
package shellguard

import (
	"fmt"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// Rewrite is one command replaced inside the shell input.
type Rewrite struct {
	From string
	To   string
	// Why says what the replacement runs instead.
	Why string
}

// Decision is the outcome of inspecting one shell input.
type Decision struct {
	// Command is the shell input with every rewrite applied. Equal to the
	// input when nothing changed.
	Command  string
	Rewrites []Rewrite
	// Blocked is the reason the input must not run, or "".
	Blocked string
}

// Changed reports whether the input was rewritten or blocked.
func (d Decision) Changed() bool {
	return d.Blocked != "" || len(d.Rewrites) > 0
}

// Context renders the rewrites as a note for the agent.
func (d Decision) Context() string {
	if len(d.Rewrites) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("governor rewrote this command before it ran:\n")
	for _, r := range d.Rewrites {
		fmt.Fprintf(&b, "  `%s` -> `%s` (%s)\n", r.From, r.To, r.Why)
	}
	b.WriteString("Tell the user the command was rewritten. Use gov_fix after edits and gov_check before finishing instead of these tools.")
	return b.String()
}

// Inspect parses command as bash and applies the rules to every simple
// command in it. A parse failure returns the input unchanged.
func Inspect(command string) Decision {
	parser := syntax.NewParser(syntax.KeepComments(true), syntax.Variant(syntax.LangBash))
	prog, err := parser.Parse(strings.NewReader(command), "")
	if err != nil {
		return Decision{Command: command}
	}

	// A Go file named anywhere in the input counts for every command in
	// it: a for loop passes the file to sed through a variable.
	mentionsGo := false
	syntax.Walk(prog, func(n syntax.Node) bool {
		if w, ok := n.(*syntax.Word); ok && isGoFile(wordText(w)) {
			mentionsGo = true
		}
		return !mentionsGo
	})

	d := Decision{Command: command}
	syntax.Walk(prog, func(n syntax.Node) bool {
		call, ok := n.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		name, ok := literal(call.Args[0])
		if !ok {
			return true
		}
		args := make([]string, 0, len(call.Args)-1)
		for _, w := range call.Args[1:] {
			args = append(args, wordText(w))
		}
		if reason := blocked(name, args, mentionsGo || anyGoFile(args)); reason != "" {
			if d.Blocked == "" {
				d.Blocked = reason
			}
			return true
		}
		to, why := rewrite(name, args)
		if to == nil {
			return true
		}
		from := name
		if len(args) > 0 {
			from += " " + strings.Join(args, " ")
		}
		d.Rewrites = append(d.Rewrites, Rewrite{From: from, To: strings.Join(to, " "), Why: why})
		call.Args = words(to)
		return true
	})

	if d.Blocked != "" {
		d.Rewrites = nil
		return d
	}
	if len(d.Rewrites) == 0 {
		return d
	}
	var out strings.Builder
	if err := syntax.NewPrinter().Print(&out, prog); err != nil {
		return Decision{Command: command}
	}
	d.Command = strings.TrimRight(out.String(), "\n")
	return d
}

// literal returns the word's value when it is made of plain literals.
func literal(w *syntax.Word) (string, bool) {
	var b strings.Builder
	for _, p := range w.Parts {
		lit, ok := p.(*syntax.Lit)
		if !ok {
			return "", false
		}
		b.WriteString(lit.Value)
	}
	return b.String(), true
}

// wordText renders a word as it appears in the source, quotes and
// expansions included.
func wordText(w *syntax.Word) string {
	var b strings.Builder
	_ = syntax.NewPrinter().Print(&b, w)
	return b.String()
}

func words(values []string) []*syntax.Word {
	out := make([]*syntax.Word, 0, len(values))
	for _, v := range values {
		out = append(out, &syntax.Word{Parts: []syntax.WordPart{&syntax.Lit{Value: v}}})
	}
	return out
}

// isGoFile reports whether an argument names a Go file, a Go file glob
// or a path ending in one.
func isGoFile(arg string) bool {
	arg = strings.Trim(arg, `"'`)
	return strings.HasSuffix(arg, ".go")
}

func anyGoFile(args []string) bool {
	return slices.ContainsFunc(args, isGoFile)
}
