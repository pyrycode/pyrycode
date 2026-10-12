# Exclusive managed memory database owner

## Files read
- `cmd/pyry/memory_roots.go` → `effectiveMemory`, `resolveEffectiveMemory`, `memoryOverlaps`: canonical vault identity and snapshot/source exclusions.
- `cmd/pyry/memory_credential.go`, `memory_credential_file.go` → `resolveMemoryCredential`, `memoryOpen`, `memoryProtected`: fresh credential resolution and descriptor validation.
- `internal/memoryruntime/install.go`, `process.go`, `smoke_test.go` → `Runtime`, `Install`, `TestSmoke`: verified interpreter/version contract; installer runner is not an owner.
- `docs/knowledge/features/memoryruntime-package.md`: retained generations, native smoke records and process death versus lock recovery are distinct proofs.
- `docs/knowledge/features/development-verification.md`: captured bytes must be committed; counters and target/pins must be checked independently.
- Pinned memsearch `MemSearch.__init__`, `MemSearch.close`, `MilvusStore._ensure_collection_for_write`, `MilvusStore.close`: fresh constructors are lazy; explicit backend connection and release are required.
- Pinned `LocalEmbedding` and `OpenAIEmbedding`: selected model initialization and fixed non-user embedding probe.
- Dispatcher `fleet-support.py` → `native_recipe`: current recipe executes only installer smoke, so companion execution requires a dispatcher handoff.

## Context
Retain one independently callable database/provider owner without daemon wiring, source reads, indexing, watchers or capture. Existing user databases and immutable installer generations remain untouched. A decision record should describe inherited lock lifetime and process identity protection.
The estimated 1200 written lines exceed the 800-line ceiling. Actual parent links are #3098 → #3148 → #3286; `needs-human:sizing` already applies. Suggested future seams would be lifecycle ownership and native evidence, but the depth gate requires building this ticket in place. Five criteria, zero exported APIs, zero consumer migrations. No concurrent feature branch touches the new owner files.

## Design
`startMemoryOwner(ctx, runtime, settings, effective)` returns a private owner with `close() error`. Runtime versions constrain the store format; provider, model and actual probe dimension constrain compatibility. Stores live at `~/.pyry/memory/stores/<sha256(canonical vault)>`; selected-model caches live in that store's `models` directory. Daemon names/additional roots do not affect identity.
Go validates private destinations using no-follow descriptor traversal and rejects source overlaps before creating them. It acquires a nonblocking kernel flock before any database opening and passes the locked descriptor to an embedded isolated Python controller. A private socket carries initialization (including freshly resolved credentials) and finite status codes. No request is placed in argv/environment or persisted.
The controller forks a worker, retaining the worker as its unreaped direct child through cleanup. The worker patches subprocess launch to inherit the lock descriptor into backend children. Thus unexpected worker/controller exit cannot authorize a competing opener while a backend retains the lock. No persisted PID authorizes a signal. Process-group escalation occurs while the controller retains its direct child's PID identity.
Worker constructs pinned MemSearch with no paths, probes the selected provider with fixed non-user text, checks compatibility, explicitly connects a fresh store, and retains the object. Compatibility metadata is content-free and never replaces incompatible state. Close follows MemSearch/MilvusStore lifecycle then controller verifies lock release. Raw child stdout/stderr are discarded; private statuses are bounded and whitelisted.

## Concurrency model
One Go lifecycle goroutine reads the private channel and joins the command. Cancellation/close closes that channel. Python controller monitors channel EOF and worker readiness/exit. Worker runs provider initialization and later waits for stop. Cleanup has a two-second graceful interval, two-second TERM interval and two-second KILL/join interval; the Go join bound includes controller cleanup. Every goroutine/process has a stop path. A backend escaping cleanup still retains exclusion via its inherited descriptor.

## State transitions and identity reuse
| Event | Race-enabled proof |
| --- | --- |
| Successful local/OpenAI init and stop | `TestMemoryOwnerProviders` |
| Local/OpenAI probe failure then retry | `TestMemoryOwnerFailures` |
| Cancellation during init | `TestMemoryOwnerCancellation` |
| Concurrent cross-process attempts and release/restart | `TestMemoryOwnerExclusion` |
| Compatible reopen and incompatible provider/model/dimension/format | `TestMemoryOwnerCompatibility` |
| Stale unrelated PID bookkeeping | `TestMemoryOwnerStalePID` |
| Abrupt daemon death / worker death with live backend | `TestMemoryOwnerProcessDeath` |
| Graceful/TERM/KILL cleanup with backend child | `TestMemoryOwnerShutdown` |

## Error handling
Fixed recoverable errors classify unsafe storage, occupancy, initialization and incomplete release. Preserve context cancellation identity. No backend reset/delete/replacement. Failed cleanup preserves exclusion through surviving descriptor holders. Symlinks/hardlinks/wrong owners/permissive mutable paths are rejected without chmod or writes to their targets.

## Testing strategy
Write focused hermetic tests first using the production Python channel/controller with controlled memsearch/provider/backend modules. Assert readiness means a connected store; probe text is never upserted. Inspect argv/environment, metadata and error text with planted secrets and oversized output. Exercise real child lifetimes, cancellation and reopens. Run `go test -race ./cmd/pyry/...`, `go vet ./...`, `go build ./cmd/pyry`.
Opt-in `memory_runtime_smoke` companion calls Install and opens/closes/reopens production owners in separate local/OpenAI vaults. Real selected local model; synthetic OpenAI credential and test-only loopback embedding endpoint. Committed records must contain exact installed pins, supported native host and named passed checks. No network/model downloads in ordinary tests. Required native records: `cmd/pyry/testdata/memory-owner-linux-amd64.json`, `cmd/pyry/testdata/memory-owner-macos-arm64.json`.

## Open questions
- Resolve native companion scheduling: current managed recipe does not execute `cmd/pyry`; report exact invocation/evidence paths for dispatcher support rather than changing installer architecture.

## Documentation handoff
Pending documentation stage: `docs/deployment.md`, “Managed memory runtime”: document chosen private store/model-cache paths, canonical-vault ownership across daemon names, compatibility rejection and safe release/recovery, plus native worker smoke invocation/evidence. Distinguish provider/database health from index readiness and agent access. State daemon activation awaits #3287 and no existing pyrybox watcher/database is migrated or removed.

## Security review
**Verdict:** PASS
- Trust boundaries: private descriptor channel accepts bounded JSON initialization; statuses are a fixed whitelist.
- Tokens: SHOULD FIX implemented in build: freshly resolve reference; credential only in private request/provider memory; clean child environment; discard raw output and emit fixed errors.
- Files: SHOULD FIX implemented in build: no-follow descriptor traversal, owner-only mutable files/directories, reject hardlinks/source overlaps and revalidate immediately before use; atomic metadata publication.
- Subprocesses: no shell, isolated verified Python; inherited lock outlives backend; controller signals only an unreaped child-owned process group, never PID records.
- Cryptography: SHA256 is a stable canonical-vault identifier, not authorization; no new cryptographic protocol.
- Network/I/O: private request/status limits; provider cancellation handled by controller cleanup; endpoint injection only in tests.
- Errors/logs: fixed sanitized recovery errors; no raw worker diagnostics retained.
- Concurrency: channel EOF initiates bounded cleanup on both targets; surviving backend keeps lock.
- Threat model: source/index/query/daemon/agent integration deferred to #3099/#3103/#3287.
**Reviewer:** builder (self-review)
**Date:** 2026-10-12

## Revisions
- 2026-10-12: Keep the read half of the private channel open when stopping; EOF on its write half initiates cleanup while the controller can still return verified release. Treat worker socket reset as exit and proceed through bounded group cleanup, rather than bypassing the release check.
- 2026-10-12: Selected local model downloads use a private flat snapshot at `models/selected`, then pinned LocalEmbedding loads that snapshot with remote code disabled. This avoids symlink-based Hugging Face snapshot destinations while retaining memsearch's provider implementation.
- 2026-10-12: Native companion scheduling is blocked by #3290: the managed recipe only invokes the installer package. Record validation rejects absent/invalid records; the two committed-native-record assertions are explicitly skipped pending this external capture dependency. Keep `needs-live-artifacts`; after #3290, run both native companions, commit the exact records and remove the temporary missing-record skip. Installer package/API remains independent.
- 2026-10-12: Cap combined worker stdout/stderr at 64 KiB without retaining any bytes. Overflow closes the private write half and prevents successful initialization. `TestMemoryOwnerFailures` emits oversized planted-secret output through the real child pipe. `TestMemoryOwnerSurvivingBackend` proves an escaped live backend retains exclusion and cleanup returns a bounded recoverable error without signalling from PID bookkeeping.
- 2026-10-12: `TestMemoryOwnerFreshCredential` uses the existing saved file-backend reference through production `resolveMemoryCredential`, then replaces its token to prove a reopen freshly resolves rather than reusing the prior credential. Socketpair setup uses the Go fork lock and portable close-on-exec operations for both supported targets.
- 2026-10-12: Publish provider/model/dimension compatibility through a uniquely named private temporary before opening a fresh database. A crash cannot leave a newly created database without its compatibility identity, and stale temporary bookkeeping cannot block a retry. This metadata records compatibility, never health; readiness still requires the connected backend and successful probe.
