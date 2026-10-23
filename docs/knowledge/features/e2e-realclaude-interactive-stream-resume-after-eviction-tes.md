# interactive_stream_resume_after_eviction_test.go

## Idle eviction and resume

`TestInteractiveStreamResumeAfterEviction` plants a unique token, drains the
completed plant turn and waits for a new `session.idle_eviction` WARN for its
bootstrap session before sending the recall turn. Counting past earlier WARNs
prevents an eviction before planting from satisfying the gate. The five-second
`resumeAfterEvictionIdle` timer and original turn deadlines remain load-bearing:
a never-evicted provider would make recall vacuous, while a fresh successor
would lack the planted context. The by-ID existence probe must select `--resume`.
See the [idle-eviction tests](idle-eviction.md#testing) and the
[recording contract](../../testing/daemon-recordings.md#idle-eviction-and-literal-native-resume-inputs).

## Resume snapshot boundary

A successful bootstrap EOF receipt can be visible before the daemon finishes
waiting for and reaping its provider. An immediate PID check rejects that valid
intermediate state. After the counted WARN, `lifecycleRecordingResumeSnapshot`
uses `waitLifecycleResumePredecessor` to require a successful receipt with empty
error, nonzero frames and the exact original job owner, then signal-zero
observation of that owner's disappearance (`ESRCH`). A present PID remains
pending; unknown observations and invalid receipts fail. Receipt waiting and
retirement share one absolute 15-second deadline, and even ESRCH returned after
expiry fails. Only after this barrier may capture read, or replay install, the
native snapshot and publish it before the original resume send. EOF, kernel
ownership, strict native inputs and final prefix/cleanup audits remain required;
no termination signal, scenario retry or settling sleep establishes retirement.

A passing fresh-home replay alone does not distinguish this race.
`TestResumePredecessorWaitsForRetirement` publishes the receipt while a real owned
child is held alive on stdin, synchronizes repeated signal-zero observations,
then closes stdin and joins `Wait` before permitting the fresh ESRCH observation.
The old immediate check fails this ordering; the bounded barrier completes only
after retirement. `TestResumePredecessorRejectsBoundary` covers foreign/failed,
error-bearing and empty receipts plus unknown process observations;
`TestResumePredecessorDeadline` covers missing receipts, persistent predecessors
and late ESRCH. These controls exercise the tagged helper without live Claude.
See the [retained failure/passing evidence](../../../internal/e2e/realclaude/testdata/3834-resume-retirement-evidence.md)
and [controlled regression guidance](development-verification-prove-that-tests-distinguish-the-change.md).

## Related live scenarios

- `interactive_stream_permission_deny_test.go` (#1175) — the security-relevant
  **deny** half of the remote permission round-trip on the stream-json runner
  (security-sensitive; architect security-review verdict PASS). #1154 proved
  allow on this stack; a fail-open regression (denied tool executes anyway)
  or a hang on the denied modal is exactly the real-claude-specific failure
  the fake tier (#1139) cannot surface.
  `TestInteractiveStreamPermissionDeny` reuses #1154's
  `startStreamModalResolutionHarness` verbatim (no new harness, no new seeded
  UUIDs) and `raiseRealPermissionModal`/`writeFileTrigger` (#1030), swaps the
  answer to `reject_once`, and adds two checks: a `modal_dismissed` drain
  asserting `Source == "remote"` + `Outcome == "reject_once"` (attribution —
  closed vocabulary rules out a timeout-deny or dropped answer masquerading
  as the explicit reject) and a workdir walk,
  `requireTriggerFileAbsent`, proving the gated `Write`'s target file never
  materialised. The retry-answering helper `denyModalsUntilIdle` (rework
  after an operator live-gate FAIL surfaced that real haiku retries a denied
  tool at least once) rejects every permission modal the turn raises until
  terminal `turn_state{idle}`, bounded by a retry-count cap
  (`maxRetryDenies`) and `perTurnReplyBudget` wall-clock, each with a distinct
  diagnostic. No content/echo assertion. Zero production files touched. Split
  from #1083. See [`codebase/1175.md`](../codebase/1175.md).
- `interactive_stream_new_session_test.go` (#1174) — real-claude cross of
  the fakeclaude sibling #1137: on the stream-json runner, `new_session`
  rotates the bootstrap session id AND `streamsup.Runner.RestartFresh`
  spawns a genuinely fresh live `claude` child under the rotated id, not a
  `--resume`. Five milestones: M1 turn-1 liveness (#1153's
  `drainForCompletedTurn`), M2 on-disk id rotation (#1031's actuation
  loop), M3 client-observed `session_transition{clear}` (#1154's
  `drainForControlEvent`), M4 turn-2 accepted (**ack only** at the time this
  spec shipped — the drain gate's sink tag was fixed at runner construction
  and dropped every post-rotation delta, #1081 out of scope. #1133 retired
  that premise: `RestartFresh` now rotates the sink tag with the runner, so
  a post-rotation delta is deliverable in principle. This file sits behind
  the `e2e_realclaude` build tag and was deliberately left unrevisited by
  #1133 — touching it would have pulled a comment edit onto the live gate —
  so M4 stays ack-only here pending a follow-up that upgrades it to a
  phone-side delta assertion), M5 a fresh `<idAfter>.jsonl` transcript appears alongside the
  untouched `<idBefore>.jsonl` (the word-independent, fake-unregressable
  fresh-spawn proof; transcript dir located empirically to sidestep the
  #989 `canonicalCase` hazard). Zero production files touched. Split from
  #1083; sibling leaves #1173 (multi-turn continuity), #1175 (permission
  DENY). See [`codebase/1174.md`](../codebase/1174.md).
- `background_trigger_probe_test.go` (#1223) — **evidence probe, not a
  regression gate**; opt-in behind `PYRY_PROBE_BACKGROUND_TRIGGER=1` on top of
  the package's normal auth skip (an ungated probe would burn ~9 live claude
  turns on every `make preship`). Settles which environment lever
  deterministically makes claude return a background handle under `pyry
  agent-run`, so #1224–#1227 can be specified against a known trigger instead
  of the model's discretion. Mechanism: the test `mkfifo`s a FIFO and holds
  the write end open in a goroutine (`holdProbeFIFO`, release only in
  `t.Cleanup`, never exposing the `*os.File`); claude's `cat <fifo>` Bash call
  blocks on the read end and cannot complete on its own, so a matching
  `tool_result` observed while the write end is held is a **structural**
  signal that claude ended the call itself — not a match against claude's
  result prose (the treadmill #563 and #1219 each paid for once). The same
  property removes the timing race from the `ps -axo pid=,ppid=,pgid=`
  snapshot: it is taken synchronously at the observation point, with liveness
  self-evidenced via a `cmd.Wait` channel rather than `Signal(0)` (which
  reports an unreaped zombie as alive). Row-table design over
  `BASH_DEFAULT_TIMEOUT_MS` / `BASH_MAX_TIMEOUT_MS` /
  `CLAUDE_CODE_DISABLE_BACKGROUND_TASKS` / model-set `run_in_background`, each
  a `t.Run` subtest with its own `t.TempDir()`/`t.Setenv`/FIFO. **Result:**
  `BASH_DEFAULT_TIMEOUT_MS` set low is the deterministic trigger (7/7 firing
  reps across all rows that carry it); `BASH_MAX_TIMEOUT_MS` alone does not
  fire (it caps what the model may set, and the model set no `timeout` in any
  rep); `toolUseResult.timedOutAfterMs` is the only surface that discriminates
  the timeout-expiry path from the model-set `run_in_background` path — the
  process tree is byte-identical between them. Four credential-free
  self-checks (FIFO hold/release, `ps`-parse, `input.timeout` projection) run
  ungated. Zero production files touched. Kept (not deleted) because
  #1224–#1227 all have to stage this same scenario. See
  [`codebase/1223.md`](../codebase/1223.md) for the full lever table, the
  live-run evidence, and two known gaps flagged by code review (SHOULD FIX,
  not blocking) in the Bash-call selection and the env-arrival control's
  absent/unread collapse.
- `fifo_reader_liveness_test.go` (#1239) — **offline instrument, not a probe**;
  no auth skip, no env gate, no live claude. Answers "is some process
  currently holding this FIFO's read end?" with no pid and no `ps`, closing a
  gap `holdProbeFIFO` alone leaves open: holding the write end proves a
  command could not have *finished*, not that it is still *alive* — a killed
  command leaves the same held write end. `fifoLiveRead(path)` returns a
  three-valued `fifoLiveOutcome` (`reader-present` / `no-reader` /
  `instrument-failed`, never a bare boolean) via `Lstat` → positive
  `os.ModeNamedPipe` allowlist gate → `open(path, O_WRONLY|O_NONBLOCK)`
  (success = reader present, `ENXIO` = no reader, everything else =
  instrument failure). The allowlist gate is what closes the inverting
  failure on the success arm: a bare open on `/dev/null` or a regular file
  succeeds with no reader anywhere, which a regular-file blacklist would
  misread as "reader present." Four offline self-checks prove: the read
  flips on one FIFO across one reader's lifetime (`Kill()` alone does not
  flip it — `Wait()`/reap does), the mode gate rejects every non-FIFO path
  including `/dev/null`, every open errno except `ENXIO` yields
  instrument-failed, and repeated reads don't perturb a blocked reader.
  Zero production files touched. #1240 is natively blocked on this file and
  calls `fifoLiveRead` at the instant it records `turn_state{idle}`. See
  [`codebase/1239.md`](../codebase/1239.md) for the implementation detail and
  two known gaps flagged by code review (SHOULD FIX, not blocking) in the
  `Lstat`-arm errno assertion coverage and a stuttering `Detail` string.
- `interactive_background_idle_probe_test.go` (#1240) — **evidence probe,
  security-sensitive**; opt-in behind `PYRY_PROBE_INTERACTIVE_BG_IDLE=1` on
  top of the package's normal auth skip. Stages one turn on the production
  stream-json interactive daemon around a Bash command claude backgrounds on
  timeout expiry (#1223's `BASH_DEFAULT_TIMEOUT_MS` trigger, reused unedited),
  records every frame the phone receives in receive order via the new
  `bgIdleRecordTurn` (the genuinely new code — both existing drains in this
  file discard frames, so neither was reusable as a recorder), and takes
  `fifoLiveRead` (#1239) twice on the same FIFO path across one command's
  lifetime — pre-rendezvous (must read `no-reader`) and at the instant `idle`
  is recorded — to re-prove the liveness flip in this rig rather than
  inheriting #1239's self-check. Attribution of the held FIFO to the
  backgrounded `tool_use` is closed by counting recorded frames
  (`bgIdleCountFIFONaming`, 201-rune truncation tell), not a third pid
  matcher. One extraction allowlist (`bgIdleFrameFromEnvelope`, five named
  arms + a default that can hold nothing but `conversation_id`) keeps
  claude's verbatim `unrecognized_message.Raw` and `assistant_delta.text` out
  of the published artefact by construction. **Result, run live 2026-07-30
  (3 reps, claude 2.1.220): yes — `turn_state{idle}` is emitted while the
  backgrounded command is still alive**, and `turn_end.stop_reason` is
  byte-identical (`"end_turn"`) between that case and a genuine finish, so a
  client has no field to key on. Side finding: an `unrecognized_message`
  frame appeared in all 3 reps specifically on the backgrounding path, filed
  separately. Zero production files touched. Split from #1227; blocks #1241
  (client-distinguishability baseline diff), which inherits this recorder.
  See [`codebase/1240.md`](../codebase/1240.md) for the full finding, two
  known gaps flagged by code review (SHOULD FIX, not blocking) in claude
  version attribution and anomaly-flag verdict gating, and the live-run
  timeline.
