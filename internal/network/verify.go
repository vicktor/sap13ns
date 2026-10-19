package network

// The DNS checks of custom domains. A custom domain is routed once its
// owner proves control of it and points it to the network:
//
//   - a TXT record _sap13ns-verify.<host> holds
//     "sap13ns-site-verification=<token>", the token of that domain on that
//     site;
//   - the host is a CNAME to SAP13NS_DOMAIN_TARGET (or to a host of the
//     network), or every A/AAAA address it has is one of the network's:
//     SAP13NS_DOMAIN_ADDRESSES, or the addresses of the target.
//
// DNS answers are untrusted: names are canonicalised before they are
// compared, only the exact record value counts, and nothing from an
// answer is stored or shown (check_error holds one of the Check* codes).
// A resolver failure (a timeout, SERVFAIL) is not an answer: it never
// counts towards a lapse.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Resolver is the DNS the network asks; *net.Resolver implements it, and
// tests use a fake.
type Resolver interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
	LookupCNAME(ctx context.Context, host string) (string, error)
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// SetResolver replaces the network's DNS resolver (net.DefaultResolver).
func (n *Network) SetResolver(r Resolver) { n.resolver = r }

// The outcomes of a DNS check, stored in domains.check_error.
const (
	CheckOK      = ""
	CheckTXT     = "txt_missing"  // no TXT record with the expected value
	CheckAddress = "not_pointing" // the host does not resolve to the network
	CheckDNS     = "dns_error"    // the resolver failed: try again later
)

const (
	// VerifyPrefix is the label of the TXT record under the host.
	VerifyPrefix = "_sap13ns-verify."
	// VerifyValuePrefix starts the value of that record.
	VerifyValuePrefix = "sap13ns-site-verification="

	dnsTimeout = 10 * time.Second
	maxTXT     = 64 // records of the verification name looked at
	maxAddrs   = 32
)

// TXTName is the name of the verification record of a host.
func TXTName(host string) string { return VerifyPrefix + host }

// TXTValue is the value of the verification record for a token.
func TXTValue(token string) string { return VerifyValuePrefix + token }

// DomainCheck is the outcome of a DNS check of a custom domain.
type DomainCheck struct {
	Problem string // CheckOK or one of the other Check* codes
}

func (c DomainCheck) OK() bool { return c.Problem == CheckOK }

// Temporary reports a failure of the resolver rather than an answer.
func (c DomainCheck) Temporary() bool { return c.Problem == CheckDNS }

// domainToken is the verification token of a host on a site: derived, so
// the same site always gets the same record for the same host (an owner
// who set it up once can claim the host again), and different on every
// site (a record proves control for one site only).
func (n *Network) domainToken(siteID int64, host string) string {
	m := hmac.New(sha256.New, n.domainKey)
	m.Write([]byte(strconv.FormatInt(siteID, 10) + "|" + host))
	return hex.EncodeToString(m.Sum(nil)[:16])
}

// notFound reports whether a lookup error is a definitive "no such name or
// record" answer, as opposed to a failure to get an answer.
func notFound(err error) bool {
	var d *net.DNSError
	return errors.As(err, &d) && d.IsNotFound
}

// checkDNS checks the verification record of token and the address of
// host.
func (n *Network) checkDNS(ctx context.Context, host, token string) DomainCheck {
	ctx, cancel := context.WithTimeout(ctx, dnsTimeout)
	defer cancel()
	if ok, err := n.hasTXT(ctx, host, token); err != nil {
		return DomainCheck{Problem: CheckDNS}
	} else if !ok {
		return DomainCheck{Problem: CheckTXT}
	}
	ok, err := n.pointsHere(ctx, host)
	switch {
	case err != nil:
		return DomainCheck{Problem: CheckDNS}
	case !ok:
		return DomainCheck{Problem: CheckAddress}
	}
	return DomainCheck{}
}

// hasTXT reports whether the verification name of host has a TXT record
// whose value is exactly the one of token. A name without records is
// false, nil; a failed lookup is an error.
func (n *Network) hasTXT(ctx context.Context, host, token string) (bool, error) {
	txts, err := n.resolver.LookupTXT(ctx, TXTName(host))
	if err != nil {
		if notFound(err) {
			return false, nil
		}
		return false, err
	}
	want := TXTValue(token)
	for i, v := range txts {
		if i >= maxTXT {
			break
		}
		if strings.TrimSpace(v) == want {
			return true, nil
		}
	}
	return false, nil
}

// pointsHere reports whether host reaches the network: a CNAME (chain)
// ending at the target or at a host of the network, or only addresses of
// the network.
func (n *Network) pointsHere(ctx context.Context, host string) (bool, error) {
	if cname, err := n.resolver.LookupCNAME(ctx, host); err == nil {
		if c, err := CanonicalHost(strings.ToLower(cname)); err == nil && c != host && n.ours(c) {
			return true, nil
		}
	}
	addrs, err := n.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		if notFound(err) {
			return false, nil
		}
		return false, err
	}
	if len(addrs) == 0 || len(addrs) > maxAddrs {
		return false, nil
	}
	allowed := slices.Clone(n.targetAddrs)
	if len(allowed) == 0 {
		if allowed, err = n.resolver.LookupNetIP(ctx, "ip", n.target); err != nil {
			return false, err // the network's own name must resolve
		}
	}
	for i := range allowed {
		allowed[i] = allowed[i].Unmap()
	}
	for _, a := range addrs {
		if !slices.Contains(allowed, a.Unmap()) {
			return false, nil
		}
	}
	return true, nil
}

// ours reports whether host is the target or a host of the network.
func (n *Network) ours(host string) bool {
	return host == n.target || under(host, n.sitesDomain) || under(host, n.baseHost)
}
