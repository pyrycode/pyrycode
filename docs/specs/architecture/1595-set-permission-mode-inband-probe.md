# #1595 — realclaude: does a `set_permission_mode` control request land in-band?

Measurement-only ticket. Nothing in `internal/`, `cmd/` changes; one new file under
`internal/e2e/realclaude/`, its captured fixtures, and one knowledge doc. #1596 acts on
what this records.

## Files to read first

Symbols, not line numbers — resolve each with `codegraph_search` / `codegraph_node`.

- `internal/e2e/realclaude/permission_protocol_spike_test.go` → `TestRealClaude_PermissionProtocol_Spike`
  — the whole shape this file mirrors: direct `exec.CommandContext("claude", …)`, one reader
  goroutine over a `bufio.Scanner`, a fixture written on every outcome, and a test that passes
  either way. **Read the whole file**; four of its helpers are reused verbatim.
- `internal/e2e/realclaude/permission_protocol_spike_test.go` → `captureClaudeVersion`,
  `versionSlug`, `packageDir`, `truncateString` — **reuse these four as-is** (same package, no
  import, no copy). They are the difference between this ticket fitting in `s` and not. Do not
  reimplement them and do not modify them.
- `internal/e2e/realclaude/permission_protocol_spike_test.go` → `writeFixture`, `fixtureRecord`
  — the shapes the new writer and record type mirror. `writeFixture` hardcodes one filename per
  version, so it cannot be reused for four arms; write a sibling. **Do not edit either** — the
  regression test below depends on both.
- `internal/e2e/realclaude/permission_protocol_regression_test.go` → `fixtureGlob`,
  `fixtureNameRE`, `assertRegressionFixture` — the trap the ticket names. Extract: exactly which
  filenames get swept into a test that asserts a different argv's findings.
- `internal/e2e/realclaude/dropped_line_capture_test.go` → `dropcapFixtureGlob` — the *other*
  testdata glob in this package. Both globs must miss the new names.
- `internal/e2e/realclaude/interactive_stream_inband_model_test.go` →
  `inbandWaitResults`, `inbandSendTurn`, `inbandTapRecorder` — the poll-until-result-count idiom
  and the mutex-guarded stdout recorder. Extract the *idiom*; do not reuse `inbandTapRecorder`
  itself (it is wired into `streamsup.Config.Stdout` and carries a partial-line accumulator this
  file does not need — see § Design, "why a plain Scanner").
- `internal/e2e/realclaude/fixtures.go` → `WithWorktreeAuthenticated` — the credential/skip gate.
  Accepts `CLAUDE_CODE_OAUTH_TOKEN`; returns the pinned `$HOME`.
- `internal/e2e/realclaude/resilience_test.go` → `resolveClaudeBin` — the binary resolver with the
  fork-bomb guard. Use it, not a bare `exec.LookPath`.
- `internal/streamsup/envelope.go` → `controlRequest`, `controlRequestInner`,
  `marshalInterruptEnvelope` — the exact production wire shape of a control line and the
  structured-encoding discipline (never string concatenation) the new marshaller must copy.
- `internal/streamsup/runner.go` → `(*Runner).Interrupt`, `nextInterruptID` — where the id is
  minted and the documented fact that the `control_response` ack is never read. This ticket reads
  it; that is the new part.
- `internal/sessions/session.go` → `claudeSettingsArgs`, `(*Session).spawnArgs` — the one place
  `--dangerously-skip-permissions` enters an argv. It is what "pyry's invocation" means in AC 5.
- `internal/sessions/pool.go` → `inBandDeliverable` — the predicate whose `YOLO != nil → false`
  arm this measurement exists to inform. Read its doc comment; it names the missing measurement
  in as many words.
- `docs/knowledge/features/permission-protocol-spike.md` — §§ "Findings" and "Reproducing the
  matrix". Finding #2 (Bash ran under every mode) is the reason a behavioural read needs a
  control, and § "Reproducing" is the rename-between-runs workaround that does not transfer here.

## Context

pyry expresses the bypass posture as a spawn-time flag, so changing it live means killing the
child and relaunching it (`(*Session).spawnArgs` → `sup.Restart(newArgs)`). #1581 moved the
model/effort case off that restart — claude accepts `/model` and `/effort` as ordinary user turns
on the held-open stream — but `inBandDeliverable` still routes any update carrying a `YOLO` to
the restart, because no in-band form for the bypass posture had been measured.

claude 2.1.220 carries a `set_permission_mode` control subtype on the same held-open stdin the
daemon already writes interrupts to. Reading the subtype out of the binary does not settle
whether it is *wired* under pyry's stream-json-in/stream-json-out invocation: the binary also
carries the string `set_permission_mode is not supported in this context (onSetPermissionMode
callback not registered)`. Only a live run answers that.

This ticket measures and records. It adds no production writer for the new subtype.

### Facts already measured — do not re-derive

From the ticket body, measured 2026-08-19 against claude 2.1.220:

- `init.permissionMode` reads `default` with no flag, `bypassPermissions` with either
  `--dangerously-skip-permissions` or `--permission-mode bypassPermissions`. The two agree.
- **The init line is emitted per turn, not at spawn.** A child given stream-json stdin and no
  user message emits nothing before EOF. So an "init after the control request?" reading is
  vacuous unless a further turn is driven first.
- **A `default`-launched child auto-approved Bash anyway** in the #383 fixtures, at 2.1.199 and
  2.1.220. "The tool still ran" is therefore not on its own evidence of a failed revocation.

## Design

One new file: `internal/e2e/realclaude/set_permission_mode_probe_test.go`, build tag
`//go:build e2e_realclaude`, package `realclaude`. No other file in the repo is modified.

### Four children, one drive sequence

| arm (subtest name) | launch flag | control request sent | role |
|---|---|---|---|
| `revoke` | `--dangerously-skip-permissions` | `mode: "default"` | measurement |
| `enable` | *(none)* | `mode: "bypassPermissions"` | measurement |
| `control_default` | *(none)* | *(none)* | control for `revoke` |
| `control_bypass` | `--dangerously-skip-permissions` | *(none)* | control for `enable` |

Every arm runs the identical sequence, so the comparison is index-symmetric:

```
spawn (stdin held open)
  → probe turn 1  → wait for a `result` line
  → [control request + wait for a `control_response`]     ← measurement arms only
  → probe turn 2  → wait for a `result` line
  → close stdin → Wait()
```

The control arms omit only the bracketed step; they still drive both turns. That is deliberate.
The measurement arm's post-change read is its **turn 2**, so its control must also be a turn 2 —
same prompt, same position in the session, same accumulated context. Comparing a measurement
turn 2 against a control turn 1 would fold a turn-index confound into the verdict.

Both probe prompts are fixed and shared across all four arms, and they differ from each other so
turn 2 is a fresh request rather than one claude can answer with "I already did that":

- turn 1: ``Use the Bash tool to run `ls -la /` and report the first line of output.``
- turn 2: ``Use the Bash tool to run `ls -la /tmp` and report the first line of output.``

Bash and `ls -la` mirror the #383 spike's probe on purpose: it makes the recorded finding #2
directly comparable rather than a second unknown.

### Argv

Per arm: the production stream-json prefix, plus the posture flag, plus a cost cap.

```
claude --input-format stream-json --output-format stream-json --verbose
       --model claude-haiku-4-5
       --max-turns 8
       [--dangerously-skip-permissions]        ← the arm's posture
```

Three deliberate divergences from the #383 spike, each load-bearing for AC 5:

- **No `--permission-prompt-tool stdio`.** The spike concluded that flag short-circuits allowlist
  enforcement; pyry's invocation does not carry it, and AC 5 asks about pyry's invocation.
- **No `--allowed-tools`.** Same reason — pyry's stream path does not set it.
- **`--dangerously-skip-permissions`, not `--permission-mode bypassPermissions`,** for the bypass
  posture. It is the flag `claudeSettingsArgs` actually emits, and the ticket's baseline table
  already confirms the two agree on `init.permissionMode`.

`--session-id` is omitted (claude mints its own); it is orthogonal to permission mode and its
absence keeps the argv shorter. Record the full argv in the fixture either way.

`--max-turns 8` is a token bound with ~2× headroom over the four assistant turns two probes need
(the spike's one probe fit in 2). If a `result` line comes back with `subtype:
"error_max_turns"`, the fixture records it plainly — raise the cap and rerun. It fails loudly,
not silently.

### Types and contracts

Nothing here is exported; the whole file is package-private to `realclaude`.

One arm descriptor, and one driver over it:

```go
// setModeArm is one row of the table above. targetMode == "" ⇒ a control arm:
// send no control request, drive both turns anyway.
type setModeArm struct{ name string; launchYOLO bool; targetMode string }

// runSetModeChild spawns one child, drives the sequence above, and returns the
// completed record. Never t.Fatalf for an outcome that is information — only for
// a broken instrument (§ Error handling).
func runSetModeChild(t *testing.T, claudeBin, workdir string, arm setModeArm,
	versionRaw, versionToken string) *setModeFixtureRecord
```

One marshaller, whose output shape is the whole point of the ticket. Marshalled structured,
never concatenated — the same one-physical-line invariant `marshalInterruptEnvelope` holds:

```go
// Shape: {"type":"control_request","request_id":"<id>",
//         "request":{"subtype":"set_permission_mode","mode":"<mode>"}}
func setModeControlLine(requestID, mode string) ([]byte, error)
```

And the behavioural read of ONE probe turn. Compared by value: two arms "behave the same" iff
their outcomes are equal, so every field here is a potential discriminator and none may be
dropped for looking redundant.

```go
type probeOutcome struct {
	ToolUseNames      []string `json:"tool_use_names"`     // in arrival order
	ToolResultSeen    bool     `json:"tool_result_seen"`
	ToolResultIsError bool     `json:"tool_result_is_error"`
	PermissionDenials int      `json:"permission_denials"` // from the result trailer
	ResultSubtype     string   `json:"result_subtype"`
	ResultIsError     bool     `json:"result_is_error"`
	ResultObserved    bool     `json:"result_observed"`    // false ⇒ the turn never closed
}
```

`setModeRecorder` is the stdout sink: one mutex, an append-only `[]json.RawMessage` of every
line, a `results int` counter, and a `[]json.RawMessage` of `control_response` lines. Accessors
`lines()`, `resultCount()`, `controlResponses()` return snapshot copies.

**Why a plain `bufio.Scanner` and not `inbandTapRecorder`'s accumulator.** That recorder exists
because `streamsup.Config.Stdout` hands it arbitrary byte chunks, so it must do its own line
splitting and cap its own partial. Here the file owns `cmd.StdoutPipe()` directly, so
`bufio.Scanner` does the splitting — exactly as `TestRealClaude_PermissionProtocol_Spike` already
does. Use the spike's 1 MiB `scanner.Buffer` cap and record `scanner.Err()` in the fixture; an
over-long line is then a recorded fact, not a silent truncation.

### Fixture record and filenames

`setModeFixtureRecord` — JSON field names matter, this is the durable artifact:

| field | why it is there |
|---|---|
| `claude_version_raw`, `claude_version` | from `captureClaudeVersion` |
| `arm`, `launch_yolo_flag`, `argv` | which posture and exactly how it was launched |
| `prompts` | the two probe prompts, verbatim |
| `control_request_sent` | the raw line written, or `null` on a control arm |
| `control_responses` | **every** `control_response` line, verbatim — AC 1 |
| `control_response_request_id_matched` | did the reply correlate to the minted id |
| `init_permission_modes` | `permissionMode` off every `system`/`init` line, arrival order |
| `stdout_events` | every line, verbatim |
| `turn_boundaries` | index of each `result` line, so a reader can slice turn windows |
| `probe_outcomes` | the two `probeOutcome`s, turn 1 then turn 2 |
| `stderr_capture`, `exit_code`, `context_deadline_tripped`, `duration_ms`, `scanner_error` | structural |

Filenames: `set_permission_mode_v<versionSlug(token)>_<arm>.json` — e.g.
`set_permission_mode_v2.1.220_revoke.json`. Four files, one per arm, so no direction overwrites
another (AC 4) and the version is carried.

**The naming trap, closed deterministically.** `fixtureGlob` is
`testdata/permission_protocol_v*_*.json` and `fixtureNameRE` parses the trailing token as the
*expected* `init.permissionMode`; `dropcapFixtureGlob` is `testdata/dropped_lines_v*.json`.
Neither matches a name starting `set_permission_mode_`. Do not leave that as a convention someone
must remember — add a second, **live-free** test in the same file:

```go
// TestRealClaude_SetPermissionMode_FixtureNamesAvoidRegressionGlobs asserts that
// every name setModeFixturePath mints for every arm is matched by NEITHER
// fixtureGlob NOR dropcapFixtureGlob. No subprocess, no credentials — it passes
// on a machine with no claude at all.
```

It is the deterministic half of AC 4's "the existing regression test still passes unchanged":
that test's behaviour is a pure function of which filenames exist, so a name check settles it
without a live run. Drive it through the same `setModeFixturePath(versionToken, arm)` the writer
uses (not a hand-typed literal), over a table of adversarial version tokens including one
containing an underscore.

### Verdict computation

Computed in the parent test after all four arms return, from turn-2 outcomes only.

1. **Discrimination gate first.** If `control_default.turn2 == control_bypass.turn2`, the probe
   cannot tell the two postures apart on this argv. Record, for **both** directions, exactly:
   *"the behavioural probe does not discriminate on this argv"* — and stop. Do not report a
   revocation or an escalation. This is AC 3's explicit branch and, given #383 finding #2, it is
   a live possibility rather than a defensive nicety.
2. Otherwise, per direction, classify the measurement arm's turn 2:

   | `revoke.turn2` matches | verdict |
   |---|---|
   | `control_default.turn2` | REVOCATION APPLIED |
   | `control_bypass.turn2` | REVOCATION FAILED |
   | neither | INCONCLUSIVE — behaviour matched no control |

   and symmetrically for `enable` against `control_bypass` (ESCALATION APPLIED) /
   `control_default` (ESCALATION FAILED).

3. **The echoed `control_response` never enters the verdict.** AC 3 is explicit in both
   directions: an echoed `success` that still behaves like the bypass control is a FAILED
   revocation, and an escalation whose behaviour matches the bypass control is a successful
   escalation *even if its `control_response` was an error*. The response and the `init` echo are
   recorded alongside the verdict, as separate rows.

If a `-run` filter left any arm unrun, log that the cross-arm verdict is unavailable and skip
step 1–3 rather than computing a verdict from a missing control.

### Concurrency model

Per child, and only one child is alive at a time (the arms run sequentially, no `t.Parallel()`):

- **Reader goroutine** — owns the `bufio.Scanner` over `cmd.StdoutPipe()`, appends and classifies
  each line under the recorder's mutex. Exits on stdout EOF, closing `readerDone`.
- **Test goroutine** — writes stdin, polls the recorder (100 ms, same idiom as
  `inbandWaitResults`), closes stdin after turn 2, then `cmd.Wait()`, then `<-readerDone`.
- **Shutdown** — `exec.CommandContext` with a per-child deadline is the hard bound; stdin close
  is the graceful one. `defer cancel()` before any return path.

No goroutine outlives its child: EOF follows either the graceful exit or the context kill.

### Error handling

The dividing line is *is this outcome information, or a broken instrument?*

Recorded and continue (each has a fixture field, and the run proceeds to the next step):

- turn 1 or turn 2 produced no `result` within budget → `ResultObserved: false`
- no `control_response` within budget → empty `control_responses`
- a `control_response` whose `request_id` does not match → `control_response_request_id_matched: false`
- a `control_response` carrying `response.subtype: "error"` → the message is in the verbatim line
- no `system`/`init` line after the control request → `init_permission_modes` shows it; turn 2 was
  driven first, which is what makes recording the absence legitimate under AC 2
- stdin write error, non-zero exit, tripped deadline, scanner error → their own fields

`t.Fatalf` only for a broken instrument: claude absent (`resolveClaudeBin` skips), no credentials
(`WithWorktreeAuthenticated` skips), spawn failure, `captureClaudeVersion` failure, or an arm that
captured **zero** stdout lines — the last mirroring the spike's structural-failure guard, since a
fixture with no events records nothing.

**The test passes on every recorded outcome**, per AC 4 and the
`TestRealClaude_PermissionProtocol_Spike` precedent. A supported mechanism and an unsupported one
are both findings; only an instrument that measured nothing is a failure.

### Budgets

| bound | value | why |
|---|---|---|
| per-child context | 4 min | hard kill; covers both turns plus slack |
| per-turn `result` wait | 2 min | a healthy probe turn lands in seconds; the headroom is for a `default`-mode child that blocks on an approval it can never receive |
| `control_response` wait | 45 s | long enough that absence means absence |
| poll interval | 100 ms | same as `inbandPoll` |

Worst case (all four arms stalling every turn) ≈ 16 min; expected ≈ 2 min and roughly $0.05 in
tokens across the four children.

## Testing strategy

This file *is* the test. What to run, in order:

1. **Live-free, first, costs nothing:**
   `go test -tags e2e_realclaude -run TestRealClaude_SetPermissionMode_FixtureNamesAvoidRegressionGlobs ./internal/e2e/realclaude/`
   plus `go test -tags e2e_realclaude -run TestRealClaude_PermissionProtocol_RegressionFixtures ./internal/e2e/realclaude/`
   — the second must pass **before and after** the new fixtures land (AC 4).
2. **Cheapest live arm, to shake out the driver before spending four children on a bug:**
   `-run 'TestRealClaude_SetPermissionMode_InBandProbe/control_bypass'`. One child, YOLO posture,
   no control request. Confirm the fixture is written and carries two `result` lines.
3. **One measurement arm:** `-run '…/revoke'`. Confirm a `control_request` line was written and
   whatever came back — or did not — is in the fixture.
4. **Full run**, then read the verdict out of the log and write it into the knowledge doc.
5. `go vet -tags e2e_realclaude ./internal/e2e/realclaude/` — **run this manually.** `make check`
   never compiles this package, so a build break there is invisible to the standard gate.
6. `gofmt -l` on the new file **only**. `gofmt -l internal/e2e/realclaude/` is already dirty on
   `main` for unrelated files (`permission_protocol_spike_test.go` among them); filter to the
   branch's own diff before reading output as a finding.

Read the `=== RUN` count, not the exit code: a package that fails to build reports zero tests and
exit 0 through a shell wrapper.

## Deliverables

1. `internal/e2e/realclaude/set_permission_mode_probe_test.go` — the two test functions above.
2. Four fixtures under `internal/e2e/realclaude/testdata/`, committed.
3. `docs/knowledge/features/set-permission-mode-inband-probe.md` — **≤ 100 lines**, these
   headings, prose only where a measured value goes:
   - **TL;DR** — one sentence answering "is an in-band bypass change available to pyry's
     invocation?" This is the sentence #1596 reads. If the behavioural probe did not
     discriminate, say so here rather than implying an answer.
   - **claude version measured** and date.
   - **Argv per arm** and the two probe prompts.
   - **The `control_request` line sent**, verbatim.
   - **The `control_response` received**, verbatim, per direction — including the error message
     if one came back, and the fact if none did.
   - **`init.permissionMode` after the change**, per direction, or the recorded absence.
   - **Behavioural verdict per direction**, naming the control arm it was judged against.
   - **The escalation finding** — if `enable` succeeded, state plainly that a bypass-posture
     escalation is reachable over the daemon's stdin channel. Record it even though this ticket
     acts on nothing.
   - **What #1596 can rely on**, and **how to reproduce**.
4. The file's own header doc comment carries the same finding in short form — the house pattern
   (`TestInteractiveStream_InBandModelChange_LiveChildReportsNewModel`,
   `TestRealClaude_PermissionProtocol_Spike`), and the copy that cannot drift from the test.

**Out of scope for the developer:** `docs/knowledge/INDEX.md` (documentation phase is its sole
writer) and `docs/knowledge/codebase/1595.md` (documentation phase writes it from this spec plus
the merged diff). Do not create either. The developer's worktree mutates the test file, the
fixtures, this spec's sibling knowledge doc, and nothing else.

## Open questions

- **Does `--max-turns 8` cover two tool-using probe turns?** The spike measured 2 assistant turns
  per probe, so 8 is ~2× headroom — but that was one probe, not two in a held-open session. If a
  `result` returns `subtype: "error_max_turns"`, raise the cap and rerun. The fixture makes this
  legible rather than silent, which is why the cap is safe to guess at.
- **Does claude accept a `control_request` while idle between turns?** `(*Runner).Interrupt`
  writes one mid-turn, which is the measured case. Idle is the deterministic point to write at
  and is what the sequence uses. If an idle control line is ignored where a mid-turn one is not,
  that is itself the finding — record it; do not silently switch to mid-turn delivery.
- **Does a `default`-launched child block or deny when a tool needs approval and no prompt tool is
  configured?** Unmeasured, and it is the discriminator the verdict hinges on. Both outcomes are
  handled: a denial shows up in `permission_denials` / `tool_result.is_error`, a block shows up as
  `ResultObserved: false` after the 2-minute wait. Either is a legitimate `probeOutcome` value and
  either discriminates against the bypass control.
- **Whether an in-band form, if it works, should ship.** Explicitly #1596's call. Do not add a
  production writer for the subtype here.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. The one boundary is claude's stdout → the test's memory,
  crossed in exactly one place (`setModeRecorder`'s scan loop) and only ever as
  `json.RawMessage` retained verbatim plus scalars pulled out by anonymous-struct decode. Nothing
  parsed here reaches production code, a network socket, or a filesystem path — it reaches a
  fixture file and `t.Logf`. SHOULD FIX, noted for code-review: a decode failure on a single line
  must be skipped silently rather than aborting the arm, mirroring `inbandTapRecorder`'s
  `consume` — claude's stdout carries lines this file has no interest in.
- **[Tokens, secrets, credentials]** MUST FIX **addressed in this spec**, not deferred. Two
  concrete leaks were reachable in the first draft and both are closed by the fixture-field table:
  (a) `stderr_capture` is truncated through `truncateString` at the spike's 8 KiB cap so a claude
  auth failure cannot dump an unbounded credential-bearing message into a committed file; (b) the
  fixture records **`argv`**, and the argv deliberately carries no credential — auth reaches the
  child through the environment (`WithWorktreeAuthenticated`), which is **not** recorded. The
  developer must not add an `env` field to `setModeFixtureRecord`. Note that
  `init.apiKeySource` appears in claude's own init line and is recorded verbatim in
  `stdout_events`; the #383 fixtures already commit that field, it names the *source* rather than
  the value, and that precedent is accepted here rather than re-litigated.
- **[File operations]** No MUST FIX. Fixture paths are `filepath.Join(packageDir(t), "testdata",
  <name>)` where `<name>` is built from `versionSlug` — which regex-substitutes everything outside
  `[a-z0-9._-]` and truncates to 32 chars — and a hardcoded arm token. `claude --version` is
  attacker-influenced only by whoever already controls the `claude` binary on the operator's PATH,
  and `versionSlug` leaves `.` and `-` intact, so `..` survives slugging: traversal is blocked by
  the token being a *filename component* only, never a path. SHOULD FIX for code-review: keep the
  temp-file-plus-rename write from `writeFixture` rather than a bare `os.WriteFile`, so an
  interrupted run cannot commit a half-written fixture. Mode `0644` matches the existing
  fixtures and is correct — these files are committed to a public repo and hold no secret.
- **[Subprocess execution]** No MUST FIX. `exec.CommandContext` with an argv slice; no `sh -c`,
  no shell metacharacter path. Every argv element is a compile-time literal except the binary
  from `resolveClaudeBin` (which carries its own fork-bomb guard against
  `PYRY_CLAUDE_BIN` pointing at the test binary). The probe prompts are literals, and they reach
  claude as JSON string *values* inside a marshalled envelope, never as argv. The `mode` value in
  the control line is one of two spec-fixed literals. `--dangerously-skip-permissions` on two of
  four arms is the *subject* of the measurement, and it runs against a `$HOME` pinned by
  `WithWorktreeAuthenticated` with the probe confined to `ls -la` — read-only, no writes.
- **[Cryptographic primitives]** Not applicable, and by design rather than by omission: the
  `request_id` is a correlation token, not a security token. `(*Runner).Interrupt` mints it from a
  monotonic counter (`nextInterruptID`) for the same reason. Nothing here compares a
  caller-supplied value against a secret, so there is no constant-time question. Do not reach for
  `crypto/rand`; a fixed per-arm id makes the fixture diffable.
- **[Network & I/O]** No MUST FIX. There is no socket. The one unbounded-input surface is
  claude's stdout, capped twice: `scanner.Buffer(…, 1 MiB)` per line, and the per-child context
  deadline on total volume. `scanner_error` records a line that exceeded the cap, so truncation is
  never silent.
- **[Error messages, logs, telemetry]** No MUST FIX, and one deliberate divergence worth naming.
  The #833 posture keeps model / effort / YOLO values out of the **daemon** log; it does not
  constrain a test's `t.Logf`, and this test must print the posture it launched with or the
  verdict is unreadable. That is a test-process log on an operator's terminal, not the daemon log,
  and no production logging path is touched. No credential reaches a log line: the only values
  logged are arm names, modes, `probeOutcome` fields, and counts.
- **[Concurrency]** No MUST FIX. One mutex, never held across a channel operation or an I/O call,
  so there is no lock-ordering question. One goroutine per child with a single exit condition
  (stdout EOF), joined via `readerDone` before the arm returns; the context deadline guarantees
  EOF even if claude hangs, so leakage across arms is not reachable. Arms are sequential, so at
  most one child and one reader exist at a time. `-race` must be part of the live run.
- **[Threat model alignment]** The **`enable` direction is itself a privilege-escalation
  probe**, and that is this pass's substantive finding rather than a category to wave through. If
  it succeeds, a bypass posture is reachable over the daemon's stdin channel without a respawn —
  which means anything that can write a line to a child's stdin can escalate that child. The spec
  requires that outcome recorded plainly in the knowledge doc even though nothing acts on it here
  (ticket note, AC 5). Mitigation is explicitly OUT OF SCOPE and belongs to **#1596**: this
  ticket adds no writer for the subtype, so the measurement cannot itself widen the surface.
  Note also that `marshalTurnEnvelope`'s structured encoding already blocks a prompt from forging
  a `control_request` line, so an *untrusted prompt* is not a path to this escalation today —
  which is precisely why #1596 must decide before a writer exists, not after.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-19
