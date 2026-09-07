# `internal/audit` — remote-permission decision audit sink

A zero-dependency leaf package that writes **exactly one structured `slog`
record per resolved remote-permission decision** — which device, which modal,
what outcome, from where. It is the **forensic sink** for the remote-permission
security model: the local, write-only record that lets an operator reconstruct
every time a phone was allowed or denied the right to answer a security-class
modal. Landed in #712 (EPIC #597 Phase 3 — mobile remote head, ADR 025 §6
"Audit").

This slice is the **writer primitive only** — it ships with **no caller**. The
package does **not** own any calling loop, timer, nonce, or decision logic; it
only records an already-decided `Entry`. Three callers construct one today:
the modal control loop (#703), `cmd/pyry`'s `questionResolverV2` since #1986
— the per-device gate for an inbound `question_answer` / `question_refused` —
and, since #2127, `cmd/pyry`'s `pairingMinterV2.MintPairing`. The first two
reuse the same `ModalID`/`ModalClass` pair rather than the package growing a
question-specific field: the batch id rides `ModalID` and the compile-time
constant `classQuestion = "question"` rides `ModalClass`, so a forensic reader
tells a question record from a modal one by that field alone. Adding a
dedicated field was considered and rejected in #1986 — it would be a third
production file in this package and would move its shared no-leak test, and
the existing id/class pair already says which batch and what kind.

`pairingMinterV2` widens the pattern one step further: its `ModalClass` rides
the compile-time constant `classPairingMint = "pairing_mint"`, but `ModalID`
is left **empty**, the field's first non-value across all three callers. A
mint has no one-time nonce to name, and filling the field with the conn id or
the minted device's hash would put a value in a field whose documented meaning
is a modal or question-batch identifier — reusing the slot honestly requires
leaving it unset when the caller has nothing that fits, not inventing a
same-shaped stand-in. The generalizable point mirrors #1986's: a vocabulary
field earns a new consumer by what it already means, and a caller with no
value for a required-looking field should say so by leaving it empty, not by
manufacturing one that reads as data but isn't.

- Decision anchor: [ADR 025](../decisions/025-mobile-remote-head-interactive-session.md)
  § "Security model — remote permission granting", item 6 "Audit" — *"Each
  remote answer is logged locally (device id, class, decision, time); never on
  the wire beyond the answer itself. Keys/tokens never logged."* This package is
  the realization of that requirement.
- Spec: [`specs/architecture/712-remote-permission-audit-log.md`](../../specs/architecture/712-remote-permission-audit-log.md).
- Ticket record: [codebase/712.md](../codebase/712.md).

## The core security property: no-leak by construction

The `internal/devices` SECURITY contract (`device.go:4-10`) forbids logging the
plain device token, wrapping it into errors, or passing it across `slog` fields.
This package honors that contract **structurally, not by discipline**:

- **The `Entry` type has no field that can hold a plain token** (or any other
  secret). It carries only non-secret identity: the device's SHA-256
  `DeviceHash` and `DeviceLabel`, the opaque modal nonce + class, the outcome,
  and the source. `device.PushToken` (an opaque secret) likewise has no field.
- **The package imports only `log/slog`** — it never imports `internal/devices`,
  so it cannot even reach a plain token.
- **The writer emits a fixed attribute set** (`device_hash`, `device_label`,
  `modal_id`, `modal_class`, `outcome`, `source`). The no-leak test pins that
  exact key set, so any future edit that adds a secret-bearing field fails the
  test (see [codebase/712.md](../codebase/712.md) § Lessons learned for why
  *exact-key-set* beats a substring scan).
- **Modal body text is deliberately not captured.** A permission prompt can
  embed a shell command or file path; the entry records the opaque `modal_id`
  and the category `modal_class` only — never `Title`/`Prompt`/`Options`.

## Exported surface (3 types, 1 func)

```go
// Entry is one resolved remote-permission decision. Constructed in-process by
// #703, never decoded from the network. Carries ONLY non-secret identity.
type Entry struct {
    DeviceHash  string  // device.TokenHash (SHA-256 hex); "" for a no-device timeout
    DeviceLabel string  // device.Name
    ModalID     string  // protocol.ModalShownPayload.ModalID — the one-time nonce (#701)
    ModalClass  string  // protocol.ModalShownPayload.Class — e.g. "permission" (ADR 025 §6 "class")
    Outcome     Outcome // the self-contained decision classification
    Source      Source  // where the decision originated (mirrors the wire set)
}

type Outcome string // self-contained vocabulary; #703 maps onto it (table below)
const (
    OutcomeAllowed            Outcome = "allowed"             // eligible device + explicit allow (the sole grant)
    OutcomeDeniedUnauthorized Outcome = "denied_unauthorized" // denied: no authorization bit
    OutcomeDeniedTimeout      Outcome = "denied_timeout"      // denied: deny-on-timeout window elapsed
    OutcomeCancelled          Outcome = "cancelled"           // phone cancelled / dismissed (ESC)
    OutcomeDenied             Outcome = "denied"              // authorized phone explicitly chose a deny option
    OutcomeDismissedLocal     Outcome = "dismissed_local"     // resolved at the desktop TTY; the picked choice is not observable by the daemon (#706)
)

type Source string // mirrors protocol.ModalDismissedPayload.Source's closed set
const (
    SourceRemote  Source = "remote"  // a remote inbound answer (the answering device/connection)
    SourceTimeout Source = "timeout" // the daemon's own internal safe-deny on timeout
    SourceLocal   Source = "local"   // resolved at the desktop TTY (ADR 025 §4 first-answer-wins; #706)
)

// Log writes exactly one slog.Info record for a resolved decision. It records
// the already-decided Entry verbatim — it does NOT consult the gate (#702),
// re-derive the outcome, or touch any token. nil logger → slog.Default(); the
// write never panics. The slog record's automatic timestamp satisfies ADR 025
// §6's "time".
func Log(logger *slog.Logger, e Entry)
```

`Log` emits one record at `Info`, message `"audit: remote permission decision"`,
with exactly those six attribute keys (plus slog's automatic `time`/`level`/`msg`).
One call → one record.

## Why self-contained vocabularies (not the gate's / wire types)

- **`Outcome` is self-contained** (AC2): it does **not** import the gate's
  `devices.RemotePermissionOutcome`. #703 maps the gate input + eligibility onto
  it (table below). Decoupling the audit's classification from the gate's input
  enum keeps the sink independent of the authorization check — and lets the
  audit name distinctions the gate's input doesn't (e.g. *allow rejected for
  lack of a bit* → `denied_unauthorized`, distinct from a plain explicit
  `denied`).
- **`Source` mirrors the wire set** `{remote, local, timeout}`
  (`protocol.ModalDismissedPayload.Source`) so #703 passes **one** source value
  to both the wire dismissal and the audit — no second, divergent vocabulary.
- **Named string-backed types, not plain wire-style `string`.** The wire
  payloads use plain `string` for `Class`/`Source` because they are JSON-decoded
  from an untrusted peer; an exhaustive Go enum would couple the decoder to a set
  that may grow. `audit.Entry` is **not** a wire type — it is built in-process —
  so that rationale doesn't apply. Named types give #703 a type-safe, self-contained
  vocabulary; the `string` backing keeps the serialized record stable and
  human-readable regardless of constant order.

## Why both `modal_id` and `modal_class`

The AC frames "modal identity" as the `modal_id` nonce; ADR 025 §6 names the
minimum as *"device id, class, decision, time."* The nonce is opaque and
ephemeral — an operator reading the log cannot tell whether an `"allowed"` entry
granted a *benign* or a *destructive* modal without the class. The entry carries
**both**: `modal_id` (the AC's instance key) and `modal_class` (ADR §6's "class",
the single most forensically meaningful field). If #703 lacks the class for a
path it passes `""`; the primitive is agnostic.

## The `RemotePermissionOutcome` → `Outcome` mapping (#703's obligation)

\#703 owns this mapping; recorded here so the vocabularies line up. The primitive
does **not** perform it (it records the already-classified `Outcome`):

| gate input (`devices.RemotePermissionOutcome`) + eligibility | audit `Outcome` | audit `Source` |
|---|---|---|
| eligible device + `OutcomeAllow` | `OutcomeAllowed` | `SourceRemote` |
| ineligible / nil device + `OutcomeAllow` (no bit) | `OutcomeDeniedUnauthorized` | `SourceRemote` |
| eligible + `OutcomeDeny` (explicit deny option) | `OutcomeDenied` | `SourceRemote` |
| eligible + `OutcomeCancel` (ESC) | `OutcomeCancelled` | `SourceRemote` |
| `OutcomeTimeout` (deny-on-timeout fired; `OutcomeNoAnswer` resolves here) | `OutcomeDeniedTimeout` | `SourceTimeout` |
| resolved at the desktop TTY (#706 first-answer-wins) | (per #703) | `SourceLocal` |

`OutcomeNoAnswer` (the gate's zero-value sentinel) is never audited on its own —
it is a not-yet-resolved state; #703 audits only the *resolved* decision, at
which point no-answer has become a timeout.

`questionResolverV2` (#1986) maps onto the same three-row subset an eligible
device can reach — `OutcomeAllowed`/`OutcomeDenied`/`OutcomeDeniedUnauthorized`,
always `SourceRemote` — because a question batch has no desktop-TTY resolution
path and no deny-on-timeout audit of its own (the no-answer backstop,
`retireQuestion`, deliberately writes no record at all: a dismissal has no
security decision behind it, where a resolution does). An eligible device's
*rejected* answer — wrong count, an out-of-range index, a malformed entry —
is not audited either: the gate admitted the device, but nothing was consumed
and no verdict reached claude, so there is no decision to record.

## Data flow

``` #703 modal control loop (the ONLY caller; owns the modal, the timer, the decision)
  resolves a decision ─┬─ inbound modal_answer → gate predicates (#702) → Outcome + SourceRemote
                       └─ deny-on-timeout fires → safe-deny          → OutcomeDeniedTimeout + SourceTimeout
        │ builds audit.Entry{DeviceHash: dev.TokenHash, DeviceLabel: dev.Name, ModalID, ModalClass, Outcome, Source}
        ▼
audit.Log(s.logger, entry)   →  one slog.Info "audit: remote permission decision"
        ▼
the daemon's configured slog handler (local log sink — file/stderr per daemon wiring)
        (never on the wire — ADR 025 §6: "never on the wire beyond the answer itself")
```

## Concurrency & error handling

- **No concurrency introduced.** `Log` is a pure function over its arguments
  plus the passed `*slog.Logger`; it holds no shared state and spawns no
  goroutine. `slog` handlers are safe for concurrent use, so concurrent `Log`
  calls need no new synchronization. In practice #703 audits on its single
  modal-control goroutine.
- **No error path.** `Log` returns nothing — an audit write cannot fail in a way
  the caller can act on. If the underlying handler's writer errors, slog drops
  the record (its documented behavior). Durability / rotation / append-only
  integrity of the audit log is the **daemon's slog-sink configuration concern,
  out of scope for this primitive** (ADR §6 requires only "logged locally").
- **nil-logger guard** (`→ slog.Default()`) mirrors the repo's
  optional-`*slog.Logger` convention (`agentrun/jsonl/reader.go:94`) — a
  forgotten logger writes to the default, never panics, so the primitive is
  total and an audit write can never become a crash that drops the decision.

## Files

```
internal/audit/
├── audit.go       Entry, Outcome (5 consts), Source (3 consts), Log
└── audit_test.go  one-per-outcome (5-row table), field-completeness, exact-key-set,
                   no-secret-leak (sentinel-absence + hash-present), nil-logger safety
```

~81 LOC production + ~186 LOC tests, one new package; imports only `log/slog`.

## Related

- [codebase/712.md](../codebase/712.md) — ticket record (patterns + lessons).
- [codebase/702.md](../codebase/702.md) / [features/devices-package.md](devices-package.md)
  — the authorization **gate** this audit is decoupled from (the bit + the
  fail-closed predicates); the SECURITY contract the no-leak property honors.
- [features/protocol-package.md](protocol-package.md) — `ModalShownPayload`
  (`ModalID` / `Class`) and `ModalDismissedPayload.Source` (the `{remote, local,
  timeout}` set this package mirrors); the modal wire types (#701).
- [ADR 025](../decisions/025-mobile-remote-head-interactive-session.md) §
  "Security model" item 6 "Audit" — the governing requirement.
- **Consumer (deferred — none wired in #712):** #703 — the modal control loop
  that constructs the `Entry` and calls `Log` on every resolved decision branch.
- **Second consumer — #1986** (landed): `cmd/pyry/question_resolve_v2.go`'s
  `questionResolverV2` is the per-device gate for an inbound question batch —
  a second, independent caller of `audit.Log`, distinguished from #703's
  modal records purely by `ModalClass: classQuestion` on the shared `Entry`
  shape. See [features/v2-session-manager-state-machine-inbound-question-control-questionresolver-seam.md](v2-session-manager-state-machine-inbound-question-control-questionresolver-seam.md).
- **Two-heads ownership — #706** (landed): the **first live `SourceLocal` consumer** —
  the surfacer's local resolution arm (`cmd/pyry/interactive_modal_v2.go`'s
  `handleModalHidden`) logs exactly one `{OutcomeDismissedLocal, SourceLocal}` entry, with
  **empty device identity** (a local TTY resolution has no answering device — the no-device
  case), on the first-answer-wins winner path only. See [codebase/706.md](../codebase/706.md)
  and [features/modalbridge-package.md](modalbridge-package.md).

[ADR 025]: ../decisions/025-mobile-remote-head-interactive-session.md
