package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
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
	if e.postAs("/admin/system/update", e.author, url.Values{"csrf": {e.acsrf}}).Code == http.StatusSeeOther {
		t.Error("an author can update the server")
	}

	// The service's verdict.
	os.Remove(e.s.updateRequest) // the service takes it
	update.WriteStatus(e.s.updateStatus, update.Status{State: update.StateFailed, Error: "the backup failed", At: time.Now()})
	if page := e.get("/admin/system", e.admin).Body.String(); !strings.Contains(page, `<div class="alert" data-variant="destructive" role="status">`) || !strings.Contains(page, "the backup failed") {
		t.Error("a failed update is not shown")
	}
}
