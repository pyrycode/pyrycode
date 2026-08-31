# Rekey: V2 conn re-key trigger seam (1.3d-1, #459 + #462)

`VerbRekey` lets a local operator client trigger an immediate Noise re-key on a named v2 conn through the control socket. The verb is single-word (`"rekey"`) — not part of a `rekey.*` family — because the trigger has no sibling operations. Slice A (#459) ships the wire contract and the seam; slice B1 (#462) ships the manager-side implementer (`*relay.V2SessionManager` satisfies `control.Rekeyer` via its `Rekey(ctx, connID)` method — see [v2-session-manager.md](v2-session-manager.md#operator-driven-manual-re-key-462--rekey-method-satisfying-controlrekeyer)). Sibling slice B2 ships the operator-facing CLI (`pyry rekey <conn_id>`). Until a `*V2SessionManager` is installed via `SetRekeyer` in production (a separate ticket — `NewV2SessionManager` has no production caller as of 2026-05-17), every `VerbRekey` request returns `Response{Error: "rekey: no rekeyer configured"}`.

The plumbing channel is the existing control socket — the operator subcommand runs in a separate process and cannot reach the daemon's in-process `V2SessionManager` directly. The trade-off is one socket round-trip on a verb the operator invokes interactively, which is immaterial in practice.

### Why a setter instead of a constructor argument

`Server.NewServer` keeps its signature frozen across the slice; the Rekeyer is installed post-construction:

```go
type Rekeyer interface {
    Rekey(ctx context.Context, connID string) error
}

func (s *Server) SetRekeyer(r Rekeyer) {
    s.mu.Lock()
    s.rekeyer = r
    s.mu.Unlock()
}
```

Threading the Rekeyer through `NewServer` would cascade across 1 production call site (`cmd/pyry/` daemon wiring) plus 10+ `_test.go` call sites that already pass long argument lists to `NewServer`. The setter sidesteps the cascade and keeps the wire contract independent of the constructor. Canonically called once between `Listen` and `Serve` as part of daemon startup; the existing `s.mu` (already guarding `listener` / `closed` / `closedCh`) covers the field's read in `handleRekey`. The lock is released before the (potentially blocking) Rekeyer call — leaf-lock, no nested critical sections.

### Why `Rekeyer` is free-standing, not embedded into `Sessioner`

Unlike `Remover` / `Renamer` / `Lister` / `GetOrCreator` which all share the `*sessions.Pool` producer (hence the `Sessioner` aggregate), `Rekeyer`'s implementer is `*relay.V2SessionManager` — a different type. Embedding `Rekeyer` into `Sessioner` would either force `Pool` to grow a stub `Rekey` method or force a covariant adapter; both are noise. Free-standing matches the package convention: "interface-at-the-consumer; aggregate sub-interfaces only when one concrete type satisfies the aggregate."

### Wire shape

`Request.Rekey` is a new omitempty pointer; every other verb's wire bytes stay byte-identical (the same back-compat property the existing payloads' additions preserve):

```go
type Request struct {
    // ... existing fields ...
    Rekey *RekeyPayload `json:"rekey,omitempty"`
}

type RekeyPayload struct {
    ConnID string `json:"connID"`
}
```

`RekeyPayload.ConnID` has **no** `omitempty` — empty `ConnID` is invalid input and the server-side guard (`handleRekey`) rejects it before calling Rekeyer. The camelCase JSON tag matches the control-socket convention (`SessionsPayload.ID`, `AttachPayload.SessionID`, `ResizePayload.SessionID`) — deliberately distinct from `RoutingEnvelope.ConnID`'s snake-case `"conn_id"`, which is the mobile-WS wire and unrelated to the operator-facing control socket.

`rekey` uses the `OK` / `Error` envelope — no typed result. The success ack is `Response{OK: true}`; the underlying handshake runs asynchronously on the conn's state machine and the verb does not wait for it.

### Typed errors via Response.ErrorCode

`Rekeyer.Rekey` returns `ErrConnNotFound` when the named conn is not currently open on the v2 session manager. The dispatcher maps this to the wire token `ErrCodeConnNotFound = "conn_not_found"`:

```
Response.ErrorCode == "conn_not_found"  → ErrConnNotFound
```

The sentinel `ErrConnNotFound = errors.New("rekey: conn not found")` is **owned in `internal/control`** because the `Rekeyer` contract is defined here. Slice B1 (#462) chose the wrap-at-the-producer shape: `relay.ErrConnNotFound = fmt.Errorf("relay: conn not found: %w", control.ErrConnNotFound)`. The dispatcher uses `errors.Is` (not `==`), so the producer's `%w` wrap maps to `ErrCodeConnNotFound` on the wire without any further plumbing. The client's `Rekey` helper reconstructs the bare `ErrConnNotFound` from `Response.ErrorCode` so callers can `errors.Is` against it.

Slice B1 also defines a second producer-side sentinel `relay.ErrSessionNotOpen` (returned when a conn exists but is not in `V2StateOpen`, or is already awaiting a prior rekey reply); slice A defined no `ErrCodeSessionNotOpen` wire code, so the dispatcher surfaces it through `Response.Error` verbatim with no `ErrorCode`. A wire code can be added if/when the operator UX wants the distinction client-actionable.

The pattern mirrors how `internal/control` consumes `sessions.ErrSessionNotFound` — the import direction is flipped because slice B's package owns the producer. The `relay → control` import is non-cyclic (verified at #462 spec time).

### Dispatcher behaviour

`handleRekey` follows `handleSessionsRename`'s no-`context.WithTimeout` shape. The rekey-trigger contract is "deliver the request to the v2 manager loop", not "complete the rekey handshake" — a handler-level timeout would lie about cancellability. `context.Background()` is passed into `Rekeyer.Rekey` so slice B's implementer can propagate its own enqueue-cancellation logic; this slice imposes no deadline.

Guards fire in this order before the Rekeyer call:

| Precondition | Server reply |
| --- | --- |
| `s.rekeyer == nil` | `Response{Error: "rekey: no rekeyer configured"}` (no `ErrorCode`) |
| `payload == nil \|\| payload.ConnID == ""` | `Response{Error: "rekey: missing connID"}` (no `ErrorCode`) |
| `r.Rekey(...)` returns `ErrConnNotFound` (via `errors.Is`) | `Response{Error: err.Error(), ErrorCode: ErrCodeConnNotFound}` |
| `r.Rekey(...)` returns any other error | `Response{Error: err.Error()}` (no `ErrorCode`) |
| `r.Rekey(...)` returns `nil` | `Response{OK: true}` |

The guard order is load-bearing: a daemon with no Rekeyer installed surfaces its config drift over an input-validation error, since the input never reached anything that could have used it. The nil-rekeyer and missing-connID guards both surface as plain strings (no `ErrorCode`) because neither is a client-actionable distinction — they indicate either config drift on the daemon side or a malformed client.

### Client helper

`control.Rekey(ctx, socketPath, connID) error` mirrors `SessionsRm`'s single-arg shape:

```go
func Rekey(ctx context.Context, socketPath, connID string) error {
    resp, err := request(ctx, socketPath, Request{
        Verb:  VerbRekey,
        Rekey: &RekeyPayload{ConnID: connID},
    })
    if err != nil { return err }
    if resp.Error != "" {
        if resp.ErrorCode == ErrCodeConnNotFound {
            return ErrConnNotFound
        }
        return errors.New(resp.Error)
    }
    if !resp.OK {
        return errors.New("control: rekey response missing ok flag")
    }
    return nil
}
```

Same one-shot dial → encode → decode → close lifecycle as the other client helpers. Production caller as of slice B2 (#463): `cmd/pyry/rekey.go`'s `runRekey` is the topmost operator-facing consumer. The helper itself is unchanged from slice A.

### Operator-facing CLI consumer (#463)

`pyry rekey <conn_id>` is the operator surface that dials `control.Rekey`. Three-way error routing at the verb layer:

| `control.Rekey` returns | Verb action | Final stderr |
| --- | --- | --- |
| `nil` | return `nil` (exit 0) | (none) |
| `errors.Is(err, ErrConnNotFound)` | print + `os.Exit(1)` | `pyry rekey: conn_id "<value>" not found` |
| Known untyped server reject (`"rekey: no rekeyer configured"`, `"rekey: missing connID"`) | print + `os.Exit(1)` | `pyry rekey: rekey: no rekeyer configured` (verbatim surfaced) |
| Anything else (transport: dial / encode / decode) | `return fmt.Errorf("rekey: %w", err)` | `pyry: rekey: <transport err>` (outer `pyry: ` from main) |

The known-server-reject branch matches by message-prefix because slice A reserves the typed-sentinel slot for `ErrConnNotFound` only. If a future slice adds wire codes for the no-rekeyer / missing-connID guards (or slice B1's `relay.ErrSessionNotOpen`), the helper collapses to `errors.Is` calls. The split keeps the operator-readable prefix correct: server rejects get a clean `pyry rekey: ...` line (no double `pyry: rekey:` from main's outer printer), while transport errors flow through the wrapped-return path so main's `pyry: ` prefix produces `pyry: rekey: <err>` without disturbing the verb-specific format.

Until the daemon-wire-up ticket lands, every production `pyry rekey` call surfaces `pyry rekey: rekey: no rekeyer configured` because `cmd/pyry/main.go` does not yet construct a `*V2SessionManager` (`NewV2SessionManager` has no production caller as of 2026-05-17). The verb is shipped ahead of cutover, matching the `pair preflight` precedent.

See [`codebase/463.md`](../codebase/463.md) for the per-ticket implementation summary.

### Threat model

Operator-authenticated by filesystem perms (`0600` on the socket) — the same authentication boundary as `pyry stop` and `pyry sessions rm`. An attacker who can issue `VerbRekey` can also issue `VerbStop`; the rekey verb does not lower the bar. `RekeyPayload.ConnID` is a string forwarded verbatim to slice B's `Rekeyer` — slice B is responsible for validating the conn-id against its `sessions` map. Error strings never echo flynn-noise error text or AEAD bytes; the wrapped `ErrConnNotFound` message contains only the operator-supplied conn-id, same trust class as the input.

See `docs/specs/architecture/459-control-rekey-wire.md` for the full ticket-time design; this section is the canonical evergreen reference.
