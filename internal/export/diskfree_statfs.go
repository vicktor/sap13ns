//go:build linux || darwin

package export

import "syscall"

// FreeSpace is the space an unprivileged process may still write on the
// file system of dir, or -1 when the system cannot tell.
func FreeSpace(dir string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}
