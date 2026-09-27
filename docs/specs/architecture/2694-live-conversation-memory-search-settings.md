# Live conversation memory search settings (#2694)

## Files read

- `internal/memorysearch/detect.go` → `Detect`, `Input`, `LaunchEvidence`: scoped evidence and aggregate precedence.
- `internal/relay/v2session_settings.go` → `handleRequestSessionSettings`: calls the optional provider only after run settings resolve and preserves the reply on error.
- `cmd/pyry/main.go` → `resolveBoundRunSettings`, `resolveBoundMCPStatus`: registry and pool binding patterns, plus exact-child query.
- `cmd/pyry/relay.go` → `relayWiring`, `startRelayV2`: provider seam into the relay manager.
- `internal/sessions/pool.go` → `Lookup`, `HarnessFor`: session identity and selected agent.
- `internal/streamsup/runner.go` → `beginSpawn`, `spawnAndWait`, `setStdin`, `takeStdin`, `QueryMCPStatus`: effective child workspace, environment, generation, and query lifecycle.
- `cmd/pyry/codex_runner.go` → `runOnce`, `codexRunner`: daemon-owned home, work directory and current client.
- `internal/config/config.go` → `Load`: fresh declarations and parse failure.
- `docs/knowledge/features/memorysearch-package.md` → effective evidence: host sighting is installation only; incomplete checks yield unknown unless other access is confirmed.
- `docs/knowledge/features/v2-session-manager-state-machine-inbound-request-session-settings-the-rea.md` → `request_session_settings` sequencing and omission contract.

## Context

The detector and optional reply seam exist, but the daemon does not supply the provider. A saved conversation workspace can lag a moved child; the provider must use live launch provenance and the registry's current session binding. This is a security-sensitive read boundary. Documentation stage should update the two topic sections named below; no new ADR is needed.

## Design

`memorySearchFor` returns a `MemorySearchFor` callback only with a registry and pool. Each invocation gets the registry-owned conversation record, rejects an empty or different session ID than the settings resolver supplied, and looks up that exact pool session. `HarnessFor` identifies the selected agent. A child launch snapshot supplies the effective workspace and child PATH, with a generation token. Claude's snapshot is published with the live stdin binding; Codex's comes from the active runner and daemon-owned home. No stored conversation cwd, bootstrap fallback, or personal Codex home enters the detector.

The callback reloads `config.Load` on every read and calls `memorysearch.Detect` with the current launch evidence. Its live MCP check calls `QueryMCPStatus` only on the selected Claude runner; unavailable/ineligible query leaves that check incomplete. Effective plugin inventory is not currently exported by either runner; it remains incomplete, as does a host CLI inventory without a completed explicit check. The provider never treats a host CLI sighting as child access. A configured declaration can independently confirm access despite incomplete launch checks. An installed disabled declaration remains in the report without making the aggregate available. This conservative posture allows `absent` only when every applicable check is actually complete.

After the query, the provider rechecks the registry binding, pool session identity and child generation. Any change yields `unknown` with no cross-child provider list. `Detect` errors also yield `unknown`; the relay's existing error handling keeps the ordinary settings reply intact. A successful result maps its availability and provider fields to `protocol.MemorySearchReport`.

## Concurrency model

The provider runs on the requesting connection's settings worker and starts no goroutine. Child launch data is copied under each runner's existing mutex. `QueryMCPStatus` has its own child-generation guard; the provider checks the generation again after the query. The registry and pool are read without a lock spanning child I/O. A bounded child query uses the existing settings query timeout and the request context. No lock is held over file I/O or the query.

## Error handling

An unresolved binding remains the relay's omitted field case. Once the settings resolver supplied an ID, a mismatch, unsupported runner, absent current child, invalid workspace, config read error, query failure, or changed binding returns a present `unknown` report. A config read error is passed to `Detect` so independently confirmed usable launch evidence can still win per its aggregation rule. No error includes client-supplied IDs or config contents in a reply or log.

## Testing strategy

- RED first: production provider tests with two bound sessions of different agent/workspace, mutable config file, child replacement, disabled declarations, and incomplete checks. Assert exact session/child isolation and fresh reads.
- A settings manager test wires the production callback through `relayWiring` and asserts saved fields remain intact on a detector error.
- Run touched-package race tests, `go vet ./...`, and `go build ./cmd/pyry`.

## Open questions

- Whether live child PATH can be captured without widening `setStdin`'s call sites. Resolve by adding an adjacent generation-bound snapshot at the spawn site; if that cannot be atomic, record the revision before implementation.

## Documentation handoff

Pending documentation stage: update `docs/knowledge/features/memorysearch-package.md` § Effective evidence and `docs/knowledge/features/v2-session-manager-state-machine-inbound-request-session-settings-the-rea.md` § inbound `request_session_settings` with the production provider, fresh bound-child resolution, and present `unknown` versus omitted results. The wire shape remains #2692's.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] The network's conversation ID is only a registry lookup key in `memorySearchFor`; the registry's session ID must match the settings resolver's ID before pool or child evidence is trusted. Child launch fields are checked by `Detect`.
- [Tokens and credentials] No token is read or returned. Codex home is taken from the daemon-owned runner configuration and used only for scope equality; it is never logged or sent on the wire.
- [File operations] `config.Load` reads the fixed Pyrycode configuration path. No client value forms that path or creates a file. `Detect` canonicalizes the effective workspace and uses bounded metadata checks for child PATH candidates.
- [Subprocesses] The provider starts no process and never runs a discovered CLI. A CLI sighting alone cannot assert access.
- [Cryptography] No cryptographic operation or key is added; the existing authenticated relay dispatch gates this read.
- [Network and I/O] The provider consumes the already bounded settings frame in `handleRequestSessionSettings`; child query has a timeout. No new listener or unbounded network read is added.
- [Errors and logs] Refusals are content-free `unknown`; no IDs, paths, configuration bytes, or child output are logged or echoed.
- [Concurrency] Registry, pool, and child are revalidated after I/O. Child generation detects replacement. Existing runner mutexes are used only for snapshots; no new goroutine or lock nesting is introduced.
- [Threat model] The relevant relay risk is cross-conversation disclosure. Exact registry binding, session identity, and child generation checks prevent a reply from borrowing another conversation's evidence.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-27

## Revisions

- The child PATH question resolved by freezing Claude's spawn environment before `cmd.Start` and publishing its PATH beside the stdin binding. Codex's app-server starts from `os.Environ` inside `codexsup.Start`, which does not expose that exact environment afterward. The Codex launcher therefore reports PATH as incomplete instead of treating a later daemon environment read as child evidence. Its active client, work directory, daemon-owned home, and replacement generation still establish the agent and launch scope for declarations.
- 2026-09-28 verifier review found that production Claude sessions store `streamRunner`, not `*streamsup.Runner`, in the pool. `streamRunner` now forwards `MemorySearchLaunch`; a compile-time capability assertion and a bound provider test using `newStreamRunnerFactory` cover that adapter boundary.
- 2026-09-28 verifier gate reproduced settings replies timing out when an eligible child stayed silent on `QueryMCPStatus`. The provider now bounds that optional query to 500 ms on the request context. Timeout leaves MCP evidence incomplete, so `Detect` can still report an independently confirmed provider while other reads return `unknown` promptly. A settings-manager test with a silent bound child covers the reply deadline and preserved ordinary fields.
