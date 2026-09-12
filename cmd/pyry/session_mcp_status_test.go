package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

type mcpStatusQueryPlan struct {
	mu      sync.Mutex
	byID    map[sessions.SessionID]turnevent.MCPStatus
	blocked map[sessions.SessionID]bool
	calls   map[sessions.SessionID]int
}

func newMCPStatusQueryPlan() *mcpStatusQueryPlan {
	return &mcpStatusQueryPlan{
		byID:    make(map[sessions.SessionID]turnevent.MCPStatus),
		blocked: make(map[sessions.SessionID]bool),
		calls:   make(map[sessions.SessionID]int),
	}
}

func (p *mcpStatusQueryPlan) arm(id sessions.SessionID, status turnevent.MCPStatus) {
	p.mu.Lock()
	p.byID[id] = status
	p.mu.Unlock()
}

func (p *mcpStatusQueryPlan) block(id sessions.SessionID) {
	p.mu.Lock()
	p.blocked[id] = true
	p.mu.Unlock()
}

func (p *mcpStatusQueryPlan) query(ctx context.Context, id sessions.SessionID) (turnevent.MCPStatus, bool) {
	p.mu.Lock()
	p.calls[id]++
	blocked := p.blocked[id]
	status, ok := p.byID[id]
	p.mu.Unlock()
	if blocked {
		<-ctx.Done()
		return turnevent.MCPStatus{}, false
	}
	return status, ok
}

func (p *mcpStatusQueryPlan) callCount(id sessions.SessionID) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[id]
}

type mcpStatusQueryRunner struct {
	stubRunner
	id   sessions.SessionID
	plan *mcpStatusQueryPlan
}

func (r mcpStatusQueryRunner) QueryMCPStatus(ctx context.Context) (turnevent.MCPStatus, bool) {
	return r.plan.query(ctx, r.id)
}

func newMCPStatusQueryTestPool(t *testing.T) (*sessions.Pool, *mcpStatusQueryPlan) {
	t.Helper()
	plan := newMCPStatusQueryPlan()
	pool, err := sessions.New(sessions.Config{
		Bootstrap:    sessions.SessionConfig{ClaudeBin: os.Args[0]},
		RegistryPath: filepath.Join(t.TempDir(), "sessions.json"),
		RunnerFactory: func(cfg sessions.RunnerConfig) (sessions.Runner, error) {
			return mcpStatusQueryRunner{id: sessions.SessionID(cfg.SessionID), plan: plan}, nil
		},
	})
	if err != nil {
		t.Fatalf("sessions.New: %v", err)
	}
	return pool, plan
}

func sentinelMCPStatus(tag string) turnevent.MCPStatus {
	return turnevent.MCPStatus{
		Servers: []turnevent.MCPServerStatus{
			{
				Name:    "name-one-" + tag,
				Status:  "status-one-" + tag,
				Error:   "error-one-" + tag,
				Scope:   "scope-one-" + tag,
				Version: "version-one-" + tag,
			},
			{
				Name:    "name-two-" + tag,
				Status:  "status-two-" + tag,
				Error:   "error-two-" + tag,
				Scope:   "scope-two-" + tag,
				Version: "version-two-" + tag,
			},
		},
		DroppedServers: 7,
	}
}

func TestResolveBoundMCPStatus_QueriesBoundRunnerAndReusesMapping(t *testing.T) {
	t.Parallel()
	pool, plan := newMCPStatusQueryTestPool(t)
	boundID, err := pool.Mint("mcp-status-bound", "")
	if err != nil && !errors.Is(err, sessions.ErrPoolNotRunning) {
		t.Fatalf("pool.Mint: %v", err)
	}
	if boundID == "" {
		t.Fatal("pool.Mint returned an empty session id")
	}
	plan.arm(pool.BootstrapID(), sentinelMCPStatus("bootstrap-must-not-leak"))
	wantEvent := sentinelMCPStatus("bound")
	plan.arm(boundID, wantEvent)
	reg := &conversations.Registry{}
	conversation := conversations.Conversation{
		ID:               "conv-mcp-bound",
		CurrentSessionID: string(boundID),
		LastUsedAt:       time.Now().UTC(),
	}
	reg.Create(conversation)

	_, canonicalID, ok := resolveBoundRunner(reg, pool, string(conversation.ID))
	if !ok {
		t.Fatal("resolveBoundRunner refused a resolvable non-bootstrap binding")
	}
	if canonicalID != conversation.ID {
		t.Fatalf("resolved conversation id = %q, want registry id %q", canonicalID, conversation.ID)
	}

	got, ok := resolveBoundMCPStatus(context.Background(), reg, pool, string(conversation.ID))
	if !ok {
		t.Fatal("resolveBoundMCPStatus refused a queryable bound runner")
	}
	want := protocol.MCPStatusPayload{
		ConversationID: "conv-mcp-bound",
		Servers: []protocol.MCPServerStatus{
			{
				Name: "name-one-bound", Status: "status-one-bound", Error: "error-one-bound",
				Scope: "scope-one-bound", Version: "version-one-bound",
			},
			{
				Name: "name-two-bound", Status: "status-two-bound", Error: "error-two-bound",
				Scope: "scope-two-bound", Version: "version-two-bound",
			},
		},
		DroppedServers: 7,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("resolveBoundMCPStatus = %+v, want %+v", got, want)
	}
	if calls := plan.callCount(boundID); calls != 1 {
		t.Errorf("bound runner query calls = %d, want 1", calls)
	}
	if calls := plan.callCount(pool.BootstrapID()); calls != 0 {
		t.Errorf("bootstrap runner query calls = %d, want 0", calls)
	}
}

func TestResolveBoundMCPStatus_RefusesWithoutQueryableBinding(t *testing.T) {
	t.Parallel()
	pool, plan := newMCPStatusQueryTestPool(t)
	plan.arm(pool.BootstrapID(), sentinelMCPStatus("must-not-leak"))
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{
		ID:               "conv-resolvable",
		CurrentSessionID: string(pool.BootstrapID()),
		LastUsedAt:       time.Now().UTC(),
	})
	reg.Create(conversations.Conversation{ID: "conv-unbound", LastUsedAt: time.Now().UTC()})
	reg.Create(conversations.Conversation{
		ID:               "conv-dangling",
		CurrentSessionID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		LastUsedAt:       time.Now().UTC(),
	})

	tests := []string{"", "conv-unknown", "conv-unbound", "conv-dangling"}
	for _, convID := range tests {
		convID := convID
		t.Run(convID, func(t *testing.T) {
			got, ok := resolveBoundMCPStatus(context.Background(), reg, pool, convID)
			if ok || !reflect.DeepEqual(got, protocol.MCPStatusPayload{}) {
				t.Fatalf("resolveBoundMCPStatus(%q) = (%+v,%v), want zero,false", convID, got, ok)
			}
		})
	}
	if calls := plan.callCount(pool.BootstrapID()); calls != 0 {
		t.Fatalf("refused bindings queried bootstrap %d times", calls)
	}
}

func TestResolveBoundMCPStatus_RefusesRunnerWithoutQueryMethod(t *testing.T) {
	t.Parallel()
	pool := newRouterTestPool(t)
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{
		ID:               "conv-plain-runner",
		CurrentSessionID: string(pool.BootstrapID()),
		LastUsedAt:       time.Now().UTC(),
	})
	if got, ok := resolveBoundMCPStatus(context.Background(), reg, pool, "conv-plain-runner"); ok ||
		!reflect.DeepEqual(got, protocol.MCPStatusPayload{}) {
		t.Fatalf("plain runner result = (%+v,%v), want zero,false", got, ok)
	}
}

func TestResolveBoundMCPStatus_ForwardsCancellation(t *testing.T) {
	t.Parallel()
	pool, plan := newMCPStatusQueryTestPool(t)
	plan.block(pool.BootstrapID())
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{
		ID:               "conv-mcp-cancel",
		CurrentSessionID: string(pool.BootstrapID()),
		LastUsedAt:       time.Now().UTC(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, ok := resolveBoundMCPStatus(ctx, reg, pool, "conv-mcp-cancel"); ok ||
		!reflect.DeepEqual(got, protocol.MCPStatusPayload{}) {
		t.Fatalf("canceled result = (%+v,%v), want zero,false", got, ok)
	}
	if calls := plan.callCount(pool.BootstrapID()); calls != 1 {
		t.Errorf("canceled query calls = %d, want 1", calls)
	}
}

func TestMCPStatusFor_PreservesNilAndDelegates(t *testing.T) {
	if got := mcpStatusFor(nil, nil); got != nil {
		t.Fatalf("mcpStatusFor(nil,nil) = %p, want nil", got)
	}
	pool, plan := newMCPStatusQueryTestPool(t)
	plan.arm(pool.BootstrapID(), sentinelMCPStatus("adapter"))
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{
		ID:               "conv-mcp-adapter",
		CurrentSessionID: string(pool.BootstrapID()),
		LastUsedAt:       time.Now().UTC(),
	})
	seam := mcpStatusFor(reg, pool)
	got, ok := seam(context.Background(), "conv-mcp-adapter")
	if !ok || got.ConversationID != "conv-mcp-adapter" || got.DroppedServers != 7 {
		t.Fatalf("adapter result = (%+v,%v), want mapped bound status", got, ok)
	}
}
