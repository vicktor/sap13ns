package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DynHand is a dynamic call site whose keys are listed in handSources.
const DynHand = "hand"

// handSources cover dynamic call sites the scanner cannot follow: strings
// passed around as data and translated later. Each key must still be written
// as a literal in From (a warning says when one disappears), and the call
// site At ("file" plus the expression the scanner prints) is reported as
// covered by hand rather than unresolved.
var handSources = []struct {
	At, Expr string
	From     string
	Keys     []string
}{
	{
		// The reason semantic search is off, shown after "Full text only".
		// Error texts (err.Error()) stay in English.
		At: "ui/admin/templates/assistant_settings.html", Expr: "T .",
		From: "internal/assistant/engine.go",
		Keys: []string{
			"embeddings are off: full-text search only",
			"the AI provider is switched off (Settings › AI provider)",
			"Claude has no embeddings API: choose an embeddings provider",
			"pgvector is not available: full-text search only",
		},
	},
}

func (e *Extractor) applyHandSources(root string) {
	for _, h := range handSources {
		if _, err := os.Stat(filepath.Join(root, h.At)); err != nil {
			continue // not this tree (tests scan small fixtures)
		}
		b, err := os.ReadFile(filepath.Join(root, h.From))
		if err != nil {
			e.Warnings = append(e.Warnings, fmt.Sprintf("hand source %s: %v", h.From, err))
			continue
		}
		src := string(b)
		for _, k := range h.Keys {
			i := strings.Index(src, strconv.Quote(k))
			if i < 0 {
				e.Warnings = append(e.Warnings, fmt.Sprintf("hand source %s no longer has %q: update handSources in scripts/i18ncheck/hand.go", h.From, k))
				continue
			}
			e.add(k, Loc{File: h.From, Line: 1 + strings.Count(src[:i], "\n")})
		}
		for i, d := range e.Dynamic {
			if d.Loc.File == h.At && d.Expr == h.Expr && d.Kind == DynUnresolved {
				e.Dynamic[i].Kind = DynHand
			}
		}
	}
}
