package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"sap13ns/internal/ai"
	"sap13ns/internal/store"
)

// planLimits is a site of a network whose plan gives plan.
type planLimits struct {
	SiteLimits // nil: only AIPlan is called
	plan       AIPlan
}

func (p planLimits) AIPlan(context.Context) (AIPlan, error) { return p.plan, nil }

// The network's AI: only a paid plan may choose it, it works within the
// plan's requests and images of the day, and a site that loses the plan
// sees its AI switched off; its own provider is free of those limits.
func TestAIPlan(t *testing.T) {
	e := newPluginEnv(t)
	ctx := context.Background()
	setPlan := func(p AIPlan) {
		e.s.limits = planLimits{plan: p}
		e.s.ai.mu.Lock()
		e.s.ai.plan = nil
		e.s.ai.mu.Unlock()
	}
	free := AIPlan{Plan: "Personal"}
	pro := AIPlan{Plan: "Pro", Paid: true, Text: &ai.Config{Provider: "anthropic", APIKey: "k", TextModel: "claude-net"}, RequestsPerDay: 1, ImagesPerDay: 0}
	setPlan(free)

	save := func(v url.Values) int {
		t.Helper()
		v.Set("provider", "anthropic")
		v.Set("min_role", "author")
		return e.admin("/admin/settings/ai", v).Code
	}
	if page := e.do("GET", "/admin/settings/ai", nil, "", e.cookie).Body.String(); !strings.Contains(page, "Disponible en los planes de pago") {
		t.Error("the free plan is not told")
	}
	if code := save(url.Values{"enabled": {"1"}, "source": {"network"}}); code != http.StatusUnprocessableEntity {
		t.Errorf("the network's AI on a free plan: %d", code)
	}
	setPlan(pro)
	if page := e.do("GET", "/admin/settings/ai", nil, "", e.cookie).Body.String(); !strings.Contains(page, "Pro incluye 1 consultas y 0 imágenes al día") {
		t.Error("the plan's allowance is not shown")
	}
	if code := save(url.Values{"enabled": {"1"}, "source": {"network"}}); code != http.StatusSeeOther {
		t.Fatalf("the network's AI on Pro: %d", code)
	}
	fake := &fakeDesignAI{answers: []string{"Un resumen."}}
	e.s.ai.mu.Lock()
	e.s.ai.provider = fake
	e.s.ai.mu.Unlock()

	call := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"title": "T", "content": "Un texto largo.", "lang": "es"})
		req := httptest.NewRequest("POST", "http://example.test"+path, strings.NewReader(string(body)))
		req.RemoteAddr = "192.0.2.30:4000"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("X-CSRF-Token", e.csrf)
		req.AddCookie(e.cookie)
		rec := httptest.NewRecorder()
		e.s.Handler().ServeHTTP(rec, req)
		return rec
	}
	if rec := call("/admin/api/ai/image"); rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), "imágenes") {
		t.Errorf("an image with none a day: %d %s", rec.Code, rec.Body)
	}
	if rec := call("/admin/api/ai/summary"); rec.Code != http.StatusOK {
		t.Fatalf("summary: %d %s", rec.Code, rec.Body)
	}
	if use, _ := e.st.AINetworkUsageOn(ctx, e.s.today()); use.Requests != 1 {
		t.Errorf("today's use: %+v", use)
	}
	if rec := call("/admin/api/ai/summary"); rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), "hoy") {
		t.Errorf("over the day's requests: %d %s", rec.Code, rec.Body)
	}

	// The plan goes: the AI is off, the settings stay.
	setPlan(free)
	if rec := call("/admin/api/ai/summary"); rec.Code != http.StatusForbidden {
		t.Errorf("after the downgrade: %d", rec.Code)
	}
	if st, _ := e.st.AISettings(ctx); !st.Enabled || st.Source != store.AISourceNetwork {
		t.Errorf("saved settings: %+v", st)
	}

	// Its own provider: no daily limit, on any plan.
	if code := save(url.Values{"enabled": {"1"}, "source": {""}, "api_key": {"sk-own"}}); code != http.StatusSeeOther {
		t.Fatalf("own provider: %d", code)
	}
	fake.answers = []string{"Otro."}
	e.s.ai.mu.Lock()
	e.s.ai.provider = fake
	e.s.ai.mu.Unlock()
	if rec := call("/admin/api/ai/summary"); rec.Code != http.StatusOK {
		t.Errorf("own provider: %d %s", rec.Code, rec.Body)
	}
}

// A site that never saved its AI settings uses the AI its plan includes,
// and only while the plan includes it; once saved, its choice stays.
func TestAIPlanDefault(t *testing.T) {
	e := newPluginEnv(t)
	ctx := context.Background()
	setPlan := func(p AIPlan) {
		e.s.limits = planLimits{plan: p}
		e.s.resetAI()
	}
	pro := AIPlan{Plan: "Pro", Paid: true, Text: &ai.Config{Provider: "anthropic", APIKey: "k", TextModel: "claude-net"}, RequestsPerDay: 5}

	setPlan(AIPlan{Plan: "Personal"})
	if st, _ := e.s.aiSettings(ctx); st.Enabled {
		t.Fatalf("the free plan: %+v", st)
	}
	setPlan(pro)
	if st, _ := e.s.aiSettings(ctx); !st.Enabled || st.Source != store.AISourceNetwork {
		t.Fatalf("Pro by default: %+v", st)
	}
	if page := e.do("GET", "/admin/settings/ai", nil, "", e.cookie).Body.String(); !strings.Contains(page, `value="network" checked`) {
		t.Error("the AI page does not show the plan's AI chosen")
	}
	// Without its own provider: the plan's AI comes with no settings.
	setPlan(AIPlan{Plan: "Pro", Paid: true})
	if st, _ := e.s.aiSettings(ctx); st.Enabled {
		t.Fatalf("a network without AI: %+v", st)
	}

	setPlan(pro)
	if code := e.admin("/admin/settings/ai", url.Values{"provider": {"anthropic"}, "min_role": {"author"}, "source": {"network"}}).Code; code != http.StatusSeeOther {
		t.Fatalf("switch off: %d", code)
	}
	if st, _ := e.s.aiSettings(ctx); st.Enabled {
		t.Fatalf("switched off, then on again: %+v", st)
	}
}
