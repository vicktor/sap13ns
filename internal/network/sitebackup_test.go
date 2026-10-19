package network

import (
	"context"
	"strings"
	"testing"
)

// TestRestoredNotification: a restore from the command line reaches the
// running server. The site's cached pages go at once, and the site is
// loaded again once idle.
func TestRestoredNotification(t *testing.T) {
	e := newNetEnv(t, nil)
	ctx := context.Background()
	a := e.tenant("alpha", "pro")
	sa := e.siteStore(a)
	publish(t, sa, "Before the restore", "news")
	const page = "https://alpha.sap13ns.site/blog/news"
	if rec := e.do("GET", page); !strings.Contains(rec.Body.String(), "Before the restore") {
		t.Fatalf("before: %d", rec.Code)
	}
	// The restore changes the database behind the server's back.
	if _, err := sa.DB.Exec(ctx, `UPDATE posts SET title = 'After the restore'`); err != nil {
		t.Fatal(err)
	}
	if rec := e.do("GET", page); !strings.Contains(rec.Body.String(), "Before the restore") {
		t.Fatal("the page was not cached; the test proves nothing")
	}
	if err := e.n.SiteRestored(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the restored content", func() bool { return strings.Contains(e.do("GET", page).Body.String(), "After the restore") })
	waitFor(t, "a reload", func() bool {
		e.reg.mu.Lock()
		defer e.reg.mu.Unlock()
		en := e.reg.entries[a.ID]
		return en == nil || en.stale
	})
}
