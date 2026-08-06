# Spec #1240 — does the interactive surface report a turn idle while a backgrounded command is still alive?

**Ticket:** [#1240](https://github.com/pyrycode/pyrycode/issues/1240) — split from #1227. Blocks #1241.
**Labels:** `size:s`, `security-sensitive`, `needs-real-claude`.
**Shape:** one new test file under `internal/e2e/realclaude`. **Zero production change.** Zero consumer call sites.

---

## Files to read first

Turn-1 reading list. Every entry is load-bearing; nothing below re-derives what these already establish.

| Path | Extract |
|---|---|
| `internal/e2e/realclaude/interactive_stream_running_turn_test.go:132-199` | `startStreamRunningTurnHarness` — the harness this probe reuses verbatim. Note it seeds `runningTurnBootstrapUUID`/`runningTurnConvID` internally and returns `(*perConvHarness, convID)`. |
| `internal/e2e/realclaude/interactive_stream_running_turn_test.go:242-355` | `drainForResponding` / `assertNoIdleWithin` — transcribe the **decrypt discipline** from these (every `noise_msg` decrypted in receive order; non-`noise_msg` skipped *without* decrypting). Both **discard** frames; neither is reusable as a recorder. |
| `internal/e2e/realclaude/interactive_stream_running_turn_test.go:225-230` | `runningTurnPrompt` — read it to see what NOT to do. It steers claude *away* from backgrounding. Do not reuse, do not edit. |
| `internal/e2e/realclaude/fifo_reader_liveness_test.go:83-179` | `fifoLiveRead`, the three verdict consts, and `fifoLiveOutcome` (already JSON-tagged). Record the struct; do not re-render it. |
| `internal/e2e/realclaude/fifo_reader_liveness_test.go:39-59` | Two hazards in the header: the dual failure mode (an instrument that can only answer `reader-present`), and why `Kill()` is not the sync point. |
| `internal/e2e/realclaude/background_trigger_probe_test.go:647-716` | `holdProbeFIFO` — the rendezvous channel, the write-end-never-escapes rule, and its `t.Cleanup` shape. |
| `internal/e2e/realclaude/background_trigger_probe_test.go:355-440` | The probe-file idiom this file mirrors: env gate + loud `Skipf`, a non-`t.TempDir` artifact dir, and the record-write cleanup registered FIRST so LIFO runs it LAST. |
| `internal/e2e/realclaude/background_trigger_probe_test.go:603-614` | `probeClaudeVersion` — reuse for AC4's version line. |
| `internal/e2e/realclaude/interactive_bootstrap_liveness_test.go:158-180` | `sealSendMessage` — the send. |
| `internal/e2e/realclaude/interactive_bootstrap_liveness_test.go:383-420` | `spawnBootstrapDaemon` — `cmd.Env = append(os.Environ(), …)` at `:403` is why the `t.Setenv` must precede the harness call. |
| `internal/e2e/realclaude/interactive_per_conversation_liveness_test.go:85,140-146` | `perTurnReplyBudget` (120s) and `perConvHarness` (`phone`/`initSend`/`initRecv`/`home`/`workdir` — **no daemon or socket field; do not add one**). |
| `internal/e2e/realclaude/fixtures.go:25-34` | `realHome` — the operator's HOME captured at package load, before any `t.Setenv`. This is the redaction key AC4 needs. |
| `internal/protocol/interactive.go:16-77` | `TurnStatePayload` / `ToolUsePayload` / `ToolResultPayload` / `TurnEndPayload` — the exact field set the record transcribes. |
| `internal/protocol/codes.go:181-186` | `TypeTurnState`, `TypeAssistantDelta`, `TypeToolUse`, `TypeToolResult`, `TypeTurnEnd`. `protocol.TypeNoiseMsg` is `v2envelope.go:19`. |
| `internal/turnbridge/outbound.go:44-47,84,147-194` | `maxSummaryLen = 200`; `is_error` is `Status == ToolStatusFailed` (so the background path's `false` is the same `false` a clean success carries); `inputSummary` compacts + truncates; `truncate` appends `…` past 200 runes → a truncated summary is **exactly 201 runes**. |
| `cmd/pyry/interactive_turn_v2.go:208-217, 267-285, 291-298` | The `TurnEnd` arm, `startTurnIfNeeded`, `transitionTo` — the source of AC3(b)'s structural argument (see § AC3(b) below). |
| `cmd/pyry/main.go:794-801` | `writeMCPApproveConfig` is called iff `InteractiveRunner == "stream-json"`, and `defer`-removed. Read it to understand *why* AC4 lands on its second arm here (§ AC4). |
| `cmd/pyry/mcp_config.go:72-127` | `renderMCPApproveConfig` embeds `["mcp-approve", "-pyry-socket", <socketPath>]`; the file is created via `os.CreateTemp("", …)` — a **shared `$TMPDIR`**. |

---

## Context

`cmd/pyry/interactive_turn_v2.go:208-217` emits `turn_end` and then transitions to `idle` in the `turnevent.TurnEnd` arm, unconditionally — nothing consults whether the tool left work behind. Since claude now returns a `tool_result` the moment a Bash command's timeout expires and leaves the command running in the background (claude 2.1.220, observed 2026-07-27), the code *predicts* that a client is told the turn finished while the work it described is still running.

A prediction about code is not a measurement of the wire. `TestInteractiveStreamRunningTurn` already treats early `idle` as a **test hazard** and steers around it (`runningTurnPrompt`, "do NOT run it in the background"); whether the same early `idle` is a defect *for real clients* has never been settled, and the surrounding frames — what `tool_result` carried, what `stop_reason` said, whether the command was alive rather than killed — have never been recorded.

This ticket is a probe. Its output is evidence: one staged turn on the production stream-json interactive daemon, every frame recorded in receive order, and two liveness reads that make the "still alive" claim falsifiable.

**Not in scope.** Whether a client can *distinguish* the two cases (the baseline diff) is #1241, which is blocked by this one and reuses this recorder. Teardown reaping is #1230; the backgrounded command's output file is #1226; `agent-run` budget accounting is #1234. No production change belongs in this ticket.

---

## Design

### One new file

`internal/e2e/realclaude/interactive_background_idle_probe_test.go` — `//go:build e2e_realclaude`, `package realclaude`.

**Every new symbol takes the `bgIdle` prefix.** Verified free at spec time across `origin/main` and every sibling branch (`origin/feature/{1174,1219,1224,1227,1230,1233}`) via `git grep -o -E '\b(bgIdle|idleProbe|turnRec|recProbe)[A-Za-z0-9_]*' <branch> -- internal/e2e/realclaude`. #1226/#1230/#1234/#1235 and this family's siblings all add files to this package concurrently; a branch-overlap check does not catch a same-package identifier collision, so re-run that grep before pushing if other branches have landed meanwhile.

**No new bootstrap/conversation UUID literals.** The ticket's naming note asks for a distinct seeded pair, but `startStreamRunningTurnHarness` hard-codes its own (`:175-176`) and "a second interactive harness or daemon spawner" is on the must-NOT-build list — parameterising the shared helper would edit a file `TestInteractiveStreamRunningTurn` and #1176 both depend on. Reusing the harness's pair is safe and is the established precedent (spec #1176 § "No new package-level constants" made the same call): each test owns its own authenticated tempdir HOME, so the literals never collide on disk, and this probe is env-gated so it and the running-turn smoke rarely run in the same invocation. Take the `convID` the harness returns; introduce none.

### Gate

`bgIdleEnableEnv = "PYRY_PROBE_INTERACTIVE_BG_IDLE"`. Skip unless `== "1"`, with a `Skipf` that (a) says a skip here is the normal `make e2e-realclaude` outcome and carries no signal about pyry, and (b) prints the exact command:

```
PYRY_PROBE_INTERACTIVE_BG_IDLE=1 go test -tags e2e_realclaude -timeout 10m -v \
  -run '^TestInteractiveStreamBackgroundIdleProbe$' ./internal/e2e/realclaude/
```

Gated for the same reason #1223's probe is: `make e2e-realclaude` runs the whole package glob, and this probe asserts nothing about pyry, so an ungated live turn buys preship cost and no signal. The `needs-real-claude` label means the operator runs it explicitly; the `Skipf` body is that operator's instruction sheet.

### Sequence

Two ordering constraints are load-bearing and both are cheap to get wrong.

| # | Step | Why here and nowhere else |
|---|---|---|
| 1 | Env gate → `Skipf` | Before anything is created. |
| 2 | `artifactDir, _ := os.MkdirTemp("", "pyry-1240-probe-*")`; `t.Logf` it | Deliberately **not** `t.TempDir()`: the operator needs these files after the run to compose AC4's comment. |
| 3 | `t.Setenv("BASH_DEFAULT_TIMEOUT_MS", "5000")` | **Must precede step 4.** `spawnBootstrapDaemon` snapshots `os.Environ()` at `interactive_bootstrap_liveness_test.go:403`; a later `Setenv` never reaches the daemon, and the daemon passes its environment to claude verbatim (`internal/streamsup/runner.go:555`). |
| 4 | `h, convID := startStreamRunningTurnHarness(t)` | Skips cleanly without claude / creds. Spawns the daemon under `--dangerously-skip-permissions` so no modal blocks the Bash call. |
| 5 | Build `rec`; `t.Cleanup(write rec)` | Registered **first** in the body so LIFO runs it **last** — a structural `t.Fatalf` below still leaves partial evidence on disk. Initialise `rec.Outcome` to `bgIdleUnresolved` with detail "did not reach a classification point". |
| 6 | `fifoPath := filepath.Join(h.workdir, bgIdleFIFOName)`; `rendezvous := holdProbeFIFO(t, fifoPath)` | **Must follow step 4** (needs `h.workdir`), which also yields the right LIFO: FIFO released → record written → phone closed → daemon stopped. The turn gets a real chance to end on its own. |
| 7 | **Pre-rendezvous read:** `rec.PreRendezvousRead = fifoLiveRead(fifoPath)` | § AC2 below. Must be after step 6 (the path must exist, else the read fails on the `Lstat` arm) and before step 8 (no `cat` can exist yet). |
| 8 | Stamp the rendezvous asynchronously | `rendezvousAt := make(chan time.Time, 1)`; one goroutine: `<-rendezvous; rendezvousAt <- time.Now()`. A buffered channel, not a shared field — the record is read on the test goroutine and a field write would race under `-race`. |
| 9 | `sentAt := time.Now()`; `sealSendMessage(t, h.phone, h.initSend, 2, convID, "m-1", bgIdlePrompt(fifoPath, nonce))` | Send id 2 = first post-handshake message, matching every sibling. |
| 10 | `frames, terminatedOn := bgIdleRecordTurn(…, atIdle)` | § Recorder. `atIdle` closes over `rec` and takes the second `fifoLiveRead`. |
| 11 | Non-blocking receive on `rendezvousAt` → `rec.RendezvousAt` (nil if it never fired) | `select { case ts := <-rendezvousAt: … default: }`. |
| 12 | Classify → `rec.Outcome`, `rec.OutcomeDetail`; `t.Logf` a one-line summary | § Verdict. |

Nothing in steps 7–12 calls `t.Fatalf`. The only fatal paths are structural (inside the harness, or a decrypt/decode failure in the recorder — see below).

### The prompt

```go
func bgIdlePrompt(fifoPath string, nonce int64) string
```

One line, no system prompt available on this surface (the daemon spawns claude with `--model haiku --dangerously-skip-permissions`), so #1223's steering rides inline: *use the Bash tool exactly once, run this command verbatim: `cat <fifoPath>`, do not chain with `&&` or `;`, do not comment, do nothing else*, plus `run=<nonce>` to defeat caching.

The only two values interpolated are `fifoPath` — `filepath.Join(h.workdir, bgIdleFIFOName)`, a `t.TempDir()`-derived absolute path plus a fixed const basename, carrying no shell metacharacters — and `nonce`, `time.Now().UnixNano()`. **`nonce` is a cache-buster, not security randomness**: it must stay wall-clock and must not be "upgraded" to `crypto/rand`, nor read as though it were a token.

**It says nothing about timeouts or backgrounding.** That is the measured axis; #1223's system prompt was deliberately silent on it for the same reason. "Exactly once, verbatim, nothing else" is setup, not measurement.

The trigger is #1223's established lever and is not re-derived here: `BASH_DEFAULT_TIMEOUT_MS=5000` on the process that spawns claude, 7 firing runs out of 7 against claude 2.1.220. `BASH_MAX_TIMEOUT_MS` alone does not fire and is not set. `cat <fifo>` is not `sleep`-leading, so it is never auto-backgrounded for the *other* reason.

### The recorder — the genuinely new code

```go
func bgIdleRecordTurn(t *testing.T, phone *fakephone.Client, cs *noise.CipherState,
    convID string, budget time.Duration, atIdle func()) (frames []bgIdleFrame, terminatedOn string)
```

Contract:

- Reads binary→phone frames until one of exactly **two** terminal conditions: a `turn_state{idle}` whose `conversation_id == convID` (`terminatedOn = bgIdleTerminatedIdle`), or `budget` elapses (`terminatedOn = bgIdleTerminatedBudget`). The budget arm is not optional — a drain that can only end on `idle` cannot record the did-not-fire outcome AC4 sanctions. Use `perTurnReplyBudget` (120s).
- **Decrypt discipline, transcribed from `drainForResponding:242-290`.** Every `noise_msg` is decrypted in receive order (the receive nonce is sequential); a non-`noise_msg` inner frame is skipped **without** decrypting so it does not advance the nonce. Getting this wrong fails as a decrypt error mid-record, not as a missing frame.
- Every decrypted envelope is appended as one `bgIdleFrame`, whatever its type — `ack`, `stall`, `api_retry`, `unrecognized_message` included. AC1 says every frame. **An envelope type outside the five AC1 names is recorded by `Type` and `conversation_id` only; its payload is never unmarshalled and never stored** (§ Security review, S2).
- A `fakephone.ErrReceiveTimeout` re-loops into the budget check (a blocked `cat` produces no frames, and that is a legitimate path to the budget arm).
- On the terminal `idle`: append the frame **first**, then call `atIdle()` synchronously, then return. Both instants are recorded, so "at the instant `idle` was recorded" is a fact in the artefact, not a claim in prose.
- `atIdle` may be nil (the seam #1241 reuses with a different hook, or none).
- **Decrypt / JSON-decode failure is `t.Fatalf`**, matching every existing drain in the package. A nonce desync makes the whole record untrustworthy — it is a broken instrument, never a datum about claude. The record-write cleanup from step 5 still fires, so the partial evidence survives and `rec.Outcome` stays at its "did not reach a classification point" initial value. **Transcribe `drainForResponding:272`'s message shape verbatim — the error only.** Do not add the raw or sealed frame bytes to the message "to help debug it"; that puts ciphertext and payload into CI logs.

### The record

Two structs, one JSON file, no sibling verbatim captures (the wire values are already summaries; #1223 needed sibling files because it captured raw JSONL and `ps` output — this ticket captures neither).

```go
type bgIdleFrame struct {
    Index        int    // position in the record
    At           string // RFC3339Nano receive instant
    SinceSendMS  int64
    Type         string
    ConversationID, TurnID string
    State        string // turn_state
    ToolUseID, Name, InputSummary string // tool_use
    IsError      *bool  // tool_result — pointer, see below
    ResultSummary string // tool_result
    StopReason   string // turn_end
    TextLen      int    // assistant_delta — LENGTH ONLY, never the text
}
```

- **`IsError` is `*bool`, not `bool`.** `is_error: false` is the load-bearing value here — `outbound.go:84` sets it from `Status == ToolStatusFailed`, so the background path carries the *same* `false` a clean success carries. `omitempty` on a plain `bool` would erase exactly the field AC3(a) needs. Use a pointer and set it only on `tool_result`.
- **`assistant_delta.text` is never recorded.** `interactive_turn_v2.go:70-75` states the package rule: application output never reaches a log. This record gets pasted into a public issue. Record the length; nothing else.
- `bgIdleRecord` carries: ticket, claude version (`probeClaudeVersion`), runner path + its attribution status (§ AC4), model (`haiku`), env delta, prompt, workdir, FIFO path, `SentAt`, `RendezvousAt *string`, `PreRendezvousRead`/`IdleLiveRead` as `fifoLiveOutcome` values (`*fifoLiveOutcome` — a read not taken is nil, never a zero-value verdict), `IdleReadAt`, `TerminatedOn`, `Frames`, `FramesDropped`, `FIFONamingMatches`/`FIFONamingTruncatedCandidates`, `StructuralArgument`, `Outcome`, `OutcomeDetail`.
- **`EnvDelta` is a fixed literal — `[]string{"BASH_DEFAULT_TIMEOUT_MS=5000"}` — never harvested.** Nothing in this file may call `os.Environ()`, read any child process's environment, or call `os.Getenv` for anything but `bgIdleEnableEnv`. The test process and the daemon both carry `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` (`fixtures.go:98-99`, `t.Setenv` by `WithWorktreeAuthenticated`), and this record is pasted into a **public** issue. This is the same prohibition #1230 applied to `ps -E`, restated because the natural implementation of a field called "env delta" is a diff of `os.Environ()`. See § Security review M1.
- **Frames are capped at `bgIdleMaxFrames` (2000), and a cap is never silent.** Past the cap the recorder stops appending, counts into `FramesDropped`, and keeps draining (the terminal-`idle` detection and `atIdle` must still fire). A truncated record that reads as "that is all the phone received" would misstate AC1.
- **Redaction is one place: the writer.** `bgIdleWriteRecord` marshals with `json.MarshalIndent`, then replaces `realHome` (`fixtures.go:34`) with `$HOME` in the marshalled bytes before `os.WriteFile(…, 0o600)`, and `t.Logf`s the same redacted bytes. One choke point catches every field, and the attribution matching in § AC3(c) runs on the raw in-memory values so redaction cannot perturb it. **Guard `realHome == ""` explicitly** — `strings.ReplaceAll(s, "", "$HOME")` inserts `$HOME` between every character of the record. Route the step-2 `t.Logf` of `artifactDir` through the same helper so no path reaches the log around the choke point.
- **One extraction site, an allowlist, not a habit.** All payload unmarshalling happens in a single `bgIdleFrameFromEnvelope(env protocol.Envelope) bgIdleFrame`, whose switch has exactly five arms (`turn_state`, `tool_use`, `tool_result`, `turn_end`, `assistant_delta`) plus a default that fills `Type` and stops. A field that is not extracted there can never reach the artefact — which is the property that keeps `unrecognized_message.Raw` (claude's verbatim offending message) and `assistant_delta.text` out of a public issue by construction rather than by care.

---

## Answering each acceptance criterion

### AC1 — staged and recorded

Steps 3–10 above. The record's `TerminatedOn` states which of the two conditions ended the drain, and each frame carries `At` + `SinceSendMS` so the receive order is legible without trusting the array index alone.

### AC2 — the flip, observed in this rig

`fifoLiveRead` proved it flips in **its own self-check** (#1239, `TestFIFOReaderLiveness_FlipsAcrossOneReaderLifetime`). That proof does not transfer to this rig: a read wired to the wrong path, or to a FIFO something else holds open, would produce the same lone `reader-present`. So the flip is re-established **here, on this path, across this command's lifetime**:

- **Pre-rendezvous (step 7):** `holdProbeFIFO` has created the FIFO and its goroutine is parked in the blocking `O_WRONLY` open; no reader has ever opened it. The read must return `no-reader` (ENXIO, mode `prw-------`). Measured during refinement — three consecutive pre-rendezvous reads returned `no-reader`, and the parked writer was **not** perturbed: a failed ENXIO open creates no fd, so there is nothing to close and the rendezvous does not fire early. Do not re-derive this.
- **At `idle` (step 10's `atIdle`):** the read must return `reader-present` if the backgrounded command is alive.

Two reads, one path, one lifetime — which two separately-constructed FIFOs could not establish. Both are recorded whole (`Verdict`, `Detail`, `Path`, `Mode`, `Errno`, `ErrnoName`), not summarised to a boolean. If either returns `instrument-failed`, the record says which arm fired and the verdict is `bgIdleUnresolved`: a broken instrument is never recorded as a dead command.

**Do not release the FIFO before both reads.** `fifoLiveRead` opens a *transient second* write end and closes it immediately; POSIX delivers EOF to a FIFO reader only when the **last** writer closes, so while `holdProbeFIFO`'s end is held the reader never notices. Take a read after that hold is released and the read's own `Close` becomes the last writer closing — it would kill the very command it is measuring. The step-5/step-6 registration order already gives the right LIFO; this is why it matters.

Nothing in this ticket kills the command, so #1239's kill-versus-reap hazard is a thing to avoid, not a step to perform.

### AC3(a) — the turn did not end for some other reason

The classifier joins the Bash `tool_use` to its `tool_result` **by `tool_use_id`**, never by prose (#563 and #1219 each paid once for prose matching). The matched `tool_result`'s `is_error` and the `turn_end`'s `stop_reason` are recorded verbatim. `CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=1` is #1223's known negative control (same expiry, `Exit code 143`, `is_error: true`, command dead) — not run here, but it is what an `is_error: true` reading would look like.

### AC3(b) — the idle belongs to this turn

Stated as a structural argument, transcribed into both the file header and the record's `StructuralArgument` field:

> `turn_state{idle}` reaches the wire from exactly one site: `interactive_turn_v2.go:216`, inside the `turnevent.TurnEnd` arm, which is entered only when `inTurn` is true (`:209`) and which immediately calls `endTurn()`. `inTurn` is set only by `startTurnIfNeeded` (`:267`), which every content arm calls together with a `transitionTo(thinking|responding)` — so no `idle` can reach the wire without a preceding non-idle `turn_state` for the same conversation, in the same record. One turn is driven, into one conversation seeded fresh in a per-test authenticated HOME, over one phone that is the only interactive conn. Every frame carries `conversation_id` verbatim and the record is ordered by receive time, so this is refuted by the record itself rather than assumed.

The classifier enforces the observable half: if the terminal `idle` is **not** preceded in the record by a Bash `tool_use` naming the FIFO, the outcome is `bgIdleUnresolved`, not the headline.

### AC3(c) — the FIFO holder was the command that was backgrounded

`fifoLiveRead` reports on the *pipe*, not on a pid, so it cannot by itself attribute the held read end to the joined `tool_use_id`. A second `cat` on the same path reads identically. The gap is closed by **counting frames already recorded**, not by a third pid matcher (#1230 has `probeDescendantsFromPS`, #1235 is building another; a third is on the must-NOT-build list).

```go
func bgIdleCountFIFONaming(frames []bgIdleFrame, fifoPath string) (matched, truncatedCandidates int)
```

- `matched`: `Type == protocol.TypeToolUse && strings.Contains(InputSummary, fifoPath)`.
- `truncatedCandidates`: `Type == protocol.TypeToolUse && !Contains && utf8.RuneCountInString(InputSummary) == 201 && strings.HasSuffix(InputSummary, "…")`. Both wire summaries are compacted and truncated at `maxSummaryLen` = 200 runes (`outbound.go:47,189-194`), and `truncate` appends `…` past the cap — so a truncated summary is **exactly 201 runes** and a truncated `input_summary` that cut off the FIFO path is an **unresolved** match, not a non-match.

Attribution is sound iff `matched == 1 && truncatedCandidates == 0`. Anything else is recorded as **unresolved attribution** and is never asserted.

**Do not gate the match on `Name == "Bash"`.** `ToolUsePayload.Name` comes from `turnevent.ToolStart.Title` (`outbound.go:74`), a value this design has not measured; gating on it would add a second, unvetted matcher whose failure is silent. The FIFO path in the summary is the discriminator; `Name` is recorded verbatim so a non-Bash tool naming the path is visible to the reader.

**Why a model-authored string cannot manufacture the headline.** `input_summary` is written by the model, so a decoy `tool_use` that merely *mentions* the path (`echo <path>`) inflates `matched`. Both readings of that are safe, and the safety comes from pairing the count with AC2's reads rather than from trusting the string:

- decoy **and** the real `cat` → `matched == 2` → `bgIdleUnresolved`.
- decoy **only**, no real `cat` → `matched == 1`, but nothing holds the read end, so the idle-instant read returns `no-reader` → `bgIdleCommandNotAlive`.

`bgIdleEarlyIdle` requires `matched == 1` **and** a `reader-present` read on that same path. No single model-authored string satisfies both. This is why the frame count is a *complement* to the liveness pair and not a substitute for it.

### AC4 — the finding, and the runner path

The record supplies the operator's comment: claude version, runner path, both liveness verdicts with `Detail`, whether the lever fired, and — since `TestInteractiveStreamRunningTurn:225-230` steers claude away from backgrounding with `runningTurnPrompt` — whether that prompt-steering is now the only thing keeping that smoke from an early `idle`. Absolute paths under the operator's home are already redacted by the writer.

**Runner path: take AC4's second arm and say so plainly.** Record it as, in substance:

> `interactive_runner: "stream-json"` is a **rig-authored** value — `writeStreamInteractiveConfig` (`interactive_stream_liveness_test.go:155`) wrote it into `<home>/.pyry/config.json`. No non-authored corroboration is recorded.

That is a deliberate design decision, not an omission. The one non-authored artefact — the MCP-approve config that `main.go:795-801` writes iff `InteractiveRunner == "stream-json"` — is created by `os.CreateTemp("", "pyry-mcp-approve-*.json")` in a **shared `$TMPDIR`** where any other stream-json pyry on the operator's machine also has one. Its only discriminator is the embedded `["mcp-approve", "-pyry-socket", <socketPath>]`, and this daemon's socket comes from `shortSocketPath`'s per-test `MkdirTemp` — a value `perConvHarness` does not carry and which the ticket forbids adding.

An attribution could be reconstructed by set-differencing `/tmp/pyry-sock-*` across the harness call. **Do not build it.** It is ~45 lines whose own failure mode is precisely the failure AC4 exists to prevent: a concurrent pyry creating a socket dir inside that window silently makes the difference set wrong, and the same shared-namespace trap already cost this family a finding. An instrument that can misattribute is worse than an honestly-stated absence.

Do **not** reach for the fallback "the daemon started at all, and `selectInteractiveRunner` fails fast on an unrecognised value (`main.go:675`, no silent PTY fallback)". That proves the value *parsed*; both `"pty"` and `"stream-json"` start fine, so it corroborates nothing about which runner was selected.

---

## Verdict taxonomy

Five outcomes, evaluated in this order. The first match wins.

| Outcome | Condition | Meaning |
|---|---|---|
| `bgIdleUnresolved` | either liveness read is `instrument-failed`; **or** `matched != 1`; **or** `truncatedCandidates > 0`; **or** the terminal `idle` is not preceded by a FIFO-naming `tool_use` | No conclusion drawn. The record names which clause fired. |
| `bgIdleDidNotFire` | no FIFO-naming `tool_use`, **or** one with no `tool_result` sharing its `tool_use_id`, and no terminal `idle` | The background lever did not fire. A legitimate result. |
| `bgIdleEarlyIdle` | terminal `idle` recorded, matched `tool_result` present, idle-instant read `reader-present` | **The headline finding**: `idle` was emitted while the backgrounded command was still alive. |
| `bgIdleCommandNotAlive` | terminal `idle` recorded, matched `tool_result` present, idle-instant read `no-reader` | The command was not alive at `idle`. The headline claim is false for this run. |
| `bgIdleNoEarlyIdle` | matched `tool_result` present, budget expired with no terminal `idle` | The surface did **not** report `idle` while the command ran. |

`OutcomeDetail` always names the clause that fired and quotes the joined `tool_use_id`, the matched `tool_result`'s `is_error`, and `turn_end.stop_reason` where present.

---

## Concurrency model

Three goroutines, no shared mutable state between them.

1. **The test goroutine** owns `rec` end to end, and is the only writer.
2. **`holdProbeFIFO`'s hold goroutine** (existing) parks in the blocking `O_WRONLY` open and closes the write end on release. Untouched.
3. **The rendezvous stamper** (new, ~3 lines): `<-rendezvous; rendezvousAt <- time.Now()`. It writes to a buffered channel, never to `rec` — a direct field write would be a `-race` failure. The test goroutine drains it with a `default` arm, so a rendezvous that never fired is recorded as nil rather than deadlocking the run.

   **Its exit path, both ways.** If the rendezvous fires, the stamper sends and returns. If it never fires, the stamper stays parked on `<-rendezvous` until `holdProbeFIFO`'s cleanup opens the read end non-blockingly (`background_trigger_probe_test.go:700`) to release its own parked writer — which closes `rendezvous` and lets the stamper proceed. `rendezvous` is **closed**, not sent on (`:684`, and see its doc at `:657-659`), so a second receiver is safe. The send then lands in the **buffered(1)** channel with no reader left and the goroutine exits. Buffer size 1 is therefore load-bearing twice: it prevents the race *and* it prevents a goroutine leak past test end.

Shutdown is `t.Cleanup` LIFO and is load-bearing: **FIFO released → record written → phone closed → daemon stopped.** The FIFO release is what lets `cat` see EOF and exit, so the turn ends on its own before the daemon is signalled.

---

## Error handling

| Failure | Handling |
|---|---|
| claude / credentials absent | `startStreamRunningTurnHarness` skips cleanly. |
| Decrypt or JSON-decode failure in the recorder | `t.Fatalf` (nonce desync ⇒ the record is untrustworthy). The record-write cleanup still fires; `Outcome` stays "did not reach a classification point". |
| `fakephone.ErrReceiveTimeout` | Re-loop into the budget check. Not an error — a blocked `cat` produces no frames. |
| Either liveness read `instrument-failed` | Recorded whole (verdict + `Detail` + errno). Outcome → `bgIdleUnresolved`. Never fatal, never collapsed into a verdict about the command. |
| Lever did not fire | `bgIdleDidNotFire`. A legitimate, publishable result. |
| Rendezvous never fired | `RendezvousAt` nil; the record says so. |
| Artifact write fails | `t.Errorf` (the run's evidence is the deliverable) — but only after `t.Logf`ing the redacted record, so the bytes survive in the test log either way. |

---

## Testing strategy

This is a probe, not a gate: nothing about claude's behaviour is asserted. Two things *are* deterministically verifiable and both are required.

**1. The gate + compile check (offline, no claude).**

- `gofmt -l` clean (check the **output**, not the exit code), `go vet -tags e2e_realclaude ./internal/e2e/realclaude/`, `staticcheck` clean.
- `go test -tags e2e_realclaude -run '^TestInteractiveStreamBackgroundIdleProbe$' ./internal/e2e/realclaude/` → SKIP with the operator instruction sheet. Proves it compiles and the gate works.

**2. `TestBgIdleCountFIFONaming` — one offline table-driven self-check, zero SKIPs.**

The truncation arm is the AC3(c) discriminator and it is exactly the kind of boundary that is wrong on first write (200 vs 201; `…` is one rune, three bytes). Cases, as scenarios:

- One `tool_use` whose `input_summary` contains the FIFO path → `matched=1, truncated=0` (attribution sound).
- No `tool_use` naming the path → `matched=0, truncated=0`.
- Two `tool_use` frames naming the path → `matched=2` (unresolved).
- A `tool_use` whose `input_summary` is **exactly 201 runes ending `…`** and does not contain the path → `matched=0, truncated=1` (**unresolved**, not a non-match).
- A `tool_use` whose `input_summary` is 200 runes, does not end `…`, and does not contain the path → `matched=0, truncated=0` (a plain non-match).

The last two are the pair that discriminates. A matcher that treated every non-match as a non-match would pass every other case and silently assert a false attribution.
Build the 201-rune fixture with `strings.Repeat` + `"…"` and assert its own rune count in the test, so a future change to `maxSummaryLen` fails here rather than in a live run.
Use multibyte runes in at least one fixture — `truncate` is rune-aware, and a byte-length implementation must not pass.

Non-goals: no offline test for the recorder (it needs the sealed transport), and none for the redactor (`realHome` is process state).

---

## Scope bound

The whole deliverable is **one new `_test.go` file** of roughly 570–640 lines including its header prose, plus its GitHub finding comment. If the file starts growing past that, the design has drifted — the things most likely to cause it are all explicitly excluded:

- no second harness or daemon spawner (one exists; use it)
- no second liveness read (both AC2 reads are `fifoLiveRead` on one path at two instants — not two instruments)
- no pid matcher or process-tree walk (AC3(c) closes the gap with recorded frames)
- no `ps -E` or any environment dump of the claude child (#1230 prohibited it: the flag dumps `CLAUDE_CODE_OAUTH_TOKEN`)
- no lever matrix, rows, or reps — one lever, one turn
- no sibling verbatim capture files — one JSON record plus the `t.Logf`
- no runner-attribution globbing (§ AC4)
- no reuse or edit of `runningTurnPrompt`
- no production change

---

## Open questions

1. **Does the 5s lever fire on this surface?** The env plumbing is read from code, not measured: test → daemon (`interactive_bootstrap_liveness_test.go:403`) → claude (`internal/streamsup/runner.go:555`). The second hop *looks* broken — the append sits inside `if r.cfg.Env != nil` — but passthrough survives **both** arms: non-nil appends to `os.Environ()`, and nil leaves `cmd.Env` nil, which makes `os/exec` inherit the parent environment verbatim. Nothing depends on `cfg.Env` being populated. **Do not "fix" it.** AC1's outcome confirms or refutes this, and `bgIdleDidNotFire` is a legitimate result.
2. **Does `input_summary` truncate away the FIFO path?** The path lives under the authenticated temp HOME (`/var/folders/…/work/bgidle-hold` on macOS, ≈90 chars) and `{"command":"cat <path>"}` lands near 110 runes — comfortably under 200. If a long test name pushes it over, the truncation arm catches it as unresolved rather than as a false non-match. That is the designed behaviour, not a gap.
3. **`turn_end.stop_reason` on the background path is unmeasured.** `TurnEndPayload.StopReason` carries `turnevent.TurnEndReason` verbatim; which value the background handoff produces is part of what this probe records. Do not predict it in the file's prose.

---

## Security review

**Verdict:** PASS (after one MUST FIX, revised inline above and re-walked)

The label is earned by mechanism, not topic: this probe runs against a **credentialed** live claude under `--dangerously-skip-permissions`, and its output artefact is **pasted into a public GitHub issue**. Every finding below is about that one asymmetry — a live secret-bearing process producing a published file.

**Findings:**

- **[Trust boundaries] SHOULD FIX — addressed.** The boundary is model-authored text crossing from claude's output into a published artefact (`input_summary`, `result_summary`, `name`, `stop_reason`, `text`). The first draft scattered it across the recorder switch, the classifier and the writer. Now it is one function — `bgIdleFrameFromEnvelope`, a five-arm allowlist with a `Type`-only default — so a field not extracted there cannot reach the artefact by construction. This is what keeps `unrecognized_message.Raw` (claude's verbatim offending message, `protocol/interactive.go:118`) and `assistant_delta.text` out, rather than developer care.
- **[Trust boundaries] No finding — `fifoLiveRead`'s input is not model-derived.** The path is `filepath.Join(h.workdir, bgIdleFIFOName)`, both components test-constructed. The instrument's own gate is a positive `os.ModeNamedPipe` allowlist over `Lstat` (not `Stat`), `fifo_reader_liveness_test.go:134-157`, so a symlink planted at the path is rejected as instrument failure rather than followed.
- **[Tokens/secrets] MUST FIX — fixed.** `rec.EnvDelta` was specified only as a parenthetical value. The natural implementation of a field named "env delta" is a diff of `os.Environ()`, and both the test process and the daemon carry `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` (`fixtures.go:98-99`) — that dumps a live OAuth token into a public issue. The spec now pins `EnvDelta` to a fixed literal and prohibits `os.Environ()`, any child-environment read, and `os.Getenv` for anything but the gate variable. Same prohibition #1230 applied to `ps -E`, restated because the failure shape here is different.
- **[Tokens/secrets] No finding — nothing is minted or stored.** The pairing bearer token and static pubkey stay in `startStreamRunningTurnHarness`'s locals (`:161-169`); `perConvHarness` carries neither, and the record stores frames rather than the harness. Lifecycle/rotation/revocation are unchanged and out of scope.
- **[Tokens/secrets] SHOULD FIX — addressed.** The redactor is the only thing between the artefact and operator-home paths (`ClaudeBin` may sit under `$HOME`), so its own failure mode matters: `strings.ReplaceAll(s, "", "$HOME")` on an unset `realHome` inserts `$HOME` between every character. The empty-`realHome` guard is now an explicit requirement, and the step-2 `artifactDir` log is routed through the same helper so nothing reaches a log around the choke point.
- **[File operations] SHOULD FIX — addressed.** Record file mode is now stated as `0o600` (package norm, `interactive_bootstrap_liveness_test.go:537,553`); `os.MkdirTemp` gives the artifact dir `0700`. The dir is deliberately **not** `t.TempDir()` and deliberately not cleaned up — the operator needs it after the run — so the residual exposure is one 0600 file per run in `$TMPDIR`, matching #1223's precedent.
- **[File operations] No finding — TOCTOU is unreachable here.** `fifoLiveRead` is `Lstat`-then-open, a check-then-use. The path lives inside a per-test `t.TempDir()` HOME with the workdir at `0700` (`interactive_stream_running_turn_test.go:148`) and the FIFO at `0600` (`holdProbeFIFO:665`), so no other user can win the gap. Atomic temp-plus-rename is not applicable: the record is a one-shot artefact, not a registry, and a partial write costs nothing but a re-run.
- **[Subprocess] No finding — this ticket spawns nothing new.** The daemon comes from `spawnBootstrapDaemon` (fixed args) and `probeClaudeVersion` runs `claude --version` under a 10s context. No `sh -c` anywhere. The prompt does construct a command for claude to run, but interpolates only a `t.TempDir()`-derived path and an integer nonce — no metacharacters. The environment is inherited wholesale **by design** (it is the entire lever mechanism, `interactive_bootstrap_liveness_test.go:403` → `streamsup/runner.go:555`); the security requirement is therefore not to scrub it but never to read it back, which is the MUST FIX above.
- **[Cryptography] No finding — no new primitives.** The only crypto-adjacent invariant is the receive-nonce discipline transcribed from `drainForResponding:242-290`: skipping a `noise_msg` desyncs the `CipherState`, so every one is decrypted in order and non-`noise_msg` frames are skipped without decrypting. `math/rand` is unused; `bgIdlePrompt`'s `nonce` is explicitly labelled a cache-buster so nobody reads it as security randomness.
- **[Network & I/O] SHOULD FIX — addressed.** `frames` was an unbounded append over a 120s window, written to a file and logged. Not a remote DoS (in-process, operator-run, time-bounded), but an unacknowledged accumulation in a published artefact. Now capped at `bgIdleMaxFrames` with `FramesDropped` recorded and draining continued past the cap — a silent truncation would read as "that is all the phone received" and misstate AC1. No listener, no upgrade path, no TLS config is added; every wait is bounded (drain 120s, version 10s, FIFO release 10s, daemon stop 3+1s).
- **[Errors/logs] SHOULD FIX — addressed.** The decrypt-failure `t.Fatalf` must transcribe `drainForResponding:272`'s shape — the error only. Adding the raw or sealed frame "to help debug" would put ciphertext and payload in CI logs. `assistant_delta.text` is never recorded (length only), matching the package rule at `interactive_turn_v2.go:70-75`. `result_summary` **is** recorded — AC1 names it, and on the background path it carries a background-task id and a claude-chosen output path; that is an accepted, AC-mandated exposure covered by the redactor, recorded here so code-review need not re-litigate it. No telemetry or metrics.
- **[Concurrency] SHOULD FIX — addressed.** No locks and no shared mutable state; the record is written only on the test goroutine. The one new goroutine (the rendezvous stamper) now has its exit path documented in both directions, including the never-fired case where `holdProbeFIFO`'s cleanup closes `rendezvous` and the buffered(1) send lets the goroutine retire instead of leaking past test end.
- **[Threat model] No finding — no protocol surface is added.** This is a read-only observer of an existing capability-gated stream over an in-process `fakerelay`; no new envelope type, no new capability, no change to `docs/protocol-mobile.md` § Security model. The one threat it genuinely touches is artefact disclosure, covered above.
- **[Out of scope]** Whether the backgrounded command survives teardown, and by whose hand it dies — #1230 / #1231. Whether a client can distinguish the two cases — #1241. `agent-run` budget accounting — #1234. The backgrounded command's output file — #1226.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-29
