package analysis

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"go/types"
	"slices"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"
)

// Request describes one intended change. The same request drives Plan
// (impact only) and Apply (make the change).
type Request struct {
	// Op is add, remove, replace or move.
	Op string
	// File scopes symbol resolution (remove, replace, move) and is the
	// destination file for add when Target is empty.
	File string
	// Symbol names the subject: Foo, T.Method, T.Field or pkg.Symbol.
	Symbol string
	// Source is Go declaration text for add and replace. It may start with
	// an import block; a package clause is not expected.
	Source string
	// Target is a package import path or an absolute file path: where add
	// puts the declarations, where move sends the symbol.
	Target string
	// Decisions answer the open questions of a move plan.
	Decisions Decisions
	// Force applies the change past blockers and unanswered questions:
	// the plan comes back OK with what it forced past listed as
	// follow-ups, and Apply leaves the broken sites for the agent. A
	// shared helper with no decision is pulled along.
	Force bool
}

// Decisions are the agent's answers to a move plan: unexported symbols of
// the source package to export in place, and shared symbols to pull along
// although other code uses them.
type Decisions struct {
	Export []string
	Pull   []string
}

// Verdict says whether a request can be applied.
type Verdict string

const (
	// VerdictOK: Apply makes the change. Follow-ups, if any, list what
	// the agent must still do afterwards.
	VerdictOK Verdict = "OK"
	// VerdictDecisions: the request must answer the questions, through
	// Decisions, before Apply runs.
	VerdictDecisions Verdict = "NEEDS DECISION"
	// VerdictBlocked: Apply refuses until the causes are resolved.
	VerdictBlocked Verdict = "BLOCKED"
)

// Issue is one reason a plan is not OK: what stands in the way and what
// would resolve it.
type Issue struct {
	Cause string
	Fix   string
}

// Report is the outcome of planning a request: a verdict, one line of
// what happens, and only the detail the agent has to act on.
type Report struct {
	Op      string
	Subject string
	Verdict Verdict
	// Summary is one line: what the change touches.
	Summary   string
	Blockers  []Issue
	Questions []Issue
	// FollowUps are sites Apply leaves for the agent: callers to update
	// after a signature change, uses that break after a removal.
	FollowUps []Section
	// Forced is set when Force turned blockers or questions into
	// follow-ups.
	Forced bool
}

// Section is one titled list.
type Section struct {
	Title string
	Lines []string
}

func (r *Report) block(cause, fix string) {
	r.Blockers = append(r.Blockers, Issue{Cause: cause, Fix: fix})
}

func (r *Report) ask(cause, fix string) {
	r.Questions = append(r.Questions, Issue{Cause: cause, Fix: fix})
}

func (r *Report) followUp(title string, lines []string) {
	if len(lines) == 0 {
		return
	}
	r.FollowUps = append(r.FollowUps, Section{Title: fmt.Sprintf("%s (%d)", title, len(lines)), Lines: lines})
}

// settle picks the verdict. With force, blockers and open questions turn
// into follow-ups and the verdict is OK.
func (r *Report) settle(force bool) {
	if force && (len(r.Blockers) > 0 || len(r.Questions) > 0) {
		var lines []string
		for _, is := range append(append([]Issue(nil), r.Blockers...), r.Questions...) {
			lines = append(lines, is.Cause)
		}
		r.FollowUps = append([]Section{{Title: fmt.Sprintf("Forced past (%d), fix afterwards", len(lines)), Lines: lines}}, r.FollowUps...)
		r.Blockers, r.Questions = nil, nil
		r.Forced = true
	}
	switch {
	case len(r.Blockers) > 0:
		r.Verdict = VerdictBlocked
	case len(r.Questions) > 0:
		r.Verdict = VerdictDecisions
	default:
		r.Verdict = VerdictOK
	}
}

// Plan reports whether a request can be applied and what it touches,
// without changing anything.
func (w *Workspace) Plan(ctx context.Context, req Request) (*Report, error) {
	snap, err := w.load(ctx)
	if err != nil {
		return nil, err
	}
	switch req.Op {
	case "add":
		return snap.planAdd(ctx, req)
	case "remove":
		return snap.planRemove(req)
	case "replace":
		return snap.planReplace(ctx, req)
	case "move":
		return snap.planMove(req)
	default:
		return nil, fmt.Errorf("op %q: expected add, remove, replace or move", req.Op)
	}
}

// maxFollowUpLines caps each follow-up list and maxIssues each list of
// causes or questions: the plan says where to act, gov_symbol lists every
// site.
const (
	maxFollowUpLines = 20
	maxIssues        = 20
)

// FormatReport renders a report as plain text.
func FormatReport(r *Report) string {
	var b strings.Builder
	verdict := string(r.Verdict)
	if r.Forced {
		verdict += " (forced)"
	}
	fmt.Fprintf(&b, "%s: %s %s\n", verdict, r.Op, r.Subject)
	if r.Summary != "" {
		fmt.Fprintf(&b, "  %s\n", r.Summary)
	}
	issues := func(title string, list []Issue) {
		if len(list) == 0 {
			return
		}
		fmt.Fprintf(&b, "\n%s:\n", title)
		for i, is := range list {
			if i == maxIssues {
				fmt.Fprintf(&b, "  ... +%d more\n", len(list)-maxIssues)
				break
			}
			fmt.Fprintf(&b, "  - %s\n", is.Cause)
			if is.Fix != "" {
				fmt.Fprintf(&b, "    Fix: %s\n", is.Fix)
			}
		}
	}
	issues("Causes", r.Blockers)
	issues("Decide", r.Questions)
	if len(r.FollowUps) > 0 {
		b.WriteString("\nAfter apply, you must:\n")
		for _, s := range r.FollowUps {
			fmt.Fprintf(&b, "  %s\n", s.Title)
			for i, l := range s.Lines {
				if i == maxFollowUpLines {
					fmt.Fprintf(&b, "    ... +%d more\n", len(s.Lines)-maxFollowUpLines)
					break
				}
				fmt.Fprintf(&b, "    %s\n", l)
			}
		}
	}
	return b.String()
}

// plural renders "n word(s)".
func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// packageByPath returns the plain variant of a workspace package.
func (s *snapshot) packageByPath(path string) *packages.Package {
	for _, p := range s.pkgs {
		if p.PkgPath == path && p.ID == p.PkgPath {
			return p
		}
	}
	return nil
}

// decl is one top-level declaration or spec with the identifier that
// names it.
type decl struct {
	pkg  *packages.Package
	file *ast.File
	// node is the FuncDecl, or the TypeSpec/ValueSpec inside gen.
	node ast.Node
	gen  *ast.GenDecl
	name *ast.Ident
	obj  types.Object
}

// span is the source range a decl occupies, doc comment included. For a
// spec alone in its group the whole group is the span.
func (d decl) span() (token.Pos, token.Pos) {
	if d.gen != nil && len(d.gen.Specs) == 1 {
		return nodeStart(d.gen, d.gen.Doc), d.gen.End()
	}
	switch n := d.node.(type) {
	case *ast.FuncDecl:
		return nodeStart(n, n.Doc), n.End()
	case *ast.TypeSpec:
		return nodeStart(n, n.Doc), n.End()
	case *ast.ValueSpec:
		return nodeStart(n, n.Doc), n.End()
	}
	return d.node.Pos(), d.node.End()
}

func nodeStart(n ast.Node, doc *ast.CommentGroup) token.Pos {
	if doc != nil {
		return doc.Pos()
	}
	return n.Pos()
}

// label renders "kind Name" or "kind T.Method".
func (d decl) label() string {
	return objectKind(d.obj) + " " + qualifiedName(d.obj)
}

// syntaxLabel renders the declaration keyword and name the way declNames
// does for parsed source: "type Name" for every type, interface or alias
// included, "method T.Name", "func Name", "var Name", "const Name".
func (d decl) syntaxLabel() string {
	switch n := d.node.(type) {
	case *ast.FuncDecl:
		if n.Recv != nil {
			return "method " + receiverName(n.Recv) + "." + d.name.Name
		}
		return "func " + d.name.Name
	case *ast.TypeSpec:
		return "type " + d.name.Name
	case *ast.ValueSpec:
		return strings.ToLower(d.gen.Tok.String()) + " " + d.name.Name
	}
	return d.label()
}

// decls indexes every top-level declaration of p by object. Methods are
// included; grouped specs each get their own entry.
func (s *snapshot) decls(p *packages.Package) map[types.Object]decl {
	out := make(map[types.Object]decl)
	for _, f := range p.Syntax {
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if obj := p.TypesInfo.Defs[d.Name]; obj != nil {
					out[obj] = decl{pkg: p, file: f, node: d, name: d.Name, obj: obj}
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch sp := spec.(type) {
					case *ast.TypeSpec:
						if obj := p.TypesInfo.Defs[sp.Name]; obj != nil {
							out[obj] = decl{pkg: p, file: f, node: sp, gen: d, name: sp.Name, obj: obj}
						}
					case *ast.ValueSpec:
						for _, n := range sp.Names {
							if obj := p.TypesInfo.Defs[n]; obj != nil {
								out[obj] = decl{pkg: p, file: f, node: sp, gen: d, name: n, obj: obj}
							}
						}
					}
				}
			}
		}
	}
	return out
}

// methodsOf returns the declared methods of a named type, from the index,
// in source order. Files are ordered by name: token.Pos order across
// files depends on parse order, which varies between loads.
func methodsOf(index map[types.Object]decl, tn *types.TypeName) []decl {
	var out []decl
	for obj, d := range index {
		if memberOwner(obj) == tn {
			if _, isFunc := obj.(*types.Func); isFunc {
				out = append(out, d)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		pi, pj := out[i].pkg.Fset.Position(out[i].name.Pos()), out[j].pkg.Fset.Position(out[j].name.Pos())
		if pi.Filename != pj.Filename {
			return pi.Filename < pj.Filename
		}
		return pi.Offset < pj.Offset
	})
	return out
}

// memberOwner returns the named type declaring a method or field, or nil
// for anything else. A field costs a scan of its package scope; callers
// resolving many objects use fieldOwners.
func memberOwner(obj types.Object) *types.TypeName {
	switch o := obj.(type) {
	case *types.Func:
		return methodOwner(o)
	case *types.Var:
		if o.IsField() && o.Pkg() != nil {
			return fieldOwners(o.Pkg())[o]
		}
	}
	return nil
}

// methodOwner returns the named type a method is declared on, or nil.
func methodOwner(fn *types.Func) *types.TypeName {
	recv := fn.Type().(*types.Signature).Recv()
	if recv == nil {
		return nil
	}
	t := recv.Type()
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	if named, ok := t.(*types.Named); ok {
		return named.Obj()
	}
	return nil
}

// fieldOwners maps every field of a package-level struct type of pkg to
// that type, in one pass over the scope.
func fieldOwners(pkg *types.Package) map[types.Object]*types.TypeName {
	out := make(map[types.Object]*types.TypeName)
	scope := pkg.Scope()
	for _, name := range scope.Names() {
		tn, ok := scope.Lookup(name).(*types.TypeName)
		if !ok {
			continue
		}
		if st, ok := tn.Type().Underlying().(*types.Struct); ok {
			for f := range st.Fields() {
				out[f] = tn
			}
		}
	}
	return out
}

// ownerOf is memberOwner with the field index precomputed.
func ownerOf(owners map[types.Object]*types.TypeName, obj types.Object) *types.TypeName {
	if fn, ok := obj.(*types.Func); ok {
		return methodOwner(fn)
	}
	return owners[obj]
}

// variantsOf lists the package variants sharing p's import path: the
// plain package and, with tests, the in-package test variant. Test mains
// are left out.
func (s *snapshot) variantsOf(p *packages.Package) []*packages.Package {
	var out []*packages.Package
	for _, v := range s.pkgs {
		if v.PkgPath == p.PkgPath && v.TypesInfo != nil && !isTestMain(v) {
			out = append(out, v)
		}
	}
	return out
}

// syntaxAt returns the parsed file of p holding pos, or nil.
func (s *snapshot) syntaxAt(p *packages.Package, pos token.Pos) *ast.File {
	for _, f := range p.Syntax {
		if pos >= f.FileStart && pos < f.FileEnd {
			return f
		}
	}
	return nil
}

// firstN returns at most the first n elements.
func firstN[T any](in []T, n int) []T {
	if len(in) <= n {
		return in
	}
	return in[:n]
}

// parsed is declaration text taken apart: the declarations, the import
// paths of any leading import block, and the text with that block and
// any package clause blanked. Blanking keeps line numbers: line n of
// body is line n of the source as given.
type parsed struct {
	decls   []ast.Decl
	imports []string
	body    string
	// text is the source as parsed, package clause blanked, and fset
	// positions it with offsets shifted by prefixLen.
	text string
	fset *token.FileSet
}

// prefixLen is the length of the package clause parseSource prepends.
const prefixLen = len("package p\n\n")

// nodeText returns the source text of a node parsed from the source.
func (p *parsed) nodeText(n ast.Node) string {
	start := p.fset.Position(n.Pos()).Offset - prefixLen
	end := p.fset.Position(n.End()).Offset - prefixLen
	return p.text[start:end]
}

// lineText returns the source text of a node with the blank lines that
// put it on its own line of the source.
func (p *parsed) lineText(n ast.Node) string {
	line := p.fset.Position(n.Pos()).Line - strings.Count("package p\n\n", "\n")
	return strings.Repeat("\n", line-1) + p.nodeText(n)
}

// blankPackageClause replaces a leading package clause with spaces, so
// the rest of the text keeps its offsets and line numbers.
func blankPackageClause(src string) string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.PackageClauseOnly)
	if err != nil || f.Name == nil || !f.Package.IsValid() {
		return src
	}
	start := fset.Position(f.Package).Offset
	end := fset.Position(f.Name.End()).Offset
	return src[:start] + strings.Repeat(" ", end-start) + src[end:]
}

// parseSource parses declaration text. A package clause is blanked, and
// leading import declarations are returned separately as import paths.
// Parse errors carry the line numbers of the text as given.
func parseSource(src string) (*parsed, error) {
	src = blankPackageClause(src)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "source.go", "package p\n\n"+src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		if list, ok := err.(scanner.ErrorList); ok && len(list) > 0 {
			e := list[0]
			return nil, fmt.Errorf("source does not parse: line %d: %s", e.Pos.Line-2, e.Msg)
		}
		return nil, fmt.Errorf("source does not parse: %w", err)
	}
	out := &parsed{text: src, fset: fset}
	var cuts []span
	for _, d := range f.Decls {
		if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.IMPORT {
			for _, spec := range g.Specs {
				path, err := strconv.Unquote(spec.(*ast.ImportSpec).Path.Value)
				if err != nil {
					return nil, fmt.Errorf("import %s: %w", spec.(*ast.ImportSpec).Path.Value, err)
				}
				out.imports = append(out.imports, path)
			}
			cuts = append(cuts, span{g.Pos(), g.End()})
			continue
		}
		out.decls = append(out.decls, d)
	}
	if len(out.decls) == 0 {
		return nil, fmt.Errorf("source declares nothing")
	}
	body := src
	for _, cut := range slices.Backward(cuts) {
		start := fset.Position(cut.start).Offset - prefixLen
		end := fset.Position(cut.end).Offset - prefixLen
		body = body[:start] + strings.Repeat("\n", strings.Count(body[start:end], "\n")) + body[end:]
	}
	out.body = body
	return out, nil
}

// declNames lists the identifiers a parsed declaration introduces, with
// a kind label.
func declNames(d ast.Decl) []string {
	switch d := d.(type) {
	case *ast.FuncDecl:
		if d.Recv != nil {
			return []string{"method " + receiverName(d.Recv) + "." + d.Name.Name}
		}
		return []string{"func " + d.Name.Name}
	case *ast.GenDecl:
		var out []string
		for _, spec := range d.Specs {
			switch sp := spec.(type) {
			case *ast.TypeSpec:
				out = append(out, "type "+sp.Name.Name)
			case *ast.ValueSpec:
				for _, n := range sp.Names {
					out = append(out, strings.ToLower(d.Tok.String())+" "+n.Name)
				}
			}
		}
		return out
	}
	return nil
}

// bareName strips the kind label from a declNames entry.
func bareName(labelled string) string {
	if i := strings.LastIndexByte(labelled, ' '); i >= 0 {
		labelled = labelled[i+1:]
	}
	if i := strings.LastIndexByte(labelled, '.'); i >= 0 {
		labelled = labelled[i+1:]
	}
	return labelled
}

// isGenerated reports whether f carries the standard generated-code
// header.
func isGenerated(f *ast.File) bool {
	for _, g := range f.Comments {
		if g.Pos() > f.Package {
			break
		}
		for _, c := range g.List {
			if strings.HasPrefix(c.Text, "// Code generated ") && strings.HasSuffix(c.Text, " DO NOT EDIT.") {
				return true
			}
		}
	}
	return false
}

// fileOf returns the workspace-relative file name holding d.
func (s *snapshot) fileOf(d decl) string {
	return relPath(s.root, s.fset.Position(d.file.Pos()).Filename)
}
