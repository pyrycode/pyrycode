package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

// settingsReaderDouble is a sessionSettingsReader over two fixed id → settings
// maps — the pool's live half and its dormant half — that RECORDS every id it is
// asked for, TAGGED with which half was asked.
//
// The recording is the point. "No pool session lookup is performed at all" is a
// claim about calls, and the returned values cannot carry it: a conversation
// bound to a session whose settings are all defaults reports exactly the zeros a
// refusal reports, so an assertion on the return value alone passes under a
// resolver that asks the pool for "" first and ignores the answer.
//
// ONE ordered sequence rather than a slice per half, because the ORDER is a
// property the resolver owes: the live read comes first, so a session the pool
// actually holds is never reported from a stale dormant entry (#2449). Two
// independent counters cannot state that.
type settingsReaderDouble struct {
	settings map[sessions.SessionID]sessions.SessionSettings
	dormant  map[sessions.SessionID]sessions.SessionSettings

	mu    sync.Mutex
	asked []string
}

func (d *settingsReaderDouble) SettingsFor(id sessions.SessionID) (sessions.SessionSettings, error) {
	return d.answer("live", d.settings, id)
}

func (d *settingsReaderDouble) DormantSettingsFor(id sessions.SessionID) (sessions.SessionSettings, error) {
	return d.answer("dormant", d.dormant, id)
}

func (d *settingsReaderDouble) answer(half string, from map[sessions.SessionID]sessions.SessionSettings, id sessions.SessionID) (sessions.SessionSettings, error) {
	d.mu.Lock()
	d.asked = append(d.asked, half+":"+string(id))
	d.mu.Unlock()
	s, ok := from[id]
	if !ok {
		return sessions.SessionSettings{}, sessions.ErrSessionNotFound
	}
	return s, nil
}

func (d *settingsReaderDouble) calls() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.asked)
}

// TestResolveBoundRunSettings exercises the settings half of the
// conversation-keyed run-configuration seam (#1609) against a counting double.
//
// Every row asserts the returned values AND the exact sequence of pool reads the
// resolver performed, because the failures this resolver exists to prevent are
// invisible in the values alone: asking the pool for "" (which the real
// Pool.Lookup resolves to the BOOTSTRAP session — the #678 isolation break),
// reporting one session's id beside another session's settings, and — since
// #2449 widened the reader to two halves — consulting the dormant half for a
// session the pool actually holds.
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
	reg.Create(conversations.Conversation{ID: "conv-dormant", CurrentSessionID: "sess-dormant", LastUsedAt: now})

	// A and B carry deliberately different settings — one YOLO:true, one empty
	// Model — so a resolver that ignores its argument and reports some captured
	// session gets at least one field wrong whichever of the two it captured.
	// sess-defaults holds the zero SessionSettings; sess-evicted is in neither
	// half, which is what an id the daemon has no record of at all looks like.
	//
	// sess-dormant is in the dormant half only — the shape every non-bootstrap
	// session has after a daemon restart (#2449). Its settings carry the default
	// posture the pool's own dormant read canonicalises to, so the row below also
	// pins that this resolver COPIES what it is handed rather than re-deriving a
	// posture of its own.
	newReader := func() *settingsReaderDouble {
		return &settingsReaderDouble{
			settings: map[sessions.SessionID]sessions.SessionSettings{
				"sess-a":        {Model: "claude-opus-4-8", Effort: "high", YOLO: true, PermissionMode: "bypassPermissions"},
				"sess-b":        {Model: "", Effort: "low", PermissionMode: "plan"},
				"sess-defaults": {},
			},
			dormant: map[sessions.SessionID]sessions.SessionSettings{
				"sess-dormant": {Model: "claude-sonnet-4-5", Effort: "low", PermissionMode: "default"},
			},
		}
	}

	cases := []struct {
		name   string
		convID string
		want   boundRunSettings
		wantOK bool
		// wantAsked is the exact sequence of reads the resolver performed, each
		// tagged with the pool half it went to. nil means the pool was never
		// touched.
		wantAsked []string
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
			name:      "a binding in neither half is fail-closed after both reads",
			convID:    "conv-dangling",
			wantAsked: []string{"live:sess-evicted", "dormant:sess-evicted"},
		},
		{
			name:      "conversation A reports its own session and settings",
			convID:    "conv-a",
			want:      boundRunSettings{sessionID: "sess-a", model: "claude-opus-4-8", effort: "high", yolo: true, permissionMode: "bypassPermissions", live: true},
			wantOK:    true,
			wantAsked: []string{"live:sess-a"},
		},
		{
			name:      "conversation B reports its own session and settings",
			convID:    "conv-b",
			want:      boundRunSettings{sessionID: "sess-b", model: "", effort: "low", yolo: false, permissionMode: "plan", live: true},
			wantOK:    true,
			wantAsked: []string{"live:sess-b"},
		},
		{
			name:      "an all-defaults session resolves — zeros are a real answer",
			convID:    "conv-defaults",
			want:      boundRunSettings{sessionID: "sess-defaults", live: true},
			wantOK:    true,
			wantAsked: []string{"live:sess-defaults"},
		},
		{
			name:      "a dormant binding reports its id, model and effort, marked not live",
			convID:    "conv-dormant",
			want:      boundRunSettings{sessionID: "sess-dormant", model: "claude-sonnet-4-5", effort: "low", permissionMode: "default"},
			wantOK:    true,
			wantAsked: []string{"live:sess-dormant", "dormant:sess-dormant"},
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
				t.Errorf("pool reads were %q, want %q — an unresolvable conversation must reach no pool read at all, and a live hit must not go on to the dormant half", asked, tc.wantAsked)
			}
			if slices.Contains(asked, "live:") || slices.Contains(asked, "dormant:") {
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

// TestResolveBoundRunSettings_DormantRealPool is the restart case against a real
// *sessions.Pool: a registry holding a bootstrap entry plus one configured entry,
// warm-started, so the second session is exactly what every non-bootstrap session
// is after a daemon restart — persisted, bound, and not materialised (#2449).
//
// It exists because the counting double above cannot prove the two things that
// make the reply correct rather than merely non-empty. That Pool.New really does
// leave the entry unmaterialised (the double is TOLD which half holds what), and
// that the reported posture is the DEFAULT the pool canonicalises to rather than
// the bypass the entry persisted — the double returns whatever its map carries,
// so the revocation is only ever asserted where the clearing happens.
func TestResolveBoundRunSettings_DormantRealPool(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	const (
		bootID    = "550e8400-e29b-41d4-a716-446655440000"
		dormantID = "6ba7b810-9dad-11d1-80b4-00c04fd430c8"
	)
	// The dormant entry persists a bypass posture in BOTH of its on-disk
	// spellings, so an implementation that reported what the file holds is red
	// here rather than in a comment.
	registry := `{"version":1,"sessions":[
		{"id":"` + bootID + `","label":"boot","bootstrap":true,"created_at":"2026-09-01T00:00:00Z","last_active_at":"2026-09-01T00:00:00Z"},
		{"id":"` + dormantID + `","label":"conv-1","model":"claude-opus-4-8","effort":"high","yolo":true,"permission_mode":"bypassPermissions","created_at":"2026-09-01T00:00:01Z","last_active_at":"2026-09-01T00:00:01Z"}
	]}`
	if err := os.WriteFile(regPath, []byte(registry), 0o600); err != nil {
		t.Fatalf("write registry: %v", err)
	}

	pool, err := sessions.New(sessions.Config{
		Bootstrap:     sessions.SessionConfig{ClaudeBin: os.Args[0]},
		RegistryPath:  regPath,
		RunnerFactory: func(sessions.RunnerConfig) (sessions.Runner, error) { return stubRunner{}, nil },
	})
	if err != nil {
		t.Fatalf("sessions.New: %v", err)
	}

	now := time.Now().UTC()
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: "conv-dormant", CurrentSessionID: dormantID, LastUsedAt: now})

	got, ok := resolveBoundRunSettings(reg, pool, "conv-dormant")
	if !ok {
		t.Fatalf("resolveBoundRunSettings(conv-dormant) ok = false, want true — a bound session the daemon has a persisted record of must resolve")
	}
	want := boundRunSettings{
		sessionID:      dormantID,
		model:          "claude-opus-4-8",
		effort:         "high",
		permissionMode: "default",
	}
	if got != want {
		t.Errorf("resolveBoundRunSettings(conv-dormant) = %+v, want %+v — yolo and the mode must be the ones a revive materialises, never the ones the entry persisted (#1487)", got, want)
	}

	// The read answered without materialising: still one live session, the
	// bootstrap, and the dormant id is still not in the live half.
	if live := pool.List(); len(live) != 1 || string(live[0].ID) != bootID {
		t.Errorf("live sessions after the read = %+v, want the bootstrap alone — request_session_settings must spawn nothing", live)
	}
	if _, err := pool.SettingsFor(sessions.SessionID(dormantID)); !errors.Is(err, sessions.ErrSessionNotFound) {
		t.Errorf("SettingsFor(dormant) err = %v, want ErrSessionNotFound", err)
	}
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
		return boundRunSettings{sessionID: "sess-a", model: "claude-opus-4-8", effort: "high", yolo: true, permissionMode: "bypassPermissions", live: true}, true
	}

	seam := runConfigFor(resolve, nil)
	if seam == nil {
		t.Fatal("runConfigFor(wired resolver, nil usage) returned nil, want a working seam reporting zero context figures")
	}
	got, ok := seam("conv-a")
	if !ok {
		t.Fatalf("seam(conv-a) ok = false, want true — an unwired usage half does not make a resolved conversation unresolvable")
	}
	want := relay.RunConfig{SessionID: "sess-a", Model: "claude-opus-4-8", Effort: "high", YOLO: true, PermissionMode: "bypassPermissions"}
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
		"conv-a": {sessionID: "sess-a", model: "claude-opus-4-8", effort: "high", yolo: true, permissionMode: "bypassPermissions", live: true},
		"conv-b": {sessionID: "sess-b", effort: "low", permissionMode: "acceptEdits", live: true},
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

	wantA := relay.RunConfig{SessionID: "sess-a", Model: "claude-opus-4-8", Effort: "high", YOLO: true, PermissionMode: "bypassPermissions", UsedTokens: 1000, WindowTokens: 200_000}
	wantB := relay.RunConfig{SessionID: "sess-b", Effort: "low", PermissionMode: "acceptEdits", UsedTokens: 150_000, WindowTokens: 1_000_000}
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

// TestRunConfigFor_DormantSessionReachesNoUsageRead is AC 4 of #2449: a
// conversation resolved from a DORMANT registry entry reports its id, model and
// effort with both context figures at zero, and the transcript reader is not
// reached at all.
//
// The recorded call count is the assertion, not the zeros. Without the gate the
// chain answers (0, 200000) rather than (0, 0) — sessionTranscriptDir returns ""
// for an id Pool.Lookup misses, and contextwindow.Read("") reports the DEFAULT
// window — and a zero window_tokens is this reply's published "do not render a
// percentage" sentinel. Reporting the default window beside a zero used count
// would claim a genuine fresh session on a channel that may be near full.
//
// The live row beside it is what keeps the gate from being satisfied by deleting
// the usage read outright.
func TestRunConfigFor_DormantSessionReachesNoUsageRead(t *testing.T) {
	t.Parallel()

	bound := map[string]boundRunSettings{
		"conv-live":    {sessionID: "sess-live", model: "claude-opus-4-8", effort: "high", permissionMode: "default", live: true},
		"conv-dormant": {sessionID: "sess-dormant", model: "claude-sonnet-4-5", effort: "low", permissionMode: "default"},
	}
	resolve := func(convID string) (boundRunSettings, bool) {
		b, ok := bound[convID]
		return b, ok
	}
	// Both ids carry figures, so a seam that read usage for the dormant one
	// reports them and is red on the values as well as on the call list.
	usage := &usageRecorder{figures: map[string][2]int{
		"sess-live":    {1000, 200_000},
		"sess-dormant": {7000, 200_000},
	}}

	seam := runConfigFor(resolve, usage.read)
	if seam == nil {
		t.Fatal("runConfigFor(wired, wired) returned nil, want a seam")
	}

	gotLive, okLive := seam("conv-live")
	gotDormant, okDormant := seam("conv-dormant")
	if !okLive || !okDormant {
		t.Fatalf("seam ok = (%v, %v) for (conv-live, conv-dormant), want (true, true) — a dormant binding is addressable, not a refusal", okLive, okDormant)
	}

	wantLive := relay.RunConfig{SessionID: "sess-live", Model: "claude-opus-4-8", Effort: "high", PermissionMode: "default", UsedTokens: 1000, WindowTokens: 200_000}
	wantDormant := relay.RunConfig{SessionID: "sess-dormant", Model: "claude-sonnet-4-5", Effort: "low", PermissionMode: "default"}
	if gotLive != wantLive {
		t.Errorf("seam(conv-live) = %+v, want %+v", gotLive, wantLive)
	}
	if gotDormant != wantDormant {
		t.Errorf("seam(conv-dormant) = %+v, want %+v — a session the daemon is not running has no transcript to read, so both context fields stay zero", gotDormant, wantDormant)
	}

	wantAsked := []string{"sess-live"}
	if asked := usage.calls(); !slices.Equal(asked, wantAsked) {
		t.Errorf("usage reader was asked for %q, want %q — a dormant session must reach no transcript read at all", asked, wantAsked)
	}
}

// TestSettingsUpdaterAdapter_CarriesPermissionMode pins the WRITE half of the
// same chain against a real *sessions.Pool (#1687). The adapter mirrors
// relay.SettingsUpdate into sessions.SettingsUpdate field by field, by hand, so a
// field added to one side and forgotten on the other compiles and ships silently
// — the relay-side table proves only that the pointer reaches the seam, not that
// the seam's implementation forwards it.
//
// The rejected-mode case is the precise mutant-killer, and it is why this test
// does not just assert the happy path. Pool.UpdateSettings validates the mode
// only when the update NAMES one, so an adapter that dropped the field would make
// this frame an empty no-op and return nil. A non-nil error is therefore
// unforgeable evidence that the value crossed. It must also not be mapped to
// relay.ErrSessionUnknown, which is reserved for an id the daemon does not host.
func TestSettingsUpdaterAdapter_CarriesPermissionMode(t *testing.T) {
	t.Parallel()

	pool := newRouterTestPool(t)
	id := string(pool.Default().ID())
	adapter := settingsUpdaterAdapter{pool}

	mode := "plan"
	if err := adapter.UpdateSettings(id, relay.SettingsUpdate{PermissionMode: &mode}); err != nil {
		t.Fatalf("UpdateSettings(%q): unexpected err %v", mode, err)
	}
	got, err := pool.SettingsFor(sessions.SessionID(id))
	if err != nil {
		t.Fatalf("SettingsFor: %v", err)
	}
	if got.PermissionMode != mode {
		t.Errorf("stored PermissionMode = %q, want %q — the pointer did not cross the adapter", got.PermissionMode, mode)
	}

	// A mode the pool refuses: the error must surface as itself, not as nil (the
	// dropped-field signature) and not as the unknown-session sentinel.
	bogus := "not-a-mode"
	err = adapter.UpdateSettings(id, relay.SettingsUpdate{PermissionMode: &bogus})
	if err == nil {
		t.Fatal("UpdateSettings(bogus mode) = nil, want a rejection — a nil here means the field never reached the pool")
	}
	if errors.Is(err, relay.ErrSessionUnknown) {
		t.Errorf("UpdateSettings(bogus mode) = %v, want a validation error, not the unknown-session sentinel", err)
	}
	if after, ferr := pool.SettingsFor(sessions.SessionID(id)); ferr != nil || after.PermissionMode != mode {
		t.Errorf("stored PermissionMode = %q (err %v), want %q unchanged — a refused mode must persist nothing", after.PermissionMode, ferr, mode)
	}
}
