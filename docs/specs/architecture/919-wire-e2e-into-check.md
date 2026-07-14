# Spec #919 — Wire `make e2e` into the `check` gate

**Ticket:** [#919](https://github.com/pyrycode/pyrycode/issues/919)
**Size:** XS — one Makefile prerequisite edit plus comment reword. **No Go code.**

## Files to read first

Codegraph does not apply here — the Makefile is not part of the Go symbol index, and there are no Go call sites. Read these two files directly:

- `Makefile:33-34` — the `check:` target. The prerequisite list `check: vet test staticcheck substrate-guard` is the one-line edit point.
- `Makefile:1-26` — header comment block. Three lines reference gate composition and need touch-ups: the `make check` line (4-7, its parenthetical enumeration), the `make e2e` line (10-14, the "Not yet part of `check`…" caveat to remove), and the `make preship` line (15-17, its `check + e2e + e2e-realclaude` enumeration).
- `Makefile:44-57` — the `e2e`, `e2e-realclaude`, and `preship` targets. `preship: check e2e e2e-realclaude` (line 57) and its block comment (52-56) are the second edit site.
- `CLAUDE.md:47-53` — Build Commands section. Confirmed at spec time to list raw `go` commands (no `make check`/`make e2e` enumeration). **No change** — per AC, confirm and skip.

## Context

Operator decision 2026-07-10: the hermetic fake-daemon e2e suite should run in the standard build gate, not only on demand. The `make e2e` target already exists (`go test -tags e2e -race -count=1 ./internal/e2e/...`). This ticket wires it into `check` so a core-daemon regression fails the standard gate instead of sitting red on `main` — the exact failure mode of #918 (eight #839-regression tests red on `main` for two days, unnoticed).

**Precondition satisfied at spec time (2026-07-14):** all three hard blockers are CLOSED — #929, #930 (both #839 daemon-side regressions), #257 (`spawnAttachableDaemon` `--session-id` crash-loop). #870 (a documented flake in the same suite) remains OPEN but is a flag-not-block per the PO decision: it is off the native blocker set and gated only by an AC ("resolved or confirmed not firing"). See the testing strategy below.

## Design

Two edit sites in `Makefile`. No Go code, no new files, no new types.

### 1. `check` target — append `e2e`

```
check: vet test staticcheck substrate-guard         →   check: vet test staticcheck substrate-guard e2e
```

`e2e` goes **last**, matching the ticket's prescribed line. Rationale: `make` runs prerequisites left-to-right serially (no `-j` in this repo's workflow), so the four fast legs (~seconds each) run first and short-circuit on failure before the ~4-minute e2e leg is ever reached. Appending rather than prepending preserves fail-fast on the cheap checks.

### 2. `preship` target — drop the now-redundant explicit `e2e`

```
preship: check e2e e2e-realclaude         →   preship: check e2e-realclaude
```

**Architect decision (the ticket delegates this).** Once `check` includes `e2e`, the explicit `e2e` token in `preship` double-runs the ~4-minute hermetic suite for no added coverage. The ticket flags the double-run as "harmless but wasteful." Dropping it removes the waste while preserving `preship`'s coverage: `check` runs the hermetic suite (vet, test, staticcheck, substrate-guard, e2e), then `e2e-realclaude` runs the live-claude suite. Serial order is preserved, so behaviour is unchanged except the removed duplicate run.

Per **Evidence-Based Fix Selection**, we do *not* keep the explicit `e2e` as a defensive "in case `check` ever drops it" — that is an unobserved failure mode, and retaining it reintroduces the exact double-run the ticket calls out. If a future edit removes `e2e` from `check`, that editor owns `preship`'s coverage.

### 3. Header-comment rewording

- **`make e2e` line (10-14):** remove the "Not yet part of `check`; gate wiring is ticketed and blocked on the suite going green again." caveat. Replace with a note that e2e is now part of `check` (a core-daemon regression fails the standard gate) and remains runnable standalone for a focused e2e run. Keep the existing suite description (builds pyry, hermetic, ~4 min with `-race`).
- **`make check` line (4-7):** extend the parenthetical enumeration so it names the e2e leg now that `check` runs it (e.g. `… + substrate-guard + the fake-daemon e2e suite`).
- **`make preship` line (15-17) and the `preship` block comment (52-56):** reword the `check + e2e + e2e-realclaude` enumeration to reflect that `check` now subsumes the hermetic e2e run (e.g. `check + e2e-realclaude`, noting `check` now includes the fake-daemon suite).

These are contract targets, not verbatim text — the developer matches the Makefile's existing comment style and column alignment.

### 4. CLAUDE.md — no change

Confirmed at spec time: `CLAUDE.md`'s Build Commands section (lines 49-53) lists raw `go build` / `go test` / `go vet` / `staticcheck` commands and does not enumerate `make check` or the gate. The AC's conditional ("update **only if** it enumerates the gate") is not met — skip.

## Concurrency model

N/A — Makefile prerequisite edit.

## Error handling

Make's native semantics: any prerequisite exiting non-zero fails the `check` target and stops the chain. A red e2e test therefore fails `make check`, which is the ticket's goal. No new error paths.

## Testing strategy

The developer verifies against the ACs; no new Go tests are written.

- **Dry-run lists the leg (AC 2).** `make -n check` must print the `go test -tags e2e -race -count=1 ./internal/e2e/...` recipe. `make -n` expands prerequisite recipes, so the e2e leg appears in the dry-run output.
- **Green `main` runs e2e to completion (AC 3, first half).** With all blockers closed, run `make check` and confirm it executes the e2e suite and passes. This doubles as the greenness confirmation — the gate the pipeline (developer/code-review/QA) already runs now exercises e2e, so a red suite is caught the moment it's wired.
- **A regression fails the gate (AC 3, second half).** Confirm mechanically: `make check` fails when the e2e suite fails. This follows from make's prerequisite semantics plus one real `make check` run showing e2e executes; the developer need not fabricate a persistent regression, but a throwaway local break-and-revert of one e2e assertion (confirming `make check` goes red, then reverting) is the cheap belt-and-suspenders demonstration.
- **#870 flake confirmed non-firing (AC 4).** Run the hermetic suite a small number of times (e.g. `make e2e` ×2–3, or target `-run TestRelay_Roundtrip_Appendix`) and confirm green across runs. If #870 fires during verification, **do not force-close it or patch it here** — flag to the operator; this ticket does not own the flake fix. Per the PO decision, a low-rate flake must not gate the wiring indefinitely, so a clean repeated run is sufficient evidence to land.

## Open questions

- **"Same checks CI runs" framing (out of scope).** The `make check` header comment (line 4) states it runs "the same checks CI runs." At spec time there is no push/PR CI workflow that runs the standard gate — `release.yml` triggers only on `v*` tags (self-check → goreleaser) and `self-check-daily.yml` is a daily cron. So this phrase is already loose and independent of this ticket; adding e2e does not make it meaningfully more or less accurate. **Do not expand scope to rewrite the CI-parity framing** — reword only the e2e/preship enumerations. If the operator wants the CI-parity comment corrected, that is a separate ticket.
- **`preship` ordering under `-j`.** The double-run removal assumes serial make (the repo's actual usage). Under `make -j`, `check` and `e2e-realclaude` could run concurrently, but `e2e-realclaude` needs live creds and is never run parallel in practice; no change warranted.

## Scope check

Production source files (`*.go`/`*.kt`/`*.ts`, excluding tests/`*.md`/spec) prescribed new-or-modified content: **0**. Files touched: `Makefile` (config, not a production source file) + this spec. Well under every red line. XS confirmed — ships as one ticket.
