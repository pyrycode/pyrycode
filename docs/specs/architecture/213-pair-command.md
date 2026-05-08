# Spec: `pyry pair` subcommand (#213)

Wiring-only ticket: compose the pairing primitives built across siblings
#206–#212 behind a new `pyry pair` subcommand. Mints a fresh device-token,
persists a `Device{TokenHash,…}` to `devices.json`, prints the QR + paste
payload to stdout. No new types, no new packages, no daemon dependency —
this is a one-shot CLI verb that touches the on-disk registry directly.

## Files to read first

- `cmd/pyry/main.go:140-174` — top-level `run()` switch on `os.Args[1]`;
  add the new `case "pair":` line here next to the existing verbs.
- `cmd/pyry/main.go:451-459` (`parseClientFlags`) — pattern for peeling
  the shared `-pyry-name` / `-pyry-socket` flags. The pair verb does NOT
  dial the socket, so we don't reuse `parseClientFlags` directly, but the
  flag-set shape mirrors it: `-pyry-name` resolution via `defaultName()`.
- `cmd/pyry/main.go:663-694` — `parseSessionsNewArgs` + `runSessionsNew`
  pair: the canonical "extract a flag-only arg parser, then a thin
  runner" pattern this ticket follows.
- `cmd/pyry/main.go:1178-1236` (`printHelp`) — append one usage line and
  add a one-line entry to the verb list comment at the top of the file
  (line 22 — the reserved control-verbs section).
- `internal/config/config.go` (whole file, ≤47 lines) — `Load(path)` and
  `DefaultConfig()`. Note `Load` returns `DefaultConfig()` filled in for
  absent fields; `RelayURL` defaults to `wss://relay.pyrycode.dev`.
- `internal/identity/store.go:37-47` — `LoadOrCreate(path)` semantics
  (cold-start mints + persists; warm-start reads + validates; never
  overwrites on the existing-file path).
- `internal/devices/registry.go:25-115` — `Load(path)`, `Add(d)`,
  `Save(path)` contract. Save is atomic (temp + rename, mode 0600).
  `Add` does NOT validate uniqueness — wiring is responsible if that
  ever matters; this ticket does not need uniqueness.
- `internal/devices/device.go:24-39` — `Device{TokenHash,Name,PairedAt,LastSeenAt}`
  shape and `HashToken(plain)` (lowercase SHA-256 hex, 64 chars).
- `internal/pair/payload.go:39-66` — `Payload{Server,Relay,Token}` and
  `Encode(p)`. Encode does not validate; we construct from validated
  inputs.
- `internal/pair/render.go:31-39` — `Render(p, w)` writes QR + blank +
  encoded string + one-line instruction to `w`. Returns first write
  error; does NOT log or persist.
- `docs/protocol-mobile.md:60-65` — token format: 256-bit random,
  hex-encoded; the binary stores `sha256(token)` in `devices.json`,
  never the plaintext.
- `docs/specs/architecture/209-pair-devices-registry-crud.md:97-106`
  ("Path note") — devices.json lives at `~/.pyry/<name>/devices.json`
  per the per-instance-subdirectory convention `internal/sessions`
  already uses.
- `cmd/pyry/main.go:88-96` (`resolveRegistryPath`) — the existing
  `~/.pyry/<sanitized-name>/sessions.json` resolver. The pair command
  resolves devices.json + server-id by the same construction.
- `internal/e2e/harness.go:526-560` (`RunBare`) — drives the binary with
  no daemon and no HOME isolation. The pair e2e test needs HOME isolation
  (so it can read the resulting devices.json), so add a small
  `RunBareIn(t, home, args...)` helper alongside `RunBare`, modelled on
  `Run` minus the harness-injected `-pyry-socket` flag.
- `internal/e2e/cli_verbs_test.go:97-108` — `TestVersion_E2E` is the
  closest analogue: a no-daemon verb tested via `RunBare`.

## Context

Phase 3 wiring ticket. Every primitive is already in the repo and
unit-tested in its own ticket; nothing new is being designed here. The
design decisions worth pinning are (a) which on-disk paths the verb
resolves, (b) the order of operations (so a partial failure never
leaves a pairing whose token has escaped the process), and (c) the
exact flag and exit-code shape the AC commits us to.

The verb runs **without a daemon** — it directly reads `config.json`,
`server-id`, and `devices.json`, mints a token, appends one entry, and
saves. No socket dial, no goroutines, no context. This matches the
shape of `pyry version` (one-shot, no daemon) more than any of the
control verbs.

## Design

### Files

- **NEW** `cmd/pyry/pair.go` (~110 lines production code).
- `cmd/pyry/main.go` — two edits: one switch-case (2 lines), one
  printHelp usage line (~3 lines), one entry in the reserved-verbs
  comment block at the top (~1 line). ≤6 lines net.
- **NEW** `cmd/pyry/pair_test.go` — unit tests for the arg parser and
  the relay-resolution helper. Tests scale linearly; not counted.
- **NEW** `internal/e2e/pair_test.go` — single end-to-end test (AC #6).
- `internal/e2e/harness.go` — add `RunBareIn(t, home, args...)`
  alongside `RunBare`. ~25 lines, mechanical copy of `RunBare` with a
  `cmd.Env = childEnv(home)` line spliced in.

Production-code total: ~110 + ~6 + ~25 = ~140 lines, three production
files (one new + two edited). Within the S red lines.

### On-disk paths

Per-instance state lives under `~/.pyry/<sanitized-name>/`, mirroring
`resolveRegistryPath`. Per-user state lives at `~/.pyry/<file>` directly.

| File | Path | Owner |
|---|---|---|
| Config | `~/.pyry/config.json` | per-user (AC #4 names this path explicitly) |
| Server-id | `~/.pyry/<name>/server-id` | per-instance (`identity.LoadOrCreate`) |
| Devices | `~/.pyry/<name>/devices.json` | per-instance (`devices.Load` / `Save`) |

`<name>` is resolved exactly like the rest of cmd/pyry: `-pyry-name`
flag wins, else `PYRY_NAME` env, else the literal `"pyry"`. This is
the same `defaultName()` function the supervisor and every control
verb already uses; the pair verb participates in the same scoping.

A small helper next to `resolveRegistryPath`:

```go
// resolveDevicesPath returns ~/.pyry/<sanitized-name>/devices.json.
// Mirrors resolveRegistryPath's shape; falls back to a CWD-relative
// path if $HOME can't be resolved. MUST call sanitizeName(name)
// before joining — defends against PYRY_NAME or -pyry-name carrying
// path-traversal segments (`..`, `/`).
func resolveDevicesPath(name string) string {
    home, err := os.UserHomeDir()
    if err != nil || home == "" {
        return filepath.Join(sanitizeName(name), "devices.json")
    }
    return filepath.Join(home, ".pyry", sanitizeName(name), "devices.json")
}

// resolveServerIDPath returns ~/.pyry/<sanitized-name>/server-id.
// Same sanitization contract as resolveDevicesPath.
func resolveServerIDPath(name string) string { … }

// resolveConfigPath returns ~/.pyry/config.json (per-user, no
// instance-name interpolation, so no sanitization required).
func resolveConfigPath() string { … }
```

`sanitizeName` is the existing helper in `cmd/pyry/main.go:121-138`;
the pair verb's path resolvers MUST call it for the same reason
`resolveRegistryPath` and `resolveSocketPath` do — `PYRY_NAME=../../etc`
must not let an unprivileged user write to a sibling directory.

These three helpers live in `cmd/pyry/pair.go` (used only here for
now). If a future ticket needs them elsewhere, lift to `main.go`
alongside `resolveRegistryPath`.

### Flag surface

```
pyry pair [-pyry-name=<instance>] [--name <device-name>] [--relay <url>]
```

`-pyry-name` is the shared instance-scoping flag (matches every other
pyry verb). `--name` is the device label persisted into the registry
entry. `--relay` overrides the relay URL in the printed payload only —
it does NOT mutate `~/.pyry/config.json`.

The AC names only `--name` and `--relay`; `-pyry-name` is added
because the registry path depends on it. If the caller doesn't pass
`-pyry-name`, the resolver uses `defaultName()` (= `"pyry"` unless
`PYRY_NAME` is set) — same default the supervisor uses, so a
single-instance user gets one consistent state directory across every
verb.

The arg parser is a `flag.NewFlagSet("pyry pair", flag.ContinueOnError)`
with three `String` flags. Reject any non-flag positional with
`fmt.Errorf("unexpected positional %q", fs.Arg(0))` — same shape as
`parseSessionsNewArgs`.

### Operation order

The order is chosen so that no failure path leaves a plaintext token
in any context outside this process's RAM:

```
1. Parse flags                                      → exit 2 on parse error
2. Resolve relay URL                                → exit 2 if empty
3. Load registry from ~/.pyry/<name>/devices.json   → exit 1 on I/O error
4. LoadOrCreate server-id at ~/.pyry/<name>/server-id → exit 1 on error
5. Mint token: 32 random bytes from crypto/rand.Read → hex encode (64 chars).
   Go's `crypto/rand.Read` is documented as infallible since Go 1.20
   (always fills the buffer entirely); the implementation should
   nonetheless propagate any returned error as exit 1 rather than
   panic. Pre-1.20 short-read defensive code is unnecessary — a
   non-nil error is already conclusive.
6. Compute hash := devices.HashToken(plain)
7. Resolve device name: --name (if non-empty) or "device-" + hash[:8]
8. Append Device{TokenHash: hash, Name: <name>, PairedAt: time.Now().UTC()}
9. registry.Save(devicesPath)                       → exit 1 on I/O error
10. Build Payload{Server, Relay, Token: plain}
11. pair.Render(payload, os.Stdout)                 → exit 1 on write error
```

Steps 2 and 4 run before any token is generated: a misconfigured
relay or unreadable server-id file fails fast, before the random
draw. Step 5 happens only on the path where steps 1–4 succeeded.
Once Step 5 runs, the plaintext token exists only in this process's
RAM until Step 11 writes it to stdout. Step 9's atomic-rename Save
gates the rest: if the registry write fails, Render is skipped, and
the plaintext token never escapes the process. The user retries; a
new (independent) token is minted on the next run. The on-disk
artifact (the orphaned in-memory device) is dropped with the process.

The reverse ordering (render first, save second) would be wrong: a
post-render save failure would print a working pairing payload that
the daemon would later reject because the token isn't in the registry.

### Relay URL resolution

```go
// resolveRelay returns the first non-empty value among:
//   1. flagValue              (from --relay)
//   2. cfg.RelayURL           (from ~/.pyry/config.json, with defaults)
//   3. config.DefaultConfig().RelayURL
// Returns "" only if all three are empty.
func resolveRelay(flagValue string, cfg config.Config) string { … }
```

Note: `config.Load` already overlays `DefaultConfig()` onto a
partially-populated file, so `cfg.RelayURL` is non-empty whenever the
default is non-empty. The third leg of the OR is therefore *almost*
always redundant — its only purpose is to document the AC's
"default-empty + config-unset + flag-unset" exit-2 path explicitly,
not to rely on `Load` filling it in. Keeping the explicit fall-through
makes the AC text traceable in code.

### Auto-name

`device-<short>` where `<short>` is the first 8 characters of
`HashToken(plain)` (sha256 hex, lowercase). Stable per-token, derivable
from the on-disk registry entry alone, never reveals the plaintext
(8 hex chars = 32 bits of hash, no preimage). Computed AFTER the hash
in step 6, used in step 7. Empty `--name` (the default) triggers the
auto-name; non-empty `--name` is used verbatim — no validation, no
uniqueness check. `Add` already documents that uniqueness is the
caller's concern; this ticket explicitly does not enforce it (matches
the AC, which never names duplicate-name behaviour).

### Exit codes

```
0 — pairing succeeded; payload printed to stdout
1 — registry I/O error (ReadFile, MkdirAll, atomic write),
    server-id I/O error (the AC says "registry I/O error" — server-id
    is the same shape of failure: missing/unreadable HOME, permission
    denied, file corruption — and the operator's remediation is
    identical: fix the filesystem, retry. Folding it under exit 1 is
    less surprising than inventing a third class.),
    or stdout-write error from Render.
2 — flag parse error OR resolved relay URL is empty.
```

The AC names only "registry I/O error" for exit 1 and "empty relay"
for exit 2. The above mapping extends that contract:

- **Server-id load → exit 1.** Same fundamental failure mode as
  registry I/O; lumping under exit 1 keeps the user-observable
  contract simple.
- **Render write error → exit 1.** Stdout is closed or piped to a
  broken sink. Operationally indistinguishable from an I/O error.
- **Flag parse error → exit 2.** Matches the precedent in
  `runAttach` (`os.Exit(2)` on parse failures).

The implementation pattern: `runPair` returns an `error` for the
exit-1 cases (which `main()` already maps to `os.Exit(1)`), and calls
`os.Exit(2)` directly for parse-error / empty-relay cases.

### Sequence diagram

```
caller                 runPair                  ~/.pyry/<name>/
  │                       │                            │
  │── pyry pair ─────────▶│                            │
  │                       │── parseFlags ──┐            │
  │                       │◀───────────────┘            │
  │                       │── config.Load ─────────▶ config.json
  │                       │◀──────────────────────── (cfg)
  │                       │── resolveRelay ──┐         │
  │                       │◀─────────────────┘         │
  │                       │   if relay == "": exit 2   │
  │                       │── devices.Load ────────▶ devices.json
  │                       │◀──────────────────────── (registry)
  │                       │── identity.LoadOrCreate ▶ server-id
  │                       │◀──────────────────────── (serverID)
  │                       │── crypto/rand.Read ──┐    │
  │                       │◀─────────────────────┘    │
  │                       │── HashToken(plain) ──┐    │
  │                       │◀─────────────────────┘    │
  │                       │── registry.Add(d) ───┐    │
  │                       │◀─────────────────────┘    │
  │                       │── registry.Save ────────▶ devices.json
  │                       │◀──────────────────────── (ok)
  │                       │── pair.Render ────────┐   │
  │◀── QR + payload ──────│◀──────────────────────┘   │
  │                       │                            │
```

No goroutines, no context, no timeouts. The whole flow is
strictly sequential and bounded by filesystem I/O. Wall-clock budget
is dominated by atomic-rename Save (~ms) and qrterminal rendering
(<10 ms for a ~150-byte payload).

## Concurrency model

None. Single goroutine, top-down. `devices.Registry` is mutex-guarded
internally (per #209's spec) but the pair verb only ever calls Load
once and Save once with no intervening contention.

`identity.LoadOrCreate` is "not safe for concurrent use against the
same path" (per its docstring); since pair runs in a fresh process
with no daemon contending for the same file, this is satisfied
trivially. If a daemon happened to be running with the same
`-pyry-name`, it would have created the server-id at startup; pair
would warm-load the same value. No race because the file is never
overwritten on the existing-file path (per `LoadOrCreate`'s
"NEVER overwritten" contract).

The one race-against-daemon scenario worth naming: if a daemon is
running and holds `devices.json` in memory, pair appends + Saves a
fresh entry on disk, the daemon's in-memory copy is now stale until
its next Load. This is a Phase-3 wiring concern that future tickets
will resolve (likely via a control verb `pyry pair` that asks the
daemon to mint, mirroring `pyry sessions new`). For #213 there is no
daemon-side consumer of `devices.json` yet, so the staleness window
is structurally empty.

A second race worth naming: two concurrent `pyry pair` invocations
each Load the same registry, each Add a fresh device, each Save. The
second Save's atomic-rename overwrites the first's, dropping the
first's appended entry. The first invocation has already printed a
QR; the user thinks they paired but the on-disk record is gone, so
the phone's later auth will fail. This is a `devices.Registry`-level
limitation (the package's API is read-modify-write, not
read-modify-write-with-CAS) and out of scope for #213's wiring; the
fix is `flock`/`fcntl` on `devices.json` at the registry layer.
Operationally the window is small (the user sees a printed QR
moments before the loss) and the failure mode is "phone fails to
auth", not credential leakage. Document under
PROJECT-MEMORY/lessons.md if observed in practice.

## Error handling

| Failure | Exit | Surface |
|---|---|---|
| `flag.Parse` returns error | 2 | flag package writes its own usage to stderr; main's printer doesn't add `pyry:` prefix. |
| Unexpected positional | 2 | `fmt.Errorf("unexpected positional %q", fs.Arg(0))`, printed via `os.Exit(2)`. |
| Resolved relay URL is empty | 2 | `fmt.Fprintln(os.Stderr, "pyry pair: relay URL is empty (set --relay or relay_url in ~/.pyry/config.json)")` then `os.Exit(2)`. The hint names both knobs. |
| `config.Load` error | 1 | `fmt.Errorf("pair: %w", err)` → main prints `pyry: pair: <wrapped>`. |
| `devices.Load` error | 1 | `fmt.Errorf("pair: %w", err)`. |
| `identity.LoadOrCreate` error | 1 | `fmt.Errorf("pair: %w", err)`. |
| `crypto/rand.Read` error | 1 | `fmt.Errorf("pair: read random: %w", err)`. (Documented as infallible by the stdlib but explicit error path is cheaper than panic-and-trap.) |
| `registry.Save` error | 1 | `fmt.Errorf("pair: %w", err)`. |
| `pair.Render` write error | 1 | `fmt.Errorf("pair: render: %w", err)`. Plaintext token is NOT in the wrapped error — `Render`'s own contract guarantees no token in returned errors (it's just an `io.Writer` write error). |

**Token-leak discipline.** No log line from this command may contain
`plain` (the minted token), `payload` (which embeds it), or the
output of `pair.Encode(payload)`. The verb has no `slog.Logger` —
nothing in pair.go ever calls slog. All operator feedback is via
stderr human strings (the "empty relay" hint above) or the wrapped
error returned by main. The single bytes-of-plaintext escape route
is `pair.Render(payload, os.Stdout)`. This matches the discipline
spelled out in `internal/pair/render.go:23-30`.

## Testing strategy

### Unit tests — `cmd/pyry/pair_test.go`

Test only the pure pieces; everything filesystem-side is e2e-tested.

- `TestParsePairArgs` — table-driven. Cases:
  - empty args → all zero values, no error.
  - `--name=foo` → name="foo", relay="".
  - `--relay=wss://x` → name="", relay="wss://x".
  - `--name=foo --relay=wss://x` → both populated.
  - positional after flags → wraps `unexpected positional`.
  - unknown flag → returns flag.Parse error verbatim.
- `TestResolveRelay` — table-driven over the three-leg precedence:
  - flag set, all others set → flag wins.
  - flag empty, cfg set → cfg wins.
  - flag empty, cfg empty, default non-empty → default wins.
  - flag empty, cfg empty, default empty → returns "".
- `TestResolveDevicesPath` / `TestResolveServerIDPath` — quick HOME
  isolation via `t.Setenv("HOME", t.TempDir())`, asserts the joined
  path. Mirrors the existing `resolveRegistryPath`'s coverage style.

### End-to-end test — `internal/e2e/pair_test.go`

One test fulfilling AC #6. `//go:build e2e` tag, follows the
`TestVersion_E2E` no-daemon shape:

```go
func TestPair_E2E(t *testing.T) {
    home := t.TempDir()
    r := RunBareIn(t, home, "pair", "--name=test-phone")

    // 1. exit code 0
    // 2. stdout decodes via pair.Decode → Payload
    // 3. devices.json on disk contains exactly one entry
    // 4. that entry's TokenHash == HashToken(payload.Token)
    // 5. that entry's Name == "test-phone"
}
```

Path the test reads:
`<home>/.pyry/pyry/devices.json` (the binary uses the default
instance name `"pyry"` because `PYRY_NAME` is not set in the child env
and `-pyry-name` is not passed). Test parses with
`json.Unmarshal` directly into the `registryFile` envelope (or via
`devices.Load`).

Token-hash linkage verified via `devices.VerifyToken(payload.Token,
entry.TokenHash)`, which is the canonical plaintext-↔-hash check.

The auto-name path is covered by a second sub-test that omits
`--name` and asserts `entry.Name == "device-" + entry.TokenHash[:8]`.

Empty-relay (AC exit-2 path) is left to a unit test rather than an
e2e test because it's not reachable through real config — the
default constant is non-empty. The unit test for `resolveRelay`
documents the contract; an e2e test would require swapping the
default constant, which is build-time and out of scope.

### `RunBareIn` helper

```go
// RunBareIn behaves like RunBare but pins HOME to the supplied
// directory, so verbs that read ~-relative state (e.g. `pair`) can be
// driven against a t.TempDir() in isolation. Like RunBare it does NOT
// auto-inject -pyry-socket — there is no daemon spawned.
func RunBareIn(t *testing.T, home string, args ...string) RunResult { … }
```

Implementation is `RunBare` with a single line changed:
`cmd.Env = childEnv(home)`. `childEnv` already exists in harness.go
and is what `Run` uses — we reuse it verbatim.

## Open questions

1. **`-pyry-name` exposure.** The AC text mentions only `--name` and
   `--relay`. We add `-pyry-name` because the devices/server-id paths
   are per-instance. Is that the intended UX? (The alternative — make
   pair per-user — collides with the `~/.pyry/<name>/devices.json`
   convention #209 already pinned, so the per-instance answer wins
   absent further input.) **Resolved in spec.** Document the choice
   in the help text.
2. **`device-<short>` collision.** Two pairings of the same `--name`
   (or two consecutive auto-names that happen to share an 8-hex-char
   prefix — practically zero but non-zero) silently coexist in
   `devices.json` because `Add` does not enforce uniqueness. AC says
   nothing about this. Defer — current behaviour is "last write
   wins on lookup-by-name, which is fine because lookup is by token
   hash, not by name." If a future ticket adds `pyry pair revoke
   <name>`, that ticket will own the uniqueness story.
3. **Daemon staleness.** If a daemon is running with the same
   `-pyry-name`, its in-memory `devices.json` becomes stale after
   `pair`. No mitigation in #213 because no daemon-side reader of
   `devices.json` exists yet (the WS-handshake auth path is a future
   ticket). Document in PROJECT-MEMORY when implementing so the
   future ticket knows to reload-on-write.
4. **macOS keychain.** The plaintext token never touches disk in
   this verb (only the SHA-256 hex). No keychain integration needed
   here. If a future ticket wants the phone-side to keep its token
   in keychain, that's strictly client-side and out of scope.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. Two boundaries: (a) flag input
  (`--name`, `--relay`, `-pyry-name`) and (b) on-disk JSON files
  (`config.json`, `server-id`, `devices.json`). All flag values flow
  through Go's `encoding/json` (for the device entry and the wire
  payload) — no path interpolation except the instance name, which is
  sanitized (see [File operations]). All on-disk reads parse
  through bounded JSON / strict UUIDv4 validators that reject
  malformed input with an error rather than execute it.
- **[Tokens]** No MUST FIX. Token is 256-bit `crypto/rand`,
  hex-encoded; only `sha256(token)` reaches disk; no log line carries
  the plaintext (the verb has no `slog.Logger` at all). SHOULD FIX
  documented inline: `crypto/rand.Read` is treated as infallible per
  Go ≥1.20 contract — any returned error propagates as exit 1, not a
  panic. OUT OF SCOPE: revocation, rotation, expiry — owned by future
  tickets (e.g. `pyry pair revoke <name>`); the AC for #213 names
  none of these.
- **[File operations]** No findings (after revision). Path resolvers
  (`resolveDevicesPath`, `resolveServerIDPath`) MUST call
  `sanitizeName(name)` before joining, matching `resolveRegistryPath`
  and `resolveSocketPath`. Defends against `PYRY_NAME=../../etc` and
  similar. Atomic-rename writes inherited from `devices.Save` and
  `identity.LoadOrCreate` — both already documented as
  temp+chmod+fsync+rename at mode 0600. Symlink chasing on `os.ReadFile`
  paths is bounded by user ownership of `~/.pyry`; out of scope.
- **[Subprocess]** N/A. The pair verb does not exec any subprocess.
- **[Cryptographic primitives]** No findings. `crypto/rand` for token,
  `crypto/sha256` (via `devices.HashToken`) for the on-disk hash, no
  hand-rolled crypto. No key reuse — the token is a one-purpose bearer.
  Constant-time comparison is owned by `devices.VerifyToken` (#209's
  concern), not invoked from this verb.
- **[Network & I/O]** N/A. The verb performs no network I/O. The
  output sink is `os.Stdout`; `pair.Render` returns the first write
  error, which we wrap and return as exit 1.
- **[Error messages, logs, telemetry]** No findings. No `slog.Logger`
  is constructed in `cmd/pyry/pair.go`; no telemetry is emitted; the
  only operator-visible strings are wrapped errors via
  `fmt.Errorf("pair: %w", err)` and one stderr hint for the empty-relay
  case. Plaintext token never appears in any error context (verified
  by source: it lives only in the local `plain` variable and inside
  the `Payload` passed to `Render`).
- **[Concurrency]** No MUST FIX. Single-goroutine flow; locks are
  internal to `devices.Registry` and held only briefly inside its
  methods. SHOULD FIX documented inline: two concurrent `pyry pair`
  processes against the same `devices.json` race at Save → second
  wins, first's entry is dropped. Failure mode is "phone fails later
  auth," not credential leakage. Hardening (file-lock at the
  `devices.Registry` layer) is OUT OF SCOPE — this is a property of
  #209's API, not #213's wiring.
- **[Threat model alignment]** No findings. Aligned with
  `docs/protocol-mobile.md`'s security model: token is unguessable
  (256-bit), only the hash is persisted (`devices.json` § "Token
  storage"), the QR/paste output is the documented one-time display
  surface. No relay protocol surface in this ticket — the verb does
  not dial the relay; the printed payload's relay URL is consumed by
  the phone, not by this verb.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-05-09
