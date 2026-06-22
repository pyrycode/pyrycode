# Spec #702 — remote-permission gate: per-device authorization bit + `--allow-remote-permissions` (default OFF)

**Ticket:** #702 — feat(security): remote-permission gate — `pyry pair --allow-remote-permissions` (default OFF)
**Size:** S (PO-sized S; architect confirms S — 3 production files, 1 new exported type, ~0 consumer cascade; see § Scope). Considered XS and held S for the eligibility×outcome test matrix + the required security pass.
**Epic:** #597 (Phase 3 — remote modal). This ticket owns the **authorization primitive**. Siblings: **#701** (modal wire types — merged), **#703** (modal control loop — mints/emits modals, routes/validates the inbound answer, owns the deny-on-timeout *timer*; **the consumer of this primitive**), **#706** (two-heads ownership), **#712** (audit log — the decision-logging primitive, peeled out of this ticket).
**Security-sensitive:** **yes** (label present). Inline security pass at § Security review (verdict **PASS**) — required before commit per the label gate. `agents/architect/security-review.md` is not synced into this worktree; the pass uses the standard adversarial categories per the #701/#487/#209 precedent.

---

## Files to read first

Generated from `codegraph_context` + the reads done during this spec; off-topic hits pruned. Every addition mirrors an existing precedent in these exact files — copy the precedent, don't invent.

- `internal/devices/device.go:24-43` — the `Device` struct + the `Platform`/`PushToken` `omitempty` precedent (the exact shape the new `AllowRemotePermissions bool` field follows) and the **package SECURITY contract** (lines 4-10): never log/serialize the plain token. The new code touches no token — confirm compliance.
- `internal/devices/auth.go` (whole file, 47 lines) — the existing device-authorization home (`Registry.Validate`, the WS-perimeter auth predicate) and its **SECURITY doc voice** (never log plain/hash/name; the returned bool is the only signal). The new eligibility predicate + fail-closed decision land here, beside `Validate`, in this voice.
- `internal/devices/device_test.go:102-173` — three test templates to mirror: `TestDevice_PopulatedRoundTrip` (true-case JSON round-trip), `TestDevice_LegacyOmitsPushFields` (encoded form omits an empty `omitempty` key), `TestDevice_DecodeLegacyDiskShape` (a pre-field on-disk record decodes the new field to its zero value). The last is the **exact AC4 "pre-field → OFF" template**.
- `internal/devices/registry.go:37-107` — `Load` / `Save` (atomic temp+rename, 0600/0700). The field rides this unchanged; the AC4 registry round-trip test drives `Save`→`Load`.
- `internal/devices/registry_test.go:160-240` — the registry round-trip + table-driven `wantOK` patterns to mirror for the AC4 Save→Load persistence test.
- `cmd/pyry/pair.go:73-100` — `pairArgs` struct + `parsePairArgs` flagset (where the `--allow-remote-permissions` bool flag registers). `cmd/pyry/pair.go:158-225` — `runPairDefault` + the `devices.Device{...}` literal (~205) to thread the bit into; the usage string (~163) to extend.
- `cmd/pyry/pair_test.go:22-64` — `TestParsePairArgs` table (add the flag cases here). `cmd/pyry/pair_test.go:615-660` — `TestRunPairDefault_PopulatesStaticPubkey` + `decodeRenderedPayload` (the HOME=tmp `runPairDefault` integration harness the AC1/AC4 end-to-end test reuses; load the resulting `devices.json` and assert the bit).
- `internal/relay/v2session.go:245-262` — **read-only context.** The per-session `device *devices.Device` snapshot and the `interactive bool` field whose doc states "the zero value (false) is the fail-closed default for every other path." This is the design precedent for the gate (zero value = safe default) **and** shows how #703 reaches the bit. The bit is also surfaced per-conn via `dispatch.Conn.Auth() *devices.Device` (`internal/dispatch/dispatch.go:82-93`). **You do not edit either file** — #703 consumes the primitive there.
- `docs/knowledge/decisions/025-mobile-remote-head-interactive-session.md` § "Security model — remote permission granting" (lines 134-145) — the canonical model this ticket implements: per-device opt-in (default OFF), deny-on-timeout, only *answering* is gated. Decision 3 (line 55) is the load-bearing accepted decision.
- `docs/specs/architecture/701-modal-wire-types.md` § "Producer obligations" (line 248) — confirms #701 explicitly defers the per-device answer gate (item d) to #702; the wire carries no capability field, so the gate is a **stored authorization**, not a wire bit.

---

## Context

Phase 3 of epic #597 (ADR 025). Answering a permission / trust / destructive modal from a phone is the highest-trust action the mobile head can take, so it is gated separately from everything else a paired phone does (view the stream, snapshot, send, interrupt, dequeue — all ungated, per ADR 025 lines 142-143).

This ticket owns the **authorization primitive**: the per-device stored bit, the CLI flag that records it at pair time, the fail-closed eligibility predicate, and the fail-closed outcome→decision resolution. It is **consumed by the modal control loop (#703)**, which wires the bit into the live permission-answer path and owns the timeout *timer*. The **audit log** is peeled into sibling #712 (a separate primitive #703 calls on a decision).

This gate is a per-device **stored authorization**, NOT a wire capability and NOT a backward-compat seam (epic #597 retires the coarse path; there are no old phones — ADR 025 amendment 2026-06-22). The wire types (#701, merged) carry no capability field for it by design.

**Verified against live code:** `runPairDefault` (`cmd/pyry/pair.go:205`) creates the device via `registry.Add(devices.Device{...})`; `Device` (`internal/devices/device.go:24`) carries no permission field yet; the frame dispatcher exposes the authenticated `*devices.Device` per connection (`dispatch.Conn.Auth()`, `v2session.go:250 device`).

---

## Design

Three additive surfaces. No state machine, no goroutines, no consumer cascade. The bit is set **only** locally at pair time (CLI), never over the wire; it is read by #703 off the already-authenticated `*devices.Device`.

### 1. `internal/devices/device.go` — the per-device bit (AC1, AC4)

Add one field to `Device`, mirroring the `Platform`/`PushToken` `omitempty` precedent:

```go
// AllowRemotePermissions authorizes THIS device to ANSWER a remote
// permission / trust / destructive modal (ADR 025 § "Security model").
// Default OFF (the zero value): an omitted/pre-field on-disk record decodes
// to false = denied. Set only by `pyry pair --allow-remote-permissions`;
// never set or carried over the wire. Read off the authenticated *Device by
// the modal control loop (#703) via dispatch.Conn.Auth(). Gating applies ONLY
// to answering a permission-class modal; everything else a paired phone does
// is ungated.
AllowRemotePermissions bool `json:"allow_remote_permissions,omitempty"`
```

- **`omitempty` is deliberate**, matching `Platform`/`PushToken`: the common OFF case stays off disk, and `false` is the secure default, so an absent key (OFF device, pre-field record, push-only device) reads as denied. AC4 holds: a `true` device writes the key and reloads `true`; an OFF/pre-field record has no key and reloads `false`.
- `Device` stays comparable (`bool` is comparable) — the existing `out != in` round-trip asserts still compile.
- **Forward-compat per Technical Notes:** the default `encoding/json` decoder tolerates the missing field on a pre-existing `devices.json` (→ `false`). Do **not** reach for `DisallowUnknownFields`.

### 2. `internal/devices/auth.go` — eligibility predicate + fail-closed decision (AC2, AC3)

Both are **pure predicates — no side effects** (no logging, no I/O, no token handling). Audit-writing on a decision is #712's primitive, called by #703 — *not* here. This keeps the gate unit-testable in isolation and uncoupled from observability. Land them in `auth.go` beside `Validate`, in its SECURITY doc voice.

**Eligibility predicate (AC2)** — "is this device permitted to answer *at all*":

```go
// MayAnswerRemotePermission reports whether this device is authorized to answer
// a remote permission/trust/destructive modal. Fail-closed: returns true ONLY
// when the per-device opt-in bit is set. A nil receiver (no authenticated
// device on the connection) and a bit-OFF device both return false = denied.
// #703 calls this off dispatch.Conn.Auth() to reject a non-permitted phone's
// modal_answer with an error envelope BEFORE resolving any answer (ADR 025 §1).
func (d *Device) MayAnswerRemotePermission() bool
```

- **Pointer receiver + nil-guard** (`return d != nil && d.AllowRemotePermissions`): `Conn.Auth()` is typed `*devices.Device` and is nil before the first-frame gate accepts. A nil device → denied makes the predicate **total and fail-closed** — the safe default is structural, not a convention #703 must remember. (This is the safe-default contract the AC mandates, not a speculative defense.)

**Outcome enum + fail-closed decision (AC3)** — "given what happened to the modal, grant or deny":

```go
// RemotePermissionOutcome is what the modal control loop (#703) observed for a
// surfaced remote-permission modal. The zero value (OutcomeNoAnswer) is the
// safe default and resolves to DENY, so a default-constructed call denies.
type RemotePermissionOutcome int
const (
    OutcomeNoAnswer RemotePermissionOutcome = iota // no answer observed (default → DENY)
    OutcomeAllow                                    // phone explicitly chose an allow option
    OutcomeDeny                                     // phone explicitly chose a deny option
    OutcomeTimeout                                  // deny-on-timeout window elapsed (#703's timer)
    OutcomeCancel                                   // phone cancelled / dismissed (ESC)
)

// AuthorizeRemotePermission resolves the final grant decision, fail-closed.
// Returns true (ALLOW) ONLY when the device is eligible AND the outcome is an
// explicit allow. Every other (device, outcome) — ineligible/nil device, no
// answer, timeout, cancel, explicit deny — returns false (DENY). #703 applies
// this on timeout (OutcomeTimeout → false). Body: d.MayAnswerRemotePermission()
// && outcome == OutcomeAllow.
func AuthorizeRemotePermission(d *Device, outcome RemotePermissionOutcome) bool
```

- **Why an outcome enum and not a bare bool:** AC3/AC5 demand *distinct, tested* denying outcomes (no-answer / timeout / cancel). A bool erases that distinction (all three would be the identical input, making the AC5 tests vacuous). The enum makes the fail-closed property **structural**: the only ALLOW branch is `OutcomeAllow`; any future outcome added to the enum defaults to DENY unless explicitly mapped. This is the deterministic safety net the security model needs — the safe default lives in one unit-tested place, not re-derived at each #703 branch.
- **Why both functions:** they serve two distinct #703 code paths. `MayAnswerRemotePermission` gates "may this device answer at all" → reject with an *error envelope* if not (ADR 025 §1). `AuthorizeRemotePermission` resolves "given an eligible device, did the outcome grant" → drives the keystroke + the `modal_dismissed` outcome. The decision function re-checks eligibility (belt-and-suspenders) so it is correct standalone *and* composes.

**Decision returns `bool`, not a named `Decision` type** — matches the package's `Validate (Device, bool)` and `v2session.interactive bool` precedent, and keeps the new exported-type count at 1.

### 3. `cmd/pyry/pair.go` — the flag (AC1)

- `pairArgs` gains `allowRemotePermissions bool`.
- `parsePairArgs` registers `fs.Bool("allow-remote-permissions", false, "authorize this device to answer remote permission/trust/destructive modals (default OFF)")` and returns its value. Bare `--allow-remote-permissions` → `true`; absent → `false`; `--allow-remote-permissions=false` → `false` (Go `flag` bool semantics).
- `runPairDefault` threads it into the `devices.Device{...}` literal (~205): `AllowRemotePermissions: parsed.allowRemotePermissions`.
- Extend the usage string (~163) to `... [--relay <url>] [--allow-remote-permissions]`.

No other call site sets the field. `pyry pair list`/`revoke`/`preflight` are unaffected (read-only / removal).

---

## Data flow

```
pyry pair --allow-remote-permissions   (operator, local CLI — the ONLY writer)
        │  parsePairArgs → pairArgs.allowRemotePermissions = true
        ▼
runPairDefault: devices.Device{..., AllowRemotePermissions: true}
        │  registry.Add → registry.Save (atomic temp+rename, 0600)
        ▼
~/.pyry/<name>/devices.json  ← "allow_remote_permissions": true  (omitted when false)
        │  (later) devices.Load on daemon start; Validate matches token on WS connect
        ▼
*devices.Device snapshot → dispatch.Conn.Auth()   (read-only here; #703 consumes)
        │
        ├── #703: conn.Auth().MayAnswerRemotePermission()  → false ⇒ reject modal_answer w/ error envelope
        └── #703: AuthorizeRemotePermission(conn.Auth(), outcome)
                    outcome=OutcomeAllow & eligible ⇒ grant; everything else ⇒ DENY (incl. timeout)
```

This ticket owns only the boxed CLI→disk→`*Device` path and the two predicate functions. The wire path, the `dispatchAppFrame` interception, the nonce/timer, and the audit write are siblings (#701 merged / #703 / #712).

---

## Concurrency model

**None.** A struct field (rides the existing `Registry.mu`-guarded `Save`/`Load`, unchanged) plus two pure pass-by-value/pointer predicates with no goroutines, channels, locks, or I/O. #703 reads the bit off a snapshot `*devices.Device` on its own dispatch goroutine (same access pattern as the existing `interactive bool`) — no new synchronization introduced here.

## Error handling

**None at this layer.** The predicates have no error path (they return `bool`). The field round-trips through the existing `Registry.Save`/`Load`, whose error contract is unchanged. A malformed `devices.json` still fails at `Load` exactly as today; a missing field is not an error (decodes to `false`). Flag-parse errors are handled by the existing `parsePairArgs` → exit-2 path, unchanged.

## Testing strategy

`make check` (gofmt + `go vet` + `staticcheck` + `go test -race` + `cmd/substrate-guard`) must be green. The new code adds no claude-screen literal — substrate-guard stays green. Table-driven, stdlib `testing` only, `t.Parallel()`, no testify. Write test *code* in each package's idiom; scenarios below define inputs + expected behavior. (Heads-up: the repo may be gofmt-dirty at HEAD under a newer local Go than CI — check `git show HEAD:<f> | gofmt -l` before "fixing" any file you didn't change. See [[pyrycode-gofmt-dirty-at-head-go1.26]].)

**`internal/devices/device_test.go`** (mirror the three cited templates):
- *Bit-true JSON round-trip* (mirror `TestDevice_PopulatedRoundTrip`): a `Device` with `AllowRemotePermissions: true` marshals + unmarshals, field survives `true`.
- *OFF device omits the key* (mirror `TestDevice_LegacyOmitsPushFields`): a `Device` with the bit `false` has an encoded form that does **not** contain `"allow_remote_permissions"` (asserts `omitempty`).
- *Pre-field disk shape → OFF* (mirror `TestDevice_DecodeLegacyDiskShape`): a legacy JSON literal lacking the key unmarshals with `AllowRemotePermissions == false`. **This is the AC4 "predates the field → OFF" proof.**

**`internal/devices/auth_test.go`** (new test functions in the existing file):
- *`MayAnswerRemotePermission` table:* bit set → `true`; bit OFF → `false`; **nil receiver → `false`** (fail-closed totality).
- *`AuthorizeRemotePermission` table* over the full matrix:
  - eligible device × `OutcomeAllow` → **`true`** (the sole ALLOW).
  - eligible device × {`OutcomeDeny`, `OutcomeNoAnswer`, `OutcomeTimeout`, `OutcomeCancel`} → `false` (AC3: fail-closed default; no-answer / timeout / cancel each a named case).
  - ineligible (bit-OFF) device × `OutcomeAllow` → `false` (AC2).
  - nil device × `OutcomeAllow` → `false`.
  - zero-value `RemotePermissionOutcome{}` (== `OutcomeNoAnswer`) on an eligible device → `false` (default-constructed call denies).

**`internal/devices/registry_test.go`** (AC4 persistence through Save/Load; mirror the round-trip pattern):
- Build a `Registry`, `Add` a device with the bit `true`, `Save` to a temp path, `Load` it back → the reloaded device's `AllowRemotePermissions == true`. Add a second device with the bit `false` → reloads `false`.
- `Load` a hand-authored pre-field `devices.json` (envelope with one device, no `allow_remote_permissions` key) → reloaded device reads `false`.

**`cmd/pyry/pair_test.go`:**
- *`TestParsePairArgs` additions* (extend the existing table): `--allow-remote-permissions` → `allowRemotePermissions == true`; absent (existing cases) → `false`; `--allow-remote-permissions=false` → `false`. Add the field to the table's compare/asserts.
- *End-to-end pair → persist → reload* (mirror `TestRunPairDefault_PopulatesStaticPubkey`'s HOME=tmp harness): `runPairDefault([]string{"--allow-remote-permissions"})`, then `devices.Load(resolveDevicesPath(...))` and assert the single added device has `AllowRemotePermissions == true`; a second run of `runPairDefault(nil)` adds a device with `false`. Covers AC1 (flag records the bit) + AC4 (survives the real Save/Load) end-to-end through the actual CLI path.

**AC → test mapping:** AC1 → pair_test flag + e2e; AC2 → `MayAnswerRemotePermission` table; AC3 → `AuthorizeRemotePermission` matrix; AC4 → device legacy-decode + registry Save/Load + pair e2e; AC5 → all `auth_test.go` + `device_test.go` cases above.

---

## Scope (size self-check)

**Production source files modified** (excluding `*_test.go`, `*.md`, this spec): **`internal/devices/device.go`, `internal/devices/auth.go`, `cmd/pyry/pair.go` = 3.** Below the ≥5-file gate. **New exported types: 1** (`RemotePermissionOutcome`). **New exported funcs/methods: 2** (`MayAnswerRemotePermission`, `AuthorizeRemotePermission`); **new field: 1**; **new consts: 5**. **Reject branches / state-machine fan-out: 0** (pure predicates). **Consumer cascade: 0** — the field + funcs are additive; the only consumer is #703 (separate ticket); the file-overlap check (§1.5) found **no** in-flight branch touching these files. **Total written LOC** (≈45 production + ≈180 tests) ≈ **~225**. Below the ~400 S line and well below the ~600 split line. No red line tripped — solidly S.

## Open questions

- **None blocking.** All five ACs map to a concrete surface above.
- **`Decision` as a named type vs. `bool`** — resolved to `bool` (matches `Validate`/`interactive` precedent; keeps exported-type count at 1). If #712's audit log wants a richer verdict type, it introduces it there; not speculated here.
- **Producer obligation handed to #703** (named, not this ticket's work): #703 must call `MayAnswerRemotePermission` *first* to reject a non-permitted phone with an error envelope, then map its observed modal state (answer option-kind / timeout / cancel) onto a `RemotePermissionOutcome` and call `AuthorizeRemotePermission`. The mapping of a `modal_answer.option_id` → allow/deny is #703's (it owns the outstanding-modal option list); this primitive only resolves the resulting outcome fail-closed.

---

## Security review

**Reviewer:** architect (self-review; `agents/architect/security-review.md` not synced into this worktree — inline pass using the standard adversarial categories, per the #701/#487/#209 precedent).
**Date:** 2026-06-22
**Verdict:** PASS

Run adversarially against the spec above, assuming it has holes. This is *the* authorization boundary for remote permission granting — the adversarial question is "can a phone (or a dropped/late/replayed message, or a pre-field device) end up GRANTED when it should be DENIED, and is the safe default truly the default everywhere?"

**Findings:**

- **[Trust boundaries — who can set the bit].** No findings; the boundary is correct. The bit is written by **exactly one** path: `runPairDefault`, driven by the local operator's `pyry pair --allow-remote-permissions` CLI flag, persisted to `~/.pyry/<name>/devices.json` (mode 0600, operator-owned). It is **never** settable over the wire — #701's modal payloads carry no capability field (confirmed: `ModalAnswerPayload` = `{modal_id, option_id, answer_token}`; no permission/grant field). A phone cannot grant itself the bit; the only way to flip it is local operator action at pair time. The on-disk file is the same trust level as running the CLI — no new attack surface. **Enforced by** the single call site (grep `AllowRemotePermissions:` finds only `runPairDefault`).
- **[Fail-closed — the core security property].** No findings — the safe default is DENY everywhere, structurally:
  - *Missing/pre-field record* → `false` via `omitempty` + default JSON decode (AC4 test).
  - *Bit OFF* → `MayAnswerRemotePermission` false (AC2 test).
  - *nil `*Device`* (unauthenticated conn) → predicate false (nil-guard; AC test).
  - *Every non-explicit-allow outcome* (no-answer/timeout/cancel/explicit-deny) → `AuthorizeRemotePermission` false (AC3 matrix). The zero-value outcome (`OutcomeNoAnswer`) is DENY, so even a default-constructed/forgotten call denies.
  The only ALLOW path is `eligible && OutcomeAllow` — a single, unit-tested conjunction. This is the "deny-on-timeout, never auto-grant" model (ADR 025 Decision 3 / alternative C rejected) realized in deterministic code, not a stochastic agent rule — correct fabric for the safety net (pipeline principle: belt-and-suspenders means *different* fabric).
- **[Deny-on-timeout ownership split].** No findings. #703 owns the *timer* (detects the timeout); this ticket owns the *decision applied on timeout* (`OutcomeTimeout → false`). The split is clean: the primitive cannot "forget" to deny on timeout because timeout is just another non-allow outcome funneling through the same one ALLOW gate. A bug in #703's timer (fires late / never) degrades to "modal stays outstanding," never to "auto-grant," because no outcome other than `OutcomeAllow` ever grants.
- **[Eligibility vs. decision — no bypass].** No findings. `AuthorizeRemotePermission` re-checks `MayAnswerRemotePermission` internally, so even if #703 skipped the upfront eligibility gate and called the decision directly with `OutcomeAllow`, an ineligible/nil device still denies. The two functions are defense-in-depth, not a single point that can be bypassed by calling the "wrong" one.
- **[Secrets / token hygiene].** No findings — the package SECURITY contract (`device.go:4-10`) is preserved. The new field is a plain bool; the new predicates handle no token, hash, or device name, log nothing, and return only a bool. No new logging is introduced (audit is #712's, deliberately separate — keeping this gate side-effect-free also keeps it from accidentally logging anything).
- **[Persistence weakening via `omitempty`].** No findings. `omitempty` omits only the `false` (secure-default) case; a `true` bit is always written. Absence ⇒ `false` ⇒ denied. There is no value of the on-disk shape where an *absent* key reads as *granted*. (Contrast: a hypothetical `"deny_remote_permissions": false` default-true field would be dangerous under omitempty — the chosen polarity, default-OFF/allow-true, is the safe one.)
- **[Enumeration / info leak].** No findings — N/A. The predicates expose a single bool; no device name, count, or token is revealed. #703's error-envelope-on-ineligible path must follow the existing anti-enumeration discipline (no device name on reject — `internal/relay/auth.go` precedent); flagged as a #703 obligation, not enforceable here.
- **[Threat model alignment].** Aligned with ADR 025 § "Security model" and Decision 3. The dangerous failure modes the model names — silent grant of a destructive permission, replay of an answer onto a later modal, a non-permitted phone granting — are all denied-by-construction here (replay/nonce is #706's layer on top; this layer ensures even a "successful" replayed answer from an ineligible device, or any non-allow outcome, never grants). No threat is introduced; the primitive makes every required mitigation expressible and makes DENY the default for every unhandled path.

**Producer obligations surfaced (carry into #703 / #712):** (a) call `MayAnswerRemotePermission` first → error-envelope-reject a non-permitted phone before resolving any answer; (b) map observed modal state → `RemotePermissionOutcome` (option-kind/timeout/cancel) and apply `AuthorizeRemotePermission`; (c) on reject, no device name in the error (anti-enumeration); (d) audit the decision via #712, never logging modal body text or tokens. None is a gap in this primitive — the shape supports all four.
