package sitebackup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"sap13ns/internal/testutil"
)

// TestPruneLimits: the newest SafetyKeep safety backups are kept, the
// kept backups stay within Retention.MaxBytes (the oldest go first, never
// the newest backup nor a protected one), and a decrypted copy a crash
// left behind goes.
func TestPruneLimits(t *testing.T) {
	now := time.Date(2026, 10, 19, 3, 30, 0, 0, time.UTC)
	write := func(dir string, at time.Time, safety bool, size int) string {
		t.Helper()
		p := filepath.Join(dir, archiveName("x", at, safety, false))
		if err := os.WriteFile(p, make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	names := func(dir string) (regular, safety []string) {
		t.Helper()
		list, err := List(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range list {
			if a.PreRestore {
				safety = append(safety, a.Path)
			} else {
				regular = append(regular, a.Path)
			}
		}
		return regular, safety
	}

	// A fourth safety backup prunes the oldest.
	dir := t.TempDir()
	write(dir, now.Add(-48*time.Hour), false, 10)
	var safety []string
	for i := range 4 {
		safety = append(safety, write(dir, now.Add(time.Duration(i-4)*time.Hour), true, 10))
	}
	removed, err := Prune(dir, Retention{Daily: 7, Weekly: 4}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, left := names(dir); !slices.Equal(removed, safety[:1]) || len(left) != SafetyKeep || slices.Contains(left, safety[0]) {
		t.Errorf("safety backups: removed %v, left %v", removed, left)
	}
	// The one a restore uses is never removed, whatever its age.
	old := write(dir, now.Add(-10*24*time.Hour), true, 10)
	if _, err := prune(dir, Retention{Daily: 7, Weekly: 4}, now, old); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); err != nil {
		t.Error("prune removed the protected archive")
	}

	// Within the total: the oldest first; the newest backup stays even
	// when it alone is larger.
	dir = t.TempDir()
	var regular []string
	for d := range 5 {
		regular = append(regular, write(dir, now.Add(-time.Duration(d)*24*time.Hour), false, 100)) // newest first
	}
	sf := write(dir, now.Add(-time.Hour), true, 100)
	removed, err = Prune(dir, Retention{Daily: 7, Weekly: 4, MaxBytes: 250}, now)
	if err != nil {
		t.Fatal(err)
	}
	// The safety backup of an hour ago is the second newest.
	left, leftSafety := names(dir)
	if !slices.Equal(left, regular[:1]) || !slices.Equal(leftSafety, []string{sf}) || len(removed) != 4 {
		t.Errorf("within 250 bytes: kept %v and %v, removed %v", left, leftSafety, removed)
	}
	if _, err := Prune(dir, Retention{Daily: 7, Weekly: 4, MaxBytes: 1}, now); err != nil {
		t.Fatal(err)
	}
	if left, _ := names(dir); !slices.Equal(left, regular[:1]) {
		t.Errorf("the newest backup must stay: %v", left)
	}
	// A protected archive stays too, and counts.
	dir = t.TempDir()
	newest := write(dir, now, false, 100)
	mid := write(dir, now.Add(-24*time.Hour), false, 100)
	oldest := write(dir, now.Add(-48*time.Hour), false, 100)
	if _, err := prune(dir, Retention{Daily: 7, Weekly: 4, MaxBytes: 150}, now, oldest); err != nil {
		t.Fatal(err)
	}
	if left, _ := names(dir); !slices.Equal(left, []string{newest, oldest}) {
		t.Errorf("protected: kept %v (removed %s?)", left, mid)
	}

	// Decrypted copies: one a crash left a day ago goes, a fresh one stays.
	stale, fresh := filepath.Join(dir, ".decrypted-1"), filepath.Join(dir, ".decrypted-2")
	os.WriteFile(stale, []byte("x"), 0o600)
	os.WriteFile(fresh, []byte("x"), 0o600)
	os.Chtimes(stale, now.Add(-25*time.Hour), now.Add(-25*time.Hour))
	os.Chtimes(fresh, now.Add(-time.Hour), now.Add(-time.Hour))
	if _, err := Prune(dir, Retention{Daily: 7, Weekly: 4}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Error("a stale decrypted copy stayed")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("prune removed a decrypted copy in use")
	}

	// Busy while a backup or restore holds the lock.
	unlock, err := lock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Prune(dir, Retention{}, now); !errors.Is(err, ErrBusy) {
		t.Errorf("prune while locked: %v", err)
	}
	unlock()
	if removed, err := Prune(filepath.Join(dir, "missing"), Retention{}, now); err != nil || len(removed) != 0 {
		t.Errorf("a site without backups: %v %v", removed, err)
	}
	if a, b := Limits(0); a != 0 || b != 0 {
		t.Error("no quota, no limits")
	}
	if a, b := Limits(1 << 30); a != 1<<30 || b != 2<<30 {
		t.Errorf("limits of 1 GiB: %d %d", a, b)
	}
}

// TestRestoreLimits: a backup larger than the site may make is refused
// and recorded, a restore is bounded the same way and leaves no decrypted
// copy when it fails, and repeated restores keep SafetyKeep safety
// backups, never the archive restored.
func TestRestoreLimits(t *testing.T) {
	st := testutil.Store(t)
	ctx := context.Background()
	base := t.TempDir()
	up, dir := filepath.Join(base, "uploads"), filepath.Join(base, "backups")
	s := seed(t, st, up)
	o := siteOpts(dir, up, 0)
	o.Passphrase = "correct horse battery staple"
	clock := time.Date(2026, 10, 19, 3, 30, 0, 0, time.UTC)
	o.Now = func() time.Time { clock = clock.Add(time.Minute); return clock }
	est, err := Estimate(ctx, st, up)
	if err != nil {
		t.Fatal(err)
	}

	// An oversize backup is refused before anything is written.
	big := o
	big.MaxBytes = est - 1
	if _, _, err := Create(ctx, st, big); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversize backup: %v", err)
	}
	if list, _ := List(dir); len(list) != 0 {
		t.Errorf("an oversize backup was written: %v", list)
	}
	if stt, _ := ReadStatus(dir); stt.Result != ResultSkipped || stt.Reason != ReasonQuota {
		t.Errorf("status of an oversize backup: %+v", stt)
	}
	big.MaxBytes = est
	arch, _, err := Create(ctx, st, big)
	if err != nil {
		t.Fatal(err)
	}
	if stt, _ := ReadStatus(dir); stt.Result != ResultOK || stt.Reason != "" {
		t.Errorf("status: %+v", stt)
	}
	before := snapshot(t, st)

	decrypted := func(what string) {
		t.Helper()
		if left, _ := filepath.Glob(filepath.Join(dir, decryptedPattern)); len(left) != 0 {
			t.Errorf("%s: decrypted copies left: %v", what, left)
		}
	}
	ro := restoreOpts(o, s.admin, true)
	// A restore of an archive larger than the plan allows now fails after
	// decrypting it, and removes the copy.
	ro.MaxBytes = 1 << 10
	if _, err := Restore(ctx, st, arch.Path, ro); !errors.Is(err, ErrTooLarge) {
		t.Errorf("an archive larger than the plan: %v", err)
	}
	decrypted("too large")
	// So does a safety backup larger than the plan.
	ro.MaxBytes = est - 1
	if _, err := Restore(ctx, st, arch.Path, ro); !errors.Is(err, ErrTooLarge) {
		t.Errorf("a safety backup larger than the plan: %v", err)
	}
	decrypted("safety backup too large")
	// Without room for the decrypted copy, nothing is decrypted.
	ro.MaxBytes = 0
	ro.FreeSpace = func(string) (int64, error) { return arch.Size, nil }
	if _, err := Restore(ctx, st, arch.Path, ro); !errors.Is(err, ErrLowDisk) {
		t.Errorf("no room for the decrypted copy: %v", err)
	}
	decrypted("low disk")
	if !mapsEqual(snapshot(t, st), before) {
		t.Error("a refused restore changed the site")
	}
	if _, safety := countKinds(t, dir); safety != 0 {
		t.Errorf("refused restores left %d safety backups", safety)
	}

	// Four restores: the fourth safety backup prunes the oldest, and the
	// archive restored stays whatever the total.
	ro.FreeSpace = nil
	ro.Retention = &Retention{Daily: 7, Weekly: 4, MaxBytes: 1}
	var safeties []string
	for i := range 4 {
		rep, err := Restore(ctx, st, arch.Path, ro)
		if err != nil {
			t.Fatalf("restore %d: %v", i+1, err)
		}
		if rep.Safety == nil {
			t.Fatalf("restore %d made no safety backup", i+1)
		}
		safeties = append(safeties, rep.Safety.Path)
		decrypted("restore")
	}
	regular, safety := countKinds(t, dir)
	if regular != 1 || safety != 1 {
		// MaxBytes 1: only what the last restore used and made stays.
		t.Errorf("after four restores within 1 byte: %d regular, %d safety", regular, safety)
	}
	if _, err := os.Stat(arch.Path); err != nil {
		t.Error("the archive restored was pruned")
	}
	if _, err := os.Stat(safeties[3]); err != nil {
		t.Error("the latest safety backup was pruned")
	}
	ro.Retention = &Retention{Daily: 7, Weekly: 4}
	for i := range SafetyKeep {
		rep, err := Restore(ctx, st, arch.Path, ro)
		if err != nil {
			t.Fatalf("restore %d: %v", i+5, err)
		}
		safeties = append(safeties, rep.Safety.Path)
	}
	if _, safety := countKinds(t, dir); safety != SafetyKeep {
		t.Errorf("%d safety backups kept, want %d", safety, SafetyKeep)
	}
	for _, p := range safeties[len(safeties)-SafetyKeep:] {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("a recent safety backup was pruned: %s", filepath.Base(p))
		}
	}

	// Every restore was recorded, refused or not; a pace refuses the next
	// before anything is written.
	stt, err := ReadStatus(dir)
	if err != nil || len(stt.Restores) != 10 {
		t.Fatalf("restores recorded: %d %v", len(stt.Restores), err)
	}
	ro.Pace = Pace{Cooldown: time.Hour}
	if _, err := Restore(ctx, st, arch.Path, ro); !errors.Is(err, ErrTooSoon) {
		t.Errorf("a restore within the cooldown: %v", err)
	}
	if _, safety := countKinds(t, dir); safety != SafetyKeep {
		t.Errorf("a refused restore made a safety backup: %d", safety)
	}
	decrypted("too soon")
}

func TestPace(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	ago := func(m ...int) []time.Time {
		var out []time.Time
		for _, n := range m {
			out = append(out, now.Add(-time.Duration(n)*time.Minute))
		}
		return out
	}
	p := Pace{Cooldown: 10 * time.Minute, PerHour: 3}
	for _, c := range []struct {
		starts []time.Time
		want   time.Duration
	}{
		{nil, 0},
		{ago(4), 6 * time.Minute},
		{ago(10), 0},
		{ago(50, 30), 0},
		{ago(50, 30, 15), 10 * time.Minute}, // the one of 50 minutes ago leaves the hour
		{ago(70, 30, 15), 0},
		{ago(55, 40, 20, 3), 20 * time.Minute}, // two must leave the hour first
	} {
		if got := p.wait(c.starts, now); got != c.want {
			t.Errorf("wait after %v: %v, want %v", c.starts, got, c.want)
		}
	}
	if (Pace{}).wait(ago(0, 0, 0, 0), now) != 0 {
		t.Error("the zero pace allows any")
	}
}

func countKinds(t *testing.T, dir string) (regular, safety int) {
	t.Helper()
	list, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range list {
		if a.PreRestore {
			safety++
		} else {
			regular++
		}
	}
	return regular, safety
}
