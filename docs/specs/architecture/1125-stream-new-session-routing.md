# Spec: Stream turn-stream — per-conversation `new_session` routing (#1125)

Rotate the **active conversation's** pool-side session id to a fresh daemon-minted
id and fresh-restart its **bound** runner — reaching the per-conversation
`*streamsup.Runner` that is actually executing that conversation, not the idle
bootstrap supervisor — with **no `/clear` keystroke**. The routing twin of the
interrupt sibling #1121; consumes the runner-level `RestartFresh(newID)` mechanism
delivered by #1124.

## Files to read first

- `cmd/pyry/main.go:1198-1249` — `resolveBoundRunner` + `activeInterrupter` + `interruptRunner`. **This spec's three new symbols mirror these one-for-one.** Extract: the `CurrentSessionID == ""` isolation guard (never resolve to bootstrap), the injected-seam struct shape, the type-switch dispatch idiom.
- `cmd/pyry/main.go:930-940` — the relay-worker literal where `activeInterrupter` is populated (`currentConv: active.CurrentConversation`, `resolveRunner` over `resolveBoundRunner`). The new `activeSessionStarter` is wired in the same literal, identically.
- `cmd/pyry/relay.go:183-186` — the worker struct field `activeInterrupter relay.Interrupter`. Add the sibling `activeSessionStarter` field here.
- `cmd/pyry/relay.go:565-576` — the `Interrupter:`/`SessionStarter:` wiring block in the `V2SessionConfig` literal. Change `SessionStarter: w.sup` → `SessionStarter: w.activeSessionStarter` (+ its comment).
- `internal/sessions/transition.go` (whole file, 107 lines) — `onRotate`, `rebindConversation`, `notifyTransition`. The new `RotateForNewSession` lives here and reuses `notifyTransition(ReasonClear)`. Extract: the rotate→rebind→observe sequence and the off-`Pool.mu` `notifyTransition` discipline.
- `internal/sessions/pool.go:586-627` — `RotateID` body (the re-key + bootstrap-flip + `saveLocked`). `RotateForNewSession` reuses this re-key logic under `Pool.mu`.
- `internal/sessions/pool.go:1386-1428` — `RegisterAllocatedUUID` / `registerAllocatedUUIDLocked` / `IsAllocated` + their doc. Extract: the skip-set invariant — **every id pyry mints and spawns claude with via `--session-id` must be registered before the spawn**, or the fsnotify watcher treats the resulting `<id>.jsonl` CREATE as a claude self-rotation.
- `internal/streamsup/runner.go:314-371` — `RestartFresh(newID)` + `nextSpawnID` (rotatePending → `--session-id <newID>`, one fresh spawn, then `--resume`). Extract: this is a **concrete** method deliberately off `sessions.Runner`; the empty-id Warn no-op; the "leaves `r.args` untouched" contract.
- `cmd/pyry/interrupt_routing_test.go` (whole file) — the exact test shape to mirror: `baseRunner` stub, per-arm dispatch stubs, `TestResolveBoundRunner` (real pool), `TestActiveInterrupter` (injected fakes, bootstrap-untouched proof). `newRouterTestPool(t)` helper is reused.
- `internal/sessions/transition_test.go` — reuse its rotation/rebind/observer test helpers for `TestRotateForNewSession`.
- `internal/conversations/registry.go:232` — `RebindSession(oldID, newID string) bool` (returns true iff a conversation owned oldID). Called inside `notifyTransition`'s rebind; confirms the active conversation's `CurrentSessionID` moves oldID→newID.
- `internal/relay/v2session_modal.go:480-521` — `handleNewSession`. **Unchanged** — the `s.interactive` capability gate + nil-seam guard stay exactly as-is. Extract: only the *adapter behind the `SessionStarter` seam* changes; the relay handler and its `v2session_newsession_test.go` are untouched.

## Context

Today `new_session` is a **bootstrap mis-route** plus an **indirect** rotation:

1. `handleNewSession` calls `SessionStarter.StartNewSession()`, wired
   `SessionStarter: w.sup` — the bootstrap `*supervisor.Supervisor`, regardless of
   which conversation the remote client is actually in.
2. `StartNewSession` types the fixed `/clear` slash command into that child.
3. The pool-side id flip happens **indirectly**, only when the fsnotify rotation
   watcher later observes claude self-rotating its `<uuid>.jsonl` and drives
   `Pool.RotateID` via `onRotate`.

This is the same shape the interrupt sibling #1121 already fixed for `interrupt`.
On the stream-json path `new_session` is a **direct** operation: mint a fresh id,
rotate the pool-side id to it, and fresh-restart the active conversation's runner —
no typed `/clear`, no waiting on a watcher. The runner-level mechanism
(`(*streamsup.Runner).RestartFresh(newID)`) landed in #1124 (closed). This ticket
wires the routing that calls it.

## Design

Three changes, mirroring #1121's factoring:

### 1. New seam: `Pool.RotateForNewSession(oldID) (newID, error)` — `internal/sessions/transition.go`

The **direct** analog of the watcher's `onRotate`, but daemon-driven. `cmd/pyry`
cannot compose this from exported primitives (`rebindConversation` and
`notifyTransition` are unexported), so it is one new exported method.

```
// RotateForNewSession rotates the session keyed by oldID to a fresh
// daemon-minted id and returns the new id for the caller to feed to
// RestartFresh. It is the direct (new_session) analog of onRotate: it re-keys
// the pool entry, registers the new id in the allocated skip-set, rebinds the
// owning conversation, and fires a ReasonClear transition so the client sees
// the fresh-session break — but it MINTS the id and drives the rotation itself
// rather than observing claude's self-rotation.
func (p *Pool) RotateForNewSession(oldID SessionID) (SessionID, error)
```

Behavior (contract the developer implements):

- Mint `newID, err := NewID()`. On the (crypto/rand) error return `("", err)` with
  no mutation and no transition.
- Under `Pool.mu` (write), atomically:
  - `ErrSessionNotFound` if `oldID` is absent — no mutation, no transition,
    return `("", ErrSessionNotFound)`. (TOCTOU guard: the binding may vanish
    between the caller's resolve and this call.)
  - Re-key `oldID → newID` (the `RotateID` body: stamp `sess.id`/`lastActiveAt`
    under `lcMu`, move the map entry, flip `p.bootstrap` if `oldID` was it).
  - `registerAllocatedUUIDLocked(newID)` — **the key asymmetry vs `onRotate`.**
    In `onRotate` claude already created `<newID>.jsonl` and the id is
    deliberately *un*-allocated (that is how the watcher detects a real
    self-rotation). Here **we** are about to spawn `claude --session-id <newID>`
    (via `RestartFresh`), so `<newID>.jsonl` will CREATE-fire the watcher; the id
    MUST be in the skip-set or the watcher double-rotates. This upholds the
    documented `RegisterAllocatedUUID` invariant that `GetOrCreate` already obeys
    for caller-supplied `--session-id` mints.
  - `saveLocked()` — persist. A save error is **logged at Warn and swallowed**
    (not returned): the in-memory rotation + rebind are already applied and
    authoritative, matching `rebindConversation`'s and `RotateID`'s documented
    best-effort-durability posture.
- Off-lock, `notifyTransition(SessionTransition{PreviousID: oldID, NewID: newID,
  Reason: ReasonClear, OccurredAt: now})` — this rebinds the conversation
  (`CurrentSessionID` oldID→newID via `RebindSession`) **and** fires the
  `session_transition` observer, exactly as `onRotate` does.
- Return `(newID, nil)`.

Reuse `ReasonClear` — no new `TransitionReason`, no wire change. From the client's
view a direct `new_session` and a `/clear` are the same event: a fresh session
started. #657's `ReasonClear → wire "clear"` mapping is untouched.

Implementation note (elegance, not contract): the re-key block is shared with
`RotateID`. Extract a private `rekeyLocked(oldID, newID)` helper (caller holds
`Pool.mu`) used by both, or inline the ~6 lines — developer's call. Do **not**
change `RotateID`'s exported signature or its `onRotate` caller.

### 2. New adapter: `activeSessionStarter` + `startFreshRunner` — `cmd/pyry/main.go`

Mirrors `activeInterrupter` + `interruptRunner`. `activeSessionStarter` satisfies
`relay.SessionStarter` and routes `StartNewSession()` to the runner bound to the
**active** conversation.

```
type activeSessionStarter struct {
    currentConv  func() string
    resolveBound func(convID string) (runner sessions.Runner, oldID sessions.SessionID, ok bool)
    rotate       func(oldID sessions.SessionID) (sessions.SessionID, error) // = pool.RotateForNewSession
}

func (a activeSessionStarter) StartNewSession() error   // resolve active → dispatch
```

`StartNewSession()` order is load-bearing (mirrors `activeInterrupter.SendEsc`):

1. `convID := a.currentConv()`; if `""` → return `nil` (**AC4** — no active
   conversation, inert, no rotation, no panic).
2. `runner, oldID, ok := a.resolveBound(convID)`; if `!ok` → return `nil` (**AC4** —
   unbound/dangling binding is inert; the `CurrentSessionID == ""` guard inside
   `resolveBound` is the #678 isolation enforcement point — an unbound conversation
   NEVER resolves to the bootstrap session that `Pool.Lookup("")` returns).
3. `return startFreshRunner(runner, oldID, a.rotate)`.

`startFreshRunner` is the type-switch dispatch (the `new_session` twin of
`interruptRunner`), matched `RestartFresh` first:

```
func startFreshRunner(r sessions.Runner, oldID sessions.SessionID,
    rotate func(sessions.SessionID) (sessions.SessionID, error)) error {
    switch v := r.(type) {
    case interface{ RestartFresh(string) }:      // *streamsup.Runner — direct, NO /clear
        newID, err := rotate(oldID)              // rotate FIRST (registers allocated) ...
        if err != nil { return err }
        v.RestartFresh(string(newID))            // ... THEN spawn --session-id newID
        return nil
    case interface{ StartNewSession() error }:   // *supervisor.Supervisor — /clear, watcher rotates
        return v.StartNewSession()
    default:
        return nil                               // unknown runner: inert
    }
}
```

- **Stream arm** (`*streamsup.Runner`): rotate the pool id + `RestartFresh` — the
  new direct path. **No `/clear`.** `rotate` runs **before** `RestartFresh` so
  `newID` is in the allocated skip-set before the fresh child spawns `<newID>.jsonl`
  (else the watcher double-rotates; §Concurrency).
- **PTY arm** (`*supervisor.Supervisor`): keep the existing `StartNewSession`
  `/clear` behavior — the watcher drives that rotation as it does today. We do
  **not** pre-mint/rotate the pool id here: `/clear` makes claude pick its *own*
  new id, so a pre-minted id would mismatch. This preserves today's behavior for a
  PTY-bound conversation with zero regression, exactly as #1121 kept both arms.
- **default**: inert (nil) — "no actuation beats wrong actuation" (#1121).

`resolveBound` must return the runner **and** `oldID`. Do **not** refactor the
shared `resolveBoundRunner` (interrupt's path stays byte-stable). Add a sibling
helper next to it:

```
func resolveBoundSession(convReg *conversations.Registry, pool *sessions.Pool,
    convID string) (*sessions.Session, sessions.SessionID, bool)
```

Same `Get → CurrentSessionID == "" guard → Pool.Lookup` body as `resolveBoundRunner`,
but returns `(*Session, boundID, ok)`. The wiring closure yields
`(sess.Runner(), boundID, ok)`. The small Get→guard→Lookup duplication with
`resolveBoundRunner` is accepted (same tolerance the reviewer already granted the
isolation-guard duplication in #1121; keeping interrupt untouched is worth more
than DRY here).

### 3. Wiring — `cmd/pyry/relay.go` + `cmd/pyry/main.go`

- `relay.go` (worker struct, near line 186): add
  `activeSessionStarter relay.SessionStarter`.
- `main.go` (worker literal, near line 935, beside `activeInterrupter`):
  ```
  activeSessionStarter: activeSessionStarter{
      currentConv:  active.CurrentConversation,
      resolveBound: func(convID string) (sessions.Runner, sessions.SessionID, bool) {
          sess, id, ok := resolveBoundSession(convReg, pool, convID)
          if !ok { return nil, "", false }
          return sess.Runner(), id, true
      },
      rotate: pool.RotateForNewSession,
  },
  ```
- `relay.go` (the `V2SessionConfig` literal, line 576): `SessionStarter: w.sup` →
  `SessionStarter: w.activeSessionStarter`, and update the comment to describe the
  active-conversation routing (mirroring the `Interrupter:` comment at 565-571).

`internal/relay` is **untouched** — the `SessionStarter` seam and `handleNewSession`
already carry the exact contract (nil-seam inert, `s.interactive` gate,
best-effort Warn-on-error). We only swap what sits behind the seam.

### Data flow

```
inbound new_session frame (no payload)
   │  handleNewSession: s.interactive gate → nil-seam guard  (UNCHANGED)
   ▼
SessionStarter.StartNewSession()  ── now w.activeSessionStarter (was w.sup)
   │
   ├─ currentConv() == ""            → nil                (AC4 inert)
   ├─ resolveBound(convID) !ok       → nil                (AC4 inert; never bootstrap)
   ▼
startFreshRunner(runner, oldID, rotate)
   ├─ *streamsup.Runner:  newID = pool.RotateForNewSession(oldID)  (re-key + skip-set + rebind + ReasonClear)
   │                      runner.RestartFresh(string(newID))       → next spawn: claude --session-id <newID>
   │                      (client sees the break via session_transition{clear}; NO /clear keystroke)
   └─ *supervisor.Supervisor: runner.StartNewSession()             → /clear; watcher drives RotateID (unchanged)
```

## Concurrency model

- `StartNewSession()` runs on the manager's **single Run dispatch goroutine** (same
  as `handleInterrupt`/`handleNewSession`), so `currentConv` / `resolveBound` reads
  need no extra synchronization beyond the primitives they call.
- `Pool.RotateForNewSession` takes `Pool.mu` (write) for the re-key +
  skip-set-register + `saveLocked`, then releases it before `notifyTransition` (the
  established off-`Pool.mu` leaf-callback discipline — `notifyTransition` →
  `rebindConversation` touches the conversations registry, never `Pool.mu`). Lock
  order unchanged: `Pool.mu → Session.lcMu`.
- **Ordering is load-bearing.** `rotate()` completes fully — including
  `registerAllocatedUUIDLocked(newID)` published under `Pool.mu` — *before*
  `startFreshRunner` calls `RestartFresh`. `RestartFresh` then cancels the child and
  the streamsup Run loop respawns with `--session-id <newID>`, creating
  `<newID>.jsonl`. The fsnotify CREATE can only fire *after* that spawn, so the
  watcher's `IsAllocated(newID)` (also under `Pool.mu`) is guaranteed to observe the
  registration and skip the CREATE. Reversing the order (RestartFresh before rotate)
  reopens the double-rotation race.
- `RestartFresh` is itself non-blocking/fire-and-forget and safe from any goroutine
  (#1124); it drives only Runner-internal state (`restartMu`, a ctx cancel,
  `restartCh`) and never a `Pool` lock, so calling it after `rotate()` releases
  `Pool.mu` has no lock-order concern.

## Error handling

- **No active conversation / unbound / dangling / unknown runner** → `nil` (inert).
  Best-effort contract; `handleNewSession` already tolerates a nil return silently.
- **`RotateForNewSession` not-found** (`oldID` vanished between resolve and rotate)
  → `ErrSessionNotFound` propagates up through `startFreshRunner` →
  `handleNewSession` Warn-logs it (`v2.new_session.keystroke_err`) and tolerates. No
  rotation, no `RestartFresh`, no partial state.
- **`NewID()` failure** (crypto/rand) → propagated the same way; no mutation.
- **`saveLocked` failure** inside `RotateForNewSession` → logged at Warn and
  swallowed; the in-memory rotation + rebind stand and `RestartFresh` proceeds
  against the authoritative in-memory `newID` (durability best-effort, consistency
  intact — matches `rebindConversation`/`RotateID`).
- **`RestartFresh` empty id** → its own Warn no-op guard (#1124). Unreachable here
  because `newID` is a freshly-minted non-empty UUID, but the guard is the
  primitive-boundary backstop.
- **AC3 (idle-eviction unaffected):** this path calls `RestartFresh` (one fresh
  `--session-id` spawn, then `--resume`) only on an explicit `new_session`. The
  pool's idle-eviction respawn path is never routed through here and continues to
  `--resume` (reuse id, no fork). The routing change adds a caller to `RestartFresh`;
  it does not touch the eviction/resume path.

## Testing strategy

Two files. The existing `internal/relay/v2session_newsession_test.go` stays valid
(the seam contract is unchanged) — do not modify it.

**`internal/sessions/transition_test.go` — `TestRotateForNewSession`** (in-package,
table/subtests, `-race`):

- *bound conversation rotates:* build a pool + conversations registry with a
  conversation bound to `oldID`; install a recording `TransitionObserver`.
  `newID, err := RotateForNewSession(oldID)` →
  - `err == nil`, `newID` non-empty and `ValidID(newID)`;
  - `Lookup(oldID)` now errors, `Lookup(newID)` returns the *same* `*Session`;
  - the conversation's `CurrentSessionID == string(newID)` (rebound);
  - `IsAllocated(newID) == true` (registered before spawn — note: `IsAllocated`
    consumes, assert once);
  - the observer fired exactly one `SessionTransition{PreviousID: oldID,
    NewID: newID, Reason: ReasonClear}`.
- *unknown oldID is inert:* `RotateForNewSession(bogusID)` →
  `("", ErrSessionNotFound)`; pool + registry byte-unchanged; observer never fired.
- (Reuse the `notifyTransition`/rebind helpers already in this test file.)

**`cmd/pyry/new_session_routing_test.go`** — mirror `interrupt_routing_test.go`:

- *`TestStartFreshRunner_Dispatch`* — stubs embedding `baseRunner`:
  - a `restartFreshStub` (has `RestartFresh(string)`, records the id) → asserts
    `rotate` is called with `oldID`, `RestartFresh` receives the rotated `newID`,
    and `StartNewSession` is **never** invoked (the `/clear` arm not taken → **the
    "no `/clear`" AC**);
  - a `startNewSessionStub` (has `StartNewSession() error`, records the call) →
    asserts the `/clear` arm is taken and `rotate` is **not** called;
  - an inert stub (neither method) → `nil`, no actuation;
  - error propagation: a `rotate` that returns an error → surfaced; a
    `RestartFresh`/`StartNewSession` that would error → surfaced.
- *`TestActiveSessionStarter`* (the **AC2** proof, injected fakes):
  - bound fake records exactly one fresh-restart with the rotated id; a separate
    bootstrap fake wired nowhere records **zero** — proving `new_session` reaches the
    bound per-conversation runner, **not** the bootstrap;
  - `rotate` fake returns a known `newID` and records the `oldID` it was handed
    (asserts the rotation targets the bound session's id, not the bootstrap's);
  - assert **no `/clear`**: the stream fake exposes no `StartNewSession`, so the
    dispatch provably never routes a `/clear`;
  - AC4 inert: `currentConv() == ""` short-circuits before `resolveBound`;
    `resolveBound` returning `!ok` (unbound/dangling) → `nil`, `rotate` never called.
- *`TestResolveBoundSession`* (real pool, mirror `TestResolveBoundRunner`): unknown
  conversation, **empty-`CurrentSessionID` never resolves to bootstrap** (the #678
  isolation case — the load-bearing one), dangling binding, and the happy path all
  return the expected `(sess, id, ok)`.

`go build ./... && go vet ./... && go test -race ./internal/sessions/... ./cmd/pyry/...`
must be green.

## Open questions

- **PTY-arm reachability.** In the current architecture a remote client's
  conversation binds to a `*streamsup.Runner`; a conversation bound to a
  `*supervisor.Supervisor` (the PTY arm) is the bootstrap/foreground case. The PTY
  arm is kept for zero-regression parity with #1121, but if a follow-up confirms no
  remote conversation ever binds to a PTY runner, the `StartNewSession` arm could be
  retired to `default` (inert). Out of scope here — keep both arms.
- **Watched-dir topology.** The allocated-skip-set registration is prescribed
  unconditionally because it upholds the `RegisterAllocatedUUID` invariant. If
  per-conversation stream sessions provably run in a different cwd than the
  bootstrap (so the watcher never observes their `<id>.jsonl`), the registration is
  merely harmless rather than load-bearing — but registering is the
  convention-correct, deterministic choice regardless, so the design does not depend
  on resolving this.

## Security review

Adversarial self-review (ticket carries `security-sensitive`). Verdict: **PASS**.

- **Trust boundary — no untrusted input reaches the id.** The `new_session` frame
  carries **no payload** (`handleNewSession` decodes nothing). The rotated-to id is
  **daemon-minted** (`sessions.NewID()`, crypto/rand UUIDv4) inside
  `RotateForNewSession`; the client cannot supply, influence, or observe-then-replay
  it. No client bytes flow into `RotateID`/`RestartFresh`/`--session-id`. The
  "caller-supplied id validation at the primitive boundary" convention is
  vacuously satisfied — there is no caller-supplied id.
- **Authorization — unchanged, upstream of this change.** The `s.interactive`
  capability gate in `handleNewSession` (line 506) is the authz decision and is
  **not touched**. Only a paired, interactive conn can reach `StartNewSession()`.
  This routing change lives entirely downstream of the gate.
- **Cross-conversation isolation (#678) — preserved and load-bearing.**
  `new_session` rotates **only the active conversation's** bound session.
  `resolveBoundSession`'s `CurrentSessionID == ""` guard fires **before**
  `Pool.Lookup`, so an unbound conversation can never resolve to the bootstrap
  session that `Pool.Lookup("")` returns — the exact hazard the guard defeats and
  that `TestResolveBoundSession`'s empty-binding case pins. `rotate(oldID)` operates
  on the resolved bound id only; it cannot re-key the bootstrap or a sibling
  conversation's session. A stale `oldID` yields `ErrSessionNotFound` (inert), never
  a wrong-session rotation.
- **No wrong-actuation.** The type-switch dispatches only on the concrete bound
  runner: the stream arm rotates + `RestartFresh`, the PTY arm `/clear`s its own
  bound child, unknown → inert. No path actuates a runner other than the active
  conversation's.
- **No secret / no new wire surface.** No key material, token, or transcript byte is
  read, logged, or emitted. The only observable is the existing
  `session_transition{clear}` marker (already a client-visible control event, #657)
  and content-free `slog` fields (`event`, `conn_id`, session ids — non-secret
  routing identifiers, consistent with the existing `rebind_conversation` /
  `v2.new_session.*` log fields). No payload bytes exist to leak.
- **Fail-safe.** Every error/ambiguous state (no active conv, unbound, dangling,
  not-found, save failure, mint failure) is inert or best-effort Warn-logged — never
  a panic, never a silent rotation of the wrong session, never a false success. A
  persistence failure preserves in-memory consistency (correct binding) at the cost
  of durability only.

No FAIL findings; no spec revision required.
