# Spec: Real-claude e2e — permission **DENY** on the stream-json runner (#1175)

**Size:** S (small end). **Zero production source files.** One new `*_test.go` file in
`internal/e2e/realclaude/` that composes helpers already landed by #1030, #1153 and #1154. No
edit to any existing file; every reused symbol resolves at package scope and stays
byte-identical.

**Security-sensitive** — see `## Security review` at the end. The load-bearing property is
*a permission-gated tool is refused (fails closed) via an explicit, device-authorized
`reject` answer to a modal that genuinely surfaced, the turn resolves to terminal idle rather
than hanging, and the gated tool's file never materialises* — proven on the **real** claude
stream stack, not the fake tier. The deny half is the security-relevant half of the
round-trip; #1154 proved allow, this proves deny.

This is the sibling of #1154 (allow) — a near-clone of its file with three deltas: (1) the
answer option kind flips `allow_once` → `reject_once`; (2) the post-answer drain proves the
turn *resolves* (idle) without requiring a continuation delta, and attributes the resolution
to **our** remote reject; (3) a filesystem check proves the gated `Write` did not execute.

---

## Files to read first

The whole ticket is a composition of three shipped real-claude tests. The new file is #1154's
`TestInteractiveStreamModalResolution` with the answer flipped to reject and two extra
assertions. Reuse every cited symbol UNCHANGED (same package `realclaude`, same
`//go:build e2e_realclaude` tag — no new export, **no edit to any of these files**).

- `internal/e2e/realclaude/interactive_stream_modal_resolution_test.go` (#1154) — **THE base;
  read the whole file.**
  - `startStreamModalResolutionHarness` (lines 135-200) — **reused verbatim** (called, not
    copied). Pairs the phone `--allow-remote-permissions` (the device gate — load-bearing for
    deny too, see § Security review), writes the stream-json toggle
    (`writeStreamInteractiveConfig`), seeds bootstrap-pool + bound conversation, spawns via
    `spawnPermissionDaemon` (no `--dangerously-skip-permissions`), returns
    `(*perConvHarness, streamModalConvID)`. **The new test calls this — it introduces no new
    harness and no new seeded-UUID constants.**
  - `TestInteractiveStreamModalResolution` body (lines 85-118) — the exact flow to transcribe:
    `raiseRealPermissionModal` (reqID 2) → seal `modal_answer` (reqID 3) → drain. The new test
    swaps the `OptionID` and the post-answer drain (see § Design).
- `internal/e2e/realclaude/interactive_modal_resolution_test.go` (#1030) — the shared trigger
  and drain scaffold:
  - `writeFileTrigger` (lines 181-183) — **reused verbatim.** The stream-runner trigger: "Use
    the Write tool to create a file named `pyrycode-<nonce>.txt` …". The `Write` of a fresh file
    is what genuinely gates on the stream-json runner (#1170: a bare echo auto-approves). The
    filename it targets is the AC-2 absence observable.
  - `raiseRealPermissionModal` (lines 198-213) — **reused verbatim.** Sends the trigger, drains
    to `modal_shown`, asserts `Class == "permission"` + non-empty `ModalID` **before returning**
    (this IS the AC-4 non-vacuity gate: a modal that never surfaced deadlines the internal
    `drainForControlEvent` and fails; a non-permission modal fails the `Class` check).
  - `drainForControlEvent` (lines ~327-370 — read the whole function) — **reused verbatim** for
    the new attribution drain. Drains and decrypts every `noise_msg` frame *in arrival order*
    (preserving the receive-nonce) until the target `Type`, fatals on timeout/error-envelope.
    The new test calls it with `protocol.TypeModalDismissed`.
  - Phase B dismissal assertion (lines ~155-162) — the `modal_dismissed` decode +
    `dis.Source == "remote"` pattern to **mirror** (#1030's is a *cancel*; ours is a *reject
    answer*, but the wire shape and the `Source` check are identical).
- `internal/e2e/realclaude/interactive_stream_liveness_test.go` (#1153) —
  `drainForCompletedTurn` (lines 181-254) — **the frame-loop template `drainForTurnIdle`
  mirrors, MINUS the mandatory M1 delta.** Copy its decrypt-in-order loop verbatim; keep only
  the terminal `turn_state{idle}` milestone, drop the "require a non-empty `assistant_delta`
  first" gate (real claude's post-denial continuation is non-deterministic — see § Design).
- `internal/e2e/realclaude/interactive_per_conversation_liveness_test.go` — `perConvHarness`
  struct (lines 140-146: `phone`, `initSend`, `initRecv`, `home`, **`workdir`**) the harness
  returns; `const perTurnReplyBudget = 120 * time.Second` (line 85) — reuse as the idle-drain
  budget. (`const modalSurfaceBudget = 120 * time.Second` lives in
  `interactive_modal_resolution_test.go:100` — reuse for the `modal_dismissed` drain.)
- `internal/turnevent/taxonomy.go:52` — `PermissionOptionKindRejectOnce PermissionOptionKind =
  "reject_once"` (the reject option id the answer carries; `RejectAlways` at :53 is the
  fallback — see § Open questions).
- `internal/protocol/messaging.go:143-156` — `ModalDismissedPayload{ModalID, Outcome, Source}`.
  Doc (lines 147-151): `Outcome` = the answered option id; `Source` ∈ **{remote, local,
  timeout}** — `remote` = a phone answer/cancel, `timeout` = deny-on-timeout fired. This closed
  vocabulary is the attribution key (§ Design, § Security review).
- `cmd/pyry/modal_resolve_v2.go:280-368` — `ResolveAnswer` (read it; grounds the attribution
  assertion). **Reject is device-gated exactly like allow** (line 297:
  `if !dev.MayAnswerRemotePermission()` → deny + `false`, no dismissal). A gated remote reject
  returns `ModalDismissal{Outcome: optionID, Source: "remote"}` (line 367) and calls
  `ResolveStream(modalID, allow=false, reasonRemoteDeny)` (line 341) — **the production path
  that refuses the parked stream Write, so the file never lands.**
- `internal/relay/v2session_modal.go:194-231` — `handleModalAnswer` (emits
  `broadcastModalDismissed` on a gated answer) vs. `handleModalTimeout` (line 220 — emits the
  `Source: "timeout"` dismissal when the approval window elapses with no answer). This is the
  exact fork the `Source == "remote"` assertion discriminates.
- `internal/e2e/realclaude/fixtures.go` + the #854/#997/#1030 files — the shared harness surface
  (`WithWorktreeAuthenticated`, `runPyry`, `sealEnvelope`, `mustJSON`, `perConvHarness`,
  `relayTestLogger`, `seedBootstrapRegistry`, `seedBoundConversation`, …). All package-private,
  reused verbatim, **no edit**.
- `docs/knowledge/features/e2e-realclaude.md` — suite conventions the new file must match:
  single `//go:build e2e_realclaude` header, `WithWorktreeAuthenticated` skip contract,
  `make e2e-realclaude`, **no CI workflow**, no `-race`, `t.Parallel()` NOT called.

---

## Context

Per the always-a-real-claude-gate policy (2026-07-08) the operator must never be the first
real-stack execution: every operator-facing flow on the stream-json runner (the Mac-daemon
cutover target) needs a real-claude e2e that RUNS in the pre-ship gate. The remote permission
round-trip is the entire point of driving a session remotely, and the round-trip has two
halves:

- **allow** — proven on the real stream stack by #1154 (`TestInteractiveStreamModalResolution`).
- **deny** — the **security-relevant** half (a gate that fails *closed*), and unproven against
  real claude today.

A permission denial that silently *executed* the tool anyway (fail-open), or a turn that
*hung* on the denied modal (no terminal idle), is exactly the real-claude-specific failure the
fake tier cannot surface (the recurring fake-green/real-red class, e.g. #949). Every other
stream deny behaviour is covered only against a scripted fakeclaude. This ticket adds real-claude
coverage for **exactly one** behaviour: the permission **DENY** round-trip on the stream-json
runner. Part of #1083 (T9); INDEPENDENT sibling leaves are #1173 (multi-turn continuity) and
#1174 (new-session rotation).

Deny rungs that already exist and this is NOT a replacement for:

- **Fake tier, stream path (#1139):** `modal_shown` / `modal_answer` / verdict, and
  **timeout-deny** fail-closed — deterministic, against a spawned fakeclaude under
  `interactive_runner:"stream-json"`.
- **Real tier, PTY path (#1030 Phase B):** *cancel* against live claude under the default PTY
  runner (a `modal_cancel`, not a reject *answer*).

This ticket is the **stream-json real-claude explicit-reject-answer** rung — the one the
desktop cutover gate is missing.

**Scope: explicit reject answer only.** The filer's optional "if cheap" riders —
**timeout-deny** (no answer → the #725 approval timer fires) and **disconnect-deny** (phone
drops) — are **out of scope**. Timeout-deny fail-closed is already covered deterministically at
the fake tier on the stream path (#1139); neither has an observed real-claude regression to
defend against, so per Evidence-Based fix selection each becomes its own real-claude ticket if
live coverage is later demanded. (These two are not merely deferred flavour text — the
`Source == "remote"` attribution assertion below exists precisely so that a *timeout*-deny can
never masquerade as this ticket's *explicit*-deny; see § Security review.)

---

## Design

Purely additive, test-only. **Zero production source files. Zero edits to existing files. Zero
new seeded-UUID constants** (the reused harness supplies them). One new file, one new test, two
small package-private drain/assert helpers. Every other symbol is reused verbatim.

### New file: `internal/e2e/realclaude/interactive_stream_permission_deny_test.go`

Header `//go:build e2e_realclaude`, `package realclaude`. Imports mirror #1154's file, plus
`path/filepath` + `os` for the absence walk, `encoding/json` for the `modal_dismissed` decode.
(No `io`.)

### Reuse the #1154 harness — no new harness, no new constants

The new test calls **`startStreamModalResolutionHarness(t)`** (#1154, merged) verbatim. The
ticket left "reuse the shared harness vs. stand up a deny-specific one" as the architect's
call: **reuse.** Rationale — the two tests never share on-disk state (each gets its own
`WithWorktreeAuthenticated(t)` tempdir HOME + its own daemon + its own registry seeded under
*that* HOME), and the suite runs them sequentially (no `t.Parallel()` — `WithWorktreeAuthenticated`
calls `t.Setenv`). So the shared seeded literals `streamModalBootstrapUUID` /
`streamModalConvID` are safe to reuse; standing up a deny-specific harness with fresh literals
would duplicate ~65 lines to differentiate nothing observable. Simplicity First ⇒ reuse. This
also means the new file redeclares **no** package-level identifier (see § Concurrency /
same-package hygiene).

### Test — `TestInteractiveStreamPermissionDeny(t *testing.T)`

The flow (the developer writes the Go in the house idiom; this is the sequence, not a pasted
body):

1. `h, convID := startStreamModalResolutionHarness(t)` — reused verbatim.
2. `nonce := time.Now().UnixNano()` — a per-run nonce keeps the trigger's target filename
   unique (defeats accidental caching **and** makes the absence walk unambiguous).
3. `modalID := raiseRealPermissionModal(t, h, 2, convID, writeFileTrigger(nonce))` — **AC 1 +
   AC 4.** Sends the gated `Write` trigger and, before returning, asserts a genuine
   `modal_shown{class=permission}` with a non-empty `modal_id` surfaced (a modal that never
   appeared deadlines the drain → `t.Fatalf`, never a silent pass). This proves the gated tool
   was *requested and held* — the anchor that makes the later absence check non-vacuous.
4. Seal the **reject** answer (reqID 3): `sealEnvelope(t, h.phone, h.initSend, …)` with
   `protocol.TypeModalAnswer` + `protocol.ModalAnswerPayload{ModalID: modalID, OptionID:
   string(turnevent.PermissionOptionKindRejectOnce), AnswerToken: "e2e-1175-answer-token"}`.
   Lift the exact envelope shape from #1154 lines 98-109; the **only** change from #1154 is
   `allow_once` → `reject_once`. (`AnswerToken` is a client-minted idempotency key, not
   authorization — an arbitrary constant is fine.)
5. **Attribution drain — AC (fail-closed *by our explicit reject*, not by timeout).** Drain to
   `modal_dismissed` and assert the resolution is ours:
   `env := drainForControlEvent(t, h.phone, h.initRecv, protocol.TypeModalDismissed,
   modalSurfaceBudget)`, decode `protocol.ModalDismissedPayload`, assert
   `dis.ModalID == modalID`, `dis.Source == "remote"`, and
   `dis.Outcome == string(turnevent.PermissionOptionKindRejectOnce)`. This is the crux of the
   deny test (see § Security review): "file absent + turn idle" is *also* the outcome of a
   timeout-deny (`Source == "timeout"`) or a device-gate-rejected answer (no dismissal at all →
   later timeout) — the `Source == "remote"` + `Outcome == "reject_once"` conjunction is the
   only thing that pins the deny to *this* answer.
6. `drainForTurnIdle(t, h.phone, h.initRecv, convID, perTurnReplyBudget)` — **AC 3.** Waits for
   the terminal `turn_state{idle}` for the conv; the turn resolves (aware it was denied) rather
   than hanging on the denied modal. It tolerates zero or more continuation deltas (see below).
7. `requireTriggerFileAbsent(t, h.workdir, nonce)` — **AC 2, fail-closed observable.** Asserts
   `pyrycode-<nonce>.txt` is absent anywhere under the daemon workdir: the gated `Write` did not
   execute. Checked AFTER idle so the tool phase is definitively over.

Request-ID sequencing mirrors #1154 exactly (2 = trigger send inside `raiseRealPermissionModal`,
3 = the `modal_answer`).

### New helper — `drainForTurnIdle(t, phone, cs, convID, timeout)`

**Why a new drain rather than reuse `drainForCompletedTurn`.** The ticket's flagged tension:
#1154's `drainForCompletedTurn` *requires* a non-empty continuation `assistant_delta` (M1)
**before** it will accept `turn_state{idle}` (M2). Under **allow** the trigger's trailing
"reply with a single short word" guarantees that delta. Under **deny** the Write is refused and
real claude's post-denial continuation is non-deterministic — it may emit a substantive
acknowledgement delta, or it may go straight to idle. Reusing `drainForCompletedTurn` verbatim
would therefore risk a flaky false-RED at M1. The hard AC-3 requirement is *terminal idle*
(the turn doesn't hang), not a continuation delta.

Contract (signature + behaviour; the developer writes the body by copying
`drainForCompletedTurn`'s decrypt-in-order loop and dropping the M1 gate):

- `func drainForTurnIdle(t *testing.T, phone *fakephone.Client, cs *noise.CipherState, convID string, timeout time.Duration)`
- Loops on a deadline; for each `noise_msg` frame, decrypts **in arrival order** (skips
  non-`noise_msg` control frames without decrypting — the receive-nonce invariant, identical to
  `drainForCompletedTurn` lines 203-223). `assistant_delta` frames for the conv are tolerated
  (optionally `t.Logf`'d), never required.
- Returns on the first `turn_state{idle}` for `convID`.
- On timeout: `t.Fatalf` naming the fail-closed failure — the turn hung on the denied modal and
  never reached terminal idle within `timeout`.
- The invariant asserted by the test that asserts non-vacuity: because this drain is sequenced
  **after** `raiseRealPermissionModal` (modal proven open) and **after** the `modal_dismissed`
  drain (modal proven resolved-by-us), the first idle-for-conv it sees is necessarily the
  *terminal* one — there is no pre-turn resting idle left on the wire (`raiseRealPermissionModal`'s
  internal `drainForControlEvent` already consumed the leading `turn_state{responding}` and any
  earlier idle in order while draining to `modal_shown`).

### New helper — `requireTriggerFileAbsent(t, workdir, nonce)`

- `func requireTriggerFileAbsent(t *testing.T, workdir string, nonce int64)`
- Walks the `workdir` subtree (`filepath.WalkDir`) for any entry whose base name is
  `fmt.Sprintf("pyrycode-%d.txt", nonce)`; `t.Fatalf` if one is found (naming the found path —
  "fail-open regression: the gated Write executed despite reject_once").
- **Why a walk, not a single `os.Stat` of `<workdir>/pyrycode-<nonce>.txt`.** The bound
  conversation resolves claude's cwd to `workdir`, so a relative `Write` of `pyrycode-N.txt`
  lands at `<workdir>/pyrycode-N.txt` — a top-level `os.Stat` satisfies the AC's literal
  wording. The walk is cheap belt-and-suspenders against the residual real-claude
  non-determinism of claude choosing a sub-path: absence *only at the one path we happened to
  check* would be a vacuous green if claude wrote elsewhere under the workdir. This is
  belt-and-suspenders in *different fabric* — the stochastic real-claude `modal_shown` gate
  (proves the Write was attempted) paired with a deterministic filesystem walk (proves nothing
  materialised). The unique nonce guarantees the walk cannot false-match any other file.
  (A single top-level `os.Stat` is the acceptable minimal fallback if the developer prefers it;
  the walk is the recommended form.)

---

## Concurrency model

Unchanged from #1154/#1030/#1153. One spawned real `pyry` daemon (real `claude --model haiku`,
default permission mode — **no** `--dangerously-skip-permissions`, so claude genuinely gates the
`Write`), one headless phone. The test goroutine is the single reader of the phone conn —
`raiseRealPermissionModal`'s internal `drainForControlEvent`, then the `modal_dismissed`
`drainForControlEvent`, then `drainForTurnIdle` read serially; the sequential Noise receive
nonce forbids concurrent reads and requires every `noise_msg` frame be decrypted in arrival
order (all three drains honour this). Daemon lifecycle + fakerelay teardown via `t.Cleanup`
(SIGTERM → grace → SIGKILL). No goroutines spun by the test itself → `-race` stays off (matches
the suite; `make e2e-realclaude` has no `-race`).

**Same-package hygiene.** All `internal/e2e/realclaude/*.go` files compile as one package.
Because this file reuses the #1154 harness it introduces **no** new seeded-UUID constant, so
there is zero UUID redeclaration risk. Its three new package-level identifiers —
`TestInteractiveStreamPermissionDeny`, `drainForTurnIdle`, `requireTriggerFileAbsent` — were
checked against `main` and the in-flight sibling branches (`feature/1172`, `feature/1173`,
`feature/1174`, `feature/1031`): no collision. The developer must keep these three names as-is
(or, if renamed, re-verify against those branches) so no same-package redeclaration surfaces at
merge.

---

## Error handling / failure modes

- **No credential / no claude** → `startStreamModalResolutionHarness` →
  `WithWorktreeAuthenticated` skips in ms (named-variable diagnostic:
  `ANTHROPIC_API_KEY` / `CLAUDE_CODE_OAUTH_TOKEN`) before any daemon allocation; the
  `exec.LookPath("claude")` guard skips (not fatal) when claude is absent. Must **not** time out
  on the skip path (**AC 5**). CI has neither → the file compiles and skips cleanly.
- **Modal never surfaces** → `raiseRealPermissionModal`'s internal `drainForControlEvent`
  deadlines at `modalSurfaceBudget` (120s) — the trigger didn't gate, tui-driver didn't classify
  it, or the surfacer didn't broadcast. The reject is never sent over a phantom modal.
- **Device gate not granted** (`--allow-remote-permissions` omitted) → `ResolveAnswer` denies at
  `dev.MayAnswerRemotePermission()` (Step 2), **returns `false`, emits NO `modal_dismissed`** →
  the `modal_dismissed` drain (step 5) deadlines at `modalSurfaceBudget` → loud RED. This is the
  *desired* failure — see § Security review; the harness pairs with the flag so the exercised
  reject is the device-authorized one. (Without the attribution drain, this failure mode would
  instead silently pass via the #725 timeout-deny.)
- **Reject answered but turn never resumes** → `drainForTurnIdle` deadlines at
  `perTurnReplyBudget` (120s) → "the turn hung on the denied modal and never reached terminal
  idle." Fail-closed on hang (**AC 3**).
- **Fail-open regression (the gate let the Write run)** → the parked stream approval executed the
  `Write` despite the deny → `requireTriggerFileAbsent` finds `pyrycode-<nonce>.txt` → RED
  (**AC 2**). This is the exact security regression the ticket exists to catch.
- **Daemon fails to start** → `spawnPermissionDaemon`'s `waitForReady` already `t.Fatalf`s with
  captured stderr.

---

## Testing strategy

- **Build-tag exclusion (AC 5):** the file carries only `//go:build e2e_realclaude`. `make test`
  / `make check` do not compile or run it; `make e2e-realclaude` globs
  `./internal/e2e/realclaude/...` (no Makefile edit).
- **Compiles + skips clean in CI (AC 5):** verify `go vet -tags e2e_realclaude
  ./internal/e2e/realclaude/...` builds, and that with no creds the test skips (not hangs).
- **Real-claude run (code-review phase — the always-real-claude gate):** the developer runs
  `make e2e-realclaude` locally with credentials present to confirm: live claude raises a genuine
  `Write` permission modal on the stream-json runner; the `reject_once` answer resolves it with
  `modal_dismissed{source=remote, outcome=reject_once}`; the turn reaches terminal `idle`; and
  `pyrycode-<nonce>.txt` is absent. (~$0.01, one haiku turn + one gated-and-refused tool call.)
  This is the pre-ship instrument the operator gate later reuses.
- **No production change / single-file diff:** confirm the diff is exactly one new `*_test.go`
  file; no edit to `interactive_stream_modal_resolution_test.go`,
  `interactive_modal_resolution_test.go`, `interactive_stream_liveness_test.go`, or `fixtures.go`.

---

## Acceptance Criteria

- [ ] A new real-claude e2e (`internal/e2e/realclaude/interactive_stream_permission_deny_test.go`,
  `//go:build e2e_realclaude`) stands up the interactive daemon on the production stream-json
  runner (`interactive_runner: stream-json`, via the reused
  `startStreamModalResolutionHarness`) against a live claude, raises a genuine permission modal
  via the `Write` trigger (`writeFileTrigger`, #1170), and answers it **DENY**
  (`OptionID: string(turnevent.PermissionOptionKindRejectOnce)`) from the paired phone.
- [ ] **Fail-closed observable — the gated tool does NOT execute:** `requireTriggerFileAbsent`
  asserts `pyrycode-<nonce>.txt` is absent under the daemon workdir after the turn resolves.
- [ ] **The turn resolves, aware it was denied:** `drainForTurnIdle` observes terminal
  `turn_state{idle}` for the driving conversation — it does not hang open on the denied modal;
  a hang deadlines the drain and fails.
- [ ] **Attribution (the deny is ours, not a timeout):** the spec drains `modal_dismissed` and
  asserts `Source == "remote"` and `Outcome == string(turnevent.PermissionOptionKindRejectOnce)`
  for the answered `ModalID`, so a timeout-deny (`Source == "timeout"`) or a gate-rejected answer
  (no dismissal) cannot green in place of the explicit reject.
- [ ] **Non-vacuity:** the modal genuinely surfaced before the reject — `raiseRealPermissionModal`
  asserts `modal_shown{class=permission}` with a non-empty `modal_id` before the answer is
  sealed; a modal that never appears deadlines the drain rather than passing silently.
- [ ] The spec is gated behind `e2e_realclaude`, passes green when run against a live claude on
  the stream-json runner, and skips cleanly (exit 0) when claude / credentials are absent —
  exactly like the sibling stream specs.

---

## Open questions

- **`reject_once` vs `reject_always`.** Spec prescribes `reject_once` (the minimal single-turn
  deny that matches "decline this request"). If the code-review real run shows claude *working
  around* the denial (e.g. retrying via a `Bash` redirect that the stream runner auto-approves,
  which would create the file and RED the absence check), switch to `reject_always` — it more
  strongly signals "do not do this" and discourages retry. Both are device-gated identically and
  both return `Outcome == <the option id>` in `modal_dismissed`, so the attribution assertion
  must read whichever kind was sent. The trigger anchors claude to the `Write` tool
  ("Use the Write tool to create a file …"), so a workaround is unlikely; noted as a real-run
  risk, not a design change.
- **Second gated modal after the deny.** If real claude, post-denial, requests a *second* gated
  tool, that modal sits unanswered and `drainForTurnIdle` would deadline (correctly reported as a
  hang). Not observed on the analogous PTY deny (#1030 Phase B); the 120s budget absorbs normal
  post-denial latency. If it proves flaky in the real run, the fix is `reject_always` (above),
  not a new drain.
- **feature/363 in-flight edit to `fixtures.go`.** As with #1154: this ticket only *calls*
  helpers defined there and never edits the file, so no textual merge conflict is possible — only
  a normal compile-time reconciliation if a called helper's signature changed, caught by the
  developer's `make e2e-realclaude` build. No block set (the overlap rule gates on same-file
  textual edits, which this is not; the branch-overlap check at architect time found no branch
  touching this new file).

---

## Security review

**Verdict:** PASS

The adversarial question this pass must answer: *does the spec genuinely prove that a
permission-gated tool on the real stream stack is **refused** (fails closed) via an explicit,
device-authorized reject to a modal that actually surfaced — or does it green over a gate that
failed open (the tool ran anyway), a turn that hung, or a deny that came from somewhere OTHER
than our explicit answer (a timeout, a dropped/gate-rejected answer)?* I walked every category
assuming the spec has holes. This ticket adds **no production code** — it exercises existing
gates against real claude — so the review is about whether the *proof of fail-closed* is
airtight, not whether a new surface is safe. The single sharpest risk for a **deny** test (which
#1154's allow review did not have to face): the passing outcome — *file absent + turn idle* — is
**also** the outcome of several non-deny paths, so the test must actively rule them out.

**Findings:**

- **[Trust boundaries]** No MUST FIX. The untrusted input is the phone's `modal_answer`
  (network→daemon); the trusted authority is `ResolveAnswer`
  (`cmd/pyry/modal_resolve_v2.go:280`), which for an eligible device maps `reject_once` →
  `allow=false` and calls `ResolveStream(modalID, false, reasonRemoteDeny)` (line 341) to refuse
  the parked stream `Write`. The daemon runs **without** `--dangerously-skip-permissions`
  (`spawnPermissionDaemon`), so real claude genuinely gates the `Write` — the modal is not a
  fake trigger. The spec adds no new path to deny (or to allow); the only test-written boundary
  is `<home>/.pyry/config.json` under a test-owned tempdir HOME (trusted test wiring, reused from
  #1154's harness).

- **[Attribution — the load-bearing check unique to a deny test]** No MUST FIX (this finding
  drove a design addition — the `modal_dismissed{source=remote, outcome=reject_once}` drain in
  step 5). A naive transcription of #1154 that only checked *file absent + terminal idle* would
  green **vacuously** on at least three non-deny paths, because all of them also leave the file
  absent and the turn eventually idle: (a) **timeout-deny** — the #725 approval timer fires
  (`handleModalTimeout`, `v2session_modal.go:220`), producing `modal_dismissed{source=timeout}`;
  (b) **device-gate rejection** — if `--allow-remote-permissions` were dropped, `ResolveAnswer`
  denies at Step 2 (`!dev.MayAnswerRemotePermission()`, line 297), emits **no** dismissal, and
  the modal later times-out-denies; (c) any path where our answer is silently dropped. The spec
  closes this by asserting `Source == "remote"` **and** `Outcome == "reject_once"` for the
  answered `ModalID` — the `remote` source is set only on the gated-answer path (line 367;
  `audit.SourceRemote`), never on the timeout path. This is what makes the fail-closed proof
  attributable to *our explicit reject* rather than to *something* denying. Without it the test
  could pass even if remote reject answers were completely broken. **This is the security
  contribution of the spec over a naive clone; it is a core AC, not a nicety.**

- **[Fail-open detection — does the deny actually stop the tool]** No findings. AC-2's
  `requireTriggerFileAbsent` walks the daemon workdir for `pyrycode-<nonce>.txt`. If a fail-open
  regression let the parked `Write` execute despite the deny, the file appears → RED. The
  `modal_shown{permission}` non-vacuity gate (AC-4, inside `raiseRealPermissionModal`) proves the
  `Write` was genuinely *requested and held* — so the absence is not the trivial absence of a
  file nobody asked for; it is the refusal of a specific gated write. The unique per-run nonce
  makes the walk unambiguous and immune to false matches. The walk (vs. a single top-level
  `os.Stat`) hardens against the residual real-claude non-determinism of claude writing to a
  sub-path — deterministic-code fabric paired with the stochastic modal gate.

- **[Non-vacuity — no green over a phantom or non-permission modal, or a non-terminal idle]** No
  findings. `raiseRealPermissionModal` asserts `Class == "permission"` + non-empty `ModalID`
  *before* any answer is sealed (a modal that never surfaced deadlines the internal
  `drainForControlEvent`). `drainForTurnIdle` is sequenced *after* both the proven-open modal and
  the proven-resolved (`modal_dismissed`) event, so the first idle-for-conv it accepts is
  necessarily terminal — the leading `turn_state{responding}` and any pre-turn resting idle were
  consumed in order by the earlier drains. An idle-only drain here cannot green a turn that never
  ran.

- **[Authorization actually required]** No MUST FIX; one SHOULD-note. The reject round-trip
  resolves as `remote` ONLY because the reused harness pairs the phone
  `--allow-remote-permissions` — reject is device-gated **identically** to allow
  (`ResolveAnswer` Step 2 runs before option classification). **SHOULD (code-review checkpoint):**
  confirm the developer keeps `--allow-remote-permissions` on the reused harness's `pair` call.
  Its loss no longer degrades to a *silent* pass (that was the pre-attribution risk) — the
  `modal_dismissed{source=remote}` drain would deadline (no dismissal is emitted on a gate
  rejection) → loud RED — but the diagnostic is "no modal_dismissed within 120s" rather than
  "unauthorized answer," so keeping the flag keeps the failure legible. Inherited from the reused
  #1154 harness.

- **[Tokens, secrets, credentials]** No findings. No new token. `AnswerToken` is the existing
  client idempotency key (not authorization — authorization is `ModalID` validity + the device
  gate); an arbitrary constant is correct. The live-claude credential is injected via
  `WithWorktreeAuthenticated`'s `t.Setenv` into an isolated tempdir HOME (existing fixture, never
  logged). No secret is written to `config.json` (it holds only
  `{"interactive_runner":"stream-json"}`, via the reused harness).

- **[File operations]** No findings. The only new file op is the read-only
  `requireTriggerFileAbsent` walk over a test-owned tempdir workdir — no writes, no path built
  from untrusted input (the path is `workdir` + a nonce-derived constant name), no traversal, no
  TOCTOU (the walk runs once, after the turn is idle, and only asserts absence). The reused
  harness's `config.json` write is `0o600` (unchanged from #1154).

- **[Subprocess / external command]** No findings. The trigger's `Write` of
  `pyrycode-<nonce>.txt` is a scripted, non-secret fixture prompt the real claude `Write` tool
  would run *only after* an allow — which is precisely the gate under proof, and here it is
  denied. No new `exec.Command` beyond the reused `spawnPermissionDaemon` (no
  `--dangerously-skip-permissions`) and `exec.LookPath("claude")`. The nonce is a timestamp,
  content-free.

- **[Cryptographic primitives]** No findings. No new crypto. The Noise transport and the
  `crypto/rand` modal-id nonce are unchanged; all three drains preserve the receive-nonce
  invariant by decrypting every `noise_msg` in arrival order (reused loop shape).

- **[Network & I/O]** No findings. All wire I/O is the existing headless-phone / fakerelay /
  Unix-socket path. Every drain is bounded by a finite deadline (`modalSurfaceBudget` 120s for
  `modal_shown` and `modal_dismissed`; `perTurnReplyBudget` 120s for idle) → no hang, no
  unbounded read. The skip path allocates nothing before returning (AC-5 no-timeout-on-skip).

- **[Error messages, logs, telemetry]** No findings. The test asserts only structural fields
  (`Class`, `ModalID`, `Source`, `Outcome`, `State == "idle"`, filesystem absence) — never
  claude's actual words (real output is non-deterministic; substrate-guard safe). No prompt/tool
  input content is asserted or logged by the test.

- **[Concurrency]** No findings. Single test goroutine reads the phone serially through three
  in-order drains; the daemon side (permission resolver, surfacer, relay, #725 timeout timer) is
  pre-existing and unit-tested. No new locks, no new goroutines in the test. `t.Cleanup` tears
  down daemon + relay deterministically.

- **[Threat model alignment]** No MUST FIX. The relevant `protocol-mobile.md` § Security-model
  threat — a permission gate on the stream runner **failing open** (executing a tool without a
  real authorized answer), or a turn hanging on an unresolved modal — is exactly what this spec
  proves is mitigated end-to-end on the **real** stream stack. The fake tier (#1139) owns the
  deterministic timeout/deny shape; the PTY real tier (#1030 Phase B) owns PTY cancel; this rung
  owns the stream-real-claude explicit reject answer. It weakens no mitigation. **OUT OF SCOPE
  (named):** timeout-deny and disconnect-deny real-claude coverage — deferred per Evidence-Based
  fix selection (timeout-deny is at the fake tier #1139; no observed real regression), each its
  own future ticket if demanded.

No MUST FIX findings. The one design-level risk a deny test carries that an allow test does not —
*the pass outcome is shared with non-deny paths* — is closed in the design itself by the
`modal_dismissed{source=remote, outcome=reject_once}` attribution assertion (a core AC), so it is
not left as a finding to fix. The single SHOULD (keep `--allow-remote-permissions` on the reused
harness's `pair` call) is inherited from #1154 and is a code-review checkpoint, not a design hole.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-23
