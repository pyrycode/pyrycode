# Managed Linux/OpenAI memory runtime

## Files read

- `internal/update/checksum.go` → `VerifySHA256`: integrity precedent; updater keys cannot authenticate upstream artifacts.
- `internal/update/replace.go` → `AtomicReplace`: temporary-file, sync, rename publication convention.
- `cmd/pyry/update.go` → `installRelease`: verified download before execution/publication.
- `internal/memorysearch/detect.go` → `Detect`: read-only access detection stays separate from installation.
- `docs/knowledge/features/update-package.md`: signature trust must precede artifact execution.
- `docs/knowledge/features/memorysearch-package.md`: installed runtime alone cannot assert workspace search readiness.
- `docs/knowledge/features/development-verification.md`: executed evidence must survive in commits.
- `CODING-STYLE.md`: context cancellation, subprocess helpers, returned errors and private persistence.
- Upstream memsearch v0.4.21 `pyproject.toml`: OpenAI base dependencies; local extras excluded.

## Context

Supply one independently callable installer in `internal/memoryruntime`, without daemon wiring, credentials, watchers, indexing or model initialization. The daemon consumer remains #3148. A decision record is warranted for the shipped artifact lock and immutable generation publication.

Sizing: approximately 1,150 written lines including the complete wheel lock, security/lifecycle code, hermetic tests, smoke/evidence and this plan; three exported types, zero consumer updates, five acceptance criteria. Verified lineage is #3152 → #3147 → #3098. The grandchild rule requires building despite the line overage; `needs-human:sizing` records it. Splitting artifact security from lifecycle would violate the one-consumer floor. No fetched feature branch touches this new package.

## Design

- `Install(ctx, Options) (Runtime, error)` accepts service-account home, mode (`openai`) and an optional synchronous callback with finite stage names. Default home comes from `os.UserHomeDir`. Success returns absolute managed Python/CLI locations and exact versions; failures are recoverable and expose bounded stage information without subprocess output.
- Advertise only Ubuntu 24.04 Linux amd64 with glibc >=2.39, the available smoke target. Other operating systems, distributions, architectures and modes fail before setup. Later tickets extend the target/mode lock.
- Ship standalone CPython 3.12.11 (20250918 build), its bundled bootstrap pip 24.3.1, memsearch 0.4.21, pymilvus 2.5.16, Milvus Lite 2.5.1, OpenAI 1.109.1 and the full 32-wheel closure with URLs and SHA-256 hashes. Build-time resolution is frozen in shipped metadata; runtime installs verified local wheels with no index, dependencies, cache, source builds or resolution. No build dependencies execute.
- Store only runtime generations, downloaded artifacts and publication metadata under `<home>/.pyry/memory/runtime`. Use a rooted filesystem to confine operations, verify ownership and permissions, reject destination symlinks/hardlinks, and create directories at 0700 and metadata at 0600.
- Hash verified downloads before extraction/execution; cache them by shipped hash and rehash on reuse. Download into exclusive temporary files, sync and publish only matching hashes. Bound downloads and extraction; cancellation preserves verified cache entries.
- Extract regular files/directories before archive links. Reject absolute/traversing names, escaping links, duplicate entries and special files. Resolve link targets within the extraction root before creating links; no writes follow archive links.
- Install into unique, permanent generation paths so pip console-script shebangs never move. Seal the completed tree, run version/import/CLI/pip-compatibility probes, then atomically publish the generation/lock/seal record. Reuse requires matching lock, a valid tree seal and successful probes. Retain prior generations, including previously returned launch locations; incomplete generations are never returned and retries use new paths.

## Concurrency model

One owner-only flock file per service-account runtime serializes all attempts, including independent processes. Nonblocking acquisition waits with context cancellation; waiters never cancel the holder. The holder retains the lock through publication. No application goroutines are started. Subprocesses use a sanitized environment, context cancellation of their process group and Linux parent-death termination; wheel-only installation avoids build subprocesses. Process interruption releases the kernel lock; a retry ignores unpublished generations.

## State transitions and identity reuse

| Event | Contract | Race-enabled test |
| --- | --- | --- |
| Repeat setup | Rehash seal and probe before returning same generation | `TestRepeatAndReplacement` |
| Shipped lock changes / old seal corrupts | Create new generation; leave old locations untouched | `TestRepeatAndReplacement` |
| Two concurrent calls | One active attempt; follower verifies completed generation | `TestConcurrentAndCancelledWaiter` |
| Independent processes share target | Kernel lock serializes installer | `TestCrossProcessExclusion` |
| Waiting caller cancels | Holder continues and publishes | `TestConcurrentAndCancelledWaiter` |
| Active attempt cancels / probe or install fails | No publication; next call retries cached artifacts | `TestFailuresRetry` |
| Process dies before publication | No partial usable state; lock releases for retry | `TestInterruptedRetry` |
| Corrupt/incomplete download | No execution; subsequent attempt safely redownloads | `TestDownloadIntegrity` |

## Error handling

Unsupported target/mode, unsafe destination, integrity, install and probe errors carry a finite stage and preserve `errors.Is` cancellation. Network failures never expose response bodies. Failed replacement leaves the old publication record and generation untouched. Attempts use exclusive names and never mutate another generation. No credentials enter child environments or metadata.

## Testing strategy

Write failing hermetic tests first with controlled HTTP downloads, generated archives and install/probe seams. Prove the lifecycle table plus unsafe paths, ownership/modes, archive links and integrity rejection. Use TestHelperProcess for cross-process locks and interruption. Run `go test -race ./internal/memoryruntime/...`, `go vet ./...` and `go build ./cmd/pyry`; the verifier owns the full-module gate.

A build-tagged `TestSmoke` is the opt-in network entry point, outside make check. It calls the production installer, verifies imports/CLI/dependency compatibility and opens/closes Milvus Lite in a temporary database without credentials or models. Commit its actual generated evidence beside the smoke test, including invocation, distro, architecture, libc, exact versions and named results. Execute it on the sole advertised target.

## Open questions

None. Wheel closure and supported target are frozen before implementation; smoke failure requires revising the lock or advertised support before publication.

## Documentation handoff

Pending documentation stage: `docs/deployment.md`, new “Managed memory runtime” section: document exact pinned components/dependency lock, supported Linux architectures/minimum OS/libc requirements, private runtime/download-cache locations, opt-in smoke invocation and resumable setup. Distinguish installation from index readiness. State that this slice alone neither starts managed memory nor migrates the existing pyrybox watcher, and that macOS/local mode await #3153/#3154.

## Security review

**Verdict:** PASS

- [Trust boundaries] Shipped hashes gate runtime and all wheel code. `download` verifies cache and network bytes before `extract` or `provision` can use them. Publication is gated by tree seal and probes, not a downloaded checksum file.
- [Tokens] No credentials accepted, inherited by children or stored. Provider initialization belongs to #3148.
- [File operations] MUST FIX addressed in design: confinement alone permits in-root destination aliases. `privateDir` and `regularFile` must additionally reject symlinks, foreign ownership, writable shared directories and hardlinked mutable metadata. Archive links are delayed until every file is written.
- [Subprocesses] Fixed executable/argv only; no shell. `run` uses a minimal environment, process-group cancellation, output limits and parent-death termination on Linux.
- [Cryptography] SHA-256 digests compiled with the installer bind every artifact; no shared updater signing key is reused.
- [Network/I/O] Context-bound HTTPS with timeouts and size limits; neither response bodies nor arbitrary subprocess output enter failures/progress. Unpacked size and member count are bounded.
- [Errors/logs] Finite stages and generic failure text; wrapped causes retain cancellation classification. No payloads, environment or credentials are logged.
- [Concurrency] Lock acquisition through publication is one critical section. Cancelled waiters close only their own descriptors. Unique generations avoid stale-writer replacement after interruption.
- [Threat model] Hostile network/archive and other-user filesystem changes are in scope. A malicious process already running as the service account can replace its own binary/metadata and is outside this boundary; later provider/daemon lifecycle belongs to #3148.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-10
