package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// modelListPlan is the per-pool-session answer table modelListRunner reads at
// CALL time rather than at construction. The indirection is load-bearing for the
// bootstrap session: sessions.New invokes the runner factory while building it,
// so the test cannot know the bootstrap id until New has returned and the runner
// already exists. Arming by id afterwards is what lets the refusal cases below
// give the bootstrap a distinguishable list.
//
// The mutex is not decorative: the isolation test runs Pool.Run, whose lifecycle
// goroutines hold the same runner values the test goroutine arms through.
type modelListPlan struct {
	mu   sync.Mutex
	byID map[sessions.SessionID]turnevent.ModelList
}

func newModelListPlan() *modelListPlan {
	return &modelListPlan{byID: map[sessions.SessionID]turnevent.ModelList{}}
}

// arm makes id's runner report list. A session with NO entry reports the
// unreported state instead, which is the AC 2 fixture — the plan's zero state is
// "this child never answered initialize".
func (p *modelListPlan) arm(id sessions.SessionID, list turnevent.ModelList) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.byID[id] = list
}

func (p *modelListPlan) get(id sessions.SessionID) (turnevent.ModelList, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	list, ok := p.byID[id]
	return list, ok
}

// modelListRunner is stubRunner plus the ONE concrete method
// resolveBoundModelList asserts for. stubRunner itself deliberately does not
// implement it and therefore stays the ready-made not-implemented fixture — see
// the runner-lacks-the-method test below, which builds its pool with
// newRouterTestPool for exactly that reason.
type modelListRunner struct {
	stubRunner
	id   sessions.SessionID
	plan *modelListPlan
}

func (r modelListRunner) ModelList() (turnevent.ModelList, bool) { return r.plan.get(r.id) }

// newModelListTestPool builds a real *sessions.Pool whose every session's runner
// answers from the returned plan. RegistryPath points into a fresh temp dir so
// Pool.Create has a resolvable data dir for the per-session settings file; a
// cold start there mints a bootstrap without spawning claude, exactly as
// newRouterTestPool relies on.
func newModelListTestPool(t *testing.T) (*sessions.Pool, *modelListPlan) {
	t.Helper()
	plan := newModelListPlan()
	pool, err := sessions.New(sessions.Config{
		Bootstrap:    sessions.SessionConfig{ClaudeBin: os.Args[0]},
		RegistryPath: filepath.Join(t.TempDir(), "sessions.json"),
		RunnerFactory: func(cfg sessions.RunnerConfig) (sessions.Runner, error) {
			return modelListRunner{id: sessions.SessionID(cfg.SessionID), plan: plan}, nil
		},
	})
	if err != nil {
		t.Fatalf("sessions.New: %v", err)
	}
	return pool, plan
}

// sentinelModelList builds a two-entry list in which every string carries tag
// and the two entries differ in every field, so a transposed entry, a crossed
// field or a sibling session's menu is visible rather than plausible.
//
// The values are CONSPICUOUS SENTINELS rather than realistic model names, for
// modelAnnouncedFixture's measured reason: the AC 4 negative below is a
// strings.Contains over a whole captured log, and a natural value like "sonnet"
// or "model" is a substring of unrelated text, which would make the negative RED
// against a correct implementation.
//
// Entry 2 is the deliberately awkward one — nil EffortLevels, nil
// TruncatedFields, false SupportsAutoMode — so a resolver that allocates an
// empty slice, hard-codes the bool, or normalises here (normalisation is
// ModelListPayload.MarshalJSON's job) is visible. A non-zero DroppedModels
// reddens a hard-coded 0.
func sentinelModelList(tag string) turnevent.ModelList {
	return turnevent.ModelList{
		Models: []turnevent.ModelOption{
			{
				ResolvedModel:    "ZZRESOLVEDONE" + tag + "ZZ",
				Value:            "ZZVALUEONE" + tag + "ZZ",
				DisplayName:      "ZZDISPLAYONE" + tag + "ZZ",
				EffortLevels:     []string{"ZZEFFORTONEA" + tag + "ZZ", "ZZEFFORTONEB" + tag + "ZZ"},
				SupportsAutoMode: true,
				TruncatedFields:  []string{"ZZTRUNCONE" + tag + "ZZ"},
			},
			{
				ResolvedModel:    "ZZRESOLVEDTWO" + tag + "ZZ",
				Value:            "ZZVALUETWO" + tag + "ZZ",
				DisplayName:      "ZZDISPLAYTWO" + tag + "ZZ",
				EffortLevels:     nil,
				SupportsAutoMode: false,
				TruncatedFields:  nil,
			},
		},
		DroppedModels: 3,
	}
}

// assertPayloadCarries compares a resolved payload against the retained event it
// was mapped from, field by field across all six ModelOption fields and in
// order. Comparing against the SOURCE rather than a hand-spelled copy keeps this
// file from re-tabling internal/turnbridge's mapping (which owns and pins it)
// while still catching a transposed entry or a crossed field, because
// sentinelModelList makes every value distinct.
func assertPayloadCarries(t *testing.T, got protocol.ModelListPayload, want turnevent.ModelList) {
	t.Helper()
	if len(got.Models) != len(want.Models) {
		t.Fatalf("payload carries %d models, want %d: %+v", len(got.Models), len(want.Models), got.Models)
	}
	for i := range want.Models {
		w, g := want.Models[i], got.Models[i]
		if g.ResolvedModel != w.ResolvedModel {
			t.Errorf("Models[%d].ResolvedModel = %q, want %q", i, g.ResolvedModel, w.ResolvedModel)
		}
		if g.Value != w.Value {
			t.Errorf("Models[%d].Value = %q, want %q", i, g.Value, w.Value)
		}
		if g.DisplayName != w.DisplayName {
			t.Errorf("Models[%d].DisplayName = %q, want %q", i, g.DisplayName, w.DisplayName)
		}
		if !reflect.DeepEqual(g.EffortLevels, w.EffortLevels) {
			t.Errorf("Models[%d].EffortLevels = %#v, want %#v", i, g.EffortLevels, w.EffortLevels)
		}
		if g.SupportsAutoMode != w.SupportsAutoMode {
			t.Errorf("Models[%d].SupportsAutoMode = %v, want %v", i, g.SupportsAutoMode, w.SupportsAutoMode)
		}
		if !reflect.DeepEqual(g.TruncatedFields, w.TruncatedFields) {
			t.Errorf("Models[%d].TruncatedFields = %#v, want %#v", i, g.TruncatedFields, w.TruncatedFields)
		}
	}
}

// #1857 AC 1: a conversation bound to a session whose runner holds a reported
// list resolves to a marshal-ready payload carrying THAT conversation's id and
// THAT session's models.
//
// DroppedModels and ConversationID are asserted against literals rather than
// against the fixture's own fields, so the two values that a caller could
// plausibly re-derive (a count from len(Models), an id reflected from the convID
// parameter) are pinned to what was actually configured.
func TestResolveBoundModelList_ResolvesTheBoundSessionsMenu(t *testing.T) {
	t.Parallel()

	pool, plan := newModelListTestPool(t)
	held := sentinelModelList("BOUND")
	plan.arm(pool.BootstrapID(), held)

	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{
		ID:               "conv-bound",
		CurrentSessionID: string(pool.BootstrapID()),
		LastUsedAt:       time.Now().UTC(),
	})

	got, ok := resolveBoundModelList(reg, pool, "conv-bound")
	if !ok {
		t.Fatalf("resolveBoundModelList(conv-bound) refused; want the bound session's menu")
	}
	if got.ConversationID != "conv-bound" {
		t.Errorf("ConversationID = %q, want %q", got.ConversationID, "conv-bound")
	}
	if got.DroppedModels != 3 {
		t.Errorf("DroppedModels = %d, want 3 (the retained count, never recomputed from len(Models))", got.DroppedModels)
	}
	assertPayloadCarries(t, got, held)
}

// #2124 AC 3: an id the registry does not carry — the empty id included — answers
// "no list" and hands back the ZERO payload EVEN WHILE a vocabulary is retained.
// The registry lookup is the ONE arm #2124 left as a hard refusal, and it is the
// whole security boundary: the daemon-wide fallback is reachable only through a
// resolved registry record, so no caller-supplied id is ever answered from the
// bootstrap's menu.
//
// This was #1857's four-row refusal table. Its `unbound conversation` and
// `binding names a session the pool lacks` rows have MOVED to
// TestResolveBoundModelList_FallsBackToTheDaemonWideVocabulary, which is where
// they now resolve; what stays here is the pair that must never move.
//
// The bootstrap session is armed with a distinguishable list, which is what makes
// the boundary's mutant SOLE-RED rather than invisible: drop the registry
// refusal — let an unresolved id fall through to the fallback the way every arm
// below it now does — and both rows flip to ok == true carrying ZZ...BOOTSTRAPZZ
// models under an id the daemon never minted. An unarmed bootstrap would flip
// them to ok == false and pin nothing. The two leak assertions are that mutant's
// only red and are not decorative.
func TestResolveBoundModelList_RefusesAnIdTheRegistryDoesNotCarry(t *testing.T) {
	t.Parallel()

	pool, plan := newModelListTestPool(t)
	plan.arm(pool.BootstrapID(), sentinelModelList("BOOTSTRAP"))

	// One resolvable row, so the fixture proves the vocabulary really is reachable
	// from this pool — otherwise "refused" would be indistinguishable from "nothing
	// was retained to leak in the first place" and the rows below would pin nothing.
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{
		ID:               "conv-resolvable",
		CurrentSessionID: string(pool.BootstrapID()),
		LastUsedAt:       time.Now().UTC(),
	})
	if _, ok := resolveBoundModelList(reg, pool, "conv-resolvable"); !ok {
		t.Fatal("the fixture's own resolvable conversation refused; the rows below would pin nothing")
	}

	tests := []struct {
		name   string
		convID string
		why    string
	}{
		{"unknown conversation", "conv-does-not-exist", "the registry has no such record, so the fallback is never reached"},
		{"empty conversation id", "", "no conversation carries an empty id, so the registry misses and nothing below runs"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := resolveBoundModelList(reg, pool, tc.convID)
			if ok {
				t.Fatalf("resolveBoundModelList(%q) = (%+v, true), want a refusal — %s", tc.convID, got, tc.why)
			}
			if !reflect.DeepEqual(got, protocol.ModelListPayload{}) {
				t.Errorf("refusal returned %+v, want the zero payload", got)
			}
			if strings.Contains(got.ConversationID, "BOOTSTRAP") || len(got.Models) > 0 {
				t.Errorf("refusal leaked the bootstrap's menu to an id the registry does not carry: %+v", got)
			}
		})
	}
}

// #2124 AC 1: a conversation the registry carries whose OWN BINDING yields no
// list resolves to the daemon-wide vocabulary, stamped with that conversation's
// own id. All three arms below the registry lookup fall back — no session bound,
// a binding the pool cannot resolve, and a bound session with no list to give.
//
// The assertion is an IDENTITY COMPARISON against the baseline rather than
// against the fixture: AC 1 says the models, effort levels and dropped_models
// must be identical to what the same retained vocabulary produces for a
// conversation bound to the session holding it, so the baseline is resolved from
// this same registry and compared field-for-field with only ConversationID
// substituted. Comparing each row against sentinelModelList instead would pin the
// contents but not the "identical to the bound reading" half.
func TestResolveBoundModelList_FallsBackToTheDaemonWideVocabulary(t *testing.T) {
	t.Parallel()

	pool, plan := newModelListTestPool(t)
	daemonWide := sentinelModelList("DAEMONWIDE")
	plan.arm(pool.BootstrapID(), daemonWide)

	// A second pool session whose runner implements ModelList but has nothing
	// armed: the "bound session holds no list" arm, one step deeper than the two
	// that never reach a hold at all. Pool.Create schedules on the run group, so
	// the pool has to be running.
	ctx := runPoolReady(t, pool)
	silent, err := pool.Create(ctx, "session-silent")
	if err != nil {
		t.Fatalf("Pool.Create: %v", err)
	}

	now := time.Now().UTC()
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: "conv-bootstrap-bound", CurrentSessionID: string(pool.BootstrapID()), LastUsedAt: now})
	reg.Create(conversations.Conversation{ID: "conv-unbound", CurrentSessionID: "", LastUsedAt: now})
	reg.Create(conversations.Conversation{ID: "conv-dangling", CurrentSessionID: "session-not-in-pool", LastUsedAt: now})
	reg.Create(conversations.Conversation{ID: "conv-silent", CurrentSessionID: string(silent), LastUsedAt: now})

	baseline, ok := resolveBoundModelList(reg, pool, "conv-bootstrap-bound")
	if !ok {
		t.Fatal("the conversation bound to the session HOLDING the vocabulary refused; there is no baseline to compare against")
	}

	tests := []struct {
		name   string
		convID string
		why    string
	}{
		{"no session bound", "conv-unbound", "CurrentSessionID is empty"},
		{"binding the pool cannot resolve", "conv-dangling", "Pool.Lookup reports ErrSessionNotFound"},
		{"bound session holds no list", "conv-silent", "the bound runner implements ModelList and reports the unreported state"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := resolveBoundModelList(reg, pool, tc.convID)
			if !ok {
				t.Fatalf("resolveBoundModelList(%q) refused; want the daemon-wide vocabulary — %s", tc.convID, tc.why)
			}
			if got.ConversationID != tc.convID {
				t.Errorf("ConversationID = %q, want %q — the fallback stamps the RESOLVED RECORD's id, never the holding session's conversation", got.ConversationID, tc.convID)
			}
			want := baseline
			want.ConversationID = tc.convID
			if !reflect.DeepEqual(got, want) {
				t.Errorf("fallback payload differs from the bound reading of the same vocabulary:\n got %+v\nwant %+v", got, want)
			}
			assertPayloadCarries(t, got, daemonWide)
		})
	}
}

// #2124 AC 2: a conversation whose bound session holds its OWN list still
// resolves from that session — the daemon-wide value never overrides a live
// child's own report.
//
// Two DIFFERENT sentinel lists are armed, which is the whole test: with one list
// armed on both sessions a resolver that read the bootstrap first would be
// indistinguishable from one that defers to it. This is the sole red for an
// ordering mistake in the fallback, which no AC 1 row can catch because every row
// there has nothing of its own to prefer.
func TestResolveBoundModelList_TheBoundSessionsOwnListWins(t *testing.T) {
	t.Parallel()

	pool, plan := newModelListTestPool(t)
	ctx := runPoolReady(t, pool)
	own, err := pool.Create(ctx, "session-own")
	if err != nil {
		t.Fatalf("Pool.Create: %v", err)
	}
	plan.arm(pool.BootstrapID(), sentinelModelList("DAEMONWIDE"))
	ownList := sentinelModelList("OWN")
	plan.arm(own, ownList)

	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{
		ID:               "conv-own",
		CurrentSessionID: string(own),
		LastUsedAt:       time.Now().UTC(),
	})

	got, ok := resolveBoundModelList(reg, pool, "conv-own")
	if !ok {
		t.Fatal("resolveBoundModelList(conv-own) refused; the bound session holds its own list")
	}
	assertPayloadCarries(t, got, ownList)
	// Stated separately rather than left implicit above: the failure this test
	// exists to catch is the bootstrap's menu overriding a live child's own, and a
	// reader should not have to compare two sentinel tags to see it.
	for _, m := range got.Models {
		if strings.Contains(m.Value, "DAEMONWIDE") {
			t.Fatalf("the daemon-wide vocabulary overrode the bound session's own report: %q", m.Value)
		}
	}
}

// #2124 AC 4: with NO vocabulary retained anywhere, every arm below the registry
// resolves to nothing and hands back the zero payload. Absence stays the only
// "no list" signal — no empty Models array standing in for "unknown".
//
// Nothing is armed, so the fallback source is as silent as the binding. This is
// the negative half of TestResolveBoundModelList_FallsBackToTheDaemonWideVocabulary
// and shares its three rows exactly, so a mutant that answered true with an empty
// payload on any arm is red here rather than in a row this file does not have.
func TestResolveBoundModelList_NoVocabularyRetainedAnywhere(t *testing.T) {
	t.Parallel()

	pool, _ := newModelListTestPool(t)
	ctx := runPoolReady(t, pool)
	silent, err := pool.Create(ctx, "session-silent")
	if err != nil {
		t.Fatalf("Pool.Create: %v", err)
	}

	now := time.Now().UTC()
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: "conv-unbound", CurrentSessionID: "", LastUsedAt: now})
	reg.Create(conversations.Conversation{ID: "conv-dangling", CurrentSessionID: "session-not-in-pool", LastUsedAt: now})
	reg.Create(conversations.Conversation{ID: "conv-silent", CurrentSessionID: string(silent), LastUsedAt: now})

	for _, convID := range []string{"conv-unbound", "conv-dangling", "conv-silent"} {
		t.Run(convID, func(t *testing.T) {
			t.Parallel()
			got, ok := resolveBoundModelList(reg, pool, convID)
			if ok {
				t.Fatalf("resolveBoundModelList(%q) = (%+v, true); nothing is retained anywhere, so there is no vocabulary to answer with", convID, got)
			}
			if !reflect.DeepEqual(got, protocol.ModelListPayload{}) {
				t.Errorf("refusal returned %+v, want the zero payload", got)
			}
		})
	}
}

// #2124 AC 4, the nil-bootstrap half: a pool with NO bootstrap to read from
// resolves to nothing rather than panicking. Pool.Default returns
// p.sessions[p.bootstrap] and is nil on a map miss — the case Pool.DefaultSettings
// documents and answers with a comma-ok — and sessions.Config.BootstrapEvicted
// (set by pyry acp) reaches the same "no vocabulary to read" outcome by a
// different route.
//
// A zero-value &sessions.Pool{} is the fixture because it is the one shape whose
// Default() is nil, which newModelListTestPool's cold start never produces. The
// mutant this is sole-red for is dropping the nil guard in front of the fallback's
// Runner() read: Session.Runner has a POINTER RECEIVER that dereferences
// (`func (s *Session) Runner() Runner { return s.sup }`), so a nil session panics
// rather than failing the type assertion cleanly — which is exactly why the twin
// resolvers' "a nil Runner fails the assertion cleanly, so no nil check is needed"
// sentence does NOT carry over to a session that is itself nil.
//
// Both rows also cross Pool.Lookup on a zero-value pool, whose nil session map
// answers ErrSessionNotFound for the dangling id — so neither the lookup nor the
// fallback may assume a constructed pool.
func TestResolveBoundModelList_NoBootstrapToFallBackTo(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: "conv-unbound", CurrentSessionID: "", LastUsedAt: now})
	reg.Create(conversations.Conversation{ID: "conv-dangling", CurrentSessionID: "session-not-in-pool", LastUsedAt: now})

	for _, convID := range []string{"conv-unbound", "conv-dangling"} {
		t.Run(convID, func(t *testing.T) {
			t.Parallel()
			got, ok := resolveBoundModelList(reg, &sessions.Pool{}, convID)
			if ok {
				t.Fatalf("resolveBoundModelList(%q) = (%+v, true); a pool with no bootstrap holds no vocabulary", convID, got)
			}
			if !reflect.DeepEqual(got, protocol.ModelListPayload{}) {
				t.Errorf("refusal returned %+v, want the zero payload", got)
			}
		})
	}
}

// #1857 AC 2: a session whose runner implements ModelList but has nothing
// retained answers "no list" through the comma-ok rather than an empty payload —
// an empty model set is not a value any caller can be handed. Split out of the
// table above because the resolution here succeeds all the way DOWN to the hold
// and refuses there, which is a different arm from every row above; it is the
// sole red for a `list, _ := lister.ModelList()` simplification.
func TestResolveBoundModelList_UnreportedSessionAnswersNoList(t *testing.T) {
	t.Parallel()

	pool, _ := newModelListTestPool(t)
	// Nothing armed: the bootstrap's runner implements ModelList and reports the
	// unreported state.
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{
		ID:               "conv-silent",
		CurrentSessionID: string(pool.BootstrapID()),
		LastUsedAt:       time.Now().UTC(),
	})

	got, ok := resolveBoundModelList(reg, pool, "conv-silent")
	if ok {
		t.Fatalf("resolveBoundModelList = (%+v, true); a session that reported nothing must answer no list", got)
	}
	if len(got.Models) != 0 {
		t.Errorf("refusal carried %d models, want none", len(got.Models))
	}
	if !reflect.DeepEqual(got, protocol.ModelListPayload{}) {
		t.Errorf("refusal returned %+v, want the zero payload", got)
	}
}

// #1857 AC 3: a runner that does not implement ModelList is a REFUSAL, not a
// panic. newRouterTestPool's plain stubRunner is the fixture — it satisfies
// sessions.Runner and nothing more, which is exactly the shape a non-stream-json
// runner has.
func TestResolveBoundModelList_RunnerWithoutTheMethodRefuses(t *testing.T) {
	t.Parallel()

	pool := newRouterTestPool(t)
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{
		ID:               "conv-plain",
		CurrentSessionID: string(pool.BootstrapID()),
		LastUsedAt:       time.Now().UTC(),
	})

	got, ok := resolveBoundModelList(reg, pool, "conv-plain")
	if ok {
		t.Fatalf("resolveBoundModelList = (%+v, true); a runner without ModelList must refuse", got)
	}
	if !reflect.DeepEqual(got, protocol.ModelListPayload{}) {
		t.Errorf("refusal returned %+v, want the zero payload", got)
	}
}

// #1857 AC 3, second half: two conversations bound to two DIFFERENT pool
// sessions each get their own session's menu and never the sibling's. This is
// the pin a hard-coded lookup, a shared cache, or a resolver keyed on the wrong
// hop cannot pass.
//
// Not parallel: t.Setenv confines anything the pool's create path might resolve
// out of HOME, and t.Setenv forbids t.Parallel.
func TestResolveBoundModelList_IsolatesConversations(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	pool, plan := newModelListTestPool(t)
	// Pool.Create schedules the new session on the run group, so the pool has to
	// be running or supervise returns ErrPoolNotRunning.
	ctx := runPoolReady(t, pool)
	sessA := pool.BootstrapID()
	sessB, err := pool.Create(ctx, "session-b")
	if err != nil {
		t.Fatalf("Pool.Create: %v", err)
	}

	listA, listB := sentinelModelList("ALPHA"), sentinelModelList("BETA")
	plan.arm(sessA, listA)
	plan.arm(sessB, listB)

	now := time.Now().UTC()
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: "conv-a", CurrentSessionID: string(sessA), LastUsedAt: now})
	reg.Create(conversations.Conversation{ID: "conv-b", CurrentSessionID: string(sessB), LastUsedAt: now})

	gotA, ok := resolveBoundModelList(reg, pool, "conv-a")
	if !ok {
		t.Fatal("resolveBoundModelList(conv-a) refused")
	}
	if gotA.ConversationID != "conv-a" {
		t.Errorf("conv-a payload names %q", gotA.ConversationID)
	}
	assertPayloadCarries(t, gotA, listA)

	gotB, ok := resolveBoundModelList(reg, pool, "conv-b")
	if !ok {
		t.Fatal("resolveBoundModelList(conv-b) refused")
	}
	if gotB.ConversationID != "conv-b" {
		t.Errorf("conv-b payload names %q", gotB.ConversationID)
	}
	assertPayloadCarries(t, gotB, listB)

	// Stated as its own assertion rather than left implicit in the two above: the
	// failure this test exists to catch is A being answered with B's menu, and a
	// reader should not have to compare two sentinel tags to see that.
	if gotA.Models[0].Value == gotB.Models[0].Value {
		t.Fatalf("both conversations resolved to the same menu: %q", gotA.Models[0].Value)
	}
}

// #1857 AC 4: no model value, display name or effort level reaches a log record
// on this path at any level.
//
// What this pins, honestly: the function takes no *slog.Logger, so the
// STRUCTURAL half of AC 4 is enforced by the signature and by review, not here.
// What this catches is the one real regression shape — someone reaching for the
// package-level slog.Info / slog.Default() instead of adding a parameter. A
// mutant adding slog.Info("resolved", "models", list.Models) to the happy path
// reddens it; nothing else in the package does.
//
// It must NOT call t.Parallel: slog.SetDefault is process-global and would race
// every other parallel test in this package. The pool is built BEFORE the
// default is swapped, so sessions.New captures the old default and the pool's
// own diagnostics cannot land in this buffer — the only writer left is the code
// under test.
func TestResolveBoundModelList_LogsNothing(t *testing.T) {
	pool, plan := newModelListTestPool(t)
	held := sentinelModelList("LOGNEG")
	plan.arm(pool.BootstrapID(), held)

	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{
		ID:               "conv-logneg",
		CurrentSessionID: string(pool.BootstrapID()),
		LastUsedAt:       time.Now().UTC(),
	})

	var buf bytes.Buffer
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	got, ok := resolveBoundModelList(reg, pool, "conv-logneg")
	if !ok {
		t.Fatalf("resolveBoundModelList refused; the log negative needs the happy path")
	}
	// Refused resolutions are the other half: the untrusted convID must not reach
	// a log line either.
	if _, ok := resolveBoundModelList(reg, pool, "conv-ZZUNTRUSTEDZZ"); ok {
		t.Fatal("resolveBoundModelList resolved an unknown conversation")
	}

	logs := buf.String()
	if logs != "" {
		t.Fatalf("resolveBoundModelList wrote %d bytes of log; it must write none:\n%s", len(logs), logs)
	}
	// Belt-and-braces on the values themselves, so a future handler swap that made
	// the emptiness check weaker still names what leaked.
	for _, m := range got.Models {
		for _, v := range append([]string{m.ResolvedModel, m.Value, m.DisplayName}, m.EffortLevels...) {
			if v != "" && strings.Contains(logs, v) {
				t.Errorf("a model value leaked into a log record: %q", v)
			}
		}
	}
	if strings.Contains(logs, "ZZUNTRUSTEDZZ") {
		t.Errorf("the caller's untrusted conversation id leaked into a log record:\n%s", logs)
	}
}

// indexByConversation keys an enumeration's result on ConversationID, which is
// how every assertion below reads it: retainedModelLists returns List's order
// (registry insertion order) and that is deliberately NOT a contract — the
// client correlates on conversation_id and reconcileModelLists sends one
// envelope per payload. It also fails a duplicated id, which is the shape a
// loop that appended the same row twice would take.
func indexByConversation(t *testing.T, got []protocol.ModelListPayload) map[string]protocol.ModelListPayload {
	t.Helper()
	byID := make(map[string]protocol.ModelListPayload, len(got))
	for _, p := range got {
		if _, dup := byID[p.ConversationID]; dup {
			t.Fatalf("conversation %q contributed twice: %+v", p.ConversationID, got)
		}
		byID[p.ConversationID] = p
	}
	return byID
}

// #1867 AC 1: one conversation bound to a session holding a retained list yields
// exactly one payload, carrying that session's entries in claude's order under
// that conversation's id.
//
// The bootstrap session is armed and bound, so a mutant that returned the zero
// payload or an empty slice is red on the count as well as on the contents.
func TestRetainedModelLists_EnumeratesTheBoundSessionsMenu(t *testing.T) {
	t.Parallel()

	pool, plan := newModelListTestPool(t)
	held := sentinelModelList("ENUM")
	plan.arm(pool.BootstrapID(), held)

	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{
		ID:               "conv-enum",
		CurrentSessionID: string(pool.BootstrapID()),
		LastUsedAt:       time.Now().UTC(),
	})

	got := retainedModelLists(reg, pool)()
	if len(got) != 1 {
		t.Fatalf("retainedModelLists returned %d payloads, want exactly 1: %+v", len(got), got)
	}
	if got[0].ConversationID != "conv-enum" {
		t.Errorf("ConversationID = %q, want %q", got[0].ConversationID, "conv-enum")
	}
	if got[0].DroppedModels != 3 {
		t.Errorf("DroppedModels = %d, want 3 (carried from the retained list, never recomputed)", got[0].DroppedModels)
	}
	assertPayloadCarries(t, got[0], held)
}

// #2124 AC 1 at the enumerator: once ANY vocabulary is retained, EVERY row the
// registry carries contributes exactly one payload, whatever its own session
// state. This replaces #1867's TestRetainedModelLists_SkipsEachRefusalAndKeepsGoing,
// whose premise — three refusals behind one survivor — no longer exists, because
// the only arm that still refuses is the registry miss and no id reaching this
// loop can miss: every one of them came out of List.
//
// The mutant coverage is REPLACED rather than dropped, and the replacement is a
// different mutant because the old one is now equivalent. #1867's fixture order
// (contributor last) existed to redden a `break`-instead-of-`continue`; with no
// reachable refusal the `continue` never executes, so that mutation changes
// nothing. What is still reachable and still load-bearing is an early exit AFTER
// an append — `out = append(...); break`, or a loop that returns the first
// payload — and a four-row registry in which every row contributes is sole-red
// for it on the count alone. The four rows are the four distinct paths through
// the resolver (bound-and-holding, unbound, dangling, bound-but-silent), so the
// count also pins that no path drops a row.
//
// All four live in ONE registry on purpose: a payload carrying a sibling's models
// or a duplicated id is red here and invisible in four single-row registries.
func TestRetainedModelLists_EveryRowContributesOnceAVocabularyIsRetained(t *testing.T) {
	t.Parallel()

	pool, plan := newModelListTestPool(t)
	held := sentinelModelList("DAEMONWIDE")
	plan.arm(pool.BootstrapID(), held)

	// A second pool session whose runner implements ModelList but has nothing
	// armed: the bound-but-silent path, one step deeper than the two that never
	// reach a hold. Bound to its own row so the path is reached through the
	// registry rather than asserted directly.
	ctx := runPoolReady(t, pool)
	silent, err := pool.Create(ctx, "session-silent")
	if err != nil {
		t.Fatalf("Pool.Create: %v", err)
	}

	now := time.Now().UTC()
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: "conv-silent", CurrentSessionID: string(silent), LastUsedAt: now})
	reg.Create(conversations.Conversation{ID: "conv-unbound", CurrentSessionID: "", LastUsedAt: now})
	reg.Create(conversations.Conversation{ID: "conv-dangling", CurrentSessionID: "session-not-in-pool", LastUsedAt: now})
	reg.Create(conversations.Conversation{ID: "conv-live", CurrentSessionID: string(pool.BootstrapID()), LastUsedAt: now})

	got := retainedModelLists(reg, pool)()
	if len(got) != 4 {
		t.Fatalf("retainedModelLists returned %d payloads, want 4 — every registry row contributes once a vocabulary is retained: %+v", len(got), got)
	}
	byID := indexByConversation(t, got)
	for _, convID := range []string{"conv-silent", "conv-unbound", "conv-dangling", "conv-live"} {
		p, ok := byID[convID]
		if !ok {
			t.Fatalf("%q contributed no payload: %+v", convID, got)
		}
		assertPayloadCarries(t, p, held)
	}
	// An empty menu is never presented as a real one: no payload may carry zero
	// models, whichever path produced it.
	for _, p := range got {
		if len(p.Models) == 0 {
			t.Errorf("payload for %q carries an empty menu; a row with nothing to say must contribute nothing at all", p.ConversationID)
		}
	}
}

// #2124 AC 4 at the enumerator, plus #1867's nothing-to-send half: no payloads
// and no panic. Two rows, two pools, because the two "nothing to send" states are
// now genuinely different fixtures rather than two registries over one pool.
//
// `no vocabulary retained anywhere` is what #1867's `every row refuses` row
// became: those same two rows now resolve whenever the bootstrap holds a list, so
// the fixture that makes them silent is an UNARMED pool rather than an unbound
// registry. It is the enumerator's half of AC 4 — no bootstrap reply yet, and
// therefore no model_list frame for any conversation.
//
// `empty registry` keeps its own pool ARMED, which is the point of splitting them:
// it is the sole red for a mutant that enumerated the POOL instead of the
// registry, and an unarmed bootstrap would hide that.
func TestRetainedModelLists_NothingToSend(t *testing.T) {
	t.Parallel()

	t.Run("empty registry", func(t *testing.T) {
		t.Parallel()
		pool, plan := newModelListTestPool(t)
		plan.arm(pool.BootstrapID(), sentinelModelList("UNREACHABLE"))
		if got := retainedModelLists(&conversations.Registry{}, pool)(); len(got) != 0 {
			t.Fatalf("retainedModelLists returned %d payloads over an empty registry, want none — the enumeration walks the registry, not the pool: %+v", len(got), got)
		}
	})

	t.Run("no vocabulary retained anywhere", func(t *testing.T) {
		t.Parallel()
		pool, _ := newModelListTestPool(t) // nothing armed: the bootstrap never answered initialize.
		now := time.Now().UTC()
		reg := &conversations.Registry{}
		reg.Create(conversations.Conversation{ID: "conv-unbound", CurrentSessionID: "", LastUsedAt: now})
		reg.Create(conversations.Conversation{ID: "conv-dangling", CurrentSessionID: "session-not-in-pool", LastUsedAt: now})
		reg.Create(conversations.Conversation{ID: "conv-bootstrap-bound", CurrentSessionID: string(pool.BootstrapID()), LastUsedAt: now})
		if got := retainedModelLists(reg, pool)(); len(got) != 0 {
			t.Fatalf("retainedModelLists returned %d payloads with nothing retained anywhere, want none: %+v", len(got), got)
		}
	})
}

// #1867: the enumeration is unfiltered, so an ARCHIVED conversation whose bound
// session still holds a list DOES contribute. Registry.SetArchived writes
// exactly one field and never unbinds CurrentSessionID, and the reconcile
// asserts current control truth — the client decides what to show.
//
// This is the sole red for a mutant that narrows the call to
// List(ListFilter{IsArchived: &f}), which is otherwise invisible: every other
// test here builds unarchived rows.
func TestRetainedModelLists_ArchivedConversationsContribute(t *testing.T) {
	t.Parallel()

	pool, plan := newModelListTestPool(t)
	held := sentinelModelList("ARCHIVED")
	plan.arm(pool.BootstrapID(), held)

	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{
		ID:               "conv-archived",
		CurrentSessionID: string(pool.BootstrapID()),
		IsArchived:       true,
		LastUsedAt:       time.Now().UTC(),
	})

	got := retainedModelLists(reg, pool)()
	if len(got) != 1 {
		t.Fatalf("retainedModelLists returned %d payloads, want 1 — archiving does not unbind the session: %+v", len(got), got)
	}
	if got[0].ConversationID != "conv-archived" {
		t.Errorf("ConversationID = %q, want %q", got[0].ConversationID, "conv-archived")
	}
	assertPayloadCarries(t, got[0], held)
}

// #1867: two conversations bound to two DIFFERENT pool sessions each carry their
// own session's menu across one enumeration. This is the pin a shared buffer, an
// off-by-one, or a loop-variable capture cannot pass — TestResolveBoundModelList_
// IsolatesConversations makes the same point one call at a time, and only the
// enumerator can cross two rows within a single result slice.
//
// Not parallel: t.Setenv confines anything the pool's create path resolves out
// of HOME, and t.Setenv forbids t.Parallel.
func TestRetainedModelLists_DoesNotCrossConversations(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	pool, plan := newModelListTestPool(t)
	ctx := runPoolReady(t, pool)
	sessA := pool.BootstrapID()
	sessB, err := pool.Create(ctx, "session-b")
	if err != nil {
		t.Fatalf("Pool.Create: %v", err)
	}

	listA, listB := sentinelModelList("ALPHAENUM"), sentinelModelList("BETAENUM")
	plan.arm(sessA, listA)
	plan.arm(sessB, listB)

	now := time.Now().UTC()
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: "conv-a", CurrentSessionID: string(sessA), LastUsedAt: now})
	reg.Create(conversations.Conversation{ID: "conv-b", CurrentSessionID: string(sessB), LastUsedAt: now})

	got := retainedModelLists(reg, pool)()
	if len(got) != 2 {
		t.Fatalf("retainedModelLists returned %d payloads, want 2: %+v", len(got), got)
	}
	byID := indexByConversation(t, got)
	gotA, ok := byID["conv-a"]
	if !ok {
		t.Fatalf("conv-a contributed no payload: %+v", got)
	}
	gotB, ok := byID["conv-b"]
	if !ok {
		t.Fatalf("conv-b contributed no payload: %+v", got)
	}
	assertPayloadCarries(t, gotA, listA)
	assertPayloadCarries(t, gotB, listB)
	// Stated separately for the same reason the resolver's twin states it: the
	// failure this test exists to catch is A being handed B's menu, and a reader
	// should not have to compare two sentinel tags to see it.
	if gotA.Models[0].Value == gotB.Models[0].Value {
		t.Fatalf("both conversations enumerated the same menu: %q", gotA.Models[0].Value)
	}
}

// #1867 AC 3: no model value — selectable value, resolved model or display name
// — and no effort level reaches a log record on this path at any level.
//
// What this pins, honestly: retainedModelLists takes no *slog.Logger, so the
// structural half of AC 3 is enforced by the signature and by review. What this
// catches is the one real regression shape — someone reaching for the
// package-level slog.Info / slog.Default() to explain why a row did not
// contribute. The registry deliberately holds BOTH a contributing row and the
// refusing ones, because that "why did this row skip" line is exactly where such
// a call would be added.
//
// It must NOT call t.Parallel: slog.SetDefault is process-global. The pool is
// built BEFORE the default is swapped, so sessions.New captures the old default
// and the pool's own diagnostics cannot land in this buffer.
//
// #2124 moved the count from 1 to 3: the unbound and dangling rows now resolve
// from the daemon-wide vocabulary rather than skipping. The registry deliberately
// keeps them, because the fallback is a NEW place a "why did this row not use its
// own session" line would be reached for, and that line's only possible content
// is a conversation id or the model values themselves.
func TestRetainedModelLists_LogsNothing(t *testing.T) {
	pool, plan := newModelListTestPool(t)
	held := sentinelModelList("ENUMLOGNEG")
	plan.arm(pool.BootstrapID(), held)

	now := time.Now().UTC()
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: "conv-logneg", CurrentSessionID: string(pool.BootstrapID()), LastUsedAt: now})
	reg.Create(conversations.Conversation{ID: "conv-ZZUNTRUSTEDZZ", CurrentSessionID: "", LastUsedAt: now})
	reg.Create(conversations.Conversation{ID: "conv-gone", CurrentSessionID: "session-not-in-pool", LastUsedAt: now})

	var buf bytes.Buffer
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	got := retainedModelLists(reg, pool)()
	if len(got) != 3 {
		t.Fatalf("retainedModelLists returned %d payloads, want 3; the log negative needs both the bound reading and the two fallbacks", len(got))
	}

	logs := buf.String()
	if logs != "" {
		t.Fatalf("retainedModelLists wrote %d bytes of log; it must write none:\n%s", len(logs), logs)
	}
	// Belt-and-braces on the values themselves, so a future handler swap that made
	// the emptiness check weaker still names what leaked.
	for _, m := range got[0].Models {
		for _, v := range append([]string{m.ResolvedModel, m.Value, m.DisplayName}, m.EffortLevels...) {
			if v != "" && strings.Contains(logs, v) {
				t.Errorf("a model value leaked into a log record: %q", v)
			}
		}
	}
	if strings.Contains(logs, "ZZUNTRUSTEDZZ") {
		t.Errorf("a skipped conversation's id leaked into a log record:\n%s", logs)
	}
}
