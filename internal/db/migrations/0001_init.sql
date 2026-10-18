-- Initial schema for sap13ns.

CREATE TABLE users (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    username        text NOT NULL UNIQUE CHECK (username ~ '^[a-z0-9][a-z0-9_.-]{2,31}$'),
    email           text NOT NULL CHECK (length(email) <= 254),
    display_name    text NOT NULL DEFAULT '' CHECK (length(display_name) <= 100),
    bio             text NOT NULL DEFAULT '' CHECK (length(bio) <= 2000),
    website         text NOT NULL DEFAULT '' CHECK (length(website) <= 300),
    role            text NOT NULL CHECK (role IN ('admin', 'editor', 'author', 'contributor', 'subscriber')),
    status          text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    password_hash   text NOT NULL,
    totp_secret     bytea,               -- AES-256-GCM encrypted with the instance secret key
    totp_enabled    boolean NOT NULL DEFAULT false,
    totp_last_step  bigint NOT NULL DEFAULT 0,
    recovery_codes  text[] NOT NULL DEFAULT '{}', -- SHA-256 hex of unused recovery codes
    failed_logins   integer NOT NULL DEFAULT 0,
    locked_until    timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    last_login_at   timestamptz
);
CREATE UNIQUE INDEX users_email_key ON users (lower(email));

CREATE TABLE sessions (
    token_hash    bytea PRIMARY KEY,     -- SHA-256 of the cookie token; the raw token is never stored
    user_id       bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    csrf_token    text NOT NULL,
    mfa_pending   boolean NOT NULL DEFAULT false,
    created_at    timestamptz NOT NULL DEFAULT now(),
    last_seen_at  timestamptz NOT NULL DEFAULT now(),
    expires_at    timestamptz NOT NULL,
    ip            inet,
    user_agent    text NOT NULL DEFAULT ''
);
CREATE INDEX sessions_user_idx ON sessions (user_id);
CREATE INDEX sessions_expires_idx ON sessions (expires_at);

CREATE TABLE password_resets (
    token_hash  bytea PRIMARY KEY,
    user_id     bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at  timestamptz NOT NULL DEFAULT now(),
    expires_at  timestamptz NOT NULL,
    used_at     timestamptz
);

CREATE TABLE media (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    uploader_id    bigint REFERENCES users (id) ON DELETE SET NULL,
    path           text NOT NULL UNIQUE,      -- relative to the uploads directory, e.g. 2026/10/ab12cd.jpg
    original_name  text NOT NULL DEFAULT '',
    mime           text NOT NULL,
    size           bigint NOT NULL,
    width          integer NOT NULL DEFAULT 0,
    height         integer NOT NULL DEFAULT 0,
    alt            text NOT NULL DEFAULT '' CHECK (length(alt) <= 500),
    caption        text NOT NULL DEFAULT '' CHECK (length(caption) <= 1000),
    variants       jsonb NOT NULL DEFAULT '{}',  -- {"thumb": {"path": ..., "width": .., "height": ..}, ...}
    sha256         text NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX media_created_idx ON media (created_at DESC);

CREATE TABLE posts (
    id                 bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    type               text NOT NULL CHECK (type IN ('post', 'page')),
    status             text NOT NULL CHECK (status IN ('draft', 'pending', 'published', 'private', 'trash')),
    title              text NOT NULL DEFAULT '' CHECK (length(title) <= 300),
    slug               text NOT NULL CHECK (slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$' AND length(slug) <= 200),
    excerpt            text NOT NULL DEFAULT '' CHECK (length(excerpt) <= 2000),
    content            text NOT NULL DEFAULT '' CHECK (length(content) <= 2000000),
    author_id          bigint REFERENCES users (id) ON DELETE SET NULL,
    parent_id          bigint REFERENCES posts (id) ON DELETE SET NULL,
    menu_order         integer NOT NULL DEFAULT 0,
    featured_media_id  bigint REFERENCES media (id) ON DELETE SET NULL,
    comment_status     text NOT NULL DEFAULT 'open' CHECK (comment_status IN ('open', 'closed')),
    meta_description   text NOT NULL DEFAULT '' CHECK (length(meta_description) <= 300),
    published_at       timestamptz,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    search             tsvector GENERATED ALWAYS AS (
        setweight(to_tsvector('simple', coalesce(title, '')), 'A') ||
        setweight(to_tsvector('simple', coalesce(excerpt, '')), 'B') ||
        setweight(to_tsvector('simple', coalesce(content, '')), 'C')
    ) STORED,
    UNIQUE (type, slug)
);
CREATE INDEX posts_public_idx ON posts (type, status, published_at DESC);
CREATE INDEX posts_author_idx ON posts (author_id);
CREATE INDEX posts_search_idx ON posts USING gin (search);

CREATE TABLE post_revisions (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    post_id     bigint NOT NULL REFERENCES posts (id) ON DELETE CASCADE,
    title       text NOT NULL,
    excerpt     text NOT NULL,
    content     text NOT NULL,
    author_id   bigint REFERENCES users (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX post_revisions_post_idx ON post_revisions (post_id, created_at DESC);

CREATE TABLE terms (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    taxonomy     text NOT NULL CHECK (taxonomy IN ('category', 'tag')),
    name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    slug         text NOT NULL CHECK (slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$' AND length(slug) <= 120),
    description  text NOT NULL DEFAULT '' CHECK (length(description) <= 1000),
    parent_id    bigint REFERENCES terms (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (taxonomy, slug)
);

CREATE TABLE post_terms (
    post_id  bigint NOT NULL REFERENCES posts (id) ON DELETE CASCADE,
    term_id  bigint NOT NULL REFERENCES terms (id) ON DELETE CASCADE,
    PRIMARY KEY (post_id, term_id)
);
CREATE INDEX post_terms_term_idx ON post_terms (term_id);

CREATE TABLE comments (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    post_id       bigint NOT NULL REFERENCES posts (id) ON DELETE CASCADE,
    parent_id     bigint REFERENCES comments (id) ON DELETE CASCADE,
    user_id       bigint REFERENCES users (id) ON DELETE SET NULL,
    author_name   text NOT NULL CHECK (length(author_name) BETWEEN 1 AND 100),
    author_email  text NOT NULL DEFAULT '' CHECK (length(author_email) <= 254),
    author_url    text NOT NULL DEFAULT '' CHECK (length(author_url) <= 300),
    content       text NOT NULL CHECK (length(content) BETWEEN 1 AND 10000),
    status        text NOT NULL CHECK (status IN ('pending', 'approved', 'spam', 'trash')),
    ip            inet,
    user_agent    text NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX comments_post_idx ON comments (post_id, status, created_at);
CREATE INDEX comments_status_idx ON comments (status, created_at DESC);

CREATE TABLE settings (
    key         text PRIMARY KEY,
    value       jsonb NOT NULL,
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE menu_items (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    location    text NOT NULL CHECK (location ~ '^[a-z0-9_-]{1,40}$'),
    label       text NOT NULL CHECK (length(label) BETWEEN 1 AND 100),
    url         text NOT NULL CHECK (length(url) <= 500),
    position    integer NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX menu_items_location_idx ON menu_items (location, position);

CREATE TABLE widgets (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    area        text NOT NULL CHECK (area ~ '^[a-z0-9_-]{1,40}$'),
    kind        text NOT NULL CHECK (length(kind) <= 200),   -- "recent_posts", "plugin:<id>:<widget>", ...
    title       text NOT NULL DEFAULT '' CHECK (length(title) <= 100),
    config      jsonb NOT NULL DEFAULT '{}',
    position    integer NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX widgets_area_idx ON widgets (area, position);

CREATE TABLE plugins (
    id                   text PRIMARY KEY CHECK (id ~ '^[a-z0-9][a-z0-9-]{1,49}$'),
    enabled              boolean NOT NULL DEFAULT false,
    wasm_sha256          text NOT NULL,      -- hash approved by an administrator
    granted_permissions  text[] NOT NULL DEFAULT '{}',
    settings             jsonb NOT NULL DEFAULT '{}',
    approved_by          bigint REFERENCES users (id) ON DELETE SET NULL,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE plugin_kv (
    plugin_id   text NOT NULL REFERENCES plugins (id) ON DELETE CASCADE,
    key         text NOT NULL CHECK (length(key) BETWEEN 1 AND 200),
    value       bytea NOT NULL,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (plugin_id, key)
);

CREATE TABLE audit_log (
    id        bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    at        timestamptz NOT NULL DEFAULT now(),
    user_id   bigint REFERENCES users (id) ON DELETE SET NULL,
    action    text NOT NULL,
    target    text NOT NULL DEFAULT '',
    ip        inet,
    details   jsonb NOT NULL DEFAULT '{}'
);
CREATE INDEX audit_log_at_idx ON audit_log (at DESC);
