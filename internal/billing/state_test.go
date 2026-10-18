package billing

import (
	"testing"
	"time"
)

func TestStateFromStatus(t *testing.T) {
	for status, want := range map[string]SubscriptionState{
		"active":             StateActive,
		"trialing":           StateActive,
		"incomplete":         StatePending,
		"past_due":           StateGrace,
		"unpaid":             StateGrace,
		"paused":             StateGrace,
		"canceled":           StateCanceled,
		"incomplete_expired": StateCanceled,
		"something_new":      StateGrace,
		"":                   StateGrace,
	} {
		if got := StateFromStatus(status); got != want {
			t.Errorf("StateFromStatus(%q) = %s, want %s", status, got, want)
		}
	}
}

func TestShouldSuspend(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	cases := []struct {
		name       string
		state      SubscriptionState
		graceSince time.Time
		grace      time.Duration
		want       bool
	}{
		{"active", StateActive, time.Time{}, 0, false},
		{"active ignores stale grace", StateActive, now.Add(-100 * day), 0, false},
		{"grace just started", StateGrace, now.Add(-time.Hour), 0, false},
		{"grace unknown start", StateGrace, time.Time{}, 0, false},
		{"grace day 13", StateGrace, now.Add(-13 * day), 0, false},
		{"grace day 14", StateGrace, now.Add(-14 * day), 0, true},
		{"grace custom 3 days", StateGrace, now.Add(-4 * day), 3 * day, true},
		{"pending", StatePending, time.Time{}, 0, true},
		{"canceled", StateCanceled, time.Time{}, 0, true},
	}
	for _, tc := range cases {
		if got := ShouldSuspend(tc.state, tc.graceSince, now, tc.grace); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
