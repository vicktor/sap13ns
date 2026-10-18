package analytics

import (
	"strings"
	"testing"
)

// Real browser User-Agents and their expected classification.
var browserUAs = []struct {
	ua, browser, os, device string
}{
	{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36",
		browserChrome, osWindows, deviceDesktop},
	{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36",
		browserChrome, osMac, deviceDesktop},
	{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/26.0 Safari/605.1.15",
		browserSafari, osMac, deviceDesktop},
	{"Mozilla/5.0 (iPhone; CPU iPhone OS 18_6 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.6 Mobile/15E148 Safari/604.1",
		browserSafari, osIOS, deviceMobile},
	{"Mozilla/5.0 (iPad; CPU OS 18_6 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.6 Mobile/15E148 Safari/604.1",
		browserSafari, osIOS, deviceTablet},
	{"Mozilla/5.0 (iPhone; CPU iPhone OS 18_6 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/141.0.7390.41 Mobile/15E148 Safari/604.1",
		browserChrome, osIOS, deviceMobile},
	{"Mozilla/5.0 (iPhone; CPU iPhone OS 18_6 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) FxiOS/143.0 Mobile/15E148 Safari/605.1.15",
		browserFirefox, osIOS, deviceMobile},
	{"Mozilla/5.0 (X11; Linux x86_64; rv:143.0) Gecko/20100101 Firefox/143.0",
		browserFirefox, osLinux, deviceDesktop},
	{"Mozilla/5.0 (X11; Ubuntu; Linux x86_64; rv:143.0) Gecko/20100101 Firefox/143.0",
		browserFirefox, osLinux, deviceDesktop},
	{"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:143.0) Gecko/20100101 Firefox/143.0",
		browserFirefox, osWindows, deviceDesktop},
	{"Mozilla/5.0 (Android 15; Mobile; rv:143.0) Gecko/143.0 Firefox/143.0",
		browserFirefox, osAndroid, deviceMobile},
	{"Mozilla/5.0 (Android 15; Tablet; rv:143.0) Gecko/143.0 Firefox/143.0",
		browserFirefox, osAndroid, deviceTablet},
	{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36 Edg/141.0.0.0",
		browserEdge, osWindows, deviceDesktop},
	{"Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Mobile Safari/537.36 EdgA/141.0.0.0",
		browserEdge, osAndroid, deviceMobile},
	{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36 OPR/124.0.0.0",
		browserOpera, osWindows, deviceDesktop},
	{"Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Mobile Safari/537.36 OPR/92.0.0.0",
		browserOpera, osAndroid, deviceMobile},
	{"Mozilla/5.0 (Linux; Android 15; SAMSUNG SM-S931B) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/28.0 Chrome/130.0.0.0 Mobile Safari/537.36",
		browserSamsung, osAndroid, deviceMobile},
	{"Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Mobile Safari/537.36",
		browserChrome, osAndroid, deviceMobile},
	{"Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36",
		browserChrome, osAndroid, deviceTablet},
	{"Mozilla/5.0 (X11; CrOS x86_64 14541.0.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36",
		browserChrome, osChromeOS, deviceDesktop},
	{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36 Vivaldi/7.6",
		other, osWindows, deviceDesktop},
	{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/138.0.0.0 YaBrowser/25.8.0.0 Safari/537.36",
		other, osWindows, deviceDesktop},
	{"Mozilla/5.0 (iPhone; CPU iPhone OS 18_6 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148 [FBAN/FBIOS;FBAV/520.0.0.38.101;FBBV/123;FBDV/iPhone16,2;FBMD/iPhone;FBSN/iOS;FBSV/18.6;FBSS/3;FBCR/;FBID/phone;FBLC/es_ES;FBOP/80]",
		other, osIOS, deviceMobile},
	{"Mozilla/5.0 (Linux; Android 14; Pixel 8 Build/AP2A.240805.005; wv) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/141.0.0.0 Mobile Safari/537.36",
		other, osAndroid, deviceMobile},
	{"Mozilla/5.0 (iPhone; CPU iPhone OS 18_6 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) GSA/380.0.0 Mobile/15E148 Safari/604.1",
		other, osIOS, deviceMobile},
	{"Mozilla/5.0 (Windows NT 10.0; WOW64; Trident/7.0; rv:11.0) like Gecko",
		other, osWindows, deviceDesktop},
	{"Mozilla/5.0 (Linux; Android 11; CUBOT P50) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Mobile Safari/537.36",
		browserChrome, osAndroid, deviceMobile},
	{"Mozilla/5.0 (Linux; Android 9; KFTRWI) AppleWebKit/537.36 (KHTML, like Gecko) Silk/141.1.1 like Chrome/141.0.0.0 Safari/537.36",
		other, osAndroid, deviceTablet},
	{"Opera/9.80 (J2ME/MIDP; Opera Mini/9.80 (S60; SymbOS; Opera Mobi/23.348; U; en) Presto/2.5.25 Version/10.54",
		browserOpera, other, deviceMobile},
}

func TestParseUA(t *testing.T) {
	for _, tc := range browserUAs {
		b, o, d := parseUA(strings.ToLower(tc.ua))
		if b != tc.browser || o != tc.os || d != tc.device {
			t.Errorf("parseUA(%q)\n got  %q, %q, %q\n want %q, %q, %q", tc.ua, b, o, d, tc.browser, tc.os, tc.device)
		}
	}
}

func TestBrowsersAreNotBots(t *testing.T) {
	for _, tc := range browserUAs {
		if IsBot(tc.ua) {
			t.Errorf("IsBot(%q) = true", tc.ua)
		}
	}
}

func TestIsBot(t *testing.T) {
	bots := []string{
		"",
		"   ",
		"Mozilla/5.0",
		"Mozilla/5.0 (compatible)",
		// Search engines.
		"Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)",
		"Mozilla/5.0 (Linux; Android 6.0.1; Nexus 5X Build/MMB29P) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.7390.54 Mobile Safari/537.36 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)",
		"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; bingbot/2.0; +http://www.bing.com/bingbot.htm) Chrome/116.0.1938.76 Safari/537.36",
		"Mozilla/5.0 (compatible; YandexBot/3.0; +http://yandex.com/bots)",
		"Mozilla/5.0 (compatible; Baiduspider/2.0; +http://www.baidu.com/search/spider.html)",
		"DuckDuckBot/1.1; (+http://duckduckgo.com/duckduckbot.html)",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_5) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/13.1.1 Safari/605.1.15 (Applebot/0.1; +http://www.apple.com/go/applebot)",
		"Mozilla/5.0 (Linux; Android 7.0;) AppleWebKit/537.36 (KHTML, like Gecko) Mobile Safari/537.36 (compatible; PetalBot;+https://webmaster.petalsearch.com/site/petalbot)",
		"Mozilla/5.0 (compatible; Google-InspectionTool/1.0;)",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36 GoogleOther",
		"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/83.0.4103.97 Safari/537.36 Google-Read-Aloud",
		"FeedFetcher-Google; (+http://www.google.com/feedfetcher.html)",
		// AI crawlers and agents.
		"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko); compatible; GPTBot/1.2; +https://openai.com/gptbot",
		"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko); compatible; ChatGPT-User/1.0; +https://openai.com/bot",
		"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko); compatible; OAI-SearchBot/1.0; +https://openai.com/searchbot",
		"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; ClaudeBot/1.0; +claudebot@anthropic.com)",
		"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; Claude-User/1.0; +Claude-User@anthropic.com)",
		"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; Claude-SearchBot/1.0; +Claude-SearchBot@anthropic.com)",
		"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; PerplexityBot/1.0; +https://perplexity.ai/perplexitybot)",
		"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; Perplexity-User/1.0; +https://perplexity.ai/perplexity-user)",
		"CCBot/2.0 (https://commoncrawl.org/faq/)",
		"Mozilla/5.0 (Linux; Android 5.0) AppleWebKit/537.36 (KHTML, like Gecko) Mobile Safari/537.36 (compatible; Bytespider; spider-feedback@bytedance.com)",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.6 Safari/605.1.15 (Amazonbot/0.1; +https://developer.amazon.com/support/amazonbot)",
		"meta-externalagent/1.1 (+https://developers.facebook.com/docs/sharing/webmasters/crawler)",
		"Mozilla/5.0 (compatible; Diffbot/0.1; +http://www.diffbot.com)",
		"Mozilla/5.0 (compatible; YouBot (+http://www.you.com))",
		"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; DuckAssistBot/1.2; +http://duckduckgo.com/duckassistbot.html)",
		"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; MistralAI-User/1.0; +https://docs.mistral.ai/robots)",
		"Mozilla/5.0 (compatible) AI2Bot (+https://www.allenai.org/crawler)",
		"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.114 Safari/537.36 img2dataset",
		"cohere-ai",
		"Mozilla/5.0 (compatible; ImagesiftBot; +imagesift.com)",
		"Mozilla/5.0 (compatible; Timpibot/0.8; +http://www.timpi.io)",
		// SEO tools.
		"Mozilla/5.0 (compatible; AhrefsBot/7.0; +http://ahrefs.com/robot/)",
		"Mozilla/5.0 (compatible; SemrushBot/7~bl; +http://www.semrush.com/bot.html)",
		"Mozilla/5.0 (compatible; MJ12bot/v1.4.8; http://mj12bot.com/)",
		// Link previews.
		"facebookexternalhit/1.1 (+http://www.facebook.com/externalhit_uatext.php)",
		"Twitterbot/1.0",
		"LinkedInBot/1.0 (compatible; Mozilla/5.0; Apache-HttpClient +http://www.linkedin.com)",
		"Slackbot-LinkExpanding 1.0 (+https://api.slack.com/robots)",
		"Mozilla/5.0 (compatible; Discordbot/2.0; +https://discordapp.com)",
		"TelegramBot (like TwitterBot)",
		"WhatsApp/2.23.20.0",
		"Mastodon/4.4.0 (http.rb/5.2.0; +https://mastodon.social/)",
		"Mozilla/5.0 (compatible; Bluesky Cardyb/1.1; +mailto:support@bsky.app)",
		"Iframely/1.3.1 (+https://iframely.com/docs/about)",
		"Mozilla/5.0 (Windows NT 6.1; WOW64) SkypeUriPreview Preview/0.5 skype-url-preview@microsoft.com",
		// Monitoring and performance.
		"Mozilla/5.0+(compatible; UptimeRobot/2.0; http://www.uptimerobot.com/)",
		"Pingdom.com_bot_version_1.4_(http://www.pingdom.com/)",
		"Mozilla/5.0 (Linux; Android 11; moto g power (2022)) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Mobile Safari/537.36 Chrome-Lighthouse",
		"Uptime-Kuma/2.0.0",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36 PTST/250101.123456",
		// Headless and tools.
		"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/141.0.0.0 Safari/537.36",
		"Mozilla/5.0 (Unknown; Linux x86_64) AppleWebKit/538.1 (KHTML, like Gecko) PhantomJS/2.1.1 Safari/538.1",
		"curl/8.7.1",
		"Wget/1.24.5",
		"python-requests/2.32.3",
		"Python-urllib/3.12",
		"Go-http-client/2.0",
		"okhttp/4.12.0",
		"axios/1.7.7",
		"node-fetch/1.0 (+https://github.com/bitinn/node-fetch)",
		"Java/21.0.4",
		"Apache-HttpClient/4.5.14 (Java/17.0.12)",
		"Scrapy/2.11.2 (+https://scrapy.org)",
		"Feedly/1.0 (+http://www.feedly.com/fetcher.html; 3 subscribers)",
		"WordPress/6.6.2; https://example.com",
	}
	for _, ua := range bots {
		if !IsBot(ua) {
			t.Errorf("IsBot(%q) = false", ua)
		}
	}
}

func BenchmarkIsBot(b *testing.B) {
	ua := browserUAs[0].ua
	for b.Loop() {
		IsBot(ua)
	}
}

func TestMatcherAgreesWithContains(t *testing.T) {
	naive := func(words []string, s string) bool {
		s = strings.ToLower(s)
		for _, w := range words {
			if strings.Contains(s, w) {
				return true
			}
		}
		return false
	}
	// Overlapping words exercise the failure links.
	words := []string{"abcd", "bc", "bcx", "cdef", "aab", "x/y"}
	m := newMatcher(words)
	for _, s := range []string{"", "a", "abc", "ABCD", "zabcz", "aabcd", "abcdef", "acdef", "abx/yz", "x/", "bbc", "aaab", "zzz"} {
		if got, want := m.match(s), naive(words, s); got != want {
			t.Errorf("match(%q) = %v, want %v", s, got, want)
		}
	}
	var inputs []string
	for _, tc := range browserUAs {
		inputs = append(inputs, tc.ua)
	}
	for _, w := range botWords {
		inputs = append(inputs, "Mozilla/5.0 (X11) "+strings.ToUpper(w)+" tail", w[:len(w)-1])
	}
	for _, s := range inputs {
		if got, want := botMatcher.match(s), naive(botWords, s); got != want {
			t.Errorf("botMatcher.match(%q) = %v, want %v", s, got, want)
		}
	}
}
