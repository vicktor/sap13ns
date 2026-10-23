package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type MenuItem struct {
	ID        int64     `db:"id"`
	Location  string    `db:"location"`
	Lang      string    `db:"lang"` // "" = every language without its own menu
	Label     string    `db:"label"`
	URL       string    `db:"url"`
	Position  int       `db:"position"`
	CreatedAt time.Time `db:"created_at"`
}

// MenuKey names a menu: its location, plus "@lang" for a language's own menu.
func MenuKey(location, lang string) string {
	if lang == "" {
		return location
	}
	return location + "@" + lang
}

// AllMenus returns every menu keyed by MenuKey.
func (s *Store) AllMenus(ctx context.Context) (map[string][]MenuItem, error) {
	rows, err := s.DB.Query(ctx, `SELECT id, location, lang, label, url, position, created_at FROM menu_items ORDER BY location, lang, position, id`)
	if err != nil {
		return nil, err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByName[MenuItem])
	if err != nil {
		return nil, err
	}
	out := map[string][]MenuItem{}
	for _, it := range items {
		k := MenuKey(it.Location, it.Lang)
		out[k] = append(out[k], it)
	}
	return out, nil
}

func (s *Store) AddMenuItem(ctx context.Context, location, lang, label, url string) error {
	_, err := s.DB.Exec(ctx, `INSERT INTO menu_items (location, lang, label, url, position)
		VALUES ($1, $2, $3, $4, coalesce((SELECT max(position) + 1 FROM menu_items WHERE location = $1 AND lang = $2), 0))`,
		location, lang, label, url)
	return mapErr(err)
}

func (s *Store) UpdateMenuItem(ctx context.Context, id int64, label, url string) error {
	_, err := s.DB.Exec(ctx, `UPDATE menu_items SET label = $2, url = $3 WHERE id = $1`, id, label, url)
	return err
}

func (s *Store) DeleteMenuItem(ctx context.Context, id int64) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM menu_items WHERE id = $1`, id)
	return err
}

// ErrOrder: the order sent is not that of one whole group (one menu, or
// the widgets of one area).
var ErrOrder = errors.New("store: not the rows of one group")

// ReorderMenu puts the items of one menu in the order of ids, which must
// be every item of that menu (one location and language), each once.
func (s *Store) ReorderMenu(ctx context.Context, ids []int64) error {
	return s.InTx(ctx, func(tx pgx.Tx) error {
		var loc, lang string
		if len(ids) > 0 {
			if err := tx.QueryRow(ctx, `SELECT location, lang FROM menu_items WHERE id = $1`, ids[0]).Scan(&loc, &lang); err != nil {
				return mapErr(err)
			}
		}
		return reorderRows(ctx, tx, "menu_items", "location = $1 AND lang = $2", []any{loc, lang}, ids)
	})
}

// reorderRows numbers the rows of one group in the order of ids, which must
// be every row of the group, each once. table and where are compile-time
// constants, never user input.
func reorderRows(ctx context.Context, tx pgx.Tx, table, where string, args []any, ids []int64) error {
	if len(ids) == 0 {
		return ErrOrder
	}
	rows, err := tx.Query(ctx, `SELECT id FROM `+table+` WHERE `+where+` FOR UPDATE`, args...)
	if err != nil {
		return err
	}
	have, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	if len(have) != len(ids) {
		return ErrOrder
	}
	seen := make(map[int64]bool, len(ids))
	for _, id := range have {
		seen[id] = false
	}
	for _, id := range ids {
		if done, ok := seen[id]; !ok || done {
			return ErrOrder
		}
		seen[id] = true
	}
	for pos, id := range ids {
		if _, err := tx.Exec(ctx, `UPDATE `+table+` SET position = $2 WHERE id = $1`, id, pos); err != nil {
			return err
		}
	}
	return nil
}

type Widget struct {
	ID        int64             `db:"id"`
	Area      string            `db:"area"`
	Lang      string            `db:"lang"` // "" = every language
	Kind      string            `db:"kind"`
	Title     string            `db:"title"`
	Config    map[string]string `db:"config"`
	Position  int               `db:"position"`
	CreatedAt time.Time         `db:"created_at"`
}

func (s *Store) AllWidgets(ctx context.Context) (map[string][]Widget, error) {
	rows, err := s.DB.Query(ctx, `SELECT id, area, lang, kind, title, config, position, created_at FROM widgets ORDER BY area, position, id`)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByName[Widget])
	if err != nil {
		return nil, err
	}
	out := map[string][]Widget{}
	for _, w := range list {
		out[w.Area] = append(out[w.Area], w)
	}
	return out, nil
}

func (s *Store) AddWidget(ctx context.Context, area, lang, kind, title string, config map[string]string) error {
	if config == nil {
		config = map[string]string{}
	}
	raw, _ := json.Marshal(config)
	_, err := s.DB.Exec(ctx, `INSERT INTO widgets (area, lang, kind, title, config, position)
		VALUES ($1, $2, $3, $4, $5, coalesce((SELECT max(position) + 1 FROM widgets WHERE area = $1), 0))`, area, lang, kind, title, raw)
	return err
}

func (s *Store) UpdateWidget(ctx context.Context, id int64, lang, title string, config map[string]string) error {
	raw, _ := json.Marshal(config)
	_, err := s.DB.Exec(ctx, `UPDATE widgets SET title = $2, config = $3, lang = $4 WHERE id = $1`, id, title, raw, lang)
	return err
}

func (s *Store) DeleteWidget(ctx context.Context, id int64) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM widgets WHERE id = $1`, id)
	return err
}

// ReorderWidgets puts the widgets of one area in the order of ids, which
// must be every widget of that area, each once.
func (s *Store) ReorderWidgets(ctx context.Context, ids []int64) error {
	return s.InTx(ctx, func(tx pgx.Tx) error {
		var area string
		if len(ids) > 0 {
			if err := tx.QueryRow(ctx, `SELECT area FROM widgets WHERE id = $1`, ids[0]).Scan(&area); err != nil {
				return mapErr(err)
			}
		}
		return reorderRows(ctx, tx, "widgets", "area = $1", []any{area}, ids)
	})
}
