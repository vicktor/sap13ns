package export

import (
	"strings"
	"testing"

	"sap13ns/internal/wpimport"
)

func TestWPImageClasses(t *testing.T) {
	cases := map[string]string{
		`<img src="/a.png" alt="x" class="align-left" width="300"/>`:  `<img src="/a.png" alt="x" class="alignleft size-medium" width="300"/>`,
		`<img src="/a.png" alt="x" class="align-right" width="120"/>`: `<img src="/a.png" alt="x" class="alignright size-thumbnail" width="120"/>`,
		`<img src="/a.png" alt="x" class="align-center"/>`:            `<img src="/a.png" alt="x" class="aligncenter size-full"/>`,
		`<img src="/a.png" alt="x" class="align-full"/>`:              `<img src="/a.png" alt="x" class="alignfull size-full"/>`,
		`<img src="/a.png" alt="x" width="800"/>`:                     `<img class="size-large" src="/a.png" alt="x" width="800"/>`,
		`<img src="/a.png" alt="x" width="2000"/>`:                    `<img class="size-full" src="/a.png" alt="x" width="2000"/>`,
		`<img src="/a.png" alt="x"/>`:                                 `<img src="/a.png" alt="x"/>`,
	}
	for in, want := range cases {
		if got := wpImageClasses(in); got != want {
			t.Errorf("wpImageClasses(%s)\n got %s\nwant %s", in, got, want)
		}
	}
}

// Sizes, alignments and videos survive a trip to WordPress and back.
func TestLayoutRoundTrip(t *testing.T) {
	e := &exporter{o: Options{Site: Site{URL: "https://site.test"}}}
	md := "Intro.\n\n![Left](https://cdn.test/a.png){width=300 align=left} Text that wraps.\n\n" +
		"![Centred](https://cdn.test/b.png){width=640 align=center}\n\n![Full](https://cdn.test/c.png){align=full}\n\n" +
		"https://youtu.be/dQw4w9WgXcQ\n\n[video url=\"https://vimeo.com/22439234\" start=\"90\"]\n"
	h := e.renderHTML(md)
	for _, s := range []string{
		`class="alignleft size-medium" width="300"`, `class="aligncenter size-large" width="640"`, `class="alignfull size-full"`,
		"<p>https://www.youtube.com/watch?v=dQw4w9WgXcQ</p>", "<p>https://vimeo.com/22439234#t=90s</p>",
	} {
		if !strings.Contains(h, s) {
			t.Errorf("export: missing %s in\n%s", s, h)
		}
	}
	if strings.Contains(h, "sap-video") || strings.Contains(h, "align-left") {
		t.Errorf("export keeps sap13ns markup:\n%s", h)
	}
	back, _ := wpimport.ToMarkdown(h, wpimport.ConvertOptions{})
	for _, s := range []string{
		"![Left](https://cdn.test/a.png){width=300 align=left} Text that wraps.",
		"![Centred](https://cdn.test/b.png){width=640 align=center}",
		"![Full](https://cdn.test/c.png){align=full}",
		"<https://www.youtube.com/watch?v=dQw4w9WgXcQ>",
		"<https://vimeo.com/22439234#t=90s>",
	} {
		if !strings.Contains(back, s) {
			t.Errorf("import: missing %s in\n%s", s, back)
		}
	}
}
