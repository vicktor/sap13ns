package store

import (
	"context"
	"errors"
	"time"
)

// AISettings configure the writing assistant. They are kept apart from
// Settings because they hold the provider's API key, encrypted with the
// server secret (APIKey is never filled in by the store).
type AISettings struct {
	Enabled    bool   `json:"enabled"`
	Provider   string `json:"provider"` // "anthropic", "openai" or "gemini"
	APIKeyEnc  []byte `json:"api_key_enc,omitempty"`
	BaseURL    string `json:"base_url"`
	TextModel  string `json:"text_model"`
	ImageModel string `json:"image_model"`
	AllowLocal bool   `json:"allow_local"` // plain http:// to localhost (Ollama, llama.cpp)
	NoThinking bool   `json:"no_thinking"` // ask compatible servers to switch reasoning off
	MinRole    string `json:"min_role"`    // lowest role allowed to use it
	// Monthly limits per user; 0 means unlimited.
	MonthlyRequests int    `json:"monthly_requests"`
	MonthlyImages   int    `json:"monthly_images"`
	ImageStyle      string `json:"image_style"`
}

const aiSettingsKey = "ai"

func (s *Store) AISettings(ctx context.Context) (AISettings, error) {
	st := AISettings{Provider: "anthropic", MinRole: "author", MonthlyRequests: 300, MonthlyImages: 30}
	if err := s.GetJSON(ctx, aiSettingsKey, &st); err != nil && !errors.Is(err, ErrNotFound) {
		return st, err
	}
	return st, nil
}

func (s *Store) SaveAISettings(ctx context.Context, st AISettings) error {
	return s.SetJSON(ctx, aiSettingsKey, st)
}

// AIUsage is one user's use of the assistant in one month.
type AIUsage struct {
	Month        time.Time `db:"month"`
	UserID       int64     `db:"user_id"`
	UserName     string    `db:"user_name"`
	Requests     int       `db:"requests"`
	InputTokens  int64     `db:"input_tokens"`
	OutputTokens int64     `db:"output_tokens"`
	Images       int       `db:"images"`
}

func (u AIUsage) Tokens() int64 { return u.InputTokens + u.OutputTokens }

func monthOf(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC) }

func (s *Store) AddAIUsage(ctx context.Context, userID int64, at time.Time, requests int, in, out int64, images int) error {
	_, err := s.DB.Exec(ctx, `INSERT INTO ai_usage (month, user_id, requests, input_tokens, output_tokens, images)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (month, user_id) DO UPDATE SET requests = ai_usage.requests + EXCLUDED.requests,
			input_tokens = ai_usage.input_tokens + EXCLUDED.input_tokens,
			output_tokens = ai_usage.output_tokens + EXCLUDED.output_tokens, images = ai_usage.images + EXCLUDED.images`,
		monthOf(at), userID, requests, in, out, images)
	return err
}

// AIUsageOf returns a user's usage in the month of at (zero when none).
func (s *Store) AIUsageOf(ctx context.Context, userID int64, at time.Time) (AIUsage, error) {
	u := AIUsage{Month: monthOf(at), UserID: userID}
	err := s.DB.QueryRow(ctx, `SELECT requests, input_tokens, output_tokens, images FROM ai_usage WHERE month = $1 AND user_id = $2`,
		u.Month, userID).Scan(&u.Requests, &u.InputTokens, &u.OutputTokens, &u.Images)
	if err != nil && !errors.Is(mapErr(err), ErrNotFound) {
		return u, err
	}
	return u, nil
}

// AIUsageMonth lists every user's usage in the month of at.
func (s *Store) AIUsageMonth(ctx context.Context, at time.Time) ([]AIUsage, error) {
	return collect[AIUsage](s.DB.Query(ctx, `SELECT a.month, a.user_id, coalesce(nullif(u.display_name, ''), u.username) AS user_name,
		a.requests, a.input_tokens, a.output_tokens, a.images
		FROM ai_usage a JOIN users u ON u.id = a.user_id WHERE a.month = $1 ORDER BY a.requests DESC`, monthOf(at)))
}
