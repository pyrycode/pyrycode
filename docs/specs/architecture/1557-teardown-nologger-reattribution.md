# #1557 — Re-attribute the two teardown no-Logger comments to streamrunner

**Size:** XS. Three comment sites, two files, both `*_test.go`. Zero production files, zero
statements, zero new symbols. Expected diff: **11 lines added, 11 removed.**

## Files to read first

Symbol anchors, not line numbers — every one resolves with `codegraph_search` or a `git grep`
on the name. Line spans appear once each in § Design as a measurement snapshot at `c1b1ce1`,
because AC6 is itself a line-arithmetic criterion; they are not addresses to rely on.

| Where | Symbol | What to extract |
|---|---|---|
| `internal/e2e/realclaude/teardown_liveness_test.go` | file-header doc comment, § *"The reaper line, and the two ways a matcher over it inverts"* — the paragraph beginning `The first is the slog.NewTextHandler` | **Site 1.** The 9-line paragraph. Note its first line is already correct and must not be touched. |
| same file | `TestTdnClassifyReapLog` → the table row named `slog.Default rendering, one pgid, held present` | **Site 2.** Its two-line leading comment. The row's `name:` field is a string literal and is off-limits (AC5). |
| `internal/e2e/realclaude/teardown_reap_capture_test.go` | `tdnCaptureReap` doc comment — the paragraph beginning `Why log.SetOutput captures it:` | **Site 3.** Only its first two lines move. |
| `cmd/pyry/agent_run.go` | `runAgentRunStreamRunner` | The `streamrunner.Config` composite literal: fields are `ClaudeBin`, `WorkDir`, `Args`, `PromptBytes`, `Stdout`, `Stderr` — **no `Logger`**. This is the fact the new wording asserts. |
| `internal/agentrun/streamrunner/runner.go` | `Run` | `logger := cfg.Logger` / `if logger == nil { logger = slog.Default() }`, and that the same `logger` is handed to `reapDescendantGroupsFn(cmd.Process.Pid, logger)`. Also the `Config.Logger` field's own doc line, *"Optional; nil falls back to slog.Default()."* |
| `internal/agentrun/streamrunner/reap.go` | `reapDescendantGroupsFn` | Bound to `agentrun.ReapDescendantGroups` — the production wiring that closes the chain. |
| `cmd/pyry/agent_run_test.go` | the comment naming the `ptyRun` / `newSessionID` seams | The in-repo wording model the ticket cites: state history plainly, don't imply a dead seam is live. Note that none of our three sentences need to mention the deleted package at all. |
| `docs/knowledge/features/pyry-agent-run-command.md` | — | Background on the single surviving agent-run path. Reconciled by #1555 (merged `5a36235`), so it is current as of today; earlier revisions still described the deleted two-path world. |

## Context

#1348 deleted the PTY `pyry agent-run` entry point. `runAgentRunPty` has no declaration in the
tracked tree and `internal/agentrun/ptyrunner` no longer exists, but three comments in the two
realclaude teardown files still explain a load-bearing capture mechanism in terms of those dead
names. The files sit behind the `e2e_realclaude` build tag, so `make check` never compiles them
and no standard gate can see the rot.

**The argument each comment makes is still true. Only the owner's name is wrong.** Verified at
`c1b1ce1` (not merely re-quoted from the ticket):

1. `runAgentRunStreamRunner` builds `streamrunner.Config` and sets no `Logger` field.
2. `streamrunner.Run` does `logger := cfg.Logger; if logger == nil { logger = slog.Default() }`.
3. That same `logger` reaches the reaper via `reapDescendantGroupsFn(cmd.Process.Pid, logger)`,
   and `reapDescendantGroupsFn` is `agentrun.ReapDescendantGroups` in production.

So a spawned `pyry agent-run` still renders `ReapDescendantGroups`'s reap line through
`slog.Default()` and Go's built-in default handler, which writes through the `log` package —
which is exactly why `log.SetOutput` captures it. This is a rename of the mechanism's owner, not
a change to the argument. No ADR is warranted.

**Site-table exhaustiveness, independently confirmed.** A symbol sweep is not sufficient here
(site 2 names neither `runAgentRunPty` nor a qualified `ptyrunner.X`), so the two files were
swept three further ways to rule out a fourth site restating the claim in yet other words:
`-i pty` (returns only the three known comment lines plus `omitempty` struct-tag and `empty`
prose noise), `-i terminal` (one unrelated hit about re-exec depth in `teardown_reap_capture_test.go`),
and `Logger|SetDefault` (returns only lines inside the three known sites). **Three sites is the
complete set for these two files.**

## Design

There is no design surface: no types, no interfaces, no data flow, no goroutines, no error
paths. The deliverable is three comment substitutions whose only non-trivial constraint is
AC6's per-hunk line parity. That constraint is what this section specifies.

### Why parity, and why it is cheap

35 markdown citations of the form `teardown_liveness_test.go:NNN` point below the site-1
paragraph, three of them below site 2. `cmd/cite-guard` does not police markdown citing Go, so a
one-line growth would silently rot all 35 with a green build. Parity holds every one of them.

The parity is achievable **without any judgment call**, because the growth per site is bounded
and was measured. The three substitutions add `runAgentRunStreamRunner` (+9),
`streamrunner.Config` (+3) and `streamrunner.Run` (+3). Sites 2 and 3 have enough headroom on
the affected lines to absorb theirs with no reflow at all; only site 1 needs its paragraph
reflowed, and 8 lines is provably the minimum that fits (7 lines would require a 78.1-character
average against the file's 77-character text budget).

### The three hunks

Measured at `c1b1ce1`; the anchors above are what to navigate by. All widths below are raw
line length including the `// ` prefix, against the file's 80-column comment convention.

**Site 1 — `teardown_liveness_test.go`, the file-header paragraph (currently lines 33–41).**
The paragraph's first line is already correct and byte-identical to the replacement, so the hunk
is **8 lines out, 8 lines in** (currently 34–41). Verified: 8 lines, every line ≤ 79 raw, no
backticked token split across a break, `"no line"` kept intact.

```go
// The first is the slog.NewTextHandler runSupervisor installs. The second is   <- UNCHANGED
// slog.Default(), and it is the one that matters: runAgentRunStreamRunner sets
// no Logger on streamrunner.Config, so streamrunner.Run falls back to
// slog.Default() on a nil Logger, and every probe in this package spawns
// `pyry agent-run` and captures its stderr. A matcher anchored on
// `msg="agentrun: reaped…"` finds nothing on the live path and answers
// "no line" — read as "the reaper never fired" — with nothing going red. So
// the anchor is the BARE message text, a string literal in this file, never a
// level token, a timestamp, or a reference to what reap.go defines.
```

**Site 2 — `teardown_liveness_test.go`, the `slog.Default rendering, one pgid, held present`
row's leading comment (currently lines 544–545).** First line unchanged; **1 line out, 1 in.**

```go
			// The rendering the LIVE path produces: `pyry agent-run` passes no   <- UNCHANGED
			// Logger, so streamrunner.Run falls back to slog.Default().
```

**Site 3 — `teardown_reap_capture_test.go`, the `tdnCaptureReap` doc comment (currently lines
414–420).** Both growths land on lines with headroom (66 → 75 and 73 → 79 raw), so lines 3–7 of
the paragraph are untouched: **2 lines out, 2 in.** No reflow.

```go
// Why log.SetOutput captures it: runAgentRunStreamRunner sets no Logger on
// streamrunner.Config, so streamrunner.Run falls back to slog.Default() and no
// non-test code calls slog.SetDefault — so every `pyry agent-run`, which is    <- UNCHANGED
```

### Two resolved readings the developer should not re-litigate

- **Site 2 keeps its short form.** AC3's sentence names all three symbols, but AC6 forbids
  growing this row: the full form runs to three lines inside a table-row indent whose text budget
  is ~64 characters. The short form still attributes the fallback to the surviving path, and AC4
  requires the row keep its existing point (this row's fixture is the rendering the live path
  produces, versus the `slog.NewTextHandler` rendering of the row above). The ticket's own
  arithmetic note assumed the bare `ptyrunner` → `streamrunner` swap (+3); the wording above uses
  `streamrunner.Run` (+7) instead because that is the function that actually performs the
  fallback. Both satisfy AC6 with room; the qualified form reads truer against AC3.
- **Neither replacement mentions the deleted package.** Per the `cmd/pyry/agent_run_test.go`
  model, the sentences simply name the surviving path. Adding "formerly ptyrunner" would
  reintroduce the identifier AC2 sweeps for.

### Two ways to fail this ticket

- **Do not widen into a general `ptyrunner` sweep.** That identifier is named in comments across
  34 other files, much of it legitimate history — including `internal/agentrun/streamrunner/reap.go`,
  `internal/agentrun/selfcheck/selfcheck.go`, `finding_exit_path_probe_test.go`,
  `finding_run_gather_test.go` and `teardown_liveness_probe_test.go`. AC2 is file-scoped for
  exactly this reason. The `finding_live_*` files belong to #1556 and `cmd/pyry/agent_run.go`
  to #1555; leave both alone.
- **Do not write a `file.go:NNN` citation into any Go comment you touch.** `cmd/cite-guard` is
  diff-scoped and fails the build on such a citation inside a Go comment when it resolves into a
  declaration — and it has no depth or range exemption. The whole point of this ticket is that
  line-anchored references rot; name the symbol. The line numbers in this spec are a measurement
  snapshot for navigation and must not be copied into the code.

## Concurrency model

None. No runtime behaviour changes; the diff is inert to the compiler.

## Error handling

None. No new failure modes.

## Testing strategy

No new tests. `tdnCaptureReap` and `TestTdnClassifyReapLog` already pin the behaviour the
comments describe, and this change must leave both byte-identical in their executable parts.

Verification is five mechanical checks over the finished diff. Run them from the worktree root.

**AC1 + AC2 — the sweeps, with their non-vacuity controls:**

```bash
git grep -n '\brunAgentRunPty\b' -- internal/e2e/realclaude/teardown_liveness_test.go internal/e2e/realclaude/teardown_reap_capture_test.go   # want: no output
git grep -n 'ptyrunner'          -- internal/e2e/realclaude/teardown_liveness_test.go internal/e2e/realclaude/teardown_reap_capture_test.go   # want: no output
git grep -c '\brunAgentRunStreamRunner\b' -- '*.go'   # control: must still return hits
git stash && git grep -c 'ptyrunner' -- internal/e2e/realclaude/teardown_liveness_test.go internal/e2e/realclaude/teardown_reap_capture_test.go && git stash pop   # control: exactly 3 on the unmodified tree
```

Use `git grep`, never `grep -r`: the untracked `.claude/worktrees/` checkout is a pre-#1348
module that still defines `runAgentRunPty` for real, and `git grep` is untracked-blind by
construction. (`git grep -E '\bfoo\b'` matches nothing — POSIX ERE has no `\b`. The commands
above use `git grep`'s default basic-regex mode, where `\b` does work.)

**AC5 — comment lines only.** Every changed line must be a `//` comment; this must print nothing:

```bash
git diff -U0 -- internal/e2e/realclaude/teardown_liveness_test.go internal/e2e/realclaude/teardown_reap_capture_test.go \
  | grep -E '^[+-]' | grep -vE '^(\+\+\+|---)' | grep -vE '^[+-][[:space:]]*//'
```

**AC6 — per-hunk parity.** Every hunk must report `OK`, and the two files must still be 1213 and
716 lines. Note the `+++`/`---` header skip: without it the second file's headers corrupt the
count.

```bash
git diff -U0 -- internal/e2e/realclaude/teardown_liveness_test.go internal/e2e/realclaude/teardown_reap_capture_test.go \
  | awk '/^\+\+\+/ || /^---/ {next}
         /^@@/ { if (h != "") print (a==d ? "OK  " : "FAIL"), "add="a, "del="d, h; h=$0; a=0; d=0; next }
         /^\+/ {a++} /^-/ {d++}
         END { if (h != "") print (a==d ? "OK  " : "FAIL"), "add="a, "del="d, h }'
wc -l internal/e2e/realclaude/teardown_liveness_test.go internal/e2e/realclaude/teardown_reap_capture_test.go
```

Expected: three hunks reporting `add=8 del=8`, `add=1 del=1`, `add=2 del=2`.

**AC7 — the gates.** `make check` cannot compile these files, so the tagged commands are not
optional:

```bash
go vet -tags e2e_realclaude ./internal/e2e/realclaude/   # exit 0
go build -tags e2e_realclaude ./...                      # exit 0
make check                                               # green
```

Baseline at `main`: both tagged commands exit 0, so any failure is yours.

## Open questions

None. Every fact the wording asserts was re-verified against `c1b1ce1`, the three drafts were
machine-checked for line count, width and token integrity, and the site table was confirmed
exhaustive for the two owned files by three independent sweeps.

## Scope check against the size-S boundary

Recorded because one line trips and the trip should be visible to review rather than buried.

| Limit | Boundary | This ticket |
|---|---|---|
| Production source files created or modified | ≤ 3 | **0** — both files are `*_test.go` |
| Total written work | ≤ 400 lines | **~22** (11 added, 11 removed) |
| New exported types or interfaces | ≤ 5 | **0** |
| Consumer call sites needing simultaneous update | ≤ 10 | **0** |
| Acceptance criteria | ≤ 5 | **7 — exceeds** |
| Distinct error/reject branches | ≤ 10 | **0** |

Shipped as one XS ticket rather than routed back for a split, on three grounds:

1. **The nearest analogue commit.** #1555 — same #1554 split family, same `size:xs`, same
   stale-comment-reconciliation shape, **also 7 acceptance criteria** — closed clean as `5a36235`
   with no `rework-count` label. #1556 carries 6 ACs and #1519 carries 6. In this family the AC
   count measures verification density over one atomic edit, not deliverable count: of the seven,
   two state the deliverable (AC3, AC4) and five are commands run over the same single diff.
2. **The re-count does not shrink real work.** The rationalization the boundary defends against
   is one that re-labels edits as cheap while the developer still pays for all of them (#75's 26
   `NewServer` call sites). Here there is no hidden volume to re-label: 11 changed lines, no
   statements, no tests to write, no fan-out. The five verification ACs are five shell commands,
   supplied verbatim above.
3. **A split is self-defeating, not merely wasteful.** AC2 demands *zero* `ptyrunner` hits across
   both files. Any child that ships a subset leaves hits behind, so the criterion is satisfiable
   only by whichever child lands last — the parent's mechanism would have to be dropped or
   rewritten site-scoped to split at all (the #1312 failure shape). The three sites are one
   claim stated three times; they are not separable.

If review disagrees, the correct remedy is to relax the AC count in PO's template for
verification-dense comment tickets, not to fragment this one.
