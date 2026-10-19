package autopost

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode"

	"github.com/vicktor/sap13ns/pluginsdk"
)

// FetchProblem classifies the outcome of a request. It returns false when
// the request succeeded (a 2xx answer). Otherwise the Result says why: a
// response with an error status, or an error raised before anything was
// sent, is safe to retry (Failed); any other error, such as a timeout,
// leaves the outcome unknown. base is the URL the request went to, and
// hint is appended to the explanation of a host the plugin may not reach.
func FetchProblem(resp *pluginsdk.HTTPResponse, err error, base, hint string) (Result, bool) {
	switch {
	case errors.Is(err, pluginsdk.ErrDenied):
		h := HostOf(base)
		return Result{Failed: true, Detail: "the host " + h + " is not in this plugin's permissions (http:" + h + ")" + hint}, true
	case errors.Is(err, pluginsdk.ErrLimit), errors.Is(err, pluginsdk.ErrInvalid), errors.Is(err, pluginsdk.ErrUnavailable):
		return Result{Failed: true, Detail: err.Error()}, true
	case err != nil:
		msg := err.Error()
		if notSent(msg) {
			return Result{Failed: true, Detail: Shorten(msg, 160)}, true
		}
		return Result{Detail: "no answer (" + Shorten(msg, 120) + ")"}, true
	case resp == nil:
		return Result{Detail: "no answer"}, true
	case resp.Status < 200 || resp.Status > 299:
		return Result{Failed: true, Detail: "HTTP " + strconv.Itoa(resp.Status) + ErrorText(resp.Body)}, true
	}
	return Result{}, false
}

// notSent recognizes errors raised before the request reached the server.
func notSent(msg string) bool {
	for _, s := range []string{"no such host", "connection refused", "destination address is not allowed",
		"only absolute https://", "redirect to a host", "method not allowed", "request body too large", "network is unreachable"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// ErrorText extracts the message of a JSON error answer, as ": message",
// never the whole body (it could echo what was sent). It understands the
// shapes the networks use:
//
//	{"error": "...", "message": "..."}                     Bluesky, Mastodon
//	{"title": "...", "detail": "...", "type": "..."}       X (problem details)
//	{"errors": [{"message": "..."}]}                       X
//	{"message": "...", "serviceErrorCode": 100}            LinkedIn
//	{"error": {"message": "...", "error_user_msg": "..."}} Meta
//	{"error": "...", "error_description": "..."}           OAuth 2
func ErrorText(body string) string {
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(body), &m) != nil {
		return ""
	}
	var parts []string
	add := func(vals ...string) {
		for _, s := range vals {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			dup := false
			for _, p := range parts {
				dup = dup || strings.EqualFold(p, s)
			}
			if !dup {
				parts = append(parts, s)
			}
		}
	}
	str := func(k string) string {
		var s string
		json.Unmarshal(m[k], &s)
		return s
	}
	if raw := m["error"]; len(raw) > 0 && raw[0] == '{' {
		var e struct {
			Message string `json:"message"`
			UserMsg string `json:"error_user_msg"`
			Code    int    `json:"code"`
			Sub     int    `json:"error_subcode"`
		}
		json.Unmarshal(raw, &e)
		add(e.Message, e.UserMsg)
		if e.Code != 0 {
			code := "code " + strconv.Itoa(e.Code)
			if e.Sub != 0 {
				code += "/" + strconv.Itoa(e.Sub)
			}
			add("(" + code + ")")
		}
	} else {
		add(str("error"))
	}
	// "detail" only in problem details (RFC 9457), which always have a
	// title: elsewhere it may echo what was sent.
	if title := str("title"); title != "" {
		add(title, str("detail"))
	}
	add(str("message"), str("error_description"))
	var errs []struct {
		Message string `json:"message"`
		Detail  string `json:"detail"`
	}
	if json.Unmarshal(m["errors"], &errs) == nil {
		for i, e := range errs {
			if i == 2 {
				break
			}
			add(e.Message, e.Detail)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return ": " + Shorten(strings.Join(parts, ": "), 200)
}

// Shorten replaces control characters with spaces and cuts s to n runes.
func Shorten(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
