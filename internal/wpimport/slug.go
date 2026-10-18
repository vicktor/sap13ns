package wpimport

import (
	"net/url"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const maxSlugLen = 120

var translit = strings.NewReplacer(
	"ñ", "n", "Ñ", "n", "ß", "ss", "æ", "ae", "Æ", "ae", "ø", "o", "Ø", "o", "œ", "oe", "Œ", "oe",
	"ł", "l", "Ł", "l", "đ", "d", "Đ", "d", "ð", "d", "Ð", "d", "þ", "th", "Þ", "th", "ħ", "h", "ı", "i",
)

// Slugify builds a slug of lowercase ASCII letters, digits and single
// hyphens, at most 120 bytes long, like content.Slugify. Percent-encoded
// WordPress slugs ("caf%c3%a9") are decoded first and accents are
// transliterated (é → e, ñ → n). Scripts without an ASCII transliteration
// yield "", so callers need a fallback.
func Slugify(s string) string {
	if strings.Contains(s, "%") {
		if dec, err := url.PathUnescape(s); err == nil && utf8.ValidString(dec) {
			s = dec
		}
	}
	s = norm.NFD.String(translit.Replace(s))
	var b strings.Builder
	dash := false
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Mn, r):
			continue
		case r < utf8.RuneSelf && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			b.WriteRune(unicode.ToLower(r))
			dash = false
		case r == '\'' || r == '’':
			// "don't" → "dont", as WordPress does.
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
		if b.Len() >= maxSlugLen {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}

// mediaName derives a safe file name for StoreMedia from a media URL:
// "/wp-content/uploads/2020/01/Café%20photo.JPG" → "Cafe-photo.JPG".
func mediaName(u *url.URL) string {
	base := path.Base(u.Path)
	if dec, err := url.PathUnescape(base); err == nil {
		base = dec
	}
	ext := path.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	stem = norm.NFD.String(translit.Replace(stem))
	var b strings.Builder
	dash := false
	for _, r := range stem {
		switch {
		case unicode.Is(unicode.Mn, r):
		case r < utf8.RuneSelf && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'):
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
		if b.Len() >= 80 {
			break
		}
	}
	name := strings.Trim(b.String(), "-")
	if name == "" {
		name = "file"
	}
	clean := true
	for _, r := range ext[min(1, len(ext)):] {
		if r >= utf8.RuneSelf || !(unicode.IsLetter(r) || unicode.IsDigit(r)) {
			clean = false
		}
	}
	if clean && len(ext) > 1 && len(ext) <= 8 {
		name += ext
	}
	return name
}
