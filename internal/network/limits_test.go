package network

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"sap13ns/internal/plugin"
	"sap13ns/internal/store"
)

// addPlan adds a free plan to the network.
func (e *netEnv) addPlan(slug string, storage int64, domains int, uploads bool) {
	e.t.Helper()
	if _, err := e.n.db.Exec(e.ctx, `INSERT INTO plans (slug, name, paid, hand_approved, commercial, sites_per_grant, storage_bytes,
		custom_domains, plugin_uploads, theme_uploads, position) VALUES ($1, $1, false, false, false, 1, $2, $3, $4, false, 9)`,
		slug, storage, domains, uploads); err != nil {
		e.t.Fatal(err)
	}
}

// setPlan moves a site to another plan, as a downgrade does.
func (e *netEnv) setPlan(s *Site, plan string) {
	e.t.Helper()
	if _, err := e.n.db.Exec(e.ctx, `UPDATE sites SET plan = $2 WHERE id = $1`, s.ID, plan); err != nil {
		e.t.Fatal(err)
	}
}

func (e *netEnv) storage(id int64) int64 {
	e.t.Helper()
	b, err := e.n.StorageBytes(e.ctx, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return b
}

// siteAdmin signs a new browser in to a site as a new administrator with
// a second factor and returns it with the session's CSRF token.
func (e *netEnv) siteAdmin(s *Site, name string) (*browser, string) {
	e.t.Helper()
	st := e.siteStore(s)
	id, err := st.CreateUser(e.ctx, store.NewUser{Username: name, Email: name + "@example.com", Role: "admin", PasswordHash: "x"})
	if err != nil {
		e.t.Fatal(err)
	}
	if err := st.SetTOTP(e.ctx, id, []byte("sealed"), true, nil); err != nil {
		e.t.Fatal(err)
	}
	b := e.browser()
	return b, b.siteSignIn(s, id)
}

// send posts a file in a multipart form to a site, as the admin's pages
// do (with Accept: application/json, as its media library does).
func (b *browser) send(s *Site, target, csrf, field, name string, data []byte, accept string) *httptest.ResponseRecorder {
	b.e.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile(field, name)
	if err == nil {
		_, err = fw.Write(data)
	}
	if err == nil {
		err = mw.WriteField("csrf", csrf)
	}
	if err == nil {
		err = mw.Close()
	}
	if err != nil {
		b.e.t.Error(err)
	}
	body := buf.Bytes()
	return b.do("POST", "https://"+s.PrimaryHost+target, nil, func(r *http.Request) {
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		r.Header.Set("Content-Type", mw.FormDataContentType())
		r.Header.Set("X-CSRF-Token", csrf)
		if accept != "" {
			r.Header.Set("Accept", accept)
		}
	})
}

// text is a distinct text file of n bytes.
func text(i, n int) []byte {
	b := []byte(fmt.Sprintf("file %d\n", i))
	return append(b, bytes.Repeat([]byte("."), n-len(b))...)
}

// uploads counts the files of a site's uploads directory and their bytes,
// and fails when one is left in its temporary directory.
func (e *netEnv) uploads(s *Site) (n int, size int64) {
	e.t.Helper()
	dir := filepath.Join(e.n.Dir(s.ID), "uploads")
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil || d.IsDir() {
			return err
		}
		if rel, _ := filepath.Rel(dir, p); strings.HasPrefix(rel, ".tmp"+string(filepath.Separator)) {
			return fmt.Errorf("left behind: %s", rel)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		n, size = n+1, size+info.Size()
		return nil
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return n, size
}

func storageFull(body string) bool {
	return strings.Contains(body, "storage is full") || strings.Contains(body, "almacenamiento del sitio está lleno")
}

// TestStorageQuota: uploads count against the plan's storage; what does
// not fit is refused with 413 and leaves no file; a deletion gives the
// space back; a downgrade blocks additions only; the daily measurement
// replaces the count.
func TestStorageQuota(t *testing.T) {
	e := newNetEnv(t, nil)
	e.addPlan("tiny", 10_000, 0, false)
	e.addPlan("mini", 5_000, 0, false)
	s := e.tenant("quota", "tiny")
	b, csrf := e.siteAdmin(s, "admin")
	const js = "application/json"
	host := "https://" + s.PrimaryHost
	// The plan's storage also bounds a background export (web.exportFits).
	quota := func() int64 {
		t.Helper()
		q, err := siteHooks{hooks{r: e.reg, id: s.ID}}.StorageQuota(e.ctx)
		if err != nil {
			t.Fatal(err)
		}
		return q
	}
	if q := quota(); q != 10_000 {
		t.Fatalf("quota %d, want 10000", q)
	}

	var ids []int64
	for i := range 3 {
		rec := b.send(s, "/admin/media", csrf, "file", fmt.Sprintf("notes-%d.txt", i), text(i, 3000), js)
		var out struct {
			Items []struct {
				ID int64 `json:"id"`
			} `json:"items"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil || len(out.Items) != 1 {
			t.Fatalf("upload %d: %d %s", i, rec.Code, rec.Body.String())
		}
		ids = append(ids, out.Items[0].ID)
	}
	if got := e.storage(s.ID); got != 9000 {
		t.Fatalf("storage %d, want 9000", got)
	}

	// Over the quota, from the media library and from a form.
	rec := b.send(s, "/admin/media", csrf, "file", "big.txt", text(9, 1001), js)
	if rec.Code != http.StatusRequestEntityTooLarge || !storageFull(rec.Body.String()) {
		t.Fatalf("over the quota: %d %s", rec.Code, rec.Body.String())
	}
	rec = b.send(s, "/admin/media", csrf, "file", "big.txt", text(9, 1001), "")
	if rec.Code != http.StatusRequestEntityTooLarge || !storageFull(rec.Body.String()) {
		t.Fatalf("over the quota (form): %d", rec.Code)
	}
	media := func() int {
		var n int
		if err := e.siteStore(s).DB.QueryRow(e.ctx, `SELECT count(*) FROM media`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n, size := e.uploads(s); n != 3 || size != 9000 || e.storage(s.ID) != 9000 || media() != 3 {
		t.Fatalf("after the refusals: %d files of %d bytes, %d counted, %d rows", n, size, e.storage(s.ID), media())
	}

	// A deletion gives its bytes back.
	del := func(id int64) {
		t.Helper()
		rec := b.do("POST", host+"/admin/media/"+strconv.FormatInt(id, 10)+"/delete", url.Values{"csrf": {csrf}})
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("delete: %d", rec.Code)
		}
	}
	del(ids[0])
	if n, _ := e.uploads(s); n != 2 || e.storage(s.ID) != 6000 {
		t.Fatalf("after a deletion: %d files, %d counted", n, e.storage(s.ID))
	}

	// A downgrade keeps what is there and blocks additions.
	e.setPlan(s, "mini")
	if q := quota(); q != 5_000 {
		t.Fatalf("quota after the downgrade %d, want 5000", q)
	}
	if rec := b.send(s, "/admin/media", csrf, "file", "small.txt", text(10, 100), js); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over the new plan: %d %s", rec.Code, rec.Body.String())
	}
	if n, _ := e.uploads(s); n != 2 || e.storage(s.ID) != 6000 {
		t.Fatalf("after the downgrade: %d files, %d counted", n, e.storage(s.ID))
	}
	del(ids[1])
	if rec := b.send(s, "/admin/media", csrf, "file", "small.txt", text(10, 1000), js); rec.Code != http.StatusOK {
		t.Fatalf("back under the limit: %d %s", rec.Code, rec.Body.String())
	}
	if e.storage(s.ID) != 4000 {
		t.Fatalf("%d counted", e.storage(s.ID))
	}

	// The daily measurement replaces the count with what is there.
	e.reg.Maintain(e.ctx)
	schema, err := e.n.SchemaBytes(e.ctx, s.Schema)
	if err != nil {
		t.Fatal(err)
	}
	files, err := dirBytes(e.n.Dir(s.ID))
	if err != nil {
		t.Fatal(err)
	}
	if got := e.storage(s.ID); got != schema+files || files < 4000 {
		t.Fatalf("measured %d, want %d + %d", got, schema, files)
	}
	// What changed while it measured is kept.
	before := e.storage(s.ID)
	if err := e.n.ReserveStorage(e.ctx, s.ID, 1); !errors.Is(err, ErrLimit) {
		t.Fatalf("the tables count too: %v", err)
	}
	e.setPlan(s, "personal")
	if err := e.n.ReserveStorage(e.ctx, s.ID, 700); err != nil {
		t.Fatal(err)
	}
	if err := e.n.SetStorage(e.ctx, s.ID, 5000, before); err != nil {
		t.Fatal(err)
	}
	if got := e.storage(s.ID); got != 5700 {
		t.Fatalf("an upload during the measurement: %d, want 5700", got)
	}
	if err := e.n.ReleaseStorage(e.ctx, s.ID, 1<<40); err != nil || e.storage(s.ID) != 0 {
		t.Fatalf("release more than counted: %v %d", err, e.storage(s.ID))
	}
}

// TestStorageConcurrent: concurrent uploads never overshoot the plan.
func TestStorageConcurrent(t *testing.T) {
	e := newNetEnv(t, nil)
	e.addPlan("tiny", 10_000, 0, false)
	s := e.tenant("race", "tiny")

	var ok atomic.Int32
	var wg sync.WaitGroup
	for range 40 {
		wg.Go(func() {
			switch err := e.n.ReserveStorage(e.ctx, s.ID, 300); {
			case err == nil:
				ok.Add(1)
			case !errors.Is(err, ErrLimit):
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if ok.Load() != 33 || e.storage(s.ID) != 9900 {
		t.Fatalf("%d reserved, %d counted", ok.Load(), e.storage(s.ID))
	}
	if err := e.n.ReleaseStorage(e.ctx, s.ID, 9900); err != nil || e.storage(s.ID) != 0 {
		t.Fatalf("release: %v %d", err, e.storage(s.ID))
	}

	// Through the admin: twelve uploads at once, room for six.
	b, csrf := e.siteAdmin(s, "admin")
	codes := make([]int, 12)
	for i := range codes {
		wg.Go(func() {
			codes[i] = b.send(s, "/admin/media", csrf, "file", fmt.Sprintf("f%d.txt", i), text(i, 1500), "application/json").Code
		})
	}
	wg.Wait()
	stored := 0
	for _, c := range codes {
		switch c {
		case http.StatusOK:
			stored++
		case http.StatusRequestEntityTooLarge:
		default:
			t.Fatalf("codes %v", codes)
		}
	}
	if n, size := e.uploads(s); stored != 6 || n != 6 || size != 9000 || e.storage(s.ID) != 9000 {
		t.Fatalf("%d stored (%v), %d files of %d bytes, %d counted", stored, codes, n, size, e.storage(s.ID))
	}
}

// TestCustomDomainLimit: a plan's custom domains, counted per site; a
// downgrade keeps those there and blocks more.
func TestCustomDomainLimit(t *testing.T) {
	e := newNetEnv(t, nil)
	free := e.tenant("free", PlanPersonal)
	if _, err := e.n.AddDomain(e.ctx, free.ID, "blog.example.com"); !errors.Is(err, ErrLimit) {
		t.Fatalf("Personal: %v", err)
	}
	pro, other := e.tenant("pro", "pro"), e.tenant("other", "pro")
	for _, h := range []string{"example.org", "www.example.org"} {
		if _, err := e.n.AddDomain(e.ctx, pro.ID, h); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.n.AddDomain(e.ctx, pro.ID, "shop.example.org"); !errors.Is(err, ErrLimit) {
		t.Fatalf("a third: %v", err)
	}
	if _, err := e.n.AddDomain(e.ctx, other.ID, "example.net"); err != nil {
		t.Fatalf("another site's: %v", err)
	}
	e.setPlan(pro, PlanPersonal)
	for _, h := range []string{"example.org", "www.example.org"} {
		if d, err := e.n.Domain(e.ctx, h); err != nil || d.SiteID != pro.ID {
			t.Fatalf("%s after the downgrade: %v", h, err)
		}
	}
	if _, err := e.n.AddDomain(e.ctx, pro.ID, "shop.example.org"); !errors.Is(err, ErrLimit) {
		t.Fatalf("after the downgrade: %v", err)
	}
}

// TestSitesPerPlan: the sites an account asks for, per plan.
func TestSitesPerPlan(t *testing.T) {
	f := setup(t)
	ann := f.account("ann")
	if _, err := f.n.db.Exec(f.ctx, `UPDATE plans SET sites_per_grant = 2 WHERE slug = 'personal'`); err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"annone", "anntwo"} {
		if _, err := f.n.RequestSite(f.ctx, ann, slug); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.n.RequestSite(f.ctx, ann, "annthree"); !errors.Is(err, ErrState) {
		t.Fatalf("a third: %v", err)
	}
}

// packPlugin packs a plugin directory, its manifest edited first.
func packPlugin(t *testing.T, src string, edit func(map[string]any)) []byte {
	t.Helper()
	dir := src
	if edit != nil {
		dir = filepath.Join(t.TempDir(), "hooks")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		wasm, err := os.ReadFile(filepath.Join(src, "plugin.wasm"))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(src, "plugin.json"))
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		edit(m)
		raw, _ = json.Marshal(m)
		if os.WriteFile(filepath.Join(dir, "plugin.json"), raw, 0o644) != nil || os.WriteFile(filepath.Join(dir, "plugin.wasm"), wasm, 0o644) != nil {
			t.Fatal("write the plugin")
		}
	}
	var buf bytes.Buffer
	if _, err := plugin.Pack(dir, &buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// enable switches a plugin of a site on from its page, as its admin does.
func (b *browser) enable(s *Site, csrf, id string) {
	b.e.t.Helper()
	p := b.e.entry(s.ID).app.Plugins().Get(id)
	if p == nil {
		b.e.t.Fatalf("no plugin %s", id)
	}
	b.do("POST", "https://"+s.PrimaryHost+"/admin/plugins/"+id+"/approve", url.Values{"csrf": {csrf}, "pin": {p.ReviewPin()}, "confirm_risk": {"1"}})
}

// TestPluginPolicy: a Personal site installs from the network's catalogue
// and cannot upload; a Pro site's upload is held until a network admin
// approves that very build (its module, assets and permissions), after
// which it can be switched on and runs.
func TestPluginPolicy(t *testing.T) {
	src := cronPlugin(t)
	e := newNetEnv(t, nil)
	pkg := packPlugin(t, src, nil)
	if _, err := e.entry(e.portalID()).app.Plugins().Install(e.ctx, bytes.NewReader(pkg), int64(len(pkg)), plugin.InstallOptions{Source: "cli"}); err != nil {
		t.Fatal(err)
	}
	hooks := func(s *Site) *plugin.Plugin {
		t.Helper()
		return e.entry(s.ID).app.Plugins().Get("hooks")
	}
	pluginDir := func(s *Site) []string {
		t.Helper()
		ents, err := os.ReadDir(filepath.Join(e.n.Dir(s.ID), "plugins"))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Fatal(err)
		}
		var names []string
		for _, d := range ents {
			names = append(names, d.Name())
		}
		return names
	}

	// Personal: the catalogue only.
	free := e.personal("free")
	fb, fcsrf := e.siteAdmin(free, "admin")
	freeURL := "https://" + free.PrimaryHost
	page := fb.get(freeURL + "/admin/plugins")
	if body := page.Body.String(); page.Code != http.StatusOK || !strings.Contains(body, `action="/admin/plugins/catalog/hooks/install"`) ||
		strings.Contains(body, `action="/admin/plugins/upload"`) {
		t.Fatalf("plugins page: %d", page.Code)
	}
	if rec := fb.send(free, "/admin/plugins/upload", fcsrf, "package", "hooks.zip", pkg, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("Personal upload: %d", rec.Code)
	}
	if names := pluginDir(free); len(names) != 0 {
		t.Fatalf("left %v", names)
	}
	if rec := fb.do("POST", freeURL+"/admin/plugins/catalog/nope/install", url.Values{"csrf": {fcsrf}}); rec.Code != http.StatusNotFound {
		t.Fatalf("not in the catalogue: %d", rec.Code)
	}
	if rec := fb.do("POST", freeURL+"/admin/plugins/catalog/hooks/install", url.Values{"csrf": {fcsrf}}); rec.Code != http.StatusSeeOther ||
		rec.Header().Get("Location") != "/admin/plugins/hooks" {
		t.Fatalf("catalogue install: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if p := hooks(free); p == nil || p.Record.Source != "catalog" || p.Record.ReviewRequired {
		t.Fatalf("from the catalogue: %+v", p)
	}
	fb.enable(free, fcsrf, "hooks")
	if p := hooks(free); p.Status != plugin.StatusEnabled {
		t.Fatalf("catalogue plugin: %s %s", p.Status, p.Err)
	}

	// Pro: an upload waits for the operator.
	pro := e.tenant("pro", "pro")
	pb, pcsrf := e.siteAdmin(pro, "admin")
	upload := func(pkg []byte) {
		t.Helper()
		if rec := pb.send(pro, "/admin/plugins/upload", pcsrf, "package", "hooks.zip", pkg, ""); rec.Code != http.StatusSeeOther ||
			rec.Header().Get("Location") != "/admin/plugins/hooks" {
			t.Fatalf("upload: %d %s %s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
		}
	}
	upload(pkg)
	if p := hooks(pro); p == nil || !p.Record.ReviewRequired || p.Record.Source != "upload" {
		t.Fatalf("an upload: %+v", p)
	}
	pb.enable(pro, pcsrf, "hooks")
	if p := hooks(pro); p.Status == plugin.StatusEnabled {
		t.Fatal("enabled before the review")
	}
	reviews, err := e.n.PluginReviews(e.ctx)
	if err != nil || len(reviews) != 1 {
		t.Fatalf("reviews %+v %v", reviews, err)
	}
	r := reviews[0]
	if r.SiteID != pro.ID || r.PluginID != "hooks" || r.Pin != hooks(pro).ReviewPin() || r.SHA256 != hooks(pro).SHA256 ||
		!slices.Contains(r.Permissions, "mail") || r.Version != "1.0.0" {
		t.Fatalf("review %+v", r)
	}
	if n, err := e.n.PendingPluginReviews(e.ctx); err != nil || n != 1 {
		t.Fatalf("pending %d %v", n, err)
	}

	// The network admin's queue.
	_, nb, ncsrf := e.networkAdmin("operator")
	if body := nb.page("/admin/network/plugins"); !strings.Contains(body, `name="pin" value="`+r.Pin+`"`) || !strings.Contains(body, "mail") {
		t.Fatal("not in the queue")
	}
	review := func(pin, verdict string) {
		t.Helper()
		nb.post("/admin/network/plugins/review", "/admin/network/plugins", url.Values{"csrf": {ncsrf},
			"site": {strconv.FormatInt(pro.ID, 10)}, "plugin": {"hooks"}, "pin": {pin}, "verdict": {verdict}})
	}

	// Rejected: it stays held; uploading it again asks again.
	review(r.Pin, "reject")
	if !hooks(pro).Record.ReviewRequired || audits(t, e.portal, "network.plugin_rejected") != 1 {
		t.Fatal("rejected")
	}
	if n, _ := e.n.PendingPluginReviews(e.ctx); n != 0 {
		t.Fatalf("%d pending", n)
	}
	upload(pkg)
	if n, _ := e.n.PendingPluginReviews(e.ctx); n != 1 || !hooks(pro).Record.ReviewRequired {
		t.Fatalf("asked again: %d", n)
	}

	// Approved: the site switches it on and it runs.
	review(r.Pin, "approve")
	if hooks(pro).Record.ReviewRequired || audits(t, e.portal, "network.plugin_approved") != 1 {
		t.Fatal("approved")
	}
	pb.enable(pro, pcsrf, "hooks")
	if p := hooks(pro); p.Status != plugin.StatusEnabled {
		t.Fatalf("after the review: %s %s", p.Status, p.Err)
	}
	e.entry(pro.ID).app.Plugins().Cron(e.clock.now())
	waitFor(t, "the plugin to run", func() bool { return e.logged(pro.ID, "cron.hourly") > 0 })

	// The same build again: approved at once.
	upload(pkg)
	if hooks(pro).Record.ReviewRequired {
		t.Fatal("the approved build is held again")
	}
	// The same module asking for more is a build of its own.
	more := packPlugin(t, src, func(m map[string]any) { m["permissions"] = []string{"posts:read", "mail", "kv"} })
	upload(more)
	if !hooks(pro).Record.ReviewRequired {
		t.Fatal("more permissions, not held")
	}
	if reviews, _ := e.n.PluginReviews(e.ctx); len(reviews) != 1 || reviews[0].Pin == r.Pin || !slices.Contains(reviews[0].Permissions, "kv") {
		t.Fatalf("more permissions: %+v", reviews)
	}
	stale := hooks(pro).ReviewPin()

	// Replaced from the catalogue: no review needed, and the request goes.
	install := func() {
		t.Helper()
		if rec := pb.do("POST", "https://"+pro.PrimaryHost+"/admin/plugins/catalog/hooks/install", url.Values{"csrf": {pcsrf}}); rec.Code != http.StatusSeeOther {
			t.Fatalf("catalogue install: %d", rec.Code)
		}
	}
	install()
	if p := hooks(pro); p.Record.ReviewRequired || p.Record.Source != "catalog" {
		t.Fatalf("from the catalogue: %+v", p.Record)
	}
	if reviews, _ := e.n.PluginReviews(e.ctx); len(reviews) != 0 {
		t.Fatalf("a replaced build listed: %+v", reviews)
	}
	if n, _ := e.n.PendingPluginReviews(e.ctx); n != 0 {
		t.Fatalf("%d pending", n)
	}
	// An approval that comes after the site replaced the build changes
	// nothing.
	upload(more)
	install()
	review(stale, "approve")
	if body := nb.page("/admin/network/plugins"); !strings.Contains(body, "no longer has this build") && !strings.Contains(body, "ya no tiene esta compilación") {
		t.Fatal("no message for a stale approval")
	}
	if n, _ := e.n.PendingPluginReviews(e.ctx); n != 0 {
		t.Fatalf("%d pending", n)
	}
	upload(more)
	if !hooks(pro).Record.ReviewRequired {
		t.Fatal("the stale approval counted")
	}

	// A downgrade stops uploads and keeps what is installed.
	e.setPlan(pro, PlanPersonal)
	if rec := pb.send(pro, "/admin/plugins/upload", pcsrf, "package", "hooks.zip", pkg, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("after the downgrade: %d", rec.Code)
	}
	if hooks(pro) == nil {
		t.Fatal("removed by the downgrade")
	}

	// Plugins count against the storage: neither an upload nor the
	// catalogue goes over it.
	e.addPlan("tinyup", 1000, 0, true)
	tiny := e.tenant("tiny", "tinyup")
	tb, tcsrf := e.siteAdmin(tiny, "admin")
	if rec := tb.send(tiny, "/admin/plugins/upload", tcsrf, "package", "hooks.zip", pkg, ""); rec.Code != http.StatusRequestEntityTooLarge || !storageFull(rec.Body.String()) {
		t.Fatalf("an upload over the quota: %d", rec.Code)
	}
	if rec := tb.do("POST", "https://"+tiny.PrimaryHost+"/admin/plugins/catalog/hooks/install", url.Values{"csrf": {tcsrf}}); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("a catalogue install over the quota: %d", rec.Code)
	}
	if names := pluginDir(tiny); len(names) != 0 || e.storage(tiny.ID) != 0 {
		t.Fatalf("left %v, %d counted", names, e.storage(tiny.ID))
	}
	// Only the Pro site's last upload waits.
	if reviews, _ := e.n.PluginReviews(e.ctx); len(reviews) != 1 || reviews[0].SiteID != pro.ID {
		t.Fatalf("waiting: %+v", reviews)
	}
}
