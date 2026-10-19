-- The official plugins ship inside the binary, and every site can install
-- them, on every plan. The network operator can hide some of them from the
-- sites (Network → Plugin reviews → Official plugins): a hidden plugin is
-- not listed and cannot be installed or updated from the sites' admin.
-- Copies already installed stay. The portal and the command line are the
-- operator's and are not affected.
CREATE TABLE official_plugins_hidden (
    plugin_id  text PRIMARY KEY CHECK (plugin_id ~ '^[a-z0-9][a-z0-9-]{1,49}$'),
    hidden_by  bigint REFERENCES users (id) ON DELETE SET NULL,
    hidden_at  timestamptz NOT NULL DEFAULT now()
);
