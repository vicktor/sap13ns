package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestStatsLevel(t *testing.T) {
	for _, tc := range []struct {
		v, peak int64
		want    int
	}{{0, 100, 0}, {1, 100, 1}, {10, 100, 3}, {99, 100, 5}, {100, 100, 5}, {1, 1, 5}, {3, 2, 5}} {
		if got := statsLevel(tc.v, tc.peak); got != tc.want {
			t.Errorf("statsLevel(%d, %d) = %d, want %d", tc.v, tc.peak, got, tc.want)
		}
	}
}

func TestStatsMap(t *testing.T) {
	e := newCustomEnv(t)
	ctx := context.Background()
	today := time.Now().In(e.s.loc()).Format(time.DateOnly)
	if _, err := e.st.DB.Exec(ctx, `INSERT INTO analytics_dims VALUES ($1, 'country', 'ES', 90), ($1, 'country', 'FR', 9),
		($1, 'country', 'SG', 1)`, today); err != nil {
		t.Fatal(err)
	}
	rec := e.get("/admin/analytics", e.admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET: %d", rec.Code)
	}
	body := rec.Body.String()
	for _, s := range []string{
		`class="geo-5"><title>España: 90 visitas (90.0%)</title>`, // Spain, the most visited
		`class="geo-3"><title>Francia: 9 visitas (9.0%)</title>`,
		`class="geo-0"><title>Portugal</title>`, // no visits
		"Singapur",                              // too small for the map, still listed
		"no tiene base de datos de países",      // no SAP13NS_GEOIP_DB
	} {
		if !strings.Contains(body, s) {
			t.Errorf("missing %q", s)
		}
	}
	if strings.Contains(body, "db-ip.com") {
		t.Error("a DB-IP credit with no database")
	}

	// Today: the day so far, without the per-day chart.
	body = e.get("/admin/analytics?range=today", e.admin).Body.String()
	if !strings.Contains(body, `aria-current="page">Hoy</a>`) || !strings.Contains(body, "Singapur") || strings.Contains(body, "stats-chart") {
		t.Error("the Today range")
	}
	if rec := e.get("/admin/analytics", e.author); rec.Code == http.StatusOK {
		t.Error("an author opened the analytics")
	}
}

func TestStatsBots(t *testing.T) {
	e := newCustomEnv(t)
	ctx := context.Background()
	const (
		chrome = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36"
		gpt    = "Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko); compatible; GPTBot/1.2; +https://openai.com/gptbot"
		google = "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"
	)
	visit := func(method, path, ua string) {
		req := httptest.NewRequest(method, "http://example.test"+path, nil)
		req.Header.Set("User-Agent", ua)
		e.do(req, nil)
	}
	visit("GET", "/", chrome) // the only visit
	visit("GET", "/", gpt)
	visit("GET", "/robots.txt", gpt)
	visit("GET", "/", google)
	visit("GET", "/wp-login.php", chrome) // scanners, whatever their client
	visit("POST", "/xmlrpc.php", "")
	visit("GET", "/.env", "curl/8.7.1")
	visit("GET", "/static/admin.min.js", gpt) // files a page loads are not counted
	visit("GET", "/healthz", "curl/8.7.1")

	rec := e.get("/admin/analytics", e.admin) // signed in: not counted either
	if rec.Code != http.StatusOK {
		t.Fatalf("GET: %d", rec.Code)
	}
	var views int64
	if err := e.st.DB.QueryRow(ctx, `SELECT coalesce(sum(views), 0) FROM analytics_days`).Scan(&views); err != nil {
		t.Fatal(err)
	}
	if views != 1 {
		t.Errorf("%d views, want 1: bots and scanners must not count as visits", views)
	}
	rows, err := e.st.DB.Query(ctx, `SELECT category || ':' || name || ':' || hits FROM analytics_bots ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		got = append(got, s)
	}
	rows.Close()
	if want := "[ai:GPTBot:2 scanner::3 search:Googlebot:1]"; fmt.Sprint(got) != want {
		t.Errorf("bot rows %v, want %s", got, want)
	}
	body := rec.Body.String()
	for _, s := range []string{
		"Bots de IA", "Buscadores", "Escáneres", "Vistas previas sociales", "SEO y monitorización", "Otros bots",
		"GPTBot", "Googlebot", "6 peticiones en este periodo.",
	} {
		if !strings.Contains(body, s) {
			t.Errorf("missing %q", s)
		}
	}
}

// An administrator sets the statistics back to zero; the page then says
// since when it counts.
func TestStatsReset(t *testing.T) {
	e := newCustomEnv(t)
	ctx := context.Background()
	today := time.Now().In(e.s.loc()).Format(time.DateOnly)
	for _, q := range []string{`INSERT INTO analytics_days VALUES ($1, 40, 7)`,
		`INSERT INTO analytics_pages VALUES ($1, '/test', 40)`, `INSERT INTO analytics_bots VALUES ($1, 'ai', 'GPTBot', 3)`} {
		if _, err := e.st.DB.Exec(ctx, q, today); err != nil {
			t.Fatal(err)
		}
	}
	count := func() (n int) {
		t.Helper()
		if err := e.st.DB.QueryRow(ctx, `SELECT (SELECT count(*) FROM analytics_days) + (SELECT count(*) FROM analytics_pages)
			+ (SELECT count(*) FROM analytics_dims) + (SELECT count(*) FROM analytics_bots)`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	body := e.get("/admin/analytics", e.admin).Body.String()
	if !strings.Contains(body, `action="/admin/analytics/reset"`) || strings.Contains(body, "Contando desde") {
		t.Fatal("before a reset: no reset button, or a date to count from")
	}
	if rec := e.postAs("/admin/analytics/reset", e.author, url.Values{"csrf": {e.acsrf}}); rec.Code == http.StatusSeeOther || count() != 3 {
		t.Fatalf("an author reset the statistics: %d", rec.Code)
	}
	if rec := e.postForm("/admin/analytics/reset", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("reset: %d", rec.Code)
	}
	if n := count(); n != 0 {
		t.Errorf("%d rows left after the reset", n)
	}
	if len(e.audited("analytics.reset")) != 1 {
		t.Error("the reset is not in the audit log")
	}
	body = e.get("/admin/analytics", e.admin).Body.String()
	if !strings.Contains(body, "Contando desde el "+today) {
		t.Error("the page does not say since when it counts")
	}
}
