-- Official plugins, which ship inside the binary (internal/plugin/bundled)
-- and install from Plugins → Official plugins, are recorded as such.
ALTER TABLE plugins DROP CONSTRAINT plugins_source_check,
    ADD CONSTRAINT plugins_source_check CHECK (source IN ('manual', 'upload', 'cli', 'catalog', 'official'));
