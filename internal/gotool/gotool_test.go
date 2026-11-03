package gotool

import (
	"slices"
	"testing"
)

func TestModuleTools(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want []string
	}{
		{
			"go 1.27 form",
			"asm\ncover\nvet\ngofumpt (mvdan.cc/gofumpt)\nstaticcheck (honnef.co/go/tools/cmd/staticcheck)\n",
			[]string{"mvdan.cc/gofumpt", "honnef.co/go/tools/cmd/staticcheck"},
		},
		{
			"pre 1.27 form",
			"asm\nvet\nmvdan.cc/gofumpt\nhonnef.co/go/tools/cmd/staticcheck\n",
			[]string{"mvdan.cc/gofumpt", "honnef.co/go/tools/cmd/staticcheck"},
		},
		{
			"go 1.27 ambiguous names print bare",
			"asm\nvet\nexample.com/x/cmd/vet\ngofumpt (mvdan.cc/gofumpt)\nexample.com/a/deadcode\ngolang.org/x/tools/cmd/deadcode\n",
			[]string{"example.com/x/cmd/vet", "mvdan.cc/gofumpt", "example.com/a/deadcode", "golang.org/x/tools/cmd/deadcode"},
		},
		{"built-ins only", "asm\ncover\nvet\n", nil},
		{"empty", "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ModuleTools(c.out); !slices.Equal(got, c.want) {
				t.Errorf("ModuleTools = %v, want %v", got, c.want)
			}
		})
	}
}

func TestFind(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"gopls", "golang.org/x/tools/gopls"},
		{"staticcheck", "honnef.co/go/tools/cmd/staticcheck"},
		{"mycmd", "example.com/mycmd/v2"},
		{"v2", ""},
		{"vet", ""},   // built-in, not a module tool
		{"cover", ""}, // built-in
		{"gocognit", ""},
		{"", ""},
	}
	outs := map[string]string{
		"pre 1.27": "asm\ncgo\ncompile\ncover\nvet\ngolang.org/x/tools/gopls\nhonnef.co/go/tools/cmd/staticcheck\nexample.com/mycmd/v2\n",
		"go 1.27":  "asm\nvet\ngopls (golang.org/x/tools/gopls)\nstaticcheck (honnef.co/go/tools/cmd/staticcheck)\nmycmd (example.com/mycmd/v2)\n",
	}
	for form, out := range outs {
		for _, c := range cases {
			if got := Find(out, c.name); got != c.want {
				t.Errorf("Find(%s output, %q) = %q, want %q", form, c.name, got, c.want)
			}
		}
	}

	// Ambiguous names print bare on Go 1.27; the first listed wins.
	out := "vet\nexample.com/a/deadcode\ngolang.org/x/tools/cmd/deadcode\n"
	if got := Find(out, "deadcode"); got != "example.com/a/deadcode" {
		t.Errorf("Find(ambiguous, deadcode) = %q", got)
	}
}

func TestExecName(t *testing.T) {
	cases := map[string]string{
		"mvdan.cc/gofumpt":           "gofumpt",
		"example.com/mycmd/v2":       "mycmd",
		"example.com/mycmd/v10":      "mycmd",
		"example.com/mycmd/v1":       "v1",
		"example.com/mycmd/v0":       "v0",
		"example.com/mycmd/v2beta":   "v2beta",
		"v2":                         "v2",
		"github.com/deixis/governor": "governor",
	}
	for in, want := range cases {
		if got := ExecName(in); got != want {
			t.Errorf("ExecName(%q) = %q, want %q", in, got, want)
		}
	}
}
