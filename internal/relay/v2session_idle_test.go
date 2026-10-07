package relay

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// --- idle-sweep teardown tests (#774) ---
//
// These tests override the package-level idleTimeout to a sub-second value via
// a save/restore + t.Cleanup, mirroring
// TestV2Session_RekeyInitiator_Emit_ReArmViaResponder. They are deliberately
// NOT t.Parallel: idleTimeout is a package var read by the dispatch goroutine
// of every open session at arm time.

// setIdleTimeout overrides the package-level idleTimeout for the test and
// restores it on cleanup. Call it BEFORE startManager/driveToOpen so the
// cleanup (LIFO) restores only after the manager's Run goroutine has stopped —
// no dispatch goroutine ever reads a half-updated value.
func setIdleTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := idleTimeout
	idleTimeout = d
	t.Cleanup(func() { idleTimeout = prev })
}

// waitForConnClose polls rec until an envelope addressed to connID carrying the
// given WS close code appears, returning it. Unlike waitForEnvelopes it keys on
// (conn_id, close_code) so churn tests spanning many conns can wait for one
// conn's teardown regardless of interleaving.
func waitForConnClose(t *testing.T, rec *v2Recorder, connID string, code uint16) protocol.RoutingEnvelope {
	t.Helper()
	return waitForConnCloseBy(t, rec, connID, code, 2*time.Second)
}

// waitForConnCloseBy is waitForConnClose with a caller-chosen deadline, for the
// near-ceiling bundle fixtures whose teardown trails ~32 MiB of enqueue work
// (#1505).
func waitForConnCloseBy(t *testing.T, rec *v2Recorder, connID string, code uint16, within time.Duration) protocol.RoutingEnvelope {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		for _, e := range rec.snapshot() {
			if e.ConnID == connID && e.CloseCode == code {
				return e
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("waitForConnClose: no envelope conn=%q close_code=%d within %v; captured %d envelopes",
		connID, code, within, len(rec.snapshot()))
	return protocol.RoutingEnvelope{}
}

// handshakeConnToOpen drives a paired-device Noise_IK handshake for connID
// through an already-running manager's Frames channel and blocks until that
// conn's noise_resp is recorded (which the manager emits only on the success
// path that reaches V2StateOpen). Unlike driveToOpen it starts no manager of
// its own, so churn tests can open many conns on one manager.
func handshakeConnToOpen(t *testing.T, frames chan protocol.RoutingEnvelope, rec *v2Recorder, connID string, respPub, initPriv []byte) {
	t.Helper()
	initiator, err := noise.NewInitiator(initPriv, respPub)
	if err != nil {
		t.Fatalf("NewInitiator(%s): %v", connID, err)
	}
	initMsg, err := initiator.WriteInit(buildHelloEarlyData(t, v2TestToken))
	if err != nil {
		t.Fatalf("WriteInit(%s): %v", connID, err)
	}
	frames <- wrapInnerFrame(t, connID, protocol.TypeNoiseInit, initMsg)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, e := range rec.snapshot() {
			if e.ConnID == connID && e.CloseCode == 0 && e.Frame != nil {
				return
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("handshakeConnToOpen(%s): no noise_resp recorded within deadline", connID)
}

// TestV2Session_IdleTeardown_FullTeardown pins AC-1: an open session that
// receives no inbound frame within idleTimeout is torn down through closeWith —
// a 4408 close-only envelope is emitted and the session plus its push queue are
// removed from the manager's maps. Because idleTimeout (60ms) is far short of
// the unchanged 1-hour rekeyInterval, the idle sweep pre-empts the scheduled
// rekey: the second envelope is the 4408 close, never a rekey_request.
func TestV2Session_IdleTeardown_FullTeardown(t *testing.T) {
	setIdleTimeout(t, 60*time.Millisecond)

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)
	frames := make(chan protocol.RoutingEnvelope, 8)
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

	closeEnv := waitForConnClose(t, sess.rec, v2TestConnID, uint16(StatusIdleTimeout))
	if closeEnv.Frame != nil {
		t.Errorf("idle close Frame = %s, want nil (close-only at 4408)", string(closeEnv.Frame))
	}
	// Exactly two envelopes total: the initial noise_resp and the idle close.
	// No rekey_request slipped in before the sweep.
	if envs := sess.rec.snapshot(); len(envs) != 2 {
		t.Fatalf("recorded %d envelopes, want 2 (noise_resp + 4408 close)", len(envs))
	}

	// Full teardown: session + push queue removed from the manager's maps (the
	// session struct becoming unreferenced is what drops its two CipherStates).
	// Stop Run first so the map reads are race-free.
	sess.stop()
	if _, ok := sess.mgr.sessions[v2TestConnID]; ok {
		t.Errorf("sessions[%q] still present after idle sweep; closeWith should have deleted it", v2TestConnID)
	}
	sess.mgr.pushMu.Lock()
	_, qok := sess.mgr.queues[v2TestConnID]
	sess.mgr.pushMu.Unlock()
	if qok {
		t.Errorf("queues[%q] still present after idle sweep; closeWith should have deleted it", v2TestConnID)
	}
}

// TestV2Session_IdleActivity_ReArmsNoPrematureTeardown exercises the
// idle < idleTimeout reschedule branch: a phone sending inbound frames at
// intervals shorter than idleTimeout keeps re-stamping lastActivityAt, so the
// idle timer reschedules instead of tearing the session down — even across a
// total active span longer than idleTimeout. Once the phone stops, the sweep
// fires idleTimeout past the last frame. The activity frames are sealed
// `interrupt` control frames; the session is non-interactive (no advertised
// caps) so each interrupt is inert (no outbound), leaving the noise_resp as the
// only recorded envelope during the active span.
func TestV2Session_IdleActivity_ReArmsNoPrematureTeardown(t *testing.T) {
	setIdleTimeout(t, 200*time.Millisecond)

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)
	frames := make(chan protocol.RoutingEnvelope, 8)
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

	// 8 frames × 40ms = 320ms of activity, comfortably longer than the 200ms
	// idle window: a non-rescheduling implementation would tear down mid-span.
	for i := 0; i < 8; i++ {
		time.Sleep(40 * time.Millisecond)
		env := protocol.Envelope{ID: 1, Type: protocol.TypeInterrupt, TS: time.Now().UTC()}
		sess.frames <- sealAppFrame(t, sess.initSend, env)
		// Inert interrupt → no outbound; a premature 4408 would show up here as
		// a second envelope.
		if envs := sess.rec.snapshot(); len(envs) != 1 {
			t.Fatalf("iter %d during active span: recorded %d envelopes, want 1 (noise_resp only — no premature idle teardown)",
				i, len(envs))
		}
	}

	// Sends stopped: the timer now runs out idleTimeout past the last frame and
	// the teardown appears.
	closeEnv := waitForConnClose(t, sess.rec, v2TestConnID, uint16(StatusIdleTimeout))
	if closeEnv.Frame != nil {
		t.Errorf("idle close Frame = %s, want nil (close-only at 4408)", string(closeEnv.Frame))
	}
}

// TestV2Session_IdleChurn_ReturnsToBaseline pins AC-3: under repeated
// connect → handshake → go-idle → sweep on distinct conn_ids, the live-session
// count peaks at one and returns to baseline (zero) after each idle window —
// stale encrypted sessions do not accumulate.
func TestV2Session_IdleChurn_ReturnsToBaseline(t *testing.T) {
	setIdleTimeout(t, 150*time.Millisecond)

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
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
	})
	t.Cleanup(stop)

	bg := context.Background()
	connIDs := []string{"c-churn-0", "c-churn-1", "c-churn-2", "c-churn-3", "c-churn-4"}
	for _, connID := range connIDs {
		handshakeConnToOpen(t, frames, rec, connID, respPub, initPriv)
		// Freshly handshaked and the ONLY open conn — every prior round was
		// swept and deleted, so the count never climbs above one.
		if conns := mgr.ActiveConns(bg); len(conns) != 1 || conns[0].ConnID != connID {
			t.Fatalf("after handshake %s: ActiveConns = %v, want exactly one open conn %s (stale sessions accumulated)", connID, conns, connID)
		}
		waitForConnClose(t, rec, connID, uint16(StatusIdleTimeout))
		if conns := mgr.ActiveConns(bg); len(conns) != 0 {
			t.Fatalf("after idle sweep of %s: ActiveConns = %v, want [] (baseline)", connID, conns)
		}
	}
}

// TestV2Session_PeerClose_TearsDownWithoutReply pins #2601: the relay's close
// notice for a phone whose WebSocket ended ({conn_id, close_code}) tears that
// conn's session down at once, leaves every other conn open, and publishes
// nothing — the phone is already gone. The junk frame on the notice would earn
// a 4421 close if it were decoded, so "nothing recorded" also proves it is not.
// Frames is unbuffered so each send is serviced by Run before the next
// ActiveConns snapshot.
func TestV2Session_PeerClose_TearsDownWithoutReply(t *testing.T) {
	setIdleTimeout(t, 100*time.Millisecond)

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
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

	bg := context.Background()
	handshakeConnToOpen(t, frames, rec, "c-gone", respPub, initPriv)
	handshakeConnToOpen(t, frames, rec, "c-stays", respPub, initPriv)

	frames <- protocol.RoutingEnvelope{
		ConnID:    "c-gone",
		Frame:     json.RawMessage(`"not an inner frame"`),
		CloseCode: 1001,
	}
	conns := mgr.ActiveConns(bg)
	if len(conns) != 1 || conns[0].ConnID != "c-stays" {
		t.Fatalf("after close notice: ActiveConns = %v, want exactly [c-stays]", conns)
	}
	if envs := rec.snapshot(); len(envs) != 2 {
		t.Fatalf("recorded %d envelopes, want 2 (the two noise_resps; the close notice publishes nothing)", len(envs))
	}

	// Past the idle window: the torn-down conn's idle timer must not surface a
	// late 4408 for it. The surviving conn's own sweep is allowed.
	time.Sleep(250 * time.Millisecond)
	for _, e := range rec.snapshot() {
		if e.ConnID == "c-gone" && e.CloseCode != 0 {
			t.Errorf("close envelope published for c-gone (close_code %d); want none", e.CloseCode)
		}
	}

	stop()
	if _, ok := mgr.sessions["c-gone"]; ok {
		t.Errorf("sessions[c-gone] still present after close notice")
	}
	mgr.pushMu.Lock()
	_, qok := mgr.queues["c-gone"]
	mgr.pushMu.Unlock()
	if qok {
		t.Errorf("queues[c-gone] still present after close notice")
	}
}

// TestV2Session_PeerClose_UnknownConnIgnored pins #2601: a close notice for a
// conn_id with no session creates none and sends nothing.
func TestV2Session_PeerClose_UnknownConnIgnored(t *testing.T) {
	respPriv, _ := genV2Keypair(t)
	frames := make(chan protocol.RoutingEnvelope)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   rec.outbound,
		StaticPriv: respPriv,
		Devices:    v2PairedRegistry(t, v2TestToken),
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
	})
	t.Cleanup(stop)

	frames <- protocol.RoutingEnvelope{ConnID: "c-unknown", CloseCode: 1006}
	if conns := mgr.ActiveConns(context.Background()); len(conns) != 0 {
		t.Fatalf("ActiveConns = %v, want []", conns)
	}
	if envs := rec.snapshot(); len(envs) != 0 {
		t.Fatalf("recorded %d envelopes, want 0", len(envs))
	}
	stop()
	if _, ok := mgr.sessions["c-unknown"]; ok {
		t.Errorf("sessions[c-unknown] created by a close notice")
	}
}

// TestV2Session_SweptThenNoiseMsg_4410_ThenReHandshakes pins #2488: a client
// that still holds cipher state for a session the daemon has dropped sends its
// next sealed frame into V2StateAwaitingInit, and must get the retryable 4410
// rather than the fatal 4421 — a client that stops re-dialling on 4421 never
// reaches the fresh handshake the spec promises as the recovery. The idle sweep
// is the reachable stand-in for all three causes (restart, sweep, push-queue
// overflow): closeWith deletes the conn either way, so they reduce to one conn
// id the manager no longer holds. The second conn proves the manager keeps
// serving handshakes after the reject, i.e. the 4410 tore down only that conn.
func TestV2Session_SweptThenNoiseMsg_4410_ThenReHandshakes(t *testing.T) {
	setIdleTimeout(t, 120*time.Millisecond)

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)
	frames := make(chan protocol.RoutingEnvelope, 8)
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

	// Leg 1: the daemon drops the session under the client's feet.
	waitForConnClose(t, sess.rec, v2TestConnID, uint16(StatusIdleTimeout))

	// Leg 2: the client, which never saw the close, seals its next frame under
	// the CipherState it still holds and sends it on the same conn id. The
	// manager holds no session for that conn, so handleFrame creates a fresh
	// V2StateAwaitingInit one and handleNoiseMsg rejects close-only.
	env := protocol.Envelope{ID: 1, Type: protocol.TypeInterrupt, TS: time.Now().UTC()}
	sess.frames <- sealAppFrame(t, sess.initSend, env)

	closeEnv := waitForConnClose(t, sess.rec, v2TestConnID, uint16(StatusSessionGone))
	if closeEnv.Frame != nil {
		t.Errorf("4410 close Frame = %s, want nil (no CipherStates exist, so nothing can be sealed)", string(closeEnv.Frame))
	}

	// Leg 3: the recovery the retryable code buys — a fresh handshake on a new
	// conn id against the same still-running manager.
	handshakeConnToOpen(t, frames, rec, "c-2488-rehandshake", respPub, initPriv)
}

// TestV2Session_SweptThenReconnect_ReHandshakes pins AC-3's reconnect clause: a
// phone that returns after its session was swept re-handshakes cleanly via the
// normal lazy session-create path — no stuck or half-torn-down state. A fresh
// noise_init on the same conn_id after the 4408 sweep must complete the IK
// handshake and yield a valid hello_ack.
func TestV2Session_SweptThenReconnect_ReHandshakes(t *testing.T) {
	setIdleTimeout(t, 120*time.Millisecond)

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)
	frames := make(chan protocol.RoutingEnvelope, 8)
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

	// Let the first session go idle and be swept before reconnecting.
	waitForConnClose(t, sess.rec, v2TestConnID, uint16(StatusIdleTimeout))

	initiator2, err := noise.NewInitiator(initPriv, respPub)
	if err != nil {
		t.Fatalf("NewInitiator (reconnect): %v", err)
	}
	initMsg2, err := initiator2.WriteInit(buildHelloEarlyData(t, v2TestToken))
	if err != nil {
		t.Fatalf("WriteInit (reconnect): %v", err)
	}
	frames <- wrapInnerFrame(t, v2TestConnID, protocol.TypeNoiseInit, initMsg2)

	// Envelope ordering is deterministic: [noise_resp #1, 4408 close, noise_resp
	// #2]. The 4408 was already recorded (waited on above) before init #2 was
	// sent, and rekeyInterval is the unchanged 1h so no rekey_request intrudes.
	envs := waitForEnvelopes(t, sess.rec, 3)
	resp2 := envs[2]
	if resp2.CloseCode != 0 {
		t.Fatalf("reconnect noise_resp CloseCode = %d, want 0 (clean re-handshake)", resp2.CloseCode)
	}
	respRaw := decodeRespFrame(t, resp2)
	earlyAck, _, _, err := initiator2.ReadResp(respRaw)
	if err != nil {
		t.Fatalf("reconnect ReadResp: %v (re-handshake did not complete cleanly)", err)
	}
	if ack := decodeHelloAck(t, earlyAck); ack.ProtocolVersion != "v2" || ack.ConnID != v2TestConnID {
		t.Errorf("reconnect hello_ack = {ver:%q conn:%q}, want {v2 %q}", ack.ProtocolVersion, ack.ConnID, v2TestConnID)
	}
}
