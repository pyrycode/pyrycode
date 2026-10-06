# Control serving waits for pool readiness (#2866)

## Files read

- `cmd/pyry/main.go` → `runSupervisor`: early socket ownership, detached control context, and writer-before-control shutdown joins.
- `cmd/pyry/workspace_seed.go` → `seedWhenReady`: existing cancellable readiness gate from #2569.
- `cmd/pyry/session_revive_test.go` → `newRecordingPool`, `runPoolReady`: hermetic runners and pool lifecycle cleanup.
- `cmd/pyry/relay_guard_test.go` → `formattedGoFunc`: composition-root wiring assertions without matching comments.
- `cmd/pyry/channel_delivery_test.go` → `TestChannelDelivery_ShutdownRetainsOwnershipUntilWritersStop`: existing daemon shutdown ownership proof.
- `internal/sessions/pool.go` → `Run`, `Ready`, `MintWith`: readiness closes after supervision is wired; mint persists before supervision.
- `internal/control/server.go` → `Listen`, `Serve`, `Close`, `handle`: socket ownership and handler joins.
- `docs/knowledge/features/sessions-package.md` and `sessions-package-key-types-config-bootstrapevicted-pool-ready.md`: readiness prevents permanently unsupervised registry entries.
- `docs/knowledge/features/control-plane.md` § Lifecycle and Testing: serving cancellation must remain detached until writers stop; root wiring guards complement socket tests.
- `docs/knowledge/features/development-verification.md` § Prove that tests distinguish the change: hold readiness open for cancellation and test the ungated mutation.
- `docs/knowledge/features/cli-verb-dispatch.md` and `CODING-STYLE.md`: daemon cancellation classification and Go conventions.

## Change

Keep `ctrl.Listen` at its current early ownership claim. Replace the direct
`ctrl.Serve` call in its existing goroutine with a private
`serveControlWhenReady(startupCtx, ready, controlCtx, ctrl) error` helper in
`main.go`. It waits for readiness or startup cancellation; cancellation returns
the context error, while readiness delegates to `ctrl.Serve(controlCtx)`.
The wait uses the daemon context and serving retains the detached control
context, preserving delivery-writer shutdown ordering and the existing close
and join paths. Readiness is the pool supervisor handle, never child state.
No new goroutines, public types, wire contracts, or persistence paths are added.
No overlapping feature branches were found for the planned files.

Sizing: one startup-order deliverable, approximately 250 total written lines,
zero exported types, one consumer, two acceptance criteria, one cancellation
branch. All five limits remain below the builder ceilings.

## Testing strategy

Use a real Unix control socket and persistent pool with hermetic stub runners.
Queue `sessions.new` after bind while the pool has not run, prove no response,
unchanged registry bytes and bootstrap-only memory, then run the pool and prove
the queued request succeeds with exactly one new entry in memory and on disk.
Also cover an already-ready pool and cancellation with readiness held open,
joining startup and releasing/rebinding the socket. Pin the root's helper call
and context arguments using `formattedGoFunc`, so removing the production gate
fails even when helper tests remain green. Check the behavioral test against an
ungated helper mutation. Run the existing daemon ownership shutdown test, then
`go test -race ./cmd/pyry/...`, `go vet ./...`, and `go build ./cmd/pyry` (output
outside the worktree). The dispatcher owns the full-module verifier gate.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `control.Server.Listen` retains the owner-only local socket boundary; `handle` retains request parsing. Requests cannot enter handlers before pool readiness.
- [Tokens and cryptography] The helper transports only contexts, a channel and the existing server; no credential, randomness or crypto operations change.
- [File operations] `Listen` and `Close` retain socket creation/removal and permissions. The existing pool registry writer remains unchanged; the gate prevents its premature invocation.
- [Subprocesses] The gate observes `Pool.Ready`, not child output or process state; executable selection and child cancellation remain unchanged.
- [Network and I/O] No new reads or decoders; the existing local socket handshake deadline remains in `handle`. Pre-readiness connections remain in the kernel backlog until serving or close.
- [Errors, logs and telemetry] Cancellation returns `startupCtx.Err()` internally; no client payloads, paths or credentials are logged by the helper.
- [Concurrency] The wait must use the daemon context, while `Serve` must use detached `controlCtx`. Mixing these would either hang the startup join or release ownership before delivery writers stop. Tests cover cancellation and existing shutdown ownership.
- [Threat model] This is a local startup ordering change under the existing socket ownership boundary, with no relay/mobile message, authorization or audience changes.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-06
