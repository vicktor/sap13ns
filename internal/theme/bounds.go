package theme

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"io"
	"maps"
	"net/url"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"
)

// The bounds of an uploaded theme's templates as they run. A template is a
// program: one that formats the result of a format again
// ({{$a = printf "%s%s" $a $a}}) doubles its text at each step, and one
// that repeats an argument (%[1]s%[1]s…) or pads it multiplies it, before
// it writes a byte. So the templates of an uploaded theme run with their
// own copies of the functions (funcsFor), bound to a guard:
//
//   - every function checks the guard before it runs: past RenderTimeout,
//     once the request ends or once a limit was hit, it fails, so a page
//     stops even between two writes. The pristine template sets are bound
//     to a guard that always fails: no path can run them unbounded;
//   - every function counts the text it makes: at most maxMade per page;
//   - the functions that format (printf, print, println, html, js,
//     urlquery, T, plural, join, paragraphs) compute an upper bound of the
//     text before they make it, from the format actually used (for T, the
//     translation, not its key) and from the arguments, and fail past
//     maxFormatted. They format only texts, numbers, booleans and values
//     with a String or Error method (dates): fmt writes other values field
//     by field, and pads each element to the width, which nothing bounds;
//   - dict and list make values that fmt writes as "map[...]" and "[...]":
//     a list that holds itself twice, then four times, and so on would
//     otherwise print in exponential space, through html/template's
//     escapers, which print with fmt.

const (
	maxFormatted = 1 << 20  // one text a function makes
	maxMade      = 16 << 20 // all the text the functions make for one page
	maxWidth     = 99       // widths and precisions in formats
	// maxSlots is the number of copies of one page's templates, in one
	// language, that render at the same time.
	maxSlots = 4
)

var (
	errTooLong = errors.New("an uploaded theme cannot make a text longer than 1 MB")
	errTooMuch = errors.New("an uploaded theme cannot make more than 16 MB of text for one page")
	errTimeout = fmt.Errorf("took longer than %s", RenderTimeout)
	errBusy    = errors.New("every copy of the page's templates stayed busy")
	errClosed  = errors.New("an uploaded theme's templates run only through RenderContext")
	errWidth   = errors.New("an uploaded theme formats with widths and precisions of at most 99, written in the format")
	errValue   = errors.New("an uploaded theme formats only texts, numbers, booleans and dates, not lists, maps or structures")
)

// guard is the budget of one execution of an uploaded theme's templates.
// A slot's guard serves one execution at a time.
type guard struct {
	ctx      context.Context
	deadline time.Time
	made     int
	err      error
}

// closedGuard binds the pristine template sets of uploaded themes.
var closedGuard = &guard{err: errClosed}

func (g *guard) start(ctx context.Context, deadline time.Time) {
	g.ctx, g.deadline, g.made, g.err = ctx, deadline, 0, nil
}

func (g *guard) stop() { g.ctx, g.err = nil, errClosed }

func (g *guard) check() error {
	if g.err != nil {
		return g.err
	}
	if !time.Now().Before(g.deadline) {
		g.err = errTimeout
	} else if err := g.ctx.Err(); err != nil {
		g.err = err
	}
	return g.err
}

// fail records the first limit hit; the template stops at the next call
// or write.
func (g *guard) fail(err error) error {
	if g.err == nil {
		g.err = err
	}
	return g.err
}

// count adds n bytes of text made.
func (g *guard) count(n int) error {
	if g.err != nil {
		return g.err
	}
	if g.made += n; g.made > maxMade {
		return g.fail(errTooMuch)
	}
	return nil
}

// within fails past maxFormatted, before a text of at most n bytes is made.
func (g *guard) within(n int) error {
	if n > maxFormatted || n < 0 {
		return g.fail(errTooLong)
	}
	return nil
}

var errorType = reflect.TypeFor[error]()

// guardFunc wraps a function of the server for a guard: it checks the
// guard first and counts the text it returns. The result has the same
// arguments, and an error.
func guardFunc(fn any, g *guard) any {
	v := reflect.ValueOf(fn)
	ft := v.Type()
	if ft.Kind() != reflect.Func || ft.NumOut() == 0 || ft.NumOut() > 2 || ft.NumOut() == 2 && ft.Out(1) != errorType {
		return fn // text/template refuses it anyway
	}
	in := make([]reflect.Type, ft.NumIn())
	for i := range in {
		in[i] = ft.In(i)
	}
	zero := reflect.Zero(ft.Out(0))
	failed := func(err error) []reflect.Value { return []reflect.Value{zero, reflect.ValueOf(&err).Elem()} }
	wt := reflect.FuncOf(in, []reflect.Type{ft.Out(0), errorType}, ft.IsVariadic())
	return reflect.MakeFunc(wt, func(args []reflect.Value) []reflect.Value {
		if err := g.check(); err != nil {
			return failed(err)
		}
		var res []reflect.Value
		if ft.IsVariadic() {
			res = v.CallSlice(args)
		} else {
			res = v.Call(args)
		}
		if len(res) == 1 {
			res = append(res, reflect.Zero(errorType))
		}
		if res[0].Kind() == reflect.String {
			if err := g.count(res[0].Len()); err != nil {
				return failed(err)
			}
		}
		return res
	}).Interface()
}

// funcsFor returns the functions of one execution of the theme's
// templates in lang, bound to g.
func (t *Theme) funcsFor(lang string, g *guard) template.FuncMap {
	out := template.FuncMap{}
	for name, fn := range t.funcs {
		out[name] = guardFunc(fn, g)
	}
	core, _ := t.funcs["T"].(func(string, ...any) string)
	if t.langFuncs != nil {
		lf := t.langFuncs(lang)
		for name, fn := range lf {
			out[name] = guardFunc(fn, g)
		}
		if c, ok := lf["T"].(func(string, ...any) string); ok {
			core = c
		}
	}
	// T formats the text it translates to: the theme's own translation,
	// or the server's (T with no arguments returns it as is), or the key.
	cat := t.i18n[lang]
	text := func(key string) string {
		if v := cat[key]; v != "" {
			return v
		}
		if core != nil {
			return core(key)
		}
		return key
	}
	tr := func(key string, args ...any) (string, error) {
		if err := g.check(); err != nil {
			return "", err
		}
		if len(args) == 0 {
			return text(key), nil
		}
		return g.sprintf(text(key), args)
	}
	out["T"] = tr
	out["plural"] = func(n int, one, many string, args ...any) (string, error) {
		key := many
		if n == 1 {
			key = one
		}
		return tr(key, append([]any{n}, args...)...)
	}
	maps.Copy(out, textFuncs(g, t.funcs))
	return out
}

// textFuncs are the functions that make text, bounded; join, paragraphs,
// dict and list wrap the server's functions of those names.
func textFuncs(g *guard, base template.FuncMap) template.FuncMap {
	out := template.FuncMap{
		"printf":   func(format string, args ...any) (string, error) { return g.sprintf(format, args) },
		"print":    func(args ...any) (string, error) { return g.sprint(args, false) },
		"println":  func(args ...any) (string, error) { return g.sprint(args, true) },
		"html":     func(args ...any) (string, error) { return g.escape(args, 5, template.HTMLEscapeString) },
		"js":       func(args ...any) (string, error) { return g.escape(args, 6, template.JSEscapeString) },
		"urlquery": func(args ...any) (string, error) { return g.escape(args, 3, url.QueryEscape) },
	}
	if join, ok := base["join"].(func([]string, string) string); ok {
		out["join"] = func(elems []string, sep string) (string, error) {
			if err := g.check(); err != nil {
				return "", err
			}
			n := len(sep) * max(len(elems)-1, 0)
			for _, e := range elems {
				if n += len(e); n > maxFormatted {
					return "", g.fail(errTooLong)
				}
			}
			s := join(elems, sep)
			return s, g.count(len(s))
		}
	}
	if para, ok := base["paragraphs"].(func(string) template.HTML); ok {
		// At most five bytes per byte of text (an escaped quote; a line
		// break is four), and seven per paragraph, which takes two.
		out["paragraphs"] = func(text string) (template.HTML, error) {
			if err := g.check(); err != nil {
				return "", err
			}
			if err := g.within(5*len(text) + 16); err != nil {
				return "", err
			}
			h := para(text)
			return h, g.count(len(h))
		}
	}
	if dict, ok := base["dict"].(func(...any) (map[string]any, error)); ok {
		out["dict"] = func(kv ...any) (themeMap, error) {
			if err := g.check(); err != nil {
				return nil, err
			}
			m, err := dict(kv...)
			if err != nil {
				return nil, err
			}
			return themeMap(m), g.count(16 * (len(kv) + 1))
		}
	}
	if list, ok := base["list"].(func(...any) []any); ok {
		out["list"] = func(v ...any) (themeList, error) {
			if err := g.check(); err != nil {
				return nil, err
			}
			return themeList(list(v...)), g.count(16 * (len(v) + 1))
		}
	}
	return out
}

// themeList and themeMap are the lists and dicts of an uploaded theme's
// templates. Templates range over them and read them as usual; fmt writes
// them as a fixed text.
type (
	themeList []any
	themeMap  map[string]any
)

const (
	listText = "[...]"
	mapText  = "map[...]"
)

func (themeList) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, listText) }
func (themeMap) Format(f fmt.State, _ rune)  { _, _ = io.WriteString(f, mapText) }

func (g *guard) sprintf(format string, args []any) (string, error) {
	if err := g.check(); err != nil {
		return "", err
	}
	xs, err := textValues(args)
	if err != nil {
		return "", err
	}
	n, err := formatBound(format, xs)
	if err != nil {
		return "", err
	}
	if err := g.within(n); err != nil {
		return "", err
	}
	s := fmt.Sprintf(format, xs...)
	return s, g.count(len(s))
}

// sprint is fmt.Sprint (or Sprintln) of args, as text/template's print
// writes them: a space between two operands that are not strings.
func (g *guard) sprint(args []any, ln bool) (string, error) {
	if err := g.check(); err != nil {
		return "", err
	}
	xs, err := textValues(args)
	if err != nil {
		return "", err
	}
	if err := g.within(printBound(xs)); err != nil {
		return "", err
	}
	var s string
	if ln {
		s = fmt.Sprintln(xs...)
	} else {
		s = joinPrinted(args, xs)
	}
	return s, g.count(len(s))
}

// escape is text/template's html, js or urlquery (HTMLEscaper, JSEscaper,
// URLQueryEscaper): the text of its arguments (one string as is, or their
// printable values printed with fmt.Sprint), escaped, which makes at most
// factor bytes per byte.
func (g *guard) escape(args []any, factor int, esc func(string) string) (string, error) {
	if err := g.check(); err != nil {
		return "", err
	}
	s, ok := "", false
	if len(args) == 1 {
		s, ok = args[0].(string)
	}
	if !ok {
		printable := make([]any, len(args))
		for i, a := range args {
			printable[i] = printableValue(a)
		}
		xs, err := textValues(printable)
		if err != nil {
			return "", err
		}
		if err := g.within(printBound(xs)); err != nil {
			return "", err
		}
		s = joinPrinted(printable, xs)
	}
	if err := g.within(factor*len(s) + 16); err != nil {
		return "", err
	}
	out := esc(s)
	return out, g.count(len(out))
}

// joinPrinted is fmt.Sprint of xs, the text values of args, with the
// spaces fmt.Sprint puts between args.
func joinPrinted(args, xs []any) string {
	var b strings.Builder
	prev := false
	for i, x := range xs {
		isString := args[i] != nil && reflect.TypeOf(args[i]).Kind() == reflect.String
		if i > 0 && !isString && !prev {
			b.WriteByte(' ')
		}
		b.WriteString(fmt.Sprint(x))
		prev = isString
	}
	return b.String()
}

// printableValue is text/template's, for html, js and urlquery: the value
// a pointer points to, or a pointer to a value whose methods take one.
func printableValue(a any) any {
	v := reflect.ValueOf(a)
	if v.Kind() == reflect.Pointer {
		for (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) && !v.IsNil() {
			v = v.Elem()
		}
	}
	if !v.IsValid() {
		return "<no value>"
	}
	if !hasText(v.Type()) && v.CanAddr() && hasText(reflect.PointerTo(v.Type())) {
		v = v.Addr()
	}
	if !v.CanInterface() {
		return a
	}
	return v.Interface()
}

var (
	stringerType  = reflect.TypeFor[fmt.Stringer]()
	formatterType = reflect.TypeFor[fmt.Formatter]()
	goStringType  = reflect.TypeFor[fmt.GoStringer]()
)

func hasText(t reflect.Type) bool { return t.Implements(errorType) || t.Implements(stringerType) }

func textValues(args []any) ([]any, error) {
	xs := make([]any, len(args))
	for i, a := range args {
		x, err := textValue(a)
		if err != nil {
			return nil, err
		}
		xs[i] = x
	}
	return xs, nil
}

// textValue is what the bounded functions format in place of a: a's own
// text for a value with an Error or String method, a itself for a basic
// value or an address. Lists, maps and structures are refused: fmt writes
// them field by field.
func textValue(a any) (any, error) {
	switch a := a.(type) {
	case nil, string, bool, int, int64, float64:
		return a, nil
	case themeList:
		return listText, nil
	case themeMap:
		return mapText, nil
	}
	v := reflect.ValueOf(a)
	t := v.Type()
	if t.Implements(formatterType) {
		return nil, errValue
	}
	if hasText(t) {
		if v.Kind() == reflect.Pointer && v.IsNil() {
			return "<nil>", nil // what fmt writes when the method panics
		}
		if e, ok := a.(error); ok {
			return e.Error(), nil
		}
		return a.(fmt.Stringer).String(), nil
	}
	if t.Implements(goStringType) {
		return nil, errValue
	}
	switch v.Kind() {
	case reflect.Array, reflect.Slice, reflect.Struct, reflect.Map, reflect.Interface:
		return nil, errValue
	case reflect.Pointer:
		if !v.IsNil() {
			switch v.Elem().Kind() {
			case reflect.Array, reflect.Slice, reflect.Struct, reflect.Map:
				return nil, errValue // fmt writes &{...}
			}
		}
	}
	return a, nil
}

// The length of fmt's text. formatBound follows fmt's doPrintf: the same
// flags, explicit argument indexes, widths and precisions, and the same
// texts for a missing argument, a bad index, a verb fmt does not know for
// the value and the arguments left over. sizeOf bounds the text of one
// verb; the caller adds the width.

// formatBound returns an upper bound of the length of
// fmt.Sprintf(format, xs...), xs being text values.
func formatBound(format string, xs []any) (int, error) {
	end := len(format)
	n, argNum := 0, 0
	afterIndex, reordered := false, false
	index := func(i int, good *bool) int {
		var bracket, ok bool
		argNum, i, afterIndex, bracket, ok = argNumber(argNum, format, i, len(xs))
		reordered = reordered || bracket
		if !ok {
			*good = false
		}
		return i
	}
	for i := 0; i < end; {
		good := true
		last := i
		for i < end && format[i] != '%' {
			i++
		}
		n += i - last
		if i >= end {
			break
		}
		i++
		sharp := false
	flags:
		for ; i < end; i++ {
			switch format[i] {
			case '#':
				sharp = true
			case '0', '+', '-', ' ':
			default:
				break flags
			}
		}
		i = index(i, &good)
		if i < end && format[i] == '*' {
			return 0, errWidth
		}
		wid, digits := number(format, i)
		if digits > 2 {
			return 0, errWidth
		}
		i += digits
		if afterIndex && digits > 0 {
			good = false
		}
		if i+1 < end && format[i] == '.' {
			i++
			if afterIndex {
				good = false
			}
			i = index(i, &good)
			if i < end && format[i] == '*' {
				return 0, errWidth
			}
			_, digits := number(format, i)
			if digits > 2 {
				return 0, errWidth
			}
			i += digits
		}
		if !afterIndex {
			i = index(i, &good)
		}
		if i >= end {
			n += len("%!(NOVERB)")
			break
		}
		verb, size := utf8.DecodeRuneInString(format[i:])
		i += size
		switch {
		case verb == '%':
			n++
		case !good:
			n += len("%!(BADINDEX)") + utf8.UTFMax
		case argNum >= len(xs):
			n += len("%!(MISSING)") + utf8.UTFMax
		default:
			n += sizeOf(xs[argNum], verb, sharp) + wid
			argNum++
		}
		if n > maxFormatted {
			return n, nil
		}
	}
	if !reordered && argNum < len(xs) {
		n += len("%!(EXTRA )")
		for _, x := range xs[argNum:] {
			n += len(", ") + typeLen(x) + len("=") + vSize(x)
		}
	}
	return n, nil
}

// number reads the digits at format[i:]: their value (up to two digits)
// and how many there are.
func number(format string, i int) (value, digits int) {
	for j := i; j < len(format) && '0' <= format[j] && format[j] <= '9'; j++ {
		if digits++; digits <= 2 {
			value = value*10 + int(format[j]-'0')
		}
	}
	return value, digits
}

// argNumber is fmt's: an explicit argument index such as [2] at
// format[i:]. bracket reports that there was one (fmt then no longer
// writes the arguments left over), ok that it was valid.
func argNumber(argNum int, format string, i, numArgs int) (newArgNum, newi int, found, bracket, ok bool) {
	if len(format) <= i || format[i] != '[' {
		return argNum, i, false, false, true
	}
	index, wid, valid := parseArgNumber(format[i:])
	if valid && 0 <= index && index < numArgs {
		return index, i + wid, true, true, true
	}
	return argNum, i + wid, valid, true, false
}

func parseArgNumber(format string) (index, wid int, ok bool) {
	if len(format) < 3 {
		return 0, 1, false
	}
	for i := 1; i < len(format); i++ {
		if format[i] == ']' {
			width, ok, newi := parsenum(format, 1, i)
			if !ok || newi != i {
				return 0, i + 1, false
			}
			return width - 1, i + 1, true
		}
	}
	return 0, 1, false
}

func parsenum(s string, start, end int) (num int, isnum bool, newi int) {
	if start >= end {
		return 0, false, end
	}
	for newi = start; newi < end && '0' <= s[newi] && s[newi] <= '9'; newi++ {
		if num > 1e6 || num < -1e6 {
			return 0, false, end
		}
		num = num*10 + int(s[newi]-'0')
		isnum = true
	}
	return
}

func typeLen(x any) int {
	if x == nil {
		return 0
	}
	return len(reflect.TypeOf(x).String())
}

// vSize bounds the length of fmt's %v of a text value, without a width.
func vSize(x any) int {
	if x == nil {
		return 16
	}
	switch v := reflect.ValueOf(x); v.Kind() {
	case reflect.String:
		return v.Len()
	case reflect.Complex64, reflect.Complex128:
		return 64
	}
	return 32
}

// sizeOf bounds the length of fmt's text of the text value x for verb,
// with any precision up to maxWidth; the width is added by the caller.
func sizeOf(x any, verb rune, sharp bool) int {
	if x == nil {
		return 16 // "<nil>" or "%!d(<nil>)"
	}
	typ := typeLen(x)
	if verb == 'T' {
		return typ
	}
	// "%!d(type=value)", the value written with %v (quoted with #),
	// padded to the width.
	inner := vSize(x)
	if sharp {
		inner = 4*inner + 2
	}
	bad := 16 + typ + inner + maxWidth
	switch v := reflect.ValueOf(x); v.Kind() {
	case reflect.String:
		l := v.Len()
		switch verb {
		case 's':
			return l
		case 'v':
			if sharp {
				return 4*l + 2
			}
			return l
		case 'q':
			return 4*l + 2 // \x12 for a byte, \U0010ffff for four
		case 'x', 'X':
			return 5*l + 2 // "% #x": 0x12 and a space per byte
		}
	case reflect.Bool:
		if verb == 't' || verb == 'v' {
			return 5
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return max(256, bad) // 64 binary digits, or the precision, a sign and 0x
	case reflect.Float32, reflect.Float64:
		return max(512, bad) // %f of 1e308: 309 digits, and the precision
	case reflect.Complex64, reflect.Complex128:
		return max(1300, bad) // two floats, each padded to the width
	default:
		return max(256+typ, bad) // an address
	}
	return bad
}

// printBound bounds fmt.Sprint and Sprintln of text values.
func printBound(xs []any) int {
	n := 1
	for _, x := range xs {
		n += vSize(x) + 1
	}
	return n
}
