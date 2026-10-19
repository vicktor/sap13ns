package network

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"sap13ns/internal/auth"
	"sap13ns/internal/plugin"
	"sap13ns/internal/store"
)

// cronPlugin builds internal/plugin/testdata/hooks, which logs every event
// it gets, subscribed to post.published and cron.hourly. It returns the
// directory to copy into a site's plugins directory.
func cronPlugin(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds a wasm module")
	}
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..")
	dir := filepath.Join(t.TempDir(), "hooks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-buildmode=c-shared", "-trimpath", "-o", filepath.Join(dir, "plugin.wasm"), "./internal/plugin/testdata/hooks")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build the plugin: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(filepath.Join(root, "internal", "plugin", "testdata", "hooks", "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m["events"] = []string{"post.published", "cron.hourly"}
	raw, _ = json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// install copies the plugin into a loaded site and approves it there.
func (e *netEnv) install(s *Site, src string) {
	e.t.Helper()
	app := e.entry(s.ID).app
	dst := filepath.Join(app.Config().PluginsDir, "hooks")
	if err := os.MkdirAll(dst, 0o750); err != nil {
		e.t.Fatal(err)
	}
	for _, f := range []string{"plugin.json", "plugin.wasm"} {
		b, err := os.ReadFile(filepath.Join(src, f))
		if err != nil {
			e.t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, f), b, 0o640); err != nil {
			e.t.Fatal(err)
		}
	}
	uid, err := app.Store().CreateUser(e.ctx, store.NewUser{Username: "boss", Email: "boss@example.test", Role: "admin", PasswordHash: "x"})
	if err != nil {
		e.t.Fatal(err)
	}
	if err := app.Plugins().Load(e.ctx); err != nil {
		e.t.Fatal(err)
	}
	if err := app.Plugins().Approve(e.ctx, "hooks", "", uid); err != nil {
		e.t.Fatal(err)
	}
	if p := app.Plugins().Get("hooks"); p == nil || p.Status != plugin.StatusEnabled {
		e.t.Fatalf("plugin %+v", p)
	}
}

// logged counts the lines the plugin of a loaded site logged with text.
func (e *netEnv) logged(id int64, text string) int {
	ent := e.entry(id)
	if ent == nil {
		return 0
	}
	p := ent.app.Plugins().Get("hooks")
	if p == nil {
		return 0
	}
	n := 0
	for _, l := range p.Logs() {
		if strings.Contains(l.Text, text) {
			n++
		}
	}
	return n
}

func (e *netEnv) unloadAll() {
	e.t.Helper()
	e.clock.add(24 * time.Hour)
	e.reg.Sweep(e.ctx)
	if got := e.reg.Loaded(); !slices.Equal(got, []int64{e.portalID()}) {
		e.t.Fatalf("still loaded: %v", got)
	}
}

func (e *netEnv) row(id int64) *Site {
	e.t.Helper()
	s, err := e.n.Site(e.ctx, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

// TestAnnounceUnloaded: a post scheduled while its site ran, due after the
// site was unloaded, is announced once by a wake-up.
func TestAnnounceUnloaded(t *testing.T) {
	src := cronPlugin(t)
	e := newNetEnv(t, nil)
	a := e.tenant("alpha", "pro")
	if rec := e.do("GET", "https://alpha.sap13ns.site/"); rec.Code != http.StatusOK {
		t.Fatalf("alpha: %d", rec.Code)
	}
	e.install(a, src)

	// Scheduled through the site's hook, as the editor does.
	st := e.entry(a.ID).app.Store()
	author, err := st.CreateUser(e.ctx, store.NewUser{Username: "writer", Email: "w@example.test", Role: "author", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	due := time.Now().Add(1500 * time.Millisecond)
	id, err := st.CreatePost(e.ctx, store.PostInput{Type: store.TypePost, Status: store.StatusPublished, Title: "Later", Slug: "later",
		Lang: "es", AuthorID: author, CommentStatus: "open", PublishedAt: &due})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetAnnouncePending(e.ctx, id, true); err != nil {
		t.Fatal(err)
	}
	hooks{r: e.reg, id: a.ID}.Wakeup(e.ctx, due)
	// A wake-up lost to a recompute running at the same time: the unload
	// records the right date again.
	if err := e.n.SetNextAnnounce(e.ctx, a.ID, nil); err != nil {
		t.Fatal(err)
	}

	e.unloadAll()
	row := e.row(a.ID)
	if !row.WantsCron || row.NextAnnounceAt == nil || !row.NextAnnounceAt.Equal(due.Truncate(time.Microsecond)) {
		t.Fatalf("after the unload: cron %v, next %v, want %v", row.WantsCron, row.NextAnnounceAt, due)
	}
	if got := e.reg.Announce(e.ctx); len(got) != 0 {
		t.Fatalf("woke %v before the post is due", got)
	}

	time.Sleep(time.Until(due) + 50*time.Millisecond)
	opened := e.reg.opened.Load()
	if got := e.reg.Announce(e.ctx); !slices.Equal(got, []int64{a.ID}) {
		t.Fatalf("woke %v", got)
	}
	if n := e.reg.opened.Load() - opened; n != 1 {
		t.Fatalf("the wake-up opened %d sites", n)
	}
	waitFor(t, "the announcement", func() bool { return e.logged(a.ID, "post.published later") > 0 })
	if e.row(a.ID).NextAnnounceAt != nil {
		t.Fatal("the wake-up stays due")
	}
	// Nothing announces it again: not the next sweep, not the site's own
	// loop.
	if got := e.reg.Announce(e.ctx); len(got) != 0 {
		t.Fatalf("woke %v again", got)
	}
	e.entry(a.ID).app.Server().AnnounceDue(e.ctx)
	time.Sleep(200 * time.Millisecond)
	if n := e.logged(a.ID, "post.published later"); n != 1 {
		t.Fatalf("announced %d times", n)
	}
	// The wake-up was not a use: the next sweep unloads the site.
	if got := e.reg.Sweep(e.ctx); !slices.Equal(got, []int64{a.ID}) {
		t.Fatalf("sweep after the wake-up unloaded %v", got)
	}

	// A site that does not load is tried again later, not every minute.
	b := e.tenant("beta", "pro")
	if _, err := e.n.db.Exec(e.ctx, `UPDATE sites SET secret = '\x00'::bytea WHERE id = $1`, b.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.n.WakeAt(e.ctx, b.ID, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := e.reg.Announce(e.ctx); len(got) != 0 {
		t.Fatalf("woke %v", got)
	}
	if next := e.row(b.ID).NextAnnounceAt; next == nil || time.Until(*next) < 9*time.Minute {
		t.Fatalf("broken site wakes again at %v", next)
	}
}

// TestCron: the hourly cron reaches the loaded sites and loads only the
// unloaded ones whose plugins want it.
func TestCron(t *testing.T) {
	src := cronPlugin(t)
	e := newNetEnv(t, nil)
	a, b := e.tenant("alpha", "pro"), e.tenant("beta", "pro")
	for _, s := range []*Site{a, b} {
		if rec := e.do("GET", "https://"+s.Slug+".sap13ns.site/"); rec.Code != http.StatusOK {
			t.Fatalf("%s: %d", s.Slug, rec.Code)
		}
	}
	e.install(a, src)
	e.unloadAll()
	if !e.row(a.ID).WantsCron || e.row(b.ID).WantsCron {
		t.Fatalf("wants cron: alpha %v, beta %v", e.row(a.ID).WantsCron, e.row(b.ID).WantsCron)
	}

	opened := e.reg.opened.Load()
	if got, want := e.reg.Cron(e.ctx), []int64{e.portalID(), a.ID}; !slices.Equal(got, want) {
		t.Fatalf("cron reached %v, want %v", got, want)
	}
	if n := e.reg.opened.Load() - opened; n != 1 || e.entry(b.ID) != nil {
		t.Fatalf("cron opened %d sites; beta loaded: %v", n, e.entry(b.ID) != nil)
	}
	waitFor(t, "cron.hourly", func() bool { return e.logged(a.ID, "cron.hourly") == 1 })

	// The site keeps wanting it after a reload with its plugin; it stops
	// once the plugin is disabled.
	if err := e.entry(a.ID).app.Plugins().Disable(e.ctx, "hooks"); err != nil {
		t.Fatal(err)
	}
	if got := e.reg.Sweep(e.ctx); !slices.Equal(got, []int64{a.ID}) {
		t.Fatalf("sweep unloaded %v", got)
	}
	if e.row(a.ID).WantsCron {
		t.Fatal("a site without cron plugins still wants the cron")
	}
	if got := e.reg.Cron(e.ctx); !slices.Equal(got, []int64{e.portalID()}) {
		t.Fatalf("cron reached %v", got)
	}
}

// TestMaintain: the daily upkeep cleans and measures every schema without
// loading any site.
func TestMaintain(t *testing.T) {
	e := newNetEnv(t, nil)
	a := e.tenant("alpha", "pro")
	st := e.siteStore(a)
	uid, err := st.CreateUser(e.ctx, store.NewUser{Username: "visitor", Email: "u@example.test", Role: "subscriber", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	for i, exp := range []time.Duration{-time.Hour, time.Hour} {
		if err := st.CreateSession(e.ctx, store.Session{TokenHash: auth.HashToken("t" + strconv.Itoa(i)), UserID: uid, CSRFToken: "c",
			ExpiresAt: time.Now().Add(exp), UserAgent: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(e.n.Dir(a.ID), "uploads"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.n.Dir(a.ID), "uploads", "x.bin"), make([]byte, 5000), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := e.n.db.Exec(e.ctx, `INSERT INTO sso_codes (code_hash, site_id, account_id, challenge, redirect_uri, expires_at)
		VALUES ('\x01', $1, $2, '\x02', 'https://alpha.sap13ns.site/sso', now() - interval '2 days'),
		       ('\x03', $1, $2, '\x04', 'https://alpha.sap13ns.site/sso', now() + interval '1 minute')`, a.ID, e.owner); err != nil {
		t.Fatal(err)
	}

	opened := e.reg.opened.Load()
	e.reg.Maintain(e.ctx)
	if e.reg.opened.Load() != opened || !slices.Equal(e.reg.Loaded(), []int64{e.portalID()}) {
		t.Fatalf("maintenance loaded sites: %v", e.reg.Loaded())
	}
	var sessions, codes int
	if err := st.DB.QueryRow(e.ctx, `SELECT count(*) FROM sessions`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if err := e.n.db.QueryRow(e.ctx, `SELECT count(*) FROM sso_codes`).Scan(&codes); err != nil {
		t.Fatal(err)
	}
	if sessions != 1 || codes != 1 {
		t.Fatalf("left %d sessions and %d SSO codes, want 1 and 1", sessions, codes)
	}
	schema, err := e.n.SchemaBytes(e.ctx, a.Schema)
	if err != nil || schema == 0 {
		t.Fatalf("schema bytes %d %v", schema, err)
	}
	row := e.row(a.ID)
	if row.StorageCheckedAt == nil || row.StorageBytes < schema+5000 {
		t.Fatalf("storage %d at %v, want at least %d", row.StorageBytes, row.StorageCheckedAt, schema+5000)
	}
	if portal := e.row(e.portalID()); portal.StorageCheckedAt == nil || portal.StorageBytes == 0 {
		t.Fatalf("portal storage %d", portal.StorageBytes)
	}
	if n, err := dirBytes(filepath.Join(t.TempDir(), "none")); err != nil || n != 0 {
		t.Fatalf("missing directory: %d %v", n, err)
	}
}
