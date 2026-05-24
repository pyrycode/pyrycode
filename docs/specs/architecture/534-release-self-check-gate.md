# Spec: release-tag gate — goreleaser waits on self-check pass

**Ticket:** #534
**Size:** S (2 files modified, ~25 added lines of YAML, no Go code, no tests)

## Files to read first

- `.github/workflows/release.yml` — entire file (~37 lines). The only file with new jobs added. Internalize the existing single-job shape before editing.
- `.github/workflows/self-check-daily.yml` — entire file (~70 lines). The only modification here is adding a third trigger to the `on:` block and updating the header comment. The two existing jobs (`self-check`, `notify-failure`) are NOT touched.
- `docs/specs/architecture/533-self-check-discord-alert.md` — sibling spec. The `notify-failure` job's behaviour (job-level `if: failure()` + soft-fail on missing `DISCORD_WEBHOOK_URL`) travels into the release-tag invocation for free; that's intentional and worth understanding before designing the wiring.
- `docs/knowledge/codebase/533.md` — implementation notes for the Discord-alert sibling. Pattern for `notify-failure` semantics is the same one that will fire on a release-time failure.

No Go source is in scope.

## Context

The `agent-run self-check (daily)` workflow is the deterministic safety net for the per-agent tool-allowlist contract (#375). Today it runs on cron only — no enforcement at release time. The `release.yml` workflow runs goreleaser on every `v*` tag push, independent of self-check state.

Observed risk window (2026-05-15 → 2026-05-24): self-check went red continuously for ~9 days while a real regression shipped to main (#526). Had a `v0.14.0` tag been pushed during that window, goreleaser would have published binaries with the per-agent security boundary broken. #533 (Discord alert on failure) closes the visibility gap for cron runs; this ticket (#534) closes the publishing gap for tag pushes.

Out of scope: any change to the self-check body itself, any change to CI workflow, any change to goreleaser config (`.goreleaser.yaml` is untouched).

## Design

**Pattern: reusable workflow via `workflow_call`.** Convert `self-check-daily.yml` into a dual-purpose workflow by adding `workflow_call:` to its trigger set (alongside the existing `schedule:` and `workflow_dispatch:`). Add a new `self-check` job to `release.yml` that invokes the reusable workflow; declare `needs: self-check` on the existing `goreleaser` job.

Why this over the "add a sibling self-check job inside release.yml" alternative (which the ticket also lists as acceptable):

- **Single source of truth, structurally enforced.** Future changes to the self-check steps (e.g. swapping the npm-installed claude version, tightening the timeout, adding a new env var) land in one file and propagate to both call sites automatically. The "copy the steps into release.yml" approach is one missed sync away from the daily cron drifting from the release gate — exactly the failure mode this AC is structured to prevent.
- **`notify-failure` travels for free.** A release-time self-check failure invokes the same `notify-failure` job that the daily cron uses, so the operator gets the same Discord ping (in addition to the goreleaser job being skipped and the tag's overall workflow going red). The ticket's Technical Notes explicitly call this out as the desired property when #533 has shipped (it has — see commit 4ce6ab7).
- **Idiomatic in GitHub Actions.** `workflow_call` is the documented mechanism for cross-workflow body reuse. A composite action under `.github/actions/self-check/` would be heavier, force splitting `notify-failure` out of the same file, and gain nothing on a ~30-line workflow.

### `self-check-daily.yml` modifications

Two surgical changes; the existing jobs are untouched.

1. **Add `workflow_call:` to the `on:` block.** It needs no `inputs:` (the workflow's behaviour does not vary by caller). It needs no `secrets:` block — `ANTHROPIC_API_KEY` and `DISCORD_WEBHOOK_URL` flow in via the caller's `secrets: inherit`, which is the documented pattern for "any secret the caller can read, the called workflow can read too" and matches how the cron run already accesses them. (Explicit-list alternative: declare `ANTHROPIC_API_KEY` and `DISCORD_WEBHOOK_URL` under `workflow_call.secrets:` with `required: true` and `required: false` respectively, then have the caller pass them by name. Reject this — it adds a second source of truth for "which secrets does self-check need" and duplicates a list that already exists implicitly in the job bodies. `secrets: inherit` is correct here.)
2. **Extend the header comment block** to call out the dual trigger surface so a future contributor reading the workflow knows release-tag pushes also fan in. Append (do not replace) the existing wording. Required content per AC:
   > Triggers: daily cron (`schedule:`), manual (`workflow_dispatch:`), and release-tag pushes via the reusable-workflow `workflow_call:` entry point invoked from `.github/workflows/release.yml`. A failing run on any trigger surfaces via the Discord alert (`notify-failure` job, #533) AND, when invoked from `release.yml`, blocks the goreleaser job from publishing (#534).

   Keep the existing "Belt-and-suspenders safety net" + `#375` / `#336` / `#533` paragraph intact; this is additive.

No `permissions:` block change. The default `GITHUB_TOKEN` permissions are sufficient for the self-check job (`actions/checkout`, `actions/setup-go`, `npm install`, `go build`, exec the binary — no repo writes). Adding an explicit `permissions: contents: read` at the workflow level would be defense-in-depth but is outside the AC's scope and inherits the same default behaviour either way. Defer.

### `release.yml` modifications

The current single-job workflow becomes a two-job workflow. Concrete shape:

```yaml
jobs:
  self-check:
    uses: ./.github/workflows/self-check-daily.yml
    secrets: inherit

  goreleaser:
    needs: self-check
    runs-on: ubuntu-latest
    steps:
      # ... existing steps unchanged ...
```

Key wiring points:

- **`uses:` with a relative path** — `./.github/workflows/self-check-daily.yml` references the same-repo workflow. This is the standard form; no `@ref` needed because GitHub resolves it at the same SHA as the caller (the tag push).
- **`secrets: inherit`** — passes `ANTHROPIC_API_KEY` and `DISCORD_WEBHOOK_URL` through. Mandatory: reusable workflows cannot read repo secrets directly. The ticket's Technical Notes confirm this.
- **`needs: self-check`** on `goreleaser` — fail-closed gate. If `self-check` fails, `goreleaser` is `skipped` (not `failed`), which is the desired contract: the tag is recorded but no binaries publish, no GitHub Release is created, no homebrew formula push fires. (GitHub Actions treats `needs:` of a `workflow_call` job as "the entire called workflow concluded `success`" — so `notify-failure` running with `if: failure()` does not block goreleaser, because the called workflow's overall conclusion is determined by all its jobs including the failed `self-check`. The conclusion is `failure` iff `self-check` failed, regardless of whether `notify-failure` succeeded or itself failed.)
- **`permissions:`** on the caller stays as-is (`contents: write` for goreleaser to create the GitHub Release). The reusable workflow gets its own permission scope; `contents: write` is not implicitly granted to the self-check job. This is desirable — defense-in-depth even though we're not adding a workflow-level `permissions:` block to self-check-daily.yml in this ticket.

### What goreleaser does on the failure path

If `self-check` fails:
- `notify-failure` runs → Discord alert posted (or soft-fail warning if `DISCORD_WEBHOOK_URL` is unset).
- `goreleaser` is `skipped` — never starts, never produces artifacts.
- The release-workflow run's overall conclusion is `failure` → red badge in Actions tab.
- The tag remains in the repo. To retry: operator must delete the tag (`git tag -d vX.Y.Z && git push origin :refs/tags/vX.Y.Z`), fix the regression, retag. This is the standard "tag triggered a workflow that failed" recovery dance — no new operator burden introduced by this ticket.

If `self-check` passes:
- `goreleaser` runs and produces release artifacts exactly as it does today. No behaviour change on the happy path.

## Concurrency model

N/A — GitHub Actions sequences `self-check` and `goreleaser` via `needs:`. The reusable-workflow invocation runs as a separate logical workflow conclusion that the caller waits on.

No `concurrency:` group is added in this ticket. Tag pushes are operator-initiated and typically serialized in practice; if a future incident shows concurrent tag pushes racing, that's a separate ticket.

## Error handling

Five concrete failure modes for the new wiring; each one has a defined behaviour:

1. **`self-check` job fails (regression detected)** — `goreleaser` is `skipped`, `notify-failure` posts Discord alert, overall run is `failure`. Tag remains; operator recovers per the dance above. **This is the desired contract.**
2. **`self-check` job times out (`timeout-minutes: 5` exhausted)** — same as #1. `needs:` treats timeout as a failure terminal state; goreleaser is skipped. `notify-failure` fires (the timeout path is one of the reasons the alert is structured as a job-level `if: failure()` rather than step-level — see #533's spec).
3. **`ANTHROPIC_API_KEY` secret is unset on the repo** — `self-check` job fails inside `./pyry agent-run --self-check` (no API key → claude can't reach the API). Same outcome as #1: goreleaser skipped, Discord alert fires. The failure is loud and self-explanatory in the job log.
4. **`DISCORD_WEBHOOK_URL` secret is unset on the repo** — `notify-failure` soft-fails per #533's contract (`::warning::` + `exit 0`). `goreleaser` is still skipped (because `self-check` failed) — the soft-fail in `notify-failure` does not somehow rescue `goreleaser`. This is the correct asymmetry: the alerting plumbing degrades gracefully; the publishing gate does not.
5. **Reusable-workflow invocation itself errors out (malformed `uses:`, missing file)** — the `self-check` caller job fails before reaching the called workflow; `goreleaser` is skipped via `needs:`. The fail-closed behaviour holds across "self-check ran and failed" and "self-check couldn't even start" alike. This is the desired property for a security gate.

No retry on the self-check side. The operator deletes the tag and retags after fixing the root cause; that's the manual retry surface.

## Testing strategy

No automated tests. This is workflow YAML; the test harness is the workflow itself.

Verification steps for the developer:

1. **Static lint.** Run `actionlint .github/workflows/release.yml .github/workflows/self-check-daily.yml` to catch YAML / expression-syntax errors and the common `workflow_call`-related pitfalls (unreferenced inputs, malformed `uses:`).
2. **Defensive Go check.** `go vet ./... && go build ./cmd/pyry` — no Go code touched, but a clean build run is the project's standing "before opening PR" baseline.
3. **End-to-end manual verification (AC4-mandated, must be in the PR body).** Push a throwaway `v0.0.0-rc.test` tag in a fork or a side branch of pyrycode/pyrycode and confirm:
   - On a known-failing self-check (e.g. set the throwaway branch HEAD to a commit known to fail, like `41cdc5f` from the May regression window), the `self-check` job goes red and `goreleaser` is `skipped`. The Actions tab shows zero release artifacts produced. No GitHub Release appears at the throwaway tag. No homebrew formula push fires.
   - On a known-passing self-check (e.g. set HEAD to current main with #526 fixed), the `self-check` job goes green and `goreleaser` proceeds normally. (In a fork or test branch where `HOMEBREW_TAP_TOKEN` is not set, the goreleaser job is expected to fail late at the homebrew-push step — that's fine; the ordering and "goreleaser ran" assertion is what's being verified, not full successful publish.)
   - Delete the throwaway tag after verification (`git push origin :refs/tags/v0.0.0-rc.test`).

Capture both observations in the PR body — what was tagged, what the conclusion was for each job, screenshots of the Actions UI if convenient.

## PR description requirements (acceptance criteria, not code)

Three items the developer must include in the PR body — these are AC, not optional:

1. **Manual verification report** (per AC4). Describe both the failing-self-check run and the passing-self-check run with run URLs (or Actions-tab references). State plainly that `goreleaser` was `skipped` on the failure path and `ran` on the success path. The throwaway tag must be deleted; mention this in the report.
2. **Operator note on tag-recovery.** Plain sentence: "If a release tag push hits a self-check failure, the tag remains in the repo. To retry: delete the tag (`git tag -d vX.Y.Z && git push origin :refs/tags/vX.Y.Z`), fix the regression on main, retag from the new main HEAD." So the operator isn't surprised the first time this fires.
3. **Behavioural symmetry note.** Plain sentence: "On a release-tag-triggered self-check failure, Discord receives the same `notify-failure` alert that daily-cron failures fire — the reusable-workflow design means the alert behaviour travels for free."

## Open questions

- **Should `concurrency:` be added to the reusable workflow to prevent overlapping invocations?** Two tag pushes within the ~3-5 minute self-check window would today queue two parallel runs. A `concurrency: { group: self-check, cancel-in-progress: false }` block on the called workflow would serialize them. Decision: defer. No operator pain observed; the cost is one extra YAML block hiding a potential surprise (cancelled cron run if a tag push arrives mid-cron). Revisit only if observed.
- **Should `permissions: contents: read` be added explicitly at the workflow level on self-check-daily.yml?** Defense-in-depth. Decision: defer. Default `GITHUB_TOKEN` permissions are already sufficient for the existing job bodies, and the reusable-workflow invocation does not implicitly grant the caller's `contents: write`. Adding it is harmless but outside this ticket's AC.
- **Should the `self-check` job's `name:` be set explicitly in release.yml to make the Actions UI clearer?** A bare `uses:` job typically displays the reusable workflow's own job names ("self-check / self-check", "self-check / notify-failure"). Acceptable as-is — the operator opens the run and sees the structure. Add `name: Per-agent self-check` if Dev wants the rolled-up label to read better; not gated.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. The only data crossing a trust boundary in this design is the `v*` tag itself — and the trust boundary (who is allowed to push tags) is enforced by GitHub repo permissions, not by this workflow. The workflow consumes `github.ref` only via implicit `actions/checkout` (which checks out the tagged commit); no untrusted input flows into the self-check body or the gate logic. The reusable-workflow `uses: ./.github/workflows/self-check-daily.yml` is resolved at the caller's SHA, so an attacker who could push a malicious tag could also rewrite the self-check body in the same push — but they would still be subject to the gate evaluating the body they pushed, which would either pass (no regression) or fail (gate fires). There is no "TOCTOU on the workflow body" attack because the called workflow is pinned to the same SHA as the caller.
- **[Tokens, secrets, credentials]** No findings. Three secrets cross workflow boundaries via `secrets: inherit`: `ANTHROPIC_API_KEY`, `DISCORD_WEBHOOK_URL`, and (in the goreleaser job, unchanged) `HOMEBREW_TAP_TOKEN` + `GITHUB_TOKEN`. `secrets: inherit` is the documented GitHub Actions mechanism; it does not expose secrets to logs and does not make them visible to PRs from forks (PRs do not trigger `v*` tag-push or `workflow_call` here). No new secret is introduced. No secret is logged or echoed by the new workflow body; the existing `notify-failure` job already POSTs the webhook URL via `curl` without echoing it (verified in #533's implementation).
- **[File operations]** N/A — no filesystem operations are introduced by this change. The self-check body's file operations are pre-existing and unchanged.
- **[Subprocess / external command execution]** No findings. No new `exec` shapes are introduced. The new YAML wires existing jobs; no new `run:` blocks with user-controlled input.
- **[Cryptographic primitives]** N/A — no cryptography in scope.
- **[Network & I/O]** No findings. New network calls: (a) `actions/checkout` and `actions/setup-go` over the GitHub Actions network surface (pre-existing pattern, identical to release.yml today and self-check-daily.yml today); (b) `./pyry agent-run --self-check` reaching `api.anthropic.com` (pre-existing pattern). No new ingress is added — the workflow is triggered by tag push (existing) and by reusable-workflow `uses:` from a same-repo workflow (no external ingress vector).
- **[Error messages, logs, telemetry]** No findings. No new log content beyond what `self-check` and `notify-failure` already emit. The `skipped` conclusion on `goreleaser` is the only new operator-visible signal; it leaks no internal state.
- **[Concurrency]** No findings. Sequencing is via `needs:`, which GitHub serializes. No shared state is mutated by the new wiring. The "two concurrent tag pushes" case is noted under Open questions but is not a security issue — both runs would independently gate via self-check.
- **[Threat model alignment]** The relevant threat from #375's contract is: "a regression silently disables the per-agent tool allowlist, and binaries shipping that regression land in users' hands via a release." The fix lands exactly at the release boundary: the gate fails closed. The complementary threat — "the gate itself is misconfigured and fails open" — is addressed by the design's fail-closed defaults: every failure mode enumerated above (self-check fails, times out, secret unset, reusable-workflow errors out) results in `goreleaser` being `skipped`. There is no design path where `goreleaser` runs without `self-check` having reported `success`. Confirmed by inspection of GitHub Actions `needs:` semantics: a needed job's conclusion must be `success` (not `failure`, not `skipped`, not `cancelled`) for the dependent job to run, unless an explicit `if: always()` / `if: failure()` override is set on the dependent. No such override is set on `goreleaser`.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-05-24
