-- Revisions get the same theme slug check as theme_custom (0006). The
-- server only writes validated slugs, so existing rows pass.
ALTER TABLE theme_custom_revisions
    ADD CONSTRAINT theme_custom_revisions_theme_check CHECK (theme ~ '^[a-z0-9][a-z0-9_-]{0,63}$');
