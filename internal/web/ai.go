package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"sap13ns/internal/ai"
	"sap13ns/internal/auth"
	"sap13ns/internal/content"
	"sap13ns/internal/media"
	"sap13ns/internal/store"
)

// The writing assistant. Text tasks (summary, SEO, writing help) answer
// directly; translations and images take longer and run as jobs the editor
// polls. Every call is checked against the user's monthly budget, metered,
// and written to the audit log. The style linter is local and free.

var aiRoles = []string{auth.RoleContributor, auth.RoleAuthor, auth.RoleEditor, auth.RoleAdmin}

type aiState struct {
	mu       sync.Mutex
	settings *store.AISettings
	provider ai.Provider
	noImages bool // the provider answered an image request with ErrUnsupported
	jobs     map[string]*aiJob
}

type aiJob struct {
	ID       string    `json:"id"`
	UserID   int64     `json:"-"`
	Kind     string    `json:"kind"`
	Done     bool      `json:"done"`
	Error    string    `json:"error,omitempty"`
	Result   any       `json:"result,omitempty"`
	Started  time.Time `json:"started"`
	finished time.Time
}

func (s *Server) resetAI() {
	s.ai.mu.Lock()
	s.ai.settings, s.ai.provider, s.ai.noImages = nil, nil, false
	s.ai.mu.Unlock()
}

func (s *Server) aiSettings(ctx context.Context) (store.AISettings, error) {
	s.ai.mu.Lock()
	cached := s.ai.settings
	s.ai.mu.Unlock()
	if cached != nil {
		return *cached, nil
	}
	st, err := s.store.AISettings(ctx)
	if err != nil {
		return st, err
	}
	s.ai.mu.Lock()
	s.ai.settings = &st
	s.ai.mu.Unlock()
	return st, nil
}

func (s *Server) aiKey(st store.AISettings) (string, error) {
	if len(st.APIKeyEnc) == 0 {
		return "", nil
	}
	b, err := s.keys.Decrypt(st.APIKeyEnc, []byte("ai-api-key"))
	return string(b), err
}

// aiProvider returns the configured provider, or nil when the assistant is
// off or not configured.
func (s *Server) aiProvider(ctx context.Context) (ai.Provider, store.AISettings, error) {
	st, err := s.aiSettings(ctx)
	if err != nil || !st.Enabled {
		return nil, st, err
	}
	s.ai.mu.Lock()
	p := s.ai.provider
	s.ai.mu.Unlock()
	if p != nil {
		return p, st, nil
	}
	key, err := s.aiKey(st)
	if err != nil {
		return nil, st, fmt.Errorf("decrypt AI key: %w", err)
	}
	cfg := ai.Config{Provider: st.Provider, APIKey: key, BaseURL: st.BaseURL, TextModel: st.TextModel,
		ImageModel: st.ImageModel, AllowInsecureLocal: st.AllowLocal, NoThinking: st.NoThinking}
	if st.AllowLocal {
		cfg.Timeout = 5 * time.Minute // local models on a laptop are slow
	}
	p, err = ai.New(cfg, nil)
	if err != nil {
		return nil, st, err
	}
	s.ai.mu.Lock()
	s.ai.provider = p
	s.ai.mu.Unlock()
	return p, st, nil
}

// aiWriteDeadline lifts the server's WriteTimeout for a synchronous AI
// call: local models may take up to their 5-minute text timeout.
func aiWriteDeadline(w http.ResponseWriter) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(6 * time.Minute))
}

// aiAllowed reports whether u may use the assistant at all.
func aiAllowed(st store.AISettings, u *store.User) bool {
	min := slices.Index(aiRoles, st.MinRole)
	if min < 0 {
		min = 1
	}
	return st.Enabled && slices.Index(aiRoles, u.Role) >= min
}

// aiAvailable is used by templates to show the assistant.
func (s *Server) aiAvailable(r *http.Request) bool {
	u := currentUser(r)
	if u == nil {
		return false
	}
	st, err := s.aiSettings(r.Context())
	return err == nil && aiAllowed(st, u)
}

var errBudget = errors.New("budget exceeded")

func (s *Server) aiBudget(ctx context.Context, st store.AISettings, u *store.User, images int) error {
	use, err := s.store.AIUsageOf(ctx, u.ID, time.Now())
	if err != nil {
		return err
	}
	if st.MonthlyRequests > 0 && use.Requests >= st.MonthlyRequests {
		return errBudget
	}
	if images > 0 && st.MonthlyImages > 0 && use.Images+images > st.MonthlyImages {
		return errBudget
	}
	return nil
}

func (s *Server) aiCharge(r *http.Request, u *store.User, task string, m *ai.Metered, ref string) {
	use := m.Usage()
	if err := s.store.AddAIUsage(context.WithoutCancel(r.Context()), u.ID, time.Now(), use.Requests,
		int64(use.InputTokens), int64(use.OutputTokens), use.Images); err != nil {
		s.log.Error("ai usage", "err", err)
	}
	s.audit(r, u.ID, "ai."+task, ref, map[string]string{"requests": strconv.Itoa(use.Requests),
		"tokens": strconv.Itoa(use.InputTokens + use.OutputTokens), "images": strconv.Itoa(use.Images)})
}

// aiErrorMessage turns provider errors into something an editor can act on.
func (s *Server) aiErrorMessage(err error) string {
	switch {
	case errors.Is(err, errBudget):
		return s.t("You have used this month's AI allowance. An administrator can raise it in Settings → AI.")
	case errors.Is(err, media.ErrQuota):
		return s.storageFull()
	case errors.Is(err, ai.ErrAuth):
		return s.t("The AI provider rejected the API key. Check Settings → AI.")
	case errors.Is(err, ai.ErrRateLimited):
		return s.t("The AI provider is busy (rate limit). Try again in a minute.")
	case errors.Is(err, ai.ErrUnsupported):
		return s.t("This provider or model cannot do that. Choose another model in Settings → AI.")
	case errors.Is(err, ai.ErrRefused):
		return s.t("The model declined this request.")
	case errors.Is(err, ai.ErrThinking):
		return s.t("The model used its whole budget thinking. Turn on “Answer without thinking” in Settings → AI, or choose a larger model.")
	case errors.Is(err, ai.ErrTruncated):
		return s.t("The answer was cut short. Try with a shorter text.")
	case errors.Is(err, context.DeadlineExceeded):
		return s.t("The AI provider took too long to answer.")
	}
	return s.t("The AI request failed: %s", truncateErr(err))
}

func truncateErr(err error) string {
	msg := err.Error()
	if len(msg) > 200 {
		msg = msg[:200] + "…"
	}
	return msg
}

type aiReq struct {
	PostID  int64  `json:"post_id"`
	Title   string `json:"title"`
	Excerpt string `json:"excerpt"`
	Content string `json:"content"`
	Lang    string `json:"lang"`
	// writing help
	Mode  string `json:"mode"`
	Text  string `json:"text"`
	Brief string `json:"brief"`
	Tone  string `json:"tone"`
	// images and translation
	Kind  string `json:"kind"` // "header" or "inline"
	Style string `json:"style"`
	To    string `json:"to"`
}

func (s *Server) decodeAIReq(w http.ResponseWriter, r *http.Request) (*aiReq, bool) {
	var req aiReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": s.t("Invalid request.")})
		return nil, false
	}
	if !validLang(req.Lang) {
		req.Lang = s.mainLang()
	}
	return &req, true
}

func (req *aiReq) article() ai.Article {
	return ai.Article{Title: req.Title, Excerpt: req.Excerpt, Content: req.Content, Lang: req.Lang}
}

// apiAI handles the direct text tasks.
func (s *Server) apiAI(w http.ResponseWriter, r *http.Request) {
	aiWriteDeadline(w)
	task := r.PathValue("task")
	req, ok := s.decodeAIReq(w, r)
	if !ok {
		return
	}
	u := currentUser(r)
	if task == "lint" { // local, always available
		writeJSON(w, http.StatusOK, map[string]any{"findings": lintForJS(req.Text, req.Lang)})
		return
	}
	p, st, err := s.aiProvider(r.Context())
	if err != nil || p == nil || !aiAllowed(st, u) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": s.t("The AI assistant is not available.")})
		return
	}
	if err := s.aiBudget(r.Context(), st, u, 0); err != nil {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": s.aiErrorMessage(err)})
		return
	}
	m := ai.Meter(p)
	ctx := r.Context()
	var out any
	switch task {
	case "summary":
		var text string
		text, err = ai.Summarize(ctx, m, req.article(), req.Lang)
		out = map[string]string{"text": text}
	case "seo":
		out, err = ai.SEO(ctx, m, req.article(), req.Lang)
	case "write":
		if !slices.Contains([]string{ai.ModeDraft, ai.ModeImprove, ai.ModeContinue, ai.ModeShorten, ai.ModeOutline}, req.Mode) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": s.t("Invalid request.")})
			return
		}
		var text string
		text, err = ai.Write(ctx, m, ai.WriteRequest{Mode: req.Mode, Text: req.Text, Brief: req.Brief, Lang: req.Lang, Tone: req.Tone})
		out = map[string]any{"text": text, "findings": lintForJS(text, req.Lang)}
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown task"})
		return
	}
	s.aiCharge(r, u, task, m, "#"+strconv.FormatInt(req.PostID, 10))
	if err != nil {
		s.log.Warn("ai task", "task", task, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": s.aiErrorMessage(err)})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// lintForJS converts byte offsets to UTF-16 code units for the browser.
func lintForJS(text, lang string) []map[string]any {
	fs := ai.Lint(text, lang)
	out := make([]map[string]any, 0, len(fs))
	for _, f := range fs {
		out = append(out, map[string]any{"start": utf16Offset(text, f.Start), "end": utf16Offset(text, f.End),
			"phrase": f.Phrase, "rule": f.Rule, "suggestion": f.Suggestion})
	}
	return out
}

func utf16Offset(s string, byteOff int) int {
	n := 0
	for i, r := range s {
		if i >= byteOff {
			break
		}
		n += utf16.RuneLen(r)
	}
	return n
}

// ------------------------------------------------------------------- jobs --

func (s *Server) startAIJob(u *store.User, kind string, run func(ctx context.Context) (any, error)) *aiJob {
	j := &aiJob{ID: auth.NewToken(12), UserID: u.ID, Kind: kind, Started: time.Now()}
	s.ai.mu.Lock()
	if s.ai.jobs == nil {
		s.ai.jobs = map[string]*aiJob{}
	}
	for id, old := range s.ai.jobs { // forget finished jobs after an hour
		if old.Done && time.Since(old.finished) > time.Hour {
			delete(s.ai.jobs, id)
		}
	}
	s.ai.jobs[j.ID] = j
	s.ai.mu.Unlock()
	s.goJob(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		res, err := run(ctx)
		s.ai.mu.Lock()
		j.Done, j.Result, j.finished = true, res, time.Now()
		if err != nil {
			j.Error = s.aiErrorMessage(err)
		}
		s.ai.mu.Unlock()
	})
	return j
}

func (s *Server) apiAIJob(w http.ResponseWriter, r *http.Request) {
	s.ai.mu.Lock()
	j, ok := s.ai.jobs[r.PathValue("job")]
	var snap aiJob
	if ok {
		snap = *j
	}
	s.ai.mu.Unlock()
	if !ok || snap.UserID != uid(r) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

// apiAIImage generates a header or inline image and adds it to the media
// library.
func (s *Server) apiAIImage(w http.ResponseWriter, r *http.Request) {
	req, ok := s.decodeAIReq(w, r)
	if !ok {
		return
	}
	u := currentUser(r)
	p, st, err := s.aiProvider(r.Context())
	if err != nil || p == nil || !aiAllowed(st, u) || !auth.Can(u.Role, auth.CapUploadMedia) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": s.t("The AI assistant is not available.")})
		return
	}
	s.ai.mu.Lock()
	noImages := s.ai.noImages
	s.ai.mu.Unlock()
	if noImages { // fail fast instead of writing a prompt first
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": s.aiErrorMessage(ai.ErrUnsupported)})
		return
	}
	if err := s.aiBudget(r.Context(), st, u, 1); err != nil {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": s.aiErrorMessage(err)})
		return
	}
	aspect := ai.Aspect16x9
	if req.Kind == "inline" {
		aspect = ai.Aspect3x2
	}
	style := strings.TrimSpace(req.Style)
	if style == "" {
		style = st.ImageStyle
	}
	if len(style) > 500 {
		style = style[:500]
	}
	art := req.article()
	if req.Kind == "inline" && strings.TrimSpace(req.Text) != "" {
		art.Content = req.Text // illustrate the selected passage
	}
	rq := r.Clone(context.WithoutCancel(r.Context()))
	j := s.startAIJob(u, "image", func(ctx context.Context) (any, error) {
		m := ai.Meter(p)
		defer s.aiCharge(rq, u, "image", m, "#"+strconv.FormatInt(req.PostID, 10))
		prompt, err := ai.ImagePrompt(ctx, m, art, style)
		if err != nil {
			return nil, err
		}
		imgs, err := m.Image(ctx, ai.ImageRequest{Prompt: prompt, Aspect: aspect, N: 1})
		if errors.Is(err, ai.ErrUnsupported) {
			s.ai.mu.Lock()
			s.ai.noImages = true
			s.ai.mu.Unlock()
		}
		if err != nil {
			return nil, err
		}
		if len(imgs) == 0 {
			return nil, ai.ErrBadResponse
		}
		md, err := s.storeAIImage(ctx, u, imgs[0], req.Title)
		if err != nil {
			return nil, err
		}
		return map[string]any{"media_id": md.ID, "url": md.URL(), "thumb": md.VariantURL("thumb"), "alt": md.Alt, "prompt": prompt}, nil
	})
	writeJSON(w, http.StatusAccepted, map[string]string{"job": j.ID})
}

func (s *Server) storeAIImage(ctx context.Context, u *store.User, img ai.Image, title string) (*store.Media, error) {
	res, err := s.media.Save(ctx, bytes.NewReader(img.Data))
	if err != nil {
		return nil, err
	}
	ext := media.Allowed[res.MIME]
	name := content.Slugify(title)
	if name == "" {
		name = "image"
	}
	m := &store.Media{UploaderID: &u.ID, Path: res.Path, OriginalName: name + "-ai" + ext, MIME: res.MIME, Size: res.Size,
		Width: res.Width, Height: res.Height, Variants: res.Variants, SHA256: res.SHA256, Alt: title}
	if m.Variants == nil {
		m.Variants = map[string]store.MediaVariant{}
	}
	if m.ID, err = s.store.CreateMedia(ctx, m); err != nil {
		s.media.Remove(ctx, m)
		return nil, err
	}
	return m, nil
}

// apiAITranslate translates a post into another site language and saves the
// result as a linked draft.
func (s *Server) apiAITranslate(w http.ResponseWriter, r *http.Request) {
	req, ok := s.decodeAIReq(w, r)
	if !ok {
		return
	}
	u := currentUser(r)
	p, st, err := s.aiProvider(r.Context())
	if err != nil || p == nil || !aiAllowed(st, u) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": s.t("The AI assistant is not available.")})
		return
	}
	src, err := s.store.PostByID(r.Context(), req.PostID)
	if err != nil || !canEditPost(u, src) || !s.isExtraLangOrMain(req.To) || req.To == src.Lang {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": s.t("Invalid request.")})
		return
	}
	if err := s.aiBudget(r.Context(), st, u, 0); err != nil {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": s.aiErrorMessage(err)})
		return
	}
	rq := r.Clone(context.WithoutCancel(r.Context()))
	j := s.startAIJob(u, "translate", func(ctx context.Context) (any, error) {
		m := ai.Meter(p)
		defer s.aiCharge(rq, u, "translate", m, "#"+strconv.FormatInt(src.ID, 10)+" → "+req.To)
		out, err := ai.Translate(ctx, m, ai.Article{Title: src.Title, Excerpt: src.Excerpt, Content: src.Content, Lang: src.Lang}, src.Lang, req.To)
		if err != nil {
			return nil, err
		}
		id, err := s.createTranslation(ctx, u, src, req.To, content.Slugify(out.Title), out.Title, out.Excerpt, out.Content)
		if err != nil {
			return nil, err
		}
		s.contentChanged()
		return map[string]any{"post_id": id, "edit_url": "/admin/posts/" + strconv.FormatInt(id, 10), "lang": req.To}, nil
	})
	writeJSON(w, http.StatusAccepted, map[string]string{"job": j.ID})
}

func (s *Server) isExtraLangOrMain(l string) bool { return slices.Contains(s.siteLangs(), l) }

// createTranslation stores a linked draft of src in lang, pointing a
// sub-page at its parent's translation when there is one.
func (s *Server) createTranslation(ctx context.Context, u *store.User, src *store.Post, lang, slug, title, excerpt, body string) (int64, error) {
	in := postInputOf(src)
	in.Lang, in.TranslationOf, in.Status, in.PublishedAt, in.AuthorID = lang, src.ID, store.StatusDraft, nil, u.ID
	in.Title, in.Excerpt, in.Content = title, excerpt, body
	if src.ParentID != nil {
		in.ParentID = nil
		if parent, err := s.store.PostByID(ctx, *src.ParentID); err == nil {
			if trs, err := s.store.Translations(ctx, parent); err == nil {
				for _, t := range trs {
					if t.Lang == lang {
						id := t.ID
						in.ParentID = &id
					}
				}
			}
		}
	}
	slug, err := s.uniqueSlug(ctx, src.Type, lang, slug, title, 0)
	if err != nil {
		return 0, err
	}
	in.Slug = slug
	return s.store.CreatePost(ctx, in)
}

// ---------------------------------------------------------------- settings --

type aiSettingsData struct {
	S          store.AISettings
	HasKey     bool
	Providers  []LangLink
	TextModels map[string][]ai.Model
	ImgModels  map[string][]ai.Model
	Roles      []string
	Usage      []store.AIUsage
	Error      string
	Notice     string
}

func (s *Server) adminAISettings(w http.ResponseWriter, r *http.Request) {
	st, err := s.aiSettings(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderAISettings(w, r, http.StatusOK, st, "", "")
}

func (s *Server) renderAISettings(w http.ResponseWriter, r *http.Request, status int, st store.AISettings, msg, notice string) {
	usage, err := s.store.AIUsageMonth(r.Context(), time.Now())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	d := aiSettingsData{S: st, HasKey: len(st.APIKeyEnc) > 0, Roles: aiRoles, Usage: usage, Error: msg, Notice: notice,
		Providers: []LangLink{{Code: ai.ProviderAnthropic, Name: "Claude (Anthropic)"}, {Code: ai.ProviderOpenAI, Name: s.t("OpenAI or compatible (Ollama, llama.cpp, OpenRouter…)")},
			{Code: ai.ProviderGemini, Name: "Gemini (Google)"}},
		TextModels: map[string][]ai.Model{}, ImgModels: map[string][]ai.Model{}}
	for _, p := range ai.Providers {
		d.TextModels[p], d.ImgModels[p] = ai.Models(p)
	}
	s.renderAdminStatus(w, r, status, "ai_settings", s.t("AI assistant"), "settings", d)
}

func (s *Server) adminAISettingsSave(w http.ResponseWriter, r *http.Request) {
	old, err := s.aiSettings(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	st := old
	st.Enabled = r.FormValue("enabled") == "1"
	st.Provider = r.FormValue("provider")
	st.BaseURL = strings.TrimRight(trimmed(r, "base_url"), "/")
	st.TextModel, st.ImageModel = trimmed(r, "text_model"), trimmed(r, "image_model")
	st.AllowLocal = r.FormValue("allow_local") == "1"
	st.NoThinking = r.FormValue("no_thinking") == "1"
	st.MinRole = r.FormValue("min_role")
	st.MonthlyRequests, st.MonthlyImages = formInt(r, "monthly_requests", 0), formInt(r, "monthly_images", 0)
	st.ImageStyle = trimmed(r, "image_style")
	fail := func(msg string) { s.renderAISettings(w, r, http.StatusUnprocessableEntity, st, msg, "") }
	switch {
	case !slices.Contains(ai.Providers, st.Provider):
		fail(s.t("Choose a provider."))
		return
	case !slices.Contains(aiRoles, st.MinRole):
		fail(s.t("Invalid request."))
		return
	case st.MonthlyRequests < 0 || st.MonthlyImages < 0 || st.MonthlyRequests > 1_000_000 || st.MonthlyImages > 100_000:
		fail(s.t("Invalid number."))
		return
	case tooLong(st.TextModel, 100) || tooLong(st.ImageModel, 100) || tooLong(st.ImageStyle, 500):
		fail(s.t("The text is too long."))
		return
	}
	if st.BaseURL != "" {
		u, err := url.Parse(st.BaseURL)
		if err != nil || (u.Scheme != "https" && !(u.Scheme == "http" && st.AllowLocal)) || u.Host == "" || u.User != nil {
			fail(s.t("The address must start with https:// (or http://localhost with local models allowed)."))
			return
		}
	}
	if key := strings.TrimSpace(r.FormValue("api_key")); key != "" {
		if len(key) > 500 {
			fail(s.t("The text is too long."))
			return
		}
		enc, err := s.keys.Encrypt([]byte(key), []byte("ai-api-key"))
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		st.APIKeyEnc = enc
	} else if r.FormValue("remove_key") == "1" || st.Provider != old.Provider {
		st.APIKeyEnc = nil
	}
	if err := s.store.SaveAISettings(r.Context(), st); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.resetAI()
	s.audit(r, uid(r), "settings.ai_saved", st.Provider, map[string]string{"enabled": strconv.FormatBool(st.Enabled)})
	if r.FormValue("test") == "1" {
		p, _, err := s.aiProvider(r.Context())
		if err == nil && p == nil {
			err = errors.New(s.t("Turn the assistant on to test it."))
		}
		if err == nil {
			ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
			defer cancel()
			_, err = p.Text(ctx, ai.TextRequest{Messages: []ai.Message{{Role: ai.RoleUser, Content: "Reply with the single word: ok"}}, MaxTokens: 16})
		}
		if err != nil {
			s.renderAISettings(w, r, http.StatusOK, st, s.aiErrorMessage(err), "")
			return
		}
		s.renderAISettings(w, r, http.StatusOK, st, "", s.t("Connection works."))
		return
	}
	s.redirect(w, r, "/admin/settings/ai", "success", s.t("Settings saved."))
}
