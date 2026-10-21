package theme

import (
	"bytes"
	"context"
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
	return context.WithValue(ctx, textsKey{}, tx)
}

func textsFrom(ctx context.Context) *Texts {
	tx, _ := ctx.Value(textsKey{}).(*Texts)
	return tx
}

// The marks around a text while the page renders, private-use characters
// that no template escapes and no text contains: start, key, the end of
// the key, the text, end. markTexts turns them into spans afterwards.
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
	if tx.Mark && v != "" && !strings.ContainsAny(key+v, markStart+markKey+markEnd) {
		return markStart + key + markKey + v + markEnd
	}
	return v
}

// markTexts turns the marks of a rendered page into spans where the text
// is the page's content, and drops them elsewhere: in attributes, the
// title, scripts and styles, where a span cannot go.
func markTexts(page []byte) []byte {
	if !bytes.Contains(page, []byte(markStart)) {
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
			out.Write(stripMarks(tok))
		case xhtml.TextToken:
			if raw > 0 {
				out.Write(stripMarks(tok))
			} else {
				out.Write(spanMarks(tok))
			}
		default:
			out.Write(stripMarks(tok))
		}
	}
	return out.Bytes()
}

// stripMarks drops the marks and the keys they carry.
func stripMarks(b []byte) []byte {
	if !bytes.Contains(b, []byte(markStart)) && !bytes.Contains(b, []byte(markEnd)) {
		return b
	}
	var out []byte
	for {
		i := bytes.Index(b, []byte(markStart))
		if i < 0 {
			break
		}
		j := bytes.Index(b[i:], []byte(markKey))
		if j < 0 {
			break
		}
		out = append(out, b[:i]...)
		b = b[i+j+len(markKey):]
	}
	out = append(out, b...)
	return bytes.ReplaceAll(out, []byte(markEnd), nil)
}

// spanMarks turns the marks of a text into spans.
func spanMarks(b []byte) []byte {
	if !bytes.Contains(b, []byte(markStart)) {
		return stripMarks(b)
	}
	var out []byte
	for {
		i := bytes.Index(b, []byte(markStart))
		if i < 0 {
			break
		}
		j := bytes.Index(b[i:], []byte(markKey))
		k := bytes.Index(b[i:], []byte(markEnd))
		if j < 0 || k < j {
			break
		}
		// The key went through the template's escaping, as text.
		key := html.UnescapeString(string(b[i+len(markStart) : i+j]))
		out = append(out, b[:i]...)
		out = append(out, `<span data-sap-text="`...)
		out = append(out, html.EscapeString(key)...)
		out = append(out, `">`...)
		out = append(out, b[i+j+len(markKey):i+k]...)
		out = append(out, `</span>`...)
		b = b[i+k+len(markEnd):]
	}
	out = append(out, b...)
	return stripMarks(out)
}
