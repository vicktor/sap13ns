package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"sap13ns/internal/ai"
	"sap13ns/internal/analytics"
	"sap13ns/internal/assistant"
	"sap13ns/internal/auth"
	"sap13ns/internal/sitecheck"
	"sap13ns/internal/store"
)

// The site assistant: visitors ask questions in a chat widget (or on /ask
// without JavaScript) and get answers drawn only from public posts and
// pages, with numbered links to them. Retrieval runs in-process
// (internal/assistant); the answer comes from the AI provider of the
// writing assistant. Nothing is stored about the conversation: the browser
// sends its last turns with every question.

type botState struct {
	once     sync.Once
	engine   *assistant.Engine
	mu       sync.Mutex
	settings *store.AssistantSettings
	perIP    *auth.Limiter // questions per visitor and hour
	perIPN   int
	mcpIP    *auth.Limiter
}

const (
	maxAssistantBody = 48 << 10
	mcpPerMinute     = 60
)

// assistant returns the search engine, created on first use.
func (s *Server) assistant() *assistant.Engine {
	s.bot.once.Do(func() {
		s.bot.engine = assistant.New(s.store, s.keys, s.log)
		s.bot.mcpIP = auth.NewLimiter(mcpPerMinute, time.Minute)
	})
	return s.bot.engine
}

func (s *Server) assistantSettings(ctx context.Context) (store.AssistantSettings, error) {
	s.bot.mu.Lock()
	cached := s.bot.settings
	s.bot.mu.Unlock()
	if cached != nil {
		return *cached, nil
	}
	st, err := s.store.AssistantSettings(ctx)
	if err != nil {
		return st, err
	}
	s.bot.mu.Lock()
	s.bot.settings = &st
	s.bot.mu.Unlock()
	return st, nil
}

// resetAssistant drops cached settings after a save; pages embedding the
// widget and llms.txt change with them.
func (s *Server) resetAssistant() {
	s.bot.mu.Lock()
	s.bot.settings, s.bot.perIP = nil, nil
	s.bot.mu.Unlock()
	s.assistant().Reset()
	s.contentChanged()
}

// assistantPostChanged keeps the index in step with a saved post: one that
// may no longer be used leaves it at once, the rest is reindexed shortly.
func (s *Server) assistantPostChanged(ctx context.Context, id int64) {
	s.assistant().Forget(ctx, id)
}

// assistantOn reports whether visitors can use the assistant: it is
// switched on and the AI provider that writes the answers is too.
func (s *Server) assistantOn(ctx context.Context) (store.AssistantSettings, bool) {
	as, err := s.assistantSettings(ctx)
	if err != nil || !as.Enabled {
		return as, false
	}
	aiSt, err := s.aiSettings(ctx)
	return as, err == nil && aiSt.Enabled
}

// The states of the chat on the site, as the admin shows them.
const (
	chatOn      = "on"
	chatOff     = "off"
	chatWaiting = "waiting" // switched on, but the AI provider is off
)

// chatState tells whether visitors see the chat from the site assistant's
// switch and the AI provider's, which are on two pages.
func chatState(assistantOn, aiOn bool) string {
	switch {
	case !assistantOn:
		return chatOff
	case !aiOn:
		return chatWaiting
	}
	return chatOn
}

// siteChatState is chatState for the saved settings.
func (s *Server) siteChatState(ctx context.Context) (string, error) {
	as, err := s.assistantSettings(ctx)
	if err != nil {
		return "", err
	}
	aiSt, err := s.aiSettings(ctx)
	if err != nil {
		return "", err
	}
	return chatState(as.Enabled, aiSt.Enabled), nil
}

// ipLimiter returns the per-visitor limiter for the configured rate.
func (s *Server) ipLimiter(n int) *auth.Limiter {
	s.bot.mu.Lock()
	defer s.bot.mu.Unlock()
	if s.bot.perIP == nil || s.bot.perIPN != n {
		s.bot.perIP, s.bot.perIPN = auth.NewLimiter(n, time.Hour), n
	}
	return s.bot.perIP
}

// AssistantView is what themes get to show the chat widget.
type AssistantView struct {
	Name        string
	Greeting    string
	Suggestions []string
	Lang        string
	Endpoint    string // POST, JSON or a text/event-stream
	AskURL      string // the page that works without JavaScript; the widget's code is in site.min.js
	// The chat button: its corner (store.BubbleRight or BubbleLeft), its
	// height (BubbleNormal, BubbleRaised or BubbleHigh) and the label
	// beside it, "" for none.
	Position string
	Offset   string
	Label    string
}

// Class is the classes of the widget's root, which place the button.
func (v *AssistantView) Class() string {
	return "sap-assistant sap-assistant-" + v.Position + " sap-assistant-offset-" + v.Offset
}

// bubblePlace returns the saved place of the chat button, or the default
// for a value the page would not save.
func bubblePlace(as store.AssistantSettings) (position, offset string) {
	position, offset = as.BubblePosition, as.BubbleOffset
	if !slices.Contains(store.BubblePositions, position) {
		position = store.BubblePositions[0]
	}
	if !slices.Contains(store.BubbleOffsets, offset) {
		offset = store.BubbleOffsets[0]
	}
	return position, offset
}

// Assistant returns the widget's data, or nil when the assistant is off.
// Themes render it with {{template "shared/assistant" .}}.
func (v *SiteView) Assistant() *AssistantView {
	if v.server == nil {
		return nil
	}
	return v.server.assistantView(v.Lang)
}

func (s *Server) assistantView(lang string) *AssistantView {
	as, on := s.assistantOn(context.Background())
	if !on {
		return nil
	}
	txt := as.In(lang)
	name := as.Name
	if name == "" {
		name = s.i18n.T(lang, "Site assistant")
	}
	greeting := txt.Greeting
	if greeting == "" {
		greeting = s.i18n.T(lang, "Hi! Ask me anything about this site and I will answer from its pages.")
	}
	position, offset := bubblePlace(as)
	return &AssistantView{Name: name, Greeting: greeting, Suggestions: txt.Suggestions, Lang: lang,
		Endpoint: "/api/assistant", AskURL: s.langPath(lang, "/ask"),
		Position: position, Offset: offset, Label: txt.BubbleLabel}
}

// ------------------------------------------------------------- answering --

type assistantReq struct {
	Question string           `json:"question"`
	History  []assistant.Turn `json:"history"`
	Lang     string           `json:"lang"`
}

type assistantReply struct {
	HTML    template.HTML      `json:"html"`
	Text    string             `json:"text"`
	Sources []assistant.Source `json:"sources"`
}

// assistantError is a refusal with a message for the visitor.
type assistantError struct {
	status int
	msg    string
}

func (e *assistantError) Error() string { return e.msg }

// admitQuestion applies the checks every question goes through, whatever
// the transport: assistant on, no known bot, per-visitor rate, length and
// the site-wide budget. It counts the question in the day's usage.
func (s *Server) admitQuestion(r *http.Request, as store.AssistantSettings, question string) error {
	lang := s.reqLang(r)
	if analytics.IsBot(r.UserAgent()) {
		return &assistantError{http.StatusForbidden, s.i18n.T(lang, "Automated clients cannot use the assistant.")}
	}
	if as.PerIPHour > 0 && !s.ipLimiter(as.PerIPHour).Allow(ipKey(clientIP(r))) {
		return &assistantError{http.StatusTooManyRequests, s.i18n.T(lang, "Too many questions. Please wait a while and try again.")}
	}
	if strings.TrimSpace(question) == "" {
		return &assistantError{http.StatusBadRequest, s.i18n.T(lang, "Write a question.")}
	}
	if utf8.RuneCountInString(question) > assistant.MaxQuestion {
		return &assistantError{http.StatusRequestEntityTooLarge, s.i18n.T(lang, "The question is too long (%d characters at most).", assistant.MaxQuestion)}
	}
	now := time.Now()
	day, month, err := s.store.AssistantUsageTotals(r.Context(), now)
	if err != nil {
		return err
	}
	busy := &assistantError{http.StatusTooManyRequests, s.i18n.T(lang, "The assistant has answered all the questions it can for now. Please come back later.")}
	if as.DailyRequests > 0 && day.Requests >= as.DailyRequests || as.MonthlyRequests > 0 && month.Requests >= as.MonthlyRequests {
		return busy
	}
	// The network's AI: within the plan's requests of the day.
	if aiSt, err := s.aiSettings(r.Context()); err != nil {
		return err
	} else if err := s.aiDaily(r.Context(), aiSt, 0); errors.Is(err, errAIDaily) {
		return busy
	} else if err != nil {
		return err
	}
	return s.store.AddAssistantUsage(r.Context(), now, 1, 0, 0, 0)
}

// prepareAnswer retrieves the sources and builds the prompt.
func (s *Server) prepareAnswer(ctx context.Context, as store.AssistantSettings, lang, question string, history []assistant.Turn) (ai.TextRequest, []assistant.Source, int, error) {
	history = assistant.CleanHistory(history)
	res, err := s.assistant().Search(ctx, assistant.SearchQuery(question, history), assistant.SearchOptions{Lang: lang})
	if err != nil {
		return ai.TextRequest{}, nil, 0, err
	}
	sources := assistant.Sources(res.Hits, func(h assistant.Hit) string { return s.langPath(h.Lang, postPath(h.Type, h.Slug)) })
	sources = append(sources, s.liveSources(ctx, lang, assistant.SearchQuery(question, history), len(sources)+1)...)
	name := as.Name
	if name == "" {
		name = s.i18n.T(lang, "Site assistant")
	}
	req := assistant.Prompt(assistant.PromptInput{Bot: name, Site: s.settings().Text(lang).SiteTitle, Lang: lang,
		Now: time.Now().In(s.loc()), Sources: sources, History: history, Question: strings.TrimSpace(question)})
	return req, sources, res.Embeddings, nil
}

func (s *Server) chargeAssistant(m *ai.Metered, embeddings int) {
	u := m.Usage()
	if err := s.store.AddAssistantUsage(context.Background(), time.Now(), 0, int64(u.InputTokens), int64(u.OutputTokens), embeddings); err != nil {
		s.log.Error("assistant usage", "err", err)
	}
	s.chargeDaily(context.Background(), u.Requests, 0)
	s.recordAICalls(context.Background(), "assistant", u)
}

// apiAssistant answers POST /api/assistant. It takes JSON only (cross-site
// forms cannot send it, and CrossOriginProtection blocks cross-origin
// fetches) and answers with a text/event-stream when the client accepts
// one: "delta" events carry the text as it is written, "thinking" marks a
// reasoning model at work, "done" the rendered answer and its sources,
// "error" a failure. Otherwise it answers with one JSON object.
func (s *Server) apiAssistant(w http.ResponseWriter, r *http.Request) {
	fail := func(status int, msg string) { writeJSON(w, status, map[string]string{"error": msg}) }
	as, on := s.assistantOn(r.Context())
	if !on {
		fail(http.StatusNotFound, s.tr(r, "The assistant is not available."))
		return
	}
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		fail(http.StatusUnsupportedMediaType, "JSON expected")
		return
	}
	var req assistantReq
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAssistantBody))
	if err := dec.Decode(&req); err != nil {
		fail(http.StatusBadRequest, s.tr(r, "Invalid request."))
		return
	}
	if req.Lang != "" && slices.Contains(s.siteLangs(), req.Lang) {
		rctx(r).Lang = req.Lang
	}
	lang := s.reqLang(r)
	if err := s.admitQuestion(r, as, req.Question); err != nil {
		var ae *assistantError
		if errors.As(err, &ae) {
			if ae.status == http.StatusTooManyRequests {
				w.Header().Set("Retry-After", "600")
			}
			fail(ae.status, ae.msg)
			return
		}
		s.serverErrorJSON(w, r, err)
		return
	}
	p, _, err := s.aiProvider(r.Context())
	if err != nil || p == nil {
		if err != nil {
			s.log.Error("assistant provider", "err", err)
		}
		fail(http.StatusServiceUnavailable, s.tr(r, "The assistant is not available."))
		return
	}
	aiWriteDeadline(w)
	prompt, sources, embeddings, err := s.prepareAnswer(r.Context(), as, lang, req.Question, req.History)
	if err != nil {
		s.serverErrorJSON(w, r, err)
		return
	}
	m := ai.Meter(p)
	defer s.chargeAssistant(m, embeddings)

	if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		res, err := m.Text(r.Context(), prompt)
		if err != nil {
			fail(http.StatusBadGateway, s.assistantErrorMessage(r, err))
			return
		}
		a := assistant.RenderAnswer(res.Text, sources)
		writeJSON(w, http.StatusOK, assistantReply{HTML: a.HTML, Text: res.Text, Sources: a.Sources})
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	send := func(event string, v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		rc.Flush()
	}
	thinking := false
	res, err := m.TextStream(r.Context(), prompt, func(d ai.Delta) {
		switch {
		case d.Thinking && !thinking:
			thinking = true
			send("thinking", map[string]bool{"thinking": true})
		case !d.Thinking && d.Text != "":
			send("delta", map[string]string{"text": d.Text})
		}
	})
	if err != nil {
		if r.Context().Err() == nil {
			send("error", map[string]string{"error": s.assistantErrorMessage(r, err)})
		}
		return
	}
	a := assistant.RenderAnswer(res.Text, sources)
	send("done", assistantReply{HTML: a.HTML, Text: res.Text, Sources: a.Sources})
}

// assistantErrorMessage tells visitors what went wrong without provider
// details (those go to the log).
func (s *Server) assistantErrorMessage(r *http.Request, err error) string {
	s.log.Warn("assistant answer", "err", err)
	switch {
	case errors.Is(err, ai.ErrRateLimited):
		return s.tr(r, "The assistant is busy right now. Please try again in a minute.")
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, ai.ErrTruncated):
		return s.tr(r, "The answer took too long. Please try again, perhaps with a more specific question.")
	}
	return s.tr(r, "The assistant could not answer. Please try again later.")
}

// ------------------------------------------------------- page without JS --

// AskView is the /ask page: the assistant without JavaScript, one question
// at a time.
type AskView struct {
	*AssistantView
	Question string
	Answer   template.HTML
	Sources  []assistant.Source
	Error    string
}

func (s *Server) handleAsk(w http.ResponseWriter, r *http.Request) {
	as, on := s.assistantOn(r.Context())
	if !on {
		s.notFound(w, r)
		return
	}
	th, pd, err := s.basePage(r, "ask")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	view := s.assistantView(s.reqLang(r))
	if view == nil {
		s.notFound(w, r)
		return
	}
	pd.Title, pd.Heading, pd.NoIndex = view.Name, view.Name, true
	pd.Ask = &AskView{AssistantView: view}
	status := http.StatusOK
	if r.Method == http.MethodPost {
		w.Header().Set("Cache-Control", "no-store")
		r.Body = http.MaxBytesReader(w, r.Body, maxAssistantBody)
		q := strings.TrimSpace(r.FormValue("q"))
		pd.Ask.Question = q
		if err := s.admitQuestion(r, as, q); err != nil {
			var ae *assistantError
			if !errors.As(err, &ae) {
				s.serverError(w, r, err)
				return
			}
			pd.Ask.Error, status = ae.msg, ae.status
		} else if err := s.answerAsk(w, r, as, pd.Ask); err != nil {
			pd.Ask.Error, status = err.Error(), http.StatusBadGateway
		}
	}
	s.renderPage(w, r, th, "ask", pd, status)
}

func (s *Server) answerAsk(w http.ResponseWriter, r *http.Request, as store.AssistantSettings, v *AskView) error {
	p, _, err := s.aiProvider(r.Context())
	if err != nil || p == nil {
		return errors.New(s.tr(r, "The assistant is not available."))
	}
	aiWriteDeadline(w)
	prompt, sources, embeddings, err := s.prepareAnswer(r.Context(), as, s.reqLang(r), v.Question, nil)
	if err != nil {
		s.log.Error("assistant search", "err", err)
		return errors.New(s.tr(r, "The assistant could not answer. Please try again later."))
	}
	m := ai.Meter(p)
	defer s.chargeAssistant(m, embeddings)
	res, err := m.Text(r.Context(), prompt)
	if err != nil {
		return errors.New(s.assistantErrorMessage(r, err))
	}
	a := assistant.RenderAnswer(res.Text, sources)
	v.Answer, v.Sources = a.HTML, a.Sources
	return nil
}

// ------------------------------------------------------------------ admin --

type assistantSettingsData struct {
	S          store.AssistantSettings
	Langs      []LangLink
	Texts      map[string]assistantTextForm
	HasKey     bool
	AIOn       bool
	State      string // of the saved settings: chatOn, chatOff or chatWaiting
	Position   string // of the chat button, for the preview
	Offset     string
	AIProvider string
	Providers  []LangLink
	Models     []ai.Model
	Status     *assistant.Status
	StatusErr  string
	Today      store.AssistantUsage
	Month      store.AssistantUsage
	Days       []store.AssistantUsage
	MCPURL     string
	NewToken   string // shown once, right after it was generated
	Error      string
	Notice     string
}

type assistantTextForm struct {
	Greeting    string
	Suggestions string
	BubbleLabel string
}

func (s *Server) adminAssistantSettings(w http.ResponseWriter, r *http.Request) {
	st, err := s.assistantSettings(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderAssistantSettings(w, r, http.StatusOK, st, "", "", "")
}

func (s *Server) renderAssistantSettings(w http.ResponseWriter, r *http.Request, status int, st store.AssistantSettings, msg, notice, token string) {
	ctx := r.Context()
	aiSt, err := s.aiSettings(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	saved, err := s.assistantSettings(ctx) // st may be a form that failed
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	d := assistantSettingsData{S: st, Langs: s.extraLangs(), Texts: map[string]assistantTextForm{}, HasKey: len(st.EmbedKeyEnc) > 0,
		AIOn: aiSt.Enabled, State: chatState(saved.Enabled, aiSt.Enabled),
		AIProvider: aiSt.Provider, MCPURL: s.cfg.AbsURL("/mcp"), NewToken: token, Error: msg, Notice: notice,
		Providers: []LangLink{{Code: "same", Name: s.tu(r, "The AI provider (AI)")},
			{Code: ai.ProviderOpenAI, Name: s.tu(r, "OpenAI or compatible (LM Studio, Ollama, llama.cpp…)")},
			{Code: ai.ProviderGemini, Name: "Gemini (Google)"}, {Code: "none", Name: s.tu(r, "None: full-text search only")}}}
	for _, p := range []string{ai.ProviderOpenAI, ai.ProviderGemini} {
		d.Models = append(d.Models, ai.EmbedModels(p)...)
	}
	d.Position, d.Offset = bubblePlace(st)
	for _, l := range d.Langs {
		t := st.Text[l.Code]
		d.Texts[l.Code] = assistantTextForm{Greeting: t.Greeting, Suggestions: strings.Join(t.Suggestions, "\n"), BubbleLabel: t.BubbleLabel}
	}
	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if sts, err := s.assistant().Status(sctx); err != nil {
		d.StatusErr = err.Error()
	} else {
		d.Status = &sts
	}
	if d.Today, d.Month, err = s.store.AssistantUsageTotals(ctx, time.Now()); err != nil {
		s.serverError(w, r, err)
		return
	}
	if d.Days, err = s.store.AssistantUsageDays(ctx, 14); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderAdminStatus(w, r, status, "assistant_settings", s.tu(r, "Site assistant"), "assistant", d)
}

// lines splits a textarea into at most n trimmed, non-empty lines.
func lines(v string, n int) []string {
	var out []string
	for _, l := range strings.Split(v, "\n") {
		if l = strings.TrimSpace(l); l != "" && len(out) < n {
			out = append(out, l)
		}
	}
	return out
}

func (s *Server) adminAssistantSettingsSave(w http.ResponseWriter, r *http.Request) {
	old, err := s.assistantSettings(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	st := old
	st.Enabled = r.FormValue("enabled") == "1"
	st.Name, st.Greeting = trimmed(r, "name"), trimmed(r, "greeting")
	st.Suggestions = lines(r.FormValue("suggestions"), 6)
	st.BubblePosition, st.BubbleOffset = r.FormValue("bubble_position"), r.FormValue("bubble_offset")
	st.BubbleLabel = trimmed(r, "bubble_label")
	st.Text = map[string]store.AssistantText{}
	for _, l := range s.extraLangs() {
		t := store.AssistantText{Greeting: trimmed(r, "greeting_"+l.Code), Suggestions: lines(r.FormValue("suggestions_"+l.Code), 6),
			BubbleLabel: trimmed(r, "bubble_label_"+l.Code)}
		if t.Greeting != "" || len(t.Suggestions) > 0 || t.BubbleLabel != "" {
			st.Text[l.Code] = t
		}
	}
	st.PerIPHour = formInt(r, "per_ip_hour", 0)
	st.DailyRequests, st.MonthlyRequests = formInt(r, "daily_requests", 0), formInt(r, "monthly_requests", 0)
	st.EmbedProvider = r.FormValue("embed_provider")
	st.EmbedBaseURL = strings.TrimRight(trimmed(r, "embed_base_url"), "/")
	st.EmbedModel = trimmed(r, "embed_model")
	st.EmbedDimensions = formInt(r, "embed_dimensions", 0)
	st.EmbedAllowLocal = r.FormValue("embed_allow_local") == "1"
	st.MCPEnabled = r.FormValue("mcp_enabled") == "1"

	fail := func(msg string) { s.renderAssistantSettings(w, r, http.StatusUnprocessableEntity, st, msg, "", "") }
	// The rules are shared with restores (sitecheck).
	if bad := sitecheck.Assistant(st); len(bad) > 0 {
		switch bad[0] {
		case "embed_provider":
			fail(s.tu(r, "Choose a provider."))
		case "bubble_position", "bubble_offset":
			fail(s.tu(r, "Choose where the chat button goes."))
		case "per_ip_hour", "daily_requests", "monthly_requests", "embed_dimensions":
			fail(s.tu(r, "Invalid number."))
		case "embed_base_url":
			if tooLong(st.EmbedBaseURL, 300) {
				fail(s.tu(r, "The text is too long."))
			} else {
				fail(s.tu(r, "The address must start with https:// (or http://localhost with local models allowed)."))
			}
		default:
			fail(s.tu(r, "The text is too long."))
		}
		return
	}
	if key := strings.TrimSpace(r.FormValue("embed_api_key")); key != "" {
		if len(key) > 500 {
			fail(s.tu(r, "The text is too long."))
			return
		}
		enc, err := s.keys.Encrypt([]byte(key), []byte(assistant.EmbedKeyAD))
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		st.EmbedKeyEnc = enc
	} else if r.FormValue("remove_embed_key") == "1" || st.EmbedProvider != old.EmbedProvider {
		st.EmbedKeyEnc = nil
	}
	token := ""
	switch {
	case r.FormValue("mcp_new_token") == "1":
		token = "sap13ns_mcp_" + auth.NewToken(24)
		sum := sha256.Sum256([]byte(token))
		st.MCPTokenHash = hex.EncodeToString(sum[:])
	case r.FormValue("mcp_remove_token") == "1":
		st.MCPTokenHash = ""
	}
	if err := s.store.SaveAssistantSettings(r.Context(), st); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.resetAssistant()
	s.audit(r, uid(r), "settings.assistant_saved", st.EmbedProvider, map[string]string{"enabled": strconv.FormatBool(st.Enabled),
		"mcp": strconv.FormatBool(st.MCPEnabled), "mcp_token": strconv.FormatBool(st.MCPTokenHash != "")})
	if token != "" {
		// The token is shown once, on this response, never in a cookie.
		w.Header().Set("Cache-Control", "no-store")
		s.renderAssistantSettings(w, r, http.StatusOK, st, "", s.tu(r, "Settings saved. Copy the new token now: it will not be shown again."), token)
		return
	}
	s.redirect(w, r, "/admin/settings/assistant", "success", s.tu(r, "Settings saved."))
}

// adminAssistantRebuild reindexes every post in the background. Unchanged
// chunks keep their embeddings; reembed discards them too.
func (s *Server) adminAssistantRebuild(w http.ResponseWriter, r *http.Request) {
	opt := assistant.ReconcileOptions{Rechunk: true, Reembed: r.FormValue("reembed") == "1"}
	s.audit(r, uid(r), "assistant.rebuild", "", map[string]string{"reembed": strconv.FormatBool(opt.Reembed)})
	s.goJob(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
		defer cancel()
		if _, err := s.assistant().Reconcile(ctx, opt); err != nil {
			s.log.Error("assistant rebuild", "err", err)
		}
	})
	s.redirect(w, r, "/admin/settings/assistant", "success", s.tu(r, "The index is being rebuilt in the background. Reload this page to follow it."))
}
