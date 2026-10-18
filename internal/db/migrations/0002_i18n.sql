-- Multilingual content. Every post and page has a language; translations of
-- the same piece share a translation group (the id of the original). The main
-- language is served without a URL prefix, the others under /<lang>/.

ALTER TABLE posts ADD COLUMN lang text NOT NULL DEFAULT 'es'
    CHECK (lang ~ '^[a-z]{2,3}(-[a-z0-9]{2,8})?$');
UPDATE posts SET lang = coalesce((SELECT value->>'language' FROM settings WHERE key = 'site'), 'es');
ALTER TABLE posts ALTER COLUMN lang DROP DEFAULT;

ALTER TABLE posts ADD COLUMN tgroup bigint;
CREATE UNIQUE INDEX posts_tgroup_lang_idx ON posts (tgroup, lang) WHERE tgroup IS NOT NULL;

ALTER TABLE posts DROP CONSTRAINT posts_type_slug_key;
ALTER TABLE posts ADD CONSTRAINT posts_type_lang_slug_key UNIQUE (type, lang, slug);

DROP INDEX posts_public_idx;
CREATE INDEX posts_public_idx ON posts (type, lang, status, published_at DESC);

-- Term names and descriptions per language: {"en": {"name": "...", "description": "..."}}.
ALTER TABLE terms ADD COLUMN i18n jsonb NOT NULL DEFAULT '{}';

-- Menus and widgets: '' applies to every language unless one has its own.
ALTER TABLE menu_items ADD COLUMN lang text NOT NULL DEFAULT ''
    CHECK (lang = '' OR lang ~ '^[a-z]{2,3}(-[a-z0-9]{2,8})?$');
DROP INDEX menu_items_location_idx;
CREATE INDEX menu_items_location_idx ON menu_items (location, lang, position);

ALTER TABLE widgets ADD COLUMN lang text NOT NULL DEFAULT ''
    CHECK (lang = '' OR lang ~ '^[a-z]{2,3}(-[a-z0-9]{2,8})?$');
