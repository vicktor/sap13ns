package store

import (
	"context"
	"path"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type MediaVariant struct {
	Path   string `json:"path"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

type Media struct {
	ID           int64                   `db:"id"`
	UploaderID   *int64                  `db:"uploader_id"`
	Path         string                  `db:"path"`
	OriginalName string                  `db:"original_name"`
	MIME         string                  `db:"mime"`
	Size         int64                   `db:"size"`
	Width        int                     `db:"width"`
	Height       int                     `db:"height"`
	Alt          string                  `db:"alt"`
	Caption      string                  `db:"caption"`
	Variants     map[string]MediaVariant `db:"variants"`
	SHA256       string                  `db:"sha256"`
	CreatedAt    time.Time               `db:"created_at"`
}

func (m *Media) IsImage() bool { return strings.HasPrefix(m.MIME, "image/") }

// URL returns the public path of the original file.
func (m *Media) URL() string { return "/uploads/" + m.Path }

// VariantURL returns the path of a resized variant, falling back to the original.
func (m *Media) VariantURL(name string) string {
	if v, ok := m.Variants[name]; ok {
		return "/uploads/" + v.Path
	}
	return m.URL()
}

// Srcset builds a srcset attribute value from the available variants.
func (m *Media) Srcset() string {
	var parts []string
	for _, name := range []string{"thumb", "medium", "large"} {
		if v, ok := m.Variants[name]; ok && v.Width > 0 {
			parts = append(parts, "/uploads/"+v.Path+" "+itoa(v.Width)+"w")
		}
	}
	if m.Width > 0 {
		parts = append(parts, m.URL()+" "+itoa(m.Width)+"w")
	}
	return strings.Join(parts, ", ")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

const mediaCols = `id, uploader_id, path, original_name, mime, size, width, height, alt, caption, variants, sha256, created_at`

func (s *Store) CreateMedia(ctx context.Context, m *Media) (int64, error) {
	var id int64
	err := s.DB.QueryRow(ctx, `INSERT INTO media (uploader_id, path, original_name, mime, size, width, height, alt, caption, variants, sha256)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING id`,
		m.UploaderID, m.Path, m.OriginalName, m.MIME, m.Size, m.Width, m.Height, m.Alt, m.Caption, m.Variants, m.SHA256).Scan(&id)
	return id, mapErr(err)
}

func (s *Store) MediaByID(ctx context.Context, id int64) (*Media, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+mediaCols+` FROM media WHERE id = $1`, id)
	if err != nil {
		return nil, err
	}
	m, err := pgx.CollectExactlyOneRow(rows, pgx.RowToAddrOfStructByName[Media])
	return m, mapErr(err)
}

func (s *Store) MediaByIDs(ctx context.Context, ids []int64) (map[int64]*Media, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+mediaCols+` FROM media WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToAddrOfStructByName[Media])
	if err != nil {
		return nil, err
	}
	out := make(map[int64]*Media, len(list))
	for _, m := range list {
		out[m.ID] = m
	}
	return out, nil
}

// OriginalPath is the path of the original file behind a variant's path
// (base-medium.jpg → base.jpg); other paths are returned as they are.
func OriginalPath(p string) string {
	ext := path.Ext(p)
	stem := strings.TrimSuffix(p, ext)
	for _, v := range []string{"-thumb", "-medium", "-large"} {
		if s, ok := strings.CutSuffix(stem, v); ok {
			return s + ext
		}
	}
	return p
}

// MediaByPaths finds media items by the path of their original or of one
// of their variants (below /uploads/), keyed by the path asked for.
func (s *Store) MediaByPaths(ctx context.Context, paths []string) (map[string]*Media, error) {
	if len(paths) == 0 {
		return map[string]*Media{}, nil
	}
	originals := make([]string, 0, len(paths))
	for _, p := range paths {
		originals = append(originals, OriginalPath(p))
	}
	rows, err := s.DB.Query(ctx, `SELECT `+mediaCols+` FROM media WHERE path = ANY($1)`, originals)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToAddrOfStructByName[Media])
	if err != nil {
		return nil, err
	}
	byPath := make(map[string]*Media, len(list))
	for _, m := range list {
		byPath[m.Path] = m
	}
	out := make(map[string]*Media, len(paths))
	for _, p := range paths {
		m := byPath[OriginalPath(p)]
		if m == nil {
			continue
		}
		if p == m.Path {
			out[p] = m
			continue
		}
		for _, v := range m.Variants {
			if v.Path == p {
				out[p] = m
				break
			}
		}
	}
	return out, nil
}

type MediaQuery struct {
	UploaderID int64
	ImagesOnly bool
	MIME       string // exact type, such as font/woff2
	Search     string
	Limit      int
	Offset     int
}

func (s *Store) ListMedia(ctx context.Context, q MediaQuery) ([]Media, int, error) {
	var w whereBuilder
	if q.UploaderID > 0 {
		w.add(`uploader_id = ?`, q.UploaderID)
	}
	if q.ImagesOnly {
		w.add(`mime LIKE 'image/%'`)
	}
	if q.MIME != "" {
		w.add(`mime = ?`, q.MIME)
	}
	if q.Search != "" {
		p := "%" + EscapeLike(q.Search) + "%"
		w.add(`(original_name ILIKE ? OR alt ILIKE ?)`, p, p)
	}
	var total int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM media`+w.sql(), w.args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.DB.Query(ctx, `SELECT `+mediaCols+` FROM media`+w.sql()+` ORDER BY created_at DESC, id DESC LIMIT `+w.arg(q.Limit)+` OFFSET `+w.arg(q.Offset), w.args...)
	if err != nil {
		return nil, 0, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByName[Media])
	return list, total, err
}

func (s *Store) UpdateMediaText(ctx context.Context, id int64, alt, caption string) error {
	_, err := s.DB.Exec(ctx, `UPDATE media SET alt = $2, caption = $3 WHERE id = $1`, id, alt, caption)
	return err
}

func (s *Store) DeleteMedia(ctx context.Context, id int64) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM media WHERE id = $1`, id)
	return err
}

func (s *Store) MediaBySHA(ctx context.Context, sha string) (*Media, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+mediaCols+` FROM media WHERE sha256 = $1 LIMIT 1`, sha)
	if err != nil {
		return nil, err
	}
	m, err := pgx.CollectExactlyOneRow(rows, pgx.RowToAddrOfStructByName[Media])
	return m, mapErr(err)
}
