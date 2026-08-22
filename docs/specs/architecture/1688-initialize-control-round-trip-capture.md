# #1688 — Measure the `initialize` control-request round trip and commit the response capture

**Ticket:** [#1688](https://github.com/pyrycode/pyrycode/issues/1688) · **Size:** `s` · **Labels:** `needs-real-claude` (live run), test-only

One new file: `internal/e2e/realclaude/initialize_control_probe_test.go`. Zero production files.
One machine-generated artifact committed alongside it: `internal/e2e/realclaude/testdata/initialize_control_v<slug>.json`.

---

## Files to read first

This is the turn-1 data load. Read these before writing anything; every symbol below is
resolvable with `codegraph_search` / `codegraph_node`.

**The substrate this ticket writes through — already landed, do not re-derive:**

- `internal/e2e/realclaude/initialize_control_names_test.go` → `initControlFixtureName` — the
  namer. Takes ONE input (the version token), mints `initialize_control_v<slug>.json`. Never
  format the name yourself; the writer already calls this.
- `internal/e2e/realclaude/initialize_control_record_test.go` → `initControlFixtureRecord` — the
  22-field JSON contract this run fills. Read its doc comment in full: it states which fields are
  POPULATED-never-computed (`ModelsPresent` / `ModelsCount` / `ModelsEntryFields`) and that
  nothing in that file caps anything.
- `internal/e2e/realclaude/initialize_control_writer_test.go` → `writeInitControlFixture` — the
  directory-injectable atomic writer. **Signature is `(t, dir, rec)`** — this ticket is the caller
  that finally passes the real `testdata/`. Its doc states the `StderrCapture` cap lands on the
  writer's local copy; that is why this run hands it raw stderr (see § Error handling).

**The precedent this file mirrors — reuse from it, do not clone it:**

- `internal/e2e/realclaude/set_permission_mode_probe_test.go` → `runSetModeChild` — the whole
  driver shape: `exec.CommandContext` + `StdinPipe`/`StdoutPipe`, one reader goroutine over a
  `bufio.Scanner`, write-then-wait per step, fixture write, `t.Logf` summary. Read this end to end;
  the new driver is this minus the arm table.
- same file → `setModeRecorder` — reuse verbatim. It retains every stdout line as raw JSON,
  classifies `control_response`, counts non-JSON lines, and tracks turn boundaries. Its
  `initModes` field is unused here and that is fine.
- same file → `setModeWaitFor` — the polling idiom. Reuse; it carries the poll interval.
- same file → `setModeTurnLine` — mints the user-turn line in pyry's production envelope shape.
  Reuse.
- same file → `setModeResponseIDMatches` — checks the correlation id at BOTH placements. Reuse; it
  is what keeps `control_response_request_id_matched` honest.
- same file → `setModeScanMax` — the 1 MiB per-line cap and its stated rationale. Reuse; a second
  1 MiB constant in the same package for the same reason is restatement.
- same file → `setModeControlRequest`, `setModeControlRequestInner` — read them to see the shape,
  then **do not reuse them** (see § Design, "the request line").

**Shared package helpers:**

- `internal/e2e/realclaude/permission_protocol_spike_test.go` → `captureClaudeVersion`,
  `versionSlug`, `packageDir`, `truncateString`, `stderrFixtureCap` — version capture, the slug the
  namer applies, the `testdata/` path root, and the fatal-message truncator.
- `internal/e2e/realclaude/resilience_test.go` → `resolveClaudeBin` — `t.Skip` when claude is absent.
- `internal/e2e/realclaude/fixtures.go` → `WithWorktreeAuthenticated` — pinned `$HOME`, `t.Skip`
  when there are no credentials.
- `internal/e2e/realclaude/offline_exec_ban_test.go` → `finOfflineExecBans` — read the loop that
  consumes it: it is `for f := range finOfflineExecBans`, **registry-keyed, not directory-swept**.
  This file execs, so it gets NO entry, and the absence costs nothing.

**Production shape being mirrored:**

- `internal/streamsup/runner.go` → `buildArgs` — the fixed
  `--input-format stream-json --output-format stream-json --verbose` prefix. That prefix is what
  AC 1 means by "pyry's stream-json-in/stream-json-out argv"; the cost flags this file adds occupy
  the `base` slot.

**Docs:**

- `docs/knowledge/features/e2e-realclaude.md` § the `initialize_control_*` entries (#1696, #1701,
  #1702, #1700) — what the substrate already proves, so this run does not re-prove it.
- `docs/knowledge/features/set-permission-mode-inband-probe.md` — the measurement writeup for the
  arrangement this ticket inherits: a control request written after a completed turn got a
  `control_response` back on both arms at 2.1.220.

---

## Context

The daemon needs to publish claude's model list to connected clients. The list needs no
credential and no HTTP endpoint — the child the daemon already supervises hands it over when
asked: a `control_request` with subtype `initialize`, written on the held-open stdin, comes back
with a `models` array. That was measured **by hand, outside this repo**, against claude 2.1.220 on
2026-08-21. Nothing in the tree records the request line claude accepts or the response shape, and
three slices downstream (#1689, #1690, #1692) decode against it.

#1695's split (#1696 namer, #1701 record, #1702 writer, #1700 the cap proof) built the offline
substrate with no claude binary and no tokens. This ticket is the live run that fills the record
and commits the bytes.

Scope is ONE arrangement: the request written **after a completed turn**. That is not a guess —
`runSetModeChild` writes its control request at exactly that point and got a `control_response`
back on both measurement arms. Whether the request is also answered before the first user turn,
and whether the round trip perturbs the live session, need a three-arm rig with a no-request
control and were carved out to #1694.

**No ADR.** This adds no decision — it records a measurement. The finding belongs in the package
overview, which the documentation phase owns.

---

## Design

One new file, `internal/e2e/realclaude/initialize_control_probe_test.go`, holding six declarations.

### Line budget — check against this, it is the size boundary

| piece | budget | derived from |
|---|---|---|
| file header | 60 | `set_permission_mode_probe_test.go`'s header is ~138. This file has one arm and no verdict, so most of what that header explains does not exist here |
| constants | 28 | seven, one of them reused |
| `initControlRequest` + inner + `initControlLine` | 24 | `setModeControlLine` and its two structs are ~35 with the `mode` field and its prose |
| `initControlSummary` + `initControlSummarize` | 48 | new; nothing in the package reads `models` |
| `initControlScrubbed` | 12 | the credential guard, § Security review MUST FIX 1 |
| `runInitControlChild` | 130 | `runSetModeChild` is 178 with the arm table, the arm-conditional control block and the `ProbeOutcomes` wiring, all of which are dropped |
| `TestRealClaude_InitializeControl_Capture` | 45 | one child, no subtests, no verdict matrix |
| optional summariser check (§ Testing strategy) | 20 | first thing to cut if the total runs over |
| **total** | **367** | boundary is 400 |

The margin is ~33 lines. The two ways to spend it and not notice are a header that grows toward
the precedent's 138 and a summariser check that grows into a unit suite. Both are capped above.

**On the error-branch count, stated rather than waved past:** the driver carries ~11 ordinary
`if err != nil` sites (three pipe/start checks, two marshal checks, two waits, the credential
guard, the zero-lines guard, the stdin close, the zero-response assertion). That is not the
state-machine reject fan-out the size boundary's last row is about — there is no state machine
here — and every one of those but the credential guard already ships inside `runSetModeChild`, in
a ticket that landed at this size. Each is three lines and none carries a bespoke log call.

### What is NOT built

The ticket is explicit and the boundary is what keeps this at `s`. Do not port:

- `probeOutcome`, its `equal`/`String` methods, `setModeProbeOutcome`, `setModeTurnWindows`,
  `setModeOutcomeAt`, `setModeFieldMatches`, `setModeDirections`, `setModeArms`. That is ~250
  lines of machinery for classifying measurement arms against control arms. **This ticket has one
  arm and no controls.** Copying that structure is what would blow the size; the capture itself
  does not.
- `inbandTapRecorder`. It retains no payloads — only the `model` field of `system`/`init` lines and
  a count of `result` lines — and does not classify `control_response` at all. Its own doc says it
  is deliberately an assertion instrument, not a forensic one. `inbandWaitResults` is typed to it
  concretely and is likewise unusable here.
- A second recorder. `setModeRecorder` already does everything AC 1–3 need.

### Constants (~28 lines)

Seven, all file-local except the reader cap:

| name | value | why |
|---|---|---|
| `initControlWorkdirName` | `"initialize-control-work"` | a fresh empty directory under the pinned `$HOME`, deliberately not a git repo — less project context to load, so the turn is cheaper |
| `initControlPrompt` | a one-line, tool-free instruction | the probe turn exists only to reach the after-a-completed-turn arrangement. **No Bash tool**, unlike #1595's probe: a tool-free turn cannot stall on a permission prompt, so no `--dangerously-skip-permissions` is needed and the `default`-posture hang #1595 budgets for cannot happen |
| `initControlModel` | `"claude-haiku-4-5"` | cost. The model list claude reports is a property of the binary, not of the model answering the probe turn |
| `initControlMaxTurns` | `"4"` | cost guard with headroom over a single assistant turn. A `result` line carrying `subtype:"error_max_turns"` lands in the fixture plainly — raise and rerun |
| `initControlChildBudget` | `3 * time.Minute` | hard kill per child; the outer bound. The per-step waits below can sum past it, in which case the deadline trips and `context_deadline_tripped` records that rather than the run hanging |
| `initControlTurnBudget` | `90 * time.Second` | the one probe turn. A tool-free turn lands in seconds |
| `initControlControlBudget` | `45 * time.Second` | long enough that an absent `control_response` means absence, not impatience. Same value and same reason as `setModeControlBudget` |

`setModeScanMax` is reused for the reader buffer. `setModeWaitFor` carries the poll interval, so no
poll constant is declared here.

### The request line (~24 lines)

`initControlRequest` / `initControlRequestInner` + `initControlLine(requestID string) ([]byte, error)`.

Returns the single newline-terminated line, marshalled structured and never string-concatenated,
so the appended `'\n'` is the only raw newline in the envelope:

```json
{"type":"control_request","request_id":"<id>","request":{"subtype":"initialize"}}
```

**A fresh two-field inner struct, not `setModeControlRequestInner`.** That type carries
`Mode string \`json:"mode"\`` with no `omitempty`, so reusing it emits `"mode":""` and the sent line
stops being the minimal shape the ticket mandates. Adding `omitempty` to it instead would change
what `control_request_sent` reads in #1595's four committed fixtures. Twenty-four lines is the
correct price; neither alternative is.

`requestID` is a fixed literal (`"initialize-control-1"`), not a counter: a correlation token, not
a security token, and a stable one keeps the committed fixture diffable. The same string goes into
`ControlRequestID`, so `setModeResponseIDMatches` has something real to correlate against.

**Send the minimal shape and do not iterate.** If claude answers `subtype:"error"` complaining
about a missing or malformed field, that error text is a recorded passing outcome and the input to
#1689. Do not go hunting for an accepted shape against a live child — the wall-clock and token risk
in this ticket is the live run, not the typing.

### The response read (~48 lines)

One unexported struct and one function:

```go
type initControlSummary struct {
	subtype           string
	modelsPresent     bool
	modelsCount       int
	modelsEntryFields []string
}

func initControlSummarize(responses []json.RawMessage) initControlSummary
```

Behaviour, one sentence: decode each response's envelope reading `subtype` and `models` at **both**
placements — top level and nested under `response` — and return the first non-empty subtype and the
first non-null `models` array found, with the array's entry count and the sorted union of the field
names its entries carry.

Three points, each of which is a way to get this silently wrong:

1. **Both placements, per the ticket's technical note.** `internal/streamsup/parser.go`'s
   `control_response` arm records, as measured shape, that `subtype` and `request_id` are nested
   under `response` rather than carried at top level. `setModeResponseIDMatches` already checks
   both positions for `request_id`; this helper applies the same discipline to `subtype` and
   `models`. A reader that checks only the top level reports a false absence.

2. **`present` is decided on the raw bytes, not on the decoded slice.** Decode the `models` field
   into a `json.RawMessage` first: present iff it is non-empty and not the four bytes `null`. Then
   unmarshal that into `[]map[string]json.RawMessage` for the count and the keys. Going straight to
   a slice makes `"models":[]` and an absent `models` both decode to a nil slice, and the
   difference between "claude has no models to report" and "claude reported no models array" is
   exactly what #1690 needs. If the second unmarshal fails, `modelsPresent` stays true with a zero
   count — the raw bytes are in `control_responses` verbatim either way.

3. **Sort the union.** Go's map iteration is randomised, so an unsorted key union writes a
   different byte sequence into the committed fixture on every run. Dedupe, then `slices.Sort`.

**`modelsEntryFields` is a union across entries, and it must be documented as one.** The by-hand
2026-08-21 table shows entries that differ — `Haiku` carried neither `supportsAutoMode` nor
`supportedEffortLevels` while the other four did. `initControlFixtureRecord.ModelsEntryFields` is a
flat `[]string` fixed by #1701, so a union is the only shape that fits it, and a union
**over-reports**: it names every field some entry carries, not every field every entry carries. Say
so in the helper's doc comment, because #1690's decoder is designed against this field and a
decoder that reads it as a per-entry guarantee will nil-deref on `Haiku`. The per-entry truth is
preserved verbatim in `control_responses`; that is the ground truth, and this summary is a
convenience over it.

### The driver (~120 lines)

```go
func runInitControlChild(t *testing.T, claudeBin, workdir, versionRaw, versionToken string) *initControlFixtureRecord
```

Spawns one child, drives the sequence, writes the fixture, returns the completed record.
Structurally `runSetModeChild` with the arm parameter and the arm branches removed. Sequence:

```
argv := --input-format stream-json --output-format stream-json --verbose
        --model <initControlModel> --max-turns <initControlMaxTurns>

exec.CommandContext(ctx=initControlChildBudget, claudeBin, argv...)   cmd.Dir = workdir
  ├─ cmd.StdinPipe()   held open across the whole sequence
  ├─ cmd.StdoutPipe()  → one reader goroutine: bufio.Scanner, Buffer(64 KiB, setModeScanMax)
  │                      → setModeRecorder.add per line; scanner.Err() → scannerErr
  └─ cmd.Stderr = &bytes.Buffer

 1. write setModeTurnLine(initControlPrompt)
 2. setModeWaitFor(rec.resultCount, 1, initControlTurnBudget)      — timeout: t.Logf, continue
 3. write initControlLine(requestID)                                — t.Logf the line as sent
 4. setModeWaitFor(rec.controlResponseCount, 1, initControlControlBudget) — timeout: t.Logf, continue
 5. stdinPipe.Close() → cmd.Wait() → <-readerDone
 6. initControlScrubbed(t, stderrBuf.String())     — the credential guard, before ANY use of stderr
 7. assemble the record → writeInitControlFixture(t, packageDir(t)/"testdata", rec)
```

The `--model` and `--max-turns` flags occupy the `base` slot that `buildArgs` appends after the
fixed prefix, so the argv is production's prefix plus two cost guards. `Argv` in the record is
`append([]string{claudeBin}, argv...)`, matching `runSetModeChild`.

`ControlRequestSent` is the sent line with the trailing newline trimmed, so the fixture field holds
one valid JSON value — `bytes.TrimRight(line, "\n")`, as `runSetModeChild` does.

The reader goroutine exits on EOF, which follows either the stdin close at step 5 or the context
kill, and closes `readerDone`, so no goroutine outlives its child. `scannerErr` is written only
before that close and read only after it — that ordering is the whole synchronisation and there is
no mutex on it; do not add a channel send or a second goroutine.

### The credential guard (~12 lines)

```go
func initControlScrubbed(t *testing.T, stderr string)
```

`t.Fatalf`s when `stderr` contains the non-empty value of `CLAUDE_CODE_OAUTH_TOKEN` or
`ANTHROPIC_API_KEY`; returns silently otherwise. An unset variable is skipped, never compared
against `""` — every string contains the empty string, so comparing it would fail every run.

This exists because **this is the first fixture in the `initialize_control_*` family to carry real
claude stderr, and the file it writes is committed to a public repo.** `WithWorktreeAuthenticated`
puts a live credential in the child's environment, and an auth failure is exactly the condition
that makes claude print a long message to stderr. `stderrFixtureCap` bounds how much of it lands
in the file, and #1700 proves that bound — but **a cap is not a redaction**: 8 KiB of a
credential-bearing message still commits the credential. `finOfflineExecBans`' own header calls
this hazard a credential guard and says it is not tidiness; this is the same guard at the one call
site that writes live stderr to disk.

Three details, each of which makes the difference between a guard and a decoration:

- **It runs at step 6 — after the `<-readerDone` join, before the zero-stdout-lines fatal, before
  the record is assembled, and before the write.** After the join so its `t.Fatalf` cannot strand
  the reader goroutine; before the fatal because that message prints 8 KiB of stderr into the run
  log, which the dispatcher salvages; before the write because a poisoned fixture must never reach
  disk at all, not even to be deleted afterwards.
- **It checks the raw stderr, not the capped copy.** The raw is a superset, so a token past the cap
  still fails the run. Stricter and simpler than reasoning about where the cut lands.
- **Its failure message names the variable and prints nothing else.** No stderr excerpt, no offset,
  no record dump. An error message that helpfully quotes the leak is the own-goal this guard
  exists to prevent.

Plain `strings.Contains`, not `crypto/subtle`. Constant-time comparison defends a secret against an
attacker who does not know it; here the only party on the other side is the claude binary, which
was handed the token. Timing buys it nothing.

**Step 2's timeout is recorded, not fatal.** AC 2 closes the failure list with "only": no
`control_response` at all, a spawn failure, or zero stdout lines. A probe turn that never produced
a `result` line is none of those. It does mean the capture was taken off-arrangement, and
`turn_boundaries` in the committed fixture is what tells #1694 so; make the `t.Logf` say that in
words, so a reader of the run output does not have to infer it from an empty array.

### The test (~45 lines)

```go
func TestRealClaude_InitializeControl_Capture(t *testing.T)
```

`resolveClaudeBin` (skips when claude is absent) → `WithWorktreeAuthenticated` (skips when there
are no credentials) → create the workdir under the pinned `$HOME` → `captureClaudeVersion` →
`runInitControlChild` → summarise → assert.

No `t.Parallel`, no subtests: one child, one reader goroutine, one pinned `$HOME`.

The verdict, in order, and the order matters:

1. `runInitControlChild` has already written the fixture. **The evidence lands on disk before any
   assertion runs**, so a run that fails still leaves an artifact a human can read.
2. `t.Logf` the written path, the line count, the response count, the subtype, the models summary,
   the exit code and the duration — plus each `control_response` verbatim, as
   `runSetModeChild` does.
3. `if len(rec.ControlResponses) == 0 { t.Fatalf(...) }` — AC 2's one real assertion.
4. Everything else passes. A `subtype:"error"` is a recorded refusal and a **passing** outcome; say
   that in the test's doc comment, because a reviewer reading `ControlResponseSubtype == "error"`
   in a green run will otherwise read it as a bug.

---

## Concurrency model

Two goroutines and one channel, the whole of it inherited from `runSetModeChild`:

- **The test goroutine** owns the stdin pipe and writes every line on it. Nothing else writes.
- **One reader goroutine** owns the stdout pipe, splits with `bufio.Scanner`, and calls
  `setModeRecorder.add`. `setModeRecorder`'s own mutex is what makes the recorder's counters safe
  to poll from the test goroutine while the reader appends.
- **`readerDone`** closes when the reader returns. `<-readerDone` after `cmd.Wait()` is the
  join, and it is what makes reading `scannerErr` race-free.

Shutdown: `stdinPipe.Close()` → child sees EOF → child exits → `cmd.Wait()` returns → stdout pipe
closes → scanner returns false → reader closes `readerDone`. The `context.WithTimeout` on
`initControlChildBudget` is the backstop when the child does not exit on its own; its firing is
recorded in `context_deadline_tripped`, never swallowed.

`go test -race` is the gate. Run it.

---

## Error handling

The design principle is the one `runSetModeChild`'s doc states: **`t.Fatalf` only for a broken
instrument; every other outcome is information and lands in a fixture field.**

Fatal — nothing to capture:

| condition | why fatal |
|---|---|
| `cmd.StdinPipe` / `cmd.StdoutPipe` / `cmd.Start` fails | no child, no capture. AC 2 names spawn failure |
| `initControlLine` or `setModeTurnLine` marshal fails | a programmer error in this file |
| `initControlScrubbed` finds a credential in stderr | the artifact and the run log would both carry it. Runs first of the three below, and names only the variable |
| zero stdout lines captured | AC 2 names it. The message prints `truncateString(stderrBuf.String(), stderrFixtureCap)` and the wait error, and **nothing else** — and only ever after `initControlScrubbed` has passed |
| zero `control_response` after the fixture is written | AC 2: "there is no artifact to commit". Asserted in the test, after the write |

Recorded, never fatal — each has a field:

| condition | field |
|---|---|
| probe turn produced no `result` line | `turn_boundaries` empty, plus a `t.Logf` saying the arrangement did not hold |
| `control_response` carried `subtype:"error"` | `control_response_subtype` |
| response carried no `models` array | `models_present: false` |
| response's `request_id` did not correlate | `control_response_request_id_matched: false` |
| a stdin write failed | `stdin_write_errors` |
| a stdout line exceeded `setModeScanMax` | `scanner_error` — this is AC 3. `bufio.Scanner` returns `bufio.ErrTooLong` from `Err()` and stops; the field carries it, so a lost line is never silent |
| child exited non-zero / `Wait` errored / deadline tripped | `exit_code`, `wait_error`, `context_deadline_tripped` |

**Two things that look like bugs and are not — do not "fix" either:**

1. **Do not pre-truncate `StderrCapture`.** Hand `stderrBuf.String()` to the record raw.
   `writeInitControlFixture` applies `capFixtureCapture` on its own local copy, which is #1702's
   design and #1700's proof. `runSetModeChild` truncates at the call site because *its* writer has
   no cap; copying that here duplicates the bound in two places for no gain. The record in memory
   holds the raw string; the file on disk holds the capped one.
2. **Never `%+v` the record into a log or a fatal message.** That moves up to `stderrFixtureCap`
   bytes of child output out of the bounded file and into an unbounded run log — the exact thing
   the cap exists to prevent. `writeInitControlFixture`'s doc says this about its own failure path;
   it applies to every log line in this file.

---

## Testing strategy

This is itself the test. What has to be verified before the PR:

1. **`make preship`**, not `make check`. The package is behind the `e2e_realclaude` build tag, so
   `make check` never compiles it — the package can fail to build while the standard gate passes
   honestly. The new file's `initControlLine` / `initControlSummarize` compile only under
   `make preship` or a `-tags e2e_realclaude` run.
2. **Read the count of tests that ran, never the exit code.** A missing credential skips every live
   test and exits 0; a broken build runs zero tests and exits 0 through a shell wrapper. Count the
   `=== RUN` lines. A healthy full live run is in the 700s as of August 2026. Report that number.
3. **`initControlSummarize` is the one piece of logic here worth a targeted check**, and it can be
   checked without a credential — call it against three hand-written response literals: subtype and
   `models` at the top level, the same nested under `response`, and a response carrying neither.
   Confirm the union is sorted and that `"models":[]` reads as present-with-zero. Keep it inside
   the live test file rather than adding a second file; if it costs more than ~25 lines, drop it —
   the round trip against a real child is the deliverable, not a unit suite for the summariser.
4. **Offline gates already green.** `make cite-guard` (name symbols, never lines — this package's
   `finOfflineExecBans` prose is the model), `gofmt`, `go vet` under the tag.

### Producing the committed artifact

The fixture is machine-generated (#1595's four run 946–1027 lines each) and is not written work.

1. Run the live test with credentials present. Confirm from the output that it **ran** — the
   `=== RUN` line for `TestRealClaude_InitializeControl_Capture` and no skip reason.
2. Confirm the test **passed**. The run logs the written path.
3. **Open the produced JSON and read two fields before staging it.** `initControlScrubbed` already
   failed the run on a literal credential, so this is the second layer over what a literal match
   cannot see:
   - `stderr_capture` — anything credential-shaped, and anything that reads as an auth failure
     rather than ordinary child noise, means do not commit. Rotate if in doubt; a token in a public
     repo is a rotation event whether or not anyone reads it.
   - the file's line count — #1595's four fixtures run 946–1027 lines each on a two-turn Bash
     probe. This probe is tool-free and single-turn, so a file far past that is a signal the child
     did something unexpected, not an artifact to commit. `stdout_events` is uncapped in line count
     by design (#1701: capping structured evidence destroys the artifact), and `--max-turns` plus
     `initControlChildBudget` are the only bounds on it.
4. `git add internal/e2e/realclaude/testdata/initialize_control_v<slug>.json` — that one path, not
   the directory. A failed run also leaves a file at that name (by design, § the test, step 1), and
   committing a no-response capture is exactly the artifact #1690 must not decode against.
5. Paste the response's subtype, the models count and the field union into the PR body. That is the
   measurement, and it is what #1689/#1690/#1692 read before their own runs exist.

---

## Open questions

Each is answered by the capture itself, which is the point of the ticket. None blocks the build.

- **Where does `models` sit — top level or under `response`?** The by-hand measurement recorded the
  array but not its nesting; `internal/streamsup/parser.go`'s `control_response` arm records
  `subtype` and `request_id` as nested. `initControlSummarize` reads both placements, so the design
  does not depend on the answer, and `control_responses` records it verbatim for #1690.
- **Does the minimal request shape get accepted at 2.1.220?** Unknown, deliberately. A refusal is a
  passing outcome and #1689's input. Do not iterate against a live child to find out.
- **Do entries genuinely differ in their field sets?** The by-hand table says yes (`Haiku` lacked
  two fields). If the committed capture confirms it, #1690's decoder must treat every per-entry
  field as optional, and the union in `models_entry_fields` must not be read as a per-entry
  guarantee. Flag it in the PR body if it holds.
- **Is `--max-turns 4` enough?** A `result` line with `subtype:"error_max_turns"` lands in the
  fixture plainly. If it appears, raise the constant and rerun.

---

## Security review

**Verdict:** PASS (after two MUST FIXes, both applied above before this section was written)

The framing that matters for this ticket: it is the first slice in the `initialize_control_*`
family to run a real child, and the only one that writes **live child stderr into a file committed
to a public repository** while a live operator credential sits in that child's environment. Every
finding below is measured against that, not against the offline substrate #1696/#1701/#1702/#1700
already settled.

**Findings:**

- **[Trust boundaries] No findings.** The untrusted→trusted crossing is claude's stdout entering
  this process, and it is a single named place: `setModeRecorder.add`. It sanitises at the
  boundary rather than downstream — a line that is not valid JSON is retained as a JSON *string*,
  so a raw byte run in child stdout cannot make the committed fixture unparseable, and `nonJSON`
  counts them so the two encodings stay distinguishable. The reverse crossing (memory → disk) is
  likewise single and named: `writeInitControlFixture`. Nothing in this design parses child output
  in a second place.

- **[Tokens, secrets, credentials] MUST FIX ×1 — applied.** `WithWorktreeAuthenticated` re-pins
  `ANTHROPIC_API_KEY` and/or `CLAUDE_CODE_OAUTH_TOKEN` so the child inherits a live credential, and
  an auth failure is the exact condition that makes claude write a long stderr message. The record
  already carries no `env` field (#1701, deliberate) and the argv is all file-local constants, so
  those two vectors were closed before this ticket. `stderr_capture` was not: `stderrFixtureCap`
  bounds it and #1700 proves the bound, but **a cap is not a redaction** — 8 KiB of a
  credential-bearing message still commits the credential. Added `initControlScrubbed`, a
  deterministic guard that fails the run on a literal match of either variable's non-empty value in
  the raw stderr, placed after the `readerDone` join and before every consumer of stderr (the
  zero-lines fatal, the record, the write). Backed at commit time by the `stderr_capture` read now
  required in § Producing the committed artifact. Deterministic code plus a human read, not two
  instructions. Token creation, rotation, revocation and expiry are **out of scope**: this test
  consumes an operator credential and neither mints nor stores one.

- **[File operations] No findings, and one inherited obligation now discharged.** The only
  caller-influenced component of the written path is the version token, which comes from the
  child's own `--version` output and is therefore untrusted; `initControlFixtureName` runs it
  through `versionSlug`, and #1696 proves for every input that the minted name is a single clean
  path component — no separator, never `.` or `..`. **`initControlFixtureName`'s doc explicitly
  hands one obligation to its caller**: that guarantee is lexical and says nothing about the
  directory, so a `dir` that is or contains a symlink still resolves wherever the symlink points,
  and choosing `dir` stays the caller's job. This ticket is that caller and discharges it by
  passing `filepath.Join(packageDir(t), "testdata")` — `packageDir` returns `os.Getwd()`, which
  under `go test` is the package's own source directory, with no untrusted component anywhere in
  it. Writes are temp-file-plus-rename (#1702), so an interrupted run cannot strand a half-written
  fixture for a later commit; modes are `0o644` for the fixture, which is correct because it is
  destined for a public repo and — given the guard above — holds no secret by construction, and
  `0o700` for the probe workdir under the pinned `$HOME`. No check-then-use on any path. The fixed
  `path + ".tmp"` name would race between two concurrent invocations of this package; that is
  inherited from #1702 and #1662, the suite runs one child at a time by design, and it is **out of
  scope** here rather than silently accepted.

- **[Subprocess execution] No findings, plus one design choice worth naming so a later edit cannot
  undo it silently.** Every argv element is a file-local constant; no user-controlled or
  child-derived value reaches `exec.CommandContext`, and there is no `sh -c`. The environment is
  inherited deliberately — that is the credential's only route to the child — and `$HOME` is pinned
  by `WithWorktreeAuthenticated`. **The probe prompt is tool-free and this run does not pass
  `--dangerously-skip-permissions`**, unlike two of `runSetModeChild`'s arms; the child therefore
  never gets unsandboxed tool access. A later "make the probe use Bash like #1595" edit would
  reintroduce that flag, which is why the prompt's tool-free property is stated as a rationale in
  § Constants rather than left as an accident. Orphaned grandchildren after a `CommandContext`
  SIGKILL are inherited from `runSetModeChild` and belong to this package's reap family
  (`teardown_reap_capture_test.go`); **out of scope**.

- **[Cryptographic primitives] Not applicable, by design rather than by omission.** No RNG:
  `requestID` is a fixed literal correlation token, chosen so the committed fixture stays diffable,
  and it is explicitly not a security token — the same reasoning `(*Runner).Interrupt`'s monotonic
  counter rests on. No hashing, no key material, no TLS. The one comparison against an
  environment-sourced secret is `initControlScrubbed`, and it uses plain `strings.Contains`: the
  only party on the other side is the claude binary, which was handed the token, so constant-time
  comparison would defend nothing.

- **[Network & I/O] No findings.** No sockets, no server, no TLS in this file. The input-size
  question is real and is AC 3: the reader caps each line at `setModeScanMax` (1 MiB), and an
  over-long line makes `bufio.Scanner` stop and report through `scanner_error` rather than
  truncating silently. Every wait is explicitly bounded — `initControlChildBudget`,
  `initControlTurnBudget`, `initControlControlBudget`, and the context deadline as backstop — so no
  step can block unbounded. **SHOULD FIX, addressed:** there is no cap on the *number* of retained
  lines (`stdout_events` is uncapped in line count by #1701's deliberate choice, since capping
  structured evidence destroys the artifact), so `--max-turns` and the child budget are the only
  bounds on artifact size. Added the line-count read to § Producing the committed artifact, with
  #1595's measured 946–1027 lines as the reference magnitude.

- **[Error messages, logs, telemetry] MUST FIX ×1 — applied, and it is the same fix as the
  credential finding.** The zero-stdout-lines fatal prints up to `stderrFixtureCap` bytes of child
  stderr into the run log, which this pipeline salvages — a second leak path for the same bytes the
  fixture cap was protecting. Ordering `initControlScrubbed` before that fatal closes both paths
  with one guard. Two supporting rules are stated in § Error handling: never `%+v` the record into
  any log or fatal message (it would move bounded bytes into an unbounded log — the writer's own
  doc gives this reason for its failure path), and `initControlScrubbed`'s own message names the
  variable and prints no excerpt, since an error that quotes the leak is the own-goal. Logging
  `control_response` verbatim is retained: that is model output and it is the measurement.

- **[Concurrency] No findings.** Two goroutines, one channel. The test goroutine is the sole writer
  to the child's stdin; the reader goroutine is the sole caller of `setModeRecorder.add`, whose own
  mutex covers the shared counters the test goroutine polls through `setModeWaitFor`. One lock, so
  there is no ordering question. `scannerErr` is written only before `close(readerDone)` and read
  only after `<-readerDone`, which is the whole synchronisation for it — the spec says not to add a
  channel send or a second goroutine there. Every goroutine's exit is accounted for: EOF follows
  the stdin close or the context kill, and the join is unconditional. The one hazard a fatal could
  introduce — stranding the reader — is why `initControlScrubbed` is specified as running *after*
  the join rather than before. Interruption mid-write is covered by the temp-file-plus-rename.

- **[Threat model alignment] No relay or mobile surface, so `docs/protocol-mobile.md` § Security
  model does not apply.** The threat that does apply is this package's own, and it is named in
  `finOfflineExecBans`' header: the process environment here carries `CLAUDE_CODE_OAUTH_TOKEN` and
  `ANTHROPIC_API_KEY`, and the artifacts are committed. Categories 2 and 7 above are that threat.
  This file execs, so it correctly takes no `finOfflineExecBans` entry — the consuming loop is
  `for f := range finOfflineExecBans`, registry-keyed rather than directory-swept, so the absence
  costs nothing and `initControlScrubbed` is what carries the guard here instead.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-22
