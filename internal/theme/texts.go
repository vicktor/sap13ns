package theme

import (
	"bytes"
	"context"
	"crypto/rand"
	"html"
	"strings"

	xhtml "golang.org/x/net/html"
)

// Texts changes a theme's interface texts (what its templates write with
// T, in one language) for one render: the customiser's edits replace them,
// and in its preview each one is marked so that it can be typed over.
// Only texts without arguments change; a text with placeholders ("%d
// posts") stays the theme's.
type Texts struct {
	// Over maps a text's key (the English source the template names) to
	// what the page shows instead.
	Over map[string]string
	// Mark wraps each text of the page's body in <span data-sap-text=key>.
	Mark bool

	// nonce follows every mark of this render (WithTexts), so that the
	// same characters in a post or a comment are never taken for one.
	nonce string
}

// Protected are the texts no design may change: the licence credit
// (CPAL-1.0, Exhibit B) is rendered with them.
var Protected = map[string]bool{"Powered by": true}

type textsKey struct{}

// WithTexts returns ctx carrying tx for RenderContext.
func WithTexts(ctx context.Context, tx *Texts) context.Context {
	if tx == nil || (len(tx.Over) == 0 && !tx.Mark) {
		return ctx
	}
	if tx.Mark && tx.nonce == "" {
		tx.nonce = newNonce()
	}
	return context.WithValue(ctx, textsKey{}, tx)
}

func textsFrom(ctx context.Context) *Texts {
	tx, _ := ctx.Value(textsKey{}).(*Texts)
	return tx
}

// newNonce makes 12 private-use characters from 16 random bytes' worth.
func newNonce() string {
	var b [12]byte
	rand.Read(b[:])
	var sb strings.Builder
	for _, c := range b {
		sb.WriteRune(0xE100 + rune(c))
	}
	return sb.String()
}

// The marks around a text while the page renders, private-use characters
// that no template escapes, each followed by the render's nonce: start,
// key, the end of the key, the text, end. markTexts turns them into spans
// afterwards; content that carries the same characters without the nonce
// is left alone.
const (
	markStart = ""
	markKey   = ""
	markEnd   = ""
)

// text is what T writes for key, whose translation is v.
func (tx *Texts) text(key, v string) string {
	if tx == nil || Protected[key] {
		return v
	}
	if o := tx.Over[key]; o != "" {
		v = o
	}
	if tx.Mark && tx.nonce != "" && v != "" && !strings.ContainsAny(key+v, markStart+markKey+markEnd) {
		return markStart + tx.nonce + key + markKey + tx.nonce + v + markEnd + tx.nonce
	}
	return v
}

// markTexts turns the marks of a rendered page into spans where the text
// is the page's content, and drops them elsewhere: in attributes, the
// title, scripts and styles, where a span cannot go.
func markTexts(page []byte, nonce string) []byte {
	m := textMarks{start: []byte(markStart + nonce), key: []byte(markKey + nonce), end: []byte(markEnd + nonce)}
	if nonce == "" || !bytes.Contains(page, m.start) {
		return page
	}
	var out bytes.Buffer
	out.Grow(len(page) + len(page)/8)
	z := xhtml.NewTokenizer(bytes.NewReader(page))
	raw := 0 // inside title, textarea, option, script or style
	for {
		tt := z.Next()
		if tt == xhtml.ErrorToken {
			break
		}
		tok := z.Raw()
		switch tt {
		case xhtml.StartTagToken, xhtml.EndTagToken:
			name, _ := z.TagName()
			switch string(name) {
			case "title", "textarea", "option", "script", "style":
				if tt == xhtml.StartTagToken {
					raw++
				} else if raw > 0 {
					raw--
				}
			}
			out.Write(m.strip(tok))
		case xhtml.TextToken:
			if raw > 0 {
				out.Write(m.strip(tok))
			} else {
				out.Write(m.span(tok))
			}
		default:
			out.Write(m.strip(tok))
		}
	}
	return out.Bytes()
}

type textMarks struct{ start, key, end []byte }

// strip drops the marks and the keys they carry. A key is the template's
// escaping of one T key, between its two marks in one token.
func (m textMarks) strip(b []byte) []byte {
	if !bytes.Contains(b, m.start) && !bytes.Contains(b, m.end) {
		return b
	}
	var out []byte
	for {
		i := bytes.Index(b, m.start)
		if i < 0 {
			break
		}
		j := bytes.Index(b[i:], m.key)
		if j < 0 {
			break
		}
		out = append(out, b[:i]...)
		b = b[i+j+len(m.key):]
	}
	out = append(out, b...)
	return bytes.ReplaceAll(out, m.end, nil)
}

// span turns the marks of a text token into spans.
func (m textMarks) span(b []byte) []byte {
	if !bytes.Contains(b, m.start) {
		return m.strip(b)
	}
	var out []byte
	for {
		i := bytes.Index(b, m.start)
		if i < 0 {
			break
		}
		j := bytes.Index(b[i:], m.key)
		k := bytes.Index(b[i:], m.end)
		if j < 0 || k < j {
			break
		}
		// The key went through the template's escaping, as text.
		key := html.UnescapeString(string(b[i+len(m.start) : i+j]))
		out = append(out, b[:i]...)
		out = append(out, `<span data-sap-text="`...)
		out = append(out, html.EscapeString(key)...)
		out = append(out, `">`...)
		out = append(out, b[i+j+len(m.key):i+k]...)
		out = append(out, `</span>`...)
		b = b[i+k+len(m.end):]
	}
	out = append(out, b...)
	return m.strip(out)
}
