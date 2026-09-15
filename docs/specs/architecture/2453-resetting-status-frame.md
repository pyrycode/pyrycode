# #2453 — declare the `resetting` status frame

Declaration-only ticket. It mints one envelope type, one payload struct and two
closed-set constant groups for the frame the daemon's own conversation-reset
routine will emit. The producer is #2455; the `/clear` routing that triggers it is
#2456. Nothing in this ticket emits the frame.

## Files read

- `internal/protocol/codes.go` → `TypeApiRetry` / `TypeCompacting` (the status-peer
  group the new constant sits next to) and `TypeUnrecognizedMessage` (the package's
  precedent for a status frame grouped alone because it is not a claude sub-state).
  Both block comments state the grouping criterion the new one has to answer.
- `internal/protocol/interactive.go` → `CompactingPayload` (the show/clear shape this
  frame extends), `CompactionBoundaryPayload` (the type that directly follows it and
  is documented as its pair), `ToolResultPayload` (the file's stated no-`omitempty`
  rule).
- `internal/protocol/system_prompt.go` → `SystemPromptStatusNoSession` /
  `SystemPromptStatusMatches` / `SystemPromptStatusDiffers` — the package's only
  precedent for a closed value set declared as named string constants, and the
  comment shape to copy.
- `internal/protocol/interactive_test.go` → `TestCompactingPayload_RoundTrip`,
  `TestCompactingPayload_FallingEdgeRoundTrip`, `roundTripEnvelope`; and
  `internal/protocol/envelope_test.go` → `readFixture`. The two helpers live in
  different files in the same package.
- `internal/protocol/compat_test.go` → `v2OnlyTypes` **and**
  `TestTypeConstants_V1V2Partition`'s `all` slice. See "the second guard entry" in
  Design — the AC names only the map, and the map alone reddens the build.
- `cmd/pyry/relay_guard_test.go` → `excludedTypes` and its `"push"` block;
  `TestEveryInboundV2TypeHasHandler` Assertion #3 is the detector that walks the
  AST of `codes.go`.
- `docs/knowledge/features/protocol-package-drift-detectors.md` — the lesson that
  changes how this ticket is built: `TestTypeConstants_V1V2Partition` is
  hand-maintained and does **not** fail on a constant left off both of its lists,
  while the relay guard's Assertion #3 does; and only a committed fixture catches a
  wire-*string* typo, because every registry entry keys on the Go symbol.
- `docs/protocol-mobile.md` → `§ compacting`, `§ compaction_boundary`,
  `§ slash_command_list`, and the `### Interactive events (v2, capability-gated)`
  intro. Read to size the documentation handoff; not edited (see below).

## Context

A conversation reset runs in two daemon-side phases — a wrap-up turn that writes a
handoff note for the successor, then a kill-and-respawn under a new session id.
Today a client sees neither: the screen simply pauses. This frame is the wire shape
that reports both phases and whether the note was actually written.

It joins the status-peer family of `compacting` and `api_retry` on shape — outbound
binary → phone, gated on the already-negotiated `interactive` capability, never
opening or closing a turn — and departs from every one of them on **provenance**.
Every status peer so far is the wire form of a stream-json detector reading claude's
own output. This frame's source is the daemon's own reset routine, so it is not a
`turnevent` variant and not one of the turn-stream events the interactive-events
intro counts. `slash_command_list` already draws that distinction in the same file.

No ADR is warranted: this adds a member to an established frame family and settles
no boundary the family has not already settled.

### Sizing — one boundary exceeded, and why this still ships as one ticket

Measured against the six-number boundary: **2** production source files
(`codes.go`, `interactive.go`), **~500** lines of total written work, **1** new
exported type, **0** consumer call sites, **0** state-machine reject branches — and
**6** acceptance criteria against a ceiling of 5.

The ceiling says split; the floor rule says do not, and the floor wins. Every slice
this ticket could be cut into produces a child whose only deliverable is consumed by
exactly one sibling inside the same family: a constant nothing decodes into, a
payload struct with no envelope type to ride, or fixtures pinning a shape that does
not exist yet. None of those can be verified standing alone. The named analogue
(#2256, `banner`) landed constant, payload, fixtures and docs together for the same
reason. Split depth was checked before this conclusion — #2453 has no parent and no
grandparent, so depth was not the constraint.

Two of the six criteria are also not work: AC 6 is a negative fence naming files
that stay untouched, and AC 2 is the compile requirement of AC 1 rather than a
deliverable of its own — a constant added without both guard entries fails
`make check` immediately.

The overage is recorded here rather than acted on. Nothing is deferred by it.

## Design

### 1. `TypeResetting` — its own const group in `codes.go`

A new `const` block placed immediately after the `TypeApiRetry` / `TypeCompacting`
group and before the `TypeUnrecognizedMessage` block:

```go
const (
    TypeResetting = "resetting" // binary → phone, outbound v2 conversation-reset status
)
```

It does **not** join the group above it. That group's comment reads "PTY-derived
status peers of `TypeStall`" and counts its own two members; this frame is derived
from no claude output at all. `TypeUnrecognizedMessage` is the package's precedent
for exactly this — grouped alone, with a block comment that states which sibling
group it declines to join and why. The block comment here states: the
daemon-reset-routine provenance, that the frame is outbound and never dispatched
inbound, and the standing `MUST NOT be added to inboundAppTypeSet` rule both
neighbouring blocks carry.

### 2. `ResettingPayload` and the two closed sets in `interactive.go`

```go
const ( ResetPhaseWrappingUp, ResetPhaseRestarting = "wrapping_up", "restarting" )   // sketch; declared one per line
const ( ResetHandoffPending, ResetHandoffWritten, ResetHandoffSkipped = "pending", "written", "skipped" )

type ResettingPayload struct {
    ConversationID string `json:"conversation_id"`
    Active         bool   `json:"active"`
    Phase          string `json:"phase"`
    Handoff        string `json:"handoff"`
}
```

Two separate `const` groups, one per field, each with a block comment stating the set
is CLOSED and what each member means — `SystemPromptStatus*`'s shape, the package's
only precedent for named closed-set values. The `Reset` prefix rather than
`Resetting` keeps the constant reading as the domain ("a reset's phase") rather than
as the frame's name, which is what `SystemPromptStatus*` does for its own field.

**Placement.** The type lands immediately after `CompactionBoundaryPayload`, not
between it and `CompactingPayload`. Those two are a documented pair in both this file
and the spec — `CompactingPayload`'s closing paragraph forward-references
`compaction_boundary` as the frame that carries what it cannot — and splitting them
would break a reference that reads as adjacency. The new type is still inside the
compacting neighbourhood the AC asks for.

**No `omitempty` on any field**, per the rule this file states at
`ToolResultPayload`: the key is always on the wire and its zero value is the
statement. On a falling edge `Phase` and `Handoff` are both the empty string, and
that empty string is the frame saying "no phase is in progress" rather than a missing
key a client has to infer from.

**The two properties the doc comment must carry**, because a decoder gets both wrong
otherwise:

1. One reset emits **two** `active: true` frames — `wrapping_up`, then `restarting` —
   before a single `active: false`. This is where the frame departs from
   `compacting`'s strict edge pair. A repeated `active: true` carrying a new `phase`
   is a phase change, not a second reset.
2. `Phase` and `Handoff` are meaningful **only while `Active` is true**. `Handoff` is
   `pending` throughout `wrapping_up` and resolves to `written` or `skipped` on
   `restarting`, which is what lets a client say whether a note was made.

No capability string is added. The existing `interactive` gate covers the frame, so
the spec's "Defined capability strings" table is untouched.

### 3. The two guard entries — and the second `compat_test.go` edit the AC does not name

- `cmd/pyry/relay_guard_test.go`: `"TypeResetting": "push"` in `excludedTypes`, as its
  own commented entry rather than appended to the anonymous `"push"` block, because
  the block's members are all stream-json-derived and this one is not. This slice
  declares no inbound verb, so `"push"` is true rather than borrowed. Mandatory from
  the moment the constant exists: Assertion #3 walks `codes.go`'s AST and reports an
  unclassified constant, not an unemitted one.
- `internal/protocol/compat_test.go`: `TypeResetting: true` in `v2OnlyTypes` **and**
  `TypeResetting` in `TestTypeConstants_V1V2Partition`'s `all` slice. The AC names
  only the map, and the map alone is not enough — that test closes with
  `len(inboundAppTypeSet)+len(v2OnlyTypes) == len(all)`, so a map entry without a
  slice entry reddens the count assertion with a bare size mismatch. Both lists are
  hand-maintained literals; the drift-detector overview records that neither fails on
  a constant left off *both*, which is why the relay guard is the one that actually
  catches an omission here.

## Concurrency model

None. `internal/protocol` is a pure-data leaf package: no goroutines, no `context`,
no I/O, no shared mutable state. Nothing in this ticket changes that, and no
shutdown path exists to extend.

## Error handling

No validation and no error paths. This is a declaration: `encoding/json` decodes the
four keys into the struct and an unknown `phase` or `handoff` token decodes to
whatever string arrived. The closed sets are a **client contract**, not a decoder
gate — the package validates no payload today and this frame does not start.

The daemon is the sole author of every value, so an out-of-set token can only be a
daemon bug, and the producer (#2455) is where a switch over the named constants
belongs. A client's own decoder should treat an unrecognised token as *unknown*
rather than as an error, the standing rule for every token field on this wire.

## Testing strategy

Four committed fixtures under `internal/protocol/testdata/`, each with a round trip in
`TestCompactingPayload_RoundTrip`'s shape — read the fixture, assert `env.Type`,
decode the payload, assert the four fields, then `roundTripEnvelope` for the
struct → wire byte equality. All four use `conversation_id: "c1"`, matching the
compacting fixtures the tests are modelled on.

| Fixture | Edge | Asserts |
|---|---|---|
| `resetting.json` | rising | `active: true`, `wrapping_up`, `pending` |
| `resetting_restarting_written.json` | rising | `active: true`, `restarting`, `written` |
| `resetting_restarting_skipped.json` | rising | `active: true`, `restarting`, `skipped` |
| `resetting_ended.json` | falling | `active: false`, both closed-set fields empty |

The fixtures are the only thing in this ticket that can catch a wire-*string* typo.
Every registry entry in both guards keys on the Go symbol, so a mutated constant
*value* moves consistently through all of them and reddens nothing — measured on
#1895 and recorded in the drift-detector overview. Each test asserts the field values
against the named constants where a closed set is involved, so a typo in a constant's
value reddens the fixture comparison rather than being compared against itself.

Gate: `go test -race ./internal/protocol/... ./cmd/pyry/...`, `go vet ./...`,
`go build ./cmd/pyry`.

## Documentation handoff

Pending for the documentation stage. Not written here — `docs/protocol-mobile.md` is
a shared protocol reference the documentation stage owns, and `docs/knowledge/` is
closed to this role.

1. **`docs/protocol-mobile.md` (AC 5 of #2453, carried forward in full).** A
   `#### resetting` block under `### Interactive events (v2, capability-gated)`,
   placed beside `compacting` — after `#### compaction_boundary` and before
   `#### banner`, so the documented `compacting` → `compaction_boundary` pair stays
   adjacent, matching the type order this ticket uses in `interactive.go`. It states:
   direction binary → phone; the `interactive` capability gate; that it never opens
   or closes a turn; the field table for `conversation_id` / `active` / `phase` /
   `handoff` with both closed sets enumerated; **both** numbered properties from the
   Design section above; and the invariant that every reset's rising sequence ends in
   a falling edge — on success **and on every error path**. The daemon-routine
   provenance is stated in `slash_command_list`'s wording — not a `turnevent` variant,
   not one of the turn-stream events the intro counts — so the intro's count sentence
   stays unchanged.
2. **`docs/protocol-mobile.md` § `slash_command_list`, SECURITY paragraph (AC 5,
   second half).** That paragraph currently closes by saying inbound message text
   reaches a path that does not treat it as a command vocabulary, and that the frame
   declares no inbound verb. It gains a dated changelog note naming the one exception
   #2456 introduces — the literal `/clear`, matched once before routing — marked as
   **not yet live**. **Ordering constraint:** this note must land before or together
   with #2456. Until it does, the spec states that inbound message text reaches no
   command-vocabulary path while a `/clear` match exists — a claim the code would
   have falsified. Nothing here can be deferred past that ticket.
3. **`docs/knowledge/features/protocol-package-constants-codes-go-envelope-types.md`**
   — record `TypeResetting` and why it is grouped alone.
4. **`docs/knowledge/features/protocol-package-interactive-event-payloads.md`** —
   record `ResettingPayload` beside its `CompactingPayload` entry, including the
   two-rising-edges property and the daemon-routine provenance that separates it from
   every other status peer.

## Open questions

1. **Is `ResettingPayload` placed after `CompactionBoundaryPayload` or directly after
   `CompactingPayload`?** Resolved in Design § 2 before implementation: after
   `CompactionBoundaryPayload`, to keep the documented pair adjacent.
2. **Does `v2OnlyTypes` alone satisfy AC 2?** Resolved in Design § 3: no — the `all`
   slice in the same file must move with it or the count assertion reddens.
3. **Fixture envelope ids.** Non-load-bearing; 910–913 with a 2026-09-15 timestamp,
   chosen only to avoid colliding with a committed fixture.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** SHOULD FIX — the design has **no** untrusted-to-trusted
  crossing, and the risk is that the doc comment fails to say so. All four fields are
  daemon-authored: `ConversationID` is the daemon's own registry key, `Active` is a
  bool the daemon computes, and `Phase` / `Handoff` are tokens the daemon selects from
  the named constants this ticket declares. That is a class difference from the
  neighbour the type sits beside — `CompactingPayload.ErrorText` carries `claude`'s own
  unsanitized prose and carries a SECURITY paragraph accordingly. Copying that
  paragraph onto this type would tell a client this frame carries untrusted text and
  send it down the wrong render path; omitting any statement would leave a client to
  infer the neighbour's rules from adjacency. **Phase B must state the contrast
  positively in `ResettingPayload`'s doc comment**, and the documentation handoff must
  carry the same statement into the spec section. The failure mode is measured, not
  hypothetical: #2253 records a justification copied along with a neighbouring test's
  shape into a home where it was false.
- **[Trust boundaries]** No finding, by an explicit design decision worth naming — the
  handoff note's **content never crosses this wire**. The note is prose a `claude`
  wrap-up turn writes, which would be the frame's one genuinely untrusted value;
  `Handoff` is a three-token closed set reporting only *whether* a note was made.
  A future field carrying the note's text would need a bound, a sanitization
  statement and a render rule, and this frame declares none because it carries no such
  field.
- **[Tokens, secrets, credentials]** No findings — no credential, no new identifier and
  no entropy source. `ConversationID` is the routing key every interactive event
  already carries, not an authorization token. Note what is deliberately absent: the
  `restarting` phase says a restart is happening and **does not name the new session
  id**, keeping the package's standing rule that `claude`'s `session_id` never crosses
  this wire. Session-boundary identity has its own frame, `session_transition`.
- **[File operations]** Not applicable by design decision — `internal/protocol` is a
  pure-data leaf package and no path, mode or file handle appears anywhere in this
  ticket. The handoff note is a real file, but it is written by the reset routine in
  #2455; its mode and atomicity are that ticket's to decide.
- **[Subprocess / external command execution]** Not applicable by design decision — no
  field here reaches a child process as an argv element, because this ticket declares
  no producer and no consumer. The kill-and-respawn the `restarting` phase reports is
  #2455's subprocess work, named as out of scope.
- **[Cryptographic primitives]** Not applicable — no randomness, no hashing, no key
  material, and no comparison against a secret.
- **[Network & I/O]** No findings, and this is the category the `security-sensitive`
  label is actually for. A hostile phone sending `{"type":"resetting"}` inbound must
  reach nothing, and two structural facts make that true: absence from
  `inboundAppTypeSet` makes `IsKnownAppType` reject it, and the `excludedTypes`
  classification records that no `dispatchAppFrame` arm exists. The frame therefore
  answers with `dispatch.Route`'s unknown-type reply rather than actuating a reset.
  **The guard entries in AC 2 are that property's enforcement, not bookkeeping** —
  which is why they are mandatory from the moment the constant exists rather than from
  the moment something emits it. No size cap is owed: the two token fields are selected
  from named constants, not read from input.
- **[Error messages, logs, telemetry]** No findings, by an explicit design decision —
  **this frame declares no prose channel at all**. `handoff: skipped` is a token, not a
  reason string, so nothing here can leak a path, an internal state or a stack trace.
  The natural follow-up request ("*why* was it skipped?") would open exactly that
  channel, and whoever declares such a field owes it a bound and a sanitization
  statement, as `compact_error` carries next door.
- **[Concurrency]** Not applicable by design decision — the package has no goroutines,
  no locks, no `context` and no shared mutable state, and this ticket adds constants,
  one struct and tests.
- **[Threat model alignment]** No findings — `docs/protocol-mobile.md` § Security
  model's threat 1 (prompt injection, in the outward direction) is the threat that
  lands on every interactive frame carrying model- or workspace-authored text, and it
  does **not** land here because no field does. Stating that in the spec is the
  documentation handoff's job; leaving it unstated is the first finding above.
- **[Threat model alignment]** SHOULD FIX — the `/clear` changelog note deferred to the
  documentation stage is a spec-accuracy risk with a security framing: the
  `slash_command_list` SECURITY paragraph asserts that inbound message text reaches no
  command-vocabulary path, and #2456 makes that assertion false. This role cannot edit
  that file, so the fix is the ordering constraint now recorded in
  § Documentation handoff item 2 — the note lands before or with #2456, never after.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-15
