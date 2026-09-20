package main

import (
	"context"
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
	"github.com/pyrycode/pyrycode/internal/streamsup"
)

type effectiveEffortQueryResult struct {
	settings streamsup.AppliedSettings
	ok       bool
	block    bool
}

type effectiveEffortQueryPlan struct {
	mu sync.Mutex

	results       map[sessions.SessionID]effectiveEffortQueryResult
	queries       []sessions.SessionID
	deadlines     map[sessions.SessionID][]time.Time
	ordinaryTurns []sessions.SessionID
}

func newEffectiveEffortQueryPlan() *effectiveEffortQueryPlan {
	return &effectiveEffortQueryPlan{
		results:   make(map[sessions.SessionID]effectiveEffortQueryResult),
		deadlines: make(map[sessions.SessionID][]time.Time),
	}
}

func (p *effectiveEffortQueryPlan) arm(id sessions.SessionID, settings streamsup.AppliedSettings, ok bool) {
	p.mu.Lock()
	p.results[id] = effectiveEffortQueryResult{settings: settings, ok: ok}
	p.mu.Unlock()
}

func (p *effectiveEffortQueryPlan) block(id sessions.SessionID) {
	p.mu.Lock()
	p.results[id] = effectiveEffortQueryResult{block: true}
	p.mu.Unlock()
}

func (p *effectiveEffortQueryPlan) query(ctx context.Context, id sessions.SessionID) (streamsup.AppliedSettings, bool) {
	p.mu.Lock()
	p.queries = append(p.queries, id)
	if deadline, ok := ctx.Deadline(); ok {
		p.deadlines[id] = append(p.deadlines[id], deadline)
	} else {
		p.deadlines[id] = append(p.deadlines[id], time.Time{})
	}
	result := p.results[id]
	p.mu.Unlock()

	if result.block {
		<-ctx.Done()
		return streamsup.AppliedSettings{}, false
	}
	return result.settings, result.ok
}

func (p *effectiveEffortQueryPlan) recordOrdinaryTurn(id sessions.SessionID) {
	p.mu.Lock()
	p.ordinaryTurns = append(p.ordinaryTurns, id)
	p.mu.Unlock()
}

func (p *effectiveEffortQueryPlan) queryIDs() []sessions.SessionID {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.queries)
}

func (p *effectiveEffortQueryPlan) deadlineFor(id sessions.SessionID) (time.Time, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	deadlines := p.deadlines[id]
	if len(deadlines) == 0 {
		return time.Time{}, false
	}
	return deadlines[len(deadlines)-1], !deadlines[len(deadlines)-1].IsZero()
}

func (p *effectiveEffortQueryPlan) ordinaryTurnIDs() []sessions.SessionID {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.ordinaryTurns)
}

type effectiveEffortQueryRunner struct {
	stubRunner
	id   sessions.SessionID
	plan *effectiveEffortQueryPlan
}

func (r effectiveEffortQueryRunner) QueryAppliedSettings(ctx context.Context) (streamsup.AppliedSettings, bool) {
	return r.plan.query(ctx, r.id)
}

func (r effectiveEffortQueryRunner) WriteUserTurn(context.Context, string, []byte) error {
	r.plan.recordOrdinaryTurn(r.id)
	return nil
}

func newEffectiveEffortTestPool(t *testing.T) (*sessions.Pool, *effectiveEffortQueryPlan) {
	t.Helper()
	plan := newEffectiveEffortQueryPlan()
	pool, err := sessions.New(sessions.Config{
		Bootstrap:    sessions.SessionConfig{ClaudeBin: os.Args[0]},
		RegistryPath: filepath.Join(t.TempDir(), "sessions.json"),
		RunnerFactory: func(cfg sessions.RunnerConfig) (sessions.Runner, error) {
			return effectiveEffortQueryRunner{id: sessions.SessionID(cfg.SessionID), plan: plan}, nil
		},
	})
	if err != nil {
		t.Fatalf("sessions.New: %v", err)
	}
	return pool, plan
}

func mintEffectiveEffortSession(t *testing.T, pool *sessions.Pool, label string) sessions.SessionID {
	t.Helper()
	id, err := pool.Mint(label, "")
	if err != nil && !errors.Is(err, sessions.ErrPoolNotRunning) {
		t.Fatalf("pool.Mint(%q): %v", label, err)
	}
	if id == "" {
		t.Fatalf("pool.Mint(%q) returned an empty id", label)
	}
	return id
}

func createEffectiveEffortConversation(t *testing.T, reg *conversations.Registry, convID string, sessionID sessions.SessionID) {
	t.Helper()
	reg.Create(conversations.Conversation{
		ID:               conversations.ConversationID(convID),
		CurrentSessionID: string(sessionID),
		LastUsedAt:       time.Now().UTC(),
	})
}

func stringPointer(value string) *string { return &value }

func TestEffectiveEffortFor_IsolatesSavedAndAppliedValuesByConversation(t *testing.T) {
	t.Parallel()

	pool, plan := newEffectiveEffortTestPool(t)
	idA := mintEffectiveEffortSession(t, pool, "effort-a")
	idB := mintEffectiveEffortSession(t, pool, "effort-b")
	plan.arm(pool.BootstrapID(), streamsup.AppliedSettings{Effort: stringPointer("bootstrap-must-not-leak")}, true)
	plan.arm(idA, streamsup.AppliedSettings{Model: "model-a-must-not-cross", Effort: stringPointer("medium")}, true)
	plan.arm(idB, streamsup.AppliedSettings{Model: "model-b-must-not-cross", Effort: stringPointer("max")}, true)

	reg := &conversations.Registry{}
	createEffectiveEffortConversation(t, reg, "conv-a", idA)
	createEffectiveEffortConversation(t, reg, "conv-b", idB)

	saved := runConfigFor(func(convID string) (boundRunSettings, bool) {
		values := map[string]boundRunSettings{
			"conv-a": {sessionID: string(idA), model: "saved-model-a", effort: "high", live: true},
			"conv-b": {sessionID: string(idB), model: "saved-model-b", effort: "low", live: true},
		}
		value, ok := values[convID]
		return value, ok
	}, nil)
	applied := effectiveEffortFor(reg, pool)
	if applied == nil {
		t.Fatal("effectiveEffortFor(wired registry, wired pool) returned nil")
	}

	tests := []struct {
		conversation string
		wantSaved    relay.RunConfig
		wantApplied  string
	}{
		{conversation: "conv-a", wantSaved: relay.RunConfig{SessionID: string(idA), Model: "saved-model-a", Effort: "high"}, wantApplied: "medium"},
		{conversation: "conv-b", wantSaved: relay.RunConfig{SessionID: string(idB), Model: "saved-model-b", Effort: "low"}, wantApplied: "max"},
	}
	for _, tc := range tests {
		gotSaved, savedOK := saved(tc.conversation)
		if !savedOK || gotSaved != tc.wantSaved {
			t.Errorf("saved(%q) = (%+v,%v), want (%+v,true)", tc.conversation, gotSaved, savedOK, tc.wantSaved)
		}
		gotApplied, appliedOK := applied(context.Background(), tc.conversation)
		if !appliedOK || gotApplied == nil || *gotApplied != tc.wantApplied {
			t.Errorf("applied(%q) = (%v,%v), want (%q,true)", tc.conversation, gotApplied, appliedOK, tc.wantApplied)
		}
	}

	if got := plan.queryIDs(); !slices.Equal(got, []sessions.SessionID{idA, idB}) {
		t.Errorf("queried runners = %q, want [%q %q] in conversation order", got, idA, idB)
	}
	if got := plan.ordinaryTurnIDs(); len(got) != 0 {
		t.Errorf("ordinary user turns reached runners %q, want none", got)
	}
}

func TestResolveBoundEffectiveEffort_PreservesThreeOutcomes(t *testing.T) {
	t.Parallel()

	pool, plan := newEffectiveEffortTestPool(t)
	id := mintEffectiveEffortSession(t, pool, "three-outcomes")
	reg := &conversations.Registry{}
	createEffectiveEffortConversation(t, reg, "conv-outcomes", id)

	plan.arm(id, streamsup.AppliedSettings{Model: "ignored-model", Effort: stringPointer("high")}, true)
	got, ok := resolveBoundEffectiveEffort(context.Background(), reg, pool, "conv-outcomes")
	if !ok || got == nil || *got != "high" {
		t.Fatalf("string result = (%v,%v), want (high,true)", got, ok)
	}

	plan.arm(id, streamsup.AppliedSettings{Model: "ignored-model", Effort: nil}, true)
	got, ok = resolveBoundEffectiveEffort(context.Background(), reg, pool, "conv-outcomes")
	if !ok || got != nil {
		t.Fatalf("explicit-null result = (%v,%v), want (nil,true)", got, ok)
	}

	plan.arm(id, streamsup.AppliedSettings{Effort: stringPointer("poison-must-be-discarded")}, false)
	got, ok = resolveBoundEffectiveEffort(context.Background(), reg, pool, "conv-outcomes")
	if ok || got != nil {
		t.Fatalf("unavailable result = (%v,%v), want (nil,false)", got, ok)
	}
}

func TestResolveBoundEffectiveEffort_RefusesUnaddressableAndUnsupported(t *testing.T) {
	t.Parallel()

	pool, plan := newEffectiveEffortTestPool(t)
	plan.arm(pool.BootstrapID(), streamsup.AppliedSettings{Effort: stringPointer("bootstrap-must-not-leak")}, true)
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: "conv-unbound", LastUsedAt: time.Now().UTC()})
	reg.Create(conversations.Conversation{
		ID:               "conv-dangling",
		CurrentSessionID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		LastUsedAt:       time.Now().UTC(),
	})

	for _, convID := range []string{"", "conv-unknown", "conv-unbound", "conv-dangling"} {
		got, ok := resolveBoundEffectiveEffort(context.Background(), reg, pool, convID)
		if ok || got != nil {
			t.Errorf("resolveBoundEffectiveEffort(%q) = (%v,%v), want (nil,false)", convID, got, ok)
		}
	}
	if got := plan.queryIDs(); len(got) != 0 {
		t.Fatalf("unaddressable conversations queried runners %q, want none", got)
	}

	plainPool := newRouterTestPool(t)
	plainReg := &conversations.Registry{}
	createEffectiveEffortConversation(t, plainReg, "conv-unsupported", plainPool.BootstrapID())
	if got, ok := resolveBoundEffectiveEffort(context.Background(), plainReg, plainPool, "conv-unsupported"); ok || got != nil {
		t.Fatalf("unsupported runner result = (%v,%v), want (nil,false)", got, ok)
	}
}

func TestResolveBoundEffectiveEffort_UsesDeadlineAndHonorsContextEnd(t *testing.T) {
	t.Parallel()

	pool, plan := newEffectiveEffortTestPool(t)
	id := mintEffectiveEffortSession(t, pool, "deadline")
	reg := &conversations.Registry{}
	createEffectiveEffortConversation(t, reg, "conv-deadline", id)

	plan.arm(id, streamsup.AppliedSettings{Effort: stringPointer("high")}, true)
	started := time.Now()
	if _, ok := resolveBoundEffectiveEffort(context.Background(), reg, pool, "conv-deadline"); !ok {
		t.Fatal("background-context query was unavailable")
	}
	deadline, hasDeadline := plan.deadlineFor(id)
	if !hasDeadline {
		t.Fatal("child query context had no deadline")
	}
	if remaining := deadline.Sub(started); remaining <= 0 || remaining > 31*time.Second {
		t.Errorf("child query deadline remaining = %v, want within (0,31s]", remaining)
	}

	plan.block(id)
	timedOut, cancelTimeout := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancelTimeout()
	if got, ok := resolveBoundEffectiveEffort(timedOut, reg, pool, "conv-deadline"); ok || got != nil {
		t.Fatalf("timed-out result = (%v,%v), want (nil,false)", got, ok)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if got, ok := resolveBoundEffectiveEffort(cancelled, reg, pool, "conv-deadline"); ok || got != nil {
		t.Fatalf("cancelled result = (%v,%v), want (nil,false)", got, ok)
	}
}

func TestEffectiveEffortFor_ReResolvesBindingOnEveryCall(t *testing.T) {
	t.Parallel()

	pool, plan := newEffectiveEffortTestPool(t)
	predecessor := mintEffectiveEffortSession(t, pool, "predecessor")
	successor := mintEffectiveEffortSession(t, pool, "successor")
	plan.arm(predecessor, streamsup.AppliedSettings{Effort: stringPointer("low")}, true)
	plan.arm(successor, streamsup.AppliedSettings{Effort: stringPointer("max")}, true)
	plan.arm(pool.BootstrapID(), streamsup.AppliedSettings{Effort: stringPointer("bootstrap-must-not-leak")}, true)

	reg := &conversations.Registry{}
	createEffectiveEffortConversation(t, reg, "conv-rebound", predecessor)
	provider := effectiveEffortFor(reg, pool)

	first, firstOK := provider(context.Background(), "conv-rebound")
	if !firstOK || first == nil || *first != "low" {
		t.Fatalf("first result = (%v,%v), want (low,true)", first, firstOK)
	}
	if !reg.RebindSession(string(predecessor), string(successor)) {
		t.Fatal("Registry.RebindSession returned false")
	}
	second, secondOK := provider(context.Background(), "conv-rebound")
	if !secondOK || second == nil || *second != "max" {
		t.Fatalf("second result = (%v,%v), want (max,true)", second, secondOK)
	}

	if got := plan.queryIDs(); !slices.Equal(got, []sessions.SessionID{predecessor, successor}) {
		t.Errorf("queried runners = %q, want predecessor then successor", got)
	}
}

func TestEffectiveEffortFor_PreservesNilDependencies(t *testing.T) {
	t.Parallel()

	pool, _ := newEffectiveEffortTestPool(t)
	reg := &conversations.Registry{}
	for name, provider := range map[string]func(context.Context, string) (*string, bool){
		"both nil":     effectiveEffortFor(nil, nil),
		"registry nil": effectiveEffortFor(nil, pool),
		"pool nil":     effectiveEffortFor(reg, nil),
	} {
		if provider != nil {
			t.Errorf("%s: provider = %p, want nil", name, provider)
		}
	}
	if provider := effectiveEffortFor(reg, pool); provider == nil {
		t.Fatal("wired dependencies produced nil provider")
	}
}
