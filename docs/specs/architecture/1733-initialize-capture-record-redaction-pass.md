# #1733 — apply the `initialize` capture's redaction to every record payload at the fill site

Test-only. No production file changes. Two test files touched, both under
`internal/e2e/realclaude/`, both behind the `e2e_realclaude` build tag.

## Files to read first

- `internal/e2e/realclaude/initialize_control_redaction_test.go` → `newInitControlRedactor` — the construction this slice applies: four path parameters, no ambient read, one trailing-slash trim inside it, longest-value-first sort. **Do not re-trim at the call site.**
- Same file → `initControlDivergentDir` — returns `(handed, resolved)` for a real directory whose `filepath.EvalSymlinks` form differs on every platform. AC2's workdir value comes from here and nowhere else.
- Same file → `initControlTempDirValue`, `initControlTempHomeValue`, `initControlWorkdirValue`, `initControlOperatorHomeValue` — the four nested synthetic constants. AC2 and AC4 reuse three of them; the workdir slot is the divergent pair instead.
- Same file → its header, § "The mechanism is reused whole" and § "Offline, and further: no I/O in either direction" — this is the file the pass joins, and its offline contract binds everything you add to it.
- `internal/e2e/realclaude/dropped_line_capture_test.go` → `dropcapRedactor`, `redact`, `str`, `strs`, `substitutions`, `dropcapSubstitution`, `dropcapSlug`, `dropcapPathSpellings`, `add` — the mechanism. Extract: `redact` counts **per rule, per call, accumulated on the redactor**, so the census sums across every field the pass visits; `redact` returns its input slice untouched when no rule matched, which is what makes AC3's byte-identity clause satisfiable at all.
- `internal/e2e/realclaude/initialize_control_record_test.go` → `initControlFixtureRecord` — the four string-bearing Go shapes and which field carries which. Extract the exact field/tag list; the pass visits by field, not by reflection.
- Same file → `initControlFullRecord`, and literal-choice **2** in its doc comment — AC4's subject, and the reason it must NOT acquire `<`, `>`, `&` or insignificant whitespace.
- Same file → `initControlFixtureFields` and the `reflect.TypeOf(initControlFixtureRecord{}).NumField()` assertion inside `TestInitControlFullRecord_PinsEveryFieldAndTheSluggableVersionToken` — this is what a record field added in this slice would collide with. AC5 forbids the field; this is what enforces it.
- `internal/e2e/realclaude/initialize_control_probe_test.go` → `runInitControlChild` — the fill site. Extract three positions: the `record := &initControlFixtureRecord{…}` literal, the `writeInitControlFixture` call, and the `t.Logf` loop over `responses` that follows the write.
- Same file → `TestRealClaude_InitializeControl_Capture` — the ONE call site, and the place that already holds `home` (the pinned `$HOME`) and `workdir`.
- `internal/e2e/realclaude/initialize_control_writer_test.go` → `writeInitControlFixture` — read the `out := *rec` / cap-only-`StderrCapture` contract to confirm this slice leaves the writer alone; and `compactInitControlRawRows`, whose doc records that this record carries three raw-JSON-bearing fields of **two different Go types** and that a normaliser covering one is "red on arrival".
- `internal/e2e/realclaude/offline_exec_ban_test.go` → `finOfflineExecBans` (the `initialize_control_redaction_test.go` entry) and `TestFinOfflineFilesReachNoExecHelper` — that entry bans `realHome` and `os.TempDir` in the file the pass joins, and permits `t.TempDir`. A new file would need its own entry to be checked at all.
- `internal/e2e/realclaude/fixtures.go` → `realHome` — `os.Getenv("HOME")` captured at package load, empty when HOME was unset. `add`'s empty-value guard already drops such a rule, so the live call site needs no guard of its own.
- `docs/knowledge/features/e2e-realclaude.md`, the `initialize_control_redaction_test.go` (#1732) entry — the two review lessons. The load-bearing one here: **assemble an expectation from the row's own inputs; never read it back off the subject.**

## Context

The committed artifact `internal/e2e/realclaude/testdata/initialize_control_v2.1.239.json`
carries operator paths today — `argv[0]` under the operator's home, `cwd` under
`/private/var/folders/…`, `memory_paths.auto` under `/var/folders/…`. Three of the five
fixed classes `dropcapFixedNeedles` arms, in a file two independent human reads passed.

#1732 shipped the table. This slice is what makes a clean record producible: it applies
that table to `initControlFixtureRecord` uniformly, over each payload's **own bytes**,
between the record literal and `writeInitControlFixture` in `runInitControlChild`. #1729
is the fail-closed deny-scan behind it and consumes this; it cannot land first, because a
scan over today's record aborts every live run of this family.

No ADR. This is one test-helper function plus a wiring change inside a family whose
design record already lives in the e2e-realclaude overview.

## Design

### The pass

Add to `initialize_control_redaction_test.go` (not a new file — see § Where it lives):

```go
// redactInitControlRecord rewrites every string-bearing field of rec through red,
// in place, and returns the classes that fired. It assigns nothing else to rec.
func redactInitControlRecord(red *dropcapRedactor, rec *initControlFixtureRecord) []dropcapSubstitution
```

Body shape — one assignment per field, no reflection, no marshal:

| Go shape | Fields | Applied through |
|---|---|---|
| `string` | `claude_version_raw`, `claude_version`, `arm`, `control_request_id`, `control_response_subtype`, `stderr_capture`, `wait_error`, `scanner_error` | `red.str` |
| `[]string` | `argv`, `prompts`, `models_entry_fields`, `stdin_write_errors` | `red.strs` |
| `json.RawMessage` | `control_request_sent` | `json.RawMessage(red.redact(…))` |
| `[]json.RawMessage` | `control_responses`, `stdout_events` | a four-line local helper mirroring `strs` |

Then `return red.substitutions()`.

Four constraints on that body, each of which is a real mutant this design closes:

- **Every field of every string-bearing shape, not a curated list.** `wait_error`,
  `scanner_error` and `stdin_write_errors` carry a child's own error text, which is
  exactly where a path arrives that a targeted pass forgets. `control_request_sent` is a
  **bare `json.RawMessage`** and a different Go type from the two raw-JSON slices beside
  it: a pass that names `[]json.RawMessage` and forgets `json.RawMessage` is green on
  every other value in AC2's fixture. `compactInitControlRawRows`'s doc records this same
  split as "red on arrival", not a latent risk.
- **Never a whole-record round trip.** Marshalling the record, substituting into the
  bytes and unmarshalling back strips insignificant whitespace from every
  `json.RawMessage` and HTML-escapes `<`, `>` and `&` into numeric `\u` escapes — so a
  payload naming no path comes back changed. AC3's byte-identity clause is what reports
  it.
- **Nothing is assigned to the record beyond the redacted values.** No new field, no
  census stored. `initControlFixtureRecord` is untouched in this slice; #1731 is the one
  that puts the census on it. The record's field count is asserted through
  `reflect.TypeOf(initControlFixtureRecord{}).NumField()`, so a field added here would
  not collide with #1731 — it would simply be #1731's work in the wrong slice, and it
  reddens AC4 on arrival (the marshal before the pass carries the new field at its zero
  value, the marshal after carries it populated).
- **`after_send_point_result_trailers` is not visited.** Every field of
  `initControlResultTrailer` is an `int`, a `float64` or a `bool`. No string, so no path.
  Leaving it alone is also what keeps AC4 honest: `initControlFullRecord`'s two trailer
  entries survive byte for byte.

`redact` on a nil `[]byte` returns nil, so `control_request_sent` keeps its nil-ness. The
two slice helpers (`strs` and the raw-slice one) allocate `make(…, len(in))`, so a nil
field becomes an empty slice and its committed value moves from `null` to `[]`. That
affects only `models_entry_fields` and `stdin_write_errors`, and only on a run where they
carried nothing. **Accepted rather than guarded:** the record deliberately carries no
`omitempty` on any tag so that absent values stay visible as present keys in an artifact a
human reads, and `[]` is the more legible of the two. Nothing decodes the committed bytes;
`initControlFullRecord` has no nil slice, so AC4 stays green. Reuse `red.strs` whole and
shape the raw-slice helper the same way — the two slice shapes behaving identically is
worth more than preserving a distinction the record does not use.

### The fill site

`runInitControlChild` takes the redactor, mirroring `runSetModeChild`'s
`(t, claudeBin, workdir string, arm setModeArm, versionRaw, versionToken string)`:

```go
func runInitControlChild(t *testing.T, claudeBin, workdir string, red *dropcapRedactor,
    versionRaw, versionToken string) *initControlFixtureRecord
```

`TestRealClaude_InitializeControl_Capture` builds it, after `workdir` exists:

```go
red := newInitControlRedactor(realHome, home, workdir, os.TempDir())
```

`realHome` and `os.TempDir()` are legitimate there and only there: that file execs and
correctly carries no `finOfflineExecBans` entry, while `initialize_control_redaction_test.go`
bans both by name. **Do not trim `os.TempDir()`'s trailing slash** — #1732 moved that trim
inside the construction as its one permitted normalisation. `realHome` may be empty when
HOME was unset at launch; `add` drops an empty-valued rule, so no guard is needed here.

**Why one redactor rather than three more string parameters.** The alternative —
`operatorHome, tempHome, tempDir` threaded in beside the `workdir` that is already there —
makes the signature eight positional parameters, six of them strings, and puts the
four-argument construction at two sites instead of one. A transposition of `operatorHome`
and `tempHome` is silent (both install rules, both produce a placeholder, just the wrong
one) and no offline test can see it, because every offline row builds its own redactor. One
construction site is one place to get that order right. It also keeps both environment reads
at the live call site, which is the requirement either way, and leaves #1715 free to mint a
fresh redactor per arm inside its loop — which it must, so each arm's census counts only its
own run.

`red` is never nil: one call site, always constructed. No nil guard — this failure has not
been observed and the pass would panic loudly rather than silently mis-redact.

### Placement, and what it buys

The pass call goes **between the record literal and the `writeInitControlFixture` call**,
and nowhere else:

```go
record := &initControlFixtureRecord{ … }          // unchanged
subs := redactInitControlRecord(red, record)      // new
t.Logf("#1733: redaction applied: %+v", subs)     // new — classes, replacements, counts
path := writeInitControlFixture(t, …, record)     // unchanged
```

That position is ahead of the two log sites that would otherwise leak what the fixture no
longer carries: the loop over the captured `control_responses`, and the summary line that
logs `scanner_error`. Both sit after the write, both land in a run log this pipeline
salvages. A pass placed after them would clean the file and leak into the log.

**Position is necessary and not sufficient — each of those log sites must read the RECORD'S
field.** The pass assigns fresh values rather than writing through what it was handed
(`initControlRedactRaws` allocates, and `redact` returns `bytes.ReplaceAll`'s result), so a
pre-pass local aliasing the same payloads keeps the unredacted bytes. Measured on the first
cut of this slice: the response loop ranged the local `responses` and printed the operator
path past a correctly placed pass, while the summary line was safe because it reads
`record.ScannerError`. The loop ranges `record.ControlResponses`, and it prints the redacted
form rather than the verbatim one.

The census log line is the fill site's consumer for the returned value — class names,
replacements and counts only, never a value, so it is safe for a run log. #1731 is what puts
the census on the record.

`writeInitControlFixture` is **unchanged**. Its `out := *rec` copy and its `StderrCapture`
cap are #1729's byte-identity subject; this slice must not touch either.

Out of scope, and deliberately: `messaging_socket_path` (`/tmp/cc-socks/<pid>.sock`, not one
of the five denied prefixes) and `session_id` (a UUID claude mints, unknown to the harness).
Neither is a redaction rule — this slice rewrites what the harness produced. The zero-stdout
`t.Fatalf` that prints raw child stderr is out of scope for the same reason, and it fires
before the record literal exists. All three are #1729's questions.

### Where it lives

The pass and its tests join `initialize_control_redaction_test.go`. That file is already
registered in `finOfflineExecBans` and reads nothing from the environment, so the pass joins
at no cost. A new file needs its own entry: the registry is keyed by filename and
`TestFinOfflineFilesReachNoExecHelper` drives its subtests from `for f := range
finOfflineExecBans`, so a file with no entry produces no subtest and no failure. The existing
entry permits `t.TempDir` (`initControlDivergentDir` needs it) and bans `os.TempDir`, which
is a different symbol.

The file's entry needs **no change**: everything below builds its values from that file's own
constants and from `initControlDivergentDir`.

## Concurrency model

None new. `dropcapRedactor`'s counters are unlocked by design, and every `redact` call this
slice adds runs on the test goroutine: at the fill site the pass sits after `<-readerDone`,
so the reader goroutine is joined; in the tests the parent runs the pass before any subtest
starts. Subtests may take `t.Parallel()` and read the mutated record — the precedent is
`TestInitControlFullRecord_PinsEveryFieldAndTheSluggableVersionToken`, whose parent computes
once and whose subtests only read.

## Error handling

The pass returns no error and cannot fail: it is `bytes.ReplaceAll` over fields it already
holds. `initControlDivergentDir` already carries the loud `t.Fatalf` for a non-divergent
result, inherited rather than restated. Test failures are `t.Errorf` naming the field and
printing got/want — safe here and only here, because every value in these rows is a
synthetic literal or a path minted under `t.TempDir()`. Nothing anywhere may `%+v` the live
record.

## Testing strategy

Two test functions in `initialize_control_redaction_test.go`. All rows must **PASS, not
SKIP**, on a machine with no claude and no credentials.

### Test 1 — the record fixture (AC2, AC3, AC5)

The parent builds one record, runs the pass once, and hands four subtests the result.

**Inputs.** `handed, resolved := initControlDivergentDir(t)`; the other three classes are
`initControlOperatorHomeValue`, `initControlTempHomeValue`, `initControlTempDirValue`. The
redactor is `newInitControlRedactor(initControlOperatorHomeValue, initControlTempHomeValue,
handed, initControlTempDirValue)` — the shipped construction, over real values, never a
hand-built rule table and never four empty strings.

**The record's load-bearing fields**, one per required shape:

- `argv` (`[]string`) — `argv[0]` is `initControlOperatorHomeValue + "/.local/bin/claude"`.
- `stderr_capture` (`string`) — free text containing `initControlTempDirValue + "/claude-shim.log"`.
- `stdout_events` (`[]json.RawMessage`), entry 0 — the measured `system`/`init` shape,
  carrying **both** measured leaks: `cwd` as `resolved` (the spelling the harness never
  held), and `memory_paths.auto` as the composite, assembled from this row's own values as
  `initControlTempHomeValue + "/.claude/projects/" + dropcapSlug(resolved) + "/memory/"`.
  The mixed spellings are the measurement: the temp home in the spelling the harness handed
  over, the workdir in its resolved form's slug. A row built with both values in their
  handed spellings is green against a table that never enumerated a resolved form.
- `stdout_events` entry 1 — path-free, and it **must** carry `<`, `>` or `&` (a realistic
  assistant-text event is where one turns up).
- `control_responses` (`[]json.RawMessage`), one entry — path-free, and it **must** carry
  insignificant whitespace (spaces after the colons).
- `control_request_sent` (bare `json.RawMessage`) — carries `handed`, the one spelling no
  other field here exercises.

Every other field carries a literal free of all four classes.

**Subtests:**

- *each value is replaced by the placeholder of its own class* — a table of
  `{field, got, want}` rows compared as **expected bytes**, one row per bullet above.
  Assemble each `want` from the row's own inputs (`"$HOME/.local/bin/claude"`,
  `"$TMPDIR/claude-shim.log"`, `"…\"cwd\":\"$WORKDIR\"…"`,
  `"$TEMP_HOME/.claude/projects/$WORKDIR/memory/"`, `"…$WORKDIR…"`), never by reading the
  subject back. Absence decides nothing on the composite: both substitution orderings leave
  zero denied values behind there, so only expected bytes discriminate.
- *the captured payloads still count and still decode* — `len(rec.StdoutEvents)` and
  `len(rec.ControlResponses)` unchanged, and `json.Valid` over `control_request_sent` and
  over every entry of both slices.
- *payloads that named no path come back byte-identical* — `stdout_events[1]` and the one
  `control_responses` entry compared with `bytes.Equal` against the **literal constants the
  record was built from**, not against a variable the record also holds and not against the
  written file. Naming them as `const` and using the same constant on both sides is what
  keeps this independent. `writeInitControlFixture` marshals with `json.MarshalIndent`, which
  re-indents inside an embedded raw message, so a file-level assertion here reddens for a
  perfectly correct pass.
- *the census names exactly the classes that fired, with their counts* — compare the
  returned `[]dropcapSubstitution` against the expected list. For the fixture above the
  arithmetic is: `operator_home` 1 (`argv[0]`), `temp_dir` 1 (`stderr_capture`), `temp_home`
  1 (the composite's prefix), `workdir` 3 (`handed` in `control_request_sent`, `resolved` in
  `cwd`, `dropcapSlug(resolved)` in the composite) — sorted by class, zero-count classes
  dropped, which is `substitutions()`'s own shape. Re-derive it against the fixture you
  actually build rather than copying these numbers: counting is per rule within a class, and
  longest-first means a longer rule's replacement hides the shorter rules' values from the
  bytes that follow, which is why the composite contributes one temp-home hit and one
  workdir hit rather than three of anything.

### Test 2 — a path-free record is left alone (AC4)

`initControlFullRecord`, marshalled before and after the pass, through a redactor built by
`newInitControlRedactor` over the four synthetic constants. Compare the two byte slices.
Non-trivial rather than tautological: that record carries `0` in three places
(`turn_boundaries: [0, 7]`, `total_cost_usd: 0.0731`, and a second trailer entry whose cost
is zero), so it goes red the moment the construction acquires a nonce rule or any other rule
over a value it legitimately carries. It must NOT acquire `<`, `>`, `&` or insignificant
whitespace — literal-choice 2 in its own doc comment forbids exactly that, and AC3's row is
where those characters belong.

### Running it

```
go test -tags e2e_realclaude -race -count=1 -v \
  -run 'TestInitControlRedact|TestFinOfflineFilesReachNoExecHelper' \
  ./internal/e2e/realclaude/
```

Read the count of tests that executed, never the exit code: this package is behind the
`e2e_realclaude` tag, `make check` never compiles it, and the suite exits 0 both on a build
failure and on a full credentials skip.

### Mutants — each row is the sole red for at least one

| # | Mutant | Sole red |
|---|---|---|
| 1 | the pass skips `json.RawMessage`, keeps `[]json.RawMessage` | AC2's `control_request_sent` row |
| 2 | the pass skips `[]string` | AC2's `argv` row (census loses `operator_home`) |
| 3 | the pass skips plain `string` | AC2's `stderr_capture` row (census loses `temp_dir`) |
| 4 | the pass skips `[]json.RawMessage` | AC2's `cwd` + composite rows |
| 5 | the pass is a whole-record marshal → substitute → unmarshal | AC3's byte-identity row |
| 6 | the pass returns `nil` (or a hand-built list) instead of `red.substitutions()` | AC5's census row |
| 7 | the pass stores the census on a new record field | AC4's before/after equality |
| 8 | `newInitControlRedactor`'s sort reversed to shortest-first | AC2's composite row + AC5's census (also #1732's row 3) |
| 9 | a field is visited but its result not assigned back | that field's AC2 row |

## Open questions

- **Not provable offline: that the pass runs before the write.** `runInitControlChild`
  spawns a live child, so no row here can observe the ordering. Accepted rather than
  papered over with an AST check the failure has never justified: #1729's fail-closed
  deny-scan is what makes a live run diagnostic, and #1715 is the labelled ticket that
  drives the live arms. Reviewers should read the fill site's three positions directly.
- **The committed fixture is not regenerated by this slice.** It still carries the three
  measured leaks until a live run of #1715 rewrites it. Whether the stale artifact should be
  deleted or left in place until then is #1729's call.

## Size check

| Limit | Boundary | This spec |
|---|---|---|
| Production source files created or modified | ≤ 3 | **0** — two `*_test.go` files, no production file |
| Total written work | ≤ 400 lines | **~340** — the pass and two tests in the redaction file (~280, no new file header, no `finOfflineExecBans` entry), the fill-site wiring (~55) |
| New exported types or interfaces | ≤ 5 | **0** |
| Consumer call sites needing simultaneous update | ≤ 10 | **1** — `TestRealClaude_InitializeControl_Capture`, the only caller of `runInitControlChild` |
| Acceptance criteria | ≤ 5 | **5** |
| Distinct error/reject branches in a state machine | ≤ 10 | **0** — the pass has no branch and returns no error |

Sized against the nearest analogue: #1732 (`92cc21f`) shipped **419** lines for a
constructor, three helpers and four tests — including a ~72-line new-file header and a
41-line `finOfflineExecBans` entry, neither of which this slice needs. #1723 (`4b6bb77`)
shipped 292. This slice's test surface is smaller (one test with four subtests plus one
short test) and its wiring is smaller than #1722's 94-line probe-file delta (which added two
record fields and a selector).

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** The boundary this slice moves is subprocess stdout/stderr →
  committed public artifact, and the design makes it a **single explicit point**:
  `redactInitControlRecord`, called once between the record literal and
  `writeInitControlFixture` in `runInitControlChild`. Before this slice the boundary was
  scattered — untrusted child bytes reached `writeInitControlFixture` unfiltered from four
  Go shapes. One finding worth stating rather than assuming: the pass rewrites only values
  the harness **knows it produced**, so a path the harness never predicted crosses
  untouched. That is by design and is why redaction and scanning are different fabric —
  the fail-closed net is #1729, and this slice exists so #1729 can be armed without
  aborting every live run.
- **[Tokens, secrets, credentials]** No token handling is added or changed.
  `initControlScrubbed` remains the credential guard and still runs on **raw** stderr,
  before the record literal and therefore before the pass — the ordering is correct: a
  credential must fail the run, never be quietly rewritten. SHOULD-NOT-FIX, stated so
  nobody "improves" it: do not add `CLAUDE_CODE_OAUTH_TOKEN` or `ANTHROPIC_API_KEY` as
  redaction rules. A credential-bearing run must abort, and a rule would turn a hard stop
  into a silent placeholder. The census the pass returns carries class names, replacements
  and counts — never a value — so the new `t.Logf` cannot leak one.
- **[File operations]** No path is constructed from an external value. The one filesystem
  path the tests create comes from `initControlDivergentDir` under `t.TempDir()`, whose
  symlink the helper itself creates and which is never derived from an argument or the
  environment. `writeInitControlFixture` is unchanged, so its temp-file-plus-rename, its
  `0644` mode and `initControlArmFixtureName`'s lexical containment guarantee are all
  untouched. One consequence to state: the filename is minted from the record's
  `claude_version` and `arm` **after** the pass, so a redacted version token would produce
  a redacted filename. That is correct behaviour, not a traversal risk —
  `newInitControlRedactor`'s replacements are `$HOME`, `$TEMP_HOME`, `$WORKDIR`, `$TMPDIR`,
  and `versionSlug` rewrites `$` (outside `[a-z0-9._-]`) to `_` before the name is built.
  No `..` and no separator can be introduced.
- **[Subprocess / external command execution]** Unchanged. The child's argv, environment,
  context deadline and stdin close all sit before the record literal; the pass touches the
  **recorded copy** of `argv`, never the slice handed to `exec.CommandContext`.
- **[Cryptographic primitives]** N/A — no randomness, no comparison against a secret. The
  design deliberately takes **no nonce parameter**: `strconv.FormatInt` never returns `""`,
  so `add`'s empty-value guard cannot stop a nonce rule, and `0` would install one that
  rewrites every `0` byte in the record. AC4's row over `initControlFullRecord` is the
  standing guard.
- **[Network & I/O]** N/A — no socket, no reader. The one cap in this path
  (`capFixtureCapture` inside `writeInitControlFixture`) is untouched and still bounds
  `stderr_capture` on disk. The pass runs on the raw in-memory string, which is a superset
  of what is written — stricter, and the same discipline `initControlScrubbed` already uses.
- **[Error messages, logs, telemetry]** The one real hazard in this slice, and the design
  addresses it by **placement plus what each log site reads**: the pass sits ahead of both
  post-write log sites — the loop over the captured `control_responses`, and the summary
  line that logs `scanner_error` — and both of them read the record's own fields. Those
  bytes land in a run log this pipeline salvages, so a pass placed after them would clean
  the fixture and leak into the log; and because the pass assigns fresh values rather than
  writing through what it was handed, a log site holding a pre-pass local leaks past a
  correctly placed pass. Measured on the first cut of this slice, where the response loop
  ranged the local `responses`; it ranges `record.ControlResponses` and prints the redacted
  form. Two log sites are knowingly left
  unredacted and are named out of scope for #1729: the zero-stdout-lines `t.Fatalf`, which
  prints raw child stderr and fires before the record exists, and the earlier line printing
  the control request sent, which the harness builds from constants and which carries no
  path. The standing "never `%+v` the record" rule is unchanged and the new census line
  respects it.
- **[Concurrency]** `dropcapRedactor`'s counters are deliberately unlocked. Every `redact`
  this slice adds runs on the test goroutine — at the fill site after `<-readerDone`, in the
  tests before any subtest starts — so no lock is needed and none is added. One forward
  hazard, recorded rather than guarded: a caller that shared one redactor across #1715's
  three arms would both race the counters and produce a cumulative census. Passing the
  redactor as a parameter is what lets #1715 mint a fresh one per arm; this slice's single
  call site constructs exactly one.
- **[Threat model alignment]** The threat is a public repository documenting where the
  operator's machine ran a capture — the leak class #1260's entry in the e2e-realclaude
  overview records, re-measured 2026-08-24 on the committed artifact. This slice addresses
  the predictable half. The unpredictable half (#1729's fail-closed deny-scan) and the live
  proof (#1715) are named out of scope here and are sequenced behind it.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-24
