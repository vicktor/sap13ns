package ai

import (
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// Lint is a local, deterministic detector of AI clichés: phrase lists for
// Spanish and English (case- and accent-insensitive, whole words) plus a few
// heuristics. It never touches the network. Code blocks, inline code and
// URLs are skipped.

// Lint rules.
const (
	RuleCliche   = "cliche"
	RuleNotJust  = "not-just"        // "no es solo X, es Y" / "not just X, but Y"
	RuleEmDash   = "em-dash"         // too many em-dashes
	RuleEmoji    = "emoji"           //
	RuleQuestion = "question-opener" // the text opens with a rhetorical question
	RuleBold     = "bold"            // bolded buzzwords
)

// Finding is one span to underline. Start and End are byte offsets into the
// linted text; Phrase is the text as written.
type Finding struct {
	Start      int    `json:"start"`
	End        int    `json:"end"`
	Phrase     string `json:"phrase"`
	Rule       string `json:"rule"`
	Suggestion string `json:"suggestion"`
}

type phraseGroup struct {
	hint    string
	phrases []string // a trailing "*" also matches longer words: "delv*" → delves, delving
}

var esPhrases = []phraseGroup{
	{"Arranque genérico: empieza por el hecho concreto.", []string{
		"en el mundo actual", "en la era digital", "en la era de la", "en el panorama actual",
		"en un mundo cada vez más", "en los tiempos que corren", "hoy más que nunca", "en constante evolución",
		"en un mundo en constante", "el cambiante panorama",
	}},
	{"Invitación de folleto: dilo de forma directa.", []string{
		"sumérgete", "sumergirte", "sumergirnos", "sumerjámonos", "adentrémonos", "adéntrate",
		"desbloque*", "embárcate", "un viaje", "¡vamos allá", "vamos allá", "no te lo pierdas",
		"descubre cómo", "te contamos todo", "todo lo que necesitas saber", "la guía definitiva",
	}},
	{"Muletilla: elimínala o di directamente lo que importa.", []string{
		"cabe destacar", "cabe señalar", "cabe mencionar", "cabe resaltar", "es importante señalar",
		"es importante destacar", "es importante tener en cuenta", "es importante recordar",
		"vale la pena mencionar", "vale la pena destacar", "es crucial", "es fundamental",
		"sin lugar a dudas", "sin duda alguna", "dicho esto", "en última instancia", "a fin de cuentas",
		"en este artículo", "a lo largo de este artículo", "exploraremos", "vamos a explorar",
	}},
	{"Cierre de manual: termina con una idea concreta, no con un resumen.", []string{
		"en conclusión", "en resumen", "para concluir", "en definitiva", "en pocas palabras",
		"esperamos que este artículo", "espero que este artículo", "te haya sido útil", "no dudes en",
	}},
	{"Fórmula publicitaria: sustitúyela por el beneficio concreto.", []string{
		"potencia tu", "potenciar tu", "potencia al máximo",
		"al siguiente nivel", "al próximo nivel", "a otro nivel", "eleva tu", "todo tu potencial",
		"máximo potencial", "un antes y un después", "cambio de paradigma", "punto de inflexión",
		"una experiencia única", "infinitas posibilidades", "un mundo de posibilidades", "sin precedentes",
		"de primer nivel", "clave del éxito", "el secreto mejor guardado",
	}},
	{"Adjetivo hueco: muestra el dato que lo justifica.", []string{
		"revolucionari*", "fascinant*", "apasionante", "innovador*", "disruptiv*", "transformador*",
		"vanguardista", "de vanguardia", "a la vanguardia", "meticulos*", "holístic*", "sinergi*",
		"impresionante", "increíble",
	}},
	{"Expresión gastada: concreta qué papel o qué cantidad.", []string{
		"juega un papel crucial", "juega un papel fundamental", "desempeña un papel", "un papel clave",
		"pieza clave", "piedra angular", "en el corazón de", "un sinfín de", "una amplia gama de",
		"un amplio abanico de", "es un testimonio de", "como modelo de lenguaje",
	}},
}

var enPhrases = []phraseGroup{
	{"Generic opener: start with the concrete fact.", []string{
		"in today's fast-paced world", "in today's world", "in today's digital age", "in the digital age",
		"in an ever-changing", "ever-evolving", "ever-changing landscape", "fast-paced world",
		"in the realm of", "in the world of",
	}},
	{"Brochure invitation: say it plainly.", []string{
		"delv*", "unlock*", "embark*", "unleash*", "dive into", "deep dive", "let's dive in", "dive deep",
		"buckle up", "look no further", "the ultimate guide", "everything you need to know",
		"have you ever wondered", "imagine a world", "picture this", "whether you're a",
	}},
	{"Filler: cut it or say what matters.", []string{
		"it's worth noting", "it is worth noting", "worth mentioning", "it's important to note",
		"it is important to note", "it's important to remember", "needless to say", "in this article",
		"we'll explore", "let's explore", "at the end of the day", "in essence", "moreover", "furthermore",
		"ultimately",
	}},
	{"Textbook ending: close on a concrete point, not a recap.", []string{
		"in conclusion", "in summary", "to sum up", "to summarize", "key takeaways",
		"i hope this helps", "feel free to",
	}},
	{"Marketing formula: name the concrete benefit instead.", []string{
		"game-changer", "game changer", "game-changing", "next level", "harness the power",
		"elevate your", "supercharge*", "empower*", "paradigm shift", "cutting-edge",
		"groundbreaking", "unparalleled", "revolutioniz*", "revolutionis*", "transformative", "seamless*",
		"leverag*",
	}},
	{"Worn-out image: be specific.", []string{
		"tapestr*", "treasure trove", "testament to", "a symphony of", "beacon of", "plays a crucial role",
		"plays a vital role", "plays a pivotal role", "pivotal", "at the heart of", "a myriad of", "plethora",
		"multifaceted", "holistic", "synerg*", "meticulous*", "intricate*", "nestled", "bustling",
		"vibrant", "navigate the complexities", "navigating the complexities", "the landscape of",
		"underscore*", "as an ai",
	}},
}

type phraseRule struct {
	phrase string // folded
	prefix bool
	hint   string
}

var (
	esRules = compilePhrases(esPhrases)
	enRules = compilePhrases(enPhrases)
)

func compilePhrases(groups []phraseGroup) []phraseRule {
	var out []phraseRule
	for _, g := range groups {
		for _, p := range g.phrases {
			r := phraseRule{hint: g.hint}
			r.phrase, r.prefix = strings.CutSuffix(p, "*")
			r.phrase = fold(r.phrase).s
			out = append(out, r)
		}
	}
	return out
}

// folded is text lowercased with accents removed and typographic apostrophes
// and hyphens normalized; start and end map each folded byte back to the
// original rune's byte range.
type folded struct {
	s          string
	start, end []int
}

func fold(text string) folded {
	f := folded{start: make([]int, 0, len(text)), end: make([]int, 0, len(text))}
	var b strings.Builder
	b.Grow(len(text))
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		s := foldRune(r)
		b.WriteString(s)
		for range len(s) {
			f.start = append(f.start, i)
			f.end = append(f.end, i+size)
		}
		i += size
	}
	f.s = b.String()
	return f
}

func foldRune(r rune) string {
	switch r {
	case '’', '‘', 'ʼ', '′':
		return "'"
	case '‐', '‑':
		return "-"
	}
	r = unicode.ToLower(r)
	if r < utf8.RuneSelf {
		return string(r)
	}
	var b strings.Builder
	for _, c := range norm.NFD.String(string(r)) {
		if !unicode.Is(unicode.Mn, c) {
			b.WriteRune(c)
		}
	}
	if b.Len() == 0 {
		return string(r)
	}
	return b.String()
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

func (f folded) wordBefore(i int) bool {
	if i <= 0 {
		return false
	}
	r, _ := utf8.DecodeLastRuneInString(f.s[:i])
	return isWordRune(r)
}

func (f folded) wordAt(i int) bool {
	if i >= len(f.s) {
		return false
	}
	r, _ := utf8.DecodeRuneInString(f.s[i:])
	return isWordRune(r)
}

func (f folded) span(from, to int) (int, int) {
	return f.start[from], f.end[to-1]
}

var (
	notJustES = regexp.MustCompile(`\bno (?:(?:es|son|era|eran|fue|fueron|sera|seran|se trata|tiene que ver) )?(?:solo|solamente|simplemente|unicamente|meramente)\b[^.!?\n]{1,150}?(?:[,;:—–]\s*(?:es|son|era|fue|sera|se trata|hablamos de)\b|\bsino\b)`)
	notJustEN = regexp.MustCompile(`\b(?:not|isn't|aren't|wasn't|weren't|doesn't|don't) (?:just|only|merely|simply)\b[^.!?\n]{1,150}?(?:\bbut\b|[,;:—–]\s*(?:it's|it is|they're|they are|it was|this is|that's|we're|you're|it)\b)`)
	boldRe    = regexp.MustCompile(`\*\*[^*\n]{1,40}\*\*|__[^_\n]{1,40}__`)
	linkTgtRe = regexp.MustCompile(`\]\([^)\s]*(?:\s+"[^"\n]*")?\)`)
)

// Lint returns the findings for text, sorted by offset. lang selects the
// phrase list ("es", "en"; an empty lang uses both, other languages get only
// the language-independent heuristics).
func Lint(text, lang string) []Finding {
	skip := skippedRanges(text)
	f := fold(text)
	var out []Finding
	add := func(start, end int, rule, hint string) {
		if start >= end || overlaps(skip, start, end) {
			return
		}
		out = append(out, Finding{Start: start, End: end, Phrase: text[start:end], Rule: rule, Suggestion: hint})
	}

	pl := primaryLang(lang)
	var rules []phraseRule
	switch pl {
	case "es":
		rules = esRules
	case "en":
		rules = enRules
	case "":
		rules = append(slices.Clone(esRules), enRules...)
	}
	for _, r := range rules {
		for from := 0; ; {
			i := strings.Index(f.s[from:], r.phrase)
			if i < 0 {
				break
			}
			i += from
			end := i + len(r.phrase)
			from = i + 1
			if f.wordBefore(i) && isWordRune(firstRune(r.phrase)) {
				continue
			}
			if r.prefix {
				for f.wordAt(end) {
					_, size := utf8.DecodeRuneInString(f.s[end:])
					end += size
				}
			} else if f.wordAt(end) && isWordRune(lastRune(r.phrase)) {
				continue
			}
			s, e := f.span(i, end)
			add(s, e, RuleCliche, r.hint)
		}
	}

	if pl == "es" || pl == "" {
		for _, m := range notJustES.FindAllStringIndex(f.s, -1) {
			s, e := f.span(m[0], m[1])
			add(s, e, RuleNotJust, "Estructura «no es solo X, es Y»: di directamente lo que es.")
		}
	}
	if pl == "en" || pl == "" {
		for _, m := range notJustEN.FindAllStringIndex(f.s, -1) {
			s, e := f.span(m[0], m[1])
			add(s, e, RuleNotJust, `"Not just X, but Y" pattern: say plainly what it is.`)
		}
	}

	es := pl == "es"
	for _, d := range emDashes(text, skip) {
		add(d[0], d[1], RuleEmDash, pick(es, "Demasiadas rayas: usa comas, paréntesis o un punto.", "Too many em-dashes: use commas, parentheses or a full stop."))
	}
	for _, e := range emojiRuns(text) {
		add(e[0], e[1], RuleEmoji, pick(es, "Emoji: quítalo del texto del artículo.", "Emoji: remove it from the article text."))
	}
	if q, ok := questionOpener(text); ok {
		add(q[0], q[1], RuleQuestion, pick(es, "Pregunta retórica de arranque: empieza con una afirmación.", "Rhetorical question as opener: start with a statement."))
	}
	for _, b := range boldBuzzwords(text) {
		add(b[0], b[1], RuleBold, pick(es, "Demasiadas negritas en el párrafo: deja solo la imprescindible.", "Too much bold in one paragraph: keep only what is essential."))
	}

	slices.SortStableFunc(out, func(a, b Finding) int {
		if a.Start != b.Start {
			return a.Start - b.Start
		}
		return b.End - a.End
	})
	// Drop findings nested in an earlier one of the same rule ("en resumen"
	// inside a longer match, "game changer" twice).
	var kept []Finding
	for _, x := range out {
		dup := false
		for i := len(kept) - 1; i >= 0 && kept[i].End > x.Start; i-- {
			if kept[i].Rule == x.Rule {
				dup = true
				break
			}
		}
		if !dup {
			kept = append(kept, x)
		}
	}
	return kept
}

func pick(es bool, a, b string) string {
	if es {
		return a
	}
	return b
}

func firstRune(s string) rune { r, _ := utf8.DecodeRuneInString(s); return r }

func lastRune(s string) rune { r, _ := utf8.DecodeLastRuneInString(s); return r }

func overlaps(ranges [][2]int, start, end int) bool {
	for _, r := range ranges {
		if start < r[1] && r[0] < end {
			return true
		}
	}
	return false
}

// skippedRanges returns the byte ranges of fenced code, inline code, URLs
// and link targets.
func skippedRanges(text string) [][2]int {
	var out [][2]int
	pos, fence, fenceStart := 0, "", 0
	for _, line := range strings.SplitAfter(text, "\n") {
		switch {
		case fence != "":
			if closesFence(line, fence) {
				out = append(out, [2]int{fenceStart, pos + len(line)})
				fence = ""
			}
		case fenceOf(line) != "":
			fence, fenceStart = fenceOf(line), pos
		}
		pos += len(line)
	}
	if fence != "" {
		out = append(out, [2]int{fenceStart, len(text)})
	}
	for _, re := range []*regexp.Regexp{inlineCodeRe, urlRe, linkTgtRe} {
		for _, m := range re.FindAllStringIndex(text, -1) {
			out = append(out, [2]int{m[0], m[1]})
		}
	}
	return out
}

// emDashes returns the em-dashes to flag: all of them when the text has at
// least three and more than one per 120 words, or those of a paragraph with
// three or more. Dashes opening a line (dialogue) are not counted.
func emDashes(text string, skip [][2]int) [][2]int {
	var all [][2]int
	byPara := map[int][][2]int{}
	para := 0
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		if r == '\n' && strings.HasPrefix(text[i+1:], "\n") {
			para++
		}
		lineStart := i == 0 || text[i-1] == '\n'
		if r == '—' && !lineStart && !overlaps(skip, i, i+size) {
			d := [2]int{i, i + size}
			all = append(all, d)
			byPara[para] = append(byPara[para], d)
		}
		i += size
	}
	words := len(strings.Fields(text))
	if len(all) >= 3 && len(all)*120 > words {
		return all
	}
	var out [][2]int
	for _, ds := range byPara {
		if len(ds) >= 3 {
			out = append(out, ds...)
		}
	}
	return out
}

func isEmoji(r rune) bool {
	return r >= 0x1F300 && r <= 0x1FAFF || r >= 0x2600 && r <= 0x27BF || r >= 0x1F000 && r <= 0x1F2FF ||
		r >= 0x1F1E6 && r <= 0x1F1FF || r == 0x2B50 || r == 0x2B55 || r == 0x2B06 || r == 0x2934 || r == 0x2935
}

// emojiRuns groups consecutive emoji, including joiners and variation selectors.
func emojiRuns(text string) [][2]int {
	var out [][2]int
	start := -1
	for i, r := range text {
		switch {
		case isEmoji(r):
			if start < 0 {
				start = i
			}
		case start >= 0 && (r == 0x200D || r == 0xFE0F || r >= 0x1F3FB && r <= 0x1F3FF):
		default:
			if start >= 0 {
				out = append(out, [2]int{start, i})
				start = -1
			}
		}
	}
	if start >= 0 {
		out = append(out, [2]int{start, len(text)})
	}
	return out
}

// questionOpener finds the first sentence of the first prose paragraph and
// reports it when it is a question.
func questionOpener(text string) ([2]int, bool) {
	pos := 0
	for _, line := range strings.SplitAfter(text, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "!") || strings.HasPrefix(t, "---") ||
			strings.HasPrefix(t, "[") || fenceOf(line) != "" {
			pos += len(line)
			continue
		}
		start := pos + len(line) - len(strings.TrimLeft(line, " \t"))
		rest := text[start:]
		end := strings.IndexAny(rest, ".!?…\n")
		if end >= 0 && rest[end] == '?' {
			return [2]int{start, start + end + 1}, true
		}
		return [2]int{}, false
	}
	return [2]int{}, false
}

// boldBuzzwords flags bold spans in paragraphs that have three or more.
func boldBuzzwords(text string) [][2]int {
	var out [][2]int
	pos := 0
	for _, p := range strings.SplitAfter(text, "\n\n") {
		if t := strings.TrimSpace(p); !strings.HasPrefix(t, "#") {
			if ms := boldRe.FindAllStringIndex(p, -1); len(ms) >= 3 {
				for _, m := range ms {
					out = append(out, [2]int{pos + m[0], pos + m[1]})
				}
			}
		}
		pos += len(p)
	}
	return out
}
