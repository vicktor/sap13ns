package store

import (
	"context"
	"encoding/json"
	"errors"
)

// Settings are the site-wide options edited from the admin panel.
type Settings struct {
	SiteTitle string `json:"site_title"`
	Tagline   string `json:"tagline"`
	Language  string `json:"language"` // main language, served without a URL prefix
	// Languages are the extra content languages, each served under /<code>/.
	Languages []string `json:"languages"`
	// SiteText translates the title, tagline and footer per extra language.
	SiteText             map[string]SiteText `json:"site_text"`
	Timezone             string              `json:"timezone"`
	PostsPerPage         int                 `json:"posts_per_page"`
	FrontPage            string              `json:"front_page"` // "posts" | "page" | "theme" (internal/web/front.go)
	FrontPageID          int64               `json:"front_page_id"`
	ActiveTheme          string              `json:"active_theme"`
	FooterText           string              `json:"footer_text"`
	DiscourageSearch     bool                `json:"discourage_search"`
	CommentsEnabled      bool                `json:"comments_enabled"`
	CommentModeration    string              `json:"comment_moderation"` // "all" | "returning" | "none"
	CommentsCloseDays    int                 `json:"comments_close_days"`
	CommentsRequireLogin bool                `json:"comments_require_login"`
	AllowRegistration    bool                `json:"allow_registration"`
	DefaultRole          string              `json:"default_role"`
	FeedItems            int                 `json:"feed_items"`
	FeedFullText         bool                `json:"feed_full_text"`
	WebSubHub            string              `json:"websub_hub"`
	// TwoFactor is who must use a second factor (TOTP or a passkey):
	// "staff" (everyone with admin access, the default) or "editors"
	// (administrators and editors only). It cannot be switched off.
	TwoFactor string `json:"two_factor"`
	// OGCards is the layout of generated Open Graph images ("classic",
	// "bold", "minimal"), or "off". Empty means classic.
	OGCards  string `json:"og_cards"`
	OGAccent string `json:"og_accent"` // hex colour; empty uses the sap13ns violet
	// AIAccess controls AI crawlers and the llms.txt/Markdown endpoints:
	// "open" (default), "no-training" (answer engines yes, training no) or
	// "block" (every AI agent is disallowed and the endpoints are off).
	AIAccess     string `json:"ai_access"`
	AnalyticsOff bool   `json:"analytics_off"`
	SessionDays  int    `json:"session_days"`
	IdleHours    int    `json:"idle_hours"`
	// GravatarUsers shows the Gravatar of users without a photo of their
	// own (on by default). The server looks it up with the SHA-256 of the
	// e-mail address and serves a copy; off, nothing is sent to Gravatar
	// and the copies are deleted.
	GravatarUsers bool `json:"gravatar_users"`
	// GravatarCommenters does the same for commenters (off by default);
	// it needs GravatarUsers.
	GravatarCommenters bool `json:"gravatar_commenters"`
}

// SiteText is the translatable part of the settings.
type SiteText struct {
	SiteTitle  string `json:"site_title"`
	Tagline    string `json:"tagline"`
	FooterText string `json:"footer_text"`
}

// Text returns the site title, tagline and footer in lang, falling back to the
// main-language values for anything not translated.
func (s Settings) Text(lang string) SiteText {
	out := SiteText{SiteTitle: s.SiteTitle, Tagline: s.Tagline, FooterText: s.FooterText}
	if t, ok := s.SiteText[lang]; ok && lang != s.Language {
		if t.SiteTitle != "" {
			out.SiteTitle = t.SiteTitle
		}
		if t.Tagline != "" {
			out.Tagline = t.Tagline
		}
		if t.FooterText != "" {
			out.FooterText = t.FooterText
		}
	}
	return out
}

func DefaultSettings() Settings {
	return Settings{
		SiteTitle:         "Mi sitio",
		Tagline:           "Otro sitio hecho con sap13ns",
		Language:          "es",
		Timezone:          "Europe/Madrid",
		PostsPerPage:      10,
		FrontPage:         "posts",
		ActiveTheme:       "apple",
		CommentsEnabled:   true,
		CommentModeration: "returning",
		CommentsCloseDays: 0,
		DefaultRole:       "subscriber",
		FeedItems:         20,
		FeedFullText:      true,
		SessionDays:       14,
		IdleHours:         48,
		TwoFactor:         "staff",
		OGCards:           "classic",
		AIAccess:          "open",
		GravatarUsers:     true,
	}
}

func (s *Store) GetJSON(ctx context.Context, key string, dst any) error {
	var raw []byte
	err := s.DB.QueryRow(ctx, `SELECT value FROM settings WHERE key = $1`, key).Scan(&raw)
	if err != nil {
		return mapErr(err)
	}
	return json.Unmarshal(raw, dst)
}

func (s *Store) SetJSON(ctx context.Context, key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(ctx, `INSERT INTO settings (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, key, raw)
	return err
}

// LoadSettings merges the stored settings over the defaults.
func (s *Store) LoadSettings(ctx context.Context) (Settings, error) {
	st := DefaultSettings()
	err := s.GetJSON(ctx, "site", &st)
	if errors.Is(err, ErrNotFound) {
		return st, nil
	}
	return st, err
}

func (s *Store) SaveSettings(ctx context.Context, st Settings) error {
	return s.SetJSON(ctx, "site", st)
}

// ThemeOptions are per-theme values declared in each theme manifest.
func (s *Store) ThemeOptions(ctx context.Context, theme string) (map[string]string, error) {
	out := map[string]string{}
	err := s.GetJSON(ctx, "theme:"+theme, &out)
	if errors.Is(err, ErrNotFound) {
		return out, nil
	}
	return out, err
}

func (s *Store) SaveThemeOptions(ctx context.Context, theme string, opts map[string]string) error {
	return s.SetJSON(ctx, "theme:"+theme, opts)
}
