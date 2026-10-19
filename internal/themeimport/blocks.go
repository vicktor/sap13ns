package themeimport

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

// The block grammar. WordPress stores blocks as HTML with comment
// delimiters (https://developer.wordpress.org/block-editor/explanations/architecture/key-concepts/):
//
//	<!-- wp:group {"layout":{"type":"constrained"}} -->
//	<div class="wp-block-group">…inner blocks…</div>
//	<!-- /wp:group -->
//	<!-- wp:site-title {"level":0} /-->
//
// A static block keeps the HTML it saved between its delimiters; a
// dynamic one (site-title) is rendered on the server from its attributes.
// This parser follows WordPress's own (class-wp-block-parser.php): text
// between blocks at the top level is freeform HTML, a closer that does not
// match closes the blocks opened after its opener, and blocks still open
// at the end are closed there.

// A Block is one parsed block.
type Block struct {
	Name  string         // "core/group"; "" for freeform HTML
	Attrs map[string]any // the JSON attributes (numbers as json.Number)
	Inner []*Block
	// Parts is the block's own HTML, with the places of its inner blocks:
	// a part with Block >= 0 stands for Inner[Block].
	Parts   []Part
	BadJSON bool // the attributes did not parse
}

// A Part is a chunk of a block's HTML, or the place of an inner block.
type Part struct {
	HTML  string
	Block int // -1 for HTML
}

// Limits of one parse: blocks in all, and how deep they nest.
const (
	maxBlocks     = 20000
	maxBlockDepth = 64
	maxAttrBytes  = 64 << 10
)

var errTooManyBlocks = errors.New("too many blocks")

// ParseBlocks parses block markup into its top-level blocks. Freeform
// HTML between top-level blocks is a block named "".
func ParseBlocks(src string) ([]*Block, error) {
	p := &blockParser{src: src, closeAt: -2}
	return p.parse()
}

// closeAfter returns the position of the first "-->" at or after j, or
// -1 when there is none.
func (p *blockParser) closeAfter(j int) int {
	if p.closeAt == -1 || p.closeAt >= j && p.closeFrom <= j {
		return p.closeAt
	}
	p.closeFrom = j
	if i := strings.Index(p.src[j:], "-->"); i >= 0 {
		p.closeAt = j + i
	} else {
		p.closeAt = -1
	}
	return p.closeAt
}

type blockParser struct {
	src   string
	stack []*Block
	top   []*Block
	count int
	// closeAt caches where the next "-->" is: every candidate delimiter
	// before it ends there, so the text is searched once, not once per
	// candidate (a run of unclosed "<!-- wp:a" would be quadratic). -1
	// once there is none left: no later delimiter can end either.
	closeAt   int
	closeFrom int
}

// delimiter is a parsed block comment.
type delimiter struct {
	start, end int // its bytes in src
	name       string
	closer     bool
	void       bool
	attrs      string // the JSON text, "" when none
}

func (p *blockParser) parse() ([]*Block, error) {
	pos := 0
	for {
		d, ok := p.next(pos)
		if !ok {
			p.text(p.src[pos:])
			break
		}
		p.text(p.src[pos:d.start])
		pos = d.end
		switch {
		case d.closer:
			i := len(p.stack) - 1
			for i >= 0 && p.stack[i].Name != d.name {
				i--
			}
			if i < 0 {
				continue // a closer without its opener: dropped
			}
			p.stack = p.stack[:i]
		default:
			b, err := p.block(d)
			if err != nil {
				return nil, err
			}
			p.attach(b)
			if !d.void {
				if len(p.stack) >= maxBlockDepth {
					return nil, errTooManyBlocks
				}
				p.stack = append(p.stack, b)
			}
		}
	}
	return p.top, nil
}

func (p *blockParser) block(d delimiter) (*Block, error) {
	if p.count++; p.count > maxBlocks {
		return nil, errTooManyBlocks
	}
	b := &Block{Name: d.name, Attrs: map[string]any{}}
	if d.attrs != "" {
		if len(d.attrs) > maxAttrBytes || !decodeAttrs(d.attrs, &b.Attrs) {
			b.Attrs, b.BadJSON = map[string]any{}, true
		}
	}
	return b, nil
}

func decodeAttrs(s string, into *map[string]any) bool {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil {
		return false
	}
	*into = m
	return true
}

// attach adds a block to the open block, or to the top level.
func (p *blockParser) attach(b *Block) {
	if n := len(p.stack); n > 0 {
		parent := p.stack[n-1]
		parent.Parts = append(parent.Parts, Part{Block: len(parent.Inner)})
		parent.Inner = append(parent.Inner, b)
		return
	}
	p.top = append(p.top, b)
}

// text adds HTML to the open block, or freeform HTML to the top level.
func (p *blockParser) text(s string) {
	if s == "" {
		return
	}
	if n := len(p.stack); n > 0 {
		parent := p.stack[n-1]
		parent.Parts = append(parent.Parts, Part{HTML: s, Block: -1})
		return
	}
	if strings.TrimSpace(s) == "" {
		return
	}
	p.top = append(p.top, &Block{Attrs: map[string]any{}, Parts: []Part{{HTML: s, Block: -1}}})
}

// next finds the next block delimiter at or after pos.
func (p *blockParser) next(pos int) (delimiter, bool) {
	for {
		i := strings.Index(p.src[pos:], "<!--")
		if i < 0 {
			return delimiter{}, false
		}
		start := pos + i
		if d, ok := p.delimiterAt(start); ok {
			return d, true
		}
		if p.closeAt == -1 {
			return delimiter{}, false // no "-->" left: nothing more can close
		}
		pos = start + 4
	}
}

func isSpaceByte(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' }

func (p *blockParser) delimiterAt(start int) (delimiter, bool) {
	s := p.src
	i := start + 4
	j := i
	for j < len(s) && isSpaceByte(s[j]) {
		j++
	}
	if j == i {
		return delimiter{}, false
	}
	d := delimiter{start: start}
	if j < len(s) && s[j] == '/' {
		d.closer = true
		j++
	}
	if !strings.HasPrefix(s[j:], "wp:") {
		return delimiter{}, false
	}
	j += 3
	nameStart := j
	for j < len(s) && (s[j] >= 'a' && s[j] <= 'z' || s[j] >= '0' && s[j] <= '9' || s[j] == '-' || s[j] == '_' || s[j] == '/') {
		j++
	}
	name := s[nameStart:j]
	if !validBlockName(name) {
		return delimiter{}, false
	}
	if !strings.Contains(name, "/") {
		name = "core/" + name
	}
	d.name = name
	end := p.closeAfter(j)
	if end < 0 {
		return delimiter{}, false
	}
	inner := s[j:end]
	d.end = end + 3
	if inner != "" && !isSpaceByte(inner[0]) {
		return delimiter{}, false
	}
	inner = strings.TrimSpace(inner)
	if strings.HasSuffix(inner, "/") {
		d.void = true
		inner = strings.TrimSpace(strings.TrimSuffix(inner, "/"))
	}
	switch {
	case inner == "":
	case strings.HasPrefix(inner, "{") && strings.HasSuffix(inner, "}") && !d.closer:
		d.attrs = inner
	default:
		return delimiter{}, false
	}
	return d, true
}

// validBlockName is [a-z][a-z0-9_-]* with an optional namespace.
func validBlockName(n string) bool {
	parts := strings.Split(n, "/")
	if len(parts) > 2 {
		return false
	}
	for _, p := range parts {
		if p == "" || p[0] < 'a' || p[0] > 'z' {
			return false
		}
	}
	return true
}

// Attribute helpers: the JSON of a block is untrusted, so every read
// checks its type.

func attrString(m map[string]any, key string) string {
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}

func attrBool(m map[string]any, key string) bool {
	b, _ := m[key].(bool)
	return b
}

func attrMap(m map[string]any, key string) map[string]any {
	v, _ := m[key].(map[string]any)
	return v
}

// attrNumber reads a number, or a numeric string.
func attrNumber(m map[string]any, key string) (float64, bool) {
	switch v := m[key].(type) {
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	case float64:
		return v, true
	case string:
		f, err := json.Number(v).Float64()
		return f, err == nil
	}
	return 0, false
}

func attrInt(m map[string]any, key string, def int) int {
	if f, ok := attrNumber(m, key); ok && f >= -1e6 && f <= 1e6 {
		return int(f)
	}
	return def
}

// path reads a nested value: dig(m, "style", "spacing", "padding").
func dig(m map[string]any, keys ...string) any {
	var v any = m
	for _, k := range keys {
		mm, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = mm[k]
	}
	return v
}

// decodeJSON decodes a theme's JSON file with numbers kept as text.
func decodeJSON(b []byte) (map[string]any, error) {
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	if m == nil {
		return nil, errors.New("not a JSON object")
	}
	return m, nil
}
