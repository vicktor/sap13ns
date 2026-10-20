package analytics

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"
)

// Totals are the views and visitors of a period. Visitors is the sum of
// daily unique visitors: daily salts make visitors unlinkable across days.
type Totals struct {
	Views    int64
	Visitors int64
}

// DayStat is one day of the series.
type DayStat struct {
	Day      time.Time // the date, as midnight UTC
	Views    int64
	Visitors int64
}

// Item is one row of a top list.
type Item struct {
	Value string
	Views int64
	Share float64 // percent of the dimension's views in the period, one decimal
}

// Summary is the report of a date range.
type Summary struct {
	From, To time.Time // inclusive dates, as midnight UTC
	Totals
	// Previous covers the same number of days right before From.
	Previous       Totals
	ViewsChange    Delta
	VisitorsChange Delta
	// Series has one entry per day from From to To, zeros included.
	Series    []DayStat
	PeakViews int64 // highest daily views in Series, for scaling charts
	// Top lists, most viewed first. Pages and Langs shares are of all views;
	// Referrers shares are of entries (views not coming from the site
	// itself), whose largest source is usually "(direct)".
	Pages, Referrers, Browsers, OS, Devices, Langs []Item
	// Countries lists every country of the period (ISO 3166-1 alpha-2
	// codes), whatever the limit, for the map; shares are of the views
	// whose country is known.
	Countries []Item
}

// Delta is a relative change, as in "+12% vs previous period".
type Delta struct {
	Percent float64
	Valid   bool // false when there is nothing to compare with
}

// Compare returns the change from prev to cur.
func Compare(cur, prev int64) Delta {
	if prev == 0 {
		return Delta{}
	}
	return Delta{Percent: float64(cur-prev) * 100 / float64(prev), Valid: true}
}

// Up reports a rounded increase.
func (d Delta) Up() bool { return d.Valid && math.Round(d.Percent) > 0 }

// Down reports a rounded decrease.
func (d Delta) Down() bool { return d.Valid && math.Round(d.Percent) < 0 }

// String formats the change as "+12%", "-3%" or "0%", or "" when not Valid.
func (d Delta) String() string {
	if !d.Valid {
		return ""
	}
	switch p := math.Round(d.Percent); {
	case p > 0:
		return fmt.Sprintf("+%.0f%%", p)
	case p < 0:
		return fmt.Sprintf("%.0f%%", p)
	}
	return "0%"
}

const (
	// maxCountries bounds Summary.Countries: there are 249 ISO countries.
	maxCountries       = 300
	defaultReportLimit = 10
	maxReportLimit     = 1000
	maxReportDays      = 3660
)

// Report summarises the dates from..to (inclusive; only the calendar date of
// each is used, in its own location) with the top limit entries of every
// list. It reads what has been flushed; call Collector.Flush first to
// include the last minute.
func Report(ctx context.Context, db DB, from, to time.Time, limit int) (*Summary, error) {
	f, t := dateNum(from.Date()), dateNum(to.Date())
	if t < f {
		return nil, errors.New("analytics: report range ends before it starts")
	}
	n := int(t-f) + 1
	if n > maxReportDays {
		return nil, fmt.Errorf("analytics: report range longer than %d days", maxReportDays)
	}
	if limit <= 0 {
		limit = defaultReportLimit
	}
	limit = min(limit, maxReportLimit)

	s := &Summary{From: f.time(), To: t.time(), Series: make([]DayStat, n)}
	for i := range s.Series {
		s.Series[i].Day = (f + dayNum(i)).time()
	}
	if err := s.loadDays(ctx, db, f, t); err != nil {
		return nil, err
	}
	err := db.QueryRow(ctx, `SELECT coalesce(sum(views), 0)::int8, coalesce(sum(visitors), 0)::int8
		FROM analytics_days WHERE day BETWEEN $1 AND $2`, (f-dayNum(n)).time(), (f-1).time()).
		Scan(&s.Previous.Views, &s.Previous.Visitors)
	if err != nil {
		return nil, fmt.Errorf("analytics: previous period: %w", err)
	}
	s.ViewsChange = Compare(s.Views, s.Previous.Views)
	s.VisitorsChange = Compare(s.Visitors, s.Previous.Visitors)
	if s.Pages, err = topPages(ctx, db, f, t, limit); err != nil {
		return nil, err
	}
	if err := s.loadDims(ctx, db, f, t, limit); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Summary) loadDays(ctx context.Context, db DB, f, t dayNum) error {
	rows, err := db.Query(ctx, `SELECT day, views::int8, visitors::int8 FROM analytics_days
		WHERE day BETWEEN $1 AND $2`, f.time(), t.time())
	if err != nil {
		return fmt.Errorf("analytics: days: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var d DayStat
		if err := rows.Scan(&d.Day, &d.Views, &d.Visitors); err != nil {
			return fmt.Errorf("analytics: days: %w", err)
		}
		i := int(dateNum(d.Day.Date()) - f)
		if i < 0 || i >= len(s.Series) {
			continue
		}
		d.Day = s.Series[i].Day
		s.Series[i] = d
		s.Views += d.Views
		s.Visitors += d.Visitors
		s.PeakViews = max(s.PeakViews, d.Views)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("analytics: days: %w", err)
	}
	return nil
}

func topPages(ctx context.Context, db DB, f, t dayNum, limit int) ([]Item, error) {
	rows, err := db.Query(ctx, `SELECT path, sum(views)::int8 AS v, (sum(sum(views)) OVER ())::int8
		FROM analytics_pages WHERE day BETWEEN $1 AND $2
		GROUP BY path ORDER BY v DESC, path LIMIT $3`, f.time(), t.time(), limit)
	if err != nil {
		return nil, fmt.Errorf("analytics: pages: %w", err)
	}
	defer rows.Close()
	var items []Item
	for rows.Next() {
		var it Item
		var total int64
		if err := rows.Scan(&it.Value, &it.Views, &total); err != nil {
			return nil, fmt.Errorf("analytics: pages: %w", err)
		}
		it.Share = share(it.Views, total)
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("analytics: pages: %w", err)
	}
	return items, nil
}

func (s *Summary) loadDims(ctx context.Context, db DB, f, t dayNum, limit int) error {
	rows, err := db.Query(ctx, `SELECT dim, value, v, total FROM (
			SELECT dim, value, sum(views)::int8 AS v,
				(sum(sum(views)) OVER (PARTITION BY dim))::int8 AS total,
				row_number() OVER (PARTITION BY dim ORDER BY sum(views) DESC, value) AS rn
			FROM analytics_dims WHERE day BETWEEN $1 AND $2
			GROUP BY dim, value
		) s WHERE rn <= CASE WHEN dim = 'country' THEN $4::int8 ELSE $3::int8 END ORDER BY dim, rn`, f.time(), t.time(), limit, maxCountries)
	if err != nil {
		return fmt.Errorf("analytics: dimensions: %w", err)
	}
	defer rows.Close()
	lists := map[string]*[]Item{
		"referrer": &s.Referrers, "browser": &s.Browsers, "os": &s.OS, "device": &s.Devices, "lang": &s.Langs, "country": &s.Countries,
	}
	for rows.Next() {
		var dim string
		var it Item
		var total int64
		if err := rows.Scan(&dim, &it.Value, &it.Views, &total); err != nil {
			return fmt.Errorf("analytics: dimensions: %w", err)
		}
		if l := lists[dim]; l != nil {
			it.Share = share(it.Views, total)
			*l = append(*l, it)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("analytics: dimensions: %w", err)
	}
	return nil
}

func share(v, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return math.Round(float64(v)*1000/float64(total)) / 10
}

// Prune deletes the statistics of the days before olderThan (its calendar
// date) and returns the number of rows removed.
func Prune(ctx context.Context, db DB, olderThan time.Time) (int64, error) {
	var n int64
	err := db.QueryRow(ctx, `WITH
		a AS (DELETE FROM analytics_days WHERE day < $1 RETURNING 1),
		b AS (DELETE FROM analytics_pages WHERE day < $1 RETURNING 1),
		c AS (DELETE FROM analytics_dims WHERE day < $1 RETURNING 1)
		SELECT (SELECT count(*) FROM a) + (SELECT count(*) FROM b) + (SELECT count(*) FROM c)`,
		dateNum(olderThan.Date()).time()).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("analytics: prune: %w", err)
	}
	return n, nil
}
