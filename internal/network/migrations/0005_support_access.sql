-- Support access (docs/MULTISITE.md): the consent that lets the network's
-- administrators open a site they are not members of, as operators. The
-- site's owner, or one of its administrators, grants it from "My sites"
-- for 24 hours, 7 days or until revoked (expires_at NULL). A grant is live
-- while revoked_at is NULL and expires_at is NULL or in the future. A new
-- grant ends the live one (revoked_at, revoked_by), so a site has at most
-- one live grant, and the rows stay as the site's history.
CREATE TABLE support_grants (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    site_id     bigint NOT NULL REFERENCES sites ON DELETE CASCADE,
    granted_by  bigint REFERENCES users (id) ON DELETE SET NULL,
    granted_at  timestamptz NOT NULL,
    expires_at  timestamptz,
    revoked_at  timestamptz,
    revoked_by  bigint REFERENCES users (id) ON DELETE SET NULL,
    CHECK (expires_at IS NULL OR expires_at > granted_at),
    CHECK (revoked_at IS NOT NULL OR revoked_by IS NULL)
);
CREATE INDEX support_grants_site_idx ON support_grants (site_id, granted_at DESC);

-- Emergency access: without the owner's consent, a network admin opens a
-- site for a legal order or a serious security incident, for at most four
-- hours, for themselves only. It works on a suspended site too. It is live
-- while ended_at is NULL and expires_at is in the future.
CREATE TABLE emergency_access (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    site_id        bigint NOT NULL REFERENCES sites ON DELETE CASCADE,
    account_id     bigint REFERENCES users (id) ON DELETE SET NULL,
    category       text NOT NULL CHECK (category IN ('legal', 'security')),
    justification  text NOT NULL CHECK (length(justification) BETWEEN 1 AND 500),
    started_at     timestamptz NOT NULL,
    expires_at     timestamptz NOT NULL,
    ended_at       timestamptz,
    ended_by       bigint REFERENCES users (id) ON DELETE SET NULL,
    CHECK (expires_at > started_at AND expires_at <= started_at + interval '4 hours'),
    CHECK (ended_at IS NOT NULL OR ended_by IS NULL)
);
CREATE INDEX emergency_access_site_idx ON emergency_access (site_id, started_at DESC);

-- A code issued for an emergency access signs in with it.
ALTER TABLE sso_codes ADD COLUMN emergency boolean NOT NULL DEFAULT false;
