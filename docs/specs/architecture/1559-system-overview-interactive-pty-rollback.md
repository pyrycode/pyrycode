# #1559 — system-overview says `interactive_runner: "pty"` selects a path; it now aborts startup

**Size: XS** (PO sized `s`; overridden downward). One file edited:
`docs/knowledge/architecture/system-overview.md`. Zero production source files
created or modified, zero test files, zero new exported types, zero consumer call
sites, five acceptance criteria, zero state-machine reject branches.

The work is six edits inside one section plus one table row, and a verification
sweep whose every step is a one-line command. The sweep is the expensive half of
the ticket, and § "The AC-5 sweep" below hands it over pre-built.

---

## Files to read first

The deliverable is one markdown file. Everything else here is read to *verify* a
claim before writing it, or to avoid a trap.

- `docs/knowledge/architecture/system-overview.md` § **Data Flow** → **Interactive
  Session** (the fenced diagram through the agent-pipeline paragraph) and the
  `pyrycode/tui-driver` + `creack/pty` rows of § **Dependencies** — **the only
  regions you may edit.** § Restart Cycle, § Fast-crash self-heal and § Key Types
  belong to #1560 / #1561 and still describe the deleted `internal/supervisor`;
  leaving them alone is correct, not an oversight.
- `cmd/pyry/main.go` → `selectInteractiveRunner` — the source of truth for AC-2.
  Its `switch` gives you the accepted set verbatim and its doc comment gives you
  why the empty default moved. Read the sibling `selectsStreamRunner` too: it
  records that since #1348 *every valid value including the empty one* selects the
  stream runner.
- `cmd/pyry/agent_run.go` → `runAgentRunStreamRunner` and the comment block at its
  call site inside `runAgentRun` — the source of truth for AC-3. It carries the
  "deliberately NOT read any more, and setting it is harmless" decision and the
  "all five dispatcher forks carry it in their .env" claim you must **attribute**
  rather than assert. Also read this file's `agentRunLongHelp`-adjacent doc string
  ("There is no second runner…"), which is the house phrasing for the same fact.
- `internal/e2e/realclaude/background_reach_probe_test.go` → `reachRunnerPathFromEnv`
  and the `PYRY_USE_STREAMJSON` skip gate inside
  `TestRealClaude_BackgroundReachability` — the two live `os.Getenv` read sites
  that make "the variable is read nowhere" false. Behind the `e2e_realclaude`
  build tag, so `make check` never compiles it; that does not make the reads
  hypothetical.
- `internal/streamsup/runner.go` → `buildArgs`, `Runner.Run`, `Runner.Stdin` — the
  shape of the path that replaces the PTY diagram: `--input-format stream-json
  --output-format stream-json --verbose`, plus `--session-id` on create or
  `--resume` on reattach, with the child's stdin pipe held open across turns.
- `cmd/pyry/streamsup_runner.go` → `newStreamRunnerFactory`, and
  `cmd/pyry/stream_turn_drain.go` → `newStreamTurnSink` — the consumer end of the
  interactive data path (`streamsup.NewParser` → turn events → the single relay-leg
  drain). Needed to make the replacement diagram's right-hand side true.
- `README.md`, the "There is one way to drive claude" paragraph — the model for
  tone and compactness the ticket points at. **Read it for tone, not for text:
  it contains the one sentence AC-3 forbids.** See § "Trap 3" below.
- `docs/knowledge/features/streamsup-package.md` — the package overview for the
  path that replaces the PTY one. Useful for what `streamsup` does and does not do
  (notably: no transcript tailing). Its own opening line still calls `streamsup`
  the "sibling of `internal/supervisor`" — that doc is out of scope and owned by
  the documentation phase; do not fix it here, and do not quote it.
- `docs/specs/architecture/1558-system-overview-tree-nonexistent-paths.md` — the
  merged sibling that fixed this same file's module tree. Its § Context carries the
  ownership note reproduced below; its edit-by-edit shape is the model for this one.
- `docs/specs/architecture/1553-creack-pty-indirect.md` — the merged ticket that
  **already fixed the `creack/pty` row**. Read before touching that row. See
  § "Premise drift" below.

---

## Context

Repo `CLAUDE.md` names `system-overview.md` the architecture authority and tells
every agent to trust it over any other source. Its § Interactive Session tells that
agent that `interactive_runner: "pty"` "selects the code path described here". It
does not: `selectInteractiveRunner` gives `"pty"` its own arm that returns an error,
and the daemon does not start. An agent following the doc hands an operator a config
change that breaks their daemon — the doc's authority is what makes this a defect
rather than untidiness.

The section rotted in **both** directions, which is why the naive fix is wrong three
times over (the ticket enumerates the three; § "Traps" below adds a fourth found
while writing this spec). Deleting every sentence a repo grep cannot resolve would
remove a correct description of tui-driver's `Session` API, and would leave the
`creack/pty` row's true transitive-provenance claim looking like a fabrication.

**No ADR is warranted.** This corrects a description; it decides nothing.

### Ownership note for code review

The pipeline's normal rule is that the developer writes only under `cmd/` and
`internal/`, and the documentation phase owns `docs/knowledge/`. This ticket has no
code surface at all — PO filed it with the `documentation` label and acceptance
criteria that are entirely about the content of
`docs/knowledge/architecture/system-overview.md`. Editing that file **is** the
deliverable; there is no in-lane alternative. The merged sibling #1558 shipped under
exactly this exception, as did #1639. Do not treat the write as out-of-lane.

This is the whole of the exception: **no other file under `docs/knowledge/` may be
touched**, and `docs/knowledge/INDEX.md` stays untouched (this creates no new
document).

---

## Premise drift — verify before you inherit

The ticket body was written against `main` at `2e33ffe`. Three of its factual
claims have moved since. Each was re-verified for this spec against the current
branch point (`663f18e`). **Re-run them yourself before writing; do not inherit
either the ticket's numbers or mine.**

### D1 — the `creack/pty` row is already fixed. Do not edit it.

The ticket describes a row whose purpose claim is `GetsizeFull(os.Stdin)` for the
foreground SIGWINCH watcher, and warns that `go.mod`'s direct-require block will
mislead you. **#1553 landed since and rewrote that row.** At the branch point it
reads:

> PTY allocation inside `pyrycode/tui-driver`'s hosted session — this module has
> made no direct call since #1348 deleted both terminal-driving claude paths;
> reclassified `// indirect` in `go.mod` by #1553

Both halves of AC-4's `creack/pty` clause therefore already hold: the
transitive-provenance claim is kept (and strengthened — `go.mod` now carries
`github.com/creack/pty v1.1.24 // indirect`, so the instrument the ticket warned
against no longer misleads), and the dangling `GetsizeFull` purpose claim is gone.

**Your job on this row is to verify and report, not to edit.** Re-editing it risks
regressing #1553's correction into a fresh falsehood. Record the two checks in the
PR body (rows C1/C2 of the sweep table) and move on. If your re-verification
disagrees with the above, that is a finding — say so in the PR body rather than
silently writing a third version.

### D2 — the tui-driver consumer list is partly wrong

The ticket names five "production consumers": `cmd/pyry/relay.go`,
`cmd/pyry/modal_resolve_v2.go`, `cmd/pyry/stream_turn_busy.go`,
`internal/modalbridge/modal.go`, `internal/protocol/interactive.go`.

**Three of those five do not import tui-driver.** `cmd/pyry/relay.go`,
`cmd/pyry/stream_turn_busy.go` and `internal/protocol/interactive.go` mention it
only in *comments* — the same comments-pass-a-naive-grep trap the ticket itself
flags for `ptyrunner`, reappearing inside the ticket's own evidence. The two that
genuinely import it are `cmd/pyry/modal_resolve_v2.go` and
`internal/modalbridge/modal.go`. AC-4 says "at least one real surviving production
consumer from the list in Context" — satisfiable, but **only from those two**.

The count is also wrong. Re-measured at the branch point: **14** files import
tui-driver; **38** mention it in any form; neither is 18. Do not write "18", and
prefer naming consumers over quoting any count — a count rots on the next import.

### D3 — nothing in pyrycode holds a `tuidriver.Session` any more

This is the substantive discovery, and it changes what the tui-driver row's Purpose
column should say. Measured at the branch point:

- `tuidriver.Spawn` — one occurrence repo-wide, inside a comment in a realclaude
  test. No call site.
- `AttachInput` and `MirrorOutput` — **zero** occurrences in any `.go` file.
- The `tuidriver.Session` *type* — zero references. (Watch the prefix trap: a grep
  for `tuidriver.Session` returns seven hits, and every one of them is
  `tuidriver.SessionJSONLPath`, a package-level function, not the type.)

What production actually consumes is the module's **JSONL vocabulary and modal
classification**, not its screen hosting. The four non-test production importers
and the symbols they use:

| Importer | tui-driver symbols used |
|---|---|
| `cmd/pyry/modal_resolve_v2.go` | `ModalClassPermission` |
| `internal/modalbridge/modal.go` | `ModalClass`, `ModalClassPermission`, `ModalClassTrustFolder` |
| `internal/agentrun/budget/budget.go` | `JSONLEntry`, `TailJSONL`, `IsEndTurn` |
| `internal/agentrun/streamjson/emitter.go` | `JSONLEntry`, `AssistantText`, `IsEndTurn` |

(`internal/e2e/realclaude/fixtures.go` also imports it, for `SessionJSONLPath`, but
it is test infrastructure behind the `e2e_realclaude` tag — not a production
consumer, and naming it as one would not satisfy AC-4.)

This is the "different job" the ticket's Context says the dependency survived #1348
with. E5 below turns it into the row's corrected Purpose.

---

## Design

Six edits. Five rewrite prose in § Interactive Session and one table row; the sixth
is a verification with no edit. Nothing outside those regions moves.

### E1 — the diagram and the raw-mode/SIGWINCH sentence (AC-1)

The fenced `User terminal … PTY master fd … claude` block and the single sentence
beneath it ("The supervisor puts the controlling terminal into raw mode… SIGWINCH
signals are forwarded to the PTY…") both go. The sentence is **deleted, not
downgraded**: AC-1 fails any surviving phrasing that leaves the PTY path merely
non-default or non-preferred, because `internal/supervisor` does not exist.

AC-1 does not require a replacement diagram, but § Data Flow's job is to show the
interactive data path and an empty section fails at that. Write one for the path
that actually runs. Its contract:

- It must show pyry writing a stream-json turn envelope to claude's **stdin**,
  held open across turns, and reading claude's stream-json from **stdout**.
- It must not name a PTY, a master fd, raw mode, or a controlling terminal.
- It must not invent a head. The operator's local terminal is no longer the
  interactive head — #1535 removed the attach/resize wire verbs. Anchor the
  diagram on the runner boundary (pyry ↔ claude), and only name what you have
  read in `cmd/pyry/streamsup_runner.go` and `cmd/pyry/stream_turn_drain.go`.

A sketch to verify and adjust, not to paste — check every arrow against
`streamsup.Runner.Run` / `Runner.Stdin` / `newStreamRunnerFactory` before you
commit it:

```
pyry (internal/streamsup)
    │
    ├── user turn ─────> claude stdin   (stream-json envelope; pipe held open across turns)
    │
    └── turn events <─── claude stdout  (stream-json --verbose, parsed by streamsup.NewParser)
```

Spawn argv is the one place worth naming concretely, because it is what makes the
path identifiable: `--input-format stream-json --output-format stream-json
--verbose`, plus `--session-id <uuid>` on create or `--resume <uuid>` on reattach
(`buildArgs`). Keep it to one line under the fence.

### E2 — the "Production path since 2026-07-24" paragraph (AC-1)

It currently says the PTY path is "not the production default, and it is not a
working fallback either". Both halves presuppose the path exists. Replace with a
statement that there is one interactive path. Keep the true, still-useful facts it
carries: `internal/streamsup`, `interactive_runner: "stream-json"`, #1081, and the
2026-07-24 cutover date.

### E3 — the "Do not read `interactive_runner: "pty"` as a rollback switch" paragraph (AC-2)

The whole paragraph is replaced. What must be true of the replacement:

- It states that `interactive_runner: "pty"` is **rejected at daemon startup** —
  the daemon does not start, rather than starting on some other path.
- It names the accepted set as `selectInteractiveRunner` defines it: `""` and
  `"stream-json"`. Both, including the empty one; the empty default moving off the
  terminal runner is the load-bearing half for an operator with an absent config.
- It is worth one clause that `"pty"` gets a *dedicated* error arm rather than
  falling into the unrecognised-value default, because an operator with that key in
  a config file is told the path was deleted rather than that their value is a typo.
  That is the reason the arm exists (`selectInteractiveRunner`'s own doc comment).

Four things must be **gone**, not softened: the `PYRY_PTY_GATE=1` claim, the
`internal/e2e/realclaude/pty_gate_test.go` cite, the 2026-09-01 audit expiry, and
every framing of #1348 as an open decision. #1348 was executed. You may still cite
#1348 as the ticket that *did* the deletion — that is a historical reference, not an
open-decision framing.

The "four live gates failed on clean `main`" measurement is the reason the path was
deleted, not evidence about a path that exists. If you keep it, it must read as
history behind an executed decision. Dropping it entirely is also fine and is
shorter; `README.md` keeps it, so either choice has precedent.

### E4 — the agent-pipeline paragraph (AC-3)

Three separate claims, and AC-3 pins the exact shape of each:

1. **Carried, not read.** The dispatcher forks still set `PYRY_USE_STREAMJSON=1` in
   their `.env`; `pyry agent-run` does not read it; the stream path is the only
   path. The distinction is the point of the sentence — see Trap 3.
2. **Attribution.** This repo contains no dispatcher checkout, so the fork claim
   cannot be verified from here. Attribute it to its in-repo source — the comment
   at the `runAgentRunStreamRunner` call site in `cmd/pyry/agent_run.go` — with
   wording that makes the attribution visible ("per …", "records that …"). Do not
   assert it as independently verified.
3. **`ptyrunner` is gone.** Every sentence describing it as a runnable-but-untested
   alternative goes — the "measured separately and is less clear-cut", the
   2026-07-30 no-turn and 2026-08-06 full-turn probes, and "Treat it as untested
   rather than as either working or dead". The package directory does not exist.

The trailing "tui-driver has no consumer on either production path" belongs to E5's
subject matter and is false (D3); delete it here rather than move it.

### E5 — the `pyrycode/tui-driver` dependency row (AC-4)

The row must stop claiming tui-driver has no production consumer, and must name at
least one real one. Per D2, draw only from `cmd/pyry/modal_resolve_v2.go` and
`internal/modalbridge/modal.go`; `internal/agentrun/budget` and
`internal/agentrun/streamjson` are equally real and equally namable, and give the
row a second, non-modal consumer if you want one.

Gone from the row: the `PYRY_USE_STREAMJSON` claim, the `ptyrunner` reference, the
four-failing-gates claim, and the #1348-open framing.

**Surviving intact: the `Session` API description** — `AttachInput`,
`MirrorOutput`, `Resize`, `Wait`, `Close`. These are real methods on
`tuidriver.Session` at the `v1.12.0` in `go.mod` and must not be deleted as
dangling; a repo-scoped grep is the wrong instrument for them (Trap 2).

There is a real tension here to resolve rather than paper over. AC-4 requires that
sentence to survive, but per D3 nothing in pyrycode constructs a `Session` any
more, so leaving "Hosts claude as a `Session`" as the row's *Purpose* would ship a
fresh misleading claim of exactly the kind this ticket exists to remove. Resolve it
by separating capability from consumption:

- Keep the `Session` sentence as a description of **what the module provides**, and
  say plainly that no pyrycode path constructs one since #1348.
- Make the row's Purpose the consumption that is actually live: claude's **JSONL
  entry vocabulary** (`JSONLEntry`, `TailJSONL`, `IsEndTurn`, `AssistantText`) and
  **modal classification** (`ModalClass`, `ModalClassPermission`,
  `ModalClassTrustFolder`), consumed by the packages in D3's table.

The "Why not stdlib" column ("Single home for all claude screen knowledge… the
substrate seal, now guarding a path nothing runs") needs a light touch: the seal is
still real and still enforced by `cmd/substrate-guard`, but "guarding a path nothing
runs" is the same non-existent-path framing AC-1 rejects. Keep the ADR 025 link.

### E6 — the `creack/pty` row: verify, do not edit

Per D1. Two checks (sweep rows C1/C2), reported in the PR body. No edit.

### What must not move

§ Restart Cycle, § Backoff Strategy, § Fast-crash self-heal, § Key Types
(`supervisor.Config` / `supervisor.Bridge` / `supervisor.Supervisor`), the module
tree, and every other Dependencies row — including `golang.org/x/term`, whose
"Terminal raw mode, state save/restore" purpose has the same smell as the rows in
scope. It is #1560/#1561 territory or nobody's yet. Repeating a stale supervisor
fact outside your two regions is explicitly correct here; widening is the failure
mode, not the fix.

---

## The AC-5 sweep

AC-5 is the criterion most likely to be under-served, because it is the only one
that is work rather than writing. It requires that **every cite still standing in
the two edited regions** be resolved with the instrument matching its *kind*, that
the PR body show command and result per cite, and that the sweep also run against
a **resolving control of each kind** — so an instrument that reports absence
unconditionally is caught rather than trusted.

### Classify first, then instrument

The classification is where this goes wrong, not the running. `GetsizeFull` is
"dangling" and `AttachInput` is "fine", yet both are absent from every `.go` file
in this repo — because one is a claim about a pyrycode *call site* and the other is
a claim about an *external module's API*. Decide which kind each cite is **before**
picking a command.

| Kind | Instrument | Not |
|---|---|---|
| repo file or directory path | `git ls-files <path>` / `ls -d <dir>` | a string grep |
| repo symbol / call site | a declaration or call site in a `.go` file | any occurrence |
| environment variable | an `os.Getenv` / `os.LookupEnv` read site | a mention |
| external module API | `go doc` against the `go.mod` version | a repo grep |

### The controls

Each kind needs a probe that **resolves** as well as one that **dangles**. A check
that reports absence unconditionally passes the dangling case and proves nothing;
the resolving control is what discriminates. Suggested pairs — substitute your own,
but keep one of each polarity per kind:

| Kind | Resolves (control) | Dangles |
|---|---|---|
| path | `internal/streamsup` | `internal/supervisor`, `internal/agentrun/ptyrunner`, `internal/e2e/realclaude/pty_gate_test.go` |
| symbol | `selectInteractiveRunner` | `GetsizeFull` (as a pyrycode call site) |
| env var | `PYRY_CLAUDE_BIN` (read in `cmd/pyry/agent_run.go`) | `PYRY_PTY_GATE` |
| external API | `tuidriver.Session` + its five methods | pick a name absent from `go doc`'s listing |

### Traps, all four confirmed at the branch point

**Trap 1 — `internal/agentrun/ptyrunner` passes a naive `.go` grep.** The directory
is deleted, but `git grep ptyrunner -- '*.go'` returns dozens of hits across
`cmd/pyry`, `internal/agentrun/*`, `internal/streamsup` and `internal/e2e/realclaude`
— all comments — plus genuine string literals in `cmd/substrate-guard/main_test.go`
(e.g. `"internal/agentrun/ptyrunner/helper_test.go"`, a fixture path in a
walk-exclusion test). A grep reports the package alive. `ls -d` and `git ls-files`
report it deleted. Use those. The same holds for `internal/supervisor`.

**Trap 2 — `AttachInput` / `MirrorOutput` fail a repo grep and are still real.**
Zero `.go` occurrences repo-wide; both are live methods on `tuidriver.Session` at
`v1.12.0`. `go doc github.com/pyrycode/tui-driver/pkg/tuidriver.Session` lists all
five of AC-4's methods. Deleting the sentence on grep evidence is the failure this
trap exists to prevent.

**Trap 3 — `README.md` is the model for tone and carries the sentence AC-3
forbids.** The ticket points you at README's "There is one way to drive claude"
paragraph as a model for precision. That paragraph contains "`PYRY_USE_STREAMJSON`
is no longer read" — unqualified, and precisely the claim AC-3 rules out ("It does
not claim the variable is read nowhere"). It is a fair summary in a README, but it
is not the sentence to copy into a doc whose ACs distinguish *carried* from *read
on the production path*. Copy the compactness; write the distinction yourself.
Fixing README is out of scope.

**Trap 4 — `tuidriver.Session` prefix-matches `tuidriver.SessionJSONLPath`.** A
grep for the type returns seven hits, none of which are the type. If you use a repo
grep here at all (you should not — it is kind 4), it will tell you the Session type
is in live use.

One more, not a trap but a scoping note: the two `os.Getenv("PYRY_USE_STREAMJSON")`
sites live in `internal/e2e/realclaude/background_reach_probe_test.go`, behind the
`e2e_realclaude` build tag. `make check` never compiles that package, so a green
`make check` is not evidence either way about those reads. `git grep` sees them
regardless of build tags, which is why the env-var instrument is a grep for a read
site rather than a test run.

### Output

The PR body carries a table with one row per cite: the cite, its kind, the exact
command, the result, and the verdict. Include the control rows, labelled as
controls — a sweep that shows only the dangling cases cannot demonstrate the
instrument discriminates, which is the half of AC-5 that is easy to skip.

---

## Testing strategy

There is no code under test. Verification is the AC-5 sweep plus three checks that
the edit did not overreach:

- `git diff --stat` shows exactly one file changed:
  `docs/knowledge/architecture/system-overview.md`.
- `git diff` hunks fall only inside § Interactive Session and the two Dependencies
  rows. A hunk touching § Key Types, § Restart Cycle or the module tree is scope
  creep; revert it.
- No cite you *added* is a bare `:NNN` or a `file.go:120-140` line range —
  `make cite-guard` has no depth or range exemptions left. Name symbols. This
  applies to prose in a markdown file as much as to code comments: the guard is
  diff-scoped to `//`-comments, so a markdown line will not trip it, but the house
  rule is the same and code review reads for it.

Run `make check` once before committing. It exercises none of this, but a green
gate rules out an accidental stray edit outside the doc.

---

## Open questions

- **How much of the deletion history to keep.** E3 can either carry the
  four-failing-gates measurement as history or drop it. Both satisfy AC-2. The
  ticket does not decide, and `README.md` keeps it while being the shorter
  document. Developer's call — pick one and be consistent between E2 and E3, so the
  section does not state the cutover rationale twice at different lengths.
- **Whether the corrected tui-driver row should name four consumers or one.** AC-4
  requires "at least one". Naming the modal pair and the JSONL pair makes the row's
  new Purpose self-evidencing; naming one keeps the table cell to the length of its
  neighbours. Lean short — this is a dependency table, not a package doc — and let
  the two symbol families carry the specificity.
- **`docs/knowledge/features/streamsup-package.md` opens by calling `streamsup` the
  "sibling of `internal/supervisor`".** Out of scope here and owned by the
  documentation phase. Worth a follow-up ticket; do not file or fix it from this
  branch.
