# `internal/config` — typed schema + overlay loader

User-configurable values for pyry, loaded from `~/.pyry/config.json`. Foundation slice for Phase 3 (mobile + relay) work — the first field is the relay URL needed by `pyry pair`. Future fields land additively in the same struct.

This package is leaf-level: stdlib only, no consumers wired in this slice. Daemon startup and `pyry pair` wire `Load` from their own tickets.

## Surface

```go
type Config struct {
    RelayURL string `json:"relay_url"`
}

func DefaultConfig() Config        // built-in defaults
func Load(path string) (Config, error)
```

Three exports total. No `Save`, no `Watch`, no `ErrConfigMissing` sentinel — read-only this slice. If a future ticket needs writes, it lands then (compare `internal/sessions/registry.go`, where `loadRegistry` shipped without `saveRegistry`).

The default is built into the function body (not a package-level `const`) so callers don't reach for "the current value" through a separate symbol; when more fields land, the constructor grows naturally to a multi-line struct literal.

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
