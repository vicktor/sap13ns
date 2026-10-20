package analytics

import (
	"context"
	crand "crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// testDB connects to SAP13NS_TEST_DATABASE_URL and returns a connection
// whose search_path is a temporary schema holding the analytics tables of
// the migration. The schema is dropped when the test ends.
func testDB(t *testing.T) *pgx.Conn {
	t.Helper()
	url := os.Getenv("SAP13NS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set SAP13NS_TEST_DATABASE_URL to run against PostgreSQL")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Skipf("test database unavailable: %v", err)
	}
	var rnd [6]byte
	crand.Read(rnd[:])
	schema := pgx.Identifier{fmt.Sprintf("analytics_test_%x", rnd)}.Sanitize()
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		conn.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		if _, err := conn.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop schema: %v", err)
		}
		conn.Close(ctx)
	})
	if _, err := conn.Exec(ctx, "SET search_path TO "+schema); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join("..", "db", "migrations", "0003_features.sql"))
	if err != nil {
		t.Fatal(err)
	}
	tables := regexp.MustCompile(`(?s)CREATE TABLE analytics_\w+ \(.*?\n\);`).FindAllString(string(src), -1)
	if len(tables) != 3 {
		t.Fatalf("found %d analytics tables in the migration, want 3", len(tables))
	}
	for _, ddl := range tables {
		if _, err := conn.Exec(ctx, ddl); err != nil {
			t.Fatalf("%s: %v", ddl, err)
		}
	}
	return conn
}

func dbDay(t *testing.T, db DB, day string) [2]int64 {
	t.Helper()
	var v [2]int64
	err := db.QueryRow(context.Background(), `SELECT views, visitors FROM analytics_days WHERE day = $1::text::date`, day).Scan(&v[0], &v[1])
	if err != nil && err != pgx.ErrNoRows {
		t.Fatal(err)
	}
	return v
}

func TestDatabase(t *testing.T) {
	conn := testDB(t)
	ctx := context.Background()
	clk := &clock{t: time.Date(2026, 10, 10, 9, 0, 0, 0, madrid)}
	c := newTestCollector(conn, clk, Options{})
	iphone, firefox := browserUAs[3].ua, browserUAs[7].ua

	r := pageReq("203.0.113.1", chromeUA)
	r.Header.Set("Referer", "https://www.google.com/")
	c.Record(r, "/", "es")
	c.Record(pageReq("203.0.113.1", chromeUA), "/blog/hello", "es")
	c.Record(pageReq("203.0.113.2", iphone), "/blog/hello", "en")
	c.Record(pageReq("203.0.113.3", firefox), "/a\x00b ñ?q=secret", "es")
	if err := c.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if got := dbDay(t, conn, "2026-10-10"); got != [2]int64{4, 3} {
		t.Fatalf("after first flush = %v, want [4 3]", got)
	}

	c.Record(pageReq("203.0.113.1", chromeUA), "/", "es") // returning visitor
	if err := c.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if got := dbDay(t, conn, "2026-10-10"); got != [2]int64{5, 3} {
		t.Fatalf("after second flush = %v, want [5 3]", got)
	}

	// A restart must not lower the stored visitors.
	c2 := newTestCollector(conn, clk, Options{})
	c2.Record(pageReq("203.0.113.9", chromeUA), "/", "es")
	if err := c2.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if got := dbDay(t, conn, "2026-10-10"); got != [2]int64{6, 3} {
		t.Fatalf("after restart = %v, want [6 3]", got)
	}

	rows, _ := conn.Query(ctx, `SELECT path FROM analytics_pages WHERE day = '2026-10-10' ORDER BY path`)
	paths, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"/", "/ab ñ", "/blog/hello"}; !slices.Equal(paths, want) {
		t.Errorf("paths = %q, want %q", paths, want)
	}

	// 01:00 in Madrid on the 12th is still the 11th in UTC.
	clk.Set(time.Date(2026, 10, 12, 1, 0, 0, 0, madrid))
	c.Record(pageReq("203.0.113.1", chromeUA), "/", "es")
	c.Record(pageReq("203.0.113.4", iphone), "/about", "en")
	if err := c.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if got := dbDay(t, conn, "2026-10-12"); got != [2]int64{2, 2} {
		t.Fatalf("2026-10-12 = %v, want [2 2]", got)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO analytics_days VALUES ('2026-10-06', 10, 5)`); err != nil {
		t.Fatal(err)
	}

	s, err := Report(ctx, conn, time.Date(2026, 10, 9, 0, 0, 0, 0, madrid), time.Date(2026, 10, 12, 23, 59, 0, 0, madrid), 2)
	if err != nil {
		t.Fatal(err)
	}
	var series []string
	for _, d := range s.Series {
		series = append(series, fmt.Sprintf("%s:%d/%d", d.Day.Format(time.DateOnly), d.Views, d.Visitors))
	}
	if want := []string{"2026-10-09:0/0", "2026-10-10:6/3", "2026-10-11:0/0", "2026-10-12:2/2"}; !slices.Equal(series, want) {
		t.Errorf("series = %v, want %v", series, want)
	}
	if s.Totals != (Totals{8, 5}) || s.Previous != (Totals{10, 5}) || s.PeakViews != 6 {
		t.Errorf("totals %+v, previous %+v, peak %d", s.Totals, s.Previous, s.PeakViews)
	}
	if s.ViewsChange.String() != "-20%" || s.VisitorsChange.String() != "0%" {
		t.Errorf("changes %s, %s", s.ViewsChange, s.VisitorsChange)
	}
	if !s.From.Equal(time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)) || !s.To.Equal(time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("range %v - %v", s.From, s.To)
	}
	for name, tc := range map[string]struct {
		got  []Item
		want string
	}{
		"pages":     {s.Pages, "[{/ 4 50} {/blog/hello 2 25}]"},
		"referrers": {s.Referrers, "[{(direct) 7 87.5} {google.com 1 12.5}]"},
		"browsers":  {s.Browsers, "[{Chrome 5 62.5} {Safari 2 25}]"},
		"os":        {s.OS, "[{Windows 5 62.5} {iOS 2 25}]"},
		"devices":   {s.Devices, "[{desktop 6 75} {mobile 2 25}]"},
		"langs":     {s.Langs, "[{es 6 75} {en 2 25}]"},
	} {
		if got := fmt.Sprint(tc.got); got != tc.want {
			t.Errorf("%s = %s, want %s", name, got, tc.want)
		}
	}

	if _, err := Report(ctx, conn, time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC), 5); err == nil {
		t.Error("Report accepted a reversed range")
	}

	// Prune removes 2026-10-06 (1 row) and 2026-10-10 (1 day, 3 pages and
	// 12 dimension rows).
	n, err := Prune(ctx, conn, time.Date(2026, 10, 11, 12, 0, 0, 0, madrid))
	if err != nil {
		t.Fatal(err)
	}
	if n != 17 {
		t.Errorf("pruned %d rows, want 17", n)
	}
	s, err = Report(ctx, conn, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC), 0)
	if err != nil {
		t.Fatal(err)
	}
	if s.Totals != (Totals{2, 2}) || len(s.Series) != 31 || len(s.Pages) != 2 {
		t.Errorf("after prune totals %+v, %d days, pages %v", s.Totals, len(s.Series), s.Pages)
	}
}

func TestReportCountries(t *testing.T) {
	conn := testDB(t)
	ctx := context.Background()
	codes := []string{"ES", "US", "FR", "DE", "IT", "PT", "MX", "AR", "CO", "CL", "PE", "GB"}
	for i, cc := range codes {
		if _, err := conn.Exec(ctx, `INSERT INTO analytics_dims VALUES ('2026-10-10', 'country', $1, $2), ('2026-10-10', 'lang', $1, $2)`,
			cc, len(codes)-i); err != nil {
			t.Fatal(err)
		}
	}
	day := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	s, err := Report(ctx, conn, day, day, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Langs) != 2 || len(s.Countries) != len(codes) {
		t.Fatalf("%d languages, %d countries; want 2 and %d", len(s.Langs), len(s.Countries), len(codes))
	}
	if first := s.Countries[0]; first.Value != "ES" || first.Views != 12 || first.Share != 15.4 {
		t.Errorf("first country %+v", first)
	}
}
