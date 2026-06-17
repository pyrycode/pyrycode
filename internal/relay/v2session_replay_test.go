package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/eventring"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// --- #647 mid-turn-reconnect replay fixtures ---

const v2TestConvID = "conv-replay-A"

// buildHelloEarlyDataReplay marshals a v2 hello carrying last_event_id (a
// reconnecting phone's advertised position) for embedding in IK message 1
// early-data. A nil lastEventID drops the key via omitempty — the absent-field
// AC-1 shape. Mirrors buildHelloEarlyDataCaps otherwise (paired token, no caps).
func buildHelloEarlyDataReplay(t *testing.T, token string, lastEventID *uint64) []byte {
	t.Helper()
	payload, err := json.Marshal(protocol.HelloClientPayload{
		Role:             "client",
		DeviceName:       v2TestDevName,
		ClientVersion:    "v2-test",
		ProtocolVersions: []string{"v2"},
		Token:            token,
		LastEventID:      lastEventID,
	})
	if err != nil {
		t.Fatalf("marshal hello payload: %v", err)
	}
	envBytes, err := json.Marshal(protocol.Envelope{
		ID:      1,
		Type:    protocol.TypeHello,
		TS:      time.Now().UTC(),
		Payload: payload,
	})
	if err != nil {
		t.Fatalf("marshal hello envelope: %v", err)
	}
	return envBytes
}

// appendRingEvents appends n events of type typ for convID, assigning ids
// 1..n. Each payload is {"n":<id>} so a replay frame can be matched back to
// the event it replays.
func appendRingEvents(r *eventring.Ring, convID, typ string, n int) {
	for i := 1; i <= n; i++ {
		r.Append(convID, typ, json.RawMessage(fmt.Sprintf(`{"n":%d}`, i)), time.Now().UTC())
	}
}

// waitConnOpen polls mgr.ActiveConns until connID is enumerated (V2StateOpen)
// or the deadline expires. Because the replay runs inline at the tail of
// handleNoiseInit BEFORE Run returns to its select (and ActiveConns funnels
// onto that same Run goroutine), once connID appears the whole replay/resync
// has already been forwarded and recorded — so a snapshot taken afterward is
// final, with no waitForEnvelopes race.
func waitConnOpen(t *testing.T, mgr *V2SessionManager, connID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		for _, c := range mgr.ActiveConns(ctx) {
			if c.ConnID == connID {
				return
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("conn %q did not reach open within deadline", connID)
		case <-time.After(2 * time.Millisecond):
		}
	}
}

// reconnectScenario drives one mid-turn-reconnect handshake end-to-end and
// returns the binary→phone frames forwarded AFTER the noise_resp, decrypted in
// order. The manager is built from respPriv with a registry paired for
// v2TestToken; when ring != nil it is published as the replay source with
// cursor (a nil ring leaves replay disabled — never calling SetReplaySource).
// The hello advertises lastEventID (nil ⇒ key absent). It asserts exactly
// wantEnvs routing envelopes were emitted (1 noise_resp + the replay/resync
// frames) and that the noise_resp carried no close code.
func reconnectScenario(t *testing.T, respPriv, respPub, initPriv []byte, ring *eventring.Ring, cursor func() string, lastEventID *uint64, wantEnvs int) []protocol.Envelope {
	t.Helper()
	frames := make(chan protocol.RoutingEnvelope, 1)
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
	if ring != nil {
		mgr.SetReplaySource(ring, cursor)
	}

	initiator, err := noise.NewInitiator(initPriv, respPub)
	if err != nil {
		t.Fatalf("NewInitiator: %v", err)
	}
	initMsg, err := initiator.WriteInit(buildHelloEarlyDataReplay(t, v2TestToken, lastEventID))
	if err != nil {
		t.Fatalf("WriteInit: %v", err)
	}
	frames <- wrapInnerFrame(t, v2TestConnID, protocol.TypeNoiseInit, initMsg)

	waitConnOpen(t, mgr, v2TestConnID)
	envs := rec.snapshot()
	if len(envs) != wantEnvs {
		t.Fatalf("envelope count: got %d, want %d", len(envs), wantEnvs)
	}
	respRaw := decodeRespFrame(t, envs[0])
	if envs[0].CloseCode != 0 {
		t.Errorf("noise_resp emitted close_code=%d, want 0", envs[0].CloseCode)
	}
	_, _, initRecv, err := initiator.ReadResp(respRaw)
	if err != nil {
		t.Fatalf("ReadResp: %v", err)
	}
	forwarded := make([]protocol.Envelope, 0, len(envs)-1)
	for _, env := range envs[1:] {
		forwarded = append(forwarded, decryptAppFrame(t, env, initRecv))
	}
	return forwarded
}

// --- tests ---

// TestV2Session_Reconnect_ReplaysMissedTail is AC-2: a phone reconnecting with
// an in-ring last_event_id receives exactly the conversation's events with id >
// last_event_id, ascending, each carrying its durable event_id, on its own
// connection — nothing skipped, nothing duplicated.
func TestV2Session_Reconnect_ReplaysMissedTail(t *testing.T) {
	t.Parallel()
	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)

	ring := eventring.New(eventring.MaxEventsPerConversation)
	appendRingEvents(ring, v2TestConvID, protocol.TypeTurnState, 5)
	cursor := func() string { return v2TestConvID }

	last := uint64(2)
	// noise_resp + events 3,4,5 == 4 envelopes.
	forwarded := reconnectScenario(t, respPriv, respPub, initPriv, ring, cursor, &last, 4)

	if len(forwarded) != 3 {
		t.Fatalf("replay frames: got %d, want 3 (events 3,4,5)", len(forwarded))
	}
	for i, ev := range forwarded {
		wantID := uint64(i + 3) // 3,4,5 ascending
		if ev.Type != protocol.TypeTurnState {
			t.Errorf("frame %d type = %q, want %q", i, ev.Type, protocol.TypeTurnState)
		}
		if ev.ID != wantID {
			t.Errorf("frame %d env.ID = %d, want %d", i, ev.ID, wantID)
		}
		if ev.EventID == nil || *ev.EventID != wantID {
			t.Errorf("frame %d EventID = %v, want pointer to %d", i, ev.EventID, wantID)
		}
		if want := fmt.Sprintf(`{"n":%d}`, wantID); string(ev.Payload) != want {
			t.Errorf("frame %d payload = %s, want %s", i, ev.Payload, want)
		}
	}
}

// TestV2Session_Reconnect_CaughtUp_NoReplay is AC-3 + AC-5: a last_event_id at
// or beyond the newest event — including a hostile-large value — produces no
// replay frames and no panic. Replay is idempotent: re-advertising the same
// position never re-sends.
func TestV2Session_Reconnect_CaughtUp_NoReplay(t *testing.T) {
	t.Parallel()
	cursor := func() string { return v2TestConvID }

	cases := []struct {
		name string
		last uint64
	}{
		{"at-newest", 5},
		{"beyond-newest", 99},
		{"hostile-max-uint64", math.MaxUint64},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			respPriv, respPub := genV2Keypair(t)
			initPriv, _ := genV2Keypair(t)
			ring := eventring.New(eventring.MaxEventsPerConversation)
			appendRingEvents(ring, v2TestConvID, protocol.TypeTurnState, 5)

			last := tc.last
			forwarded := reconnectScenario(t, respPriv, respPub, initPriv, ring, cursor, &last, 1)
			if len(forwarded) != 0 {
				t.Errorf("caught-up last_event_id=%d: got %d replay frames, want 0", tc.last, len(forwarded))
			}
		})
	}
}

// TestV2Session_Reconnect_Gap_EmitsResync is AC-4: when last_event_id predates
// the oldest retained event, the daemon emits a single resync marker for the
// conversation (not a partial, gap-ful replay) so the phone is never left with
// a silent gap.
func TestV2Session_Reconnect_Gap_EmitsResync(t *testing.T) {
	t.Parallel()
	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)

	// Cap 2 + 5 appends evicts ids 1,2,3; oldest retained is 4. With
	// last_event_id=2, the next-expected event (3) fell off the back → gap.
	ring := eventring.New(2)
	appendRingEvents(ring, v2TestConvID, protocol.TypeTurnState, 5)
	cursor := func() string { return v2TestConvID }

	last := uint64(2)
	// noise_resp + one resync == 2 envelopes.
	forwarded := reconnectScenario(t, respPriv, respPub, initPriv, ring, cursor, &last, 2)

	if len(forwarded) != 1 {
		t.Fatalf("gap reply: got %d frames, want 1 (a single resync)", len(forwarded))
	}
	marker := forwarded[0]
	if marker.Type != protocol.TypeResync {
		t.Errorf("marker type = %q, want %q", marker.Type, protocol.TypeResync)
	}
	if marker.EventID != nil {
		t.Errorf("resync marker carried EventID = %v, want nil (not a structured event)", marker.EventID)
	}
	var p struct {
		ConversationID string `json:"conversation_id"`
	}
	if err := json.Unmarshal(marker.Payload, &p); err != nil {
		t.Fatalf("decode resync payload: %v", err)
	}
	if p.ConversationID != v2TestConvID {
		t.Errorf("resync conversation_id = %q, want %q", p.ConversationID, v2TestConvID)
	}
}

// TestV2Session_Reconnect_AbsentLastEventID_NoReplay is AC-1: a phone that
// advertises no last_event_id receives no replay — just the normal live
// stream — and the session opens.
func TestV2Session_Reconnect_AbsentLastEventID_NoReplay(t *testing.T) {
	t.Parallel()
	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)

	ring := eventring.New(eventring.MaxEventsPerConversation)
	appendRingEvents(ring, v2TestConvID, protocol.TypeTurnState, 5)
	cursor := func() string { return v2TestConvID }

	forwarded := reconnectScenario(t, respPriv, respPub, initPriv, ring, cursor, nil, 1)
	if len(forwarded) != 0 {
		t.Errorf("absent last_event_id: got %d frames, want 0", len(forwarded))
	}
}

// TestV2Session_Reconnect_ScopedToCursorConversation is AC-5: replay is scoped
// to the daemon-resolved conversation, never one the phone names. With the
// cursor pointing at conversation B while the ring holds only conversation A,
// no A events surface — the phone gets a resync for B instead.
func TestV2Session_Reconnect_ScopedToCursorConversation(t *testing.T) {
	t.Parallel()
	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)

	ring := eventring.New(eventring.MaxEventsPerConversation)
	appendRingEvents(ring, "conv-A", protocol.TypeTurnState, 5)
	// The daemon resolves the conversation; the phone cannot choose it.
	cursor := func() string { return "conv-B" }

	last := uint64(2)
	// After("conv-B", 2): unknown conversation, afterID>0 → gap → one resync.
	forwarded := reconnectScenario(t, respPriv, respPub, initPriv, ring, cursor, &last, 2)

	if len(forwarded) != 1 {
		t.Fatalf("scoped reply: got %d frames, want 1 (resync for B)", len(forwarded))
	}
	if forwarded[0].Type != protocol.TypeResync {
		t.Errorf("frame type = %q, want %q", forwarded[0].Type, protocol.TypeResync)
	}
	// No structured event (no conv-A content) was surfaced.
	for i, ev := range forwarded {
		if ev.EventID != nil {
			t.Errorf("frame %d carried EventID = %v; a foreign conversation's event leaked", i, ev.EventID)
		}
	}
	var p struct {
		ConversationID string `json:"conversation_id"`
	}
	if err := json.Unmarshal(forwarded[0].Payload, &p); err != nil {
		t.Fatalf("decode resync payload: %v", err)
	}
	if p.ConversationID != "conv-B" {
		t.Errorf("resync conversation_id = %q, want %q (the daemon-resolved id)", p.ConversationID, "conv-B")
	}
}

// TestV2Session_Reconnect_ReplayDisabled_NoReplay: with no replay source ever
// published (the structured stream off), a hello carrying last_event_id yields
// no replay and no resync — the phone simply gets the live stream.
func TestV2Session_Reconnect_ReplayDisabled_NoReplay(t *testing.T) {
	t.Parallel()
	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)

	last := uint64(2)
	// ring == nil ⇒ reconnectScenario never calls SetReplaySource.
	forwarded := reconnectScenario(t, respPriv, respPub, initPriv, nil, nil, &last, 1)
	if len(forwarded) != 0 {
		t.Errorf("replay disabled: got %d frames, want 0", len(forwarded))
	}
}

// TestV2Session_Reconnect_OtherConnsUnaffected is AC-2's "other connected
// phones are unaffected": a second open conn that advertised no last_event_id
// receives nothing from the reconnecting conn's replay (replay is addressed to
// the reconnecting conn only).
func TestV2Session_Reconnect_OtherConnsUnaffected(t *testing.T) {
	t.Parallel()
	respPriv, respPub := genV2Keypair(t)
	initPrivA, _ := genV2Keypair(t)
	initPrivB, _ := genV2Keypair(t)
	const connA, connB = "c-v2-A", "c-v2-B"

	ring := eventring.New(eventring.MaxEventsPerConversation)
	appendRingEvents(ring, v2TestConvID, protocol.TypeTurnState, 5)

	frames := make(chan protocol.RoutingEnvelope, 2)
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
	mgr.SetReplaySource(ring, func() string { return v2TestConvID })

	// Conn B opens with NO last_event_id → no replay; just its noise_resp.
	initB, err := noise.NewInitiator(initPrivB, respPub)
	if err != nil {
		t.Fatalf("NewInitiator B: %v", err)
	}
	initMsgB, err := initB.WriteInit(buildHelloEarlyDataReplay(t, v2TestToken, nil))
	if err != nil {
		t.Fatalf("WriteInit B: %v", err)
	}
	frames <- wrapInnerFrame(t, connB, protocol.TypeNoiseInit, initMsgB)
	waitConnOpen(t, mgr, connB)

	// Conn A reconnects with last_event_id=2 → replays 3,4,5 to A only.
	initA, err := noise.NewInitiator(initPrivA, respPub)
	if err != nil {
		t.Fatalf("NewInitiator A: %v", err)
	}
	last := uint64(2)
	initMsgA, err := initA.WriteInit(buildHelloEarlyDataReplay(t, v2TestToken, &last))
	if err != nil {
		t.Fatalf("WriteInit A: %v", err)
	}
	frames <- wrapInnerFrame(t, connA, protocol.TypeNoiseInit, initMsgA)
	waitConnOpen(t, mgr, connA)

	counts := map[string]int{}
	for _, env := range rec.snapshot() {
		counts[env.ConnID]++
	}
	if counts[connB] != 1 {
		t.Errorf("connB received %d frames, want 1 (noise_resp only); the replay leaked to it", counts[connB])
	}
	if counts[connA] != 4 {
		t.Errorf("connA received %d frames, want 4 (noise_resp + 3 replay)", counts[connA])
	}
}

// reconnectOpenLive drives a mid-turn-reconnect handshake to open with replay
// enabled and keeps the manager alive so the caller can mgr.Push live frames
// afterward (the OtherConnsUnaffected inline pattern; reconnectScenario discards
// the manager). It asserts exactly wantHandshake routing envelopes were recorded
// during the handshake (1 noise_resp + wantHandshake-1 replay frames), then
// decrypts each replay noise_msg to advance initRecv's nonce in lockstep with
// the responder's send chain so the live frames the caller pushes next decrypt
// cleanly. Returns the manager, recorder, and the initiator's recv CipherState;
// live frames the caller pushes land in the recorder at index >= wantHandshake.
func reconnectOpenLive(t *testing.T, ring *eventring.Ring, cursor func() string, lastEventID *uint64, wantHandshake int) (*V2SessionManager, *v2Recorder, *noise.CipherState) {
	t.Helper()
	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)

	frames := make(chan protocol.RoutingEnvelope, 1)
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
	mgr.SetReplaySource(ring, cursor)

	initiator, err := noise.NewInitiator(initPriv, respPub)
	if err != nil {
		t.Fatalf("NewInitiator: %v", err)
	}
	initMsg, err := initiator.WriteInit(buildHelloEarlyDataReplay(t, v2TestToken, lastEventID))
	if err != nil {
		t.Fatalf("WriteInit: %v", err)
	}
	frames <- wrapInnerFrame(t, v2TestConnID, protocol.TypeNoiseInit, initMsg)

	waitConnOpen(t, mgr, v2TestConnID)
	envs := rec.snapshot()
	if len(envs) != wantHandshake {
		t.Fatalf("handshake frames: got %d, want %d", len(envs), wantHandshake)
	}
	respRaw := decodeRespFrame(t, envs[0])
	_, _, initRecv, err := initiator.ReadResp(respRaw)
	if err != nil {
		t.Fatalf("ReadResp: %v", err)
	}
	// Decrypt the replay frames forwarded during the handshake so initRecv's
	// nonce stays aligned with the responder's send chain (the watermark guard
	// drops before seal, so dropped frames never gap the nonce).
	for _, env := range envs[1:] {
		decryptAppFrame(t, env, initRecv)
	}
	return mgr, rec, initRecv
}

// TestV2Session_Reconnect_OutOfRangeLastEventID_LiveStreamDelivered is AC-2 +
// AC-5: a last_event_id beyond the conversation's newest retained id — including
// a hostile math.MaxUint64 — is caught-up (no replay) AND must NOT suppress the
// subsequent live stream. Under the #663 bug replayMissed set replayThrough from
// the raw afterID, so the forwardEnvelope guard dropped every later live frame
// (id <= afterID) permanently; the clamp to min(afterID, NewestID) makes the
// watermark 5, so the next live event (id 6) flows. This is the precise gap the
// green #647 suite missed: it asserted "no replay frames", never live delivery.
func TestV2Session_Reconnect_OutOfRangeLastEventID_LiveStreamDelivered(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		last uint64
	}{
		{"plain-out-of-range", 99},
		{"hostile-max-uint64", math.MaxUint64},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ring := eventring.New(eventring.MaxEventsPerConversation)
			appendRingEvents(ring, v2TestConvID, protocol.TypeTurnState, 5) // newest id 5
			cursor := func() string { return v2TestConvID }

			last := tc.last
			mgr, rec, initRecv := reconnectOpenLive(t, ring, cursor, &last, 1)

			// The conversation's next live event (id 6 > newest 5). The bug drops
			// it (6 <= afterID); the clamp delivers it (6 > 5).
			id6 := uint64(6)
			live := protocol.Envelope{
				ID:      6,
				Type:    protocol.TypeTurnState,
				TS:      time.Now().UTC(),
				Payload: json.RawMessage(`{"n":6}`),
				EventID: &id6,
			}
			if err := mgr.Push(context.Background(), v2TestConnID, live); err != nil {
				t.Fatalf("Push live frame: %v", err)
			}
			envs := waitForEnvelopes(t, rec, 2) // noise_resp + the live frame
			got := decryptAppFrame(t, envs[1], initRecv)
			if got.EventID == nil || *got.EventID != 6 {
				t.Fatalf("live frame EventID = %v, want pointer to 6 (frame suppressed by the watermark)", got.EventID)
			}
			if string(got.Payload) != `{"n":6}` {
				t.Errorf("live frame payload = %s, want {\"n\":6}", got.Payload)
			}
		})
	}
}

// TestV2Session_Reconnect_ClearRotation_LiveStreamDelivered is AC-3: after a
// /clear rotates the active conversation to B (whose ring counter restarts low),
// a phone reconnecting with a stale higher last_event_id carried over from A
// must still receive B's live events (ids restarting at 1). cursor resolves to
// B; After(B, 100) is caught-up because B already holds events 1..3 (latest 3 <
// 100). Under the bug replayThrough = 100 dropped every B live frame <= 100;
// the clamp makes it 3, so B's next events (4, 5) flow.
//
// Critical: conv-B must be NON-EMPTY at reconnect. An empty conv-B would make
// After(B, 100) a gap (afterID > 0 on an unknown conversation), emitting a
// resync and leaving replayThrough == 0 — a path that already delivers live
// events even with the bug present, reproducing the green suite's false
// confidence. The caught-up branch is only reachable with a retained event.
func TestV2Session_Reconnect_ClearRotation_LiveStreamDelivered(t *testing.T) {
	t.Parallel()
	const convB = "conv-B"
	ring := eventring.New(eventring.MaxEventsPerConversation)
	appendRingEvents(ring, convB, protocol.TypeTurnState, 3) // B's newest id 3
	cursor := func() string { return convB }

	last := uint64(100) // stale id from the rotated-away conversation A
	mgr, rec, initRecv := reconnectOpenLive(t, ring, cursor, &last, 1)

	for _, id := range []uint64{4, 5} {
		eid := id
		live := protocol.Envelope{
			ID:      id,
			Type:    protocol.TypeTurnState,
			TS:      time.Now().UTC(),
			Payload: json.RawMessage(fmt.Sprintf(`{"n":%d}`, id)),
			EventID: &eid,
		}
		if err := mgr.Push(context.Background(), v2TestConnID, live); err != nil {
			t.Fatalf("Push live frame %d: %v", id, err)
		}
	}
	envs := waitForEnvelopes(t, rec, 3) // noise_resp + 2 live frames
	for i, wantID := range []uint64{4, 5} {
		got := decryptAppFrame(t, envs[i+1], initRecv)
		if got.EventID == nil || *got.EventID != wantID {
			t.Fatalf("live frame %d EventID = %v, want pointer to %d (B's live stream suppressed by stale watermark)", i, got.EventID, wantID)
		}
	}
}

// TestV2Session_Reconnect_SameConversation_DedupPreserved is AC-4: the clamp
// must not loosen the legitimate same-conversation dedup. A phone reconnects
// with an in-range last_event_id=2, replays events 3,4,5 (replayThrough ends at
// 5), then the live stream resumes: a re-delivered EventID=3 is still dropped
// (<= 5) while EventID=6 flows. This exercises the replay branch's clamp end to
// end (min(2,5)=2, loop advances to 5), proving real dedup is intact.
func TestV2Session_Reconnect_SameConversation_DedupPreserved(t *testing.T) {
	t.Parallel()
	ring := eventring.New(eventring.MaxEventsPerConversation)
	appendRingEvents(ring, v2TestConvID, protocol.TypeTurnState, 5)
	cursor := func() string { return v2TestConvID }

	last := uint64(2)
	// noise_resp + replay 3,4,5 == 4 handshake frames.
	mgr, rec, initRecv := reconnectOpenLive(t, ring, cursor, &last, 4)

	// A late re-delivery of an already-replayed event (id 3 <= replayThrough 5)
	// must be dropped; the next genuinely-new live event (id 6) must flow.
	id3, id6 := uint64(3), uint64(6)
	for _, eid := range []*uint64{&id3, &id6} {
		live := protocol.Envelope{
			ID:      *eid,
			Type:    protocol.TypeTurnState,
			TS:      time.Now().UTC(),
			Payload: json.RawMessage(fmt.Sprintf(`{"n":%d}`, *eid)),
			EventID: eid,
		}
		if err := mgr.Push(context.Background(), v2TestConnID, live); err != nil {
			t.Fatalf("Push live frame %d: %v", *eid, err)
		}
	}
	// Only id 6 survives the guard: 4 handshake + 1 live == 5 total. FIFO drain
	// processes id 3 (dropped) before id 6 (recorded), so the count is stable.
	envs := waitForEnvelopes(t, rec, 5)
	if len(envs) != 5 {
		t.Fatalf("frames after dedup: got %d, want 5 (id 3 must be dropped, id 6 delivered)", len(envs))
	}
	got := decryptAppFrame(t, envs[4], initRecv)
	if got.EventID == nil || *got.EventID != 6 {
		t.Fatalf("delivered live frame EventID = %v, want pointer to 6 (dedup dropped id 3, id 6 should flow)", got.EventID)
	}
}

// TestV2Session_ForwardEnvelope_ReplayWatermarkGuard pins the dedup mechanism
// in isolation: forwardEnvelope drops a live structured envelope whose EventID
// <= replayThrough, forwards one above it, and never drops an EventID==nil
// control envelope (snapshot/error/rekey/resync). Run is not started, so the
// session inject + direct forwardEnvelope calls are single-goroutine.
func TestV2Session_ForwardEnvelope_ReplayWatermarkGuard(t *testing.T) {
	t.Parallel()
	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)

	// Run a real handshake offline to obtain matched respSend / initRecv.
	initiator, err := noise.NewInitiator(initPriv, respPub)
	if err != nil {
		t.Fatalf("NewInitiator: %v", err)
	}
	responder, err := noise.NewResponder(respPriv)
	if err != nil {
		t.Fatalf("NewResponder: %v", err)
	}
	initMsg, err := initiator.WriteInit([]byte("{}"))
	if err != nil {
		t.Fatalf("WriteInit: %v", err)
	}
	if _, err := responder.ReadInit(initMsg); err != nil {
		t.Fatalf("ReadInit: %v", err)
	}
	respMsg, respSend, _, err := responder.WriteResp([]byte("{}"))
	if err != nil {
		t.Fatalf("WriteResp: %v", err)
	}
	_, _, initRecv, err := initiator.ReadResp(respMsg)
	if err != nil {
		t.Fatalf("ReadResp: %v", err)
	}

	rec := &v2Recorder{}
	mgr, err := NewV2SessionManager(V2SessionConfig{
		Frames:     make(chan protocol.RoutingEnvelope),
		Outbound:   rec.outbound,
		StaticPriv: respPriv,
		Devices:    &devices.Registry{},
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
	})
	if err != nil {
		t.Fatalf("NewV2SessionManager: %v", err)
	}
	mgr.sessions[v2TestConnID] = &V2Session{
		connID:        v2TestConnID,
		state:         V2StateOpen,
		send:          respSend,
		replayThrough: 5,
	}
	ctx := context.Background()

	mk := func(eventID *uint64, typ string) protocol.Envelope {
		return protocol.Envelope{
			ID:      1,
			Type:    typ,
			TS:      time.Now().UTC(),
			Payload: json.RawMessage(`{}`),
			EventID: eventID,
		}
	}
	id5, id6 := uint64(5), uint64(6)

	// EventID == replayThrough → dropped (no frame, no error).
	if err := mgr.forwardEnvelope(ctx, v2TestConnID, mk(&id5, protocol.TypeAssistantDelta)); err != nil {
		t.Fatalf("forwardEnvelope(EventID=5): %v", err)
	}
	if n := len(rec.snapshot()); n != 0 {
		t.Fatalf("EventID=5 (<= replayThrough=5): got %d frames, want 0 (dropped)", n)
	}

	// EventID just above the watermark → forwarded.
	if err := mgr.forwardEnvelope(ctx, v2TestConnID, mk(&id6, protocol.TypeAssistantDelta)); err != nil {
		t.Fatalf("forwardEnvelope(EventID=6): %v", err)
	}
	// EventID == nil control (a resync) → never dropped.
	if err := mgr.forwardEnvelope(ctx, v2TestConnID, mk(nil, protocol.TypeResync)); err != nil {
		t.Fatalf("forwardEnvelope(EventID=nil): %v", err)
	}

	envs := rec.snapshot()
	if len(envs) != 2 {
		t.Fatalf("after EventID=6 + nil control: got %d frames, want 2", len(envs))
	}
	got6 := decryptAppFrame(t, envs[0], initRecv)
	if got6.EventID == nil || *got6.EventID != 6 {
		t.Errorf("frame 0 EventID = %v, want pointer to 6", got6.EventID)
	}
	gotCtl := decryptAppFrame(t, envs[1], initRecv)
	if gotCtl.Type != protocol.TypeResync {
		t.Errorf("frame 1 type = %q, want %q", gotCtl.Type, protocol.TypeResync)
	}
	if gotCtl.EventID != nil {
		t.Errorf("frame 1 EventID = %v, want nil (control envelope never dropped)", gotCtl.EventID)
	}
}
