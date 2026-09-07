package relay

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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
	// StatusIdleTimeout is the WS close code the binary asks the relay to
	// apply when a v2 session receives no inbound frame within idleTimeout
	// and the manager tears it down through the in-repo idle sweep (#774).
	// Echoes HTTP 408 (Request Timeout), consistent with the 44xx←HTTP
	// close-code convention (4401←401, 4404←404, 4409←409, 4429←429). Wire
	// spec: docs/protocol-mobile.md § Error codes, close-code row 4408.
	// Sending it to an already-dropped conn is a harmless relay no-op.
	StatusIdleTimeout websocket.StatusCode = 4408

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

	// StatusQueueOverflow is the WS close code the binary asks the relay to
	// apply when a v2 session's push queue exceeds pushQueueByteCeiling and
	// handlePushOverflow tears it down (#1505). Echoes HTTP 413 (Content Too
	// Large), consistent with the 44xx←HTTP convention. Deliberately NOT
	// StatusIdleTimeout: reusing 4408 would mislabel an overflow as an idle
	// session, the same class of mistake #912 documented when a transport
	// outage surfaced as noise.rekey_failed. Wire spec:
	// docs/protocol-mobile.md § Error codes, close-code row 4413.
	//
	// closeWith is called with a NIL frame on this path — nothing is sealed.
	// The trip condition is a parked drain, which in the #874 case means the
	// transport is down, so sealing an error envelope would burn a Noise
	// send-nonce for a frame that cannot arrive and gap the phone's recv nonce
	// (the #912 hazard). The idle sweep already sets this precedent, and the
	// close code alone suffices because the phone's recovery is a fresh
	// handshake, not a message. A future change that adds a sealed error frame
	// here reintroduces the nonce hazard.
	StatusQueueOverflow websocket.StatusCode = 4413
)

// idleTimeout is the bounded window a v2 session may go without any
// inbound frame before the manager tears it down through the in-repo
// idle sweep (#774). Well short of the 1-hour rekeyInterval so a dropped
// or backgrounded phone's two Noise CipherStates and armed rekey timer
// never linger up to an hour (the relay↔binary leg is a single
// multiplexed WebSocket with no per-connection disconnect frame, so a
// silently-gone phone is only detectable by inbound-frame silence). Long
// enough not to tear down a foregrounded-but-momentarily-quiet phone
// mid-read (which would force a disruptive re-handshake on the next tap).
// Exposed as a package var (lowercase) so tests can substitute a
// sub-second value via a t.Cleanup save-and-restore idiom; not part of
// the public API and not yet config-driven (a deferred concern, same
// posture as modalDenyTimeout's #708 note).
var idleTimeout = 15 * time.Minute

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

// ErrTransportDown is returned by (*V2SessionManager).Rekey when the named
// session is open and eligible but the relay transport is currently down, so
// a manual rekey would seal a rekey_request that cannot reach the phone
// (burning a Noise send-nonce and arming a doomed reply window). Distinct
// from ErrSessionNotOpen so the operator sees "transport down, retry" rather
// than "not open". Surfaced verbatim by the control dispatcher through
// Response.Error with no ErrorCode, the same posture as ErrSessionNotOpen
// (#912).
var ErrTransportDown = errors.New("relay: transport down, rekey deferred - retry")

// wakeKind enumerates the per-session timer events the manager's Run
// goroutine handles on its wake channel. The values are internal and
// MUST NOT be exposed across the package boundary.
type wakeKind int

const (
	wakeRekeyEmit wakeKind = iota
	wakeRekeyReplyTimeout
	wakeIdleTimeout
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

// snapshotReq is enqueued by (*V2SessionManager).ActiveConns and dequeued
// by Run on the snapshot channel arm. reply is per-request (cap=1) so Run's
// reply send is non-blocking even if the caller's ctx fires between enqueue
// and reply. Mirrors manualRekeyReq minus the per-conn inputs — a snapshot
// takes no addressed-conn argument. The reply carries the capability-aware
// enumeration ([]ActiveConn).
type snapshotReq struct {
	reply chan []ActiveConn
}

// appReplyMsg carries one handler reply from a per-conn worker goroutine
// (routeAppFrame) back to Run's dispatch goroutine for sealing (#965). The
// worker never touches s.send — it posts the not-yet-sealed reply here and
// Run's m.appReply arm calls forwardAppReply, keeping every s.send.Encrypt
// on the single-owner Run goroutine. s pins which session's send
// CipherState seals the reply; reply is the marshalled inner frame the
// handler emitted via c.Send.
type appReplyMsg struct {
	s     *V2Session
	reply protocol.RoutingEnvelope
}

// wakeBufferSize sizes the manager's wake channel. The 1-hour rekey
// cadence makes concurrent fires across sessions vanishingly rare; 16
// is a generous safety margin that absorbs the realistic worst case
// (every session times out simultaneously while Run is busy in a slow
// handler invocation) without forcing the timer-callback goroutine to
// block. cap=1 would also be correct.
const wakeBufferSize = 16

// handlerOutboundBuf is the buffer size for the per-frame dispatch.Conn
// outbound channel allocated by routeAppFrame. The three production
// handlers (send_message, list_conversations, register_push_token) emit
// exactly one reply per invocation; Route emits at most one error reply.
// 8 is a generous safety margin and is documented as the
// synchronous-handler assumption in V2SessionConfig.Handlers.
const handlerOutboundBuf = 8

// appFrameQueueDepth bounds the per-conn V2Session.appFrames queue — the
// number of decrypted application-frame plaintexts that may sit awaiting
// the conn's worker goroutine at once (#965). Realistic request/response
// pipelining depth on one conn is 1–2; 16 is a generous margin that never
// trips a legitimate burst while still bounding inbound memory
// deterministically. dispatchAppFrame's non-blocking enqueue tears the
// conn down (4421) on overflow rather than block Run (which would
// reintroduce the cross-conn head-of-line stall this ticket removes). A
// package-level var, not a const, so the overflow test can shrink it.
var appFrameQueueDepth = 16

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

	// idleTimer fires idleTimeout after this session's last inbound frame
	// and drives the in-repo idle sweep (#774). On fire the AfterFunc
	// callback delivers a wakeIdleTimeout signal onto m.wake; the manager's
	// Run goroutine then either reschedules the timer (a frame arrived after
	// the timer was armed, so time.Since(lastActivityAt) < idleTimeout) or
	// tears the session down via closeWith(StatusIdleTimeout /* 4408 */,
	// nil), bounding the lifetime of the two Noise CipherStates under
	// connect/disconnect churn. Armed at V2StateOpen alongside rekeyTimer;
	// stopped (and replaced with nil) by closeWith on any close path. Nil
	// before initial open; nil after closeWith.
	idleTimer *time.Timer

	// lastActivityAt is the instant of the most recent inbound frame,
	// stamped in handleFrame for every frame (handshake or app). It is the
	// idle deadline the idleTimer chases: a fire whose
	// time.Since(lastActivityAt) is still < idleTimeout reschedules for the
	// remaining window instead of tearing down, so the timer ends up firing
	// at exactly lastActivityAt + idleTimeout. Run-owned (written in
	// handleFrame, read in handleWake) — same single-writer regime as
	// state/rekeyTimer, no lock or atomic. Never crosses the wire, so the
	// monotonic reading is retained (PROJECT-MEMORY's time.Time round-trip
	// discipline does not apply).
	lastActivityAt time.Time

	// replayThrough is the highest durable event id already delivered to this
	// conn by the mid-turn-reconnect replay (#647). Run-owned: written in
	// replayMissed (the #663 clamp) and drainReplayOnce (the per-event trailing
	// advance), read in forwardEnvelope, all on the single Run goroutine — same
	// single-writer regime as state/interactive, so no lock or atomic.
	// forwardEnvelope drops a live structured envelope whose EventID
	// <= replayThrough, so the transient replay/live overlap never
	// double-delivers an event (the deterministic dedup proven in spec
	// § Concurrency model). Zero for any conn that never advertised
	// last_event_id, and live ids are always >= 1, so the guard is inert for
	// them.
	replayThrough uint64

	// replayQueue is the not-yet-forwarded tail of a mid-turn-reconnect replay
	// (#777), populated by replayMissed and drained one event per Run pass by
	// drainReplayOnce so a large replay can no longer monopolise the Run loop.
	// Run-owned: written and read only on the dispatch goroutine (replayMissed,
	// drainReplayOnce, drainOnce's gate, closeWith) — no lock or atomic, same
	// regime as state/replayThrough. nil/empty ⇒ no replay in flight. Bounded by
	// ring retention (≤ MaxEventsPerConversation), never the push cap: the
	// events come straight from ring.After's materialised copy, so there is no
	// drop/gap risk a second cap would reintroduce (spec § Open questions).
	replayQueue []eventring.Event

	// appFrames is the per-conn FIFO queue of decrypted application-frame
	// plaintexts handed off to this session's worker goroutine
	// (appFrameWorker) so a slow handler cannot stall the Run loop (#965).
	// Written ONLY on Run (dispatchAppFrame's non-blocking enqueue, after
	// s.recv.Decrypt); read ONLY by the worker. Buffered
	// (appFrameQueueDepth); NEVER closed (avoids a close-vs-send race — the
	// worker terminates via s.done / ctx and the channel is GC'd with the
	// session). Created alongside done in handleNoiseInit's open tail, so a
	// pre-open or non-open session leaves it nil. The plaintext slices are
	// freshly allocated by noise.CipherState.Decrypt, so enqueuing them for
	// later off-Run processing aliases nothing.
	//
	// The element carries the plaintext PLUS the routing decision dispatchAppFrame
	// already made (#1897) — see appFrameJob. The worker never re-derives it.
	appFrames chan appFrameJob

	// bundleAssembling is the accept-side half of the per-conn debug-bundle
	// in-flight gate (#1491), covering the window [request accepted →
	// StreamBundle's enqueue complete] that the queue-derived bundleInFlight scan
	// cannot see: the assembly now runs off Run, so between the accept and the
	// first Push this conn's push queue holds no bundle frame to find. Set in
	// handleDebugBundleRequest at accept, cleared in handleBundleReady after
	// StreamBundle returns — both on Run, and the clear runs in the same Run pass
	// as the enqueue, so the two halves overlap and no second request can slip
	// between them.
	//
	// Run-owned single-writer, exactly like state/replayThrough — no lock, no
	// atomic, and pushMu is NOT involved, so pushMu's "taken alone, never held
	// across an Encrypt, m.send, or any channel op" invariant is untouched by
	// construction rather than by discipline. The off-Run assembleBundle goroutine
	// never reads or writes it.
	//
	// Living on the session rather than in a manager-level map is what makes a
	// permanent lockout impossible: closeWith deletes the session, so a
	// reconnecting conn_id gets a fresh V2Session whose marker is the zero value,
	// and there is no entry to leak or forget to delete.
	bundleAssembling bool

	// done is closed by closeWith to stop this session's worker goroutine
	// without waiting for Run exit (no per-session goroutine leak under conn
	// churn). Follows the timer-cleanup pattern: created alongside appFrames
	// at open, closed exactly once (closeWith's V2StateClosed guard makes it
	// close-once). Closed — never reassigned — so the worker's field read
	// stays race-free (the channel value is stable; only its closed-state
	// flips). Nil for a session that never reached open.
	done chan struct{}
}

// State returns the externally-observable state. Called from the same
// goroutine that mutates today; a cross-goroutine reader (e.g. a
// broadcast layer added in a later slice) would need atomic.Int32 or a
// small mutex — tracked in spec § Open questions.
func (s *V2Session) State() V2SessionState { return s.state }

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

	// replayCh signals "some session has a pending reconnect-replay tail" to
	// Run's replay-drain arm (#777). Mirrors drainCh exactly: capacity 1 with
	// non-blocking sends (from replayMissed and from drainReplayOnce's
	// re-signal) collapses concurrent wakes into at most one pending, so a
	// self-perpetuating pump forwards one replay frame per Run pass with no lost
	// wakeup. Carries no data — the queued events live on each V2Session
	// (replayQueue); this channel references no session and spawns no goroutine,
	// so concurrent multi-conn replays are covered by the single cap-1 channel.
	replayCh chan struct{}

	// snapshot funnels (*V2SessionManager).ActiveConns calls onto Run's dispatch
	// goroutine so the read of m.sessions runs under the single-owner-goroutine
	// invariant, serialised against every map write (lazy-create, delete, state
	// transitions).
	// Unbuffered: backpressure is correct — if Run is busy, the caller waits;
	// the caller's ctx is the escape arm. Not closed by the manager on Run
	// exit; in-flight callers unblock via ctx.Done.
	snapshot chan snapshotReq

	// appReply funnels one handler reply from a per-conn worker goroutine
	// (routeAppFrame) back to Run for sealing (#965), so s.send.Encrypt stays
	// on the single-owner Run goroutine while the handler itself runs off-Run.
	// Mirrors the drainCh/replayCh house style of "off-Run producers, Run
	// consumes-and-seals", but carries data (appReplyMsg) rather than a bare
	// wake — the reply's addressed session travels with it. Unbuffered:
	// backpressure onto the worker is correct (a worker whose replies Run
	// cannot keep up with should slow, not accumulate pending seals in a
	// manager-global channel shared across every conn). Not closed by the
	// manager on Run exit; in-flight workers unblock via runCtx / s.done in
	// forwardToRun.
	appReply chan appReplyMsg

	// pushOverflow carries the connID of a session whose push queue latched
	// pushQueue.overflowed from the off-Run producer goroutine inside Push to
	// the Run goroutine, which tears the session down in handlePushOverflow
	// (#1505). Mirrors modalTimeout's shape — a chan string, buffered
	// (wakeBufferSize) — because enqueue runs off-Run under pushMu and closeWith
	// is Run-owned, exactly the split modalTimeout bridges.
	//
	// The send is NON-blocking (the drainCh idiom), unlike armIdleTimer's
	// blocking send: armIdleTimer blocks a fresh time.AfterFunc goroutine, while
	// Push runs on the producer's goroutine, where blocking would break the
	// never-blocks-the-producer contract this whole queue exists to uphold. The
	// signal is level-triggered instead — the latch is never cleared, so every
	// subsequent Push re-drives a send a full buffer dropped. With no further
	// pushes the queue is frozen at <= the ceiling and the idle sweep reaps it,
	// which is the pre-#1505 bound and strictly no worse.
	pushOverflow chan string

	// bundleReady carries one finished debug-bundle assembly from its off-Run
	// goroutine (assembleBundle) back to Run, which streams it or sends the
	// deterministic error reply in handleBundleReady (#1491). Mirrors appReply's
	// "off-Run producer, Run consumes-and-seals" shape: the archive is built off
	// Run, but every s.send.Encrypt — StreamBundle → drainOnce → forwardEnvelope
	// for the stream, debugBundleReplyError → forwardEnvelope for the failure —
	// stays on the single-owner Run goroutine. The payload type is bundleResult,
	// defined beside the rest of the verb's machinery in v2session_debugbundle.go
	// (the #1025 carve-out).
	//
	// Buffered (wakeBufferSize) like wake / modalTimeout / pushOverflow; it is
	// only pressured by more conns finishing assembly at once than the buffer
	// holds while Run is busy, and assembleBundle's send BLOCKS rather than drops,
	// so that case is correct rather than lossy. Not closed by the manager on Run
	// exit; an in-flight producer unblocks via runCtx / s.done.
	bundleReady chan bundleResult

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
		replayCh:     make(chan struct{}, 1),
		snapshot:     make(chan snapshotReq),
		appReply:     make(chan appReplyMsg),
		modalTimeout: make(chan string, wakeBufferSize),
		pushOverflow: make(chan string, wakeBufferSize),
		bundleReady:  make(chan bundleResult, wakeBufferSize),
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
		case connID := <-m.pushOverflow:
			m.handlePushOverflow(runCtx, connID)
		case res := <-m.bundleReady:
			// A debug bundle finished assembling off Run (#1491). Stream it —
			// or reply with the deterministic error — here, so every
			// s.send.Encrypt stays on the single-owner Run goroutine. The
			// assembly goroutine performed no crypto and touched nothing Run
			// owns.
			m.handleBundleReady(runCtx, res)
		case req := <-m.manualRekey:
			req.reply <- m.handleManualRekey(runCtx, req.connID)
		case <-m.drainCh:
			m.drainOnce(runCtx)
		case <-m.cfg.Reconnect:
			// Fresh transport conn: wake the drain so any #874-held head flushes
			// now (not on the next Push). Re-signal drainCh — do not call
			// drainOnce here — so the existing drain arm owns the single pop path
			// and its FIFO self-re-signal. A nil m.cfg.Reconnect makes this a
			// nil-channel read, permanently not-ready, so the select behaves
			// exactly as pre-#875.
			select {
			case m.drainCh <- struct{}{}:
			default:
			}
		case <-m.replayCh:
			m.drainReplayOnce(runCtx)
		case req := <-m.snapshot:
			req.reply <- m.handleActiveConns()
		case rep := <-m.appReply:
			// A per-conn worker (routeAppFrame) produced a handler reply
			// off-Run; seal it here so every s.send.Encrypt stays on the
			// single-owner Run goroutine (#965). The worker never touches
			// s.send — it only marshalled + pushed the reply through c.Send.
			m.forwardAppReply(rep.s, rep.reply)
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
		if m.transportDown() {
			// #912: the relay leg is down, so a rekey_request sealed now
			// would burn a Noise send-nonce on a frame m.send silently
			// drops — gapping the phone's recv nonce and, one reply window
			// later, tearing this healthy session down as a mislabelled
			// noise.rekey_failed. Defer instead: seal nothing, arm no reply
			// window, and re-arm a short retry so the emit lands once the
			// transport recovers. The fired 1-hour timer that delivered
			// this wake is one-shot and inert, so overwriting rekeyTimer
			// needs no Stop(). A permanently-down phone is reaped by the
			// idle sweep, so this retry loop cannot spin forever.
			w.s.rekeyTimer = m.armRekeyRetryTimer(ctx, w.s)
			m.cfg.Logger.Info("relay: v2 rekey emit deferred (transport down)",
				"event", "v2.rekey.emit.deferred_transport_down",
				"conn_id", w.s.connID,
				"reason", "scheduled",
				"retry_in", rekeyRetryInterval)
			return
		}
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
	case wakeIdleTimeout:
		if idle := time.Since(w.s.lastActivityAt); idle < idleTimeout {
			// A frame arrived after this timer was armed but before the
			// wake was serviced; handleFrame already re-stamped
			// lastActivityAt. Reschedule for the remaining window so the
			// timer fires at exactly lastActivityAt + idleTimeout — no
			// active session is ever torn down by a stale wake.
			w.s.idleTimer = m.armIdleTimer(ctx, w.s, idleTimeout-idle)
			return
		}
		// Genuinely idle: no inbound frame for idleTimeout. Tear down
		// through the existing close path, which drops both CipherStates,
		// stops every per-session timer, and removes the session + push
		// queue.
		m.cfg.Logger.Info("relay: v2 idle teardown",
			"event", "v2.idle.teardown",
			"conn_id", w.s.connID,
			"close_code", int(StatusIdleTimeout))
		m.closeWith(ctx, w.s, StatusIdleTimeout, nil)
	}
}

// armIdleTimer arms the idle-sweep timer (#774) for d. The callback runs
// on a fresh runtime goroutine (time.AfterFunc semantics); it pushes a
// wakeIdleTimeout signal onto m.wake under blocking-send + ctx.Done
// semantics, exactly like armRekeyTimer. The initial arm (in
// handleNoiseInit's success tail) passes idleTimeout; the reschedule arm
// (in handleWake) passes the remaining window so the timer fires at
// exactly lastActivityAt + idleTimeout. The callback touches only m.wake
// — never s.send / s.recv / s.state / m.sessions — so it cannot race the
// cipher states. ctx is the manager's Run-derived runCtx; cancelled on
// Run exit, which unblocks any pending callback goroutine.
func (m *V2SessionManager) armIdleTimer(ctx context.Context, s *V2Session, d time.Duration) *time.Timer {
	return time.AfterFunc(d, func() {
		select {
		case m.wake <- wakeSignal{s: s, kind: wakeIdleTimeout}:
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
	// Every inbound frame — handshake or app — counts as activity for the
	// idle sweep (#774). Stamp before dispatch so the idle timer chases the
	// freshest deadline; a torn-down conn's late frame was dropped by the
	// check above first. A single field write on the hot path — no timer op
	// here; the armed idleTimer reads this on its next fire (handleWake).
	s.lastActivityAt = time.Now()

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

// dispatchAppFrame runs on the Run goroutine. It splits an open-state
// plaintext two ways: a v2 control envelope (rekey/modal/question/interrupt/
// new_session/dequeue/snapshot/debug_bundle/settings) is handled inline on
// Run — those handlers touch s.send / session state / timers and are fast,
// with one exception: handleDebugBundleRequest is fast only because #1491 moved
// its uncapped read+gzip off Run, leaving the accept, the gate and the seals
// here; an application frame is handed OFF Run to this conn's worker goroutine
// (appFrameWorker) via a non-blocking enqueue onto s.appFrames, then
// dispatchAppFrame returns so the Run loop keeps servicing every other arm
// (other conns' frames, m.wake, m.modalTimeout, m.manualRekey) while the
// possibly-slow handler runs (#965). The worker routes the frame
// (routeAppFrame → dispatch.Route → handler → c.Send: a marshal + channel
// push, no AEAD) and posts each reply back to Run via m.appReply, where
// forwardAppReply seals it under s.send — so every s.send.Encrypt stays on
// the single-owner Run goroutine.
//
// The enqueue is non-blocking: on overflow (appFrameQueueDepth app frames
// already in flight for this one conn — far beyond request/response norms)
// the conn is torn down at 4421 rather than blocking Run, which would
// reintroduce the cross-conn head-of-line stall this ticket removes.
// Overflow is self-inflicted per conn: the plaintext was AEAD-decrypted
// under s.recv (in handleNoiseMsg) before reaching here, so only the
// authenticated phone can fill its own queue — no cross-conn or injection
// vector.
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
		case protocol.TypeQuestionAnswer:
			m.handleQuestionAnswer(s, probeEnv)
			return
		case protocol.TypeQuestionRefused:
			m.handleQuestionRefusal(s, probeEnv)
			return
		case protocol.TypeInterrupt:
			m.handleInterrupt(s, probeEnv)
			return
		case protocol.TypeNewSession:
			m.handleNewSession(s, probeEnv)
			return
		case protocol.TypeDequeueMessage:
			m.handleDequeueMessage(s, probeEnv)
			return
		case protocol.TypeRequestDebugBundle:
			m.handleDebugBundleRequest(ctx, s, probeEnv)
			return
		case protocol.TypeSetSessionSettings:
			m.handleSetSessionSettings(ctx, s, probeEnv)
			return
		case protocol.TypeRequestSessionSettings:
			m.handleRequestSessionSettings(ctx, s, probeEnv)
			return
		case protocol.TypeRequestModelList:
			// Inline on Run (#2125), beside its shape twin above rather than with
			// the three hand-off arms below: answering is a registry lookup, a copy
			// of at most ten model rows and one small marshal. None of that is the
			// hashing, disk reading or cap-budgeting that moved those arms off Run,
			// so an appFrameJob kind and a worker route would buy nothing. See
			// handleRequestModelList's file header for what that placement obliges.
			m.handleRequestModelList(ctx, s, probeEnv)
			return
		case protocol.TypeRequestSystemPrompt:
			// Inline on Run (#2152), beside the two shape twins above rather than
			// with the hand-off arms below: answering is a registry lookup, one pool
			// map read and one small marshal. See handleRequestSystemPrompt's file
			// header for what that placement obliges of a future edit.
			m.handleRequestSystemPrompt(ctx, s, probeEnv)
			return
		case protocol.TypeAttachmentChunk:
			// The FIRST control type whose handler does not run inline on Run
			// (#1897), and one of two — the retrieval arm below joined it in
			// #2054. Every other arm above is fast; a completing chunk hashes
			// and writes up to the per-upload byte bound, which is exactly the
			// work #1491 had to move off Run for handleDebugBundleRequest. So
			// this arm only tags the frame and falls through to the same
			// non-blocking enqueue the v1 application path uses — the worker
			// then routes it to handleAttachmentChunk instead of dispatch.Route.
			// The case selector is what TestEveryInboundV2TypeHasHandler reads,
			// and the guard reads selectors rather than bodies, so handing off
			// satisfies it.
			m.enqueueAppFrame(ctx, s, appFrameJob{plaintext: plaintext, kind: appFrameAttachmentChunk})
			return
		case protocol.TypeRequestAttachment:
			// The second arm to run off Run (#2054), and for the same reason as
			// the one above rather than a new one: answering this frame reads a
			// stored file of up to the per-upload byte bound, hashes it and
			// base64-marshals one envelope per chunk. Tags and falls through to
			// the same non-blocking enqueue; the worker routes it to
			// handleRequestAttachment.
			m.enqueueAppFrame(ctx, s, appFrameJob{plaintext: plaintext, kind: appFrameAttachmentRequest})
			return
		case protocol.TypeRequestHistory:
			// The third arm to run off Run (#2116), for the same reason as the two
			// above rather than a new one: answering this frame opens log segments
			// off disk, decodes them and marshals a page of up to the
			// application-envelope cap. Tags and falls through to the same
			// non-blocking enqueue; the worker routes it to handleRequestHistory.
			m.enqueueAppFrame(ctx, s, appFrameJob{plaintext: plaintext, kind: appFrameHistoryRequest})
			return
		}
	}

	m.enqueueAppFrame(ctx, s, appFrameJob{plaintext: plaintext, kind: appFrameRoute})
}

// enqueueAppFrame hands one frame to this conn's worker (off-Run) via a
// non-blocking enqueue. Blocking here would re-couple Run to the
// handler's duration — the exact cross-conn head-of-line stall #965
// removes. s.appFrames is non-nil for any V2StateOpen session (created
// in handleNoiseInit's open tail alongside the worker), and Run reaching
// this line means the session is open (handleNoiseMsg's V2StateOpen
// case).
//
// Extracted from dispatchAppFrame's tail by #1897 so the attachment_chunk arm
// and the v1 application fall-through share ONE enqueue, one overflow policy and
// one close-at-4421 posture rather than growing a second copy that could drift.
// MUST run on the Run goroutine, like the site it was extracted from.
func (m *V2SessionManager) enqueueAppFrame(ctx context.Context, s *V2Session, job appFrameJob) {
	select {
	case s.appFrames <- job:
	default:
		// Overflow: appFrameQueueDepth app frames already in flight for this
		// one conn, far beyond request/response norms. Tear the conn down at
		// 4421 (reusing the existing protocol-mismatch code — a pacing
		// violation is protocol-adjacent) rather than block Run or silently
		// drop a request/response. The WARN reason distinguishes it; the log
		// carries only conn_id + a static count, never plaintext.
		m.cfg.Logger.Warn("relay: v2 app frame queue overflow; closing conn",
			"event", "v2.app_frame.queue_overflow",
			"conn_id", s.connID,
			"close_code", int(StatusProtocolMismatch),
			"depth", appFrameQueueDepth)
		m.closeWith(ctx, s, StatusProtocolMismatch, nil)
	}
}

// appFrameJob is one plaintext queued for a conn's appFrameWorker, carrying the
// routing decision dispatchAppFrame already made (#1897) rather than leaving the
// worker to re-derive it.
//
// The tag exists so the decision lives in ONE place. Re-probing the envelope type
// inside the worker would look equivalent and is not: it puts the same decision in
// two places that can silently disagree, and TestEveryInboundV2TypeHasHandler
// reads dispatchAppFrame's case selectors as THE registry of those decisions — so
// a worker-side copy would let a future edit delete the case while the frame kept
// routing, flipping the guard without changing behaviour.
type appFrameJob struct {
	// plaintext is the decrypted application frame, freshly allocated by
	// noise.CipherState.Decrypt and therefore aliasing nothing.
	plaintext []byte

	// kind is what dispatchAppFrame recognised the frame as, and therefore which
	// handler the worker runs. appFrameRoute — the zero value — is every v1
	// application frame, which routes through dispatch.Route unchanged. Every
	// construction site names its kind rather than leaning on that zero value, so
	// the producer and the worker's arms read as one enumeration.
	kind appFrameKind
}

// appFrameKind names the off-Run handlers, one member per dispatchAppFrame case
// that hands off rather than handling inline.
//
// A TYPED KIND RATHER THAN ONE BOOL PER TYPE (#2054 widened #1897's lone
// `attachment bool`): the members are mutually exclusive by construction, and a
// set of bools can express a state that is not — two set at once, with the
// worker's arm order silently deciding which wins. Widening rather than
// re-probing the envelope type inside the worker is what keeps the routing
// decision in ONE place, which is the property appFrameJob exists for.
type appFrameKind uint8

const (
	// appFrameRoute is the v1 application dispatch chain (dispatch.Route). The
	// zero value, so a job built without a kind routes the way it always did.
	appFrameRoute appFrameKind = iota
	// appFrameAttachmentChunk is the inbound upload leg (#1897).
	appFrameAttachmentChunk
	// appFrameAttachmentRequest is the inbound retrieval request (#2054).
	appFrameAttachmentRequest
	// appFrameHistoryRequest is the inbound conversation-history request (#2116).
	appFrameHistoryRequest
)

// appFrameWorker is the per-conn sub-actor that runs application handlers
// off the Run goroutine (#965). Exactly one is spawned per session in
// handleNoiseInit's open tail; it processes s.appFrames in strict FIFO
// arrival order — one frame fully routed (Route returned, all its replies
// forwarded to Run) before the next is dequeued — so no two handlers for
// the same conn run concurrently and their sealed replies emit in arrival
// order (AC-2). It terminates when s.done is closed (per-session teardown
// in closeWith) or ctx (runCtx) is cancelled (Run exit) — leaving no
// goroutine behind under conn churn or shutdown.
//
// The worker NEVER touches s.send / s.recv / keys / session state: it only
// runs Route → handler → c.Send (a marshal + channel push, no AEAD) and
// posts replies to m.appReply for Run to seal. That is the load-bearing
// single-owner-cipher invariant (AC-3).
func (m *V2SessionManager) appFrameWorker(ctx context.Context, s *V2Session) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.done:
			return
		case job := <-s.appFrames:
			// closeWith may have closed s.done while this frame sat in the
			// buffer (select picks randomly when both are ready). Re-check and
			// abandon queued-but-unstarted frames for a torn-down conn rather
			// than spawn a handler/child for a conn being closed.
			select {
			case <-s.done:
				return
			default:
			}
			switch job.kind {
			case appFrameAttachmentChunk:
				// The upload path (#1897). Runs here rather than in
				// routeAppFrame because it neither builds an outbound channel
				// nor calls dispatch.Route — it emits its own replies straight
				// through forwardToRun. Being on this goroutine is what
				// discharges attachments.Intake.Receive's single-feeder
				// precondition: strictly FIFO, one frame fully handled before
				// the next is dequeued, one worker per conn.
				m.handleAttachmentChunk(ctx, s, job.plaintext)
			case appFrameAttachmentRequest:
				// The retrieval path (#2054), here for the same reason: it
				// reads a stored file and enqueues one envelope per chunk,
				// which must not run on Run. Its chunks leave through Push
				// (safe from any goroutine) and its rejects through
				// forwardToRun, so like the arm above it never touches s.send.
				m.handleRequestAttachment(ctx, s, job.plaintext)
			case appFrameHistoryRequest:
				// The conversation-history path (#2116), here because it reads log
				// segments off disk. UNLIKE the two arms above it has only ONE
				// emission route: a page is a single envelope rather than a stream,
				// so both the page and every reject leave through forwardToRun and
				// nothing on this path ever touches s.send.
				m.handleRequestHistory(ctx, s, job.plaintext)
			case appFrameRoute:
				// The v1 application dispatch chain, unchanged: build the outbound
				// channel, call dispatch.Route, forward its replies to Run.
				m.routeAppFrame(ctx, s, job.plaintext)
			default:
				// Unreachable — every appFrameKind has an arm above, and Go cannot
				// check that for us. A kind added without one lands here and takes
				// the v1 chain, which answers an unrecognised type with
				// protocol.unsupported; the alternative to keeping this arm is
				// dropping such a frame in silence.
				m.routeAppFrame(ctx, s, job.plaintext)
			}
		}
	}
}

// routeAppFrame runs dispatch.Route for one application frame on the
// worker goroutine and forwards each reply the handler emits back to Run
// (via m.appReply) for sealing. Preserves #909's concurrent-drain shape:
// Route runs on its own short-lived goroutine while this goroutine drains
// the per-frame outbound channel, so a handler emitting more than
// handlerOutboundBuf replies cannot fill outbound and deadlock Route inside
// c.Send. A single sender (the handler) + this single receiver preserves
// FIFO emission order, and forwardToRun's blocking send onto the FIFO
// m.appReply channel preserves it across the seam.
//
// Only Route (→ handler → c.Send: a marshal + channel push, no AEAD) runs
// here; the seal (forwardAppReply → s.send.Encrypt) happens on Run. The
// outbound channel is deliberately NOT closed — a misbehaving handler that
// forks a sender after Route returns writes into a leaked but
// capacity-bounded channel the GC reclaims once the goroutine exits;
// closing here would panic such a sender (#446).
//
// ctx.Done() is not an arm of the Route-drain select for the same reason
// as #909: the handler observes cancellation through c.Send's own ctx arm
// (ctx is runCtx), so a shutdown drives Route to return and fires routeDone
// naturally. forwardToRun DOES honor ctx / s.done so a worker parked on a
// reply send unblocks on teardown.
func (m *V2SessionManager) routeAppFrame(ctx context.Context, s *V2Session, plaintext []byte) {
	outbound := make(chan protocol.RoutingEnvelope, handlerOutboundBuf)
	conn := dispatch.NewConn(s.connID, outbound, s.device)

	routeDone := make(chan struct{})
	go func() {
		defer close(routeDone)
		dispatch.Route(ctx, m.cfg.Logger, conn, m.cfg.Handlers, plaintext)
	}()
	for {
		select {
		case reply := <-outbound:
			if !m.forwardToRun(ctx, s, reply) {
				return
			}
		case <-routeDone:
			// Route returned (no further c.Send in flight); drain any residual
			// buffered replies in FIFO order, then return.
			for {
				select {
				case reply := <-outbound:
					if !m.forwardToRun(ctx, s, reply) {
						return
					}
				default:
					return
				}
			}
		}
	}
}

// forwardToRun blocks until reply is handed to Run's m.appReply arm (which
// seals it under s.send), or the session/manager tears down. Returns true
// on a successful hand-off, false if ctx (runCtx, Run exiting) or s.done
// (this conn's closeWith) fired first — in which case routeAppFrame
// abandons the remaining drain (the Route goroutine still unwinds via
// c.Send's own ctx arm, and any reply already past this point is dropped by
// forwardAppReply's V2StateOpen gate). The blocking send is what applies
// backpressure onto the worker so replies never accumulate unbounded.
func (m *V2SessionManager) forwardToRun(ctx context.Context, s *V2Session, reply protocol.RoutingEnvelope) bool {
	select {
	case m.appReply <- appReplyMsg{s: s, reply: reply}:
		return true
	case <-s.done:
		return false
	case <-ctx.Done():
		return false
	}
}

// forwardAppReply seals one handler reply under s.send and forwards it as
// a noise_msg via m.send. MUST run only on the manager's Run goroutine —
// s.send is the single-owner Noise send CipherState, and a concurrent
// Encrypt would reuse a nonce / corrupt the send counter on the encrypted
// wire. Reached from Run's m.appReply arm, where a per-conn worker posts
// the not-yet-sealed reply. Drops the reply (WARN, no wire emission) on the
// realistically-unreachable seal/marshal error, exactly as #446: never
// emit an unsealed frame. Also drops it unsealed when transportDown reports
// the relay leg down (#1525, see the branch comment).
func (m *V2SessionManager) forwardAppReply(s *V2Session, reply protocol.RoutingEnvelope) {
	if s.state != V2StateOpen {
		// The worker was mid-handler when closeWith tore this session down
		// (#965). Drop the reply — sealing under a dead session would burn a
		// send-nonce for a frame no live peer awaits. Reading s.state is safe:
		// forwardAppReply runs on Run, the sole writer of s.state. Mirrors
		// forwardEnvelope's V2StateOpen gate.
		m.cfg.Logger.Debug("relay: v2 app reply dropped; session not open",
			"conn_id", s.connID)
		return
	}
	if m.transportDown() {
		// Transport-down DROP (#1525), the same shape drainOnce holds the push
		// head with and handleWake defers the rekey emit with. Seal nothing:
		// m.send swallows its Outbound error at Debug, so reacting to that error
		// is structurally too late — the send-nonce is already spent. And a
		// burned nonce is not a lost message: relay↔binary carries no per-conn
		// disconnect frame, so the phone leaves its recv CipherState untouched,
		// the next delivered frame fails AEAD, and the session dies at
		// StatusProtocolMismatch without self-healing. #965 is why this seal
		// needs the guard that #874 judged unnecessary: the reply now returns to
		// Run up to a whole handler duration after its inbound frame, wide
		// enough to hold an entire blip.
		//
		// Dropped, not parked. The reply is undeliverable either way today —
		// m.send already discards it — so this only stops paying a nonce for
		// that non-delivery. Post-recovery delivery would need a buffer, an
		// eviction policy and an ordering rule against the push queue; out of
		// scope. Nothing is held, so the down→up edge needs no flush.
		//
		// Debug and content-free (event slug + conn-id, no payload, plaintext,
		// ciphertext or key bytes): transport-down is expected during reconnect,
		// and a blip on a chatty conn emits one line per reply.
		//
		// The probe is a plain read of a construction-time func on the Run
		// goroutine — no lock, no atomic. The single-frame TOCTOU at the up→down
		// instant carries over from #874 and #912, documented on the Connected
		// seam itself.
		m.cfg.Logger.Debug("relay: v2 app reply dropped; transport down",
			"event", "v2.app_reply.dropped_transport_down",
			"conn_id", s.connID)
		return
	}
	ciphertext, err := s.send.Encrypt(reply.Frame)
	if err != nil {
		// Realistically unreachable under correct flynn/noise. Drop the
		// reply rather than emit the unencrypted frame.
		m.cfg.Logger.Warn("relay: v2 seal app reply failed; reply dropped",
			"conn_id", s.connID)
		return
	}
	frame, err := marshalInnerFrameV2(protocol.TypeNoiseMsg, ciphertext)
	if err != nil {
		m.cfg.Logger.Warn("relay: v2 marshal app reply failed; reply dropped",
			"conn_id", s.connID)
		return
	}
	// The reply's CloseCode is ignored: handlers do not signal closes
	// through c.Send/c.Reply; the close-code field on the routing envelope
	// is reserved for the manager's own close-intent emissions (closeWith).
	m.send(protocol.RoutingEnvelope{ConnID: s.connID, Frame: frame})
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
	if s.idleTimer != nil {
		s.idleTimer.Stop()
		s.idleTimer = nil
	}
	// Stop this conn's app-frame worker (#965), symmetric with the m.queues
	// delete below: closing s.done makes the worker return (and abandon any
	// queued-but-unstarted frames) without waiting for Run exit. The
	// V2StateClosed guard at the top makes this close-once; s.done is left
	// closed-but-non-nil (never reassigned) so the worker's field read stays
	// race-free. nil for a session torn down before it ever reached open.
	if s.done != nil {
		close(s.done)
	}
	// Release this conn's in-flight uploads (#1897), in the same per-conn cleanup
	// cluster as the worker stop above and the m.queues delete below. Without it a
	// phone that drops mid-upload keeps holding daemon-wide upload capacity until
	// uploadIdleTimeout expires, so a client that opens and drops conns can hold
	// the whole budget. Called unconditionally for any conn (it is a no-op for one
	// holding nothing), including a session torn down before it ever reached open.
	//
	// Safe HERE, on Run, while this conn's worker may still be inside Receive:
	// ReleaseConn is the documented exception to that method's single-feeder
	// precondition — remove-only, under the registry's own mutex, touching no
	// accumulator. A teardown landing between Deliver and Store still files those
	// bytes, which internal/attachments documents as accepted rather than
	// mitigated.
	if m.cfg.AttachmentIntake != nil {
		m.cfg.AttachmentIntake.ReleaseConn(s.connID)
	}
	// Drop any in-flight reconnect-replay tail (#777). The session is deleted
	// from m.sessions just below, so drainReplayOnce's scan can no longer find
	// it; niling matches the timer-cleanup pattern and releases the slice for GC.
	s.replayQueue = nil
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
		if _, gated := replayPending[id]; gated {
			continue // replay in flight for this conn; hold its live events (#777).
		}
		connID = id
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
