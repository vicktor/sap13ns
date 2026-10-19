// Package autopost announces a site's new articles on social networks from
// a sap13ns plugin. It holds what the official auto-post plugins share, so
// that each one only describes its network:
//
//   - a templated text in the post's language, with hashtags made from its
//     tags, fitted to the network's length limit (Compose);
//   - posts first published longer ago than a setting are never announced;
//   - a post is never sent twice to a network: before a request leaves, the
//     post is recorded as being sent, and an answer that never came is
//     never retried;
//   - failed sends are retried on the next hourly runs, three attempts in
//     all, and a network that asks to wait (a rate limit) is tried again
//     after the time it gave;
//   - scheduled posts are queued and sent once they are public.
//
// A plugin declares the event hook with post.published, post.updated,
// post.trashed and cron.hourly, the kv and posts:read permissions and the
// shared settings (see Config), then builds a Poster for every call:
//
//	func handle(req *pluginsdk.Request) (any, error) {
//		p := autopost.Poster{Config: autopost.ConfigFrom(req.Plugin.Settings), Networks: []autopost.Network{…}}
//		return p.Handle(req)
//	}
//
// Everything is written to the plugin's activity log. The package runs in
// the plugin (wasip1) and natively under pluginsdk/sdktest.
package autopost

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
	HTTPPerCall    = 4
	QueriesPerCall = 10
)

// Network is one place posts are sent to.
type Network struct {
	// Key names the network in the records ("b", "x"): short, stable, never
	// "t".
	Key string
	// Name is shown in the log ("Bluesky").
	Name string
	// Cost is the number of HTTP requests one delivery needs.
	Cost int
	// Ready reports whether the network is configured (and connected).
	Ready bool
	// Limit is the text limit. Text, when set, builds the text instead of
	// Compose(tpl, fields, Limit).
	Limit Limit
	Text  func(tpl string, f Fields) string
	// Target describes the request in a dry run ("POST https://… as alice").
	Target string
	// Skip returns why a post cannot go to this network ("it has no
	// featured image"), or "". A skipped post is not recorded, so it is
	// considered again if it is published again.
	Skip func(p pluginsdk.Post) string
	// Send publishes the text. It is called only for public posts, after
	// the post is recorded as being sent.
	Send func(p pluginsdk.Post, text string) Result
}

// Result is the outcome of one delivery.
type Result struct {
	// Done: posted, at URL. Failed: the network refused, or the request
	// never left, so it is safe to retry. Neither: whether it was posted is
	// unknown, and it is never retried.
	Done, Failed bool
	URL          string
	Link         string // a web address for people, when URL is not one
	Detail       string // why it failed, for the log (never secrets)
	// Later, with Failed, means the network asked to wait (a rate limit):
	// the post is tried again on the first hourly run after Later, and
	// this attempt does not count.
	Later time.Time
}

// Poster runs the auto-post events of one call.
type Poster struct {
	Config   Config
	Networks []Network
	// NotConfigured ends "not posted: " when no network is ready
	// ("neither Bluesky nor Mastodon is configured").
	NotConfigured string
	// DefaultTemplate is the text when no template setting applies (nil:
	// DefaultTemplate).
	DefaultTemplate func(lang, postType string) string
	// HTTPUsed is the number of requests the plugin already made in this
	// call, before Handle.
	HTTPUsed int

	// Clock and waits, for tests (nil: time.Now and time.Sleep).
	Now   func() time.Time
	Sleep func(time.Duration)
}

func (p *Poster) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *Poster) sleep(d time.Duration) {
	if p.Sleep != nil {
		p.Sleep(d)
		return
	}
	time.Sleep(d)
}

// Handle answers post.published, post.updated, post.trashed and
// cron.hourly, and ignores everything else. It never returns an error for
// expected situations (nothing configured, storage denied in the scanner's
// dry run): everything is explained in the activity log.
func (p *Poster) Handle(req *pluginsdk.Request) (any, error) {
	if req.Hook != pluginsdk.HookEvent {
		return nil, nil
	}
	switch req.Event {
	case pluginsdk.EventCronHourly, pluginsdk.EventPostPublished, pluginsdk.EventPostUpdated, pluginsdk.EventPostTrashed:
	default:
		return nil, nil
	}
	r := &run{p: p, req: req, cfg: p.Config, httpLeft: HTTPPerCall - p.HTTPUsed, queriesLeft: QueriesPerCall,
		newest: map[string][]pluginsdk.Post{}}
	for _, w := range r.cfg.Warnings {
		pluginsdk.Log(pluginsdk.Warn, w)
	}
	if req.Event == pluginsdk.EventCronHourly {
		r.cron()
		return nil, nil
	}
	var post pluginsdk.Post
	if err := req.Decode(&post); err != nil {
		pluginsdk.Log(pluginsdk.Warn, req.Event+": unreadable payload: "+err.Error())
		return nil, nil
	}
	switch req.Event {
	case pluginsdk.EventPostPublished:
		r.published(post)
	case pluginsdk.EventPostUpdated:
		r.updated(post)
	case pluginsdk.EventPostTrashed:
		r.trashed(post)
	}
	return nil, nil
}

// run is one event call: the settings and what is left of the host's
// per-call budgets.
type run struct {
	p           *Poster
	req         *pluginsdk.Request
	cfg         Config
	httpLeft    int
	queriesLeft int
	newest      map[string][]pluginsdk.Post // newest public posts by type and language
}

func (r *run) now() time.Time { return r.p.now() }

func (r *run) logf(level pluginsdk.Level, p pluginsdk.Post, format string, args ...any) {
	pluginsdk.Log(level, Label(p)+": "+fmt.Sprintf(format, args...))
}

// Label names a post in the log: post 12 “Title”.
func Label(p pluginsdk.Post) string {
	kind := "post"
	if p.Type == "page" {
		kind = "page"
	}
	return kind + " " + strconv.FormatInt(p.ID, 10) + " “" + Truncate(p.Title, 60) + "”"
}

// ready lists the configured networks.
func (r *run) ready() []Network {
	var out []Network
	for _, n := range r.p.Networks {
		if n.Ready {
			out = append(out, n)
		}
	}
	return out
}

func publishedAt(p pluginsdk.Post) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, p.PublishedAt)
	return t, err == nil
}

// When formats a time for the log.
func When(t time.Time) string { return t.UTC().Format("2006-01-02 15:04 UTC") }

func (r *run) notConfigured() string {
	if r.p.NotConfigured != "" {
		return r.p.NotConfigured
	}
	return "no network is configured"
}

func (r *run) published(p pluginsdk.Post) {
	if !r.cfg.Wants(p.Type) {
		r.logf(pluginsdk.Info, p, "not posted: the plugin posts %s (setting “Post”)", r.cfg.PostTypes)
		return
	}
	if len(r.ready()) == 0 && !r.cfg.DryRun {
		r.logf(pluginsdk.Info, p, "not posted: %s", r.notConfigured())
		return
	}
	at, ok := publishedAt(p)
	if !ok {
		at = r.now()
	}
	if at.After(r.now().Add(time.Minute)) {
		if r.cfg.DryRun {
			r.logf(pluginsdk.Info, p, "scheduled for %s; dry run, so it is not queued", When(at))
			return
		}
		r.queue(p, at, fmt.Sprintf("scheduled for %s; it will be posted within an hour after it goes live", When(at)))
		return
	}
	if age := r.now().Sub(at); age > r.cfg.SkipOlder {
		r.logf(pluginsdk.Info, p, "not posted: it was first published on %s, more than %s ago (setting “Skip posts older than”)",
			When(at), hours(r.cfg.SkipOlder))
		return
	}
	recs, err := LoadRecords()
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
	nets := r.ready()
	if len(nets) == 0 {
		return ""
	}
	var names []string
	for _, n := range nets {
		if st, _ := rec.Get(n.Key); st != StateDone {
			return ""
		}
		names = append(names, n.Name)
	}
	return strings.Join(names, " and ")
}

func (r *run) queue(p pluginsdk.Post, at time.Time, why string) {
	it := Item{ID: p.ID, Type: p.Type, Lang: p.Lang, Title: Truncate(p.Title, 120), At: at.Unix(), Since: r.now().Unix()}
	var dropped *Item
	if err := r.p.update(func(_ map[string]Record, q *[]Item) { dropped = enqueue(q, it) }); err != nil {
		r.logf(pluginsdk.Warn, p, "could not be queued (%v); it will not be posted", err)
		return
	}
	r.logf(pluginsdk.Info, p, "%s", why)
	if dropped != nil {
		pluginsdk.Log(pluginsdk.Warn, fmt.Sprintf("the queue is full (%d posts): post %d “%s” was dropped and will not be posted", MaxQueue, dropped.ID, dropped.Title))
	}
}

func (r *run) updated(p pluginsdk.Post) {
	q, err := LoadQueue()
	if err != nil || FindItem(q, p.ID) == nil {
		return // nothing queued for this post (or no storage): updates are never posted
	}
	at, ok := publishedAt(p)
	now := r.now()
	err = r.p.update(func(_ map[string]Record, q *[]Item) {
		if it := FindItem(*q, p.ID); it != nil {
			it.Title, it.Type, it.Lang = Truncate(p.Title, 120), p.Type, p.Lang
			// A new schedule moves the item; a retry that is due stays due.
			if ok && (at.After(now) || it.At > now.Unix()) {
				it.At = at.Unix()
			}
		}
	})
	if err != nil {
		r.logf(pluginsdk.Warn, p, "the queued post could not be updated (%v)", err)
		return
	}
	if ok && at.After(now) {
		r.logf(pluginsdk.Info, p, "queued post updated: now due %s", When(at))
	}
}

func (r *run) trashed(p pluginsdk.Post) {
	q, err := LoadQueue()
	if err != nil || FindItem(q, p.ID) == nil {
		return
	}
	removed := false
	if err := r.p.update(func(_ map[string]Record, q *[]Item) { removed = dequeue(q, p.ID) }); err != nil {
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
	q, err := LoadQueue()
	if err != nil {
		pluginsdk.Log(pluginsdk.Warn, "hourly run: the plugin's storage is not available ("+err.Error()+")")
		return
	}
	now := r.now()
	var due []Item
	for _, it := range q {
		if it.At <= now.Unix() {
			due = append(due, it)
		}
	}
	if len(due) > 0 && len(r.ready()) == 0 && !r.cfg.DryRun {
		// Kept: they go out once the network is configured again, unless
		// they are too old by then.
		pluginsdk.Log(pluginsdk.Warn, fmt.Sprintf("hourly run: %d queued post(s) wait: %s", len(due), r.notConfigured()))
		return
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
				len(due)-i, HTTPPerCall, QueriesPerCall))
			break
		}
		switch at, ok := publishedAt(p); {
		case vis == hidden:
			if now.Sub(time.Unix(it.At, 0)) > QueueGrace {
				drops = append(drops, drop{stub, "dropped from the queue: still not public 48 hours after " + When(time.Unix(it.At, 0))})
			}
		case ok && now.Sub(at) > r.cfg.SkipOlder:
			drops = append(drops, drop{p, fmt.Sprintf("dropped from the queue: first published on %s, more than %s ago", When(at), hours(r.cfg.SkipOlder))})
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
	err = r.p.update(func(_ map[string]Record, q *[]Item) {
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
	net      Network
	attempts int
	res      Result
}

// texts builds the text of every network.
func (r *run) texts(p pluginsdk.Post) map[string]string {
	lang := p.Lang
	if lang == "" {
		lang = r.req.Site.Language
	}
	fields := Fields{Title: p.Title, URL: p.URL, Excerpt: p.Excerpt, Site: r.req.Site.Title, Hashtags: Hashtags(p.Tags, r.cfg.MaxHashtags)}
	tpl := r.cfg.TemplateFor(lang, p.Type, r.p.DefaultTemplate)
	out := map[string]string{}
	for _, n := range r.p.Networks {
		if n.Text != nil {
			out[n.Key] = n.Text(tpl, fields)
		} else {
			out[n.Key] = Compose(tpl, fields, n.Limit)
		}
	}
	return out
}

func skipReason(n Network, p pluginsdk.Post) string {
	if n.Skip == nil {
		return ""
	}
	return n.Skip(p)
}

// deliver sends a public post to every configured network it has not
// reached yet. The record is marked "sending" before any request leaves,
// so that a crash or a timeout can never lead to a second copy.
func (r *run) deliver(p pluginsdk.Post) {
	text := r.texts(p)
	if r.cfg.DryRun {
		r.dryRun(p, text)
		return
	}

	var todo []outcome
	deferred := false
	err := r.p.update(func(recs map[string]Record, q *[]Item) {
		rec := recs[key(p.ID)]
		for _, n := range r.ready() {
			st, attempts := rec.Get(n.Key)
			switch {
			case st == StateDone:
			case st == StateSending:
				r.logf(pluginsdk.Warn, p, "%s: an earlier attempt got no answer, so whether it was posted is unknown; it is not retried, so that it cannot appear twice. Check the account", n.Name)
			case st == StateFailed && attempts >= MaxAttempts:
			case skipReason(n, p) != "":
				r.logf(pluginsdk.Info, p, "%s: not posted: %s", n.Name, skipReason(n, p))
			case r.httpLeft < n.Cost:
				deferred = true
				r.logf(pluginsdk.Info, p, "%s: this run used its %d requests; it will be posted on the next hourly run", n.Name, HTTPPerCall)
			default:
				r.httpLeft -= n.Cost
				rec.Set(n.Key, StateSending, attempts+1)
				todo = append(todo, outcome{net: n, attempts: attempts + 1})
			}
		}
		rec.T = r.now().Unix()
		recs[key(p.ID)] = rec
		switch {
		case len(todo) == 0 && deferred:
			r.requeue(q, p, time.Time{}) // nothing sent: make sure it waits in the queue
		case len(todo) == 0:
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
		todo[i].res = todo[i].net.Send(p, text[todo[i].net.Key])
	}

	retry := deferred
	var later time.Time
	err = r.p.update(func(recs map[string]Record, q *[]Item) {
		rec := recs[key(p.ID)]
		for _, o := range todo {
			switch {
			case o.res.Done:
				rec.Set(o.net.Key, StateDone, o.attempts)
			case o.res.Failed && !o.res.Later.IsZero():
				rec.Set(o.net.Key, StateFailed, o.attempts-1)
				retry = true
				if o.res.Later.After(later) {
					later = o.res.Later
				}
			case o.res.Failed:
				rec.Set(o.net.Key, StateFailed, o.attempts)
				retry = retry || o.attempts < MaxAttempts
			}
		}
		rec.T = r.now().Unix()
		recs[key(p.ID)] = rec
		if retry {
			r.requeue(q, p, later)
		} else {
			dequeue(q, p.ID)
		}
	})
	for _, o := range todo {
		name := o.net.Name
		switch {
		case o.res.Done && o.res.Link != "":
			r.logf(pluginsdk.Info, p, "posted to %s: %s (%s)", name, o.res.URL, o.res.Link)
		case o.res.Done:
			r.logf(pluginsdk.Info, p, "posted to %s: %s", name, o.res.URL)
		case o.res.Failed && !o.res.Later.IsZero():
			r.logf(pluginsdk.Warn, p, "%s asked to wait: %s; it will be tried again on the first hourly run after %s (this does not count as an attempt)", name, o.res.Detail, When(o.res.Later))
		case o.res.Failed && o.attempts < MaxAttempts:
			r.logf(pluginsdk.Warn, p, "%s failed (attempt %d of %d): %s; it will be retried on the next hourly run", name, o.attempts, MaxAttempts, o.res.Detail)
		case o.res.Failed:
			r.logf(pluginsdk.Warn, p, "%s failed (attempt %d of %d): %s; giving up", name, o.attempts, MaxAttempts, o.res.Detail)
		default:
			r.logf(pluginsdk.Warn, p, "%s: %s; whether it was posted is unknown, so it is not retried. Check the account", name, o.res.Detail)
		}
	}
	if err != nil {
		r.logf(pluginsdk.Error, p, "the result could not be recorded (%v); the post stays marked as being sent and is not retried", err)
	}
}

// requeue keeps a post in the queue for the next hourly run, or for the
// first one after later.
func (r *run) requeue(q *[]Item, p pluginsdk.Post, later time.Time) {
	at := r.now().Unix()
	if it := FindItem(*q, p.ID); it != nil {
		at = min(it.At, at)
	}
	if !later.IsZero() && later.Unix() > at {
		at = later.Unix()
	}
	enqueue(q, Item{ID: p.ID, Type: p.Type, Lang: p.Lang, Title: Truncate(p.Title, 120), At: at, Since: r.now().Unix()})
}

// dryRun writes what would be sent, without sending or recording it.
func (r *run) dryRun(p pluginsdk.Post, text map[string]string) {
	nets := r.ready()
	if len(nets) == 0 {
		nets = r.p.Networks
	}
	for _, n := range nets {
		if why := skipReason(n, p); why != "" {
			r.logf(pluginsdk.Info, p, "dry run, %s: not posted: %s", n.Name, why)
			continue
		}
		size := Runes(text[n.Key])
		if n.Limit.Len != nil {
			size = n.Limit.Len(text[n.Key])
		}
		r.logf(pluginsdk.Info, p, "dry run, %s (%s, %d characters): %q", n.Name, n.Target, size, text[n.Key])
	}
}

func key(id int64) string { return strconv.FormatInt(id, 10) }

func hours(d time.Duration) string {
	h := d.Hours()
	if h == float64(int(h)) {
		return strconv.Itoa(int(h)) + " hours"
	}
	return strconv.FormatFloat(h, 'f', 1, 64) + " hours"
}
