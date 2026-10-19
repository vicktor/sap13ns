// The instagram plugin publishes new articles to the Instagram
// professional account that connected it, and shows that account's latest
// posts as a carousel or a grid.
//
// The core runs the OAuth connection (plugin.json, "oauth"): Instagram's
// short-lived token is traded for a 60-day one when the account connects,
// and the core renews it before it ends. The plugin only asks for the
// access token, and never sees the app secret.
//
//   - Publishing (instagram.go) runs on pluginsdk/autopost: the events, the
//     queue, the retries and the record of what was published (a post is
//     never sent twice). Each post is an image container with the featured
//     image and the caption, then media_publish.
//   - The feed (feed.go) is fetched in the background, on cron.hourly and
//     when the account connects, and stored. Shortcodes and widgets
//     (render.go) render it from storage, without network access.
//
// Everything except main.go is plain Go, tested natively with sdktest.
package main

import (
	"errors"
	"strings"
	"time"
	_ "time/tzdata" // the sandbox has no zoneinfo files

	"github.com/vicktor/sap13ns/pluginsdk"
	"github.com/vicktor/sap13ns/pluginsdk/autopost"
)

// provider is the id of the OAuth connection in plugin.json.
const provider = "instagram"

// feedMaxAge is how long the host may keep a post's body with a feed
// before it renders the shortcode again: a list fetched in the background
// reaches the post within this time (plus the page cache's two minutes), as
// it reaches a widget, which is rendered with every page.
const feedMaxAge = 5 * time.Minute

// Test hooks.
var (
	now   = time.Now
	sleep = time.Sleep
)

// logged keeps each render warning to one log line per plugin instance;
// instances are recreated whenever the settings change.
var logged = map[string]bool{}

func logOnce(level pluginsdk.Level, msg string) {
	if !logged[msg] {
		logged[msg] = true
		pluginsdk.Log(level, msg)
	}
}

// Config holds the plugin settings, normalized.
type Config struct {
	autopost.Config
	Publish bool
}

// ConfigFrom reads the plugin settings.
func ConfigFrom(s map[string]string) Config {
	return Config{Config: autopost.ConfigFrom(s), Publish: strings.TrimSpace(s["publish"]) != "false"}
}

// connection is the account of one call.
type connection struct {
	token   string
	account string // the Instagram user id (IG_ID)
	name    string // the username
	why     string // why there is none
	gone    bool   // nobody is connected (not merely expired or unavailable)
}

// connect asks the host for the access token.
func connect() connection {
	t, err := pluginsdk.OAuth(provider)
	switch {
	case errors.Is(err, pluginsdk.ErrNotConnected):
		return connection{gone: true, why: "Instagram is not connected: connect it in Admin → Plugins → Instagram → Connections"}
	case errors.Is(err, pluginsdk.ErrOAuthExpired):
		return connection{why: "the Instagram connection ended: connect it again in Admin → Plugins → Instagram → Connections"}
	case err != nil:
		return connection{why: "the Instagram connection is not available (" + err.Error() + ")"}
	case t.AccountID == "":
		return connection{why: "the Instagram connection has no account id: connect it again"}
	}
	return connection{token: t.AccessToken, account: t.AccountID, name: t.AccountName}
}

// handle answers the hooks of plugin.json. Expected trouble (nothing
// connected, storage denied in the scanner's dry run) is logged, never
// returned as an error, so a page always renders.
func handle(req *pluginsdk.Request) (any, error) {
	switch req.Hook {
	case pluginsdk.HookShortcode:
		var sc pluginsdk.Shortcode
		if err := req.Decode(&sc); err != nil {
			return nil, nil
		}
		lang := req.Site.Language
		if sc.Post != nil && sc.Post.Lang != "" {
			lang = sc.Post.Lang
		}
		a := sc.Attrs
		return pluginsdk.HTMLMaxAge(render(req, a["count"], a["layout"], a["title"], lang, false, "[instagram_feed]"), feedMaxAge), nil
	case pluginsdk.HookWidget:
		var w pluginsdk.Widget
		if err := req.Decode(&w); err != nil {
			return nil, nil
		}
		lang := req.Site.Language
		if w.Page != nil && w.Page.Lang != "" {
			lang = w.Page.Lang
		}
		return pluginsdk.HTML(render(req, w.Config["count"], w.Config["layout"], "", lang, true, "the widget")), nil
	case pluginsdk.HookEvent:
		return onEvent(req)
	}
	return nil, nil
}

// onEvent refreshes the feed (cron.hourly, oauth.connected) and runs the
// publishing.
func onEvent(req *pluginsdk.Request) (any, error) {
	c := ConfigFrom(req.Plugin.Settings)
	var conn connection
	switch req.Event {
	case pluginsdk.EventOAuthConnected:
		RefreshFeed(connect(), true)
		return nil, nil
	case pluginsdk.EventCronHourly:
		conn = connect()
	case pluginsdk.EventPostPublished:
		if c.Publish {
			conn = connect()
		}
	}
	used := 0
	if req.Event == pluginsdk.EventCronHourly {
		used = RefreshFeed(conn, false)
	}
	if !c.Publish {
		return nil, nil
	}
	who := "@" + conn.name
	if conn.name == "" {
		who = conn.account
	}
	p := &autopost.Poster{
		Config:          c.Config,
		NotConfigured:   conn.why,
		DefaultTemplate: defaultTemplate,
		HTTPUsed:        used,
		Now:             func() time.Time { return now() },
		Sleep:           func(d time.Duration) { sleep(d) },
		Networks: []autopost.Network{{
			Key: "i", Name: "Instagram", Cost: publishCost,
			Ready:  conn.token != "",
			Limit:  captionLimit,
			Target: "POST " + graphBase + "<IG_ID>/media, then media_publish, as " + who,
			Skip:   skipPost,
			Send: func(p pluginsdk.Post, caption string) autopost.Result {
				return publish(conn, p, caption)
			},
		}},
	}
	if p.NotConfigured == "" {
		p.NotConfigured = "Instagram is not connected"
	}
	return p.Handle(req)
}

// defaultTemplate keeps the link out of the caption, where Instagram does
// not make it clickable: "Link in bio." points readers to the profile.
func defaultTemplate(lang, postType string) string {
	switch {
	case autopost.IsSpanish(lang) && postType == "page":
		return "Nueva página: {title}\n\n{excerpt}\n\nEnlace en la bio.\n\n{hashtags}"
	case autopost.IsSpanish(lang):
		return "Nueva entrada: {title}\n\n{excerpt}\n\nEnlace en la bio.\n\n{hashtags}"
	case postType == "page":
		return "New page: {title}\n\n{excerpt}\n\nLink in bio.\n\n{hashtags}"
	}
	return "New post: {title}\n\n{excerpt}\n\nLink in bio.\n\n{hashtags}"
}
