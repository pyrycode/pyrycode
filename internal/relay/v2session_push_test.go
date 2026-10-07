package relay

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// sleepUntil blocks until t (or returns immediately if t is in the
// past). Helper for the manual-rekey timer-rebase test's wall-clock
// boundary checks.
func sleepUntil(t time.Time) {
	d := time.Until(t)
	if d > 0 {
		time.Sleep(d)
	}
}

// --- unsolicited push surface tests (#571) ---

// buildMessageEnvelope constructs a fully-formed binary→phone `message`
// envelope (TypeMessage + protocol.MessagePayload) — the shape cmd/pyry's
// fan-out emitters hand to Push after enumerating open conns with ActiveConns.
// text identifies the frame so the decrypting side can assert the payload
// round-tripped intact.
func buildMessageEnvelope(t *testing.T, id uint64, text string) protocol.Envelope {
	t.Helper()
	payload, err := json.Marshal(protocol.MessagePayload{
		ConversationID: "conv-push-1",
		MessageID:      "msg-" + text,
		Role:           "assistant",
		Text:           text,
	})
	if err != nil {
		t.Fatalf("marshal message payload: %v", err)
	}
	return protocol.Envelope{
		ID:      id,
		Type:    protocol.TypeMessage,
		TS:      time.Now().UTC(),
		Payload: payload,
	}
}

// --- pushQueue drop-policy unit tests (#610) ---

// pqEnv builds a minimal envelope carrying only the Type (which drives the drop
// class) and a strictly-increasing ID (which lets order assertions track an
// individual envelope across drops). No payload/seal — pushQueue.enqueue is a
// pure pre-seal policy over the envelope class.
func pqEnv(typ string, id uint64) protocol.Envelope {
	return protocol.Envelope{ID: id, Type: typ}
}

// assertQueue checks q.items matches want (by Type+ID, in order), that each
// item's droppable flag is consistent with its Type, and that q.dropped equals
// wantDropped.
func assertQueue(t *testing.T, q *pushQueue, want []protocol.Envelope, wantDropped uint64) {
	t.Helper()
	if len(q.items) != len(want) {
		t.Fatalf("queue len = %d, want %d", len(q.items), len(want))
	}
	for i := range want {
		got := q.items[i]
		if got.env.Type != want[i].Type || got.env.ID != want[i].ID {
			t.Errorf("item[%d] = (%s,%d), want (%s,%d)",
				i, got.env.Type, got.env.ID, want[i].Type, want[i].ID)
		}
		if wantDrop := want[i].Type == protocol.TypeAssistantDelta; got.droppable != wantDrop {
			t.Errorf("item[%d] droppable = %v, want %v", i, got.droppable, wantDrop)
		}
	}
	if q.dropped != wantDropped {
		t.Errorf("dropped = %d, want %d", q.dropped, wantDropped)
	}
}

// fillDeltas enqueues deltas with IDs lo..hi-1 onto q (helper for the cap-fill
// scenarios). Returns the want-slice describing those same items.
func fillDeltas(q *pushQueue, lo, hi uint64) []protocol.Envelope {
	want := make([]protocol.Envelope, 0, hi-lo)
	for id := lo; id < hi; id++ {
		e := pqEnv(protocol.TypeAssistantDelta, id)
		q.enqueue(e)
		want = append(want, e)
	}
	return want
}

// TestPushQueue_Enqueue_UnderCap_AllRetained pins the below-cap path: a mix of
// deltas and control events under pushQueueCap are all retained in FIFO order
// with no drops.
func TestPushQueue_Enqueue_UnderCap_AllRetained(t *testing.T) {
	t.Parallel()

	q := &pushQueue{}
	seq := []protocol.Envelope{
		pqEnv(protocol.TypeAssistantDelta, 1),
		pqEnv(protocol.TypeTurnState, 2),
		pqEnv(protocol.TypeAssistantDelta, 3),
		pqEnv(protocol.TypeToolUse, 4),
		pqEnv(protocol.TypeToolResult, 5),
		pqEnv(protocol.TypeAssistantDelta, 6),
		pqEnv(protocol.TypeTurnEnd, 7),
	}
	for _, e := range seq {
		if dropped := q.enqueue(e); dropped {
			t.Errorf("enqueue(id=%d) reported a drop under cap", e.ID)
		}
	}
	assertQueue(t, q, seq, 0)
}

// TestPushQueue_Enqueue_DropOldestDelta pins AC#2: at capacity, enqueuing a new
// delta evicts the OLDEST queued delta so the most recent text is retained.
func TestPushQueue_Enqueue_DropOldestDelta(t *testing.T) {
	t.Parallel()

	q := &pushQueue{}
	fillDeltas(q, 0, pushQueueCap) // ids 0..cap-1

	if dropped := q.enqueue(pqEnv(protocol.TypeAssistantDelta, 9999)); !dropped {
		t.Fatal("enqueue at capacity should report a drop")
	}
	// Oldest (id 0) evicted; ids 1..cap-1 retained in order, new delta at tail.
	want := fillDeltas(&pushQueue{}, 1, pushQueueCap)
	want = append(want, pqEnv(protocol.TypeAssistantDelta, 9999))
	assertQueue(t, q, want, 1)
}

// TestPushQueue_Enqueue_ControlEvictsDelta pins AC#3: at capacity, a control
// event is admitted by evicting the oldest droppable delta — never by dropping
// the control event.
func TestPushQueue_Enqueue_ControlEvictsDelta(t *testing.T) {
	t.Parallel()

	q := &pushQueue{}
	fillDeltas(q, 0, pushQueueCap)

	if dropped := q.enqueue(pqEnv(protocol.TypeTurnEnd, 9999)); !dropped {
		t.Fatal("control at capacity should evict a delta (reported as a drop)")
	}
	want := fillDeltas(&pushQueue{}, 1, pushQueueCap)
	want = append(want, pqEnv(protocol.TypeTurnEnd, 9999))
	assertQueue(t, q, want, 1)
}

// TestPushQueue_Enqueue_MessageIsNeverDrop pins the ticket's class decision:
// the coarse v1 "message" envelope (#589 bridge) is never-drop — only
// assistant_delta is droppable. A message at capacity evicts a delta, like any
// other control event.
func TestPushQueue_Enqueue_MessageIsNeverDrop(t *testing.T) {
	t.Parallel()

	q := &pushQueue{}
	fillDeltas(q, 0, pushQueueCap)

	if dropped := q.enqueue(pqEnv(protocol.TypeMessage, 9999)); !dropped {
		t.Fatal("message at capacity should evict a delta, not drop itself")
	}
	want := fillDeltas(&pushQueue{}, 1, pushQueueCap)
	want = append(want, pqEnv(protocol.TypeMessage, 9999))
	assertQueue(t, q, want, 1)
}

// TestPushQueue_Enqueue_ControlNeverDroppedWhenDeltasPresent pins AC#3 at
// volume: pushing N control events into a full all-delta queue evicts exactly N
// deltas (oldest-first) and keeps every control event, in order.
func TestPushQueue_Enqueue_ControlNeverDroppedWhenDeltasPresent(t *testing.T) {
	t.Parallel()

	q := &pushQueue{}
	fillDeltas(q, 0, pushQueueCap)

	controlTypes := []string{
		protocol.TypeTurnState,
		protocol.TypeToolUse,
		protocol.TypeToolResult,
		protocol.TypeTurnEnd,
		protocol.TypeStall,
	}
	for k, ct := range controlTypes {
		if dropped := q.enqueue(pqEnv(ct, uint64(1000+k))); !dropped {
			t.Errorf("control %q at capacity should evict a delta", ct)
		}
	}
	// The N oldest deltas (ids 0..N-1) evicted; ids N..cap-1 retained, then the
	// N control events at the tail in push order.
	n := uint64(len(controlTypes))
	want := fillDeltas(&pushQueue{}, n, pushQueueCap)
	for k, ct := range controlTypes {
		want = append(want, pqEnv(ct, uint64(1000+k)))
	}
	assertQueue(t, q, want, n)
}

// TestPushQueue_Enqueue_OrderPreservedAcrossDrops pins AC#4: across a long
// scripted interleave that forces many delta evictions, the surviving
// envelopes stay in enqueue order (strictly-increasing IDs) and no control
// event is ever lost. Conservation: every enqueue either appends or drops one,
// and deltas are always available to evict, so the queue stays at exactly cap
// and dropped == totalEnqueued - cap.
func TestPushQueue_Enqueue_OrderPreservedAcrossDrops(t *testing.T) {
	t.Parallel()

	q := &pushQueue{}
	var nextID uint64
	controlIDs := map[uint64]bool{}
	enqueue := func(typ string) {
		nextID++
		if typ != protocol.TypeAssistantDelta {
			controlIDs[nextID] = true
		}
		q.enqueue(pqEnv(typ, nextID))
	}

	// Fill to cap with alternating control/delta so there are always deltas to
	// evict and controls to protect.
	for i := 0; i < pushQueueCap; i++ {
		if i%2 == 0 {
			enqueue(protocol.TypeTurnState)
		} else {
			enqueue(protocol.TypeAssistantDelta)
		}
	}
	// Drive many more enqueues past cap: deltas (evict oldest delta) then
	// controls (evict oldest delta to be admitted).
	for i := 0; i < 50; i++ {
		enqueue(protocol.TypeAssistantDelta)
	}
	for i := 0; i < 10; i++ {
		enqueue(protocol.TypeToolUse)
	}

	// Order preserved: IDs strictly increasing across the surviving FIFO.
	var prev uint64
	for i, qe := range q.items {
		if qe.env.ID <= prev {
			t.Errorf("items[%d] id %d not strictly greater than prev %d — order not preserved",
				i, qe.env.ID, prev)
		}
		prev = qe.env.ID
	}
	// No control event dropped.
	present := make(map[uint64]bool, len(q.items))
	for _, qe := range q.items {
		present[qe.env.ID] = true
	}
	for id := range controlIDs {
		if !present[id] {
			t.Errorf("control id %d was dropped — control must never drop", id)
		}
	}
	// Conservation: stayed at cap, dropped accounts for the overflow.
	if len(q.items) != pushQueueCap {
		t.Errorf("len = %d, want %d (deltas always available to evict)", len(q.items), pushQueueCap)
	}
	if want := nextID - uint64(pushQueueCap); q.dropped != want {
		t.Errorf("dropped = %d, want %d (totalEnqueued - cap)", q.dropped, want)
	}
}

// TestPushQueue_Enqueue_AllControlSoftOverflow pins the documented soft-overflow
// edge: when the queue is saturated entirely with control events, an incoming
// control is admitted PAST nominal cap (never dropped, never blocks), while an
// incoming delta is dropped (it cannot evict a control event), leaving the
// queue unchanged.
func TestPushQueue_Enqueue_AllControlSoftOverflow(t *testing.T) {
	t.Parallel()

	q := &pushQueue{}
	for i := 0; i < pushQueueCap; i++ {
		q.enqueue(pqEnv(protocol.TypeTurnState, uint64(i)))
	}

	// Control past a full all-control queue: admitted, no drop, len == cap+1.
	if dropped := q.enqueue(pqEnv(protocol.TypeTurnEnd, 9000)); dropped {
		t.Error("control soft-overflow must not report a drop")
	}
	if len(q.items) != pushQueueCap+1 {
		t.Errorf("len = %d, want %d (soft overflow admits control past cap)", len(q.items), pushQueueCap+1)
	}
	if q.dropped != 0 {
		t.Errorf("dropped = %d, want 0", q.dropped)
	}

	// Delta into the all-control-saturated queue: dropped; queue unchanged.
	lenBefore := len(q.items)
	if dropped := q.enqueue(pqEnv(protocol.TypeAssistantDelta, 9001)); !dropped {
		t.Error("delta into an all-control full queue must be dropped")
	}
	if len(q.items) != lenBefore {
		t.Errorf("len = %d after dropped delta, want %d (unchanged)", len(q.items), lenBefore)
	}
	if q.dropped != 1 {
		t.Errorf("dropped = %d, want 1", q.dropped)
	}
	// The tail is still the soft-overflow control — the dropped delta was never
	// appended.
	last := q.items[len(q.items)-1]
	if last.env.Type != protocol.TypeTurnEnd || last.env.ID != 9000 {
		t.Errorf("tail = (%s,%d), want (turn_end,9000)", last.env.Type, last.env.ID)
	}
}

// --- pushQueue byte-ceiling unit tests (#1505) ---

// maxAppEnvelopePayload is the v2 application-envelope cap
// (docs/protocol-mobile.md § Application-envelope size cap) — the largest
// payload a real control envelope can carry, and the size the ceiling's
// derivation is written against. Duplicated here rather than exported from
// production: internal/relay enforces the cap on the Noise side
// (maxNoisePayloadBytes), not on Payload, so there is no production constant to
// borrow.
const maxAppEnvelopePayload = 65519

// payloadOfLen returns a payload of exactly n bytes. Its CONTENT is irrelevant
// — pushQueue.bytes measures len(env.Payload) and nothing else — so every
// envelope in a fill loop can share one backing array, which is what keeps a
// 32 MiB ceiling test to a single allocation instead of hundreds.
func payloadOfLen(n int) json.RawMessage {
	return make(json.RawMessage, n)
}

// pqEnvPayload is pqEnv with a payload attached — the only input the byte
// accounting reads. pqEnv itself leaves Payload nil (0 bytes), which is why
// every pre-#1505 pushQueue test is unaffected by the ceiling.
func pqEnvPayload(typ string, id uint64, payload json.RawMessage) protocol.Envelope {
	e := pqEnv(typ, id)
	e.Payload = payload
	return e
}

// TestPushQueue_Enqueue_ByteCeilingBoundary pins AC#1's both-sides assertion:
// filling an all-control queue right up to pushQueueByteCeiling admits the
// envelope that lands EXACTLY on it (the bound is `>`, not `>=`), and the very
// next envelope — one byte — is rejected, leaving the queue byte-for-byte and
// item-for-item unchanged with the overflowed latch set and the drop-policy
// counter untouched.
func TestPushQueue_Enqueue_ByteCeilingBoundary(t *testing.T) {
	t.Parallel()

	big := payloadOfLen(maxAppEnvelopePayload)
	q := &pushQueue{}

	// Fill with full-size control envelopes while a whole one still fits.
	var id uint64
	for q.bytes+maxAppEnvelopePayload <= pushQueueByteCeiling {
		id++
		if dropped := q.enqueue(pqEnvPayload(protocol.TypeToolResult, id, big)); dropped {
			t.Fatalf("enqueue(id=%d) reported a drop below the ceiling", id)
		}
	}
	if q.overflowed {
		t.Fatal("overflowed latched while every envelope still fit under the ceiling")
	}

	// The last admission: an envelope sized to land EXACTLY on the ceiling.
	headroom := pushQueueByteCeiling - q.bytes
	if headroom <= 0 {
		t.Fatalf("headroom = %d, want > 0 (the fill loop must stop short of the ceiling)", headroom)
	}
	lenBefore := len(q.items)
	if dropped := q.enqueue(pqEnvPayload(protocol.TypeTurnState, 9000, payloadOfLen(headroom))); dropped {
		t.Error("the exact-fit envelope must not report a drop")
	}
	if len(q.items) != lenBefore+1 {
		t.Fatalf("len = %d, want %d (an envelope landing exactly on the ceiling is admitted)", len(q.items), lenBefore+1)
	}
	if q.bytes != pushQueueByteCeiling {
		t.Fatalf("bytes = %d, want exactly %d", q.bytes, pushQueueByteCeiling)
	}
	if q.overflowed {
		t.Error("overflowed latched on the exact-fit envelope; the bound is `>`, not `>=`")
	}

	// The first rejection: one more byte cannot fit.
	if dropped := q.enqueue(pqEnvPayload(protocol.TypeTurnEnd, 9001, payloadOfLen(1))); dropped {
		t.Error("a ceiling rejection must not report a drop-policy drop")
	}
	if len(q.items) != lenBefore+1 {
		t.Errorf("len = %d after the rejection, want %d (unchanged)", len(q.items), lenBefore+1)
	}
	if q.bytes != pushQueueByteCeiling {
		t.Errorf("bytes = %d after the rejection, want %d (unchanged)", q.bytes, pushQueueByteCeiling)
	}
	if !q.overflowed {
		t.Error("overflowed = false after a ceiling rejection, want true (it is the teardown signal)")
	}
	if q.dropped != 0 {
		t.Errorf("dropped = %d, want 0 (a ceiling rejection is not a drop-policy drop)", q.dropped)
	}
	// The tail is still the exact-fit envelope — the rejected one never landed.
	last := q.items[len(q.items)-1]
	if last.env.Type != protocol.TypeTurnState || last.env.ID != 9000 {
		t.Errorf("tail = (%s,%d), want (turn_state,9000)", last.env.Type, last.env.ID)
	}
}

// TestPushQueue_Enqueue_ByteCeilingIsBytesNotCount pins AC#1's second case: the
// ceiling measures BYTES, so a queue holding far more than pushQueueCap *small*
// control envelopes — whose total stays well under the ceiling — is admitted
// whole, with no latch and no drop. A count-only implementation fails here:
// control payloads span ~100 B to 64 KB (a 640× spread), so any count cap
// either permits N × 64 KB or kills legitimate bundles.
func TestPushQueue_Enqueue_ByteCeilingIsBytesNotCount(t *testing.T) {
	t.Parallel()

	const smallLen = 100
	const n = 20 * pushQueueCap // far past the nominal count cap
	small := payloadOfLen(smallLen)

	q := &pushQueue{}
	for i := 0; i < n; i++ {
		if dropped := q.enqueue(pqEnvPayload(protocol.TypeToolResult, uint64(i), small)); dropped {
			t.Fatalf("enqueue(id=%d) reported a drop; small control events must all be admitted", i)
		}
	}
	if len(q.items) != n {
		t.Errorf("len = %d, want %d (every small control envelope admitted)", len(q.items), n)
	}
	if want := n * smallLen; q.bytes != want {
		t.Errorf("bytes = %d, want %d", q.bytes, want)
	}
	if q.bytes >= pushQueueByteCeiling {
		t.Fatalf("fixture bytes = %d reached the ceiling %d; the case is vacuous", q.bytes, pushQueueByteCeiling)
	}
	if q.overflowed {
		t.Error("overflowed = true well under the ceiling — the bound is counting items, not bytes")
	}
	if q.dropped != 0 {
		t.Errorf("dropped = %d, want 0", q.dropped)
	}
}

// TestPushQueue_PopHead_AccountingRoundTrip guards the missed-decrement bug
// whose production symptom is a phantom-byte leak that eventually tears down a
// perfectly healthy long-lived session: enqueue a mix of classes and payload
// sizes, pop every one, and the counter must return to exactly zero. Also pins
// that popHead is FIFO.
func TestPushQueue_PopHead_AccountingRoundTrip(t *testing.T) {
	t.Parallel()

	sizes := []int{0, 1, 37, 4096, maxAppEnvelopePayload, 12, 999}
	q := &pushQueue{}
	total := 0
	for i, n := range sizes {
		typ := protocol.TypeToolResult
		if i%2 == 1 {
			typ = protocol.TypeAssistantDelta
		}
		q.enqueue(pqEnvPayload(typ, uint64(i), payloadOfLen(n)))
		total += n
	}
	if q.bytes != total {
		t.Fatalf("bytes after enqueue = %d, want %d", q.bytes, total)
	}
	for i := range sizes {
		env := q.popHead()
		if env.ID != uint64(i) {
			t.Errorf("popHead #%d returned id %d, want %d (FIFO)", i, env.ID, i)
		}
	}
	if len(q.items) != 0 {
		t.Errorf("len = %d after draining, want 0", len(q.items))
	}
	if q.bytes != 0 {
		t.Errorf("bytes = %d after draining every item, want 0 (a missed decrement leaks phantom bytes)", q.bytes)
	}
}

// TestPushQueue_Enqueue_EvictionDecrementsBytes pins the evict site's side of
// the accounting: at cap with queued deltas, admitting a control event evicts
// the oldest delta, so the counter must fall by exactly that delta's payload
// length and rise by the incoming one's.
func TestPushQueue_Enqueue_EvictionDecrementsBytes(t *testing.T) {
	t.Parallel()

	const deltaLen = 400
	const controlLen = 4321

	q := &pushQueue{}
	for i := 0; i < pushQueueCap; i++ {
		q.enqueue(pqEnvPayload(protocol.TypeAssistantDelta, uint64(i), payloadOfLen(deltaLen)))
	}
	before := q.bytes
	if want := pushQueueCap * deltaLen; before != want {
		t.Fatalf("bytes after fill = %d, want %d", before, want)
	}
	if dropped := q.enqueue(pqEnvPayload(protocol.TypeTurnEnd, 9999, payloadOfLen(controlLen))); !dropped {
		t.Fatal("control at capacity should evict a delta (reported as a drop)")
	}
	if want := before - deltaLen + controlLen; q.bytes != want {
		t.Errorf("bytes = %d, want %d (evicted delta subtracted, incoming control added)", q.bytes, want)
	}
	if len(q.items) != pushQueueCap {
		t.Errorf("len = %d, want %d (evict-then-append holds at cap)", len(q.items), pushQueueCap)
	}
}

// TestV2Session_Push_NonBlockingUnderStall pins AC#1 end-to-end: with the
// outbound (relay) leg stalled so the Run goroutine is wedged mid-forward, the
// producer's Push calls all return promptly (never blocked on the relay), the
// drop counter engages once the buffer fills past cap, and after the stall is
// released the surviving deltas decrypt in order under the phone's recv state —
// proving drop-before-seal left no nonce gap and FIFO order was preserved.
func TestV2Session_Push_NonBlockingUnderStall(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	// Stalling outbound: records every frame via an inner v2Recorder, but once
	// armed, blocks each call on release first. The handshake runs with stall
	// off so the session reaches open; we arm afterwards to wedge the drain.
	rec := &v2Recorder{}
	var stall atomic.Bool
	release := make(chan struct{})
	gated := func(env protocol.RoutingEnvelope) error {
		if stall.Load() {
			<-release
		}
		return rec.outbound(env)
	}

	frames := make(chan protocol.RoutingEnvelope, 4)
	sess := driveToOpen(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   gated,
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
	}, frames, rec, respPub, initPriv)

	var releaseOnce sync.Once
	releaseFn := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(sess.stop) // runs last
	t.Cleanup(releaseFn) // runs first — unblock any wedged forward before stop

	// Arm the stall: the next forward (the first drained push) blocks in gated.
	stall.Store(true)

	// Pre-build push envelopes on the test goroutine (avoid building inside the
	// pusher goroutine). Deltas with strictly-increasing IDs 0..nPush-1, enough
	// to overflow the cap so the drop policy engages.
	const nPush = pushQueueCap + 64
	pushes := make([]protocol.Envelope, nPush)
	for i := range pushes {
		pushes[i] = pqEnv(protocol.TypeAssistantDelta, uint64(i))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Fire all pushes from a goroutine; assert they all return while the
	// outbound is stalled (proving Push never blocks on the relay).
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range pushes {
			if err := sess.mgr.Push(ctx, v2TestConnID, pushes[i]); err != nil {
				t.Errorf("Push[%d]: %v", i, err)
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		releaseFn() // avoid leaking the wedged Run goroutine
		t.Fatal("Push calls did not return while outbound stalled — producer wedged")
	}

	// The buffer filled past cap, so the drop counter engaged. Read under the
	// leaf lock. No more enqueues happen after this point, so dropped is final.
	sess.mgr.pushMu.Lock()
	dropped := sess.mgr.queues[v2TestConnID].dropped
	sess.mgr.pushMu.Unlock()
	if dropped == 0 {
		t.Fatalf("dropped = 0, want > 0 (buffer should overflow past cap=%d with %d pushes)", pushQueueCap, nPush)
	}

	// Release the stall; the drain pumps the survivors one-per-pass. Exactly
	// nPush-dropped app frames are forwarded (every push is dropped or sent),
	// plus the handshake noise_resp.
	releaseFn()
	wantApp := nPush - int(dropped)
	envs := waitForEnvelopes(t, sess.rec, 1+wantApp)
	if len(envs) != 1+wantApp {
		t.Fatalf("recorded %d envelopes, want %d (noise_resp + %d survivors)", len(envs), 1+wantApp, wantApp)
	}

	// Decrypt survivors in capture (= seal = FIFO drain) order under the phone's
	// recv state. A clean in-order decrypt proves drop-before-seal left no nonce
	// gap; strictly-increasing IDs prove FIFO order survived the drops.
	var prevID uint64
	haveFirst := false
	for _, e := range envs[1:] {
		inner := decryptAppFrame(t, e, sess.initRecv)
		if inner.Type != protocol.TypeAssistantDelta {
			t.Errorf("survivor type = %q, want assistant_delta", inner.Type)
		}
		if haveFirst && inner.ID <= prevID {
			t.Errorf("survivor id %d not strictly greater than prev %d — order not preserved", inner.ID, prevID)
		}
		prevID = inner.ID
		haveFirst = true
	}
	// Drop-oldest retains the most recent text: the last survivor is the last
	// pushed delta.
	if prevID != uint64(nPush-1) {
		t.Errorf("last survivor id = %d, want %d (most-recent delta retained)", prevID, nPush-1)
	}
}

// TestV2Session_Push_InterleavedWithReply_DecryptsUnderRace pins AC#1 and
// AC#4: an unsolicited Push fired from a separate goroutine while a
// request/reply dispatch is in flight on the same session. Both outbound
// frames must decrypt cleanly under the phone's recv CipherState in
// capture (= seal) order — any nonce reuse from concurrent s.send access
// would surface as an AEAD failure inside decryptAppFrame. The pushed
// frame decodes through the SAME path as the solicited reply
// (decryptAppFrame) to a valid TypeMessage envelope, proving no new wire
// shape is introduced. Order between reply and push is nondeterministic
// (Run's select); assert presence, not order.
func TestV2Session_Push_InterleavedWithReply_DecryptsUnderRace(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	const replyText = "push-interleave-reply"
	echoPayload, err := json.Marshal(map[string]string{"text": replyText})
	if err != nil {
		t.Fatalf("marshal echo payload: %v", err)
	}
	handlers := map[string]dispatch.Handler{
		protocol.TypeListConversations: func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
			return c.Reply(ctx, env, protocol.TypeConversations, echoPayload)
		},
	}

	frames := make(chan protocol.RoutingEnvelope, 2)
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

	const reqID uint64 = 41
	const pushText = "unsolicited-assistant-text"
	req := sealAppFrame(t, sess.initSend, protocol.Envelope{
		ID:      reqID,
		Type:    protocol.TypeListConversations,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(`{}`),
	})
	// Build the push envelope on the test goroutine: t.Fatalf inside a
	// child goroutine is unsafe, and buildMessageEnvelope may call it.
	pushEnv := buildMessageEnvelope(t, 100, pushText)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Fire the push concurrently with feeding the inbound request so the
	// buffer drain and dispatchAppFrame contend for the single Run goroutine.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := sess.mgr.Push(ctx, v2TestConnID, pushEnv); err != nil {
			t.Errorf("Push: %v", err)
		}
	}()
	frames <- req
	wg.Wait()

	// noise_resp (envs[0]) + reply + push.
	envs := waitForEnvelopes(t, sess.rec, 3)
	if len(envs) != 3 {
		t.Fatalf("envs: got %d, want exactly 3 (noise_resp + reply + push)", len(envs))
	}

	// Decrypt every app frame in capture order under the phone's recv
	// state. A clean in-order decrypt across both frames is the
	// nonce-integrity proof; decryptAppFrame t.Fatals on any AEAD failure.
	var sawReply, sawPush bool
	for _, e := range envs[1:] {
		inner := decryptAppFrame(t, e, sess.initRecv)
		switch inner.Type {
		case protocol.TypeConversations:
			if inner.InReplyTo == nil || *inner.InReplyTo != reqID {
				t.Errorf("reply InReplyTo = %v, want pointer to %d", inner.InReplyTo, reqID)
			}
			sawReply = true
		case protocol.TypeMessage:
			var mp protocol.MessagePayload
			if err := json.Unmarshal(inner.Payload, &mp); err != nil {
				t.Fatalf("decode pushed message payload: %v", err)
			}
			if mp.Text != pushText {
				t.Errorf("pushed message text = %q, want %q", mp.Text, pushText)
			}
			sawPush = true
		default:
			t.Errorf("unexpected outbound inner type %q", inner.Type)
		}
	}
	if !sawReply {
		t.Error("no conversations reply captured")
	}
	if !sawPush {
		t.Error("no message push captured")
	}
}

// TestV2Session_Push_ConcurrentWithReplies_NoNonceCorruption pins AC#2:
// N concurrent pushes plus M in-flight request/reply dispatches on the
// same open session. All N+M outbound frames must decrypt in capture
// order under the phone's recv state with no AEAD failure — the stress
// proof that the buffer drain serialises every s.send.Encrypt onto Run and
// the nonce counter never reuses. Run under -race.
func TestV2Session_Push_ConcurrentWithReplies_NoNonceCorruption(t *testing.T) {
	t.Parallel()

	const (
		nPush = 8
		mReq  = 8
	)

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
	// advances on a single goroutine; they must be fed (and thus decrypted
	// by the binary's recv) in this same order.
	reqs := make([]protocol.RoutingEnvelope, mReq)
	for i := range reqs {
		reqs[i] = sealAppFrame(t, sess.initSend, protocol.Envelope{
			ID:      uint64(1000 + i),
			Type:    protocol.TypeListConversations,
			TS:      time.Now().UTC(),
			Payload: json.RawMessage(`{}`),
		})
	}
	// Pre-build push envelopes on the test goroutine (avoid t.Fatalf from
	// a child goroutine).
	pushEnvs := make([]protocol.Envelope, nPush)
	for i := range pushEnvs {
		pushEnvs[i] = buildMessageEnvelope(t, uint64(2000+i), "push")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	for i := 0; i < nPush; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := sess.mgr.Push(ctx, v2TestConnID, pushEnvs[i]); err != nil {
				t.Errorf("Push[%d]: %v", i, err)
			}
		}(i)
	}
	// M inbound requests fed in seal order from this goroutine, interleaving
	// with the concurrent pushes at Run's select.
	for _, r := range reqs {
		frames <- r
	}
	wg.Wait()

	envs := waitForEnvelopes(t, sess.rec, 1+mReq+nPush)

	var pushes, replies int
	for _, e := range envs[1:] {
		inner := decryptAppFrame(t, e, sess.initRecv)
		switch inner.Type {
		case protocol.TypeConversations:
			replies++
		case protocol.TypeMessage:
			pushes++
		default:
			t.Errorf("unexpected outbound inner type %q", inner.Type)
		}
	}
	if pushes != nPush {
		t.Errorf("decoded %d pushes, want %d", pushes, nPush)
	}
	if replies != mReq {
		t.Errorf("decoded %d replies, want %d", replies, mReq)
	}
}

// TestV2Session_Push_UnknownConn_ErrConnNotFound_OtherSessionUnaffected
// pins AC#3: pushing to a conn_id the manager has never seen returns
// ErrConnNotFound (wrapping control.ErrConnNotFound for the wire-mapping
// invariant) and does NOT mutate an unrelated open session — that
// session's subsequent solicited round-trip still decrypts cleanly.
func TestV2Session_Push_UnknownConn_ErrConnNotFound_OtherSessionUnaffected(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	echoPayload, err := json.Marshal(map[string]string{"text": "ok"})
	if err != nil {
		t.Fatalf("marshal echo payload: %v", err)
	}
	handlers := map[string]dispatch.Handler{
		protocol.TypeListConversations: func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
			return c.Reply(ctx, env, protocol.TypeConversations, echoPayload)
		},
	}

	frames := make(chan protocol.RoutingEnvelope, 2)
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

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err = sess.mgr.Push(ctx, "no-such-conn", buildMessageEnvelope(t, 1, "x"))
	if !errors.Is(err, ErrConnNotFound) {
		t.Errorf("errors.Is(err, relay.ErrConnNotFound) = false; err = %v", err)
	}
	if !errors.Is(err, control.ErrConnNotFound) {
		t.Errorf("errors.Is(err, control.ErrConnNotFound) = false; err = %v (wire-mapping invariant broken)", err)
	}

	// The unrelated open session is untouched: a solicited round-trip still
	// decrypts cleanly (its send CipherState was never mutated).
	const reqID uint64 = 7
	frames <- sealAppFrame(t, sess.initSend, protocol.Envelope{
		ID:      reqID,
		Type:    protocol.TypeListConversations,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(`{}`),
	})
	envs := waitForEnvelopes(t, sess.rec, 2) // noise_resp + reply
	inner := decryptAppFrame(t, envs[1], sess.initRecv)
	if inner.Type != protocol.TypeConversations {
		t.Errorf("reply Type = %q, want %q", inner.Type, protocol.TypeConversations)
	}
	if inner.InReplyTo == nil || *inner.InReplyTo != reqID {
		t.Errorf("reply InReplyTo = %v, want pointer to %d", inner.InReplyTo, reqID)
	}
}

// TestV2Session_Push_NotOpen_ReturnsErrConnNotFound pins the error-contract
// change (#610): a session that exists in m.sessions but never reached
// V2StateOpen has no push queue (queues are created only at the open tail), so
// the public Push collapses "not open" into ErrConnNotFound — a not-open conn
// is indistinguishable from an unknown one at the enqueue boundary. The
// V2StateOpen security gate moved to the drain side (forwardEnvelope); see
// TestV2Session_forwardEnvelope_NotOpen_GateRefuses for that half. White-box
// session injection (no queue) mirrors the gating test.
func TestV2Session_Push_NotOpen_ReturnsErrConnNotFound(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		state V2SessionState
	}{
		{"awaiting_init", V2StateAwaitingInit},
		{"handshake_complete", V2StateHandshakeComplete},
	} {
		t.Run(tc.name, func(t *testing.T) {
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

			// Inject a pre-open session WITHOUT a queue — exactly the state of a
			// conn mid-handshake. Push never touches s.send, so nil CipherStates
			// are safe here.
			const connID = "c-notopen"
			mgr.sessions[connID] = &V2Session{connID: connID, state: tc.state}

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			err := mgr.Push(ctx, connID, buildMessageEnvelope(t, 1, "x"))
			if !errors.Is(err, ErrConnNotFound) {
				t.Errorf("Push to %v session: err = %v, want ErrConnNotFound", tc.state, err)
			}
			if got := rec.snapshot(); len(got) != 0 {
				t.Errorf("rec.snapshot() len = %d, want 0 (no outbound on not-open push)", len(got))
			}
		})
	}
}

// TestV2Session_forwardEnvelope_NotOpen_GateRefuses pins the drain-side
// security gate (#610 security review): forwardEnvelope — the path the buffer
// drain uses to seal-and-forward — refuses a session that is not V2StateOpen,
// so a buffered push to a conn that closed or de-authed between enqueue and
// drain is dropped before sealing and never delivered to an un-authenticated
// peer. White-box, no Run goroutine: forwardEnvelope is called directly on the
// test goroutine over an injected non-open session, so the read of s.state is
// uncontended.
func TestV2Session_forwardEnvelope_NotOpen_GateRefuses(t *testing.T) {
	t.Parallel()

	respPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)
	rec := &v2Recorder{}
	mgr, err := NewV2SessionManager(V2SessionConfig{
		Frames:     make(chan protocol.RoutingEnvelope),
		Outbound:   rec.outbound,
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
	})
	if err != nil {
		t.Fatalf("NewV2SessionManager: %v", err)
	}
	// Intentionally do NOT start Run: forwardEnvelope is exercised directly.
	const connID = "c-gate"

	// Missing session → ErrConnNotFound.
	if err := mgr.forwardEnvelope(context.Background(), connID, buildMessageEnvelope(t, 1, "x")); !errors.Is(err, ErrConnNotFound) {
		t.Errorf("forwardEnvelope to unknown conn: err = %v, want ErrConnNotFound", err)
	}

	// Present-but-not-open session (e.g. closed/de-authed before drain) →
	// ErrSessionNotOpen, no seal, no outbound. nil CipherStates are safe: the
	// state check returns before s.send is touched.
	mgr.sessions[connID] = &V2Session{connID: connID, state: V2StateHandshakeComplete}
	if err := mgr.forwardEnvelope(context.Background(), connID, buildMessageEnvelope(t, 1, "x")); !errors.Is(err, ErrSessionNotOpen) {
		t.Errorf("forwardEnvelope to non-open session: err = %v, want ErrSessionNotOpen", err)
	}
	if got := rec.snapshot(); len(got) != 0 {
		t.Errorf("rec.snapshot() len = %d, want 0 (gate refused before any seal/send)", len(got))
	}
}

// TestV2Session_Push_ClosedSession_ReturnsErrConnNotFound pins AC#3: a
// session that was opened then torn down (an AEAD-failure 4421 close
// deletes it from the map) collapses into the same ErrConnNotFound branch
// as a never-seen conn. closeWith deletes the entry before emitting the
// close envelope, so observing the close guarantees the delete has
// happened on the Run goroutine.
func TestV2Session_Push_ClosedSession_ReturnsErrConnNotFound(t *testing.T) {
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

	// Drive an AEAD-failure close: flip a byte in a sealed frame's
	// ciphertext.
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

	closeEnvs := waitForEnvelopes(t, sess.rec, 2) // noise_resp + 4421 close
	if closeEnvs[1].CloseCode != uint16(StatusProtocolMismatch) {
		t.Fatalf("close_code = %d, want %d", closeEnvs[1].CloseCode, StatusProtocolMismatch)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err = sess.mgr.Push(ctx, v2TestConnID, buildMessageEnvelope(t, 1, "x"))
	if !errors.Is(err, ErrConnNotFound) {
		t.Errorf("Push to closed session: err = %v, want ErrConnNotFound", err)
	}
}

// TestV2Session_Push_CtxCancelled_ReturnsCtxErr pins the ctx-cancellation
// arm: a Push whose ctx is already cancelled returns ctx.Err() without
// blocking. Push checks ctx before the enqueue, so a cancelled ctx
// short-circuits without consulting the queues — deterministic, no Run
// goroutine required.
func TestV2Session_Push_CtxCancelled_ReturnsCtxErr(t *testing.T) {
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
	// Intentionally do NOT start Run: the ctx pre-check short-circuits before
	// any enqueue, so no drain goroutine is needed.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := mgr.Push(ctx, v2TestConnID, buildMessageEnvelope(t, 1, "x")); !errors.Is(err, context.Canceled) {
		t.Errorf("Push with cancelled ctx: err = %v, want context.Canceled", err)
	}
}

// --- push-queue byte-ceiling teardown tests (#1505) ---

// queueBytes returns connID's retained push-queue payload total under the leaf
// lock, or -1 if no queue exists. The byte twin of queueLen; used to prove a
// near-ceiling fixture is genuinely near the ceiling rather than trivially
// under it.
func queueBytes(mgr *V2SessionManager, connID string) int {
	mgr.pushMu.Lock()
	defer mgr.pushMu.Unlock()
	q, ok := mgr.queues[connID]
	if !ok {
		return -1
	}
	return q.bytes
}

// waitQueueGone polls until connID's push queue is deleted — the observable
// end of closeWith's teardown, and the point every retained byte is freed. A
// positive settle, so poll-until is deterministic and fast.
func waitQueueGone(t *testing.T, mgr *V2SessionManager, connID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if queueLen(mgr, connID) == -1 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("push queue for %s still present (depth %d); the ceiling teardown never freed it", connID, queueLen(mgr, connID))
}

// TestV2Session_Push_ByteCeilingTearsDownSession pins AC#2 + AC#3: with the
// drain parked by the #874 transport-down hold and a live stream of
// control-class pushes, crossing pushQueueByteCeiling tears the session down —
// it leaves the manager and its push queue is deleted, freeing every retained
// byte — while the producer sees nothing but nil, and later pushes to the
// torn-down conn get the pre-existing ErrConnNotFound. Run under -race:
// exercises the off-Run Push → m.pushOverflow → Run → closeWith path.
func TestV2Session_Push_ByteCeilingTearsDownSession(t *testing.T) {
	t.Parallel()

	// The teardown observed below must be the CEILING's, not the pre-existing
	// idle sweep's: idleTimeout stays at its production value and this test
	// completes in well under a second. (The idle-sweep tests that shrink it are
	// deliberately non-parallel, so they cannot overlap with this one.)
	if idleTimeout != 15*time.Minute {
		t.Fatalf("idleTimeout = %v, want the production 15m; a shrunk sweep would make this test vacuous", idleTimeout)
	}

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	// The independent probe/send wiring from
	// TestV2Session_Push_HoldGatedOnProbeNotSendError: Connected drives the #874
	// hold while Outbound records UNCONDITIONALLY. A plain gatedRecorder is the
	// wrong fixture here — it records nothing while down, so the 4413 close
	// frame would be invisible.
	var probeUp atomic.Bool
	probeUp.Store(true)
	rec := &v2Recorder{}
	frames := make(chan protocol.RoutingEnvelope, 2)
	sess := driveToOpen(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   rec.outbound,
		Connected:  probeUp.Load,
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
	}, frames, rec, respPub, initPriv)
	t.Cleanup(sess.stop)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Park the drain: nothing pops, so the queue only grows (the #874 hold + a
	// live turn, which is this ticket's driver).
	probeUp.Store(false)

	// Exactly enough full-size control pushes to cross the ceiling ONCE, and not
	// one more. The teardown cannot begin before the crossing, so every push in
	// this loop must return nil (AC#3); pushing beyond it would race the
	// teardown into ErrConnNotFound and make the assertion non-deterministic.
	payload := payloadOfLen(maxAppEnvelopePayload)
	pushes := pushQueueByteCeiling/maxAppEnvelopePayload + 1
	for i := 1; i <= pushes; i++ {
		env := protocol.Envelope{
			ID:      uint64(i),
			Type:    protocol.TypeToolResult,
			TS:      time.Now().UTC(),
			Payload: payload,
		}
		if err := sess.mgr.Push(ctx, v2TestConnID, env); err != nil {
			t.Fatalf("Push #%d/%d returned %v, want nil (the overflow is not the producer's failure)", i, pushes, err)
		}
	}

	// The queue is deleted, so every retained byte is freed — without waiting
	// out the 15-minute idle sweep.
	waitQueueGone(t, sess.mgr, v2TestConnID)

	// The session left V2StateOpen: closeWith deleted it, so the open-session
	// enumeration no longer lists it.
	for _, c := range sess.mgr.ActiveConns(ctx) {
		if c.ConnID == v2TestConnID {
			t.Fatalf("conn %q still enumerated as open after the ceiling teardown", c.ConnID)
		}
	}

	// The close reached the wire as 4413 with a NIL frame — nothing sealed, so
	// no Noise send-nonce was burned for a frame the down transport cannot
	// deliver (the #912 hazard).
	closeEnv := waitForConnClose(t, rec, v2TestConnID, uint16(StatusQueueOverflow))
	if closeEnv.Frame != nil {
		t.Errorf("close frame = %d bytes, want nil (the ceiling teardown seals nothing)", len(closeEnv.Frame))
	}

	// No NEW error value reaches a producer: the next push gets the pre-existing
	// sentinel, errors.Is-comparable exactly as before.
	err := sess.mgr.Push(ctx, v2TestConnID, protocol.Envelope{ID: 9999, Type: protocol.TypeToolResult, TS: time.Now().UTC()})
	if !errors.Is(err, ErrConnNotFound) {
		t.Fatalf("Push after teardown = %v, want ErrConnNotFound", err)
	}
	if !errors.Is(err, control.ErrConnNotFound) {
		t.Errorf("Push after teardown does not wrap control.ErrConnNotFound: %v", err)
	}
}
