# #2670 — capability list says whether slash commands, MCP servers and context-usage detail exist

## Files read

- `internal/protocol/settings.go` → `SessionCapabilities` — the wire object; no omitempty, every key always present.
- `internal/relay/v2session_seams.go` → `AgentCapabilities`, `V2SessionConfig.CapabilitiesFor` — the agent half the daemon supplies.
- `internal/relay/v2session_settings.go` → `sessionCapabilities` — composes the agent half into the wire object.
- `cmd/pyry/main.go` → `settingsUpdaterAdapter.Capabilities` — decides the agent half per harness; `mcpStatusQuerier`, `resolveBoundMCPStatus` — the MCP resolver's type assertion.
- `cmd/pyry/session_slash_command_list.go` → `resolveBoundSlashCommandList` — asserts the runner against an inline anonymous interface.
- `cmd/pyry/relay_context_usage.go` → `contextUsageQuerier`, `contextUsageResolve` — the context-usage resolver's type assertion.
- `cmd/pyry/streamsup_runner.go` → `streamRunner` (the Claude runner; value receiver `SlashCommandList`, `QueryMCPStatus`, `QueryContextUsage`); `cmd/pyry/codex_runner.go` → `codexRunner` (implements none of them).
- `cmd/pyry/codex_claude_only_test.go` → `TestCodexSession_ClaudeOnlyFeaturesUnavailable` — #2586's proof that each resolver refuses a Codex session.
- Tests to extend: `cmd/pyry/session_capabilities_test.go`, `internal/relay/v2session_capabilities_test.go` (`fixtureAgentCaps`, `wantFixtureCapObj`, the empty-capabilities key check), `internal/protocol/settings_test.go` (the capabilities marshal check).

## Change

Add three booleans to both `protocol.SessionCapabilities` (`slash_commands`, `mcp_servers`, `context_usage_detail`, no omitempty, placed after `mid_turn_input` beside the other booleans) and `relay.AgentCapabilities` (`SlashCommands`, `MCPServers`, `ContextUsageDetail`). `sessionCapabilities` copies them through. `settingsUpdaterAdapter.Capabilities` sets all three to `harness == sessions.HarnessClaude`, beside `Interrupt`; an unknown harness already returns false (no object), and a Codex session gets all three false.

To make the pin real, the slash-command resolver's inline interface gets a name, `slashCommandLister`, in `session_slash_command_list.go`, so the test asserts against the exact type `resolveBoundSlashCommandList` does, as it already can for `mcpStatusQuerier` and `contextUsageQuerier`. Behaviour of that resolver is unchanged.

An older client never receives the `capabilities` key (#2646's gate in `handleRequestSessionSettings`), so its reply stays byte-identical; the existing `TestV2Session_RequestSessionSettings_CapabilitiesForMultiAgentOnly` old-client arm already proves it and needs no change beyond the fixture.

## Testing strategy

- `cmd/pyry/session_capabilities_test.go`: new `TestSettingsUpdaterAdapter_CapabilityFlagsMatchResolvers` — for Claude (`streamRunner{}`) and Codex (`&codexRunner{}`), each flag from `Capabilities` equals whether that harness's runner passes the matching resolver assertion (`slashCommandLister`, `mcpStatusQuerier`, `contextUsageQuerier`), and the test also states the expected direction (Claude passes, Codex fails). Existing Claude rows in `TestSettingsUpdaterAdapter_Capabilities` gain the three trues.
- `internal/relay/v2session_capabilities_test.go`: `fixtureAgentCaps` sets the three true and `wantFixtureCapObj` expects them (proves the copy through `sessionCapabilities` and onto the wire for a multi_agent conn); the empty-capabilities key check adds `"slash_commands":false`, `"mcp_servers":false`, `"context_usage_detail":false` (always present when false).
- `internal/protocol/settings_test.go`: the marshal string includes the three keys.

## Documentation handoff

Pending for the documentation stage: `docs/protocol-mobile.md` § `capabilities` (multi_agent, #2646) — add rows for `slash_commands`, `mcp_servers`, `context_usage_detail` (bool, always present; true for Claude, false for Codex), update the example object, and add a changelog entry.
