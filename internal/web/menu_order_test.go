package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// A menu is reordered by sending all its items in their new order; a list
// that is not exactly one menu changes nothing.
func TestMenuOrder(t *testing.T) {
	e := newPluginEnv(t)
	ctx := context.Background()
	for _, l := range []string{"A", "B", "C"} {
		if err := e.st.AddMenuItem(ctx, "primary", "", l, "/"+l); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.st.AddMenuItem(ctx, "footer", "", "F", "/f"); err != nil {
		t.Fatal(err)
	}
	all, _ := e.st.AllMenus(ctx)
	p, f := all["primary"], all["footer"]
	id := func(i int64) string { return strconv.FormatInt(i, 10) }
	order := func(ids ...int64) int {
		v := url.Values{}
		for _, i := range ids {
			v.Add("ids", id(i))
		}
		return e.admin("/admin/appearance/menus/order", v).Code
	}
	if code := order(p[2].ID, p[0].ID, p[1].ID); code != http.StatusNoContent {
		t.Fatalf("reorder: %d", code)
	}
	all, _ = e.st.AllMenus(ctx)
	if got := all["primary"]; got[0].Label != "C" || got[1].Label != "A" || got[2].Label != "B" {
		t.Fatalf("order: %v", got)
	}
	for name, ids := range map[string][]int64{
		"missing one":  {p[0].ID, p[1].ID},
		"another menu": {p[0].ID, p[1].ID, f[0].ID},
		"twice":        {p[0].ID, p[0].ID, p[1].ID},
		"no such item": {999999},
	} {
		if code := order(ids...); code != http.StatusConflict {
			t.Errorf("%s: %d", name, code)
		}
	}
	all, _ = e.st.AllMenus(ctx)
	if got := all["primary"]; got[0].Label != "C" {
		t.Errorf("a refused order changed the menu: %v", got)
	}

	// An item is saved as it is edited: the page's script asks for JSON.
	save := func(label, link string) int {
		v := url.Values{"label": {label}, "url": {link}, "csrf": {e.csrf}}
		req := httptest.NewRequest("POST", "http://example.test/admin/appearance/menus/"+id(p[0].ID), strings.NewReader(v.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")
		req.AddCookie(e.cookie)
		rec := httptest.NewRecorder()
		e.s.Handler().ServeHTTP(rec, req)
		return rec.Code
	}
	if code := save("Inicio", "/inicio"); code != http.StatusOK {
		t.Fatalf("autosave: %d", code)
	}
	if code := save("Inicio", "javascript:alert(1)"); code != http.StatusUnprocessableEntity {
		t.Fatalf("a bad link: %d", code)
	}
	all, _ = e.st.AllMenus(ctx)
	for _, it := range all["primary"] {
		if it.ID == p[0].ID && (it.Label != "Inicio" || it.URL != "/inicio") {
			t.Errorf("saved: %+v", it)
		}
	}
}
