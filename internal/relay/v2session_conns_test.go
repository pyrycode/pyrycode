package relay

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// --- open-session enumeration (ActiveConns) tests (#588) ---

// TestV2Session_ActiveConns_OpenOnly pins both halves of AC#1: every
// V2StateOpen session is enumerated, and every non-open session (still
// handshaking, or handshake-complete-but-token-unvalidated) is excluded — the
// same V2StateOpen security gate Push enforces. White-box session injection
// mirrors TestV2Session_Push_NotOpen: ActiveConns reads s.state only (never
// s.send), so nil CipherStates are safe, and the snapshot channel-send is the
// happens-before edge that publishes the map writes to Run (no frames fed, no
// timers armed, so Run touches the map only when it services the snapshot).
//
// The four-state injection map is load-bearing: this is the only test covering
// the V2StateAwaitingInit arm of the gate. The sibling
// TestV2Session_ActiveConns_MixedInteractive injects a handshake-complete
// session but no awaiting-init one.
func TestV2Session_ActiveConns_OpenOnly(t *testing.T) {
	t.Parallel()

	respPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)
	frames := make(chan protocol.RoutingEnvelope)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   rec.outbound,
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
	})
	t.Cleanup(stop)

	for connID, st := range map[string]V2SessionState{
		"c-open-a":    V2StateOpen,
		"c-open-b":    V2StateOpen,
		"c-handshake": V2StateHandshakeComplete,
		"c-awaiting":  V2StateAwaitingInit,
	} {
		mgr.sessions[connID] = &V2Session{connID: connID, state: st}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	conns := mgr.ActiveConns(ctx)
	got := make([]string, len(conns))
	for i, c := range conns {
		got[i] = c.ConnID
	}
	slices.Sort(got) // unordered set — sort before comparing positionally
	want := []string{"c-open-a", "c-open-b"}
	if !slices.Equal(got, want) {
		t.Errorf("ActiveConns() conn ids = %v, want %v (open sessions only)", got, want)
	}
}

// TestV2Session_ActiveConns_TornDownSessionAbsent pins AC#2: a session that
// was opened then torn down (closeWith deletes it from the map) no longer
// appears in the snapshot. Drives a real open, confirms its id is enumerated,
// then drives an AEAD-failure 4421 teardown (the TestV2Session_Push_Closed
// recipe) and re-enumerates.
func TestV2Session_ActiveConns_TornDownSessionAbsent(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	frames := make(chan protocol.RoutingEnvelope, 2)
	rec := &v2Recorder{}
	sess := driveToOpen(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   rec.outbound,
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
	}, frames, rec, respPub, initPriv)
	t.Cleanup(sess.stop)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	enumerated := func(conns []ActiveConn) bool {
		return slices.ContainsFunc(conns, func(c ActiveConn) bool { return c.ConnID == v2TestConnID })
	}

	// The freshly-opened session is enumerated.
	if got := sess.mgr.ActiveConns(ctx); !enumerated(got) {
		t.Fatalf("ActiveConns() = %v, want it to contain %q", got, v2TestConnID)
	}

	// Drive an AEAD-failure 4421 teardown: flip a byte in a sealed frame's
	// ciphertext so closeWith deletes the session from the map.
	envBytes, err := json.Marshal(protocol.Envelope{
		ID: 1, Type: protocol.TypeListConversations, TS: time.Now().UTC(),
		Payload: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	ciphertext, err := sess.initSend.Encrypt(envBytes)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	ciphertext[0] ^= 0xff
	frames <- wrapInnerFrame(t, v2TestConnID, protocol.TypeNoiseMsg, ciphertext)

	// closeWith deletes the entry before emitting the 4421 close; observing
	// the close envelope guarantees the delete has happened on Run, so the
	// subsequent snapshot (also serviced on Run) sees the post-delete map.
	closeEnvs := waitForEnvelopes(t, sess.rec, 2) // noise_resp + 4421 close
	if closeEnvs[1].CloseCode != uint16(StatusProtocolMismatch) {
		t.Fatalf("close_code = %d, want %d", closeEnvs[1].CloseCode, StatusProtocolMismatch)
	}

	if got := sess.mgr.ActiveConns(ctx); enumerated(got) {
		t.Errorf("ActiveConns() = %v, want it to NOT contain torn-down %q", got, v2TestConnID)
	}
}

// TestV2Session_ActiveConns_ConcurrentWithDispatch_RaceClean pins AC#3 — the
// -race proof. A separate goroutine hammers ActiveConns in a tight loop
// while inbound sealed request frames drive dispatchAppFrame replies on the
// same open session, so the snapshot funnel and the reply path contend for the
// single Run goroutine. Every snapshot is either {v2TestConnID} or empty —
// never garbage; the solicited replies still decrypt in capture (= seal) order
// afterwards, proving the concurrent reads never corrupted session state. Run
// under -race.
func TestV2Session_ActiveConns_ConcurrentWithDispatch_RaceClean(t *testing.T) {
	t.Parallel()

	const mReq = 16

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	echoPayload, err := json.Marshal(map[string]string{"text": "reply"})
	if err != nil {
		t.Fatalf("marshal echo payload: %v", err)
	}
	handlers := map[string]dispatch.Handler{
		protocol.TypeListConversations: func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
			return c.Reply(ctx, env, protocol.TypeConversations, echoPayload)
		},
	}

	frames := make(chan protocol.RoutingEnvelope, mReq)
	rec := &v2Recorder{}
	sess := driveToOpen(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   rec.outbound,
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
		Handlers:   handlers,
	}, frames, rec, respPub, initPriv)
	t.Cleanup(sess.stop)

	// Pre-seal the M inbound requests sequentially so initSend's nonce
	// advances on a single goroutine; they decrypt (binary recv) in this
	// same order.
	reqs := make([]protocol.RoutingEnvelope, mReq)
	for i := range reqs {
		reqs[i] = sealAppFrame(t, sess.initSend, protocol.Envelope{
			ID:      uint64(3000 + i),
			Type:    protocol.TypeListConversations,
			TS:      time.Now().UTC(),
			Payload: json.RawMessage(`{}`),
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	done := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			for _, c := range sess.mgr.ActiveConns(ctx) {
				if c.ConnID != v2TestConnID {
					t.Errorf("snapshot contained unexpected id %q", c.ConnID)
				}
			}
		}
	}()

	for _, r := range reqs {
		frames <- r
	}
	// All M replies (+ the noise_resp) captured ⇒ inbound dispatch drained.
	envs := waitForEnvelopes(t, sess.rec, 1+mReq)
	close(done)
	wg.Wait()

	// The solicited replies still decrypt in capture order — nonce integrity
	// intact, session state never corrupted by the concurrent snapshots.
	for _, e := range envs[1:] {
		inner := decryptAppFrame(t, e, sess.initRecv)
		if inner.Type != protocol.TypeConversations {
			t.Errorf("reply Type = %q, want %q", inner.Type, protocol.TypeConversations)
		}
	}
}

// TestV2Session_ActiveConns_EmptyManager pins the empty case: a started
// manager with zero sessions returns a len-0 slice without blocking.
func TestV2Session_ActiveConns_EmptyManager(t *testing.T) {
	t.Parallel()

	respPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)
	frames := make(chan protocol.RoutingEnvelope)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   rec.outbound,
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
	})
	t.Cleanup(stop)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if got := mgr.ActiveConns(ctx); len(got) != 0 {
		t.Errorf("ActiveConns() on empty manager = %v, want len 0", got)
	}
}

// TestV2Session_ActiveConns_CtxCancelled_ReturnsNil pins the
// ctx-cancellation arm: a call whose ctx is already cancelled returns nil
// without blocking. With no Run goroutine draining m.snapshot, the send case
// can never proceed, so the ctx.Done arm is the only ready case —
// deterministic, no flakiness from select picking the send.
func TestV2Session_ActiveConns_CtxCancelled_ReturnsNil(t *testing.T) {
	t.Parallel()

	respPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)
	mgr, err := NewV2SessionManager(V2SessionConfig{
		Frames:     make(chan protocol.RoutingEnvelope),
		Outbound:   (&v2Recorder{}).outbound,
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
	})
	if err != nil {
		t.Fatalf("NewV2SessionManager: %v", err)
	}
	// Intentionally do NOT start Run: m.snapshot has no receiver.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if got := mgr.ActiveConns(ctx); got != nil {
		t.Errorf("ActiveConns with cancelled ctx = %v, want nil", got)
	}
}

// TestV2Session_ActiveConns_MixedInteractive pins the capability-aware
// enumeration on a mixed map: an interactive open conn, a non-interactive open
// conn, and a flagged-but-still-handshaking conn. ActiveConns reports the
// correct flag per open conn and excludes the non-open one (the V2StateOpen
// gate wins over the interactive flag — belt-and-suspenders of different
// fabric). White-box injection mirrors TestV2Session_ActiveConns_OpenOnly;
// the snapshot channel-send is the happens-before edge publishing the writes
// to Run.
func TestV2Session_ActiveConns_MixedInteractive(t *testing.T) {
	t.Parallel()

	respPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)
	frames := make(chan protocol.RoutingEnvelope)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   rec.outbound,
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
	})
	t.Cleanup(stop)

	mgr.sessions["c-int"] = &V2Session{connID: "c-int", state: V2StateOpen, interactive: true}
	mgr.sessions["c-plain"] = &V2Session{connID: "c-plain", state: V2StateOpen, interactive: false}
	mgr.sessions["c-flagged-handshake"] = &V2Session{connID: "c-flagged-handshake", state: V2StateHandshakeComplete, interactive: true}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	got := map[string]bool{}
	for _, c := range mgr.ActiveConns(ctx) {
		got[c.ConnID] = c.Interactive
	}
	if len(got) != 2 || !got["c-int"] || got["c-plain"] {
		t.Errorf("ActiveConns flags = %v, want {c-int:true, c-plain:false} (open only)", got)
	}
}
