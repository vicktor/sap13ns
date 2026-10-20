// Package worldmap holds the world map and the country names of the
// analytics page: SVG path data per ISO 3166-1 alpha-2 code, from Natural
// Earth's 1:110m countries (public domain, through world-atlas, ISC), and
// the CLDR names of every country. scripts/worldmap generates both files.
package worldmap

import (
	_ "embed"
	"encoding/json"
	"strings"
	"sync"
)

//go:embed world.json
var worldJSON []byte

//go:embed names.json
var namesJSON []byte

// Shape is the outline of one country; a country may have several.
type Shape struct {
	Code string `json:"id"` // ISO 3166-1 alpha-2
	D    string `json:"d"`  // SVG path data
}

// Map is the projected world (Equal Earth, no Antarctica).
type Map struct {
	W, H   int
	Shapes []Shape
}

var load = sync.OnceValues(func() (*Map, map[string]map[string]string) {
	var w struct {
		W, H   int
		Shapes []Shape
	}
	var names map[string]map[string]string
	if err := json.Unmarshal(worldJSON, &w); err != nil {
		panic("worldmap: " + err.Error())
	}
	if err := json.Unmarshal(namesJSON, &names); err != nil {
		panic("worldmap: " + err.Error())
	}
	return &Map{W: w.W, H: w.H, Shapes: w.Shapes}, names
})

// World returns the map. Callers must not modify it.
func World() *Map {
	m, _ := load()
	return m
}

// Valid reports whether code is a country the names know.
func Valid(code string) bool {
	_, names := load()
	_, ok := names["en"][code]
	return ok
}

// Name is the name of the country code in lang (a tag such as "es" or
// "es-MX"), in English when lang has none, and the code itself when the
// country is unknown.
func Name(code, lang string) string {
	_, names := load()
	base, _, _ := strings.Cut(strings.ToLower(lang), "-")
	if n := names[base][code]; n != "" {
		return n
	}
	if n := names["en"][code]; n != "" {
		return n
	}
	return code
}
