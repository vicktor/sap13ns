package billing

import (
	"strings"
	"testing"
)

func TestEncodeFormNested(t *testing.T) {
	vals, err := encodeForm(map[string]any{
		"mode": "subscription",
		"line_items": []any{
			map[string]any{"price": "price_A", "quantity": 2},
			map[string]any{"price": "price_B", "quantity": int64(1)},
		},
		"metadata":              map[string]string{"site_id": "42", "plan": "pro"},
		"subscription_data":     map[string]any{"metadata": map[string]string{"site_id": "42"}, "trial_period_days": 14},
		"allow_promotion_codes": true,
		"expand":                []string{"data.product", "data.tiers"},
		"skipped":               nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"mode":                                 "subscription",
		"line_items[0][price]":                 "price_A",
		"line_items[0][quantity]":              "2",
		"line_items[1][price]":                 "price_B",
		"line_items[1][quantity]":              "1",
		"metadata[site_id]":                    "42",
		"metadata[plan]":                       "pro",
		"subscription_data[metadata][site_id]": "42",
		"subscription_data[trial_period_days]": "14",
		"allow_promotion_codes":                "true",
		"expand[0]":                            "data.product",
		"expand[1]":                            "data.tiers",
	}
	if len(vals) != len(want) {
		t.Errorf("got %d keys, want %d: %v", len(vals), len(want), vals)
	}
	for k, v := range want {
		if got := vals.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if _, ok := vals["skipped"]; ok {
		t.Error("nil value should be skipped")
	}
	// Brackets are percent-encoded on the wire, which Stripe decodes.
	enc := vals.Encode()
	if !strings.Contains(enc, "line_items%5B0%5D%5Bprice%5D=price_A") {
		t.Errorf("unexpected encoding: %s", enc)
	}
	// Encoding is deterministic.
	vals2, _ := encodeForm(map[string]any{
		"metadata": map[string]string{"plan": "pro", "site_id": "42"},
	})
	vals3, _ := encodeForm(map[string]any{
		"metadata": map[string]string{"site_id": "42", "plan": "pro"},
	})
	if vals2.Encode() != vals3.Encode() {
		t.Error("encoding not deterministic")
	}
}

func TestEncodeFormEscapesValues(t *testing.T) {
	vals, err := encodeForm(map[string]any{
		"success_url": "https://example.com/ok?session_id={CHECKOUT_SESSION_ID}&a=b",
	})
	if err != nil {
		t.Fatal(err)
	}
	enc := vals.Encode()
	if strings.Contains(enc, "&a=b") {
		t.Errorf("value not escaped: %s", enc)
	}
	if got := vals.Get("success_url"); got != "https://example.com/ok?session_id={CHECKOUT_SESSION_ID}&a=b" {
		t.Errorf("round trip: %q", got)
	}
}

func TestEncodeFormRejectsBracketKeys(t *testing.T) {
	for _, key := range []string{"site]x", "a[b", ""} {
		_, err := encodeForm(map[string]any{"metadata": map[string]string{key: "v"}})
		if err == nil {
			t.Errorf("key %q accepted", key)
		}
	}
	if _, err := encodeForm(map[string]any{"x": 1.5}); err == nil {
		t.Error("float accepted")
	}
}

func TestCheckMetadata(t *testing.T) {
	if err := checkMetadata(map[string]string{"site_id": "42"}); err != nil {
		t.Fatal(err)
	}
	if err := checkMetadata(map[string]string{strings.Repeat("k", 41): "v"}); err == nil {
		t.Error("long key accepted")
	}
	if err := checkMetadata(map[string]string{"k": strings.Repeat("v", 501)}); err == nil {
		t.Error("long value accepted")
	}
	if err := checkMetadata(map[string]string{"k": strings.Repeat("é", 500)}); err != nil {
		t.Errorf("500 runes rejected: %v", err)
	}
	big := map[string]string{}
	for i := range 51 {
		big[strings.Repeat("k", i+1)] = "v"
	}
	if err := checkMetadata(big); err == nil {
		t.Error("51 keys accepted")
	}
}

func TestCheckoutLocale(t *testing.T) {
	for in, want := range map[string]string{
		"":      "auto",
		"auto":  "auto",
		"es":    "es",
		"es-ES": "es",
		"es-MX": "es-419",
		"es_AR": "es-419",
		"en-GB": "en-GB",
		"en-US": "en",
		"pt-br": "pt-BR",
		"pt-PT": "pt",
		"fr-CA": "fr-CA",
		"ca":    "auto",
		"zh-tw": "zh-TW",
		"de-AT": "de",
		"xx-YY": "auto",
	} {
		if got := CheckoutLocale(in); got != want {
			t.Errorf("CheckoutLocale(%q) = %q, want %q", in, got, want)
		}
	}
}
