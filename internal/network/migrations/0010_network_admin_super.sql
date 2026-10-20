-- Super admins: network admins made from the command line. Only they
-- grant a site a paid plan without payment, and only they revoke one
-- another. A column of its own, as granted_by also turns NULL when the
-- account that granted it is deleted: so only the first admin, the one
-- network init made, becomes one here; `network init -username U` makes
-- others.
ALTER TABLE network_admins ADD COLUMN super boolean NOT NULL DEFAULT false;
UPDATE network_admins SET super = true WHERE account_id =
    (SELECT account_id FROM network_admins WHERE granted_by IS NULL ORDER BY created_at, account_id LIMIT 1);
