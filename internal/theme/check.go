package theme

import (
	"context"
	"errors"
	"fmt"
	htmltemplate "html/template"
	"io/fs"
	"path"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"text/template/parse"
	"time"

	"golang.org/x/net/html"
)

// The checks of an uploaded theme's templates. html/template escapes every
// value, and the Content Security Policy keeps scripts and styles out of
// the page, but a template is still a program that runs on the server for
// every visitor and decides what the page says. So a theme's templates:
//
//   - call only the functions of themeFuncs, the ones docs/THEMES.md lists
//     (minus seq), and parse with the server's functions and the shared
//     templates, so that an unknown function fails;
//   - cannot redefine the protected templates, which render the head and
//     the credit, nor name a template with "$" (html/template's names for
//     the escaped copies of templates);
//   - loop only over lists of the page data: fields, methods and index or
//     slice of them, never over a number, a literal, a list the template
//     makes (list, seq) or the result of arithmetic. The page data is built
//     by the server and bounded by the site's content, so a loop is too;
//   - cannot recurse, so that every page terminates;
//   - do not nest loops whose passes may write nothing: a page that keeps
//     writing is stopped by the output and time limits of RenderContext,
//     but a loop that writes nothing is not, so the work of one silent
//     pass must stay small and cheap (no text scans, no other silent
//     loop), and the whole page has a bounded number of steps;
//   - format only with literal formats and widths up to 99;
//   - never read a form token (the signed-in user's CSRF token, the
//     comment form's), call server-side methods such as .Site.Assistant,
//     or write out whole values that hold a token (printing a struct
//     prints its fields), nor write the placeholders the server fills with
//     tokens and plugin forms;
//   - hold no <script>, <style>, style attributes, event handlers,
//     <meta http-equiv>, <base>, frames, plugins, <math>, or form controls
//     that post elsewhere; their forms use GET and send to a path of the
//     site, and never hold a shared template with a form or a form
//     control (the comment form, the admin bar), whose fields, tokens
//     included, the browser would add to the theme's form. Inside <svg>,
//     only drawing elements: the HTML tokenizer of these checks reads the
//     page as a browser does only outside foreign content;
//   - leave the page's Referrer-Policy alone: no referrerpolicy attribute,
//     no <meta name="referrer">, no rel="opener";
//   - always render shared/footer-meta with the page data, in the page's
//     text: the licence's attribution. RenderContext checks the rendered
//     page too.
//
// These rules apply to the theme's own templates. The shared templates are
// trusted code, but they run with what the theme passes them, so the loop
// and secret rules follow the theme's arguments into them.

// protected templates are rendered by the core: a theme cannot redefine
// them.
var protected = map[string]bool{
	"shared/head": true, "shared/footer-meta": true, "shared/blocks-footer": true,
	"shared/blocks-sections": true, "shared/block": true, "blocks/footer-meta": true,
	"shared/consent": true, "shared/consent-link": true,
}

// textOnly are shared templates a theme must call in the page's text: the
// credit, and the forms that carry the signed-in user's CSRF token, which
// must not end up in an attribute (an image URL) or a comment.
var textOnly = map[string]bool{
	"shared/footer-meta": true, "shared/admin-bar": true, "shared/comments": true, "shared/comment-form": true,
}

// themeFuncs are the functions an uploaded theme's templates may call:
// those docs/THEMES.md lists, plans and statusLabel, and the built-in
// functions of text/template but call. seq is left out (it makes a list
// to loop over from a number), and so are the admin's helpers and toJSON,
// which writes out whole values.
var themeFuncs = map[string]bool{
	"T": true, "plural": true, "link": true, "lang": true, "date": true, "shortDate": true, "dateTime": true,
	"ago": true, "monthName": true, "isoDate": true, "icon": true, "dict": true, "list": true,
	"add": true, "sub": true, "mul": true, "div": true, "join": true, "lower": true, "upper": true,
	"contains": true, "hasPrefix": true, "truncate": true, "initials": true, "paragraphs": true,
	"plans": true, "statusLabel": true,
	"and": true, "or": true, "not": true, "len": true, "index": true, "slice": true,
	"eq": true, "ne": true, "lt": true, "le": true, "gt": true, "ge": true,
	"print": true, "printf": true, "println": true, "html": true, "js": true, "urlquery": true,
}

// secretFields hold a form token: the signed-in user's CSRF token, the
// comment form's token; deniedMembers do work on the server instead of
// reading the page data.
var (
	secretFields  = map[string]bool{"CSRF": true, "Token": true}
	deniedMembers = map[string]bool{"Assistant": true, "Consent": true, "ConsentOnce": true}
)

// TokenSlots are the placeholders the server replaces with a fresh form
// token in every response (internal/web: commentTokenSlot, formTokenSlot);
// a theme's templates and translations cannot write them.
var TokenSlots = []string{"sap-ctoken-", "sap-ftoken-"}

// refusedElements cannot appear in a theme's templates. The CSP blocks
// most of them anyway; refusing them tells the author at once.
var refusedElements = map[string]string{
	"script": "template.script", "style": "template.style", "base": "template.element",
	"iframe": "template.element", "frame": "template.element", "frameset": "template.element",
	"object": "template.element", "embed": "template.element", "applet": "template.element",
	"math": "template.element",
	// A browser with JavaScript reads <noscript> as raw text, one without
	// it as markup; no single reading checks both, and a theme, which has
	// no JavaScript, has no use for it.
	"noscript": "template.element",
}

// svgElements are the elements a theme's <svg> can hold: drawing ones.
// None is an HTML integration point (foreignObject, desc, title), raw text
// (style, script) or an HTML element that ends the SVG, so the browser
// reads everything up to </svg> as SVG, as the checks do.
var svgElements = map[string]bool{
	"svg": true, "g": true, "path": true, "circle": true, "ellipse": true, "line": true, "polyline": true,
	"polygon": true, "rect": true, "defs": true, "symbol": true, "use": true, "lineargradient": true,
	"radialgradient": true, "stop": true, "clippath": true, "mask": true, "pattern": true, "text": true, "tspan": true,
}

// formAttrs re-target a form; a theme's controls cannot have them.
var formAttrs = map[string]bool{
	"form": true, "formaction": true, "formmethod": true, "formenctype": true, "formtarget": true,
}

// formTags make a template "form-bearing": a form, a control with a value
// (such as a hidden token), or a plugin form's placeholder.
var formTags = []string{"<form", "<input", "<select", "<textarea", "data-form"}

// Bounds of the expansion of a page's templates (calls expanded, loops
// counted once) and of one pass of a loop that may write nothing, in
// steps; scanCost is the weight of a function that reads a whole text.
const (
	maxSteps      = 100_000
	maxSilentBody = 400
	scanCost      = 100
	// The analysis itself is bounded: templates analysed per page, and
	// the size of a value's description.
	maxRuns = 5000
	maxSig  = 8 << 10
)

var (
	stringType = reflect.TypeFor[string]()
	intType    = reflect.TypeFor[int]()
	floatType  = reflect.TypeFor[float64]()
	boolType   = reflect.TypeFor[bool]()
	anyMap     = reflect.TypeFor[map[string]any]()
	anySlice   = reflect.TypeFor[[]any]()
	timeType   = reflect.TypeFor[time.Time]()
)

// checker collects the problems of one theme.
type checker struct {
	m        *Manager
	pageType reflect.Type
	results  map[string]reflect.Type // functions -> their result type
	problems []Problem
	seen     map[string]bool
	secret   map[reflect.Type]bool
}

func (c *checker) report(p Problem) {
	key := p.Code + "\x00" + p.File + "\x00" + strconv.Itoa(p.Line) + "\x00" + fmt.Sprint(p.Args...)
	if c.seen[key] || len(c.problems) >= MaxProblems {
		return
	}
	c.seen[key] = true
	c.problems = append(c.problems, p)
}

// hasSecret reports whether a value of type t holds a secret field.
func (c *checker) hasSecret(t reflect.Type) bool {
	if t == nil {
		return false
	}
	if v, ok := c.secret[t]; ok {
		return v
	}
	c.secret[t] = false // cycles
	found := false
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array:
		found = c.hasSecret(t.Elem())
	case reflect.Map:
		found = c.hasSecret(t.Key()) || c.hasSecret(t.Elem())
	case reflect.Struct:
		for i := range t.NumField() {
			f := t.Field(i)
			if f.IsExported() && (secretFields[f.Name] || c.hasSecret(f.Type)) {
				found = true
				break
			}
		}
	}
	c.secret[t] = found
	return found
}

// checkTemplates runs the checks on a theme loaded from the staging
// folder.
func (m *Manager) checkTemplates(fsys fs.FS, t *Theme) []Problem {
	c := &checker{m: m, pageType: m.pageType, results: map[string]reflect.Type{}, seen: map[string]bool{}, secret: map[reflect.Type]bool{}}
	addFuncs := func(fm map[string]any) {
		for name, fn := range fm {
			if ft := reflect.TypeOf(fn); ft != nil && ft.Kind() == reflect.Func && ft.NumOut() > 0 {
				c.results[name] = ft.Out(0)
			}
		}
	}
	addFuncs(m.funcs)
	if m.langFuncs != nil {
		addFuncs(m.langFuncs("en"))
	}
	shared := map[string]bool{}
	for _, pattern := range []string{"*.html", "blocks/*.html"} {
		files, _ := fs.Glob(m.shared, pattern)
		for _, f := range files {
			src, err := fs.ReadFile(m.shared, f)
			if err != nil {
				continue
			}
			trees, _ := parseFile(path.Base(f), string(src))
			for name := range trees {
				shared[name] = true
			}
		}
	}

	// Each file on its own: names and HTML.
	partials, _ := fs.Glob(fsys, "templates/partials/*.html")
	sections, _ := fs.Glob(fsys, "templates/sections/*.html")
	partials = append(partials, sections...) // every page's set holds both
	files := append([]string{"templates/layout.html"}, partials...)
	for _, p := range pageNames {
		if _, err := fs.Stat(fsys, "templates/"+p+".html"); err == nil {
			files = append(files, "templates/"+p+".html")
		}
	}
	parsed := map[string]map[string]*parse.Tree{}
	for _, f := range files {
		src, err := fs.ReadFile(fsys, f)
		if err != nil {
			continue
		}
		trees, err := parseFile(path.Base(f), string(src))
		if err != nil {
			c.report(Problem{Code: "template.parse", File: f, Args: []any{cleanParseError(err)}})
			continue
		}
		parsed[f] = trees
		for _, name := range sortedKeys(trees) {
			tr := trees[name]
			line := lineOf(tr, tr.Root)
			if strings.Contains(name, "$") {
				c.report(Problem{Code: "template.name", File: f, Line: line, Args: []any{name}})
			}
			if protected[name] {
				c.report(Problem{Code: "template.protected", File: f, Line: line, Args: []any{name}})
			}
			walkNodes(tr.Root, func(n parse.Node) {
				var s string
				switch n := n.(type) {
				case *parse.TextNode:
					s = string(n.Text)
				case *parse.StringNode:
					s = n.Text
				default:
					return
				}
				for _, slot := range TokenSlots {
					if strings.Contains(s, slot) {
						c.report(Problem{Code: "template.token", File: f, Line: lineOf(tr, n), Args: []any{slot}})
					}
				}
			})
			sk := &skeleton{tree: tr, file: f}
			sk.walk(tr.Root)
			c.checkHTML(sk)
		}
	}
	if len(c.problems) > 0 {
		return c.problems
	}
	if c.pageType == nil {
		c.report(Problem{Code: "template.parse", Args: []any{"the server did not name its page data"}})
		return c.problems
	}

	// Each page as it renders: the layout with the page's templates.
	owns := map[string]map[string]string{}
	var noCredit []string
	for _, page := range sortedKeys(t.pages) {
		own := map[string]string{}
		for _, f := range append(append([]string{"templates/layout.html"}, partials...), "templates/"+page+".html") {
			for _, name := range sortedKeys(parsed[f]) {
				tr := parsed[f][name]
				if parse.IsEmptyTree(tr.Root) && (shared[name] || own[name] != "") {
					continue // html/template keeps the earlier definition
				}
				own[name] = f
			}
		}
		owns[page] = own
		if layout := t.pages[page].Lookup("layout"); layout != nil && layout.Tree != nil {
			sk := &skeleton{set: t.pages[page], own: own, tree: layout.Tree, file: "templates/layout.html", stack: []string{"layout"}}
			sk.walk(layout.Tree.Root)
			c.checkHTML(sk)
		}
		pc := &pageCheck{c: c, set: t.pages[page], own: own, memo: map[string]*sum{}}
		s := pc.call("layout", val{t: c.pageType, data: true}, loc{file: "templates/layout.html"}, false, nil, nil)
		if s.cost > maxSteps {
			c.report(Problem{Code: "template.too-big", File: "templates/" + page + ".html", Args: []any{page, maxSteps}})
		}
		if !s.credit {
			noCredit = append(noCredit, page)
		}
	}
	if len(noCredit) > 0 {
		c.report(Problem{Code: "template.credit", File: "templates/layout.html", Args: []any{strings.Join(noCredit, ", ")}})
	}
	if len(c.problems) == 0 {
		c.probe(t, owns)
	}
	return c.problems
}

// parseFile parses a template file on its own, as html/template names its
// templates: the file's base name and its {{define}}s.
func parseFile(name, src string) (map[string]*parse.Tree, error) {
	trees := map[string]*parse.Tree{}
	t := parse.New(name)
	t.Mode = parse.SkipFuncCheck
	_, err := t.Parse(src, "", "", trees)
	return trees, err
}

func lineOf(tr *parse.Tree, n parse.Node) int {
	if tr == nil || n == nil {
		return 0
	}
	location, _ := tr.ErrorContext(n)
	parts := strings.Split(location, ":")
	if len(parts) < 3 {
		return 0
	}
	line, _ := strconv.Atoi(parts[len(parts)-2])
	return line
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// walkNodes calls fn on n and every node under it.
func walkNodes(n parse.Node, fn func(parse.Node)) {
	if n == nil || reflect.ValueOf(n).IsNil() {
		return
	}
	fn(n)
	switch n := n.(type) {
	case *parse.ListNode:
		for _, c := range n.Nodes {
			walkNodes(c, fn)
		}
	case *parse.ActionNode:
		walkNodes(n.Pipe, fn)
	case *parse.PipeNode:
		for _, c := range n.Cmds {
			walkNodes(c, fn)
		}
	case *parse.CommandNode:
		for _, a := range n.Args {
			walkNodes(a, fn)
		}
	case *parse.ChainNode:
		walkNodes(n.Node, fn)
	case *parse.IfNode:
		walkBranch(&n.BranchNode, fn)
	case *parse.WithNode:
		walkBranch(&n.BranchNode, fn)
	case *parse.RangeNode:
		walkBranch(&n.BranchNode, fn)
	case *parse.TemplateNode:
		walkNodes(n.Pipe, fn)
	}
}

func walkBranch(b *parse.BranchNode, fn func(parse.Node)) {
	walkNodes(b.Pipe, fn)
	walkNodes(b.List, fn)
	if b.ElseList != nil {
		walkNodes(b.ElseList, fn)
	}
}

// The analysis.

// val is what the analysis knows of a value.
type val struct {
	t      reflect.Type // nil: not known
	data   bool         // read from the page data, or a server list (plans)
	lit    bool         // a constant, or a list the template makes
	str    string       // a string constant's text
	isStr  bool
	nilv   bool           // nil, or a missing entry: a loop over it makes no pass
	rec    map[string]val // the entries of a dict
	open   bool           // a dict with computed keys
	secret bool           // holds a secret, as far as the analysis can tell
}

// sig identifies what the analysis of a template depends on in its data,
// for the memo; "" when the value is too large to compare.
func (v val) sig() string {
	var b strings.Builder
	if !v.writeSig(&b) {
		return ""
	}
	return b.String()
}

func (v val) writeSig(b *strings.Builder) bool {
	if b.Len() > maxSig {
		return false
	}
	if v.t != nil {
		b.WriteString(v.t.PkgPath() + "." + v.t.String())
	}
	fmt.Fprintf(b, "|%t%t%t%t%t", v.data, v.lit, v.nilv, v.open, v.secret)
	if v.rec != nil {
		b.WriteString("{")
		for _, k := range sortedKeys(v.rec) {
			b.WriteString(strconv.Quote(k) + ":")
			if !v.rec[k].writeSig(b) {
				return false
			}
			b.WriteString(",")
		}
		b.WriteString("}")
	}
	return true
}

// sum is what the analysis learns of a part of a template.
type sum struct {
	must      bool  // every execution writes at least once
	credit    bool  // every execution renders shared/footer-meta with the page data
	cost      int64 // steps, calls expanded and loops counted once
	silentAny bool  // holds a loop whose passes may write nothing
	theme     bool  // holds the theme's code
	scans     bool  // calls a function that reads a whole text
	brk       bool  // may break or continue the loop it is in
}

func (s *sum) add(o sum) {
	s.cost = min(s.cost+o.cost, 1<<50)
	s.silentAny = s.silentAny || o.silentAny
	s.theme = s.theme || o.theme
	s.scans = s.scans || o.scans
	s.brk = s.brk || o.brk
}

type loc struct {
	file string
	line int
}

// pageCheck analyses one page's template set.
type pageCheck struct {
	c     *checker
	set   *htmltemplate.Template
	own   map[string]string // template name -> the theme file that defines it
	memo  map[string]*sum
	stack []string
	runs  int // templates analysed
}

func (pc *pageCheck) call(name string, dot val, site loc, footerBuilt bool, from *walker, n parse.Node) sum {
	tmpl := pc.set.Lookup(name)
	if tmpl == nil || tmpl.Tree == nil {
		if from != nil {
			from.report("template.missing", n, name)
		}
		return sum{}
	}
	file, isTheme := pc.own[name]
	if i := slices.Index(pc.stack, name); i >= 0 {
		cycle := append(slices.Clone(pc.stack[i:]), name)
		if slices.ContainsFunc(cycle, func(x string) bool { _, ok := pc.own[x]; return ok }) && from != nil {
			from.report("template.recursion", n, name, strings.Join(cycle, " → "))
		}
		return sum{} // a recursion of the shared templates (comment replies) follows the data
	}
	sig := dot.sig()
	if pc.runs++; sig == "" || pc.runs > maxRuns {
		if from != nil {
			from.report("template.complex", n)
		}
		return sum{}
	}
	key := name + "\x00" + sig + "\x00" + strconv.FormatBool(footerBuilt)
	if s, ok := pc.memo[key]; ok {
		return *s
	}
	pc.stack = append(pc.stack, name)
	w := &walker{pc: pc, name: name, tree: tmpl.Tree, theme: isTheme, file: file, dot: dot, dollar: dot, site: site, footerBuilt: footerBuilt}
	w.assigned = map[string]bool{}
	walkNodes(tmpl.Tree.Root, func(n parse.Node) {
		if p, ok := n.(*parse.PipeNode); ok && p.IsAssign {
			for _, d := range p.Decl {
				w.assigned[d.Ident[0]] = true
			}
		}
	})
	s := w.list(tmpl.Tree.Root)
	s.theme = s.theme || isTheme
	pc.stack = pc.stack[:len(pc.stack)-1]
	pc.memo[key] = &s
	return s
}

// walker analyses one template called with one kind of data.
type walker struct {
	pc          *pageCheck
	name        string
	tree        *parse.Tree
	theme       bool
	file        string
	dot, dollar val
	vars        []scoped
	assigned    map[string]bool // variables the template reassigns
	site        loc             // the theme's call this shared template runs under
	footerBuilt bool            // inside {{if .BlocksFooter}}
}

type scoped struct {
	name string
	v    val
}

func (w *walker) where(n parse.Node) loc {
	if w.theme {
		return loc{w.file, lineOf(w.tree, n)}
	}
	return w.site
}

func (w *walker) report(code string, n parse.Node, args ...any) {
	l := w.where(n)
	w.pc.c.report(Problem{Code: code, File: l.file, Line: l.line, Args: args})
}

// desc is how the template wrote a node, for messages.
func desc(n parse.Node) string {
	s := n.String()
	if len(s) > 80 {
		s = s[:80] + "…"
	}
	return s
}

func (w *walker) isPage(v val) bool {
	return v.data && !v.nilv && v.t == w.pc.c.pageType
}

func (w *walker) list(l *parse.ListNode) sum {
	var s sum
	if l == nil {
		return s
	}
	broke := false
	for _, n := range l.Nodes {
		ns := w.node(n)
		if !broke {
			s.must = s.must || ns.must
			s.credit = s.credit || ns.credit
		}
		broke = broke || ns.brk
		s.add(ns)
	}
	return s
}

func (w *walker) node(n parse.Node) sum {
	switch n := n.(type) {
	case *parse.TextNode:
		return sum{must: len(n.Text) > 0, cost: 1}
	case *parse.ActionNode:
		v, s := w.pipe(n.Pipe, true)
		s.cost++
		if len(n.Pipe.Decl) == 0 {
			s.must = true
			if v.secret || w.pc.c.hasSecret(v.t) {
				w.report("template.secret-print", n, desc(n.Pipe))
			}
		}
		return s
	case *parse.IfNode:
		return w.branch(&n.BranchNode, false)
	case *parse.WithNode:
		return w.branch(&n.BranchNode, true)
	case *parse.RangeNode:
		return w.rangeNode(n)
	case *parse.TemplateNode:
		return w.template(n)
	case *parse.BreakNode, *parse.ContinueNode:
		return sum{brk: true, cost: 1}
	case *parse.ListNode:
		return w.list(n)
	case *parse.CommentNode:
		return sum{}
	}
	return sum{cost: 1}
}

func (w *walker) branch(b *parse.BranchNode, with bool) sum {
	mark := len(w.vars)
	built := w.footerBuilt
	if w.blocksFooter(b.Pipe) {
		w.footerBuilt = true
	}
	v, s := w.pipe(b.Pipe, true)
	dot := w.dot
	if with {
		w.dot = v
	}
	ts := w.list(b.List)
	w.dot, w.footerBuilt = dot, built
	var es sum
	if b.ElseList != nil {
		es = w.list(b.ElseList)
	}
	w.vars = w.vars[:mark]
	s.add(ts)
	s.add(es)
	s.cost++
	s.must = b.ElseList != nil && ts.must && es.must
	s.credit = b.ElseList != nil && ts.credit && es.credit
	return s
}

// blocksFooter reports whether a condition is .BlocksFooter of the page
// data: shared/blocks-footer then renders the credit of the page builder's
// footer (internal/builder always gives it one).
func (w *walker) blocksFooter(p *parse.PipeNode) bool {
	if p == nil || len(p.Decl) > 0 || len(p.Cmds) != 1 || len(p.Cmds[0].Args) != 1 {
		return false
	}
	switch a := p.Cmds[0].Args[0].(type) {
	case *parse.FieldNode:
		return len(a.Ident) == 1 && a.Ident[0] == "BlocksFooter" && w.isPage(w.dot)
	case *parse.VariableNode:
		return len(a.Ident) == 2 && a.Ident[0] == "$" && a.Ident[1] == "BlocksFooter" && w.isPage(w.dollar)
	}
	return false
}

func (w *walker) rangeNode(n *parse.RangeNode) sum {
	mark := len(w.vars)
	v, s := w.pipe(n.Pipe, false)
	if ok := rangeable(v); !ok {
		if w.theme {
			w.report("template.range", n, desc(n.Pipe))
		} else {
			w.report("template.range-shared", n, w.name, desc(n.Pipe))
		}
	}
	key, elem := w.elemOf(v)
	switch d := n.Pipe.Decl; len(d) {
	case 1:
		w.declare(d[0].Ident[0], elem)
	case 2:
		w.declare(d[0].Ident[0], key)
		w.declare(d[1].Ident[0], elem)
	}
	dot := w.dot
	w.dot = elem
	bs := w.list(n.List)
	w.dot = dot
	var es sum
	if n.ElseList != nil {
		es = w.list(n.ElseList)
	}
	w.vars = w.vars[:mark]

	silent := !bs.must || bs.brk
	if silent && (w.theme || bs.theme) {
		where := "range " + desc(n.Pipe)
		if bs.silentAny {
			w.report("template.silent-loops", n, where)
		}
		if bs.scans || bs.cost > maxSilentBody {
			w.report("template.silent-cost", n, where, maxSilentBody)
		}
	}
	res := sum{cost: 1}
	res.add(s)
	res.add(bs)
	res.add(es)
	res.brk = es.brk // a break in the body ends this loop
	res.silentAny = res.silentAny || silent
	res.must = n.ElseList != nil && bs.must && !bs.brk && es.must
	res.credit = n.ElseList != nil && bs.credit && !bs.brk && es.credit
	return res
}

// rangeable reports whether a loop over v is bounded by the page data.
func rangeable(v val) bool {
	if v.nilv {
		return true
	}
	if !v.data || v.lit || v.t == nil {
		return false
	}
	t := v.t
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Slice, reflect.Array, reflect.Map:
		return true
	}
	return false
}

func (w *walker) elemOf(v val) (key, elem val) {
	t := v.t
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil {
		return val{}, val{secret: v.secret}
	}
	switch t.Kind() {
	case reflect.Slice, reflect.Array:
		return val{t: intType}, val{t: t.Elem(), data: v.data, secret: w.pc.c.hasSecret(t.Elem())}
	case reflect.Map:
		return val{t: t.Key(), data: v.data}, val{t: t.Elem(), data: v.data, secret: w.pc.c.hasSecret(t.Elem())}
	}
	return val{}, val{}
}

func (w *walker) template(n *parse.TemplateNode) sum {
	arg, s := val{nilv: true}, sum{}
	if n.Pipe != nil {
		arg, s = w.pipe(n.Pipe, false)
	}
	page := w.isPage(arg)
	if w.theme && n.Name == "shared/footer-meta" && !page {
		w.report("template.credit-data", n)
	}
	site := w.site
	if w.theme {
		site = w.where(n)
	}
	cs := w.pc.call(n.Name, arg, site, w.footerBuilt, w, n)
	s.add(cs)
	s.cost++
	s.brk = false
	s.must = cs.must
	s.credit = cs.credit || n.Name == "shared/footer-meta" && page || n.Name == "shared/blocks-footer" && page && w.footerBuilt
	return s
}

func (w *walker) declare(name string, v val) {
	if w.assigned[name] {
		v.data = false // a variable the template reassigns is not page data
	}
	w.vars = append(w.vars, scoped{name, v})
}

func (w *walker) assign(name string, v val) {
	for i := len(w.vars) - 1; i >= 0; i-- {
		if w.vars[i].name == name {
			old := w.vars[i].v
			old.data = false
			old.secret = old.secret || v.secret
			if old.t != v.t {
				old.t = nil
			}
			w.vars[i].v = old
			return
		}
	}
}

func (w *walker) lookup(name string) val {
	if name == "$" {
		return w.dollar
	}
	for i := len(w.vars) - 1; i >= 0; i-- {
		if w.vars[i].name == name {
			return w.vars[i].v
		}
	}
	return val{}
}

func (w *walker) pipe(p *parse.PipeNode, declare bool) (val, sum) {
	if p == nil {
		return val{nilv: true}, sum{}
	}
	var v val
	var s sum
	for i, cmd := range p.Cmds {
		var final *val
		if i > 0 {
			prev := v
			final = &prev
		}
		cv, cs := w.command(cmd, final)
		v = cv
		s.add(cs)
	}
	if len(p.Decl) > 0 {
		if p.IsAssign {
			for _, d := range p.Decl {
				w.assign(d.Ident[0], v)
			}
		} else if declare {
			for _, d := range p.Decl {
				w.declare(d.Ident[0], v)
			}
		}
	}
	return v, s
}

func (w *walker) command(cmd *parse.CommandNode, final *val) (val, sum) {
	if len(cmd.Args) == 0 {
		return val{}, sum{}
	}
	args := cmd.Args[1:]
	switch n := cmd.Args[0].(type) {
	case *parse.FieldNode:
		return w.fields(w.dot, n.Ident, args, final, n)
	case *parse.ChainNode:
		recv, s := w.arg(n.Node)
		v, s2 := w.fields(recv, n.Field, args, final, n)
		s.add(s2)
		return v, s
	case *parse.VariableNode:
		v := w.lookup(n.Ident[0])
		if len(n.Ident) == 1 {
			return v, sum{cost: 1}
		}
		return w.fields(v, n.Ident[1:], args, final, n)
	case *parse.IdentifierNode:
		return w.function(n.Ident, args, final, n)
	}
	return w.arg(cmd.Args[0])
}

// arg evaluates an argument.
func (w *walker) arg(n parse.Node) (val, sum) {
	switch n := n.(type) {
	case *parse.FieldNode:
		return w.fields(w.dot, n.Ident, nil, nil, n)
	case *parse.ChainNode:
		recv, s := w.arg(n.Node)
		v, s2 := w.fields(recv, n.Field, nil, nil, n)
		s.add(s2)
		return v, s
	case *parse.VariableNode:
		v := w.lookup(n.Ident[0])
		if len(n.Ident) == 1 {
			return v, sum{cost: 1}
		}
		return w.fields(v, n.Ident[1:], nil, nil, n)
	case *parse.IdentifierNode:
		return w.function(n.Ident, nil, nil, n)
	case *parse.PipeNode:
		return w.pipe(n, false)
	case *parse.DotNode:
		return w.dot, sum{cost: 1}
	case *parse.NilNode:
		return val{nilv: true}, sum{cost: 1}
	case *parse.StringNode:
		return val{t: stringType, lit: true, isStr: true, str: n.Text}, sum{cost: 1}
	case *parse.NumberNode:
		t := intType
		if !n.IsInt {
			t = floatType
		}
		return val{t: t, lit: true}, sum{cost: 1}
	case *parse.BoolNode:
		return val{t: boolType, lit: true}, sum{cost: 1}
	}
	return val{}, sum{cost: 1}
}

func (w *walker) args(nodes []parse.Node, final *val) ([]val, sum) {
	var s sum
	out := make([]val, 0, len(nodes)+1)
	for _, a := range nodes {
		v, as := w.arg(a)
		out = append(out, v)
		s.add(as)
	}
	if final != nil {
		out = append(out, *final)
	}
	return out, s
}

// costly reports whether passing v makes a function read a text of the
// page data, whose size the template does not bound.
func costly(v val) bool {
	if v.lit || v.nilv {
		return false
	}
	if v.t == nil {
		return true
	}
	switch v.t.Kind() {
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
		return false
	}
	return v.t != timeType && v.t != reflect.PointerTo(timeType)
}

func (s *sum) scan(args []val) {
	for _, a := range args {
		if costly(a) {
			s.cost += scanCost
			s.scans = true
		}
	}
}

// fields follows .A.B.C from recv; the last one may be a method called
// with args.
func (w *walker) fields(recv val, idents []string, argNodes []parse.Node, final *val, n parse.Node) (val, sum) {
	var s sum
	v := recv
	for i, name := range idents {
		var args []val
		if i == len(idents)-1 && (len(argNodes) > 0 || final != nil) {
			var as sum
			args, as = w.args(argNodes, final)
			s.add(as)
		}
		v = w.field(v, name, args, n, &s)
	}
	return v, s
}

func (w *walker) field(recv val, name string, args []val, n parse.Node, s *sum) val {
	s.cost++
	if w.theme && secretFields[name] {
		w.report("template.secret", n, "."+name)
	}
	if w.theme && deniedMembers[name] {
		w.report("template.member-denied", n, "."+name)
	}
	if recv.rec != nil {
		if e, ok := recv.rec[name]; ok {
			return e
		}
		if recv.open {
			return val{secret: recv.secret}
		}
		return val{nilv: true}
	}
	if recv.nilv {
		return val{nilv: true}
	}
	t := recv.t
	if t == nil {
		return val{data: recv.data, secret: recv.secret}
	}
	if m, ok := methodOf(t, name); ok {
		s.scan(args)
		var out reflect.Type
		if m.Type.NumOut() > 0 {
			out = m.Type.Out(0)
		}
		return val{t: out, data: recv.data, secret: w.pc.c.hasSecret(out)}
	}
	it := t
	for it.Kind() == reflect.Pointer {
		it = it.Elem()
	}
	switch it.Kind() {
	case reflect.Struct:
		if f, ok := it.FieldByName(name); ok && f.IsExported() {
			return val{t: f.Type, data: recv.data, secret: w.pc.c.hasSecret(f.Type)}
		}
	case reflect.Map:
		if it.Key().Kind() == reflect.String {
			return val{t: it.Elem(), data: recv.data, secret: w.pc.c.hasSecret(it.Elem())}
		}
	case reflect.Interface:
		return val{data: recv.data, secret: recv.secret}
	}
	if w.theme {
		w.report("template.field", n, it.String(), name)
	}
	return val{}
}

func methodOf(t reflect.Type, name string) (reflect.Method, bool) {
	if m, ok := t.MethodByName(name); ok && m.IsExported() {
		return m, true
	}
	if t.Kind() != reflect.Pointer && t.Kind() != reflect.Interface {
		if m, ok := reflect.PointerTo(t).MethodByName(name); ok && m.IsExported() {
			return m, true
		}
	}
	return reflect.Method{}, false
}

func (w *walker) function(name string, argNodes []parse.Node, final *val, n parse.Node) (val, sum) {
	args, s := w.args(argNodes, final)
	s.cost++
	if w.theme && !themeFuncs[name] {
		w.report("template.func-denied", n, name)
	}
	anySecret := slices.ContainsFunc(args, func(a val) bool { return a.secret || w.pc.c.hasSecret(a.t) })
	hasLit := slices.ContainsFunc(args, func(a val) bool { return a.lit || a.nilv })
	format := func(i int) {
		if !w.theme || i >= len(args) {
			return
		}
		if !args[i].lit || !args[i].isStr {
			w.report("template.format-literal", n, name)
		} else if !boundedFormat(args[i].str) {
			w.report("format.width", n, args[i].str)
		}
	}
	switch name {
	case "index":
		if len(args) == 0 {
			return val{}, s
		}
		v := args[0]
		for _, k := range args[1:] {
			if costly(k) {
				s.cost += scanCost
				s.scans = true
			}
			if v.rec != nil && k.isStr && k.lit {
				if e, ok := v.rec[k.str]; ok {
					v = e
				} else {
					v = val{nilv: true}
				}
				continue
			}
			_, elem := w.elemOf(v)
			if t := v.t; t != nil && t.Kind() == reflect.String {
				elem = val{t: reflect.TypeFor[uint8]()}
			}
			elem.secret = elem.secret || v.secret
			v = elem
		}
		return v, s
	case "slice":
		if len(args) == 0 {
			return val{}, s
		}
		v := args[0]
		v.rec = nil
		return v, s
	case "dict":
		v := val{t: anyMap, rec: map[string]val{}}
		for i := 0; i+1 < len(args); i += 2 {
			if args[i].lit && args[i].isStr {
				v.rec[args[i].str] = args[i+1]
			} else {
				v.open = true
			}
			v.secret = v.secret || args[i+1].secret || w.pc.c.hasSecret(args[i+1].t)
		}
		return v, s
	case "list":
		return val{t: anySlice, lit: true, secret: anySecret}, s
	case "and", "or":
		if len(args) == 0 {
			return val{}, s
		}
		v := args[0]
		for _, a := range args[1:] {
			if a.t != v.t || !a.data {
				v.t, v.data = nil, false
			}
			v.secret = v.secret || a.secret
		}
		v.rec, v.lit = nil, false
		return v, s
	case "not":
		return val{t: boolType}, s
	case "eq", "ne", "lt", "le", "gt", "ge":
		if !hasLit {
			s.scan(args)
		}
		return val{t: boolType}, s
	case "len":
		return val{t: intType}, s
	case "printf":
		format(0)
	case "T":
		if len(args) > 1 {
			format(0)
		}
	case "plural":
		format(1)
		format(2)
	}
	if anySecret {
		w.report("template.secret-print", n, name)
	}
	s.scan(args)
	out := val{t: w.pc.c.results[name]}
	switch name {
	case "print", "printf", "println", "html", "js", "urlquery":
		out.t = stringType
	case "plans":
		out.data = true // the network's plans, a short list
	}
	return out, s
}

// The HTML of a theme's templates.

// skeleton is the text of a template with each action replaced: by a
// placeholder when it writes, by nothing otherwise, so that the HTML
// tokenizer sees the tags and attributes the text spells out, branches
// included. With a set, the theme's own templates are inlined where they
// are called, so that a tag cannot be split across templates either.
// Inlining stops at maxSkeleton bytes or maxSkeletonNodes nodes, as it
// does for recursion: a call not inlined is a placeholder, which the
// checks treat as a template that may write anything. Without the node
// bound, templates that write nothing and call each other ten times over
// would take the walk through 10^n nodes before the step count refuses
// them.
type skeleton struct {
	b     strings.Builder
	spans []span
	set   *htmltemplate.Template
	own   map[string]string
	stack []string
	tree  *parse.Tree
	file  string
	nodes int
}

type span struct {
	off  int
	node parse.Node
	tree *parse.Tree
	file string
}

// The skeleton's placeholders: an action that writes, a template that is
// not inlined, and an action that writes a path of the site
// ({{link "/search"}}, a path that cannot start with "//").
const (
	writeMark    = "\uE000"
	templateMark = "\uE001"
	pathMark     = "\uE002"
	maxSkeleton  = 4 << 20
	// maxSkeletonNodes is ten times maxSteps: a page within the step
	// count is inlined in full.
	maxSkeletonNodes = 10 * maxSteps
)

var marks = []string{writeMark, templateMark, pathMark}

func hasMark(s string) bool {
	for _, m := range marks {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// sitePath reports whether an action writes a path of the site: link
// with a path in quotes that starts with one "/" and something else.
func sitePath(p *parse.PipeNode) bool {
	if len(p.Cmds) != 1 || len(p.Cmds[0].Args) != 2 {
		return false
	}
	fn, ok := p.Cmds[0].Args[0].(*parse.IdentifierNode)
	arg, isStr := p.Cmds[0].Args[1].(*parse.StringNode)
	if !ok || !isStr || fn.Ident != "link" || len(arg.Text) < 2 || arg.Text[0] != '/' {
		return false
	}
	return arg.Text[1] != '/' && arg.Text[1] != '\\' && !strings.ContainsAny(arg.Text, "\t\n\r")
}

// siteAction reports whether a form's action, as the template writes it,
// is a path of the site, as the browser reads it: without tabs and line
// breaks, and the spaces and controls around it.
func siteAction(v string) bool {
	v = strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, v)
	v = strings.TrimFunc(v, func(r rune) bool { return r <= ' ' })
	switch {
	case v == "", strings.HasPrefix(v, pathMark):
		return true
	case strings.HasPrefix(v, "/"):
		rest := v[1:]
		return rest == "" || rest[0] != '/' && rest[0] != '\\' && !strings.HasPrefix(rest, writeMark) &&
			!strings.HasPrefix(rest, templateMark) && !strings.HasPrefix(rest, pathMark)
	}
	return false
}

// formBearing reports whether the template name of set writes a form or a
// form control, itself or through the templates it calls.
func formBearing(set *htmltemplate.Template, name string, memo map[string]bool) bool {
	if v, ok := memo[name]; ok {
		return v
	}
	memo[name] = false // cycles
	tmpl := set.Lookup(name)
	if tmpl == nil || tmpl.Tree == nil {
		return false
	}
	found := false
	walkNodes(tmpl.Tree.Root, func(n parse.Node) {
		switch n := n.(type) {
		case *parse.TextNode:
			lower := strings.ToLower(string(n.Text))
			found = found || slices.ContainsFunc(formTags, func(t string) bool { return strings.Contains(lower, t) })
		case *parse.TemplateNode:
			found = found || formBearing(set, n.Name, memo)
		}
	})
	memo[name] = found
	return found
}

func (sk *skeleton) mark(n parse.Node, s string) {
	sk.spans = append(sk.spans, span{sk.b.Len(), n, sk.tree, sk.file})
	sk.b.WriteString(s)
}

func (sk *skeleton) walk(n parse.Node) {
	sk.nodes++
	switch n := n.(type) {
	case *parse.ListNode:
		if n == nil {
			return
		}
		for _, c := range n.Nodes {
			sk.walk(c)
		}
	case *parse.TextNode:
		sk.mark(n, string(n.Text))
	case *parse.ActionNode:
		if len(n.Pipe.Decl) == 0 {
			if sitePath(n.Pipe) {
				sk.mark(n, pathMark)
			} else {
				sk.mark(n, writeMark)
			}
		}
	case *parse.TemplateNode:
		if f, ok := sk.own[n.Name]; ok && sk.set != nil && len(sk.stack) < 30 && sk.b.Len() < maxSkeleton && sk.nodes < maxSkeletonNodes && !slices.Contains(sk.stack, n.Name) {
			if t := sk.set.Lookup(n.Name); t != nil && t.Tree != nil {
				tree, file := sk.tree, sk.file
				sk.stack = append(sk.stack, n.Name)
				sk.tree, sk.file = t.Tree, f
				sk.walk(t.Tree.Root)
				sk.tree, sk.file = tree, file
				sk.stack = sk.stack[:len(sk.stack)-1]
				return
			}
		}
		sk.mark(n, templateMark)
	case *parse.IfNode:
		sk.branch(&n.BranchNode)
	case *parse.WithNode:
		sk.branch(&n.BranchNode)
	case *parse.RangeNode:
		sk.branch(&n.BranchNode)
	}
}

func (sk *skeleton) branch(b *parse.BranchNode) {
	sk.walk(b.List)
	if b.ElseList != nil {
		sk.walk(b.ElseList)
	}
}

// at returns the file and line of the skeleton's offset off.
func (sk *skeleton) at(off int) (string, int) {
	i := sort.Search(len(sk.spans), func(i int) bool { return sk.spans[i].off > off }) - 1
	if i < 0 {
		return sk.file, lineOf(sk.tree, sk.tree.Root)
	}
	sp := sk.spans[i]
	line := lineOf(sp.tree, sp.node)
	if t, ok := sp.node.(*parse.TextNode); ok {
		end := min(off-sp.off, len(t.Text))
		line += strings.Count(string(t.Text[:end]), "\n")
	}
	return sp.file, line
}

// nodeAt returns the node that wrote the skeleton's offset off.
func (sk *skeleton) nodeAt(off int) parse.Node {
	i := sort.Search(len(sk.spans), func(i int) bool { return sk.spans[i].off > off }) - 1
	if i < 0 {
		return nil
	}
	return sk.spans[i].node
}

// checkHTML looks for what a theme's HTML cannot hold. The template
// text is checked file by file, and each page with the theme's templates
// inlined, where the shared templates a theme form holds are checked too.
func (c *checker) checkHTML(sk *skeleton) {
	text := sk.b.String()
	report := func(code string, off int, args ...any) {
		file, line := sk.at(off)
		c.report(Problem{Code: code, File: file, Line: line, Args: args})
	}
	for _, m := range marks {
		for _, mark := range []string{"<" + m, "</" + m} {
			if i := strings.Index(text, mark); i >= 0 {
				report("template.tag-name", i)
			}
		}
	}
	memo := map[string]bool{}
	forms, selects, svg := 0, 0, 0 // open in the theme's markup
	z := html.NewTokenizer(strings.NewReader(text))
	off := 0
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			return
		}
		start := off
		raw := string(z.Raw())
		off += len(raw)
		switch tt {
		case html.TextToken:
			// In a page, with the theme's templates inlined: the shared
			// templates called in a theme's form, select or svg.
			if sk.set == nil || forms == 0 && selects == 0 && svg == 0 {
				continue
			}
			for i := strings.Index(raw, templateMark); i >= 0; {
				at := start + i
				if tn, ok := sk.nodeAt(at).(*parse.TemplateNode); ok {
					switch {
					case svg > 0:
						report("template.svg", at, fmt.Sprintf("{{template %q}}", tn.Name))
					case formBearing(sk.set, tn.Name, memo):
						report("template.form-nested", at, tn.Name)
					}
				}
				next := strings.Index(raw[i+len(templateMark):], templateMark)
				if next < 0 {
					break
				}
				i += len(templateMark) + next
			}
			continue
		case html.EndTagToken:
			name, _ := z.TagName()
			switch tag := string(name); {
			case svg > 0 && tag == "svg":
				svg--
			case svg > 0 && !svgElements[tag]:
				report("template.svg", start, "</"+tag+">")
			case tag == "form" && selects == 0 && forms > 0:
				forms-- // a browser ignores </form> inside a <select>
			case tag == "select" && selects > 0:
				selects--
			}
			continue
		case html.StartTagToken, html.SelfClosingTagToken:
		default:
			continue
		}
		name, more := z.TagName()
		tag := string(name)
		switch code := refusedElements[tag]; {
		case code == "template.element":
			report(code, start, tag)
		case code != "":
			report(code, start)
		case svg > 0 && !svgElements[tag]:
			report("template.svg", start, "<"+tag+">")
		}
		switch tag {
		case "svg":
			if tt == html.StartTagToken {
				svg++
			}
		case "form": // <form/> opens a form too
			forms++
		case "select":
			selects++
		}
		for more {
			var k, v []byte
			k, v, more = z.TagAttr()
			key, val := string(k), string(v)
			switch {
			case hasMark(key):
				// html/template lets an action write a harmless attribute
				// name, such as "method", which would undo the form check.
				report("template.attr-name", start)
			case strings.HasPrefix(key, "on"):
				report("template.handler", start, key)
			case key == "style":
				report("template.style", start)
			case tag == "meta" && key == "http-equiv":
				report("template.http-equiv", start)
			case tag == "meta" && key == "name":
				if hasMark(val) {
					report("template.meta-name", start)
				} else if strings.EqualFold(strings.TrimSpace(val), "referrer") {
					report("template.referrer", start)
				}
			case key == "referrerpolicy":
				report("template.referrer", start)
			case key == "rel":
				if hasMark(val) || slices.ContainsFunc(strings.Fields(val), func(t string) bool { return strings.EqualFold(t, "opener") }) {
					report("template.rel", start)
				}
			case key == "data-form":
				report("template.token", start, "data-form")
			case formAttrs[key]:
				report("template.form-attr", start, key)
			case tag == "form" && key == "method" && !strings.EqualFold(val, "get"):
				report("template.form-post", start)
			case tag == "form" && key == "action" && !siteAction(val):
				report("template.form-action", start)
			}
		}
	}
}

// probe runs every page once, without page data, through a slot as
// RenderContext does: within the same bounds (bounds.go), all pages within
// one RenderTimeout. Running escapes the page as html/template does before
// its first run: an ambiguous context is an error, and a textOnly template
// called inside an attribute, a comment or an RCDATA element gets a
// mangled name ("shared/footer-meta$htmltemplate_…"). A page that goes
// over a bound without any data would go over it for every visitor.
func (c *checker) probe(t *Theme, owns map[string]map[string]string) {
	deadline := time.Now().Add(RenderTimeout)
	for _, page := range sortedKeys(t.pages) {
		s, err := t.newSlot(page, "en")
		if err != nil {
			c.report(Problem{Code: "template.parse", Args: []any{err.Error()}})
			continue
		}
		set := s.set
		s.g.start(context.Background(), deadline)
		err = set.ExecuteTemplate(&limitWriter{g: s.g, max: MaxRenderBytes}, "layout", nil)
		limit := s.g.err
		s.g.stop()
		if limit != nil {
			c.report(Problem{Code: "template.render-limit", File: "templates/layout.html", Args: []any{page}})
			if errors.Is(limit, errTimeout) {
				return // the next pages have no time left
			}
			continue
		}
		var he *htmltemplate.Error
		if errors.As(err, &he) && he.ErrorCode != htmltemplate.OK {
			file := owns[page][he.Name]
			line := 0
			if file != "" {
				line = he.Line
			}
			c.report(Problem{Code: "template.escape", File: file, Line: line, Args: []any{he.Description}})
			continue
		}
		for _, tmpl := range set.Templates() {
			if tmpl.Tree == nil {
				continue
			}
			file := owns[page][tmpl.Name()]
			walkNodes(tmpl.Tree.Root, func(n parse.Node) {
				tn, ok := n.(*parse.TemplateNode)
				if !ok {
					return
				}
				base, _, mangled := strings.Cut(tn.Name, "$htmltemplate_")
				if !mangled || !textOnly[base] {
					return
				}
				line := 0
				if file != "" {
					line = lineOf(tmpl.Tree, tn)
				}
				if base == "shared/footer-meta" {
					c.report(Problem{Code: "template.credit-context", File: file, Line: line})
				} else {
					c.report(Problem{Code: "template.context", File: file, Line: line, Args: []any{base}})
				}
			})
		}
	}
}
