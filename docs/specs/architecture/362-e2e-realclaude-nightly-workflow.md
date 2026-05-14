# Spec: e2e/realclaude nightly GitHub workflow (#362)

## Files to read first

- `.github/workflows/self-check-daily.yml` (entire file, 36 lines) — canonical template. Copy structure: leading comment block → `on:` (schedule + workflow_dispatch) → single job with `runs-on`, `timeout-minutes`, `actions/checkout@v6`, `actions/setup-go@v6` with `go-version: "1.26.x"`, npm install of `@anthropic-ai/claude-code`, then a final `run` step that exports `ANTHROPIC_API_KEY` from the secret. **Mirror the action versions and Go pin exactly** — the AC specifies parity, and divergence creates two CI footprints to keep in sync.
- `Makefile` — confirm `e2e-realclaude` target exists and what it runs (`go test -tags e2e_realclaude ./internal/e2e/realclaude/...`). The workflow invokes the make target, not `go test` directly, so future test additions in `internal/e2e/realclaude/` are picked up without touching CI.
- `internal/e2e/realclaude/` — current contents (smoke test scaffold from #361). Just confirm it exists; no code changes here.
- `docs/knowledge/codebase/336.md` (if present) — the self-check-daily ticket's lessons. Skim for any monitoring-contract gotchas that should propagate.

## Context

Real-claude e2e tests cost API credits and are intentionally tag-gated out of per-PR CI (#361). Without a scheduled run, an upstream `claude` binary regression would only surface at the next production dispatch. A nightly run provides 24h-bounded detection at low cost.

This is the second consumer of the badge-only monitoring contract established by `self-check-daily.yml` (#336). No new monitoring infrastructure — same contract, second workflow.

## Design

**One new file:** `.github/workflows/e2e-realclaude-nightly.yml`.

**Structure (mirrors `self-check-daily.yml`):**

1. **Leading comment block** documenting:
   - Purpose: nightly real-claude e2e suite catches upstream `claude` binary regressions within 24h.
   - Operator prerequisite: `ANTHROPIC_API_KEY` must be set as a repo secret.
   - Monitoring contract: failure surfaces via the workflow badge; no auto-issue, no webhook (same as `self-check-daily.yml`). If badge-only proves insufficient in practice, follow up in a separate ticket.
   - Cost note: every run consumes API credits; do not enable a more aggressive cadence without revisiting.

2. **`on:` triggers:**
   - `schedule: cron: "0 4 * * *"` — 04:00 UTC daily (per AC). Offset from `self-check-daily.yml`'s 06:13 UTC so the two nightly jobs don't contend for any shared rate-limit window.
   - `workflow_dispatch: {}` — manual trigger for validating the workflow itself.

3. **Single job `e2e-realclaude`:**
   - `runs-on: ubuntu-latest` (matches template).
   - `timeout-minutes: 15` — conservative cap. The #361 smoke test is fast (~seconds), but #363–#368 will add longer scenarios. Revisit when those land. Hard upper bound is the GitHub Actions 6h default; 15 minutes prevents a hung `claude` from burning that.
   - **No** `continue-on-error: true` — a red nightly is the entire signal.

4. **Job steps (in order):**
   - `actions/checkout@v6`
   - `actions/setup-go@v6` with `go-version: "1.26.x"` (exact parity with template)
   - `npm install -g @anthropic-ai/claude-code` (no name change; matches template)
   - Run `make e2e-realclaude` with `ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}` exported in the step's `env:` block.

**No build step.** Unlike `self-check-daily.yml` (which builds `pyry` then invokes the binary), this workflow runs Go tests directly via the make target — `go test` handles its own compilation. Don't add a redundant `go build`.

## Concurrency model

Not applicable — single sequential job, no parallel matrix.

## Error handling

- **Timeout:** `timeout-minutes: 15` is the only failure-mode guard beyond the test framework itself. A hung `claude` binary is the realistic failure mode this protects against; a hung Go test would also trip it.
- **Secret missing:** if `ANTHROPIC_API_KEY` is unset in the repo, the test that depends on it fails inside the suite — surfaces as a red badge with a clear test-output reason. No workflow-level pre-check needed; the comment block documents the prerequisite.
- **Test failure:** exits non-zero from `make e2e-realclaude` → workflow red → badge red. That IS the intended behavior.

## Testing strategy

- **Local smoke before commit:** `act -j e2e-realclaude` is not required (act has known limitations with secrets and `npm install -g`). The author should instead validate by `workflow_dispatch` after the PR merges to `main`, OR by pushing the workflow file to a personal branch and triggering it manually with the secret set on a fork.
- **Validation that `make test` is unaffected:** run `make test` locally before commit; expect zero `realclaude` tests to execute (the build tag exclusion from #361 is the enforcement). This is a one-line check; add it to the PR description as evidence.
- **Validation of the YAML syntax:** GitHub validates on push; a syntax error rejects the workflow with a clear message. No need for a separate linter step in CI.

## Open questions

None. The pattern is fully prescribed by `self-check-daily.yml` and the AC.

## Files modified

- **New:** `.github/workflows/e2e-realclaude-nightly.yml` (~35–45 lines including the comment block)
- **Modified:** none
