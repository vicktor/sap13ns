package web

import (
	"cmp"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"sap13ns/internal/ai"
	"sap13ns/internal/store"
)

// The network's usage statistics (/admin/network/stats), for super admins:
// the sites, accounts and plans of the network; the content, media and
// visits of every site; and what the AI did, by provider, model, feature
// and source.

// NetStats are the network's numbers, from NetworkStats.
type NetStats struct {
	From, To                   time.Time
	Active, Pending, Suspended int64 // tenant sites
	StorageBytes               int64
	Accounts, Subscriptions    int64 // network accounts; active subscriptions
	NewSites, NewAccounts      int64 // in the period
	ByPlan                     []NetCount
	Growth                     []NetDay
	Total                      *store.SiteStats // every site added up
	Sites                      []NetSiteStats
	Failed                     int // sites that could not be measured
}

// NetCount is a number of things with a name.
type NetCount struct {
	Name string
	N    int64
}

// NetDay is the network's growth on one date.
type NetDay struct {
	Day             time.Time
	Sites, Accounts int64
}

// NetSiteStats is one site's numbers.
type NetSiteStats struct {
	ID                 int64
	Slug, Plan, Status string
	StorageBytes       int64
	Stats              *store.SiteStats
	Period             store.StatsDay
}

type netStatsData struct {
	Range  string
	Ranges []string
	S      *NetStats
	P      store.StatsDay // the period, added up
	Cards  []netStatCard
	AI     []netStatCard
	Charts []netChart
	// The period's AI, grouped: by provider, model, feature and source.
	Breakdowns []netBreakdown
	Top        []netTopSite
	Lang       string
}

type netStatCard struct {
	Icon, Label, Value, Foot string
}

type netChart struct {
	Title, Note string
	W, H        int
	Bars        []statsBar
	Ticks       []statsTick
	Legend      []netLegend
	Empty       bool
}

type netLegend struct{ Class, Label string }

type netBreakdown struct {
	Title string
	Rows  []netBreakdownRow
}

type netBreakdownRow struct {
	Name                           string
	Requests, Input, Output, Image string
	Share                          float64 // of the requests, in %
}

type netTopSite struct {
	NetSiteStats
	Posts, Pages, Media, Views, Requests, Tokens, Images string
}

func (s *Server) adminNetStats(w http.ResponseWriter, r *http.Request) {
	if !s.superAdmin(w, r) {
		return
	}
	key, days := "30d", 30
	var keys []string
	for _, rg := range statsRanges {
		keys = append(keys, rg.Key)
		if r.URL.Query().Get("range") == rg.Key {
			key, days = rg.Key, rg.Days
		}
	}
	to := time.Now().UTC()
	from := to.AddDate(0, 0, -(days - 1))
	st, err := s.portal.NetworkStats(r.Context(), from, to)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	lang := s.uiLang(r)
	n := func(v int64) string { return fmtNum(lang, v) }
	d := netStatsData{Range: key, Ranges: keys, S: st, P: st.Total.Period(), Lang: lang}
	t := st.Total
	d.Cards = []netStatCard{
		{"layers", s.tu(r, "Sites"), n(st.Active + st.Pending + st.Suspended),
			s.tu(r, "%s active, %s pending, %s suspended", n(st.Active), n(st.Pending), n(st.Suspended))},
		{"users", s.tu(r, "Accounts"), n(st.Accounts), s.tu(r, "%s new in the period", n(st.NewAccounts))},
		{"credit-card", s.tu(r, "Active subscriptions"), n(st.Subscriptions), s.tu(r, "%s new sites in the period", n(st.NewSites))},
		{"file-text", s.tu(r, "Posts"), n(t.Posts), s.tu(r, "%s published, %s new in the period", n(t.PublishedPosts), n(d.P.Posts))},
		{"file", s.tu(r, "Pages"), n(t.Pages), s.tu(r, "%s published, %s new in the period", n(t.PublishedPages), n(d.P.Pages))},
		{"image", s.tu(r, "Media"), n(t.Media), s.tu(r, "%s made with AI, %s new in the period", n(t.AIImages), n(d.P.Media))},
		{"hard-drive", s.tu(r, "Storage"), filesize(st.StorageBytes), s.tu(r, "%s in the media libraries", filesize(t.MediaBytes))},
		{"message-square", s.tu(r, "Comments"), n(t.Comments), s.tu(r, "%s in the period", n(d.P.Comments))},
		{"eye", s.tu(r, "Page views"), n(d.P.Views), s.tu(r, "%s visitors in the period", n(d.P.Visitors))},
	}
	d.AI = []netStatCard{
		{"bot", s.tu(r, "AI requests"), n(d.P.Requests), s.tu(r, "%s since the start", n(t.AllRequests))},
		{"arrow-down-to-line", s.tu(r, "Input tokens"), compactNum(lang, d.P.InputTokens), s.tu(r, "%s since the start", compactNum(lang, t.AllInput))},
		{"arrow-up-from-line", s.tu(r, "Output tokens"), compactNum(lang, d.P.OutputTokens), s.tu(r, "%s since the start", compactNum(lang, t.AllOutput))},
		{"image-plus", s.tu(r, "AI images"), n(d.P.Images), s.tu(r, "%s since the start", n(t.AllImages))},
	}
	dates := statsDates(st.From, st.To)
	byDay := make(map[time.Time]store.StatsDay, len(t.Days))
	for _, dd := range t.Days {
		byDay[dd.Day.UTC()] = dd
	}
	growth := make(map[time.Time]NetDay, len(st.Growth))
	for _, g := range st.Growth {
		growth[g.Day.UTC()] = g
	}
	series := func(f func(time.Time) [2]int64) [][2]int64 {
		out := make([][2]int64, len(dates))
		for i, day := range dates {
			out[i] = f(day)
		}
		return out
	}
	label := func(a, b string) [2]string { return [2]string{a, b} }
	d.Charts = []netChart{
		s.netChart(r, lang, s.tu(r, "New content per day"), dates, label(s.tu(r, "Posts"), s.tu(r, "Pages")),
			series(func(t time.Time) [2]int64 { return [2]int64{byDay[t].Posts, byDay[t].Pages} })),
		s.netChart(r, lang, s.tu(r, "Growth per day"), dates, label(s.tu(r, "Sites"), s.tu(r, "Accounts")),
			series(func(t time.Time) [2]int64 { return [2]int64{growth[t].Sites, growth[t].Accounts} })),
		s.netChart(r, lang, s.tu(r, "AI requests per day"), dates, label(s.tu(r, "Requests"), ""),
			series(func(t time.Time) [2]int64 { return [2]int64{byDay[t].Requests, 0} })),
		s.netChart(r, lang, s.tu(r, "AI tokens per day"), dates, label(s.tu(r, "Input tokens"), s.tu(r, "Output tokens")),
			series(func(t time.Time) [2]int64 { return [2]int64{byDay[t].InputTokens, byDay[t].OutputTokens} })),
		s.netChart(r, lang, s.tu(r, "AI images per day"), dates, label(s.tu(r, "Images"), ""),
			series(func(t time.Time) [2]int64 { return [2]int64{byDay[t].Images, 0} })),
		s.netChart(r, lang, s.tu(r, "Page views per day"), dates, label(s.tu(r, "Page views"), ""),
			series(func(t time.Time) [2]int64 { return [2]int64{byDay[t].Views, 0} })),
	}
	d.Breakdowns = []netBreakdown{
		{s.tu(r, "By provider"), s.netBreakdown(lang, t.Calls, func(c store.AICall) string { return providerName(c.Provider) })},
		{s.tu(r, "By model"), s.netBreakdown(lang, t.Calls, func(c store.AICall) string {
			if c.Model == "" {
				return providerName(c.Provider) + " · " + s.tu(r, "default model")
			}
			return c.Model
		})},
		{s.tu(r, "By feature"), s.netBreakdown(lang, t.Calls, func(c store.AICall) string { return s.aiFeatureLabel(r, c.Feature) })},
		{s.tu(r, "By source"), s.netBreakdown(lang, t.Calls, func(c store.AICall) string {
			if c.Source == store.AICallNetwork {
				return s.tu(r, "The network's AI (plans)")
			}
			return s.tu(r, "The sites' own keys")
		})},
	}
	top := slices.Clone(st.Sites)
	slices.SortFunc(top, func(a, b NetSiteStats) int {
		ta, tb := a.Period.InputTokens+a.Period.OutputTokens, b.Period.InputTokens+b.Period.OutputTokens
		return cmp.Or(cmp.Compare(tb, ta), cmp.Compare(b.Period.Requests, a.Period.Requests),
			cmp.Compare(b.Stats.Posts+b.Stats.Pages, a.Stats.Posts+a.Stats.Pages), cmp.Compare(a.ID, b.ID))
	})
	for _, x := range top[:min(len(top), 50)] {
		d.Top = append(d.Top, netTopSite{NetSiteStats: x, Posts: n(x.Stats.Posts), Pages: n(x.Stats.Pages), Media: n(x.Stats.Media),
			Views: n(x.Period.Views), Requests: n(x.Period.Requests), Tokens: compactNum(lang, x.Period.InputTokens+x.Period.OutputTokens),
			Images: n(x.Period.Images)})
	}
	s.renderAdmin(w, r, "network_stats", s.tu(r, "Usage statistics"), "net-stats", d)
}

// statsDates lists the dates from from to to.
func statsDates(from, to time.Time) []time.Time {
	var out []time.Time
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		out = append(out, d)
	}
	return out
}

// netChart lays out one or two stacked series as SVG bars, as statsChart
// does: geometry here, colours from CSS classes.
func (s *Server) netChart(r *http.Request, lang, title string, dates []time.Time, names [2]string, values [][2]int64) netChart {
	c := netChart{Title: title, W: 720, H: 160}
	var peak, total int64
	for _, v := range values {
		peak = max(peak, v[0]+v[1])
		total += v[0] + v[1]
	}
	c.Empty = total == 0
	c.Legend = []netLegend{{"bar", names[0]}}
	if names[1] != "" {
		c.Legend = append(c.Legend, netLegend{"bar bar-2", names[1]})
	}
	if len(values) == 0 {
		return c
	}
	top := niceCeil(peak)
	step := float64(c.W) / float64(len(values))
	gap := min(step*0.2, 2)
	for i, v := range values {
		x := float64(i)*step + gap/2
		h0 := float64(v[0]) / float64(top) * float64(c.H)
		h1 := float64(v[1]) / float64(top) * float64(c.H)
		tip := dates[i].Format("2006-01-02") + ": " + names[0] + " " + fmtNum(lang, v[0])
		if names[1] != "" {
			tip += ", " + names[1] + " " + fmtNum(lang, v[1])
		}
		if h0 > 0 {
			c.Bars = append(c.Bars, statsBar{X: x, Y: float64(c.H) - h0, W: step - gap, H: h0, Label: tip})
		}
		if h1 > 0 {
			c.Bars = append(c.Bars, statsBar{X: x, Y: float64(c.H) - h0 - h1, W: step - gap, H: h1, Label: tip, Second: true})
		}
	}
	for _, f := range []float64{0, 0.5, 1} {
		c.Ticks = append(c.Ticks, statsTick{Y: float64(c.H) * (1 - f), Label: compactNum(lang, int64(float64(top)*f))})
	}
	return c
}

// netBreakdown groups the AI calls by name, most requested first.
func (s *Server) netBreakdown(lang string, calls []store.AICall, name func(store.AICall) string) []netBreakdownRow {
	type sum struct{ req, in, out, img int64 }
	sums := map[string]*sum{}
	var all int64
	for _, c := range calls {
		k := name(c)
		if sums[k] == nil {
			sums[k] = &sum{}
		}
		x := sums[k]
		x.req += c.Requests
		x.in += c.InputTokens
		x.out += c.OutputTokens
		x.img += c.Images
		all += c.Requests
	}
	keys := make([]string, 0, len(sums))
	for k := range sums {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b string) int {
		return cmp.Or(cmp.Compare(sums[b].req, sums[a].req), cmp.Compare(sums[b].in+sums[b].out, sums[a].in+sums[a].out), strings.Compare(a, b))
	})
	var out []netBreakdownRow
	for _, k := range keys {
		x := sums[k]
		row := netBreakdownRow{Name: k, Requests: fmtNum(lang, x.req), Input: compactNum(lang, x.in), Output: compactNum(lang, x.out),
			Image: fmtNum(lang, x.img)}
		if all > 0 {
			row.Share = float64(x.req) / float64(all) * 100
		}
		out = append(out, row)
	}
	return out
}

// providerName is the brand of a provider code.
func providerName(code string) string {
	switch code {
	case ai.ProviderAnthropic:
		return "Anthropic"
	case ai.ProviderOpenAI:
		return "OpenAI"
	case ai.ProviderGemini:
		return "Gemini"
	case ai.ProviderAlibaba:
		return "Alibaba Cloud (Qwen)"
	case "":
		return "—"
	}
	return code
}

// aiFeatureLabel names what an AI call was for.
func (s *Server) aiFeatureLabel(r *http.Request, f string) string {
	switch f {
	case "summary":
		return s.tu(r, "Summaries")
	case "seo":
		return s.tu(r, "SEO")
	case "write":
		return s.tu(r, "Writing")
	case "image":
		return s.tu(r, "Images")
	case "translate":
		return s.tu(r, "Translations")
	case "design":
		return s.tu(r, "Design with AI")
	case "assistant":
		return s.tu(r, "Site assistant")
	}
	return f
}

// fmtNum writes n with the thousands grouped as lang does.
func fmtNum(lang string, n int64) string {
	sep := ","
	if numDot(lang) {
		sep = "."
	}
	neg := n < 0
	if neg {
		n = -n
	}
	digits := strconv.FormatInt(n, 10)
	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	for i, c := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteString(sep)
		}
		b.WriteRune(c)
	}
	return b.String()
}

// compactNum writes large numbers short: 12.3k, 4.5M (with lang's
// decimal mark).
func compactNum(lang string, n int64) string {
	var v float64
	var unit string
	switch {
	case n >= 1_000_000_000:
		v, unit = float64(n)/1e9, "B"
	case n >= 1_000_000:
		v, unit = float64(n)/1e6, "M"
	case n >= 10_000:
		v, unit = float64(n)/1e3, "k"
	default:
		return fmtNum(lang, n)
	}
	out := fmt.Sprintf("%.1f", v)
	out = strings.TrimSuffix(out, ".0")
	if numDot(lang) {
		out = strings.Replace(out, ".", ",", 1)
	}
	return out + unit
}

// numDot: lang groups thousands with dots and writes a decimal comma.
func numDot(lang string) bool {
	switch strings.SplitN(lang, "-", 2)[0] {
	case "es", "de", "it", "pt", "nl", "ca", "gl", "eu", "da", "id", "tr":
		return true
	}
	return false
}
