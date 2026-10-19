-- Custom domains are checked in DNS: when added (the TXT record proves
-- control, an address or CNAME shows that the host points to the
-- network), and every day once verified. A verified domain whose checks
-- keep failing lapses: it is no longer routed (verified_at back to NULL,
-- lapsed_at set) and, if it was the primary host, the default subdomain
-- takes over. failing_since is the first failed check of the current run.
-- check_error holds a short code, never text from a DNS answer.
ALTER TABLE domains ADD COLUMN failing_since timestamptz;
ALTER TABLE domains ADD COLUMN lapsed_at timestamptz;
ALTER TABLE domains ADD CONSTRAINT domains_check_error_code CHECK (check_error ~ '^[a-z_]{0,32}$');
CREATE INDEX domains_recheck_idx ON domains (checked_at) WHERE kind = 'custom' AND verified_at IS NOT NULL;
