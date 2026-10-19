-- The language of the administration panel, chosen by each user (the
-- language menu in the admin's top bar, or their profile). '' follows the
-- site's main language, which is also what the public site uses; the
-- public site never reads this column. A code the binary has no catalog
-- for also falls back to the site's language. Runs in every site schema
-- of a network (so a user signed in through single sign-on keeps one
-- choice per site, and the portal its own), and in public on a
-- single-site install.
ALTER TABLE users
    ADD COLUMN ui_language text NOT NULL DEFAULT ''
        CHECK (ui_language = '' OR ui_language ~ '^[a-z]{2,3}(-[a-z0-9]{2,8})?$');
