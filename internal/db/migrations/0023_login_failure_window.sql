-- Failed sign-ins count within a window: one bad attempt every quarter of
-- an hour must not keep an account locked for ever.
ALTER TABLE users ADD COLUMN last_failed_login_at timestamptz;
