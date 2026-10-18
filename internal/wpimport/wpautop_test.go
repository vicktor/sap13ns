package wpimport

import (
	"strings"
	"testing"
)

// Cases from WordPress's tests/phpunit/tests/formatting/wpAutop.php, so the
// port can be checked against the reference behaviour.
func TestWpautopMatchesWordPress(t *testing.T) {
	tests := []struct {
		name, in, want string
		trim           bool
	}{
		{name: "paragraphs and line breaks", in: "a\nb\n\nc", want: "<p>a<br />\nb</p>\n<p>c</p>\n"},
		{name: "empty", in: " \n\t ", want: ""},
		{name: "newline inside a tag", in: "Hello <a\nhref='world'>", want: "<p>Hello <a\nhref='world'></p>\n"},
		{name: "newline inside a comment", in: "Hello <!-- a\nhref='world' -->", want: "<p>Hello <!-- a\nhref='world' --></p>\n"},
		{
			name: "object inside a comment",
			in:   "Hello <!-- <object>\n<param>\n<param>\n<embed>\n</embed>\n</object>\n -->",
			want: "<p>Hello <!-- <object>\n<param>\n<param>\n<embed>\n</embed>\n</object>\n --></p>\n",
		},
		{
			name: "pre is not touched", trim: true,
			in:   "Look at this code\n\n<pre>line 1\n\nline 2\n  indented</pre>\n\nIsn't that cool?",
			want: "<p>Look at this code</p>\n<pre>line 1\n\nline 2\n  indented</pre>\n<p>Isn't that cool?</p>",
		},
		{
			name: "input elements stay inline", trim: true,
			in:   `Username: <input type="text" id="username" name="username" /><br />Password: <input type="password" id="password1" name="password1" />`,
			want: `<p>Username: <input type="text" id="username" name="username" /><br />Password: <input type="password" id="password1" name="password1" /></p>`,
		},
		{
			name: "select and options", trim: true,
			in:   "Country: <select id=\"state\" name=\"state\">\n<option value=\"1\">Alabama</option>\n<option value=\"2\">Alaska</option>\n</select>",
			want: "<p>Country: <select id=\"state\" name=\"state\"><option value=\"1\">Alabama</option><option value=\"2\">Alaska</option></select></p>",
		},
		{name: "blockquote", trim: true, in: "<blockquote>foo</blockquote>", want: "<blockquote><p>foo</p></blockquote>"},
		{name: "inline element", trim: true, in: "<strong>foo</strong>", want: "<p><strong>foo</strong></p>"},
		{
			name: "skips line breaks after br", trim: true,
			in:   "\nline 1<br>\nline 2<br/>\nline 3<br />\nline 4\nline 5\n",
			want: "<p>line 1<br />\nline 2<br />\nline 3<br />\nline 4<br />\nline 5</p>",
		},
		{
			name: "paragraph after multiple br", trim: true,
			in:   "\nline 1<br>\n<br/>\nline 2<br/>\n<br />\n",
			want: "<p>line 1</p>\n<p>line 2</p>",
		},
		{name: "hr is not peed", trim: true, in: "paragraph1<hr>paragraph2", want: "<p>paragraph1</p>\n<hr>\n<p>paragraph2</p>"},
		{
			name: "inline svg is kept", trim: true,
			in:   "foo <svg>\n<g><circle></circle></g></svg> bar",
			want: "<p>foo <svg>\n<g><circle></circle></g></svg> bar</p>",
		},
		{
			name: "block elements", trim: true,
			in:   "Line one\nLine two\n\n<div>block</div>\nafter",
			want: "<p>Line one<br />\nLine two</p>\n<div>block</div>\n<p>after</p>",
		},
		{
			name: "figcaption", trim: true,
			in:   "<figure><img src=\"a.jpg\">\n<figcaption>Caption</figcaption>\n</figure>",
			want: "<figure><img src=\"a.jpg\"><figcaption>Caption</figcaption></figure>",
		},
		{
			name: "lists", trim: true,
			in:   "Intro\n<ul>\n<li>one</li>\n<li>two</li>\n</ul>",
			want: "<p>Intro</p>\n<ul>\n<li>one</li>\n<li>two</li>\n</ul>",
		},
		{
			name: "source and track", trim: true,
			in:   "<video width=\"300\">\n<source src=\"a.mp4\" type=\"video/mp4\">\n<track src=\"a.vtt\">\n</video>",
			want: "<p><video width=\"300\"><source src=\"a.mp4\" type=\"video/mp4\"><track src=\"a.vtt\"></video></p>",
		},
		{name: "crlf", trim: true, in: "a\r\n\r\nb", want: "<p>a</p>\n<p>b</p>"},
	}
	for _, tt := range tests {
		got := wpautop(tt.in, true)
		if tt.trim {
			got = strings.TrimSpace(got)
		}
		if got != tt.want {
			t.Errorf("%s:\n got %q\nwant %q", tt.name, got, tt.want)
		}
	}
}

func TestWpautopWithoutBr(t *testing.T) {
	if got := strings.TrimSpace(wpautop("a\nb\n\nc", false)); got != "<p>a\nb</p>\n<p>c</p>" {
		t.Errorf("got %q", got)
	}
}

func TestBrNewlines(t *testing.T) {
	for in, want := range map[string]string{
		"a\nb":         "a<br />\nb",
		"a  \n\tb":     "a<br />\n\tb",
		"a<br />\nb":   "a<br />\nb",
		"a<br />  \nb": "a<br /> <br />\nb", // PCRE quirk, reproduced on purpose
		"a b":          "a b",
	} {
		if got := brNewlines(in); got != want {
			t.Errorf("brNewlines(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHTMLSplit(t *testing.T) {
	got := htmlSplit("a<b>c<!-- x > y -->d<![CDATA[ <z> ]]>e<f")
	want := []string{"a", "<b>", "c", "<!-- x > y -->", "d", "<![CDATA[ <z> ]]>", "e", "<f", ""}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("htmlSplit = %q, want %q", got, want)
	}
}
