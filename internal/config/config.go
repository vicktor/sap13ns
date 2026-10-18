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
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	DatabaseURL    string
	Listen         string // "127.0.0.1:8080" or "unix:/run/sap13ns/sap13ns.sock"
	BaseURL        *url.URL
	SecretKey      []byte
	DataDir        string
	UploadsDir     string
	PluginsDir     string
	ThemesDir      string
	TrustedProxies []netip.Prefix
	MaxUploadBytes int64
	ServeUploads   bool // serve /uploads from the app (nginx normally does it)
	Dev            bool
	// DevSkip2FA lifts the mandatory second factor in development mode only
	// (SAP13NS_DEV_SKIP_2FA), for local testing with seeded accounts.
	DevSkip2FA   bool
	CookieSecure bool
	HSTS         bool
	LogLevel     slog.Level
	LogFormat    string // "json" | "text"
	SMTP         SMTP
	DBMaxConns   int32
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

	conns, err := strconv.Atoi(env("DB_MAX_CONNS", "16"))
	if err != nil || conns < 2 {
		return nil, errors.New("SAP13NS_DB_MAX_CONNS must be an integer >= 2")
	}
	c.DBMaxConns = int32(conns)

	c.CookieSecure = envBool("COOKIE_SECURE", u.Scheme == "https")
	c.HSTS = envBool("HSTS", u.Scheme == "https")
	c.ServeUploads = envBool("SERVE_UPLOADS", true)

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
	return c, nil
}

// AbsURL joins a site-relative path with the configured base URL.
func (c *Config) AbsURL(path string) string {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return c.BaseURL.String() + path
}
