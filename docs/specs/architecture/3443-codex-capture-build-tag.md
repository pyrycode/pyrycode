# #3443 — Opt-in build tag for live Codex tests

## Files read

- `internal/codexsup/capture_test.go` → `TestCaptureLive`, `captureTurn`, `writeCapture`, `TestCaptureFixturesScrubbed`, `TestCaptureApprovalPredicates`: separate the live entry point from offline fixture readers and tested helpers.
- `cmd/pyry/codex_approval_test.go` → `TestCodexApprovalLive`, `waitTurnEnd`, `TestCodexDecision`: retain the offline approval suite and shared helpers.
- `docs/knowledge/features/codexsup-package.md` § “The capture's live gaps, and what they mean for a recapture” and “Testing”: env-only guards cannot guarantee ordinary gates avoid live Codex.
- `docs/knowledge/features/codexsup-package-production-wiring.md` → Codex approvals: the live test exercises the existing runner and registry wiring.
- `docs/knowledge/features/development-verification.md` § “Captures and live evidence” and “Test execution and artifact survival”: compile tagged tests without live credentials and distinguish a skipped live run from proof.
- `CODING-STYLE.md` → testing and contract comments; `Makefile` → `check`: verifier owns the full-module gate.

## Change

Move only `TestCaptureLive` to `internal/codexsup/capture_live_test.go` and `TestCodexApprovalLive` to `cmd/pyry/codex_approval_live_test.go`, each guarded by `//go:build codex_capture`. Preserve both test bodies and their existing environment guards. Keep all offline tests, fixture types, constants and helpers in their untagged files; remove only imports made unused by the moves. Update the live comments to state the tag requirement, prerequisites and allowance cost, with `go test -tags codex_capture -run TestCaptureLive ./internal/codexsup/` in the capture comment. No production, concurrency, lifecycle or identity behavior changes.

Sizing: one deliverable, about 400 total added/deleted lines including this plan, zero new exported types/interfaces, zero consumer updates, two acceptance criteria, zero new reject branches. This remains within every builder limit. No overlapping remote feature branch touches the four planned test paths after fetching origin.

## Testing strategy

Before the move, list the tests to demonstrate both live entry points are included by default. Afterward, compare default and `codex_capture` test listings and Go-selected test files: the live tests must appear only with the tag, while the complete offline listing remains unchanged. Run the tagged live tests with both environment variables empty to verify their guards skip without spending allowance. Run race tests for both touched packages with `PYRY_CODEX_CAPTURE_BIN=/nonexistent/codex` and `PYRY_CODEX_CAPTURE_HOME=/nonexistent/codex-home`, tagged vet for both packages, module vet and the CLI build. From the clean implementation commit, record that `git status --porcelain -- internal/codexsup/testdata/ cmd/pyry/` is empty. The verifier must run `PYRY_CODEX_CAPTURE_BIN=/nonexistent/codex PYRY_CODEX_CAPTURE_HOME=/nonexistent/codex-home make check` from that clean commit and record the gate result and empty status; builder instructions prohibit running the full-module gate here.

## Documentation handoff

- Pending for the documentation stage: in `docs/knowledge/features/codexsup-package.md`, under “The capture's live gaps, and what they mean for a recapture” and “Testing”, replace env-only safety claims with the `codex_capture` tag requirement and `go test -tags codex_capture -run TestCaptureLive ./internal/codexsup/`; retain environment prerequisites and the Codex allowance warning.

## Revisions

- 2026-10-15 — Verifier findings 1 and 2: default staticcheck reported eight U1000 errors because the live entry points were the helper graph's only callers. Move `captureFrame`, `captureScenario`, `captureTurn`, `drain`, `accountKeyPrefix`, `writeCapture`, `scrub`, the live-only `captureEffort` constant, and `waitTurnEnd` into the existing `codex_capture` files. Keep the offline tests and all helpers they use (`captureModel`, `isExactCommand`, `allUnder`, `captureDir`, `emailPattern`, `machineKeys`, and the approval harness) untagged; default compilation confirms the replay tests still need `captureModel`. Test bodies and helper behavior remain unchanged; correct `captureTurn`'s stale handshake comment to describe its actual approval policy. This supersedes the original instruction to leave every helper untagged.
- Validation: reproduce all eight default staticcheck failures before editing; require default and tagged staticcheck to pass after the move, preserve all offline test names, check tagged env-guard skips, and rerun the scoped race tests, tagged/module vet and CLI build. The verifier still owns the full poisoned-environment `make check` from the clean pushed commit and its empty fixture/CLI status check.
- Re-sizing: one deliverable, under 800 total added/deleted lines including the original moves and this revision, zero new exports or consumer updates, two acceptance criteria and zero new reject branches. No overlapping remote feature branch touches the four test paths after fetching origin.
