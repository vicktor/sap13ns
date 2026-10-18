package backup

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// clock is a fake clock that a daily timer advances.
type clock struct{ t time.Time }

func (c *clock) Now() time.Time          { return c.t }
func (c *clock) Advance(d time.Duration) { c.t = c.t.Add(d) }

func touchArchive(t *testing.T, dir string, at time.Time, encrypted bool) string {
	t.Helper()
	p := filepath.Join(dir, archiveName(at, encrypted))
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func remaining(t *testing.T, dir string) []string {
	t.Helper()
	all, err := archives(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range all {
		out = append(out, e.Created.Format("2006-01-02"))
	}
	return out
}

func TestPruneGFS(t *testing.T) {
	dir := t.TempDir()
	c := &clock{t: time.Date(2026, 1, 1, 3, 30, 0, 0, time.UTC)} // a Thursday
	for c.t.Before(time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC)) {
		touchArchive(t, dir, c.Now(), false)
		c.Advance(24 * time.Hour)
	}
	removed, err := Prune(dir, 7, 4, 3)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		// daily: the 7 newest days (Sunday 15 March back to Monday 9 March)
		"2026-03-15", "2026-03-14", "2026-03-13", "2026-03-12", "2026-03-11", "2026-03-10", "2026-03-09",
		// weekly: the newest of ISO weeks 11 (already kept), 10, 9 and 8
		"2026-03-08", "2026-03-01",
		// monthly: March (kept), February, January
		"2026-02-28", "2026-02-22",
		"2026-01-31",
	}
	if got := remaining(t, dir); !slices.Equal(got, want) {
		t.Errorf("kept %v\nwant %v", got, want)
	}
	if len(removed) != 74-len(want) {
		t.Errorf("removed %d archives, want %d", len(removed), 74-len(want))
	}
	// Pruning again is a no-op.
	if removed, err := Prune(dir, 7, 4, 3); err != nil || len(removed) != 0 {
		t.Errorf("second prune removed %v, %v", removed, err)
	}
}

// A year of the daily timer, with a manual backup now and then, pruning
// after each run as the systemd unit does.
func TestPruneSimulatedYear(t *testing.T) {
	dir := t.TempDir()
	c := &clock{t: time.Date(2025, 11, 3, 3, 30, 0, 0, time.UTC)}
	end := time.Date(2026, 12, 20, 0, 0, 0, 0, time.UTC)
	var newest time.Time
	for day := 0; c.t.Before(end); day++ {
		touchArchive(t, dir, c.Now(), day%2 == 0)
		newest = c.Now()
		if day%17 == 0 { // a manual backup later the same day
			touchArchive(t, dir, c.Now().Add(9*time.Hour), false)
			newest = c.Now().Add(9 * time.Hour)
		}
		if _, err := Prune(dir, 7, 4, 12); err != nil {
			t.Fatal(err)
		}
		c.Advance(24 * time.Hour)
	}
	all, _ := archives(dir)
	if !all[0].Created.Equal(newest) {
		t.Errorf("newest archive %v was not kept (have %v)", newest, all[0].Created)
	}
	if len(all) > 1+7+4+12 {
		t.Errorf("kept %d archives, more than the schedule allows", len(all))
	}
	days, months := map[string]bool{}, map[string]bool{}
	for _, e := range all {
		days[e.Created.Format("2006-01-02")] = true
		months[e.Created.Format("2006-01")] = true
	}
	for i := range 7 {
		if d := time.Date(2026, 12, 19-i, 0, 0, 0, 0, time.UTC).Format("2006-01-02"); !days[d] {
			t.Errorf("no archive kept for %s", d)
		}
	}
	for m := 1; m <= 12; m++ {
		if mm := time.Date(2026, time.Month(m), 1, 0, 0, 0, 0, time.UTC).Format("2006-01"); !months[mm] {
			t.Errorf("no archive kept for %s", mm)
		}
	}
	// Each month keeps its newest archive.
	for _, mm := range []string{"2026-01-31", "2026-06-30", "2026-11-30"} {
		if !days[mm] {
			t.Errorf("end-of-month archive %s not kept", mm)
		}
	}
}

func TestPruneKeepsNewestAndForeignFiles(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 10, 1, 3, 30, 0, 0, time.UTC)
	for i := range 5 {
		touchArchive(t, dir, base.Add(time.Duration(i)*24*time.Hour), i%2 == 1)
	}
	foreign := []string{"notes.txt", "sap13ns-2026.tar.gz", "sap13ns-20250101-000000.tar.gz.bak", ".sap13ns-123.tmp", "sap13ns-20240101-000000.tar.gz.enc.part"}
	for _, n := range foreign {
		os.WriteFile(filepath.Join(dir, n), []byte("keep me"), 0o600)
	}
	// Directories and symlinks with archive names are not touched either.
	os.Mkdir(filepath.Join(dir, "sap13ns-20200101-000000.tar.gz"), 0o700)
	os.Symlink("/etc/hosts", filepath.Join(dir, "sap13ns-20200102-000000.tar.gz"))

	removed, err := Prune(dir, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 4 {
		t.Errorf("removed %v", removed)
	}
	if got := remaining(t, dir); !slices.Equal(got, []string{"2026-10-05"}) {
		t.Errorf("kept %v, want only the newest", got)
	}
	for _, n := range append(foreign, "sap13ns-20200101-000000.tar.gz", "sap13ns-20200102-000000.tar.gz") {
		if _, err := os.Lstat(filepath.Join(dir, n)); err != nil {
			t.Errorf("%s was touched: %v", n, err)
		}
	}
	if _, err := Prune(dir, -1, 0, 0); err == nil {
		t.Error("negative keep count accepted")
	}
	if removed, err := Prune(t.TempDir(), 7, 4, 12); err != nil || len(removed) != 0 {
		t.Errorf("empty dir: %v, %v", removed, err)
	}
}

func TestParseName(t *testing.T) {
	for name, want := range map[string]bool{
		"sap13ns-20261018-033000.tar.gz":     true,
		"sap13ns-20261018-033000.tar.gz.enc": true,
		"sap13ns-20261318-033000.tar.gz":     false, // month 13
		"sap13ns-20261018-033000.tar":        false,
		"xsap13ns-20261018-033000.tar.gz":    false,
		"sap13ns-20261018-033000.tar.gz.tmp": false,
	} {
		if _, _, ok := parseName(name); ok != want {
			t.Errorf("parseName(%q) ok = %v", name, ok)
		}
	}
	created, enc, _ := parseName(archiveName(testNow, true))
	if !created.Equal(testNow) || !enc {
		t.Errorf("archiveName does not round-trip: %v %v", created, enc)
	}
}
