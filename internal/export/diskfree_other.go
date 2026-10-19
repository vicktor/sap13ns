//go:build !linux && !darwin

package export

// FreeSpace is the space an unprivileged process may still write on the
// file system of dir, or -1 when the system cannot tell, as here.
func FreeSpace(dir string) (int64, error) { return -1, nil }
