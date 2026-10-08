package sessions

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
)

func TestLifecycleRotationFacts(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		cause  LifecycleCause
		reason TransitionReason
		rotate func(*Pool, SessionID) (SessionID, error)
	}{
		{"reset", CauseOperatorReset, ReasonClear, func(p *Pool, id SessionID) (SessionID, error) { return p.RotateForNewSession(id) }},
		{"clear", CauseClaudeClear, ReasonClear, func(p *Pool, id SessionID) (SessionID, error) {
			next := SessionID("22222222-2222-4222-8222-222222222222")
			return next, p.AdoptAnnouncedID(id, next)
		}},
		{"recovery", CauseRecovery, "", func(p *Pool, _ SessionID) (SessionID, error) { return p.RotateBootstrapForSelfHeal() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, failSave := range []bool{false, true} {
				t.Run(map[bool]string{false: "persisted", true: "save failure"}[failSave], func(t *testing.T) {
					pool := helperPool(t, false)
					old := pool.BootstrapID()
					reg, _ := seedBoundConvRegistry(t, pool, "owner", old)
					var logs bytes.Buffer
					if failSave {
						blocker := filepath.Join(t.TempDir(), "file")
						if err := os.WriteFile(blocker, nil, 0600); err != nil {
							t.Fatal(err)
						}
						pool.registryPath = filepath.Join(blocker, "sessions.json")
						pool.convRegistryPath = filepath.Join(blocker, "conversations.json")
						pool.log = slog.New(slog.NewTextHandler(&logs, nil))
					}
					var facts []SessionTransition
					pool.SetTransitionObserver(func(f SessionTransition) {
						// Acquiring each lock here proves callbacks execute off all three.
						pool.mu.Lock()
						pool.mu.Unlock()
						pool.Default().lcMu.Lock()
						pool.Default().lcMu.Unlock()
						pool.capMu.Lock()
						pool.capMu.Unlock()
						c, _ := reg.Get("owner")
						if c.CurrentSessionID != string(f.NewID) {
							t.Errorf("binding before callback = %q, fact = %+v", c.CurrentSessionID, f)
						}
						facts = append(facts, f)
					})
					next, err := tc.rotate(pool, old)
					if err != nil {
						t.Fatal(err)
					}
					if failSave && (!strings.Contains(logs.String(), "rebind_conversation.persist_failed") || (strings.Count(logs.String(), "persist_failed") != 2)) {
						t.Fatalf("save failures not exercised: %s", logs.String())
					}
					if len(facts) != 1 {
						t.Fatalf("facts = %+v", facts)
					}
					f := facts[0]
					if f.Cause != tc.cause || f.Reason != tc.reason || f.ConversationID != "owner" || f.PreviousID != old || f.NewID != next || f.AgentSwitch || f.ResetHandoffOutcome != nil || f.PreviousAgent != "" || f.NextAgent != "" {
						t.Fatalf("fact = %+v", f)
					}
					if f.OccurredAt.IsZero() || f.OccurredAt.Location() != time.UTC {
						t.Fatalf("occurrence = %v", f.OccurredAt)
					}
				})
			}
		})
	}
}

func TestLifecycleResetHandoffSnapshot(t *testing.T) {
	t.Parallel()
	pool := helperPool(t, false)
	var fact SessionTransition
	pool.SetTransitionObserver(func(f SessionTransition) { fact = f })
	outcome := "stored"
	if _, err := pool.RotateForNewSessionWithHandoff(pool.BootstrapID(), &outcome); err != nil {
		t.Fatal(err)
	}
	outcome = "changed"
	if fact.ConversationID != "" || fact.ResetHandoffOutcome == nil || *fact.ResetHandoffOutcome != "stored" {
		t.Fatalf("fact = %+v", fact)
	}
}

func TestLifecycleDelayedResetProvenance(t *testing.T) {
	t.Parallel()
	for _, owned := range []bool{true, false} {
		t.Run(map[bool]string{true: "owned", false: "unowned"}[owned], func(t *testing.T) {
			pool := helperPool(t, false)
			sess := pool.Default()
			old := sess.ID()
			reg, _ := seedBoundConvRegistry(t, pool, "original", old)
			if !owned {
				reg.Delete("original")
			}
			// Reset recomposition waits here AFTER the pool rekey but BEFORE fan-out.
			sess.systemPromptPath = filepath.Join(t.TempDir(), "prompt.md")
			sess.spawnArgsMu.Lock()
			locked := true
			defer func() {
				if locked {
					sess.spawnArgsMu.Unlock()
				}
			}()
			rec := &transitionRecorder{}
			pool.SetTransitionObserver(rec.observe)
			finished := make(chan error, 1)
			go func() { _, err := pool.RotateForNewSession(old); finished <- err }()
			if !pollUntil(t, 2*time.Second, func() bool { return pool.BootstrapID() != old }) {
				t.Fatal("reset did not rekey")
			}
			middle := pool.BootstrapID()
			if !owned {
				// A late, foreign binding must not be acquired by the delayed A→B fact.
				reg.Create(conversations.Conversation{ID: "foreign", CurrentSessionID: string(old)})
			}
			next := SessionID("33333333-3333-4333-8333-333333333333")
			if err := pool.AdoptAnnouncedID(middle, next); err != nil {
				t.Fatal(err)
			}
			sess.spawnArgsMu.Unlock()
			locked = false
			select {
			case err := <-finished:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("reset stuck")
			}
			facts := rec.snapshot()
			if len(facts) != 2 {
				t.Fatalf("facts = %+v", facts)
			}
			owner := ""
			if owned {
				owner = "original"
				c, _ := reg.Get("original")
				if c.CurrentSessionID != string(next) {
					t.Fatalf("binding = %+v", c)
				}
			} else {
				c, _ := reg.Get("foreign")
				if c.CurrentSessionID != string(old) || len(c.SessionHistory) != 0 {
					t.Fatalf("foreign binding = %+v", c)
				}
			}
			if facts[0].PreviousID != middle || facts[0].NewID != next || facts[0].Cause != CauseClaudeClear || facts[1].PreviousID != old || facts[1].NewID != middle || facts[1].Cause != CauseOperatorReset {
				t.Fatalf("pairs/causes = %+v", facts)
			}
			if !facts[1].OccurredAt.Before(facts[0].OccurredAt) {
				t.Fatalf("mutation timestamps = %+v", facts)
			}
			for _, f := range facts {
				if f.ConversationID != owner {
					t.Fatalf("owner = %q, want %q", f.ConversationID, owner)
				}
			}
		})
	}
}

func TestLifecycleEvictionCapturedOwner(t *testing.T) {
	t.Parallel()
	pool := helperPool(t, false)
	sess := pool.Default()
	old := sess.ID()
	reg, _ := seedBoundConvRegistry(t, pool, "owner", old)
	rec := &transitionRecorder{}
	pool.SetTransitionObserver(func(f SessionTransition) {
		pool.mu.Lock()
		pool.mu.Unlock()
		sess.lcMu.Lock()
		sess.lcMu.Unlock()
		pool.capMu.Lock()
		pool.capMu.Unlock()
		rec.observe(f)
	})
	sess.beginEvict(ReasonEviction, CauseIdleSleep)
	reg.Delete("owner")
	f := rec.snapshot()[0]
	if f.ConversationID != "owner" || f.Cause != CauseIdleSleep || f.NewID != "" || f.PreviousID != old {
		t.Fatalf("fact = %+v", f)
	}
	// Removal teardown and the silent spontaneous exit path produce no fact.
	pool.mu.Lock()
	delete(pool.sessions, old)
	pool.mu.Unlock()
	sess.beginEvict(ReasonEviction, CauseCapacityEviction)
	sess.beginEvict("")
	if rec.len() != 1 {
		t.Fatalf("facts = %+v", rec.snapshot())
	}
}

func TestLifecycleRefusedAndEqualRotations(t *testing.T) {
	t.Parallel()
	pool := helperPool(t, false)
	old := pool.BootstrapID()
	rec := &transitionRecorder{}
	pool.SetTransitionObserver(rec.observe)
	if err := pool.AdoptAnnouncedID(old, old); err != nil {
		t.Fatal(err)
	}
	if err := pool.AdoptAnnouncedID("absent", old); !errors.Is(err, ErrSessionNotFound) {
		t.Fatal(err)
	}
	pool.PublishSwitchTransition(old, old)
	pool.PublishSwitchTransition("", old)
	pool.PublishSwitchTransition(old, "")
	pool.mu.Lock()
	delete(pool.sessions, old)
	pool.mu.Unlock()
	if _, err := pool.RotateBootstrapForSelfHeal(); !errors.Is(err, ErrSessionNotFound) {
		t.Fatal(err)
	}
	if rec.len() != 0 {
		t.Fatalf("facts = %+v", rec.snapshot())
	}
}

func TestLifecycleSwitchUnknownMetadata(t *testing.T) {
	t.Parallel()
	pool := helperPool(t, false)
	old := pool.BootstrapID()
	seedBoundConvRegistry(t, pool, "owner", old)
	var observed, published SessionTransition
	pool.SetTransitionObserver(func(f SessionTransition) { observed = f })
	pool.SetSwitchTransitionPublisher(func(f SessionTransition) { published = f })
	pool.PublishSwitchTransition(old, "next")
	if observed != published || observed.Cause != CauseAgentSwitch || !observed.AgentSwitch || observed.Reason != ReasonClear || observed.ConversationID != "" || observed.PreviousAgent != "" || observed.NextAgent != "" || observed.ResetHandoffOutcome != nil {
		t.Fatalf("unknown metadata was inferred: observed=%+v published=%+v", observed, published)
	}
}
