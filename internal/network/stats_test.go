package network

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"sap13ns/internal/store"
)

// The usage statistics add up every served site: their content, and the
// AI by provider and model, merged across sites.
func TestNetworkStats(t *testing.T) {
	e := newNetEnv(t, nil)
	a, b := e.tenant("stats-a", PlanPersonal), e.tenant("stats-b", "pro")
	day := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	for i, s := range []*Site{a, b} {
		st := e.siteStore(s)
		if _, err := st.DB.Exec(e.ctx, `INSERT INTO posts (type, status, slug, lang, created_at) VALUES
			('post', 'published', 'one', 'es', $1), ('post', 'draft', 'two', 'es', $1), ('page', 'published', 'about', 'es', $1),
			('post', 'trash', 'gone', 'es', $1)`, day.Add(10*time.Hour)); err != nil {
			t.Fatal(err)
		}
		for _, c := range []store.AICall{
			{Day: day, Feature: "summary", Kind: store.AIKindText, Provider: "anthropic", Model: "claude-x", Source: store.AICallOwn,
				Requests: 2, InputTokens: 100, OutputTokens: 40},
			{Day: day, Feature: "image", Kind: store.AIKindImage, Provider: "alibaba", Model: "qwen-image-2.1-pro", Source: store.AICallNetwork,
				Requests: int64(i + 1), Images: int64(i + 1)},
		} {
			if err := st.AddAICall(e.ctx, c); err != nil {
				t.Fatal(err)
			}
		}
	}
	out, err := portalHooks{hooks{r: e.reg}}.NetworkStats(e.ctx, day.AddDate(0, 0, -6), time.Now().UTC().AddDate(0, 0, 1)) // the sites are created now
	if err != nil {
		t.Fatal(err)
	}
	tot := out.Total
	if out.Failed != 0 || len(out.Sites) != 2 || tot.Posts != 4 || tot.PublishedPosts != 2 || tot.Pages != 2 || tot.Drafts != 2 {
		t.Fatalf("totals: %+v %+v", out, tot)
	}
	if out.Active+out.Pending+out.Suspended != 2 || out.NewSites != 2 || out.Accounts < 1 {
		t.Errorf("network: %+v", out)
	}
	p := tot.Period()
	if p.Posts != 6 || p.Pages != 2 || p.Requests != 7 || p.InputTokens != 200 || p.OutputTokens != 80 || p.Images != 3 {
		t.Errorf("period: %+v", p)
	}
	if len(tot.Calls) != 2 {
		t.Fatalf("calls: %+v", tot.Calls)
	}
	for _, c := range tot.Calls {
		if c.Provider == "alibaba" && (c.Images != 3 || c.Model != "qwen-image-2.1-pro") || c.Provider == "anthropic" && c.Requests != 4 {
			t.Errorf("call: %+v", c)
		}
	}
}

// Only super admins see the usage statistics, in the menu and the page.
func TestNetworkStatsPage(t *testing.T) {
	e := newNetEnv(t, nil)
	e.tenant("stats-a", "pro")
	nina, nb, _ := e.networkAdmin("nina")
	if body := nb.page("/admin/network/stats?range=7d"); !strings.Contains(body, "Estadísticas de uso") || !strings.Contains(body, "stats-a") {
		t.Fatal("the statistics page lacks its title or the site")
	}
	if body := nb.page("/admin/"); !strings.Contains(body, `href="/admin/network/stats"`) {
		t.Fatal("a super admin's menu lacks the statistics")
	}
	omar, err := e.portal.CreateUser(e.ctx, store.NewUser{Username: "omar", Email: "omar@example.com", Role: "admin", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.n.AddNetworkAdmin(e.ctx, omar, nina); err != nil {
		t.Fatal(err)
	}
	e.mfa(omar)
	ob := e.browser()
	ob.portalSignIn(omar)
	if rec := ob.get(portalURL + "/admin/network/stats"); rec.Code != http.StatusForbidden {
		t.Errorf("a network admin who is not a super admin: %d", rec.Code)
	}
	if body := ob.page("/admin/"); strings.Contains(body, `href="/admin/network/stats"`) {
		t.Error("the statistics are in the menu of a network admin who is not a super admin")
	}
}
