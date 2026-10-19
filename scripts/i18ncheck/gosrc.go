package main

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// goScanner finds translation calls in Go source with go/ast. It does not type
// check: translators are recognised by shape.
//
//   - x.t(key, ...), x.tr(r, key, ...), x.tu(r, key, ...) and x.T(lang, key, ...)
//     are the server and catalog methods;
//   - a variable or parameter whose type is func(string, ...any) string is a
//     translator taking the key first;
//   - a function or method that passes one of its parameters as the key of a
//     translation call is a wrapper, and the literals given for that parameter
//     are keys too (found to a fixed point, so wrappers of wrappers work).
type goScanner struct {
	e      *Extractor
	root   string
	module string
	fset   *token.FileSet
	pkgs   map[string]*goPkg // by import path
	order  []string
	// wrappers maps a function or method name to the position of its key
	// parameter; methods "t", "tr", "tu" and "T" are the built-in translators.
	wrappers map[string]int
	depth    int // recursion guard of exprType
}

type goPkg struct {
	path     string
	files    []*goFile
	vars     map[string]ast.Expr // package-level var name → initialiser
	varTypes map[string]ast.Expr // package-level var name → declared type
	consts   map[string]ast.Expr // package-level const name → value
	types    map[string]*ast.TypeSpec
	funcs    map[string]*ast.FuncDecl
	methods  map[string]*ast.FuncDecl // "Recv.Name"
}

type goFile struct {
	rel         string
	f           *ast.File
	imports     map[string]string // local name → import path
	translators map[string]bool   // local names of func(string, ...any) string values
}

func newGoScanner(e *Extractor, root string) *goScanner {
	return &goScanner{e: e, root: root, module: modulePath(root), fset: token.NewFileSet(), pkgs: map[string]*goPkg{},
		wrappers: map[string]int{"t": 0, "tr": 1, "tu": 1, "T": 1}}
}

func modulePath(root string) string {
	f, err := os.Open(filepath.Join(root, "go.mod"))
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "module "); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// addTree parses every non-test Go file under dir.
func (g *goScanner) addTree(dir string) error {
	return filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); n == "testdata" || strings.HasPrefix(n, ".") || n == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(g.fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			// A file someone is editing: report it, keep going.
			g.e.Warnings = append(g.e.Warnings, rel(g.root, p)+": "+err.Error())
			if f == nil {
				return nil
			}
		}
		ip := g.module + "/" + filepath.ToSlash(rel(g.root, filepath.Dir(p)))
		pkg := g.pkgs[ip]
		if pkg == nil {
			pkg = &goPkg{path: ip, vars: map[string]ast.Expr{}, varTypes: map[string]ast.Expr{}, consts: map[string]ast.Expr{},
				types: map[string]*ast.TypeSpec{}, funcs: map[string]*ast.FuncDecl{}, methods: map[string]*ast.FuncDecl{}}
			g.pkgs[ip] = pkg
			g.order = append(g.order, ip)
		}
		gf := &goFile{rel: rel(g.root, p), f: f, imports: map[string]string{}, translators: map[string]bool{}}
		for _, im := range f.Imports {
			path, _ := strconv.Unquote(im.Path.Value)
			name := path[strings.LastIndex(path, "/")+1:]
			if im.Name != nil {
				name = im.Name.Name
			}
			gf.imports[name] = path
		}
		for _, decl := range f.Decls {
			if fd, ok := decl.(*ast.FuncDecl); ok {
				if fd.Recv == nil {
					pkg.funcs[fd.Name.Name] = fd
				} else if len(fd.Recv.List) == 1 {
					if id, ok := deref(fd.Recv.List[0].Type).(*ast.Ident); ok {
						pkg.methods[id.Name+"."+fd.Name.Name] = fd
					}
				}
				continue
			}
			gd, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gd.Specs {
				switch sp := spec.(type) {
				case *ast.TypeSpec:
					pkg.types[sp.Name.Name] = sp
				case *ast.ValueSpec:
					for i, n := range sp.Names {
						if gd.Tok == token.VAR && sp.Type != nil {
							pkg.varTypes[n.Name] = sp.Type
						}
						if i >= len(sp.Values) {
							continue
						}
						if gd.Tok == token.VAR {
							pkg.vars[n.Name] = sp.Values[i]
						} else {
							pkg.consts[n.Name] = sp.Values[i]
						}
					}
				}
			}
		}
		findTranslators(gf)
		pkg.files = append(pkg.files, gf)
		return nil
	})
}

// findTranslators records the local names bound to func(string, ...any) string.
func findTranslators(gf *goFile) {
	ast.Inspect(gf.f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncType:
			if n.Params == nil {
				return true
			}
			for _, fld := range n.Params.List {
				if ft, ok := fld.Type.(*ast.FuncType); ok && isTranslatorSig(ft) {
					for _, nm := range fld.Names {
						gf.translators[nm.Name] = true
					}
				}
			}
		case *ast.AssignStmt:
			for i, rhs := range n.Rhs {
				if i < len(n.Lhs) && isTranslatorValue(rhs) {
					if id, ok := n.Lhs[i].(*ast.Ident); ok {
						gf.translators[id.Name] = true
					}
				}
			}
		case *ast.ValueSpec:
			if ft, ok := n.Type.(*ast.FuncType); ok && isTranslatorSig(ft) {
				for _, nm := range n.Names {
					gf.translators[nm.Name] = true
				}
			}
			for i, v := range n.Values {
				if i < len(n.Names) && isTranslatorValue(v) {
					gf.translators[n.Names[i].Name] = true
				}
			}
		}
		return true
	})
}

func isTranslatorValue(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.FuncLit:
		return isTranslatorSig(e.Type)
	case *ast.TypeAssertExpr:
		ft, ok := e.Type.(*ast.FuncType)
		return ok && isTranslatorSig(ft)
	case *ast.SelectorExpr: // T := s.t
		return e.Sel.Name == "t"
	}
	return false
}

// isTranslatorSig reports whether ft is func(string, ...any) string.
func isTranslatorSig(ft *ast.FuncType) bool {
	var params []ast.Expr
	for _, f := range ft.Params.List {
		n := max(len(f.Names), 1)
		for range n {
			params = append(params, f.Type)
		}
	}
	if len(params) != 2 || !isIdent(params[0], "string") {
		return false
	}
	el, ok := params[1].(*ast.Ellipsis)
	if !ok || !(isIdent(el.Elt, "any") || isEmptyInterface(el.Elt)) {
		return false
	}
	if ft.Results == nil || len(ft.Results.List) != 1 || len(ft.Results.List[0].Names) > 1 {
		return false
	}
	return isIdent(ft.Results.List[0].Type, "string")
}

func isIdent(e ast.Expr, name string) bool { id, ok := e.(*ast.Ident); return ok && id.Name == name }

func isEmptyInterface(e ast.Expr) bool {
	it, ok := e.(*ast.InterfaceType)
	return ok && (it.Methods == nil || len(it.Methods.List) == 0)
}

// scan walks every file twice: first to find wrapper functions, then to
// extract the keys.
func (g *goScanner) scan() {
	for range 5 { // wrappers of wrappers, to a fixed point
		before := len(g.wrappers)
		g.walk(false)
		if len(g.wrappers) == before {
			break
		}
	}
	g.walk(true)
}

func (g *goScanner) walk(extract bool) {
	for _, ip := range g.order {
		pkg := g.pkgs[ip]
		for _, gf := range pkg.files {
			var stack []ast.Node
			ast.Inspect(gf.f, func(n ast.Node) bool {
				if n == nil {
					stack = stack[:len(stack)-1]
					return true
				}
				stack = append(stack, n)
				if call, ok := n.(*ast.CallExpr); ok {
					g.call(pkg, gf, call, stack, extract)
				}
				return true
			})
		}
	}
}

// keyArg returns the key argument of call when call is a translation call.
func (g *goScanner) keyArg(gf *goFile, call *ast.CallExpr) (ast.Expr, bool) {
	idx := -1
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		if i, ok := g.wrappers[fun.Sel.Name]; ok {
			idx = i
		}
	case *ast.Ident:
		if gf.translators[fun.Name] {
			idx = 0
		} else if i, ok := g.wrappers[fun.Name]; ok && !builtinMethod(fun.Name) {
			idx = i
		}
	}
	if idx < 0 || idx >= len(call.Args) {
		return nil, false
	}
	return call.Args[idx], true
}

func builtinMethod(name string) bool {
	return name == "t" || name == "tr" || name == "tu" || name == "T"
}

func (g *goScanner) call(pkg *goPkg, gf *goFile, call *ast.CallExpr, stack []ast.Node, extract bool) {
	arg, ok := g.keyArg(gf, call)
	if !ok {
		return
	}
	loc := g.loc(call.Pos())
	if s, ok := g.constString(pkg, gf, arg); ok {
		if extract {
			g.e.add(s, loc)
		}
		return
	}
	// A parameter of the enclosing function: that function is a wrapper.
	if id, ok := arg.(*ast.Ident); ok {
		if fn, idx := enclosingParam(stack, id.Name); fn != nil {
			if name := funcName(fn); name != "" {
				if _, known := g.wrappers[name]; !known {
					g.wrappers[name] = idx
				}
			}
			if extract {
				g.e.Dynamic = append(g.e.Dynamic, DynamicSite{Loc: loc, Expr: exprString(call), Kind: DynForward})
			}
			return
		}
	}
	if !extract {
		return
	}
	if themeCall(call) {
		g.e.Dynamic = append(g.e.Dynamic, DynamicSite{Loc: loc, Expr: exprString(call), Kind: DynTheme})
		return
	}
	if vals, ok := g.table(pkg, gf, arg, stack); ok {
		for _, v := range vals {
			g.e.addTable(v.s, v.loc)
		}
		g.e.Dynamic = append(g.e.Dynamic, DynamicSite{Loc: loc, Expr: exprString(call), Kind: DynTable})
		return
	}
	g.e.Dynamic = append(g.e.Dynamic, DynamicSite{Loc: loc, Expr: exprString(call), Kind: DynUnresolved})
}

func (g *goScanner) loc(p token.Pos) Loc {
	pos := g.fset.Position(p)
	return Loc{File: rel(g.root, pos.Filename), Line: pos.Line}
}

func exprString(e ast.Expr) string {
	s := types.ExprString(e)
	if len(s) > 110 {
		s = s[:107] + "…"
	}
	return s
}

// enclosingParam finds the innermost function in stack that declares a
// parameter called name, and that parameter's position.
func enclosingParam(stack []ast.Node, name string) (ast.Node, int) {
	for i := len(stack) - 1; i >= 0; i-- {
		var ft *ast.FuncType
		switch fn := stack[i].(type) {
		case *ast.FuncDecl:
			ft = fn.Type
		case *ast.FuncLit:
			ft = fn.Type
		default:
			continue
		}
		idx := 0
		for _, f := range ft.Params.List {
			if len(f.Names) == 0 {
				idx++
			}
			for _, n := range f.Names {
				if n.Name == name {
					return stack[i], idx
				}
				idx++
			}
		}
		// Closures can forward a parameter of an outer function; keep looking.
	}
	return nil, 0
}

// funcName is the name of a declared function or method; closures have none.
func funcName(fn ast.Node) string {
	if d, ok := fn.(*ast.FuncDecl); ok {
		return d.Name.Name
	}
	return ""
}

// constString evaluates a constant string expression: literals, parentheses,
// concatenation and package-level constants.
func (g *goScanner) constString(pkg *goPkg, gf *goFile, e ast.Expr) (string, bool) {
	return g.constStringDepth(pkg, gf, e, 0)
}

func (g *goScanner) constStringDepth(pkg *goPkg, gf *goFile, e ast.Expr, depth int) (string, bool) {
	if depth > 8 {
		return "", false
	}
	switch e := e.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(e.Value)
		return s, err == nil
	case *ast.ParenExpr:
		return g.constStringDepth(pkg, gf, e.X, depth+1)
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "", false
		}
		a, ok1 := g.constStringDepth(pkg, gf, e.X, depth+1)
		b, ok2 := g.constStringDepth(pkg, gf, e.Y, depth+1)
		return a + b, ok1 && ok2
	case *ast.Ident:
		if v, ok := pkg.consts[e.Name]; ok {
			return g.constStringDepth(pkg, gf, v, depth+1)
		}
	case *ast.SelectorExpr:
		if x, ok := e.X.(*ast.Ident); ok {
			if other := g.pkgs[gf.imports[x.Name]]; other != nil {
				if v, ok := other.consts[e.Sel.Name]; ok && len(other.files) > 0 {
					return g.constStringDepth(other, other.files[0], v, depth+1)
				}
			}
		}
	}
	return "", false
}

type tableValue struct {
	s   string
	loc Loc
}

// table resolves a dynamic key read from a literal table:
//
//	labels[k]                       labels is a package-level map literal
//	o.Label   in  for _, o := range opts / range []T{...}
//	label     in  for _, label := range []string{...}
func (g *goScanner) table(pkg *goPkg, gf *goFile, e ast.Expr, stack []ast.Node) ([]tableValue, bool) {
	switch e := e.(type) {
	case *ast.IndexExpr:
		lit, lpkg, lf := g.varLit(pkg, gf, e.X)
		if lit == nil {
			return nil, false
		}
		var out []tableValue
		for _, el := range lit.Elts {
			if kv, ok := el.(*ast.KeyValueExpr); ok {
				if s, ok := g.constString(lpkg, lf, kv.Value); ok {
					out = append(out, tableValue{s, g.loc(kv.Value.Pos())})
				}
			} else if s, ok := g.constString(lpkg, lf, el); ok { // slice or array of strings
				out = append(out, tableValue{s, g.loc(el.Pos())})
			}
		}
		return out, len(out) > 0
	case *ast.SelectorExpr:
		v, ok := e.X.(*ast.Ident)
		if !ok {
			return g.fieldKeys(pkg, gf, e, stack)
		}
		lit, lpkg, lf := g.rangeOver(pkg, gf, v.Name, stack)
		if lit == nil {
			return g.fieldKeys(pkg, gf, e, stack)
		}
		var out []tableValue
		for _, el := range lit.Elts {
			if kv, ok := el.(*ast.KeyValueExpr); ok { // map[K]struct{...}
				el = kv.Value
			}
			if u, ok := el.(*ast.UnaryExpr); ok && u.Op == token.AND {
				el = u.X
			}
			cl, ok := el.(*ast.CompositeLit)
			if !ok {
				continue
			}
			for _, f := range cl.Elts {
				kv, ok := f.(*ast.KeyValueExpr)
				if !ok || !isIdent(kv.Key, e.Sel.Name) {
					continue
				}
				if s, ok := g.constString(lpkg, lf, kv.Value); ok {
					out = append(out, tableValue{s, g.loc(kv.Value.Pos())})
				}
			}
		}
		return out, len(out) > 0
	case *ast.Ident:
		lit, lpkg, lf := g.rangeOver(pkg, gf, e.Name, stack)
		if lit == nil {
			return nil, false
		}
		var out []tableValue
		for _, el := range lit.Elts {
			if s, ok := g.constString(lpkg, lf, el); ok {
				out = append(out, tableValue{s, g.loc(el.Pos())})
			}
		}
		return out, len(out) > 0
	}
	return nil, false
}

// rangeOver finds the range statement in stack that binds name as its value
// and returns the composite literal it ranges over.
func (g *goScanner) rangeOver(pkg *goPkg, gf *goFile, name string, stack []ast.Node) (*ast.CompositeLit, *goPkg, *goFile) {
	for i := len(stack) - 1; i >= 0; i-- {
		rs, ok := stack[i].(*ast.RangeStmt)
		if !ok || !isIdent(rs.Value, name) {
			continue
		}
		if cl, ok := rs.X.(*ast.CompositeLit); ok {
			return cl, pkg, gf
		}
		return g.varLit(pkg, gf, rs.X)
	}
	return nil, nil, nil
}

// varLit resolves a package-level variable (local or imported) initialised
// with a composite literal.
func (g *goScanner) varLit(pkg *goPkg, gf *goFile, e ast.Expr) (*ast.CompositeLit, *goPkg, *goFile) {
	var (
		p    = pkg
		name string
	)
	switch e := e.(type) {
	case *ast.Ident:
		name = e.Name
	case *ast.SelectorExpr:
		x, ok := e.X.(*ast.Ident)
		if !ok {
			return nil, nil, nil
		}
		p = g.pkgs[gf.imports[x.Name]]
		name = e.Sel.Name
	default:
		return nil, nil, nil
	}
	if p == nil {
		return nil, nil, nil
	}
	v, ok := p.vars[name]
	if !ok {
		return nil, nil, nil
	}
	cl, ok := v.(*ast.CompositeLit)
	if !ok {
		return nil, nil, nil
	}
	// The file declaring the variable, for its own imports and constants.
	for _, f := range p.files {
		if f.f.Pos() <= cl.Pos() && cl.End() <= f.f.End() {
			return cl, p, f
		}
	}
	return cl, p, gf
}

// sortedWrappers lists the wrapper functions found, for -dynamic.
func (g *goScanner) sortedWrappers() []string {
	var out []string
	for k := range g.wrappers {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
