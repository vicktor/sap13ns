package main

import (
	"strings"
	"testing"
	"time"
)

// The example of X's "Creating a signature" page
// (docs.x.com/fundamentals/authentication/oauth-1-0a/creating-a-signature):
// a form-encoded status update. The page's older Twitter version signed
// the same request for api.twitter.com.
var sample = OAuth1{
	ConsumerKey:    "xvz1evFS4wEEPTGEFPHBog",
	ConsumerSecret: "kAcSOqF21Fu85e7zjz7ZN2U4ZRhfV3WpwPAoE3Z7kBw",
	Token:          "370773112-GmHxMAgYyLbNEtIKZeRNFsMKPR9EyMZeS9weJAEb",
	TokenSecret:    "LswwdoUaIvS8ltyTt5jkRh4J50vUPVVHtR2YPi5kE",
}

func sampleParams() map[string]string {
	return map[string]string{
		"status":                 "Hello Ladies + Gentlemen, a signed OAuth request!",
		"include_entities":       "true",
		"oauth_consumer_key":     sample.ConsumerKey,
		"oauth_nonce":            "kYjzVBB8Y0ZFabxSWbWovY3uYSQ2pTgmZeNu2VS4cg",
		"oauth_signature_method": "HMAC-SHA1",
		"oauth_timestamp":        "1318622958",
		"oauth_token":            sample.Token,
		"oauth_version":          "1.0",
	}
}

func TestSignatureVector(t *testing.T) {
	// include_entities in the query or among the parameters signs the same.
	for _, u := range []string{"https://api.twitter.com/1.1/statuses/update.json?include_entities=true"} {
		params := sampleParams()
		delete(params, "include_entities")
		if got := sample.Signature("POST", u, params); got != "hCtSmYh+iHYCEqBWrE7C7hYmtUk=" {
			t.Errorf("signature %s", got)
		}
	}
	if got := sample.Signature("post", "https://API.Twitter.com:443/1.1/statuses/update.json", sampleParams()); got != "hCtSmYh+iHYCEqBWrE7C7hYmtUk=" {
		t.Errorf("signature with parameters %s", got)
	}
	// The current page uses api.x.com, which signs differently: the host is
	// part of the base string.
	if got := sample.Signature("POST", "https://api.x.com/1.1/statuses/update.json", sampleParams()); got != "Ls93hJiZbQ3akF3HF3x1Bz8/zU4=" {
		t.Errorf("signature for api.x.com %s", got)
	}
}

func TestNormalize(t *testing.T) {
	got := normalize("https://api.example.test/x?b=2&a=3&a=1", map[string]string{"c": "a b", "oauth_x": "!*'()"})
	if got != "a=1&a=3&b=2&c=a%20b&oauth_x=%21%2A%27%28%29" {
		t.Errorf("%s", got)
	}
	if baseURI("HTTPS://Api.X.com/2/tweets?x=1#f") != "https://api.x.com/2/tweets" {
		t.Errorf("%s", baseURI("HTTPS://Api.X.com/2/tweets?x=1#f"))
	}
}

func TestEncode(t *testing.T) {
	tests := map[string]string{
		"Ladies + Gentlemen": "Ladies%20%2B%20Gentlemen",
		"An encoded string!": "An%20encoded%20string%21",
		"Dogs, Cats & Mice":  "Dogs%2C%20Cats%20%26%20Mice",
		"☃":                  "%E2%98%83",
		"-._~":               "-._~",
	}
	for in, want := range tests {
		if got := encode(in); got != want {
			t.Errorf("encode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHeader(t *testing.T) {
	at := time.Unix(1318622958, 0)
	h := sample.Header("POST", "https://api.twitter.com/1.1/statuses/update.json",
		map[string]string{"status": "Hello Ladies + Gentlemen, a signed OAuth request!", "include_entities": "true"},
		"kYjzVBB8Y0ZFabxSWbWovY3uYSQ2pTgmZeNu2VS4cg", at)
	want := `OAuth oauth_consumer_key="xvz1evFS4wEEPTGEFPHBog", oauth_nonce="kYjzVBB8Y0ZFabxSWbWovY3uYSQ2pTgmZeNu2VS4cg", ` +
		`oauth_signature="hCtSmYh%2BiHYCEqBWrE7C7hYmtUk%3D", oauth_signature_method="HMAC-SHA1", oauth_timestamp="1318622958", ` +
		`oauth_token="370773112-GmHxMAgYyLbNEtIKZeRNFsMKPR9EyMZeS9weJAEb", oauth_version="1.0"`
	if h != want {
		t.Errorf("header\n%s\nwant\n%s", h, want)
	}
	if strings.Contains(h, sample.ConsumerSecret) || strings.Contains(h, sample.TokenSecret) {
		t.Error("a secret is in the header")
	}
}
