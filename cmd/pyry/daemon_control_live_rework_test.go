package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func TestControlLiveReworkContextFallbackOvertakesStream(t *testing.T) {
	b := testControlBindings(t, "conv", "a")
	src := b.capture("conv", "a", false)
	rec := &contextUsageRecorder{reg: b.reg, path: filepath.Join(t.TempDir(), "registry.json"), logger: discardLogger()}
	rec.record("conv", turnevent.ContextUsage{Model: "stored-old", TotalTokens: 1, MaxTokens: 10}, src)
	q := newFakeContextUsageQuerier(false)
	q.block = make(chan struct{})
	r := newContextUsageResolver(context.Background(), func(string) (contextUsageQuerier, conversations.ConversationID, bool) { return q, "conv", true }, nil, nil)
	r.rec, r.live = rec, b
	done := make(chan struct{})
	go func() { defer close(done); r.Get(context.Background(), "conv") }()
	<-q.details
	b.sink.live.acceptEvent(src, turnevent.ContextUsage{Model: "stream-new", TotalTokens: 2, MaxTokens: 10})
	close(q.block)
	<-done
	retained := testLiveReadings(t, b.sink.live, "conv")[liveReadingKey{protocol.TypeContextUsage, ""}]
	var p protocol.ContextUsagePayload
	json.Unmarshal(retained.Envelope.Payload, &p)
	if p.Model != "stream-new" {
		t.Fatalf("failed predecessor query restored %q over newer stream reading, revision=%d", p.Model, retained.Revision)
	}
}

func TestControlLiveReworkModelCacheRetaggedAfterSameIDTransition(t *testing.T) {
	pool, plan := newModelListTestPool(t)
	sid := string(pool.BootstrapID())
	list := turnevent.ModelList{Models: []turnevent.ModelOption{{Value: "sonnet"}}}
	plan.arm(pool.BootstrapID(), list)
	b := testControlBindings(t, "conv", sid)
	src := b.capture("conv", sid, false)
	b.sink.live.acceptEvent(src, list)
	provider := modelListFor(b.reg, pool, nil, b)
	if _, ok := provider("conv", false); !ok {
		t.Fatal("initial provider failed")
	}
	initial := testLiveReadings(t, b.sink.live, "conv")[liveReadingKey{protocol.TypeModelList, ""}]
	if string(initial.Envelope.SessionID) != `"`+sid+`"` {
		t.Fatal("initial cached source not known")
	}
	b.sink.live.transition(sessions.SessionTransition{ConversationID: "conv", PreviousID: sessions.SessionID(sid), NewID: sessions.SessionID(sid), NextAgent: "claude"})
	if _, ok := provider("conv", false); !ok {
		t.Fatal("legacy provider refused cache")
	}
	retained := testLiveReadings(t, b.sink.live, "conv")[liveReadingKey{protocol.TypeModelList, ""}]
	if !retained.Envelope.SessionStateCleared {
		t.Fatalf("predecessor model cache installed as successor: generation=%d old=%d source=%q", retained.SessionGeneration, src.SessionGeneration, retained.Envelope.SessionID)
	}
}

func TestControlLiveReworkMergedModelSourceLost(t *testing.T) {
	pool, plan := newModelListTestPool(t)
	sid := string(pool.BootstrapID())
	list := turnevent.ModelList{Models: []turnevent.ModelOption{{Value: "sonnet"}}}
	plan.arm(pool.BootstrapID(), list)
	b := testControlBindings(t, "conv", sid)
	src := b.capture("conv", sid, false)
	b.sink.live.acceptEvent(src, list)
	if _, ok := modelListFor(b.reg, pool, nil, b)("conv", true); !ok {
		t.Fatal("merged provider refused known Claude-only menu")
	}
	retained := testLiveReadings(t, b.sink.live, "conv")[liveReadingKey{protocol.TypeModelList, ""}]
	if string(retained.Envelope.SessionID) != `"`+sid+`"` {
		t.Fatalf("known source lost after capability projection: source=%q", retained.Envelope.SessionID)
	}
}

func TestControlLiveReworkResetCaptureAfterDispatch(t *testing.T) {
	b := testControlBindings(t, starterConvA, "a")
	original := b.capture(starterConvA, "a", false)
	emitter := newResettingEmitterV2(context.Background(), discardLogger())
	emitter.live = b
	reset := &conversationReset{base: context.Background(), log: discardLogger()}
	observed := make(chan daemonLiveReading, 1)
	outcome := make(chan struct{})
	runner := &asyncRunner{childPID: 1}
	starter := activeSessionStarter{
		reset: reset, resetting: emitter, log: discardLogger(),
		resolveBound: func(string) (sessions.Runner, sessions.SessionID, string, bool) {
			return runner, "a", "/fake-workspace", true
		},
		spawnDirFor: func(string) (string, error) {
			b.sink.live.transition(sessions.SessionTransition{ConversationID: starterConvA, PreviousID: "a", NewID: "a", NextAgent: "claude"})
			return "", nil
		},
		rotate: func(sessions.SessionID) (sessions.SessionID, error) {
			observed <- testLiveReadings(t, b.sink.live, starterConvA)[liveReadingKey{protocol.TypeResetting, ""}]
			return "", fmt.Errorf("synthetic rotation refusal")
		},
	}
	starter.StartNewSessionLate(starterConvA, func(error) { close(outcome) })
	reading := <-observed
	<-outcome
	deadline := time.After(3 * time.Second)
	for {
		reset.mu.Lock()
		_, active := reset.inFlight[starterConvA]
		reset.mu.Unlock()
		if !active {
			break
		}
		select {
		case <-deadline:
			t.Fatal("reset worker did not finish")
		case <-time.After(time.Millisecond):
		}
	}
	if !reading.Envelope.SessionStateCleared {
		t.Fatalf("pre-transition reset captured after dispatch: retained generation=%d original=%d source=%q", reading.SessionGeneration, original.SessionGeneration, reading.Envelope.SessionID)
	}
}
func TestControlLiveReworkRememberedPayloadSourceMismatch(t *testing.T) {
	b := testControlBindings(t, "conv", "a")
	old := b.capture("conv", "a", false)
	rec := &contextUsageRecorder{reg: b.reg, path: filepath.Join(t.TempDir(), "registry.json"), logger: discardLogger()}
	rec.record("conv", turnevent.ContextUsage{Model: "old-bytes", TotalTokens: 1, MaxTokens: 10}, old)
	resolver := newContextUsageResolver(context.Background(), func(string) (contextUsageQuerier, conversations.ConversationID, bool) { return nil, "conv", true }, nil, nil)
	resolver.rec, resolver.live = rec, b
	rec.mu.Lock()
	held := true
	defer func() {
		if held {
			rec.mu.Unlock()
		}
	}()
	done := make(chan daemonLiveReading, 1)
	go func() {
		reading, _ := resolver.GetLive(context.Background(), "conv", protocol.Envelope{})
		done <- reading
	}()
	deadline := time.After(3 * time.Second)
	for {
		stack := make([]byte, 1<<20)
		n := runtime.Stack(stack, true)
		if bytes.Contains(stack[:n], []byte("(*contextUsageRecorder).source(")) || bytes.Contains(stack[:n], []byte("(*contextUsageRecorder).snapshot(")) {
			break
		}
		select {
		case <-deadline:
			t.Fatal("remembered request did not reach the source lookup")
		case <-time.After(time.Millisecond):
		}
	}
	b.sink.live.transition(sessions.SessionTransition{ConversationID: "conv", PreviousID: "a", NewID: "a", NextAgent: "claude"})
	next := old
	next.SessionGeneration++
	saved := conversations.ContextUsageReading{Model: "new-bytes", TotalTokens: 2, MaxTokens: 10, AsOf: time.Now().UTC()}
	// Replace the registry bytes and their evidence while the snapshot is parked.
	b.reg.SetLastContextUsage("conv", saved)
	rec.evidence["conv"] = contextUsageEvidence{saved, next}
	held = false
	rec.mu.Unlock()
	reply := <-done
	var payload protocol.ContextUsagePayload
	json.Unmarshal(reply.Envelope.Payload, &payload)
	if payload.Model == "old-bytes" && reply.SessionGeneration == next.SessionGeneration {
		t.Fatalf("remembered old bytes acquired successor source: payload=%q generation=%d original=%d", payload.Model, reply.SessionGeneration, old.SessionGeneration)
	}
}

// Exercise the production atomic cache seam without owner-envelope evidence.
type testLiveInventoryRunner struct {
	stubRunner
	models   *sessionModelHold
	commands *sessionSlashCommandHold
}

func (r testLiveInventoryRunner) ModelList() (turnevent.ModelList, bool) { return r.models.ModelList() }
func (r testLiveInventoryRunner) ModelListLive() (turnevent.ModelList, daemonLiveSource, bool) {
	return r.models.ModelListLive()
}
func (r testLiveInventoryRunner) SlashCommandList() (turnevent.SlashCommandList, bool) {
	return r.commands.SlashCommandList()
}
func (r testLiveInventoryRunner) SlashCommandListLive() (turnevent.SlashCommandList, daemonLiveSource, bool) {
	return r.commands.SlashCommandListLive()
}

func TestControlLiveReworkAtomicInventoryCaches(t *testing.T) {
	for _, family := range []string{protocol.TypeModelList, protocol.TypeSlashCommandList} {
		t.Run(family, func(t *testing.T) {
			models := newSessionModelHold(nil)
			commands := newSessionSlashCommandHold(nil)
			pool, err := sessions.New(sessions.Config{
				Bootstrap:    sessions.SessionConfig{ClaudeBin: os.Args[0]},
				RegistryPath: filepath.Join(t.TempDir(), "sessions.json"),
				RunnerFactory: func(sessions.RunnerConfig) (sessions.Runner, error) {
					return testLiveInventoryRunner{models: models, commands: commands}, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			sid := string(pool.BootstrapID())
			b := testControlBindings(t, "conv", sid)
			models.capture = func() daemonLiveSource { return b.capture("conv", sid, false) }
			commands.capture = models.capture
			read := func() {
				if family == protocol.TypeModelList {
					_, ok := modelListFor(b.reg, pool, nil, b)("conv", true)
					if !ok {
						t.Fatal("model refusal")
					}
				} else {
					_, ok := resolveBoundSlashCommandList(b.reg, pool, "conv", b)
					if !ok {
						t.Fatal("slash refusal")
					}
				}
			}
			publish := func() {
				models.Sink(turnevent.ModelList{Models: []turnevent.ModelOption{{Value: "sonnet"}}})
				commands.Sink(sentinelSlashCommandList("cache"))
			}
			publish()
			read()
			before := testLiveReadings(t, b.sink.live, "conv")[liveReadingKey{family, ""}]
			if string(before.Envelope.SessionID) != `"`+sid+`"` {
				t.Fatal("cache source missing")
			}
			b.sink.live.transition(sessions.SessionTransition{ConversationID: "conv", PreviousID: sessions.SessionID(sid), NewID: sessions.SessionID(sid), NextAgent: "claude"})
			read()
			after := testLiveReadings(t, b.sink.live, "conv")[liveReadingKey{family, ""}]
			if !after.Envelope.SessionStateCleared {
				t.Fatal("cached predecessor replaced clear")
			}
			publish() // Identical bytes from a new producer generation establish fresh evidence.
			read()
			fresh := testLiveReadings(t, b.sink.live, "conv")[liveReadingKey{family, ""}]
			if fresh.Envelope.SessionStateCleared || fresh.SessionGeneration <= before.SessionGeneration || string(fresh.Envelope.SessionID) != `"`+sid+`"` {
				t.Fatal("fresh cache source lost")
			}
		})
	}
}
