package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// libpqEnv maps connection-string keywords to the libpq environment
// variables pg_dump and pg_restore read. host, port, dbname, user and
// password are resolved by pgconn.ParseConfig instead (defaults, passfile,
// service file), and keywords that are not libpq's (pgx pool settings,
// runtime parameters) are ignored.
var libpqEnv = map[string]string{
	"hostaddr":                 "PGHOSTADDR",
	"passfile":                 "PGPASSFILE",
	"service":                  "PGSERVICE",
	"servicefile":              "PGSERVICEFILE",
	"options":                  "PGOPTIONS",
	"application_name":         "PGAPPNAME",
	"client_encoding":          "PGCLIENTENCODING",
	"connect_timeout":          "PGCONNECT_TIMEOUT",
	"sslmode":                  "PGSSLMODE",
	"sslnegotiation":           "PGSSLNEGOTIATION",
	"sslcert":                  "PGSSLCERT",
	"sslkey":                   "PGSSLKEY",
	"sslcertmode":              "PGSSLCERTMODE",
	"sslrootcert":              "PGSSLROOTCERT",
	"sslcrl":                   "PGSSLCRL",
	"sslcrldir":                "PGSSLCRLDIR",
	"sslsni":                   "PGSSLSNI",
	"ssl_min_protocol_version": "PGSSLMINPROTOCOLVERSION",
	"ssl_max_protocol_version": "PGSSLMAXPROTOCOLVERSION",
	"min_protocol_version":     "PGMINPROTOCOLVERSION",
	"max_protocol_version":     "PGMAXPROTOCOLVERSION",
	"requirepeer":              "PGREQUIREPEER",
	"require_auth":             "PGREQUIREAUTH",
	"channel_binding":          "PGCHANNELBINDING",
	"gssencmode":               "PGGSSENCMODE",
	"gsslib":                   "PGGSSLIB",
	"krbsrvname":               "PGKRBSRVNAME",
	"target_session_attrs":     "PGTARGETSESSIONATTRS",
	"load_balance_hosts":       "PGLOADBALANCEHOSTS",
}

// inheritEnv lists the variables of our own environment that the PostgreSQL
// tools may see. Everything else is dropped, in particular SAP13NS_SECRET_KEY
// and the backup passphrase.
var inheritEnv = map[string]bool{
	"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "LANG": true, "LANGUAGE": true,
	"TZ": true, "TMPDIR": true, "KRB5CCNAME": true, "KRB5_CONFIG": true, "KRB5_KTNAME": true,
}

// pgEnv turns a connection string into the environment of a pg_dump or
// pg_restore child process, so that the password never appears on a command
// line, where any local user could read it with ps. It also returns the
// database name.
func pgEnv(dsn string) (env []string, dbname string, err error) {
	cfg, err := pgconn.ParseConfig(dsn)
	if err != nil {
		return nil, "", fmt.Errorf("parse database url: %w", err)
	}
	params, err := dsnParams(dsn)
	if err != nil {
		return nil, "", err
	}
	if _, ok := params["sslpassword"]; ok {
		return nil, "", errors.New("parse database url: sslpassword cannot be passed to pg_dump without exposing it; use a client key without a passphrase, readable only by the service user")
	}

	vars := map[string]string{}
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		// PG* variables are defaults that pgconn has applied as well,
		// so the tools resolve the connection the same way.
		if v != "" && (inheritEnv[k] || strings.HasPrefix(k, "LC_") || strings.HasPrefix(k, "PG")) {
			vars[k] = v
		}
	}
	for k, v := range params {
		if name, ok := libpqEnv[k]; ok {
			vars[name] = v
		}
	}
	if params["ssl"] == "true" && params["sslmode"] == "" {
		vars["PGSSLMODE"] = "require" // pgx's URL alias
	}

	// Several hosts become libpq's comma-separated lists. Fallbacks repeat
	// a host for sslmode=prefer/allow; keep each host:port once.
	hosts := []string{cfg.Host}
	ports := []string{strconv.Itoa(int(cfg.Port))}
	seen := map[string]bool{cfg.Host + "\x00" + ports[0]: true}
	for _, fb := range cfg.Fallbacks {
		port := strconv.Itoa(int(fb.Port))
		if key := fb.Host + "\x00" + port; !seen[key] {
			seen[key] = true
			hosts = append(hosts, fb.Host)
			ports = append(ports, port)
		}
	}
	vars["PGHOST"] = strings.Join(hosts, ",") // a directory for a Unix socket
	vars["PGPORT"] = strings.Join(ports, ",")
	vars["PGUSER"] = cfg.User
	dbname = cfg.Database
	if dbname == "" {
		dbname = cfg.User // libpq's default
	}
	vars["PGDATABASE"] = dbname
	if cfg.Password != "" {
		vars["PGPASSWORD"] = cfg.Password
	}
	if vars["PGAPPNAME"] == "" {
		vars["PGAPPNAME"] = "sap13ns-backup"
	}

	env = make([]string, 0, len(vars))
	for k, v := range vars {
		env = append(env, k+"="+v)
	}
	slices.Sort(env)
	return env, dbname, nil
}

// dsnParams returns the parameters written in a connection string, which is
// either a URL (postgres://...?sslmode=...) or keyword/value pairs
// (host=... sslmode=...). Unlike pgconn.Config, it keeps the libpq-only
// keywords such as sslmode.
func dsnParams(dsn string) (map[string]string, error) {
	m := map[string]string{}
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		// Only the query matters; the authority, which may list several
		// hosts, is left to pgconn.
		_, q, _ := strings.Cut(dsn, "?")
		q, _, _ = strings.Cut(q, "#")
		vals, err := url.ParseQuery(q)
		if err != nil {
			return nil, errors.New("parse database url: invalid query string")
		}
		for k, v := range vals {
			m[k] = v[len(v)-1]
		}
		return m, nil
	}
	isSpace := func(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v' }
	s := dsn
	for {
		s = strings.TrimLeft(s, " \t\n\r\f\v")
		if s == "" {
			return m, nil
		}
		eq := strings.IndexByte(s, '=')
		if eq < 0 {
			return nil, errors.New(`parse database url: expected keyword=value`)
		}
		key := strings.TrimSpace(s[:eq])
		s = strings.TrimLeft(s[eq+1:], " \t\n\r\f\v")
		quoted := strings.HasPrefix(s, "'")
		if quoted {
			s = s[1:]
		}
		var val strings.Builder
		i := 0
		for ; i < len(s); i++ {
			c := s[i]
			if c == '\\' && i+1 < len(s) {
				i++
				val.WriteByte(s[i])
				continue
			}
			if quoted && c == '\'' || !quoted && isSpace(c) {
				break
			}
			val.WriteByte(c)
		}
		if quoted {
			if i >= len(s) {
				return nil, errors.New("parse database url: unterminated quoted value")
			}
			i++ // closing quote
		}
		s = s[i:]
		m[key] = val.String()
	}
}

// conninfoDB is a --dbname argument that names the database only, quoted so
// that no database name can be taken for a connection string. Everything
// else, the password included, comes from the environment.
func conninfoDB(name string) string {
	return "--dbname=dbname='" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(name) + "'"
}

// tailBuffer keeps the last max bytes written to it: enough of a child's
// stderr to explain a failure, without letting it grow without bound.
type tailBuffer struct {
	max     int
	b       []byte
	dropped bool
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) >= t.max {
		t.b = append(t.b[:0], p[len(p)-t.max:]...)
		t.dropped = true
		return n, nil
	}
	if over := len(t.b) + len(p) - t.max; over > 0 {
		t.b = append(t.b[:0], t.b[over:]...)
		t.dropped = true
	}
	t.b = append(t.b, p...)
	return n, nil
}

func (t *tailBuffer) String() string {
	s := strings.TrimSpace(string(t.b))
	if t.dropped && s != "" {
		s = "[...] " + s
	}
	return s
}

const maxStderr = 8 << 10

// runTool runs pg_dump or pg_restore with a scrubbed environment and returns
// its stderr in the error when it fails.
func runTool(ctx context.Context, log *slog.Logger, bin string, args, env []string, stdout io.Writer) error {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = env
	cmd.Stdout = stdout
	stderr := &tailBuffer{max: maxStderr}
	cmd.Stderr = stderr
	cmd.WaitDelay = 10 * time.Second
	name := filepath.Base(bin)
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("%s: %w", name, ctx.Err())
		}
		if msg := stderr.String(); msg != "" {
			return fmt.Errorf("%s: %w: %s", name, err, msg)
		}
		return fmt.Errorf("%s: %w", name, err)
	}
	if msg := stderr.String(); msg != "" {
		log.Warn(name+" reported warnings", "stderr", msg)
	}
	return nil
}

// serverInfo is what backup asks PostgreSQL directly.
type serverInfo struct {
	Version   string // server_version
	Size      int64  // pg_database_size of the current database
	Relations int64  // tables, sequences, views... outside the system schemas
}

type probeFunc func(ctx context.Context, dsn string) (*serverInfo, error)

func probeServer(ctx context.Context, dsn string) (*serverInfo, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	cfg.RuntimeParams["application_name"] = "sap13ns-backup"
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(cctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	defer conn.Close(context.Background())
	var info serverInfo
	err = conn.QueryRow(cctx, `SELECT current_setting('server_version'),
		pg_database_size(current_database()),
		(SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname <> 'information_schema' AND n.nspname NOT LIKE 'pg\_%')`).
		Scan(&info.Version, &info.Size, &info.Relations)
	if err != nil {
		return nil, fmt.Errorf("query database: %w", err)
	}
	return &info, nil
}
