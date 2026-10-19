//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package sitebackup

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// lock takes the site's backup lock: one backup or restore at a time per
// site, across processes (the server and the command line). It never
// waits: a held lock is ErrBusy.
func lock(dir string) (unlock func(), err error) {
	if err := ensureDir(dir); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrBusy
		}
		return nil, err
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
