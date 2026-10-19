package network

import (
	"context"
	"net"
	"net/netip"
	"slices"
	"sync"
)

// fakeDNS is the resolver of the tests: no test asks a real DNS server.
// A name without records is a "not found" answer; a name in fail makes
// the resolver fail (a timeout, SERVFAIL).
type fakeDNS struct {
	mu    sync.Mutex
	txt   map[string][]string
	cname map[string]string
	addrs map[string][]netip.Addr
	fail  map[string]bool
}

// The addresses of the network's own name (sap13ns.site) in the tests.
var (
	netAddr4 = netip.MustParseAddr("192.0.2.80")
	netAddr6 = netip.MustParseAddr("2001:db8::80")
)

func newFakeDNS() *fakeDNS {
	return &fakeDNS{
		txt:   map[string][]string{},
		cname: map[string]string{},
		addrs: map[string][]netip.Addr{"sap13ns.site": {netAddr4, netAddr6}},
		fail:  map[string]bool{},
	}
}

func notFoundErr(name string) error {
	return &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func failErr(name string) error {
	return &net.DNSError{Err: "server misbehaving", Name: name, IsTemporary: true}
}

func (f *fakeDNS) LookupTXT(_ context.Context, name string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail[name] {
		return nil, failErr(name)
	}
	if v, ok := f.txt[name]; ok {
		return slices.Clone(v), nil
	}
	return nil, notFoundErr(name)
}

// LookupCNAME answers like net.Resolver: the canonical name, with a
// trailing dot, which is the host itself when it has no CNAME.
func (f *fakeDNS) LookupCNAME(_ context.Context, host string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail[host] {
		return "", failErr(host)
	}
	if c, ok := f.cname[host]; ok {
		return c + ".", nil
	}
	if _, ok := f.addrs[host]; ok {
		return host + ".", nil
	}
	return "", notFoundErr(host)
}

func (f *fakeDNS) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for range 8 { // follow CNAMEs
		if f.fail[host] {
			return nil, failErr(host)
		}
		if a, ok := f.addrs[host]; ok {
			return slices.Clone(a), nil
		}
		c, ok := f.cname[host]
		if !ok {
			break
		}
		host = c
	}
	return nil, notFoundErr(host)
}

// set changes the records under the lock.
func (f *fakeDNS) set(change func(*fakeDNS)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f)
}

// point gives host the verification record of token and a CNAME to the
// network.
func (f *fakeDNS) point(host, token string) {
	f.set(func(f *fakeDNS) {
		f.txt[TXTName(host)] = []string{TXTValue(token)}
		f.cname[host] = "sap13ns.site"
	})
}

// clear removes every record of host.
func (f *fakeDNS) clear(host string) {
	f.set(func(f *fakeDNS) {
		delete(f.txt, TXTName(host))
		delete(f.cname, host)
		delete(f.addrs, host)
		delete(f.fail, host)
		delete(f.fail, TXTName(host))
	})
}
