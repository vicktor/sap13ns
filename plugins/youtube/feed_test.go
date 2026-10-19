package main

import (
	"strings"
	"testing"
	"time"
)

// The feeds in testdata/ were captured once from YouTube on 2026-10-19:
// the channel and the uploads playlist of Google for Developers.
func TestParseChannelFeed(t *testing.T) {
	l, err := ParseFeed([]byte(readTestdata(t, "channel-feed.xml")), maxVideos)
	if err != nil {
		t.Fatal(err)
	}
	if l.Title != "Google for Developers" || l.Author != "Google for Developers" || l.Channel != gfdChannel {
		t.Fatalf("feed: %q by %q, channel %q", l.Title, l.Author, l.Channel)
	}
	if len(l.Videos) != 15 {
		t.Fatalf("%d videos, want the feed's 15", len(l.Videos))
	}
	first := l.Videos[0]
	want := Video{ID: "LJ2v4-FvPcA", Title: "🧰 Builders building with Gemini Audio",
		Published: time.Date(2026, 10, 16, 21, 0, 0, 0, time.UTC).Unix(), Short: true}
	if first != want {
		t.Errorf("first video %+v\nwant %+v", first, want)
	}
	if v := l.Videos[3]; v.ID != "i3X7Fv0Z3Uc" || v.Short || v.Duration != 0 {
		t.Errorf("fourth video %+v: want a regular video without a duration", v)
	}
	shorts := 0
	for _, v := range l.Videos {
		if v.Short {
			shorts++
		}
		if !videoID.MatchString(v.ID) || v.Title == "" || v.Published == 0 {
			t.Errorf("incomplete video %+v", v)
		}
	}
	if shorts != 8 {
		t.Errorf("%d Shorts, want 8", shorts)
	}
}

func TestParsePlaylistFeed(t *testing.T) {
	l, err := ParseFeed([]byte(readTestdata(t, "playlist-feed.xml")), 5)
	if err != nil {
		t.Fatal(err)
	}
	if l.Title != "Uploads from Google for Developers" || l.Author != "Google for Developers" || l.Channel != gfdChannel {
		t.Fatalf("feed: %q by %q, channel %q", l.Title, l.Author, l.Channel)
	}
	if len(l.Videos) != 5 || l.Videos[0].ID != "LJ2v4-FvPcA" {
		t.Fatalf("videos: %+v", l.Videos)
	}
}

func TestParseFeedRefusals(t *testing.T) {
	for name, in := range map[string]string{
		"html (a consent page)": `<!DOCTYPE html><html><body>Before you continue to YouTube</body></html>`,
		"rss":                   `<?xml version="1.0"?><rss><channel><title>x</title></channel></rss>`,
		"broken xml":            `<feed xmlns="http://www.w3.org/2005/Atom"><entry><title>x</entry></feed>`,
		"entity expansion": `<?xml version="1.0"?><!DOCTYPE feed [<!ENTITY a "aaaaaaaaaa"><!ENTITY b "&a;&a;&a;&a;">]>` +
			`<feed xmlns="http://www.w3.org/2005/Atom"><title>&b;</title></feed>`,
		"empty": ``,
	} {
		if l, err := ParseFeed([]byte(in), maxVideos); err == nil {
			t.Errorf("%s: parsed as %+v, want an error", name, l)
		}
	}
}

func TestParseFeedCleansEntries(t *testing.T) {
	in := `<?xml version="1.0"?><feed xmlns:yt="http://www.youtube.com/xml/schemas/2015" xmlns="http://www.w3.org/2005/Atom">` +
		`<title>  My   channel ` + "‮" + `</title>` +
		`<entry><yt:videoId>bad id</yt:videoId><title>skipped</title></entry>` +
		`<entry><yt:videoId>dQw4w9WgXcQ</yt:videoId><title>` + strings.Repeat("ab ", 100) + "\u0085" + `</title>` +
		`<link rel="alternate" href="https://www.youtube.com/watch?v=dQw4w9WgXcQ"/><published>not a date</published></entry>` +
		`<entry><yt:videoId>aBcDeFgHiJk</yt:videoId><title>&lt;b&gt;Tom &amp; Jerry&lt;/b&gt;</title></entry>` +
		`</feed>`
	l, err := ParseFeed([]byte(in), maxVideos)
	if err != nil {
		t.Fatal(err)
	}
	if l.Title != "My channel" {
		t.Errorf("title %q", l.Title)
	}
	if len(l.Videos) != 2 {
		t.Fatalf("videos: %+v", l.Videos)
	}
	if v := l.Videos[0]; len([]rune(v.Title)) != maxTitleRunes || !strings.HasSuffix(v.Title, "…") || v.Published != 0 || v.Short {
		t.Errorf("long title not cut, or date invented: %+v", v)
	}
	if l.Videos[1].Title != "<b>Tom & Jerry</b>" { // escaped when rendered
		t.Errorf("title %q", l.Videos[1].Title)
	}
}

func TestParseDuration(t *testing.T) {
	for in, want := range map[string]int{
		"PT15M33S": 933, "PT1H2M3S": 3723, "PT58S": 58, "PT4M": 240, "PT2H": 7200, "P1DT2H": 93600, "P0D": 0,
		"": 0, "15:33": 0, "PT": 0, "PTXS": 0, "-PT1S": 0,
	} {
		if got := ParseDuration(in); got != want {
			t.Errorf("ParseDuration(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestClockAndSpoken(t *testing.T) {
	for sec, want := range map[int][2]string{
		58: {"0:58", "58 s"}, 253: {"4:13", "4 min 13 s"}, 240: {"4:00", "4 min"},
		3723: {"1:02:03", "1 h 2 min"}, 7200: {"2:00:00", "2 h"},
	} {
		if c, s := Clock(sec), Spoken(sec); c != want[0] || s != want[1] {
			t.Errorf("%d s: %q %q, want %q %q", sec, c, s, want[0], want[1])
		}
	}
}
