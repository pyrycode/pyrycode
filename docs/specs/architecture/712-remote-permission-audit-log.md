# Spec #712 — remote-permission decision audit log (the audit-sink primitive)

**Ticket:** #712 — feat(security): remote-permission decision audit log
**Size:** S (PO-sized S; architect holds S — XS-by-LOC but held S for the security-sensitive pass + the outcome×source test matrix + the structural no-secret-leak guarantee, mirroring sibling #702's reasoning). **1 production file, 3 new exported types, 0 consumer cascade** — see § Scope.
**Epic:** #597 (Phase 3 — remote modal). This ticket owns the **audit-sink primitive**. Siblings: **#702** (the per-device authorization gate — the bit + the fail-closed decision; **merged**, this ticket was peeled out of it), **#701** (modal wire types — merged), **#703** (modal control loop — mints/emits modals, routes/validates the inbound answer, owns the deny-on-timeout *timer*; **the sole consumer of this primitive**), **#706** (two-heads ownership / first-answer-wins).
**Security-sensitive:** **yes** (label present). Inline security pass at § Security review (verdict **PASS**) — required before commit per the label gate. `agents/architect/security-review.md` is not synced into this worktree or the canonical repo; the pass uses the standard adversarial categories per the #702/#701/#487/#209 precedent.

---

## Files to read first

Generated from `codegraph_context` + the reads done during this spec; off-topic hits pruned. The audit primitive imports **only `log/slog`** — it depends on neither `devices` nor `protocol`. The reads below are for the *mapping contract* (#703 maps the gate/modal types onto this primitive's self-contained vocabulary) and for the slog/test idioms to mirror — not because this package imports them.

- `docs/knowledge/decisions/025-mobile-remote-head-interactive-session.md` § "Security model — remote permission granting" (lines 134-145), esp. **item 6 "Audit"** (line 141): *"Each remote answer is logged locally (device id, class, decision, time); never on the wire beyond the answer itself. Keys/tokens never logged."* This is the canonical requirement this ticket realizes — note it names **class** as part of the minimum (see § Design, "Why ModalClass").
- `internal/devices/device.go:4-10` — the package **SECURITY contract**: callers MUST NOT log the plain device token, wrap it into errors, or pass it across slog fields. The audit identifies a device by `TokenHash` / `Name` only. `internal/devices/device.go:24-42` — the `Device.TokenHash` (SHA-256 hex), `.Name`, and `.PushToken` fields: hash+name are the *safe* identity the audit records; `PushToken` is an opaque secret the audit Entry has **no field for** (structurally cannot leak it).
- `internal/devices/auth.go:64-91` — the gate's **input** vocabulary `RemotePermissionOutcome` (`OutcomeNoAnswer/Allow/Deny/Timeout/Cancel`) that #703 maps **from**, and the explicit "audit is #712's primitive, deliberately separate" notes (lines 58-59, 88). Read to write the gate-outcome → audit-outcome mapping table (§ Design), **not** to import it — the AC mandates a self-contained outcome vocabulary.
- `internal/protocol/messaging.go:97-118` — `ModalShownPayload.ModalID` (the one-time nonce = the audit's modal identity) and `.Class` (the plain-string modal class, e.g. `"permission"` — the audit's `ModalClass`). `internal/protocol/messaging.go:149-162` — `ModalDismissedPayload.Source` documents the **closed set `{remote, local, timeout}`** the audit `Source` mirrors (so #703 passes one source value to both the wire dismissal and the audit). Read to confirm field names + align source values.
- `internal/agentrun/jsonl/reader.go:94-110` — the `Logger *slog.Logger` optional-Config idiom (line 99) + "defaults to `slog.Default()`" convention (line 94). `internal/agentrun/jsonl/reader.go:283-287` — the `logger.Warn("jsonl: …", slog.String("err", …))` attr-emit form to mirror (the audit writer uses the same `slog.String` attr form at `Info` level).
- `internal/agentrun/streamrunner/runner.go:201` — `logger.Warn("streamrunner: …", "key", val)`: the **package-prefixed slog message** convention (the audit message is `"audit: remote permission decision"`).
- `internal/devices/device_test.go:102-173` — `TestDevice_LegacyOmitsPushFields` / `TestDevice_PopulatedRoundTrip` / `TestDevice_DecodeLegacyDiskShape`: the **encoded-form assertion** templates (a key IS present / a string is NOT present in the serialized output) to mirror for the audit field-completeness + no-leak tests.
- `docs/specs/architecture/702-remote-permission-gate.md` § Security review, "Producer obligations" (line 218): item **(d)** *"audit the decision via #712, never logging modal body text or tokens"* — the exact contract this ticket fulfills.

---

## Context

Phase 3 of epic #597 (ADR 025, § "Security model" item 6 "Audit"). Answering a remote permission / trust / destructive modal from a phone is the highest-trust action the mobile head can take. #702 (merged) owns the **gate** — the per-device authorization bit and the fail-closed `MayAnswerRemotePermission` / `AuthorizeRemotePermission` predicates. This ticket owns the **audit sink**: the primitive that writes one forensic record per decision the gate produces.

The decisions themselves are made by the **modal control loop (#703)**: when an inbound `modal_answer` arrives, #703 consults the gate and either routes the answer or denies it; on the deny-on-timeout window elapsing, #703 applies the safe-deny. **Every one of those outcomes must be audited.** This ticket ships the writer primitive #703 calls **once per decision** — it does not own the loop, the timer, or the decision logic.

No audit-logging infrastructure exists today; `log/slog` is the only structured logger. The device package carries a hard SECURITY contract: the plain device token is never logged, wrapped into errors, or passed across slog fields — so the audit entry identifies a device by `TokenHash` / `Name` only.

**Verified against live code:** `internal/audit` does not exist (new leaf package, no overlap — branch-overlap check § Scope found no in-flight branch touching it). `ModalShownPayload.Class` (`messaging.go:113`) and `ModalDismissedPayload.Source` (`messaging.go:161`, closed set `{remote, local, timeout}`) are present today. The gate's `RemotePermissionOutcome` (`auth.go:67`) is the input #703 maps from.

---

## Design

A new leaf package **`internal/audit`** with a struct, two named string-vocabulary types, and one package-level writer. **Zero internal dependencies** (imports only `log/slog`). No goroutines, no state, no consumer cascade. #703 imports it and calls `audit.Log(logger, entry)` once per resolved decision.

### Package shape — `internal/audit/audit.go`

**The entry (the four AC fields + the ADR-§6 class):**

```go
// Entry is one remote-permission decision to be recorded. It carries ONLY
// non-secret identity: the device's TokenHash / Name (NEVER the plain token —
// the entry has no field that could hold one), the one-time modal nonce, the
// modal class, the resolved outcome, and where the decision originated.
type Entry struct {
    DeviceHash  string   // device.TokenHash (SHA-256 hex); "" for a no-device timeout
    DeviceLabel string   // device.Name
    ModalID     string   // protocol.ModalShownPayload.ModalID — the one-time nonce (#701)
    ModalClass  string   // protocol.ModalShownPayload.Class — e.g. "permission" (ADR 025 §6 "class")
    Outcome     Outcome  // the self-contained decision classification (below)
    Source      Source   // where the decision originated (mirrors the wire set)
}
```

**Outcome — this ticket's self-contained vocabulary (AC2):**

```go
// Outcome is the security classification of a resolved remote-permission
// decision. Self-contained per this ticket; #703 maps devices.RemotePermissionOutcome
// + eligibility onto it (mapping table below). String-backed so the serialized
// audit record is stable and human-readable regardless of constant order.
type Outcome string
const (
    OutcomeAllowed            Outcome = "allowed"             // eligible device + explicit allow (the sole grant)
    OutcomeDeniedUnauthorized Outcome = "denied_unauthorized" // denied: no authorization bit (AC2)
    OutcomeDeniedTimeout      Outcome = "denied_timeout"      // denied: deny-on-timeout window elapsed (AC2)
    OutcomeCancelled          Outcome = "cancelled"           // phone cancelled / dismissed (ESC) (AC2)
    OutcomeDenied             Outcome = "denied"              // authorized phone explicitly chose a deny option
)
```

**Source — mirrors the established wire vocabulary (`ModalDismissedPayload.Source`):**

```go
// Source is where the decision originated. The value set deliberately mirrors
// protocol.ModalDismissedPayload.Source's documented closed set {remote, local,
// timeout}, so #703 passes ONE source value to both the wire dismissal and this
// audit entry — no second, divergent source vocabulary.
type Source string
const (
    SourceRemote  Source = "remote"  // a remote inbound answer (the answering device/connection) — AC source
    SourceTimeout Source = "timeout" // the daemon's own internal safe-deny on timeout — AC source
    SourceLocal   Source = "local"   // resolved at the desktop TTY (ADR 025 §4 first-answer-wins; #706)
)
```

**The writer:**

```go
// Log writes exactly one structured audit record for a resolved remote-permission
// decision. It records the already-decided Entry verbatim — it does NOT consult
// the gate, re-derive the outcome, or touch any token (AC4). A nil logger defaults
// to slog.Default() (matching the package convention). Emitted at slog.Info.
func Log(logger *slog.Logger, e Entry)
```

- **Behavior (the contract the no-leak test pins):** emits one `slog` record at `Info` level, message `"audit: remote permission decision"`, with **exactly these attribute keys** and no others: `device_hash`, `device_label`, `modal_id`, `modal_class`, `outcome`, `source` (plus slog's automatic `time`, `level`, `msg`). One call → one record. Uses the `slog.String(key, val)` attr form (mirror `jsonl/reader.go:283-287`); `Outcome`/`Source` stringify via their underlying `string`.
- **`time` satisfies ADR §6's "time"** implicitly (every slog record is timestamped) — no explicit timestamp field.
- **nil-logger guard** mirrors the repo's optional-`*slog.Logger` convention (`jsonl/reader.go:94`); keeps the primitive total (a forgotten logger writes to the default, never panics).
- **No `context.Context` parameter:** an audit write is synchronous, non-cancellable, and matches the repo's ctx-free slog convenience-call style (`logger.Warn(msg, attrs…)`). A ctx variant is a YAGNI add later if trace propagation is ever wanted (Open Questions).

### Why a dedicated package, not a slog convention in `devices`/`relay`

The Technical Notes leave "small dedicated package vs structured-slog convention" to the architect. A leaf package is chosen because it makes the **core security property structural and unit-testable in isolation**: the `Entry` type has *no field that can hold a plain token*, so the leak the package SECURITY contract forbids is impossible by construction, not by discipline. Putting it in `devices` would couple the sink to the token-bearing package (the one place the plain token lives); putting it in `relay` would couple it to #703's loop. A zero-dependency leaf keeps the audit decoupled from both the gate (AC4) and the loop, and the no-secret-leak guarantee testable against `Entry` alone.

### Why a named `Outcome`/`Source` type, not a plain wire-style string

The wire payloads use plain `string` for `Class`/`Source`/`Role` (leaf-data convention) because they are JSON-decoded from an untrusted peer and an exhaustive Go enum would couple the decoder to a set that may grow. `audit.Entry` is **not** a wire type — it is constructed in-process by #703 and never decoded from the network — so that rationale does not apply. Named string-backed types give #703 a type-safe, self-contained vocabulary (the AC's "self-contained outcome vocabulary"), while the `string` backing keeps the serialized record stable and human-readable. The values still *match* the wire `Source` set so #703 needs no translation layer.

### Why `ModalClass` (AC reconciliation)

The AC's four behavioral fields frame "modal identity" as the `modal_id` nonce. But ADR 025 §6 — which the Technical Notes cite verbatim — names the audit minimum as **"device id, class, decision, time."** The `modal_id` nonce is opaque and ephemeral; an operator reading the audit log in isolation cannot tell whether a `"allowed"` entry granted a *benign* or a *destructive* modal without it. `ModalShownPayload.Class` is available to #703 at decision time (it minted the modal), is a non-secret category string, and is the single most forensically meaningful field. The entry therefore carries **both** `modal_id` (the AC's modal identity / instance key) **and** `modal_class` (ADR §6's "class"). This satisfies both documents; dropping class would leave the audit unable to answer "what was at stake."

### The `RemotePermissionOutcome` → `Outcome` mapping (#703's obligation, documented here)

#703 owns this mapping; it is recorded here so the vocabularies line up. The primitive does not perform it (AC4 — it records the already-classified `Outcome`):

| gate input (`devices.RemotePermissionOutcome`) + eligibility | audit `Outcome` | audit `Source` |
|---|---|---|
| eligible device + `OutcomeAllow` | `OutcomeAllowed` | `SourceRemote` |
| **ineligible / nil device** + `OutcomeAllow` (answer rejected, no bit) | `OutcomeDeniedUnauthorized` | `SourceRemote` |
| eligible + `OutcomeDeny` (explicit deny option) | `OutcomeDenied` | `SourceRemote` |
| eligible + `OutcomeCancel` (ESC) | `OutcomeCancelled` | `SourceRemote` |
| `OutcomeTimeout` (deny-on-timeout fired; `OutcomeNoAnswer` resolves here) | `OutcomeDeniedTimeout` | `SourceTimeout` |
| resolved at the desktop TTY (#706 first-answer-wins) | (per #703) | `SourceLocal` |

`OutcomeNoAnswer` (the gate's zero-value sentinel) is never audited on its own — it is a not-yet-resolved state; #703 audits only the *resolved* decision, at which point no-answer has become a timeout.

---

## Data flow

```
#703 modal control loop (the ONLY caller; owns the modal, the timer, the decision)
  resolves a decision  ─┬─ inbound modal_answer  → gate predicates (#702) → Outcome + SourceRemote
                        └─ deny-on-timeout fires  → safe-deny           → OutcomeDeniedTimeout + SourceTimeout
        │  builds audit.Entry{DeviceHash: dev.TokenHash, DeviceLabel: dev.Name,
        │                     ModalID, ModalClass, Outcome, Source}   (plain strings only)
        ▼
audit.Log(s.logger, entry)
        │  one slog.Info record, msg "audit: remote permission decision",
        │  attrs: device_hash · device_label · modal_id · modal_class · outcome · source (+ time/level/msg)
        ▼
the daemon's configured slog handler (local log sink — file/stderr per daemon wiring)
        (never on the wire — ADR 025 §6: "never on the wire beyond the answer itself")
```

This ticket owns only the `audit` package (the boxed `Log` + types). The gate, the wire interception, the nonce/timer, the mapping, and the daemon's logger configuration are all siblings/elsewhere (#702 merged / #701 merged / #703 / daemon wiring).

---

## Concurrency model

**None introduced.** `Log` is a pure function over its arguments plus the passed `*slog.Logger`; it holds no shared state. `slog.Handler` implementations are documented safe for concurrent use, and the stdlib handlers serialize writes — so concurrent `audit.Log` calls are safe with no new synchronization. In practice #703 resolves and audits on its single modal-control goroutine; the primitive imposes no ordering requirement either way.

## Error handling

**None at this layer.** `Log` returns nothing — an audit write cannot fail in a way the caller can act on. If the underlying handler's `io.Writer` errors, slog drops the record (its documented behavior); durability/rotation/append-only integrity of the audit log is the **daemon's logger-configuration concern, out of scope for this primitive** (ADR 025 §6 requires "logged locally," which the daemon's slog sink satisfies). The nil-logger guard converts a forgotten logger into a default-logger write, not a panic.

## Testing strategy

`make check` (gofmt + `go vet` + `staticcheck` + `go test -race` + `cmd/substrate-guard`) must be green. The package adds no claude-screen literal — substrate-guard stays green. Table-driven, stdlib `testing` only, `t.Parallel()`, no testify. Tests capture output by building `slog.New(slog.NewJSONHandler(&buf, nil))`, passing it to `Log`, and asserting on the decoded JSON (mirror the encoded-form assertion style of `device_test.go:102-173`). Write the test code in the package idiom; scenarios below define inputs + expected behavior.

**`internal/audit/audit_test.go`:**

- **One entry per outcome class (AC5):** table over all five `Outcome` values (`allowed`, `denied_unauthorized`, `denied_timeout`, `cancelled`, `denied`) each paired with its expected `Source`. For each: call `Log` once → assert **exactly one** JSON record emitted (count the records / newlines == 1) and its `outcome`/`source` equal the inputs. Asserts "a single audit entry per decision" + outcome-vocabulary coverage.
- **Field completeness (AC1, AC5):** a fully-populated `Entry` → the decoded record contains all of `device_hash`, `device_label`, `modal_id`, `modal_class`, `outcome`, `source` with values equal to the input, plus slog's `time`/`level`/`msg` (`level == INFO`, `msg == "audit: remote permission decision"`).
- **No-secret-leak — exact key set (AC3):** assert the record's attribute key set is **exactly** `{device_hash, device_label, modal_id, modal_class, outcome, source}` (plus `time`/`level`/`msg`) — no stray key. This is the structural guard: it fails if any future edit adds a field that could carry a secret.
- **No-secret-leak — sentinel absence (AC3):** define a sentinel plaintext-token string; populate `Entry` with a realistic 64-hex `DeviceHash` (and a `DeviceLabel`/`ModalID`) that do **not** contain the sentinel; assert the serialized record does **not** contain the sentinel substring, and **does** contain the hash (proving hash is the recorded identity, token is not). Reinforces that the `Entry` type has no field for a plain token.
- **nil-logger safety:** `Log(nil, entry)` does not panic (defaults to `slog.Default()`).

**AC → test mapping:** AC1 → field-completeness + per-outcome (entry written per decision); AC2 → per-outcome table covers `allowed`/`denied_unauthorized`/`denied_timeout`/`cancelled` (+ `denied`); AC3 → exact-key-set + sentinel-absence; AC4 → structural (the package imports neither `devices` nor any gate predicate — `go list`/import check, and the `Log` signature takes a pre-classified `Outcome`); AC5 → all of the above.

---

## Scope (size self-check)

**Production source files created/modified** (excluding `*_test.go`, `*.md`, this spec): **`internal/audit/audit.go` = 1.** Far below the ≥5-file gate. **New exported types: 3** (`Entry`, `Outcome`, `Source`) — below 5. **New exported funcs: 1** (`Log`); **new consts: 8** (5 `Outcome` + 3 `Source`). **Reject branches / state-machine fan-out: 0** (one pure writer, no branching beyond the nil-logger guard). **Consumer cascade: 0** — the package is additive and new; the only consumer is #703 (separate ticket); the §1.5 branch-overlap check (`git fetch` + diff every `origin/feature/*` branch) found **no** in-flight branch touching `internal/audit`. **Total written LOC** ≈ 70 production + ≈ 140 tests ≈ **~210**. Below the ~400 S line; nowhere near the ~600 split line. No red line tripped — solidly S (XS-by-LOC; held S for the security pass + the outcome×source matrix, per the #702 sibling precedent).

## Open questions

- **None blocking.** All five ACs map to a concrete surface above.
- **`ModalClass` inclusion** — resolved to include it (reconciles the AC's `modal_id`-only framing with ADR 025 §6's named "class" minimum; see § Design "Why ModalClass"). If #703 finds the class unavailable for a given decision path, it passes `""`; the primitive is agnostic.
- **`SourceLocal` coverage** — the AC-required sources are `remote` + `timeout`; `local` is included to mirror the wire `ModalDismissedPayload.Source` set so a #706 first-answer-wins local resolution that pre-empts a phone answer is auditable without a vocabulary change. Whether #703 audits local resolutions is #703's call; the constant's presence costs nothing and prevents a divergent source vocabulary.
- **Audit-log durability / integrity** (append-only, tamper-evidence, rotation) is the daemon's slog-sink configuration concern, not this primitive's; ADR §6 requires only "logged locally." Not speculated here.
- **A structured filter marker** (e.g. an `slog.Group("audit", …)` or an `event` key) for machine log-processing was considered and deferred — the stable message `"audit: remote permission decision"` is sufficient to grep; a group would nest the keys and complicate the no-leak key-set assertion for no AC benefit.

---

## Security review

**Reviewer:** architect (self-review; `agents/architect/security-review.md` not synced into this worktree or the canonical repo — inline pass using the standard adversarial categories, per the #702/#701/#487/#209 precedent).
**Date:** 2026-06-22
**Verdict:** PASS

Run adversarially against the spec above, assuming it has holes. This primitive is the **forensic record** of the remote-permission boundary — the adversarial questions are: *"Can a secret (plain device token, push token, or any other sensitive value) ever reach an audit entry? Can the audit be made to lie, omit, or be confused with a non-decision? Does writing it open any new trust/leak surface?"*

**Findings:**

- **[Secrets / token hygiene — the core property].** No findings — leak is impossible by construction. The `Entry` type has **no field that can hold a plain device token** (`device.go:4-10`'s forbidden value): it carries `DeviceHash` (SHA-256 hex), `DeviceLabel` (`Name`), `ModalID`/`ModalClass` (non-secret nonce/category), `Outcome`, `Source`. The plain token is never passed to this layer — #703 constructs the entry from `dev.TokenHash` / `dev.Name`. `device.PushToken` (an opaque secret) likewise has **no field**, so it cannot leak either. The writer emits a **fixed attribute set** (`device_hash, device_label, modal_id, modal_class, outcome, source`); the no-leak test pins that set exactly, so a future edit that adds a secret-bearing field fails the test. The package imports `log/slog` only — it never imports `devices`, so it cannot even reach a plain token. **Enforced by:** the `Entry` field list + the fixed writer attrs + the exact-key-set test. This honors the #702 producer-obligation item (d) ("audit … never logging modal body text or tokens").
- **[Modal body text — deliberate non-capture].** No findings. The entry records `modal_id` + `modal_class` but **not** the modal's `Title`/`Prompt`/`Options` text. A permission prompt can embed a shell command, file path, or other sensitive operational content; capturing only the opaque id + the category avoids writing that content to the audit log. Structural: `Entry` has no title/prompt field.
- **[Decoupling — the audit cannot re-derive or disagree (AC4)].** No findings. `Log` records the already-classified `Outcome` verbatim; it does **not** consult the gate (`MayAnswerRemotePermission`/`AuthorizeRemotePermission`) or re-decide. This means the audit faithfully transcribes #703's decision rather than computing a second, possibly-divergent verdict. *Residual, flagged as a #703 obligation, not a gap here:* #703 must pass the `Outcome` that matches the keystroke it actually sent (audit says `allowed` ⟺ it granted). The primitive's job is faithful transcription; correctness of the value supplied is the caller's. The mapping table in § Design is the contract that keeps #703 honest.
- **[Fail-safe / completeness — the audit cannot silently skip a decision].** No findings at this layer. `Log` writes exactly one record per call and cannot return an error that tempts #703 to swallow it; #703's obligation is simply to *call* `Log` on every resolved decision (every branch of its loop — allow, deny, unauthorized-reject, timeout, cancel). That call-site completeness is #703's to test; this primitive makes each call a single, total, panic-free write. The nil-logger guard prevents a missing-logger from turning an audit write into a crash that drops the decision.
- **[Trust boundaries — no new inbound surface].** No findings. The sink is **write-only and local**: it reads nothing from the network, takes only in-process values #703 already holds, and never emits to the wire (ADR §6: "never on the wire beyond the answer itself"). It introduces no parser, no deserialization, no new externally-reachable code path. The on-disk audit log is the daemon's local log, at the operator's trust level — no new attack surface.
- **[Info leak / enumeration].** No findings. The audit reveals device hashes + labels + decisions, but to the **local operator's log only** — the operator already has full registry access (`devices.json`). Nothing is exposed to a remote party. There is no remote-reachable query over the audit; enumeration concerns (anti-device-name-on-reject, `internal/relay/auth.go` precedent) apply to #703's error-envelope path, not to this local sink.
- **[Threat-model alignment].** Aligned with ADR 025 §6. The required minimum — **device id** (hash+label), **class** (`modal_class`), **decision** (`outcome` + `source`), **time** (implicit slog timestamp) — is captured; **keys/tokens never logged** is guaranteed structurally. The dangerous failure mode an audit exists to prevent — *a remote grant of a destructive permission going unrecorded, or recorded without enough context to recognize it* — is closed: every decision is one record, and `modal_class` makes a destructive grant legible. No threat is introduced; the primitive makes the audit faithful and the secret-leak impossible by construction.

**Producer obligations surfaced (carry into #703):** (a) call `audit.Log` on **every** resolved decision branch (allow / unauthorized-reject / explicit-deny / cancel / timeout), constructing `Entry` from `dev.TokenHash` + `dev.Name` (never the plain token); (b) supply the `Outcome`/`Source` per the § Design mapping table so the record matches the keystroke actually sent; (c) populate `ModalClass` from the surfaced modal; (d) do not pass modal body text into any field. None is a gap in this primitive — the shape supports all four and forbids the unsafe ones structurally.
