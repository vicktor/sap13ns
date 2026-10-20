-- Super admins: the network admins made from the command line. Only they
-- grant a site a paid plan without payment, and only they revoke one
-- another. A column of its own, as granted_by turns NULL when the account
-- that granted it is deleted.
ALTER TABLE network_admins ADD COLUMN super boolean NOT NULL DEFAULT false;
UPDATE network_admins SET super = true WHERE granted_by IS NULL;
