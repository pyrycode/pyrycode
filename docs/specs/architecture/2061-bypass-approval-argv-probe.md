# #2061 — the bypass launch argv against production's approval flags, and the pre-downgrade window

Measurement ticket. Ships no production writer and changes no daemon behaviour. It
answers two questions #1686 needs settled before anything is built on #2060's
finding, and commits the captures.

## Files read

- `internal/e2e/realclaude/set_permission_mode_probe_test.go` → `setModeArm`,
  `setModeChildConfig`, `runSetModeChild`, `setModeRecorder`, `probeOutcome`,
  `setModeFieldMatches`, `setModeNoDiscrimination`, `writeSetModeFixture`,
  `setModeOutcomeAt`, `setModeAdversarialVersionTokens` — the rig this ticket
  rides. `runSetModeChild` builds its argv inline and branches only on
  `arm.launchYOLO`; every control request it writes is positioned *after* a turn.
  Those are the two axes that have to widen.
- `internal/e2e/realclaude/bypass_reescalation_probe_test.go` → `reescalateArm`,
  `reescalateDirection`, `reescalateDowngrade`, `reescalateFixtureName`,
  `TestBypassReescalation_FixtureNamesAvoidRegressionGlobs` — #2060, the nearest
  analogue: inline arm construction, its own fixture minter, a per-direction read
  turn, and a gate that refuses a vacuous verdict. Its header carries the finding
  this ticket builds on.
- `internal/e2e/realclaude/ask_user_question_capture_test.go` →
  `askQuestionCaptureServe`, `askQuestionCapturePromptTool`, and the argv literal
  in `TestRealClaude_AskUserQuestion_CapturesTheCall` — the in-package precedent
  for a transcribed mcp-config, a stub control socket bound *before* claude
  starts, an **accept loop** rather than a single accept, and unconditional deny.
  Its argv already carries all four production flags, `--permission-mode default`
  included.
- `cmd/pyry/mcp_config.go` → `permissionArgs`, `approveToolRef`,
  `renderMCPApproveConfig` — the production flag set this ticket puts a bypass
  flag next to, and the document shape the test transcribes (package `main`, not
  importable here). `permissionArgs`' own doc states the yolo branch means
  "claude's permission path is disabled: no prompt tool, no mcp-config", which is
  the hazard Q2 exists to measure.
- `cmd/pyry/streamsup_runner.go` → `withApprovalArgs`, `namesPermissionMode` —
  production never composes bypass **and** the approval flags: `withApprovalArgs`
  returns early on `--dangerously-skip-permissions`. #1686 would have to change
  that, so no committed capture covers the combined argv.
- `internal/control/protocol.go` → `Request`, `ApprovePayload`, `ApproveResult`,
  `Response` — the wire types the stub socket decodes and answers.
- `internal/permbridge/permbridge.go` → `BehaviorDeny` — the verdict constant the
  stub returns, referenced rather than spelled `"deny"`.
- `internal/e2e/realclaude/harness_daemon_test.go` → `shortSocketPath` — macOS
  caps a Unix socket path near 104 bytes and `t.TempDir()` under the pinned HOME
  overruns it.
- `internal/e2e/realclaude/fixtures.go` → `WithWorktreeAuthenticated`,
  `ensurePyryBuilt`; `permission_protocol_spike_test.go` → `captureClaudeVersion`,
  `versionSlug`, `packageDir`, `truncateString`, `stderrFixtureCap`;
  `permission_mode_switch_probe_test.go` → `modeSwitchNameToken`,
  `modeSwitchNamePattern`, `modeSwitchAnchor`, `modeSwitchFamilyPrefix`;
  `dropped_line_capture_test.go` → `dropcapFixedNeedles` — whose fixed deny
  classes include `/var/folders/` and `/private/var/folders/`, which is why this
  ticket redacts run-local temp paths out of what it commits.
- `internal/e2e/realclaude/offline_exec_ban_test.go` → `finOfflineExecBans` —
  per-file and keyed by filename. This ticket's file execs, so it takes no entry,
  exactly as `bypass_reescalation_probe_test.go` takes none.
- `docs/knowledge/features/set-permission-mode-inband-probe.md` — documents
  `-run TestRealClaude_SetPermissionMode` as #1595's reproduce filter, which is
  why this ticket's test name shares no prefix with it.
- `CLAUDE.md` § Testing — read the count of `=== RUN` lines, never the exit code;
  a test that writes a fixture does not commit it.

## Context

#1686 wants every claude child launched with `--dangerously-skip-permissions` and
downgraded in-band immediately, so no posture change ever needs a respawn.

#2060 landed the mechanism finding it rests on: **claude gates the escalation on
the LAUNCH ARGV, not on the session's current mode.** A child launched with the
flag can be dropped to `default` and asked back into `bypassPermissions` in the
same process, and both requests are accepted.

Two gaps remain, and both are about an argv no capture in this repo covers.
#1595's probe and #2060's after it deliberately spawned a **bare** argv — no
`--permission-prompt-tool`, no `--mcp-config` — because the #383 spike concluded
`--permission-prompt-tool stdio` short-circuits enforcement. Production's non-yolo
interactive spawn carries four flags neither probe had, composed by
`permissionArgs` and injected per-spawn by `withApprovalArgs`:

```
--permission-prompt-tool mcp__pyry_approve__approve
--mcp-config <path>
--strict-mcp-config
--permission-mode default
```

Every capture under `testdata/` is *either* bypass-with-no-approval-flags *or*
approval-flags-with-no-bypass. #1686 would put the bypass flag onto that same argv
for **every** session.

| # | Question | What a "no" means |
|---|---|---|
| Q2 | With the four approval flags **also** on the launch argv, does the child launch, and is it gated **through the approval bridge** after the downgrade? | The design removes the daemon's approval gate from every session. |
| Q3 | Can anything execute between launch and the downgrade landing? | The design starts children in bypass. #1686 says stop and report, do not mitigate. |

Either "no" closes #1686 as answered-no.

**Sizing, re-counted against this plan.** 0 production source files, 0 new
exported types, 0 consumer call sites forced to change (every rig knob is an
additive keyed field), 5 acceptance criteria — four of the six boundaries clear.
Total written work lands at roughly 1,400 lines (this note ~350, the measurement
file ~900, the rig widening ~160), which **exceeds the 800-line boundary**, and
the builder's independent count concurs with the refiner's ~1,500 estimate rather
than reducing it. Split depth is closed — parent #2059, grandparent #1686 — so the
ticket is refined in place under `needs-human:sizing` and built rather than cut.
The seam a split would have taken is Q2 / Q3: they share the argv, the socket and
the rig widening, and cutting there would duplicate all three across two children
while leaving neither able to state a verdict the other's controls supply.

**No ADR is warranted.** This ticket records two measurements; the decision they
feed is #1686's, and an ADR written here would pre-empt it. Where the run produces
a durable design note, its home is this file plus the probe file's header.

## Design

### The five children

One `TestRealClaude_BypassApprovalArgv_Probe`, five live children driven
sequentially through the rig's existing sequence. All five carry the four approval
flags; the socket, the mcp-config document and the pyry binary are built once and
shared.

| arm | launch flags | control request | position | primary read |
|---|---|---|---|---|
| `bypass_then_default` | bypass **+** the four | `default` | after turn 1 | turn 2 (Q2) |
| `approval_only` | the four | (none) | — | turn 2 (Q2 control); turn 1 (Q3 gated reference) |
| `prewrite_1` … `prewrite_3` | bypass **+** the four | `default` | **before turn 1** | turn 1 (Q3) |

Every arm drives **two** turns, so index symmetry holds: Q2 is classified at turn
2 on both the arm and its control, Q3 at turn 1 across all three references.
`setModePromptOne` / `setModePromptTwo` are reused verbatim, keeping the turns
byte-identical to #1595's, #2041's and #2060's.

The three `prewrite_*` arms are AC 3's "at least three consecutive spawns". They
differ from each other in name alone — that is the point, since Q3's subject is
whether the outcome is stable across spawns rather than a one-run coincidence.

### Q2's verdict is taken on the socket, not on the behaviour

AC 2 is the load-bearing constraint. A bypass-launched child whose bridge never
came back executes its tool ungated; a correctly bridged child whose approval was
*allowed* also executes ungated. Every field `setModeFieldMatches` compares reads
identically across those two, so a behavioural verdict would report the gate
intact in exactly the case where it is gone.

So the run records, per arm, **how many approval requests reached the stub socket
during each turn**, and takes Q2's verdict on the post-downgrade turn's count:

- arm's turn-2 count ≥ 1 → the bridge is back; the gate survives the combined argv.
- arm's turn-2 count == 0 **while the control's is ≥ 1** → the bridge did not come
  back. `#1686 removes the approval gate from every session` — a decisive no.
- both counts 0 → the instrument measured nothing (the control never consulted the
  socket either), reported as `setModeNoDiscrimination` rather than a verdict.

The behavioural comparison is recorded **beside** the verdict through the rig's
existing `setModeFieldMatches`, as corroboration, and never as the verdict. Its
per-field rows are what make a disagreement between the two reads legible.

### The stub answers DENY, unconditionally

`askQuestionCaptureServe`'s shape, for its reason plus one more: with a deny stub
a correctly-bridged post-downgrade turn is *gated* and a dead-bridge one is
*ungated*, so the behavioural read is informative here and the two reads can be
checked against each other. That does **not** license taking the verdict
behaviourally — the deny is a property of this stub, not of production, and AC 2's
allow case is the one #1686 would actually ship.

Deny is also what keeps a gated tool from executing on the `approval_only` arm.
The bypass arms execute `ls -la /` regardless; that is the posture under test.

The deny message is a fixed package constant, never derived from the request: a
message built from claude-supplied bytes would send them back out through a run
log this pipeline salvages.

### Q3's read, and the partition AC 3 requires

The `prewrite_*` arms write the downgrade **before any user turn** and wait for its
ack on the rig's existing `setModeControlBudget` before writing turn 1. Waiting is
deliberate: it is the case most favourable to #1686, so an ungated turn 1 after a
landed ack is a *real* race window rather than an artefact of not waiting.

Two outcomes, partitioned exactly as AC 3 names them, per spawn:

- **acked before turn 1, turn 1 still ungated** → a real race window. The design
  starts children in bypass and something can execute there.
- **no ack drew before turn 1 at all** → the design cannot write the downgrade
  that early; its "downgrade immediately" step has no landing point.

"Ungated" is read two ways and both are recorded: the socket count for turn 1
(zero requests ⇒ nothing consulted the bridge) and the behavioural comparison of
turn 1 against `bypass_then_default`'s turn 1 (bypass reference) and
`approval_only`'s turn 1 (approval-gated reference). Where those two references
are equal, `setModeNoDiscrimination` is reported for the behavioural half and the
socket half stands alone.

### Rig widening — four additive knobs, all inert at the zero value

Additive fields on the two existing carriers, following `secondTargetMode` /
`promptThree`'s precedent. No existing construction site changes; all three
current callers use keyed fields.

- `setModeArm.extraLaunchArgs []string` — appended to the argv after the
  `launchYOLO` branch. Nil on every existing arm.
- `setModeArm.requestBeforeFirstTurn bool` — moves the **first** control request
  ahead of turn 1. The existing send-and-wait block is lifted into a local closure
  called from one of two places; its body, its budget and its correlation
  recording are unchanged. #2060 moved the *second* request's position; this moves
  the first's.
- `setModeChildConfig.mark func(stage string)` — called after each completed step
  of the drive sequence with one of seven `setModeStage*` constants, so a caller
  can attribute out-of-band events the rig knows nothing about (an approval on a
  socket it never sees) to the turn that produced them. Nil-checked; nil on every
  existing caller.
- `setModeChildConfig.approval func() *bypassArgvApproval` — called once just
  before the record is built, and its result stored in a new nested pointer field
  on `setModeFixtureRecord`. A closure rather than a value because the observation
  is only complete after the child has run, which is what separates it from
  `autoMode`. The type is declared in this ticket's file, mirroring
  `modeSwitchAutoObservation`.
- `setModeChildConfig.redactRunLocal func(string) string` — applied to each
  recorded argv token and to the stderr capture before either is written. See
  § "What is committed" below.

### What is committed, and what is redacted

`writeSetModeFixture` is the only sanctioned writer and is called unchanged; a
sibling writer would be a second route to `packageDir` that eleven
`finOfflineExecBans` entries silently fail to cover.

The mcp-config path and the socket path are run-local temp paths. `/var/folders/`
and `/private/var/folders/` are two of `dropcapFixedNeedles`' five fixed deny
classes, so committing one cuts against this package's own grain, and a fresh
random path per run also makes the captures undiffable — in a ticket whose entire
subject is the argv. `redactRunLocal` replaces both with fixed placeholders in the
recorded argv and in the stderr capture, and never touches what is executed. The
claude binary path is left alone: every committed capture in this package already
carries it, and redacting it would break comparability with #2060's.

The approval log records the tool NAME (through `truncateString`, since it arrives
from a subprocess and nothing bounds it) and the arrival order. It records **no
tool input** — that is model-composed bytes, and `writeSetModeFixture` runs no
deny-scan.

### The fixture family and the name test

`bypass_approval_argv_v<slug>_<arm>.json`, minted by `bypassArgvFixtureName` from
a literal prefix plus `versionSlug` and `modeSwitchNameToken` — #2060's minter
shape, for its reasons (case preservation, path-metacharacter mapping).

`TestBypassApprovalArgv_FixtureNamesAvoidRegressionGlobs` ships beside it, reusing
`modeSwitchNamePattern` / `modeSwitchAnchor` / `setModeAdversarialVersionTokens`
rather than declaring a fourth copy. It covers the six families #2060's covers
**plus** `bypass_reescalation`, built from `reescalateFamilyPrefix` so a rename
follows rather than going vacuous, and it keeps #2060's four sub-tests: no minted
name joins a family, every pattern can still match its own control, every minted
path stays a plain component inside `testdata/`, and the arms mint distinct names.

Arms are constructed **inline** by a `bypassArgvArm(name)` switch, never appended
to `setModeArms` — that var is #1595's live measurement and an arm added there
would join it and mint into the colliding family.

### Launch failure vs refusal

`renderMCPApproveConfig`'s bad path — `--mcp-config` at a nonexistent file — makes
claude answer "Invalid MCP configuration" and exit 1, spending no tokens. The run
must not read that as a refusal. Two guards:

- The config is written by this test to a `t.TempDir()` at mode `0600` and the
  write error is fatal, so a missing file is an instrument failure by
  construction.
- Per arm, the record is checked for a `system/init` line before any verdict is
  computed. No init line ⇒ the arm is reported as a launch failure, naming the
  exit code and the (capped, redacted) stderr, and is excluded from the verdicts.
  `runSetModeChild` already fatals on an arm that produced zero stdout lines.

AC 1's "whether the child launches at all" is answered from these fields, which
are recorded for the arm and the control alike: `init_permission_modes` (the
`permissionMode` echo), the init line's `mcp_servers` array, `exit_code`, and the
stderr capture.

### `mcp_servers` — a new read on the init line

AC 1 asks the verdict to state what `system/init` reports for `permissionMode`
**and** `mcp_servers`. `setModeRecorder` records only `permissionMode` today. The
`mcp_servers` read is done in this ticket's file, over the committed
`stdout_events` the record already carries, rather than by widening the recorder:
the events are retained verbatim, so a second reader over them is a pure function
and adds no shared mutable state to a struct three measurements share.

## Concurrency model

Three goroutines, all joined before the test body returns.

- **The accept loop** — one goroutine over `ln.Accept()`, spawned before claude
  starts, serving each connection inline (`control.Approve` opens a fresh
  connection per approval, so a single accept would drop the second). Exits when
  `ln.Close()` makes `Accept` return an error. Registered through `t.Cleanup`, so
  a `t.Fatalf` anywhere still closes the listener and **joins** the goroutine — an
  un-joined server goroutine outliving the test is what `-race` reports.
- **The stdout reader** — the rig's own, unchanged: one goroutine per child,
  closing `readerDone` at EOF, joined by `runSetModeChild` after `cmd.Wait()`.
- **The test goroutine** — drives the sequence and reads the approval log.

The approval log is mutex-guarded: the accept loop appends while the test
goroutine snapshots at each `mark`. That is a race under `-race` without the lock.
Nothing on the accept loop calls `t.Fatalf` — from a non-test goroutine it does
not fail the test it was meant to fail; a decode or encode failure is `t.Logf`
plus a return, and the verdict switch on the test goroutine turns the resulting
silence into a finding.

One connection deadline per exchange (`askQuestionCaptureConnBudget`'s shape),
because a peer that died mid-frame would otherwise hold the accept loop's turn.

## Error handling

The rig's discipline, unchanged: `t.Fatalf` only for a broken **instrument**;
every other outcome is information and lands in a fixture field.

| Failure | Handling |
|---|---|
| No claude binary / no credentials | `t.Skip` through `resolveClaudeBin` / `WithWorktreeAuthenticated`, before anything is spawned |
| Socket bind fails | `t.Fatalf` naming the (test-owned, temporary) path — the only thing that makes EADDRINUSE or an over-long path diagnosable |
| mcp-config write fails | `t.Fatalf` — the instrument cannot be built |
| pyry build fails | `t.Fatalf` through `ensurePyryBuilt` |
| Child produced zero stdout | `t.Fatalf` in `runSetModeChild`, unchanged |
| Child emitted no `system/init` | Recorded; the arm is reported as a launch failure and excluded from the verdicts |
| No `control_response` to a request | Recorded as absence, drive continues (rig behaviour) |
| Turn never closed | `ResultObserved` false, recorded |
| Approval decode/encode error on the socket | `t.Logf` + return on that connection; counted in the arm's observation |
| Non-approval request on the socket | Answered with an error `control.Response` rather than dropped, so the peer gets a response instead of an EOF it fails closed on for a different reason; counted separately |
| Controls behave identically | `setModeNoDiscrimination` for that half of the read |
| Any recorded outcome | The test PASSES |

Credential discipline is inherited: `initControlScrubbed` already runs inside
`runSetModeChild` on the stderr string before any bytes are written or printed.

## Testing strategy

**Live (`e2e_realclaude`, five children, real tokens):**
`TestRealClaude_BypassApprovalArgv_Probe`. The name shares a prefix with neither
`TestRealClaude_SetPermissionMode` nor `TestRealClaude_InBandModeSwitch` nor
`TestRealClaude_BypassReescalation` — all three are documented reproduce filters
for runs that budgeted their own children.

**Deterministic (no claude, no credentials, no subprocess):**

- `TestBypassApprovalArgv_FixtureNamesAvoidRegressionGlobs` — four sub-tests as
  above, over the shared adversarial token list crossed with hostile arm tokens.
- `TestBypassArgvApprovalVerdict_*` — table-driven over the Q2 verdict function.
  The rows that matter are the ones AC 2 names: an arm whose turn-2 socket count
  is 0 while the control's is ≥ 1 must report the gate GONE, and must **not** be
  rescued by a behavioural comparison that happens to match; both counts 0 must
  report no-discrimination rather than either verdict.
- `TestBypassArgvPrewriteOutcome_*` — table-driven over Q3's partition: acked +
  ungated ⇒ race window; no ack ⇒ no landing point; acked + gated ⇒ the window
  closed on that spawn.
- `TestBypassArgvArms_CarryTheShapeTheVerdictsClassify` — #2060's
  arm-table-vs-direction-table pin: the measurement arm carries both the bypass
  flag and the four approval flags, the control carries the four alone and sends
  no request, the prewrite arms all set `requestBeforeFirstTurn`, and every arm a
  verdict names exists.
- `TestBypassArgvRedactRunLocal_*` — the redactor removes both run-local paths
  from an argv and from a stderr string, and leaves a string containing neither
  unchanged.

**Gate:** `go test -race ./internal/e2e/realclaude/...` cannot compile this file
(`make check` never compiles the tag), so the touched-scope gate is
`go vet -tags e2e_realclaude ./internal/e2e/realclaude/` plus
`go test -tags e2e_realclaude -run '<the deterministic tests>' ./internal/e2e/realclaude/`,
then the live run. **Read the count of `=== RUN` lines, never the exit code** — a
credential-less suite skips everything and exits 0, and a package that fails to
build runs zero tests and exits 0 through any shell wrapper.

Every arm's capture is `git add`ed in the same commit as the file that writes it.
A live run that spends tokens and lands no artifact does not satisfy this ticket.

## Open questions

1. **Does claude accept `--dangerously-skip-permissions` together with
   `--permission-mode default` on one argv?** `permissionArgs` never emits both,
   so no capture covers it. A refusal at launch is itself a decisive "no" for
   #1686 and is recorded as such via the no-init-line branch. Resolve in Phase B
   from the run.
2. **Is `mcp_servers` the init line's actual key?** AC 1 names it. If the key is
   spelled differently at 2.1.239, the reader records the absence and the run
   reports the raw init line instead of inventing a field. Resolve from the first
   capture, and record the resolution here.
3. **Do the three `prewrite_*` spawns agree?** If they disagree the window is
   nondeterministic, which is a stronger finding than either uniform outcome. The
   verdict reports the per-spawn partition and the tally, never a majority vote.

Each is resolved in Phase B and any that changes the design gets a `## Revisions`
entry.

## Revisions

### 2026-09-03 — the live run, and the three open questions resolved

The measurement ran: five children, 52 s, claude 2.1.239, model `claude-haiku-4-5`,
every arm exit 0 with no tripped deadline and empty stderr. Both verdicts are
recorded in the probe file's header and every capture is committed under
`internal/e2e/realclaude/testdata/bypass_approval_argv_v2.1.239_*.json`.

- **Q2 — APPROVAL GATE INTACT.** The combined argv launches, `mcp_servers` reports
  `[{"name":"pyry_approve","status":"connected"}]` even while the session is in
  bypass, and the post-downgrade turn reached the stub socket once where the
  in-bypass turn reached it zero times. The bridge comes back.
- **Q3 — RACE WINDOW CLOSED** on all three spawns, with no disagreement. Every
  prewrite child drew its ack before turn 1 and was gated on turn 1.

**Open Question 1 — resolved: yes.** claude accepts `--dangerously-skip-permissions`
beside `--permission-mode default` on one argv, and comes up in
`bypassPermissions`. The no-init-line launch-failure branch did not fire.

**Open Question 2 — resolved: `mcp_servers` is the key**, spelled as an array of
objects carrying `name` and `status`. `bypassArgvInitMCPServers` records it verbatim
rather than decoding it, so the design did not depend on the answer and does not
change now that it is known.

**Open Question 3 — resolved: the three spawns agree**, so the per-spawn tally
reports one uniform outcome and the disagreement branch did not fire. It stays in
the code: a nondeterministic window would be a stronger finding than a uniform one,
and one run agreeing is not evidence that a later one will.

**One design point the run added, no code change.** The redactor covers the argv and
the stderr capture — the two places this ticket puts a run-local path — and NOT
claude's own `cwd` echo inside `system/init`, which lands in `stdout_events`. Every
committed capture in this package already carries that path (fifteen occurrences in
#2060's `enable` arm, six in #1595's `revoke`), and `stdout_events` is a verbatim
recording whose value is that nothing rewrites it. Recorded in the probe header so
the redactor is not read as a guarantee it does not make.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** Two, both explicit. (a) claude's stdout → this process:
  crossed only inside `setModeRecorder.add`, which retains every line verbatim and
  encodes an unparseable one as a JSON *string* so the artifact stays parseable;
  the `mcp_servers` reader added here is a pure function over those already-retained
  bytes and adds no second parse site. (b) The stub socket → this process: crossed
  in one function, `bypassArgvServe`, which decodes exactly `control.Request` and
  reads exactly `ToolName` off it. Nothing downstream holds claude-supplied bytes
  in a trusted position: the verdicts are computed from **counts** and from
  `probeOutcome`, never from a subprocess-supplied string.
- **[Tokens, secrets, credentials]** No new secret is minted, stored or rotated;
  the request ids are correlation tokens minted from the arm name, matching
  `(*Runner).Interrupt`'s monotonic-counter rationale. The credential reaches the
  child through the environment (`WithWorktreeAuthenticated`) and never through
  the argv, which is what makes recording the argv safe. `initControlScrubbed`
  already runs on the stderr string inside `runSetModeChild` **before** any bytes
  are written or printed, and this ticket adds no path that writes stderr earlier.
  `redactRunLocal` runs on top of it, never instead of it.
- **[File operations]** The mcp-config is written to a fresh `t.TempDir()` at mode
  `0600` — it is not secret, but it is an execution instruction claude obeys, and
  a world-writable one in a shared temp directory is a local-privilege footgun.
  No path is built from any external input: the fixture path comes from
  `bypassArgvFixtureName`, whose version token goes through `versionSlug` and whose
  arm token goes through `modeSwitchNameToken`, both of which map every path
  metacharacter and are asserted to stay inside `testdata/` over `..`, `../..`,
  `a/b` and `/abs`. `writeSetModeFixture`'s temp-file-plus-rename is reused
  unchanged, so an interrupted run cannot leave a half-written capture for a later
  commit. No `os.Stat`-then-open anywhere, so no TOCTOU gap.
- **[Subprocess execution]** Two spawns. claude: `exec.CommandContext` with an
  argv slice, no `sh -c`; every element is a package constant or a path this test
  minted, and `extraLaunchArgs` is populated only from this file's own literals —
  no external value reaches the argv. pyry: forked by claude as
  `pyry mcp-approve -pyry-socket <path>` from the transcribed config, with both the
  binary and the socket absolute. `--strict-mcp-config` is **non-negotiable and
  is on every arm**: without it a project or user `.mcp.json` can register a second
  `pyry_approve` server that shadows this one and answers *allow*, which would make
  Q2's socket read report a bridge that was never consulted. `cmd.Env` stays nil so
  the child inherits the environment `WithWorktreeAuthenticated` has already pinned
  (HOME to the worktree); `buildEnvWithRealHome` is deliberately not used — handing
  claude the operator's real HOME would defeat the worktree isolation. Kill path is
  the rig's `exec.CommandContext` deadline (`setModeChildBudget`) plus the graceful
  stdin close, both unchanged.
- **[Cryptographic primitives]** Not applicable — no randomness, no comparison
  against a secret, no key material anywhere in the design. The one comparison
  against an identifier (`setModeResponseIDMatches`) is over a locally-minted
  correlation token, not a credential, so constant-time comparison would be
  cargo-cult here.
- **[Network & I/O]** The socket is a Unix socket in a `0700`-parented
  `os.MkdirTemp` under `/tmp`, reachable only by this user; the peer is
  `pyry mcp-approve` on this machine as this user, so this is a liveness boundary
  rather than an adversarial one. Bounds are nonetheless explicit: a per-exchange
  `SetDeadline`, a whole-child `context.WithTimeout`, a 1 MiB scanner cap on
  claude's stdout with an over-long line landing in `scanner_error` rather than
  being silently truncated, and `stderrFixtureCap` on the stderr capture. The
  approval decoder reads one `control.Request` per connection and closes.
  **SHOULD FIX:** the approval log is an unbounded slice fed by a subprocess. A
  claude that retried a tool in a loop would grow it for the life of one child.
  Phase B caps the retained entries and records the overflow **count**, so the
  verdict (a count comparison) stays correct past the cap.
- **[Error messages, logs, telemetry]** The deny message is a fixed package
  constant and never derived from the request — a message built from
  claude-supplied bytes would be echoed straight back out through a run log this
  pipeline salvages. The tool name is the one subprocess-supplied string that
  reaches a log line or the artifact, and it goes through `truncateString`
  (`reescalateModeQuoteCap`'s rationale). **The approval log records no tool
  input**: that is model-composed bytes, `writeSetModeFixture` runs no deny-scan,
  and #1938 needed a whole `dropcapScanner` to commit such a payload safely. The
  record is never printed with `%v`/`%+v`. `redactRunLocal` keeps two run-local
  temp paths out of the committed argv and stderr, matching two of
  `dropcapFixedNeedles`' fixed deny classes.
- **[Concurrency]** One lock, held by one type (the approval log), never held
  across a `Write`, an `Encode` or a `t.Logf`, so there is no ordering to document
  and no lock-inversion to construct. Goroutine lifecycle is exhaustive: the accept
  loop exits on `ln.Close()` and is **joined** in the same `t.Cleanup` that closes
  the listener, so a `t.Fatalf` on any branch still joins it; the reader goroutine
  is the rig's, joined after `cmd.Wait()`. No `t.Fatalf` from a non-test goroutine.
  Mid-write interruption leaves no partial artifact — the writer renames.
- **[Threat model alignment]** No relay, no mobile transport, no device identity,
  so `docs/protocol-mobile.md` § Security model has no applicable threat. The one
  CLI-relevant threat this design *touches* is the approval-bridge bypass, and
  touching it is the entire point: this ticket **measures** whether #1686's argv
  would remove the gate and **changes no daemon behaviour**. The structural
  fail-safe #2060's header names is untouched — an in-band escalation is
  unreachable from pyry's own surface because `permissionModeAllowed` refuses
  `bypassPermissions` by non-membership, and that allow-list is production code
  this ticket does not modify. **OUT OF SCOPE:** whatever mitigation Q3's window
  turns out to need. #1686 says stop and report, do not mitigate, and #1686 is
  where it is picked up.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-03
