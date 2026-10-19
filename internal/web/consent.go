package web

// Cookie consent on the public site (docs/SECURITY.md, "Cookie consent").
//
// Every feature that stores something in the visitor's browser, or loads
// content from another site, declares it: the core here (coreConsentItems),
// plugins in plugin.json "privacy" (internal/plugin/privacy.go). The list
// is grouped in categories; necessary storage needs no consent. The banner
// appears only when something outside "necessary" stores data without
// asking at its own placeholder; the cookie settings dialog, reached from
// the footer link that shared/footer-meta renders on every page, lists
// everything either way.
//
// The server never reads the visitor's choice. Pages are the same for
// everyone (the page cache keeps one copy per language and path), and
// static/consent.js keeps the choice in localStorage under consentKey and
// applies it in the browser: it shows the banner, holds the click-to-load
// placeholders' notes and tells plugin scripts what they may load
// (window.sapConsent).

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strconv"
	"strings"

	"sap13ns/internal/plugin"
	"sap13ns/internal/store"
)

const (
	// consentKey is the localStorage entry with the visitor's choice.
	consentKey = "sap13ns-consent"
	// consentDays is how long a choice lasts: six months, as the CNIL
	// recommends, after which the banner asks again (a refusal too).
	consentDays = 182
)

// ConsentItem is one entry of the cookie list.
type ConsentItem struct {
	Kind      string // plugin.PrivacyCookie, PrivacyStorage, PrivacyService or PrivacyResource
	KindLabel string
	Name      string
	Service   string // services: the consent id shared by every placeholder of the service
	Category  string
	// Provider is who sets it, "" for the site itself; ProviderLabel is
	// what the list shows.
	Provider      string
	ProviderLabel string
	Purpose       string
	Lifetime      string
	Policy        string
	ClickToLoad   bool
	// Sources are what declares it: "sap13ns" for the core, or plugin
	// names.
	Sources []string
}

// Clear reports whether consent.js deletes the item when the visitor
// withdraws consent: the site's own optional cookies and storage.
func (i ConsentItem) Clear() bool {
	return i.Provider == "" && i.Category != plugin.CategoryNecessary &&
		(i.Kind == plugin.PrivacyCookie || i.Kind == plugin.PrivacyStorage)
}

// asks reports whether the item stores data in the browser without asking
// at a placeholder, so that the banner must ask first.
func (i ConsentItem) asks() bool {
	if i.Category == plugin.CategoryNecessary {
		return false
	}
	return i.Kind == plugin.PrivacyCookie || i.Kind == plugin.PrivacyStorage || i.Kind == plugin.PrivacyService && !i.ClickToLoad
}

// ConsentCategory is a group of the cookie list.
type ConsentCategory struct {
	ID, Name, Description string
	Required              bool // necessary: always on
	// Toggle: the visitor can turn the category on or off; a category of
	// resources only (loaded with the page, storing nothing) has none.
	Toggle bool
	Items  []ConsentItem
}

// ConsentView is what shared/consent renders, in the page's language.
type ConsentView struct {
	Key  string // localStorage key
	Rev  string // version of the choice: a new category or a revision asks again
	Days int    // lifetime of a choice
	// Banner: new visitors see the banner; otherwise only the footer link
	// opens the dialog.
	Banner     bool
	Title      string
	Text       string
	PolicyURL  string
	Categories []ConsentCategory
}

// Optional reports whether anything on the site needs consent.
func (v *ConsentView) Optional() bool {
	return slices.ContainsFunc(v.Categories, func(c ConsentCategory) bool { return c.Toggle })
}

// Consent returns the cookie consent, or nil when it is off (and in the
// customiser's preview, where it would cover the design being edited).
// shared/head and shared/footer-meta render it.
func (v *SiteView) Consent() *ConsentView {
	if v.server == nil || v.Custom != nil && v.Custom.Preview {
		return nil
	}
	if !v.consentDone {
		v.consent, v.consentDone = v.server.consentView(v.Lang), true
	}
	return v.consent
}

// ConsentOnce reports true the first time a page asks: the banner and the
// dialog are rendered once, even where a layout shows the footer credit
// twice.
func (p *PageData) ConsentOnce() bool {
	if p.consentDone {
		return false
	}
	p.consentDone = true
	return true
}

func (s *Server) consentView(lang string) *ConsentView {
	st := s.settings()
	c := st.Consent
	if c.Off {
		return nil
	}
	tr := func(key string, args ...any) string { return s.i18n.T(lang, key, args...) }
	txt := c.In(lang)
	if lang == st.Language {
		txt = store.ConsentText{Title: c.Title, Text: c.Text, PolicyURL: c.PolicyURL}
	}
	v := &ConsentView{Key: consentKey, Days: consentDays, Title: txt.Title, Text: txt.Text, PolicyURL: txt.PolicyURL}
	if v.Title == "" {
		v.Title = tr("Cookies on this site")
	}
	if v.Text == "" {
		v.Text = tr(defaultConsentText)
	}
	v.Categories = s.consentCategories(lang)
	v.Banner, _ = consentBanner(v.Categories, c.AlwaysBanner)
	var toggles []string
	for _, cat := range v.Categories {
		if cat.Toggle {
			toggles = append(toggles, cat.ID)
		}
	}
	sum := sha256.Sum256([]byte(strings.Join(toggles, ",") + "|" + strconv.Itoa(c.Revision)))
	v.Rev = hex.EncodeToString(sum[:5])
	return v
}

// defaultConsentText is the banner's text when the administrator wrote
// none.
const defaultConsentText = "This site uses the cookies it needs to work. With your permission, it also uses optional cookies and content from other sites, listed in the settings. You can change your choice at any time with the “Cookies” link at the bottom of every page."

// consentBanner reports whether new visitors see the banner, and whether
// anything needs consent at all: the banner asks when something optional
// stores data without asking at its own placeholder, or always when the
// administrator wants it and there is something to ask.
func consentBanner(cats []ConsentCategory, always bool) (banner, optional bool) {
	asks := false
	for _, cat := range cats {
		optional = optional || cat.Toggle
		for _, it := range cat.Items {
			asks = asks || it.asks()
		}
	}
	return optional && (asks || always), optional
}

// consentCategories groups the cookie list, in lang: the categories with
// at least one entry, in plugin.PrivacyCategories order.
func (s *Server) consentCategories(lang string) []ConsentCategory {
	tr := func(key string, args ...any) string { return s.i18n.T(lang, key, args...) }
	items := s.consentItems(lang)
	var out []ConsentCategory
	for _, id := range plugin.PrivacyCategories {
		cat := ConsentCategory{ID: id, Required: id == plugin.CategoryNecessary}
		switch id {
		case plugin.CategoryNecessary:
			cat.Name = tr("Strictly necessary")
			cat.Description = tr("Needed for the site to work and for what you ask of it: signing in, sending a form, remembering the light or dark mode you pick and this choice. They cannot be turned off.")
		case plugin.CategoryPreferences:
			cat.Name = tr("Preferences")
			cat.Description = tr("Remember choices that change how the site looks or behaves for you.")
		case plugin.CategoryAnalytics:
			cat.Name = tr("Analytics")
			cat.Description = tr("Help to understand how the site is used. The site's own visit statistics use no cookies and are not listed here.")
		case plugin.CategoryMedia:
			cat.Name = tr("External media and marketing")
			cat.Description = tr("Content from other sites, such as videos and social media posts, and what those sites store once it loads. Videos load only when you press play.")
		}
		for _, it := range items {
			if it.Category != id {
				continue
			}
			cat.Items = append(cat.Items, it)
			cat.Toggle = cat.Toggle || !cat.Required && it.Kind != plugin.PrivacyResource
		}
		if len(cat.Items) > 0 {
			out = append(out, cat)
		}
	}
	return out
}

// consentItems is the whole list in lang: the core's entries, then the
// enabled plugins'. An entry declared twice (YouTube by the core and a
// plugin) is listed once, with both sources.
func (s *Server) consentItems(lang string) []ConsentItem {
	tr := func(key string, args ...any) string { return s.i18n.T(lang, key, args...) }
	items := s.coreConsentItems(lang)
	forms := false
	for _, pp := range s.plugins.Privacy() {
		forms = forms || pp.Forms
		for _, d := range pp.Decls {
			items = append(items, ConsentItem{Kind: d.Type, Name: d.Name, Service: d.Service, Category: d.Category,
				Provider: d.Provider, Purpose: d.Purpose.In(lang), Lifetime: d.Lifetime.In(lang), Policy: d.Policy,
				ClickToLoad: d.ClickToLoad, Sources: []string{pp.Name}})
		}
	}
	if forms {
		items = append(items, ConsentItem{Kind: plugin.PrivacyCookie, Name: formCookie, Category: plugin.CategoryNecessary,
			Purpose:  tr("Carries the outcome of a form you sent, encrypted, to the page you return to."),
			Lifetime: tr("5 minutes"), Sources: []string{"sap13ns"}})
	}
	var out []ConsentItem
	at := map[string]int{}
	for _, it := range items {
		key := it.Kind + "\x00" + cmp.Or(it.Service, it.Name)
		if i, seen := at[key]; seen {
			for _, src := range it.Sources {
				if !slices.Contains(out[i].Sources, src) {
					out[i].Sources = append(out[i].Sources, src)
				}
			}
			continue
		}
		at[key] = len(out)
		switch it.Kind {
		case plugin.PrivacyCookie:
			it.KindLabel = tr("Cookie")
		case plugin.PrivacyStorage:
			it.KindLabel = tr("Browser storage")
		case plugin.PrivacyService:
			it.KindLabel = tr("Content from another site")
			if it.ClickToLoad {
				it.KindLabel = tr("Content from another site, on request")
			}
		case plugin.PrivacyResource:
			it.KindLabel = tr("Files from another site, with the page")
		}
		it.ProviderLabel = cmp.Or(it.Provider, tr("This site"))
		if it.Lifetime == "" {
			it.Lifetime = "—"
		}
		out = append(out, it)
	}
	return out
}

// coreConsentItems is what sap13ns itself stores or loads.
func (s *Server) coreConsentItems(lang string) []ConsentItem {
	tr := func(key string, args ...any) string { return s.i18n.T(lang, key, args...) }
	st := s.settings()
	core := []string{"sap13ns"}
	nec := func(kind, name, purpose, lifetime string) ConsentItem {
		return ConsentItem{Kind: kind, Name: name, Category: plugin.CategoryNecessary, Purpose: purpose, Lifetime: lifetime, Sources: core}
	}
	items := []ConsentItem{
		nec(plugin.PrivacyCookie, s.cookieName(), tr("Keeps you signed in and protects your forms against forgery. Set only when you sign in."),
			tr("%d days, or until you sign out", st.SessionDays)),
		nec(plugin.PrivacyCookie, flashCookie, tr("Shows a one-time message after you send a form, such as a comment."), tr("2 minutes")),
		nec(plugin.PrivacyCookie, s.ceremonyCookieName("login")+", "+s.ceremonyCookieName("reg"),
			tr("Links the steps of signing in, or adding a passkey, with a passkey."), tr("5 minutes")),
	}
	if s.sso != nil {
		items = append(items, nec(plugin.PrivacyCookie, s.ssoCookieName(), tr("Links the steps of signing in with your network account."), tr("10 minutes")))
	}
	items = append(items,
		nec(plugin.PrivacyStorage, "themeMode", tr("Remembers the light or dark mode you chose with the button. Stored only when you press it."),
			tr("Until you clear your browser's data")),
		nec(plugin.PrivacyStorage, consentKey, tr("Remembers your choices in the cookie settings."), tr("6 months")),
	)
	if _, on := s.assistantOn(context.Background()); on {
		items = append(items, nec(plugin.PrivacyStorage, "sap-assistant:"+lang,
			tr("Keeps your conversation with the site assistant while the tab is open."), tr("Until you close the tab")))
	}
	if st.VideoEmbeds != "off" {
		items = append(items,
			ConsentItem{Kind: plugin.PrivacyService, Name: "YouTube", Service: "youtube", Category: plugin.CategoryMedia,
				Provider: "Google Ireland Limited", Policy: "https://policies.google.com/privacy", ClickToLoad: true, Sources: core,
				Purpose:  tr("Plays the YouTube videos of posts and pages from youtube-nocookie.com. Nothing loads until you press play; then YouTube receives your IP address and may store data on your device."),
				Lifetime: tr("Set by the provider")},
			ConsentItem{Kind: plugin.PrivacyService, Name: "Vimeo", Service: "vimeo", Category: plugin.CategoryMedia,
				Provider: "Vimeo.com, Inc.", Policy: "https://vimeo.com/privacy", ClickToLoad: true, Sources: core,
				Purpose:  tr("Plays the Vimeo videos of posts and pages in Do Not Track mode. Nothing loads until you press play; then Vimeo receives your IP address and may store technical data on your device."),
				Lifetime: tr("Set by the provider")},
		)
	}
	return items
}
