// Package store contains every SQL query used by sap13ns. All queries use
// placeholders; no SQL is ever built from user input.
package store

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("already exists")
)

type Store struct {
	DB DB
}

// DB is what the store needs from PostgreSQL: a *pgxpool.Pool, or on a
// multisite install a db.Scoped that pins each statement to one site's
// schema.
type DB interface {
	querier
	Begin(ctx context.Context) (pgx.Tx, error)
	Ping(ctx context.Context) error
}

func New(db DB) *Store { return &Store{DB: db} }

// querier is satisfied by both the pool and a transaction.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return fmt.Errorf("%w: %s", ErrConflict, pg.ConstraintName)
	}
	return err
}

// InTx runs fn inside a transaction, committing when fn returns nil.
func (s *Store) InTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ipArg converts an address to a value pgx stores as inet (NULL when invalid).
func ipArg(ip netip.Addr) any {
	if !ip.IsValid() {
		return nil
	}
	return ip.String()
}

// whereBuilder accumulates AND-ed conditions with positional arguments.
type whereBuilder struct {
	conds []string
	args  []any
}

func (w *whereBuilder) add(cond string, args ...any) {
	// Conditions use "?" markers that are rewritten to $n.
	for _, a := range args {
		w.args = append(w.args, a)
		cond = strings.Replace(cond, "?", fmt.Sprintf("$%d", len(w.args)), 1)
	}
	w.conds = append(w.conds, cond)
}

func (w *whereBuilder) arg(a any) string {
	w.args = append(w.args, a)
	return fmt.Sprintf("$%d", len(w.args))
}

func (w *whereBuilder) sql() string {
	if len(w.conds) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(w.conds, " AND ")
}

// EscapeLike escapes LIKE wildcards in user input.
func EscapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// collect scans every row into T by column name.
func collect[T any](rows pgx.Rows, err error) ([]T, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[T])
}
