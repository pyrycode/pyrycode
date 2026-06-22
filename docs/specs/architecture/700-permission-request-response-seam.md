# Spec: internal `PermissionRequest` + `PermissionResponse` seam (#700)

Sub-issue of #597 (Phase 3). Extends the neutral internal event model
(`internal/turnevent`, #606) with the permission request/response seam. Pure
value types + a constructor; no transport, no wire, no consumers wired. Mirrors
#606's footprint and conventions exactly.

## Files to read first

- `internal/turnevent/event.go:23-103` — the `Event` sealed sum type, the
  `isTurnEvent()` unexported marker, the value-receiver marker convention, and
  the `var _ Event = …{}` compile-time assertion block. `PermissionRequest`
  joins this exactly as `Stall` did. Note the package doc (lines 14-18): inbound
  commands are deferred "to a later ticket" — **this is that ticket for the
  first inbound type.**
- `internal/turnevent/taxonomy.go` (whole file, 95 lines) — the ACP enum
  pattern: string-backed type, `<Type><Value>` const block, unexported
  canonical `[]T` slice as single source of truth, `Valid()` scanning it.
  `PermissionOptionKind` is added here as the fourth enum, same shape.
- `internal/turnevent/taxonomy_test.go` (whole file, 122 lines) — the exactness
  test pattern to mirror: independent raw-string `want` literal, length guard,
  `reflect.DeepEqual(slice, want)`, every-canonical-`Valid()`, and a
  validity table (in-taxonomy / fabricated / empty).
- `internal/turnevent/content.go` (whole file, 34 lines) — a second worked
  example of the sealed-sum-type idiom (`ToolContent`/`isToolContent()`); the
  template for the new `Inbound`/`isInbound()` marker.
- `internal/turnevent/event_test.go:46-84` — the stream type-switch test style:
  build a heterogeneous `[]Event`, recover each kind via type switch. Reuse for
  asserting `PermissionRequest` is an `Event` and `PermissionResponse` is an
  `Inbound`.
- `internal/turnevent/boundary_test.go` (whole file) — **do not modify.** It
  auto-scans every production `.go` file in the package for non-stdlib imports.
  The new `permission.go` must be stdlib-only (it needs **zero** imports); the
  boundary test covers it for free.
- `docs/knowledge/decisions/025-mobile-remote-head-interactive-session.md`
  §"Security model — remote permission granting" (lines 134-145) and the Phase 3
  decomposition (line 215) — confirms the gate/nonce/deny-on-timeout work is
  **downstream**, not here. Establishes why this slice is unlabelled.
- `docs/knowledge/features/turnevent-package.md:198-209` — the "deliberately NOT
  in the package" list. Note: it tentatively grouped `PermissionRequest` under
  "Non-turn / internal-only events." This ticket **reclassifies it as outbound**
  (see Design §1). The doc is owned by the documentation phase and will be
  reconciled there after merge — **do not edit it.**

## Context

Phase 3 (epic #597) layers modals/permissions/queue onto the structured
interactive session. The permission **request/response** pair is the one Phase 3
internal type that is genuinely cross-cutting: both the mobile wire adapter
(#597) and the future `pyry acp` adapter (#600) map onto the same daemon-owned,
ACP-neutral shape — ACP's `session/request_permission` lands on this exact
`PermissionRequest` later. Shaping it adapter-neutral now keeps each adapter a
thin pass-through and keeps external ACP-spec churn out of the daemon core, the
same containment logic the rest of `turnevent` provides.

This ticket adds **only** the internal types + one constructor. It does not
touch tui-driver, does not define wire types (`internal/protocol/` is the wire
SSOT — separate ticket), does not push to the wire, and wires no consumers.

## Design

### 1. `PermissionRequest` is an outbound `Event` variant

The daemon *emits* a permission request toward the consumer (phone / ACP
client); the consumer *answers* it. So `PermissionRequest` is **outbound** and
joins the existing `Event` sealed sum type via the same unexported
`isTurnEvent()` marker — identical to `TextChunk` … `Stall`.

This reclassifies the tentative grouping in #606's package doc, which listed
`PermissionRequest` under "Non-turn / internal-only events." That grouping
predates the decision; `Stall` was on the same list and **graduated into**
`Event` in #638. `PermissionRequest` follows that precedent. (The doc itself is
documentation-phase-owned and will be updated post-merge — not by this ticket.)

**Type contract** (`permission.go`):

```
// PermissionRequest is an outbound Event: the daemon asks the consumer to
// answer a permission modal. Correlated to its PermissionResponse by RequestID.
type PermissionRequest struct {
    RequestID  string             // correlates request <-> response
    ToolCallID string             // the tool call this gates; "" if not tool-triggered
    Title      string             // human-readable prompt text (e.g. "Do you want to proceed?")
    Options    []PermissionOption // ordered; the consumer renders + selects from these
}
func (PermissionRequest) isTurnEvent() {}   // joins Event
var _ Event = PermissionRequest{}
```

- `ToolCallID` correlates to the `ToolStart.ToolCallID` the consumer already
  saw in the stream (same by-ID reference style as `ToolUpdate`); the request
  does **not** re-embed tool details. Empty when a permission prompt has no
  backing tool call.
- `Title` is the always-present human-readable context; covers both the
  tool-triggered and prompt-only cases the AC's "tool-call / prompt context"
  phrasing spans.
- `Options` order is significant (the consumer renders in order); it is a slice,
  not a map.

**Constructor** (AC-mandated; overrides the package's prior "constructors are
YAGNI" note for this one type):

```
// NewPermissionRequest assembles a PermissionRequest from its parts. It does
// NOT validate — constructing an invalid one is allowed and caught downstream
// via PermissionOptionKind.Valid(), matching the package convention.
func NewPermissionRequest(requestID, toolCallID, title string, options []PermissionOption) PermissionRequest
```

Positional params; the three leading strings are order-sensitive
(`requestID`, `toolCallID`, `title`) — the developer's unit test pins the order.
No `Config` struct: this is a 4-field value assembler, not a long-lived
component (the `Config`-struct pattern is for `Supervisor`-style wiring).

### 2. `PermissionOption` + the `PermissionOptionKind` enum

```
// PermissionOption is one selectable answer in a PermissionRequest.
type PermissionOption struct {
    ID    string                // referenced by PermissionResponse.OptionID
    Label string                // human-readable ("Yes", "No, and don't ask again")
    Kind  PermissionOptionKind  // the ACP semantic kind
}
```

`PermissionOptionKind` is added to **`taxonomy.go`** as the fourth ACP enum,
identical in shape to `ToolKind` / `ToolStatus` / `TurnEndReason`:

```
type PermissionOptionKind string
const (
    PermissionOptionKindAllowOnce    PermissionOptionKind = "allow_once"
    PermissionOptionKindAllowAlways  PermissionOptionKind = "allow_always"
    PermissionOptionKindRejectOnce   PermissionOptionKind = "reject_once"
    PermissionOptionKindRejectAlways PermissionOptionKind = "reject_always"
)
var permissionOptionKinds = []PermissionOptionKind{ /* the four, in the order above */ }  // unexported SSOT
func (k PermissionOptionKind) Valid() bool   // scans permissionOptionKinds
```

The four values are **exactly** the four ACP `session/request_permission`
kinds — no more, no fewer. Canonical slice stays unexported (no consumer
enumerates yet; #607 may add an exported accessor when its wire mapping needs
one — same deferral the existing three enums use).

**Why this enum lives in `taxonomy.go`, not `permission.go`:** `taxonomy.go`'s
stated purpose is "the … enums [that] carry exactly the ACP taxonomy values."
`permission_option_kind` is one. Co-locating keeps all four ACP enums and their
SSOT slices visible together, and lets the exactness test sit beside its three
siblings in `taxonomy_test.go` — which is precisely what the AC's "mirroring
`taxonomy.go` / `taxonomy_test.go`" asks for.

### 3. Inbound home: establish the `Inbound` sealed sum type now

**The architect design call (AC bullet 3 + Technical Notes).** `turnevent`
today seals only the *outbound* `Event` family. `PermissionResponse` is the
first *inbound* type. Two options:

- **(A) Standalone** `PermissionResponse` struct, no marker.
- **(B) Establish the inbound sealed sum-type home** — a new
  `Inbound interface{ isInbound() }` marker, with `PermissionResponse` as its
  first member.

**Decision: (B).** Rationale:

1. **Package idiom.** This package already seals every closed variant set with
   an unexported marker — `Event`/`isTurnEvent()`, `ToolContent`/`isToolContent()`.
   `Inbound`/`isInbound()` is the same 4-line idiom applied a third time, not
   speculative abstraction. The whole package is a forward-looking neutral
   contract with no wired consumers yet (#606 shipped it that way deliberately);
   defining the inbound contract ahead of its consumer is the package's job,
   not a YAGNI violation.
2. **Eliminates the collision the Technical Notes flag.** The deferred
   inbound-commands ticket (`Prompt`, `Cancel`, `DropQueued`) then simply *adds
   variants* to an existing home — an unambiguous, additive path. Option (A)
   would force that ticket to either retrofit a marker onto `PermissionResponse`
   or leave one inbound type outside the sum while three sit inside it (an
   awkward inconsistency).
3. **Cost is ~4 lines**, symmetric with the outbound side, and constrains
   nothing about future variants (a marker interface lets each variant carry its
   own fields).

```
// Inbound is the sealed sum type of inbound commands the consumer sends back to
// the daemon. PermissionResponse is the first member; the deferred
// inbound-commands ticket adds Prompt / Cancel / DropQueued here.
type Inbound interface{ isInbound() }

// PermissionResponse answers a PermissionRequest (matched by RequestID).
type PermissionResponse struct {
    RequestID string  // the PermissionRequest this answers
    OptionID  string  // the selected PermissionOption.ID; "" when Cancelled
    Cancelled bool    // true => the consumer dismissed the modal without selecting
}
func (PermissionResponse) isInbound() {}
var _ Inbound = PermissionResponse{}
```

- `OptionID` references a `PermissionOption.ID` from the request (the **option
  id**, *not* the kind enum — an important subtlety; the kind is request-side
  metadata).
- `Cancelled` discriminates the ACP `cancelled` outcome from `selected`.
  Exactly one of {`OptionID` set, `Cancelled` true} is meaningful; the package
  does **not** enforce mutual exclusion — consistent with its
  construct-invalid-then-validate-downstream stance. No `Valid()` is required on
  `PermissionResponse` (the AC does not ask for one; YAGNI until the inbound
  parser needs it).

`Inbound` and `PermissionResponse` live in `permission.go` next to their only
current member. The deferred inbound-commands ticket may relocate `Inbound` to
its own `inbound.go` when it adds siblings — a forward note, not this ticket's
work.

### 4. File layout

| File | Change | Contents |
|---|---|---|
| `internal/turnevent/taxonomy.go` | **modify** | append `PermissionOptionKind` type + 4 consts + `permissionOptionKinds` slice + `Valid()` |
| `internal/turnevent/taxonomy_test.go` | **modify** | append `TestPermissionOptionKind_Taxonomy` mirroring the three existing exactness tests |
| `internal/turnevent/permission.go` | **create** | `PermissionRequest` (+ marker + `var _ Event`), `PermissionOption`, `NewPermissionRequest`, `Inbound`, `PermissionResponse` (+ marker + `var _ Inbound`) |
| `internal/turnevent/permission_test.go` | **create** | construct/read-back + sum-type membership tests (see Testing) |

Production source files touched: 2 (`taxonomy.go` modified, `permission.go`
created). Exported types added: 5 (`PermissionRequest`, `PermissionResponse`,
`PermissionOption`, `PermissionOptionKind`, `Inbound`) — at the S cap. `permission.go`
needs **zero imports** (pure value types); the stdlib-only boundary test passes
unchanged.

## Concurrency model

**None.** Pure value types — no goroutines, channels, mutexes, or I/O. Values
pass by value. Thread-safety of any *stream* of these is the consumer's concern
(#608), unchanged from #606.

## Error handling

**None at this layer.** No I/O, no parse, nothing that can fail. Invalid values
(out-of-taxonomy kind, a `PermissionResponse` with both `OptionID` and
`Cancelled` set) are *constructible* and detected downstream — `Valid()` for the
kind; the modal control loop for the response invariant. This matches the
package's deliberate "value types are dumb; validation is the consumer's"
contract (`turnevent-package.md:170`).

## Testing strategy

`make check` (gofmt + `go vet` + `staticcheck` + `go test -race`) must be green.
All tests are table-driven, stdlib `testing` only, `t.Parallel()`, no testify —
matching the package. Write the test *code* in the package's idiom; the
scenarios below define inputs + expected behavior, not function bodies.

**`taxonomy_test.go` — `TestPermissionOptionKind_Taxonomy`** (mirror the three
existing `*_Taxonomy` tests exactly):
- `want := []PermissionOptionKind{"allow_once","allow_always","reject_once","reject_always"}`
  as an **independent raw-string literal** (catches a typo'd const).
- Length guard: `len(permissionOptionKinds) == 4`.
- `reflect.DeepEqual(permissionOptionKinds, want)` — exactness + order.
- Every canonical value reports `Valid() == true`.
- Validity table: in-taxonomy (`PermissionOptionKindAllowOnce` → true),
  fabricated (`"nope"` → false), empty (`""` → false).

**`permission_test.go`:**
- *Construct + read-back via the constructor:* call
  `NewPermissionRequest("req1", "tc1", "Do you want to proceed?", opts)` with a
  two-element `[]PermissionOption` (an allow_once + a reject_once), assert every
  field round-trips, including option order and each option's `ID`/`Label`/`Kind`.
- *`PermissionRequest` is an `Event`:* `var _ Event = PermissionRequest{}` (compile-time)
  plus a runtime type-switch (reuse the `event_test.go` stream style) recovering
  it from a `[]Event`.
- *`PermissionResponse` selected case:* construct
  `PermissionResponse{RequestID:"req1", OptionID:"opt-allow", Cancelled:false}`,
  assert fields.
- *`PermissionResponse` cancelled case:* `{RequestID:"req1", Cancelled:true}`,
  assert `OptionID == ""` and `Cancelled == true`.
- *`PermissionResponse` is an `Inbound`:* `var _ Inbound = PermissionResponse{}`
  plus a runtime type-switch over a `[]Inbound`.
- *Option-kind validity through an option:* an option carrying a fabricated kind
  reports `Kind.Valid() == false` (proves the field is the enum, not a free
  string at the call site).

## Open questions

- **None blocking.** The inbound-home decision (§3) is resolved in-spec
  (establish `Inbound` now). The one forward note — whether the deferred
  inbound-commands ticket relocates `Inbound` to `inbound.go` — is that ticket's
  call, not a blocker here.
- `PermissionRequest.Title` vs richer tool context: deliberately minimal
  (correlate by `ToolCallID`, don't re-embed the tool call). If a downstream
  consumer (#607 wire mapping) finds it needs more request-side tool context, it
  surfaces there; not speculated here.

## Why not security-sensitive

Pure value types + one assembler: no I/O, no wire parse, no dispatch / gate /
route policy — the identical shape to #606 (unlabelled). The security review
belongs on the downstream slices that (a) parse the inbound `PermissionResponse`
frame from the phone (untrusted input) and (b) gate answering behind
`--allow-remote-permissions` + deny-on-timeout + the one-time nonce (ADR-025
§"Security model"). The label rides there, not here. (Confirmed against the
project's labelling rule: a slice earns the label only when it adds/changes a
gate, route, or trust-boundary parse — this adds none.)
