package web

// Design with AI (Appearance → Design): the owner asks in plain words for a
// change ("a one-page site about sailing, with animations", "change the
// front page's «Welcome» to «Aloha!»") and the site's AI model answers with
// changes to the theme's draft. The model gets a guide to how sap13ns pages
// are made (designGuide), the catalogue of what this theme can use (blocks,
// its sections, its texts, colours, fonts, animations), and the current
// draft with the texts it shows; it answers one JSON object with merge
// patches. The patches go through the same validation as the editors
// (customize.Parse, builder.Parse); what is refused goes back to the model
// to fix, at most designAIAttempts times. Nothing is published: the owner
// sees the draft in the preview and publishes it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"sap13ns/internal/ai"
	"sap13ns/internal/builder"
	"sap13ns/internal/customize"
	"sap13ns/internal/motion"
	"sap13ns/internal/store"
	"sap13ns/internal/theme"
)

const (
	designAIAttempts  = 3
	designAIMaxTokens = 16000
	designAIMaxTurns  = 12   // conversation turns kept from the client
	designAIMaxChars  = 8000 // per turn
)

// designAISchema is the model's answer. Patches are JSON documents as
// strings so that the schema stays strict; "" means no change.
var designAISchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["reply", "design_patch", "layout_patch", "block_edits"],
  "properties": {
    "reply": {"type": "string"},
    "design_patch": {"type": "string"},
    "layout_patch": {"type": "string"},
    "block_edits": {"type": "array", "items": {
      "type": "object",
      "additionalProperties": false,
      "required": ["block_id", "lang", "settings"],
      "properties": {"block_id": {"type": "string"}, "lang": {"type": "string"}, "settings": {"type": "string"}}
    }}
  }
}`)

type designAIAnswer struct {
	Reply       string `json:"reply"`
	DesignPatch string `json:"design_patch"`
	LayoutPatch string `json:"layout_patch"`
	BlockEdits  []struct {
		BlockID  string `json:"block_id"`
		Lang     string `json:"lang"`
		Settings string `json:"settings"`
	} `json:"block_edits"`
}

// designGuide is how sap13ns pages are made, for the model.
const designGuide = `You are the design assistant of a sap13ns site (a CMS). You change the
design of the site's theme by answering ONE JSON object:

  {"reply": "...", "design_patch": "...", "layout_patch": "...", "block_edits": [...]}

- reply: what you did or what you need, short, in the language the user writes in.
  Say that the changes are in the draft and show in the preview; the user
  publishes them. If something cannot be done with these tools, say so.
- design_patch: "" or a JSON merge patch (RFC 7386) over DESIGN, as a string.
- layout_patch: "" or a JSON merge patch over LAYOUT, as a string. Arrays are
  replaced whole: to change a region, send its complete "sections" list.
  null removes a key: {"pages": {"front": null}} puts the theme's own front
  page back.
- block_edits: small text changes in blocks already in LAYOUT:
  {"block_id": "<id>", "lang": "" for the main language or a language code,
   "settings": "{\"title\": \"...\"}"} (a JSON object as a string, merged).

DESIGN (the "Style and texts" tab):
- light / dark: colour tokens -> "#rrggbb" (TOKENS lists them). Keep enough
  contrast (WCAG AA) between each colour and its "-foreground".
- fonts: {"body": {"stack": S}, "heading": {"stack": S or "body"}}, S in FONTS.
- scale: {"text", "radius", "density", "shadow", "width"} from SCALES.
- motion: {"level": "" | "subtle" | "expressive" | "off"}, and
  {"content": {"<text key>": "<animation>"}} to animate a theme text.
- content: the theme's texts (THEME TEXTS) in the main language, key -> text;
  content_i18n: {"<lang>": {"<key>": "<text>"}} for the other languages.
  An empty string puts the theme's own text back. "lines" texts are one item
  per line. Image keys need a media id: leave them alone.
- css: custom CSS (no @import, no external url()); use it only when asked.

LAYOUT (the "Structure" tab): {"header": R, "footer": R, "pages": {"front": R,
"home": R, "single": R, "page": R, "archive": R, "search": R, "404": R}}, where
a region R is {"sections": [...]}. A missing region keeps the theme's design.
- section: {"id": "s1", "layout": "1" | "1-1" | "2-1" | "1-2" | "1-1-1",
  "anchor": "about" (optional, for #about menu links), "style": {...},
  "columns": [[block, ...], ...]} with one list of blocks per column.
  Style keys and values are in SECTION STYLES; "animation" animates the
  section, and "sequence": "on" plays its blocks one after another.
- block: {"id": "b1", "type": T, "settings": {...}, "i18n": {"<lang>":
  {...}}, "items": [{"settings": {...}, "i18n": {...}}]}. T and its fields
  are in BLOCKS; "where" says which regions accept it, "once" means once per
  region. Fields marked i18n take a translation per language in "i18n"; the
  main language goes in "settings". Every block takes "animation".
- ids: short and unique ("s1", "b1"...). Keep the ids of what you keep.
- The theme's sections (types "theme-...") show the theme's own design and
  texts; their fields override the texts for that copy only.
- A page made with the theme's sections looks like the theme. Prefer them when
  the user wants to keep the look; use the general blocks for new content.
- The front page layout only shows when the site's front page is the theme's
  (STATE says it). If it is not, tell the user to choose it in Settings → Reading.

Rules:
- Change only what the user asks for, and keep everything else as it is.
- To change a text the user quotes ("change «Welcome» to «Aloha»"), find it in
  CURRENT TEXTS or in LAYOUT and change it where it comes from: a theme text
  (design_patch content / content_i18n, in that language), or a block's
  setting (block_edits). A text shown in several languages changes only in the
  language the user means; ask if it is not clear.
- Write real, specific copy for the site's subject; no lorem ipsum, no
  invented figures, testimonials or prices: write placeholders the user will
  replace, and say so.
- Write the texts in the site's main language, and translations for the other
  languages of the site when you create new content.
- Images need media ids that you do not have: leave image fields empty and
  tell the user where to choose the pictures.
- If the validator refuses your changes, you get the errors: send the whole
  answer again, fixed.`

// designAIView is the design screen's assistant panel.
type designAIView struct {
	Model string // the text model that will answer
	Local bool   // a model on this machine or network, likely small
}

// designAIView returns the panel for the current user, or nil when the
// site's AI is off or not for them.
func (s *Server) designAIView(r *http.Request) *designAIView {
	u := currentUser(r)
	if u == nil {
		return nil
	}
	st, err := s.aiSettings(r.Context())
	if err != nil || !aiAllowed(st, u) || (st.Provider == "" && st.BaseURL == "") {
		return nil
	}
	v := &designAIView{Model: st.TextModel}
	if v.Model == "" {
		v.Model = ai.DefaultTextModel(st.Provider)
	}
	if b := strings.ToLower(st.BaseURL); st.AllowLocal || strings.Contains(b, "localhost") || strings.Contains(b, "127.0.0.1") ||
		strings.Contains(b, "://192.168.") || strings.Contains(b, "://10.") || strings.Contains(b, ".local") {
		v.Local = true
	}
	return v
}

// designAIReq is what the design screen sends.
type designAIReq struct {
	Messages []ai.Message `json:"messages"`
	Lang     string       `json:"lang"` // the preview's language
}

func (s *Server) apiDesignAI(w http.ResponseWriter, r *http.Request) {
	aiWriteDeadline(w)
	th, ok := s.themes.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": s.tu(r, "Not found.")})
		return
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": s.tu(r, "Invalid request.")})
		return
	}
	var req designAIReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": s.tu(r, "Invalid request.")})
		return
	}
	msgs := designAIMessages(req.Messages)
	if len(msgs) == 0 || msgs[len(msgs)-1].Role != ai.RoleUser {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": s.tu(r, "Invalid request.")})
		return
	}
	u := currentUser(r)
	p, st, err := s.aiProvider(r.Context())
	if err != nil || p == nil || !aiAllowed(st, u) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": s.tu(r, "The writing assistant is not available.")})
		return
	}
	if err := s.aiBudget(r.Context(), st, u, 0); err != nil {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": s.aiErrorMessage(r, err)})
		return
	}
	rq := r.Clone(context.WithoutCancel(r.Context()))
	j := s.startAIJob(r, u, "design", func(ctx context.Context) (any, error) {
		m := ai.Meter(p)
		defer s.aiCharge(rq, u, "design", m, th.Slug)
		return s.runDesignAI(ctx, rq, m, th, msgs, req.Lang)
	})
	writeJSON(w, http.StatusAccepted, map[string]string{"job": j.ID})
}

// designAIMessages keeps the last turns of the conversation, alternating
// and trimmed.
func designAIMessages(in []ai.Message) []ai.Message {
	var out []ai.Message
	for _, m := range in {
		if m.Role != ai.RoleUser && m.Role != ai.RoleAssistant {
			continue
		}
		c := strings.TrimSpace(m.Content)
		if c == "" {
			continue
		}
		if r := []rune(c); len(r) > designAIMaxChars {
			c = string(r[:designAIMaxChars])
		}
		if n := len(out); n > 0 && out[n-1].Role == m.Role {
			out[n-1].Content += "\n\n" + c
			continue
		}
		out = append(out, ai.Message{Role: m.Role, Content: c})
	}
	for len(out) > designAIMaxTurns {
		out = out[1:]
	}
	for len(out) > 0 && out[0].Role != ai.RoleUser {
		out = out[1:]
	}
	return out
}

// designAIResult is the job's result for the design screen.
type designAIResult struct {
	Reply  string `json:"reply"`
	Design bool   `json:"design"` // the style and texts changed
	Layout bool   `json:"layout"` // the structure changed
}

func (s *Server) runDesignAI(ctx context.Context, r *http.Request, p ai.Provider, th *theme.Theme, msgs []ai.Message, lang string) (*designAIResult, error) {
	ms := s.mediaSet(ctx)
	d, _, err := s.draftDesign(ctx, th, ms)
	if err != nil {
		return nil, err
	}
	l, _, err := s.draftLayout(ctx, th, ms)
	if err != nil {
		return nil, err
	}
	designJSON, layoutJSON := d.JSON(), l.JSON()
	system := designGuide + "\n\n" + s.designAICatalog(th) + "\n\n" + s.designAIState(ctx, th, d, designJSON, layoutJSON, lang)
	conv := slices.Clone(msgs)
	var lastErr error
	for range designAIAttempts {
		resp, err := p.Text(ctx, ai.TextRequest{System: system, Messages: conv, MaxTokens: designAIMaxTokens, JSON: true, Schema: designAISchema})
		if err != nil {
			return nil, err
		}
		if resp.StopReason == ai.StopLength {
			return nil, errDesignAITooLong
		}
		var ans designAIAnswer
		if err := json.Unmarshal([]byte(resp.Text), &ans); err != nil {
			lastErr = ai.ErrBadResponse
			conv = append(conv, ai.Message{Role: ai.RoleAssistant, Content: resp.Text},
				ai.Message{Role: ai.RoleUser, Content: "Your answer was not the JSON object described. Send it again, as that JSON object only."})
			continue
		}
		res, problems, err := s.applyDesignAI(ctx, r, th, ms, designJSON, layoutJSON, &ans)
		if err != nil {
			return nil, err
		}
		if len(problems) == 0 {
			res.Reply = strings.TrimSpace(ans.Reply)
			return res, nil
		}
		lastErr = fmt.Errorf("%w: %s", errDesignAIRefused, strings.Join(problems, "; "))
		conv = append(conv, ai.Message{Role: ai.RoleAssistant, Content: resp.Text},
			ai.Message{Role: ai.RoleUser, Content: "The site refused these changes, so nothing was saved:\n- " + strings.Join(problems, "\n- ") +
				"\nFix them and send the whole answer again."})
	}
	return nil, lastErr
}

var (
	errDesignAITooLong = errors.New("design assistant: the answer was cut off")
	errDesignAIRefused = errors.New("design assistant: changes refused")
)

// applyDesignAI applies the model's answer to the drafts it was shown and
// saves them; problems are what the validators refused (nothing is saved
// then).
func (s *Server) applyDesignAI(ctx context.Context, r *http.Request, th *theme.Theme, ms *mediaSet, designJSON, layoutJSON []byte, ans *designAIAnswer) (*designAIResult, []string, error) {
	var problems []string
	res := &designAIResult{}
	var newDesign *customize.Design
	if p := strings.TrimSpace(ans.DesignPatch); p != "" && p != "{}" && p != "null" {
		merged, err := mergePatchJSON(designJSON, []byte(p))
		if err != nil {
			problems = append(problems, "design_patch: "+err.Error())
		} else if newDesign, err = customize.Parse(merged, s.designContext(th, ms)); err != nil {
			if ms.err != nil {
				return nil, nil, ms.err
			}
			problems = append(problems, validationProblems("design", err)...)
		}
	}
	layout := layoutJSON
	layoutChanged := false
	if p := strings.TrimSpace(ans.LayoutPatch); p != "" && p != "{}" && p != "null" {
		merged, err := mergePatchJSON(layout, []byte(p))
		if err != nil {
			problems = append(problems, "layout_patch: "+err.Error())
		} else {
			layout, layoutChanged = merged, true
		}
	}
	if len(ans.BlockEdits) > 0 {
		edited, errs := editBlocks(layout, ans.BlockEdits)
		problems = append(problems, errs...)
		if len(errs) == 0 {
			layout, layoutChanged = edited, true
		}
	}
	var newLayout *builder.Layouts
	if layoutChanged && len(problems) == 0 {
		ms.prefetch(builder.ImageIDs(layout, themeSectionTypes(th)...))
		var err error
		if newLayout, err = builder.Parse(layout, s.layoutContext(th, ms)); err != nil {
			if ms.err != nil {
				return nil, nil, ms.err
			}
			problems = append(problems, validationProblems("layout", err)...)
		}
	}
	if len(problems) > 0 {
		return nil, problems, nil
	}
	if newDesign != nil && string(newDesign.JSON()) != string(designJSON) {
		if err := s.store.SaveCustomDraft(ctx, th.Slug, store.CustomDesign, newDesign.JSON(), uid(r)); err != nil {
			return nil, nil, err
		}
		res.Design = true
	}
	if newLayout != nil && string(newLayout.JSON()) != string(layoutJSON) {
		if err := s.store.SaveCustomDraft(ctx, th.Slug, store.CustomLayout, newLayout.JSON(), uid(r)); err != nil {
			return nil, nil, err
		}
		res.Layout = true
	}
	if res.Design || res.Layout {
		s.audit(r, uid(r), "theme.design_ai", th.Slug, map[string]string{"design": fmt.Sprint(res.Design), "layout": fmt.Sprint(res.Layout)})
	}
	return res, nil, nil
}

// validationProblems lists a validator's field errors for the model.
func validationProblems(what string, err error) []string {
	var ce customize.Errors
	var be builder.Errors
	var out []string
	switch {
	case errors.As(err, &ce):
		for _, e := range ce {
			out = append(out, fmt.Sprintf("%s %s: %s", what, e.Field, e.Message))
		}
	case errors.As(err, &be):
		for _, e := range be {
			b, _ := json.Marshal(e)
			out = append(out, what+": "+string(b))
		}
	default:
		out = append(out, what+": "+err.Error())
	}
	if len(out) > 40 {
		out = append(out[:40], fmt.Sprintf("and %d more", len(out)-40))
	}
	return out
}

// mergePatchJSON applies an RFC 7386 merge patch to doc.
func mergePatchJSON(doc, patch []byte) ([]byte, error) {
	var d, p any
	if err := json.Unmarshal(doc, &d); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(patch, &p); err != nil {
		return nil, fmt.Errorf("not JSON: %v", err)
	}
	if _, ok := p.(map[string]any); !ok {
		return nil, errors.New("not a JSON object")
	}
	return json.Marshal(mergePatch(d, p))
}

func mergePatch(target, patch any) any {
	pm, ok := patch.(map[string]any)
	if !ok {
		return patch
	}
	tm, ok := target.(map[string]any)
	if !ok {
		tm = map[string]any{}
	}
	for k, v := range pm {
		if v == nil {
			delete(tm, k)
			continue
		}
		tm[k] = mergePatch(tm[k], v)
	}
	return tm
}

// editBlocks merges settings into the blocks of layout with the given ids.
func editBlocks(layout []byte, edits []struct {
	BlockID  string `json:"block_id"`
	Lang     string `json:"lang"`
	Settings string `json:"settings"`
}) ([]byte, []string) {
	var l builder.Layouts
	if err := json.Unmarshal(layout, &l); err != nil {
		return nil, []string{"layout: " + err.Error()}
	}
	regions := []*builder.Region{l.Header, l.Footer}
	for _, k := range slices.Sorted(func(yield func(string) bool) {
		for k := range l.Pages {
			if !yield(k) {
				return
			}
		}
	}) {
		regions = append(regions, l.Pages[k])
	}
	find := func(id string) *builder.Block {
		for _, rg := range regions {
			if rg == nil {
				continue
			}
			for si := range rg.Sections {
				for ci := range rg.Sections[si].Columns {
					for bi := range rg.Sections[si].Columns[ci] {
						if b := &rg.Sections[si].Columns[ci][bi]; b.ID == id {
							return b
						}
					}
				}
			}
		}
		return nil
	}
	var problems []string
	for _, e := range edits {
		b := find(e.BlockID)
		if b == nil {
			problems = append(problems, fmt.Sprintf("block_edits: no block %q in LAYOUT", e.BlockID))
			continue
		}
		var set map[string]string
		if err := json.Unmarshal([]byte(e.Settings), &set); err != nil {
			problems = append(problems, fmt.Sprintf("block_edits %q: settings must be a JSON object of strings", e.BlockID))
			continue
		}
		if b.Settings == nil {
			b.Settings = map[string]string{}
		}
		dst := b.Settings
		if e.Lang != "" {
			if b.I18n == nil {
				b.I18n = map[string]map[string]string{}
			}
			if b.I18n[e.Lang] == nil {
				b.I18n[e.Lang] = map[string]string{}
			}
			dst = b.I18n[e.Lang]
		}
		for k, v := range set {
			dst[k] = v
		}
	}
	if len(problems) > 0 {
		return nil, problems
	}
	out, err := json.Marshal(&l)
	if err != nil {
		return nil, []string{"layout: " + err.Error()}
	}
	return out, nil
}

// designAICatalog is what th can use, for the model.
func (s *Server) designAICatalog(th *theme.Theme) string {
	var b strings.Builder
	langs := append([]string{s.mainLang()}, func() (out []string) {
		for _, l := range s.extraLangs() {
			out = append(out, l.Code)
		}
		return
	}()...)
	fmt.Fprintf(&b, "LANGUAGES: main %q, all %s\n", langs[0], jsonString(langs))
	fmt.Fprintf(&b, "TOKENS: %s\n", jsonString(customize.ColorTokens))
	fmt.Fprintf(&b, "FONTS: %s (heading also \"body\")\n", jsonString(slices.DeleteFunc(slices.Clone(customize.FontStacks), func(f string) bool { return f == "media" })))
	fmt.Fprintf(&b, "SCALES: text %s, radius %s, density %s, shadow %s, width %s\n",
		jsonString(slices.Sorted(mapKeys(customize.TextScales))), jsonString(slices.Sorted(mapKeys(customize.RadiusScales))),
		jsonString(slices.Sorted(mapKeys(customize.DensityScales))), jsonString(slices.Sorted(mapKeys(customize.ShadowScales))),
		jsonString(slices.Sorted(mapKeys(customize.Widths))))
	fmt.Fprintf(&b, "ANIMATIONS: %s (\"none\" for none)\n", jsonString(motion.Names()))
	fmt.Fprintf(&b, "SECTION STYLES: %s\n", jsonString(builder.SectionStyles))
	type field struct {
		Key     string   `json:"key"`
		Type    string   `json:"type"`
		Label   string   `json:"label,omitempty"`
		Choices []string `json:"choices,omitempty"`
		I18n    bool     `json:"i18n,omitempty"`
		Max     int      `json:"max,omitempty"`
	}
	type block struct {
		Type   string   `json:"type"`
		Label  string   `json:"label"`
		Where  []string `json:"where"`
		Once   bool     `json:"once,omitempty"`
		Fields []field  `json:"fields"`
		Items  []field  `json:"items,omitempty"`
		Max    int      `json:"max_items,omitempty"`
	}
	conv := func(fs []builder.Field) (out []field) {
		for _, f := range fs {
			if f.Key == "animation" {
				continue
			}
			c := field{Key: f.Key, Type: f.Type, Label: f.Label, I18n: f.I18n, Max: f.Max}
			for _, ch := range f.Choices {
				c.Choices = append(c.Choices, ch.Value)
			}
			out = append(out, c)
		}
		return
	}
	var blocks []block
	for _, t := range append(slices.Clone(builder.Types), themeSectionTypes(th)...) {
		bl := block{Type: t.Type, Label: t.Label, Where: t.Where, Once: t.Once, Fields: conv(t.Fields)}
		if t.Items != nil {
			bl.Items, bl.Max = conv(t.Items.Fields), t.Items.Max
		}
		blocks = append(blocks, bl)
	}
	fmt.Fprintf(&b, "BLOCKS: %s\n", jsonString(blocks))
	var texts []map[string]string
	for _, g := range th.Content {
		for _, f := range g.Fields {
			texts = append(texts, map[string]string{"key": f.Key, "type": f.Type, "label": g.Label + " · " + f.Label})
		}
	}
	fmt.Fprintf(&b, "THEME TEXTS: %s\n", jsonString(texts))
	return b.String()
}

// designAIState is the current draft, for the model.
func (s *Server) designAIState(ctx context.Context, th *theme.Theme, d *customize.Design, designJSON, layoutJSON []byte, lang string) string {
	var b strings.Builder
	front := s.settings().FrontPage
	fmt.Fprintf(&b, "STATE: theme %q (%s); the site's front page shows %s; the user is looking at the preview in %q.\n",
		th.Slug, th.Name, map[string]string{"theme": "the theme's front page (the front layout)", "page": "a page of the site, not the theme's front page", "posts": "the latest posts, not the theme's front page"}[front], lang)
	fmt.Fprintf(&b, "DESIGN: %s\n", designJSON)
	fmt.Fprintf(&b, "LAYOUT: %s\n", layoutJSON)
	// What the theme's texts say now, per language, so that a quoted text
	// can be found.
	main := s.mainLang()
	langs := []string{main}
	for _, l := range s.extraLangs() {
		langs = append(langs, l.Code)
	}
	ms := s.mediaSet(ctx)
	current := map[string]map[string]string{}
	for _, l := range langs {
		sv := &SiteView{Lang: l, theme: th, server: s, content: &siteContent{design: d, media: ms.lookup, main: main}}
		m := map[string]string{}
		for _, g := range th.Content {
			for _, f := range g.Fields {
				if f.Type != theme.ContentImage {
					if t := sv.Text(f.Key); t != "" {
						m[f.Key] = t
					}
				}
			}
		}
		current[l] = m
	}
	fmt.Fprintf(&b, "CURRENT TEXTS (lang -> key -> what the page shows): %s\n", jsonString(current))
	return b.String()
}

func jsonString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func mapKeys[M ~map[string]V, V any](m M) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}
