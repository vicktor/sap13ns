package sitecheck

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"sap13ns/internal/store"
)

// MenuItem checks a menu item as the menus page does: a label of at most
// 100 characters and a link MenuURL accepts.
func MenuItem(label, link string) bool {
	return strings.TrimSpace(label) != "" && !TooLong(label, 100) && MenuURL(link)
}

// BuiltinWidgets are the widget kinds of sap13ns itself; plugins add
// "plugin:<id>:<widget>".
var BuiltinWidgets = []string{"recent_posts", "categories", "tags", "archives", "search", "feeds", "text"}

// MaxWidgetText is the size of a text widget, in bytes.
const MaxWidgetText = 10000

// WidgetConfig is the settings of a built-in widget, normalised as the
// widgets page does: a recent posts count of 1 to 20 (5 otherwise), a text
// of at most MaxWidgetText bytes, nothing else.
func WidgetConfig(kind string, in map[string]string) map[string]string {
	cfg := map[string]string{}
	switch kind {
	case "recent_posts":
		n, err := strconv.Atoi(in["count"])
		if err != nil || n < 1 || n > 20 {
			n = 5
		}
		cfg["count"] = strconv.Itoa(n)
	case "text":
		t := in["text"]
		if len(t) > MaxWidgetText {
			t = t[:MaxWidgetText]
			for len(t) > 0 && !utf8.ValidString(t) {
				t = t[:len(t)-1]
			}
		}
		cfg["text"] = t
	}
	return cfg
}

// WidgetTitle cuts a widget's title to its 100 characters.
func WidgetTitle(t string) string {
	t = strings.TrimSpace(t)
	if TooLong(t, 100) {
		t = string([]rune(t)[:100])
	}
	return t
}

// Widget checks the place of a widget: an area name, and a language of
// the site or "" for all. The kind and its settings are checked by
// WidgetConfig, or by the plugin that has it.
func Widget(w store.Widget, langs []string) bool {
	return Slug.MatchString(w.Area) && (w.Lang == "" || slices.Contains(langs, w.Lang))
}

// MaxThemeOption is the length of a theme option, in characters.
const MaxThemeOption = 2000

var optionKey = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// ThemeOptions keeps the theme options a page could save: names like
// those of theme.json, valid text of at most MaxThemeOption characters,
// at most 200 of them. The theme sets each option's default and checks
// its choices whenever it reads them. It names the options left out.
func ThemeOptions(in map[string]string) (map[string]string, []string) {
	out := map[string]string{}
	var bad []string
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		v := strings.TrimSpace(in[k])
		if !optionKey.MatchString(k) || !utf8.ValidString(v) || TooLong(v, MaxThemeOption) || len(out) >= 200 {
			bad = append(bad, k)
			continue
		}
		out[k] = v
	}
	return out, bad
}
