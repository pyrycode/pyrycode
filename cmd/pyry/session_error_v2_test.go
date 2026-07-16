package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
)

// decodeSessionError asserts the envelope type and decodes its SessionErrorPayload.
func decodeSessionError(t *testing.T, env protocol.Envelope) protocol.SessionErrorPayload {
	t.Helper()
	if env.Type != protocol.TypeSessionError {
		t.Fatalf("env type = %q, want %q", env.Type, protocol.TypeSessionError)
	}
	var p protocol.SessionErrorPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatalf("decode session_error payload: %v", err)
	}
	return p
}

// bcastConns builds a fakeInteractiveBcast over a single fixed conn snapshot.
func bcastConns(conns ...relay.ActiveConn) *fakeInteractiveBcast {
	return &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{conns}}
}

// AC-1: a give-up notice fans exactly one session_error to every interactive
// conn, each decoding to {convID, CodeSessionBlocked, reason}. Env IDs increment
// per conn (nextID is Run-goroutine-local).
func TestSessionErrorEmitterV2_Broadcast_FansOnePerInteractiveConn(t *testing.T) {
	t.Parallel()
	bcast := bcastConns(
		relay.ActiveConn{ConnID: "c1", Interactive: true},
		relay.ActiveConn{ConnID: "c2", Interactive: true},
	)
	e := newSessionErrorEmitterV2(nil, discardLogger())

	e.broadcast(context.Background(), bcast, giveUpNotice{convID: "conv-A", reason: "wedged at startup"})

	if len(bcast.pushes) != 2 {
		t.Fatalf("want 2 pushes, got %d", len(bcast.pushes))
	}
	for i, want := range []string{"c1", "c2"} {
		if bcast.pushes[i].connID != want {
			t.Errorf("push[%d] to %q, want %q", i, bcast.pushes[i].connID, want)
		}
		got := decodeSessionError(t, bcast.pushes[i].env)
		if got.ConversationID != "conv-A" {
			t.Errorf("push[%d] conversation_id = %q, want conv-A", i, got.ConversationID)
		}
		if got.Code != protocol.CodeSessionBlocked {
			t.Errorf("push[%d] code = %q, want %q", i, got.Code, protocol.CodeSessionBlocked)
		}
		if got.Message != "wedged at startup" {
			t.Errorf("push[%d] message = %q, want %q", i, got.Message, "wedged at startup")
		}
	}
	if bcast.pushes[0].env.ID != 1 || bcast.pushes[1].env.ID != 2 {
		t.Errorf("env IDs = %d,%d, want 1,2", bcast.pushes[0].env.ID, bcast.pushes[1].env.ID)
	}
}

// AC-2: the capability gate — a snapshot mixing interactive and non-interactive
// conns delivers only to the interactive ones.
func TestSessionErrorEmitterV2_Broadcast_SkipsNonInteractive(t *testing.T) {
	t.Parallel()
	bcast := bcastConns(
		relay.ActiveConn{ConnID: "c1", Interactive: true},
		relay.ActiveConn{ConnID: "c2", Interactive: false},
		relay.ActiveConn{ConnID: "c3", Interactive: true},
	)
	e := newSessionErrorEmitterV2(nil, discardLogger())

	e.broadcast(context.Background(), bcast, giveUpNotice{convID: "conv-A", reason: "r"})

	if len(bcast.pushes) != 2 {
		t.Fatalf("want 2 pushes (interactive only), got %d", len(bcast.pushes))
	}
	for i, want := range []string{"c1", "c3"} {
		if bcast.pushes[i].connID != want {
			t.Errorf("push[%d] to %q, want %q (non-interactive c2 must be skipped)", i, bcast.pushes[i].connID, want)
		}
	}
}

// AC-2: the producer stamps the fixed terminal CodeSessionBlocked regardless of
// the notice contents — the seam carries no code field, so a client can never
// read the frame as the transient CodeServerBinaryBusy.
func TestSessionErrorEmitterV2_Broadcast_StampsTerminalCode(t *testing.T) {
	t.Parallel()
	bcast := oneInteractiveConn("c1")
	e := newSessionErrorEmitterV2(nil, discardLogger())

	// A reason that mentions "busy" must NOT flip the terminal code.
	e.broadcast(context.Background(), bcast, giveUpNotice{convID: "conv-A", reason: "server was busy"})

	got := decodeSessionError(t, bcast.pushes[0].env)
	if got.Code != protocol.CodeSessionBlocked {
		t.Errorf("code = %q, want %q", got.Code, protocol.CodeSessionBlocked)
	}
	if got.Code == protocol.CodeServerBinaryBusy {
		t.Errorf("code must not be the transient %q", protocol.CodeServerBinaryBusy)
	}
}

// AC-1/AC-3: the daemon reason round-trips into Message verbatim. The producer
// holds no queue handle, so "no queued text" is structural; this pins the
// pass-through of the (sanitised) daemon reason.
func TestSessionErrorEmitterV2_Broadcast_MessageVerbatim(t *testing.T) {
	t.Parallel()
	const reason = "delivery failed persistently for 2m0s; claude session may be wedged (last error: readiness gate)"
	bcast := oneInteractiveConn("c1")
	e := newSessionErrorEmitterV2(nil, discardLogger())

	e.broadcast(context.Background(), bcast, giveUpNotice{convID: "conv-A", reason: reason})

	got := decodeSessionError(t, bcast.pushes[0].env)
	if got.Message != reason {
		t.Errorf("message = %q, want %q", got.Message, reason)
	}
}

// A per-conn Push error must not abort the fan-out — the remaining conns still
// receive their frame.
func TestSessionErrorEmitterV2_Broadcast_PushErrorContinuesLoop(t *testing.T) {
	t.Parallel()
	bcast := bcastConns(
		relay.ActiveConn{ConnID: "c1", Interactive: true},
		relay.ActiveConn{ConnID: "c2", Interactive: true},
		relay.ActiveConn{ConnID: "c3", Interactive: true},
	)
	bcast.pushErr = map[string]error{"c1": context.Canceled}
	e := newSessionErrorEmitterV2(nil, discardLogger())

	e.broadcast(context.Background(), bcast, giveUpNotice{convID: "conv-A", reason: "r"})

	// All three are attempted (the failing c1 is recorded too); c2 and c3 succeed.
	got := pushTypes(bcast.pushes)
	if len(got) != 3 {
		t.Fatalf("want 3 push attempts (loop continues past c1 error), got %d", len(got))
	}
	if len(pushesFor(bcast.pushes, "c2")) != 1 || len(pushesFor(bcast.pushes, "c3")) != 1 {
		t.Errorf("c2/c3 must each receive a push after c1 failed; pushes = %+v", bcast.pushes)
	}
}

// AC-4: sessionErrorNotify must never block on a full channel (GiveUpFunc's
// MUST-NOT-BLOCK contract) — a send to a full buffer drops with a content-free
// Warn that carries the conversation_id but NEVER the reason.
func TestSessionErrorNotify_DropOnFullDoesNotBlock(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	ch := make(chan giveUpNotice, 1)
	notify := sessionErrorNotify(ch, logger)

	notify("conv-A", "first-reason") // fills the cap-1 buffer

	const secretReason = "SENTINEL-untrusted-looking-reason"
	done := make(chan struct{})
	go func() {
		notify("conv-B", secretReason) // buffer full → must drop, not block
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("sessionErrorNotify blocked on a full channel")
	}

	if got := <-ch; got.convID != "conv-A" || got.reason != "first-reason" {
		t.Errorf("buffered notice = %+v, want {conv-A first-reason}", got)
	}
	logs := buf.String()
	if !strings.Contains(logs, "queue_full") {
		t.Errorf("missing queue_full warn; logs = %q", logs)
	}
	if !strings.Contains(logs, "conv-B") {
		t.Errorf("drop warn should name the dropped conversation_id; logs = %q", logs)
	}
	if strings.Contains(logs, secretReason) {
		t.Errorf("drop warn must NOT log the reason (never-log discipline); logs = %q", logs)
	}
}

// AC-4: the channel carries BOTH the convID and the one-shot reason — a
// bare-convID regression (that would lose the un-recoverable reason) must fail.
func TestSessionErrorNotify_DeliversFullNotice(t *testing.T) {
	t.Parallel()
	ch := make(chan giveUpNotice, sessionErrorQueueSize)
	notify := sessionErrorNotify(ch, discardLogger())

	notify("conv-X", "blocked by an unexpected startup dialog")

	select {
	case got := <-ch:
		if got.convID != "conv-X" {
			t.Errorf("convID = %q, want conv-X", got.convID)
		}
		if got.reason != "blocked by an unexpected startup dialog" {
			t.Errorf("reason = %q, want the full one-shot reason", got.reason)
		}
	default:
		t.Fatal("notify did not deliver the notice to the channel")
	}
}

// End-to-end through the channel: a notice drains through Run and produces a
// session_error on the interactive conn; ctx-cancel stops Run.
func TestSessionErrorEmitterV2_Run_DeliversFromChannel(t *testing.T) {
	t.Parallel()
	ch := make(chan giveUpNotice, sessionErrorQueueSize)
	see := newSessionErrorEmitterV2(ch, discardLogger())

	pushed := make(chan struct{}, 1)
	bcast := &notifyingBcast{
		inner:  oneInteractiveConn("c1"),
		pushed: pushed,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cleanup := startSessionErrorStreamV2(ctx, see, bcast)
	t.Cleanup(func() {
		cancel()
		cleanup()
	})

	sessionErrorNotify(ch, discardLogger())("conv-A", "wedged")

	select {
	case <-pushed:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not push a session_error for the notified give-up")
	}
}

// AC-5: startSessionErrorStreamV2's cleanup joins the Run goroutine on ctx-cancel
// and is idempotent (no leaked goroutine).
func TestStartSessionErrorStreamV2_CleanupJoinsOnCancel(t *testing.T) {
	t.Parallel()
	ch := make(chan giveUpNotice, sessionErrorQueueSize)
	see := newSessionErrorEmitterV2(ch, discardLogger())
	bcast := oneInteractiveConn("c1")

	ctx, cancel := context.WithCancel(context.Background())
	cleanup := startSessionErrorStreamV2(ctx, see, bcast)

	cancel()
	done := make(chan struct{})
	go func() {
		cleanup()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup did not return after ctx cancel")
	}
	cleanup() // idempotent
}

// Race coverage: OnGiveUp fires from many goroutines while Run drains. nextID is
// touched only by Run, the channel is concurrency-safe, and bcast is read only by
// Run — so -race must report nothing.
func TestSessionErrorEmitterV2_Run_ConcurrentNotify(t *testing.T) {
	t.Parallel()
	ch := make(chan giveUpNotice, sessionErrorQueueSize)
	see := newSessionErrorEmitterV2(ch, discardLogger())
	bcast := oneInteractiveConn("c1")
	notify := sessionErrorNotify(ch, discardLogger())

	ctx, cancel := context.WithCancel(context.Background())
	cleanup := startSessionErrorStreamV2(ctx, see, bcast)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				notify("conv-A", "wedged") // drop-on-full is fine; we assert no race/panic
			}
		}()
	}
	wg.Wait()

	cancel()
	cleanup()
}
