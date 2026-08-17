# Spec #1501 — `-count=1` on the four opt-in e2e recipes

**Size:** XS — one file (`Makefile`), four recipe lines plus one comment block. No Go source changes.

## Files to read first

| Path | Symbol | What to extract |
|---|---|---|
| `Makefile` | targets `test`, `e2e`, `e2e-realclaude`, `e2e-liverelay`, `e2e-install`, `e2e-update` | The six `go test` recipes. Four change; `test` and `e2e` do not. |
| `internal/e2e/harness.go` | `ensurePyryBuilt` | The subprocess `go build` of `cmd/pyry` shared by the `e2e` **and** `e2e-install` suites — the file carries `//go:build e2e \|\| e2e_install`. Also builds fakeclaude a few lines below. |
| `internal/e2e/realclaude/fixtures.go` | `ensurePyryBuilt` | Realclaude's own copy of the same subprocess build (plus the self-ref guard — read it, don't touch it). |
| `internal/e2e/liverelay/liverelay_test.go` | `ensureRelayBuilt`, `ensurePyryBuilt`, `defaultSiblingRelayRepo` | Two build sites (sibling relay + `cmd/pyry`), and the resolution logic that decides whether the suite **runs or skips**. Load-bearing for the AC2 repro — see § Testing strategy. |
| `cmd/pyry/update_e2e_test.go` | the two `exec.Command("go", "build", …)` sites | Builds `cmd/pyry` and `internal/brokenpyry`. The second is the one outside the recipe's dependency graph. |
| `docs/release-tooling.md` | § *Live-claude suite — read the count, not the exit code* | The two existing false-green modes. A replayed cache is the third. **Pointer only — the documentation phase owns this file; do not edit it in this ticket.** |

## Context

`make e2e` carries `-race -count=1`. The other four opt-in recipes invoke `go test` bare.

Every one of those suites builds its binary under test with a **subprocess** `go build`. A child
process's file reads are not recorded in the test binary's cache key, so the source tree each suite
actually compiles is invisible to `go test`'s caching. An edit confined to `cmd/pyry`, to
`internal/brokenpyry`, or to the sibling relay checkout leaves the key unchanged, and `go test`
replays the previous `ok` without spawning anything.

I re-verified the ticket's claims at `2e33ffe` rather than inheriting them:

- **Nine subprocess `go build` sites, all tag-gated.** Confirmed by sweeping `exec.Command("go", "build", …)`
  across the tree. Tags: `e2e` (2 × `harness.go`, 2 × `fakeclaude/main_test.go`), `e2e || e2e_install`
  (the `harness.go` pair, reached by both suites), `e2e_realclaude`, `e2e_liverelay` (× 2),
  `(darwin || linux) && e2e_update` (× 2). The two `fakeclaude/main_test.go` sites sit behind plain
  `e2e`, which is why the ticket's gap table omits them — `e2e` already carries the flag.
- **`test` has no gap and its cache must stay.** It is untagged, so it reaches none of the nine.
- **The `e2e-update` correction is right.** `go list -deps -tags e2e_update ./cmd/pyry/` returns zero
  matches for `brokenpyry`, and it is in neither `TestImports` nor `XTestImports` — while
  `update_e2e_test.go` builds it by subprocess. An earlier revision of the ticket asserted the
  opposite; the correction stands.

## Design

Add `-count=1` to four recipes, positioned after `-tags` to match the ordering `e2e` already uses.

| Target | Before | After |
|---|---|---|
| `e2e-realclaude` | `$(GO) test -tags e2e_realclaude ./internal/e2e/realclaude/...` | `$(GO) test -tags e2e_realclaude -count=1 ./internal/e2e/realclaude/...` |
| `e2e-liverelay` | `$(GO) test -tags e2e_liverelay ./internal/e2e/liverelay/...` | `$(GO) test -tags e2e_liverelay -count=1 ./internal/e2e/liverelay/...` |
| `e2e-install` | `$(GO) test -tags e2e_install ./internal/e2e/...` | `$(GO) test -tags e2e_install -count=1 ./internal/e2e/...` |
| `e2e-update` | `$(GO) test -tags e2e_update ./cmd/pyry/...` | `$(GO) test -tags e2e_update -count=1 ./cmd/pyry/...` |

### What must not change

These are constraints, not suggestions — each one is a way this ticket has already been misread:

- **No `-race` anywhere.** `e2e` reads `-tags e2e -race -count=1`; only the `-count=1` half is being
  copied. Adding `-race` to `e2e-realclaude` would slow a suite that spends real tokens on every run.
- **`test` is untouched.** Its cache is load-bearing — it runs inside `make check` on every
  inner-loop iteration, and it reaches none of the nine build sites.
- **`e2e` is untouched.** It already has the flag.
- **No scope changes.** `e2e-install` stays `./internal/e2e/...`; `e2e-update` stays `./cmd/pyry/...`.
- **Nothing outside `Makefile`.** No Go source, no docs. `docs/release-tooling.md` gets its pointer
  from the documentation phase after merge, not from this PR.

### Comment (AC3)

One block, placed immediately above `.PHONY: e2e-realclaude` — the first affected recipe in file
order and the only one of the four with no comment block of its own. A single block naming all four
targets beats four copies: a reader about to delete the flag from `e2e-update` greps `-count=1` and
lands on the one canonical explanation.

Wording is the developer's to adjust, but it must carry all three facts — *which* recipes, *why the
cache is blind* (subprocess build), and *why `test` is deliberately excluded*:

```make
# e2e-realclaude, e2e-liverelay, e2e-install and e2e-update each pass -count=1,
# and it is load-bearing. Every one of them builds its binary under test with a
# subprocess `go build` (ensurePyryBuilt in internal/e2e, internal/e2e/realclaude
# and internal/e2e/liverelay; ensureRelayBuilt for the sibling relay; both update
# sites in cmd/pyry). A child process's file reads never enter the test binary's
# cache key, so `go test` cannot see the sources these suites actually compile —
# an edit confined to cmd/pyry, internal/brokenpyry or the sibling relay checkout
# leaves the key unchanged and replays the previous `ok`, proving nothing. Do not
# drop the flag as redundant. `test` above keeps its cache deliberately: it is
# untagged, reaches none of those build sites, and runs on every `make check`.
```

Name symbols, not line numbers — the build sites move, and a stale `file:NNN` in a comment is the
exact rot `cite-guard` exists to stop (it scans `.go` only, so the Makefile is on the honour system).

## Concurrency model

Not applicable — a build-recipe flag change. No goroutines, no shutdown sequence.

## Error handling / failure modes

No new failure modes; the change removes one. Two consequences to accept rather than tune away:

- **The gate gets slower, deliberately.** Any realclaude gate run that was previously replaying a
  cached `ok` now executes the full live suite (700-plus tests) and spends real tokens every time.
  That is the point of the ticket.
- **Skip-loud posture is unchanged.** `ensureRelayBuilt` still `t.Skip`s with a named diagnostic when
  no sibling relay checkout is present. `-count=1` does not change what runs, only that it is not
  replayed — which is exactly the trap in § Testing strategy.

## Testing strategy

No automated test. **Do not add a Makefile-grepping guard test** — nobody has ever removed one of
these flags as redundant, so a code-level enforcement would be a defense for an unobserved failure
mode. The AC3 comment is the chosen (advisory, cheap) defense.

The proof is AC2's repro, run by hand on `e2e-liverelay` — credential-free, hermetic, ~5 s per run,
and the one suite that shares the mechanism with the other three without spending anything.

### Do not run the other three

- **`make e2e-install` mutates the operator's real launchd/systemd user domain.** It is not hermetic.
  Never run it to "check the flag works."
- **`make e2e-realclaude` spends real tokens** and the dispatcher owns that gate (`needs-real-claude`
  is on this ticket).
- **`make e2e-update`** spawns and restarts a real daemon; hermetic under a temp HOME, but slow and
  unnecessary — it shares the mechanism `e2e-liverelay` already proves.

Their defect is established by the shared subprocess-build mechanism plus the `go list` dependency
check above, not by measurement. Say so in the PR rather than implying all four were exercised.

### Repro

Point the suite at the sibling relay checkout first. **`defaultSiblingRelayRepo` resolves
`../pyrycode-relay` from the compile-time source path**, which in a worktree is
`.pyrycode-worktrees/<branch>/` — so the default lands on a directory that does not exist and the
whole suite skips. Derive it from the *canonical* repo root instead (verified working from this
worktree):

```sh
CANON="$(dirname "$(git rev-parse --path-format=absolute --git-common-dir)")"
export PYRY_RELAY_REPO="$(cd "$CANON/.." && pwd)/pyrycode-relay"
```

Then, with the fix applied — guard the revert with `trap … EXIT`, not `set -e`, which skips the
revert when a run fails:

- Run `make e2e-liverelay`, capturing output.
- Append a comment line to any `cmd/pyry/*.go` file (the ticket used `relay.go`).
- Run `make e2e-liverelay` again, capturing output.
- Revert the probe edit. `git status` must be clean of it before commit.

**Pass condition — check both, not just the first:**

1. Neither log contains `(cached)`.
2. `grep -c '^=== RUN'` is **greater than zero and equal** in both logs.

Check (2) is not optional. `-count=1` makes "no `(cached)`" true even when every test in the suite
skips, so check (1) alone is satisfied vacuously by a suite that never ran — the same
count-not-exit-code failure `docs/release-tooling.md` already documents for the live suite, arriving
at a new site. A zero count means `PYRY_RELAY_REPO` did not resolve.

Optionally capture the pre-fix baseline too (stash the Makefile change, repeat): run 2 prints
`(cached)` in ~0.2 s. It makes the PR evidence self-contained by showing the assertion discriminates
rather than passing trivially.

### Trap: `-overlay` is invalid here

The house technique of running mutations via `go test -overlay=<json>` to avoid worktree writes
**does not work for cache questions**. The overlay flag is part of the command line, so it changes
the cache key and forces a miss for the wrong reason — the run would look like a pass regardless of
whether `-count=1` is present. This repro requires a real on-disk edit and a real revert.

### Regression surface

`make check` is unaffected — it runs `vet test staticcheck substrate-guard cite-guard e2e`, none of
which change. Run it to confirm nothing else moved.

## Open questions

- **Comment placement** is an architect call (one shared block above `e2e-realclaude`), not a
  constraint from the ticket. If review prefers a one-line pointer beside each of the other three,
  that is a fine variation — the requirement is only that a reader at any of the four finds the
  reason before deleting the flag.
- **Whether `e2e-install` / `e2e-update` stay in scope** is flagged in the ticket as the operator's
  call: striking either is a one-line edit. Absent instruction, implement all four — both have the
  same defect by the same mechanism and cost nine characters each.
