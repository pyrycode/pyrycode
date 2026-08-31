# background_reach_probe_test.go
- `background_reach_probe_test.go` (#1230) — **evidence probe, not a regression
  gate**; opt-in behind `PYRY_PROBE_BACKGROUND_REACH=1`, reusing #1223's staging
  rig verbatim (`background_trigger_probe_test.go` not edited; every new symbol
  `reach`-prefixed against the concurrent `feature/1219` branch and sibling
  #1231). Answers the predictive half of "does a backgrounded Bash command
  outlive pyry": is it still a transitive child of claude's pid inside
  `agentrun.ReapDescendantGroups`'s descendant-BFS reach, and would its process
  group survive the reaper's three exclusions (`reap.go:52`) — one during-turn
  snapshot, no teardown. **Content-first identification, not subtree-first**:
  one full-table `ps -axww -o pid=,ppid=,pgid=,command=` matched in Go against
  the run's FIFO path and session UUID across the whole process table — the
  read #1223's subtree-first, base-name-only `probeAnnotateCommands` cannot
  perform, and the one that could actually catch a re-parented survivor. Root
  pinned content-first via `--session-id <uuid>` in claude's argv (the ptyrunner
  path only), checked for agreement against the rig's positional
  `probeWaitForDirectChild` guess rather than trusted on its own. Two
  reachability reads off one integer snapshot — `reachChainUp` walking ppid
  links up, `probeDescendantsFromPS` (#1223's, unedited) BFS-ing down —
  disagreement recorded as an instrument fault, never a finding. Three-valued
  match outcome (`matched` / `trigger-never-fired` / `fired-no-row-matched`),
  established before any reachability claim is made. **Result (live run,
  2026-07-30, claude 2.1.220):** the backgrounded `cat`/`zsh -c` pair IS
  reachable from claude's pid, two hops down, and the zsh wrapper's process
  group survives all three exclusions — the reaper *would* target it; whether
  it actually dies is #1231's question. Redaction is structural
  (`security-sensitive`, earned by this ticket): the raw argv table never
  leaves one stack frame, no `-E`/`-e`-with-environment/`eww` anywhere,
  commands capped at 512 bytes after matching, only the integer-column
  snapshot is persisted verbatim. Three credential-free self-checks
  (`TestReachMatchArgvRows`, `TestReachChainUp`, `TestReachBackgroundHandle`)
  run ungated. Zero production files touched. See
  [`codebase/1230.md`](../codebase/1230.md) for the full arithmetic, the
  live-run evidence, and lessons from two rounds of code review (a MUST FIX
  gating the reachability verdict on the integer snapshot's own read error,
  plus a still-open SHOULD FIX on two record fields' finding-semantics).
- `process_pin_liveness_test.go` (#1235) — **offline instrument, not a probe**;
  no auth skip, no env gate, no live claude, no verdict about pyry — it is
  depended on as code, not as evidence, by the live probes #1236 → #1237. Two
  parts, both additive over #1230's `reach*` surface. **Exclusion-aware argv
  scan (`pin*` prefix)**: `pinPartition` is a pure post-filter over
  `reachMatchArgvRows`' own `(matches, total)`, splitting by a caller-supplied
  `exclude map[int]string` so an instrument-owned pid is withheld with its
  reason recorded rather than relying on a needle that happens not to collide
  with it; every matched row is retained (`MatchCount` visible as `> 1` rather
  than resolved to the first), and `reachMatchArgvRows`/`TestReachMatchArgvRows`
  are untouched. **Four-valued per-pid liveness read**: `pinReadState(pid)`
  execs a narrow `ps -p <pid> -o pid=,ppid=,stat=` (no descendant requirement —
  a target re-parented to pid 1 reads like any other) and classifies into
  `running` / `exited-but-not-yet-reaped` / `no-such-process` /
  `instrument-failed`, never collapsing two of them. Branch order is the
  contract: stderr, a `CommandContext` timeout's non-`ExitError` type, and
  stdout arriving alongside an error are all checked before the
  `no-such-process` default is reachable — closing the measured trap where a
  bad `ps` column prints a keyword list on stdout next to a non-zero exit, and
  the measured trap where a timeout-killed `ps` is byte-identical to a dead pid
  on every field but the sign of its exit status. Zombie detection is
  first-rune (`state[0] == 'Z'`), not equality — darwin emits `ZN`/`Z`, Linux
  `Z+`, and an equality miss falls through to `running` silently, the one
  direction this instrument must never fail in. Five credential-free
  self-checks, including a one-subject one-lifetime flip
  (`running` → kill-without-wait → `exited-but-not-yet-reaped` → wait →
  `no-such-process`) that proves the exec wiring rather than only the
  classifier. `security-sensitive`, earned by the column set's environment-read
  prohibition (`pid=,ppid=,stat=`, no `-E`/`-e`-env/`eww`) backed by a
  deterministic tripwire test, not just a doc comment. Zero production files
  touched; blocked by, and reuses rather than rebuilds, #1230's argv scan. See
  [`codebase/1235.md`](../codebase/1235.md) for the branch-order table, the
  patterns this ticket's measured traps establish, and a code-review SHOULD FIX
  (not blocking, deferred to #1236) on a self-check whose comment overclaims
  what its assertion pins.
- `teardown_liveness_test.go` (#1250) — **offline instrument, not a probe**; no
  auth skip, no env gate, no live claude, no verdict about pyry — depended on
  as code by the live rig #1251. Two additive parts over #1235's `pin*` and
  #1239's `fifoLive*` surfaces, both unedited. **Reaper-log classifier
  (`tdn*` prefix)**: `tdnClassifyReapLog(stderr, heldPGID)` is pure over bytes
  and answers `held-pgid-in-reap-line` / `reap-line-without-held-pgid` /
  `no-reap-line` (ambiguous by construction — `reap.go:64` guards the emit on
  `len(reaped) > 0`, so silence means "reaped nothing" or "never fired," and
  the `Detail` names both) / `instrument-failed`. Anchored on the reap
  message's bare text as a string literal, never `msg="..."` —
  `runAgentRunStreamRunner` passes no `Logger`, so `streamrunner.Run` falls
  back to `slog.Default()`, not the `slog.NewTextHandler` the ticket body
  cited, and an anchor built against the wrong handler would silently read
  "no line" on the only path that matters. (#1557 re-attributed this and two
  sibling comments in the source from the `ptyrunner` path #1348 deleted to
  the surviving `streamrunner` path; the mechanism was unchanged, only its
  owner's name was wrong.)
  Membership decided over parsed integers via a key-boundary attribute match
  (`tdnAttrIndex`), never a substring — closes both a false negative (`slog`
  quotes `pgids=` the moment a second pgid appears) and its dual false
  positive (held `77` inside the text of `pgids=[7788]`). **Real-`ps`
  fail-safe premise (AC2)**: four mis-invocation arms assert
  `len(exitErr.Stderr) > 0` read from `.Output()`'s own `*exec.ExitError`
  (the exact channel `pinReadState` consumes) before requiring
  `pinClassifyState` to return `instrument-failed` — proving, against real
  bytes rather than #1235's hand-built errors, that branch 1 keeps every
  broken invocation off the `no-such-process` verdict. The bad-column arm
  uses four requested columns (not three) because `ps` silently drops the
  unknown one and prints the rest, producing a row `pinStateRow` parses
  *successfully* as a live pid; the out-of-range arm escalates a candidate
  ladder until `ps` actually rejects one, rather than assuming a hard-coded
  constant is out of range (macOS caps at 99999, Linux's default `pid_max` is
  4194304). **Record + writer (AC3)**: `tdnRecord` composes
  `pinStateOutcome`/`fifoLiveOutcome`/`tdnReapOutcome` with no new liveness
  type and no verdict synthesized across them; `writeTdnArtifacts` emits
  exactly one file (`teardown.json`, `0o600`) — "exactly one file" is itself
  the redaction assertion, since the sibling writer's second file (a verbatim
  `ps` snapshot) has no analog here. 22 credential-free self-checks, zero
  SKIP. Zero production files touched. See [`codebase/1250.md`](../codebase/1250.md)
  for the full implementation, the subprocess-boundary citation-swap pattern,
  and a code-review SHOULD FIX (not blocking, deferred to #1251) on a
  first-match self-check row that doesn't discriminate its own claimed
  mutation.
- `teardown_reap_capture_test.go` (#1253) — **offline instrument, not a
  probe**; no auth skip, no env gate, no live claude. Drives #1250's
  `tdnClassifyReapLog` with bytes captured from a *real*
  `agentrun.ReapDescendantGroups` call, replacing that classifier's
  hand-written string-constant fixtures with a live capture so a future
  `slog` rendering change fails a test instead of silently making every
  liveness answer read `no-reap-line`. Builds real two-level process trees
  (test → re-exec'd parent → leaves, the parent required because
  `setpgid` on a child rules out a shell) and captures whatever
  `slog.Default()` emits during the reap via `log.SetOutput` — no `t.Parallel`
  in the file, since that redirect is process-global and not reentrant. Proves
  the capture *flips* within one harness: a killed group classifies
  `held-pgid-in-reap-line`, a childless walk root emits no line at all and
  classifies `no-reap-line`. A same-group sibling spared by `reap.go:52` is
  asserted *still alive* at the instant its pgid reads absent from the line —
  the unearned negative the instrument exists to refuse. Both renderings
  (`pgids=[N]` unquoted, `pgids="[N M]"` quoted) come from real reaps and are
  asserted to differ; the substring hazard is closed in the previously-untested
  suffix direction (`88` vs `[7788]`). One new file rather than an edit to
  `teardown_liveness_test.go`, both because that file's header declares itself
  "pure over bytes" (this harness spawns real trees and issues real SIGKILLs)
  and because #1251 had an in-flight +259/−40 diff to it at filing time. Every
  pid a real reap produces is treated as a trust boundary: `tdnKillTree`
  refuses `pid <= 1`/the test's own pid/pgid before any `syscall.Kill`, and the
  multi-line report-file parse is all-or-nothing rather than treating a short
  read as "not ready yet." 32 credential-free subtests, zero SKIP. Zero
  production files touched; calls `tdnClassifyReapLog` and does not edit it.
  See [`codebase/1253.md`](../codebase/1253.md) for the full implementation and
  two non-blocking code-review NITs (a misleadingly-named loop variable, one
  reasoned-not-measured comment).
