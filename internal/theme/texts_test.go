package theme

import (
	"strings"
	"testing"
)

// The marks become spans in the page's text and go away elsewhere.
func TestMarkTexts(t *testing.T) {
	tx := &Texts{Over: map[string]string{"Hello": "Aloha", "Powered by": "Nope"}, Mark: true}
	m := func(k string) string { return tx.text(k, k) }
	page := `<html><head><title>` + m("Title") + `</title><script>var x = "` + m("JS") + `"</script></head>` +
		`<body><h1 class="t">` + m("Hello") + `!</h1><a title="` + m("Tip") + `" href="/">` + m("Tom &amp; Jerry") + `</a>` +
		`<p>` + m("Powered by") + ` sap13ns</p><textarea>` + m("Area") + `</textarea></body></html>`
	out := string(markTexts([]byte(page)))
	for _, w := range []string{
		`<title>Title</title>`, `var x = "JS"`, `<h1 class="t"><span data-sap-text="Hello">Aloha</span>!</h1>`,
		`<a title="Tip" href="/"><span data-sap-text="Tom &amp; Jerry">Tom &amp; Jerry</span></a>`,
		`<p>Powered by sap13ns</p>`, `<textarea>Area</textarea>`,
	} {
		if !strings.Contains(out, w) {
			t.Errorf("missing %s in\n%s", w, out)
		}
	}
	if strings.ContainsAny(out, markStart+markKey+markEnd) {
		t.Error("marks left")
	}
}
