# #2761 — Resolve one service workspace base at startup

## Files read

- `cmd/pyry/main.go` → `runSupervisor`, `confineWorkdirToHome`, `withinDir`: composition root, canonical HOME confinement and boundary-aware comparison.
- `cmd/pyry/relay.go` → `relayWiring`, `startRelay`, `startRelayV2`: carry the startup value into the production manager configuration.
- `cmd/pyry/workspace_seed.go` → `seedDefaultWorkspace`, `seedWhenReady`: existing seed, marker and pool-readiness contracts stay intact.
- `cmd/pyry/workspace_seed_test.go` → seed assertions and stub creator: persisted rows, labels and failure logging.
- `internal/relay/v2session_handshake.go` → `WorkspaceRoot`, `handleNoiseInit`: lexical fallback and #2760's non-nil supplied-base contract, including empty omission.
- `internal/e2e/harness.go`, `internal/e2e/workspace_seed_test.go`, `internal/e2e/relay_v2_daemon_test.go` → existing daemon lifecycle, seed-file projection and encrypted handshake infrastructure.
- `docs/knowledge/features/conversations-registry.md` § One-time seed marker: wait for pool readiness to avoid orphan sessions; key the label on the stored canonical cwd.
- `docs/knowledge/features/protocol-package-handshake-control-payloads.md`, `docs/knowledge/features/v2-session-manager-state-machine-noise-init-happy-and-failure-path.md`: admitted-peer-only encrypted metadata and supplied/unsupplied semantics.
- `docs/knowledge/features/cli-verb-dispatch.md`, `docs/knowledge/features/e2e-harness.md`, `docs/knowledge/features/development-verification.md`: production wiring, teardown and raw-key assertions.
- `CODING-STYLE.md`, `docs/protocol-mobile.md` § Security model: conventions and trust boundaries.

## Context

The service process cwd holds the operator's instructions, but the daemon currently seeds and advertises the lexical legacy folder independently. #2760 is merged and provides the manager's supplied-base contract. This ticket connects one startup-resolved base to both consumers; it does not migrate persisted conversations or alter spawn overrides or relative-cwd normalization. No decision record is needed. No other fetched feature branch overlaps the planned existing files.

## Design

Add `resolveStartupWorkspaceBase(getwd func() (string, error), logger *slog.Logger) string` in `cmd/pyry/workspace_base.go`. Obtain the existing lexical fallback from `relay.WorkspaceRoot`; an empty fallback means HOME is unavailable or non-absolute and the result must be empty. An absolute cwd successfully confined by `confineWorkdirToHome` selects its canonical realpath, including HOME itself. All other inputs select the lexical fallback, even if it does not exist. No directory creation or trust marking occurs here.

`runSupervisor` resolves once after logger construction, using `os.Getwd`, independently of `--pyry-workdir`. Store the string in `relayWiring.workspaceBase`; `startRelayV2` always passes its address as `V2SessionConfig.WorkspaceBase`, including an empty string. Capture the same local string in the existing readiness-gated seed call. Update the seed's root comment only; its behavior remains unchanged.

Sizing: one deliverable, four acceptance criteria, no exported types, three consumer/wiring edits, fewer than ten rejection branches, approximately 450–550 total written lines including plan and tests (below 800).

## Concurrency model

Resolution is synchronous before relay construction. The string is immutable afterwards; the manager copies the supplied value on construction. Seeding uses the existing `seedWhenReady` goroutine, canceled by daemon shutdown and joined after `pool.Run`. No new production goroutines or locks.

## Error handling

Every unusable startup resolution emits one static warning event `workspace_base.fallback`, without paths, raw errors or derived attributes. Invalid HOME returns empty rather than accepting an otherwise absolute cwd. Existing spawn preflight remains strict; this fallback does not bypass it. Existing seed failure recovery and retry remain unchanged.

## Testing strategy

- Table-driven resolver tests: service folder, HOME, canonical legacy cwd, symlinked HOME/cwd, escaping symlink, prefix-sharing sibling, missing cwd, getwd error, empty/relative cwd, missing/unresolvable HOME and non-absolute HOME. Assert exactly one static event on fallback, none on success, and no creation/trust writes.
- Real daemon regression using existing fake relay/phone and sleep-Claude infrastructure, with process cwd under HOME and a different `--pyry-workdir`: decrypt `hello_ack`, inspect its raw workspace key, wait for persisted promoted General, label and marker; include the canonical legacy folder. Do not add a reusable harness.
- Seed tests restart with a different base and prove existing rows and labels remain intact, both marked and unmarked. Existing empty-root tests prove no channel/marker; a production source guard pins explicit empty-base forwarding and single startup resolution.
- Run race tests for `cmd/pyry` and the targeted tagged e2e regression, existing relay handshake security tests, `go vet ./...` and `go build` for `cmd/pyry` with output outside the worktree. The verifier owns the full-module gate.

## Open questions

None.

## Documentation handoff

Pending for the documentation stage:

- `docs/protocol-mobile.md`, “hello_ack (v2-specific note)”, and `docs/knowledge/features/protocol-package-handshake-control-payloads.md`, the `WorkspaceRoot` contract: daemon supplies canonical service process cwd when usable and HOME-confined, lexical `$HOME/pyry-workspace` fallback otherwise, empty without absolute HOME. Preserve #2760's generic supplied/unsupplied contract and encrypted, admitted-peer-only, no-path-logging rules.
- `docs/knowledge/features/conversations-registry.md`, “One-time seed marker (#2569)”: describe `<startup-base>/default` and explicitly state existing rows are not migrated.
- `docs/knowledge/features/v2-session-manager-state-machine-noise-init-happy-and-failure-path.md`, workspace metadata selection: describe daemon consuming a startup-resolved value rather than resolving HOME per handshake.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `resolveStartupWorkspaceBase` requires absolute HOME and cwd, then uses `confineWorkdirToHome` and `withinDir` on canonical paths. Network clients cannot choose this base. Advertisement grants no filesystem access.
- [Tokens/secrets] No credential generation or storage changes. Metadata is populated only in the existing admitted-peer branch of `handleNoiseInit`; rejected peers still decrypt an ack with no workspace key.
- [File operations] Resolver performs read-only canonicalization. Symlink escapes and prefix siblings select fallback; the existing creator rechecks confinement before creation/trust marking. Existing atomic registry persistence and modes remain unchanged.
- [Subprocesses] No new subprocess production path; the Claude spawn override and strict preflight remain independent and unchanged.
- [Cryptography] Existing Noise_IK encrypted ack and cipher ownership remain unchanged; no new primitives or nonce use.
- [Network/I/O] No new socket reads, servers or payloads; existing relay limits and deadlines continue to apply.
- [Errors/logs] Static warning contains neither HOME, cwd nor error values. Tests pin event count and exact attribute shape; seed logs retain existing pathless contract.
- [Concurrency] Startup string is resolved before worker startup and never mutated. Existing readiness/cancellation/join path owns seeding.
- [Threat model] Existing relay untrusted-routing and admitted-client authorization boundaries remain intact; this metadata does not widen workspace filesystem access.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-04
