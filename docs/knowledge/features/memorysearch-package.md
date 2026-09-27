# `internal/memorysearch` — agent workspace search access

`Detect(Input)` reports whether a selected agent can use memory search in one
canonical workspace. It describes search access, not saved conversations or
knowledge capture. The package is a read-only detector and result model. The
client wire contract is defined in
[the mobile protocol](../../protocol-mobile.md#memory_search-2692). The relay
publishes a supplied report through `MemorySearchFor` (#2693); #2694 wires the
live daemon provider.

## Declarations

`~/.pyry/config.json` accepts `memory_search_providers`, an array of objects:

```json
{
  "memory_search_providers": [
    {
      "id": "local-search",
      "display_name": "Local Search",
      "agent": "claude",
      "workspace": "/absolute/canonical/workspace",
      "enabled": true
    }
  ]
}
```

| Field | Meaning |
| --- | --- |
| `id` | Stable provider identity: 1–64 characters, starting with a lowercase ASCII letter, then lowercase ASCII letters, digits, `-`, `_`, or `.`. Built-ins use `memsearch`, `qmd`, and `smart-connections`. |
| `display_name` | Nonblank user-facing name, at most 128 bytes, without control characters. |
| `agent` | Exactly `claude` or `codex`. |
| `workspace` | Canonical absolute path for the selected workspace. |
| `enabled` | Required boolean. `true` asserts installed and usable search in this agent/workspace; `false` records an installed but disabled provider. Missing is unresolved, not `false`. |

`config.Load` decodes declarations without semantic validation. `Detect` compares
agent and workspace before validating a matching declaration. A stale path in
another workspace's declaration cannot make this workspace's result unknown.
Malformed or unreadable config is unresolved evidence. See
[the config package](config-package.md#load-semantics).

## Effective evidence

The caller supplies a snapshot of the **selected child launch**: effective
plugins, child `PATH`, host CLI sightings, live MCP status, and their completion
flags. Claude's strict MCP scope excludes user and project registrations;
Codex evidence must match its daemon-owned `CODEX_HOME`. Mismatched launch scope
cannot establish availability.

- Memsearch is recognized through an enabled `memsearch` plugin or accessible
  child CLI. QMD is recognized through an accessible child CLI or an effective
  MCP server named `qmd` or `qmd-mcp`. Smart Connections needs an effective
  `smart-connections` or `smart-connections-search` MCP bridge, or a scoped
  declaration. Obsidian's plugin alone and unrelated names containing
  `memory` provide no evidence.
- A host CLI sighting establishes installation only. An empty host CLI list
  establishes no absence unless `HostCLIsKnown` says the check completed.
  A child CLI proves access only when the child uses the daemon's credentials,
  the real and effective IDs match, and POSIX execute access succeeds. Execute
  mode bits alone can misclassify a file owned by the child.
- Live MCP `connected` confirms access; `failed`, `disconnected`, or `disabled`
  retain the installation as unavailable. Other statuses are unresolved.
  Missing or incomplete live status cannot prove absence.

Evidence for one provider is merged by stable ID: confirmed access wins, then
unresolved access, then confirmed unavailability. `Provider` retains the ID,
display name, installed and enabled flags, and its own availability. Results
are sorted by ID.

## Aggregate availability

| Value | Meaning |
| --- | --- |
| `available` | At least one provider is confirmed usable, even if another check is unresolved. |
| `unknown` | No provider is confirmed usable and some applicable evidence is missing, unreadable, uncheckable, or has an unrecognized state. A host-only CLI also remains here. |
| `unavailable` | Installed providers are confirmed disabled or failed, with no unresolved check and no usable provider. They remain in `Providers`, so clients can avoid an install prompt. |
| `absent` | Every applicable check completed and found no installed provider. An empty provider list with missing evidence is `unknown`, not `absent`. |

`Detect` does not start an agent, execute a CLI, index content, or change config.
It only inspects bounded launch evidence and CLI file metadata. The
[architecture spec](../../specs/architecture/2689-memory-search-detection.md)
records the evidence boundary.
