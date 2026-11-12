package network

// The domains file (SAP13NS_DOMAINS_FILE) lists the custom domains the
// network routes, one per line, sorted. The registry rewrites it whenever
// a host table reload changes that list. It is how a proxy that cannot
// ask for certificates on demand learns about them: with nginx, the root
// helper deploy/sap13ns-domains watches the file (a systemd path unit),
// gets a certificate for each new domain and writes its server block, and
// removes both when a domain goes. The web process itself never touches
// nginx or the certificates.

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
)

// customHosts are the routed hosts that are neither the portal's nor
// under SAP13NS_SITES_DOMAIN: the ones the wildcard certificate does not
// cover.
func (n *Network) customHosts(t *hostTable) []string {
	var out []string
	for h := range t.hosts {
		if !under(h, n.sitesDomain) && !under(h, n.baseHost) {
			out = append(out, h)
		}
	}
	slices.Sort(out)
	return out
}

// writeDomainsFile writes the domains file when its content changed since
// the last write of this process (always the first time), atomically.
func (r *Registry) writeDomainsFile(t *hostTable) {
	path := r.cfg.DomainsFile
	if path == "" {
		return
	}
	var b bytes.Buffer
	for _, h := range r.net.customHosts(t) {
		b.WriteString(h)
		b.WriteByte('\n')
	}
	r.domainsMu.Lock()
	defer r.domainsMu.Unlock()
	if r.domainsWritten != nil && bytes.Equal(*r.domainsWritten, b.Bytes()) {
		return
	}
	if err := writeFileAtomic(path, b.Bytes()); err != nil {
		r.log.Error("network: writing the domains file", "path", path, "err", err)
		return
	}
	data := b.Bytes()
	r.domainsWritten = &data
}

func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // after a successful rename it is gone
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
