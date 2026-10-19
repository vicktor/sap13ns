// Package config loads runtime configuration from environment variables.
//
// Every setting has the prefix SAP13NS_. In production the variables come from
// a systemd EnvironmentFile; in development an env file can be passed with -env.
package config

import (
	"bufio"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

type SMTP struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
}

func (s SMTP) Enabled() bool { return s.Host != "" && s.From != "" }

type Config struct {
	DatabaseURL string
	Listen      string // "127.0.0.1:8080" or "unix:/run/sap13ns/sap13ns.sock"
	SocketGroup string // group given access to the Unix socket (nginx), if not the process group
	BaseURL     *url.URL
	SecretKey   []byte
	DataDir     string
	UploadsDir  string
	PluginsDir  string
	ThemesDir   string
	// NetworkThemesDir, on a site of a network, is the network's themes
	// directory (SAP13NS_THEMES_DIR): the operator's catalogue of themes,
	// offered to every site besides the built-in ones.
	NetworkThemesDir string
	TrustedProxies   []netip.Prefix
	MaxUploadBytes   int64
	ServeUploads     bool // serve /uploads from the app (nginx normally does it)
	Dev              bool
	// DevSkip2FA lifts the mandatory second factor in development mode only
	// (SAP13NS_DEV_SKIP_2FA), for local testing with seeded accounts.
	DevSkip2FA   bool
	CookieSecure bool
	HSTS         bool
	LogLevel     slog.Level
	LogFormat    string // "json" | "text"
	SMTP         SMTP
	DBMaxConns   int32
	SiteBackups  SiteBackups

	// DevGravatarURL, in development mode only (SAP13NS_DEV_GRAVATAR_URL),
	// replaces https://gravatar.com/avatar/ with a stand-in server, which
	// may be on this machine, so that seeded accounts never reach Gravatar.
	DevGravatarURL string

	// Multisite settings. They are read only when SAP13NS_MULTISITE is
	// true, so a single-site install gets the zero values whatever else
	// its environment contains.
	Multisite      bool
	SitesDomain    string        // default site hosts are <slug>.<SitesDomain>
	UnknownHost    string        // "404", or "redirect" (302 to BaseURL)
	MaxLoadedSites int           // sites kept in memory at once
	SiteIdle       time.Duration // a site is unloaded after this long without requests
	MemoryLimitMB  int           // soft heap limit of the process; 0 = none
	TLS            string        // "proxy" (nginx or Caddy terminate TLS) or "autocert"
	Stripe         Stripe        // billing; off when Stripe.SecretKey is empty

	// Custom domains point to the network with a CNAME to DomainTarget (by
	// default the BaseURL host), or with A/AAAA records: the addresses in
	// DomainAddresses, or those DomainTarget resolves to when it is empty.
	DomainTarget    string
	DomainAddresses []netip.Addr

	// SAP13NS_TLS=autocert: the process terminates TLS on Listen. TLSCert
	// and TLSKey hold the static (wildcard) certificate of the portal host
	// and the default subdomains; the built-in ACME client gets one for
	// each verified custom domain. HTTPListen ("" = off) answers HTTP-01
	// challenges and redirects to https.
	TLSCert       string
	TLSKey        string
	ACMEEmail     string
	ACMEDirectory string // "" = Let's Encrypt
	HTTPListen    string

	// Set by ForSite for one site of a network. The zero values keep the
	// single-site behaviour: HSTS covers subdomains, and passkeys use the
	// BaseURL host as RP ID and the BaseURL origin.
	HSTSNoSubdomains bool     // a custom domain: never force HTTPS on the owner's other hosts
	PasskeyRPID      string   // WebAuthn RP ID
	PasskeyOrigins   []string // accepted WebAuthn origins
}

// Stripe holds the billing settings of a network (docs/BILLING.md §6).
type Stripe struct {
	SecretKey       string
	WebhookSecrets  []string                // more than one during a rotation
	Prices          map[string]StripePrices // by plan slug
	APIVersion      string                  // "" = the version pinned by internal/billing
	AutomaticTax    bool
	TaxIDCollection bool
	BillingAddress  bool // Checkout always collects a billing address (VAT evidence)
	TrialDays       int
	GraceDays       int // days a site stays online after a failed renewal
	// PortalConfiguration is the Customer Portal configuration (bpc_...)
	// of the portal sessions; "" uses the account's default.
	PortalConfiguration string
}

// StripePrices are the price IDs of one plan.
type StripePrices struct {
	Monthly string
	Yearly  string // optional
}

func (s Stripe) Enabled() bool { return s.SecretKey != "" }

// SiteBackups are the settings of the per-site backups (internal/sitebackup,
// docs/MULTISITE.md "Backups"). The database backups of `sap13ns backup
// create` read their own variables in the CLI.
type SiteBackups struct {
	// Root is SAP13NS_BACKUP_DIR, by default <data dir>/backups.
	Root string
	// Dir holds the site's archives: <Root>/site on a single site,
	// <Root>/sites/<id> on a site of a network (ForSite, ForNetwork), ""
	// for the network itself.
	Dir string
	// PassphraseFile and Passphrase (SAP13NS_BACKUP_PASSPHRASE_FILE, else
	// SAP13NS_BACKUP_PASSPHRASE) encrypt the archives, as they do the
	// database backups. ReadPassphrase reads them.
	PassphraseFile string
	Passphrase     string
	// Retention, applied after every backup: the newest of each of the
	// last KeepDaily days and KeepWeekly weeks.
	KeepDaily  int // SAP13NS_SITE_BACKUP_KEEP_DAILY, default 7
	KeepWeekly int // SAP13NS_SITE_BACKUP_KEEP_WEEKLY, default 4
	// Network only: the nightly backup of every site (SAP13NS_SITE_BACKUPS,
	// on by default). It starts at At after local midnight
	// (SAP13NS_SITE_BACKUP_TIME, "03:30"), backs up Concurrency sites at
	// once (SAP13NS_SITE_BACKUP_CONCURRENCY, 2) and stops after Budget
	// (SAP13NS_SITE_BACKUP_BUDGET, 3h); the next night begins with the
	// sites it did not reach.
	Nightly     bool
	At          time.Duration
	Concurrency int
	Budget      time.Duration
}

// String keeps the passphrase out of logs.
func (b SiteBackups) String() string {
	return fmt.Sprintf("site backups(%s, encrypted %v)", b.Dir, b.PassphraseFile != "" || b.Passphrase != "")
}

// ReadPassphrase returns the passphrase of the archives, "" for none.
func (b SiteBackups) ReadPassphrase() (string, error) {
	if b.PassphraseFile != "" {
		p, err := os.ReadFile(b.PassphraseFile)
		if err != nil {
			return "", fmt.Errorf("backup passphrase file: %w", err)
		}
		return strings.TrimRight(string(p), "\r\n"), nil
	}
	return b.Passphrase, nil
}

// SiteBackupDir is the directory of the backups of a site of a network.
func (b SiteBackups) SiteBackupDir(id int64) string {
	return filepath.Join(b.Root, "sites", strconv.FormatInt(id, 10))
}

func loadSiteBackups(dataDir string, multisite bool) (SiteBackups, error) {
	root, err := filepath.Abs(env("BACKUP_DIR", filepath.Join(dataDir, "backups")))
	if err != nil {
		return SiteBackups{}, err
	}
	b := SiteBackups{Root: root, PassphraseFile: env("BACKUP_PASSPHRASE_FILE", ""), Passphrase: env("BACKUP_PASSPHRASE", "")}
	if !multisite {
		b.Dir = filepath.Join(root, "site")
	}
	for _, k := range []struct {
		key string
		def string
		to  *int
		max int
	}{
		{"SITE_BACKUP_KEEP_DAILY", "7", &b.KeepDaily, 366},
		{"SITE_BACKUP_KEEP_WEEKLY", "4", &b.KeepWeekly, 520},
	} {
		n, err := strconv.Atoi(env(k.key, k.def))
		if err != nil || n < 0 || n > k.max {
			return SiteBackups{}, fmt.Errorf("SAP13NS_%s must be between 0 and %d", k.key, k.max)
		}
		*k.to = n
	}
	if !multisite {
		return b, nil
	}
	if b.Nightly, err = envStrictBool("SITE_BACKUPS", true); err != nil {
		return SiteBackups{}, err
	}
	at := env("SITE_BACKUP_TIME", "03:30")
	t, err := time.Parse("15:04", at)
	if err != nil {
		return SiteBackups{}, fmt.Errorf("SAP13NS_SITE_BACKUP_TIME must be a time of day such as 03:30, got %q", at)
	}
	b.At = time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute
	b.Concurrency, err = strconv.Atoi(env("SITE_BACKUP_CONCURRENCY", "2"))
	if err != nil || b.Concurrency < 1 || b.Concurrency > 16 {
		return SiteBackups{}, errors.New("SAP13NS_SITE_BACKUP_CONCURRENCY must be between 1 and 16")
	}
	b.Budget, err = time.ParseDuration(env("SITE_BACKUP_BUDGET", "3h"))
	if err != nil || b.Budget < 10*time.Minute || b.Budget > 23*time.Hour {
		return SiteBackups{}, errors.New("SAP13NS_SITE_BACKUP_BUDGET must be a duration between 10m and 23h, such as 3h")
	}
	return b, nil
}

// String keeps the API key and the webhook secrets out of logs.
func (s Stripe) String() string {
	if !s.Enabled() {
		return "stripe(off)"
	}
	return fmt.Sprintf("stripe(%d plans)", len(s.Prices))
}

// LoadEnvFile reads KEY=VALUE lines into the process environment without
// overriding variables that are already set.
func LoadEnvFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("%s:%d: expected KEY=VALUE", path, n)
		}
		k = strings.TrimSpace(strings.TrimPrefix(k, "export "))
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		if _, exists := os.LookupEnv(k); !exists {
			os.Setenv(k, v)
		}
	}
	return sc.Err()
}

func env(key, def string) string {
	if v, ok := os.LookupEnv("SAP13NS_" + key); ok && v != "" {
		return v
	}
	return def
}

// envStrictBool is envBool for the newer settings, where a typo is an
// error instead of the default.
func envStrictBool(key string, def bool) (bool, error) {
	v := env(key, "")
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("SAP13NS_%s must be true or false, got %q", key, v)
	}
	return b, nil
}

func envBool(key string, def bool) bool {
	v := env(key, "")
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

// Load builds the configuration. requireSecret is false for commands that
// never touch encrypted data (migrate, keygen).
func Load(requireSecret bool) (*Config, error) {
	c := &Config{
		DatabaseURL: env("DATABASE_URL", ""),
		Listen:      env("LISTEN", "127.0.0.1:8080"),
		SocketGroup: env("SOCKET_GROUP", ""),
		DataDir:     env("DATA_DIR", "./data"),
		Dev:         envBool("DEV", false),
		DevSkip2FA:  envBool("DEV_SKIP_2FA", false),
		LogFormat:   env("LOG_FORMAT", "json"),
	}
	if c.DatabaseURL == "" {
		return nil, errors.New("SAP13NS_DATABASE_URL is required")
	}

	base := env("BASE_URL", "http://localhost:8080")
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("SAP13NS_BASE_URL must be an absolute http(s) URL, got %q", base)
	}
	if u.Path == "/" {
		u.Path = ""
	}
	if u.Path != "" {
		return nil, errors.New("SAP13NS_BASE_URL must not contain a path; mount the site at the domain root")
	}
	c.BaseURL = u

	if k := env("SECRET_KEY", ""); k != "" {
		key, err := hex.DecodeString(k)
		if err != nil || len(key) != 32 {
			return nil, errors.New("SAP13NS_SECRET_KEY must be 64 hex characters (32 bytes); generate one with `sap13ns keygen`")
		}
		c.SecretKey = key
	} else if requireSecret {
		return nil, errors.New("SAP13NS_SECRET_KEY is required; generate one with `sap13ns keygen`")
	}

	c.DataDir, err = filepath.Abs(c.DataDir)
	if err != nil {
		return nil, err
	}
	c.UploadsDir = env("UPLOADS_DIR", filepath.Join(c.DataDir, "uploads"))
	c.PluginsDir = env("PLUGINS_DIR", filepath.Join(c.DataDir, "plugins"))
	c.ThemesDir = env("THEMES_DIR", filepath.Join(c.DataDir, "themes"))

	for _, p := range strings.Split(env("TRUSTED_PROXIES", "127.0.0.1/32,::1/128"), ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		pfx, err := netip.ParsePrefix(p)
		if err != nil {
			return nil, fmt.Errorf("SAP13NS_TRUSTED_PROXIES: %w", err)
		}
		c.TrustedProxies = append(c.TrustedProxies, pfx)
	}

	mb, err := strconv.Atoi(env("MAX_UPLOAD_MB", "16"))
	if err != nil || mb < 1 || mb > 1024 {
		return nil, errors.New("SAP13NS_MAX_UPLOAD_MB must be between 1 and 1024")
	}
	c.MaxUploadBytes = int64(mb) << 20

	c.Multisite, err = envStrictBool("MULTISITE", false)
	if err != nil {
		return nil, err
	}
	defConns := "16"
	if c.Multisite {
		defConns = "32" // every site of the network shares the pool
	}
	conns, err := strconv.Atoi(env("DB_MAX_CONNS", defConns))
	if err != nil || conns < 2 {
		return nil, errors.New("SAP13NS_DB_MAX_CONNS must be an integer >= 2")
	}
	c.DBMaxConns = int32(conns)

	c.CookieSecure = envBool("COOKIE_SECURE", u.Scheme == "https")
	c.HSTS = envBool("HSTS", u.Scheme == "https")
	c.ServeUploads = envBool("SERVE_UPLOADS", true)
	if c.Dev {
		c.DevGravatarURL = env("DEV_GRAVATAR_URL", "")
	}

	switch strings.ToLower(env("LOG_LEVEL", "info")) {
	case "debug":
		c.LogLevel = slog.LevelDebug
	case "warn":
		c.LogLevel = slog.LevelWarn
	case "error":
		c.LogLevel = slog.LevelError
	default:
		c.LogLevel = slog.LevelInfo
	}

	port, _ := strconv.Atoi(env("SMTP_PORT", "587"))
	c.SMTP = SMTP{
		Host:     env("SMTP_HOST", ""),
		Port:     port,
		Username: env("SMTP_USERNAME", ""),
		Password: env("SMTP_PASSWORD", ""),
		From:     env("SMTP_FROM", ""),
	}
	if c.SiteBackups, err = loadSiteBackups(c.DataDir, c.Multisite); err != nil {
		return nil, err
	}
	if c.Multisite {
		if err := c.loadMultisite(); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// hostRe matches a host name in lower-case A-label form, without port or
// trailing dot.
var hostRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`)

// ValidHost reports whether h is a host name a network can route: lower
// case, ASCII (A-label), without port or trailing dot, not an IP address.
func ValidHost(h string) bool {
	if len(h) > 253 || !hostRe.MatchString(h) {
		return false
	}
	_, err := netip.ParseAddr(h)
	return err != nil
}

func (c *Config) loadMultisite() error {
	c.SitesDomain = strings.ToLower(env("SITES_DOMAIN", c.BaseURL.Hostname()))
	if !ValidHost(c.SitesDomain) {
		return fmt.Errorf("SAP13NS_SITES_DOMAIN must be a host name in lower-case ASCII, without port, got %q", c.SitesDomain)
	}
	c.UnknownHost = env("UNKNOWN_HOST", "404")
	if c.UnknownHost != "404" && c.UnknownHost != "redirect" {
		return fmt.Errorf("SAP13NS_UNKNOWN_HOST must be 404 or redirect, got %q", c.UnknownHost)
	}
	n, err := strconv.Atoi(env("MAX_LOADED_SITES", "50"))
	if err != nil || n < 1 || n > 100000 {
		return errors.New("SAP13NS_MAX_LOADED_SITES must be between 1 and 100000")
	}
	c.MaxLoadedSites = n
	c.SiteIdle, err = time.ParseDuration(env("SITE_IDLE", "15m"))
	if err != nil || c.SiteIdle < time.Minute {
		return errors.New("SAP13NS_SITE_IDLE must be a duration of at least 1m, such as 15m")
	}
	c.MemoryLimitMB, err = strconv.Atoi(env("MEMORY_LIMIT_MB", "0"))
	if err != nil || c.MemoryLimitMB < 0 || c.MemoryLimitMB > 0 && c.MemoryLimitMB < 64 {
		return errors.New("SAP13NS_MEMORY_LIMIT_MB must be 0 (no limit) or at least 64")
	}
	c.DomainTarget = strings.ToLower(env("DOMAIN_TARGET", c.BaseURL.Hostname()))
	if !ValidHost(c.DomainTarget) {
		return fmt.Errorf("SAP13NS_DOMAIN_TARGET must be a host name in lower-case ASCII, without port, got %q", c.DomainTarget)
	}
	for _, a := range strings.Split(env("DOMAIN_ADDRESSES", ""), ",") {
		if a = strings.TrimSpace(a); a == "" {
			continue
		}
		ip, err := netip.ParseAddr(a)
		if err != nil || ip.Zone() != "" || !ip.IsGlobalUnicast() {
			return fmt.Errorf("SAP13NS_DOMAIN_ADDRESSES must be comma-separated unicast IP addresses (not loopback or link-local), got %q", a)
		}
		c.DomainAddresses = append(c.DomainAddresses, ip.Unmap())
	}
	c.TLS = env("TLS", "proxy")
	switch {
	case c.TLS != "proxy" && c.TLS != "autocert":
		return fmt.Errorf("SAP13NS_TLS must be proxy or autocert, got %q", c.TLS)
	case c.TLS == "autocert" && c.BaseURL.Scheme != "https":
		return errors.New("SAP13NS_TLS=autocert needs an https SAP13NS_BASE_URL")
	}
	if c.TLS == "autocert" {
		if err := c.loadAutocert(); err != nil {
			return err
		}
	}
	c.Stripe, err = loadStripe()
	return err
}

// loadAutocert reads the settings of a network that terminates TLS itself.
func (c *Config) loadAutocert() error {
	c.TLSCert, c.TLSKey = env("TLS_CERT", ""), env("TLS_KEY", "")
	if c.TLSCert == "" || c.TLSKey == "" {
		return errors.New("SAP13NS_TLS=autocert needs SAP13NS_TLS_CERT and SAP13NS_TLS_KEY: the wildcard certificate of the portal host and *." + c.SitesDomain)
	}
	if strings.HasPrefix(c.Listen, "unix:") {
		return errors.New("SAP13NS_TLS=autocert terminates TLS itself: SAP13NS_LISTEN must be a TCP address such as :443")
	}
	c.ACMEEmail = env("ACME_EMAIL", "")
	if c.ACMEEmail != "" && (!strings.Contains(c.ACMEEmail, "@") || strings.ContainsAny(c.ACMEEmail, " \t\r\n,<>")) {
		return fmt.Errorf("SAP13NS_ACME_EMAIL must be an e-mail address, got %q", c.ACMEEmail)
	}
	c.ACMEDirectory = env("ACME_DIRECTORY", "")
	if c.ACMEDirectory != "" {
		u, err := url.Parse(c.ACMEDirectory)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return fmt.Errorf("SAP13NS_ACME_DIRECTORY must be an https URL, got %q", c.ACMEDirectory)
		}
	}
	c.HTTPListen = env("HTTP_LISTEN", ":80")
	switch {
	case c.HTTPListen == "off":
		c.HTTPListen = ""
	case strings.HasPrefix(c.HTTPListen, "unix:"):
		return errors.New("SAP13NS_HTTP_LISTEN must be a TCP address such as :80, or off")
	}
	return nil
}

var (
	stripeKeyRe = regexp.MustCompile(`^(sk|rk)_(live|test)_[A-Za-z0-9]{8,}$`)
	stripeIDRe  = regexp.MustCompile(`^[A-Za-z0-9]{8,}$`)
	planSlugRe  = regexp.MustCompile(`^[a-z][a-z0-9_]{1,31}$`) // as the CHECK on network.plans
)

const pricePrefix = "SAP13NS_STRIPE_PRICE_"

// requiredPlans need a monthly price when billing is on.
var requiredPlans = []string{"pro", "business"}

// loadStripe reads the billing settings. Billing is off when
// SAP13NS_STRIPE_SECRET_KEY is unset, and the other variables are then
// ignored.
func loadStripe() (Stripe, error) {
	key := env("STRIPE_SECRET_KEY", "")
	if key == "" {
		return Stripe{}, nil
	}
	if !stripeKeyRe.MatchString(key) {
		return Stripe{}, errors.New("SAP13NS_STRIPE_SECRET_KEY must be a Stripe secret (sk_) or restricted (rk_) key")
	}
	s := Stripe{SecretKey: key, APIVersion: env("STRIPE_API_VERSION", ""), Prices: map[string]StripePrices{}}
	for _, w := range strings.Split(env("STRIPE_WEBHOOK_SECRET", ""), ",") {
		if w = strings.TrimSpace(w); w == "" {
			continue
		}
		if id, ok := strings.CutPrefix(w, "whsec_"); !ok || !stripeIDRe.MatchString(id) {
			return Stripe{}, errors.New("SAP13NS_STRIPE_WEBHOOK_SECRET must be one or more comma-separated whsec_ secrets")
		}
		s.WebhookSecrets = append(s.WebhookSecrets, w)
	}
	if len(s.WebhookSecrets) == 0 {
		return Stripe{}, errors.New("SAP13NS_STRIPE_WEBHOOK_SECRET is required when SAP13NS_STRIPE_SECRET_KEY is set")
	}
	// SAP13NS_STRIPE_PRICE_<PLAN> and SAP13NS_STRIPE_PRICE_<PLAN>_YEARLY.
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		rest, ok := strings.CutPrefix(k, pricePrefix)
		if !ok || v == "" {
			continue
		}
		plan, yearly := strings.CutSuffix(rest, "_YEARLY")
		plan = strings.ToLower(plan)
		if !planSlugRe.MatchString(plan) {
			return Stripe{}, fmt.Errorf("%s: %q is not a plan name", k, plan)
		}
		if id, ok := strings.CutPrefix(v, "price_"); !ok || !stripeIDRe.MatchString(id) {
			return Stripe{}, fmt.Errorf("%s must be a Stripe price ID (price_...)", k)
		}
		p := s.Prices[plan]
		if yearly {
			p.Yearly = v
		} else {
			p.Monthly = v
		}
		s.Prices[plan] = p
	}
	for plan, p := range s.Prices {
		if p.Monthly == "" {
			name := pricePrefix + strings.ToUpper(plan)
			return Stripe{}, fmt.Errorf("%s_YEARLY is set without %s", name, name)
		}
	}
	for _, plan := range requiredPlans {
		if s.Prices[plan].Monthly == "" {
			return Stripe{}, fmt.Errorf("%s%s is required when billing is on", pricePrefix, strings.ToUpper(plan))
		}
	}
	var err error
	if s.AutomaticTax, err = envStrictBool("STRIPE_AUTOMATIC_TAX", false); err != nil {
		return Stripe{}, err
	}
	if s.TaxIDCollection, err = envStrictBool("STRIPE_TAX_ID_COLLECTION", true); err != nil {
		return Stripe{}, err
	}
	if s.BillingAddress, err = envStrictBool("STRIPE_BILLING_ADDRESS", true); err != nil {
		return Stripe{}, err
	}
	s.TrialDays, err = strconv.Atoi(env("STRIPE_TRIAL_DAYS", "0"))
	if err != nil || s.TrialDays < 0 || s.TrialDays > 730 {
		return Stripe{}, errors.New("SAP13NS_STRIPE_TRIAL_DAYS must be between 0 and 730")
	}
	if s.PortalConfiguration = env("STRIPE_PORTAL_CONFIGURATION", ""); s.PortalConfiguration != "" {
		if id, ok := strings.CutPrefix(s.PortalConfiguration, "bpc_"); !ok || !stripeIDRe.MatchString(id) {
			return Stripe{}, errors.New("SAP13NS_STRIPE_PORTAL_CONFIGURATION must be a Customer Portal configuration ID (bpc_...)")
		}
	}
	s.GraceDays, err = strconv.Atoi(env("BILLING_GRACE_DAYS", "14"))
	if err != nil || s.GraceDays < 0 || s.GraceDays > 90 {
		return Stripe{}, errors.New("SAP13NS_BILLING_GRACE_DAYS must be between 0 and 90")
	}
	return s, nil
}

// SiteParams describes one site of a network for ForSite.
type SiteParams struct {
	ID     int64
	Host   string // primary host: lower-case A-label without port
	Custom bool   // Host is a custom domain, not a subdomain of SitesDomain
	Secret []byte // the site's own 32-byte secret, unwrapped
	RPID   string // WebAuthn RP ID fixed when the site was created; "" = Host
}

// ForSite returns the configuration of one site of a network: a deep copy
// of base with the site's URL, data directory and secret. The site keeps
// the network's scheme and port (http://blog.localhost:8137 in
// development), so CookieSecure and HSTS stay as the network has them.
// The billing settings are dropped: billing runs in the network, never in
// a site's server.
func ForSite(base *Config, p SiteParams) (*Config, error) {
	switch {
	case !base.Multisite:
		return nil, errors.New("config: ForSite needs a multisite configuration")
	case p.ID < 1:
		return nil, fmt.Errorf("config: invalid site id %d", p.ID)
	case !ValidHost(p.Host):
		return nil, fmt.Errorf("config: invalid site host %q", p.Host)
	case len(p.Secret) != 32:
		return nil, errors.New("config: a site secret must be 32 bytes")
	}
	rpID := p.RPID
	if rpID == "" {
		rpID = p.Host
	}
	switch {
	case !ValidHost(rpID):
		return nil, fmt.Errorf("config: invalid passkey RP ID %q", rpID)
	case rpID == base.SitesDomain || rpID == base.BaseURL.Hostname():
		// A passkey for the shared parent domain would work on every site.
		return nil, fmt.Errorf("config: a site cannot use %q as passkey RP ID", rpID)
	}

	c := *base
	c.Stripe = Stripe{}
	c.TrustedProxies = slices.Clone(base.TrustedProxies)
	c.DomainAddresses = slices.Clone(base.DomainAddresses)
	c.SecretKey = slices.Clone(p.Secret)
	origin := func(host string) *url.URL {
		u := &url.URL{Scheme: base.BaseURL.Scheme, Host: host}
		if port := base.BaseURL.Port(); port != "" {
			u.Host += ":" + port
		}
		return u
	}
	c.BaseURL = origin(p.Host)
	c.DataDir = filepath.Join(base.DataDir, "sites", strconv.FormatInt(p.ID, 10))
	c.UploadsDir = filepath.Join(c.DataDir, "uploads")
	c.PluginsDir = filepath.Join(c.DataDir, "plugins")
	c.ThemesDir = filepath.Join(c.DataDir, "themes")
	c.NetworkThemesDir = base.ThemesDir
	c.SiteBackups.Dir = base.SiteBackups.SiteBackupDir(p.ID)
	c.HSTSNoSubdomains = p.Custom
	c.PasskeyRPID = rpID
	c.PasskeyOrigins = []string{c.BaseURL.String()}
	if rpID != p.Host {
		// The RP ID host serves /.well-known/webauthn naming the primary
		// origin (Related Origin Requests), so both origins are accepted.
		c.PasskeyOrigins = append(c.PasskeyOrigins, origin(rpID).String())
	}
	return &c, nil
}

// ForNetwork returns the configuration of the network portal, the site
// served on the SAP13NS_BASE_URL host: a deep copy of base with the
// portal's own secret and data directory. It keeps the base URL, so its
// passkeys use the portal host, and the billing settings, since billing
// runs there.
func ForNetwork(base *Config, id int64, secret []byte) (*Config, error) {
	switch {
	case !base.Multisite:
		return nil, errors.New("config: ForNetwork needs a multisite configuration")
	case id < 1:
		return nil, fmt.Errorf("config: invalid site id %d", id)
	case len(secret) != 32:
		return nil, errors.New("config: a site secret must be 32 bytes")
	}
	c := *base
	u := *base.BaseURL
	c.BaseURL = &u
	c.TrustedProxies = slices.Clone(base.TrustedProxies)
	c.DomainAddresses = slices.Clone(base.DomainAddresses)
	c.SecretKey = slices.Clone(secret)
	c.Stripe.WebhookSecrets = slices.Clone(base.Stripe.WebhookSecrets)
	if base.Stripe.Prices != nil {
		c.Stripe.Prices = maps.Clone(base.Stripe.Prices)
	}
	c.DataDir = filepath.Join(base.DataDir, "sites", strconv.FormatInt(id, 10))
	c.UploadsDir = filepath.Join(c.DataDir, "uploads")
	c.PluginsDir = filepath.Join(c.DataDir, "plugins")
	c.ThemesDir = filepath.Join(c.DataDir, "themes")
	c.NetworkThemesDir = base.ThemesDir
	c.SiteBackups.Dir = base.SiteBackups.SiteBackupDir(id)
	c.HSTSNoSubdomains, c.PasskeyRPID, c.PasskeyOrigins = false, "", nil
	return &c, nil
}

// AbsURL joins a site-relative path with the configured base URL.
func (c *Config) AbsURL(path string) string {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return c.BaseURL.String() + path
}
