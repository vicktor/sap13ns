package content

import (
	"strings"
	"testing"
)

func TestRenderLinks(t *testing.T) {
	out := string(RenderPlain("[home](/blog/hola) and [out](https://example.org/x) and [bad](javascript:alert(1))\n\n<script>alert(1)</script>"))
	for _, want := range []string{`<a href="/blog/hola">home</a>`, `<a href="https://example.org/x">out</a>`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in %s", want, out)
		}
	}
	for _, bad := range []string{"nofollow", "javascript:", "<script"} {
		if strings.Contains(out, bad) {
			t.Errorf("unexpected %q in %s", bad, out)
		}
	}
}
