package themeimport

import (
	"strings"
	"testing"
)

func TestEvalPHP(t *testing.T) {
	cases := []struct {
		seg  string
		echo bool
		want string
	}{
		{` esc_html_e( 'Hello', 'theme' ); `, false, "Hello"},
		{` echo esc_html__( 'A & B', 'theme' ); `, false, "A &amp; B"},
		{` echo esc_html( '©&nbsp;2026 &amp; <b>' ); `, false, "©&nbsp;2026 &amp; &lt;b&gt;"}, // no entity encoded twice, as WordPress
		{` echo 'a' . "b\n" . 'c'; `, false, "ab\nc"},
		{` printf( esc_html__( '%1$s by %2$s', 'theme' ), 'Theme', 'Me' ); `, false, "Theme by Me"},
		{` echo esc_url( get_template_directory_uri() ); `, false, sentinelThemeURI},
		{` echo esc_url( home_url( '/' ) ); `, false, sentinelHome},
		{` echo esc_html( gmdate( 'Y' ) ); `, false, sentinelYear},
		{` bloginfo( 'name' ); `, false, sentinelTitle},
		{` esc_html__( 'Hi', 'theme' ) `, true, "Hi"},
		{"\n/**\n * Title: X\n */\n", false, ""},
		{` // a comment
		echo 'x'; `, false, "x"},
		{` $a = __( 'One', 'theme' ); $b = $a . ' two'; echo esc_html( $b ); `, false, "One two"},
	}
	for _, tc := range cases {
		got, err := evalPHP(tc.seg, tc.echo, nil)
		if err != nil || got != tc.want {
			t.Errorf("evalPHP(%q) = %q, %v; want %q", tc.seg, got, err, tc.want)
		}
	}
	// Anything else is not run: it is left out.
	for _, seg := range []string{
		` system( 'id' ); `,
		` echo shell_exec( 'id' ); `,
		` include 'x.php'; `,
		` eval( 'echo 1;' ); `,
		` echo $undefined; `,
		` echo "Hi $name"; `,
		` foreach ( $a as $b ) { echo $b; } `,
		` echo get_bloginfo( 'admin_email' ); `,
		` echo gmdate( 'Y-m-d' ); `,
		` $f = 'system'; $f( 'id' ); `,
		` echo ` + "`id`" + `; `,
		` echo 'unterminated; `,
		` new Foo(); `,
	} {
		if got, err := evalPHP(seg, false, nil); err == nil {
			t.Errorf("evalPHP(%q) = %q, want it left out", seg, got)
		}
	}
	// Bounds on variables.
	var b strings.Builder
	for i := range maxPHPVars + 1 {
		b.WriteString(" $v" + strings.Repeat("x", i) + " = 'a';")
	}
	if _, err := evalPHP(b.String(), false, nil); err == nil {
		t.Error("too many variables")
	}
	if _, err := evalPHP(` $a = '`+strings.Repeat("a", maxPHPVarBytes+1)+`';`, false, nil); err == nil {
		t.Error("a variable too large")
	}
}

func TestParsePattern(t *testing.T) {
	p := parsePattern("patterns/hero.php", "<?php\n/**\n * Title: Hero\n * Slug: x/hero\n * Block Types: core/template-part/header\n */\n?>\n"+
		"<!-- wp:paragraph --><p><?php esc_html_e( 'Welcome', 'x' ); ?></p><!-- /wp:paragraph -->\n<?php system('id'); ?><?= esc_html__( 'Short', 'x' ) ?>")
	if p.Slug != "x/hero" || p.Title != "Hero" {
		t.Errorf("header: %+v", p)
	}
	if !strings.Contains(p.Markup, "<p>Welcome</p>") || !strings.Contains(p.Markup, "Short") || strings.Contains(p.Markup, "system") {
		t.Errorf("markup: %q", p.Markup)
	}
	if len(p.Skipped) != 1 || p.Skipped[0] != 9 {
		t.Errorf("skipped lines: %v", p.Skipped)
	}
	// "?>" inside a string does not end the segment.
	p = parsePattern("p.php", "<?php echo 'a ?> b'; ?>c")
	if p.Markup != "a ?&gt; bc" && p.Markup != "a ?> bc" {
		t.Errorf("markup: %q", p.Markup)
	}
}

// A theme cannot forge the importer's marks: not with the runes
// themselves, not with entities, not with PHP literals, not with
// invalid UTF-8.
func TestSentinelForgery(t *testing.T) {
	raw := sentinel("title") + sentinel("year") + "0"
	p := parsePattern("p.php", "<p>"+raw+"</p><?php echo '"+raw+"'; ?>")
	if strings.ContainsAny(p.Markup, sentinelOpen+sentinelClose+holeOpen+holeClose) {
		t.Errorf("raw marks survive: %q", p.Markup)
	}
	if p := parsePattern("p.php", "<p>\xee\x82</p><?php echo '\xa0'; ?>"); !p.Invalid {
		t.Error("a pattern that is not UTF-8 is left out")
	}
	if _, err := literal("'\xff'"); err == nil {
		t.Error("an invalid UTF-8 literal is refused")
	}
	if s, _ := literal(sentinel("home")); s != "home" {
		t.Errorf("literal keeps marks: %q", s)
	}

	// Through a whole conversion: the runes, their entities (decoded by
	// the HTML tokenizer) and a forged hole all stay inert text.
	forged := "&#xE0A0;title&#xE0A1;|&#57504;year&#57505;|" + raw + "|&#xE0B0;0&#xE0B1;|{{.Site.Viewer}}"
	res := mustConvert(t, minimal(`<!-- wp:paragraph --><p>`+forged+`</p><!-- /wp:paragraph -->`+
		`<!-- wp:pattern {"slug":"mini/p"} /-->`,
		"patterns/p.php", "<?php\n/**\n * Title: P\n * Slug: mini/p\n */\n?><p class=\"x\" title=\""+forged+"\"><?php echo '"+raw+"'; ?></p>"))
	home := content(t, res, "home")
	for _, bad := range []string{"$.Site.Title", "$.Site.Year", "{{.Site.Viewer}}", "{{template", sentinelOpen, holeOpen} {
		if strings.Contains(home, bad) {
			t.Errorf("forged mark %q became live: %s", bad, home)
		}
	}
	if !strings.Contains(home, "&#123;&#123;.Site.Viewer}}") {
		t.Errorf("braces are not escaped: %s", home)
	}
}
