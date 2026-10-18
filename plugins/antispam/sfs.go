package main

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strings"

	"github.com/vicktor/sap13ns/pluginsdk"
)

// sfsAPI is the StopForumSpam lookup API (https://www.stopforumspam.com/usage).
const sfsAPI = "https://api.stopforumspam.org/api"

// SFSQuery builds the lookup URL: the IP address and the MD5 hash of the
// lowercased e-mail address, never the name, the content or the address
// itself. ok is false when there is nothing to ask about.
func SFSQuery(cfg Config, ip, email string) (string, bool) {
	q := url.Values{}
	if cfg.SFSIP {
		if a, err := netip.ParseAddr(strings.TrimSpace(ip)); err == nil && publicAddr(a) {
			q.Set("ip", a.Unmap().String())
		}
	}
	if cfg.SFSEmail {
		if e := strings.ToLower(strings.TrimSpace(email)); strings.Contains(e, "@") {
			sum := md5.Sum([]byte(e))
			q.Set("emailhash", hex.EncodeToString(sum[:]))
		}
	}
	if len(q) == 0 {
		return "", false
	}
	return sfsAPI + "?json&" + q.Encode(), true
}

// publicAddr leaves out addresses StopForumSpam cannot know: loopback,
// private networks and the like (a site on a LAN or behind a
// misconfigured proxy).
func publicAddr(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsGlobalUnicast() && !a.IsPrivate()
}

// sfsAnswer is the JSON answer of the API.
type sfsAnswer struct {
	Success   int       `json:"success"`
	Error     string    `json:"error"`
	IP        *sfsField `json:"ip"`
	EmailHash *sfsField `json:"emailhash"`
}

type sfsField struct {
	Appears    flexNum `json:"appears"`
	Frequency  flexNum `json:"frequency"`
	Confidence flexNum `json:"confidence"`
}

// flexNum accepts 12.5, "12.5" and true (1).
type flexNum float64

func (f *flexNum) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	switch s {
	case "true":
		*f = 1
	case "false", "", "null":
		*f = 0
	default:
		var n float64
		if err := json.Unmarshal([]byte(s), &n); err != nil {
			return err
		}
		*f = flexNum(n)
	}
	return nil
}

// errSFS is a failed lookup. Its message never contains the request URL,
// which carries the IP address.
type errSFS string

func (e errSFS) Error() string { return "StopForumSpam: " + string(e) }

// AskSFS looks the commenter up. It returns no signal when they are not
// listed, and an error when the lookup failed (the comment is then judged
// by the local checks alone).
func AskSFS(cfg Config, ip, email string) (*Signal, error) {
	u, ok := SFSQuery(cfg, ip, email)
	if !ok {
		return nil, nil
	}
	resp, err := pluginsdk.Fetch(pluginsdk.HTTPRequest{Method: "GET", URL: u, Headers: map[string]string{"Accept": "application/json"}})
	switch {
	case errors.Is(err, pluginsdk.ErrDenied):
		return nil, errSFS("the request was denied (is the http:api.stopforumspam.org permission granted?)")
	case errors.Is(err, pluginsdk.ErrLimit):
		return nil, errSFS("request limit reached")
	case err != nil:
		msg := err.Error()
		if strings.Contains(msg, "deadline") || strings.Contains(msg, "timeout") || strings.Contains(msg, "canceled") {
			return nil, errSFS("no answer in time")
		}
		return nil, errSFS("network error")
	case resp.Status != 200:
		return nil, errSFS(fmt.Sprintf("HTTP %d", resp.Status))
	}
	var a sfsAnswer
	if json.Unmarshal([]byte(resp.Body), &a) != nil || a.Success != 1 {
		return nil, errSFS("unexpected answer")
	}
	var listed []string
	best := 0.0
	for _, f := range []struct {
		name  string
		field *sfsField
	}{{"IP address", a.IP}, {"e-mail address", a.EmailHash}} {
		if f.field == nil || f.field.Appears == 0 {
			continue
		}
		listed = append(listed, fmt.Sprintf("%s (confidence %.0f%%, seen %.0f times)", f.name, float64(f.field.Confidence), float64(f.field.Frequency)))
		best = max(best, float64(f.field.Confidence))
	}
	if len(listed) == 0 {
		return nil, nil
	}
	v := verdictHold
	if best >= cfg.SFSThreshold {
		v = verdictSpam
	}
	return &Signal{Verdict: v, Text: "listed by StopForumSpam: " + strings.Join(listed, " and ")}, nil
}
