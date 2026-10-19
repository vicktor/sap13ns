package themeimport

import (
	"strings"
	"testing"
)

func TestReadBlockStyles(t *testing.T) {
	src := `<?php
add_action( 'init', function () {
	register_block_style(
		'core/list',
		array(
			'name'         => 'checkmark-list',
			'label'        => __( 'Checkmark', 'x' ), // a call is skipped, not run
			/* 'inline_style' => 'ignored' */
			'inline_style' => '
			ul.is-style-checkmark-list { list-style-type: "\2713"; }',
		)
	);
	register_block_style( 'core/heading', [ 'name' => 'asterisk', 'inline_style' => ".is-style-asterisk:before { content: ''; }" ] );
	register_block_style( 'core/quote', array( 'name' => 'var', 'inline_style' => $css ) );
	register_block_style( 'core/quote', array( 'name' => 'dollar', 'inline_style' => "a { color: $c; }" ) );
	register_block_style( $block, array( 'name' => 'x', 'inline_style' => 'a{}' ) );
	register_block_style( 'core/quote', array( 'name' => 'Bad Name', 'inline_style' => 'a { color: red; }' ) );
} );
register_block_style( 'core/quote', array( 'name' => 'unclosed', 'inline_style' => 'a { color: red; }'`
	got := readBlockStyles(src)
	if len(got) != 2 {
		t.Fatalf("styles: %+v", got)
	}
	if got[0].Block != "core/list" || got[0].Name != "checkmark-list" || !strings.Contains(got[0].CSS, `"\2713"`) {
		t.Errorf("first: %+v", got[0])
	}
	if got[1].Block != "core/heading" || got[1].Name != "asterisk" || !strings.Contains(got[1].CSS, "content: ''") {
		t.Errorf("second: %+v", got[1])
	}
}

// The CSS of a block style is foreign CSS: cleaned like style.css, it
// cannot load files, close the stylesheet or reach the page's scripts.
func TestBlockStylesConverted(t *testing.T) {
	php := `<?php
register_block_style( 'core/list', array( 'name' => 'tick', 'inline_style' => 'ul.is-style-tick { list-style-type: "\2713"; }' ) );
register_block_style( 'core/list', array( 'name' => 'evil', 'inline_style' => '@import url(https://evil.example/x.css); .is-style-evil { background: url(https://evil.example/t.png); } </style><script>alert(1)</script>' ) );
system( 'id' );`
	res := mustConvert(t, minimal(`<!-- wp:paragraph --><p>x</p><!-- /wp:paragraph -->`, "functions.php", php))
	css := string(res.Files["static/theme.css"])
	if !strings.Contains(css, `ul.is-style-tick { list-style-type: "\2713"; }`) && !strings.Contains(css, `ul.is-style-tick{list-style-type:"\2713"}`) {
		t.Errorf("the block style is missing")
	}
	for _, bad := range []string{"evil.example", "<script", "</style", "@import", "alert(1)", "system("} {
		if strings.Contains(css, bad) {
			t.Errorf("theme.css has %s", bad)
		}
	}
	if !hasNote(res.Report, "theme.block-styles") {
		t.Error("no theme.block-styles note")
	}
}
