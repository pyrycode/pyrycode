# Spec: Real-claude e2e — permission round-trip under `interactive_runner: "stream-json"` (desktop#483 scenario) (#1154)

**Size:** S (comfortably). **Zero production source files.** One new `*_test.go` file in
`internal/e2e/realclaude/` that composes helpers already landed by #1030 and #1153. No
edit to any existing file; every reused symbol resolves at package scope and stays
byte-identical.

**Security-sensitive** — see `## Security review` at the end. The load-bearing property is
*an approval lands as allow only via an explicit, device-gated `allow_once` answer to a
modal that genuinely surfaced, and the turn observably resumes after* — proven on the
**real** claude stream stack, not the fake tier.

---

## Files to read first

The whole ticket is a composition of two shipped real-claude tests. Read both in full — the
new file is `TestInteractiveModalResolution`'s Phase A wearing #1153's stream-json toggle and
stronger drain. Reuse every cited symbol UNCHANGED (same package `realclaude`, same
`//go:build e2e_realclaude` tag — no new export, no edit to these files).

- `internal/e2e/realclaude/interactive_modal_resolution_test.go` (#1030) — **THE template;
  read the whole file.**
  - `startModalResolutionHarness` (lines 210-269) — the harness the new one mirrors. It pairs
    the phone **`--allow-remote-permissions`** (line 234 — the device gate; without it the
    answer denies at `ResolveAnswer` and the turn never resumes), seeds bootstrap-pool +
    bound conversation, and spawns via `spawnPermissionDaemon`. Copy it; add exactly ONE line
    (see § Design).
  - `spawnPermissionDaemon` (lines 271-316) — the no-`--dangerously-skip-permissions` spawn so
    real claude actually raises the modal. Reused verbatim.
  - `raiseRealPermissionModal` (lines 179-195) — sends the nonce-carrying Bash trigger, drains
    to `modal_shown`, asserts `Class == "permission"` + non-empty `ModalID` **before returning**
    (this IS the AC-2 non-vacuity gate), returns the `ModalID`. Reused verbatim.
  - Phase A answer (lines 120-133) — the exact `sealEnvelope` + `protocol.TypeModalAnswer` +
    `protocol.ModalAnswerPayload{ModalID, OptionID: string(turnevent.PermissionOptionKindAllowOnce),
    AnswerToken: "…"}` shape to lift. **Only the answer half; skip Phase B (cancel) — out of scope.**
  - `drainForControlEvent` (lines 327-370) — used internally by `raiseRealPermissionModal`;
    no direct call needed, just know it's there.
- `internal/e2e/realclaude/interactive_stream_liveness_test.go` (#1153) — the two composable
  seams this ticket rides:
  - `writeStreamInteractiveConfig` (lines 155-165) — writes `{"interactive_runner":"stream-json"}`
    to `<home>/.pyry/config.json` at `0o600`. **Call BEFORE the daemon spawns** —
    `resolveConfigPath` reads it once at startup (lines 92-95 show the placement). This is the
    single line that distinguishes this test from the PTY-path `TestInteractiveModalResolution`.
  - `drainForCompletedTurn` (lines 181-254) — the stronger drain AC-3 requires: M1 = a
    non-empty `assistant_delta` for the conv, M2 = the terminal `turn_state{idle}` for the conv
    observed AFTER M1. No content/echo assertion (real claude's words are non-deterministic).
    Reused verbatim. This replaces #1030 Phase A's `drainForAssistantReply` (M1 only) — the
    upgrade is exactly AC-3's "turn makes real progress … reaching terminal `turn_state{idle}`".
- `internal/e2e/realclaude/interactive_per_conversation_liveness_test.go` — `perConvHarness`
  struct (lines 140-146: `phone`, `initSend`, `initRecv`, `home`, `workdir`) the harness
  returns; and `const perTurnReplyBudget = 120 * time.Second` (line 85) — reuse as the
  completed-turn drain budget (generous: covers cold-tool-exec → continuation delta → idle).
- `internal/turnevent/taxonomy.go:50` — `PermissionOptionKindAllowOnce PermissionOptionKind = "allow_once"`
  (the approve option id the answer carries).
- `internal/protocol/messaging.go:127` — `ModalAnswerPayload` field names (`ModalID`,
  `OptionID`, `AnswerToken`).
- `internal/e2e/realclaude/fixtures.go` + the #854/#997/#1030 files — the shared harness
  surface (`WithWorktreeAuthenticated`, `runPyry`, `decodePairPayload`, `readPersistedServerID`,
  `waitBinaryHello`, `relayTestLogger`, `ensurePyryBuilt`, `shortSocketPath`,
  `seedBootstrapRegistry`, `seedBoundConversation`, `driveHandshakeInteractive`,
  `sealSendMessage`, `sealEnvelope`, `mustJSON`). All package-private, reused verbatim, **no
  edit**. (Note: feature/363 has in-flight edits to `fixtures.go`; this ticket only *calls*
  those helpers and never modifies the file, so there is no textual merge conflict — see
  § Open questions.)
- `docs/knowledge/features/e2e-realclaude.md` — suite conventions the new file must match:
  single `//go:build e2e_realclaude` header, `WithWorktreeAuthenticated` skip contract,
  `make e2e-realclaude`, **no CI workflow**, no `-race`, `t.Parallel()` NOT called.

---

## Context

Per the always-a-real-claude-gate policy (2026-07-08) the operator must never be the first
real-stack execution: every operator-facing happy-path flow needs a real-claude e2e that
actually RUNS in the pre-ship gate before the Mac daemon binary is swapped. The remote
permission round-trip — a phone answering a real tool-permission modal — is the entire point
of driving a session remotely, and the **stream-json** runner is the cutover target. This ADR
is the desktop#483 scenario on the real stack: on the PTY path this scenario is red today;
the stream path is expected to surface `modal_shown` correctly, and this spec proves it
against live claude.

Two rungs already exist and this is the third:

- **Fake tier, stream path (#1139):** `modal_shown` / `modal_answer` / verdict, timeout denies
  fail-closed — green against a spawned fakeclaude under `interactive_runner:"stream-json"`.
- **Real tier, PTY path (#1030, `TestInteractiveModalResolution`):** answer + cancel against
  live claude under the **default PTY** interactive runner (it does NOT write the stream-json
  toggle).

This ticket is the **stream-json variant of that PTY-path real test** — same answer
round-trip, flipped onto `interactive_runner:"stream-json"`. It is a sibling, not a
replacement: the PTY-path test stays; this one sits alongside it as the stream-path proof
(desktop#483 is exactly PTY-red vs. stream-green). It rides the reusable seam #1153 shipped
for exactly this purpose — `writeStreamInteractiveConfig` was kept a standalone config-writer
(not a spawn wrapper) so this permission-flow rider can compose it with `spawnPermissionDaemon`
instead of `spawnBootstrapDaemon` (#1153 spec, § "Why a config-writer, not a spawn wrapper").

Scope is **answer-only (approve)**. The cancel phase (`modal_cancel` → `modal_dismissed`)
that #1030 Phase B covers is out of scope here.

---

## Design

Purely additive, test-only. **Zero production source files.** One new file in
`internal/e2e/realclaude/`, one new test, one new package-private harness function, fresh
seeded-UUID constants. Every other symbol is reused verbatim from #1030/#1153/#997 — no edit
to any existing file.

### New file: `internal/e2e/realclaude/interactive_stream_modal_resolution_test.go`

Header `//go:build e2e_realclaude`, `package realclaude`. Imports mirror
`interactive_modal_resolution_test.go` MINUS the cancel-only surface (no `io`; the test body
does not reference `TypeModalCancel`/`ModalCancelPayload`).

#### Fresh seeded-UUID constants

Two new package-level constants, distinct **names** and **literals** from the #1153
(`streamBootstrapUUID`/`streamConvID`) and #1030 (`liveModalBootstrapUUID`/`liveModalConvID`)
fixtures so no file in the package redeclares a name. Follow the seed-validator shape
`xxxxxxxx-xxxx-4xxx-8xxx-xxxxxxxxxxxx` (UUIDv4 version/variant nibbles), e.g.:

- `streamModalBootstrapUUID = "77777777-7777-4777-8777-777777777777"` — the bootstrap session
  POOL id (seeded once at startup via `seedBootstrapRegistry`).
- `streamModalConvID = "55555555-5555-4555-8555-555555555555"` — the driving conversation,
  bound to the bootstrap id via `seedBoundConversation`.

The two real-claude tests never share on-disk state (each gets its own authenticated tempdir
HOME); the distinct literals keep any cross-test confusion impossible.

#### Harness — `startStreamModalResolutionHarness(t *testing.T) (*perConvHarness, string)`

A near-exact copy of `startModalResolutionHarness` (#1030 lines 210-269) with **exactly one
inserted line** and the fresh constants. Contract: pairs the phone
`--allow-remote-permissions`, seeds bootstrap-pool + bound conversation, spawns the
no-skip-permissions daemon under the stream-json runner, drives the interactive handshake,
returns `(*perConvHarness, streamModalConvID)`. Skips cleanly (`t.Skipf`) when claude / creds
are absent.

The one delta from #1030's harness, in order:

1. After `WithWorktreeAuthenticated(t)` + the `workdir` mkdir, and **BEFORE**
   `spawnPermissionDaemon`, call `writeStreamInteractiveConfig(t, home)`. (Order relative to
   `pair`/seed doesn't matter; before spawn does — the daemon reads `config.json` once at
   startup.) This is the stream-vs-PTY differentiator — the entire reason the file exists.

Everything else is identical to #1030's harness: the `exec.LookPath("claude")` skip guard,
`WithWorktreeAuthenticated`, the isolated `workdir` under HOME, `runPyry("pair", …,
"--allow-remote-permissions")`, `seedBootstrapRegistry(t, home, streamModalBootstrapUUID)` +
`seedBoundConversation(t, home, streamModalConvID, streamModalBootstrapUUID, workdir)`,
`fakerelay.New`, `spawnPermissionDaemon`, `readPersistedServerID` + `waitBinaryHello`,
`fakephone.Dial`, `driveHandshakeInteractive`. No `t.Parallel()`
(`WithWorktreeAuthenticated` calls `t.Setenv`).

**Why inline the harness rather than parameterize `startModalResolutionHarness`.** Adding a
"write stream config" hook to #1030's harness would edit another ticket's test file
(file-overlap risk) and inject a branch into a shipped path — a refactor of adjacent code the
suite's idiom explicitly avoids. Every real-claude test in this package
(#854/#997/#1028/#1030/#1153) inlines its own setup sequence, differing only in the deltas
that matter; #1153 set the exact precedent — it transcribed #854's body rather than refactor
the shared harness. Consistency + Simplicity First ⇒ inline. The "duplication" is a sequence
of shared-helper calls, which is the accepted idiom, not copied logic. (Inlining the body
directly into the test function instead of a named `startStreamModalResolutionHarness` helper
is an equally valid developer call; the named helper is prescribed only because it makes the
one-line delta legible.)

#### Test — `TestInteractiveStreamModalResolution(t *testing.T)`

Answer-only, single phase — #1030 Phase A on the stream stack. Scenario (the developer writes
the Go in the house idiom; this is the flow, not a pasted body):

1. `h, convID := startStreamModalResolutionHarness(t)`.
2. `nonce := time.Now().UnixNano()` — a per-run nonce keeps the trigger command distinct
   (defeats accidental caching) without asserting on its echo.
3. `modalID := raiseRealPermissionModal(t, h, 2, convID, nonce)` — sends the gated-Bash
   trigger and, **before returning**, drains to `modal_shown` and asserts `Class == "permission"`
   + non-empty `ModalID` (**AC 1 + AC 2 non-vacuity gate**: a modal that never surfaced
   deadlines the drain → `t.Fatalf`, never a silent pass; a non-permission modal fails).
4. Seal the approve answer (request ID 3): `sealEnvelope(t, h.phone, h.initSend, …)` with
   `protocol.TypeModalAnswer` + `protocol.ModalAnswerPayload{ModalID: modalID, OptionID:
   string(turnevent.PermissionOptionKindAllowOnce), AnswerToken: "e2e-1154-answer-token"}`.
   Lift the exact envelope shape from #1030 lines 121-132. `AnswerToken` is a client-minted
   idempotency key, not authorization — an arbitrary constant is fine.
5. `drainForCompletedTurn(t, h.phone, h.initRecv, convID, perTurnReplyBudget)` — **AC 3**:
   asserts the turn makes real progress after the answer — a non-empty continuation
   `assistant_delta` (M1) FOLLOWED BY the terminal `turn_state{idle}` (M2). "Modal answered but
   the turn never resumed" fails at the M1 timeout; "delta but never idle" fails at M2 —
   neither greens vacuously.

Request-ID sequencing mirrors #1030 Phase A exactly (2 = the trigger send inside
`raiseRealPermissionModal`, 3 = the `modal_answer`).

---

## Concurrency model

Unchanged from #1030/#1153. One spawned real `pyry` daemon (real `claude --model haiku`,
default permission mode), one headless phone. The test goroutine is the single reader of the
phone conn — `raiseRealPermissionModal`'s internal `drainForControlEvent` and then
`drainForCompletedTurn` read serially; the sequential Noise receive nonce forbids concurrent
reads and requires every `noise_msg` frame be decrypted in arrival order. Daemon lifecycle:
`spawnPermissionDaemon` blocks until the control socket is dialable; `t.Cleanup(d.stop)` does
SIGTERM → grace → SIGKILL. `fakerelay` closed via `t.Cleanup`. No goroutines spun by the test
itself → `-race` stays off (matches the suite; `make e2e-realclaude` has no `-race`).

---

## Error handling / failure modes

- **No credential / no claude** → `WithWorktreeAuthenticated` skips in ms with the
  named-variable diagnostic (`ANTHROPIC_API_KEY` / `CLAUDE_CODE_OAUTH_TOKEN`) before any
  daemon allocation; the `exec.LookPath("claude")` guard skips (not fatal) when claude is
  absent. Must **not** time out on the skip path (**AC 4**). CI has neither claude nor a
  credential, so the file compiles and skips cleanly (**AC 5**).
- **Modal never surfaces** → `raiseRealPermissionModal`'s internal `drainForControlEvent`
  deadlines at `modalSurfaceBudget` (120s) with a message naming the likely cause (trigger
  didn't raise a modal, tui-driver didn't classify it, or the surfacer didn't broadcast). The
  answer is never sent over a phantom modal.
- **Answer sent but turn never resumes** → `drainForCompletedTurn`'s per-milestone `t.Fatalf`:
  `!sawDelta` ⇒ "never observed a non-empty assistant_delta … the turn never drained
  end-to-end" (points at a device-gate denial or a UUID mismatch between the two seeds); delta
  but no idle ⇒ "the turn opened but never closed." The 120s `perTurnReplyBudget` absorbs cold
  tool-exec + continuation latency.
- **Device gate not granted** (`--allow-remote-permissions` omitted from `pair`) →
  `ResolveAnswer` denies at `dev.MayAnswerRemotePermission()`, no continuation streams,
  `drainForCompletedTurn` deadlines at M1 → loud failure. The flag is load-bearing; the
  harness pairs with it.
- **Daemon fails to start** → `spawnPermissionDaemon`'s `waitForReady` already `t.Fatalf`s
  with captured stderr.

---

## Testing strategy

- **Build-tag exclusion (AC 5):** the file carries only `//go:build e2e_realclaude`. After
  landing, `make test` / `make check` do not compile or run it (`make e2e-realclaude` globs
  `./internal/e2e/realclaude/...` and already exists — no Makefile edit).
- **Compiles + skips clean in CI (AC 4, AC 5):** verify `go vet -tags e2e_realclaude
  ./internal/e2e/realclaude/...` builds, and that with no creds the test skips (not hangs).
- **Real-claude run (code-review phase — the always-real-claude gate):** the developer runs
  `make e2e-realclaude` locally with credentials present to confirm the full round-trip drives
  live claude to a genuine Bash permission modal, `modal_shown{permission}` surfaces, the
  `allow_once` answer resolves, and the turn completes M1→M2 (~$0.01, one haiku turn + one tool
  call). This is the pre-ship instrument the operator gate later reuses.
- **No production change / single-file diff:** confirm the diff is exactly one new `*_test.go`
  file; no edit to `interactive_modal_resolution_test.go`, `interactive_stream_liveness_test.go`,
  or `fixtures.go`.

---

## Acceptance Criteria

- [ ] A real-claude spec (`internal/e2e/realclaude/interactive_stream_modal_resolution_test.go`,
  `//go:build e2e_realclaude`) drives a live claude to a genuine tool-permission prompt — the
  gated Bash tool under default permission mode via `raiseRealPermissionModal` — under
  `interactive_runner: "stream-json"` (toggled by `writeStreamInteractiveConfig(t, home)` before
  the daemon spawns).
- [ ] The spec asserts the prompt surfaces as a `modal_shown` event of `Class == "permission"`
  carrying a non-empty `ModalID`, **before** the answer is sent (the assertion lives inside
  `raiseRealPermissionModal`, which returns only after it holds — a modal that never surfaced
  deadlines the drain and fails).
- [ ] The spec answers the modal with `allow_once`
  (`protocol.TypeModalAnswer` + `ModalAnswerPayload{OptionID:
  string(turnevent.PermissionOptionKindAllowOnce)}`) and asserts the turn then makes real
  progress via `drainForCompletedTurn`: a subsequent non-empty `assistant_delta` (M1) AND the
  terminal `turn_state{idle}` for the conversation (M2) — so "modal answered but the turn never
  resumed" fails rather than greening vacuously.
- [ ] The spec is gated by `WithWorktreeAuthenticated(t)` and skips with the named-variable
  diagnostic when no live-claude credential is present; it does not time out on the skip path.
- [ ] The spec runs under `e2e_realclaude` / `make e2e-realclaude`, is excluded from
  `make test` / `make check`, and compiles and skips cleanly in CI.

---

## Open questions

- **feature/363 in-flight edit to `fixtures.go`.** #363 modifies
  `internal/e2e/realclaude/fixtures.go`; this ticket only *calls* helpers defined there and
  never edits the file, so no textual merge conflict is possible. The only integration risk is
  a signature change to a helper the new test calls — a normal compile-time reconciliation at
  merge, not a conflict, and covered by the developer's `make e2e-realclaude` build. No block
  set (the overlap rule gates on same-file textual edits, which this is not).
- **Named harness vs. inlined body.** Spec prescribes a `startStreamModalResolutionHarness`
  helper for legibility of the one-line delta. Inlining the setup directly into
  `TestInteractiveStreamModalResolution` (as #1153's `TestInteractiveStreamLiveness` does) is
  equally valid — the developer's call; both are the same LOC and the same reused helpers.
- **Second phase (cancel).** Deliberately out of scope (answer-only, per the ticket). #1030
  Phase B already proves cancel on the PTY path; a stream-path cancel rider, if ever wanted, is
  a cheap follow-up that reuses `modal_cancel` + `drainForControlEvent(TypeModalDismissed)` — do
  not add it here.

---

## Security review

**Verdict:** PASS

The adversarial question this pass must answer: *does the spec genuinely prove that a
tool-permission approval on the real stream stack lands as allow ONLY via an explicit,
device-authorized `allow_once` answer to a modal that actually surfaced — or does it green
over a gate that fails open (a stray allow, an unauthenticated answer, or a turn that "resumed"
without the tool being gated at all)?* I walked every category assuming the spec has holes.
This ticket adds **no production code** — it exercises existing gates against real claude — so
the review is about whether the *proof* is airtight, not whether a new surface is safe.

**Findings:**

- **[Trust boundaries]** No MUST FIX. The untrusted input is the phone's `modal_answer`
  (network→daemon). The trusted authority is the daemon's real permission-modal resolver,
  reachable to **allow** only when `ResolveAnswer` passes the device gate
  (`dev.MayAnswerRemotePermission()`) AND the `ModalID` matches an outstanding modal. The
  daemon runs **without** `--dangerously-skip-permissions` (`spawnPermissionDaemon`), so real
  claude genuinely gates the Bash call — the modal is not a fake trigger. The spec adds no new
  path to allow; the only new "boundary" is a test-written config file
  (`<home>/.pyry/config.json`) under a test-owned tempdir HOME — trusted test wiring, not a
  production trust boundary.

- **[Authorization actually required — the load-bearing check]** No MUST FIX; one SHOULD-note.
  The approve round-trip passes ONLY because the harness pairs the phone
  `--allow-remote-permissions`. Drop that flag and `ResolveAnswer` denies at the device gate,
  no continuation streams, and `drainForCompletedTurn` deadlines at M1 (loud failure). So the
  exercised allow is specifically the *gated-and-answered* one — never a default-allow. **SHOULD
  (code-review checkpoint):** confirm the developer keeps `--allow-remote-permissions` on the
  `pair` call (it is the single line that makes the answer authorized); losing it silently would
  turn a genuine allow-round-trip into a device-gate-denial that the M1 timeout would report as
  a generic drain failure rather than "the answer was unauthorized." The assertion still fails —
  fail-closed — but the diagnostic degrades.

- **[Non-vacuity — no green over a phantom or non-permission modal]** No findings. AC-2's gate
  is structural: `raiseRealPermissionModal` drains to `modal_shown` and asserts
  `Class == "permission"` + non-empty `ModalID` *before* the test seals any answer. A modal that
  never surfaces deadlines the internal `drainForControlEvent` (`t.Fatalf`), never a silent pass;
  a non-permission modal (a trust/onboarding modal slipping through) fails the `Class` check.
  The answer can never be sent over a modal that did not genuinely gate a real tool call.

- **[Turn actually resumed — no green over a stalled turn]** No findings. AC-3 uses
  `drainForCompletedTurn` (M1 non-empty `assistant_delta` → M2 terminal `turn_state{idle}`),
  strictly stronger than #1030's `drainForAssistantReply` (M1 only). "Answered but never
  resumed" fails at M1; "resumed but never closed" fails at M2. The tool executing after the
  authorized allow is what produces the continuation delta, so a fail-open regression (the gate
  letting the tool run without a real answer, or the answer resolving to allow without the
  device grant) cannot masquerade as a completed turn — either the modal assertion or the
  drain milestones catch it.

- **[Tokens, secrets, credentials]** No findings. No new token. `AnswerToken` is the existing
  client idempotency key (not authorization — dedup is the `modal_id` one-shot); an arbitrary
  constant is correct. The live-claude credential is injected via `WithWorktreeAuthenticated`'s
  `t.Setenv` into an isolated tempdir HOME (existing fixture, never logged). No secret is
  written to the test-owned `config.json` (it holds only `{"interactive_runner":"stream-json"}`).

- **[File operations]** No findings. The only new file op is `writeStreamInteractiveConfig`
  writing a fixed, non-user-derived path (`<home>/.pyry/config.json`) at `0o600` under a
  test-chosen tempdir — no path concatenation from untrusted input → no traversal. The seeded
  registries use the suite's existing atomic-write helpers, untouched.

- **[Subprocess / external command]** No findings. The trigger's `echo pyrycode-<nonce>` is a
  scripted, non-secret fixture command the real claude Bash tool would run *only after* the
  human-equivalent approve answer routes — which is precisely the gate under proof. No new
  `exec.Command` in the test beyond `spawnPermissionDaemon` (reused verbatim, no
  `--dangerously-skip-permissions`) and `exec.LookPath("claude")`. The nonce is a timestamp,
  content-free.

- **[Cryptographic primitives]** No findings. No new crypto. The Noise transport and the
  `crypto/rand` modal-id nonce are unchanged; the drain preserves the receive-nonce invariant
  by decrypting every `noise_msg` in arrival order (reused loop).

- **[Network & I/O]** No findings. All wire I/O is the existing headless-phone / fakerelay /
  Unix-socket path. Every drain is bounded by a finite deadline (`modalSurfaceBudget` 120s,
  `perTurnReplyBudget` 120s) → no hang, no unbounded read. The skip path allocates nothing
  before returning (AC-4 no-timeout-on-skip).

- **[Error messages, logs, telemetry]** No findings. The test asserts only structural fields
  (`Class`, non-empty `ModalID`, non-empty delta text, `State == "idle"`) — never claude's
  actual words (substrate-guard safe, real output is non-deterministic). No prompt/tool-input
  content is asserted or logged by the test.

- **[Concurrency]** No findings. Single test goroutine reads the phone serially; the daemon
  side (permission resolver, surfacer, relay) is pre-existing and unit-tested. No new locks, no
  new goroutines in the test. `t.Cleanup` tears down daemon + relay deterministically.

- **[Threat model alignment]** No MUST FIX. The relevant `protocol-mobile.md` § Security-model
  threat — an internet-sourced `modal_answer` escalating a real tool call to allow without the
  device grant, or a permission gate failing open on the stream runner — is exactly what this
  spec proves is mitigated end-to-end on the **real** stack (the fake tier #1139 owns the
  deterministic timeout/deny shape; the PTY real tier #1030 owns the PTY runner). It weakens no
  mitigation; it adds the stream-real-claude coverage rung the desktop#483 cutover gate
  requires. **OUT OF SCOPE:** the cancel/deny/timeout legs (owned by #1030 Phase B and #1139) —
  this rider is the approve round-trip only, by ticket scope.

No MUST FIX findings. The one SHOULD (keep `--allow-remote-permissions` on the `pair` call, so
the proven allow is the device-gated one) is inherited verbatim from the reused #1030 harness
and is a code-review checkpoint, not a design hole.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-21
