package relay

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// --- #1979 connect-time question-batch reconcile (the fourth Mode B instance,
// after the #877 modal, #878 queue and #1863 model-list sets) ---
//
// reconcileQuestions's defensive marshal branch is deliberately NOT covered: no
// value of a QuestionShownPayload can fail to marshal (it is two strings plus
// []Question, Question is two strings plus []QuestionOption plus a bool,
// QuestionOption is two strings, and both custom MarshalJSONs delegate to
// json.Marshal over those closed types), so there is no fixture that reddens it.
// None of the three analogue test files contains a marshal test for the same
// reason — the gap is a decision, not an oversight.
//
// Nor is the len(outstanding) == 0 check a coverage gap: with a zero-length slice
// the loop body never executes, so deleting it is an EQUIVALENT mutant. The "zero
// payloads" row below pins the contract (a real producer returning nil must not
// error), not a reachable branch.

// sampleQuestionPayload builds a fully-populated QuestionShownPayload for convID
// under batchID, with one Question per header given and two options under each,
// standing in for one batch the OutstandingQuestions seam enumerates. Every field
// is distinct and non-zero so a mapping that dropped or crossed one cannot survive
// the reflect.DeepEqual round-trip check; MultiSelect is set on the first question
// only, so the bool is carried rather than defaulted. Two options per question
// keeps the NESTED level non-trivial — AC1 asks for both nesting levels unchanged,
// and a single option would let an Options-dropping bug through on arity alone.
// QuestionShownPayload has no time.Time field, so DeepEqual is safe here and
// #878's equalQueued has no counterpart.
func sampleQuestionPayload(convID, batchID string, headers ...string) protocol.QuestionShownPayload {
	questions := make([]protocol.Question, 0, len(headers))
	for i, h := range headers {
		questions = append(questions, protocol.Question{
			Text:   fmt.Sprintf("Which %s do you want?", h),
			Header: h,
			Options: []protocol.QuestionOption{
				{Label: fmt.Sprintf("%s-first", h), Description: fmt.Sprintf("the first %s option", h)},
				{Label: fmt.Sprintf("%s-second", h), Description: fmt.Sprintf("the second %s option", h)},
			},
			MultiSelect: i == 0,
		})
	}
	return protocol.QuestionShownPayload{
		ConversationID:  convID,
		QuestionBatchID: batchID,
		Questions:       questions,
	}
}

// reconciledQuestions decrypts every noise_msg addressed to connID under recv (in
// recorded, hence AEAD-nonce, order) and returns each question_shown keyed by
// question_batch_id — never by position. Keying is load-bearing twice: Snapshot
// walks a map and its doc block states the order is unspecified, so an assertion
// coupled to position becomes a flake once #1980 wires the real read; and two
// batches can share one conversation, which a conversation-keyed map would
// silently collapse.
//
// In these tests the reconcile is the only outbound app traffic, so a noise_msg of
// any other Type is a bug — that check is where "no turn opened" is asserted, since
// a turn-boundary frame would have to show up here as a non-question_shown
// envelope. The nil-EventID check is the other half of AC3: it keeps the frame out
// of the #647 turn-event replay ring and makes forwardEnvelope's last_event_id
// dedup inert for it. A repeated question_batch_id is a fan-out/dup bug. Decrypt
// each conn's frames exactly once per test — Decrypt advances the recv nonce.
func reconciledQuestions(t *testing.T, rec *v2Recorder, connID string, recv *noise.CipherState) map[string]protocol.QuestionShownPayload {
	t.Helper()
	out := make(map[string]protocol.QuestionShownPayload)
	for _, env := range noiseMsgsForConn(t, rec, connID) {
		inner := decryptAppFrame(t, env, recv)
		if inner.Type != protocol.TypeQuestionShown {
			t.Fatalf("conn %q: noise_msg Type = %q, want %q", connID, inner.Type, protocol.TypeQuestionShown)
		}
		if inner.EventID != nil {
			t.Errorf("conn %q: reconciled question_shown carries event_id %d, want none", connID, *inner.EventID)
		}
		var p protocol.QuestionShownPayload
		if err := json.Unmarshal(inner.Payload, &p); err != nil {
			t.Fatalf("conn %q: decode question_shown payload: %v", connID, err)
		}
		if _, dup := out[p.QuestionBatchID]; dup {
			t.Errorf("conn %q: question_batch_id %q re-sent more than once", connID, p.QuestionBatchID)
		}
		out[p.QuestionBatchID] = p
	}
	return out
}

// TestV2Session_QuestionReconcile_Delivery sends exactly the seam's payloads to a
// freshly interactive-open conn, unchanged (AC1). Each row's payloads are compared
// by reflect.DeepEqual against the seam's own values, keyed by question_batch_id,
// so the match is order-independent and covers conversation_id plus both nesting
// levels — each Question's Text / Header / MultiSelect and each QuestionOption's
// Label / Description.
func TestV2Session_QuestionReconcile_Delivery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		payloads []protocol.QuestionShownPayload
	}{
		{
			name:     "one outstanding batch",
			payloads: []protocol.QuestionShownPayload{sampleQuestionPayload("conv-q-one", "batch-one", "framework")},
		},
		{
			// Two batches in ONE conversation: a reconcile keyed on conversation_id
			// rather than on question_batch_id would collapse these into one frame,
			// and no single-batch row can catch that.
			name: "two batches in one conversation",
			payloads: []protocol.QuestionShownPayload{
				sampleQuestionPayload("conv-q-shared", "batch-shared-1", "framework"),
				sampleQuestionPayload("conv-q-shared", "batch-shared-2", "database", "cache"),
			},
		},
		{
			name: "two conversations",
			payloads: []protocol.QuestionShownPayload{
				sampleQuestionPayload("conv-q-1", "batch-conv1", "framework"),
				sampleQuestionPayload("conv-q-2", "batch-conv2", "database", "cache"),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			const connA = "c-v2-A"

			respPriv, respPub := genV2Keypair(t)
			reg := v2PairedRegistry(t, v2TestToken)
			frames := make(chan protocol.RoutingEnvelope, 8)
			rec := &v2Recorder{}
			mgr, stop := startManager(t, V2SessionConfig{
				Frames:               frames,
				Outbound:             rec.outbound,
				StaticPriv:           respPriv,
				Devices:              reg,
				ServerID:             v2TestServerID,
				Logger:               silentLogger(),
				OutstandingQuestions: func() []protocol.QuestionShownPayload { return tt.payloads },
			})
			t.Cleanup(stop)

			_, aRecv := openModalConn(t, mgr, frames, rec, respPub, connA, []string{protocol.CapabilityInteractive})

			// noise_resp + one question_shown per outstanding batch; exactly that many
			// are ever emitted here, so the snapshot is final once they are recorded.
			waitForEnvelopes(t, rec, 1+len(tt.payloads))

			got := reconciledQuestions(t, rec, connA, aRecv)
			if len(got) != len(tt.payloads) {
				t.Fatalf("conn %q: got %d question_shown, want %d", connA, len(got), len(tt.payloads))
			}
			for _, want := range tt.payloads {
				if !reflect.DeepEqual(got[want.QuestionBatchID], want) {
					t.Errorf("conn %q: batch %q = %+v, want %+v", connA, want.QuestionBatchID, got[want.QuestionBatchID], want)
				}
			}
		})
	}
}

// TestV2Session_QuestionReconcile_UnicastAndPureRead proves three things at once,
// because all three are properties of the same second open:
//
//   - AC2's "a second open interactive conn receives nothing from this conn's
//     reconcile": with A already open, opening B delivers to B only — A receives no
//     second frame.
//   - AC3's "running it twice over the same outstanding set sends the same frames
//     both times": B's decoded set is compared to A's.
//   - AC3's "changes nothing it read": the seam's backing payloads are compared
//     against a pristine copy taken before either open, at both nesting levels.
func TestV2Session_QuestionReconcile_UnicastAndPureRead(t *testing.T) {
	t.Parallel()

	const (
		connA  = "c-v2-A" // interactive; opened first
		connB  = "c-v2-B" // interactive; opened second
		convID = "conv-q-unicast"
	)

	outstanding := []protocol.QuestionShownPayload{
		sampleQuestionPayload(convID, "batch-unicast-1", "framework"),
		sampleQuestionPayload(convID, "batch-unicast-2", "database", "cache"),
	}
	// Deep pristine copy, taken BEFORE any open. sampleQuestionPayload is
	// deterministic, so rebuilding it is a copy that shares no backing array with
	// the slice the seam hands out — a reconcile that mutated a Question or an
	// Option in place would diverge from this and from nothing else.
	pristine := []protocol.QuestionShownPayload{
		sampleQuestionPayload(convID, "batch-unicast-1", "framework"),
		sampleQuestionPayload(convID, "batch-unicast-2", "database", "cache"),
	}

	respPriv, respPub := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)
	frames := make(chan protocol.RoutingEnvelope, 8)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:               frames,
		Outbound:             rec.outbound,
		StaticPriv:           respPriv,
		Devices:              reg,
		ServerID:             v2TestServerID,
		Logger:               silentLogger(),
		OutstandingQuestions: func() []protocol.QuestionShownPayload { return outstanding },
	})
	t.Cleanup(stop)

	_, aRecv := openModalConn(t, mgr, frames, rec, respPub, connA, []string{protocol.CapabilityInteractive})
	_, bRecv := openModalConn(t, mgr, frames, rec, respPub, connB, []string{protocol.CapabilityInteractive})

	// resp(A) + 2×question_shown(A) + resp(B) + 2×question_shown(B) = 6 in the
	// correct (unicast) case. A fan-out on B's open would push more (a second pair
	// to A), which the count plus the helper's per-conn dup check would catch.
	waitForEnvelopes(t, rec, 6)

	gotA := reconciledQuestions(t, rec, connA, aRecv)
	gotB := reconciledQuestions(t, rec, connB, bRecv)
	if len(gotA) != len(outstanding) {
		t.Errorf("conn %q (opened first): got %d question_shown, want exactly %d (its own open, none from B's open)", connA, len(gotA), len(outstanding))
	}
	if !reflect.DeepEqual(gotA, gotB) {
		t.Errorf("second open sent a different set: conn %q = %+v, conn %q = %+v", connA, gotA, connB, gotB)
	}
	if !reflect.DeepEqual(outstanding, pristine) {
		t.Errorf("reconcile mutated what it read: seam payloads = %+v, want %+v", outstanding, pristine)
	}
}

// TestV2Session_QuestionReconcile_NoFrame pins AC2's three no-send arms: no frame
// AND no error — each row asserts zero noise_msg for the conn under test and that
// the session is still enumerable-open, so behaviour is byte-identical to the
// pre-#1979 posture.
func TestV2Session_QuestionReconcile_NoFrame(t *testing.T) {
	t.Parallel()

	const (
		connA   = "c-v2-A"   // the conn under test; must receive nothing
		connCtl = "c-v2-CTL" // interactive control conn, capability row only
		convID  = "conv-q-noframe"
	)
	nonEmptySeam := func() []protocol.QuestionShownPayload {
		return []protocol.QuestionShownPayload{sampleQuestionPayload(convID, "batch-noframe", "framework")}
	}

	tests := []struct {
		name string
		seam func() []protocol.QuestionShownPayload
		caps []string
		// controlConn opens a second, interactive conn AFTER the conn under test.
		// The capability row needs it: without a conn that DOES receive a
		// question_shown, A's zero is indistinguishable from an empty seam and a
		// dropped !s.interactive guard survives green.
		controlConn bool
	}{
		{
			name: "nil seam",
			seam: nil, // unwired / foreground
			caps: []string{protocol.CapabilityInteractive},
		},
		{
			name: "zero payloads",
			seam: func() []protocol.QuestionShownPayload { return nil },
			caps: []string{protocol.CapabilityInteractive},
		},
		{
			name:        "capability not negotiated",
			seam:        nonEmptySeam,
			caps:        nil,
			controlConn: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			respPriv, respPub := genV2Keypair(t)
			reg := v2PairedRegistry(t, v2TestToken)
			frames := make(chan protocol.RoutingEnvelope, 8)
			rec := &v2Recorder{}
			mgr, stop := startManager(t, V2SessionConfig{
				Frames:               frames,
				Outbound:             rec.outbound,
				StaticPriv:           respPriv,
				Devices:              reg,
				ServerID:             v2TestServerID,
				Logger:               silentLogger(),
				OutstandingQuestions: tt.seam,
			})
			t.Cleanup(stop)

			// The conn under test opens first and fully settles (its reconcile runs
			// synchronously in handleNoiseInit and pushes nothing) before the control
			// conn's multi-step handshake pumps the Run loop.
			openModalConn(t, mgr, frames, rec, respPub, connA, tt.caps)

			wantEnvelopes := 1 // resp(A) only
			var ctlRecv *noise.CipherState
			if tt.controlConn {
				_, ctlRecv = openModalConn(t, mgr, frames, rec, respPub, connCtl, []string{protocol.CapabilityInteractive})
				wantEnvelopes = 3 // + resp(CTL) + question_shown(CTL)
			}
			waitForEnvelopes(t, rec, wantEnvelopes)

			if tt.controlConn {
				if got := reconciledQuestions(t, rec, connCtl, ctlRecv); len(got) != 1 {
					t.Fatalf("interactive control conn %q: got %d question_shown, want 1 (the seam must be enumerating a batch for A's zero to mean the gate)", connCtl, len(got))
				}
			}
			if msgs := noiseMsgsForConn(t, rec, connA); len(msgs) != 0 {
				t.Errorf("conn %q: got %d noise_msg, want 0", connA, len(msgs))
			}
			// Still enumerable-open — no frame AND no error.
			waitConnOpen(t, mgr, connA)
		})
	}
}

// TestV2Session_QuestionReconcile_ContentFreeLogging pins AC4: no question text,
// header, option label or option description reaches a log line on this path at any
// level. The success path emits no log line for the reconcile at all; the two error
// branches carry content-free discriminants only, by construction. Captured at
// Debug (the lowest level) so any leak would surface; the handshake-accept line
// proves the capture is live (non-vacuous). Four distinct sentinels rather than
// one: the fields are separate struct members at two nesting levels, and a branch
// echoing just one of them must not pass.
func TestV2Session_QuestionReconcile_ContentFreeLogging(t *testing.T) {
	t.Parallel()

	const (
		connA       = "c-v2-A"
		convID      = "conv-q-secret"
		batchID     = "batch-q-secret"
		text        = "SENTINEL-QUESTION-TEXT-do-not-log"
		header      = "SENTINEL-QUESTION-HEADER-do-not-log"
		label       = "SENTINEL-OPTION-LABEL-do-not-log"
		description = "SENTINEL-OPTION-DESCRIPTION-do-not-log"
	)
	payload := protocol.QuestionShownPayload{
		ConversationID:  convID,
		QuestionBatchID: batchID,
		Questions: []protocol.Question{{
			Text:    text,
			Header:  header,
			Options: []protocol.QuestionOption{{Label: label, Description: description}},
		}},
	}

	logBuf := &lockedBuffer{}
	logger := slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	respPriv, respPub := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)
	frames := make(chan protocol.RoutingEnvelope, 8)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:               frames,
		Outbound:             rec.outbound,
		StaticPriv:           respPriv,
		Devices:              reg,
		ServerID:             v2TestServerID,
		Logger:               logger,
		OutstandingQuestions: func() []protocol.QuestionShownPayload { return []protocol.QuestionShownPayload{payload} },
	})
	t.Cleanup(stop)

	_, aRecv := openModalConn(t, mgr, frames, rec, respPub, connA, []string{protocol.CapabilityInteractive})

	// noise_resp + the one reconciled question_shown: once both are recorded the
	// open path (and any log line it would emit) has run.
	waitForEnvelopes(t, rec, 2)
	if got := reconciledQuestions(t, rec, connA, aRecv); !reflect.DeepEqual(got[batchID], payload) {
		t.Fatalf("conn %q: reconcile did not send the sentinel payload (got %+v)", connA, got)
	}

	logs := logBuf.String()
	if !strings.Contains(logs, "handshake") {
		t.Fatalf("log capture appears inert (no handshake line); cannot trust the never-log assertion. logs = %q", logs)
	}
	for _, sentinel := range []string{text, header, label, description} {
		if strings.Contains(logs, sentinel) {
			t.Errorf("question content %q leaked into a log record; logs = %q", sentinel, logs)
		}
	}
}
