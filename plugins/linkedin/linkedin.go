package main

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/vicktor/sap13ns/pluginsdk"
	"github.com/vicktor/sap13ns/pluginsdk/autopost"
)

// postsURL is the Posts API (LinkedIn Marketing / Community Management,
// versioned). The trailing path ends the host for the package scanner.
const postsURL = "https://api.linkedin.com/rest/posts"

// Share is the body of POST /rest/posts for an article share: the text,
// and a card with the article's link, title and description.
type Share struct {
	Author                    string       `json:"author"`
	Commentary                string       `json:"commentary"`
	Visibility                string       `json:"visibility"`
	Distribution              Distribution `json:"distribution"`
	Content                   Content      `json:"content"`
	LifecycleState            string       `json:"lifecycleState"`
	IsReshareDisabledByAuthor bool         `json:"isReshareDisabledByAuthor"`
}

type Distribution struct {
	FeedDistribution               string   `json:"feedDistribution"`
	TargetEntities                 []string `json:"targetEntities"`
	ThirdPartyDistributionChannels []string `json:"thirdPartyDistributionChannels"`
}

type Content struct {
	Article Article `json:"article"`
}

// Article is the card. Its thumbnail would have to be an image uploaded
// to LinkedIn first (Images API), which needs the image bytes; without it
// the card shows the title and the description.
type Article struct {
	Source      string `json:"source"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
}

// ShareFor builds the share of a post.
func ShareFor(c Config, author string, p pluginsdk.Post, text string) Share {
	return Share{
		Author: author, Commentary: text, Visibility: c.Visibility,
		Distribution: Distribution{FeedDistribution: "MAIN_FEED", TargetEntities: []string{}, ThirdPartyDistributionChannels: []string{}},
		Content: Content{Article: Article{
			Source: p.URL,
			// The limits are 400 and 4086 characters.
			Title:       autopost.Truncate(p.Title, 400),
			Description: autopost.Truncate(p.Excerpt, 4000),
		}},
		LifecycleState: "PUBLISHED",
	}
}

// ShareRequest builds the request.
func ShareRequest(c Config, token string, s Share) pluginsdk.HTTPRequest {
	body, _ := json.Marshal(s)
	return pluginsdk.HTTPRequest{Method: "POST", URL: postsURL, Body: string(body), Headers: map[string]string{
		"Authorization":             "Bearer " + token,
		"LinkedIn-Version":          c.Version,
		"X-Restli-Protocol-Version": "2.0.0",
		"Content-Type":              "application/json",
		"Accept":                    "application/json",
	}}
}

// share publishes the post on the member's profile.
func share(c Config, conn connection, p pluginsdk.Post, text string) autopost.Result {
	resp, err := pluginsdk.Fetch(ShareRequest(c, conn.token, ShareFor(c, conn.author, p, text)))
	if err == nil && resp != nil && resp.Status == 429 {
		// LinkedIn's limits are per day and reset at midnight UTC.
		n := now().UTC()
		midnight := time.Date(n.Year(), n.Month(), n.Day()+1, 0, 5, 0, 0, time.UTC)
		return autopost.Result{Failed: true, Later: midnight, Detail: "rate limited (HTTP 429" + autopost.ErrorText(resp.Body) + "); LinkedIn's daily limits reset at midnight UTC"}
	}
	if r, bad := autopost.FetchProblem(resp, err, postsURL, ""); bad {
		if resp != nil {
			r.Detail += hint(resp.Status, resp.Body)
		}
		return r
	}
	id := resp.Headers["x-restli-id"]
	if id == "" {
		var created struct {
			ID string `json:"id"`
		}
		json.Unmarshal([]byte(resp.Body), &created)
		id = created.ID
	}
	if !strings.HasPrefix(id, "urn:li:") {
		return autopost.Result{Done: true, URL: "(LinkedIn did not return the post's id)"}
	}
	return autopost.Result{Done: true, URL: "https://www.linkedin.com/feed/update/" + id + "/"}
}

// hint explains the refusals an administrator can fix.
func hint(status int, body string) string {
	var e struct {
		Code string `json:"code"`
	}
	json.Unmarshal([]byte(body), &e)
	switch {
	case status == 401:
		return ". The token was revoked or expired: connect LinkedIn again on the plugin's page"
	case status == 403:
		return ". The app may lack the “Share on LinkedIn” product, or the connection the w_member_social scope: check the app, then connect again"
	case status == 426 || e.Code == "NONEXISTENT_VERSION" || e.Code == "VERSION_MISSING":
		return ". The “LinkedIn API version” setting is not a version LinkedIn supports: use a recent one, such as " + DefaultVersion
	}
	return ""
}
