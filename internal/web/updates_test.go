package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sap13ns/internal/config"
	"sap13ns/internal/mail"
	"sap13ns/internal/testutil"
	"sap13ns/internal/update"
)

// githubRT answers the release API with one release, without the network.
type githubRT struct{ body []byte }

func (g githubRT) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(string(g.body))), Request: r}, nil
}

// A newer release puts a warning on every admin page and its changes, since
// the running version only, on the System page, through the sanitiser.
func TestUpdateNoticeChanges(t *testing.T) {
	old := Version
	Version = "0.2.0"
	t.Cleanup(func() { Version = old })

	notes := "## v0.3.0 — 2026-11-02\n\n- Big **news**. <script>alert(1)</script>\n\n## v0.2.1 — 2026-10-20\n\n- A fix.\n\n## v0.2.0 — 2026-10-20\n\n- Already running.\n"
	body, _ := json.Marshal(map[string]any{"tag_name": "v0.3.0", "body": notes, "html_url": "https://github.com/vicktor/sap13ns/releases/tag/v0.3.0",
		"published_at": "2026-11-02T10:00:00Z"})
	c := update.NewChecker(Version, &http.Client{Transport: githubRT{body}}, nil)
	c.Disabled = false
	if err := c.CheckNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	e := newCustomEnv(t)
	e.s.SetUpdateChecker(c)
	e.s.mailer = mail.New(config.SMTP{}, testutil.Log(), false) // the System page describes it

	dash := e.get("/admin/", e.admin).Body.String()
	if !strings.Contains(dash, `class="alert update-notice" data-variant="warning"`) || !strings.Contains(dash, `href="/admin/system#changes"`) {
		t.Fatal("no update warning on the dashboard")
	}
	if strings.Contains(e.get("/admin/", e.author).Body.String(), "update-notice") {
		t.Fatal("an author is told to update the server")
	}

	rec := e.get("/admin/system", e.admin)
	sys := rec.Body.String()
	i := strings.Index(sys, `id="changes"`)
	if i < 0 {
		t.Fatalf("no changes on the System page (%d):\n%.3000s", rec.Code, sys)
	}
	sys = sys[i:]
	for _, want := range []string{"0.3.0", "2026-11-02", "<strong>news</strong>", "0.2.1", "A fix."} {
		if !strings.Contains(sys, want) {
			t.Errorf("changes lack %q", want)
		}
	}
	if strings.Contains(sys, "Already running") || strings.Contains(sys, "<script>alert") {
		t.Errorf("changes show the running release or unsanitised notes:\n%s", sys[:min(len(sys), 2000)])
	}

	// Without the update service the page shows the shell commands only.
	if strings.Contains(sys, `action="/admin/system/update"`) {
		t.Error("Update now without the update service")
	}

	// With it: "Update now" leaves a request, and the page says it is on its way.
	dir := t.TempDir()
	e.s.updateStatus, e.s.updateRequest = filepath.Join(dir, "status"), filepath.Join(dir, "update-request")
	e.s.updateUnit = filepath.Join(dir, "sap13ns-update.path")
	os.Mkdir(e.s.updateStatus, 0o755)
	if !strings.Contains(e.get("/admin/system", e.admin).Body.String(), `action="/admin/system/update"`) {
		t.Fatal("no Update now with the update service")
	}
	update.PublicKey = "x" // a signed build
	t.Cleanup(func() { update.PublicKey = "" })
	if rec := e.postForm("/admin/system/update", url.Values{}); rec.Code != http.StatusSeeOther || !update.Requested(e.s.updateRequest) {
		t.Fatalf("update now: %d, requested %v", rec.Code, update.Requested(e.s.updateRequest))
	}
	page := e.get("/admin/system", e.admin).Body.String()
	page = page[strings.Index(page, `id="updates"`):]
	if strings.Contains(page, `action="/admin/system/update"`) || !strings.Contains(page, `<div class="alert" role="status">`) {
		t.Error("the page does not show the update under way")
	}
	if !strings.Contains(e.get("/admin/system", e.admin).Body.String(), `data-update-dialog data-update-waiting`) {
		t.Error("a page loaded during the update does not follow it")
	}
	if e.postAs("/admin/system/update", e.author, url.Values{"csrf": {e.acsrf}}).Code == http.StatusSeeOther {
		t.Error("an author can update the server")
	}

	// The service's verdict.
	os.Remove(e.s.updateRequest) // the service takes it
	update.WriteStatus(e.s.updateStatus, update.Status{State: update.StateFailed, Error: "the backup failed", At: time.Now()})
	if page := e.get("/admin/system", e.admin).Body.String(); !strings.Contains(page, `<div class="alert" data-variant="destructive" role="status">`) || !strings.Contains(page, "the backup failed") {
		t.Error("a failed update is not shown")
	}

	// The System page's script: Update now in JSON, then the status it polls.
	status := func(cookie *http.Cookie) (int, updateStateJSON) {
		rec := e.get("/admin/system/update-status", cookie)
		var st updateStateJSON
		json.Unmarshal(rec.Body.Bytes(), &st)
		return rec.Code, st
	}
	if code, st := status(e.admin); code != http.StatusOK || st.State != update.StateFailed || st.Error != "the backup failed" || st.Version != Version || st.Requested || st.At == nil {
		t.Errorf("status: %d %+v", code, st)
	}
	req := httptest.NewRequest("POST", "http://example.test/admin/system/update", nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-CSRF-Token", e.csrf)
	rec = e.do(req, e.admin)
	var st updateStateJSON
	if json.Unmarshal(rec.Body.Bytes(), &st); rec.Code != http.StatusAccepted || !st.Requested || st.Version != Version {
		t.Errorf("update now in JSON: %d %s", rec.Code, rec.Body)
	}
	if code, _ := status(e.author); code == http.StatusOK {
		t.Error("an author reads the update status")
	}
	os.Remove(e.s.updateRequest)

	// A site of a network: no System page, no notice; the server is the
	// operator's, who updates from the portal.
	e.s.sso = &fakeSSO{}
	if code, _ := status(e.admin); code != http.StatusNotFound {
		t.Errorf("update status on a tenant: %d", code)
	}
	if rec := e.get("/admin/system", e.admin); rec.Code != http.StatusNotFound {
		t.Errorf("System on a tenant: %d", rec.Code)
	}
	if rec := e.postForm("/admin/system/update", url.Values{}); rec.Code != http.StatusNotFound {
		t.Errorf("Update now on a tenant: %d", rec.Code)
	}
	if dash := e.get("/admin", e.admin).Body.String(); strings.Contains(dash, `href="/admin/system"`) || strings.Contains(dash, "update-notice") {
		t.Error("a tenant's admin links to System or shows the update notice")
	}
}
