# Spec #1177 — realclaude/stream: resume after idle eviction against live claude

**Size:** S — one new `_test.go` in `internal/e2e/realclaude/`, 0 production code, ~260 LOC.
**Security-sensitive:** no (label absent; session/turn lifecycle, not an auth surface). Security-review pass skipped per label gate.
**Split:** no — single concern (an idle-evicted stream session resumes with context intact).
**File overlap:** none. The one new file has a unique path; sibling in-flight branches `feature/1173`–`feature/1176` are each purely additive (own spec + own distinct new test file, 0 shared-file edits — verified via `git diff --name-only origin/main...origin/feature/<n>`). No `blockedBy` needed. Same-package identifier collision is handled by distinct fixed UUIDs + distinct helper names (below), not by a branch dependency.

---

## Files to read first

The developer's turn-1 data load. Read these before writing anything.

- `internal/e2e/realclaude/interactive_stream_liveness_test.go:73-142` — **`TestInteractiveStreamLiveness`**, the setup mold to transcribe: skip gates, isolated workdir, `writeStreamInteractiveConfig` (the stream-json toggle), `pair` (no `--allow-remote-permissions`), `seedBootstrapRegistry` + `seedBoundConversation`, `spawnBootstrapDaemon`, `waitBinaryHello`, `fakephone.Dial`, `driveHandshakeInteractive`, one `sealSendMessage` + drain.
- `internal/e2e/realclaude/interactive_stream_liveness_test.go:167-254` — **`drainForCompletedTurn`**: the full-turn drain (M1 non-empty `assistant_delta` → M2 terminal `turn_state{idle}`), in-order noise decrypt discipline, non-`noise_msg` skip-without-decrypt. Reuse verbatim for the plant turn; **fork** it (accumulate delta text, return the concat) for the recall turn.
- `internal/e2e/realclaude/interactive_stream_running_turn_test.go:132-214` — **`startStreamRunningTurnHarness`** (the setup-transcription pattern to mirror) + `driveRunningTurn`. Note it returns a `perConvHarness` but **does not expose the daemon handle** — your harness must, so the test can read the daemon's stderr for the eviction WARN.
- `internal/e2e/realclaude/interactive_bootstrap_liveness_test.go:369-462` — **`bootstrapDaemon`** type (the `stderr *lockedBuffer` field is your eviction-WARN source; `.stop`, `.waitForReady`) and **`spawnBootstrapDaemon`**. Line 393 hardcodes `-pyry-idle-timeout=0` — this is the single line your spawn variant parameterizes.
- `internal/e2e/realclaude/interactive_bootstrap_liveness_test.go:160-350, 481-599` — `sealSendMessage`, `driveHandshakeInteractive`, `mustJSON`, `runPyry`, `readPersistedServerID`, `seedBootstrapRegistry`, `seedBoundConversation`, `waitBinaryHello`, `lockedBuffer` (`.String()` is race-safe against the os/exec copy goroutine). All same-package — reuse, never redeclare.
- `internal/e2e/realclaude/interactive_per_conversation_liveness_test.go:85, 140` — `perTurnReplyBudget = 120 * time.Second` (reuse for both turns) and the `perConvHarness` struct fields (`phone, initSend, initRecv, home, workdir`).
- `internal/e2e/respawn_after_eviction_test.go:96-136` — the **eviction-observation** pattern to transcribe (NOT import — that file is package `e2e`): registry→`"evicted"`, then a stderr poll for the `session.idle_eviction` WARN with `wantSubstrings = {event=session.idle_eviction, session_id=, idle_timeout=, bootstrap=true}`. This is the #396 reference the ticket points at.
- `internal/sessions/session.go:516-580` — **`runActive`**: the idle timer is armed **once at session activation** (`time.NewTimer(s.idleTimeout)`, 528-532), fires `idleTimeout` later with **no per-turn reset** (the only re-arm is `attached>0`, 556), and logs the `session.idle_eviction` WARN (565) with `session_id` / `idle_timeout` / `bootstrap` fields. This is *why* the plant turn must complete before the timer fires — the binding timing constraint of the whole test.
- `internal/streamsup/runner.go:406-435, 583-605` — **`Run`** (`firstRun := true` at 408, re-armed on every `Run()` call) and **`buildArgs`** (firstRun → `--session-id <id>`, else `--resume <id>`; pure, no existence probe). This is *why* re-activation does `--session-id`(refused)→`--resume`.
- `docs/knowledge/decisions/032-bootstrap-resume-per-spawn-existence-probe.md` — the **supervisor**-path existence-probe that the **stream** path lacks; grounds why real-claude validation is the ground truth for the resume path (the ticket's premise).
- `docs/knowledge/features/idle-eviction.md` — the idle-eviction feature contract (relay/phone conns are not bridge-`attached`, so the idle timer fires; `-pyry-idle-timeout` default 0 / opt-in).

---

## Context

The interactive **stream** runner (`internal/streamsup`) has three real-claude e2e specs — `interactive_stream_liveness` (#1153, one turn), `interactive_stream_modal_resolution` (#1154, one gated turn), `interactive_stream_running_turn` (#1172, running-turn infra). **None exercises idle eviction + resume.** Idle-evict + respawn is covered end-to-end only against fakeclaude and only on the **PTY/bootstrap** runner (`TestE2E_IdleEviction_RespawnsOnSendMessage`, #396) — and, for the *stream* path, `TestE2E_PerConversation_IdleEvictsAndReactivates` (#680) proves lifecycle/routing but **explicitly defers content-recall to "realclaude's domain."**

The streamrunner plan flagged **restart after eviction** as a risk: respawn goes through `--resume`, which could fork a new on-disk id (a fresh spawn, not a true resume). Whether a real claude, evicted mid-conversation and respawned, actually reattaches to the same transcript and retains context is unproven end-to-end. This spec adds that one rung: an idle-evicted stream session resumes with continuity, proven against a live claude.

Part of #1083 (T9). Sibling leaves: #1173 (multi-turn continuity), #1174 (new-session rotation), #1175 (permission DENY), #1176 (interrupt).

### Two mechanics that drive the whole design (verified in source)

**(1) The idle timer arms at activation and never resets per-turn.** `runActive` arms `time.NewTimer(s.idleTimeout)` when the session becomes active (≈ daemon start for the warm-started bootstrap) and evicts when it fires with `attached==0`. Turn delivery runs on a *different* goroutine and never touches the timer. **Consequence:** a short idle-timeout fires *mid-plant-turn*, killing claude before the token commits. So the plant turn must reach `turn_state{idle}` **before** the timer fires ⇒ the idle window `D` must exceed the wall-clock from daemon start to plant-turn completion. This is why every existing stream realclaude test *disables* idle (`-pyry-idle-timeout=0`); #1177 is the first to enable it.

**(2) Re-activation re-arms `firstRun`, so resume is `--session-id`(refused)→`--resume`.** The `streamsup.Runner` is a *fixed instance* built once per session (`RunnerFactory`, `SessionID` pinned at construction). On idle eviction `runActive` cancels the runner's `Run(subCtx)` (it returns); on the next send the session re-Activates and `runActive` calls `s.sup.Run(subCtx)` **again**. Each `Run()` starts `firstRun := true`, so the first post-eviction spawn is `--session-id <id>` against an **existing** transcript, which claude 2.1.199 refuses → the child exits non-zero → `started==true` flips `firstRun` false → after backoff the respawn is `--resume <id>` → reattach with context. Net observable: continuity is retained via `--resume`, through one crash-recovery cycle. **This is the exact "restart after eviction" path the ticket verifies live.**

---

## Design

One new file: `internal/e2e/realclaude/interactive_stream_resume_after_eviction_test.go` (package `realclaude`, `//go:build e2e_realclaude`). Zero production changes — eviction and resume already ship; this proves they run together live on the stream toggle.

### Fixed identifiers

Single-char-repeat UUID stems are exhausted in the package (`1111`–`bbbb` merged; `cccc/dddd` reserved by #1173, `eeee/ffff` by #1174). Use **ticket-encoded** UUIDs — collision-free by construction and valid UUIDv4 (version nibble `4`, variant nibble `8`):

- `evictResumeBootstrapUUID = "11770000-0000-4000-8000-000000000001"` — the bootstrap session's pool id (`seedBootstrapRegistry`).
- `evictResumeConvID = "11770000-0000-4000-8000-000000000002"` — the driving conversation, bound to the bootstrap (`seedBoundConversation`).
- `resumeAfterEvictionIdle = "30s"` — the `-pyry-idle-timeout` value `D` (see § Timing).

The per-run recall token is **minted in the test** (not a const): a per-run-unique, unbroken, upper-case-friendly string, e.g. `fmt.Sprintf("PYRYRESUME%X", time.Now().UnixNano())`. Per-run uniqueness defeats accidental caching / coincidental matches; an unbroken token survives `strings.Contains` even if claude wraps it in punctuation.

### Test: `TestInteractiveStreamResumeAfterEviction`

Sequenced as bullets (developer writes the code in the package idiom; no full body here):

1. **Skip gates** (AC5): `exec.LookPath("claude")` skip; `WithWorktreeAuthenticated(t)` skip when creds absent. No `t.Parallel` (the helper calls `t.Setenv`). Identical to the sibling stream specs.
2. **Setup** (transcribe `TestInteractiveStreamLiveness`): isolated workdir under the authenticated HOME; `writeStreamInteractiveConfig` (stream-json toggle); `pair` **without** `--allow-remote-permissions` (no answer path needed); `seedBootstrapRegistry(evictResumeBootstrapUUID)`; `seedBoundConversation(evictResumeConvID, evictResumeBootstrapUUID, workdir)`; fakerelay; **spawn the daemon with a short idle-timeout via the new variant** (§ helper 1), keeping the returned `*bootstrapDaemon` handle; `waitBinaryHello`; dial phone; `driveHandshakeInteractive` → `(initSend, initRecv)`.
3. **Plant turn** (AC4 setup): `sealSendMessage(id=2, evictResumeConvID, "m-1", plantPrompt)` where `plantPrompt` instructs claude to remember the exact token and reply with just `ok` (token is never asserted here). Drain with the reused **`drainForCompletedTurn(..., perTurnReplyBudget)`** — this **blocks until the plant turn reaches `idle`**, which is the synchronization point that guarantees the token is committed to the transcript before eviction. (If the idle timer evicts mid-turn, this drain never sees `idle` and REDs at `perTurnReplyBudget` — a loud failure, never a false green; see § Timing.)
4. **Force + observe eviction** (AC1, AC2): after the plant drain returns, poll the daemon stderr for the `session.idle_eviction` WARN via **`waitForIdleEvictionWARN`** (§ helper 3). This is the deterministic gate that proves the bootstrap stream session was evicted **before** the resume turn — without it the continuity assertion is vacuous (a never-killed child trivially retains context).
5. **Resume turn** (AC3, AC4): `sealSendMessage(id=3, evictResumeConvID, "m-2", recallPrompt)` where `recallPrompt` asks claude for the exact token it was told to remember, reply with just that token. Drain with the new **`drainForResumedTurnText(..., perTurnReplyBudget)`** (§ helper 2) → returns the concatenated `assistant_delta` text after M1(non-empty)→M2(idle). The budget must absorb the re-activation recovery (`--session-id` refusal + backoff + `--resume` cold spawn + reply); `perTurnReplyBudget = 120s` covers it comfortably.
6. **Assert continuity** (AC4): `strings.Contains(strings.ToUpper(recallText), strings.ToUpper(token))`. A true `--resume` reloads the transcript and recalls the token; a forked fresh spawn would not → RED. `t.Fatalf` on miss, naming the token and the captured text. (M1 in the drain already enforces AC3's non-empty delta; `Contains("", token)` is false, so an empty reply also REDs.)

### New helpers (all local to the new file; distinct names to avoid same-package collision)

1. **`spawnBootstrapDaemonWithIdle(t, home, workdir, claudeBin, relayURL, idleTimeout string) *bootstrapDaemon`** — a near-copy of `spawnBootstrapDaemon` (interactive_bootstrap_liveness_test.go:383) whose sole delta is `-pyry-idle-timeout=<idleTimeout>` instead of the hardcoded `=0`. Reuses the `bootstrapDaemon` type, `shortSocketPath`, `ensurePyryBuilt`, `lockedBuffer`, `waitForReady` (all same-package). ~30 LOC. *Duplication rationale:* a signature change to the shared `spawnBootstrapDaemon` would fan out to its callers (#1153, #1172) and edit a file other in-flight siblings compose from; a self-contained variant keeps this ticket's worktree to one new file with zero shared-file merge surface — the discipline the whole realclaude family already follows.
2. **`drainForResumedTurnText(t, phone, cs, convID, timeout) string`** — a fork of `drainForCompletedTurn` (interactive_stream_liveness_test.go:181): same M1(non-empty `assistant_delta`)→M2(terminal `turn_state{idle}`) milestones and same in-order noise-decrypt discipline, but it **accumulates every matching delta's `Text`** (drop the `if sawDelta { continue }` short-circuit) and **returns the concatenation** at M2. Distinct name from #1173's `drainForCompletedTurnText` (not on `main`; a shared name would redeclare once #1173 lands). ~35 LOC.
3. **`waitForIdleEvictionWARN(t, d *bootstrapDaemon, bootstrapUUID string, timeout)`** — polls `d.stderr.String()` until it contains all of `event=session.idle_eviction`, `session_id=<bootstrapUUID>`, `bootstrap=true` (pin the keys/id, tolerate slog field reordering), else `t.Fatalf` with the captured stderr. Mirrors the #396 reference's substring poll; `containsAll` lives in package `e2e`, so inline the all-substrings check (~15 LOC).

A small harness helper (`startResumeAfterEvictionHarness`) that returns `(*perConvHarness, *bootstrapDaemon, string)` is optional sugar; inlining the setup in the test (as #1153 does) is equally acceptable. Either way the daemon handle must reach the test for the stderr read.

### Timing (`D` and the coupling constraint)

`D = 30s` (`-pyry-idle-timeout=30s`). The idle timer arms at daemon start and fires at `D`; the plant turn is sent right after the handshake and must reach `idle` before `D`. A cold haiku plant turn (`--session-id` cold spawn + model load + a one-word reply) is typically ~5–15s, so `D=30s` gives ~2× margin. The eviction WARN then fires ~`D` after daemon start; `waitForIdleEvictionWARN`'s timeout begins after the plant drain and should be ≥ `D` + eviction-processing slack (~40s) so it always covers the remaining window.

Coupling constraint (state it in a comment, analogous to the running-turn spec's `L < 120s` note): **`D` must exceed the wall-clock from daemon start to plant-turn completion.** If the plant turn REDs at its drain, the idle timer evicted mid-turn — raise `D`. This is a standing preship liveness gate, not a deterministic RED/GREEN oracle; it fails loud, never false-green.

---

## Concurrency model

No new production concurrency — this is a test. The goroutines in play (all existing machinery):

- **Daemon subprocess** — real `pyry` hosting a real claude stream child; its stderr is teed into `bootstrapDaemon.stderr` (`*lockedBuffer`, mutex-guarded so the test reads while os/exec's copy goroutine writes).
- **Session lifecycle goroutine** (`Session.Run` → `runActive`/`runEvicted`) — arms the idle timer, evicts on fire, re-activates on the resume send. The test observes it only through the `session.idle_eviction` WARN and the resumed turn.
- **`streamsup.Runner.Run`** — re-invoked on re-activation (fresh `firstRun`), driving the `--session-id`→`--resume` recovery.
- **Test goroutine** — sequential: send → drain (blocks on the noise receive stream in receive-nonce order) → poll stderr → send → drain → assert. The two drains share `initRecv`; the receive nonce stays continuous across them (each drain resumes where the last left off — same discipline as the running-turn spec threading `h.initRecv` through `drainForResponding` → `assertNoIdleWithin`).

Shutdown: `t.Cleanup(func(){ d.stop(t) })` (SIGTERM→grace→SIGKILL) and `phone.Close()` / `fr.Close()`, all reused verbatim.

---

## Error handling (fail-loud points; no false green)

- **Plant turn never completes** (idle timer evicted mid-turn, or delivery never reached the child) → `drainForCompletedTurn` REDs at `perTurnReplyBudget` with its M1/M2 diagnostic. Signals `D` is too small or a UUID-seed mismatch.
- **Eviction never observed** → `waitForIdleEvictionWARN` REDs with the captured stderr. Guards AC4 vacuity (no eviction ⇒ trivial continuity).
- **Resume turn never completes** → `drainForResumedTurnText` REDs at `perTurnReplyBudget`. This is the honest surface for the **restart-after-eviction risk**: if the stream re-activation does *not* recover from the `--session-id` refusal to `--resume` (e.g. it crash-loops, or forks a new id and the routed turn lands nowhere), the resume turn never drains → RED. That RED is a **genuine finding** (a real bug in the eviction-resume path), which is the entire point of a real-claude gate — not a spec defect. My source read says it recovers in one cycle (`started==true` → `firstRun=false` → `--resume`), so GREEN is expected.
- **Resumed but no recall** (forked fresh spawn, or empty reply) → the `Contains` assertion REDs. The per-run-unique token makes a coincidental/cached match implausible; case-fold `Contains` tolerates claude's non-deterministic formatting without weakening the discriminator.

No content/echo assertion anywhere except the planted-token recall (per the ticket): the plant turn asserts only completion; the resume turn asserts non-empty delta (AC3) + token `Contains` (AC4).

---

## Testing strategy

The file *is* the test. AC → assertion mapping:

| AC | Mechanism |
|----|-----------|
| 1 — stream runner, live claude, first turn to completion, then idle eviction | `writeStreamInteractiveConfig` + real claude + plant `drainForCompletedTurn` + `-pyry-idle-timeout=30s` |
| 2 — eviction observed **before** the resume turn | `waitForIdleEvictionWARN` gate between the two turns |
| 3 — post-eviction turn completes end-to-end | `drainForResumedTurnText` reaches M1(non-empty delta)→M2(idle) |
| 4 — continuity proven by content, resume vs fresh | per-run token planted turn-1, recalled turn-2, `Contains(ToUpper…)` |
| 5 — `e2e_realclaude` gated, skips clean (exit 0) when creds/claude absent; `needs-real-claude` operator-gated | build tag + `LookPath` / `WithWorktreeAuthenticated` skip gates |

Verification the developer runs (no live creds expected on the build box): compiles under `-tags e2e_realclaude`, `go vet -tags e2e_realclaude ./internal/e2e/realclaude/...` clean, `go build ./...` clean, and `go test -race -tags e2e_realclaude -run TestInteractiveStreamResumeAfterEviction ./internal/e2e/realclaude/` **SKIPs cleanly (exit 0)** when creds are absent. Compiling under the tag is the real check here — it catches redeclaration (distinct UUIDs + helper names), signature drift, and missing-helper references. The live GREEN runs in the operator's preship gate.

---

## Open questions

- **On-disk transcript-id structural signal (deferred).** The ticket offers, as an *optional* stronger signal, asserting the same `<bootstrapUUID>.jsonl` continues with no forked `<newID>.jsonl`. It is deferred: content-continuity is the robust primary discriminator, and the on-disk path-resolution hazard (#989: encoded-cwd canonical-case) makes a filesystem assertion fragile. Evidence-Based Fix Selection — add it only if the content signal proves insufficient in practice.
- **`D` tuning against real-claude variance.** `D=30s` is a first estimate. If the operator's preship runs show the plant turn occasionally REDs at its drain (a slow cold spawn racing the timer), raise `D`; the coupling comment tells them exactly which knob and why. The dead-time cost (waiting `~D` for the WARN after a fast plant turn) is acceptable for a preship gate.
- **Whether the stream re-activation recovers cleanly** is what this test *verifies*, not something the spec asserts a priori — see § Error handling. A RED on the resume turn is a real finding to route to a fix, not a flake.
