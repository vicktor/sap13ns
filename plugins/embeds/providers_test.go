package main

import (
	"encoding/json"
	"net/url"
	"os"
	"slices"
	"testing"
)

var all = map[string]bool{"youtube": true, "vimeo": true, "tiktok": true}

func TestMatch(t *testing.T) {
	for _, tc := range []struct {
		href     string
		provider string // "" means no match
		player   string
		portrait bool
	}{
		{"https://www.youtube.com/watch?v=dQw4w9WgXcQ", "youtube", "https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ?autoplay=1", false},
		{"http://youtube.com/watch?feature=share&v=dQw4w9WgXcQ", "youtube", "https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ?autoplay=1", false},
		{"https://m.youtube.com/watch?v=dQw4w9WgXcQ&t=90", "youtube", "https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ?autoplay=1&start=90", false},
		{"https://music.youtube.com/watch?v=dQw4w9WgXcQ", "youtube", "https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ?autoplay=1", false},
		{"https://youtu.be/dQw4w9WgXcQ?t=1m30s", "youtube", "https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ?autoplay=1&start=90", false},
		{"https://youtu.be/dQw4w9WgXcQ?si=abc&t=1h2m3s", "youtube", "https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ?autoplay=1&start=3723", false},
		{"https://www.youtube.com/shorts/aBcDeFgHiJk", "youtube", "https://www.youtube-nocookie.com/embed/aBcDeFgHiJk?autoplay=1", true},
		{"https://www.youtube.com/embed/dQw4w9WgXcQ?start=42", "youtube", "https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ?autoplay=1&start=42", false},
		{"https://www.youtube.com/live/dQw4w9WgXcQ", "youtube", "https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ?autoplay=1", false},
		{"https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ", "youtube", "https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ?autoplay=1", false},
		{"https://WWW.YOUTUBE.COM./watch?v=dQw4w9WgXcQ&t=bogus", "youtube", "https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ?autoplay=1", false},
		{"https://vimeo.com/76979871", "vimeo", "https://player.vimeo.com/video/76979871?autoplay=1&dnt=1", false},
		{"https://www.vimeo.com/76979871#t=1m5s", "vimeo", "https://player.vimeo.com/video/76979871?autoplay=1&dnt=1#t=65s", false},
		{"https://vimeo.com/76979871/a1b2c3d4e5", "vimeo", "https://player.vimeo.com/video/76979871?autoplay=1&dnt=1&h=a1b2c3d4e5", false},
		{"https://vimeo.com/76979871?h=a1b2c3d4e5", "vimeo", "https://player.vimeo.com/video/76979871?autoplay=1&dnt=1&h=a1b2c3d4e5", false},
		{"https://vimeo.com/channels/staffpicks/76979871", "vimeo", "https://player.vimeo.com/video/76979871?autoplay=1&dnt=1", false},
		{"https://player.vimeo.com/video/76979871?h=a1b2c3", "vimeo", "https://player.vimeo.com/video/76979871?autoplay=1&dnt=1&h=a1b2c3", false},
		{"https://www.tiktok.com/@scout2015/video/6718335390845095173", "tiktok", ttPlayer, true},
		{"https://www.tiktok.com/@scout2015/video/6718335390845095173?is_from_webapp=1&sender_device=pc&lang=es", "tiktok", ttPlayer, true},
		{"http://tiktok.com/@scout_2015.x/video/6718335390845095173/", "tiktok", ttPlayer, true},
		{"https://m.tiktok.com/v/6718335390845095173.html", "tiktok", ttPlayer, true},
		{"https://www.tiktok.com/@scout2015/photo/6718335390845095173", "tiktok", ttPlayer, true},
		{"https://www.tiktok.com/embed/v2/6718335390845095173", "tiktok", ttPlayer, true},
		{"https://www.tiktok.com/embed/6718335390845095173", "tiktok", ttPlayer, true},
		{"https://www.tiktok.com/player/v1/6718335390845095173?music_info=1", "tiktok", ttPlayer, true},
		// Not videos, or not these providers.
		{"https://www.youtube.com/", "", "", false},
		{"https://www.youtube.com/watch?v=short", "", "", false},
		{"https://www.youtube.com/watch?v=dQw4w9WgXcQ%22onload", "", "", false},
		{"https://www.youtube.com/@channel", "", "", false},
		{"https://www.youtube.com/playlist?list=PL123", "", "", false},
		{"https://youtu.be/dQw4w9WgXcQ/extra", "", "", false},
		{"https://notyoutube.com/watch?v=dQw4w9WgXcQ", "", "", false},
		{"https://youtube.com.evil.test/watch?v=dQw4w9WgXcQ", "", "", false},
		{"https://user:pw@youtube.com/watch?v=dQw4w9WgXcQ", "", "", false},
		{"ftp://youtube.com/watch?v=dQw4w9WgXcQ", "", "", false},
		{"javascript:alert(1)//youtube.com/watch?v=dQw4w9WgXcQ", "", "", false},
		{"/watch?v=dQw4w9WgXcQ", "", "", false},
		{"https://vimeo.com/about", "", "", false},
		{"https://vimeo.com/76979871/not-a-hash", "", "", false},
		{"https://vimeo.com/channels/staffpicks", "", "", false},
		{"https://player.vimeo.com/video/abc", "", "", false},
		{"http://[::1", "", "", false},
		{"https://vm.tiktok.com/ZMabc123/", "", "", false},
		{"https://www.tiktok.com/t/ZTabc123/", "", "", false},
		{"https://www.tiktok.com/@scout2015", "", "", false},
		{"https://www.tiktok.com/@scout2015/video/123", "", "", false},
		{"https://www.tiktok.com/@scout2015/video/6718335390845095173x", "", "", false},
		{"https://www.tiktok.com/@scout<2015/video/6718335390845095173", "", "", false},
		{"https://www.tiktok.com/scout2015/video/6718335390845095173", "", "", false},
		{"https://www.tiktok.com/@scout2015/live", "", "", false},
		{"https://tiktok.com.evil.test/@a/video/6718335390845095173", "", "", false},
		{"https://www.tiktok.com/tag/cats", "", "", false},
	} {
		v, ok := match(tc.href, all)
		if tc.provider == "" {
			if ok {
				t.Errorf("match(%q) = %+v, want no match", tc.href, v)
			}
			continue
		}
		if !ok || v.provider.id != tc.provider || v.player != tc.player || v.portrait != tc.portrait {
			t.Errorf("match(%q) = %q %q portrait=%v, ok=%v\nwant %q %q portrait=%v", tc.href, providerID(v), v.player, v.portrait, ok, tc.provider, tc.player, tc.portrait)
		}
	}
}

const ttPlayer = "https://www.tiktok.com/player/v1/6718335390845095173?autoplay=1&description=1&rel=0"

func TestTikTokAuthor(t *testing.T) {
	for href, want := range map[string]string{
		"https://www.tiktok.com/@scout2015/video/6718335390845095173": "@scout2015",
		"https://www.tiktok.com/@a.b_c/photo/6718335390845095173":     "@a.b_c",
		"https://m.tiktok.com/v/6718335390845095173.html":             "",
		"https://www.tiktok.com/embed/v2/6718335390845095173":         "",
	} {
		if v, ok := match(href, all); !ok || v.author != want {
			t.Errorf("match(%q) author = %q (ok=%v), want %q", href, v.author, ok, want)
		}
	}
}

// TestManifestCoversProviders: every provider has its bool setting and its
// player's origin under "csp" → "frame-src", or its iframe would be blocked.
func TestManifestCoversProviders(t *testing.T) {
	raw, err := os.ReadFile("plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		CSP      map[string][]string `json:"csp"`
		Settings []struct{ Key, Type, Default string }
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	samples := map[string]string{"youtube": yt, "vimeo": "https://vimeo.com/76979871", "tiktok": tt}
	for _, p := range providers {
		v, ok := match(samples[p.id], all)
		if !ok {
			t.Fatalf("no sample link for %s", p.id)
		}
		u, _ := url.Parse(v.player)
		if !slices.Contains(m.CSP["frame-src"], "https://"+u.Host) {
			t.Errorf("%s: frame-src lacks https://%s", p.id, u.Host)
		}
		if !slices.ContainsFunc(m.Settings, func(s struct{ Key, Type, Default string }) bool {
			return s.Key == p.id && s.Type == "bool" && s.Default == "true"
		}) {
			t.Errorf("%s: no bool setting defaulting to true", p.id)
		}
	}
	if len(m.CSP["frame-src"]) != len(providers) || len(m.CSP) != 1 {
		t.Errorf("csp asks for more than the players: %v", m.CSP)
	}
}

func providerID(v video) string {
	if v.provider == nil {
		return ""
	}
	return v.provider.id
}

func TestMatchDisabledProvider(t *testing.T) {
	if _, ok := match("https://youtu.be/dQw4w9WgXcQ", map[string]bool{"vimeo": true}); ok {
		t.Error("YouTube matched while disabled")
	}
	if _, ok := match("https://vimeo.com/76979871", map[string]bool{"youtube": true}); ok {
		t.Error("Vimeo matched while disabled")
	}
	if _, ok := match("https://www.tiktok.com/@a/video/6718335390845095173", map[string]bool{"youtube": true, "vimeo": true}); ok {
		t.Error("TikTok matched while disabled")
	}
}

func TestSeconds(t *testing.T) {
	for in, want := range map[string]int{
		"": 0, "90": 90, "90s": 90, "1m30s": 90, "1m": 60, "2h": 7200, "1h2m3s": 3723, "1H2M3S": 3723,
		"bogus": 0, "-5": 0, "1.5": 0, "1m30": 90, " 45 ": 45,
	} {
		if got := seconds(in); got != want {
			t.Errorf("seconds(%q) = %d, want %d", in, got, want)
		}
	}
}
