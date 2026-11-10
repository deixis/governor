// Package gofile answers questions about Go source files on disk.
package gofile

import (
	"go/ast"
	"go/parser"
	"go/token"
)

// Generated reports whether the file at path carries the standard
// "// Code generated ... DO NOT EDIT." header before its package clause.
// Files that cannot be read or parsed count as hand-written.
func Generated(path string) bool {
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.PackageClauseOnly|parser.ParseComments)
	if err != nil {
		return false
	}
	return ast.IsGenerated(f)
}
