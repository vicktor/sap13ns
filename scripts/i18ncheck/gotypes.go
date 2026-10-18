package main

import (
	"go/ast"
	"go/token"
	"strconv"
)

// A light type inference, enough to follow keys read from struct fields when
// the struct does not come from a literal in sight:
//
//	for _, f := range t.Fields { s.t(f.Label) }
//	f, _ := t.Field(k); s.tr2(lang, f.Label)
//
// The type of the variable is worked out from range statements, short variable
// declarations, parameters, package variables, struct fields and the results
// of functions and methods. The keys are then the constants given for that
// field in every composite literal of the struct type, including literals
// built by small constructors (a parameter stored in the field) and by pair
// helpers such as choices("value", "Label", ...). A call inside
// "if f.I18n && ... {" only takes the literals that set I18n: true.

type typ struct {
	pkg  *goPkg
	gf   *goFile
	expr ast.Expr
}

func deref(e ast.Expr) ast.Expr {
	for {
		switch x := e.(type) {
		case *ast.StarExpr:
			e = x.X
		case *ast.ParenExpr:
			e = x.X
		default:
			return e
		}
	}
}

func (g *goScanner) fileOf(p *goPkg, pos token.Pos) *goFile {
	for _, f := range p.files {
		if f.f.Pos() <= pos && pos <= f.f.End() {
			return f
		}
	}
	if len(p.files) > 0 {
		return p.files[0]
	}
	return nil
}

// named resolves t to a type declared in the module.
func (g *goScanner) named(t typ) (*goPkg, string, *ast.TypeSpec) {
	if t.pkg == nil {
		return nil, "", nil
	}
	switch x := deref(t.expr).(type) {
	case *ast.Ident:
		if ts := t.pkg.types[x.Name]; ts != nil {
			return t.pkg, x.Name, ts
		}
	case *ast.SelectorExpr:
		if id, ok := x.X.(*ast.Ident); ok && t.gf != nil {
			if p := g.pkgs[t.gf.imports[id.Name]]; p != nil {
				if ts := p.types[x.Sel.Name]; ts != nil {
					return p, x.Sel.Name, ts
				}
			}
		}
	}
	return nil, "", nil
}

// underlying follows declared types to their definition.
func (g *goScanner) underlying(t typ) typ {
	for range 8 {
		p, _, ts := g.named(t)
		if ts == nil {
			break
		}
		t = typ{p, g.fileOf(p, ts.Pos()), ts.Type}
	}
	t.expr = deref(t.expr)
	return t
}

// elem is the type of the values a range over t yields.
func (g *goScanner) elem(t typ) (typ, bool) {
	u := g.underlying(t)
	switch x := u.expr.(type) {
	case *ast.ArrayType:
		return typ{u.pkg, u.gf, x.Elt}, true
	case *ast.MapType:
		return typ{u.pkg, u.gf, x.Value}, true
	case *ast.Ellipsis:
		return typ{u.pkg, u.gf, x.Elt}, true
	}
	return typ{}, false
}

func (g *goScanner) field(t typ, name string) (typ, bool) {
	u := g.underlying(t)
	st, ok := u.expr.(*ast.StructType)
	if !ok {
		return typ{}, false
	}
	for _, f := range st.Fields.List {
		for _, n := range f.Names {
			if n.Name == name {
				return typ{u.pkg, u.gf, f.Type}, true
			}
		}
	}
	return typ{}, false
}

func resultTypes(ft *ast.FuncType) []ast.Expr {
	var out []ast.Expr
	if ft.Results == nil {
		return nil
	}
	for _, f := range ft.Results.List {
		for range max(len(f.Names), 1) {
			out = append(out, f.Type)
		}
	}
	return out
}

func (g *goScanner) result(p *goPkg, gf *goFile, ft *ast.FuncType, i int) (typ, bool) {
	rs := resultTypes(ft)
	if i >= len(rs) {
		return typ{}, false
	}
	return typ{p, gf, rs[i]}, true
}

// exprType infers the type of e, at pos inside the nodes of stack.
func (g *goScanner) exprType(pkg *goPkg, gf *goFile, e ast.Expr, stack []ast.Node, pos token.Pos) (typ, bool) {
	if g.depth > 24 {
		return typ{}, false
	}
	g.depth++
	defer func() { g.depth-- }()
	switch x := e.(type) {
	case *ast.ParenExpr:
		return g.exprType(pkg, gf, x.X, stack, pos)
	case *ast.UnaryExpr:
		if x.Op == token.AND {
			return g.exprType(pkg, gf, x.X, stack, pos)
		}
	case *ast.StarExpr:
		return g.exprType(pkg, gf, x.X, stack, pos)
	case *ast.CompositeLit:
		if x.Type != nil {
			return typ{pkg, gf, x.Type}, true
		}
	case *ast.IndexExpr:
		if t, ok := g.exprType(pkg, gf, x.X, stack, pos); ok {
			return g.elem(t)
		}
	case *ast.Ident:
		if t, local := g.localType(pkg, gf, x.Name, stack, pos); local {
			return t, t.expr != nil
		}
		return g.pkgVarType(pkg, x.Name)
	case *ast.SelectorExpr:
		if id, ok := x.X.(*ast.Ident); ok {
			if _, local := g.localType(pkg, gf, id.Name, stack, pos); !local {
				if p := g.pkgs[gf.imports[id.Name]]; p != nil {
					return g.pkgVarType(p, x.Sel.Name)
				}
			}
		}
		if t, ok := g.exprType(pkg, gf, x.X, stack, pos); ok {
			return g.field(t, x.Sel.Name)
		}
	case *ast.CallExpr:
		return g.callResult(pkg, gf, x, 0, stack, pos)
	}
	return typ{}, false
}

// callResult is the type of result i of call.
func (g *goScanner) callResult(pkg *goPkg, gf *goFile, call *ast.CallExpr, i int, stack []ast.Node, pos token.Pos) (typ, bool) {
	switch f := call.Fun.(type) {
	case *ast.FuncLit:
		return g.result(pkg, gf, f.Type, i)
	case *ast.Ident:
		if fd := pkg.funcs[f.Name]; fd != nil {
			return g.result(pkg, g.fileOf(pkg, fd.Pos()), fd.Type, i)
		}
	case *ast.SelectorExpr:
		if id, ok := f.X.(*ast.Ident); ok {
			if _, local := g.localType(pkg, gf, id.Name, stack, pos); !local {
				if p := g.pkgs[gf.imports[id.Name]]; p != nil {
					if fd := p.funcs[f.Sel.Name]; fd != nil {
						return g.result(p, g.fileOf(p, fd.Pos()), fd.Type, i)
					}
					return typ{}, false
				}
			}
		}
		rt, ok := g.exprType(pkg, gf, f.X, stack, pos)
		if !ok {
			return typ{}, false
		}
		if p, name, _ := g.named(rt); p != nil {
			if fd := p.methods[name+"."+f.Sel.Name]; fd != nil {
				return g.result(p, g.fileOf(p, fd.Pos()), fd.Type, i)
			}
		}
	}
	return typ{}, false
}

func (g *goScanner) pkgVarType(p *goPkg, name string) (typ, bool) {
	if te := p.varTypes[name]; te != nil {
		return typ{p, g.fileOf(p, te.Pos()), te}, true
	}
	if v := p.vars[name]; v != nil {
		return g.exprType(p, g.fileOf(p, v.Pos()), v, nil, token.NoPos)
	}
	return typ{}, false
}

// localType finds the local declaration of name visible at pos. local is
// false when name is not a local variable; the type is empty when it is one
// but its type could not be worked out.
func (g *goScanner) localType(pkg *goPkg, gf *goFile, name string, stack []ast.Node, pos token.Pos) (t typ, local bool) {
	for i := len(stack) - 1; i >= 0; i-- {
		outer := stack[:i]
		switch n := stack[i].(type) {
		case *ast.RangeStmt:
			if n.Tok != token.DEFINE {
				continue
			}
			if isIdent(n.Value, name) {
				if xt, ok := g.exprType(pkg, gf, n.X, outer, n.Pos()); ok {
					if et, ok := g.elem(xt); ok {
						return et, true
					}
				}
				return typ{}, true
			}
			if isIdent(n.Key, name) {
				return typ{}, true
			}
		case *ast.BlockStmt:
			var found *typ
			for _, st := range n.List {
				if st.End() > pos {
					break
				}
				if tt, ok := g.declares(pkg, gf, st, name, append(outer, n), st.Pos()); ok {
					found = &tt
				}
			}
			if found != nil {
				return *found, true
			}
		case *ast.IfStmt:
			if n.Init != nil {
				if tt, ok := g.declares(pkg, gf, n.Init, name, outer, n.Pos()); ok {
					return tt, true
				}
			}
		case *ast.ForStmt:
			if n.Init != nil {
				if tt, ok := g.declares(pkg, gf, n.Init, name, outer, n.Pos()); ok {
					return tt, true
				}
			}
		case *ast.FuncLit:
			if tt, ok := paramType(n.Type, nil, name); ok {
				return typ{pkg, gf, tt}, true
			}
		case *ast.FuncDecl:
			if tt, ok := paramType(n.Type, n.Recv, name); ok {
				return typ{pkg, gf, tt}, true
			}
			return typ{}, false // no closures beyond a declared function
		}
	}
	return typ{}, false
}

// declares reports whether statement st declares name, and its type.
func (g *goScanner) declares(pkg *goPkg, gf *goFile, st ast.Stmt, name string, stack []ast.Node, pos token.Pos) (typ, bool) {
	switch s := st.(type) {
	case *ast.AssignStmt:
		if s.Tok != token.DEFINE {
			return typ{}, false
		}
		for li, lhs := range s.Lhs {
			if !isIdent(lhs, name) {
				continue
			}
			var t typ
			var ok bool
			if len(s.Rhs) == len(s.Lhs) {
				t, ok = g.exprType(pkg, gf, s.Rhs[li], stack, pos)
			} else if call, isCall := s.Rhs[0].(*ast.CallExpr); isCall && len(s.Rhs) == 1 {
				t, ok = g.callResult(pkg, gf, call, li, stack, pos)
			}
			if !ok {
				t = typ{}
			}
			return t, true
		}
	case *ast.DeclStmt:
		gd, ok := s.Decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			return typ{}, false
		}
		for _, sp := range gd.Specs {
			vs := sp.(*ast.ValueSpec)
			for vi, n := range vs.Names {
				if n.Name != name {
					continue
				}
				if vs.Type != nil {
					return typ{pkg, gf, vs.Type}, true
				}
				if vi < len(vs.Values) {
					if t, ok := g.exprType(pkg, gf, vs.Values[vi], stack, pos); ok {
						return t, true
					}
				}
				return typ{}, true
			}
		}
	}
	return typ{}, false
}

func paramType(ft *ast.FuncType, recv *ast.FieldList, name string) (ast.Expr, bool) {
	lists := []*ast.FieldList{recv, ft.Params, ft.Results}
	for _, l := range lists {
		if l == nil {
			continue
		}
		for _, f := range l.List {
			for _, n := range f.Names {
				if n.Name == name {
					return f.Type, true
				}
			}
		}
	}
	return nil, false
}

// requiredFlags lists the bool fields of variable name that the conditions of
// the if statements around the call require: "if f.I18n && f.Default != """
// requires I18n.
func requiredFlags(stack []ast.Node, name string) []string {
	var out []string
	for i, n := range stack {
		ifs, ok := n.(*ast.IfStmt)
		if !ok || i+1 >= len(stack) || stack[i+1] != ifs.Body {
			continue
		}
		var conj []ast.Expr
		conjuncts(ifs.Cond, &conj)
		for _, c := range conj {
			if sel, ok := c.(*ast.SelectorExpr); ok && isIdent(sel.X, name) {
				out = append(out, sel.Sel.Name)
			}
		}
	}
	return out
}

func conjuncts(e ast.Expr, out *[]ast.Expr) {
	switch x := e.(type) {
	case *ast.BinaryExpr:
		if x.Op == token.LAND {
			conjuncts(x.X, out)
			conjuncts(x.Y, out)
			return
		}
	case *ast.ParenExpr:
		conjuncts(x.X, out)
		return
	}
	*out = append(*out, e)
}

// fieldKeys resolves the key x.F, where x is a struct of a declared type, to
// the constants the literals of that type give for F.
func (g *goScanner) fieldKeys(pkg *goPkg, gf *goFile, e *ast.SelectorExpr, stack []ast.Node) ([]tableValue, bool) {
	t, ok := g.exprType(pkg, gf, e.X, stack, e.Pos())
	if !ok {
		return nil, false
	}
	p, name, ts := g.named(t)
	if ts == nil {
		return nil, false
	}
	if _, ok := g.field(t, e.Sel.Name); !ok {
		return nil, false
	}
	var need []string
	if id, ok := e.X.(*ast.Ident); ok {
		need = requiredFlags(stack, id.Name)
	}
	vals := g.harvest(p, name, e.Sel.Name, need)
	return vals, len(vals) > 0
}

// harvest collects the constants given for field in the composite literals
// of type tpkg.tname anywhere in the scanned code.
func (g *goScanner) harvest(tpkg *goPkg, tname, field string, need []string) []tableValue {
	var out []tableValue
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
				cl, ok := n.(*ast.CompositeLit)
				if !ok || cl.Type == nil {
					return true
				}
				t := typ{pkg, gf, cl.Type}
				if p, nm, _ := g.named(t); p == tpkg && nm == tname {
					out = append(out, g.literalField(pkg, gf, cl, field, need, stack)...)
					return true
				}
				// Elements of []T{...} or map[K]T{...} may leave out their type.
				et, ok := g.elem(t)
				if !ok {
					return true
				}
				if p, nm, _ := g.named(et); p != tpkg || nm != tname {
					return true
				}
				for _, el := range cl.Elts {
					if kv, ok := el.(*ast.KeyValueExpr); ok {
						el = kv.Value
					}
					if u, ok := el.(*ast.UnaryExpr); ok && u.Op == token.AND {
						el = u.X
					}
					if ecl, ok := el.(*ast.CompositeLit); ok && ecl.Type == nil {
						out = append(out, g.literalField(pkg, gf, ecl, field, need, stack)...)
					}
				}
				return true
			})
		}
	}
	return out
}

// literalField is the value of field in literal cl, when every flag in need
// is set to true there.
func (g *goScanner) literalField(pkg *goPkg, gf *goFile, cl *ast.CompositeLit, field string, need []string, stack []ast.Node) []tableValue {
	vals := map[string]ast.Expr{}
	for _, el := range cl.Elts {
		if kv, ok := el.(*ast.KeyValueExpr); ok {
			if id, ok := kv.Key.(*ast.Ident); ok {
				vals[id.Name] = kv.Value
			}
		}
	}
	for _, f := range need {
		if !isIdent(vals[f], "true") {
			return nil
		}
	}
	v, ok := vals[field]
	if !ok {
		return nil
	}
	if s, ok := g.constString(pkg, gf, v); ok {
		return []tableValue{{s, g.loc(v.Pos())}}
	}
	// A constructor: the field takes a parameter of the enclosing function.
	var positions func(nargs int) []int
	var param string
	switch x := v.(type) {
	case *ast.Ident:
		param = x.Name
		positions = nil
	case *ast.IndexExpr: // pairs[i+1] in a loop over a variadic parameter
		id, ok := x.X.(*ast.Ident)
		if !ok {
			return nil
		}
		param = id.Name
		start, step, ok := loopPattern(stack, x.Index)
		if !ok {
			return nil
		}
		positions = func(nargs int) []int {
			var ps []int
			for p := start; p < nargs; p += step {
				ps = append(ps, p)
			}
			return ps
		}
	default:
		return nil
	}
	fnIdx := -1
	for i := len(stack) - 1; i >= 0; i-- {
		if _, ok := stack[i].(*ast.FuncLit); ok {
			fnIdx = i
			break
		}
		if _, ok := stack[i].(*ast.FuncDecl); ok {
			fnIdx = i
			break
		}
	}
	if fnIdx < 0 {
		return nil
	}
	fn, idx := enclosingParam(stack[:fnIdx+1], param)
	if fn != stack[fnIdx] {
		return nil
	}
	name := funcName(fn)
	if name == "" {
		name = funcLitName(stack, fnIdx)
	}
	if name == "" {
		return nil
	}
	var out []tableValue
	for _, c := range g.callsTo(pkg, name) {
		var ps []int
		if positions == nil {
			ps = []int{idx}
		} else {
			ps = positions(len(c.call.Args) - idx)
			for i := range ps {
				ps[i] += idx
			}
		}
		for _, p := range ps {
			if p < len(c.call.Args) {
				if s, ok := g.constString(c.pkg, c.gf, c.call.Args[p]); ok {
					out = append(out, tableValue{s, g.loc(c.call.Args[p].Pos())})
				}
			}
		}
	}
	return out
}

// loopPattern reads "for i := start; ...; i += step" around an index i or
// i+k, and returns the first position and the step.
func loopPattern(stack []ast.Node, index ast.Expr) (int, int, bool) {
	var name string
	off := 0
	switch x := index.(type) {
	case *ast.Ident:
		name = x.Name
	case *ast.BinaryExpr:
		id, ok := x.X.(*ast.Ident)
		lit, ok2 := x.Y.(*ast.BasicLit)
		if !ok || !ok2 || x.Op != token.ADD || lit.Kind != token.INT {
			return 0, 0, false
		}
		name = id.Name
		off, _ = strconv.Atoi(lit.Value)
	default:
		return 0, 0, false
	}
	for i := len(stack) - 1; i >= 0; i-- {
		fs, ok := stack[i].(*ast.ForStmt)
		if !ok {
			continue
		}
		init, ok := fs.Init.(*ast.AssignStmt)
		if !ok || len(init.Lhs) != 1 || !isIdent(init.Lhs[0], name) {
			continue
		}
		lit, ok := init.Rhs[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.INT {
			return 0, 0, false
		}
		start, _ := strconv.Atoi(lit.Value)
		step := 0
		switch p := fs.Post.(type) {
		case *ast.IncDecStmt:
			if p.Tok == token.INC && isIdent(p.X, name) {
				step = 1
			}
		case *ast.AssignStmt:
			if p.Tok == token.ADD_ASSIGN && isIdent(p.Lhs[0], name) {
				if l, ok := p.Rhs[0].(*ast.BasicLit); ok && l.Kind == token.INT {
					step, _ = strconv.Atoi(l.Value)
				}
			}
		}
		if step <= 0 {
			return 0, 0, false
		}
		return start + off, step, true
	}
	return 0, 0, false
}

// funcLitName is the variable a function literal is assigned to.
func funcLitName(stack []ast.Node, i int) string {
	if i == 0 {
		return ""
	}
	fl := stack[i]
	switch p := stack[i-1].(type) {
	case *ast.ValueSpec:
		for j, v := range p.Values {
			if v == fl && j < len(p.Names) {
				return p.Names[j].Name
			}
		}
	case *ast.AssignStmt:
		for j, v := range p.Rhs {
			if v == fl && j < len(p.Lhs) {
				if id, ok := p.Lhs[j].(*ast.Ident); ok {
					return id.Name
				}
			}
		}
	}
	return ""
}

type callSite struct {
	call *ast.CallExpr
	pkg  *goPkg
	gf   *goFile
}

// callsTo finds the calls to function name of package p: name(...) inside p
// and pkg.name(...) elsewhere.
func (g *goScanner) callsTo(p *goPkg, name string) []callSite {
	var out []callSite
	for _, ip := range g.order {
		pkg := g.pkgs[ip]
		for _, gf := range pkg.files {
			ast.Inspect(gf.f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch f := call.Fun.(type) {
				case *ast.Ident:
					if pkg == p && f.Name == name {
						out = append(out, callSite{call, pkg, gf})
					}
				case *ast.SelectorExpr:
					if id, ok := f.X.(*ast.Ident); ok && f.Sel.Name == name && gf.imports[id.Name] == p.path {
						out = append(out, callSite{call, pkg, gf})
					}
				}
				return true
			})
		}
	}
	return out
}
