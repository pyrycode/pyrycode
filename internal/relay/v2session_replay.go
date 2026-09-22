package relay

import (
	"context"
	"encoding/json"
	"time"

	"github.com/pyrycode/pyrycode/internal/eventring"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// This file holds two reconnect-recovery concerns — the screen-snapshot request
// path (the TypeRequestSnapshot handler, its deterministic error-reply helper,
// and the two static error-message constants) and the missed-event
// replay/resync path (SetReplaySource, replayMissed, emitResync, drainReplayOnce)
// — carved out of v2session.go (#1026). Pure move: same package, no behaviour
// change. The V2Session / V2SessionManager structs, the Run loop, the
// TypeRequestSnapshot dispatch case in dispatchAppFrame, the snapshotReq /
// ActiveConns enumeration machinery, and the shared forwardEnvelope / sealError
// seal path all stay in v2session.go. This is the 6th #964 slice, after #1021
// (handshake), #1022 (rekey), #1023 (modal+queue), #1024 (settings), and #1025
// (debugbundle).

// Static error messages for request_snapshot replies. Deliberately generic:
// the wire reply NEVER echoes the JSON decode error or the (attacker-
// controlled) raw conversation_id, only one of these constants.
const (
	msgSnapshotConvNotFound = "unknown or foreign conversation_id"
	msgSnapshotOffline      = "no live claude session"
)

// handleRequestSnapshot renders the current claude screen and pushes a
// screen_snapshot addressed to s, or a deterministic error reply. It is the
// inbound-control handler for TypeRequestSnapshot — intercepted in
// dispatchAppFrame before dispatch.Route, exactly like handleRekeyRequest —
// and runs on the manager's single Run dispatch goroutine. Every branch pushes
// exactly one reply and returns: it never panics, hangs, or silently drops the
// request (AC #3).
//
// Both the success and error replies are delivered via m.forwardEnvelope — the
// single existing seal-and-forward path. The public Push is deliberately NOT
// used here: it would enqueue the reply onto the buffered push stream (subject
// to the drop policy and a deferred drain pass), whereas a snapshot reply is
// InReplyTo-correlated and must seal immediately and in-line on this same Run
// goroutine.
//
// SECURITY: the rendered screen text is NEVER logged; error replies carry only
// a static message constant. The conversation_id is validated before any render
// (AC #4): an unknown/foreign id renders nothing.
func (m *V2SessionManager) handleRequestSnapshot(ctx context.Context, s *V2Session, env protocol.Envelope) {
	var payload protocol.RequestSnapshotPayload
	// A decode failure is tolerated: it leaves ConversationID == "", which the
	// KnownConversation check below rejects as not-found. The decode error is
	// never echoed back to the phone.
	_ = json.Unmarshal(env.Payload, &payload)

	// AC #4: reject an unknown/foreign conversation_id before any render. A nil
	// KnownConversation (optional seam) rejects everything as not-found.
	if m.cfg.KnownConversation == nil || !m.cfg.KnownConversation(payload.ConversationID) {
		m.snapshotReplyError(ctx, s, env.ID, protocol.CodeConversationNotFound, msgSnapshotConvNotFound, false)
		return
	}

	// AC #3: a nil Snapshotter (optional seam) means the feature is
	// unavailable; report it deterministically rather than dropping.
	if m.cfg.Snapshotter == nil {
		m.snapshotReplyError(ctx, s, env.ID, protocol.CodeServerBinaryOffline, msgSnapshotOffline, true)
		return
	}
	text, live := m.cfg.Snapshotter.ScreenSnapshot()
	if !live {
		// AC #3: no claude child attached (between restarts / idle-evicted).
		m.snapshotReplyError(ctx, s, env.ID, protocol.CodeServerBinaryOffline, msgSnapshotOffline, true)
		return
	}

	// Reflect the bootstrap session's persisted model / effort / YOLO (#848). A
	// nil seam (optional, foreground / unwired) leaves the three at their
	// defaults — empty model/effort ("inherited daemon default"), yolo:false
	// (permissions enforced) — byte-identical to the pre-#848 zero-value reply.
	var model, effort string
	var yolo bool
	if m.cfg.SnapshotSettings != nil {
		model, effort, yolo = m.cfg.SnapshotSettings()
	}

	// Reflect the bootstrap session's current context-window occupancy (#857). A
	// nil seam (optional, foreground / unwired) leaves both at zero — byte-
	// identical to the pre-#857 reply apart from the two always-present fields.
	// The cmd/pyry closure collapses any transcript-open failure to the same
	// zero/window-default report, so this read never errors.
	var usedTokens, windowTokens int
	if m.cfg.SnapshotUsage != nil {
		usedTokens, windowTokens = m.cfg.SnapshotUsage()
	}

	snapPayload, err := json.Marshal(protocol.ScreenSnapshotPayload{
		ConversationID: payload.ConversationID,
		Text:           text,
		TS:             time.Now().UTC(),
		Model:          model,
		Effort:         effort,
		YOLO:           yolo,
		UsedTokens:     usedTokens,
		WindowTokens:   windowTokens,
	})
	if err != nil {
		// ScreenSnapshotPayload is a closed struct of scalars + a time; marshal
		// cannot fail in practice. Defensive — NEVER echo err (it could quote the
		// rendered text). Fall back to a deterministic error reply so the request
		// is still answered, never silently dropped (AC #3).
		m.cfg.Logger.Warn("relay: v2 screen_snapshot marshal failed",
			"event", "v2.snapshot.marshal_err",
			"conn_id", s.connID,
			"conversation_id", payload.ConversationID)
		m.snapshotReplyError(ctx, s, env.ID, protocol.CodeServerBinaryOffline, msgSnapshotOffline, true)
		return
	}
	inReplyTo := env.ID
	reply := protocol.Envelope{
		ID:        1, // non-load-bearing; the phone correlates on InReplyTo.
		Type:      protocol.TypeScreenSnapshot,
		TS:        time.Now().UTC(),
		Payload:   snapPayload,
		InReplyTo: &inReplyTo,
	}
	// Before the served log, which would otherwise claim a delivery (#1526).
	if m.dropInlineReplyIfDown(s, "v2.snapshot.dropped_transport_down") {
		return
	}
	m.cfg.Logger.Info("relay: v2 screen snapshot served",
		"event", "v2.snapshot.served",
		"conn_id", s.connID,
		"conversation_id", payload.ConversationID)
	if err := m.forwardEnvelope(ctx, s.connID, reply); err != nil {
		// Unreachable in practice: s is V2StateOpen on the dispatch goroutine.
		// Logged at debug and dropped — the package's outbound-drop posture;
		// NEVER echo the rendered text.
		m.cfg.Logger.Debug("relay: v2 screen_snapshot push dropped",
			"event", "v2.snapshot.push_err",
			"conn_id", s.connID,
			"err", err)
	}
}

// snapshotReplyError pushes a single TypeError reply to s, correlated to
// inReplyTo, via the same m.forwardEnvelope seal-and-forward path the success
// reply uses (no parallel send path). message MUST be a static constant —
// never attacker-controlled bytes.
func (m *V2SessionManager) snapshotReplyError(ctx context.Context, s *V2Session, inReplyTo uint64, code, message string, retryable bool) {
	errPayload, err := json.Marshal(protocol.ErrorPayload{
		Code:      code,
		Message:   message,
		Retryable: retryable,
	})
	if err != nil {
		// A closed struct of strings + bool; marshal cannot fail in practice.
		m.cfg.Logger.Warn("relay: v2 snapshot error reply marshal failed",
			"event", "v2.snapshot.err_marshal",
			"conn_id", s.connID,
			"code", code)
		return
	}
	reply := protocol.Envelope{
		ID:        1, // non-load-bearing; the phone correlates on InReplyTo.
		Type:      protocol.TypeError,
		TS:        time.Now().UTC(),
		Payload:   errPayload,
		InReplyTo: &inReplyTo,
	}
	if m.dropInlineReplyIfDown(s, "v2.snapshot.err_dropped_transport_down") {
		return
	}
	if err := m.forwardEnvelope(ctx, s.connID, reply); err != nil {
		m.cfg.Logger.Debug("relay: v2 snapshot error reply push dropped",
			"event", "v2.snapshot.err_push",
			"conn_id", s.connID,
			"code", code,
			"err", err)
	}
}

// dropInlineReplyIfDown reports whether an inline reply must be dropped
// unsealed because the relay leg is down (#1526), logging the drop. The inline
// replies (snapshot, settings, bundle-error, resync) call forwardEnvelope
// directly on Run, outside the push drain, so each needs the pre-seal probe
// drainOnce (#874), handleWake (#912) and forwardAppReply (#1525) already have:
// m.send swallows its Outbound error, so a reply sealed while down spends a
// send-nonce the phone never sees, and the next delivered frame fails AEAD and
// kills the session. Reacting to the post-send error is too late.
//
// The probe sits at each call site, not inside forwardEnvelope: drainReplayOnce
// would read a transport-down return from forwardEnvelope as a failed replay and
// abandon the whole tail on a blip. The down path is a drop, not a park, like
// #1525 — the reply was undeliverable anyway — so recovery needs no flush.
//
// event is a per-site constant slug. The line is Debug and content-free: slug,
// conn-id and reason only, never payload, plaintext, ciphertext, key bytes or
// settings values (#833). Runs on Run, the single owner of s.send; the probe
// needs no lock, and the single-frame TOCTOU at the up→down instant carries over
// from #874.
func (m *V2SessionManager) dropInlineReplyIfDown(s *V2Session, event string) bool {
	if !m.transportDown() {
		return false
	}
	m.cfg.Logger.Debug("relay: v2 inline reply dropped; transport down",
		"event", event,
		"conn_id", s.connID,
		"reason", "transport_down")
	return true
}

// SetReplaySource publishes the mid-turn-reconnect replay source to the manager
// (#647). It is called once during relay wiring, AFTER the interactive emitter
// (which owns the eventring) is constructed: the emitter and manager have a
// circular dependency (the emitter takes the manager as its broadcaster; the
// replay path needs the emitter-owned ring), so the ring does not exist when
// NewV2SessionManager runs — a construction-time V2SessionConfig field is not
// buildable without the constructor cascade #646 avoided. A late-bound setter
// is the seam that breaks the cycle. ring is the emitter's per-conversation
// event ring; currentConv resolves the conversation a reconnecting conn replays
// for (the supervisor's #312 cursor).
//
// Stored under pushMu (the existing leaf lock, taken alone): this write happens
// off the Run goroutine during wiring, and replayMissed reads them on the Run
// goroutine at reconnect — much later, after a full network handshake. A nil
// ring or cursor leaves replay disabled. Idempotent by construction (the wiring
// calls it once).
func (m *V2SessionManager) SetReplaySource(ring *eventring.Ring, currentConv func() string) {
	m.pushMu.Lock()
	defer m.pushMu.Unlock()
	m.replayRing = ring
	m.replayCursor = currentConv
}

// replayMissed classifies a mid-turn-reconnect for s after its hello advertised
// last_event_id=afterID (#647) and, when there is a tail to replay, hands it to
// the paced replay drain (#777). It runs on the manager's single Run goroutine
// at the tail of handleNoiseInit's success path. The classification work — the
// ring read, the gap→resync branch, and the #663 watermark clamp — completes
// inline; the tail itself is stored in s.replayQueue and forwarded one event per
// Run pass by drainReplayOnce, so a large replay never monopolises Run. Because
// replayMissed populates replayQueue on this pass (before Run returns to its
// select) and the drainOnce gate holds the conn's live push queue until the tail
// empties, every replay frame still reaches the wire before any live frame for
// this conn — AC-2's "before the live stream resumes", now preserved by the gate
// rather than by inline completion. Replay frames seal via forwardEnvelope (the
// established handleRequestSnapshot inline-reply pattern), not the buffered push
// stream.
//
// afterID is untrusted remote input (AC-5): it is only ever an index into the
// self-synchronised ring (ring.After's own mutex makes the read safe off the
// emitter goroutine), scoped to the daemon-resolved conversation (cursor()),
// never to a conversation the phone names. Work is bounded by what the ring
// retains (MaxEventsPerConversation); a hostile-large id classifies as a gap
// (#1494) — one resync marker, still bounded work, and the untrusted value is
// never written to per-conn state. SECURITY: replayed payloads are the same
// structured envelopes #649 already streams to this authenticated conn; the
// bytes are never logged.
func (m *V2SessionManager) replayMissed(ctx context.Context, s *V2Session, afterID uint64) {
	m.pushMu.Lock()
	ring, cursor := m.replayRing, m.replayCursor
	m.pushMu.Unlock()
	if ring == nil || cursor == nil {
		return // replay disabled: no SetReplaySource, or the stream is off.
	}
	convID := cursor()
	if convID == "" {
		return // no active conversation; nothing to catch up on.
	}

	// Read the newest retained id BEFORE classifying with After: any event the
	// emitter appends concurrently then carries an id > newest and reaches the
	// live stream instead of the clamp below (staleness can only lower the
	// watermark — deliver more — never raise it, the safe direction for the
	// never-a-silent-gap guarantee).
	newest := ring.NewestID(convID)
	events, gap := ring.After(convID, afterID)
	if gap {
		// The requested position aged out of the bounded ring (AC-4), or names an
		// id this daemon never issued — a stale cross-/clear id, a post-restart
		// cursor, a hostile 2^64-1 (#1494): emit one honest resync marker telling
		// the phone to full-reload, never a partial gap-ful replay. Leave
		// replayThrough untouched — the phone discards its cursor and must accept
		// all live events afterward.
		m.emitResync(ctx, s, convID)
		return
	}
	// Clamp the watermark to server-known reality (#663). Since #1494 every
	// afterID above this conversation's id space returns at the gap branch above,
	// so what the clamp still defends is the window between the NewestID and
	// After reads: an Append landing there makes an afterID that was out of range
	// at the first read caught-up at the second, and min holds the watermark at
	// the stale-but-real newest so the concurrently appended event is not muted.
	// min also preserves legitimate dedup (afterID == newest in the caught-up
	// case); during the drain the watermark trails one event behind the frame
	// being forwarded, so forwardEnvelope's guard never self-drops a replay
	// envelope.
	//
	// The watermark this writes is a per-conn scalar with no conversation tag,
	// while it is derived from one conversation — the one cursor() resolved. That
	// mismatch was a real defect until #2022: ring ids restarted at 1 per
	// conversation, so a rotation to another conversation produced live ids at or
	// below this value and forwardEnvelope silently dropped them. Ids are now
	// unique ring-wide, so newest is a point in a single ordering that every
	// later event of every conversation is above. Nothing downstream re-checks
	// the conversation, and nothing needs to.
	s.replayThrough = min(afterID, newest)
	if len(events) == 0 {
		return // caught up: After returned no tail; the clamp above is all the work.
	}
	// Hand the tail to the paced drain instead of forwarding it inline (#777):
	// sealing and forwarding up to MaxEventsPerConversation frames in this single
	// Run pass would stall every other conn's delivery (and inbound frames,
	// wakes, snapshots) until the whole batch finished. drainReplayOnce forwards
	// one event per Run pass, advancing replayThrough as it goes, so Run returns
	// to its select between frames. events is the fresh copy ring.After already
	// materialised — bounded by ring retention, never the push cap, so no
	// drop/gap on this path.
	s.replayQueue = events
	select {
	case m.replayCh <- struct{}{}:
	default:
	}
}

// emitResync forwards a single resync marker to s, signalling that its
// advertised last_event_id is not a position the ring can replay from — it aged
// out of the bounded window (#647, AC-4) or lies beyond the conversation's id
// space (#1494) — and that it must do a full reload of convID. The marker is a
// TypeResync control envelope carrying only convID in an inline anonymous
// payload — no named protocol payload type, mirroring emitRekeyRequest's
// payload-less inline-struct control precedent. It carries NO EventID (it is not
// a structured event), so forwardEnvelope's replay-watermark guard never touches
// it.
//
// SECURITY: convID is the daemon's own resolved conversation id, never
// attacker-derived; the marker exposes no buffered conversation content.
func (m *V2SessionManager) emitResync(ctx context.Context, s *V2Session, convID string) {
	payload, err := json.Marshal(struct {
		ConversationID string `json:"conversation_id"`
	}{ConversationID: convID})
	if err != nil {
		// A closed struct of one string; marshal cannot fail in practice.
		m.cfg.Logger.Warn("relay: v2 resync marshal failed",
			"event", "v2.replay.resync_marshal_failed",
			"conn_id", s.connID)
		return
	}
	marker := protocol.Envelope{
		ID:      1, // non-load-bearing; the phone keys resync on Type, not ID.
		Type:    protocol.TypeResync,
		TS:      time.Now().UTC(),
		Payload: payload,
	}
	// Before the resync log, which would otherwise claim a delivery (#1526).
	if m.dropInlineReplyIfDown(s, "v2.replay.resync_dropped_transport_down") {
		return
	}
	m.cfg.Logger.Info("relay: v2 reconnect resync",
		"event", "v2.replay.resync",
		"conn_id", s.connID,
		"conversation_id", convID)
	if err := m.forwardEnvelope(ctx, s.connID, marker); err != nil {
		m.cfg.Logger.Debug("relay: v2 resync marker dropped",
			"event", "v2.replay.resync_dropped",
			"conn_id", s.connID,
			"err", err)
	}
}

// drainReplayOnce forwards at most ONE buffered reconnect-replay event across
// all sessions on the Run goroutine, then returns to the select — the same
// one-per-pass yielding drainOnce provides for the push stream, so a large
// mid-turn-reconnect replay (up to MaxEventsPerConversation events) can no
// longer monopolise Run and stall other conns' delivery, inbound frames, wakes,
// or snapshots (#777, AC #1). replayQueue and replayThrough are Run-owned (no
// pushMu): the scan, pop, and watermark advance all run on this goroutine — the
// same single-writer regime forwardEnvelope's s.send.Encrypt relies on, so the
// Noise send-nonce sequence stays monotonic (AC #3).
//
// Re-signal discipline mirrors drainOnce: when the popped conn's tail empties it
// signals drainCh to release the live events the drainOnce gate held back (they
// carry ids > replayThrough, so they drain in order after the replay, AC #2);
// while any session still has a tail it re-signals replayCh to keep the pump
// running (covers concurrent multi-conn replays through the single cap-1
// channel). On a forward error the conn's remaining tail is abandoned (matching
// the old inline loop's early return), but the re-signal scan still runs so a
// different session's replay is never stranded.
func (m *V2SessionManager) drainReplayOnce(ctx context.Context) {
	// Transport-down HOLD (#1490), the replay twin of drainOnce's #874 hold: pop
	// nothing, seal nothing, advance no replayThrough, re-signal nothing. m.send
	// swallows the Outbound error, so a replay frame sealed while down spends a
	// send-nonce the phone never sees and the next delivered frame fails AEAD —
	// and every later pass would consume the rest of the tail the same way. Not
	// re-signalling replayCh keeps a down leg from busy-spinning Run; Run's
	// Reconnect arm re-signals it on recovery, so a held tail needs a wired
	// Reconnect seam to resume (production wires it with Connected). The probe
	// sits here, not in forwardEnvelope, whose error return takes the abandon
	// branch below and would throw the tail away (see dropInlineReplyIfDown).
	if m.transportDown() {
		return
	}
	var s *V2Session
	// Go randomises map-range order, giving rough cross-conn fairness across the
	// realistically-tiny open-conn count (same as drainOnce).
	for _, cand := range m.sessions {
		if len(cand.replayQueue) > 0 {
			s = cand
			break
		}
	}
	if s == nil {
		return
	}

	ev := s.replayQueue[0]
	s.replayQueue[0] = eventring.Event{} // release the event for GC; slot slides out below
	s.replayQueue = s.replayQueue[1:]    // pop head (ascending id order)

	id := ev.ID // per-frame local; never &ev.ID of the loop-scoped copy.
	replay := protocol.Envelope{
		ID:      ev.ID, // per-conn id ascending + self-consistent within the replay.
		Type:    ev.Type,
		TS:      ev.TS,
		Payload: ev.Payload,
		EventID: &id, // required: the phone advances its cursor from this.
	}
	if err := m.forwardEnvelope(ctx, s.connID, replay); err != nil {
		// Session vanished / seal failure: log at debug and abandon this conn's
		// remaining tail — the package's outbound-drop posture (mirrors the old
		// inline loop's early return). NEVER echo payload/ciphertext/key bytes.
		m.cfg.Logger.Debug("relay: v2 reconnect replay frame dropped",
			"event", "v2.replay.frame_dropped",
			"conn_id", s.connID,
			"err", err)
		s.replayQueue = nil
	} else {
		s.replayThrough = ev.ID // trailing-watermark advance (identical to the old inline loop).
	}

	// This conn's replay is done (drained to empty or abandoned on error):
	// release the live events the drainOnce gate held back for it.
	if len(s.replayQueue) == 0 {
		select {
		case m.drainCh <- struct{}{}:
		default:
		}
	}
	// Keep the pump running while ANY session still has a replay tail.
	for _, cand := range m.sessions {
		if len(cand.replayQueue) > 0 {
			select {
			case m.replayCh <- struct{}{}:
			default:
			}
			break
		}
	}
}
