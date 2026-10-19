-- OAuth connections of plugins (docs/PLUGINS.md, OAuth connections). A
-- plugin declares its providers in plugin.json; the core runs the
-- authorization with the administrator and keeps the tokens here, sealed
-- with the site's key (AES-256-GCM, bound to the plugin and the provider).
-- Plugins only ever get a valid access token through the oauth_token host
-- function. Site backups and exports never read these tables.

CREATE TABLE plugin_oauth (
    plugin_id          text NOT NULL REFERENCES plugins (id) ON DELETE CASCADE,
    provider           text NOT NULL CHECK (provider ~ '^[a-z][a-z0-9_-]{0,39}$'),
    tokens             text NOT NULL CHECK (length(tokens) <= 65536), -- sealed {"a": access, "r": refresh}
    account_id         text NOT NULL DEFAULT '' CHECK (length(account_id) <= 200),
    account_name       text NOT NULL DEFAULT '' CHECK (length(account_name) <= 200),
    scopes             text NOT NULL DEFAULT '' CHECK (length(scopes) <= 2000),
    expires_at         timestamptz,          -- of the access token; NULL: no lifetime given
    refresh_expires_at timestamptz,          -- of the refresh token, when the provider says
    refreshable        boolean NOT NULL DEFAULT false,
    connected_by       bigint REFERENCES users (id) ON DELETE SET NULL,
    connected_at       timestamptz NOT NULL DEFAULT now(),
    refreshed_at       timestamptz,
    refresh_error      text NOT NULL DEFAULT '' CHECK (length(refresh_error) <= 300),
    PRIMARY KEY (plugin_id, provider)
);

-- Authorizations in progress: one row per Connect, removed by its callback
-- (single use) or once expired. The state itself is never stored, only its
-- SHA-256, and the row is bound to the session that started it.
CREATE TABLE plugin_oauth_states (
    state_hash   bytea PRIMARY KEY,
    plugin_id    text NOT NULL REFERENCES plugins (id) ON DELETE CASCADE,
    provider     text NOT NULL CHECK (provider ~ '^[a-z][a-z0-9_-]{0,39}$'),
    session_hash bytea NOT NULL REFERENCES sessions (token_hash) ON DELETE CASCADE,
    user_id      bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    verifier     text NOT NULL DEFAULT '' CHECK (length(verifier) <= 1000), -- sealed PKCE verifier
    redirect_uri text NOT NULL CHECK (length(redirect_uri) <= 2048),
    expires_at   timestamptz NOT NULL
);
CREATE INDEX plugin_oauth_states_expires_idx ON plugin_oauth_states (expires_at);
