-- Plugin packages. A row is now created when a package is installed
-- (always disabled); enabling still pins wasm_sha256 and
-- granted_permissions, and now also assets_sha256 (the files under
-- assets/), so that changed CSS or JavaScript needs a new approval too.

ALTER TABLE plugins
    ADD COLUMN assets_sha256   text NOT NULL DEFAULT '',
    ADD COLUMN version         text NOT NULL DEFAULT '' CHECK (length(version) <= 60),
    ADD COLUMN source          text NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'upload', 'cli')),
    ADD COLUMN uploaded_by     bigint REFERENCES users (id) ON DELETE SET NULL,
    ADD COLUMN uploaded_at     timestamptz,
    -- Multisite: a plugin uploaded by a tenant cannot be enabled until the
    -- network operator has reviewed it. Single sites never set it.
    ADD COLUMN review_required boolean NOT NULL DEFAULT false,
    -- Latest scanner report (plugin.Report as JSON) and when it was made.
    ADD COLUMN scan_report     jsonb,
    ADD COLUMN scanned_at      timestamptz;
