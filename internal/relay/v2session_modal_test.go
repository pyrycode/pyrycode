package relay

import (
	"encoding/base64"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// --- #727 inbound modal-control interception + dismiss-broadcast fixtures ---

// modalCallRecord captures the arguments of one ModalResolver call so a test can
// assert what the manager routed across the seam.
type modalCallRecord struct {
	modalID     string
	optionID    string
	answerToken string
	dev         *devices.Device
}

// fakeModalResolver is a relay-side test double for ModalResolver: it records
// every call and returns a canned ModalDismissal with ok=true for a cancel of
// the configured cancelOKFor id (any other id ⇒ the unknown-id no-op).
// ResolveAnswer is always the deferred no-op (ok=false), matching this slice's
// contract. The mutex guards the cross-goroutine read (Run goroutine writes, the
// test goroutine reads).
type fakeModalResolver struct {
	mu          sync.Mutex
	cancelOKFor string
	dismissal   ModalDismissal
	cancelCalls []modalCallRecord
	answerCalls []modalCallRecord
}

func (f *fakeModalResolver) ResolveCancel(modalID string, dev *devices.Device) (ModalDismissal, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelCalls = append(f.cancelCalls, modalCallRecord{modalID: modalID, dev: dev})
	if modalID != f.cancelOKFor {
		return ModalDismissal{}, false
	}
	return f.dismissal, true
}

func (f *fakeModalResolver) ResolveAnswer(modalID, optionID, answerToken string, dev *devices.Device) (ModalDismissal, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answerCalls = append(f.answerCalls, modalCallRecord{modalID: modalID, optionID: optionID, answerToken: answerToken, dev: dev})
	return ModalDismissal{}, false
}

func (f *fakeModalResolver) cancelSnapshot() []modalCallRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]modalCallRecord(nil), f.cancelCalls...)
}

func (f *fakeModalResolver) answerSnapshot() []modalCallRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]modalCallRecord(nil), f.answerCalls...)
}

// sealAppFrameConn AEAD-seals env under cs, wraps as a noise_msg addressed to
// connID, and returns the routing envelope ready for the manager's Frames
// channel. The conn-aware sibling of sealAppFrame (which hardcodes v2TestConnID).
func sealAppFrameConn(t *testing.T, cs *noise.CipherState, connID string, env protocol.Envelope) protocol.RoutingEnvelope {
	t.Helper()
	envBytes, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	ciphertext, err := cs.Encrypt(envBytes)
	if err != nil {
		t.Fatalf("seal app envelope: %v", err)
	}
	return wrapInnerFrame(t, connID, protocol.TypeNoiseMsg, ciphertext)
}

// openModalConn drives one paired-device handshake for connID (advertising caps)
// through an already-running manager and returns the initiator's CipherStates
// (initSend encrypts phone→binary, initRecv decrypts binary→phone). It waits for
// the conn to reach V2StateOpen, then recovers that conn's noise_resp from the
// shared recorder to complete the handshake. Used to stand up ≥2 heads on one
// manager for the dismiss fan-out.
func openModalConn(t *testing.T, mgr *V2SessionManager, frames chan protocol.RoutingEnvelope, rec *v2Recorder, respPub []byte, connID string, caps []string) (initSend, initRecv *noise.CipherState) {
	t.Helper()
	initPriv, _ := genV2Keypair(t)
	initiator, err := noise.NewInitiator(initPriv, respPub)
	if err != nil {
		t.Fatalf("NewInitiator(%s): %v", connID, err)
	}
	initMsg, err := initiator.WriteInit(buildHelloEarlyDataCaps(t, v2TestToken, caps))
	if err != nil {
		t.Fatalf("WriteInit(%s): %v", connID, err)
	}
	frames <- wrapInnerFrame(t, connID, protocol.TypeNoiseInit, initMsg)
	waitConnOpen(t, mgr, connID)

	respRaw := findNoiseRespForConn(t, rec, connID)
	_, initSend, initRecv, err = initiator.ReadResp(respRaw)
	if err != nil {
		t.Fatalf("ReadResp(%s): %v", connID, err)
	}
	return initSend, initRecv
}

// findNoiseRespForConn returns the decoded noise_resp raw bytes for connID from
// the recorder. waitConnOpen guarantees the noise_resp was forwarded (and thus
// recorded) before the conn is enumerable as open, so the scan is race-free.
func findNoiseRespForConn(t *testing.T, rec *v2Recorder, connID string) []byte {
	t.Helper()
	for _, env := range rec.snapshot() {
		if env.ConnID != connID {
			continue
		}
		var inner protocol.InnerFrameV2
		if err := json.Unmarshal(env.Frame, &inner); err != nil {
			t.Fatalf("unmarshal inner frame for %s: %v", connID, err)
		}
		if inner.Type != protocol.TypeNoiseResp {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(inner.Data)
		if err != nil {
			t.Fatalf("base64 noise_resp for %s: %v", connID, err)
		}
		return raw
	}
	t.Fatalf("no noise_resp recorded for conn %q", connID)
	return nil
}

// noiseMsgsForConn returns the captured noise_msg routing envelopes addressed to
// connID (the binary→phone application frames — modal_dismissed here), in
// recorded order.
func noiseMsgsForConn(t *testing.T, rec *v2Recorder, connID string) []protocol.RoutingEnvelope {
	t.Helper()
	var out []protocol.RoutingEnvelope
	for _, env := range rec.snapshot() {
		if env.ConnID != connID {
			continue
		}
		var inner protocol.InnerFrameV2
		if err := json.Unmarshal(env.Frame, &inner); err != nil {
			t.Fatalf("unmarshal inner frame for %s: %v", connID, err)
		}
		if inner.Type == protocol.TypeNoiseMsg {
			out = append(out, env)
		}
	}
	return out
}

// waitForResolverCall polls get until it reports at least n recorded calls or the
// deadline expires. The synchronisation knob for the no-op branches, which emit
// no outbound envelope but do route through the resolver seam.
func waitForResolverCall(t *testing.T, get func() int, n int, label string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if get() >= n {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("%s: got %d calls, want >= %d", label, get(), n)
}

// assertModalDismissed decrypts the single noise_msg addressed to connID under
// recv and asserts it is a modal_dismissed carrying the expected fields.
func assertModalDismissed(t *testing.T, rec *v2Recorder, connID string, recv *noise.CipherState, wantModalID, wantOutcome, wantSource string) {
	t.Helper()
	msgs := noiseMsgsForConn(t, rec, connID)
	if len(msgs) != 1 {
		t.Fatalf("conn %q: got %d noise_msg, want exactly 1 (modal_dismissed)", connID, len(msgs))
	}
	inner := decryptAppFrame(t, msgs[0], recv)
	if inner.Type != protocol.TypeModalDismissed {
		t.Fatalf("conn %q: reply Type = %q, want %q", connID, inner.Type, protocol.TypeModalDismissed)
	}
	var p protocol.ModalDismissedPayload
	if err := json.Unmarshal(inner.Payload, &p); err != nil {
		t.Fatalf("conn %q: decode modal_dismissed payload: %v", connID, err)
	}
	if p.ModalID != wantModalID {
		t.Errorf("conn %q: ModalID = %q, want %q", connID, p.ModalID, wantModalID)
	}
	if p.Outcome != wantOutcome {
		t.Errorf("conn %q: Outcome = %q, want %q", connID, p.Outcome, wantOutcome)
	}
	if p.Source != wantSource {
		t.Errorf("conn %q: Source = %q, want %q", connID, p.Source, wantSource)
	}
}

// TestV2Session_ModalCancel_FanOut drives a modal_cancel through the real
// Frames/Run loop with three open heads — two interactive, one not — and asserts
// the manager routes ResolveCancel with the right modal_id + device and fans the
// modal_dismissed{cancelled, remote} to BOTH interactive heads but not the
// non-interactive one. Running through the real Run loop is the no-deadlock proof
// (broadcastModalDismissed reads m.sessions directly and never calls ActiveConns;
// an accidental ActiveConns call would hang this test). AC-1, AC-2.
func TestV2Session_ModalCancel_FanOut(t *testing.T) {
	t.Parallel()

	const (
		connA      = "c-v2-A" // interactive; the canceling head
		connB      = "c-v2-B" // interactive; a second head showing the same modal
		connC      = "c-v2-C" // non-interactive; must NOT receive the dismissal
		modalID    = "modal-fanout-cancel"
		wantOutT   = "cancelled"
		wantSource = "remote"
	)

	respPriv, respPub := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)
	fake := &fakeModalResolver{
		cancelOKFor: modalID,
		dismissal:   ModalDismissal{Outcome: wantOutT, Source: wantSource},
	}

	frames := make(chan protocol.RoutingEnvelope, 8)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:        frames,
		Outbound:      rec.outbound,
		StaticPriv:    respPriv,
		Devices:       reg,
		ServerID:      v2TestServerID,
		Logger:        silentLogger(),
		ModalResolver: fake,
	})
	t.Cleanup(stop)

	aSend, aRecv := openModalConn(t, mgr, frames, rec, respPub, connA, []string{protocol.CapabilityInteractive})
	_, bRecv := openModalConn(t, mgr, frames, rec, respPub, connB, []string{protocol.CapabilityInteractive})
	openModalConn(t, mgr, frames, rec, respPub, connC, nil) // non-interactive

	// Cancel from head A. The frame is sealed under A's send state and routed
	// on A's conn id.
	frames <- sealAppFrameConn(t, aSend, connA, protocol.Envelope{
		ID:      42,
		Type:    protocol.TypeModalCancel,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(`{"modal_id":"` + modalID + `"}`),
	})

	// Three handshake noise_resp + two dismissals (A and B, not C) = five total.
	// Exactly five are ever emitted in this scenario, so once five are recorded
	// the fan-out is complete and the snapshot is final.
	waitForEnvelopes(t, rec, 5)

	// AC-1: ResolveCancel routed once with the right modal_id + per-conn device.
	calls := fake.cancelSnapshot()
	if len(calls) != 1 {
		t.Fatalf("ResolveCancel calls = %d, want 1", len(calls))
	}
	if calls[0].modalID != modalID {
		t.Errorf("ResolveCancel modal_id = %q, want %q", calls[0].modalID, modalID)
	}
	if calls[0].dev == nil {
		t.Fatal("ResolveCancel device is nil, want the per-conn paired device")
	}
	if calls[0].dev.Name != v2TestDevName {
		t.Errorf("ResolveCancel device.Name = %q, want %q", calls[0].dev.Name, v2TestDevName)
	}

	// AC-2: the dismissal fans out to BOTH interactive heads...
	assertModalDismissed(t, rec, connA, aRecv, modalID, wantOutT, wantSource)
	assertModalDismissed(t, rec, connB, bRecv, modalID, wantOutT, wantSource)

	// ...and the capability gate withholds it from the non-interactive head.
	if msgs := noiseMsgsForConn(t, rec, connC); len(msgs) != 0 {
		t.Errorf("non-interactive conn %q got %d noise_msg, want 0", connC, len(msgs))
	}
}

// TestV2Session_ModalAnswer_NoOp proves an inbound modal_answer is intercepted
// and routed to the resolver seam but, with this slice's deferred-no-op
// ResolveAnswer, broadcasts NO modal_dismissed (AC-3). The relay owns the
// "routed through the seam + no dismissal" half; the resolver test owns "no
// keystroke / no audit / modal untouched".
func TestV2Session_ModalAnswer_NoOp(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)
	fake := &fakeModalResolver{} // ResolveAnswer always (zero,false)

	frames := make(chan protocol.RoutingEnvelope, 4)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:        frames,
		Outbound:      rec.outbound,
		StaticPriv:    respPriv,
		Devices:       reg,
		ServerID:      v2TestServerID,
		Logger:        silentLogger(),
		ModalResolver: fake,
	})
	t.Cleanup(stop)

	send, _ := openModalConn(t, mgr, frames, rec, respPub, v2TestConnID, []string{protocol.CapabilityInteractive})

	frames <- sealAppFrameConn(t, send, v2TestConnID, protocol.Envelope{
		ID:      7,
		Type:    protocol.TypeModalAnswer,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(`{"modal_id":"m-ans","option_id":"allow_once","answer_token":"tok-1"}`),
	})

	// No outbound frame is emitted, so synchronise on the resolver call instead.
	waitForResolverCall(t, func() int { return len(fake.answerSnapshot()) }, 1, "ResolveAnswer")

	// Stop the Run goroutine so the recorder is final, then assert no dismissal.
	stop()

	calls := fake.answerSnapshot()
	if len(calls) != 1 {
		t.Fatalf("ResolveAnswer calls = %d, want 1", len(calls))
	}
	if calls[0].modalID != "m-ans" || calls[0].optionID != "allow_once" || calls[0].answerToken != "tok-1" {
		t.Errorf("ResolveAnswer args = %+v, want {m-ans allow_once tok-1}", calls[0])
	}
	if got := len(fake.cancelSnapshot()); got != 0 {
		t.Errorf("ResolveCancel calls = %d, want 0 (answer must not route cancel)", got)
	}
	if msgs := noiseMsgsForConn(t, rec, v2TestConnID); len(msgs) != 0 {
		t.Errorf("modal_answer broadcast %d noise_msg, want 0 (deferred no-op)", len(msgs))
	}
	if s := mgr.sessions[v2TestConnID]; s == nil || s.State() != V2StateOpen {
		t.Errorf("session state after modal_answer not V2StateOpen")
	}
}

// TestV2Session_ModalCancel_UnknownID_NoOp proves a modal_cancel whose resolver
// reports ok=false (unknown / already-resolved id) broadcasts no dismissal
// (AC-4).
func TestV2Session_ModalCancel_UnknownID_NoOp(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)
	fake := &fakeModalResolver{cancelOKFor: "some-other-id"} // the sent id misses

	frames := make(chan protocol.RoutingEnvelope, 4)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:        frames,
		Outbound:      rec.outbound,
		StaticPriv:    respPriv,
		Devices:       reg,
		ServerID:      v2TestServerID,
		Logger:        silentLogger(),
		ModalResolver: fake,
	})
	t.Cleanup(stop)

	send, _ := openModalConn(t, mgr, frames, rec, respPub, v2TestConnID, []string{protocol.CapabilityInteractive})

	frames <- sealAppFrameConn(t, send, v2TestConnID, protocol.Envelope{
		ID:      9,
		Type:    protocol.TypeModalCancel,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(`{"modal_id":"ghost-modal"}`),
	})

	waitForResolverCall(t, func() int { return len(fake.cancelSnapshot()) }, 1, "ResolveCancel")
	stop()

	if msgs := noiseMsgsForConn(t, rec, v2TestConnID); len(msgs) != 0 {
		t.Errorf("unknown-id modal_cancel broadcast %d noise_msg, want 0", len(msgs))
	}
	if s := mgr.sessions[v2TestConnID]; s == nil || s.State() != V2StateOpen {
		t.Errorf("session state after unknown-id modal_cancel not V2StateOpen")
	}
}

// TestV2Session_ModalControl_NilResolver proves that with no ModalResolver wired
// (foreground / pre-#708) both modal control frames are inert no-ops: each
// debug-logs and returns, emitting no outbound frame, never panicking, and
// leaving the session V2StateOpen. Mirrors the rekey_request inert tests.
func TestV2Session_ModalControl_NilResolver(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		ftype   string
		payload string
		logWant string
	}{
		{"cancel inert", protocol.TypeModalCancel, `{"modal_id":"x"}`, "event=v2.modal.cancel.inert"},
		{"answer inert", protocol.TypeModalAnswer, `{"modal_id":"x","option_id":"o","answer_token":"t"}`, "event=v2.modal.answer.inert"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			respPriv, respPub := genV2Keypair(t)
			reg := v2PairedRegistry(t, v2TestToken)

			logger, logBuf := bufferLogger()
			frames := make(chan protocol.RoutingEnvelope, 4)
			rec := &v2Recorder{}
			mgr, stop := startManager(t, V2SessionConfig{
				Frames:     frames,
				Outbound:   rec.outbound,
				StaticPriv: respPriv,
				Devices:    reg,
				ServerID:   v2TestServerID,
				Logger:     logger,
				// ModalResolver intentionally nil.
			})
			t.Cleanup(stop)

			send, _ := openModalConn(t, mgr, frames, rec, respPub, v2TestConnID, []string{protocol.CapabilityInteractive})

			frames <- sealAppFrameConn(t, send, v2TestConnID, protocol.Envelope{
				ID:      11,
				Type:    tt.ftype,
				TS:      time.Now().UTC(),
				Payload: json.RawMessage(tt.payload),
			})

			waitForLogContains(t, logBuf, tt.logWant)
			stop()

			if msgs := noiseMsgsForConn(t, rec, v2TestConnID); len(msgs) != 0 {
				t.Errorf("nil-resolver %s broadcast %d noise_msg, want 0", tt.ftype, len(msgs))
			}
			if s := mgr.sessions[v2TestConnID]; s == nil || s.State() != V2StateOpen {
				t.Errorf("session state after nil-resolver %s not V2StateOpen", tt.ftype)
			}
		})
	}
}
