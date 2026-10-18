package billing

import "time"

// SubscriptionState is the hosting view of a Stripe subscription status.
//
// How the hosting layer should react:
//
//   - StateActive (active, trialing): serve the site with the plan's
//     features. Clear any grace marker.
//   - StatePending (incomplete): checkout finished but the first payment has
//     not succeeded yet (for example 3-D Secure or a delayed payment method).
//     Do not grant the paid plan yet: a new commercial site stays
//     unpublished, an existing site keeps its previous plan.
//     Stripe moves the subscription to active or, after about 23 hours, to
//     incomplete_expired.
//   - StateGrace (past_due, unpaid, paused, and any status this package does
//     not know): a renewal payment failed or collection stopped. Keep serving
//     the site, record when grace started (grace_since) the first time this
//     state is seen, show a billing banner in the admin and email the owner
//     with a link to the Customer Portal. Once the grace period
//     (DefaultGracePeriod unless configured) has elapsed, suspend the site:
//     stop serving public pages (show a neutral "site suspended" page),
//     keep all content, keep the admin reachable so the owner can pay.
//     A later invoice.paid / status active lifts the suspension.
//   - StateCanceled (canceled, incomplete_expired): the subscription is over
//     (end of period after a cancellation, or Stripe gave up collecting).
//     Suspend the site right away and drop the paid plan. Keep the data for
//     the retention period announced in the terms, then delete after notice.
//     Re-subscribing goes through a new Checkout Session.
//
// Free personal sites never have a subscription and are not governed by
// these states.
type SubscriptionState string

const (
	StateActive   SubscriptionState = "active"
	StatePending  SubscriptionState = "pending"
	StateGrace    SubscriptionState = "grace"
	StateCanceled SubscriptionState = "canceled"
)

// DefaultGracePeriod is how long a site in StateGrace keeps being served.
// It roughly matches Stripe's default Smart Retries window; align it with
// the retry and "subscription status after all retries fail" settings in
// the Stripe Dashboard (Billing > Revenue recovery).
const DefaultGracePeriod = 14 * 24 * time.Hour

// StateFromStatus maps a Stripe subscription status to a SubscriptionState.
// Unknown statuses map to StateGrace, so a status added by Stripe in the
// future neither takes a paying site offline at once nor keeps it online
// indefinitely.
func StateFromStatus(status string) SubscriptionState {
	switch status {
	case "active", "trialing":
		return StateActive
	case "incomplete":
		return StatePending
	case "past_due", "unpaid", "paused":
		return StateGrace
	case "canceled", "incomplete_expired":
		return StateCanceled
	default:
		return StateGrace
	}
}

// ShouldSuspend reports whether a site that requires a paid plan must not be
// served at now.
// graceSince is when the site first entered StateGrace (zero if unknown,
// in which case grace is assumed to start now); grace <= 0 means
// DefaultGracePeriod.
func ShouldSuspend(state SubscriptionState, graceSince, now time.Time, grace time.Duration) bool {
	switch state {
	case StateActive:
		return false
	case StateGrace:
		if graceSince.IsZero() {
			return false
		}
		if grace <= 0 {
			grace = DefaultGracePeriod
		}
		return !now.Before(graceSince.Add(grace))
	default: // StatePending, StateCanceled
		return true
	}
}
