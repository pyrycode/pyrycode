# Session transcript & `--resume` probes (#1655, #1656)

## TL;DR

**HOLDS.** A `claude` launched with `--session-id <id>` that runs no turn leaves no
`<id>.jsonl` on disk — neither while the child is still running nor after it exits gracefully
under `SIGTERM`. This is the premise the suspected streamsup crash-loop (observed 2026-08-18)
and [ADR 032](../decisions/032-bootstrap-resume-per-spawn-existence-probe.md)'s per-spawn
existence probe rest on; #1630 carries the by-id-existence rule into `streamsup` and consumes
this measurement's **after-exit** reading (the one its respawn-time probe actually takes).

See the `#1656` section below for the other half: how claude answers `--resume <id>` when
`<id>.jsonl` is absent — also **HOLDS**.

## claude version measured

**2.1.220**, measured 2026-08-20, macOS, `-race`. Test
`internal/e2e/realclaude/session_transcript_probe_test.go`,
`TestRealClaude_TurnlessSessionIDTranscript`. Companion credential-free table test
`TestTurnlessTranscriptVerdict` pins the classifier's five outcome rows offline.

## Argv per arm

Both arms are built by the same function, `transcriptProbeArgs(id)` — the argv shape
`buildArgs` (`internal/streamsup/runner.go`) emits on a first spawn, plus the precedent probe's
`--model`/`--max-turns` addition, no `-p`/`--print`:

```
claude --input-format stream-json --output-format stream-json --verbose \
       --model claude-haiku-4-5 --max-turns 2 --session-id <id>
```

| arm | session id | driven |
|---|---|---|
| control (A) | `16550000-0000-4000-8000-000000000001` | one turn (`"Reply with the single word: ok"`), exit 0 |
| turnless (B) | `16550000-0000-4000-8000-000000000002` | no turn — stdin held open, never written, never closed before reading 1 |

Because both argvs are identical but for the id, the control arm completing its turn under this
exact shape *is* the proof that this claude version accepts arm B's flags — arm B needed no
`init`-line liveness check of its own (`init` is emitted per turn, not at spawn, per
[`set-permission-mode-inband-probe.md`](set-permission-mode-inband-probe.md)).

## The two readings

| reading | taken | found | detail |
|---|---|---|---|
| alive | while the turnless child was still running (`alive_before_reading` / `alive_after_reading` both `true`) | **absent** | `stat ... 16550000-…-000000000002.jsonl: no such file or directory` |
| after exit | after the child ended | **absent** | ended under `sigterm` (533 ms from signal to exit) — no `SIGKILL` escalation needed, so the reading is not confounded by a force-kill |

`#1630`'s probe runs at respawn — after the previous child is already gone — so **the after-exit
reading is the one it consumes.**

## The directory comparison

`child_cwd` (what `agentrun.ResolveWorkdir` resolved the test workdir to, mirroring what
`streamsup` sets `cmd.Dir` to):

```
/private/var/folders/k0/gc07w9ws319b07n0plnw6y8r0000gn/T/TestRealClaude_TurnlessSessionIDTranscript3529494245/001/session-transcript-probe-work
```

Empirical directory (`streamNewSessionTranscriptDir`, found by locating the control arm's
`<A>.jsonl`) and recomputed directory (`sessions.DefaultClaudeSessionsDir(child_cwd)`) —
**matched** in this run:

```
/var/folders/k0/gc07w9ws319b07n0plnw6y8r0000gn/T/TestRealClaude_TurnlessSessionIDTranscript3529494245/001/.claude/projects/-private-var-folders-k0-gc07w9ws319b07n0plnw6y8r0000gn-T-TestRealClaude-TurnlessSessionIDTranscript3529494245-001-session-transcript-probe-work
```

`agentrun.ResolveWorkdir` applies `canonicalCase` on top of `EvalSymlinks`, where
`sessions.DefaultClaudeSessionsDir` applies `EvalSymlinks` alone — this run's path carried no
case difference, so the asymmetry didn't manifest. A divergence, if one is ever recorded on a
different filesystem/case-sensitivity combination, is the finding the follow-up that supplies
this directory on the daemon's production path needs — see the ADR's Related section.

## Verbatim output

```json
{
  "claude_version_raw": "2.1.220 (Claude Code)",
  "claude_version_token": "2.1.220",
  "child_cwd": "/private/var/folders/k0/gc07w9ws319b07n0plnw6y8r0000gn/T/TestRealClaude_TurnlessSessionIDTranscript3529494245/001/session-transcript-probe-work",
  "transcript_dir_empirical": "/var/folders/k0/gc07w9ws319b07n0plnw6y8r0000gn/T/TestRealClaude_TurnlessSessionIDTranscript3529494245/001/.claude/projects/-private-var-folders-k0-gc07w9ws319b07n0plnw6y8r0000gn-T-TestRealClaude-TurnlessSessionIDTranscript3529494245-001-session-transcript-probe-work",
  "transcript_dir_recomputed": "/var/folders/k0/gc07w9ws319b07n0plnw6y8r0000gn/T/TestRealClaude_TurnlessSessionIDTranscript3529494245/001/.claude/projects/-private-var-folders-k0-gc07w9ws319b07n0plnw6y8r0000gn-T-TestRealClaude-TurnlessSessionIDTranscript3529494245-001-session-transcript-probe-work",
  "transcript_dir_match": true,
  "control_arm": {
    "session_id": "16550000-0000-4000-8000-000000000001",
    "argv": ["--input-format","stream-json","--output-format","stream-json","--verbose","--model","claude-haiku-4-5","--max-turns","2","--session-id","16550000-0000-4000-8000-000000000001"],
    "prompt": "Reply with the single word: ok",
    "exit_code": 0,
    "wait_error": "",
    "transcript_path": "/var/folders/k0/gc07w9ws319b07n0plnw6y8r0000gn/T/TestRealClaude_TurnlessSessionIDTranscript3529494245/001/.claude/projects/-private-var-folders-k0-gc07w9ws319b07n0plnw6y8r0000gn-T-TestRealClaude-TurnlessSessionIDTranscript3529494245-001-session-transcript-probe-work/16550000-0000-4000-8000-000000000001.jsonl",
    "transcript_size": 10032,
    "stdout": "<one line of stream-json: system/init, thinking_tokens, rate_limit_event, assistant text \"ok\", result subtype=success — omitted here at full length, see the test's -v log>",
    "stderr": ""
  },
  "turnless_arm": {
    "session_id": "16550000-0000-4000-8000-000000000002",
    "argv": ["--input-format","stream-json","--output-format","stream-json","--verbose","--model","claude-haiku-4-5","--max-turns","2","--session-id","16550000-0000-4000-8000-000000000002"],
    "settle_ms": 30000,
    "alive_before_reading": true,
    "alive_after_reading": true,
    "reading_alive": {
      "found": false,
      "path": "",
      "size": 0,
      "stat_error": "stat /var/folders/k0/gc07w9ws319b07n0plnw6y8r0000gn/T/TestRealClaude_TurnlessSessionIDTranscript3529494245/001/.claude/projects/-private-var-folders-k0-gc07w9ws319b07n0plnw6y8r0000gn-T-TestRealClaude-TurnlessSessionIDTranscript3529494245-001-session-transcript-probe-work/16550000-0000-4000-8000-000000000002.jsonl: no such file or directory"
    },
    "termination_mode": "sigterm",
    "term_to_exit_ms": 533,
    "exit_code": 143,
    "wait_error": "exit status 143",
    "reading_after_exit": {
      "found": false,
      "path": "",
      "size": 0,
      "stat_error": "stat /var/folders/k0/gc07w9ws319b07n0plnw6y8r0000gn/T/TestRealClaude_TurnlessSessionIDTranscript3529494245/001/.claude/projects/-private-var-folders-k0-gc07w9ws319b07n0plnw6y8r0000gn-T-TestRealClaude-TurnlessSessionIDTranscript3529494245-001-session-transcript-probe-work/16550000-0000-4000-8000-000000000002.jsonl: no such file or directory"
    },
    "stdout": "",
    "stderr": ""
  },
  "verdict": "HOLDS",
  "verdict_sentence": "HOLDS: the turnless session's transcript was absent both while the child ran and after it exited gracefully under SIGTERM, so a `--session-id` launch that runs no turn leaves no <id>.jsonl on disk.",
  "reading_alive_verdict": "absent while alive",
  "reading_after_exit_verdict": "absent after a graceful (SIGTERM) exit",
  "reading_consumed_by_1630": "after-exit"
}
```

(The control arm's full `stdout` is one very long stream-json line — an `init` envelope, two
`thinking_tokens` events, a `rate_limit_event`, the assistant's `"ok"` text, and a `result`
envelope with `subtype:"success"`. Elided above for readability; it carries no credential value
and no finding beyond "this claude version accepts the argv and completes a turn under it".)

No credential value from the run environment appears anywhere in this record — `redactCredentials`
runs upstream of every captured stream, and neither arm's stderr was non-empty in this run.

## Verdict

**HOLDS.** The measurement confirms the premise the crash-loop suspicion and ADR 032 rest on: a
`claude` launched under `--session-id <id>` that runs no turn establishes no `<id>.jsonl`, so a
later respawn that emits `--resume <id>` against it is asking claude to resume a transcript that
was never created. **#1630's respawn-time probe consumes the after-exit reading** — the shape
that matches what a real crash-loop respawn actually observes (the previous child already gone).

Caveats carried forward from the design: this is a claude-version fact (2.1.220), not a
guarantee across versions, and the classifier would have recorded **INCONCLUSIVE** rather than
HOLDS had the child needed a `SIGKILL` to end (an absence read after a force-kill cannot
distinguish "never wrote" from "wrote but the flush never ran").

## How to reproduce

```bash
export CLAUDE_CODE_OAUTH_TOKEN=...   # or ANTHROPIC_API_KEY
go test -tags e2e_realclaude -race -v -run TestRealClaude_TurnlessSessionIDTranscript ./internal/e2e/realclaude/
```

~44 s, one cheap haiku turn plus one idle child held ~30 s before termination. `go vet -tags
e2e_realclaude ./internal/e2e/realclaude/` must be run manually — `make check` never compiles
this package. The credential-free classifier table (`TestTurnlessTranscriptVerdict`) runs under
plain `go test ./internal/e2e/realclaude/...` with no credentials and no live claude.

## #1656 — how claude answers `--resume` against an absent transcript

**HOLDS.** `--resume <id>` against an id whose `<id>.jsonl` claude has no record of exits
non-zero, while the identical `--resume` against an id whose transcript exists does not — it
sits on stdin past its deadline instead, the same accept-and-wait shape #1655 measured for a
turnless `--session-id` launch. #1655 measured that the id claude is asked to resume was never
established; this measures how claude answers when it is asked anyway, which is the half the
suspected streamsup respawn crash-loop (observed 2026-08-18) actually turns on. #1630 consumes
both this verdict and #1655's after-exit reading.

### claude version measured

**2.1.220**, measured 2026-08-20 via the dispatcher's `make e2e-realclaude` gate run (`go test
-tags e2e_realclaude -timeout 20m`), macOS. Test
`internal/e2e/realclaude/resume_absent_transcript_probe_test.go`,
`TestRealClaude_ResumeAbsentTranscript` (59.63 s). Companion credential-free table test
`TestResumeAbsentVerdict` pins the classifier's nine outcome cells offline; a third test,
`TestResumeProbeArgsIsRespawnShape`, pins that the resume argv differs from the first-spawn argv
only in the trailing id-flag pair.

### Argv and deadline per arm

All three children run in one workdir. The establish arm calls #1655's `transcriptProbeArgs`
(the first-spawn shape, `--session-id <id>`) unchanged; both resume arms call this ticket's own
`resumeProbeArgs(id)` — the same fixed prefix and base args with the trailing pair swapped to
`--resume <id>` — so their argvs are identical by construction bar the id.

| arm | session id | flag | deadline |
|---|---|---|---|
| establish | `16560000-…-000000000001` (`<A>`) | `--session-id` | 60 s exit window after stdin close |
| absent | `16560000-…-000000000002` (`<C>`, reserved, never established) | `--resume` | 45 s |
| control | `16560000-…-000000000001` (`<A>`, established by the establish arm) | `--resume` | 45 s |

```
claude --input-format stream-json --output-format stream-json --verbose \
       --model claude-haiku-4-5 --max-turns 2 --resume <id>
```

The establish arm ran one turn (`"Reply with the single word: ok"`) to completion, exit 0,
creating `16560000-…-000000000001.jsonl` (9992 bytes) in a directory the run located empirically
via `streamNewSessionTranscriptDir` — matching the recomputed `DefaultClaudeSessionsDir` value in
this run (`transcript_dir_match: true`).

### Both `<C>` readings

| reading | taken | found |
|---|---|---|
| pre-arm | immediately before the absent arm launched | **absent** — `stat …/16560000-…-000000000002.jsonl: no such file or directory` |
| post-arm | after the absent arm ended, before the control arm ran | **absent** — same `stat` error |

**NO STUB:** the resume of an absent id left no `<C>.jsonl` behind, so #1630's by-id existence
probe still reads absent after a rejection and would fall back to `--session-id` rather than
looping on `--resume` forever.

### Verbatim output per arm

**Absent arm** (`<C>`) — exited on its own, exit code 1, message carried on **both** streams:

```
stderr:
No conversation found with session ID: 16560000-0000-4000-8000-000000000002

stdout:
{"type":"result","subtype":"error_during_execution","duration_ms":0,"duration_api_ms":0,
 "is_error":true,"num_turns":0,"stop_reason":null,
 "session_id":"16560000-0000-4000-8000-000000000002","total_cost_usd":0,
 "usage":{...all zero...},"modelUsage":{},"permission_denials":[],
 "uuid":"7fe052e4-7c3f-44e3-8350-a06be0f2f897",
 "errors":["No conversation found with session ID: 16560000-0000-4000-8000-000000000002"]}
```

**Control arm** (`<A>`, transcript present) — did **not** exit within the 45 s deadline; claude
accepted the resume and sat on stdin, exactly as #1655's turnless child did. Ended by this run's
own `SIGTERM`, 558 ms signal-to-exit, no `SIGKILL` escalation needed. Carrying stream:
**neither** (no output was produced before termination — a `SIGTERM`'s post-signal exit code,
143 here, is cleanup detail the classifier never reads, never the answer).

No credential value from the run environment appears in either capture — `redactCredentials` ran
upstream of both streams, and the absent arm's message names only the session id.

### Verdict

**HOLDS.** `classifyResumeAbsent` reads the control arm first: it did not reject (still running
at its deadline, not exited non-zero), so the absent arm decides — it rejected (exit 1) — giving
**HOLDS** under AC 2's rule. `--resume <id>` against a transcript claude has no record of exits
non-zero; a respawn that emits `--resume <id>` for a session that was never established is
therefore asking claude to fail, which is the mechanism the suspected crash-loop needs and ADR
032's fix (resume iff the transcript exists, by-id, no dir scan) addresses.

Caveat carried forward from the design: this is a claude-version fact (2.1.220), not a guarantee
across versions, and a control arm that itself exited non-zero would have made the run
INCONCLUSIVE regardless of the absent arm's answer — that did not happen in this run.

### How to reproduce

```bash
export CLAUDE_CODE_OAUTH_TOKEN=...   # or ANTHROPIC_API_KEY
go test -tags e2e_realclaude -race -v -run TestRealClaude_ResumeAbsentTranscript ./internal/e2e/realclaude/
```

~60 s: one cheap haiku turn to establish `<A>`, a fast absent-arm rejection, then the control arm
burning its full 45 s deadline before this run's cleanup ends it. The credential-free classifier
table (`TestResumeAbsentVerdict`) and the argv-shape pin (`TestResumeProbeArgsIsRespawnShape`)
both run under plain `go test ./internal/e2e/realclaude/...` with no credentials and no live
claude.
