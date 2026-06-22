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
	got, ok := r.Validate("plain-1")
	if !ok {
		t.Fatalf("ok = false, want true")
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
			got, ok := r.Validate(tc.plain)
			if ok {
				t.Errorf("ok = true, want false")
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
			d, ok := r.Validate("plain-1")
			if !ok {
				t.Errorf("[%d] ok = false, want true", i)
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
