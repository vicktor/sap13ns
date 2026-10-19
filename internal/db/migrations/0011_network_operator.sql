-- Multisite operator access. Runs in every site schema, and in public on a
-- single-site install, which never sets the column.

-- A network admin who opens a site they are not a member of signs in as a
-- user of their own there, flagged here, and never as the linked user of a
-- member: the site's users and audit log then show who acted as the
-- network's operator. A network account has at most one linked user and
-- one operator user on a site.
ALTER TABLE users ADD COLUMN network_operator boolean NOT NULL DEFAULT false
    CHECK (NOT network_operator OR network_account_id IS NOT NULL);
ALTER TABLE users DROP CONSTRAINT users_network_account_id_key;
CREATE UNIQUE INDEX users_network_account_id_key ON users (network_account_id, network_operator);
