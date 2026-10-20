package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Updates from the admin. The web server cannot replace its own binary (it
// runs unprivileged, with the system read-only), and must not be able to.
// It only asks: it creates RequestFile, which starts sap13ns-update.path,
// and the root service it triggers removes the file, then runs
//
//	sap13ns update -yes -status StatusDir -backup … -restart sap13ns.service
//
// That installs the latest signed release, as an operator would, whatever
// the request file holds: the web server can ask for an update, never
// choose what is installed. The service writes its progress to StatusDir,
// which only root can write and the web server reads.

const (
	RequestFile = "/var/lib/sap13ns/update-request"
	StatusDir   = "/var/lib/sap13ns-update"
	PathUnit    = "/etc/systemd/system/sap13ns-update.path" // installed by deploy/setup.sh
	statusFile  = "status.json"
)

// Progress of an update started from the admin.
const (
	StateRunning   = "running"   // backing up or installing
	StateInstalled = "installed" // the release is installed and the service restarted
	StateCurrent   = "current"   // nothing newer to install
	StateFailed    = "failed"
)

// Status is what the update service last did.
type Status struct {
	State string    `json:"state"`
	From  string    `json:"from,omitempty"` // version before
	To    string    `json:"to,omitempty"`   // version installed, or being installed
	Error string    `json:"error,omitempty"`
	At    time.Time `json:"at"`
}

// Updater reports whether the update service is installed: its path unit
// is, or its status directory exists. systemd creates that directory only
// the first time the service runs, so the unit alone has to count.
func Updater(dir, unit string) bool {
	if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
		return true
	}
	fi, err := os.Stat(unit)
	return err == nil && fi.Mode().IsRegular()
}

// Request asks the update service for an update. by says who asked, for
// the log; the service does not read it.
func Request(path, by string, now time.Time) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(f, "requested by %s at %s\n", by, now.UTC().Format(time.RFC3339))
	return errors.Join(err, f.Close())
}

// Requested reports whether a request waits for the update service.
func Requested(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// ReadStatus returns the last status in dir, nil if there is none.
func ReadStatus(dir string) (*Status, error) {
	f, err := os.Open(filepath.Join(dir, statusFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 8<<10))
	if err != nil {
		return nil, err
	}
	var st Status
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fmt.Errorf("%s: %w", f.Name(), err)
	}
	return &st, nil
}

// WriteStatus replaces the status in dir atomically, readable by all.
func WriteStatus(dir string, st Status) error {
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".status-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := errors.Join(tmp.Chmod(0o644), tmp.Close()); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, statusFile))
}
