-- The secret key of a site moved here from another install (sap13ns
-- transfer, Tools → Move site). Everything the site's tables hold
-- encrypted (second factors, SMTP, AI and plugin secrets) was sealed with
-- that key, so the site keeps it: sealed here with SAP13NS_SECRET_KEY, and
-- used in its place once opened. A site that was never moved has no row.
-- Sites of a network have their key in the network schema and never use
-- this table.
CREATE TABLE site_key (
    id         boolean PRIMARY KEY DEFAULT true CHECK (id),
    sealed     bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
