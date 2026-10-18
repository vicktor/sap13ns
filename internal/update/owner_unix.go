//go:build unix

package update

import (
	"io/fs"
	"os"
	"syscall"
)

// preserveOwner gives f the owner and group of fi when running as root (a
// sudo update would otherwise leave a root-owned binary). Other users can
// only create files they own, so nothing is changed for them.
func preserveOwner(f *os.File, fi fs.FileInfo) error {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || os.Geteuid() != 0 {
		return nil
	}
	return f.Chown(int(st.Uid), int(st.Gid))
}
