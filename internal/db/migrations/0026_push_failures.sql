-- A subscription whose push service keeps refusing it is forgotten after
-- a few posts in a row (store.MaxPushFailures); a delivery starts the
-- count again. Without it, addresses nobody listens on stayed for good
-- and could fill the site's room for subscriptions.
ALTER TABLE push_subscriptions ADD COLUMN failures smallint NOT NULL DEFAULT 0;

-- A post is notified once, however many times it is published again.
-- Those already live count as notified.
ALTER TABLE posts ADD COLUMN pushed_at timestamptz;
UPDATE posts SET pushed_at = published_at
    WHERE type = 'post' AND status = 'published' AND NOT announce_pending;
