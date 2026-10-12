# Local embedding dependency installation

## Files read

- `internal/memoryruntime/install.go` → `Install`, `engine.install`, `lock`: independently callable installer and permanent verified publication.
- `internal/memoryruntime/targets.go` → `selectPins`: supported target/mode selection and numeric macOS minimum.
- `internal/memoryruntime/process.go` → `provision`, `probe`, `run`: offline binary installation, full version/import/CLI/compatibility probes and sanitized cancellable subprocesses.
- `internal/memoryruntime/artifacts.go` → `download`, `validateWheel`, `extract`: shipped hashes, cache revalidation and archive containment.
- `internal/memoryruntime/storage.go` → `acquire`, `publish`, `treeSeal`: cross-process exclusion, atomic publication and private generation integrity.
- `internal/memoryruntime/install_test.go`, `process_test.go`, `targets_test.go` → lifecycle/security harness, production probe cancellation and committed smoke validation.
- `internal/memoryruntime/{linux-amd64,macos-arm64}.lock.json`, `smoke_test.go`, existing smoke records: exact base closure and native installation evidence.
- `docs/knowledge/features/memoryruntime-package.md`: preserve wheel basenames and generation shebang paths; fresh full probes on reuse; compatibility errors must retain cancellation causes; canonicalize macOS temporary homes.
- `docs/knowledge/decisions/044-memory-runtime-lock-and-generations.md`: reuse reviewed embedded artifact identities, no new decision record.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md` § Test execution and artifact survival: behavior proofs and committed native evidence.
- Upstream memsearch v0.4.21 `pyproject.toml`: local extra requires einops >=0.8.2 and sentence-transformers >=3.0.

## Context

Add credential-free local dependency installation to the existing installer, without constructing providers, downloading models, initializing production databases or changing daemon/watchers. Both existing targets remain Ubuntu 24.04 amd64/glibc >=2.39 and macOS arm64 >=26.6.2. Installation establishes no indexing or agent-search readiness.

Sizing: one observable deliverable, approximately 740 written lines including plan, locks, tests and both-mode evidence; zero new exported types/interfaces and zero production consumer migrations; five acceptance criteria; two added mode branches, no new lifecycle reject states. The #3153 analogue reused the same installer/security lifecycle. No fetched feature branch overlaps the planned package files. Documentation is a later-stage handoff.

## Design

Extend each existing platform JSON lock with `local_wheels`, the exact additional binary-wheel closure. Preserve all 32 base pins and their artifact identities. Pin sentence-transformers 5.1.1, transformers 4.57.1, einops 0.8.2 and torch 2.8.0 (Linux CPU build 2.8.0+cpu), plus every resolved transitive distribution. Select platform-compatible CPython 3.12 artifacts from upstream metadata and ship their SHA-256 identities. No source/build dependencies or dynamic dependency resolution are used at runtime. CPU-only Linux torch avoids installing CUDA executables and unnecessary GPU requirements.

`selectPins(goos, arch, mode) (lock, error)` accepts `openai` and `local`; local selection appends only the platform's additional wheels and sets a private local marker. OpenAI selection retains its original serialized lock identity and downloads no local wheels. The existing single publication describes the most recently verified requested mode. Switching may provision another permanent generation; previous returned paths remain valid. Failed switching leaves the current publication untouched.

`probe` retains imports, all exact Python/pip/distribution versions, CLI version and `pip check`. Local probes additionally import einops, sentence_transformers and the memsearch local embedding module, and execute a tiny CPU tensor operation without constructing a model. Both modes use the existing sanitized environment and offline model flags. No higher host minimum is expected; native imports/tensor execution must establish compatibility with both existing targets before evidence is committed.

Extend tagged `TestSmoke` to install/reuse both modes in one private home, freshly verify retained earlier paths after switching, and open/close a separate temporary Milvus Lite database for each mode. Record host identity, invocation, each mode's complete exact version map and named executed/passed checks. Hermetic readers reject missing modes, incomplete/wrong pins and unexecuted results. Native fleet checks use the full pushed candidate revision and their exact records are committed.

## Concurrency model

No new production goroutines. The existing account-wide flock covers both modes through verification and publication. Independently cancelled waiters close their own descriptors. Managed subprocess groups are joined and killed on cancellation; Linux retains parent-death termination. Native test tasks run outside the installer and produce durable retrieved artifacts.

## State transitions and identity reuse

| Event | Race-enabled coverage |
|---|---|
| Repeated local/OpenAI request, fresh compatibility failure, corrupt/missing local dependency, replacement | `TestLocalSwitchAndReplacement`, `TestLocalLifecycleContracts/RepeatAndReplacement` |
| OpenAI → local → OpenAI switching; retained earlier paths | `TestLocalSwitchAndReplacement`, tagged `TestSmoke` |
| Failed local install/probe/cancellation → retry, OpenAI publication preserved | `TestLocalFailurePreservesOpenAI` |
| Concurrent holders/waiters and independently cancelled waiting | `TestLocalLifecycleContracts/ConcurrentAndCancelledWaiter` |
| Cross-process deadline and verification/reuse after release | `TestLocalLifecycleContracts/CrossProcessExclusion` |
| Installer interruption then retry with verified download reuse | `TestLocalLifecycleContracts/InterruptedRetry` |
| Unsafe destination/generation alias and cache revalidation | `TestLocalLifecycleContracts/UnsafeDestinations`, `/GenerationSymlinkRejected`, `/DownloadIntegrity` |
| Actual compatibility command cancellation/deadline then retry | `TestLocalLifecycleContracts/CompatibilityProbeCancellation` |

## Error handling

Unsupported targets/modes retain recoverable `ErrUnsupported`. Integrity, archive/destination rejection, install and probe failures return no launch paths and publish nothing partial. `Failure` preserves underlying errors including context cancellation/deadline, while emitting only bounded stage information. Earlier verified paths/publication survive failed local setup. Existing download, wheel, tree and subprocess limits remain enforced.

## Testing strategy

Write failing local selection/closure and mode lifecycle tests first, then implement. Reuse inherited lifecycle/security and actual compatibility cancellation tests under local mode. Add focused local switching, fresh verification, replacement and failed/cancelled retry tests with existing OpenAI installed. Run `go test -race ./internal/memoryruntime/...`, `go vet ./...` and `go build ./cmd/pyry` (binary output in scratch). Network checks stay behind `memory_runtime_smoke`; run tagged `TestSmoke` natively on both targets, inspect actual named counts and commit both generated records. The verifier owns the full-module gate.

## Open questions

Resolve during build: verify that the selected binary closure satisfies native macOS dependency markers and both targets' imports/native libraries within inherited resource caps. Any additional real host minimum must be explicitly declared and proved, without dropping either target.

## Documentation handoff

Pending documentation stage: `docs/deployment.md`, “Managed memory runtime”: add exact local dependency pins/locks and additional minimum requirements, mode-compatible reuse/replacement and the both-mode opt-in smoke invocation for both supported targets. Describe private runtime/download-cache storage; selected-model cache initialization belongs to #3148. Retain resumable setup, installation-versus-index-readiness wording and the statement that this slice alone neither starts managed memory nor migrates the existing pyrybox watcher.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `selectPins` selects shipped platform/mode data; `download` verifies pinned SHA-256 before any extraction/execution. Upstream resolution metadata prepares reviewed locks, never installation-time trust.
- [Tokens] `run` supplies a private home and minimal environment without inherited credentials; local imports never construct a provider/model. No credentials are persisted.
- [File operations] Retain `openPrivateHome`, `privateDir`, `regularFile`, `validateWheel`, `extract` and `treeSeal` ownership/link/confinement checks and private permissions; only `publish` atomically updates verified metadata.
- [Subprocesses] Explicit arguments and isolated Python retain process-group cancellation and bounded output. CPU tensor execution initializes native computation without network/model access.
- [Cryptography] Existing SHA-256 artifact/tree identities and cryptographic random generation paths remain; no key material is added.
- [Network/I/O] Keep the 512 MiB download and 2 GiB wheel/tree bounds, HTTP timeout and context. CPU-only torch must fit them; no service/listener is introduced.
- [Errors/logs] Existing finite stages and generic `Failure.Error` exclude arbitrary subprocess output; evidence contains versions/host/checks only.
- [Concurrency] Shared flock serializes both modes; permanent unique generations and publication after probe/seal preserve earlier installations after interruption. No new background tasks in production.
- [Threat model] Account-local destination security and reviewed upstream code apply; relay/mobile authorization and selected-model cache/provider initialization remain OUT OF SCOPE, owned by #3148.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-12
