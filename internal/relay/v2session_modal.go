package relay

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// This file holds the modal lifecycle plus the queue reconcile/dequeue
// handlers — both reconcile per-session state on reconnect — carved out
// of v2session.go (#1023). Pure move: same package, no behaviour change.
// The V2Session / V2SessionManager structs, the Run loop, and the
// ModalResolver seam interface (plus its V2SessionConfig.ModalResolver
// field) stay in v2session.go; the seam interfaces are their own later
// slice. #1021 carved out the initiator handshake and #1022 the rekey
// machinery before this slice.

// queuedEnv is one unsealed envelope buffered in a per-session pushQueue.
// droppable is precomputed from env.Type so the drop policy never re-derives
// the class while scanning under pushMu. Envelopes are held UNSEALED: the
// Noise send nonce is strictly sequential, so dropping a sealed frame would
// gap the phone's recv nonce (MAC failure → 4421 close). Sealing happens on
// the Run goroutine, in order, only for the frames that are actually sent.
type queuedEnv struct {
	env       protocol.Envelope
	droppable bool // env.Type == protocol.TypeAssistantDelta
}

// pushQueue is a per-session bounded FIFO of unsealed envelopes awaiting the
// Run-side seal-and-forward. It is owned by V2SessionManager and guarded in
// full by m.pushMu; it holds no lock of its own. The event-class-aware drop
// policy lives in enqueue (ADR 025 § Backpressure: assistant_delta drop-oldest,
// control never drops).
type pushQueue struct {
	items   []queuedEnv // FIFO; bounded by the drop policy at pushQueueCap
	dropped uint64      // observability counter; no app content
}

// enqueue applies the droppable-delta drop policy and appends env, returning
// whether a drop occurred (so Push can debug-log after releasing pushMu). The
// caller MUST hold m.pushMu. The method only ever removes existing entries and
// appends at the tail, so the relative order of every surviving envelope is
// preserved (AC#4).
//
// Drop policy (AC#2/#3):
//   - below cap: append.
//   - at cap with a queued delta: evict the OLDEST queued delta, then append
//     the incoming event (delta or control). A control event is admitted by
//     evicting a droppable delta, never by dropping a control event.
//   - at cap with no queued delta (all control), incoming delta: drop the
//     incoming delta (loss-tolerant; cannot evict a control event).
//   - at cap with no queued delta (all control), incoming control: admit past
//     nominal cap (documented soft overflow — see § Design in the spec). The
//     trilemma bounded ∧ never-drop-control ∧ never-block-producer is
//     unsatisfiable here; we yield "strictly bounded". This state IS reachable:
//     StreamBundle (#812) enqueues control-class debug_bundle_chunk /
//     debug_bundle_done frames, so one request_debug_bundle can drive hundreds
//     of never-droppable control events onto a connected-but-very-slow relay
//     with zero interleaved text. What keeps it bounded is
//     handleDebugBundleRequest's per-conn in-flight gate (#911): while a conn's
//     queue still holds any bundle frame, further request_debug_bundle on that
//     conn are rejected before StreamBundle, so a single conn accumulates at
//     most one bundle's chunks (bounded ≈ 4/3 × archive) and retries can no
//     longer stack unbounded memory.
func (q *pushQueue) enqueue(env protocol.Envelope) bool {
	qe := queuedEnv{env: env, droppable: env.Type == protocol.TypeAssistantDelta}
	if len(q.items) < pushQueueCap {
		q.items = append(q.items, qe)
		return false
	}
	// At capacity. Evict the oldest queued delta (the first droppable from the
	// front) to make room for the incoming event, delta or control.
	for i := range q.items {
		if q.items[i].droppable {
			q.items = slices.Delete(q.items, i, i+1)
			q.items = append(q.items, qe)
			q.dropped++
			return true
		}
	}
	// No droppable delta queued: every buffered event is control.
	if qe.droppable {
		// Drop the incoming delta — cannot evict a control event.
		q.dropped++
		return true
	}
	// Soft overflow: admit the control event past nominal cap. Reachable via
	// StreamBundle's bundle chunks; bounded per conn by #911's in-flight gate.
	q.items = append(q.items, qe)
	return false
}

// pushQueueCap bounds the per-session push buffer (count of envelopes, not
// bytes). Starting value pending the ADR-025 load test (decisions/025 line
// 220): post-#609 coalescing makes deltas arrive per-message/~250 ms, so 256
// gives ample headroom to ride out one transport WriteTimeout window without
// dropping while bounding worst-case per-session memory. Control events may
// push the queue past this: StreamBundle's debug_bundle_chunk / debug_bundle_done
// frames are control-class, so one request_debug_bundle can soft-overflow the
// queue (see pushQueue.enqueue). #911's per-conn in-flight gate bounds that to
// one bundle's chunks per conn (≈ 4/3 × archive); it no longer relies on the
// all-control saturated state being unreachable.
const pushQueueCap = 256

// modalDenyTimeout is the bounded window between a surfaced modal and the
// fail-closed safe-deny: if no modal_answer / modal_cancel resolves it first,
// the daemon denies it (ESC) so a permission claude is waiting on can never
// linger or be silently granted. ADR 025 § Security model specifies "a bounded
// window" but no number; 2 minutes balances "long enough for a human to react to
// a push notification and tap" against "short enough not to leave claude
// blocked." Test-overridable (lowercase, save/restore in tests); not part of the
// public API and not yet config-driven (a deferred #708 concern).
var modalDenyTimeout = 2 * time.Minute

// ModalDismissal is the wire outcome+source the manager broadcasts after a
// resolver consumes an outstanding modal. The manager already holds modal_id
// (from the inbound control payload), so it is not repeated here.
type ModalDismissal struct {
	Outcome string // e.g. "cancelled" (cancel); #717 uses the answered option_id
	Source  string // closed set {remote, local, timeout}; cancel ⇒ "remote"
}

// ArmModalTimeout arms the daemon-side deny-on-timeout for a surfaced modal
// (#725). The surfacer calls it (off the Run goroutine) immediately after
// reg.Record; the time.AfterFunc callback runs on a fresh runtime goroutine and
// funnels modalID onto m.modalTimeout so the Run goroutine fires the safe-deny
// under the single-owner invariant (mirrors armRekeyTimer's callback shape). ctx
// is the surfacer's ctx (the daemon ctx in production, since cmd/pyry runs the
// surfacer and Run under the same ctx); its Done arm releases a fired-but-
// undelivered callback on shutdown, so no goroutine outlives Run.
//
// The *time.Timer is deliberately discarded — never stored, never Stopped. The
// registry's one-shot Resolve is the idempotency gate: a timer that fires after
// an answer/cancel already consumed the modal simply runs handleModalTimeout →
// ResolveTimeout → Resolve-miss → no-op. Tracking timers to Stop them on resolve
// would need a map[modalID]*time.Timer mutated by both the surfacer (arm) and Run
// (cancel) — new cross-goroutine state and a new lock — for zero correctness gain
// (see § Concurrency in the spec). An un-fired AfterFunc holds only a heap entry,
// not a parked goroutine, so leaving it un-Stopped leaks nothing.
func (m *V2SessionManager) ArmModalTimeout(ctx context.Context, modalID string) {
	time.AfterFunc(modalDenyTimeout, func() {
		select {
		case m.modalTimeout <- modalID:
		case <-ctx.Done():
		}
	})
}

// handleModalCancel resolves an inbound modal_cancel control frame: it consumes
// the named modal via the ModalResolver seam, then — only if the resolver
// consumed an outstanding modal — fans a modal_dismissed broadcast to every
// interactive-capable conn. Intercepted in dispatchAppFrame before
// dispatch.Route, exactly like handleRequestSnapshot, and runs on the manager's
// single Run dispatch goroutine. There is no reply to the caller — modal control
// is fire-and-broadcast, not request/reply.
//
// A nil ModalResolver (foreground / pre-#708) makes the frame inert. A decode
// failure is tolerated → empty modal_id → the resolver's unknown-id no-op; the
// decode error is never echoed back to the phone. An unknown/already-resolved id
// returns ok=false ⇒ no keystroke, no audit, no broadcast (AC #4).
//
// SECURITY: the untrusted modal_id is decoded into a typed struct and used only
// as a registry key downstream; the payload bytes are never logged or echoed.
func (m *V2SessionManager) handleModalCancel(ctx context.Context, s *V2Session, env protocol.Envelope) {
	if m.cfg.ModalResolver == nil {
		m.cfg.Logger.Debug("relay: v2 modal_cancel inert; no resolver wired",
			"event", "v2.modal.cancel.inert",
			"conn_id", s.connID)
		return
	}
	var payload protocol.ModalCancelPayload
	// A decode failure is tolerated: it leaves ModalID == "", which the
	// resolver rejects as the unknown-id no-op. Never echoed back to the phone.
	_ = json.Unmarshal(env.Payload, &payload)

	d, ok := m.cfg.ModalResolver.ResolveCancel(payload.ModalID, s.device)
	if !ok {
		return // unknown / already-resolved id: no keystroke, no audit, no broadcast (AC #4)
	}
	m.broadcastModalDismissed(ctx, payload.ModalID, d)
}

// handleModalAnswer resolves an inbound modal_answer control frame through the
// same ModalResolver seam as handleModalCancel. In this slice ResolveAnswer is a
// deferred no-op (always ok=false), so the broadcast line is unreachable until
// #717 fills the gated answer arm — but it is present, so #717 needs no manager
// change. The escalating ALLOW path stays inert until the per-device gate exists
// (the fail-safe property of this slice). Runs on the manager's single Run
// dispatch goroutine; see handleModalCancel for the nil-resolver / decode /
// never-echo discipline it shares.
func (m *V2SessionManager) handleModalAnswer(ctx context.Context, s *V2Session, env protocol.Envelope) {
	if m.cfg.ModalResolver == nil {
		m.cfg.Logger.Debug("relay: v2 modal_answer inert; no resolver wired",
			"event", "v2.modal.answer.inert",
			"conn_id", s.connID)
		return
	}
	var payload protocol.ModalAnswerPayload
	_ = json.Unmarshal(env.Payload, &payload)

	d, ok := m.cfg.ModalResolver.ResolveAnswer(payload.ModalID, payload.OptionID, payload.AnswerToken, s.device)
	if !ok {
		return // deferred no-op in this slice (AC #3); #717 fills the gated arm
	}
	m.broadcastModalDismissed(ctx, payload.ModalID, d)
}

// handleModalTimeout fires the fail-closed safe-deny for a modal whose
// deny-on-timeout window elapsed with no answer/cancel (#725). Funneled onto the
// Run goroutine via m.modalTimeout, so it shares the single-owner serialisation
// with handleModalCancel / handleModalAnswer: the answer-vs-timeout race cannot
// double-act — whichever the Run select services first consumes the modal via the
// registry's one-shot Resolve; the loser's ResolveTimeout reports ok=false. A nil
// ModalResolver (foreground / pre-#708) makes it inert (mirrors handleModalCancel's
// nil guard). An already-resolved id ⇒ ok=false ⇒ no keystroke, no audit, no
// broadcast (the AC-2 loser path); only a fresh consume broadcasts.
func (m *V2SessionManager) handleModalTimeout(ctx context.Context, modalID string) {
	if m.cfg.ModalResolver == nil {
		m.cfg.Logger.Debug("relay: v2 modal timeout inert; no resolver wired",
			"event", "v2.modal.timeout.inert")
		return
	}
	d, ok := m.cfg.ModalResolver.ResolveTimeout(modalID)
	if !ok {
		return // already answered/cancelled: no keystroke, no audit, no broadcast (AC-2)
	}
	m.broadcastModalDismissed(ctx, modalID, d)
}

// broadcastModalDismissed fans a modal_dismissed envelope to every
// interactive-capable open session. Runs on the Run goroutine; reads m.sessions
// directly (like handleActiveConns) and Pushes per conn — it MUST NOT call
// ActiveConns, which funnels onto this same goroutine via m.snapshot and would
// deadlock. Push is non-blocking and Run-goroutine-safe (it touches only
// m.queues under pushMu; the seal+forward happens on a later Run iteration via
// drainOnce).
//
// The capability filter (s.interactive) is the same #607 gate modal_shown rides:
// an old, non-interactive phone never receives v2 modal events. The fan-out
// reaches every interactive conn (including ones that never saw this modal's
// modal_shown); the payload carries only the opaque modal_id + outcome/source,
// no modal body, so it discloses nothing — a conn with no matching outstanding
// modal ignores it.
//
// SECURITY: the payload bytes are never logged; a per-conn Push error (ctx
// teardown or ErrConnNotFound from a raced teardown) is debug-logged with the
// transport sentinel only and the fan-out continues.
func (m *V2SessionManager) broadcastModalDismissed(ctx context.Context, modalID string, d ModalDismissal) {
	payload, err := json.Marshal(protocol.ModalDismissedPayload{
		ModalID: modalID,
		Outcome: d.Outcome,
		Source:  d.Source,
	})
	if err != nil {
		// ModalDismissedPayload is a closed struct of three strings; marshal
		// cannot fail in practice. Defensive — NEVER echo err (it could quote
		// the payload). Skip the broadcast rather than crash.
		m.cfg.Logger.Warn("relay: v2 modal_dismissed marshal failed",
			"event", "v2.modal.dismissed.marshal_err",
			"modal_id", modalID)
		return
	}
	// One timestamp shared by every conn for this logical dismissal.
	ts := time.Now().UTC()
	for connID, s := range m.sessions {
		if s.state != V2StateOpen || !s.interactive {
			continue
		}
		env := protocol.Envelope{
			ID:      1, // non-load-bearing; the phone correlates on modal_id.
			Type:    protocol.TypeModalDismissed,
			TS:      ts,
			Payload: payload,
		}
		if err := m.Push(ctx, connID, env); err != nil {
			// ctx teardown or a conn torn down between enumeration and Push:
			// debug-log the transport sentinel and continue the fan-out; the
			// missed conn re-syncs on reconnect. NEVER echo payload bytes.
			m.cfg.Logger.Debug("relay: v2 modal_dismissed push dropped",
				"event", "v2.modal.dismissed.push_err",
				"conn_id", connID,
				"err", err)
		}
	}
}

// reconcileModals unicasts the current outstanding modal_shown set to a freshly
// interactive-open conn (#877). A phone that connects or reconnects after a
// permission prompt was raised never saw the raise-time broadcastInteractive
// fan-out (EventID == nil, so it is not in the turn-event replay ring); without
// this, the prompt silently rides the daemon's 2-minute deny-on-timeout unseen.
//
// Structural sibling of broadcastModalDismissed, minus the fan-out: it addresses
// exactly s.connID rather than every open interactive conn, and sources the
// payloads from the current-truth snapshot instead of a dismissal. Reconciling
// from current state is idempotent — a still-pending modal is re-sent, an
// already-resolved one is simply absent from the snapshot and never resurfaces
// (AC4) — so there is no stale-replay risk and no special-casing of how long the
// client was away.
//
// Run-goroutine only (called from handleNoiseInit's success tail), so
// s.interactive / s.connID are read lock-free under the package's single-owner
// invariant. It reaches no cmd/pyry emitter state: the raise-time nextID is
// producer-goroutine-owned and the modal control ID is non-load-bearing (the
// phone correlates on modal_id), so the re-send is entirely relay-side with a
// fixed ID — no cross-goroutine coupling, no lock added.
//
// SECURITY: the modal body (title/prompt/options) is application content and is
// NEVER logged; the two error branches carry only content-free discriminants
// (event, conn_id, and modal_id — an opaque nonce, not a secret). Matches
// broadcastModalDismissed's no-body-in-logs discipline.
func (m *V2SessionManager) reconcileModals(ctx context.Context, s *V2Session) {
	// Capability gate (AC2) + the unwired/foreground opt-out. s.interactive is
	// the same negotiated flag broadcastModalDismissed gates on; a nil seam is
	// the nil-resolver posture the other optional control seams share.
	if !s.interactive || m.cfg.OutstandingModals == nil {
		return
	}
	outstanding := m.cfg.OutstandingModals()
	if len(outstanding) == 0 {
		return // nothing pending ⇒ nothing sent (AC3).
	}
	// One timestamp shared by the batch (matches broadcastModalDismissed).
	ts := time.Now().UTC()
	for _, p := range outstanding {
		payload, err := json.Marshal(p)
		if err != nil {
			// ModalShownPayload is a closed struct of strings / []struct; marshal
			// cannot fail in practice. Defensive — NEVER echo err or the body (it
			// could quote the payload). Skip this one, keep sending the rest.
			m.cfg.Logger.Warn("relay: v2 modal_shown reconcile marshal failed",
				"event", "v2.modal.reconcile.marshal_err",
				"conn_id", s.connID,
				"modal_id", p.ModalID)
			continue
		}
		env := protocol.Envelope{
			ID:      1, // non-load-bearing; the phone correlates on modal_id.
			Type:    protocol.TypeModalShown,
			TS:      ts,
			Payload: payload,
			// EventID left nil: a control event, never part of the turn-event
			// replay ring (forwardEnvelope's dedup is inert for EventID == nil).
		}
		if err := m.Push(ctx, s.connID, env); err != nil {
			// ctx teardown ⇒ stop (the session is going away); any other sentinel
			// (ErrConnNotFound is unreachable — the queue was created two
			// statements earlier on this same goroutine) ⇒ skip and continue.
			// NEVER echo payload bytes.
			m.cfg.Logger.Debug("relay: v2 modal_shown reconcile push dropped",
				"event", "v2.modal.reconcile.push_err",
				"conn_id", s.connID,
				"err", err)
			if ctx.Err() != nil {
				return
			}
		}
	}
}

// reconcileQueues unicasts the current per-conversation queue_state set to a
// freshly interactive-open conn (#878) — the queue twin of reconcileModals. #722
// pushes queue_state only on change, so a phone that connected/reconnected
// between backlog changes has no way to learn the current backlog; this re-send
// brings it to current queue truth instead of a stale/empty view. queue_state is
// snapshot-shaped full state, so the re-send is idempotent by construction — no
// stale-replay risk and no special-casing of how long the client was away; an
// absent snapshot after the handshake means an empty backlog (the
// reset-on-reconnect client contract).
//
// Run-goroutine only (called from handleNoiseInit's success tail), so
// s.interactive / s.connID are read lock-free under the package's single-owner
// invariant. It reaches no cmd/pyry emitter state: the #722 producer's nextID is
// producer-goroutine-owned and queue_state's envelope ID is non-load-bearing (the
// phone correlates on conversation_id + queued_msg_id), so the re-send is entirely
// relay-side with a fixed ID — no cross-goroutine coupling, no lock added.
//
// SECURITY: the queued text is untrusted, phone-originated content and is NEVER
// logged; the two error branches carry only content-free discriminants (event,
// conn_id, and conversation_id — a non-secret routing id). Matches the #722
// producer's and reconcileModals's no-body-in-logs discipline.
func (m *V2SessionManager) reconcileQueues(ctx context.Context, s *V2Session) {
	// Capability gate (AC4) + the unwired/foreground opt-out. Mirrors
	// reconcileModals: s.interactive is the negotiated flag, a nil seam is the
	// nil-resolver posture the other optional control seams share.
	if !s.interactive || m.cfg.OutstandingQueues == nil {
		return
	}
	outstanding := m.cfg.OutstandingQueues()
	if len(outstanding) == 0 {
		return // nothing pending ⇒ nothing sent (AC3); the seam already omits empty conversations.
	}
	// One timestamp shared by the batch (matches reconcileModals).
	ts := time.Now().UTC()
	for _, p := range outstanding {
		payload, err := json.Marshal(p)
		if err != nil {
			// QueueStatePayload is a closed struct of strings / ints / time; marshal
			// cannot fail in practice. Defensive — NEVER echo err or the payload (it
			// could quote the untrusted text). Skip this one, keep sending the rest.
			m.cfg.Logger.Warn("relay: v2 queue_state reconcile marshal failed",
				"event", "v2.queue.reconcile.marshal_err",
				"conn_id", s.connID,
				"conversation_id", p.ConversationID)
			continue
		}
		env := protocol.Envelope{
			ID:      1, // non-load-bearing; the phone correlates on conversation_id + queued_msg_id.
			Type:    protocol.TypeQueueState,
			TS:      ts,
			Payload: payload,
			// EventID left nil: a control event, never part of the turn-event
			// replay ring (forwardEnvelope's dedup is inert for EventID == nil).
		}
		if err := m.Push(ctx, s.connID, env); err != nil {
			// ctx teardown ⇒ stop (the session is going away); any other sentinel
			// (ErrConnNotFound is unreachable — the queue was created two
			// statements earlier on this same goroutine) ⇒ skip and continue.
			// NEVER echo payload bytes.
			m.cfg.Logger.Debug("relay: v2 queue_state reconcile push dropped",
				"event", "v2.queue.reconcile.push_err",
				"conn_id", s.connID,
				"err", err)
			if ctx.Err() != nil {
				return
			}
		}
	}
}

// handleInterrupt routes an inbound `interrupt` control frame to the supervised
// claude as one Esc — the remote equivalent of pressing Esc at the local
// terminal (#707). The frame carries no payload, so there is nothing to decode;
// there is no reply and no broadcast (fire-and-forget). Intercepted in
// dispatchAppFrame before dispatch.Route, like handleModalCancel, and runs on the
// manager's single Run dispatch goroutine — so the s.interactive read is lock-free
// under the package's single-owner invariant.
//
// The signature takes only s (no ctx, no env): the frame has no payload to decode
// and the handler does no cancellable work — an intentional deviation from the
// (ctx, s, env) sibling handlers.
//
// Order is load-bearing — the capability gate comes first:
//  1. A non-interactive conn's interrupt is inert (no Esc). This is the new
//     inbound capability gate (#707): existing inbound controls gate outbound
//     emission on s.interactive, but interrupt is the first whose authorization
//     IS the interactive capability. A one-line check, NOT a reusable inbound-gate
//     abstraction — interrupt shares this bare-capability shape only with
//     dequeue_message (which also gates on the interactive capability since #723
//     but carries no per-device gate; modal_answer uses the per-device gate,
//     modal_cancel a nonce), and a one-line check shared by two consumers does not
//     warrant a helper abstraction (CODING-STYLE: over-DRY).
//  2. A nil Interrupter (foreground / pre-wire) makes the frame inert, mirroring
//     handleModalCancel's nil-resolver guard.
//  3. SendEsc is best-effort: an error (no live session / mid-teardown) is
//     Warn-logged with the supervisor sentinel + conn_id and tolerated — there is
//     nothing to roll back and no reply is owed. NEVER log payload bytes (there
//     are none) or the rendered screen.
func (m *V2SessionManager) handleInterrupt(s *V2Session) {
	if !s.interactive {
		return // non-interactive conn: inert, no Esc (the AC-2 negative path)
	}
	if m.cfg.Interrupter == nil {
		m.cfg.Logger.Debug("relay: v2 interrupt inert; no interrupter wired",
			"event", "v2.interrupt.inert",
			"conn_id", s.connID)
		return
	}
	if err := m.cfg.Interrupter.SendEsc(); err != nil {
		m.cfg.Logger.Warn("relay: v2 interrupt keystroke failed",
			"event", "v2.interrupt.keystroke_err",
			"conn_id", s.connID,
			"err", err)
	}
}

// handleNewSession routes an inbound `new_session` control frame to the
// supervised claude as a /clear — the remote equivalent of typing `/clear` at
// the local terminal, starting a fresh session (#831). The frame carries no
// payload, so there is nothing to decode; there is no reply and no broadcast
// (fire-and-forget) — the client observes the resulting break via the existing
// session_transition marker (#656/#657). Intercepted in dispatchAppFrame before
// dispatch.Route, like handleInterrupt, and runs on the manager's single Run
// dispatch goroutine — so the s.interactive read is lock-free under the
// package's single-owner invariant.
//
// The signature takes only s (no ctx, no env): the frame has no payload to
// decode and the handler does no cancellable work — the same intentional
// deviation handleInterrupt established.
//
// Order is load-bearing — the capability gate comes first:
//  1. A non-interactive conn's new_session is inert (no /clear). The inbound
//     capability gate is the authorization, matching interrupt / dequeue_message
//     — a one-line check, NOT a reusable inbound-gate abstraction (CODING-STYLE:
//     over-DRY).
//  2. A nil SessionStarter (foreground / pre-wire) makes the frame inert,
//     mirroring handleInterrupt's nil-seam guard.
//  3. StartNewSession is best-effort: an error (no live session / mid-teardown)
//     is Warn-logged with the supervisor sentinel + conn_id and tolerated —
//     there is nothing to roll back and no reply is owed. NEVER log payload bytes
//     (there are none) or the rendered screen.
func (m *V2SessionManager) handleNewSession(s *V2Session) {
	if !s.interactive {
		return // non-interactive conn: inert, no /clear (the AC-4 negative path)
	}
	if m.cfg.SessionStarter == nil {
		m.cfg.Logger.Debug("relay: v2 new_session inert; no session starter wired",
			"event", "v2.new_session.inert",
			"conn_id", s.connID)
		return
	}
	if err := m.cfg.SessionStarter.StartNewSession(); err != nil {
		m.cfg.Logger.Warn("relay: v2 new_session keystroke failed",
			"event", "v2.new_session.keystroke_err",
			"conn_id", s.connID,
			"err", err)
	}
}

// handleDequeueMessage removes a not-yet-drained queued message named by an
// inbound dequeue_message control frame (#723), letting a phone cancel a turn it
// queued before it drains. Intercepted in dispatchAppFrame before dispatch.Route,
// like handleInterrupt / handleModalCancel, and runs on the manager's single Run
// dispatch goroutine — so the s.interactive read is lock-free under the package's
// single-owner invariant.
//
// The signature takes (s, env) — no ctx — mirroring handleInterrupt's deviation
// from the (ctx, s, env) siblings: Remove takes no context, there is no
// forwardEnvelope, and queue_state convergence is decoupled onto the #722 emitter
// goroutine (the OnChange seam Remove fires), so the handler has no cancellable
// work.
//
// Order is load-bearing — the capability gate comes first:
//  1. A non-interactive conn's dequeue_message is inert: no Remove call, no
//     mutation, no panic (AC-3). This is the new inbound capability gate; like
//     handleInterrupt it is the bare interactive check, read on the Run goroutine
//     lock-free.
//  2. A nil QueueRemover (foreground / pre-wire) makes the frame inert, mirroring
//     handleInterrupt's nil-Interrupter guard.
//  3. The payload is decoded tolerantly: a decode failure leaves zero-value
//     fields, which Remove("", 0) no-ops on. The decode error and the payload
//     bytes are NEVER echoed back to the phone or into a log (encoding/json can
//     quote attacker bytes into its error string) — the never-echo discipline of
//     handleRequestSnapshot / handleModalCancel.
//  4. Remove returning false (unknown/already-delivered/in-flight-head id, or an
//     unknown/foreign conversation_id) is success of a valid request, not an error
//     (AC-2): no reply, no broadcast. The convID arg confines the effect to that
//     one FIFO — a hostile id cannot touch another conversation's backlog.
//  5. There is no reply and no broadcast. AC-4's queue_state convergence is the
//     automatic OnChange → #722-producer path that Remove's notify fires on a
//     successful removal; the handler MUST NOT push or re-emit queue_state itself.
func (m *V2SessionManager) handleDequeueMessage(s *V2Session, env protocol.Envelope) {
	if !s.interactive {
		return // non-interactive conn: inert, no Remove (the AC-3 negative path)
	}
	if m.cfg.QueueRemover == nil {
		m.cfg.Logger.Debug("relay: v2 dequeue_message inert; no queue remover wired",
			"event", "v2.dequeue.inert",
			"conn_id", s.connID)
		return
	}
	var p protocol.DequeueMessagePayload
	// A decode failure is tolerated: it leaves zero-value fields, which
	// Remove("", 0) no-ops on. Never echoed back to the phone or into a log.
	_ = json.Unmarshal(env.Payload, &p)

	if m.cfg.QueueRemover.Remove(p.ConversationID, p.QueuedMsgID) {
		m.cfg.Logger.Info("relay: v2 dequeue_message removed",
			"event", "v2.dequeue.removed",
			"conn_id", s.connID,
			"conversation_id", p.ConversationID,
			"queued_msg_id", p.QueuedMsgID)
		return
	}
	// false ⇒ success of a valid request (AC-2): unknown / already-delivered /
	// in-flight-head id, or an unknown/foreign conversation_id. Nothing changed,
	// so emitting nothing is correct — no reply, no broadcast.
	m.cfg.Logger.Debug("relay: v2 dequeue_message no-op",
		"event", "v2.dequeue.noop",
		"conn_id", s.connID,
		"conversation_id", p.ConversationID,
		"queued_msg_id", p.QueuedMsgID)
}
