# 1552 — Scope substrate-guard to this tree and retire the stale ptyrunner exemption

**Size:** XS · **Package:** `cmd/substrate-guard` · **Security-sensitive:** yes (see § Security review)

## Files to read first

- `cmd/substrate-guard/main.go` → the package doc comment, `allowlist`, `isAllowlisted`, `pattern`, `patterns`, `main`. The whole file is the change surface; read it end to end before editing. Extract: the doc block's factual claims, the three allowlist entries, and the shape of `main`'s walk closure.
- `cmd/cite-guard/main.go` → `newIndex`, `main`, `allowlist`, `isAllowlisted`. **The sibling guard, and the decisive precedent for this ticket**: it was born with `.claude` already in its skip switch, in both of its walks. Extract the exact spelling of its skip `case` list and copy it verbatim — the two guards' walks should stay byte-comparable.
- `cmd/cite-guard/main_test.go` → `TestAllowlistCoversThisTest`. The house shape for asserting allowlist membership directly. Extract the shape, **not** the policy: cite-guard allowlists its own `main_test.go` and says so in its `allowlist` doc comment; substrate-guard must not (AC 1).
- `Makefile` → the `substrate-guard` target and the comment block above it. Extract: the target is `$(GO) run ./cmd/substrate-guard`, so cwd is always the repo root; and the comment makes no allowlist-count or CI claim, so nothing there needs to stay in sync. **The Makefile is outside your mutable surface — read only.**
- `docs/knowledge/features/fakeclaude-binary.md` → the sections that mention "the `cmd/substrate-guard` allowlist". Extract: the fakeclaude entry is load-bearing and repeatedly relied on by that doc, and every one of those references stays true after this change (they name the entry, never a count). Read-only; the documentation phase owns it.
- `CODING-STYLE.md` § Testing and § Comments — Citing Other Code. Extract: table-driven, stdlib `testing` only, `t.Parallel()`, `t.Helper()`; and the cite-by-symbol rule that `make cite-guard` enforces on every comment the new test file adds.

## Context

`cmd/substrate-guard` is the different-fabric second check behind the tui-driver compiler seal: it fails the build when a claude-TUI substrate literal appears in a pyrycode `.go` file outside an explicit allowlist. Two defects, entangled, are the reason this is one ticket:

1. **A live exemption for a deleted path.** The allowlist still carries `internal/agentrun/ptyrunner/helper_test.go`, which #1348 deleted. `isAllowlisted` matches by `strings.HasSuffix`, so the entry is not inert — it is a standing exemption that any future file at that path would inherit silently. A recreated ptyrunner spike would be exempt from the ban the guard exists to enforce.
2. **The walk descends into `.claude/`.** The skip switch in `main` skips `.git`, `vendor`, `node_modules` and `dist` — not `.claude`. On the operator's clone, `.claude/worktrees/<name>/` is a checkout of another branch that predates the deletion and still contains that helper. Its path *ends* with the allowlisted suffix, so defect 1 is currently masking defect 2. Deleting the entry alone turns `make check` red for reasons that have nothing to do with the tree under test.

`.claude/worktrees/` is excluded via a per-clone `.git/info/exclude`, so a fresh clone never reproduces it and never will. That is what makes the coupling easy to miss and what makes a machine-state-dependent proof worthless: **the binding evidence for this ticket has to be hermetic.**

Two facts settle design choices that the ticket left to this document:

- **`cmd/cite-guard` already skips `.claude` wholesale, by directory name, and has since its introducing commit** (`git log -S '".claude"' -- cmd/cite-guard/main.go` returns exactly `20e2c5f feat(cite-guard): ban comment citations a symbol name replaces`). cite-guard's doc comment records that it is modelled on substrate-guard. So substrate-guard is the one that drifted, and closing the gap is a convergence, not a new policy.
- **`git ls-files .claude` returns exactly one tracked file, `.claude/settings.json`, and zero `.go` files**, so the wholesale skip costs no coverage today. The residual blind spot it creates is named and accepted in § Security review.

The guard's own doc comment also asserts a `check.yml PR gate` that has never existed at any commit; `.github/workflows/` holds only `release.yml` and `self-check-daily.yml`. That clause sits two lines above the doc text this ticket rewrites anyway, so it is corrected here.

**No ADR is warranted.** The decision is a one-word skip plus a deletion, and its rationale belongs in the package doc comment where the next reader of the switch will find it. The documentation phase should not open a decision record for it.

## Design

Four changes, all in `cmd/substrate-guard`, plus one new test file. Make them in a single commit: D1 without D2 is the red row from the ticket's table.

### D1 — Retire the ptyrunner entry

Delete `"internal/agentrun/ptyrunner/helper_test.go"` from `allowlist`. The remaining two entries keep their current bytes and their current order. **Add nothing.** The tempting third entry — this ticket's own new `main_test.go` — is forbidden by AC 1 and is solved by D5 instead.

### D2 — Stop descending into `.claude`

Add `".claude"` to the skip `case` in the walk's directory branch, appended last so the list reads identically to cite-guard's:

```go
case ".git", "vendor", "node_modules", "dist", ".claude":
```

Matching stays on `d.Name()`, so a directory of that name is skipped wherever it appears. That is deliberate and is the same choice cite-guard made; the alternative — a name-based `case "worktrees"` — would skip any directory called `worktrees` anywhere in the tree, which is a strictly worse blind spot for a strictly narrower benefit.

### D3 — Extract a testable `scan`

`main` currently owns the walk and calls `os.Exit`, so the walk is unreachable from a test. Split the reporting from the scanning:

```go
// hit is one banned substrate literal, at one line of one scanned file.
type hit struct {
	file string  // slash-separated, relative to the scan root
	line int     // 1-based
	pat  pattern // the first pattern matching that line
}

// scan walks root and reports every banned substrate literal in .go files
// outside the allowlist, at most one hit per line, in walk order. Directories
// named .git, vendor, node_modules, dist and .claude are not descended into.
// Per-entry walk errors and unreadable files are skipped, as today. The
// returned error is non-nil only when the walk itself fails.
func scan(root string) ([]hit, error)
```

`main` becomes: call `scan(".")`; on error, the existing stderr line and `os.Exit(2)`; on a non-empty result, the existing multi-line report and `os.Exit(1)`; otherwise the existing `substrate-guard: clean`. **Every byte the guard prints today must survive unchanged** — the report line is still built with `strconv.Quote` of the matched bytes and the pattern name, only now from a `hit` rather than inline in the closure.

`hit.file` is root-relative: compute it with `filepath.Rel(root, path)` and then `filepath.ToSlash`. For `root == "."` this yields exactly the strings `filepath.ToSlash(path)` yields today, so production output does not change. `filepath.Rel` cannot fail for a path `WalkDir` produced from that same root; if it ever does, fall back to `filepath.ToSlash(path)` rather than dropping the file, so a hit is never silently lost — one line, with a comment saying why.

`isAllowlisted` keeps its current `strings.HasSuffix` semantics and its current signature. Narrowing it to an exact match is a real improvement and is deliberately **not** done here — see § Security review, finding [Trust boundaries].

### D4 — Make the package doc true

Rewrite the doc comment so every claim holds. The precise residual sweep was run over the file and the result is exact:

| Line's claim | Verdict |
|---|---|
| `the second, different-fabric check` | **Carve-out — keep.** Describes the relationship to the compiler seal. |
| `the two sanctioned fake-claude helpers` + the ptyrunner path that follows it | **The only count to fix.** |
| `BOTH a compiled literal … and an escaped source form` | **Carve-out — keep.** Describes byte matching. |
| `the per-line first-match reports the most precise name` | **Carve-out — keep.** Describes pattern ordering. |
| `wired into make check and the check.yml PR gate` | **False clause — delete the `check.yml` half.** |
| `os.Exit(2)` | Code, not a count. Leave it alone. |

Nothing else in the file matches a count word. **Do not sweep on a bare `2`:** `e2e` and `#603` contain the digit and are not counts; changing either breaks a real path and a real reference.

Suggested replacement prose for the two affected blocks — adjust wording freely, but every factual claim in it must stay true:

```
// Run via `make substrate-guard`, wired into `make check`. Scans every .go
// file under the repo root, skipping the allowlist; exits non-zero and prints
// file:line for every hit. Directories named .git, .claude, vendor,
// node_modules and dist are not descended into: .claude/ holds agent working
// copies of other branches, which are not this tree's source.
//
// Allowlist: the sanctioned fake-claude helper internal/e2e/internal/
// fakeclaude/main.go, which legitimately emits these literals to simulate
// claude's TUI for the relay/send_message e2e flows (#603), and this guard's
// own source, which must name the patterns it bans.
```

Naming `.claude/` and *why* it is skipped is not decoration: D2 creates a permanent blind spot, and a blind spot a reader can see is the accepted form of one. Do not claim in the doc that no CI gate exists — that is a fact about another directory and would rot the same way the `check.yml` clause did.

### D5 — The test file may not spell a banned literal

`cmd/substrate-guard/main_test.go` does not end with any allowlisted suffix, so **the guard scans its own test file like any other file.** A test that writes a fixture triggering a hit therefore cannot contain the literal in its own source. Resolve it by building fixtures from the `patterns` table, so the test source carries no substrate bytes at all:

```go
// bannedLiteral returns the substr of the pattern registered under name.
// It fails the test when no such pattern exists, so a rename in `patterns`
// reddens here instead of silently turning every fixture below into an
// ordinary file and every assertion into a tautology.
func bannedLiteral(t *testing.T, name string) []byte

// writeGo creates <root>/<rel>, parents included, as a .go file whose body
// contains body. Fails the test on any I/O error.
func writeGo(t *testing.T, root, rel string, body []byte)
```

Use the pattern named `pasted-text chip` as the fixture literal throughout. Its **name** is safe to spell in the test source — it differs from its own `substr` in case and separator, and no pattern name in the table contains any pattern's `substr`. If that ever stops being true, `make check` says so immediately, because the guard scans this file.

Do **not** solve this by allowlisting the test file (AC 1), and do **not** solve it by spelling glyphs as hex byte lists — the `patterns`-table route needs no maintenance and cannot drift out of step with the thing it is testing.

## Concurrency model

None in production. `scan` is a single-goroutine `filepath.WalkDir` and takes no locks.

In the test file, every test may take `t.Parallel()` because each owns its own `t.TempDir()`. **One hard rule: no test may assign to `allowlist` or `patterns`.** Both are package-level `var`s and every parallel sibling reads them; a test that swaps the allowlist to exercise a case would be a data race under `-race` and would leak mutated state into whichever test ran next. Every scenario below is expressible by choosing fixture *paths*, which needs no mutation.

## Error handling

| Failure | Behaviour | Change from today |
|---|---|---|
| `filepath.WalkDir` returns a walk error | `scan` returns it; `main` prints `substrate-guard: walk error:` and exits 2 | none |
| Per-entry error inside the closure | skipped, walk continues | none |
| `os.ReadFile` fails on a `.go` file | file skipped, walk continues | none — matches cite-guard |
| `filepath.Rel(root, path)` fails | fall back to `filepath.ToSlash(path)`; the hit is still reported | new, unreachable in practice, documented in a one-line comment |
| Hits found | existing multi-line stderr report, exit 1 | none — byte-identical output |

## Testing strategy

One new file, `cmd/substrate-guard/main_test.go`, `package main`, table-driven, stdlib only. Every scenario builds its fixture tree under `t.TempDir()` and calls `scan(tmpRoot)`. **No fixture may ever be created inside the repo worktree** — see the prohibition in § Build and verification.

- **`TestAllowlistIsExact`** — `allowlist` equals exactly `internal/e2e/internal/fakeclaude/main.go` then `cmd/substrate-guard/main.go`, in that order. Reddens if the ptyrunner entry returns *or* if a third entry is added. Assert in the same test that `isAllowlisted("cmd/substrate-guard/main_test.go")` is false, which states the D5 trap as a fact rather than a comment.

- **`TestScan_DoesNotDescendIntoDotClaude`** — this is AC 2's hermetic form. Rows, each its own temp root, each expecting **zero** hits:
  - a banned literal at `.claude/worktrees/focused-goldstine-41bfd6/internal/agentrun/ptyrunner/helper_test.go` — the exact shape of the operator-machine failure, on every machine
  - a banned literal at `.claude/worktrees/other/internal/relay/client.go` — a path that matches no allowlist suffix, proving the skip and not the exemption is what suppresses it
  - a banned literal at `internal/tooling/.claude/x.go` — a nested `.claude`, pinning that the skip is name-based and not root-anchored

- **`TestScan_ReportsTheRetiredPtyrunnerPath`** — the retirement-vs-relocation discriminator. A banned literal at `internal/agentrun/ptyrunner/helper_test.go` in the **main** tree position yields exactly one hit, with `file` equal to that exact path and `pat.name` equal to the name passed to `bannedLiteral`. A clean run alone cannot tell retirement from relocation; this assertion can, and it reddens the moment the entry is re-added under any spelling.

- **`TestScan_ReportsBannedLiteralsAcrossTheTree`** — proves D2 narrowed nothing else (AC 3, second half). One row per placement, each its own temp root, each expecting exactly one hit at that exact path:
  - `main.go` at the root
  - `cmd/pyry/main.go`
  - `internal/relay/client.go`
  - `internal/agentrun/ptyrunner/helper_test.go`

- **`TestScan_HonoursTheSurvivingAllowlist`** — one temp root carrying a banned literal at **both** `internal/e2e/internal/fakeclaude/main.go` and `cmd/substrate-guard/main.go`, expecting zero hits. Reddens if either surviving entry is dropped while the doc block above it is being edited.

- **`TestScan_StillSkipsTheOtherDirectories`** — regression fence around the `case` edit. Rows for `.git/x.go`, `vendor/v/x.go`, `node_modules/n/x.go`, `dist/d/x.go`, each with a banned literal, each expecting zero hits.

- **`TestScan_IgnoresNonGoFiles`** — a `.md` and a `.txt` carrying the literal yield zero hits.

**Do not add a per-pattern coverage test.** Iterating `patterns` and asserting one hit per entry looks natural and is a trap: the CSI catch-all overlaps the bracketed-paste forms, and `main`'s per-line first-match makes the expected `pat.name` for an overlapping fixture non-obvious. No AC asks for it, and getting it subtly wrong buys a green test that proves less than it appears to.

## Build and verification

- **`make check` must be green** (AC 5). It runs `substrate-guard` against the real tree and runs the new tests under `-race`.
- **AC 1** is proved by `TestAllowlistIsExact` and by reading the diff.
- **AC 3** is proved by `TestScan_ReportsTheRetiredPtyrunnerPath` and `TestScan_ReportsBannedLiteralsAcrossTheTree`. No fixture survives in the committed tree; every one lives under `t.TempDir()`.
- **AC 4** is proved by reading the rewritten doc against the D4 table.
- **AC 2 needs a note.** Its literal wording asks for `go run ./cmd/substrate-guard` to print `clean` "with `.claude/worktrees/focused-goldstine-41bfd6/` present on disk". That directory exists only on the operator's clone; it is absent from every agent worktree, where `.claude/` holds `settings.json` alone. `TestScan_DoesNotDescendIntoDotClaude` reconstructs that exact path, with that exact literal, and asserts the exact outcome — hermetically, on every machine, on every `make check`. **Treat the test as the binding proof for AC 2** and say so in the PR body so code review does not read the missing manual row as a skipped criterion.
- **Absolute prohibition: do not create a `.claude/worktrees/…` fixture inside your worktree**, not even briefly. It is untracked, the dispatcher's safety-net auto-commit fires on any dirty worktree, and an agent working copy committed onto `feature/1552` and pushed to origin is a mess to unwind. The hermetic test exists precisely so nobody needs to.
- **Do not touch `cmd/cite-guard/main.go`**, even though its doc comment carries the identical false `check.yml PR gate` clause. It is out of this ticket's scope; it is filed as a follow-up in § Open questions.

## Open questions

1. **`cmd/cite-guard`'s doc carries the same false CI claim.** Its "Run via `make cite-guard`, wired into `make check` and the check.yml PR gate" is wrong for the same reason and was missed by the same sweep. Out of scope here — PO should file it as a one-line sibling ticket rather than let the developer widen this diff.
2. **No CI backstop exists for either guard.** A developer who never runs `make check` bypasses both entirely. Named in the ticket's Context; a "wire `make check` into a PR gate" ticket is the real fix and is nobody's job on this ticket.
3. **`isAllowlisted`'s suffix matching is the residual class** behind defect 2. Exact matching against the now root-relative path would retire the class rather than the instance, but it should land in both guards together. See § Security review.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] SHOULD FIX — deferred, deliberately.** The design's boundary is "what counts as this tree's source", and after D2 it is explicit and in one place: the walk root, the skip `case` in `scan`, and `isAllowlisted` over a root-relative path. The residual hole is `isAllowlisted`'s `strings.HasSuffix`: *any* path ending in a surviving allowlist entry is exempt, so a nested copy of this repo under a non-skipped directory inherits the fakeclaude exemption. This is the same mechanism that produced defect 2, so it is an observed class and not a speculative one. It is **not** fixed here for three reasons: no AC asks for it; D3 makes the fix trivial only *because* paths became root-relative, so it is cheap to do later; and `isAllowlisted` is byte-identical in `cmd/cite-guard`, so narrowing one guard alone silently diverges the pair. Practical exploitability is low — a wrongly-exempt file would have to sit at the real fakeclaude helper's own path tail, and a nested copy does not compile into `pyry`. Recorded as Open question 3 for PO.
- **[Trust boundaries] No finding — boundary completeness.** The other foreign working copies on this machine live in `.pyrycode-worktrees/`, a **sibling** of the repo root, so a walk rooted at the repo never reaches them. `.claude/worktrees/` was the only in-tree instance of the class, and D2 closes it.
- **[File operations] SHOULD FIX — addressed in the spec, must hold in review.** D2 creates a permanent, silent blind spot: after this change no `.go` file anywhere under any `.claude/` is ever scanned, so a future Go-based agent hook parked there could carry substrate literals forever. Accepted because `git ls-files .claude` returns one tracked file and zero `.go` files, nothing under `.claude/` compiles into `pyry`, and `cmd/cite-guard` already made the identical call. Mitigation is that D4 **names the skip and its reason in the package doc**, so the blind spot is visible to the next reader of the switch rather than inferred from a bare `case`. Code review should fail the ticket if the doc ships without it.
- **[File operations] No finding — fixture paths.** Every path the new tests touch is a literal in the test table joined onto `t.TempDir()`; nothing is derived from an environment variable, an argument, or the repo. No traversal input, no check-then-use gap, no symlink following, and the framework removes the tree. `scan` itself only reads; it creates and modifies nothing at any mode.
- **[File operations] MUST NOT — stated as a hard prohibition in § Build and verification.** A fixture created inside the worktree to satisfy AC 2's literal wording would be untracked, would trip the dispatcher's dirty-worktree auto-commit, and would push another branch's working copy to origin on `feature/1552`. The hermetic test removes the incentive; the prohibition removes the option.
- **[Error messages, logs] No finding, with a small improvement.** The report prints the matched literal via `strconv.Quote` and the path. The literals are public tui-driver substrate, not secrets, and this is unchanged. D3's root-relative `hit.file` means a scan rooted at an absolute path reports relative paths, so the guard leaks slightly less about the operator's filesystem layout than it does today.
- **[Concurrency] MUST NOT — stated as a rule in § Concurrency model.** `allowlist` and `patterns` are package-level `var`s read by every parallel test. A test that mutates either to exercise a case is a `-race` failure and leaks state across siblings. Every scenario in § Testing strategy is expressible through fixture paths alone, so the temptation has no legitimate use here.
- **[Vacuous-assertion risk] No finding — designed against.** The whole test file's evidence rests on the fixture actually containing a banned literal. `bannedLiteral` looking the pattern up by name and calling `t.Fatalf` on a miss is what keeps a rename in `patterns` from turning every fixture into an ordinary file and every zero-hit assertion into a tautology that passes for the wrong reason. This is a load-bearing requirement of D5, not a nicety.
- **[Threat model alignment] OUT OF SCOPE, named.** The guard's threat is substrate drift re-coupling pyrycode to claude's TUI internals across self-updates. The unaddressed threat is that the guard has no CI backstop at all — a local `make check` is its only execution site — so skipping it bypasses both defects' fixes and everything else. That is Open question 2 and belongs to a separate ticket; this ticket cannot close it from inside `cmd/`.
- **[Tokens, secrets] / [Subprocess execution] / [Cryptographic primitives] / [Network & I/O] — not applicable, by design.** `scan` handles no credentials, executes no commands, uses no randomness or crypto, and opens no sockets. It reads `.go` files from a local directory tree and writes to stderr. There is no code path in this design where any of the four categories has a surface to review.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-20
