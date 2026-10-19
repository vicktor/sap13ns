-- Billing (docs/BILLING.md): Checkout, the Customer Portal and the webhook.

-- One subscription pays for one site (BILLING.md maps each Checkout
-- Session to metadata[site_id]). sites_per_grant stays a plan setting: a
-- plan with several sites per subscription only needs a larger value here.
UPDATE plans SET sites_per_grant = 1 WHERE slug = 'business' AND sites_per_grant = 3;

-- The customer that pays a subscription (an account normally has one, kept
-- in account_billing; a second Checkout started before the first finished
-- can create another), and the site a Checkout not finished yet is for.
ALTER TABLE subscriptions
    ADD COLUMN stripe_customer_id text NOT NULL DEFAULT '' CHECK (stripe_customer_id = '' OR stripe_customer_id ~ '^cus_'),
    ADD COLUMN checkout_site_id   bigint REFERENCES sites ON DELETE SET NULL;
CREATE INDEX subscriptions_account_idx ON subscriptions (account_id);
CREATE INDEX sites_subscription_idx ON sites (subscription_id) WHERE subscription_id IS NOT NULL;

-- An event is retried with a growing delay after a failure; infinity once
-- it gave up, until a network admin retries it.
ALTER TABLE billing_events ADD COLUMN next_attempt_at timestamptz NOT NULL DEFAULT now();
DROP INDEX billing_events_pending_idx;
CREATE INDEX billing_events_pending_idx ON billing_events (next_attempt_at, created) WHERE processed_at IS NULL;
