package plugin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/tetratelabs/wazero/api"

	"sap13ns/internal/store"
)

// Host function return codes (negative values). Non-negative values are the
// length of a result waiting to be copied with `result`.
const (
	codeNotFound int32 = -1
	codeDenied   int32 = -2
	codeInvalid  int32 = -3
	codeQuota    int32 = -4
	codeFailed   int32 = -5
	codeLimit    int32 = -6
)

const (
	kvMaxBytes      = 1 << 20
	kvMaxKeys       = 1000
	kvMaxValue      = 256 << 10
	maxKVOpsPerCall = 200
	maxHTTPPerCall  = 4
	maxDBPerCall    = 10
	httpMaxBody     = 256 << 10
	httpMaxResponse = 1 << 20
)

var (
	metaNameRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9:_.-]{0,63}$`)
	kvKeyRe    = regexp.MustCompile(`^[\x21-\x7e]{1,200}$`)
)

func (h *Host) registerHostModule(ctx context.Context) error {
	_, err := h.rt.NewHostModuleBuilder("sap13ns").
		NewFunctionBuilder().WithFunc(h.hostLog).Export("log").
		NewFunctionBuilder().WithFunc(h.hostResult).Export("result").
		NewFunctionBuilder().WithFunc(h.hostKVGet).Export("kv_get").
		NewFunctionBuilder().WithFunc(h.hostKVSet).Export("kv_set").
		NewFunctionBuilder().WithFunc(h.hostKVDelete).Export("kv_delete").
		NewFunctionBuilder().WithFunc(h.hostPostsQuery).Export("posts_query").
		NewFunctionBuilder().WithFunc(h.hostHTTPFetch).Export("http_fetch").
		Instantiate(ctx)
	return err
}

func state(ctx context.Context) *callState {
	st, _ := ctx.Value(stateKey{}).(*callState)
	return st
}

func readMem(m api.Module, ptr, size uint32, max uint32) ([]byte, bool) {
	if size > max {
		return nil, false
	}
	b, ok := m.Memory().Read(ptr, size)
	if !ok {
		return nil, false
	}
	return bytes.Clone(b), true
}

func (st *callState) granted(perm string) bool {
	return st.plugin.Record != nil && slices.Contains(st.plugin.Record.GrantedPermissions, perm) && slices.Contains(st.plugin.Permissions, perm)
}

func (st *callState) setPending(b []byte) int32 {
	st.pending = b
	return int32(len(b))
}

// log(level, ptr, len)
func (h *Host) hostLog(ctx context.Context, m api.Module, level, ptr, size uint32) {
	st := state(ctx)
	if st == nil {
		return
	}
	b, ok := readMem(m, ptr, size, 4096)
	if !ok {
		return
	}
	levels := []string{"debug", "info", "warn", "error"}
	if level >= uint32(len(levels)) {
		level = 1
	}
	st.plugin.addLog(levels[level], strings.ToValidUTF8(string(b), "?"))
}

// result(ptr, cap) copies the pending result of the previous host call.
func (h *Host) hostResult(ctx context.Context, m api.Module, ptr, capacity uint32) int32 {
	st := state(ctx)
	if st == nil {
		return codeInvalid
	}
	if uint32(len(st.pending)) > capacity {
		return codeInvalid
	}
	if !m.Memory().Write(ptr, st.pending) {
		return codeInvalid
	}
	n := int32(len(st.pending))
	st.pending = nil
	return n
}

func (h *Host) kvKey(ctx context.Context, m api.Module, kptr, klen uint32) (*callState, string, int32) {
	st := state(ctx)
	if st == nil {
		return nil, "", codeInvalid
	}
	if !st.granted(PermKV) {
		return st, "", codeDenied
	}
	st.kvOps++
	if st.kvOps > maxKVOpsPerCall {
		return st, "", codeLimit
	}
	k, ok := readMem(m, kptr, klen, 200)
	if !ok || !kvKeyRe.Match(k) {
		return st, "", codeInvalid
	}
	return st, string(k), 0
}

// kv_get(kptr, klen) -> len | -1 not found
func (h *Host) hostKVGet(ctx context.Context, m api.Module, kptr, klen uint32) int32 {
	st, key, code := h.kvKey(ctx, m, kptr, klen)
	if code != 0 {
		return code
	}
	v, err := h.store.PluginKVGet(ctx, st.plugin.ID, key)
	if err == store.ErrNotFound {
		return codeNotFound
	}
	if err != nil {
		return codeFailed
	}
	return st.setPending(v)
}

// kv_set(kptr, klen, vptr, vlen) -> 0
func (h *Host) hostKVSet(ctx context.Context, m api.Module, kptr, klen, vptr, vlen uint32) int32 {
	st, key, code := h.kvKey(ctx, m, kptr, klen)
	if code != 0 {
		return code
	}
	v, ok := readMem(m, vptr, vlen, kvMaxValue)
	if !ok {
		return codeInvalid
	}
	stored, err := h.store.PluginKVSet(ctx, st.plugin.ID, key, v, kvMaxBytes, kvMaxKeys)
	if err != nil {
		return codeFailed
	}
	if !stored {
		return codeQuota
	}
	return 0
}

// kv_delete(kptr, klen) -> 0
func (h *Host) hostKVDelete(ctx context.Context, m api.Module, kptr, klen uint32) int32 {
	st, key, code := h.kvKey(ctx, m, kptr, klen)
	if code != 0 {
		return code
	}
	if err := h.store.PluginKVDelete(ctx, st.plugin.ID, key); err != nil {
		return codeFailed
	}
	return 0
}

type postsQuery struct {
	Limit    int    `json:"limit"`
	Category string `json:"category"`
	Tag      string `json:"tag"`
	Search   string `json:"search"`
	Type     string `json:"type"`
	Exclude  int64  `json:"exclude"`
}

// posts_query(qptr, qlen) -> len of a JSON array of PostInfo. Only public
// content is ever returned.
func (h *Host) hostPostsQuery(ctx context.Context, m api.Module, qptr, qlen uint32) int32 {
	st := state(ctx)
	if st == nil {
		return codeInvalid
	}
	if !st.granted(PermPostsRead) {
		return codeDenied
	}
	st.dbQueries++
	if st.dbQueries > maxDBPerCall {
		return codeLimit
	}
	raw, ok := readMem(m, qptr, qlen, 4096)
	if !ok {
		return codeInvalid
	}
	var q postsQuery
	if err := json.Unmarshal(raw, &q); err != nil {
		return codeInvalid
	}
	if q.Limit <= 0 || q.Limit > 20 {
		q.Limit = 5
	}
	pq := store.PostQuery{Type: store.TypePost, PublicOnly: true, Limit: q.Limit, WithTerms: true, Search: strings.TrimSpace(q.Search)}
	if q.Type == store.TypePage {
		pq.Type = store.TypePage
	}
	if q.Exclude > 0 {
		pq.ExcludeIDs = []int64{q.Exclude}
	}
	for tax, slug := range map[string]string{"category": q.Category, "tag": q.Tag} {
		if slug == "" {
			continue
		}
		t, err := h.store.TermBySlug(ctx, tax, slug)
		if err != nil {
			return st.setPending([]byte("[]"))
		}
		pq.TermID = t.ID
	}
	posts, _, err := h.store.ListPosts(ctx, pq)
	if err != nil {
		return codeFailed
	}
	out := make([]PostInfo, 0, len(posts))
	for i := range posts {
		out = append(out, h.links.PostInfo(&posts[i]))
	}
	b, _ := json.Marshal(out)
	return st.setPending(b)
}

type httpRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

type httpResponse struct {
	Status     int               `json:"status"`
	Headers    map[string]string `json:"headers"`
	Body       string            `json:"body,omitempty"`
	BodyBase64 string            `json:"body_base64,omitempty"`
	Error      string            `json:"error,omitempty"`
}

var forbiddenHeaders = map[string]bool{
	"host": true, "connection": true, "content-length": true, "transfer-encoding": true, "upgrade": true,
	"te": true, "trailer": true, "proxy-authorization": true, "proxy-connection": true, "keep-alive": true,
	"cookie": true, "x-forwarded-for": true, "x-real-ip": true, "forwarded": true,
}

// http_fetch(reqptr, reqlen) -> len of a JSON httpResponse. Only https URLs on
// hosts granted by "http:<host>" permissions, only from event hooks, never to
// private or loopback addresses.
func (h *Host) hostHTTPFetch(ctx context.Context, m api.Module, rptr, rlen uint32) int32 {
	st := state(ctx)
	if st == nil {
		return codeInvalid
	}
	if !st.allowHTTP {
		return codeDenied
	}
	st.httpCalls++
	if st.httpCalls > maxHTTPPerCall {
		return codeLimit
	}
	raw, ok := readMem(m, rptr, rlen, httpMaxBody+16<<10)
	if !ok {
		return codeInvalid
	}
	var req httpRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return codeInvalid
	}
	fail := func(msg string) int32 {
		b, _ := json.Marshal(httpResponse{Error: msg})
		return st.setPending(b)
	}
	u, err := url.Parse(req.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return fail("only absolute https:// URLs without credentials are allowed")
	}
	granted := []string{}
	if st.plugin.Record != nil {
		for _, g := range st.plugin.Record.GrantedPermissions {
			if slices.Contains(st.plugin.Permissions, g) {
				granted = append(granted, g)
			}
		}
	}
	if !hostAllowed(granted, u.Hostname()) {
		return codeDenied
	}
	method := strings.ToUpper(req.Method)
	if method == "" {
		method = http.MethodGet
	}
	if !slices.Contains([]string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE"}, method) {
		return fail("method not allowed")
	}
	if len(req.Body) > httpMaxBody {
		return fail("request body too large")
	}
	hreq, err := http.NewRequestWithContext(context.WithValue(ctx, grantKey{}, granted), method, u.String(), strings.NewReader(req.Body))
	if err != nil {
		return fail(err.Error())
	}
	n := 0
	for k, v := range req.Headers {
		if n >= 20 || forbiddenHeaders[strings.ToLower(k)] || strings.ContainsAny(k+v, "\r\n") {
			continue
		}
		hreq.Header.Set(k, v)
		n++
	}
	hreq.Header.Set("User-Agent", "sap13ns-plugin/"+st.plugin.ID)
	resp, err := h.httpc.Do(hreq)
	if err != nil {
		return fail(err.Error())
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, httpMaxResponse+1))
	if err != nil {
		return fail(err.Error())
	}
	if len(body) > httpMaxResponse {
		return fail("response too large")
	}
	out := httpResponse{Status: resp.StatusCode, Headers: map[string]string{}}
	for k := range resp.Header {
		if len(out.Headers) < 30 {
			out.Headers[strings.ToLower(k)] = resp.Header.Get(k)
		}
	}
	if utf8.Valid(body) {
		out.Body = string(body)
	} else {
		out.BodyBase64 = base64.StdEncoding.EncodeToString(body)
	}
	b, _ := json.Marshal(out)
	return st.setPending(b)
}
