package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func TestRuntimeHistoryDividerMapping(t *testing.T) {
	for _, cause := range []sessions.LifecycleCause{sessions.CauseOperatorReset, sessions.CauseClaudeClear, sessions.CauseAgentSwitch, sessions.CauseRecovery, sessions.CauseWorkspaceChange, sessions.CauseIdleSleep, sessions.CauseCapacityEviction} {
		t.Run(string(cause), func(t *testing.T) {
			store := history.New(t.TempDir())
			e := newSessionTransitionEmitterV2(historyOnlyBroadcaster{}, nil, discardLogger())
			e.hist = store
			fact := sessions.SessionTransition{ConversationID: testConvID, PreviousID: "old", NewID: "new", PreviousAgent: "claude", NextAgent: "codex", Cause: cause, OccurredAt: time.Now().UTC()}
			e.broadcast(context.Background(), fact)
			entries := historyEntries(t, store, testConvID)
			if len(entries) != 1 || entries[0].Type != historySessionDivider {
				t.Fatalf("divider: %+v", entries)
			}
			if entries[0].Shown == nil || *entries[0].Shown != (cause != sessions.CauseIdleSleep) {
				t.Fatalf("visibility: %+v", entries[0])
			}
			var p runtimeHistoryFact
			if err := json.Unmarshal(entries[0].Payload, &p); err != nil || p.Cause != string(cause) || p.PreviousSessionID != "old" || p.NewSessionID != "new" || !p.OccurredAt.Equal(fact.OccurredAt) {
				t.Fatalf("fact: %+v, %v", p, err)
			}
			if len(newHistoryPager(store, discardLogger())(testConvID, "", 128).Entries) != 0 {
				t.Fatal("divider entered legacy history")
			}
		})
	}
}

func TestRuntimeHistoryResetOutcomeAndNoops(t *testing.T) {
	for _, outcome := range []string{"", "written", "skipped", "pending", "assumed"} {
		t.Run(outcome, func(t *testing.T) {
			fact := sessions.SessionTransition{ConversationID: testConvID, PreviousID: "a", NewID: "b", Cause: sessions.CauseOperatorReset}
			if outcome != "" {
				fact.ResetHandoffOutcome = &outcome
			}
			p, ok := runtimeDivider(fact)
			if !ok {
				t.Fatal("reset rejected")
			}
			if (p.ResetHandoffOutcome != nil) != (outcome == "written" || outcome == "skipped") {
				t.Fatalf("outcome: %+v", p)
			}
			for _, pair := range [][2]string{{"", "b"}, {"a", "a"}, {"", ""}} {
				fact.PreviousID, fact.NewID = sessions.SessionID(pair[0]), sessions.SessionID(pair[1])
				if _, ok := runtimeDivider(fact); ok {
					t.Fatalf("no-op accepted: %+v", fact)
				}
			}
		})
	}
}

func TestRuntimeHistoryWriterOrdering(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := history.New(t.TempDir())
	resolve := stubBusyResolve(map[string]string{"a": testConvID, "b": testConvID, "c": testConvID, "other": testConvIDB})
	sink := newStreamTurnSink(64, discardLogger())
	busy := newTurnBusyTracker(resolve, discardLogger(), withExitEpoch(sink.exitEpoch))
	delivery := testDelivery(t, t.TempDir(), store, nil)
	delivery.bind(busy)
	e := newInteractiveTurnEmitterV2(nil, historyOnlyBroadcaster{}, discardLogger())
	e.hist = store
	installRuntimeHistory(sink, e, busy)
	transition := newSessionTransitionEmitterV2(historyOnlyBroadcaster{}, resolve, discardLogger())
	transition.hist, transition.runtimeSink, transition.runtimeContext = store, sink, ctx
	a := sink.sinkForTag(func() string { return "a" }, "claude")
	b := sink.sinkForTag(func() string { return "b" }, "codex")
	other := sink.sinkForTag(func() string { return "other" }, "claude")
	a(turnevent.ToolStart{ToolCallID: "old-tool", Title: "Read"})
	a(turnevent.TextChunk{Text: "A-tail"})
	other(turnevent.TextChunk{Text: "unrelated"})
	at := time.Now().UTC()
	transition.Enqueue(sessions.SessionTransition{ConversationID: testConvID, PreviousID: "a", NewID: "b", PreviousAgent: "claude", NextAgent: "codex", Cause: sessions.CauseAgentSwitch, OccurredAt: at})
	b(turnevent.TextChunk{Text: "B-tail"})
	transition.Enqueue(sessions.SessionTransition{ConversationID: testConvID, PreviousID: "b", NewID: "c", PreviousAgent: "codex", NextAgent: "claude", Cause: sessions.CauseRecovery, OccurredAt: at.Add(time.Millisecond)})
	sink.dispatchPlacement(ctx, func() {
		newOperatorMessageHistory(store, nil, nil, discardLogger(), history.SessionProvenance{Kind: "claude", SessionID: "c"})(testConvID, msgqueue.QueuedMessage{Text: "operator-C"})
	})
	delivery.sessionFor = func(conversations.ConversationID) *history.SessionProvenance {
		return &history.SessionProvenance{Kind: "claude", SessionID: "c"}
	}
	testAccept(t, delivery, conversations.ConversationID(testConvID), "post-C")
	delivery.drain()
	if len(historyEntries(t, store, testConvID)) != 0 {
		t.Fatal("channel post overtook pending boundary")
	}
	fence := make(chan struct{})
	sink.dispatchPlacement(ctx, func() { close(fence) })
	cleanup := startStreamTurnDrainV2(ctx, sink, e, resolve, busy, discardLogger())
	defer func() { cancel(); cleanup() }()
	select {
	case <-fence:
	case <-time.After(5 * time.Second):
		t.Fatal("drain fence timed out")
	}
	delivery.drain()
	entries := historyEntries(t, store, testConvID)
	var visible []string
	for _, entry := range entries {
		switch entry.Type {
		case protocol.TypeAssistantDelta:
			var p protocol.AssistantDeltaPayload
			_ = json.Unmarshal(entry.Payload, &p)
			visible = append(visible, p.Text)
		case protocol.TypeMessage:
			visible = append(visible, "operator-C")
		case historyToolInterrupted:
			visible = append(visible, "tool-interrupted")
		case historyTurnInterrupted:
			visible = append(visible, "turn-interrupted")
		case historySessionDivider:
			var p runtimeHistoryFact
			_ = json.Unmarshal(entry.Payload, &p)
			visible = append(visible, p.PreviousSessionID+"->"+p.NewSessionID)
			if p.ConversationID != testConvID || (p.PreviousSessionID == "a" && p.PreviousAgent != "claude") || (p.PreviousSessionID == "b" && p.PreviousAgent != "codex") {
				t.Fatalf("captured owner: %+v", p)
			}
		}
	}
	want := "A-tail,tool-interrupted,turn-interrupted,a->b,B-tail,turn-interrupted,b->c,operator-C,post-C"
	if strings.Join(visible, ",") != want {
		t.Fatalf("order = %v, want %s", visible, want)
	}
	for _, entry := range historyEntries(t, store, testConvIDB) {
		if entry.Type == historyTurnInterrupted || entry.Type == historySessionDivider {
			t.Fatal("another conversation closed")
		}
	}
}

func TestRuntimeHistoryExitSourceAndReplacement(t *testing.T) {
	for _, rotate := range []bool{false, true} {
		t.Run(map[bool]string{false: "same_session", true: "rotated_tag"}[rotate], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			store := history.New(t.TempDir())
			sink := newStreamTurnSink(32, discardLogger())
			resolve := stubBusyResolve(map[string]string{"a": testConvID, "b": testConvID})
			busy := newTurnBusyTracker(resolve, discardLogger(), withExitEpoch(sink.exitEpoch))
			e := newInteractiveTurnEmitterV2(nil, historyOnlyBroadcaster{}, discardLogger())
			e.hist = store
			installRuntimeHistory(sink, e, busy)
			tag := newStreamSessionTag("a")
			produce, exit := sink.sinkForSessionTag(tag, "claude"), sink.exitForSessionTag(tag)
			produce(turnevent.ThoughtChunk{Text: "old"})
			if rotate {
				tag.Rotate("b")
			}
			exit()
			produce(turnevent.TextChunk{Text: "replacement"})
			sink.offer(streamTurnEnvelope{sessionID: tag.ID(), source: history.SessionProvenance{Kind: "claude", SessionID: tag.ID()}, exit: true, exitEpoch: sink.exitEpoch()}, true)
			fence := make(chan struct{})
			sink.dispatchPlacement(ctx, func() { close(fence) })
			cleanup := startStreamTurnDrainV2(ctx, sink, e, resolve, busy, discardLogger())
			defer func() { cancel(); cleanup() }()
			select {
			case <-fence:
			case <-time.After(5 * time.Second):
				t.Fatal("exit fence timed out")
			}
			entries := historyEntries(t, store, testConvID)
			var endings int
			for _, entry := range entries {
				if entry.Type == historySessionDivider {
					t.Fatal("same-child exit wrote divider")
				}
				if entry.Type == historyTurnInterrupted {
					endings++
					if entry.Session == nil || entry.Session.SessionID != "a" {
						t.Fatalf("wrong exit source: %+v", entry)
					}
				}
			}
			if endings != 1 || !busy.Busy(testConvID) {
				t.Fatalf("endings=%d replacement busy=%v", endings, busy.Busy(testConvID))
			}
		})
	}
}

func TestRuntimeHistoryEvictionTailCause(t *testing.T) {
	for _, cause := range []sessions.LifecycleCause{sessions.CauseIdleSleep, sessions.CauseCapacityEviction} {
		t.Run(string(cause), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			store := history.New(t.TempDir())
			sink := newStreamTurnSink(16, discardLogger())
			resolve := stubBusyResolve(map[string]string{"a": testConvID})
			busy := newTurnBusyTracker(resolve, discardLogger(), withExitEpoch(sink.exitEpoch))
			e := newInteractiveTurnEmitterV2(nil, historyOnlyBroadcaster{}, discardLogger())
			e.hist = store
			installRuntimeHistory(sink, e, busy)
			trans := newSessionTransitionEmitterV2(historyOnlyBroadcaster{}, resolve, discardLogger())
			trans.hist, trans.runtimeSink, trans.runtimeContext = store, sink, ctx
			produce := sink.sinkForTag(func() string { return "a" }, "claude")
			produce(turnevent.ToolStart{ToolCallID: "pending", Title: "Read"})
			trans.Enqueue(sessions.SessionTransition{ConversationID: testConvID, PreviousID: "a", PreviousAgent: "claude", Cause: cause, Reason: sessions.ReasonEviction, OccurredAt: time.Now().UTC()})
			if ready := sink.takeBoundaries(100); len(ready) != 0 {
				t.Fatal("early eviction passed producer join")
			}
			produce(turnevent.TextChunk{Text: "joined-tail"})
			sink.runnerStopped("a")
			fence := make(chan struct{})
			sink.dispatchPlacement(ctx, func() { close(fence) })
			cleanup := startStreamTurnDrainV2(ctx, sink, e, resolve, busy, discardLogger())
			defer func() { cancel(); cleanup() }()
			select {
			case <-fence:
			case <-time.After(5 * time.Second):
				t.Fatal("eviction fence timed out")
			}
			var order []string
			for _, entry := range historyEntries(t, store, testConvID) {
				switch entry.Type {
				case protocol.TypeAssistantDelta, historyToolInterrupted, historyTurnInterrupted, historySessionDivider:
					order = append(order, entry.Type)
				}
				if entry.Type == historyToolInterrupted || entry.Type == historyTurnInterrupted || entry.Type == historySessionDivider {
					var p runtimeHistoryFact
					_ = json.Unmarshal(entry.Payload, &p)
					if p.Cause != string(cause) {
						t.Fatalf("closure cause: %s", entry.Payload)
					}
				}
				if entry.Type == protocol.TypeSessionTransition {
					var p protocol.SessionTransitionPayload
					_ = json.Unmarshal(entry.Payload, &p)
					if p.Reason != "idle_evict" || p.PreviousSessionID != "a" || p.NewSessionID != "a" {
						t.Fatalf("legacy delimiter changed: %+v", p)
					}
				}
			}
			want := strings.Join([]string{protocol.TypeAssistantDelta, historyToolInterrupted, historyTurnInterrupted, historySessionDivider}, ",")
			if strings.Join(order, ",") != want {
				t.Fatalf("closure order: %v", order)
			}
		})
	}
}

func TestRuntimeHistoryActualResetHandoff(t *testing.T) {
	for _, written := range []bool{false, true} {
		t.Run(map[bool]string{false: "skipped", true: "written"}[written], func(t *testing.T) {
			opt := resetOptions{unresolvable: !written}
			if written {
				opt.answerOnWrite = answerWith("handoff")
			}
			fixture := newResetFixture(t, opt)
			var got string
			runner := &restartFreshRunner{childPID: starterLiveChildPID}
			starter := activeSessionStarter{reset: fixture.reset, log: discardLogger(), rotateWithHandoff: func(id sessions.SessionID, outcome *string) (sessions.SessionID, error) {
				got = *outcome
				return "successor", nil
			}}
			starter.resetThenRotate(func() {}, nil, runner, "old", resetConvA, "", false)
			want := "skipped"
			if written {
				want = "written"
			}
			if got != want || len(runner.restarts) != 1 {
				t.Fatalf("actual handoff=%q, restarts=%v", got, runner.restarts)
			}
		})
	}
}

func TestRuntimeHistorySealedPredecessorCannotCloseSuccessor(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := history.New(t.TempDir())
	sink := newStreamTurnSink(16, discardLogger())
	resolve := stubBusyResolve(map[string]string{"a": testConvID, "b": testConvID})
	busy := newTurnBusyTracker(resolve, discardLogger(), withExitEpoch(sink.exitEpoch))
	e := newInteractiveTurnEmitterV2(nil, historyOnlyBroadcaster{}, discardLogger())
	e.hist = store
	installRuntimeHistory(sink, e, busy)
	trans := newSessionTransitionEmitterV2(historyOnlyBroadcaster{}, resolve, discardLogger())
	trans.hist, trans.runtimeSink, trans.runtimeContext = store, sink, ctx
	a := sink.sinkForTag(func() string { return "a" }, "claude")
	b := sink.sinkForTag(func() string { return "b" }, "codex")
	a(turnevent.ThoughtChunk{Text: "old"})
	trans.Enqueue(sessions.SessionTransition{ConversationID: testConvID, PreviousID: "a", NewID: "b", Cause: sessions.CauseRecovery, OccurredAt: time.Now().UTC()})
	sink.dispatchPlacement(ctx, func() { busy.openForDelivery(testConvID) })
	// A denial opens a main turn without publishing a running phase. Checking
	// only the phase would mistake this real successor turn for idle.
	b(turnevent.ToolCallDenied{ToolCallID: "successor-denied"})
	a(turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn})
	a(turnevent.ThoughtChunk{Text: "late"})
	fence := make(chan struct{})
	sink.dispatchPlacement(ctx, func() { close(fence) })
	cleanup := startStreamTurnDrainV2(ctx, sink, e, resolve, busy, discardLogger())
	select {
	case <-fence:
	case <-time.After(5 * time.Second):
		t.Fatal("sealed-source fence timed out")
	}
	cancel()
	cleanup()
	var opened, interrupted int
	for _, entry := range historyEntries(t, store, testConvID) {
		if entry.Type == historyTurnOpened {
			opened++
		}
		if entry.Type == historyTurnInterrupted {
			interrupted++
		}
	}
	if opened != 2 || interrupted != 1 || !busy.Busy(testConvID) {
		t.Fatalf("opened=%d interrupted=%d busy=%v", opened, interrupted, busy.Busy(testConvID))
	}
	if !e.turns[testConvID+"\x00codex\x00b"].inTurn {
		t.Fatal("successor turn closed")
	}
}

func TestRuntimeHistorySourceClosure(t *testing.T) {
	ctx := context.Background()
	store := history.New(t.TempDir())
	e := newInteractiveTurnEmitterV2(nil, historyOnlyBroadcaster{}, discardLogger())
	e.hist, e.runtimeFacts = store, true
	a := history.SessionProvenance{Kind: "claude", SessionID: "a"}
	b := history.SessionProvenance{Kind: "codex", SessionID: "b"}
	e.HandleFor(ctx, testConvID, turnevent.ThoughtChunk{Text: "private-thought"}, a)
	e.HandleFor(ctx, testConvID, turnevent.ToolStart{ToolCallID: "open", Title: "Read"}, a)
	e.HandleFor(ctx, testConvID, turnevent.ToolStart{ToolCallID: "done", Title: "Read"}, a)
	e.HandleFor(ctx, testConvID, turnevent.ToolUpdate{ToolCallID: "done"}, a)
	e.HandleFor(ctx, testConvID, turnevent.ToolStart{ToolCallID: "denied", Title: "Write"}, a)
	e.HandleFor(ctx, testConvID, turnevent.ToolCallDenied{ToolCallID: "denied"}, a)
	e.HandleFor(ctx, testConvID, turnevent.ToolStart{ToolCallID: "agent", Title: "Agent"}, a)
	e.HandleFor(ctx, testConvID, turnevent.TextChunk{Text: "old-tail"}, a)
	e.HandleFor(ctx, testConvID, turnevent.TextChunk{Text: "new-tail"}, b)
	e.closeRuntimeSource(ctx, testConvID, "a", string(sessions.CauseClaudeClear), time.Now().UTC(), true, 0)
	e.closeRuntimeSource(ctx, testConvID, "a", string(sessions.CauseClaudeClear), time.Now().UTC(), true, 0)
	e.HandleFor(ctx, testConvID, turnevent.ThoughtChunk{Text: "late-thought"}, a)
	e.HandleFor(ctx, testConvID, turnevent.TextChunk{Text: strings.Repeat("x", maxDeltaTextBytes+8)}, a)
	e.flushAll(ctx)
	counts := map[string]int{}
	var oldTurn string
	var lateChunks int
	for _, entry := range historyEntries(t, store, testConvID) {
		counts[entry.Type]++
		if entry.Type == historyTurnOpened && entry.Session != nil && entry.Session.SessionID == "a" {
			var p runtimeHistoryFact
			_ = json.Unmarshal(entry.Payload, &p)
			oldTurn = p.TurnID
		}
		if entry.Type == protocol.TypeAssistantDelta && strings.Contains(string(entry.Payload), "xxxx") {
			var p protocol.AssistantDeltaPayload
			_ = json.Unmarshal(entry.Payload, &p)
			lateChunks++
			if len(p.Text) > maxDeltaTextBytes || p.TurnID != oldTurn {
				t.Fatalf("late delta lost bound/identity: %+v", p)
			}
		}
		if strings.Contains(string(entry.Payload), "private-thought") || strings.Contains(string(entry.Payload), "late-thought") {
			t.Fatal("thought text persisted")
		}
		if entry.Type == historyToolInterrupted || entry.Type == historyTurnInterrupted {
			var p runtimeHistoryFact
			_ = json.Unmarshal(entry.Payload, &p)
			if entry.Session == nil || entry.Session.SessionID != "a" || p.Cause != "claude_clear" || (entry.Type == historyToolInterrupted && p.ToolCallID != "open") || entry.Shown == nil || !*entry.Shown {
				t.Fatalf("wrong interrupted work: %+v %s", entry, entry.Payload)
			}
		}
	}
	if counts[historyTurnOpened] != 2 || counts[historyToolInterrupted] != 1 || counts[historyTurnInterrupted] != 1 {
		t.Fatalf("facts: %v", counts)
	}
	if lateChunks != 2 {
		t.Fatalf("late chunks=%d", lateChunks)
	}
	if !e.turns[testConvID+"\x00codex\x00b"].inTurn {
		t.Fatal("successor work was closed")
	}
}

func TestRuntimeHistoryFactsLegacyIsolation(t *testing.T) {
	for _, mode := range []string{"healthy", "nil", "failed"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			var logs bytes.Buffer
			log := bufLogger(&logs)
			var store *history.Store
			if mode != "nil" {
				root := filepath.Join(t.TempDir(), "private-root-path")
				if mode == "failed" {
					if err := os.WriteFile(root, []byte("occupied"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				store = history.New(root)
			}
			bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "interactive", Interactive: true}, {ConnID: "legacy"}}}}
			e := newInteractiveTurnEmitterV2(nil, bcast, log)
			e.hist, e.runtimeFacts = store, true
			source := history.SessionProvenance{Kind: "claude", SessionID: "a"}
			e.HandleFor(ctx, testConvID, turnevent.ThoughtChunk{Text: "secret-thought"}, source)
			e.HandleFor(ctx, testConvID, turnevent.ToolStart{ToolCallID: "tool", Title: "Read"}, source)
			e.closeRuntimeSource(ctx, testConvID, "a", "operator_reset", time.Now().UTC(), true, 0)
			trans := newSessionTransitionEmitterV2(bcast, nil, log)
			trans.hist = store
			trans.broadcast(ctx, sessions.SessionTransition{ConversationID: testConvID, PreviousID: "a", NewID: "b", PreviousAgent: "claude", NextAgent: "claude", Cause: sessions.CauseOperatorReset, Reason: sessions.ReasonClear, OccurredAt: time.Now().UTC()})
			events, _ := e.ring.After(testConvID, 0)
			for _, event := range events {
				if !legacyHistoryType(event.Type) {
					t.Fatalf("new fact in replay: %s", event.Type)
				}
			}
			if len(bcast.pushes) == 0 {
				t.Fatal("storage suppressed legacy delivery")
			}
			for _, push := range bcast.pushes {
				if !legacyHistoryType(push.env.Type) || push.connID != "interactive" {
					t.Fatalf("legacy gate changed: %+v", push)
				}
			}
			if strings.Contains(logs.String(), "secret-thought") || strings.Contains(logs.String(), "private-root-path") {
				t.Fatalf("content-bearing failure log: %s", logs.String())
			}
			if mode == "healthy" {
				for _, entry := range newHistoryPager(store, log)(testConvID, "", 128).Entries {
					if !legacyHistoryType(entry.Type) {
						t.Fatalf("new fact in legacy page: %s", entry.Type)
					}
				}
			} else if mode == "failed" && logs.Len() == 0 {
				t.Fatal("failed storage not logged")
			}
		})
	}
}

func TestRuntimeHistoryExitCapturesRetiredSource(t *testing.T) {
	sink := newStreamTurnSink(8, discardLogger())
	tag := newStreamSessionTag("a")
	produce, exit := sink.sinkForSessionTag(tag, "claude"), sink.exitForSessionTag(tag)
	produce(turnevent.ThoughtChunk{})
	<-sink.ch
	tag.Rotate("b")
	// Old-child output may arrive after RestartFresh has already rotated the
	// routing tag. It must not replace the identity of the child being stopped.
	produce(turnevent.ToolProgress{})
	<-sink.ch
	exit()
	if env := <-sink.ch; env.sessionID != "b" || env.source.SessionID != "a" {
		t.Fatalf("retired exit: %+v", env)
	}
	produce(turnevent.ThoughtChunk{})
	<-sink.ch
	exit()
	if env := <-sink.ch; env.source.SessionID != "b" {
		t.Fatalf("replacement exit: %+v", env)
	}
}

func TestRuntimeHistoryStaleExitCannotPlaceSuccessorDelivery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := newStreamTurnSink(8, discardLogger())
	resolve := stubBusyResolve(map[string]string{"a": testConvID})
	busy := newTurnBusyTracker(resolve, discardLogger(), withExitEpoch(sink.exitEpoch))
	e := newInteractiveTurnEmitterV2(nil, historyOnlyBroadcaster{}, discardLogger())
	installRuntimeHistory(sink, e, busy)
	sink.sinkForTag(func() string { return "a" }, "claude")(turnevent.ThoughtChunk{})
	sink.exitFor("a")()
	busy.openForDelivery(testConvID)
	var idle atomic.Int32
	observer := func(string) { idle.Add(1) }
	sink.placementIdle.Store(&observer)
	fence := make(chan struct{})
	sink.dispatchPlacement(ctx, func() { close(fence) })
	cleanup := startStreamTurnDrainV2(ctx, sink, e, resolve, busy, discardLogger())
	defer func() { cancel(); cleanup() }()
	select {
	case <-fence:
	case <-time.After(5 * time.Second):
		t.Fatal("stale-exit fence timed out")
	}
	if idle.Load() != 0 || !busy.Busy(testConvID) {
		t.Fatalf("successor placement: idle=%d busy=%v", idle.Load(), busy.Busy(testConvID))
	}
}
