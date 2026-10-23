package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// Task helpers. Each builds a prompt, makes one or more Text requests and
// validates the answer; none stores anything. Article text is untrusted
// input to the model, so it travels in the user message, inside tags, and
// the instructions stay in the system prompt.

// Input limits, in characters.
const (
	maxTitleChars     = 500
	maxExcerptChars   = 2_000
	maxInputChars     = 60_000  // bodies sent whole are cut to this
	maxTranslateChars = 200_000 // translated in chunks; longer bodies are refused, never cut
	translateChunk    = 10_000  // bytes per translation request
	maxBriefChars     = 8_000
	maxToneChars      = 120
	maxStyleChars     = 300
)

// Output limits, in characters.
const (
	ExcerptMax        = 300
	SEOTitleMax       = 60
	SEODescriptionMax = 155
	OGTitleMax        = 70
	SlugMax           = 80
	MaxKeywords       = 8
	imagePromptMax    = 900
)

type Article struct {
	Title   string
	Excerpt string
	Content string // Markdown, possibly with shortcodes
	Lang    string // BCP 47 tag, e.g. "es"
}

const StyleGuideES = `Guía de estilo:
- Escribe como un autor humano cuidadoso: concreto, específico, con frases de longitud variada y sin relleno.
- Empieza por lo importante: un hecho, un ejemplo, una escena o una afirmación clara. Nunca por una generalidad sobre la época, el sector o «el mundo».
- Prefiere verbos precisos y sustantivos concretos a los adjetivos grandilocuentes. Un dato vale más que un superlativo.
- No uses estas fórmulas ni otras parecidas: «en el mundo actual», «en la era digital», «sumérgete», «desbloquea», «un viaje», «cabe destacar», «es importante señalar», «en conclusión», «en resumen», «no es solo X, es Y», «¡Vamos allá!», «potencia tu», «revolucionario», «fascinante».
- Evita los tics del texto generado: enumeraciones de tres elementos por reflejo, preguntas retóricas como arranque, emojis, abuso de la raya (—), palabras de moda en negrita y párrafos finales que resumen lo ya dicho.
- No inventes hechos, citas, cifras, fechas, nombres ni fuentes. Si falta un dato, deja un marcador visible [TODO: qué falta] en lugar de suponerlo.
- Respeta la ortografía y la tipografía del español: signos de apertura (¿ ¡), comillas coherentes y mayúscula solo en la primera palabra de los títulos.`

const StyleGuideEN = `Style guide:
- Write like a careful human author: concrete, specific, varied sentence length, no filler.
- Open with what matters: a fact, an example, a scene or a clear claim. Never with a generality about the times, the industry or "the world".
- Prefer precise verbs and concrete nouns to grand adjectives. A figure beats a superlative.
- Do not use these phrases or anything like them: "In today's fast-paced world", "delve", "unlock", "embark", "tapestry", "game-changer", "it's worth noting", "In conclusion", "it's not just X, it's Y".
- Avoid the tics of generated text: triads by reflex, rhetorical questions as openers, emoji, excessive em-dashes, bolded buzzwords and closing paragraphs that summarise what was already said.
- Never invent facts, quotes, statistics, dates, names or sources. When information is missing, leave a visible [TODO: what is missing] instead of guessing.`

// StyleGuide returns the Spanish guide for Spanish and the English one
// otherwise (prompts are in English; the output language is stated apart).
func StyleGuide(lang string) string {
	if primaryLang(lang) == "es" {
		return StyleGuideES
	}
	return StyleGuideEN
}

var (
	langRe    = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8}){0,3}$`)
	langNames = map[string]string{
		"es": "Spanish", "en": "English", "fr": "French", "de": "German", "it": "Italian",
		"pt": "Portuguese", "ca": "Catalan", "gl": "Galician", "eu": "Basque", "nl": "Dutch",
		"pl": "Polish", "ro": "Romanian", "sv": "Swedish", "da": "Danish", "fi": "Finnish",
		"nb": "Norwegian", "cs": "Czech", "el": "Greek", "tr": "Turkish", "ru": "Russian",
		"uk": "Ukrainian", "ja": "Japanese", "zh": "Chinese", "ko": "Korean", "ar": "Arabic",
		"he": "Hebrew", "hi": "Hindi",
	}
)

func primaryLang(tag string) string {
	p, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(tag)), "-")
	return p
}

// describeLang turns a language tag into prompt text such as "Spanish (es)".
// The tag is validated because it is interpolated into instructions.
func describeLang(tag string) (string, error) {
	tag = strings.TrimSpace(tag)
	if !langRe.MatchString(tag) {
		return "", fmt.Errorf("%w: invalid language %q", ErrInvalidInput, truncateRunes(tag, 20))
	}
	if name := langNames[primaryLang(tag)]; name != "" {
		return name + " (" + tag + ")", nil
	}
	return "the language with BCP 47 tag " + tag, nil
}

// DescribeLanguage is describeLang for prompts built outside this package.
func DescribeLanguage(tag string) (string, error) { return describeLang(tag) }

// outputLang describes the requested language, or the article's own one.
func outputLang(tags ...string) (string, string, error) {
	for _, t := range tags {
		if strings.TrimSpace(t) != "" {
			d, err := describeLang(t)
			return t, d, err
		}
	}
	return "", "the same language as the article", nil
}

// limitRunes cuts s to at most n characters (no ellipsis: model input).
func limitRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

var tagEscaper = strings.NewReplacer("</article>", "</ article>", "</content>", "</ content>",
	"</title>", "</ title>", "</excerpt>", "</ excerpt>", "</text>", "</ text>", "</brief>", "</ brief>", "</markdown>", "</ markdown>")

func tagged(name, value string) string {
	return "<" + name + ">\n" + tagEscaper.Replace(value) + "\n</" + name + ">\n"
}

func articleBlock(a Article) string {
	var b strings.Builder
	b.WriteString("<article>\n")
	b.WriteString(tagged("title", limitRunes(strings.TrimSpace(a.Title), maxTitleChars)))
	if e := strings.TrimSpace(a.Excerpt); e != "" {
		b.WriteString(tagged("excerpt", limitRunes(e, maxExcerptChars)))
	}
	b.WriteString(tagged("content", limitRunes(strings.TrimSpace(a.Content), maxInputChars)))
	b.WriteString("</article>")
	return b.String()
}

// ask sends one request and maps refusals, truncation and empty answers to errors.
func ask(ctx context.Context, p Provider, req TextRequest) (*TextResponse, error) {
	res, err := p.Text(ctx, req)
	if err != nil {
		return nil, err
	}
	switch {
	case res.StopReason == StopRefusal || res.StopReason == StopFiltered:
		return nil, fmt.Errorf("%s: %w", p.Name(), ErrRefused)
	case res.StopReason == StopLength:
		return nil, fmt.Errorf("%s: %w", p.Name(), truncated(res))
	case strings.TrimSpace(res.Text) == "":
		return nil, fmt.Errorf("%s: %w: empty answer", p.Name(), ErrBadResponse)
	}
	return res, nil
}

func userMsg(s string) []Message { return []Message{{Role: RoleUser, Content: s}} }

// cleanLine collapses whitespace, drops Markdown emphasis and wrapping quotes.
func cleanLine(s string) string {
	s = strings.NewReplacer("**", "", "__", "", "`", "").Replace(s)
	s = strings.Join(strings.Fields(s), " ")
	for _, q := range [][2]string{{`"`, `"`}, {"“", "”"}, {"«", "»"}, {"'", "'"}, {"‘", "’"}} {
		if len(s) > len(q[0])+len(q[1]) && strings.HasPrefix(s, q[0]) && strings.HasSuffix(s, q[1]) &&
			!strings.Contains(s[len(q[0]):len(s)-len(q[1])], q[1]) {
			s = strings.TrimSpace(s[len(q[0]) : len(s)-len(q[1])])
		}
	}
	return s
}

// stripLabel removes a leading "Label:" the model may add.
func stripLabel(s string, labels ...string) string {
	low := strings.ToLower(s)
	for _, l := range labels {
		if strings.HasPrefix(low, l) {
			return strings.TrimSpace(s[len(l):])
		}
	}
	return s
}

// clipWords cuts s to at most max characters at a word boundary, without
// an ellipsis, and drops dangling punctuation.
func clipWords(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)[:max+1]
	cut := max
	for i := max; i > max/2; i-- {
		if unicode.IsSpace(r[i]) {
			cut = i
			break
		}
	}
	return strings.TrimRight(string(r[:cut]), " ,;:–—-([{«“\"'")
}

// clipSentence keeps whole sentences within max characters when it can,
// otherwise cuts at a word and adds an ellipsis.
func clipSentence(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	for i := max - 1; i >= max/3; i-- {
		if (r[i] == '.' || r[i] == '!' || r[i] == '?' || r[i] == '…') && (i+1 == len(r) || unicode.IsSpace(r[i+1])) {
			return string(r[:i+1])
		}
	}
	return clipWords(s, max-1) + "…"
}

const summarizePrompt = `You write the excerpt shown under a blog post's title in listings, feeds and search results.
Write one or two sentences in %s, at most 280 characters in total, that say what the article actually tells or offers the reader.
No teaser, no question, no hashtags, no quotation marks around it, no Markdown, no label. Use only information in the article.
Output only the excerpt.`

// Summarize returns a one or two sentence excerpt of at most ExcerptMax
// characters, in lang (or the article's language).
func Summarize(ctx context.Context, p Provider, a Article, lang string) (string, error) {
	tag, ln, err := outputLang(lang, a.Lang)
	if err != nil {
		return "", err
	}
	clean := func(t string) string {
		return stripLabel(cleanLine(t), "excerpt:", "extracto:", "resumen:", "summary:")
	}
	res, _, err := askRevised(ctx, p, TextRequest{
		System:    fmt.Sprintf(summarizePrompt, ln) + "\n\n" + StyleGuide(tag),
		Messages:  userMsg(articleBlock(a)),
		MaxTokens: 400,
	}, func(t string) []string {
		s := clean(t)
		return append(lengthProblem("The excerpt", s, ExcerptMax), lintProblems(s, tag)...)
	})
	if err != nil {
		return "", err
	}
	s := clean(res.Text)
	if s == "" {
		return "", fmt.Errorf("%s: %w: empty excerpt", p.Name(), ErrBadResponse)
	}
	return clipSentence(s, ExcerptMax), nil
}

type SEOResult struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Slug        string   `json:"slug"`
	Keywords    []string `json:"keywords"`
	OGTitle     string   `json:"og_title"`
}

// seoSchema avoids length keywords: Claude's structured output rejects them.
var seoSchema = json.RawMessage(`{"type":"object","properties":{` +
	`"title":{"type":"string"},"description":{"type":"string"},"slug":{"type":"string"},` +
	`"keywords":{"type":"array","items":{"type":"string"}},"og_title":{"type":"string"}},` +
	`"required":["title","description","slug","keywords","og_title"],"additionalProperties":false}`)

const seoPrompt = `You write search and social metadata for a blog post. Write the values in %s and reply with a JSON object with these fields:
- "title": the SEO title, at most 60 characters, specific to this article; no clickbait, no site name.
- "description": the meta description, at most 155 characters: one or two plain sentences saying what the reader gets.
- "slug": a URL slug of lowercase ASCII words joined by hyphens, at most 6 words.
- "keywords": up to 8 short search phrases people would actually type, most important first.
- "og_title": the title for social cards, at most 70 characters; it may be a little more conversational than "title".
Use only information in the article.`

// SEO proposes metadata; every field is validated and cut to its limit.
func SEO(ctx context.Context, p Provider, a Article, lang string) (*SEOResult, error) {
	tag, ln, err := outputLang(lang, a.Lang)
	if err != nil {
		return nil, err
	}
	res, _, err := askRevised(ctx, p, TextRequest{
		System:    fmt.Sprintf(seoPrompt, ln) + "\n\n" + StyleGuide(tag),
		Messages:  userMsg(articleBlock(a)),
		MaxTokens: 700,
		JSON:      true,
		Schema:    seoSchema,
	}, func(t string) []string { return seoProblems(t, tag) })
	if err != nil {
		return nil, err
	}
	seo, err := parseSEO(res.Text)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p.Name(), err)
	}
	return seo, nil
}

// seoProblems checks the answer before parseSEO cuts its fields to size.
func seoProblems(raw, lang string) []string {
	obj, err := extractJSON(raw)
	var v struct {
		Title       string   `json:"title"`
		Description string   `json:"description"`
		Keywords    []string `json:"keywords"`
		OGTitle     string   `json:"og_title"`
	}
	if err != nil || json.Unmarshal([]byte(obj), &v) != nil {
		return []string{"It is not the JSON object described: send only that object."}
	}
	var out []string
	if cleanLine(v.Title) == "" || cleanLine(v.Description) == "" {
		out = append(out, `"title" and "description" must not be empty.`)
	}
	out = append(out, lengthProblem(`"title"`, cleanLine(v.Title), SEOTitleMax)...)
	out = append(out, lengthProblem(`"description"`, cleanLine(v.Description), SEODescriptionMax)...)
	out = append(out, lengthProblem(`"og_title"`, cleanLine(v.OGTitle), OGTitleMax)...)
	if len(v.Keywords) > MaxKeywords {
		out = append(out, fmt.Sprintf(`"keywords" has %d phrases; give at most %d.`, len(v.Keywords), MaxKeywords))
	}
	return append(out, lintProblems(v.Title+"\n\n"+v.Description+"\n\n"+v.OGTitle, lang)...)
}

func parseSEO(raw string) (*SEOResult, error) {
	obj, err := extractJSON(raw)
	if err != nil {
		return nil, err
	}
	var v struct {
		Title       string          `json:"title"`
		Description string          `json:"description"`
		Slug        string          `json:"slug"`
		Keywords    json.RawMessage `json:"keywords"`
		OGTitle     string          `json:"og_title"`
	}
	if err := json.Unmarshal([]byte(obj), &v); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadResponse, err)
	}
	r := &SEOResult{
		Title:       clipWords(cleanLine(v.Title), SEOTitleMax),
		Description: clipWords(cleanLine(v.Description), SEODescriptionMax),
		OGTitle:     clipWords(cleanLine(v.OGTitle), OGTitleMax),
	}
	if r.Title == "" || r.Description == "" {
		return nil, fmt.Errorf("%w: SEO answer without title or description", ErrBadResponse)
	}
	if r.OGTitle == "" {
		r.OGTitle = r.Title
	}
	if r.Slug = slugify(v.Slug); r.Slug == "" {
		r.Slug = slugify(r.Title)
	}
	var kws []string
	if json.Unmarshal(v.Keywords, &kws) != nil {
		var s string
		if json.Unmarshal(v.Keywords, &s) == nil {
			kws = strings.Split(s, ",")
		}
	}
	seen := map[string]bool{}
	for _, k := range kws {
		k = clipWords(cleanLine(k), 60)
		key := strings.ToLower(k)
		if k == "" || seen[key] {
			continue
		}
		seen[key] = true
		r.Keywords = append(r.Keywords, k)
		if len(r.Keywords) == MaxKeywords {
			break
		}
	}
	return r, nil
}

var slugTranslit = strings.NewReplacer("ñ", "n", "Ñ", "n", "ß", "ss", "æ", "ae", "Æ", "ae", "ø", "o", "Ø", "o",
	"œ", "oe", "Œ", "oe", "ł", "l", "Ł", "l", "đ", "d", "&", " and ")

// slugify returns lowercase ASCII words joined by hyphens, at most SlugMax
// bytes, cut at a hyphen.
func slugify(s string) string {
	s = norm.NFD.String(slugTranslit.Replace(s))
	var b strings.Builder
	dash := false
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Mn, r):
		case r < utf8.RuneSelf && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			b.WriteRune(unicode.ToLower(r))
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > SlugMax {
		out = out[:SlugMax]
		if i := strings.LastIndexByte(out, '-'); i > SlugMax/2 {
			out = out[:i]
		}
		out = strings.Trim(out, "-")
	}
	return out
}

const translateMetaPrompt = `Translate a blog post's title and excerpt from %s to %s.
Reply with a JSON object {"title": "...", "excerpt": "..."}; leave "excerpt" empty when the source has none.
Keep the meaning, tone and approximate length. Plain text: no quotation marks around the values, no Markdown.`

const translateBodyPrompt = `You are a professional translator. Translate the Markdown document inside <markdown> from %s to %s.
Output only the translated Markdown, without the <markdown> tags and without any comment.
Rules:
- Keep the Markdown structure exactly: the same headings, lists, tables, block quotes, emphasis, line breaks and blank lines.
- Lines such as @@CODE_3@@ stand for code blocks: copy each one unchanged, on its own line.
- Do not translate or change inline code (text between backticks), URLs, link targets, image paths, HTML tags and attributes, or footnote labels. Do translate link text and image alt text.
- Shortcodes look like [name attr="value"]…[/name] or [name /]: copy the opening and closing tags exactly as they are, attribute names and values included, and translate only the text between them.
- Translate the meaning, not word by word, into natural %s as a careful native writer would. Do not add, drop or summarise anything, do not add a title, and do not introduce clichés the source does not have.`

var translateMetaSchema = json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"},"excerpt":{"type":"string"}},"required":["title","excerpt"],"additionalProperties":false}`)

// Translate translates the title, excerpt and Markdown body. Code blocks are
// never sent (placeholders stand in for them), long bodies are translated
// section by section, and the result must pass CheckStructure.
func Translate(ctx context.Context, p Provider, a Article, from, to string) (*Article, error) {
	if strings.TrimSpace(from) == "" {
		from = a.Lang
	}
	src, err := describeLang(from)
	if err != nil {
		return nil, err
	}
	dst, err := describeLang(to)
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(strings.TrimSpace(from), strings.TrimSpace(to)) {
		return nil, fmt.Errorf("%w: source and target language are the same", ErrInvalidInput)
	}
	if utf8.RuneCountInString(a.Content) > maxTranslateChars {
		return nil, fmt.Errorf("%w: article is too long to translate (over %d characters)", ErrInvalidInput, maxTranslateChars)
	}
	out := &Article{Lang: strings.TrimSpace(to)}

	if strings.TrimSpace(a.Title) != "" || strings.TrimSpace(a.Excerpt) != "" {
		res, err := ask(ctx, p, TextRequest{
			System: fmt.Sprintf(translateMetaPrompt, src, dst),
			Messages: userMsg(tagged("title", limitRunes(strings.TrimSpace(a.Title), maxTitleChars)) +
				tagged("excerpt", limitRunes(strings.TrimSpace(a.Excerpt), maxExcerptChars))),
			MaxTokens: 800,
			JSON:      true,
			Schema:    translateMetaSchema,
		})
		if err != nil {
			return nil, err
		}
		var meta struct{ Title, Excerpt string }
		obj, err := extractJSON(res.Text)
		if err == nil {
			err = json.Unmarshal([]byte(obj), &meta)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w: %v", p.Name(), ErrBadResponse, err)
		}
		out.Title, out.Excerpt = cleanLine(meta.Title), cleanLine(meta.Excerpt)
		if out.Title == "" && strings.TrimSpace(a.Title) != "" {
			return nil, fmt.Errorf("%s: %w: empty title", p.Name(), ErrBadResponse)
		}
		if strings.TrimSpace(a.Excerpt) == "" {
			out.Excerpt = ""
		}
	}

	if strings.TrimSpace(a.Content) == "" {
		out.Content = a.Content
		return out, nil
	}
	body, blocks := protectCode(a.Content)
	system := fmt.Sprintf(translateBodyPrompt, src, dst, dst)
	var b strings.Builder
	for _, chunk := range splitSections(body, translateChunk) {
		lead := chunk[:len(chunk)-len(strings.TrimLeft(chunk, " \t\r\n"))]
		trail := chunk[len(strings.TrimRight(chunk, " \t\r\n")):]
		if strings.TrimSpace(placeholderRe.ReplaceAllString(chunk, "")) == "" {
			b.WriteString(chunk) // only code
			continue
		}
		res, _, err := askRevised(ctx, p, TextRequest{
			System:    system,
			Messages:  userMsg(tagged("markdown", strings.TrimSpace(chunk))),
			MaxTokens: min(len(chunk)/2+1024, 16_000),
		}, func(t string) []string { return chunkProblems(chunk, cleanMarkdown(t)) })
		if err != nil {
			return nil, err
		}
		b.WriteString(lead)
		b.WriteString(cleanMarkdown(res.Text))
		b.WriteString(trail)
	}
	restored, err := restoreCode(b.String(), blocks)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p.Name(), err)
	}
	if err := CheckStructure(a.Content, restored); err != nil {
		return nil, fmt.Errorf("%s: %w", p.Name(), err)
	}
	out.Content = restored
	return out, nil
}

// chunkProblems checks the translation of one section: the code
// placeholders and the Markdown structure must survive, and the text must
// have been translated at all.
func chunkProblems(src, dst string) []string {
	var out []string
	want, got := placeholderRe.FindAllString(src, -1), placeholderRe.FindAllString(dst, -1)
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(want, got) {
		out = append(out, "Keep every @@CODE_n@@ placeholder exactly as written, once each, where it was.")
	}
	var se *StructureError
	if err := CheckStructure(src, dst); errors.As(err, &se) {
		for _, p := range se.Problems {
			out = append(out, "The Markdown structure changed ("+p+"): keep headings, links, URLs, inline code and shortcodes as in the source.")
		}
	}
	if runeLen(src) > 200 && strings.TrimSpace(src) == strings.TrimSpace(dst) {
		out = append(out, "The text was not translated: translate it.")
	}
	return out
}

// cleanMarkdown removes wrappers models add around a Markdown answer.
func cleanMarkdown(s string) string {
	s = strings.TrimSpace(unwrapFence(s))
	s = strings.TrimSpace(strings.TrimPrefix(s, "<markdown>"))
	s = strings.TrimSpace(strings.TrimSuffix(s, "</markdown>"))
	return s
}

// DefaultImageStyle is used when ImagePrompt gets no style.
const DefaultImageStyle = "editorial illustration for a thoughtful magazine: restrained palette, one clear focal point, generous negative space, textured and hand-made rather than glossy 3D"

// imageRules are appended to every image prompt, whatever the style.
const imageRules = "No text, letters, numbers, logos, brand marks or watermarks in the image. No real or identifiable people."

const imagePromptTemplate = `You write prompts for an image generation model. Read the article and describe one concrete header image for it: the subject, the setting, the composition and framing, the light, the colour palette and the medium.
Describe a scene that can be drawn, not abstract ideas, and prefer a specific, unexpected visual metaphor to stock clichés (no light bulbs, handshakes, glowing brains, rockets or robots unless the article is about them).
Style: %s.
The image must not contain text, letters, numbers, logos, brand marks, watermarks, user interfaces or real, identifiable people; use anonymous figures if people are needed.
Reply in English with a single paragraph of at most 90 words, without any preamble.`

// ImagePrompt turns the article into a visual prompt for a header image.
func ImagePrompt(ctx context.Context, p Provider, a Article, style string) (string, error) {
	style = strings.Join(strings.Fields(style), " ")
	if style == "" {
		style = DefaultImageStyle
	}
	if utf8.RuneCountInString(style) > maxStyleChars {
		return "", fmt.Errorf("%w: style is too long", ErrInvalidInput)
	}
	res, _, err := askRevised(ctx, p, TextRequest{
		System:    fmt.Sprintf(imagePromptTemplate, style),
		Messages:  userMsg(articleBlock(a)),
		MaxTokens: 400,
	}, func(t string) []string {
		if n := len(strings.Fields(t)); n > 90 {
			return []string{fmt.Sprintf("It has %d words; the limit is 90.", n)}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	s := stripLabel(cleanLine(res.Text), "prompt:", "image prompt:")
	if s == "" {
		return "", fmt.Errorf("%s: %w: empty prompt", p.Name(), ErrBadResponse)
	}
	return clipSentence(s, imagePromptMax) + " " + imageRules, nil
}

// Write modes.
const (
	ModeDraft    = "draft"
	ModeImprove  = "improve"
	ModeContinue = "continue"
	ModeShorten  = "shorten"
	ModeOutline  = "outline"
)

type WriteRequest struct {
	Mode  string
	Text  string // the editor's text (selection or whole body)
	Brief string // what the piece should cover
	Lang  string // empty: the language of the text
	Tone  string // optional, e.g. "cercano", "formal"
}

var writeModes = map[string]struct {
	instruction string
	needsText   bool
	needsBrief  bool
}{
	ModeDraft: {instruction: `Write a first draft of a blog post in Markdown from the brief (and the notes in <text>, if any).
Use ## headings for sections (no # title), short paragraphs, and lists only where they help the reader.
Wherever a fact, figure, quote, example or source is needed and not given, leave a visible [TODO: …] describing what is missing.`, needsBrief: true},
	ModeImprove: {instruction: `Edit the text in <text> for clarity, rhythm and precision. Keep the author's meaning, facts, voice and Markdown structure; remove filler and clichés; do not add new facts.
Return the full revised text only.`, needsText: true},
	ModeContinue: {instruction: `Continue the text in <text> from where it stops, matching its voice, tense and formatting. Write one to three paragraphs, or the next section if it ends on a heading.
Return only the new text, without repeating what is already written. Leave [TODO: …] where a fact you do not have is needed.`, needsText: true},
	ModeShorten: {instruction: `Shorten the text in <text> to about half its length. Keep the key facts, the argument and the author's voice, and the Markdown structure where it still makes sense.
Return only the shortened text.`, needsText: true},
	ModeOutline: {instruction: `Write an outline for a blog post in Markdown from the brief (or from <text>): a list of ## section headings, each followed by two to four bullet points naming the specific points, examples or data to cover.
Mark facts that need checking or are missing with [TODO: …].`},
}

// Write drafts, edits, continues, shortens or outlines text; the answer is Markdown.
func Write(ctx context.Context, p Provider, req WriteRequest) (string, error) {
	mode, ok := writeModes[req.Mode]
	if !ok {
		return "", fmt.Errorf("%w: unknown writing mode %q", ErrInvalidInput, truncateRunes(req.Mode, 20))
	}
	text := limitRunes(strings.TrimSpace(req.Text), maxInputChars)
	brief := strings.TrimSpace(req.Brief)
	if mode.needsText && text == "" || mode.needsBrief && brief == "" || text == "" && brief == "" {
		return "", fmt.Errorf("%w: %s needs %s", ErrInvalidInput, req.Mode, map[bool]string{true: "text", false: "a brief"}[mode.needsText])
	}
	if utf8.RuneCountInString(brief) > maxBriefChars {
		return "", fmt.Errorf("%w: brief is too long", ErrInvalidInput)
	}
	tone := strings.Join(strings.Fields(req.Tone), " ")
	if utf8.RuneCountInString(tone) > maxToneChars {
		return "", fmt.Errorf("%w: tone is too long", ErrInvalidInput)
	}
	tag, ln, err := outputLang(req.Lang)
	if err != nil {
		return "", err
	}
	if tag == "" {
		ln = "the same language as the text"
	}
	system := "You help an author write for their blog. " + mode.instruction + "\nWrite in " + ln + "."
	if tone != "" {
		system += " Tone: " + tone + "."
	}
	system += " Output only Markdown, with no preamble and no closing remarks.\n\n" + StyleGuide(tag)

	var user strings.Builder
	if brief != "" {
		user.WriteString(tagged("brief", brief))
	}
	if text != "" {
		user.WriteString(tagged("text", text))
	}
	maxTokens := 4000
	switch req.Mode {
	case ModeImprove, ModeShorten:
		maxTokens = min(len(text)/2+1024, 16_000)
	case ModeContinue, ModeOutline:
		maxTokens = 1500
	}
	res, left, err := askRevised(ctx, p, TextRequest{System: system, Messages: userMsg(user.String()), MaxTokens: maxTokens},
		func(t string) []string { return writeProblems(req.Mode, text, cleanMarkdown(t), tag) })
	if err != nil {
		return "", err
	}
	out := cleanMarkdown(res.Text)
	if len(left) > 0 {
		out = stripWrapper(out)
	}
	return out, nil
}

// writeProblems checks a Write answer: no remarks to the author around it,
// no clichés, and what the mode asks of its length and form.
func writeProblems(mode, text, out, lang string) []string {
	problems := wrapperProblems(out)
	switch mode {
	case ModeDraft, ModeOutline:
		if strings.HasPrefix(out, "# ") {
			problems = append(problems, "Do not start with a # title: the post has its own title; use ## headings for sections.")
		}
	case ModeShorten:
		if n, m := runeLen(out), runeLen(text); n > m*7/10 {
			problems = append(problems, fmt.Sprintf("It has %d characters and the original %d: shorten it to about half, around %d.", n, m, m/2))
		}
	case ModeContinue:
		tail := strings.TrimSpace(text)
		if r := []rune(tail); len(r) > 120 {
			tail = string(r[len(r)-120:])
		}
		if runeLen(tail) >= 40 && strings.Contains(out, tail) {
			problems = append(problems, "It repeats text already written: send only the new text that follows it.")
		}
	}
	return append(problems, lintProblems(out, lang)...)
}
