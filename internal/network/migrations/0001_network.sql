-- The network schema: the control plane of a multisite install. It also
-- holds the site migration set, for the network portal, which is itself a
-- site; users here are the network accounts. This set is tracked in
-- network_migrations and runs after the site set.

-- What a site may use. sites_per_grant is the number of sites one
-- subscription (or one free grant) covers; the other limits apply per site.
CREATE TABLE plans (
    slug             text PRIMARY KEY CHECK (slug ~ '^[a-z][a-z0-9_]{1,31}$'),
    name             text NOT NULL,
    paid             boolean NOT NULL,
    hand_approved    boolean NOT NULL DEFAULT false,  -- a network admin approves each new site
    commercial       boolean NOT NULL DEFAULT false,
    sites_per_grant  integer NOT NULL CHECK (sites_per_grant >= 1),
    storage_bytes    bigint NOT NULL,
    custom_domains   integer NOT NULL DEFAULT 0,
    plugin_uploads   boolean NOT NULL DEFAULT false,
    theme_uploads    boolean NOT NULL DEFAULT false,
    position         integer NOT NULL DEFAULT 0
);
-- The plans of the corporate site's pricing table (internal/web/render.go).
-- Two custom domains per site: an apex and its www.
INSERT INTO plans (slug, name, paid, hand_approved, commercial, sites_per_grant, storage_bytes,
                   custom_domains, plugin_uploads, theme_uploads, position) VALUES
    ('personal', 'Personal', false, true,  false, 1,  1073741824, 0, false, false, 1),
    ('pro',      'Pro',      true,  false, true,  1, 10737418240, 2, true,  true,  2),
    ('business', 'Business', true,  false, true,  3, 53687091200, 2, true,  true,  3);

CREATE TABLE network_admins (
    account_id  bigint PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    granted_by  bigint REFERENCES users (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE account_billing (
    account_id          bigint PRIMARY KEY REFERENCES users (id) ON DELETE RESTRICT,
    stripe_customer_id  text NOT NULL UNIQUE CHECK (stripe_customer_id ~ '^cus_')
);

CREATE TABLE subscriptions (
    id                      bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    account_id              bigint NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    plan                    text NOT NULL REFERENCES plans,
    stripe_subscription_id  text UNIQUE,
    stripe_price_id         text NOT NULL DEFAULT '',
    status                  text NOT NULL DEFAULT 'incomplete',  -- raw Stripe status
    state                   text NOT NULL DEFAULT 'pending' CHECK (state IN ('active', 'pending', 'grace', 'canceled')),
    grace_since             timestamptz,
    current_period_end      timestamptz,
    cancel_at_period_end    boolean NOT NULL DEFAULT false,
    synced_at               timestamptz,
    created_at              timestamptz NOT NULL DEFAULT now(),
    updated_at              timestamptz NOT NULL DEFAULT now()
);

-- The id comes from the sequence before the insert: the sealed secret is
-- bound to it ("site:<id>"), and the schema is named after it.
CREATE SEQUENCE sites_id_seq;
CREATE TABLE sites (
    id                  bigint PRIMARY KEY DEFAULT nextval('sites_id_seq'),
    kind                text NOT NULL DEFAULT 'tenant' CHECK (kind IN ('network', 'tenant')),
    slug                text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$'),
    schema_name         text NOT NULL UNIQUE CHECK ((kind = 'network' AND schema_name = 'network') OR schema_name = 'site_' || id),
    owner_id            bigint REFERENCES users (id) ON DELETE RESTRICT,
    plan                text REFERENCES plans,
    subscription_id     bigint REFERENCES subscriptions ON DELETE SET NULL,
    status              text NOT NULL DEFAULT 'provisioning'
                        CHECK (status IN ('provisioning', 'pending', 'active', 'suspended', 'deleted')),
    approved_by         bigint REFERENCES users (id) ON DELETE SET NULL,
    approved_at         timestamptz,
    suspended_reason    text NOT NULL DEFAULT '' CHECK (length(suspended_reason) <= 500),
    suspended_at        timestamptz,
    secret              bytea NOT NULL,  -- random 32-byte site secret, AES-256-GCM sealed under the master key, AAD "site:<id>"
    rp_id               text NOT NULL,   -- WebAuthn RP ID of the site's passkeys, fixed at creation (the default subdomain)
    storage_bytes       bigint NOT NULL DEFAULT 0,
    storage_checked_at  timestamptz,
    next_announce_at    timestamptz,     -- earliest scheduled announcement; the registry wakes the site
    wants_cron          boolean NOT NULL DEFAULT false,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    deleted_at          timestamptz,
    CHECK ((kind = 'network') = (owner_id IS NULL AND plan IS NULL))
);
ALTER SEQUENCE sites_id_seq OWNED BY sites.id;
CREATE UNIQUE INDEX sites_one_network ON sites (kind) WHERE kind = 'network';
CREATE INDEX sites_owner_idx ON sites (owner_id);
CREATE INDEX sites_announce_idx ON sites (next_announce_at) WHERE next_announce_at IS NOT NULL;

-- Hosts are A-labels without port or trailing dot. A custom domain is not
-- routed until verified; default domains are verified on insert.
CREATE TABLE domains (
    host            text PRIMARY KEY CHECK (host ~ '^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$'),
    site_id         bigint NOT NULL REFERENCES sites ON DELETE CASCADE,
    kind            text NOT NULL CHECK (kind IN ('default', 'custom')),
    is_primary      boolean NOT NULL DEFAULT false,
    verify_token    text NOT NULL DEFAULT '',
    verified_at     timestamptz,
    checked_at      timestamptz,
    check_failures  integer NOT NULL DEFAULT 0,
    check_error     text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    CHECK (NOT is_primary OR verified_at IS NOT NULL)
);
CREATE UNIQUE INDEX domains_one_primary ON domains (site_id) WHERE is_primary;
CREATE INDEX domains_site_idx ON domains (site_id);

CREATE TABLE site_members (
    site_id      bigint NOT NULL REFERENCES sites ON DELETE CASCADE,
    account_id   bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role         text NOT NULL CHECK (role IN ('admin', 'editor', 'author', 'contributor', 'subscriber')),
    invited_by   bigint REFERENCES users (id) ON DELETE SET NULL,
    last_sso_at  timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (site_id, account_id)
);
CREATE INDEX site_members_account_idx ON site_members (account_id);

CREATE TABLE site_invites (
    token_hash   bytea PRIMARY KEY,
    site_id      bigint NOT NULL REFERENCES sites ON DELETE CASCADE,
    email        text NOT NULL CHECK (length(email) <= 254),
    role         text NOT NULL CHECK (role IN ('admin', 'editor', 'author', 'contributor', 'subscriber')),
    invited_by   bigint REFERENCES users (id) ON DELETE SET NULL,
    expires_at   timestamptz NOT NULL,
    accepted_at  timestamptz
);

-- Single sign-on codes, stored as hashes, single use, bound to one site and
-- one redirect URI (PKCE: challenge = sha256(verifier)).
CREATE TABLE sso_codes (
    code_hash     bytea PRIMARY KEY,
    site_id       bigint NOT NULL REFERENCES sites ON DELETE CASCADE,
    account_id    bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    challenge     bytea NOT NULL,
    redirect_uri  text NOT NULL,
    operator      boolean NOT NULL DEFAULT false,
    created_at    timestamptz NOT NULL DEFAULT now(),
    expires_at    timestamptz NOT NULL,
    used_at       timestamptz
);
CREATE INDEX sso_codes_expires_idx ON sso_codes (expires_at);

-- Stripe webhook events, stored on receipt and processed in order.
CREATE TABLE billing_events (
    id            text PRIMARY KEY CHECK (id ~ '^evt_'),
    type          text NOT NULL,
    created       timestamptz NOT NULL,
    livemode      boolean NOT NULL,
    object_id     text NOT NULL DEFAULT '',
    payload       jsonb NOT NULL,
    received_at   timestamptz NOT NULL DEFAULT now(),
    processed_at  timestamptz,
    attempts      integer NOT NULL DEFAULT 0,
    result        text NOT NULL DEFAULT ''  -- 'applied' | 'unmapped' | the last error
);
CREATE INDEX billing_events_pending_idx ON billing_events (created) WHERE processed_at IS NULL;

-- Plugins uploaded by a site wait for a network admin.
CREATE TABLE plugin_reviews (
    site_id       bigint NOT NULL REFERENCES sites ON DELETE CASCADE,
    plugin_id     text NOT NULL,
    wasm_sha256   text NOT NULL,
    requested_at  timestamptz NOT NULL DEFAULT now(),
    reviewed_by   bigint REFERENCES users (id) ON DELETE SET NULL,
    reviewed_at   timestamptz,
    verdict       text NOT NULL DEFAULT '' CHECK (verdict IN ('', 'approved', 'rejected')),
    PRIMARY KEY (site_id, plugin_id, wasm_sha256)
);
