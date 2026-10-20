-- Personal access tokens: a user's key for an AI agent (Claude Code, an
-- MCP client) to work on the site as that user, through /admin/mcp. Only
-- the SHA-256 of a token is kept; it is shown once, when created.
CREATE TABLE user_tokens (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id      bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
    token_hash   bytea NOT NULL UNIQUE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz,
    expires_at   timestamptz
);
CREATE INDEX user_tokens_user ON user_tokens (user_id);
