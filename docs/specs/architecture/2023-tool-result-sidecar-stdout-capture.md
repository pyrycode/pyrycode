# #2023 — capture claude's `tool_result` sidecar off the live stream-json **stdout** stream

One live-claude probe, one committed capture. It answers one empirical question:
**does a `user`/`tool_result` line on claude's stdout carry a top-level
`toolUseResult` sidecar, and if so with what top-level key set?** Every shape the
tree knows today was read off the *transcript*, which the daemon does not parse.

## Files read

- `internal/e2e/realclaude/dropped_line_capture_test.go` → `dropcapRecorder`
  (`Write`, `consume`, `snapshot`), `newDropcapRedactor`, `dropcapRedactor.str` /
  `.strs` / `.substitutions`, `newDropcapScanner`, `dropcapScanner.scan` /
  `.applied`, `dropcapPathSpellings`, `dropcapJSONEscape`, `dropcapContains`,
  `dropcapWriteRecord`, `newDropcapArgvHandler`, `dropcapWaitForChild` — the rig
  this ticket reuses wholesale rather than rebuilding. The recorder already keeps
  every complete stdout line verbatim with its decoded `type`/`subtype`, and
  closes `resultSeen` on the first `result` line; that is exactly this probe's
  turn boundary and its retention source.
- `internal/e2e/realclaude/initialize_control_probe_test.go` → `initControlScanApplied`,
  `initControlScanPathClasses`, `writeInitControlFixture`'s call site in
  `runInitControlChild`, `TestRealClaude_InitializeControl_SendPointArms` — #1688 /
  #1747's shape for the commit-the-bytes half: `newDropcapScanner(home, "", workdir)`
  with an empty `artifactDir`, the fill-site completion of the applied map, and
  the write into `filepath.Join(packageDir(t), "testdata")`.
- `internal/streamsup/parser.go` → `emitUser` — the consumer this capture exists
  to inform. It maps each `tool_result` block in a `user` message to a
  `turnevent.ToolUpdate` and reads **nothing** from a top-level `toolUseResult`.
  It is also why #1260's taxonomy does not fit: a `user`/`tool_result` line *emits*,
  so it is not a dropped line and has no drop reason.
- `internal/e2e/realclaude/permission_protocol_spike_test.go` → `packageDir`,
  `captureClaudeVersion`, `versionSlug` — the version token and the `testdata/`
  path, both already shared package helpers.
- `internal/e2e/realclaude/fixtures.go` → `realHome`, `WithWorktreeAuthenticated`
  — the credential skip and the pinned temp `$HOME` that plays `tempHome` for
  both the redactor and the deny-scan.
- `internal/e2e/realclaude/interactive_background_idle_probe_test.go` →
  `bgIdlePrompt` — the "exactly once, verbatim, nothing else" prompt posture and
  the wall-clock nonce as a cache-buster (never a token). This probe's prompt
  mirrors that posture with a different body; `bgIdlePrompt` itself is not reused,
  because it drives a FIFO `cat` and this turn needs a file read plus a shell call.
- `docs/knowledge/features/e2e-realclaude.md` § Make target — `make e2e-realclaude`
  runs the package glob with **no `-race`**. That is why this probe must not be
  env-gated (an env-gated probe skips under that gate, and a skip is
  indistinguishable from a pass by exit code) and why `-race` is a local choice
  for the offline self-checks rather than something the gate supplies.

## Context

`internal/streamsup` parses claude's **stdout** under `--output-format stream-json`.
The 33 `toolUseResult` sidecars the downstream tool-row work is designed against
all come from `internal/agentrun/jsonl/testdata/*.jsonl` — transcript files, a
surface the daemon never reads. The four live tests that decode `toolUseResult`
(`sigterm_mid_tool_use_test.go`, `teardown_liveness_probe_test.go`,
`background_reach_probe_test.go`, `finding_staging_fill_test.go`) all reach it
through the session JSONL on disk.

Nothing in the tree records a stdout `user`/`tool_result` line carrying a sidecar
at all. The single real stdout `user` line the tree holds — `dropped_lines[26]`
in `internal/e2e/realclaude/testdata/dropped_lines_v2.1.220.json` — is the
synthetic harness nudge: it carries the transcript's envelope style
(`parent_tool_use_id`, `session_id`, `uuid`, `timestamp`, `isSynthetic`), which
makes a sidecar *plausible* there, but it carries no tool result and so proves
nothing about one.

A shape-keyed dispatch written against a sidecar that never arrives on stdout is
dead code, and no hermetic test catches it — hand-written fixtures lifted from the
transcript would pass against a decoder that never fires in production. This
ticket buys the observation before the decoder is written.

**No ADR is warranted.** This changes no design; it records a measurement.

## Design

One new file, `internal/e2e/realclaude/tool_result_sidecar_probe_test.go`, plus the
JSON it writes under `internal/e2e/realclaude/testdata/`. No production file is
touched. Every file-local identifier takes the `sidecap` prefix — siblings add
files to this package concurrently and a branch-overlap check does not catch a
same-package identifier collision (`dropped_line_capture_test.go`'s header states
this lesson; `sidecap` is confirmed unused package-wide).

### What is reused, and what is new

Reused unchanged from `dropped_line_capture_test.go`: `dropcapRecorder` (via
`newDropcapRecorder`), `newDropcapRedactor`, `newDropcapScanner`,
`dropcapJSONEscape`, `dropcapContains`, `newDropcapArgvHandler`,
`dropcapWaitForChild`. That is the whole spawn-and-retain-verbatim apparatus and
the whole redaction apparatus, which is what makes committing live stdout safe.

New here, and only this: the retention predicate, the per-line sidecar
inspection, the record type, the write path, and the applied-map completion.

### The retention predicate — the part that is not a one-line edit

#1260 retains lines that emitted **zero** events, and its taxonomy
(`ignored-line-type`, `user-block-suppressed`, `empty-message`, `undecodable-line`)
is a reason for that zero. A `user`/`tool_result` line is not in that taxonomy:
`emitUser` maps each `tool_result` block to a `turnevent.ToolUpdate`, so the line
*emits*. Retention here is therefore keyed on **line type** — `type == "user"` on
the decoded envelope — and keeps a line that did emit. No parser is run and no
drop reason is computed; `dropcapClassifyAll` and its helpers are deliberately
not reused.

### Spawn shape

`streamsup.New` + `Runner.Run`, with the recorder installed in `Config.Stdout` —
the slot `cmd/pyry`'s `newStreamRunnerFactory` gives `streamsup.NewParser`. So
"the production interactive stream-json shape" and "upstream of the parser" are
facts about the wiring, not arguments. `sidecapArgs` is `--model haiku
--dangerously-skip-permissions`, declared locally rather than borrowing #1260's
`dropcapArgs`, so an edit there cannot silently change this probe's spawn shape.
`spawn_shape` is *observed* from the runner's own "spawning claude" log record via
`newDropcapArgvHandler`, never transcribed.

### The turn

The workdir is a fresh directory under the pinned temp `$HOME`, deliberately not
a git repo, holding exactly **one rig-authored file** of fixed content. The prompt
takes `bgIdlePrompt`'s posture — exactly once, verbatim, nothing else — and asks
for one `Read` of that file and one `Bash` running a fixed `echo` of a rig-authored
marker, with a wall-clock nonce as a cache-buster (not security randomness; it must
not be "upgraded" to `crypto/rand`). Those two calls are chosen because the
transcript's two dominant sidecar key sets are the shell set (`stdout`, `stderr`,
`interrupted`, `isImage`, `noOutputExpected`) and the file set (`file`, `type`).

The turn ends on the recorder's `resultSeen` or on a budget, whichever first;
`terminated_on` records which.

### Per-line inspection — contracts

```go
// sidecapInspect reports what one retained stdout line carries. Presence is
// decided by KEY EXISTENCE on a map[string]json.RawMessage decode, never by a
// zero value: an absent `toolUseResult` and a `"toolUseResult": null` are
// different answers and this ticket exists to tell them apart.
func sidecapInspect(raw []byte) sidecapEntry
```

`sidecapEntry` fields, all recorded per retained line:

| field | what it answers |
| --- | --- |
| `index`, `payload` / `payload_b64`, `payload_encoding`, `payload_len_bytes` | AC1 — the verbatim line |
| `line_keys` | the envelope's own top-level key set, sorted |
| `tool_use_result_present` | AC2 — was the key there at all |
| `tool_use_result_is_object` | AC2 — was it a JSON object |
| `tool_use_result_kind` | `absent` / `object` / `array` / `string` / `number` / `bool` / `null` — what it was when not an object |
| `tool_use_result_keys` | AC2 — its top-level key set, sorted, when an object |
| `tool_result_block_count` | AC3 — how many `tool_result` blocks the line carried |
| `content_block_types` | the block types present, so a non-`tool_result` block is visible rather than silently uncounted |

`payload_encoding` is `json-string` or `base64`, carried on **every** entry, for
#1260's reason: Go's encoder rewrites invalid UTF-8 to U+FFFD, so a payload that
is not valid UTF-8 goes to base64 and says so. A base64 payload is invisible to a
scan of the marshalled record, so the writer scans the decoded bytes too.

`sidecapRecord` carries the provenance and census frame: `ticket`,
`claude_version`, `captured_at`, `is_capture`, `model`, `spawn_shape`,
`spawn_shape_delta`, `env_delta` (a fixed literal — `os.Environ()` is never read
into the record), `workdir`, `prompt`, `outcome`, `outcome_detail`,
`terminated_on`, `stdout_lines_captured`, `user_lines_retained`,
`user_lines[]`, the three cap counters, plus `sidecar_present_count`,
`sidecar_absent_count`, `observed_key_sets` (each distinct sorted key set with a
count), `tool_result_block_count_histogram`, `redaction`,
`redaction_rationale`, `credential_scan_applied` and `limitations`.

`observed_key_sets` and the histogram are what let a reader see AC3's
one-sidecar-per-block question answered off stdout rather than off the
transcript's 33-for-33.

### Outcomes and the failure surface

Four outcomes, mirroring #1260's vocabulary:

- `instrument-broken` — the only fatal class. Reached on `streamsup.New` failure,
  on no live child within the spawn wait, on a `WriteTurn` failure, or on **zero
  stdout lines captured**.
- `no-user-line` — stdout carried lines but none of type `user`. Recorded, not
  fatal; `sidecar_observation_valid` is false.
- `sidecar-absent` — `user` lines were retained and **none** carried a top-level
  `toolUseResult`. This is a **passing** outcome and is the answer the ticket most
  expects to get.
- `sidecar-present` — at least one retained `user` line carried the key.

**AC2's fail condition is read as "zero stdout lines captured", not "zero `user`
lines retained".** The alternative reading fails the run on claude's behaviour,
which contradicts AC2's own first sentence and this family's standing rule that
only a broken instrument or a fail-closed refusal is fatal (`#1260`'s
`dropcapClassifyOutcome`, `#1763`'s `runInitControlChild`). A turn that provoked no
tool call is information, and it lands in `outcome` where a reader sees it; it is
not an instrument failure. This is stated here because it is a design decision, not
an accident.

## Concurrency model

Two goroutines beyond the test's own, both inherited from #1260's rig and both
with an explicit exit:

- `Runner.Run(ctx)` on its own goroutine, closing `runDone` on return. A
  `t.Cleanup` cancels `ctx` and waits up to `sidecapRunExitWait` for `runDone`,
  reporting a `t.Errorf` if it does not return. This is the only goroutine that
  outlives the turn.
- `os/exec`'s internal stdout copier, which drives `dropcapRecorder.Write`. The
  recorder is mutex-guarded for exactly this reason: the copier writes while the
  test goroutine calls `snapshot()`, and that is a race under `-race`.

`dropcapRedactor`'s counters are unlocked and accumulate across calls. One
redactor is built, on the test goroutine, and every redaction runs there — after
the turn, for the record, and inline for each `t.Logf`. No redactor is shared with
another goroutine. `dropcapScanner` is append-only during construction and
read-only afterwards, so it is safe to hold by value; that asymmetry is #1747's
and is not licence to share a redactor.

`t.Cleanup` is LIFO, so the record writer is registered **first** in the test body
and therefore runs **last**: the resulting order is runner ctx cancelled → record
written, and a structural `t.Fatalf` still leaves the evidence on disk.

## Error handling

Every claude-side outcome is data. `t.Fatalf` is reserved for a broken instrument
or a fail-closed refusal:

- `streamsup.New` error, no live child within `sidecapSpawnWait`, `WriteTurn`
  error, zero stdout lines captured → recorded as `instrument-broken`, and the
  test fails.
- Marshal failure of the record → fatal; nothing is written.
- Deny-scan hit on the marshalled record, or on any base64 payload's decoded
  bytes → **fatal, and nothing at all is written** — not the fixture, not a
  partial. The message names the hit **class** only, never the matched value:
  printing it would put the very string being redacted into CI output the
  pipeline salvages.

**`Config.Stderr` stays nil, so the child's stderr is discarded by the `exec.Cmd`
itself** and never reaches the record, the run log or memory. That is stronger
than scrubbing it, and it is why `initControlScrubbed` is **not** reused here:
that guard exists because #1688's fixture *records* stderr in a `StderrCapture`
field, and this record has no such field. Capturing stderr in order to scrub it
would mean building a mutex-guarded buffer — `os/exec` drives `Config.Stderr`
from its own copier goroutine — to hold bytes nothing consumes. Do not add one.

**No payload, and no record, ever reaches `t.Logf`.** The deny-scan gates the
*file*; it does not gate the run log, which this pipeline salvages. A
`t.Logf("%+v", rec)` would therefore move every retained payload into a salvaged
log having bypassed the fail-closed net entirely — and it would do so even on the
run where the scan *refused* to write the file. Logging is restricted to counts,
outcome names, the applied map, the observed key sets, and redactor-passed
strings: `outcome_detail`, the fixture path and every error string go through
`red.str` before they are formatted, because an error from `streamsup.New` or
`WriteTurn` carries the workdir path. This rule is the only guard, which is why
it is stated at the site as well as here.

**The scanner value is credential-bearing.** `newDropcapScanner` reads
`CLAUDE_CODE_OAUTH_TOKEN` and `ANTHROPIC_API_KEY` into its needles, so a
`dropcapScanner` in scope is two live credentials in a struct. It must never be
formatted with `%v`, `%+v`, `%#v` or `%q`. Its `applied()` **map** is safe to print
and is the diagnostic worth printing — its keys are declared vocabulary and its
values are bools.

### The applied-map completion (AC4)

```go
// sidecapScanApplied returns s.applied() with every path class PRESENT, adding
// ONLY a missing key and leaving an existing true/false alone.
func sidecapScanApplied(s dropcapScanner) map[string]bool
```

`newDropcapScanner`'s two arming paths differ for an absent value.
`addDynamic` appends unconditionally, so a class handed `""` lands as `false`.
`addDynamicPath` goes through `dropcapPathSpellings`, which returns nil for `""` —
no needle is appended, and `applied()`, which builds its map by ranging the
needles, carries **no key at all** for that class. An omitted class reads exactly
like a class nobody thought about.

This probe writes straight into `testdata/` and mints no artifact directory, so it
calls `newDropcapScanner(home, "", workdir)` and `artifact_dir` is precisely the
class that would vanish. Passing `workdir` into the middle slot would arm
`artifact_dir` with the workdir path and leave `workdir` unarmed; minting a
directory to fill it would report an arming that never happened. The completion is
done **at this fill site**, not inside `dropcapScanner`, whose record shape is
already committed and shared with two other families — #1747 made the same call for
#1688.

Only a missing key is added. A completion that assigned `false` unconditionally
would report every armed class as armed-nothing while still satisfying the
`artifact_dir` check; the `workdir` control in the offline test below is its sole
red. Note this is the **deny-scan's** map, not `substitutions()` — that one drops
zero-count classes on purpose and is not what AC4 is about.

### Redaction rationale, and how it differs from #1260's

The primary defence is still by construction: a fresh workdir with no git repo,
a rig-authored prompt, a rig-authored `echo` marker, and `os.Environ()` never read
into the record. **One thing changes:** the workdir is no longer empty — it holds
one rig-authored file whose content this file declares as a constant. That is
deliberate (AC1 needs a file read) and it is stated in the record's
`redaction_rationale`, because the by-construction argument is weaker by exactly
that much and a reader deciding whether to publish the artifact has to be told.

On top of that, `dropcapRedactor` substitutes the declared path/identifier classes
into every string entering the record, and `dropcapScanner` is a fail-closed
deny-scan over the whole marshalled record plus every decoded base64 payload.

Deliberately kept: top-level types and subtypes, claude's structural fields, tool
names, tool-result key sets, the rig-authored file content and echo marker, token
counts and timestamps. Kept and operator-derived: a `system/init` line's
configuration inventory, if one is retained — it is not, since only `user` lines
are retained, which is a narrowing this probe gets for free over #1260.

## Testing strategy

The live probe asserts nothing about claude and cannot be the RED. Two offline
table-driven tests are, and they run under `make e2e-realclaude` with no claude
and no credentials:

- `TestSidecapInspect_*` — drives `sidecapInspect` over hand-written stdout line
  literals covering: sidecar absent; sidecar present as an object with the shell
  key set; present as an object with the file key set; present but `null`; present
  but a string; two `tool_result` blocks on one line; a `user` line whose only
  block is a `text` block. Asserts presence-vs-null separation, the sorted key
  set, and the block count. The `null` and two-block rows are the ones that fail
  against a naive `Unmarshal`-into-struct implementation.
- `TestSidecapScanApplied_RecordsAnArmedNothingClassForAnAbsentPath` — builds
  `newDropcapScanner(tempHome, "", workdir)`, asserts `artifact_dir` is present and
  `false` in the completed map, and — the staleness control — asserts `workdir` is
  present and **`true`**, so a completion that assigned `false` unconditionally is
  red.

Verification gate (§ B2), scoped to the touched package:

```
go vet -tags e2e_realclaude ./internal/e2e/realclaude/
go test -race -tags e2e_realclaude -run '^TestSidecap' ./internal/e2e/realclaude/
go build ./cmd/pyry
```

The live arm is run once by hand to produce the artifact
(`go test -tags e2e_realclaude -timeout 15m -v -run '^TestRealClaude_ToolResultSidecarCapture$'`),
and **the JSON it writes is `git add`ed in this PR**. #1763 is the failure not to
repeat: its gate ran green, spent real tokens, and landed zero of its three
artifacts, because a pipeline run's worktree is discarded when the run ends. A
green gate and a spent budget look identical whether the bytes landed or not.

## Out of scope

Named so the next reader does not look for them: no offline reader, no
fixture-name lock, no fully-populated record pin, no directory-injectable writer,
and therefore **no `offline_exec_ban_test.go` entry** — that registry keys only
offline files, and neither `initialize_control_probe_test.go` nor
`dropped_line_capture_test.go` appears in it. #1688's probe commit (`7bb3432a`)
touched exactly two paths, the probe and its JSON; this copies that shape. No
`probeOutcome` / `setModeFieldMatches` verdict machinery — one arm, no controls.
Pinning this artifact for reuse is follow-on work *only if* the observed shape
turns out to be worth pinning, which is a question this run answers rather than
presumes.

## Size

Six boundaries, re-counted against this written plan:

| Limit | Boundary | This plan |
| --- | --- | --- |
| Production source files created or modified | ≤ 5 | **0** |
| Total written work | ≤ 800 | **~1050** (≈560 probe test + 487 spec) — **exceeded** |
| New exported types or interfaces | ≤ 5 | **0** |
| Consumer call sites needing simultaneous update | ≤ 10 | **0** |
| Acceptance criteria | ≤ 5 | **5** |
| Distinct error/reject branches | ≤ 10 | **4** outcomes + 4 fatal arms |

**One line is exceeded: total written work, at ~1050 against a ceiling of 800.**
The ticket predicted ~1000 and stated the overage deliberately; this plan confirms
it rather than discovering it. Reusing #1260's recorder, redactor, scanner and argv
handler *as they are* took the probe from #1260's 1773 lines down to ~560, but the
spec came in at 487 — of which ~90 is the § Security review that the
`security-sensitive` label makes mandatory and that no split would remove, since
each child would need its own.

**This is not split, because the floor beats the ceiling here.** Every seam
available is a one-consumer seam: a record type only the next slice reads, a
fixture name minted for one caller, a writer with one consumer. That is measured,
not asserted — the initialize family took 4 tickets and 1114 lines of prerequisite
apparatus to commit one capture, and the AskUserQuestion family took 6 tickets and
3057 hand-written lines to commit a 24-line JSON. Merged, this is one ticket
answering one empirical question and committing one file, and none of its parts
changes anything observable on its own.

The two costs are not symmetric. The ceiling protects against a budget miss, which
costs one continuation leg. The floor protects against a ticket that cannot be
verified on its own, which no resume fixes. The overage is stated here, in the
plan, so it is a recorded judgement rather than an accident the verifier has to
reconstruct from the diff.

## Open questions

1. **Does the sidecar arrive on stdout at all?** That is the ticket. Both answers
   are recorded passing outcomes; only the artifact settles it.
2. **Does haiku reliably take both tool calls in one turn?** If it takes only one,
   the record still lands and says which key sets were observed. If it takes
   neither, `outcome` is `no-user-line` and the run passes with an explicitly
   invalid observation. Resolve at implementation by reading the live run's log
   line; if the turn under-provokes, tighten the prompt rather than the failure
   surface.
3. **Is a `tool_result` block ever accompanied by more than one sidecar, or a
   sidecar by more than one block?** `tool_result_block_count_histogram` plus
   `sidecar_present_count` answer it for this turn; a cross-version answer is
   explicitly outside this ticket's `limitations`.

## Security review

**Verdict:** PASS (after two MUST FIX findings were addressed in the plan above).

**Findings:**

- [Trust boundaries] No findings. One boundary — claude's stdout → parent memory →
  redacted record → committed public artifact — with a single ingest
  (`dropcapRecorder.Write`) and a single egress (`sidecapWriteRecord`). Both are
  named types, not scattered parsing. `sidecapInspect` reads only the decoded
  envelope's key sets and never re-emits payload bytes into a new field.
- [Trust boundaries] **MUST FIX — addressed.** The run log is a *second* egress
  the deny-scan does not cover, and the plan as first written did not name it. A
  `%+v` of the record moves every retained payload into a pipeline-salvaged log
  having bypassed the fail-closed net — including on the run where the scan
  refused to write the file. § Error handling now forbids logging the record or
  any payload, and routes `outcome_detail`, the fixture path and every error
  string through `dropcapRedactor.str`.
- [Tokens] No findings on storage: no token is generated, stored or rotated here.
  `newDropcapScanner` holds `CLAUDE_CODE_OAUTH_TOKEN` and `ANTHROPIC_API_KEY` as
  deny-scan needles, so a `dropcapScanner` in scope is two live credentials in a
  struct; § Error handling forbids formatting it and permits its `applied()` map,
  whose keys are declared vocabulary and whose values are bools. Both values are
  also armed as needles, so a credential echoed into a payload refuses the write.
- [Tokens] **MUST FIX — addressed.** The plan promised a stderr credential scrub
  modelled on `initControlScrubbed`, which is unimplementable as designed: this rig
  spawns through `streamsup.New`, whose `Config.Stderr` is nil and whose child
  stderr therefore goes to `/dev/null` — there is nothing to scrub, and capturing
  it in order to scrub it would add a mutex-guarded buffer holding bytes nothing
  consumes. Replaced with the by-construction statement, plus why
  `initControlScrubbed` does not transfer (#1688 records stderr in a
  `StderrCapture` field; this record has none).
- [File operations] No findings on traversal, and the reason is `versionSlug`, not
  luck: `claude --version` is subprocess output flowing into a filename, but the
  slug rewrites `[^a-z0-9._-]+` to `_` and the name is built as a fixed prefix plus
  the slug plus `.json`, so the joined element can never begin with `../`. A `..`
  token yields the ordinary filename `tool_result_sidecar_v...json`.
- [File operations] SHOULD FIX — write the artifact via `os.CreateTemp` in the
  destination directory plus `os.Rename`, at mode `0o600`, removing the temp on
  any failure. A non-atomic write can leave a partial JSON in `testdata/` that the
  run then `git add`s, and a leftover `.tmp` is itself commit bait. `writeFixture`
  is the in-package precedent for temp-plus-rename; `0o600` follows
  `dropcapWriteRecord` rather than `writeFixture`'s `0o644`, which costs nothing
  because git records only the exec bit.
- [Subprocess execution] No findings, but the exposure is named rather than
  glossed: `--dangerously-skip-permissions` means claude may run any shell command
  with the operator's privileges, and the blast radius is the machine, not the
  workdir. No user-controlled value reaches argv — the model and both flags are
  literals and the prompt travels over stdin, so nothing is shell-interpreted.
  `Config.Env` stays nil so the child inherits verbatim (this is how it
  authenticates) and `env_delta` is a fixed literal, never harvested from
  `os.Environ()`. This is #1260's established shape for this exact rig and
  question, not a new exposure.
- [Cryptographic primitives] Not applicable, by a design decision worth stating so
  it is not "fixed" later: the only randomness is `time.Now().UnixNano()` as a
  prompt cache-buster. It is not security randomness, must stay wall-clock, and
  must not be read as a token.
- [Network & I/O] No findings. No sockets. Ingest is bounded by
  `dropcapMaxPartial` (4 MiB accumulator) and `dropcapMaxCaptureBytes` (8 MiB
  total, dropping whole lines and counting them, never truncating). Residual,
  accepted: a retained `user` line has no per-entry cap, so a large `Read` result
  could inflate the artifact up to the 8 MiB total. AC1 requires verbatim
  retention, so truncation is not the fix; the rig-authored file is small by
  construction and the three cap counters make any drop non-silent.
- [Error messages, logs, telemetry] No findings beyond the MUST FIX above. The
  deny-scan failure names the hit class only — printing the matched value would put
  the very string being redacted into salvaged CI output.
- [Concurrency] No findings. Two goroutines: `Runner.Run` (cancelled by
  `t.Cleanup`, waited on with `sidecapRunExitWait` and a `t.Errorf` on overrun) and
  `os/exec`'s stdout copier (whose race with `snapshot()` is what
  `dropcapRecorder`'s mutex exists for). The redactor's unlocked counters stay on
  the test goroutine, where cleanups also run; the scanner is append-only during
  construction and read-only after, so holding it by value is safe. Cleanup LIFO
  gives cancel-then-write, so a structural `t.Fatalf` still leaves evidence on
  disk. #1260's parked rendezvous goroutine has no analogue here — this probe has
  no FIFO.
- [Threat model alignment] `docs/protocol-mobile.md` § Security model is not
  applicable — nothing here touches the relay. The governing threat is publishing
  operator-derived data into a public repo, and this probe is a genuine narrowing
  of #1260's exposure rather than a copy of it: retention is keyed on `type ==
  "user"`, so a `system/init` line — #1260's largest operator-specific class, the
  local MCP/tool/skill/subagent inventory — is never retained here at all. The
  residual widening in the other direction is the workdir file, named below.
- [Threat model alignment] SHOULD FIX — the by-construction defence is weaker than
  #1260's by exactly one file: that probe's workdir is empty, and this one holds a
  rig-authored file because AC1 needs a file read. Content and marker are declared
  constants in this file, so nothing operator-derived is introduced, but the record's
  `redaction_rationale` must say so rather than inherit #1260's "fresh empty
  directory" wording verbatim. The verifier should check that string.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-02

## Revisions

### 2026-09-02 — the sidecar is on stdout under a different spelling

**What changed:** `sidecapInspect` searches **two** spellings of the sidecar key,
`toolUseResult` and `tool_use_result`, and records per line which one was seen
(`tool_use_result_key`, `tool_use_result_spellings`) plus a record-level
`sidecar_key_spelling_census`. The plan above specified a single key, `toolUseResult`,
because that is the only spelling the 33 transcript sidecars use.

**What drove it:** the first live run, against claude 2.1.239. It returned
`outcome: sidecar-absent` — no line carried `toolUseResult` — and that answer was
literally true and materially wrong. The retained lines' own `line_keys` carried
`tool_use_result`, and its value was the transcript's file key set verbatim:
`{"type":"text","file":{"filePath":…,"content":…,"numLines":…,"startLine":…,"totalLines":…}}`.
**The sidecar is on stdout. Stdout spells it in snake_case.**

**Why this was not left as a follow-up.** An `Open questions` entry above asks
whether the sidecar arrives on stdout at all, and a committed artifact answering
"absent" is exactly the input that would tell the downstream decoder ticket not to
build a decoder. That is the *inverse* of the dead-code failure this ticket exists
to prevent, and it would have been just as invisible: the artifact would have been
internally consistent, the gate green, the budget spent. A one-spelling instrument
is the defect, not the ticket's scope.

**Result after the change**, same claude version, one live turn:

| | |
| --- | --- |
| outcome | `sidecar-present` — 2 of 3 retained `user` lines |
| spelling census | `{"tool_use_result": 2}` — **zero** camelCase |
| observed key sets | `[file, type]` and `[interrupted, isImage, noOutputExpected, stderr, stdout]` |
| block histogram | `{"0": 1, "1": 2}` — one `tool_result` block per sidecar-bearing line |

So the two key sets the transcript's shape table predicts for a file read and a
shell call **do** reproduce on stdout, byte-shape intact, under a snake_case key.
Open question 1 is resolved present-with-a-caveat; open question 3 is resolved for
this turn at one sidecar per block, on a stdout observation rather than on the
transcript's 33-for-33.

### 2026-09-02 — the writer declines to write on an instrument-broken run

**What changed:** `sidecapWriteFixture` returns without writing when
`stdout_lines_captured` is zero, logging the outcome instead. The plan's § Outcomes
made `instrument-broken` fatal but did not say whether a fixture lands.

**Why:** #1260 writes its record on every path because it writes to a temp artifact
directory an operator inspects by hand. This writes into `testdata/`, which the run
`git add`s, so a record with no captured lines would commit a worthless artifact
under a filename claiming to be a capture of claude 2.1.239. The outcome still
reaches the run log, which is where a broken instrument needs to be visible.
