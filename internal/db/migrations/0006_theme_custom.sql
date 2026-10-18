-- Theme customiser and block builder. One row per theme and kind: the
-- customiser's design ("design") and the builder's layouts ("layout"),
-- each with an editable draft and the published version visitors get.
-- draft is NULL when there is nothing unpublished.
CREATE TABLE theme_custom (
    theme         text NOT NULL CHECK (theme ~ '^[a-z0-9][a-z0-9_-]{0,63}$'),
    kind          text NOT NULL CHECK (kind IN ('design', 'layout')),
    draft         jsonb,
    published     jsonb NOT NULL DEFAULT '{}',
    draft_at      timestamptz,
    published_at  timestamptz,
    updated_by    bigint REFERENCES users (id) ON DELETE SET NULL,
    PRIMARY KEY (theme, kind)
);

-- Published versions, newest first; the server keeps the last ten per
-- theme and kind.
CREATE TABLE theme_custom_revisions (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    theme       text NOT NULL,
    kind        text NOT NULL CHECK (kind IN ('design', 'layout')),
    data        jsonb NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    user_id     bigint REFERENCES users (id) ON DELETE SET NULL
);
CREATE INDEX theme_custom_revisions_theme ON theme_custom_revisions (theme, kind, id DESC);
