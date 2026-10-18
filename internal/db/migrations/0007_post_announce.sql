-- post.published fires once per publication. A post published with a date
-- in the future is marked here, and the server announces it (plugins,
-- WebSub) when the date arrives.
ALTER TABLE posts ADD COLUMN announce_pending boolean NOT NULL DEFAULT false;
CREATE INDEX posts_announce_idx ON posts (published_at) WHERE announce_pending;
