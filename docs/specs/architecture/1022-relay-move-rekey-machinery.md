# Spec — #1022: move rekey machinery out of `v2session.go`

**Size:** S (confirmed — pure mechanical move; 2 production files, ~0 net new logic, 0 call-site changes).
**Security-sensitive:** No. Not labelled `security-sensitive`; a same-package carve-out with no behaviour change, no new trust boundary, and no new design to audit. Touching Noise re-key code does not by itself earn the label — the AEAD-sealed `rekey_request`, the responder-side CipherState swap, and the manual-rekey guard all move byte-for-byte; there is nothing to attack that did not already exist on `main`. (Same posture as sibling #1021's handshake move.)

## Context

`internal/relay/v2session.go` is **3323 lines** (down from 3744 after the #1021 handshake slice merged) — still the largest readability liability flagged in the 2026-07-15 full-repo review. This is the **second** slice of the #964 split: carve out exactly one concern — the **Noise re-key lifecycle** (the periodic timer-driven re-key plus the operator-triggered manual re-key) — into a new file in the *same* `package relay`.

It is a **pure move**, the mirror image of #1021 (which explicitly left `handleRekeyInit` and the rekey timers "for the later re-key slice" — this is that slice). No exported identifier changes; no logic changes; because every declaration stays in `package relay`, **no call site anywhere in the tree changes** — Go resolves package-level identifiers across files regardless of which file they live in. In particular, `(*V2SessionManager).Rekey`'s satisfaction of `control.Rekeyer` (consumed by `internal/control/server.go` and `cmd/pyry/rekey.go`) is a property of the method set of a type in a package, not of the file the method is defined in — moving the method file-to-file leaves it satisfied.

## Files to read first

- `internal/relay/v2session.go:53-77` — the three re-key duration vars (`rekeyInterval` 53–57, `rekeyReplyTimeout` 59–65, `rekeyRetryInterval` 67–77), contiguous. **Move block A.** Note lines 79+ (`modalDenyTimeout`, `idleTimeout`, the three `Err*` sentinels) begin immediately after — they STAY.
- `internal/relay/v2session.go:104-128` — `ErrConnNotFound` / `ErrSessionNotOpen` / `ErrTransportDown`. **STAY** (ticket: leave shared sentinels in core). `Rekey`/`handleManualRekey` reference them by same-package name after the move.
- `internal/relay/v2session.go:130-149` — `wakeKind` iota block (`wakeRekeyEmit`, `wakeRekeyReplyTimeout`, `wakeIdleTimeout`) + `wakeSignal`. **STAY.** The block contains the non-rekey `wakeIdleTimeout`; splitting it would fracture the iota ordering. Moved funcs reference the two rekey kinds by name.
- `internal/relay/v2session.go:151-158` — `manualRekeyReq` type. **Move block B.**
- `internal/relay/v2session.go:289-420` — `V2Session` struct + field doc comments (`rekeyTimer`, `rekeyReplyTimer`, `awaitingRekeyReply`, referenced by name in the doc at 336–363). Read-only context: struct + fields STAY (a struct cannot be split across files); the moved methods mutate these fields by name (same package). The struct doc at 293 *mentions* `handleRekeyInit` in prose — a comment reference, no compile dependency.
- `internal/relay/v2session.go:421-457` — `rekeyComplete` (method on `*V2Session`; doc starts 421, func 442). **Move.** Methods may live in any file in the package even though `V2Session` stays in core.
- `internal/relay/v2session.go:1085-1150` — the three arm-timer methods: `armRekeyTimer` (doc 1085), `armRekeyReplyTimer` (doc 1103), `armRekeyRetryTimer` (doc 1119, func ends 1150). **Move.**
- `internal/relay/v2session.go:1151` — `armIdleTimer`. **STAYS** — idle-timeout, not rekey (ticket-stated). It sits immediately after `armRekeyRetryTimer`; do not carry it along.
- `internal/relay/v2session.go:1248-1379` — `handleRekeyInit` (doc 1248, func 1276, ends before `dispatchAppFrame` at 1380). **Move.** Calls `s.rekeyComplete(m, ctx)` at 1360 (moves too).
- `internal/relay/v2session.go:1493-1559` — `handleRekeyRequest` (doc 1493, func 1510, ends before `handleRequestSnapshot` at 1560). **Move.**
- `internal/relay/v2session.go:2642-2732` — `emitRekeyRequest` (doc 2642, func 2670, ends before `sealError` at 2733). **Move.**
- `internal/relay/v2session.go:2843-2875` — `Rekey` **plus** the `var _ control.Rekeyer = (*V2SessionManager)(nil)` assertion at line 2860 (doc 2843, assertion 2860, func 2862). **Move both** — the assertion is the interface-satisfaction anchor for the moved method; keep them together. (Leaving the assertion in core also compiles; moving it keeps the concern cohesive.)
- `internal/relay/v2session.go:2877-2953` — `handleManualRekey` (doc 2877, func 2889, ends before `Push` at 2954). **Move.**
- `internal/relay/v2session.go:995-1053` — the manager Run loop's `m.manualRekey` arm (996 → `handleManualRekey`) and `handleWake`'s `wakeRekeyEmit` arm (1042 → `armRekeyRetryTimer`, 1050 → `emitRekeyRequest`). **STAY** — the wake/dispatch infrastructure; they call the moved funcs by same-package name.
- `internal/relay/v2session.go:1393` — the recv-loop call `m.handleRekeyRequest(...)`. **STAYS** (caller); same-package call.
- `internal/relay/v2session_handshake.go:99,315` — this file (created by #1021) calls `handleRekeyInit` (99) and `armRekeyTimer` (315). **STAYS UNTOUCHED.** After the move those references resolve same-package to the new file. Do not edit it — AC#3.
- `docs/specs/architecture/1021-relay-move-noise-handshake.md` — the sibling move spec; this spec mirrors its shape and conventions.

## Design

### What moves, into what

Create `internal/relay/v2session_rekey.go` (`package relay`). Relocate, **verbatim** (including doc comments), exactly these declarations:

| Decl | Kind | Doc-comment start |
|---|---|---|
| `rekeyInterval`, `rekeyReplyTimeout`, `rekeyRetryInterval` | package vars | 53 (contiguous block to 77) |
| `manualRekeyReq` | type | 151 |
| `rekeyComplete` | method (`*V2Session`) | 421 |
| `armRekeyTimer` | method | 1085 |
| `armRekeyReplyTimer` | method | 1103 |
| `armRekeyRetryTimer` | method | 1119 |
| `handleRekeyInit` | method | 1248 |
| `handleRekeyRequest` | method | 1493 |
| `emitRekeyRequest` | method | 2642 |
| `Rekey` + `var _ control.Rekeyer = (*V2SessionManager)(nil)` | method + assertion | 2843 |
| `handleManualRekey` | method | 2877 |

Each move includes the decl's **leading doc comment** through its closing brace. The exact first/last line of each cut is the developer's only judgement call; `go build ./internal/relay/` after each cut verifies it in seconds (a mis-scoped boundary — e.g. dragging `armIdleTimer` along, or leaving `rekeyComplete` behind — fails to compile or leaves an orphan immediately).

Suggested in-file order for the new file (duration vars → the `manualRekeyReq` type → the responder/handler methods → the arm-timer helpers → the manual-rekey path), keeping the reading order coherent. Order is cosmetic (Go is order-independent within a package); optimise for readability.

### What explicitly STAYS in `v2session.go`

- The `V2Session` and `V2SessionManager` structs and **all their fields** — including `rekeyTimer` / `rekeyReplyTimer` / `awaitingRekeyReply` (on `V2Session`), `manualRekey chan manualRekeyReq` (on `V2SessionManager`, ~line 868), and the Config timer-override fields (~629–652). A struct cannot be split across files; the moved methods mutate these fields by name.
- The `wakeKind` iota block + `wakeSignal` (130–149), `armIdleTimer` (1151), `idleTimeout`/`modalDenyTimeout` (79–102), and the three `Err*` sentinels (104–128).
- The Run loop, `handleWake`, the recv loop / `handleFrame`, and every other caller of a moved symbol. Same-package calls, no edit.

### Cross-file references are all in-package (why nothing else changes)

Every consumer of a moved symbol is either in `package relay` (Run loop, `handleWake`, recv loop, `v2session_handshake.go`, and the `internal/relay` test files) or reaches `Rekey` through the `control.Rekeyer` interface (`internal/control/server.go:794`, transitively `cmd/pyry/rekey.go:95`). None of these depend on which *file* the decl lives in. No file outside `v2session.go` and the new file may be edited — AC#3.

## Import hygiene (the one green-build gotcha)

**No import goes stale in `v2session.go`.** Every package the moved code uses (`context`, `time`, `bytes`, `encoding/json`, `errors`, and `internal/{control,dispatch,noise,protocol}`) also has many other users in the ~3000 lines of staying core (the struct field types, `dispatchAppFrame`, `sealError`, `emitResync`, the idle sweep, the Run loop). So the developer must **not** remove any import from `v2session.go`; `go vet` / `staticcheck` will flag the (not expected) case of a genuinely-orphaned one.

**The new file needs its own import block** — a subset of the parent's. Expected set (let `goimports -w internal/relay/v2session_rekey.go` or the compiler be the source of truth):

```
bytes
context
encoding/json
errors
time
github.com/pyrycode/pyrycode/internal/control
github.com/pyrycode/pyrycode/internal/dispatch
github.com/pyrycode/pyrycode/internal/noise
github.com/pyrycode/pyrycode/internal/protocol
```

`log/slog` and `github.com/coder/websocket` are **compiler-determined**: the moved code references package-level consts that stay in `v2session.go` (e.g. `StatusHandshakeFailure`) and may log through a manager helper rather than `slog.` constructors — add them only if the build asks. `protocol` is certain (envelope building/sealing); `noise` is certain (the responder-side fresh-IK swap in `handleRekeyInit`).

## Do not touch `v2session_handshake.go` (or any test/e2e file)

`v2session_handshake.go` (from #1021) references `handleRekeyInit` and `armRekeyTimer`; those resolve same-package to the new file after the move — **no edit, no import fix there**. Likewise the `internal/relay/*_test.go` and `internal/e2e/*_test.go` files that exercise the rekey path resolve unchanged. AC#3 forbids editing anything but `v2session.go` and the new file; resist the reflex to "follow" a moved symbol with an edit elsewhere.

## Concurrency model

Unchanged. The single-owner-goroutine invariant — `s.send` / `s.recv` / `s.state` / `s.rekeyTimer` / `awaitingRekeyReply` mutated only on the manager's Run dispatch goroutine, timers armed via `time.AfterFunc` callbacks that only push a `wakeSignal` onto `m.wake` (never touch session state directly), and the manual-rekey request funnelled through `m.manualRekey` — is a property of *when* these functions run relative to Run, not of *which file* they live in. The move preserves it verbatim.

## Error handling

Unchanged. The re-key failure paths (reply-window expiry → `StatusHandshakeFailure` 4426 + `noise.rekey_failed` log; the `Rekey` sentinels `ErrConnNotFound` / `ErrSessionNotOpen` / `ErrTransportDown`; the transport-down deferral in `handleManualRekey`) all move byte-for-byte. No failure mode is introduced or removed.

## Testing strategy

No new tests; no test file changes (AC#3). Verification is the existing suite proving behavioural identity:

- Incremental, fast: `go build ./internal/relay/` after each cut to catch a mis-scoped boundary early.
- `go vet ./internal/relay/` + `staticcheck ./internal/relay/` — catch any accidentally-unused import.
- Full gate once at the end: `make check` (vet, `go test -race`, staticcheck, substrate-guard, e2e) must be green (AC#4). The existing rekey unit tests (periodic timer, manual `Rekey`, reply-window timeout, transport-down deferral) and the reconnect/rekey e2e tests exercise the moved code unchanged.
- Confirm the diff touches exactly two files: `git diff --name-only` must list only `internal/relay/v2session.go` and `internal/relay/v2session_rekey.go` (AC#3).
- Preserve aligned `slog` field lists / doc-comment alignment on paste (gofmt 1.26 reflow is not CI-gated; keep the blocks byte-identical).

## Open questions

None. Boundaries (which decls move, which of the interleaved neighbours — `armIdleTimer`, the `wakeKind` block, the `Err*` sentinels — stay), the `var _ control.Rekeyer` placement, and the import set are all resolved above. The developer's only judgement call is the exact first/last line of each cut, which `go build ./internal/relay/` verifies in seconds.

## Note for the record — branch-overlap false positive

The §1.5 branch-overlap scan flagged `origin/feature/449` as also touching `v2session.go`. Verified **false positive**: issue #449 is CLOSED, has no open PR, and `feature/449` is ~1142 commits behind `main` (4 stale commits). Its work — the re-key responder `handleRekeyInit` — is already on current `main` as part of the code this ticket *moves* (not new work colliding at a merge). Not a live merge target; no block set.
