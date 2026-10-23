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
	return m.push(ctx, connID, env, nil)
}

func (m *V2SessionManager) push(ctx context.Context, connID string, env protocol.Envelope, live *LiveState) error {
	return m.pushSource(ctx, connID, env, live, false)
}

func (m *V2SessionManager) pushSource(ctx context.Context, connID string, env protocol.Envelope, live *LiveState, ready bool) error {
	m.cancelThreadQuery(connID, env)
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
	dropped := q.enqueueSource(env, live, ready)
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

// handlePushOverflow closes a quarantined session on Run, using the first
// terminal cause recorded under pushMu. Reconnect reconciliation is required
// even if transport recovers before this signal is processed. A nil close frame
// avoids spending a Noise nonce on output a down transport cannot deliver.
func (m *V2SessionManager) handlePushOverflow(ctx context.Context, connID string) {
	s, ok := m.sessions[connID]
	if !ok {
		return
	}
	if s.state != V2StateOpen {
		return
	}
	m.pushMu.Lock()
	q := m.queues[connID]
	if q == nil || !q.overflowed {
		m.pushMu.Unlock()
		return
	}
	producerGap := q.producerGap
	m.pushMu.Unlock()
	if producerGap {
		m.cfg.Logger.Warn("relay: v2 producer gap teardown",
			"event", "v2.push.producer_gap.teardown",
			"conn_id", connID,
			"close_code", int(StatusProducerGap))
		m.closeWith(ctx, s, StatusProducerGap, nil)
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
	if ctx.Err() != nil {
		return
	}
	// A fresh reading parked behind its clear completes before another clear
	// can replace the single pending slot. Each call still sends only one frame.
	for _, s := range m.sessions {
		if s.livePending != nil && !m.pushRetired(s.connID) && m.hostedPushAvailable(s) {
			m.drainLiveOnce(ctx, true)
			return
		}
	}
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
	// Snapshot candidates with pushMu alone. Producers may shed a delta before
	// the second queue read; only Run removes heads and changes session identity.
	m.pushMu.Lock()
	candidates := make([]string, 0, len(m.queues))
	for id, q := range m.queues {
		if !q.overflowed && len(q.items) > 0 {
			candidates = append(candidates, id)
		}
	}
	m.pushMu.Unlock()
	for _, connID := range candidates {
		s := m.sessions[connID]
		if s != nil && s.livePending != nil {
			continue
		}
		m.pushMu.Lock()
		q := m.queues[connID]
		marker := q.items[0].barrier != nil
		m.pushMu.Unlock()
		if !marker && !m.reserveHostedPush(s) {
			continue
		}
		m.pushMu.Lock()
		q = m.queues[connID]
		// Retirement may latch while hosted admission runs off pushMu.
		if q.overflowed {
			m.pushMu.Unlock()
			m.releaseHostedPush(s)
			continue
		}
		head := q.items[0]
		env := q.popHead()
		m.pushMu.Unlock()
		if head.barrier != nil {
			close(head.barrier)
		} else if head.ready {
			m.hostedEnvelope(ctx, s, env)
		} else if head.live != nil {
			if err := m.forwardLiveState(ctx, s, *head.live); err != nil {
				m.cfg.Logger.Warn("relay: supplied live state dropped", "event", "v2.live_state.invalid", "conn_id", connID)
			}
		} else if err := m.forwardEnvelope(ctx, connID, env); err != nil {
			m.cfg.Logger.Debug("relay: v2 push drain drop", "event", "v2.push.drain_drop", "conn_id", connID, "err", err)
		}
		m.releaseHostedPush(s)
		m.signalLiveDrain()
		return
	}
	m.drainLiveOnce(ctx, false)
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
func (m *V2SessionManager) forwardEnvelope(ctx context.Context, connID string, env protocol.Envelope) error {
	return m.forwardEnvelopeSource(ctx, connID, env, nil)
}

func (m *V2SessionManager) forwardEnvelopeSource(ctx context.Context, connID string, env protocol.Envelope, reading *LiveState) error {
	supplied := reading != nil
	s, ok := m.sessions[connID]
	if !ok {
		// A torn-down session was already deleted from the map by
		// closeWith, so "closed" collapses into this same branch.
		return ErrConnNotFound
	}
	if s.state != V2StateOpen {
		return ErrSessionNotOpen
	}
	if s.hosted != nil {
		s.hosted.pushDeferred = false
	}
	// Withheld output consumes neither a sender ID nor a Noise nonce.
	if m.withheldFromConn(s, env) || (!supplied && m.threadWithheld(s, env)) {
		return nil
	}
	// Per-conversation revision ordering for reply_suggestion (#2830): a stale
	// connect-time snapshot draining behind a newer live clear is dropped here.
	// Recorded only after m.send, so a frame that never reached the wire does not
	// advance the watermark.
	suggestionConv, suggestionRev, stale := replySuggestionStale(s, env)
	if stale && !supplied {
		return nil
	}
	unprojected := !supplied && env.InReplyTo == nil || supplied && reading.ModelListUnprojected
	env = m.mergedForConn(s, env, unprojected)
	env = m.agentTaggedForConn(s, env)
	env = m.threadProjected(s, env)
	envJSON, err := json.Marshal(env)
	if err != nil {
		// Defensive: a well-typed envelope (e.g. a message envelope, a
		// closed struct of strings) does not fail to marshal in practice.
		return fmt.Errorf("marshal push envelope: %w", err)
	}
	if s.thread && liveStateFamily(env.Type) && len(envJSON) > protocol.MaxThreadEnvelopeBytes {
		return errInvalidLiveState
	}
	if s.hosted != nil {
		s.hosted.pushDeferred = m.hostedEnvelope(ctx, s, env)
		if suggestionRev > 0 && !supplied {
			recordReplySuggestion(s, suggestionConv, suggestionRev)
		}
		return nil
	}
	s.nextID = max(s.nextID, min(env.ID, hostedMaxID)+1)
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
	if suggestionRev > 0 && !supplied {
		recordReplySuggestion(s, suggestionConv, suggestionRev)
	}
	return nil
}

// mergedForConn projects canonical model rows for a multi_agent recipient.
// Unsolicited pushes are canonical; supplied readings mark their projection
// explicitly. Correlated replies already carry their projection.
// Clears stay empty. Shared payload bytes are never mutated, and conversation
// and dropped-model fields are preserved. Runs only on the Run goroutine.
func (m *V2SessionManager) mergedForConn(s *V2Session, env protocol.Envelope, unprojected bool) protocol.Envelope {
	if !s.multiAgent || m.cfg.MergedModelOptions == nil || env.Type != protocol.TypeModelList || env.SessionStateCleared || !unprojected {
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
