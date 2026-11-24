package license

import (
	"crypto/ed25519"
	"errors"
	"io"
	"io/fs"
	"os"
	"sync"
	"time"
)

// DefaultPath is where an install looks for its licence when
// SAP13NS_LICENSE_FILE is not set: next to its environment file.
const DefaultPath = "/etc/sap13ns/sap13ns.license"

// ReadFile reads and checks the licence at path. A missing file returns an
// error that matches fs.ErrNotExist.
func ReadFile(path string) (*License, error) { return readFile(path, nil) }

func readFile(path string, key ed25519.PublicKey) (*License, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxSize+1))
	if err != nil {
		return nil, err
	}
	return Parse(data, key)
}

// File is the licence of a running install. It reads the file again at
// most once a minute when it changed, so that a new or renewed licence is
// picked up without a restart, and a removed one stops counting.
type File struct {
	path string
	// OnChange, if set, is told each new state: the licence, or the
	// reason there is none (nil, nil when the file is absent).
	OnChange func(*License, error)
	// Key checks the signature; nil is the licensor's key (PublicKey).
	// Only tests set another.
	Key ed25519.PublicKey

	mu      sync.Mutex
	checked time.Time
	mod     time.Time
	size    int64
	lic     *License
}

// recheck is how often the file is looked at again.
const recheck = time.Minute

// NewFile returns the licence at path; "" or "off" returns nil, which
// holds no licence.
func NewFile(path string) *File {
	if path == "" || path == "off" {
		return nil
	}
	return &File{path: path}
}

// Path is the file's path.
func (f *File) Path() string { return f.path }

// Current returns the licence the file holds now, or nil: absent, not
// valid, or not signed by the licensor. A nil File holds none.
func (f *File) Current(now time.Time) *License {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.checked.IsZero() && now.Sub(f.checked) < recheck && now.After(f.checked) {
		return f.lic
	}
	f.checked = now
	st, err := os.Stat(f.path)
	if err != nil {
		if f.lic != nil || f.size != -1 {
			f.lic, f.mod, f.size = nil, time.Time{}, -1
			if !errors.Is(err, fs.ErrNotExist) {
				f.notify(nil, err)
			} else {
				f.notify(nil, nil)
			}
		}
		return nil
	}
	if st.ModTime().Equal(f.mod) && st.Size() == f.size {
		return f.lic
	}
	f.mod, f.size = st.ModTime(), st.Size()
	f.lic, err = readFile(f.path, f.Key)
	f.notify(f.lic, err)
	return f.lic
}

func (f *File) notify(l *License, err error) {
	if f.OnChange != nil {
		f.OnChange(l, err)
	}
}
