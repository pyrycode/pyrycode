# #1656 — evidence: measure how `--resume` answers an absent transcript

## Files to read first

Everything below except the last three entries is in **package `realclaude`, build tag
`e2e_realclaude`** — directly callable, no import, no copy. That reuse is most of why this
ticket is `s`.

- `internal/e2e/realclaude/session_transcript_probe_test.go` → `startProbeChild`,
  `probeChild.alive`, `probeChild.waitExit`, `probeChild.snapshotExit` — the child lifecycle
  this file reuses wholesale. Note `snapshotExit` returns `(-1, "")` for a child that has not
  exited; that is AC 3's "did not exit within N".
- same file → `endTurnlessChild`, `terminationMode` (`terminationSIGTERM` /
  `terminationSIGKILL` / `terminationDidNotExit`) — SIGTERM → grace → SIGKILL, never Fatals,
  reports which signal actually ended the child. Despite the name it ends **any** probe child.
- same file → `boundedBuffer`, `recordStream`, `redactCredentials` — bounded ingest capture and
  the single redact-then-truncate egress. `recordStream` is the **only** permitted path from a
  captured stream to a string this test emits.
- same file → `statByID`, `statByIDPolled`, `probeReading` — the by-id instrument, through
  `transcript.StatByID`. An absent file returns `(Result{}, err)` wrapping the raw `os.Stat`
  error; that is the expected answer here, not a failure.
- same file → `transcriptProbeArgs` — the first-spawn argv shape. **Do not edit it**; #1655's
  test depends on it. Extract the shape it emits; this ticket writes a sibling.
- same file → `TestRealClaude_TurnlessSessionIDTranscript` — its **arm A block** (start child,
  write one turn, locate the directory, close stdin, wait, snapshot) is the ~58-line turnful
  drive to transcribe. There is no control-turn helper to call.
- same file → `classifyTurnlessTranscript` and `TestTurnlessTranscriptVerdict` — the
  verdict-function-plus-offline-table shape this file mirrors, including the
  `strings.HasPrefix(sentence, want+":")` assertion.
- `internal/streamsup/runner.go` → `buildArgs` — production's argv. Confirms the only
  difference between a first spawn and a respawn is the trailing pair. The `firstRun` latch
  it reads is advanced in `Runner.Run`'s post-wait block, on `started`, which is the bug
  #1630 fixes.
- `internal/e2e/realclaude/interactive_stream_new_session_test.go` →
  `streamNewSessionTranscriptDir` — polls `<home>/.claude/projects/*/` for `<id>.jsonl`,
  returns that dir, `t.Fatalf`s with a full tree listing on timeout. **Do not edit it**;
  `TestInteractiveStreamNewSessionRotatesAndSpawnsFresh` depends on it.
- `internal/e2e/realclaude/set_permission_mode_probe_test.go` → `setModeTurnLine` — one
  newline-terminated stream-json user-turn line in pyry's production envelope shape.
- `internal/e2e/realclaude/permission_protocol_spike_test.go` → `captureClaudeVersion` —
  `(raw, token)` for the record.
- `internal/e2e/realclaude/fixtures.go` → `WithWorktreeAuthenticated` (and `WithWorktree`
  beneath it) — per-test temp dir pinned as `$HOME` via `t.Setenv`, credential re-pin, clean
  skip when neither credential variable is set. **Do not edit it.**
- `internal/transcript/transcript.go` → `StatByID`, `Result.Found` — the primitive #1630's
  respawn probe calls, so evidence and consumer read with one instrument.
- `internal/agentrun` → `ResolveWorkdir` — canonicalises the child cwd the way `streamsup`
  does before assigning `cmd.Dir`. Signature is `(string, error)`.
- `internal/sessions/reconcile.go` → `DefaultClaudeSessionsDir` — the production reference.
  **Read it to know what it is; do not use it to derive the directory here** (see § Design).
- `docs/knowledge/features/session-transcript-and-resume-probe.md` — the record this
  measurement extends. Its `#1655` sections' findings survive unchanged.
- `docs/knowledge/decisions/032-bootstrap-resume-per-spawn-existence-probe.md` § Context —
  the refusal fact that orders the arms, and the ~220 ms latency that calibrates the deadline.

## Context

`internal/streamsup` picks a spawn's id flag from a one-way latch: `--session-id <id>` on the
first spawn, `--resume <id>` on every spawn after. The latch flips as soon as `cmd.Start`
succeeded, not when a transcript reached disk. The suspected failure (observed 2026-08-18) is a
crash-loop: a respawn emits `--resume <id>` for a session claude has no record of, claude exits
non-zero, and the daemon retries forever on a widening backoff.

That story rests on a claim this repo has never measured: **`--resume <id>` against an absent
`<id>.jsonl` exits non-zero.** ADR 032 already decided the rule that would fix the loop (resume
iff the transcript exists, by-id, no dir scan) and #1630 carries it into `streamsup`. #1655 has
landed the other half of the premise — a turnless `--session-id` launch leaves no `<id>.jsonl`,
**HOLDS** at claude 2.1.220. What #1655 measured is that the id claude is asked to resume was
never established. What is still unmeasured is how claude answers when asked anyway.

A non-zero exit on its own proves nothing: bad credentials, a rejected argv shape, or a broken
workdir all exit non-zero. So the absent arm needs a control in the same run and the same
workdir — the same argv against an id whose transcript *does* exist. Only the contrast makes a
non-zero exit attributable to the absence.

**No ADR is warranted.** ADR 032 already carries the decision; this ticket supplies the
measurement that decision assumed. The record belongs in the existing feature doc, and the
documentation phase writes it (see § What the developer does not deliver).

## Design

One new file, `internal/e2e/realclaude/resume_absent_transcript_probe_test.go`, `//go:build
e2e_realclaude`, package `realclaude`. **Zero production files.** #1655's file is green and
stays untouched.

### The single property the verdict reads

Everything hard about this ticket collapses once you notice the verdict reads exactly **one
derived boolean per arm**: did claude *reject* the resume?

```go
// armOutcome is one resume arm's answer, snapshotted BEFORE any signal from us.
type armOutcome struct {
    Exited   bool // exited on its own, within the deadline
    ExitCode int  // meaningful only when Exited
}

func (o armOutcome) rejected() bool { return o.Exited && o.ExitCode != 0 }
```

An arm that is still running at its deadline has **not** rejected the resume — claude accepted
the argv and is sitting on stdin, exactly as #1655's turnless child did. So `did-not-exit` and
`exit 0` mean the same thing to the verdict (no rejection) and different things to the reader,
which is why the record carries the exit code, the wait error and the did-not-exit flag
verbatim while the classifier reads only `rejected()`.

**Snapshot the outcome before terminating, never after.** This is the one way to get a wrong
number recorded as a right one: an arm that does not exit on its own is ended with `SIGTERM`,
after which `snapshotExit` reports **143** — a non-zero exit that would read as a rejection the
child never made. The sequence inside the arm runner is therefore fixed:

1. `exited := c.waitExit(deadline)`
2. if `exited`, `code, waitErr := c.snapshotExit()` → that pair, and only that pair, builds
   `armOutcome{Exited: true, ExitCode: code}`
3. if not, `armOutcome{Exited: false}` — and only *then* `endTurnlessChild`
4. the post-termination exit code and `terminationMode` are recorded as **cleanup detail**, in
   their own fields, and are never fed to the classifier

### Argv

A sibling of `transcriptProbeArgs`, not a changed signature:

```go
// resumeProbeArgs returns the argv shape buildArgs emits on a RESPAWN — the same
// fixed stream-json prefix and base args as transcriptProbeArgs, with the trailing
// pair swapped to `--resume <id>`.
func resumeProbeArgs(sessionID string) []string
```

Both resume arms call it, so their argvs are identical by construction bar the id — the same
structural argument #1655 used to make its control meaningful. The establishing arm calls
#1655's `transcriptProbeArgs` unchanged.

### Constants

Declare this ticket's own, do not alias #1655's. Reusing its constants would let a future edit
to #1655's file silently change #1656's recorded parameters; the functions and types are the
reuse target, eight lines of constants are not worth the coupling.

- `<A>` = `16560000-0000-4000-8000-000000000001` — the established (present) id
- `<C>` = `16560000-0000-4000-8000-000000000002` — the reserved absent id
- model / max-turns / prompt: same values #1655 used (`claude-haiku-4-5`, `2`, a one-word
  reply prompt)
- **resume-arm deadline: 45 s**, shared by both arms. ADR 032 measured the sibling refusal
  (`--session-id` on a *present* transcript) at ~220 ms, and #1655's nearest calibration
  windows are 30 s settle / 60 s exit / 120 s dir. 45 s is two orders of magnitude of headroom
  over the observed refusal latency while staying under #1655's exit window. It is recorded
  per arm, because a claude that rejects more slowly than N would be recorded as "still
  running" and read as FALSIFIED.
- kill grace 5 s (mirrors `streamsup`'s `killGrace`), hard wait 15 s, post-arm `<C>` poll
  window 10 s, dir budget 120 s, establish-arm exit window 60 s
- outer `context.WithTimeout` leak guard per arm, comfortably above `deadline + grace + hard`
  (3 min). A context kill is a `SIGKILL`, so this must never be the thing that ends an arm —
  if it fires, the arm records as did-not-exit.

### Order of operations

ADR 032 records that claude **refuses** `--session-id <uuid>` when `<uuid>.jsonl` already
exists, so the turnful establish must precede anything that resumes `<A>`:

1. **Establish `<A>`** — transcribe the arm A block of
   `TestRealClaude_TurnlessSessionIDTranscript`: `startProbeChild` under
   `transcriptProbeArgs(<A>)`, write one `setModeTurnLine` turn, close stdin, wait, snapshot.
2. **Locate the directory empirically** — `streamNewSessionTranscriptDir(t, home, <A>,
   dirBudget)`. This is authoritative and is AC 2's "if `<A>.jsonl` never appears, the run
   fails rather than recording a verdict" (the helper Fatals with a tree listing).
   `DefaultClaudeSessionsDir(childCwd)` is recorded alongside as a compared-against value only
   — it applies `EvalSymlinks` where the child's cwd went through `ResolveWorkdir`'s
   `canonicalCase` (the #989 hazard), so a recomputation risks asserting absence against a
   folder claude never wrote. A divergence is a **recorded outcome**, logged, not a failure.
3. **Pre-read `<C>`** — `statByID(dir, <C>)` in that same directory, with the same instrument.
   `Found` ⇒ `t.Fatalf`: a reserved id that already exists means the fixture is not what the
   design assumes, and no verdict is readable.
4. **Absent arm** — `runResumeArm(<C>)`.
5. **Post-read `<C>`** — `statByIDPolled(dir, <C>, postWindow)`. A *present* file here is the
   stub finding #1630's by-id probe would flip to `--resume` forever on. It is recorded with
   its **own** sentence and **does not** enter the verdict, which AC 2 defines purely on exits.
6. **Control resume arm** — `runResumeArm(<A>)`, same builder, same workdir, same deadline.

The control resume arm runs **last** so that nothing between the pre-read and the absent arm
can create `<C>`, and so that step 5's reading is taken with no other claude process having run
since the arm ended. AC 2's rule is order-independent, so this costs nothing.

All six steps live in **one test function**: `WithWorktree` pins `$HOME` with `t.Setenv`, so a
second function gets a different `$HOME` and the pinned directory no longer exists. No
`t.Parallel` on the live test, for the same reason.

### The arm runner

One helper, called twice. Two inline copies of a launch-wait-snapshot-terminate sequence is
where the deadline-before-signal rule gets applied in one and forgotten in the other.

```go
// runResumeArm launches one `--resume <id>` child, waits out the deadline, snapshots
// its outcome BEFORE any signal, then ends whatever is left. Never fails the test for
// the child's behaviour — did-not-exit is a recorded outcome. Fatals only when
// startProbeChild fails, which is a broken instrument, not a measurement.
func runResumeArm(t *testing.T, claudeBin, cwd, sessionID string, deadline time.Duration) resumeArm
```

Inside: outer `context.WithTimeout`; `startProbeChild`; `t.Cleanup` kill; **`defer` the stdin
close** so it runs after termination — closing stdin early sends EOF, and a child that exits on
EOF would be recorded as having answered the resume when it answered the EOF; the four-step
snapshot sequence above; `recordStream` on both buffers.

### The record

Three structs, all unexported, all JSON-tagged, mirroring #1655's shape so the two doc sections
read alike.

- `resumeArm` — session id, argv, deadline ms, `Exited`, `ExitCode`, `WaitError`,
  `TerminationMode`, `TermToExitMs`, `PostTermExitCode`, `Stdout`, `Stderr`, and a
  `CarryingStream` string naming which stream carried the operator-visible message (AC 1). Set
  it from which of the two recorded strings is non-empty: `"stderr"`, `"stdout"`, `"both"`, or
  `"neither"`.
- `establishArm` — session id, argv, prompt, exit code, wait error, transcript path, transcript
  size, stderr. **Its stdout is not recorded**: it is one very long stream-json line whose only
  content is "this claude version accepts the argv and completes a turn", and #1655's doc
  elided it for exactly that reason.
- `resumeProbeRecord` — claude version raw + token, child cwd, empirical dir, recomputed dir,
  dir match, the establish arm, both `<C>` readings (`probeReading`), both resume arms, the
  verdict, the verdict sentence, and the stub sentence.

Emit it the way #1655 does: the plain-language lines via `t.Logf` **first**, so the verdict
survives a truncated log, then the `json.MarshalIndent` blob.

### The classifier

```go
// classifyResumeAbsent maps the two arms' outcomes to the one sentence a reader must
// not have to infer. A control that rejected a resume whose transcript EXISTS is
// measuring the environment, not the absence, and short-circuits everything.
func classifyResumeAbsent(control, absent armOutcome) (verdict, sentence string)
```

Three arms, in this order:

| condition | verdict |
|---|---|
| `control.rejected()` | **INCONCLUSIVE** |
| `!control.rejected() && absent.rejected()` | **HOLDS** |
| `!control.rejected() && !absent.rejected()` | **FALSIFIED** |

Each returns a sentence opening `"<VERDICT>: "`, stating the observation in plain language.
The INCONCLUSIVE sentence must name *why* — the control rejected a resume whose transcript
exists, so the shape rejects resumes generally and the absent arm's answer attributes to
nothing.

The nine-cell cross-product of `{exit 0, exit non-zero, did-not-exit}²` maps onto those three
rows without a gap, which is the point of collapsing to `rejected()` first: the outcome space
is wider than the verdict space, and every cell has a home.

A separate one-line helper produces the stub sentence from the post-read's `Found`, stating
whether a rejected resume left `<C>.jsonl` behind and what that means for #1630's probe
converging.

## Concurrency model

Inherited, not designed. `startProbeChild` spawns exactly one waiter goroutine per child whose
only job is to store `cmd.Wait`'s error and `close(exited)`; `alive`, `waitExit` and
`snapshotExit` all read through that channel, which makes every read race-free. Three children
exist across the run and each is ended before the next starts, so at most one is live at a
time.

Two rules the new code must not break:

- **The waiter must not touch `*testing.T`.** On a did-not-exit path it outlives the test
  function, and a `t.Logf` from a goroutine after the test completes panics with "Log in
  goroutine after test has completed" — destroying the record on exactly the outcome that is
  hardest to reproduce. `runResumeArm` returns data; it does not log from a goroutine.
- **`cmd.Env` stays nil.** Assigning it drops `WithWorktree`'s pinned `HOME` (the child writes
  into the operator's real projects tree and the empirical scan finds nothing) *and* drops the
  credential.

Shutdown: every child gets a `t.Cleanup` `Process.Kill`, and `endTurnlessChild` signals a
single pid, never a negative-pid process group — under some `go test` invocations the test
binary shares the child's group.

## Error handling

The design distinguishes three outcomes that all look like "something went wrong":

| situation | handling |
|---|---|
| `claude` not on PATH, no credential | `t.Skipf` / `WithWorktreeAuthenticated`'s skip |
| `startProbeChild` fails, `<A>.jsonl` never appears, `<C>.jsonl` already present | **`t.Fatalf`** — broken instrument, no verdict readable |
| an arm exits non-zero, exits zero, or does not exit | **recorded**, classified, test passes |

`statByID`'s error on an absent file is the expected answer and is stored in
`probeReading.StatError`, never branched on beyond `Result.Found()`. A directory divergence
between the empirical and recomputed paths is `t.Logf`'d, not failed.

The Fatal messages must include `recordStream(...)` output, never a bare `snapshot()` — the
Fatal that fires on an argv rejection or auth failure is the highest-risk site in the file
(see § Security review).

## Testing strategy

**Live measurement** — `TestRealClaude_ResumeAbsentTranscript`. One run, six steps, one
workdir. Expect roughly 60–120 s wall clock: one cheap haiku turn, the absent arm (fast if the
fact HOLDS), and the control arm burning its 45 s deadline in the healthy shape where claude
accepts the resume and waits on stdin.

**Offline classifier table** — `TestResumeAbsentVerdict`, `t.Parallel`, no credentials, no
subprocess, passes on a machine with no `claude` at all. All nine cross-product rows, each
naming both arms' outcomes:

- control did-not-exit × absent exit non-zero → **HOLDS**
- control exit 0 × absent exit non-zero → **HOLDS**
- control did-not-exit × absent exit 0 → **FALSIFIED**
- control exit 0 × absent exit 0 → **FALSIFIED**
- control did-not-exit × absent did-not-exit → **FALSIFIED**
- control exit 0 × absent did-not-exit → **FALSIFIED**
- **control exit non-zero × absent exit non-zero → INCONCLUSIVE** — AC 5's mandated row, the
  one that must not read HOLDS
- control exit non-zero × absent exit 0 → **INCONCLUSIVE**
- control exit non-zero × absent did-not-exit → **INCONCLUSIVE**

Assert both the verdict string and that the sentence opens with `want + ":"`, mirroring
`TestTurnlessTranscriptVerdict` — the sentence is the deliverable AC 4 forbids a reader from
having to infer, so it has to state the verdict rather than merely accompany it.

**Gates.** `make check` passes (it never compiles this package, so it proves only that nothing
else broke). `go vet -tags e2e_realclaude ./internal/e2e/realclaude/` must be run manually and
be clean. The live run is judged by the count of `=== RUN` lines, never by the exit code — a
suite that skips everything and a package that failed to build both exit 0.

## What the developer does not deliver

**AC 4's doc section is the documentation phase's deliverable, not the developer's.**
`docs/knowledge/features/` is owned by that phase. The same shape #1655 used applies here: the
test landed in the developer's commit and the record doc in the documentation-phase commit,
written from the live run's `t.Logf` output.

The developer's obligation is to make that source **complete** — every field AC 4 names must be
present in the logged record: claude version, argv and deadline per arm, verbatim per-arm
output with the carrying stream named, both `<C>` readings, the verdict, the verdict sentence,
and the stub sentence.

Notes for the documentation phase, carried here so they are not lost:

- New **top-level section naming #1656**. The `#1655` sections' recorded findings survive
  unchanged; only the doc's title line and a pointer sentence in `## TL;DR` may be touched, and
  only so #1655's `**HOLDS.**` is not read as covering this measurement too.
- The doc is already in `docs/knowledge/INDEX.md`; no new index entry.
- **Read the captured stderr before transcribing it** — see § Security review, finding
  [Tokens].

Also not deliverables: no testdata fixture files (the record is two exit codes, two short
messages and two file readings, and the doc is already the record), and no edits to
`transcriptProbeArgs`, `streamNewSessionTranscriptDir`, or `fixtures.go`.

## Open questions

- **Does `--resume <absent>` reject at all?** Unknown by construction — that is the
  measurement. If it does not exit within 45 s, the classifier records FALSIFIED, and #1630's
  premise needs revisiting rather than the test being retried with a longer deadline. Do not
  raise the deadline to chase a preferred verdict.
- **Does a rejected resume leave a stub `<C>.jsonl`?** Also unknown; step 5 answers it. A
  present stub is a real finding for #1630 — its by-id probe would see the stub, choose
  `--resume` forever, and never converge — and it should be surfaced to #1630 as a comment,
  not silently absorbed into this record.
- **Directory divergence.** #1655's run showed empirical and recomputed matching on macOS. If
  this run diverges, that is the finding the follow-up supplying this directory on the daemon's
  production path needs; record it, do not fail.
- **Version scope.** Like #1655, this is a claude-version fact, not a guarantee across
  versions. The record names the version measured.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. One boundary: subprocess stdout/stderr → recorded string,
  crossed only in `recordStream`, which redacts before truncating (so a truncation boundary
  cannot slice a token into a form the redactor no longer matches). The spec forbids a bare
  `boundedBuffer.snapshot()` anywhere in the new file, including inside `t.Fatalf` messages,
  and § Error handling states it at the site where it is easiest to forget. The second
  boundary — `<C>.jsonl` on disk → verdict — is read only through `statByID`, which validates
  the stem in `transcript.StatByID` before any `filepath.Join`.
- **[Tokens, secrets, credentials]** SHOULD FIX, mitigated. `redactCredentials` replaces the
  values of `ANTHROPIC_API_KEY` and `CLAUDE_CODE_OAUTH_TOKEN` read from the environment, which
  is how the credential actually reaches the child (`cmd.Env` is nil, so it is inherited).
  **Residual:** `WithWorktreeAuthenticated` also copies the operator's real `~/.claude.json`
  verbatim into the pinned temp `$HOME`, and that file can hold OAuth material the env-var
  redactor does not know. Unlike #1655 — which recorded empty stderr on both arms — this run
  *expects* non-empty stderr on the absent arm, so the exposure is newly live. Judged
  SHOULD FIX rather than MUST FIX: a resume rejection names a session id, and no observation
  exists of claude echoing its credentials file to stderr, so building a second redactor would
  be a defence for an unobserved failure mode. The deterministic net stays `recordStream`; the
  spec adds an explicit instruction under § What the developer does not deliver that the
  documentation phase reads the captured stderr before transcribing it into the doc, which is
  a different fabric from the code-level redactor rather than a second stochastic copy of it.
  If a future run *does* record credential-shaped stderr, that is the evidence that escalates
  this to a code change.
- **[File operations]** No findings. Every path is under a `t.TempDir()` `$HOME` at the test
  process's own umask; no file is created by this design at all — the only writes are claude's
  own transcripts. The two `<C>` readings are `os.Stat` with no subsequent open, so there is no
  check-then-use gap to exploit: the stat result *is* the observation, not a precondition for a
  later operation. Ids are compile-time constants matching `transcript.ValidStem`, so the
  traversal surface `StatByID` guards against is empty by construction here — the guard is
  defence in depth, not the only barrier.
- **[Subprocess / external command execution]** No findings. `startProbeChild` uses
  `exec.CommandContext` with an argv slice, never `sh -c`. Every argv element is a compile-time
  constant or a constant session id — no user-controlled value reaches the command line. The
  binary is resolved with `exec.LookPath("claude")`. Environment is inherited deliberately
  (documented in `startProbeChild`); scrubbing it would drop both the pinned `HOME` and the
  credential. Termination is `SIGTERM` → grace → `SIGKILL` on a **single pid**, never a
  negative-pid process group, because under some `go test` invocations the test binary shares
  the child's group and a group signal would take out the runner. Double-fork escape is out of
  scope: this is a test harness measuring a first-party binary, and `t.Cleanup` plus the outer
  context both backstop a survivor.
- **[Cryptographic primitives]** Not applicable — no randomness, no comparison against a
  secret, no key material handled by this design. The session ids are fixed reserved stems per
  this package's convention, deliberately *not* runtime-minted, so there is no RNG choice to
  audit.
- **[Network & I/O]** No findings on the design's own surface; the network exposure is claude's
  own API call, not this test's. Input size **is** capped: `boundedBuffer` bounds at ingest
  (1 MiB/stream) rather than at record time, so a looping child cannot balloon memory before
  anything trims it, and `recordStream` caps again at 64 KiB on egress. Every wait is bounded —
  the per-arm deadline, the kill grace, the hard post-`SIGKILL` bound, the dir poll budget, the
  post-arm stat poll window — and the outer `context.WithTimeout` per arm is the leak guard
  above all of them. There is no unbounded read anywhere in the design.
- **[Error messages, logs, telemetry]** No findings beyond [Tokens]. Every operator-visible
  string derived from a child stream goes through `recordStream`. Paths that appear in the
  record are temp-dir paths that cease to exist when the test ends; #1655's committed record
  already contains the same shape, so this discloses nothing new. No telemetry, no metrics, no
  user-identifiable aggregation.
- **[Concurrency]** No findings. No locks are taken by the new code; `boundedBuffer`'s mutex is
  inherited and already guards the one genuine race (`os/exec` writes from its copying
  goroutine while the test reads). One waiter goroutine per child, exiting when `cmd.Wait`
  returns; on a did-not-exit arm it may outlive the test, which is why the spec forbids it
  touching `*testing.T`. No shared state is checked-then-mutated. The one TOCTOU-shaped
  sequence in the design — snapshot the arm outcome, *then* signal — is ordered deliberately
  and called out in § Design, because the reversed order records a `SIGTERM`'s 143 as a
  rejection claude never made.
- **[Threat model alignment]** Not applicable to the relay threat model — no relay, no network
  peer, no device identity. The design does not touch `internal/relay`, `internal/noise`, or
  `internal/keys`. The one threat it *does* sit adjacent to is ADR 032's confused-deputy
  isolation gap (a restart adopting a second claude's newer transcript); this design cannot
  reintroduce it because every read is by-id through `StatByID` and no dir scan for a "newest"
  transcript occurs. `streamNewSessionTranscriptDir` scans for a *directory* containing a
  known id, not for an id to adopt.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-20
