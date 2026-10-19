package web

import (
	"fmt"
	"html/template"
	"regexp"
	"sort"
	"strings"
)

// The admin's single-choice lists are Basecoat's select and combobox
// components rather than native <select>s. Templates describe one with the
// selectbox function and render it with the "select" partial:
//
//	{{$o := list}}{{range .Roles}}{{$o = append $o (opt . (roleLabel .))}}{{end}}
//	{{template "select" (selectbox "id" "role" "name" "role" "value" .Role "options" $o)}}
//
// The trigger button carries the id, so an existing <label for> still names
// it, and a hidden input carries the name and the value, so forms submit
// exactly what the native select did. ui/static/selects.js re-dispatches
// the component's change as native input and change events from that hidden
// input, so scripts read it like a <select>.

// selectOption is one choice of a select.
type selectOption struct {
	Value string
	Label string
}

// opt builds a selectOption; the value goes through fmt.Sprint, so ids and
// numbers compare with the selectbox value the same way.
func opt(value, label any) selectOption {
	return selectOption{Value: fmt.Sprint(value), Label: fmt.Sprint(label)}
}

// appendAny lets a template build a list in a range loop:
// {{$o = append $o (opt .ID .Name)}}.
func appendAny(list []any, v ...any) []any { return append(list, v...) }

// selectBox is what the "select" partial renders.
type selectBox struct {
	ID          string // the trigger's id, what a <label for> points at
	Name        string // the hidden input's name; empty for a script-only control
	Value       string // the submitted value, resolved against Options
	Options     []selectOption
	Placeholder string // shown, muted, while the value is empty
	Class       string // extra classes on the root, such as "w-full" or "grow"
	Size        string // the trigger's data-size ("sm")
	Align       string // the popover's data-align ("end")
	Disabled    bool
	Invalid     bool
	// Search makes it a combobox with a search field, for long lists. Its
	// empty-value option, if any, becomes the placeholder.
	Search      bool
	Describedby string // aria-describedby of the trigger (help, error)
	AriaLabel   string // for a list without a visible <label for>
	// Data holds data-* attributes for the hidden input, which is what
	// scripts see as the control (data-autosubmit, data-type…).
	Data template.HTMLAttr

	Label  string // the label of the chosen option, or the placeholder
	Chosen bool   // false while the placeholder shows
}

// Selected reports whether o is the chosen option.
func (b *selectBox) Selected(o selectOption) bool { return b.Chosen && o.Value == b.Value }

var dataAttrName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// selectbox builds a selectBox from key/value pairs, like dict. Unknown keys
// are an error, so a typo fails the template test instead of the page.
func selectbox(kv ...any) (*selectBox, error) {
	if len(kv)%2 != 0 {
		return nil, fmt.Errorf("selectbox: odd number of arguments")
	}
	b := &selectBox{}
	for i := 0; i < len(kv); i += 2 {
		k, ok := kv[i].(string)
		if !ok {
			return nil, fmt.Errorf("selectbox: key %v is not a string", kv[i])
		}
		v := kv[i+1]
		str := func() string {
			if v == nil {
				return ""
			}
			return fmt.Sprint(v)
		}
		flag := func() (bool, error) {
			f, ok := v.(bool)
			if !ok {
				return false, fmt.Errorf("selectbox: %s wants a bool, got %T", k, v)
			}
			return f, nil
		}
		var err error
		switch k {
		case "id":
			b.ID = str()
		case "name":
			b.Name = str()
		case "value":
			b.Value = str()
		case "placeholder":
			b.Placeholder = str()
		case "class":
			b.Class = str()
		case "size":
			b.Size = str()
		case "align":
			b.Align = str()
		case "describedby":
			b.Describedby = str()
		case "ariaLabel":
			b.AriaLabel = str()
		case "disabled":
			b.Disabled, err = flag()
		case "invalid":
			b.Invalid, err = flag()
		case "search":
			b.Search, err = flag()
		case "options":
			b.Options, err = selectOptions(v)
		case "data":
			b.Data, err = dataAttrs(v)
		default:
			err = fmt.Errorf("selectbox: unknown key %q", k)
		}
		if err != nil {
			return nil, err
		}
	}
	if b.ID == "" {
		return nil, fmt.Errorf("selectbox: an id is required")
	}
	b.resolve()
	return b, nil
}

// resolve picks the chosen option the way a native select does: the one
// whose value matches, else the first, unless a placeholder can show an
// empty value. The submitted value always matches what the trigger shows.
func (b *selectBox) resolve() {
	if b.Search && b.Placeholder == "" {
		for _, o := range b.Options {
			if o.Value == "" {
				b.Placeholder = o.Label
				break
			}
		}
	}
	for _, o := range b.Options {
		if o.Value == b.Value && (o.Value != "" || b.Placeholder == "") {
			b.Label, b.Chosen = o.Label, true
			return
		}
	}
	if b.Placeholder != "" || len(b.Options) == 0 {
		b.Value, b.Label = "", b.Placeholder
		return
	}
	b.Value, b.Label, b.Chosen = b.Options[0].Value, b.Options[0].Label, true
}

func selectOptions(v any) ([]selectOption, error) {
	switch l := v.(type) {
	case nil:
		return nil, nil
	case []selectOption:
		return l, nil
	case []any:
		out := make([]selectOption, 0, len(l))
		for _, e := range l {
			switch o := e.(type) {
			case selectOption:
				out = append(out, o)
			case []any:
				// (list value label), handy for a fixed list.
				if len(o) != 2 {
					return nil, fmt.Errorf("selectbox: option %v is not a value and a label", o)
				}
				out = append(out, opt(o[0], o[1]))
			default:
				return nil, fmt.Errorf("selectbox: option %v (%T) is not an opt", e, e)
			}
		}
		return out, nil
	case []string:
		out := make([]selectOption, len(l))
		for i, s := range l {
			out[i] = selectOption{Value: s, Label: s}
		}
		return out, nil
	}
	return nil, fmt.Errorf("selectbox: options must be a list, got %T", v)
}

// dataAttrs renders a dict as data-* attributes, escaped, in key order.
func dataAttrs(v any) (template.HTMLAttr, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return "", fmt.Errorf("selectbox: data must be a dict, got %T", v)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		if !dataAttrName.MatchString(k) {
			return "", fmt.Errorf("selectbox: bad data attribute %q", k)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		sb.WriteString(" data-" + k)
		if s := fmt.Sprint(m[k]); s != "" {
			sb.WriteString(`="` + template.HTMLEscapeString(s) + `"`)
		}
	}
	return template.HTMLAttr(sb.String()), nil
}
