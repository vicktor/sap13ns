package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

type fakeMCPBackend struct {
	query, lang string
	limit       int
	fail        bool
}

func (f *fakeMCPBackend) Search(_ context.Context, query, lang string, limit int) ([]mcpHit, error) {
	if f.fail {
		return nil, errors.New("db down")
	}
	f.query, f.lang, f.limit = query, lang, limit
	if query == "nothing" {
		return nil, nil
	}
	return []mcpHit{{Title: "Horario", URL: "https://example.com/horario", Lang: "es", Section: "Tienda", Text: "Abrimos a las 9."}}, nil
}

func (f *fakeMCPBackend) Get(_ context.Context, address string) (*mcpDoc, error) {
	if address != "https://example.com/horario" {
		return nil, nil // excluded, private or unknown
	}
	return &mcpDoc{Title: "Horario", URL: address, Lang: "es", Type: "page", Updated: "2026-10-18T10:00:00Z", Markdown: "Abrimos a las 9."}, nil
}

func mcpCall(t *testing.T, h *mcpHandler, header http.Header, body string) (int, map[string]any) {
	t.Helper()
	if header == nil {
		header = http.Header{}
	}
	status, resp := h.handle(context.Background(), header, []byte(body))
	if resp == nil {
		return status, nil
	}
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return status, out
}

func result(t *testing.T, resp map[string]any) map[string]any {
	t.Helper()
	r, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %v", resp)
	}
	return r
}

func errCode(resp map[string]any) float64 {
	e, _ := resp["error"].(map[string]any)
	c, _ := e["code"].(float64)
	return c
}

func newTestMCP() (*mcpHandler, *fakeMCPBackend) {
	b := &fakeMCPBackend{}
	return &mcpHandler{backend: b, name: "sap13ns", title: "Mi sitio", version: "test", instructions: "Read-only."}, b
}

func TestMCPLegacyHandshake(t *testing.T) {
	h, _ := newTestMCP()
	status, resp := mcpCall(t, h, nil, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"c","version":"1"}}}`)
	r := result(t, resp)
	if status != 200 || r["protocolVersion"] != "2025-06-18" || r["serverInfo"].(map[string]any)["name"] != "sap13ns" {
		t.Fatalf("initialize: %d %v", status, resp)
	}
	if _, ok := r["capabilities"].(map[string]any)["tools"]; !ok {
		t.Error("tools capability missing")
	}
	// An unknown version gets the newest legacy one.
	_, resp = mcpCall(t, h, nil, `{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"2024-01-01"}}`)
	if result(t, resp)["protocolVersion"] != mcpLegacyVersions[0] {
		t.Errorf("fallback version: %v", resp)
	}
	// Notifications are accepted without a body.
	if status, resp := mcpCall(t, h, nil, `{"jsonrpc":"2.0","method":"notifications/initialized"}`); status != http.StatusAccepted || resp != nil {
		t.Errorf("notification: %d %v", status, resp)
	}
	hdr := http.Header{"Mcp-Protocol-Version": {"2025-06-18"}}
	if _, resp := mcpCall(t, h, hdr, `{"jsonrpc":"2.0","id":3,"method":"ping"}`); resp["error"] != nil {
		t.Errorf("ping: %v", resp)
	}
}

func TestMCPModernDiscoverAndList(t *testing.T) {
	h, _ := newTestMCP()
	hdr := http.Header{"Mcp-Protocol-Version": {mcpVersion}, "Mcp-Method": {"server/discover"}}
	status, resp := mcpCall(t, h, hdr, `{"jsonrpc":"2.0","id":"a","method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"`+mcpVersion+`"}}}`)
	r := result(t, resp)
	if status != 200 || resp["id"] != "a" || r["resultType"] != "complete" || r["supportedVersions"].([]any)[0] != mcpVersion {
		t.Fatalf("discover: %d %v", status, resp)
	}
	if r["_meta"].(map[string]any)[mcpMetaServerInfo].(map[string]any)["version"] != "test" || r["cacheScope"] != "public" {
		t.Errorf("discover meta: %v", r)
	}

	_, resp = mcpCall(t, h, http.Header{"Mcp-Protocol-Version": {mcpVersion}}, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	r = result(t, resp)
	tools := r["tools"].([]any)
	if len(tools) != 2 || r["ttlMs"] == nil {
		t.Fatalf("tools/list: %v", r)
	}
	names := []string{}
	for _, x := range tools {
		tool := x.(map[string]any)
		names = append(names, tool["name"].(string))
		schema := tool["inputSchema"].(map[string]any)
		if schema["type"] != "object" || schema["additionalProperties"] != false || tool["annotations"].(map[string]any)["readOnlyHint"] != true {
			t.Errorf("tool %v", tool)
		}
	}
	if strings.Join(names, ",") != "search_posts,get_post" {
		t.Errorf("tools %v", names)
	}
}

func TestMCPErrors(t *testing.T) {
	h, _ := newTestMCP()
	cases := []struct {
		name   string
		header http.Header
		body   string
		status int
		code   float64
	}{
		{"parse", nil, `{"jsonrpc":`, 400, rpcParseError},
		{"batch", nil, `[{"jsonrpc":"2.0","id":1,"method":"tools/list"}]`, 400, rpcInvalidRequest},
		{"version", nil, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"1999-01-01"}}}`, 400, mcpUnsupportedVersion},
		{"header version", http.Header{"Mcp-Protocol-Version": {"1999-01-01"}}, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, 400, mcpUnsupportedVersion},
		{"meta vs header", http.Header{"Mcp-Protocol-Version": {"2025-06-18"}}, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"` + mcpVersion + `"}}}`, 400, mcpHeaderMismatch},
		{"method header", http.Header{"Mcp-Method": {"tools/call"}}, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, 400, mcpHeaderMismatch},
		{"no jsonrpc", nil, `{"id":1,"method":"tools/list"}`, 400, rpcInvalidRequest},
		{"method", nil, `{"jsonrpc":"2.0","id":1,"method":"resources/list"}`, 200, rpcMethodNotFound},
		{"unknown tool", nil, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"delete_everything","arguments":{}}}`, 200, rpcInvalidParams},
	}
	for _, c := range cases {
		status, resp := mcpCall(t, h, c.header, c.body)
		if status != c.status || errCode(resp) != c.code {
			t.Errorf("%s: %d %v, want %d / %v", c.name, status, resp, c.status, c.code)
		}
	}
	_, resp := mcpCall(t, h, nil, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"1999-01-01"}}}`)
	data := resp["error"].(map[string]any)["data"].(map[string]any)
	if data["requested"] != "1999-01-01" || data["supported"].([]any)[0] != mcpVersion {
		t.Errorf("version error data: %v", data)
	}
}

func TestMCPTools(t *testing.T) {
	h, b := newTestMCP()
	call := func(args string) map[string]any {
		t.Helper()
		_, resp := mcpCall(t, h, nil, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":`+args+`}}`)
		return resp
	}
	r := result(t, call(`"search_posts","arguments":{"query":"  horario  ","lang":"es","limit":3}`))
	if r["isError"] != false || b.query != "horario" || b.lang != "es" || b.limit != 3 {
		t.Fatalf("search: %v (backend got %q %q %d)", r, b.query, b.lang, b.limit)
	}
	text := r["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "[1] Horario\nhttps://example.com/horario") || !strings.Contains(text, "Abrimos a las 9.") {
		t.Errorf("search text: %q", text)
	}
	if res := r["structuredContent"].(map[string]any)["results"].([]any); len(res) != 1 {
		t.Errorf("structured: %v", r["structuredContent"])
	}
	call(`"search_posts","arguments":{"query":"x"}`)
	if b.limit != 5 {
		t.Errorf("default limit %d", b.limit)
	}
	if r := result(t, call(`"search_posts","arguments":{"query":"nothing"}`)); r["isError"] != false ||
		len(r["structuredContent"].(map[string]any)["results"].([]any)) != 0 {
		t.Errorf("no results: %v", r)
	}
	// Bad values are tool errors the model can read.
	for _, args := range []string{
		`"search_posts","arguments":{"query":""}`,
		`"search_posts","arguments":{"query":"x","limit":50}`,
		`"search_posts","arguments":{"query":"x","lang":"<script>"}`,
		`"search_posts","arguments":{"query":"x","extra":1}`,
		`"search_posts","arguments":{"query":"` + strings.Repeat("a", 1001) + `"}`,
		`"get_post","arguments":{}`,
		`"get_post","arguments":{"url":"https://example.com/secret"}`,
	} {
		if r := result(t, call(args)); r["isError"] != true {
			t.Errorf("%s: %v", args, r)
		}
	}
	r = result(t, call(`"get_post","arguments":{"url":"https://example.com/horario"}`))
	if r["isError"] != false || r["structuredContent"].(map[string]any)["markdown"] != "Abrimos a las 9." {
		t.Errorf("get_post: %v", r)
	}
	b.fail = true
	if resp := call(`"search_posts","arguments":{"query":"x"}`); errCode(resp) != rpcInternalError {
		t.Errorf("backend failure: %v", resp)
	}
}
