# Surface

```go
package relay

// WS close codes (the wire-spec values in docs/protocol-mobile.md § Error codes).
// 4401 (StatusUnauthorized) lives in auth.go and is reused unchanged.
const (
    StatusIdleTimeout      websocket.StatusCode = 4408 // idle-session teardown by the in-repo sweep (#774); echoes HTTP 408
    StatusProtocolMismatch websocket.StatusCode = 4421 // state-machine / discriminator violation
    StatusHandshakeFailure websocket.StatusCode = 4426 // Noise_IK failure before CipherStates exist
    StatusQueueOverflow    websocket.StatusCode = 4413 // push-queue byte ceiling exceeded (#1505); echoes HTTP 413
    StatusSessionGone      websocket.StatusCode = 4410 // noise_msg on a conn id the manager holds no session for (#2488); retryable, echoes HTTP 410
)

type V2SessionState int

const (
    V2StateAwaitingInit V2SessionState = iota
    V2StateHandshakeComplete
    V2StateOpen
    V2StateClosed
)

type V2Session struct { /* unexported fields: connID, state, resp, send, recv, device, peerStatic, interactive (#626) */ }

func (s *V2Session) State() V2SessionState

// Interrupter delivers a single Esc to a conversation's supervised claude — the
// remote equivalent of a local Esc, claude's own interrupt (#707; widened with an
// optional conversationID #2103). Since #1121 production wires cmd/pyry's
// activeInterrupter, not *supervisor.Supervisor directly, which resolves the
// named (or, if empty, the active) conversation's bound runner; declared in the
// consumer so internal/relay imports neither
// internal/supervisor, internal/streamsup, internal/conversations, nor
// internal/sessions. Named for its relay-domain role even though the method
// keeps the sealed surface's name. An empty conversationID is not an error — it
// is the pre-#2103 cursor path — and the string is untrusted: an implementation
// must shape-check before any use and must not let it become a path component.
type Interrupter interface{ SendEsc(conversationID string) error }

// SessionStarter drives a conversation's supervised claude through a fresh
// session — the remote equivalent of a local /clear (#831; widened with an
// optional conversationID #2099). Since #1125 production wires cmd/pyry's
// activeSessionStarter, not *supervisor.Supervisor directly, which resolves the
// named (or, if empty, the active) conversation's bound runner; declared in the
// consumer (beside Interrupter) so internal/relay imports neither
// internal/supervisor, internal/streamsup, internal/conversations, nor
// internal/sessions. Named for its relay-domain role even though the method
// keeps the sealed surface's name. Same untrusted-string contract as Interrupter.
type SessionStarter interface{ StartNewSession(conversationID string) error }

// QueueRemover drops a not-yet-drained queued message from a conversation's
// inbound backlog by id (#723). *msgqueue.Queue satisfies it via the existing
// Remove (#719), so msgqueue needs no new method; declared in the consumer (beside
// Interrupter) so internal/relay imports neither internal/msgqueue nor cmd/pyry.
// Returns true iff a message was removed; an unknown/foreign conversationID, an
// unknown or already-delivered id, or the in-flight (draining) head is a safe
// no-op (false). The conversationID arg IS the mutation scope: Remove(A,…) provably
// never touches conversation B's backlog (the security boundary).
type QueueRemover interface {
    Remove(conversationID string, queuedMsgID uint64) bool
}

// ModalDismissal is the wire outcome+source the manager broadcasts after a
// resolver consumes an outstanding modal (#727). modal_id is held by the manager
// already, so it is not repeated here.
type ModalDismissal struct {
    Outcome string // e.g. "cancelled" (cancel); #717 uses the answered option_id
    Source  string // closed set {remote, local, timeout}; cancel ⇒ "remote"
}

// ModalResolver resolves an inbound modal control frame against the daemon's
// outstanding-modal state (#727, extended #725). Declared in the consumer
// so internal/relay imports neither
// internal/supervisor, internal/modalbridge, internal/audit, nor cmd/pyry; the
// cmd/pyry resolver satisfies it. *devices.Device crosses the seam (the per-conn
// s.device) on the cancel/answer arms. Both methods run on the manager's
// single Run dispatch goroutine.
//
// #1539 deleted the third method, ResolveTimeout, along with the relay-side
// modalDenyTimeout machinery that called it — neither had a production caller
// after the #1348 stream cutover. The permission bridge's own timer (#1103,
// time.AfterFunc → expire) is the only deny-on-timeout left.
type ModalResolver interface {
    // ResolveCancel consumes modalID (registry Resolve), routes a cancel/ESC
    // keystroke, audits outcome=cancelled, and returns the dismissal to
    // broadcast with ok=true. An unknown/already-resolved id ⇒ (zero, false):
    // no keystroke, no audit, no dismissal.
    ResolveCancel(modalID string, dev *devices.Device) (ModalDismissal, bool)
    // ResolveAnswer resolves an inbound modal_answer. #727 introduced it as a
    // deferred no-op; #717 fills the gated answer arm — gate-before-consume,
    // returns the dismissal (Outcome=answered option_id) on ok=true. The manager
    // already broadcasts on ok=true, so #717 touched no manager code.
    ResolveAnswer(modalID, optionID, answerToken string, dev *devices.Device) (ModalDismissal, bool)
}

type V2SessionConfig struct {
    Frames     <-chan protocol.RoutingEnvelope         // required; closes ⇒ Run returns nil
    Outbound   func(protocol.RoutingEnvelope) error    // required; production passes (*Connection).Send
    Connected  func() bool                             // optional (#874); nil ⇒ push drain never holds. Production passes (*Connection).Connected — the pre-seal transport-down probe.
    Reconnect  <-chan struct{}                         // optional (#875); nil ⇒ no new wake source. Production passes (*Connection).Reconnected() — eager reconnect-edge trigger that re-signals drainCh without waiting for a Push.
    StaticPriv []byte                                  // required; must be noise.KeyLen (32) bytes
    Devices    *devices.Registry                       // required; token-validation predicate
    DevicesPath string                                 // optional (#782); reloaded into Devices before each handshake's Validate; "" disables the reload
    ServerID   string                                  // required; surfaced into hello_ack
    Logger     *slog.Logger                            // required (panic if nil)
    Handlers   map[string]dispatch.Handler             // optional; open-state envelope-type → handler
    KnownConversation func(conversationID string) bool // optional (#618); nil ⇒ request_snapshot → conversation.not_found. Also consulted by handleMCPStatusRequest.
    RunConfigFor      func(conversationID string) (RunConfig, bool) // optional (#1609); nil ⇒ request_session_settings answers the zero reply. Conversation-keyed replacement for BootstrapSessionID/SnapshotSettings/SnapshotUsage (deleted from this struct, #1610) — comma-ok, false means not addressable and every RunConfig field is zero. Consulted by request_session_settings (#1610).
    ModalResolver     ModalResolver                    // optional (#727/#725); nil ⇒ modal_answer/modal_cancel inert no-ops. Deny-on-timeout is not gated by this seam — the permission bridge's own timer (#1103) fires unconditionally.
    OutstandingModals func() []protocol.ModalShownPayload // optional (#877); nil ⇒ no connect-time modal reconcile — byte-identical to pre-#877. Production wires modalbridge.Registry.Snapshot.
    OutstandingQueues func() []protocol.QueueStatePayload // optional (#878); nil ⇒ no connect-time queue reconcile — byte-identical to pre-#878. Production wires the cmd/pyry outstandingQueues adapter over *msgqueue.Queue.SnapshotAll.
    RetainedModelLists func() []protocol.ModelListPayload // optional (#1863); nil ⇒ no connect-time model-list reconcile — byte-identical to pre-#1863. Production wires the cmd/pyry retainedModelLists adapter (#1867) over resolveBoundModelList (#1857).
    Interrupter       Interrupter                      // optional (#707); nil ⇒ interrupt is inert (no Esc) — foreground/unwired. Production wires cmd/pyry's activeInterrupter (since #1121).
    SessionStarter    SessionStarter                   // optional (#831); nil ⇒ new_session is inert (no /clear) — foreground/unwired. Production wires cmd/pyry's activeSessionStarter (since #1125).
    QueueRemover      QueueRemover                     // optional (#723); nil ⇒ dequeue_message is inert (no Remove) — foreground/unwired. Production wires the concrete *msgqueue.Queue.
    AttachmentResolve func(conversationID, attachmentID string) (path string, ok bool) // optional (#2054); nil ⇒ request_attachment is inert (consumed, no decode, no reply) — foreground/unwired. Comma-ok, not an error: one false covers an unknown id, a non-canonical id and an id resolving outside the conversation's directory alike, so the seam cannot leak which sub-case fired. Discharges NO registry check — handleRequestAttachment validates conversation membership via KnownConversation before either id reaches this seam. The only sanctioned implementation wraps attachments.ResolvePath; a path from anywhere else has no containment guarantee and, for a non-regular file, wedges the conn's appFrameWorker (os.ReadFile blocks forever on a FIFO). Production wires cmd/pyry's existing attachmentResolve closure (already built for handlers.SendMessage).
}

type V2SessionManager struct { /* unexported */ }

func NewV2SessionManager(cfg V2SessionConfig) (*V2SessionManager, error)
func (m *V2SessionManager) Run(ctx context.Context) error

// Satisfies control.Rekeyer. Operator-driven manual re-key trigger (#462).
func (m *V2SessionManager) Rekey(ctx context.Context, connID string) error

// Concurrency-safe server-initiated push (#571; non-blocking under backpressure
// #610). Enqueues a caller-owned envelope onto the addressed session's bounded
// per-session buffer and returns immediately — NEVER blocks on the relay/send
// path. Run drains the buffer (drainOnce), sealing each envelope under s.send
// in order, so a slow/stalled relay can never wedge the calling producer. Under
// pressure the event-class drop policy runs pre-seal: assistant_delta drops
// oldest; control events never drop. Before sealing, drainOnce also consults
// the optional Connected probe (#874): a down transport holds the queue head
// un-popped and unsealed instead of sealing-and-dropping it, so no Noise
// send-nonce is burned for a frame that cannot reach the phone. Returns
// ErrConnNotFound when no open session has connID (a queue exists iff
// V2StateOpen); ctx.Err() only if ctx is already cancelled at entry. A drop is
// not an error (returns nil + debug-logs).
func (m *V2SessionManager) Push(ctx context.Context, connID string, env protocol.Envelope) error

// Streams an arbitrarily-large blob to one open, authenticated conn as ordered,
// cap-respecting debug_bundle_chunk frames + a debug_bundle_done marker (#812,
// security-sensitive). Builds the envelopes with bundleEnvelopes, then loops
// Push in order — the manager's async send path, NEVER the per-frame
// handler-reply channel (handlerOutboundBuf = 8; concurrently drained since
// #909, but still one ciphertext-per-reply capped at maxNoisePayloadBytes, and
// still ties up Run for the handler's whole invocation). So a bundle of any
// size cannot exceed a single AEAD frame or stall the dispatch loop. Safe from
// any goroutine (incl. a future #813 handler on Run): Push never blocks and
// never touches s.send. Returns on ENQUEUE, not
// delivery; returns the first Push error (ErrConnNotFound → conn not open) and
// stops. Logs one content-free debug line (conn_id, chunks, bytes — never the
// streamed bytes, AC#4). Ships unwired; the request verb that drives it is #813.
func (m *V2SessionManager) StreamBundle(ctx context.Context, connID string, blob []byte) error

// ReassembleBundle is the exported receiver-contract reference + test oracle for
// the debug-bundle stream (#812): it walks frames in arrival order and rebuilds
// the blob, requiring each chunk's Seq == chunks-seen and done.Total ==
// chunks-seen, skipping interleaved non-bundle frames. On ANY failure
// (reorder / gap / duplicate / count-mismatch / malformed / missing marker) it
// returns (nil, err) — never partial or corrupt bytes. Pure; it has NO daemon
// inbound caller (production use is phone-side, out of repo) — it exists so the
// "fail cleanly on a truncated / reordered stream" contract is testable in-repo.
func ReassembleBundle(frames []protocol.Envelope) ([]byte, error)

// Mid-turn-reconnect replay source (#647), late-bound once during relay wiring
// after the interactive emitter (which owns the eventring) is built — a
// construction-time V2SessionConfig field is not buildable because the emitter
// and manager have a circular dependency (the ring does not exist when
// NewV2SessionManager runs). Stored under pushMu (the existing leaf lock). ring
// is the emitter's per-conversation event ring; currentConv resolves the
// conversation a reconnecting conn replays for (the cmd/pyry active-conversation
// cursor as of #687 — was the supervisor's #312 cursor, which #678 emptied for
// routed turns).
// nil ring or cursor (the setter never called, or the stream off) leaves replay
// disabled — a phone advertising last_event_id then just gets the live stream.
func (m *V2SessionManager) SetReplaySource(ring *eventring.Ring, currentConv func() string)

// ActiveConn is one open v2 session in the capability-aware enumeration: its
// routing conn-id, the negotiated interactive-capability decision recorded at
// handshake, and what the client reported about itself there (#2148). It holds
// no *V2Session, CipherState, key, or plaintext, so the snapshot is safe to hand
// to a consumer goroutine.
//
// DeviceName and ClientVersion are REMOTE-AUTHORED, UNVALIDATED display strings
// — the only fields here that are not daemon-authored routing/decision data,
// which is why they carry an obligation the other two do not: a consumer MUST
// NOT log them, interpolate them into an error, or format the struct wholesale
// (%+v, slog.Any), which would emit them into the daemon log by accident.
// internal/sessions' admitClient is the gate the one consumer that renders them
// uses. Both are "" for a client that reported nothing and for one whose value
// exceeded maxRetainedClientNameBytes/maxRetainedClientVersionBytes.
type ActiveConn struct {
    ConnID        string
    Interactive   bool
    DeviceName    string
    ClientVersion string
}

// Concurrency-safe snapshot of every session currently in V2StateOpen (#588,
// made capability-aware in #626), each paired with its negotiated interactive
// flag — the enumeration half of server-initiated fan-out. The future #596
// structured-stream fan-out calls this and selects interactive vs
// non-interactive conns on the Interactive flag, then Push on each conn-id.
// Safe to call from any goroutine other than the dispatch goroutine — funneled
// onto Run so m.sessions is never read concurrently with a map write. Result is
// an unordered set; sessions still handshaking / token-unvalidated are excluded
// (the same V2StateOpen gate Push enforces, so the negotiated flag of an
// un-authenticated peer is never observable). Returns nil on ctx cancellation or
// after Run has exited — no error, since a snapshot has no failure the caller
// can act on. Capability-aware v2 analog of v1's dispatch.Dispatcher.ActiveConns().
func (m *V2SessionManager) ActiveConns(ctx context.Context) []ActiveConn

// Sentinels returned by Rekey and Push. ErrConnNotFound wraps
// control.ErrConnNotFound via %w so the slice A dispatcher's errors.Is
// mapping fires unchanged.
var (
    ErrConnNotFound   error // wraps control.ErrConnNotFound
    ErrSessionNotOpen error // session not in V2StateOpen (or, for Rekey, already awaiting a rekey reply)
)
```

`NewV2SessionManager` panics on missing `Frames` or `Logger` (programmer errors, same posture as `internal/dispatch.New`); returns a wrapped error on missing `Outbound` / `Devices` / `ServerID` or on wrong-length `StaticPriv` (caller-facing config bugs). `Handlers` is optional — nil or empty means every open-state envelope falls through to a sealed `protocol.unsupported` reply via [`dispatch.Route`](dispatch-package.md). `KnownConversation` (#618) and `ModalResolver` (#727) are also **optional and unvalidated** — leaving them nil keeps the existing construction sites compiling unchanged; a nil `KnownConversation` rejects every `request_snapshot` as `conversation.not_found`, and a known `conversation_id` answers `server.binary_offline` unconditionally (#2540 deleted the render arm and the `Snapshotter` / `SnapshotSettings` / `SnapshotUsage` fields that used to gate and populate it — see [Inbound `request_snapshot` handler](v2-session-manager-state-machine-inbound-screen-snapshot-handler-handlere.md)); a nil `ModalResolver` makes both modal-control frames **and** an armed deny-on-timeout (#725) inert debug-logged no-ops (the modal bridge unwired — the foreground case; the relay wires it live, and #798 wired the outbound producer that arms it); a nil `Interrupter` (#707) makes an inbound `interrupt` inert (no Esc — the foreground/unwired case); a nil `QueueRemover` (#723) makes an inbound `dequeue_message` inert (no `Remove` — foreground/unwired); a nil `Connected` (#874) makes `transportDown()` always `false`, so the push drain never holds — byte-identical to the pre-#874 drop-on-send posture (foreground/unwired/existing tests); a nil `Reconnect` (#875) makes `Run`'s reconnect arm a permanently-not-ready nil-channel read, so the drain wakes only on the pre-#875 Push-driven `drainCh` re-signal — byte-identical foreground/unwired/existing-test posture; a nil `OutstandingModals` (#877) makes `reconcileModals` a no-op on every `handleNoiseInit` success tail — byte-identical to the pre-#877 / foreground / existing-test posture; a nil `OutstandingQueues` (#878) makes `reconcileQueues` the same no-op, byte-identical to the pre-#878 / foreground / existing-test posture; a nil `RetainedModelLists` (#1863) makes `reconcileModelLists` the same no-op — the foreground/v1 and test posture; production wires a non-nil producer as of #1867; a nil `AttachmentResolve` (#2054) makes `request_attachment` inert the same way a nil `AttachmentIntake` makes `attachment_chunk` inert — consumed at the dispatch boundary so it no longer draws `dispatch.Route`'s unknown-type reply, but nothing decoded, resolved or replied. `Run` blocks until `Frames` closes (returns `nil`) or `ctx` is cancelled (returns `ctx.Err()`); every per-conn session is dropped on return.
