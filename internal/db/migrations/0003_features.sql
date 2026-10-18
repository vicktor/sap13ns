-- Passkeys, redirects, autosave and edit locks, AI usage and cookie-less
-- analytics.

-- WebAuthn credentials. data holds the go-webauthn Credential (public key,
-- flags, authenticator data) as JSON.
CREATE TABLE passkeys (
    id            bigserial PRIMARY KEY,
    user_id       bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    credential_id bytea NOT NULL UNIQUE,
    name          text NOT NULL DEFAULT '' CHECK (length(name) <= 100),
    data          jsonb NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    last_used_at  timestamptz
);
CREATE INDEX passkeys_user_idx ON passkeys (user_id);

-- Redirects from old paths (as visitors see them, language prefix
-- included). auto marks the ones created when a published slug changed.
CREATE TABLE redirects (
    id          bigserial PRIMARY KEY,
    source      text NOT NULL UNIQUE CHECK (source LIKE '/%' AND length(source) <= 1000),
    target      text NOT NULL CHECK (length(target) <= 2000),
    status      smallint NOT NULL DEFAULT 301 CHECK (status IN (301, 302, 307, 308, 410)),
    auto        boolean NOT NULL DEFAULT false,
    hits        bigint NOT NULL DEFAULT 0,
    last_hit_at timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- The latest unsaved editor state, one per post and user.
CREATE TABLE post_autosaves (
    post_id  bigint NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
    user_id  bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title    text NOT NULL,
    content  text NOT NULL,
    excerpt  text NOT NULL,
    saved_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (post_id, user_id)
);

-- Soft edit locks, kept alive by the editor's heartbeat.
CREATE TABLE post_locks (
    post_id    bigint PRIMARY KEY REFERENCES posts(id) ON DELETE CASCADE,
    user_id    bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL
);

-- AI assistant usage, for budgets and the usage report.
CREATE TABLE ai_usage (
    month         date NOT NULL,
    user_id       bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    requests      integer NOT NULL DEFAULT 0,
    input_tokens  bigint NOT NULL DEFAULT 0,
    output_tokens bigint NOT NULL DEFAULT 0,
    images        integer NOT NULL DEFAULT 0,
    PRIMARY KEY (month, user_id)
);

-- Cookie-less analytics: daily aggregates only. Visitors are counted with
-- a salted hash that rotates every day and is never stored.
CREATE TABLE analytics_days (
    day      date PRIMARY KEY,
    views    integer NOT NULL DEFAULT 0,
    visitors integer NOT NULL DEFAULT 0
);
CREATE TABLE analytics_pages (
    day   date NOT NULL,
    path  text NOT NULL,
    views integer NOT NULL DEFAULT 0,
    PRIMARY KEY (day, path)
);
-- dim is one of: referrer, browser, os, device, lang.
CREATE TABLE analytics_dims (
    day   date NOT NULL,
    dim   text NOT NULL,
    value text NOT NULL,
    views integer NOT NULL DEFAULT 0,
    PRIMARY KEY (day, dim, value)
);
