package relay

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// --- #2830 connect-time reply-suggestion reconcile and revision guard ---
//
// The marshal arm and the non-ctx push arm are unreachable by fixture for the
// reasons v2session_rosterreconcile_test.go's header gives.

// suggestionFrame is one decrypted reply_suggestion: the decoded payload and the
// raw payload bytes, so a test can see a literal null rather than a decoded nil.
type suggestionFrame struct {
	p   protocol.ReplySuggestionPayload
	raw string
}

// suggestionConn tracks one conn's position in its own decrypted stream, so a
// test can read the frames delivered since its last look. A CipherState
// decrypts in nonce order, so every frame is decrypted exactly once, in order.
type suggestionConn struct {
	id   string
	recv *noise.CipherState
	seen int
}

// next decrypts the reply_suggestion frames addressed to c since the last call.
// The reconcile and the test's own pushes are the only outbound app traffic, so
// any other Type is a bug, as is a non-nil EventID (AC2).
func (c *suggestionConn) next(t *testing.T, rec *v2Recorder) []suggestionFrame {
	t.Helper()
	msgs := noiseMsgsForConn(t, rec, c.id)
	var out []suggestionFrame
	for _, env := range msgs[c.seen:] {
		inner := decryptAppFrame(t, env, c.recv)
		if inner.Type != protocol.TypeReplySuggestion {
			t.Fatalf("conn %q: noise_msg Type = %q, want %q", c.id, inner.Type, protocol.TypeReplySuggestion)
		}
		if inner.EventID != nil {
			t.Errorf("conn %q: reply_suggestion carries event_id %d, want none", c.id, *inner.EventID)
		}
		var p protocol.ReplySuggestionPayload
		if err := json.Unmarshal(inner.Payload, &p); err != nil {
			t.Fatalf("conn %q: decode reply_suggestion payload: %v", c.id, err)
		}
		out = append(out, suggestionFrame{p: p, raw: string(inner.Payload)})
	}
	c.seen = len(msgs)
	return out
}

func suggestion(conv string, rev uint64, text *string) protocol.ReplySuggestionPayload {
	return protocol.ReplySuggestionPayload{ConversationID: conv, SessionID: "sess-" + conv, Revision: rev, SuggestedReply: text}
}

// pushSuggestion pushes p to connID as the producer's live fan-out will (#2831).
// It reports with Errorf, not Fatalf, because the guard test calls it from inside
// the seam, on the manager's Run goroutine.
func pushSuggestion(t *testing.T, mgr *V2SessionManager, connID string, p protocol.ReplySuggestionPayload) {
	t.Helper()
	payload, err := json.Marshal(p)
	if err != nil {
		t.Errorf("marshal suggestion: %v", err)
		return
	}
	env := protocol.Envelope{ID: 1, Type: protocol.TypeReplySuggestion, TS: time.Now().UTC(), Payload: payload}
	if err := mgr.Push(context.Background(), connID, env); err != nil {
		t.Errorf("Push(%s): %v", connID, err)
	}
}

// wantSuggestions compares a conn's delivered frames, in order, by conversation,
// revision and suggestion (nil meaning the clear).
func wantSuggestions(t *testing.T, connID string, got []suggestionFrame, want []protocol.ReplySuggestionPayload) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("conn %q: got %d reply_suggestion %+v, want %d", connID, len(got), got, len(want))
	}
	for i, w := range want {
		g := got[i].p
		if g.ConversationID != w.ConversationID || g.SessionID != w.SessionID || g.Revision != w.Revision {
			t.Errorf("conn %q frame %d: got %s@%d (session %q), want %s@%d (session %q)",
				connID, i, g.ConversationID, g.Revision, g.SessionID, w.ConversationID, w.Revision, w.SessionID)
		}
		switch {
		case w.SuggestedReply == nil:
			if g.SuggestedReply != nil || !strings.Contains(got[i].raw, `"suggested_reply":null`) {
				t.Errorf("conn %q frame %d: payload %s, want a literal \"suggested_reply\":null", connID, i, got[i].raw)
			}
		case g.SuggestedReply == nil || *g.SuggestedReply != *w.SuggestedReply:
			t.Errorf("conn %q frame %d: suggested_reply = %v, want %q", connID, i, g.SuggestedReply, *w.SuggestedReply)
		}
	}
}

// TestV2Session_ReplySuggestionReconcile_Delivery sends exactly the seam's
// payloads, in order, to a freshly interactive-open conn, a cleared conversation
// as a literal null and every frame without an event_id (AC1, AC2).
func TestV2Session_ReplySuggestionReconcile_Delivery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		payloads []protocol.ReplySuggestionPayload
	}{
		{"suggestion", []protocol.ReplySuggestionPayload{suggestion("conv-s-1", 3, strPtr("Yes, go ahead"))}},
		{"cleared", []protocol.ReplySuggestionPayload{suggestion("conv-s-1", 4, nil)}},
		{"two conversations, one cleared", []protocol.ReplySuggestionPayload{
			suggestion("conv-s-1", 2, nil),
			suggestion("conv-s-2", 7, strPtr("Run the tests")),
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			respPriv, respPub := genV2Keypair(t)
			frames := make(chan protocol.RoutingEnvelope, 8)
			rec := &v2Recorder{}
			mgr, stop := startManager(t, V2SessionConfig{
				Frames:           frames,
				Outbound:         rec.outbound,
				StaticPriv:       respPriv,
				Devices:          v2PairedRegistry(t, v2TestToken),
				ServerID:         v2TestServerID,
				Logger:           silentLogger(),
				ReplySuggestions: func() []protocol.ReplySuggestionPayload { return tt.payloads },
			})
			t.Cleanup(stop)

			_, aRecv := openModalConn(t, mgr, frames, rec, respPub, "c-v2-A", []string{protocol.CapabilityInteractive})
			waitForEnvelopes(t, rec, 1+len(tt.payloads))

			a := &suggestionConn{id: "c-v2-A", recv: aRecv}
			wantSuggestions(t, a.id, a.next(t, rec), tt.payloads)
		})
	}
}

// TestV2Session_ReplySuggestionReconcile_NoFrame: no frame, no log record and an
// open conn for an unwired seam, for no suggestion state, and for a conn that did
// not negotiate the interactive capability (AC1).
func TestV2Session_ReplySuggestionReconcile_NoFrame(t *testing.T) {
	t.Parallel()

	const eventScope = "v2.replysuggestion."
	some := func() []protocol.ReplySuggestionPayload {
		return []protocol.ReplySuggestionPayload{suggestion("conv-s-noframe", 1, strPtr("hello"))}
	}

	tests := []struct {
		name string
		seam func() []protocol.ReplySuggestionPayload
		caps []string
		// controlConn opens an interactive conn after A so the capability row's
		// zero is measured against a seam that does produce a frame.
		controlConn bool
	}{
		{name: "nil seam", seam: nil, caps: []string{protocol.CapabilityInteractive}},
		{name: "no suggestion state", seam: func() []protocol.ReplySuggestionPayload { return nil }, caps: []string{protocol.CapabilityInteractive}},
		{name: "capability not negotiated", seam: some, caps: nil, controlConn: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			logBuf := &lockedBuffer{}
			logger := slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

			respPriv, respPub := genV2Keypair(t)
			frames := make(chan protocol.RoutingEnvelope, 8)
			rec := &v2Recorder{}
			mgr, stop := startManager(t, V2SessionConfig{
				Frames:           frames,
				Outbound:         rec.outbound,
				StaticPriv:       respPriv,
				Devices:          v2PairedRegistry(t, v2TestToken),
				ServerID:         v2TestServerID,
				Logger:           logger,
				ReplySuggestions: tt.seam,
			})
			t.Cleanup(stop)

			openModalConn(t, mgr, frames, rec, respPub, "c-v2-A", tt.caps)

			wantEnvelopes := 1 // resp(A)
			var ctl *suggestionConn
			if tt.controlConn {
				_, ctlRecv := openModalConn(t, mgr, frames, rec, respPub, "c-v2-CTL", []string{protocol.CapabilityInteractive})
				ctl = &suggestionConn{id: "c-v2-CTL", recv: ctlRecv}
				wantEnvelopes = 3 // + resp(CTL) + suggestion(CTL)
			}
			waitForEnvelopes(t, rec, wantEnvelopes)

			if ctl != nil {
				if got := ctl.next(t, rec); len(got) != 1 {
					t.Fatalf("interactive control conn: got %d reply_suggestion, want 1", len(got))
				}
			}
			if msgs := noiseMsgsForConn(t, rec, "c-v2-A"); len(msgs) != 0 {
				t.Errorf("conn c-v2-A: got %d noise_msg, want 0", len(msgs))
			}
			logs := logBuf.String()
			if !strings.Contains(logs, "handshake") {
				t.Fatalf("log capture appears inert (no handshake line); logs = %q", logs)
			}
			if strings.Contains(logs, eventScope) {
				t.Errorf("reconcile wrote a log record on a no-frame path; logs = %q", logs)
			}
			waitConnOpen(t, mgr, "c-v2-A")
		})
	}
}

// TestV2Session_ReplySuggestionReconcile_ReconnectGetsCurrentState: a conn opened
// after the state moved receives the seam's current state only — not the frame
// an earlier conn was given (AC2).
func TestV2Session_ReplySuggestionReconcile_ReconnectGetsCurrentState(t *testing.T) {
	t.Parallel()

	const conv = "conv-s-reconnect"
	var mu sync.Mutex
	state := []protocol.ReplySuggestionPayload{suggestion(conv, 1, strPtr("Ship it"))}

	respPriv, respPub := genV2Keypair(t)
	frames := make(chan protocol.RoutingEnvelope, 8)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   rec.outbound,
		StaticPriv: respPriv,
		Devices:    v2PairedRegistry(t, v2TestToken),
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
		ReplySuggestions: func() []protocol.ReplySuggestionPayload {
			mu.Lock()
			defer mu.Unlock()
			return state
		},
	})
	t.Cleanup(stop)

	_, aRecv := openModalConn(t, mgr, frames, rec, respPub, "c-v2-A", []string{protocol.CapabilityInteractive})
	waitForEnvelopes(t, rec, 2)

	mu.Lock()
	state = []protocol.ReplySuggestionPayload{suggestion(conv, 2, nil)} // another device cleared it
	mu.Unlock()

	_, bRecv := openModalConn(t, mgr, frames, rec, respPub, "c-v2-B", []string{protocol.CapabilityInteractive})
	waitForEnvelopes(t, rec, 4)

	a := &suggestionConn{id: "c-v2-A", recv: aRecv}
	b := &suggestionConn{id: "c-v2-B", recv: bRecv}
	wantSuggestions(t, a.id, a.next(t, rec), []protocol.ReplySuggestionPayload{suggestion(conv, 1, strPtr("Ship it"))})
	wantSuggestions(t, b.id, b.next(t, rec), []protocol.ReplySuggestionPayload{suggestion(conv, 2, nil)})
}

// TestV2Session_ReplySuggestion_RevisionGuard forces the race the guard closes
// (AC3): the snapshot is read at revision 5 and, from inside the seam call, a
// live clear at revision 6 is pushed to the same conn, so it is queued ahead of
// the snapshot frame that then drains behind it. Two conns and two conversations
// show the watermark is per conn and per conversation.
func TestV2Session_ReplySuggestion_RevisionGuard(t *testing.T) {
	t.Parallel()

	const (
		connA = "c-v2-A"
		connB = "c-v2-B"
		convX = "conv-s-x"
		convY = "conv-s-y"
	)
	var (
		x5 = suggestion(convX, 5, strPtr("Looks good"))
		x6 = suggestion(convX, 6, nil)
		y3 = suggestion(convY, 3, strPtr("Try again"))
		y4 = suggestion(convY, 4, strPtr("Try once more"))
	)

	respPriv, respPub := genV2Keypair(t)
	frames := make(chan protocol.RoutingEnvelope, 8)
	rec := &v2Recorder{}
	var mgr *V2SessionManager
	calls := 0 // Run-goroutine only: the seam is called there, once per open.
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   rec.outbound,
		StaticPriv: respPriv,
		Devices:    v2PairedRegistry(t, v2TestToken),
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
		ReplySuggestions: func() []protocol.ReplySuggestionPayload {
			calls++
			if calls == 1 {
				// A's open: the producer publishes the clear between this read
				// and the snapshot's drain.
				pushSuggestion(t, mgr, connA, x6)
			}
			// B's open returns the same stale snapshot with no live push, so B's
			// delivery of X@5 proves A's watermark is not shared.
			return []protocol.ReplySuggestionPayload{x5, y3}
		},
	})
	t.Cleanup(stop)

	_, aRecv := openModalConn(t, mgr, frames, rec, respPub, connA, []string{protocol.CapabilityInteractive})
	// resp(A) + X@6 + Y@3; X@5 drains between them and is dropped, and Y@3
	// draining last means it has already been judged.
	waitForEnvelopes(t, rec, 3)
	a := &suggestionConn{id: connA, recv: aRecv}
	wantSuggestions(t, connA, a.next(t, rec), []protocol.ReplySuggestionPayload{x6, y3})

	_, bRecv := openModalConn(t, mgr, frames, rec, respPub, connB, []string{protocol.CapabilityInteractive})
	waitForEnvelopes(t, rec, 6)
	b := &suggestionConn{id: connB, recv: bRecv}
	wantSuggestions(t, connB, b.next(t, rec), []protocol.ReplySuggestionPayload{x5, y3})

	// Live frames: an equal revision is dropped on both conns; a newer one is
	// delivered; each conn's later frame proves the dropped one was judged.
	pushSuggestion(t, mgr, connA, y3)
	pushSuggestion(t, mgr, connB, y3)
	pushSuggestion(t, mgr, connA, x5) // below A's X@6
	pushSuggestion(t, mgr, connA, y4)
	pushSuggestion(t, mgr, connB, x6)
	waitForEnvelopes(t, rec, 8)

	wantSuggestions(t, connA, a.next(t, rec), []protocol.ReplySuggestionPayload{y4})
	wantSuggestions(t, connB, b.next(t, rec), []protocol.ReplySuggestionPayload{x6})
}

// TestV2Session_ReplySuggestion_CodexWithheld: withheldFromConn withholds a Codex
// conversation's suggestion, reconciled and live, from a conn without
// multi_agent, and delivers it to one with it (AC4). No log line carries the
// suggestion text.
func TestV2Session_ReplySuggestion_CodexWithheld(t *testing.T) {
	t.Parallel()

	const (
		connOld = "c-v2-OLD"
		connCap = "c-v2-CAP"
	)
	var (
		codex1  = suggestion(gateCodexConv, 1, strPtr("codex-secret-suggestion"))
		claude1 = suggestion(gateClaudeConv, 1, strPtr("claude-secret-suggestion"))
		codex2  = suggestion(gateCodexConv, 2, strPtr("codex-secret-live"))
		claude2 = suggestion(gateClaudeConv, 2, nil)
	)

	logBuf := &lockedBuffer{}
	logger := slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	respPriv, respPub := genV2Keypair(t)
	frames := make(chan protocol.RoutingEnvelope, 8)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:            frames,
		Outbound:          rec.outbound,
		StaticPriv:        respPriv,
		Devices:           v2PairedRegistry(t, v2TestToken),
		ServerID:          v2TestServerID,
		Logger:            logger,
		CodexConversation: gateCodexSeam,
		ReplySuggestions: func() []protocol.ReplySuggestionPayload {
			return []protocol.ReplySuggestionPayload{codex1, claude1}
		},
	})
	t.Cleanup(stop)

	_, oldRecv := openModalConn(t, mgr, frames, rec, respPub, connOld, gateOldCaps)
	_, capRecv := openModalConn(t, mgr, frames, rec, respPub, connCap, gateCapableCaps)
	waitForEnvelopes(t, rec, 5) // resp(OLD) + claude1 + resp(CAP) + codex1 + claude1

	// The withheld codex frame is pushed ahead of claude2, so seeing claude2 on
	// the old conn means codex2 was judged there.
	for _, id := range []string{connOld, connCap} {
		pushSuggestion(t, mgr, id, codex2)
		pushSuggestion(t, mgr, id, claude2)
	}
	waitForEnvelopes(t, rec, 8)

	old := &suggestionConn{id: connOld, recv: oldRecv}
	capable := &suggestionConn{id: connCap, recv: capRecv}
	wantSuggestions(t, connOld, old.next(t, rec), []protocol.ReplySuggestionPayload{claude1, claude2})
	wantSuggestions(t, connCap, capable.next(t, rec), []protocol.ReplySuggestionPayload{codex1, claude1, codex2, claude2})

	logs := logBuf.String()
	if !strings.Contains(logs, "v2.push.withheld_multi_agent") {
		t.Fatalf("log capture appears inert (no withhold line); logs = %q", logs)
	}
	if strings.Contains(logs, "secret") {
		t.Errorf("a log line carries suggestion text; logs = %q", logs)
	}
}
