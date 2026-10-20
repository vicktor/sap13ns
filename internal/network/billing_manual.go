package network

// Plans granted by hand: a network admin gives a site a paid plan for a
// year without Stripe (Network → Sites → a site → Plan), for a customer
// who paid by other means. The site then behaves as a paid one: it has the
// plan's limits until the year ends, and is held afterwards like a site
// whose subscription was not renewed, unless it is extended or its owner
// subscribes through Stripe Checkout, which replaces the manual plan.
// Canceling ends it at once and moves the site to the free plan.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"sap13ns/internal/store"
)

// manualYears is the period one manual grant pays for.
const manualYears = 1

// manualHeld is Network.Held for a manual plan: held once the period it was
// granted for is over.
func manualHeld(paidUntil *time.Time, now time.Time) bool {
	return paidUntil == nil || !now.Before(*paidUntil)
}

// GrantPlanYear gives a tenant site the paid plan for a year from now, or,
// when it already has a manual plan that has not ended, for a year more
// from that end (changing the plan if another is asked for). It returns
// the end of the period. ErrInvalid: no such paid plan; ErrState: the site
// is not active or suspended, or a Stripe subscription that is not over
// pays for it.
func (n *Network) GrantPlanYear(ctx context.Context, siteID int64, planSlug string, now time.Time) (time.Time, error) {
	plan, err := n.Plan(ctx, planSlug)
	if errors.Is(err, store.ErrNotFound) || err == nil && !plan.Paid {
		return time.Time{}, fmt.Errorf("%w: no paid plan %q", ErrInvalid, planSlug)
	} else if err != nil {
		return time.Time{}, err
	}
	var end time.Time
	err = n.mutate(ctx, siteID, func(tx pgx.Tx) error {
		var owner *int64
		var status string
		var subID *int64
		err := tx.QueryRow(ctx, `SELECT owner_id, status, subscription_id FROM sites WHERE id = $1 AND kind = 'tenant' FOR UPDATE`,
			siteID).Scan(&owner, &status, &subID)
		if errors.Is(err, pgx.ErrNoRows) {
			return store.ErrNotFound
		} else if err != nil {
			return err
		}
		if (status != StatusActive && status != StatusSuspended) || owner == nil {
			return fmt.Errorf("%w: the site is %s", ErrState, status)
		}
		start := now
		if subID != nil {
			var manual bool
			var state string
			var paidUntil *time.Time
			if err := tx.QueryRow(ctx, `SELECT manual, state, paid_until FROM subscriptions WHERE id = $1 FOR UPDATE`, *subID).
				Scan(&manual, &state, &paidUntil); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			switch {
			case manual && state == "active":
				if paidUntil != nil && paidUntil.After(start) {
					start = *paidUntil
				}
				end = start.AddDate(manualYears, 0, 0)
				if _, err := tx.Exec(ctx, `UPDATE subscriptions SET plan = $2, paid_until = $3, current_period_end = $3, updated_at = now()
					WHERE id = $1`, *subID, plan.Slug, end); err != nil {
					return err
				}
				_, err := tx.Exec(ctx, `UPDATE sites SET plan = $2, updated_at = now() WHERE id = $1`, siteID, plan.Slug)
				return err
			case !manual && state != "canceled" && state != "":
				return fmt.Errorf("%w: a Stripe subscription pays for the site", ErrState)
			}
		}
		end = start.AddDate(manualYears, 0, 0)
		var id int64
		if err := tx.QueryRow(ctx, `INSERT INTO subscriptions (account_id, plan, status, state, manual, paid_until, current_period_end,
				cancel_at_period_end, synced_at)
			VALUES ($1, $2, 'manual', 'active', true, $3, $3, true, $4) RETURNING id`, *owner, plan.Slug, end, now).Scan(&id); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE sites SET subscription_id = $2, plan = $3, updated_at = now() WHERE id = $1`, siteID, id, plan.Slug)
		return err
	})
	return end, err
}

// CancelManualPlan ends a site's manual plan now and moves the site to the
// first free plan. ErrState: the site has no manual plan, or the network
// has no free plan.
func (n *Network) CancelManualPlan(ctx context.Context, siteID int64, now time.Time) error {
	return n.mutate(ctx, siteID, func(tx pgx.Tx) error {
		var subID *int64
		err := tx.QueryRow(ctx, `SELECT subscription_id FROM sites WHERE id = $1 AND kind = 'tenant' FOR UPDATE`, siteID).Scan(&subID)
		if errors.Is(err, pgx.ErrNoRows) {
			return store.ErrNotFound
		} else if err != nil {
			return err
		}
		var manual bool
		if subID != nil {
			if err := tx.QueryRow(ctx, `SELECT manual FROM subscriptions WHERE id = $1 FOR UPDATE`, *subID).Scan(&manual); err != nil &&
				!errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		if !manual {
			return fmt.Errorf("%w: the site has no manual plan", ErrState)
		}
		var free string
		err = tx.QueryRow(ctx, `SELECT slug FROM plans WHERE NOT paid ORDER BY position, slug LIMIT 1`).Scan(&free)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: the network has no free plan", ErrState)
		} else if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE subscriptions SET state = 'canceled', status = 'canceled', cancel_at_period_end = false,
				paid_until = least(paid_until, $2), current_period_end = least(current_period_end, $2), updated_at = now()
			WHERE id = $1`, *subID, now); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE sites SET subscription_id = NULL, plan = $2, updated_at = now() WHERE id = $1`, siteID, free)
		return err
	})
}
