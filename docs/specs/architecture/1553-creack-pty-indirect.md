# #1553 — Demote `creack/pty` to an indirect dependency in `go.mod`

**Size:** XS (confirmed; PO's label unchanged)
**Scope:** `go.mod`, one line moved. No `.go` file changes.

## Files to read first

This ticket has no code surface — the deliverable is the module manifest — so the
reading list is short by nature. Read these three, in order, and nothing else:

- `go.mod` — the two `require` blocks. The first (direct) currently holds
  `github.com/creack/pty v1.1.24`; the second (indirect) is where it belongs.
  This is the whole edit surface.
- The ticket body's **Technical Notes** — specifically the `git grep` form and
  the `.claude/worktrees/` trap. Both are load-bearing; see § Verification traps.
- `CODING-STYLE.md` § "Dependencies" — read it to confirm you are **not** editing
  it. It still lists `creack/pty` under "Current justified deps" with a
  direct-PTY-allocation rationale. That line is stale, and it stays stale in this
  ticket; see § Context.

Do **not** open `internal/e2e/realclaude/finding_live_run_test.go`. Its sole
`creack/pty` mention is prose inside the comment block in `finLiveRunStage`,
explaining which fd claude inherits on each runner path. It is a comment, it does
not hold the direct requirement open, and rewriting it is #1554's job.

## Context

`go.mod` lists `github.com/creack/pty v1.1.24` in the direct `require` block. After
#1348 deleted both terminal-driving claude paths, no package in this module imports
it under any build tag. It is still needed, but only transitively via
`github.com/pyrycode/tui-driver`, so the requirement is misclassified rather than
obsolete.

The cost of leaving it is that the next unrelated `go mod tidy` — on any future
dependency change — silently rewrites the require block, and an innocent PR carries
a `creack/pty` diff its reviewer must investigate or wave through. Doing the move
deliberately, in a ticket whose entire diff is that move, converts a future
surprise into a reviewed one-line change.

**Premise re-verified at `main` 18e397e**, not inherited from the #1524 split or
from PO's 2e33ffe measurement:

| Check | Result at 18e397e |
|---|---|
| `go mod tidy -diff` | exactly the two hunks in § Design, and no `go.sum` hunk |
| `git grep -n 'creack/pty' -- '*.go'` | 1 hit, the comment in `finLiveRunStage` |
| `git grep -n '"github.com/creack/pty"' -- '*.go'` | **0 hits** — no import line anywhere |
| import-set sweep, `creack/pty` | 0 |
| import-set sweep, `tui-driver` (control) | 7 |
| `go list -deps`, `creack/pty` | 1 (the transitive resolution that must survive) |

**Two drifts from the numbers in the ticket body, both benign — expect them:**

1. **The control reads 7, not the body's 8.** The body's control-tested sweep was
   run at 2e33ffe; a tui-driver importer has gone away since. This does not weaken
   the sweep: the control's job is to prove the command can return a non-zero
   number, and 7 does that. The `creack/pty` zero still means absence, not a broken
   invocation.
2. **`CODING-STYLE.md`'s stale line is at line 83**, not the body's `82-84`.
   Irrelevant to the work — you are not editing that file — but do not treat the
   mismatch as evidence the premise moved.

**A second stale doc, not named in the ticket and still unowned.**
`docs/knowledge/architecture/system-overview.md`'s dependency table gives
`creack/pty`'s purpose as "Reading the operator's *own* terminal size
(`GetsizeFull(os.Stdin)`) for the foreground SIGWINCH watcher" — a direct use.
No such call exists: every `GetsizeFull` and `pty.Start` occurrence in a tracked
`.go` file is inside a comment, and there are zero import lines. So the table
asserts a live direct dependency the code has not had since #1348, exactly like
the `CODING-STYLE.md` line PO flagged. It is out of scope here for the same reason
— a doc edit would destroy the one-line-diff property that makes this ticket cheap
to verify — and `docs/knowledge/` belongs to the documentation phase regardless.
Recording it here so the fact is not lost: **two** dependency-rationale docs now
describe a direct `creack/pty` use, and both need a ticket.

**No ADR.** A manifest reclassification that Go's own resolver performs
mechanically does not carry a decision worth recording.

## Design

Run `go mod tidy`. Do not hand-edit `go.mod`.

The tidy is the design decision. A hand edit would produce the same two lines, but
`go mod tidy` is the tool whose output the acceptance criteria are written against,
it derives the classification from the real import graph rather than from the
developer's reading of it, and it cannot get the indirect block's sort order wrong.

The expected result, measured at 18e397e — this is the contract, not an
illustration. Anything else means stop and reassess:

```
 require (
 	github.com/coder/websocket v1.8.13
-	github.com/creack/pty v1.1.24
 	github.com/flynn/noise v1.1.0
 ...
 require (
+	github.com/creack/pty v1.1.24 // indirect
 	github.com/hinshun/vt10x v0.0.0-...
```

The version stays `v1.1.24`. This is a re-classification, not an upgrade — if the
version moves, the tidy did more than intended and the change is wrong.

**Do not add a regression guard.** No test, no `go list` assertion, no CI check
that the requirement stays indirect. The failure this ticket addresses is a future
tidy producing a surprise diff, and performing the tidy now *is* the fix — a guard
would defend a failure mode that no longer has a trigger. It would also add lines
to a diff whose exactness is itself an acceptance criterion. The module manifest is
already self-enforcing here: the next `go mod tidy` is the check.

**Out of scope, explicitly.** The stale `runAgentRunPty` comments (#1554), the
`CODING-STYLE.md` dependency line, and the `system-overview.md` table row. Keep the
diff to `go.mod`.

**One constraint clarification.** The developer's standing rule is that mutable
production code lives under `cmd/` and `internal/`. `go.mod` sits outside both, but
it is this module's build manifest — not a shared doc owned by another pipeline
phase — and it is the entire approved deliverable of this ticket. Edit it. The rule
exists to keep the developer out of the knowledge base and `PROJECT-MEMORY.md`,
and it is not a reason to self-block here.

## Concurrency model

None. No runtime code changes.

## Error handling

No runtime failure modes. Two build-time ones worth naming:

- **`go mod tidy` wants the network.** The module cache is warm on this tree
  (`go mod tidy -diff` ran clean), so tidy resolves from cache. If it tries to
  reach the network, something else is wrong — do not add `GOFLAGS=-mod=mod` or a
  `GOPROXY` override to work around it.
- **The tidy produces more than the two hunks.** Treat as stop-and-reassess, not
  as something to trim back by hand. It would mean the import graph changed under
  the ticket, which invalidates the premise rather than the method.

## Testing strategy

No new tests. This change cannot be unit-tested — the assertion is about the
manifest, and the verification is the AC set itself. Run these:

- **AC 1 + 2 (the move, and nothing else).**
  `git diff --numstat go.mod` → exactly `1  1`.
  `git diff --numstat go.sum` → **no line at all**. `--numstat` prints nothing for
  an unchanged file; it never prints a `0  0` row, so "no output" is the pass
  condition, not a `0 0` row.
- **AC 3 (idempotency).** Use `go mod tidy -diff` rather than running tidy a second
  time and inspecting `git diff`. It reports the changes tidy *would* make to both
  `go.mod` and `go.sum` without writing either file, exits non-zero when updates
  are needed, and exits 0 with empty output when the tree is already tidy. On the
  post-change tree, **exit 0 and no output is the pass.** This is strictly better
  than a second real tidy: it cannot leave the worktree dirty if it turns out not
  to be a no-op.
- **AC 4 (the tagged package).** `go build -tags e2e_realclaude ./...` and
  `go vet -tags e2e_realclaude ./internal/e2e/realclaude/`, both exit 0. This is
  the criterion that earns its place: `make check` never compiles the live-claude
  package, so its green says nothing about it, and a manifest change is precisely
  the class of change that can break a tagged-only package while the standard gate
  stays honestly green. These are compile checks — no claude credentials, no live
  turns, no tokens.
- **AC 5.** `make check` green.

### Verification traps

- **Use `git grep`, never `grep -r`.** `.claude/worktrees/` holds an *untracked*
  pre-deletion checkout that genuinely imports `creack/pty` in several `.go` files
  and carries its own `go.mod`. Being untracked it is invisible to `git grep` and
  to `git archive`, and being a separate module it neither affects `go mod tidy`
  nor should be edited. A plain `grep -r` hits it and returns false positives that
  read as "the demotion is wrong". `git grep -n 'creack/pty' -- '*.go'` is correct
  by construction rather than by remembering an exclusion, and returns exactly one
  hit — the comment in `finLiveRunStage`.
- **The sharper grep is the import-line one.** `git grep -n '"github.com/creack/pty"' -- '*.go'`
  returns zero hits and is the direct evidence for the demotion, because it cannot
  match prose. The looser `creack/pty` grep matches comments and needs a human to
  classify each hit.
- **Do not `rm -rf` a scratch copy to test the tidy.** `go mod tidy -diff` answers
  the same question read-only, in this worktree, with no copy and no cleanup. A
  prior run of this ticket was halted by a denied `rm -rf`; the destructive form
  was never necessary.

## Open questions

None blocking. One item for a human, restated so it is not lost: **two** docs —
`CODING-STYLE.md` § "Dependencies" and `system-overview.md`'s dependency table —
still describe a direct `creack/pty` use this module has not had since #1348.
Neither is covered by any of the eight sibling #1348-residue tickets (#1519, #1535,
#1536, #1537, #1538, #1543, #1544, #1554), which cover Go comments and the
control-plane/sessions feature docs. They need their own ticket. Deliberately not
folded in here.
