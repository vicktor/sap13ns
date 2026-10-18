-- Multisite single sign-on. Runs in every site schema, and in public on a
-- single-site install, which never sets either column.

-- A user linked to a network account (network.users.id) signs in through
-- the network portal, and the network decides their role. NULL is a local
-- user. Accounts created by SSO have the password hash '!sso', which never
-- verifies.
ALTER TABLE users ADD COLUMN network_account_id bigint UNIQUE;

-- How the session was authenticated: '' on this site (password, passkey,
-- TOTP), 'sso' through the portal, 'sso-operator' for a network admin
-- opening the site as administrator. The portal required the second factor
-- before issuing the sign-in code.
ALTER TABLE sessions ADD COLUMN amr text NOT NULL DEFAULT ''
    CHECK (amr IN ('', 'sso', 'sso-operator'));
