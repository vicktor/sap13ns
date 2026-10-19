package content

import (
	"strings"
	"testing"
)

func TestImageAttrs(t *testing.T) {
	good := map[string]struct {
		w int
		a string
	}{
		"width=480":              {480, ""},
		"align=left":             {0, "left"},
		"width=480 align=right":  {480, "right"},
		"align=center width=1":   {1, "center"},
		"width=4096 align=full":  {4096, "full"},
		"width=320  align=left ": {320, "left"},
	}
	for in, want := range good {
		w, a, ok := ImageAttrs(in)
		if !ok || w != want.w || a != want.a {
			t.Errorf("ImageAttrs(%q) = %d, %q, %v", in, w, a, ok)
		}
	}
	for _, in := range []string{"", "width=0", "width=4097", "width=048", "width=50%", "width=-1", "align=wide", "align=LEFT",
		"style=color", "width=480 width=500", "height=200", "class=x", "width=480 onload=x"} {
		if w, a, ok := ImageAttrs(in); ok {
			t.Errorf("ImageAttrs(%q) = %d, %q, ok", in, w, a)
		}
	}
}

func TestRenderImageLayout(t *testing.T) {
	media := func(src string) (ImageInfo, bool) {
		if !strings.HasPrefix(src, "/uploads/2026/10/abc") {
			return ImageInfo{}, false
		}
		return ImageInfo{Width: 2000, Height: 1000, Sources: []ImageSource{
			{"/uploads/2026/10/abc-thumb.jpg", 480}, {"/uploads/2026/10/abc-medium.jpg", 1024}, {"/uploads/2026/10/abc.jpg", 2000},
		}}, true
	}
	cases := []struct{ src, want string }{
		{"![A photo](/uploads/2026/10/abc-medium.jpg){width=600 align=left} The text wraps.",
			`<p><img src="/uploads/2026/10/abc-medium.jpg" alt="A photo" loading="lazy" decoding="async" class="align-left" width="600" height="300" srcset="/uploads/2026/10/abc-thumb.jpg 480w, /uploads/2026/10/abc-medium.jpg 1024w, /uploads/2026/10/abc.jpg 2000w" sizes="(max-width: 640px) 100vw, 600px"/> The text wraps.</p>`},
		{"![A photo](/uploads/2026/10/abc-medium.jpg){width=300}",
			`<p><img src="/uploads/2026/10/abc-thumb.jpg" alt="A photo" loading="lazy" decoding="async" width="300" height="150" srcset="/uploads/2026/10/abc-thumb.jpg 480w, /uploads/2026/10/abc-medium.jpg 1024w, /uploads/2026/10/abc.jpg 2000w" sizes="(max-width: 640px) 100vw, 300px"/></p>`},
		// Not in the library: the width only, the height follows from CSS.
		{"![x](https://cdn.test/a.png){width=480 align=center}",
			`<p><img src="https://cdn.test/a.png" alt="x" loading="lazy" decoding="async" class="align-center" width="480"/></p>`},
		{"![x](/a.png \"Title: here?\"){align=right}text",
			`<p><img src="/a.png" alt="x" title="Title: here?" loading="lazy" decoding="async" class="align-right"/>text</p>`},
		// Anything unknown leaves the braces as text.
		{"![x](/a.png){width=480 align=wide}", `<p><img src="/a.png" alt="x" loading="lazy" decoding="async"/>{width=480 align=wide}</p>`},
		{"![x](/a.png){style=color:red}", `<p><img src="/a.png" alt="x" loading="lazy" decoding="async"/>{style=color:red}</p>`},
		{"![x](/a.png) {width=480}", `<p><img src="/a.png" alt="x" loading="lazy" decoding="async"/> {width=480}</p>`},
		{"![x](/a.png){width=*480*}", `<p><img src="/a.png" alt="x" loading="lazy" decoding="async"/>{width=<em>480</em>}</p>`},
		// Alt text keeps colons and question marks.
		{"![Q: why?](/a.png){width=10}", `<p><img src="/a.png" alt="Q: why?" loading="lazy" decoding="async" width="10"/></p>`},
		// A line break after the attributes stays.
		{"![x](/a.png){align=left}\nNext line", "<p><img src=\"/a.png\" alt=\"x\" loading=\"lazy\" decoding=\"async\" class=\"align-left\"/>\nNext line</p>"},
		{"`![x](/a.png){width=480}`", `<p><code>![x](/a.png){width=480}</code></p>`},
	}
	for _, c := range cases {
		got := strings.TrimSpace(string(RenderWith(c.src, Options{Images: media})))
		if got != c.want {
			t.Errorf("%s\n got %s\nwant %s", c.src, got, c.want)
		}
	}
}

func TestContentPolicyImages(t *testing.T) {
	p := ContentPolicy()
	cases := map[string]string{
		`<img src="/a.png" width="480" height="240" class="align-left">`:        `<img src="/a.png" width="480" height="240" class="align-left">`,
		`<img src="/a.png" width="50%" height="1e3">`:                           `<img src="/a.png">`,
		`<img src="/a.png" width="99999">`:                                      `<img src="/a.png">`,
		`<img src="/a.png" style="float:left" class="align-left evil">`:         `<img src="/a.png">`,
		`<img src="/a.png" class="align-wide">`:                                 `<img src="/a.png">`,
		`<img src="/a.png" srcset="https://evil.test/a.png 1w">`:                `<img src="/a.png">`,
		`<img src="/a.png" srcset="/uploads/a.png 480w, /uploads/b.png 1024w">`: `<img src="/a.png" srcset="/uploads/a.png 480w, /uploads/b.png 1024w">`,
		`<img src="/a.png" sizes="100vw">`:                                      `<img src="/a.png">`,
		`<img src="/a.png" onerror="alert(1)" align="left">`:                    `<img src="/a.png">`,
		`<p class="align-left" width="10">x</p>`:                                `<p>x</p>`,
		`<img src="javascript:alert(1)" alt="x">`:                               `<img alt="x">`,
		`<img src="/a.png" alt="Fish &amp; chips: why?">`:                       `<img src="/a.png" alt="Fish &amp; chips: why?">`,
		`<img src="/a.png" sizes="(max-width: 640px) 100vw, 480px">`:            `<img src="/a.png" sizes="(max-width: 640px) 100vw, 480px">`,
	}
	for in, want := range cases {
		if got := p.Sanitize(in); got != want {
			t.Errorf("Sanitize(%s)\n got %s\nwant %s", in, got, want)
		}
	}
	// Plugin filters return the whole post: the layout survives them too.
	in := `<img src="/a.png" width="480" height="240" class="align-left" srcset="/uploads/a.png 480w, /uploads/b.png 1024w" sizes="(max-width: 640px) 100vw, 480px">`
	if got := PluginPolicy().Sanitize(in); got != in {
		t.Errorf("PluginPolicy:\n got %s\nwant %s", got, in)
	}
}
