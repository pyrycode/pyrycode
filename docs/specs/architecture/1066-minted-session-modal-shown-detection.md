# Spec #1066 — Minted per-conversation session must surface `modal_shown`

**Ticket:** [#1066](https://github.com/pyrycode/pyrycode/issues/1066) · **Size:** S · **Labels:** `bug`, `security-sensitive`
**Split lineage:** #1050 → #1063 → { #1065 (scoping, LANDED), **#1066 (this — detection/emission)** }

## Files to read first

Turn-1 data load. Read these before touching anything — the pipeline is already built; this ticket adds a minted-session regression path + a fix (if an in-repo defect exists).

- `cmd/pyry/interactive_modal_stream_v2.go` (whole file, 167 lines) — `runModalStream` (the modal drain), `startInteractiveModalStreamV2` (the single startup wiring), `boundScreenText` (the #1065 single-cursor-read that pairs `conversation_id` + screen). **This is the follow-active modal path; the fix, if any, lives here or in what it resolves.**
- `cmd/pyry/interactive_turn_stream_v2.go:436-510` — `resolveTarget`, the follow-active `TargetResolver` **shared** by the turn stream and the modal stream. The three branches (`convID==""` bootstrap / bound-to-bootstrap / bound-to-minted). The minted branch (line 499-508) is what a minted conversation resolves through.
- `cmd/pyry/interactive_modal_v2.go:84-144` — `interactiveModalEmitterV2.Handle` / `handleModalShown`. The scoped emission: `reg.Record(req, class, convID)` (line 109) stamps `conversation_id`. **Do not add a second emission route — see § Security.**
- `internal/modalbridge/modal.go:145-165` — `Registry.Record(req, wireClass, convID)`; confirms `ConversationID = convID` is stamped atomically under the same lock that mints `modal_id` (#1065).
- `internal/turnbridge/producer.go:31-63,191-264` — `SessionHost` (the `Session()` + `WaitForPTY` seam), `Target`, `NewTargetSubscriber`. Shows how a (re)subscription captures the resolved host's live `*tuidriver.Session` and subscribes to its `Events()`. **The turn stream and modal stream use this identically.**
- `internal/sessions/pool.go:405-437` (bootstrap supervisor build) vs `internal/sessions/pool.go:1241+` `buildSession` (minted). The **only** argv/config deltas between bootstrap and minted: `--session-id` source, `WorkDir`, and `ResolveTranscript`. Both get the same `--settings` MCP file (line 412 / buildSession), same `Bridge`, same hosting.
- `internal/e2e/per_conversation_eviction_test.go:332+` — `createConversationViaPhone`: the hermetic "mint a per-conversation session over the wire" helper AC3 reuses.
- `internal/e2e/relay_v2_modal_answer_test.go:46-130` (`modalHarness` / `bringUpModalHarness`) — the #791 hermetic modal harness. Note: it raises the modal on the **bootstrap** session (`convID==""`, no bound conversation seeded). AC3 must raise it on a **minted** session instead.
- `internal/e2e/internal/fakeclaude/main.go:68-134` — the `PYRY_FAKE_CLAUDE_MODAL_TRIGGER` fixture: a file whose appearance makes fakeclaude paint a permission-modal screen once, driving tui-driver's `Unknown→Permission` class transition.
- `internal/e2e/realclaude/interactive_modal_resolution_test.go` (whole file) — #1030's real-claude modal gate on the **bootstrap** session: `spawnPermissionDaemon` (the no-`--dangerously-skip-permissions` variant), `raiseRealPermissionModal`, `drainForControlEvent`. AC4 combines this with the per-conversation harness.
- `internal/e2e/realclaude/interactive_per_conversation_liveness_test.go` — #997's real-claude per-conversation harness (`startPerConversationHarness`). AC4 = this harness + `spawnPermissionDaemon` + `raiseRealPermissionModal`.
- Memory/doc: `docs/knowledge/codebase/1030.md` § Reliability determination — the tui-driver v1.10.0 / claude 2.1.199 version-lock status (relevant to whether AC4 can run green).

## Context

A per-conversation interactive session (created over the wire → the daemon mints a dedicated claude session via `Pool.GetOrCreate`) started **without** `--dangerously-skip-permissions` blocks on claude's PTY permission prompt when a turn hits a gated tool. The daemon never fans `modal_shown`, so the remote sees no dialog and no reply; the turn wedges in the client's 120 s window.

Evidence: the desktop real-claude gate `real-claude-permission-modal.spec.ts` FAILED against a minted per-conversation session (no `modal_shown`, no reply). The fake-daemon twin passes, ruling out the client. Detection is proven for the **bootstrap** session by #1030 (`TestInteractiveModalResolution`, real claude).

**Why now:** #1065 (the scoping half) landed 2026-07-17. This is the detection/emission half — the operator outcome (approve/deny remotely on a per-conversation session) needs both.

### Core finding — the detection/emission pipeline is structurally SHARED

The single most important thing this spec contributes: **there is no bootstrap-only branch in the modal pipeline.** Walked end-to-end:

1. **Emission + scoping** (`interactiveModalEmitterV2` → `reg.Record(req,class,convID)` → broadcast) is one code path for all sessions. #1065's `conversation_id` stamp happens here regardless of session origin.
2. **Follow-active resolution** (`resolveTarget`) is **shared** by the turn stream and the modal stream. The turn stream demonstrably reaches minted sessions — AC2 states `assistant_delta`/`turn_end` already fan for the per-conversation turn — so `boundHost(convID)`, `WaitForPTY`, `Session()`, and the `Switch`-driven re-subscribe all resolve the minted host correctly.
3. **Detection** (`EventKindPtyModalShown` from tui-driver's `classify(snapshot, cols, rows)` in `mergeEvents`) runs inside the `Session.Events()` merge loop. The screen buffer is fed by tui-driver's internal PTY reader at `Spawn` for **every** session (`tui-driver session.go` reader goroutine), independent of whether `MirrorOutput` is drained. Default dims are 40×120 for bootstrap and minted alike (`tui-driver pty.go` `DefaultPtyRows/Cols`); #1030 proves 40×120 detects real modals in the daemon.
4. **Config deltas** between bootstrap and minted (`pool.go` vs `buildSession`) are limited to `--session-id` source, `WorkDir`, and `ResolveTranscript` — none touches modal rendering. Both get the same `--settings` file (`SkipDangerousModePermissionPrompt`/`EnableAllProjectMcpServers`, #943); #1030 shows that file does **not** suppress per-tool permission modals.

**Consequence:** a hermetic minted-session modal reproduction is *very likely to already pass on `main`*, because the in-repo path is symmetric with the (working) bootstrap path and the (working) minted turn stream. The genuinely-unknown remainder is real-claude-tier: whether claude 2.x renders a **freshly-minted** per-conversation session's permission dialog in a way tui-driver 1.10.0 classifies — a rendering/timing/version-skew surface that the fake tier does not reproduce. This drives the diagnosis order below and is an explicit Open Question.

## Design — diagnosis-first, then the minimal fix

This is a diagnosis ticket with unknown root-cause depth. Do **not** start by editing production code. Start by building the reproduction; let its result on `main` decide whether an in-repo fix exists.

### Step 1 — Build AC3 (the hermetic minted-session modal test) FIRST, run it on `main`

Contract (see § Testing for the scenario). Then branch on the result:

- **RED on `main`** (no `modal_shown` fans for the minted session hermetically): there **is** an in-repo defect. Diagnose it with the RED test in hand (candidate areas in Step 2), apply the minimal fix, go GREEN. This satisfies AC1 + AC3 directly.

- **GREEN on `main`** (the minted session already fans `modal_shown` hermetically): the in-repo detection/emission path is correct — consistent with the Core finding. This is a **valid** diagnostic outcome, not a dead end:
  - Keep the AC3 test — it is net-new regression coverage of the minted-session modal path (the #791 harness only covers the bootstrap session). GREEN-both-sides is acceptable **for this outcome** (the AC's "RED on main" is contingent on an in-repo bug existing; document that it did not).
  - The residual gap is real-claude-tier. Carry the deterministic proof on AC4 (§ Testing), gated/skipped per the toolchain rule.
  - Record the finding in the codebase note and **flag the operator/PO**: the in-repo minted path is verified correct; the remaining defect is claude-2.x/tui-driver-1.10.0 rendering of a minted session's permission dialog — a tui-driver (cross-repo) or toolchain follow-up, not an in-repo `cmd/pyry`/`internal/*` change. Do not manufacture an in-repo change to force AC3 red.

Do NOT spend the turn budget hunting a phantom in-repo bug if Step 1 is GREEN. The reproduction *is* the deliverable in that case.

### Step 2 — If RED: candidate in-repo fix areas (narrowed by the Core finding)

Because the emitter, scoping, broadcast, and JSONL-tail paths are proven shared/working, a real in-repo defect is confined to the minted-session *screen/detection* wiring. In descending likelihood:

1. **Modal-stream re-subscription to a minted host** (`cmd/pyry/interactive_modal_stream_v2.go` + `resolveTarget` minted branch): confirm the `Switch` fires on the minted route and the modal drain re-subscribes onto the minted `Session().Events()` — compare directly against the turn stream, which works. A divergence here (e.g. a minted-only resolve error the turn stream tolerates but the modal drain does not) is the prime suspect.
2. **`active.set(convID)` for minted routes** (`cmd/pyry/main.go:1088`, `sessionRouter.Route`): confirm the active cursor advances to the minted conv id so `boundScreenText`/`resolveTarget` follow it. Shared with the turn stream, so low probability.
3. **Minted-session screen at modal time** (`internal/sessions/session.go` hosting, tui-driver dims): confirm `Host.Session()` returns the minted session's live PTY-backed `*tuidriver.Session` whose buffer shows the modal. Low probability (buffer fed at `Spawn`).

**Fix constraint (binding, security):** whatever the fix, `modal_shown` for a minted session MUST continue to be produced by the existing `runModalStream → interactiveModalEmitterV2.handleModalShown → reg.Record(req, class, convID)` path. Do not construct or dispatch a `modal_shown` envelope by any minted-session-specific route — that would bypass #1065's `conversation_id` stamp and reintroduce the cross-conversation leak on the internet-exposed relay (§ Security).

### Concurrency model

Unchanged. The modal drain is one goroutine over one `Session.Events()` subscription at a time (`runModalStream`'s single-Run-goroutine invariant, `interactive_modal_v2.go:22` — the emitter's unguarded counters rely on it). Follow-active teardown/re-subscribe is the existing `Switch`/`subCtx` choreography in `NewTargetSubscriber`. This ticket adds no new goroutine and no new shared state. If Step 2 requires a fix, it must preserve the single-Run-goroutine invariant.

### Error handling

Unchanged. The emitter already fail-closes: a `Record` id-mint failure drops the modal (never an id-less push); `ArmModalTimeout` deny-on-timeout is armed before the broadcast so an unresolved modal is safe-denied on the window. No new failure modes introduced.

## Testing strategy

### AC3 — hermetic minted-session modal (deterministic; the diagnostic pivot)

New e2e test (fake tier, `//go:build e2e`), e.g. `internal/e2e/relay_v2_modal_perconv_test.go`. Reuse, do not reinvent:

- Daemon + modal-trigger fakeclaude via the `modalHarness` shape (`relay_v2_modal_answer_test.go`), but **do not** seed a bootstrap-bound conversation. Set `PYRY_FAKE_CLAUDE_MODAL_TRIGGER` at the daemon level (all fakeclaudes inherit it).
- One interactive phone (handshake grants the `interactive` capability that gates modal broadcasts).
- Mint a per-conversation session over the wire with `createConversationViaPhone` (returns the server-minted conv id).
- Route a turn to that conv (`send_message`) so the active cursor advances and the modal stream follows onto the minted session.
- Drop the trigger file; drain (Type-only, like `drainForControlEvent`) for `modal_shown`.

Scenario assertions (bullet-pointed; developer writes the test in-idiom):
- A `modal_shown` fans, `Class == "permission"`, non-empty `modal_id`.
- **The payload's `conversation_id` equals the minted conv id** (the #1065 scoping proof — a minted-session modal is stamped for its own conversation, not `""` and not another conv). This assertion is the security-relevant one; it also disambiguates the minted session's modal from the bootstrap fakeclaude's (which watches the same trigger file but is not the active/followed session).
- Non-vacuity: assert `modal_shown` before any resolution step, so nothing passes over a modal that never surfaced (mirror #1030's non-vacuity discipline).
- Expected disposition: RED on `main` *iff* an in-repo defect exists (Step 1). If GREEN on `main`, this stands as minted-session-modal regression coverage — record that in the codebase note.

### AC4 — real-claude per-conversation permission rung (standing gate; gated/skipped-safe)

New real-claude test (`//go:build e2e_realclaude`) under `internal/e2e/realclaude/`, wired into `make e2e-realclaude`/preship by the build tag alone (no Makefile change, mirroring #854/#997/#1030). Compose existing pieces:

- `spawnPermissionDaemon` (#1030 — the no-`--dangerously-skip-permissions` variant; the note that `spawnBootstrapDaemon` hardcodes the skip flag is already solved by this helper).
- `startPerConversationHarness` (#997 — mint a real minted per-conversation session over the wire), pairing the phone the same way #1030's `startModalResolutionHarness` does if the rung also resolves the modal (answer/cancel) — otherwise a plain interactive pairing suffices to observe `modal_shown`.
- `raiseRealPermissionModal` / `drainForControlEvent` (#1030) to force one gated Bash call on the **minted** session and assert `modal_shown{permission}`.

Toolchain rule (AC4, mandatory): real-claude PTY-modal detection may be blocked by claude-2.x vs tui-driver-1.10.0 on the current host. If the rung cannot run green, **gate/skip it explicitly** (e.g. `t.Skip` with a reason string, or a documented tag) — **do not delete coverage** — and carry the deterministic proof on AC3. Placement under `e2e_realclaude` already means it runs only in preship/with creds; a clean skip when claude/creds are absent is expected (mirror #1030's `startModalResolutionHarness` skip).

### AC2 — no reply-stream regression

The AC3 test already routes a real turn to the minted conv; assert `assistant_delta` / `turn_end` continue to fan for that turn (reuse the existing per-conversation reply-drain helpers). If Step 2 touches the modal drain only, this is regression-covered by the existing turn-stream tests; add the explicit assertion in AC3 to pin it.

## Open questions / risks

- **Is there an in-repo defect at all?** The Core finding argues the hermetic path is likely already correct and the real defect is real-claude-tier. Step 1 resolves this empirically. This is the ticket's central uncertainty and the reason diagnosis precedes any edit.
- **Cross-repo escape hatch.** If Step 1 is RED but the root cause turns out to be tui-driver classification (not `cmd/pyry`/`internal/*` wiring), that is a tui-driver (separate repo) change — out of scope here. Stop, document, and route back rather than vendoring a fix. (Not expected — but named so the developer doesn't chase it in-repo.)
- **Trigger attribution in AC3.** The daemon-wide `PYRY_FAKE_CLAUDE_MODAL_TRIGGER` makes both the bootstrap and the minted fakeclaude paint a modal. The `conversation_id == minted-conv-id` assertion is what disambiguates; if the drain proves flaky on cross-session ordering, prefer asserting on the scoped `conversation_id` rather than adding a second trigger mechanism.
- **AC4 green-ness** depends on the claude/tui-driver lock (`codebase/1030.md`). Treat a skip as success for this ticket per the toolchain rule; AC3 is the load-bearing proof.

## Acceptance criteria mapping

- **AC1** (minted session fans `modal_shown` before turn completes) → Step 1 fix if RED; already-satisfied-and-proven if GREEN. Verified by AC3 (hermetic) and, where the toolchain permits, AC4 (real claude).
- **AC2** (no `assistant_delta`/`turn_end` regression) → explicit assertion in the AC3 test + existing turn-stream coverage.
- **AC3** (hermetic modal-detection/emitter test on a bound per-conversation session; RED→GREEN) → the new fake-tier test; RED-on-main contingent on an in-repo defect (documented either way).
- **AC4** (real-claude permission rung under `e2e_realclaude`, no Makefile change; gate/skip if toolchain blocks) → the new realclaude test composing #997 + #1030.

## Scope self-check

Production source files prescribed for new/modified content: **at most 1** (the fix in `cmd/pyry/interactive_modal_stream_v2.go` or an adjacent resolver, only if Step 1 is RED). New test files: 2 (AC3 fake tier, AC4 real tier). Well under the 5-production-file gate. e2e tests double CI wall-time; that is inherent to AC3/AC4 and unavoidable (the ticket mandates both tiers).

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. The untrusted→trusted boundary here is claude's PTY screen (subprocess output) → `modal_shown` broadcast to internet-exposed relay conns. The design keeps that boundary at the single, existing, scoped emitter: `interactiveModalEmitterV2.handleModalShown` → `reg.Record(req, class, convID)` (`interactive_modal_v2.go:109`, `modalbridge/modal.go:153`). The **Fix constraint** (§ Design) forbids any minted-session-specific emission route, so no new boundary is introduced.
- **[Threat model alignment — cross-conversation confidentiality]** No MUST FIX; this is the ticket's whole reason for the `security-sensitive` label. The scoping key `conversation_id` is stamped inside `Record` from `boundScreenText`'s **single** `active.CurrentConversation()` read (`interactive_modal_stream_v2.go:100-118`, #1065) — content and scope cannot diverge. The AC3 test asserts `conversation_id == minted-conv-id`, giving a deterministic regression guard that a conversation-A modal is never stamped for (and thus never broadcast to followers of) conversation B. A GREEN-on-main outcome (§ Step 1) means the scoping already holds for minted sessions; a fix, if any, is constrained to preserve it.
- **[Concurrency]** No MUST FIX. No new goroutine or shared state. The emitter's unguarded counters remain safe under `runModalStream`'s single-Run-goroutine invariant; a Step-2 fix must preserve it (stated in § Concurrency). `Record`/`Resolve` mutate the registry under its own mutex (unchanged).
- **[Error messages, logs, telemetry]** No MUST FIX. The emitter already treats modal body (title/prompt/screenText) as application content that is NEVER logged (`interactive_modal_v2.go:28-31`); logs carry only content-free discriminants (event, class, conn_id, env_id). This ticket adds no new log site on the content path. The AC3/AC4 tests assert on wire fields (class, modal_id, conversation_id), never on claude's rendered words (substrate-guard safe, mirroring #1030).
- **[Tokens/secrets, File ops, Subprocess, Crypto, Network & I/O]** Not applicable — this ticket adds no token/credential handling, no filesystem path construction, no new `exec` argv (the fakeclaude/real-claude spawns reuse existing #791/#997/#1030 helpers unchanged), no crypto primitive, and no new socket read. `modal_id` continues to be minted via `crypto/rand` inside the unchanged `Record`.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-17
