package web

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"sap13ns/internal/analytics"
	"sap13ns/internal/store"
)

// Cookie-less analytics (internal/analytics). cached() records every public
// HTML page served to anonymous visitors; staff browsing with a session is
// never counted. Aggregates are kept for statsRetention.

const statsRetention = 2 * 365 * 24 * time.Hour

func (s *Server) newStats() *analytics.Collector {
	return analytics.New(s.store.DB, analytics.Options{
		Location: s.loc(),
		Log:      s.log,
		ClientIP: func(r *http.Request) string { return clientIP(r).String() },
		// Stable per install, so that a restart keeps today's unique
		// visitors comparable; the daily salt still rotates in memory.
		SiteSalt: s.keys.SignBytes([]byte("analytics visitor salt")),
		Hosts:    append([]string{s.cfg.BaseURL.Hostname()}, s.hosts...),
		Disabled: s.settings().AnalyticsOff,
	})
}

// recordView counts a public page view. Only successful HTML responses count.
func (s *Server) recordView(r *http.Request, status int, contentType string) {
	if status != http.StatusOK || !strings.HasPrefix(contentType, "text/html") {
		return
	}
	lang := rctx(r).Lang
	s.stats.Record(r, s.langPath(lang, r.URL.Path), cmp.Or(lang, s.mainLang()))
}

// Close flushes what the analytics collector and the redirect hit counters
// hold in memory. Call it after the HTTP server has shut down.
func (s *Server) Close(ctx context.Context) error {
	s.flushRedirectHits(ctx)
	return s.stats.Close(ctx)
}

func pruneStats(ctx context.Context, st *store.Store, log *slog.Logger) {
	if n, err := analytics.Prune(ctx, st.DB, time.Now().Add(-statsRetention)); err != nil {
		log.Error("prune analytics", "err", err)
	} else if n > 0 {
		log.Info("old analytics pruned", "rows", n)
	}
}

// ------------------------------------------------------------------ admin --

var statsRanges = []struct {
	Key  string
	Days int
}{{"7d", 7}, {"30d", 30}, {"90d", 90}, {"12m", 365}}

type statsData struct {
	Range  string
	Ranges []string
	Off    bool
	S      *analytics.Summary
	Chart  statsChart
}

type statsChart struct {
	W, H  int
	Bars  []statsBar
	Ticks []statsTick
}

type statsBar struct {
	X, Y, W, H float64
	Label      string
}

type statsTick struct {
	Y     float64
	Label string
}

func (s *Server) adminStats(w http.ResponseWriter, r *http.Request) {
	key, days := statsRanges[1].Key, statsRanges[1].Days
	for _, rg := range statsRanges {
		if r.URL.Query().Get("range") == rg.Key {
			key, days = rg.Key, rg.Days
		}
	}
	d := statsData{Range: key, Off: s.settings().AnalyticsOff}
	for _, rg := range statsRanges {
		d.Ranges = append(d.Ranges, rg.Key)
	}
	if err := s.stats.Flush(r.Context()); err != nil { // include the last minute
		s.log.Warn("analytics flush", "err", err)
	}
	to := time.Now().In(s.loc())
	sum, err := analytics.Report(r.Context(), s.store.DB, to.AddDate(0, 0, -(days-1)), to, 10)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	d.S, d.Chart = sum, s.statsChart(sum)
	s.renderAdmin(w, r, "analytics", s.t("Analytics"), "analytics", d)
}

// statsChart lays out the daily views as SVG bars. Geometry is computed here
// because the CSP forbids style attributes; colours come from CSS classes.
func (s *Server) statsChart(sum *analytics.Summary) statsChart {
	c := statsChart{W: 720, H: 180}
	n := len(sum.Series)
	if n == 0 {
		return c
	}
	peak := niceCeil(sum.PeakViews)
	step := float64(c.W) / float64(n)
	gap := min(step*0.2, 2)
	for i, day := range sum.Series {
		h := float64(day.Views) / float64(peak) * float64(c.H)
		c.Bars = append(c.Bars, statsBar{X: float64(i)*step + gap/2, Y: float64(c.H) - h, W: step - gap, H: h,
			Label: s.t("%s: %d views, %d visitors", day.Day.Format("2006-01-02"), day.Views, day.Visitors)})
	}
	for _, f := range []float64{0, 0.5, 1} {
		c.Ticks = append(c.Ticks, statsTick{Y: float64(c.H) * (1 - f), Label: fmt.Sprint(int64(float64(peak) * f))})
	}
	return c
}

// niceCeil rounds n up to 1, 2 or 5 times a power of ten (at least 10).
func niceCeil(n int64) int64 {
	for p := int64(1); ; p *= 10 {
		for _, m := range []int64{1, 2, 5} {
			if v := m * p; v >= max(n, 10) {
				return v
			}
		}
	}
}

// dashStats is the dashboard card: the last 7 days.
type dashStats struct {
	Views, Visitors int64
	Change          analytics.Delta
}

func (s *Server) dashboardStats(ctx context.Context) *dashStats {
	if s.settings().AnalyticsOff {
		return nil
	}
	to := time.Now().In(s.loc())
	sum, err := analytics.Report(ctx, s.store.DB, to.AddDate(0, 0, -6), to, 1)
	if err != nil {
		s.log.Warn("dashboard analytics", "err", err)
		return nil
	}
	return &dashStats{Views: sum.Views, Visitors: sum.Visitors, Change: sum.VisitorsChange}
}
