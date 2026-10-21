package store

import (
	"context"
	"slices"
	"time"
)

// AICall is what the AI did on one day for one feature, kind of call,
// provider, model and source (ai_calls).
type AICall struct {
	Day          time.Time `db:"day"`
	Feature      string    `db:"feature"`
	Kind         string    `db:"kind"` // text or image
	Provider     string    `db:"provider"`
	Model        string    `db:"model"`
	Source       string    `db:"source"` // own or network
	Requests     int64     `db:"requests"`
	InputTokens  int64     `db:"input_tokens"`
	OutputTokens int64     `db:"output_tokens"`
	Images       int64     `db:"images"`
}

// The kinds of AI call, and where the AI came from.
const (
	AIKindText    = "text"
	AIKindImage   = "image"
	AICallOwn     = "own"
	AICallNetwork = "network"
)

// AddAICall adds c to its day's row.
func (s *Store) AddAICall(ctx context.Context, c AICall) error {
	if c.Requests == 0 && c.Images == 0 && c.InputTokens == 0 && c.OutputTokens == 0 {
		return nil
	}
	_, err := s.DB.Exec(ctx, `INSERT INTO ai_calls (day, feature, kind, provider, model, source, requests, input_tokens, output_tokens, images)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (day, feature, kind, provider, model, source) DO UPDATE SET
			requests = ai_calls.requests + EXCLUDED.requests,
			input_tokens = ai_calls.input_tokens + EXCLUDED.input_tokens,
			output_tokens = ai_calls.output_tokens + EXCLUDED.output_tokens,
			images = ai_calls.images + EXCLUDED.images`,
		dateOf(c.Day), truncateRunes(c.Feature, 40), c.Kind, truncateRunes(c.Provider, 40), truncateRunes(c.Model, 120), c.Source,
		c.Requests, c.InputTokens, c.OutputTokens, c.Images)
	return err
}

func truncateRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// SiteStats are a site's numbers for the network's usage statistics: what
// it holds now, and what happened from From to To (dates, both included).
type SiteStats struct {
	// Now.
	Posts, Pages           int64 // not in the trash; each translation counts
	PublishedPosts         int64
	PublishedPages         int64
	Drafts                 int64 // posts and pages
	Media, AIImages        int64 // AIImages: the generated images still in the library
	MediaBytes             int64
	Comments               int64 // approved
	Users                  int64
	AllRequests, AllImages int64 // the AI since the site began (ai_usage and the assistant's)
	AllInput, AllOutput    int64

	// In the period.
	Days  []StatsDay // one per date that has something, oldest first
	Calls []AICall   // the period's AI by feature, kind, provider, model and source (Day zero)
}

// StatsDay is a date's activity on a site.
type StatsDay struct {
	Day          time.Time `db:"day"`
	Posts        int64     `db:"posts"` // created
	Pages        int64     `db:"pages"`
	Media        int64     `db:"media"`
	Comments     int64     `db:"comments"`
	Views        int64     `db:"views"`
	Visitors     int64     `db:"visitors"`
	Requests     int64     `db:"requests"` // AI
	InputTokens  int64     `db:"input_tokens"`
	OutputTokens int64     `db:"output_tokens"`
	Images       int64     `db:"images"`
}

// SiteStats measures the site, with the activity from from to to.
func (s *Store) SiteStats(ctx context.Context, from, to time.Time) (*SiteStats, error) {
	st := &SiteStats{}
	from, to = dateOf(from), dateOf(to)
	err := s.DB.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE type = 'post' AND status <> 'trash'),
		count(*) FILTER (WHERE type = 'page' AND status <> 'trash'),
		count(*) FILTER (WHERE type = 'post' AND status = 'published'),
		count(*) FILTER (WHERE type = 'page' AND status = 'published'),
		count(*) FILTER (WHERE status IN ('draft', 'pending'))
		FROM posts`).Scan(&st.Posts, &st.Pages, &st.PublishedPosts, &st.PublishedPages, &st.Drafts)
	if err != nil {
		return nil, err
	}
	if err := s.DB.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE original_name LIKE '%-ai.%'), coalesce(sum(size), 0)::bigint FROM media`).
		Scan(&st.Media, &st.AIImages, &st.MediaBytes); err != nil {
		return nil, err
	}
	if err := s.DB.QueryRow(ctx, `SELECT (SELECT count(*) FROM comments WHERE status = 'approved'), (SELECT count(*) FROM users)`).
		Scan(&st.Comments, &st.Users); err != nil {
		return nil, err
	}
	if err := s.DB.QueryRow(ctx, `SELECT
		coalesce((SELECT sum(requests) FROM ai_usage), 0) + coalesce((SELECT sum(requests) FROM assistant_usage), 0),
		coalesce((SELECT sum(images) FROM ai_usage), 0),
		coalesce((SELECT sum(input_tokens) FROM ai_usage), 0) + coalesce((SELECT sum(input_tokens) FROM assistant_usage), 0),
		coalesce((SELECT sum(output_tokens) FROM ai_usage), 0) + coalesce((SELECT sum(output_tokens) FROM assistant_usage), 0)`).
		Scan(&st.AllRequests, &st.AllImages, &st.AllInput, &st.AllOutput); err != nil {
		return nil, err
	}
	// The dates of created_at are UTC's; those of the daily tables the site's.
	if st.Days, err = collect[StatsDay](s.DB.Query(ctx, `WITH d AS (
		SELECT (created_at AT TIME ZONE 'UTC')::date AS day, (type = 'post')::int AS posts, (type = 'page')::int AS pages,
			0 AS media, 0 AS comments, 0 AS views, 0 AS visitors, 0 AS requests, 0 AS input_tokens, 0 AS output_tokens, 0 AS images
			FROM posts WHERE created_at >= $1 AND created_at < $2::date + 1
		UNION ALL SELECT (created_at AT TIME ZONE 'UTC')::date, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0
			FROM media WHERE created_at >= $1 AND created_at < $2::date + 1
		UNION ALL SELECT (created_at AT TIME ZONE 'UTC')::date, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0
			FROM comments WHERE status = 'approved' AND created_at >= $1 AND created_at < $2::date + 1
		UNION ALL SELECT day, 0, 0, 0, 0, views, visitors, 0, 0, 0, 0 FROM analytics_days WHERE day BETWEEN $1 AND $2
		UNION ALL SELECT day, 0, 0, 0, 0, 0, 0, requests, input_tokens, output_tokens, images FROM ai_calls WHERE day BETWEEN $1 AND $2)
		SELECT day, sum(posts)::bigint AS posts, sum(pages)::bigint AS pages, sum(media)::bigint AS media,
			sum(comments)::bigint AS comments, sum(views)::bigint AS views, sum(visitors)::bigint AS visitors,
			sum(requests)::bigint AS requests, sum(input_tokens)::bigint AS input_tokens,
			sum(output_tokens)::bigint AS output_tokens, sum(images)::bigint AS images
		FROM d GROUP BY day ORDER BY day`, from, to)); err != nil {
		return nil, err
	}
	if st.Calls, err = collect[AICall](s.DB.Query(ctx, `SELECT '0001-01-01'::date AS day, feature, kind, provider, model, source,
		sum(requests)::bigint AS requests, sum(input_tokens)::bigint AS input_tokens,
		sum(output_tokens)::bigint AS output_tokens, sum(images)::bigint AS images
		FROM ai_calls WHERE day BETWEEN $1 AND $2 GROUP BY feature, kind, provider, model, source`, from, to)); err != nil {
		return nil, err
	}
	return st, nil
}

// Add adds o's numbers to st's: the days by date, the AI by feature, kind,
// provider, model and source.
func (st *SiteStats) Add(o *SiteStats) {
	st.Posts += o.Posts
	st.Pages += o.Pages
	st.PublishedPosts += o.PublishedPosts
	st.PublishedPages += o.PublishedPages
	st.Drafts += o.Drafts
	st.Media += o.Media
	st.AIImages += o.AIImages
	st.MediaBytes += o.MediaBytes
	st.Comments += o.Comments
	st.Users += o.Users
	st.AllRequests += o.AllRequests
	st.AllImages += o.AllImages
	st.AllInput += o.AllInput
	st.AllOutput += o.AllOutput

	days := make(map[time.Time]int, len(st.Days))
	for i, d := range st.Days {
		days[d.Day] = i
	}
	for _, d := range o.Days {
		i, ok := days[d.Day]
		if !ok {
			days[d.Day] = len(st.Days)
			st.Days = append(st.Days, d)
			continue
		}
		t := &st.Days[i]
		t.Posts += d.Posts
		t.Pages += d.Pages
		t.Media += d.Media
		t.Comments += d.Comments
		t.Views += d.Views
		t.Visitors += d.Visitors
		t.Requests += d.Requests
		t.InputTokens += d.InputTokens
		t.OutputTokens += d.OutputTokens
		t.Images += d.Images
	}
	slices.SortFunc(st.Days, func(a, b StatsDay) int { return a.Day.Compare(b.Day) })

	type key struct{ feature, kind, provider, model, source string }
	calls := make(map[key]int, len(st.Calls))
	for i, c := range st.Calls {
		calls[key{c.Feature, c.Kind, c.Provider, c.Model, c.Source}] = i
	}
	for _, c := range o.Calls {
		k := key{c.Feature, c.Kind, c.Provider, c.Model, c.Source}
		i, ok := calls[k]
		if !ok {
			calls[k] = len(st.Calls)
			st.Calls = append(st.Calls, c)
			continue
		}
		t := &st.Calls[i]
		t.Requests += c.Requests
		t.InputTokens += c.InputTokens
		t.OutputTokens += c.OutputTokens
		t.Images += c.Images
	}
}

// Period adds up the period's days.
func (st *SiteStats) Period() StatsDay {
	var p StatsDay
	for _, d := range st.Days {
		p.Posts += d.Posts
		p.Pages += d.Pages
		p.Media += d.Media
		p.Comments += d.Comments
		p.Views += d.Views
		p.Visitors += d.Visitors
		p.Requests += d.Requests
		p.InputTokens += d.InputTokens
		p.OutputTokens += d.OutputTokens
		p.Images += d.Images
	}
	return p
}
