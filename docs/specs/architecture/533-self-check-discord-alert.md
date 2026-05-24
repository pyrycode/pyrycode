# Spec: self-check-daily — Discord alert on workflow failure

**Ticket:** #533
**Size:** XS (1 file modified, ~30-50 added lines, no Go code, no tests)

## Files to read first

- `.github/workflows/self-check-daily.yml` — entire file (~40 lines). The only file you'll modify. Internalize the existing header comment block before touching it; you'll extend it, not replace it.
- `.github/workflows/ci.yml` — skim for any pre-existing pattern for failure notifications. (Currently there is none — confirm and proceed.)
- `.github/workflows/release.yml` — same skim, same purpose. Confirm no existing webhook-post pattern is established elsewhere in this repo that you should mimic.

No Go source is in scope.

## Context

The `agent-run self-check (daily)` workflow is the deterministic safety net for the per-agent tool-allowlist contract (#375). Between 2026-05-19 and 2026-05-24 it correctly went red while a regression (`selfcheck.go` not passing `AllowedTools` to `ptyrunner.Config`, fixed in #526) shipped to main. The signal worked; the alerting surface (red README badge + Actions tab entry) did not reach the operator. This ticket converts the passive badge into an active Discord push so a future regression of the same shape gets investigated within a day, not five.

Out of scope (sibling tickets): the release-tag gate (the Technical Notes call this out explicitly), any change to the self-check body itself, any change to other workflows.

## Design

Add a second job to `self-check-daily.yml`, named `notify-failure`, that runs only when the `self-check` job fails. Trigger the alert via a job-level `if: failure()` plus `needs: self-check` so cancellations and timeouts are also covered (a step-level `if: failure()` inside the same job wouldn't fire on `timeout-minutes: 5` exhaustion, which is one of the failure modes we want to alert on).

The job has a single step: a `curl` POST to Discord with a JSON body containing one `content` field formatted as a multi-line markdown string. No external action dependency — matches "stdlib over dependencies" from CLAUDE.md.

### Job sketch (contract, not the final YAML)

```yaml
notify-failure:
  needs: self-check
  if: failure()
  runs-on: ubuntu-latest
  steps:
    - name: Post Discord alert
      env:
        DISCORD_WEBHOOK_URL: ${{ secrets.DISCORD_WEBHOOK_URL }}
        RUN_URL: ${{ github.server_url }}/${{ github.repository }}/actions/runs/${{ github.run_id }}
        WORKFLOW_NAME: ${{ github.workflow }}
        SHA: ${{ github.sha }}
        RUN_DATE: ${{ github.event.repository.updated_at }}   # placeholder; see "Date semantics" below
      run: |
        # POST a JSON {content: "..."} payload to $DISCORD_WEBHOOK_URL using curl --fail-with-body
        # Use jq to build the JSON to avoid quoting hazards in the SHA/title.
        # If DISCORD_WEBHOOK_URL is unset, log a clear warning and exit 0 (do NOT fail the
        # workflow — a missing secret should not turn the notify-failure job into its own
        # alarm. The PR description tells the operator to set the secret; if they forget,
        # the next failure logs a visible "secret unset" line in the Actions tab.)
```

### Content field format

A single multi-line markdown string with these required fields (per AC):

- Workflow name (`${{ github.workflow }}`)
- Failing run URL (`${{ github.server_url }}/${{ github.repository }}/actions/runs/${{ github.run_id }}`)
- Failing commit SHA (`${{ github.sha }}` — first 12 chars for readability is fine)
- Date of the run (see semantics below)

Suggested shape (developer can iterate; the AC names fields, not formatting):

```
🚨 **agent-run self-check (daily)** failed
Run: <https://github.com/pyrycode/pyrycode/actions/runs/12345>
Commit: `abc123def456`
Date: 2026-05-25 06:13 UTC
Investigate before trusting any new dispatcher activity.
```

The wrapping `<...>` around the URL suppresses Discord's link preview embed — keeps the alert compact in a busy channel.

### Date semantics

`${{ github.event.repository.updated_at }}` is the wrong field on a `schedule` trigger. Use a shell-generated UTC timestamp inside the `run:` block instead — `date -u +'%Y-%m-%d %H:%M UTC'`. This avoids GitHub-context surprises across `schedule` vs `workflow_dispatch` triggers and matches what an operator would write.

### Secret-missing behaviour

Treat an unset `DISCORD_WEBHOOK_URL` as a soft failure: log a single clear line (`::warning::DISCORD_WEBHOOK_URL secret not set — alert suppressed`) and exit 0. Reasoning: until the operator wires the secret, every workflow failure would otherwise produce *two* failure signals (the real one plus the notify-failure job going red), which doubles the noise the ticket is trying to reduce. The PR description (AC) explicitly tells the operator to set the secret before merge, so the soft-fail window is intended to be ~zero in practice.

### Header comment update

Extend the existing header comment block to document the alerting contract in plain English. Append (do not replace) the existing wording. Required content per the AC:

> On failure, this workflow posts to the Discord channel via the `DISCORD_WEBHOOK_URL` secret. Operator action: investigate the linked run before trusting any new dispatcher activity.

Keep the existing "belt-and-suspenders safety net" paragraph intact. The line "A stronger pager hookup is a follow-up if false-positive rate is acceptable" in the existing comment is now satisfied by this ticket — remove that sentence to keep the comment truthful, and replace with the new alerting-contract paragraph.

## Concurrency model

N/A — GitHub Actions sequences the two jobs via `needs:`. The `notify-failure` job only schedules after `self-check` reports a terminal state.

## Error handling

Two failure modes for the new job:

1. **`DISCORD_WEBHOOK_URL` unset** — soft-fail, log a warning, exit 0 (see above).
2. **Discord API rejects the POST** (rate-limited, malformed payload, webhook revoked) — use `curl --fail-with-body` so the job exits non-zero. A red `notify-failure` job alongside a red `self-check` job is acceptable: the operator already sees the original red badge; the second red badge surfaces "the alert plumbing itself is broken" which is information they want.

No retry. The next day's run alerts again if the failure persists.

## Testing strategy

No automated tests. This is workflow YAML; the test harness is the workflow itself.

Verification steps for the developer to perform before opening the PR:

1. Run `actionlint` on the modified file (or `gh workflow view` after pushing) to catch YAML / expression-syntax errors. The file is small; visual review is also acceptable.
2. Manually trigger the workflow via `gh workflow run self-check-daily.yml` after pushing the branch — confirm the `self-check` job still passes (your changes must not regress the existing job).
3. (Optional, not required by the AC) Force a synthetic failure to verify the alert fires: temporarily edit a copy of the workflow in a throwaway branch to `exit 1` in the self-check step, push, observe the Discord alert. Do NOT include this experimentation in the PR — it's developer-side verification only.

The "the alert actually fires" verification is left to the operator after merge, since `DISCORD_WEBHOOK_URL` must be set in repo secrets before the path is exercised end-to-end. The PR description (AC) makes this explicit.

## PR description requirements (acceptance criteria, not code)

Two items the developer must include in the PR body — these are AC, not optional:

1. **Audit paragraph.** The ticket asks to "confirm the 2026-05-19 failure-window start matches commit `058c569` merge timestamp." The actual commit timestamp is `2026-05-21 16:47:22 +0300` (verify with `git log -1 --format='%H %ai %s' 058c569`). The developer should report what they find — if the timestamps don't align as the ticket asserts, say so plainly in the audit paragraph rather than fabricate a match. Possible explanations to investigate before writing the paragraph: (a) the 2026-05-19 figure in the ticket may be from a different workflow run or an earlier related failure, (b) the regression-introducing commit may actually be a different SHA. Use `gh run list --workflow=self-check-daily.yml --limit 20` to enumerate actual failure dates and cross-reference against the commit history. Honest audit > fictional alignment.

2. **Operator action callout.** Plain sentence in the PR description: "Before merging this PR, add `DISCORD_WEBHOOK_URL` to repo secrets (Settings → Secrets and variables → Actions). Without the secret, the new step logs a warning and no-ops." Place this near the top of the PR body where the reviewer will see it.

## Open questions

- **Embed vs plain content.** This spec specifies plain `content`. Discord embeds (richer formatting, color stripe) are an alternative. The AC names required fields, not formatting; either satisfies. Plain content is simpler and matches "stdlib over dependencies" — recommend keeping it.
- **Alert deduplication across consecutive failures.** Currently the workflow runs daily; a persistent failure produces a daily alert. Acceptable — this is the "actionable signal" the ticket is asking for. If alert fatigue emerges, file a follow-up to gate on "first failure after a run of green," but do not preempt that here.
