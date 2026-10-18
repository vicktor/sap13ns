// Package pluginskel creates a new plugin project from the skeleton in
// plugins/skeleton ("sap13ns plugin new").
//
// The skeleton is its own Go module, and go:embed cannot reach into another
// module, so an exact copy lives in _skeleton (with go.mod renamed to
// go.mod.txt). TestSkeletonInSync fails when the two differ; run
// "go test ./internal/pluginskel -update" after editing plugins/skeleton.
package pluginskel

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

//go:embed all:_skeleton
var embedded embed.FS

// Root is the skeleton inside the embedded files.
const Root = "_skeleton"

// SDKModule is the module path of the plugin SDK.
const SDKModule = "github.com/vicktor/sap13ns/pluginsdk"

// feature is one example of the skeleton and everything it adds.
type feature struct {
	Hook       string
	Files      []string // deleted when the hook is not wanted
	Shortcodes []string
	Widgets    []string
	Events     []string
	Forms      []string
	Perms      []string
	Settings   []string
}

var features = []feature{
	{Hook: "shortcode", Files: []string{"hello.go", "hello_test.go"}, Shortcodes: []string{"hello"}, Settings: []string{"greeting"}},
	{Hook: "widget", Files: []string{"latest.go", "latest_test.go"}, Widgets: []string{"latest"}, Perms: []string{"posts:read"}, Settings: []string{"greeting"}},
	{Hook: "content.filter", Files: []string{"reading.go", "reading_test.go"}, Settings: []string{"words_per_minute"}},
	{Hook: "head.meta", Files: []string{"head.go", "head_test.go"}},
	{Hook: "event", Files: []string{"events.go", "events_test.go"}, Events: []string{"post.published"}, Perms: []string{"kv"}},
	{Hook: "comment.check", Files: []string{"comments.go", "comments_test.go"}, Settings: []string{"max_links"}},
	// The contact form is placed with its own [contact] shortcode.
	{Hook: "form.submit", Files: []string{"contact.go", "contact_test.go"}, Shortcodes: []string{"contact"}, Forms: []string{"contact"},
		Perms: []string{"mail"}, Settings: []string{"copy_to"}},
}

// Hooks lists the hooks the skeleton has an example for.
func Hooks() []string {
	out := make([]string, len(features))
	for i, f := range features {
		out[i] = f.Hook
	}
	return out
}

// Options describe the new plugin.
type Options struct {
	ID     string   // plugin id, e.g. "hello-world"
	Name   string   // display name; derived from the id when empty
	Module string   // Go module path; "example.com/<id>" when empty
	SDK    string   // directory of pluginsdk for the go.mod replace; "" leaves it out
	Hooks  []string // examples to keep; empty keeps every one
}

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,49}$`)

// Generate writes a new plugin project into dir, which must not exist or be
// empty.
func Generate(dir string, o Options) error {
	if !idRe.MatchString(o.ID) {
		return errors.New("the id must be 2-50 lower-case letters, digits and dashes (^[a-z0-9][a-z0-9-]{1,49}$)")
	}
	if o.ID == "my-plugin" {
		return errors.New(`choose an id for your plugin ("my-plugin" is the placeholder of the skeleton)`)
	}
	if o.Name == "" {
		o.Name = NameFromID(o.ID)
	}
	if o.Module == "" {
		o.Module = "example.com/" + o.ID
	}
	keep := map[string]bool{}
	for _, h := range o.Hooks {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if !slices.Contains(Hooks(), h) {
			return fmt.Errorf("unknown hook %q (choose from %s)", h, strings.Join(Hooks(), ", "))
		}
		keep[h] = true
	}
	if len(keep) == 0 {
		for _, h := range Hooks() {
			keep[h] = true
		}
	}
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		return fmt.Errorf("%s exists and is not empty", dir)
	}

	var drop []string
	for _, f := range features {
		if !keep[f.Hook] {
			drop = append(drop, f.Files...)
		}
	}
	files := map[string][]byte{}
	err := fs.WalkDir(embedded, Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel := strings.TrimPrefix(p, Root+"/")
		if slices.Contains(drop, rel) {
			return nil
		}
		b, err := embedded.ReadFile(p)
		if err != nil {
			return err
		}
		files[strings.TrimSuffix(rel, ".txt")] = b
		return nil
	})
	if err != nil {
		return err
	}

	manifest, err := pruneManifest(files["plugin.json"], keep, o)
	if err != nil {
		return err
	}
	files["plugin.json"] = manifest
	files["go.mod"] = rewriteGoMod(files["go.mod"], o)
	files["main.go"] = pruneFileList(files["main.go"], drop)
	for name, b := range files {
		if name == "go.mod" || name == "plugin.json" {
			continue
		}
		b = bytes.ReplaceAll(b, []byte("my-plugin"), []byte(o.ID))
		b = bytes.ReplaceAll(b, []byte("My plugin"), []byte(o.Name))
		files[name] = b
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		target := filepath.Join(dir, filepath.FromSlash(n))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, files[n], 0o644); err != nil {
			return err
		}
	}
	return nil
}

// NameFromID turns "hello-world" into "Hello world".
func NameFromID(id string) string {
	s := strings.ReplaceAll(id, "-", " ")
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func rewriteGoMod(b []byte, o Options) []byte {
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		switch {
		case strings.HasPrefix(line, "module "):
			line = "module " + o.Module
		case strings.HasPrefix(line, "replace "+SDKModule):
			if o.SDK == "" {
				continue
			}
			line = "replace " + SDKModule + " => " + filepath.ToSlash(o.SDK)
		case strings.HasPrefix(line, "//") && o.SDK == "":
			continue // the comment about the replace line
		}
		out = append(out, line)
	}
	return []byte(strings.Join(out, "\n"))
}

// pruneFileList removes the lines naming dropped files from the overview
// at the top of main.go.
func pruneFileList(b []byte, drop []string) []byte {
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		skip := false
		for _, f := range drop {
			if strings.HasPrefix(line, "//\t"+f+" ") {
				skip = true
			}
		}
		if !skip {
			out = append(out, line)
		}
	}
	return []byte(strings.Join(out, "\n"))
}

// pruneManifest edits the skeleton's plugin.json for the kept hooks,
// keeping its key order.
func pruneManifest(raw []byte, keep map[string]bool, o Options) ([]byte, error) {
	keys, vals, err := orderedObject(raw)
	if err != nil {
		return nil, fmt.Errorf("skeleton plugin.json: %w", err)
	}
	set := func(k string, v any) {
		b, _ := json.Marshal(v)
		vals[k] = b
	}
	set("id", o.ID)
	set("name", o.Name)

	var hooks, shortcodes, widgets, events, forms, perms, settings []string
	for _, f := range features {
		if !keep[f.Hook] {
			continue
		}
		hooks = append(hooks, f.Hook)
		shortcodes = append(shortcodes, f.Shortcodes...)
		widgets = append(widgets, f.Widgets...)
		events = append(events, f.Events...)
		forms = append(forms, f.Forms...)
		perms = append(perms, f.Perms...)
		settings = append(settings, f.Settings...)
	}
	if len(shortcodes) > 0 && !slices.Contains(hooks, "shortcode") {
		hooks = append([]string{"shortcode"}, hooks...) // the [contact] shortcode
	}
	set("hooks", hooks)
	var keepNames = func(list []string) func(string) bool {
		return func(s string) bool { return slices.Contains(list, s) }
	}
	if err := filterStrings(vals, "shortcodes", keepNames(shortcodes)); err != nil {
		return nil, err
	}
	if err := filterStrings(vals, "events", keepNames(events)); err != nil {
		return nil, err
	}
	if err := filterStrings(vals, "permissions", keepNames(perms)); err != nil {
		return nil, err
	}
	for k, list := range map[string][]string{"widgets": widgets, "forms": forms} {
		if err := filterObjects(vals, k, "id", keepNames(list)); err != nil {
			return nil, err
		}
	}
	if err := filterObjects(vals, "settings", "key", keepNames(settings)); err != nil {
		return nil, err
	}
	// Leave out what became empty (but keep "permissions": [] as a
	// reminder that there are none).
	var outKeys []string
	for _, k := range keys {
		if k != "permissions" && (string(vals[k]) == "[]" || string(vals[k]) == "null") {
			continue
		}
		outKeys = append(outKeys, k)
	}
	return formatObject(outKeys, vals)
}

func filterStrings(vals map[string]json.RawMessage, key string, keep func(string) bool) error {
	raw, ok := vals[key]
	if !ok {
		return nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err != nil {
		return err
	}
	out := []string{}
	for _, s := range list {
		if keep(s) {
			out = append(out, s)
		}
	}
	vals[key], _ = json.Marshal(out)
	return nil
}

func filterObjects(vals map[string]json.RawMessage, key, idField string, keep func(string) bool) error {
	raw, ok := vals[key]
	if !ok {
		return nil
	}
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil {
		return err
	}
	// Kept items are copied as written (json.Marshal would compact them).
	var out [][]byte
	for _, item := range list {
		var id map[string]json.RawMessage
		if err := json.Unmarshal(item, &id); err != nil {
			return err
		}
		var s string
		json.Unmarshal(id[idField], &s)
		if keep(s) {
			out = append(out, bytes.TrimSpace(item))
		}
	}
	vals[key] = append(append([]byte("["), bytes.Join(out, []byte(","))...), ']')
	return nil
}

// orderedObject reads a JSON object's keys in order and their raw values.
func orderedObject(raw []byte) ([]string, map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, nil, errors.New("not a JSON object")
	}
	var keys []string
	vals := map[string]json.RawMessage{}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, nil, err
		}
		k, _ := t.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, nil, err
		}
		keys = append(keys, k)
		vals[k] = v
	}
	return keys, vals, nil
}

// formatObject prints a manifest the way the skeleton is written: one key
// per line, short values inline, lists of objects one object per line
// (each object keeps its own layout).
func formatObject(keys []string, vals map[string]json.RawMessage) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString("{\n")
	for i, k := range keys {
		kb, _ := json.Marshal(k)
		b.WriteString("  " + string(kb) + ": ")
		v := bytes.TrimSpace(vals[k])
		var items []json.RawMessage
		if len(v) > 0 && v[0] == '[' && json.Unmarshal(v, &items) == nil && len(items) > 0 && bytes.HasPrefix(bytes.TrimSpace(items[0]), []byte("{")) {
			b.WriteString("[\n")
			for j, it := range items {
				b.WriteString("    ")
				b.Write(bytes.TrimSpace(it)) // as written in the skeleton
				if j < len(items)-1 {
					b.WriteString(",")
				}
				b.WriteString("\n")
			}
			b.WriteString("  ]")
		} else {
			s, err := spaced(v)
			if err != nil {
				return nil, err
			}
			b.WriteString(s)
		}
		if i < len(keys)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("}\n")
	return b.Bytes(), nil
}

// spaced prints JSON on one line with a space after ":" and ",", keeping
// the key order.
func spaced(raw []byte) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	type frame struct {
		obj bool
		n   int
	}
	var stack []frame
	var b strings.Builder
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		closing := tok == json.Delim('}') || tok == json.Delim(']')
		if len(stack) > 0 && !closing {
			top := &stack[len(stack)-1]
			switch {
			case top.obj && top.n%2 == 1:
				b.WriteString(": ")
			case top.n > 0:
				b.WriteString(", ")
			}
			top.n++
		}
		switch t := tok.(type) {
		case json.Delim:
			b.WriteString(t.String())
			if closing {
				stack = stack[:len(stack)-1]
			} else {
				stack = append(stack, frame{obj: t == '{'})
			}
		default:
			var enc bytes.Buffer
			e := json.NewEncoder(&enc)
			e.SetEscapeHTML(false)
			if err := e.Encode(t); err != nil {
				return "", err
			}
			b.Write(bytes.TrimSpace(enc.Bytes()))
		}
	}
	return b.String(), nil
}

// FindSDK looks for a sap13ns checkout's pluginsdk directory in dir and its
// parents, and returns it relative to dir ("" when there is none).
func FindSDK(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	for d := abs; ; d = filepath.Dir(d) {
		mod, err := os.ReadFile(filepath.Join(d, "pluginsdk", "go.mod"))
		if err == nil && bytes.Contains(mod, []byte("module "+SDKModule+"\n")) {
			rel, err := filepath.Rel(abs, filepath.Join(d, "pluginsdk"))
			if err != nil {
				return filepath.Join(d, "pluginsdk")
			}
			return rel
		}
		if filepath.Dir(d) == d {
			return ""
		}
	}
}
