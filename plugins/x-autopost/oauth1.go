package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// OAuth1 holds the four credentials of an X app acting for its own
// account: the app's consumer key and secret, and the account's access
// token and secret (OAuth 1.0a user context).
type OAuth1 struct {
	ConsumerKey, ConsumerSecret string
	Token, TokenSecret          string
}

// Header returns the Authorization header of a request, signed with
// HMAC-SHA1 as RFC 5849 §3.4 describes. params are the request's query and
// form parameters; a JSON body is not signed, as X documents for the v2
// API.
func (o OAuth1) Header(method, rawURL string, params map[string]string, nonce string, at time.Time) string {
	oauth := map[string]string{
		"oauth_consumer_key":     o.ConsumerKey,
		"oauth_nonce":            nonce,
		"oauth_signature_method": "HMAC-SHA1",
		"oauth_timestamp":        strconv.FormatInt(at.Unix(), 10),
		"oauth_token":            o.Token,
		"oauth_version":          "1.0",
	}
	all := map[string]string{}
	for k, v := range params {
		all[k] = v
	}
	for k, v := range oauth {
		all[k] = v
	}
	oauth["oauth_signature"] = o.Signature(method, rawURL, all)

	keys := make([]string, 0, len(oauth))
	for k := range oauth {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, encode(k)+`="`+encode(oauth[k])+`"`)
	}
	return "OAuth " + strings.Join(parts, ", ")
}

// Signature is the base64 HMAC-SHA1 of the signature base string, keyed
// with the consumer secret and the token secret. params include the
// oauth_* parameters and must not include oauth_signature.
func (o OAuth1) Signature(method, rawURL string, params map[string]string) string {
	base := strings.ToUpper(method) + "&" + encode(baseURI(rawURL)) + "&" + encode(normalize(rawURL, params))
	mac := hmac.New(sha1.New, []byte(encode(o.ConsumerSecret)+"&"+encode(o.TokenSecret)))
	mac.Write([]byte(base))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// baseURI is the URL without query or fragment, with a lower-case scheme
// and host and without a default port (RFC 5849 §3.4.1.2).
func baseURI(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	host := strings.ToLower(u.Host)
	host = strings.TrimSuffix(host, ":443")
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	return strings.ToLower(u.Scheme) + "://" + host + path
}

// normalize encodes every parameter (the query's and params), sorts them
// by name, then value, and joins them (RFC 5849 §3.4.1.3.2).
func normalize(rawURL string, params map[string]string) string {
	type kv struct{ k, v string }
	var pairs []kv
	if u, err := url.Parse(rawURL); err == nil {
		for k, vs := range u.Query() {
			for _, v := range vs {
				pairs = append(pairs, kv{encode(k), encode(v)})
			}
		}
	}
	for k, v := range params {
		pairs = append(pairs, kv{encode(k), encode(v)})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].k != pairs[j].k {
			return pairs[i].k < pairs[j].k
		}
		return pairs[i].v < pairs[j].v
	})
	out := make([]string, len(pairs))
	for i, p := range pairs {
		out[i] = p.k + "=" + p.v
	}
	return strings.Join(out, "&")
}

// encode is the percent-encoding of RFC 3986 that OAuth 1.0 requires:
// everything but ALPHA, DIGIT, "-", ".", "_" and "~", with upper-case hex
// digits (so a space is %20, never "+").
func encode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteString("%" + strings.ToUpper(hex.EncodeToString([]byte{c})))
	}
	return b.String()
}

// newNonce is 32 random bytes, hex encoded.
var newNonce = func() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}
