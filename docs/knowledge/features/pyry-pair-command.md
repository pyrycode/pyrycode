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
| `revoke <name>` | Remove the registry entry whose `Name` matches |
| `preflight` | v2 release gate — exit 0 if registry empty, exit 2 if any paired device exists (silent stdout, byte-pinned stderr line on the non-empty path) |

Mirrors `runSessions`'s sub-router shape ([ADR 010](../decisions/010-sessions-cli-sub-router.md)) with one deviation: `runPair` does NOT call `parseClientFlags` — the pair family does not dial the daemon, so there is no `-pyry-socket` to peel. Per-verb flag-set parsers (`parsePairArgs`, `parsePairListArgs`, `parsePairRevokeArgs`, `parsePairPreflightArgs`) own `-pyry-name` directly.

`pairVerbList` is a single string constant updated in lockstep when verbs land (today: `"list, revoke, preflight"`). The dispatcher is deliberately ~10 lines, NOT factored into a generic helper — `runSessions` and `runPair` each carry their own switch by design (CLAUDE.md "Simplicity first"; each new sibling verb is one switch case, not a refactor).

**Flags-vs-verb disambiguation.** `pyry pair --name=foo` has `--name=foo` as `args[0]`. The dispatcher must not treat that as an unknown verb. Real verbs are bare identifiers (no leading `-`); the `strings.HasPrefix(args[0], "-")` check routes flag-leading args to `runPairDefault` so the bare-pair flow keeps accepting `--name`/`--relay`. Unknown bare-verb tokens hit the usage-error path → exit 2.

## Surface

```
pyry pair [-pyry-name=<instance>] [--name <device-label>] [--relay <url>] [--allow-remote-permissions]
pyry pair list [-pyry-name=<instance>]
pyry pair revoke [-pyry-name=<instance>] <name>
pyry pair preflight [-pyry-name=<instance>]
```

| Flag | Purpose | Default |
|------|---------|---------|
| `-pyry-name` | Instance scope (state dir: `~/.pyry/<name>/`) | `defaultName()` — `PYRY_NAME` env or literal `"pyry"` |
| `--name` | Device label persisted in the registry | `device-<short>` (first 8 hex chars of the token hash) |
| `--relay` | Override the relay URL printed in the payload (does NOT mutate config.json) | resolved from config, then built-in default |
| `--allow-remote-permissions` | Authorize THIS device to answer a remote permission/trust/destructive modal (#702) — records `Device.AllowRemotePermissions` on the pairing record | `false` (OFF — the safe default; the only writer of the bit) |

The `--allow-remote-permissions` bit (added #702) is the per-device authorization for the highest-trust mobile action: answering a permission modal. `pyry pair` is its **only** writer — it is never settable over the wire. Everything else a paired phone does stays ungated. Bare `--allow-remote-permissions` → `true`; absent → `false`; `--allow-remote-permissions=false` → `false` (Go `flag` bool semantics). See [`features/devices-package.md`](devices-package.md) § "Remote-permission gate" and [ADR 025](../decisions/025-mobile-remote-head-interactive-session.md) § "Security model".

`-pyry-name` is added because `devices.json` and `server-id` are per-instance — same scoping as `pyry sessions *` and the supervisor itself. A single-instance user gets one consistent state directory across every verb.

## `pyry pair` (bare) — operation order

Chosen so no failure path leaves a plaintext token in any context outside this process's RAM:

```
 1. Parse flags                                                          → exit 2 on parse error
 2. config.Load(~/.pyry/config.json)                                     → exit 1 on I/O / parse error
 3. resolveRelay(flag, cfg) → relay URL                                  → exit 2 if empty
 4. identity.LoadOrCreate(~/.pyry/<name>/server-id) → server-id          → exit 1 on I/O error
 5. keys.LoadOrCreate(~/.pyry, sanitizeName(name)) → static keypair      → exit 1 on I/O / corruption / mode error
 6. crypto/rand.Read(32 bytes) → hex.EncodeToString                      → exit 1 on rng error
 7. devices.HashToken(plain) → 64-char hex SHA-256
 8. deviceName := --name OR "device-" + hash[:8]
 9. devices.WithLock(devicesPath, pairLockWait, func)                    → exit 1, byte-unchanged file, on a busy lock (#1531)
      a. devices.Load(~/.pyry/<name>/devices.json) → registry              → exit 1 on I/O error
      b. registry.Add(Device{TokenHash, Name, PairedAt: now.UTC(), RedeemBy, AllowRemotePermissions})
      c. registry.Save(devicesPath)                                        → exit 1 on I/O error
10. pair.Render(Payload{Server, Relay, Token, ServerStaticPubkey}, os.Stdout)  → exit 1 on write error
```

Steps 2–5 run before any token is generated: a misconfigured relay, unreadable server-id, or insecure static-key file fails fast, before the random draw. Step 6 happens only on the path where 1–5 succeeded. Once Step 6 runs, the plaintext token exists only in this process's RAM until Step 10 writes it to stdout.

**Step 9's `Load` runs *inside* the lock, not before it (#1531).** Before #1531, this verb loaded the registry as an early step (where step 4 used to sit, ahead of the key loads), then mutated and saved that same snapshot with no cross-process exclusion — any daemon or other CLI write landing in the window between the load and the save was silently erased by this verb's `Save`. Retrofitting the fix is not just wrapping the existing `Save` in `devices.WithLock`: that alone would still mutate a snapshot taken outside the lock, leaving the bug fully intact. The `Load` had to move to *after* key generation so the snapshot it mutates is read in the same critical section as the `Save`. Steps 4–8 (`identity.LoadOrCreate`, `keys.LoadOrCreate`, the CSPRNG read, hashing, naming) stay outside the region on purpose — none of them touch `devices.json`, and holding the lock across key generation would falsify `redemptionLockWait`'s doc comment, which promises its peers (this verb and revoke) hold sub-millisecond regions. See [`features/devices-registry.md`](devices-registry.md) § "Update (2026-09-07, #1531)" and § "Testing a best-effort, lock-guarded persist" for the `WithLock` mechanics and the test shape that distinguishes a real fix from a wrap-only one.

A refused acquisition (lock held elsewhere past `pairLockWait`) fails closed: step 9 never runs `fn`, so `devices.json` isn't opened at all, and the already-minted `plain` token is discarded without being persisted or rendered — a token whose hash never reached disk is unusable, not unrecorded.

**Step 6** (`keys.LoadOrCreate`, added #432) loads the binary's persistent Noise_IK static keypair from `~/.pyry/<sanitized-name>/static_key.json`, minting it on first run. Failures (`ErrInvalidDaemonName` if `sanitizeName` produces a name `keys.validDaemonName` rejects; `ErrInsecureKeyDirMode` / `ErrInsecureKeyFileMode` if the keystore has weak permissions; `ErrCorruptKeyFile` on parse failure; raw I/O errors) surface as `pyry: pair: keys: <wrapped>` and exit 1 — operator-actionable. Step 12 then copies `staticKey.PublicKey()` (the `[32]byte` by-value accessor) into the `pair.Payload`'s `ServerStaticPubkey` field as `base64.StdEncoding.EncodeToString(pub[:])`. `StaticKey.PrivateKey()` is never read by this verb — grep-enforceable.

**Step 9 gates step 10.** If the locked region fails — a busy lock, or `registry.Save` failing inside it — `Render` is skipped and the plaintext token never escapes the process. The user retries; a new (independent) token is minted on the next run; the orphaned in-memory device is dropped with the process. The reverse ordering (render first, save second) would be wrong: a post-render save failure would print a working pairing payload that the daemon would later reject because the token isn't in the registry. See [ADR 021](../decisions/021-pair-cli-order-of-operations.md). The static keypair, by contrast, is intentionally persisted *before* the random draw — every paired phone binds to the same keypair, so it must outlive any single `pyry pair` invocation.

### Daemon-name validator mismatch

`cmd/pyry/main.go:sanitizeName` is a transformer that permits `.` and uppercase and replaces other bad chars with `_`; `internal/keys.validDaemonName` rejects both `.` and uppercase outright. `PYRY_NAME=Foo` produces `~/.pyry/Foo/devices.json` from `resolveDevicesPath` (which calls `sanitizeName`) but fails at `keys.LoadOrCreate("~/.pyry", "Foo")` with `ErrInvalidDaemonName`. The mismatch is intentional — `internal/keys` cannot relax its allowlist without weakening the path-traversal defence — and surfaces as a clean `pyry: pair: keys: invalid daemon name "Foo"` error. **No auto-rewrite, no `strings.ToLower`-and-retry, no synchronization.** Operator-facing fix: rename the instance to match `[a-z0-9_-]{1,64}` with no leading `-`. See [codebase/438.md](../codebase/438.md) for the rationale.

## On-disk paths

| File | Path | Owner |
|------|------|-------|
| Config | `~/.pyry/config.json` | per-user (no instance interpolation) |
| Server-id | `~/.pyry/<sanitized-name>/server-id` | per-instance |
| Devices | `~/.pyry/<sanitized-name>/devices.json` | per-instance |
| Static key | `~/.pyry/<sanitized-name>/static_key.json` | per-instance (mode 0600, parent 0700) |

`resolveDevicesPath` and `resolveServerIDPath` MUST call `sanitizeName(name)` before joining — defends against `PYRY_NAME=../../etc` or `-pyry-name=../etc` carrying path-traversal segments. Same discipline as `resolveRegistryPath` and `resolveSocketPath` in `cmd/pyry/main.go`. `resolveConfigPath` does NOT sanitize — there is no instance-name segment to neutralize. Pinned by `TestResolveDevicesPath` / `TestResolveServerIDPath` (each asserts that `../etc` cannot escape `~/.pyry/`).

`resolveStaticKeyBaseDir` (added #432) returns just the *parent* directory (`~/.pyry`); the `<sanitized-name>/static_key.json` suffix is owned by `internal/keys`, which performs its own stricter daemon-name validation via `validDaemonName` (cannot bypass via caller-precomputed path — the API takes `(baseDir, daemonName)`, not `path`). Allowlist defence + 0700/0600 mode enforcement + `O_NOFOLLOW` on read all live in `internal/keys`; see [features/keys-package.md](keys-package.md) and [codebase/439.md](../codebase/439.md).

The four resolvers (`resolveDevicesPath`, `resolveServerIDPath`, `resolveConfigPath`, `resolveStaticKeyBaseDir`) live in `cmd/pyry/pair.go` for now. If a future ticket needs them elsewhere, lift to `main.go` alongside `resolveRegistryPath`.

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

Single goroutine, top-down, no `context.Context` — no goroutines are spawned in production code. `devices.Registry` is mutex-guarded internally for in-process use, and since #1531 the mint's and revoke's whole read-modify-write also runs inside one held `devices.WithLock(devicesPath, pairLockWait, …)` region (a per-instance cross-process `flock(2)` sidecar at `<devicesPath>.lock`), so `Load` runs exactly once and `Save` runs exactly once with no daemon or sibling-CLI write able to land between them. Lock ordering is file lock → `Registry.mu`, matching `recordRedemption` (`internal/relay/v2session_redemption.go`); `WithLock` is the `devices` package's sole acquirer, so no inversion is reachable and neither closure nests a second acquisition. `pairLockWait` defaults to `devices.DefaultLockWait` — an operator-invoked one-shot isn't a request path, so it doesn't inherit the tightened bound `redemptionLockWait` uses. See [`features/devices-registry.md`](devices-registry.md) § "Testing a best-effort, lock-guarded persist" for the primitive and its testing idioms.

`identity.LoadOrCreate` is "not safe for concurrent use against the same path" (per its docstring). Since `pair` runs in a fresh process with no daemon contending for the same file, this is satisfied trivially. If a daemon happens to be running with the same `-pyry-name`, it has already created the server-id at startup; the pair verb warm-loads the same value (no overwrite per `LoadOrCreate`'s contract). This load sits outside the devices lock — it never touches `devices.json`.

### Race against running daemon (partially open — #1532)

If a daemon is running with the same `-pyry-name` and holds `devices.json` in memory, `pair`'s locked write still lands on disk correctly, but the daemon's in-memory copy is stale until it reloads (`internal/relay`'s v2 handshake reloads before each `Validate`, per [`features/devices-registry.md`](devices-registry.md) § Reload). **This slice does not close the daemon-vs-CLI race**: the daemon's own writer, `register_push_token` → `UpdatePushRegistration` → `Save`, is unlocked until #1532 lands, so a daemon `Save` landing inside this verb's now-locked region still overwrites whatever it wrote — the lock only excludes other `WithLock` callers, and `register_push_token` isn't one yet. #1532 is the ticket that retrofits that writer and closes the race end-to-end.

### Race between two `pyry pair` invocations — closed (#1531)

Two concurrent invocations used to each `Load` the same registry, each `Add` a fresh device, each `Save` — the second `Save`'s atomic-rename would overwrite the first's, silently dropping the first's appended entry after it had already printed a working-looking QR. `devices.WithLock` closes this: both invocations serialize on the sidecar `flock`, and each re-reads the registry *inside* the lock before mutating, so the second invocation's `Save` is built on a snapshot that includes the first's device. The same fix closes the security-relevant sibling case — a `pyry pair` racing a `pyry pair revoke` can no longer resurrect the device the revoke just removed, because revoke's `Save` (or the mint's) always mutates a snapshot taken after the other's commit, never before it.

## Tests

### Unit (`cmd/pyry/pair_test.go`)

Same package, table-driven, stdlib `testing` only. Covers the pure pieces; filesystem-side behavior is e2e-tested.

- `TestParsePairArgs` — happy paths (empty, `--name`, `--relay`, both, name-space-form) plus the two error shapes runPair maps to exit 2 (unexpected positional, unknown flag).
- `TestResolveRelay` — three-leg precedence: flag wins, config wins when flag empty, default wins when both empty.
- `TestResolveDevicesPath` / `TestResolveServerIDPath` — HOME-isolated via `t.Setenv("HOME", t.TempDir())`; assert the joined path AND that path-traversal input (`../etc`) is neutralized via `filepath.Rel` containment check.
- `TestResolveConfigPath` — confirms the per-user (no instance-name) layout.
- `TestRunPairDefault_PopulatesStaticPubkey` (#432) — drives `runPairDefault(nil)` against an isolated `HOME=t.TempDir()`, captures stdout via an inline `captureStdout(t, fn)` pipe helper, decodes the rendered payload via `pair.Decode`, asserts `ServerStaticPubkey != ""` and base64-decodes to exactly 32 bytes. Runs `runPairDefault` a *second* time against the same HOME and asserts the same `ServerStaticPubkey` — verifies `keys.LoadOrCreate` returns the persisted key, not a fresh one. Skipped on Windows.

`cmd/pyry/pair_lock_test.go` (#1531), same package, POSIX-only (`flock`):

- `TestRunPairDefault_BusyLockRefusesWithoutWriting` — holds the sidecar lock, shrinks `pairLockWait` so the bound is reachable in-test, asserts `runPairDefault` returns an error wrapping `devices.ErrLockBusy` naming the lock path, and that `devices.json` is unchanged in both bytes *and* mtime (mtime back-dated first via `os.Chtimes`, since `Save` is idempotent and a byte comparison alone passes vacuously).
- `TestRunPairDefault_ReadsSnapshotInsideLock` — **the flagship test, and the one that separates this design from wrapping the existing `Save` alone.** Parks a lock holder, starts `runPairDefault` on a goroutine, asserts it has *not* completed after a 300ms grace (proves no region covers the write), commits a second device from under it, releases, and asserts the final registry holds both that device and the minted one. Reddens two distinct ways: with no lock at all the mint completes during the grace; with the lock wrapped around `Save` only, the mint's pre-lock `Load` misses the concurrently-committed device and its `Save` erases it. Its discriminating power against the wrap-only mutant was verified with a `go test -overlay` run (no worktree writes), not assumed.
- `TestRunPairRevoke_BusyLockRefusesWithoutWriting` / `TestRunPairRevoke_ReadsSnapshotInsideLock` — the revoke legs of the two tests above, run against `runPairRevoke([]string{"alpha"})` instead of `runPairDefault`. The snapshot test is also the security-relevant direction: a device committed by a racing `pair` while revoke is parked on the lock must survive the revoke of a *different* device, never be silently erased by revoke's `Save`.
- `TestRunPairReadVerbs_TakeNoLock` — `pair list` and `pair preflight` never create the `.lock` sidecar; see § "`pyry pair list`" § Tests below for the full description.

### End-to-end (`internal/e2e/pair_test.go`, `//go:build e2e`)

`TestPair_E2E` fulfills AC #6. Two sub-tests:

1. **with explicit name** — `RunBareIn(t, home, "pair", "--name=test-phone")`, decode payload via `pair.Decode`, load `<home>/.pyry/pyry/devices.json` via `devices.Load`, assert exactly one entry with `Name == "test-phone"` and `devices.VerifyToken(payload.Token, entry.TokenHash)` true. Asserts `payload.ServerStaticPubkey != ""` (#432).
2. **auto-name when `--name` omitted** — same shape, asserts `entry.Name == "device-" + entry.TokenHash[:8]`. Asserts `payload.ServerStaticPubkey != ""` (#432).

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
- `TestRunPairReadVerbs_TakeNoLock` (#1531, `cmd/pyry/pair_lock_test.go`) — against an instance directory with a saved registry and no `.lock` sidecar, runs `pair list` and `pair preflight` and asserts the sidecar still doesn't exist afterwards. The sidecar is created by `WithLock` before its function runs, so its continued absence is a strictly stronger witness than "no `Save` ran" — it proves the region was never entered at all, which is what a reader must guarantee: taking the lock in a reader would let a writer block a listing for the full `pairLockWait`.

E2E (`internal/e2e/pair_test.go`, `//go:build e2e`):

- `TestPairList_E2E` — two sub-tests. "empty registry" runs `pyry pair list` on a fresh `t.TempDir()` HOME and asserts exact stdout `No paired devices.\n` + exit 0. "after pair" runs `pyry pair --name=phone-a` first, loads the registry directly to capture the expected `TokenHash[:8]`, then runs `pyry pair list` and asserts both `phone-a` and the prefix appear in stdout.

## `pyry pair revoke <name>` (#215)

Destructive operator action: remove the registry entry whose `Device.Name` equals `<name>`. Thin wiring on top of `Registry.Remove` + `Registry.Save`.

```
1. Parse flags + sole positional                              → exit 2 on parse / arg-count error
2. resolveDevicesPath(name) → path                            (sanitized)
3. devices.WithLock(path, pairLockWait, func)                  → exit 1, byte-unchanged file, on a busy lock (#1531)
     a. devices.Load(path) → registry                             → exit 1 (wrapped) on I/O / parse error
     b. registry.Remove(<name>) → bool                            → errPairDeviceNotFound sentinel when false
     c. registry.Save(path)                                       → exit 1 (wrapped) on I/O error
4. sentinel? → direct os.Exit(1) OUTSIDE the region (see below)   : fmt.Printf("Revoked %s.\n", <name>) to stdout, exit 0
```

`Registry.Remove` does the byte-exact name lookup and slice splice; the runner is deliberately a thin shell — no caller-side index search, no plain comparison. Cold-start (file missing or zero-byte) collapses to "not found" via `Registry.Load`'s contract, so step 3a never errors on a fresh HOME and step 3b returns `false` for any `<name>`.

**Step 3 re-reads the registry inside the lock (#1531).** Before #1531 this verb `Load`ed, `Remove`d, and `Save`d with no cross-process exclusion; a `pyry pair` racing a `pyry pair revoke` could resurrect the very device the revoke just removed — the security-relevant direction, since it turns a credential the operator explicitly withdrew back into an acceptable one. `devices.WithLock` closes it the same way the mint's does: the `Load` sits inside the region, so the snapshot `Remove` operates on always reflects whatever the other side already committed.

### Save-only-on-Remove invariant (the key design decision)

`Registry.Save` runs only on the `true` branch of `Remove`. This is necessary for AC#5's "the on-disk registry is not rewritten" guarantee: even though `Save` is idempotent for unchanged content, on an empty cold-start registry it would create the file at `~/.pyry/<name>/devices.json` (`MkdirAll` + atomic rename of an empty `{"devices":[]}\n`). The not-found branch must be byte-identical to the pre-call state, which means short-circuiting before `Save`. The E2E "missing registry" sub-test pins this with `os.Stat == fs.ErrNotExist` after the call.

### Exit-code asymmetry — direct exit on not-found, returned error on I/O

Three exit branches share the value `1` but split on mechanism:

| Failure | Mechanism | Stderr |
|---|---|---|
| Not found (`Remove` returns false) | direct `os.Exit(1)` | `pyry pair revoke: no device named <name>\n` |
| `devices.Load` failure | `return fmt.Errorf("pair revoke: %w", err)` | `pyry: pair revoke: <wrapped>\n` |
| `registry.Save` failure | `return fmt.Errorf("pair revoke: %w", err)` | `pyry: pair revoke: <wrapped>\n` |

Direct `os.Exit(1)` for not-found suppresses `main.run`'s `pyry: ` prefix so the AC's byte-exact `pyry pair revoke: no device named <name>` is met without a doubled `pyry: pyry pair revoke: …`. I/O errors return-and-wrap so `main.run` adds the standard `pyry: ` for diagnostic value. Same pattern as `runPairList`'s exit-2 paths.

**The not-found branch now *does* use a sentinel error, but only to cross the `WithLock` boundary, not to reach `main.run` (#1531).** Not-found used to be detectable the moment `Remove` returned `false`, with the `os.Exit(1)` sitting right there. Once the whole `Load`/`Remove`/`Save` sequence moved inside a `devices.WithLock` region, that `os.Exit` would have fired *inside* the locked closure — survivable, since the kernel releases the `flock` when the process's descriptors close at termination, but a trap for the next in-region early-exit that guards something the kernel doesn't clean up for free. The unexported package-level `errPairDeviceNotFound` carries the not-found case out of the closure (`WithLock` returns `fn`'s error verbatim), and the `os.Exit(1)` moved to right after the `WithLock` call returns — outside the region, exactly where it was conceptually before. The dispatch is still `errors.Is(err, errPairDeviceNotFound)`, never a string match, so a device named to *read like* the not-found line can't spoof the branch, and a busy-lock `devices.ErrLockBusy` falls to the ordinary I/O-error branch instead of printing "no device named".

Exit `2` is reserved for **usage** errors only — flag parse failure, missing positional (`pyry pair revoke`), extra positional (`pyry pair revoke a b`), or unknown flag (`--bogus`). All four go through `parsePairRevokeArgs` returning an error which `runPairRevoke` maps to direct `os.Exit(2)` with `pyry pair revoke: <err>` + a usage line. Stdlib `flag` stops at the first non-flag token, so `pyry pair revoke phone -pyry-name=foo` (flag-after-positional) would treat `-pyry-name=foo` as a second positional and exit 2 — same constraint as every other sibling subcommand; not a custom reorder pass.

### Concurrency

Closed for CLI-vs-CLI (#1531): see § Concurrency model above. Two concurrent `pair revoke` invocations, and a `pair revoke` racing a `pair`, now serialize on the per-instance `devices.json.lock` `flock` and each re-read the registry inside the lock before mutating, so neither can build its `Save` on a snapshot the other has already replaced. The daemon-vs-CLI leg (`register_push_token`) is still open until #1532.

### Tests

Unit (`cmd/pyry/pair_test.go`):

- `TestParsePairRevokeArgs` — table: happy (`phone`), with-instance (`-pyry-name=foo phone`), missing positional (`nil` → `missing device name`), extra positional (`a b` → `unexpected positional`), unknown flag (`--bogus phone` → `flag provided but not defined`). Pins `t.Setenv("PYRY_NAME", "")` for deterministic `defaultName()`.
- `TestRunPairRevoke_RemovesEntry` — two-device fixture; call `runPairRevoke([]string{"alpha"})`; reload via `devices.Load`; assert one survivor (`bravo`) byte-for-byte (`Name`, `TokenHash`, `PairedAt.Equal`, `LastSeenAt.Equal`).
- `TestRunPairRevoke_SaveFailure` — fixture as above, then `os.Chmod(<dir>, 0o500)`; assert returned error contains `pair revoke:`. **Its doc comment still says this exercises a `Save` failure; since #1531 it doesn't.** `WithLock` now opens the `.lock` sidecar before `fn` runs, and that open fails against the `0500` parent first — the error is a lock-acquisition failure one layer earlier than `Save`, not a `Save` failure, though it still carries the asserted `pair revoke:` prefix so the test stays green. No test in the package currently exercises revoke's `Save` failing inside an acquired lock; a fix (pre-create the sidecar at `0600` before the chmod, so the lock opens but the temp-file rename still fails) was identified in code review and deferred rather than landed in this ticket — worth picking up alongside #1532 rather than treating the green run as coverage it no longer provides.

Not-found and missing-positional paths can't be unit-tested without `os.Exit` capture machinery (not present in this repo); they're covered E2E.

`cmd/pyry/pair_lock_test.go` (#1531) — see § "`pyry pair` (bare)" § Tests above for `TestRunPairRevoke_BusyLockRefusesWithoutWriting` and `TestRunPairRevoke_ReadsSnapshotInsideLock`, the revoke legs of the same two lock-behaviour criteria.

E2E (`internal/e2e/pair_test.go`, `//go:build e2e`) — `TestPairRevoke_E2E` with three sub-tests, all using `RunBareIn(t, home, "pair", "revoke", …)`:

- **removes one of two** — pair `phone-a`, pair `phone-b`, capture `phone-b`'s on-disk entry, `pyry pair revoke phone-a`; assert exit 0 + stdout exactly `Revoked phone-a.\n` + stderr empty + survivor's `TokenHash` and `PairedAt` byte-identical to the captured snapshot.
- **not found** — pair `phone-a`, snapshot the file bytes, `pyry pair revoke ghost`; assert exit 1 + stderr exactly `pyry pair revoke: no device named ghost\n` + stdout empty + on-disk bytes unchanged (`bytes.Equal`).
- **missing registry** — fresh HOME, no prior pair, `pyry pair revoke ghost`; assert exit 1 + same stderr + `os.Stat(<path>) == fs.ErrNotExist` after the call (the AC's "no file created" requirement).

## `pyry pair preflight` (#436)

v2 release gate: exit non-zero if any paired device exists on the target binary, so the release workflow can refuse to flip the v2 cutover flag against a deployment that still has v1 pair records. v1 records have no `server_static_pubkey` (#432), and v2 has no migration tooling per [ADR 024](../decisions/024-noise-ik-mobile-e2e.md); any paired device at v2 cutover would fail Noise_IK handshake with WS code `4426` and the user has no recovery path other than `pyry pair revoke && pyry pair` per device.

```
1. Parse flags                                                → exit 2 on parse error
2. resolveDevicesPath(name) → path                            (sanitized)
3. devices.Load(path) → registry                              → exit 1 (wrapped) on I/O / parse error
4. preflightVerdict(len(registry.List())) → (code, line)
5. if code != 0: fmt.Fprintln(os.Stderr, line); os.Exit(code) (exit 2)
   else:         return nil                                    (exit 0)
```

### Output contract

| Outcome | Stdout | Stderr | Exit |
|---|---|---|---|
| Registry empty (gate passes) | (silent) | (silent) | `0` |
| Registry I/O error / malformed JSON | (silent) | `pyry: pair preflight: <wrapped err>\n` | `1` |
| Registry non-empty (gate fails) | (silent) | `pyry pair preflight: <N> paired device(s); v2 release gate requires zero.\n` | `2` |
| Flag parse error / unknown flag / unexpected positional | (silent) | `pyry pair preflight: <err>\nusage: pyry pair preflight [-pyry-name=<instance>]\n` | `2` |

Stdout is silent on every branch — allows release tooling to redirect stdout to `/dev/null` without losing the gate-failure message. The 0/1/2 triple matches `grep(1)`: 0 = condition met (gate passes), 1 = check itself failed, 2 = gate fired. The 1-vs-2 split is load-bearing for CI: "the check itself failed" needs human investigation; "the gate fired" needs the operator-instructed remediation.

### `preflightVerdict` — pure verdict helper

```go
func preflightVerdict(count int) (exitCode int, stderrLine string)
```

Returns `(0, "")` on `count == 0`, `(2, "pyry pair preflight: <N> paired device(s); v2 release gate requires zero.")` on `count > 0`. Extracted from `runPairPreflight` so the byte-exact stderr-line contract is unit-testable without driving `os.Exit` through a subprocess. The "device(s)" form is deliberate — singular/plural pluralisation in CI output is not worth the branch.

### Why a dedicated verb, not `pair list --quiet --fail-if-any`

Three reasons (architect spec):

1. **Output divergence.** `pair list` writes a table to stdout; preflight is silent on stdout on every branch. Smuggling completely different output behaviour through a flag combo on the same verb is more surprising than a clearly-named sibling.
2. **CI legibility.** Release tooling literally contains the string `pyry pair preflight`. A reader who has never seen this codebase understands what it does.
3. **No ambiguous flag interactions.** A `--fail-if-any` flag would invite future flags (`--max-count`, `--exclude-name`, …); a dedicated verb pins the surface at its current behaviour and pushes any future variants onto their own verb.

The 2-line `devices.Load` + `registry.List()` preamble shared with `runPairList` is **not extracted** — per the project's "duplicated until a Nth instance forces extraction" policy, this is the first sibling reader and the downstream output contracts (table vs. count-and-gate) diverge entirely.

### Stability vs. `pair list`

`pair list`'s default tabular stdout output is unchanged. Out-of-tree scripts parsing the table are unaffected (issue body Technical Note). The gate is strictly additive — a new opt-in sub-verb, not a flag mutating `pair list`.

### Intended CI invocation

Documented in [`docs/protocol-mobile.md`](../../protocol-mobile.md) § *Pre-flight*:

```sh
if ! pyry pair preflight; then
    echo "v2 release gate failed; aborting" >&2
    exit 1
fi
```

A `case $?` block can distinguish the exit-1 (check failed) and exit-2 (gate fired) cases when finer-grained reporting is needed. The pyrycode release tooling does not yet have a feature-flag mechanism distinct from version bumping; this documented invocation is the load-bearing artefact until one lands. The CLI exit-code behaviour is already in `pyry`.

### Tests

Unit (`cmd/pyry/pair_test.go`):

- `TestParsePairPreflightArgs` — table: empty (defaults), `-pyry-name=foo`, positional rejected, unknown flag rejected.
- `TestPreflightVerdict` — table: `count=0 → (0, "")`; `count=1` / `count=2` / `count=10` → `(2, "pyry pair preflight: <N> paired device(s); v2 release gate requires zero.")`. Byte-for-byte stderr-line contract pinned here.
- `TestRunPairPreflight_EmptyRegistry` — `t.Setenv("HOME", t.TempDir())`, no `devices.json` on disk, assert `runPairPreflight(nil)` returns nil. Pins the cold-start (missing file) gate-pass path.
- `TestRunPairPreflight_ZeroByteRegistry` — same, but write a zero-byte `devices.json` first; assert `runPairPreflight(nil)` returns nil. Pins the cold-start (empty file) gate-pass path via `devices.Load`'s contract.
- `TestRunPairPreflight_CorruptRegistry` — write `[}` (malformed JSON) to `devices.json`; assert returned error is non-nil and prefixed with `"pair preflight:"`. Pins the exit-1 wrapped-error path.

The exit-2 non-empty-registry path (`os.Exit(2)` from inside `runPairPreflight`) is not driven through the runner in unit tests because `os.Exit` terminates the test binary. The byte-exact contract for that path is fully covered by `TestPreflightVerdict`; the remaining two-line dispatch (`fmt.Fprintln(os.Stderr, line); os.Exit(exitCode)`) is trivial wiring. Same shape as how `runPairRevoke`'s `os.Exit(1)` not-found path is unit-tested (it isn't — covered e2e).

## Open question (deferred)

**Daemon staleness on warm `devices.json`.** When a Phase 3 daemon-side WS-handshake auth path lands, it will read `devices.json` once at startup and hold the in-memory `Registry`. A `pyry pair` invocation while the daemon is running will append on disk but leave the daemon's copy stale until its next `Load`. Mitigation (reload-on-write via fsnotify on `devices.json`, or a daemon-side `pair` control verb that mints in-process) is owned by the consumer ticket. Current wiring is the right shape: the storage primitive (`devices.Registry`) doesn't know about the daemon; the verb writes through it directly.

## Out of scope (deferred)

- **Daemon-side `pair` control verb** — mirroring `pyry sessions new` once a WS-handshake auth path actually consumes `devices.json` in-process.
- ~~**`flock`/`fcntl` on `devices.json`**~~ Delivered for the two CLI writers by #1531 (`devices.WithLock`, added by #1530). `register_push_token`, the daemon's writer, remains unlocked until #1532 — until then the daemon-vs-CLI race is still open even though CLI-vs-CLI is closed.
- **Empty-relay e2e test** — would require swapping the default constant at build time; pinned by unit test instead.
- **`--relay` mutating `~/.pyry/config.json`** — explicitly rejected; `--relay` overrides the printed payload only. Edit `config.json` directly to persist a relay change.

## Related

- [`features/config-package.md`](config-package.md) — `Config.RelayURL` and `Load`'s overlay-decode shape consumed in step 2.
- [`features/identity-package.md`](identity-package.md) — `LoadOrCreate` contract for the per-instance server-id (step 5).
- [`features/devices-package.md`](devices-package.md) — `Device` shape, `HashToken` (step 7), `VerifyToken` (used in the e2e linkage check).
- [`features/devices-registry.md`](devices-registry.md) — `Registry.Load` / `Add` / `Save` semantics consumed in steps 4, 9, 10.
- [`features/pair-package.md`](pair-package.md) — `Payload`, `Encode`, `Render`, `Fingerprint` (step 12); token-secrecy contract that this wiring inherits.
- [`features/keys-package.md`](keys-package.md) — `LoadOrCreate(baseDir, daemonName)` (step 6); first production consumer of the Mobile Protocol v2 static keypair.
- [`features/e2e-harness.md`](e2e-harness.md) — `RunBareIn` helper added for AC #6.
- [ADR 020](../decisions/020-devices-registry-snapshot-then-write.md) — `Save`'s snapshot-then-write discipline that keeps the auth path unblocked while pair writes.
- [ADR 021](../decisions/021-pair-cli-order-of-operations.md) — load-fail-fast → mint → save → render order, chosen so no plaintext token escapes the process if `Save` fails.
- `docs/protocol-mobile.md:60-65` — token format (256-bit random, hex-encoded; binary stores `sha256(token)` only).
- [ADR 010](../decisions/010-sessions-cli-sub-router.md) — `runSessions` sub-router shape that `runPair` mirrors (with the `parseClientFlags` deviation noted above).
- `docs/specs/architecture/213-pair-command.md` — architect's spec for the bare-pair wiring slice.
- `docs/specs/architecture/214-pair-list.md` — architect's spec for `pyry pair list`.
- `docs/specs/architecture/215-pair-revoke.md` — architect's spec for `pyry pair revoke`.
- `docs/specs/architecture/436-pair-preflight-empty-check.md` — architect's spec for `pyry pair preflight`.
- `docs/specs/architecture/1531-pair-writers-through-devices-lock.md` — architect's spec for routing mint and revoke through `devices.WithLock`; carries the full security review and the mutant-verification note for the interleaving tests.
- [ADR 024](../decisions/024-noise-ik-mobile-e2e.md) — Mobile Protocol v2 / Noise_IK parent decision; § *Pre-flight* names #436 as the implementation slice.
