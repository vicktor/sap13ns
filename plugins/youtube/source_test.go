package main

import (
	"strings"
	"testing"
)

func TestParseChannel(t *testing.T) {
	for _, tc := range []struct {
		in   string
		kind string
		id   string
		err  string // substring of the error; "" for none
	}{
		{in: gfdChannel, kind: kindChannel, id: gfdChannel},
		{in: "  " + gfdChannel + " ", kind: kindChannel, id: gfdChannel},
		{in: "https://www.youtube.com/channel/" + gfdChannel, kind: kindChannel, id: gfdChannel},
		{in: "http://youtube.com/channel/" + gfdChannel + "/videos", kind: kindChannel, id: gfdChannel},
		{in: "m.youtube.com/channel/" + gfdChannel, kind: kindChannel, id: gfdChannel},
		{in: "@GoogleDevelopers", kind: kindHandle, id: "GoogleDevelopers"},
		{in: "GoogleDevelopers", kind: kindHandle, id: "GoogleDevelopers"},
		{in: "https://www.youtube.com/@GoogleDevelopers/videos", kind: kindHandle, id: "GoogleDevelopers"},
		{in: "https://www.youtube.com/@caf%C3%A9.m%C3%BAsica", kind: kindHandle, id: "café.música"},
		{in: "@名前のチャンネル", kind: kindHandle, id: "名前のチャンネル"},
		{in: "https://www.youtube.com/playlist?list=PLOU2XLYxmsIJ8ItHmK4bRlY4GCzMgXLAJ", kind: kindPlaylist, id: "PLOU2XLYxmsIJ8ItHmK4bRlY4GCzMgXLAJ"},
		{in: "https://www.youtube.com/c/GoogleDevelopers", err: "old custom URL"},
		{in: "https://www.youtube.com/user/GoogleDevelopers", err: "old custom URL"},
		{in: "https://www.youtube.com/watch?v=dQw4w9WgXcQ", err: "not a channel URL"},
		{in: "https://youtube.com.evil.test/channel/" + gfdChannel, err: "not a channel id"},
		{in: "https://user:pw@www.youtube.com/channel/" + gfdChannel, err: "not a channel id"},
		{in: "javascript:alert(1)", err: "not a channel id"},
		{in: "@a", err: "not a channel id"},
		{in: "@" + strings.Repeat("x", 31), err: "not a channel id"},
		{in: "@bad name", err: "not a channel id"},
		{in: `@x"><script>`, err: "not a channel id"},
		{in: "", err: "not a channel id"},
	} {
		s, err := ParseChannel(tc.in)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("ParseChannel(%q) = %+v, %v; want an error with %q", tc.in, s, err, tc.err)
			}
			continue
		}
		if err != nil || s.Kind != tc.kind || s.ID != tc.id {
			t.Errorf("ParseChannel(%q) = %+v, %v; want %s %s", tc.in, s, err, tc.kind, tc.id)
		}
	}
}

func TestParsePlaylist(t *testing.T) {
	const pl = "PLOU2XLYxmsIJ8ItHmK4bRlY4GCzMgXLAJ"
	for in, want := range map[string]string{
		pl:         pl,
		gfdUploads: gfdUploads,
		"https://www.youtube.com/playlist?list=" + pl:            pl,
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ&list=" + pl: pl,
		"youtube.com/playlist?list=" + pl:                        pl,
		"https://www.youtube.com/playlist?list=bad%20id":         "",
		"https://www.youtube.com/playlist?list=" + gfdChannel:    "", // a channel id
		"https://example.com/playlist?list=" + pl:                "",
		"short":    "",
		gfdChannel: "",
	} {
		s, err := ParsePlaylist(in)
		if want == "" {
			if err == nil {
				t.Errorf("ParsePlaylist(%q) = %+v, want an error", in, s)
			}
			continue
		}
		if err != nil || s.Kind != kindPlaylist || s.ID != want {
			t.Errorf("ParsePlaylist(%q) = %+v, %v; want %s", in, s, err, want)
		}
	}
}

func TestPick(t *testing.T) {
	const pl = "PLOU2XLYxmsIJ8ItHmK4bRlY4GCzMgXLAJ"
	for _, tc := range []struct {
		channel, playlist, defChannel, defPlaylist string
		want                                       string // key, or "" for errNoSource
	}{
		{"", "", gfdChannel, "", "c:" + gfdChannel},
		{"", "", gfdChannel, pl, "p:" + pl}, // the playlist wins
		{"@Other", "", gfdChannel, pl, "h:other"},
		{"", pl, gfdChannel, "", "p:" + pl},
		{"@Other", pl, "", "", "p:" + pl},
		{" ", " ", "", "", ""},
	} {
		s, err := Pick(tc.channel, tc.playlist, tc.defChannel, tc.defPlaylist)
		if tc.want == "" {
			if err != errNoSource {
				t.Errorf("Pick(%+v) = %+v, %v; want errNoSource", tc, s, err)
			}
			continue
		}
		if err != nil || s.Key() != tc.want {
			t.Errorf("Pick(%+v) = %q, %v; want %q", tc, s.Key(), err, tc.want)
		}
	}
}

func TestSourceKeysAndPages(t *testing.T) {
	for _, tc := range []struct {
		src       Source
		key, page string
	}{
		{Source{kindChannel, gfdChannel}, "c:" + gfdChannel, "https://www.youtube.com/channel/" + gfdChannel},
		{Source{kindHandle, "GoogleDevelopers"}, "h:googledevelopers", "https://www.youtube.com/@GoogleDevelopers"},
		{Source{kindHandle, "café.música"}, "h:café.música", "https://www.youtube.com/@caf%C3%A9.m%C3%BAsica"},
		{Source{kindPlaylist, "PLOU2XLYxmsIJ8ItHmK4bRlY4GCzMgXLAJ"}, "p:PLOU2XLYxmsIJ8ItHmK4bRlY4GCzMgXLAJ", "https://www.youtube.com/playlist?list=PLOU2XLYxmsIJ8ItHmK4bRlY4GCzMgXLAJ"},
	} {
		if got := tc.src.Key(); got != tc.key {
			t.Errorf("%+v.Key() = %q, want %q", tc.src, got, tc.key)
		}
		if got := tc.src.Page(); got != tc.page {
			t.Errorf("%+v.Page() = %q, want %q", tc.src, got, tc.page)
		}
		back, ok := SourceFromKey(tc.key)
		if !ok || back.Key() != tc.key {
			t.Errorf("SourceFromKey(%q) = %+v, %v", tc.key, back, ok)
		}
	}
	for _, bad := range []string{"", "c:", "c:UCshort", "x:" + gfdChannel, "h:a", "p:bad id", gfdChannel} {
		if s, ok := SourceFromKey(bad); ok {
			t.Errorf("SourceFromKey(%q) = %+v, want invalid", bad, s)
		}
	}
}
