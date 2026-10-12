# Responsive saved-preference example and generated-app proof

## Files read
- `templates/hosted-app/scaffold.mjs` → `scaffold`: explicit generated file inventory and synthetic identity inputs.
- `templates/hosted-app/source/src/web/App.tsx` → `App`, `main.tsx`: literal title rendering and external stylesheet entry.
- `templates/hosted-app/source/scripts/build.mjs` → production bundle: external root-relative assets, compiled service ESM.
- `templates/hosted-app/source/test/app.test.mjs` → literal-title assertion: direct App invocation must remain supported.
- `templates/hosted-app/source/src/service/{environment,database,main,health}.ts` → environment, preferences, service routes: complete startup contract, 204 commit acknowledgment, SQL defaults.
- `templates/hosted-app/source/test/service.test.mjs`: deterministic temporary-SQL and restart/replacement proof to preserve.
- `docs/hosted-apps.md` § Template and saved preferences / Startup and health: mandatory production environment and external durable data.
- `docs/specs/architecture/3120-hosted-app-contract.md` § Runtime, roots and service conventions / Electron and Android resource/Fetch bridge / Responsive shell and orchestrator walkthrough; ADR 043: exact CSP and native-independent scope.
- Approved #3120 dashboard reference: local colors, wrapping navigation, card breakpoint, table scrolling and touch sizing.
- `docs/knowledge/features/development-verification.md` § Test execution and artifact survival: counts and committed evidence, not exit code alone.

## Context
Complete the merged #3140/#3141 example with explicit durable preference saving and proof against generated production assets. Publication/native integration remains later work. No new decision record is needed. Refreshed remote feature branches have no template overlap.
Sizing: one deliverable, four criteria, approximately 650–750 written lines including plan, tests, lockfile and evidence metadata; no new exported Go APIs, no consumer migrations, fewer than ten UI/fixture reject branches.

## Design
Keep `App({title})` as a hook-free wrapper rendering its literal heading and a stateful preference component. Load a validated complete record from `/api/preferences` with credentials omitted. Loading/saving disable editing; explicit Save captures the entire record, performs one POST, and reports success only on 204. Changes clear old success. Failures show static polite feedback and allow only a new explicit save; no browser storage.
Use local light theme variables, safe-area padding, wrapping navigation, labeled period select, three illustrative cards and a separately scrolling table. Above 760px cards have three columns; below they have one. External CSS provides contrast, focus and coarse-pointer minimum height.
Add pinned Playwright Core as a test-only browser driver. A repository smoke entry invokes scaffold for fresh releases 1.0.0 and 1.0.1, running ci/typecheck/test/build in order for each. A generated focused browser script runs compiled migrations/service/health with a complete synthetic environment and external data directory. Its loopback fixture serves only a fixed built-asset allowlist and proxies API requests with exact CSP/nosniff. It gates delayed acknowledgment and injects failed/uncertain responses without changing the production service.
The browser script records commands, versions, assertion counts, layout/contrast/touch measurements and screenshots outside its temporary fixture. This run commits synthetic review artifacts under `templates/hosted-app/testdata/browser-3142/` and links them from the PR.

## Concurrency model
React effect owns one cancellable initial read; cleanup aborts it. One explicit write can be pending at a time, bounded by AbortSignal timeout, never replayed. Node child processes have bounded command/start/exit waits; final cleanup closes browser contexts/browser, destroys fixture connections and terminates service with bounded SIGTERM/SIGKILL fallback. Fixture requests are bounded and failures become diagnostics.

## State transitions and identity reuse
No Go lifecycle state; these transitions are covered by the built-browser smoke rather than go test -race.
| Event | Proof |
| --- | --- |
| Initial/fresh client read | Defaults plus fresh-context persisted settings |
| Repeated Save / local edit | Complete POST payload, delayed 204, disabled pending controls and cleared stale success |
| Rejected/uncertain write | Visible polite feedback; stable POST count during bounded observation |
| Initial read failure | Polite feedback, no false success |
| Service restart, same app identity/data | Matching health and fresh context recovers nondefault settings |
| Release 1.0.1 replacement, same host/app/data | Separate generated build, matching health and fresh context recovers settings |

## Error handling
Missing/unsupported Node, npm, browser executable or driver fails nonzero with prerequisite guidance. Invalid read records, non-200 reads, non-204 writes and transport timeouts show static error feedback. Smoke command/build/browser failures retain evidence and fail nonzero, never skip.

## Testing strategy
First assert required shell and API request behavior before implementing. Preserve hostile literal-title testing with external React imports so hooks share the renderer. Run existing deterministic template tests; run the generated-app browser command with Chromium at 320×800 and 1280×800, checking exact response headers, no CSP violations, page overflow, card layout, contrast, keyboard focus and coarse-pointer sizes. Save/error/delay/restart/replacement scenarios use production builds and file-backed SQLite. Run go vet ./... and go build ./cmd/pyry; no Go package is changed.

## Open questions
Resolved: Chromium executable is supplied explicitly via `HOSTED_APP_BROWSER`; artifacts use an explicit persistent output directory. Node 24.15+ is a mandatory prerequisite, not a runtime fallback.

## Documentation handoff
Satisfied in [Template and saved preferences](../../hosted-apps.md#template-and-saved-preferences): committed smoke command, Node/npm/Chromium installation prerequisites, built-UI workflow, generated browser script/pinned driver, retained evidence and approved responsive reference. Earlier scaffold commands/layout and service API/defaults/limits/migration/startup documentation are preserved. The guide explains data survival across replacement builds, generation versus registration/publication/readiness (#3125/#3126), and fixture proof versus pending native/live-channel acceptance (#3133 and client tickets).

The React renderer-sharing and release-mismatch fixture lessons are recorded in
[apps package testing](../../knowledge/features/apps-package-testing.md#testing).

## Security review
**Verdict:** PASS
**Findings:**
- [Trust boundaries] Validate GET record version/enums before installing UI state; React text renders title literally. Fixture paths use a fixed built-asset allowlist.
- [Tokens] Synthetic identities only; browser requests omit credentials; no secrets or browser durable storage.
- [File operations] Temporary private fixture/data directories; artifacts are explicit output, not network-selected paths. Existing service owns SQLite permissions and migration safety.
- [Subprocesses] Fixed argv arrays, no shell interpolation; production service children receive minimal mandatory environment and bounded shutdown.
- [Cryptography] None added; fixture identity is synthetic, never an authorization claim.
- [Network and I/O] Loopback-only fixture/service, request/fetch deadlines and body caps, exact CSP/nosniff on responses.
- [Errors/logs] Static UI errors; evidence includes synthetic commands/results only, never inherited environment or credentials.
- [Concurrency] Abort stale reads, disable pending writes, explicitly release fixture gates in cleanup and await child exit.
- [Threat model] Native isolation/publication/live-channel conformance remains OUT OF SCOPE under #3125/#3126/#3133 and client tickets; this proof does not claim it.
**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-11

## Revisions
- 2026-10-11: The replacement release's deterministic suite exposed a fixed `1.0.1` mismatch sentinel in `startup and readiness failures`. Derive a release different from the generated manifest so the existing reject assertion applies to both generated releases; no service contract changes.
