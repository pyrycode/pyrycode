# #1230 — Is a backgrounded Bash command inside the reaper's descendant-BFS reach?

**Size:** S — 1 new file (`internal/e2e/realclaude/background_reach_probe_test.go`),
**0 production files**, 0 consumer call sites, ~590 LOC projected. The staging half
(spawn pyry, hold the FIFO, poll the session JSONL) is already on `main` from #1223
and is reused verbatim; this ticket adds only the measurement, the record, and the
pure self-checks.

**No production change.** This is a probe. Per the project's evidence-based-fix rule,
no defence is built for a failure mode nobody has observed — the deliverable is the
observation.

---

## Files to read first

Read these before writing anything. Together they are the whole data load; nothing
below requires a repo-wide search.

**Production — the thing being measured:**

- `internal/agentrun/reap.go:35-67` — `ReapDescendantGroups`. The three exclusions
  are the loop at `:52` (`pgid <= 1 || pgid == self || pgid == rootPid`) and
  `self := syscall.Getpgrp()` at `:49`. **This is the arithmetic AC3 asks you to
  reproduce with real integers.**
- `internal/agentrun/reap.go:45-48` — the comment claiming claude "is its session/group
  leader, so pgid == pid". AC3's second half asks whether that in fact holds. It is a
  claim, not a measurement — treat it as the hypothesis under test.
- `internal/agentrun/reap.go:75-116` — `descendantPGIDs`: the exact parse
  (`strings.Fields`, `len(fields) != 3`, three `Atoi`s) and the exact BFS
  (`children[ppid]` map, `seen` cycle guard, queue seeded from `children[rootPid]`).
  AC2's "same parse and walk as production" means this, literally.
- `internal/agentrun/ptyrunner/runner.go:314`, `:398`, `:499` — the three reap call
  sites. All three pass `cmd.Process.Pid`. **`rootPid` is the pid of the process pyry
  itself spawned as claude** — that is the pid AC2's root must be pinned to.
- `internal/agentrun/ptyrunner/runner.go:616-625` — `buildArgs`. claude's argv on the
  ptyrunner path is
  `<bin> --session-id <uuid> --settings <path> --permission-mode dontAsk --append-system-prompt-file <path> --model <m> --effort <e>`.
  Two things fall out: the run's session UUID is in claude's argv (that is the content
  evidence for the root — see § Root pinning), and `--append-system-prompt-file` is the
  ptyrunner-shape marker.

**The #1223 rig — reuse, do not rebuild (and do not edit):**

- `internal/e2e/realclaude/background_trigger_probe_test.go:405-570` — `runProbeRep`.
  The staging skeleton to mirror: cleanup registration order, the `pyryExited` channel,
  the rendezvous select, the classification point at `:529`. Read this whole function
  before writing yours.
- `:621-645` — `spawnProbePyry`. Note `SysProcAttr{Setpgid: true}` at `:640`: **the
  probe's pyry is its own process-group leader, so its pgid == its pid.** AC3 depends
  on knowing this (see § The `self` integer).
- `:663-720` — `holdProbeFIFO`. The write end never leaves the helper; the only release
  is its own `t.Cleanup`. This is why the snapshot has no timing race.
- `:746-757` `probeWaitForSessionID` · `:762-788` `probeWaitForBashToolUse` ·
  `:793-816` `probeWaitForToolResult` · `:827-852` `probeToolUseInput` — the JSONL
  polls and the tool-input projection.
- `:857-925` — `probeSnapshot` / `probeProcessSnapshot` / `probeDescendantsFromPS`.
  `probeDescendantsFromPS` is already a byte-pure mirror of production's
  `descendantPGIDs` and is already self-checked. **It is AC2's down-BFS read; you write
  no new code for that half.**
- `:930-960` — `probeAnnotateCommands`. Read it to understand what this ticket exists to
  fix: `filepath.Base(fields[1])` at `:954` drops the argv tail holding the FIFO path,
  and its `ps` at `:940` is narrow (`-p <pids it already found>`). Two independent
  reasons it cannot answer "where is the process holding *this* FIFO".
- `:975-988` — `probeWaitForDirectChild`. Returns pyry's **first direct child** by
  position, with no content check. AC2 names this as the root's weak point.
- `:1074-1102` — `writeProbeArtifacts`. The artifact-writing shape to mirror at ⅓ the
  size.
- `:1181-1242` — `TestProbeDescendantsFromPS_SyntheticTree`. The self-check shape (a
  raw-string synthetic snapshot, table-driven) your two self-checks mirror.

**Package fixtures (call, don't re-derive):**

- `internal/e2e/realclaude/fixtures.go:96` `WithWorktreeAuthenticated` (skips without
  credentials; returns a workdir that IS the pinned HOME) · `:325` `ensurePyryBuilt`
- `internal/e2e/realclaude/resilience_test.go:282` `resolveClaudeBin`
- `internal/e2e/realclaude/prompt_fidelity_test.go:78` `jsonlPathFor`
- `internal/e2e/realclaude/per_agent_test.go:138` `truncate`

**Docs:**

- `docs/knowledge/codebase/1223.md` — the settled trigger recipe (`BASH_DEFAULT_TIMEOUT_MS=5000`,
  7/7), the `toolUseResult.timedOutAfterMs` discriminator, and § Lessons learned's two
  **shipped-unfixed** SHOULD FIX items. The first one (`probeWaitForBashToolUse` returns
  the *first* Bash `tool_use` regardless of command) bites this ticket directly — see
  § Guarding the tool_use.
- `CODING-STYLE.md` — table-driven tests, stdlib `testing` only, `gofmt`.

---

## Context

`agentrun.ReapDescendantGroups` is pyry's defence against claude leaking Bash
subprocesses. Its entire reach is a BFS from claude's pid over a `ps` ppid→children
map. If a backgrounded command is not a transitive child of claude's pid, the walk
never reaches it, the group is never killed, and the process outlives pyry.

claude now moves a Bash command to the background when its timeout expires. Nobody has
checked whether such a command is inside that BFS reach. **This ticket answers the
predictive half only** — would the walk find it, and would its pgid survive the three
exclusions. One during-turn snapshot, no teardown. The confirmatory half (teardown,
after-exit liveness, disposition) is #1231, which takes its own before/after snapshots
in its own live run so the load-bearing correlation never crosses a ticket boundary.

The answer is runner-independent by construction: `ptyrunner`, `streamrunner` and
`streamsup` all route their teardown reap through the identical
`agentrun.ReapDescendantGroups` behind a `reapDescendantGroupsFn` seam. One walk, one
answer, all three surfaces.

---

## Design

### What this ticket adds

One new file, `internal/e2e/realclaude/background_reach_probe_test.go`, build-tagged
`e2e_realclaude`, gated on `PYRY_PROBE_BACKGROUND_REACH=1`, holding one live test
(`TestRealClaude_BackgroundReachability`) plus two credential-free self-checks.

**One row, one rep.** The trigger is settled (`BASH_DEFAULT_TIMEOUT_MS=5000`, #1223
7/7) — do not re-derive it. There is no lever matrix, no negative control, no
env-arrival read, no workdir listing. Those were #1223's job and #1223 did them.

### What this ticket must NOT build

Listed explicitly because each is a plausible-looking re-derivation that would blow the
budget:

- **No edits to `background_trigger_probe_test.go`.** Read-only reuse. #1231 is a
  sibling ticket that will want the same helpers; two children editing the shared rig
  is a merge conflict for zero benefit. Same reasoning as #1223's own deliberate
  duplication of `sigterm_mid_tool_use_test.go` helpers.
- **Namespace every new symbol `reach*`.** `feature/1219` is in flight in this same
  package and #1231 lands next. A `probe`-prefixed name risks a same-package redeclare
  at merge.
- **No new FIFO-hold, sync-buffer, spawn, or JSONL-poll implementation.** Call
  #1223's.
- **No production change.** Not to `reap.go`, not to either runner.
- **No `docs/knowledge/codebase/1230.md`.** The documentation phase owns that file and
  writes it after the PR merges.

### The measurement — one ordered block at the classification point

Everything below happens at #1223's classification point (`:529`): the matching
`tool_result` has landed, the FIFO write end is **still held**, and pyry is still
running. Order matters; do it exactly like this:

1. **Integer snapshot.** `probeProcessSnapshot(pyryPID)` → its `raw` field is the
   verbatim `ps -axo pid=,ppid=,pgid=` bytes. This is the load-bearing snapshot: every
   number in AC2 and AC3 comes out of these bytes, and it is the one pasted into the
   comment.
2. **Argv snapshot.** `reachScanArgv(needles)` — a second, immediately-following `ps`
   that reads command lines. It returns **matched rows only**; the raw table never
   leaves the wrapper (§ Redaction).
3. **During-turn evidence.** Non-blocking read of the `pyryExited` channel, exactly as
   `:534-542` does. Record it. If pyry had already returned, the run is inconclusive
   and no reachability verdict is stated.
4. **Skew cross-check.** Every pid matched in (2) must be present in (1)'s parse. A pid
   in the argv table but absent from the integer table is an **instrument fault**
   (two-snapshot skew), recorded as such — never as "not reachable".

   Two snapshots means a check-then-use window, so state what bounds it: the held
   process cannot exit during it (the FIFO write end is held), and only pids matched by
   a run-unique needle are ever used — so a pid recycled between the two `ps` calls
   cannot be mistaken for a matched one. That is the argument; the cross-check is the
   net under it.

### Content-first identification (AC1)

Two needles, one matcher, both searched against **full command lines across the whole
process table** — never against a base name, never restricted to a subtree:

- **The held process:** the run's unique FIFO path (`rec.FIFOPath`).
- **The root:** the run's session UUID (§ Root pinning).

`ps` must be invoked with `-ww`. Without it, macOS truncates the command column to the
terminal width and the argv tail holding the FIFO path is silently lost — reproducing
the exact defect (`probeAnnotateCommands`'s `filepath.Base`) this ticket exists to
avoid, but with no visible symptom.

**Expect more than one match for the FIFO needle.** #1223's observed tree is
`pyry → claude → zsh → cat`; both the `zsh -c` wrapper and the `cat` carry the FIFO
path in their argv. That is correct and AC1 sanctions it ("one or more rows"). Run the
hop-chain walk for **every** matched row and the exclusion arithmetic for **every
distinct pgid** across them. Do not silently pick one.

**The three-valued match outcome**, recorded as one field, never collapsed:

| Value | Condition | What the record may then say |
|---|---|---|
| `matched` | background handle came back **and** ≥1 argv row matched the FIFO needle | proceed to the reachability verdict |
| `trigger-never-fired` | no background handle in the `tool_result` | **inconclusive, re-run.** No reachability claim. |
| `fired-no-row-matched` | handle came back, zero argv rows matched | a finding in its own right: backgrounded and gone from the table |

Order is load-bearing: **establish the handle first, then look at the table.** A zero-row
match must never be recorded as "not reachable from claude's pid" — that reports the
alarming branch from an instrument that did not run. #1223's 7-of-7 firing rate is
evidence, not a guarantee for this run.

"The background handle came back" is read from the `tool_result` envelope's
`toolUseResult.backgroundTaskId` (present) — with `toolUseResult.timedOutAfterMs`
recorded alongside it as the expiry-path corroboration (`5000` on the expiry path,
absent when the model asked for backgrounding — settled by #1223 AC4). Do **not** key
off `input.timeout`: #1223 observed it absent 10/10, the client default backgrounds with
nothing in the request params.

### Guarding the tool_use

`probeWaitForBashToolUse` returns the **first** `Bash` `tool_use` regardless of
`input.command` — a known, deliberately-shipped gap in #1223 (its § Lessons learned).
If the model issues any other Bash call before the FIFO `cat`, you would key off the
wrong envelope.

Fix cheaply and content-first, consistent with the rest of this ticket: call it as-is,
then project its input with `probeToolUseInput` and assert
`strings.Contains(<input.command>, fifoPath)`. If it does not contain the FIFO path,
record `trigger-never-fired` with a detail naming the mismatch and stop — inconclusive,
re-run. Do not modify `probeWaitForBashToolUse` (§ What this ticket must NOT build).

**Record the boolean, not the JSON.** `probeToolUseInput` returns the model's verbatim
Bash params. Those are model-controlled bytes and this ticket has no use for them —
#1223 already published them. Carry `tool_use_command_matched_fifo: true|false` into
the record and drop the `json.RawMessage` on the floor.

### Root pinning — content-first (AC2)

Both reachability reads are rooted at claude's pid, so **a mis-identified root makes
them agree and both be wrong.** Agreement does not validate the root. Three signals,
all recorded:

1. **Positional (the #1223 rig's answer):** `probeWaitForDirectChild(pyryPID, …)` —
   pyry's first direct child, no content check.
2. **Content (this ticket's answer):** the argv row whose command line contains the run's
   **session UUID**. pyry passes `--session-id <uuid>` in claude's argv
   (`ptyrunner/runner.go:618`), the UUID is unique on the machine, and it survives
   shebang rewriting — matching on the resolved `claudeBin` path does not, because the
   `claude` CLI may execute as `node …/cli.js`.
3. **Agreement:** whether (1) and (2) name the same pid.

If (2) yields no row, or yields a pid that disagrees with (1), **the record says so and
stops** — no arithmetic that would be relative to the wrong process. That is a stated
outcome, not a gap.

Corroboration worth one boolean: whether the matched claude row's argv contains
`--append-system-prompt-file`, the ptyrunner-shape marker from `buildArgs`. This
evidences the runner path from the process table rather than from the env the test set
— #1223 learned the hard way that a routing assumption can change underneath a probe.

### Reachability — two reads, one set of bytes (AC2)

Both reads run over the **same** integer snapshot from step (1):

- **Up:** walk the ppid chain from each held pid toward claude's pid, collecting every
  hop as `{pid, ppid, pgid}`. Terminates on reaching claude's pid (reachable), on
  reaching pid ≤ 1 (not reachable), on a pid absent from the table (not reachable), or
  on revisiting a pid (cycle guard, mirroring production's `seen`).
- **Down:** `probeDescendantsFromPS(rawIntegerSnapshot, claudePID)` — the existing,
  already-self-checked byte-pure mirror of production's `descendantPGIDs`.

**The agreement condition is stronger than membership:** the held pid must appear in the
down-BFS result *and* every hop in the up-chain must appear in it too. A disagreement is
an **instrument fault to be fixed, not a finding to be reported** — the record says
"instrument fault" and states no reachability verdict.

### Exclusion arithmetic (AC3)

For each distinct pgid across the matched rows, evaluate `reap.go:52`'s three exclusions
and record **the actual integers each comparison is made against**: the held pgid, the
reaper's own pgid, claude's pid, claude's pgid.

**The `self` integer.** `syscall.Getpgrp()` is read *in the process that calls
`ReapDescendantGroups`* — the `pyry agent-run` process, not the test binary that spawned
it. Reading the test's own group would silently compare against the wrong number. Get
it from the integer snapshot: the row where `pid == pyryPID` carries pyry's pgid.

Record one caveat alongside it, because it bounds what the reading generalises to:
`spawnProbePyry` sets `Setpgid: true` (`:640`), so under this probe pyry is its own
group leader and its pgid == its pid. An operator-launched pyry inherits its shell's
job-control group instead. The verdict is unaffected only if the held pgid differs from
**both** candidate values — which the recorded integers show explicitly. State it; do
not hand-wave it.

**Also record whether `pgid == pid` holds for claude.** `reap.go:52` compares
`pgid == rootPid`, i.e. claude's *pid*, resting on the `reap.go:45-47` claim that claude
is its own group leader. On the ptyrunner path claude is spawned into a PTY (tui-driver
→ `pty.Start`, which sets `Setsid`), which would make it a session leader; on a non-PTY
path a child with no `Setpgid` inherits its parent's group instead. If `pgid != pid` for
claude, **the exclusion does not exclude what it believes it does, and that is a
finding** — record it as one.

### Disposition (AC5)

The verdict is a single recorded string plus its detail. Four arms:

- **not reachable from claude's pid** → the BFS cannot find it and the reaper cannot
  kill it. The developer files a follow-up issue naming the held pid, its pgid, and the
  reachability gap.
- **reachable and survives all three exclusions** → the record says so *and states
  plainly* that this establishes only that the reaper **would target** the group.
  Whether it actually dies, and by whose hand, is #1231's to answer and **must not be
  claimed here.**
- **inconclusive, re-run** → `trigger-never-fired`, snapshot-not-during-turn, root
  disagreement, or instrument fault.
- **backgrounded process left the table** → `fired-no-row-matched`.

Never a reachability verdict from either non-matched arm of AC1.

---

## Interfaces

Contracts only. Every one of these is new, `reach`-prefixed, and file-local.

```go
// Pure over its bytes; self-checked. Returns rows whose full command line
// contains any needle, plus the total row count scanned. Never returns the table.
// Each match's Command is capped at reachMaxCommandBytes with a truncation marker.
func reachMatchArgvRows(table []byte, needles []string) (matches []reachProc, total int)

// Impure wrapper: execs `ps -axww -o pid=,ppid=,pgid=,command=` under a 5 s context,
// hands the bytes to reachMatchArgvRows, discards them. The raw table never leaves
// this frame. NEVER -E / eww — argv only, never envp (§ Redaction rule 1).
func reachScanArgv(needles []string) (matches []reachProc, total int, err error)

// Pure; self-checked. Parses the integer snapshot into a pid→proc index using the
// same Fields/len==3/Atoi discipline as descendantPGIDs.
func reachIndexFromPS(snapshot []byte) map[int]reachProc

// Pure; self-checked. Walks ppid links from `from` toward `root`. Returns the hop
// chain (from → … → root) and whether root was reached. Cycle-guarded.
func reachChainUp(index map[int]reachProc, from, root int) (hops []reachProc, reached bool)

// Pure. Evaluates reap.go:52's three exclusions. reasons names each one that fires.
func reachExclusionVerdict(heldPGID, pyryPGID, claudePID int) (survives bool, reasons []string)

// Narrow projection of the tool_result envelope's toolUseResult.
func reachBackgroundHandle(raw []byte) (backgroundTaskID, timedOutAfterMs string, present bool)
```

`reachProc` mirrors `probeProc` with a `Command` field that is populated **only** for
matched rows.

**`reachRecord`** — one JSON-marshalled struct, written once. Fields: ticket, claude
version, runner path (env-derived *and* argv-derived), session id, pyry pid + pgid,
claude pid (positional, content, agreement), the background-handle triple,
`tool_use_command_matched_fifo`, the AC1 three-valued match outcome, the matched rows
and the total rows scanned, the per-row hop chains, the down-BFS membership result, the
up/down agreement boolean, the per-pgid exclusion arithmetic with all four integers,
`snapshot_taken_during_turn`, the argv/integer skew result, the disposition verdict +
detail, and free-form notes.

It carries **no** raw process table, **no** environment read, **no** model-authored
JSON, and **no** pyry stream capture.

---

## Concurrency model

Unchanged from #1223 and deliberately so: one FIFO-hold goroutine (parked in
`open(…, O_WRONLY)`, released only by its own `t.Cleanup`), one `cmd.Wait` goroutine
closing `pyryExited`, and the test goroutine. No new goroutines, no shared mutable
state, no locks. Cleanup registration order is load-bearing and must match `:436-455`:
artifact-writer first (runs last, so the record is complete), then the pyry-group
backstop, then `holdProbeFIFO`.

**Carry `:443`'s `if rec.PyryPID <= 0 { return }` guard into the backstop cleanup
verbatim.** You are writing a fresh rep body, and this is the line most likely to be
lost in the copy. Without it, a rep that fails before `cmd.Start` succeeds reaches
`syscall.Kill(-0, syscall.SIGKILL)` — and `kill(0, sig)` is defined as *"send to every
process in the caller's own process group"*, i.e. the test binary SIGKILLs itself and
its siblings. It is a one-line omission with a whole-run blast radius.

Liveness of pyry is read from the `pyryExited` channel, never `Signal(0)` — a zombie
answers `Signal(0)` with nil and would falsify the during-turn claim (#1223's § Lessons
learned).

---

## Error handling

Every failure mode below is **recorded, not fatal**. `t.Fatalf` fires only on structural
failure (pyry won't build, `mkfifo` fails, pyry never spawns a child, no `system/init`
session id) — same discipline as #1223.

| Failure | Recorded as | Verdict |
|---|---|---|
| argv `ps` fails / returns nothing | `argv_scan_error` | inconclusive |
| pid matched in argv, absent from integer table | `skew` instrument fault | inconclusive |
| content root missing, or disagrees with positional | root-disagreement | inconclusive, **no arithmetic** |
| up-walk and down-BFS disagree | instrument fault | inconclusive |
| pyry already exited at snapshot time | `snapshot_taken_during_turn: false` | inconclusive |
| no `tool_result`, or no `backgroundTaskId` | `trigger-never-fired` | inconclusive, re-run |
| handle present, zero rows matched | `fired-no-row-matched` | reported as its own finding |

---

## Redaction design

This is the ticket's one design decision worth auditing, and why it carries
`security-sensitive`.

The mechanism introduces a **full-table argv read** — every process's command line on
the operator's machine. Command lines routinely carry tokens and credentials. The
artifacts here get pasted into a public GitHub issue.

The defence is **structural, not a reviewer's judgment call** (the same discipline
`probeLeverVars` established at `:150-158`):

1. **argv only, never envp. This ticket must never invoke `ps -E`, `ps -e` with an
   environment column, or the BSD-syntax `ps eww`.** `ps -o command=` prints argv;
   `ps -E`/`ps eww` prints argv **plus the full environment**. A full-table
   environment read would dump `CLAUDE_CODE_OAUTH_TOKEN` and `ANTHROPIC_API_KEY` —
   which `WithWorktreeAuthenticated` requires to be in the outer environment, and which
   pyry passes to claude verbatim — for every process on the machine, into an artifact
   destined for a public issue. #1223 needed an environment read and paid for it with
   the `probeLeverVars` three-name allowlist (`:150-158`, `:997-1001`). **This ticket
   needs no environment read at all**, so the safe design is to not have the capability.
   One flag is the difference; the prohibition is the control.
2. `reachScanArgv` execs `ps`, immediately hands the bytes to `reachMatchArgvRows`, and
   returns only matched rows. The raw table is confined to that one stack frame — it is
   never assigned to a record field, never written to a file, never logged. The
   wrapper is ~20 lines a reviewer can eyeball in full. Bound it with a 5 s
   `context.WithTimeout`, as `probeProcessSnapshot` does at `:868`.
3. The needles are two run-scoped, run-unique strings the test itself generated: the
   FIFO path and the session UUID. A row matches only by carrying one of them, so an
   unrelated process's argv cannot enter the record by accident.
4. **Needles are matched in Go, in-process — never passed to `ps`, `grep`, or a
   shell.** The FIFO path is a filesystem path; handing it to a `sh -c` filter would
   shell-interpret it. There is no user-controlled value anywhere in the `ps` argv.
5. **Cap each retained command string at 512 bytes**, with an explicit truncation
   marker. A local process can observe the process table too, and could therefore spawn
   a process whose argv carries this run's needle in order to place attacker-chosen text
   into an artifact the operator pastes into a public issue. That requires local code
   execution and a race inside the held window — well past this probe's threat model —
   but the cap costs three lines and bounds the blast radius to something a skim
   catches. Record `total` rows scanned alongside the match count, so an implausible
   match count is visible rather than silent.
6. The **only** `ps` output persisted verbatim is the integer-column snapshot
   (`pid=,ppid=,pgid=`) — three integers per line, no command column. That is what AC4
   requires the comment to carry, and it is what #1223 already published.
7. Artifact files are mode `0600`, in a `MkdirTemp` directory outside the repo. **No
   pyry stdout/stderr artifact file** — unlike #1223, this ticket writes none. The
   streams are used only in `t.Fatalf` diagnostics on structural failure, truncated via
   the existing `truncate` helper.

**Residual, stated rather than hidden:** the retained rows' argv are published. Those are
`cat <fifo>` / `zsh -c cat <fifo>` and pyry's own `claude --session-id … --settings …`
command line — both fully constructed by pyry from the probe's own flags. They disclose
temp paths (the workdir, which is the pinned HOME) and the run's session UUID. #1223
already published both fields (`workdir`, `session_id`). Skim the record before pasting.

---

## Testing strategy

Two credential-free self-checks, table-driven, mirroring
`TestProbeDescendantsFromPS_SyntheticTree`'s shape. Both run without the opt-in gate and
without credentials — a bug in either would turn a clean observation into a false one:

```
go test -tags e2e_realclaude -v -run 'TestReach' ./internal/e2e/realclaude/
```

**`TestReachMatchArgvRows`** — synthetic four-column table, raw string literal:

- a row whose argv **tail** holds the needle matches (the `probeAnnotateCommands`
  defect: a base-name-only match must **not** find it)
- a row whose base name equals the needle but whose argv does not contain it does **not**
  match
- two rows both carrying the needle (`zsh -c cat <fifo>` and `cat <fifo>`) both match —
  the observed real shape
- zero matches returns an empty slice and a non-zero total (distinguishing "scanned
  nothing" from "scanned and found nothing")
- a command line containing spaces is not split — the command column is the rest of the
  line after three integers
- a command line longer than `reachMaxCommandBytes` is truncated with the marker, and
  still matches on a needle that fell inside the cap
- malformed lines (fewer than four fields, non-integer pid) are skipped, not fatal

**`TestReachChainUp`** — synthetic integer snapshot, same shape as `:1185-1193`:

- a three-hop chain `cat → zsh → claude` reports `reached: true` with all three hops
  carrying their pid/ppid/pgid
- a re-parented process (`ppid: 1`) reports `reached: false` — **this is the leak case
  the whole ticket exists to detect**
- a pid absent from the index reports `reached: false` without panicking
- a synthetic ppid cycle terminates (cycle guard)
- `reachIndexFromPS` skips malformed lines, mirroring `descendantPGIDs`

`reachExclusionVerdict` gets no dedicated test: it is three integer comparisons copied
from `reap.go:52`, and the record publishes all four integers so a reader can redo the
arithmetic by eye — which is precisely what AC3 asks for.

**The live gate.** `PYRY_PROBE_BACKGROUND_REACH=1` plus real claude credentials. The
suite **SKIPs** without them and a skip exits 0. This ticket carries
`needs-real-claude`: **it must not close on a green that was actually a skip.** The
per-phase `t.Logf` output is the SKIP≠PASS proof — log the outcome, the disposition, the
four exclusion integers, and the hop chain so a transcript distinguishes a real run from
a skipped one at a glance.

```
PYRY_PROBE_BACKGROUND_REACH=1 BASH_DEFAULT_TIMEOUT_MS=5000 \
  go test -tags e2e_realclaude -timeout 10m -v \
  -run 'TestRealClaude_BackgroundReachability' ./internal/e2e/realclaude/
```

---

## Budget guardrails

Projected ~580 LOC in one file. If it runs long, drop in this order — each is
independently sheddable without touching an AC:

1. The JSON record file → keep only the `t.Logf` summary and the verbatim integer `ps`
   file. (AC4 wants a comment; the JSON is convenience.)
2. `reachExclusionVerdict` → inline the three comparisons in the rep body; the record
   still publishes all four integers.
3. The `--append-system-prompt-file` runner-path corroboration boolean.

Do **not** shed: the content-first identification, the two reachability reads, the
three-valued match outcome, the two self-checks, or the redaction wrapper.

---

## Open questions

- **Does `pgid == pid` hold for claude on the ptyrunner path?** Expected yes
  (tui-driver spawns through `pty.Start`, which sets `Setsid`), but `tui-driver` is an
  external module (`v1.12.0`) and `reap.go:45-47` asserts it as a comment, not a test.
  This ticket **measures** it rather than assuming it. A `pgid != pid` reading is a
  first-class finding, not an instrument fault.
- **Does the streamrunner path differ?** `streamrunner` spawns claude with plain
  `exec.CommandContext` and no `Setpgid` (`streamrunner/runner.go:174`), so claude would
  inherit pyry's group there — making `pgid == rootPid` never fire while `pgid == self`
  excludes claude's group instead. Out of scope for this ticket (one run, the
  `agent-run` default path); worth a follow-up if the ptyrunner reading is surprising.
  Record which path was exercised so the reading's scope is unambiguous.
- **`fired-no-row-matched` is three-valued underneath** (never ran / backgrounded then
  exited / matcher broken). The two self-checks discharge the third; the first is
  excluded by the handle-first ordering. What remains — "backgrounded and then left the
  table during the held window" — is genuinely surprising given the FIFO hold, and
  should be reported verbatim rather than interpreted.

---

## Security review

**Verdict:** PASS (first pass FAILed on one MUST FIX; revised inline and re-walked)

**Findings:**

- **[Trust boundaries] MUST FIX — fixed.** First draft left the untrusted→trusted
  crossing for the process table implicit. Now a single named pair: `reachScanArgv`
  (impure, one frame, discards the table) → `reachMatchArgvRows` (pure, self-checked,
  returns matched-and-capped rows only). The two other crossings are narrowed to
  projections that emit scalars: `reachBackgroundHandle` → two strings + a bool, and
  `probeToolUseInput` → one bool (§ Guarding the tool_use now forbids carrying the
  model's verbatim params into the record). The integer-snapshot crossing is
  `reachIndexFromPS` / `probeDescendantsFromPS`, both pure and both self-checked.
- **[Subprocess] MUST FIX — fixed.** The spec said "mirror `:436-455`" for the cleanup
  block without naming `:443`'s `if rec.PyryPID <= 0 { return }` guard. A developer
  writing a fresh rep body drops that line and a pre-`cmd.Start` failure reaches
  `syscall.Kill(-0, SIGKILL)` — `kill(0, sig)` signals *the caller's own process
  group*, so the test binary SIGKILLs itself and its siblings. This was the FAIL.
  § Concurrency model now names the line, the mechanism, and the blast radius.
- **[Tokens, secrets, credentials] MUST FIX — fixed by prohibition.** The ticket's
  mechanism is a full-table process read, and `ps -E` / `ps eww` is one flag away from
  `ps -o command=`. That flag dumps every process's *environment*, and
  `WithWorktreeAuthenticated` requires `CLAUDE_CODE_OAUTH_TOKEN` or `ANTHROPIC_API_KEY`
  in the outer environment, which pyry forwards to claude verbatim — so the mistake
  would put the operator's live credential into an artifact destined for a public
  issue. #1223 needed an environment read and contained it with the `probeLeverVars`
  allowlist (`:150-158`); this ticket needs none, so § Redaction rule 1 removes the
  capability rather than allowlisting it.
- **[Trust boundaries / Error messages] SHOULD FIX — addressed in design.** A local
  process can read the process table, so it could spawn a process carrying this run's
  needle in its argv and inject attacker-chosen text into an artifact the operator
  pastes into a public issue. Requires local code execution plus a race inside the held
  window — outside this probe's threat model — but the mitigation is three lines:
  § Redaction rule 5 caps each retained command at `reachMaxCommandBytes` with a marker
  and records the total rows scanned so an implausible match count is visible.
- **[File operations] No findings.** No untrusted value is ever concatenated into a
  filesystem path — the needles are substring comparands, not paths to open. **The probe
  opens nothing it discovers via `ps`**, so there is no symlink or TOCTOU surface on
  discovered paths. Artifacts: `MkdirTemp` (0700) + `0600` files, outside the repo. The
  one check-then-use window that does exist (two `ps` calls) is named in § The
  measurement step 4, bounded by the FIFO hold and by run-unique needles, and netted by
  an explicit skew cross-check that reports "instrument fault", never "not reachable".
- **[Subprocess] No further findings.** `ps` is invoked with fixed argv; no `sh -c`; no
  user-controlled value in any exec argument; needles are matched in Go, in-process
  (§ Redaction rule 4), so a FIFO path can never be shell-interpreted. Both execs are
  bounded by a 5 s `context.WithTimeout`. Double-fork escape is the phenomenon under
  measurement here, not a vulnerability in the measurement.
- **[Cryptographic primitives] Not applicable — by design, not by omission.** No
  randomness, no hashing, no key material, no secret comparison. The one identifier
  matched on (the run's session UUID) is generated by pyry and used as an *evidence*
  lookup, never an authorization decision, so a non-constant-time `strings.Contains` is
  the correct primitive.
- **[Network & I/O] Not applicable.** No sockets, no HTTP, no TLS, no deserialization of
  remote input. `exec.Cmd.Output()` buffers the process table without a size cap;
  considered and accepted — it is local, bounded by the machine's process count, and a
  memory question rather than a security one.
- **[Error messages, logs, telemetry] No findings.** Every non-structural failure is
  *recorded* rather than fatal (§ Error handling table). The `t.Logf` summary emits
  record fields only. `t.Fatalf` diagnostics print pyry's streams through the existing
  `truncate` helper, and § Redaction rule 7 drops #1223's stdout/stderr artifact files
  entirely. No telemetry.
- **[Concurrency] No findings.** Two goroutines, both inherited from #1223, both with a
  documented exit path (FIFO hold → its own `t.Cleanup` release; `cmd.Wait` → pyry
  exit, guaranteed by the backstop). No leak, no locks beyond the reused
  `probeSyncBuffer`. LIFO cleanup order is pinned in § Concurrency model. A `t.Fatalf`
  mid-run leaves a partial record on disk, which is correct — the record is evidence,
  not state to be recovered.
- **[Threat model alignment] No repo threat-model doc exists** (`docs/threat-model.md`
  absent; `docs/knowledge/decisions/` carries no relevant ADR). This ticket adds **zero
  production files**, so it has no production attack surface at all. The one applicable
  threat is artifact disclosure into a public issue, which § Redaction addresses with
  seven structural rules and one stated residual (matched rows disclose temp paths and
  the run's session UUID — both fields #1223 already published).

**Carry into code review:** confirm no `-E`, `-e` with an environment column, or `eww`
reached any `ps` invocation, and that `reachScanArgv` neither returns nor stores the raw
table.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-29
