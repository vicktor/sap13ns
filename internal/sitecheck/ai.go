package sitecheck

import (
	"slices"

	"sap13ns/internal/ai"
	"sap13ns/internal/store"
)

// The fields of Settings → AI, in the order of its checks.
var aiFields = func() []field[store.AISettings] {
	type S = store.AISettings
	return []field[S]{
		{"provider", func(s *S) bool { return slices.Contains(ai.Providers, s.Provider) }, func(d, s *S) { d.Provider = s.Provider }},
		{"min_role", func(s *S) bool { return slices.Contains(AIRoles, s.MinRole) }, func(d, s *S) { d.MinRole = s.MinRole }},
		{"monthly_requests", func(s *S) bool { return s.MonthlyRequests >= 0 && s.MonthlyRequests <= 1_000_000 },
			func(d, s *S) { d.MonthlyRequests = s.MonthlyRequests }},
		{"monthly_images", func(s *S) bool { return s.MonthlyImages >= 0 && s.MonthlyImages <= 100_000 },
			func(d, s *S) { d.MonthlyImages = s.MonthlyImages }},
		{"text_model", func(s *S) bool { return !TooLong(s.TextModel, 100) }, func(d, s *S) { d.TextModel = s.TextModel }},
		{"image_model", func(s *S) bool { return !TooLong(s.ImageModel, 100) }, func(d, s *S) { d.ImageModel = s.ImageModel }},
		{"image_style", func(s *S) bool { return !TooLong(s.ImageStyle, 500) }, func(d, s *S) { d.ImageStyle = s.ImageStyle }},
		{"base_url", func(s *S) bool {
			return s.BaseURL == "" || len(s.BaseURL) <= 300 && ServiceURL(s.BaseURL, s.AllowLocal)
		},
			func(d, s *S) { d.BaseURL, d.AllowLocal = s.BaseURL, s.AllowLocal }},
	}
}()

// AI names the settings of the writing assistant that its page would
// refuse.
func AI(s store.AISettings) []string { return check(aiFields, s) }

// CleanAI resets what fails in the writing assistant's settings (see
// CleanSettings). It never touches the key.
func CleanAI(in, cur store.AISettings) (store.AISettings, []string) {
	return clean(aiFields, in, cur, store.DefaultAISettings())
}

// EmbedProviders are the embedding choices of the site assistant.
var EmbedProviders = []string{"same", "none", ai.ProviderOpenAI, ai.ProviderGemini}

// MaxSuggestions is how many suggested questions the assistant shows.
const MaxSuggestions = 6

func suggestionsOK(list []string) bool {
	if len(list) > MaxSuggestions {
		return false
	}
	for _, q := range list {
		if q == "" || TooLong(q, 120) {
			return false
		}
	}
	return true
}

// The fields of Settings → Assistant, in the order of its checks.
var assistantFields = func() []field[store.AssistantSettings] {
	type S = store.AssistantSettings
	return []field[S]{
		{"embed_provider", func(s *S) bool { return slices.Contains(EmbedProviders, s.EmbedProvider) },
			func(d, s *S) { d.EmbedProvider = s.EmbedProvider }},
		{"per_ip_hour", func(s *S) bool { return s.PerIPHour >= 0 && s.PerIPHour <= 10_000 }, func(d, s *S) { d.PerIPHour = s.PerIPHour }},
		{"daily_requests", func(s *S) bool { return s.DailyRequests >= 0 && s.DailyRequests <= 1_000_000 },
			func(d, s *S) { d.DailyRequests = s.DailyRequests }},
		{"monthly_requests", func(s *S) bool { return s.MonthlyRequests >= 0 && s.MonthlyRequests <= 10_000_000 },
			func(d, s *S) { d.MonthlyRequests = s.MonthlyRequests }},
		{"embed_dimensions", func(s *S) bool { return s.EmbedDimensions >= 0 && s.EmbedDimensions <= 4000 },
			func(d, s *S) { d.EmbedDimensions = s.EmbedDimensions }},
		{"name", func(s *S) bool { return !TooLong(s.Name, 60) }, func(d, s *S) { d.Name = s.Name }},
		{"greeting", func(s *S) bool { return !TooLong(s.Greeting, 300) }, func(d, s *S) { d.Greeting = s.Greeting }},
		{"suggestions", func(s *S) bool { return suggestionsOK(s.Suggestions) }, func(d, s *S) { d.Suggestions = s.Suggestions }},
		{"text", func(s *S) bool {
			for l, t := range s.Text {
				if !ValidLang(l) || TooLong(t.Greeting, 300) || !suggestionsOK(t.Suggestions) {
					return false
				}
			}
			return true
		}, func(d, s *S) { d.Text = s.Text }},
		{"embed_model", func(s *S) bool { return !TooLong(s.EmbedModel, 100) }, func(d, s *S) { d.EmbedModel = s.EmbedModel }},
		{"embed_base_url", func(s *S) bool {
			return s.EmbedBaseURL == "" || len(s.EmbedBaseURL) <= 300 && ServiceURL(s.EmbedBaseURL, s.EmbedAllowLocal)
		}, func(d, s *S) { d.EmbedBaseURL, d.EmbedAllowLocal = s.EmbedBaseURL, s.EmbedAllowLocal }},
	}
}()

// Assistant names the settings of the site assistant that its page would
// refuse.
func Assistant(s store.AssistantSettings) []string { return check(assistantFields, s) }

// CleanAssistant resets what fails in the site assistant's settings (see
// CleanSettings). It never touches the embedding key or the MCP token.
func CleanAssistant(in, cur store.AssistantSettings) (store.AssistantSettings, []string) {
	return clean(assistantFields, in, cur, store.DefaultAssistantSettings())
}
