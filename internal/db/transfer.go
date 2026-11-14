package db

import (
	"context"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
)

// What internal/transfer needs of a migration set: moving a site to
// another install recreates its schema at exactly the migrations it had,
// loads the data, and only then lets the new binary apply what it adds.

// Versions lists the versions of the set, in order.
func (set *MigrationSet) Versions() []int {
	vs := make([]int, len(set.migs))
	for i, m := range set.migs {
		vs[i] = m.version
	}
	return vs
}

// Applied lists the versions recorded in the tracking table of the schema
// that q works on, in order; none when the table does not exist.
func (set *MigrationSet) Applied(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}) ([]int, error) {
	var exists bool
	if err := q.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, set.table).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	rows, err := q.Query(ctx, `SELECT version FROM `+pgx.Identifier{set.table}.Sanitize()+` ORDER BY version`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int])
}

// ApplyTx applies, inside tx, the migrations of versions (all of which
// the set must have) that the schema does not have yet, in order, and
// records them. A version the set does not know is an error: the binary
// is older than the schema. A transfer calls it twice: with the versions
// of the site it loads, then, once the rows are in, with every version
// of the set, so that the site is upgraded in the same transaction.
func (set *MigrationSet) ApplyTx(ctx context.Context, tx pgx.Tx, versions []int) error {
	for _, v := range versions {
		if !slices.ContainsFunc(set.migs, func(m migration) bool { return m.version == v }) {
			return fmt.Errorf("migration %04d is unknown to this version of sap13ns", v)
		}
	}
	table := pgx.Identifier{set.table}.Sanitize()
	if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS `+table+` (
		version integer PRIMARY KEY,
		name text NOT NULL,
		applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	applied, err := set.Applied(ctx, tx)
	if err != nil {
		return err
	}
	for _, m := range set.migs {
		if !slices.Contains(versions, m.version) || slices.Contains(applied, m.version) {
			continue
		}
		if _, err := tx.Exec(ctx, m.sql); err != nil {
			return fmt.Errorf("migration %s: %w", m.name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO `+table+` (version, name) VALUES ($1, $2)`, m.version, m.name); err != nil {
			return err
		}
	}
	return nil
}
