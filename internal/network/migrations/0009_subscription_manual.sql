-- Plans an operator grants by hand for a year (Network → Sites → Upgrade),
-- as if the owner had paid for one: no Stripe subscription, the paid
-- period in paid_until and current_period_end, never renewed.
ALTER TABLE subscriptions ADD COLUMN manual boolean NOT NULL DEFAULT false;
