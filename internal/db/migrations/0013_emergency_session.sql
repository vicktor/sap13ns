-- Emergency access of a network (docs/MULTISITE.md): a network admin who
-- opens a site without its owner's consent, for a legal order or a serious
-- security incident, signs in with 'sso-emergency', so that the session
-- tells it apart from a consented operator's. Runs in every site schema,
-- and in public on a single-site install, which never sets it.
ALTER TABLE sessions DROP CONSTRAINT sessions_amr_check,
    ADD CONSTRAINT sessions_amr_check CHECK (amr IN ('', 'sso', 'sso-operator', 'sso-emergency'));
