# Spec #782 — Daemon reloads the paired-devices registry at handshake

**Ticket:** #782 · **Size:** S · **Labels:** `security-sensitive`
**Chosen mechanism:** (a) read-through reload at handshake time — self-contained in the daemon, no coupling of `pyry pair` to a running daemon.

## Files to read first

- `internal/devices/registry.go:37-53` — `Load`'s ENOENT/empty/malformed contract. **`Reload` mirrors it exactly** (ENOENT & zero-byte → empty set, no error; malformed/other-read-error → error). Extract a shared `readDevicesFile(path) ([]Device, error)` helper if it reads cleanly; otherwise duplicate the branch logic.
- `internal/devices/registry.go:63-107` — `Save`: atomic temp+rename, sorts by (PairedAt, Name). Reconcile need **not** sort — Save re-sorts on write.
- `internal/devices/registry.go:109-179` — `Add`/`Remove`/`List`/`FindByTokenHash`/`UpdatePushRegistration`: the in-memory model + the `r.mu` discipline `Reload` must join.
- `internal/devices/auth.go:32-46` — `Validate`: the security contract (empty-token short-circuit; never logs/returns token, hash, or name). `Reload` runs **before** `Validate`, **never inside it** — do not add I/O to `Validate`.
- `internal/relay/v2session.go:1169` — the sole production `Devices.Validate(...)` call site. Insert the reload immediately before it (post-IK, pre-Validate, on the manager Run goroutine).
- `internal/relay/v2session.go:510-583` — `V2SessionConfig`: add the optional `DevicesPath string` field after `Devices` (line 526), following the "Optional: when empty …" doc idiom already used by `Handlers`/`Snapshotter`.
- `internal/relay/v2session.go:689-693` — `NewV2SessionManager`: `DevicesPath` is **optional**, so do **not** add it to the required-field validation/panic list.
- `internal/relay/handlers/register_push_token.go:80-95` — `UpdatePushRegistration` → `Save`. Insert `reg.Reload(registryPath)` (best-effort) immediately before the `Save`. Handler signature already carries `registryPath`; unchanged.
- `cmd/pyry/relay.go:123` — startup `devices.Load(resolveDevicesPath(instanceName))`; and `:308-347` — `V2SessionConfig{...}` construction: add `DevicesPath: resolveDevicesPath(instanceName)`.
- `internal/e2e/relay_v2_handshake_test.go:55-118` — `startV2Harness` + `dialPhone`; `:198-250` — `testV2HappyPath` to mirror for the new reload e2e subtests. Package helpers (`sendNoiseInit`, `readInnerFrame`, `buildHelloEarly`, `driveHandshakeToOpen`) are reusable.
- `internal/devices/registry_test.go` — existing table + concurrent-readwrite race probe to mirror for `TestReload`.
- `internal/relay/handlers/register_push_token_test.go:120-170` — existing handler-test scaffold to mirror for the clobber-regression test.

## Context

`pyry pair` runs as a **separate process**: `Load(devices.json)` → `Add` → `Save` (atomic rename). The daemon `devices.Load`s the registry **once at startup** (`cmd/pyry/relay.go:123`) and validates every handshake against that in-memory snapshot (`internal/relay/v2session.go:1169` → `Registry.Validate`). A device paired after the daemon started is on disk but absent from memory → its hello is rejected until the service restarts. Affects every relay client (mobile now, desktop next; both speak Mobile Protocol v2).

Two writers, one file. The daemon is **also** a writer of `devices.json`: `register_push_token` calls `Registry.Save` (`internal/relay/handlers/register_push_token.go:88`). Both writers rewrite the whole file via atomic rename → last-writer-wins on the entire file. A stale-in-memory daemon that Saves erases a device `pyry pair` just added. **The reload must be a reconciling read-modify-write, applied at the daemon's read (handshake) AND write (`register_push_token` Save) sites** — otherwise the fix trades the restart bug for a data-loss bug (worked scenario in § Design).

## Design

### Package structure

No new package, no new file, no schema change. One new method + one unexported helper on `internal/devices.Registry`; one optional field on `internal/relay.V2SessionConfig`; three call-site edits.

### New primitive — `internal/devices.Registry.Reload`

```go
// Reload reconciles the on-disk device set at path INTO the in-memory
// registry under r.mu, disk being authoritative for membership.
func (r *Registry) Reload(path string) error
```

Contract (mirrors `Load`'s disk-read semantics, then reconciles instead of constructing):

| Disk state | Reload result | In-memory effect |
|---|---|---|
| Missing (ENOENT) | `nil` | membership → empty (disk says none) |
| Zero-byte | `nil` | membership → empty |
| Valid list (incl. `{"devices":[]}`) | `nil` | membership → disk's set (adds newly-paired, drops revoked) |
| Malformed JSON | wrapped error | **unchanged** (fail closed) |
| Other read error (EACCES/EIO) | wrapped error | **unchanged** (fail closed) |

Reconciliation (unexported helper, keyed on `TokenHash`; ~12 lines — do **not** expand into a full method):

- For each disk device: if a same-`TokenHash` device exists in memory, **keep the in-memory struct** (preserves the `LastSeenAt` bumps `Validate` makes and never persists, and any in-flight push registration); otherwise adopt the disk struct (newly paired — has no in-memory runtime state).
- In-memory devices whose `TokenHash` is absent from disk are **dropped** (revoked via `pyry pair revoke`).
- Result order = disk order (irrelevant; `Save` re-sorts, lookups scan linearly).

Why keep-in-memory-survivor and not adopt-disk: for a single daemon, memory is always ≥ disk for the only daemon-mutated fields (`LastSeenAt`, push registration — the daemon is their sole writer, memory→disk via `Save`). `pyry pair` cannot edit an existing device's fields (it only `Add`s; there is no edit verb — revoke+re-pair mints a **new** `TokenHash`, so it reconciles as add-new + drop-old). See Open Questions for the future-edit-verb caveat.

Lock discipline: read `path` **outside** `r.mu` (I/O off the lock, as `Save` does), then take `r.mu` for the reconcile-and-assign. `Reload` never nests locks and never calls back into a locked path — `r.mu` is the single mutex already guarding the slice. Concurrent `Reload`/`Validate`/`Save` from different goroutines serialise at `r.mu`; each is internally consistent (set-based, disk-derived), so interleave is safe.

**Optional refactor:** extract `readDevicesFile(path) ([]Device, error)` returning `(nil,nil)` for ENOENT/empty, `(nil,err)` for malformed/other-read-error, `(devs,nil)` for success — shared by `Load` (construct) and `Reload` (reconcile). Behaviour-preserving for `Load`; skip if it complicates the diff.

### Wiring — v2 handshake (the production read path)

`V2SessionConfig` gains:

```go
// DevicesPath is the on-disk devices.json path used to reload the device
// set at each handshake (#782). Optional: "" disables the reload — the
// handshake validates against the startup-loaded in-memory set only
// (keeps existing tests byte-stable; mirrors the claudeSessionsDir=="" idiom).
DevicesPath string
```

At `v2session.go:1169`, immediately before `Devices.Validate`:

- If `m.cfg.DevicesPath != ""`, call `m.cfg.Devices.Reload(m.cfg.DevicesPath)`.
- On error: **log at Warn and proceed to `Validate` against the retained in-memory set** (fail closed — accept set not widened, loaded devices not lost). See § Error handling for the log-hygiene constraint.
- `Validate(helloPayload.Token)` runs unchanged afterward.

`cmd/pyry/relay.go:308` construction adds `DevicesPath: resolveDevicesPath(instanceName)` (the same path `startup Load` and `register_push_token` use).

### Wiring — `register_push_token` (the write path clobber-guard)

Between `UpdatePushRegistration` (line 80) and `Save` (line 88), insert a best-effort `reg.Reload(registryPath)`:

- On success: memory now includes any device `pyry pair` added since the last read, so the subsequent whole-file `Save` does not erase it.
- On error: log at Warn and **still proceed to `Save`** (self-heal — write known-good in-memory state over a corrupt/unreadable file; no worse than the pre-#782 blind Save).
- Do **not** touch the dedupe early-return path (line 72-78) — it does not `Save`, so it cannot clobber.

Why the write path also needs the reload (worked scenario — this is the "trades restart for data-loss" bug the reload-at-handshake **alone** does not fix):

1. Daemon memory = `[A]`. 2. `pyry pair` adds B → disk `[A,B]`. 3. Device A (live conn since startup) sends `register_push_token` **before B ever handshakes** → `UpdatePushRegistration(A)` → `Save` writes `[A]` → **B erased from disk**. 4. B handshakes → reload reads disk `[A]` → **B rejected**. The restart bug re-manifests as data loss. Reload-before-`Save` at step 3 merges disk `[A,B]` into memory first → `Save` writes `[A_push,B]` → B survives.

`register_push_token` is the **only** in-daemon `Save` site (`Validate` never persists; grep-confirmed: sole `Devices.Save` callers are `pyry pair` and this handler).

### v1 path — out of scope (deliberate)

The v1 dispatch leg (`cmd/pyry/relay.go:163-221`) is deprecated ("no shipping client speaks v1"). The handshake reload is wired to v2 only. The `register_push_token` clobber-guard lives **inside** the shared handler, so it covers v1's write path automatically. Reviving v1's handshake reload later is a one-line mirror in `authGate`; not built now (Simplicity First / Evidence-Based).

## Concurrency model

- `Reload` at the v2 handshake runs on the **manager Run goroutine** (single fan-in; `v2session.go` § "Run is the only goroutine the manager owns"). One reload per connect — a small JSON read; bounded, non-hot. Holding nothing but a local before taking `r.mu` for the reconcile.
- `Reload` in `register_push_token` runs on the **dispatch per-conn goroutine**. It and the manager-goroutine reload serialise at `r.mu`. No new lock, no nesting, no deadlock.
- The captured `dev := c.Auth()` pointer in `register_push_token` is a snapshot **copy** (`s.device = &device` where `device` is `Validate`'s by-value return), not a pointer into `r.devices` — `Reload` rebuilding the slice does not invalidate it.
- Live-conn revocation propagation is explicitly **out of scope** (auth.go already documents "revocation propagation to live conns is a separate concern"). Reload reflects revoke/pair on the **next** handshake — exactly the "reflects unpair for free" consequence.

## Error handling

- **Malformed / unreadable disk after startup (AC4):** `Reload` returns a wrapped error and leaves memory untouched; the handshake proceeds to `Validate` against the retained set → a startup-known device still authenticates, no unpaired client is accepted.
- **Missing / empty-list file:** reconcile to empty membership (no error) — disk-authoritative. `pyry pair revoke <all>` writes a **valid** `{"devices":[]}` (not ENOENT), so the "reflect unpair" path is a normal success. ENOENT only arises from manual deletion; treating it as "no devices" mirrors `Load` and is a coherent (if drastic) consequence of disk authority — see Open Questions.
- **Reload failure logging (SECURITY MUST — see § Security review):** the reload-failure log lines (v2 handshake + `register_push_token`) MUST record `path` and a static reason only. They MUST NOT include the raw wrapped error value — a `json.Unmarshal` failure on a corrupt file can carry file bytes, which may include a `token_hash`. `Registry.Validate`'s contract (never logs token/hash/name) extends to every new site the reload introduces.

## Testing strategy

Scenarios (developer writes them in the repo's table-driven, stdlib-only idiom; `go test -race`):

**`internal/devices/registry_test.go` — `TestReload` (table + one race probe):**
- Added device: Load `[A]`; write `[A,B]` to disk; `Reload`; `Validate(tokenB)` hits, `Validate(tokenA)` still hits.
- Removed device: Load `[A,B]`; write `[A]`; `Reload`; `Validate(tokenB)` misses, `Validate(tokenA)` hits. (AC: reflect revoke.)
- Preserves in-memory `LastSeenAt`: Load `[A]`; `Validate(tokenA)` to bump; write `[A]` to disk with an older/zero `LastSeenAt`; `Reload`; assert in-memory A retains the bumped value (survivor kept, not disk-clobbered).
- Malformed → no loss (AC4): Load `[A]`; write malformed bytes; `Reload` returns error; `Validate(tokenA)` still hits; a never-paired token still misses (accept set not widened).
- Unreadable → no loss (AC4): Load `[A]`; force a read error (point `path` at a directory — deterministic, CI-safe); `Reload` returns error; `Validate(tokenA)` still hits.
- Missing → empties (documented): Load `[A]`; delete file; `Reload` nil; `Validate(tokenA)` misses.
- Empty-list → empties (revoke-all): Load `[A]`; write `{"devices":[]}`; `Reload` nil; `Validate(tokenA)` misses.
- Race probe: concurrent `Reload` + `Validate` goroutines, `-race` clean.

**`internal/relay/handlers/register_push_token_test.go` — clobber regression:**
- Seed registry `[A]` (authed as A); write `[A,B]` to the same on-disk path (simulating `pyry pair` adding B post-load); invoke the handler with a push-token change for A; assert the persisted file is `[A_push, B]` — **B not erased**. This is the core data-loss guard.

**`internal/e2e/relay_v2_handshake_test.go` (`//go:build e2e`) — two new subtests (AC1, AC4 at the handshake):**
- `reload_picks_up_newly_paired` (AC1 end-to-end): build the manager with `DevicesPath` = a temp `devices.json` seeded `[A]`; after the harness is up, **rewrite the file to `[A,B]` on disk** (what `pyry pair` does — a separate process); dial a phone and drive the v2 handshake with **token B**; assert a `noise_resp` → `hello_ack{v2}` accept. Mirror `testV2HappyPath`.
- `reload_malformed_fails_closed` (AC4): manager with `DevicesPath` and startup registry `[A]`; corrupt the on-disk file; a handshake with an **unpaired** token → 4401 reject (not widened); a handshake with **token A** → accept (retained set).
- To set `DevicesPath` without a 5-call-site cascade through `startV2Harness`, write the reload subtests as self-contained functions reusing the package helpers (`fakerelay`, `dialPhone`, `sendNoiseInit`, `readInnerFrame`, `buildHelloEarly`), OR thread an optional `devicesPath` param through `startV2Harness` — developer's discretion; prefer the former to avoid the fixture cascade.

AC coverage map: AC1 → e2e `reload_picks_up_newly_paired` + `TestReload` added-device. AC2 → `TestReload` malformed (accept set not widened) + `Validate` exact-match unchanged. AC3 → startup `Load` untouched; `TestReload` missing/empty. AC4 → `TestReload` malformed/unreadable no-loss + e2e `reload_malformed_fails_closed`.

## Security review (label-gated: `security-sensitive`)

Ran the § security-review pass on this spec. Categories walked:

- **Trust boundary — accept-set integrity (AC2).** The only membership mutation is `Reload`, which sets membership to disk's **exact** device set (add-newly-paired, drop-revoked); it never fabricates entries. `Validate` stays exact-match by `HashToken(plain)` with the empty-token short-circuit intact. A hello with a token absent from disk is rejected. **PASS.**
- **Fail-closed on reload failure (AC4).** `Reload` on malformed/unreadable returns an error and performs **no** in-memory mutation; the handshake continues against the retained set. No path widens the accept set on a read failure, and no loaded device is lost. Enforced structurally (mutation happens only on the success branch, after a clean parse). **PASS.**
- **Secret non-leakage (MUST-FIX, now specified).** The reload introduces two new log sites (v2 handshake reject-to-Warn, `register_push_token` reject-to-Warn). A `json.Unmarshal` error on a corrupt `devices.json` can echo file bytes that include a `token_hash`. → Constraint pinned in § Error handling: these log lines carry `path` + a **static** reason only, never the raw wrapped `err`. Token/hash/name never enter a log or error at any new site; `Validate`'s contract is untouched (reload is called before it, never inside). **PASS with the constraint.**
- **Cross-process TOCTOU.** `pyry pair` is a separate process; no shared lock is possible without a file-lock mechanism (out of scope — no schema/mechanism change). Reload narrows the last-writer-wins window dramatically (reconcile-before-write) but does not close it; the next reload reconciles any addition that raced a `Save`. Same last-writer-wins baseline as today — no new vulnerability. **PASS.**
- **DoS via per-connect reload.** One bounded JSON read per handshake, on the single manager goroutine, gated on `DevicesPath!=""` and only on the post-IK `noise_init` path (attacker already completed an IK handshake to reach it). No meaningful new DoS surface. **PASS.**

**Verdict: PASS** (the secret-non-leakage constraint is a spec requirement the developer must honor, and the tests should assert no hash appears in the reject log if a hook is available).

## Open questions

- **ENOENT-after-startup → empty set.** Chosen to mirror `Load` and because `revoke` writes a valid empty-list file (never ENOENT), so only manual deletion trips it. If operators find "deleting devices.json unpairs everyone silently" surprising, a future refinement could treat ENOENT-after-a-nonempty-load as fail-closed-keep. Deferred — no observed failure, and it would diverge `Reload` from `Load`.
- **Future device-edit verb.** The keep-in-memory-survivor reconcile assumes an existing device's disk fields never change under a stable `TokenHash` (true today — no edit verb). If a `pyry pair edit` is ever added, `Reload` must revisit whether disk or memory wins per field. Noted for the eventual consumer.
