package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"html/template"
	"net/http"
	"net/mail"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"sap13ns/internal/auth"
	"sap13ns/internal/plugin"
)

// Plugin features of the public site: the static assets of enabled plugins,
// the forms they declare in plugin.json, and the Content-Security-Policy
// sources they add. The hooks themselves run in internal/plugin; this file
// is the HTTP side.

// pluginWebState is created lazily; the zero value is ready to use.
type pluginWebState struct {
	once   sync.Once
	formIP *auth.Limiter

	// Form tokens are single-use. Two generations of used nonces cover the
	// whole validity of a token (formTokenTTL).
	nonceMu   sync.Mutex
	nonces    map[string]struct{}
	oldNonces map[string]struct{}
	rotated   time.Time

	csp atomic.Pointer[cspCache]
}

type cspCache struct {
	gen          uint64
	base, policy string
}

const (
	formTokenTTL  = 24 * time.Hour
	formMinAge    = 3 * time.Second // faster than this is a bot
	formMaxBody   = 64 << 10
	formCookie    = "sap_pform"
	formTokenSlot = "sap-ftoken-"
	maxNonces     = 200_000
)

func (s *Server) plugWeb() *pluginWebState {
	s.plug.once.Do(func() {
		s.plug.formIP = auth.NewLimiter(10, 10*time.Minute)
		s.plug.nonces, s.plug.oldNonces, s.plug.rotated = map[string]struct{}{}, map[string]struct{}{}, time.Now()
	})
	return &s.plug
}

// ------------------------------------------------------------- assets --

// handlePluginAsset serves /plugin-assets/{id}/{version}/{path...}. The
// version is the content hash of the plugin's assets, so responses are
// immutable; an outdated version is a 404.
func (s *Server) handlePluginAsset(w http.ResponseWriter, r *http.Request) {
	data, ctype, ok := s.plugins.Asset(r.PathValue("id"), r.PathValue("version"), r.PathValue("path"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("Cache-Control", "public, max-age=31536000, immutable")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	// Opened directly (an SVG, say), a file can do nothing.
	h.Set("Content-Security-Policy", "default-src 'none'; img-src 'self' data:; style-src 'self'; font-src 'self'; sandbox")
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
}

// pluginHead fills the plugin parts of <head>: JSON-LD and asset links.
func (s *Server) pluginHead(ctx context.Context, pd *PageData, info *plugin.PageInfo) {
	head := s.plugins.Head(ctx, info)
	pd.Meta = head.Meta
	for _, ld := range head.JSONLD {
		pd.PluginLD = append(pd.PluginLD, template.JS(ld)) // validated and HTML-escaped by the host
	}
	if len(pd.PluginLD) > 0 {
		pd.JSONLD = nil // a plugin's structured data replaces the host's WebSite or BlogPosting
	}
	pd.PluginCSS, pd.PluginJS = s.plugins.AssetLinks()
}

// pluginWidgetTitle is the title a plugin declares for one of its widgets.
func (s *Server) pluginWidgetTitle(pluginID, widgetID string) string {
	if p := s.plugins.Get(pluginID); p != nil {
		for _, w := range p.Widgets {
			if w.ID == widgetID {
				return w.Title
			}
		}
	}
	return ""
}

// pageInfo describes the page to plugins (head.meta and widgets).
func pageInfo(pd *PageData) *plugin.PageInfo {
	return &plugin.PageInfo{Kind: pd.Kind, URL: pd.Canonical, Lang: pd.Site.Lang, Title: pd.Title,
		Description: pd.Description, Post: pd.postInfo}
}

// ---------------------------------------------------------------- CSP --

// pluginCSP returns base with the sources that enabled plugins add, cached
// until the plugin set changes.
func (s *Server) pluginCSP(base string) string {
	if s.plugins == nil {
		return base
	}
	gen := s.plugins.Generation()
	if c := s.plug.csp.Load(); c != nil && c.gen == gen && c.base == base {
		return c.policy
	}
	policy := mergeCSP(base, s.plugins.CSPSources())
	s.plug.csp.Store(&cspCache{gen: gen, base: base, policy: policy})
	return policy
}

// mergeCSP adds sources to directives of a policy. A directive set to
// 'none' is replaced; a missing one is added with 'self' plus the sources.
func mergeCSP(base string, add map[string][]string) string {
	if len(add) == 0 {
		return base
	}
	parts := strings.Split(base, ";")
	done := map[string]bool{}
	for i, part := range parts {
		fields := strings.Fields(part)
		if len(fields) == 0 {
			continue
		}
		dir := strings.ToLower(fields[0])
		srcs, ok := add[dir]
		if !ok {
			continue
		}
		done[dir] = true
		vals := fields[1:]
		if len(vals) == 1 && vals[0] == "'none'" {
			vals = nil
		}
		for _, src := range srcs {
			if !slices.Contains(vals, src) {
				vals = append(vals, src)
			}
		}
		lead := ""
		if i > 0 {
			lead = " "
		}
		parts[i] = lead + dir + " " + strings.Join(vals, " ")
	}
	dirs := make([]string, 0, len(add))
	for dir := range add {
		if !done[dir] {
			dirs = append(dirs, dir)
		}
	}
	slices.Sort(dirs)
	for _, dir := range dirs {
		parts = append(parts, " "+dir+" 'self' "+strings.Join(add[dir], " "))
	}
	return strings.Join(parts, ";")
}

// -------------------------------------------------------------- forms --

// The host renders a plugin form where the plugin's output contains the
// qualified placeholder <div data-form="plugin.form"></div>. Rendered
// bodies are cached, so the placeholder is expanded on every response: the
// form carries a signed, single-use timestamp token, and after a submission
// the visitor sees the result (and their input, on errors) from an encrypted
// cookie. The </select></form> before the form ends any form or select an
// uploaded theme left open around the placeholder, so that a browser never
// adds these fields to the theme's form.

var (
	formSlotRe  = regexp.MustCompile(`<div data-form="([a-z0-9][a-z0-9-]{1,49})\.([a-z][a-z0-9_-]{0,39})"></div>`)
	formTokenRe = regexp.MustCompile(formTokenSlot + `([a-z0-9][a-z0-9-]{1,49}\.[a-z][a-z0-9_-]{0,39})`)
	telRe       = regexp.MustCompile(`^[0-9+().\s-]{3,40}$`)
)

// formFlash is the outcome of a submission, shown once after the redirect.
type formFlash struct {
	Key    string            `json:"k"`
	OK     bool              `json:"ok"`
	Msg    string            `json:"m"`
	Errors map[string]string `json:"e,omitempty"`
	Values map[string]string `json:"v,omitempty"` // re-filled after an error
}

type formView struct {
	Anchor, Action, Back, Lang, Token string
	Title, Submit                     string
	Notice                            string
	NoticeOK, Done                    bool
	Fields                            []formFieldView
}

type formFieldView struct {
	ID, Name, Type, Label, Placeholder, Value, Error, Autocomplete string
	Required, Checked                                              bool
	Max                                                            int
	Choices                                                        []string
}

var pluginFormTmpl = template.Must(template.New("form").Parse(`<div class="plugin-form-wrap" id="{{.Anchor}}">
{{- with .Title}}<h3 class="plugin-form-title">{{.}}</h3>{{end}}
{{- with .Notice}}<p class="comment-notice {{if $.NoticeOK}}is-success{{else}}is-error{{end}}" role="{{if $.NoticeOK}}status{{else}}alert{{end}}">{{.}}</p>{{end}}
{{- if not .Done}}
</select></form><form class="plugin-form comment-form" method="post" action="{{.Action}}">
<input type="hidden" name="_token" value="{{.Token}}">
<input type="hidden" name="_back" value="{{.Back}}">
<input type="hidden" name="_lang" value="{{.Lang}}">
<div class="sap-hp" aria-hidden="true"><label for="{{.Anchor}}-hp">Website</label><input id="{{.Anchor}}-hp" name="_hp" type="text" tabindex="-1" autocomplete="off"></div>
{{- range .Fields}}{{$f := .}}
<p class="plugin-form-field{{if .Error}} has-error{{end}}">
{{- if eq .Type "checkbox"}}<label class="plugin-form-check"><input type="checkbox" id="{{.ID}}" name="{{.Name}}" value="on"{{if .Checked}} checked{{end}}{{if .Required}} required{{end}}{{if .Error}} aria-invalid="true" aria-describedby="{{.ID}}-err"{{end}}> {{.Label}}</label>
{{- else}}<label for="{{.ID}}">{{.Label}}{{if .Required}} <span class="plugin-form-req" aria-hidden="true">*</span>{{end}}</label>
{{- if eq .Type "textarea"}}<textarea id="{{.ID}}" name="{{.Name}}" rows="5" maxlength="{{.Max}}"{{if .Required}} required{{end}}{{with .Placeholder}} placeholder="{{.}}"{{end}}{{if .Error}} aria-invalid="true" aria-describedby="{{.ID}}-err"{{end}}>{{.Value}}</textarea>
{{- else if eq .Type "select"}}<select id="{{.ID}}" name="{{.Name}}"{{if .Required}} required{{end}}{{if .Error}} aria-invalid="true" aria-describedby="{{.ID}}-err"{{end}}>{{if not .Required}}<option value=""></option>{{end}}{{range .Choices}}<option{{if eq . $f.Value}} selected{{end}}>{{.}}</option>{{end}}</select>
{{- else}}<input id="{{.ID}}" name="{{.Name}}" type="{{.Type}}"{{if ne .Type "number"}} maxlength="{{.Max}}"{{else}} step="any"{{end}} value="{{.Value}}"{{if .Required}} required{{end}}{{with .Placeholder}} placeholder="{{.}}"{{end}}{{with .Autocomplete}} autocomplete="{{.}}"{{end}}{{if .Error}} aria-invalid="true" aria-describedby="{{.ID}}-err"{{end}}>{{end}}
{{- end}}
{{- with .Error}}<span class="plugin-form-error" id="{{$f.ID}}-err">{{.}}</span>{{end}}</p>
{{- end}}
<p class="comment-submit"><button type="submit">{{.Submit}}</button></p>
</form>
{{- end}}
</div>`))

// expandForms replaces form placeholders in a rendered page. Anonymous
// pages get token slots (filled per response by fillTokens, since the body
// may be cached); signed-in visitors get a token directly.
func (s *Server) expandForms(w http.ResponseWriter, r *http.Request, lang string, body []byte) []byte {
	if s.plugins == nil || !bytes.Contains(body, []byte(`<div data-form="`)) {
		return body
	}
	var flash *formFlash
	if pf := r.URL.Query().Get("pf"); pf != "" {
		flash = s.takeFormFlash(w, r, pf)
	}
	back := s.here(r)
	return formSlotRe.ReplaceAllFunc(body, func(m []byte) []byte {
		sub := formSlotRe.FindSubmatch(m)
		pid, fid := string(sub[1]), string(sub[2])
		f, ok := s.plugins.Form(pid, fid)
		if !ok {
			return nil
		}
		key := pid + "." + fid
		token := formTokenSlot + key
		if rctx(r).Session != nil {
			token = s.newFormToken(key)
		}
		var fl *formFlash
		if flash != nil && flash.Key == key {
			fl = flash
		}
		var buf bytes.Buffer
		if err := pluginFormTmpl.Execute(&buf, s.formView(f, pid, lang, back, token, fl)); err != nil {
			s.log.Error("render plugin form", "form", key, "err", err)
			return nil
		}
		return buf.Bytes()
	})
}

func formAnchor(pid, fid string) string { return "form-" + pid + "-" + fid }

func (s *Server) formView(f *plugin.FormDecl, pid, lang, back, token string, fl *formFlash) formView {
	v := formView{Anchor: formAnchor(pid, f.ID), Action: "/plugin-forms/" + pid + "/" + f.ID, Back: back, Lang: lang, Token: token,
		Title: f.Title.In(lang), Submit: f.Submit.In(lang)}
	if v.Submit == "" {
		v.Submit = s.i18n.T(lang, "Send")
	}
	if fl != nil {
		v.Notice, v.NoticeOK, v.Done = fl.Msg, fl.OK, fl.OK
	}
	for _, fd := range f.Fields {
		fv := formFieldView{ID: v.Anchor + "-" + fd.Name, Name: fd.Name, Type: fd.Type, Label: fd.Label.In(lang),
			Placeholder: fd.Placeholder.In(lang), Required: fd.Required, Max: fd.Limit(), Choices: fd.Choices}
		switch fd.Type {
		case "email":
			fv.Autocomplete = "email"
		case "tel":
			fv.Autocomplete = "tel"
		case "url":
			fv.Autocomplete = "url"
		}
		if fd.Name == "name" {
			fv.Autocomplete = "name"
		}
		if fl != nil && !fl.OK {
			fv.Value, fv.Error = fl.Values[fd.Name], fl.Errors[fd.Name]
			fv.Checked = fv.Value == "true"
		}
		v.Fields = append(v.Fields, fv)
	}
	return v
}

// newFormToken returns "nonce~signed-timestamp" for the form key.
func (s *Server) newFormToken(key string) string {
	b := make([]byte, 12)
	rand.Read(b)
	nonce := hex.EncodeToString(b)
	return nonce + "~" + s.keys.Sign("plugin-form", key+"/"+nonce, time.Now())
}

// fillFormTokens replaces token slots in a cached body.
func (s *Server) fillFormTokens(body []byte) []byte {
	if !bytes.Contains(body, []byte(formTokenSlot)) {
		return body
	}
	return formTokenRe.ReplaceAllFunc(body, func(m []byte) []byte {
		return []byte(s.newFormToken(string(m[len(formTokenSlot):])))
	})
}

// checkFormToken verifies a token's signature and returns its nonce and
// age, without spending it.
func (s *Server) checkFormToken(key, token string) (string, time.Duration, bool) {
	nonce, sig, ok := strings.Cut(token, "~")
	if !ok || len(nonce) != 24 {
		return "", 0, false
	}
	age, valid := s.keys.Verify("plugin-form", key+"/"+nonce, sig, formTokenTTL)
	return nonce, age, valid
}

// spendFormNonce records a nonce as used, reporting false when it was used
// before. A full generation is rotated out early: the oldest nonces are
// forgotten rather than refusing every new submission.
func (s *Server) spendFormNonce(nonce string) bool {
	st := s.plugWeb()
	st.nonceMu.Lock()
	defer st.nonceMu.Unlock()
	if time.Since(st.rotated) > formTokenTTL || len(st.nonces) >= maxNonces {
		st.oldNonces, st.nonces, st.rotated = st.nonces, map[string]struct{}{}, time.Now()
	}
	_, used := st.nonces[nonce]
	_, usedOld := st.oldNonces[nonce]
	if used || usedOld {
		return false
	}
	st.nonces[nonce] = struct{}{}
	return true
}

// handlePluginForm receives POST /plugin-forms/{plugin}/{form}. The
// cross-origin check runs in the middleware chain.
func (s *Server) handlePluginForm(w http.ResponseWriter, r *http.Request) {
	pid, fid := r.PathValue("plugin"), r.PathValue("form")
	f, ok := s.plugins.Form(pid, fid)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/x-www-form-urlencoded") {
		http.Error(w, "unsupported media type", http.StatusUnsupportedMediaType)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, formMaxBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}
	key := pid + "." + fid
	lang := r.PostFormValue("_lang")
	if !slices.Contains(s.siteLangs(), lang) {
		lang = s.mainLang()
	}
	back := safePublicPath(r.PostFormValue("_back"))
	values := map[string]string{}
	for _, fd := range f.Fields {
		v := r.PostFormValue(fd.Name)
		if fd.Type == "checkbox" {
			v = strconv.FormatBool(v == "on" || v == "true")
		}
		values[fd.Name] = truncateRunes(v, fd.Limit())
	}
	T := func(msg string, args ...any) string { return s.i18n.T(lang, msg, args...) }
	finish := func(ok bool, msg string, errs map[string]string) {
		fl := formFlash{Key: key, OK: ok, Msg: msg, Errors: errs}
		if !ok {
			fl.Values = values
		}
		s.setFormFlash(w, fl)
		http.Redirect(w, r, setQuery(back, "pf", key)+"#"+formAnchor(pid, fid), http.StatusSeeOther)
	}
	ip := clientIP(r).String()
	if !s.plugWeb().formIP.Allow(ipKey(clientIP(r))) {
		finish(false, T("Too many submissions. Wait a few minutes and try again."), nil)
		return
	}
	if r.PostFormValue("_hp") != "" {
		s.log.Info("plugin form rejected as spam", "form", key, "ip", ip, "reason", "honeypot")
		finish(true, T("Thanks! Your message was sent."), nil) // do not tell bots
		return
	}
	// The age is checked before the nonce is spent, so a submission that
	// was too fast can be sent again with the same token.
	nonce, age, valid := s.checkFormToken(key, r.PostFormValue("_token"))
	if valid && age < formMinAge {
		s.log.Info("plugin form rejected as spam", "form", key, "ip", ip, "reason", "too fast")
		finish(false, T("That was too fast. Wait a moment and send the form again."), nil)
		return
	}
	if !valid || !s.spendFormNonce(nonce) {
		finish(false, T("This form expired. Check the fields and send it again."), nil)
		return
	}
	fields, errs := validateFormFields(f, values, T)
	if len(errs) > 0 {
		finish(false, T("Check the highlighted fields."), errs)
		return
	}
	res, err := s.plugins.SubmitForm(r.Context(), pid, fid, fields, lang, s.cfg.AbsURL(back))
	if err != nil {
		s.log.Warn("plugin form failed", "form", key, "err", err)
		finish(false, T("The form could not be sent. Try again later."), nil)
		return
	}
	if !res.OK {
		msg := res.Message
		if msg == "" {
			msg = T("Check the highlighted fields.")
		}
		finish(false, msg, res.Errors)
		return
	}
	msg := res.Message
	if msg == "" {
		msg = T("Thanks! Your message was sent.")
	}
	finish(true, msg, nil)
}

// validateFormFields checks submitted values against the declaration and
// returns the clean fields, or errors by field name.
func validateFormFields(f *plugin.FormDecl, values map[string]string, T func(string, ...any) string) (map[string]string, map[string]string) {
	out, errs := map[string]string{}, map[string]string{}
	for _, fd := range f.Fields {
		v := values[fd.Name]
		if fd.Type == "textarea" {
			v = strings.TrimSpace(strings.ReplaceAll(v, "\r\n", "\n"))
		} else {
			v = strings.TrimSpace(v)
		}
		v = stripControl(v, fd.Type == "textarea")
		switch {
		case fd.Type == "checkbox":
			if fd.Required && v != "true" {
				errs[fd.Name] = T("This field is required.")
			}
		case v == "":
			if fd.Required {
				errs[fd.Name] = T("This field is required.")
			}
		case utf8.RuneCountInString(v) > fd.Limit():
			errs[fd.Name] = T("Too long (at most %d characters).", fd.Limit())
		case fd.Type == "email":
			if a, err := mail.ParseAddress(v); err != nil || a.Address != v || len(v) > 254 {
				errs[fd.Name] = T("Enter a valid e-mail address.")
			}
		case fd.Type == "url":
			if u, err := url.Parse(v); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				errs[fd.Name] = T("Enter a valid web address (https://…).")
			}
		case fd.Type == "tel":
			if !telRe.MatchString(v) {
				errs[fd.Name] = T("Enter a valid phone number.")
			}
		case fd.Type == "number":
			if _, err := strconv.ParseFloat(v, 64); err != nil {
				errs[fd.Name] = T("Enter a number.")
			}
		case fd.Type == "select":
			if !slices.Contains(fd.Choices, v) {
				errs[fd.Name] = T("Choose one of the options.")
			}
		}
		out[fd.Name] = v
	}
	return out, errs
}

// stripControl removes control characters (keeping newlines and tabs in
// multi-line text) and invalid UTF-8.
func stripControl(v string, multiline bool) string {
	v = strings.ToValidUTF8(v, "")
	return strings.Map(func(r rune) rune {
		if (r == '\n' || r == '\t') && multiline {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, v)
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// safePublicPath accepts a local path of the public site ("/" otherwise).
func safePublicPath(p string) string {
	n := safeNext(p)
	if n == "/admin/" || strings.HasPrefix(n, "/admin") || strings.HasPrefix(n, "/plugin-forms/") {
		return "/"
	}
	return n
}

// setQuery sets one query parameter on a local path.
func setQuery(p, key, value string) string {
	u, err := url.Parse(p)
	if err != nil {
		return p
	}
	q := u.Query()
	q.Set(key, value)
	u.RawQuery = q.Encode()
	u.Fragment = ""
	return u.String()
}

func (s *Server) setFormFlash(w http.ResponseWriter, fl formFlash) {
	raw, _ := json.Marshal(fl)
	if len(raw) > 2800 { // keep the cookie well under 4 KB
		fl.Values = nil
		raw, _ = json.Marshal(fl)
	}
	enc, err := s.keys.Encrypt(raw, []byte(formCookie))
	if err != nil {
		return
	}
	http.SetCookie(w, &http.Cookie{Name: formCookie, Value: base64.RawURLEncoding.EncodeToString(enc), Path: "/", MaxAge: 300,
		HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode})
}

// takeFormFlash reads and clears the submission outcome for form key.
func (s *Server) takeFormFlash(w http.ResponseWriter, r *http.Request, key string) *formFlash {
	ck, err := r.Cookie(formCookie)
	if err != nil {
		return nil
	}
	http.SetCookie(w, &http.Cookie{Name: formCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode})
	enc, err := base64.RawURLEncoding.DecodeString(ck.Value)
	if err != nil {
		return nil
	}
	raw, err := s.keys.Decrypt(enc, []byte(formCookie))
	if err != nil {
		return nil
	}
	var fl formFlash
	if json.Unmarshal(raw, &fl) != nil || fl.Key != key {
		return nil
	}
	return &fl
}
