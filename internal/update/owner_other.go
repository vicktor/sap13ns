//go:build !unix

package update

import (
	"io/fs"
	"os"
)

func preserveOwner(*os.File, fs.FileInfo) error { return nil }
