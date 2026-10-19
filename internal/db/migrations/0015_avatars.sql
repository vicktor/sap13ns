-- Profile photos (internal/web/avatars.go). A user's uploaded photo and the
-- copy of their Gravatar live in the site's uploads, under avatars/, as
-- square images in three sizes; a column holds the name of the set without
-- its size ("avatars/u12-<random>.jpg" is avatars/u12-<random>-64.jpg and
-- its siblings). The server fetches Gravatars itself, so visitors' browsers
-- never contact Gravatar. Runs in every site schema of a network, and in
-- public on a single-site install.
ALTER TABLE users
    ADD COLUMN avatar text NOT NULL DEFAULT ''
        CHECK (avatar = '' OR avatar ~ '^avatars/u[0-9]+-[0-9a-f]{20}\.(jpg|png)$'),
    ADD COLUMN gravatar text NOT NULL DEFAULT ''
        CHECK (gravatar = '' OR gravatar ~ '^avatars/g[0-9]+-[0-9a-f]{20}\.(jpg|png)$'),
    -- When the Gravatar of the user's e-mail address was last looked up
    -- (NULL: never, or the address changed since).
    ADD COLUMN gravatar_checked_at timestamptz;

-- The Gravatars of commenters, when the site shows them (Settings →
-- Discussion): one row per SHA-256 of a trimmed, lower-cased e-mail
-- address, with the copy found ('' when Gravatar has none). The cache is
-- bounded in rows and bytes: the least recently shown go first.
CREATE TABLE gravatar_cache (
    hash        text PRIMARY KEY CHECK (hash ~ '^[0-9a-f]{64}$'),
    path        text NOT NULL DEFAULT ''
        CHECK (path = '' OR path ~ '^avatars/c-[0-9a-f]{20}\.(jpg|png)$'),
    bytes       bigint NOT NULL DEFAULT 0 CHECK (bytes >= 0),
    checked_at  timestamptz NOT NULL DEFAULT now(),
    used_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX gravatar_cache_used_at ON gravatar_cache (used_at);
