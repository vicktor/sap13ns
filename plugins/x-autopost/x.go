package main

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/vicktor/sap13ns/pluginsdk"
	"github.com/vicktor/sap13ns/pluginsdk/autopost"
)

// tweetsURL creates a post (X API v2, POST /2/tweets). The trailing path
// ends the host for the package scanner.
const tweetsURL = "https://api.x.com/2/tweets"

// TweetRequest is the body of POST /2/tweets. The link card is not part of
// it: X builds it from the page's Open Graph and twitter:card tags.
type TweetRequest struct {
	Text string `json:"text"`
}

// TweetRequestFor builds the signed request.
func TweetRequestFor(o OAuth1, text string, nonce string, at time.Time) pluginsdk.HTTPRequest {
	body, _ := json.Marshal(TweetRequest{Text: text})
	return pluginsdk.HTTPRequest{Method: "POST", URL: tweetsURL, Body: string(body), Headers: map[string]string{
		"Content-Type":  "application/json",
		"Accept":        "application/json",
		"Authorization": o.Header("POST", tweetsURL, nil, nonce, at),
	}}
}

// postX publishes the text.
func postX(o OAuth1, text string) autopost.Result {
	resp, err := pluginsdk.Fetch(TweetRequestFor(o, text, newNonce(), now()))
	if err == nil && resp != nil {
		switch resp.Status {
		case 429:
			return rateLimited(resp)
		case 401, 402, 403:
			r, _ := autopost.FetchProblem(resp, nil, tweetsURL, "")
			r.Detail += refusalHint(resp)
			return r
		}
	}
	if r, bad := autopost.FetchProblem(resp, err, tweetsURL, ""); bad {
		return r
	}
	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	json.Unmarshal([]byte(resp.Body), &created)
	if created.Data.ID == "" {
		// X accepted it (2xx) but its answer is not the documented one.
		return autopost.Result{Done: true, URL: "(X answered HTTP " + strconv.Itoa(resp.Status) + " without the post's id)"}
	}
	return autopost.Result{Done: true, URL: "https://x.com/i/web/status/" + created.Data.ID}
}

// rateLimited reads when X lets the account post again. X sends a reset
// time (unix seconds) for the 15-minute window and for the 24-hour caps of
// the user and the app; the one that ran out decides.
func rateLimited(resp *pluginsdk.HTTPResponse) autopost.Result {
	resetOf := func(prefix string) (time.Time, bool) {
		at, err := strconv.ParseInt(resp.Headers[prefix+"-reset"], 10, 64)
		return time.Unix(at, 0), err == nil
	}
	var reset time.Time
	for _, prefix := range []string{"x-rate-limit", "x-user-limit-24hour", "x-app-limit-24hour"} {
		if t, ok := resetOf(prefix); ok && resp.Headers[prefix+"-remaining"] == "0" && t.After(reset) {
			reset = t
		}
	}
	if t, ok := resetOf("x-rate-limit"); ok && reset.IsZero() {
		reset = t
	}
	n := now()
	switch {
	case reset.IsZero() || !reset.After(n):
		reset = n.Add(15 * time.Minute)
	case reset.After(n.Add(25 * time.Hour)):
		reset = n.Add(25 * time.Hour)
	}
	return autopost.Result{Failed: true, Later: reset, Detail: "rate limited (HTTP 429" + autopost.ErrorText(resp.Body) + ")"}
}

// refusalHint explains the refusals an administrator can fix.
func refusalHint(resp *pluginsdk.HTTPResponse) string {
	var p struct {
		Title  string `json:"title"`
		Detail string `json:"detail"`
		Type   string `json:"type"`
		Reason string `json:"reason"`
	}
	json.Unmarshal([]byte(resp.Body), &p)
	all := strings.ToLower(p.Title + " " + p.Detail + " " + p.Type + " " + p.Reason)
	switch {
	case resp.Status == 401:
		return ". The keys or the tokens are wrong, or were regenerated: copy all four again from the Developer Console"
	case resp.Status == 402 || strings.Contains(all, "credit"):
		return ". The developer account has no credits left: buy credits in the Developer Console (X charges per post)"
	case strings.Contains(all, "client-not-enrolled") || strings.Contains(all, "client-forbidden"):
		return ". The app cannot use this endpoint: in the Developer Console, check that the app belongs to a project on the pay-per-use plan"
	case strings.Contains(all, "duplicate"):
		return ". X refuses a text identical to a recent post"
	case strings.Contains(all, "oauth1-permissions") || strings.Contains(all, "not permitted"):
		return ". The access token may be read-only: set the app's permissions to “Read and write”, then generate the Access Token and Secret again"
	}
	return ""
}
