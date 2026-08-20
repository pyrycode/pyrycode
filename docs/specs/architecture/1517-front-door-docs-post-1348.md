# #1517 — front-door docs still describe the deleted terminal paths

Docs-only. **Three** files change:

| File | ACs |
| --- | --- |
| `CLAUDE.md` | 1, 2, 3 |
| `CODING-STYLE.md` | 4 |
| `docs/knowledge/features/ptyrunner-package.md` | 5 (banner + inversions) |

`docs/knowledge/INDEX.md` is **not** the developer's file — see § "AC 5 is split across two
phases". No Go file is created or modified, no test is added. `make check` is unaffected and is
still the gate the PR must show green.

Sized **S** and comfortably inside every boundary: 0 production source files, ~45 lines of
markdown, 0 new exported types, 0 consumer call sites, 5 ACs, 0 reject branches. No split.

**Branch-overlap check: clean.** `git fetch origin --prune` then a sweep of all 73
`origin/feature/<n>` branches against the three files above found **zero** overlaps. (A sweep
that also included `INDEX.md` flags `origin/feature/58`, a 2026-05-02 branch on an open
meta-issue about branch cleanup. That is stale, and `INDEX.md` is out of the developer's diff
anyway. No `blockedBy` needed — do not re-flag it.)

---

## Files to read first

Read in this order. Everything below was verified against this branch's tip (`f7d7562`) on
2026-08-20. The "what to extract" column is the reason to open it, not a summary you can
substitute for it.

| Path | Symbol / section | What to extract |
| --- | --- | --- |
| `CLAUDE.md` | § *Architecture* fenced block; § *Testing* → "Conventions" bullet list | The three edit sites for ACs 1–3. The block is a fixed-width two-column table — preserve the padding to column 31. |
| `CODING-STYLE.md` | § *Package Layout*, one-package-per-concern bullet | The single edit site for AC 4. Read § *Naming* directly below it too — it also says `supervisor`, and it is deliberately **out of scope** (see § "What not to touch"). |
| `internal/control/server.go` | `Server.handle` | **The authoritative verb list for AC 2.** Read the `switch req.Verb` arms, not the ticket's prose. Find it by symbol; the ticket's `:552-586` range has already shifted. |
| `internal/control/server.go` | `handleLogs`, `handleStop`, `handleSessionsNew`, `handleSessionsRm`, `handleSessionsRename`, `handleSessionsList`, `handleSessionsHasID`, `handleRekey`, `handleApprove` | Nine handlers plus the inline `VerbStatus` arm = ten. The handler set and the switch-arm set agree exactly; that agreement is the cross-check for AC 2. |
| `internal/control/protocol.go` | `VerbStatus`, `VerbLogs`, `VerbStop`, `VerbSessionsNew`, `VerbSessionsRm`, `VerbSessionsRename`, `VerbSessionsList`, `VerbSessionsHasID`, `VerbRekey`, `VerbMCPApprove` | The ten wire strings. Copy the row's verb tokens from these constant values, not from the ticket body. |
| `docs/knowledge/features/jsonl-reconciliation.md` | Title line + the bold lead paragraph | **The banner precedent AC 5 mandates.** Copy the *shape* — title suffix + bold lead saying what was deleted and where the behaviour lives now. Do **not** copy its `[#839](../codebase/839.md)` link form; see § "The banner's link target". |
| `docs/knowledge/features/ptyrunner-package.md` | Title line; § *Out of scope*; § *Related* | The four inversion sites and the banner insertion point. Locate them with the sweep in § "Verification strategy", not by the ticket's line numbers. |
| `docs/knowledge/features/pyry-agent-run-command.md` | § opening / the `#1348` single-path statement | The doc that already records the post-#1348 reality correctly. This is the banner's "where the behaviour lives now" link target. |
| `cmd/pyry/agent_run.go` | `runAgentRunStreamRunner` and the comment block naming `PYRY_USE_STREAMJSON` | Proof the variable is deliberately unread. Its own comment says so. Needed to write the AC-5 correction accurately. |
| `docs/specs/architecture/1560-system-overview-restart-backoff-selfheal.md` | whole file | The house pattern for this ticket family (#1553/#1555/#1558/#1559/#1560, all merged 2026-08-20). Same shape: verified-facts table, symbol cites, scoped sweep with controls in the PR body. |

**Cite by symbol name or section heading, never by line number.** `make cite-guard` fails a
`//`-comment citation that resolves into a declaration, and this ticket demonstrates the rot
first-hand: the body's `internal/control/protocol.go:35`, `server.go:506/:524/:581` and
`client.go:80` cites all point at symbols that **no longer exist**, and its `go.mod:7` is
actually line 17. Markdown prose is not gated by `cite-guard`, but the same rot applies.

---

## Context

`CLAUDE.md` and `CODING-STYLE.md` are the first two files a headless agent loads, and
`ptyrunner-package.md` is reachable from either via QMD. All three still present the pre-#1348
world, so their drift propagates into every plan built on top of them. #1348 (`58524e9`,
2026-08-16) deleted the terminal-driving runner; `internal/supervisor` and
`internal/agentrun/ptyrunner` are both gone from disk.

This is the sixth ticket in a family — #1553, #1555, #1558, #1559 and #1560 all landed on
2026-08-20 correcting the same deletion's fallout in `system-overview.md`,
`pyry-agent-run-command.md` and `go.mod`. **No ADR is warranted.** These are docs catching up
to a deletion that already has its own record; there is no new decision. The documentation
phase should not open one.

---

## Three of the ticket's premises are wrong. Read this before editing anything.

Each was re-verified on this tip. Two of them change what the developer must write.

### 1. `VerbAttach` and `VerbResize` no longer exist — anywhere

The body says `VerbAttach` "is still declared" and `VerbResize` "is still *sent* by
`client.go:80` with no server arm to receive it," and its Non-Goals reserve that asymmetry for
a sibling code ticket.

Verified: `git grep -n 'VerbAttach\|VerbResize' -- '*.go'` returns **zero** lines, against a
positive control (`VerbStatus` → five files) proving the sweep reports presence. `handleAttach`
does not exist either. The ten switch arms and the ten handler methods correspond one-to-one
with no gaps.

**Consequence:** AC 2's "does not name `attach`" is trivially satisfiable, and the Non-Goals'
reserved code ticket has nothing left to reconcile. `attach` survives in `internal/control`
only as stale *comments* (`protocol.go`'s package doc, `Server`'s field comments) — do not be
misled by those into thinking a verb exists. Fixing those comments is out of scope; note it in
the PR body as sibling material.

### 2. Nothing in this repo imports `creack/pty` — so AC 3's option (b) would ship a new false claim

This is the load-bearing correction. The body says
`internal/e2e/realclaude/finding_live_run_test.go` "is the only file in the repo importing
`github.com/creack/pty`."

It is not. That file's import block is `os`, `path/filepath`, `syscall`, `testing`, `time`. Its
sole `creack/pty` hit is inside a **comment**, and its sole `tuidriver.Spawn` hit is likewise a
comment describing the deleted `ptyrunner.Run`. Two independent methods agree:

- `git grep -n 'creack/pty' -- '*.go'` → one line, a comment.
- `go mod why github.com/creack/pty` → `cmd/pyry → tui-driver/pkg/tuidriver → creack/pty`, i.e.
  transitive-only. That is why go.mod marks it `// indirect` (done by #1553 on 2026-08-20).

Pyrycode's own use of tui-driver is **type-only** — `tuidriver.JSONLEntry`, `ModalClass*`,
`Render`, `TailJSONL`, `IsEndTurn`. No call site allocates a PTY.

**Consequence:** AC 3 offers two options — remove the bullet, or reword it to say "the only
PTY-dependent test is the opt-in `internal/e2e/realclaude` suite." **Option (b) is falsified:
that suite has no PTY-dependent test either.** Writing it would replace one stale claim with a
newer one — exactly the failure this ticket exists to fix.

**Take option (a): delete the bullet.** This satisfies AC 3's text literally ("Either it is
removed, or…") and is the only option that does not assert something false.

*(Same failure mode as #1560, where the AC's prescribed replacement wording had been falsified
by a merge that landed after filing. Verify the fix text, not just the defect.)*

### 3. The rollback-knob inversion appears **four** times, not two

AC 5 names lines 5 and 276. A sweep of the claim's vocabulary — not one needle — finds four
sites in `ptyrunner-package.md`:

| Site | Current text (abridged) | Why it is inverted |
| --- | --- | --- |
| Title paragraph (line 5) | "`streamrunner` is retained as a `PYRY_USE_STREAMJSON=1` rollback knob" | Backwards: `streamrunner` is the only path; `ptyrunner` is deleted. |
| § *Out of scope* (line 271) | "Streamrunner deletion → not planned… streamrunner stays as a sibling indefinitely… selected via `PYRY_USE_STREAMJSON=1`" | **`ptyrunner` was deleted, not `streamrunner`.** And nothing "selects" via the env var. |
| § *Related* (line 276) | "The two coexist post-#470 under the `PYRY_USE_STREAMJSON` rollback knob." | They do not coexist. |
| § *Related* (line 277) | "Post-#470 wired to `ptyrunner` by default; falls back to `streamrunner` when `PYRY_USE_STREAMJSON=1`." | Both halves false since #1348. |

**Fix all four.** AC 5's literal minimum is two, but leaving two live present-tense inversions
sitting three lines below a banner that declares the document historical directly contradicts
the banner — the same AC's other clause. All four are inside the one in-scope file; none is
touched by the Non-Goals. Line numbers are given for orientation only; locate them with the
sweep in § "Verification strategy".

---

## Design

### `CLAUDE.md`

**AC 1 — drop the `internal/supervisor` row.** Delete the whole line from the § *Architecture*
fenced block. `ls internal/supervisor` → no such directory; there is nothing to re-point it at.
The two prose uses of the word "supervisor" — the product description in § *Project* and
working principle 5 in § *Working Principles* — describe the product, not the package. **Both
stay**, per the AC's own parenthetical. After the edit,
`grep -c 'internal/supervisor' CLAUDE.md` must read `0` while `grep -ci supervisor CLAUDE.md`
still reads `2`.

**AC 2 — re-derive the `internal/control/` row from `Server.handle`.** The row currently reads
`Unix-socket control plane (status, logs, attach, stop)`: it names a verb that does not exist
and misses six that do. The switch dispatches ten verbs — `status`, `logs`, `stop`,
`sessions.new`, `sessions.rm`, `sessions.rename`, `sessions.list`, `sessions.has-id`, `rekey`,
`mcp.approve`. Collapsing the five `sessions.*` verbs into one token is explicitly permitted,
which gives six tokens:

```
internal/control/              Unix-socket control plane (status, logs, stop, sessions.*, rekey, mcp.approve)
```

Preserve the block's fixed-width alignment (description starts at column 31). The resulting
line is shorter than the existing `internal/agentrun/` row, so nothing else needs reflowing.
**Re-read the constants in `internal/control/protocol.go` and copy the verb strings from
there** — do not transcribe them from this spec or from the ticket body.

**AC 3 — delete the PTY testing bullet.** Remove
`- Tests for PTY-dependent code must handle non-TTY environments (CI runners have no terminal)`
from the "Conventions" list in § *Testing*. Per § "Three of the ticket's premises are wrong"
point 2, no rewording is truthful. The two bullets above it (`Unit tests`, `No mocking
frameworks`) stay unchanged.

**What not to touch in `CLAUDE.md`.** § *Architecture*'s "`internal/` holds ~33 packages" —
`internal/` currently holds 31, and the tilde tolerates that. Removing one row does not make it
wrong. Leave it. Working principle 2's "(like `creack/pty`)" is a stdlib-vs-dependency
illustration, not an inventory claim; leave it (sibling material, see § "Open questions").

### `CODING-STYLE.md`

**AC 4 — one-package-per-concern bullet.** It currently reads:

> One package per concern. `internal/supervisor` owns process lifecycle, `internal/control`
> (future) will own the Unix socket.

Both defects in one sentence: the first package does not exist, and the second is annotated
"(future)" though it has shipped. Replace both halves with packages that exist and are
described in the present tense — recommended:

> One package per concern. `internal/streamsup` owns the claude process lifecycle,
> `internal/control` owns the Unix control socket.

`ls internal/streamsup` and `ls internal/control` both succeed; that is exactly the AC's stated
verification. `internal/streamsup` is the right substitute because it is the package that
inherited the supervise/restart loop — `Runner.Run`, per `CLAUDE.md`'s "Stream-json supervision
(production interactive path)". Any other existing-package pair that illustrates the rule is
acceptable; the AC constrains existence and tense, not the specific names.

**What not to touch in `CODING-STYLE.md`.** These are adjacent and tempting; leave every one:

- § *Naming* — "`supervisor`, not `process_supervisor`" and "`supervisor.New()`, not
  `supervisor.NewSupervisor()`". These illustrate *naming form*. A reader takes `supervisor`
  there as a hypothetical package name, not as a claim this repo has one. AC 4 scopes to the
  one-package-per-concern bullet only.
- § *Naming* — `PTY` in the acronym-capitalisation list. It is an acronym example.
- § *Error Handling* — `fmt.Errorf("pty start: %w", err)` and the `ptmx.Close()` comment.
  Illustrative snippets.
- § *Dependencies* — the "Current justified deps" list naming `creack/pty` and
  `golang.org/x/term`. Both are arguably stale now (`creack/pty` is `// indirect` since #1553;
  `golang.org/x/term` has exactly one importer, `internal/e2e/internal/fakeclaude`), but no AC
  covers this list and #1553 already made the deliberate call to fix only the two claims it
  named. Note it in the PR body as sibling material; do not edit it.

### `docs/knowledge/features/ptyrunner-package.md`

**AC 5, part one — the deletion banner.** Follow `jsonl-reconciliation.md`'s shape: title
suffixed with the retirement, then a **bold** lead paragraph stating what was deleted and where
the behaviour lives now. Contract, not prose to copy:

- Title becomes `# \`internal/agentrun/ptyrunner\` — interactive-TUI claude spawn primitive (deleted 2026-08-16, #1348)`.
- A new bold lead paragraph directly under it, before the existing line-5 paragraph, saying:
  (a) the package was deleted in #1348; (b) `internal/agentrun/streamrunner` is the only
  `pyry agent-run` path now; (c) this document is **historical reference only** and describes
  code that is no longer in the tree.
- The existing line-5 history paragraph stays — reframed as history by the banner above it,
  minus its inverted final clause (below).

Deletion date is **2026-08-16**, from `58524e9`
(`refactor(agent-run): delete the terminal-driving runner, keep its guarantees (#1348)`).

**The banner's link target — do not copy the precedent's link form.** `jsonl-reconciliation.md`
links `[#839](../codebase/839.md)`. There is **no `docs/knowledge/codebase/1348.md`** and no
`docs/specs/architecture/1348-*.md`; that link would be dead on arrival. Point the "where the
behaviour lives now" clause at
[`pyry-agent-run-command.md`](../../knowledge/features/pyry-agent-run-command.md)
(relative link `pyry-agent-run-command.md` from inside `features/`), which already records the
single-path reality correctly. `streamrunner-package.md` is the right *package* to name in
prose, but **it still carries the mirror-image inversion and is out of scope** (Non-Goals) — so
name the package, and do not lean on that doc as evidence of current behaviour.

**AC 5, part two — the four inversions.** Correct all four sites from § "Three of the ticket's
premises are wrong" point 3 to the post-#1348 truth: `streamrunner` is the only path, and
`PYRY_USE_STREAMJSON` is **deliberately not read** — confirmed by `runAgentRunStreamRunner`'s
own comment in `cmd/pyry/agent_run.go`, which says so in as many words, and pinned by
`TestRunAgentRun_SelectorVariableNoLongerDecides`. Watch the § *Out of scope* site especially:
it says *streamrunner* deletion was not planned, when the package actually deleted was
*ptyrunner*. Reversing the subject is the fix, not softening the tense.

Everything else in the file is a historical description of deleted code and is correct as
history under the banner. Leave it. In particular the § *Testing* CI note and the § *Dependency
direction* section were **already corrected by #1553 on 2026-08-20** — re-read them before
touching anything near them.

### AC 5 is split across two phases

AC 5's final clause — the `docs/knowledge/INDEX.md` row for the doc reading in past tense — is
**not the developer's deliverable**, per the ticket's own Technical Note and `CLAUDE.md`'s rule
that the documentation phase is INDEX.md's sole writer. #1560 drew the same boundary and shipped
cleanly.

**Developer: do not open `docs/knowledge/INDEX.md`.** Instead, put the required change in the PR
body so the documentation phase can apply it verbatim: the `ptyrunner-package.md` row
(currently "…Stateless `Run(ctx, Config) error` spawns claude under tui-driver…") needs its
present-tense verbs moved to past tense and a "deleted in #1348" marker, matching the row for
`pyry-agent-run-command.md` which already reads correctly.

---

## Concurrency model

Not applicable — no code changes, no goroutines, no shutdown sequence.

## Error handling

Not applicable — no failure modes introduced. The only behaviour under discussion is the absence
of one: nothing in this module allocates a PTY, and saying that plainly (by deleting the claim
that implies otherwise) is the deliverable.

---

## Verification strategy

There is no test to write. Verification is a set of scoped sweeps, and they belong **in the PR
body** with controls in both directions.

### Per-AC checks

```bash
# AC 1 — row gone, prose kept
grep -c 'internal/supervisor' CLAUDE.md          # expect 0
grep -ci  'supervisor'         CLAUDE.md          # expect 2  (product prose only)
ls internal/supervisor                            # expect: No such file or directory

# AC 2 — row verbs vs dispatch arms, both directions
grep -n 'internal/control/' CLAUDE.md
grep -nE 'Verb[A-Za-z]+ +Verb += +"' internal/control/protocol.go   # the ten wire strings
grep -c  'attach' CLAUDE.md                       # expect 0

# AC 3 — bullet gone
grep -ci 'PTY-dependent' CLAUDE.md                # expect 0

# AC 4 — both named packages exist on disk, no "(future)"
ls internal/streamsup && ls internal/control      # both must succeed
grep -n '(future)' CODING-STYLE.md                # expect 0 in the Package Layout bullet

# AC 5 — banner present, all four inversions gone
grep -n '1348' docs/knowledge/features/ptyrunner-package.md          # expect >=1 (was 0)
grep -n 'PYRY_USE_STREAMJSON' docs/knowledge/features/ptyrunner-package.md   # expect 4 sites, all corrected
grep -niE 'rollback|coexist|by default|indefinitely' docs/knowledge/features/ptyrunner-package.md
```

### The AC-2 cross-check, with both controls

A bare "does the row name real verbs?" check passes the current, wrong row — `status`, `logs`
and `stop` are all real. The check must run **both directions**: every verb the row names has a
`case` arm, *and* no arm's verb is missing from the row.

```bash
# every dispatched verb (read the switch arms in Server.handle, then confirm the strings)
git grep -oE '"(status|logs|stop|sessions\.[a-z-]+|rekey|mcp\.approve)"' internal/control/protocol.go | sort -u
```

Expected ten: `status`, `logs`, `stop`, `sessions.new`, `sessions.rm`, `sessions.rename`,
`sessions.list`, `sessions.has-id`, `rekey`, `mcp.approve`. Re-run it; do not paste these
without running.

**Controls to include in the PR body:**

- **Positive** — `mcp.approve` resolves to a `case VerbMCPApprove` arm *and* a `handleApprove`
  method. It is the verb the ticket's own proposed list dropped, so it proves the sweep can
  report presence the naive list missed.
- **Negative** — `git grep -n 'VerbAttach\|VerbResize' -- '*.go'` returns zero, paired with
  `git grep -c 'VerbStatus' -- '*.go'` returning five files. Zero alone is indistinguishable
  from a broken sweep; the `VerbStatus` control is what makes the zero mean something.

### The AC-3 evidence

Two independent methods, both in the PR body:

```bash
git grep -n 'creack/pty' -- '*.go'                # one hit, and it is a COMMENT
go mod why github.com/creack/pty                  # transitive: cmd/pyry -> tui-driver -> creack/pty
```

Do not run a bare `grep -rn pty internal/` — it false-hits on the substring in "em**pty**"
(e.g. `internal/sessions/pool_create_test.go`), which is the trap the ticket body warns about.

### Shell hazards that make a sweep silently lie

- **`git grep -E '\bfoo\b'` matches nothing.** POSIX ERE has no `\b`, and git reports the empty
  result as a clean exit — indistinguishable from real absence. Use `-F`, `-P`, or the Grep tool.
- **Under `zsh`, `git grep -- $FILES` does not word-split** an unquoted variable holding a path
  list; it passes one bogus pathspec, yielding 0 hits and exit 0. Quote each pathspec, or run in
  `bash`.
- `git grep -c` prints `path:count` per file; a multi-file symbol needs summing, not the first
  line.

### Final check before pushing

Re-read the three edited files and confirm every package and symbol named in them resolves on
disk. That, not the scripts, is what the ACs are asking for — the scripts are the evidence.

---

## Open questions

1. **AC 3, option (a) vs (b).** The spec directs option (a) — delete the bullet — because
   option (b)'s prescribed wording is falsified (§ "premises are wrong", point 2). This is not
   a developer judgment call; taking (b) ships a false claim. If the developer disagrees after
   re-running the two `creack/pty` checks, say so in the PR body rather than writing (b).
2. **Four inversions vs the AC's two.** The spec directs fixing all four. If turn budget
   somehow forces a cut, the two the AC names (title paragraph, § *Related* "coexist") are the
   floor — but the § *Out of scope* site is the most actively wrong of the four, since it names
   the wrong package as deleted. Prefer it over a § *Related* line if only three can land.
3. **Sibling material for PO, to be listed in the PR body, all out of scope here:**
   - `docs/knowledge/features/streamrunner-package.md` carries the mirror-image inversion
     ("#470 cut the verb's default back to `ptyrunner`"), and `docs/knowledge/INDEX.md`
     describes `streamrunner` as a sibling of `agentrun.Drive`, a function that no longer
     exists. Both named in this ticket's own Non-Goals.
   - `internal/control` comments still describe an `attach` verb that has no constant, no arm
     and no handler (`protocol.go` package doc, `Server` field comments). The ticket's Non-Goals
     reserved a code ticket for the `VerbAttach`/`VerbResize` asymmetry — **that ticket is now
     moot**; the residue is comment-only.
   - `CODING-STYLE.md` § *Dependencies* still lists `creack/pty` (now `// indirect`) and
     `golang.org/x/term` (one importer, a test-only fake) as "current justified deps".
   - `cmd/pyry/agent_run.go` comments still name `ptyrunner` and `supervisor.runOnce`, and
     `internal/sessions/pool_cap_test.go` names `supervisor.runOnce`. Named in this ticket's
     Non-Goals.
4. **Ticket-body line numbers are stale and must not be trusted.** `protocol.go:35`,
   `server.go:506/:524/:581`, `client.go:80` point at symbols that no longer exist; `go.mod:7`
   is line 17. Locate every edit site by heading or symbol.
