package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"sap13ns/internal/assistant"
	"sap13ns/internal/store"
)

// A read-only Model Context Protocol server at /mcp (Streamable HTTP,
// JSON-RPC 2.0). Agents get two tools over the site's public content, with
// the assistant's own retrieval and exclusion rules: search_posts and
// get_post. It speaks the stateless 2026-07-28 revision (server/discover,
// the version in every request's _meta) and, for older clients, the
// initialize handshake of the 2025 revisions, without sessions either way.
// It is off by default, may require a bearer token, and is closed while
// Settings.AIAccess is "block".

const (
	mcpVersion = "2026-07-28"
	mcpMaxBody = 64 << 10
	// JSON-RPC and MCP error codes.
	rpcParseError         = -32700
	rpcInvalidRequest     = -32600
	rpcMethodNotFound     = -32601
	rpcInvalidParams      = -32602
	rpcInternalError      = -32603
	mcpHeaderMismatch     = -32020
	mcpUnsupportedVersion = -32022
	mcpMetaVersion        = "io.modelcontextprotocol/protocolVersion"
	mcpMetaServerInfo     = "io.modelcontextprotocol/serverInfo"
)

// mcpLegacyVersions are answered through initialize, newest first.
var mcpLegacyVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26"}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// mcpHit and mcpDoc are what the tools return.
type mcpHit struct {
	Title     string `json:"title"`
	URL       string `json:"url"`
	Lang      string `json:"lang"`
	Section   string `json:"section,omitempty"`
	Published string `json:"published,omitempty"`
	Text      string `json:"text"`
}

type mcpDoc struct {
	Title     string `json:"title"`
	URL       string `json:"url"`
	Lang      string `json:"lang"`
	Type      string `json:"type"`
	Published string `json:"published,omitempty"`
	Updated   string `json:"updated"`
	Excerpt   string `json:"excerpt,omitempty"`
	Markdown  string `json:"markdown"`
}

// mcpBackend is the site's content as the tools see it.
type mcpBackend interface {
	Search(ctx context.Context, query, lang string, limit int) ([]mcpHit, error)
	// Get returns nil when the address is not a post the assistant may use.
	Get(ctx context.Context, address string) (*mcpDoc, error)
}

// mcpHandler answers JSON-RPC messages; HTTP concerns (switch, token, rate
// limit) are handled before it.
type mcpHandler struct {
	backend      mcpBackend
	name         string
	title        string
	version      string
	instructions string
}

var mcpLangRe = regexp.MustCompile(`^[a-z]{2,3}(-[a-z0-9]{2,8})?$`)

func (h *mcpHandler) serverInfo() map[string]string {
	return map[string]string{"name": h.name, "title": h.title, "version": h.version}
}

func (h *mcpHandler) tools() []map[string]any {
	ro := map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false}
	return []map[string]any{
		{
			"name":  "search_posts",
			"title": "Search the site",
			"description": "Search the published posts and pages of " + h.title + ". Returns the most relevant passages, " +
				"each with its page title, URL, language and section. Use get_post for the full text of a page.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{"type": "string", "minLength": 1, "maxLength": assistant.MaxQuestion, "description": "What to look for, in natural language or keywords."},
					"lang":  map[string]any{"type": "string", "pattern": mcpLangRe.String(), "description": "Preferred language (BCP 47, e.g. \"es\"). Other languages are still searched."},
					"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 10, "default": 5, "description": "How many passages to return."},
				},
				"required":             []string{"query"},
				"additionalProperties": false,
			},
			"outputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{"results": map[string]any{"type": "array", "items": map[string]any{
					"type": "object",
					"properties": map[string]any{"title": map[string]any{"type": "string"}, "url": map[string]any{"type": "string"},
						"lang": map[string]any{"type": "string"}, "section": map[string]any{"type": "string"},
						"published": map[string]any{"type": "string"}, "text": map[string]any{"type": "string"}},
					"required": []string{"title", "url", "lang", "text"},
				}}},
				"required": []string{"results"},
			},
			"annotations": ro,
		},
		{
			"name":        "get_post",
			"title":       "Read a page of the site",
			"description": "Return the full Markdown of a published post or page of " + h.title + ", given its URL (as returned by search_posts).",
			"inputSchema": map[string]any{
				"type":                 "object",
				"properties":           map[string]any{"url": map[string]any{"type": "string", "minLength": 1, "maxLength": 2000, "description": "The page's URL or path."}},
				"required":             []string{"url"},
				"additionalProperties": false,
			},
			"outputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{"title": map[string]any{"type": "string"}, "url": map[string]any{"type": "string"},
					"lang": map[string]any{"type": "string"}, "type": map[string]any{"type": "string"},
					"published": map[string]any{"type": "string"}, "updated": map[string]any{"type": "string"},
					"excerpt": map[string]any{"type": "string"}, "markdown": map[string]any{"type": "string"}},
				"required": []string{"title", "url", "lang", "type", "updated", "markdown"},
			},
			"annotations": ro,
		},
	}
}

// handle processes one message. It returns the HTTP status and the
// response, nil for a notification or a client's response (202).
func (h *mcpHandler) handle(ctx context.Context, header http.Header, body []byte) (int, *rpcResponse) {
	fail := func(status int, id json.RawMessage, code int, msg string, data any) (int, *rpcResponse) {
		if len(id) == 0 {
			id = json.RawMessage("null")
		}
		return status, &rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg, Data: data}}
	}
	body = bytes.TrimSpace(body)
	if len(body) > 0 && body[0] == '[' {
		return fail(http.StatusBadRequest, nil, rpcInvalidRequest, "Batches are not supported", nil)
	}
	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return fail(http.StatusBadRequest, nil, rpcParseError, "Parse error", nil)
	}
	if req.Method == "" {
		if len(req.ID) > 0 && req.JSONRPC == "2.0" {
			return http.StatusAccepted, nil // a response to a server request: none are sent
		}
		return fail(http.StatusBadRequest, req.ID, rpcInvalidRequest, "Invalid request", nil)
	}
	if req.JSONRPC != "2.0" {
		return fail(http.StatusBadRequest, req.ID, rpcInvalidRequest, "Invalid request", nil)
	}
	if len(req.ID) == 0 {
		return http.StatusAccepted, nil // notifications (initialized, cancelled) need no answer
	}
	if m := header.Get("Mcp-Method"); m != "" && m != req.Method {
		return fail(http.StatusBadRequest, req.ID, mcpHeaderMismatch, "Mcp-Method header does not match the request", nil)
	}

	var params struct {
		Meta            map[string]json.RawMessage `json:"_meta"`
		ProtocolVersion string                     `json:"protocolVersion"` // initialize
		Name            string                     `json:"name"`            // tools/call
		Arguments       json.RawMessage            `json:"arguments"`
	}
	if len(req.Params) > 0 && string(req.Params) != "null" {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return fail(http.StatusOK, req.ID, rpcInvalidParams, "Invalid params", nil)
		}
	}
	version := header.Get("Mcp-Protocol-Version")
	if raw, ok := params.Meta[mcpMetaVersion]; ok {
		var v string
		if json.Unmarshal(raw, &v) != nil || version != "" && v != version {
			return fail(http.StatusBadRequest, req.ID, mcpHeaderMismatch, "MCP-Protocol-Version header does not match _meta", nil)
		}
		version = v
	}
	supported := append([]string{mcpVersion}, mcpLegacyVersions...)
	if req.Method == "initialize" {
		v := params.ProtocolVersion
		if !slices.Contains(mcpLegacyVersions, v) {
			v = mcpLegacyVersions[0]
		}
		return http.StatusOK, &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"protocolVersion": v, "capabilities": map[string]any{"tools": map[string]bool{"listChanged": false}},
			"serverInfo": h.serverInfo(), "instructions": h.instructions,
		}}
	}
	if version != "" && !slices.Contains(supported, version) {
		return fail(http.StatusBadRequest, req.ID, mcpUnsupportedVersion, "Unsupported protocol version",
			map[string]any{"supported": supported, "requested": version})
	}
	result := map[string]any{"resultType": "complete"}
	if version == mcpVersion {
		result["_meta"] = map[string]any{mcpMetaServerInfo: h.serverInfo()}
	}
	switch req.Method {
	case "server/discover":
		result["supportedVersions"] = supported
		result["capabilities"] = map[string]any{"tools": map[string]bool{"listChanged": false}}
		result["instructions"] = h.instructions
		result["_meta"] = map[string]any{mcpMetaServerInfo: h.serverInfo()}
		result["ttlMs"], result["cacheScope"] = 3_600_000, "public"
	case "ping":
		// 2025 revisions only; harmless to answer.
	case "tools/list":
		result["tools"] = h.tools()
		result["ttlMs"], result["cacheScope"] = 3_600_000, "public"
	case "tools/call":
		content, structured, isErr, rpcErr := h.call(ctx, params.Name, params.Arguments)
		if rpcErr != nil {
			return fail(http.StatusOK, req.ID, rpcErr.Code, rpcErr.Message, rpcErr.Data)
		}
		result["content"] = []map[string]string{{"type": "text", "text": content}}
		if structured != nil {
			result["structuredContent"] = structured
		}
		result["isError"] = isErr
	default:
		return fail(http.StatusOK, req.ID, rpcMethodNotFound, "Method not found: "+limitString(req.Method, 100), nil)
	}
	return http.StatusOK, &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result}
}

// call runs a tool. Bad argument values are tool errors (isError, with a
// message the model can act on); an unknown tool is a protocol error.
func (h *mcpHandler) call(ctx context.Context, name string, raw json.RawMessage) (string, any, bool, *rpcError) {
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage("{}")
	}
	toolErr := func(msg string) (string, any, bool, *rpcError) { return msg, nil, true, nil }
	switch name {
	case "search_posts":
		var a struct {
			Query string `json:"query"`
			Lang  string `json:"lang"`
			Limit *int   `json:"limit"`
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&a); err != nil {
			return toolErr("Invalid arguments: expected {query: string, lang?: string, limit?: integer}.")
		}
		a.Query = strings.TrimSpace(a.Query)
		limit := 5
		if a.Limit != nil {
			limit = *a.Limit
		}
		switch {
		case a.Query == "":
			return toolErr("query must not be empty.")
		case len([]rune(a.Query)) > assistant.MaxQuestion:
			return toolErr("query is too long: at most " + strconv.Itoa(assistant.MaxQuestion) + " characters.")
		case a.Lang != "" && !mcpLangRe.MatchString(a.Lang):
			return toolErr("lang must be a language code such as \"en\" or \"es\".")
		case limit < 1 || limit > 10:
			return toolErr("limit must be between 1 and 10.")
		}
		hits, err := h.backend.Search(ctx, a.Query, a.Lang, limit)
		if err != nil {
			return "", nil, false, &rpcError{Code: rpcInternalError, Message: "Search failed"}
		}
		if len(hits) == 0 {
			return "No published page of the site matches this query.", map[string]any{"results": []mcpHit{}}, false, nil
		}
		var b strings.Builder
		for i, hit := range hits {
			if i > 0 {
				b.WriteString("\n\n---\n\n")
			}
			b.WriteString("[" + strconv.Itoa(i+1) + "] " + hit.Title + "\n" + hit.URL + "\n")
			if hit.Section != "" {
				b.WriteString("Section: " + hit.Section + "\n")
			}
			b.WriteString("Language: " + hit.Lang)
			if hit.Published != "" {
				b.WriteString(" · Published: " + hit.Published)
			}
			b.WriteString("\n\n" + hit.Text)
		}
		return b.String(), map[string]any{"results": hits}, false, nil
	case "get_post":
		var a struct {
			URL string `json:"url"`
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&a); err != nil || strings.TrimSpace(a.URL) == "" || len(a.URL) > 2000 {
			return toolErr("Invalid arguments: expected {url: string}, the URL of a page of this site.")
		}
		doc, err := h.backend.Get(ctx, strings.TrimSpace(a.URL))
		if err != nil {
			return "", nil, false, &rpcError{Code: rpcInternalError, Message: "Lookup failed"}
		}
		if doc == nil {
			return toolErr("No published page of this site has that URL. Use a URL returned by search_posts.")
		}
		text := "# " + doc.Title + "\n\n" + doc.URL + "\n\n"
		if doc.Excerpt != "" {
			text += doc.Excerpt + "\n\n"
		}
		return text + doc.Markdown, doc, false, nil
	}
	return "", nil, false, &rpcError{Code: rpcInvalidParams, Message: "Unknown tool: " + limitString(name, 100)}
}

func limitString(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// --------------------------------------------------------------- HTTP --

// mcpOn reports whether /mcp is open, with the settings.
func (s *Server) mcpOn(ctx context.Context) (store.AssistantSettings, bool) {
	as, err := s.assistantSettings(ctx)
	return as, err == nil && as.MCPEnabled && s.aiOpen()
}

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	as, on := s.mcpOn(r.Context())
	if !on {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if as.MCPTokenHash != "" {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
		if !ok || subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(as.MCPTokenHash)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="sap13ns"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}
	s.assistant() // creates the limiter
	if !s.bot.mcpIP.Allow(ipKey(clientIP(r))) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		http.Error(w, "JSON-RPC over application/json expected", http.StatusUnsupportedMediaType)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, mcpMaxBody))
	if err != nil {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}
	title := s.settings().Text(s.mainLang()).SiteTitle
	h := &mcpHandler{backend: siteMCP{s}, name: "sap13ns", title: title, version: Version,
		instructions: "Read-only access to the published posts and pages of " + title + " (" + s.cfg.AbsURL("/") +
			"). Search with search_posts, then read a page with get_post. Cite the URLs you use."}
	status, resp := h.handle(r.Context(), r.Header, body)
	if resp == nil {
		w.WriteHeader(status)
		return
	}
	writeJSON(w, status, resp)
}

// siteMCP serves the tools from the assistant's index and the posts table.
type siteMCP struct{ s *Server }

func (m siteMCP) Search(ctx context.Context, query, lang string, limit int) ([]mcpHit, error) {
	res, err := m.s.assistant().Search(ctx, query, assistant.SearchOptions{Lang: lang, Limit: limit})
	if err != nil {
		return nil, err
	}
	src := assistant.Sources(res.Hits, func(h assistant.Hit) string { return m.s.cfg.AbsURL(m.s.langPath(h.Lang, postPath(h.Type, h.Slug))) })
	out := make([]mcpHit, len(src))
	for i, x := range src {
		out[i] = mcpHit{Title: x.Title, URL: x.URL, Lang: x.Lang, Section: x.Heading, Text: x.Content}
		if x.Published != nil {
			out[i].Published = x.Published.In(m.s.loc()).Format("2006-01-02")
		}
	}
	return out, nil
}

// Get resolves a URL (absolute on this site, or a path) to a post: /blog/x
// or /x, with an optional language prefix and .md suffix.
func (m siteMCP) Get(ctx context.Context, address string) (*mcpDoc, error) {
	u, err := url.Parse(address)
	if err != nil {
		return nil, nil
	}
	if u.Host != "" && !strings.EqualFold(u.Host, m.s.cfg.BaseURL.Host) {
		return nil, nil
	}
	p := strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), ".md")
	lang := m.s.mainLang()
	if seg, rest, ok := strings.Cut(strings.TrimPrefix(p, "/"), "/"); ok && m.s.isExtraLang(seg) {
		lang, p = seg, "/"+rest
	}
	typ, slug := store.TypePage, strings.TrimPrefix(p, "/")
	if rest, ok := strings.CutPrefix(p, "/blog/"); ok {
		typ, slug = store.TypePost, rest
	}
	if slug == "" || strings.Contains(slug, "/") {
		return nil, nil
	}
	post, err := m.s.store.AssistantPostBySlug(ctx, typ, lang, slug)
	if err != nil || post == nil {
		return nil, err
	}
	doc := &mcpDoc{Title: post.Title, URL: m.s.cfg.AbsURL(m.s.postURL(post)), Lang: post.Lang, Type: post.Type,
		Updated: post.UpdatedAt.In(m.s.loc()).Format(time.RFC3339), Excerpt: post.Excerpt, Markdown: post.Content}
	if post.PublishedAt != nil {
		doc.Published = post.PublishedAt.In(m.s.loc()).Format(time.RFC3339)
	}
	return doc, nil
}
