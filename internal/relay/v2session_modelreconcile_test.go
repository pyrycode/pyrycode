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

// --- #1863 connect-time model-list reconcile (the third Mode B instance, after
// the #877 modal and #878 queue sets) ---
//
// reconcileModelLists's defensive marshal branch is deliberately NOT covered: no
// value of a ModelListPayload can fail to marshal (it is string + []ModelOption +
// int, ModelOption is three strings, two []string and a bool, and both custom
// MarshalJSONs delegate to json.Marshal over those closed types), so there is no
// fixture that reddens it. Neither analogue test file contains a marshal test for
// the same reason — the gap is a decision, not an oversight.

// sampleModelListPayload builds a fully-populated ModelListPayload for convID with
// one ModelOption per value, standing in for one entry the RetainedModelLists seam
// enumerates. Every field is distinct and non-zero so a mapping that dropped or
// crossed one cannot survive the reflect.DeepEqual round-trip check;
// SupportsAutoMode is set on the first entry only, so the bool is carried rather
// than defaulted. ModelListPayload has no time.Time field, so DeepEqual is safe
// here and #878's equalQueued has no counterpart.
func sampleModelListPayload(convID string, values ...string) protocol.ModelListPayload {
	models := make([]protocol.ModelOption, 0, len(values))
	for i, v := range values {
		models = append(models, protocol.ModelOption{
			ResolvedModel:    fmt.Sprintf("%s-20260101", v),
			Value:            v,
			DisplayName:      fmt.Sprintf("Display %s", v),
			EffortLevels:     []string{"low", "high"},
			SupportsAutoMode: i == 0,
		})
	}
	return protocol.ModelListPayload{ConversationID: convID, Models: models}
}

// reconciledModelLists decrypts every noise_msg addressed to connID under recv (in
// recorded, hence AEAD-nonce, order) and returns each model_list keyed by
// conversation_id. In these tests the reconcile is the only outbound app traffic,
// so a noise_msg of any other Type is a bug — that check is where "no turn opened"
// is asserted, since a turn-boundary frame would have to show up here as a
// non-model_list envelope. A repeated conversation_id is a fan-out/dup bug.
// Decrypt each conn's frames exactly once per test — Decrypt advances the recv
// nonce.
func reconciledModelLists(t *testing.T, rec *v2Recorder, connID string, recv *noise.CipherState) map[string]protocol.ModelListPayload {
	t.Helper()
	out := make(map[string]protocol.ModelListPayload)
	for _, env := range noiseMsgsForConn(t, rec, connID) {
		inner := decryptAppFrame(t, env, recv)
		if inner.Type != protocol.TypeModelList {
			t.Fatalf("conn %q: noise_msg Type = %q, want %q", connID, inner.Type, protocol.TypeModelList)
		}
		var p protocol.ModelListPayload
		if err := json.Unmarshal(inner.Payload, &p); err != nil {
			t.Fatalf("conn %q: decode model_list payload: %v", connID, err)
		}
		if _, dup := out[p.ConversationID]; dup {
			t.Errorf("conn %q: conversation_id %q re-sent more than once", connID, p.ConversationID)
		}
		out[p.ConversationID] = p
	}
	return out
}

// TestV2Session_ModelListReconcile_Delivery sends exactly the seam's payloads to a
// freshly interactive-open conn, unchanged (AC1). Each row's payloads are compared
// by reflect.DeepEqual against the seam's own values, keyed by conversation_id, so
// the match is order-independent and covers Models, DroppedModels and each entry's
// EffortLevels / SupportsAutoMode / TruncatedFields.
func TestV2Session_ModelListReconcile_Delivery(t *testing.T) {
	t.Parallel()

	// A payload whose bound reporters are both non-zero: DroppedModels on the
	// aggregate and TruncatedFields on one entry. They are the frame's only record
	// that the producer cut something, so a mapping that silently zeroed them would
	// pass every other row.
	bounded := sampleModelListPayload("conv-modellist-bounded", "opus", "haiku")
	bounded.DroppedModels = 3
	bounded.Models[0].TruncatedFields = []string{"display_name", "effort_levels"}

	tests := []struct {
		name     string
		payloads []protocol.ModelListPayload
	}{
		{
			name:     "one retained list",
			payloads: []protocol.ModelListPayload{sampleModelListPayload("conv-modellist-one", "sonnet")},
		},
		{
			name: "two conversations",
			payloads: []protocol.ModelListPayload{
				sampleModelListPayload("conv-modellist-1", "sonnet"),
				sampleModelListPayload("conv-modellist-2", "opus", "haiku"),
			},
		},
		{
			name:     "dropped models and truncated fields survive",
			payloads: []protocol.ModelListPayload{bounded},
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
				Frames:             frames,
				Outbound:           rec.outbound,
				StaticPriv:         respPriv,
				Devices:            reg,
				ServerID:           v2TestServerID,
				Logger:             silentLogger(),
				RetainedModelLists: func() []protocol.ModelListPayload { return tt.payloads },
			})
			t.Cleanup(stop)

			_, aRecv := openModalConn(t, mgr, frames, rec, respPub, connA, []string{protocol.CapabilityInteractive})

			// noise_resp + one model_list per retained list; exactly that many are
			// ever emitted here, so the snapshot is final once they are recorded.
			waitForEnvelopes(t, rec, 1+len(tt.payloads))

			got := reconciledModelLists(t, rec, connA, aRecv)
			if len(got) != len(tt.payloads) {
				t.Fatalf("conn %q: got %d model_list, want %d", connA, len(got), len(tt.payloads))
			}
			for _, want := range tt.payloads {
				if !reflect.DeepEqual(got[want.ConversationID], want) {
					t.Errorf("conn %q: conv %q = %+v, want %+v", connA, want.ConversationID, got[want.ConversationID], want)
				}
			}
		})
	}
}

// TestV2Session_ModelListReconcile_UnicastOnlyOpeningConn proves the send is
// unicast to the just-opened conn, NOT a fan-out: with A already open, opening B
// delivers the model_list to B only — A receives no second one (AC1's "unicast to
// that conn only").
func TestV2Session_ModelListReconcile_UnicastOnlyOpeningConn(t *testing.T) {
	t.Parallel()

	const (
		connA  = "c-v2-A" // interactive; opened first
		connB  = "c-v2-B" // interactive; opened second
		convID = "conv-modellist-unicast"
	)

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
		RetainedModelLists: func() []protocol.ModelListPayload {
			return []protocol.ModelListPayload{sampleModelListPayload(convID, "sonnet")}
		},
	})
	t.Cleanup(stop)

	_, aRecv := openModalConn(t, mgr, frames, rec, respPub, connA, []string{protocol.CapabilityInteractive})
	_, bRecv := openModalConn(t, mgr, frames, rec, respPub, connB, []string{protocol.CapabilityInteractive})

	// resp(A) + model_list(A) + resp(B) + model_list(B) = 4 in the correct (unicast)
	// case. A fan-out on B's open would push a 5th (a second model_list to A), which
	// the count plus the helper's per-conn dup check below would catch.
	waitForEnvelopes(t, rec, 4)

	gotA := reconciledModelLists(t, rec, connA, aRecv)
	gotB := reconciledModelLists(t, rec, connB, bRecv)
	if len(gotA) != 1 {
		t.Errorf("conn %q (opened first): got %d model_list, want exactly 1 (its own open, none from B's open)", connA, len(gotA))
	}
	if len(gotB) != 1 {
		t.Errorf("conn %q (opened second): got %d model_list, want 1", connB, len(gotB))
	}
}

// TestV2Session_ModelListReconcile_NoFrame pins AC2's three arms: no frame AND no
// error — each row asserts zero noise_msg for the conn under test and that the
// session is still enumerable-open, so behaviour is byte-identical to the
// pre-#1863 posture.
func TestV2Session_ModelListReconcile_NoFrame(t *testing.T) {
	t.Parallel()

	const (
		connA   = "c-v2-A"   // the conn under test; must receive nothing
		connCtl = "c-v2-CTL" // interactive control conn, capability row only
		convID  = "conv-modellist-noframe"
	)
	nonEmptySeam := func() []protocol.ModelListPayload {
		return []protocol.ModelListPayload{sampleModelListPayload(convID, "sonnet")}
	}

	tests := []struct {
		name string
		seam func() []protocol.ModelListPayload
		caps []string
		// controlConn opens a second, interactive conn AFTER the conn under test.
		// The capability row needs it: without a conn that DOES receive a
		// model_list, A's zero is indistinguishable from an empty seam.
		controlConn bool
	}{
		{
			name: "nil seam",
			seam: nil, // unwired / foreground
			caps: []string{protocol.CapabilityInteractive},
		},
		{
			name: "zero payloads",
			seam: func() []protocol.ModelListPayload { return nil },
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
				Frames:             frames,
				Outbound:           rec.outbound,
				StaticPriv:         respPriv,
				Devices:            reg,
				ServerID:           v2TestServerID,
				Logger:             silentLogger(),
				RetainedModelLists: tt.seam,
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
				wantEnvelopes = 3 // + resp(CTL) + model_list(CTL)
			}
			waitForEnvelopes(t, rec, wantEnvelopes)

			if tt.controlConn {
				if got := reconciledModelLists(t, rec, connCtl, ctlRecv); len(got) != 1 {
					t.Fatalf("interactive control conn %q: got %d model_list, want 1 (the seam must be enumerating a list for A's zero to mean the gate)", connCtl, len(got))
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

// TestV2Session_ModelListReconcile_ContentFreeLogging pins AC3: no model value —
// selectable value, resolved model, or display name — reaches a log line on this
// path at any level. The success path emits no log line for the reconcile at all;
// the two error branches carry content-free discriminants only, by construction.
// Captured at Debug (the lowest level) so any leak would surface; the
// handshake-accept line proves the capture is live (non-vacuous). Three distinct
// sentinels rather than one: the fields are separate struct members, and a branch
// echoing just one of them must not pass.
func TestV2Session_ModelListReconcile_ContentFreeLogging(t *testing.T) {
	t.Parallel()

	const (
		connA    = "c-v2-A"
		convID   = "conv-modellist-secret"
		value    = "SENTINEL-VALUE-do-not-log"
		resolved = "SENTINEL-RESOLVED-MODEL-do-not-log"
		display  = "SENTINEL-DISPLAY-NAME-do-not-log"
	)
	payload := protocol.ModelListPayload{
		ConversationID: convID,
		Models: []protocol.ModelOption{{
			ResolvedModel: resolved,
			Value:         value,
			DisplayName:   display,
			EffortLevels:  []string{"low"},
		}},
	}

	logBuf := &lockedBuffer{}
	logger := slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	respPriv, respPub := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)
	frames := make(chan protocol.RoutingEnvelope, 8)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:             frames,
		Outbound:           rec.outbound,
		StaticPriv:         respPriv,
		Devices:            reg,
		ServerID:           v2TestServerID,
		Logger:             logger,
		RetainedModelLists: func() []protocol.ModelListPayload { return []protocol.ModelListPayload{payload} },
	})
	t.Cleanup(stop)

	_, aRecv := openModalConn(t, mgr, frames, rec, respPub, connA, []string{protocol.CapabilityInteractive})

	// noise_resp + the one reconciled model_list: once both are recorded the open
	// path (and any log line it would emit) has run.
	waitForEnvelopes(t, rec, 2)
	if got := reconciledModelLists(t, rec, connA, aRecv); !reflect.DeepEqual(got[convID], payload) {
		t.Fatalf("conn %q: reconcile did not send the sentinel payload (got %+v)", connA, got)
	}

	logs := logBuf.String()
	if !strings.Contains(logs, "handshake") {
		t.Fatalf("log capture appears inert (no handshake line); cannot trust the never-log assertion. logs = %q", logs)
	}
	for _, sentinel := range []string{value, resolved, display} {
		if strings.Contains(logs, sentinel) {
			t.Errorf("model value %q leaked into a log record; logs = %q", sentinel, logs)
		}
	}
}
