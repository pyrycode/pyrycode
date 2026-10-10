# #3117 — Local memory configuration and effective roots

## Files read

- `cmd/pyry/memory_credential.go` → `runMemory`, `resolveMemoryCredential`: preserve credential dispatch and use lifecycle resolution without exposing its result.
- `cmd/pyry/memory_credential_file.go` → `selected`, `resolve`, `directory`: fresh resolution, stable references and protected no-follow storage.
- `cmd/pyry/memory_credential_test.go` → lifecycle, refusal and failed-save tests: reuse hermetic credential helpers.
- `cmd/pyry/main.go` → `runArgs`, `helpText`: existing offline dispatch and command advertising.
- `cmd/pyry/pair.go` → `resolveConfigPath`: its cwd fallback must not be used by memory operations.
- `cmd/pyry/workspace_base.go` → `resolveStartupWorkspaceBase`: explicit service startup context, independent of channel/session cwd.
- `internal/config/config.go`, `internal/config/memory.go` → `Load`, memory schema, `UpdateMemory`: parse-only data and atomic replacement preserving unknown JSON.
- `docs/knowledge/features/config-package.md` → Surface and Load semantics: caller owns semantic validation.
- `docs/knowledge/features/cli-verb-dispatch.md` → Memory credential persistence and option parsing: validate flags without echoing inputs; no credential caches.
- `docs/knowledge/features/memorysearch-package.md` → Effective evidence: saved configuration must not claim search readiness.
- `docs/knowledge/features/development-verification.md` → inherited premises and behavioral proofs; `CODING-STYLE.md` → file size, tests and persistence.
- QMD search for memory/credential/configuration → #3116 plan: semantic trust belongs to this boundary.

## Context

Expose one managed-settings boundary for offline CLI/wizard use and later daemon consumers. Saving settings enables no runtime. No decision record needed. #3089 overlaps `main.go` in supervisor wiring; our help-only edit is additive and independent.
Sizing after sketch and finished plan: about 770 written lines (340 production, 350 tests, 80 plan), zero new exported types, one existing dispatch caller, five acceptance criteria, no state machine. The credential analogue already supplies secret handling and atomic updates; those are reused rather than rebuilt.

## Design

- Add `memory_config.go` for parsing, callable `configureMemory(ctx, settings) error`, `memoryStatus(ctx)` and sanitized JSON projection; `memory_roots.go` for shared validation and callable `resolveEffectiveMemory(ctx, settings, startupWorkspaceBase)`.
- Strictly require all five main configure flags; permit repeated knowledge folders and one optional reference; reject other/duplicate main flags, missing values and positional arguments. Status accepts none. Existing credential verbs keep their contract.
- Resolve an existing absolute user home with no cwd fallback. Configure validates a copy, then calls `config.UpdateMemory` on that home's `.pyry/config.json`. Successful command output is exactly one configured JSON line.
- Validate enums, nonblank models and local/reference incompatibility. OpenAI always freshly calls `resolveMemoryCredential`; discard token and sanitize failures. Status projects only known saved fields, replacing reference with boolean credential availability; missing/null memory yields only configured false.
- Existing vault/root paths are absolute, cleaned, symlink-resolved directories. Check POSIX access for invoking effective user: vault write/traverse; roots read/traverse. Reserved transcript/credential paths resolve through their existing directory ancestors, rejecting broken links and file ancestors without creation.
- Separate vault cannot overlap transcripts or credentials in either ancestry direction; roots cannot overlap credentials. Default saves no path and defers vault checks. Normalize roots by component ancestry, retaining parents and siblings.
- Effective resolution accepts the daemon's explicitly resolved startup base, validates the effective vault, and returns vault write destination, read-only roots, transcript ownership path and normalized search union as distinct fields. A failure returns a zero result and changes nothing. No startup wiring in this ticket.
- Status validates saved semantics/paths but leaves default vault unresolved. No vault instructions/files, transcript directories, manual memsearch or search-availability contract are modified.

## Concurrency model

Synchronous operations; no new goroutines or mutable globals. CLI process handles cancellation through existing signal context; credential waits retain lifecycle deadlines. Wizard callers serialize config updates as required by `UpdateMemory`; cross-process config writer serialization remains its existing caller contract.

## State transitions and identity reuse

| Event | Race-enabled proof |
| --- | --- |
| Configure then status in fresh processes; repeat replaces roots/vault/provider | `TestMemoryConfigurationProcess`, `TestMemoryConfigurationReplacement` |
| Default resolves under distinct service startup bases | `TestEffectiveMemoryRoots` |
| Failed validation/save followed by retry preserves old config | `TestMemoryConfigurationReplacement`, `TestMemoryConfigurationFailures` |
| Saved OpenAI reference resolves afresh after token loss/unsafe storage | `TestMemoryConfigurationCredentials` |

## Error handling

Static operation/validation errors never include user values, JSON parser details, references, tokens or backend metadata. Emit no success JSON until validation/save/read completes. Atomic save failures preserve old bytes. Missing config means unconfigured; malformed/non-object config and invalid saved choices fail closed.

## Testing strategy

Tests first: callable settings/status, independent provider/capture choices, full replacement and unknown preservation, exact CLI/help output and fresh-process persistence; tables cover syntax, access, symlinks, absent reserved ancestors, component overlaps, invalid saved choices, credential rejection/redaction and failed saves. Check unchanged instruction bytes and absent transcript/vault creation. Run `go test -race ./cmd/pyry/...`, `go vet ./...`, and `go build -o /tmp/builder-3117/pyry ./cmd/pyry`; verifier owns the full-module gate.

## Open questions

None. Directory validation is a snapshot; later runtime operations must revalidate before use.

## Documentation handoff

Pending for documentation stage:
- `docs/guide.md`, new “Memory configuration”: exact flags, full-replacement semantics and JSON status shape; two vault modes, deferred default resolution, optional read-only roots, automatic transcript path, independent embedding/capture choices, credential references/redaction and rejection behavior. Saving alone does not install/start memory; future daemon application occurs after restart.
- `docs/knowledge/features/config-package.md`, “Surface” and “Load semantics”: link local semantic-validation operations and describe default-vault resolution with explicit daemon startup context.

## Security review

**Verdict:** PASS

**Findings:**
- [Trust boundaries] `validateMemorySettings` checks CLI and persisted settings before save/status/effective use; resolution returns no usable result on failure.
- [Tokens] SHOULD FIX: parser and load errors can echo reference-like input. Suppress parser output and replace errors with static diagnostics; never serialize the stored embedding struct into status. Lifecycle resolution owns protection and freshness.
- [File operations] Canonical directory/access checks precede component ancestry exclusions; reserved-path peeling must use lstat so a broken symlink cannot look absent. `UpdateMemory` owns private atomic writes. Validation creates nothing; runtime check/use protection belongs to #3098/#3099/#3100/#3101.
- [Subprocesses] No production subprocess or shell; test helper re-execs only this test binary with explicit arguments.
- [Cryptography] No new crypto or secret comparisons; reuse credential lifecycle.
- [Network and I/O] Local metadata only, no network listeners or manual memsearch operations.
- [Errors/logs/telemetry] Static errors and explicit status projection exclude references, tokens and backend/source metadata; no new logs or telemetry.
- [Concurrency] No new workers/locks; atomic config publication and existing credential directory locks retain their contracts.
- [Threat model] Service-user local metadata boundary only. Runtime installation/indexing/capture readiness and adversarial mutation after resolution belong to downstream runtime tickets named above; no relay/mobile changes.

**Reviewer:** builder (self-review)
**Date:** 2026-10-10

## Revisions

- 2026-10-10: Final implementation and hermetic coverage exceed the initial 770-line sketch (approximately 880 lines); canonical reserved-path and POSIX access cases account for the overage. Actual GitHub lineage is #3097 → #3108 → #3117, so the grandchild sizing rule requires continued building with `needs-human:sizing`. Candidate standalone seams would have been offline configure/status and effective daemon resolution; no scope is added. Root ordering is stable in input order, with the effective vault first unless a containing read-only root subsumes it; ownership/destination fields remain independent.
