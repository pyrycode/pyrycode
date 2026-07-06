# Spec #801 — Live-wire the permission proxy into the ACP session

Wire the already-built, unit-tested `acpPermissionProxy` (#752) into the running
`pyry acp` session so a real claude permission modal drives an outbound
`session/request_permission` Call to the ACP host and the host's choice routes
back as the resolving keystroke (ADR 027 divergence 2). This is **pure wiring** —
the round-trip / route / deny / one-shot logic already exists in the proxy; this
ticket supplies the three collaborators and the per-session lifecycle. It is the
ACP twin of #796 (which live-wired the outbound turn stream) and the last unwired
ACP outbound adapter before the #754 conformance capstone.

## Files to read first

- `cmd/pyry/acp_permission.go` (whole file, ~316 lines) — **the proxy this ticket
  wires.** Do NOT modify it. Key contracts to extract:
  - `newACPPermissionProxy(caller permissionCaller, kb modalKeystroker, sessionID string, timeout time.Duration, logger *slog.Logger) *acpPermissionProxy` (`:86`) — the constructor this ticket calls.
  - `Handle(ctx context.Context, ev tuidriver.Event)` (`:108`) — the modal-event sink; gates internally on `EventKindPtyModalShown`+`ModalClassPermission` (start round-trip) and `EventKindPtyModalHidden` (retire). Everything else is a no-op. **The drain calls this on every event; the proxy does the filtering.**
  - The type doc (`:36–55`) — the default-safe-deny security argument and the content-free-logging posture the wiring must not weaken.
  - `handleShown` (`:124`) spawns `go p.runRoundTrip(cctx, …)` where `cctx = context.WithTimeout(ctx, p.timeout)`. The `ctx` it receives IS the drain's ctx → the round-trip inherits teardown-cancel and the answer window from the same parent. This is load-bearing for the timeout AC and the no-leak AC.
- `cmd/pyry/acp_turn_streams.go` (whole file, 177 lines) — **the per-session manager to extend.** `start(id)` (`:82`) is where the one new line goes; `newACPTurnStreams` / `attach` / `wait` / the `started` idempotency map / the `wg` are all reused as-is. Extract the exact `resolve`-closure + `NewTargetSubscriber` + fresh-`Tracker` shape at `:107–123` — the permission drain builds an identical fixed target.
- `cmd/pyry/interactive_modal_stream_v2.go:110–153` — `runModalStream`, the mobile leg's mapper-free raw-`tuidriver.Event` drain. **`runPermissionModalStream` is its ACP sibling, but simpler** (no `screenText`, no kind pre-filter — the proxy's `Handle` filters). Also read the type/function doc at `:22–53` for the "second `Session.Events()` subscription is blessed by tui-driver" argument you'll cite.
- `cmd/pyry/acp.go:88–168` — `serveACPWithPool`. Read to confirm the wiring needs **no change here**: `streams` already receives `runCtx`/`pool`/`claudeSessionsDir`/`logger` and gets the transport via `streams.attach(t)` in the register closure (`:130`). The permission proxy's three collaborators are all reachable from inside the manager.
- `cmd/pyry/interactive_turn_stream_v2.go:329` — `resolveBoundSessionJSONL(dir, sessionID string) func(ctx) (path string, startOffset int64, err error)`. Reuse verbatim as the fixed-target JSONL resolver (fresh per subscription for the #671 cold/warm offset reset).
- `cmd/pyry/acp_turn_streams_test.go` (whole file) — **the test patterns to mirror.** In particular:
  - `TestACPTurnStreams_ScriptedTurnEmitsOrderedFrames` (`:35`) — drives events through the wired drain via `scriptedSubscriber`, NOT through `start()`. The permission AC1/AC2 tests mirror this shape exactly.
  - `TestACPTurnStreams_LifecycleTeardownJoinsProducer` (`:133`) — the real-fake-claude-pool lifecycle test. After this ticket, `start(id)` also spawns the permission drain, so this test's `wait()` now joins **two** goroutines per session; verify it still returns (it will — the permission drain also exits on ctx cancel). This is free AC4 coverage.
  - `TestACPTurnStreams_EmptyDirDisablesStreaming` (`:187`) — asserts `dir == ""` disables the whole `start()`; the permission drain is disabled by the same shared guard, so this test stays green unchanged.
- `cmd/pyry/interactive_turn_stream_v2_test.go:550–590` — `scriptedSubscriber` (`streams []<-chan tuidriver.Event`, `subscribe(ctx)`, `callCount`, `resolveCount`). Reuse verbatim; its `subscribe` satisfies `turnbridge.Subscriber`, which is what `runPermissionModalStream` consumes. **This is the AC3 seam.**
- `cmd/pyry/acp_permission_test.go` (whole file) — the proxy's existing unit tests. Extract the `permissionCaller` / `modalKeystroker` test doubles and `generousTimeout`; the wired tests build the proxy over the same doubles but drive it through the drain.
- `internal/turnbridge/producer.go:25–63,191–300` — `Subscriber` / `SessionHost` / `Target` / `NewTargetSubscriber`. Extract why the modal drain still needs a JSONL `Resolve` (`sess.Events(ctx, path, off, tr)` is JSONL-gated) — this is why `dir == ""` disables the permission drain too.
- `internal/supervisor/modal.go:52–66` — `Answer` / `SendEsc` (`*supervisor.Supervisor` satisfies `modalKeystroker`). No change; read to confirm the keystroke contract (best-effort, `ErrNoLiveSession` when no child).
- `internal/relay/v2session.go:81` — `modalDenyTimeout = 2 * time.Minute`, the daemon's existing deny-on-timeout window. The ACP permission Call deadline mirrors this value for policy consistency.

## Context

**Epic #600 — `pyry acp` as a thin adapter over the shared remote-head core.**
ADR 027 divergence 2: an ACP permission decision is a blocking agent→client
request. #752 built `acpPermissionProxy` — the adapter that issues the outbound
`session/request_permission` and routes the reply back into claude's modal — but
shipped it **unwired**: `newACPPermissionProxy` has zero non-test callers and its
own doc says so (`acp_permission.go:59`). The outbound turn stream had the
identical shape (built #750, wired #796). This ticket does for permission exactly
what #796 did for the turn stream: give the frozen adapter its per-session
lifecycle and event source.

All three collaborators the proxy needs are already present at the per-session
layer inside `serveACPWithPool`, so no round-trip logic is (re)designed here:

| Proxy collaborator | Supplied by | Already at hand |
|---|---|---|
| `permissionCaller` (`Call(ctx, method, params)`) | `*acp.Transport` | `m.transport`, set by `streams.attach(t)` (`acp.go:130`); `acp.Transport.Call` matches the interface exactly (`internal/acp/acp.go:292`) |
| `modalKeystroker` (`Answer`/`SendEsc`/`AcceptTrust`) | `sess.Supervisor()` | `host := sess.Supervisor()` already computed in `start()` (`acp_turn_streams.go:105`) |
| permission-modal event source | `sess.Supervisor()`'s raw tui-driver stream | the same `NewTargetSubscriber` fixed-target path the turn stream follows |

## Design

### Placement: fold into the existing per-session manager

`acpTurnStreams` already owns exactly the collaborators and the lifecycle the
permission proxy needs: `ctx` (runCtx), `pool`, `dir`, `transport` (via `attach`),
`logger`, the `started` idempotency map, and the `wg` that `wait()` joins. Rather
than duplicate that entire lifecycle in a parallel manager (and thread a second
`start`/`wait`/`attach` through `acp.go` and both session handlers), the
permission drain is **folded into the same `start(id)`**: one manager, one
`started` mark per session guarding both goroutines, one `wait()` joining both.

This is the minimal-surface choice — it touches `acp.go` not at all and changes
no constructor signature. The cost is that `acpTurnStreams` now owns **two**
outbound adapters per session (turn producer + permission drain); its type doc is
broadened to say so. A rename to e.g. `acpSessionStreams` was considered and
**rejected**: it fans out across `acp.go` + `newSessionHandler`/`loadSessionHandler`
+ two test files for zero behavioural gain, and "turn streams" reads acceptably as
"the session's outbound streams." Keep the name; broaden the doc.

### New file: `cmd/pyry/acp_permission_streams.go`

Three things, ~50 lines total:

1. **Timeout constant.**
   ```go
   // acpPermissionTimeout bounds each outbound session/request_permission Call: a
   // host that never answers denies-and-unblocks after this window rather than
   // wedging claude. Mirrors the daemon's modalDenyTimeout (relay/v2session.go:81)
   // for policy consistency across the mobile and ACP permission legs.
   const acpPermissionTimeout = 2 * time.Minute
   ```
   A package const, not a constructor param — the value is fixed policy, so
   plumbing it through `newACPTurnStreams` would be churn for no configurability.

2. **`startPermissionProxy` — the manager glue** (method on `*acpTurnStreams`,
   same package). Signature + behaviour only:
   ```go
   func (m *acpTurnStreams) startPermissionProxy(host *supervisor.Supervisor, sessionID string)
   ```
   - constructs `newACPPermissionProxy(m.transport, host, sessionID, acpPermissionTimeout, m.logger)` — `host` is both the `modalKeystroker` (kb) and, below, the `SessionHost`;
   - builds a fixed-target subscriber identical to the turn stream's: `resolve` closure returning `turnbridge.Target{Host: host, Resolve: resolveBoundSessionJSONL(m.dir, sessionID), Switch: nil}`, wrapped by `turnbridge.NewTargetSubscriber(resolve, tuidriver.NewTracker(tuidriver.TrackerOpts{}), m.logger)` — a **fresh** Tracker (its own merge loop, no shared parse state with the turn stream's Tracker);
   - `m.wg.Add(1)` then `go func() { defer m.wg.Done(); runPermissionModalStream(m.ctx, sub, proxy) }()`.

3. **`runPermissionModalStream` — the drain and the AC3 seam.** The raw-event
   drain, structurally the ACP sibling of `runModalStream` but simpler (no
   screen-text arg, no per-kind pre-filter):
   ```go
   func runPermissionModalStream(ctx context.Context, sub turnbridge.Subscriber, proxy *acpPermissionProxy)
   ```
   Behaviour — the outer re-subscribe / inner drain loop, verbatim in shape from
   `runModalStream`/`Producer.Run`:
   - outer: `ch, err := sub(ctx)`; a non-nil err is ctx-cancel (Subscriber contract) → return;
   - inner `select`: `<-ctx.Done()` → return; `ev, ok := <-ch`; `!ok` (session restart) → break to re-subscribe; otherwise `proxy.Handle(ctx, ev)`;
   - after the inner loop, `if ctx.Err() != nil { return }`, else re-subscribe (follow a session restart).
   - Logs nothing itself (mirrors `runModalStream`): the proxy owns all logging, all content-free.
   This function is production code AND the deterministic test seam — a test drives
   it with `scriptedSubscriber.subscribe` (a `turnbridge.Subscriber`) + a proxy over
   scripted doubles, injecting a permission-modal event with no live claude. This
   is the seam #754 consumes to observe divergence 2.

### One-line change in `cmd/pyry/acp_turn_streams.go`

Inside `start(id)`, immediately after the turn producer goroutine is spawned (the
existing `m.wg.Add(1); go func(){ … prod.Run … }()` block), add:
```go
m.startPermissionProxy(host, sessionID)
```
`host` (`*supervisor.Supervisor`) and `sessionID` (`string(id)`) are both already
in scope. Also broaden the type doc comment (`:14–33`) to note the manager now
owns the per-session **permission proxy drain** alongside the turn producer, both
disabled together when `dir == ""` and both joined by `wait()`.

### Why the modal drain still needs the JSONL resolver

`NewTargetSubscriber` opens the unified event stream via
`sess.Events(subCtx, path, off, tr)`, which is JSONL-gated
(`WaitForSessionJSONL` before open — `producer.go:253–278`). Modal events are
PTY-sourced, but the subscriber surfaces them *through* the JSONL-tailing stream,
so a valid JSONL `path` is required to open it at all. Hence `resolveBoundSessionJSONL`
is reused, and hence `dir == ""` (no `$HOME` / unresolvable sessions dir) disables
the permission drain by the same `start()` guard that disables the turn stream —
correct, because the drain cannot function without the JSONL path, and in
production `runACP` always supplies a non-empty `claudeSessionsDir`.

## Concurrency model

- **Two goroutines per session, one join.** `start(id)` now spawns the turn
  producer (`prod.Run`) and the permission drain (`runPermissionModalStream`),
  both under `m.wg`, both parented on `m.ctx` (runCtx). `wait()` (called after
  `cancel()` in `serveACPWithPool`) joins both. The `started` map marks the id
  once, so a duplicate `start(id)` (session/load after session/new) re-spawns
  neither — exactly one of each per session.
- **Second `Session.Events()` subscription is blessed.** The permission drain
  opens a second `Session.Events()` on the same bound `*tuidriver.Session`
  alongside the turn stream's. tui-driver explicitly supports this (two `Events()`
  calls = two independent merge loops over their own JSONL tail + poll, sharing no
  mutable state — see `interactive_modal_stream_v2.go:35–39`). A separate `Tracker`
  per subscription keeps that invariant.
- **The proxy's single-goroutine invariant holds.** `runPermissionModalStream` is
  the sole caller of `proxy.Handle`, so `p.inflight` (touched only by the drain
  goroutine) never races (`acp_permission.go:63–68`). The cross-goroutine handoff
  to the detached round-trip goroutine is the proxy's own `atomic.Bool` one-shot.
- **Round-trip goroutine lifecycle (no leak).** `handleShown` spawns a detached
  `runRoundTrip` with `cctx = context.WithTimeout(m.ctx, acpPermissionTimeout)`.
  Because `cctx` descends from `m.ctx`, teardown-cancel unblocks any in-flight
  `Call` immediately: it returns `ctx.Err()`, `route` denies (ESC), the goroutine
  exits in microseconds. It is not `wg`-joined (the proxy is frozen; the wiring
  can't join it), but it is ctx-bounded and cannot outlive teardown — the drain is
  the joined unit, matching the turn stream's discipline. `runRoundTrip`'s
  `defer rt.cancel()` releases the deadline timer on every exit.
- **Shutdown sequence** (unchanged in `serveACPWithPool`): signal/EOF → `cancel()`
  → every drain's `sub(ctx)`/`select` observes ctx.Done and returns, every
  in-flight round-trip's `cctx` fires and denies → `streams.wait()` joins all
  drains → `<-poolErr`.

## Error handling

- **Default-safe deny is preserved in the wired path.** Every non-grant path —
  timeout (Call `cctx` deadline), transport error, teardown-cancel, decode
  failure, cancelled outcome, forged/unknown optionId — routes ESC. This is the
  proxy's existing behaviour (`route`, `acp_permission.go:174–208`); the wiring
  adds no path that bypasses it. AC2 asserts it holds through the drain, not only
  in the isolated adapter.
- **Keystroke failure is best-effort** (`answer`/`deny` swallow `Answer`/`SendEsc`
  errors as content-free Warn): a modal resolved against a torn-down session has
  nothing to roll back. Unchanged.
- **Subscriber transients** (JSONL not yet present, session mid-restart) are
  retried inside `NewTargetSubscriber` with backoff; the drain only ever sees a
  live channel or ctx-cancel. A session restart closes the channel → the drain
  re-subscribes onto the now-live session (follow-restart), same as the turn
  producer.
- **`dir == ""`** disables the permission drain (shared `start()` guard); claude's
  permission modals then go unanswered in that degrade, consistent with the turn
  stream being off — this path only occurs with no resolvable `$HOME`, where the
  whole outbound leg is disabled anyway.

## Testing strategy

New test file `cmd/pyry/acp_permission_streams_test.go`, reusing `scriptedSubscriber`
(from `interactive_turn_stream_v2_test.go`), the `permissionCaller`/`modalKeystroker`
doubles + `generousTimeout` (from `acp_permission_test.go`), and `discardLogger`/
`waitClosed`/`newFakeClaudePool` (from the turn-stream test harness). Scenarios as
bullets — the developer writes them in the project's table-driven idiom:

- **AC1 — round-trip through the drain (happy path).** Drive
  `runPermissionModalStream(ctx, scriptedSub.subscribe, proxy)` in a goroutine;
  `proxy` built over a scripted `permissionCaller` that records the Call params and
  returns `{"outcome":{"outcome":"selected","optionId":<a surfaced id>}}`, and a
  recording `modalKeystroker`. Feed one `tuidriver.Event{Kind: EventKindPtyModalShown, Modal: ModalClassPermission}`.
  Assert: exactly one Call to `session/request_permission`; its params carry
  `sessionId` == the session id and the four options in fixed order with kinds
  `allow_once`/`allow_always`/`reject_once`/`reject_always`; the selected optionId
  routed back as `Answer("<digit>")` where digit == option index + 1; no `SendEsc`.
  (Proves the wiring, not just the proxy — the analog of
  `TestACPTurnStreams_ScriptedTurnEmitsOrderedFrames`.)
- **AC2 — default-safe deny on a never-answering host.** `permissionCaller.Call`
  blocks until its ctx is cancelled, then returns `ctx.Err()`; proxy built with a
  short timeout (e.g. tens of ms). Feed a permission ModalShown. Assert: after the
  window, the keystroker recorded `SendEsc` (deny), never `Answer`. Deny survives
  in the wired path.
- **AC3 — the seam is the vehicle.** AC1/AC2 *are* the AC3 evidence: both drive a
  scripted permission-modal event through `runPermissionModalStream` +
  `scriptedSubscriber`, deterministically, with no live claude modal. Add a one-line
  test comment naming this as the seam #754 consumes.
- **AC4 — lifecycle teardown / no leak.**
  - Extend the existing real-pool lifecycle test (or add a sibling): after
    `streams.start(id)` (which now spawns both goroutines) and a duplicate
    `start(id)`, assert `len(streams.started) == 1`; then `mgrCancel()` and assert
    `streams.wait()` returns within a deadline — it must join the permission drain
    as well as the turn producer.
  - In-flight round-trip teardown: drive a modal event so a round-trip is blocked
    in the scripted Call, cancel the drain ctx, assert the drain returns AND the
    round-trip resolved to deny (Call unblocked via `cctx` cancel → ESC). Proves no
    in-flight goroutine outlives teardown.
- **Content-free logging (preservation).** A no-leak-style test driving a host
  reply through the wired chain against a scripted caller must show no host
  `optionId`, option label, or modal body in a debug-level log buffer — mirrors
  `TestACPTurnStreams_NoAppContentLogLeak`. The proxy already enforces this; the
  test guards the wired path.
- **Regression check the developer must run:** `TestACPTurnStreams_LifecycleTeardownJoinsProducer`
  and `TestACPTurnStreams_EmptyDirDisablesStreaming` now also exercise the
  permission drain (via the shared `start()`). Confirm both still pass unchanged —
  the empty-dir guard disables both goroutines; the lifecycle `wait()` joins both.
- **AC5 — `make check` green** (`go vet`, `staticcheck`, `go test -race`).

## Open questions

- **Round-trip goroutine join.** The detached `runRoundTrip` is ctx-bounded but
  not `wg`-joined (the frozen proxy owns its spawn). If a future ticket wants a
  hard join for a `goleak`-style assertion, that requires a proxy change and is out
  of scope here. The design's position: ctx-bounded exit is sufficient for AC4's
  "no leaked goroutine," demonstrated by the in-flight teardown test.
- **Timeout value.** `2 * time.Minute` mirrors `modalDenyTimeout`. If the ACP host
  UX wants a different permission-answer window than the mobile deny-on-timeout,
  that's a one-line const change in a follow-up; not designed as configurable now.

## Security review

**Verdict:** PASS

This ticket wires an outbound tool-permission **policy** surface (default-safe-deny),
hence the `security-sensitive` label (per #752). The ACP stdio transport is
local-trusted (epic #600) and not itself a network threat surface; the label
tracks the permission-decision design. The adversarial question walked: *can a
hostile or buggy ACP host reply, a confused developer, or teardown coerce an
unintended tool ALLOW, wedge claude, or leak content?*

**Findings:**

- **[Trust boundaries]** No finding. The untrusted datum is the host's reply to
  `session/request_permission`. It crosses to trusted at a single explicit point —
  the proxy's `route` (`acp_permission.go:174–208`): the **only** path to an allow
  keystroke is `outcome:"selected"` **and** `optionId ∈ rt.options` (membership via
  `slices.IndexFunc`); the routed digit is `idx+1` from the **daemon-built** option
  order, never a host-supplied value. Every other reply (cancelled, decode error,
  forged/unknown optionId, null) denies. The wiring supplies the transport as
  `permissionCaller` and drives `Handle`; it never inspects the reply, so the
  boundary is inherited from #752 intact.
- **[Tokens/secrets]** N/A — no tokens, keys, or credentials. The `optionId` is a
  daemon-minted, class-fixed (`PermissionRequestForClass`), non-secret identifier;
  the host is *handed* the option set and can only select within it. Never logged.
- **[File operations]** No finding. The drain builds a JSONL path via
  `resolveBoundSessionJSONL(m.dir, sessionID)`, identical to the already-merged turn
  stream (#796). `m.dir` is the daemon's own `$HOME`-confined `claudeSessionsDir`
  (`acp.go:66`); `sessionID` is always a pool-resident id — `Pool.Create`-minted, or
  (session/load) `Pool.Lookup`-validated **before** `start()` is reached, so a host
  `../`-shaped id fails Lookup (→ CodeInvalidParams) and never reaches path
  construction. No new traversal surface; the id validation is upstream and shared.
- **[Subprocess]** N/A — no command execution. The resolving keystroke is a PTY
  write via `Answer`/`SendEsc`, not `exec`.
- **[Cryptographic primitives]** N/A — no RNG/keys. The optionId membership check is
  equality on non-secret identifiers; it guards no secret, so constant-time
  comparison is not required.
- **[Network & I/O]** No finding. The outbound Call runs over local-trusted ACP
  stdio. DoS resistance: the `acpPermissionTimeout` (2 min) Call deadline is the
  never-answering-host mitigation — verified `acp.Transport.Call` honors ctx and
  reclaims the pending slot (`acp.go:313–322`), so the host denies-and-unblocks
  rather than wedging claude (AC2). Goroutine exhaustion: tui-driver's single-modal
  invariant plus the proxy's defensive `retireInflight` bound in-flight round-trips
  to at most one per session.
- **[Error messages / logs]** No finding. Content-free posture preserved: the proxy
  logs only discriminants + a supervisor sentinel err (`acp_permission.go:52–55,
  217–247`) — never the host optionId, option label, or modal body; the new drain
  and glue (`runPermissionModalStream`, `startPermissionProxy`) log nothing. The
  wiring introduces no content-carrying log site. Guarded by the content-free-leak
  test in the testing strategy.
- **[Concurrency]** No MUST-FIX. The Call is issued off the Serve read loop (the
  proxy's detached round-trip goroutine — `acp.go:284` requirement satisfied), so no
  deadlock; `p.inflight` is touched only by the single drain goroutine (lock-free,
  no race), the cross-goroutine handoff being the proxy's `atomic.Bool` one-shot.
  Every goroutine has a ctx-bounded exit: the drain returns on ctx-cancel and is
  `wg`-joined; the detached round-trip's `cctx` descends from `m.ctx`, so teardown
  unblocks its Call immediately and it exits in microseconds. SHOULD-note (see Open
  Questions): the round-trip is ctx-bounded but not `wg`-joined — joining it would
  require changing the frozen proxy; the in-flight-teardown test demonstrates it
  terminates on cancel, satisfying AC4's "no leaked goroutine."
- **[Threat model]** No finding. ADR 027 divergence 2 (permission = blocking
  agent→client request) is implemented default-safe (ADR 025): the coerced-allow
  threat is closed by the membership check, the wedged-claude threat by the
  timeout-deny. ACP stdio is local-trusted (epic #600), out of scope for network
  threats.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-07
