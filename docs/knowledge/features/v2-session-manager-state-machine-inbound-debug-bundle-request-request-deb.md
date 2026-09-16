# Inbound debug-bundle request (#813) — `request_debug_bundle` → `DebugBundler` → `StreamBundle`

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
  `handleRequestSnapshot`, though since [#1491](../../specs/architecture/1491-assemble-debug-bundle-off-run.md)
  it only **accepts** the request on the manager's single `Run` dispatch
  goroutine; the seam call that used to run inline here now runs off it (below).
  Every branch either accepts exactly one assembly or sends exactly one error
  reply, then returns — it never panics, hangs, or silently drops:
  1. **`m.cfg.DebugBundler == nil`** → deterministic error reply (feature
     unavailable / foreground / v1 / unwired), return.
  2. **Gate busy** (either half — see below) → the same deterministic error
     reply, return. The seam is never invoked for a rejected retry.
  3. **Accept** → `s.bundleAssembling = true`, then `go m.assembleBundle(ctx, s,
     env.ID)` and return, freeing `Run` for its next select pass.
- **`assembleBundle(ctx, s, inReplyTo)`** — the off-`Run` goroutine, one per
  accepted request (bounded to one per conn by the gate). Calls
  `m.cfg.DebugBundler()` — in production the uncapped `io.CopyN` read of the
  newest recording plus the in-memory gzip that used to park `Run` for the whole
  duration — then funnels `(archive, err)` back via a **blocking** send on
  `m.bundleReady`, with `<-s.done` and `<-ctx.Done()` as the only escapes. The
  send blocks rather than drops (unlike the `drainCh` idiom): a dropped result
  would leave `s.bundleAssembling` set forever and lock the conn out of its own
  bundle permanently — this goroutine exists to carry exactly one result, so
  parking it costs nothing. It touches nothing `Run` owns: only
  `m.cfg.DebugBundler` (immutable after construction, like the `m.cfg.Logger`
  `Push` already reads off-`Run`) and `s.done` (created on `Run`, closed once by
  `closeWith`, the same field `appFrameWorker` already reads off-`Run`) — never
  `s.send`, `s.recv`, `s.state`, or `m.sessions`, so no cryptographic operation
  can run concurrently with `Run`'s and reuse a nonce.
- **`handleBundleReady(ctx, res)`** — the `Run` arm fed by `m.bundleReady`.
  Every step that used to run inline in `handleDebugBundleRequest` runs here
  instead, in the same place it always ran — on `Run`, where `s.send` is
  single-owned — so moving the seam call off `Run` moved nothing else:
  1. `defer func() { res.s.bundleAssembling = false }()` — deferred so every
     exit path below releases the gate; a branch-local clear would be one
     missed early-return away from the permanent lockout AC #2 forbids.
  2. **`res.s.state != V2StateOpen`** → the conn tore down between accept and
     result (`closeWith` ran); drop (debug log), no reply — sealing under a
     dead session would burn a send-nonce nobody awaits. A reconnected
     same-`conn_id` session is a *different* pointer with a false marker, so
     there is nothing to consult in `m.sessions` here.
  3. **`res.err != nil`** → log the failure **event only**
     (`v2.bundle.assemble_err`, `conn_id` — **never** the wrapped err, which
     could quote a recording path/filename) at warn, then error reply. Honours
     #811's read-failure honesty: a recording that exists but fails to read
     surfaces as an error, not a false-absent.
  4. **`m.StreamBundle(ctx, res.s.connID, res.archive)`** → enqueue the chunk
     stream. An `ErrConnNotFound` (unreachable given the state check above) is
     debug-logged and dropped (the package's outbound-drop posture); never the
     archive.
  5. **On success**, one content-free info log (`v2.bundle.served`, `conn_id` +
     `len(archive)` — a **byte count**, never the archive or any member) — AC #4.
- **`debugBundleReplyError(ctx, s, inReplyTo)`** — unchanged by #1491: sends one
  `TypeError` reply via the same `m.forwardEnvelope` seal-and-forward path
  (never `c.Send`), with a **fixed** `CodeServerBinaryOffline` +
  `retryable: true` + the static `msgDebugBundleUnavailable = "debug bundle
  unavailable"` message. Reached from three call sites now (nil seam, gate busy,
  assembly error) instead of the original two, and byte-identical from all of
  them, so **no** attacker-influenced or assembly-error text ever reaches the
  wire and the phone cannot distinguish "busy" from "offline"/"assemble-failed".

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
if s.bundleAssembling || m.bundleInFlight(s.connID) {
    m.debugBundleReplyError(ctx, s, env.ID)
    return
}
```

**Accept-side half (`s.bundleAssembling`, #1491) — closes the window the
queue-derived scan can't see.** Once [assembly moved off `Run`](#inbound-debug-bundle-request-813--request_debug_bundle--debugbundler--streambundle),
a gap opened between accept and `StreamBundle`'s first enqueue during which
`bundleInFlight` reads `false` — nothing is queued yet — even though an
assembly is already running. A second `request_debug_bundle` landing in that
gap would pass the gate and start a concurrent full-size assembly, reopening
exactly the unbounded per-retry growth this gate exists to prevent.
`V2Session.bundleAssembling` is the accept-side half: set on `Run` at accept,
cleared on `Run` via `defer` in `handleBundleReady` once `StreamBundle` has
returned — in the **same** `Run` pass as the enqueue, so the two halves
overlap rather than merely abut, and no other `Run` arm can interleave between
them to observe both `false` at once. It is `Run`-owned single-writer, the
same regime as `state`/`replayThrough` — no lock, no atomic, and `pushMu`
gains no new acquisition site. Living on the session, not a manager-level map,
is what makes the permanent lockout AC #2 forbids structurally impossible:
`closeWith` deletes the session, so a reconnecting `conn_id` gets a fresh
`V2Session` whose marker is the zero value — nothing to leak, nothing to
forget to clear.

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

**Reused for a second off-Run producer (#2477).** `new_session`'s wrap-up turn
can take up to 90 seconds and cannot answer its reply from `handleNewSession`
on Run either, for the identical reason `assembleBundle` moved off it. Its
`LateSessionStarter` shape copies this one verbatim — a bounded blocking send
(`newSessionDone`, escapes on `s.done`/`ctx`) to a channel Run selects on
(`handleNewSessionDone`), with `handleBundleReady`'s staleness guard reused
for the same reason (a torn-down session must not seal a reply nobody
awaits). See [Inbound new_session § The wrap-up turn, and the reply's
tense](v2-session-manager-state-machine-inbound-new-session-sessionstarter-seam.md#the-wrap-up-turn-and-the-replys-tense-2477).
The generalisable shape, now instantiated twice: an off-Run producer that
owes a caller a reply funnels its outcome back to Run on a dedicated channel
rather than answering from its own goroutine, because the reply's seal
(`s.send.Encrypt` via `forwardEnvelope`) is single-owned there.

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
