package main

import (
	"strings"
	"testing"
)

func TestReadPaths(t *testing.T) {
	got, err := readPaths(strings.NewReader("a.go\n\n  b.go  \nc.md\n"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "a.go,b.go,c.md" {
		t.Errorf("paths = %v", got)
	}
}

func TestGoFiles(t *testing.T) {
	got := goFiles([]string{"a.go", "README.md", "x_test.go", "dir/b.go.txt"})
	if strings.Join(got, ",") != "a.go,x_test.go" {
		t.Errorf("goFiles = %v", got)
	}
}
