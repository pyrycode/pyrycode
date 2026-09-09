# #2251 — capture the session-start line with an effort actually set

## Files read

- `internal/streamsup/parser.go` → `systemInitLine` — the one-field decode target this family
  feeds; its doc states the rule (field mapping comes from a committed capture) and enumerates the
  22 keys the 2.1.239 line carried.
- `internal/e2e/realclaude/set_permission_mode_probe_test.go` → `setModeArm`,
  `setModeChildConfig`, `runSetModeChild`, `setModeFixtureRecord`, `writeSetModeFixture` — the
  rig this ticket rides. `extraLaunchArgs` is the argv seam, `promptOne`/`promptTwo`/`promptThree`
  the turn seam, and `writeSetModeFixture`'s doc is the reason no second writer is added here.
- `internal/e2e/realclaude/bypass_approval_argv_probe_test.go` → `bypassArgvApprovalArgs`,
  `bypassArgvArm`, `bypassArgvSocket`, `bypassArgvServe`, `bypassArgvRedactor`,
  `bypassArgvInitMCPServers` — the production approval-argv shape, its stub socket and its
  argv redactor, all reused verbatim.
- `internal/e2e/realclaude/dropped_line_capture_test.go` → `newDropcapRedactor`,
  `dropcapRedactor.add`, `dropcapPathSpellings`, `newDropcapScanner`,
  `dropcapScanner.addDynamicPath`, `dropcapWriteRecord`, `dropcapLocateHits` — the redaction and
  deny-scan lineage AC 5 names, and the "hits print class names, never values, and nothing is
  written" discipline this ticket copies.
- `internal/e2e/realclaude/compaction_capture_test.go` → `TestRealClaude_CompactionCapture` —
  the fixture-absence gate, the external artifact directory that outlives a discarded worktree,
  and the `newDropcapScanner`-after-`WithWorktreeAuthenticated` ordering rule.
- `internal/streamsup/compaction_capture_test.go` → `compactionReaderGate`,
  `TestRealClaudeCompactionCaptureShapesArePinned` — the four-quadrant (fixture, pin) state
  machine with exactly one legal skip, and the re-derive-from-the-line's-own-bytes rule.
- `internal/streamsup/initialize_capture_test.go` → `capturedInitialize`, `initCaptureRecord` —
  the version-pinned reader over committed captures, and the accepted-risk argument for a
  parallel struct over a record whose authoritative type sits behind a build tag.
- `internal/e2e/realclaude/interactive_stream_inband_model_test.go` → header, § "Effort has no
  such observable" — the 2026-08-19 negative at 2.1.220 this ticket re-measures, and the
  measured fact that claude emits one `system/init` line per turn (three turns, three lines).
- `internal/sessions/session.go` → `claudeSettingsArgs` — appends `--effort <level>`, the launch
  path arm A reproduces.
- `internal/sessions/pool.go` → `Pool.UpdateSettings` in-band delivery — writes `/effort <level>`
  as an ordinary user turn, the path arm B reproduces.
- `docs/knowledge/features/e2e-realclaude-compaction-capture-test-go.md` — "a gate-only live run
  can promote a fixture and still lose it". This is the lesson that shapes § Fixture promotion
  below: the dispatcher's real-claude gate runs from a detached worktree and never runs `git add`.
- `docs/knowledge/features/bypass-approval-argv-probe.md` — the five committed arms, the
  redactor's honest limit (a per-run redactor covers only the paths *you* put on the argv), and
  the empty-needle footgun in `strings.ReplaceAll`.
- `internal/e2e/realclaude/testdata/initialize_control_v2.1.239.json` — re-read to confirm the
  model rows: `default`, `sonnet`, `claude-fable-5[1m]` and `opus` publish `supportsEffort: true`
  with levels `low, medium, high, xhigh, max`; both haiku rows publish neither key.

## Context

`systemInitLine` declares one field. Three more of the line's keys have an operator consumer —
`claude_code_version`, `permissionMode` and `effort` — and #2252 will declare them from bytes this
repo has seen rather than from an SDK type. Two are already proven by 33 committed captures. The
third is not: none of those captures carries an `effort` key, and none of them ever set an effort,
so the absence is currently unfalsifiable. The only measurement that ever looked (2026-08-19, claude
2.1.220, in-band `/effort low` only) is a single-version negative recorded in prose.

This ticket produces one capture at 2.1.259, under the daemon's production approval-argv spawn
shape, with an effort set on **both** of the daemon's paths — `--effort <A>` on the launch argv and
`/effort <B>` in band at a different level — and pins what the init line carries.

An absent `effort` is a valid and useful result. The design's whole job is to make that absence
*mean* something, which it only does if the record itself proves an effort was set.

No ADR is warranted: this adds no production behaviour and no new convention. It reuses three
existing rigs.

## Design

### Structure

Two new files, three edited, zero production source files.

| file | what it is |
|---|---|
| `internal/e2e/realclaude/effort_init_capture_test.go` | new: the live probe, its arm, its redaction pass, its offline self-checks |
| `internal/streamsup/effort_init_capture_test.go` | new: the tag-free reader and the pin |
| `internal/e2e/realclaude/set_permission_mode_probe_test.go` | edited: two inert knobs on `setModeChildConfig`, one parameter on `writeSetModeFixture` |
| `internal/e2e/realclaude/dropped_line_capture_test.go` | edited: two methods on `dropcapRedactor`, one substitution class |
| `docs/specs/architecture/2251-effort-init-capture.md` | this plan |

### The live probe rides `runSetModeChild`

One arm, built the way `bypassArgvArm` builds its five: `setModeArm` with `extraLaunchArgs` =
`bypassArgvApprovalArgs(mcpConfigPath)` plus `--effort <A>`, `launchYOLO` false and `targetMode`
empty (no control request; this measurement's second path is a user turn, not a control frame).
The stub approval socket and the mcp-config document are stood up exactly as
`TestRealClaude_BypassApprovalArgv_Probe` stands them up, so the init line reports `pyry_approve`
connected — AC 1's spawn-shape requirement.

Three turns, because claude emits one `system/init` line per turn:

1. a rig-authored no-tool prompt → init line 1, on the child launched with `--effort <A>`
2. the bare text `/effort <B>` → init line 2, the in-band turn itself
3. a second, different no-tool prompt → init line 3, after the in-band change landed

Prompts 1 and 3 ban tool use and each carry a per-run `run=<nonce>` suffix, both as
`ccapPrimePrompt` does. The approval socket is stood up so the spawn shape is production's, not so
an approval fires; a tool-using prompt would spend turns proving something
`bypass_approval_argv_probe_test.go` already proved.

The nonce is `time.Now().UnixNano()` and is **not optional**. `newDropcapRedactor`'s last
parameter is formatted with `strconv.FormatInt`, so a zero nonce installs `"0"` as a
one-byte substitution rule and rewrites every zero digit in the whole record to `$NONCE`.
The empty-value guard the constructor documents does not catch it, because `"0"` is not empty.
Every existing caller passes a real timestamp; this one must too, and the prompts are where it
comes from so the value the table substitutes is a value the record actually contains.

Model: `claude-sonnet-5`, the precedent constant `askQuestionCaptureModel` already uses. Not
haiku — the committed `initialize` response is explicit that haiku publishes no `supportsEffort`.
Levels: `low` on the argv, `high` in band. Both are in the five that response publishes, and they
differ, which AC 1 requires.

### Two inert knobs on the shared rig

`runSetModeChild` builds the record and calls `writeSetModeFixture` unconditionally. This ticket
needs to (a) record an observation only computable from the finished record and (b) refuse the
write outright when a deny-scan trips. Both are additive knobs on `setModeChildConfig`, zero-valued
at all four existing construction sites, following `mark` / `approval` / `redactRunLocal`:

- `fillRecord func(*testing.T, *setModeFixtureRecord)` — called after the record is built and
  before it is marshalled.
- `screenFixture func(*testing.T, []byte) ([]byte, bool)` — called by `writeSetModeFixture` on the
  marshalled bytes; returns replacement bytes and a verdict. `false` writes nothing at all.

`writeSetModeFixture` gains the screen as a parameter rather than a sibling writer, for the reason
its own doc gives: it is the single fenced route to `testdata/`, named on eleven `finOfflineExecBans`
lists, and a second writer would be a second route none of them cover. It has exactly one call site.
When the screen refuses, it returns an empty path and the caller logs the refusal.

Screening the **marshalled bytes** rather than the record's fields is deliberate: a field-by-field
redaction leaks whatever field its author forgot, and this record carries claude's verbatim stdout.

### Redaction

The record goes through the `newDropcapRedactor` + `newDropcapScanner` lineage. Three of the four
classes AC 5 names already exist: `cwd` is the workdir class, plus `session_id` and both home
classes. Two values are not knowable at construction:

- `session_id` — claude mints it; the setMode rig spawns `claude` directly rather than through
  `streamsup.Config.SessionID`, so unlike `ccapSessionID` it cannot be supplied.
- `messaging_socket_path` — claude's own socket, spelled `/tmp/cc-socks/<pid>.sock`; it sits
  un-redacted in 19 committed files today.

So `dropcapRedactor` grows two methods that append a class after construction and restore the
longest-value-first order the constructor established: `addValueClass(class, replacement, value)`
and `addPathClass(class, replacement, path)`, the latter over `dropcapPathSpellings`. The
constructor's sort is lifted into the shared `resort` they both call, so the invariant has one
home. One new class name, `dropcapClassMessagingSocket`. No existing call site changes — and none
could have taken these as constructor parameters, because none of them knows the values.

The stub socket path and the mcp-config path are added the same way, so the *whole record* is
covered rather than just the argv `bypassArgvRedactor` reaches. That is belt-and-suspenders with
different fabric: `redactRunLocal` substitutes informative placeholders into the recorded argv, the
dropcap table sweeps every byte of the marshalled record, and the deny-scan is the net behind both.

The scanner gets the messaging-socket path via its existing `addDynamicPath`, so the same value the
table removes is the value the net looks for. On a hit the run fatals with class names and the
`dropcapLocateHits`-style location only — never the offending value — and writes nothing.

**Ordering is load-bearing.** `newDropcapScanner` reads `CLAUDE_CODE_OAUTH_TOKEN` and
`ANTHROPIC_API_KEY` through `os.Getenv` as deny needles, and `WithWorktreeAuthenticated` is what
re-pins them into this process. Built first, the scanner takes an empty needle, which
`dropcapScanner.scan` reports as not-applied — silently skipped, never fatal. The credential net
would be off while every message still read green. The scanner is therefore built **after** the
auth helper, and `credential_scan_applied` ships in the record so "the net ran" is a recorded fact
rather than an absence.

Both the in-repo fixture and the artifact copy are written **downstream of the screen**, so a
deny-scan hit leaves neither. That is AC 5's requirement and `dropcapWriteRecord`'s discipline; the
cost is that a tripping run is diagnosable only from the located class names, which is the trade
that rule already made.

### The record's non-vacuity witnesses

A new nested block on `setModeFixtureRecord`, `effort_capture`, nil on every other arm, its inner
fields carrying no `omitempty` for the reason `ModeSwitchAuto`'s doc gives — a false here is a
finding, and `omitempty` spells false by absence:

- `launch_level`, `inband_level` — the two levels
- `launch_flag_seen` — `--effort <A>` located in the **recorded** argv, adjacent tokens
- `inband_prompt` — the exact turn text written
- `acknowledgement`, `acknowledgement_seen` — claude's own assistant text for the `/effort <B>`
  turn, capped through `truncateString`
- `init_lines` — per init line: sorted key set, `claude_code_version`, `permissionMode`, `effort`
  and whether the key was present at all
- `redaction`, `credential_scan_applied`, `credential_scan_skipped` — the lineage's own metadata

`init_lines` is the probe's summary and is corroboration only: the streamsup reader re-derives
every pinned value from each line's own bytes, per #2237's finding that a reader trusting the
record's labels pins the probe's decoding rather than claude's line.

### The streamsup-side reader

`internal/streamsup/effort_init_capture_test.go`, no build tag, reading
`../e2e/realclaude/testdata/` for the reason `compactionCapturePath`'s doc gives: the bytes are only
bytes. A fourth self-contained reader with its own package constants and no path parameter, not a
generalisation of the three that exist — same argument, restated where the omission would be.

Shape:

- `effortInitCaptureVersion` spliced into the path, so the filename cannot drift from the version
  the reader enforces.
- `effortInitPins`, one entry per init line: the full sorted key set, `claude_code_version`,
  `permissionMode`, `effort` and `effort_present`. **Empty until the live gate has run.**
- `effortInitReaderGate(fixtureExists, pinFilled bool) (action, reason string)` — pure, four
  quadrants, exactly one legal skip (absent fixture, empty pin) and a fatal on the other three.
- The pin test: gate, decode, bind the filename to `claude_version` + `arm`, assert both witnesses
  (`--effort <A>` adjacent in `argv`; `/effort <B>` in `prompts`; `acknowledgement_seen`), require
  at least two init lines, then compare each line's re-derived answer against its pin.

Asserting the witnesses **inside the pin test** is what makes the whole measurement honest: a pin
recording `effort: absent` is only evidence if the same test proves the run set one.

### Fixture promotion

The gate that can run this is the dispatcher's `make e2e-realclaude`, which executes from a
detached worktree it then removes; it never runs `git add`. #2229's fixture was lost exactly that
way. The mitigations, all four of them:

1. The fixture path is a compile-time constant under `testdata/` — an in-repo write.
2. The record is **also** written to an `os.MkdirTemp` artifact directory outside the worktree, so
   the bytes survive the removal and can be promoted later, which is how #2236 recovered #2229's.
3. The probe's gate is the **fixture's absence**, never a `PYRY_PROBE_*` variable. `make
   e2e-realclaude` sets no such variable, so an env-gated probe skips before the credential check
   and the gate greens having produced nothing.
4. The streamsup reader fatals on a landed fixture with an empty pin, so the bytes cannot be
   committed and left unpinned.

The probe logs the artifact path and an explicit "commit it" instruction on success.

## Concurrency model

Inherited, not invented. The stub approval socket's accept loop is `bypassArgvServe`'s, joined by a
`t.Cleanup` that closes the listener and waits on the done channel, so no goroutine outlives the
test — the shape `-race` reports when it is missing. `runSetModeChild` owns the single stdout reader
goroutine and the serialised drive sequence. The probe is not `t.Parallel()`: `WithWorktreeAuthenticated`
reaches `t.Setenv`. Redaction, screening and the record build all run on the test goroutine after
the child has exited, so the redactor's counters need no lock.

## Error handling

The probe **passes on every recorded outcome**, matching every measurement in this family: a child
that refuses to launch on `--effort low`, an `effort` key that is absent, a `/effort` turn claude
answers with a refusal — each is a finding that lands in the record and the log.

Four conditions are exceptions, and each is an instrument failure rather than a result:

- zero stdout lines — `runSetModeChild` already fatals.
- a deny-scan hit — fatal, class names only, nothing written (the AC 5 requirement, and the
  `dropcapWriteRecord` discipline).
- a record whose `claude_version` does not match the pinned fixture version — refuse the in-repo
  write and say so, `ccapRecord.fixtureWorthy`'s rule, so a claude upgrade is a loud instruction to
  re-capture rather than a fixture that quietly measures another release.
- fewer than two `system/init` lines — refuse the in-repo write. A capture that cannot answer AC 1
  must not be promoted; the artifact copy is still written so the failure is diagnosable.

Errors from the child, stderr and write paths keep the rig's existing handling: `initControlScrubbed`
runs first and unconditionally, stderr is capped at `stderrFixtureCap`, and every failure string is
put through the redactor before it reaches a log.

## Testing strategy

`make check` cannot run the live probe; everything below except the last row runs inside it or in
the tagged package's deterministic half.

| test | package | proves |
|---|---|---|
| `effortInitReaderGate`'s four quadrants | `streamsup` | the gate is a state machine, provable on the leg where the fixture is still absent |
| the pin test | `streamsup` | skips today (absent fixture, empty pin); reddens the moment bytes land unpinned |
| the arm's argv | `realclaude` | the four approval flags **and** `--effort <A>` present, `--dangerously-skip-permissions` absent |
| the summariser | `realclaude` | sorted keys, the three values, and the absent-`effort` case, over synthetic init lines; a non-init line contributes nothing |
| the acknowledgement extractor | `realclaude` | it reads the `/effort` turn's assistant text and not turn 1's |
| the redaction extension | `realclaude` | a synthetic init line carrying `/tmp/cc-socks/<pid>.sock` and a session id comes out substituted, **and** the un-redacted form is one the scanner would have caught — the net proves the table rather than agreeing with it |
| fixture naming | `realclaude` | the minted name matches neither of the package's existing testdata globs |
| `TestRealClaude_EffortInitCapture` | `realclaude`, tagged | the capture itself, on the live gate |

The redaction row is the one that must not be vacuous. An empty needle is skipped and reported as
not-applied rather than failing, so a test that merely ran the scan over a clean record would pass
with the net switched off. It asserts both directions on the same synthetic bytes.

## Open questions

1. **Does `effort` appear on the init line at 2.1.259 with an effort set?** The question the run
   exists to answer. Either answer is recorded and pinned; if absent, a comment goes on this ticket
   and on #2252 and the downstream children drop the field.
2. **Does `--effort low` launch at all on 2.1.259 beside the four approval flags?** No capture
   combines them. A refusal is a finding — recorded, and reported as `launch_flag_seen` with no
   init line.
3. **Does the `/effort high` turn produce a result line, so turn 3 is reached?** `/model` does
   (three turns, three results, measured 2026-08-19). If `/effort` does not, the turn budget expires
   and the record says so; two init lines still satisfy AC 1.
4. **Does claude echo the mcp-config or stub-socket path anywhere in stdout?** Not expected —
   `mcp_servers` carries name and status only — but both are in the substitution table regardless,
   so the answer changes no committed byte.

Each is resolved during the live lap, and any that changes the design lands in a `## Revisions`
entry.

## Sizing

Over the 800-line total-work ceiling on purpose, at roughly 1150 lines across five files. The
refiner's estimate is ~950 across three; the difference is the shared-rig edits and the offline
self-checks this package requires of every capture.

The floor rule is why it is not split. The fixture's only consumer in this family is its own
reader: a "produce the capture" slice would redden nothing on its own — its live test cannot run in
`make check` at all — and a "read the capture" slice would have no bytes to read and would ship a
test that skips. Per the sizing guide, when the floor and the ceiling disagree the floor wins. Every
other line of the boundary holds: **zero** production source files, zero new exported types, zero
consumer call sites requiring simultaneous update (both `dropcapRedactor` additions are new methods,
and `writeSetModeFixture` has one caller), five acceptance criteria, no state-machine reject fan-out.

## Revisions

**2026-09-09 — the record declares the deny-scan but not the substitution counts.**
§ Design listed `redaction`, `credential_scan_applied` and `credential_scan_skipped` as fields of
the `effort_capture` block. Only `credential_scan_applied` shipped, and the omission is forced
rather than a trim: the substitution runs on the **marshalled** record, so a count table describing
that pass cannot be inside the bytes it describes — writing it before the marshal would commit a
table of zeroes, and `dropcapRecord` avoids the same problem only because it redacts field by
field, which § Design rejects here for leak-by-forgotten-field. The alternative, a hand-written
declaration of the table's classes, would be a second copy of `newDropcapRedactor`'s own list and
would drift from it. The counts and the not-applied classes are logged by the screen instead.
`credential_scan_skipped` went the same way: `scanner.applied()` already answers per class whether
the needle ran, which is the fact worth committing, and the run-time skip list is logged.

Nothing else in the design moved. Every open question in § Open questions is still open by
construction — each is answered by the live lap, not by this implementation.

## Security review

**Verdict:** PASS (second pass — the first found one MUST FIX, now designed out above)

**Findings:**

- [Trust boundaries] No findings. There is exactly one boundary and it is a single function: every
  byte claude wrote reaches disk only through the `screenFixture` pass, over the **marshalled**
  record rather than field by field, so a field a future author forgets to redact is covered by
  construction. Two consequences are named rather than assumed: the `/effort` acknowledgement is
  model-composed text, and the summariser copies claude-chosen key *names* into the record — both
  cross the same single screen. The pin in `internal/streamsup` is claude-controlled data becoming
  Go source, which is a code-review boundary, not an execution one; it must be transcribed from the
  committed fixture, never from a run log, per #2237's finding that the three available sources
  disagree.
- [Tokens] **MUST FIX, fixed in this plan.** `newDropcapRedactor`'s nonce parameter goes through
  `strconv.FormatInt`, so passing `0` — the obvious value for a probe with fixed prompts — installs
  `"0"` as a one-byte substitution rule and rewrites every zero digit in the record. The
  constructor's empty-value guard does not catch it. The design now takes a
  `time.Now().UnixNano()` nonce and carries it in the prompts. Separately, **SHOULD FIX, also
  folded in:** `newDropcapScanner` must be built after `WithWorktreeAuthenticated` or its
  credential needles are empty and silently not-applied; § Redaction now states that ordering and
  the record ships `credential_scan_applied`. No token is minted, stored or logged here, and
  `setModeFixtureRecord` deliberately has no `env` field.
- [File operations] No findings. The fixture name is minted from package constants through
  `versionSlug` and `modeSwitchNameToken`, so no token reaches a path unfiltered and the arm name is
  a constant; `writeSetModeFixture` stays the single fenced route to `testdata/` and writes
  temp-then-rename. The artifact copy goes to an `os.MkdirTemp` directory at `0600`. The
  `os.Stat`-then-write gap on the fixture is not a TOCTOU boundary: an attacker who can write into
  the worktree has already won, and the suite is sequential so no second run races it.
- [Subprocess] No findings. `--effort low` / `--effort high` are package constants, never values
  derived from something claude said — the discriminator `modeSwitchModelValueOK` exists for, since
  a subprocess-derived value beginning with `-` parses as a flag. No `sh -c`. `--strict-mcp-config`
  is inherited from `bypassArgvApprovalArgs` and is load-bearing: without it a project or user
  `.mcp.json` could register a second `pyry_approve` that shadows the stub socket. The in-band
  `/effort` text is marshalled by `setModeTurnLine`, so it cannot inject into the stream. Child
  lifetime is `exec.CommandContext` plus the rig's stdin-close-then-Wait.
- [Cryptographic primitives] Not applicable: nothing here generates, compares or stores a secret.
  The nonce is a diffability and turn-freshness device, not a security value, which is why
  `time.Now()` is the correct source for it.
- [Network & I/O] No findings for what this ticket adds; the caps are inherited and named:
  `setModeScanMax` (1 MiB per stdout line, overflow lands in `scanner_error`), `stderrFixtureCap`
  (8 KiB), the per-turn and per-child context budgets. The one uncapped field this ticket
  introduces, the acknowledgement, goes through `truncateString`. `stdout_events` is unbounded in
  this record shape and in every committed capture of the family — inherited, bounded in practice by
  the turn budget, and not this ticket's to change.
- [Error messages, logs, telemetry] No findings. A deny-scan hit prints class names and the located
  region only, never the offending value — printing it in CI output is the exposure the scan exists
  to prevent. `initControlScrubbed` runs first and unconditionally inside `runSetModeChild`, before
  any stderr can reach a message. Every path in a log goes through the redactor. The one path named
  in the clear is the stub socket in the `net.Listen` failure, an inherited and deliberate choice —
  it is test-owned and temporary, and naming it is the only thing that makes `EADDRINUSE` or an
  over-long `sun_path` diagnosable.
- [Concurrency] No findings. The accept loop is joined by a `t.Cleanup` that closes the listener
  and waits on its done channel, so no goroutine outlives the test. The `dropcapRedactor`'s
  counters are unsynchronised by design and are touched only from the screen, which runs on the test
  goroutine after the child has exited; `redactRunLocal` stays a pure closure.
- [Threat model alignment] The relevant threat is a committed capture carrying operator identity
  into a public repo. There is no `docs/threat-model.md` in this repo; the in-repo statement is
  `dropcapRedactionRationale` and the redaction lineage, and AC 5 is that threat's mitigation.
  **OUT OF SCOPE, and named by the ticket itself:** the 19 already-committed files carrying an
  un-redacted `messaging_socket_path`. This ticket covers the new capture; a re-redaction sweep of
  the existing ones needs its own ticket and its own re-derivation of every pin that reads them.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-09
