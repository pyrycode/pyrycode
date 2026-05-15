# Spec: Remove `e2e-realclaude-nightly` workflow (#379)

## Files to read first

- `.github/workflows/e2e-realclaude-nightly.yml` — the file being deleted; ~40 lines, single-job nightly. Skim to confirm there's nothing in it that needs to be preserved as a comment elsewhere (there isn't — every rationale that lived here is now obsolete).
- `docs/knowledge/features/e2e-realclaude.md:45-58` — the `## CI cadence: nightly workflow` section that must be replaced wholesale. Lines 67-69 (`## Related`) also reference `#362` codebase note and need adjustment.
- `docs/knowledge/codebase/362.md` (full file, ~43 lines) — the codebase note that needs an end-of-life banner at the top pointing at #379. The body stays (historical record), but readers should understand on first glance that the workflow no longer exists.
- `CLAUDE.md` § "Documentation Structure" — confirms `docs/knowledge/INDEX.md` is documentation-phase territory; developer does NOT edit it. See also § "Never Update" in `agents/architect/CLAUDE.md`.

Codegraph is not relevant — this ticket touches zero Go symbols. No `codegraph_context` was run.

## Context

`e2e-realclaude-nightly.yml` was added 2026-05-14 (#362, merged hours before this ticket was written) to schedule the real-`claude`-binary suite once per day on GitHub Actions. The follow-up decision (2026-05-14 very late evening) replaces it: real-claude tests will run **locally during the code-review phase** of every dispatched ticket, covered by a separate companion ticket that updates the code-review agent's `CLAUDE.md`. This ticket is the symmetric CI-side removal.

The rationale for the move:

- GitHub Actions requires a paid `ANTHROPIC_API_KEY` secret. Max-plan tokens (already used locally) are free.
- Cost per nightly run scales with test count: $0.10–$0.50 today, more as #363–#368 land.
- A nightly run surfaces upstream `claude` regressions on an unpredictable cadence (4 AM UTC). Code-review-phase execution surfaces them on the same cycle as the development work, which is when the maintainer can act.
- Removing the workflow eliminates a secret-management surface and one CI file to keep in sync with `self-check-daily.yml`.

The code-review-phase coverage equivalence is in scope for the companion ticket, not this one. The architectural claim this ticket relies on is just: "once the companion lands, every PR runs the real-claude suite locally, which is at least as frequent as daily." That claim is reviewed in the companion's spec.

## Design

This is a pure deletion + documentation correction. No code change, no test change, no Go package boundary touched.

### Steps the developer performs

1. **Delete the workflow file.**
   ```bash
   git rm .github/workflows/e2e-realclaude-nightly.yml
   ```

2. **Rewrite `docs/knowledge/features/e2e-realclaude.md`.** Two surgical edits:
   - **Replace the `## CI cadence: nightly workflow` section** (currently lines 45-57, including the line just above it). New section:

     ```markdown
     ## CI cadence: code-review phase, no nightly workflow

     The real-`claude` suite is NOT wired into GitHub Actions. It runs **locally
     during the code-review phase** of every dispatched ticket via the pipeline
     — see the code-review agent's `CLAUDE.md` for the invocation contract.

     The earlier nightly workflow (`.github/workflows/e2e-realclaude-nightly.yml`,
     #362) was removed in #379 the same day it landed. CI-side rationale for the
     removal:

     - GitHub Actions would need an `ANTHROPIC_API_KEY` repo secret; Max-plan
       tokens used locally are free.
     - Per-run cost ($0.10–$0.50, scaling with test count) buys nothing local
       runs don't already cover once code-review runs the suite on every PR.
     - Failure surface synchronised to dispatch cadence beats unpredictable
       04:00 UTC failures.
     - One fewer CI file to keep in lockstep with `self-check-daily.yml`.

     The make target is unchanged — `make e2e-realclaude` is still the entry
     point, just no longer invoked by CI.
     ```

   - **Adjust the `## Related` section** (currently lines 63-69): remove the line referencing `codebase/362.md` as "nightly workflow", or rephrase it as "the now-removed nightly workflow, see also #379". One-line edit at the developer's discretion as long as the file no longer claims the workflow exists.

3. **Add an end-of-life banner to `docs/knowledge/codebase/362.md`.** Insert immediately after the `# Ticket #362 — …` heading (before the existing first paragraph). New paragraph (keep the body intact below it):

   ```markdown
   > **End of life (2026-05-15, #379).** The workflow this ticket introduced
   > was removed the same day. Real-`claude` e2e coverage moved to the
   > code-review phase of every dispatched ticket — see
   > [features/e2e-realclaude.md](../features/e2e-realclaude.md) for the
   > current contract. The notes below remain as a historical record of how
   > the workflow was shaped; do not infer current behaviour from them.
   ```

4. **Verify grep cleanliness.**
   ```bash
   grep -rni "e2e-realclaude-nightly" .
   ```
   Expected matches after the edits:
   - `docs/knowledge/codebase/362.md` — the existing body that documents the workflow's implementation (kept as historical record under the EOL banner). The AC explicitly permits this match.
   - `docs/specs/architecture/362-e2e-realclaude-nightly-workflow.md` — the original spec file. Specs are immutable build artifacts; leave it untouched. (The grep AC says "no matches except, optionally, the end-of-life note in `362.md`" — interpret this generously: spec files under `docs/specs/architecture/` are also acceptable, since they're frozen historical records by project convention. Do not edit them.)
   - `docs/knowledge/INDEX.md:55` — the long `**CI cadence:**` blurb is now stale. **Do NOT edit this here** — INDEX.md is owned by the documentation phase per `CLAUDE.md` and `agents/architect/CLAUDE.md` § "Never Update". The documentation phase will revise this entry once the feature doc is in its final form. Note this in the PR description so code-review and documentation pick it up.

   Anything else is a bug — flag it and edit.

5. **Refresh QMD index.**
   ```bash
   qmd update && qmd embed
   ```
   Per `CLAUDE.md`: "Always `qmd update && qmd embed` after adding or modifying docs. `embed` alone doesn't detect new files." A file deletion plus content rewrites qualifies.

### Pipeline-phase ownership

The AC includes "INDEX.md entries for `e2e-realclaude.md` and `362.md` still accurately summarize the (updated) doc contents — adjusted if needed." Per the pipeline's phase boundaries:

- **Architect (this run):** spec only.
- **Developer:** steps 1–5 above. Does NOT touch `INDEX.md`.
- **Code-review:** verifies grep cleanliness, EOL banner present, feature-doc section is internally consistent, no orphan references.
- **Documentation:** rewrites the `e2e-realclaude.md` INDEX row (line 55 today) — strip the entire `**CI cadence:** …` clause and replace with a short note that the suite runs in the code-review phase only; trim any `#362`-workflow-specific text. There is no separate INDEX row for `codebase/362.md` today (codebase notes are not individually indexed) — confirm by grep and adjust only if the codebase-notes structure has changed since.

This split is enforced by the existing `agents/architect/CLAUDE.md` and the parallel `agents/developer/CLAUDE.md` documentation-phase ownership rule.

### Why no spec edits to the original #362 spec

`docs/specs/architecture/362-e2e-realclaude-nightly-workflow.md` is a build-time artifact frozen at the moment #362 shipped. Editing it would rewrite history. The codebase note (`codebase/362.md`) is the right surface for the EOL pointer because it's evergreen — it's already the "current view" of the work #362 produced.

## Concurrency model

N/A — no runtime code changes.

## Error handling

N/A — no runtime code changes.

## Testing strategy

- **No new tests.** This is a documentation + CI-config deletion.
- **Manual verification by the developer before commit:**
  - `gh workflow list` no longer shows the nightly workflow (it'll persist in GitHub's UI until the deletion lands on the default branch, so this check runs *after* PR merge; pre-merge, confirm the file is gone via `ls .github/workflows/`).
  - `grep -rni e2e-realclaude-nightly .` matches only the historical references named in step 4.
  - `make test` and `make check` still pass (sanity — nothing should have changed).
  - `qmd update && qmd embed` exits cleanly and the QMD index reports the feature doc with its new content (`mcp__qmd__query` on a phrase from the new section returns the rewritten file).
- **Out of scope here:** verifying that the code-review-phase invocation of `make e2e-realclaude` works. That belongs to the companion ticket's spec.

## Open questions

- **None blocking implementation.** The companion ticket (code-review CLAUDE.md update) is named explicitly in the issue body as "separate, not in scope here"; this spec assumes the maintainer is tracking that work and will land it on or near the same day. If the companion is delayed, the only consequence is a window during which neither nightly CI nor code-review-phase coverage runs — operator-acceptable for a few days given the existing local `make e2e-realclaude` workflow.

## Size

XS. One file deleted, two markdown files edited, one shell command (`qmd update && qmd embed`). Production-source-file count (per architect § "self-check the scope" — `*.go`/`*.kt`/`*.ts`/`*.tsx`, excluding tests and `*.md`): **0**. No red lines tripped. No file-overlap with in-flight feature branches on the files the developer touches (checked 2026-05-15; `feature/58` touches `docs/knowledge/INDEX.md` only, which the developer does not edit in this ticket — INDEX.md ownership is the documentation phase, which runs after #58 will have landed or been resolved through its own merge).
