package web

import "testing"

func TestStripHTMLComments(t *testing.T) {
	for in, want := range map[string]string{
		"a <!-- secret --> b":                        "a  b",
		"x\n<!--\nnote\n-->\ny":                      "x\n\ny",
		"```html\n<!-- kept -->\n```\n<!-- gone -->": "```html\n<!-- kept -->\n```\n",
		"no comments":                                "no comments",
	} {
		if got := stripHTMLComments(in); got != want {
			t.Errorf("stripHTMLComments(%q) = %q, want %q", in, got, want)
		}
	}
}
