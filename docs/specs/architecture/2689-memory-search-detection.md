# Memory search detection for an agent workspace (#2689)

## Files read

- `internal/config/config.go` → `Config`, `Load`: additive JSON schema and parse-only overlay.
- `internal/turnevent/event.go` → `MCPStatus`, `MCPServerStatus`: bounded live server inventory; names and status are untrusted text.
- `internal/protocol/interactive.go` → `MCPServerStatus`: the corresponding public status shape; detection uses the internal event shape before a wire mapper.
- `internal/streamsup/runner.go` → `mcpStatusEligible`: strict Claude launches exclude personal and project MCP registrations.
- `cmd/pyry/codex_home.go` → `codexHomePath`: Codex uses the daemon-owned home.
- `cmd/pyry/mcp_config.go` → `permissionArgs`: the effective strict MCP config is daemon supplied.
- `docs/knowledge/features/config-package.md` → `Load` semantics: schema fields decode without validation; missing file defaults and read errors stay distinct.
- `docs/knowledge/features/development-verification.md` → source checks and focused proofs.

## Context

Clients need a search-access result for an agent/workspace before #2690 publishes it. A host installation alone does not establish that the selected child can search. This package reports search capability only; it does not inspect saved conversations or capture knowledge. The documentation stage should describe this new package and the declaration semantics; no ADR is needed for this additive detector.

## Design

Add `MemorySearchProviders []MemorySearchProvider` to `config.Config`. A declaration has `id`, `display_name`, `agent`, `workspace`, and `enabled`; `enabled` is a pointer so missing and explicit false are distinguishable. The config loader continues to parse only. The detector validates applicable declarations: nonempty stable ID and display name, supported agent, canonical absolute workspace, explicit enabled. Invalid or unreadable applicable config is unresolved evidence.

`internal/memorysearch` exports `Detect(Input) Result`. `Input` carries selected `Agent` and canonical `Workspace`, decoded `config.Config` and a config-read error, and an `Evidence` snapshot from that selected launch: complete/incomplete effective plugin inventory, selected child's PATH, optional host CLI sightings, complete/incomplete live MCP status, and effective MCP scope. It also carries the Codex home used by the launch; an absent home makes Codex plugin/MCP evidence untrusted. The detector never reads an agent's personal home, starts a child, runs a CLI, or searches the daemon's PATH. It only checks executable bits on candidate files in the supplied child PATH, with a bounded number of directories. Host CLI sightings never establish availability.

Built-in identities are `memsearch`, `qmd`, and `smart-connections`. Match only explicit provider IDs in the effective plugin inventory and exact recognised search server names in live MCP status. `memsearch` uses its enabled plugin or CLI; `qmd` uses its CLI or effective MCP; `smart-connections` uses its search bridge or a scoped declaration. Do not infer a provider from generic `memory` strings, unrelated tool names, or an Obsidian plugin alone. MCP status `connected` confirms availability; known disabled or failed states retain an installed, unavailable provider. Unknown status remains unresolved. A custom declaration is matched only by the selected agent and exact canonical workspace; enabled true confirms search, false records disabled installation. Aggregate precedence is available > unknown if any applicable check is unresolved > unavailable for installed but disabled/failed providers > absent after complete negative checks. Results are sorted by stable ID.

The selected launch owns evidence provenance. Callers must supply inventory after Claude's strict MCP filtering and from the daemon-owned Codex home. The detector rejects contradictory scope metadata and otherwise leaves incomplete sources unknown. The API does not silently replace missing launch evidence with host observations.

## Concurrency model

Detection is synchronous and read-only. It starts no goroutine and holds no shared state. The caller snapshots live MCP status before calling it.

## Error handling

Invalid selected agent or workspace returns an error. Filesystem failures during an applicable CLI probe and unreadable config become unresolved evidence, not confirmed absence. Missing live status is unresolved. A confirmed available provider wins over unresolved checks; disabled/failed providers remain in the result. No raw MCP error prose is copied into results or logs.

## Testing strategy

- Table-driven fixtures for memsearch plugin/CLI, QMD CLI/MCP, Smart Connections bridge/declaration, and custom declaration parsing.
- Disabled and failed installed providers, host-only CLI, unreadable evidence, and complete absence pin aggregation.
- Cross-agent, cross-workspace, strict Claude MCP, and Codex-home fixtures pin isolation.
- Temp executable files prove child PATH probing without running binaries.

## Open questions

- Confirm the exact MCP status success/failure vocabulary and the precise search bridge names from existing captured status fixtures before implementation. Record any resulting contract change in Revisions.

## Documentation handoff

Pending documentation stage: `docs/knowledge/features/memorysearch-package.md` must document declaration fields, evidence precedence, and the meanings of absent and unknown. #2690 owns `docs/protocol-mobile.md` client wire contract.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] The caller supplies launch-scoped evidence; `Detect` validates agent/workspace and refuses to promote incomplete or contradictory scope to availability. MCP strings remain untrusted observations and only exact known names classify built-ins.
- [Tokens and cryptography] No tokens or cryptographic operations enter this read-only detector.
- [File operations] Child PATH is bounded and only candidate executable metadata is read. No user-provided ID becomes a filesystem path, and no files are written. An unreadable candidate makes the check unknown.
- [Subprocesses] No subprocess is executed. PATH evidence uses the selected child's supplied PATH, never daemon `exec.LookPath`.
- [Network and I/O] No network read occurs. Input slices and PATH scanning are bounded; the live MCP snapshot is already bounded by `MCPStatus`.
- [Errors and telemetry] Raw MCP error prose and arbitrary config paths are not returned or logged. The result includes only stable provider identity and state.
- [Concurrency] Synchronous snapshot processing has no locks, goroutines, or shutdown path.
- [Threat model alignment] #2690 owns relay exposure and wire rendering. This detector does not accept a remote command or perform actuation.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-27
