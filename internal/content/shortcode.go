package content

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// Shortcode is one occurrence of [name attr="value"] or
// [name attr="value"]inner[/name] in post content.
type Shortcode struct {
	Name  string            `json:"name"`
	Attrs map[string]string `json:"attrs"`
	Inner string            `json:"inner"`
}

var (
	// Lengths are checked after matching (Go's regexp caps nested repeats).
	openTag  = regexp.MustCompile(`\[([a-z][a-z0-9_-]{0,39})((?:\s+[a-z][a-z0-9_-]*(?:=(?:"[^"\]\n]*"|'[^'\]\n]*'|[^\s\]"']+))?)*)\s*(/)?\]`)
	attrPart = regexp.MustCompile(`([a-z][a-z0-9_-]{0,39})(?:=(?:"([^"]*)"|'([^']*)'|([^\s"']+)))?`)
)

const maxShortcodesPerDoc = 100

func parseAttrs(s string) map[string]string {
	out := map[string]string{}
	for _, m := range attrPart.FindAllStringSubmatch(s, -1) {
		v := m[2] + m[3] + m[4]
		if m[2] == "" && m[3] == "" && m[4] == "" {
			v = "true"
		}
		out[m[1]] = v
	}
	return out
}

// expandShortcodes replaces registered shortcodes outside code blocks with
// random placeholder tokens and returns the token → HTML map.
func expandShortcodes(src string, isRegistered func(string) bool, render ShortcodeFunc, icons IconRenderer) (string, map[string]string) {
	nonceBytes := make([]byte, 8)
	rand.Read(nonceBytes)
	nonce := hex.EncodeToString(nonceBytes)
	replacements := map[string]string{}

	lines := strings.SplitAfter(src, "\n")
	var out strings.Builder
	inFence := false
	fence := ""
	count := 0

	// Process the document segment by segment, skipping fenced code blocks.
	var segment strings.Builder
	flush := func() {
		if segment.Len() == 0 {
			return
		}
		s := segment.String()
		segment.Reset()
		out.WriteString(replaceInSegment(s, nonce, &count, replacements, isRegistered, render, icons))
	}
	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " ")
		if inFence {
			out.WriteString(line)
			if strings.HasPrefix(trimmed, fence) {
				inFence = false
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			flush()
			inFence = true
			fence = trimmed[:3]
			out.WriteString(line)
			continue
		}
		segment.WriteString(line)
	}
	flush()
	return out.String(), replacements
}

func replaceInSegment(s, nonce string, count *int, repl map[string]string, isRegistered func(string) bool, render ShortcodeFunc, icons IconRenderer) string {
	var out strings.Builder
	for {
		loc := openTag.FindStringSubmatchIndex(s)
		if loc == nil || *count >= maxShortcodesPerDoc {
			out.WriteString(s)
			return out.String()
		}
		name := s[loc[2]:loc[3]]
		prefix := s[:loc[0]]
		if !isRegistered(name) || insideInlineCode(prefix) || loc[5]-loc[4] > 2000 {
			out.WriteString(s[:loc[1]])
			s = s[loc[1]:]
			continue
		}
		sc := Shortcode{Name: name, Attrs: parseAttrs(s[loc[4]:loc[5]])}
		end := loc[1]
		selfClosing := loc[6] >= 0
		if !selfClosing {
			closeTag := "[/" + name + "]"
			if i := strings.Index(s[loc[1]:], closeTag); i >= 0 {
				sc.Inner = strings.TrimSpace(s[loc[1] : loc[1]+i])
				end = loc[1] + i + len(closeTag)
			}
		}
		html, err := render(sc)
		if err != nil {
			html = ""
		}
		token := fmt.Sprintf("sc%sx%d", nonce, *count)
		*count++
		repl[token] = string(SanitizePluginHTML(html, icons))
		out.WriteString(prefix)
		out.WriteString(token)
		s = s[end:]
	}
}

// insideInlineCode reports whether the text after prefix is inside an
// unclosed inline code span on the current line.
func insideInlineCode(prefix string) bool {
	if i := strings.LastIndexByte(prefix, '\n'); i >= 0 {
		prefix = prefix[i+1:]
	}
	return strings.Count(prefix, "`")%2 == 1
}
