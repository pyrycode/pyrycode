# `pyry pair` — CLI device-pairing verb family

`pyry pair` is a one-shot CLI verb family that owns the operator-facing pairing surface. It mints, persists, and surfaces device tokens for the per-instance registry at `~/.pyry/<name>/devices.json`. Composes the Phase 3 foundation primitives (`internal/config`, `internal/identity`, `internal/devices`, `internal/pair`) — wiring only, no new types or packages.

The verbs run **without a daemon**: each reads/writes the on-disk state directly. No socket dial, no goroutines, no `context.Context`.

Lives in [`cmd/pyry/pair.go`](../../../cmd/pyry/pair.go). Dispatched from `cmd/pyry/main.go`'s `case "pair":` arm, alongside `version` / `attach` / `sessions` / `install-service` / `update`.

## Verb dispatcher

`runPair` peels `args[0]`. Sub-verbs implemented today:

| Verb | Action |
|------|--------|
| (bare) | Mint a token, persist a `Device`, render QR + paste payload to stdout |
| `list` | Print the registry as a tabular listing (read-only) |

Mirrors `runSessions`'s sub-router shape ([ADR 010](../decisions/010-sessions-cli-sub-router.md)) with one deviation: `runPair` does NOT call `parseClientFlags` — the pair family does not dial the daemon, so there is no `-pyry-socket` to peel. Per-verb flag-set parsers (`parsePairArgs`, `parsePairListArgs`) own `-pyry-name` directly.

`pairVerbList` is a single string constant updated in lockstep when verbs land (`#215` will append `revoke`). The dispatcher is deliberately ~10 lines, NOT factored into a generic helper — `runSessions` and `runPair` each carry their own switch by design (CLAUDE.md "Simplicity first"; the next sibling verb is one new case, not a refactor).

**Flags-vs-verb disambiguation.** `pyry pair --name=foo` has `--name=foo` as `args[0]`. The dispatcher must not treat that as an unknown verb. Real verbs are bare identifiers (no leading `-`); the `strings.HasPrefix(args[0], "-")` check routes flag-leading args to `runPairDefault` so the bare-pair flow keeps accepting `--name`/`--relay`. Unknown bare-verb tokens (`pyry pair revoke` before #215 lands) hit the usage-error path → exit 2.

## Surface

```
pyry pair [-pyry-name=<instance>] [--name <device-label>] [--relay <url>]
pyry pair list [-pyry-name=<instance>]
```

| Flag | Purpose | Default |
|------|---------|---------|
| `-pyry-name` | Instance scope (state dir: `~/.pyry/<name>/`) | `defaultName()` — `PYRY_NAME` env or literal `"pyry"` |
| `--name` | Device label persisted in the registry | `device-<short>` (first 8 hex chars of the token hash) |
| `--relay` | Override the relay URL printed in the payload (does NOT mutate config.json) | resolved from config, then built-in default |

`-pyry-name` is added because `devices.json` and `server-id` are per-instance — same scoping as `pyry sessions *` and the supervisor itself. A single-instance user gets one consistent state directory across every verb.

## `pyry pair` (bare) — operation order

Chosen so no failure path leaves a plaintext token in any context outside this process's RAM:

```
1. Parse flags                                           → exit 2 on parse error
2. config.Load(~/.pyry/config.json)                      → exit 1 on I/O / parse error
3. resolveRelay(flag, cfg) → relay URL                   → exit 2 if empty
4. devices.Load(~/.pyry/<name>/devices.json) → registry  → exit 1 on I/O error
5. identity.LoadOrCreate(~/.pyry/<name>/server-id)       → exit 1 on I/O error
6. crypto/rand.Read(32 bytes) → hex.EncodeToString       → exit 1 on rng error
7. devices.HashToken(plain) → 64-char hex SHA-256
8. deviceName := --name OR "device-" + hash[:8]
9. registry.Add(Device{TokenHash, Name, PairedAt: now.UTC()})
10. registry.Save(devicesPath)                           → exit 1 on I/O error
11. pair.Render(Payload{Server, Relay, Token: plain}, os.Stdout) → exit 1 on write error
```

Steps 2–5 run before any token is generated: a misconfigured relay or unreadable server-id file fails fast, before the random draw. Step 6 happens only on the path where 1–5 succeeded. Once Step 6 runs, the plaintext token exists only in this process's RAM until Step 11 writes it to stdout.

**Step 10 gates the rest.** If `registry.Save` fails, `Render` is skipped and the plaintext token never escapes the process. The user retries; a new (independent) token is minted on the next run; the orphaned in-memory device is dropped with the process. The reverse ordering (render first, save second) would be wrong: a post-render save failure would print a working pairing payload that the daemon would later reject because the token isn't in the registry. See [ADR 021](../decisions/021-pair-cli-order-of-operations.md).

## On-disk paths

| File | Path | Owner |
|------|------|-------|
| Config | `~/.pyry/config.json` | per-user (no instance interpolation) |
| Server-id | `~/.pyry/<sanitized-name>/server-id` | per-instance |
| Devices | `~/.pyry/<sanitized-name>/devices.json` | per-instance |

`resolveDevicesPath` and `resolveServerIDPath` MUST call `sanitizeName(name)` before joining — defends against `PYRY_NAME=../../etc` or `-pyry-name=../etc` carrying path-traversal segments. Same discipline as `resolveRegistryPath` and `resolveSocketPath` in `cmd/pyry/main.go`. `resolveConfigPath` does NOT sanitize — there is no instance-name segment to neutralize. Pinned by `TestResolveDevicesPath` / `TestResolveServerIDPath` (each asserts that `../etc` cannot escape `~/.pyry/`).

The three resolvers live in `cmd/pyry/pair.go` for now. If a future ticket needs them elsewhere, lift to `main.go` alongside `resolveRegistryPath`.

## Relay URL resolution

Three-leg precedence in [`resolveRelay`](../../../cmd/pyry/pair.go):

1. `--relay` flag value
2. `cfg.RelayURL` from `~/.pyry/config.json`
3. `config.DefaultConfig().RelayURL`

The first non-empty value wins. The third leg is normally redundant — `config.Load` already overlays `DefaultConfig` onto the loaded file (see [features/config-package.md](config-package.md), [ADR 018](../decisions/018-config-overlay-decode.md)) — but the AC names it explicitly so the explicit fall-through stays in code. Empty result is reachable only if all three are empty: built-in default constant unset *and* config.json absent/unset *and* flag unset. This triggers the AC's exit-2 path.

## Auto-name

When `--name` is omitted, the device label becomes `device-<short>` where `<short>` is the first 8 characters of `HashToken(plain)`. Stable per-token, derivable from the on-disk registry entry alone, never reveals plaintext (8 hex chars = 32 bits of hash, no preimage). Computed AFTER hashing in step 7, used in step 8.

Non-empty `--name` is used verbatim — no validation, no uniqueness check. `devices.Registry.Add` documents that uniqueness is the caller's concern; this verb explicitly does not enforce it. Lookup in the auth path is by `TokenHash`, not by `Name`, so duplicate-name entries coexist harmlessly.

## Exit codes

| Code | Cause |
|------|-------|
| `0` | Pairing succeeded; payload printed to stdout |
| `1` | Registry / server-id / config I/O error, or `Render` write error |
| `2` | Flag parse error, unexpected positional, OR resolved relay URL is empty |

`runPair` returns an `error` for exit-1 conditions (which `main()` already maps to `os.Exit(1)` with a `pyry: ` prefix). It calls `os.Exit(2)` directly for exit-2 conditions so the `pyry: ` prefix doesn't appear on usage-style failures. The empty-relay stderr message names both knobs explicitly: `pyry pair: relay URL is empty (set --relay or relay_url in ~/.pyry/config.json)`.

## Token visibility (SECURITY)

The plaintext token has exactly one egress: `pair.Render(payload, os.Stdout)`. `cmd/pyry/pair.go` never constructs a `*slog.Logger`, never calls `slog.*`, and never embeds `plain` / `payload` / `Encode(payload)` into any error wrapping. All operator-visible strings are either:

- **stderr human strings** (the empty-relay hint, parse-error usage line)
- **bare `fmt.Errorf("pair: %w", err)` wraps** of underlying errors from `config.Load`, `devices.Load`, `identity.LoadOrCreate`, `crypto/rand.Read`, `registry.Save`, `pair.Render` — none of which carry token bytes by construction (each layer's contract is reviewed separately; see [features/pair-package.md](pair-package.md) § "Token visibility").

The discipline mirrors `internal/pair/render.go:23-30` and the package doc-comment of `internal/devices/device.go`. Code review enforces this on any future caller.

## Concurrency model

None. Single goroutine, top-down, no `context.Context`. `devices.Registry` is mutex-guarded internally; the verb only ever calls `Load` once and `Save` once with no intervening contention.

`identity.LoadOrCreate` is "not safe for concurrent use against the same path" (per its docstring). Since `pair` runs in a fresh process with no daemon contending for the same file, this is satisfied trivially. If a daemon happens to be running with the same `-pyry-name`, it has already created the server-id at startup; the pair verb warm-loads the same value (no overwrite per `LoadOrCreate`'s contract).

### Race against running daemon

If a daemon is running with the same `-pyry-name` and holds `devices.json` in memory, `pair` appends + saves a fresh entry on disk; the daemon's in-memory copy is now stale until its next `Load`. This is structurally empty in #213 because no daemon-side consumer of `devices.json` exists yet — the WS-handshake auth path is a future ticket. The future ticket will add reload-on-write or a control verb that asks the daemon to mint (mirroring `pyry sessions new`).

### Race between two `pyry pair` invocations

Two concurrent invocations each `Load` the same registry, each `Add` a fresh device, each `Save`. The second `Save`'s atomic-rename overwrites the first's, dropping the first's appended entry. The first invocation has already printed a QR; the user thinks they paired but the on-disk record is gone, so the phone's later auth will fail. This is a `devices.Registry`-level limitation (the API is read-modify-write, not read-modify-write-with-CAS) and out of scope for #213's wiring — the fix is `flock`/`fcntl` on `devices.json` at the registry layer. Operationally the window is small (the user sees a printed QR moments before the loss); failure mode is "phone fails to auth," not credential leakage.

## Tests

### Unit (`cmd/pyry/pair_test.go`)

Same package, table-driven, stdlib `testing` only. Covers the pure pieces; filesystem-side behavior is e2e-tested.

- `TestParsePairArgs` — happy paths (empty, `--name`, `--relay`, both, name-space-form) plus the two error shapes runPair maps to exit 2 (unexpected positional, unknown flag).
- `TestResolveRelay` — three-leg precedence: flag wins, config wins when flag empty, default wins when both empty.
- `TestResolveDevicesPath` / `TestResolveServerIDPath` — HOME-isolated via `t.Setenv("HOME", t.TempDir())`; assert the joined path AND that path-traversal input (`../etc`) is neutralized via `filepath.Rel` containment check.
- `TestResolveConfigPath` — confirms the per-user (no instance-name) layout.

### End-to-end (`internal/e2e/pair_test.go`, `//go:build e2e`)

`TestPair_E2E` fulfills AC #6. Two sub-tests:

1. **with explicit name** — `RunBareIn(t, home, "pair", "--name=test-phone")`, decode payload via `pair.Decode`, load `<home>/.pyry/pyry/devices.json` via `devices.Load`, assert exactly one entry with `Name == "test-phone"` and `devices.VerifyToken(payload.Token, entry.TokenHash)` true.
2. **auto-name when `--name` omitted** — same shape, asserts `entry.Name == "device-" + entry.TokenHash[:8]`.

The empty-relay (AC exit-2 path) is covered by `TestResolveRelay` rather than e2e because it's not reachable through real config — the default constant is non-empty, so an e2e test would require swapping it (build-time, out of scope).

### `RunBareIn` helper

Sibling of `RunBare` added to `internal/e2e/harness.go` for #213. Behaves like `RunBare` but pins `HOME` to a caller-supplied directory via `cmd.Env = childEnv(home)`, so verbs that read `~`-relative state (e.g. `pair`) can be driven against a `t.TempDir()` in isolation. Like `RunBare`, it does NOT auto-inject `-pyry-socket` — there is no daemon spawned. ~25 LOC, mechanical copy of `RunBare` with one line spliced. See [features/e2e-harness.md](e2e-harness.md).

## `pyry pair list` (#214)

Read-only operator inspection of the on-disk registry. No mint, no `Save`, no `Add`/`Remove`. Cold-start (file missing or zero-byte) is a valid empty state, not an error.

```
1. Parse flags                                                → exit 2 on parse error
2. resolveDevicesPath(name) → path                            (sanitized)
3. devices.Load(path) → registry                              → exit 1 on I/O / parse error
4. renderPairList(registry.List(), os.Stdout)                 → exit 1 on write error
```

### Output format

Empty registry — exactly:

```
No paired devices.\n
```

Non-empty — `text/tabwriter` aligned columns (2-space padding, no minimum width), header row plus one data row per device:

```
NAME   PAIRED                LAST SEEN             TOKEN-PREFIX
alpha  2026-01-01T00:00:00Z  2026-01-02T00:00:00Z  aaaaaaaa
bravo  2026-01-03T00:00:00Z  2026-01-04T00:00:00Z  bbbbbbbb
```

Column rules:

- `NAME` — `Device.Name` verbatim.
- `PAIRED` — `Device.PairedAt` formatted as `time.RFC3339`.
- `LAST SEEN` — `Device.LastSeenAt` formatted as `time.RFC3339`; the literal string `never` when the value is the zero time.
- `TOKEN-PREFIX` — first 8 lowercase hex chars of `Device.TokenHash` (visual identification only; never the plaintext token).

### Defensive sort, even though `Save` already sorts

The formatter applies `sort.SliceStable` by `(PairedAt, Name)` ascending on every call, even though `Registry.Save` already writes the on-disk slice in that order. The formatter's input is `Registry.List()` — whatever `Load` decoded into memory. If a future writer skips `Save`'s sort step or the file was hand-edited, the formatter still produces the documented order. Cost: one stable sort on a tiny slice; benefit: the AC's determinism guarantee is local to `renderPairList`, not coupled to a sibling package's invariant. Uses `time.Time.Equal` (not `==`) for the primary comparator — JSON roundtrip strips monotonic-clock state (per [`docs/lessons.md`](../../lessons.md) § "Atomic on-disk writes").

### Defensive token-prefix length check

`Device.TokenHash` is documented as 64 hex chars, but `renderPairList` is a pure UI function and shouldn't panic on malformed input. The `len(prefix) >= 8` guard avoids `runtime error: slice bounds out of range` on a corrupt registry; `prefix = d.TokenHash` is the fallback (display the whole short hash rather than panic).

### Pure formatter

`renderPairList(list []devices.Device, w io.Writer) error` — no globals, no `os.Stdout` access, no clock reads. The output is a deterministic function of `list`, which is what makes it unit-testable byte-for-byte. The non-empty-list golden is captured as a string literal in `TestRenderPairList_TwoDevices`; if `tabwriter`'s padding heuristic ever shifts across Go versions, that test updates with it.

### Exit codes

Same shape as the bare verb:

| Code | Cause |
|------|-------|
| `0` | Listing succeeded (including the empty-registry case) |
| `1` | Registry I/O error or stdout write error |
| `2` | Flag parse error, unexpected positional, unknown sub-verb (via `runPair`) |

`runPairList` returns `error` for exit-1 conditions (`main()` adds the `pyry: ` prefix and maps to `os.Exit(1)`); calls `os.Exit(2)` directly for exit-2 conditions so the prefix doesn't appear on usage-style failures. Registry path is included in the error chain because `devices.Load` already wraps with `registry: read <path>: <err>`; `runPairList` adds `pair list: %w` on top.

### Tests

Unit (`cmd/pyry/pair_test.go`):

- `TestRenderPairList_TwoDevices` — golden-string assertion of the entire byte-for-byte output (header + two rows in `(PairedAt, Name)` order + tabwriter padding).
- `TestRenderPairList_NeverSeen` — single device with zero `LastSeenAt`; asserts the literal `never` and that `0001-01-01` does NOT appear.
- `TestRenderPairList_Empty` — `bytes.Equal(buf.Bytes(), []byte("No paired devices.\n"))`.
- `TestRenderPairList_SortOrder` — input slice in non-sorted order; asserts output rows in ascending `(PairedAt, Name)` independent of input order. Guards the defensive sort on its own.
- `TestParsePairListArgs` — table: empty (defaults), `-pyry-name=foo`, positional rejected, unknown flag rejected.

E2E (`internal/e2e/pair_test.go`, `//go:build e2e`):

- `TestPairList_E2E` — two sub-tests. "empty registry" runs `pyry pair list` on a fresh `t.TempDir()` HOME and asserts exact stdout `No paired devices.\n` + exit 0. "after pair" runs `pyry pair --name=phone-a` first, loads the registry directly to capture the expected `TokenHash[:8]`, then runs `pyry pair list` and asserts both `phone-a` and the prefix appear in stdout.

## Open question (deferred)

**Daemon staleness on warm `devices.json`.** When a Phase 3 daemon-side WS-handshake auth path lands, it will read `devices.json` once at startup and hold the in-memory `Registry`. A `pyry pair` invocation while the daemon is running will append on disk but leave the daemon's copy stale until its next `Load`. Mitigation (reload-on-write via fsnotify on `devices.json`, or a daemon-side `pair` control verb that mints in-process) is owned by the consumer ticket. Current wiring is the right shape: the storage primitive (`devices.Registry`) doesn't know about the daemon; the verb writes through it directly.

## Out of scope (deferred)

- **`pyry pair revoke <name>`** — sibling (#215), calls `Remove(name)` then `Save`. Will append `revoke` to `pairVerbList` and add one switch case in `runPair`.
- **Daemon-side `pair` control verb** — mirroring `pyry sessions new` once a WS-handshake auth path actually consumes `devices.json` in-process.
- **`flock`/`fcntl` on `devices.json`** — fix for two-concurrent-`pair` races; belongs at the `devices.Registry` layer (a property of #209's API, not this wiring).
- **Empty-relay e2e test** — would require swapping the default constant at build time; pinned by unit test instead.
- **`--relay` mutating `~/.pyry/config.json`** — explicitly rejected; `--relay` overrides the printed payload only. Edit `config.json` directly to persist a relay change.

## Related

- [`features/config-package.md`](config-package.md) — `Config.RelayURL` and `Load`'s overlay-decode shape consumed in step 2.
- [`features/identity-package.md`](identity-package.md) — `LoadOrCreate` contract for the per-instance server-id (step 5).
- [`features/devices-package.md`](devices-package.md) — `Device` shape, `HashToken` (step 7), `VerifyToken` (used in the e2e linkage check).
- [`features/devices-registry.md`](devices-registry.md) — `Registry.Load` / `Add` / `Save` semantics consumed in steps 4, 9, 10.
- [`features/pair-package.md`](pair-package.md) — `Payload`, `Encode`, `Render` (step 11); token-secrecy contract that this wiring inherits.
- [`features/e2e-harness.md`](e2e-harness.md) — `RunBareIn` helper added for AC #6.
- [ADR 020](../decisions/020-devices-registry-snapshot-then-write.md) — `Save`'s snapshot-then-write discipline that keeps the auth path unblocked while pair writes.
- [ADR 021](../decisions/021-pair-cli-order-of-operations.md) — load-fail-fast → mint → save → render order, chosen so no plaintext token escapes the process if `Save` fails.
- `docs/protocol-mobile.md:60-65` — token format (256-bit random, hex-encoded; binary stores `sha256(token)` only).
- [ADR 010](../decisions/010-sessions-cli-sub-router.md) — `runSessions` sub-router shape that `runPair` mirrors (with the `parseClientFlags` deviation noted above).
- `docs/specs/architecture/213-pair-command.md` — architect's spec for the bare-pair wiring slice.
- `docs/specs/architecture/214-pair-list.md` — architect's spec for `pyry pair list`.
