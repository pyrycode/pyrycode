# #1253 — Reap-log classifier, driven by bytes captured from a real reap

**Ticket:** [#1253](https://github.com/pyrycode/pyrycode/issues/1253) (split from #1236)
**Size:** S — one new test file, zero production files, zero consumer call sites.
**Package:** `internal/e2e/realclaude`, build tag `e2e_realclaude`, identifier prefix `tdn*`.
**Live claude:** none. No credentials, no daemon, no env gate, no `t.Skip`. This ticket must
not acquire `needs-real-claude`.

---

## § 0 — What this spec does NOT prescribe

Read this first; it is the budget.

- **No second classifier.** `tdnClassifyReapLog` merged in #1250 (`teardown_liveness_test.go:144`)
  and is four-valued, unions pgids across every anchored line, decides membership over parsed
  integers, and anchors on the bare message text. It is called here and **not edited**.
- **No edit to `teardown_liveness_test.go`.** See § 2 — that file is in flight on another branch.
  Everything this ticket adds lands in one new file.
- **No `ps` invocation of its own.** See § 6.
- **No new production file, no change to `internal/agentrun`.** `ReapDescendantGroups` is already
  exported and already the thing under observation; it is called, not modified.
- **No ordering claim.** The reap line lands on stderr and the result trailer on stdout — two
  pipes, two copier goroutines. This ticket adds no claim about which came first, and no verdict
  about pyry.
- **No record/writer work.** `tdnRecord` and `writeTdnArtifacts` are #1250's and #1251's. Nothing
  here writes an artifact.
- **No `docs/knowledge/codebase/1253.md`.** The documentation phase owns that after the PR merges.

---

## § 1 — Files to read first

| Path | What to extract |
|---|---|
| `internal/agentrun/reap.go:35-67` | The subject. Note `len(reaped) > 0` guards the emit at `:64` (silence has two readings); `pgid <= 1 \|\| pgid == self \|\| pgid == rootPid` at `:52` is the exclusion set AC3 exercises; the `logger.Info` at `:65` is the only line classified. |
| `internal/agentrun/reap_test.go:27-101` | **The proven tree design.** Four subtests covering exactly the shapes AC1/AC3 need. Read `ReapsGrandchildGroupSparesRoot` (`:88`) and `ExcludesRootOwnGroup` (`:69`) — this ticket's mixed tree is those two merged. |
| `internal/agentrun/reap_test.go:120-199` | `startReapHelper` / `waitReport` / `processAlive` / `waitGroupGone` — the cleanup discipline, the report-file poll, and (`:117-119`, `:239-244`) **why a background `Wait` per child is load-bearing**: a zombie still answers `kill(-pgid, 0)`. Unexported in `package agentrun`; see § 3. |
| `internal/agentrun/reap_test.go:217-269` | `runReapHelper` / `spawnGrandchildAndBlock` — the re-exec role dispatch this ticket mirrors in reduced form. Note the `os/exec` env-dedup comment at `:252-254`. |
| `internal/e2e/realclaude/teardown_liveness_test.go:24-49` | The file header's argument for why the anchor is the bare message and why membership is over parsed integers. Do not restate it in the new file; cite it. |
| `internal/e2e/realclaude/teardown_liveness_test.go:89-127` | `tdnReapMessage`, the four verdict constants, `tdnReapOutcome`'s fields. The new file consumes all of these. |
| `internal/e2e/realclaude/teardown_liveness_test.go:144-219` | `tdnClassifyReapLog` — the contract being driven. Note the `heldPGID <= 1` rejection at `:147`. |
| `internal/e2e/realclaude/teardown_liveness_test.go:460-481` | The existing prefix-direction row (held `77` vs `pgids=[7788]`). AC4 is its suffix twin — read it and match its comment register. |
| `internal/e2e/realclaude/teardown_liveness_test.go:1176+` | `tdnIsReapVerdict`, `tdnEqualInts` — reuse, do not redeclare. |
| `internal/e2e/realclaude/fixtures_test.go:338-377` | `TestMain` and `runFakePyry`. **Two things:** the `GO_TEST_HELPER_PROCESS` branch and the fork-bomb lesson in its comment (§ 3), and `PYRY_E2E_FAKE_MODE=sleep` at `:366` — the ready-made blocking leaf this ticket reuses instead of adding a leaf role. |
| `internal/e2e/realclaude/process_pin_liveness_test.go:221-232` | `pinStateColumns` and its prohibition. Inherit it verbatim (§ 6); do not weaken it. |
| `docs/knowledge/codebase/1250.md` | § Implementation and § Lessons learned — what merged, and the two open items (`:529-540` weak row, the untested nits) that belong to **#1251, not here**. |
| `CODING-STYLE.md` § Testing | The `TestHelperProcess` re-exec pattern this ticket's helper role follows. |

---

## § 2 — The one design decision that isn't obvious: a new file

**The file-overlap check (architect § 1.5) found a real overlap.** Run 2026-07-31:

```
origin/feature/1251 → internal/e2e/realclaude/teardown_liveness_test.go
                      internal/e2e/realclaude/teardown_liveness_probe_test.go
```

`#1251` is OPEN at `done:code-review` with `needs-real-claude` (PR #1256), and its diff to
`teardown_liveness_test.go` is **+259 / −40**, with hunks *inside* `TestTdnClassifyReapLog`'s
table (`@@ -532,0 +631,14 @@`, `@@ -534 +646 @@`, `@@ -537,3 +649,5 @@`) — precisely where AC4's
row would land.

The ticket is entirely offline and its own Technical Notes say it "ships without waiting on an
operator." Blocking it behind an operator-gated live rig would be the wrong trade when the overlap
is removable by design. **So this ticket touches exactly one file, and that file is new:**

```
internal/e2e/realclaude/teardown_reap_capture_test.go
```

Same package, same build tag, same `tdn*` prefix — no second family name, per the ticket's note.
No in-flight branch touches this path (it does not exist yet), so no `blockedBy` is set and no
merge conflict is possible.

This is better design independently of the conflict. `teardown_liveness_test.go`'s own header
declares its contents "pure over bytes: no exec, no file read, no clock" and "takes no
measurement." This ticket's harness spawns real process trees, redirects a process-global writer,
and issues real SIGKILLs. Filing it under that header would falsify the header. The package
already keeps one concern per file (`process_pin_liveness_test.go`, `fifo_reader_liveness_test.go`,
`background_reach_probe_test.go`).

**Consequence for AC4.** The suffix row goes in the new file as a small focused test carrying
**both** directions of the substring hazard side by side (prefix `77` and suffix `88`, both against
`pgids=[7788]`), rather than as a lone row appended to the sibling's table. Duplicating the prefix
row costs six lines and makes the pair legible in one place; the sibling's existing prefix row
stays untouched. Name the sibling row's location in a comment so neither half can be dropped
without the other being findable.

**Symbols verified free** on `origin/main` **and** on `origin/feature/1251` (both files) as of
2026-07-31: `tdnTree`, `tdnStartTree`, `tdnStartLeaf`, `tdnCaptureReap`, `tdnAlive`,
`tdnAliveThroughout`, `tdnGroupGone`, `tdnWaitTreeReport`, `TestTdnRealReapCapture`,
`TestTdnReapTreeHelperProcess`, `TestTdnClassifyReapLogSubstringMembership`. Note `tdnScan` **is
taken** by #1251 — do not use `tdnScan*`. Re-run the check before naming anything not on this list:

```bash
git show origin/feature/1251:internal/e2e/realclaude/teardown_liveness_test.go | grep -E '^(func|type|const|var) '
git show origin/feature/1251:internal/e2e/realclaude/teardown_liveness_probe_test.go | grep -E '^(func|type|const|var) '
```

---

## § 3 — Building the tree: why re-exec, and how not to fork-bomb

### The shape the criteria force

`descendantPGIDs` walks from `rootPid`'s **children** downward, and `reap.go:52` excludes
`pgid == rootPid`. So a group that actually gets killed must sit **two levels** below the test:

```
test process  (never the walk root — see the guard in § 4)
└── parent P          started with Setpgid → its own group, pgid == P.pid == rootPid
    ├── leaf F        started with Setpgid → own group, pgid == F.pid   → REAPED
    └── leaf S        started without Setpgid → pgid == P.pid == rootPid → SPARED by :52
```

A one-level tree cannot produce a reaped group without rooting the walk at the test process, which
is forbidden. So the intermediate process must be able to call `setpgid` on its own children — and
that rules out a shell: `setsid(1)` is not present on macOS, and a background job in a
non-interactive `sh` stays in the shell's own group. **The intermediate must be a Go binary, which
means re-execing the test binary.** This is the same conclusion `reap_test.go` reached; that
fixture is unexported in `package agentrun` and not importable from a build-tagged test in
`internal/e2e/realclaude`, so the role is rebuilt here in reduced form rather than shared. Do not
add a non-test package to make it shareable — that would put process-tree helpers into production
for one test's benefit.

### The fork-bomb guard

`internal/e2e/realclaude` **already has a `TestMain`** (`fixtures_test.go:348`) which branches on
`GO_TEST_HELPER_PROCESS == "1"` and otherwise falls through to `m.Run()`. Its comment records the
2026-05-16 fork bomb: a re-exec of the test binary that reaches `m.Run()` unbounded recursion.

**The design that cannot recurse, and touches no shared file:**

- The parent is re-exec'd as
  `exec.Command(os.Args[0], "-test.run=^TestTdnReapTreeHelperProcess$")` with the role env vars and
  **without** `GO_TEST_HELPER_PROCESS`. `TestMain` falls to `m.Run()`, the `-test.run` filter admits
  exactly one test, that test plays its role and never returns to the framework. Bounded by
  construction. This is `CODING-STYLE.md` § Testing's documented `TestHelperProcess` pattern.
- `TestTdnReapTreeHelperProcess` returns immediately when its env gate is unset, so it is a no-op
  PASS under `-run '^TestTdn'` and under a plain package run. It must not be a `t.Skip` — the
  package's zero-SKIP property is load-bearing (#1250).
- **The leaves need no new role at all.** They are started with
  `GO_TEST_HELPER_PROCESS=1` + `PYRY_E2E_FAKE_MODE=sleep`, which routes through the package's
  existing, tested `runFakePyry` (`fixtures_test.go:366`) and blocks 30 s. A leaf needs no logic:
  whether it leads a fresh group is decided by *its spawner's* `SysProcAttr`, not by the leaf.
  Leaves spawn nothing, so the recursion depth is 2 and terminal.

### Contracts

```go
// TestTdnReapTreeHelperProcess plays a reap-tree role when TDN_REAP_TREE_ROLE
// is set, and returns immediately otherwise. It never returns to the test
// framework when it plays a role.
func TestTdnReapTreeHelperProcess(t *testing.T)

// tdnTree is one live process tree the test owns. RootPID is what the walk is
// rooted at; Fresh are the group leaders the reaper must kill; Same are the
// descendants sharing RootPID's group, which reap.go:52 must spare.
type tdnTree struct {
    RootPID int
    Fresh   []int
    Same    []int
}

// tdnStartTree starts a parent with `fresh` fresh-group leaves and `same`
// same-group leaves, blocks until every child pid is reported, and registers
// cleanups that SIGKILL each group and pid.
func tdnStartTree(t *testing.T, fresh, same int) tdnTree

// tdnStartLeaf starts one childless blocking process in its own group. Used as
// the walk root for the nothing-to-reap arm.
func tdnStartLeaf(t *testing.T) int

// tdnKillTree SIGKILLs a group and then a pid, refusing any pid the caller
// could not have owned. Every kill in this file goes through it.
func tdnKillTree(pid int)
```

The `parent` role: read `TDN_REAP_TREE_FRESH` / `TDN_REAP_TREE_SAME` counts, start that many
leaves (`Setpgid: true` for fresh, unset for same), **start a background `Wait` per leaf**, write
one `<kind> <pid>` line per child to `TDN_REAP_TREE_REPORT`, then block.

### The report file is a trust boundary — treat it as one

Every pid in that file becomes a `syscall.Kill` target and one becomes a walk root. The path is
under `t.TempDir()` (fresh, `0700`, per-test, removed on exit) and the writer is our own helper, so
the realistic threat is a truncated or crashed write rather than a hostile one — but the
consequence of a bad value is not proportionate to how unlikely it is:

- **`syscall.Kill(-0, SIGKILL)` signals the caller's own process group.** `-0 == 0`, and
  `kill(0, sig)` delivers to every process in the caller's group — the test binary, `go test`, and
  whatever shares that group. A single `0` line, or a pid field that fails to parse into a
  variable left at its zero value, reaches it. So: `tdnKillTree` must return early unless
  `pid > 1`, and every cleanup and every helper must kill through it. `reap_test.go:169` already
  guards `pid > 0` at the parse; guard at the parse **and** at the kill site, because the kill site
  is where the damage is.
- **The parse is all-or-nothing.** The file must hold exactly `fresh+same` well-formed
  `<kind> <pid>` lines before it is accepted. `waitReport` (`reap_test.go:164`) reads one pid and
  can treat any partial read as "not ready yet"; with N lines a short read is *syntactically valid
  but incomplete*, and accepting it yields a `tdnTree` with a missing `Fresh` entry — an index
  panic at best, a subtest silently asserting about the wrong process at worst. Keep polling until
  the count matches; `t.Fatalf` naming the path, the expected count and the timeout if it never
  does.
- **Reject a reported pid equal to `os.Getpid()` or to the test's own pgid** (`syscall.Getpgrp()`)
  before it is used or killed. No correct helper can report either; a run that does is broken in a
  way that must not proceed to a kill.
- The helper writes the report at `0o600` in one shot, mirroring `reap_test.go:264`.

Four details that are not optional:

1. **The background `Wait` per child is the zombie guard.** `ps` lists zombies and
   `kill(pid, 0)` succeeds on one. Without the `Wait`, a SIGKILLed leaf lingers in the process
   table with its pgid intact — which would make the reaped-group check pass on a corpse and, worse
   for AC3, make a *killed* spared leaf read as "still alive." `reap_test.go:117-119` and
   `:239-244` record this in its own package. Mirror it, and mirror it in the test for the parent
   too (`go func() { _ = cmd.Wait() }()`).
2. **Report before blocking, and have the test block on the report.** Every child pid must be in
   the process table before `ReapDescendantGroups` takes its `ps` snapshot. Poll the report file to
   a bounded deadline, exactly as `waitReport` (`reap_test.go:164`) does — not an unbounded pipe
   read.
3. **`os/exec` dedups env keeping the last occurrence.** Append the role vars *after*
   `os.Environ()`, so an operator's pre-set `PYRY_E2E_FAKE_MODE` or `TDN_REAP_TREE_*` cannot
   redirect a child into a different role. See `reap_test.go:252-254`. The children inherit the
   operator's full environment — including `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` — which
   matches the in-repo precedent (`reap_test.go:124`) and is safe only because every role in this
   family prints nothing but pids and never reaches `runFakePyry`'s `argv` mode. Do not add a role
   that echoes its environment or its argv.
4. **Register cleanups so children die before the parent.** `t.Cleanup` runs LIFO: register the
   parent's cleanup at `Start`, then each child's after the report arrives, and the children are
   killed first. A parent killed first orphans its leaves to init, where a fresh-group leaf outlives
   the test until its own 30 s backstop. `reap_test.go:141-157` is the pattern.

---

## § 4 — Capturing the bytes

`runAgentRunPty` sets no `Logger` on `ptyrunner.Config` (`cmd/pyry/agent_run.go:316-329`), so
`ptyrunner.Run` falls back to `slog.Default()` (`runner.go:289-291`) and no non-test code calls
`slog.SetDefault`. Go's built-in default `slog` handler writes through the `log` package, so
`log.SetOutput` captures it.

**Re-verified against this repo's toolchain, 2026-07-31** (independent of the ticket body's claim —
#1250's own § Patterns established records that a cited rationale can be wrong even when the design
built on it is right):

```
2026/07/31 01:21:30 INFO agentrun: reaped claude descendant process groups count=1 pgids=[7788]
2026/07/31 01:21:30 INFO agentrun: reaped claude descendant process groups count=2 pgids="[7788 9900]"
```

`pgids=[7788]` present and `pgids="[7788` absent in the one-group rendering; `pgids="[7788 9900]"`
present in the two-group one. Both confirmed by byte-level `Contains`.

```go
// tdnCaptureReap runs a real reap rooted at rootPID and returns the bytes
// slog.Default() emitted while it ran. It fatals if rootPID is the test
// process — see below.
func tdnCaptureReap(t *testing.T, rootPID int) []byte
```

Behaviour: save `log.Writer()`, `log.SetOutput(&buf)`, call
`agentrun.ReapDescendantGroups(rootPID, slog.Default())`, restore in a `defer`, return
`buf.Bytes()`. `internal/e2e/realclaude` already imports `internal/agentrun/...` subpackages
(`ptyrunner_byte_equivalence_test.go:20-23`), so importing `internal/agentrun` itself needs no new
boundary.

Four properties to hold:

- **The blast-radius guard is code, not a comment.** `tdnCaptureReap` must `t.Fatalf` when
  `rootPID == os.Getpid()` or `rootPID <= 1`. A reap rooted at the test process sweeps every
  fresh-group descendant of the whole binary, and in this package sibling specs spawn real
  `pyry agent-run` and `claude`. The ticket states the rule in prose; this makes it enforced.
- **`log.SetOutput` is process-global.** Restore it unconditionally via `defer`. **No `t.Parallel`
  anywhere in this file**, and say so in the file header. Pollution from a concurrently-logging
  sibling does not corrupt the *answer* — the classifier anchors on `tdnReapMessage` and ignores
  every other line, and nothing else in the process emits that message — but it does mean the
  buffer can hold text this test never produced, which is why § 6 forbids printing it. The redirect
  is also not reentrant: two concurrent captures would lose one another's bytes.
- **Pass `slog.Default()` explicitly**, mirroring what `ptyrunner` hands the reaper. Do not
  construct a handler; constructing one would prove nothing about the live path.
- **Capture per call.** A fresh buffer per `tdnCaptureReap` call, so the silent arm cannot inherit
  the positive arm's bytes.

---

## § 5 — The tests

Three tree shapes, one table. Subtests run sequentially and share outer-scope byte slices so the
rendering-difference assertion (AC2) can compare captures from two different real reaps.

### `TestTdnRealReapCapture`

**Subtest A — one group killed, a same-group sibling spared** (`tdnStartTree(t, 1, 1)`).
Covers AC1's positive half, AC2's single-group rendering, and all of AC3.

- Capture a real reap rooted at `tree.RootPID`; keep the bytes in the outer scope as the
  single-group rendering.
- `tdnClassifyReapLog(bytes, tree.Fresh[0])` → `tdnReapHeldPGIDKilled`, with `PGIDs` containing
  `tree.Fresh[0]`, `LineCount == 1`, `Count == 1`. No constant anywhere in the path.
- `tdnGroupGone(tree.Fresh[0], 2*time.Second)` → the reaper really killed it, not merely logged it.
- **AC3's pairing, asserted in this order:** immediately after the capture, assert
  `tdnAliveThroughout(t, tree.Same[0], 250*time.Millisecond)`; then
  `tdnClassifyReapLog(bytes, tree.RootPID)` → `tdnReapHeldPGIDAbsent`. The spared leaf's pgid *is*
  `RootPID` (it never called `setpgid`, and the parent leads its own group), so the verdict is
  about exactly the group whose member was just proven alive. That is the unearned negative this
  ticket exists to refuse: absent-from-the-line and "it had already exited" are now
  distinguishable.
- **AC2's single-group rendering, on the bytes:** `pgids=[<fresh>]` present **and** `pgids="[`
  absent.
- Assert `tree.RootPID` is *not* in `PGIDs` — the exclusion is structural.

Why `tdnAliveThroughout` rather than a bare `kill(pid, 0)`: `kill(pid, 0)` succeeds on a zombie, so
a spared leaf that had in fact been SIGKILLed could read as alive inside the window before the
parent's background `Wait` reaps it. The reaper's `syscall.Kill` returns before
`ReapDescendantGroups` does, so any kill has already been *delivered* by capture time; a bounded
settle in which the pid stays live is therefore sufficient, and 250 ms is generous against the
`Wait`. Assert continuously across the window, not just at both ends.

**Subtest B — nothing to reap emits no line** (`tdnStartLeaf(t)` as the walk root).
AC1's silent half, and the half that proves the capture flips.

- Capture a real reap rooted at the childless leaf. `reap.go:64` guards the emit on
  `len(reaped) > 0`, so no line is written at all.
- `tdnClassifyReapLog(bytes, <the leaf's pid>)` → `tdnReapNoLine`, `LineCount == 0`.
- Assert the captured bytes do **not** contain `tdnReapMessage`.
- Assert the leaf is still alive: a no-descendant reap must kill nothing.

Use a separate childless root rather than re-reaping subtest A's tree. A second reap of A's parent
would race the leaf's zombie window — `ps` lists zombies, so a not-yet-reaped corpse's pgid would
still enumerate and the arm would be timing-dependent. A fresh leaf is deterministic and costs four
lines.

**What the pair proves.** The positive alone rules out a capture wired to a stream the reaper never
writes (it would be empty and A would fail). The silent arm rules out the dual — a capture that
carries the line unconditionally (a stale buffer, a leaked fixture). Together they are the flip:
same helper, same capture path, same process, opposite answers.

**Subtest C — several groups killed are rendered as a quoted list** (`tdnStartTree(t, 2, 0)`).
AC2's multi-group half. No existing fixture reaps more than one group; this tree shape is new.

- Capture; keep the bytes in the outer scope as the multi-group rendering.
- `tdnClassifyReapLog` with `held = tree.Fresh[0]` and again with `held = tree.Fresh[1]`, both
  → `tdnReapHeldPGIDKilled`; `LineCount == 1`, `Count == 2`, and `PGIDs` is the two-element set.
- **Assert membership as a set, never as an ordered slice or an exact rendered string.** `reaped`
  is built by ranging a map in `reap.go:51`, so the order of `pgids=[A B]` is not deterministic.
- Both groups gone (`tdnGroupGone` on each).

**Subtest D — the two renderings differ** (no new tree; asserts over the bytes A and C captured).

- Single-group bytes: contain `pgids=[`, do **not** contain `pgids="[`.
- Multi-group bytes: contain `pgids="[`.
- Fail with a message naming both **classified** lines — `outcome.Line`, never the raw buffer
  (§ 6). The difference itself is the regression surface:
  a matcher proven only on the single-group form inverts to a false negative on a multi-group
  teardown, which is the ordinary case.
- Guard against a silently-skipped predecessor: fatal if either byte slice is empty.

### `TestTdnClassifyReapLogSubstringMembership`

AC4, plus its already-covered twin for legibility. A two-row table over the
`slog.Default()` rendering of `pgids=[7788]`:

- held `77` — **prefix** direction. Already covered by the row at
  `teardown_liveness_test.go:470-481`; carried here so both halves of the hazard read together.
  Name that row's location in the comment.
- held `88` — **suffix** direction. The new coverage. `88` is a substring of `7788` in the other
  direction, and a substring matcher satisfies every other criterion in this ticket while inverting
  its answer here.
- Both → `tdnReapHeldPGIDAbsent` with `PGIDs == []int{7788}`, `LineCount == 1`.

Reuse `tdnEqualInts` and `tdnIsReapVerdict`; do not redeclare them.

---

## § 6 — Redaction, and the prohibition this file inherits

This reader consumes text pyry already wrote. **It must not grow a `ps` invocation of its own**,
and must never reach one through `-E`, `-e` with an environment column, or BSD `eww`: those print
each process's full environment, which on an operator machine means `CLAUDE_CODE_OAUTH_TOKEN` and
`ANTHROPIC_API_KEY`, into artifacts that end up in public issues. `pinStateColumns`
(`process_pin_liveness_test.go:232`) records that prohibition as a constant with an enforcing test;
inherit it, do not weaken it.

Liveness in this file is **signal-zero only** (`syscall.Kill(pid, 0)`, `syscall.Kill(-pgid, 0)`) —
no column set, no `ps`, no argv. `ReapDescendantGroups` runs its own content-blind
`ps -axo pid=,ppid=,pgid=` internally (`reap.go:76`); that is production code under observation,
not a read this file adds.

**Nothing here writes an artifact.** No `writeTdnArtifacts` call, no `tdnRecord`, no file under an
artifact dir. The only bytes that leave the test are `t.Errorf` / `t.Fatalf` messages.

**No failure message may print the raw capture buffer.** `log.SetOutput` redirects a *process-global*
writer, so for the duration of a capture that buffer collects whatever anything else in the test
process logs through the standard logger — not only the reap line. Dumping it into a failure
message would publish that into CI output and, from there, into the issue an operator pastes it
onto. Report `outcome.Line` and `outcome.Detail` instead: both are anchored on `tdnReapMessage` and
both are already capped by `reachCapCommand`. Where a raw-bytes assertion fails (the
quoted/unquoted rendering checks in § 5), report the *predicate and the pgids*, and at most the
single `tdnReapMessage`-bearing line extracted from the buffer — never `string(captured)`. Length
is not the concern; provenance is.

---

## § 7 — Failure modes and how each surfaces

| Failure | How it surfaces |
|---|---|
| `slog.Default()` stops routing through `log` (toolchain change) | Subtest A's capture is empty → `no-reap-line` → A fails loud. This is the drift the ticket exists to make noisy. |
| Someone calls `slog.SetDefault` in the test process | Same as above — A fails. Correct: the live path's handler would have changed too. |
| `reap.go:65`'s message is renamed | `tdnReapMessage` is a string literal, never a reference — every arm goes red rather than silently following the rename. |
| `slog` changes how it quotes a multi-element slice | Subtest D fails on the rendering-difference assertion; C fails on parsing. |
| Helper re-exec reaches `m.Run()` unfiltered | Bounded by `-test.run=^TestTdnReapTreeHelperProcess$` plus the env gate; the leaf path routes through the existing `GO_TEST_HELPER_PROCESS` branch and spawns nothing. |
| Report never written, or short (helper crashed mid-write) | Bounded poll requiring exactly `fresh+same` well-formed lines → `t.Fatalf` naming the path, the expected count and the timeout. Never a partially-populated `tdnTree`. |
| A `0` or unparsed pid reaches a kill site | `tdnKillTree` refuses `pid <= 1`, so `kill(-0)` — which would signal the test's own process group — is unreachable. |
| A leaf leaks past the test | Each is a 30 s self-terminating `sleep` mode **and** covered by `t.Cleanup` SIGKILL of both group and pid. |
| A spared leaf is killed but still a zombie at read time | `tdnAliveThroughout`'s bounded settle plus the parent's background `Wait` — the corpse is reaped and `kill(pid, 0)` starts failing inside the window. |
| Test process reaped by its own walk | `tdnCaptureReap` fatals when `rootPID == os.Getpid()`. |

---

## § 8 — Verification

```bash
gofmt -l internal/e2e/realclaude/teardown_reap_capture_test.go   # must print nothing
go vet -tags e2e_realclaude ./internal/e2e/realclaude/
go test -tags e2e_realclaude -run '^TestTdn' -v ./internal/e2e/realclaude/   # zero SKIP
go test -race -tags e2e_realclaude -run '^TestTdnRealReapCapture' -count=3 ./internal/e2e/realclaude/
```

- **Zero SKIP** across `^TestTdn` — #1250 ships 22 subtests with none, and this ticket must not
  introduce the first. `TestTdnReapTreeHelperProcess` returns (a no-op PASS), it does not skip.
- `-count=3` is the flake check: three tree builds, three real reaps, three captures.
- **Baseline noise, not findings about this diff:** `gofmt -l` is dirty on `main` for three
  unrelated files in this package — filter to the new file before reporting anything.
- **Prove the capture is the capture, once, by hand.** Before finishing, mutate `tdnCaptureReap` to
  write to a throwaway buffer it never returns and confirm subtest A fails with `no-reap-line`;
  then restore and confirm `git status --porcelain` is clean. This is #1250's own deliberate-mutation
  discipline, and it is the only way to know the positive arm is not passing for the wrong reason.
- **Do not run `make e2e-realclaude`** for this ticket. It spends real credentials and its ~40 live
  specs will SKIP without them; the offline `-run '^TestTdn'` prefix suite is the whole gate here.

---

## § 9 — Budget

One new file. Projected **~430–500 lines** at this package's comment density (~0.45
comment-to-code). Roughly: file header ~55, helper role + tree builder + report poll ~150, capture
+ liveness helpers ~55, `TestTdnRealReapCapture` ~150, `TestTdnClassifyReapLogSubstringMembership`
~30.

Zero production files, zero consumer call sites, zero new exported identifiers, zero state-machine
reject branches. #1250's spec for the sibling file under-called its landing size by 1.65×; if that
repeats here the file lands near 700 lines, still one file and still additive. **If it starts
running past that, the thing to cut is subtest C's separate tree — fold AC2's multi-group case onto
a `tdnStartTree(t, 2, 1)` shape shared with A — not the mutation check in § 8 and not AC3's
aliveness pairing.**

---

## § 10 — Open questions

1. **Does `-test.run` filtering plus the existing `TestMain` behave as reasoned on the first run?**
   The analysis is sound (`TestMain` falls to `m.Run()`, the filter admits one test, that test
   exits the process) and follows `CODING-STYLE.md`'s documented pattern, but it has not been
   executed in this package. Verify it first, before building the tree logic on top: start the
   helper with the role unset and confirm the child exits promptly rather than running the suite.
   If it misbehaves, the fallback is a `PYRY_E2E_FAKE_MODE` case in `runFakePyry` — but that edits
   `fixtures_test.go`, so take it only if the `-test.run` path genuinely fails.
2. **Is 250 ms the right settle for `tdnAliveThroughout`?** Chosen as generous against a
   background `Wait` on a machine under CI load, against a kill already delivered before the
   capture returned. If it proves flaky, raise it — do not delete the assertion or replace it with
   a single-point `kill(pid, 0)`.
3. **Does macOS ever report a fresh-group leaf's pgid as something other than its pid?** Not
   observed; `reap_test.go` has relied on `pgid == pid` for group leaders since #565. The tree
   builder reports pids and the assertions treat them as pgids on that basis. If a platform breaks
   it, the symptom is a clean `reap-line-without-held-pgid` in subtest A — loud, not silent.

---

## Security review

**Verdict:** PASS (after one FAIL round — two MUST FIX items found against the first draft and
folded in before commit; see § 3 "The report file is a trust boundary" and § 6's raw-buffer rule).

**Findings:**

- **[Trust boundaries] MUST FIX — fixed.** The first draft described the report file as a
  mechanism, not a boundary: it said `tdnStartTree` "registers cleanups that SIGKILL each group and
  pid" with no bound on the value. Every pid in that file becomes a `syscall.Kill` target and one
  becomes the walk root. `syscall.Kill(-0, SIGKILL)` is `kill(0, …)`, which signals **the caller's
  own process group** — the test binary, `go test`, and whatever shares that group — and a single
  `0` line or an unparsed field left at its zero value reaches it. § 3 now routes every kill through
  `tdnKillTree`, which refuses `pid <= 1`, requires the parse to be all-or-nothing on
  `fresh+same` well-formed lines, and rejects any reported pid equal to `os.Getpid()` or the test's
  own pgid. The other boundary — captured bytes into `tdnClassifyReapLog` — is a single explicit
  function and unchanged from #1250.
- **[Error messages, logs] MUST FIX — fixed.** The first draft told subtest D to "fail with a
  message naming both captured lines." `log.SetOutput` redirects a *process-global* writer, so the
  buffer can hold anything else the process logged during the window, not only the reap line;
  printing it would publish that into CI output and from there into the issue an operator pastes it
  onto. § 6 now forbids printing the raw buffer at all — failures report `outcome.Line` /
  `outcome.Detail`, both anchored on `tdnReapMessage` and both already capped by `reachCapCommand`,
  or at most the single anchored line extracted from the buffer.
- **[Subprocess execution] No findings.** Arguments to `exec.Command` are fixed literals
  (`os.Args[0]`, `-test.run=^TestTdnReapTreeHelperProcess$`) with no interpolated input; no `sh -c`
  anywhere, and § 3 records that a shell was *rejected* rather than merely unused (no portable
  `setsid`, and a non-interactive shell's background job stays in the shell's group). Double-fork
  escape is bounded: the recursion depth is 2 and the leaf role spawns nothing. Orphaning is
  handled by LIFO cleanup ordering (§ 3 detail 4) plus each leaf's 30 s self-terminating backstop.
- **[Tokens, secrets] No findings, one documented decision.** Nothing is generated, stored, or
  compared. Children inherit the operator's full environment, including
  `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY`. That is deliberate — it matches
  `reap_test.go:124` and the children need `PATH`/`HOME`/`TMPDIR` — and is safe only because no
  role in this family prints its environment or its argv, and role vars are appended *after*
  `os.Environ()` so an operator's pre-set `PYRY_E2E_FAKE_MODE` cannot redirect a child into
  `runFakePyry`'s `argv` mode. § 3 detail 3 states both the inheritance and the rule that keeps it
  safe. A scrubbed minimal environment would be defence-in-depth against a failure mode not
  observed here; not prescribed.
- **[File operations] No findings.** The one path is `t.TempDir()`-rooted (fresh, `0700`,
  per-test, auto-removed) — no user input is concatenated into it, so no traversal surface and no
  symlink surface. The report is written `0o600` in one shot (`reap_test.go:264`); the reader
  tolerates a partial read by re-polling rather than accepting it, which is the TOCTOU-equivalent
  here. Nothing else is written: no artifact, no `tdnRecord`, no second file.
- **[Cryptographic primitives] Not applicable.** No randomness is security-relevant. The one
  nondeterminism present — `reap.go:51` ranges a map, so `pgids=[A B]` ordering is unstable — is
  addressed as a correctness constraint in § 5 subtest C (assert membership as a set, never as an
  ordered slice or an exact rendered string), not papered over with a seeded RNG.
- **[Network & I/O] Not applicable.** No sockets, no listeners, no HTTP. The capture buffer is fed
  by exactly one in-process reap call and bounded by that line's pgid list; `reachCapCommand`
  already caps everything reaching a `Detail`. Tree sizes are literals (1–2), not derived from
  input, so there is no attacker-influenced fan-out.
- **[Concurrency] No findings.** No locks, so no ordering to document. The one piece of shared
  mutable state is the process-global `log` writer: restored unconditionally via `defer`, with no
  `t.Parallel` anywhere in the file and the non-reentrancy stated in § 4. Every goroutine the spec
  spawns is one `cmd.Wait()` per child, exiting when that child dies — bounded by the 30 s backstop
  and by cleanup. Nothing persistent is written, so there is no partial state to recover from a
  mid-write signal.
- **[Threat model alignment] No findings.** The operative threat for `internal/e2e/realclaude` is
  operator credentials reaching a public issue through an artifact or a CI log; it is addressed by
  § 6 in three independent ways (no `ps` column set of its own — `pinStateColumns`' prohibition
  inherited verbatim; no artifact written at all; no raw buffer in failure output). The relay threat
  model in `docs/protocol-mobile.md` § Security model is not applicable — this ticket adds no
  network surface. The reaper's own blast radius is the remaining hazard and is held by a code
  guard, not a comment: `tdnCaptureReap` fatals when `rootPID == os.Getpid()` or `rootPID <= 1`.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-31
