package web

// The design MCP server at /admin/mcp: the design assistant's tools for an
// AI agent outside the site (Claude Code, any MCP client), authenticated
// with a personal access token (Profile → Access tokens), never with the
// session cookie. The agent works as the token's user, who must be able
// to change the appearance; like the in-app assistant (design_ai.go) it
// reads the guide and the theme's catalogue, reads the draft, and sends
// merge patches that pass the editors' validation into the draft. It
// never publishes.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	"sap13ns/internal/auth"
	"sap13ns/internal/store"
	"sap13ns/internal/theme"
)

// userTokenPrefix starts every personal access token, so that a leaked one
// is easy to recognise.
const userTokenPrefix = "sap13ns_"

const designMCPIntro = `You are designing the theme of a sap13ns site (a CMS) through its MCP tools:

- get_design_state: the draft (DESIGN, LAYOUT) and CURRENT TEXTS. Read it
  before every change, as the owner may have changed the draft meanwhile.
- change_design: send design_patch (a JSON merge patch, RFC 7386, over
  DESIGN), layout_patch (the same over LAYOUT; arrays are replaced whole, so
  send a region's complete "sections"; null removes a key) and block_edits
  ({"block_id", "lang": "" for the main language or a language code,
  "settings": {...}} merged into a block of LAYOUT). It saves to the draft
  only if everything passes; otherwise it returns the problems to fix.
- Nothing is published: tell the user to check the preview and publish in
  Appearance → Design.

`

// designTools is the toolset of one request.
type designTools struct {
	s *Server
	r *http.Request
}

func (d designTools) list() []map[string]any {
	ro := map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false}
	theme := map[string]any{"type": "string", "description": "The theme's slug; the active theme when left out."}
	return []map[string]any{
		{
			"name": "get_design_guide", "title": "How sap13ns designs work",
			"description": "The guide to sap13ns designs and layouts, and the catalogue of the theme: blocks and their fields, the theme's sections and texts, colour tokens, fonts, scales, animations. Read it first.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"theme": theme}, "additionalProperties": false},
			"annotations": ro,
		},
		{
			"name": "get_design_state", "title": "Read the draft design",
			"description": "The theme's draft design (DESIGN), its layouts (LAYOUT) and what its texts show in every language (CURRENT TEXTS), with the preview's address.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"theme": theme}, "additionalProperties": false},
			"annotations": ro,
		},
		{
			"name": "change_design", "title": "Change the draft design",
			"description": "Apply merge patches to the draft design and layouts, and text edits to blocks. Everything is validated; nothing is saved unless it all passes, and the problems are returned to fix. Never publishes.",
			"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
				"theme":        theme,
				"design_patch": map[string]any{"type": "object", "description": "JSON merge patch over DESIGN."},
				"layout_patch": map[string]any{"type": "object", "description": "JSON merge patch over LAYOUT."},
				"block_edits": map[string]any{"type": "array", "items": map[string]any{"type": "object", "additionalProperties": false,
					"required": []string{"block_id", "settings"}, "properties": map[string]any{
						"block_id": map[string]any{"type": "string"},
						"lang":     map[string]any{"type": "string", "description": "Empty for the main language."},
						"settings": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
					}}},
			}},
			"annotations": map[string]any{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false},
		},
	}
}

func (d designTools) theme(raw json.RawMessage) (*theme.Theme, string, error) {
	var a struct {
		Theme string `json:"theme"`
	}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, "", err
		}
	}
	if a.Theme == "" {
		th := d.s.themes.Active(d.s.settings().ActiveTheme)
		if th == nil {
			return nil, "There is no active theme.", nil
		}
		return th, "", nil
	}
	th, ok := d.s.themes.Get(a.Theme)
	if !ok {
		return nil, fmt.Sprintf("There is no theme %q.", limitString(a.Theme, 60)), nil
	}
	return th, "", nil
}

func (d designTools) call(ctx context.Context, name string, raw json.RawMessage) (string, any, bool, *rpcError) {
	s := d.s
	switch name {
	case "get_design_guide", "get_design_state", "change_design":
	default:
		return "", nil, false, &rpcError{Code: rpcInvalidParams, Message: "Unknown tool: " + limitString(name, 100)}
	}
	th, bad, err := d.theme(raw)
	if err != nil {
		return "", nil, false, &rpcError{Code: rpcInvalidParams, Message: "Invalid arguments"}
	}
	if bad != "" {
		return bad, nil, true, nil
	}
	switch name {
	case "get_design_guide":
		return designMCPIntro + designGuideBody + "\n\n" + s.designAICatalog(th), nil, false, nil
	case "get_design_state":
		ms := s.mediaSet(ctx)
		dz, _, err := s.draftDesign(ctx, th, ms)
		if err != nil {
			return "", nil, false, &rpcError{Code: rpcInternalError, Message: "The draft could not be read"}
		}
		l, _, err := s.draftLayout(ctx, th, ms)
		if err != nil {
			return "", nil, false, &rpcError{Code: rpcInternalError, Message: "The draft could not be read"}
		}
		state := s.designAIState(ctx, th, dz, dz.JSON(), l.JSON(), s.mainLang())
		return state + "\nPREVIEW: " + s.designPreviewURL(th) + " (signed in)\nEDITOR: " +
			s.cfg.AbsURL("/admin/appearance/themes/"+th.Slug+"/customize") + "\n", nil, false, nil
	}
	var a struct {
		DesignPatch json.RawMessage `json:"design_patch"`
		LayoutPatch json.RawMessage `json:"layout_patch"`
		BlockEdits  []struct {
			BlockID  string          `json:"block_id"`
			Lang     string          `json:"lang"`
			Settings json.RawMessage `json:"settings"`
		} `json:"block_edits"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", nil, false, &rpcError{Code: rpcInvalidParams, Message: "Invalid arguments"}
	}
	ans := &designAIAnswer{DesignPatch: patchText(a.DesignPatch), LayoutPatch: patchText(a.LayoutPatch)}
	for _, e := range a.BlockEdits {
		ans.BlockEdits = append(ans.BlockEdits, struct {
			BlockID  string `json:"block_id"`
			Lang     string `json:"lang"`
			Settings string `json:"settings"`
		}{e.BlockID, e.Lang, patchText(e.Settings)})
	}
	ms := s.mediaSet(ctx)
	dz, _, err := s.draftDesign(ctx, th, ms)
	if err != nil {
		return "", nil, false, &rpcError{Code: rpcInternalError, Message: "The draft could not be read"}
	}
	l, _, err := s.draftLayout(ctx, th, ms)
	if err != nil {
		return "", nil, false, &rpcError{Code: rpcInternalError, Message: "The draft could not be read"}
	}
	res, problems, err := s.applyDesignAI(ctx, d.r, th, ms, dz.JSON(), l.JSON(), ans)
	if err != nil {
		s.log.Error("design mcp", "theme", th.Slug, "err", err)
		return "", nil, false, &rpcError{Code: rpcInternalError, Message: "The draft could not be saved"}
	}
	if len(problems) > 0 {
		return "Nothing was saved. Fix these and send the changes again:\n- " + strings.Join(problems, "\n- "),
			map[string]any{"saved": false, "problems": problems}, true, nil
	}
	msg := "No change: the draft already was like that."
	if res.Design || res.Layout {
		msg = "Saved to the draft. The owner checks it in the preview (" + s.designPreviewURL(th) + ") and publishes it in Appearance → Design."
	}
	return msg, map[string]any{"saved": res.Design || res.Layout, "design": res.Design, "layout": res.Layout}, false, nil
}

// patchText is a patch argument as JSON text: an object as it came, or a
// string holding one; "" for none.
func patchText(raw json.RawMessage) string {
	t := strings.TrimSpace(string(raw))
	if t == "" || t == "null" {
		return ""
	}
	if strings.HasPrefix(t, `"`) {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
	}
	return t
}

// designPreviewURL is where a signed-in user sees th's draft.
func (s *Server) designPreviewURL(th *theme.Theme) string {
	if s.themes.Active(s.settings().ActiveTheme) == th {
		return s.cfg.AbsURL("/?customize=draft")
	}
	return s.cfg.AbsURL("/?customize=draft&preview_theme=" + th.Slug)
}

// tokenUser returns the user of the request's personal access token, or
// nil.
func (s *Server) tokenUser(r *http.Request) (*store.User, error) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	token = strings.TrimSpace(token)
	if !ok || !strings.HasPrefix(token, userTokenPrefix) || len(token) > 100 {
		return nil, nil
	}
	t, err := s.store.UserTokenByHash(r.Context(), auth.HashToken(token))
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	u, err := s.store.UserByID(r.Context(), t.UserID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && u.Status != "active") {
		return nil, nil
	}
	return u, err
}

func (s *Server) handleDesignMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	key := ipKey(clientIP(r))
	if s.tokenIP.Blocked(key) {
		w.Header().Set("Retry-After", "600")
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	u, err := s.tokenUser(r)
	if err != nil {
		s.log.Error("design mcp token", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if u == nil {
		s.tokenIP.Allow(key)
		w.Header().Set("WWW-Authenticate", `Bearer realm="sap13ns"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// The same conditions as for having a token (tokens.go): the role may
	// have changed, or a second factor become required, since it was made.
	if u.NetworkOperator || !auth.Can(u.Role, auth.CapAppearance) || s.mustEnroll2FA(r, u) {
		http.Error(w, "forbidden: this user cannot change the appearance with a token", http.StatusForbidden)
		return
	}
	if !s.agentUser.Allow(fmt.Sprint(u.ID)) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		http.Error(w, "JSON-RPC over application/json expected", http.StatusUnsupportedMediaType)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 512<<10))
	if err != nil {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}
	// The agent acts as the token's user, for the audit log and the
	// drafts' authorship; never as a browser session.
	c := rctx(r)
	c.User, c.Session = u, nil
	title := s.settings().Text(s.mainLang()).SiteTitle
	h := &mcpHandler{toolset: designTools{s: s, r: r}, name: "sap13ns-design", title: title + " · design", version: Version,
		instructions: "Design the theme of " + title + " (" + s.cfg.AbsURL("/") + ") as " + u.Username +
			". Read get_design_guide first, then get_design_state, then send change_design. Changes go to the draft; the owner publishes them."}
	status, resp := h.handle(r.Context(), r.Header, body)
	if resp == nil {
		w.WriteHeader(status)
		return
	}
	writeJSON(w, status, resp)
}
