package i18n_test

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"sap13ns/internal/i18n"
	"sap13ns/ui"
)

// TestCatalogs checks the real UI catalogs (ui/i18n/*.json): valid JSON with
// unique keys in sorted order and the canonical 2-space layout, no empty
// translation, and the same fmt verbs, HTML tags and entities as the English
// key, so that T never formats a translation with the wrong arguments.
func TestCatalogs(t *testing.T) {
	files, err := fs.Glob(ui.FS, "i18n/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no catalogs: %v", err)
	}
	cats, err := i18n.Load(ui.FS, "i18n")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		lang := strings.TrimSuffix(strings.TrimPrefix(name, "i18n/"), ".json")
		t.Run(lang, func(t *testing.T) {
			if !cats.Has(lang) {
				t.Fatalf("Load did not read %s", name)
			}
			raw, err := fs.ReadFile(ui.FS, name)
			if err != nil {
				t.Fatal(err)
			}
			keys := orderedKeys(t, raw)
			for i := 1; i < len(keys); i++ {
				if keys[i-1] >= keys[i] {
					t.Errorf("keys not sorted or repeated: %q before %q", keys[i-1], keys[i])
				}
			}
			m := map[string]string{}
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			enc := json.NewEncoder(&buf)
			enc.SetEscapeHTML(false)
			enc.SetIndent("", "  ")
			if err := enc.Encode(m); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(buf.Bytes(), raw) {
				t.Errorf("%s is not in canonical form (sorted keys, 2-space indent, no HTML escaping, final newline)", name)
			}
			for _, k := range slices.Sorted(maps.Keys(m)) {
				v := m[k]
				if strings.TrimSpace(v) == "" {
					t.Errorf("%q: empty translation", k)
					continue
				}
				if kv, vv := verbs(k), verbs(v); !maps.Equal(kv, vv) {
					t.Errorf("%q: fmt verbs %v, translation %q has %v", k, kv, v, vv)
				}
				if kt, vt := markup(k), markup(v); !slices.Equal(kt, vt) {
					t.Errorf("%q: tags and entities %v, translation %q has %v", k, kt, v, vt)
				}
			}
		})
	}
}

// orderedKeys returns the keys of a flat JSON object in file order.
func orderedKeys(t *testing.T, raw []byte) []string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		t.Fatalf("not a JSON object: %v %v", tok, err)
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		k, ok := tok.(string)
		if !ok {
			t.Fatalf("bad key %v", tok)
		}
		var v string
		if err := dec.Decode(&v); err != nil {
			t.Fatalf("%q: value is not a string: %v", k, err)
		}
		keys = append(keys, k)
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		t.Fatalf("trailing data: %v %v", tok, err)
	}
	if dec.More() {
		t.Fatal("more than one JSON value")
	}
	return keys
}

var verbRe = regexp.MustCompile(`%(?:\[(\d+)\])?[-+# 0]*(?:\d+|\*)?(?:\.(?:\d+|\*))?([a-zA-Z%])`)

// verbs maps each argument number to the verb that formats it, following
// fmt's rules: arguments are taken in order and %[n] moves to argument n.
func verbs(s string) map[int]string {
	out := map[int]string{}
	arg := 1
	for _, m := range verbRe.FindAllStringSubmatch(s, -1) {
		if m[2] == "%" {
			continue
		}
		if m[1] != "" {
			arg, _ = strconv.Atoi(m[1])
		}
		out[arg] += m[2] // two verbs for one argument show up as "dd"
		arg++
	}
	return out
}

var markupRe = regexp.MustCompile(`</?[a-zA-Z][a-zA-Z0-9]*|&(?:[a-zA-Z]+|#\d+|#x[0-9a-fA-F]+);`)

// markup lists the HTML tag names and character references in s, sorted.
func markup(s string) []string {
	return slices.Sorted(slices.Values(markupRe.FindAllString(s, -1)))
}

func TestVerbs(t *testing.T) {
	for _, c := range []struct {
		a, b string
		same bool
	}{
		{"%d posts", "%d entradas", true},
		{"%s by %s on %s", "%[1]s, de %[2]s, el %[3]s", true},
		{"%s has %d", "%[2]d en %[1]s", true},
		{"%s has %d", "%d en %s", false},
		{"100%% of %d", "el 100 %% de %d", true},
		{"%.1f MB", "%.1f MB", true},
		{"%d", "%s", false},
		{"%s", "", false},
	} {
		if got := maps.Equal(verbs(c.a), verbs(c.b)); got != c.same {
			t.Errorf("%q vs %q: same = %v", c.a, c.b, got)
		}
	}
}
