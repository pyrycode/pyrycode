# #2041 — measure whether `acceptEdits`, `dontAsk`, `plan` and `auto` switch a running child in-band at 2.1.239

## Files read

- `internal/e2e/realclaude/set_permission_mode_probe_test.go` → `runSetModeChild`, `setModeArm`,
  `setModeFixtureRecord`, `writeSetModeFixture`, `setModeFixtureName`, `setModeRecorder`,
  `setModeControlLine`, `setModeTurnLine`, `setModeWaitFor`, `setModeTurnWindows`,
  `TestRealClaude_SetPermissionMode_FixtureNamesAvoidRegressionGlobs` — the drive sequence and
  recorder this ticket reuses, and the model constant it must displace.
- `internal/e2e/realclaude/initialize_control_probe_test.go` → `initControlLine` (reused
  verbatim), `initControlSummarize` (the three-placement rule for finding `models`, and the
  record that the payload also carries `account`), `initControlScrubbed`, `initControlModel`,
  `initControlPromptOne` — whose doc block is why this probe's turns are tool-free.
- `internal/e2e/realclaude/inband_bypass_revoke_names_test.go` → `setModeFamilyGlob`,
  `poolRevokeNamePattern`, `anchorFixtureName`, `hostileArms` — the three-glob table with
  per-row anchoring and per-row controls, the pattern AC 3's offline test copies.
- `internal/e2e/realclaude/permission_protocol_regression_test.go` → `fixtureGlob`, and
  `internal/e2e/realclaude/dropped_line_capture_test.go` → `dropcapFixtureGlob` — the two
  other families a capture must not join.
- `internal/e2e/realclaude/permission_protocol_spike_test.go` → `captureClaudeVersion`,
  `versionSlug`, `packageDir`, `truncateString`, `stderrFixtureCap` — shared helpers.
- `internal/e2e/realclaude/offline_exec_ban_test.go` → `finOfflineExecBans` — read to confirm
  no entry needs adding: this ticket adds no file whose header claims offline purity, and no
  new `packageDir` wrapper an existing entry would have to learn (see "no new writer symbol").
- `internal/e2e/realclaude/testdata/initialize_control_v2.1.239.json` → the committed `models`
  array. Confirms the entry key is `value`, and that `supportsAutoMode` is published `true` on
  `default` / `sonnet` / `opus` / `claude-fable-5[1m]` and **absent** on the two haiku rows.
- `docs/knowledge/features/set-permission-mode-inband-probe.md` — #1595's finding doc; its
  reproduce command filters on `-run TestRealClaude_SetPermissionMode`, which is why this
  ticket's tests take a different name prefix.
- `docs/knowledge/features/e2e-realclaude.md`, `CODING-STYLE.md` — package conventions.

## Sizing

Re-counted against this written plan: **~950 lines of total written work** (this plan plus an
estimated ~580 of test code), against the size-S ceiling of 800. Zero production source files
— everything lands in `*_test.go` and this plan. The other four boundaries are clear: no new
exported types, two call sites needing simultaneous update (`runSetModeChild` and
`writeSetModeFixture` have one caller each), five acceptance criteria, no state machine.

The overage is stated rather than split on, because **the floor rule binds and outranks the
ceiling**. Every slice this work decomposes into has exactly one consumer inside the family:
the runner generalisation is consumed only by the probe; the discovery step is consumed only
by the arm table; the offline name and predicate tests exist only to protect captures the
same ticket writes. There is one deliverable here — a live measurement of four modes,
recorded — and a child that cannot be verified without its sibling is not a ticket. The
security-review section this label mandates accounts for about a third of the overage on its
own, and it is not optional.

## Context

#1687 is scoped from an operator claim, taken 2026-08-21 against claude 2.1.220, that
`acceptEdits`, `dontAsk`, `plan` and `auto` all switch on a running child. Nothing in this
repo measures that. The `permission_protocol_v2.1.143_*` captures that look like they do are
launch-argv measurements from #383; #1595's committed captures cover only `default` and
`bypassPermissions`. The installed claude is 2.1.239.

This ticket measures and records. It adds no production writer and changes no production
behaviour — the same boundary #1595 held. Its output is five committed captures plus a
`# The finding` block, and #1687 and #1686 read that finding.

No ADR is warranted: this ships no decision, only evidence.

## Design

### Shape

One new test file, `internal/e2e/realclaude/permission_mode_switch_probe_test.go`, plus a
narrow generalisation of #1595's runner so the drive sequence can be reused rather than
copied.

### The problem with reusing `runSetModeChild` unchanged

That runner hardcodes four things this ticket must vary: the model (`setModeModel`, which
is `claude-haiku-4-5` — one of the two rows publishing no `supportsAutoMode`), the two probe
prompts (Bash-driving, which this read does not need), the turn bound, and the fixture family
its writer mints into (`setModeFamilyGlob`, which AC 3 forbids for these captures).

The generalisation is one new parameter carrying exactly those, and one new observation
field:

```go
// set_permission_mode_probe_test.go
type setModeChildConfig struct {
	model       string
	promptOne   string
	promptTwo   string
	maxTurns    string
	fixturePath func(t *testing.T, versionToken, arm string) string
	autoMode    *modeSwitchAutoObservation // nil on #1595's arms
}

func runSetModeChild(t *testing.T, claudeBin, workdir string, arm setModeArm,
	versionRaw, versionToken string, cfg setModeChildConfig) *setModeFixtureRecord
func writeSetModeFixture(t *testing.T, rec *setModeFixtureRecord,
	fixturePath func(t *testing.T, versionToken, arm string) string) string
```

**`setModeFixtureName` and `setModeFixturePath` keep their signatures**, deliberately:
`inband_bypass_revoke_names_test.go` calls the namer twice as its own control set, and
changing it there would edit a file this ticket has no business in. Only `writeSetModeFixture`
and `runSetModeChild` change shape, and each has exactly one caller.

**No new writer symbol.** The path minter is a parameter of the existing
`writeSetModeFixture` rather than a new sibling function, because that name is on eleven
`finOfflineExecBans` lists as a banned identifier; a fresh `writeModeSwitchFixture` would be
a second route to `packageDir` that every one of those entries silently fails to cover.

`setModeFixtureRecord` gains two fields — `Model string` (always written; #1595's re-runs
record `claude-haiku-4-5` where they previously recorded nothing) and
`ModeSwitchAuto *modeSwitchAutoObservation` with `omitempty`, nil on #1595's arms.

### The auto-mode observation, and why absence is not `false`

```go
type modeSwitchAutoObservation struct {
	ModelListRequestID string               `json:"model_list_request_id"`
	RowPresent         bool                 `json:"row_present"`
	SupportsAutoMode   bool                 `json:"supports_auto_mode"`
	Published          []modeSwitchAutoRow  `json:"published"`
}
type modeSwitchAutoRow struct {
	Value            string `json:"value"`
	SupportsAutoMode bool   `json:"supports_auto_mode"`
	KeyPresent       bool   `json:"key_present"`
}
```

The inner fields carry **no** `omitempty`: `supports_auto_mode` is exactly the field whose
`false` is spelled by absence in claude's own reply, and a capture that omitted it would
reproduce the trap the ticket names. `RowPresent` separates "the list carries this model and
it publishes false" from "the list does not carry this model at all"; `KeyPresent` separates
"published `false`" from "key absent", which claude's 2.1.239 list only ever does the second
way.

### Live model selection, not a configured model

`runModeSwitchDiscovery` spawns ONE extra child — `--model claude-haiku-4-5`, `--max-turns 1`,
no probe turns, therefore no assistant tokens — writes `initControlLine` on its held-open
stdin, waits for one `control_response`, closes stdin. `modeSwitchAutoRows` reads the `models`
array out of the reply at the same three placements `initControlSummarize` documents (top
level, under `response`, under `response.response`) and returns one row per entry.

**The raw `initialize` reply is neither recorded nor logged.** `initControlSummarize`'s doc
block records that this payload carries `account` and `pid` alongside `models`, and these
captures are committed to a public repo. Only the derived `value` / `supportsAutoMode` /
`key_present` triples cross out of `runModeSwitchDiscovery`; the raw bytes stay in the
child's pipe. #1688's committed captures are where the reply's full shape already lives.

The model list is a property of the binary and the account, not of the model answering a turn
— `initControlModel`'s doc block states this — so one discovery serves all five arms.

Selection is a preference list intersected with the live rows, never a bare constant:

- auto-capable: first of `sonnet`, `default`, `claude-fable-5[1m]`, `opus` that the run
  observed publishing `true`; otherwise the first `true` row in arrival order.
- auto-incapable: first of `claude-haiku-4-5`, `haiku` observed publishing `false`;
  otherwise the first `false` row in arrival order.

**Every candidate passes `modeSwitchModelValueOK` before it can be selected.** This is the
one place in the design where a value read out of one subprocess's stdout becomes an argv
element of another subprocess, and the hazard is not shell quoting — `exec.CommandContext`
takes an arg slice and no shell is involved. It is that a value beginning with `-` is a
*flag* to claude's own parser: a row whose `value` read `--dangerously-skip-permissions`
would turn `--model <value>` into a flagless `--model` followed by a bypass flag, launching
the child in the one posture this probe assumes it is not in and recording a finding that is
false in exactly the direction #1687 would act on. The predicate is deliberately narrow —
non-empty, at most 64 bytes, no leading `-`, and every byte in `[A-Za-z0-9._:@/+[\]-]`,
which admits every value the 2.1.239 list publishes including `claude-fable-5[1m]`. A row
that fails it is skipped during selection and recorded in `published` like any other, so the
rejection is visible rather than silent.

This is what makes the Technical Notes' failure impossible rather than merely unlikely: an
`auto` refusal can never be recorded against a model that never supported auto, because the
model was chosen from the run's own list.

Where the list is present but holds no `true` row (or no `false` row), that is a **finding**:
the affected arm is not driven, and the run logs and records why. It is not an instrument
failure — the instrument measured a list.

### The five arms

All five: `launchYOLO: false` (pyry's stream argv carries no `--dangerously-skip-permissions`,
no `--permission-prompt-tool`, no `--allowed-tools`), the same two prompts, the same
`--max-turns`, one `set_permission_mode` control request between turn 1 and turn 2.

| arm | `targetMode` | model |
|---|---|---|
| `acceptEdits` | `acceptEdits` | the auto-capable model |
| `dontAsk` | `dontAsk` | the auto-capable model |
| `plan` | `plan` | the auto-capable model |
| `auto` | `auto` | the auto-capable model |
| `auto_unsupported` | `auto` | the auto-incapable model |

`auto_unsupported` differs from `auto` **only** in the model, which is AC 2's requirement and
the reason the model is an arm property rather than a package constant.

The first three run on the auto-capable model too, so all five arms differ in at most the two
dimensions under study. The one thing that would confound the two auto arms is a model
difference elsewhere in the table, and there is none.

### Tool-free probe turns

`initControlPromptOne`'s doc block says not to copy `runSetModeChild`'s Bash probes into a
read that does not need them: a tool-free turn cannot stall on an approval it can never
receive, and these arms launch in `default` posture with no bypass flag. This read is the ack
plus the next turn's `init.permissionMode`, so behaviour is not measured and Bash buys
nothing but stall risk and unsandboxed tool access. Prompts differ from each other so turn 2
is a fresh request rather than one claude answers with "I already did that".

Per the Technical Notes, **no control arms**. #1595 needed them because "the tool still ran"
is not evidence about a bypass change; here the ack and the init echo answer directly.

### Fixture family

`modeSwitchFixtureName(versionToken, arm) = "permission_mode_switch_v" + versionSlug(token) +
"_" + arm + ".json"`, under `testdata/`. It is a new family: the prefix shares no literal head
with `permission_protocol_v` (`fixtureGlob`), `dropped_lines_v` (`dropcapFixtureGlob`) or
`set_permission_mode_v` (`setModeFamilyGlob`), and `filepath.Match`'s `*` does not cross a
separator.

### Failure vs finding

`t.Fatalf`/`t.Errorf` only where the instrument measured nothing (AC 4):

- discovery produced no `control_response`, or no `models` array → `t.Fatalf` before any arm
  spends a token;
- an arm produced no stdout at all, or failed to spawn → `runSetModeChild` already fatals;
- an arm produced **zero** `control_response`s, or **zero** `system`/`init` lines →
  `t.Errorf` in that arm's subtest, **after** its fixture is written, so the evidence lands
  and the remaining arms still run.

Every other outcome — a `subtype:"error"` reply, an unchanged `init.permissionMode`, a
mismatched `request_id` — is a recorded finding and the probe passes.

## Concurrency model

Unchanged from `runSetModeChild`: one child at a time, one reader goroutine per child over
`cmd.StdoutPipe()`, closed over `readerDone` before the record is built; no `t.Parallel` on
the live tests, since all arms share one pinned `$HOME`. `runModeSwitchDiscovery` uses the
same single-goroutine shape and joins it the same way, so no goroutine outlives its child.
`setModeRecorder`'s mutex already guards the reader-appends / test-polls race under `-race`.

The offline names test is `t.Parallel` and touches nothing shared.

## Error handling

- Discovery: a spawn error, a marshal error or an absent model list is fatal (instrument).
- Arms: every claude-side outcome lands in a fixture field. `context_deadline_tripped`,
  `scanner_error`, `stdin_write_errors`, `wait_error` and `exit_code` are recorded as
  #1595's driver already records them.
- `setModeChildBudget` (4 min) is below the worst-case sum of the per-step waits (2 min + 45 s
  + 2 min). That is #1595's existing property, inherited unchanged and deliberately not
  altered here — a tool-free turn lands in seconds, and a deadline trip is recorded in the
  fixture either way. Out of scope to retune; `initControlChildBudget` is the shape that
  fixed it for its own family.
- Credentials never reach the argv (they arrive through the environment via
  `WithWorktreeAuthenticated`), stderr is capped at `stderrFixtureCap` before it is recorded,
  and the discovery record carries no stderr at all.
- **`runSetModeChild` calls `initControlScrubbed` on the raw stderr before writing the
  fixture.** A cap is not a redaction: an auth failure is exactly the condition that makes
  claude print a long message to stderr, and 8 KiB of a credential-bearing message still
  commits the credential. `initialize_control_probe_test.go` added that guard for its own
  family after #1595 shipped, so #1595's driver never got it; these five captures are new
  public files carrying claude stderr, and adding the one call covers both families at once.
  It fatals before any bytes are written, and it is inert when neither credential variable is
  set, so it cannot misfire on a machine with no token.

## Testing strategy

- `TestRealClaude_InBandModeSwitch_Probe` — the live probe. Distinct name prefix from
  `TestRealClaude_SetPermissionMode` so #1595's documented reproduce command does not sweep
  five more children into a run that budgeted four.
- `TestRealClaude_InBandModeSwitch_FixtureNamesAvoidRegressionGlobs` — deterministic, no
  child, no credentials. Over the adversarial version tokens
  `TestRealClaude_SetPermissionMode_FixtureNamesAvoidRegressionGlobs` uses — hoisted into a
  shared `setModeAdversarialVersionTokens` var so "the same tokens" is structural rather than
  a copied literal — crossed with all five arm names, it asserts:
  - no minted name matches `fixtureGlob`, `dropcapFixtureGlob` or `setModeFamilyGlob`, each
    anchored the way its owner evaluates it (the first two against a `testdata/`-prefixed
    subject, the third against the base name);
  - **per-glob controls**, so no row is vacuous: a synthetic `permission_protocol_v0_x.json`,
    `dropped_lines_v0.json` and `set_permission_mode_v0_x.json` must each still match their
    glob. Without these the three negative assertions pass identically against a typo'd
    pattern constant;
  - every minted path stays a plain component directly inside `testdata/` (the `..` tokens
    survive `versionSlug`, which maps `/` to `_` but leaves `.` alone);
  - the five arm names mint five distinct filenames.

  The arm dimension carries **hostile literals** alongside the five real names — `a/b`,
  `..`, `/abs`, `""`, `set_permission_mode` — the way `inband_bypass_revoke_names_test.go`
  carries `hostileArms`. The version token is slugged and the arm token is not, so the arm
  is the half a future contributor can walk out of `testdata/` by typing a string into the
  arm table.
- `TestModeSwitchModelValueOK_RejectsAFlagShapedModelValue` — deterministic, table-driven
  over the predicate: the 2.1.239 values (`claude-fable-5[1m]` included) accepted, and
  `--dangerously-skip-permissions`, `-x`, `""`, a 65-byte value and an embedded space
  rejected. This is the guard from the security review, and it is the half that can be
  proven without a live child.
- Verification: `go vet -tags e2e_realclaude ./internal/e2e/realclaude/` and
  `go test -tags e2e_realclaude -race -run TestRealClaude_InBandModeSwitch_FixtureNames
  ./internal/e2e/realclaude/` for the offline half; `go build ./cmd/pyry` and
  `go test -race ./internal/e2e/...` for the untagged tree. The live half is the dispatcher's
  `needs-real-claude` gate.
- Read the `=== RUN` count, never the exit code: a build failure and an unauthenticated run
  both exit 0.
- **The captures must be `git add`ed by the run that writes them.** A pipeline run's worktree
  is removed at the end, so a green live gate that commits nothing lands nothing (#1763).

## Open questions

1. **Does the discovery child's `initialize` reply carry `models` before any turn?** #1688's
   `initialize_control_v2.1.239_before_first_turn.json` says yes at this version. If a live
   run disagrees, the fallback is to drive one tool-free turn first — recorded as a revision.
2. **Does `--model claude-fable-5[1m]` survive argv passing?** Only reachable if neither
   `sonnet` nor `default` publishes `true`. No shell is involved, so brackets are literal;
   noted so a surprise here is read as expected rather than as a probe bug.
3. **What does claude return for `dontAsk` at 2.1.239?** `dontAsk` appears in no committed
   capture in this repo. If it is rejected as an unknown mode rather than as an unsupported
   one, that is the finding, and #1687's vocabulary drops the row.

## Security review

**Verdict:** PASS (after one MUST FIX was addressed in this plan, before the plan commit).

**Findings:**

- **[Trust boundaries] MUST FIX — addressed above.** The design has one boundary that the
  first draft did not name: `modeSwitchAutoRows` lifts `models[].value` out of a child's
  stdout and the selector hands it to the *next* child as an argv element. Untrusted →
  trusted, crossed implicitly and in a place a reader would not look. It is now explicit and
  single-sited: `modeSwitchModelValueOK` is the only gate a value passes through on the way
  to `--model`, and selection consults nothing else. Everything else the probe reads from
  stdout stays data — recorded into a fixture, compared, or logged, never executed.
- **[Subprocess execution] MUST FIX — same finding, the exploit half.** No shell is involved
  (`exec.CommandContext` with an arg slice), so this is not shell injection. It is flag
  injection: a `value` beginning with `-` is parsed as a *flag*, and
  `--model --dangerously-skip-permissions` plausibly parses as a valueless `--model` plus a
  bypass flag. That silently inverts the launch posture every arm's finding is read against
  — the probe would pass and say something false, which is the precise failure the ticket's
  Technical Notes already warn about in its other form. The leading-`-` rejection in
  `modeSwitchModelValueOK` is what closes it; the 64-byte and charset bounds close the
  adjacent argv-shape surprises. Environment is inherited deliberately (that is how the
  child authenticates) and is never copied into a record. `cmd.Wait` plus the context
  deadline are the kill path; claude's own grandchildren are out of scope here as in #1595.
- **[Tokens, secrets, credentials] SHOULD FIX — addressed above.** No token is minted:
  `request_id` is a fixed per-arm correlation literal, deliberately not `crypto/rand`,
  because it correlates a reply on a pipe this process owns and a random one would rewrite
  the committed fixture on every run. The real exposure is the committed artifact:
  `runSetModeChild` records claude stderr and #1595's family never received the
  `initControlScrubbed` guard its sibling family added. The plan now calls it before the
  write. Argv is recorded and is credential-free by construction; env is not recorded at all.
- **[Error messages, logs, telemetry] No findings, by a design decision worth stating.** The
  `initialize` reply carries `account` and `pid` beside `models`. Nothing records or logs it:
  only the derived `value` / `supportsAutoMode` / `key_present` triples leave the discovery,
  and the arms' own `set_permission_mode` replies (small, account-free at 2.1.239 per
  #1595's captures) are the only verbatim bytes that reach a log or a file.
- **[File operations] SHOULD FIX — addressed above.** No traversal is reachable: `versionSlug`
  maps `/` to `_`, and `..` survives only as a plain component. The arm token is *not*
  slugged, which is the untested half — hence the hostile arm literals and the containment
  assertion in the names test. Writes stay temp-file-plus-rename, `0644` for a public
  artifact, `0700` for the workdir. No check-then-use on any caller-controlled path, so no
  TOCTOU; `packageDir` is `os.Getwd()` and follows nothing.
- **[Network & I/O] No findings.** No sockets and no server. Every read is bounded:
  `setModeScanMax` caps a line at 1 MiB and an overlong one lands in `scanner_error` rather
  than truncating silently; `setModeTurnBudget`, `setModeControlBudget` and
  `setModeChildBudget` bound the waits; six children run sequentially, each under a context
  deadline.
- **[Cryptographic primitives] Not applicable by design** — the ticket adds no primitive, no
  key and no comparison against a secret. The one guard that compares against a credential
  (`initControlScrubbed`) uses plain `strings.Contains` on purpose: constant-time comparison
  defends a secret from a party that does not know it, and the only other party here is the
  binary that was handed the token.
- **[Concurrency] No findings.** One reader goroutine per child, joined on `readerDone`
  before the record is built, so none outlives its child; `setModeRecorder`'s mutex already
  covers the reader-appends / test-polls race; `scannerErr` is written before the close and
  read after it. No lock is taken twice and no new shared state is introduced. The live
  tests take no `t.Parallel` because the arms share one pinned `$HOME`.
- **[Threat model alignment] OUT OF SCOPE, named.** This ticket ships no production writer
  and no wire vocabulary, so `docs/protocol-mobile.md` § Security model is untouched. The
  decision this measurement feeds — whether a client may send a permission mode at all, and
  which — belongs to #1687, and #1686 owns the bypass row's dependency on the launch argv.
  The `revoke` caveat #1595 recorded (anything that can write a child's stdin can drop its
  bypass posture mid-session) is unchanged by this ticket and is not re-litigated here.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-02
</content>
</invoke>

## Revisions

### 2026-09-02 — the arm token needed sanitising, and the namer grew a helper

**What changed:** `modeSwitchFixtureName` now passes the arm through
`modeSwitchNameToken` before joining it into the filename. The plan specified sanitising for
the version token only (via `versionSlug`), and treated the arm token as safe because the arm
names are compile-time constants.

**What drove it:** the hostile arm literals the security review's file-operations finding
added to the names test — `a/b`, `/abs`, `../..` — went red on first run. A namer asked for
`a/b` minted a path resolving to `testdata/permission_mode_switch_v…_a/b.json`, outside the
one directory the writer creates. The security review predicted the exposure in prose ("the
arm token is *not* slugged, which is the untested half") and then the plan left the namer
unchanged; the test is what turned the prediction into a red.

**The new contract:** `modeSwitchNameToken` maps every byte outside `[A-Za-z0-9_-]` to `_` and
caps at 32, preserving case. It is deliberately not `versionSlug`, which lowercases and would
mint `…_acceptedits.json`, costing the reader the token that says which mode a capture is
about. Sanitising rather than rejecting: containment becomes a property of the name, so no
caller has to validate first.

### 2026-09-02 — the auto-capable preference list carried a stale value

**What changed:** `modeSwitchAutoCapablePreference` names `claude-fable-5-1[1m]` where the
plan and the ticket both say `claude-fable-5[1m]`.

**What drove it:** the live 2.1.239 model list published `claude-fable-5-1[1m]`, while the
committed `initialize_control_v2.1.239.json` records `claude-fable-5[1m]`. Same binary
version, drifted value. Nothing depended on it — `sonnet` was selected — and the entry is an
ordering hint whose fallback takes the first qualifying row in arrival order, so a name that
no longer exists costs nothing. It is corrected and recorded because it is the concrete
evidence for the design's central choice: select the model out of the run's own list, never
out of a table. `modeSwitchModelValueOK`'s table now pins both spellings.
