# Buildable hosted-app scaffold (#3140)

## Files read

- `internal/apps/manifest.go` → `ValidateManifest`, `validText`, `validRelease`, `fixedManifest`: exact v1 fields, UTF-8 byte bounds and identity/version constraints.
- `docs/knowledge/features/apps-package.md` § Validation and stored identity, Testing: validation proves no readiness; uppercase UUID tests need hexadecimal letters.
- `docs/specs/architecture/3120-hosted-app-contract.md` § Manifest and validation, Runtime, roots and service conventions, Electron and Android isolation: fixed layout, runtime range, external assets and CSP.
- `docs/hosted-apps.md` § Runtime and layout: generated source and compiled release entry paths.
- `docs/knowledge/decisions/043-hosted-app-runtime-and-isolation.md`: trusted Node services and private/public root separation.
- `docs/knowledge/features/development-verification.md` § Verify inherited premises, Prove that tests distinguish the change: test boundaries independently and preserve execution evidence.
- `CODING-STYLE.md`: errors, focused tests and contract comments.
- esbuild Getting Started, React `createRoot`, npm package metadata documentation: portable bundling, React entry and runtime enforcement.

## Context

Publishing tools and orchestrator apps need a maintained, independently buildable source scaffold. #3141 supplies SQL service behavior and #3142 supplies the preference UI. #3125 owns registration, identities, daemon-selected destinations and publication; #3126 owns channel tools. No new decision record is needed beyond ADR 043.

Sizing: one deliverable, three acceptance criteria, approximately 650 written lines including lockfile, focused tests and this plan; zero exported types/interfaces, zero migrated consumers and at most nine distinct rejection categories. The refiner's 550-line forecast is plausible but allows less integration proof. Recounted against this plan: below all five limits. No fetched feature branch overlaps `templates/hosted-app/` or this plan.

## Design

- `templates/hosted-app/scaffold.mjs` is a dependency-free standalone Node command: `node templates/hosted-app/scaffold.mjs --destination PATH --title TEXT --server-id UUID --app-id UUID [--release-version VERSION]`. It accepts caller identities only; default release is `1.0.0`. The destination's parent must already exist.
- `scaffold` validates inputs against the Go manifest constraints before writes. Exclusive nonrecursive directory creation refuses every existing destination, including dangling symlinks. It materializes only an explicit list of maintained source/config/test files, creates `sql/migrations`, and writes the fixed manifest with `JSON.stringify`. A failed write leaves an incomplete destination for inspection; retries never overwrite it.
- `source/` holds pinned React/React DOM, TypeScript, their type definitions and esbuild-wasm, plus a consistent npm lockfile. The WASM bundler avoids platform-specific binaries/dependency matrices; it is a build dependency only. No runtime TS loader or dev server.
- `src/web/main.tsx` renders a minimal `App` with the title from a JSON data file. No caller input is interpolated into JavaScript, HTML or CSS. `App` renders React text children; CSS and the module script are external root-relative assets.
- Separate frontend and service tsconfigs check both trees; the service config emits ESM to `dist/service`. `scripts/build.mjs` checks the actual runtime before invoking tools, cleans only generated `dist`, emits frontend assets from explicit entry points, writes static HTML, and compiles the service. SQL remains at release-relative `sql/migrations`; no SQL behavior ships here.
- `engines`, npm `devEngines` and `.npmrc` reject a mismatched actual Node executable before installation. `runtime.mjs` checks the actual process version before typecheck/test/build and from compiled service/migration entries. The supported range is exactly `>=24.15.0 <25.0.0`; no runtime selection or fallback.
- Unfinished `main.ts` and `migrate.ts` exit nonzero with an explicit unfinished-service diagnostic. They bind no port, write no database and claim no readiness.

## Concurrency model

Scaffold filesystem work is synchronous. Build children use fixed executable/argv without caller data and are awaited with bounded timeouts. Tests isolate destinations in temporary directories and await child completion. No daemon workers, servers or background processes.

## State transitions and identity reuse

| Event | Focused test |
|---|---|
| Scaffold twice at the same destination, including a symlink | `existing destinations are untouched` checks byte/target preservation. |
| Concurrent scaffold calls for the same destination | `concurrent destination creation has one owner` checks one success and one refusal. |
| Same identities used in distinct caller destinations | `fresh generated app passes the toolchain` and default/custom release cases; generation never registers or changes identity. |
| Repeated build with stale/private files | `fresh generated app passes the toolchain` verifies cleanup and identical output inventories. |

These are Node filesystem/build tests; no Go lifecycle state changes, so Go race coverage remains the existing manifest package suite.

## Error handling

Invalid arguments/manifest fields fail before creating a destination with field-specific diagnostics. Existing or unavailable destinations fail without replacement. Filesystem/build errors return nonzero; partial generation cannot be mistaken for success. Runtime mismatch identifies the required range and actual version. Service/migration placeholders always fail explicitly.

## Testing strategy

Write Node focused tests first and observe failure before implementation. Cover all manifest bounds (including UTF-8, controls, Go whitespace rules, UUID variants and version overflow), default/custom release, existing file/directory/live/dangling symlink preservation, concurrent creation and hostile title JSON/React rendering. A fresh temporary generated app runs `npm ci`, `npm run typecheck`, `npm test`, `npm run build`; inject frontend/service type errors to prove both checks, inspect public output/private sentinels, execute compiled placeholders, repeat the build and verify runtime mismatch using an explicitly selected unsupported executable. Generated `npm test` stays deterministic and offline. Validate a generated manifest against the existing Go validator using a scratch test overlay. Run `go test -race ./internal/apps/...`, `go vet ./...`, `go build -o /tmp/builder-3140/pyry ./cmd/pyry` and `git diff --check`. The verifier owns the full-module gate.

## Open questions

None. SQL schema/readiness, preferences, registration/publication and viewer/browser conformance are explicitly owned by later tickets.

## Documentation handoff

Pending for the documentation stage: add `Template and saved preferences` to `docs/hosted-apps.md`, describing the committed scaffold entry, caller inputs, generated layout and `npm ci`, `npm run typecheck`, `npm test`, `npm run build`. State that this slice supplies buildable source, while SQL service and preference UI follow in #3141/#3142. Scaffold generation alone does not register, publish or return a ready app link. Link the later #3125/#3126 publishing/channel workflow.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings: `scaffold` validates caller fields once; JSON serialization and React text children preserve hostile titles as data.
- [Tokens/secrets] No credentials minted, read or logged. Build uses explicit web entry points and no environment injection; private sentinel tests verify no source-root copying.
- [File operations] No findings: exclusive `mkdir` avoids an existence-check race, uses mode 0700 and refuses symlinks. Fixed copy paths exclude installed modules/build output. Parent directories and maintained template files are trusted local inputs; hostile concurrent parent replacement is outside this local trusted-code scaffold's contract.
- [Subprocesses] No findings: tool/test subprocesses receive fixed argv, never caller shell text, and have deadlines. No process exists after completion.
- [Cryptography] No new cryptography or authentication; caller UUIDs are identifiers, not credentials.
- [Network/I/O] No server or socket boundary; install fetches integrity-locked npm dependencies. Service HTTP/SQL behavior is OUT OF SCOPE, owned by #3141.
- [Errors/logs] Local diagnostics identify fields and runtime versions, never echo title content or environment values. No telemetry.
- [Concurrency] No findings: exclusive destination creation gives one owner; interrupted generation remains visibly incomplete and a retry refuses overwrite.
- [Threat model] Native hostile-content isolation is OUT OF SCOPE, owned by desktop #1927 and mobile #2063. This slice supplies external CSP-compatible assets, not browser isolation evidence or publication readiness.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-11

## Revisions

- 2026-10-11: Pin TypeScript 6.0.3, the portable JavaScript compiler, rather than the newly native TypeScript 7 compiler: the native platform packages grew the lockfile from the forecast to 486 lines. The final lockfile is 125 lines, and the complete change remains below 800 written lines. TypeScript 6 requires an explicit CSS module declaration, supplied in the frontend source. No caller/build contract changes.
