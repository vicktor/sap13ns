-- The grace period whose failed payment the owner was e-mailed about (the
-- grace_since it started at): Stripe retries a failed renewal several
-- times, and each retry that fails sends invoice.payment_failed again, but
-- the owner is told once per grace period, at the first failure.
ALTER TABLE subscriptions ADD COLUMN failure_noticed_for timestamptz;
