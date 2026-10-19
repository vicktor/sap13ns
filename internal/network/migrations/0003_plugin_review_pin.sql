-- The network operator reviews a build of a plugin as the site will run
-- it: its module, its assets and the permissions it pins (the plugin's
-- ReviewPin), not the module alone, so the same module cannot come back
-- asking for more. wasm_sha256 stays, to show the operator.
--
-- Nothing wrote plugin_reviews before this migration.
DELETE FROM plugin_reviews;
ALTER TABLE plugin_reviews ADD COLUMN pin text NOT NULL CHECK (pin ~ '^[0-9a-f]{64}$');
ALTER TABLE plugin_reviews DROP CONSTRAINT plugin_reviews_pkey;
ALTER TABLE plugin_reviews ADD PRIMARY KEY (site_id, plugin_id, pin);
