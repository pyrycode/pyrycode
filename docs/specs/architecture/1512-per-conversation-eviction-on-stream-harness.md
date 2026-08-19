# #1512 — Port the per-conversation eviction e2e onto a stream-mode child

**Status:** spec · **Size:** S · **Labels:** `security-sensitive`, `needs-real-claude`

Zero production behaviour changes. The diff is e2e wiring plus two stale comments.

---

## Files to read first

Read these before writing anything. Each entry names the symbol and what to extract.

| File | Symbol | What to extract |
|---|---|---|
| `internal/e2e/per_conversation_eviction_test.go` | `TestE2E_PerConversation_IdleEvictsAndReactivates`, `TestE2E_PerConversation_CapEvictsCrossDiscussion`, `startPerConvHarness`, `dialHelloPhone`, `createConversationViaPhone` | The whole file is the work surface. Note the file header comment block above the first test — it carries the stale TUI rationale. |
| `internal/e2e/relay_v2_stream_send_test.go` | `TestRelayV2_StreamSendMessageDrainsTurn` | **The reference implementation for everything new here.** The interactive handshake, the ack-then-drain shape, the M1/M2 milestone loop, the `noise_msg`-only frame filter, and the doc comment explaining why the ack is not the turn. Transcribe its drain structure; do not invent a new one. |
| `internal/e2e/harness.go` | `StartStreamInteractiveWithRelay` | The `config.json` write (the production `interactive_runner` toggle) and the stream child env set. Its doc comment states why stream mode sets **none** of `SESSIONS_DIR` / `INITIAL_UUID` / `TRIGGER` / `STDIN_LOG`. Also read `seedBootstrapRegistry` and `spawnWith` (flag last-wins semantics). |
| `internal/e2e/handshake_interactive_helpers_test.go` | `driveHandshakeToOpenDaemonInteractive` | The interactive grant the structured stream requires. Compare with the non-interactive `driveHandshakeToOpenDaemon` that `dialHelloPhone` uses today. |
| `internal/e2e/internal/fakeclaude/main.go` | `main` (the `envStreamJSON` short-circuit at the top) | Why stream mode binds no sessions dir and opens no transcript — this is what makes the idle test's `sessionsDir` / pre-created `<uuid>.jsonl` plumbing dead weight. |
| `cmd/pyry/stream_turn_drain.go` | `startStreamTurnDrainV2` | The active-session gate — forwards only when `env.sessionID == active`. This is the invariant AC#2 depends on and the seam AC#3 mutates. |
| `cmd/pyry/streamsup_runner.go` | `newStreamRunnerFactory` | `sink.sinkFor(cfg.SessionID)` — the per-runner sink tag, re-bound on **every** respawn. This is the mutation site for AC#3. |
| `internal/sessions/session.go` | `runActive` | **Load-bearing, non-obvious:** the idle timer is armed once on entering active and is reset only while `attached > 0`. Turn activity does **not** reset it. See § "The reactivation window is the idle timeout". |
| `internal/msgqueue/queue.go` | `defaultRetryInterval` | `1 * time.Second`. The other half of the same arithmetic. |
| `internal/e2e/cap_test.go` | `waitForBootstrap`, `waitForSessionState`, `assertActive` | The registry-polling helpers both tests already use; unchanged by this ticket. |
| `internal/e2e/realclaude/interactive_stream_resume_after_eviction_test.go` | `waitForIdleEvictionWARN` | Its doc comment carries the second stale `respawn_after_eviction_test.go` cite. Comment-only edit. |
| `docs/knowledge/features/idle-eviction.md` | the `internal/e2e/per_conversation_eviction_test.go` paragraph | **Read only.** It carries the third stale cite and the now-false TUI rationale. Do **not** edit it — see § "Doc split". |

---

## Context

`internal/e2e/per_conversation_eviction_test.go` starts its supervised child with `PYRY_FAKE_CLAUDE_TUI=1` while the daemon it starts runs the **stream-json** interactive runner (`selectInteractiveRunner`'s empty-string arm returns the stream factory). The defect is one-sided: the daemon speaks stream-json, the child was told to be a TUI.

The consequence is that AC#2 of #680 — "a `send_message` reactivates the evicted conversation and delivers the turn" — is certified by a `TypeAck` plus the registry flipping `evicted → active`. On the stream path the ack is issued on *accept-into-backlog*, not on delivery: `streamRunner.WriteUserTurn` returns the retryable `ErrNoLiveChild` while the child is between spawn and stdin-ready, and the msgqueue drain retries. So a regression that breaks post-reactivation delivery — a stale sink binding on the respawned runner, a drain gate that stops matching — leaves the ack and both lifecycle transitions intact and `make check` green. Only the cred-gated realclaude suite, or a user, catches it.

Stream-mode fakeclaude echoes each user turn back as an assistant text line. That is what makes a real delivery assertion cheap here: after reactivation the phone can observe an `assistant_delta` carrying the marker it just sent, followed by a terminal `turn_state{idle}` — the same two ordered milestones `TestRelayV2_StreamSendMessageDrainsTurn` already pins for the never-evicted bootstrap.

**No ADR is warranted.** This changes no design decision; it corrects a fixture and strengthens an assertion.

---

## Design

### Overview

Three seams change, all inside `internal/e2e`:

```
startPerConvHarness      TUI child         →  stream-json child + explicit daemon config toggle
dialHelloPhone           non-interactive   →  interactive grant
createConversationViaPhone  single-frame read  →  drain loop to the matching reply
```

and one new assertion block replaces the ack-only AC#2 proof in `TestE2E_PerConversation_IdleEvictsAndReactivates`.

`TestE2E_PerConversation_CapEvictsCrossDiscussion` gets no new assertions. It changes only by inheriting the stream child, the interactive conn, and the drained `createConversationViaPhone`; its AC#3/AC#4 registry pins must survive unchanged.

### 1. Extract the production config toggle — one copy, not two

`StartStreamInteractiveWithRelay` inlines the `<home>/.pyry/config.json` write of `{"interactive_runner":"stream-json"}`. `startPerConvHarness` now needs the same write.

**Extract it** into an unexported helper in `internal/e2e/harness.go`:

```go
// writeStreamInteractiveConfig writes <home>/.pyry/config.json opting the daemon
// into the stream-json interactive runner. Must land BEFORE spawn.
func writeStreamInteractiveConfig(t *testing.T, home string)
```

Behaviour-preserving move of the existing block, doc comment carried over. Two call sites: `StartStreamInteractiveWithRelay` and `startPerConvHarness`.

Do **not** inline a second copy of the JSON literal. A duplicated production-toggle string is precisely the defect class this ticket is fixing: if `interactive_runner` is ever renamed, the missed copy makes the daemon silently fall back to the default runner and the test keeps passing while asserting nothing. One copy makes that failure structural rather than a coin flip.

`harness.go` builds under `e2e || e2e_install`; the extraction is import-neutral (`os`, `path/filepath`, `testing` are already imported).

### 2. `startPerConvHarness` — stream child

Current signature:

```go
func startPerConvHarness(t *testing.T, home, sessionsDir, initialUUID, relayURL string, extraFlags ...string)
```

New signature:

```go
func startPerConvHarness(t *testing.T, home, initialUUID, relayURL string, extraFlags ...string) *Harness
```

Changes:

- **Drop the `sessionsDir` parameter** and the `os.MkdirAll` on it. Stream-mode fakeclaude short-circuits above its `mustEnv` calls, binds no sessions dir and opens no transcript. `Harness.ClaudeSessionsDir` is left unset, matching `StartStreamInteractiveWithRelay`.
- **Keep `initialUUID`.** It still feeds `seedBootstrapRegistry(t, home, initialUUID)`, which is what pins the bootstrap pool id the cap test's `waitForBootstrap` reads. This is a *daemon-side* seed and is unrelated to the child env of the same name — do not conflate them.
- **Call `writeStreamInteractiveConfig(t, home)`** before `spawnWith`. AC#1 requires the stream-runner selection to stop being implicit in the empty-string default.
- **Child env becomes exactly**: `PYRY_ALLOW_INSECURE_RELAY=1`, `PYRY_MOBILE_V2=1`, `PYRY_FAKE_CLAUDE_STREAM_JSON=1`. Everything else goes — `PYRY_FAKE_CLAUDE_TUI`, `SESSIONS_DIR`, `INITIAL_UUID`, `TRIGGER`, `STDIN_LOG`. The `tmp := t.TempDir()` that only fed `TRIGGER`/`STDIN_LOG` goes with them.
- **Add `-pyry-verbose`** to the standard flags, for the same reason `StartStreamInteractiveWithRelay` carries it: it raises the stderr handler to `slog.LevelDebug` and nothing else, which is what makes the gate's `stream_turn.not_active` drop record visible. That record is the single highest-value line when the new AC#2 assertion times out.
- **Return `*Harness`** so the idle test can attach a window of captured daemon stderr to its M1 failure. Two call sites, both in this file.

Rewrite the doc comment: it currently describes fakeclaude-TUI and names the deleted `respawn_after_eviction_test.go`. The replacement states that this is the file-local variadic-**flag** generalization of the shared stream harness — which takes env only — and why it exists (one caller needs `-pyry-idle-timeout`, the other `-pyry-active-cap`). Cite symbols, never lines.

### 3. `dialHelloPhone` — interactive grant

Swap `driveHandshakeToOpenDaemon` for `driveHandshakeToOpenDaemonInteractive`. Nothing else in the body changes. Update the doc comment: the conn is now interactive, which is the capability the structured stream requires, and it is what makes unsolicited outbound frames possible — which is why every subsequent read is a drain loop and not a single-frame read.

### 4. `createConversationViaPhone` — real drain loop

Its doc comment already claims it "drains any racing message/spinner envelopes to the `conversation_created` reply" while its body asserts the opposite ("exactly one sealed reply per request, so no spinner/message drain is needed"). The body was right for a non-interactive conn. On an interactive conn the daemon may push unsolicited structured frames, so **the comment's version becomes the true one** and the body must be made to match.

Contract: loop until the deadline, reading frames in arrival order; skip non-`noise_msg` inner frames **without decrypting** (they do not advance the receive nonce); decrypt every `noise_msg`; return the first envelope whose `Type == TypeConversationCreated` and whose `InReplyTo` matches `reqID`; ignore every other envelope type. On deadline, fail naming what was seen.

Signature and return value are unchanged. Same 15s budget (it still covers the mint + `Pool.Activate` spawn).

**Nonce lockstep is a hard contract, not a style note.** The receive `CipherState` must decrypt sealed frames in the order they arrive. A drain loop that skips a `noise_msg` frame *without* decrypting it desynchronises the nonce and every later decrypt fails. Skip on the inner-frame type only, exactly as `TestRelayV2_StreamSendMessageDrainsTurn` does.

The ack read in `TestE2E_PerConversation_IdleEvictsAndReactivates` needs the same treatment for the same reason: it is a single-frame read today and must become a loop-until-`TypeAck`.

### 5. The reactivation window is the idle timeout — raise it to 8s

**This is the non-obvious constraint that decides whether the new assertion is stable.**

`runActive` arms `time.NewTimer(s.idleTimeout)` once on entering the active state and resets it only in the `attached > 0` branch. Turn activity does not touch it. So a reactivated session has *exactly* `idleTimeout` from `Activate` to re-eviction, whatever the turn is doing.

Under the ack-only assertion that was fine — the ack fires early. Under a delivery assertion it is not, because the stream path's delivery chain is:

```
Activate (respawn)  →  WriteUserTurn → ErrNoLiveChild (child not stdin-ready)
                    →  msgqueue drain retries every defaultRetryInterval = 1s
                    →  WriteTurn → fakeclaude echo → streamsup.Parser
                    →  sink → gate → emitter → sealed push → phone
```

With `-pyry-idle-timeout=2s`, two retries exhaust the window and the child is SIGKILLed before the turn lands. The turn then sits in the backlog with no further `Activate` to drain it, and the test hangs to its deadline. That is a flaky red in the hermetic gate — the worst possible outcome for this change.

**Set the idle test to `-pyry-idle-timeout=8s`.** Arithmetic: worst case ≈ spawn (~0.3s) + 3 retry intervals (3s) + echo/parse/drain/seal (~0.5s) ≈ 4s, leaving ≥4s margin under `-race` on a loaded runner. Cost is ≈6s of suite wall time on one test — cheap against a flake.

Widen the AC#1 eviction waits accordingly (`waitForSessionState(..., "evicted", ...)` must exceed 8s; use 15s). Leave `waitForSessionState(..., "active", ...)` a short poll — it only needs to *catch* the session active, and with an 8s window it comfortably will.

The cap test is unaffected: it sets `-pyry-active-cap=2` with no idle timeout, so no timer is armed.

**Constraint to preserve, stated for future edits:** the idle test's reactivation window must stay ≥ `3 × msgqueue.defaultRetryInterval` plus spawn. If either number moves, this one moves with it.

### 6. The new AC#2 assertion

Replaces the ack-only proof. Shape mirrors `TestRelayV2_StreamSendMessageDrainsTurn`'s M1/M2 loop.

Preconditions (kept, demoted from proof to precondition):
- the `TypeAck` with `InReplyTo == reqID`, drained per § 4;
- `waitForSessionState(regPath, boundA, "active", …)` — the `evicted → active` registry transition.

Then drain for two ordered milestones, on the same serial reader:

- **M1** — an `assistant_delta` with `ConversationID == convA` **and** `Text` containing the marker sent in *that same turn*. Both halves are required; see § Security review, finding S1.
- **M2** — a terminal `turn_state{idle}` with `ConversationID == convA`, observed **after** M1. The leading `turn_state{responding}` precedes the delta; ignore `turn_state` until M1 is set.

Marker: a per-test literal unique to this turn (e.g. `e2e-1512-wake:<something>`), sent as the `send_message` `Text` and matched as a substring of the delta. The existing `e2e-680-marker:wake up` text can be reused as the send text, but the *needle* must be a distinct constant so a stray delta from an unrelated turn cannot satisfy it.

Drain deadline: 20s, matching the sibling stream spec.

Failure diagnostics must distinguish the three causes, because they point at different defects:
- **M1 timeout, no delta at all** — delivery never reached the respawned child (stale sink binding, gate mismatch), *or* the session re-evicted before delivery. Name both, and log the tail of the harness's captured stderr; the `stream_turn.not_active` Debug record discriminates them.
- **M1, delta present but wrong `ConversationID`** — a scoping failure. Fail, do not continue the loop.
- **M2 timeout** — the turn opened but never closed.

Keep AC#4's bystander check (`assertEvicted(t, regPath, boundB)`) **after** M2. With an 8s window the assertion still lands well inside convA's re-arm, and running it after the drain makes it a stronger statement: convB stayed evicted across a *completed* turn in convA, not merely across an ack.

### 7. Dead plumbing to remove

In both tests: the `claudeSessionsDir(home)` call, the `os.MkdirAll(sessionsDir, …)`, and the pre-created `<initialUUID>.jsonl` write. Stream-mode fakeclaude opens no transcript, so the file has no reader. `initialUUID` itself stays (it feeds `seedBootstrapRegistry` through the harness).

Drop now-unused imports (`os`, `path/filepath` may survive for `regPath`/`convPath` — check before removing).

### 8. Stale cites

Two comment-only edits, both required by AC#5's code half:

- `startPerConvHarness`'s doc comment — names `respawn_after_eviction_test.go`'s `startEvictionHarness`. The file was deleted. Rewrite per § 2.
- `waitForIdleEvictionWARN` in `internal/e2e/realclaude/interactive_stream_resume_after_eviction_test.go` — "Mirrors the #396 reference's substring poll (`respawn_after_eviction_test.go`)". Drop the parenthetical filename; keep the `#396` reference and the note that `containsAll` lives in package `e2e` behind a disjoint build tag. **Comment only** — no code change in that file, and it must still compile under `e2e_realclaude`.

Also rewrite the file-header comment block above `TestE2E_PerConversation_IdleEvictsAndReactivates`: it states the tests use "the v1 fakephone/fakerelay harness and fakeclaude in TUI mode", and its "What the harness can observe" paragraph justifies the ack-only proof by the shared-`INITIAL_UUID` JSONL. Both are false after this change — stream mode binds no JSONL at all, and AC#2 now proves delivery. State what is *still* true: this tier asserts lifecycle/routing scoping plus wire-observable turn delivery; per-file transcript **content recall** remains realclaude's domain (`TestInteractiveStreamResumeAfterEviction`).

### Doc split — `docs/knowledge/features/idle-eviction.md` is NOT a developer deliverable

AC#5 also names the `idle-eviction.md` paragraph. That file is `docs/knowledge/features/`, owned by the **documentation phase**, which runs on this ticket after code review. The developer's worktree may mutate only code, tests, and this spec file.

**Documentation phase — the required edit,** so it is not lost between phases. In the `internal/e2e/per_conversation_eviction_test.go` paragraph of `idle-eviction.md`:

1. Replace "Both use fakeclaude-TUI (the reactivation ack is gated on `WaitReady`+`DeliverPrompt` even on the nil-resolver path)" — false after #1512. Both now run a **stream-json** child under the production `interactive_runner` toggle, with an **interactive** phone.
2. Restate the idle test's AC#2 coverage: reactivation is certified by an `assistant_delta` scoped to the conversation carrying that turn's marker plus a terminal `turn_state{idle}`, with the ack and the `evicted → active` transition as preconditions.
3. Remove "(a generalization of `respawn_after_eviction_test.go`'s `startEvictionHarness`)" — that file is deleted.
4. Replace the "shared-env-UUID fakeclaude JSONL" sentence — stream-mode fakeclaude binds no transcript. The tier boundary is unchanged in substance (content **recall** is still realclaude's domain via `TestInteractiveStreamResumeAfterEviction`) but the reason is now "stream mode opens no transcript", not "every child shares one file".
5. Note the idle window is 8s, not 2s, and why (§ 5).

References under `docs/specs/` and `docs/knowledge/codebase/` legitimately name the file as it was at their time. Leave them.

---

## Concurrency model

No new goroutines. The phone conn is read **serially on the test goroutine** — the single-reader discipline every stream spec in this package uses. Two readers on one `fakephone.Client` would race the Noise receive nonce.

Daemon-side concurrency is untouched: the msgqueue drain goroutine per conversation, the stream drain goroutine, and the session lifecycle loop all run exactly as in production. The tests observe them through the wire and through `sessions.json`.

Ordering the tests depend on, none of it new:
- the `create_conversation` reply is sent after the handler's eager `reg.Save`, so `boundSessionID`'s registry read is safe once the reply lands;
- `lifecycle_state == "evicted"` is persisted only after the supervisor stops the child, so it faithfully witnesses "process gone";
- the drain gate sees `busy.observe` before the active-session check, and the exit envelope before the gate — neither is asserted here.

---

## Error handling

Test-side only. Failure modes and the response each demands:

| Failure | Surfaces as | Response |
|---|---|---|
| Daemon never reaches the stream runner (config toggle missed) | M1 timeout, no delta | `writeStreamInteractiveConfig` is the single source of the toggle string (§ 1) |
| Child started in the wrong mode | fakeclaude `mustEnv` fatal at spawn → `waitForReady` fails | fail fast at harness start, not 20s later |
| Session re-evicts before delivery | M1 timeout, no delta | 8s window (§ 5); M1 message names this cause explicitly |
| Gate drops the event (tag mismatch) | M1 timeout, no delta | `-pyry-verbose` surfaces `stream_turn.not_active` with `session_id`; M1 attaches the stderr tail |
| Unsolicited frame desynchronises the nonce | decrypt error mid-drain | drain loops skip on inner-frame type only, never on a decrypted envelope (§ 4) |
| Delta arrives scoped to the wrong conversation | M1 `ConversationID` mismatch | hard fail — this is the cross-bleed case, not a skip |

No production error paths change.

---

## Testing strategy

### Gate

`make check` (the hermetic gate — vet + race unit tests + staticcheck + substrate-guard + the fake-daemon e2e suite). The two tests live behind `//go:build e2e`; a bare `go test ./internal/e2e/` runs **zero** tests. Use the Makefile target's flags and report the `=== RUN` count.

`make check` does **not** compile `internal/e2e/realclaude` (build tag `e2e_realclaude`). AC#5 names compilation of `interactive_stream_resume_after_eviction_test.go` explicitly for that reason. Since the change there is comment-only, `go vet -tags e2e_realclaude ./internal/e2e/realclaude/` is sufficient evidence; the ticket carries `needs-real-claude` so the dispatcher's live tier also compiles it.

### Scenarios pinned (all pre-existing except M1/M2)

`TestE2E_PerConversation_IdleEvictsAndReactivates`, on a stream child, interactive phone, `-pyry-idle-timeout=8s`:

- two `create_conversation`s bind two distinct dedicated sessions, neither the bootstrap, neither shared *(AC#1 / AC#4 binding distinctness)*
- both bound sessions reach `lifecycle_state == "evicted"` *(AC#1)*
- a `send_message` to evicted convA is acked with `InReplyTo == reqID` *(precondition)*
- convA's bound session returns to `"active"` *(precondition)*
- **M1** — `assistant_delta`, `ConversationID == convA`, `Text` contains this turn's marker *(new — AC#2)*
- **M2** — terminal `turn_state{idle}`, `ConversationID == convA`, after M1 *(new — AC#2)*
- convB's bound session is still `"evicted"` after the completed turn *(AC#4 no cross-bleed)*

`TestE2E_PerConversation_CapEvictsCrossDiscussion`, on a stream child, interactive phone, `-pyry-active-cap=2`, unchanged assertions:

- creating A leaves active = {bootstrap, A} = cap, no eviction
- creating B cap-evicts the LRU peer = bootstrap; A and B stay active *(AC#3)*
- creating C cap-evicts the LRU peer = boundA, a **per-conversation** session — discussion C's activity evicts discussion A *(AC#3, the cross-conversation case)*
- B and C stay active; the active count never exceeds the cap at any settled checkpoint *(AC#3)*
- all four ids distinct; every `current_session_id` unchanged from capture *(AC#4)*

### Non-vacuity (AC#3)

Per-run mutation via `go test -overlay=<abs path to overlay.json>` — no worktree writes, so the mutant never reaches a commit.

**Required mutant — stale sink binding on respawn.** In `newStreamRunnerFactory` (`cmd/pyry/streamsup_runner.go`), make the sink tag first-wins instead of per-runner: capture the first `cfg.SessionID` the factory ever sees in a package-level variable and pass that to `sink.sinkFor` for every subsequent runner. This models the exact regression the ticket names — "the respawned child gets `--resume` but the sink binding is stale" — and nothing else.

Required evidence, all from the same run:

1. **Mutated tree:** the new M1 assertion in `TestE2E_PerConversation_IdleEvictsAndReactivates` is **RED**.
2. **Mutated tree:** `TestRelayV2_StreamSendMessageDrainsTurn` is **GREEN**. It drives only the never-evicted bootstrap, so first-wins and per-runner agree there. This is the evidence that the new assertion covers ground the existing stream spec does not — without it, the mutant proves only that *some* stream delivery is asserted somewhere.
3. **Unmutated tree:** both **GREEN**, same invocation.

Report the actual `--- FAIL` / `--- PASS` lines, not a summary. If the mutant does not redden M1, the assertion is vacuous and the cause must be found before shipping — do not weaken the mutant to fit.

Note whichever sibling stream specs also redden under the mutant (`relay_v2_stream_new_session_test.go` is a likely one — it also respawns). That is expected and should be reported, not hidden; only claim #2 above is load-bearing.

---

## Security review

Ticket is labelled `security-sensitive`. Pass run against this spec; verdict **PASS** with three findings folded into the design above.

**Trust boundaries.** Nothing crosses a new boundary. The phone→daemon path is unchanged Noise_IK over a loopback `fakerelay`; the daemon→child path is unchanged stdin/stdout. The only boundary *change* is the phone's capability grant, below.

**S1 — the M1 assertion must be scoped, or it masks the cross-conversation leak it exists to guard.** *(Folded into § 6.)* The cap test's cross-discussion eviction is described in this file's own comments as the security-sensitive case the PO flagged: activity in one discussion evicting another's session. The idle test's AC#4 counterpart is "churn in convA leaves convB untouched". An M1 that matched on marker text alone would be satisfied by a delta scoped to convB carrying convA's text — which *is* the cross-bleed defect. An M1 that matched on `ConversationID` alone would be satisfied by any delta on convA, including a stale one from before eviction. **Both halves are required**, and a delta with the right text on the wrong conversation must be a hard fail, never a loop-continue. This is why the marker must be minted per turn rather than reused across the two conversations.

**S2 — the interactive grant is a real capability escalation on the test conn; confirm no negative depended on its absence.** `dialHelloPhone` moves from `driveHandshakeToOpenDaemon` to `driveHandshakeToOpenDaemonInteractive`. Interactive is the capability that authorises unsolicited structured pushes to that conn — that is the whole point of the swap. The risk would be silently deleting coverage of the non-interactive negative. Checked: these two tests assert only registry lifecycle state, binding distinctness, and request/reply pairs; neither asserts that a non-interactive conn receives *no* unsolicited outbound. That negative is pinned independently by `relay_two_phone_structured_test.go`'s phone-B case and is untouched here. **No coverage is lost.** The developer should not add a capability assertion to these tests — it belongs where it already lives.

**S3 — dropped child env narrows the child's reach; nothing depended on it.** The child loses `PYRY_FAKE_CLAUDE_SESSIONS_DIR`, `INITIAL_UUID`, `TRIGGER`, and `STDIN_LOG`. Net effect is a child with strictly less filesystem reach — a small improvement, not a regression. Verify the negative anyway: no assertion in either test reads the transcript, the trigger path, or the stdin log (the trigger path was deliberately never created). The *daemon-side* `seedBootstrapRegistry(t, home, initialUUID)` is a different mechanism with the same-looking name and **must stay** — dropping it would unpin the bootstrap pool id the cap test's `waitForBootstrap` depends on.

**Secrets and log hygiene.** New failure diagnostics may print: the marker (test-authored), the observed `ConversationID` (a server-minted UUID), envelope types, and a tail of daemon stderr. Daemon stderr at `-pyry-verbose` includes the drain-gate drop records, which are content-free by construction (`SECURITY: content-free` — discriminant and session id only). Do **not** log the pair token, the server static pubkey, or any `noise.CipherState`. Do not widen `-pyry-verbose` to any production call site; it flips the stderr handler's level and nothing else.

**Insecure-transport flag.** `PYRY_ALLOW_INSECURE_RELAY=1` is retained unchanged — hermetic loopback `ws://` against a `fakerelay` in-process. No change in posture.

**Denial of service.** The 8s idle window means an evicted-then-reactivated session now holds a child ~6s longer per test. Test-fixture only; no production timeout, cap, or backlog bound moves. `defaultMaxQueuedPerConversation` is untouched.

**Non-vacuity as a security property.** The mutant in AC#3 is what stops this spec from replacing one green-but-blind assertion with another. The requirement that `TestRelayV2_StreamSendMessageDrainsTurn` stays green under the mutant is deliberate: it forces the demonstration to prove the *respawn* path specifically, which is the path the cred-gated suite is currently the only guard for.

---

## Open questions

Resolvable during implementation; none blocks a start.

1. **Does the interactive conn actually receive unsolicited frames during `create_conversation`?** Possibly not — stream fakeclaude emits nothing outside a turn, and cap eviction of an idle session should push nothing. The drain loop in § 4 is correct either way and costs nothing when the reply is the first frame. Build it regardless; do not "verify no drain is needed" and skip it, because that is exactly the reasoning that made the current comment/body disagreement.
2. **Is 3 retry intervals the right worst case?** 8s was chosen with ≥4s margin over a ~4s worst case. If the suite proves flaky at 8s the answer is to raise it and record the observed latency, not to loosen M1's deadline — M1's deadline does not extend the child's life.
3. **`-pyry-verbose` chattiness.** It lands Debug records in the 200-entry `pyry logs` ring, shortening history in these two tests. Neither reads that ring. Same trade `StartStreamInteractiveWithRelay` already made.
4. **Whether `createConversationViaPhone`'s 15s budget still fits** now that the child is a Go binary spawn rather than a PTY-hosted TUI. It should be more generous than before, not less; leave it.
