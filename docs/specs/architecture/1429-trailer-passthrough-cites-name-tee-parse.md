# #1429 — The trailer family's passthrough cites name the tee-parse block

**Size:** XS. Two files, **zero** production source files, five 7-for-7 in-place
replacements. Every claim below re-derived at `1d2c53d` (the ticket's numbers
were taken at `e20f4d2`; see § Drift check — they all still hold).

## Files to read first

| Path | What to extract |
|---|---|
| `internal/agentrun/streamrunner/runner.go:170-176` | The **wrong** target: `childCtx, cancelChild := context.WithCancel(ctx)` plus its comment. Cancellation plumbing — the words "operator SIGTERM/SIGINT", "no-result teardown". Nothing about claude's bytes. |
| `internal/agentrun/streamrunner/runner.go:177-179` | The **right** target: `// Tee-parse stdout … bytes pass through unchanged` + `parser := newStreamParser(cfg.Stdout, nil)`. This is the passthrough all four cites describe. |
| `internal/e2e/realclaude/trailer_terminal_reason_test.go:23-32` | Site 1 (`:28`), inside the file-header doc's "absence means opposite things on the two runner paths" bullet pair. |
| `internal/e2e/realclaude/trailer_terminal_reason_test.go:91-109` | Site 2 (`:97`), `trailReasonPresentOwesNone`'s doc. Here the cite is the **reason** the claim limit exists — read the whole paragraph to see that the argument breaks if the cite points at cancellation code. |
| `internal/e2e/realclaude/trailer_terminal_reason_test.go:222-244` | Sites 3 and 4 (`:232`, `:242`), inside the two streamrunner arms' **published** `Detail` format strings. These reach a probe record a human reviews. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:126`, `:1212` | The already-shipped **correct** form (`:177-179`), in a doc comment and in a published Detail respectively. Precedent for both shapes being fixed; match it. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:307-309` | `trailDetail` = `reachCapCommand(fmt.Sprintf(...))`. Explains why the cap check cannot see a length change under 512 bytes. |
| `internal/e2e/realclaude/background_reach_probe_test.go:945-950` | `reachCapCommand`: pure passthrough at or under `reachMaxCommandBytes`. Confirms the Details are returned verbatim, so `len(Detail)` measures the format-string expansion exactly. |
| `docs/knowledge/features/e2e-realclaude.md:819` | The same doc's **correct** cite of the passthrough. The fix at `:1104` is bringing one line into agreement with this one. |
| `docs/knowledge/features/e2e-realclaude.md:1100-1108` | Site 5 (`:1104`), the stale cite in the evergreen doc. |
| `docs/specs/architecture/1417-gate-admits-absent-reason-owes-none.md:343` | Where this defect was found and deliberately deferred. Confirms the deferral was explicit, not an oversight. |

Codegraph was queried (`codegraph_context` on the passthrough/childCtx
distinction); it returns the `streamrunner.Run` body and confirms the two blocks'
layout but nothing beyond the reads above. This ticket is entirely
comment-and-string text, which codegraph does not index — the grep sweeps in
§ Residual are the authoritative instrument here, and that is expected, not a
gap.

## Context

Four Go sites and one evergreen-doc line cite `streamrunner/runner.go:170-176`
for "streamrunner passes claude's bytes through unchanged". That range is the
`childCtx`/`cancelChild` block — cancellation plumbing. The passthrough is at
`:177-179`.

Two of the four are inside **published** `Detail` strings, so the wrong cite is
not merely a comment: it lands in a probe record a human is asked to reconcile
against the tree, and it sends them to unrelated code. A third (`:97`) is worse
in kind — there the cite is the *load-bearing reason* for
`trailReasonPresentOwesNone`'s claim limit ("claude could equally have emitted
this key, so no reading names pyry as author"). An argument resting on a
citation to cancellation plumbing does not hold up.

The sibling file already ships the corrected form at
`trailer_admissibility_test.go:126` and `:1212`, so the family currently
contradicts itself and the correct form is established precedent. The evergreen
doc contradicts *itself* — `:819` is already right.

Found during #1417, deliberately deferred as out of its scope
(`1417-*.md:343`).

## Design

Five replacements of the seven-character substring `170-176` with `177-179`,
each immediately preceded by `runner.go:`. Nothing else.

| # | File | Line | Current text (excerpt) |
|---|---|---|---|
| 1 | `internal/e2e/realclaude/trailer_terminal_reason_test.go` | 28 | `bytes through UNCHANGED (streamrunner/runner.go:170-176), synthesising a` |
| 2 | same | 97 | `(internal/agentrun/streamrunner/runner.go:170-176), a terminal_reason on a` |
| 3 | same | 232 | `"(streamrunner/runner.go:170-176), so a healthy run's trailer is claude's own "+` |
| 4 | same | 242 | `"(streamrunner/runner.go:170-176), so claude can produce the same reading. Empty "+` |
| 5 | `docs/knowledge/features/e2e-realclaude.md` | 1104 | ``passes claude's bytes through unchanged, `streamrunner/runner.go:170-176`,`` |

Note site 2 carries the **fully qualified** path and sites 1/3/4 the short one.
Both forms survive — this ticket corrects the *range*, not the path style, and
the shipped precedent uses both (`trailer_admissibility_test.go:126` qualified,
`:1212` short). Do not normalise the paths; that would be a second change AC5
forbids.

### The edit shape is what makes AC5 free

Do all four Go sites as a **single `replace_all` of `runner.go:170-176` →
`runner.go:177-179`, scoped to `trailer_terminal_reason_test.go`**. Two
properties fall out for free:

- **7-for-7, so no line changes length**, no comment needs rewrapping, no
  format-string continuation needs rebalancing, and the two Details' byte
  counts cannot move (AC1 + AC2 + AC5).
- **In-place, so no line is inserted or deleted** — every `git diff -U0` hunk is
  `-a,1 +c,1` (AC5's per-hunk parity).

Scope the replace to the one file. A repo-wide `sed` would also rewrite the
build artifacts and `codebase/1366.md`, which AC3 and AC4 forbid.

Site 5 is the same replacement in the doc, and `170-176` occurs exactly once in
that file (verified), so it is unambiguous.

There is no concurrency, error-handling, or interface surface in this change —
no runtime code is touched, and the only Go file is `//go:build
e2e_realclaude`-tagged test instrumentation.

### Why per-hunk parity, concretely

Six line-anchored cites point *into* `trailer_terminal_reason_test.go` from Go
sources, all in `trailer_admissibility_test.go`:

`:226` → `:222` · `:354` → `:208-221` · `:424` → `:203-206` · `:443` → `:222` ·
`:977` → `:222` · `:1288` → `:337-344`

Edit sites 3 and 4 (`:232`, `:242`) sit **between** `:222` and `:337-344`. A
`+1/−0` at either one silently walks the `:1288` → `:337-344` cite off its
target — creating exactly the defect class this ticket exists to close. Whole-file
line parity does not catch it: `+2` here against `−2` there nets zero while
displacing everything between. Hence per-hunk equality, not just a total.

(The file's own two bare continuation refs — `:100`'s `watchdog.go:253, :280`
and `:200`'s `finding_live_staging_test.go:485, :498, :576, :597` — inherit a
*different* file each, so they point outward and are unaffected by in-place
edits here.)

### One thing deliberately not touched

`:100` cites `streamrunner/watchdog.go:253, :280` for "pyry's own synthesis
there is unconditional when it happens". Both are **accurate** at `1d2c53d`:
`:253` is the `TerminalReason string \`json:"terminal_reason"\`` field
declaration and `:280` is the unconditional `TerminalReason: "idle_stall"`
literal at the synthesis site. It looks adjacent to this ticket's defect but is
not one. Leave it alone; changing it would trip AC5.

## Testing strategy

No test is added. The change adds no behaviour, so the existing suite is the
regression net and the work is in *demonstrating* the six ACs. Each is a command
with a pinned expected output.

**AC1 — the four cites moved.** Sweep for the qualified stale string scoped to
Go sources; expect **zero** hits. Then sweep for `runner.go:177-179` in
`trailer_terminal_reason_test.go`; expect **four**, at `:28`, `:97`, `:232`,
`:242`.

**AC2 — the Details still measure 269 and 395 bytes.** Measure with a `go test
-overlay` mapping a throwaway tagged `_test.go` into the package, so nothing is
written into the worktree. This recipe was run at `1d2c53d` and returns
`269` / `395`:

```go
//go:build e2e_realclaude

package realclaude

import "testing"

func TestZZMeasureDetailBytes(t *testing.T) {
	absent := trailReasonAgainstPath("streamrunner (claude argv carries --input-format)",
		[]string{"type", "subtype"}, "")
	present := trailReasonAgainstPath("streamrunner (claude argv carries --input-format)",
		[]string{"type", trailReasonKeyName}, "error")
	t.Logf("ABSENT_BYTES=%d", len(absent.Detail))   // want 269
	t.Logf("PRESENT_BYTES=%d", len(present.Detail)) // want 395
}
```

Drive it with an overlay JSON whose `Replace` maps an absolute
`internal/e2e/realclaude/zz_measure_test.go` to the scratch file, then
`go test -count=1 -tags e2e_realclaude -overlay=<abs>.json -run
'^TestZZMeasureDetailBytes$' -v ./internal/e2e/realclaude/`. Both `Logf` values
must read exactly 269 and 395 **after** the edit.

Do **not** substitute the truncation-marker assertion at
`trailer_terminal_reason_test.go:434`. It keys on the 512-byte cap via
`reachCapCommand`, and the Details sit 243 and 117 bytes under it — it stays
green through a 7-for-8 replacement, a 7-for-20 one, or a wholesale reflow, so
it cannot fail for the reason AC2 cares about. Nothing in the tree pins these
lengths; the pinned numbers are the only falsifiable form of "neither Detail
changed length" measurable on a single tree.

**AC3 — doc corrected, record not.** `e2e-realclaude.md:1104` reads `:177-179`
and matches `:819`. `docs/knowledge/codebase/1366.md` is untouched — confirm with
`git diff --name-only` showing exactly two paths (the Go file and the feature
doc) plus this spec.

**AC4 — the residual is stated.** See § Residual below; run the sweep and match
it line for line.

**AC5 — nothing else moves, demonstrated.** `git diff -U0 --
internal/e2e/realclaude/trailer_terminal_reason_test.go` and read each hunk
header `@@ -a,b +c,d @@`: every hunk must show `b == d` (all four will be
`-N,1 +N,1`). Also confirm total lines stay at **650** (`wc -l`). Then eyeball
the four changed lines: each is a comment or a format-string literal — no
assertion, conditional, comparison, gate value, run outcome, set membership,
count word or numeral changes.

**AC6 — the suite is shown to have run.** `make check` does **not** compile this
file (it is `//go:build e2e_realclaude`, which `make check` excludes), so a green
`make check` is not evidence about this diff and must not be cited as one. Run
the tagged suite once into a log and read the counts off it:

```
go test -count=1 -tags e2e_realclaude -run '^TestTrail' -v ./internal/e2e/realclaude/ 2>&1 | tee trail.log
grep -cE '^[[:space:]]*--- PASS' trail.log   # want 149
grep -cE '^[[:space:]]*--- SKIP' trail.log   # want 0
grep -cE '^[[:space:]]*--- FAIL' trail.log   # want 0
```

The 149 counts top-level tests **and** subtests, which is why the leading-space
class is in the pattern. Report all three numbers, not a bare "green" — an exit
code cannot tell a skip from a pass, and that is the failure this family already
paid for once. `go vet -tags e2e_realclaude ./internal/e2e/realclaude/` is the
cheaper compile-only check if wanted, but it does not satisfy AC6 on its own.

This suite needs **no credentials and no live claude** — measured 149 tests in
~3.4 s offline at `1d2c53d`, no `t.Skip`, no env gate. The `e2e_realclaude` tag
is the mechanism that keeps offline probe instruments out of `make check`; it is
not a live-claude marker, which is why this ticket correctly carries no
`needs-real-claude` label.

## Residual

After the change, the repository-wide sweep for the **qualified** string

```
grep -rn "runner\.go:170-176" . --exclude-dir=.git
```

returns exactly seven lines, in two out-of-scope classes:

| Class | Hits | Why out of scope |
|---|---|---|
| `docs/specs/architecture/` build artifacts | `1366-*.md:23`, `:71`, `:233`; `1417-*.md:343`; `1419-*.md:241` | Per-ticket build artifacts describing what those tickets designed and observed at their own commits. #1417 and #1419 *record the cite as stale and deliberately deferred* — rewriting them would erase the deferral that justifies this ticket's existence. |
| `docs/knowledge/codebase/1366.md` | `:36`, `:191` | Per-ticket record of what #1366 actually shipped. #1366 shipped the `170-176` cite; rewriting the record to say otherwise would falsify it. Explicitly forbidden by AC3. |

The qualifier is load-bearing. An unqualified `170-176` sweep also matches
`docs/specs/architecture/582-retire-binary-relay-hello-handshake.md:64`, an
unrelated `Send`-comment cite, so the seven-line residual above is only
reproducible with `runner.go:` attached.

This section is the AC4 statement and ships as part of the change (this spec is
committed with it). Restate it in the PR description so a reviewer meets it
without opening the spec.

## Drift check

The ticket's facts were measured at `e20f4d2`; the branch point is `1d2c53d`
(two commits later, via #1430). Re-derived at `1d2c53d`:

- `trailer_terminal_reason_test.go` — **zero** drift since `e20f4d2`. All four
  cite lines, the 650-line total, and the 269/395 byte measurements are current.
- `docs/knowledge/features/e2e-realclaude.md` — drifted (5 insertions, 5
  deletions from #1430), but line-count neutral: `:819` and `:1104` still hold
  their stated content at `1d2c53d`, re-verified directly.
- The six inbound cites, the seven-line residual, and the 149/0/149 counts all
  reproduce unchanged.

No branch overlap: no in-flight `origin/feature/*` branch touches either file.

## Open questions

None blocking. One judgment call already made: § Design's "One thing
deliberately not touched" resolves the only nearby cite that could tempt a
scope-widening edit — `watchdog.go:253, :280` is accurate and stays.
