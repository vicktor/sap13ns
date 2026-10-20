package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"sap13ns/internal/customize"
	"sap13ns/internal/store"
)

// A personal access token opens the design MCP server to an agent, as its
// user: it reads the guide and the draft, changes the draft through the
// editors' validation, never with the session cookie, and no more once
// revoked.
func TestDesignMCP(t *testing.T) {
	e := newPluginEnv(t)
	files := tinyTheme("mcpy")
	files["theme.json"] = `{"name": "Mcpy", "version": "1.0.0",
  "content": [{"id": "g", "label": "Front", "fields": [{"key": "hello", "label": "Greeting", "type": "text", "default": "Welcome"}]}]}`
	if rec := e.uploadTheme("mcpy.zip", zipOf(t, "mcpy", files, nil)); rec.Code != http.StatusSeeOther {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	if rec := e.admin("/admin/appearance/themes/mcpy/activate", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("activate: %d", rec.Code)
	}

	rec := e.admin("/admin/profile/tokens", url.Values{"name": {"Claude Code"}, "days": {"30"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	token := regexp.MustCompile(`sap13ns_[A-Za-z0-9_-]{20,}`).FindString(rec.Body.String())
	if token == "" || !strings.Contains(rec.Body.String(), "claude mcp add --transport http sap13ns-design") {
		t.Fatal("the new token is not shown")
	}
	if page := e.do("GET", "/admin/profile", nil, "", e.cookie).Body.String(); strings.Contains(page, token) || !strings.Contains(page, "Claude Code") {
		t.Error("the profile shows the token again, or not its name")
	}

	n := 0
	rpc := func(auth, method string, params any) (*httptest.ResponseRecorder, map[string]any) {
		t.Helper()
		n++
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": n, "method": method, "params": params})
		req := httptest.NewRequest("POST", "http://example.test/admin/mcp", strings.NewReader(string(body)))
		req.RemoteAddr = "192.0.2.20:4000"
		req.Header.Set("Content-Type", "application/json")
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		if auth == "cookie" {
			req.Header.Del("Authorization")
			req.AddCookie(e.cookie)
		}
		rec := httptest.NewRecorder()
		e.s.Handler().ServeHTTP(rec, req)
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec, out
	}
	call := func(name string, args map[string]any) (string, bool) {
		t.Helper()
		rec, out := rpc(token, "tools/call", map[string]any{"name": name, "arguments": args})
		res, _ := out["result"].(map[string]any)
		if rec.Code != 200 || res == nil {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body)
		}
		content := res["content"].([]any)[0].(map[string]any)["text"].(string)
		return content, res["isError"] == true
	}

	for _, a := range []string{"", "cookie", "sap13ns_wrong", "not-a-token"} {
		if rec, _ := rpc(a, "tools/list", nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%q: %d", a, rec.Code)
		}
	}
	_, out := rpc(token, "tools/list", nil)
	tools := out["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 3 {
		t.Fatalf("%d tools", len(tools))
	}
	if guide, _ := call("get_design_guide", nil); !strings.Contains(guide, "BLOCKS:") || !strings.Contains(guide, `"key":"hello"`) || !strings.Contains(guide, "change_design") {
		t.Errorf("guide:\n%.500s", guide)
	}
	if state, _ := call("get_design_state", map[string]any{"theme": "mcpy"}); !strings.Contains(state, `"hello":"Welcome"`) || !strings.Contains(state, "?customize=draft") {
		t.Errorf("state:\n%.800s", state)
	}
	if msg, _ := call("get_design_state", map[string]any{"theme": "nope"}); !strings.Contains(msg, "There is no theme") {
		t.Errorf("unknown theme: %s", msg)
	}

	// Refused: nothing saved, and the problem says why.
	msg, isErr := call("change_design", map[string]any{"design_patch": map[string]any{"light": map[string]any{"primary": "blue"}}})
	if !isErr || !strings.Contains(msg, "Nothing was saved") {
		t.Errorf("bad colour: %v %s", isErr, msg)
	}
	row, _ := e.st.ThemeCustom(t.Context(), "mcpy", store.CustomDesign)
	if row.Draft != nil {
		t.Fatal("a refused change was saved")
	}
	msg, isErr = call("change_design", map[string]any{"design_patch": map[string]any{"light": map[string]any{"primary": "#123456"}, "content": map[string]any{"hello": "Aloha"}}})
	if isErr || !strings.Contains(msg, "Saved to the draft") {
		t.Fatalf("change: %v %s", isErr, msg)
	}
	row, _ = e.st.ThemeCustom(t.Context(), "mcpy", store.CustomDesign)
	var d customize.Design
	json.Unmarshal(row.Draft, &d)
	if d.Light["primary"] != "#123456" || d.Content["hello"] != "Aloha" || row.Published != nil && strings.Contains(string(row.Published), "Aloha") {
		t.Errorf("draft: %s, published: %s", row.Draft, row.Published)
	}

	// Revoked: the agent is out.
	toks, _ := e.st.UserTokens(t.Context(), e.adminID)
	if len(toks) != 1 || toks[0].LastUsedAt == nil {
		t.Fatalf("tokens: %+v", toks)
	}
	if rec := e.admin("/admin/profile/tokens/"+itoa(toks[0].ID)+"/delete", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("revoke: %d", rec.Code)
	}
	if rec, _ := rpc(token, "tools/list", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("after revoking: %d", rec.Code)
	}
}

