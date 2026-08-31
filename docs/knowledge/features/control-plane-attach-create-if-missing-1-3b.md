# Attach: --create-if-missing (1.3b)

`pyry attach --create-if-missing <uuid>` lets SDK consumers (Claudian / `@anthropic-ai/claude-agent-sdk`) issue one attach call instead of two — without the flag they would need `pyry sessions new --id <uuid>` followed by `pyry attach <uuid>`, and the SDK would have to track "have I created this session yet?" The flag is opt-in and orthogonal to `--stdio`; the SDK's primary invocation is `pyry attach --stdio --create-if-missing <uuid>`. See [ADR 014](../decisions/014-get-or-create-take-or-create.md) for why this is `GetOrCreate` (take-or-create) rather than `CreateWithID` (insert-or-error).

### Wire field

One field added to `AttachPayload`:

```go
type AttachPayload struct {
    Cols            int    `json:"cols,omitempty"`
    Rows            int    `json:"rows,omitempty"`
    SessionID       string `json:"sessionID,omitempty"`
    CreateIfMissing bool   `json:"createIfMissing,omitempty"`
}
```

`omitempty` is load-bearing for the same v0.5.x byte-identical contract that pins `SessionID`'s tag — pre-1.3b clients send a payload byte-indistinguishable from before. Pinned by `TestAttach_CreateIfMissingOnWire`.

### Server-side branch

`handleAttach` gains one branch at the front of the resolve step:

```go
if createIfMissing {
    if s.sessioner == nil {
        // Foreground-mode pyry has no Pool wired.
        _ = enc.Encode(Response{Error: "attach: no sessioner configured"})
        return false
    }
    // GetOrCreate validates that sessionID is a canonical UUIDv4.
    // ResolveID's prefix logic does NOT apply on this path.
    id, err = s.sessioner.GetOrCreate(activateCtx, sessions.SessionID(sessionID), "")
    // ... err handling encodes "attach: <err>" verbatim
} else {
    // Today's two-step ResolveID-then-Lookup path.
    id, err = s.sessions.ResolveID(sessionID)
    // ...
}
```

The 30s `activateCtx` is built earlier than today's PTY-mode path so `GetOrCreate`'s internal `Pool.Activate` gets the same budget; the existing `sess.Activate(activateCtx)` below the branch reuses the same ctx.

**Why skip `ResolveID`'s prefix logic.** With `--create-if-missing` set, the handler passes the literal `payload.SessionID` to `GetOrCreate` and never calls `ResolveID`. A "prefix that doesn't match" being interpreted as a fresh UUID to register would create sessions with non-canonical ids that the registry, the file-system layout, and `pyry sessions list` were never designed for. `GetOrCreate`'s `ValidID` gate catches a non-UUID input at the Pool boundary; the handler surfaces the typed error verbatim.

### `GetOrCreator` interface

```go
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

Mirrors the `Remover` / `Renamer` / `Lister` pattern — a 1-method interface embedded into the `Sessioner` facade. `*sessions.Pool` satisfies it structurally via `Pool.GetOrCreate`; tests fake the interface directly.

### Client signatures

Both attach client functions gain one bool:

```go
func Attach(ctx context.Context, socketPath string, cols, rows int, sessionID string, createIfMissing bool) error
func AttachStdio(ctx context.Context, socketPath, sessionID string, in io.Reader, out io.Writer, createIfMissing bool) error
```

The struct literal that builds `AttachPayload` adds the new field. No other client-side change. Each function has exactly one production caller (`cmd/pyry/main.go:runAttach`) — single edit each, no fan-out.

### CLI flag

`parseAttachArgs(args) (sessionID, stdio, createIfMissing, err)`. Fresh `flag.NewFlagSet("pyry attach", flag.ContinueOnError)` registers `--stdio` and `--create-if-missing`; positionals route through `attachSelectorFromArgs` as before. The combined flag matrix:

| Invocation | Mode | Behaviour |
|---|---|---|
| `pyry attach <id>` | PTY | Today — `ResolveID`, `Lookup`, attach. |
| `pyry attach --stdio <id>` | stdio | Phase 1.3a — same resolve, no-PTY byte forwarding. |
| `pyry attach --create-if-missing <uuid>` | PTY | Skip `ResolveID`; `GetOrCreate(uuid)`; PTY-mode attach. |
| `pyry attach --stdio --create-if-missing <uuid>` | stdio | The SDK's primary shape. |

`<uuid>` is required when `--create-if-missing` is set — `GetOrCreate`'s empty-id rejection handles it (the empty positional flows through as an empty `SessionID`, `GetOrCreate` returns `ErrInvalidSessionID`, server encodes the error). No extra CLI-layer arity check needed; parsing is purely about shape, semantic rejection lives at the Pool.

### Error handling

| Failure | Wire string |
|---|---|
| `--create-if-missing` set, sessionID empty / malformed | `"attach: sessions: invalid session id"` |
| `--create-if-missing` set, server has no sessioner wired (foreground mode) | `"attach: no sessioner configured"` (symmetric with the existing `"sessions.new: no sessioner configured"`) |
| `--create-if-missing` unset, sessionID unknown | Today's `"attach: sessions: session not found"` (no behaviour change for existing callers) |
| `GetOrCreate` create-path: `Pool.Activate` fails | `"attach: <activate err>"`. Entry already persisted; lifecycle goroutine scheduled. |

Concurrent same-UUID `--create-if-missing` calls produce exactly one registry entry — see the `GetOrCreate` atomicity discussion in [sessions-package.md § Pool.GetOrCreate](sessions-package.md#poolgetorcreate-13b).

### Tests

| Test | Asserts |
|---|---|
| `TestAttach_CreateIfMissingOnWire` (`internal/control/attach_create_if_missing_test.go`) | `CreateIfMissing: true` marshals to `"createIfMissing":true`; `false` is omitempty-dropped |
| `TestServer_AttachCreateIfMissing_DispatchesToGetOrCreate` | Fake sessioner records the call with the exact id; ResolveID is NOT called; ack OK; byte stream begins |
| `TestServer_AttachCreateIfMissing_NoSessioner` | Server with `sessioner=nil` encodes `"attach: no sessioner configured"`; GetOrCreate not called |
| `TestServer_AttachCreateIfMissing_InvalidID` | `ErrInvalidSessionID` from fake GetOrCreate flows through verbatim to the wire |
| `TestParseAttachArgs` (`cmd/pyry/args_test.go`) | Flag combinations: bare positional, `--stdio`, `--create-if-missing`, both flags, `--create-if-missing` with no positional (parses cleanly; semantic rejection is server-side) |

E2E coverage of `pyry attach --stdio --create-if-missing` against a real daemon is intentionally deferred to a sibling Phase 1.3 ticket that wires up the broader attach/stdio harness — same posture as #161 / #162 sit alongside #154's unit tests. See also [`docs/specs/architecture/155-attach-create-if-missing.md`](../../specs/architecture/155-attach-create-if-missing.md) for the full architect's spec.
