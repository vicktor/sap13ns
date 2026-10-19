# Official plugin packages

`make plugins` builds every official plugin under `plugins/` (not the
skeleton) and packs it here as `<id>.zip`, with the packer of `sap13ns plugin
pack`, after scanning it as an upload would be scanned. The binary embeds this
directory (`internal/plugin/bundled`), and **Plugins → Official plugins**
offers the packages to install.

The packages are generated and ignored by git. A plain `go build` from a fresh
clone embeds only this file, and the binary then offers no official plugins.
`make build` and the release workflow run `make plugins` first.
