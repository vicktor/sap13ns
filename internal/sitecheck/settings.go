package sitecheck

import (
	"regexp"
	"slices"
	"strings"

	"sap13ns/internal/auth"
	"sap13ns/internal/store"
)

// MaxLanguages is how many extra content languages a site can have.
const MaxLanguages = 10

// Site is what checking the settings needs to know about the site.
type Site struct {
	// PublicPage reports whether id is a published page, which the front
	// page may be. Nil accepts none.
	PublicPage func(id int64) bool
	// Theme reports whether a theme is installed. Nil checks the name
	// only (an unknown theme falls back to the default when loaded).
	Theme func(slug string) bool
	// SignIn reports whether path, which has the form of LoginPath, is
	// free to be the site's sign-in address: no address the site serves
	// itself, and no page or redirect there (web.SignInPathFree). Nil
	// accepts only DefaultLoginPath.
	SignIn func(path string) bool
}

// DefaultLoginPath is the sign-in address of a site that never chose
// another one.
const DefaultLoginPath = "/login"

// LoginPath matches the sign-in addresses an administrator can choose:
// one or two segments of lowercase letters, digits and dashes, such as
// /entrar or /equipo/acceso.
var LoginPath = regexp.MustCompile(`^/[a-z0-9][a-z0-9-]{2,40}(/[a-z0-9][a-z0-9-]{2,40})?$`)

// UnderSignIn reports whether the redirect source src, normalised as the
// redirects page saves it, is the sign-in address loginPath or under it,
// where the sign-in pages answer first: no redirect may start there.
func UnderSignIn(loginPath, src string) bool {
	p, _, _ := strings.Cut(src, "?")
	if p = strings.ToLower(p); len(p) > 1 {
		p = strings.TrimSuffix(p, "/")
	}
	return p == loginPath || strings.HasPrefix(p, loginPath+"/")
}

// signInOK is the rule of the sign-in address of s.
func signInOK(s *store.Settings, c Site) bool {
	if s.LoginPath == DefaultLoginPath {
		return true
	}
	return LoginPath.MatchString(s.LoginPath) && c.SignIn != nil && c.SignIn(s.LoginPath)
}

// The fields of the settings page (Settings → General), in its order. The
// names are the form's.
func settingsFields(c Site) []field[store.Settings] {
	type S = store.Settings
	in := func(v string, set ...string) bool { return slices.Contains(set, v) }
	between := func(n, lo, hi int) bool { return n >= lo && n <= hi }
	return []field[S]{
		{"site_title", func(s *S) bool { return s.SiteTitle != "" && !TooLong(s.SiteTitle, 120) },
			func(d, s *S) { d.SiteTitle = s.SiteTitle }},
		{"tagline", func(s *S) bool { return !TooLong(s.Tagline, 250) }, func(d, s *S) { d.Tagline = s.Tagline }},
		{"footer_text", func(s *S) bool { return !TooLong(s.FooterText, 1000) }, func(d, s *S) { d.FooterText = s.FooterText }},
		{"language", func(s *S) bool { return ValidLang(s.Language) }, func(d, s *S) { d.Language = s.Language }},
		{"languages", func(s *S) bool { return slices.Equal(s.Languages, cleanLanguages(s.Languages, s.Language)) },
			func(d, _ *S) { d.Languages = cleanLanguages(d.Languages, d.Language) }}, // keeps the valid ones
		{"site_text", func(s *S) bool { return siteTextOK(s) }, func(d, _ *S) { d.SiteText = cleanSiteText(d.SiteText, d.Languages) }},
		{"timezone", func(s *S) bool { return ValidZone(s.Timezone) }, func(d, s *S) { d.Timezone = s.Timezone }},
		{"posts_per_page", func(s *S) bool { return between(s.PostsPerPage, 1, 100) }, func(d, s *S) { d.PostsPerPage = s.PostsPerPage }},
		{"front_page", func(s *S) bool {
			return s.FrontPage == "posts" || s.FrontPage == "theme" || s.FrontPage == "page" && c.PublicPage != nil && c.PublicPage(s.FrontPageID)
		}, func(d, s *S) { d.FrontPage, d.FrontPageID = s.FrontPage, s.FrontPageID }},
		{"comment_moderation", func(s *S) bool { return in(s.CommentModeration, "all", "returning", "none") },
			func(d, s *S) { d.CommentModeration = s.CommentModeration }},
		{"comments_close_days", func(s *S) bool { return between(s.CommentsCloseDays, 0, 3650) },
			func(d, s *S) { d.CommentsCloseDays = s.CommentsCloseDays }},
		{"default_role", func(s *S) bool { return in(s.DefaultRole, auth.RoleSubscriber, auth.RoleContributor, auth.RoleAuthor) },
			func(d, s *S) { d.DefaultRole = s.DefaultRole }},
		{"feed_items", func(s *S) bool { return between(s.FeedItems, 1, 100) }, func(d, s *S) { d.FeedItems = s.FeedItems }},
		{"websub_hub", func(s *S) bool {
			return s.WebSubHub == "" || strings.HasPrefix(s.WebSubHub, "https://") && HTTPURL(s.WebSubHub)
		},
			func(d, s *S) { d.WebSubHub = s.WebSubHub }},
		{"two_factor", func(s *S) bool { return in(s.TwoFactor, "staff", "editors") }, func(d, s *S) { d.TwoFactor = s.TwoFactor }},
		{"og_cards", func(s *S) bool { return in(s.OGCards, "classic", "bold", "minimal", "off") }, func(d, s *S) { d.OGCards = s.OGCards }},
		{"og_accent", func(s *S) bool { return s.OGAccent == "" || HexColor.MatchString(s.OGAccent) }, func(d, s *S) { d.OGAccent = s.OGAccent }},
		{"ai_access", func(s *S) bool { return in(s.AIAccess, "open", "no-training", "block") }, func(d, s *S) { d.AIAccess = s.AIAccess }},
		{"session_days", func(s *S) bool { return between(s.SessionDays, 1, 90) }, func(d, s *S) { d.SessionDays = s.SessionDays }},
		{"idle_hours", func(s *S) bool { return between(s.IdleHours, 1, 24*90) }, func(d, s *S) { d.IdleHours = s.IdleHours }},
		// Commenters' Gravatars need the users' setting, which is the one
		// that sends anything to Gravatar at all.
		{"gravatar_commenters", func(s *S) bool { return !s.GravatarCommenters || s.GravatarUsers },
			func(d, s *S) { d.GravatarCommenters = s.GravatarCommenters }},
		{"login_path", func(s *S) bool { return signInOK(s, c) }, func(d, s *S) { d.LoginPath = s.LoginPath }},
		// Not on the page: set by activating a theme.
		{"active_theme", func(s *S) bool { return Slug.MatchString(s.ActiveTheme) && (c.Theme == nil || c.Theme(s.ActiveTheme)) },
			func(d, s *S) { d.ActiveTheme = s.ActiveTheme }},
	}
}

// cleanLanguages keeps the valid extra languages: known, not main, once
// each, at most MaxLanguages.
func cleanLanguages(list []string, main string) []string {
	var out []string
	for _, l := range list {
		if ValidLang(l) && l != main && !slices.Contains(out, l) && len(out) < MaxLanguages {
			out = append(out, l)
		}
	}
	return out
}

func siteTextOK(s *store.Settings) bool {
	for l, t := range s.SiteText {
		if !slices.Contains(s.Languages, l) || !siteTextFits(t) {
			return false
		}
	}
	return true
}

func siteTextFits(t store.SiteText) bool {
	return !TooLong(t.SiteTitle, 120) && !TooLong(t.Tagline, 250) && !TooLong(t.FooterText, 1000)
}

// cleanSiteText keeps the translations of enabled languages that fit.
func cleanSiteText(in map[string]store.SiteText, langs []string) map[string]store.SiteText {
	out := map[string]store.SiteText{}
	for l, t := range in {
		if slices.Contains(langs, l) && siteTextFits(t) {
			out[l] = t
		}
	}
	return out
}

// Settings names the settings that the settings page would refuse, in
// its order.
func Settings(s store.Settings, c Site) []string { return check(settingsFields(c), s) }

// CleanSettings resets every setting of in that fails to cur's value, or
// to the default when cur's fails too, and names the settings reset. The
// extra languages and their texts keep what is valid of them.
func CleanSettings(in, cur store.Settings, c Site) (store.Settings, []string) {
	return clean(settingsFields(c), in, cur, store.DefaultSettings())
}
