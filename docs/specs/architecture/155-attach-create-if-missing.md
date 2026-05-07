# Spec — `pyry attach --create-if-missing` (auto-create session on attach)

**Ticket:** [#155](https://github.com/pyrycode/pyrycode/issues/155)
**Phase:** 1.3b (extends Phase 1.3a `--stdio` mode with a take-or-create
session primitive so SDK consumers can issue one attach instead of two
calls)
**Size:** S

## Files to read first

The developer's turn-1 reading list. Each entry pulls the exact slice
needed to implement this spec without re-discovery.

- `internal/sessions/pool.go:773-882` (`Pool.Create`) — the existing
  spawn primitive. Mints its own UUID via `NewID()`. The new
  `GetOrCreate` is a deliberate variant: caller-supplied id, with an
  in-pool short-circuit when the id is already registered. The
  build-supervisor + build-session block (810-854) is the chunk to
  share with `GetOrCreate` via a private `buildSession(id, label)`
  helper.
- `internal/sessions/pool.go:909-944` (`RegisterAllocatedUUID`,
  `IsAllocated`, `pruneAllocatedLocked`) — the rotation-watcher
  skip-set. `Pool.Create` calls `RegisterAllocatedUUID` AFTER
  `p.mu.Unlock()` (line 873). `GetOrCreate` must do the same
  *logically* but inline the body under the same critical section
  it uses for register+persist+supervise (see "Design / Atomic
  registration" below) — call out to a new
  `registerAllocatedUUIDLocked(id)` helper that assumes the lock
  is already held.
- `internal/sessions/pool.go:752-771` (`supervise`) — schedules
  `sess.Run` on the pool's errgroup. `GetOrCreate` does **not** call
  this method directly; it inlines the equivalent `g.Go(...)` under
  the registration lock (the race-closure point — see Design).
- `internal/sessions/id.go:14-32` (`SessionID`, `NewID`) — UUIDv4
  canonical shape (`%08x-%04x-%04x-%04x-%012x`, 36 chars, 4 dashes
  at positions 8/13/18/23). `GetOrCreate` validates its input
  matches this shape; the validator lives next to `NewID`.
- `internal/control/protocol.go:116-137` (`AttachPayload`) — the wire
  struct gains one omitempty bool. The omitempty contract is
  load-bearing (cf. `TestAttach_EmptySessionIDOmittedOnWire` —
  `attach_resolve_test.go:20-36`); the new field follows the same
  rule so v0.5.x clients don't see new bytes.
- `internal/control/server.go:599-687` (`handleAttach`) — the seam
  for the new branch. Today's flow is `ResolveID → Lookup → Activate
  → Resize → Attach`. The new branch sits at the front of the resolve
  step: when `payload.CreateIfMissing` is set, dispatch to
  `Sessioner.GetOrCreate(ctx, SessionID(payload.SessionID), "")`
  instead of `ResolveID`. The rest of the function is unchanged.
- `internal/control/server.go:111-133` (`Sessioner` interface) — the
  embedded-sub-interface pattern. `GetOrCreate` joins the family,
  embedded via a new `GetOrCreator` 1-method sub-interface for
  symmetry with `Remover`/`Renamer`/`Lister`.
- `internal/control/attach_client.go:25-100` (`Attach`) — extend the
  signature with `createIfMissing bool`. One additional struct-literal
  field on the `AttachPayload`. No other client-side change.
- `internal/control/attach_stdio_client.go:11-81` (`AttachStdio`) —
  same shape: extend signature, set the field on the handshake
  payload.
- `cmd/pyry/main.go:435-522` (`parseAttachArgs`, `runAttach`) — add
  one `Bool` flag (`create-if-missing`), thread the value through
  `parseAttachArgs`'s return tuple, pass it to both
  `control.Attach`/`control.AttachStdio` call sites.
- `internal/sessions/pool_create_test.go:18-81` (`helperPoolCreate`,
  `runPoolInBackground`) — reuse these for the new tests verbatim.
  The race test follows the same shape: spin up a pool, run
  goroutines that fire `GetOrCreate` against the same id, assert
  one entry on disk and `Pool.List` shows exactly one new session.
- `internal/control/attach_test.go:746-865` (`TestAttach_*`) — the
  flag-on-wire pattern. The new `CreateIfMissing` field gets a
  byte-shape pin (omitempty when false) and a fakeSessioner-driven
  end-to-end (handshake → GetOrCreate stub returns → ack OK → byte
  stream).

## Context

### Problem

Claudian's SDK already generates a UUID per chat upstream and passes
it to claude via `--session-id <uuid>`. To swap claude out for
`pyry attach --stdio <uuid>` (Phase 1.3a) and keep the same single-call
SDK shape, pyry must accept the SDK's UUID even when pyry hasn't seen
it before — the first attach for a new chat refers to a UUID that
isn't in pyry's registry yet.

Today the SDK would have to:

1. `pyry sessions new --id <uuid>` (the `--id` flag itself is a
   separate ticket — see "Out of scope" below; today the flag does
   not exist, so step 1 is *not even possible* yet).
2. `pyry attach --stdio <uuid>`.

That's two control-plane round trips, and the SDK has to track
"have I created this session yet?" — which is the kind of state
the SDK explicitly doesn't want to carry (the whole point of
making `<uuid>` the natural key).

### Solution shape

Add a flag — `--create-if-missing` — to `pyry attach`. When set,
the daemon either binds to the existing session under the supplied
UUID, or atomically creates a fresh session under that exact UUID
and binds. The CLI invocation is one call:

```
pyry attach --stdio --create-if-missing <uuid>
```

The atomicity of the "or-create" is what makes the concurrent-call
acceptance criterion achievable — the alternative (resolve, see
"not found", then call `sessions.new`) has a TOCTOU window that
two SDK chats opened simultaneously would race through.

### Decision: a new Pool primitive, not a flag on `Pool.Create`

The ticket invites either `GetOrCreate(ctx, id, label)` or
`CreateWithID(ctx, id, label)` — the architect call here is
**`GetOrCreate`**: take-or-create rather than insert-or-error.
Rationale:

- The handler's natural shape is "ensure session X exists, then
  attach". An insert-or-error primitive would force the handler to
  branch on `ErrIDInUse` and call `Lookup` itself — re-introducing
  the TOCTOU window we're trying to close (between the failed
  insert and the lookup, the session could be removed).
- The take-or-create shape collapses both branches into one Pool
  call. The handler treats "exists" and "fresh" identically from
  here on — same `sess.Activate`, same `sess.Attach`.

`Pool.Create` (UUID-minted) keeps its current shape; `GetOrCreate`
sits beside it and shares the build-session helper.

### Decision: validate that the supplied id is a canonical UUIDv4

`SessionID` is a `string` newtype with no validation today. With a
caller-supplied id flowing through the wire, "a session with id `b`"
becomes accidentally constructible. `GetOrCreate` validates that
the id matches the shape `NewID` produces (36 chars, 4 dashes, hex
elsewhere) and rejects anything else with `ErrInvalidSessionID`.

Validation lives at the Pool boundary (not the handler) so all
future callers — direct Go consumers, test harnesses, future verbs
— pick it up adapter-free.

### Decision: `--create-if-missing` requires a full UUID, skips
`ResolveID` prefix logic

Without `--create-if-missing`, today's attach uses `ResolveID`,
which accepts a unique prefix as a convenience for human users.
With `--create-if-missing` set, the handler skips `ResolveID`
entirely and passes the literal `payload.SessionID` to
`GetOrCreate`. Reasoning:

- The flag's intended caller is the SDK, which always passes a
  full UUID. Prefix-resolution is a human affordance not relevant
  to that path.
- A "prefix that doesn't match" being interpreted as a fresh UUID
  to register is a hazard, not a feature — it would create
  sessions with non-canonical ids (`b`, `0d1`, etc.) that
  subsequent `pyry sessions list` rendering / file-system layout
  was never designed for.

`GetOrCreate`'s validator catches a non-UUID input at the Pool
boundary; the handler surfaces the typed error verbatim.

## Design

### New Pool primitive

```go
// ErrInvalidSessionID is returned by Pool.GetOrCreate when the
// supplied id is not a canonical UUIDv4-shaped string. Empty id
// also returns this. Matchable via errors.Is.
var ErrInvalidSessionID = errors.New("sessions: invalid session id")

// GetOrCreate is the take-or-create entry point: returns the
// canonical SessionID of the session keyed by id, creating one if
// none is registered. The returned SessionID is exactly id on
// success.
//
// id MUST be a canonical UUIDv4 string (matches NewID's output
// shape). Empty id and malformed strings return ErrInvalidSessionID.
//
// The "exists" path is a constant-time map lookup that returns
// without activating the session. Subsequent Activate is the
// caller's responsibility (handleAttach already does this).
//
// The "create" path is byte-equivalent to Pool.Create except the
// caller's id is used in place of NewID's output, and the
// register+persist+supervise sequence is held under p.mu — see
// "Atomic registration" below.
//
// Atomicity: two concurrent GetOrCreate calls for the same id
// produce exactly one registry entry. The loser observes the
// winner's entry under p.mu and returns the canonical id with
// no error. The lifecycle goroutine for the new session is
// scheduled before the winner's GetOrCreate returns; the loser's
// later Activate is therefore safe.
//
// Concurrency: safe for concurrent use. Concurrent calls for
// different ids serialize only briefly through p.mu (the same
// shape Pool.Create uses for its register+persist step).
//
// Returns:
//   - id, nil           — session is registered (existed before, or this call created it)
//   - "", ErrInvalidSessionID — id is empty / not a canonical UUIDv4
//   - "", ErrPoolNotRunning   — no errgroup wired (Pool.Run has not started or has exited)
//   - "", <other>             — supervisor.New, saveLocked, or Activate error (creation path)
//
// On the create path, an Activate failure returns id (the entry
// is registered and lifecycle goroutine is scheduled) plus the
// underlying error — same shape as Pool.Create.
func (p *Pool) GetOrCreate(ctx context.Context, id SessionID, label string) (SessionID, error)
```

### Atomic registration — the load-bearing change

`Pool.Create` today releases `p.mu` between the registry-persist
step and the `supervise()` step (lines 867-875). That gap is
benign for `Create` because the id is freshly minted — no second
goroutine ever sees the entry before `supervise` runs.

`GetOrCreate` cannot afford that gap. A concurrent
`GetOrCreate(sameID)` could observe the entry under `Pool.mu` after
the winner's persist, return the session reference, and call
`Activate(ctx)` — but the winner's lifecycle goroutine has not
been scheduled yet, so the buffered `activateCh` send completes,
the unbuffered `activeCh` is never closed, and Activate blocks
until ctx times out. Reproducing as a real race in CI is unlikely
but the failure mode is "30s hangs at attach time on the loser",
which is a long way to walk.

The fix is to hold `p.mu` across **all five** of:

1. duplicate-id short-circuit (`if _, ok := p.sessions[id]; ok`)
2. registry-map insert
3. `saveLocked()`
4. `registerAllocatedUUIDLocked(id)` (skip-set prime — see helper note below)
5. `g.Go(func() error { return sess.Run(gctx) })` (lifecycle goroutine schedule)

`g.Go` is non-blocking — the goroutine it spawns parks on
`activateCh` / `runCtx.Done()` before doing any pool work. Holding
`p.mu` across `g.Go` is therefore safe (no lock-order violations,
no deadlock risk).

`Activate(ctx)` happens AFTER `p.mu` is released — Activate has
its own (capMu, lcMu) discipline; holding `p.mu` across it would
deadlock, same constraint Pool.Remove already encodes (#94 design
notes).

Pseudocode for the create path:

```
p.mu.Lock()
if existing, ok := p.sessions[id]; ok:
    p.mu.Unlock()
    return id, nil  // take-path

p.sessions[id] = sess
if err := p.saveLocked(); err != nil:
    delete(p.sessions, id)
    p.mu.Unlock()
    return "", err

p.registerAllocatedUUIDLocked(id)

g, gctx := p.runGroup, p.runCtx
if g == nil:
    delete(p.sessions, id)
    _ = p.saveLocked()  // best-effort rollback of the persist
    p.mu.Unlock()
    return "", ErrPoolNotRunning

g.Go(func() error { return sess.Run(gctx) })
p.mu.Unlock()

return id, p.Activate(ctx, id)
```

Note: the `registerAllocatedUUIDLocked` helper is a refactor of
the inner body of `RegisterAllocatedUUID` (pool.go:917-925) so it
can be called while p.mu is already held. The exported
`RegisterAllocatedUUID` keeps its current "takes the lock"
contract for `Pool.Create`'s caller.

### Helper extraction in `Pool` (refactor)

Extract two private helpers from `Pool.Create` so `GetOrCreate`
shares them adapter-free:

```go
// buildSession constructs the per-session supervisor + Session
// for a given (id, label). Same body as Pool.Create lines
// 809-854. Does not touch Pool state. Returned Session is in
// stateEvicted; supervise/Activate is the caller's
// responsibility.
func (p *Pool) buildSession(id SessionID, label string) (*Session, error)

// registerAllocatedUUIDLocked is the lock-held variant of
// RegisterAllocatedUUID. Caller MUST hold p.mu (write).
func (p *Pool) registerAllocatedUUIDLocked(id SessionID)
```

`Pool.Create` is updated to call `buildSession` (replacing the
inlined block at lines 809-854) and `RegisterAllocatedUUID`
delegates to `registerAllocatedUUIDLocked`. Net change to
`Pool.Create`: the body shrinks by ~30 lines and grows by 1; no
behavioural change.

### Validator

```go
// ValidID reports whether s is a canonical UUIDv4 string of the
// shape NewID produces: 36 chars, 4 dashes at positions 8/13/18/23,
// hex elsewhere, version-4 nibble (0x40) at position 14, RFC 4122
// variant (0x80-0xb0) at position 19.
//
// Lives in id.go next to NewID so the producer + validator are
// one file. Rejects empty input as a convenience — Pool.GetOrCreate
// does not need a separate empty-check branch.
func ValidID(s string) bool
```

The version-4 nibble + variant check is belt-and-suspenders. The
SDK-produced UUIDs are uuidv4 by construction, so the cost is
nil and a future contributor mistakenly trying to register a
v3/v5 id gets a clean error.

### Wire change

One field added to `AttachPayload`:

```go
type AttachPayload struct {
    Cols            int    `json:"cols,omitempty"`
    Rows            int    `json:"rows,omitempty"`
    SessionID       string `json:"sessionID,omitempty"`
    CreateIfMissing bool   `json:"createIfMissing,omitempty"`
}
```

Omitempty is load-bearing per the same v0.5.x byte-identical
contract that pins `SessionID` (`attach_resolve_test.go:20-36`).
A new test pins the byte shape (see "Testing strategy").

### Server-side branch

`handleAttach` (server.go:605-687) gains one branch at the front:

```go
sessionID := ""
createIfMissing := false
if payload != nil {
    sessionID = payload.SessionID
    createIfMissing = payload.CreateIfMissing
}

var id sessions.SessionID
var err error
if createIfMissing {
    if s.sessioner == nil {
        _ = enc.Encode(Response{Error: "attach: no sessioner configured"})
        return false
    }
    // GetOrCreate validates that sessionID is a canonical UUIDv4.
    // ResolveID's prefix logic does not apply on this path.
    activateCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()
    id, err = s.sessioner.GetOrCreate(activateCtx, sessions.SessionID(sessionID), "")
    if err != nil {
        _ = enc.Encode(Response{Error: fmt.Sprintf("attach: %v", err)})
        return false
    }
} else {
    id, err = s.sessions.ResolveID(sessionID)
    if err != nil {
        _ = enc.Encode(Response{Error: fmt.Sprintf("attach: %v", err)})
        return false
    }
}

sess, err := s.sessions.Lookup(id)
// ... unchanged from here: SetDeadline(0), Activate, Resize, Attach
```

The existing 30s `activateCtx` is built lower in the function —
on the createIfMissing path we build it earlier (so GetOrCreate's
internal Activate gets the same budget) and let the existing
`sess.Activate(activateCtx)` reuse it. The pre-existing
`defer cancel()` discipline is preserved.

### `Sessioner` interface segment

Mirror the `Remover`/`Renamer`/`Lister` pattern: a new 1-method
interface, embedded in `Sessioner`.

```go
// GetOrCreator is the per-pool view the control server depends on
// for take-or-create attaches. *sessions.Pool satisfies it
// structurally via Pool.GetOrCreate.
type GetOrCreator interface {
    GetOrCreate(ctx context.Context, id sessions.SessionID, label string) (sessions.SessionID, error)
}

type Sessioner interface {
    Create(ctx context.Context, label string) (sessions.SessionID, error)
    GetOrCreator
    Remover
    Renamer
    Lister
}
```

`*sessions.Pool` already satisfies once `Pool.GetOrCreate` lands.
`cmd/pyry`'s `NewServer(..., pool, ...)` call site needs no
change — `pool` is passed as the `Sessioner` arg today.

### Client-side: extend `Attach` and `AttachStdio` signatures

Both client functions gain one param:

```go
func Attach(ctx context.Context, socketPath string, cols, rows int, sessionID string, createIfMissing bool) error
func AttachStdio(ctx context.Context, socketPath, sessionID string, in io.Reader, out io.Writer, createIfMissing bool) error
```

The struct literal that builds `AttachPayload` adds the new
field:

```go
Attach: &AttachPayload{Cols: cols, Rows: rows, SessionID: sessionID, CreateIfMissing: createIfMissing},
```

No other client-side change. Both functions have exactly one
production caller (`cmd/pyry/main.go:runAttach`) — single edit
each, no fan-out.

### CLI flag

`parseAttachArgs` (cmd/pyry/main.go:438-450) gains one flag:

```go
func parseAttachArgs(args []string) (sessionID string, stdio bool, createIfMissing bool, err error) {
    fs := flag.NewFlagSet("pyry attach", flag.ContinueOnError)
    fs.SetOutput(io.Discard)
    stdioFlag := fs.Bool("stdio", false, "no-PTY byte forwarding for SDK consumers")
    cimFlag := fs.Bool("create-if-missing", false, "create the session if the supplied UUID is not registered")
    if err := fs.Parse(args); err != nil {
        return "", false, false, err
    }
    sel, err := attachSelectorFromArgs(fs.Args())
    if err != nil {
        return "", false, false, err
    }
    return sel, *stdioFlag, *cimFlag, nil
}
```

`runAttach` threads the value into both call sites.

The combined flag matrix:

| Invocation | Mode | Behaviour |
|---|---|---|
| `pyry attach <id>` | PTY | Today's behaviour — `ResolveID`, `Lookup`, attach. |
| `pyry attach --stdio <id>` | stdio | Phase 1.3a — same resolve, no-PTY byte forwarding. |
| `pyry attach --create-if-missing <uuid>` | PTY | Skip `ResolveID`; `GetOrCreate(uuid)`; PTY-mode attach. |
| `pyry attach --stdio --create-if-missing <uuid>` | stdio | The SDK's primary shape. |

`<id>`/`<uuid>` is required when `--create-if-missing` is set —
GetOrCreate's empty-id rejection handles it (the empty
positional flows through as an empty SessionID, GetOrCreate
returns ErrInvalidSessionID, server encodes the error). No
extra CLI-layer arity check needed.

### Files touched

| File | Lines | Why |
|---|---|---|
| `internal/sessions/get_or_create.go` (NEW) | ~60 prod | `GetOrCreate` + `ErrInvalidSessionID`. |
| `internal/sessions/id.go` | ~15 prod | `ValidID` helper next to `NewID`. |
| `internal/sessions/pool.go` | ~10 net prod | Extract `buildSession` + `registerAllocatedUUIDLocked` helpers; thread through `Pool.Create`. |
| `internal/control/protocol.go` | ~3 prod | New `CreateIfMissing` field on `AttachPayload`. |
| `internal/control/server.go` | ~25 prod | New `GetOrCreator` interface, embed in `Sessioner`, branch in `handleAttach`. |
| `internal/control/attach_client.go` | ~3 prod | Signature + struct-literal field. |
| `internal/control/attach_stdio_client.go` | ~3 prod | Signature + struct-literal field. |
| `cmd/pyry/main.go` | ~10 prod | New flag, thread through `parseAttachArgs` + both call sites. |
| `internal/sessions/pool_get_or_create_test.go` (NEW) | ~180 test | Table-driven + race test. |
| `internal/control/attach_test.go` | ~80 test | Wire-shape, dispatch, fakeSessioner end-to-end. |
| `cmd/pyry/main_test.go` (if it exists) | ~30 test | `parseAttachArgs` flag-parsing cases. |

Total: ~129 lines production. 2 new files (1 prod, 1 test). 1 new
exported error sentinel, 1 new exported method, 1 new exported
interface, 1 new exported wire field, 1 new exported helper
(`ValidID`). Edit fan-out: 1 production call site for Attach, 1
for AttachStdio. Comfortably within the S envelope.

## Concurrency model

### Inside `Pool.GetOrCreate`

- **Locks:** `Pool.mu` (write) for the whole register+persist
  +supervise critical section. Pool.capMu is taken later by
  `Pool.Activate` along the existing path. No new locks introduced.
- **Lock order:** `Pool.mu (write) → (g.Go, non-blocking)`. Then
  release Pool.mu, then call `Pool.Activate` which takes capMu →
  Pool.mu(R) → Session.lcMu. Existing order, unchanged.
- **The race that is closed:** without holding Pool.mu across
  `g.Go`, a concurrent same-id observer could see the registered
  session before its lifecycle goroutine is scheduled. Holding
  Pool.mu across `g.Go` makes the schedule observable as part of
  the same critical section that registers the entry — so any
  same-id observer sees both, atomically.

### Two concurrent `GetOrCreate(sameID)` callers

- Both reach `p.mu.Lock()` in `GetOrCreate`.
- One wins, takes the lock first, observes empty map slot,
  registers, persists, schedules `sess.Run`, releases lock,
  proceeds to `Activate`. Returns id.
- The other acquires the lock, observes the now-registered
  session via `if existing, ok := p.sessions[id]; ok`, releases
  lock, returns id (no error).
- Both callers then return the same id to their respective
  handler invocations. Both handlers proceed to `Lookup(id) →
  sess.Activate → sess.Attach`. Activate is idempotent (already
  active → LRU touch, no-op).
- Net result: exactly one registry entry, exactly one supervised
  child, exactly one rotation-skip-set entry. AC #4.

### Two concurrent `GetOrCreate(differentIDs)` callers

- Each takes p.mu briefly (sequentially). Each builds its own
  supervisor + session (the `buildSession` call happens before
  the lock — supervisor.New is non-blocking, no claude spawn yet).
- The lock contention is brief (a few hundred microseconds);
  spawn time (Activate) is per-id and not serialized.

### The `--create-if-missing` flag and `--stdio` are orthogonal

The flag's effect is server-side, in `handleAttach`'s
resolve-vs-create branch. `--stdio` selects the **client-side**
transport. Neither touches the other:

- `--create-if-missing` without `--stdio` → PTY-mode attach to a
  freshly-created session. AC #3 (orthogonal composition).
- `--create-if-missing` with `--stdio` → stdio-mode attach to a
  freshly-created session. The SDK's primary shape.
- `--stdio` without `--create-if-missing` → today's stdio attach,
  still requires the session to pre-exist (AC #2: opt-in
  semantics).
- Neither flag → today's PTY attach, still requires the session
  to pre-exist.

## Error handling

| Failure | Behaviour |
|---|---|
| `--create-if-missing` set, sessionID empty | `ErrInvalidSessionID` from GetOrCreate; server encodes `"attach: sessions: invalid session id"`. |
| `--create-if-missing` set, sessionID malformed | Same as above. |
| `--create-if-missing` set, server has no sessioner wired (foreground mode) | Server encodes `"attach: no sessioner configured"`. Symmetric with the existing `"sessions.new: no sessioner configured"` (server.go:447). |
| `GetOrCreate` create-path: supervisor.New fails | Wrapped error; no entry on disk; nothing registered. |
| `GetOrCreate` create-path: saveLocked fails | Rolled back (delete from p.sessions); error returned. |
| `GetOrCreate` create-path: pool.Run not active (g == nil) | Rolled back (delete from p.sessions, best-effort re-save); `ErrPoolNotRunning`. |
| `GetOrCreate` create-path: Activate fails | Returns id (entry registered, lifecycle goroutine scheduled) + the Activate error. Same shape as `Pool.Create` today. Handler encodes via `fmt.Sprintf("attach: %v", err)`. |
| `--create-if-missing` unset, sessionID unknown | Today's behaviour: `ErrSessionNotFound` from `ResolveID`; encoded `"attach: sessions: session not found"`. AC #2. |
| Two concurrent same-id GetOrCreate, one's create fails after persist (e.g. ctx cancellation during Activate) | Loser still returns id with no error. The lifecycle goroutine is scheduled and will eventually park on `activateCh`. Idle timer reaps if no client attaches. Same lazy-eviction shape as today. |

## Testing strategy

### `internal/sessions/pool_get_or_create_test.go` (new)

All tests reuse `helperPoolCreate` + `runPoolInBackground` from
`pool_create_test.go`. No new test helpers needed.

#### Validator (`TestValidID`)

Table-driven, no Pool needed. Cases:

| Case | Input | Want |
|---|---|---|
| empty | `""` | false |
| canonical (NewID output) | (call NewID) | true |
| short | `"abc"` | false |
| long | (37 chars) | false |
| wrong dash positions | `"abcdefgh-ijkl-mnop-qrst-uvwxyzabcdef"` rearranged | false |
| non-hex | `"zzzzzzzz-zzzz-4zzz-8zzz-zzzzzzzzzzzz"` | false |
| version-3 nibble | (a v3 UUID) | false |
| variant `0xc` | (a non-RFC-4122 variant) | false |

#### Take-path (`TestPool_GetOrCreate_Take_ReturnsExisting`)

Pool with bootstrap; call `Pool.Create(ctx, "x")` → id1; call
`Pool.GetOrCreate(ctx, id1, "y")`. Assert:
- Returned id == id1.
- No new entry in `Pool.List()` (still 2: bootstrap + id1).
- Label is "x" — the take-path does NOT touch the existing label.
- On-disk registry has 2 entries.

#### Create-path (`TestPool_GetOrCreate_Create_Persists`)

Pool with bootstrap; mint id via `NewID()`. Call
`Pool.GetOrCreate(ctx, id, "claudian-chat-1")`. Assert:
- Returned id == input id (verbatim).
- `Pool.List()` shows 2 entries: bootstrap + new.
- `pollUntil` for `pool.Lookup(id).State().ChildPID > 0` (claude
  spawned).
- On-disk registry has 2 entries; the new entry's label is
  `"claudian-chat-1"`.

#### Persist-after-disconnect (`TestPool_GetOrCreate_PersistsPostDetach`)

Same as Create-path but explicitly evicts via
`pool.Lookup(id).Evict(ctx)` after the spawn confirms; assert the
on-disk registry still carries the entry (with
`lifecycleState=evicted`). AC #1's "persists post-disconnect"
clause.

#### Invalid id (`TestPool_GetOrCreate_InvalidID`)

Cases: empty, malformed, non-UUIDv4. All should return
`ErrInvalidSessionID` (verified via `errors.Is`). Pool state
unchanged afterward (still 1 entry: bootstrap).

#### Pool not running (`TestPool_GetOrCreate_PoolNotRunning`)

Build the pool but **don't** call `runPoolInBackground` (so
runGroup is nil). Call `GetOrCreate(ctx, validID, "")`. Assert:
- Returns `ErrPoolNotRunning`.
- `Pool.List()` shows just bootstrap (rollback verified).
- Registry on disk shows just bootstrap.

#### Race (`TestPool_GetOrCreate_ConcurrentSameID`)

The AC #4 test. Pool with bootstrap, runPoolInBackground.
Mint a target UUID via `NewID()`. Spawn N=8 goroutines, each
calling `Pool.GetOrCreate(ctx, target, fmt.Sprintf("g-%d", i))`.
Wait via sync.WaitGroup. Assert:
- All goroutines returned the same id (== target).
- All goroutines returned nil errors.
- `Pool.List()` shows exactly 2 entries (bootstrap + target).
- The target entry's label is one of the `g-N` strings (the
  winner's). Which one is non-deterministic, but it's a real
  string from the input set — guards against the "label leaks
  across" failure mode.
- On-disk registry has 2 entries.
- Run with `-race`. Required by the AC.

#### Cap interaction (`TestPool_GetOrCreate_Create_HonorsCap`)

`helperPoolCreate(t, regPath, 1)` (cap=1; bootstrap consumes the
slot). Mint id. Call `GetOrCreate(ctx, id, "")`. Assert:
- Bootstrap is evicted (`pool.Default().LifecycleState() ==
  stateEvicted`) — Pool.Activate's cap-eviction kicks in on the
  create path.
- New session is active and spawned.

This proves the create path goes through `Pool.Activate`
(reusing the cap-aware path) rather than spawning unconstrained.

### `internal/control/attach_test.go` (extend)

#### `TestAttach_CreateIfMissingOnWire`

Pin the byte shape: `Marshal(AttachPayload{CreateIfMissing:
true})` contains `"createIfMissing":true`;
`Marshal(AttachPayload{})` does not contain `"createIfMissing"`
(omitempty). Companion to the existing
`TestAttach_EmptySessionIDOmittedOnWire`.

#### `TestServer_AttachCreateIfMissing_HitsGetOrCreate`

Custom `fakeSessioner` whose `GetOrCreate` records the call and
returns a fixed SessionID. Build a server with the fake.
Client (using the existing test-conn pattern) sends
`Request{Verb: VerbAttach, Attach: &AttachPayload{
SessionID: "<uuid>", CreateIfMissing: true}}`. Assert:
- `fakeSessioner.GetOrCreateCalls` shows one call with that
  exact id.
- `fakeSessions.ResolveIDCalls` shows zero calls (the
  createIfMissing branch skips ResolveID).
- Ack is OK; byte stream begins.

#### `TestServer_AttachCreateIfMissing_NoSessioner`

Server built with `sessioner=nil`. Client sends
CreateIfMissing=true. Assert ack carries
`Error: "attach: no sessioner configured"`. No GetOrCreate is
called.

#### `TestServer_AttachCreateIfMissing_InvalidID`

Server with a fake sessioner whose GetOrCreate returns
`sessions.ErrInvalidSessionID`. Client sends CreateIfMissing=true,
SessionID="". Assert error wire string starts with
`"attach: sessions: invalid session id"` (or just match via
`strings.Contains`).

#### `TestServer_AttachCreateIfMissing_ExistingID`

Fake `GetOrCreate` returns a known id and the fake resolver's
`Lookup` returns a fake Session for that id. Verify the rest of
the attach flow runs (Activate called, ack OK, byte stream
forwards). Proves the "exists" branch is byte-identical to the
take path from the handler's perspective.

### `cmd/pyry/main_test.go`

If `parseAttachArgs` already has tests, extend with:

| args | want sessionID | want stdio | want createIfMissing | want err |
|---|---|---|---|---|
| `["abc"]` | `"abc"` | false | false | nil |
| `["--stdio", "abc"]` | `"abc"` | true | false | nil |
| `["--create-if-missing", "abc"]` | `"abc"` | false | true | nil |
| `["--stdio", "--create-if-missing", "abc"]` | `"abc"` | true | true | nil |
| `["--create-if-missing"]` (no positional) | `""` | false | true | nil |

The empty-positional case in row 5 doesn't error at parse time —
it errors server-side (GetOrCreate's empty-id rejection). That's
the correct boundary: parsing is purely about shape; semantic
rejection lives at the Pool.

### `go test -race ./internal/sessions/... ./internal/control/...`

Required by AC #5. The race test in `pool_get_or_create_test.go`
is the load-bearing one; the rest are unit tests that pass under
`-race` trivially.

### E2E

The combined `pyry attach --stdio --create-if-missing` E2E
(driving the real binary, real daemon, real claude — or fake
claude) is intentionally deferred to a sibling Phase 1.3 ticket
that wires up the broader attach/stdio harness (the same way
#161/#162 sit as siblings to #154's unit tests). The unit tests
above land the AC.

## Open questions

None blocking. Notes for the developer:

- **`Pool.Create` refactor scope.** The spec asks for two helper
  extractions (`buildSession`, `registerAllocatedUUIDLocked`).
  `Pool.Create`'s body shrinks; behaviour is unchanged. If the
  developer finds it cleaner to leave `Pool.Create`'s body
  untouched and duplicate the build-session block, the duplication
  is ~30 lines and acceptable — but the spec preference is to
  share, since both code paths construct the same shape.
- **Why ctx flows through GetOrCreate.** `GetOrCreate` calls
  `Pool.Activate(ctx, id)` on the create path. That `ctx` is
  the handler's `activateCtx` (30s timeout). If the developer
  finds reading easier with separate ctxs, it works either way —
  but reusing `activateCtx` keeps the handler's existing 30s
  budget invariant intact end-to-end.
- **Reusing `RegisterAllocatedUUID` vs. inlining the locked
  variant.** The spec calls for a `registerAllocatedUUIDLocked`
  refactor. An equally-valid alternative: just inline the
  three-line body inside GetOrCreate's critical section. The
  refactor is preferred because it keeps the skip-set policy
  (TTL prune, time.Now+TTL) in one place — but the developer
  may inline if the helper feels like ceremony for something
  this small.
- **Label on the take path.** When `GetOrCreate` short-circuits to
  an existing entry, the spec says "label is not touched" — the
  caller's `label` argument is silently dropped. Today's only
  caller passes `""`, so it doesn't matter. If a future caller
  wants take-or-create-with-label-update, that's a separate
  primitive (`Rename`-after-`GetOrCreate`); don't smuggle it in
  here.
- **Foreground mode.** `Pool` is wired only in service mode, so
  `s.sessioner` is non-nil whenever attach is plausible.
  Foreground-mode pyry currently fails attach with a different
  wire string (`"attach: no attach provider configured (daemon
  may be in foreground mode)"`) at the Bridge layer; that path
  is unchanged. The `--create-if-missing` + foreground combo
  surfaces "no sessioner configured" at the GetOrCreate-dispatch
  level, BEFORE the bridge layer is reached. Documenting in
  passing — no production caller hits this combo.
