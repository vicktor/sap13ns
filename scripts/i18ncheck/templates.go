package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"text/template/parse"
)

// templateFuncs are the template functions that translate their string
// arguments, with the positions of those arguments (0 is the function name).
var templateFuncs = map[string][]int{
	"T":      {1},    // T "key" args...
	"plural": {2, 3}, // plural N "%d one" "%d many"
}

// templateTables are template functions that translate through a Go table;
// the Go scanner extracts the table, so their arguments are not keys.
var templateTables = map[string]bool{"roleLabel": true, "statusLabel": true}

// ScanTemplate extracts the keys of one template file. It parses the file
// with text/template/parse, so quoting, escapes, raw strings, comments and
// actions split over several lines are handled exactly as html/template does.
// When the file does not parse (for instance while someone is editing it), a
// regular expression fallback is used and a warning recorded.
func (e *Extractor) ScanTemplate(file, text string) {
	trees := map[string]*parse.Tree{}
	t := parse.New(file)
	t.Mode = parse.SkipFuncCheck
	if _, err := t.Parse(text, "", "", trees); err != nil {
		e.Warnings = append(e.Warnings, fmt.Sprintf("%s: %v (scanned with the fallback pattern)", file, err))
		e.scanTemplateFallback(file, text)
		return
	}
	seen := map[*parse.Tree]bool{}
	all := []*parse.Tree{t}
	for _, tr := range trees {
		all = append(all, tr)
	}
	for _, tr := range all {
		if seen[tr] || tr.Root == nil {
			continue
		}
		seen[tr] = true
		v := &tmplVisitor{e: e, tree: tr, file: file}
		v.node(tr.Root)
	}
}

type tmplVisitor struct {
	e    *Extractor
	tree *parse.Tree
	file string
}

func (v *tmplVisitor) loc(n parse.Node) Loc {
	loc, _ := v.tree.ErrorContext(n)
	// loc is "name:line:col".
	parts := strings.Split(loc, ":")
	line := 0
	if len(parts) >= 3 {
		line, _ = strconv.Atoi(parts[len(parts)-2])
	}
	return Loc{File: v.file, Line: line}
}

func (v *tmplVisitor) node(n parse.Node) {
	switch n := n.(type) {
	case nil:
	case *parse.ListNode:
		if n == nil {
			return
		}
		for _, c := range n.Nodes {
			v.node(c)
		}
	case *parse.ActionNode:
		v.pipe(n.Pipe)
	case *parse.IfNode:
		v.branch(&n.BranchNode)
	case *parse.RangeNode:
		v.branch(&n.BranchNode)
	case *parse.WithNode:
		v.branch(&n.BranchNode)
	case *parse.TemplateNode:
		v.pipe(n.Pipe)
	case *parse.PipeNode:
		v.pipe(n)
	case *parse.ChainNode:
		v.node(n.Node)
	}
}

func (v *tmplVisitor) branch(b *parse.BranchNode) {
	v.pipe(b.Pipe)
	if b.List != nil {
		v.node(b.List)
	}
	if b.ElseList != nil {
		v.node(b.ElseList)
	}
}

func (v *tmplVisitor) pipe(p *parse.PipeNode) {
	if p == nil {
		return
	}
	for i, cmd := range p.Cmds {
		v.command(cmd, i > 0)
	}
}

func (v *tmplVisitor) command(c *parse.CommandNode, piped bool) {
	if len(c.Args) == 0 {
		return
	}
	if id, ok := c.Args[0].(*parse.IdentifierNode); ok {
		if pos, ok := templateFuncs[id.Ident]; ok {
			for _, i := range pos {
				switch {
				case i < len(c.Args):
					if s, ok := c.Args[i].(*parse.StringNode); ok {
						v.e.add(s.Text, v.loc(s))
					} else {
						v.e.Dynamic = append(v.e.Dynamic, DynamicSite{Loc: v.loc(c), Expr: c.String(), Kind: DynUnresolved})
					}
				case piped && i == len(c.Args):
					// {{.Label | T}}: the key is the piped value.
					v.e.Dynamic = append(v.e.Dynamic, DynamicSite{Loc: v.loc(c), Expr: "… | " + c.String(), Kind: DynUnresolved})
				}
			}
		} else if templateTables[id.Ident] {
			v.e.Dynamic = append(v.e.Dynamic, DynamicSite{Loc: v.loc(c), Expr: c.String(), Kind: DynTable})
		}
	}
	for _, a := range c.Args {
		switch a := a.(type) {
		case *parse.PipeNode, *parse.ChainNode:
			v.node(a)
		}
	}
}

// fallbackRe matches T "..." and plural x "..." "..." with Go string quoting.
var (
	fallbackT      = regexp.MustCompile(`(?:\{\{-?\s*|\(\s*|\|\s*)T\s+("(?:[^"\\]|\\.)*"|` + "`[^`]*`" + `)`)
	fallbackPlural = regexp.MustCompile(`plural\s+[^\s"]+\s+("(?:[^"\\]|\\.)*")\s+("(?:[^"\\]|\\.)*")`)
)

func (e *Extractor) scanTemplateFallback(file, text string) {
	line := func(off int) int { return strings.Count(text[:off], "\n") + 1 }
	for _, m := range fallbackT.FindAllStringSubmatchIndex(text, -1) {
		if s, err := strconv.Unquote(text[m[2]:m[3]]); err == nil {
			e.add(s, Loc{file, line(m[2])})
		}
	}
	for _, m := range fallbackPlural.FindAllStringSubmatchIndex(text, -1) {
		for _, g := range [][2]int{{m[2], m[3]}, {m[4], m[5]}} {
			if s, err := strconv.Unquote(text[g[0]:g[1]]); err == nil {
				e.add(s, Loc{file, line(g[0])})
			}
		}
	}
}
