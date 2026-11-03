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
