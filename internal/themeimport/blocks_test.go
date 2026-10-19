package themeimport

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestParseBlocks(t *testing.T) {
	src := `<p>freeform</p>
<!-- wp:group {"layout":{"type":"constrained"},"n":3} -->
<div class="wp-block-group"><!-- wp:paragraph --><p>Hi</p><!-- /wp:paragraph --><!-- wp:site-title {"level":0} /--></div>
<!-- /wp:group -->
<!-- wp:my-plugin/widget /-->
<!-- a plain comment -->
<!-- wp:Upper /-->`
	bs, err := ParseBlocks(src)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, b := range bs {
		names = append(names, b.Name)
	}
	if got := strings.Join(names, ","); got != ",core/group,my-plugin/widget," {
		t.Fatalf("top-level blocks = %q", got)
	}
	g := bs[1]
	if n, ok := g.Attrs["n"].(json.Number); !ok || n.String() != "3" {
		t.Errorf("numbers stay json.Number: %#v", g.Attrs["n"])
	}
	if attrString(attrMap(g.Attrs, "layout"), "type") != "constrained" {
		t.Errorf("nested attributes: %#v", g.Attrs)
	}
	if len(g.Inner) != 2 || g.Inner[0].Name != "core/paragraph" || g.Inner[1].Name != "core/site-title" {
		t.Fatalf("inner blocks: %+v", g.Inner)
	}
	// The parts interleave the group's HTML and its inner blocks.
	var parts []string
	for _, p := range g.Parts {
		if p.Block >= 0 {
			parts = append(parts, "#")
		} else {
			parts = append(parts, strings.TrimSpace(p.HTML))
		}
	}
	if got := strings.Join(parts, "|"); got != `<div class="wp-block-group">|#|#|</div>` {
		t.Errorf("group parts = %q", got)
	}
	// Text that is not a delimiter stays HTML.
	last := bs[len(bs)-1]
	if last.Name != "" || !strings.Contains(last.Parts[0].HTML, "a plain comment") || !strings.Contains(last.Parts[0].HTML, "wp:Upper") {
		t.Errorf("plain comments are freeform HTML: %+v", last.Parts)
	}
}

func TestParseBlocksRecovery(t *testing.T) {
	t.Run("a closer closes the blocks opened after its opener", func(t *testing.T) {
		bs, err := ParseBlocks(`<!-- wp:group --><!-- wp:columns --><!-- wp:column -->x<!-- /wp:group --><!-- wp:paragraph /-->`)
		if err != nil {
			t.Fatal(err)
		}
		if len(bs) != 2 || bs[0].Name != "core/group" || bs[1].Name != "core/paragraph" {
			t.Fatalf("blocks = %+v", bs)
		}
	})
	t.Run("a closer without its opener is dropped", func(t *testing.T) {
		bs, _ := ParseBlocks(`<!-- /wp:group --><!-- wp:paragraph --><p>a</p><!-- /wp:paragraph -->`)
		if len(bs) != 1 || bs[0].Name != "core/paragraph" {
			t.Fatalf("blocks = %+v", bs)
		}
	})
	t.Run("blocks still open at the end are closed there", func(t *testing.T) {
		bs, _ := ParseBlocks(`<!-- wp:group --><p>a</p>`)
		if len(bs) != 1 || len(bs[0].Parts) != 1 {
			t.Fatalf("blocks = %+v", bs)
		}
	})
	t.Run("bad JSON keeps the block without attributes", func(t *testing.T) {
		bs, _ := ParseBlocks(`<!-- wp:group {"layout":} /-->`)
		if len(bs) != 1 || !bs[0].BadJSON || len(bs[0].Attrs) != 0 {
			t.Fatalf("blocks = %+v", bs)
		}
	})
	t.Run("attributes that are not an object are not a delimiter", func(t *testing.T) {
		bs, _ := ParseBlocks(`<!-- wp:group [1,2] /-->`)
		if len(bs) != 1 || bs[0].Name != "" {
			t.Fatalf("blocks = %+v", bs)
		}
	})
	t.Run("too deep", func(t *testing.T) {
		if _, err := ParseBlocks(strings.Repeat(`<!-- wp:group -->`, maxBlockDepth+1)); err == nil {
			t.Fatal("no error for blocks nested too deeply")
		}
	})
	t.Run("too many", func(t *testing.T) {
		if _, err := ParseBlocks(strings.Repeat(`<!-- wp:spacer /-->`, maxBlocks+1)); err == nil {
			t.Fatal("no error for too many blocks")
		}
	})
}

// A megabyte of delimiters that never close parses in linear time: the
// "-->" search runs once, not once per candidate (it was quadratic).
func TestParseBlocksUnclosedIsLinear(t *testing.T) {
	for name, unit := range map[string]string{
		"opener":     "<!-- wp:a ",
		"attributes": `<!-- wp:a {"x":1} `,
		"closer":     "<!-- /wp:a ",
		"comment":    "<!-- ",
	} {
		t.Run(name, func(t *testing.T) {
			src := strings.Repeat(unit, (1<<20)/len(unit))
			start := time.Now()
			bs, err := ParseBlocks(src)
			if err != nil {
				t.Fatal(err)
			}
			if d := time.Since(start); d > 2*time.Second {
				t.Fatalf("parsing 1 MiB of unclosed delimiters took %v", d)
			}
			if len(bs) != 1 || bs[0].Name != "" {
				t.Fatalf("unclosed delimiters are text: %d blocks", len(bs))
			}
		})
	}
	// Closed ones after a long unclosed run still parse.
	src := strings.Repeat("<!-- wp:a ", 10000) + "--><p>x</p><!-- /wp:a -->"
	start := time.Now()
	if _, err := ParseBlocks(src); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %v", d)
	}
}
