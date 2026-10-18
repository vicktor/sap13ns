package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/vicktor/sap13ns/pluginsdk"
)

// Host limits per call (docs/PLUGINS.md, Limits).
const (
	httpPerCall    = 4
	queriesPerCall = 10
)

const (
	netBluesky  = "bluesky"
	netMastodon = "mastodon"
)

var netNames = map[string]string{netBluesky: "Bluesky", netMastodon: "Mastodon"}

// cost is the number of HTTP requests one delivery needs.
var cost = map[string]int{netBluesky: 2, netMastodon: 1} // Bluesky: sign in, then post

// handle answers the events declared in plugin.json. It never returns an
// error for expected situations (nothing configured, storage denied in the
// scanner's dry run): everything is explained in the activity log.
func handle(req *pluginsdk.Request) (any, error) {
	if req.Hook != pluginsdk.HookEvent {
		return nil, nil
	}
	r := &run{req: req, cfg: ConfigFrom(req.Plugin.Settings), httpLeft: httpPerCall, queriesLeft: queriesPerCall,
		newest: map[string][]pluginsdk.Post{}}
	for _, w := range r.cfg.Warnings {
		pluginsdk.Log(pluginsdk.Warn, w)
	}
	if req.Event == pluginsdk.EventCronHourly {
		r.cron()
		return nil, nil
	}
	var p pluginsdk.Post
	if err := req.Decode(&p); err != nil {
		pluginsdk.Log(pluginsdk.Warn, req.Event+": unreadable payload: "+err.Error())
		return nil, nil
	}
	switch req.Event {
	case pluginsdk.EventPostPublished:
		r.published(p)
	case pluginsdk.EventPostUpdated:
		r.updated(p)
	case pluginsdk.EventPostTrashed:
		r.trashed(p)
	}
	return nil, nil
}

// run is one event call: the settings and what is left of the host's
// per-call budgets.
type run struct {
	req         *pluginsdk.Request
	cfg         Config
	httpLeft    int
	queriesLeft int
	newest      map[string][]pluginsdk.Post // newest public posts by type and language
}

func (r *run) logf(level pluginsdk.Level, p pluginsdk.Post, format string, args ...any) {
	pluginsdk.Log(level, label(p)+": "+fmt.Sprintf(format, args...))
}

func label(p pluginsdk.Post) string {
	kind := "post"
	if p.Type == "page" {
		kind = "page"
	}
	return kind + " " + strconv.FormatInt(p.ID, 10) + " “" + Truncate(p.Title, 60) + "”"
}

// networks lists the configured networks.
func (r *run) networks() []string {
	var out []string
	if r.cfg.Bluesky() {
		out = append(out, netBluesky)
	}
	if r.cfg.Mastodon() {
		out = append(out, netMastodon)
	}
	return out
}

func publishedAt(p pluginsdk.Post) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, p.PublishedAt)
	return t, err == nil
}

func when(t time.Time) string { return t.UTC().Format("2006-01-02 15:04 UTC") }

func (r *run) published(p pluginsdk.Post) {
	if !r.cfg.Wants(p.Type) {
		r.logf(pluginsdk.Info, p, "not posted: the plugin posts %s (setting “Post”)", r.cfg.PostTypes)
		return
	}
	if len(r.networks()) == 0 && !r.cfg.DryRun {
		r.logf(pluginsdk.Info, p, "not posted: neither Bluesky nor Mastodon is configured")
		return
	}
	at, ok := publishedAt(p)
	if !ok {
		at = now()
	}
	if at.After(now().Add(time.Minute)) {
		if r.cfg.DryRun {
			r.logf(pluginsdk.Info, p, "scheduled for %s; dry run, so it is not queued", when(at))
			return
		}
		r.queue(p, at, fmt.Sprintf("scheduled for %s; it will be posted within an hour after it goes live", when(at)))
		return
	}
	if age := now().Sub(at); age > r.cfg.SkipOlder {
		r.logf(pluginsdk.Info, p, "not posted: it was first published on %s, more than %s ago (setting “Skip posts older than”)",
			when(at), hours(r.cfg.SkipOlder))
		return
	}
	recs, err := loadRecords()
	if err != nil {
		r.logf(pluginsdk.Warn, p, "not posted: the plugin's storage is not available (%v)", err)
		return
	}
	if done := r.allDone(recs[key(p.ID)]); done != "" {
		r.logf(pluginsdk.Info, p, "already posted to %s; it is never posted twice", done)
		return
	}
	fresh, vis := r.visible(p)
	if vis != public {
		if r.cfg.DryRun {
			r.logf(pluginsdk.Info, p, "not publicly visible; dry run, so nothing would be posted")
			return
		}
		r.queue(p, at, "not publicly visible yet; it is checked again every hour for 48 hours")
		return
	}
	r.deliver(fresh)
}

// allDone names the configured networks when the post was sent to all of
// them, or returns "".
func (r *run) allDone(rec Record) string {
	nets := r.networks()
	if len(nets) == 0 {
		return ""
	}
	var names []string
	for _, n := range nets {
		if st, _ := rec.Get(n); st != stateDone {
			return ""
		}
		names = append(names, netNames[n])
	}
	return strings.Join(names, " and ")
}

func (r *run) queue(p pluginsdk.Post, at time.Time, why string) {
	it := Item{ID: p.ID, Type: p.Type, Lang: p.Lang, Title: Truncate(p.Title, 120), At: at.Unix(), Since: now().Unix()}
	var dropped *Item
	if err := update(func(_ map[string]Record, q *[]Item) { dropped = enqueue(q, it) }); err != nil {
		r.logf(pluginsdk.Warn, p, "could not be queued (%v); it will not be posted", err)
		return
	}
	r.logf(pluginsdk.Info, p, "%s", why)
	if dropped != nil {
		pluginsdk.Log(pluginsdk.Warn, fmt.Sprintf("the queue is full (%d posts): post %d “%s” was dropped and will not be posted", maxQueue, dropped.ID, dropped.Title))
	}
}

func (r *run) updated(p pluginsdk.Post) {
	q, err := loadQueue()
	if err != nil || findItem(q, p.ID) == nil {
		return // nothing queued for this post (or no storage): updates are never posted
	}
	at, ok := publishedAt(p)
	err = update(func(_ map[string]Record, q *[]Item) {
		if it := findItem(*q, p.ID); it != nil {
			it.Title, it.Type, it.Lang = Truncate(p.Title, 120), p.Type, p.Lang
			// A new schedule moves the item; a retry that is due stays due.
			if ok && (at.After(now()) || it.At > now().Unix()) {
				it.At = at.Unix()
			}
		}
	})
	if err != nil {
		r.logf(pluginsdk.Warn, p, "the queued post could not be updated (%v)", err)
		return
	}
	if ok && at.After(now()) {
		r.logf(pluginsdk.Info, p, "queued post updated: now due %s", when(at))
	}
}

func (r *run) trashed(p pluginsdk.Post) {
	q, err := loadQueue()
	if err != nil || findItem(q, p.ID) == nil {
		return
	}
	removed := false
	if err := update(func(_ map[string]Record, q *[]Item) { removed = dequeue(q, p.ID) }); err != nil {
		r.logf(pluginsdk.Warn, p, "could not be removed from the queue (%v)", err)
		return
	}
	if removed {
		r.logf(pluginsdk.Info, p, "moved to the trash: removed from the queue, it will not be posted")
	}
}

// cron sends the queued posts that are due: scheduled posts that went
// live, and retries. Posts that never became public are dropped after 48
// hours.
func (r *run) cron() {
	q, err := loadQueue()
	if err != nil {
		pluginsdk.Log(pluginsdk.Warn, "hourly run: the plugin's storage is not available ("+err.Error()+")")
		return
	}
	var due []Item
	for _, it := range q {
		if it.At <= now().Unix() {
			due = append(due, it)
		}
	}
	sort.SliceStable(due, func(i, j int) bool { return due[i].At < due[j].At })
	type drop struct {
		p   pluginsdk.Post
		why string
	}
	var drops []drop
	for i, it := range due {
		stub := pluginsdk.Post{ID: it.ID, Type: it.Type, Lang: it.Lang, Title: it.Title}
		p, vis := pluginsdk.Post{}, unchecked
		if r.httpLeft > 0 {
			p, vis = r.visible(stub)
		}
		if vis == unchecked {
			pluginsdk.Log(pluginsdk.Info, fmt.Sprintf("hourly run: %d queued post(s) wait for the next hourly run (the host allows %d requests and %d post queries per run)",
				len(due)-i, httpPerCall, queriesPerCall))
			break
		}
		switch at, ok := publishedAt(p); {
		case vis == hidden:
			if now().Sub(time.Unix(it.At, 0)) > queueGrace {
				drops = append(drops, drop{stub, "dropped from the queue: still not public 48 hours after " + when(time.Unix(it.At, 0))})
			}
		case ok && now().Sub(at) > r.cfg.SkipOlder:
			drops = append(drops, drop{p, fmt.Sprintf("dropped from the queue: first published on %s, more than %s ago", when(at), hours(r.cfg.SkipOlder))})
		case !r.cfg.Wants(p.Type):
			drops = append(drops, drop{p, "dropped from the queue: the plugin no longer posts " + p.Type + "s"})
		default:
			r.deliver(p)
		}
	}
	if len(drops) == 0 {
		return
	}
	if r.cfg.DryRun {
		for _, d := range drops {
			r.logf(pluginsdk.Info, d.p, "dry run, left in the queue; it would be %s", d.why)
		}
		return
	}
	err = update(func(_ map[string]Record, q *[]Item) {
		for _, d := range drops {
			dequeue(q, d.p.ID)
		}
	})
	for _, d := range drops {
		if err != nil {
			r.logf(pluginsdk.Warn, d.p, "could not be removed from the queue (%v)", err)
			continue
		}
		r.logf(pluginsdk.Info, d.p, "%s", d.why)
	}
}

// visibility is what posts_query says about a post.
type visibility int

const (
	unchecked visibility = iota // the query budget ran out or the query failed
	hidden                      // not among the public posts
	public
)

// visible looks for the post among the public posts (posts_query never
// returns drafts, private, scheduled or trashed content) and returns its
// current version.
func (r *run) visible(p pluginsdk.Post) (pluginsdk.Post, visibility) {
	typ := "post"
	if p.Type == "page" {
		typ = "page"
	}
	k := typ + "|" + p.Lang
	list, cached := r.newest[k]
	if !cached {
		if r.queriesLeft < 1 {
			return p, unchecked
		}
		r.queriesLeft--
		var err error
		if list, err = pluginsdk.QueryPosts(pluginsdk.PostsQuery{Type: typ, Lang: p.Lang, Limit: 20}); err != nil {
			r.logf(pluginsdk.Warn, p, "could not check that it is public (%v)", err)
			return p, unchecked
		}
		r.newest[k] = list
	}
	for _, c := range list {
		if c.ID == p.ID {
			return c, public
		}
	}
	// Older than the 20 newest (a scheduled post with a back date, or a
	// busy site): search by title.
	words := searchWords(p.Title)
	if words == "" {
		return p, hidden
	}
	if r.queriesLeft < 1 {
		return p, unchecked
	}
	r.queriesLeft--
	found, err := pluginsdk.QueryPosts(pluginsdk.PostsQuery{Type: typ, Lang: p.Lang, Limit: 20, Search: words})
	if err != nil {
		r.logf(pluginsdk.Warn, p, "could not check that it is public (%v)", err)
		return p, unchecked
	}
	for _, c := range found {
		if c.ID == p.ID {
			return c, public
		}
	}
	return p, hidden
}

// searchWords reduces a title to plain words for a full-text search
// ("or" is an operator there).
func searchWords(title string) string {
	words := strings.FieldsFunc(title, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	var out []string
	for _, w := range words {
		if strings.EqualFold(w, "or") {
			continue
		}
		out = append(out, w)
		if len(out) == 8 {
			break
		}
	}
	return strings.Join(out, " ")
}

// outcome is one network's result within a delivery.
type outcome struct {
	net      string
	attempts int
	res      sendResult
}

// deliver sends a public post to every configured network it has not
// reached yet. The record is marked "sending" before any request leaves,
// so that a crash or a timeout can never lead to a second copy.
func (r *run) deliver(p pluginsdk.Post) {
	lang := p.Lang
	if lang == "" {
		lang = r.req.Site.Language
	}
	fields := Fields{Title: p.Title, URL: p.URL, Excerpt: p.Excerpt, Site: r.req.Site.Title, Hashtags: Hashtags(p.Tags, r.cfg.MaxHashtags)}
	tpl := r.cfg.TemplateFor(lang, p.Type)
	text := map[string]string{
		netBluesky:  Compose(tpl, fields, blueskyNet),
		netMastodon: Compose(tpl, fields, mastodonNet),
	}
	if r.cfg.DryRun {
		r.dryRun(p, text)
		return
	}

	var todo []outcome
	deferred := false
	err := update(func(recs map[string]Record, q *[]Item) {
		rec := recs[key(p.ID)]
		for _, n := range r.networks() {
			st, attempts := rec.Get(n)
			switch {
			case st == stateDone:
			case st == stateSending:
				r.logf(pluginsdk.Warn, p, "%s: an earlier attempt got no answer, so whether it was posted is unknown; it is not retried, so that it cannot appear twice. Check the account", netNames[n])
			case st == stateFailed && attempts >= maxAttempts:
			case r.httpLeft < cost[n]:
				deferred = true
				r.logf(pluginsdk.Info, p, "%s: this run used its %d requests; it will be posted on the next hourly run", netNames[n], httpPerCall)
			default:
				r.httpLeft -= cost[n]
				rec.Set(n, stateSending, attempts+1)
				todo = append(todo, outcome{net: n, attempts: attempts + 1})
			}
		}
		rec.T = now().Unix()
		recs[key(p.ID)] = rec
		if len(todo) == 0 && !deferred {
			dequeue(q, p.ID)
		}
	})
	if err != nil {
		r.logf(pluginsdk.Warn, p, "not posted: the plugin's records could not be updated (%v); it will be tried again on the next hourly run if it is queued", err)
		return
	}
	if len(todo) == 0 {
		return
	}

	for i := range todo {
		switch todo[i].net {
		case netBluesky:
			todo[i].res = postBluesky(r.cfg, text[netBluesky], p, now())
		case netMastodon:
			todo[i].res = postMastodon(r.cfg, r.req.Site.URL, text[netMastodon], p)
		}
	}

	retry := deferred
	err = update(func(recs map[string]Record, q *[]Item) {
		rec := recs[key(p.ID)]
		for _, o := range todo {
			switch {
			case o.res.done:
				rec.Set(o.net, stateDone, o.attempts)
			case o.res.failed:
				rec.Set(o.net, stateFailed, o.attempts)
				retry = retry || o.attempts < maxAttempts
			}
		}
		rec.T = now().Unix()
		recs[key(p.ID)] = rec
		if retry {
			at := now().Unix()
			if it := findItem(*q, p.ID); it != nil {
				at = min(it.At, at)
			}
			enqueue(q, Item{ID: p.ID, Type: p.Type, Lang: p.Lang, Title: Truncate(p.Title, 120), At: at, Since: now().Unix()})
		} else {
			dequeue(q, p.ID)
		}
	})
	for _, o := range todo {
		name := netNames[o.net]
		switch {
		case o.res.done && o.res.link != "":
			r.logf(pluginsdk.Info, p, "posted to %s: %s (%s)", name, o.res.url, o.res.link)
		case o.res.done:
			r.logf(pluginsdk.Info, p, "posted to %s: %s", name, o.res.url)
		case o.res.failed && o.attempts < maxAttempts:
			r.logf(pluginsdk.Warn, p, "%s failed (attempt %d of %d): %s; it will be retried on the next hourly run", name, o.attempts, maxAttempts, o.res.detail)
		case o.res.failed:
			r.logf(pluginsdk.Warn, p, "%s failed (attempt %d of %d): %s; giving up", name, o.attempts, maxAttempts, o.res.detail)
		default:
			r.logf(pluginsdk.Warn, p, "%s: %s; whether it was posted is unknown, so it is not retried. Check the account", name, o.res.detail)
		}
	}
	if err != nil {
		r.logf(pluginsdk.Error, p, "the result could not be recorded (%v); the post stays marked as being sent and is not retried", err)
	}
}

// dryRun writes what would be sent, without sending or recording it.
func (r *run) dryRun(p pluginsdk.Post, text map[string]string) {
	nets := r.networks()
	if len(nets) == 0 {
		nets = []string{netBluesky, netMastodon}
	}
	for _, n := range nets {
		var target string
		switch n {
		case netBluesky:
			who := r.cfg.BlueskyHandle
			if who == "" {
				who = "(no handle configured)"
			}
			target = "POST " + r.cfg.BlueskyService + "/xrpc/com.atproto.repo.createRecord as " + who
		case netMastodon:
			inst := r.cfg.MastodonInstance
			if inst == "" {
				inst = "(no instance configured)"
			}
			target = "POST " + inst + "/api/v1/statuses, visibility " + r.cfg.MastodonVisibility
		}
		r.logf(pluginsdk.Info, p, "dry run, %s (%s, %d characters): %q", netNames[n], target, map[string]Network{netBluesky: blueskyNet, netMastodon: mastodonNet}[n].Len(text[n]), text[n])
	}
}

func key(id int64) string { return strconv.FormatInt(id, 10) }

func itoa(n int) string { return strconv.Itoa(n) }

func hours(d time.Duration) string {
	h := d.Hours()
	if h == float64(int(h)) {
		return strconv.Itoa(int(h)) + " hours"
	}
	return strconv.FormatFloat(h, 'f', 1, 64) + " hours"
}
