# Spec #1040 — Remove the dead `AuthenticateFirstFrame` first-frame auth gate

**Ticket:** #1040 (split from #966) · **Size:** S · **Security-sensitive:** No

## Files to read first

- `internal/relay/auth.go:1-162` — the whole file. Everything except the two
  consts (`StatusUnauthorized`, `MsgInvalidToken`, lines 16-27) and the
  `github.com/coder/websocket` import is removed. This is the core edit.
- `internal/relay/auth_test.go` (all 235 lines) — deleted in full; nothing is
  re-homed (see § Coverage below).
- `internal/relay/v2session_handshake.go:270-290`, `400-418` — the live
  consumers of `StatusUnauthorized` / `MsgInvalidToken` on the Noise_IK v2 path.
  Read to confirm the two consts must survive unchanged; also the hello_ack
  comment at `:187-189` ("auth.go's buildResponse") is one of the three stale
  citations to fix.
- `internal/protocol/envelope.go:55-77` — the `Envelope.Token` field doc.
  Comment at `:65-67` cites `AuthenticateFirstFrame` as "the only consumer";
  the field is deliberately retained, the comment must be adjusted.
- `cmd/pyry/relay_guard_test.go:91-97` — `excludedTypes` `TypeHello` entry;
  parenthetical "(AuthenticateFirstFrame)" at `:94` is the third stale citation.
- `internal/relay/v2session.go:22-24` — comment "4401 (StatusUnauthorized)
  lives in auth.go and is reused unchanged." This stays valid **iff the consts
  stay in auth.go** — a reason to keep, not relocate (see § Design).

## Context

The v1 relay dispatch path that consumed `AuthenticateFirstFrame` was removed
from production in #913 slice 1 (`5edfa1e`); the cmd-local `authGate` closure
feeding it went with that branch. `AuthenticateFirstFrame` now has **zero
production callers** — verified via `codegraph_impact` (touches only `auth.go`
definition + `auth_test.go`) and a tree-wide `.go` grep (only test + comment
references remain). The live relay authenticator is the Noise_IK v2 handshake in
`v2session.go` / `v2session_handshake.go`, which this ticket does not touch.

This is dead-code cleanup, not a security change: removing a predicate already
disconnected from every request path changes no live authentication behaviour.

## Design

Pure deletion. No new types, no new files, no consumer cascade.

### 1. `internal/relay/auth.go` — remove the dead surface, keep the two consts

Remove, in full:
- `ErrMalformedHelloFrame` (var, `:29-34`)
- `AuthOutcome` (type, `:36-55`)
- `AuthenticateFirstFrame` (func, `:57-134`)
- `buildResponse` (func, `:136-162`) — transitively dead: `codegraph_callers`
  confirms its only caller is `AuthenticateFirstFrame` (both call sites, `:100`
  + `:121`). staticcheck U1000 will force its removal even if left behind.

Keep: `StatusUnauthorized` (`:16-21`) and `MsgInvalidToken` (`:23-27`) verbatim,
including their doc comments — both are consumed live by `v2session_handshake.go`
(`StatusUnauthorized`: :278/:286/:289/:409/:414/:417; `MsgInvalidToken`:
:274/:405) and neither doc comment cites a removed identifier.

Prune the import block to the single remaining dependency. After the edit the
imports collapse to exactly:

```go
import "github.com/coder/websocket"
```

The other seven imports (`encoding/json`, `errors`, `fmt`, `log/slog`, `time`,
`internal/devices`, `internal/protocol`) were used only by the removed code; a
`goimports`/`go build` pass surfaces any that linger. The ticket body confirms
`coder/websocket` (for the `StatusUnauthorized` type) is the only survivor.

**Keep the consts in `auth.go`.** The ticket grants relocation at implementer's
discretion, but keeping them where they are (a) is the smaller diff, and (b)
leaves the `v2session.go:24` comment ("4401 (StatusUnauthorized) lives in
auth.go") accurate — relocating would turn that into a fourth stale citation.

### 2. `internal/relay/auth_test.go` — delete in full

`git rm internal/relay/auth_test.go`. Every test in it exercises only the
removed function.

### 3. Three stale `.go` comment citations — update or drop

These live outside the edited-file blast radius, so the "tidy the file you're
editing" reflex misses them. Grep confirmed these are the *only* three `.go`
comment sites citing a removed identifier outside `auth.go`/`auth_test.go`:

| File:line | Stale text | Fix |
|---|---|---|
| `cmd/pyry/relay_guard_test.go:94` | `consumed at the auth first-frame gate (AuthenticateFirstFrame), never reaches dispatchAppFrame` | Re-point to the live consumer: the hello handshake is now consumed on the Noise_IK v2 path (`v2session_handshake.go`), not `AuthenticateFirstFrame`. Preserve the "never reaches dispatchAppFrame" / AC-#4 rationale. |
| `internal/protocol/envelope.go:67` | `AuthenticateFirstFrame is the only consumer.` | The field is deliberately retained (its removal is separate follow-on scope) but its sole consumer is now gone. State that the field currently has no live consumer. **Keep** the `SECURITY: … MUST NOT log Token` sentence above it — `Token` is still plaintext credential material on the wire type. |
| `internal/relay/v2session_handshake.go:189` | `mirror v1's request/response pairing convention (auth.go's buildResponse).` | Drop the `(auth.go's buildResponse)` parenthetical (the symbol no longer exists). The "mirror v1's request/response pairing convention" clause stands on its own. |

Exact wording is the developer's call; the contract is: no `.go` comment in the
tree cites `AuthenticateFirstFrame`, `AuthOutcome`, `ErrMalformedHelloFrame`, or
`buildResponse` after this ticket.

### Out of scope (do not touch)

- `docs/**` markdown citations — owned by the documentation phase; specs are
  frozen build artifacts. The AC scopes the comment sweep to `.go` files only.
- `protocol.Envelope.Token` field itself — intentionally retained; its removal
  is separate follow-on scope.
- `v2session.go:24` — cites the **kept** `StatusUnauthorized`; no edit needed
  (and stays correct only because the const stays in `auth.go`).

## Error handling

None. No new failure modes; this removes code, adds none.

## Concurrency model

Unchanged. No goroutines added or removed.

## Coverage

`auth_test.go` is deleted whole with no re-homing. Its one test of a *kept*
symbol, `TestStatusUnauthorized_Value` (asserts the const == 4401), is not lost
in substance: the live v2 tests already pin the close code —
`v2session_test.go:358/517/4320` and `v2session_debugbundle_test.go:319` all
assert `CloseCode == uint16(StatusUnauthorized)`, exercising the constant's
value through the real reject path. No standalone value test needs re-creating.

## Testing strategy

- `make check` green is the acceptance gate (`go vet` + `staticcheck` +
  `go test -race ./...`). The two load-bearing signals:
  - **staticcheck U1000** confirms `buildResponse` (and any other now-dead
    helper) is actually removed — a leftover would fail the build, not just warn.
  - **unused-import compile error** confirms the import block was pruned
    correctly.
- No new tests are written. The behaviour under test (v1 first-frame auth) no
  longer exists; the live v2 auth behaviour is unchanged and already covered.
- Sanity grep after the edit (belt-and-suspenders for AC #4/#5):
  `grep -rn 'AuthenticateFirstFrame\|AuthOutcome\|ErrMalformedHelloFrame\|buildResponse' --include='*.go' .`
  must return zero hits.

## Open questions

None. Every claim in the ticket body was verified against live code before this
spec was written.
