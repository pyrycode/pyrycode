package relay

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// Push enqueues env onto the addressed session's bounded push buffer and
// returns immediately — it NEVER blocks on the relay/send path. The Run
// goroutine drains the buffer on its own schedule (drainOnce), sealing each
// envelope under s.send in order; so a slow or stalled relay can never wedge
// the calling producer/dispatch goroutine (the ADR-025 open-risk guard,
// decisions/025 line 220). Safe to call from any goroutine: Push touches only
// m.queues (under pushMu) — never s.send, m.sessions, or Outbound.
//
// connID names a specific connected phone. The caller owns env entirely
// (Type, ID, TS, Payload); Push performs no envelope validation — it is a
// transport primitive.
//
// Under pressure (queue at capacity) the event-class-aware drop policy runs
// pre-seal (pushQueue.enqueue): an assistant_delta evicts the oldest queued
// delta (drop-oldest); a control event is admitted by evicting a droppable
// delta and is never itself dropped. A drop is not an error — Push returns nil
// and debug-logs the running count + the dropped class (env.Type only; never
// payload bytes).
//
// Past pushQueueByteCeiling the soft-overflow admission stops: the envelope is
// rejected, pushQueue.overflowed latches, and this Push signals m.pushOverflow
// so Run tears the session down (handlePushOverflow, #1505). That is still not
// an error the producer sees — the call returns nil, and promptly; the overflow
// is not the producer's failure, and both production callers only debug-log an
// error anyway. Pushes that follow the teardown find no queue and get the
// pre-existing ErrConnNotFound, so no new error value ever reaches a producer.
//
// Returns ErrConnNotFound (wraps control.ErrConnNotFound) when no queue exists
// for connID — i.e. the session never reached V2StateOpen, was never seen, or
// has been torn down (closeWith deletes the queue). This collapses the former
// "session not open" case into ErrConnNotFound: a not-open conn has no queue.
// The V2StateOpen security gate is preserved on the drain side — forwardEnvelope
// re-checks s.state before sealing, so a buffered push to a conn that closed or
// de-authed before drain is dropped there, never delivered to an
// un-authenticated peer. Returns ctx.Err() only when ctx is already cancelled
// at entry (preserves the emitter's ctx-teardown branch). Both production
// callers (#632 emitter, #589 coarse bridge) only debug-log the error, so the
// collapse is invisible to them.
func (m *V2SessionManager) Push(ctx context.Context, connID string, env protocol.Envelope) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.pushMu.Lock()
	q, ok := m.queues[connID]
	if !ok {
		m.pushMu.Unlock()
		return ErrConnNotFound
	}
	before := q.overflowed
	dropped := q.enqueue(env)
	droppedCount := q.dropped
	// Capture both the LEVEL (still latched ⇒ keep signalling) and the EDGE
	// (just tripped ⇒ log once) under the same hold, so neither can be read
	// against a queue another producer has since mutated.
	overflowed, justTripped := q.overflowed, q.overflowed && !before
	retained := q.bytes
	m.pushMu.Unlock()

	// Non-blocking wake: a cap-1 channel + this default coalesces concurrent
	// signals; the drain re-signals if more work remains, so no wake is lost.
	select {
	case m.drainCh <- struct{}{}:
	default:
	}

	if overflowed {
		// Level-triggered: signal on EVERY push while latched, so a send this
		// default dropped (full buffer) is re-driven by the next one. See
		// V2SessionManager.pushOverflow for why this must not block.
		select {
		case m.pushOverflow <- connID:
		default:
		}
	}
	if justTripped {
		// Edge-triggered, so an operator sees exactly one Warn per session
		// however many pushes follow. Warn, not Debug: unlike the routine
		// per-envelope drop below, this ends the conn. Content-free — bytes and
		// ceiling are lengths, and type is the same discriminator v2.push.drop
		// already carries.
		m.cfg.Logger.Warn("relay: v2 push queue byte ceiling exceeded",
			"event", "v2.push.ceiling",
			"conn_id", connID,
			"bytes", retained,
			"ceiling", pushQueueByteCeiling,
			"type", env.Type)
	}
	if dropped {
		m.cfg.Logger.Debug("relay: v2 push drop under pressure",
			"event", "v2.push.drop",
			"conn_id", connID,
			"dropped", droppedCount,
			"type", env.Type)
	}
	return nil
}

// handlePushOverflow tears down the session whose push queue latched
// pushQueue.overflowed, freeing every retained byte immediately instead of
// waiting out the 15-minute idle sweep (#1505). Run-goroutine only — reached
// from Run's m.pushOverflow arm — so the m.sessions read and the closeWith call
// sit under the package's single-owner invariant, exactly like handleWake's
// wakeIdleTimeout case. An absent session (already torn down; its queue went
// with it) or a non-open one is a no-op, mirroring handleWake's state guard.
//
// It deliberately does NOT re-check whether the queue has since drained back
// under the ceiling. Run's select is unordered, so several drainOnce passes can
// run between the signal and this arm, and a recovered transport may have
// emptied the queue by now — the session is torn down anyway. Envelopes were
// already discarded when the latch tripped, and only the re-handshake this
// teardown forces replays them (#647 replay + reconcileModals / reconcileQueues,
// none of which fire on transport recovery alone). A "kinder" re-check would
// leave a surviving session with a permanent silent gap in the phone's turn
// stream and nothing to reconcile it; the cost of tearing down is one extra
// handshake in a rare race.
func (m *V2SessionManager) handlePushOverflow(ctx context.Context, connID string) {
	s, ok := m.sessions[connID]
	if !ok {
		return
	}
	if s.state != V2StateOpen {
		return
	}
	m.cfg.Logger.Warn("relay: v2 push queue ceiling teardown",
		"event", "v2.push.ceiling.teardown",
		"conn_id", connID,
		"close_code", int(StatusQueueOverflow))
	m.closeWith(ctx, s, StatusQueueOverflow, nil)
}

// transportDown reports whether the push drain should hold the head rather than
// seal-and-forward it (#874). A nil Connected seam ⇒ never down ⇒ the drain path
// is byte-identical to pre-#874 (the foreground / unwired / existing-test case).
// Called off-lock on the Run goroutine, BEFORE pushMu, so pushMu's "taken alone,
// never held across an external call" invariant is preserved.
func (m *V2SessionManager) transportDown() bool {
	return m.cfg.Connected != nil && !m.cfg.Connected()
}

// drainOnce pops at most ONE buffered envelope across all sessions and forwards
// it on the Run goroutine. Popping one-per-pass (rather than draining a whole
// buffer) is the "a slow m.send must not re-block the producer" guard: Run
// returns to its select between sends, so ActiveConns / inbound frames / wakes
// are serviced with at most one in-flight Outbound (≤ one WriteTimeout) of
// delay. If any queue still has items after the pop, it re-signals drainCh.
func (m *V2SessionManager) drainOnce(ctx context.Context) {
	// Transport-down HOLD (#874): if the relay leg is down, pop nothing, seal
	// nothing, re-signal nothing — leave the head un-popped and unsealed so no
	// Noise send-nonce is burned for a frame that cannot reach the phone (a
	// burned nonce gaps the phone's recv nonce → 4421 close of a still-live
	// session). The probe runs off-lock, before pushMu, and BEFORE the seal in
	// forwardEnvelope — the post-send Outbound error is too late, the nonce is
	// already gone. The next Push re-signals drainCh (the existing lazy flush),
	// which re-enters here; once the transport recovers the held head drains in
	// FIFO order (immediate flush-on-reconnect is #875). Session-level failures
	// (ErrConnNotFound / ErrSessionNotOpen / seal failure) still drop, downstream
	// in forwardEnvelope, reached only on this transport-up path (AC2).
	if m.transportDown() {
		return
	}
	// A conn with an in-flight reconnect-replay tail must keep its live push
	// queue buffered until the replay finishes, so replay ids (<= replayThrough)
	// reach the wire before live ids (#777, AC #2). Snapshot the gated conn-ids
	// from m.sessions BEFORE taking pushMu: m.sessions and replayQueue are
	// Run-owned and drainOnce runs on Run, so this read needs no lock, and
	// keeping it outside pushMu preserves pushMu's "guards only m.queues, taken
	// alone" invariant. drainReplayOnce re-signals drainCh when a tail empties,
	// so a gated conn's buffered live events are never stranded.
	var replayPending map[string]struct{}
	for id, s := range m.sessions {
		if len(s.replayQueue) > 0 {
			if replayPending == nil {
				replayPending = make(map[string]struct{})
			}
			replayPending[id] = struct{}{}
		}
	}

	m.pushMu.Lock()
	var (
		connID  string
		env     protocol.Envelope
		found   bool
		barrier chan struct{}
	)
	// Go randomises map-range order, giving rough fairness across the
	// realistically-tiny open-conn count.
	for id, q := range m.queues {
		if len(q.items) == 0 {
			continue
		}
		if _, gated := replayPending[id]; gated {
			continue // replay in flight for this conn; hold its live events (#777).
		}
		connID = id
		barrier = q.items[0].barrier
		env = q.popHead()
		found = true
		break
	}
	// After the pop, note whether any UNGATED queue still has work to re-signal.
	// Gated queues are excluded: their re-signal comes from drainReplayOnce when
	// the replay tail empties, not from the push pump.
	more := false
	if found {
		for id, q := range m.queues {
			if len(q.items) == 0 {
				continue
			}
			if _, gated := replayPending[id]; gated {
				continue
			}
			more = true
			break
		}
	}
	m.pushMu.Unlock()

	if !found {
		return
	}
	if barrier != nil {
		close(barrier)
	} else if err := m.forwardEnvelope(ctx, connID, env); err != nil {
		// Session vanished / not open / seal failure: drop with no app content
		// in the log (the package's outbound-drop posture). The V2StateOpen
		// security gate lives in forwardEnvelope.
		m.cfg.Logger.Debug("relay: v2 push drain drop",
			"event", "v2.push.drain_drop",
			"conn_id", connID,
			"err", err)
	}
	if more {
		select {
		case m.drainCh <- struct{}{}:
		default:
		}
	}
}

// forwardEnvelope runs on Run's dispatch goroutine. It looks up the session,
// requires V2StateOpen (the security gate that keeps server output away from an
// un-authenticated or torn-down peer), then seals env under s.send and forwards
// a noise_msg — reusing emitRekeyRequest's marshal→Encrypt→wrap→send sequence
// (minus the rekey bookkeeping). It is the single existing seal-and-forward
// path, shared by the push-buffer drain (drainOnce) and the snapshot error-reply
// helper (snapshotReplyError, which calls it directly because its replies are
// InReplyTo-correlated and not part of the ordered push stream).
//
// Reads s.send at execution time on the dispatch goroutine, so it always
// uses the current CipherState and composes with re-key swaps: a forward
// either seals fully under the old key or fully under the new key, never
// a torn read.
//
// The seal/marshal error paths return wrapped errors (the caller decides
// log level); they MUST NOT echo env, plaintext, ciphertext, or key
// bytes, matching the package's no-AEAD-bytes-in-logs discipline.
func (m *V2SessionManager) forwardEnvelope(_ context.Context, connID string, env protocol.Envelope) error {
	s, ok := m.sessions[connID]
	if !ok {
		// A torn-down session was already deleted from the map by
		// closeWith, so "closed" collapses into this same branch.
		return ErrConnNotFound
	}
	if s.state != V2StateOpen {
		return ErrSessionNotOpen
	}
	// A frame about a Codex conversation, for a conn without multi_agent (#2644).
	// Nil, not an error: drainReplayOnce abandons a conn's replay tail on an error
	// and must instead advance past a withheld event to the ones behind it. Before
	// the seal, so a withheld frame spends no send-nonce.
	if m.withheldFromConn(s, env) || m.threadWithheld(s, env) {
		return nil
	}
	// Reconnect-replay dedup (#647): drop a live structured envelope this conn
	// already received via replay. Envelopes with EventID == nil (snapshot,
	// error, rekey, resync) are never structured events and are never dropped;
	// conns that never advertised last_event_id keep replayThrough == 0 and
	// live ids are always >= 1, so the guard is inert for them.
	//
	// replayThrough is a single per-conn scalar carrying no conversation tag,
	// and this is deliberately NOT compensated for here. It is sound because
	// eventring assigns ids from one ring-wide counter (#2022): every event
	// appended after the watermark was taken carries a higher id, whatever
	// conversation it belongs to, so the guard can only ever drop something this
	// conn was actually given. Before that the id spaces overlapped, and a
	// watermark clamped for the conversation a conn's replay came from muted
	// every lower-id event of any conversation the daemon later rotated to.
	// Making this comparison conversation-aware instead would mean plumbing the
	// conversation from the emitter through Push to here, for a failure the id
	// space already removes.
	if env.EventID != nil && *env.EventID <= s.replayThrough {
		return nil
	}
	// Per-conversation revision ordering for reply_suggestion (#2830): a stale
	// connect-time snapshot draining behind a newer live clear is dropped here.
	// Recorded only after m.send, so a frame that never reached the wire does not
	// advance the watermark.
	suggestionConv, suggestionRev, stale := replySuggestionStale(s, env)
	if stale {
		return nil
	}
	env = m.mergedForConn(s, env)
	env = m.agentTaggedForConn(s, env)
	env = m.threadProjected(s, env)
	envJSON, err := json.Marshal(env)
	if err != nil {
		// Defensive: a well-typed envelope (e.g. a message envelope, a
		// closed struct of strings) does not fail to marshal in practice.
		return fmt.Errorf("marshal push envelope: %w", err)
	}
	ciphertext, err := s.send.Encrypt(envJSON)
	if err != nil {
		// Realistically unreachable under correct flynn/noise.
		return fmt.Errorf("seal push envelope: %w", err)
	}
	frame, err := marshalInnerFrameV2(protocol.TypeNoiseMsg, ciphertext)
	if err != nil {
		return fmt.Errorf("marshal push frame: %w", err)
	}
	m.send(protocol.RoutingEnvelope{ConnID: s.connID, Frame: frame})
	if suggestionRev > 0 {
		recordReplySuggestion(s, suggestionConv, suggestionRev)
	}
	return nil
}

// mergedForConn returns env as s is to be sent it: for a conn that negotiated
// protocol.CapabilityMultiAgent, a pushed model_list whose Claude entries are
// replaced by MergedModelOptions' merged, tagged list (#2652); every other frame,
// and every frame to any other conn, unchanged. conversation_id and
// dropped_models are the pushed frame's.
//
// A pushed model_list is told apart by its EventID: the turn lane stamps one on
// every live push and drainReplayOnce on every replay. #2651's two paths leave it
// nil — the request_model_list reply and reconcileModelLists' connect-time
// snapshot — and both already carry the merged list, so merging them here would
// re-tag Codex's entries as Claude's and append them twice. InReplyTo alone does
// not separate them: the reconcile snapshot has none either.
//
// The pushed envelope is shared by every conn and the replay ring, so this builds
// the capable conn's copy per call and never writes through env.Payload: env is a
// by-value copy and only its Payload field is reassigned to fresh bytes. A payload
// that does not decode, or a merged one that does not marshal, is delivered
// unchanged. Nothing is logged: the payload is claude-authored (#833).
// Run-goroutine only, like its caller: s.multiAgent is Run-owned.
func (m *V2SessionManager) mergedForConn(s *V2Session, env protocol.Envelope) protocol.Envelope {
	if !s.multiAgent || m.cfg.MergedModelOptions == nil || env.Type != protocol.TypeModelList || env.EventID == nil {
		return env
	}
	var p protocol.ModelListPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return env
	}
	p.Models = m.cfg.MergedModelOptions(p.Models)
	payload, err := json.Marshal(p)
	if err != nil {
		return env
	}
	env.Payload = payload
	return env
}
