package analytics

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// botUAs are real User-Agents of automated clients and how they classify.
var botUAs = []struct {
	ua   string
	cat  Category
	name string
}{
	{"", Other, NoUA},
	{"   ", Other, NoUA},

	// Search engines.
	{"Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)", Search, "Googlebot"},
	{"Mozilla/5.0 (Linux; Android 6.0.1; Nexus 5X Build/MMB29P) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.7390.54 Mobile Safari/537.36 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)", Search, "Googlebot"},
	{"Googlebot-Image/1.0", Search, "Googlebot"},
	{"Mozilla/5.0 (compatible; Google-InspectionTool/1.0;)", Search, "Google-InspectionTool"},
	{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36 GoogleOther", Search, "GoogleOther"},
	{"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; bingbot/2.0; +http://www.bing.com/bingbot.htm) Chrome/116.0.1938.76 Safari/537.36", Search, "Bingbot"},
	{"DuckDuckBot/1.1; (+http://duckduckgo.com/duckduckbot.html)", Search, "DuckDuckBot"},
	{"Mozilla/5.0 (compatible; YandexBot/3.0; +http://yandex.com/bots)", Search, "YandexBot"},
	{"Mozilla/5.0 (compatible; Baiduspider/2.0; +http://www.baidu.com/search/spider.html)", Search, "Baiduspider"},
	{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_5) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/13.1.1 Safari/605.1.15 (Applebot/0.1; +http://www.apple.com/go/applebot)", Search, "Applebot"},
	{"Mozilla/5.0 (compatible; Qwantbot/1.0_4193332; +https://help.qwant.com/bot/)", Search, "Qwantbot"},
	{"Mozilla/5.0 (compatible; SeznamBot/4.0; +https://o-seznam.cz/napoveda/vyhledavani/en/seznambot-crawler/)", Search, "SeznamBot"},
	{"Mozilla/5.0 (compatible; Yeti/1.1; +https://naver.me/spd)", Search, "Yeti (Naver)"},
	{"Sogou web spider/4.0(+http://www.sogou.com/docs/help/webmasters.htm#07)", Search, "Sogou"},
	{"Mozilla/5.0 (compatible; EcosiaBot/1.0; +https://www.ecosia.org/bot)", Search, "EcosiaBot"},
	{"Mozilla/5.0 (compatible; Yahoo! Slurp; http://help.yahoo.com/help/us/ysearch/slurp)", Search, "Yahoo! Slurp"},

	// AI crawlers and agents, including those of search companies.
	{"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko); compatible; GPTBot/1.2; +https://openai.com/gptbot", AI, "GPTBot"},
	{"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko); compatible; ChatGPT-User/1.0; +https://openai.com/bot", AI, "ChatGPT-User"},
	{"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko); compatible; OAI-SearchBot/1.0; +https://openai.com/searchbot", AI, "OAI-SearchBot"},
	{"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; ClaudeBot/1.0; +claudebot@anthropic.com)", AI, "ClaudeBot"},
	{"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; Claude-User/1.0; +Claude-User@anthropic.com)", AI, "Claude-User"},
	{"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; Claude-SearchBot/1.0; +Claude-SearchBot@anthropic.com)", AI, "Claude-SearchBot"},
	{"anthropic-ai/1.0", AI, "anthropic-ai"},
	{"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; PerplexityBot/1.0; +https://perplexity.ai/perplexitybot)", AI, "PerplexityBot"},
	{"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; Perplexity-User/1.0; +https://perplexity.ai/perplexity-user)", AI, "Perplexity-User"},
	{"Google-Extended", AI, "Google-Extended"},
	{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_5) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/13.1.1 Safari/605.1.15 (Applebot-Extended/0.1; +http://www.apple.com/go/applebot)", AI, "Applebot-Extended"},
	{"CCBot/2.0 (https://commoncrawl.org/faq/)", AI, "CCBot"},
	{"Mozilla/5.0 (Linux; Android 5.0) AppleWebKit/537.36 (KHTML, like Gecko) Mobile Safari/537.36 (compatible; Bytespider; spider-feedback@bytedance.com)", AI, "Bytespider"},
	{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.6 Safari/605.1.15 (Amazonbot/0.1; +https://developer.amazon.com/support/amazonbot)", AI, "Amazonbot"},
	{"meta-externalagent/1.1 (+https://developers.facebook.com/docs/sharing/webmasters/crawler)", AI, "Meta-ExternalAgent"},
	{"meta-externalfetcher/1.1 (+https://developers.facebook.com/docs/sharing/webmasters/crawler)", AI, "Meta-ExternalFetcher"},
	{"cohere-ai", AI, "cohere-ai"},
	{"Mozilla/5.0 (compatible; Diffbot/0.1; +http://www.diffbot.com)", AI, "Diffbot"},
	{"Mozilla/5.0 (compatible; YouBot (+http://www.you.com))", AI, "YouBot"},
	{"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; MistralAI-User/1.0; +https://docs.mistral.ai/robots)", AI, "MistralAI-User"},
	{"Mozilla/5.0 (compatible; DeepSeekBot/1.0; +https://www.deepseek.com/bot)", AI, "DeepSeekBot"},
	{"Mozilla/5.0 (compatible; Timpibot/0.8; +http://www.timpi.io)", AI, "Timpibot"},
	{"Mozilla/5.0 (compatible; ImagesiftBot; +imagesift.com)", AI, "ImagesiftBot"},
	{"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; DuckAssistBot/1.2; +http://duckduckgo.com/duckassistbot.html)", AI, "DuckAssistBot"},

	// Link previews.
	{"facebookexternalhit/1.1 (+http://www.facebook.com/externalhit_uatext.php)", Social, "facebookexternalhit"},
	{"Twitterbot/1.0", Social, "Twitterbot"},
	{"LinkedInBot/1.0 (compatible; Mozilla/5.0; Apache-HttpClient +http://www.linkedin.com)", Social, "LinkedInBot"},
	{"Slackbot-LinkExpanding 1.0 (+https://api.slack.com/robots)", Social, "Slackbot"},
	{"Mozilla/5.0 (compatible; Discordbot/2.0; +https://discordapp.com)", Social, "Discordbot"},
	{"TelegramBot (like TwitterBot)", Social, "TelegramBot"},
	{"WhatsApp/2.23.20.0", Social, "WhatsApp"},
	{"Pinterest/0.2 (+http://www.pinterest.com/)", Social, "Pinterestbot"},
	{"Mozilla/5.0 (compatible; redditbot/1.0; +http://www.reddit.com/feedback)", Social, "redditbot"},
	{"Mozilla/5.0 (compatible; Embedly/0.2; +http://support.embed.ly/)", Social, "Embedly"},
	{"Mastodon/4.4.0 (http.rb/5.2.0; +https://mastodon.social/)", Social, "Mastodon"},

	// SEO tools and monitors.
	{"Mozilla/5.0+(compatible; UptimeRobot/2.0; http://www.uptimerobot.com/)", SEO, "UptimeRobot"},
	{"Pingdom.com_bot_version_1.4_(http://www.pingdom.com/)", SEO, "Pingdom"},
	{"Mozilla/5.0 (compatible; AhrefsBot/7.0; +http://ahrefs.com/robot/)", SEO, "AhrefsBot"},
	{"Mozilla/5.0 (compatible; SemrushBot/7~bl; +http://www.semrush.com/bot.html)", SEO, "SemrushBot"},
	{"Mozilla/5.0 (compatible; MJ12bot/v1.4.8; http://mj12bot.com/)", SEO, "MJ12bot"},
	{"Mozilla/5.0 (compatible; DotBot/1.2; +https://opensiteexplorer.org/dotbot; help@moz.com)", SEO, "DotBot"},
	{"Mozilla/5.0 (Linux; Android 7.0;) AppleWebKit/537.36 (KHTML, like Gecko) Mobile Safari/537.36 (compatible; PetalBot;+https://webmaster.petalsearch.com/site/petalbot)", SEO, "PetalBot"},
	{"Mozilla/5.0 (compatible; BLEXBot/1.0; +https://help.seranking.com/en/blex-crawler)", SEO, "BLEXBot"},
	{"Mozilla/5.0 (compatible; DataForSeoBot/1.0; +https://dataforseo.com/dataforseo-bot)", SEO, "DataForSeoBot"},
	{"Screaming Frog SEO Spider/21.0", SEO, "Screaming Frog"},
	{"Mozilla/5.0 (Linux; Android 11; moto g power (2022)) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Mobile Safari/537.36 Chrome-Lighthouse", SEO, "Lighthouse"},
	{"Uptime-Kuma/2.0.0", SEO, "Uptime Kuma"},

	// Other bots, libraries and headless browsers: the name comes from
	// the User-Agent.
	{"curl/8.7.1", Other, "curl"},
	{"Wget/1.24.5", Other, "Wget"},
	{"python-requests/2.32.3", Other, "python-requests"},
	{"Python-urllib/3.12", Other, "Python-urllib"},
	{"Go-http-client/2.0", Other, "Go-http-client"},
	{"axios/1.7.7", Other, "axios"},
	{"node-fetch/1.0 (+https://github.com/bitinn/node-fetch)", Other, "node-fetch"},
	{"Java/21.0.4", Other, "Java"},
	{"Scrapy/2.11.2 (+https://scrapy.org)", Other, "Scrapy"},
	{"libwww-perl/6.77", Other, "libwww-perl"},
	{"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/141.0.0.0 Safari/537.36", Other, "HeadlessChrome"},
	{"Mozilla/5.0 (Unknown; Linux x86_64) AppleWebKit/538.1 (KHTML, like Gecko) PhantomJS/2.1.1 Safari/538.1", Other, "PhantomJS"},
	{"Mozilla/5.0 (compatible; Barkrowler/0.9; +https://babbar.tech/crawler)", SEO, "Barkrowler"},
	{"Mozilla/5.0 (compatible; NewThingBot/0.3; +https://example.com/bot)", Other, "NewThingBot"},
	{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36 SomeCrawler/2.0", Other, "SomeCrawler"},
	{"Mozilla/5.0", Other, "(unknown)"},
	{"Feedly/1.0 (+http://www.feedly.com/fetcher.html; 3 subscribers)", Other, "Feedly"},
	{"Mozilla/5.0 (compatible; <script>alert(1)</script>/1.0)", Other, "scriptalert"},
}

// uaOf returns the first User-Agent of botUAs named name.
func uaOf(t *testing.T, name string) string {
	t.Helper()
	for _, tc := range botUAs {
		if tc.name == name {
			return tc.ua
		}
	}
	t.Fatalf("no User-Agent of %s", name)
	return ""
}

func TestClassify(t *testing.T) {
	for _, tc := range botUAs {
		if got := Classify(tc.ua); got != (Agent{tc.cat, tc.name}) {
			t.Errorf("Classify(%q) = %+v, want {%s %s}", tc.ua, got, tc.cat, tc.name)
		}
		if !IsBot(tc.ua) {
			t.Errorf("IsBot(%q) = false", tc.ua)
		}
	}
	for _, tc := range browserUAs {
		if got := Classify(tc.ua); got.Bot() || got.Name != "" {
			t.Errorf("Classify(%q) = %+v, want a person", tc.ua, got)
		}
	}
}

func TestBotRules(t *testing.T) {
	cats := map[Category]bool{}
	for _, c := range BotCategories {
		cats[c] = true
	}
	for i, r := range botRules {
		if r.token != strings.ToLower(r.token) || r.token == "" {
			t.Errorf("rule %d: token %q is not lowercase", i, r.token)
		}
		if !cats[r.cat] || r.cat == Scanner {
			t.Errorf("rule %q: category %q", r.token, r.cat)
		}
		if r.name == "" || len(r.name) > maxBotName {
			t.Errorf("rule %q: name %q", r.token, r.name)
		}
		// A rule must win for its own token: an earlier, shorter token
		// that also matches would shadow it.
		ua := "Mozilla/5.0 (compatible; " + r.token + "/1.0)"
		if got := Classify(ua); got.Name != r.name || got.Category != r.cat {
			t.Errorf("rule %q is shadowed: %q classifies as %+v", r.token, ua, got)
		}
	}
	for _, s := range []string{"x", "<b>", strings.Repeat("a", 100), "--", "Foo Bar/1"} {
		if n := cleanBotName(s); len(n) > maxBotName || strings.ContainsAny(n, "<> /") {
			t.Errorf("cleanBotName(%q) = %q", s, n)
		}
	}
}

func TestClassifyRequest(t *testing.T) {
	for _, tc := range []struct {
		name, header, value string
		want                Agent
	}{
		{"browser", "", "", Agent{}},
		{"headless client hint", "Sec-Ch-Ua", `"Chromium";v="141", "HeadlessChrome";v="141"`, Agent{Other, "HeadlessChrome"}},
		{"real client hint", "Sec-Ch-Ua", `"Chromium";v="141", "Google Chrome";v="141"`, Agent{}},
		{"from address", "From", "crawler@example.com", Agent{Other, UnknownBot}},
		{"from obfuscated", "From", "googlebot(at)googlebot.com", Agent{Other, UnknownBot}},
	} {
		r := httptest.NewRequest("GET", "https://example.com/", nil)
		r.Header.Set("User-Agent", chromeUA)
		if tc.header != "" {
			r.Header.Set(tc.header, tc.value)
		}
		if got := ClassifyRequest(r); got != tc.want {
			t.Errorf("%s: %+v, want %+v", tc.name, got, tc.want)
		}
	}
	r := httptest.NewRequest("GET", "https://example.com/", nil)
	r.Header.Set("User-Agent", "curl/8.7.1")
	r.Header.Set("Sec-Ch-Ua", "HeadlessChrome")
	if got := ClassifyRequest(r); got != (Agent{Other, "curl"}) {
		t.Errorf("the User-Agent must win: %+v", got)
	}
}

func TestScannerPath(t *testing.T) {
	scans := []string{
		"/wp-login.php", "/wp-admin", "/wp-admin/", "/wp-admin/install.php", "/WP-LOGIN.PHP",
		"/wp-includes/wlwmanifest.xml", "/wp-content/plugins/revslider/temp.php", "/wp-json/wp/v2/users",
		"/blog/wp-admin/setup-config.php", "/wordpress/wp-admin/", "/xmlrpc.php", "/index.php", "/info.php",
		"/.env", "/.env.production", "/api/.env", "/.git/config", "/.git/HEAD", "/app/.git/config", "/.svn/entries",
		"/.DS_Store", "/.aws/credentials", "/.htaccess", "/phpmyadmin/", "/phpMyAdmin/index.php", "/pma/",
		"/cgi-bin/luci", "/vendor/phpunit/phpunit/src/Util/PHP/eval-stdin.php", "/lib/phpunit/x",
		"/.well-known/../../etc/passwd", "/a/../b", "/backup.zip", "/site.tar.gz", "/db.sql", "/config.php.bak",
		"/.well-known/shell.php", "/.well-known/something", "/actuator/health", "/server-status", "/owa/auth/logon.aspx",
		"/default.asp", "/login.jsp", "/telescope/requests", "/_ignition/execute-solution", "/docker-compose.yml",
		"/config.json", "/sftp-config.json", "/web.config", "/id_rsa.pem",
	}
	visits := []string{
		"/", "/blog", "/blog/hello-world", "/about", "/es/blog/hola", "/search", "/feed.xml", "/sitemap.xml",
		"/robots.txt", "/llms.txt", "/telescope-review", "/shellfish", "/vendors", "/environment", "/git-tips",
		"/uploads/2026/10/backup.zip", "/uploads/notes.php", "/static/admin.min.js", "/wp-content/uploads/2019/05/photo.jpg",
		"/.well-known/acme-challenge/abc", "/.well-known/security.txt", "/.well-known/webauthn",
		"/.well-known/change-password", "/.well-known/pki-validation/x.txt", "/category/php", "/tag/wordpress",
		"/blog/env", "/archive/2026/10",
	}
	for _, p := range scans {
		if !ScannerPath(p) {
			t.Errorf("ScannerPath(%q) = false", p)
		}
	}
	for _, p := range visits {
		if ScannerPath(p) {
			t.Errorf("ScannerPath(%q) = true", p)
		}
	}
}

func TestRecordRequest(t *testing.T) {
	db := newFakeDB()
	clk := &clock{t: time.Date(2026, 10, 10, 9, 0, 0, 0, madrid)}
	c := newTestCollector(db, clk, Options{MaxBotNames: 3})
	req := func(method, path, ua string) {
		r := httptest.NewRequest(method, "https://example.com"+path, nil)
		r.Header.Set("User-Agent", ua)
		c.RecordRequest(r)
		c.Record(r, path, "es") // never counts any of these as a view
	}
	gpt, google := uaOf(t, "GPTBot"), uaOf(t, "Googlebot")
	req("GET", "/", gpt)
	req("GET", "/blog/hello", gpt)
	req("HEAD", "/", gpt)
	req("GET", "/robots.txt", google)
	req("GET", "/wp-login.php", chromeUA) // a scanner with a browser's User-Agent
	req("POST", "/xmlrpc.php", "")        // a scanner, whatever its client
	req("GET", "/", "curl/8.7.1")
	req("GET", "/", "Wget/1.24")
	req("GET", "/", "Scrapy/2.11") // beyond MaxBotNames
	req("GET", "/", "")

	// People are left to Record.
	r := pageReq("203.0.113.1", chromeUA)
	c.RecordRequest(r)
	c.Record(r, "/", "es")

	c.SetEnabled(false)
	req("GET", "/", gpt)
	c.SetEnabled(true)

	if err := c.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	d := "2026-10-10"
	if got := db.day(d); got != [2]int64{1, 1} {
		t.Errorf("day = %v, want one view of one visitor", got)
	}
	for _, tc := range []struct {
		cat  Category
		name string
		want int64
	}{
		{AI, "GPTBot", 3}, {Search, "Googlebot", 1}, {Scanner, "", 2}, {Other, "curl", 1},
		{Other, "Wget", 0}, {Other, "(other)", 3}, {Other, NoUA, 0}, // only 3 names a day
	} {
		if got := db.bot(d, tc.cat, tc.name); got != tc.want {
			t.Errorf("%s %q = %d, want %d", tc.cat, tc.name, got, tc.want)
		}
	}
	var total int64
	for _, n := range db.bots {
		total += n
	}
	if total != 10 {
		t.Errorf("%d bot hits, want 10 (%v)", total, db.bots)
	}
}

func TestBotsDatabase(t *testing.T) {
	conn := testDB(t)
	ctx := context.Background()
	clk := &clock{t: time.Date(2026, 10, 10, 9, 0, 0, 0, madrid)}
	c := newTestCollector(conn, clk, Options{})
	hit := func(path, ua string, n int) {
		for range n {
			r := httptest.NewRequest("GET", "https://example.com"+path, nil)
			r.Header.Set("User-Agent", ua)
			c.RecordRequest(r)
		}
	}
	gpt, claude, google := uaOf(t, "GPTBot"), uaOf(t, "ClaudeBot"), uaOf(t, "Googlebot")
	hit("/", gpt, 5)
	hit("/", claude, 2)
	hit("/", google, 3)
	hit("/wp-login.php", chromeUA, 4)
	hit("/", "curl/8.7.1", 1)
	c.Record(pageReq("203.0.113.1", chromeUA), "/", "es")
	if err := c.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	clk.Set(time.Date(2026, 10, 11, 9, 0, 0, 0, madrid))
	hit("/", gpt, 1)
	hit("/", "facebookexternalhit/1.1", 2)
	if err := c.Close(ctx); err != nil {
		t.Fatal(err)
	}

	s, err := Report(ctx, conn, time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC), 3)
	if err != nil {
		t.Fatal(err)
	}
	if s.Totals != (Totals{1, 1}) {
		t.Errorf("human totals %+v, want one view and visitor", s.Totals)
	}
	if s.Bots.Hits != 18 {
		t.Errorf("bot hits %d, want 18", s.Bots.Hits)
	}
	if got, want := fmt.Sprint(s.Bots.Categories), "[{search 3 16.7} {ai 8 44.4} {social 2 11.1} {seo 0 0} {other 1 5.6} {scanner 4 22.2}]"; got != want {
		t.Errorf("categories = %s, want %s", got, want)
	}
	if got, want := fmt.Sprint(s.Bots.Top), "[{GPTBot ai 6 33.3} {Googlebot search 3 16.7} {ClaudeBot ai 2 11.1}]"; got != want {
		t.Errorf("top = %s, want %s", got, want)
	}

	// Pruning removes the bot rows of the 10th: GPTBot, ClaudeBot,
	// Googlebot, curl and the scanners, besides the day row, a page and
	// four dimensions of the one visit.
	n, err := Prune(ctx, conn, time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	var left int64
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM analytics_bots`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 2 || n < 5 {
		t.Errorf("pruned %d rows, %d bot rows left; want 2 left", n, left)
	}
}
