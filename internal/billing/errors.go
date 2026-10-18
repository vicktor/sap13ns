package billing

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// Error is an error response from the Stripe API. Message is redacted so it
// never carries API keys or webhook secrets.
type Error struct {
	// Type is one of api_error, card_error, idempotency_error,
	// invalid_request_error (or empty when the body was not Stripe JSON).
	Type string
	// Code is the programmatic error code, for example resource_missing.
	Code string
	// DeclineCode is set for card declines.
	DeclineCode string
	Message     string
	// Param names the offending request parameter, e.g. line_items[0][price].
	Param      string
	DocURL     string
	StatusCode int
	// RequestID is the Request-Id response header, useful in Stripe support
	// tickets and the Workbench request log.
	RequestID string
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("stripe: ")
	if e.Type != "" {
		b.WriteString(e.Type)
		b.WriteString(": ")
	}
	if e.Message != "" {
		b.WriteString(e.Message)
	} else {
		b.WriteString(http.StatusText(e.StatusCode))
	}
	var extra []string
	if e.Code != "" {
		extra = append(extra, "code="+e.Code)
	}
	if e.DeclineCode != "" {
		extra = append(extra, "decline_code="+e.DeclineCode)
	}
	if e.Param != "" {
		extra = append(extra, "param="+e.Param)
	}
	extra = append(extra, fmt.Sprintf("status=%d", e.StatusCode))
	if e.RequestID != "" {
		extra = append(extra, "request_id="+e.RequestID)
	}
	b.WriteString(" (")
	b.WriteString(strings.Join(extra, ", "))
	b.WriteString(")")
	return b.String()
}

// IsNotFound reports whether err is a Stripe 404 / resource_missing error.
func IsNotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && (e.StatusCode == http.StatusNotFound || e.Code == "resource_missing")
}

// IsIdempotencyError reports whether an Idempotency-Key was reused with
// different parameters. Generate a fresh key and retry in that case.
func IsIdempotencyError(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Type == "idempotency_error"
}

func parseError(resp *http.Response, body []byte, key string) *Error {
	e := &Error{
		StatusCode: resp.StatusCode,
		RequestID:  sanitizeHeader(resp.Header.Get("Request-Id")),
	}
	var env struct {
		Error *struct {
			Type        string `json:"type"`
			Code        string `json:"code"`
			DeclineCode string `json:"decline_code"`
			Message     string `json:"message"`
			Param       string `json:"param"`
			DocURL      string `json:"doc_url"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &env) == nil && env.Error != nil {
		e.Type = env.Error.Type
		e.Code = env.Error.Code
		e.DeclineCode = env.Error.DeclineCode
		e.Message = redact(truncate(env.Error.Message, 1000), key)
		e.Param = redact(truncate(env.Error.Param, 200), key)
		e.DocURL = env.Error.DocURL
		return e
	}
	// Not Stripe JSON: a proxy page, a redirect, an empty body.
	e.Type = "api_error"
	switch {
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		e.Message = "unexpected redirect from Stripe API"
	default:
		e.Message = "unexpected non-JSON error response"
	}
	return e
}

var secretPattern = regexp.MustCompile(`\b(sk|rk|pk)_(live|test)_[0-9A-Za-z*]+|\bwhsec_[0-9A-Za-z+/=*]+`)

// redact removes the client's own key and anything shaped like a Stripe key
// or webhook secret.
func redact(s, key string) string {
	if key != "" {
		s = strings.ReplaceAll(s, key, "[REDACTED]")
	}
	return secretPattern.ReplaceAllStringFunc(s, func(m string) string {
		if strings.HasPrefix(m, "whsec_") {
			return "whsec_[REDACTED]"
		}
		parts := strings.SplitN(m, "_", 3)
		return parts[0] + "_" + parts[1] + "_[REDACTED]"
	})
}

// redactErr returns err unchanged unless its message would leak a secret.
func redactErr(err error, key string) error {
	msg := err.Error()
	if r := redact(msg, key); r != msg {
		return errors.New(r)
	}
	return err
}

func sanitizeHeader(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f {
			return -1
		}
		return r
	}, s)
	return truncate(s, 100)
}
