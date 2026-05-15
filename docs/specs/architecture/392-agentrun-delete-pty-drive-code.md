# Ticket #392 — agentrun: delete PTY drive code (drive.go, trust.go, settings.go) after stream-json migration

**Size:** XS. Mechanical deletion of seven files (–1375 LoC), one comment-hygiene edit (one production file), one new knowledge-base note. No new code, no behaviour change, no consumer cascade.

**Blocked-by:** #375 (closed). The selfcheck rewrite landed on `main` (commit `a209572`) and `internal/agentrun/selfcheck/selfcheck.go` no longer imports `agentrun.{MarkWorkdirTrusted,WriteSettings,SettingsFilename,Drive,DriveConfig}`. Pre-architect grep confirmed the legacy primitives have zero callers across `cmd/` and `internal/`.

## Files to read first

- `internal/agentrun/streamrunner/runner.go:1-22` — package doc comment; line 6 names `internal/agentrun.Drive` as the PTY sibling, which is the only surviving stale reference outside the doomed files. Edit target.
- `internal/agentrun/selfcheck/selfcheck.go:1-20` — confirms the rewritten self-check no longer depends on the legacy primitives (mentions `streamrunner` + `jsonl.Reader` only, no `agentrun.{Drive,MarkWorkdirTrusted,WriteSettings}`).
- `docs/knowledge/codebase/375.md` — the format-and-tone reference for the codebase note this ticket adds (`docs/knowledge/codebase/392.md`). Mirror the section layout (header → Implementation → Lessons / Related, omit sections that don't apply).
- `docs/knowledge/codebase/README.md` — confirms the `<N>.md` filename convention used in the AC.

No need to read the doomed files themselves before deleting — the developer is the *deletion* agent, not a reviewer of what the files do. `git rm` does not require comprehension.

## Context

`pyry agent-run` and its boot-time self-check both used to drive an interactive `claude` over a PTY: spawn under a tty, sleep + press Enter to dismiss the trust dialog, sleep, type the prompt bytes, watch the on-disk session JSONL for tool events. After #390 introduced `streamrunner` and #391 / #375 cut over both the production verb and the self-check to spawn `claude` with `--input-format stream-json --output-format stream-json --dangerously-skip-permissions` (single JSON envelope on stdin, structured events on stdout), the PTY primitives in `internal/agentrun/` are unreachable. They survive only as compiled-but-unused exported symbols.

This ticket deletes them. No semantic change is intended.

## Design

A pure deletion. No interfaces to redesign, no concurrency model to evaluate. The "design" is the deletion manifest plus one comment-hygiene edit, plus the knowledge-capture note required by AC #4.

### Deletion manifest

`git rm` these files. The package `internal/agentrun/` (the parent of `streamjson`, `streamrunner`, `jsonl`, `budget`, `selfcheck`) continues to exist as a directory because its subpackages remain; the *Go package* at that path collapses to empty after these deletions. That is fine — Go does not require a package directory to contain `.go` files at the directory root; the subpackages are independent units. No new `doc.go` or stub file is needed.

| File | LoC | Reason |
|---|---|---|
| `internal/agentrun/drive.go` | 133 | Defines `Drive`, `DriveConfig`. PTY-only. Zero callers. |
| `internal/agentrun/drive_test.go` | 272 | Tests `Drive`. Mechanical follow-on. |
| `internal/agentrun/drive_e2e_test.go` | 114 | E2E tests `Drive`. Mechanical follow-on. |
| `internal/agentrun/trust.go` | 158 | Defines `MarkWorkdirTrusted`. PTY-only (the trust dialog only appears under a tty). Zero callers. |
| `internal/agentrun/trust_test.go` | 479 | Tests `MarkWorkdirTrusted`. Mechanical follow-on. |
| `internal/agentrun/settings.go` | 82 | Defines `WriteSettings`, `SettingsFilename`. Wrote `.pyry-agent-run-settings.json` consumed by the PTY-mode `--settings` path. Stream-json `agent-run` does not pass `--settings`. Zero callers. |
| `internal/agentrun/settings_test.go` | 137 | Tests `WriteSettings`. Mechanical follow-on. |

Total: 7 files, 1375 LoC removed. No file in this list mixes PTY-only and stream-json-relevant helpers — the AC #2 probe rule (any test that constructs `DriveConfig` or calls `MarkWorkdirTrusted` is PTY-only) was verified to cover the entire content of each test file at refinement time. The developer does **not** need to read these files before deleting them; if the build passes after `git rm`, the deletion was clean.

### Comment-hygiene edit

`internal/agentrun/streamrunner/runner.go` lines 6–9 currently read:

```go
// This is the headless sibling of internal/agentrun.Drive — that primitive
// drives an interactive claude via a PTY; this one drives the
// `--input-format stream-json --output-format stream-json` shape used by
// `pyry agent-run` from a single shot of stdin.
```

Rewrite to drop the dead-symbol reference while preserving the description of what `streamrunner` does. The replacement should:

1. Drop the "headless sibling of internal/agentrun.Drive" framing entirely — the deleted Drive is no longer a useful comparison point for a future reader.
2. Keep the description of `streamrunner`'s shape (writes one JSON envelope to stdin in `--input-format stream-json --output-format stream-json` mode, used by `pyry agent-run`).
3. Stay 4 lines or fewer so the package-doc rhythm is unchanged.

Suggested replacement (developer may adjust wording, but must preserve the three semantic points above and must not reintroduce any of `Drive`, `DriveConfig`, `MarkWorkdirTrusted`, `WriteSettings`, `SettingsFilename`):

```go
// `pyry agent-run` uses streamrunner to drive claude in
// `--input-format stream-json --output-format stream-json` mode from a
// single shot of stdin; the package owns the spawn, the stdin write, and
// the wait — nothing else.
```

That is the *only* edit to a non-deleted file. The rest of `runner.go` (lines 10+) is unaffected. Grep confirmed no other file in `cmd/` or `internal/` names any of the five doomed symbols.

### Knowledge-capture note (AC #4)

Create `docs/knowledge/codebase/392.md`. Mirror the layout of `docs/knowledge/codebase/375.md` but keep it short — this is a deletion, there is no implementation to describe. Required content:

- **Header line.** `# Ticket #392 — agentrun: delete PTY drive code (drive.go, trust.go, settings.go) after stream-json migration`.
- **One paragraph** stating what was deleted (the five exported symbols + the seven files), why (stream-json runtime cutover via #391 and the selfcheck rewrite via #375 removed the last callers), and the lineage (link to `codebase/390.md`, `codebase/391.md`, `codebase/375.md` for the migration that made this safe). Mention specifically that no behaviour change is intended — this is dead-code removal only, not a refactor.
- **`## Related`** section linking the three migration-predecessor codebase notes (`codebase/390.md`, `codebase/391.md`, `codebase/375.md`).

Do **not** add a "Patterns established" or "Lessons learned" section. A mechanical deletion teaches nothing new about the codebase; padding the note with manufactured lessons is noise. If the developer notices something genuinely surprising during the deletion (e.g. a file imported a package nothing else uses, leaving an orphaned dependency in `go.mod`), capture that as a one-sentence "Lessons learned" bullet — otherwise omit the section entirely.

## Concurrency model

N/A. No goroutines created, modified, or removed. The deleted files contained goroutine code (the PTY I/O bridge, the trust-dialog watcher), but its removal is purely subtractive — no surviving goroutine acquires a new responsibility.

## Error handling

N/A. No error paths added or modified. The build either compiles cleanly after `git rm` (success — all callers were genuinely gone) or it does not (a hidden caller exists outside the surveyed `cmd/` + `internal/` tree, in which case the developer reports the surprise and the spec is wrong).

## Testing strategy

The AC pins the verification surface:

- **`go build ./...`** — proves no package imports a deleted symbol.
- **`go vet ./...`** — proves no syntactic damage.
- **`go test -race ./...`** — proves no test file imports a deleted symbol and no surviving test exercises a deleted code path indirectly.

Run all three from the worktree root after the deletions and the comment edit. They should pass first try; if any fails, the failure mode is "I missed a caller" — re-grep the failing import/symbol, delete or update the additional site, re-run.

No new test cases are added. A deletion ticket that introduces new tests is over-scoped.

### Self-verification grep (run before commit)

```bash
grep -rn -E "agentrun\.(Drive|DriveConfig|MarkWorkdirTrusted|WriteSettings|SettingsFilename)" cmd/ internal/
```

Expected output: empty. If non-empty, AC #2 fails — investigate each hit. Comment-only references (after the streamrunner edit) and string literals are still failures — the AC requires *all* such references to be updated or dropped.

## Open questions

None. The blocker (#375) has landed, the grep at refinement time showed zero callers of the doomed symbols, and the only stale comment is at the documented line (`internal/agentrun/streamrunner/runner.go:6`). The developer should proceed directly to deletion.

## Out of scope

- Reorganizing the surviving `internal/agentrun/` subpackages (`streamjson`, `streamrunner`, `jsonl`, `budget`, `selfcheck`). They keep their current shape and locations.
- Renaming `internal/agentrun` itself now that its root package is empty. The directory exists as a namespace for the subpackages; renaming it is a separate cross-package refactor and not in this ticket.
- Touching `docs/knowledge/features/*.md` or `docs/PROJECT-MEMORY.md`. Documentation phase owns shared docs (per architect agent rules); the per-ticket codebase note at `docs/knowledge/codebase/392.md` is the only doc this ticket writes.
- Any change to `cmd/pyry/agent_run.go`, `cmd/pyry/agent_run_selfcheck.go`, or the selfcheck package contents. #375 already brought those off the PTY path; touching them again here would re-open a settled change set.

## Definition of done (developer checklist)

1. `git rm` the seven files in the deletion manifest above.
2. Edit the package-doc comment in `internal/agentrun/streamrunner/runner.go` lines 6–9 per the rewrite guidance.
3. Write `docs/knowledge/codebase/392.md` per the AC #4 + knowledge-capture-note guidance above.
4. Run `go build ./... && go vet ./... && go test -race ./...` from the worktree root; all three must pass.
5. Run the self-verification grep; output must be empty.
6. Commit. The dispatcher pushes.
