package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

type Term struct {
	ID          int64     `db:"id"`
	Taxonomy    string    `db:"taxonomy"`
	Name        string    `db:"name"`
	Slug        string    `db:"slug"`
	Description string    `db:"description"`
	ParentID    *int64    `db:"parent_id"`
	CreatedAt   time.Time `db:"created_at"`
	Count       int       `db:"count"` // public posts using the term

	// I18n holds the name and description in the site's other languages.
	I18n map[string]TermText `db:"i18n"`
}

type TermText struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// In returns the term with its name and description in lang, when translated.
func (t Term) In(lang string) Term {
	if tr, ok := t.I18n[lang]; ok {
		if tr.Name != "" {
			t.Name = tr.Name
		}
		if tr.Description != "" {
			t.Description = tr.Description
		}
	}
	return t
}

// termSelect counts public posts; with a language argument ($2 in every
// caller) only posts in that language are counted ("" counts them all).
const termSelect = `SELECT t.id, t.taxonomy, t.name, t.slug, t.description, t.parent_id, t.created_at, t.i18n,
	(SELECT count(*) FROM post_terms pt JOIN posts p ON p.id = pt.post_id
	 WHERE pt.term_id = t.id AND p.status = 'published' AND p.published_at <= now() AND ($2 = '' OR p.lang = $2))::int AS count
	FROM terms t`

// ListTerms lists a taxonomy, counting public posts in lang ("" = all).
func (s *Store) ListTerms(ctx context.Context, taxonomy, lang string) ([]Term, error) {
	rows, err := s.DB.Query(ctx, termSelect+` WHERE t.taxonomy = $1 ORDER BY lower(t.name)`, taxonomy, lang)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[Term])
}

// PopularTerms returns the terms with most public posts.
func (s *Store) PopularTerms(ctx context.Context, taxonomy, lang string, limit int) ([]Term, error) {
	rows, err := s.DB.Query(ctx, `SELECT * FROM (`+termSelect+` WHERE t.taxonomy = $1) x WHERE count > 0 ORDER BY count DESC, lower(name) LIMIT $3`, taxonomy, lang, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[Term])
}

func (s *Store) TermByID(ctx context.Context, id int64) (*Term, error) {
	rows, err := s.DB.Query(ctx, termSelect+` WHERE t.id = $1`, id, "")
	if err != nil {
		return nil, err
	}
	t, err := pgx.CollectExactlyOneRow(rows, pgx.RowToAddrOfStructByName[Term])
	return t, mapErr(err)
}

func (s *Store) TermBySlug(ctx context.Context, taxonomy, slug string) (*Term, error) {
	rows, err := s.DB.Query(ctx, termSelect+` WHERE t.taxonomy = $1 AND t.slug = $3`, taxonomy, "", slug)
	if err != nil {
		return nil, err
	}
	t, err := pgx.CollectExactlyOneRow(rows, pgx.RowToAddrOfStructByName[Term])
	return t, mapErr(err)
}

type TermInput struct {
	Taxonomy    string
	Name        string
	Slug        string
	Description string
	ParentID    *int64
	I18n        map[string]TermText
}

func (s *Store) CreateTerm(ctx context.Context, in TermInput) (int64, error) {
	var id int64
	err := s.DB.QueryRow(ctx, `INSERT INTO terms (taxonomy, name, slug, description, parent_id, i18n) VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		in.Taxonomy, in.Name, in.Slug, in.Description, in.ParentID, termI18n(in.I18n)).Scan(&id)
	return id, mapErr(err)
}

func (s *Store) UpdateTerm(ctx context.Context, id int64, in TermInput) error {
	_, err := s.DB.Exec(ctx, `UPDATE terms SET name = $2, slug = $3, description = $4, parent_id = $5, i18n = $6 WHERE id = $1`,
		id, in.Name, in.Slug, in.Description, in.ParentID, termI18n(in.I18n))
	return mapErr(err)
}

func termI18n(m map[string]TermText) map[string]TermText {
	if m == nil {
		return map[string]TermText{}
	}
	return m
}

func (s *Store) DeleteTerm(ctx context.Context, id int64) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM terms WHERE id = $1`, id)
	return err
}

// EnsureTags returns the ids of tags with the given names, creating missing ones.
func (s *Store) EnsureTags(ctx context.Context, names, slugs []string) ([]int64, error) {
	ids := make([]int64, 0, len(names))
	for i := range names {
		var id int64
		err := s.DB.QueryRow(ctx, `INSERT INTO terms (taxonomy, name, slug) VALUES ('tag', $1, $2)
			ON CONFLICT (taxonomy, slug) DO UPDATE SET slug = EXCLUDED.slug RETURNING id`, names[i], slugs[i]).Scan(&id)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}
