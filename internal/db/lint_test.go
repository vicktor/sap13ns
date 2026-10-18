package db

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// A Scoped handle decides the schema, whatever the caller's context says.
func TestScopedOverridesContext(t *testing.T) {
	s := &Scoped{schema: "site_1"}
	ctx := withSchema(context.Background(), "site_2")
	if got := s.ctx(ctx).Value(schemaKey{}); got != "site_1" {
		t.Fatalf("schema = %v, want site_1", got)
	}
}

// sessionSQL matches SQL that changes session state, which on a shared
// connection would outlive the statement and reach the next site: the
// search_path itself, the role, a session-level SET, RESET, DISCARD and
// temporary tables (pg_temp is searched for tables). SET LOCAL ends with
// its transaction and is allowed.
var sessionSQL = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bsearch_path\b`),
	regexp.MustCompile(`(?i)\bset_config\b`),
	regexp.MustCompile(`(?i)(^|;)\s*set\s+(session\s+|local\s+)?role\b`),
	regexp.MustCompile(`(?i)\bsession\s+authorization\b`),
	regexp.MustCompile(`(?i)\breset\s+(all|role|session)\b`),
	regexp.MustCompile(`(?i)\bdiscard\b`),
	regexp.MustCompile(`(?i)\btemp(orary)?\s+(table|view|sequence)\b`),
	regexp.MustCompile(`(?i)(^|;)\s*set\s+(session\s+)?([a-z_.]+\s*(=|\bto\b)|time\s+zone\b)`),
}

func badSessionSQL(sql string) string {
	for _, re := range sessionSQL {
		if m := re.FindString(sql); m != "" {
			return strings.TrimSpace(m)
		}
	}
	return ""
}

func TestSessionSQLPatterns(t *testing.T) {
	for _, bad := range []string{
		`SET search_path TO site_2`, `SELECT pg_catalog.set_config('x', 'y', false)`, `SET ROLE admin`,
		`SET SESSION AUTHORIZATION bob`, `RESET ALL`, `discard all`, `CREATE TEMP TABLE t (id int)`,
		`CREATE TEMPORARY TABLE t (id int)`, `SET statement_timeout = 0`, `SELECT 1; SET work_mem TO '1GB'`,
		`SET SESSION statement_timeout = 0`, `SET TIME ZONE 'UTC'`,
	} {
		if badSessionSQL(bad) == "" {
			t.Errorf("not caught: %s", bad)
		}
	}
	for _, ok := range []string{
		`SET LOCAL statement_timeout = '120s'`, `UPDATE users SET bio = $1 WHERE id = $2`,
		"UPDATE posts\n\tSET status = 'published'", `DELETE FROM password_resets WHERE used_at IS NOT NULL`,
		`INSERT INTO settings (key, value) VALUES ($1, $2) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`,
	} {
		if m := badSessionSQL(ok); m != "" {
			t.Errorf("false positive %q in: %s", m, ok)
		}
	}
}

// sqlDirs hold every query and migration that runs on a site's connection.
var (
	sqlPackages   = []string{"../store", "../analytics"}
	migrationDirs = []string{"migrations"}
)

// 6. No SQL that runs on a shared connection changes session state.
func TestNoSessionStateInSQL(t *testing.T) {
	checked := 0
	for _, dir := range sqlPackages {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range files {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			// A query is often a concatenation (`UPDATE ` + table + ` SET
			// ...`), so a chain of + is checked as one string, with "_"
			// for the operands that are not literals.
			var visit func(ast.Node) bool
			visit = func(n ast.Node) bool {
				var e ast.Expr
				switch n := n.(type) {
				case *ast.BinaryExpr:
					if n.Op != token.ADD {
						return true
					}
					e = n
				case *ast.BasicLit:
					if n.Kind != token.STRING {
						return true
					}
					e = n
				default:
					return true
				}
				var b strings.Builder
				var others []ast.Expr
				lits := 0
				var flatten func(ast.Expr)
				flatten = func(e ast.Expr) {
					switch e := e.(type) {
					case *ast.BinaryExpr:
						if e.Op == token.ADD {
							flatten(e.X)
							flatten(e.Y)
							return
						}
					case *ast.ParenExpr:
						flatten(e.X)
						return
					case *ast.BasicLit:
						if e.Kind == token.STRING {
							s, err := strconv.Unquote(e.Value)
							if err != nil {
								t.Fatalf("%s: %v", fset.Position(e.Pos()), err)
							}
							b.WriteString(s)
							lits++
							return
						}
						b.WriteString("_")
						return
					}
					b.WriteString("_")
					others = append(others, e)
				}
				flatten(e)
				if lits > 0 {
					if m := badSessionSQL(b.String()); m != "" {
						t.Errorf("%s: %q changes session state on a shared connection", fset.Position(e.Pos()), m)
					}
					checked++
				}
				for _, o := range others {
					ast.Inspect(o, visit)
				}
				return false
			}
			ast.Inspect(f, visit)
		}
	}
	comment := regexp.MustCompile(`--[^\n]*`)
	for _, dir := range migrationDirs {
		files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
		if err != nil {
			t.Fatal(err)
		}
		if len(files) == 0 {
			t.Fatalf("no migrations in %s", dir)
		}
		for _, path := range files {
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if m := badSessionSQL(comment.ReplaceAllString(string(b), "")); m != "" {
				t.Errorf("%s: %q changes session state on a shared connection", path, m)
			}
			checked++
		}
	}
	if checked < 100 {
		t.Fatalf("only %d strings checked; are the paths right?", checked)
	}
}
