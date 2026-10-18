package backup

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// cleanPGEnv hides the developer's PG* variables and ~/.pgpass, which
// pgconn would otherwise apply as defaults.
func cleanPGEnv(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "PG") {
			t.Setenv(k, "")
		}
	}
	t.Setenv("PGPASSFILE", "/nonexistent/pgpass")
	t.Setenv("PGSERVICEFILE", "/nonexistent/pg_service.conf")
}

func envMap(env []string) map[string]string {
	m := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	return m
}

func TestPGEnv(t *testing.T) {
	cleanPGEnv(t)
	t.Setenv("SAP13NS_SECRET_KEY", "must-not-reach-pg_dump")
	tests := []struct {
		dsn    string
		dbname string
		want   map[string]string
		absent []string
	}{{
		dsn:    "postgres://app:s3cr%40t@db.example.com:6432/sap13ns?sslmode=verify-full&application_name=cms&search_path=x&pool_max_conns=4",
		dbname: "sap13ns",
		want: map[string]string{"PGHOST": "db.example.com", "PGPORT": "6432", "PGUSER": "app", "PGPASSWORD": "s3cr@t",
			"PGDATABASE": "sap13ns", "PGSSLMODE": "verify-full", "PGAPPNAME": "cms"},
		absent: []string{"SAP13NS_SECRET_KEY", "PGSEARCH_PATH"},
	}, {
		// Unix socket in a URL, as testutil uses.
		dsn:    "postgres:///sap13ns_test?host=/tmp",
		dbname: "sap13ns_test",
		want:   map[string]string{"PGHOST": "/tmp", "PGPORT": "5432", "PGDATABASE": "sap13ns_test", "PGAPPNAME": "sap13ns-backup"},
		absent: []string{"PGPASSWORD", "PGSSLMODE"},
	}, {
		dsn:    `host=/var/run/postgresql port=5433 dbname='my db' user=sap13ns password='p\'w d\\x' sslmode=disable connect_timeout=5`,
		dbname: "my db",
		want: map[string]string{"PGHOST": "/var/run/postgresql", "PGPORT": "5433", "PGDATABASE": "my db", "PGUSER": "sap13ns",
			"PGPASSWORD": `p'w d\x`, "PGSSLMODE": "disable", "PGCONNECT_TIMEOUT": "5"},
	}, {
		dsn:    "postgresql://u:pw@h1:5432,h2:5433/db?sslmode=prefer&target_session_attrs=read-write",
		dbname: "db",
		want:   map[string]string{"PGHOST": "h1,h2", "PGPORT": "5432,5433", "PGSSLMODE": "prefer", "PGTARGETSESSIONATTRS": "read-write"},
	}, {
		dsn:    "postgres://u@h/db?ssl=true",
		dbname: "db",
		want:   map[string]string{"PGSSLMODE": "require"},
	}}
	for _, tt := range tests {
		env, dbname, err := pgEnv(tt.dsn)
		if err != nil {
			t.Errorf("%s: %v", tt.dsn, err)
			continue
		}
		if dbname != tt.dbname {
			t.Errorf("%s: dbname = %q, want %q", tt.dsn, dbname, tt.dbname)
		}
		got := envMap(env)
		for k, v := range tt.want {
			if got[k] != v {
				t.Errorf("%s: %s = %q, want %q", tt.dsn, k, got[k], v)
			}
		}
		for _, k := range tt.absent {
			if _, ok := got[k]; ok {
				t.Errorf("%s: %s should not be set", tt.dsn, k)
			}
		}
		if !slices.IsSorted(env) {
			t.Errorf("%s: env is not sorted", tt.dsn)
		}
	}
}

func TestPGEnvRefusesSSLPassword(t *testing.T) {
	cleanPGEnv(t)
	if _, _, err := pgEnv("host=localhost dbname=x sslpassword=secret"); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("err = %v", err)
	}
}

func TestConninfoDB(t *testing.T) {
	for in, want := range map[string]string{
		"sap13ns":            `--dbname=dbname='sap13ns'`,
		"host=evil dbname=x": `--dbname=dbname='host=evil dbname=x'`,
		`it's\`:              `--dbname=dbname='it\'s\\'`,
	} {
		if got := conninfoDB(in); got != want {
			t.Errorf("conninfoDB(%q) = %s, want %s", in, got, want)
		}
		params, err := dsnParams(strings.TrimPrefix(conninfoDB(in), "--dbname="))
		if err != nil || len(params) != 1 || params["dbname"] != in {
			t.Errorf("conninfoDB(%q) does not parse back: %v %v", in, params, err)
		}
	}
}

func TestTailBuffer(t *testing.T) {
	b := &tailBuffer{max: 10}
	b.Write([]byte("hello "))
	if b.String() != "hello" {
		t.Fatalf("got %q", b.String())
	}
	b.Write([]byte("world, again"))
	if b.String() != "[...] rld, again" || len(b.b) > 10 {
		t.Fatalf("got %q (%d bytes)", b.String(), len(b.b))
	}
	b.Write([]byte(strings.Repeat("x", 100) + "end"))
	if len(b.b) != 10 || !strings.HasSuffix(b.String(), "xxxxxxxend") {
		t.Fatalf("got %q", b.String())
	}
}
