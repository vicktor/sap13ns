package main

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/vicktor/sap13ns/pluginsdk"
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
	for _, m := range urlRe.FindAllStringIndex(text, -1) {
		start, end := m[0], m[1]
		end = start + len(trimURL(text[start:end]))
		out = append(out, Facet{Index: FacetIndex{start, end},
			Features: []FacetFeature{{Type: "app.bsky.richtext.facet#link", URI: text[start:end]}}})
	}
	for _, m := range hashtagRe.FindAllStringSubmatchIndex(text, -1) {
		start, end := m[2], m[3]
		tag := text[start+1 : end]
		if allDigits(tag) || len([]rune(tag)) > maxTagRunes {
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
			URI: p.URL, Title: Truncate(p.Title, 300), Description: Truncate(p.Excerpt, 300)}}
	}
	return rec
}

// sendResult is the outcome of one network's delivery.
type sendResult struct {
	// done: posted, with url. failed: the server refused or the request
	// never left (safe to retry). Otherwise the outcome is unknown.
	done, failed bool
	url, link    string // link: a web address for people, when url is not one
	detail       string // why it failed, for the log
}

// postBluesky signs in with the app password and creates the post.
func postBluesky(c Config, text string, p pluginsdk.Post, at time.Time) sendResult {
	body, _ := json.Marshal(map[string]string{"identifier": c.BlueskyHandle, "password": c.BlueskyPassword})
	resp, err := pluginsdk.Fetch(pluginsdk.HTTPRequest{Method: "POST", URL: c.BlueskyService + "/xrpc/com.atproto.server.createSession",
		Headers: map[string]string{"Content-Type": "application/json", "Accept": "application/json"}, Body: string(body)})
	if r, bad := fetchProblem(resp, err, c.BlueskyService); bad {
		// No post can exist without a session, so this is always safe to
		// retry.
		r.failed, r.detail = true, "signing in: "+r.detail
		return r
	}
	var session struct {
		AccessJwt string `json:"accessJwt"`
		DID       string `json:"did"`
	}
	if json.Unmarshal([]byte(resp.Body), &session) != nil || session.AccessJwt == "" || session.DID == "" {
		return sendResult{failed: true, detail: "signing in: unexpected answer from the server"}
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
	return sendResult{done: true, url: created.URI, link: blueskyLink(created.URI)}
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

// fetchProblem classifies a failed request. A response with an error
// status, or an error raised before anything was sent, is safe to retry
// (failed); any other error leaves the outcome unknown.
func fetchProblem(resp *pluginsdk.HTTPResponse, err error, base string) (sendResult, bool) {
	switch {
	case errors.Is(err, pluginsdk.ErrDenied):
		return sendResult{failed: true, detail: "the host " + hostOf(base) + " is not in this plugin's permissions (http:" + hostOf(base) +
			"); see “Adding a Mastodon instance or a Bluesky server” in the plugin's README"}, true
	case errors.Is(err, pluginsdk.ErrLimit), errors.Is(err, pluginsdk.ErrInvalid), errors.Is(err, pluginsdk.ErrUnavailable):
		return sendResult{failed: true, detail: err.Error()}, true
	case err != nil:
		msg := err.Error()
		if notSent(msg) {
			return sendResult{failed: true, detail: shorten(msg, 160)}, true
		}
		return sendResult{detail: "no answer (" + shorten(msg, 120) + ")"}, true
	case resp.Status < 200 || resp.Status > 299:
		return sendResult{failed: true, detail: "HTTP " + itoa(resp.Status) + errorText(resp.Body)}, true
	}
	return sendResult{}, false
}

// notSent recognizes errors raised before the request reached the server.
func notSent(msg string) bool {
	for _, s := range []string{"no such host", "connection refused", "destination address is not allowed",
		"only absolute https://", "redirect to a host", "method not allowed", "request body too large", "network is unreachable"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// errorText extracts the error message of a Bluesky or Mastodon error
// answer ({"error": "...", "message": "..."}), never the whole body.
func errorText(body string) string {
	var e struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal([]byte(body), &e) != nil || e.Error == "" && e.Message == "" {
		return ""
	}
	parts := []string{}
	for _, s := range []string{e.Error, e.Message} {
		if s = strings.TrimSpace(s); s != "" {
			parts = append(parts, s)
		}
	}
	return ": " + shorten(strings.Join(parts, ": "), 200)
}

func shorten(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
