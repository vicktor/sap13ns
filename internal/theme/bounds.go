package theme

import (
	"errors"
	"fmt"
	"html/template"
	"reflect"
	texttemplate "text/template"
)

// The functions that make text, for uploaded themes, bounded: a template
// that formats the result of a format again ({{$a := printf "%s%s" $b $b}})
// doubles its text at each step and could take all the memory before it
// writes a byte, so these functions refuse to make a text longer than
// maxFormatted, from an estimate made before the text is. Their results
// are otherwise those of the server's functions and of text/template's
// built-in ones, which the overrides call.

const maxFormatted = 1 << 20

var errTooLong = errors.New("an uploaded theme cannot make a text longer than 1 MB")

// estimate is an upper bound of the length of fmt's text of args, with any
// width or precision (at most 99, check.go) and any verb (%q or %x at most
// triple a byte; the escapers of html, js and urlquery at most sextuple).
func estimate(extra int, args []any) int {
	n := extra
	for _, a := range args {
		v := reflect.ValueOf(a)
		switch v.Kind() {
		case reflect.String:
			n += v.Len()
		case reflect.Slice:
			if v.Type().Elem().Kind() == reflect.Uint8 {
				n += v.Len()
			} else {
				n += len(fmt.Sprint(a))
			}
		case reflect.Invalid, reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
			n += 32
		default:
			n += len(fmt.Sprint(a))
		}
		n += 100
		if n > maxFormatted {
			return n
		}
	}
	return 6 * n
}

func bounded(extra int, args []any) error {
	if estimate(extra, args) > maxFormatted {
		return errTooLong
	}
	return nil
}

// boundedFuncs replace the text-making functions of an uploaded theme's
// templates.
func boundedFuncs(base template.FuncMap) template.FuncMap {
	out := template.FuncMap{
		"printf": func(format string, args ...any) (string, error) {
			if err := bounded(len(format), args); err != nil {
				return "", err
			}
			return fmt.Sprintf(format, args...), nil
		},
		"print": func(args ...any) (string, error) {
			if err := bounded(0, args); err != nil {
				return "", err
			}
			return fmt.Sprint(args...), nil
		},
		"println": func(args ...any) (string, error) {
			if err := bounded(0, args); err != nil {
				return "", err
			}
			return fmt.Sprintln(args...), nil
		},
		"html": func(args ...any) (string, error) {
			if err := bounded(0, args); err != nil {
				return "", err
			}
			return texttemplate.HTMLEscaper(args...), nil
		},
		"js": func(args ...any) (string, error) {
			if err := bounded(0, args); err != nil {
				return "", err
			}
			return texttemplate.JSEscaper(args...), nil
		},
		"urlquery": func(args ...any) (string, error) {
			if err := bounded(0, args); err != nil {
				return "", err
			}
			return texttemplate.URLQueryEscaper(args...), nil
		},
	}
	if join, ok := base["join"].(func([]string, string) string); ok {
		out["join"] = func(elems []string, sep string) (string, error) {
			n := len(sep) * len(elems)
			for _, e := range elems {
				if n += len(e); n > maxFormatted {
					return "", errTooLong
				}
			}
			return join(elems, sep), nil
		}
	}
	if para, ok := base["paragraphs"].(func(string) template.HTML); ok {
		out["paragraphs"] = func(text string) (template.HTML, error) {
			if err := bounded(0, []any{text}); err != nil {
				return "", err
			}
			return para(text), nil
		}
	}
	return out
}

// boundLangFuncs bounds T and plural, which format their arguments.
func boundLangFuncs(funcs template.FuncMap) {
	if t, ok := funcs["T"].(func(string, ...any) string); ok {
		funcs["T"] = func(key string, args ...any) (string, error) {
			if len(args) > 0 {
				if err := bounded(len(key)+4096, args); err != nil {
					return "", err
				}
			}
			return t(key, args...), nil
		}
	}
	if p, ok := funcs["plural"].(func(int, string, string, ...any) string); ok {
		funcs["plural"] = func(n int, one, many string, args ...any) (string, error) {
			if err := bounded(len(one)+len(many)+4096, args); err != nil {
				return "", err
			}
			return p(n, one, many, args...), nil
		}
	}
}
