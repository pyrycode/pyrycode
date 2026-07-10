package relay

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// --- #878 connect-time queue reconcile fixtures (twin of the #877 modal set) ---

// sampleQueueTS gives each queued id a stable, whole-second UTC timestamp so the
// payload round-trips through JSON exactly (compared via time.Time.Equal).
func sampleQueueTS(id uint64) time.Time {
	return time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC).Add(time.Duration(id) * time.Second)
}

// sampleQueuePayload builds a fully-populated QueueStatePayload for convID with
// one QueuedItem per id, standing in for one entry the OutstandingQueues seam
// enumerates. A fresh Queued slice per call mirrors SnapshotAll's clone-on-read.
func sampleQueuePayload(convID string, ids ...uint64) protocol.QueueStatePayload {
	items := make([]protocol.QueuedItem, 0, len(ids))
	for _, id := range ids {
		items = append(items, protocol.QueuedItem{
			QueuedMsgID: id,
			Text:        fmt.Sprintf("queued-%s-%d", convID, id),
			TS:          sampleQueueTS(id),
		})
	}
	return protocol.QueueStatePayload{ConversationID: convID, Queued: items}
}

// equalQueued compares two queued-item lists by queued_msg_id, text, and ts (via
// time.Time.Equal — JSON round-trips strip the monotonic reading, the
// project-wide rule; reflect.DeepEqual on a time.Time field is unsafe).
func equalQueued(a, b []protocol.QueuedItem) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].QueuedMsgID != b[i].QueuedMsgID || a[i].Text != b[i].Text || !a[i].TS.Equal(b[i].TS) {
			return false
		}
	}
	return true
}

// reconciledQueues decrypts every noise_msg addressed to connID under recv (in
// recorded, hence AEAD-nonce, order) and returns each queue_state keyed by
// conversation_id. In these tests the reconcile is the only outbound app traffic,
// so a non-queue_state noise_msg is a bug; a repeated conversation_id is a
// fan-out/dup bug. Decrypt each conn's frames exactly once per test — Decrypt
// advances the recv nonce.
func reconciledQueues(t *testing.T, rec *v2Recorder, connID string, recv *noise.CipherState) map[string]protocol.QueueStatePayload {
	t.Helper()
	out := make(map[string]protocol.QueueStatePayload)
	for _, env := range noiseMsgsForConn(t, rec, connID) {
		inner := decryptAppFrame(t, env, recv)
		if inner.Type != protocol.TypeQueueState {
			t.Fatalf("conn %q: noise_msg Type = %q, want %q", connID, inner.Type, protocol.TypeQueueState)
		}
		var p protocol.QueueStatePayload
		if err := json.Unmarshal(inner.Payload, &p); err != nil {
			t.Fatalf("conn %q: decode queue_state payload: %v", connID, err)
		}
		if _, dup := out[p.ConversationID]; dup {
			t.Errorf("conn %q: conversation_id %q re-sent more than once", connID, p.ConversationID)
		}
		out[p.ConversationID] = p
	}
	return out
}

// TestV2Session_QueueReconcile_InteractiveOpen re-sends the one non-empty
// conversation's queue_state to a freshly interactive-open conn, carrying that
// conversation_id and its queued items (AC1).
func TestV2Session_QueueReconcile_InteractiveOpen(t *testing.T) {
	t.Parallel()

	const (
		connA  = "c-v2-A"
		convID = "conv-reconcile-one"
	)
	want := sampleQueuePayload(convID, 1, 2)

	respPriv, respPub := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)
	frames := make(chan protocol.RoutingEnvelope, 8)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:            frames,
		Outbound:          rec.outbound,
		StaticPriv:        respPriv,
		Devices:           reg,
		ServerID:          v2TestServerID,
		Logger:            silentLogger(),
		OutstandingQueues: func() []protocol.QueueStatePayload { return []protocol.QueueStatePayload{sampleQueuePayload(convID, 1, 2)} },
	})
	t.Cleanup(stop)

	_, aRecv := openModalConn(t, mgr, frames, rec, respPub, connA, []string{protocol.CapabilityInteractive})

	// noise_resp + one reconciled queue_state = 2 envelopes; exactly two are ever
	// emitted here, so the snapshot is final once two are recorded.
	waitForEnvelopes(t, rec, 2)

	got := reconciledQueues(t, rec, connA, aRecv)
	if len(got) != 1 {
		t.Fatalf("conn %q: got %d queue_state, want 1", connA, len(got))
	}
	if got[convID].ConversationID != convID {
		t.Errorf("conn %q: conversation_id = %q, want %q", connA, got[convID].ConversationID, convID)
	}
	if !equalQueued(got[convID].Queued, want.Queued) {
		t.Errorf("conn %q: queued = %+v, want %+v", connA, got[convID].Queued, want.Queued)
	}
}

// TestV2Session_QueueReconcile_TwoConversations re-sends every non-empty
// conversation's queue_state, keyed and matched by conversation_id,
// order-independent (AC1).
func TestV2Session_QueueReconcile_TwoConversations(t *testing.T) {
	t.Parallel()

	const (
		connA = "c-v2-A"
		conv1 = "conv-reconcile-1"
		conv2 = "conv-reconcile-2"
	)
	want1, want2 := sampleQueuePayload(conv1, 1), sampleQueuePayload(conv2, 1, 2)

	respPriv, respPub := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)
	frames := make(chan protocol.RoutingEnvelope, 8)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   rec.outbound,
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
		OutstandingQueues: func() []protocol.QueueStatePayload {
			return []protocol.QueueStatePayload{sampleQueuePayload(conv1, 1), sampleQueuePayload(conv2, 1, 2)}
		},
	})
	t.Cleanup(stop)

	_, aRecv := openModalConn(t, mgr, frames, rec, respPub, connA, []string{protocol.CapabilityInteractive})

	// noise_resp + two reconciled queue_state = 3 envelopes.
	waitForEnvelopes(t, rec, 3)

	got := reconciledQueues(t, rec, connA, aRecv)
	if len(got) != 2 {
		t.Fatalf("conn %q: got %d queue_state, want 2", connA, len(got))
	}
	if got[conv1].ConversationID != conv1 || !equalQueued(got[conv1].Queued, want1.Queued) {
		t.Errorf("conn %q: conv %q = %+v, want %+v", connA, conv1, got[conv1], want1)
	}
	if got[conv2].ConversationID != conv2 || !equalQueued(got[conv2].Queued, want2.Queued) {
		t.Errorf("conn %q: conv %q = %+v, want %+v", connA, conv2, got[conv2], want2)
	}
}

// TestV2Session_QueueReconcile_UnicastOnlyOpeningConn proves the re-send is
// unicast to the just-opened conn, NOT a fan-out: with A already open, opening B
// delivers the queue_state to B only — A receives no new queue_state (AC2). The
// #722 producer broadcasts to every open interactive conn on change; this
// reconcile must address only s.connID.
func TestV2Session_QueueReconcile_UnicastOnlyOpeningConn(t *testing.T) {
	t.Parallel()

	const (
		connA  = "c-v2-A" // interactive; opened first
		connB  = "c-v2-B" // interactive; opened second
		convID = "conv-reconcile-unicast"
	)

	respPriv, respPub := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)
	frames := make(chan protocol.RoutingEnvelope, 8)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:            frames,
		Outbound:          rec.outbound,
		StaticPriv:        respPriv,
		Devices:           reg,
		ServerID:          v2TestServerID,
		Logger:            silentLogger(),
		OutstandingQueues: func() []protocol.QueueStatePayload { return []protocol.QueueStatePayload{sampleQueuePayload(convID, 1)} },
	})
	t.Cleanup(stop)

	_, aRecv := openModalConn(t, mgr, frames, rec, respPub, connA, []string{protocol.CapabilityInteractive})
	_, bRecv := openModalConn(t, mgr, frames, rec, respPub, connB, []string{protocol.CapabilityInteractive})

	// resp(A) + queue_state(A) + resp(B) + queue_state(B) = 4 in the correct
	// (unicast) case. A broadcast on B's open would push a 5th (a second
	// queue_state to A), which the count + A-side dup check below would catch.
	waitForEnvelopes(t, rec, 4)

	gotA := reconciledQueues(t, rec, connA, aRecv)
	gotB := reconciledQueues(t, rec, connB, bRecv)
	if len(gotA) != 1 {
		t.Errorf("conn %q (opened first): got %d queue_state, want exactly 1 (its own open, none from B's open)", connA, len(gotA))
	}
	if len(gotB) != 1 {
		t.Errorf("conn %q (opened second): got %d queue_state, want 1", connB, len(gotB))
	}
}

// TestV2Session_QueueReconcile_NonInteractiveNoResend withholds the re-send from
// a conn that handshook without the interactive capability (AC4). The interactive
// control conn A proves the seam WAS enumerating a backlog, so C's zero is the
// capability gate, not an empty seam.
func TestV2Session_QueueReconcile_NonInteractiveNoResend(t *testing.T) {
	t.Parallel()

	const (
		connC  = "c-v2-C" // non-interactive; must receive nothing
		connA  = "c-v2-A" // interactive control
		convID = "conv-reconcile-noninteractive"
	)

	respPriv, respPub := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)
	frames := make(chan protocol.RoutingEnvelope, 8)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:            frames,
		Outbound:          rec.outbound,
		StaticPriv:        respPriv,
		Devices:           reg,
		ServerID:          v2TestServerID,
		Logger:            silentLogger(),
		OutstandingQueues: func() []protocol.QueueStatePayload { return []protocol.QueueStatePayload{sampleQueuePayload(convID, 1)} },
	})
	t.Cleanup(stop)

	// Open the non-interactive conn first and let it fully settle (its reconcile
	// runs synchronously in handleNoiseInit and pushes nothing) before the
	// interactive control conn's multi-step handshake pumps the Run loop.
	openModalConn(t, mgr, frames, rec, respPub, connC, nil)
	_, aRecv := openModalConn(t, mgr, frames, rec, respPub, connA, []string{protocol.CapabilityInteractive})

	// resp(C) + resp(A) + queue_state(A) = 3 envelopes.
	waitForEnvelopes(t, rec, 3)

	if got := reconciledQueues(t, rec, connA, aRecv); len(got) != 1 {
		t.Fatalf("interactive control conn %q: got %d queue_state, want 1", connA, len(got))
	}
	if msgs := noiseMsgsForConn(t, rec, connC); len(msgs) != 0 {
		t.Errorf("non-interactive conn %q: got %d noise_msg, want 0", connC, len(msgs))
	}
}

// TestV2Session_QueueReconcile_NothingOutstanding sends no queue_state when the
// seam enumerates nothing (AC3 — the adapter omits empty conversations, so an
// empty slice means nothing pending). The empty slice short-circuits reconcile
// before any Push, so the only envelope is the handshake noise_resp.
func TestV2Session_QueueReconcile_NothingOutstanding(t *testing.T) {
	t.Parallel()

	const connA = "c-v2-A"

	respPriv, respPub := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)
	frames := make(chan protocol.RoutingEnvelope, 8)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:            frames,
		Outbound:          rec.outbound,
		StaticPriv:        respPriv,
		Devices:           reg,
		ServerID:          v2TestServerID,
		Logger:            silentLogger(),
		OutstandingQueues: func() []protocol.QueueStatePayload { return nil }, // no non-empty conversation
	})
	t.Cleanup(stop)

	openModalConn(t, mgr, frames, rec, respPub, connA, []string{protocol.CapabilityInteractive})

	// Only the noise_resp is ever emitted; it was recorded synchronously during
	// the handshake (openModalConn already found it), so the snapshot is final.
	waitForEnvelopes(t, rec, 1)

	if msgs := noiseMsgsForConn(t, rec, connA); len(msgs) != 0 {
		t.Errorf("conn %q: got %d noise_msg, want 0 (nothing outstanding)", connA, len(msgs))
	}
}

// TestV2Session_QueueReconcile_NilSeamInert keeps the pre-#878 posture: a nil
// OutstandingQueues seam makes reconcile a no-op and leaves the session open
// (foreground / unwired byte-stability).
func TestV2Session_QueueReconcile_NilSeamInert(t *testing.T) {
	t.Parallel()

	const connA = "c-v2-A"

	respPriv, respPub := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)
	frames := make(chan protocol.RoutingEnvelope, 8)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   rec.outbound,
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
		// OutstandingQueues left nil.
	})
	t.Cleanup(stop)

	openModalConn(t, mgr, frames, rec, respPub, connA, []string{protocol.CapabilityInteractive})

	// The nil guard short-circuits before any Push; only the noise_resp exists.
	waitForEnvelopes(t, rec, 1)

	if msgs := noiseMsgsForConn(t, rec, connA); len(msgs) != 0 {
		t.Errorf("conn %q: got %d noise_msg, want 0 (nil seam)", connA, len(msgs))
	}
	// The session is still enumerable-open — the nil seam changed nothing.
	waitConnOpen(t, mgr, connA)
}

// TestV2Session_QueueReconcile_ContentFreeLogging pins AC5: the untrusted queued
// text is NEVER logged across the interactive-open reconcile path. The success
// path emits no log line for the reconcile at all; the two error branches log
// only content-free discriminants (event, conn_id, conversation_id) by
// construction. Captured at Debug (the lowest level) so any leak would surface;
// the handshake-accept line proves the capture is live (non-vacuous).
func TestV2Session_QueueReconcile_ContentFreeLogging(t *testing.T) {
	t.Parallel()

	const (
		connA  = "c-v2-A"
		convID = "conv-reconcile-secret"
		secret = "SUPER-SECRET-QUEUED-CONTENT-do-not-log"
	)
	payload := protocol.QueueStatePayload{
		ConversationID: convID,
		Queued:         []protocol.QueuedItem{{QueuedMsgID: 1, Text: secret, TS: sampleQueueTS(1)}},
	}

	logBuf := &lockedBuffer{}
	logger := slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	respPriv, respPub := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)
	frames := make(chan protocol.RoutingEnvelope, 8)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:            frames,
		Outbound:          rec.outbound,
		StaticPriv:        respPriv,
		Devices:           reg,
		ServerID:          v2TestServerID,
		Logger:            logger,
		OutstandingQueues: func() []protocol.QueueStatePayload { return []protocol.QueueStatePayload{payload} },
	})
	t.Cleanup(stop)

	_, aRecv := openModalConn(t, mgr, frames, rec, respPub, connA, []string{protocol.CapabilityInteractive})

	// noise_resp + the one reconciled queue_state: once both are recorded the open
	// path (and any log line it would emit) has run.
	waitForEnvelopes(t, rec, 2)
	if got := reconciledQueues(t, rec, connA, aRecv); len(got) != 1 || got[convID].Queued[0].Text != secret {
		t.Fatalf("conn %q: reconcile did not re-send the secret payload (got %+v)", connA, got)
	}

	logs := logBuf.String()
	if !strings.Contains(logs, "handshake") {
		t.Fatalf("log capture appears inert (no handshake line); cannot trust the never-log assertion. logs = %q", logs)
	}
	if strings.Contains(logs, secret) {
		t.Errorf("queued text leaked into a log record; logs = %q", logs)
	}
}
