package content

import (
	"strings"
	"testing"
)

func TestSanitizeHTMLBlockKeeps(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`<section class="promo grid" id="offer"><h2>Offer</h2><p>Text with <strong>bold</strong> and <a href="/about">a link</a>.</p></section>`,
			`<section class="promo grid" id="offer"><h2>Offer</h2><p>Text with <strong>bold</strong> and <a href="/about">a link</a>.</p></section>`},
		{`<figure><img src="/uploads/a.png" alt="A: picture" width="640" height="480" loading="lazy"><figcaption>Caption</figcaption></figure>`,
			`<figure><img src="/uploads/a.png" alt="A: picture" width="640" height="480" loading="lazy"><figcaption>Caption</figcaption></figure>`},
		{`<details open><summary>More</summary><p>Hidden text</p></details>`, `<details open=""><summary>More</summary><p>Hidden text</p></details>`},
		{`<table><thead><tr><th scope="col">A</th></tr></thead><tbody><tr><td colspan="2">1</td></tr></tbody></table>`,
			`<table><thead><tr><th scope="col">A</th></tr></thead><tbody><tr><td colspan="2">1</td></tr></tbody></table>`},
		{`<ul role="list" aria-label="Steps"><li data-step="1">One</li></ul>`, `<ul role="list" aria-label="Steps"><li data-step="1">One</li></ul>`},
		{`<a href="mailto:hi@example.com">Mail</a> <a href="tel:+34600000000">Call</a>`, `<a href="mailto:hi@example.com">Mail</a> <a href="tel:+34600000000">Call</a>`},
		{`<a class="btn" data-variant="outline" href="https://example.com">Go</a>`, `<a class="btn" data-variant="outline" href="https://example.com">Go</a>`},
		{`<a href="https://example.com" target="_blank">New tab</a>`, `<a href="https://example.com" target="_blank" rel="noopener">New tab</a>`},
		{`<header><nav aria-label="Secondary"><ol><li><a href="#a" aria-current="page">A</a></li></ol></nav></header>`,
			`<header><nav aria-label="Secondary"><ol><li><a href="#a" aria-current="page">A</a></li></ol></nav></header>`},
	} {
		if got := SanitizeHTMLBlock(tc.in); got != tc.want {
			t.Errorf("SanitizeHTMLBlock(%q)\n got %q\nwant %q", tc.in, got, tc.want)
		}
	}
}

// TestSanitizeHTMLBlockHostile: nothing that runs scripts, styles the page
// inline, frames or posts anything survives (as markup: escaped text is
// harmless).
func TestSanitizeHTMLBlockHostile(t *testing.T) {
	forbidden := []string{"<script", "<style", "style=", "onerror", "onclick", "onload", "onmouseover", "<iframe", "<object", "<embed",
		"<form", "<input", "<button", "<textarea", "<select", "javascript:", "vbscript:", "data:text", "<svg", "<math", "<base", "<meta", "<link",
		"srcdoc", "formaction", "data-video-src", "data-form", "data-assistant", "data-theme-toggle", "<template", "<noscript"}
	for _, in := range []string{
		`<script>alert(1)</script>`,
		`<SCRIPT SRC=//evil.example/x.js></SCRIPT>`,
		`<scr<script>ipt>alert(1)</script>`,
		`<img src=x onerror=alert(1)>`,
		`<img src="x" OnError="alert(1)">`,
		`<p onclick="alert(1)">x</p>`,
		`<body onload=alert(1)>`,
		`<div style="background:url(javascript:alert(1))">x</div>`,
		`<style>body{display:none}</style><p>x</p>`,
		`<a href="javascript:alert(1)">x</a>`,
		`<a href="JaVaScRiPt:alert(1)">x</a>`,
		`<a href="java&#x09;script:alert(1)">x</a>`,
		`<a href="&#106;avascript:alert(1)">x</a>`,
		`<a href="vbscript:msgbox(1)">x</a>`,
		`<a href=" javascript:alert(1)">x</a>`,
		`<a href="data:text/html,<script>alert(1)</script>">x</a>`,
		`<img src="data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==">`,
		`<iframe src="https://evil.example"></iframe>`,
		`<iframe srcdoc="<script>alert(1)</script>"></iframe>`,
		`<object data="x.swf"></object><embed src="x.swf">`,
		`<form action="https://evil.example"><input name="password"><button formaction="javascript:alert(1)">Go</button></form>`,
		`<textarea>x</textarea><select><option>1</option></select>`,
		`<svg onload=alert(1)><circle r="1"/></svg>`,
		`<math><mi xlink:href="javascript:alert(1)">x</mi></math>`,
		`<base href="https://evil.example/"><meta http-equiv="refresh" content="0;url=https://evil.example"><link rel="stylesheet" href="https://evil.example/x.css">`,
		`<a href="https://example.com" data-video-src="https://evil.example/player">x</a>`,
		`<div data-form="contact.form"></div>`,
		`<div data-assistant-open>x</div><button data-theme-toggle>t</button>`,
		`<template><script>alert(1)</script></template><noscript><img src=x onerror=alert(1)></noscript>`,
		`<p title="x" onmouseover="alert(1)" style="color:red">x</p>`,
		`<img src=x:alert(1) onerror=eval(src)>`,
		`<<script>alert(1);//<</script>`,
		`<div id="x" class="a" STYLE="x:expression(alert(1))">x</div>`,
		`<!--<script>alert(1)</script>-->`,
		`<p>unclosed <a href="/x" onclick="alert(1)">`,
	} {
		out := strings.ToLower(SanitizeHTMLBlock(in))
		for _, f := range forbidden {
			if strings.Contains(out, f) {
				t.Errorf("SanitizeHTMLBlock(%q) = %q: contains %q", in, out, f)
			}
		}
	}
}

// TestSanitizeHTMLBlockIdempotent: what is stored (sanitised on save) is
// rendered unchanged (sanitised again).
func TestSanitizeHTMLBlockIdempotent(t *testing.T) {
	for _, in := range []string{
		`<a href="https://example.com" target="_blank" rel="nofollow">x</a>`,
		`<p class="a b" data-x="1 & 2">Tom &amp; Jerry &lt;3</p>`,
		`<div><p>unclosed`,
	} {
		once := SanitizeHTMLBlock(in)
		if twice := SanitizeHTMLBlock(once); twice != once {
			t.Errorf("not idempotent: %q → %q → %q", in, once, twice)
		}
	}
}

func TestReservedDataAttr(t *testing.T) {
	for name, want := range map[string]bool{
		"data-step": false, "data-variant": false, "data-size": false, "data-video-src": true, "data-video": false,
		"data-form": true, "data-form-x": true, "data-formula": false, "data-theme": true, "data-theme-toggle": true,
		"data-icon": true, "data-Upper": true, "data-": true,
	} {
		if got := reservedDataAttr(name); got != want {
			t.Errorf("reservedDataAttr(%q) = %v, want %v", name, got, want)
		}
	}
}
