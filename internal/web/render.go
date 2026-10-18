package web

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"path"
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
		"plural": func(n int, one, many string) string {
			if n == 1 {
				return t(one, n)
			}
			return t(many, n)
		},
		"roleLabel":   func(role string) string { return t(roleLabels[role]) },
		"statusLabel": func(st string) string { return t(statusLabels[st]) },
	}
}

func (s *Server) funcMap() template.FuncMap {
	m := template.FuncMap{
		"icon":    icons.Template,
		"asset":   s.assetURL,
		"isoDate": func(t time.Time) string { return t.UTC().Format(time.RFC3339) },
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
	m["T"] = s.t
	m["lang"] = s.lang
	m["date"] = func(t time.Time) string { return i18n.FormatDate(s.lang(), t.In(s.loc())) }
	m["shortDate"] = func(t time.Time) string { return i18n.FormatShortDate(s.lang(), t.In(s.loc())) }
	m["dateTime"] = s.dateTime
	m["ago"] = func(t time.Time) string { return s.i18n.Ago(s.lang(), t, time.Now()) }
	m["monthName"] = func(n int) string { return i18n.MonthName(s.lang(), time.Month(n)) }
	m["plural"] = func(n int, one, many string) string {
		if n == 1 {
			return s.t(one, n)
		}
		return s.t(many, n)
	}
	m["roleLabel"] = func(role string) string { return s.t(roleLabels[role]) }
	m["statusLabel"] = func(st string) string { return s.t(statusLabels[st]) }
	m["link"] = func(p string) string { return p }
	return m
}

// planViews lists the network's hosting plans. Until billing is configured it
// returns sample plans in development so that themes can be designed.
func (s *Server) planViews() []PlanView {
	if !s.cfg.Dev {
		return nil
	}
	return []PlanView{
		{Slug: "personal", Name: "Personal", Description: "For your blog, portfolio or CV.", Free: true, SignupURL: "/signup?plan=personal",
			Features: []string{"1 site", "sap13ns.site subdomain", "1 GB storage", "All themes"}},
		{Slug: "pro", Name: "Pro", Description: "For freelancers and small businesses.", Price: "5 €", Interval: "month", Commercial: true, Highlight: true,
			SignupURL: "/signup?plan=pro", Features: []string{"Custom domain with HTTPS", "10 GB storage", "Your own themes and plugins", "Daily backups"}},
		{Slug: "business", Name: "Business", Description: "For teams that publish a lot.", Price: "12 €", Interval: "month", Commercial: true,
			SignupURL: "/signup?plan=business", Features: []string{"3 sites", "50 GB storage", "AI assistant credits", "Priority support"}},
	}
}

func (s *Server) dateTime(t time.Time) string {
	lt := t.In(s.loc())
	return i18n.FormatShortDate(s.lang(), lt) + " " + lt.Format("15:04")
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
	funcs  template.FuncMap
	reload bool
	mu     sync.RWMutex
	pages  map[string]*template.Template
}

var adminBase = []string{"layout.html", "auth.html", "partials.html", "ai_panel.html"}

func newAdminTemplates(ui fs.FS, funcs template.FuncMap, reload bool) (*adminTemplates, error) {
	sub, err := fs.Sub(ui, "admin/templates")
	if err != nil {
		return nil, err
	}
	a := &adminTemplates{fsys: sub, funcs: funcs, reload: reload}
	return a, a.parse()
}

func (a *adminTemplates) parse() error {
	base, err := template.New("admin").Funcs(a.funcs).ParseFS(a.fsys, adminBase...)
	if err != nil {
		return err
	}
	files, err := fs.Glob(a.fsys, "*.html")
	if err != nil {
		return err
	}
	pages := map[string]*template.Template{}
	for _, f := range files {
		if contains(adminBase, f) {
			continue
		}
		set, err := base.Clone()
		if err != nil {
			return err
		}
		if _, err := set.ParseFS(a.fsys, f); err != nil {
			return err
		}
		pages[strings.TrimSuffix(f, ".html")] = set
	}
	a.mu.Lock()
	a.pages = pages
	a.mu.Unlock()
	return nil
}

func (a *adminTemplates) render(w io.Writer, name string, data any) error {
	if a.reload {
		if err := a.parse(); err != nil {
			return err
		}
	}
	a.mu.RLock()
	set, ok := a.pages[name]
	a.mu.RUnlock()
	if !ok {
		return fmt.Errorf("admin template %q not found", name)
	}
	var buf bytes.Buffer
	if err := set.ExecuteTemplate(&buf, name+".html", data); err != nil {
		return err
	}
	_, err := w.Write(buf.Bytes())
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
	D       any
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
		Version: Version, SiteURL: s.cfg.BaseURL.String(), D: d, Flashes: s.takeFlashes(w, r)}
	if sess := rctx(r).Session; sess != nil {
		p.CSRF = sess.CSRFToken
	}
	if p.User != nil && auth.Can(p.User.Role, auth.CapModerate) {
		if counts, err := s.store.CountCommentsByStatus(r.Context()); err == nil {
			p.Pending = counts["pending"]
		}
	}
	return p
}

func (s *Server) renderAdmin(w http.ResponseWriter, r *http.Request, name, title, section string, d any) {
	s.renderAdminStatus(w, r, http.StatusOK, name, title, section, d)
}

func (s *Server) renderAdminStatus(w http.ResponseWriter, r *http.Request, status int, name, title, section string, d any) {
	page := s.newAdminPage(w, r, title, section, d)
	var buf bytes.Buffer
	if err := s.admin.render(&buf, name, page); err != nil {
		s.log.Error("render admin template", "template", name, "err", err)
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
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
		s.adminError(w, r, http.StatusInternalServerError, s.t("Something went wrong. The error has been logged."))
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
