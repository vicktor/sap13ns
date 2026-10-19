package themeimport

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Patterns that each include the next ten times stay within the stack's
// depth but would multiply to 10^11 blocks: the conversion's budget stops
// them early, and the conversion fails with a clear error.
func TestPatternFanOut(t *testing.T) {
	files := minimal(`<!-- wp:pattern {"slug":"mini/p0"} /-->`)
	for i := range 11 {
		var b strings.Builder
		b.WriteString("<?php\n/**\n * Title: P\n * Slug: mini/p" + strconv.Itoa(i) + "\n */\n?>")
		b.WriteString(`<!-- wp:paragraph --><p>` + strings.Repeat("x", 200) + `</p><!-- /wp:paragraph -->`)
		for range 10 {
			b.WriteString(`<!-- wp:group --><div class="wp-block-group"><!-- wp:pattern {"slug":"mini/p` + strconv.Itoa(i+1) + `"} /--></div><!-- /wp:group -->`)
		}
		files["mini/patterns/p"+strconv.Itoa(i)+".php"] = []byte(b.String())
	}
	start := time.Now()
	_, err := convertFiles(t, files)
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("the conversion took %v", d)
	}
	if !errors.Is(err, errBudget) {
		t.Fatalf("err = %v, want the budget error", err)
	}

	// Fan-out at the top level of a template (flatten) is bounded too.
	files = minimal(`<!-- wp:pattern {"slug":"mini/q0"} /-->`)
	for i := range 4 {
		files["mini/patterns/q"+strconv.Itoa(i)+".php"] = []byte("<?php\n/**\n * Title: Q\n * Slug: mini/q" + strconv.Itoa(i) + "\n */\n?>" +
			strings.Repeat(`<!-- wp:pattern {"slug":"mini/q`+strconv.Itoa(i+1)+`"} /-->`, 400))
	}
	files["mini/patterns/q4.php"] = []byte("<?php\n/**\n * Title: Q\n * Slug: mini/q4\n */\n?><!-- wp:paragraph --><p>x</p><!-- /wp:paragraph -->")
	start = time.Now()
	_, err = convertFiles(t, files)
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("the conversion took %v", d)
	}
	if !errors.Is(err, errBudget) {
		t.Fatalf("err = %v, want the budget error", err)
	}

	// A theme within the budget converts as before.
	mustConvert(t, fixture(t))
}

// A pattern of many PHP segments over many lines reads in linear time,
// and only its first maxPHPSegments segments are read.
func TestPatternManySegments(t *testing.T) {
	src := "<?php\n/**\n * Title: P\n * Slug: x/p\n */\n?>" + strings.Repeat("<p>a</p>\n<?php echo 'b'; ?>\n", 100_000)
	start := time.Now()
	p := parsePattern("patterns/p.php", src)
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("parsing took %v", d)
	}
	if n := strings.Count(p.Markup, "b"); n != maxPHPSegments-1 {
		t.Errorf("%d segments evaluated, want %d", n, maxPHPSegments-1)
	}
	// The header is segment 1; repeat k is on line 7+2(k-1), and the one
	// past the bound is repeat maxPHPSegments.
	if len(p.Skipped) != 1 || p.Skipped[0] != 7+2*(maxPHPSegments-1) {
		t.Errorf("skipped: %v", p.Skipped)
	}
	if strings.Contains(p.Markup, "<?") {
		t.Error("PHP past the bound reached the markup")
	}
}

// Deeply nested at-rules are dropped by cleanCSS, refused by balanced,
// and modeRules stops at a fixed depth: no quadratic re-scanning.
func TestCSSNesting(t *testing.T) {
	deep := strings.Repeat("@media screen{", 5000) + "a{color:#fff}" + strings.Repeat("}", 5000)
	start := time.Now()
	if out, n := cleanCSS(deep+"\nb{color:#000}", func(string) (string, bool) { return "", false }); strings.Contains(out, "@media") || n != 1 || !strings.Contains(out, "b{color:#000}") {
		t.Errorf("cleanCSS = %q, %d dropped", out, n)
	}
	if balanced(deep) {
		t.Error("balanced accepts 5000 nested blocks")
	}
	if out := modeRules(deep, darken, true); strings.Contains(out, "a{") || strings.Contains(out, ".dark a") {
		t.Errorf("modeRules went past its depth: %q", out[:min(len(out), 200)])
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %v", d)
	}

	// Within the depth, nothing changes.
	ok := "@media screen{@supports (display:grid){a{color:#fff}}}"
	if out, n := cleanCSS(ok, func(string) (string, bool) { return "", false }); n != 0 || !strings.Contains(out, "a{color:#fff}") {
		t.Errorf("cleanCSS(%q) = %q, %d dropped", ok, out, n)
	}
	if !balanced(ok) {
		t.Error("balanced refuses three levels")
	}
	if out := modeRules(ok, darken, true); !strings.Contains(out, "@supports") {
		t.Errorf("modeRules = %q", out)
	}

	// Through a whole conversion: a deep style.css is cleaned, not choked on.
	files := minimal(`<!-- wp:paragraph --><p>x</p><!-- /wp:paragraph -->`)
	files["mini/style.css"] = append(files["mini/style.css"], []byte(deep)...)
	start = time.Now()
	res := mustConvert(t, files)
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("the conversion took %v", d)
	}
	if strings.Count(string(res.Files["static/theme.css"]), "@media screen{@media screen{@media screen{@media screen{@media screen{@media screen{@media") > 0 {
		t.Error("the deep CSS reached theme.css")
	}
}
