package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"path"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"sap13ns/internal/auth"
	"sap13ns/internal/content"
	"sap13ns/internal/i18n"
	"sap13ns/internal/icons"
	"sap13ns/internal/store"
	"sap13ns/internal/theme"
)

// Version is set at build time with -ldflags "-X sap13ns/internal/web.Version=...".
var Version = "dev"

// langFuncs are the template functions that depend on the language. Admin
// templates use the main language; public themes get one copy per language.
func (s *Server) langFuncs(lang string) template.FuncMap {
	t := func(key string, args ...any) string { return s.i18n.T(lang, key, args...) }
	return template.FuncMap{
		"T":    t,
		"lang": func() string { return lang },
		// link prefixes a site path with the language segment of this render.
		"link": func(p string) string { return s.langPath(lang, p) },
		"date": func(tm time.Time) string {
			return i18n.FormatDate(lang, tm.In(s.loc()))
		},
		"shortDate": func(tm time.Time) string {
			return i18n.FormatShortDate(lang, tm.In(s.loc()))
		},
		"dateTime": func(tm time.Time) string {
			lt := tm.In(s.loc())
			return i18n.FormatShortDate(lang, lt) + " " + lt.Format("15:04")
		},
		"ago":       func(tm time.Time) string { return s.i18n.Ago(lang, tm, time.Now()) },
		"monthName": func(m int) string { return i18n.MonthName(lang, time.Month(m)) },
		// plural N "%d post" "%d posts" [args...]: the count is the first
		// argument of the translation, then args.
		"plural": func(n int, one, many string, args ...any) string {
			if n == 1 {
				return t(one, append([]any{n}, args...)...)
			}
			return t(many, append([]any{n}, args...)...)
		},
		"roleLabel":   func(role string) string { return t(roleLabels[role]) },
		"statusLabel": func(st string) string { return t(statusLabels[st]) },
	}
}

func (s *Server) funcMap() template.FuncMap {
	m := template.FuncMap{
		"icon":  icons.Template,
		"asset": s.assetURL,
		// themeDomain is the network's sites domain: the light or dark
		// choice is kept in a cookie for all its sites (theme-init.js).
		"themeDomain": s.themeDomain,
		"isoDate":     func(t time.Time) string { return t.UTC().Format(time.RFC3339) },
		"inputTime": func(t *time.Time) string {
			if t == nil {
				return ""
			}
			return t.In(s.loc()).Format("2006-01-02T15:04")
		},
		"add": func(a, b int) int { return a + b },
		"sub": func(a, b int) int { return a - b },
		"mul": func(a, b int) int { return a * b },
		"div": func(a, b int) int {
			if b == 0 {
				return 0
			}
			return a / b
		},
		"seq": func(n int) []int {
			out := make([]int, n)
			for i := range out {
				out[i] = i
			}
			return out
		},
		"dict": func(kv ...any) (map[string]any, error) {
			if len(kv)%2 != 0 {
				return nil, fmt.Errorf("dict: odd number of arguments")
			}
			m := make(map[string]any, len(kv)/2)
			for i := 0; i < len(kv); i += 2 {
				k, ok := kv[i].(string)
				if !ok {
					return nil, fmt.Errorf("dict: key %v is not a string", kv[i])
				}
				m[k] = kv[i+1]
			}
			return m, nil
		},
		"list":      func(v ...any) []any { return v },
		"append":    appendAny,
		"opt":       opt,
		"selectbox": selectbox,
		"join":      strings.Join,
		"lower":     strings.ToLower,
		"upper":     strings.ToUpper,
		"contains":  strings.Contains,
		"hasPrefix": strings.HasPrefix,
		"truncate":  content.Truncate,
		"initials":  initials,
		"filesize":  filesize,
		"paragraphs": func(text string) template.HTML {
			var b strings.Builder
			for _, para := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n\n") {
				para = strings.TrimSpace(para)
				if para == "" {
					continue
				}
				lines := strings.Split(para, "\n")
				for i := range lines {
					lines[i] = template.HTMLEscapeString(lines[i])
				}
				b.WriteString("<p>" + strings.Join(lines, "<br>") + "</p>")
			}
			return template.HTML(b.String())
		},
		"toJSON": func(v any) (string, error) {
			b, err := json.Marshal(v)
			return string(b), err
		},
		"hasID": func(id int64, ids []int64) bool {
			for _, v := range ids {
				if v == id {
					return true
				}
			}
			return false
		},
		"derefInt": func(p *int64) int64 {
			if p == nil {
				return 0
			}
			return *p
		},
		"plans": s.planViews,
		"now":   time.Now,
		"deref": func(b *bool) bool { return b != nil && *b },
	}
	// Same names as langFuncs, but resolving the main language on each call so
	// that admin templates follow a settings change without being re-parsed.
	maps.Copy(m, s.adminLangFuncs(s.lang))
	m["link"] = func(p string) string { return p }
	return m
}

// adminFuncs are the functions of the admin templates in lang, an admin
// language a user chose (uilang.go); "" is the site's main language,
// followed as the settings change.
func (s *Server) adminFuncs(lang string) template.FuncMap {
	m := s.funcMap()
	if lang != "" {
		maps.Copy(m, s.adminLangFuncs(func() string { return lang }))
	}
	return m
}

// adminLangFuncs are the admin template functions that depend on the
// language, which lang gives on each call.
func (s *Server) adminLangFuncs(lang func() string) template.FuncMap {
	t := func(key string, args ...any) string { return s.i18n.T(lang(), key, args...) }
	return template.FuncMap{
		"T":         t,
		"lang":      lang,
		"date":      func(tm time.Time) string { return i18n.FormatDate(lang(), tm.In(s.loc())) },
		"shortDate": func(tm time.Time) string { return i18n.FormatShortDate(lang(), tm.In(s.loc())) },
		"dateTime":  func(tm time.Time) string { return s.dateTimeIn(lang(), tm) },
		"ago":       func(tm time.Time) string { return s.i18n.Ago(lang(), tm, time.Now()) },
		"monthName": func(n int) string { return i18n.MonthName(lang(), time.Month(n)) },
		"plural": func(n int, one, many string, args ...any) string {
			if n == 1 {
				return t(one, append([]any{n}, args...)...)
			}
			return t(many, append([]any{n}, args...)...)
		},
		// themeT translates a string of a theme's manifest (theme.json: its
		// description, option labels and help, menus and widget areas) for
		// the admin: the theme's own i18n/<lang>.json first, then the UI
		// catalog.
		"themeT":      func(th *theme.Theme, key string) string { return s.themeText(lang(), th, key) },
		"roleLabel":   func(role string) string { return t(roleLabels[role]) },
		"statusLabel": func(st string) string { return t(statusLabels[st]) },
	}
}

// themeText translates a string of th's manifest into lang: the theme's
// own catalog first, then the UI catalog (see themeT).
func (s *Server) themeText(lang string, th *theme.Theme, key string) string {
	if th != nil {
		if v, ok := th.Text(lang, key); ok {
			return v
		}
	}
	return s.i18n.T(lang, key)
}

// dateTimeIn renders a short date and a time in lang, in the site's zone.
func (s *Server) dateTimeIn(lang string, t time.Time) string {
	lt := t.In(s.loc())
	return i18n.FormatShortDate(lang, lt) + " " + lt.Format("15:04")
}

func upperFirst(s string) string {
	for i, r := range s {
		return string(unicode.ToUpper(r)) + s[i+utf8.RuneLen(r):]
	}
	return s
}

var roleLabels = map[string]string{
	auth.RoleAdmin: "Administrator", auth.RoleEditor: "Editor", auth.RoleAuthor: "Author",
	auth.RoleContributor: "Contributor", auth.RoleSubscriber: "Subscriber",
}

var statusLabels = map[string]string{
	store.StatusDraft: "Draft", store.StatusPending: "Pending review", store.StatusPublished: "Published",
	store.StatusPrivate: "Private", store.StatusTrash: "Trash", "scheduled": "Scheduled",
	"approved": "Approved", "spam": "Spam", "active": "Active", "disabled": "Disabled",
}

func initials(name string) string {
	var out []rune
	for _, w := range strings.Fields(name) {
		for _, r := range w {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				out = append(out, unicode.ToUpper(r))
				break
			}
		}
		if len(out) == 2 {
			break
		}
	}
	if len(out) == 0 {
		return "?"
	}
	return string(out)
}

func filesize(n int64) string {
	switch {
	case n >= 1<<30 && n%(1<<30) == 0:
		return fmt.Sprintf("%d GB", n>>30)
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// ------------------------------------------------------- admin templates --

type adminTemplates struct {
	fsys   fs.FS
	funcs  func(lang string) template.FuncMap
	reload bool
	mu     sync.RWMutex
	// sets holds, for each admin language in use ("" is the site's main
	// language), the pages rendered so far. A page is parsed with that
	// language's functions when it is first shown: a site whose
	// administration nobody opens keeps none of them in memory, and a
	// network has many such sites loaded.
	sets map[string]*adminSet
}

// adminSet is one language's templates: the shared ones every page is
// built on, and the pages parsed so far.
type adminSet struct {
	base  *template.Template // never executed: pages are clones of it
	pages map[string]*template.Template
}

var adminBase = []string{"layout.html", "auth.html", "partials.html", "ai_panel.html", "site_switcher.html", "avatar.html"}

// adminChecked remembers, for the whole process, that the admin templates
// of a file system parse (the key names their contents): every page is
// parsed once at start to find a broken one, and then let go.
var adminChecked sync.Map

// newAdminTemplates prepares the admin templates; funcs gives the template
// functions of a language (Server.adminFuncs). It fails when one of them
// does not parse.
func newAdminTemplates(ui fs.FS, funcs func(lang string) template.FuncMap, reload bool) (*adminTemplates, error) {
	sub, err := fs.Sub(ui, "admin/templates")
	if err != nil {
		return nil, err
	}
	a := &adminTemplates{fsys: sub, funcs: funcs, reload: reload, sets: map[string]*adminSet{}}
	return a, a.check()
}

// check parses every page in the site's main language, without keeping
// them, once per process for the same templates and functions.
func (a *adminTemplates) check() error {
	files, err := fs.Glob(a.fsys, "*.html")
	if err != nil {
		return err
	}
	h := sha256.New()
	for _, f := range files {
		b, err := fs.ReadFile(a.fsys, f)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", f, len(b))
		h.Write(b)
	}
	funcs := a.funcs("")
	for _, name := range slices.Sorted(maps.Keys(funcs)) {
		fmt.Fprintf(h, "\x00%s", name)
	}
	key := hex.EncodeToString(h.Sum(nil)[:16])
	if _, ok := adminChecked.Load(key); ok {
		return nil
	}
	base, err := template.New("admin").Funcs(funcs).ParseFS(a.fsys, adminBase...)
	if err != nil {
		return err
	}
	for _, f := range files {
		if contains(adminBase, f) {
			continue
		}
		if _, err := parseAdminPage(base, a.fsys, f); err != nil {
			return err
		}
	}
	adminChecked.Store(key, true)
	return nil
}

func parseAdminPage(base *template.Template, fsys fs.FS, file string) (*template.Template, error) {
	set, err := base.Clone()
	if err != nil {
		return nil, err
	}
	return set.ParseFS(fsys, file)
}

// page returns the page name in lang, parsed on first use (on every use
// in development, so that template edits show up).
func (a *adminTemplates) page(lang, name string) (*template.Template, error) {
	file := name + ".html"
	if contains(adminBase, file) {
		return nil, fmt.Errorf("admin template %q not found", name)
	}
	if a.reload {
		base, err := template.New("admin").Funcs(a.funcs(lang)).ParseFS(a.fsys, adminBase...)
		if err != nil {
			return nil, err
		}
		return a.parsePage(base, name)
	}
	a.mu.RLock()
	var set *template.Template
	if s := a.sets[lang]; s != nil {
		set = s.pages[name]
	}
	a.mu.RUnlock()
	if set != nil {
		return set, nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.sets[lang]
	if s == nil {
		base, err := template.New("admin").Funcs(a.funcs(lang)).ParseFS(a.fsys, adminBase...)
		if err != nil {
			return nil, err
		}
		s = &adminSet{base: base, pages: map[string]*template.Template{}}
		a.sets[lang] = s
	}
	if set := s.pages[name]; set != nil {
		return set, nil // parsed while this request waited
	}
	set, err := a.parsePage(s.base, name)
	if err != nil {
		return nil, err
	}
	s.pages[name] = set
	return set, nil
}

func (a *adminTemplates) parsePage(base *template.Template, name string) (*template.Template, error) {
	if _, err := fs.Stat(a.fsys, name+".html"); err != nil {
		return nil, fmt.Errorf("admin template %q not found", name)
	}
	return parseAdminPage(base, a.fsys, name+".html")
}

// render executes the page name in lang ("" is the site's main language).
func (a *adminTemplates) render(w io.Writer, lang, name string, data any) error {
	set, err := a.page(lang, name)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := set.ExecuteTemplate(&buf, name+".html", data); err != nil {
		return err
	}
	_, err = w.Write(buf.Bytes())
	return err
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// adminPage is the data every admin template receives.
type adminPage struct {
	Title   string
	Section string
	User    *store.User
	CSRF    string
	Site    store.Settings
	Flashes []flash
	Pending int
	Path    string
	Version string
	SiteURL string
	Update  *updateNotice // newer release, shown to administrators
	Auth    AuthPaths     // the sign-in pages, at the site's sign-in address
	D       any

	// The language of the panel (uilang.go): its code and direction, the
	// languages of the language menu, and where choosing one comes back to.
	Lang     string
	Dir      string
	UILangs  []LangLink
	LangNext string
	// On a sign-in page: the language of its ?lang= ("" for the site's),
	// which its forms and links carry, and the page in each language.
	SignInLang  string
	SignInLangs []LangLink

	// On the network portal: every account has "My sites"; network admins
	// also have the network admin area, with the sites waiting for them.
	Portal       bool
	NetworkAdmin bool
	SuperAdmin   bool // sees the usage statistics
	NetPending   int
	Billing      bool // the network has billing
	NetFailing   int  // billing events that failed, for network admins
	NetReviews   int  // plugin uploads waiting for the operator's review
	Reviews      bool // the network reviews the plugins its sites upload

	// On every site of a network: the account's other sites (switcher.go).
	Switcher *switcherView
	// Tenant: a site of a network (not the portal), which has no System
	// page and no update notice: the server is the operator's.
	Tenant bool

	// The sign-in pages' layout (auth.html): NoCredit leaves out the
	// "Powered by sap13ns" credit where the public pages do (credit.go);
	// Icon, the site icon, replaces the sap13ns one (the maintenance page).
	NoCredit bool
	Icon     string
}

func (p *adminPage) Can(c string) bool {
	return p.User != nil && auth.Can(p.User.Role, auth.Cap(c))
}

func (p *adminPage) CSRFField() template.HTML {
	return template.HTML(`<input type="hidden" name="csrf" value="` + template.HTMLEscapeString(p.CSRF) + `">`)
}

func (p *adminPage) Active(section string) bool { return p.Section == section }

func (s *Server) newAdminPage(w http.ResponseWriter, r *http.Request, title, section string, d any) *adminPage {
	p := &adminPage{Title: title, Section: section, User: currentUser(r), Site: s.settings(), Path: r.URL.Path,
		Version: Version, SiteURL: s.cfg.BaseURL.String(), Auth: s.auth(), D: d, Flashes: s.takeFlashes(w, r),
		NoCredit: s.hideCredit(r)}
	if p.Lang = s.uiLang(r); !s.i18n.Has(p.Lang) {
		p.Lang = "en" // a site language without a catalog: the strings are the English source
	}
	p.Dir = langDir(p.Lang)
	if langs := s.uiLanguages(p.Lang); len(langs) > 1 {
		if p.User != nil {
			p.UILangs, p.LangNext = langs, langNext(r)
		} else if s.signInLangPage(r.URL.Path) {
			p.SignInLang, p.SignInLangs = rctx(r).UILang, signInLangs(r, langs)
		}
	}
	if sess := rctx(r).Session; sess != nil {
		p.CSRF = sess.CSRFToken
	}
	p.Tenant = s.sso != nil
	if p.User != nil && auth.Can(p.User.Role, auth.CapManageSettings) && !p.Tenant {
		p.Update = s.updateNotice()
	}
	if p.User != nil && auth.Can(p.User.Role, auth.CapModerate) {
		if n, err := s.store.CountPendingComments(r.Context()); err == nil {
			p.Pending = n
		}
	}
	p.Switcher = s.siteSwitcher(r)
	if s.portal != nil && p.User != nil {
		p.Portal, p.Billing = true, s.billing != nil
		if ok, err := s.portal.IsNetworkAdmin(r.Context(), p.User.ID); err == nil && ok {
			p.NetworkAdmin = true
			p.SuperAdmin, _ = s.portal.IsSuperAdmin(r.Context(), p.User.ID)
			if _, n, err := s.portal.Sites(r.Context(), NetSiteQuery{Status: "pending", Limit: 1}); err == nil {
				p.NetPending = n
			}
			if s.billing != nil {
				if n, err := s.billing.FailingBillingEvents(r.Context()); err == nil {
					p.NetFailing = n
				}
			}
			if p.Reviews = s.reviews != nil; p.Reviews {
				if n, err := s.reviews.PendingPluginReviews(r.Context()); err == nil {
					p.NetReviews = n
				}
			}
		}
	}
	return p
}

func (s *Server) renderAdmin(w http.ResponseWriter, r *http.Request, name, title, section string, d any) {
	s.renderAdminStatus(w, r, http.StatusOK, name, title, section, d)
}

func (s *Server) renderAdminStatus(w http.ResponseWriter, r *http.Request, status int, name, title, section string, d any) {
	s.renderAdminPage(w, r, status, name, s.newAdminPage(w, r, title, section, d))
}

// renderAdminPage renders the template name with page.
func (s *Server) renderAdminPage(w http.ResponseWriter, r *http.Request, status int, name string, page *adminPage) {
	lang := s.uiLang(r)
	if lang == s.lang() {
		lang = "" // the set that follows the site's language
	}
	var buf bytes.Buffer
	if err := s.admin.render(&buf, lang, name, page); err != nil {
		s.log.Error("render admin template", "template", name, "err", err)
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if name == "post_edit" {
		s.allowVideoFrames(w) // the editor plays videos in its preview
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	w.Write(buf.Bytes())
}

func (s *Server) adminError(w http.ResponseWriter, r *http.Request, status int, msg string) {
	s.renderAdminStatus(w, r, status, "error", http.StatusText(status), "", map[string]any{"Status": status, "Message": msg})
}

func (s *Server) serverError(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("internal error", "path", r.URL.Path, "err", err)
	if strings.HasPrefix(r.URL.Path, "/admin") && currentUser(r) != nil {
		s.adminError(w, r, http.StatusInternalServerError, s.tu(r, "Something went wrong. The error has been logged."))
		return
	}
	http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// ---------------------------------------------------------------- flash --

type flash struct {
	Kind string `json:"k"` // success | error | warning | info
	Text string `json:"t"`
}

const flashCookie = "sap13ns_flash"

func (s *Server) flash(w http.ResponseWriter, kind, text string) {
	raw, _ := json.Marshal([]flash{{Kind: kind, Text: text}})
	val := base64.RawURLEncoding.EncodeToString(raw)
	sig := s.keys.Sign("flash", val, time.Now())
	http.SetCookie(w, &http.Cookie{Name: flashCookie, Value: val + "~" + sig, Path: "/", MaxAge: 120,
		HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode})
}

func (s *Server) takeFlashes(w http.ResponseWriter, r *http.Request) []flash {
	ck, err := r.Cookie(flashCookie)
	if err != nil {
		return nil
	}
	http.SetCookie(w, &http.Cookie{Name: flashCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode})
	val, sig, ok := strings.Cut(ck.Value, "~")
	if !ok {
		return nil
	}
	if _, ok := s.keys.Verify("flash", val, sig, 5*time.Minute); !ok {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(val)
	if err != nil {
		return nil
	}
	var out []flash
	json.Unmarshal(raw, &out)
	return out
}

// redirect sends a 303 after a successful POST, optionally with a flash.
func (s *Server) redirect(w http.ResponseWriter, r *http.Request, to, kind, msg string) {
	if msg != "" {
		s.flash(w, kind, msg)
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// ---------------------------------------------------------------- static --

func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request) {
	name := path.Clean("/" + r.PathValue("path"))[1:]
	hash, ok := s.staticHash(name)
	if !ok || strings.Contains(name, "..") {
		http.NotFound(w, r)
		return
	}
	f, err := s.ui.Open("static/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	serveAsset(w, r, f, name, hash)
}

func serveAsset(w http.ResponseWriter, r *http.Request, f fs.File, name, hash string) {
	etag := `"` + hash + `"`
	if r.URL.Query().Get("v") == hash {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=300")
	}
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		b, err := io.ReadAll(f)
		if err != nil {
			http.Error(w, "read error", http.StatusInternalServerError)
			return
		}
		rs = bytes.NewReader(b)
	}
	http.ServeContent(w, r, name, time.Time{}, rs)
}
