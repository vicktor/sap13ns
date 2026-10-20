package analytics

import "strings"

// botWords are lowercase substrings of the User-Agents of crawlers, AI
// agents, link preview fetchers, uptime monitors, headless browsers and HTTP
// libraries. Generic words (bot, crawl, spider, ...) catch most of them; the
// named entries cover agents whose UA lacks those words.
var botWords = []string{
	// Generic.
	"bot", "crawl", "spider", "slurp", "scrap", "archiv", "fetch", "preview",
	"indexer", "indexing", "http://", "https://", "(compatible)",
	// Search engines without a generic word in their UA.
	"yandex", "baidu", "sogou", "yeti/", "daumoa", "seznam", "qwant", "mojeek",
	"-google", "google-", "googleother", "google favicon", "google web preview", "google page speed",
	// AI crawlers and agents.
	"gptbot", "chatgpt", "oai-searchbot", "claude", "anthropic", "perplexity", "ccbot",
	"bytespider", "amazonbot", "applebot", "cohere", "diffbot", "youbot", "timpibot",
	"imagesift", "omgili", "petalbot", "ai2bot", "duckassist", "mistralai", "img2dataset",
	"kangaroo", "pangubot", "gemini", "webzio", "velenpublic", "sidetrade", "iaskspider",
	"firecrawl", "crawl4ai", "apify", "novaact", "deepseek", "meta-external", "cotoyogi",
	// SEO tools and scanners.
	"semrush", "ahrefs", "majestic", "mj12", "screaming frog", "sitebulb", "netcraft",
	"censys", "shodan", "zgrab", "masscan", "nmap", "nuclei", "nikto", "sqlmap", "wpscan",
	"expanse", "leakix", "l9explore", "qualys", "researchscan", "internet-measurement",
	// Link previews and social fetchers.
	"facebookexternalhit", "facebookcatalog", "skypeuri", "embedly", "iframely", "vkshare",
	"mastodon", "pleroma", "akkoma", "misskey", "cardyb", "flipboardproxy", "outbrain",
	"whatsapp/", "kakaotalk-scrap",
	// Feed readers.
	"feedly", "feedbin", "inoreader", "newsblur", "miniflux", "freshrss", "tt-rss",
	"tiny tiny rss", "netnewswire", "rss reader", "rssreader",
	// Monitoring, performance and validation.
	"monitor", "uptime", "pingdom", "statuscake", "site24x7", "freshping", "updown", "ohdear",
	"checkly", "catchpoint", "thousandeyes", "newrelic", "datadog", "dynatrace", "nagios",
	"zabbix", "check_http", "healthcheck", "health check", "health-check", "kube-probe",
	"prometheus", "blackbox", "lighthouse", "pagespeed", "gtmetrix", "ptst/", "speedcurve",
	"validator", "linkcheck", "checklink",
	// Headless and automation.
	"headless", "phantomjs", "slimerjs", "puppeteer", "playwright", "selenium", "webdriver",
	"cypress",
	// HTTP libraries and command-line tools.
	"curl", "wget", "python", "libwww", "lwp-", "java/", "okhttp", "go-http", "axios",
	"undici", "aiohttp", "httpx", "guzzle", "postman", "insomnia", "httpie", "httrack",
	"nutch", "heritrix", "http-client", "httpclient", "http_client", "dalvik", "cfnetwork",
	"winhttp", "powershell", "node/", "deno/", "bun/", "ruby", "perl", "php/",
}

var botMatcher = newMatcher(botWords)

// IsBot reports whether a User-Agent belongs to a crawler, AI agent, link
// preview fetcher, monitor, headless browser or HTTP library rather than a
// person using a browser. An empty User-Agent counts as a bot. Classify
// also tells which kind of bot.
func IsBot(ua string) bool { return Classify(ua).Bot() }

func isBotLower(l string) bool {
	if len(l) < 20 || !strings.Contains(l, "(") {
		return true
	}
	// Every current browser sends "Mozilla/5.0 (...)"; only Presto-era Opera
	// starts with "Opera/".
	if !strings.HasPrefix(l, "mozilla/") && !strings.HasPrefix(l, "opera/") {
		return true
	}
	// "(compatible; Name/1.0; ...)" without MSIE is a bot convention.
	if strings.Contains(l, "compatible;") && !strings.Contains(l, "msie") && !strings.Contains(l, "trident") {
		return true
	}
	// CUBOT is an Android phone brand, not a bot.
	return botMatcher.match(strings.ReplaceAll(l, "cubot", ""))
}

// matcher finds any of a set of words in a string in one pass (an
// Aho-Corasick automaton compiled to a DFA over the bytes that occur in the
// words; ASCII letters match case-insensitively).
type matcher struct {
	class  [256]uint8 // byte -> column; 0 for bytes in no word
	ncols  int
	next   []uint16 // state*ncols + column -> state
	accept []bool
}

func newMatcher(words []string) *matcher {
	m := &matcher{ncols: 1}
	for _, w := range words {
		for i := 0; i < len(w); i++ {
			if b := w[i]; m.class[b] == 0 {
				m.class[b] = uint8(m.ncols)
				if 'a' <= b && b <= 'z' {
					m.class[b-'a'+'A'] = uint8(m.ncols)
				}
				m.ncols++
			}
		}
	}
	// Trie of the words.
	type node struct {
		child map[uint8]int
		fail  int
		end   bool
	}
	nodes := []node{{child: map[uint8]int{}}}
	for _, w := range words {
		s := 0
		for i := 0; i < len(w); i++ {
			c := m.class[w[i]]
			n, ok := nodes[s].child[c]
			if !ok {
				nodes = append(nodes, node{child: map[uint8]int{}})
				n = len(nodes) - 1
				nodes[s].child[c] = n
			}
			s = n
		}
		nodes[s].end = true
	}
	if len(nodes) > 1<<16 {
		panic("analytics: too many bot words")
	}
	// Breadth-first: fill each state's row from its failure state's row,
	// which is shallower and therefore already complete.
	m.next = make([]uint16, len(nodes)*m.ncols)
	m.accept = make([]bool, len(nodes))
	queue := []int{0}
	for len(queue) > 0 {
		s := queue[0]
		queue = queue[1:]
		m.accept[s] = nodes[s].end || m.accept[nodes[s].fail]
		for c := 0; c < m.ncols; c++ {
			n, ok := nodes[s].child[uint8(c)]
			switch {
			case ok && s == 0:
				nodes[n].fail = 0
			case ok:
				nodes[n].fail = int(m.next[nodes[s].fail*m.ncols+c])
			case s != 0:
				m.next[s*m.ncols+c] = m.next[nodes[s].fail*m.ncols+c]
				continue
			default:
				continue
			}
			m.next[s*m.ncols+c] = uint16(n)
			queue = append(queue, n)
		}
	}
	return m
}

// match reports whether s contains any of the words.
func (m *matcher) match(s string) bool {
	st := 0
	for i := 0; i < len(s); i++ {
		st = int(m.next[st*m.ncols+int(m.class[s[i]])])
		if m.accept[st] {
			return true
		}
	}
	return false
}

// Browser, OS and device names stored in analytics_dims.
const (
	browserChrome  = "Chrome"
	browserSafari  = "Safari"
	browserFirefox = "Firefox"
	browserEdge    = "Edge"
	browserOpera   = "Opera"
	browserSamsung = "Samsung Internet"

	osWindows  = "Windows"
	osMac      = "macOS"
	osIOS      = "iOS"
	osAndroid  = "Android"
	osLinux    = "Linux"
	osChromeOS = "ChromeOS"

	deviceMobile  = "mobile"
	deviceTablet  = "tablet"
	deviceDesktop = "desktop"

	other = "other"
)

// otherBrowsers are browsers and in-app web views that also carry a Chrome or
// Safari token but should not be counted as those browsers.
var otherBrowsers = []string{
	"vivaldi/", "yabrowser/", "ucbrowser/", "ucweb", "whale/", "duckduckgo/", "miuibrowser/",
	"huaweibrowser/", "heytapbrowser/", "silk/", "qqbrowser/", "baidubrowser", "puffin/",
	"fban/", "fbav/", "fb_iab", "instagram", "line/", "micromessenger/", "snapchat", "tiktok",
	"musical_ly", "bytedancewebview", "pinterest", "gsa/", "; wv)", "electron/",
}

// parseUA classifies a lowercased (non-bot) User-Agent into browser, OS
// and device.
func parseUA(l string) (browser, os, device string) {
	return parseBrowser(l), parseOS(l), parseDevice(l)
}

func parseBrowser(l string) string {
	switch {
	case strings.Contains(l, "samsungbrowser/"):
		return browserSamsung
	case strings.Contains(l, "edg/") || strings.Contains(l, "edga/") || strings.Contains(l, "edgios/") || strings.Contains(l, "edge/"):
		return browserEdge
	case strings.Contains(l, "opr/") || strings.Contains(l, "opios/") || strings.Contains(l, "opt/") ||
		strings.Contains(l, "opera"):
		return browserOpera
	}
	for _, s := range otherBrowsers {
		if strings.Contains(l, s) {
			return other
		}
	}
	switch {
	case strings.Contains(l, "firefox/") || strings.Contains(l, "fxios/"):
		return browserFirefox
	case strings.Contains(l, "crios/") || strings.Contains(l, "chrome/") || strings.Contains(l, "chromium/"):
		return browserChrome
	case strings.Contains(l, "safari/") && strings.Contains(l, "version/"):
		return browserSafari
	}
	return other
}

func parseOS(l string) string {
	switch {
	case strings.Contains(l, "windows phone"):
		return other
	case strings.Contains(l, "windows"):
		return osWindows
	case strings.Contains(l, "cros "):
		return osChromeOS
	case strings.Contains(l, "iphone") || strings.Contains(l, "ipad") || strings.Contains(l, "ipod"):
		return osIOS
	case strings.Contains(l, "android"):
		return osAndroid
	case strings.Contains(l, "macintosh") || strings.Contains(l, "mac os x"):
		return osMac
	case strings.Contains(l, "tizen") || strings.Contains(l, "kaios") || strings.Contains(l, "webos") ||
		strings.Contains(l, "harmonyos"):
		return other
	case strings.Contains(l, "linux") || strings.Contains(l, "x11; ubuntu") || strings.Contains(l, "x11; fedora"):
		return osLinux
	}
	return other
}

func parseDevice(l string) string {
	switch {
	case strings.Contains(l, "ipad") || strings.Contains(l, "tablet") || strings.Contains(l, "kindle") ||
		strings.Contains(l, "silk/") || strings.Contains(l, "playbook"):
		return deviceTablet
	case strings.Contains(l, "mobi") || strings.Contains(l, "iphone") || strings.Contains(l, "ipod") ||
		strings.Contains(l, "windows phone") || strings.Contains(l, "opera mini"):
		return deviceMobile
	case strings.Contains(l, "android"):
		// Android phones say "Mobile"; tablets do not.
		return deviceTablet
	}
	return deviceDesktop
}
