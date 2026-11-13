-- Notifications of new posts (internal/web/push.go, internal/webpush).
-- push_subscriptions holds what a visitor's browser gave the site when
-- they asked to be told: the address of its push service and the two keys
-- that encrypt messages for it. No name, e-mail or IP address.
CREATE TABLE push_subscriptions (
    id         bigserial PRIMARY KEY,
    endpoint   text NOT NULL UNIQUE,
    p256dh     bytea NOT NULL,
    auth       bytea NOT NULL,
    lang       text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);

-- push_keys is the site's VAPID key pair, one row: the private key is
-- encrypted with the instance key, the public one goes to browsers.
CREATE TABLE push_keys (
    id          boolean PRIMARY KEY DEFAULT true CHECK (id),
    private_enc bytea NOT NULL,
    public      bytea NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);
