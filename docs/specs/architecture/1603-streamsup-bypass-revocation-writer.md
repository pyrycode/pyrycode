# Spec — #1603 streamsup: write a bypass revocation onto a live child's stdin as a `set_permission_mode` control request

**Ticket:** [#1603](https://github.com/pyrycode/pyrycode/issues/1603) · **Size:** S · **Labels:** `bug`, `security-sensitive`
**Blocks:** #1604 (session-layer wiring) · **Split from:** #1596 · **Measurement authority:** #1595

## Files to read first

Symbols, not line numbers — resolve each with `codegraph_search` / `codegraph_node`.

| Where | Symbol | What to extract |
|---|---|---|
| `internal/streamsup/envelope.go` | `controlRequest`, `controlRequestInner` | The outer/inner split this ticket extends. The outer type's doc already says the discipline "is kept for symmetry and future control subtypes" — this is that subtype. |
| `internal/streamsup/envelope.go` | `marshalInterruptEnvelope` | The marshaller to mirror: fixed literals, `json.Marshal`, append `'\n'`. Its doc credits the fixed literals for leaving it "no injection surface of its own" — the sentence this ticket's security argument extends. |
| `internal/streamsup/envelope.go` | `WriteInterrupt` | The package-level writer to mirror: nil-check FIRST, marshal, single `Write`, never close. Copy its error-wrap shape. |
| `internal/streamsup/envelope.go` | `ErrNoLiveChild` | The sentinel AC4 requires at both levels. Returned bare, never wrapped. |
| `internal/streamsup/envelope.go` | `marshalTurnEnvelope` | The injection-resistance argument in prose form; the model for the `requestID` injection test (§ Testing). |
| `internal/streamsup/runner.go` | `(*Runner).Interrupt` | The three-line method shape to mirror, and its doc's "concrete method, NOT on `sessions.Runner`" reasoning. |
| `internal/streamsup/runner.go` | `nextInterruptID`, `interruptSeq` | The counter this ticket reuses and renames (§ Design decision 3). |
| `internal/streamsup/runner.go` | `(*Runner).Stdin` | Returns an **untyped** nil under `r.mu` when no child is live, and releases the lock before returning — which is what makes the writer's `w == nil` check work and keeps the blocking write off the lock. |
| `internal/streamsup/envelope_test.go` | `TestMarshalInterruptEnvelope` | **AC3's gate. Its `want` literal must not be edited.** |
| `internal/streamsup/envelope_test.go` | `TestWriteInterrupt_NilRefusal`, `TestWriteInterrupt_WritesEnvelope`, `TestWriteInterrupt_WriteError`, `errWriter` | The four-test shape to mirror; `errWriter` is reused, not redeclared. |
| `internal/streamsup/envelope_test.go` | `TestMarshalTurnEnvelope_InjectionResistance`, `decodedControlRequest` | The injection table to mirror; the decode shape to extend with `Mode`. |
| `internal/streamsup/interface_test.go` | `TestRunner_Interrupt_NoLiveChild`, `TestRunner_Interrupt_LiveChildDelivers`, `TestRunner_NextInterruptID_Monotonic` | AC4's runner-level mirror, the live-child delivers pattern, and the test the rename touches. |
| `internal/streamsup/interface_test.go` | `helperRunCfg`, `runInBackground`, `waitForContains`, `safeBuffer` | Existing harness helpers — reuse, do not re-invent. The `echo_lines` helper child echoes stdin back as `ECHO:<line>`. |
| `internal/streamsup/parser.go` | `(*Parser).consumeLine`, `case "control_response"` | **Read this before deciding to read the ack.** #1500 already consumes the ack content-free; see § Design decision 5. |
| `docs/knowledge/features/set-permission-mode-inband-probe.md` | § "The `control_request` line sent", § "The escalation finding" | The verbatim measured line AC1 pins, and the one-direction finding AC2 enforces. |

**Do not read for a pattern to copy:** `internal/e2e/realclaude/set_permission_mode_probe_test.go` → `setModeControlLine`. It takes `mode` as a parameter because a probe must drive both directions. Production must not — that is AC2 and the whole security argument.

## Context

`internal/streamsup` owns the writer half of claude's stdin control channel. Today it writes exactly two envelope kinds: a user turn (`marshalTurnEnvelope`) and an interrupt control request (`marshalInterruptEnvelope`). The `controlRequest` type's doc has been carrying a promise since #1120 — its structured-encoding discipline "is kept for symmetry and future control subtypes." This ticket delivers the first of those subtypes.

#1595 measured `set_permission_mode` live against claude 2.1.220 and recorded it in `docs/knowledge/features/set-permission-mode-inband-probe.md`: a `set_permission_mode` control request on a running child's stdin drops the bypass posture with **no respawn**, verified three independent ways (a `success` control response, the next `init` line reporting `permissionMode: default`, and turn-2 behaviour matching a `default`-launched control child exactly).

**The revoke direction is the whole scope, by design and not by convenience.** #1595 also drove the opposite direction and claude refused it in words: a request for the bypass mode on a child launched without `--dangerously-skip-permissions` is rejected because the session was not launched with that flag. An enable delivered over this channel would be a privilege escalation reachable over the daemon's own stdin. So the mode this writer emits is a fixed literal in code, not a parameter — exactly as `marshalInterruptEnvelope` fixes its subtype. A general take-any-mode primitive would have no second caller and would leave the escalation shape one argument away from any future one.

This ticket adds the writer and **nothing in production that calls it**. That is expected, not an oversight. #1604 wires the session layer; #1605 proves the composed path live.

## Design

Three parts, mirroring the interrupt trio exactly. The split is what makes the marshalled bytes assertable without spawning a child.

```go
// internal/streamsup/envelope.go
func marshalBypassRevocationEnvelope(requestID string) ([]byte, error)
func WriteBypassRevocation(w io.Writer, requestID string) error

// internal/streamsup/runner.go
func (r *Runner) RevokeBypass() error
```

- `marshalBypassRevocationEnvelope` — builds `controlRequest` with the fixed literals `control_request` / `set_permission_mode` / `default`, `json.Marshal`, append `'\n'`. Returns the exact line AC1 pins; asserted by `TestMarshalBypassRevocationEnvelope`.
- `WriteBypassRevocation` — nil-check first (returns `ErrNoLiveChild`, writes nothing), then marshal, then one `Write`. Never closes `w`. Asserted by `TestWriteBypassRevocation_NilRefusal` / `_WritesEnvelope` / `_WriteError`.
- `(*Runner).RevokeBypass` — one line: `WriteBypassRevocation(r.Stdin(), r.nextControlID())`. Asserted by `TestRunner_RevokeBypass_NoLiveChild` and `TestRunner_RevokeBypass_LiveChildDelivers`.

**Neither exported entry point takes a mode.** No parameter, no struct field, no option, no variadic. That is AC2's first half, and it is the security property: the escalation direction is not one argument away, it is absent.

### Decision 1 — extend the shared inner type with an `omitempty` mode field

`controlRequestInner` gains a second field, **after** `Subtype`:

```go
type controlRequestInner struct {
	Subtype string `json:"subtype"`          // "interrupt" | "set_permission_mode"
	Mode    string `json:"mode,omitempty"`   // set_permission_mode only; omitempty is load-bearing (AC3)
}
```

Two properties, both verified mechanically before this spec was written (a throwaway `json.Marshal` of both shapes):

1. Go marshals struct fields in declaration order, so `Subtype` before `Mode` yields AC1's byte order exactly: `{"type":"control_request","request_id":"fixed-id","request":{"subtype":"set_permission_mode","mode":"default"}}`.
2. `omitempty` on the interrupt path (where `Mode` is the zero value) omits the key entirely, reproducing `TestMarshalInterruptEnvelope`'s existing `want` literal byte for byte. **Without `omitempty` the interrupt line becomes `…"request":{"subtype":"interrupt","mode":""}}` — which is AC3's hazard, and it diverges from the existing `want`, so that test is a real gate rather than a vacuous one.**

Why the shared type rather than a separate inner type (the AC deliberately does not pick): the outer type's doc has promised reuse since #1120, and a second outer struct would duplicate the `type` / `request_id` envelope contract in two places. `Request` is concretely typed, so reuse of the outer means either this field or genericising `controlRequest` — and generics for two subtypes is heavier than one `omitempty` tag. AC3's hazard is a wire-shape hazard, not a security one: the escalation surface is the *function signature*, not the struct field, and no function accepts a mode.

**The `omitempty` tag is the only thing keeping interrupt lines clean. Do not drop it, and do not weaken `TestMarshalInterruptEnvelope`'s byte-exact `want` — that assertion is AC3's enforcement.**

### Decision 2 — naming

`WriteBypassRevocation` mirrors `WriteTurn` / `WriteInterrupt` (verb + noun phrase); `RevokeBypass` mirrors `Interrupt` (a bare verb on the runner). The intent name is chosen over a wire name like `WriteSetPermissionModeDefault` because it states what the daemon is doing at its own level of abstraction, and because `RevokeBypass()` reads as having no opposite in the API — which is the point. Both names are free of collisions today (`git grep` for `Revoke`/`Revocation` in Go finds only the unrelated `pair`-token family in `cmd/pyry` and `internal/sessions`).

### Decision 3 — one control-request sequence, reused and renamed

The ticket leaves the id source to the architect. **Reuse the existing counter, and rename it to match its now-shared role:**

- field `interruptSeq` → `controlSeq`
- method `nextInterruptID` → `nextControlID`

Both new names are verified free of collisions. Two Go call sites change (`(*Runner).Interrupt` and `TestRunner_NextInterruptID_Monotonic`, which is renamed to `TestRunner_NextControlID_Monotonic`); its `first != "1" || second != "2"` assertion stays valid because it builds a fresh `&Runner{}`.

Why reuse rather than add a second counter: `request_id` must be unique across **all** in-flight control requests on one stream, not per subtype. Two independent counters would both mint `"1"`, and a future ack-correlator keyed on `request_id` could not tell an interrupt ack from a revocation ack. The existing counter's own doc names that future correlator as its reason for existing, so a second counter would silently break the property the first one documents itself as providing. The rename is the honest cost of the reuse decision, not adjacent refactoring — a counter named `interruptSeq` minting revocation ids is exactly the inconsistency a reviewer would flag.

The id stays **locally minted and never caller-supplied**, and it remains write-only: nothing correlates it (§ Decision 5).

### Decision 4 — the `requestID` contract on the exported writer

`WriteBypassRevocation` is exported and takes a `string`, so it is worth stating what the string may be: **a locally-minted id only.** Its doc comment must say so, and `RevokeBypass` is the only in-repo caller that satisfies it.

The structured encoding makes a hostile id non-catastrophic rather than merely unlikely — `json.Marshal` escapes every metacharacter, so no id can introduce a second physical line and forge a `result`, an interrupt, or a permission approval. That is the same invariant `marshalTurnEnvelope` holds for untrusted prompts, and § Testing pins it with the same table shape. The contract and the escaping are belt and suspenders of different fabric: a doc-comment convention plus a deterministic test.

### Decision 5 — do not read the ack; #1500 already consumes it

`(*Runner).Interrupt` deliberately never reads the `control_response`. **Do the same.** Adding a reader would need a stdout tap this ticket has no consumer for.

The ticket's framing ("nothing in production consumes a control reply") needs one correction that *removes* work rather than adding it: #1500 landed a `case "control_response"` arm in `(*Parser).consumeLine` that consumes the ack **content-free** — logging the top-level type only, never the line and never the `request_id`. Nothing *correlates* the ack, so "do not read it" still stands; but the ack is no longer unhandled.

**Consequence, and it is load-bearing for this ticket: the revocation's ack needs no parser work at all.** #1500's arm dispatches on the top-level `type` and reads nothing beneath it, and its test table already carries the revocation ack shape verbatim (`{"type":"control_response","response":{"subtype":"success","request_id":…,"response":{"mode":"default"}}}`) alongside the bypass-refusal NAK. So a revocation cannot regress the `unrecognized_message` frame that #1500 exists to protect. **Do not touch `internal/streamsup/parser.go`.**

### Decision 6 — naming the refused value without tripping AC2's grep

AC2's second half is `git grep -F '"bypassPermissions"' -- 'internal/streamsup/*.go'` returning no hits. The pattern includes the double quotes, so it matches a Go string literal or JSON field value of exactly that token — not a bare prose mention.

This is already demonstrated inside the target directory: `internal/streamsup/parser_test.go` contains the token bare inside #1595's longer refusal sentence, and the grep does **not** trip on it. So when documenting #1595's refusal at the new writer, **name the value bare or in backticks, never in double quotes.** A comment containing the quoted form would trip the grep, exactly as `set_permission_mode_probe_test.go` does.

Both halves of AC2's grep were control-run before this spec: unfiltered it finds 17 hits repo-wide including the real Go literal in `set_permission_mode_probe_test.go`, and the pathspec itself resolves (a known-present control token finds 5 hits across 3 files, 16 `.go` files covered). The check discriminates; it does not report absence unconditionally.

### Data flow

```
sessions layer (#1604 — NOT this ticket)
        │
        ▼
(*Runner).RevokeBypass()
        │  r.Stdin()          → io.Writer, or untyped nil (no live child); r.mu released before return
        │  r.nextControlID()  → "N" (atomic, digits only, write-only)
        ▼
WriteBypassRevocation(w, id)
        │  w == nil ────────────────────────────────► ErrNoLiveChild (zero bytes written)
        ▼
marshalBypassRevocationEnvelope(id)  → fixed literals + escaped id + '\n'
        ▼
one w.Write(env)  → child's held-open stdin  (never closed; write happens OFF r.mu)
        │
        ▼
claude drops the bypass posture, no respawn      → control_response ack
                                                    → consumed content-free by #1500's parser arm
```

## Concurrency model

No new goroutines, no new locks, no channels. `RevokeBypass` is safe from any goroutine:

- The id comes from `atomic.Uint64.Add`.
- `Stdin()` takes and releases `r.mu` itself, so the potentially-blocking `Write` happens **without `r.mu` held** — a slow or full pipe cannot block the `Run` goroutine's `setStdin`. Mirrors `Interrupt`.
- Teardown race (`Stdin()` captures the handle → teardown closes the pipe → `Write`) surfaces as a wrapped `EPIPE`, never a panic and never a false success. Same contract as `WriteTurn` / `WriteInterrupt`.

## Error handling

| Condition | Behaviour |
|---|---|
| `w == nil` (no live child) | Return `ErrNoLiveChild` **bare** (matchable with `errors.Is`), write zero bytes. Checked first, so no panic and no partial write. |
| `Stdin()` nil at the runner level | Same sentinel, via the same path — AC4's second level. |
| Marshal failure | Wrapped `streamsup: marshal bypass revocation: %w`. Not reachable with fixed literals and a string id; defensive, mirroring `WriteInterrupt`. |
| `Write` failure (e.g. EPIPE mid-teardown) | Wrapped `streamsup: write bypass revocation: %w`. Never mis-reported as `ErrNoLiveChild`. |
| claude refuses / NAKs the request | **Not observable here by design** — nothing reads the ack (§ Decision 5). The revoke direction has only ever been observed to succeed (#1595). |

Three reject branches total. No logging in the writer: no `request_id`, no envelope bytes, matching the parser's content-free discipline.

## Testing strategy

All under `make check`. The live proof of the composed path is #1605; this ticket needs no `needs-real-claude`.

**`internal/streamsup/envelope_test.go`**

- `TestMarshalBypassRevocationEnvelope` — for `"fixed-id"`, assert the output equals AC1's literal byte for byte (field order included); exactly one raw newline and it is the terminator; and it round-trips through a decode shape extended with `Mode` (`subtype` = `set_permission_mode`, `mode` = `default`, `request_id` = `fixed-id`).
- `TestMarshalBypassRevocationEnvelope_RequestIDInjectionResistance` — table-driven over hostile ids, mirroring `TestMarshalTurnEnvelope_InjectionResistance`'s table shape and rows: an embedded forged `result` line, an envelope-breakout-then-`control_request`, multiple embedded newlines, quotes/backslashes/tabs, carriage returns, the empty id, unicode and control bytes. For every row: exactly one raw newline (the terminator), the line decodes as a single JSON object, and `subtype`/`mode` still decode as the fixed literals — an id cannot rewrite the mode or append a second line.
- `TestWriteBypassRevocation_NilRefusal` — AC4 level 1: nil writer yields `ErrNoLiveChild`, no panic.
- `TestWriteBypassRevocation_WritesEnvelope` — the bytes on the sink equal `marshalBypassRevocationEnvelope`'s output.
- `TestWriteBypassRevocation_WriteError` — reuse the existing `errWriter`; the error is non-nil, is **not** `ErrNoLiveChild`, and mentions `write bypass revocation`.
- `TestMarshalInterruptEnvelope` — **unmodified.** AC3 is satisfied by this existing test staying green with its existing `want`. Do not edit it, and do not weaken its byte-exact assertion.

**`internal/streamsup/interface_test.go`**

- `TestRunner_RevokeBypass_NoLiveChild` — AC4 level 2: on an unspawned runner `Stdin()` is nil, so `RevokeBypass` returns `ErrNoLiveChild` without writing and without panicking. Mirrors `TestRunner_Interrupt_NoLiveChild`.
- `TestRunner_RevokeBypass_LiveChildDelivers` — mirrors `TestRunner_Interrupt_LiveChildDelivers`: spawn the `echo_lines` helper child, wait for `READY`, call `RevokeBypass`, and assert the echoed line carries `type` = `control_request`, `request.subtype` = `set_permission_mode`, `request.mode` = `default`, and a non-empty locally-minted `request_id`. Additionally send an `Interrupt` in the same test and assert the two echoed `request_id`s **differ** — the two-line pin on § Decision 3's shared-sequence invariant.
- `TestRunner_NextControlID_Monotonic` — the renamed `TestRunner_NextInterruptID_Monotonic`, assertions unchanged.

**Verification command:** `make check`.

## Explicitly out of scope

- **`internal/sessions` and `inBandDeliverable`** — untouched. #1604 owns the wiring and the direction split.
- **`cmd/pyry/streamsup_runner.go`** — the `streamRunner` adapter gains **no** forwarder here. `sessions.Runner` stays un-widened (#1077); a forwarder with no dispatcher and no test is #1604's, alongside the type assertion that reaches it (the pattern `Interrupt` / `RestartFresh` / `BeginRotation` already follow).
- **`internal/streamsup/parser.go`** — no ack reader, and none needed (§ Decision 5).
- **`internal/e2e/internal/fakeclaude`** — no arm for the new subtype. Its `interruptControlRequest` matches on `request.subtype == "interrupt"`, so a revocation line would simply be ignored; nothing in this ticket sends one. #1604/#1605 own the fake's rider if their e2e needs an ack.
- **The enable direction** — structurally absent, and refused by claude anyway (#1595). It stays on the `Restart(newArgs)` respawn path.
- **`docs/knowledge/codebase/1603.md`** — the documentation phase writes it from this spec plus the merged diff. **Not a developer AC.**

**Note for the documentation phase (not developer scope):** § Decision 3's rename makes one evergreen reference stale — `docs/knowledge/features/streamsup-package.md` names `nextInterruptID` as the id source. That file needs the new subtype documented regardless, so the rename folds into an edit already required.

## Open questions

1. **Should `RevokeBypass` be idempotent-aware?** Revoking on a child that is already in `default` mode is untested — #1595 only measured revoke-from-bypass. claude most likely returns a `success` ack for a no-op, but nothing here reads the ack, so the method cannot distinguish the cases and does not try. #1604 decides whether the session layer tracks posture to avoid the redundant write; this primitive stays stateless.
2. **The interleaving hazard is inherited, not introduced.** Two concurrent writes onto one stdin pipe could in principle interleave; a single small write to a pipe is atomic up to `PIPE_BUF` (4096 on both target platforms) and this envelope is ~110 bytes, so a revocation cannot be split. A concurrent `WriteTurn` carrying a prompt larger than `PIPE_BUF` is the only shape that could interleave, and that exposure is identical for `WriteInterrupt` today. No occurrence has been observed; per the evidence-based rule, no write mutex is added here. If one is ever warranted it belongs on `Runner` and covers all three writers at once.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. The envelope has exactly one variable field, `request_id`, and it never carries untrusted data: `RevokeBypass` mints it from `nextControlID` (`strconv.FormatUint` of an atomic counter — digits only), never from a caller, a device, or the relay. This is a stronger position than `WriteTurn`, which does carry untrusted prompt bytes. The boundary is a single function (`marshalBypassRevocationEnvelope`), not scattered.
- **[Trust boundaries]** SHOULD FIX — **addressed in this spec, listed because the gap was real.** `WriteBypassRevocation` is exported and takes a bare `string`, so a future in-package caller could hand it a non-minted id; the interrupt path never got a test for this. § Decision 4 adds the doc-comment contract and § Testing adds `TestMarshalBypassRevocationEnvelope_RequestIDInjectionResistance`, which pins that even an adversarial id cannot forge a second physical line or rewrite the fixed mode. Code-review should confirm both landed.
- **[Tokens, secrets, credentials]** No findings, and the sequential id is deliberately *not* one. `request_id` is not a credential or a capability — claude authorises nothing on it, and nothing in pyry correlates it — so predictability grants no attacker anything and `crypto/rand` would be miscategorisation, not hardening. It is never logged (the writer logs nothing; #1500's parser arm logs the top-level type only). Lifecycle is trivial: minted per request, written once, never stored.
- **[File operations]** Not applicable by design. The writer takes an `io.Writer` and never opens, creates, stats, or renames a path. No traversal, TOCTOU, mode, or symlink surface exists.
- **[Subprocess / external command execution]** No findings, and this is the category that matters most. Nothing is spawned, signalled, or `exec`'d — the write goes to an already-live child's stdin. The privilege-relevant property is direction, and escalation is unreachable **three** independent ways: no function on the added surface accepts a mode (AC2, structural); the emitted mode is a fixed literal beside the fixed subtype; and claude itself refuses the enable direction on the launch argv (#1595). The worst outcome an attacker who reached `RevokeBypass` could cause is a *downgrade* — a yolo session's tools start getting gated, i.e. degraded function, never added privilege. Re-granting still requires the respawn path, so a revocation cannot be silently undone in-band either.
- **[Cryptographic primitives]** Not applicable by design. No randomness, keys, nonces, or comparisons. The counter is an `atomic.Uint64`, and per the Tokens finding no security property depends on its unpredictability.
- **[Network & I/O]** No findings. The writer performs one bounded write (~110 bytes plus the id) and no reads, so there is no input to cap and no parse to bound. No timeout is set, matching `WriteInterrupt`; a full-pipe block is bounded in practice by the envelope being far under `PIPE_BUF`, and § Concurrency keeps that write off `r.mu` so it cannot stall the `Run` goroutine.
- **[Error messages, logs, telemetry]** No findings. Errors name the operation only (`marshal bypass revocation` / `write bypass revocation`) and never include the envelope bytes or the `request_id`; `ErrNoLiveChild` returns bare so `errors.Is` works. The writer emits no log records at all, so there is no new field to classify as must-not-log. No telemetry.
- **[Concurrency]** No findings introduced. No new goroutines or locks; the id is atomic; the blocking write happens off `r.mu`; the teardown race surfaces as a wrapped `EPIPE` rather than a panic or a false success. The pipe-interleaving hazard is inherited from the existing writers and unobserved — recorded as Open question 2 rather than defended against speculatively.
- **[Threat model alignment]** OUT OF SCOPE → **#1604.** The threat #1595 raised is that anything able to write a child's stdin can drop that child's bypass posture mid-session. This ticket does not widen who can do that — it adds no production caller and no interface method, so the surface stays daemon-internal. Deciding *who* may trigger a revocation (and whether a relay-reachable request can reach it) is #1604's call, and it should carry the `security-sensitive` label for that reason.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-19
