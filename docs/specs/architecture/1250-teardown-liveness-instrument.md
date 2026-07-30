# #1250 — The offline teardown-liveness instrument

One new file, `internal/e2e/realclaude/teardown_liveness_test.go`, under the existing
`e2e_realclaude` build tag. **No production code.** Three deliverables: a reaper-log
classifier, a real-bytes proof of the per-pid liveness read's fail-safe premise, and the
record its live consumer (#1251) will write.

## Files to read first

- `internal/agentrun/reap.go:35-67` — `ReapDescendantGroups`. The `Info` at `:65` is the
  only line this ticket classifies; the `len(reaped) > 0` guard at `:64` is what makes
  silence ambiguous; `:56-62` skips `ESRCH` **before** the append, so a group that exited
  in the teardown window is not in `pgids`.
- `internal/e2e/realclaude/process_pin_liveness_test.go:199-448` — `pinStateColumns`,
  `pinStateArgs`, `pinStateOutcome`, `pinReadState`'s `pid <= 0` guard at `:276`,
  `pinClassifyState`'s ten-branch contract at `:297-331`, `pinStateRow`'s
  exactly-three-fields rule at `:404-426`, `pinIsZombie` at `:436`. AC2 **calls** these; it
  edits none of them.
- `internal/e2e/realclaude/process_pin_liveness_test.go:712-983` — `TestPinClassifyState`'s
  13 hand-built rows (what is already proven, so AC2 does not re-prove it) and
  `TestPinStateColumns_ReadsNoEnvironment` (`:950`), the redaction tripwire AC3 must not
  duplicate.
- `internal/e2e/realclaude/process_pin_liveness_test.go:1082-1126` — `pinExit1` /
  `pinSignaled`. These are the hand-built errors AC2 exists to complement; read them to see
  exactly what is *not* proven by them.
- `internal/e2e/realclaude/background_reach_probe_test.go:162-168, 199-260` — `reachProc`
  and `reachRecord` (field-comment density, `note`, the `Ticket` provenance field). The
  record in AC3 mirrors this shape.
- `internal/e2e/realclaude/background_reach_probe_test.go:820-855` — `writeReachArtifacts`:
  `MarshalIndent`, `0o600`, `t.Errorf` (never `Fatal`) on a failed write. AC3's writer
  follows this and diverges in exactly one way (§ 3.2).
- `internal/e2e/realclaude/background_reach_probe_test.go:857-882` — `reachScanArgv` and
  redaction rule 1 at `:866-872`; `reachCapCommand` at `:945`.
- `internal/e2e/realclaude/fifo_reader_liveness_test.go:78-93, 111-212` — the
  three-valued verdict block and `fifoLiveOutcome`. This is the closest existing model for
  a pure classifier with an `instrument-failed` arm, and AC3 composes the type verbatim.
- `cmd/pyry/agent_run.go:299-330` and `internal/agentrun/ptyrunner/runner.go:289-292` —
  **read these two together.** `runAgentRunPty` passes no `Logger`, so `ptyrunner` falls
  back to `slog.Default()`. This decides AC1's fixture set (§ 1.1) and is not what the
  ticket body's cited `cmd/pyry/main.go:743-744` renders.

## Context

`agentrun.ReapDescendantGroups` SIGKILLs the detached Bash process groups claude leaves
behind at teardown. #1251 will measure, on a live turn, whether a *backgrounded* Bash
command survives that teardown. It needs three instruments, and the ticket's job is to ship
them proven before the run they are meant to judge — the shape that worked for #1239 →
#1240, where the consumer exposed a mis-wiring the upstream self-check structurally could
not have caught.

The per-pid liveness half already exists (#1235, `5fd6098`). What remains is one real gap
in it plus two things that do not exist at all. Everything here is provable offline: no
claude, no credentials, no `t.Skip`.

## Design

### 0. Shape and budget

One file, package `realclaude`, `//go:build e2e_realclaude`. Every new symbol carries the
`tdn` prefix (verified unused across `internal/`): the package already holds `probe*`
(#1223), `reach*` (#1230), `pin*` (#1235) and `fifoLive*` (#1239).

**Budget discipline.** This file should land at ~550–600 lines. The following were
considered and deliberately cut; do not add them back:

- Disambiguating the no-line arm by also scanning for `reap.go`'s two `Warn` messages. No
  observation has ever needed it (Evidence-Based Fix Selection); the ambiguity is carried
  in the verdict's `Detail` instead (§ 1.3).
- A second artifact file. The writer emits exactly one, and that is itself the redaction
  assertion (§ 3.2).
- Any new liveness type. AC3 composes `pinStateOutcome` and `fifoLiveOutcome` as-is.
- Re-proving `pinClassifyState`'s branch logic. `TestPinClassifyState`'s 13 rows own that;
  AC2 proves the *platform premise* underneath them and nothing else.

The file header should run ~45 lines, not the ~70 of its siblings: three of its four design
rationales (the four-valued read, redaction rule 1, "an instrument failure is a datum, not
an abort") already have full treatments in `process_pin_liveness_test.go` and
`fifo_reader_liveness_test.go`. Cross-reference them; do not restate them.

### 1. The reaper-log classifier (AC1)

```go
const (
    tdnReapHeldPGIDKilled  = "held-pgid-in-reap-line"
    tdnReapHeldPGIDAbsent  = "reap-line-without-held-pgid"
    tdnReapNoLine          = "no-reap-line"
    tdnReapInstrumentFailed = "instrument-failed"
)

// tdnClassifyReapLog answers "did the reaper report killing heldPGID?" over
// pyry's captured stderr. Pure: no exec, no file read, no clock.
func tdnClassifyReapLog(stderr []byte, heldPGID int) tdnReapOutcome
```

```go
type tdnReapOutcome struct {
    Verdict   string `json:"verdict"`
    Detail    string `json:"detail"`
    HeldPGID  int    `json:"held_pgid"`
    PGIDs     []int  `json:"pgids_in_line,omitempty"`   // the parsed set, so the record shows what was matched against
    Count     int    `json:"count_attr,omitempty"`      // reap.go's own count= attr, for cross-check against len(PGIDs)
    LineCount int    `json:"reap_lines_seen"`
    Line      string `json:"reap_line,omitempty"`       // capped via reachCapCommand
}
```

Three answer values exactly as AC1 specifies, **plus** the package-standard
`instrument-failed` arm, which is never an answer. That arm is not scope growth: without
it, a line whose `pgids=` attribute cannot be parsed collapses into
`reap-line-without-held-pgid` — which reads as *"the reaper ran and did not kill our
group,"* a leak finding manufactured out of the instrument's own breakage. That is the
exact failure this whole ticket exists to close, and both siblings already carry the arm
(`pinStateInstrumentFailed`, `fifoLiveInstrumentFailed`).

#### 1.1 Anchor on the bare message text — the two handlers render `msg` differently

Measured 2026-07-30, Darwin 25.5, Go's `log/slog`:

```
# slog.NewTextHandler — what cmd/pyry/main.go:743-744 installs (runSupervisor)
level=INFO msg="agentrun: reaped claude descendant process groups" count=1 pgids=[4242]
level=INFO msg="agentrun: reaped claude descendant process groups" count=2 pgids="[4242 77]"

# slog.Default() — what `pyry agent-run` actually uses
2026/07/30 22:59:06 INFO agentrun: reaped claude descendant process groups count=1 pgids=[4242]
2026/07/30 22:59:06 INFO agentrun: reaped claude descendant process groups count=2 pgids="[4242 77]"
```

The ticket body's fact 1 holds on the `pgids` attribute — both handlers quote it identically
the moment it contains a space. But the body's cited handler is the wrong one for this
family. `runAgentRunPty` (`cmd/pyry/agent_run.go:299`) sets no `Logger` on
`ptyrunner.Config`, and `ptyrunner.Run` falls back to `slog.Default()`
(`internal/agentrun/ptyrunner/runner.go:289-292`). Every probe in this package spawns
`pyry agent-run` and captures its stderr (`background_reach_probe_test.go:388-389`), so
**the second rendering is the one #1251 will read.**

A matcher anchored on `msg="agentrun: reaped…"` therefore finds nothing on the live path and
returns `no-reap-line` — *"the reaper never fired"* — with nothing going red. So:

- The message anchor is the bare text `agentrun: reaped claude descendant process groups`,
  a **string literal in this file**, never a reference to anything `reap.go` defines. A
  renamed message must break this test rather than silently follow it.
- The anchor must never include `msg="`, a level token, or a timestamp.
- The fixture set carries **both** renderings.

#### 1.2 Membership over parsed integers, never substring

The ticket's fact 1 names a false *negative* (single-pgid matcher inverts on the multi-pgid
line). The dual is worse and is not in the body: substring-matching a pgid inside the
bracket text is a false *positive*. Held pgid `77` matches the text of `pgids=[7788]`, and
`held-pgid-in-reap-line` is the arm a consumer reads as "no leak."

So the extraction is: locate the `pgids=` token on an anchored line → take the value,
handling both the bare `[A B C]` and the quoted `"[A B C]"` forms → split on spaces → `Atoi`
each → membership over the resulting `[]int`. Notes:

- Bound the value by its terminator (closing `]`, then the closing quote when quoted), not
  by end-of-line. `pgids` is last in `reap.go`'s call today; a handler `WithAttrs` could
  append more.
- `pgid=` (singular, `reap.go:59`'s Warn) is a prefix-substring of `pgids=`, so a
  `Contains(line, "pgid=")` probe hits the Info line too. Anchor on the message first, then
  search for `pgids=` exactly.
- Any parse failure — no `pgids=` token, an unterminated value, a non-integer element —
  is `instrument-failed`, never `reap-line-without-held-pgid`.
- `pgids=[]` parses to the empty set and reads as `reap-line-without-held-pgid`. It cannot
  be emitted (`reap.go:64` guards on `len(reaped) > 0`); no special branch for it.

#### 1.3 Multiple lines, and the no-line arm's ambiguity

Scan **every** anchored line, union their pgids, and record `LineCount`. This is #1235's
own anti-first-match discipline (`pinScan.Matches` is a slice precisely because nothing here
resolves to "the" one) applied to lines instead of rows. A second reap line is a datum in the
record, not a failure.

`Count` carries `reap.go`'s own `count=` attribute so the record shows the two frames
agreeing. A disagreement between `Count` and `len(PGIDs)` is recorded in `Detail`, not
escalated — the union across lines can legitimately differ from any single `count=`.

The `no-reap-line` verdict's `Detail` must spell out both readings in the text itself:
`reap.go:64` guards the emit, so silence means *the reaper ran and reaped nothing* **or**
*the reaper never fired*. Collapsing those into either one is the defect. The verdict name
says "no line," never "no reap."

### 2. The liveness read's fail-safe premise, against real `ps` bytes (AC2)

`pinClassifyState`'s branch 5 — non-zero exit, empty stdout **and** empty stderr — is the
only input that yields `pinStateNoSuchProcess`, the one verdict #1251 reports as a result.
Branch 1 (`ToolStderr != ""`) fires first and keeps every broken invocation away from it.
That is entirely load-bearing on real `ps` writing to stderr, and nothing asserts it:
`TestPinClassifyState` builds its errors with `pinExit1` / `pinSignaled`.

A table-driven test executes real mis-invocations and, per arm, asserts the observed triple
then requires `pinClassifyState` to return `pinStateInstrumentFailed`.

#### 2.1 Read stderr from the channel the classifier reads

The arms must run `exec.Command("ps", args...).Output()` — the same call `pinReadState`
uses at `:293` — and assert on `exitErr.Stderr` from the resulting `*exec.ExitError`. **Not**
on a separately captured buffer.

This is the sharpest trap in AC2. `Output()` populates `ExitError.Stderr` only because it
owns `cmd.Stderr`; a helper that sets `cmd.Stderr = &buf` to "capture stderr for the
assertion" leaves `ExitError.Stderr` empty, and the classifier then sees exit 1 + empty
stdout + empty stderr = **branch 5**. The test written to prove branch 5 unreachable would
reach it. Assert the premise on the bytes the consumer consumes, or the assertion is about
a different channel.

The load-bearing assertion is `len(exitErr.Stderr) > 0`, stated per arm with its own failure
message. A verdict-only assertion still passes on a platform gone silent, which is the
scenario the AC names.

#### 2.2 The four arms

Measured 2026-07-30 on Darwin 25.5, credential-free:

| arm | invocation | exit | stdout | stderr |
|---|---|---|---|---|
| A. bad column, `=` suffix | `ps -p 1 -o pid=,nosuchcolumn=,ppid=,stat=` | 1 | `    1     0 Us  ` — **3 fields** | `ps: nosuchcolumn: keyword not found` |
| B. bare bad column | `ps -p 1 -o nosuchcolumn` | 1 | full keyword list (~497 B) | `keyword not found` + `no valid keywords` |
| C. illegal option | `ps -Q -p 1` | 1 | empty | `illegal option` + usage block |
| D. out-of-range pid | `ps -p <candidate> -o pid=,ppid=,stat=` | 1 | empty | `ps: process id too large: <candidate>` |

**Arm A is the strongest fixture in the ticket and is deliberately four columns, not
three.** The ticket body's `pid=,nosuchcolumn=,stat=` yields a *two*-field row, which
`pinStateRow`'s exactly-three rule rejects anyway. Requesting four columns with one bad one
yields `1 0 Us` — a row `pinStateRow` parses **successfully** as pid 1, ppid 0, state `Us`,
which `pinIsZombie` reads as *running*. So arm A's real bytes demonstrate that branch order
alone stands between a broken `ps` and a fabricated `running` verdict about pid 1. Assert
that: call `pinStateRow(stdout)` directly, show `ok == true`, then show
`pinClassifyState(1, stdout, err)` still returns `pinStateInstrumentFailed`.

**Arm A's stdout shape is Darwin-specific.** Linux procps rejects an unknown `-o` specifier
with empty stdout. So the portable assertions are the invariants — stderr non-empty, verdict
`instrument-failed` — and the parsed-row observation is **conditional**: *if* `pinStateRow`
parsed a row out of this stdout, the verdict must still be `instrument-failed`. Record the
observed field count in the log line either way. Do not make the partial row a hard
requirement; do not silently drop the assertion where the platform produces it.

**Arm D must confirm the rejection, never assume the constant.** Measured here: `ps -p
99999` is a clean absence (empty/empty — branch 5, a legitimate `no-such-process`), and
`100000` is the first rejected value. Linux's default `pid_max` is 4194304, so 100000 there
is an ordinary unused pid and this arm would silently become an absence test — asserting
`instrument-failed` against real bytes that correctly say `no-such-process`.

Design: an ordered candidate ladder, escalating until one produces non-empty stderr.
Measured rejections on Darwin: `100000`, `4194305`, `2147483647`, `4294967296`,
`99999999999999999999` — all `process id too large`. Ladder:
`{100000, 4194305, 2147483648, 4294967296, 99999999999999999999}` (the last as a string
operand, past `int64`). Rules:

- Escalate while stderr is empty — this covers both "accepted as an unused pid"
  (exit 1, empty/empty) and "it is a live pid" (exit 0, a real row).
- The first candidate with non-empty stderr is the arm; record which one fired.
- If the ladder exhausts, `t.Fatalf` naming every candidate and its triple. That is a real
  finding about the platform, not a flake.
- Build arm D's args with `pinStateArgs(candidate)` where the candidate fits an `int`, so
  this arm also exercises the exact production argument list. Arms A–C must build their own
  lists by construction — deviating from `pinStateArgs` is what makes them mis-invocations.

#### 2.3 Record reachability rather than asserting unreachable arms

AC2 requires the table to say which shapes can occur through `pinReadState` in production.
State it in the table's doc comment, per arm — not as assertions:

- **Non-numeric pid** — unreachable. `pinReadState` takes an `int`.
- **Non-positive pid** — unreachable past the guard at `:276`, and already covered by
  `TestPinClassifyState`'s final subtest (`:919`). Do not duplicate it.
- **Arms A, B, C** — unreachable without an edit to `pinStateColumns` / `pinStateArgs`,
  which `TestPinStateColumns_ReadsNoEnvironment` (`:950`) already catches. They are proven
  here as *platform* facts, not as reachable code paths.
- **Arm D** — structurally **reachable**. `pinReadState` bounds the pid below, never above,
  so any caller holding a garbage-but-positive pid reaches it. No current caller does
  (pids come from `pinScanArgv` matches and `cmd.Process.Pid`), but this is the one arm
  where the premise protects a live path rather than an edit-only one.

### 3. The record and its writer (AC3)

#### 3.1 The record

```go
type tdnRecord struct {
    Ticket   string   `json:"ticket"`     // "1250"
    Notes    []string `json:"notes,omitempty"`

    HeldPGID int      `json:"held_pgid"`
    HeldPID  int      `json:"held_pid"`

    ArgvScan  pinScan         `json:"argv_scan"`   // #1235 — the ONLY source of any command string
    Liveness  pinStateOutcome `json:"liveness"`    // #1235 — narrow per-pid read
    FIFO      fifoLiveOutcome `json:"fifo"`        // #1239
    Reap      tdnReapOutcome  `json:"reap"`        // this ticket
}

func (r *tdnRecord) note(format string, args ...any)   // mirrors reachRecord.note (:257)
```

No new liveness type, no verdict synthesised across the three components — #1251 owns the
join, and both live consumers own their own pid/content join at the rig level (#1236 AC2,
#1251 AC3). The record composes; it does not conclude.

`ArgvScan` is the only member that can carry a command string, which is the design AC3's
redaction clause names: `command` reaches the record from the content-first argv scan that
already publishes matched rows, never from the narrow per-pid read.

#### 3.2 The writer

```go
// writeTdnArtifacts persists the record as one JSON file, 0600, in dir.
func writeTdnArtifacts(t *testing.T, dir string, rec *tdnRecord)
```

Follows `writeReachArtifacts` (`:823-855`): `json.MarshalIndent`, trailing newline,
`0o600`, `t.Errorf` on failure (a lost artifact is loud but does not abort the caller's
remaining cleanups). The caller supplies `dir` from `os.MkdirTemp("", "pyry-1250-*")`.

**One divergence, and it is the redaction design.** `writeReachArtifacts` writes a second
file, `reach.ps.txt`, holding a verbatim three-integer `ps` snapshot. This writer emits
`teardown.json` and **nothing else**. This ticket takes no wide integer snapshot, and its one
wide read (`pinScanArgv` → `reachScanArgv`) never lets its raw table out of that frame
(`:861-862`). "The writer writes exactly one file" is therefore a checkable statement of
"no verbatim `ps` output is persisted," and the self-check asserts it by reading the
directory.

#### 3.3 The redaction self-check

Assertions over the *marshalled record*, never over this file's source — arm B and arm C
above legitimately contain the strings `nosuchcolumn` and `-Q`, so a source-level grep
tripwire would be checking the wrong artifact.

Populate a record whose every field is non-zero, including an `ArgvScan` with a matched row
and an exclusion, marshal it, and assert:

- The bytes contain no `environ`, no `ps -E`, no `-eww`, no bare `eww`.
- Marshalling `Liveness`, `FIFO` and `Reap` individually yields no `"command"` key. This is
  the positive form of "`command` may reach the record only from the argv scan," and it
  holds structurally today — the assertion is the tripwire against a future field.
- The written file's `Mode().Perm()` is `0o600`, and its directory holds exactly one entry.

`TestPinStateColumns_ReadsNoEnvironment` already pins the per-pid column set. Do not restate
it here; reference it.

## Concurrency model

None. Every symbol in this file is synchronous. AC1's classifier and AC3's writer touch no
goroutines; AC2's arms each run one `exec.Command(...).Output()` to completion. The one
timing-sensitive construct in the package — `pinPollState`'s bounded poll — is not needed:
nothing here observes a process transition.

## Error handling

Three rules, all inherited:

1. **An instrument failure is a datum, not an abort.** `tdnClassifyReapLog` takes no
   `*testing.T` and never fails a test; a malformed line produces `instrument-failed` with a
   `Detail` naming the arm. Same contract as `fifoLiveRead` and `pinReadState`.
2. **Failing safe means failing away from the finding.** Ambiguity lands on
   `instrument-failed`, never on `held-pgid-in-reap-line` (which a consumer reads as "the
   reaper worked") and never on `reap-line-without-held-pgid` (which it reads as a leak).
3. **A lost artifact is loud but not fatal.** `t.Errorf`, as in `writeReachArtifacts:852`.

Every `Detail` and the recorded `Line` pass through `reachCapCommand` (`:945`). This is not
cosmetic: Go's `exec` caps captured stderr at 32 KB, arm C's usage block is multi-line, and
these strings land in an artifact an operator pastes into a public issue.

## Testing strategy

Everything is a deterministic RED/GREEN gate. No `t.Skip`, no env gate, no
`WithWorktreeAuthenticated` (`fixtures.go:96`), no `resolveClaudeBin`
(`resilience_test.go:282`) — those two calls are exactly what would turn a green run into a
skipped one.

```
go test -tags e2e_realclaude -run '^TestTdn' -v ./internal/e2e/realclaude/
```

must PASS with **zero SKIP** lines. Verify by grepping the `-v` output for both `--- PASS`
and `--- SKIP`; an exit code of 0 does not distinguish them (see
`docs/knowledge/codebase/1230.md` and the reach probe's `reachFinish` note at `:792-794`).

**`TestTdnClassifyReapLog`** — table-driven, message text as a string literal:

- TextHandler rendering, one pgid, held present → `held-pgid-in-reap-line`.
- TextHandler rendering, two pgids in slog's quoted `pgids="[A B]"` form, held present →
  `held-pgid-in-reap-line`. A single-pgid-only matcher fails here.
- `slog.Default()` rendering (bare message, no `msg=`, leading `2026/07/30 22:59:06 INFO`),
  one pgid, held present → `held-pgid-in-reap-line`.
- `slog.Default()` rendering, quoted multi-pgid form, held present → same.
- Multi-pgid line, held pgid **not** among them → `reap-line-without-held-pgid`.
- **Substring collision**: held `77`, line carries `pgids=[7788]` → `reap-line-without-held-pgid`.
  Fails under any `strings.Contains` matcher.
- Stderr with no anchored line at all (carrying other pyry log lines, including
  `reap.go:59`'s `pgid=` Warn) → `no-reap-line`, and the `Detail` names both readings of
  silence.
- Empty input → `no-reap-line`.
- Anchored line with no `pgids=` token → `instrument-failed`.
- Anchored line with an unterminated value (`pgids="[4242 77`) → `instrument-failed`.
- Anchored line whose list holds a non-integer (`pgids="[4242 abc]"`) → `instrument-failed`.
- Two anchored lines, held pgid only in the second → `held-pgid-in-reap-line` with
  `LineCount == 2`. Proves the union, not first-match.

Per row, assert verdict, `PGIDs`, `LineCount`, and that a non-empty `Detail` accompanies
every `instrument-failed`.

**`TestTdnRealPSMisinvocationsFailSafe`** — the four arms of § 2.2. Per arm: exit status,
stdout-empty?, `len(exitErr.Stderr) > 0` (the load-bearing one), then
`pinClassifyState(...) == pinStateInstrumentFailed` with an explicit "and never
`pinStateNoSuchProcess`" failure message. Arm A additionally shows `pinStateRow` parses its
stdout where the platform produces the partial row. Arm D logs which ladder rung fired.

**`TestTdnRecordWriter`** — the § 3.3 assertions, as subtests of one function: the
marshalled-bytes redaction scan, the per-component no-`command` check, the file mode, and
the single-entry directory. Uses `t.TempDir()`; the `os.MkdirTemp` call belongs to #1251's
rig, not to the self-check.

Also run `go vet -tags e2e_realclaude ./internal/e2e/realclaude/` and `gofmt -l`. Note that
`gofmt -l` on this package is dirty on `main` for reasons unrelated to this ticket — filter
to the one new file.

## Open questions

1. **Does #1251 capture pyry's stderr through `probeSyncBuffer`?** The classifier is pure
   over `[]byte`, so it does not care, but if #1251 instead reads `pyry logs`' ring buffer
   (`control.NewRingBuffer`, `cmd/pyry/main.go:742`), the rendering is a third one. Not
   blocking: the fixture set already spans two handlers and the anchor is handler-agnostic
   by construction. Flag it to #1251's architect rather than pre-solving it here.
2. **`#1235`'s deferred SHOULD FIX** (`process_pin_liveness_test.go:603-613`, a subtest
   whose assertion re-derives its comparison slice) is explicitly *not* an AC. Fix it only
   if the diff is comfortably inside budget; skip it otherwise.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** Two, both explicit and both single-function. (a) `pyry`'s stderr
  bytes → `tdnClassifyReapLog`, the sole parser; nothing downstream re-parses the line, and
  the outcome carries parsed `[]int` rather than text for consumers to re-scan. (b) real
  `ps` stdout/stderr → `pinClassifyState`, unchanged from #1235. Untrusted input here is
  a subprocess's own output on the operator's machine, not network or third-party data;
  the design records it capped and never executes or path-expands it.
- **[Subprocess execution]** No finding, and one design decision worth naming. AC2 execs
  `ps` with **literal argument slices** — never `sh -c`, never a format string, never a
  caller-supplied column set. Arm D's ladder is a hard-coded constant list plus
  `pinStateArgs`, so no external value reaches an argument position. The one integer that
  does (`pinStateArgs(candidate)`) is bounded below by `pinReadState`'s `pid <= 0` guard
  (`:276`), which exists precisely because `strconv.Itoa(-1)` would render as a flag; arm D
  supplies only large positives. Environment is inherited, which is correct — none of these
  invocations reads or emits an environment column.
- **[Error messages, logs, telemetry]** The live risk in this family, and it is the one the
  spec spends most of § 3.3 on. `ps` output can carry an operator's
  `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` if any environment column is requested,
  and this record is destined for a public issue. Design: the per-pid read has no
  environment capability at all (`pinStateColumns`, pinned by
  `TestPinStateColumns_ReadsNoEnvironment`); the record's only command-bearing member is the
  argv scan, whose raw table never escapes `reachScanArgv`'s frame; the writer emits no
  verbatim `ps` file; and every `Detail`/`Line` is capped by `reachCapCommand` so arm C's
  multi-line usage block cannot flood an artifact. Residual, and accepted as inherited from
  #1230: `reachScanArgv` reads the full `command=` column, so a *needle-matched* row whose
  own argv contains a secret would reach the record. Out of scope here — that is #1230's
  established boundary and this ticket adds no new wide read.
- **[File operations]** Artifacts are `0o600` in a caller-supplied directory (`t.TempDir()`
  in the self-check, `os.MkdirTemp("", …)` in #1251's rig) — outside the repo, so nothing
  can be committed by accident. No path is built from external input: filenames are
  constants joined onto that directory, so there is no traversal surface. No atomic
  temp-plus-rename: a single terminal write of a diagnostic artifact has no partial-state
  recovery requirement, matching `writeReachArtifacts`. No symlink following — the writer
  creates rather than opens.
- **[Concurrency]** No finding, by construction: nothing here spawns a goroutine, takes a
  lock, or shares state. See § Concurrency model.
- **[Tokens / Cryptographic primitives / Network & I/O]** Not applicable, and not by
  omission: this ticket mints no token, performs no comparison against a secret, opens no
  socket, and starts no server. Its only randomness-adjacent value is `t.TempDir()`'s name,
  chosen by the stdlib and not security-relevant.
- **[Threat model alignment]** The relevant threat for this package is "diagnostic evidence
  destined for a public issue leaks operator credentials," carried in
  `background_reach_probe_test.go:57-92` (redaction rules 1–3) and
  `process_pin_liveness_test.go:60-75`. Every rule is addressed above and none is weakened;
  rule 1's "do not have the capability" posture is preserved — this ticket adds no new `ps`
  column set of its own.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-30
