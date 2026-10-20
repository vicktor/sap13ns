package web

import (
	"context"
	"net/http"
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
	if rec := e.get("/admin/analytics", e.author); rec.Code == http.StatusOK {
		t.Error("an author opened the analytics")
	}
}
