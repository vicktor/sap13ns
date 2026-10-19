package content

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

func TestParseVideoURL(t *testing.T) {
	good := []struct {
		in   string
		want Video
	}{
		{"https://www.youtube.com/watch?v=dQw4w9WgXcQ", Video{Provider: "youtube", ID: "dQw4w9WgXcQ"}},
		{"http://youtube.com/watch?v=dQw4w9WgXcQ&t=1m30s", Video{Provider: "youtube", ID: "dQw4w9WgXcQ", Start: 90}},
		{"https://m.youtube.com/watch?feature=share&v=dQw4w9WgXcQ", Video{Provider: "youtube", ID: "dQw4w9WgXcQ"}},
		{"https://music.youtube.com/watch?v=dQw4w9WgXcQ&list=PLx", Video{Provider: "youtube", ID: "dQw4w9WgXcQ"}},
		{"https://youtu.be/dQw4w9WgXcQ?si=abc&t=90", Video{Provider: "youtube", ID: "dQw4w9WgXcQ", Start: 90}},
		{"https://www.youtube.com/shorts/aqz-KE-bpKQ", Video{Provider: "youtube", ID: "aqz-KE-bpKQ", Portrait: true}},
		{"https://www.youtube.com/embed/dQw4w9WgXcQ?start=42", Video{Provider: "youtube", ID: "dQw4w9WgXcQ", Start: 42}},
		{"https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ", Video{Provider: "youtube", ID: "dQw4w9WgXcQ"}},
		{"https://www.youtube.com/live/dQw4w9WgXcQ/", Video{Provider: "youtube", ID: "dQw4w9WgXcQ"}},
		{"https://www.youtube.com/playlist?list=PLFgquLnL59alCl_2TQvOiD5Vgm1hCaGSI", Video{Provider: "youtube", List: "PLFgquLnL59alCl_2TQvOiD5Vgm1hCaGSI"}},
		{"https://www.youtube.com/embed/videoseries?list=PLFgquLnL59alCl_2TQvOiD5Vgm1hCaGSI", Video{Provider: "youtube", List: "PLFgquLnL59alCl_2TQvOiD5Vgm1hCaGSI"}},
		{"https://WWW.YouTube.com/watch?v=dQw4w9WgXcQ#t=2h", Video{Provider: "youtube", ID: "dQw4w9WgXcQ", Start: 7200}},
		{"https://vimeo.com/22439234", Video{Provider: "vimeo", ID: "22439234"}},
		{"https://vimeo.com/22439234/0a1b2c3d4e", Video{Provider: "vimeo", ID: "22439234", Hash: "0a1b2c3d4e"}},
		{"https://vimeo.com/22439234?h=0a1b2c3d4e#t=1m5s", Video{Provider: "vimeo", ID: "22439234", Hash: "0a1b2c3d4e", Start: 65}},
		{"https://vimeo.com/channels/staffpicks/22439234", Video{Provider: "vimeo", ID: "22439234"}},
		{"https://vimeo.com/groups/shortfilms/videos/22439234", Video{Provider: "vimeo", ID: "22439234"}},
		{"https://player.vimeo.com/video/22439234?h=0a1b2c3d4e", Video{Provider: "vimeo", ID: "22439234", Hash: "0a1b2c3d4e"}},
	}
	for _, c := range good {
		got, ok := ParseVideoURL(c.in)
		if !ok || got != c.want {
			t.Errorf("ParseVideoURL(%q) = %+v, %v; want %+v", c.in, got, ok, c.want)
		}
	}
	bad := []string{
		"",
		"youtu.be/dQw4w9WgXcQ",
		"ftp://youtu.be/dQw4w9WgXcQ",
		"javascript:alert(1)//youtube.com/watch?v=dQw4w9WgXcQ",
		"https://youtube.com.evil.test/watch?v=dQw4w9WgXcQ",
		"https://evil.test/youtube.com/watch?v=dQw4w9WgXcQ",
		"https://notyoutube.com/watch?v=dQw4w9WgXcQ",
		"https://youtube.com./watch?v=dQw4w9WgXcQ",
		"https://user:pass@youtube.com/watch?v=dQw4w9WgXcQ",
		"https://youtube.com:8443/watch?v=dQw4w9WgXcQ",
		"https://www.youtube.com/watch?v=dQw4w9WgXc",   // 10 characters
		"https://www.youtube.com/watch?v=dQw4w9WgXcQQ", // 12
		"https://www.youtube.com/watch?v=dQw4w9WgX%22Q",
		"https://www.youtube.com/watch?v=dQw4w9WgX\"Q",
		"https://www.youtube.com/watch/dQw4w9WgXcQ",
		"https://www.youtube.com/shorts/dQw4w9WgXcQ/extra",
		"https://www.youtube.com/@channel",
		"https://www.youtube.com/playlist?list=short",
		"https://www.youtube.com/playlist?list=PL<script>x",
		"https://youtu.be/dQw4w9WgXcQ/x",
		"https://vimeo.com/0123",
		"https://vimeo.com/1234567890123",
		"https://vimeo.com/22439234/NOTHEX",
		"https://vimeo.com/22439234?h=zz",
		"https://vimeo.com/channels/staff picks/22439234",
		"https://vimeo.com/about",
		"https://player.vimeo.com/22439234",
		"https://vimeo.com.evil.test/22439234",
		strings.Repeat("https://youtu.be/dQw4w9WgXcQ", 100),
	}
	for _, in := range bad {
		if v, ok := ParseVideoURL(in); ok {
			t.Errorf("ParseVideoURL(%q) = %+v, want no video", in, v)
		}
	}
}

func TestParseStart(t *testing.T) {
	cases := map[string]int{"90": 90, "90s": 90, "1m30s": 90, "1h2m3s": 3723, "1:30": 90, "1:02:03": 3723, "2h": 7200,
		"": 0, "0": 0, "-5": 0, "1:75": 0, "abc": 0, "100:00:00": 0, "1m30x": 0}
	for in, want := range cases {
		if got := ParseStart(in); got != want {
			t.Errorf("ParseStart(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestVideoURLs(t *testing.T) {
	cases := []struct {
		v             Video
		watch, player string
	}{
		{Video{Provider: "youtube", ID: "dQw4w9WgXcQ", Start: 90}, "https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=90s", "https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ?autoplay=1&start=90"},
		{Video{Provider: "youtube", ID: "aqz-KE-bpKQ", Portrait: true}, "https://www.youtube.com/shorts/aqz-KE-bpKQ", "https://www.youtube-nocookie.com/embed/aqz-KE-bpKQ?autoplay=1"},
		{Video{Provider: "youtube", List: "PLFgquLnL59alCl_2TQvOiD5Vgm1hCaGSI"}, "https://www.youtube.com/playlist?list=PLFgquLnL59alCl_2TQvOiD5Vgm1hCaGSI", "https://www.youtube-nocookie.com/embed/videoseries?list=PLFgquLnL59alCl_2TQvOiD5Vgm1hCaGSI&autoplay=1"},
		{Video{Provider: "vimeo", ID: "22439234", Hash: "0a1b2c3d4e", Start: 65}, "https://vimeo.com/22439234/0a1b2c3d4e#t=65s", "https://player.vimeo.com/video/22439234?autoplay=1&dnt=1&h=0a1b2c3d4e#t=65s"},
	}
	for _, c := range cases {
		if got := c.v.WatchURL(); got != c.watch {
			t.Errorf("WatchURL(%+v) = %q, want %q", c.v, got, c.watch)
		}
		if got := c.v.PlayerURL(); got != c.player || !PlayerURLPattern.MatchString(got) {
			t.Errorf("PlayerURL(%+v) = %q (pattern %v), want %q", c.v, got, PlayerURLPattern.MatchString(got), c.player)
		}
	}
	for _, s := range []string{
		"https://www.youtube.com/embed/dQw4w9WgXcQ?autoplay=1",
		"https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ?autoplay=1&mute=0",
		"http://player.vimeo.com/video/22439234?autoplay=1&dnt=1",
		"https://player.vimeo.com/video/22439234?autoplay=1",
		"javascript:alert(1)",
	} {
		if PlayerURLPattern.MatchString(s) {
			t.Errorf("PlayerURLPattern matches %q", s)
		}
	}
}

func testVideoOptions() *VideoOptions {
	return &VideoOptions{
		Meta: func(v Video) VideoMeta {
			if v.Provider == "vimeo" {
				return VideoMeta{Title: "The Mountain", Poster: "/video-thumbs/vm-22439234.jpg", Width: 1280, Height: 720}
			}
			return VideoMeta{}
		},
		Tr: func(key string, args ...any) string {
			es := map[string]string{"Watch on %s": "Ver en %s", "Play video: %s": "Reproducir vídeo: %s", "%s video": "Vídeo de %s", "%s video %d": "Vídeo de %s %d", "Play %s": "Reproducir %s"}
			return fmt.Sprintf(es[key], args...)
		},
	}
}

func TestRenderVideoPlayer(t *testing.T) {
	var found []string
	o := testVideoOptions()
	o.Found = func(v Video) { found = append(found, v.Key()) }
	got := string(RenderWith("<https://vimeo.com/22439234>\n", Options{Video: o}))
	want := `<figure class="sap-video sap-video-vimeo" data-video-provider="vimeo"><a class="sap-video-link" href="https://vimeo.com/22439234" data-video-src="https://player.vimeo.com/video/22439234?autoplay=1&amp;dnt=1" data-video-title="The Mountain (Vimeo)" data-video-label="Reproducir vídeo: The Mountain (Vimeo)"><img class="sap-video-poster" src="/video-thumbs/vm-22439234.jpg" alt="" width="1280" height="720" loading="lazy" decoding="async"><span class="sap-video-play" aria-hidden="true"></span><span class="sap-video-caption"><span class="sap-video-title">The Mountain</span><span class="sap-video-source">Ver en Vimeo</span></span></a></figure>` + "\n"
	if got != want {
		t.Fatalf("player:\n got %s\nwant %s", got, want)
	}
	if !HasVideo(got) {
		t.Error("HasVideo = false")
	}

	src := "https://youtu.be/dQw4w9WgXcQ\n\nhttps://www.youtube.com/shorts/aqz-KE-bpKQ\n\n[video url=\"https://youtu.be/dQw4w9WgXcQ\" start=\"1:30\" title=\"Our <keynote>\"]\n"
	got = string(RenderWith(src, Options{Video: o}))
	for _, s := range []string{
		`data-video-label="Reproducir Vídeo de YouTube"`, // untitled: numbered from the second one
		`data-video-label="Reproducir Vídeo de YouTube 2"`,
		`class="sap-video sap-video-youtube sap-video-portrait"`,
		`data-video-src="https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ?autoplay=1&amp;start=90"`,
		`<span class="sap-video-title">Our &lt;keynote&gt;</span>`,
		`data-video-title="Our &lt;keynote&gt; (YouTube)"`,
	} {
		if !strings.Contains(got, s) {
			t.Errorf("missing %s in\n%s", s, got)
		}
	}
	if strings.Count(got, "<figure") != 3 || len(found) != 4 {
		t.Errorf("figures %d, found %v", strings.Count(got, "<figure"), found)
	}
	// Nothing is loaded from the provider before the click.
	if strings.Contains(got, "<iframe") || strings.Contains(got, ` src="https://`) {
		t.Errorf("third-party URL in a src: %s", got)
	}
}

func TestRenderVideoStaysLink(t *testing.T) {
	o := testVideoOptions()
	for _, src := range []string{
		"Watch https://youtu.be/dQw4w9WgXcQ now\n",
		"- https://youtu.be/dQw4w9WgXcQ\n",
		"> https://youtu.be/dQw4w9WgXcQ\n",
		"[Our keynote](https://youtu.be/dQw4w9WgXcQ)\n",
		"https://youtu.be/dQw4w9WgXcQ\nhttps://vimeo.com/22439234\n",
		"```\nhttps://youtu.be/dQw4w9WgXcQ\n[video url=\"https://youtu.be/dQw4w9WgXcQ\"]\n```\n",
		"https://youtube.com.evil.test/watch?v=dQw4w9WgXcQ\n",
		"[video url=\"https://evil.test/x\"]\n",
		"\\[video url=\"https://youtu.be/dQw4w9WgXcQ\"]\n",
	} {
		if got := string(RenderWith(src, Options{Video: o})); HasVideo(got) {
			t.Errorf("%q became a player:\n%s", src, got)
		}
	}
	// Without options (embeds off, excerpts) a URL stays a link and the
	// shortcode becomes one.
	got := string(RenderWith("https://youtu.be/dQw4w9WgXcQ\n\n[video url=\"https://vimeo.com/22439234\" title=\"Talk\"]\n", Options{}))
	want := "<p><a href=\"https://youtu.be/dQw4w9WgXcQ\">https://youtu.be/dQw4w9WgXcQ</a></p>\n<p><a href=\"https://vimeo.com/22439234\">Talk</a></p>\n"
	if got != want {
		t.Errorf("links:\n got %q\nwant %q", got, want)
	}
}

func TestRenderVideoShortcodeOwnedByCore(t *testing.T) {
	called := false
	o := Options{
		IsShortcode: func(name string) bool { return name == "video" || name == "note" },
		Shortcode: func(sc Shortcode) (string, error) {
			called = called || sc.Name == "video"
			return "<p>plugin</p>", nil
		},
		Video: testVideoOptions(),
	}
	got := string(RenderWith("[video url=\"https://youtu.be/dQw4w9WgXcQ\"]\n\n[note]\n", o))
	if called || !HasVideo(got) || !strings.Contains(got, "plugin") {
		t.Errorf("plugin called for [video]: %v\n%s", called, got)
	}
	o.Video = nil
	got = string(RenderWith("[video url=\"https://youtu.be/dQw4w9WgXcQ\"]\n", o))
	if !called || HasVideo(got) {
		t.Errorf("with embeds off the plugin's [video] should run: %v\n%s", called, got)
	}
}

func TestRenderVideoFeedAndWordPress(t *testing.T) {
	src := "https://vimeo.com/22439234\n\n[video url=\"https://youtu.be/dQw4w9WgXcQ\" start=\"90\"]\n"
	o := testVideoOptions()
	o.Mode = VideoFeed
	got := string(RenderWith(src, Options{Video: o}))
	want := `<figure data-video-provider="vimeo"><a href="https://vimeo.com/22439234"><img src="/video-thumbs/vm-22439234.jpg" alt="Ver en Vimeo: The Mountain" width="1280" height="720"></a><figcaption><a href="https://vimeo.com/22439234">The Mountain (Vimeo)</a></figcaption></figure>` + "\n" +
		`<figure data-video-provider="youtube"><a href="https://www.youtube.com/watch?v=dQw4w9WgXcQ&amp;t=90s">Vídeo de YouTube</a></figure>` + "\n"
	if got != want {
		t.Errorf("feed:\n got %s\nwant %s", got, want)
	}
	if HasVideo(got) {
		t.Error("a feed has no player")
	}
	// Content filters do not see the feed's links either.
	if p, restore := ProtectVideos(got); strings.Contains(p, "vimeo.com") || restore(p) != got {
		t.Errorf("feed not protected: %s", p)
	}
	got = string(RenderWith(src, Options{Video: &VideoOptions{Mode: VideoWordPress}}))
	want = "<p>https://vimeo.com/22439234</p>\n<p>https://www.youtube.com/watch?v=dQw4w9WgXcQ&amp;t=90s</p>\n"
	if got != want {
		t.Errorf("wordpress:\n got %q\nwant %q", got, want)
	}
}

func TestContentPolicyVideoMarkup(t *testing.T) {
	p := ContentPolicy()
	cases := map[string]string{
		// Author iframes never survive.
		`<iframe src="https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ"></iframe>`: ``,
		// Only player URLs become data-video-src.
		`<a class="sap-video-link" href="https://x.test/" data-video-src="https://evil.test/">x</a>`:                                                `<a class="sap-video-link" href="https://x.test/">x</a>`,
		`<a class="sap-video-link" href="/x" data-video-src="javascript:alert(1)">x</a>`:                                                            `<a class="sap-video-link" href="/x">x</a>`,
		`<a class="sap-video-link" href="/x" data-video-src="https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ?autoplay=1">x</a>`:                  `<a class="sap-video-link" href="/x" data-video-src="https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ?autoplay=1">x</a>`,
		`<figure class="sap-video sap-video-tiktok" data-video-provider="tiktok"><span class="sap-video-play evil" onclick="x()">a</span></figure>`: `<figure><span>a</span></figure>`,
		`<a class="evil sap-video-link" href="/x" style="color:red">x</a>`:                                                                          `<a href="/x">x</a>`,
		`<div data-video-src="https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ?autoplay=1">x</div>`:                                               `<div>x</div>`,
	}
	for in, want := range cases {
		if got := p.Sanitize(in); got != want {
			t.Errorf("Sanitize(%s)\n got %s\nwant %s", in, got, want)
		}
	}
}

var videoToken = regexp.MustCompile(`sapvideo[0-9a-f]{32}x[0-9]+`)

func TestProtectRestoreVideos(t *testing.T) {
	html := string(RenderWith("Intro\n\nhttps://youtu.be/dQw4w9WgXcQ\n\nhttps://vimeo.com/22439234\n", Options{Video: testVideoOptions()}))
	protected, restore := ProtectVideos(html)
	if HasVideo(protected) || strings.Contains(protected, "youtu") || strings.Count(protected, "sapvideo") != 2 {
		t.Fatalf("protected: %s", protected)
	}
	tokens := videoToken.FindAllString(protected, -1)
	if len(tokens) != 2 {
		t.Fatalf("tokens: %q", tokens)
	}
	if again, _ := ProtectVideos(html); again == protected {
		t.Error("the nonce is the same on every call")
	}
	players := videoFigure.FindAllString(html, -1)
	if got := restore(protected); got != html {
		t.Errorf("unfiltered:\n got %s\nwant %s", got, html)
	}
	// A filter that wraps text in paragraphs, or the post in a section,
	// does not break the players.
	filtered := strings.Replace(protected, tokens[1], "<p>"+tokens[1]+"</p>", 1)
	if got := restore(filtered); got != html {
		t.Errorf("paragraph:\n got %s\nwant %s", got, html)
	}
	if got := restore("<section>" + protected + "</section>"); got != "<section>"+html+"</section>" {
		t.Errorf("section:\n got %s", got)
	}

	cases := []struct {
		name, filtered, want string
	}{
		// A token copied into an attribute: the attribute goes, the player
		// stays where the token was.
		{"attribute", `<a title="` + tokens[0] + `" href="/x">x</a>` + "\n" + tokens[0] + "\n",
			`<a href="/x">x</a>` + "\n" + players[0] + "\n"},
		{"attribute only", `<p><img src="/a.png" alt="` + tokens[0] + `"></p>`, `<p><img src="/a.png"></p>`},
		{"attribute part", `<a href="https://x.test/` + tokens[0] + `?a=1&amp;b=2" rel="nofollow">x</a>`, `<a rel="nofollow">x</a>`},
		// A duplicated token: one player.
		{"duplicate", tokens[0] + "\n" + tokens[0] + "\n<p>" + tokens[0] + "</p>", players[0] + "\n\n"},
		{"two in a text", tokens[1] + "\n" + tokens[0] + "\n", players[1] + "\n" + players[0] + "\n"},
		// A token a filter wrapped in other markup is dropped.
		{"link", `<a href="/x">` + tokens[0] + `</a>`, `<a href="/x"></a>`},
		{"text", `<p>Watch ` + tokens[0] + ` now</p>`, `<p>Watch  now</p>`},
		{"top-level text", "Watch " + tokens[0] + "\n", "Watch \n"},
		{"code", `<pre><code>` + tokens[0] + `</code></pre>`, `<pre><code></code></pre>`},
		{"strong in paragraph", `<p><strong>` + tokens[0] + `</strong></p>`, `<p><strong></strong></p>`},
		{"heading", `<h2>` + tokens[0] + `</h2>`, `<h2></h2>`},
		{"list", `<ul><li>` + tokens[0] + `</li></ul>`, `<ul><li>` + players[0] + `</li></ul>`},
		{"in a list", `<ul>` + tokens[0] + `</ul>`, `<ul></ul>`},
		{"comment", `<!-- ` + tokens[0] + ` -->ok`, `ok`},
		{"unknown number", strings.TrimSuffix(tokens[0], "0") + "7", ``},
		// Removed stays removed; a forged token is text.
		{"removed", `<p>Intro</p>`, `<p>Intro</p>`},
		{"forged", `<p>sapvideo0123456789abcdef0123456789abcdefx0</p>`, `<p>sapvideo0123456789abcdef0123456789abcdefx0</p>`},
	}
	for _, c := range cases {
		got := restore(c.filtered)
		if got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, c.want)
		}
		if videoToken.MatchString(got) && c.name != "forged" {
			t.Errorf("%s: a token is left: %s", c.name, got)
		}
		if n := strings.Count(got, `<figure class="sap-video `); n > 2 {
			t.Errorf("%s: %d players", c.name, n)
		}
	}

	if p, restore := ProtectVideos("<p>no video</p>"); p != "<p>no video</p>" || restore("x") != "x" {
		t.Error("ProtectVideos changed a post without videos")
	}
}
