# Spec #913 — Remove the v1 relay dispatch branch + `PYRY_MOBILE_V2` switch from `startRelay`

**Ticket:** #913 · **Size:** S · **security-sensitive**

This is a **deletion** ticket: it removes the legacy Mobile Protocol **v1** relay
dispatch leg and its `PYRY_MOBILE_V2` escape hatch from `startRelay`, so the daemon
speaks only Mobile Protocol v2 (Noise_IK E2E). No new behavior. The work is
mechanical removal of one coherent dead branch plus two exclusive cmd-local files,
gated by three small relocations that keep the surviving v2 path and the `cmd/pyry`
test package compiling.

Written against **post-#917** code (`relayWiring` params struct; merged as #951,
commit `84230ec`). All native blockers (#941/#942 v1-e2e retirement, #917) are
CLOSED — no open dependencies.

---

## Files to read first

- `cmd/pyry/relay.go:26-49` — `authGate`. Delete (AC-3). Sole caller is the v1
  branch at `relay.go:322`; once the branch goes it is an orphaned unexported
  symbol → `staticcheck` U1000.
- `cmd/pyry/relay.go:169-234` — `relayWiring` struct. Drop the `v2Enabled` field
  (181-183) **and** its mention in the struct doc comment (line 167). The v2 leg
  becomes unconditional, so no field replaces it.
- `cmd/pyry/relay.go:259-411` — `startRelay`. Collapse `if w.v2Enabled { …v2… }
  else { …v1… }` (304-382) into the unconditional v2 leg. **Preserve verbatim**
  the shared `conn.Wait()` classifier below the branch (384-404: 4409-conflict →
  `shutdown()`, ctx-cancel → debug, other terminal error → warn) and the final
  `cleanup` (406-410).
- `cmd/pyry/relay.go:437-598` — `startRelayV2`, the surviving leg. **Unchanged.**
  Note its `Handlers:` map (503-515) registers the *identical* 11-verb handler set
  the v1 branch registered (324-334) — no app-verb coverage is lost.
- `cmd/pyry/main.go:761-772` — the `PYRY_MOBILE_V2` env read (`v2Enabled :=
  os.Getenv("PYRY_MOBILE_V2") != "0"`, line 772) and its 10-line comment
  (763-771). Delete both.
- `cmd/pyry/main.go:872-889` — the `relayWiring{…}` literal. Drop `v2Enabled:
  v2Enabled,` (877). All other fields stay.
- `cmd/pyry/main.go:1173-1180` — `activeConversation.CurrentConversation()`, a
  surviving `cursorReader` implementer. Its doc comment (1174) points at the
  stale `assistant_turn.go:24` — repoint to `cursorReader`'s new home.
- `cmd/pyry/assistant_turn.go` — the coarse v1 assistant-turn bridge. Whole file
  deletes **after** relocating the `cursorReader` interface (24-26) out. Everything
  else (`assistantTurnQueueSize`, `connBroadcaster`, `assistantTurnEmitter`,
  `newAssistantTurnEmitter`, `startAssistantTurnBridge`, methods) is v1-only.
- `cmd/pyry/interactive_turn_v2.go:67-117` — `interactiveTurnEmitterV2` (field
  `sup cursorReader`, line 68) + `newInteractiveTurnEmitterV2`. The natural new home
  for the `cursorReader` interface (its surviving consumer).
- `cmd/pyry/assistant_turn_test.go` — whole file deletes **after** relocating the
  shared test helpers (see §Design step 4 — **note the third helper the ticket body
  omits**).
- `cmd/pyry/interactive_turn_v2_test.go:1-16` — recommended relocation target for
  the shared test helpers (already consumes all three). Needs `io` +
  `sync/atomic` imports added.
- `cmd/pyry/active_conversation_test.go:178` — a `cursorReader` doc-comment
  reference to keep coherent (optional cosmetic).
- `docs/knowledge/decisions/024-noise-ik-mobile-e2e.md` — ADR 024, the hard v2
  cutover (no mixed-mode wire). `025-mobile-remote-head-interactive-session.md` —
  ADR 025, whose 2026-06-22 amendment removed old-app-version support. The
  ratification basis for retiring v1.

---

## Context

Ratified 2026-07-12: no shipping client speaks v1. ADR 024 made v2 a hard cutover;
ADR 025's amendment removed capability negotiation (single operator, daemon + app
ship together); #699 removed the v2 coarse path. `PYRY_MOBILE_V2=0` today flips the
daemon onto a wire no client can speak — the "escape hatch" restores nothing, and
`relay.go` already logs the v1 branch as DEPRECATED.

This is the **startRelay-level** production removal — the first code slice of the v1
retirement. It fuses what the ratified body called slices 1 and 2 because they
cannot land separately: the v1 `else` branch is the sole production caller of both
`authGate` and `startAssistantTurnBridge`, both unexported and cmd-local, so
removing the branch orphans them in the same PR and `staticcheck` U1000 forbids
leaving them behind. Later, separately-filed slices finish the retirement (see
§Follow-on).

---

## Design

Five edits + two file deletions. No new types, no new files. Every cross-symbol
reference is within `package main`, so relocations are resolved by name — **zero
call-site edits** (this is why the >3-file touch is deletion + relocation, not a
fan-out cascade).

### 1. Drop the `PYRY_MOBILE_V2` env read + `v2Enabled` plumbing (AC-1)

Three coupled deletions:
- `main.go` — remove the env read (line 772) and its comment block (763-771).
- `main.go` — remove `v2Enabled: v2Enabled,` from the `relayWiring{…}` literal (877).
- `relay.go` — remove the `v2Enabled` struct field + its two-line doc (181-183) and
  strike `v2Enabled` from the struct-level doc comment's field list (line 167).

`v2Enabled` has exactly one reader (`if w.v2Enabled`, `relay.go:304`) and one
writer (the call-site assignment) — both removed here, so the field has no surviving
reference.

### 2. Collapse the branch to the unconditional v2 leg (AC-2)

In `startRelay`, replace the `if w.v2Enabled { …v2… } else { …v1… }` block with the
v2 body run unconditionally. Contract of the result:

- `var legCleanup func()` then unconditionally: log v2 selection, call
  `startRelayV2(ctx, logger, w, conn, registry, serverID)`, on error
  `_ = conn.Close(); return nil, err`, else set `legCleanup` to the
  `conn.Close(); drain()` closure — i.e. exactly the current `if`-true body (305-316),
  de-indented.
- The entire `else` branch (317-382 — `dispatch.New`, the 11 `d.Register` calls, the
  `startAssistantTurnBridge` wiring, the dispatcher/forwarder goroutines, and their
  `legCleanup`) is **deleted**.
- The `conn.Wait()` classifier (384-404) and the returned `cleanup` (406-410) are
  **preserved unchanged** — the v2 leg inherits the 4409/ctx-cancel/terminal-error
  contract exactly as today.

Update the surviving selection log (currently `relay.go:305`, "…set PYRY_MOBILE_V2=0
to force legacy v1") to drop the now-false v1 escape-hatch phrasing.

**No import churn in `relay.go`:** every package the v1 branch used
(`dispatch`, `protocol`, `handlers`) is still used by `startRelayV2` — `dispatch`
via `map[string]dispatch.Handler`, `protocol`/`handlers` via the v2 Handlers map. Let
the compiler confirm; do not pre-prune imports.

### 3. Delete `authGate` (AC-3)

Remove `authGate` + its doc comment (`relay.go:26-49`) entirely. Its only reference
was `relay.go:322`, deleted in step 2.

### 4. Delete the coarse assistant-turn bridge — relocate first (AC-4, AC-5)

**Relocate `cursorReader` before deleting the file (AC-5).** Move the interface
(`assistant_turn.go:24-26`) to `interactive_turn_v2.go` (its surviving consumer's
file). It carries no imports (one string-returning method). Its surviving
implementers — `newInteractiveTurnEmitterV2` (`interactive_turn_v2.go`) and
`activeConversation.CurrentConversation()` (`main.go:1176`) — reference it by name,
so no call-site changes; repoint the stale `assistant_turn.go:24` doc-refs at
`main.go:1174` and `active_conversation_test.go:178`.

Then `git rm cmd/pyry/assistant_turn.go` — `assistantTurnQueueSize`,
`connBroadcaster`, `assistantTurnEmitter`, `newAssistantTurnEmitter`,
`startAssistantTurnBridge` and methods are all v1-only.

**Relocate the shared test helpers before deleting the test file (AC-4 compile
trap).** `assistant_turn_test.go` is the sole definer of helpers the surviving
`cmd/pyry` test package still needs. A wholesale `git rm` breaks the whole package.
Relocate these **three** to a surviving test file (recommended:
`interactive_turn_v2_test.go`, which already uses all three):

| Symbol | Def site | Surviving consumers | Action |
|---|---|---|---|
| `discardLogger()` | `assistant_turn_test.go:38` | 8 other test files, 66 sites | **relocate** |
| `stubCursor` + `.set()` | `assistant_turn_test.go:22-32` | `interactive_turn_stream_v2_test.go`, `interactive_turn_v2_test.go` | **relocate** |
| `testConvID` const | `assistant_turn_test.go:18` | `active_conversation_test.go`, `interactive_turn_stream_v2_test.go`, `interactive_turn_v2_test.go` | **relocate** |

> ⚠️ **`testConvID` is a third shared helper the ticket body does not name.** The
> body's AC-4 says "the two shared test helpers"; there are **three**. `testConvID`
> (the const at line 18) is referenced by 3 surviving test files; leaving it behind
> with the deleted file breaks their compile. `testChunk` (line 19, same const
> block) is *not* shared — used only in `assistant_turn_test.go` — so split the const
> block: relocate `testConvID`, delete `testChunk`.

Relocating `discardLogger` needs `io` + `log/slog`; `stubCursor` needs
`sync/atomic`. Add whichever the target file lacks (`interactive_turn_v2_test.go`
already imports `log/slog`; add `io` + `sync/atomic`). Let the build/`goimports`
confirm the exact set.

Then `git rm cmd/pyry/assistant_turn_test.go` — only `testChunk`, `stubBroadcaster`,
`drainOutbound`, and the `TestAssistantTurnEmitter_*` functions delete with it (all
coarse-bridge-only).

### 5. Do not touch the deferred surfaces (AC-6)

`internal/dispatch` (`Route`/`Conn`/`Handler`/`NewConn`, and the v1
`Dispatcher`/`FirstFrameGate`) and `relay.AuthenticateFirstFrame` are **out of
scope** — they are exported, carry no U1000 forcing function, and belong to later
slices. `cmd/pyry` stops *using* `dispatch.New`/`dispatch.Dispatcher`/
`dispatch.FirstFrameGate`, but the package is not modified.

---

## Concurrency model

Nothing new. The deletion **removes** two goroutines (the v1 dispatcher `Run` and
the outbound forwarder) and the assistant-turn emitter's `Run` goroutine, along with
their `dispatcherDone`/`forwarderDone`/`bridgeCleanup` shutdown choreography. The
surviving v2 leg's goroutine model (`mgr.Run`, the interactive turn/modal streams,
the session-transition and queue-state producers) and the shared `waitDone`
classifier goroutine are untouched. Net concurrency footprint strictly shrinks.

---

## Error handling

Unchanged. The surviving `conn.Wait()` classifier keeps the exact 4409 →
`shutdown()` (no reconnect loop), ctx-cancel → debug, terminal-error → warn contract.
`startRelayV2`'s fail-fast prologue (static-key / manager-build errors return wrapped,
`conn.Close()` on the v2-build error path) is preserved as-is. No failure mode is
added or removed; one dead error path (the v1 `authGate` malformed-frame
fall-through) is deleted with the branch.

---

## Testing strategy

- **Green gate (AC-7):** `go build ./...`, `go vet ./...`, `staticcheck ./...`, and
  `go test -race ./...` all pass. The two acceptance risks are (a) an orphaned
  unexported symbol tripping U1000 — mitigated by co-removing `authGate` and the
  `assistant_turn.go` symbols in the same change, and (b) the `cmd/pyry` test package
  failing to compile — mitigated by relocating all **three** shared test helpers
  before deleting `assistant_turn_test.go`.
- **No new tests.** This removes coverage of a dead transport; the v2 twins already
  cover the identical handler set. The surviving v2 unit tests
  (`interactive_turn_v2_test.go`, `interactive_turn_stream_v2_test.go`,
  `queue_state_v2_test.go`, `session_transition_v2_test.go`, the ACP tests) must stay
  green — they are the regression signal that the relocations landed correctly.
- **v2 e2e unchanged.** The `internal/e2e` tests set `PYRY_MOBILE_V2=1`; after this
  change the var is inert (the daemon is always v2). Leaving the `=1` is harmless;
  scrubbing it is optional cleanup, not required for green, and **out of scope** here
  (it would widen the file touch and double e2e CI for no behavioral gain).
- **Verification:** after the edits, confirm `grep -rn 'PYRY_MOBILE_V2\|v2Enabled\|
  authGate\|assistantTurnEmitter\b\|startAssistantTurnBridge' cmd/` returns only
  inert e2e `=1` occurrences and no `cmd/pyry` production/test references.

---

## Open questions

- **Selection log line.** Step 2 keeps a single "v2 enabled" info log. Trim it to a
  plain "relay: Mobile Protocol v2 (Noise_IK)" statement of fact (no v1 mention) —
  left to the developer's judgment; not gated by an AC.
- **`testChunk` re-home.** If the developer prefers to keep the whole const block
  intact by moving both `testConvID` and `testChunk` to the target file (rather than
  splitting it), that is acceptable — `testChunk` then becomes an unused const in the
  target only if no test references it; verify it is deleted, not silently relocated
  into disuse (an unused package-level const does **not** trip U1000, but leaving dead
  test constants is untidy). Cleanest: relocate `testConvID`, delete `testChunk`.

---

## Follow-on tickets (file after this lands)

Both defer cleanly — their symbols are exported, so no U1000 forces them here:

3. Remove the v1 `Dispatcher` + `FirstFrameGate` from `internal/dispatch`, keeping
   `Route`/`Conn`/`Handler`/`NewConn`.
4. Remove `relay/auth.go`'s `AuthenticateFirstFrame`, the envelope `Token`
   binary-side consumer, the `internal/protocol` `payload_encrypted`/
   `IsV1Compatible` v1-compat surface, the fakephone/fakerelay token-injection
   surfaces, and the doc updates.

---

## Security review

Adversarial pass on this spec, per the `security-sensitive` label. The lens: this
change **deletes** a dispatch path and a first-frame auth gate on the
internet-exposed relay surface — the risk is not "what does new code let in" but
"does removing this gate open an unauthenticated path, or drop a control that the
surviving path relied on."

**Trust boundary — the relay WebSocket is the internet-exposed, attacker-reachable
surface.** Frames arriving on `conn.Frames()` are untrusted. Two mutually exclusive
consumers exist today (ADR 024 hard cutover, no mixed mode): the v1 dispatcher
(gated by `authGate` → `relay.AuthenticateFirstFrame`, `relay.go:31-49`) and the v2
Noise_IK manager (`startRelayV2`). This change removes the v1 consumer and its gate.

- **Does removing `authGate` open an unauthenticated dispatch path?** No. `authGate`
  gated the **v1** leg only. After this change the sole consumer of `conn.Frames()`
  is `startRelayV2`'s `NewV2SessionManager`, which authenticates the Noise_IK
  handshake **before any verb is dispatched** (`relay.go:437-598`, `StaticPriv` +
  `Devices` registry + per-session handshake). The authenticate-before-dispatch
  property is preserved by the surviving path; the gate being deleted protected a
  path that no longer exists. `relay.AuthenticateFirstFrame` itself is **not** deleted
  here (AC-6, deferred to slice 4) — only `authGate`, the cmd-local closure that fed
  it into the v1 dispatcher.

- **Is there any residual reader of untrusted frames left ungated?** No. After step 2
  exactly one leg consumes the frame stream. `grep` verification in §Testing confirms
  no surviving `cmd/pyry` reference to `authGate`, the v1 `dispatch.New`, or the
  coarse bridge. `internal/dispatch`'s v1 `Dispatcher`/`FirstFrameGate` code remains
  in-tree but is **unreachable** — it has zero callers after this change (its only
  production caller was the deleted branch), pending physical removal in slice 3.

- **Does deleting the coarse assistant-turn bridge drop a security control?** No. The
  bridge (`assistant_turn.go`) was an **outbound** PTY-output fan-out to paired
  phones over the v1 dispatcher; it enforced no inbound authz. Its documented
  security posture — PTY chunk bytes NEVER logged (only `chunk_len`/ids) — is a
  log-hygiene property of code being deleted, not a control the surviving path
  depends on. The v2 structured turn stream (`interactive_turn_v2.go`, retained)
  carries the same "application output never logged" discipline independently
  (see its `SECURITY:` doc, lines 61-66), so no output-confidentiality regression.

- **Data-flow of the relocations.** `cursorReader` and the three test helpers are
  moved, not changed — pure lexical relocation within `package main`, no behavior,
  no new trust boundary, no secret handling. The v2 leg's secret handling
  (`StaticPriv`, the X25519 static secret passed opaquely to the manager, never
  logged/wrapped/echoed — `relay.go:433-436`) is entirely outside the touched lines.

- **Deferred surface is genuinely inert, not merely hidden.** The v1
  `Dispatcher`/`FirstFrameGate` and `AuthenticateFirstFrame`/`Token` surfaces remain
  compiled but have no production caller after this change. They are dead, not
  latent — no env var, flag, or config re-enables them (the only switch,
  `PYRY_MOBILE_V2`, is deleted here). Their physical removal is scheduled (slices
  3-4); until then they present no reachable attack surface.

**No new inbound-content parsing, no new outbound policy decision, no
nonce/key/token minting is introduced** — the change only subtracts. The single
security-relevant property to preserve, *authenticate-before-dispatch on the
untrusted relay surface*, is upheld by the surviving Noise_IK leg.

**Verdict: PASS.** The removal deletes a dead, unreachable-in-production dispatch
path plus its first-frame auth gate; it does not weaken, bypass, or remove any
control the surviving v2 path relies on.
