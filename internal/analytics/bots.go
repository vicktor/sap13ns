package analytics

import (
	"net/http"
	"strings"
)

// Category is the kind of automated client a request comes from. The
// zero value, Human, is a person using a browser.
type Category string

// Categories of automated traffic, stored in analytics_bots.category.
const (
	Human   Category = ""
	Search  Category = "search"  // search engine crawlers
	AI      Category = "ai"      // AI crawlers, training scrapers and assistants fetching for a user
	Social  Category = "social"  // link previews of social networks and chat apps
	SEO     Category = "seo"     // SEO crawlers, uptime monitors and performance tools
	Other   Category = "other"   // any other bot, HTTP library or headless browser
	Scanner Category = "scanner" // requests probing for vulnerable software (ScannerPath)
)

// BotCategories lists the categories of automated traffic in display order.
var BotCategories = []Category{Search, AI, Social, SEO, Other, Scanner}

// Agent is the classification of a request's client.
type Agent struct {
	Category Category
	// Name is the bot's name for display: a fixed one of botRules
	// ("GPTBot"), or for an unlisted bot its product token ("curl") cut to
	// maxBotName characters of [A-Za-z0-9._-]; NoUA for an empty
	// User-Agent and UnknownBot when no name can be told. "" for humans
	// and scanners.
	Name string
}

// Bot reports whether the agent is automated.
func (a Agent) Bot() bool { return a.Category != Human }

// Names of bots that have none of their own.
const (
	NoUA       = "(none)"
	UnknownBot = "(unknown)"
	maxBotName = 40
)

// botRule maps a lowercase User-Agent substring to a named bot.
type botRule struct {
	token string
	name  string
	cat   Category
}

// botRules are tried in order; the first whose token occurs in the
// lowercased User-Agent wins. More specific tokens therefore come first:
// "applebot-extended" before "applebot", "telegrambot" before "twitterbot"
// (Telegram's UA says "like TwitterBot"), the AI agents of search companies
// before their search crawlers.
var botRules = []botRule{
	// AI crawlers, training scrapers and assistants.
	{"gptbot", "GPTBot", AI},
	{"chatgpt-user", "ChatGPT-User", AI},
	{"chatgpt-agent", "ChatGPT Agent", AI},
	{"oai-searchbot", "OAI-SearchBot", AI},
	{"claudebot", "ClaudeBot", AI},
	{"claude-user", "Claude-User", AI},
	{"claude-searchbot", "Claude-SearchBot", AI},
	{"claude-web", "Claude-Web", AI},
	{"anthropic-ai", "anthropic-ai", AI},
	{"perplexitybot", "PerplexityBot", AI},
	{"perplexity-user", "Perplexity-User", AI},
	{"google-extended", "Google-Extended", AI},
	{"google-cloudvertexbot", "Google-CloudVertexBot", AI},
	{"gemini-deep-research", "Gemini-Deep-Research", AI},
	{"applebot-extended", "Applebot-Extended", AI},
	{"ccbot", "CCBot", AI},
	{"bytespider", "Bytespider", AI},
	{"amazonbot", "Amazonbot", AI},
	{"meta-externalagent", "Meta-ExternalAgent", AI},
	{"meta-externalfetcher", "Meta-ExternalFetcher", AI},
	{"facebookbot", "FacebookBot", AI},
	{"cohere-training-data-crawler", "cohere-training-data-crawler", AI},
	{"cohere-ai", "cohere-ai", AI},
	{"diffbot", "Diffbot", AI},
	{"youbot", "YouBot", AI},
	{"mistralai-user", "MistralAI-User", AI},
	{"deepseekbot", "DeepSeekBot", AI},
	{"timpibot", "Timpibot", AI},
	{"imagesiftbot", "ImagesiftBot", AI},
	{"ai2bot", "AI2Bot", AI},
	{"duckassistbot", "DuckAssistBot", AI},
	{"pangubot", "PanguBot", AI},
	{"kangaroo bot", "Kangaroo Bot", AI},
	{"img2dataset", "img2dataset", AI},
	{"omgili", "omgili", AI},
	{"webzio", "Webzio", AI},
	{"firecrawl", "Firecrawl", AI},
	{"crawl4ai", "Crawl4AI", AI},
	{"iaskspider", "iAskSpider", AI},
	{"novaact", "NovaAct", AI},
	{"cotoyogi", "Cotoyogi", AI},

	// Search engines.
	{"googlebot", "Googlebot", Search},
	{"adsbot-google", "AdsBot-Google", Search},
	{"storebot-google", "Storebot-Google", Search},
	{"google-inspectiontool", "Google-InspectionTool", Search},
	{"googleother", "GoogleOther", Search},
	{"feedfetcher-google", "FeedFetcher-Google", Search},
	{"bingbot", "Bingbot", Search},
	{"bingpreview", "BingPreview", Search},
	{"msnbot", "msnbot", Search},
	{"adidxbot", "AdIdxBot", Search},
	{"duckduckbot", "DuckDuckBot", Search},
	{"yandex", "YandexBot", Search},
	{"baiduspider", "Baiduspider", Search},
	{"applebot", "Applebot", Search},
	{"qwant", "Qwantbot", Search},
	{"seznam", "SeznamBot", Search},
	{"yeti/", "Yeti (Naver)", Search},
	{"sogou", "Sogou", Search},
	{"ecosiabot", "EcosiaBot", Search},
	{"mojeek", "MojeekBot", Search},
	{"yahoo! slurp", "Yahoo! Slurp", Search},
	{"exabot", "Exabot", Search},
	{"coccocbot", "coccocbot", Search},
	{"daumoa", "Daum", Search},
	{"seekportbot", "SeekportBot", Search},
	{"bravebot", "Bravebot", Search},

	// Link previews of social networks and chat apps.
	{"facebookexternalhit", "facebookexternalhit", Social},
	{"facebookcatalog", "facebookcatalog", Social},
	{"telegrambot", "TelegramBot", Social},
	{"twitterbot", "Twitterbot", Social},
	{"linkedinbot", "LinkedInBot", Social},
	{"slackbot", "Slackbot", Social},
	{"slack-imgproxy", "Slackbot", Social},
	{"discordbot", "Discordbot", Social},
	{"whatsapp/", "WhatsApp", Social},
	{"pinterestbot", "Pinterestbot", Social},
	{"pinterest/0.", "Pinterestbot", Social},
	{"redditbot", "redditbot", Social},
	{"embedly", "Embedly", Social},
	{"iframely", "Iframely", Social},
	{"skypeuripreview", "Skype", Social},
	{"vkshare", "VKShare", Social},
	{"cardyb", "Bluesky", Social},
	{"mastodon", "Mastodon", Social},
	{"kakaotalk-scrap", "KakaoTalk", Social},
	{"flipboardproxy", "Flipboard", Social},

	// SEO crawlers, uptime monitors and performance tools.
	{"ahrefs", "AhrefsBot", SEO},
	{"semrush", "SemrushBot", SEO},
	{"mj12bot", "MJ12bot", SEO},
	{"dotbot", "DotBot", SEO},
	{"petalbot", "PetalBot", SEO},
	{"blexbot", "BLEXBot", SEO},
	{"dataforseobot", "DataForSeoBot", SEO},
	{"screaming frog", "Screaming Frog", SEO},
	{"sitebulb", "Sitebulb", SEO},
	{"serpstatbot", "SerpstatBot", SEO},
	{"barkrowler", "Barkrowler", SEO},
	{"rogerbot", "rogerbot (Moz)", SEO},
	{"seokicks", "SEOkicks", SEO},
	{"uptimerobot", "UptimeRobot", SEO},
	{"uptime-kuma", "Uptime Kuma", SEO},
	{"pingdom", "Pingdom", SEO},
	{"statuscake", "StatusCake", SEO},
	{"site24x7", "Site24x7", SEO},
	{"freshping", "Freshping", SEO},
	{"updown.io", "updown.io", SEO},
	{"ohdear", "Oh Dear", SEO},
	{"checkly", "Checkly", SEO},
	{"better uptime", "Better Stack", SEO},
	{"betterstack", "Better Stack", SEO},
	{"hetrixtools", "HetrixTools", SEO},
	{"chrome-lighthouse", "Lighthouse", SEO},
	{"gtmetrix", "GTmetrix", SEO},
	{"ptst/", "WebPageTest", SEO},
	{"datadog", "Datadog", SEO},
	{"newrelicpinger", "New Relic", SEO},
	{"w3c_validator", "W3C Validator", SEO},
	{"validator.nu", "W3C Validator", SEO},
}

// ruleMatcher catches the named bots whose User-Agent the generic test
// (isBotLower) would let through, so that Classify's fast path for
// browsers stays one pass over the string per matcher.
var ruleMatcher = func() *matcher {
	words := make([]string, len(botRules))
	for i, r := range botRules {
		words[i] = r.token
	}
	return newMatcher(words)
}()

// Classify returns the agent behind a User-Agent string.
func Classify(ua string) Agent {
	return classifyLower(strings.ToLower(strings.TrimSpace(ua)), ua)
}

func classifyLower(l, ua string) Agent {
	if l == "" {
		return Agent{Other, NoUA}
	}
	if !isBotLower(l) && !ruleMatcher.match(l) {
		return Agent{}
	}
	for _, r := range botRules {
		if strings.Contains(l, r.token) {
			return Agent{r.cat, r.name}
		}
	}
	return Agent{Other, botName(strings.TrimSpace(ua))}
}

// ClassifyRequest classifies the client of r by its User-Agent and, for a
// browser-like one, by headers browsers never send: a From address (the
// contact some crawlers give) or a Client Hint naming a headless browser.
func ClassifyRequest(r *http.Request) Agent {
	a := Classify(r.Header.Get("User-Agent"))
	if a.Bot() {
		return a
	}
	if strings.Contains(strings.ToLower(r.Header.Get("Sec-Ch-Ua")), "headless") {
		return Agent{Other, "HeadlessChrome"}
	}
	if f := r.Header.Get("From"); strings.Contains(f, "@") || strings.Contains(f, "(at)") {
		return Agent{Other, UnknownBot}
	}
	return a
}

// botName tells the name of an unlisted bot from its User-Agent: the
// product token of "curl/8.7.1" or "python-requests/2.32", the token after
// "compatible;" in "Mozilla/5.0 (compatible; FooBot/1.0; +https://...)",
// or a word containing bot, crawl or spider.
func botName(ua string) string {
	l := strings.ToLower(ua)
	switch {
	case strings.Contains(l, "headlesschrome"):
		return "HeadlessChrome"
	case strings.Contains(l, "phantomjs"):
		return "PhantomJS"
	}
	if !strings.HasPrefix(l, "mozilla/") && !strings.HasPrefix(l, "opera/") {
		if n := cleanBotName(firstToken(ua)); n != "" {
			return n
		}
	}
	if i := strings.Index(l, "compatible;"); i >= 0 {
		if n := cleanBotName(firstToken(strings.TrimLeft(ua[i+len("compatible;"):], " +"))); n != "" && !strings.EqualFold(n, "mozilla") {
			return n
		}
	}
	for _, f := range strings.FieldsFunc(ua, func(r rune) bool { return r == ' ' || r == ';' || r == '(' || r == ')' || r == ',' || r == '+' }) {
		lf := strings.ToLower(f)
		if strings.HasPrefix(lf, "http") || strings.Contains(lf, "@") {
			continue
		}
		if strings.Contains(lf, "bot") || strings.Contains(lf, "crawl") || strings.Contains(lf, "spider") {
			if n := cleanBotName(firstToken(f)); n != "" {
				return n
			}
		}
	}
	return UnknownBot
}

// firstToken returns s up to the first character that ends a product name.
func firstToken(s string) string {
	if i := strings.IndexAny(s, " /;()[],+"); i >= 0 {
		return s[:i]
	}
	return s
}

// cleanBotName keeps a name that is safe and short to store and show.
func cleanBotName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if b.Len() >= maxBotName {
			break
		}
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	n := strings.Trim(b.String(), ".-_")
	if len(n) < 2 {
		return ""
	}
	return n
}

// legitWellKnown are the /.well-known/ names real clients ask for.
var legitWellKnown = map[string]bool{
	"acme-challenge": true, "security.txt": true, "webauthn": true, "change-password": true,
	"assetlinks.json": true, "apple-app-site-association": true, "openid-configuration": true,
	"oauth-authorization-server": true, "oauth-protected-resource": true, "nodeinfo": true,
	"host-meta": true, "host-meta.json": true, "webfinger": true, "mta-sts.txt": true,
	"traffic-advice": true, "gpc.json": true, "dnt-policy.txt": true, "ai-plugin.json": true,
	"mcp": true, "mcp.json": true, "trust.txt": true, "humans.txt": true, "keybase.txt": true,
	"matrix": true, "atproto-did": true, "appspecific": true, "brave-rewards-verification.txt": true,
}

// scannerPrefixes start the paths of software this CMS does not run, the
// usual targets of vulnerability scanners. Lowercase, with a leading slash.
// One that does not end in a slash matches a whole segment or a file name
// ("/telescope/requests", "/xmlrpc.php"), never a longer word
// ("/telescope-review").
var scannerPrefixes = []string{
	"/wp-admin", "/wp-login", "/wp-includes", "/wp-content/plugins", "/wp-content/themes",
	"/wp-json", "/wp-config", "/wp-cron", "/wp-signup", "/wp-trackback", "/wp-mail",
	"/wordpress/", "/wp/", "/xmlrpc", "/cgi-bin", "/phpmyadmin", "/pma/", "/myadmin", "/mysqladmin",
	"/phpinfo", "/adminer", "/vendor/", "/actuator", "/server-status", "/server-info",
	"/solr/", "/owa/", "/ecp/", "/autodiscover/", "/remote/login", "/telescope", "/_ignition",
	"/console/", "/manager/html", "/hnap1", "/boaform", "/goform", "/setup.cgi",
	"/administrator/", "/user/register", "/joomla", "/magento", "/typo3", "/sites/default/",
	"/api/jsonws", "/jenkins", "/geoserver", "/druid/", "/nacos", "/containers/json",
	"/sftp-config.json", "/config.json", "/config.yml", "/docker-compose", "/_profiler",
	"/wlwmanifest.xml", "/elmah.axd", "/trace.axd", "/web.config", "/aws.yml", "/.env",
}

// scannerSegments are path segments (any depth) that only scanners ask for.
var scannerSegments = []string{
	".env", ".git", ".svn", ".hg", ".bzr", ".ds_store", ".aws", ".ssh", ".vscode", ".idea",
	".htaccess", ".htpasswd", ".npmrc", ".dockerenv", ".docker", ".kube", ".travis.yml",
	".gitlab-ci.yml", ".circleci", ".bash_history", ".mysql_history", "wp-config.php",
	"phpunit", "eval-stdin.php",
}

// scannerExts are file types this CMS never serves as pages.
var scannerExts = []string{
	".php", ".php3", ".php5", ".php7", ".phtml", ".asp", ".aspx", ".ashx", ".asmx", ".jsp",
	".jspa", ".do", ".action", ".cgi", ".pl", ".cfm", ".sql", ".bak", ".old", ".orig", ".swp",
	".save", ".ini", ".log", ".sh", ".env", ".yml", ".yaml", ".toml", ".pem", ".key",
}

// rootArchives are archive types scanners request at the site root
// (backup.zip, site.tar.gz); uploads under /uploads/ are not scanner paths.
var rootArchives = []string{".zip", ".tar", ".tar.gz", ".tgz", ".gz", ".rar", ".7z"}

// ScannerPath reports whether a request path probes for software the CMS
// does not run (WordPress, PHP, exposed secrets, admin panels of other
// products), so that the request is a vulnerability scan rather than a
// visit. Uploaded files and the /.well-known/ names real clients use are
// never scanner paths.
func ScannerPath(p string) bool {
	l := strings.ToLower(p)
	if l == "" || l[0] != '/' {
		l = "/" + l
	}
	if strings.Contains(l, "/../") || strings.HasSuffix(l, "/..") || strings.Contains(l, "%2e") || strings.Contains(l, "\x00") {
		return true
	}
	if strings.HasPrefix(l, "/uploads/") || strings.HasPrefix(l, "/static/") || strings.HasPrefix(l, "/themes/") ||
		strings.HasPrefix(l, "/plugin-assets/") || strings.HasPrefix(l, "/wp-content/uploads/") {
		return false
	}
	if rest, ok := strings.CutPrefix(l, "/.well-known/"); ok {
		name, _, _ := strings.Cut(rest, "/")
		return !legitWellKnown[name] && !strings.HasPrefix(name, "pki-validation")
	}
	for _, pre := range scannerPrefixes {
		if rest, ok := strings.CutPrefix(l, pre); ok && (rest == "" || strings.HasSuffix(pre, "/") || strings.ContainsRune("/._", rune(rest[0]))) {
			return true
		}
	}
	for _, seg := range strings.Split(l, "/") {
		if seg == "" {
			continue
		}
		for _, s := range scannerSegments {
			if seg == s || strings.HasPrefix(seg, s+".") || strings.HasPrefix(seg, s+"_") || strings.HasPrefix(seg, s+"-") {
				return true
			}
		}
	}
	last := l[strings.LastIndexByte(l, '/')+1:]
	for _, ext := range scannerExts {
		if strings.HasSuffix(last, ext) {
			return true
		}
	}
	if strings.Count(l, "/") == 1 {
		for _, ext := range rootArchives {
			if strings.HasSuffix(last, ext) {
				return true
			}
		}
	}
	return false
}
