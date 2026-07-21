# `internal/relay` V2 session manager — Noise_IK handshake + open-state dispatch

The fourth surface of `internal/relay` (alongside the v1 outbound dial in `connection.go`, the v1 first-frame auth gate in `auth.go`, and the per-envelope-type handlers under `handlers/`). Adds the binary-side per-`conn_id` state machine that completes a [Mobile Protocol v2](../../protocol-mobile.md) Noise_IK handshake, validates the device-token piggybacked in IK message 1 early-data, negotiates the phone's advertised `interactive` capability against the daemon's authoritative supported set — echoing the intersection in `hello_ack` and recording the per-conn `interactive` flag (#626), dispatches `noise_msg` frames in the `open` state through the existing handler chain (#446), intercepts v2 control envelopes (`rekey_request`, #454) at the dispatch boundary, runs the responder side of a phone-initiated re-key with peer-static continuity and atomic CipherState swap (#453), arms a per-session 1-hour timer that emits an AEAD-sealed `rekey_request` envelope and tears the conn down at WS 4426 if the phone does not reply with a fresh `noise_init` within 30 s (#450), arms a per-session idle timer that tears an open session down at WS 4408 when no inbound frame arrives within a bounded idle window — the in-repo idle sweep that bounds the lifetime of a dropped/backgrounded phone's two Noise `CipherState`s and armed rekey timer under connect/disconnect churn rather than letting them linger up to the 1-hour rekey interval (#774), exposes a `Rekey(ctx, connID)` method that funnels operator-driven manual re-keys onto the same emit machinery with `payload.reason = "manual"` (#462), exposes a `Push(ctx, connID, env)` method for concurrency-safe **server-initiated** delivery of an unsolicited `noise_msg` to an addressed open session — non-blocking under relay backpressure via a per-session bounded buffer + droppable-delta drop policy (#571, made non-blocking #610), exposes a `StreamBundle(ctx, connID, blob)` method that moves an arbitrarily-large `[]byte` to one open conn as ordered, cap-respecting `debug_bundle_chunk` frames ending in a `debug_bundle_done` marker by looping `Push` — the streaming primitive for a bundle too large for a single AEAD frame or the 8-slot synchronous handler-reply buffer, driven by the #813 request verb (#812, `security-sensitive`), exposes a capability-aware `ActiveConns(ctx)` enumeration (and its `ActiveConnIDs(ctx)` `[]string` projection) that returns a concurrency-safe snapshot of every currently-open session paired with its negotiated `interactive` flag — the enumeration half of the fan-out primitive (#588, made capability-aware in #626), intercepts the inbound `request_snapshot` control envelope at the dispatch boundary and pushes a `screen_snapshot` carrying the current claude screen rendered to plain text back to the requester (#618, `security-sensitive`), serves mid-turn-reconnect replay from a late-bound event ring — a phone advertising `hello.last_event_id` is replayed the conversation's missed tail (or sent a `resync` marker) before the live stream resumes (#647, `security-sensitive`; the caught-up watermark is clamped to the conversation's newest retained id so an out-of-range / hostile `last_event_id` cannot suppress the live stream — #663; and the missed tail is forwarded one event per `Run` pass — via a Run-owned `replayQueue` drained by `drainReplayOnce`, with a `drainOnce` gate holding the conn's live events until the tail empties — so a large replay no longer monopolises the dispatch goroutine and stalls other connections' delivery, while replay ids still precede live ids on the wire and every seal stays single-writer — #777, see [Reconnect replay](#reconnect-replay-647--hellolast_event_id--ring-replay--resync)), intercepts the inbound `modal_answer` / `modal_cancel` control envelopes at the dispatch boundary and — via a consumer-declared `ModalResolver` seam — resolves a `modal_cancel` (consume the outstanding modal, route the fail-safe ESC, audit) then fans a `modal_dismissed` broadcast to every interactive-capable conn, while `modal_answer` resolves **only from a per-device-gated device** — `option_id` validated against the surfaced modal, the safe-answer keystroke routed, the terminal decision audited — and, when **no** device answers within a bounded window, arms a daemon-global deny-on-timeout that safe-denies the modal (ESC), fans the same `modal_dismissed{timeout}` broadcast, and audits `denied_timeout` (#727 seam + #717 gated answer arm + #725 deny-on-timeout, `security-sensitive`, see [Inbound modal control](#inbound-modal-control-727717--deny-on-timeout-725--modalresolver-seam--modal_dismissed-broadcast)), intercepts the inbound `interrupt` control envelope at the dispatch boundary and — gated on the conn's negotiated `interactive` capability — routes a single Esc to the supervised claude via a consumer-declared `Interrupter` seam (the remote equivalent of pressing Esc locally; `security-sensitive`, the first inbound frame whose authorization *is* the capability — see [Inbound interrupt](#inbound-interrupt-707--interrupter-seam--esc-routing), #707), intercepts the inbound `new_session` control envelope at the dispatch boundary and — gated on the conn's negotiated `interactive` capability — routes a `/clear` to the supervised claude via a consumer-declared `SessionStarter` seam (the remote equivalent of typing `/clear` locally; `security-sensitive`, reuses the `interactive`-capability-is-the-authorization posture `interrupt` established; the client observes the resulting break through the pre-existing `session_transition` marker, no new emitter or ack path — see [Inbound new_session](#inbound-new_session-831--sessionstarter-seam--clear-routing), #831, split from #824), intercepts the inbound `dequeue_message` control envelope at the dispatch boundary and — gated on the conn's negotiated `interactive` capability — removes a not-yet-drained queued message by id from the live `msgqueue` backlog via a consumer-declared `QueueRemover` seam, letting a phone cancel a queued `send_message` before it drains (the automatic `OnChange` → #722 producer path then refreshes `queue_state`; `security-sensitive`, the second inbound capability-gated frame — see [Inbound dequeue_message](#inbound-dequeue_message-723--queueremover-seam--msgqueueremove), #723), intercepts the inbound bare `request_debug_bundle` control envelope at the dispatch boundary and — for any paired conn (pairing is the authorization; **not** capability-gated) — assembles the daemon-global debug bundle via an injected `DebugBundler` closure (over [#811's `debugbundle.Assemble`](debugbundle-package.md)) and streams it back with `StreamBundle`, the capstone wiring #811 (producer) + #812 (transport) into one paired-client request/response flow (#813, `security-sensitive`, the first wire-reachable path emitting the recording to a remote client — see [Inbound debug-bundle request](#inbound-debug-bundle-request-813--request_debug_bundle--debugbundler--streambundle)), bounds repeated `request_debug_bundle` requests on a stalled/slow transport to one in-flight bundle's chunks per conn via a `pushMu`-guarded `bundleInFlight` scan checked before assembly, closing a retry-driven unbounded-memory amplification the control-class bundle chunks opened (#911, `security-sensitive`, see [Inbound debug-bundle request](#inbound-debug-bundle-request-813--request_debug_bundle--debugbundler--streambundle)), intercepts the inbound `set_session_settings` control envelope at the dispatch boundary and — gated on the conn's negotiated `interactive` capability — validates the untrusted `model` / `effort` fields at the wire boundary, persists any combination of `model` / `effort` / `yolo` via a consumer-declared `SettingsUpdater` seam (`*sessions.Pool.UpdateSettings`, #840) applied atomically, and **always replies** on the interactive path with a deterministic `session_settings_updated` success or a fixed-string `error` failure — unlike the fire-and-forget control verbs above, this is the first inbound control verb that owes the caller a reply (#845, `security-sensitive`, the untrusted remote write path for per-session model/effort/YOLO and the argv-injection defense #833 deferred — see [Inbound set_session_settings](#inbound-set_session_settings-845--settingsupdater-seam-validate-persist-reply)), extends the push queue's `queuedEnv` unsealed-hold invariant from the enqueue side to the drain side via an optional pre-seal `Connected func() bool` transport probe — a control envelope queued while the relay leg is down is held un-popped and unsealed rather than sealed-and-dropped, so no Noise send-nonce is burned for a frame that cannot reach the phone (`security-sensitive`, layer 1 of the reconnect-reliability design, umbrella #829; the immediate flush-on-reconnect trigger — landed in #875, see [Immediate flush-on-reconnect](#immediate-flush-on-reconnect-875--reconnect-signal--connectionreconnected-fan-out) — re-signals the drain the instant the transport reconnects, with no intervening `Push` required), unicasts the still-outstanding `modal_shown` set to a freshly interactive-open conn via an optional `OutstandingModals` closure seam, bringing a phone that connects or reconnects while a permission prompt is pending to current modal truth instead of letting it silently ride the daemon's deny-on-timeout window unseen (#877, `security-sensitive`, the modal half of the reconcile-on-connect mechanism, umbrella #829, see [Connect-time modal reconcile](#connect-time-modal-reconcile-877--outstandingmodals-seam--reconcilemodals)), unicasts the current per-conversation `queue_state` for every non-empty backlog to a freshly interactive-open conn via a sibling optional `OutstandingQueues` closure seam, bringing a phone that connects or reconnects between backlog changes to current queue truth instead of the stale/empty view #722's push-on-change leaves (#878, `security-sensitive`, the queue half of the reconcile-on-connect mechanism, umbrella #829, see [Connect-time queue reconcile](#connect-time-queue-reconcile-878--outstandingqueues-seam--reconcilequeues)), and refuses every out-of-state inner frame or tampered AEAD payload at the WS-close layer.

**Wire role:** the responder half of [`internal/noise`](noise-package.md)'s `Responder` / `WriteResp` API, parameterised with the binary's static X25519 private key, the device registry, an outbound `RoutingEnvelope` forwarder, and an optional `dispatch.Handler` table for open-state application dispatch.

**Production wiring:** **wired into the daemon as of [#549](../codebase/549.md)**, originally behind the operator switch `PYRY_MOBILE_V2=1`; [#913](../codebase/913.md) removed the switch (and the v1 `else` leg it selected) from `startRelay`, so `cmd/pyry/relay.go:startRelay` now builds the `V2SessionManager` over `conn.Frames()` (via the `startRelayV2` helper) unconditionally. It registers the **same** `dispatch.Handler` values against `V2SessionConfig.Handlers` that the retired v1 path passed to `Dispatcher.Register` (four as of #666: `list_conversations` / `create_conversation` / `register_push_token` / `send_message`) — no app-verb coverage was lost by the removal. The static key is loaded with the same `keys.LoadOrCreate(resolveStaticKeyBaseDir(), sanitizeName(name))` pair `pyry pair` uses, so the daemon's private key derives the public key the phone pinned at pairing. The cutover was a **hard switch, no soft fallback** ([ADR 024](../decisions/024-noise-ik-mobile-e2e.md)) from the start; `pyry pair preflight` ([#436](../codebase/436.md)) remains the operator's pre-flip safety check for pre-v2 pair records. See [`codebase/549.md`](../codebase/549.md) § Cutover behaviour for the original switch mechanism and [`codebase/913.md`](../codebase/913.md) for its removal.

## Surface

```go
package relay

// WS close codes (the wire-spec values in docs/protocol-mobile.md § Error codes).
// 4401 (StatusUnauthorized) lives in auth.go and is reused unchanged.
const (
    StatusIdleTimeout      websocket.StatusCode = 4408 // idle-session teardown by the in-repo sweep (#774); echoes HTTP 408
    StatusProtocolMismatch websocket.StatusCode = 4421 // state-machine / discriminator violation
    StatusHandshakeFailure websocket.StatusCode = 4426 // Noise_IK failure before CipherStates exist
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

// ScreenSnapshotter renders the daemon's live claude screen to plain text
// (#618). *supervisor.Supervisor satisfies it; declared in the consumer so
// internal/relay depends on neither internal/supervisor nor tui-driver.
type ScreenSnapshotter interface {
    ScreenSnapshot() (text string, live bool) // live==false (text "") ⇒ no child attached
}

// Interrupter delivers a single Esc to the supervised claude — the remote
// equivalent of a local Esc, claude's own interrupt (#707). *supervisor.Supervisor
// satisfies it via the sealed SendEsc (#726), so the supervisor needs no new
// method; declared in the consumer (beside ScreenSnapshotter) so internal/relay
// imports neither internal/supervisor nor tui-driver. Named for its relay-domain
// role even though the method keeps the sealed surface's name.
type Interrupter interface{ SendEsc() error }

// SessionStarter drives the supervised claude's /clear — the remote equivalent
// of a local /clear, starting a fresh session (#831). *supervisor.Supervisor
// satisfies it via the sealed StartNewSession (#830), so the supervisor needs no
// new method; declared in the consumer (beside Interrupter) so internal/relay
// imports neither internal/supervisor nor tui-driver. Named for its relay-domain
// role even though the method keeps the sealed surface's name.
type SessionStarter interface{ StartNewSession() error }

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

// ModalResolver resolves an inbound modal control frame (or a deny-on-timeout)
// against the daemon's outstanding-modal state (#727, extended #725). Declared in
// the consumer (beside ScreenSnapshotter) so internal/relay imports neither
// internal/supervisor, internal/modalbridge, internal/audit, nor cmd/pyry; the
// cmd/pyry resolver satisfies it. *devices.Device crosses the seam (the per-conn
// s.device) on the cancel/answer arms. All three methods run on the manager's
// single Run dispatch goroutine.
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
    // ResolveTimeout safe-denies an unanswered modal whose deny-on-timeout window
    // elapsed (#725): consume modalID (registry Resolve) → fail-closed ESC →
    // audit outcome=denied_timeout/source=timeout with an EMPTY device (a timeout
    // has no answering device) → return the dismissal with ok=true. Takes no
    // device (the deny is unconditional, nothing to gate). An unknown/already-
    // resolved id (an answer/cancel won the race) ⇒ (zero, false): no keystroke,
    // no audit, no dismissal — the loser path.
    ResolveTimeout(modalID string) (ModalDismissal, bool)
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
    Snapshotter       ScreenSnapshotter                // optional (#618); nil ⇒ request_snapshot → server.binary_offline
    KnownConversation func(conversationID string) bool // optional (#618); nil ⇒ request_snapshot → conversation.not_found
    SnapshotSettings  func() (model, effort string, yolo bool) // optional (#848); nil ⇒ screen_snapshot reports defaults (empty model/effort, yolo:false)
    SnapshotUsage     func() (usedTokens, windowTokens int) // optional (#857); nil ⇒ screen_snapshot reports used_tokens:0, window_tokens:0
    ModalResolver     ModalResolver                    // optional (#727/#725); nil ⇒ modal_answer/modal_cancel + deny-on-timeout inert no-ops
    OutstandingModals func() []protocol.ModalShownPayload // optional (#877); nil ⇒ no connect-time modal reconcile — byte-identical to pre-#877. Production wires modalbridge.Registry.Snapshot.
    OutstandingQueues func() []protocol.QueueStatePayload // optional (#878); nil ⇒ no connect-time queue reconcile — byte-identical to pre-#878. Production wires the cmd/pyry outstandingQueues adapter over *msgqueue.Queue.SnapshotAll.
    Interrupter       Interrupter                      // optional (#707); nil ⇒ interrupt is inert (no Esc) — foreground/unwired. Production wires *supervisor.Supervisor.
    SessionStarter    SessionStarter                   // optional (#831); nil ⇒ new_session is inert (no /clear) — foreground/unwired. Production wires *supervisor.Supervisor.
    QueueRemover      QueueRemover                     // optional (#723); nil ⇒ dequeue_message is inert (no Remove) — foreground/unwired. Production wires the concrete *msgqueue.Queue.
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

// ActiveConn is one open v2 session in the capability-aware enumeration (#626):
// its routing conn-id and the negotiated interactive-capability decision
// recorded at handshake. Holds only non-secret routing/decision data — never a
// *V2Session, CipherState, key, or plaintext.
type ActiveConn struct {
    ConnID      string
    Interactive bool
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

// Thin projection over ActiveConns (dropping the interactive flag), preserved
// for the capability-agnostic #589 fan-out consumer. Signature and observable
// contract (unordered set, nil on ctx cancellation, non-nil-empty on an empty
// manager) unchanged.
func (m *V2SessionManager) ActiveConnIDs(ctx context.Context) []string

// Sentinels returned by Rekey and Push. ErrConnNotFound wraps
// control.ErrConnNotFound via %w so the slice A dispatcher's errors.Is
// mapping fires unchanged.
var (
    ErrConnNotFound   error // wraps control.ErrConnNotFound
    ErrSessionNotOpen error // session not in V2StateOpen (or, for Rekey, already awaiting a rekey reply)
)
```

`NewV2SessionManager` panics on missing `Frames` or `Logger` (programmer errors, same posture as `internal/dispatch.New`); returns a wrapped error on missing `Outbound` / `Devices` / `ServerID` or on wrong-length `StaticPriv` (caller-facing config bugs). `Handlers` is optional — nil or empty means every open-state envelope falls through to a sealed `protocol.unsupported` reply via [`dispatch.Route`](dispatch-package.md). `Snapshotter` and `KnownConversation` (#618), `SnapshotSettings` (#848), and `ModalResolver` (#727) are also **optional and unvalidated** — leaving them nil keeps the existing construction sites compiling unchanged; a nil `KnownConversation` rejects every `request_snapshot` as `conversation.not_found` and a nil `Snapshotter` reports `server.binary_offline`, so the snapshot feature is simply unavailable, never a crash; a nil `SnapshotSettings` leaves the `screen_snapshot` reply's `model`/`effort`/`yolo` fields at their defaults (empty model/effort, `yolo:false`) — never a rejected reply, since the settings read is downstream of the `live` check; a nil `SnapshotUsage` (#857) leaves the reply's `used_tokens`/`window_tokens` fields at `0`/`0`, same posture; a nil `ModalResolver` makes both modal-control frames **and** an armed deny-on-timeout (#725) inert debug-logged no-ops (the modal bridge unwired — the foreground case; the relay wires it live, and #798 wired the outbound producer that arms it); a nil `Interrupter` (#707) makes an inbound `interrupt` inert (no Esc — the foreground/unwired case); a nil `QueueRemover` (#723) makes an inbound `dequeue_message` inert (no `Remove` — foreground/unwired); a nil `Connected` (#874) makes `transportDown()` always `false`, so the push drain never holds — byte-identical to the pre-#874 drop-on-send posture (foreground/unwired/existing tests); a nil `Reconnect` (#875) makes `Run`'s reconnect arm a permanently-not-ready nil-channel read, so the drain wakes only on the pre-#875 Push-driven `drainCh` re-signal — byte-identical foreground/unwired/existing-test posture; a nil `OutstandingModals` (#877) makes `reconcileModals` a no-op on every `handleNoiseInit` success tail — byte-identical to the pre-#877 / foreground / existing-test posture; a nil `OutstandingQueues` (#878) makes `reconcileQueues` the same no-op, byte-identical to the pre-#878 / foreground / existing-test posture. `Run` blocks until `Frames` closes (returns `nil`) or `ctx` is cancelled (returns `ctx.Err()`); every per-conn session is dropped on return.

## Wire types (`internal/protocol/v2envelope.go`)

```go
const V2Version = 2

const (
    TypeNoiseInit = "noise_init"
    TypeNoiseResp = "noise_resp"
    TypeNoiseMsg  = "noise_msg"
)

type InnerFrameV2 struct {
    Version int    `json:"v"`
    Type    string `json:"type"`
    Data    string `json:"data"` // base64.StdEncoding, padded; ≤ 65535 bytes decoded
}
```

Pure data type — the manager owns shape-checking. The 65535-byte cap on decoded `Data` is the Noise framework's per-message limit (`docs/protocol-mobile.md` § Wire shapes); enforced at the JSON-decode boundary so oversized payloads never reach `Responder.ReadInit`.

The `Token string \`json:"token,omitempty"\`` field appended to `protocol.HelloClientPayload` is the in-band carrier of the device-pairing token under v2 (`docs/protocol-mobile.md` § Authentication line 420). `omitempty` keeps v1 round-trip byte-identical for existing fixtures and tests. The v1 routing-envelope `RoutingEnvelope.Token` field is NOT removed in this slice — the v1 dispatcher still consumes it; v2's manager deliberately ignores `RoutingEnvelope.Token` per spec line 600.

## State machine

```
                    +-------------------+
                    | V2StateAwaitingInit |    (created lazily on first frame for a conn_id)
                    +-------------------+
                              |
                  noise_init  | (run handshake)
                              v
                    +---------------------+
                    | V2StateHandshakeCompl |  (CipherStates live; token not yet validated)
                    +---------------------+
                              |
                       token  | (Validate hit)
                          OK  v
                    +-----------+
                    | V2StateOpen |          (noise_msg → dispatch.Route → AEAD-sealed reply)
                    +-----------+

  Any rejection at any state → emit a single RoutingEnvelope carrying the
  optional sealed error frame plus the CloseCode, transition to V2StateClosed,
  delete the session from the manager's map.
```

The `handshakeComplete` substate is observably distinct from `open` even though both can be set inside the same `noise_init` handler — the externally-controlled `state` field exists so the gating test pins the "handler chain unreachable from `handshakeComplete`" invariant deterministically (AC #4) and so any future refactor that splits the dispatch loop cannot silently remove the invariant.

### Transition table

| Inbound on conn_id | `awaitingInit` | `handshakeComplete` | `open` | `closed` |
|---|---|---|---|---|
| `noise_init` | run handshake (below) | close(4421), → closed (`reason=noise_init_in_handshake_complete`) | **re-key responder; peer-static check; CipherState swap; state stays `open` (#453)** | drop |
| `noise_resp` (phone is never the writer) | close(4421), → closed | close(4421), → closed | close(4421), → closed | drop |
| `noise_msg`, decrypt succeeds | close(4421), → closed (no CipherStates yet) | sealed `auth.invalid_token` + close(4401), → closed | **dispatch via handler chain; AEAD-sealed reply emitted; state stays `open`** | drop |
| `noise_msg`, decrypt fails / no CipherStates | close(4421), → closed | close(4421), → closed | **close(4421), → closed (AEAD-failure teardown; session entry dropped — also fires on stale-key frames after a #453 swap)** | drop |
| Unknown `type` / bad `v` / malformed JSON / oversized `data` | close(4421), → closed | close(4421), → closed | close(4421), → closed | drop |

### `noise_init` happy-and-failure path

1. JSON-decode inner frame, validate `Version == 2` and `Type == "noise_init"`, base64-decode `Data` (size-cap 65535).
2. Lazy-construct `noise.Responder` from `cfg.StaticPriv` (one per session).
3. `Responder.ReadInit(data)` → early-data bytes. **On err: close(4426)** (MAC fail / wrong static pubkey / malformed IK message 1; no CipherStates exist, no AEAD-sealed error possible).
3a. **Capture `s.peerStatic = s.resp.PeerStatic()`** (#452) — the initiator's static pub, pinned for the session's lifetime at the earliest authenticated point. By the time `ReadInit` returns nil, flynn has MAC-verified and DH-decrypted the static; the value is authentic regardless of whether subsequent steps accept the hello. Set exactly once per `V2Session`; a future re-key (added in #453) MUST NOT overwrite it. The field is inert in the current code (no production reader); #453's `handleRekeyInit` will compare a fresh-handshake initiator's `PeerStatic()` against it via `bytes.Equal` and reject mismatches at WS close code 4426.
4. Decode early-data as a `protocol.Envelope`; require `Type == "hello"`. On decode failure or wrong type: **close(4421)** (handshake-layer protocol violation; CipherStates still don't exist).
5. Decode `Envelope.Payload` as `protocol.HelloClientPayload`. On decode failure: close(4421).
6. Marshal `HelloAckPayload{ProtocolVersion: "v2", ServerID: cfg.ServerID, ConnID: env.ConnID}` into an `Envelope{ID: 1, Type: hello_ack, InReplyTo: &hello.ID}`.
7. `Responder.WriteResp(ackEnvJSON)` → response bytes + `(send, recv)` CipherStates. **On err: close(4426)** (practically unreachable under correct flynn/noise; defensive).
8. Persist `send` / `recv` on the session. **State → `V2StateHandshakeComplete`** (the externally-observable substate, even though step 9 immediately advances or rejects).
9. Marshal an `InnerFrameV2{Type: noise_resp, Data: base64(respMsg)}`.
9a. **Registry reload (#782).** If `cfg.DevicesPath != ""`, call `cfg.Devices.Reload(cfg.DevicesPath)` so a device paired via `pyry pair` after daemon startup authenticates here without a restart (and a `pyry pair revoke` stops being accepted). On a reload error: log at Warn (`v2.devices.reload_failed`, `conn_id` + `path` + static reason — **never** the wrapped `err`, which can carry a `token_hash` from a corrupt file) and **proceed to step 10 against the retained in-memory set** (fail closed — accept set not widened, loaded devices not lost). See [`features/devices-registry.md`](devices-registry.md) § Reload and [ADR 029](../decisions/029-devices-registry-reload-at-handshake.md).
10. `cfg.Devices.Validate(hello.Token)`:
    - **Hit**: emit `RoutingEnvelope{ConnID, Frame: noiseRespFrame}` via `Outbound`, **state → `V2StateOpen`**. Log `v2.handshake.accept` with `conn_id` + `device_name`.
    - **Miss**: emit `noise_resp` first (so the AEAD channel exists on the wire), then AEAD-seal an `Envelope{Type: error, Payload: ErrorPayload{Code: auth.invalid_token, Message: MsgInvalidToken, Retryable: false}}` under `send`, wrap as an `InnerFrameV2{Type: noise_msg, Data: base64(ciphertext)}`, and emit one `RoutingEnvelope{ConnID, Frame: noiseMsgFrame, CloseCode: 4401}`. **State → `V2StateClosed`**, session deleted. Log `v2.handshake.reject.invalid_token` with `conn_id` only (NO device-name on reject — anti-enumeration, mirrors `auth.go:129-132`).

The AEAD-error-then-close path emits the error frame AND the close code in a **single** outbound routing envelope — atomic at the wire layer. This is what guarantees the spec's MUST: the phone observes the error envelope *before* the WS close (`docs/protocol-mobile.md` § Failure modes line 436). Two-call sequencing (`Send` then `CloseConn`) would race the relay's output paths.

### `noise_msg` in `V2StateHandshakeComplete` — the gating row

Today the natural inbound-frame flow never reaches this cell: state transitions through `handshakeComplete` atomically inside the `noise_init` handler. The cell exists for two reasons:

- **Future-proofing.** If a later slice introduces deferred token validation (e.g. network-backed registry lookup), `handshakeComplete` becomes observable to incoming frames between handshake completion and validation completion. The transition row defines the behaviour for that future world today.
- **Same-package unit-test verifiability.** A gating test in `v2session_test.go` directly assigns `s.state = V2StateHandshakeComplete` and injects a hand-rolled `(send, recv)` pair to drive this row deterministically — the **structural** proof that the handler chain is unreachable from `handshakeComplete`. AC #4's load-bearing invariant.

The implementation tries AEAD-decrypt-then-decode-as-`Envelope`. Decrypt failure → close(4421). Decrypt success → seal `auth.invalid_token` under the live `send` CipherState, emit + close(4401), regardless of envelope type. The handler chain in `internal/relay/handlers/` is **not** reached.

### `noise_msg` in `V2StateOpen` — application dispatch and AEAD-failure teardown

The two `open`-row cells filled by [#446](../codebase/446.md).

**Happy path** (`dispatchAppFrame`):

1. `s.recv.Decrypt(inner.Data)` → plaintext envelope JSON. The handler chain is unreached on `Decrypt` failure (see below).
2. **v2 control-envelope discriminator** (#454, extended #618/#727/#707/#723/#813/#831): `json.Unmarshal(plaintext, &probeEnv)`; on decode success a `switch probeEnv.Type` intercepts control envelopes before the application chain — `TypeRekeyRequest` → `handleRekeyRequest`, `TypeRequestSnapshot` → `handleRequestSnapshot` (#618), `TypeModalCancel` → `handleModalCancel`, `TypeModalAnswer` → `handleModalAnswer` (#727), `TypeInterrupt` → `handleInterrupt` (#707), `TypeNewSession` → `handleNewSession` (#831), `TypeDequeueMessage` → `handleDequeueMessage` (#723), `TypeRequestDebugBundle` → `handleDebugBundleRequest` (#813) — then returns. The application handler chain is NOT consulted for any of them. Decode failures (and any other type) deliberately fall through to step 3 so `dispatch.Route`'s malformed-envelope branch emits the sealed `protocol.malformed` reply established by #446. The probe is a re-decode (`dispatch.Route` decodes the same plaintext again); the cost is one small JSON parse per application frame, well below the per-frame AEAD cost.
3. Allocate a per-frame `outbound chan protocol.RoutingEnvelope` (buffer 8 — `handlerOutboundBuf`) and a per-frame `*dispatch.Conn` via [`dispatch.NewConn`](dispatch-package.md), carrying the matched device snapshot captured in step 10 of the handshake.
4. Spawn `dispatch.Route(ctx, m.cfg.Logger, conn, m.cfg.Handlers, plaintext)` on a short-lived goroutine (`defer close(routeDone)` on return) — same error-envelope paths as v1 `Dispatcher.handleOne` (malformed JSON → sealed `protocol.malformed`; unsupported / unknown type / no handler → sealed `protocol.unsupported`-or-`unknown_type`; handler error → log WARN, no synthesised reply). **`Run` interleaves draining `outbound` with waiting for `routeDone`, rather than draining only after `Route` returns** ([#909](../codebase/909.md)) — a handler emitting more replies than `handlerOutboundBuf` holds can no longer fill the channel and deadlock `Route` inside `c.Send`.
5. For each reply — as it arrives while `Route` runs, and again in a final non-blocking residual drain once `routeDone` fires — call `forwardAppReply`: `s.send.Encrypt(reply.Frame)` → `marshalInnerFrameV2(TypeNoiseMsg, ciphertext)` → emit via `m.send` with `CloseCode: 0`. The reply's `CloseCode` is ignored — close intent is reserved for the manager's own close-with paths. Single sender (the handler, off-`Run`) + single receiver (`Run`) over `outbound` preserves FIFO emission order; the seal itself runs only on `Run` — the spawned goroutine never touches `s.send`.
6. Return once `routeDone` has fired and the residual drain is empty. State remains `V2StateOpen`.

**v2 `rekey_request` handler** (`handleRekeyRequest`, #454). The binary is always the IK responder per [ADR 024](../decisions/024-noise-ik-mobile-e2e.md); an inbound `rekey_request` takes **no transport action** — no close, no outbound frame, no state mutation. The phone re-keys by sending `noise_init` directly, not by signalling via `rekey_request`. The handler decodes `env.Payload` as `struct{ Reason string }` and emits a single structured log line:

- Recognised reasons (`scheduled`, `manual`, `compromise` — closed set from `docs/protocol-mobile.md` § Re-key): **INFO** with `event=v2.rekey.request.received`, `conn_id`, `reason`.
- Empty / unknown / non-string / JSON-decode-failure: **WARN** with the same field shape. Forward-compat is deliberate — mobile may add a `reason` value before the binary catches up.

A malformed inner payload does **not** emit a sealed `protocol.malformed` reply: the envelope itself took no transport action, so emitting an error reply for a broken control payload would be a surprising behaviour change. The handler runs on the manager's single dispatch goroutine (same as everything else in this file); no new concurrency invariant.

### `noise_init` in `V2StateOpen` — re-key responder swap (#453)

A `noise_init` arriving for a `conn_id` already in `V2StateOpen` is a phone-initiated re-key per `docs/protocol-mobile.md` § Re-key (the phone is the only IK initiator). The top of `handleNoiseInit` is now a `switch s.state` that routes the `open` arm to `handleRekeyInit`. The handler runs the IK responder again against `cfg.StaticPriv`:

1. `noise.NewResponder(cfg.StaticPriv)` (fresh `Responder` per re-run; the original `s.resp` is left dangling — dead state, cleanup deferred).
2. `Responder.ReadInit(inner.Data)` — re-key `noise_init` early-data is empty per spec; the returned slice is discarded. On err: close(4426) with `reason=rekey_read_init_failed`.
3. **Peer-static continuity check.** `bytes.Equal(resp.PeerStatic(), s.peerStatic)` — the new initiator's static MUST match the value captured at initial handshake (the [#452](../codebase/452.md) field). Mismatch closes at 4426 with `reason=rekey_peer_static_mismatch`; the reject log line deliberately omits `device_name` (anti-enumeration discipline — the re-key initiator's identity is unknown / hostile). `bytes.Equal` is intentionally variable-time-acceptable: both operands are public keys, so timing leakage carries no secret. A one-line code comment names this choice to forestall a "should be `subtle.ConstantTimeCompare`" review nit. **Hello validation and token re-check do NOT run on the re-key path** — they ran at initial handshake; the per-rekey identity gate is the peer-static check, not the token.
4. `Responder.WriteResp(nil)` (empty early-data per spec) → new `(send, recv)` CipherStates + response bytes. On err: close(4426) with `reason=rekey_write_resp_failed`.
5. Marshal `InnerFrameV2{Type: noise_resp, Data: base64(respMsg)}`. On err: close(4426) with `reason=rekey_marshal_noise_resp`.
6. **Atomic CipherState swap.** A single tuple assignment `s.send, s.recv = newSend, newRecv` on the manager's single dispatch goroutine. No half-mixed state where one direction uses new keys and the other uses old — the loop IS the lock for `flynn/noise`'s non-goroutine-safe `CipherState` (#433's contract); a tuple assignment on this goroutine cannot be observed half-applied by any other code path, so the spec's atomic-switchover requirement is structural. The old `*CipherState` pointers are dropped from the struct; Go's GC reclaims the underlying memory. **No explicit `Wipe()` of the key bytes is exposed** — would require touching #433's surface, deferred; the single-owner-goroutine invariant means no code path reads the old state after the swap, which is the practical zeroisation property.
7. Log `v2.rekey.accept` at INFO with `conn_id` + `device_name` (operator-actionable; SAME device as initial handshake by construction — `s.device` is preserved across re-key).
8. Emit the new `noise_resp` envelope. State stays `V2StateOpen`; `s.device` and `s.peerStatic` are preserved (the [#452](../codebase/452.md) lifetime contract — a successful re-key MUST NOT overwrite `s.peerStatic`).

**Failure-mode → close-code table.** Every re-key failure mode closes at **4426** — the SAME code the initial handshake uses for IK-pattern failure. No new close code is introduced. The five reject branches in `handleRekeyInit` (`rekey_responder_init_failed`, `rekey_read_init_failed`, `rekey_peer_static_mismatch`, `rekey_write_resp_failed`, `rekey_marshal_noise_resp`) each emit a `v2.handshake.reject.ik_failure` WARN line and call `closeWith(ctx, s, StatusHandshakeFailure, nil)`. `closeWith` removes the session entry from `m.sessions`, so the next inbound frame on the same `conn_id` lazy-creates a fresh `V2StateAwaitingInit` session.

**`noise_init` in `V2StateHandshakeComplete` still rejects at 4421.** A `noise_init` arriving while CipherStates are held but uncommitted is the same state-machine violation it was before #453 (the `default` arm of the `switch s.state` block, with `reason=noise_init_in_handshake_complete`).

**Old-key frames after the swap fail AEAD and tear down the conn.** Once `s.recv` points at the new CipherState, any frame sealed under the old keys fails `s.recv.Decrypt` and lands in the existing #446 tampered-frame branch: `closeWith(StatusProtocolMismatch /*4421*/, nil)` → session removal. **No new code path** — the inheritance is verified by `TestV2Session_RekeyResponder_OldKeyFrameAfterSwap_4421`.

**Head-of-line blocking during re-key.** The re-key handshake costs roughly one X25519 derivation + AEAD setup (~100µs) plus one outbound `noise_resp` send. During this window the dispatch goroutine cannot service any other `conn_id` — same posture as the initial handshake. The per-conn fan-out follow-up that #446 names also covers this.

### Initiator-side scheduled re-key (#450) — 1-hour timer + `rekey_request` emit + 30 s reply timeout

Each `V2Session` that reaches `V2StateOpen` arms a 1-hour `*time.Timer` (`rekeyInterval`, package-var lowercase so tests substitute via `t.Cleanup`). On fire the timer's `time.AfterFunc` callback pushes a `wakeSignal{s, wakeRekeyEmit}` onto a per-manager buffered channel `m.wake` (cap `wakeBufferSize = 16`); the manager's `Run` loop pops the signal on a new third select arm and dispatches to `handleWake` → `emitRekeyRequest` on its own goroutine. The callback NEVER touches session state directly — the single-owner-goroutine invariant for `s.send` / `s.recv` / `s.state` is preserved by structurally routing every timer fire through the wake channel.

`Run` derives `runCtx, cancelRun := context.WithCancel(ctx); defer cancelRun()` and passes `runCtx` to every downstream handler (`handleFrame`, `handleWake`, the arming helpers, and transitively to `closeWith` / `handleNoiseInit` / `handleRekeyInit`). The arming callbacks select on `(m.wake <- signal, runCtx.Done())` so a fired-but-undelivered wake on a shutting-down `Run` exits via the ctx branch and leaves no goroutine behind. `Run`'s observable behaviour is unchanged — the return value is still `ctx.Err()` (so parent-cancel returns `context.Canceled` and `Frames`-close returns nil).

`emitRekeyRequest` mirrors `sealError`'s seal template: marshal `Envelope{ID: 1, Type: protocol.TypeRekeyRequest, TS: now, Payload: {"reason": "scheduled"}}` → `s.send.Encrypt` → `marshalInnerFrameV2(TypeNoiseMsg, ciphertext)` → `m.send(RoutingEnvelope{ConnID, Frame})`. Envelope `ID = 1` because there is no `rekey_ack` to correlate by `InReplyTo` per `docs/protocol-mobile.md` § Re-key (the next successful AEAD round-trip under the new keys is the implicit ack). `reason` is always `"scheduled"` in this slice; `"manual"` and `"compromise"` belong to the future `pyry rekey <conn_id>` verb (#451).

On successful emit, the manager sets `s.awaitingRekeyReply = true` and arms `s.rekeyReplyTimer = m.armRekeyReplyTimer(ctx, s)` (`rekeyReplyTimeout = 30 * time.Second`, same package-var posture). Three terminal branches:

- **Phone replies in time.** A fresh `noise_init` lands in `handleRekeyInit`, the IK handshake re-runs with the peer-static continuity check (#453), and on swap success the new `s.rekeyComplete(m, ctx)` hook fires at the success tail of `handleRekeyInit` (unconditional on the swap path — a spontaneous phone-initiated re-key that the binary did not request still re-bases the 1-hour cadence; clearing an unset `awaitingRekeyReply` is a no-op, stopping a nil `rekeyReplyTimer` is a no-op). `rekeyComplete` clears `awaitingRekeyReply`, stops + nils `rekeyReplyTimer`, stops the old `rekeyTimer`, and assigns `s.rekeyTimer = m.armRekeyTimer(ctx, s)` — the 1-hour cadence is re-based from the swap moment, not the previous emit moment.
- **Phone does not reply within 30 s.** `rekeyReplyTimer` fires, the callback pushes `wakeSignal{s, wakeRekeyReplyTimeout}` onto `m.wake`. `handleWake`'s `wakeRekeyReplyTimeout` arm checks `s.awaitingRekeyReply` (covering the swap-completed-before-timeout-fired race), emits a structured `noise.rekey_failed` WARN log line with `conn_id` + `close_code=4426` (NO `err=` field — anti-leakage of flynn-noise error text per the security review), and calls `closeWith(ctx, s, StatusHandshakeFailure, nil)`. The existing `closeWith` cleanup path runs: `s.state = V2StateClosed`, both per-session timers stopped + nilled, `delete(m.sessions, s.connID)`, single close-only outbound envelope emitted at WS 4426.
- **Conn closes for any other reason** (AEAD failure from [#446](../codebase/446.md), `noise_init` on an `awaitingInit` re-creation, manager shutdown). `closeWith` stops both per-session timers before the existing `delete(m.sessions, s.connID)`. `Stop()` is safe on a fired timer (returns false, no-op); the callback's `ctx.Done` arm is the load-bearing teardown (Run-derived `runCtx` cancels on Run exit, unblocking any pending callback's `m.wake` send).

**Per-session state additions on `V2Session`.** Three new fields, all owned by the dispatch goroutine: `rekeyTimer *time.Timer` (nil before initial open, nil after `closeWith`), `rekeyReplyTimer *time.Timer` (nil unless `awaitingRekeyReply` is true), `awaitingRekeyReply bool` (the canonical "are we awaiting a fresh noise_init" predicate — distinct from `rekeyReplyTimer != nil` because `Stop()` returns false on a fired timer whose callback may still be in flight, and the bool gives `handleWake`'s timeout arm a stable late-arrival predicate). No mutex — same single-owner-goroutine invariant as `s.send` / `s.recv` / `s.state` / `s.device` / `s.peerStatic`.

**Emit-side seal/marshal failures Warn-and-drop.** Same posture as `sealError`'s line 498-502: AEAD-seal failure on the emit side is realistically unreachable under correct flynn/noise; closing the conn over an internal seal failure would tear down a working session for a non-protocol error. Three distinct WARN events (`v2.rekey.emit.seal_failed`, `v2.rekey.emit.marshal_failed`, `v2.rekey.emit.skipped_already_awaiting`) carry `event` + `conn_id` only — no error text, no envelope bytes, no AEAD ciphertext. The `skipped_already_awaiting` case fires defensively if `emitRekeyRequest` is reached with `awaitingRekeyReply == true` (which should not happen — `rekeyTimer` is one-shot, only re-armed by `rekeyComplete`, which clears the bool first); surfacing it as Warn means operators see it if a future refactor introduces a re-emit bug.

**Wake delivery is blocking-send-plus-ctx-escape, not non-blocking-drop.** A dropped `wakeRekeyEmit` would cost the 1-hour cadence one beat and never re-arm (the next re-arm comes only via `rekeyComplete`); a dropped `wakeRekeyReplyTimeout` would park the session in `awaitingRekeyReply=true` forever. With `wakeBufferSize = 16` and rare fires, send-blocking is vanishingly rare; if it does happen, the callback waits microseconds for `Run` to drain one wake and exits cleanly.

**`rekeyInterval` and `rekeyReplyTimeout` are lowercase package vars, NOT exported constants.** The substitutability pattern matches `connection.go:43`'s `handshakeTimeout`; tests swap to sub-second values via `prev := rekeyInterval; rekeyInterval = 20*time.Millisecond; t.Cleanup(func() { rekeyInterval = prev })`. Production callers cannot override the 1-hour cadence — the value is a static program literal in production builds, which is the natural rate-limit against a misbehaving timer or a hostile config bump.

**No new ADR.** [ADR 024](../decisions/024-noise-ik-mobile-e2e.md) § Re-key policy specifies the 1-hour cadence and the explicit `rekey_request` envelope; this slice implements the initiator half. No new close code is introduced — the reply-timeout teardown reuses `StatusHandshakeFailure` (4426), the same code the initial handshake and the re-key responder (#453) use for the IK-failure class.

### Operator-driven manual re-key (#462) — `Rekey` method satisfying `control.Rekeyer`

`*V2SessionManager` satisfies [`control.Rekeyer`](control-plane.md#rekey-v2-conn-re-key-trigger-seam-13d-1-459) (pinned by `var _ control.Rekeyer = (*V2SessionManager)(nil)`). The public method `Rekey(ctx context.Context, connID string) error` funnels each request onto Run's single dispatch goroutine via a new unbuffered `manualRekey chan manualRekeyReq` field; a fourth `Run` select arm dequeues the request and dispatches to a private `handleManualRekey` method that runs on the owner goroutine. The shape preserves the single-owner-goroutine invariant for `s.send` / `s.state` / `s.rekeyTimer` / `s.awaitingRekeyReply` — `Rekey` itself does only channel I/O on the caller's goroutine; every session-state read or write happens in `handleManualRekey` on `Run`'s goroutine. **No new lock, no new atomic, no new long-lived goroutine.**

```go
type manualRekeyReq struct {
    connID string
    reply  chan error // cap=1 per request; manager's send is non-blocking
}

// In V2SessionManager:
//   manualRekey chan manualRekeyReq  // unbuffered: backpressure is correct

// In Run's select:
case req := <-m.manualRekey:
    req.reply <- m.handleManualRekey(runCtx, req.connID)
```

The channel is unbuffered: backpressure is the right semantics — if `Run` is busy processing a frame, `Rekey` waits. A buffer would mislead the caller into thinking the request was accepted when in reality `Run` hadn't yet observed it. The caller's `ctx` is the escape arm in both `Rekey` select blocks (enqueue and reply). The per-request reply channel is `cap=1` so the manager's `req.reply <- err` send is non-blocking even if the caller's ctx fires between enqueue and reply. The `manualRekey` channel is NOT closed on `Run` exit (matches the existing posture for `m.cfg.Frames`); in-flight callers unblock via `ctx.Done`.

**Method-name divergence.** Ticket #462's AC text said `TriggerRekey(connID string) error`; the slice A `control.Rekeyer` interface declared `Rekey(ctx context.Context, connID string) error`. The interface signature wins because it lets `*V2SessionManager` satisfy `control.Rekeyer` directly with no adapter. The compile-time `var _ control.Rekeyer = (*V2SessionManager)(nil)` assertion pins the contract so a future refactor cannot drift the name back to the AC's informal label.

**`handleManualRekey` is the lookup + emit dispatch site.** Three reject branches, then the emit:

| Precondition | Returned sentinel |
| --- | --- |
| `m.sessions[connID]` miss | `ErrConnNotFound` (wraps `control.ErrConnNotFound`) |
| `s.state != V2StateOpen` | `ErrSessionNotOpen` |
| `s.awaitingRekeyReply` (a prior emit is in flight) | `ErrSessionNotOpen` |
| All checks pass | stop+nil `s.rekeyTimer`; call `emitRekeyRequest(ctx, s, "manual")`; return nil |

The `!= V2StateOpen` and `awaitingRekeyReply` branches return the SAME sentinel because from the operator's perspective both mean "the conn is not in a state where a manual rekey can be initiated." Distinguishing them externally would leak internal state-machine vocabulary into the operator surface with no actionable consequence. The collapse also imposes a natural per-conn rate limit: at most one manual rekey per `rekeyReplyTimeout` (30 s in production) — a second `Rekey` arriving within that window hits the awaiting-reply branch.

**`ErrConnNotFound` wraps `control.ErrConnNotFound` via `%w`.** Slice A's dispatcher uses `errors.Is(err, control.ErrConnNotFound)` (not `==`) to map to `ErrCodeConnNotFound = "conn_not_found"` on the wire. `%w` keeps that mapping firing without any further plumbing on either side. `ErrSessionNotOpen` has no wire-code analogue today — slice A defined no `ErrCodeSessionNotOpen`; the control dispatcher surfaces it through `Response.Error` verbatim with no `ErrorCode`. Sentinels live in `internal/relay` so the wire-mapping layer can import them without leaking relay-internal state-machine vocabulary into `internal/control`. The `relay → control` import is non-cyclic.

**Timer rebase.** `handleManualRekey` calls `s.rekeyTimer.Stop()` and sets `s.rekeyTimer = nil` BEFORE the emit. `Stop()`'s bool return is intentionally ignored — see "Stale-wake benign race" below. The natural [#453](../codebase/453.md) responder cycle on the phone's reply re-arms a fresh `rekeyTimer` via `rekeyComplete` from the swap moment, not the previous emit moment, so the next scheduled emit lands at T_swap + `rekeyInterval`, never at the original boundary. On reply-timeout the conn closes at WS 4426 and the session is removed entirely.

**`emitRekeyRequest` refactor.** The function signature gained a `reason string` parameter; the body deltas are mechanically minimal (the struct literal's `Reason:` field, the `Info` log's `reason` value). Call sites: `handleWake`'s `wakeRekeyEmit` arm passes `"scheduled"`; `handleManualRekey` passes `"manual"`. **One emit function, two callers — no parallel emit machinery.** The AEAD-seal posture, the awaiting-defensive skip, the marshal-failure WARN, the `awaitingRekeyReply = true` set, and the `armRekeyReplyTimer` call all run on both paths byte-identically. Mobile Protocol v2's `payload.reason = "manual"` is wire-pinned by `docs/protocol-mobile.md` § Re-key as *"operator-triggered via `pyry rekey <conn_id>`"*; the literal is the only semantic difference between the scheduled and manual emits.

**Stale-wake benign race.** Sequence: scheduled `rekeyTimer` fires at T=0, the `AfterFunc` callback pushes `wakeSignal{s, wakeRekeyEmit}` onto `m.wake` (cap 16). `Run` picks up the `manualRekey` arm first (Go's `select` is fair-random). `handleManualRekey` runs `Stop()` (returns false — fired), nils the timer, runs the manual emit (which sets `awaitingRekeyReply = true`). On a subsequent `Run` iteration the stale wake is processed: `handleWake` → `wakeRekeyEmit` arm → `emitRekeyRequest(ctx, s, "scheduled")` → the defensive `awaitingRekeyReply` check catches it and logs `v2.rekey.emit.skipped_already_awaiting`. **No double emit; one spurious WARN.** The defensive skip stays in `emitRekeyRequest` precisely so this race remains benign on the scheduled path; the manual path's explicit pre-check makes the defensive skip structurally unreachable from `handleManualRekey` (the explicit check fires first).

**AEAD-seal-failure posture: pause-not-close.** A sub-emit failure on the manual path leaves the conn with `rekeyTimer = nil` and `awaitingRekeyReply = false` — automatic scheduled re-keying is paused indefinitely until a phone-initiated re-key rebases the timer via `rekeyComplete`. Acceptable per the architect's security review: seal failures are realistically unreachable under correct flynn/noise (same posture as `sealError`); the remediation is operator-visible (`v2.rekey.emit.seal_failed` log line) and the operator can re-run `pyry rekey <conn_id>` once slice B2 lands. Re-arming the timer in the seal-failure branch would mask the underlying error AND introduce a code path the scheduled emit doesn't have — extra surface area for an unobserved failure mode.

**Control-socket wire-up of `Rekey` is out of scope.** `NewV2SessionManager` gained its first production caller in [#549](../codebase/549.md) (the `PYRY_MOBILE_V2=1` daemon cutover constructs the manager and drives `Run`), but #549 deliberately does **not** call `ctrlServer.SetRekeyer(mgr)` — that is a named non-goal. So the `Rekey` method is still reachable from `internal/relay` tests only until the control-socket wire-up lands in a separate ticket. The sibling slice B2 ships the `pyry rekey <conn_id>` operator verb in `cmd/pyry`; once both B2 and the `SetRekeyer` wire-up land on top of #549's manager construction, the verb is end-to-end functional.

### Scheduled + manual rekey emit gated behind `transportDown()` (#912)

`security-sensitive` — extends [the #874 transport-down hold](#transport-down-hold-on-the-push-drain-874--connected-probe--transportdown) to `emitRekeyRequest`'s two callers (umbrella #829). #874 covered the push drain's seal-while-down hazard; the [scheduled](#initiator-side-scheduled-re-key-450--1-hour-timer--rekey_request-emit--30-s-reply-timeout) and [manual](#operator-driven-manual-re-key-462--rekey-method-satisfying-controlrekeyer) rekey emit paths shared the identical hazard and were fixed here. Before this ticket, a `wakeRekeyEmit` fire or `handleManualRekey` call while the relay transport was down sealed a `rekey_request` under `s.send` (burning a Noise send-nonce), `m.send` silently dropped it, and — on the scheduled path — `awaitingRekeyReply` + the 30s `rekeyReplyTimer` armed anyway: 30s later `wakeRekeyReplyTimeout` closed the session at `StatusHandshakeFailure` (4426) logging a `noise.rekey_failed` WARN that mislabelled a transport outage as a crypto failure, and even a transport recovery inside the 30s window still MAC-failed the phone's next decrypt (the burned nonce gapped its recv sequence), killing the conn at 4421 anyway.

**The gate sits at each call site, not inside `emitRekeyRequest`.** Both callers diverge after the check (scheduled re-arms a timer; manual returns a named error), and `handleManualRekey` stops-and-nils the scheduled `rekeyTimer` *before* calling `emitRekeyRequest` — a check living inside the primitive and returning "deferred" would leave that stop already applied with nothing to re-arm it. So `emitRekeyRequest` stays byte-identical except for one added doc-comment precondition line (callers MUST verify `transportDown()` first); this is a documented contract, not enforced code, acceptable because the seal site has exactly two callers, both edited by this ticket, and a redundant inline guard would need a return-signal the primitive doesn't have.

- **Scheduled (`handleWake`'s `wakeRekeyEmit` arm).** `transportDown()` true → seal nothing, arm no reply timer, re-arm `w.s.rekeyTimer = m.armRekeyRetryTimer(ctx, w.s)`, log one Info line (`v2.rekey.emit.deferred_transport_down`, conn_id + reason + retry_in — content-free), `return`. The fired 1-hour timer that delivered this wake is one-shot and inert, so overwriting `rekeyTimer` needs no `Stop()`. `armRekeyRetryTimer` is a new helper — the same `time.AfterFunc` + `select{m.wake<-…; <-ctx.Done()}` callback shape as `armRekeyTimer`, but at `rekeyRetryInterval` instead of `rekeyInterval`, pushing the *same* `wakeRekeyEmit` kind. Re-entering the existing arm (rather than adding a new wake kind) makes the deferral a self-healing bounded-retry loop: each retry fire re-checks `transportDown()` and either emits normally (recovered) or re-arms again (still down).
- **`rekeyRetryInterval = 1 * time.Minute`** — new lowercase package var, same test-overridable posture as `rekeyInterval`/`rekeyReplyTimeout`. Sits above the relay reconnect backoff ceiling (~30s, so a single retry usually lands on a recovered transport) and well under the 15-minute `idleTimeout`, so a permanently-down transport is reaped by [the idle sweep](#idle-session-teardown-774--per-session-idle-timer--in-repo-sweep) rather than spinning the retry loop forever on a dead session.
- **Manual (`handleManualRekey`).** New check inserted *after* the existing `ErrConnNotFound` / `ErrSessionNotOpen` eligibility guards but *before* the scheduled-timer `Stop()`: `if m.transportDown() { return ErrTransportDown }`. Ordering is load-bearing both directions — a missing or ineligible conn still gets its precise pre-existing error regardless of transport state, and on transport-down the scheduled 1-hour `rekeyTimer` is left completely untouched, so the session keeps its cadence and the operator simply sees "retry later."
- **`ErrTransportDown`** — new package-level sentinel (`errors.New`, mirrors the `ErrSessionNotOpen` pattern), distinct so the operator-facing error reads "transport down, retry" rather than the confusing "not open." No `internal/control` change needed: `handleRekey` already puts any non-nil `Rekey` error verbatim into `Response.Error`, mapping only `ErrConnNotFound` to a wire `ErrorCode`.
- **TOCTOU-free by the same single-owner-goroutine argument as #874.** Both gate sites and the seal itself run on the Run goroutine; `s.send` has one owner; `transportDown()` reads an immutable construction-time `func() bool` never mutated after `NewV2SessionManager`. No lock, no atomic. The accepted residual is the identical one-frame up→down transition race #874 already accepts — reduced from "every frame across the whole down window" to "at most one frame at the transition instant."
- **Inert when unwired or transport up.** `transportDown()` is false whenever `Connected` is up or nil, so every existing rekey test (constructed without `Connected`) is byte-for-byte unchanged from pre-#912 behavior.

Tests: `internal/relay/v2session_test.go`, four new cases reusing the #874 `gatedRecorder` togglable-`Connected` fixture — scheduled-deferred-then-emits-on-recovery (the `sess.initRecv` nonce oracle proves the post-recovery emit decrypts cleanly, i.e. no nonce was burned while down), no `noise.rekey_failed`/4426 while down (session stays `V2StateOpen`), manual rekey while down returns `ErrTransportDown` distinct from `ErrSessionNotOpen` with the scheduled timer preserved, and a transport-up regression variant of both happy-path tests proving the gate is inert. See [`codebase/912.md`](../codebase/912.md).

### Idle-session teardown (#774) — per-session idle timer + in-repo sweep

The relay↔binary leg is a **single multiplexed WebSocket**: every phone's frames arrive on one `Connection.Frames()` channel keyed by `conn_id`, and there is **no per-connection disconnect frame**. When a phone drops or backgrounds (the protocol says phones close-on-background, then push-to-wake — `docs/protocol-mobile.md:872`), the binary receives *nothing* for that `conn_id`. Before this slice the stale `V2Session` — two Noise `CipherState`s + an armed 1-hour `rekeyTimer` — lingered until the scheduled rekey fired (up to an hour later), went unanswered, and only then closed at 4426. Under normal connect/disconnect churn these idle encrypted sessions accumulated. This slice bounds their lifetime with an **in-repo idle sweep**: an open session that receives no inbound frame within `idleTimeout` is torn down through the existing [`closeWith`](#noise_msg-in-v2stateopen--application-dispatch-and-aead-failure-teardown) path. (This is the ticket body's "option b" — the daemon side of the gap the 2026-07-03 cross-repo review flagged; the cross-repo alternative, a relay-emitted per-connection disconnect signal, does not exist in this repo and is a deferred future ticket. See [§ Out of scope](#out-of-scope-deferred).)

**The design constraint (why this is more than a bare timer).** `V2Session` cipher-state access is single-Run-goroutine-owned with no mutex — *the loop is the lock*. So the idle mechanism reuses the **existing** `time.AfterFunc → wakeSignal → handleWake` machinery the #450 rekey timer established: the timer callback does the actual teardown decision **on the Run goroutine**, never inside the callback. No new package, no new exported type beyond the close code, no new goroutine topology, no signature changes → zero consumer fan-out.

**New package-level values (mirror the `rekeyInterval` idiom).**

- `idleTimeout` — lowercase, test-overridable `var` defaulting to **15 min**. Well short of the 1-hour `rekeyInterval` so a dropped/backgrounded phone's cipher states never linger up to an hour; long enough not to tear down a foregrounded-but-momentarily-quiet phone mid-read (which would force a disruptive re-handshake on the next tap). Not yet config-driven — the same deferred posture as `modalDenyTimeout` (still a package var, not config-exposed). Tests substitute a sub-second value via `prev := idleTimeout; idleTimeout = …; t.Cleanup(func() { idleTimeout = prev })`.
- `wakeIdleTimeout` — new unexported `wakeKind` const joining `wakeRekeyEmit` / `wakeRekeyReplyTimeout`; the per-session timer-event discriminator carried on `m.wake`.
- `StatusIdleTimeout websocket.StatusCode = 4408` — the idle-teardown WS close code, echoing HTTP 408 (Request Timeout), consistent with the existing 44xx←HTTP convention (4401←401, 4404←404, 4409←409, 4429←429). Added to the § Error codes table in `docs/protocol-mobile.md` (direction: binary, forwarded by relay).

**New per-session state on `V2Session`** (Run-goroutine-owned, no lock, same regime as `rekeyTimer`):

- `idleTimer *time.Timer` — armed at `V2StateOpen` alongside `rekeyTimer`, rescheduled by `handleWake` when activity is recent, stopped+nil'd by `closeWith`. Nil before open; nil after `closeWith`.
- `lastActivityAt time.Time` — stamped on **every** inbound frame in `handleFrame`. The idle deadline the timer chases. Run-owned; never crosses the wire, so the monotonic reading is retained (PROJECT-MEMORY's `time.Time` round-trip discipline does not apply).

**Mechanism: a single-arm timer that chases the last-activity deadline.** Chosen over "re-arm on every inbound frame" (keeps `handleFrame`'s hot path to a single field write — no per-frame `Stop`+re-alloc churn) and over "periodic sweep over the sessions map" (that needs a second duration + a separate channel; the AC asked for **one** test-overridable window in the `rekeyInterval` idiom). `armIdleTimer(ctx, s, d)` copies `armRekeyTimer`'s callback shape exactly — its `AfterFunc` callback does only a non-blocking `select { case m.wake <- wakeSignal{s, wakeIdleTimeout}: case <-ctx.Done(): }`, touching **only** `m.wake` (never `s.send`/`s.recv`/`s.state`/`m.sessions`), so it cannot race the cipher states.

```
1. Arm at open   — handleNoiseInit success tail, next to s.rekeyTimer = m.armRekeyTimer(ctx, s):
                     s.idleTimer = m.armIdleTimer(ctx, s, idleTimeout)
2. Stamp on frame — top of handleFrame, right after the V2StateClosed early-return
                     (a torn-down conn's late frame is dropped first): s.lastActivityAt = time.Now()
3. Fire → decide  — handleWake's wakeIdleTimeout arm, AFTER the existing `if state != Open { return }` guard:
                     idle := time.Since(s.lastActivityAt)
                     idle <  idleTimeout → reschedule for (idleTimeout - idle), return  ← a frame arrived after arm
                     idle >= idleTimeout → log v2.idle.teardown; closeWith(ctx, s, StatusIdleTimeout /*4408*/, nil)
4. Teardown       — closeWith (reused): state→Closed, stop+nil idleTimer alongside rekeyTimer/rekeyReplyTimer,
                     delete m.sessions[X] + m.queues[X], emit one close-only Outbound{ConnID:X, CloseCode:4408}
```

The reschedule re-points the timer at exactly `lastActivityAt + idleTimeout`, so an active session's idle timer fires ~once per `idleTimeout` and reschedules; an idle session's fires once and tears down. No per-frame timer churn.

**Stale-wake safety = two deterministic guards composed** (belt-and-suspenders where both layers are code, not a second stochastic timer):

- `w.s.state != V2StateOpen` (existing) — catches "`closeWith` ran between fire and wake" (e.g. a rekey failure tore the session down first).
- `idle < idleTimeout` (new) — catches "an inbound frame arrived after this timer was armed but before the wake was serviced"; `handleFrame` already re-stamped `lastActivityAt`, so the reschedule re-points the timer at the fresh deadline. **No active session is ever torn down by a stale wake.**

Both reads (`state`, `lastActivityAt`) and the act (`closeWith` / reschedule) happen in one synchronous `handleWake` call on the owning goroutine — no TOCTOU. On `Run` exit `runCtx` cancels, so a fired-but-undelivered idle callback releases via `ctx.Done` (no goroutine leak); an un-fired `AfterFunc` holds only a timer-heap entry.

**Reconnect after a sweep is clean.** `closeWith` deleted the session + queue, so a later frame on the same `conn_id` lazy-creates a fresh `V2StateAwaitingInit` session (existing `handleFrame` behaviour) and the returning phone re-handshakes from scratch — no stuck/half-torn-down state, no #647 replay reconciliation needed (a swept session is fully deleted, so there is no `last_event_id` continuity to preserve across a sweep). Sending 4408 to a `conn_id` whose phone already dropped is a harmless relay no-op.

**Security posture (spec-stage review verdict PASS).** The change is *net-positive* for secret hygiene: idle teardown **drops** the session's two Noise `CipherState`s (via `closeWith` → GC), bounding the in-memory lifetime of key material — the ticket's actual motivation. The timer is driven solely by the session's **own** inbound-frame cadence and is keyed by `*V2Session`: a phone can re-arm only its own timer (it sends frames only on the `conn_id` the relay assigned to its WS) and can neither force teardown of another phone's session nor prevent teardown of one. Keeping one's own session alive by using it is normal paired-device behaviour, not a bypass — the sweep targets *idle* sessions by design. The `v2.idle.teardown` log carries only `event` / `conn_id` / `close_code`; the fixed 4408 close code is not a per-session oracle; the close envelope carries `Frame == nil` (no sealed payload). See [codebase/774.md](../codebase/774.md).

### Concurrency-safe unsolicited push (#571) — `Push` method + `push` funnel

`Push(ctx context.Context, connID string, env protocol.Envelope) error` is the missing primitive behind every **server-initiated** delivery to a phone — the assistant's reply to `send_message`, the #632 structured stream, and any future push. It seals a caller-owned envelope under the addressed session's send CipherState, wraps it as the existing `noise_msg` transport frame, and forwards it. **Introduced as a synchronous funnel in [#571](../codebase/571.md); rewritten non-blocking in [#610](../codebase/610.md)** so a slow/stalled relay can never wedge the calling producer (the [#633](../codebase/633.md) JSONL drainer) — closing the ADR-025 § Backpressure open risk (decisions/025 line 220).

**The #610 reconciliation — buffer in front, seal stays single-writer.** `Push` no longer round-trips through `Run`. It enqueues the (still **unsealed**) envelope onto a per-session bounded FIFO (`m.queues[connID]`, a `pushQueue` guarded by the leaf mutex `m.pushMu`) and returns immediately; a non-blocking `m.drainCh <- struct{}{}` (cap-1, coalescing) wakes `Run`. `Run`'s `drainOnce` arm pops **one** envelope per pass and seals it via `forwardEnvelope` (the renamed `handlePush`) under the single-owner-goroutine invariant — so the seal never leaves `Run`, the nonce counter is never raced, and one slow `Outbound` interleaves with (never monopolises) `Run`'s servicing of inbound frames / `ActiveConns` / wakes. The **seal stays single-writer**; only the *enqueue* moved off `Run`. **Drop-before-seal is load-bearing:** the Noise send nonce is strictly sequential, so the queue holds unsealed envelopes and the drop policy runs **pre-seal** — dropping a *sealed* frame would gap the phone's `recv` nonce → MAC failure → 4421 close. `pushMu` is a leaf lock (taken alone, never held across an `Encrypt`/`m.send`/channel op); the `m.queues` key set is mutated only on `Run` (created at `V2StateOpen` in `handleNoiseInit`, deleted in `closeWith`), so `queue-exists ⟺ V2StateOpen`.

**Drop policy** (`pushQueue.enqueue`, under `pushMu`; `assistant_delta` is the only droppable class — the coarse `message` and every control type are never-drop):

| state at enqueue | incoming | action |
|---|---|---|
| below `pushQueueCap` (256) | any | append |
| at cap, a delta queued | delta or control | evict the **oldest** queued delta, then append (AC#2 newest text retained; AC#3 control admitted by evicting a droppable, never by dropping control) |
| at cap, all control | delta | drop the incoming delta (loss-tolerant) |
| at cap, all control | control | admit past nominal cap (documented **soft overflow** — see below) |

`enqueue` only removes existing entries and appends at the tail → the relative order of every survivor is preserved (AC#4); each drop bumps the per-session `dropped` counter (logged at debug, no app content).

**The all-control saturated state IS reachable (#911 corrected a stale premise).** The soft-overflow branch was originally justified by "unreachable in practice — the phone cannot drive control volume (push is server→phone only)." [`StreamBundle`](#debug-bundle-streaming-812--streambundle--bundleenvelopes--reassemblebundle)'s `debug_bundle_chunk`/`debug_bundle_done` frames are control-class, so one inbound `request_debug_bundle` **does** drive hundreds of never-droppable control events onto a connected-but-very-slow relay with zero interleaved text — falsifying that premise. What keeps it bounded is not `enqueue`'s drop policy (unchanged by #911) but [`handleDebugBundleRequest`'s per-conn in-flight gate](#inbound-debug-bundle-request-813--request_debug_bundle--debugbundler--streambundle) (`bundleInFlight`, #911): while a conn's queue still holds any bundle frame, a repeat `request_debug_bundle` on that conn is rejected before assembly, so a single conn accumulates at most one bundle's chunks (bounded ≈ 4/3 × archive, per the base64 `json.Marshal` per chunk) and a client retry loop can no longer stack unbounded memory. A single bundle can still ride past `pushQueueCap` via this soft-overflow branch — that is bounded and out of scope for #911, which eliminates cross-retry stacking, not single-bundle overflow.

**`Push` contract** (off-`Run`, non-blocking): `ctx.Err()` short-circuit at entry (the only remaining ctx dependency); else look up the queue under `pushMu` — `ErrConnNotFound` if absent — `enqueue`, signal `drainCh`, return `nil`. A drop is **not** an error. **Error-contract change (#610):** the public `Push` now collapses "session not open" into `ErrConnNotFound` (a not-open conn has no queue); `ErrSessionNotOpen` is no longer returned by `Push` (it stays a package sentinel for `Rekey` and the Run-side forward). The **`V2StateOpen` security gate moved to `forwardEnvelope`** on the drain side — a buffered push to a conn that closed/de-authed before drain is dropped there, never delivered to an un-authenticated peer. Both production callers ([#632](../codebase/632.md), [#589](../codebase/589.md)) only debug-log the error, so the collapse is invisible. `Push` stays a pure transport primitive: the caller owns `env` entirely (`Type`, `ID`, `TS`, `Payload`), and both the drop log and `forwardEnvelope`'s error log **MUST NOT** echo `env`, plaintext, ciphertext, or key bytes — only `conn_id`, the `dropped` count, and the `env.Type` class constant (the package's no-AEAD-bytes-in-logs discipline). `ErrConnNotFound` wraps `control.ErrConnNotFound` via `%w` so the wire-mapping `errors.Is` fires at both levels. See [`codebase/610.md`](../codebase/610.md) for the full design and [`codebase/571.md`](../codebase/571.md) for the original surface.

**AEAD-failure teardown** (tampered / replayed / truncated `noise_msg`): `s.recv.Decrypt` returns non-nil → log `v2.aead.fail` with `conn_id` + `close_code=4421` (NO error text — the underlying flynn/noise error may carry counter indices that aren't operator-actionable) → `closeWith(ctx, s, StatusProtocolMismatch, nil)`. `closeWith` emits a single close-only routing envelope and **deletes the session entry from `m.sessions`** — the next `noise_init` for the same `conn_id` lazy-creates a fresh `awaitingInit` with no carry-over CipherStates. The handler chain is structurally unreachable: the AEAD-decrypt branch returns before `dispatchAppFrame` is called.

**Why the outbound channel is not closed.** Closing on the sending side panics any goroutine the handler accidentally forked that retains the `*dispatch.Conn`. Since [#909](../codebase/909.md), `Run` drains `outbound` continuously via a two-arm select (`<-outbound` / `<-routeDone`) while `Route` runs, then does one final non-blocking residual drain (`default: return`) once `routeDone` fires. A handler that forks a sender *after* `dispatchAppFrame` returns — the one case the residual drain can't observe — writes into a leaked but capacity-bounded channel that the GC reclaims once the goroutine exits, the same forked-late-sender property [#446](../codebase/446.md) established, preserved verbatim. Handlers still MUST NOT retain `*dispatch.Conn` beyond the call, but (as of #909) the number of replies a handler emits *during* the call is no longer bounded by `handlerOutboundBuf`.

The `device` field on `V2Session` is set exactly once in the handshake token-accept branch (right before state advances to `V2StateOpen`) and is surfaced through `*dispatch.Conn.Auth()`. Same lifetime as v1's `Conn.auth` slot — revocation of the device after handshake does NOT tear down the active conn; this matches the v1 posture and is intentional. Revocation propagation for active conns is tracked as a separate concern.

The `peerStatic` field on `V2Session` (#452) is similarly set exactly once — at step 3a above, before any branch that calls `closeWith`. A token failure (or any later handshake-layer failure) tears the session down via `closeWith` and `delete(m.sessions, s.connID)`, which drops the captured field along with the session entry — a failed handshake leaves no peerStatic to compare against on a future re-key. The field is identity-bearing (the public-static of the paired peer); the doc-comment pins the **MUST NOT log** discipline so a future log-line refactor cannot relax it on the "but it's public" instinct — emitting it makes the binary log a parallel device registry for anyone with log-read access.

### Transport-down hold on the push drain (#874) — `Connected` probe + `transportDown`

`security-sensitive` — layer 1 of the reconnect-reliability design (umbrella #829; [immediate flush-on-reconnect](#immediate-flush-on-reconnect-875--reconnect-signal--connectionreconnected-fan-out) is layer 2, #875; new-connection catch-up is the separate reconcile-on-connect mechanism — the modal half of which landed in #877, see [Connect-time modal reconcile](#connect-time-modal-reconcile-877--outstandingmodals-seam--reconcilemodals)). The `queuedEnv` doc (above) explains why `pushQueue` holds envelopes **unsealed**: the Noise send nonce is strictly sequential, so a dropped-after-seal frame gaps the phone's `recv` nonce and MAC-fails the next delivered frame, tearing a still-live session down at 4421. Before #874 that invariant covered only the *enqueue* side — `drainOnce` still popped the head and `forwardEnvelope` sealed it before `send` learned the daemon↔relay transport was down (the transport error surfaces only *after* `m.send`, swallowed at debug as `v2 outbound drop`), so a reconnect blip inside the relay's 30-second grace could both lose the control envelope and burn a nonce for it.

**The fix moves the check before the pop, not the seal.** `V2SessionConfig.Connected func() bool` is an additive, nil-optional seam (same idiom as `Interrupter` / `QueueRemover`) — a level poll of the transport leg's live-conn state. The private helper `transportDown()` wraps it: `m.cfg.Connected != nil && !m.cfg.Connected()`. `drainOnce` calls `transportDown()` as its very first statement, off-lock and before `pushMu` — a `true` result returns immediately, popping nothing, sealing nothing, re-signalling nothing, leaving the head exactly as `enqueue` left it (unsealed, un-popped). Everything downstream — the replay-gate snapshot, the `pushMu`-guarded pop, `forwardEnvelope`, the `more`-re-signal — is byte-identical to pre-#874 once the guard passes.

**Hold is structural, not a runtime error tag.** The transport-down decision lives upstream of the pop; the session-level failures `forwardEnvelope` still drops (`ErrConnNotFound`, `ErrSessionNotOpen`, marshal/seal failure) live downstream of it, reached only once the probe has passed (i.e. only on the transport-up path). So a genuinely down transport can never masquerade as a session-level drop, and vice versa — the two postures can't conflate by construction.

**Lazy re-flush on its own.** A held head does not re-signal `drainCh` itself; absent any other wake source, only the *next* `Push` on any conn (the existing `drainCh` signal) re-enters `drainOnce`, and if the transport has recovered by then the held head drains in FIFO order. [#875](#immediate-flush-on-reconnect-875--reconnect-signal--connectionreconnected-fan-out) adds an eager wake source alongside this one — the reconnect edge itself — so a held head no longer waits on some unrelated `Push` to land.

**Residual single-frame TOCTOU (documented, not defended further).** The probe and the seal are two separate operations; the transport can drop between them. A probe-up → conn-drops → seal → send-drops sequence still burns one nonce, but only at the exact up→down transition instant — #874 shrinks the exposure from *every frame across the whole down window* to *at most one frame at the transition*, which is the layer-1 guarantee. Closing this fully would need a rekey/resync backstop; not pursued absent an observed transition-instant burn.

**Production wiring — two thin passthroughs.** `internal/transport/wssclient.go`'s `Client.IsConnected() bool` is a synchronous level poll (`false` iff `closeCh` is closed or `c.conn == nil`) agreeing with `Send`'s own live-conn predicate — distinct from the existing edge-triggered `Connected() <-chan struct{}` used to re-run the app handshake. `Close` does not nil `c.conn`, so `IsConnected` gates on `closeCh` too — a closed client reads down. `internal/relay/connection.go`'s `Connection.Connected() bool` is a one-line passthrough to `c.client.IsConnected()`. `cmd/pyry/relay.go`'s `startRelayV2` wires `Connected: conn.Connected` beside the existing `Outbound: conn.Send` in the `V2SessionConfig` literal.

**Scope: `drainOnce` only.** `drainReplayOnce` ([#777](#reconnect-replay-647--hellolast_event_id--ring-replay--resync)) and the one-shot sealed sends (dispatch replies, close frames, `emitRekeyRequest`) share the same `s.send` nonce sequence and the same seal-while-down hazard, but are inert during the #874 scenario — no replay tail and no inbound frames to reply to during a within-grace blip on the same surviving conn — so the push drain was the only *active* sealed-send path to fix. `transportDown()` is reusable; extending the guard to those paths is a one-line addition each if evidence later warrants it.

**Security review verdict: PASS, net-positive.** The new datum is a bare `bool` transport-liveness poll originating from the daemon's own transport client — never phone-controlled, never parsed, logged, or used in an authorization decision. It gates *timing* only: the `V2StateOpen` gate and per-conn addressing stay in `forwardEnvelope`, downstream of the probe. A wrong `Connected` value degrades only liveness (false-down holds until the next `Push`; false-up reproduces today's single-frame drop) — neither widens delivery. `nil` fails safe to the pre-#874 posture. No new log field, no new goroutine/lock, bounded by the existing `pushQueueCap`. See `docs/specs/architecture/874-v2-push-drain-hold-unsealed-while-transport-down.md` § Security review for the full adversarial pass.

Tests: `internal/relay/v2session_test.go` — `TestV2Session_Push_HeldWhileTransportDown_ReflushContiguous` (AC1/AC3/AC4: held while down, exactly-once FIFO delivery after recovery, both frames decrypt cleanly under the phone's `recv` state ⇒ contiguous nonce) and `TestV2Session_Push_HoldGatedOnProbeNotSendError` (AC2: probe-down holds regardless of send outcome; probe-up + send-failure still pops-and-drops, unchanged posture) — both via a `gatedRecorder` fixture (an atomic up/down flag driving both `outbound` and `connected()` in lockstep). `internal/transport/wssclient_test.go` — `TestClient_IsConnected` (false before dial, true after a live conn, false after `Close`, agrees with `Send`'s predicate). See [`codebase/874.md`](../codebase/874.md).

### Immediate flush-on-reconnect (#875) — `Reconnect` signal + `Connection.Reconnected()` fan-out

Layer 2 of the reconnect-reliability design (umbrella #829), follow-up to [the #874 transport-down hold](#transport-down-hold-on-the-push-drain-874--connected-probe--transportdown). #874 closed the drop/nonce-burn hazard but left the held head's re-flush **lazy**: it drains only when the *next* `Push` re-signals `drainCh`. With no subsequent push, a held `modal_shown` can sit past the relay's 30-second client grace and get eaten by the daemon's 2-minute deny-on-timeout. #875 adds an **eager** trigger: wake the drain the instant the transport reconnects, no intervening `Push` required.

**The reconnect edge cannot be observed directly by the manager.** `transport.Client.Connected() <-chan struct{}` is documented single-observer, and `relay.Connection.run()` is already that sole observer (it consumes the edge to re-enter `forwardFrames`). A second observer on the same channel would steal connect signals from the connection's own frame-forwarding loop. So the edge is fanned out one layer down instead:

- `relay.Connection` gains a `reconnected chan struct{}` field (cap-1, drop-on-full — same discipline as the transport's own `connectedCh`), initialised in both `Connect` and `connectWithClient`. In `run()`'s existing `case <-c.client.Connected():` arm, right after the "conn established" log and before the blocking `forwardFrames` call, a non-blocking send pokes `reconnected`. `(*Connection) Reconnected() <-chan struct{}` exposes it — the edge-triggered sibling of the #874 level-poll `Connected() bool`, same passthrough shape one layer up.
- `V2SessionConfig.Reconnect <-chan struct{}` is the new optional seam (nil ⇒ no new wake source, byte-identical to pre-#875). `Run` gains one select arm: on fire, it re-signals `drainCh` using the same non-blocking cap-1 idiom already used by `Push` (`v2session.go:1955`) and `drainOnce`'s own `more`-re-signal — it does **not** call `drainOnce` directly, so the existing drain arm keeps owning the single pop path and its FIFO self-re-signal.
- `cmd/pyry/relay.go`'s `V2SessionConfig` literal wires `Reconnect: conn.Reconnected()` beside the #874 `Connected: conn.Connected`.

**The arm wakes, it never seals.** `drainOnce` still calls `transportDown()` first, before the pop — a reconnect signal fired while the transport is still down (a redundant/duplicate edge, or a fresh drop racing the wake) wakes the drain but the probe holds the pop, burning no nonce. The #874 single-frame up→down TOCTOU is unchanged, not widened.

**No lost wakeup, no new goroutine.** `reconnected` and `drainCh` are both cap-1 buffered; a signal raised mid-work waits in the buffer, and a dropped *duplicate* signal is harmless because the pending one still triggers the drain, whose `more`→`drainCh` re-signal self-perpetuates until the held FIFO empties. Both non-blocking sends run on pre-existing goroutines (`Connection.run` and the manager's `Run`) into channels the same-side loop drains, so neither can wedge its host loop. `serve()` sets the transport's `c.conn` non-nil *before* signalling `connectedCh`, so `transportDown()` reads up by the time the manager's arm fires — the `drainOnce` it triggers pops, not holds.

**Not security-sensitive.** The edge changes wake timing only; every Noise send-nonce increment stays gated by the #874 `Connected` probe downstream, and the reconnect signal itself carries no attacker-influenceable data.

Tests: `internal/relay/v2session_test.go` — `TestV2Session_Push_FlushesOnReconnectSignal` (AC3 core: two held envelopes deliver in FIFO order, contiguous nonce, driven by a `reconnect <- struct{}{}` with **no** intervening `Push`), `TestV2Session_Push_NilReconnectInert` (AC1: `Reconnect: nil` behaves byte-identical to pre-#875 — held envelope waits for the next `Push`), `TestV2Session_Push_ReconnectWhileDownDoesNotSeal` (the arm wakes but `transportDown()` still holds — no nonce burned). `internal/relay/connection_test.go` — `TestTransportReconnect_SignalsReconnected` (mirrors `TestTransportDropPostConnect_Reconnects`; asserts `conn.Reconnected()` fires on both the initial connect and a post-drop reconnect). See [`codebase/875.md`](../codebase/875.md).

### Connect-time modal reconcile (#877) — `OutstandingModals` seam + `reconcileModals`

`security-sensitive` — the modal half of the reconcile-on-connect mechanism (umbrella #829; layered alongside [#874's transport-down hold](#transport-down-hold-on-the-push-drain-874--connected-probe--transportdown) and [#875's eager flush](#immediate-flush-on-reconnect-875--reconnect-signal--connectionreconnected-fan-out), both of which cover only a *surviving* conn — this is the first piece that brings a **brand-new** WS connection up to date). `modal_shown` is broadcast exactly once, at raise time (`cmd/pyry/interactive_modal_v2.go`'s `broadcastInteractive`), to the conns open at that instant; `EventID` is nil so it never enters the #647/#777 turn-event replay ring. A phone that connects or reconnects *after* that instant never learns a permission prompt is pending, and the prompt silently rides the daemon's 2-minute deny-on-timeout unseen.

**`OutstandingModals func() []protocol.ModalShownPayload`** is the new optional `V2SessionConfig` seam (nil ⇒ no reconcile, byte-identical to pre-#877). It is a closure, not a `*modalbridge.Registry` import: `internal/relay` does not import `internal/modalbridge`, and `protocol.ModalShownPayload` is already imported, so the boundary crosses with no new import and no cycle — the same idiom as `SnapshotSettings`/`SnapshotUsage`. Production wires `OutstandingModals: modalReg.Snapshot` (`cmd/pyry/relay.go`), the [#876](../codebase/876.md) current-truth read seam over the same daemon-singleton registry the raise-time producer `Record`s into. Since [#1065](../codebase/1065.md), `ModalShownPayload` (and the `Outstanding` it's snapshotted from) carries a `conversation_id` outbound scoping stamp — `reconcileModals` needed **no** change to replay it: it marshals the `Snapshot()` payload verbatim, so a reconnecting conn's re-sent `modal_shown` is scoped identically to the initial broadcast.

**`reconcileModals(ctx, s)`** is a structural sibling of [`broadcastModalDismissed`](#inbound-modal-control-727717--deny-on-timeout-725--modalresolver-seam--modal_dismissed-broadcast), minus the fan-out — it unicasts to exactly `s.connID` instead of every open interactive conn, and sources payloads from the current-truth snapshot instead of a dismissal:

- **Guard:** `if !s.interactive || m.cfg.OutstandingModals == nil { return }` — the capability gate and the unwired/foreground opt-out, same `s.interactive` flag `broadcastModalDismissed` gates on.
- **Snapshot once**, empty ⇒ nothing sent. A modal resolved before this point is simply absent from the snapshot — reconcile reflects current truth, so a resolved `modal_id` never resurfaces.
- **Per payload:** marshal, then `m.Push(ctx, s.connID, protocol.Envelope{ID: 1, Type: TypeModalShown, TS: ts, Payload: payload})` — `ID: 1` non-load-bearing (the phone correlates on `modal_id`, identical rationale to `broadcastModalDismissed`); `EventID` left nil (control event, never in the replay ring). A marshal or `Push` failure logs only content-free discriminants (`event`, `conn_id`, `modal_id`) and continues to the next payload; `ctx.Err() != nil` returns early (teardown).

**Call site:** one line in `handleNoiseInit`'s success tail, immediately before the [#647 replay hook](#reconnect-replay-647--hellolast_event_id--ring-replay--resync) — after `s.interactive`/`s.state = V2StateOpen` are set and after the conn's push queue exists (so `Push` never sees `ErrConnNotFound`). Ordering relative to `replayMissed` is immaterial: the reconcile's `modal_shown` lands in `m.queues` (held behind any reconnect-replay tail by [#777](#reconnect-replay-647--hellolast_event_id--ring-replay--resync), then drains), while `replayMissed` enqueues into the separate `replayQueue` — placed first only to document intent (surface the time-sensitive prompt ahead of replayed history).

**Avoids the raise-time emitter's send state.** `broadcastInteractive` (`cmd/pyry`) runs on the **producer** goroutine and advances an unguarded per-conn `nextID`; `reconcileModals` runs on the manager's **Run** goroutine. Reaching across that boundary would be a race for zero benefit — the modal control `ID` is non-load-bearing — so the reconcile is entirely relay-side with a fixed `ID`, no new cross-goroutine coupling, no lock added.

**Pure read — mints no nonce, re-arms no timer.** `OutstandingModals` never touches `newModalID` and `reconcileModals` calls no arm/resolve path, so the raise-time deny-on-timeout timer stands and a re-sent `modal_id` stays answerable exactly once, governed by the registry's one-shot `Resolve` (which this path never calls).

**Snapshot→deliver race (benign, self-healing).** A modal may resolve in the window between `OutstandingModals()` reading it and the phone receiving the re-send. The phone then holds a `modal_shown` for a now-resolved id; its later answer misses at `Resolve` → inert. Because any resolution after this conn opened also fans a `modal_dismissed` to it, and both frames traverse the same FIFO `m.queues` with the `modal_shown` enqueued first, the phone always observes shown-then-dismissed and clears the stale prompt — no ordering inversion is possible.

**Security review verdict: PASS.** Two existing gates keep the re-send from reaching an unauthorized peer: authentication (`reconcileModals` only runs after the Noise_IK handshake + device-token validation succeed, and `forwardEnvelope` re-checks `V2StateOpen` at seal time) and capability (`!s.interactive` withholds it exactly as it withholds `broadcastModalDismissed`). Unicast-not-broadcast is a *tighter* surface than `broadcastModalDismissed`'s fan-out. The re-sent payload carries no `answer_token` (that field exists only on the inbound `ModalAnswerPayload`) and no fresh nonce — `Snapshot()` mints nothing, so the nonce space is unchanged. The modal body is never logged; only `event`/`conn_id`/`modal_id` (an opaque nonce, not a secret) appear in the two defensive log branches. See `docs/specs/architecture/877-reconcile-modal-truth-on-connect.md` § Security review for the full adversarial pass.

Tests: `internal/relay/v2session_modalreconcile_test.go` (new) — one/two outstanding modals re-sent keyed by `modal_id`; unicast-only (opening conn B does not cause a second re-send to already-open conn A); non-interactive conn gets zero; nothing-outstanding sends zero; nil seam is inert; a modal resolved before open never resurfaces (real `modalbridge.Registry`); pure-read proof (snapshot unchanged across the re-send, first `Resolve` succeeds, second is inert). See [`codebase/877.md`](../codebase/877.md). The cross-layer e2e capstone lives in `internal/e2e/relay_v2_modal_reconnect_test.go` (`TestRelayV2_ModalReconcileOnReconnect`, `//go:build e2e`, #903): a real fake client disconnected then reconnected over a fresh Noise handshake sees a modal raised in its absence re-delivered exactly once with the same `modal_id`, and answers it exactly once — a genuinely driven second answer is observed rejected. See [`codebase/903.md`](../codebase/903.md).

### Connect-time queue reconcile (#878) — `OutstandingQueues` seam + `reconcileQueues`

`security-sensitive` — the queue half of the reconcile-on-connect mechanism (umbrella #829; twin of [Connect-time modal reconcile (#877)](#connect-time-modal-reconcile-877--outstandingmodals-seam--reconcilemodals) directly above, structurally near-identical). `queue_state` is pushed only on change ([#722](../codebase/722.md)'s `queueStateEmitterV2` fans a snapshot to every open interactive conn on enqueue/drain-advance/remove); a phone that connects or reconnects *between* changes never learns the current per-conversation backlog and sees a stale or empty view.

**`OutstandingQueues func() []protocol.QueueStatePayload`** is the sibling optional `V2SessionConfig` seam (nil ⇒ no reconcile, byte-identical to pre-#878). Same closure-not-concrete-import idiom as `OutstandingModals`: `internal/relay` does not import `internal/msgqueue`, and `protocol.QueueStatePayload` is already imported, so the boundary crosses with no new import and no cycle. Production wires `OutstandingQueues: outstandingQueues(queue)` (`cmd/pyry/relay.go`) — an adapter (`cmd/pyry/queue_state_v2.go`) that composes the new [`msgqueue.Queue.SnapshotAll`](msgqueue-package.md#snapshotall--connect-time-reconcile-enumeration-878) enumeration seam with the existing #722 `toQueueStatePayload` mapping. `queue` is the same live daemon queue the #722 producer snapshots on change and the #723 `QueueRemover` mutates — no new threading through `startRelay`/`startRelayV2`.

**`reconcileQueues(ctx, s)`** is `reconcileModals`'s structural twin, `protocol.TypeQueueState`/`QueueStatePayload` substituted for the modal type, keyed by `conversation_id` instead of `modal_id`:

- **Guard:** `if !s.interactive || m.cfg.OutstandingQueues == nil { return }` — identical shape to `reconcileModals`'s guard.
- **Snapshot once**, empty ⇒ nothing sent. `SnapshotAll` already omits any conversation whose backlog is empty, so an empty slice here means no conversation holds a backlog.
- **Per payload:** marshal, then `m.Push(ctx, s.connID, protocol.Envelope{ID: 1, Type: TypeQueueState, TS: ts, Payload: payload})` — `ID: 1` non-load-bearing (the phone correlates on `conversation_id` + `queued_msg_id`, matching the #722 producer's own posture); `EventID` left nil. A marshal or `Push` failure logs only content-free discriminants (`event`, `conn_id`, `conversation_id` — a non-secret routing id, **never** `text`) and continues; `ctx.Err() != nil` returns early.

**Call site:** one line in `handleNoiseInit`'s success tail, immediately after `m.reconcileModals(ctx, s)` and still before the [#647 replay hook](#reconnect-replay-647--hellolast_event_id--ring-replay--resync). Ordering relative to `reconcileModals` and `replayMissed` is immaterial to correctness (distinct payload types; `queue_state` sits in `m.queues` behind any replay tail the same way `modal_shown` does) — modal-then-queue is placed to surface the time-sensitive permission prompt ahead of the backlog, matching intent-documentation-only ordering the modal reconcile already established.

**Pure read — mints no id, dequeues nothing.** `SnapshotAll` never touches `Enqueue`/`Remove`/`nextID`, so the queued-message id space and FIFO order are untouched by a reconcile; re-sending the same snapshot is idempotent by construction (`queue_state` is already full-state, not a delta).

**Snapshot→deliver race (benign, self-healing).** A conversation may enqueue/drain/empty in the window between `SnapshotAll()` reading it and the phone receiving the re-send. The phone then holds a slightly stale full-state snapshot; because `queue_state` is idempotent full-state, the next #722 change re-emits current truth to this now-open conn, and the reset-on-reconnect client contract (filed alongside #878) makes an absent later snapshot mean empty — no ordering inversion, no lost clear (same reasoning shape as the modal reconcile's shown-then-dismissed race, adapted for full-state-not-event semantics).

**Security review verdict: PASS.** Same two gates as `reconcileModals`, applied to opaque queued `text` instead of a modal prompt body: authentication (Noise_IK + device-token validation gate `handleNoiseInit`'s success tail; `forwardEnvelope` re-checks `V2StateOpen` at seal time) and capability (`!s.interactive` withholds it, identical to the #722 producer's own gate). Unicast-to-`s.connID` is strictly narrower than the #722 producer's broadcast. Each payload's `conversation_id` and its items come from a single `SnapshotAll` map entry threaded through `toQueueStatePayload`, so a payload can never carry conversation A's id with conversation B's text; `SnapshotAll` enumerates the daemon's own map keys, never a caller-supplied id. No `answer_token`/nonce rides the payload; `SnapshotAll` mints nothing. Logging is content-free (`event`/`conn_id`/`conversation_id` only — **never** `text`, the payload bytes, or a raw `err`, since `encoding/json` could quote the untrusted `text` into its error). See `docs/specs/architecture/878-reconcile-queue-truth-on-connect.md` § Security review for the full adversarial pass.

Tests: `internal/relay/v2session_queuereconcile_test.go` (new, clones `v2session_modalreconcile_test.go`) — one/two outstanding conversations re-sent keyed by `conversation_id`; unicast-only (opening conn B does not cause a second re-send to already-open conn A); non-interactive conn gets zero; empty backlog produces no `queue_state` for that conversation; nil seam is inert; content-free logging on the marshal/push-drop branches (no `text` substring in any emitted log record). `internal/msgqueue/queue_test.go` gains the `SnapshotAll` table (two conversations, a drained-to-empty conversation omitted, empty queue, in-flight head included, value-copy isolation, `-race` against concurrent `Enqueue`/drain). `cmd/pyry/queue_state_v2_test.go` covers the `outstandingQueues` adapter. See [`codebase/878.md`](../codebase/878.md). The cross-layer e2e capstone lives in `internal/e2e/relay_v2_queue_reconnect_test.go` (`TestRelayV2_QueueReconcileOnReconnect`, `//go:build e2e`, #904): a real fake client disconnected then reconnected over a fresh Noise handshake sees a non-empty backlog populated while it was away re-sent as a `queue_state` snapshot exactly once with the same `queued_msg_id` set, and reconnecting again re-sends the identical snapshot idempotently, no duplication or corruption. Not `security-sensitive` (correctness / no-data-loss proof; the security-property half of the #829 split is the modal twin, #903). See [`codebase/904.md`](../codebase/904.md).

### Concurrency-safe open-session enumeration (#588) — `ActiveConnIDs` method + `snapshot` funnel

`ActiveConnIDs(ctx context.Context) []string` is the **enumeration** half of server-initiated fan-out: [`Push`](#concurrency-safe-unsolicited-push-571--push-method--push-funnel) can address one open session by `conn_id`, but cannot discover *which* sessions are open. `ActiveConnIDs` returns a snapshot of the `conn_id`s of every session currently in `V2StateOpen`, so a producer goroutine can fan an unsolicited frame out to all connected phones by calling `ActiveConnIDs` then `Push` per returned id. It is the v2 analog of v1's `dispatch.Dispatcher.ActiveConns()`, and the missing piece [#571](../codebase/571.md) deferred — consumed by the [#589](../codebase/589.md) assistant-turn bridge. See [`codebase/588.md`](../codebase/588.md).

It is the structural twin of `Push`/`Rekey`, the **fourth** instance of the single-writer funnel: a new unbuffered `snapshot chan snapshotReq` field + a sixth `Run` select arm route each request onto the single dispatch goroutine, where the private `handleActiveConns` (renamed from `handleActiveConnIDs` in #626) reads `m.sessions` under the single-owner-goroutine invariant — serialised by `Run`'s `select` against every map write (lazy-create, `delete` in `closeWith`, handshake/re-key state transitions). `ActiveConns`/`ActiveConnIDs` themselves do only channel I/O on the **caller's** goroutine (a `select` send onto `m.snapshot`, then a `select` receive on the per-request `reply chan []ActiveConn`, both with `ctx.Done` escape arms returning `nil`); the seal/marshal steps a `Push` would run are simply absent — a snapshot touches no CipherState. No new lock, no new goroutine, no new wire shape, no new exported type beyond the method.

```go
// The reply was widened from chan []string to chan []ActiveConn in #626; the
// handler below appends the negotiated interactive flag, and ActiveConnIDs
// projects back to []string. See § Capability negotiation (#626).
type snapshotReq struct {
    reply chan []ActiveConn // cap=1 per request; Run's reply send is non-blocking
}

// In Run's select, beside the m.drainCh arm:
case req := <-m.snapshot:
    req.reply <- m.handleActiveConns()

// handleActiveConns, on the Run goroutine — the only site reading m.sessions:
out := make([]ActiveConn, 0, len(m.sessions))
for connID, s := range m.sessions {
    if s.state == V2StateOpen {
        out = append(out, ActiveConn{ConnID: connID, Interactive: s.interactive})
    }
}
return out
```

**The `s.state == V2StateOpen` filter is the load-bearing security gate** (`security-sensitive`). Only an open session has had its token validated (in `handleNoiseInit`'s accept branch); a `V2StateHandshakeComplete` session holds CipherStates but never passed the token check, and is excluded — identical to the gate [`forwardEnvelope`](#concurrency-safe-unsolicited-push-571--push-method--push-funnel) enforces on the push-drain side (#610), so a server push never reaches an un-authenticated peer. **Belt-and-suspenders, different fabric:** even if this filter regressed, the consumer `Push`es per returned id — a non-open conn has no queue (`ErrConnNotFound`), and a conn that closes between enqueue and drain is caught by `forwardEnvelope`'s `V2StateOpen` re-check before sealing — two deterministic, independent code-level checks (enumeration filter + `forwardEnvelope` gate), neither a stochastic agent rule. A `V2StateClosed` session cannot appear: `closeWith` already `delete`d it from the map.

**Returns `[]string`, not `([]string, error)`.** A snapshot has no failure mode the caller can act on; the only non-completion (ctx cancelled, or `Run` already exited with `Frames` closed and no receiver on `m.snapshot`) returns `nil` — equivalent to "no open sessions" for the broadcast consumer, which fans out to nobody this round and re-enumerates on the next assistant turn. `nil` and an empty non-nil slice are both `len 0` and interchangeable. The result is an **unordered set** (Go's randomized map-iteration order); the handler does not sort (no AC requires it; the broadcast consumer fans out order-independently — paying O(n log n) on the single dispatch goroutine would buy nothing). `handleActiveConns` emits no log line and reads no secret-bearing field — the return holds only non-secret conn-id routing keys + the negotiated `interactive` bool, handed only to the in-process consumer, never to a wire.

**Widened to a capability-aware enumeration in #626.** The reply now carries `[]ActiveConn` (conn-id + the negotiated `interactive` flag), and `ActiveConnIDs` is a thin `[]string` projection over it; see the next subsection.

### Inbound screen-snapshot handler (#618) — `handleRequestSnapshot` + the render/push seam

`request_snapshot` is a v2 **control** envelope (phone → binary), intercepted in `dispatchAppFrame`'s discriminator switch **before** `dispatch.Route` (the same boundary `rekey_request` uses), and answered with a `screen_snapshot` (binary → phone) carrying the current claude screen rendered to plain text. It backs ADR 025's always-available, parser-independent live-view escape hatch — the floor of the safe-degradation strategy. **`security-sensitive`**: the handler accepts an inbound frame from a non-trusted party over an internet-exposed relay and returns rendered screen content; ADR 025 § Security model (line 141) deliberately keeps read-only screen viewing **outside** the per-device permission gate, but the dispatch-and-return path is the surface the spec-stage security review audited (verdict PASS). See [`codebase/618.md`](../codebase/618.md); the settings fields the reply now also carries are [`codebase/847.md`](../codebase/847.md) (wire vocabulary + accessor) wired by [`codebase/848.md`](../codebase/848.md); the context-window usage fields are [`codebase/856.md`](../codebase/856.md) (reader leaf) wired by [`codebase/857.md`](../codebase/857.md).

Five seams, smallest-blast-radius first:

- **Supervisor render seam.** `(*supervisor.Supervisor).ScreenSnapshot() (text string, live bool)` captures the live `*tuidriver.Session` under `sessMu` (mirroring `WriteUserTurn`), then renders `tuidriver.Render(sess.Snapshot(), 0, 0)` **inside the seal** — the raw VT100 bytes are consumed in the same expression and never named in pyrycode, so no claude-screen literal enters the package (`cmd/substrate-guard` stays green). `sess == nil` ⇒ `("", false)`. `0,0` selects tui-driver's 120×40 default, matching the daemon PTY's allocation in headless mode (`resizeOnce` only fires for a TTY stdin), so the render is 1:1. Total — no error path; non-blocking (a pointer read + a bounded in-memory render).
- **Consumer-declared seam.** The relay reaches the supervisor through the one-method `ScreenSnapshotter` interface (declared in the relay) and the `KnownConversation func(string) bool` closure (production: a `conversations.Registry` membership check). The relay imports neither `internal/supervisor` nor `internal/conversations` — both seams are behaviours passed in via `V2SessionConfig`, same shape as `Config.ValidateConversation`. The `Snapshotter == nil` branch below is what makes the seam degrade cleanly on the stream-json bootstrap path: `cmd/pyry/relay.go` wires it through a `screenSnapshotterOrNil` helper rather than assigning `w.sup` directly, converting the typed-nil `*supervisor.Supervisor` `Session.Supervisor()` returns on that path into a genuine nil interface value — a direct assignment would produce a non-nil interface over a nil pointer, skip this branch, and panic in `ScreenSnapshot()`. See [codebase/1101.md](../codebase/1101.md) and the [sessions-package.md typed-nil-in-interface note](sessions-package.md#runner-interface--runnerfactory-1077).
- **Settings read seam (#848).** `SnapshotSettings func() (model, effort string, yolo bool)` — a primitive-typed closure alongside `Snapshotter`/`KnownConversation`, the same shape as `DebugBundler` (#813). Production wires a closure over `*sessions.Pool.DefaultSettings` built at the composition root (`cmd/pyry/main.go`), decoding `SessionSettings` into three scalars there so `internal/relay` still imports no `internal/sessions` type (the discipline `transitionObserverSink`, `cmd/pyry/session_transition_v2.go:20-27`, states for `relay.go`). Settings-source == snapshot-source == the bootstrap session, so the reported values and the rendered screen describe the one session by construction — no runtime cross-check, no conversation-keyed variant (deliberately deferred alongside the snapshot's own single-bootstrap scope).
- **Usage read seam (#857).** `SnapshotUsage func() (usedTokens, windowTokens int)` — a primitive-typed closure alongside `SnapshotSettings`, same shape and same read timing. Unlike `SnapshotSettings`, production builds this closure in `cmd/pyry/relay.go` itself (**not** threaded from `main.go`): it needs no `internal/sessions` type, only `resolveOwnBootstrapJSONL` (already cmd/pyry-local, `interactive_turn_stream_v2.go`) and [`internal/contextwindow.Read`](contextwindow-package.md) (#856), which imports only `internal/agentrun/jsonl`. The closure builds a **dedicated** `resolveOwnBootstrapJSONL` instance — distinct from the turn-stream's own instance below, since the resolver is stateful and not safe for concurrent use — resolves the bootstrap transcript path via `sup.State().ChildPID`, and calls `Read`; any resolver error or `Read` open failure collapses to `Read("")`'s deterministic fresh-session report (`Usage{0, 200_000}`) rather than surfacing, so the seam is non-erroring by contract. Usage-source == snapshot-source == the bootstrap session, same invariant as settings, same deferred conversation-keyed variant. See [codebase/857.md § Predicted-vs-actual seam](../codebase/857.md) for why this reuses `resolveOwnBootstrapJSONL` rather than the `ResolveTranscript` seam `sessions-package.md` documents for growth-confirm.
- **The handler.** `handleRequestSnapshot(ctx, s, env)` runs on the single `Run` dispatch goroutine. Every branch pushes exactly one reply and returns (**AC #3** — never panics, hangs, or silently drops):

| Condition | Reply | Code | Retryable |
|---|---|---|---|
| Malformed payload / empty `conversation_id` | `error` | `conversation.not_found` | false |
| Unknown / foreign `conversation_id` (**AC #4**) | `error` | `conversation.not_found` | false |
| `KnownConversation == nil` (optional seam) | `error` | `conversation.not_found` | false |
| `Snapshotter == nil` (optional seam) | `error` | `server.binary_offline` | true |
| No live session (`live == false`, **AC #3**) | `error` | `server.binary_offline` | true |
| Happy path | `screen_snapshot{conversation_id, text, ts, model, effort, yolo, used_tokens, window_tokens}` | — | — |

The `conversation_id` is validated by `KnownConversation` **before any render** (AC #4): an unknown/foreign id renders nothing. A JSON decode failure is tolerated — it leaves `ConversationID == ""`, which collapses into the not-found branch; the decode error is **never echoed**. Error replies carry only a **static** message constant (`msgSnapshotConvNotFound` / `msgSnapshotOffline`), never the decode-error text or the attacker-controlled raw `conversation_id`.

`SnapshotSettings` (#848) and `SnapshotUsage` (#857) are read only on the success path, after the `live` check, immediately before the `json.Marshal` — neither read ever gates or alters an error reply. A nil `SnapshotSettings` (or a seam returning `("", "", false)`, the no-bootstrap case `Pool.DefaultSettings` collapses internally) leaves the three settings fields at their defaults, byte-identical to the pre-#848 zero-value reply — this is the explicit "never configured" contract a client must be able to read as "no per-session override, permissions enforced," not as an omitted/unset field (#847 dropped `omitempty` on all three for exactly this reason). A nil `SnapshotUsage` leaves `used_tokens`/`window_tokens` at `(0, 0)`, same "never configured" contract — and is distinct from a wired-but-fresh session, which reports `(0, 200000)`; a client reads `window_tokens == 0` as "usage unavailable," not as "0% used."

**Both success and error replies go through `m.forwardEnvelope`** (the renamed `handlePush`, [#610](../codebase/610.md)) — the single existing seal-and-forward path ([#571](../codebase/571.md)), no parallel send path. The public [`Push`](#concurrency-safe-unsolicited-push-571--push-method--push-funnel) is deliberately **NOT** called here: it would enqueue the reply onto the buffered push stream (subject to the drop policy and a deferred drain pass), whereas a snapshot reply is `InReplyTo`-correlated and must seal **immediately and in-line** on this same `Run` goroutine. The envelope `ID` is a fixed non-load-bearing `1` (mirrors `emitRekeyRequest`); the phone correlates on `InReplyTo`.

**Security / log discipline (specified + tested).** The rendered screen text is sensitive and is **NEVER** logged — mirrors the coarse bridge's chunk-bytes discipline (the v1 `assistant_turn.go`; the identical v2 emitter `assistant_turn_v2.go` was removed in [#699](../codebase/699.md)). The handler logs only `conn_id`, `conversation_id` (a non-sensitive UUID), and event names (`v2.snapshot.served`, and defensive `v2.snapshot.*_err` lines). `TestV2Session_OpenState_RequestSnapshot_NeverLogsScreenText` pins the invariant with a benign non-substrate sentinel; code-review should grep the handler for any log field carrying the rendered `text`.

**Concurrency.** No new goroutine, channel, or shutdown step — the handler runs only on `Run`. `ScreenSnapshot` takes `supervisor.sessMu` (leaf, pointer read) then a bounded in-memory render; `KnownConversation` takes a `conversations.Registry` RLock (leaf, bounded). Both are leaf locks in other packages, never nested with relay state. The TOCTOU window between `KnownConversation` and `ScreenSnapshot` is benign — a session that dies in the gap yields a deterministic `server.binary_offline`, not a crash or a stale render.

**Deferred (recorded in `codebase/618.md` § Out of scope):** per-conversation screen routing — this single-bootstrap-conversation phase validates *registry membership*, so any registered id renders the one live screen; the multi-conversation phase ([#596]+) MUST tighten the render to the named conversation's session. Also deferred: resized-foreground render dimensions, eager push on `stall_detected`, and request-flood rate-limiting.

### Inbound modal control (#727/#717) + deny-on-timeout (#725) — `ModalResolver` seam + `modal_dismissed` broadcast

`modal_answer` / `modal_cancel` are v2 **control** envelopes (phone → binary),
intercepted in `dispatchAppFrame`'s discriminator switch **before** `dispatch.Route`
(the same boundary `rekey_request` / `request_snapshot` use) — there is **no**
`dispatch.Route` handler. This is the **inbound** half of the daemon-side modal
bridge: the outbound half surfaces a modal to phones ([`modal_shown` + the
outstanding-modal registry](modalbridge-package.md), #716); this slice lets a
phone *resolve* it. The seam is the foundation #717 (gated `modal_answer`) and
#725 (deny-on-timeout) layer on; #727 proves it via `modal_cancel` (dismiss =
fail-safe deny). **`security-sensitive`**: an inbound untrusted frame mutates the
modal lifecycle and fans out a broadcast on the internet-exposed relay (spec-stage
security review, verdict PASS). See [`codebase/727.md`](../codebase/727.md).

Modal control is **fire-and-broadcast, not request/reply** — there is no reply to
the caller, so no decode error or attacker-controlled byte is ever echoed back.

- **`ModalResolver` consumer seam.** The relay declares the two-method interface
  (beside `ScreenSnapshotter`) and reaches the daemon's outstanding-modal state
  through it, so `internal/relay` imports neither `internal/supervisor`,
  `internal/modalbridge`, `internal/audit`, nor `cmd/pyry`. The `cmd/pyry`
  `modalResolverV2` (`cmd/pyry/modal_resolve_v2.go`) implements it: `ResolveCancel`
  does registry `Resolve` → supervisor `SendEsc` → `audit.Log({cancelled, remote})`;
  `ResolveAnswer` is the gated answer arm (#717 — `Lookup` → fail-closed gate →
  `option_id` classification → `Resolve` consume → safe-answer keystroke → audit).
  Wired in `cmd/pyry/relay.go`'s `startRelayV2`
  over the **daemon-singleton** `modalbridge.New()` registry (the same instance
  [#798](../codebase/798.md) live-wires the producer into).
- **`handleModalCancel`** — nil-resolver ⇒ debug-log + return (inert). Else decode
  `ModalCancelPayload` (a decode failure is tolerated → empty `modal_id` → the
  resolver's unknown-id no-op, never echoed), `ResolveCancel(modal_id, s.device)`;
  on `ok=false` return (unknown/already-resolved id → no keystroke, no audit, no
  broadcast — **AC-4**); on `ok=true` call `broadcastModalDismissed`.
- **`handleModalAnswer`** — symmetric to `handleModalCancel`. #727 shipped this
  arm with a deferred-no-op `ResolveAnswer` (always `ok=false`); #717 filled the
  gated answer arm in the resolver impl, so the broadcast line is now live: an
  authorized `modal_answer` returns `ok=true` with `Outcome` = the answered
  `option_id`, fanning the dismissal. The manager code is unchanged — #717 touched
  only `cmd/pyry/modal_resolve_v2.go` (see [`codebase/717.md`](../codebase/717.md)).
  An ungated / forged / stale answer still returns `ok=false` (no broadcast).
- **`broadcastModalDismissed(ctx, modalID, d)`** — the **load-bearing concurrency
  fact**: it fires from inside `dispatchAppFrame`, on the single `Run` goroutine,
  and **MUST NOT call `ActiveConns`** (which funnels its request *back* onto this
  same goroutine via `m.snapshot` → **deadlock**). Instead it reads `m.sessions`
  **directly** (the `handleActiveConns` pattern), filters `s.state == V2StateOpen
  && s.interactive` (the same #607 gate `modal_shown` rides), and `Push`es a
  `modal_dismissed{modal_id, outcome, source}` per conn. `Push` is
  `Run`-goroutine-safe — it touches only `m.queues` under `pushMu` and returns
  immediately (seal+forward on a later `Run` iteration via `drainOnce`), so the
  fan-out never blocks the dispatch goroutine. One shared `time.Now().UTC()`;
  envelope `ID: 1` is non-load-bearing (the phone correlates on `modal_id`;
  `modal_dismissed` is a control event, `EventID == nil`, never in the #647 ring,
  so no per-session counter is added to `V2Session`). A per-conn `Push` error
  (ctx teardown / `ErrConnNotFound` from a raced teardown) is debug-logged with
  the transport sentinel only and the fan-out continues — payload bytes are never
  logged.

The fan-out reaches *every* interactive conn, including ones that never saw this
modal's `modal_shown`; the payload carries only the opaque `modal_id` +
`cancelled`/`remote` (no modal body), so a conn with no matching outstanding modal
just ignores it. **`TestV2Session_ModalCancel_FanOut`** drives the cancel through
the real `Frames`/`Run` loop with three heads (two interactive, one not) — an
accidental `ActiveConns` call would hang it, making the test a *structural*
no-deadlock proof — and asserts the dismissal reaches both interactive heads and
neither the non-interactive one.

#### Deny-on-timeout (#725) — fail-closed safe-deny on an unanswered modal

The fail-closed safety net: if **no** authorized device answers within a bounded
window, the daemon **safe-denies** the modal rather than leave claude blocked
forever or risk a silent grant. It reuses `broadcastModalDismissed` unchanged and
adds a third `ModalResolver` arm (`ResolveTimeout`) plus a daemon-global timer
funnelled onto `Run`. **`security-sensitive`** (a timer on the permission surface;
spec-stage security review verdict PASS). See [`codebase/725.md`](../codebase/725.md).

Unlike `modal_answer`/`modal_cancel`, a timeout is **not** an inbound frame — it
originates internally and rides a new path:

- **Arm (off `Run`).** The producer surfacer (`interactiveModalEmitterV2.Handle`,
  cmd/pyry, live-wired by [#798](../codebase/798.md)) calls `(*V2SessionManager).ArmModalTimeout(ctx, modalID)`
  **immediately after `reg.Record`** — before the marshal/broadcast, so a modal that
  fails to marshal, or one surfaced to **zero** interactive conns, is still denied on
  the window (claude is blocked regardless of who is watching). `ArmModalTimeout` only
  calls `time.AfterFunc(modalDenyTimeout, cb)` and touches no `Run`-owned state, so it
  is safe off the `Run` goroutine. `modalDenyTimeout` is a package var (2 min default,
  test-overridable; ADR 025 specifies "a bounded window" but no number).
- **Funnel (`AfterFunc` callback → `Run`).** `cb` does
  `select { case m.modalTimeout <- modalID: case <-ctx.Done(): }` — the `armRekeyTimer`
  callback shape. `modalTimeout` is a **daemon-global** buffered (`wakeBufferSize`=16)
  channel keyed by `modal_id` (unlike `wake`, keyed by `*V2Session` — a modal is not
  bound to one conn). The `*time.Timer` is **deliberately discarded, never `Stop`ped**:
  the registry's one-shot `Resolve` is the idempotency gate, so a timer that fires after
  an answer/cancel already consumed the modal simply no-ops; an un-fired `AfterFunc`
  parks no goroutine, so leaving it un-`Stop`ped leaks nothing (avoids a
  `map[modalID]*time.Timer` + new lock for zero correctness gain).
- **Fire (on `Run`).** A new `Run` select arm
  `case modalID := <-m.modalTimeout: m.handleModalTimeout(runCtx, modalID)`.
  `handleModalTimeout` is a near-copy of `handleModalCancel`: nil-resolver ⇒ inert
  debug-log; else `ResolveTimeout(modalID)`; on `ok=false` return (already
  answered/cancelled — no keystroke, no audit, no broadcast); on `ok=true` call
  `broadcastModalDismissed` (the **same** fan-out, with `{denied_timeout, timeout}`).
- **`ResolveTimeout`** (cmd/pyry `modalResolverV2`) mirrors `ResolveCancel`: registry
  `Resolve` → best-effort `SendEsc` → `audit.Log({denied_timeout, timeout})` → return
  `{denied_timeout, timeout}, true`. Differences: **no device** (empty audit identity —
  the documented no-device-timeout case), and `denied_timeout`/`timeout` classification.

**Exactly-once is structural, not lock-defended.** Answer/cancel-resolution
(`handleModalAnswer`/`handleModalCancel`, via `m.cfg.Frames`) and timeout-resolution
(`handleModalTimeout`, via `m.modalTimeout`) are **both arms of the same `Run`
`select`** — serviced one at a time. Whichever `Run` services first consumes the modal
via the one-shot `Resolve`; the loser sees `ok=false` and no-ops. So an answer-vs-timeout
race cannot double-deny, double-broadcast, or double-audit; the registry mutex still
guards `Record` (surfacer goroutine) against `Resolve` (`Run`). The timeout leg **only
ever drives the deny keystroke**, never a grant — fail-closed by construction (ADR 025
§ Security model: "answered with the SAFE default (deny / ESC) … Never auto-grant").
**Live in production since [#798](../codebase/798.md)** wired the surfacer: a real
permission/trust modal now `Record`s an entry and arms this timer, so an unanswered modal is
safe-denied on the window even if it reached zero phones (net-positive availability — before
#798 nothing armed it in production). `TestV2Session_ModalTimeout_FanOut` proves the
off-`Run`-arm → on-`Run`-fire crossing under `-race`.

#### Stream-json approval bridge — the verdict arm (#1080)

`ResolveAnswer` gained a **second** actuation arm alongside the tui keystroke arm
above: a `modal_answer` for a **stream-json** permission request (a
[`permbridge`](permbridge-package.md)-parked completer, #1103) resolves that
completer to allow/deny instead of routing a keystroke — no on-screen modal exists
on the stream-json path, so there is nothing to press Esc/Enter into. Everything
up through the gate → classify → modalbridge-consume steps in `ResolveAnswer` is
**unchanged and runs first**, regardless of which arm actuates.

- **`streamApprovalBridge`** (`cmd/pyry/modal_resolve_v2.go`, co-located with
  `modalResolverV2`) is the join: it owns the `modal_id ⇄ tool_use_id`
  correlation neither `permbridge.Registry` nor `modalbridge.Registry` holds.
  `Surface(req permbridge.Request) (retire func())` — called from
  `internal/control/server.go`'s `handleApprove` via the new
  `SetApprovalSurfacer` seam (mirrors `SetRekeyer`/`SetApprovalRegistry`) —
  raises a parked approval as the **same** permission `modal_shown` clients
  already answer (via `modalbridge.PermissionRequestForClass` +
  `modal.Record`, so the 4-option/reject-once-default payload is
  byte-compatible by construction) and stores `byModal[modalID] = toolUseID`.
  `handleApprove` `defer`s the returned `retire` immediately after `Surface`
  returns, so it fires on **every** terminal `Await` path uniformly (answer,
  timeout, disconnect, shutdown).
- **`modalResolverV2.streamApprovals streamApprovalResolver`** — an optional
  nil-default field (the #1014 pattern; 18 test call sites stay untouched). At
  the actuate step, `ResolveAnswer` computes `allow :=
  devices.AuthorizeRemotePermission(dev, outcome)` **once** and dispatches:
  `r.streamApprovals != nil && r.streamApprovals.ResolveStream(modalID, allow,
  reasonRemoteDeny)`; only when that returns `false` (nil bridge, or `modalID`
  absent from `byModal` — not a stream approval) does the tui keystroke arm
  run. `ResolveStream` resolves `perm.Resolve(toolUseID, ...)` — `Allow`
  echoing the parked `Input` byte-verbatim, or the fixed content-free
  `reasonRemoteDeny` constant — and does **not** delete the correlation or
  consume modalbridge (both already handled elsewhere).
- **Single-arbiter dismissal, unconditional-delete correlation.** `retire`
  deletes `byModal[modalID]` **unconditionally** on every call, then
  `modal.Resolve(modalID)` — the modalbridge one-shot — decides whether *it*
  (not `retire`) already broadcast the dismissal: a miss means `ResolveAnswer`
  already consumed it (answer path, no second broadcast); a hit means
  timeout/disconnect/shutdown, so `retire` audits `denied_timeout` and
  broadcasts `modal_dismissed` itself (AC-3, no stale modal). The
  architect's spec originally gated the delete behind the `modal.Resolve`
  `ok` branch, which leaked `byModal` forever on every *answered* approval
  (that branch always misses on the answer path) — caught as a MUST FIX in
  the spec's own security review and fixed before code landed; a
  no-correlation-leak test on both the answer and timeout paths is the
  regression guard.
- **No new timer.** The stream modal deliberately does **not** call
  `ArmModalTimeout` — `permbridge`'s own registry-owned timer is the sole
  timeout authority (two timers would drift), and `ResolveTimeout` routes an
  Esc keystroke, which has no target on the stream-json path. `retire`
  (invoked promptly on `Await`'s return) is the client-dismissal backstop
  instead.
- **Wiring.** `startRelayV2` constructs the bridge over the **same**
  `*permbridge.Registry` `runSupervisor` created and the **same**
  `*modalbridge.Registry` the emitter/resolver already share, sets
  `modalResolver.streamApprovals = bridge` **before** `mgr.Run` starts (so no
  data race on the resolver field from an in-flight `modal_answer`), and
  returns `bridge.Surface` outward through `startRelay` to `main.go`, which
  calls `ctrl.SetApprovalSurfacer(surface)`. A nil `w.approvals`
  (foreground/v1/relay disabled) leaves `streamApprovals` nil and
  `SetApprovalSurfacer(nil)` — `handleApprove` still parks and blocks, just
  with no client-facing modal, the pre-#1080 behaviour.

See [permbridge-package.md](permbridge-package.md) for the registry primitive
this bridges, [control-plane.md § Approve](control-plane.md#approve-mcpapprove-verb--forward-to-permbridge-block-default-deny-1104)
for the `handleApprove`/`SetApprovalSurfacer` side, and
[codebase/1080.md](../codebase/1080.md) for the ticket record.

### Inbound interrupt (#707) — `Interrupter` seam + Esc routing

`interrupt` is a v2 **control** envelope (phone → binary), intercepted in
`dispatchAppFrame`'s discriminator switch **before** `dispatch.Route` (the same
boundary `rekey_request` / `request_snapshot` / `modal_cancel` use) — there is
**no** `dispatch.Route` handler. It is the **remote interrupt**: a paired phone's
equivalent of pressing **Esc** at the local terminal. The daemon maps it to the
internal neutral `turnevent.Cancel` command and routes it to the supervised claude
as a single Esc — claude's own interrupt. **`security-sensitive`**: it is the first
inbound frame whose authorization *is* the `interactive` capability (spec-stage
security review, verdict PASS). See [`codebase/707.md`](../codebase/707.md).

The frame carries **no payload** — a bare control frame, with no `conversation_id`,
no `modal_id` nonce, no `answer_token`, and no idempotency key (unlike modals). A
replayed `interrupt` simply sends another Esc (an Esc with no running turn is a
no-op in claude), so no nonce / dedup is needed.

- **`Interrupter` consumer seam.** The relay declares the one-method interface
  `Interrupter interface{ SendEsc() error }` (beside `ScreenSnapshotter` /
  `ModalResolver`) and reaches the keystroke surface through it, so `internal/relay`
  imports neither `internal/supervisor` nor `internal/streamsup` nor tui-driver.
  **Since #1121** the `V2SessionConfig.Interrupter` field is wired not to the
  bootstrap supervisor directly but to a `cmd/pyry`-side adapter,
  `activeInterrupter`, that resolves the **active conversation's bound runner**
  (`active.CurrentConversation()` → `CurrentSessionID` → `Pool.Lookup` →
  `sess.Runner()`, mirroring the follow-active `boundHost` resolution used by the
  turn/modal streams — see [conversation-session-binding.md](conversation-session-binding.md))
  and dispatches by concrete runner type: `*supervisor.Supervisor` via the sealed
  `SendEsc` (#726), `streamRunner` (the stream-json adapter) via `Interrupt`
  (#1120). `activeInterrupter.SendEsc()` keeps the seam's original method name even
  though the actuation is a per-runner interrupt, not literally an Esc — this
  interface doc already abstracted `SendEsc` as "claude's own interrupt," so
  `internal/relay` and this seam's own tests needed zero changes. Before #1121 the
  field was wired with one line, `Interrupter: sup` (the bootstrap supervisor) —
  a latent mis-routing bug: since #678 routed turns to per-conversation bound
  runners, an interrupt from a phone actuated the idle bootstrap child instead of
  the runner actually running the active conversation's turn. See
  [codebase/1121.md](../codebase/1121.md).
- **`handleInterrupt(s)`** — the only new logic. Runs on the manager's **single Run
  dispatch goroutine**, so the `s.interactive` read is lock-free under the package's
  single-owner invariant. The signature takes **only `s`** (no `ctx`, no `env`) — a
  documented deviation from the `(ctx, s, env)` sibling handlers: nothing to decode,
  no cancellable work, no reply, no broadcast (fire-and-forget). Order is
  load-bearing — **capability gate first**:
  1. **`if !s.interactive` → return** (no Esc). The new inbound capability gate
     (AC-2 negative path). A **one-line check, NOT a reusable inbound-gate
     abstraction** — `interrupt` is its only consumer (dequeue is ungated,
     `modal_answer` uses the per-device gate #702, `modal_cancel` a nonce), so a
     shared gate would be a one-consumer abstraction (YAGNI). The `s.interactive`
     flag is server-authoritative (#626) — set fail-closed from the daemon's
     `negotiateCapabilities`, never from the phone's raw advertisement, so a spoofed
     `capabilities` advertisement can never flip it.
  2. **`if m.cfg.Interrupter == nil`** → debug-log `v2.interrupt.inert`, return
     (foreground / pre-wire; mirrors `handleModalCancel`'s nil-resolver guard).
  3. **`m.cfg.Interrupter.SendEsc()`** — best-effort. An error (no live session /
     teardown) is `Warn`-logged (`v2.interrupt.keystroke_err`, `conn_id` + the
     supervisor sentinel only — never payload bytes, there are none) and tolerated;
     nothing to roll back.

`Cancel` is **declared vocabulary, not the live path** (the `modal_cancel`
precedent): the mobile `interrupt` routes to Esc directly via this seam, it does
**not** construct a `turnevent.Cancel` value — that mobile-wire → neutral-`Cancel`
translation is the future ACP adapter's job (#600). **Multi-phone:** any interactive
paired phone can send `interrupt`, and (since #1121) it actuates the **active
conversation's** bound runner rather than whichever child happens to be the
bootstrap supervisor — consistent with the broadcast fan-out model (a user's paired
devices are one trust domain, and there is one daemon-global `active` conversation).
**Residual scope:** the isolation boundary is still "active conversation," not
"sending conn's own conversation" — routing conn A's interrupt strictly to A's
conversation regardless of the global active is a larger per-connection isolation
design, not built here (#1121's spec flags this explicitly and rules it a
non-regression: today's pre-#1121 code routed every interrupt to bootstrap
regardless of conn, so #1121 strictly improves isolation without introducing a new
cross-conversation leak).

### Inbound new_session (#831) — `SessionStarter` seam + `/clear` routing

`new_session` is a v2 **control** envelope (phone → binary), intercepted in
`dispatchAppFrame`'s discriminator switch **before** `dispatch.Route` (beside
`TypeInterrupt`) — there is **no** `dispatch.Route` handler. It is the **remote
start-new-session**: a paired phone's equivalent of typing **`/clear`** at the
local terminal. The daemon routes it directly to the supervised claude as a
`/clear` via the sealed `supervisor.StartNewSession` seam (#830) — split from
#824, and structurally the `interrupt` (#707) shape one verb over.
**`security-sensitive`**: it reuses the `interactive`-capability-is-the-
authorization posture #707 established (spec-stage security review, verdict
PASS). See [`codebase/831.md`](../codebase/831.md).

The frame carries **no payload** — a bare control frame, with no
`conversation_id`, no `modal_id` nonce, no `answer_token`, and no idempotency
key (same shape as `interrupt` / `request_debug_bundle`). A replayed
`new_session` simply drives another `/clear` (harmless), so no nonce / dedup is
needed. **Unlike `interrupt` it maps to no neutral `turnevent` command** — there
is no announced ACP counterpart requiring one; a future ACP `session/new` would
add that translation additively, out of scope here.

- **`SessionStarter` consumer seam.** The relay declares the one-method
  interface `SessionStarter interface{ StartNewSession() error }` (beside
  `Interrupter`) and reaches the keystroke surface through it, so
  `internal/relay` imports neither `internal/supervisor` nor tui-driver.
  `*supervisor.Supervisor` satisfies it via the **sealed `StartNewSession`**
  (#830, shipped unwired for exactly this consumer) — **zero new supervisor
  code**. The optional nil-safe `V2SessionConfig.SessionStarter` field is wired
  in `cmd/pyry/relay.go`'s `startRelayV2` with one line, `SessionStarter: sup`
  (`sup` already in scope as `Interrupter` / `Snapshotter`).
- **`handleNewSession(s)`** — the only new logic, a line-for-line mirror of
  `handleInterrupt`. Runs on the manager's **single Run dispatch goroutine**, so
  the `s.interactive` read is lock-free under the package's single-owner
  invariant. The signature takes **only `s`** (no `ctx`, no `env`) — the same
  documented deviation `handleInterrupt` established: nothing to decode, no
  cancellable work, no reply, no broadcast (fire-and-forget). Order is
  load-bearing — **capability gate first**:
  1. **`if !s.interactive` → return** (no `/clear`). The inbound capability gate
     (AC #4 negative path). A **one-line check, NOT a reusable abstraction** —
     `new_session` / `interrupt` / `dequeue_message` each keep their own bare
     check (CODING-STYLE over-DRY). `s.interactive` is server-authoritative
     (#626) — set fail-closed from the daemon's `negotiateCapabilities`, never
     from the phone's raw advertisement.
  2. **`if m.cfg.SessionStarter == nil`** → debug-log `v2.new_session.inert`,
     return (foreground / pre-wire; mirrors `handleInterrupt`'s nil-`Interrupter`
     guard). AC #5.
  3. **`m.cfg.SessionStarter.StartNewSession()`** — best-effort. An error (no
     live session / mid-teardown → `ErrNoLiveSession`) is `Warn`-logged
     (`v2.new_session.keystroke_err`, `conn_id` + the supervisor sentinel only —
     never payload bytes or the rendered screen) and tolerated; nothing to roll
     back, no reply owed. AC #5.

**No new emitter, no ack path.** The client observes the resulting break
through the **pre-existing** `session_transition` marker: when `/clear` rotates
claude's session UUID, the rotation watcher fires `notifyTransition(ReasonClear)`
and the existing #656/#657 emitter fans `reason: "clear"` to every interactive
conn — this ticket builds neither, exactly as `interrupt` is fire-and-forget.
**Multi-phone:** any interactive paired phone can start a new session on the
single live claude — no per-conversation / per-connection binding, consistent
with the broadcast fan-out model (a user's paired devices are one trust
domain). Per-conversation `new_session` scoping in a multi-session world is a
future ticket.

### Inbound dequeue_message (#723) — `QueueRemover` seam → `msgqueue.Remove`

`dequeue_message` is a v2 **control** envelope (phone → binary, payload
`{conversation_id, queued_msg_id}`), intercepted in `dispatchAppFrame`'s
discriminator switch **before** `dispatch.Route` (the `interrupt` / `modal_cancel` /
`request_snapshot` boundary) — there is **no** `dispatch.Route` handler. It lets a
phone with an interactive session **cancel a queued `send_message` before it drains**
to claude: the daemon removes the named, not-yet-drained message from the live
`msgqueue` backlog, and the existing change seam refreshes the phone's `queue_state`.
**`security-sensitive`**: an inbound handler mutating daemon queue state from an
untrusted phone frame (mirror of `send_message`), and the **second** inbound frame
whose authorization *is* the `interactive` capability (after #707; spec-stage
security review, verdict PASS). See [`codebase/723.md`](../codebase/723.md).

- **`QueueRemover` consumer seam.** The relay declares the one-method interface
  `QueueRemover interface{ Remove(conversationID string, queuedMsgID uint64) bool }`
  (beside `Interrupter` / `ModalResolver`) and reaches the daemon-owned queue through
  it, so `internal/relay` imports neither `internal/msgqueue` nor `cmd/pyry`. The
  concrete `*msgqueue.Queue` satisfies it via the existing `Remove` (#719) — **zero
  new msgqueue code**. The optional nil-safe `V2SessionConfig.QueueRemover` field is
  wired in `cmd/pyry/relay.go`'s `startRelayV2` by widening the `queue` param to the
  concrete `*msgqueue.Queue` (which already flowed in as a `handlers.Enqueuer`) and
  adding one line, `QueueRemover: queue`.
- **`handleDequeueMessage(s, env)`** — the only new logic. Runs on the manager's
  **single Run dispatch goroutine**, so the `s.interactive` read is lock-free under
  the package's single-owner invariant. The signature takes **`(s, env)` — no `ctx`**
  — the documented `handleInterrupt` deviation from the `(ctx, s, env)` siblings:
  `Remove` takes no context, there is no `forwardEnvelope`, and `queue_state`
  convergence is decoupled onto the #722 emitter goroutine, so the handler owns no
  cancellable work. Order is **load-bearing — capability gate first**:
  1. **`if !s.interactive` → return** (no `Remove`, no mutation, no panic). The
     inbound capability gate (AC-3 negative path). `s.interactive` is
     server-authoritative (#626) — never the phone's raw advertisement. Still a
     **one-line check, NOT an abstraction** — a second consumer of the bare
     `s.interactive` check (after `interrupt`) does not justify a shared inbound-gate
     helper (CODING-STYLE over-DRY).
  2. **`if m.cfg.QueueRemover == nil`** → debug-log `v2.dequeue.inert`, return
     (foreground / pre-wire; mirrors `handleInterrupt`'s nil-`Interrupter` guard).
  3. **Decode tolerantly** (`_ = json.Unmarshal(env.Payload, &p)`): a decode failure
     leaves zero-value fields, which `Remove("", 0)` no-ops on. The decode error and
     the payload bytes are **never** echoed back to the phone or into a log (the
     never-echo discipline of `handleRequestSnapshot` / `handleModalCancel`).
  4. **`removed := m.cfg.QueueRemover.Remove(p.ConversationID, p.QueuedMsgID)`** — the
     one-line engine call. The untrusted `conversation_id` is passed **verbatim**;
     `Remove`'s `convID`-scoping is the security boundary (`Remove(A,…)` provably
     never touches conversation B's backlog).
  5. **Log only content-free discriminants** (`removed` → INFO `v2.dequeue.removed`,
     `!removed` → DEBUG `v2.dequeue.noop`; only `conn_id` / `conversation_id` /
     `queued_msg_id`, never `env.Payload`). **No reply, no broadcast.**

**`false` is success of a valid request, not an error (AC-2):** unknown-id,
already-delivered, and the in-flight (draining) head are treated identically — a
quiet no-op, never an error reply (`msgqueue.Remove` already encodes all four cases).
**AC-4 convergence is automatic, never re-emitted:** the handler emits nothing;
`Remove`'s `notify` fires the `OnChange` seam on a successful removal, and the #722
`queueStateEmitterV2` (the **sole** `queue_state` emitter) pushes the updated snapshot
to interactive phones. **No `KnownConversation`/`Route` membership gate** —
`dequeue_message` only mutates the named conversation's own existing backlog and
returns nothing, so the deterministic `convID`-scoping is self-validating; adding a
stochastic membership check would defend an unobserved failure mode and risk a
false-negative drop of a legitimate dequeue (Belt-and-Suspenders → different fabric;
the deliberate, reviewed choice in [`codebase/723.md`](../codebase/723.md) § Security).

### Capability negotiation on the handshake (#626) — `negotiateCapabilities` + `s.interactive` + capability-aware `ActiveConns`

The daemon-side **trust decision** [#607](../codebase/607.md) deferred. #607 landed the wire vocabulary (`CapabilityInteractive`, the `omitempty` `Capabilities []string` field on both `HelloClientPayload` and `HelloAckPayload`) but `handleNoiseInit` **decoded the phone's advertised capabilities and ignored them** — the `hello_ack` echoed nothing, the session recorded no capability state, and `ActiveConnIDs` returned every open conn with no capability filter. This slice closes that boundary; it is `security-sensitive` (the daemon deciding which internet-facing phones are *granted* the interactive capability) and is the enforcement half of ADR 025's deliberately split design: #607 = vocabulary (no trust), #626 = trust decision. See [`codebase/626.md`](../codebase/626.md).

**The authoritative supported set + the pure intersection.** The supported set is the daemon's own constant — **never** a mirror of the phone's claims:

```go
var supportedV2Capabilities = []string{protocol.CapabilityInteractive}

// advertised ∩ supportedV2Capabilities, in supported-set order. Iterates the
// SUPPORTED set (not the advertised one), so the result is a subset of supported
// by construction: dedups, drops the unsupported/spoofed, and yields nil for
// advertise-nothing / only-unsupported.
func negotiateCapabilities(advertised []string) []string {
    var out []string
    for _, name := range supportedV2Capabilities { // `name`, not `cap` (builtin)
        if slices.Contains(advertised, name) { out = append(out, name) }
    }
    return out
}
```

**Iterating the supported set (not the advertised set) is the security primitive** — "a spoofed capability can never be granted" is a structural property of the loop shape, deterministic, not a runtime guard a later refactor could bypass (Threat 1 / AC#3). A pure receiver-less function, directly table-testable for the whole negotiation matrix.

**Echo + record, single source of truth.** `negotiated := negotiateCapabilities(helloPayload.Capabilities)` is computed **before** the `hello_ack` literal, and `Capabilities: negotiated` is added to the existing `HelloAckPayload{…}`. The ack is sealed via `WriteResp` **before** the token check, so it is built on *every* handshake — `omitempty` keeps the key absent for a no-capability phone (v1 byte-stability, AC#5). The per-conn `interactive bool` (a new `V2Session` field beside `device`/`peerStatic`, same set-once / single-owner-goroutine discipline) is set in the token-OK tail, **between `s.device = &device` and `s.state = V2StateOpen`**, via `s.interactive = slices.Contains(negotiated, protocol.CapabilityInteractive)` — derived from the *same* `negotiated` slice the ack echoed, so the echoed capability and the recorded flag can never disagree.

**Fail-closed, two independent gates.** The flag is written **only** on the token-OK branch (after `Devices.Validate`, before `V2StateOpen`); every other path leaves it at its `false` zero value (advertise-nothing / `null` / `[]` / only-unsupported → `negotiated == nil` → `false`). And `handleActiveConns` filters on `V2StateOpen` — so even if the flag were mis-set, a non-open (un-authenticated) session is never enumerated, and the negotiated flag of an un-authenticated peer is never observable. Belt-and-suspenders of different fabric — two deterministic code-level gates, the same shape #588/#589 established. Re-key (`handleRekeyInit`) preserves `s.interactive` by never touching it, like `device`/`peerStatic`.

**Token-fail ack echo grants nothing.** On a token-fail handshake the `noise_resp` carries the ack (with `negotiated`) to a cryptographically-authenticated-but-unauthorized peer that is then closed at 4401 and deleted from `m.sessions`. The echo grants nothing (the session never opens, is never enumerated or pushed-to) and leaks nothing (`CapabilityInteractive` is public protocol vocabulary). Restructuring to build the ack after the token check would break the pinned "state → `HandshakeComplete` before token validation" invariant for zero security gain — accepted non-issue (spec § Security review, verdict PASS).

**`ActiveConns`/`ActiveConn` — the capability-aware enumeration.** The downstream consumer reads the negotiated flag via the [widened snapshot funnel](#concurrency-safe-open-session-enumeration-588--activeconnids-method--snapshot-funnel): `ActiveConns(ctx) []ActiveConn` returns each open conn paired with its `Interactive` flag, and `ActiveConnIDs` is a thin `[]string` projection (with an explicit `nil` in → `nil` out short-circuit that preserves #588's nil-on-cancel contract). The #596 structured-stream fan-out (since [#699](../codebase/699.md) the **single** v2 assistant-turn delivery path — #589's coarse `message` broadcast, re-targeted to non-interactive conns in [#634](../codebase/634.md), was deleted as dead code once the 2026-06-22 ADR 025 amendment made every phone `interactive`; the `Interactive` flag survives because the structured path still gates on it) consumes this: [`#632`](../codebase/632.md)'s `interactiveTurnEmitterV2` (`cmd/pyry/interactive_turn_v2.go`) snapshots `ActiveConns`, filters `Interactive == true` (the load-bearing capability gate, `if !c.Interactive { continue }`), then `Push`es a sealed structured envelope per conn-id — the emitter is built and unit-tested, with #633 wiring it to the live producer. [`#657`](../codebase/657.md)'s `sessionTransitionEmitterV2` (`cmd/pyry/session_transition_v2.go`) is the **second** interactive-only consumer of this exact primitive: it reuses the same `interactiveBroadcaster` interface and `if !c.Interactive { continue }` gate to fan a `session_transition` envelope (a `/clear` rotation or idle/cap eviction surfaced by [#659]'s pool observer) to interactive phones — but stamps **no** `EventID` (a session boundary is not a turn-stream event, so it does not join the #647 ring) and adds no manager-side path. [`#722`](../codebase/722.md)'s `queueStateEmitterV2` (`cmd/pyry/queue_state_v2.go`) is the **third**: it fans a `queue_state` envelope (the inbound `msgqueue` backlog for one conversation, snapshotted on each [#719] `OnChange`) to interactive phones through the same reused interface + gate, likewise `EventID`-nil (idempotent full-state, off the #647 ring, a never-drop control event). Its one wrinkle — the `OnChange` seam fires from a *mix* of goroutines (the enqueue/remove callers on the manager Run goroutine, each drain on its own), so an inline `ActiveConns` would deadlock Run — is solved the same way #657 solves it: a buffered hand-off channel + dedicated Run goroutine, so the blocking `ActiveConns` runs off the dispatch goroutine. [`#1008`](../codebase/1008.md)'s `sessionErrorEmitterV2` (`cmd/pyry/session_error_v2.go`) is the **fourth**: it fans a `session_error` envelope (terminal `CodeSessionBlocked`, `SessionErrorPayload{ConversationID,Code,Message}` per [#1007](../codebase/1007.md)) to interactive phones whenever [#1000](../codebase/1000.md)'s `msgqueue` give-up seam (`OnGiveUp`) fires — through the same reused interface + gate, likewise `EventID`-nil (a give-up is a one-shot edge with no connect-time reconcile, unlike a replay-eligible event). Structurally near-identical to `queue_state`'s hand-off-channel + dedicated-Run-goroutine shape, **minus the queue reference**: the emitter holds no `Snapshot`/`*msgqueue.Queue` field, so queued (untrusted) phone text is unreachable by construction rather than by discipline. The single-`Interactive`-bool shape is the right shape while `supportedV2Capabilities` has one member (YAGNI); a second capability is a deliberate, separately-reviewed change.

### Reconnect replay (#647) — `hello.last_event_id` → ring replay / resync

> **Note (#663).** #647 shipped (PR #651, merged 2026-06-08) with a code-review
> MUST FIX outstanding: the caught-up branch of `replayMissed` set the dedup
> watermark from the *untrusted* `last_event_id`, silently suppressing the live
> stream after a `/clear`-rotated reconnect or a hostile-large id.
> [#663](../codebase/663.md) resolved it — the caught-up watermark is now clamped
> to `min(afterID, NewestID(convID))`, so the behaviour below is the shipped
> guarantee. (Defect history: [codebase/647.md](../codebase/647.md) § Known issue.)

The inbound **consumer** of mid-turn replay (ADR 025 § Backpressure / replay). It
closes the loop opened by the [#646](../codebase/646.md) event ring (the replay
source) and [#649](../codebase/649.md)'s `event_id` on the outbound wire (the
position a phone learns). A phone
that reconnects mid-turn advertises the last durable `event_id` it saw as
`hello.last_event_id` (`HelloClientPayload.LastEventID *uint64`, omitempty); the
manager replays the missed tail on that conn **before** the live stream resumes,
or emits a `resync` marker if the position aged out of the bounded ring.

- **The replay source is late-bound, not a config field.** `emitter` ↔ `manager`
  is a construction cycle (the emitter takes the manager as its broadcaster; the
  replay path needs the emitter-owned `eventring.Ring`, created *inside* the
  emitter constructor — [#646](../codebase/646.md)). `SetReplaySource(ring, currentConv)` publishes
  the ring + the `func() string` conversation cursor to the manager once during
  wiring, after the emitter exists. As of [#687](../codebase/687.md) the cursor
  is the `cmd/pyry` active-conversation signal (`active.CurrentConversation`), not
  `sup.CurrentConversation` (the #312 bootstrap cursor) — #678 routes turns to
  bound-session supervisors, leaving the bootstrap cursor empty, so the replay
  path re-keys to the same active-conversation signal as the live emitter or it
  re-introduces the empty-cursor drop on the reconnect-replay path. Stored under
  the existing `pushMu` leaf lock; nil ⇒ replay disabled. One call site, in
  [`startInteractiveTurnStreamV2`](../codebase/633.md). (This is the inbound
  mirror of #646's "emitter-owns-the-ring retires the constructor cascade".)
  Note `cursor()` here runs on the **manager's** `Run` goroutine — a distinct
  goroutine from the live emitter's reader; the holder's mutex makes that safe.
- **`replayMissed` classifies inline on `Run`, then hands the tail to a paced
  drain (#777).** At the `handleNoiseInit` success tail — after `noise_resp` is
  sent, `state == V2StateOpen`, and the push queue exists — the hook fires iff
  `helloPayload.LastEventID != nil`. It reads `(ring, cursor)` under `pushMu`,
  resolves `convID := cursor()` (returns early on nil source or empty cursor),
  then classifies via [`eventring.Ring.After`](eventring-package.md):
  - **replay** `(events, false)` → store the tail on the Run-owned
    `s.replayQueue []eventring.Event` and signal `m.replayCh` (cap-1,
    non-blocking); return. `drainReplayOnce` then forwards **one** event per `Run`
    pass — ascending, each carrying its original `EventID`, sealed under the fresh
    session keys via `forwardEnvelope`, advancing `s.replayThrough = ev.ID` per
    frame. **Why paced, not inline (#777):** the old inline loop sealed + forwarded
    the *entire* tail (up to `MaxEventsPerConversation = 1024` events) in one `Run`
    pass, monopolising the dispatch goroutine until the whole batch finished — a
    large replay stalled every other conn's delivery, inbound frames, wakes, and
    snapshots. Pacing mirrors the `drainOnce` push-drain pump so `Run` returns to
    its select between frames (another conn is delayed by at most one seal/forward).
    **Ordering is now preserved by a gate, not by inline completion:** while
    `s.replayQueue` is non-empty, `drainOnce` **skips** this conn (holding its live
    push queue buffered), and `drainReplayOnce` signals `drainCh` to release those
    live events once the tail empties — so replay ids (≤ `newest`, ascending) still
    reach the wire before live ids (> `newest`). The seal stays single-writer:
    every `s.send.Encrypt` is still on `Run`, only the *interleaving* of forwards
    with the select changed (see [codebase/777.md](../codebase/777.md)).
  - **caught-up** `(nil, false)` → no replay frames; the watermark is clamped to
    `min(afterID, NewestID(convID))` (#663, read before `After`) so an
    out-of-range / hostile `last_event_id` cannot mute the subsequent live stream.
  - **gap** `(nil, true)` → `emitResync` forwards one `resync` marker
    (`TypeResync`, inline `{conversation_id}` payload, no `EventID`), never a
    partial gap-ful replay.
- **`replayThrough` per-conn watermark + `forwardEnvelope` guard.** A Run-owned
  `replayThrough uint64` on `V2Session` records the highest `event_id` delivered
  by replay; `forwardEnvelope` drops a live structured envelope whose
  `EventID <= replayThrough`, deterministically de-duplicating the transient
  replay/live overlap (a proven race, not speculative — see [codebase/647.md](../codebase/647.md)
  § Concurrency model). Envelopes with `EventID == nil` (snapshot, error, rekey,
  resync) are never dropped; conns that never advertised `last_event_id` keep
  `replayThrough == 0` and live ids are ≥ 1, so the guard is inert for them. The
  watermark is "different fabric" from the phone's own `event_id` dedup (defence
  in depth). The watermark is only ever set to a *real* ring id: the paced replay
  drain (`drainReplayOnce`, #777) advances it per forwarded frame, and the
  caught-up branch clamps it to `min(afterID, NewestID(convID))` (#663) — a remote
  `last_event_id` beyond the conversation's id space can never raise it above the
  newest retained id. Both writers run on `Run`, so `replayThrough` keeps its
  single-owner-goroutine regime.
- **Untrusted input.** `last_event_id` is range/shape-validated by the `*uint64`
  decode (a non-integer fails `HelloClientPayload` decode → existing 4421 close),
  bounded by `MaxEventsPerConversation`, and scoped to the daemon-resolved
  `convID` — a phone can never name another conversation (AC-5; pinned by the
  cursor→B / ring-holds-A test). The replay hook sits *after* Noise IK auth + the
  device-token check, so content is only ever served to an authenticated conn.

### Debug-bundle streaming (#812) — `StreamBundle` + `bundleEnvelopes` + `ReassembleBundle`

`StreamBundle(ctx, connID, blob) error` moves an arbitrarily-large `[]byte` (the
[#811 debug-bundle assembler](debugbundle-package.md)'s output, routinely
multi-MB) to one addressed, open, authenticated conn as **ordered, cap-respecting
chunks ending in a completion marker**. It is the transport primitive the #813
request verb drives (wired in [§ Inbound debug-bundle request](#inbound-debug-bundle-request-813--request_debug_bundle--debugbundler--streambundle));
this slice shipped it **unwired**, test-driven only. New
file `internal/relay/v2bundlestream.go` holds the whole byte-stream concept —
the chunk-size const, the pure chunker, the method, and the reassembly reference
— depending only on `internal/protocol` + the in-package `maxNoisePayloadBytes`.
`security-sensitive` (content bytes sealed as AEAD frames on the mobile surface);
architect + code-review security passes both **PASS**. See
[`codebase/812.md`](../codebase/812.md).

**Two walls a bundle cannot ride a normal reply through, and why `Push` clears
both.** A `noise_msg`'s decoded ciphertext is capped at `maxNoisePayloadBytes =
65535` — a multi-MB blob is not one sealed frame — and the per-frame
handler-reply channel (`handlerOutboundBuf = 8`) still means one handler
invocation, however many replies it emits, occupies `Run`'s dispatch loop for
its whole duration (routing a multi-MB blob through it would stall every other
`conn_id` for the streaming duration — [#909](../codebase/909.md) fixed the
>8-reply *deadlock* on this channel, not this head-of-line cost). `StreamBundle` builds
N+1 envelopes with `bundleEnvelopes` and enqueues each via
[`Push`](#concurrency-safe-unsolicited-push-571--push-method--push-funnel) — the
manager's **asynchronous** `Push` → `drainOnce` → `forwardEnvelope` path, the same
path every unsolicited daemon → client frame uses (#571 push, #618 snapshot, #647
resync). `Push` never blocks and never touches `s.send`, so `StreamBundle` is
callable from any goroutine, **including a future #813 handler on the `Run`
goroutine** (its cap-1 `drainCh` signal is non-blocking) — sidestepping both walls
with zero new concurrency surface. It returns on **enqueue**, not delivery, and
returns the first `Push` error and stops (a not-open conn → `ErrConnNotFound`).

**`bundleEnvelopes(blob) ([]protocol.Envelope, error)`** (pure, no manager) splits
`blob` into `ceil(len/bundleChunkBytes)` `TypeDebugBundleChunk` envelopes with
ascending 0-based `Seq`, then one trailing `TypeDebugBundleDone{Total: n}`. An
**empty blob** → 0 chunks + `done{total:0}` (a valid stream reassembling to
empty). `EventID` is left **nil** on every envelope, so
[`forwardEnvelope`](#concurrency-safe-unsolicited-push-571--push-method--push-funnel)'s
reconnect-replay dedup guard is inert for bundle frames; `ID` is non-load-bearing
(set to `Seq`/`Total` for debuggability — the receiver keys on Type+Seq+Total).

**`bundleChunkBytes = 48000`** bounds the raw bundle bytes per chunk with ~1.3 KB
headroom below the frame cap (base64 ×4/3 + ~200 B `Envelope` JSON wrapper + 16 B
AEAD tag → ~64216 B ciphertext < 65535). **Belt-and-suspenders, different
fabric:** the conservative const is the belt; `TestStreamBundle_EveryFrameWithinCap`
(decrypts every emitted frame, asserts `len(ciphertext) <= maxNoisePayloadBytes`)
is the deterministic suspenders. The const's doc-comment pins the rule — **if the
cap test ever fails, LOWER the const, never raise it.**

**No drop, in order (AC#2).** Bundle frames are **control-class** (`Type !=
TypeAssistantDelta`), so the `pushQueue` drop policy never evicts them — under cap
pressure they soft-overflow. Per-conn FIFO + sequential seal on the single `Run`
goroutine ⇒ chunks arrive in enqueue order with a monotonic Noise send-nonce; the
`done` marker, enqueued last, seals last. An open session that closes mid-stream
has its still-buffered chunks dropped by `forwardEnvelope`'s `V2StateOpen` gate at
drain time — never sealed for an un-authenticated peer.

**`ReassembleBundle(frames) ([]byte, error)`** is the exported, pure
**receiver-contract reference and test oracle** — production use is phone-side
(out of repo); the daemon has **no inbound caller**. It walks `frames` in arrival
order: a `TypeDebugBundleChunk` must carry `Seq ==` the count of chunks already
seen (0-based contiguous ascending, else reorder/gap/duplicate is rejected); a
`TypeDebugBundleDone` must carry `Total ==` that count (else truncated /
count-mismatch is rejected); any other type is **skipped** (tolerates an
interleaved `assistant_delta`); frames exhausted with no marker is incomplete. On
**any** failure it returns `(nil, err)` — never partial or corrupt bytes. **Two
independent integrity nets, different fabric:** the AEAD (ChaChaPoly) guarantees
per-frame *content* integrity on the wire (intra-chunk corruption is impossible),
while `Seq` + `Total` add deterministic *structural* gap / reorder / truncation
detection. It exists in-repo because you cannot test "fail cleanly on a truncated
/ reordered stream" (the correctness note) without a reassembler to fail.

**Log discipline (AC#4).** `StreamBundle` logs one content-free debug line
(`event=v2.bundle.stream`, `conn_id`, `chunks`, `bytes` — counts only, never the
streamed bytes or their base64); `Push` / `drainOnce` / `forwardEnvelope` already
never log payload/ciphertext/key bytes. `TestStreamBundle_NoBytesLogged` seeds a
recognizable byte pattern, captures under a `LevelDebug` handler, first asserts
the `v2.bundle.stream` line **is** present (non-vacuous), then asserts the blob
bytes and their base64 appear in no record. The wire vocabulary
(`TypeDebugBundleChunk` / `TypeDebugBundleDone` + the two payloads) lives in
[`internal/protocol`](protocol-package.md#debug-bundle-streaming-payloads-812) and
[protocol-mobile.md § Debug bundle](../../protocol-mobile.md#debug-bundle-v2).

### Inbound debug-bundle request (#813) — `request_debug_bundle` → `DebugBundler` → `StreamBundle`

`request_debug_bundle` is a v2 **control** envelope (phone → binary), intercepted
in `dispatchAppFrame`'s discriminator switch **before** `dispatch.Route` (the same
boundary `interrupt` / `request_snapshot` / `dequeue_message` use) — there is **no**
`dispatch.Route` handler. It is the **capstone** of the debug-bundle feature: it
wires the two previously-unwired siblings into one paired-client request/response
flow — assembling the daemon-global bundle via [#811's `debugbundle.Assemble`](debugbundle-package.md)
(injected as a closure) and streaming it back via [#812's `StreamBundle`](#debug-bundle-streaming-812--streambundle--bundleenvelopes--reassemblebundle)
as `debug_bundle_chunk*` + `debug_bundle_done`. **`security-sensitive`**: the first
wire-reachable path that emits the terminal recording — the highest-value secret
surface in the system — to a remote client (spec-stage security review, verdict
PASS). See [`codebase/813.md`](../codebase/813.md).

The frame carries **no payload** — a bare control frame, like `interrupt`. The
bundle is daemon-global by construction (the whole log ring, which has no
per-session key, plus the newest recording across all sessions, per #811), so
there is no attacker-controlled field — no `conversation_id`, no path, no id — that
flows into assembly or the wire, and no argument that could select another
session's data.

- **`DebugBundler` consumer seam.** The relay declares the optional nil-safe field
  `V2SessionConfig.DebugBundler func() (archive []byte, err error)` (beside
  `Snapshotter` / `QueueRemover`). The closure returns **only** `(archive, err)` —
  never the `Manifest`, which travels inside the archive as `manifest.json` — so
  `internal/relay` imports neither `internal/debugbundle` nor `cmd/pyry`. It is
  wired in `cmd/pyry`: `main.go` builds `func() ([]byte, error) { archive, _, err :=
  debugbundle.Assemble(recordingsDir, logRing.Snapshot()); return archive, err }`
  and threads one new `debugBundler` param through `startRelay` → `startRelayV2`
  (the v1 leg ignores it — debug bundle is v2-only). `recordingsDir` is resolved by
  the existing `resolveRecordingsDir()` **independent of the `DebugCapture` flag** —
  old recordings persist and stay readable after capture is off, and `Assemble`
  marks the recording absent when the dir is empty.
- **`handleDebugBundleRequest(ctx, s, env)`** — the structural twin of
  `handleRequestSnapshot`. Runs on the manager's **single Run dispatch goroutine**;
  `StreamBundle` → `Push` enqueues under the `pushMu` leaf lock without touching
  `s.send`, so calling it inline is safe (this is exactly the "callable from a
  future #813 handler on the `Run` goroutine" the #812 docstring green-lit). Every
  branch either enqueues one bundle stream or sends exactly one error reply, then
  returns — it never panics, hangs, or silently drops:
  1. **`m.cfg.DebugBundler == nil`** → deterministic error reply (feature
     unavailable / foreground / v1 / unwired), return.
  2. **`archive, err := m.cfg.DebugBundler()`; `err != nil`** → log the failure
     **event only** (`v2.bundle.assemble_err`, `conn_id` — **never** the wrapped
     err, which could quote a recording path/filename) at warn, error reply, return.
     Honours #811's read-failure honesty: a recording that exists but fails to read
     surfaces as an error, not a false-absent.
  3. **`m.StreamBundle(ctx, s.connID, archive)`** → enqueue the chunk stream. An
     `ErrConnNotFound` (unreachable for an open `s` on the Run goroutine) is
     debug-logged and dropped (the package's outbound-drop posture); never the
     archive.
  4. **On success**, one content-free info log (`v2.bundle.served`, `conn_id` +
     `len(archive)` — a **byte count**, never the archive or any member) — AC #4.
- **`debugBundleReplyError(ctx, s, inReplyTo)`** — sends one `TypeError` reply via
  the same `m.forwardEnvelope` seal-and-forward path (never `c.Send`), with a
  **fixed** `CodeServerBinaryOffline` + `retryable: true` + the static
  `msgDebugBundleUnavailable = "debug bundle unavailable"` message. The only failure
  this verb reports is "unavailable", so **no** attacker-influenced or assembly-error
  text ever reaches the wire.

**Per-conn in-flight gate (#911, `security-sensitive`) — bounds repeated requests
to one bundle's chunks per conn.** A reliability review found that every bundle
frame is control-class, so `pushQueue.enqueue`'s drop policy never evicts it — a
retry against a slow/stalled transport (bounded by the 10 s `WriteTimeout`;
`drainOnce` forwards one frame per `Run` pass) stacked another full bundle's
chunks onto the queue with each retry, growing daemon memory without limit. The
fix is a request-time check, inserted into `handleDebugBundleRequest` **before**
`m.cfg.DebugBundler()`, so a busy retry skips assembly entirely, not merely the
enqueue:

```go
if m.bundleInFlight(s.connID) {
    m.debugBundleReplyError(ctx, s, env.ID)
    return
}
```

`bundleInFlight(connID string) bool` locks `pushMu` alone, scans `m.queues[connID].items`
for any envelope whose `Type` is `protocol.TypeDebugBundleChunk` **or**
`protocol.TypeDebugBundleDone`, and returns whether one was found — a pure
`O(len(items))` read released before the reply, honouring the leaf lock's "never
held across an external call" invariant. Both types are scanned because
`StreamBundle` enqueues all N chunks plus the trailing `debug_bundle_done` in one
call and `drainOnce` pops one per pass — during the drain the queue holds a
shrinking suffix that always includes the done marker until the very last pop, so
the gate clears exactly when the whole bundle (including the marker) has drained.
An unknown conn (`!ok`) returns `false` — the safe direction, since a non-open
conn's `StreamBundle`/`Push` fails closed with `ErrConnNotFound` anyway.

The busy-reject reuses `debugBundleReplyError` unchanged — byte-identical to the
nil-bundler and assembly-error branches, so the phone cannot distinguish "busy"
from "offline"/"assemble-failed"; no new wire code, no queue-depth oracle. The
check-and-act is serialized: `bundleInFlight` and `drainOnce` (the sole
queue-drainer) both run on the single `Run` goroutine, so nothing empties this
conn's queue between the scan and the reply. The only off-`Run` mutator is
`Push`, which can only *append* — it can turn a `false` into a `true` (more
conservative), never clear a `true` — so there is no TOCTOU that admits a second
bundle. This closes the specific amplification the [drop-policy §](#concurrency-safe-unsolicited-push-571--push-method--push-funnel)
above documents: one small untrusted inbound frame could otherwise drive an
unbounded batch of never-droppable outbound frames. See [`codebase/911.md`](../codebase/911.md).

**No new authorization gate — and, unlike `interrupt`/`dequeue_message`, NOT gated
on the `interactive` capability.** Authorization is **pairing**, enforced
structurally at the Noise IK handshake: an unpaired device is refused with WS 4401
and never reaches `dispatchAppFrame` (AC-3, verified by a test, not a new gate). Any
device that reached `V2StateOpen` is a paired device — the authorization the ticket
specifies — so a non-interactive paired client (e.g. a desktop diagnostic tool) may
request a bundle. The bundle reaches **only** the requesting conn
(`StreamBundle(ctx, s.connID, …)`), never a broadcast. **Capture-off *withholds*
the recording structurally** (AC-2): with an empty recordings dir the archive has
no `recording.cast` member — nothing to withhold, not a flag the client could
ignore.

### Inbound `set_session_settings` (#845) — `SettingsUpdater` seam, validate, persist, reply

`set_session_settings` is a v2 **control** envelope (phone → binary),
intercepted in `dispatchAppFrame`'s discriminator switch **before**
`dispatch.Route` — same boundary as `interrupt` / `request_snapshot` /
`request_debug_bundle`, no `dispatch.Route` handler. It consumes [#844's wire
vocabulary](protocol-package.md#session-settings-payloads-844)
(`SetSessionSettingsPayload` / `SessionSettingsUpdatedPayload` /
`CodeSessionNotFound`) and is the **daemon-side write path** for a paired
client's per-session `model` / `effort` / `yolo` — the untrusted-value
validation #833 explicitly deferred to this wire-owning ticket.
**`security-sensitive`**: the first inbound control verb that both persists a
mutation from untrusted input AND owes the caller a reply. Change takes effect
on the session's **next spawn** (#833's `claudeSettingsArgs` argv path);
making a *running* session pick it up immediately is sibling #842. See
[`codebase/845.md`](../codebase/845.md).

Unlike the fire-and-forget verbs (`interrupt` / `new_session` /
`dequeue_message`), this verb **always replies** on the interactive path —
success or failure, never a silent drop. Control flow, in load-bearing order:

1. **Capability gate.** `if !s.interactive { return }` — a non-interactive
   conn is fully inert: no decode, no seam call, **no reply**. It must not
   even learn whether the named session exists (a bare check, matching
   `handleInterrupt` — not a reusable inbound-gate abstraction).
2. **Decode.** `json.Unmarshal(env.Payload, &SetSessionSettingsPayload{})`.
   Because this verb owes a reply (unlike the fire-and-forget verbs), a
   decode failure yields a `protocol.malformed` reply, not a silent drop.
   Never echoes the decode error or any payload byte —
   `encoding/json` quotes attacker bytes into its error string.
3. **Validate `model`/`effort` before any persistence** — an invalid value
   yields a malformed reply, never a persisted bad setting (the
   argv-injection defense, below). `yolo` needs no value check: a malformed
   `yolo` already failed step 2's type-decode, so bypass can never be
   inferred from a bad value.
4. **Nil-seam guard.** `m.cfg.SettingsUpdater == nil` → deterministic
   `server.binary_offline` "unavailable" reply (foreground / unwired), never
   a silent drop.
5. **Persist + reply.** `SettingsUpdater.UpdateSettings(sessionID, update)`:
   `nil` error → `session_settings_updated` success reply (echoes only the
   client's own confirmed-real `session_id`, safe because it matched a real
   session key on the nil-error path); `errors.Is(err, ErrSessionUnknown)` →
   `session.not_found`; any other error → `server.binary_offline` (the
   persist-failure event is logged — `event`, `conn_id`, `session_id` only,
   **never** `err.Error()`, which can quote a path).

- **`SettingsUpdater` / `SettingsUpdate` / `ErrSessionUnknown` consumer seam**
  (beside `Interrupter` / `SessionStarter` / `QueueRemover`). `SettingsUpdate{
  Model, Effort *string; YOLO *bool}` mirrors `sessions.SettingsUpdate` 1:1 so
  `internal/relay` imports neither `internal/sessions` nor `cmd/pyry`; the
  optional `V2SessionConfig.SettingsUpdater` field is nil-safe (nil ⇒
  "unavailable" reply, never drop). `cmd/pyry`'s `settingsUpdaterAdapter{p
  *sessions.Pool}` is the **sole** place `sessions.ErrSessionNotFound` maps
  onto `relay.ErrSessionUnknown` — the project's sentinel-to-wire-mapping
  convention (mapping lives at the consumer call site, not the primitive).
  Wired by threading a `settings relay.SettingsUpdater` param through
  `startRelay` → `startRelayV2` from the single `main.go` call site
  (`settingsUpdaterAdapter{pool}`, `pool` already in scope alongside
  `sessionMinter{pool}`).
- **`validModel(m string) bool`** — a **shape check, not an allowlist** (model
  names churn per release; a fixed allowlist would force a code edit per
  launch). `""` accepted (clears to template default — `claudeSettingsArgs`
  emits no `--model` for it); otherwise 1–64 bytes, first byte alphanumeric,
  every byte in `[A-Za-z0-9._-]`. This is the **argv-injection defense** for
  the threat #833 deferred: values reach claude as separate argv tokens
  (`--model <value>`) under `exec.CommandContext` with no shell, so the
  residual risk is a leading-dash value confusing claude's own flag parser —
  closed by the first-byte-alphanumeric rule.
- **`validEffort(e string) bool`** — `""` (clear) or the closed enum `{low,
  medium, high, xhigh, max}`, matching `cmd/pyry/agent_run.go`'s
  `validEfforts` but defined relay-local since `internal/relay` cannot import
  `cmd/pyry`.
- **`settingsReplyError(ctx, s, inReplyTo, code, message, retryable)`** — a
  third near-identical copy of the `snapshotReplyError` /
  `debugBundleReplyError` shape (marshal `protocol.ErrorPayload` →
  `Envelope{Type: TypeError, InReplyTo}` → `forwardEnvelope`); the established
  per-handler-owns-its-helper posture, not extracted into a shared helper.
  `message` is always one of three fixed constants
  (`msgSettingsMalformed` / `msgSettingsNotFound` / `msgSettingsUnavailable`)
  — never attacker-influenced bytes.

**YOLO fail-safe (the primary asset).** Three deterministic layers, no
stochastic component: (1) the `*bool` presence contract — an absent `yolo`
decodes to `nil` and `UpdateSettings` leaves the stored value untouched; (2) a
malformed `yolo` value fails the step-2 payload type-decode before the seam is
ever touched; (3) only an explicit `"yolo":true` sets `*true`, and the whole
apply is one atomic `saveLocked` (#840) — no partial-parse path can reach
`YOLO=*true`. Belt-and-suspenders is different fabric here: the safe default
is pointer-nil semantics (code) and the corruption guard is `json.Unmarshal`
strictness (code), not a second stochastic check.

## Concurrency

**One owner goroutine + transient `time.AfterFunc` callbacks routed through a wake channel.** `Run` is the only goroutine the manager owns long-term. It reads `cfg.Frames`, looks up (or lazily creates) `m.sessions[env.ConnID]`, processes the frame synchronously, and ALSO pops `wakeSignal` values from a per-manager buffered channel `m.wake` and dispatches them via `handleWake`. `m.sessions` is mutated exclusively by `Run`; no mutex.

The #450 timer plumbing introduces transient `time.AfterFunc`-spawned goroutines (one per fire, never per session — `time.AfterFunc` only spawns when the timer fires). The callbacks DO NOT touch session state directly: they push a `wakeSignal{s, kind}` onto `m.wake` and exit. The single-owner-goroutine invariant for `s.send` / `s.recv` / `s.state` / `s.device` / `s.peerStatic` / `s.interactive` / `s.awaitingRekeyReply` / `s.rekeyTimer` / `s.rekeyReplyTimer` / `s.idleTimer` (#774) / `s.lastActivityAt` (#774) / `s.replayThrough` (#647/#663) / `s.replayQueue` (#777, the paced-replay tail drained one frame per `Run` pass by `drainReplayOnce`) is structurally preserved — only `Run` reads or writes those fields. The callback closure selects on `(m.wake <- signal, <-runCtx.Done())` so a fired-but-undelivered wake on a shutting-down `Run` exits via the ctx branch without leaking; pinned by `TestV2Session_RekeyInitiator_TimerCleanup_NoGoroutineLeak`. `Run` derives `runCtx, cancelRun := context.WithCancel(ctx); defer cancelRun()` so a `Frames`-channel-close exit (which doesn't cancel `ctx`) still cancels `runCtx` and unblocks any pending callback.

`V2Session` carries no lock. The package contract is "one goroutine per `conn_id` mutates the session"; today that goroutine is `Run` itself. flynn/noise's `CipherState` carries a mutable 64-bit nonce counter; concurrent access would be UB — the serialisation point IS the lock.

As of [#965](../codebase/965.md), each open session also owns a long-lived per-conn worker goroutine (`appFrameWorker`) plus at most one short-lived `Route` goroutine at a time (spawned by `routeAppFrame`, one per application frame — [#909](../codebase/909.md)'s concurrent-drain shape, unchanged), so this is now structurally similar to [`internal/dispatch.Dispatcher`](dispatch-package.md)'s one-goroutine-per-conn model, but narrower in scope: only handler execution (`Route` → handler → `c.Send`, a marshal + channel push, no AEAD) moves off `Run`. `dispatchAppFrame`'s control-envelope arms (rekey/modal/interrupt/new_session/dequeue/snapshot/debug_bundle/settings) stay on `Run` — fast, and they touch `s.send`/session state/timers directly. An application frame is instead enqueued non-blocking onto a new per-conn `V2Session.appFrames` FIFO (capacity `appFrameQueueDepth = 16`; overflow tears the conn down at 4421 rather than block `Run`, which would reintroduce the stall), and `dispatchAppFrame` returns immediately — freeing `Run` to service every other select arm (other conns' frames, `m.wake`, `m.modalTimeout`, `m.manualRekey`) while the worker executes the handler. The worker drains `s.appFrames` in strict FIFO order — one frame fully routed (its `Route` call returned and every reply forwarded) before the next is dequeued — so no two handlers for the same conn ever run concurrently, and each reply crosses back to `Run` via a new unbuffered `V2SessionManager.appReply` channel (`chan appReplyMsg`), where `forwardAppReply` (now gated on `s.state == V2StateOpen`, dropping replies for a session torn down mid-handler) seals it under `s.send`. Every Noise cipher operation — `s.send.Encrypt`, `s.recv.Decrypt` — and every mutation of `s.state`/timers/`m.sessions`/`m.queues` keys stays exclusively on `Run`; the worker only ever touches its own conn's `s.appFrames`/`s.done` (safe unsynchronized channel ops), never `s.send`/`s.recv`/`s.state` directly, so `V2Session` still carries no lock. This closes the reply-*latency* head-of-line stall #909 explicitly deferred (#909 fixed the reply-*count* deadlock — a handler emitting more than `handlerOutboundBuf` replies — not this one). (Since #721 `send_message` is no longer the worst case for this reason alone: it now enqueues non-blocking and acks, so its `Activate`/`WriteUserTurn` blocking moved off the dispatch goroutine onto the daemon's `msgqueue` drain — see [features/msgqueue-package.md](msgqueue-package.md). `create_conversation`'s 30s `Activate` wait remains the concrete slow-handler case #965 targets.)

`V2Session.State()` is a plain field read. Safe today because no cross-goroutine reads exist. Both the push surface (#571, rewritten #610) and the #588 enumeration surface deliberately keep it that way: the #610 `Push` reads only `m.queues` under `pushMu` (never `s.state`), while `forwardEnvelope` reads `s.state` **on the `Run` goroutine** during the drain, and `handleActiveConns` reads `s.state` (and `s.interactive`, #626) **on the `Run` goroutine** (funneled through `m.snapshot`) — neither via a cross-goroutine `State()` call. So the broadcast/enumeration layer that this comment once anticipated (the [#589](../codebase/589.md) assistant-turn fan-out, built on #571's `Push` + #588's `ActiveConnIDs`) introduces **no** new reader of `s.state` off the owner goroutine, and `State()` still needs no `atomic.Int32`/mutex. Should a future slice read `s.state` directly from a producer goroutine *outside* the funnel, that accessor will need the atomic/mutex then — not pre-emptively refactored.

## Security and log discipline

Mirrors v1's `internal/relay/auth.go` posture. The implementation MUST adhere; CR checks each rule against the diff.

- **MUST NOT log at any level**: `HelloClientPayload.Token`, `cfg.StaticPriv`, raw `RoutingEnvelope.Frame` bytes, AEAD ciphertext bytes (the `Data` field of any `noise_msg`), plaintext envelope payload bytes (post-AEAD-decrypt), handler reply envelope bytes (pre-encrypt), encrypted reply bytes (post-`s.send.Encrypt`), base64-encoded forms of any of the above, **`V2Session.peerStatic` bytes (#452)**. The same MUST applies to `slog` fields, error wrapping (`fmt.Errorf("foo: %w", err)` where `err` accidentally carries the secret), and `panic` strings. `peerStatic` is identity-bearing rather than secret, but the no-key-bytes-in-logs discipline extends to per-session identity pins — emitting it makes the binary log a parallel device registry.
- **MUST log (operator-actionable) on ACCEPT**: event class `v2.handshake.accept`, `conn_id`, `device_name`. Plain low-cardinality string fields only.
- **MUST log (operator-actionable) on REJECT**: event class (`v2.handshake.reject.invalid_token` / `v2.handshake.reject.ik_failure` / `v2.state.reject`), `conn_id`, `close_code`. **NO `device_name`** even when the early-data carried one — anti-enumeration of paired-device names from binary logs.
- **MUST log on open-state AEAD failure**: event class `v2.aead.fail`, `conn_id`, `close_code=4421`. **NO error text** from `s.recv.Decrypt` (the underlying flynn/noise error may carry counter indices that aren't operator-actionable). **NO envelope shape information** — a frame that didn't decrypt cannot be inspected.
- **No per-envelope log on the open-state happy path.** High-frequency message traffic would spam the log channel; existing v1 handler logs (`send_message.ack`, etc.) inherit their per-handler log policy and surface the per-envelope diagnostic instead.

`V2SessionConfig.StaticPriv` is the binary's 32-byte X25519 static private key. The doc-comment on the field declares it MUST NOT be logged, wrapped into an error message, or emitted on any wire surface — [`internal/keys`](keys-package.md) and [`internal/noise`](noise-package.md) document the same contract for the same bytes.

The AEAD-sealed error envelope on the 4401 path emits a static `MsgInvalidToken` string and a fixed `CodeAuthInvalidToken` code; no attacker-influenced content is echoed. Close-only paths (4421 / 4426) emit no envelope at all — no leakage surface.

## Test surface

### Same-package unit tests (`internal/relay/v2session_test.go`, no WS)

Each test constructs a `V2SessionManager` with an in-memory `outbound` recorder (mutex-guarded slice; goroutine-safe) and a `devices.Registry` built inline.

- `TestV2Session_HappyPath` — paired-device `hello` in early-data → state advances to `V2StateOpen`; `noise_resp` envelope on `Outbound` carries hello_ack; CipherStates non-nil; no close-code emitted.
- `TestV2Session_BadToken_AEADErrorThen4401` — unknown-token hello → exactly one outbound envelope with `CloseCode == 4401`, frame is a `noise_msg`-wrapped AEAD-sealed error envelope; the initiator side decrypts the wrapped envelope and the test asserts `Code == auth.invalid_token`. State = closed.
- `TestV2Session_IKReject_4426` — `noise_init` carrying random bytes (no real IK message 1) → exactly one outbound envelope with `CloseCode == 4426`, no Frame body. State = closed.
- (Removed in #453: `TestV2Session_NoiseInitAfterOpen_4421` pinned the behaviour that re-key responder #453 intentionally changes; replacement coverage is the three `TestV2Session_RekeyResponder_*` tests below.)
- `TestV2Session_Gating_NoiseMsgInHandshakeComplete_4401` — directly assign `s.state = V2StateHandshakeComplete`, `s.send/recv = <CipherStates from a real adjacent handshake>`, feed a `noise_msg` whose plaintext is a non-hello envelope. Asserts: exactly one outbound envelope with `CloseCode == 4401`, frame is AEAD-sealed `error{auth.invalid_token}`. **Structurally proves the "handler chain unreachable from handshakeComplete" invariant** — the regression guard for any future refactor that might add a v2→handler edge.
- `TestV2Session_OutOfStateRejections` — table-driven over the remaining cells: malformed JSON / unknown `Type` / bad `v` / unexpected `noise_resp` → 4421 in each state.
- `TestNewV2SessionManager_ConfigValidation` — panics on nil `Frames` / nil `Logger`; wrapped errors on nil `Outbound` / nil `Devices` / empty `ServerID` / wrong-length `StaticPriv`. `Handlers` is optional — no new validation case.

Open-state dispatch additions (#446):

- `TestV2Session_OpenState_EncryptedRoundTrip` — paired-device happy path through `dispatchAppFrame`. Stub handler keyed by `TypeListConversations` replies via `c.Reply`; phone-side decrypt of the captured `noise_msg` matches the handler's payload, `InReplyTo` echoes the request id, session state stays `V2StateOpen`.
- `TestV2Session_OpenState_TamperedNoiseMsg_4421` — flip one byte of a real ciphertext → exactly one outbound envelope with `CloseCode == 4421` and nil `Frame`, the registered handler's `atomic.Bool` flag stays false (handler chain structurally unreachable), and `mgr.sessions[v2TestConnID]` is absent (AC #3 — `closeWith` deletion).
- `TestV2Session_OpenState_FreshNoiseInitAfterAEADClose` — companion to the prior test. After 4421+cleanup, a second `noise_init` on the same `conn_id` completes a fresh handshake; a ciphertext sealed under the OLD `initSend` fails against the new session's `s.recv` (deterministic proof that the post-cleanup session is fresh `awaitingInit`-then-`open` with no carry-over CipherStates).
- `TestV2Session_OpenState_UnknownEnvelopeType_SealedUnsupportedReply` — open-state envelope with `Handlers = nil` → AEAD-sealed `Envelope{Type: TypeError, Payload.Code: CodeProtocolUnsupported}`. State stays `open`.
- `TestV2Session_OpenState_MalformedInnerEnvelope_SealedMalformedReply` — open-state envelope whose AEAD plaintext is raw garbage → AEAD-sealed `Envelope{Type: TypeError, Payload.Code: CodeProtocolMalformed}`. State stays `open`.
- `TestV2Session_OpenState_HandlerAuthDevice` — handler captures `c.Auth().Name` from inside the dispatch closure; asserts the matched-device snapshot captured during handshake (`s.device`) reaches the handler via `*dispatch.Conn.Auth()`.

Peer-static capture (#452):

- `TestV2Session_InitialHandshake_CapturesPeerStatic` — drives a paired-device handshake to `V2StateOpen` via `driveToOpen`, then white-box-asserts `mgr.sessions[v2TestConnID].peerStatic == initPub` and `len(...) == noise.KeyLen`. The length check is the regression guard against an empty-slice silently passing a future `bytes.Equal(nil, nil)` comparison if the capture site is skipped. Pins the capture invariant for the inert-in-this-slice field that #453 will read.

v2 control-envelope discriminator (#454):

- `TestV2Session_OpenState_RekeyRequest_Intercepted` — paired-device handshake to open → AEAD-sealed `{type: "rekey_request", payload: {reason: "scheduled"}}` → stub handler's `atomic.Bool` stays false (application chain unreachable), session stays `V2StateOpen`, no outbound close envelope. Structural proof that the probe in `dispatchAppFrame` runs before `dispatch.Route`.
- `TestV2Session_OpenState_RekeyRequest_UnknownReasonTolerated` — `{reason: "lunar-eclipse"}` → buffer-logger captures `level=WARN`, `event=v2.rekey.request.received`, `reason=lunar-eclipse`. Session stays open, no outbound frame. Forward-compat posture for unknown reasons.
- `TestV2Session_OpenState_RekeyRequest_RecognisedReasons` — table-driven over `{scheduled, manual, compromise}`; each subtest asserts `level=INFO`, the correct `reason` field, no close, no outbound frame, state stays open.

Re-key responder swap (#453):

- `TestV2Session_RekeyResponder_HappyPath_RoundTripUnderNewKeys` — drives the paired-device handshake to open, constructs a SECOND `noise.Initiator` reusing the SAME `initPriv` (peer-continuity invariant), feeds a fresh `noise_init` with empty early-data, asserts the rekey `noise_resp` returns under `CloseCode == 0`, then AEAD-seals a `TypeListConversations` request under the NEW `initSend2` and asserts the reply decrypts cleanly under the NEW `initRecv2`. Post-stop assertions: `state == V2StateOpen`, `s.device.Name` preserved, `bytes.Equal(s.peerStatic, initPub)` (#452 lifetime contract honoured across re-key). Pins AC #1 + AC #5 — both directions of the swap are wired and the device + peer-static snapshots survive.
- `TestV2Session_RekeyResponder_DifferentPeerStatic_4426` — drives to open, then feeds a fresh `noise_init` from a DIFFERENT keypair. Exactly one additional outbound envelope with `CloseCode == 4426` and nil Frame; `mgr.sessions[v2TestConnID]` absent; log buffer contains `event=v2.handshake.reject.ik_failure` + `reason=rekey_peer_static_mismatch`; **the reject log line specifically does NOT contain `device_name`** (per-line substring check — a global check would false-positive on the initial `v2.handshake.accept`). **Security-load-bearing test** for the Threat #3 residual-risk claim.
- `TestV2Session_RekeyResponder_OldKeyFrameAfterSwap_4421` — drives to open, stashes a ciphertext sealed under `sess.initSend` BEFORE the re-key, completes a successful re-key with the same static, then feeds the stashed stale frame. The inherited #446 tampered-frame branch fires against the post-swap `s.recv` → `CloseCode == 4421`, nil Frame, session removed. **No new code path** — verifies the inheritance at the new authenticated-state boundary.

Re-key initiator — 1-hour timer + emit + 30 s reply timeout (#450):

- `TestV2Session_RekeyInitiator_Emit_ReArmViaResponder` — joint coverage of AC #5 bullets 1 + 2. Substitutes `rekeyInterval = 20*time.Millisecond` / `rekeyReplyTimeout = 500*time.Millisecond`; drives to open; waits for the FIRST emit and decodes the inner envelope under `sess.initRecv` (`Type == protocol.TypeRekeyRequest`, `payload.reason == "scheduled"`); constructs a second `noise.Initiator` reusing the SAME `initPriv` and feeds a fresh `noise_init`; reads the rekey `noise_resp` via `initiator2.ReadResp` to derive `initRecv2`; waits for the SECOND emit and decodes under the post-swap `initRecv2`. State assertions after stop: `state == V2StateOpen`, `awaitingRekeyReply == true` (no second responder cycle ran).
- `TestV2Session_RekeyInitiator_ReplyTimeout_4426` — substitutes `rekeyReplyTimeout = 40*time.Millisecond`; uses `bufferLogger()`; drives to open; does NOT feed a noise_init reply; waits for the close envelope via the new `waitForOutboundCount(t, rec, n, deadline)` helper. Asserts: third outbound envelope has `CloseCode == uint16(StatusHandshakeFailure)` and nil Frame; `mgr.sessions[v2TestConnID]` absent after stop; log buffer contains `event=noise.rekey_failed`, `close_code=4426`, `conn_id=…`; the `noise.rekey_failed` line specifically does NOT contain `err=` (per-line substring check — anti-leakage of flynn-noise error text).
- `TestV2Session_RekeyInitiator_TimerCleanup_NoGoroutineLeak` — two sub-tests: `close_via_manager_exit` (drive to open, stop immediately before the rekeyTimer fires) and `close_via_reply_timeout` (drive to open, wait for the close envelope after the reply-timeout, then stop). Both assert `runtime.NumGoroutine()` returns to within +1 of the pre-test baseline after `runtime.Gosched(); runtime.GC(); time.Sleep(20ms)`. Pins the goroutine-lifetime invariant for `time.AfterFunc` callbacks under the wake-channel design.

Operator-driven manual re-key (#462):

- `TestV2Session_RekeyManual_HappyPath_EmitsManualReason` — drives `driveToOpen`, calls `mgr.Rekey(ctx, v2TestConnID)`, waits for the second envelope (initial `noise_resp` + manual emit), AEAD-decrypts the second envelope under `sess.initRecv`, asserts `Type == protocol.TypeRekeyRequest` and `payload.reason == "manual"`, and asserts the log buffer contains `event=v2.rekey.emit` + `reason=manual` + `conn_id=<v2TestConnID>`. Substitutes long `rekeyInterval`/`rekeyReplyTimeout` (10 s each) so the scheduled boundary cannot fire during the test window.
- `TestV2Session_RekeyManual_UnknownConn_ReturnsErrConnNotFound` — manager with `Run` started but `m.sessions` empty; `mgr.Rekey(ctx, "this-conn-does-not-exist")` returns an error that satisfies BOTH `errors.Is(err, relay.ErrConnNotFound)` AND `errors.Is(err, control.ErrConnNotFound)` (the wire-mapping invariant — the `%w` wrap survives both levels). `rec.snapshot()` is empty (no outbound side-effect).
- `TestV2Session_RekeyManual_AlreadyAwaitingReply_ReturnsErrSessionNotOpen` — long `rekeyReplyTimeout` so the first emit's awaiting-reply window doesn't auto-close; first `Rekey` succeeds → wait for the manual emit to land in `rec` → second `Rekey` returns `errors.Is(err, relay.ErrSessionNotOpen)`. Asserts only ONE manual emit envelope is recorded (no double emit), session state remains `V2StateOpen`, and `s.awaitingRekeyReply` remains true.
- `TestV2Session_RekeyManual_RebasesScheduledTimer` — substitutes `rekeyInterval = 100ms` / `rekeyReplyTimeout = 1s`. Drives to open, immediately calls `Rekey`, waits for the manual emit at T ≈ small_delta and asserts `payload.reason == "manual"`. Drives a fresh responder cycle (same `initPriv` → peer-static continuity passes), captures `initRecv2`. **Original-boundary check**: at T ≈ rekeyInterval + jitter (130 ms after open — past the original scheduled boundary, well before T_rekeyComplete + rekeyInterval), asserts `rec.snapshot()` length is still 3 (manual + responder reply + nothing else). **New-boundary check**: at T_rekeyComplete + rekeyInterval + jitter, asserts a fourth envelope arrives whose payload decodes (under post-swap `initRecv2`) to `TypeRekeyRequest` with `reason == "scheduled"`. The original-boundary check is the load-bearing negative assertion — it directly pins "no stale scheduled emit lands between manual emit and the re-based scheduled boundary."

All four #462 tests are explicitly NOT `t.Parallel()` — they mutate package-level `rekeyInterval` / `rekeyReplyTimeout` vars (same posture as the other rekey tests).

Server-initiated push (#571; backpressure + drop policy #610) — all `t.Parallel()`, all `-race`-clean; the `buildMessageEnvelope(t, id, text)` helper builds the binary→phone `message` envelope (always on the test goroutine — `t.Fatalf` from a child goroutine is unsafe):

- `TestV2Session_Push_InterleavedWithReply_DecryptsUnderRace` — fires a `Push` from a separate goroutine while feeding an inbound sealed request that triggers a `dispatchAppFrame` reply; the push drain and reply path contend for the single `Run` goroutine. Decrypts all three outbound frames (`noise_resp` + reply + push) in capture order under the phone's `initRecv` — a clean in-order decrypt is the nonce-integrity proof. The pushed frame decodes through the SAME `decryptAppFrame` path to a valid `TypeMessage` envelope (AC#1 + AC#4 — no new wire shape). Order between reply and push is nondeterministic (`Run`'s `select`); asserts presence, not order. **Passes unchanged across the #610 buffered-enqueue rewrite** (`waitForEnvelopes` polls; every seal still happens on `Run` in FIFO order).
- `TestV2Session_Push_ConcurrentWithReplies_NoNonceCorruption` — the stress version: N=8 concurrent pushes + M=8 in-flight request/reply dispatches; all N+M outbound frames decrypt in capture order with no AEAD failure (AC#2 — the drain serialises every `s.send.Encrypt` onto `Run`, nonce never reuses). Also passes unchanged post-#610 (8 pushes are well under `pushQueueCap`, so none drop).
- `TestV2Session_Push_UnknownConn_ErrConnNotFound_OtherSessionUnaffected` — push to a never-seen `conn_id` returns an error satisfying BOTH `errors.Is(err, relay.ErrConnNotFound)` AND `errors.Is(err, control.ErrConnNotFound)`; an unrelated open session's subsequent solicited round-trip still decrypts (AC#3 — no mutation of another session's state).
- `TestV2Session_Push_NotOpen_ReturnsErrConnNotFound` (renamed from `…ReturnsErrSessionNotOpen` in #610) — a white-box non-open session has no queue, so `Push` now returns `ErrConnNotFound`. The companion `TestV2Session_forwardEnvelope_NotOpen_GateRefuses` pins the drain-side `V2StateOpen` security gate that moved to `forwardEnvelope`. (Pre-#610 this asserted `ErrSessionNotOpen` via `handlePush`'s state check.)
- `TestV2Session_Push_ClosedSession_ReturnsErrConnNotFound` — drives an AEAD-failure 4421 teardown (flips a ciphertext byte) that deletes the session (and its queue), then asserts a push to that `conn_id` collapses into `ErrConnNotFound`.
- `TestV2Session_Push_CtxCancelled_ReturnsCtxErr` — a `Push` with an already-cancelled ctx returns `ctx.Err()` without blocking; `Push` checks `ctx.Err()` before consulting `m.queues` (#610), so a cancelled ctx short-circuits deterministically.

#610 backpressure tests (added): the drop policy is a pure unit surface — `TestPushQueue_Enqueue_*` (helpers `pqEnv` / `fillDeltas` / `assertQueue`) cover under-cap retention, drop-oldest-delta (AC#2), control-evicts-delta (AC#3), `message`-is-never-drop, control-never-dropped-when-deltas-present, order-preserved-across-drops (AC#4), and the all-control soft overflow. The end-to-end non-blocking guarantee (AC#1) is `TestV2Session_Push_NonBlockingUnderStall`: a stalling outbound double wedges the `Run` forward, every `Push` still returns within a tight deadline, the drop counter engages past `pushQueueCap`, and after release the survivors decrypt **in order** under the phone's `recv` state (proving drop-before-seal left no nonce gap). See [`codebase/610.md`](../codebase/610.md).

Capability negotiation (#626) — `buildHelloEarlyDataCaps` / `driveToOpenCaps` variants carry the advertised set without changing the `buildHelloEarlyData` (5 callers) / `driveToOpen` (30 callers) signatures; the handshake tests capture the hello_ack early-data (which `driveToOpen` discards) via `Initiator.ReadResp` → decode `Envelope` → `HelloAckPayload`:

- `TestNegotiateCapabilities` — table-driven AC#2/#3 matrix for the pure function: `[interactive]`→`[interactive]`; `[interactive, unsupported]`→`[interactive]` (drop); `[unsupported]`→`nil` (spoof); `nil`/`[]`→`nil` (advertise-nothing); `[interactive, interactive]`→`[interactive]` (dedup). `t.Parallel()`.
- `TestV2Session_Handshake_CapabilityNegotiation` — table-driven handshake-level: advertise `[interactive]` → ack echoes `[interactive]`; advertise nothing → ack has no `capabilities` key; spoof `[interactive, god-mode]` → ack echoes only `[interactive]`; `[god-mode]` only → ack has no capabilities.
- `TestV2Session_ActiveConns_MixedInteractive` — two open conns (one interactive, one not) → `ActiveConns` reports the correct flag per conn (white-box injection mirroring `TestV2Session_ActiveConnIDs_OpenOnly`).
- `TestV2Session_CapabilitySpoof_TokenFail_NeverEnumerated` — the **security** test: a phone advertising `[interactive]` but failing the token is closed at 4401 and never appears in `ActiveConns`; the negotiated flag is never observable for an unauthenticated peer.

The pre-existing `TestV2Session_ActiveConnIDs_*` suite (`OpenOnly`, `TornDownSessionAbsent`, `ConcurrentWithDispatch_RaceClean`, `EmptyManager`, `CtxCancelled_ReturnsNil`) passes **unchanged** — the `[]string` projection preserves #588's contract (AC#5).

Reconnect replay (#647) — `internal/relay/v2session_replay_test.go` (new): each drives a real Noise handshake whose hello carries `last_event_id` against a manager whose `SetReplaySource` was given a hand-populated `eventring.Ring` + a stub cursor, decrypts the forwarded frames, and asserts. `TestV2Session_Reconnect_ReplaysMissedTail` (3,4,5 ascending before any live frame), `…_CaughtUp_NoReplay`, `…_Gap_EmitsResync` (one `resync` with `conversation_id`), `…_AbsentLastEventID_NoReplay`, `…_ScopedToCursorConversation` (cursor→B, ring holds A → zero A events, AC-5), `…_ReplayDisabled_NoReplay` (nil ring), `…_OtherConnsUnaffected`, `…_ForwardEnvelope_ReplayWatermarkGuard` (drops `EventID ≤ replayThrough`, forwards above, never drops `EventID == nil`). The #647 caught-up/out-of-range tests asserted only *no replay frames* and missed that the live stream was muted afterward; **#663 closes that gap** — `…_OutOfRangeLastEventID_LiveStreamDelivered` (incl. `math.MaxUint64`), `…_ClearRotation_LiveStreamDelivered`, and `…_SameConversation_DedupPreserved` push a live frame after the caught-up handshake and assert **delivery** (see [codebase/663.md](../codebase/663.md)). Paced replay (#777) reworked the harness — `reconnectScenario` now polls `waitForEnvelopes(t, rec, wantEnvs)` for the final frame count instead of `waitConnOpen` + an open-instant snapshot (the tail drains over `Run` passes *after* the conn is enumerable), and the content-assertions above stayed green through the swap. Four new same-package tests pin the pacing (two direct-drive on an injected session via `offlineHandshake`/`newInjectManager`, two through the live `Run` select): `…_Paced_OnePerRunPass` (one `drainReplayOnce` pass forwards exactly ONE frame, advances `replayThrough` by one), `…_LiveGated_DrainOnceSkipsReplayingConn` (**AC #2**: `drainOnce` won't pop a replaying conn's buffered live events until its tail empties), `…_LargeReplay_DoesNotBlockOtherConn` (**AC #1**: conn B's `noise_resp` lands before conn A's final frame of a ~200-event replay — probability ≈ 2⁻²⁰⁰ of failing under a fair select), `…_LiveInterleave_OrderedAfterReplay` (**AC #2** end-to-end: a live push reaches the wire after every replay frame), and `…_PacedReplay_Race` (**AC #3**: `-race`-clean replay drained concurrently with off-`Run` `Push`es + a second conn's handshake; every `s.send.Encrypt` stays serialised on `Run`) (see [codebase/777.md](../codebase/777.md)).

Inbound modal control (#727) — `internal/relay/v2session_modal_test.go` (new): a fake `ModalResolver` (records calls; canned `ModalDismissal{cancelled,remote}` with `ok=true` for the configured cancel id) + a conn-aware handshake helper (`openModalConn`) that stands up ≥2 interactive heads on one manager and recovers each conn's `noise_resp` from the shared `v2Recorder` by `ConnID`. `TestV2Session_ModalCancel_FanOut` (three heads — two interactive, one not; asserts `ResolveCancel` routed once with the right `modal_id` + per-conn device, `modal_dismissed{cancelled,remote}` decrypts at **both** interactive heads, **zero** noise_msg at the non-interactive one — AC-1/AC-2; running through the real `Frames`/`Run` loop is the structural no-deadlock proof), `…_ModalAnswer_NoOp` (routed through the seam, no dismissal — AC-3), `…_ModalCancel_UnknownID_NoOp` (resolver `ok=false` → no dismissal — AC-4), `…_ModalControl_NilResolver` (both frames inert debug-logged no-ops, session stays `V2StateOpen`). The resolver-side assertions (consume + keystroke + audit + no-body-leak) live in `cmd/pyry/modal_resolve_v2_test.go` (the relay can't import supervisor/audit/registry). See [codebase/727.md](../codebase/727.md).

Inbound interrupt (#707) — `internal/relay/v2session_interrupt_test.go` (new): a `fakeInterrupter` (mutex-guarded `escCalls` counter + injectable error, mirroring `fakeModalResolver`), driving everything through the real `Frames`/`Run` loop with the #727 helpers. The fire-and-forget no-reply handler is synced with a **barrier conn opened after the interrupt frame** — the single FIFO `Frames` + single `Run` goroutine guarantees the interrupt is fully handled before the barrier opens, so the counter is final (and works on the nil path: barrier-open is the no-panic/no-hang proof). `TestV2Session_Interrupt_RoutesEscByCapability` (table-driven both paths: interactive → exactly one Esc, non-interactive → zero — AC-2/AC-4), `…_NilInterrupterInert` (nil seam → zero Esc, no panic, manager keeps serving), `…_SendEscErrorTolerated` (`SendEsc` error → still one attempted call, no crash/close — best-effort). See [codebase/707.md](../codebase/707.md).

Inbound new_session (#831) — `internal/relay/v2session_newsession_test.go` (new): a `fakeSessionStarter` (mutex-guarded `startCalls` counter + injectable error, mirroring `fakeInterrupter` line-for-line), driving everything through the real `Frames`/`Run` loop with the #727 helpers, synced with the same **barrier-conn-after-the-frame** idiom (#707). `TestV2Session_NewSession_RoutesClearByCapability` (table-driven both paths: interactive → exactly one `/clear`, non-interactive → zero — AC #3/AC #4), `…_NilStarterInert` (nil seam → zero `/clear`, no panic, manager keeps serving — AC #5), `…_StartErrorTolerated` (`StartNewSession` error → still one attempted call, no crash/close — best-effort, AC #5). The v1/v2 partition (AC #1/AC #2) is covered by the `compat_test.go` edits, not this file. See [codebase/831.md](../codebase/831.md).

Inbound dequeue_message (#723) — `internal/relay/v2session_dequeue_test.go` (new): a `fakeQueueRemover` (mutex-guarded `(convID, id)` call recorder + programmable bool return, mirroring `fakeInterrupter`), driving everything through the real `Frames`/`Run` loop with the #727 helpers, synced with the same **barrier-conn-after-the-frame** idiom (#707). `TestV2Session_DequeueMessage_RemovesByCapability` (table-driven: interactive + `Remove→true` (AC-1) and interactive + `Remove→false` (AC-2) both call `Remove` exactly once with the decoded `(conversation_id, queued_msg_id)`; non-interactive calls it zero times (AC-3); every case asserts no app frame pushed to the conn — no reply/broadcast, AC-2/AC-4), `…_NilRemoverInert` (nil seam → no panic, manager keeps serving), `…_DecodeTolerant` (payload malformed as `DequeueMessagePayload` but valid JSON → no panic, `Remove` reached with zero-value fields, a unique payload marker never appears in a captured `bytes.Buffer` logger — the never-echo assertion), `…_InterceptedNotRouted` (a sentinel `dispatch.Handler` registered under `TypeDequeueMessage` never fires — interception precedes `dispatch.Route`). The AC-5 e2e capstone lives in `internal/e2e/relay_v2_dequeue_test.go` (`TestRelayV2_DequeueMessage_RemovesQueuedBeforeDrain`): spawned-daemon Noise harness + sleep-claude, enqueue two → dequeue the non-head → assert the surviving backlog is `[msg1]` with no error envelope. See [codebase/723.md](../codebase/723.md).

Inbound set_session_settings (#845) — `internal/relay/v2session_settings_test.go` (new): a fake `SettingsUpdater` (mutex-guarded `(sessionID, SettingsUpdate)` call recorder + programmable error) driving everything through the real `Frames`/`Run` loop, decrypting outbound replies via the `v2Recorder`/`decryptAppFrame` helpers. `TestV2Session_SetSessionSettings_AppliesByCapability` (table-driven: all-three-fields / effort-only / yolo-only / `model:""`+`effort:""` "clear" values → seam called once with the right non-nil pointers, one `session_settings_updated` reply with `in_reply_to` echoing the request and `session_id` echoing the payload; non-interactive → seam **not** called, **zero** outbound frames — AC #6), `…_ErrorReplies` (table-driven: nil seam → `server.binary_offline`/retryable; `ErrSessionUnknown` → `session.not_found`; an arbitrary persist error whose text embeds `"boom"`/`"secret"`/`.json` → reply message equals the fixed `msgSettingsUnavailable` constant and neither the reply nor the log buffer ever contains that text — never-echo, asserted on both surfaces), `…_MalformedRejected` (`{"yolo":"nope"}` type mismatch, truncated JSON, and invalid `model`/`effort` values, each carrying a distinctive marker → seam not called, `protocol.malformed` reply, marker absent from the reply and every log line), `…_InterceptedNotRouted` (a sentinel `dispatch.Handler` registered under `TypeSetSessionSettings` never fires — interception precedes `dispatch.Route`). Plus standalone unit tables `TestValidModel` / `TestValidEffort`. See [codebase/845.md](../codebase/845.md).

Deny-on-timeout (#725) — same `v2session_modal_test.go` (`fakeModalResolver` extended with `ResolveTimeout`; `modalDenyTimeout` shrunk via save/restore): `TestV2Session_ModalTimeout_FanOut` arms a timeout, lets the window elapse with no answer, and asserts `ResolveTimeout` fired exactly once and `modal_dismissed{denied_timeout,timeout}` reaches both interactive heads but not the non-interactive one — the off-`Run`-arm (`AfterFunc`) → on-`Run`-fire (`handleModalTimeout`) crossing is the `-race` proof; `…_ModalTimeout_AlreadyResolved_NoBroadcast` (resolver `ok=false` ⇒ no dismissal — AC-2 loser path); `…_ModalTimeout_NilResolver` (armed timeout firing is inert, no panic). The resolver-side `ResolveTimeout` assertions (one ESC, single `denied_timeout` audit with **empty** device + no modal body, the already-consumed/unknown/keystroke-error paths) live in `cmd/pyry/modal_resolve_v2_test.go`; the surfacer arms exactly one timeout per surfaced modal in `cmd/pyry/interactive_modal_v2_test.go` (`fakeArmer`). See [codebase/725.md](../codebase/725.md).

Handler execution offload (#965) — extends `v2session_appframe_test.go`, reusing the #909 harness (`prolificHandler`, `driveToOpen`, `v2Recorder`, `sealAppFrame`, `waitForOutboundCount`, `decryptAppFrame`): `TestV2Session_SlowHandler_DoesNotStallOtherConn` (**AC-1(a)**) drives two open conns on one manager by reusing the #727 `openModalConn` conn-aware handshake helper (no new harness variant needed — the spec's speculative `driveToOpenConn` wasn't required), blocks conn A's handler on a test-controlled channel, and asserts conn B's `list_conversations` reply is sealed + emitted promptly with no dependence on releasing A; `TestV2Session_SlowHandler_ModalTimeoutStillFires` (**AC-1(b)**) blocks an app handler on conn A and asserts the `v2session_modal_test.go` deny-on-timeout still fires on schedule; `TestV2Session_SlowHandler_RekeyStillEmits` (**AC-1**, rekey witness) blocks an app handler and asserts a scheduled/manual rekey still emits; `TestV2Session_AppFrame_PerConnSerialization` (**AC-2**) sends two app frames on one conn and asserts (a) no two handlers for that conn run concurrently and (b) the two sealed replies decrypt via `initRecv` — which decrypts strictly in send-counter order, so a reorder surfaces as an AEAD auth failure — with `in_reply_to` in arrival order; `TestV2Session_AppFrame_QueueOverflow_ClosesConn` fills one conn's `s.appFrames` past `appFrameQueueDepth` behind a permanently-blocked handler and asserts that conn closes at 4421 while an unrelated conn is unaffected; `TestV2Session_OpenState_ProlificHandler_NoDeadlock` / `…_EmissionOrder` (both pre-existing #909 tests) pass unchanged, pinning that `routeAppFrame` preserves the concurrent-Route-drain shape verbatim. AC-3 (every seal stays on `Run`) is pinned by `-race` across all of the above, not a separate fixture — an off-`Run` seal or concurrent CipherState touch fires the race detector and breaks the ordered-decrypt assertions. See [codebase/965.md](../codebase/965.md).

### E2E (`internal/e2e/relay_v2_handshake_test.go`, build tag `e2e`)

Spins up `fakerelay` (now with both `/v1/server` and `/v2/server`), wires `relay.Connect` + `V2SessionManager` **inline** (no daemon — this is the manager-in-isolation harness; the daemon-level wiring is covered separately by `relay_v2_daemon_test.go`, [#549](../codebase/549.md)), dials a `fakephone` against `/v1/client` (unchanged routing wire under v2), and drives a Noise_IK handshake from the phone side.

- `testV2HappyPath` — paired device → phone observes a `noise_resp` frame, decrypts hello_ack, then no further traffic.
- `testV2BadToken` — unpaired device → phone reads the AEAD-sealed `auth.invalid_token` `noise_msg`, then `Read` errors with `LastCloseStatus() == 4401`.
- `testV2IKReject` — phone sends an invalid noise_init (random bytes, no real IK message 1) → phone's next read errors with close code 4426. No prior frame from binary.
- `testV2EncryptedEchoRoundTrip` (#446) — paired-device handshake to open with a stub handler registered against `TypeListConversations`; phone-side AEAD-seal request, read one inner frame back, decrypt with `initRecv`, assert the inner envelope's `Type`/`InReplyTo`/`Payload` match the handler's reply.
- `testV2TamperedNoiseMsg_4421` (#446) — phone sends a `noise_msg` with one byte flipped after handshake; phone observes `LastCloseStatus() == 4421`. The "fresh `noise_init` on the same `conn_id`" assertion lives in the unit test layer because `fakerelay` assigns a new `conn_id` per dial.

The gating-invariant test and the post-AEAD-failure fresh-handshake test are unit-shape only — the e2e suite covers the natural inbound flows.

## Fakerelay / fakephone harness additions

- **`fakerelay.New` registers `/v2/server`** alongside `/v1/server`, sharing the existing `handleBinary` handler — the relay-side wire (binary↔relay routing envelope) is unchanged in v2. Phone-side `/v2/client` is NOT registered; tests connect the phone on `/v1/client`. The fakerelay's `binaryRecvPump` now treats `json.RawMessage` that marshals to the literal token `"null"` as "no frame to forward", matching the production relay's close-only envelope contract — without this, the close-only 4421/4426 paths would attempt to forward a `null` frame to the phone.
- **`fakephone.Client.SendBytes(data []byte)` / `ReceiveBytes(timeout)`** are byte-oriented siblings to `Send(env)` / `Receive(timeout)`. The wire shape inside `RoutingEnvelope.Frame` under v2 is an `InnerFrameV2` (not a `protocol.Envelope`), so the test driver builds the v2 frame as raw bytes and bypasses the `Envelope` marshal/unmarshal. `Send` / `Receive` delegate to the byte-oriented variants for the v1 case so v1 behaviour is unchanged.

## Out of scope (deferred)

- **Production wiring of `V2SessionManager` into `cmd/pyry/relay.go`** — **landed in [#549](../codebase/549.md)** behind `PYRY_MOBILE_V2=1` (see the "Production wiring" line above). One aspect remains deferred: the per-conn fan-out below (the assistant-turn `message` fan-out landed in [#589](../codebase/589.md), next bullet).
- **Assistant-turn `message` fan-out to v2 phones — landed in [#589](../codebase/589.md).** The v2 assistant-turn bridge (`cmd/pyry/assistant_turn_v2.go`) taps the assistant/PTY output stream (the v2 analog of #311), calls [`ActiveConnIDs`](#concurrency-safe-open-session-enumeration-588--activeconnids-method--snapshot-funnel) then [`Push`](#concurrency-safe-unsolicited-push-571--push-method--push-funnel) per returned id, and is wired into `startRelayV2` under a `bridge != nil` foreground gate — so `Push` and `ActiveConnIDs` now have a production caller. The outbound envelope-ID policy is settled: the bridge mints `env.ID` from a caller-side monotonic counter, with `MessagePayload.MessageID` (a UUID) as the phone's dedup/ordering key. **Re-targeted in [#634](../codebase/634.md), then removed in [#699](../codebase/699.md):** #634 made the bridge consume `ActiveConns` and skip `c.Interactive` conns (coarse → non-interactive only), then #699 deleted `cmd/pyry/assistant_turn_v2.go` entirely — once the 2026-06-22 ADR 025 amendment made every phone `interactive`, its non-interactive delivery branch was unreachable. The structured stream (#632/#633) is now the **sole** v2 assistant-turn path, and keeps `Push` / `ActiveConns` as their production callers. **Live token streaming** remains the genuinely deferred piece (pyrycode-mobile#337). The out-of-scope v1 / dispatch-leg coarse bridge (`cmd/pyry/assistant_turn.go`) still produces `message`, so the wire constant stays.
- **Per-conn fan-out for handler dispatch — landed in [#965](../codebase/965.md).** `Run` no longer blocks its dispatch loop for a handler's `dispatch.Route` duration: application-frame handlers now run on a per-conn worker goroutine (`appFrameWorker`), draining a new `s.appFrames` FIFO and posting replies back to `Run` via `m.appReply` for sealing (`s.send`/`s.recv` stay exclusively on `Run` — no per-session mutex needed, unlike the mutex-guarded shape originally anticipated here). This closed the reply-*latency* head-of-line stall [#909](../codebase/909.md) explicitly deferred (the reply-*count* deadlock #909 itself fixed). See [§ Concurrency](#concurrency).
- Operator-facing `pyry rekey <conn_id>` verb — sibling slice B2 of #460 (re-split of #451). CLI surface for manual re-key; uses slice A's [`control.Rekey`](control-plane.md#client-helper) client helper to call `*V2SessionManager.Rekey` (shipped in #462), which reuses this slice's `emitRekeyRequest` plumbing with `payload.reason == "manual"`. The `"compromise"` value remains reserved for a future caller.
- Control-socket wiring of `(*V2SessionManager).Rekey` — still deferred. As of [#549](../codebase/549.md) the daemon constructs the manager and drives `Run` (so `NewV2SessionManager` now has a production caller), but it does **not** call `ctrlServer.SetRekeyer(mgr)`. Until that wire-up lands, the #462 `Rekey` method (hence `pyry rekey <conn_id>`) is reachable from `internal/relay` tests only.
- Explicit `Wipe()` of old CipherState key bytes on re-key swap — would require touching #433's surface; deferred. The single-owner-goroutine invariant provides the practical zeroisation property (no code path observes the old state after the swap); documented in the `V2Session` package comment.
- `s.resp` reset to nil after handshake completes — the field is dead state after `WriteResp` returns and is unused by the re-key path (which constructs a fresh local `Responder`). Cleanup belongs in a `V2Session`-shape refactor, not in any re-key slice.
- **Event-driven exact teardown on phone-initiated WS close** — a relay→binary "phone disconnected" forward signal does not exist on the v2 wire today (cross-repo relay work). Since [#774](../codebase/774.md) the [in-repo idle sweep](#idle-session-teardown-774--per-session-idle-timer--in-repo-sweep) is the **coarse in-repo backstop**: a dropped/backgrounded phone's session is reclaimed within `idleTimeout` (15 min default) at WS 4408 rather than lingering up to the 1-hour rekey interval. If the relay later emits a per-connection disconnect signal, an exact-teardown-on-disconnect follow-up can supersede or complement the sweep (future ticket). AEAD-failure teardown ([#446](../codebase/446.md)) and the idle sweep (#774) are the two binary-initiated cleanup paths.
- Per-phone-conn 10s handshake timeout — requires a relay→binary "phone connected" signal that does not exist in the v2 wire today. Tracked for a future protocol amendment + binary slice.
- Revocation propagation to active conns — the device snapshot captured on `s.device` does not refresh after handshake; same posture as v1's `dispatch.Conn.auth`. Revocation tears down at the next WS recycle, not mid-conn.
- **Extending the `transportDown()` guard beyond `drainOnce` (#874 Open Questions).** `emitRekeyRequest`'s two callers are **done — see [#912](#scheduled--manual-rekey-emit-gated-behind-transportdown-912)**, gated at the call site rather than inside the primitive. `drainReplayOnce`, the dispatch-reply seal, and close-frame seals share the same seal-while-down nonce hazard but remain inert during #874's within-grace scenario (no replay tail, no inbound frames to reply to) — extending the guard to them is still a one-line addition each, gated on observed failures, not pre-emptive.
- **Reconcile-on-connect for a brand-new conn (#829) — both halves landed.** #874 covers only the *same surviving conn* riding out a blip inside the relay's 30-second grace. Bringing a **new** WS connection (post-grace reconnect, app relaunch) up to date is the separate reconcile-on-connect mechanism tracked under the #829 umbrella, distinct from both #874's within-grace hold and #647/#777's `last_event_id` ring replay: [#877](#connect-time-modal-reconcile-877--outstandingmodals-seam--reconcilemodals) ships the `modal_shown` re-send, and [#878](#connect-time-queue-reconcile-878--outstandingqueues-seam--reconcilequeues) ships the `queue_state` twin, reusing the same `handleNoiseInit` reconcile hook with a sibling `OutstandingQueues` seam. [#879](../codebase/879.md) writes the single written contract both halves implement (`docs/protocol-mobile.md` § Reconnect / Backfill semantics) so client authors build against one rule instead of inferring it from daemon behaviour.

## Dependencies

- [`internal/noise`](noise-package.md) (#433) — `Responder`, `ReadInit`, `WriteResp`, `CipherState`, `KeyLen`. The wrapper's empty-AD-at-the-type-system invariant flows through to every AEAD operation here.
- [`internal/devices`](devices-package.md) — `Registry.Validate(plain)` predicate (two-state, bumps `LastSeenAt` under `reg.mu`).
- [`internal/dispatch`](dispatch-package.md) — `Handler`, `Conn`, `NewConn`, `Route` (#446). The same handler-table dispatch primitives used by v1's `Dispatcher`, factored out so the v2 manager does not duplicate the malformed/unsupported/unknown-type error-envelope logic.
- [`internal/protocol`](protocol-package.md) — `Envelope`, `RoutingEnvelope`, `HelloClientPayload`, `HelloAckPayload`, `ErrorPayload`, `InnerFrameV2`, `V2Version`, `TypeNoise*` constants, the `Token` field on `HelloClientPayload`, and (#618) `RequestSnapshotPayload` / `ScreenSnapshotPayload` + `TypeRequestSnapshot` / `TypeScreenSnapshot` / `CodeConversationNotFound` / `CodeServerBinaryOffline` (the #617 snapshot vocabulary), and (#727) `ModalCancelPayload` / `ModalAnswerPayload` / `ModalDismissedPayload` + `TypeModalCancel` / `TypeModalAnswer` / `TypeModalDismissed` (the #701 modal vocabulary), and (#707) `TypeInterrupt` (the bare, payload-less interrupt control type), and (#723) `TypeDequeueMessage` / `DequeueMessagePayload` (the #720 dequeue vocabulary the handler decodes), and (#812) `TypeDebugBundleChunk` / `TypeDebugBundleDone` + `DebugBundleChunkPayload` / `DebugBundleDonePayload` (the debug-bundle streaming vocabulary `StreamBundle` / `ReassembleBundle` emit and parse), and (#813) `TypeRequestDebugBundle` (the bare, payload-less inbound debug-bundle request verb the `dispatchAppFrame` probe intercepts), and (#831) `TypeNewSession` (the bare, payload-less inbound new_session control type), and (#844) `TypeSetSessionSettings` / `TypeSessionSettingsUpdated` + `SetSessionSettingsPayload` / `SessionSettingsUpdatedPayload` / `CodeSessionNotFound` (the set-session-settings request/reply vocabulary #845's handler decodes and replies with).
- [`internal/sessions`](sessions-package.md) (#840, via the `cmd/pyry` adapter only) — `Pool.UpdateSettings` / `SettingsUpdate` / `ErrSessionNotFound` are wrapped by `cmd/pyry`'s `settingsUpdaterAdapter`, never imported directly by `internal/relay`; the package speaks its own relay-local `SettingsUpdater` / `SettingsUpdate` / `ErrSessionUnknown` mirror instead (#845).
- [`internal/eventring`](eventring-package.md) (#646, consumed #647) — the bounded per-conversation event ring; the manager reads `Ring.After(convID, afterID)` (self-synchronised) on the reconnect-replay path. Late-bound via `SetReplaySource`, never imported at construction.
- [`github.com/coder/websocket`](relay-package.md#dependencies) — only for the `StatusCode` type aliasing the two new exported close codes.

## Related

- [`docs/specs/architecture/445-v2-inner-frame-handshake.md`](../../specs/architecture/445-v2-inner-frame-handshake.md) — handshake spec (transition table + AC reconciliation + security review).
- [`docs/specs/architecture/446-v2-noise-msg-application-dispatch.md`](../../specs/architecture/446-v2-noise-msg-application-dispatch.md) — open-state dispatch + AEAD-failure teardown spec.
- [`docs/specs/architecture/909-v2-appframe-concurrent-drain.md`](../../specs/architecture/909-v2-appframe-concurrent-drain.md) — the concurrent-drain fix for `dispatchAppFrame`'s reply-count deadlock (`security-sensitive`, full adversarial security review keeping `s.send.Encrypt` single-owner).
- [`docs/protocol-mobile.md`](../../protocol-mobile.md) §§ Authentication, Wire shapes, Failure modes, Error codes — wire-format source of truth.
- [ADR 024](../decisions/024-noise-ik-mobile-e2e.md) — Mobile Protocol v2 (Noise_IK) parent decision.
- [`docs/specs/architecture/845-set-session-settings-handler.md`](../../specs/architecture/845-set-session-settings-handler.md) — the `set_session_settings` handler spec (control flow, validator design, security review).
- [`docs/specs/architecture/874-push-drain-hold-unsealed-while-transport-down.md`](../../specs/architecture/874-push-drain-hold-unsealed-while-transport-down.md) — the transport-down hold spec (`Connected` probe design, concurrency model, full adversarial security review).
- [`docs/specs/architecture/875-flush-held-push-on-reconnect.md`](../../specs/architecture/875-flush-held-push-on-reconnect.md) — the eager reconnect-flush spec (`Reconnect` seam design, the fan-out-at-Connection-layer decision, concurrency model). Not security-sensitive.
- [`docs/specs/architecture/877-reconcile-modal-truth-on-connect.md`](../../specs/architecture/877-reconcile-modal-truth-on-connect.md) — the connect-time modal reconcile spec (`OutstandingModals` seam design, `reconcileModals` concurrency model, full adversarial security review). `security-sensitive`.
- [`docs/specs/architecture/878-reconcile-queue-truth-on-connect.md`](../../specs/architecture/878-reconcile-queue-truth-on-connect.md) — the connect-time queue reconcile spec (`OutstandingQueues` seam design, `reconcileQueues` concurrency model, full adversarial security review). `security-sensitive`.
- [codebase/845.md](../codebase/845.md) — the #845 implementation note.
- [`codebase/433.md`](../codebase/433.md) — `internal/noise` wrapper; the responder API this manager consumes.
- [`codebase/445.md`](../codebase/445.md) / [`codebase/446.md`](../codebase/446.md) — per-ticket implementation notes for the handshake and open-state slices.
- [`codebase/909.md`](../codebase/909.md) — `dispatchAppFrame` concurrent-drain fix; `forwardAppReply` extraction, the `routeDone`-gated two-arm select, and why the reply-count deadlock is fixed while the reply-latency head-of-line stall (per-conn fan-out follow-up) is not.
- [`codebase/452.md`](../codebase/452.md) — `V2Session.peerStatic` capture at the initial IK handshake; pure-data exposure for the re-key responder's peer-continuity check.
- [`codebase/454.md`](../codebase/454.md) — v2 `rekey_request` control-envelope discriminator at the `dispatchAppFrame` seam; logs-only `handleRekeyRequest`.
- [`codebase/453.md`](../codebase/453.md) — v2 re-key responder swap on open conn; `handleRekeyInit`, peer-static continuity check, atomic CipherState swap.
- [`codebase/450.md`](../codebase/450.md) — v2 re-key initiator on binary side; 1-hour `rekeyTimer`, AEAD-sealed `rekey_request` emit under `s.send`, 30 s `rekeyReplyTimer`, `rekeyComplete` seam, wake-channel routing of `time.AfterFunc` callbacks.
- [`codebase/459.md`](../codebase/459.md) — `internal/control` rekey verb wire + `Rekeyer` interface + `control.Rekey` client helper + `control.ErrConnNotFound` sentinel; the wire contract that `(*V2SessionManager).Rekey` (#462) implements.
- [`codebase/462.md`](../codebase/462.md) — manager-side manual-rekey trigger; `Rekey` method + `manualRekey` channel + `handleManualRekey` + `emitRekeyRequest(reason)` refactor + `ErrConnNotFound`/`ErrSessionNotOpen` sentinels.
- [`codebase/774.md`](../codebase/774.md) — the in-repo idle sweep; `idleTimeout` package var + `StatusIdleTimeout` (4408) + `wakeIdleTimeout`, the `idleTimer`/`lastActivityAt` per-session fields, `armIdleTimer` (copies `armRekeyTimer`), the `handleWake` reschedule/teardown arm, the `handleFrame` activity stamp, and the `closeWith` idle-timer stop+nil. Reuses the #450 wake-channel machinery and the #446 `closeWith` teardown; bounds the idle lifetime of the two Noise `CipherState`s under connect/disconnect churn.
- [`codebase/549.md`](../codebase/549.md) — daemon cutover behind `PYRY_MOBILE_V2=1`; `NewV2SessionManager`'s first production caller.
- [`codebase/571.md`](../codebase/571.md) — concurrency-safe server-initiated push; the original `Push` method + `pushReq`/`push` channel + `handlePush`, the structural twin of the #462 rekey funnel. The primitive the [#589](../codebase/589.md) assistant-turn bridge consumes.
- [`codebase/610.md`](../codebase/610.md) — backpressure + droppable-delta drop policy (`security-sensitive`); the #571 synchronous funnel rewritten non-blocking. Removes `pushReq`/`push`, adds `pushMu`/`queues`/`drainCh` + the `pushQueue` bounded FIFO + `drainOnce`; renames `handlePush`→`forwardEnvelope`. Closes the ADR-025 line-220 open risk that a slow relay wedges the [#633](../codebase/633.md) producer.
- [`codebase/588.md`](../codebase/588.md) — concurrency-safe open-session enumeration; `ActiveConnIDs` method + `snapshotReq`/`snapshot` channel + `handleActiveConnIDs` (renamed to `handleActiveConns` in #626), the structural twin of the #571 push funnel (seal/marshal steps dropped). The enumeration half #571 deferred; with `Push` it completes the fan-out primitive the [#589](../codebase/589.md) bridge consumes.
- [`codebase/589.md`](../codebase/589.md) — the v2 assistant-turn bridge: the production consumer of `Push` + `ActiveConnIDs`, fanning finished assistant turns to every open v2 phone.
- [`codebase/618.md`](../codebase/618.md) — the inbound screen-snapshot handler (`security-sensitive`); `ScreenSnapshotter` interface, the two optional `V2SessionConfig` seams, the `dispatchAppFrame` snapshot arm, `handleRequestSnapshot` + `snapshotReplyError`, and the `(*supervisor.Supervisor).ScreenSnapshot` render seam. Reuses `forwardEnvelope` (renamed from `handlePush` in #610), never the public `Push`.
- [`codebase/617.md`](../codebase/617.md) — the screen-snapshot wire vocabulary (`request_snapshot` / `screen_snapshot` payloads + `Type` constants) #618 consumes.
- [`codebase/848.md`](../codebase/848.md) — wires the `SnapshotSettings` seam into `handleRequestSnapshot`, populating the `model`/`effort`/`yolo` fields #847 shipped unwired. Not `security-sensitive`.
- [`codebase/856.md`](../codebase/856.md) — `internal/contextwindow.Read`, the context-window usage reader #857 wires into `handleRequestSnapshot`. Not `security-sensitive`.
- [`codebase/857.md`](../codebase/857.md) — wires the `SnapshotUsage` seam into `handleRequestSnapshot`, populating the `used_tokens`/`window_tokens` fields at their zero values until this ticket; mirrors #848, including the "predicted-vs-actual seam" correction to which cmd/pyry resolver it reuses. Not `security-sensitive`.
- [`codebase/727.md`](../codebase/727.md) — the inbound modal-control interception (`security-sensitive`); the consumer-declared `ModalResolver` seam + `ModalDismissal`, the optional `V2SessionConfig.ModalResolver` field, the two `dispatchAppFrame` arms (`handleModalCancel` / `handleModalAnswer`), and the `broadcastModalDismissed` fan-out (reads `m.sessions` directly, never `ActiveConns` — deadlock). `modal_cancel` resolves (consume → ESC → audit → broadcast); `modal_answer` shipped a deferred no-op there, since filled by the #717 gated answer arm (see [`codebase/717.md`](../codebase/717.md)). The `cmd/pyry` `modalResolverV2` impl consumes the #716 registry (`Resolve`), the #726 `SendEsc` seam, and the #712 audit sink. See also [`features/modalbridge-package.md`](modalbridge-package.md) (the outbound `modal_shown` half).
- [`codebase/707.md`](../codebase/707.md) — the inbound `interrupt` → Esc routing (`security-sensitive`); the consumer-declared `Interrupter` seam (reusing the #726 `SendEsc` surface), the optional `V2SessionConfig.Interrupter` field, the `dispatchAppFrame` interrupt arm, and `handleInterrupt` (the **first** inbound frame gated on the `interactive` capability itself — a one-line `if !s.interactive`, not an abstraction). Maps to the neutral `turnevent.Cancel` (declared in #707, routed via the seam not constructed — the `modal_cancel` precedent).
- [`codebase/831.md`](../codebase/831.md) — the inbound `new_session` → `/clear` routing (`security-sensitive`, split from #824); the consumer-declared `SessionStarter` seam (reusing the #830 `StartNewSession` surface), the optional `V2SessionConfig.SessionStarter` field, the `dispatchAppFrame` new_session arm, and `handleNewSession` (a line-for-line `handleInterrupt` mirror; reuses the `interactive`-capability-is-the-authorization posture #707 established). Unlike `interrupt` it maps to no neutral `turnevent` command — it drives the seam directly. Consumes the #830 sealed keystroke surface; the client observes the break via the pre-existing #656/#657 `session_transition` marker (no new emitter, no ack path).
- [`codebase/812.md`](../codebase/812.md) — the debug-bundle streaming primitive (`security-sensitive`); `StreamBundle` (loops `Push`, never the synchronous handler-reply channel), the pure `bundleEnvelopes` chunker, the exported `ReassembleBundle` receiver-contract oracle, and `bundleChunkBytes` (conservative const + `TestStreamBundle_EveryFrameWithinCap` cap enforcement). Rides the [#571/#610 push path](#concurrency-safe-unsolicited-push-571--push-method--push-funnel) unchanged — bundle chunks are control-class, never dropped. Shipped unwired; wired by #813. Streams the [#811 assembler](debugbundle-package.md)'s output.
- [`codebase/813.md`](../codebase/813.md) — the inbound `request_debug_bundle` verb (`security-sensitive`); the capstone wiring #811 (producer) + #812 (transport). The `DebugBundler` optional seam, the `dispatchAppFrame` interception arm, `handleDebugBundleRequest` (structural twin of `handleRequestSnapshot`, streams via `StreamBundle`, content-free logging), and `debugBundleReplyError` (static `msgDebugBundleUnavailable` — never the assembly error text). Pairing is the only gate (inherited at the Noise handshake); **not** capability-gated, unlike `interrupt`/`dequeue_message`. Capture-off withholds the recording structurally (no `recording.cast` member), not by a flag.
- [`codebase/911.md`](../codebase/911.md) — per-conn in-flight gate on `request_debug_bundle` (`security-sensitive`); `bundleInFlight` (new, `pushMu`-guarded scan of `q.items` for `TypeDebugBundleChunk`/`TypeDebugBundleDone`), the `handleDebugBundleRequest` gate call placed before `DebugBundler()`, and the corrected `pushQueue.enqueue`/`pushQueueCap` soft-overflow comments (the "unreachable in practice" premise #812's control-class bundle chunks falsified). See [Inbound debug-bundle request](#inbound-debug-bundle-request-813--request_debug_bundle--debugbundler--streambundle).
- [`codebase/1006.md`](../codebase/1006.md) — fake-daemon e2e capstone proving `request_debug_bundle` end-to-end over the encrypted v2 wire (`security-sensitive`; split from #962); happy-path stream/decode of a known archive (non-maskable arrival-order reassembly) and the load-bearing no-leak error path (sentinel absent from both wire and daemon logs, gated non-vacuous by a positive log-event poll). Introduces one small env-gated production seam, `cmd/pyry/debug_bundle_fake.go`'s `fakeDebugBundler`, inert unless `PYRY_ALLOW_INSECURE_RELAY=1` — the real `DebugBundler` closure is non-byte-predictable out-of-process (`logRing.Snapshot` tees every daemon log line), so no config surface could inject exact bytes. AC-3 (#911's in-flight gate) deferred to that ticket's deterministic in-package coverage.
- [`codebase/723.md`](../codebase/723.md) — the inbound `dequeue_message` → `msgqueue.Remove` handler (`security-sensitive`); the consumer-declared `QueueRemover` seam, the optional `V2SessionConfig.QueueRemover` field, the `dispatchAppFrame` dequeue arm, and `handleDequeueMessage` (the **second** inbound capability-gated frame, mirroring `handleInterrupt`'s shape; AC-4 convergence via the automatic #722 `OnChange` path, never a direct re-emit; the deliberate no-`KnownConversation`-gate security decision).
- [`codebase/647.md`](../codebase/647.md) — the inbound mid-turn reconnect-replay consumer (`security-sensitive`); `SetReplaySource` (late-bound ring), `replayMissed`/`emitResync`, the `handleNoiseInit` hook, the `replayThrough` watermark + `forwardEnvelope` guard, and `HelloClientPayload.LastEventID` / `TypeResync`. Shipped with a caught-up-watermark MUST FIX outstanding, resolved by [`codebase/663.md`](../codebase/663.md) (clamp to `min(afterID, NewestID(convID))`). Consumes the [`codebase/646.md`](../codebase/646.md) ring + [`codebase/649.md`](../codebase/649.md) outbound `event_id`.
- [`codebase/777.md`](../codebase/777.md) — paces that reconnect replay one event per `Run` pass (`security-sensitive`). Moves the inline forward loop onto a Run-owned `V2Session.replayQueue` drained by `drainReplayOnce` (cap-1 `replayCh` pump mirroring `drainCh`/`drainOnce`); a `drainOnce` gate holds a replaying conn's live push queue until its tail empties, preserving replay-before-live ordering without the old inline-completion guarantee. The classification (ring read, gap→resync, #663 clamp) stays inline; the seal stays single-writer on `Run` (send-nonce invariant untouched). Fixes the latent fairness cliff where a ≤ `MaxEventsPerConversation` replay monopolised the dispatch goroutine.
- [`codebase/874.md`](../codebase/874.md) — transport-down hold on the push drain (`security-sensitive`, layer 1 of the reconnect-reliability design, umbrella #829); the additive `V2SessionConfig.Connected` probe + `transportDown()` helper, the `drainOnce` pre-pop guard, and the `transport.Client.IsConnected` / `relay.Connection.Connected` passthroughs. See [Transport-down hold on the push drain](#transport-down-hold-on-the-push-drain-874--connected-probe--transportdown).
- [`codebase/875.md`](../codebase/875.md) — immediate flush-on-reconnect (layer 2, not security-sensitive); the additive `V2SessionConfig.Reconnect` seam, the `Run` re-signal arm, and the `relay.Connection.reconnected`/`Reconnected()` fan-out one layer below the single-observer transport channel. See [Immediate flush-on-reconnect](#immediate-flush-on-reconnect-875--reconnect-signal--connectionreconnected-fan-out).
- [`codebase/877.md`](../codebase/877.md) — connect-time modal reconcile (`security-sensitive`; the modal half of the reconcile-on-connect mechanism, umbrella #829); the additive `V2SessionConfig.OutstandingModals` seam, the `reconcileModals` helper (structural sibling of `broadcastModalDismissed`, unicast not fan-out), and the `handleNoiseInit` call site ahead of the #647 replay hook. Consumes [`codebase/876.md`](../codebase/876.md)'s `Registry.Snapshot()`. See [Connect-time modal reconcile](#connect-time-modal-reconcile-877--outstandingmodals-seam--reconcilemodals).
- [`codebase/878.md`](../codebase/878.md) — connect-time queue reconcile (`security-sensitive`; the queue half of the reconcile-on-connect mechanism, umbrella #829; twin of #877); the additive `V2SessionConfig.OutstandingQueues` seam, the `reconcileQueues` helper (structural twin of `reconcileModals`), and the `handleNoiseInit` call site immediately after `reconcileModals`. Consumes [`msgqueue-package.md`](msgqueue-package.md)'s new `SnapshotAll` enumeration seam via the `cmd/pyry` `outstandingQueues` adapter, and reuses the #722 `toQueueStatePayload` mapping — no new payload type. See [Connect-time queue reconcile](#connect-time-queue-reconcile-878--outstandingqueues-seam--reconcilequeues).
- [`codebase/626.md`](../codebase/626.md) — capability negotiation on the handshake (`security-sensitive`); `supportedV2Capabilities` + `negotiateCapabilities`, the `hello_ack` echo, the `s.interactive` flag, and the capability-aware `ActiveConns`/`ActiveConn` enumeration (`ActiveConnIDs` becomes a projection). The daemon-side trust decision [#607](../codebase/607.md) deferred.
- [`codebase/607.md`](../codebase/607.md) — the v2 interactive wire vocabulary (`CapabilityInteractive`, the `Capabilities []string` fields) that #626 enforces.
- [ADR 025](../decisions/025-mobile-remote-head-interactive-session.md) — § Safe degradation (the parser-independent snapshot floor) and § Security model (line 141: read-only screen viewing outside the per-device permission gate).
- [`features/dispatch-package.md`](dispatch-package.md) — `Route` and `NewConn` (the production-allowed counterpart to `NewTestConn`).
- [`features/relay-package.md`](relay-package.md) — the v1 surfaces of `internal/relay`.
