package main

import (
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

// settingsReaderDouble is a sessionSettingsReader over a fixed id → settings map
// that RECORDS every id it is asked for.
//
// The recording is the point. "No pool session lookup is performed at all" is a
// claim about calls, and the returned values cannot carry it: a conversation
// bound to a session whose settings are all defaults reports exactly the zeros a
// refusal reports, so an assertion on the return value alone passes under a
// resolver that asks the pool for "" first and ignores the answer.
type settingsReaderDouble struct {
	settings map[sessions.SessionID]sessions.SessionSettings

	mu    sync.Mutex
	asked []sessions.SessionID
}

func (d *settingsReaderDouble) SettingsFor(id sessions.SessionID) (sessions.SessionSettings, error) {
	d.mu.Lock()
	d.asked = append(d.asked, id)
	d.mu.Unlock()
	s, ok := d.settings[id]
	if !ok {
		return sessions.SessionSettings{}, sessions.ErrSessionNotFound
	}
	return s, nil
}

func (d *settingsReaderDouble) calls() []sessions.SessionID {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.asked)
}

// TestResolveBoundRunSettings exercises the settings half of the
// conversation-keyed run-configuration seam (#1609) against a counting double.
//
// Every row asserts the returned values AND the exact sequence of pool reads the
// resolver performed, because the two failures this resolver exists to prevent
// are invisible in the values alone: asking the pool for "" (which the real
// Pool.Lookup resolves to the BOOTSTRAP session — the #678 isolation break), and
// reporting one session's id beside another session's settings.
//
// Each row builds its own double so the call assertions stay independent under
// t.Parallel(); the registry is read-only and shared.
func TestResolveBoundRunSettings(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: "conv-a", CurrentSessionID: "sess-a", LastUsedAt: now})
	reg.Create(conversations.Conversation{ID: "conv-b", CurrentSessionID: "sess-b", LastUsedAt: now})
	reg.Create(conversations.Conversation{ID: "conv-defaults", CurrentSessionID: "sess-defaults", LastUsedAt: now})
	reg.Create(conversations.Conversation{ID: "conv-unbound", CurrentSessionID: "", LastUsedAt: now})
	reg.Create(conversations.Conversation{ID: "conv-dangling", CurrentSessionID: "sess-evicted", LastUsedAt: now})

	// A and B carry deliberately different settings — one YOLO:true, one empty
	// Model — so a resolver that ignores its argument and reports some captured
	// session gets at least one field wrong whichever of the two it captured.
	// sess-defaults holds the zero SessionSettings; sess-evicted is absent, which
	// is what the pool looks like after an idle eviction dropped the session a
	// conversation is still bound to.
	newReader := func() *settingsReaderDouble {
		return &settingsReaderDouble{settings: map[sessions.SessionID]sessions.SessionSettings{
			"sess-a":        {Model: "claude-opus-4-8", Effort: "high", YOLO: true},
			"sess-b":        {Model: "", Effort: "low"},
			"sess-defaults": {},
		}}
	}

	cases := []struct {
		name   string
		convID string
		want   boundRunSettings
		wantOK bool
		// wantAsked is the exact sequence of ids handed to the pool. nil means the
		// pool was never touched.
		wantAsked []sessions.SessionID
	}{
		{
			name:   "empty conversation id never reaches the pool",
			convID: "",
		},
		{
			name:   "unknown conversation never reaches the pool",
			convID: "conv-does-not-exist",
		},
		{
			name:   "unbound conversation is fail-closed before the pool",
			convID: "conv-unbound",
		},
		{
			name:      "dangling binding is fail-closed after exactly one pool read",
			convID:    "conv-dangling",
			wantAsked: []sessions.SessionID{"sess-evicted"},
		},
		{
			name:      "conversation A reports its own session and settings",
			convID:    "conv-a",
			want:      boundRunSettings{sessionID: "sess-a", model: "claude-opus-4-8", effort: "high", yolo: true},
			wantOK:    true,
			wantAsked: []sessions.SessionID{"sess-a"},
		},
		{
			name:      "conversation B reports its own session and settings",
			convID:    "conv-b",
			want:      boundRunSettings{sessionID: "sess-b", model: "", effort: "low", yolo: false},
			wantOK:    true,
			wantAsked: []sessions.SessionID{"sess-b"},
		},
		{
			name:      "an all-defaults session resolves — zeros are a real answer",
			convID:    "conv-defaults",
			want:      boundRunSettings{sessionID: "sess-defaults"},
			wantOK:    true,
			wantAsked: []sessions.SessionID{"sess-defaults"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			reader := newReader()
			got, ok := resolveBoundRunSettings(reg, reader, tc.convID)
			if ok != tc.wantOK {
				t.Fatalf("resolveBoundRunSettings(%q) ok = %v, want %v", tc.convID, ok, tc.wantOK)
			}
			if got != tc.want {
				t.Errorf("resolveBoundRunSettings(%q) = %+v, want %+v", tc.convID, got, tc.want)
			}

			asked := reader.calls()
			if !slices.Equal(asked, tc.wantAsked) {
				t.Errorf("pool was asked for %q, want %q — an unresolvable conversation must reach no pool read at all", asked, tc.wantAsked)
			}
			if slices.Contains(asked, sessions.SessionID("")) {
				t.Errorf("pool was asked for the empty session id (%q) — Pool.Lookup resolves \"\" to the BOOTSTRAP session, which is the fall-through this resolver exists to make impossible", asked)
			}
		})
	}
}

// TestResolveBoundRunSettings_RealPool pins that the counting double above models
// the contract *sessions.Pool actually implements: a held id resolves, and an id
// the pool never held comes back as the real sessions.ErrSessionNotFound rather
// than as some other error the resolver might treat differently.
func TestResolveBoundRunSettings_RealPool(t *testing.T) {
	t.Parallel()

	pool := newRouterTestPool(t)
	bootstrapID := pool.Default().ID()
	neverHeld, err := sessions.NewID()
	if err != nil {
		t.Fatalf("sessions.NewID: %v", err)
	}

	now := time.Now().UTC()
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: "conv-live", CurrentSessionID: string(bootstrapID), LastUsedAt: now})
	reg.Create(conversations.Conversation{ID: "conv-gone", CurrentSessionID: string(neverHeld), LastUsedAt: now})

	t.Run("a session the pool holds resolves", func(t *testing.T) {
		t.Parallel()
		got, ok := resolveBoundRunSettings(reg, pool, "conv-live")
		if !ok {
			t.Fatalf("resolveBoundRunSettings(conv-live) ok = false, want true")
		}
		if got.sessionID != string(bootstrapID) {
			t.Errorf("sessionID = %q, want %q", got.sessionID, bootstrapID)
		}
	})

	t.Run("a session the pool never held is fail-closed", func(t *testing.T) {
		t.Parallel()
		got, ok := resolveBoundRunSettings(reg, pool, "conv-gone")
		if ok || got != (boundRunSettings{}) {
			t.Errorf("resolveBoundRunSettings(conv-gone) = (%+v, %v), want (zero, false)", got, ok)
		}
	})
}

// usageRecorder is a by-id context-window reader with per-id figures that records
// every id it is asked for — the composition-side twin of settingsReaderDouble.
// Both halves of what it records matter: that an unresolvable conversation asks
// it nothing, and that a resolved one asks it for the RESOLVED SESSION id rather
// than the conversation id it was called with.
type usageRecorder struct {
	figures map[string][2]int

	mu    sync.Mutex
	asked []string
}

func (u *usageRecorder) read(id string) (usedTokens, windowTokens int) {
	u.mu.Lock()
	u.asked = append(u.asked, id)
	u.mu.Unlock()
	f := u.figures[id]
	return f[0], f[1]
}

func (u *usageRecorder) calls() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return slices.Clone(u.asked)
}

// TestRunConfigFor_NilResolverBuildsNoSeam pins the build-time decision: with no
// settings half there is nothing to resolve, so no closure is constructed at all
// (foreground / v1). Asserting on the returned value rather than on a call is the
// point — the nil-ness is structural, not a promise made by a closure body.
func TestRunConfigFor_NilResolverBuildsNoSeam(t *testing.T) {
	t.Parallel()

	usage := &usageRecorder{}
	if seam := runConfigFor(nil, usage.read); seam != nil {
		t.Fatal("runConfigFor(nil resolver, wired usage) returned a seam, want nil — the resolver is what makes the seam answerable")
	}
	if calls := usage.calls(); len(calls) != 0 {
		t.Errorf("usage reader was called %q while building a nil seam, want no calls", calls)
	}
}

// TestRunConfigFor_NoSessionsDirectoryStillResolves is the daemon-with-no-sessions
// -directory row: snapshotUsageFor returns nil there, and an unwired usage half
// must degrade the two context figures to zero rather than make a resolved
// conversation unresolvable. It is the sole red for a builder that copies
// bootstrapSnapshotUsage's either-half-nil rule.
func TestRunConfigFor_NoSessionsDirectoryStillResolves(t *testing.T) {
	t.Parallel()

	resolve := func(convID string) (boundRunSettings, bool) {
		return boundRunSettings{sessionID: "sess-a", model: "claude-opus-4-8", effort: "high", yolo: true}, true
	}

	seam := runConfigFor(resolve, nil)
	if seam == nil {
		t.Fatal("runConfigFor(wired resolver, nil usage) returned nil, want a working seam reporting zero context figures")
	}
	got, ok := seam("conv-a")
	if !ok {
		t.Fatalf("seam(conv-a) ok = false, want true — an unwired usage half does not make a resolved conversation unresolvable")
	}
	want := relay.RunConfig{SessionID: "sess-a", Model: "claude-opus-4-8", Effort: "high", YOLO: true}
	if got != want {
		t.Errorf("seam(conv-a) = %+v, want %+v", got, want)
	}
}

// TestRunConfigFor_ReadsUsageForTheResolvedSession is the cross-conversation
// independence property one layer up from
// TestSnapshotUsageFor_SiblingTranscriptIsNeverRead: ONE composed seam answers
// for two conversations bound to different sessions, and each reports its own
// six values. The recorded ids are what pin that the usage half is asked for the
// RESOLVED session id — a composer passing the conversation id, or a captured
// bootstrap id, returns the wrong figures and records the wrong ids.
func TestRunConfigFor_ReadsUsageForTheResolvedSession(t *testing.T) {
	t.Parallel()

	bound := map[string]boundRunSettings{
		"conv-a": {sessionID: "sess-a", model: "claude-opus-4-8", effort: "high", yolo: true},
		"conv-b": {sessionID: "sess-b", effort: "low"},
	}
	resolve := func(convID string) (boundRunSettings, bool) {
		b, ok := bound[convID]
		return b, ok
	}
	usage := &usageRecorder{figures: map[string][2]int{
		"sess-a": {1000, 200_000},
		"sess-b": {150_000, 1_000_000},
	}}

	seam := runConfigFor(resolve, usage.read)
	if seam == nil {
		t.Fatal("runConfigFor(wired, wired) returned nil, want a seam")
	}

	gotA, okA := seam("conv-a")
	gotB, okB := seam("conv-b")
	if !okA || !okB {
		t.Fatalf("seam ok = (%v, %v) for (conv-a, conv-b), want (true, true)", okA, okB)
	}

	wantA := relay.RunConfig{SessionID: "sess-a", Model: "claude-opus-4-8", Effort: "high", YOLO: true, UsedTokens: 1000, WindowTokens: 200_000}
	wantB := relay.RunConfig{SessionID: "sess-b", Effort: "low", UsedTokens: 150_000, WindowTokens: 1_000_000}
	if gotA != wantA {
		t.Errorf("seam(conv-a) = %+v, want %+v — no field may describe another conversation's session", gotA, wantA)
	}
	if gotB != wantB {
		t.Errorf("seam(conv-b) = %+v, want %+v — no field may describe another conversation's session", gotB, wantB)
	}

	wantAsked := []string{"sess-a", "sess-b"}
	if asked := usage.calls(); !slices.Equal(asked, wantAsked) {
		t.Errorf("usage reader was asked for %q, want %q — the context-window reader must be handed the RESOLVED session id, never the conversation id and never \"\"", asked, wantAsked)
	}
}

// TestRunConfigFor_UnresolvableReachesNoUsageRead is the security half of the
// composition: an unresolvable conversation must address nothing, so the
// transcript reader is not reached at all. Zero values cannot carry that claim —
// an all-defaults session on a fresh transcript reports the same struct — so the
// recorded call count is the assertion.
func TestRunConfigFor_UnresolvableReachesNoUsageRead(t *testing.T) {
	t.Parallel()

	resolve := func(convID string) (boundRunSettings, bool) {
		return boundRunSettings{}, false
	}
	usage := &usageRecorder{figures: map[string][2]int{"": {999, 999}}}

	seam := runConfigFor(resolve, usage.read)
	if seam == nil {
		t.Fatal("runConfigFor(wired, wired) returned nil, want a seam")
	}
	got, ok := seam("conv-not-hosted")
	if ok || got != (relay.RunConfig{}) {
		t.Errorf("seam(conv-not-hosted) = (%+v, %v), want (zero, false)", got, ok)
	}
	if asked := usage.calls(); len(asked) != 0 {
		t.Errorf("usage reader was asked for %q, want no calls — an unresolvable conversation must reach no transcript path", asked)
	}
}
