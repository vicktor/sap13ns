// Package bundled embeds the packages of the official plugins, so that
// they ship inside the binary and travel with `sap13ns update`.
//
// The packages are generated, not committed: `make plugins` (which `make
// build` and the release workflow run first) builds every official plugin
// under plugins/ and packs it into packs/<id>.zip with the packer of
// `sap13ns plugin pack`. A plain `go build` from a fresh clone embeds only
// packs/README.md, and the binary offers no official plugins.
package bundled

import (
	"embed"
	"io/fs"
)

//go:embed packs
var packs embed.FS

// FS is the directory of the packages (plugin.LoadOfficial reads its .zip
// files).
func FS() fs.FS {
	sub, err := fs.Sub(packs, "packs")
	if err != nil {
		panic(err) // a constant, valid directory name
	}
	return sub
}
