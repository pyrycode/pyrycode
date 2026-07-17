package relay

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/pyrycode/pyrycode/internal/modalbridge"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/tui-driver/pkg/tuidriver"
)

// --- #877 connect-time modal reconcile fixtures ---

// sampleModalPayload builds a fully-populated ModalShownPayload for the given
// modal_id, standing in for one entry the OutstandingModals seam enumerates. A
// fresh Options slice per call mirrors Snapshot's clone-on-read. It carries a
// conversation_id (#1065) so the reconcile tests prove the outbound scoping key
// survives replay to a reconnecting conn identically to initial delivery (AC3).
func sampleModalPayload(modalID string) protocol.ModalShownPayload {
	return protocol.ModalShownPayload{
		ConversationID: "conv-scope",
		ModalID:        modalID,
		Class:          "permission",
		Title:          "Permission required",
		Prompt:         "Allow bash(rm -rf /tmp/scratch)?",
		Options: []protocol.ModalOption{
			{ID: "allow_once", Label: "Allow once"},
			{ID: "reject_once", Label: "Reject once"},
		},
		DefaultOptionID: "reject_once",
	}
}

// reconciledModals decrypts every noise_msg addressed to connID under recv (in
// recorded, hence AEAD-nonce, order) and returns each modal_shown keyed by
// modal_id. In these tests the reconcile is the only outbound app traffic, so a
// non-modal_shown noise_msg is a bug; a repeated modal_id is a fan-out/dup bug.
// Decrypt each conn's frames exactly once per test — Decrypt advances the recv
// nonce.
func reconciledModals(t *testing.T, rec *v2Recorder, connID string, recv *noise.CipherState) map[string]protocol.ModalShownPayload {
	t.Helper()
	out := make(map[string]protocol.ModalShownPayload)
	for _, env := range noiseMsgsForConn(t, rec, connID) {
		inner := decryptAppFrame(t, env, recv)
		if inner.Type != protocol.TypeModalShown {
			t.Fatalf("conn %q: noise_msg Type = %q, want %q", connID, inner.Type, protocol.TypeModalShown)
		}
		var p protocol.ModalShownPayload
		if err := json.Unmarshal(inner.Payload, &p); err != nil {
			t.Fatalf("conn %q: decode modal_shown payload: %v", connID, err)
		}
		if _, dup := out[p.ModalID]; dup {
			t.Errorf("conn %q: modal_id %q re-sent more than once", connID, p.ModalID)
		}
		out[p.ModalID] = p
	}
	return out
}

// TestV2Session_ModalReconcile_InteractiveOpen re-sends the one outstanding
// modal_shown to a freshly interactive-open conn, carrying the original modal_id
// and payload (AC1).
func TestV2Session_ModalReconcile_InteractiveOpen(t *testing.T) {
	t.Parallel()

	const (
		connA   = "c-v2-A"
		modalID = "modal-reconcile-one"
	)
	want := sampleModalPayload(modalID)

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
		OutstandingModals: func() []protocol.ModalShownPayload { return []protocol.ModalShownPayload{sampleModalPayload(modalID)} },
	})
	t.Cleanup(stop)

	_, aRecv := openModalConn(t, mgr, frames, rec, respPub, connA, []string{protocol.CapabilityInteractive})

	// noise_resp + one reconciled modal_shown = 2 envelopes; exactly two are
	// ever emitted here, so the snapshot is final once two are recorded.
	waitForEnvelopes(t, rec, 2)

	got := reconciledModals(t, rec, connA, aRecv)
	if len(got) != 1 {
		t.Fatalf("conn %q: got %d modal_shown, want 1", connA, len(got))
	}
	if !reflect.DeepEqual(got[modalID], want) {
		t.Errorf("conn %q: modal_shown = %+v, want %+v", connA, got[modalID], want)
	}
	// AC3 (#1065): the replayed payload carries the same conversation_id scope key
	// as initial delivery, so replay to a reconnecting conn is scoped identically.
	if got[modalID].ConversationID != want.ConversationID {
		t.Errorf("conn %q: reconciled conversation_id = %q, want %q (replay must carry the scope key)", connA, got[modalID].ConversationID, want.ConversationID)
	}
}

// TestV2Session_ModalReconcile_TwoModals re-sends every outstanding modal_shown,
// keyed and matched by modal_id, order-independent (AC1).
func TestV2Session_ModalReconcile_TwoModals(t *testing.T) {
	t.Parallel()

	const (
		connA = "c-v2-A"
		id1   = "modal-reconcile-1"
		id2   = "modal-reconcile-2"
	)
	want1, want2 := sampleModalPayload(id1), sampleModalPayload(id2)

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
		OutstandingModals: func() []protocol.ModalShownPayload {
			return []protocol.ModalShownPayload{sampleModalPayload(id1), sampleModalPayload(id2)}
		},
	})
	t.Cleanup(stop)

	_, aRecv := openModalConn(t, mgr, frames, rec, respPub, connA, []string{protocol.CapabilityInteractive})

	// noise_resp + two reconciled modal_shown = 3 envelopes.
	waitForEnvelopes(t, rec, 3)

	got := reconciledModals(t, rec, connA, aRecv)
	if len(got) != 2 {
		t.Fatalf("conn %q: got %d modal_shown, want 2", connA, len(got))
	}
	if !reflect.DeepEqual(got[id1], want1) {
		t.Errorf("conn %q: modal %q = %+v, want %+v", connA, id1, got[id1], want1)
	}
	if !reflect.DeepEqual(got[id2], want2) {
		t.Errorf("conn %q: modal %q = %+v, want %+v", connA, id2, got[id2], want2)
	}
}

// TestV2Session_ModalReconcile_UnicastOnlyOpeningConn proves the re-send is
// unicast to the just-opened conn, NOT a fan-out: with A already open, opening B
// delivers the modal_shown to B only — A receives no new modal_shown (AC1
// unicast). A broadcast (like broadcastModalDismissed) would give A a second one.
func TestV2Session_ModalReconcile_UnicastOnlyOpeningConn(t *testing.T) {
	t.Parallel()

	const (
		connA   = "c-v2-A" // interactive; opened first
		connB   = "c-v2-B" // interactive; opened second
		modalID = "modal-reconcile-unicast"
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
		OutstandingModals: func() []protocol.ModalShownPayload { return []protocol.ModalShownPayload{sampleModalPayload(modalID)} },
	})
	t.Cleanup(stop)

	_, aRecv := openModalConn(t, mgr, frames, rec, respPub, connA, []string{protocol.CapabilityInteractive})
	_, bRecv := openModalConn(t, mgr, frames, rec, respPub, connB, []string{protocol.CapabilityInteractive})

	// resp(A) + modal_shown(A) + resp(B) + modal_shown(B) = 4 in the correct
	// (unicast) case. A broadcast on B's open would push a 5th (a second
	// modal_shown to A), which the count + A-side dup check below would catch.
	waitForEnvelopes(t, rec, 4)

	gotA := reconciledModals(t, rec, connA, aRecv)
	gotB := reconciledModals(t, rec, connB, bRecv)
	if len(gotA) != 1 {
		t.Errorf("conn %q (opened first): got %d modal_shown, want exactly 1 (its own open, none from B's open)", connA, len(gotA))
	}
	if len(gotB) != 1 {
		t.Errorf("conn %q (opened second): got %d modal_shown, want 1", connB, len(gotB))
	}
}

// TestV2Session_ModalReconcile_NonInteractiveNoResend withholds the re-send from
// a conn that handshook without the interactive capability (AC2). The interactive
// control conn A proves the seam WAS enumerating a modal, so C's zero is the
// capability gate, not an empty registry.
func TestV2Session_ModalReconcile_NonInteractiveNoResend(t *testing.T) {
	t.Parallel()

	const (
		connC   = "c-v2-C" // non-interactive; must receive nothing
		connA   = "c-v2-A" // interactive control
		modalID = "modal-reconcile-noninteractive"
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
		OutstandingModals: func() []protocol.ModalShownPayload { return []protocol.ModalShownPayload{sampleModalPayload(modalID)} },
	})
	t.Cleanup(stop)

	// Open the non-interactive conn first and let it fully settle (its reconcile
	// runs synchronously in handleNoiseInit and pushes nothing) before the
	// interactive control conn's multi-step handshake pumps the Run loop.
	openModalConn(t, mgr, frames, rec, respPub, connC, nil)
	_, aRecv := openModalConn(t, mgr, frames, rec, respPub, connA, []string{protocol.CapabilityInteractive})

	// resp(C) + resp(A) + modal_shown(A) = 3 envelopes.
	waitForEnvelopes(t, rec, 3)

	if got := reconciledModals(t, rec, connA, aRecv); len(got) != 1 {
		t.Fatalf("interactive control conn %q: got %d modal_shown, want 1", connA, len(got))
	}
	if msgs := noiseMsgsForConn(t, rec, connC); len(msgs) != 0 {
		t.Errorf("non-interactive conn %q: got %d noise_msg, want 0", connC, len(msgs))
	}
}

// TestV2Session_ModalReconcile_NothingOutstanding sends no modal_shown when the
// snapshot is empty (AC3). The empty slice short-circuits reconcile before any
// Push, so the only envelope is the handshake noise_resp.
func TestV2Session_ModalReconcile_NothingOutstanding(t *testing.T) {
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
		OutstandingModals: func() []protocol.ModalShownPayload { return nil }, // empty snapshot
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

// TestV2Session_ModalReconcile_NilSeamInert keeps the pre-#877 posture: a nil
// OutstandingModals seam makes reconcile a no-op and leaves the session open
// (foreground / unwired byte-stability).
func TestV2Session_ModalReconcile_NilSeamInert(t *testing.T) {
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
		// OutstandingModals left nil.
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

// recordModal mints one outstanding permission modal in a real registry and
// returns its modal_id. Exercises the production Record path so #877's tests bind
// to the same current-truth semantics the daemon wires.
func recordModal(t *testing.T, reg *modalbridge.Registry, screenText string) string {
	t.Helper()
	req, wireClass, ok := modalbridge.PermissionRequestForClass(tuidriver.ModalClassPermission, screenText)
	if !ok {
		t.Fatalf("PermissionRequestForClass(permission): not ok")
	}
	p, err := reg.Record(req, wireClass, "")
	if err != nil {
		t.Fatalf("Record modal: %v", err)
	}
	return p.ModalID
}

// TestV2Session_ModalReconcile_ResolvedBeforeOpenNotResent proves a modal
// resolved before the handshake completes is not re-sent — reconcile reflects
// current truth, so a resolved modal_id never resurfaces (AC4). Uses a real
// registry so the current-truth semantics are the production Snapshot's.
func TestV2Session_ModalReconcile_ResolvedBeforeOpenNotResent(t *testing.T) {
	t.Parallel()

	const connA = "c-v2-A"

	realReg := modalbridge.New()
	modalID := recordModal(t, realReg, "Allow bash(git push)?")
	if _, ok := realReg.Resolve(modalID); !ok { // resolve it BEFORE the conn opens
		t.Fatalf("Resolve(%q): not ok", modalID)
	}

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
		OutstandingModals: realReg.Snapshot,
	})
	t.Cleanup(stop)

	openModalConn(t, mgr, frames, rec, respPub, connA, []string{protocol.CapabilityInteractive})

	// Snapshot is empty (the sole modal was resolved), so reconcile short-circuits
	// before any Push; only the noise_resp exists.
	waitForEnvelopes(t, rec, 1)

	if msgs := noiseMsgsForConn(t, rec, connA); len(msgs) != 0 {
		t.Errorf("conn %q: got %d noise_msg, want 0 (resolved modal must not resurface)", connA, len(msgs))
	}
}

// TestV2Session_ModalReconcile_PureReadNoMintNoRearm proves the reconcile is a
// pure read of the registry: it mints no new nonce and retires nothing, so the
// re-sent prompt stays answerable exactly once and an answer to an already-
// resolved modal_id stays inert (AC5). reconcileModals contains no arm/resolve
// call by construction — there is no deny-on-timeout timer in internal/relay
// (it lives in the cmd/pyry surfacer) — so this pins the registry-observable
// half: the snapshot is unchanged across the re-send, and Resolve is one-shot.
func TestV2Session_ModalReconcile_PureReadNoMintNoRearm(t *testing.T) {
	t.Parallel()

	const connA = "c-v2-A"

	realReg := modalbridge.New()
	modalID := recordModal(t, realReg, "Allow bash(rm build/)?")

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
		OutstandingModals: realReg.Snapshot,
	})
	t.Cleanup(stop)

	_, aRecv := openModalConn(t, mgr, frames, rec, respPub, connA, []string{protocol.CapabilityInteractive})

	// noise_resp + the one reconciled modal_shown.
	waitForEnvelopes(t, rec, 2)
	if got := reconciledModals(t, rec, connA, aRecv); len(got) != 1 || got[modalID].ModalID != modalID {
		t.Fatalf("conn %q: reconcile did not re-send modal %q (got %+v)", connA, modalID, got)
	}

	// Mints no new nonce, retires nothing: the snapshot still holds exactly the
	// one modal, same id — the re-send did not create a second registry entry.
	after := realReg.Snapshot()
	if len(after) != 1 || after[0].ModalID != modalID {
		t.Fatalf("registry after reconcile = %+v, want exactly the original modal %q (no new nonce)", after, modalID)
	}

	// One-shot answerability unchanged: the modal resolves exactly once (the first
	// answer), and a second answer to the same modal_id is inert.
	if _, ok := realReg.Resolve(modalID); !ok {
		t.Fatalf("first Resolve(%q): not ok — the re-sent modal must stay answerable once", modalID)
	}
	if _, ok := realReg.Resolve(modalID); ok {
		t.Errorf("second Resolve(%q): ok — an answer to an already-resolved modal_id must be inert", modalID)
	}
}
