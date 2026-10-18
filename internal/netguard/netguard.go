// Package netguard keeps requests the server makes on behalf of plugins,
// imports and AI providers away from internal networks (SSRF). It lists the
// address ranges that are never dialled and dials only allowed addresses,
// checking the address it actually connects to, which also defeats DNS
// rebinding.
package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"
)

// ErrForbidden is returned when no resolved address may be dialled.
var ErrForbidden = errors.New("destination address is not allowed")

// blocked are the ranges Public refuses on top of the netip predicates:
// private, shared, documentation, benchmarking and reserved ranges, and the
// IPv6 ranges that embed or translate to an IPv4 address (IPv4-mapped and
// -compatible, NAT64, 6to4, Teredo), which could reach an internal IPv4
// host through a gateway.
var blocked = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
		"172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16",
		"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
		"::/96", "::ffff:0:0/96", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64",
		"2001::/32", "2001:2::/48", "2001:10::/28", "2001:20::/28", "2001:db8::/32", "2002::/16",
		"3fff::/20", "5f00::/16", "fc00::/7", "fe80::/10", "fec0::/10", "ff00::/8",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// Public reports whether a is a public unicast address.
func Public(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsValid() || a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsInterfaceLocalMulticast() || a.IsMulticast() || a.IsUnspecified() {
		return false
	}
	for _, p := range blocked {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// Dialer returns a DialContext for http.Transport that resolves the host
// itself and connects only to addresses allow accepts (Public when nil).
// A nil resolver is net.DefaultResolver.
func Dialer(allow func(netip.Addr) bool, resolver *net.Resolver, timeout time.Duration) func(ctx context.Context, network, addr string) (net.Conn, error) {
	if allow == nil {
		allow = Public
	}
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ips, err := resolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		d := net.Dialer{Timeout: timeout}
		var lastErr error = ErrForbidden
		for _, ip := range ips {
			if !allow(ip) {
				continue
			}
			conn, err := d.DialContext(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		return nil, fmt.Errorf("%s: %w", host, lastErr)
	}
}
