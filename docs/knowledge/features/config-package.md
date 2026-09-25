# `internal/config` — typed schema + overlay loader

User-configurable values for pyry, loaded from `~/.pyry/config.json`. Foundation slice for Phase 3 (mobile + relay) work — the first field is the relay URL needed by `pyry pair`. Future fields land additively in the same struct.

This package is leaf-level: stdlib only, no consumers wired in this slice. Daemon startup and `pyry pair` wire `Load` from their own tickets.

## Surface

```go
type Config struct {
    RelayURL              string `json:"relay_url"`
    DebugCapture          bool   `json:"debug_capture"`
    InteractiveRunner     string `json:"interactive_runner"`
    StdioPermissionPrompt bool  `json:"stdio_permission_prompt"`
}

func DefaultConfig() Config        // built-in defaults
func Load(path string) (Config, error)
```

Three exports total. No `Save`, no `Watch`, no `ErrConfigMissing` sentinel — read-only this slice. If a future ticket needs writes, it lands then (compare `internal/sessions/registry.go`, where `loadRegistry` shipped without `saveRegistry`).

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

## Out of scope (deferred to follow-up tickets)

- **Path resolution.** `Load` takes a `path` string. The "where does `~/.pyry/config.json` live" question is the caller's; the daemon-startup consumer ticket will add a `resolveConfigPath()` helper alongside `resolveSocketPath` / `resolveRegistryPath` in `cmd/pyry/main.go`. Doing it here would bind the package to `os.UserHomeDir` semantics that some future caller (e.g. a `--config` override) wants to compose differently.
- **`Save`.** Read-only. Future ticket adds the atomic-rename write primitive if/when needed.
- **Watcher / hot reload.** Daemon reads once at startup. If hot reload becomes necessary, that's a separate seam (file watcher, signal handler) — most likely a `sync/atomic.Pointer[Config]` swapped on file events. Not bolted onto `Load`.
- **Schema versioning.** Per the ticket: "if `Config` ever grows incompatibly, version it at that point." `encoding/json`'s default lenient handling (unknown JSON fields ignored, missing struct fields → zero) covers backward-additive changes for free.
- **URL validation.** `RelayURL` is a `string`. Validation (scheme, parseability) is the consumer's job — `pyry pair` will validate before connecting. The config package's contract is "decode JSON into a struct"; semantic validation layers above.

## Concurrency

None. `Load` is one synchronous `os.ReadFile` + one `json.Unmarshal`. No goroutines, no shared state, no mutexes; race-detector clean by construction. The daemon will call `Load` once at startup, before any goroutines spawn.

## Tests

`internal/config/config_test.go`, same-package, table-driven. Five cases mapped 1:1 onto the AC enumeration:

- `TestDefaultConfig` — pins `DefaultConfig().RelayURL == "wss://relay.pyrycode.dev"`. Fails loudly when the real relay domain lands — that's the right signal.
- `TestLoad` (table) — missing file → defaults; valid full file → override; partial file `{}` → defaults preserved (regression guard for the overlay property when more fields land); malformed JSON → wrapped error containing `"config: parse"`.

Each row writes its fixture to `t.TempDir()` (no checked-in golden files). `Config` is a small comparable struct → direct `==` works, no `reflect.DeepEqual` needed.

## Related

- [ADR 018](../decisions/018-config-overlay-decode.md) — overlay-decode over two-pass merge or pointer-field distinguishing absent-vs-empty
- [`sessions-registry.md`](sessions-registry.md) — sibling on-disk JSON file (pyry-owned, atomic-rename writes)
- `internal/sessions/registry.go:31-51` — `loadRegistry`, the reference implementation for the missing-file / wrap shape
- [`streamsup-package.md`](streamsup-package.md) — the `"stream-json"` runner `interactive_runner` selects
- [`codebase/1081.md`](../codebase/1081.md) — the composition-root selector + relay-leg stream-mode wiring this field drives
