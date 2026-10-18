package billing

import (
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// encodeForm flattens params into Stripe's bracketed form syntax:
//
//	{"line_items": []any{map[string]any{"price": "price_1"}}} -> line_items[0][price]=price_1
//	{"metadata": map[string]string{"site_id": "42"}}           -> metadata[site_id]=42
//
// Supported values: string, bool, int, int64, map[string]string,
// map[string]any, []string, []any and nil (skipped). Map keys must not
// contain square brackets, which would let a value rewrite the key path.
func encodeForm(params map[string]any) (url.Values, error) {
	vals := url.Values{}
	for _, k := range sortedKeys(params) {
		if err := appendForm(vals, k, params[k]); err != nil {
			return nil, err
		}
	}
	return vals, nil
}

func appendForm(vals url.Values, key string, v any) error {
	switch t := v.(type) {
	case nil:
	case string:
		vals.Add(key, t)
	case bool:
		vals.Add(key, strconv.FormatBool(t))
	case int:
		vals.Add(key, strconv.Itoa(t))
	case int64:
		vals.Add(key, strconv.FormatInt(t, 10))
	case map[string]string:
		for _, k := range sortedKeys(t) {
			if err := checkFormKey(k); err != nil {
				return err
			}
			vals.Add(key+"["+k+"]", t[k])
		}
	case map[string]any:
		for _, k := range sortedKeys(t) {
			if err := checkFormKey(k); err != nil {
				return err
			}
			if err := appendForm(vals, key+"["+k+"]", t[k]); err != nil {
				return err
			}
		}
	case []string:
		for i, s := range t {
			vals.Add(key+"["+strconv.Itoa(i)+"]", s)
		}
	case []any:
		for i, e := range t {
			if err := appendForm(vals, key+"["+strconv.Itoa(i)+"]", e); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("billing: cannot form-encode %T for %s", v, key)
	}
	return nil
}

func checkFormKey(k string) error {
	if k == "" || strings.ContainsAny(k, "[]") {
		return fmt.Errorf("billing: invalid parameter key %q (empty or contains brackets)", truncate(k, 64))
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// Stripe metadata limits (docs.stripe.com/api/metadata).
const (
	maxMetadataKeys     = 50
	maxMetadataKeyLen   = 40
	maxMetadataValueLen = 500
)

func checkMetadata(md map[string]string) error {
	if len(md) > maxMetadataKeys {
		return fmt.Errorf("billing: metadata has %d keys, Stripe allows %d", len(md), maxMetadataKeys)
	}
	for k, v := range md {
		if err := checkFormKey(k); err != nil {
			return err
		}
		if utf8.RuneCountInString(k) > maxMetadataKeyLen {
			return fmt.Errorf("billing: metadata key %q longer than %d characters", truncate(k, 64), maxMetadataKeyLen)
		}
		if utf8.RuneCountInString(v) > maxMetadataValueLen {
			return fmt.Errorf("billing: metadata value for %q longer than %d characters", k, maxMetadataValueLen)
		}
	}
	return nil
}
