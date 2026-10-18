package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"os"
	"path/filepath"
	"strings"
)

// DynTheme is a call of the themeT template function, whose keys are the
// strings of the theme manifests (scanned by ScanThemeManifest).
const DynTheme = "theme"

// templateThemeFuncs translate a theme manifest string: {{themeT $th .Label}}.
var templateThemeFuncs = map[string]bool{"themeT": true}

// goThemeFuncs do the same in Go: s.themeText(th, o.Label).
var goThemeFuncs = map[string]bool{"themeText": true}

func themeCall(call *ast.CallExpr) bool {
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		return goThemeFuncs[fun.Sel.Name]
	case *ast.Ident:
		return goThemeFuncs[fun.Name]
	}
	return false
}

// scanThemes extracts the strings of every built-in theme's manifest,
// ui/themes/<slug>/theme.json.
func (e *Extractor) scanThemes(root string) error {
	files, err := filepath.Glob(filepath.Join(root, "ui", "themes", "*", "theme.json"))
	if err != nil {
		return err
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		if err := e.ScanThemeManifest(rel(root, f), b); err != nil {
			return err
		}
	}
	return nil
}

// ScanThemeManifest extracts the strings of a theme.json that the admin
// shows through themeT: the description, the labels of menus and widget
// areas, widget area descriptions, and the label, help and choice labels of
// each option. The theme's name is a name and stays as it is.
func (e *Extractor) ScanThemeManifest(file string, raw []byte) error {
	var m struct {
		Description string `json:"description"`
		Menus       []struct {
			Label string `json:"label"`
		} `json:"menus"`
		WidgetAreas []struct {
			Label       string `json:"label"`
			Description string `json:"description"`
		} `json:"widget_areas"`
		Options []struct {
			Label   string `json:"label"`
			Help    string `json:"help"`
			Choices []struct {
				Label string `json:"label"`
			} `json:"choices"`
		} `json:"options"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	src := string(raw)
	add := func(s string) {
		if s == "" {
			return
		}
		line := 1
		var q bytes.Buffer
		enc := json.NewEncoder(&q)
		enc.SetEscapeHTML(false)
		if enc.Encode(s) == nil {
			if i := strings.Index(src, strings.TrimSpace(q.String())); i >= 0 {
				line += strings.Count(src[:i], "\n")
			}
		}
		e.addTable(s, Loc{File: file, Line: line})
	}
	add(m.Description)
	for _, x := range m.Menus {
		add(x.Label)
	}
	for _, x := range m.WidgetAreas {
		add(x.Label)
		add(x.Description)
	}
	for _, o := range m.Options {
		add(o.Label)
		add(o.Help)
		for _, c := range o.Choices {
			add(c.Label)
		}
	}
	return nil
}
