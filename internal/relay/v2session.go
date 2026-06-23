package relay

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/eventring"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// Mobile Protocol v2 close codes (docs/protocol-mobile.md § Error codes).
// 4401 (StatusUnauthorized) lives in auth.go and is reused unchanged.
const (
	// StatusProtocolMismatch is the WS close code the binary asks the
	// relay to apply when a phone sends an inner frame that violates the
	// v2 inner-frame shape, the state machine, or the discriminator
	// table. Wire spec: docs/protocol-mobile.md § Error codes, close-code
	// row 4421.
	StatusProtocolMismatch websocket.StatusCode = 4421

	// StatusHandshakeFailure is the WS close code the binary asks the
	// relay to apply when the Noise_IK handshake fails before
	// CipherStates exist (e.g. MAC failure on IK message 1, wrong static
	// pubkey). No AEAD-sealed error envelope can be sent — the close
	// code is the only signal. Wire spec: docs/protocol-mobile.md
	// § Error codes, close-code row 4426.
	StatusHandshakeFailure websocket.StatusCode = 4426
)

// maxNoisePayloadBytes is the Noise framework's per-message limit on the
// decoded `data` field of an InnerFrameV2 (docs/protocol-mobile.md
// § Wire shapes). Enforced at the JSON-decode boundary so oversized
// payloads never reach Responder.ReadInit.
const maxNoisePayloadBytes = 65535

// rekeyInterval is the scheduled re-key cadence on each open v2 session
// (docs/protocol-mobile.md § Re-key — the 1-hour rule). Exposed as a
// package var (lowercase) so tests can substitute a sub-second value
// via a t.Cleanup save-and-restore idiom; not part of the public API.
var rekeyInterval = 1 * time.Hour

// rekeyReplyTimeout is the bounded window between emitting a
// rekey_request and observing the phone's fresh noise_init. On expiry
// the conn is closed at StatusHandshakeFailure (4426) and a
// noise.rekey_failed log line is emitted. Exposed as a package var
// (lowercase) so tests can substitute a sub-second value; not part of
// the public API.
var rekeyReplyTimeout = 30 * time.Second

// modalDenyTimeout is the bounded window between a surfaced modal and the
// fail-closed safe-deny: if no modal_answer / modal_cancel resolves it first,
// the daemon denies it (ESC) so a permission claude is waiting on can never
// linger or be silently granted. ADR 025 § Security model specifies "a bounded
// window" but no number; 2 minutes balances "long enough for a human to react to
// a push notification and tap" against "short enough not to leave claude
// blocked." Test-overridable (lowercase, save/restore in tests); not part of the
// public API and not yet config-driven (a deferred #708 concern).
var modalDenyTimeout = 2 * time.Minute

// ErrConnNotFound is returned by (*V2SessionManager).Rekey when connID
// is not currently registered in the manager's sessions map. Wraps
// control.ErrConnNotFound so the control dispatcher's
// errors.Is(err, control.ErrConnNotFound) check in handleRekey
// continues to map to ErrCodeConnNotFound on the wire without further
// plumbing.
var ErrConnNotFound = fmt.Errorf("relay: conn not found: %w", control.ErrConnNotFound)

// ErrSessionNotOpen is returned by (*V2SessionManager).Rekey when the
// named session exists but is not eligible for a manual rekey — either
// not in V2StateOpen (still handshaking, or already torn down), or
// already awaiting a rekey reply from a prior emit. The control
// dispatcher surfaces this verbatim through Response.Error with no
// ErrorCode (slice A defines no wire code for this state yet).
var ErrSessionNotOpen = errors.New("relay: session not open")

// wakeKind enumerates the per-session timer events the manager's Run
// goroutine handles on its wake channel. The values are internal and
// MUST NOT be exposed across the package boundary.
type wakeKind int

const (
	wakeRekeyEmit wakeKind = iota
	wakeRekeyReplyTimeout
)

// wakeSignal is the value the per-session timer-callback goroutines
// (spawned by time.AfterFunc) push onto V2SessionManager.wake. The Run
// goroutine pops these signals and performs the actual work
// (emitRekeyRequest or closeWith) under the single-owner-goroutine
// invariant for s.send / s.recv.
type wakeSignal struct {
	s    *V2Session
	kind wakeKind
}

// manualRekeyReq is enqueued by (*V2SessionManager).Rekey and dequeued
// by Run on the manual-rekey channel arm. The reply channel is
// per-request (cap=1) so the manager's reply send is non-blocking even
// if the caller's ctx fires between enqueue and reply.
type manualRekeyReq struct {
	connID string
	reply  chan error
}

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
//     unsatisfiable here; we yield "strictly bounded". This state needs a
//     connected-but-very-slow relay sustained across hundreds of control
//     events with zero interleaved text, and the phone cannot drive control
//     volume (push is server→phone only), so it is unreachable in practice.
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
	// Soft overflow: admit the control event past nominal cap.
	q.items = append(q.items, qe)
	return false
}

// snapshotReq is enqueued by (*V2SessionManager).ActiveConns and dequeued
// by Run on the snapshot channel arm. reply is per-request (cap=1) so Run's
// reply send is non-blocking even if the caller's ctx fires between enqueue
// and reply. Mirrors manualRekeyReq minus the per-conn inputs — a snapshot
// takes no addressed-conn argument. The reply carries the capability-aware
// enumeration ([]ActiveConn); ActiveConnIDs is a thin projection over the
// same reply.
type snapshotReq struct {
	reply chan []ActiveConn
}

// wakeBufferSize sizes the manager's wake channel. The 1-hour rekey
// cadence makes concurrent fires across sessions vanishingly rare; 16
// is a generous safety margin that absorbs the realistic worst case
// (every session times out simultaneously while Run is busy in a slow
// handler invocation) without forcing the timer-callback goroutine to
// block. cap=1 would also be correct.
const wakeBufferSize = 16

// pushQueueCap bounds the per-session push buffer (count of envelopes, not
// bytes). Starting value pending the ADR-025 load test (decisions/025 line
// 220): post-#609 coalescing makes deltas arrive per-message/~250 ms, so 256
// gives ample headroom to ride out one transport WriteTimeout window without
// dropping while bounding worst-case per-session memory. Control events may
// briefly push the queue past this in the unreachable-in-practice all-control
// saturated case (see pushQueue.enqueue).
const pushQueueCap = 256

// handlerOutboundBuf is the buffer size for the per-frame dispatch.Conn
// outbound channel allocated by dispatchAppFrame. The three production
// handlers (send_message, list_conversations, register_push_token) emit
// exactly one reply per invocation; Route emits at most one error reply.
// 8 is a generous safety margin and is documented as the
// synchronous-handler assumption in V2SessionConfig.Handlers.
const handlerOutboundBuf = 8

// V2SessionState is the externally-observable lifecycle state of a
// per-conn V2Session. The handshakeComplete substate is distinct from
// open even though both can be set inside the same noise_init handler:
// the field exists so the gating test pins the
// "handler chain unreachable from handshakeComplete" invariant
// deterministically (AC #1 / #4).
type V2SessionState int

const (
	V2StateAwaitingInit V2SessionState = iota
	V2StateHandshakeComplete
	V2StateOpen
	V2StateClosed
)

// V2Session is the per-conn_id state held by V2SessionManager. Mutation
// is serialised by the manager's single dispatch goroutine (the loop is
// the lock); there is no mutex because flynn/noise's CipherStates are
// not safe for concurrent use and the manager guarantees a single
// writer per conn_id. The re-key responder (handleRekeyInit) atomically
// swaps s.send / s.recv in a single tuple assignment on this same
// goroutine; old *CipherState pointers are dropped from the struct and
// reclaimed by GC. No explicit Wipe() of the key bytes is exposed — the
// single-owner-goroutine invariant means no code path reads the old
// state after the swap, which is the practical zeroisation property.
type V2Session struct {
	connID string
	state  V2SessionState
	resp   *noise.Responder
	send   *noise.CipherState
	recv   *noise.CipherState

	// device is the matched device snapshot from the handshake's
	// token-accept branch. Surfaced into the per-frame *dispatch.Conn as
	// auth so handlers can call c.Auth(). Set exactly once in
	// handleNoiseInit's token-OK path before state advances to
	// V2StateOpen. Nil before the token check completes.
	device *devices.Device

	// interactive is the negotiated interactive-capability decision (the
	// phone's advertised set ∩ supportedV2Capabilities contained
	// CapabilityInteractive). Set exactly once in handleNoiseInit's token-OK
	// path BEFORE s.state advances to V2StateOpen; the zero value (false) is
	// the fail-closed default for every other path. Re-key (handleRekeyInit)
	// preserves it by never touching it, like device/peerStatic. Read by
	// handleActiveConns on the same dispatch goroutine — no lock/atomic.
	interactive bool

	// peerStatic is the initiator's 32-byte X25519 static public key
	// captured at the initial handshake (immediately after
	// Responder.ReadInit returns nil). The field is set exactly once
	// per V2Session and pins the original peer's identity for the
	// session's entire lifetime — a successful re-key (added in #453)
	// MUST NOT overwrite this value; the re-key responder reads
	// s.peerStatic and rejects mismatches at WS close code 4426.
	//
	// SECURITY: this is a public key (not a secret), but it is
	// identity-bearing. It MUST NOT appear in any logged field; the
	// package's no-key-in-logs discipline extends to per-session
	// identity pins. Not persisted to disk; lifetime is the V2Session.
	peerStatic []byte

	// rekeyTimer fires rekeyInterval after the session entered
	// V2StateOpen (initial handshake) or after the last successful
	// responder-side CipherState swap (rekeyComplete). On fire the
	// timer's AfterFunc callback delivers a wakeRekeyEmit signal onto
	// m.wake; the manager's Run goroutine then calls emitRekeyRequest
	// under the single-owner-goroutine invariant for s.send. Stopped
	// (and replaced with nil) by closeWith on any close path. Re-armed
	// by rekeyComplete. Nil before initial open; nil after closeWith.
	rekeyTimer *time.Timer

	// rekeyReplyTimer fires rekeyReplyTimeout after emitRekeyRequest
	// sent a rekey_request. On fire the AfterFunc callback delivers a
	// wakeRekeyReplyTimeout signal onto m.wake; the manager's Run
	// goroutine then closes the conn via closeWith(StatusHandshakeFailure
	// /* 4426 */, nil) and emits the noise.rekey_failed log line.
	// rekeyComplete stops this timer (and clears awaitingRekeyReply)
	// when the phone's fresh noise_init lands in handleRekeyInit
	// before the timeout elapses. Nil unless awaitingRekeyReply is
	// true.
	rekeyReplyTimer *time.Timer

	// awaitingRekeyReply is true between an emitRekeyRequest emit and
	// either rekeyComplete (success) or the wakeRekeyReplyTimeout
	// branch (failure). The bool is the canonical "are we awaiting a
	// fresh noise_init" predicate; rekeyReplyTimer non-nil-ness is the
	// concrete machinery but is not consulted as state — Stop() on a
	// fired timer can race with the wake delivery, and the bool is the
	// stable signal that rekeyComplete already won.
	awaitingRekeyReply bool

	// replayThrough is the highest durable event id already delivered to this
	// conn by the mid-turn-reconnect replay (#647). Run-owned: written in
	// replayMissed and read in forwardEnvelope, both on the single Run
	// goroutine — same single-writer regime as state/interactive, so no lock
	// or atomic. forwardEnvelope drops a live structured envelope whose EventID
	// <= replayThrough, so the transient replay/live overlap never
	// double-delivers an event (the deterministic dedup proven in spec
	// § Concurrency model). Zero for any conn that never advertised
	// last_event_id, and live ids are always >= 1, so the guard is inert for
	// them.
	replayThrough uint64
}

// State returns the externally-observable state. Called from the same
// goroutine that mutates today; a cross-goroutine reader (e.g. a
// broadcast layer added in a later slice) would need atomic.Int32 or a
// small mutex — tracked in spec § Open questions.
func (s *V2Session) State() V2SessionState { return s.state }

// rekeyComplete is the seam that bridges responder-side swap completion
// back to initiator-side cadence. Called from the success tail of
// (*V2SessionManager).handleRekeyInit after the atomic s.send / s.recv
// swap and the v2.rekey.accept log emission.
//
// Behaviour:
//   - Clears awaitingRekeyReply (no-op if not set — a spontaneous
//     phone-initiated re-key that the binary did not request still
//     re-bases the 1-hour cadence; any successful swap is a fresh-key
//     moment).
//   - Stops and nils rekeyReplyTimer (no-op if nil).
//   - Stops and replaces rekeyTimer with a fresh one armed
//     rekeyInterval from now.
//
// Runs on the manager's single dispatch goroutine (the same goroutine
// that owns s.send / s.recv after the swap). Inherits the no-mutex /
// no-atomic invariant from the rest of the package.
//
// The (m, ctx) argument shape carries the dependencies needed to
// re-arm the 1-hour timer — m for the wake channel, ctx (Run-derived
// runCtx) for the AfterFunc callback's escape arm.
func (s *V2Session) rekeyComplete(m *V2SessionManager, ctx context.Context) {
	s.awaitingRekeyReply = false
	if s.rekeyReplyTimer != nil {
		s.rekeyReplyTimer.Stop()
		s.rekeyReplyTimer = nil
	}
	if s.rekeyTimer != nil {
		s.rekeyTimer.Stop()
	}
	s.rekeyTimer = m.armRekeyTimer(ctx, s)
}

// ScreenSnapshotter renders the daemon's live claude screen to plain text:
// text is the rendered screen, live is false (and text "") when no claude
// child is attached. *supervisor.Supervisor satisfies it. Declared here, in
// the consumer, so internal/relay depends on neither internal/supervisor nor
// tui-driver (CODING-STYLE: define interfaces where they are consumed).
type ScreenSnapshotter interface {
	ScreenSnapshot() (text string, live bool)
}

// Interrupter delivers a single Esc to the supervised claude — the remote
// equivalent of a local Esc, claude's own interrupt. *supervisor.Supervisor
// satisfies it via SendEsc (#726), so the supervisor needs no new method.
// Declared here (consumer side), beside ScreenSnapshotter, so internal/relay
// imports neither internal/supervisor nor tui-driver. Named for its relay-domain
// role (matching ScreenSnapshotter.ScreenSnapshot / ModalResolver.Resolve*),
// even though the method keeps the sealed surface's name. SendEsc is safe to call
// from any goroutine — it is the same seam ResolveCancel / ResolveTimeout use.
type Interrupter interface{ SendEsc() error }

// QueueRemover drops a not-yet-drained queued message from a conversation's
// inbound backlog by id (#723). *msgqueue.Queue satisfies it. Declared here, in
// the consumer, beside Interrupter / ModalResolver, so internal/relay imports
// neither internal/msgqueue nor cmd/pyry (CODING-STYLE: define interfaces where
// they are consumed). Returns true iff a message was removed; an unknown or
// foreign conversationID, an unknown or already-delivered id, or the in-flight
// (draining) head is a safe no-op (false). The conversationID arg IS the
// mutation scope: Remove(A,…) provably never touches conversation B's backlog.
type QueueRemover interface {
	Remove(conversationID string, queuedMsgID uint64) bool
}

// ModalDismissal is the wire outcome+source the manager broadcasts after a
// resolver consumes an outstanding modal. The manager already holds modal_id
// (from the inbound control payload), so it is not repeated here.
type ModalDismissal struct {
	Outcome string // e.g. "cancelled" (cancel); #717 uses the answered option_id
	Source  string // closed set {remote, local, timeout}; cancel ⇒ "remote"
}

// ModalResolver resolves an inbound modal control frame against the daemon's
// outstanding-modal state. Declared here (consumer side), beside
// ScreenSnapshotter, so internal/relay imports neither internal/supervisor nor
// cmd/pyry; the cmd/pyry resolver satisfies it. *devices.Device crosses the
// seam (the per-conn s.device); internal/relay already imports internal/devices,
// so no new import. Both methods run on the manager's single Run dispatch
// goroutine.
type ModalResolver interface {
	// ResolveCancel consumes modalID (registry Resolve), routes a cancel/ESC
	// keystroke, audits outcome=cancelled, and returns the dismissal to
	// broadcast with ok=true. An unknown/already-resolved id ⇒ (zero, false):
	// no keystroke, no audit, no dismissal.
	ResolveCancel(modalID string, dev *devices.Device) (ModalDismissal, bool)

	// ResolveAnswer resolves an inbound modal_answer. In this slice it is a
	// deferred no-op — always (zero, false): no keystroke, no mutation, no
	// audit. #717 fills the gated answer arm; the manager code is already
	// general (broadcasts on ok=true) so #717 changes only the impl.
	ResolveAnswer(modalID, optionID, answerToken string, dev *devices.Device) (ModalDismissal, bool)

	// ResolveTimeout safe-denies an unanswered modal whose deny-on-timeout
	// window elapsed (#725): it consumes modalID (registry Resolve), routes the
	// fail-closed deny keystroke (ESC), audits outcome=denied_timeout /
	// source=timeout with an empty device (a timeout has no answering device),
	// and returns the dismissal to broadcast with ok=true. An unknown or
	// already-resolved id (an answer/cancel won the race) ⇒ (zero, false): no
	// keystroke, no audit, no broadcast — the AC-2 loser path. Takes no device
	// (unlike ResolveCancel/ResolveAnswer): the safe-deny is unconditional, so
	// there is nothing to gate.
	ResolveTimeout(modalID string) (ModalDismissal, bool)
}

// V2SessionConfig parameterises V2SessionManager. The handshake/transport
// fields are required; NewV2SessionManager validates and panics or errors on
// missing required values per the documentation below. Handlers, Snapshotter,
// and KnownConversation are optional — their per-field docs describe the
// nil behaviour.
//
// SECURITY: StaticPriv is the binary's 32-byte X25519 static private
// key. It MUST NOT be logged, wrapped into an error message, or emitted
// on any wire surface. internal/keys and internal/noise document the
// same contract for the same bytes; this struct extends the contract
// to the manager's holding site.
type V2SessionConfig struct {
	// Frames is the inbound RoutingEnvelope stream from a relay.Connection
	// (or an in-memory channel in tests). Run consumes until Frames
	// closes or ctx is done.
	Frames <-chan protocol.RoutingEnvelope

	// Outbound forwards a single binary→relay RoutingEnvelope. Production
	// wiring passes (*relay.Connection).Send. Non-nil errors are logged
	// at debug and dropped — the relay leg's reconnect handles recovery
	// (mirrors v1's cmd/pyry/relay.go forwarder posture).
	Outbound func(protocol.RoutingEnvelope) error

	// StaticPriv is the binary's 32-byte X25519 static private key.
	StaticPriv []byte

	// Devices is the token-validation predicate for hello.Token.
	Devices *devices.Registry

	// ServerID is surfaced into the hello_ack early-data payload.
	ServerID string

	// Logger receives lifecycle and reject events. Token, key bytes,
	// payload bytes, AEAD ciphertext, and base64 forms thereof MUST NOT
	// appear in any logged field.
	Logger *slog.Logger

	// Handlers is the application-layer envelope-type → handler table
	// used for v2 open-state dispatch. Optional: nil or empty map means
	// no app handlers are registered, and every open-state envelope falls
	// through to a sealed protocol.unsupported reply via dispatch.Route.
	// Mirror v1's internal/dispatch.Dispatcher.Register registration
	// shape — production wires Handlers via the daemon, same handlers as
	// v1.
	//
	// SECURITY: handlers run on the manager's single dispatch goroutine
	// (same goroutine that mutates s.send / s.recv). Handlers MUST be
	// synchronous and MUST NOT spawn long-lived background goroutines
	// that retain a reference to the *dispatch.Conn passed in — the
	// conn's outbound channel is per-frame and is drained before
	// dispatchAppFrame returns; sends from a forked goroutine after that
	// drain are silently lost (the channel is leaked but bounded by its
	// capacity, and reclaimed by GC).
	Handlers map[string]dispatch.Handler

	// Snapshotter renders the live claude screen for an inbound
	// request_snapshot (ADR 025 § Safe degradation). Optional: when nil,
	// request_snapshot yields a server.binary_offline error reply — the
	// snapshot feature is simply unavailable, not a crash.
	Snapshotter ScreenSnapshotter

	// KnownConversation reports whether conversationID names a conversation
	// this daemon hosts. request_snapshot rejects an unknown/foreign id with
	// conversation.not_found before any render (AC #4). Optional: when nil,
	// every request_snapshot is rejected as not-found. Production wires it to
	// a conversations.Registry membership check.
	KnownConversation func(conversationID string) bool

	// ModalResolver resolves inbound modal_answer / modal_cancel control
	// frames. Optional: when nil, both are inert no-ops (the modal bridge is
	// simply unwired — foreground, or pre-#708 before the producer is live).
	// Production wires the cmd/pyry resolver.
	ModalResolver ModalResolver

	// Interrupter routes an inbound interactive `interrupt` control frame to
	// the supervised claude as one Esc (#707). Optional: nil ⇒ interrupt is
	// inert (no Esc) — the foreground / unwired case. Production wires
	// *supervisor.Supervisor.
	Interrupter Interrupter

	// QueueRemover drops a queued message named by an inbound dequeue_message
	// control frame (#723). Optional: nil ⇒ dequeue_message is inert (foreground
	// / unwired). Production wires *msgqueue.Queue.
	QueueRemover QueueRemover
}

// V2SessionManager owns the per-conn_id v2 state machine. Construct with
// NewV2SessionManager; drive with Run. The manager is single-shot — Run
// returns when Frames closes or ctx is done, and the manager must not
// be reused.
//
// Concurrency: Run is the only goroutine the manager owns. sessions is
// mutated exclusively by Run; no lock. This is intentionally simpler
// than internal/dispatch.Dispatcher (which spins one goroutine per
// conn_id); v2 in this slice runs no application handlers, every frame
// processes synchronously (~100µs Noise call or sub-µs JSON decode), so
// single-goroutine fan-in is correct and obviously safe. The re-key
// responder's CipherState swap (s.send, s.recv = newSend, newRecv)
// inherits this property: a single tuple assignment on this goroutine
// cannot be observed half-applied by any other code path, so the spec's
// atomic-switchover requirement is structural.
type V2SessionManager struct {
	cfg      V2SessionConfig
	sessions map[string]*V2Session

	// wake is the wake-up signal channel for per-session timers. Both
	// the 1-hour rekey-emit timer and the 30s rekey-reply-timeout
	// timer use time.AfterFunc callbacks (which run on fresh runtime
	// goroutines, NOT on the dispatch goroutine that owns
	// s.send / s.recv); the callbacks send a wakeSignal onto this
	// channel so the manager's Run goroutine can do the actual work
	// (emitRekeyRequest or closeWith) under the single-owner-
	// goroutine invariant. Buffered (wakeBufferSize) so timer
	// callbacks don't block on a busy Run goroutine; callbacks also
	// honour the Run-derived ctx so they unblock cleanly on Run exit
	// and leak no goroutines.
	wake chan wakeSignal

	// manualRekey funnels (*V2SessionManager).Rekey calls onto Run's
	// dispatch goroutine so the lookup + emit sequence runs under the
	// single-owner-goroutine invariant for s.send / s.state /
	// s.rekeyTimer. Unbuffered: backpressure is correct — if Run is
	// busy, Rekey waits; the caller's ctx is the escape arm in Rekey.
	// Not closed by the manager on Run exit; in-flight callers unblock
	// via ctx.Done.
	manualRekey chan manualRekeyReq

	// pushMu is a leaf lock guarding queues (the map) and every pushQueue's
	// contents. Held only around map lookup + enqueue/pop (both O(cap), cap
	// small); NEVER held across an Encrypt, m.send, or any channel op, and
	// never nested with any other lock. It orders below nothing — it is always
	// taken alone. This is what lets an off-Run Push reach the addressed
	// session's queue WITHOUT reading Run-owned m.sessions or touching s.send.
	pushMu sync.Mutex

	// queues maps connID → the per-session bounded push buffer. The KEY SET is
	// Run-managed: a queue is created when handleNoiseInit advances a session
	// to V2StateOpen and deleted by closeWith, both on the Run goroutine. Push
	// only mutates a queue's CONTENTS (under pushMu); it never adds or removes
	// keys. So queue-exists ⟺ session is V2StateOpen for any Run-side observer.
	queues map[string]*pushQueue

	// drainCh signals "some queue has work" to Run's drain arm. Capacity 1 with
	// non-blocking sends (from Push and from the drain's re-signal) collapses
	// concurrent wakes into at most one pending; a Push that enqueues while Run
	// is mid-pass lands its signal into the now-drained channel and triggers the
	// next pass — a self-perpetuating pump with no lost wakeups.
	drainCh chan struct{}

	// snapshot funnels (*V2SessionManager).ActiveConns (and ActiveConnIDs,
	// which projects over it) calls onto Run's dispatch goroutine so the read
	// of m.sessions runs under the single-owner-goroutine invariant, serialised
	// against every map write (lazy-create, delete, state transitions).
	// Unbuffered: backpressure is correct — if Run is busy, the caller waits;
	// the caller's ctx is the escape arm. Not closed by the manager on Run
	// exit; in-flight callers unblock via ctx.Done.
	snapshot chan snapshotReq

	// modalTimeout carries a surfaced modal's id from its time.AfterFunc
	// callback goroutine (armed off-Run by ArmModalTimeout) to the Run goroutine
	// for the deny-on-timeout safe-deny (#725). Daemon-global (a modal is not
	// bound to one conn), keyed by modal_id — unlike wake, which is keyed by
	// *V2Session. Buffered (wakeBufferSize) so a timer callback almost never
	// blocks; callbacks also honour the Run-derived ctx so they unblock cleanly
	// on Run exit and leak no goroutine.
	modalTimeout chan string

	// replayRing + replayCursor are the mid-turn-reconnect replay source
	// (#647), published once after the interactive emitter (which owns the
	// ring) is built — see SetReplaySource for why this is a late-bound setter
	// and not a V2SessionConfig field. Guarded by the pushMu leaf lock: the
	// publish writes them off the Run goroutine during wiring, and replayMissed
	// reads them on the Run goroutine at reconnect (much later, after a full
	// network handshake). nil ring or cursor ⇒ replay disabled (no setter
	// called, or the structured stream is off) — a phone advertising
	// last_event_id then simply gets the live stream, no replay, no resync.
	replayRing   *eventring.Ring
	replayCursor func() string
}

// NewV2SessionManager validates cfg and returns a ready manager. Panics
// on missing Frames or Logger (matching internal/dispatch.New style for
// programmer errors). Returns an error on missing Outbound, missing
// Devices, missing ServerID, or wrong-length StaticPriv.
func NewV2SessionManager(cfg V2SessionConfig) (*V2SessionManager, error) {
	if cfg.Frames == nil {
		panic("relay: V2SessionManager Frames is required")
	}
	if cfg.Logger == nil {
		panic("relay: V2SessionManager Logger is required")
	}
	if cfg.Outbound == nil {
		return nil, fmt.Errorf("relay: V2SessionManager Outbound is required")
	}
	if cfg.Devices == nil {
		return nil, fmt.Errorf("relay: V2SessionManager Devices is required")
	}
	if cfg.ServerID == "" {
		return nil, fmt.Errorf("relay: V2SessionManager ServerID is required")
	}
	if len(cfg.StaticPriv) != noise.KeyLen {
		return nil, fmt.Errorf("relay: V2SessionManager StaticPriv must be %d bytes, got %d",
			noise.KeyLen, len(cfg.StaticPriv))
	}
	return &V2SessionManager{
		cfg:          cfg,
		sessions:     make(map[string]*V2Session),
		wake:         make(chan wakeSignal, wakeBufferSize),
		manualRekey:  make(chan manualRekeyReq),
		queues:       make(map[string]*pushQueue),
		drainCh:      make(chan struct{}, 1),
		snapshot:     make(chan snapshotReq),
		modalTimeout: make(chan string, wakeBufferSize),
	}, nil
}

// Run drives the state machine until Frames closes or ctx is cancelled.
// Returns ctx.Err() on cancellation, nil on Frames close. Every per-conn
// session is released on return; no goroutines outlive Run.
//
// runCtx is a derived-cancel context so per-session timer-callback
// goroutines (spawned by armRekeyTimer / armRekeyReplyTimer via
// time.AfterFunc) unblock cleanly when Run exits. The callbacks select
// on (m.wake, runCtx.Done) so a fired-but-undelivered wake completes
// via the ctx branch on shutdown and leaves no goroutine behind.
func (m *V2SessionManager) Run(ctx context.Context) error {
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	for {
		select {
		case <-runCtx.Done():
			return ctx.Err()
		case env, ok := <-m.cfg.Frames:
			if !ok {
				return nil
			}
			m.handleFrame(runCtx, env)
		case w := <-m.wake:
			m.handleWake(runCtx, w)
		case modalID := <-m.modalTimeout:
			m.handleModalTimeout(runCtx, modalID)
		case req := <-m.manualRekey:
			req.reply <- m.handleManualRekey(runCtx, req.connID)
		case <-m.drainCh:
			m.drainOnce(runCtx)
		case req := <-m.snapshot:
			req.reply <- m.handleActiveConns()
		}
	}
}

// handleWake routes a per-session timer wake to its handler under the
// single-owner-goroutine invariant. A wake arriving on a session that
// has transitioned out of V2StateOpen (e.g. closeWith ran between the
// timer fire and the wake delivery) is dropped silently. The state-
// check is read-only on s.state from the dispatch goroutine — no race
// because the same goroutine that would have mutated s.state is the
// goroutine doing this read.
func (m *V2SessionManager) handleWake(ctx context.Context, w wakeSignal) {
	if w.s.state != V2StateOpen {
		return
	}
	switch w.kind {
	case wakeRekeyEmit:
		m.emitRekeyRequest(ctx, w.s, "scheduled")
	case wakeRekeyReplyTimeout:
		if !w.s.awaitingRekeyReply {
			// rekeyComplete already cleared the awaiting state — the
			// phone's fresh noise_init landed before the timeout fired
			// and the swap succeeded. Ignore stale wake.
			return
		}
		m.cfg.Logger.Warn("relay: v2 rekey reply timeout",
			"event", "noise.rekey_failed",
			"conn_id", w.s.connID,
			"close_code", int(StatusHandshakeFailure))
		m.closeWith(ctx, w.s, StatusHandshakeFailure, nil)
	}
}

// armRekeyTimer arms the 1-hour scheduled re-key timer. The callback
// runs on a fresh runtime goroutine (time.AfterFunc semantics); it
// pushes a wakeRekeyEmit signal onto m.wake under blocking-send +
// ctx.Done semantics. ctx is the manager's Run-derived runCtx;
// cancelled on Run exit, which unblocks any pending callback goroutine.
func (m *V2SessionManager) armRekeyTimer(ctx context.Context, s *V2Session) *time.Timer {
	return time.AfterFunc(rekeyInterval, func() {
		select {
		case m.wake <- wakeSignal{s: s, kind: wakeRekeyEmit}:
		case <-ctx.Done():
		}
	})
}

// armRekeyReplyTimer arms the 30s reply-window timer. Same shape as
// armRekeyTimer but with the wakeRekeyReplyTimeout kind and the
// shorter cadence.
func (m *V2SessionManager) armRekeyReplyTimer(ctx context.Context, s *V2Session) *time.Timer {
	return time.AfterFunc(rekeyReplyTimeout, func() {
		select {
		case m.wake <- wakeSignal{s: s, kind: wakeRekeyReplyTimeout}:
		case <-ctx.Done():
		}
	})
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

// handleFrame dispatches one inbound routing envelope to its
// per-conn_id session, creating the session lazily on first frame.
func (m *V2SessionManager) handleFrame(ctx context.Context, env protocol.RoutingEnvelope) {
	if env.ConnID == "" {
		// Binary-direct frames (e.g. hello_ack from the relay during the
		// binary↔relay handshake) are owned by relay.Connection, not by
		// the v2 manager. Connection.handshake consumes hello_ack before
		// frames flow through Frames(); any binary-direct frame that
		// reaches us is unexpected and silently dropped.
		return
	}
	s, ok := m.sessions[env.ConnID]
	if !ok {
		s = &V2Session{connID: env.ConnID, state: V2StateAwaitingInit}
		m.sessions[env.ConnID] = s
	}
	if s.state == V2StateClosed {
		// Late frame on a torn-down conn; drop silently.
		return
	}

	inner, err := decodeInnerFrameV2(env.Frame)
	if err != nil {
		m.cfg.Logger.Warn("relay: v2 state reject",
			"event", "v2.state.reject",
			"conn_id", env.ConnID,
			"close_code", int(StatusProtocolMismatch),
			"reason", "malformed_inner_frame")
		m.closeWith(ctx, s, StatusProtocolMismatch, nil)
		return
	}

	switch inner.Type {
	case protocol.TypeNoiseInit:
		m.handleNoiseInit(ctx, s, inner)
	case protocol.TypeNoiseMsg:
		m.handleNoiseMsg(ctx, s, inner)
	case protocol.TypeNoiseResp:
		// noise_resp from a phone is never valid — only the binary writes
		// noise_resp. Treat as state-machine violation.
		m.cfg.Logger.Warn("relay: v2 state reject",
			"event", "v2.state.reject",
			"conn_id", env.ConnID,
			"close_code", int(StatusProtocolMismatch),
			"reason", "noise_resp_from_phone")
		m.closeWith(ctx, s, StatusProtocolMismatch, nil)
	default:
		m.cfg.Logger.Warn("relay: v2 state reject",
			"event", "v2.state.reject",
			"conn_id", env.ConnID,
			"close_code", int(StatusProtocolMismatch),
			"reason", "unknown_inner_type")
		m.closeWith(ctx, s, StatusProtocolMismatch, nil)
	}
}

// decodeInnerFrameV2 JSON-decodes raw as an InnerFrameV2 and validates
// the discriminator shape. Returns ErrMalformedInnerFrame on any
// structural failure (bad JSON, wrong version, missing type, oversized
// data after base64 decode).
func decodeInnerFrameV2(raw json.RawMessage) (InnerFrameV2Decoded, error) {
	var inner protocol.InnerFrameV2
	if err := json.Unmarshal(raw, &inner); err != nil {
		return InnerFrameV2Decoded{}, fmt.Errorf("decode inner frame: %w", err)
	}
	if inner.Version != protocol.V2Version {
		return InnerFrameV2Decoded{}, fmt.Errorf("inner frame: version %d, want %d",
			inner.Version, protocol.V2Version)
	}
	switch inner.Type {
	case protocol.TypeNoiseInit, protocol.TypeNoiseResp, protocol.TypeNoiseMsg:
	default:
		return InnerFrameV2Decoded{}, fmt.Errorf("inner frame: unknown type %q", inner.Type)
	}
	data, err := base64.StdEncoding.DecodeString(inner.Data)
	if err != nil {
		return InnerFrameV2Decoded{}, fmt.Errorf("inner frame data base64: %w", err)
	}
	if len(data) > maxNoisePayloadBytes {
		return InnerFrameV2Decoded{}, fmt.Errorf("inner frame data: %d bytes > %d cap",
			len(data), maxNoisePayloadBytes)
	}
	return InnerFrameV2Decoded{Type: inner.Type, Data: data}, nil
}

// InnerFrameV2Decoded is the post-validation form of an InnerFrameV2:
// Type matches one of the discriminator constants and Data has been
// base64-decoded with the size cap enforced.
type InnerFrameV2Decoded struct {
	Type string
	Data []byte
}

// supportedV2Capabilities is the daemon's authoritative capability set. The
// negotiation output is built from THESE entries only — never from the phone's
// advertised set — so an unsupported/spoofed advertisement can never be echoed
// in the hello_ack or recorded on the session.
var supportedV2Capabilities = []string{protocol.CapabilityInteractive}

// negotiateCapabilities returns the phone's advertised set ∩
// supportedV2Capabilities, in supported-set order. It iterates the supported
// set (not the advertised one), so the result is a subset of supported by
// construction: duplicates collapse, an unsupported/spoofed advertisement is
// dropped, and an advertise-nothing / only-unsupported set yields nil (the
// omitempty ack field then drops the key, preserving v1 byte-stability, and
// the recorded interactive flag fails closed to false).
func negotiateCapabilities(advertised []string) []string {
	var out []string
	for _, name := range supportedV2Capabilities {
		if slices.Contains(advertised, name) {
			out = append(out, name)
		}
	}
	return out
}

// handleNoiseInit processes an inbound noise_init frame. The initial
// handshake path is documented step-by-step in
// docs/specs/architecture/445-internal-relay-v2-inner-frame-handshake-token-gating.md.
// A noise_init arriving in V2StateOpen is a phone-initiated re-key
// (docs/protocol-mobile.md § Re-key); it is routed to handleRekeyInit,
// which runs IK responder again and atomically swaps s.send / s.recv.
// noise_init in V2StateHandshakeComplete remains a state-machine
// violation (CipherStates are held but uncommitted; a fresh handshake
// at that point is indistinguishable from a wire-protocol violation).
func (m *V2SessionManager) handleNoiseInit(ctx context.Context, s *V2Session, inner InnerFrameV2Decoded) {
	switch s.state {
	case V2StateOpen:
		m.handleRekeyInit(ctx, s, inner)
		return
	case V2StateAwaitingInit:
		// fall through to the initial-handshake body below
	default: // V2StateHandshakeComplete; V2StateClosed is filtered earlier in handleFrame.
		m.cfg.Logger.Warn("relay: v2 state reject",
			"event", "v2.state.reject",
			"conn_id", s.connID,
			"close_code", int(StatusProtocolMismatch),
			"reason", "noise_init_in_handshake_complete")
		m.closeWith(ctx, s, StatusProtocolMismatch, nil)
		return
	}

	// Lazy-construct the responder on first noise_init for this conn.
	resp, err := noise.NewResponder(m.cfg.StaticPriv)
	if err != nil {
		// Realistically unreachable: StaticPriv length is validated at
		// NewV2SessionManager and key derivation is deterministic. Close
		// at 4426 anyway — without a Responder we cannot proceed.
		m.cfg.Logger.Warn("relay: v2 handshake reject",
			"event", "v2.handshake.reject.ik_failure",
			"conn_id", s.connID,
			"close_code", int(StatusHandshakeFailure),
			"reason", "responder_init_failed")
		m.closeWith(ctx, s, StatusHandshakeFailure, nil)
		return
	}
	s.resp = resp

	earlyData, err := s.resp.ReadInit(inner.Data)
	if err != nil {
		// MAC failure, malformed IK message 1, wrong static pubkey. No
		// AEAD channel exists; close-only at 4426.
		m.cfg.Logger.Warn("relay: v2 handshake reject",
			"event", "v2.handshake.reject.ik_failure",
			"conn_id", s.connID,
			"close_code", int(StatusHandshakeFailure))
		m.closeWith(ctx, s, StatusHandshakeFailure, nil)
		return
	}
	// Pin the initiator's static pub at the earliest authenticated
	// point — ReadInit success means flynn has MAC-verified and
	// decrypted the static. Consumed by the re-key responder's
	// peer-continuity check (#453); inert in this slice.
	s.peerStatic = s.resp.PeerStatic()

	var helloEnv protocol.Envelope
	if err := json.Unmarshal(earlyData, &helloEnv); err != nil {
		// Malformed early-data envelope: handshake-layer protocol
		// violation. CipherStates don't exist yet, so close-only at 4421
		// (cannot AEAD-seal an error envelope without WriteResp).
		m.cfg.Logger.Warn("relay: v2 state reject",
			"event", "v2.state.reject",
			"conn_id", s.connID,
			"close_code", int(StatusProtocolMismatch),
			"reason", "early_data_not_envelope")
		m.closeWith(ctx, s, StatusProtocolMismatch, nil)
		return
	}
	if helloEnv.Type != protocol.TypeHello {
		m.cfg.Logger.Warn("relay: v2 state reject",
			"event", "v2.state.reject",
			"conn_id", s.connID,
			"close_code", int(StatusProtocolMismatch),
			"reason", "early_data_not_hello")
		m.closeWith(ctx, s, StatusProtocolMismatch, nil)
		return
	}
	var helloPayload protocol.HelloClientPayload
	if err := json.Unmarshal(helloEnv.Payload, &helloPayload); err != nil {
		m.cfg.Logger.Warn("relay: v2 state reject",
			"event", "v2.state.reject",
			"conn_id", s.connID,
			"close_code", int(StatusProtocolMismatch),
			"reason", "hello_payload_decode")
		m.closeWith(ctx, s, StatusProtocolMismatch, nil)
		return
	}

	// Negotiate capabilities: intersect the phone's advertised set with the
	// daemon's authoritative supported set. The result is echoed in the
	// hello_ack on every handshake and (on the token-OK branch only) recorded
	// as s.interactive below. Computed before the ack literal so the same
	// negotiated slice is the single source of truth for both the echo and the
	// flag — ack and flag can never disagree.
	negotiated := negotiateCapabilities(helloPayload.Capabilities)

	// Build and AEAD-seal hello_ack via WriteResp's early-data slot. The
	// hello_ack carries InReplyTo=hello.ID to mirror v1's request/response
	// pairing convention (auth.go's buildResponse).
	helloID := helloEnv.ID
	ackPayload, err := json.Marshal(protocol.HelloAckPayload{
		ProtocolVersion: "v2",
		ServerID:        m.cfg.ServerID,
		ConnID:          s.connID,
		Capabilities:    negotiated, // omitempty: nil/empty → key absent
	})
	if err != nil {
		m.cfg.Logger.Warn("relay: v2 handshake reject",
			"event", "v2.handshake.reject.ik_failure",
			"conn_id", s.connID,
			"close_code", int(StatusHandshakeFailure),
			"reason", "ack_payload_marshal")
		m.closeWith(ctx, s, StatusHandshakeFailure, nil)
		return
	}
	ackEnv := protocol.Envelope{
		ID:        1,
		Type:      protocol.TypeHelloAck,
		TS:        time.Now().UTC(),
		Payload:   ackPayload,
		InReplyTo: &helloID,
	}
	ackJSON, err := json.Marshal(ackEnv)
	if err != nil {
		m.cfg.Logger.Warn("relay: v2 handshake reject",
			"event", "v2.handshake.reject.ik_failure",
			"conn_id", s.connID,
			"close_code", int(StatusHandshakeFailure),
			"reason", "ack_envelope_marshal")
		m.closeWith(ctx, s, StatusHandshakeFailure, nil)
		return
	}

	respMsg, sendCS, recvCS, err := s.resp.WriteResp(ackJSON)
	if err != nil {
		// Realistically unreachable under correct flynn/noise.
		m.cfg.Logger.Warn("relay: v2 handshake reject",
			"event", "v2.handshake.reject.ik_failure",
			"conn_id", s.connID,
			"close_code", int(StatusHandshakeFailure),
			"reason", "write_resp")
		m.closeWith(ctx, s, StatusHandshakeFailure, nil)
		return
	}
	s.send = sendCS
	s.recv = recvCS
	// State transitions to handshakeComplete BEFORE token validation —
	// observably distinct from open. The gating test pins this.
	s.state = V2StateHandshakeComplete

	respFrame, err := marshalInnerFrameV2(protocol.TypeNoiseResp, respMsg)
	if err != nil {
		m.cfg.Logger.Warn("relay: v2 handshake reject",
			"event", "v2.handshake.reject.ik_failure",
			"conn_id", s.connID,
			"close_code", int(StatusHandshakeFailure),
			"reason", "marshal_noise_resp")
		m.closeWith(ctx, s, StatusHandshakeFailure, nil)
		return
	}

	device, tokenOK := m.cfg.Devices.Validate(helloPayload.Token)
	if !tokenOK {
		// Token-failure path: emit AEAD-sealed error envelope and the
		// 4401 close in a SINGLE routing envelope so the phone observes
		// the error frame before the WS close (spec § Failure modes,
		// line 436; matches v1 dispatcher's atomicity pattern).
		errFrame, sealErr := m.sealError(s, protocol.CodeAuthInvalidToken,
			MsgInvalidToken, helloID)
		m.cfg.Logger.Warn("relay: v2 handshake reject",
			"event", "v2.handshake.reject.invalid_token",
			"conn_id", s.connID,
			"close_code", int(StatusUnauthorized))
		// Best-effort: send noise_resp first (so the AEAD channel exists
		// on the wire), then the error+close combined envelope.
		m.send(protocol.RoutingEnvelope{ConnID: s.connID, Frame: respFrame})
		if sealErr != nil {
			// AEAD seal failed — drop the error frame, still emit 4401.
			m.cfg.Logger.Warn("relay: v2 seal error failed; close-only",
				"conn_id", s.connID, "err", sealErr)
			m.closeWith(ctx, s, StatusUnauthorized, nil)
			return
		}
		m.closeWith(ctx, s, StatusUnauthorized, errFrame)
		return
	}

	// Success: emit noise_resp, advance to open. Capture the matched
	// device snapshot so dispatchAppFrame can surface it via *dispatch.Conn
	// for handlers that consult c.Auth().
	m.cfg.Logger.Info("relay: v2 handshake accept",
		"event", "v2.handshake.accept",
		"conn_id", s.connID,
		"device_name", device.Name)
	m.send(protocol.RoutingEnvelope{ConnID: s.connID, Frame: respFrame})
	s.device = &device
	// Record the negotiated interactive decision from the same slice the ack
	// echoed (single source of truth) BEFORE the session becomes enumerable.
	// A spoofed/unsupported advertisement never appears in negotiated, so it
	// can never flag the session.
	s.interactive = slices.Contains(negotiated, protocol.CapabilityInteractive)
	s.state = V2StateOpen
	// Create the per-session push buffer now that the session is authenticated
	// and enumerable. queue-exists ⟺ V2StateOpen; an off-Run Push finds this
	// queue under pushMu without reading Run-owned m.sessions. Mutating the map
	// here (on Run) and in closeWith (on Run) keeps the key set Run-owned.
	m.pushMu.Lock()
	m.queues[s.connID] = &pushQueue{}
	m.pushMu.Unlock()
	s.rekeyTimer = m.armRekeyTimer(ctx, s)

	// Mid-turn-reconnect replay (#647): if the phone advertised where it left
	// off, replay the conversation's missed tail (or emit a resync marker) on
	// this conn before Run returns to its select to service the live stream
	// (AC-2's "before the live stream resumes"). The hook is at the very tail
	// of the success path — noise_resp is sent (the phone's recv CipherState
	// exists), s.state is V2StateOpen (forwardEnvelope's gate passes), and the
	// push queue exists — so replay frames seal under the fresh session keys as
	// the first AEAD-transport frames. last_event_id is untrusted remote input:
	// replayMissed range/ring-bounds it and scopes it to the daemon-resolved
	// conversation (never one the phone names).
	if helloPayload.LastEventID != nil {
		m.replayMissed(ctx, s, *helloPayload.LastEventID)
	}
}

// handleRekeyInit runs the responder side of a phone-initiated re-key
// handshake. Same shape as the initial handshake (NewResponder →
// ReadInit → WriteResp → emit noise_resp) with three differences:
//
//  1. Early-data is empty on both directions (docs/protocol-mobile.md
//     § Re-key). Hello validation and token re-check are skipped — they
//     ran at initial handshake. The per-rekey identity gate is the
//     peer-static continuity check below, not the token.
//  2. Peer-static continuity is enforced: the new initiator's static
//     pub (from resp.PeerStatic() after ReadInit) MUST equal
//     s.peerStatic captured at initial handshake. Mismatch closes at
//     4426, the same code the initial handshake uses for IK-related
//     failure.
//  3. CipherState swap on success: a SINGLE tuple assignment
//     `s.send, s.recv = newSend, newRecv` on the manager's single
//     dispatch goroutine. No half-mixed state where one direction uses
//     new keys and the other uses old. State stays V2StateOpen;
//     s.device and s.peerStatic are preserved. The old *CipherState
//     pointers are dropped from the struct; Go's GC reclaims the
//     underlying memory. An explicit Wipe() of the key bytes is NOT
//     exposed (would require touching internal/noise's surface —
//     deferred); the single-owner-goroutine invariant means no code
//     path observes the old state after the swap.
//
// Failure paths reuse the initial handshake's close code (4426) and
// closeWith primitive — no new close code is introduced. closeWith
// removes the session entry, so the next inbound frame on the same
// conn_id lazy-creates a fresh V2StateAwaitingInit session.
func (m *V2SessionManager) handleRekeyInit(ctx context.Context, s *V2Session, inner InnerFrameV2Decoded) {
	resp, err := noise.NewResponder(m.cfg.StaticPriv)
	if err != nil {
		// Realistically unreachable: StaticPriv length is validated at
		// NewV2SessionManager. Close at 4426 anyway — without a Responder
		// we cannot proceed.
		m.cfg.Logger.Warn("relay: v2 handshake reject",
			"event", "v2.handshake.reject.ik_failure",
			"conn_id", s.connID,
			"close_code", int(StatusHandshakeFailure),
			"reason", "rekey_responder_init_failed")
		m.closeWith(ctx, s, StatusHandshakeFailure, nil)
		return
	}

	// Re-key noise_init carries empty early-data per spec; discard the
	// returned slice unconditionally.
	if _, err := resp.ReadInit(inner.Data); err != nil {
		// MAC failure, malformed IK message 1, or wrong responder static.
		// No re-key CipherStates exist; close-only at 4426.
		m.cfg.Logger.Warn("relay: v2 handshake reject",
			"event", "v2.handshake.reject.ik_failure",
			"conn_id", s.connID,
			"close_code", int(StatusHandshakeFailure),
			"reason", "rekey_read_init_failed")
		m.closeWith(ctx, s, StatusHandshakeFailure, nil)
		return
	}

	// Peer-static continuity check: pins the post-rekey AEAD channel to
	// the same peer identity as the initial handshake (Threat #3
	// residual-risk claim). bytes.Equal is intentional and variable-time
	// acceptable — both operands are public keys (live peer's static
	// from resp.PeerStatic() and the stored s.peerStatic), so timing
	// leakage carries no secret. device_name is intentionally omitted
	// from the reject log line: the re-key initiator's identity is
	// unknown / hostile, and logging the captured-at-initial-handshake
	// device-name on a rejected re-key would be an anti-enumeration
	// signal.
	if !bytes.Equal(resp.PeerStatic(), s.peerStatic) {
		m.cfg.Logger.Warn("relay: v2 handshake reject",
			"event", "v2.handshake.reject.ik_failure",
			"conn_id", s.connID,
			"close_code", int(StatusHandshakeFailure),
			"reason", "rekey_peer_static_mismatch")
		m.closeWith(ctx, s, StatusHandshakeFailure, nil)
		return
	}

	// Re-key noise_resp carries empty early-data per spec.
	respMsg, newSend, newRecv, err := resp.WriteResp(nil)
	if err != nil {
		// Realistically unreachable under correct flynn/noise.
		m.cfg.Logger.Warn("relay: v2 handshake reject",
			"event", "v2.handshake.reject.ik_failure",
			"conn_id", s.connID,
			"close_code", int(StatusHandshakeFailure),
			"reason", "rekey_write_resp_failed")
		m.closeWith(ctx, s, StatusHandshakeFailure, nil)
		return
	}

	respFrame, err := marshalInnerFrameV2(protocol.TypeNoiseResp, respMsg)
	if err != nil {
		m.cfg.Logger.Warn("relay: v2 handshake reject",
			"event", "v2.handshake.reject.ik_failure",
			"conn_id", s.connID,
			"close_code", int(StatusHandshakeFailure),
			"reason", "rekey_marshal_noise_resp")
		m.closeWith(ctx, s, StatusHandshakeFailure, nil)
		return
	}

	// Atomic swap on the single dispatch goroutine. The old
	// *CipherState pointers are dropped from the struct here; the GC
	// reclaims the underlying memory once no further reference exists.
	// State stays V2StateOpen; s.device and s.peerStatic are preserved.
	s.send, s.recv = newSend, newRecv

	m.cfg.Logger.Info("relay: v2 rekey accept",
		"event", "v2.rekey.accept",
		"conn_id", s.connID,
		"device_name", s.device.Name)
	m.send(protocol.RoutingEnvelope{ConnID: s.connID, Frame: respFrame})
	s.rekeyComplete(m, ctx)
}

// handleNoiseMsg processes an inbound noise_msg frame. The
// handshakeComplete branch is the gating invariant: a noise_msg
// arriving while we hold CipherStates but have not yet validated the
// token is rejected as auth.invalid_token. The open branch AEAD-decrypts
// the payload and dispatches the inner v1-shaped envelope through the
// existing handler chain; AEAD failures close the conn at 4421.
func (m *V2SessionManager) handleNoiseMsg(ctx context.Context, s *V2Session, inner InnerFrameV2Decoded) {
	switch s.state {
	case V2StateAwaitingInit:
		// No CipherStates yet; close-only at 4421.
		m.cfg.Logger.Warn("relay: v2 state reject",
			"event", "v2.state.reject",
			"conn_id", s.connID,
			"close_code", int(StatusProtocolMismatch),
			"reason", "noise_msg_before_handshake")
		m.closeWith(ctx, s, StatusProtocolMismatch, nil)
		return
	case V2StateHandshakeComplete:
		// Gating invariant: try to AEAD-decrypt and decode. If the frame
		// decrypts cleanly to a non-hello envelope, take the
		// auth.invalid_token path; otherwise reject at 4421.
		plaintext, err := s.recv.Decrypt(inner.Data)
		if err != nil {
			m.cfg.Logger.Warn("relay: v2 state reject",
				"event", "v2.state.reject",
				"conn_id", s.connID,
				"close_code", int(StatusProtocolMismatch),
				"reason", "noise_msg_decrypt_failed")
			m.closeWith(ctx, s, StatusProtocolMismatch, nil)
			return
		}
		var env protocol.Envelope
		if err := json.Unmarshal(plaintext, &env); err != nil {
			m.cfg.Logger.Warn("relay: v2 state reject",
				"event", "v2.state.reject",
				"conn_id", s.connID,
				"close_code", int(StatusProtocolMismatch),
				"reason", "noise_msg_envelope_decode")
			m.closeWith(ctx, s, StatusProtocolMismatch, nil)
			return
		}
		// Decrypted cleanly: token was not validated; reject as
		// auth.invalid_token regardless of envelope type. The handler
		// chain MUST NOT be reached from handshakeComplete (AC #4).
		errFrame, sealErr := m.sealError(s, protocol.CodeAuthInvalidToken,
			MsgInvalidToken, env.ID)
		m.cfg.Logger.Warn("relay: v2 handshake reject",
			"event", "v2.handshake.reject.invalid_token",
			"conn_id", s.connID,
			"close_code", int(StatusUnauthorized),
			"reason", "noise_msg_in_handshake_complete")
		if sealErr != nil {
			m.cfg.Logger.Warn("relay: v2 seal error failed; close-only",
				"conn_id", s.connID, "err", sealErr)
			m.closeWith(ctx, s, StatusUnauthorized, nil)
			return
		}
		m.closeWith(ctx, s, StatusUnauthorized, errFrame)
		return
	case V2StateOpen:
		plaintext, err := s.recv.Decrypt(inner.Data)
		if err != nil {
			// Tampered, replayed, or truncated frame. flynn/noise leaves
			// the receive counter unchanged on Decrypt failure, but the
			// channel is no longer trustworthy: close at 4421 and drop the
			// session entry so a subsequent noise_init for the same
			// conn_id starts a fresh awaitingInit (AC #2, #3). Do NOT log
			// the underlying error text — the AEAD ciphertext and counter
			// indices are not operator-actionable and stay out of the log
			// channel per the spec's security review.
			m.cfg.Logger.Warn("relay: v2 aead fail",
				"event", "v2.aead.fail",
				"conn_id", s.connID,
				"close_code", int(StatusProtocolMismatch))
			m.closeWith(ctx, s, StatusProtocolMismatch, nil)
			return
		}
		m.dispatchAppFrame(ctx, s, plaintext)
		return
	}
}

// dispatchAppFrame runs dispatch.Route on a per-frame *dispatch.Conn,
// drains any reply envelopes the handler emitted, AEAD-seals each one
// under s.send, wraps as noise_msg, and forwards via m.send.
// Synchronous; returns only after Route has returned and all replies
// have been drained.
//
// Assumes handlers are synchronous and do not spawn long-lived
// goroutines that retain a reference to conn; the per-frame outbound
// buffer (handlerOutboundBuf) is large enough to absorb a handler's
// synchronous replies without blocking c.Send. The channel is
// deliberately NOT closed — a misbehaving handler that forks a sender
// after Route returns writes into a leaked but capacity-bounded channel
// that the GC reclaims once the goroutine exits; closing here would
// panic such a sender.
func (m *V2SessionManager) dispatchAppFrame(ctx context.Context, s *V2Session, plaintext []byte) {
	// v2 control-envelope discriminator: a successful JSON decode whose
	// type matches a v2 control type is routed away from the v1
	// application dispatch chain. The probe is intentionally a re-decode;
	// dispatch.Route below decodes the same plaintext a second time. Cost
	// is one small JSON parse per application frame, well below the
	// per-frame AEAD cost. A JSON decode failure here deliberately falls
	// through so dispatch.Route's malformed-envelope branch emits the
	// sealed protocol.malformed reply established by #446 unchanged.
	var probeEnv protocol.Envelope
	if err := json.Unmarshal(plaintext, &probeEnv); err == nil {
		switch probeEnv.Type {
		case protocol.TypeRekeyRequest:
			m.handleRekeyRequest(ctx, s, probeEnv)
			return
		case protocol.TypeRequestSnapshot:
			m.handleRequestSnapshot(ctx, s, probeEnv)
			return
		case protocol.TypeModalCancel:
			m.handleModalCancel(ctx, s, probeEnv)
			return
		case protocol.TypeModalAnswer:
			m.handleModalAnswer(ctx, s, probeEnv)
			return
		case protocol.TypeInterrupt:
			m.handleInterrupt(s)
			return
		case protocol.TypeDequeueMessage:
			m.handleDequeueMessage(s, probeEnv)
			return
		}
	}

	outbound := make(chan protocol.RoutingEnvelope, handlerOutboundBuf)
	conn := dispatch.NewConn(s.connID, outbound, s.device)
	dispatch.Route(ctx, m.cfg.Logger, conn, m.cfg.Handlers, plaintext)
	for {
		select {
		case reply := <-outbound:
			ciphertext, err := s.send.Encrypt(reply.Frame)
			if err != nil {
				// Realistically unreachable under correct flynn/noise.
				// Drop the reply rather than emit the unencrypted frame.
				m.cfg.Logger.Warn("relay: v2 seal app reply failed; reply dropped",
					"conn_id", s.connID)
				continue
			}
			frame, err := marshalInnerFrameV2(protocol.TypeNoiseMsg, ciphertext)
			if err != nil {
				m.cfg.Logger.Warn("relay: v2 marshal app reply failed; reply dropped",
					"conn_id", s.connID)
				continue
			}
			// The reply's CloseCode is ignored: handlers do not signal
			// closes through c.Send/c.Reply; the close-code field on the
			// routing envelope is reserved for the manager's own
			// close-intent emissions (closeWith).
			m.send(protocol.RoutingEnvelope{ConnID: s.connID, Frame: frame})
		default:
			return
		}
	}
}

// handleRekeyRequest classifies an inbound v2 rekey_request control
// envelope and emits a single structured log line. The binary is always
// the IK responder (ADR 024); a rekey_request from the phone takes NO
// transport action — no close, no outbound frame, no state change. The
// phone re-keys by sending noise_init directly, not by signalling via
// rekey_request.
//
// payload.reason is matched against the closed set {scheduled, manual,
// compromise} from docs/protocol-mobile.md § Re-key:
//   - recognised values log at INFO,
//   - empty, unknown, or non-string values log at WARN (forward-compat:
//     mobile may add a reason value before the binary catches up).
//
// ctx is accepted for parity with dispatchAppFrame / handleNoiseMsg but
// is unused: the method does no work that needs cancellation. Runs on
// the manager's single dispatch goroutine (same as the rest of this
// file); no new concurrency invariant.
func (m *V2SessionManager) handleRekeyRequest(_ context.Context, s *V2Session, env protocol.Envelope) {
	var payload struct {
		Reason string `json:"reason"`
	}
	// Decode failures are tolerated and treated as empty reason; emitting
	// a sealed protocol.malformed reply for a broken control payload
	// would be a surprising behaviour change since the envelope itself
	// took no transport action either way.
	_ = json.Unmarshal(env.Payload, &payload)

	switch payload.Reason {
	case "scheduled", "manual", "compromise":
		m.cfg.Logger.Info("relay: v2 rekey request received",
			"event", "v2.rekey.request.received",
			"conn_id", s.connID,
			"reason", payload.Reason)
	default:
		m.cfg.Logger.Warn("relay: v2 rekey request received",
			"event", "v2.rekey.request.received",
			"conn_id", s.connID,
			"reason", payload.Reason)
	}
}

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

	snapPayload, err := json.Marshal(protocol.ScreenSnapshotPayload{
		ConversationID: payload.ConversationID,
		Text:           text,
		TS:             time.Now().UTC(),
	})
	if err != nil {
		// ScreenSnapshotPayload is a closed struct of two strings + a time;
		// marshal cannot fail in practice. Defensive — NEVER echo err (it could
		// quote the rendered text). Fall back to a deterministic error reply so
		// the request is still answered, never silently dropped (AC #3).
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
	if err := m.forwardEnvelope(ctx, s.connID, reply); err != nil {
		m.cfg.Logger.Debug("relay: v2 snapshot error reply push dropped",
			"event", "v2.snapshot.err_push",
			"conn_id", s.connID,
			"code", code,
			"err", err)
	}
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

// replayMissed serves the mid-turn-reconnect replay for s after its hello
// advertised last_event_id=afterID (#647). It runs inline on the manager's
// single Run goroutine at the tail of handleNoiseInit's success path, so the
// whole replay completes BEFORE Run returns to its select to service live
// events — the structural guarantee behind AC-2's "before the live stream
// resumes". Replies seal via forwardEnvelope (the established
// handleRequestSnapshot inline-reply pattern), not the buffered push stream.
//
// afterID is untrusted remote input (AC-5): it is only ever an index into the
// self-synchronised ring (ring.After's own mutex makes the read safe off the
// emitter goroutine), scoped to the daemon-resolved conversation (cursor()),
// never to a conversation the phone names. Work is bounded by what the ring
// retains (MaxEventsPerConversation); a hostile-large id classifies as
// caught-up (zero work). SECURITY: replayed payloads are the same structured
// envelopes #649 already streams to this authenticated conn; the bytes are
// never logged.
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
		// The requested position aged out of the bounded ring (AC-4): emit one
		// honest resync marker telling the phone to full-reload, never a
		// partial gap-ful replay. Leave replayThrough untouched — the phone
		// discards its cursor and must accept all live events afterward.
		m.emitResync(ctx, s, convID)
		return
	}
	// Clamp the watermark to server-known reality (#663). afterID is untrusted:
	// a stale cross-/clear id or a hostile 2^64-1 would otherwise set the
	// watermark above this conversation's id space and silently mute every live
	// frame at or below it. min preserves legitimate same-conversation dedup
	// (afterID == newest in the caught-up case there); during the loop the
	// watermark trails one event behind the frame being forwarded, so
	// forwardEnvelope's guard never self-drops a replay envelope.
	s.replayThrough = min(afterID, newest)
	for _, ev := range events {
		id := ev.ID // per-iteration local; never &ev.ID of the range variable.
		replay := protocol.Envelope{
			ID:      ev.ID, // per-conn id ascending + self-consistent within the replay.
			Type:    ev.Type,
			TS:      ev.TS,
			Payload: ev.Payload,
			EventID: &id, // required: the phone advances its cursor from this.
		}
		if err := m.forwardEnvelope(ctx, s.connID, replay); err != nil {
			// Session vanished / seal failure: log at debug and stop — the
			// package's outbound-drop posture (mirrors handleRequestSnapshot).
			// NEVER echo payload/ciphertext/key bytes.
			m.cfg.Logger.Debug("relay: v2 reconnect replay frame dropped",
				"event", "v2.replay.frame_dropped",
				"conn_id", s.connID,
				"err", err)
			return
		}
		s.replayThrough = ev.ID
	}
}

// emitResync forwards a single resync marker to s, signalling that its
// advertised last_event_id aged out of the ring and it must do a full reload of
// convID (#647, AC-4). The marker is a TypeResync control envelope carrying
// only convID in an inline anonymous payload — no named protocol payload type,
// mirroring emitRekeyRequest's payload-less inline-struct control precedent. It
// carries NO EventID (it is not a structured event), so forwardEnvelope's
// replay-watermark guard never touches it.
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

// emitRekeyRequest builds an AEAD-sealed rekey_request envelope under
// s.send, wraps it as a noise_msg inner frame, forwards via m.send,
// then sets s.awaitingRekeyReply=true and arms s.rekeyReplyTimer.
// Called on the manager's single dispatch goroutine from two sites:
// handleWake's wakeRekeyEmit arm passes reason="scheduled" (timer-
// driven), and handleManualRekey passes reason="manual" (operator-
// triggered via the control socket — docs/protocol-mobile.md § Re-key).
// The "compromise" reason is reserved for a future caller.
//
// Envelope ID is fixed at 1: there is no rekey_ack response that would
// correlate by InReplyTo (the spec is explicit — the next successful
// AEAD round-trip under the new keys is the implicit ack).
//
// AEAD-seal failure is realistically unreachable under correct
// flynn/noise (same posture as sealError). On seal or marshal failure
// the frame is dropped and a WARN line emitted; the conn is NOT
// closed — the session remains in V2StateOpen. On the scheduled path
// the next 1-hour cadence will attempt another emit; on the manual
// path the operator can re-run pyry rekey to retry.
func (m *V2SessionManager) emitRekeyRequest(ctx context.Context, s *V2Session, reason string) {
	// Defensive: a wakeRekeyEmit arriving while already awaiting a
	// reply would re-emit. Skip — the in-flight emit's reply window is
	// still ticking. (Should not happen under normal operation;
	// rekeyTimer is one-shot and only re-armed by rekeyComplete, which
	// clears the bool first.)
	if s.awaitingRekeyReply {
		m.cfg.Logger.Warn("relay: v2 rekey emit skipped",
			"event", "v2.rekey.emit.skipped_already_awaiting",
			"conn_id", s.connID)
		return
	}

	reqPayload, err := json.Marshal(struct {
		Reason string `json:"reason"`
	}{Reason: reason})
	if err != nil {
		m.cfg.Logger.Warn("relay: v2 rekey emit marshal failed",
			"event", "v2.rekey.emit.marshal_failed",
			"conn_id", s.connID)
		return
	}
	envelope := protocol.Envelope{
		ID:      1,
		Type:    protocol.TypeRekeyRequest,
		TS:      time.Now().UTC(),
		Payload: reqPayload,
	}
	envJSON, err := json.Marshal(envelope)
	if err != nil {
		m.cfg.Logger.Warn("relay: v2 rekey emit marshal failed",
			"event", "v2.rekey.emit.marshal_failed",
			"conn_id", s.connID)
		return
	}
	ciphertext, err := s.send.Encrypt(envJSON)
	if err != nil {
		m.cfg.Logger.Warn("relay: v2 rekey emit seal failed",
			"event", "v2.rekey.emit.seal_failed",
			"conn_id", s.connID)
		return
	}
	frame, err := marshalInnerFrameV2(protocol.TypeNoiseMsg, ciphertext)
	if err != nil {
		m.cfg.Logger.Warn("relay: v2 rekey emit marshal failed",
			"event", "v2.rekey.emit.marshal_failed",
			"conn_id", s.connID)
		return
	}
	m.send(protocol.RoutingEnvelope{ConnID: s.connID, Frame: frame})
	s.awaitingRekeyReply = true
	s.rekeyReplyTimer = m.armRekeyReplyTimer(ctx, s)
	m.cfg.Logger.Info("relay: v2 rekey emit",
		"event", "v2.rekey.emit",
		"conn_id", s.connID,
		"reason", reason)
}

// sealError builds a TypeError envelope, AEAD-seals it under s.send,
// and returns the wrapped noise_msg inner-frame JSON ready for the
// Frame slot of a RoutingEnvelope. Returns a non-nil error only if the
// AEAD seal itself failed; JSON marshal failures of static
// well-typed values are wrapped but practically unreachable.
func (m *V2SessionManager) sealError(s *V2Session, code, message string, inReplyTo uint64) (json.RawMessage, error) {
	errPayload, err := json.Marshal(protocol.ErrorPayload{
		Code:      code,
		Message:   message,
		Retryable: false,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal error payload: %w", err)
	}
	envelope := protocol.Envelope{
		ID:        2,
		Type:      protocol.TypeError,
		TS:        time.Now().UTC(),
		Payload:   errPayload,
		InReplyTo: &inReplyTo,
	}
	envJSON, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("marshal error envelope: %w", err)
	}
	ciphertext, err := s.send.Encrypt(envJSON)
	if err != nil {
		return nil, fmt.Errorf("aead seal error envelope: %w", err)
	}
	return marshalInnerFrameV2(protocol.TypeNoiseMsg, ciphertext)
}

// marshalInnerFrameV2 wraps rawBytes as an InnerFrameV2 of the given
// type, base64-encoding rawBytes for the wire.
func marshalInnerFrameV2(frameType string, rawBytes []byte) (json.RawMessage, error) {
	out, err := json.Marshal(protocol.InnerFrameV2{
		Version: protocol.V2Version,
		Type:    frameType,
		Data:    base64.StdEncoding.EncodeToString(rawBytes),
	})
	if err != nil {
		return nil, fmt.Errorf("marshal inner frame %s: %w", frameType, err)
	}
	return out, nil
}

// closeWith transitions s to V2StateClosed, deletes the session from
// the manager, and emits a single routing envelope carrying Frame (when
// non-nil) and CloseCode. The atomic Frame+CloseCode is what guarantees
// the spec's ordering MUST: phone observes the error frame before the
// WS close (spec § Error handling, line 436). Honors ctx by checking
// before the send; the Outbound call itself is synchronous.
func (m *V2SessionManager) closeWith(ctx context.Context, s *V2Session, code websocket.StatusCode, frame json.RawMessage) {
	if s.state == V2StateClosed {
		return
	}
	s.state = V2StateClosed
	// Stop per-session timers to free runtime timer-heap entries and
	// ensure no pending callback goroutine blocks indefinitely on
	// m.wake. The callback's ctx.Done arm is the load-bearing teardown
	// (Run-derived runCtx cancels on Run exit); these Stop() calls are
	// the defensive belt and are safe to call on a fired timer
	// (returns false, no-op).
	if s.rekeyTimer != nil {
		s.rekeyTimer.Stop()
		s.rekeyTimer = nil
	}
	if s.rekeyReplyTimer != nil {
		s.rekeyReplyTimer.Stop()
		s.rekeyReplyTimer = nil
	}
	delete(m.sessions, s.connID)
	// Symmetric with the create in handleNoiseInit: drop the per-session push
	// buffer. Any buffered-but-undrained envelopes are discarded — the conn is
	// terminal (#611 reconnect replay, not this ticket, reconciles a returning
	// phone). The close envelope itself is sent synchronously below, bypassing
	// the buffer (it is terminal, not part of the ordered push stream).
	m.pushMu.Lock()
	delete(m.queues, s.connID)
	m.pushMu.Unlock()
	env := protocol.RoutingEnvelope{
		ConnID:    s.connID,
		Frame:     frame,
		CloseCode: uint16(code),
	}
	if ctx.Err() != nil {
		return
	}
	m.send(env)
}

// send forwards env via cfg.Outbound and logs any transport error at
// debug. Mirrors v1's cmd/pyry/relay.go forwarder posture: a non-nil
// error means the relay leg is currently unhealthy; the transport
// reconnect handles recovery and the frame is lost (consistent with the
// v1 protocol contract that frames sent while disconnected are dropped).
func (m *V2SessionManager) send(env protocol.RoutingEnvelope) {
	if err := m.cfg.Outbound(env); err != nil {
		// transport.ErrDisconnected / ErrNotConnected are expected during
		// reconnect; log at debug to keep the warn channel clean.
		m.cfg.Logger.Debug("relay: v2 outbound drop",
			"conn_id", env.ConnID,
			"close_code", env.CloseCode,
			"err", err)
	}
}

// Rekey satisfies control.Rekeyer. It funnels the request onto Run's
// dispatch goroutine via m.manualRekey so the lookup + emit sequence
// runs under the single-owner-goroutine invariant on s.send / s.state /
// s.rekeyTimer — no new lock or atomic is introduced.
//
// Returns ErrConnNotFound (wraps control.ErrConnNotFound, so the
// dispatcher's errors.Is check maps to ErrCodeConnNotFound on the
// wire), ErrSessionNotOpen for sessions not in V2StateOpen or already
// awaiting a rekey reply, ctx.Err() on caller cancellation, or any
// transport-layer error surfaced by the emit path (no such error is
// returned today — seal failures are logged and dropped per
// emitRekeyRequest's documented posture).
//
// Production wire-up of *V2SessionManager into the cmd/pyry daemon
// lands in a separate ticket; until then this method is reachable
// only from internal/relay tests.
var _ control.Rekeyer = (*V2SessionManager)(nil)

func (m *V2SessionManager) Rekey(ctx context.Context, connID string) error {
	req := manualRekeyReq{connID: connID, reply: make(chan error, 1)}
	select {
	case m.manualRekey <- req:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-req.reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// handleManualRekey runs on Run's dispatch goroutine. It validates the
// session is eligible for a manual rekey, stops the scheduled 1-hour
// timer so a manual emit at T+45min does not also trigger a scheduled
// emit at T+60min (rekeyComplete re-arms a fresh timer on the responder
// reply), and reuses the existing emitRekeyRequest machinery with
// reason="manual".
//
// Sessions awaiting a prior rekey reply collapse into ErrSessionNotOpen
// — the same sentinel as "not in V2StateOpen" — because from the
// operator's perspective both states mean "the conn is not ready for
// a fresh manual rekey." This also imposes a natural per-conn rate
// limit of one manual rekey per rekeyReplyTimeout (30s in production).
func (m *V2SessionManager) handleManualRekey(ctx context.Context, connID string) error {
	s, ok := m.sessions[connID]
	if !ok {
		return ErrConnNotFound
	}
	if s.state != V2StateOpen {
		return ErrSessionNotOpen
	}
	if s.awaitingRekeyReply {
		return ErrSessionNotOpen
	}
	// Stop the scheduled 1-hour timer before emitting; rekeyComplete
	// arms a fresh one on the responder reply. Stop()'s bool return is
	// intentionally ignored: a stale wakeRekeyEmit signal already
	// queued onto m.wake is caught by emitRekeyRequest's defensive
	// awaitingRekeyReply skip (the manual emit below sets the bool
	// before Run picks up the stale wake).
	if s.rekeyTimer != nil {
		s.rekeyTimer.Stop()
		s.rekeyTimer = nil
	}
	m.emitRekeyRequest(ctx, s, "manual")
	return nil
}

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
	dropped := q.enqueue(env)
	droppedCount := q.dropped
	m.pushMu.Unlock()

	// Non-blocking wake: a cap-1 channel + this default coalesces concurrent
	// signals; the drain re-signals if more work remains, so no wake is lost.
	select {
	case m.drainCh <- struct{}{}:
	default:
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

// drainOnce pops at most ONE buffered envelope across all sessions and forwards
// it on the Run goroutine. Popping one-per-pass (rather than draining a whole
// buffer) is the "a slow m.send must not re-block the producer" guard: Run
// returns to its select between sends, so ActiveConns / inbound frames / wakes
// are serviced with at most one in-flight Outbound (≤ one WriteTimeout) of
// delay. If any queue still has items after the pop, it re-signals drainCh.
func (m *V2SessionManager) drainOnce(ctx context.Context) {
	m.pushMu.Lock()
	var (
		connID string
		env    protocol.Envelope
		found  bool
	)
	// Go randomises map-range order, giving rough fairness across the
	// realistically-tiny open-conn count.
	for id, q := range m.queues {
		if len(q.items) == 0 {
			continue
		}
		connID = id
		env = q.items[0].env
		q.items[0] = queuedEnv{} // release the envelope for GC; slot slides out below
		q.items = q.items[1:]    // pop head (FIFO)
		found = true
		break
	}
	// After the pop, note whether any queue still has work to re-signal.
	more := false
	if found {
		for _, q := range m.queues {
			if len(q.items) > 0 {
				more = true
				break
			}
		}
	}
	m.pushMu.Unlock()

	if !found {
		return
	}
	if err := m.forwardEnvelope(ctx, connID, env); err != nil {
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
// path, shared by the push-buffer drain (drainOnce) and the snapshot reply
// handlers (handleRequestSnapshot / snapshotReplyError, which call it directly
// because their replies are InReplyTo-correlated and not part of the ordered
// push stream).
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
	// Reconnect-replay dedup (#647): drop a live structured envelope this conn
	// already received via replay. Envelopes with EventID == nil (snapshot,
	// error, rekey, resync) are never structured events and are never dropped;
	// conns that never advertised last_event_id keep replayThrough == 0 and
	// live ids are always >= 1, so the guard is inert for them.
	if env.EventID != nil && *env.EventID <= s.replayThrough {
		return nil
	}
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
	return nil
}

// ActiveConn is one open v2 session in the capability-aware enumeration: its
// routing conn-id and the negotiated interactive-capability decision recorded
// at handshake. It holds only non-secret routing/decision data — never a
// *V2Session, CipherState, key, or plaintext — so the snapshot is safe to hand
// to a consumer goroutine. The downstream structured-stream fan-out selects
// interactive vs non-interactive conns on the Interactive flag.
type ActiveConn struct {
	ConnID      string
	Interactive bool
}

// ActiveConns returns a snapshot of every session currently in V2StateOpen —
// the authenticated, token-validated sessions to which Push may deliver — each
// paired with its negotiated interactive flag. The result is an unordered set
// (Go's randomized map-iteration order); a caller that needs a stable order
// must sort it.
//
// Safe to call from any goroutine other than the dispatch goroutine: the
// request is funneled onto Run via m.snapshot so m.sessions is never read
// concurrently with an in-flight handshake transition, dispatchAppFrame
// reply, re-key swap, or closeWith teardown. It is the enumeration half of
// the server-initiated fan-out primitive — a consumer calls this, then Push on
// each conn-id, fanning interactive events only to conns with Interactive set.
// It is the capability-aware v2 analog of v1's dispatch.Dispatcher.ActiveConns().
//
// Sessions still handshaking (V2StateAwaitingInit) or handshake-complete-but-
// token-unvalidated (V2StateHandshakeComplete) are excluded — the same
// V2StateOpen security gate forwardEnvelope enforces, so the negotiated flag of an
// un-authenticated peer is never observable. A torn-down session (deleted from
// the map by closeWith) cannot appear.
//
// Returns nil on caller ctx cancellation, or when Run has already exited
// (Frames closed, no receiver on m.snapshot) and the caller's ctx then fires
// — both equivalent to "no open sessions" for the broadcast consumer, which
// fans out to nobody this round and re-enumerates on the next turn. nil and an
// empty non-nil slice are interchangeable (both len 0); a snapshot has no
// failure the caller can act on, so no error is returned.
func (m *V2SessionManager) ActiveConns(ctx context.Context) []ActiveConn {
	req := snapshotReq{reply: make(chan []ActiveConn, 1)}
	select {
	case m.snapshot <- req:
	case <-ctx.Done():
		return nil
	}
	select {
	case conns := <-req.reply:
		return conns
	case <-ctx.Done():
		return nil
	}
}

// ActiveConnIDs returns a snapshot of the conn IDs of every session currently
// in V2StateOpen — the authenticated, token-validated sessions to which Push
// may deliver. It is a thin projection over ActiveConns (dropping the
// interactive flag) preserved for the capability-agnostic #589 fan-out
// consumer; its signature and observable contract (unordered set, nil on ctx
// cancellation, non-nil-empty on an empty manager) are unchanged.
//
// Production wire-up of *V2SessionManager into the cmd/pyry daemon for
// server-initiated fan-out lands in a separate ticket (#572); until then this
// method is reachable only from internal/relay tests.
func (m *V2SessionManager) ActiveConnIDs(ctx context.Context) []string {
	conns := m.ActiveConns(ctx)
	if conns == nil {
		// Preserve the nil-on-cancel contract: a cancelled snapshot is nil,
		// distinct from a non-nil-empty snapshot of an open-session-less
		// manager.
		return nil
	}
	ids := make([]string, len(conns))
	for i, c := range conns {
		ids[i] = c.ConnID
	}
	return ids
}

// handleActiveConns runs on Run's dispatch goroutine — the only site that
// reads m.sessions for the snapshot, serialised by Run's select against every
// map write (lazy-create in handleFrame, delete in closeWith, state
// transitions in the handshake handlers). No read can observe a half-updated
// map, a torn s.state, or a torn s.interactive (set before V2StateOpen on the
// same goroutine).
//
// The returned slice is freshly allocated and owned by the caller; it holds
// only conn-id strings + the negotiated interactive bool (non-secret routing /
// decision data), never a *V2Session or any key/plaintext bytes. Order is Go's
// randomized map-iteration order — an unordered set by design: the AC requires
// no ordering and the broadcast consumer fans out order-independently, so no
// O(n log n) sort is paid on the single dispatch goroutine.
func (m *V2SessionManager) handleActiveConns() []ActiveConn {
	out := make([]ActiveConn, 0, len(m.sessions))
	for connID, s := range m.sessions {
		if s.state == V2StateOpen {
			out = append(out, ActiveConn{ConnID: connID, Interactive: s.interactive})
		}
	}
	return out
}

