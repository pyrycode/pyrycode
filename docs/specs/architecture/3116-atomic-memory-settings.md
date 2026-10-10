# #3116 — Atomic additive memory settings

## Files read

- `internal/config/config.go` → `Config`, `DefaultConfig`, `Load`: additive schema, overlay defaults and zero-config error contract.
- `internal/config/config_test.go` → `TestLoad`, `TestDefaultConfig`, `TestLoadMemorySearchProviders`: existing defaults and independent search declarations.
- `internal/sessions/registry.go` → `saveRegistryLocked`: same-directory staging, sync, close and rename convention.
- `docs/knowledge/features/config-package.md` → Surface, Defaults, Load semantics: consumer-owned validation; empty files are errors, unlike pyry-owned registries.
- `docs/knowledge/features/development-verification.md` → Prove that tests distinguish the change: inspect persisted keys and exact unknown numbers, not only decoded zero values.
- `CODING-STYLE.md` → Persistent data conventions: private atomic writes and package-local errors.
- QMD `pyrycode-docs` search for config atomic save: registry persistence precedents, confirmed against current code.

## Context

Persist managed memory independently of memory search declarations so later consumers
can configure memory without losing other daemon settings. This is one deliverable:
an additive persisted contract with a replacement operation. No decision record needed.
No other fetched feature branch overlaps the planned files.

Sizing: approximately 620 written lines including plan and tests, four new exported
types, zero consumer migrations, four acceptance criteria, no state machine.
This remains below all five builder limits after planning. The #2689 config analogue
was schema-only; raw preservation, atomic failure coverage and subprocess proof
account for the additional work here.

## Design

- Add `Config.Memory *MemorySettings`; absent/null means nil and defaults enable nothing.
- `MemorySettings` contains `MemoryVault`, additional roots, `MemoryEmbedding`, and
  `MemoryCapture`. String fields stay unconstrained. Optional path/reference and
  roots use omission tags; missing roots have length zero.
- `UpdateMemory(path string, settings MemorySettings) error` replaces all known
  memory fields. Callers supply the destination and serialize updates to it.
- Read existing JSON into `map[string]json.RawMessage`. Require a top-level object;
  allow absent/null memory and nested objects, reject other structural values.
- Merge only known fields at memory/vault/embedding/capture boundaries; retain
  unknown raw values including large integers and fractional/exponent numbers.
  Omitted optional keys are deleted, supplied arrays replace old arrays.
- Encode before filesystem mutation. Create parents at 0700 and a same-directory
  temporary file at 0600, write, sync, close, then rename. Remove staging on every exit.
  A package-private file interface/factory and rename hook support isolated fault
  injection without mutable globals. Production uses ordinary OS operations.
- Production files: `internal/config/config.go`, `internal/config/memory.go`.
  Tests: `internal/config/memory_test.go`.

## Concurrency model

Synchronous operation, no goroutines or shared mutable state. Callers serialize
read/modify/write updates to one destination; atomic rename guarantees complete
reader snapshots, not cross-process transaction isolation.

## State transitions and identity reuse

| Event | Race-enabled proof |
| --- | --- |
| Missing config becomes configured; reload in fresh process | `TestUpdateMemoryFreshProcess` |
| Repeated updates; separate→default, openai→local, roots replaced/cleared | `TestUpdateMemoryReplacement` |
| Absent/null objects become populated | `TestUpdateMemoryObjects` |
| Failed update leaves old bytes or missing file and permits retry | `TestUpdateMemoryFailures` |

## Error handling

Wrap read, structure/parse, encode, mkdir, create, chmod, write, sync, close and
rename errors with config operation context. Treat short writes as errors.
Failures before rename preserve exact old bytes or absence; cleanup closes the
temporary handle and removes staging, surfacing cleanup errors too.
`Load` keeps its current defaults and error behavior.

## Testing strategy

Write tests before production and observe the missing API/schema failure. Tables
cover legacy/default/null decode, both vault modes, independent agent/provider
choices, unsupported choices and opaque references. Inspect raw persisted objects
to prove nested preservation, numeric precision and deletion of optional keys.
Reject malformed/empty/non-object documents and each wrong-shaped nested object
without modification. Assert file and newly created parent modes. Inject partial
write, short write, sync, close and rename failures against existing/missing files;
verify staging cleanup and successful retry. Cover read/create failures. Re-exec
the test binary to Load committed settings independently. Run scoped race tests,
`go vet ./...`, and build `./cmd/pyry` with output outside the worktree.

## Open questions

None. Empty strings omit optional path/reference; zero-length roots clear the key.
Semantic validation and host-path resolution belong to consumers in #3117.

## Documentation handoff

Pending for documentation stage: `docs/knowledge/features/config-package.md`,
“Surface”, “Defaults” and “Load semantics”: document the memory shape, legacy/null
unconfigured state, parse-only loading/updating, caller-owned semantic validation
and atomic memory replacement preserving unrelated/nested unknown values while
clearing omitted known optional fields. Replace the deferred-Save statement for
this new operation.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `Load` decodes untrusted JSON; `UpdateMemory` checks object structure only. Neither makes paths, choices or references trusted; #3117 owns semantic validation.
- [Tokens] Only opaque non-secret reference metadata is stored. No credential backend is called; token lifecycle remains with #3112/#3113.
- [File operations] The explicit config path is caller-authorized. New parents are 0700, staged/committed files 0600. Rename replaces a destination symlink itself; existing parent symlinks follow ordinary OS path semantics, requiring a caller-owned parent. No vault path is used for I/O.
- [Subprocesses] No production subprocess; the test binary is re-executed directly with fixed test selection and a temporary config path.
- [Cryptography] No keys, nonces, secret comparison or cryptographic operations.
- [Network and I/O] Local file I/O only; no socket or service startup. Encoding finishes before staging starts.
- [Errors/logs/telemetry] Errors name operations and destination paths, not settings or references. No logs or telemetry are added.
- [Concurrency] No goroutines/locks; rename is the commit point. Caller serialization prevents lost updates. A signal before rename leaves the previous document; OS process termination can leave an uncommitted temp file, never a partial destination.
- [Threat model] Local config metadata only; no relay/mobile boundary changes. Host-path validation is explicitly deferred to #3117 and credentials stay in #3112/#3113.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-10
