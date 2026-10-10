# `internal/config` — typed schema, overlay loader and atomic memory updates

User-configurable values for pyry, loaded from `~/.pyry/config.json`. Fields land additively in the same struct.

This package is leaf-level and uses only the standard library. `Load` parses the file
and `UpdateMemory` persists managed memory settings; consumers own semantic validation.

## Surface

```go
type Config struct {
    RelayURL              string                 `json:"relay_url"`
    MemorySearchProviders []MemorySearchProvider `json:"memory_search_providers"`
    Memory                *MemorySettings        `json:"memory,omitempty"`
    DebugCapture          bool                   `json:"debug_capture"`
    InteractiveRunner     string                 `json:"interactive_runner"`
    StdioPermissionPrompt bool                   `json:"stdio_permission_prompt"`
}

func DefaultConfig() Config        // built-in defaults
func Load(path string) (Config, error)
func UpdateMemory(path string, settings MemorySettings) error
```

`MemorySearchProvider` carries `ID`, `DisplayName`, `Agent`, `Workspace`, and
`Enabled *bool`. The pointer distinguishes a missing `enabled` field from
explicit `false`. See [memory search detection](memorysearch-package.md#declarations)
for the field contract and scope. `UpdateMemory` writes only managed memory settings;
there is no general `Save` or `Watch` API.

### Managed memory shape

`Config.Memory` holds the additive `memory` object independently of
`memory_search_providers`, which describe search access. Neither configures the other.

| Type | JSON fields | Caller-validated meaning |
|------|-------------|--------------------------|
| `MemorySettings` | `vault`, optional `additional_roots` (`[]string`), `embedding`, `capture` | Complete managed memory settings |
| `MemoryVault` | `mode`, optional `path` | `default` or `separate`; a separate vault uses a caller-resolved path |
| `MemoryEmbedding` | `provider`, `model`, optional `credential_reference` | `local` or `openai`, with an independently chosen embedding model |
| `MemoryCapture` | `agent`, `model` | `claude` or `codex`, with an independently chosen capture model |

The scalar fields are strings. Embedding provider and capture agent are independent:
OpenAI embedding can pair with Claude capture, and local embedding with Codex capture.
`credential_reference` is opaque, non-secret metadata from the
[memory credential lifecycle (#3112)](https://github.com/pyrycode/pyrycode/issues/3112).
Tokens and credential backend-selection metadata belong to that protected lifecycle.

The local semantic boundary lives in
[`cmd/pyry/memory_config.go`](../../../cmd/pyry/memory_config.go):
`configureMemory(ctx, settings)` validates a complete replacement before calling
`UpdateMemory`, and `memoryStatus(ctx)` validates saved choices and projects safe
JSON. `runMemoryConfiguration` exposes these operations through offline
`pyry memory configure` / `pyry memory status`; wizard callers can use the same
operations without a daemon. `memoryHome` requires an existing absolute home and
uses its `.pyry/config.json`, never `resolveConfigPath`'s cwd fallback. See the
[user-facing flags and status shape](../../guide.md#memory-configuration).

[`cmd/pyry/memory_roots.go`](../../../cmd/pyry/memory_roots.go) owns
`validateMemorySettings` and `resolveEffectiveMemory(ctx, settings, startupWorkspaceBase)`.
The latter returns separate `VaultPath`, read-only `AdditionalRoots`, daemon-owned
`TranscriptPath` and normalized `SearchRoots`; indexing overlap does not change
write destinations or ownership. Its explicit base must come from
`resolveStartupWorkspaceBase`, independently of the seeded `default` channel or
hosted-session cwd. These callable operations add no daemon startup wiring.

## `debug_capture` — rejected at startup since #1514

`debug_capture` (#802) used to switch on a `.cast` recorder attached to the terminal spawn path. #1348 deleted that path and the recorder with it, but the config key and its plumbing (a `SessionConfig.RecordDir` field nothing read) survived as a silent no-op: an operator who set the flag, reproduced a bug and pulled a debug bundle got nothing recorded, with no error or log line.

`Load` still decodes the field verbatim and does not validate it — `config` stays parse-only, and `internal/config/config_test.go`'s three `debug_capture` cases are unchanged. Validation moved to the composition root instead: `cmd/pyry/main.go`'s `checkDebugCapture` runs in `runSupervisor` immediately after `config.Load` succeeds, before the pool, the runner factory or the control socket exist. `true` returns an error naming `debug_capture`, saying #1348 removed the recorder, and telling the operator to remove the key or set it to `false`; the daemon exits non-zero before any session or socket is built. `false` and an absent key return `nil` and log nothing — startup is byte-identical to before this ticket. This is the same shape as the `"pty"` rejection arm below, one section down.

The debug-bundle **read** path is untouched: `resolveRecordingsDir` and its independent feed into `debugbundle.Assemble` still resolve `~/.local/share/pyry-recordings`, so a `.cast` left over from before an operator upgraded still ships in a bundle. See [debugbundle-package.md](debugbundle-package.md).

## `interactive_runner` — stream-json selection, `"pty"` rejected (#1081, corrected #2555 for #1348 fallout)

Selects which interactive runner the daemon builds. `Load` decodes it verbatim and does **not** validate it — same posture as `DebugCapture`, no `DefaultConfig` entry, so an absent field decodes to `""`. Enum validation happens at the composition root (`cmd/pyry/main.go`'s `selectInteractiveRunner`), not here, because the accepted set maps to a runner factory the leaf `config` package cannot import (`cmd/pyry`'s `streamsup` wiring).

| Value | Effect |
|-------|--------|
| absent / `"stream-json"` | The streamsup-backed runner (`internal/streamsup`), wired live end-to-end: `newStreamRunnerFactory` (#1109), wrapped in `harnessRunnerFactory` (#2593) so the pool selects by each session's recorded harness, selected as `sessions.Config.RunnerFactory`, and its turn events drained through the interactive turn stream (`startStreamTurnDrainV2`, #1098) so a relay client following the active conversation receives `turn_state`/`assistant_delta`/tool events. This has been the only reachable startup outcome since #1348 deleted the terminal-driven PTY supervisor. A session recorded with any harness but `claude` is refused before `newStreamRunnerFactory` is even called — see [sessions-package.md § Runner + RunnerFactory](sessions-package-key-types-runner-interface-runnerfactory.md). |
| `"pty"` | Daemon startup **aborts** with an error naming the #1348 removal and telling the operator to remove the key or set it to `"stream-json"`. There is no PTY supervisor left to fall back to. |
| anything else | Daemon startup **aborts** with an error naming the offending value and the accepted set (`interactive_runner %q not recognized (accepted: "", "stream-json")`) — no silent fallback. |

There is no rollback: `internal/supervisor` and the terminal-driven runner it backed were deleted outright in #1348, so no value of this field can select them again.

See [streamsup-package.md](streamsup-package.md) for the runner itself and [`codebase/1081.md`](../codebase/1081.md) for the composition-root and relay-leg wiring this field drove before #1348 (superseded, but the stream-json half is unchanged).

The `case "pty"` (rejected-value) arm here is the pattern later reused for the CLI verbs #1348 deleted outright (`attach`/`acp`) — see [cli-verb-dispatch.md](cli-verb-dispatch.md).

The default is built into the function body (not a package-level `const`) so callers don't reach for "the current value" through a separate symbol; when more fields land, the constructor grows naturally to a multi-line struct literal.

## `stdio_permission_prompt` — interactive permission transport (#2343)

This boolean selects the Claude-facing permission transport for the daemon's
non-bypass interactive stream children. It does not change the approval registry,
the client-facing modal/question flow, or the permission policy.

| Value | Effect |
|-------|--------|
| absent / `false` | Default and rollback posture. Interactive permission asks use `mcp__pyry_approve__approve`, preserving the established MCP approval path. |
| `true` | Interactive permission asks use Claude's stdio `can_use_tool` / `control_response` protocol. The spawn still carries `--mcp-config` and `--strict-mcp-config` because the same config also registers `pyry_files`, and it retains the selected permission-mode flag. |

`runSupervisor` reads the config once, so changing this value takes effect only
after a daemon restart. Set it to `false` and restart to roll back to MCP.

The selector applies only to non-bypass interactive daemon spawns. YOLO continues
to use `--dangerously-skip-permissions` in either position, and `pyry agent-run`
keeps its independent argv construction unchanged.

The opt-in is backed by the [Claude Code 2.1.259 compatibility result](permission-protocol-spike.md#current-finding-claude-code-21259-2342):
the live gate observed a correlated stdio permission request, returned a matching
deny response, and proved that the requested command did not execute.

## Defaults

| Field | Default |
|-------|---------|
| `RelayURL` | `wss://relay.pyrycode.dev` (placeholder; real domain TBD) |
| `Memory` | `nil` (managed memory unconfigured) |

`DefaultConfig` enables no managed memory. Legacy files without `memory` and files
with `"memory": null` retain that unconfigured state. In a present memory object,
missing `additional_roots` decodes to a nil slice, meaning zero additional roots;
no vault mode, provider, agent or model default is filled in.

When the real relay is provisioned, that ticket changes the constant. Existing users with no `~/.pyry/config.json` pick up the new default automatically on the next daemon start; users who pinned a value in their config file are unaffected (overlay-decode preserves their explicit setting).

## Load semantics

| Condition | Returns |
|-----------|---------|
| File doesn't exist | `DefaultConfig(), nil` |
| File exists, valid JSON `{...}` | merged config, `nil` |
| File exists, valid JSON `{}` | `DefaultConfig(), nil` (overlay no-op) |
| File exists, malformed JSON | `Config{}, fmt.Errorf("config: parse %s: %w", ...)` |
| File exists, empty (0 bytes) | `Config{}, fmt.Errorf("config: parse %s: %w", ...)` |
| File exists, read fails (perms etc.) | `Config{}, fmt.Errorf("config: read %s: %w", ...)` |

The load-bearing trick: `cfg` is initialized to `DefaultConfig()` *before* `json.Unmarshal`. `encoding/json` only writes fields present in the JSON document — absent fields keep their pre-decode value. So `{}` returns `DefaultConfig()`, and `{"relay_url": "wss://my-relay.example/"}` returns defaults with only `RelayURL` overridden. See [ADR 018](../decisions/018-config-overlay-decode.md).

The zero `Config{}` on the error paths is deliberate: callers who ignore the error see an empty `RelayURL` rather than the placeholder default, forcing them to handle the error. Returning `DefaultConfig()` on error would mask real problems.

Empty file (0 bytes) is **not** treated as "fresh install" — that signal is "no file exists at all." An empty file is operator error and falls out as a wrapped JSON-parse error naturally. (The asymmetry vs. `loadRegistry`, which treats empty-as-missing, is correct: the registry is pyry-owned, the config is user-owned.)

Wrap prefix is `config:` — matches the convention `loadRegistry` established with `registry:`.

`Load` and `UpdateMemory` remain parse-only. Unsupported mode/provider/agent strings,
blank models, unresolved paths and unverified credential references are accepted
without host, credential or service checks. Consumers validate choices, model names,
host paths and references before using them. `Load` ignores unknown fields in the typed
result; [memory replacement](#memory-replacement-and-persistence) preserves their JSON
values on disk. A present `"memory": {}` produces a non-nil `MemorySettings` with zero
fields, so presence alone does not establish usable settings.

`configureMemory`, `memoryStatus` and `resolveEffectiveMemory` apply
`validateMemorySettings`: accepted vault/provider/agent enums, nonblank independent
models, canonical directory access and reserved-path exclusions, plus fresh
`resolveMemoryCredential` validation for OpenAI. Local embeddings reject a
reference and require no credential selection. Status rejects malformed/non-object
config and invalid saved settings with static diagnostics; it does not expose
`Load`'s parser details or serialize `credential_reference`. Missing/null memory
reports unconfigured, and empty additional roots project as `[]`.

A default vault stores only its mode. Configure and status defer the default
vault's directory and reserved-path checks; status leaves its path unresolved.
`resolveEffectiveMemory` selects the explicitly supplied daemon startup base for
default mode and the saved canonical path for separate mode, then revalidates
vault write/traverse access and exclusions in either mode. Reserved transcripts
and credentials are disjoint even when redirected through symlinks; transcript
storage is derived through existing ancestors and may remain absent. A rejected
resolution returns a zero `effectiveMemory` and never changes saved settings.
Resolution is a snapshot, so runtime consumers must revalidate before use.
See [reserved-root and filesystem-identity reasoning](cli-verb-dispatch.md#memory-configuration-and-effective-roots).
Saving settings enables no runtime, and future daemon application requires restart;
configured status supplies no [search-readiness evidence](memorysearch-package.md#effective-evidence).

## Memory replacement and persistence

`UpdateMemory(path, settings)` accepts an explicit config-file path and a complete
`MemorySettings` replacement. It creates a missing config and replaces every known
memory field on later calls while preserving unrelated top-level settings, including
search-provider declarations, and unknown values inside `memory`, `vault`, `embedding`
and `capture`.

| Replacement input | Persisted effect |
|-------------------|------------------|
| Empty `Vault.Path` | Deletes the old `vault.path` key |
| Empty `Embedding.CredentialReference` | Deletes the old `embedding.credential_reference` key |
| Nil `AdditionalRoots` | Deletes the old `additional_roots` key |
| Non-nil `AdditionalRoots`, including `[]string{}` | Replaces the old array; an empty slice writes `[]` |

Thus switching separate to default or OpenAI to local clears stale path/reference
keys when the caller omits those optional values. Supplying them retains them even
with those choices: the writer does not infer semantic validity from the selected mode
or provider. Zero-valued settings still write a memory object; this API does not remove
the entire `memory` key.

Unknown values are retained as `json.RawMessage`, preserving large integers, precise
fractions and exponents beyond float64 range. Decoding them through `any` would round
numbers or fail on overflow. Whitespace and key order may change during encoding;
preservation concerns JSON values, not the original formatting.

Absent/null `memory`, `vault`, `embedding` and `capture` objects may be populated.
An existing config must be a non-null JSON object: empty, malformed and non-object
documents are rejected unchanged. Non-null non-object values at any of those four
memory-object locations are also rejected unchanged. These structural checks apply
to `UpdateMemory`; `Load` retains its overlay-decode and error behavior.

The writer finishes encoding before filesystem mutation, creates newly needed parent
directories at 0700, and stages a 0600 temporary file in the destination directory.
It writes, syncs, closes and atomically renames that file over the config. Committed
files are 0600, including replacements of files with broader permissions; existing
parent-directory modes are left as they are. Read, parse/structure, encode, mkdir,
create, chmod, write (including short write), sync, close and rename failures return
errors and preserve the previous config bytes, or leave a missing config absent.
Cleanup closes any remaining handle and removes staging, surfacing cleanup errors too.
The operation writes no vault contents or credential storage and starts no service.

## Out of scope (deferred to follow-up tickets)

- **Path resolution.** `Load` takes an explicit `path` string. `cmd/pyry` callers own location policy: `resolveConfigPath` retains its fallback, while the local memory operations require `memoryHome` and refuse a cwd-relative fallback. Effective vault resolution also belongs to the caller, with an explicit daemon startup base. Keeping these choices outside `config` avoids binding the leaf package to one home or workspace policy.
- **General `Save`.** `UpdateMemory` provides atomic replacement for managed memory settings. Writing other config fields remains outside this API.
- **Watcher / hot reload.** Daemon reads once at startup. If hot reload becomes necessary, that's a separate seam (file watcher, signal handler) — most likely a `sync/atomic.Pointer[Config]` swapped on file events. Not bolted onto `Load`.
- **Schema versioning.** Per the ticket: "if `Config` ever grows incompatibly, version it at that point." `encoding/json`'s default lenient handling (unknown JSON fields ignored, missing struct fields → zero) covers backward-additive changes for free.
- **URL validation.** `RelayURL` is a `string`. Validation (scheme, parseability) is the consumer's job — `pyry pair` will validate before connecting. The config package's contract is "decode JSON into a struct"; semantic validation layers above.

## Concurrency

`Load` and `UpdateMemory` are synchronous, with no goroutines, shared state or locks.
Callers must serialize the entire read/modify/write operation for one config path,
including updates from other processes. Atomic rename gives readers complete file
snapshots; it does not prevent concurrent writers from losing each other's updates.
The daemon reads config once at startup.

## Tests

`internal/config/config_test.go` uses same-package, table-driven fixtures:

- `TestDefaultConfig` — pins `DefaultConfig().RelayURL == "wss://relay.pyrycode.dev"`. Fails loudly when the real relay domain lands — that's the right signal.
- `TestLoad` (table) — missing file → defaults; valid full file → override; partial file `{}` → defaults preserved (regression guard for the overlay property when more fields land); malformed JSON → wrapped error containing `"config: parse"`.
- `TestLoadMemorySearchProviders` — decodes a scoped declaration and preserves an explicit `enabled: false`.

Each row writes its fixture to `t.TempDir()` (no checked-in golden files).
`Config` contains a slice and is compared with `reflect.DeepEqual`.

`internal/config/memory_test.go` covers the managed memory contract:

- `TestLoadMemory` — legacy/null unconfigured state, no managed-memory defaults,
  both vault modes, independent embedding/capture choices and parse-only decoding.
- `TestUpdateMemoryReplacement` — repeated complete replacements, unrelated and
  nested unknown values, exact numbers, optional-key deletion and array replacement.
- `TestUpdateMemoryObjects` and `TestUpdateMemoryRejectsStructure` — populate
  absent/null objects and reject invalid documents/object shapes without modification.
- `TestUpdateMemoryFreshProcess` — file/new-parent modes, replacement permissions
  and a fresh test process loading the committed settings.
- `TestUpdateMemoryFailures` and `TestUpdateMemoryReadAndDirectoryErrors` — injected
  staging failures, exact previous bytes or continued absence, cleanup and retry,
  plus read and parent-directory errors.

Replacement tests inspect raw key presence as well as decoded settings. A check of
only the decoded roots' length would pass both an omitted key and a supplied empty
array, missing a writer that persisted the wrong form. Numeric preservation checks
compact raw JSON rather than decoding through float64, which would hide the precision
loss the test is meant to catch.

## Related

- [ADR 018](../decisions/018-config-overlay-decode.md) — overlay-decode over two-pass merge or pointer-field distinguishing absent-vs-empty
- [`sessions-registry.md`](sessions-registry.md) — sibling on-disk JSON file (pyry-owned, atomic-rename writes)
- `loadRegistry` in `internal/sessions/registry.go` — the reference implementation for the missing-file / wrap shape
- [`streamsup-package.md`](streamsup-package.md) — the `"stream-json"` runner `interactive_runner` selects
- [`codebase/1081.md`](../codebase/1081.md) — the composition-root selector + relay-leg stream-mode wiring this field drives
