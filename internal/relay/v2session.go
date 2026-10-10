package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/devices"
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

	// StatusSessionGone is the WS close code the binary asks the relay to
	// apply when a noise_msg arrives on a conn the manager holds no session
	// for (#2488). handleFrame creates a bare V2StateAwaitingInit session for
	// any unheld conn id, so the frame reaches handleNoiseMsg with no
	// CipherStates: it can be neither decrypted nor answered with a sealed
	// error, and the close code is the only signal. Echoes HTTP 410 (Gone),
	// consistent with the 44xx←HTTP convention. Wire spec:
	// docs/protocol-mobile.md § Error codes, close-code row 4410.
	//
	// Deliberately NOT StatusProtocolMismatch, which this arm used before:
	// clients treat 4421 as a permanent mismatch and stop re-dialling, yet a
	// legitimate client lands here whenever it still holds cipher state for a
	// session the daemon dropped. Because closeWith deletes the conn from the
	// session map, all three causes — a daemon restart, an idle sweep (4408)
	// and the push-queue ceiling (4413) — reduce to a conn id the manager no
	// longer holds, and the documented recovery for each is a fresh Noise
	// handshake, which only a retryable close lets the client reach.
	//
	// Deliberately NOT StatusIdleTimeout either: 4408 means "your session
	// idled out", and a restart or an overflow is not that — the same mislabel
	// StatusQueueOverflow's comment already records rejecting.
	StatusSessionGone websocket.StatusCode = 4410

	// StatusClientUpdateRequired is the WS close code the binary asks the
	// relay to apply when it refuses an app build older than the host's
	// minimum for that app (#2576). It follows the sealed
	// protocol.CodeClientUpdateRequired error in the same routing envelope,
	// after the Noise handshake and the token check, so the client reads the
	// error before the close — the token-failure arm's shape. Echoes HTTP 412
	// (Precondition Failed), consistent with the 44xx←HTTP convention: the
	// minimum app version is a precondition the hello failed. 426 would be
	// the literal match but 4426 is StatusHandshakeFailure. Wire spec:
	// docs/protocol-mobile.md § Error codes, close-code row 4412.
	//
	// Terminal for THIS host only: a client stops re-dialling this daemon and
	// keeps its other hosts. Sent only while a minimum is set (#2578); see
	// MinMobileClientVersion.
	StatusClientUpdateRequired websocket.StatusCode = 4412
)

// The minimum app version this daemon release accepts, per app, as
// MAJOR.MINOR.PATCH (#2578). Build constants, not operator configuration: each
// daemon release carries its own, so during a staged rollout different hosts
// may enforce different minimums. Empty means no minimum for that app, and
// while both are empty no hello is ever refused for its version.
//
// Set the first one only after that app has shipped a release sending
// client_version as <app>/<MAJOR>.<MINOR>.<PATCH>: once any minimum is set,
// every build sending the older free text is refused (docs/protocol-mobile.md
// § Compatibility). A value that does not parse fails NewV2SessionManager and
// TestNewV2SessionManager_MinClientVersions, never reading as "no minimum".
const (
	MinMobileClientVersion  = ""
	MinDesktopClientVersion = ""
)

// MsgClientUpdateRequired is the static message of the
// protocol.CodeClientUpdateRequired error. The minimum travels in
// ErrorPayload.MinClientVersion, never in the message.
const MsgClientUpdateRequired = "this app version is no longer supported by this host; update the app"

// ShippedMinClientVersions returns this build's minimums keyed by app name, the
// shape V2SessionConfig.MinClientVersions takes.
func ShippedMinClientVersions() map[string]string {
	return map[string]string{
		protocol.AppMobile:  MinMobileClientVersion,
		protocol.AppDesktop: MinDesktopClientVersion,
	}
}

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
// the public API and not yet config-driven (a deferred concern, the same
// posture as rekeyInterval).
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

	// multiAgent is the negotiated protocol.CapabilityMultiAgent decision
	// (#2643), under interactive's regime exactly: set once in handleNoiseInit's
	// token-OK path before V2StateOpen, false everywhere else, preserved across a
	// re-key by never being touched. Surfaced into every per-frame
	// *dispatch.Conn by routeAppFrame, so a handler reads it as c.MultiAgent().
	multiAgent bool
	// thread is the authenticated admission decision, retained through rekey.
	thread bool

	// clientName, clientVersion and clientFeatures are the device_name,
	// client_version and client_features the phone reported for ITSELF in its hello,
	// retained so the appended prompt can attribute that report to the client. Set
	// exactly once in handleNoiseInit's token-OK path BEFORE V2StateOpen,
	// so an unauthenticated peer's strings are never enumerable; "" is the
	// fail-closed default for every other path. Re-key preserves them by never
	// touching them, like device/peerStatic/interactive. Read by handleActiveConns
	// on the same dispatch goroutine — no lock/atomic.
	//
	// RETAINED VERBATIM AND JUDGED BY NOBODY HERE. This package does not decide
	// what may appear in a prompt; internal/sessions' admitClient is the single
	// door, and duplicating its character-set rule here would put the same
	// decision in two places that can disagree. What IS enforced below is a
	// resource bound, which is a different concern and belongs at the point of
	// retention — see maxRetainedClientNameBytes.
	clientName     string
	clientVersion  string
	clientFeatures string

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

	// replySuggestionRevisions is the highest reply_suggestion revision already
	// delivered to this conn, per conversation_id (#2830). Run-owned, the
	// replayThrough regime: read and written only in forwardEnvelope. nil until the
	// first delivery, and a fresh V2Session per handshake, so a reconnect is never
	// held back by revisions an earlier conn saw. Bounded by the conversations this
	// conn received a suggestion for, and freed with the session.
	replySuggestionRevisions map[string]uint64

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

	// minClientVersions is cfg.MinClientVersions parsed once by
	// NewV2SessionManager, keyed by app name, holding only the apps with a
	// minimum; nil when none is set. Never mutated afterwards, so every
	// handshake compares against one snapshot.
	minClientVersions map[string]protocol.Version

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
	// (#1505). Mirrors wake's shape — buffered (wakeBufferSize), carrying the
	// affected conn — because enqueue runs off-Run under pushMu and closeWith is
	// Run-owned, exactly the split wake bridges for its timer callbacks.
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
	// Buffered (wakeBufferSize) like wake / pushOverflow; it is
	// only pressured by more conns finishing assembly at once than the buffer
	// holds while Run is busy, and assembleBundle's send BLOCKS rather than drops,
	// so that case is correct rather than lossy. Not closed by the manager on Run
	// exit; an in-flight producer unblocks via runCtx / s.done.
	bundleReady chan bundleResult

	// newSessionDone carries one deferred new_session outcome from a
	// LateSessionStarter's own goroutine back to Run, which answers it in
	// handleNewSessionDone (#2477). The same "off-Run producer, Run
	// consumes-and-seals" shape as bundleReady and appReply, for the same reason:
	// the seam's rotation runs wherever it likes, but the reply it may owe —
	// answerNewSession → newSessionReplyWorkspaceRefused → forwardEnvelope — reads
	// m.sessions and calls s.send.Encrypt, both single-owned by Run.
	//
	// Buffered (wakeBufferSize) like wake / pushOverflow / bundleReady, and the
	// producer's send BLOCKS with escapes rather than dropping (the assembleBundle
	// hand-off), so a Run that is briefly busy delays a reply instead of losing it.
	// Not closed on Run exit; an in-flight producer unblocks via its escapes.
	newSessionDone chan newSessionResult

	// switchAgentDone returns worker outcomes to the sole reply/crypto owner.
	// Producers escape via requester teardown or Run cancellation. Never closed.
	switchAgentDone chan switchAgentResult

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
	// A malformed minimum fails here rather than reading as "no minimum", which
	// would fail open. The app key and the version go through the same grammar
	// a hello's client_version does.
	var minClientVersions map[string]protocol.Version
	for app, v := range cfg.MinClientVersions {
		if v == "" {
			continue
		}
		parsedApp, parsed, ok := protocol.ParseClientVersion(app + "/" + v)
		if !ok || parsedApp != app {
			return nil, fmt.Errorf("relay: V2SessionManager MinClientVersions[%q] = %q is not <app> and MAJOR.MINOR.PATCH", app, v)
		}
		if minClientVersions == nil {
			minClientVersions = make(map[string]protocol.Version)
		}
		minClientVersions[app] = parsed
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
		pushOverflow: make(chan string, wakeBufferSize),
		bundleReady:  make(chan bundleResult, wakeBufferSize),

		newSessionDone:  make(chan newSessionResult, wakeBufferSize),
		switchAgentDone: make(chan switchAgentResult, wakeBufferSize),

		minClientVersions: minClientVersions,
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
		case connID := <-m.pushOverflow:
			m.handlePushOverflow(runCtx, connID)
		case res := <-m.bundleReady:
			// A debug bundle finished assembling off Run (#1491). Stream it —
			// or reply with the deterministic error — here, so every
			// s.send.Encrypt stays on the single-owner Run goroutine. The
			// assembly goroutine performed no crypto and touched nothing Run
			// owns.
			m.handleBundleReady(runCtx, res)
		case res := <-m.newSessionDone:
			// A LateSessionStarter finished a deferred rotation off Run (#2477).
			// Answer it here — the reply it may owe seals through forwardEnvelope,
			// so it belongs on the single-owner goroutine like every other v2
			// reply. The seam's goroutine performed no crypto and touched nothing
			// Run owns.
			m.handleNewSessionDone(runCtx, res)
		case res := <-m.switchAgentDone:
			m.handleSwitchAgentDone(runCtx, res)
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
			// exactly as pre-#875. Re-signal replayCh too, so a replay tail
			// held by drainReplayOnce's transport-down hold resumes (#1490);
			// drainOnce keeps that conn's live events gated behind it.
			select {
			case m.drainCh <- struct{}{}:
			default:
			}
			select {
			case m.replayCh <- struct{}{}:
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
	if env.CloseCode != 0 {
		// The relay's close notice (#2601): that phone's WebSocket has ended.
		// Handled ahead of the lazy create and the activity stamp so a notice
		// for an unknown conn creates nothing, and Frame is never decoded.
		m.handlePeerClose(env)
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

// dispatchAppFrame runs on the Run goroutine. Fast v2 control handlers stay on
// Run; controls that can hash, read, write, or wait are tagged here and handed to
// this conn's appFrameWorker through the bounded s.appFrames queue. Unrecognised
// application frames use the same worker to reach dispatch.Route. In every case
// dispatchAppFrame returns promptly so Run keeps servicing other connections and
// wake channels while possibly-slow work runs off-Run. The worker routes the frame
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
		case protocol.TypeStopBackgroundTask:
			// Both inert gates stay on Run, before payload decode or enqueue. The
			// paired-device stop action has no tool-permission gate or v1 fallback.
			if m.cfg.BackgroundTaskStopper == nil || !s.interactive {
				return
			}
			m.enqueueAppFrame(ctx, s, appFrameJob{plaintext: plaintext, kind: appFrameStopBackgroundTask})
			return
		case protocol.TypeSwitchAgent:
			m.handleSwitchAgent(ctx, s, probeEnv)
			return
		case protocol.TypeNewSession:
			m.handleNewSession(ctx, s, probeEnv)
			return
		case protocol.TypeDequeueMessage:
			m.handleDequeueMessage(s, probeEnv)
			return
		case protocol.TypeSendQueuedNow:
			// The write is a plain stdin pipe write with no deadline, so it runs on
			// this conn's worker rather than here: a child not reading its stdin
			// stalls one conn's frames, never the Run loop. The capability gate stays
			// on Run, so a non-interactive peer never queues work (#2729).
			if !s.interactive {
				return
			}
			m.enqueueAppFrame(ctx, s, appFrameJob{plaintext: plaintext, kind: appFrameSendQueuedNow})
			return
		case protocol.TypeRequestDebugBundle:
			m.handleDebugBundleRequest(ctx, s, probeEnv)
			return
		case protocol.TypeSetSessionSettings:
			m.handleSetSessionSettings(ctx, s, probeEnv)
			return
		case protocol.TypeRequestSessionSettings:
			// EffectiveEffortFor may wait on a child round trip, so an accepted
			// read runs on this conn's worker. The capability gate stays here on
			// Run: a non-interactive peer never queues work, decodes the payload,
			// or consults either settings dependency. A nil effective-effort
			// provider is NOT an inert gate — the existing RunConfig reply is
			// still owed, with only the optional field omitted.
			if !s.interactive {
				return
			}
			// multiAgent is Run-owned, so it travels in the job rather than being
			// read on the worker (#2646).
			m.enqueueAppFrame(ctx, s, appFrameJob{plaintext: plaintext, kind: appFrameSessionSettingsRequest, multiAgent: s.multiAgent})
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
		case protocol.TypeMCPStatusRequest:
			// A live resolver may wait on a child round trip, so accepted requests
			// run on this conn's worker. Both inert gates stay here on Run: neither
			// posture decodes the payload or consults membership.
			if m.cfg.MCPStatusFor == nil || !s.interactive {
				return
			}
			m.enqueueAppFrame(ctx, s, appFrameJob{plaintext: plaintext, kind: appFrameMCPStatusRequest})
			return
		case protocol.TypeRequestContextUsage:
			// The read-and-wait arm's twin (#2431), on the worker for the same reason
			// and then some: a live resolver defers a mid-turn request until the turn
			// ends AND waits on a child round trip. Both inert gates stay here on Run:
			// neither posture decodes the payload or consults membership, and holding
			// them here is what keeps a frame that fails either off the queue entirely.
			if m.cfg.ContextUsageFor == nil || !s.interactive {
				return
			}
			m.enqueueAppFrame(ctx, s, appFrameJob{plaintext: plaintext, kind: appFrameContextUsageRequest})
			return
		case protocol.TypeMCPReconnect:
			// The WRITE half of the MCP pair (#2419), on the worker for the same
			// reason as the read above — the actuator waits on a child round trip —
			// and with the same two inert gates held here on Run, neither of which
			// decodes the payload or consults membership. The nil gate is doing more
			// work here than on any read arm: it is what keeps an ungated actuation
			// unreachable until #2420 wires MCPActuator.
			if m.cfg.MCPActuator == nil || !s.interactive {
				return
			}
			m.enqueueAppFrame(ctx, s, appFrameJob{plaintext: plaintext, kind: appFrameMCPReconnect})
			return
		case protocol.TypeMCPToggle:
			// The arm above's twin, kept a separate case rather than folded into it
			// with a shared body: the guard reads THESE selectors as the registry of
			// dispatch decisions, so one case per inbound type is the property being
			// asserted. The two payloads differ and decode in their own handlers.
			if m.cfg.MCPActuator == nil || !s.interactive {
				return
			}
			m.enqueueAppFrame(ctx, s, appFrameJob{plaintext: plaintext, kind: appFrameMCPToggle})
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
		case protocol.TypeReadWorkspaceFile:
			// Off Run for the retrieval arm's reason (#2598): answering reads a
			// file of up to the size bound off disk, hashes it and marshals one
			// envelope per chunk. The worker routes it to handleReadWorkspaceFile.
			m.enqueueAppFrame(ctx, s, appFrameJob{plaintext: plaintext, kind: appFrameWorkspaceFileRead})
			return
		case protocol.TypeRequestHistory:
			// The third arm to run off Run (#2116), for the same reason as the two
			// above rather than a new one: answering this frame opens log segments
			// off disk, decodes them and marshals a page of up to the
			// application-envelope cap. Tags and falls through to the same
			// non-blocking enqueue; the worker routes it to handleRequestHistory.
			m.enqueueAppFrame(ctx, s, appFrameJob{plaintext: plaintext, kind: appFrameHistoryRequest})
			return
		case protocol.TypeMintPairing:
			// The fourth arm to run off Run (#2127), and the first WRITE verb among
			// them. Answering this frame takes the devices.json flock(2), reads the
			// registry, appends a record and rewrites the file — cross-process
			// blocking I/O, which is precisely what must not happen on the goroutine
			// that owns the send CipherState. Tags and falls through to the same
			// non-blocking enqueue; the worker routes it to handleMintPairing.
			m.enqueueAppFrame(ctx, s, appFrameJob{plaintext: plaintext, kind: appFrameMintPairing})
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

// handlePeerClose tears down the session for a phone the relay reports gone
// (#2601): a relay→binary routing envelope with a non-zero CloseCode. Unlike
// closeWith it publishes nothing — the phone's WebSocket has already ended, so
// there is nothing for the relay to close. A notice for a conn with no session
// is dropped. A relay that never sends the notice leaves the idle sweep as the
// only reaper, exactly as before.
func (m *V2SessionManager) handlePeerClose(env protocol.RoutingEnvelope) {
	s, ok := m.sessions[env.ConnID]
	if !ok {
		m.cfg.Logger.Debug("relay: v2 peer close for unknown conn",
			"event", "v2.peer_close.unknown",
			"conn_id", env.ConnID,
			"close_code", int(env.CloseCode))
		return
	}
	m.cfg.Logger.Info("relay: v2 peer close teardown",
		"event", "v2.peer_close.teardown",
		"conn_id", env.ConnID,
		"close_code", int(env.CloseCode))
	m.teardown(s)
}

// closeWith tears s down (teardown) and emits a single routing envelope
// carrying Frame (when non-nil) and CloseCode. The atomic Frame+CloseCode is
// what guarantees the spec's ordering MUST: phone observes the error frame
// before the WS close (spec § Error handling, line 436). Honors ctx by
// checking before the send; the Outbound call itself is synchronous.
func (m *V2SessionManager) closeWith(ctx context.Context, s *V2Session, code websocket.StatusCode, frame json.RawMessage) {
	if !m.teardown(s) {
		return
	}
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

// teardown transitions s to V2StateClosed and releases everything the session
// holds — timers, app-frame worker, uploads, replay tail — then deletes it and
// its push queue from the manager. It publishes nothing; closeWith adds the
// close envelope. Returns false, doing nothing, when s is already closed.
func (m *V2SessionManager) teardown(s *V2Session) bool {
	if s.state == V2StateClosed {
		return false
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
	// phone). closeWith's close envelope is sent synchronously after this,
	// bypassing the buffer (it is terminal, not part of the ordered push stream).
	m.pushMu.Lock()
	if q := m.queues[s.connID]; q != nil {
		for _, item := range q.items {
			if item.barrier != nil {
				close(item.barrier)
			}
		}
	}
	delete(m.queues, s.connID)
	m.pushMu.Unlock()
	return true
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
