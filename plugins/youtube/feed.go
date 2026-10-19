package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// A Video is one entry of a gallery, as stored in the plugin's key-value
// store (short JSON names keep the records small).
type Video struct {
	ID        string `json:"id"`
	Title     string `json:"t"`
	Published int64  `json:"p,omitempty"` // Unix seconds
	Duration  int    `json:"d,omitempty"` // seconds; 0 when unknown (feeds)
	Short     bool   `json:"s,omitempty"` // a YouTube Short (the feed links it as /shorts/ID)
	Live      string `json:"l,omitempty"` // "live" or "upcoming" (Data API only)
}

// A List is what a fetch returns: the source's name and its newest videos.
type List struct {
	Title   string // channel name, or playlist title
	Author  string // channel name (playlists)
	Channel string // the channel id, when the response gives it
	Videos  []Video
}

const (
	maxVideos     = 24  // per source, stored and shown
	maxTitleRunes = 150 // YouTube allows 100 characters
	maxNameRunes  = 100
)

var videoID = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

// ParseFeed reads a YouTube Atom feed (the format of
// https://www.youtube.com/feeds/videos.xml, documented with the push
// notifications of the Data API). Entries without a valid video id are
// skipped; at most max videos are kept.
func ParseFeed(b []byte, max int) (List, error) {
	type link struct {
		Rel  string `xml:"rel,attr"`
		Href string `xml:"href,attr"`
	}
	type entry struct {
		VideoID   string `xml:"http://www.youtube.com/xml/schemas/2015 videoId"`
		ChannelID string `xml:"http://www.youtube.com/xml/schemas/2015 channelId"`
		Title     string `xml:"http://www.w3.org/2005/Atom title"`
		Links     []link `xml:"http://www.w3.org/2005/Atom link"`
		Published string `xml:"http://www.w3.org/2005/Atom published"`
	}
	var out List
	dec := xml.NewDecoder(bytes.NewReader(b))
	dec.Strict = true
	dec.Entity = map[string]string{} // only the five predefined entities
	sawFeed := false
	for len(out.Videos) < max {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return List{}, errors.New("the feed is not valid XML: " + err.Error())
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch {
		case !sawFeed:
			if se.Name.Space != "http://www.w3.org/2005/Atom" || se.Name.Local != "feed" {
				return List{}, errors.New("the response is not an Atom feed")
			}
			sawFeed = true
		case se.Name.Local == "entry":
			var e entry
			if err := dec.DecodeElement(&e, &se); err != nil {
				return List{}, errors.New("the feed is not valid XML: " + err.Error())
			}
			v := Video{ID: strings.TrimSpace(e.VideoID), Title: cleanText(e.Title, maxTitleRunes)}
			if !videoID.MatchString(v.ID) {
				continue
			}
			if t, err := time.Parse(time.RFC3339, strings.TrimSpace(e.Published)); err == nil {
				v.Published = t.Unix()
			}
			for _, l := range e.Links {
				if l.Rel == "alternate" {
					if u, err := url.Parse(l.Href); err == nil && strings.HasPrefix(u.Path, "/shorts/") {
						v.Short = true
					}
				}
			}
			if out.Channel == "" && channelID.MatchString(e.ChannelID) {
				out.Channel = e.ChannelID
			}
			out.Videos = append(out.Videos, v)
		case se.Name.Local == "title" && se.Name.Space == "http://www.w3.org/2005/Atom":
			var s string
			if err := dec.DecodeElement(&s, &se); err == nil {
				out.Title = cleanText(s, maxNameRunes)
			}
		case se.Name.Local == "author":
			var a struct {
				Name string `xml:"http://www.w3.org/2005/Atom name"`
			}
			if err := dec.DecodeElement(&a, &se); err == nil {
				out.Author = cleanText(a.Name, maxNameRunes)
			}
		case se.Name.Local == "channelId" && se.Name.Space == "http://www.youtube.com/xml/schemas/2015":
			// The feed's own channelId leaves out the "UC" prefix
			// ("_x5XG1OV2P6uZZ5FSM9Ttw"); the entries carry the full id.
			var s string
			if err := dec.DecodeElement(&s, &se); err == nil && out.Channel == "" {
				if s = strings.TrimSpace(s); channelID.MatchString(s) {
					out.Channel = s
				} else if channelID.MatchString("UC" + s) {
					out.Channel = "UC" + s
				}
			}
		}
	}
	if !sawFeed {
		return List{}, errors.New("the response is not an Atom feed")
	}
	return out, nil
}

// cleanText collapses white space, drops control and format characters
// (bidirectional overrides included) and cuts the text to max runes.
func cleanText(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) && r != '‍' {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		s = strings.TrimSpace(string(r[:max-1])) + "…"
	}
	return s
}
