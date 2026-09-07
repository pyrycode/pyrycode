package devices

import (
	"sort"
	"sync"
	"testing"
	"time"
)

func TestRegistry_Validate_Hit(t *testing.T) {
	t.Parallel()
	when := mustParseTime(t, "2020-01-01T00:00:00Z")
	r := &Registry{}
	r.Add(Device{
		TokenHash:  HashToken("plain-1"),
		Name:       "alice",
		PairedAt:   when,
		LastSeenAt: when,
	})

	before := time.Now()
	got, res := r.Validate("plain-1")
	if res != ValidateAccepted {
		t.Fatalf("result = %v, want ValidateAccepted", res)
	}
	if got.Name != "alice" {
		t.Errorf("Name = %q, want %q", got.Name, "alice")
	}
	if got.TokenHash != HashToken("plain-1") {
		t.Errorf("TokenHash = %q, want %q", got.TokenHash, HashToken("plain-1"))
	}
	if !got.LastSeenAt.After(when) {
		t.Errorf("returned LastSeenAt = %v, want After(%v)", got.LastSeenAt, when)
	}
	if got.LastSeenAt.Before(before) {
		t.Errorf("returned LastSeenAt = %v, want >= before %v", got.LastSeenAt, before)
	}
	if !got.PairedAt.Equal(when) {
		t.Errorf("PairedAt = %v, want %v (unchanged)", got.PairedAt, when)
	}

	listed := r.List()
	if len(listed) != 1 {
		t.Fatalf("len(List) = %d, want 1", len(listed))
	}
	if !listed[0].LastSeenAt.After(when) {
		t.Errorf("in-memory LastSeenAt = %v, want After(%v)", listed[0].LastSeenAt, when)
	}
	if !listed[0].PairedAt.Equal(when) {
		t.Errorf("in-memory PairedAt = %v, want %v (unchanged)", listed[0].PairedAt, when)
	}
}

// TestRegistry_Validate_RejectsElapsedRedemptionWindow is AC-1: a record whose
// redemption deadline is already an hour past no longer authenticates, and the
// refusal leaves LastSeenAt exactly where it was — an expired token must not go
// on refreshing the one column that would betray it as never-scanned in
// `pyry pair list`. It carries AC-5's witness too: validation removes nothing,
// so the row is still there for `pair list` to render.
//
// It inverts #1527's inertness pin, keeping that pin's non-vacuity assertions:
// a silently zero RedeemBy would make the rejection prove nothing. They read the
// row back off the registry rather than off the returned Device, because a
// refusal returns the zero Device by design.
func TestRegistry_Validate_RejectsElapsedRedemptionWindow(t *testing.T) {
	t.Parallel()
	when := mustParseTime(t, "2020-01-01T00:00:00Z")
	expired := time.Now().Add(-time.Hour)
	r := &Registry{}
	r.Add(Device{
		TokenHash:  HashToken("plain-expired"),
		Name:       "stale",
		PairedAt:   when,
		LastSeenAt: when,
		RedeemBy:   expired,
	})

	got, res := r.Validate("plain-expired")
	if res != ValidateWindowElapsed {
		t.Fatalf("result = %v, want ValidateWindowElapsed", res)
	}
	if got != (Device{}) {
		t.Errorf("Device = %+v, want the zero Device on a refusal", got)
	}

	rows := r.List()
	if len(rows) != 1 {
		t.Fatalf("List() has %d rows, want 1 — validation must never remove a record", len(rows))
	}
	if !rows[0].LastSeenAt.Equal(when) {
		t.Errorf("LastSeenAt = %v, want the untouched %v", rows[0].LastSeenAt, when)
	}
	if rows[0].RedeemBy.IsZero() {
		t.Fatalf("fixture RedeemBy is the zero value; the rejection would hold vacuously")
	}
	if !rows[0].RedeemBy.Before(time.Now()) {
		t.Errorf("fixture RedeemBy = %v, want a deadline already in the past", rows[0].RedeemBy)
	}
}

// TestRegistry_Validate_AcceptsUnelapsedRedemptionWindow is AC-2, the other
// side of the enforcement: a deadline still in the future authenticates, and a
// zero deadline authenticates however long ago the device was paired. The zero
// arm covers both populations that carry it — a record written before the field
// existed, and a device whose deadline #1528's recordRedemption already cleared
// — so an operator's existing pairings survive the upgrade.
//
// Acceptance must still advance LastSeenAt, which is what makes the untouched
// LastSeenAt asserted by the rejection test above a real distinction rather than
// a predicate that never stamps at all.
func TestRegistry_Validate_AcceptsUnelapsedRedemptionWindow(t *testing.T) {
	t.Parallel()
	when := mustParseTime(t, "2020-01-01T00:00:00Z")

	tests := []struct {
		name     string
		redeemBy time.Time
	}{
		{name: "deadline still in the future", redeemBy: time.Now().Add(time.Hour)},
		{name: "no deadline (pre-field record, or already redeemed)", redeemBy: time.Time{}},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &Registry{}
			r.Add(Device{
				TokenHash:  HashToken("plain-live"),
				Name:       "live",
				PairedAt:   when,
				LastSeenAt: when,
				RedeemBy:   tc.redeemBy,
			})

			got, res := r.Validate("plain-live")
			if res != ValidateAccepted {
				t.Fatalf("result = %v, want ValidateAccepted", res)
			}
			if got.Name != "live" {
				t.Errorf("Name = %q, want %q", got.Name, "live")
			}
			if !got.LastSeenAt.After(when) {
				t.Errorf("LastSeenAt = %v, want advanced past %v on acceptance", got.LastSeenAt, when)
			}
			if !got.RedeemBy.Equal(tc.redeemBy) {
				t.Errorf("RedeemBy = %v, want the untouched %v — Validate must not clear it", got.RedeemBy, tc.redeemBy)
			}
		})
	}
}

// TestDevice_RedemptionWindowElapsed pins the boundary the exported path cannot
// reach without a fake clock: what happens at exactly RedeemBy. The deadline is
// exclusive — at the instant itself the record has already stopped being
// acceptable — so the predicate is `now >= RedeemBy`, not `now > RedeemBy`.
func TestDevice_RedemptionWindowElapsed(t *testing.T) {
	t.Parallel()
	deadline := mustParseTime(t, "2026-01-01T00:00:00Z")

	tests := []struct {
		name     string
		redeemBy time.Time
		now      time.Time
		want     bool
	}{
		{name: "no deadline never elapses", redeemBy: time.Time{}, now: deadline.Add(100 * 365 * 24 * time.Hour), want: false},
		{name: "before the deadline", redeemBy: deadline, now: deadline.Add(-time.Nanosecond), want: false},
		{name: "exactly at the deadline", redeemBy: deadline, now: deadline, want: true},
		{name: "past the deadline", redeemBy: deadline, now: deadline.Add(time.Nanosecond), want: true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := Device{RedeemBy: tc.redeemBy}
			if got := d.redemptionWindowElapsed(tc.now); got != tc.want {
				t.Errorf("redemptionWindowElapsed(%v) = %v, want %v", tc.now, got, tc.want)
			}
		})
	}
}

func TestRegistry_Validate_Miss(t *testing.T) {
	t.Parallel()
	when := mustParseTime(t, "2020-01-01T00:00:00Z")

	tests := []struct {
		name  string
		setup func(*Registry)
		plain string
	}{
		{
			name: "unknown-token",
			setup: func(r *Registry) {
				r.Add(Device{TokenHash: HashToken("plain-1"), Name: "alice", PairedAt: when, LastSeenAt: when})
			},
			plain: "never-paired",
		},
		{
			name: "empty-plain",
			setup: func(r *Registry) {
				r.Add(Device{TokenHash: HashToken("plain-1"), Name: "alice", PairedAt: when, LastSeenAt: when})
			},
			plain: "",
		},
		{
			name:  "empty-registry",
			setup: func(r *Registry) {},
			plain: "anything",
		},
		{
			name:  "empty-registry-empty-plain",
			setup: func(r *Registry) {},
			plain: "",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &Registry{}
			tc.setup(r)
			before := r.List()
			got, res := r.Validate(tc.plain)
			if res != ValidateUnknownToken {
				t.Errorf("result = %v, want ValidateUnknownToken", res)
			}
			if got != (Device{}) {
				t.Errorf("device = %+v, want zero Device", got)
			}
			after := r.List()
			if len(after) != len(before) {
				t.Fatalf("len(List) after = %d, want %d (no mutation)", len(after), len(before))
			}
			for i := range before {
				if !after[i].LastSeenAt.Equal(before[i].LastSeenAt) {
					t.Errorf("[%d] LastSeenAt = %v, want %v (no mutation)", i, after[i].LastSeenAt, before[i].LastSeenAt)
				}
			}
		})
	}
}

func TestDevice_MayAnswerRemotePermission(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		dev  *Device
		want bool
	}{
		{name: "bit set -> eligible", dev: &Device{AllowRemotePermissions: true}, want: true},
		{name: "bit off -> denied", dev: &Device{AllowRemotePermissions: false}, want: false},
		{name: "nil device -> denied", dev: nil, want: false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.dev.MayAnswerRemotePermission(); got != tc.want {
				t.Errorf("MayAnswerRemotePermission() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAuthorizeRemotePermission(t *testing.T) {
	t.Parallel()

	eligible := &Device{AllowRemotePermissions: true}
	ineligible := &Device{AllowRemotePermissions: false}

	tests := []struct {
		name    string
		dev     *Device
		outcome RemotePermissionOutcome
		want    bool
	}{
		// The sole ALLOW: eligible device AND an explicit allow.
		{name: "eligible + allow -> grant", dev: eligible, outcome: OutcomeAllow, want: true},

		// Eligible device, every non-allow outcome -> deny (AC3 fail-closed).
		{name: "eligible + explicit deny -> deny", dev: eligible, outcome: OutcomeDeny, want: false},
		{name: "eligible + no answer -> deny", dev: eligible, outcome: OutcomeNoAnswer, want: false},
		{name: "eligible + timeout -> deny", dev: eligible, outcome: OutcomeTimeout, want: false},
		{name: "eligible + cancel -> deny", dev: eligible, outcome: OutcomeCancel, want: false},

		// Zero-value outcome (== OutcomeNoAnswer): default-constructed call denies.
		{name: "eligible + zero-value outcome -> deny", dev: eligible, outcome: RemotePermissionOutcome(0), want: false},

		// Ineligible / nil device never grants, even on an explicit allow (AC2).
		{name: "ineligible + allow -> deny", dev: ineligible, outcome: OutcomeAllow, want: false},
		{name: "nil device + allow -> deny", dev: nil, outcome: OutcomeAllow, want: false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := AuthorizeRemotePermission(tc.dev, tc.outcome); got != tc.want {
				t.Errorf("AuthorizeRemotePermission(%+v, %v) = %v, want %v", tc.dev, tc.outcome, got, tc.want)
			}
		})
	}
}

func TestRegistry_Validate_ConcurrentSameToken(t *testing.T) {
	t.Parallel()
	when := mustParseTime(t, "2020-01-01T00:00:00Z")
	r := &Registry{}
	r.Add(Device{
		TokenHash:  HashToken("plain-1"),
		Name:       "alice",
		PairedAt:   when,
		LastSeenAt: when,
	})

	const n = 16
	var wg sync.WaitGroup
	seen := make([]time.Time, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			d, res := r.Validate("plain-1")
			if res != ValidateAccepted {
				t.Errorf("[%d] result = %v, want ValidateAccepted", i, res)
				return
			}
			seen[i] = d.LastSeenAt
		}(i)
	}
	wg.Wait()

	final := r.List()
	if len(final) != 1 {
		t.Fatalf("len(List) = %d, want 1", len(final))
	}
	if !final[0].LastSeenAt.After(when) {
		t.Errorf("final LastSeenAt = %v, want After(%v)", final[0].LastSeenAt, when)
	}

	sort.Slice(seen, func(i, j int) bool { return seen[i].Before(seen[j]) })
	for i := 1; i < n; i++ {
		if seen[i].Before(seen[i-1]) {
			t.Errorf("sorted seen[%d] = %v < seen[%d] = %v", i, seen[i], i-1, seen[i-1])
		}
	}
}
