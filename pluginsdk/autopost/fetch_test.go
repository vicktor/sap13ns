package autopost

import (
	"errors"
	"strings"
	"testing"

	"github.com/vicktor/sap13ns/pluginsdk"
)

func TestFetchProblem(t *testing.T) {
	ok := &pluginsdk.HTTPResponse{Status: 201, Body: "{}"}
	tests := []struct {
		name   string
		resp   *pluginsdk.HTTPResponse
		err    error
		bad    bool
		failed bool
		detail string
	}{
		{"success", ok, nil, false, false, ""},
		{"denied host", nil, pluginsdk.ErrDenied, true, true, "the host mastodon.example is not in this plugin's permissions (http:mastodon.example); see the README"},
		{"call limit", nil, pluginsdk.ErrLimit, true, true, ""},
		{"DNS failure", &pluginsdk.HTTPResponse{}, errors.New(`dial tcp: lookup mastodon.example: no such host`), true, true, "no such host"},
		{"private address", &pluginsdk.HTTPResponse{}, errors.New(`dial tcp 10.0.0.1:443: destination address is not allowed`), true, true, "not allowed"},
		{"timeout is unknown", &pluginsdk.HTTPResponse{}, errors.New(`context deadline exceeded`), true, false, "no answer (context deadline exceeded)"},
		{"unauthorized", &pluginsdk.HTTPResponse{Status: 401, Body: `{"error":"The access token is invalid"}`}, nil, true, true, "HTTP 401: The access token is invalid"},
		{"Bluesky error", &pluginsdk.HTTPResponse{Status: 400, Body: `{"error":"InvalidRequest","message":"Input/text must not be longer than 300 graphemes"}`}, nil, true, true,
			"HTTP 400: InvalidRequest: Input/text must not be longer than 300 graphemes"},
		{"HTML error page is not logged", &pluginsdk.HTTPResponse{Status: 502, Body: "<html>secret-ish page</html>"}, nil, true, true, "HTTP 502"},
		{"rate limited", &pluginsdk.HTTPResponse{Status: 429, Body: `{"error":"Too many requests"}`}, nil, true, true, "HTTP 429: Too many requests"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, bad := FetchProblem(tt.resp, tt.err, "https://mastodon.example", "; see the README")
			if bad != tt.bad || r.Failed != tt.failed || r.Done || !strings.Contains(r.Detail, tt.detail) {
				t.Errorf("got %+v, %v", r, bad)
			}
			if strings.Contains(r.Detail, "<html>") {
				t.Errorf("the body leaked into the log: %q", r.Detail)
			}
		})
	}
}

func TestErrorText(t *testing.T) {
	tests := []struct {
		name, body, want string
	}{
		{"Mastodon", `{"error":"Validation failed: Text character limit of 500 exceeded"}`, ": Validation failed: Text character limit of 500 exceeded"},
		{"unknown fields are not logged", `{"error":"Service unavailable","detail":"echo of the token"}`, ": Service unavailable"},
		{"X problem", `{"title":"Forbidden","detail":"You are not permitted to perform this action.","type":"about:blank","status":403}`,
			": Forbidden: You are not permitted to perform this action."},
		{"X client forbidden", `{"client_id":"1","reason":"client-not-enrolled","title":"Client Forbidden","detail":"When authenticating requests…","type":"https://api.x.com/2/problems/client-forbidden"}`,
			": Client Forbidden: When authenticating requests…"},
		{"X errors", `{"errors":[{"message":"Rate limit exceeded","code":88}]}`, ": Rate limit exceeded"},
		{"LinkedIn", `{"message":"Unpermitted fields present in REQUEST_BODY","serviceErrorCode":100,"status":403}`, ": Unpermitted fields present in REQUEST_BODY"},
		{"Meta", `{"error":{"message":"Invalid OAuth access token - Cannot parse access token","type":"OAuthException","code":190,"fbtrace_id":"x"}}`,
			": Invalid OAuth access token - Cannot parse access token: (code 190)"},
		{"Meta with a user message", `{"error":{"message":"The media is not ready for publishing, please wait for a moment","code":9007,"error_subcode":2207027,"error_user_msg":"Media not ready"}}`,
			": The media is not ready for publishing, please wait for a moment: Media not ready: (code 9007/2207027)"},
		{"OAuth", `{"error":"invalid_grant","error_description":"The token has expired"}`, ": invalid_grant: The token has expired"},
		{"not JSON", `<html>`, ""},
		{"no message", `{"id":"1"}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ErrorText(tt.body); got != tt.want {
				t.Errorf("ErrorText = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestShorten(t *testing.T) {
	if got := Shorten("a\nb\tc", 10); got != "a b c" {
		t.Errorf("%q", got)
	}
	if got := Shorten("ñññññ", 3); got != "ñññ…" {
		t.Errorf("%q", got)
	}
}
