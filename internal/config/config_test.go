package config

import (
	"log/slog"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// clearEnv unsets every SAP13NS_ variable for the test, so the developer's
// environment cannot leak into Load.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "SAP13NS_") {
			t.Setenv(k, "")
			os.Unsetenv(k)
		}
	}
}

func setEnv(t *testing.T, vars map[string]string) {
	t.Helper()
	for k, v := range vars {
		t.Setenv("SAP13NS_"+k, v)
	}
}

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// singleSiteEnv sets every variable Load read before multisite existed.
func singleSiteEnv(t *testing.T, dir string) {
	setEnv(t, map[string]string{
		"DATABASE_URL":    "postgres:///sap13ns?host=/tmp",
		"LISTEN":          "unix:/run/sap13ns/sap13ns.sock",
		"SOCKET_GROUP":    "sap13ns-web",
		"BASE_URL":        "https://example.com/",
		"SECRET_KEY":      strings.Repeat("ab", 32),
		"DATA_DIR":        dir,
		"UPLOADS_DIR":     "/srv/uploads",
		"TRUSTED_PROXIES": "10.0.0.0/8, ::1/128",
		"MAX_UPLOAD_MB":   "32",
		"DB_MAX_CONNS":    "8",
		"SERVE_UPLOADS":   "false",
		"DEV":             "true",
		"LOG_LEVEL":       "debug",
		"LOG_FORMAT":      "text",
		"SMTP_HOST":       "smtp.example.com",
		"SMTP_PORT":       "465",
		"SMTP_USERNAME":   "mailer",
		"SMTP_PASSWORD":   "secret",
		"SMTP_FROM":       "site@example.com",
	})
}

// TestLoadSingleSiteGolden pins the configuration of a single-site install:
// without SAP13NS_MULTISITE, Load returns exactly what it returned before
// multisite existed, and every multisite field stays zero even when the
// environment sets its variables.
func TestLoadSingleSiteGolden(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		clearEnv(t)
		dir := t.TempDir()
		setEnv(t, map[string]string{"DATABASE_URL": "postgres:///x", "DATA_DIR": dir})
		got, err := Load(false)
		if err != nil {
			t.Fatal(err)
		}
		want := &Config{
			DatabaseURL:    "postgres:///x",
			Listen:         "127.0.0.1:8080",
			BaseURL:        mustURL(t, "http://localhost:8080"),
			DataDir:        dir,
			UploadsDir:     filepath.Join(dir, "uploads"),
			PluginsDir:     filepath.Join(dir, "plugins"),
			ThemesDir:      filepath.Join(dir, "themes"),
			TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("::1/128")},
			MaxUploadBytes: 16 << 20,
			ServeUploads:   true,
			LogLevel:       slog.LevelInfo,
			LogFormat:      "json",
			SMTP:           SMTP{Port: 587},
			DBMaxConns:     16,
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Load() =\n%#v\nwant\n%#v", got, want)
		}
	})

	full := func(t *testing.T, dir string) *Config {
		return &Config{
			DatabaseURL:    "postgres:///sap13ns?host=/tmp",
			Listen:         "unix:/run/sap13ns/sap13ns.sock",
			SocketGroup:    "sap13ns-web",
			BaseURL:        mustURL(t, "https://example.com"),
			SecretKey:      []byte(strings.Repeat("\xab", 32)),
			DataDir:        dir,
			UploadsDir:     "/srv/uploads",
			PluginsDir:     filepath.Join(dir, "plugins"),
			ThemesDir:      filepath.Join(dir, "themes"),
			TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("::1/128")},
			MaxUploadBytes: 32 << 20,
			Dev:            true,
			CookieSecure:   true,
			HSTS:           true,
			LogLevel:       slog.LevelDebug,
			LogFormat:      "text",
			SMTP:           SMTP{Host: "smtp.example.com", Port: 465, Username: "mailer", Password: "secret", From: "site@example.com"},
			DBMaxConns:     8,
		}
	}
	t.Run("every variable", func(t *testing.T) {
		clearEnv(t)
		dir := t.TempDir()
		singleSiteEnv(t, dir)
		got, err := Load(true)
		if err != nil {
			t.Fatal(err)
		}
		if want := full(t, dir); !reflect.DeepEqual(got, want) {
			t.Fatalf("Load() =\n%#v\nwant\n%#v", got, want)
		}
	})
	for _, multisite := range []string{"", "false", "0"} {
		t.Run("multisite variables ignored, MULTISITE="+multisite, func(t *testing.T) {
			clearEnv(t)
			dir := t.TempDir()
			singleSiteEnv(t, dir)
			setEnv(t, map[string]string{
				"MULTISITE":             multisite,
				"SITES_DOMAIN":          "sites.example.com",
				"UNKNOWN_HOST":          "nonsense",
				"MAX_LOADED_SITES":      "-1",
				"SITE_IDLE":             "soon",
				"MEMORY_LIMIT_MB":       "1",
				"TLS":                   "autocert",
				"STRIPE_SECRET_KEY":     "sk_test_0123456789",
				"STRIPE_WEBHOOK_SECRET": "whsec_0123456789",
				"STRIPE_PRICE_PRO":      "price_0123456789",
			})
			got, err := Load(true)
			if err != nil {
				t.Fatal(err)
			}
			if want := full(t, dir); !reflect.DeepEqual(got, want) {
				t.Fatalf("Load() =\n%#v\nwant\n%#v", got, want)
			}
		})
	}
}

func multisiteEnv(t *testing.T) {
	clearEnv(t)
	setEnv(t, map[string]string{
		"DATABASE_URL": "postgres:///x",
		"DATA_DIR":     t.TempDir(),
		"BASE_URL":     "https://sap13ns.site",
		"MULTISITE":    "true",
	})
}

func TestLoadMultisite(t *testing.T) {
	multisiteEnv(t)
	c, err := Load(false)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Multisite || c.SitesDomain != "sap13ns.site" || c.UnknownHost != "404" || c.MaxLoadedSites != 50 ||
		c.SiteIdle != 15*time.Minute || c.MemoryLimitMB != 0 || c.TLS != "proxy" || c.DBMaxConns != 32 || c.Stripe.Enabled() {
		t.Fatalf("defaults: %+v", c)
	}

	setEnv(t, map[string]string{
		"SITES_DOMAIN":                  "Sites.Example.COM",
		"UNKNOWN_HOST":                  "redirect",
		"MAX_LOADED_SITES":              "200",
		"SITE_IDLE":                     "30m",
		"MEMORY_LIMIT_MB":               "2048",
		"TLS":                           "autocert",
		"DB_MAX_CONNS":                  "64",
		"STRIPE_SECRET_KEY":             "rk_live_0123456789abc",
		"STRIPE_WEBHOOK_SECRET":         "whsec_newnewnewnew, whsec_oldoldoldold",
		"STRIPE_PRICE_PRO":              "price_promonthly1",
		"STRIPE_PRICE_PRO_YEARLY":       "price_proyearly12",
		"STRIPE_PRICE_BUSINESS":         "price_business123",
		"STRIPE_PRICE_TEAM_PLUS":        "price_teamplus123",
		"STRIPE_AUTOMATIC_TAX":          "true",
		"STRIPE_TAX_ID_COLLECTION":      "false",
		"STRIPE_TRIAL_DAYS":             "14",
		"BILLING_GRACE_DAYS":            "7",
		"STRIPE_API_VERSION":            "2026-09-30.endive",
		"STRIPE_PRICE_BUSINESS_YEARLY":  "",
		"STRIPE_PRICE_IGNORED_IF_EMPTY": "",
	})
	c, err = Load(false)
	if err != nil {
		t.Fatal(err)
	}
	if c.SitesDomain != "sites.example.com" || c.UnknownHost != "redirect" || c.MaxLoadedSites != 200 ||
		c.SiteIdle != 30*time.Minute || c.MemoryLimitMB != 2048 || c.TLS != "autocert" || c.DBMaxConns != 64 {
		t.Fatalf("settings: %+v", c)
	}
	want := Stripe{
		SecretKey:      "rk_live_0123456789abc",
		WebhookSecrets: []string{"whsec_newnewnewnew", "whsec_oldoldoldold"},
		Prices: map[string]StripePrices{
			"pro":       {Monthly: "price_promonthly1", Yearly: "price_proyearly12"},
			"business":  {Monthly: "price_business123"},
			"team_plus": {Monthly: "price_teamplus123"},
		},
		APIVersion: "2026-09-30.endive", AutomaticTax: true, TaxIDCollection: false, BillingAddress: true, TrialDays: 14, GraceDays: 7,
	}
	if !reflect.DeepEqual(c.Stripe, want) {
		t.Fatalf("Stripe = %#v\nwant %#v", c.Stripe, want)
	}
	if s := c.Stripe.String(); strings.Contains(s, "rk_live") || strings.Contains(s, "whsec") {
		t.Fatalf("Stripe.String() leaks a secret: %s", s)
	}
}

func TestLoadMultisiteErrors(t *testing.T) {
	billing := map[string]string{
		"STRIPE_SECRET_KEY":     "sk_test_0123456789",
		"STRIPE_WEBHOOK_SECRET": "whsec_0123456789",
		"STRIPE_PRICE_PRO":      "price_0123456789",
		"STRIPE_PRICE_BUSINESS": "price_9876543210",
	}
	for _, tc := range []struct {
		name    string
		vars    map[string]string
		billing bool
		want    string
	}{
		{"bad multisite flag", map[string]string{"MULTISITE": "yes please"}, false, "SAP13NS_MULTISITE"},
		{"domain with port", map[string]string{"SITES_DOMAIN": "example.com:8080"}, false, "SAP13NS_SITES_DOMAIN"},
		{"domain is an IP", map[string]string{"SITES_DOMAIN": "192.0.2.1"}, false, "SAP13NS_SITES_DOMAIN"},
		{"domain with trailing dot", map[string]string{"SITES_DOMAIN": "example.com."}, false, "SAP13NS_SITES_DOMAIN"},
		{"domain not ASCII", map[string]string{"SITES_DOMAIN": "bücher.example"}, false, "SAP13NS_SITES_DOMAIN"},
		{"unknown host mode", map[string]string{"UNKNOWN_HOST": "302"}, false, "SAP13NS_UNKNOWN_HOST"},
		{"max loaded zero", map[string]string{"MAX_LOADED_SITES": "0"}, false, "SAP13NS_MAX_LOADED_SITES"},
		{"idle not a duration", map[string]string{"SITE_IDLE": "15"}, false, "SAP13NS_SITE_IDLE"},
		{"idle too short", map[string]string{"SITE_IDLE": "10s"}, false, "SAP13NS_SITE_IDLE"},
		{"memory limit too small", map[string]string{"MEMORY_LIMIT_MB": "16"}, false, "SAP13NS_MEMORY_LIMIT_MB"},
		{"tls mode", map[string]string{"TLS": "on"}, false, "SAP13NS_TLS"},
		{"autocert over http", map[string]string{"TLS": "autocert", "BASE_URL": "http://localhost:8137"}, false, "SAP13NS_TLS"},
		{"publishable key", map[string]string{"STRIPE_SECRET_KEY": "pk_live_0123456789"}, true, "SAP13NS_STRIPE_SECRET_KEY"},
		{"no webhook secret", map[string]string{"STRIPE_WEBHOOK_SECRET": " , "}, true, "SAP13NS_STRIPE_WEBHOOK_SECRET"},
		{"bad webhook secret", map[string]string{"STRIPE_WEBHOOK_SECRET": "whsec_0123456789,sk_test_x"}, true, "SAP13NS_STRIPE_WEBHOOK_SECRET"},
		{"missing required price", map[string]string{"STRIPE_PRICE_BUSINESS": ""}, true, "SAP13NS_STRIPE_PRICE_BUSINESS"},
		{"bad price", map[string]string{"STRIPE_PRICE_PRO": "prod_0123456789"}, true, "SAP13NS_STRIPE_PRICE_PRO"},
		{"yearly without monthly", map[string]string{"STRIPE_PRICE_EXTRA_YEARLY": "price_0123456789"}, true, "SAP13NS_STRIPE_PRICE_EXTRA"},
		{"bad plan name", map[string]string{"STRIPE_PRICE_9LIVES": "price_0123456789"}, true, "not a plan name"},
		{"bad tax flag", map[string]string{"STRIPE_AUTOMATIC_TAX": "maybe"}, true, "SAP13NS_STRIPE_AUTOMATIC_TAX"},
		{"trial too long", map[string]string{"STRIPE_TRIAL_DAYS": "1000"}, true, "SAP13NS_STRIPE_TRIAL_DAYS"},
		{"negative grace", map[string]string{"BILLING_GRACE_DAYS": "-1"}, true, "SAP13NS_BILLING_GRACE_DAYS"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			multisiteEnv(t)
			if tc.billing {
				setEnv(t, billing)
			}
			setEnv(t, tc.vars)
			_, err := Load(false)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load() error = %v, want one mentioning %s", err, tc.want)
			}
		})
	}
}

func networkConfig(t *testing.T, base string) *Config {
	t.Helper()
	multisiteEnv(t)
	setEnv(t, map[string]string{
		"BASE_URL":        base,
		"SITES_DOMAIN":    "sap13ns.site",
		"SECRET_KEY":      strings.Repeat("00", 32),
		"TRUSTED_PROXIES": "10.0.0.0/8",
	})
	setEnv(t, map[string]string{
		"STRIPE_SECRET_KEY":     "sk_test_0123456789",
		"STRIPE_WEBHOOK_SECRET": "whsec_0123456789",
		"STRIPE_PRICE_PRO":      "price_0123456789",
		"STRIPE_PRICE_BUSINESS": "price_9876543210",
	})
	c, err := Load(true)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestForSite(t *testing.T) {
	base := networkConfig(t, "https://sap13ns.site")
	before := *base
	before.TrustedProxies = append([]netip.Prefix(nil), base.TrustedProxies...)
	secret := []byte(strings.Repeat("s", 32))

	c, err := ForSite(base, SiteParams{ID: 7, Host: "blog.sap13ns.site", Secret: secret})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base.DataDir, "sites", "7")
	if c.BaseURL.String() != "https://blog.sap13ns.site" || c.DataDir != dir ||
		c.UploadsDir != filepath.Join(dir, "uploads") || c.PluginsDir != filepath.Join(dir, "plugins") ||
		c.ThemesDir != filepath.Join(dir, "themes") || string(c.SecretKey) != string(secret) ||
		c.NetworkThemesDir != base.ThemesDir || base.NetworkThemesDir != "" {
		t.Fatalf("site config: %+v", c)
	}
	if !c.CookieSecure || !c.HSTS || c.HSTSNoSubdomains || c.PasskeyRPID != "blog.sap13ns.site" ||
		!reflect.DeepEqual(c.PasskeyOrigins, []string{"https://blog.sap13ns.site"}) {
		t.Fatalf("security fields: secure=%v hsts=%v nosub=%v rp=%q origins=%v",
			c.CookieSecure, c.HSTS, c.HSTSNoSubdomains, c.PasskeyRPID, c.PasskeyOrigins)
	}
	if c.Stripe.Enabled() || c.Stripe.Prices != nil {
		t.Fatal("a site config must not carry the billing settings")
	}
	if !c.Multisite || c.DatabaseURL != base.DatabaseURL || c.SitesDomain != base.SitesDomain {
		t.Fatalf("shared settings not copied: %+v", c)
	}

	// Deep copy: no pointer, slice or map of the result is shared with base,
	// and changing the result leaves base as it was.
	bv, cv := reflect.ValueOf(base).Elem(), reflect.ValueOf(c).Elem()
	for i := range bv.NumField() {
		f := bv.Type().Field(i)
		switch f.Type.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Map:
			if !bv.Field(i).IsNil() && !cv.Field(i).IsNil() && bv.Field(i).Pointer() == cv.Field(i).Pointer() {
				t.Errorf("Config.%s is shared between the network and the site", f.Name)
			}
		}
	}
	c.TrustedProxies[0] = netip.MustParsePrefix("0.0.0.0/0")
	c.BaseURL.Host = "evil.example"
	secret[0] = 'x'
	if !reflect.DeepEqual(*base, before) {
		t.Fatal("ForSite or changes to its result modified the base config")
	}
	if c.SecretKey[0] != 's' {
		t.Fatal("the site secret aliases the caller's slice")
	}

	// A custom primary domain: HSTS without includeSubDomains, passkeys stay
	// on the RP ID fixed at creation and accept both origins.
	c, err = ForSite(base, SiteParams{ID: 7, Host: "www.example.org", Custom: true, Secret: secret, RPID: "blog.sap13ns.site"})
	if err != nil {
		t.Fatal(err)
	}
	if c.BaseURL.String() != "https://www.example.org" || !c.HSTSNoSubdomains || c.PasskeyRPID != "blog.sap13ns.site" ||
		!reflect.DeepEqual(c.PasskeyOrigins, []string{"https://www.example.org", "https://blog.sap13ns.site"}) {
		t.Fatalf("custom domain: url=%s nosub=%v rp=%q origins=%v", c.BaseURL, c.HSTSNoSubdomains, c.PasskeyRPID, c.PasskeyOrigins)
	}

	for _, tc := range []struct {
		name string
		p    SiteParams
	}{
		{"no id", SiteParams{Host: "a.sap13ns.site", Secret: secret}},
		{"host with port", SiteParams{ID: 1, Host: "a.sap13ns.site:443", Secret: secret}},
		{"upper-case host", SiteParams{ID: 1, Host: "A.sap13ns.site", Secret: secret}},
		{"short secret", SiteParams{ID: 1, Host: "a.sap13ns.site", Secret: secret[:16]}},
		{"apex RP ID", SiteParams{ID: 1, Host: "a.sap13ns.site", Secret: secret, RPID: "sap13ns.site"}},
		{"bad RP ID", SiteParams{ID: 1, Host: "a.sap13ns.site", Secret: secret, RPID: "a.sap13ns.site:443"}},
	} {
		if _, err := ForSite(base, tc.p); err == nil {
			t.Errorf("%s: ForSite accepted %+v", tc.name, tc.p)
		}
	}
	single := *base
	single.Multisite = false
	if _, err := ForSite(&single, SiteParams{ID: 1, Host: "a.sap13ns.site", Secret: secret}); err == nil {
		t.Error("ForSite accepted a single-site base config")
	}
}

// In development the network runs on a port, and its sites keep it.
func TestForSiteKeepsPort(t *testing.T) {
	base := networkConfig(t, "http://localhost:8137")
	c, err := ForSite(base, SiteParams{ID: 3, Host: "blog.localhost", Secret: make([]byte, 32)})
	if err != nil {
		t.Fatal(err)
	}
	if c.BaseURL.String() != "http://blog.localhost:8137" || c.CookieSecure || c.HSTS ||
		!reflect.DeepEqual(c.PasskeyOrigins, []string{"http://blog.localhost:8137"}) {
		t.Fatalf("dev site: url=%s secure=%v hsts=%v origins=%v", c.BaseURL, c.CookieSecure, c.HSTS, c.PasskeyOrigins)
	}
}

func TestForNetwork(t *testing.T) {
	base := networkConfig(t, "https://sap13ns.site")
	secret := []byte(strings.Repeat("n", 32))
	c, err := ForNetwork(base, 1, secret)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base.DataDir, "sites", "1")
	if c.BaseURL.String() != "https://sap13ns.site" || c.DataDir != dir || c.UploadsDir != filepath.Join(dir, "uploads") ||
		string(c.SecretKey) != string(secret) || c.PasskeyRPID != "" || c.HSTSNoSubdomains || !c.Stripe.Enabled() {
		t.Fatalf("portal config: %+v", c)
	}
	bv, cv := reflect.ValueOf(base).Elem(), reflect.ValueOf(c).Elem()
	for i := range bv.NumField() {
		f := bv.Type().Field(i)
		switch f.Type.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Map:
			if !bv.Field(i).IsNil() && !cv.Field(i).IsNil() && bv.Field(i).Pointer() == cv.Field(i).Pointer() {
				t.Errorf("Config.%s is shared between the network and the portal", f.Name)
			}
		}
	}
	if &c.Stripe.Prices == &base.Stripe.Prices || reflect.ValueOf(c.Stripe.Prices).Pointer() == reflect.ValueOf(base.Stripe.Prices).Pointer() {
		t.Error("the Stripe prices are shared")
	}
	if _, err := ForNetwork(base, 1, secret[:8]); err == nil {
		t.Error("ForNetwork accepted a short secret")
	}
	single := *base
	single.Multisite = false
	if _, err := ForNetwork(&single, 1, secret); err == nil {
		t.Error("ForNetwork accepted a single-site config")
	}
}
