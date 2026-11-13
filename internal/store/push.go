package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// PushSubscription is a browser that asked for notifications of new posts.
type PushSubscription struct {
	ID       int64
	Endpoint string
	P256dh   []byte
	Auth     []byte
	Lang     string
}

// MaxPushSubscriptions is how many browsers a site keeps: a new one past
// it is refused.
const MaxPushSubscriptions = 20000

// SavePushSubscription keeps a subscription, or refreshes the keys and the
// language of one the site already has. It reports false when the site is
// full.
func (s *Store) SavePushSubscription(ctx context.Context, sub PushSubscription) (bool, error) {
	tag, err := s.DB.Exec(ctx, `INSERT INTO push_subscriptions (endpoint, p256dh, auth, lang)
		SELECT $1, $2, $3, $4 WHERE (SELECT count(*) FROM push_subscriptions) < $5
			OR EXISTS (SELECT 1 FROM push_subscriptions WHERE endpoint = $1)
		ON CONFLICT (endpoint) DO UPDATE SET p256dh = EXCLUDED.p256dh, auth = EXCLUDED.auth, lang = EXCLUDED.lang`,
		sub.Endpoint, sub.P256dh, sub.Auth, sub.Lang, MaxPushSubscriptions)
	return err == nil && tag.RowsAffected() > 0, err
}

// DeletePushSubscription forgets a subscription by its endpoint.
func (s *Store) DeletePushSubscription(ctx context.Context, endpoint string) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM push_subscriptions WHERE endpoint = $1`, endpoint)
	return err
}

// PushSubscriptions lists the subscriptions after id, at most limit, in
// the order they were made. With langs, only those of these languages.
func (s *Store) PushSubscriptions(ctx context.Context, after int64, limit int, langs []string) ([]PushSubscription, error) {
	rows, err := s.DB.Query(ctx, `SELECT id, endpoint, p256dh, auth, lang FROM push_subscriptions
		WHERE id > $1 AND ($3::text[] IS NULL OR lang = ANY($3)) ORDER BY id LIMIT $2`, after, limit, langs)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (PushSubscription, error) {
		var p PushSubscription
		err := row.Scan(&p.ID, &p.Endpoint, &p.P256dh, &p.Auth, &p.Lang)
		return p, err
	})
}

// CountPushSubscriptions is how many browsers are subscribed.
func (s *Store) CountPushSubscriptions(ctx context.Context) (int, error) {
	var n int
	err := s.DB.QueryRow(ctx, `SELECT count(*)::int FROM push_subscriptions`).Scan(&n)
	return n, err
}

// PushKeys returns the site's VAPID keys (the private one still
// encrypted), nil when it has none yet.
func (s *Store) PushKeys(ctx context.Context) (privateEnc, public []byte, err error) {
	err = s.DB.QueryRow(ctx, `SELECT private_enc, public FROM push_keys`).Scan(&privateEnc, &public)
	if err == pgx.ErrNoRows {
		return nil, nil, nil
	}
	return privateEnc, public, err
}

// InitPushKeys keeps a first key pair; when the site got one meanwhile,
// that one stays. Read them back with PushKeys.
func (s *Store) InitPushKeys(ctx context.Context, privateEnc, public []byte) error {
	_, err := s.DB.Exec(ctx, `INSERT INTO push_keys (private_enc, public) VALUES ($1, $2) ON CONFLICT (id) DO NOTHING`, privateEnc, public)
	return err
}

// HasPublishedPosts reports whether the site has a post that is public now.
func (s *Store) HasPublishedPosts(ctx context.Context) (bool, error) {
	var ok bool
	err := s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM posts p WHERE p.type = 'post' AND `+publicCond+`)`).Scan(&ok)
	return ok, err
}
