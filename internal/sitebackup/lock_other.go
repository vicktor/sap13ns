//go:build !(linux || darwin || freebsd || openbsd || netbsd || dragonfly)

package sitebackup

import "sync"

var (
	locksMu sync.Mutex
	locks   = map[string]bool{}
)

// lock takes the site's backup lock within this process (the systems
// without flock): one backup or restore at a time per site.
func lock(dir string) (unlock func(), err error) {
	if err := ensureDir(dir); err != nil {
		return nil, err
	}
	locksMu.Lock()
	defer locksMu.Unlock()
	if locks[dir] {
		return nil, ErrBusy
	}
	locks[dir] = true
	return func() {
		locksMu.Lock()
		delete(locks, dir)
		locksMu.Unlock()
	}, nil
}
