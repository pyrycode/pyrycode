# #1174 — Real-claude e2e: stream `new_session` rotates + spawns fresh (not `--resume`)

**Ticket:** [#1174](https://github.com/pyrycode/pyrycode/issues/1174) · size **S** · not security-sensitive · `needs-real-claude`
**Deliverable:** ONE new file — `internal/e2e/realclaude/interactive_stream_new_session_test.go` (build tag `e2e_realclaude`, package `realclaude`). **Zero production changes** — the stream `new_session` path already ships (#1124/#1125/#1137).

This is the real-claude cross of two proven tests:
- the **fakeclaude** stream sibling `TestRelayV2_StreamNewSessionRotatesAndRestartsFresh` (`internal/e2e/relay_v2_stream_new_session_test.go`, #1137) — supplies the rotation-milestone structure (registry-id rotation, `new_session` actuation loop, `session_transition{clear}` drain), and
- the real-claude **session-control** sibling `TestInteractiveSessionControlLiveness` (`internal/e2e/realclaude/interactive_session_control_liveness_test.go`, #1031) — supplies the *entire* real-claude spine and every helper this test needs, but drives `new_session` on the **default (PTY) runner** (`/clear` keystroke → claude self-rotates → fsnotify watcher rotates the registry).

This test differs from #1031 in exactly one axis: the **stream-json interactive runner** (`interactive_runner: stream-json`). On that path `new_session` is a **daemon-minted rotation** (`Pool.RotateForNewSession` mints a fresh id + `streamsup.Runner.RestartFresh` re-spawns `claude --session-id <newID>`), **no `/clear`, no watcher**. The fresh-spawn observable is therefore different, and is the heart of this spec.

---

## Files to read first

Turn-1 reading list — read these before writing a line. Most of this test is *composition of existing helpers*; the reading is about which helper to reuse, not new machinery.

- `internal/e2e/realclaude/interactive_session_control_liveness_test.go` (#1031) — **the closest sibling; read in full.** It already implements: the daemon spine (pair → seed → spawn → handshake), the `new_session` re-send actuation loop (lines 188–221), the on-disk rotation reader, and the settle-after-rotation discipline. **Reuse — do NOT redeclare** (same package): `bootstrapRow`, `readBootstrapRowIfPresent`, `readBootstrapRow`, `waitBootstrapID`, `waitBootstrapIDSettled`, `uuidStemPattern`, `ptr`, and the rotation budgets `newSessionResend`/`rotateBudget`/`idSettleQuiesce`/`idSettleTimeout`.
- `internal/e2e/realclaude/interactive_stream_liveness_test.go` (#1153) — the stream-toggle setup. **Reuse:** `writeStreamInteractiveConfig` (flips `interactive_runner:"stream-json"` before spawn) and `drainForCompletedTurn` (turn-1 drain: non-empty `assistant_delta` M1 → terminal `turn_state{idle}` M2, no content assertion).
- `internal/e2e/relay_v2_stream_new_session_test.go` (#1137) — the fakeclaude sibling; read the header (lines 22–71) for the **drain-gate divergence** (§ Design → "The load-bearing tension"). Its M1–M5 are the milestone template; M4/M5's stdin-log observable is what this real-claude test *replaces* with an on-disk transcript.
- `internal/e2e/realclaude/interactive_bootstrap_liveness_test.go` (#854) — defines `spawnBootstrapDaemon`, `seedBootstrapRegistry`, `seedBoundConversation`, `sealSendMessage`, `driveHandshakeInteractive`, `readPersistedServerID`, `waitBinaryHello`, `runPyry`, `decodePairPayload`, `mustJSON`, `relayTestLogger`. All reused verbatim.
- `internal/e2e/realclaude/interactive_per_conversation_liveness_test.go:262-330` — `sealEnvelope` (seal+send an arbitrary envelope) and `drainForReply(t, phone, cs, wantType, reqID, timeout)` (drain to a typed reply correlated on `InReplyTo`). Used for the `new_session` frame and the turn-2 ack.
- `internal/e2e/realclaude/interactive_modal_resolution_test.go:345-388` — `drainForControlEvent(t, phone, cs, wantType, timeout)` (drain to the first envelope of `wantType`; Fatals on a `TypeError`). Used for the `session_transition` (M3). Note: matches on **type only** — the caller asserts the payload fields.
- `internal/streamsup/runner.go:283-365` + `internal/streamsup/runner_test.go:662` (`TestRunner_RestartFresh_RotatesThenResumesNewID`) — **the contract that makes the on-disk observable meaningful.** `RestartFresh(newID)` re-arms first-run form so the next spawn uses `--session-id <newID>` (a **fresh transcript**), *not* `--resume`. This is what mints a brand-new `<newID>.jsonl`; a `--resume` would reuse the old file. Read to understand *why* a fresh `<newID>.jsonl` on disk proves "fresh spawn, not resume".
- `internal/sessions/reconcile.go:13-62` — `encodeWorkdir` + `DefaultClaudeSessionsDir`. Read the #989 comment: claude encodes its **resolved** cwd, and `ResolveWorkdir` (`internal/agentrun/workdir.go:33`) additionally applies `canonicalCase`, so recomputing the encoded folder name in the test is fragile. This spec **locates the transcript dir empirically** instead (§ Design → AC3 observable).
- `internal/protocol/messaging.go:47` (`SessionTransitionPayload`) + `internal/protocol/codes.go` (`TypeNewSession`, `TypeSessionTransition`, `TypeAck`, `TypeSendMessage`) — wire types.

---

## Context

Stream `new_session` rotation is proven today only against fakeclaude (#1137). That test proves routing + on-disk rotation, but the *real spawn* — a genuinely fresh live `claude` child under a new session id, not a `--resume` of turn one's session — is unproven, and a scripted fake cannot regress it (the recurring fake-green/real-red class, e.g. #949). Per the 2026-07-08 always-a-real-claude-gate policy, every operator-facing happy-path flow needs a real-claude e2e that actually runs in the pre-ship gate.

The interactive stream runner has exactly two real-claude specs today, both single-turn (`interactive_stream_liveness` #1153, `interactive_stream_modal_resolution` #1154). This adds the third rung: **`new_session` rotates the stream session id and restarts fresh against a live claude.** Sibling leaves under #1083 (T9) are #1173 (multi-turn continuity) and #1175 (permission DENY); all three are independent.

---

## Design

### Test shape — one daemon, one bound conversation, one encrypted channel

A sequential spine (the #1031/#1028 shape) so the Noise receive nonce stays in lockstep — every drain helper decrypts every `noise_msg` in arrival order:

```
setup:  pair → writeStreamInteractiveConfig → seedBootstrapRegistry(bootstrapUUID)
        → seedBoundConversation(convID → bootstrapUUID, workdir) → spawnBootstrapDaemon
        → handshake  ⇒ (initSend, initRecv)

Turn 1 (pre-rotation liveness, gate passes):
        sealSendMessage(convID, "…run=N turn=1") → drainForCompletedTurn(convID)   ── M1
        idBefore := waitBootstrapID(...)   // == bootstrapUUID (a normal turn never rotates)

new_session actuation (re-send until the registry id leaves idBefore):
        loop: sealEnvelope{TypeNewSession} every newSessionResend; poll readBootstrapRowIfPresent
        idAfter := waitBootstrapIDSettled(...)                                       ── M2 (on-disk rotation)
        assert idAfter != idBefore  &&  uuidStemPattern.MatchString(idAfter)

session_transition (client-observed rotation):
        drainForControlEvent(TypeSessionTransition) → assert {clear, prev==idBefore,
             new is-valid-stem, conv==convID}                                        ── M3

Turn 2 (post-rotation "served by the fresh child"):
        sealSendMessage(convID, "…run=N turn=2") → drainForReply(TypeAck, reqID)     ── M4 (accepted)
        // NB: do NOT drain a phone-side delta for turn 2 — it is dropped at the gate.

Fresh-spawn observable (real-claude-specific, word-independent):
        dir := <locate the .claude/projects dir holding idBefore.jsonl>   // non-vacuity + dir
        requireTranscriptAppears(dir, idAfter)                                       ── M5 (fresh, not resume)
```

### Fixed identifiers (distinct-UUID hazard)

The `realclaude` package declares seed UUIDs as package-level consts; the same-package files must not collide on a **const name** (redeclaration is a compile error), and the ticket asks for **literal values** not already bound in the package. Literals in use — main: `1,5,6,7,8,9,a,b`; in-flight #1172: `3,4`; in-flight #1173: `c,d`. Use the free `e`/`f` block:

```go
const (
	streamNewSessionBootstrapUUID = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
	streamNewSessionConvID        = "ffffffff-ffff-4fff-8fff-ffffffffffff"
)
```

Both are valid UUIDv4 stems (version nibble `4`, variant nibble `8`). Names `streamNewSession*` and the new helper names below are distinct from #1172 (`runningTurn*`) and #1173 (`streamMultiTurn*`). Before the developer commits, grep the package (`grep -rhoE '"[0-9a-f]{8}-...` and `^func`) to reconfirm no collision landed from a sibling merge.

### The load-bearing tension — why turn 2 asserts the ACK, not a phone-side delta

On the opt-in stream path the turn-drain gate forwards an event only when the producing runner's **sink tag** equals the active conversation's bound session id. The sink tag is fixed at runner **construction** (`sink.sinkFor(cfg.SessionID)` = `idBefore`), and `RestartFresh` re-spawns the child **in place** — it never rebuilds the runner/Parser/sink. So after rotation the runner's events stay tagged `idBefore` while the conversation rebinds to `idAfter`, and a turn issued **after** the rotation has its `assistant_delta` **dropped at the gate** — asserting a phone-side second-turn delta would **HANG** (documented in #1137's header ~lines 61–71, and flagged as a production follow-up in #1081; **out of scope here**).

The fakeclaude sibling worked around this by asserting the fresh child's **stdin** via a stdin-log tee — a hook real claude does not have. Therefore:
- Turn 2 asserts only its **`ack`** (`drainForReply(TypeAck, reqID)`) — the ack is produced by the `send_message` handler on delivery, **not** gated by the drain sink tag, so it reaches the phone (the fakeclaude M4 proves this). This proves the rotated session **accepted/served** the turn: `Route` resolved `convID → CurrentSessionID == idAfter → Pool.Lookup` HIT on the re-keyed pool (the #1125 routing).
- Turn 2 does **NOT** call `drainForCompletedTurn`/`drainForAssistantReply` (would hang).

### AC3 observable — a fresh `<idAfter>.jsonl` on disk (fresh spawn, not `--resume`)

The registry rotation (M2) and the `session_transition` (M3) are **not** real-claude-specific — the fakeclaude sibling regresses both. AC3 demands an observable **a scripted fakeclaude could not regress** and **robust to claude's non-deterministic wording.** The chosen observable:

> After the rotation, a fresh **`<idAfter>.jsonl`** transcript appears under the authenticated claude sessions dir, alongside the untouched turn-1 `<idBefore>.jsonl`.

Why it discriminates: `RestartFresh(idAfter)` spawns `claude --session-id idAfter` (fresh transcript — proven by `runner_test.go:662`). Real claude mints a brand-new `<idAfter>.jsonl`. A `--resume idBefore` (the failure mode AC3 guards against) would **append to `<idBefore>.jsonl` and never mint an `<idAfter>.jsonl`**. Two coexisting files ⟹ a fresh session, not a resume-into-old-file. Fakeclaude cannot regress this — it uses a stdin-log tee, not real claude transcripts.

**Locate the dir empirically, do not recompute the encoded name.** `DefaultClaudeSessionsDir(workdir)` (`EvalSymlinks` + `encodeWorkdir`) is the production reference, but the daemon's stream runner sets the child cwd to `ResolveWorkdir(workdir)` which *also* applies `canonicalCase` — a potential divergence exactly like the #989 tmpdir hazard. Instead, find the actual dir claude used by locating turn-1's transcript:

- `streamNewSessionTranscriptDir(t, home, id, timeout) (dir string)` — polls `filepath.Join(home, ".claude", "projects")/*/` for the subdir containing `<id>.jsonl`; returns that dir. On timeout, Fatal listing the `projects` tree (so a dir-mismatch is self-diagnosing). Called with `idBefore` — this **is** the non-vacuity guard (turn 1 must have written a transcript) **and** it pins the real dir.
- `requireTranscriptAppears(t, dir, id, timeout)` — polls `filepath.Join(dir, id+".jsonl")` until it exists; on timeout, Fatal listing `dir`. Called with `idAfter`.

Both are the only genuinely new helpers (~15 lines each). A short doc comment on each cites `DefaultClaudeSessionsDir` as the production analog and the #989 reason for finding-not-computing.

**The fresh no-memory / fresh-context angle is DEFERRED** (the ticket asks to resolve this): proving "the fresh child cannot see turn one's content" would require a turn-2 recall whose *phone-side reply* is inspected — but that reply is the very `assistant_delta` dropped at the gate, and real claude has no stdin-tee. So a content-continuity break is **not observable today** on the stream path. The on-disk `<idAfter>.jsonl` is the achievable, deterministic, word-independent proof. The drain-gate gap that blocks the memory angle is the known #1081 production follow-up, out of scope.

### Budgets

Reuse #1031's budgets verbatim (they are tuned for real-claude stream rotation): `newSessionResend = 1s`, `rotateBudget = 45s`, `idSettleQuiesce = 2s`, `idSettleTimeout = 45s`, `perTurnReplyBudget = 120s` (turn-1 drain and turn-2 ack). The turn-2 ack budget must be generous: the fresh child cold-spawns (spawn + model load) and the daemon's inbound queue retries delivery until it is stdin-ready — `perTurnReplyBudget` (120s) absorbs this. The fresh-transcript poll budget: reuse `rotateBudget` (45s) — the file appears once the fresh child processes turn 2 (guaranteed by the M4 ack) or on its own spawn, whichever is first.

### Double-rotation tolerance

The re-send loop may deliver a second `new_session` before the first rotation is detected (real claude, daemon-minted rotation is fast but the loop polls at 25ms and re-sends at 1s). If two rotations stack, `waitBootstrapIDSettled` returns the **final** id and M5 uses that (`idAfter` = the currently-running child, whose transcript is guaranteed after turn 2). M3 asserts `prev == idBefore` — always true for the first `session_transition` (`drainForControlEvent` returns the first of that type), so it is robust to the stacked case; it asserts `new` is a valid stem `!= idBefore` rather than tying `new` to `idAfter`. This mirrors #1031's settle discipline and #1137's "match the transition from the seeded id" note.

---

## Concurrency model

Single test goroutine; no fan-out. `fakephone.Client` buffers inbound frames, so frames the binary emits while the actuation loop is polling the registry (not reading the phone) — the `session_transition`, any trailing turn-1 `turn_end` — are still available when the M3 drain reads them. The receive nonce stays in sync because **one** `initRecv` is used for the whole test and every drain helper decrypts **every** `noise_msg` it reads, in order. `new_session` frames go out on `initSend` (independent send nonce); sending them never perturbs the receive nonce. Daemon teardown is `t.Cleanup(d.stop)` (SIGTERM → grace → SIGKILL); relay + phone likewise.

---

## Error handling

Each milestone Fatals with a cause-naming message (transcribe the shape from #1031/#1137):
- **M1 timeout** → the pre-rotation turn never drained end-to-end (delivery never reached the child, or the gate/parser/emitter dropped it — most likely a UUID mismatch between the two seeds). `drainForCompletedTurn` already carries this message.
- **M2 (`rotated == false`)** → the registry id never left `idBefore` within `rotateBudget`: the stream fresh-restart did not actuate (frame dropped on a detached session, `RestartFresh` forwarder inert, or the active cursor never stamped). Include the sessions.json contents.
- **M3 timeout** → no `session_transition` after rotation: the emitter did not broadcast the clear or the resolver could not map the new id → the conversation. `drainForControlEvent` carries a generic message; the field asserts (`prev`, `reason`, `conv`) use `t.Errorf` with got/want.
- **M4 (no ack)** → the rotated session did not accept the subsequent turn (`Route` failed to resolve `convID` → the re-keyed pool id). `drainForReply` carries this.
- **M5 timeout** → the fresh `<idAfter>.jsonl` never appeared: `RestartFresh` did not spawn a fresh child under the rotated id (or it `--resume`d the old transcript). Fatal must list the located dir's contents.

Skip-clean contract (AC4): the reused setup (`exec.LookPath("claude")` skip, `WithWorktreeAuthenticated` no-creds skip) exits 0 when claude/credentials are absent — identical to every sibling stream spec. No `t.Parallel` (`WithWorktreeAuthenticated` calls `t.Setenv`).

---

## Testing strategy

This test *is* the deliverable; "testing" it means:

1. **Compiles under the tag:** `go test -tags e2e_realclaude -run TestInteractiveStreamNewSessionRotatesAndSpawnsFresh -count=1 ./internal/e2e/realclaude/` builds. Compilation under `e2e_realclaude` is the deterministic guard against redeclared consts/helpers, wrong helper signatures, and missing symbols — the whole package must build. (A duplicate UUID *literal* is not a compile error; the pre-commit grep covers that.)
2. **`go vet -tags e2e_realclaude ./internal/e2e/realclaude/`** clean.
3. **Skips clean without creds:** the same command exits 0 (SKIP) when claude/credentials are absent. `make e2e-realclaude` returns fast-SKIP in that environment — a sub-second `ok` is a SKIP, **not** a green run. This ticket carries `needs-real-claude`: the actual green requires a live claude, run by the operator from the Inbox. Do not assert the behaviour passed off a fast-SKIP.
4. **Green against live claude** (operator, pre-ship gate): M1–M5 pass on the stream-json runner.

Acceptance-criteria trace: **AC1** = M1 + actuation + M4 (one turn, `new_session`, second turn, single seeded daemon). **AC2** = M2 (on-disk rotation to a fresh UUIDv4) + M3 (`session_transition{clear, prev, new}` client-observed). **AC3** = M4 (served) + M5 (fresh `<idAfter>.jsonl`, not `--resume`). **AC4** = build tag + skip-clean.

---

## Open questions

- **Does stream-json real claude write `<id>.jsonl` at all / where?** The transcript is claude's own session persistence, independent of the I/O format, and `--session-id <id>` pins the name — so `<idAfter>.jsonl` should appear under `~/.claude/projects/<encoded-cwd>/`. The empirical `streamNewSessionTranscriptDir(idBefore)` guard **surfaces any surprise loudly**: if turn-1's transcript is not found under `.claude/projects`, the Fatal lists the tree and the developer relocates. This is the one assumption to validate on the first live run; the design fails safe (Fatal with diagnostics) rather than passing vacuously.
- **Turn-2 ack timing on a real cold spawn.** `perTurnReplyBudget` (120s) + the inbound-queue retry should cover the fresh-spawn latency. If the ack proves slow/flaky in the live gate, the transcript-appearance (M5) alone already proves "served by the fresh child" (the file only grows because the fresh child processed the turn); the ack (M4) can be relaxed to best-effort. Prefer keeping M4 as-is unless the live gate shows otherwise.
