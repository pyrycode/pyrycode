# #2269 — capture claude's `stream_event` lines under `--include-partial-messages`

One new file, `internal/e2e/realclaude/stream_event_capture_test.go`, holding a live capture probe and
its offline self-checks. No production file is created or modified. The deliverable is a committed
fixture under `internal/e2e/realclaude/testdata`; #2270 reads it to write the parser arm.

## Files read

- `internal/e2e/realclaude/dropped_line_capture_test.go` → `dropcapRecorder`, `dropcapRedactor`,
  `dropcapScanner`, `dropcapMakeEntry`, `dropcapWaitForChild`, `dropcapCaptured`,
  `dropcapRedactionRationale` — the capture kit this probe composes rather than reimplements. The
  rationale constant is the one that names `system/init`'s operator-configuration inventory, which
  this capture keeps every line of.
- `internal/e2e/realclaude/api_retry_capture_test.go` → `arcapFixturePath`, `arcapTurnBudgetWithin`,
  `arcapBudgetFor`, `arcapRecord.stagingVerdict`, `arcapRecord.fixtureWorthy` — the freshest analogue
  (#2262, merged 2026-09-09) and the source of three shapes adopted here: a fixture named from the
  observed version behind a globbing arming gate, a turn budget sized against `t.Deadline()`, and the
  double write to an artifact directory beside the in-repo path.
- `internal/e2e/realclaude/tool_progress_capture_test.go` → `TestRealClaude_ToolProgressCapture`'s
  gate comment, `tpcapRecord.fixtureWorthy`, `tpcapMarkers` — the fixture-absence gate, and the rule
  that a census must be computed independently of the production decoder.
- `internal/streamsup/runner.go` → `buildArgs` — confirms `Config.Args` appends *after* the fixed
  `--input-format`/`--output-format`/`--verbose` prefix, which is what lets the probe add its flag
  without touching production (AC 4).
- `internal/e2e/realclaude/harness_streamparse_test.go` → `parseOne` — the shipped parser's verdict,
  recorded per line as data.
- `internal/e2e/realclaude/fixtures.go` → `WithWorktreeAuthenticated`, `realHome`; `resilience_test.go`
  → `resolveClaudeBin`; `background_trigger_probe_test.go` → `probeClaudeVersion`.
- `docs/knowledge/features/e2e-realclaude-api-retry-capture-test-go.md` — the double-write lesson
  ("any new capture probe in this family should default to it") and the invocation-deadline budget
  lesson. Both are applied here.

## Context

The interactive spawn never requests partial messages, so no `stream_event` line has reached the
parser and the string appears nowhere in the tree. #2270 has to write the mapping arm, and the only
prior measurement is a vault-resident census of counts, which cannot answer where an `assistant` line
sits relative to its block's deltas, which line carries the message id the delta coalescer keys on,
or what crosses the wire while a `tool_use` block's input is still partial JSON.

This ticket measures and commits the bytes. It changes no production behaviour. No ADR is warranted:
the design is an application of the capture pattern this package already documents, not a new
decision.

## Design

### Placement and naming

One file. Every file-local identifier takes the `secap` prefix, per the package convention that
exists because siblings land in this package concurrently and a branch-overlap check cannot see a
same-package identifier collision. The prefix is currently unused in `internal/`.

### The spawn

`streamsup.New` with `Args` = `--model haiku`, `--dangerously-skip-permissions`,
`--include-partial-messages`; `Stdout` = a `dropcapRecorder`; `Logger` = a `dropcapArgvHandler` so
the argv is *observed* from the runner rather than transcribed. `WorkDir` is a fresh empty
non-git directory under the per-test temp `$HOME`. `buildArgs` is not touched: the flag rides in the
probe's own `Config.Args`, which that function appends after its fixed prefix.

The observed argv is checked for the flag and recorded as its own boolean. Without it a zero-
`stream_event` capture cannot distinguish "claude emits none here" from "the flag never reached the
child", and only the first is a finding.

### The staging

One turn, rig-authored prompt with a nonce: a few sentences of prose, then exactly one `Bash` call
running `echo` of a rig-chosen token, then a closing sentence. Text before and after a tool call is
what produces both several blocks of assistant text and the `tool_use` block AC 1 asks for, and it
reproduces the two-`assistant`-line shape the ticket names as an open question. No file is read or
written; the blast radius of the YOLO spawn shape is `echo` in an empty temp directory.

### The record

Every captured line becomes one entry, in arrival order — the deliberate difference from #2089's
probe, which keeps only lines matching its quarry. Per entry: arrival index, top-level type and
subtype, the shipped parser's event count, and the payload half built by `dropcapMakeEntry` so the
base64 arm for invalid UTF-8 is shared rather than re-derived. For a `stream_event`, additionally the
inner `event.type`, the `delta.type` when the event carries a delta, the block index, the block type
resolved from the `content_block_start` that opened that index, and the sorted key names of `event`.

The message id is found by a **declared, ordered probe set** of candidate JSON paths; the entry
carries both the id and the path that produced it, or `none`. Naming the set is what makes an absence
a measured absence rather than silence, and the recorded `event` key names are what let a reader see
a fifth spelling the set did not know.

Censuses on the record: line type (with system subtype), inner event type, delta type, block type,
`deltas_by_block_type` (a map of block type to its delta-type counts), message-id source, and the
deduplicated message ids observed in order. Every census is computed in this file from the raw bytes,
never by calling a production decoder — a census that agreed with the matcher by construction would
measure nothing.

### The gate (AC 3)

`stagingVerdict` names which of five readings a thin capture is, because only some are findings:

| Condition | Reading |
|---|---|
| the flag is absent from the observed argv | instrument fault; nothing about claude was measured |
| flag present, zero `stream_event` lines | a finding about the surface, to route back |
| no `text_delta` in any delta | the turn produced no streamed assistant text |
| no `tool_use` block opened at all | staging failure: the turn made no tool call |
| a `tool_use` block opened but carried no deltas | a finding #2270 must consume: the input never streamed |

`fixtureWorthy` refuses to promote unless the outcome fired, the flag was in the argv, at least one
`text_delta` was seen, at least one delta belonged to a `tool_use` block, the version token composes
a safe filename, no `stream_event` entry is base64-encoded (a payload the reader could not replay),
and **the recorder's caps dropped nothing** — no line over the byte cap, no discarded partial, no
unterminated tail. The live test fatals with the observed per-type censuses on each refusal, so a
failed gate run says what claude did instead.

The cap arm is this probe's own, not inherited. Every sibling capture filters to a quarry of a few
lines; this one keeps every line of a turn under a flag that multiplies the line count, so it is the
first in the family that can plausibly reach `dropcapMaxCaptureBytes`. Past that cap whole lines are
dropped and only counted, which would commit a fixture with a hole in the middle of a delta run —
precisely the shape #2270 cannot map, and one that reads as a complete capture from outside.

### The fixture

Named `testdata/stream_event_v<version>.json` from the version `claude --version` actually printed,
with the arming gate matching `testdata/stream_event_v*.json`. This follows #2262 rather than #2229's
compile-time pin: the record *is* this ticket's deliverable, so a claude release between authoring and
the live gate run must not turn the whole output into nothing promoted. #2270's reader pins from the
other end, splicing its own version constant and refusing a capture from a different release.

That version string is **subprocess-controlled data flowing into a write path**, so the validator is
a trust boundary rather than a tidiness check: it admits no path separator, requires a leading digit,
caps the length, and allows only alphanumerics, `.` and `-`. Two rules make it real rather than
decorative. The fixture write uses *only* the validator's returned path — the record's raw version
field never composes a path anywhere — and the validator's table test carries an explicit traversal
token beside the empty, unreadable and over-long ones, each also checked against the arming glob so a
written fixture provably disarms the probe.

The validator is written in this file rather than shared with `arcapFixturePath`, which hardcodes its
own prefix. The package's per-probe prefix convention exists for exactly this reason, and widening a
sibling's just-landed signature to save twenty lines is the worse trade.

### Redaction

The mechanism is inherited whole: `dropcapRedactor`'s declared substitution table over every string
that enters the record, and `dropcapScanner` as a fail-closed deny-scan over the marshalled record
plus every decoded base64 payload. `os.Environ()` is never harvested; this probe sets no environment
variable, so `env_delta` is empty rather than a literal. Two ordering rules are load-bearing.
`WithWorktreeAuthenticated` must run *before* `newDropcapScanner`, because the scanner reads the two
credential values as deny needles and building it first yields empty needles that `scan` reports as
not-applied — the credential net silently off while every message reads green. And the record is
marshalled twice, so `credential_scan_skipped` ships inside the written bytes.

What this capture specifically carries is written into its own rationale rather than inherited,
because the inherited sentence was authored for narrower probes. Keeping every line means committing
two classes the sibling captures' filters excluded: the assistant's own prose, and the `Bash` call's
input and result. Both are rig-authored here — a rig-chosen topic and an `echo` of a rig-minted token
in an empty non-git workdir — which is the by-construction defence, with the deny-scan behind it as
different fabric. It also means `system/init`, whose payload is the operator's local claude
configuration inventory: MCP server names, the tool list, slash-command and skill names, subagents,
plugins and permission mode. None is a credential and all of it is kept, because it is what a mapping
ticket has to read, but it describes one machine's setup rather than claude, so whether to publish
the record is the operator's call — which is the point of stating it in the record instead of leaving
a reader to notice.

### Writes

Two, per the #2262 lesson: the deny-scanned bytes go to an `os.MkdirTemp` artifact directory that
outlives a discarded worktree, and to the in-repo fixture path so the run that produced them is the
run that lands them. The live-gate run's in-repo write dies with the dispatcher's worktree; the
artifact copy is the recovery source, and the log names it.

## Concurrency model

No new goroutine beyond the two the kit already establishes: `os/exec`'s copier drives the recorder's
`Write` while the test goroutine reads via `snapshot`, which is why `dropcapRecorder` is mutex-guarded;
and `runner.Run` runs on its own goroutine whose exit is awaited under a bounded wait in a cleanup
registered so it runs before the record write. Classification runs entirely on the test goroutine
after the turn, where `parseOne`'s `*testing.T` is legal.

## Error handling

Nothing claude does is fatal — a thin capture is a recorded outcome with a verdict, not a crash. Only
an instrument fault or a redaction failure fails the run. The turn budget is sized against
`t.Deadline()` with a teardown reserve and skips outright below a floor, because a binary killed by
`-timeout` runs no cleanups and records nothing. The record-writing cleanup is registered before
anything below it can fail, so a structural fatal still leaves the evidence on disk. On a deny-scan
hit nothing is written at all and the message names the class only.

## Testing strategy

`make check` never compiles this package, so the offline self-checks below run under
`make e2e-realclaude` and under an explicit `go vet -tags e2e_realclaude` plus a `-run` over the
offline half, which is the compile-and-execute coverage the standard gate cannot give.

- **Classification** — hand-authored synthetic lines (a `message_start`, a text
  `content_block_start` and its delta, a `tool_use` `content_block_start` and its delta, an
  `assistant` line) fed through the classifier; asserts the entries, every census, and
  `deltas_by_block_type`. Written first and failing.
- **`fixtureWorthy`** — a table over each refusal arm, each a capture that would look green from
  outside while proving nothing.
- **`stagingVerdict`** — a table asserting the five readings, and specifically that a rig failure
  never reads as a finding about the surface.
- **Fixture path** — a table over unusable version tokens (empty, unreadable, separator-bearing,
  over-long), each also checked against the arming glob so a written fixture provably disarms the
  probe.
- The live capture itself is proven only by the gate run; its own AC 3 fatals are the assertion.

## Open questions

1. Whether the `stream_event` wrapper preserves the message id, and on which line. The declared probe
   set plus the recorded `event` key names answer it either way; a `none` across every delta is the
   finding #2270's coalescer most needs.
2. Whether a `tool_use` block's input arrives as deltas at all on this surface. Both answers are
   recorded; only "block opened, no deltas" is a finding rather than a staging failure.
3. Whether one turn yields several text blocks or one. Recorded as `block_type_census` and visible in
   the committed lines; the gate does not turn on it.

Each is resolved by reading the committed capture, and any that changes the design is recorded under
a `## Revisions` entry.

## Security review

**Verdict:** PASS (first pass FAILED on two MUST FIX findings; both are fixed above and the checklist
was re-walked from the top.)

**Findings:**

- **[Trust boundaries] SHOULD FIX, addressed in the plan.** The boundary is claude's stdout → the
  record → a committed fixture → possibly a public issue comment, and it is single-point at
  `dropcapRecorder.Write` in, `dropcapRedactor.redact` and `dropcapScanner.scan` out. The hole was
  scope, not mechanism: every sibling capture filters to a quarry, and this one keeps every line, so
  it newly commits assistant prose and a tool call's input and result. Inheriting
  `dropcapRedactionRationale` verbatim would have described a narrower capture than the one shipping.
  The Redaction section now states the widened classes in the record's own words, `system/init`'s
  operator-configuration inventory included.
- **[File operations] MUST FIX, fixed.** `probeClaudeVersion` reads `claude --version` from a
  subprocess and the plan composed the fixture path from it — subprocess-controlled data reaching a
  write path, with the validator described only as producing "a safe filename". A binary printing a
  traversal token would have written outside `testdata/`. Now the validator is named as the boundary
  (no separators, leading digit, length cap, byte allowlist), the write is required to use only its
  returned path, and its table test carries an explicit traversal token.
- **[File operations] SHOULD FIX.** Both writes are `0600` in Phase B, matching the analogues; the
  plan does not repeat the mode per call site.
- **[Network & I/O] MUST FIX, fixed.** `dropcapMaxCaptureBytes` drops whole lines past 8 MiB and only
  counts them. Every prior probe in this family filters to a handful of lines and could never reach
  it; this one keeps every line of a turn under a flag that multiplies line count, and a fixture with
  a silent hole mid-delta-run is exactly what #2270 cannot map while looking complete from outside.
  `fixtureWorthy` now refuses to promote any record whose caps dropped a line, a partial, or left an
  unterminated tail. No socket and no listener, so #2262's server-timeout category does not arise.
- **[Tokens, secrets, credentials] No findings.** Nothing is minted. The two credential values are
  read via `os.Getenv` solely as deny needles and are never stored, logged or written. The one live
  hazard is ordering — a scanner built before `WithWorktreeAuthenticated` has empty needles, which
  `scan` reports as not-applied while every message still reads green — and the Redaction section
  pins the order and requires `credential_scan_skipped` in the written bytes.
- **[Subprocess execution] No findings.** The probe contributes only file-local constants to the
  argv; nothing user-controlled reaches it, and no `sh -c` is involved. The child inherits this
  process's environment deliberately, which is how credentials reach claude, and `os.Environ()` is
  never harvested into the record. The tool restriction is prompt-level, which is stochastic — but an
  argv-level allowlist under `--dangerously-skip-permissions` is the same permission machinery the
  flag bypasses, so it would be a defence that does not defend, and the failure it would guard has
  not been observed across three sibling captures. The deterministic net is the deny-scan, which is
  different fabric, and the empty non-git workdir is the by-construction bound.
- **[Cryptographic primitives] Not applicable.** No key, no nonce in the cryptographic sense. The
  session id is a fixed literal that claude echoes back, which is what lets the redactor's
  `session_id` class catch it; it is not required to be unpredictable. The prompt nonce exists only
  to give `prompt_nonce` something to substitute.
- **[Error messages, logs, telemetry] SHOULD FIX.** Every fatal and log line reports counts, indices
  and census keys only, and census keys are a closed structural vocabulary of type names rather than
  content; a deny-scan hit names the class and never the value. In Phase B, `message_ids_observed`
  stays in the record and out of `t.Logf` — it is the one census-adjacent field carrying identifiers.
- **[Concurrency] No findings.** No new goroutine and no new lock. The recorder's mutex covers the
  copier-versus-test-goroutine race, the argv observer guards its own field, and the runner goroutine
  is awaited under a bounded wait in a cleanup registered so it runs before the record write.
- **[Threat model alignment] Not applicable in the relay sense — no network surface.** The
  CLI-relevant threat is the publication boundary, handled under Trust boundaries above.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-09
