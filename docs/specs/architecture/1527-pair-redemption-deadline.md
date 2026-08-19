# #1527 — stamp a redemption deadline on newly minted pairing records

**Size:** XS · **Ticket:** [#1527](https://github.com/pyrycode/pyrycode/issues/1527) · Split from #1509 · Followed by #1528 (record first redemption), #1529 (enforce)

## Files to read first

| File | Symbol | What to extract |
|---|---|---|
| `internal/devices/device.go` | `Device` | The struct the field joins; the existing optional-field doc-comment style (`Platform`, `PushToken`, `AllowRemotePermissions`) |
| `internal/devices/device.go` | package doc comment | The token-secrecy contract this slice must not weaken |
| `internal/devices/registry.go` | `readDevicesFile`, `Load`, `Save` | Plain `json.Unmarshal` (no `DisallowUnknownFields`) + the sort-then-encode write path — why the field is bidirectionally tolerated |
| `internal/devices/registry.go` | `reconcileDevices` | Whole-struct substitution keyed on `TokenHash` — why no reconcile change is needed here, and why #1528 will have to revisit it |
| `internal/devices/auth.go` | `Validate` | The predicate that must stay a pure hash lookup this slice |
| `cmd/pyry/pair.go` | `runPairDefault` | The sole non-test `devices.Device` construction site; the `registry.Add` → `registry.Save` → `pair.Render` order |
| `cmd/pyry/pair.go` | `renderPairList` | The four-column formatter — proves the new field surfaces in no column |
| `cmd/pyry/pair.go` | `runPairRevoke` | Load → `Remove` → `Save`; survivors are re-encoded from their loaded structs |
| `internal/relay/v2session_handshake.go` | the `Devices.Validate` call in `handleNoiseInit` | The single handshake read of the registry; nothing here may consult the new field |
| `internal/devices/device_test.go` | `TestDevice_AllowRemotePermissionsRoundTrip`, `TestDevice_OmitsAllowRemotePermissionsWhenFalse`, `TestDevice_DecodeLegacyDiskShape` | The three-shape JSON test precedent to mirror, and the legacy fixture JSON to reuse verbatim |
| `internal/devices/registry_test.go` | `TestRegistry_AllowRemotePermissionsPersists`, `mustParseTime` | The `Save`→`Load` round-trip + hand-authored pre-field envelope pattern |
| `internal/devices/auth_test.go` | `TestRegistry_Validate_Hit` | The registry-fixture shape for a `Validate` hit |
| `cmd/pyry/pair_test.go` | `TestRunPairDefault_AllowRemotePermissionsPersists`, `captureStdout` | Isolated-`HOME` end-to-end mint harness |
| `cmd/pyry/pair_test.go` | `TestRenderPairList_TwoDevices`, `TestRunPairRevoke_RemovesEntry` | The formatter golden and the revoke harness |
| `internal/relay/v2session_test.go` | `v2PairedRegistry`, `startManager`, `v2TestToken`, `v2TestServerID`, `genV2Keypair`, `silentLogger`, `v2Recorder` | The manager-wiring helpers the new relay test composes |
| `internal/relay/v2session_modal_test.go` | `openModalConn` | Drives a conn through the real v2 handshake to `open`; the accept-path driver for the inertness pin |
| `docs/knowledge/features/devices-package.md` | § "Surface" | The prose contract for `Device`'s JSON tags — states the optional-fields-keep-zero-off-disk rule this field follows |
| `docs/PROJECT-MEMORY.md` | § "Project-level conventions" | `time.Time` round-trip discipline — compare with `time.Time.Equal`, never `==`/`reflect.DeepEqual` |

## Context

`pyry pair` mints a 256-bit token, persists its SHA-256 hash, and renders the QR. `Validate` is a pure hash lookup with no expiry and no first-use check, so a token that was displayed but never scanned — left in a terminal scrollback, screenshotted, shoulder-surfed off a screen — authenticates indefinitely. Bounding that window needs a durable "this record has not been redeemed yet, and stops being acceptable at T" marker on disk.

`LastSeenAt` cannot serve as that marker. `Validate` advances it **in memory only** and its doc comment states the non-persistence contract explicitly. The only writers of `devices.json` are `pyry pair` (mint), `pyry pair revoke`, and the `register_push_token` handler. A device that never registers a push token shows `last_seen_at` as the zero value on disk forever regardless of how heavily it is used, so keying anything on `LastSeenAt.IsZero()` would reject devices in daily use.

This slice adds the marker and its single writer. **Nothing reads it.** There is no behaviour change at the handshake and no way for this slice alone to lock anyone out.

## Design

### The field

One new field on `devices.Device`, declared beside the three existing optional fields:

```go
RedeemBy time.Time `json:"redeem_by,omitzero"`
```

`RedeemBy` is the instant at which an **unredeemed** pairing record stops being acceptable. The zero value means "no deadline" — that is what a pre-#1527 record decodes to, and what a record whose deadline was never stamped means. `RedeemBy` says nothing about a device that has already been redeemed; distinguishing redeemed from unredeemed is #1528's field, and acting on either is #1529's.

**The tag is `omitzero`, not `omitempty`, and this is load-bearing.** `omitempty` on a `time.Time` is a silent no-op: `encoding/json` omits empty scalars, maps and slices, never a struct. Copying the `Platform` / `PushToken` / `AllowRemotePermissions` tag onto a timestamp would look correct and do nothing, writing `"redeem_by":"0001-01-01T00:00:00Z"` into every legacy record on the next `Save`. Go 1.26.2 (`go.mod`) has `omitzero`, which consults `time.Time`'s `IsZero()` and does what the three sibling fields' tags do for their types: keeps the zero off disk, so an existing `devices.json` round-trips through load → save unchanged. A test pins the tag choice directly (see `TestDevice_RedeemByJSONShape` below) — it goes red on `omitempty`.

Naming note: not `ExpiresAt`. The deadline bounds the *unredeemed* window, not the device's lifetime; a redeemed device keeps authenticating past `RedeemBy`. `RedeemBy` says which of the two it is, and stays honest once #1528 and #1529 land.

### The window constant

Declared in `internal/devices`, in `device.go` beside the field:

```go
const RedemptionWindow = 15 * time.Minute
```

Type is `time.Duration`. Reads `devices.RedemptionWindow` at the call site — no stutter, no second definition. It is the only place the 15 minutes appears; the mint site and the test both derive from it rather than restating it.

### The writer

`runPairDefault` is the only non-test site that constructs a `devices.Device`; every other writer mutates an existing record in place (`Registry.UpdatePushRegistration`, reached from the `register_push_token` handler). Stamping there is therefore complete — no second mint path can leave a record deadline-less.

The contract at that site: **`PairedAt` and `RedeemBy` derive from a single clock read.**

```go
mintedAt := time.Now().UTC()
// ... registry.Add(devices.Device{ …, PairedAt: mintedAt,
//                                  RedeemBy: mintedAt.Add(devices.RedemptionWindow), … })
```

The current code calls `time.Now().UTC()` inline in the composite literal. Hoisting it to a local is the whole production change beyond the one field. It matters because it makes `RedeemBy.Sub(PairedAt) == devices.RedemptionWindow` **exactly** true, so AC3's test asserts equality rather than a tolerance window — a deterministic assertion instead of a flaky one. Two separate `time.Now()` calls would differ by a scheduling hiccup and force the test into `if diff < window-slop || diff > window+slop`, which is both weaker and a flake source.

Everything else in `runPairDefault` is untouched: the load-before-mint ordering (ADR 021), the `Save`-before-`Render` ordering, and the error/exit-code contract all stand.

### Inertness — the mechanical criterion

No read of `RedeemBy` is added anywhere. After this slice, `git grep -n RedeemBy -- '*.go'` outside `_test.go` files returns **exactly two** hits: the declaration in `internal/devices/device.go` and the stamp in `cmd/pyry/pair.go`. `Validate`, `reconcileDevices`, `renderPairList`, `Remove`, `UpdatePushRegistration`, `FindByTokenHash` and the v2 handshake are all unchanged. That two-hit count is the cheapest check that the slice did not silently start enforcing; the tests below pin the behavioural side of the same claim.

### Data flow

```
pyry pair
  └─ runPairDefault: mintedAt := now
       └─ registry.Add(Device{ PairedAt: mintedAt, RedeemBy: mintedAt+RedemptionWindow, … })
            └─ registry.Save(devices.json)          ← redeem_by reaches disk here

daemon handshake (unchanged)
  └─ Devices.Reload(devicesPath)
       └─ readDevicesFile → json.Unmarshal          ← redeem_by decodes, rides along
            └─ reconcileDevices (whole-struct substitution on TokenHash)
                 └─ Devices.Validate(token)         ← never consults RedeemBy
```

`reconcileDevices` substitutes the **whole** in-memory struct for any disk record whose `TokenHash` matches, rather than merging field by field, so the new field needs no reconcile change: a newly-paired device is adopted from disk carrying its deadline, and an already-loaded device keeps the struct it was loaded with. (This same whole-struct rule becomes a real design question for #1528 — see Open questions.)

### Compatibility

Bidirectional, and neither direction needs code. `readDevicesFile` uses plain `json.Unmarshal` with no `DisallowUnknownFields`, so an old binary reading a new file ignores `redeem_by`, and a new binary reading an old file gets the zero value. Because the tag is `omitzero`, a new binary re-Saving a legacy record does not add the key, so `devices.json` gains no churn from records this slice did not mint.

A downgrade to a binary predating the field re-Saves records without it, which **fails open** — the token stops expiring, i.e. reverts to today's behaviour. That is stated in the field's doc comment and deliberately not defended against in code.

`devices.Device` is never marshalled across the wire, so this is a disk-schema change only. No protocol impact, no `docs/protocol-mobile.md` edit.

## Concurrency model

No new goroutines, no new locks, no new lock ordering. The field is written inside the existing `Registry.Add` critical section (`r.mu`) and serialized by the existing snapshot-then-write discipline in `Save` (ADR 020). `Validate`'s critical section is untouched. `Device` stays comparable (`time.Time` is comparable), so the existing `out != in` equality assertions in `device_test.go` continue to compile and pass.

## Error handling

No new failure modes and no new error values. A malformed `redeem_by` in a hand-corrupted `devices.json` fails inside the existing `json.Unmarshal`, surfacing through `readDevicesFile`'s existing path-only wrapped error — which, per that function's SECURITY comment, must keep wrapping the path and never the file bytes. Nothing about that changes.

Nothing logs `RedeemBy`. It is not secret, but adding it to no log line keeps the diff at two production sites.

## Testing strategy

Scenarios, not code. Table-driven where the shape allows, `t.Parallel()` where the surrounding file already uses it.

**`time.Time` discipline (applies to every scenario below).** Per `docs/PROJECT-MEMORY.md` § Project-level conventions, monotonic-clock readings strip on JSON marshal — compare `time.Time` fields with `time.Time.Equal`, never `==` or `reflect.DeepEqual`. The existing `out != in` whole-struct comparisons in `device_test.go` are safe only because their fixtures come from `time.Date`, which carries no monotonic reading. Any new fixture built from `time.Now()` must be compared field-wise with `.Equal`.

### `internal/devices/device_test.go`

- **`TestDevice_RedeemByJSONShape`** — table over two rows:
  - `RedeemBy` set to a `time.Date` fixture → encoded form contains a `"redeem_by"` key, and unmarshalling returns a `RedeemBy` that `.Equal`s the input.
  - `RedeemBy` zero → encoded form contains **no** `"redeem_by"` key, and unmarshalling leaves `RedeemBy.IsZero()`.

  The second row is the tag pin: it is red under `json:"redeem_by,omitempty"` and green under `omitzero`.
- **`TestRedemptionWindow`** — `devices.RedemptionWindow == 15 * time.Minute`. AC3's value pin; one assertion, goes red if the constant is retuned without a deliberate ticket.

### `internal/devices/registry_test.go`

- **`TestRegistry_RedeemByPersists`** — mirrors `TestRegistry_AllowRemotePermissionsPersists`:
  - Two devices, one with `RedeemBy` set (from `mustParseTime`), one without. `Save` → `Load` → the set one comes back `.Equal` to the seeded value, the unset one comes back `.IsZero()`.
  - Then a hand-authored pre-field envelope (reuse the inline `{"devices":[{"token_hash":…,"name":…,"paired_at":…,"last_seen_at":…}]}` JSON from the `AllowRemotePermissions` test) loads with no error, `RedeemBy.IsZero()`, **and every other field on that record intact** — assert `TokenHash`, `Name`, `PairedAt`, `LastSeenAt` individually. That last clause is AC2's, and the precedent test does not check it; do not assume it falls out.

### `internal/devices/auth_test.go`

- **`TestRegistry_Validate_IgnoresExpiredRedeemBy`** — one device whose `RedeemBy` is an hour in the past. `Validate(plain)` returns `(device, true)`.

  **Assert the fixture's own precondition too:** the returned `Device.RedeemBy` is non-zero and `.Before(time.Now())`. Without that, a fixture that silently carried a zero `RedeemBy` would pass this test *and* pass any future enforcement check, making the pin vacuous — the deadline would look inert because it was never set.

### `internal/relay/v2session_redeemby_test.go` (new file)

- **`TestV2Session_ExpiredRedeemBy_StillHandshakes`** — the handshake leg of AC4.
  - Build a `*devices.Registry` inline (do not modify the shared `v2PairedRegistry` helper, whose current shape other tests depend on) holding one device for `v2TestToken` with `RedeemBy` an hour in the past.
  - Wire a manager with `startManager` + `genV2Keypair` + `silentLogger` + a `v2Recorder`, exactly as `TestV2Session_SlowHandler_DoesNotStallOtherConn` does.
  - `openModalConn` drives the real v2 handshake to `open`; reaching `open` without a `4401` is the assertion.
  - Same non-vacuity guard: re-read the device via `reg.FindByTokenHash` and assert its `RedeemBy` is still the back-dated value.

  New file rather than appending to `v2session_test.go` — the package already splits its tests by concern (`v2session_appframe_test.go`, `v2session_modal_test.go`, …), and a new file keeps the merge surface on that 4700-line file at zero.

### `cmd/pyry/pair_test.go`

- **`TestRunPairDefault_StampsRedeemBy`** — isolated `HOME` via `t.TempDir()` + `t.Setenv`, `captureStdout(runPairDefault(nil))`, then `devices.Load(resolveDevicesPath(defaultName()))`. Assert exactly one record, `!RedeemBy.IsZero()`, and `RedeemBy.Sub(PairedAt) == devices.RedemptionWindow` — exact equality, which is what the single-clock-read contract buys.
- **`TestRenderPairList_RedeemByInvisible`** — AC5's byte-unchanged pin, as a differential rather than a golden: render one device with `RedeemBy` zero, render the same device with `RedeemBy` set, assert the two outputs are byte-identical via `bytes.Equal`. Stronger than a golden string (it cannot pass by someone updating the expected bytes) and immune to tabwriter padding drift.
- **`TestRunPairRevoke_PreservesRedeemByOnSurvivors`** — mirror `TestRunPairRevoke_RemovesEntry`'s harness. Seed a `devices.json` with two records, both carrying distinct `RedeemBy` values; revoke one; reload; assert the revoked record is gone and the survivor's `RedeemBy` `.Equal`s its seeded value (alongside its other fields).

### Mutation checks the developer should run before opening the PR

Each mutation must turn a specific named test red. Run via `go test -overlay=<abs-path json>` so nothing is written into the worktree.

| Mutation | Must redden |
|---|---|
| `json:"redeem_by,omitzero"` → `json:"redeem_by,omitempty"` | `TestDevice_RedeemByJSONShape` (zero row), `TestRegistry_RedeemByPersists` |
| Drop `RedeemBy:` from the `registry.Add` literal in `runPairDefault` | `TestRunPairDefault_StampsRedeemBy` |
| `RedemptionWindow` → `30 * time.Minute` | `TestRedemptionWindow`, `TestRunPairDefault_StampsRedeemBy` |
| Add `if !d.RedeemBy.IsZero() && time.Now().After(d.RedeemBy) { return Device{}, false }` to `Validate` | `TestRegistry_Validate_IgnoresExpiredRedeemBy`, `TestV2Session_ExpiredRedeemBy_StillHandshakes` |
| Add a `REDEEM BY` column to `renderPairList` | `TestRenderPairList_RedeemByInvisible` |

The fourth row is the one that matters most: it is the enforcement this slice must **not** have, and it must redden both the unit and the handshake pin. If it reddens only one, the other pin is vacuous.

## Open questions

- **`reconcileDevices` makes memory authoritative for non-membership fields.** A disk record whose `TokenHash` is already in memory is discarded in favour of the in-memory struct. Inert here (nothing mutates an existing record's `RedeemBy`), but #1528 records a *first redemption* on disk, and a running daemon would mask a disk-side stamp written by another process behind its own in-memory copy. #1528 has to decide whether to merge that field specifically or make disk authoritative for it. Flagging, not solving.
- **Clock skew at mint time.** `RedeemBy` is wall-clock arithmetic. A machine with a badly-wrong clock at `pyry pair` time gets a wrong deadline. Not remotely triggerable (mint is a local CLI action by the operator), and a monotonic-clock deadline cannot survive a process restart, which is the whole point of persisting it. Accepted; no clock abstraction anywhere in this slice, and tests back-date by constructing the field directly.
- **15 minutes is a guess, not a measurement.** It is the AC's number and it is the only value in the constant, so retuning it later is a one-line change plus `TestRedemptionWindow`. Whether it survives contact with a real pairing flow (an operator who prints the QR, walks to another room, then scans) is #1529's problem, when the number first has teeth.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. The one boundary this slice touches is `readDevicesFile` — untrusted file bytes → `[]Device` — and it is unchanged: a single `json.Unmarshal` at one call site, feeding `Load` and `Reload`. The new field crosses that boundary as a `time.Time` decoded by stdlib, with no parsing of its own and no branch keyed on it anywhere. The trust boundary at the WS perimeter (`Validate`) is likewise untouched; the two-hit `git grep RedeemBy` criterion in § Inertness is the mechanical statement that no new code holds this value.
- **[Tokens, secrets, credentials]** No findings on generation, storage or logging — `crypto/rand` mint, SHA-256-at-rest, and the package's never-log-the-plain contract are all unchanged; `RedeemBy` is a timestamp, not secret, and appears in no log line and no error. On lifecycle: this slice deliberately delivers **only the expiry marker**, leaving expiry itself unenforced. That is a partial mitigation by construction, and the honest statement is that the printed-but-never-scanned window is still unbounded after this ticket ships — the bound arrives with #1529. Creation, storage and revocation (per-device, via `pyry pair revoke`, propagated to a running daemon by the handshake-time `Reload`) are already addressed and unchanged.
- **[File operations]** No findings. No new path is constructed and no user input reaches a path — `resolveDevicesPath` already sanitizes the instance name against `PYRY_NAME=../../etc`, and this slice adds no caller. The write path is the existing `Save`: `os.CreateTemp` in the target dir, `chmod 0600`, encode, `Sync`, `Close`, `Rename`, with the parent dir at `0700`. No check-then-use is introduced, so no new TOCTOU. One property worth naming as *preserved rather than assumed*: `omitzero` means a legacy record survives a re-`Save` byte-identically, so this change adds no rewrite of records it did not mint.
- **[Subprocess / external command execution]** Not applicable — no `exec.Command`, no subprocess, no environment handling in either changed file.
- **[Cryptographic primitives]** Not applicable — no primitive is added, chosen, or reconfigured. The field is not an input to `HashToken`, `VerifyToken`, or the Noise handshake, and it does not participate in any comparison against attacker-controlled data, so the constant-time question does not arise. (When it does, in #1529, the comparison is timestamp-vs-clock, not secret-vs-attacker-input, and `subtle` is the wrong tool there.)
- **[Network & I/O]** No findings. No socket read, no size cap, no timeout, no TLS setting is added or changed. `devices.Device` is never marshalled across the wire, so the on-disk schema change has no wire consequence and `docs/protocol-mobile.md` needs no edit.
- **[Error messages, logs, telemetry]** No findings. No new error value and no new log call. The one pre-existing hazard in the blast radius — `readDevicesFile`'s SECURITY comment, which forbids wrapping file bytes into the error because a corrupt `devices.json` may embed a `token_hash` — is called out in § Error handling so a developer adding a "malformed redeem_by" diagnostic does not reintroduce it. That is the specific way this ticket could have leaked, and the spec names it.
- **[Concurrency]** No findings. No goroutine, no lock, no lock ordering is added; the field is written inside `Registry.Add`'s existing critical section and read by nothing. Shutdown safety is unchanged — `Save`'s temp-file-plus-rename means a process killed mid-write leaves the previous `devices.json` intact, and a record whose `RedeemBy` never reached disk simply has no deadline, which is the pre-#1527 status quo (fail open, not fail closed, and deliberately so at this slice).
- **[Threat model alignment]** Partially addressed by design, with the remainder named. `docs/protocol-mobile.md` § Security model threat #4 (*token leak via phone*, severity medium, mitigated by per-device revocation) is the closest existing entry; the failure this ticket targets is adjacent and not currently enumerated there — a token leaked at **display** time (scrollback, screenshot, shoulder-surfed QR) rather than from the phone, which today authenticates forever because `Validate` has no expiry. This slice contributes the durable marker; #1529 converts it into an actual mitigation. Updating § Security model is correctly *not* this ticket's job — there is nothing to claim until enforcement exists, and adding the threat entry alongside the enforcement in #1529 keeps the doc from overstating the binary's posture.
- **[Threat model alignment — accepted residual risk]** A downgrade to a pre-#1527 binary re-Saves records without `redeem_by` and the token stops expiring. Fails **open**, to exactly today's behaviour. Not defended in code: the attack requires local write access to swap the operator's own binary, at which point the deadline is not the weakest link. Documented in the field's doc comment so the next reader does not mistake it for an oversight.
- **[Concurrency — OUT OF SCOPE → #1528]** `reconcileDevices` substitutes the whole in-memory struct for a matching disk record, so a running daemon masks disk-side field updates for devices it has already loaded. Harmless this slice (nothing writes `RedeemBy` on an existing record). #1528 writes a first-redemption stamp to disk and must decide whether to merge that field or make disk authoritative for it; carried in § Open questions.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-19
