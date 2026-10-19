// The linkedin plugin shares new articles on the LinkedIn profile of the
// member who connected it.
//
// pluginsdk/autopost runs the events, the queue, the retries and the
// record of what was posted (a post is never sent twice). The core runs
// the OAuth connection (plugin.json, "oauth"): the plugin only asks for the
// access token, never sees the client secret, and the administrator
// connects and reconnects the account on the plugin's page. linkedin.go
// builds the article share for the Posts API.
//
// Everything except main.go is plain Go, tested natively with sdktest.
package main

import (
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/vicktor/sap13ns/pluginsdk"
	"github.com/vicktor/sap13ns/pluginsdk/autopost"
)

// provider is the id of the OAuth connection in plugin.json.
const provider = "linkedin"

// DefaultVersion is the LinkedIn-Version header when the setting is empty
// (keep plugin.json in sync). LinkedIn supports each monthly version for
// at least a year.
const DefaultVersion = "202610"

var versionRe = regexp.MustCompile(`^20[0-9]{2}(0[1-9]|1[0-2])$`)

// Config holds the plugin settings, normalized.
type Config struct {
	autopost.Config
	Version    string // LinkedIn-Version, YYYYMM
	Visibility string // PUBLIC | CONNECTIONS
}

// ConfigFrom reads the plugin settings.
func ConfigFrom(s map[string]string) Config {
	c := Config{Config: autopost.ConfigFrom(s), Version: strings.TrimSpace(s["linkedin_version"]), Visibility: strings.TrimSpace(s["visibility"])}
	if c.Version == "" {
		c.Version = DefaultVersion
	} else if !versionRe.MatchString(c.Version) {
		c.Warnings = append(c.Warnings, "LinkedIn API version must look like "+DefaultVersion+" (year and month); using "+DefaultVersion)
		c.Version = DefaultVersion
	}
	if c.Visibility != "CONNECTIONS" {
		c.Visibility = "PUBLIC"
	}
	return c
}

// Test hooks.
var (
	now   = time.Now
	sleep = time.Sleep
)

// connection is the account the posts go to, for one call.
type connection struct {
	token  string
	author string // urn:li:person:<id>
	name   string
	why    string // why there is none
}

// connect asks the host for the access token. Only the events that may
// post need it.
func connect(event string) connection {
	if event != pluginsdk.EventPostPublished && event != pluginsdk.EventCronHourly {
		return connection{why: "not needed"}
	}
	t, err := pluginsdk.OAuth(provider)
	switch {
	case errors.Is(err, pluginsdk.ErrNotConnected):
		return connection{why: "LinkedIn is not connected: connect it in Admin → Plugins → LinkedIn → Connections"}
	case errors.Is(err, pluginsdk.ErrOAuthExpired):
		return connection{why: "the LinkedIn connection expired (LinkedIn tokens last 60 days): connect it again in Admin → Plugins → LinkedIn → Connections"}
	case err != nil:
		return connection{why: "the LinkedIn connection is not available (" + err.Error() + ")"}
	case t.AccountID == "":
		return connection{why: "the LinkedIn connection has no member id: connect it again"}
	}
	return connection{token: t.AccessToken, author: "urn:li:person:" + t.AccountID, name: t.AccountName}
}

func handle(req *pluginsdk.Request) (any, error) {
	c := ConfigFrom(req.Plugin.Settings)
	conn := connect(req.Event)
	who := conn.name
	if who == "" {
		who = conn.author
	}
	if who == "" {
		who = "(not connected)"
	}
	p := &autopost.Poster{
		Config:          c.Config,
		NotConfigured:   conn.why,
		DefaultTemplate: defaultTemplate,
		Now:             func() time.Time { return now() },
		Sleep:           func(d time.Duration) { sleep(d) },
		Networks: []autopost.Network{{
			Key: "l", Name: "LinkedIn", Cost: 1,
			Ready: conn.token != "",
			// Text fits the plain text, then turns it into little text;
			// Limit only measures that result for the dry run.
			Limit:  autopost.Limit{Max: commentaryLimit.Max, Len: autopost.Runes},
			Text:   func(tpl string, f autopost.Fields) string { return Little(autopost.Compose(tpl, f, commentaryLimit)) },
			Target: "POST " + postsURL + " as " + who + ", LinkedIn-Version " + c.Version + ", visibility " + c.Visibility,
			Send: func(p pluginsdk.Post, text string) autopost.Result {
				return share(c, conn, p, text)
			},
		}},
	}
	return p.Handle(req)
}

// defaultTemplate leaves the link out: the article card under the text
// carries it.
func defaultTemplate(lang, postType string) string {
	switch {
	case autopost.IsSpanish(lang) && postType == "page":
		return "Nueva página: {title}\n\n{excerpt}\n\n{hashtags}"
	case autopost.IsSpanish(lang):
		return "Nueva entrada: {title}\n\n{excerpt}\n\n{hashtags}"
	case postType == "page":
		return "New page: {title}\n\n{excerpt}\n\n{hashtags}"
	}
	return "New post: {title}\n\n{excerpt}\n\n{hashtags}"
}
