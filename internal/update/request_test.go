package update

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRequestAndStatus(t *testing.T) {
	dir := t.TempDir()
	req := filepath.Join(dir, "update-request")
	if Requested(req) {
		t.Fatal("a request before any")
	}
	if err := Request(req, "admin", time.Now()); err != nil {
		t.Fatal(err)
	}
	if !Requested(req) {
		t.Fatal("the request is not there")
	}

	status, unit := filepath.Join(dir, "status"), filepath.Join(dir, "sap13ns-update.path")
	if Updater(status, unit) {
		t.Fatal("an updater without its directory or unit")
	}
	// Installed but never run: the unit only.
	os.WriteFile(unit, []byte("[Path]\n"), 0o644)
	if !Updater(status, unit) {
		t.Fatal("the installed unit is not seen")
	}
	os.Remove(unit)
	os.Mkdir(status, 0o755)
	if !Updater(status, unit) {
		t.Fatal("the updater is not seen")
	}
	if st, err := ReadStatus(status); st != nil || err != nil {
		t.Fatalf("status before any: %+v %v", st, err)
	}
	at := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	if err := WriteStatus(status, Status{State: StateFailed, From: "0.2.1", To: "0.2.2", Error: "boom", At: at}); err != nil {
		t.Fatal(err)
	}
	st, err := ReadStatus(status)
	if err != nil || st.State != StateFailed || st.Error != "boom" || !st.At.Equal(at) {
		t.Fatalf("status: %+v %v", st, err)
	}
	if fi, _ := os.Stat(filepath.Join(status, statusFile)); fi.Mode().Perm() != 0o644 {
		t.Errorf("status mode %v", fi.Mode().Perm())
	}
	if left, _ := filepath.Glob(filepath.Join(status, ".status-*")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}
