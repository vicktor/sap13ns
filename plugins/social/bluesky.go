package main

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/vicktor/sap13ns/pluginsdk"
	"github.com/vicktor/sap13ns/pluginsdk/autopost"
)

// Facet is a Bluesky rich-text annotation. Offsets are UTF-8 bytes.
type Facet struct {
	Index    FacetIndex     `json:"index"`
	Features []FacetFeature `json:"features"`
}

type FacetIndex struct {
	ByteStart int `json:"byteStart"`
	ByteEnd   int `json:"byteEnd"`
}

type FacetFeature struct {
	Type string `json:"$type"`
	URI  string `json:"uri,omitempty"`
	Tag  string `json:"tag,omitempty"`
}

var hashtagRe = regexp.MustCompile(`(?:^|\s)(#[\p{L}\p{M}\p{N}_]+)`)

// Facets finds the links and hashtags of a text, as Bluesky clients do:
// without facets they would show as plain text.
func Facets(text string) []Facet {
	var out []Facet
	for _, m := range autopost.URLs.FindAllStringIndex(text, -1) {
		start, end := m[0], m[1]
		end = start + len(trimURL(text[start:end]))
		out = append(out, Facet{Index: FacetIndex{start, end},
			Features: []FacetFeature{{Type: "app.bsky.richtext.facet#link", URI: text[start:end]}}})
	}
	for _, m := range hashtagRe.FindAllStringSubmatchIndex(text, -1) {
		start, end := m[2], m[3]
		tag := text[start+1 : end]
		if !autopost.ValidTag(tag) {
			continue
		}
		out = append(out, Facet{Index: FacetIndex{start, end},
			Features: []FacetFeature{{Type: "app.bsky.richtext.facet#tag", Tag: tag}}})
	}
	return out
}

// trimURL drops punctuation that ends a sentence rather than the link.
func trimURL(u string) string {
	for u != "" {
		last := u[len(u)-1]
		switch {
		case strings.IndexByte(".,;:!?'\"", last) >= 0:
			u = u[:len(u)-1]
		case last == ')' && strings.Count(u, "(") < strings.Count(u, ")"):
			u = u[:len(u)-1]
		default:
			return u
		}
	}
	return u
}

// BlueskyPost is the record of an app.bsky.feed.post.
type BlueskyPost struct {
	Type      string        `json:"$type"`
	Text      string        `json:"text"`
	CreatedAt string        `json:"createdAt"`
	Langs     []string      `json:"langs,omitempty"`
	Facets    []Facet       `json:"facets,omitempty"`
	Embed     *BlueskyEmbed `json:"embed,omitempty"`
}

type BlueskyEmbed struct {
	Type     string          `json:"$type"`
	External BlueskyExternal `json:"external"`
}

type BlueskyExternal struct {
	URI         string `json:"uri"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

// BlueskyRecord builds the post record: the text with its facets, the
// post's language, and a link card (without a thumbnail: uploading one
// would need the image bytes).
func BlueskyRecord(text string, p pluginsdk.Post, at time.Time) BlueskyPost {
	rec := BlueskyPost{
		Type:      "app.bsky.feed.post",
		Text:      text,
		CreatedAt: at.UTC().Format("2006-01-02T15:04:05.000Z"),
		Facets:    Facets(text),
	}
	if l := strings.TrimSpace(p.Lang); l != "" {
		rec.Langs = []string{l}
	}
	if p.URL != "" {
		rec.Embed = &BlueskyEmbed{Type: "app.bsky.embed.external", External: BlueskyExternal{
			URI: p.URL, Title: autopost.Truncate(p.Title, 300), Description: autopost.Truncate(p.Excerpt, 300)}}
	}
	return rec
}

// postBluesky signs in with the app password and creates the post.
func postBluesky(c Config, text string, p pluginsdk.Post, at time.Time) autopost.Result {
	body, _ := json.Marshal(map[string]string{"identifier": c.BlueskyHandle, "password": c.BlueskyPassword})
	resp, err := pluginsdk.Fetch(pluginsdk.HTTPRequest{Method: "POST", URL: c.BlueskyService + "/xrpc/com.atproto.server.createSession",
		Headers: map[string]string{"Content-Type": "application/json", "Accept": "application/json"}, Body: string(body)})
	if r, bad := fetchProblem(resp, err, c.BlueskyService); bad {
		// No post can exist without a session, so this is always safe to
		// retry.
		r.Failed, r.Detail = true, "signing in: "+r.Detail
		return r
	}
	var session struct {
		AccessJwt string `json:"accessJwt"`
		DID       string `json:"did"`
	}
	if json.Unmarshal([]byte(resp.Body), &session) != nil || session.AccessJwt == "" || session.DID == "" {
		return autopost.Result{Failed: true, Detail: "signing in: unexpected answer from the server"}
	}

	body, _ = json.Marshal(map[string]any{"repo": session.DID, "collection": "app.bsky.feed.post", "record": BlueskyRecord(text, p, at)})
	resp, err = pluginsdk.Fetch(pluginsdk.HTTPRequest{Method: "POST", URL: c.BlueskyService + "/xrpc/com.atproto.repo.createRecord",
		Headers: map[string]string{"Content-Type": "application/json", "Accept": "application/json", "Authorization": "Bearer " + session.AccessJwt},
		Body:    string(body)})
	if r, bad := fetchProblem(resp, err, c.BlueskyService); bad {
		return r
	}
	var created struct {
		URI string `json:"uri"`
	}
	json.Unmarshal([]byte(resp.Body), &created)
	return autopost.Result{Done: true, URL: created.URI, Link: blueskyLink(created.URI)}
}

// blueskyLink turns at://did/app.bsky.feed.post/rkey into the address of
// the post in the Bluesky web app (without the scheme: it is only shown in
// the log).
func blueskyLink(uri string) string {
	rest, ok := strings.CutPrefix(uri, "at://")
	if !ok {
		return ""
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 3 || parts[1] != "app.bsky.feed.post" {
		return ""
	}
	return "bsky.app/profile/" + parts[0] + "/post/" + parts[2]
}

// fetchProblem classifies a failed request (autopost.FetchProblem), with
// the README section that explains how to add a server.
func fetchProblem(resp *pluginsdk.HTTPResponse, err error, base string) (autopost.Result, bool) {
	return autopost.FetchProblem(resp, err, base, "; see “Adding a Mastodon instance or a Bluesky server” in the plugin's README")
}
