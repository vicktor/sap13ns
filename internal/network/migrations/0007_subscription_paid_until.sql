-- The end of the last period a subscription was paid for (invoice.paid).
-- A subscription that ends before then, canceled at once in the Stripe
-- Dashboard, keeps its sites served until that time (Network.Held).
-- Subscriptions active now were paid for their current period.
ALTER TABLE subscriptions ADD COLUMN paid_until timestamptz;
UPDATE subscriptions SET paid_until = current_period_end WHERE status = 'active' AND grace_since IS NULL;
