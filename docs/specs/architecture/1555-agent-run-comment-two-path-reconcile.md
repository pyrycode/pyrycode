# Spec #1555 — Reconcile the stale two-path comments in `cmd/pyry/agent_run.go`

**Size:** XS (confirmed from PO). One file, comment lines only, zero production statements.
**`security-sensitive`:** yes — two of the six sites are the load-bearing justification for a live
tool-permission enforcement block. The § Security review pass at the end of this spec is mandatory
and was run. The change cannot alter behaviour, so that review is of the *replacement wording*, not
of a design.

## Files to read first

- `cmd/pyry/agent_run.go` → `runAgentRunStreamRunner` — sites 1–3. Read the whole function including
  its doc comment; the `#1387` paragraph above the `settingsWrite` call is the one that must survive.
- `cmd/pyry/agent_run.go` → `buildStreamRunnerClaudeArgs` — sites 4–6. The doc comment is long; only
  three paragraphs of it are in scope.
- `cmd/pyry/agent_run.go` → `runAgentRun` — the body comment above the `runAgentRunStreamRunner` call
  is **already correct** and is the tone to match: it states the single-path fact and says
  `PYRY_USE_STREAMJSON` "is deliberately NOT read any more". Out of scope; do not edit it.
- `cmd/pyry/agent_run.go` → `agentRunUsageDescription` — the shipped `--help` body. This is the
  wording the comments must agree with ("There is no second runner…"). **Do not edit the literal**;
  AC 5 pins it byte-identical.
- `cmd/pyry/agent_run_test.go` → `TestAgentRunUsageDescription` — why that literal is pinned: it locks
  the `--help` prose against stale-disclaimer regressions (#359). Editing the literal breaks it.
- `cmd/pyry/agent_run_test.go` → `TestBuildStreamRunnerClaudeArgs_Shape` — the argv contract. A useful
  symbol to point at from the rewritten `buildStreamRunnerClaudeArgs` doc, in place of the deleted
  "the other path owns its own argv" sentence.
- `internal/agentrun/settings/settings.go` → `WriteSettingsWithDeny` — read its doc comment and the
  `Deny` field on `settingsFile`. This is the **source for site 3's replacement text**, verified at
  architect time: the `--disallowed-tools` tokens become `permissions.deny`, an empty slice omits the
  key entirely, and listing a tool there removes it from the model's tool surface rather than
  producing a runtime denial (`#411`, origin `#398`). Do not restate this from memory — quote the
  mechanism this doc comment already commits to.
- `CODING-STYLE.md` § "Comments — Citing Other Code" — every comment line you touch enters
  `make cite-guard`'s diff-scoped check. Cite by symbol; never write `agent_run.go:NNN`, a range, or
  a bare `:NNN` into the replacement text. Issue references (`#1348`, `pyrycode#1387`) are fine.
- `docs/specs/architecture/971-comment-hygiene-sweep.md` — the house pattern for a comment-only sweep.
  Its § Item 2 makes the point that matters here: **a mechanism correction, not a token swap.**
- `docs/knowledge/features/pyry-agent-run-command.md` — **read this as a warning, not as a source.**
  It is itself pre-#1348 and still documents the two-path world in detail (a `PYRY_USE_STREAMJSON`
  branch table, `runAgentRunPty`, `ptyrunner.Run`). If you consult it for "how does this work", it
  will feed you exactly the falsehoods this ticket removes. It is documentation-phase-owned and out
  of scope — see § Context.

## Context

#1348 deleted the PTY `agent-run` entry point. Verified at this tree, not inherited from the ticket:

- `git grep -n 'func runAgentRunPty' -- '*.go'` exits 1 — no declaration anywhere tracked.
- `internal/agentrun/` has no `ptyrunner` directory.
- `runAgentRunPty` survives in `cmd/pyry/agent_run.go` at exactly **one** comment line; `ptyrunner` at
  **four**; the control symbol `runAgentRunStreamRunner` matches **4** lines in the same file.
- Outside this file the symbol legitimately survives in 7 comments across 4 files under
  `internal/e2e/realclaude/` (`finding_live_run_test.go` ×4, `finding_live_staging_test.go`,
  `teardown_liveness_test.go`, `teardown_reap_capture_test.go`), where it is history. **Not in scope.**

The binary already tells the truth — `agentRunUsageDescription` says "There is no second runner." The
Go comments in the same file contradict it at six sites. Two of those sit inside the `pyrycode#1387`
evidence block, so a maintainer auditing the tool-permission boundary is told twice to go compare
against an enforcement path that does not exist.

**Why the line-preservation constraint (AC 6) is real.** Re-measured at this tree: `docs/` carries
**36 occurrences** of the literal form `agent_run.go:NNN` with `NNN ≥ 281` — the region being edited —
spread over **18 files**, resolving to **20 distinct line numbers**. (The ticket says 31; the exact
figure depends on the regex, and the count is a lower bound either way, since ranges and
symbol-anchored `<Symbol>:NNN` forms exist too and neither this sweep nor `cmd/cite-guard` sees them —
the guard covers Go comments, not markdown.) A net line-count change rots all of them silently, which
trades one class of stale reference for a larger one.

**No ADR warranted.** This reconciles prose to a decision already recorded (#1348); it makes no new one.

**Follow-up, explicitly not this ticket:** `docs/knowledge/features/pyry-agent-run-command.md` carries
the same falsehood at greater length and with more authority than the comments do. It is owned by the
documentation phase, and knowledge docs are off-limits to the developer's worktree. Flagging it here
so the documentation phase or a sibling #1348-residue ticket picks it up; the developer must not touch it.

## Design

Six sites, one file, in place. Each is located by its **enclosing symbol plus a distinguishing
phrase** — locate with `git grep -n -F '<phrase>' -- cmd/pyry/agent_run.go` rather than by line number,
so the instruction survives your own edits.

Every site is reworded **inside its existing comment-line count**. Budgets below are counts, not spans.

| # | Site (symbol + phrase to grep) | Budget | The false claim to remove |
|---|---|---|---|
| 1 | `runAgentRunStreamRunner`'s doc comment — `is the legacy stream-json subprocess path` | 4 lines | "legacy"; "selected by `PYRY_USE_STREAMJSON=1`"; "preserved … for billing-classification comparison" (comparison against what?) |
| 2 | inside `runAgentRunStreamRunner` — `Write the same per-spawn deny-default settings file` | 8 lines | "the same … the ptyrunner path writes, and for the same reason" |
| 3 | inside `runAgentRunStreamRunner` — `Mirrors runAgentRunPty's block below` | 2 lines | the whole sentence: names an undeclared function, points "below" at nothing, asserts a two-path invariant |
| 4 | `buildStreamRunnerClaudeArgs`'s doc opening — `do not unify these shapes` | 4 lines | "selected by `PYRY_USE_STREAMJSON=1`"; "The ptyrunner default path owns its own argv inside `internal/agentrun/ptyrunner`" |
| 5 | `buildStreamRunnerClaudeArgs`'s doc, permission-slot paragraph — `The permission slot` … `wired to a live spawn downstream.` | 9 lines | "the legacy `PYRY_USE_STREAMJSON=1` path is always YOLO" |
| 6 | `buildStreamRunnerClaudeArgs`'s doc, `--settings` bullet — `IS the enforcement on this path` | 6 lines | "exactly as on the ptyrunner path" |

Sites 2 and 3 are contiguous (separated by one `//` line), so they may be treated as **one 11-line
block** if that reflows better. Either framing satisfies AC 6: a `k`-for-`k` replacement is one hunk
with equal counts.

**The ticket's title says "five" and its table lists six.** Both are right about different things —
five is what a `runAgentRunPty`-anchored sweep would *miss*. Fix all six.

### Shared vocabulary rule

Across all six replacements, these must not appear:

- `runAgentRunPty` (AC 1), `ptyrunner` in any casing (AC 2)
- "legacy" applied to this path, or any word implying a surviving counterpart: "the other path",
  "both paths", "the two paths", "default path", "sibling", "mirrors", "preserved for comparison"
- `PYRY_USE_STREAMJSON` described as selecting, choosing, or branching to a runner

**Recommendation: do not name `PYRY_USE_STREAMJSON` in the six replacements at all.** AC 3 permits
naming it where a comment says it is ignored, but `runAgentRun`'s body comment already carries that
whole story a few lines above the function, and re-stating it inside the rewrites is the easiest way
to reintroduce selector framing by accident. Say the single-path fact and stop.

"this path" on its own is **not** a two-path claim — `runAgentRun`'s own doc uses that framing and the
ticket blesses it. Do not sweep it.

### What the replacements must say

Site 1 (`runAgentRunStreamRunner` doc): what the function does, and that it is the only `agent-run`
path — `runAgentRun` calls it unconditionally, the terminal-driving runner was deleted in #1348. One
wording that fits the 4-line budget, offered to show the budget is not tight; the substance is the
contract, the exact words are not:

```go
// runAgentRunStreamRunner drives one claude turn as a stream-json subprocess:
// it writes the per-spawn settings file that carries the tool boundary, then
// hands the argv to streamrunner.Run. It is the only agent-run path — the
// terminal-driving runner it once shared the verb with was deleted in #1348.
```

Site 4 (`buildStreamRunnerClaudeArgs` doc opening): what the argv is for, and that this pipeline is
the only one. The freed two lines are a good place to point at
`TestBuildStreamRunnerClaudeArgs_Shape` by name, which is the honest replacement for "the other path
owns its own argv" — a reader who wanted the argv contract now has somewhere real to go.

Site 3 has no surviving claim to preserve, but AC 6 forbids deleting its two lines. Fill them with
something true and load-bearing rather than padding. The information the stale sentence carried that
is still worth keeping is "including the deny list" — so say what the deny list does, taking the
mechanism from `WriteSettingsWithDeny`'s own doc comment rather than inventing one: the
`--disallowed-tools` tokens become `permissions.deny` in the same write, and a tool listed there is
removed from the model's surface rather than denied at call time (#411). Two lines, no invention.

Sites 5 and 6 are single-clause excisions inside otherwise-accurate paragraphs. Site 6's stale tail
occupies one line; the rest of the bullet is correct and must be left intact:

```go
//     IS the enforcement — the only enforcement — that agent-run has.
```

### The #1387 finding — what "survives in substance" means (AC 4)

This is the part where a careless reflow does real damage. The finding must still assert, **at the
`settingsWrite` call inside `runAgentRunStreamRunner`**:

1. `--dangerously-skip-permissions` defeats `--allowed-tools`
2. measured 2026-08-08, referenced as `pyrycode#1387`
3. that this is *why* the per-spawn deny-default settings file is written here
4. the blast radius: every agent dispatched since the 2026-07-25 fleet switch ran unrestricted,
   returning architect-only web search and subagent spawning, the deliberately-excluded Figma write
   tools, and the three human-only tools, to every role

and **in the `--settings` bullet**:

5. the settings file IS the enforcement
6. it survives `--dangerously-skip-permissions` where the command-line allowlist does not
7. `--allowed-tools` is still passed but must not be relied on as the boundary

Removing the stale names *frees* lines at both sites — site 2 loses a sentence and a half and gains
nothing — so there is no budget pressure forcing any of these seven out. If you find yourself dropping
one to make the wrapping work, stop: losing the finding is explicitly a worse outcome than leaving the
comments alone.

The neighbouring `--dangerously-skip-permissions` bullet (the one containing "Measured 2026-08-08
against claude directly, two cells") is accurate and **out of scope** — do not touch it.

## Concurrency model

None. Comment-only; no goroutines, channels, or shutdown sequencing.

## Error handling

None. No control flow, no new failure modes. The change is inert to the compiler.

## Testing strategy

No new tests. Verification is five deterministic gates plus a read.

1. **Symbol sweep, scoped, with its control** (AC 1):
   `git grep -n '\brunAgentRunPty\b' -- cmd/pyry/agent_run.go` → zero hits, and
   `git grep -c '\brunAgentRunStreamRunner\b' -- cmd/pyry/agent_run.go` → still non-zero (4 at
   baseline; the declaration and the call in `runAgentRun` guarantee it). The control is not optional:
   a mistyped pathspec reports zero hits and exit 0, which reads identical to a clean sweep.
2. **Package sweep, scoped** (AC 2): `git grep -n 'ptyrunner' -- cmd/pyry/agent_run.go` → zero hits
   (4 at baseline). Both sweeps use `git grep`, never `grep -r`: the untracked `.claude/worktrees/`
   checkout is a pre-#1348 module that still defines `runAgentRunPty` for real, and `git grep` is
   untracked-blind by construction. An **unscoped** `git grep runAgentRunPty` will still show 7 hits
   in `internal/e2e/realclaude/` after a correct fix — that is a pass, not a miss.
3. **Vocabulary sweep** (AC 3): `git grep -n -i -E 'legacy|selected by|selector|both paths|the two paths|default path' -- cmd/pyry/agent_run.go`
   and read each surviving hit. Hits outside the six sites are fine if they are true; the sweep is a
   prompt to look, not a pass/fail count.
4. **#1387 token floor** (AC 4) — a deterministic net under the prose judgement, since AC 4 is
   otherwise only checkable by reading. Baselines measured at this tree with
   `git grep -c -F '<tok>' -- cmd/pyry/agent_run.go` (matching **lines**, not occurrences):

   | token | baseline | after |
   |---|---|---|
   | `2026-08-08` | 2 | must be 2 |
   | `1387` | 3 | ≥ 3 |
   | `dangerously-skip-permissions` | 6 | ≥ 6 |
   | `deny-default` | 4 | ≥ 4 |
   | `2026-07-25` | 3 | ≥ 3 |

   A decrease means finding-substance left the file. Passing this table is necessary, not sufficient —
   the seven substance points above still have to be read for.
5. **Diff shape** (AC 5 + AC 6): `git diff -U0` and check two things by eye —
   every `+`/`-` line begins with `//` after leading whitespace (no statement, declaration or string
   literal; in particular `agentRunUsageDescription` is untouched), and every hunk header reads
   `@@ -N,k +M,k @@` with **the same `k`**. Per hunk, not per file: a per-file total of zero still
   permits displacement.
6. `make check` green — `go vet`, `staticcheck`, `go test -race`, `substrate-guard`, and the
   diff-scoped `cite-guard`, which will inspect every comment line you touched. `gofmt` will not
   reflow `//` comments, but keep the existing indentation (tabs inside the function body, none on
   the top-level doc comments) so it stays a no-op.

`make check` is the right gate here rather than `make preship`: no test file is deleted or moved, and
no `e2e_realclaude` symbol changes.

## Open questions

- **Site 1's "Byte-equivalent to the pre-cutover `runAgentRun` body" clause.** It is history about the
  #391/#470 cutovers, and it is no longer literally true — `settingsWrite` was added to this function
  after #1387. Since the whole 4-line comment is being rewritten around it, the recommendation is to
  drop the clause rather than restate a byte-equivalence nobody has re-measured. Do **not** replace it
  with a new equivalence claim.
- **How much of site 5's paragraph to re-wrap.** The stale clause spans two line-ends inside a 9-line
  paragraph. Re-wrapping the whole paragraph to 9 lines and re-wrapping only the two affected lines
  both satisfy AC 6; prefer whichever produces the smaller diff, since every touched line enters
  `cite-guard`.
- If any site genuinely cannot be reworded within its budget, stop and route back to PO per the
  ticket rather than shifting lines. Feasibility was checked at architect time for all six — sites 1,
  4 and 6 are drafted above, and sites 2, 3 and 5 gain budget from the deletions — so this should not
  fire.

## Size check

Re-counted against this written spec, per the size-S boundary:

| Limit | Boundary | This spec |
|---|---|---|
| Production source files created or modified | ≤ 3 | **1** (`cmd/pyry/agent_run.go`) |
| Total written work | ≤ 400 lines | **~25** comment lines, hard-capped by AC 5 + AC 6 |
| New exported types or interfaces | ≤ 5 | **0** |
| Consumer call sites needing simultaneous update | ≤ 10 | **0** |
| Acceptance criteria | ≤ 5 | **7** — see below |
| Distinct error/reject branches | ≤ 10 | **0** |

The AC count is over. It does not fire a split here, and the reason is falsifiable rather than a
coupling argument: **the split's own children would trip the same line.** ACs 1, 2, 5, 6 and 7 are
whole-file/whole-diff invariants (`grep` zero *across the file*, comment-only diff, per-hunk line
preservation, `make check`) that every child must carry verbatim, and ACs 1–2 are *unsatisfiable by
any proper subset of the sites* — a child fixing only site 3 leaves `ptyrunner` on three other lines,
so its AC 2 fails. A two-way split therefore yields two children with 6–7 ACs each, both editing
`cmd/pyry/agent_run.go`, so the § 1.5 overlap rule serialises the second behind the first. The
remedy cannot satisfy the boundary, which means the proxy is measuring the wrong quantity for this
ticket class; the quantity it stands in for — developer edit volume — is bounded here at ~25 comment
lines by AC 5 and AC 6 themselves, and every other line of the table is far under. Nearest analogue:
#971, a three-item comment sweep across ten files, shipped XS.

Edit fan-out: not refactor-shaped. Nothing compiles against a comment; `codegraph_impact` on the
symbols involved is irrelevant because the only surviving `runAgentRunPty` reference is prose.

File-overlap check (§ 1.5): `git fetch origin --prune` then a scan of every `origin/feature/<N>`
branch's diff against `main` for `cmd/pyry/agent_run.go` — **no overlap**. No block set.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] **No findings, and this is the category that matters.** The trust boundary is
  unchanged and is the per-spawn settings file written by `settingsWrite`
  (`settings.WriteSettingsWithDeny`) inside `runAgentRunStreamRunner`; the boundary is enforced by
  claude reading that file via `--settings`, not by the `--allowed-tools` argv. This spec moves no
  code and touches no argv. The real risk in scope is **documentary**: the boundary's *justification*
  lives in the comments being rewritten, and a reader who loses it may later "simplify" the
  `settingsWrite` call away, reopening pyrycode#1387. That is why AC 4's substance is enumerated as
  seven checkable points in § Design and backed by the token-floor table in § Testing strategy — a
  deterministic net under a prose obligation, which is different fabric from the prose judgement it
  backs.
- [Subprocess / external command execution] No findings. `buildStreamRunnerClaudeArgs` and its
  `streamrunner.BuildClaudeArgs` call are untouched; AC 5 forbids any change to a statement or string
  literal, so the emitted argv — including `--dangerously-skip-permissions` and `--settings` — is
  byte-identical. `TestBuildStreamRunnerClaudeArgs_Shape` would fail otherwise.
- [Tokens, secrets, credentials] Not applicable — no credential, token, or key is named, read, or
  written by this change. The settings file is a permissions document, not a secret; its lifecycle
  (`defer os.Remove(settingsPath)`) is unchanged.
- [File operations] Not applicable by construction — the spec prescribes no `os.` call, no path
  construction, and no file mode. The existing temp-file write and its removal are untouched.
- [Error messages, logs, telemetry] Not applicable — no error string or log call is in the diff; AC 5
  excludes string literals from the permitted diff shape.
- [Concurrency] Not applicable — no goroutine, lock, or shutdown path is described or altered.
- [Cryptographic primitives], [Network & I/O] Not applicable — neither surface is reachable from this
  file's comment text.
- [Threat model alignment] The relevant threat is the one pyrycode#1387 recorded: a dispatched agent
  obtaining tools the dispatcher deliberately excluded. This spec's obligation to that threat is to
  keep its record legible at the enforcement site, which AC 4 and the token floor gate. Explicitly out
  of scope and named for follow-up: `docs/knowledge/features/pyry-agent-run-command.md` still
  describes the deleted second path as the *default* and calls this path "legacy", including in its
  `--disallowed-tools` paragraph — a maintainer reading it would conclude the settings file is not
  this path's tool gate, which is the same misconception in a doc the developer may not edit.
  Documentation-phase or sibling-ticket owned.
- [SHOULD FIX — replacement-wording risk] The one way this change can weaken security is by filling
  site 3's mandatory two lines with padding, or by asserting a mechanism nobody measured (e.g. a new
  claim about *when* `permissions.deny` is consulted). § Design constrains site 3 to the `#411`
  deny-list fact, which is already documented. Code review should read the two replacement lines
  against that constraint rather than only running the greps.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-20
