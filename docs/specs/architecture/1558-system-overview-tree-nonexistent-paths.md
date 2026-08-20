# #1558 — system-overview's module tree names paths that do not exist

**Size:** XS. Single file edited: `docs/knowledge/architecture/system-overview.md`.
No production source file is created or modified; no test file either.

## Files to read first

The deliverable is one markdown file; everything else on this list is read to
*verify* a claim before writing it.

- `docs/knowledge/architecture/system-overview.md` § "Module Structure" (the fenced
  tree) and the single "Dependency direction:" paragraph directly beneath it — **the
  only region you may edit.** Everything from § "Data Flow" downward belongs to
  #1559 / #1560 / #1561; leaving `internal/supervisor` standing there is correct.
- `internal/sessions/runner.go` → `Runner`, `RunnerFactory`, `RunnerConfig` — the
  abstraction `internal/sessions` depends on instead of a concrete runner. `Runner`'s
  own doc comment already records that #1348 deleted `internal/supervisor` and that
  the one production implementation is `cmd/pyry`'s `streamRunner` adapter; that
  sentence is the source of truth for the corrected dependency-direction paragraph.
  (This also moots the ticket's out-of-scope note claiming this package doc still
  names `*supervisor.Supervisor` — it does not, at `main`.)
- `cmd/pyry/main.go` → `selectInteractiveRunner`, `runSupervisor` — what the binary
  entry point actually does now: parse flags, pick a `sessions.RunnerFactory`, build
  the daemon. Extract a replacement for the entry's "supervisor init" clause.
- `internal/sessions/session.go` → the `Session` struct's `sup` field and the
  `Runner` method — the Session holds a `Runner`, not a supervisor and not a bridge.
  Extract the replacement wording for that tree entry.
- `internal/control/` → `server.go` (`Server`, `NewServer`, the consumer-declared
  `Session` / `SessionResolver` / `Remover` / `Renamer` / `Lister` / `GetOrCreator` /
  `Rekeyer` / `Sessioner` interfaces), `protocol.go` (package doc, `Verb`, `Request`,
  `Response`, `ErrorCode`, `JSONLPolicy`), `client.go` (`Status`, `Logs`, `Stop`,
  `SessionsNew`, `SessionsRm`, `SessionsRename`, `SessionsList`, `SessionsHasID`,
  `Rekey`, `Approve`, `DialTimeout`), `dial.go` (`dialWithRetry`,
  `isTransientStartupError`), `logs.go` (`LogProvider`, `RingBuffer`, `SlogTee`) —
  one line of true description per file, for the five entries AC-2 requires.
- `docs/knowledge/features/control-plane.md` — the package overview. #1535 deleted
  the attach/resize wire verbs; read it before describing the control block so the
  tree and the overview agree.
- `CLAUDE.md` § Architecture — already describes `cmd/pyry/` as "Binary entry point:
  CLI parsing, daemon composition root". Prefer that existing house phrasing over
  inventing new wording for the `main.go` entry.

## Context

Repo `CLAUDE.md` names this file the architecture authority and tells every agent to
trust it over the shorter list in `CLAUDE.md` itself. Its module tree still advertises
a package `#1348` deleted, and the paragraph beneath the tree ships a verification
command that cannot run. An agent following either lands on nothing.

**Ownership note for code review.** The pipeline's normal rule is that the developer
writes only under `cmd/` and `internal/`, and the documentation phase owns
`docs/knowledge/`. This ticket has no code surface at all — PO filed it (`documentation`
label, `size:xs`) with acceptance criteria that are entirely about the content of
`docs/knowledge/architecture/system-overview.md`, and its three siblings #1559 / #1560 /
#1561 are the same shape. Editing that file **is** the deliverable here; there is no
in-lane alternative, and the same thing happened on #1639, where the implementing
commit edited three `docs/knowledge/features/*.md` files because the ACs named them.
Do not treat the write as an out-of-lane edit. This is the whole of the exception:
no other file under `docs/knowledge/` may be touched, and `docs/knowledge/INDEX.md`
stays untouched (this creates no new document).

No ADR is warranted.

## Design

Six edits, all inside the fenced tree plus the one paragraph below it. A tree-wide
existence sweep run for this spec against `main` — every path the tree claims,
`ls`-checked — resolves **74** entries and fails on exactly the five paths behind the
ticket's four rows. Nothing else in the tree is a dead path, so resist widening.

### E1 — delete the `internal/supervisor/` block

Four consecutive lines (the directory plus `supervisor.go`, `backoff.go`,
`winsize.go`) go away entirely. Do not substitute `internal/streamsup/` in its place:
adding tree entries for the packages the tree omits (`internal/streamsup`,
`internal/agentrun`, `internal/transcript`, `internal/sessions/runner.go`, the ~28
further `cmd/pyry/*.go` files) is a different ticket. Absence is not falsehood.

Fix the box-drawing continuation of the line that follows, so the tree still renders.

### E2 — delete the `cmd/pyry/acp.go` entry

Same commit `8832ca6` deleted it. `cmd/pyry/main.go` becomes the block's last child;
its connector changes from `├──` to `└──`.

### E3 — drop the second cite of that file from the `internal/acp/` block header

The header reads "…served by `cmd/pyry/acp.go`, #756". The package `internal/acp`
still exists and its three files all resolve — only the "served by" clause names a
deleted file. Remove the clause (or say the serving entry point was deleted with
#1348), keep the rest of the block.

### E4 — replace the `internal/control/attach.go` entry with the package's real files

`attach.go` never existed under that name; the real attach files
(`attach_client.go`, `attach_stdio_client.go`, and friends) were deleted by
`8832ca6`, and #1535 removed the attach/resize wire verbs. AC-2 fixes the target
shape: the block lists exactly the package's five non-test files —
`client.go`, `dial.go`, `logs.go`, `protocol.go`, `server.go` — and no entry in it
claims an attach handoff to a supervisor bridge.

Write one true line per file from the symbols in the reading list. Keep each to the
one-line density the surrounding tree uses; the block is a map, not a package doc.

Note the trap: `internal/control` source still carries `handleAttach`-flavoured
*comments*. Those are code, out of scope, and are not evidence that an attach entry
belongs in the tree.

### E5 — the two stale descriptions

- `cmd/pyry/main.go`: "CLI parsing, signal setup, supervisor init" — the last clause
  names the deleted package. `CLAUDE.md`'s "CLI parsing, daemon composition root" is
  the house phrasing; reuse it rather than invent one.
- `internal/sessions/session.go`: "Session: wraps one supervisor + optional bridge"
  — both halves are false. `git grep "type Bridge struct" -- '*.go'` returns zero
  definitions repo-wide (re-verified for this spec at `main`), and the Session holds
  a `sup Runner`. Say it holds a `Runner`; keep the rest of that entry (lifecycle
  goroutine, active↔evicted state machine, idle timer, Activate / Run / Attach)
  unchanged — it is still true.

`internal/sessions/pool.go`'s entry mentions a `supervise()` fan-out seam. That is
**not** a stale supervisor reference: `(*Pool).supervise` exists and has three
production call sites. Leave it alone.

### E6 — the dependency-direction paragraph

The paragraph's surviving clauses are verified true at `main` and must survive
verbatim in substance: `internal/control` does import `internal/sessions`, and
`internal/sessions/rotation` has no back-edge. Only the supervisor hop and the
`go list` recipe are false.

**Do not write a chain.** Measured at `main` for this spec:

| edge | result |
|---|---|
| `internal/sessions` direct imports (pyrycode) | `internal/conversations`, `internal/sessions/rotation` — and nothing else |
| `internal/sessions/...` transitive deps reaching `internal/streamsup` | none |
| packages importing `internal/streamsup` | `cmd/pyry` only |
| `internal/sessions/rotation` imports | `internal/transcript` only |
| `internal/control` imports | `internal/permbridge`, `internal/sessions` |

So the shape is an **inversion**: `internal/sessions` declares `Runner` and takes a
`Config.RunnerFactory`; `cmd/pyry` is the only package that knows the concrete
`internal/streamsup` runner and hands it down. Substituting `internal/streamsup` for
`internal/supervisor` in the old arrow ships a fresh falsehood — AC-3 says so
explicitly.

Note one drift from the ticket body: it lists `internal/transcript` among
`internal/sessions`'s imports. At `main` that edge is **transitive only**, through
`internal/sessions/rotation`. Re-measure at your base commit rather than quoting
either the ticket or this table.

Target shape for the replacement (adapt the wording; the load-bearing parts are the
inversion, the two surviving clauses, and a recipe that runs):

> Dependency direction: `cmd/pyry → internal/sessions`, with the concrete runner
> supplied *downward* rather than imported *upward* — `internal/sessions` declares the
> narrow `Runner` interface and takes a `Config.RunnerFactory`; only `cmd/pyry` imports
> `internal/streamsup` and hands the concrete runner in. `internal/control` imports
> `internal/sessions` for the `SessionID` type referenced by its `SessionResolver`
> interface. `internal/sessions/rotation` is downstream of `internal/sessions` (no
> back-edge — the contract is closures over primitive types so the rotation package
> never imports its host). Both edges are checkable:
>
> ```sh
> # the edge that exists — exits 0
> go list -deps ./internal/control | grep -qx github.com/pyrycode/pyrycode/internal/sessions
> # the edge that does not — exits 0
> ! go list -deps ./internal/sessions/... | grep -qx github.com/pyrycode/pyrycode/internal/streamsup
> ```

## Concurrency model

None. Prose edit to one markdown file.

## Error handling

None. The one runtime artifact is the shipped verification recipe, whose failure mode
is the point: see Testing strategy.

## Testing strategy

There is no Go test to write. The evidence AC-4 and AC-5 demand goes in the PR body,
and both must be produced by commands you actually run at PR time.

### AC-4 — prove the shipped recipe discriminates

The recipe this ticket removes was dead (`go list -deps ./internal/supervisor/...`
exits 1 with `no such file or directory`). A recipe that passes unconditionally would
be a quieter version of the same defect, so ship one and show it failing.

Hold the recipe *shape* fixed — `go list -deps <pkg> | grep -qx <dep>`, exit 0 means
"the edge exists" — and vary only the pair. Run four commands, paste all four with
their exit codes:

- presence check, true pair (`./internal/control` × `internal/sessions`) → **0**
- presence check, false variant (`./internal/control` × `internal/streamsup`) → **non-zero**
- absence check, true pair (`./internal/sessions/...` × `internal/streamsup`) → **0**
- absence check, false variant (`./internal/sessions/...` × `internal/conversations`,
  an edge that genuinely exists) → **non-zero**

Each half is thereby shown to flip. All four were run against `main` while writing
this spec and produced exactly that pattern; run them yourself on your branch — do not
paste this paragraph as the evidence.

The false variants are demonstrations for the PR body only. Do not commit them into
the doc.

### AC-5 — enumerate and classify every surviving `supervisor` hit

Produce the list by running the sweep at PR time, not by copying from the ticket or
from here — a sibling may land first and change both the count and the line numbers.
Sweep case-insensitively so `Supervisor` and `supervisor.Config` are caught:

```sh
grep -in supervisor docs/knowledge/architecture/system-overview.md
```

Then classify each surviving hit as **(a)** owned by a sibling, naming which,
**(b)** a generic-noun use needing no edit, or **(c)** unowned by any ticket. The
section→owner map is stable and comes from the ticket body:

| section of the file | owner |
|---|---|
| § "Data Flow" → *Interactive Session*; the `pyrycode/tui-driver` and `creack/pty` rows of § "Dependencies" | #1559 |
| § "Data Flow" → *Restart Cycle*, *Backoff Strategy*, *Fast-crash self-heal* | #1560 |
| § "Key Types" → the four `### supervisor.*` subsections | #1561 |

Anything outside those three regions is (b) or (c) and you must say which. Two places
are worth checking deliberately, because they fall in neither the in-scope region nor
any sibling's: the file's opening sentence ("Pyrycode is a process supervisor…"), and
§ "Idle Eviction + Lazy Respawn", whose diagram and prose still describe a supervisor
going up and down. Judge each on its own text — a generic noun is not a stale package
reference, but "respawns the supervisor" describing live behaviour may well be (c).

### Self-check before opening the PR

- Re-run the tree sweep: every path the tree names resolves. The five that failed
  before this change (`internal/supervisor/` + its three files, `cmd/pyry/acp.go`,
  `internal/control/attach.go`) are gone; nothing new fails.
- The fenced tree still renders — box-drawing connectors are consistent after the two
  deletions, and the last child of `cmd/pyry/` uses `└──`.
- `git diff --stat` shows one file.

## Open questions

- **Description density for the three new `internal/control` entries.** The
  surrounding tree ranges from one clause to a full paragraph per file. One line each
  is the right call for a map; if `server.go`'s existing entry already reads well,
  leave it rather than rewriting for symmetry.
- **Whether to also ship a back-edge check** (`! go list -deps ./internal/sessions/rotation
  | grep -qx …/internal/sessions`) alongside the two recipes. It would make the
  paragraph's third surviving clause checkable too. Recommended: skip it. The ticket
  says to keep the edit small, and AC-3/AC-4 are satisfied by the two lines above.
