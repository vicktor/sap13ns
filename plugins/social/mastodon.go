package main

import (
	"encoding/json"
	"net/url"
	"strconv"

	"github.com/vicktor/sap13ns/pluginsdk"
)

// MastodonStatus is the body of POST /api/v1/statuses.
type MastodonStatus struct {
	Status     string `json:"status"`
	Language   string `json:"language,omitempty"`
	Visibility string `json:"visibility"`
}

// idempotencyKey makes Mastodon ignore a repeated request for the same
// post (it remembers keys for a while), on top of the plugin's own record.
func idempotencyKey(siteURL string, postID int64) string {
	host := siteURL
	if u, err := url.Parse(siteURL); err == nil && u.Host != "" {
		host = u.Host
	}
	return "sap13ns-" + host + "-" + strconv.FormatInt(postID, 10)
}

// MastodonRequest builds the request that publishes a status.
func MastodonRequest(c Config, siteURL, text string, p pluginsdk.Post) pluginsdk.HTTPRequest {
	body, _ := json.Marshal(MastodonStatus{Status: text, Language: baseLang(p.Lang), Visibility: c.MastodonVisibility})
	return pluginsdk.HTTPRequest{Method: "POST", URL: c.MastodonInstance + "/api/v1/statuses", Body: string(body),
		Headers: map[string]string{
			"Content-Type":    "application/json",
			"Accept":          "application/json",
			"Authorization":   "Bearer " + c.MastodonToken,
			"Idempotency-Key": idempotencyKey(siteURL, p.ID),
		}}
}

// postMastodon publishes the status.
func postMastodon(c Config, siteURL, text string, p pluginsdk.Post) sendResult {
	resp, err := pluginsdk.Fetch(MastodonRequest(c, siteURL, text, p))
	if r, bad := fetchProblem(resp, err, c.MastodonInstance); bad {
		return r
	}
	var st struct {
		URL string `json:"url"`
		URI string `json:"uri"`
	}
	json.Unmarshal([]byte(resp.Body), &st)
	if st.URL == "" {
		st.URL = st.URI
	}
	return sendResult{done: true, url: st.URL}
}
