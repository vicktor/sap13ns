-- Plugins a site of a network installs from the network's catalogue (the
-- plugins the operator installed on the portal) are recorded as such.
ALTER TABLE plugins DROP CONSTRAINT plugins_source_check,
    ADD CONSTRAINT plugins_source_check CHECK (source IN ('manual', 'upload', 'cli', 'catalog'));
