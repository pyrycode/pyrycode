package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay/handlers"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/thread"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// The helper blocks opening a separate FIFO. No code path reads stdin.
func TestQueuedPlacementHelperProcess(t *testing.T) {
	if os.Getenv("PYRY_UNREAD_HELPER") != "1" {
		return
	}
	fifo := os.Getenv("PYRY_UNREAD_FIFO")
	marker, err := os.OpenFile(fifo+".spawned", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err == nil {
		marker.Close()
	} else if os.IsExist(err) {
		fmt.Println(`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"small pipe write"}]}}`)
		fmt.Println(assistantTextLine("successor-turn", "successor output"))
		fmt.Println(`{"type":"result","subtype":"success","is_error":false,"result":"successor done"}`)
	} else {
		os.Exit(2)
	}
	if err == nil && os.Getenv("PYRY_UNREAD_BUSY") == "1" {
		fmt.Println(assistantTextLine("unread-turn", "working"))
	}
	f, err := os.Open(fifo)
	if err != nil {
		os.Exit(2)
	}
	defer f.Close()
	os.Exit(3)
}

func TestQueuedPlacementOwnership(t *testing.T) {
	for _, scenario := range []string{"echo_before_exit", "completion_before_exit", "result_only_completion", "send_now_grace", "exit_during_grace", "teardown_during_grace", "teardown", "same_id_successor", "cancel", "stale_close"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			resolve := stubBusyResolve(map[string]string{"s": testConvID, "next": testConvID, "foreign": testConvIDB})
			sink := newStreamTurnSink(64, discardLogger())
			busy := newTurnBusyTracker(resolve, discardLogger(), withSendNowGrace(time.Hour))
			store := history.New(t.TempDir())
			h := testSendHistory(store)
			place := newSendNowPlacement(ctx, resolve, busy.WaitIdle)
			place.sendHistory = h
			place.bindQueued(ctx, sink, store, func(string) bool { return true }, discardLogger())
			published := make(chan operatorMessage, 8)
			sink.setOperatorPublisher(func(m operatorMessage) { published <- m })
			emitter := newInteractiveTurnEmitterV2(nil, historyOnlyBroadcaster{}, discardLogger())
			emitter.hist = store
			installRuntimeHistory(sink, emitter, busy)
			stop := startStreamTurnDrainV2(ctx, sink, emitter, resolve, busy, discardLogger())
			defer func() { cancel(); stop() }()
			child, successor := &bytes.Buffer{}, &bytes.Buffer{}
			send := func(target io.Writer, ev turnevent.Event) {
				sink.forwardEvent(streamTurnEnvelope{sessionID: "s", child: target, ev: ev})
			}
			register := func(id uint64, target io.Writer, now bool) *placedMessage {
				m := msgqueue.QueuedMessage{ID: id, MessageID: "same-app-id", Text: "safe", TS: time.Now(), SentNow: now}
				h.accepted(testConvID, m)
				e := &placedMessage{id: id, digest: sha256.Sum256([]byte("identical")), queued: true, sentNow: now, child: target, outcome: make(chan struct{}), after: place.position()}
				e.commit = func() { place.record(testConvID, m) }
				close(e.outcome)
				place.mu.Lock()
				if place.managed == nil {
					place.managed = map[string]map[uint64]*placedMessage{}
				}
				if place.managed[testConvID] == nil {
					place.managed[testConvID] = map[uint64]*placedMessage{}
				}
				place.managed[testConvID][id] = e
				place.pending[testConvID] = append(place.pending[testConvID], e)
				place.mu.Unlock()
				return e
			}
			now := scenario == "send_now_grace" || scenario == "exit_during_grace" || scenario == "teardown_during_grace"
			busy.observe("s", snOpener)
			if now {
				if ok, _, finished := busy.beginSendNow(testConvID); !ok {
					t.Fatal("no send-now carry")
				} else {
					finished()
				}
			}
			old := register(1, child, now)
			if scenario == "stale_close" {
				old.after = place.position() + 1
			}
			wantOld := false
			switch scenario {
			case "echo_before_exit":
				send(child, echoOf("identical"))
				wantOld = true
			case "completion_before_exit":
				send(child, snOpener)
				send(child, snEnd)
				wantOld = true
			case "result_only_completion":
				send(child, snEnd)
				wantOld = true
			case "send_now_grace", "exit_during_grace", "teardown_during_grace":
				send(child, snEnd)
				placementDrainBarrier(t, ctx, sink)
				if len(published) != 0 {
					t.Fatal("send now skipped grace")
				}
				if scenario == "send_now_grace" {
					busy.mu.Lock()
					gen := busy.carried[testConvID]
					busy.mu.Unlock()
					busy.releaseCarried(testConvID, gen)
					wantOld = true
				}
				if scenario == "teardown_during_grace" {
					sink.queueBoundary(sessions.SessionTransition{ConversationID: testConvID, PreviousID: "s", NewID: "next", Reason: sessions.ReasonClear, Cause: sessions.CauseOperatorReset}, func() {}, nil)
				}
			case "teardown":
				sink.queueBoundary(sessions.SessionTransition{ConversationID: testConvID, PreviousID: "s", NewID: "next", Reason: sessions.ReasonClear, Cause: sessions.CauseOperatorReset}, func() {}, nil)
			case "stale_close":
				send(child, snEnd)
			case "cancel":
				cancel()
			}
			if scenario != "cancel" {
				placementDrainBarrier(t, ctx, sink)
			}
			// Exercise the readiness fallback explicitly after its release, avoiding timer-based negative assertions.
			if scenario != "exit_during_grace" {
				place.takeQueued(testConvID, old)
			}
			if scenario != "cancel" && scenario != "send_now_grace" {
				sink.offer(streamTurnEnvelope{sessionID: "s", child: child, exit: true, exitEpoch: sink.exits.Add(1)}, true)
				placementDrainBarrier(t, ctx, sink)
				place.takeQueued(testConvID, old)
			}
			// A delayed callback must acknowledge retirement without falling through to a fresh commit.
			place.attach(testConvID, 1, func() { t.Error("retired callback committed") })
			if scenario != "cancel" {
				newEntry := register(2, successor, false)
				send(child, echoOf("identical"))
				send(child, snOpener)
				send(child, snEnd)
				sink.forwardEvent(streamTurnEnvelope{sessionID: "foreign", child: successor, ev: echoOf("identical")})
				placementDrainBarrier(t, ctx, sink)
				place.takeQueued(testConvID, newEntry)
				if len(published) != map[bool]int{true: 1}[wantOld] {
					t.Fatal("late/foreign child stole identical payload")
				}
				send(successor, echoOf("identical"))
				send(successor, echoOf("identical"))
				send(successor, snEnd)
				placementDrainBarrier(t, ctx, sink)
				place.attach(testConvID, 2, func() { t.Error("duplicate callback committed") })
			}
			counts := map[uint64]int{}
			for _, entry := range historyEntries(t, store, testConvID) {
				if entry.Type == historySendDelivered {
					counts[testSendFact(t, entry).AcceptedEntryID]++
				}
			}
			accepted := map[uint64]uint64{}
			for _, entry := range historyEntries(t, store, testConvID) {
				if entry.Type == historySendAccepted {
					accepted[testSendFact(t, entry).QueuedMessageID] = entry.ID
				}
			}
			if counts[accepted[1]] != map[bool]int{true: 1}[wantOld] {
				t.Fatalf("old delivery count=%d", counts[accepted[1]])
			}
			if scenario != "cancel" && counts[accepted[2]] != 1 {
				t.Fatal("successor confirmation missing or duplicated")
			}
		})
	}
}

func placementDrainBarrier(t *testing.T, ctx context.Context, sink *streamTurnSink) {
	t.Helper()
	done := make(chan struct{})
	sink.dispatchPlacement(ctx, func() { close(done) })
	awaitProvenanceSignal(t, done)
}

func TestQueuedPlacementRetireDoesNotWaitForSuccessorWrite(t *testing.T) {
	t.Parallel()
	place := newSendNowPlacement(context.Background(), stubBusyResolve(map[string]string{"s": testConvID}), nil)
	old := &placedMessage{id: 1, queued: true, child: &bytes.Buffer{}, outcome: make(chan struct{})}
	next := &placedMessage{id: 2, queued: true, digest: sha256.Sum256([]byte("identical")), child: &bytes.Buffer{}, outcome: make(chan struct{})}
	close(old.outcome)
	place.pending[testConvID] = []*placedMessage{old, next}
	done := make(chan struct{})
	go func() {
		place.retire("", "s", old.child)
		place.echoOwned("s", echoOf("identical"), old.child, 1)
		place.idleOwned("s", old.child, 1)
		close(done)
	}()
	defer func() { close(next.outcome); <-done }()
	awaitProvenanceSignal(t, done)
	if len(place.pending[testConvID]) != 1 || place.pending[testConvID][0] != next {
		t.Fatal("old exit retired the successor's in-progress write")
	}
}

func TestQueuedPlacementUnreadPipe(t *testing.T) {
	for _, now := range []bool{false, true} {
		t.Run(fmt.Sprintf("send_now=%v", now), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			dir := t.TempDir()
			fifo := filepath.Join(dir, "unread.fifo")
			if err := syscall.Mkfifo(fifo, 0600); err != nil {
				t.Fatal(err)
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(dir, "fake-claude")
			script := fmt.Sprintf("#!/bin/sh\nPYRY_UNREAD_HELPER=1 PYRY_UNREAD_BUSY=%d PYRY_UNREAD_FIFO=%q exec %q -test.run=^TestQueuedPlacementHelperProcess$\n", map[bool]int{true: 1}[now], fifo, executable)
			if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			resolve := stubBusyResolve(map[string]string{"unread-session": testConvID})
			sink := newStreamTurnSink(64, discardLogger())
			busy := newTurnBusyTracker(resolve, discardLogger(), withExitEpoch(sink.exitEpoch))
			store := history.New(t.TempDir())
			h := testSendHistory(store)
			place := newSendNowPlacement(ctx, resolve, busy.WaitIdle)
			place.sendHistory = h
			place.bindQueued(ctx, sink, store, func(string) bool { return true }, discardLogger())
			sink.setEchoObserver(place.echo)
			published := make(chan operatorMessage, 8)
			sink.setOperatorPublisher(func(m operatorMessage) { published <- m })
			emitter := newInteractiveTurnEmitterV2(nil, historyOnlyBroadcaster{}, discardLogger())
			emitter.hist = store
			installRuntimeHistory(sink, emitter, busy)
			stop := startStreamTurnDrainV2(ctx, sink, emitter, resolve, busy, discardLogger())
			defer func() { cancel(); stop() }()
			runner := runFactoryRunner(t, sink, bin, "unread-session")
			testShadowWait(t, func() bool { return runner.State().ChildPID != 0 && (!now || busy.Busy(testConvID)) })
			testShadowWait(t, func() bool { _, err := os.Stat(fifo + ".spawned"); return err == nil })
			pid := runner.State().ChildPID
			oldTarget := runner.(streamRunner).r.ChildTarget()
			writer := funcWriter{write: runner.WriteUserTurn}
			writerFor := func(string) (handlers.TurnWriter, error) { return writer, nil }
			written := make(chan struct{}, 1)
			record := newOperatorMessageHistory(store, nil, place, discardLogger())
			q, err := msgqueue.New(msgqueue.Config{
				Deliver:    newInboundDeliver(writerFor, busy, time.Second, place),
				SendNow:    newSendNowDeliver(writerFor, func(string) bool { return true }, busy, place),
				OnAccepted: h.accepted, OnTerminal: h.terminal,
				OnDelivered: func(c string, m msgqueue.QueuedMessage) { record(c, m); written <- struct{}{} },
				Logger:      discardLogger(),
			})
			if err != nil {
				t.Fatal(err)
			}
			id := q.EnqueueIdentified(testConvID, "original-app-id", "recoverable text", "small pipe write", []string{"attachment"}, "device", "phone", "1", time.Now())
			if now {
				if !q.SendNow(testConvID, id) {
					t.Fatal("send now refused")
				}
			} else {
				done := make(chan error, 1)
				go func() { done <- q.Run(ctx) }()
				defer func() { cancel(); <-done }()
			}
			awaitProvenanceSignal(t, written)
			placementDrainBarrier(t, ctx, sink)
			child, err := os.FindProcess(pid)
			if err != nil {
				t.Fatal(err)
			}
			if err := child.Kill(); err != nil {
				t.Fatal(err)
			}
			waitCtx, waitCancel := context.WithTimeout(ctx, 5*time.Second)
			defer waitCancel()
			if err := busy.WaitIdle(waitCtx, testConvID); err != nil {
				t.Fatal(err)
			}
			placementDrainBarrier(t, ctx, sink)
			// The factory respawns inside the same Run and reuses the session ID.
			testShadowWait(t, func() bool {
				state := runner.State()
				return state.ChildPID != 0 && state.ChildPID != pid
			})
			testShadowWait(t, func() bool {
				for _, entry := range historyEntries(t, store, testConvID) {
					if bytes.Contains(entry.Payload, []byte("successor output")) {
						return true
					}
				}
				return false
			})
			placementDrainBarrier(t, ctx, sink)
			if runner.(streamRunner).r.ChildTarget() == oldTarget {
				t.Fatal("respawn reused child ownership")
			}
			// A delayed waiter must also have reached its dispatch before checking.
			place.mu.Lock()
			for _, e := range place.pending[testConvID] {
				place.mu.Unlock()
				place.takeQueued(testConvID, e)
				place.mu.Lock()
			}
			place.mu.Unlock()
			entries := historyEntries(t, store, testConvID)
			for _, e := range entries {
				if e.Type == historySendDelivered || e.Type == protocol.TypeMessage {
					t.Fatalf("unread pipe became delivered: %s", e.Type)
				}
			}
			var accepted history.Entry
			for _, entry := range entries {
				if entry.Type == historySendAccepted {
					accepted = entry
					break
				}
			}
			fact := testSendFact(t, accepted)
			if fact.QueuedMessageID != id || fact.MessageID != "original-app-id" || fact.Text != "recoverable text" || fact.DeviceID != "device" || len(fact.AttachmentIDs) != 1 {
				t.Fatalf("lost acceptance identity: %+v", fact)
			}
			fold := thread.New(testConvID)
			if err := fold.Feed(entries); err != nil {
				t.Fatal(err)
			}
			want := "queued"
			if now {
				want = "written"
			}
			found := false
			for _, item := range fold.Items() {
				if item.ID == accepted.ID {
					found = true
					if string(item.Status) != want {
						t.Fatalf("status=%s want %s", item.Status, want)
					}
				}
			}
			if !found || len(published) != 0 || len(q.Snapshot(testConvID)) != 0 {
				t.Fatal("missing recovery item, false publication or automatic replay")
			}
		})
	}
}
