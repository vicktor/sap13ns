package store

import (
	"context"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"
)

type Comment struct {
	ID          int64      `db:"id"`
	PostID      int64      `db:"post_id"`
	ParentID    *int64     `db:"parent_id"`
	UserID      *int64     `db:"user_id"`
	AuthorName  string     `db:"author_name"`
	AuthorEmail string     `db:"author_email"`
	AuthorURL   string     `db:"author_url"`
	Content     string     `db:"content"`
	Status      string     `db:"status"`
	IP          netip.Addr `db:"ip"`
	UserAgent   string     `db:"user_agent"`
	CreatedAt   time.Time  `db:"created_at"`
	HeldBy      string     `db:"held_by"`     // comment.check plugin that held it or marked it as spam
	HeldReason  string     `db:"held_reason"` // its reason
	PostTitle   string     `db:"post_title"`
	PostSlug    string     `db:"post_slug"`
	// The photo and the Gravatar copy of the user who wrote it, if a
	// user did (User.Avatar, User.Gravatar).
	UserAvatar   string `db:"user_avatar"`
	UserGravatar string `db:"user_gravatar"`

	Replies []*Comment `db:"-"`
	// Gravatar is the copy of a commenter's Gravatar, which the web layer
	// fills in when the site shows them (CommenterGravatars).
	Gravatar string `db:"-"`
}

const commentSelect = `SELECT c.id, c.post_id, c.parent_id, c.user_id, c.author_name, c.author_email, c.author_url, c.content,
	c.status, coalesce(c.ip, '0.0.0.0'::inet) AS ip, c.user_agent, c.created_at, c.held_by, c.held_reason, p.title AS post_title, p.slug AS post_slug,
	coalesce(cu.avatar, '') AS user_avatar, coalesce(cu.gravatar, '') AS user_gravatar
	FROM comments c JOIN posts p ON p.id = c.post_id LEFT JOIN users cu ON cu.id = c.user_id`

type NewComment struct {
	PostID      int64
	ParentID    *int64
	UserID      *int64
	AuthorName  string
	AuthorEmail string
	AuthorURL   string
	Content     string
	Status      string
	IP          netip.Addr
	UserAgent   string
	CreatedAt   *time.Time // imports keep the original date; nil means now
	HeldBy      string     // see Comment
	HeldReason  string
}

func (s *Store) CreateComment(ctx context.Context, c NewComment) (int64, error) {
	var id int64
	err := s.DB.QueryRow(ctx, `INSERT INTO comments (post_id, parent_id, user_id, author_name, author_email, author_url, content, status, ip, user_agent, created_at, held_by, held_reason)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, coalesce($11, now()), $12, $13) RETURNING id`,
		c.PostID, c.ParentID, c.UserID, c.AuthorName, c.AuthorEmail, c.AuthorURL, c.Content, c.Status, ipArg(c.IP), c.UserAgent, c.CreatedAt,
		c.HeldBy, c.HeldReason).Scan(&id)
	return id, mapErr(err)
}

// ApprovedComments returns the approved comments of a post as a two-level tree.
func (s *Store) ApprovedComments(ctx context.Context, postID int64) ([]*Comment, error) {
	rows, err := s.DB.Query(ctx, commentSelect+` WHERE c.post_id = $1 AND c.status = 'approved' ORDER BY c.created_at ASC, c.id ASC`, postID)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToAddrOfStructByName[Comment])
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]*Comment, len(list))
	var roots []*Comment
	for _, c := range list {
		byID[c.ID] = c
	}
	for _, c := range list {
		if c.ParentID != nil {
			if parent, ok := byID[*c.ParentID]; ok {
				// Flatten deeper replies under the top-level ancestor.
				for parent.ParentID != nil {
					if pp, ok := byID[*parent.ParentID]; ok {
						parent = pp
					} else {
						break
					}
				}
				parent.Replies = append(parent.Replies, c)
				continue
			}
		}
		roots = append(roots, c)
	}
	return roots, nil
}

type CommentQuery struct {
	Status string
	PostID int64
	Limit  int
	Offset int
}

func (s *Store) ListComments(ctx context.Context, q CommentQuery) ([]Comment, int, error) {
	var w whereBuilder
	if q.Status != "" {
		w.add(`c.status = ?`, q.Status)
	} else {
		w.add(`c.status IN ('pending', 'approved')`)
	}
	if q.PostID > 0 {
		w.add(`c.post_id = ?`, q.PostID)
	}
	var total int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM comments c`+w.sql(), w.args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.DB.Query(ctx, commentSelect+w.sql()+` ORDER BY c.created_at DESC, c.id DESC LIMIT `+w.arg(q.Limit)+` OFFSET `+w.arg(q.Offset), w.args...)
	if err != nil {
		return nil, 0, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByName[Comment])
	return list, total, err
}

func (s *Store) CommentByID(ctx context.Context, id int64) (*Comment, error) {
	rows, err := s.DB.Query(ctx, commentSelect+` WHERE c.id = $1`, id)
	if err != nil {
		return nil, err
	}
	c, err := pgx.CollectExactlyOneRow(rows, pgx.RowToAddrOfStructByName[Comment])
	return c, mapErr(err)
}

func (s *Store) SetCommentStatus(ctx context.Context, id int64, status string) error {
	_, err := s.DB.Exec(ctx, `UPDATE comments SET status = $2 WHERE id = $1`, id, status)
	return err
}

func (s *Store) DeleteComment(ctx context.Context, id int64) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM comments WHERE id = $1`, id)
	return err
}

func (s *Store) CountCommentsByStatus(ctx context.Context) (map[string]int, error) {
	rows, err := s.DB.Query(ctx, `SELECT status, count(*)::int FROM comments GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[st] = n
	}
	return out, rows.Err()
}

// HasApprovedComment reports whether a commenter already has an approved
// comment, used by the "approve returning commenters" moderation mode. A
// signed-in user (userID > 0) is known by their account. A visitor is
// known by the e-mail they type, so only visitors' comments count, and
// never for the address of an account: anyone can type a staff address.
func (s *Store) HasApprovedComment(ctx context.Context, email string, userID int64) (bool, error) {
	var ok bool
	var err error
	if userID > 0 {
		err = s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM comments WHERE user_id = $1 AND status = 'approved')`, userID).Scan(&ok)
		return ok, err
	}
	err = s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM comments WHERE user_id IS NULL AND lower(author_email) = lower($1) AND status = 'approved')
		AND NOT EXISTS (SELECT 1 FROM users WHERE lower(email) = lower($1))`, email).Scan(&ok)
	return ok, err
}

func (s *Store) EmptyCommentStatus(ctx context.Context, status string) (int64, error) {
	tag, err := s.DB.Exec(ctx, `DELETE FROM comments WHERE status = $1`, status)
	return tag.RowsAffected(), err
}
