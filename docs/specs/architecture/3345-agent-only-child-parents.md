# Agent-only child parents

## Files read

- `internal/thread/child.go` → `resolveChildren`, `childWork`: parent lookup and retained unresolved child state.
- `internal/thread/fold.go` → `Items`, `Feed`: ancestor omission and serialized repair on every entry.
- `internal/thread/shell.go` → `registerShell`: ordinary shell calls share lifecycle registration with agents.
- `internal/thread/cache.go` → `foldingRules`, `recoveryCandidate`: changed fold semantics invalidate old recovery epochs.
- `internal/thread/child_test.go` → `TestChildReclassification`, `TestChildScopes`, `TestChildNested`: closure/reopening, identity isolation and ancestor propagation.
- `internal/thread/main_test.go` → `testMainReplay`: full replay and every two-chunk partition comparison.
- `internal/thread/cache_test.go` → `TestStoreCacheEpochs`: incompatible folding rules rotate the epoch.
- `origin/feature/3342` → `TestChildRejectsShellParent`: skipped Bash reproduction to retain and enable before that branch merges.
- `docs/knowledge/features/thread-package-agents-and-background-work.md` § Parent repair: only strictly older agent items qualify; unresolved descendants stay omitted.
- `CODING-STYLE.md` and `docs/knowledge/features/development-verification.md`: table-driven behavioral assertions and executed-test evidence.

## Change

Require the resolved parent item's `Kind` to be `agent` in `resolveChildren`, in addition to the existing same-group lookup and strictly older creating ID. Bash/local_bash remain registered ordinary lifecycle-managed calls, but cannot establish child ownership. Keep unresolved rows internally and let `Items` omit them and their descendants. Increment `foldingRules` from 1 to 2 for the semantic change. No new types, lifecycle state, goroutines or failure modes are introduced; the owner continues serializing access to `Fold`. Genuine Agent/Task resolution, ancestor closure/reopening and source/lifetime/scope lookup stay in their existing paths.

Concurrent overlap: #3342 restructures resolution scheduling while preserving the guard being extended; #3324 also increments `foldingRules`. These changes require local merges, not a redesign or an unmerged API.

Sizing recheck: one deliverable, two acceptance criteria, approximately 160 total written lines including plan/tests, zero exported types, zero consumer updates and one added reject predicate; all limits remain satisfied.

## Testing strategy

Retain and enable `TestChildRejectsShellParent` from #3342, extending its metadata-free reproduction for Bash/local_bash and recorded Claude sources. Assert omission after every feed, including attributed text, terminal ordinary tools, nested Agent/Task items and their completed/active descendants, while the shell itself retains ordinary completion. Compare replay partitions with `testMainReplay`. Add a positive Agent/Task case for child ownership and ancestor closure. Watch shell rejection fail before implementing the guard.

Run `go test -race ./internal/thread/...`, `go vet ./...`, and build `./cmd/pyry` to scratch storage. Existing `TestChildReclassification`, `TestChildNested`, `TestChildLifetimeReuse`, `TestChildScopes` and shell lifecycle tests cover repeated finals, late-link reopening and reused identifiers under race detection. `TestStoreCacheEpochs/rules` covers cache invalidation. The verifier owns the full-module gate.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] The `resolveChildren` guard validates the immutable parent item kind before accepting parent IDs derived from history payloads. Sharing `agentGroup.calls` is insufficient authority; the planned kind check closes the observed false-attribution boundary while retaining source/lifetime/scope and older-ID checks.
- [Tokens, secrets, credentials] No generation or credential flow changes. Child payloads remain inert retained content; the change adds no exposure path.
- [File operations] Only `foldingRules` changes in persistence; existing `recoveryCandidate` compatibility rejection and checkpoint writes remain responsible for recovery. No new paths or file operations are introduced.
- [Subprocesses] The fold interprets recorded tool names and does not execute shell payloads. No subprocess invocation changes.
- [Cryptography] Epoch generation and cryptographic code are unchanged; the compatibility revision forces existing recovery to choose a fresh epoch.
- [Network and I/O] No reader, wire format, socket or size boundary changes; `Items` withholds unresolved rows before downstream publication.
- [Errors, logs, telemetry] Invalid parents remain unresolved rather than generating errors or logs containing payloads; no new logging or telemetry.
- [Concurrency] `Fold` remains owner-serialized; no locks, goroutines or shutdown paths are added. Replay/race tests exercise repeated repair through existing lifecycle paths.
- [Threat model] This repair prevents false history ownership by non-agent parents. Authentication and transport security stay with their existing boundaries; no separate security work is deferred by this design.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-14
