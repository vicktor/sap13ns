package main

import "testing"

var all = map[string]bool{"youtube": true, "vimeo": true}

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
