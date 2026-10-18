// Package i18n provides message catalogs. Source strings are English; each
// catalog file (ui/i18n/<lang>.json) maps an English string to its translation.
package i18n

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"time"
)

type Catalogs struct {
	langs map[string]map[string]string
}

// Load reads every <lang>.json in dir.
func Load(fsys fs.FS, dir string) (*Catalogs, error) {
	c := &Catalogs{langs: map[string]map[string]string{"en": {}}}
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		m := map[string]string{}
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, fmt.Errorf("i18n %s: %w", e.Name(), err)
		}
		c.langs[strings.TrimSuffix(e.Name(), ".json")] = m
	}
	return c, nil
}

func (c *Catalogs) Languages() []string {
	out := []string{}
	for k := range c.langs {
		out = append(out, k)
	}
	return out
}

func (c *Catalogs) Has(lang string) bool { _, ok := c.langs[lang]; return ok }

// T translates key into lang, formatting args with fmt verbs when given.
func (c *Catalogs) T(lang, key string, args ...any) string {
	s := key
	if m, ok := c.langs[lang]; ok {
		if v, ok := m[key]; ok && v != "" {
			s = v
		}
	}
	if len(args) > 0 {
		return fmt.Sprintf(s, args...)
	}
	return s
}

var months = map[string][]string{
	"es": {"enero", "febrero", "marzo", "abril", "mayo", "junio", "julio", "agosto", "septiembre", "octubre", "noviembre", "diciembre"},
	"en": {"January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"},
}

func MonthName(lang string, m time.Month) string {
	names, ok := months[lang]
	if !ok {
		names = months["en"]
	}
	return names[m-1]
}

// FormatDate renders a long, localized date.
func FormatDate(lang string, t time.Time) string {
	if lang == "es" {
		return fmt.Sprintf("%d de %s de %d", t.Day(), MonthName(lang, t.Month()), t.Year())
	}
	return fmt.Sprintf("%s %d, %d", MonthName(lang, t.Month()), t.Day(), t.Year())
}

// FormatShortDate renders a compact date (e.g. 18 oct 2026).
func FormatShortDate(lang string, t time.Time) string {
	m := MonthName(lang, t.Month())
	if len([]rune(m)) > 3 {
		m = string([]rune(m)[:3])
	}
	if lang == "es" {
		return fmt.Sprintf("%d %s %d", t.Day(), m, t.Year())
	}
	return fmt.Sprintf("%s %d, %d", m, t.Day(), t.Year())
}

// Ago renders a relative time such as "hace 5 min" / "5 min ago".
func (c *Catalogs) Ago(lang string, t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return c.T(lang, "just now")
	case d < time.Hour:
		return c.T(lang, "%d min ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return c.T(lang, "%d h ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return c.T(lang, "%d days ago", int(d.Hours()/24))
	default:
		return FormatShortDate(lang, t)
	}
}
