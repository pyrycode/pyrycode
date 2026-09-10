# `internal/turnevent` — neutral turn-event model

Pure-data leaf package. Declares the daemon-owned, neutral set of event types
for Phase 2/3 structured streaming (EPIC #596 / #597). Predominantly **outbound**:
six ACP-shaped outbound event structs (`TextChunk`, `ThoughtChunk`, `ToolStart`,
`ToolUpdate`, `TurnEnd`, and `PermissionRequest`, #700) plus three internal-only,
PTY-derived status-peer events (`Stall` #638, `ApiRetry` / `Compacting` #1074)
behind a sealed `Event` sum type. It also seals the
**inbound** members behind a sealed `Inbound` sum type — `PermissionResponse`
(#700, first) and `Cancel` (#707, the neutral remote-Esc / ACP `session/cancel`
command). Four string-backed ACP enums and a sealed `ToolContent` sum type round
it out. No transport, no I/O, no goroutines, no `context`, no `slog` — **standard
library only** (`encoding/json` for `json.RawMessage` is the sole import;
`permission.go` needs none).

The daemon owns this model; the mobile wire (now) and the future `pyry acp`
adapter (#600) are **thin adapters on top of it**. It is shaped ~90% like ACP so
the ACP adapter is near pass-through, but it is owned by us — so churn in the
external ACP spec stays inside the ACP adapter and never reaches the daemon core
or the mobile wire. Same containment logic the tui-driver substrate seal applies
to claude's screen.

This is the **neutral model core**: pure types, no wiring. It is the stable
contract the event-stream bridge (#608) maps tui-driver `Events()` **into** and
that the v2 wire types (#607) map **out of**. No `Events()` draining and no
envelope mapping live here. The outbound `Event` family landed in #606; #700
added the permission **request/response** seam (`PermissionRequest` outbound +
the first inbound member `PermissionResponse`, see [codebase/700.md](../codebase/700.md)).

- Decision anchor: [ADR 025](../decisions/025-mobile-remote-head-interactive-session.md)
  § "The event model".
- Design source-of-truth: *Structured-Event Bridge — internal model and ACP
  mapping* (Obsidian vault, `2026-04-10-pyrycode/structured-event-bridge-acp-mapping.md`).
- Spec: [`specs/architecture/606-neutral-turn-event-model.md`](../../specs/architecture/606-neutral-turn-event-model.md).
- Ticket record: [codebase/606.md](../codebase/606.md).

## Files

```
internal/turnevent/
├── event.go         Event sealed sum type; the 5 ACP-shaped event structs + the internal-only status peers Stall (#638), ApiRetry / Compacting (#1074) + the internal-only diagnostic Unrecognized; Location; value-receiver markers; var _ Event = … assertions
├── permission.go    (#700) PermissionRequest (outbound Event variant) + PermissionOption + NewPermissionRequest; Inbound sealed sum type + PermissionResponse (first member) + Cancel (#707, fieldless inbound command); markers + assertions. Zero imports.
├── taxonomy.go      ToolKind / ToolStatus / TurnEndReason / PermissionOptionKind (#700) enums + const blocks + unexported canonical slices + Valid() methods
├── content.go       ToolContent sealed sum type; TextContent / DiffContent / TerminalContent; markers; var _ ToolContent = … assertions
├── event_test.go    field round-trip, the Event-stream type switch, RawInput opacity
├── permission_test.go (#700) constructor round-trip, Event/Inbound membership type switches, response selected/cancelled cases, option-kind validity
├── taxonomy_test.go per-enum exactness (canonical slice == hardcoded ACP list + count guard), Valid() table
├── content_test.go  ToolContent shape recovery via type switch + nil-is-status-only
└── boundary_test.go AC#5 stdlib-only import boundary, enforced via go/parser
```

Four production files (~220 LOC), five test files (~280 LOC). Every type is a
~4-line declaration with no logic, no untrusted input, no concurrency.

## The three sum-type seams (sealed marker interfaces)

A neutral model that "the bridge maps **into**" and "the wire maps **out of**" is,
by construction, one ordered heterogeneous outbound stream, one polymorphic
content field, and (since #700) one inbound command set. All three are modelled
as Go's idiomatic **closed sum type**: a sealed interface with an unexported
marker method.

```go
type Event interface{ isTurnEvent() }         // event.go — outbound turn events
type ToolContent interface{ isToolContent() } // content.go — a ToolUpdate's content
type Inbound interface{ isInbound() }          // permission.go (#700) — inbound commands
```

- The unexported marker keeps the variant set **closed to this package**, so
  external ACP-spec churn cannot inject a variant. The bridge (#608) ranges a
  stream of `Event`; the wire adapter (#607) type-switches to map each kind.
- This is **not** a preemptive interface (which `CODING-STYLE.md` warns against):
  there are already seven / three / one concrete implementations and a known
  consumer (#608) that needs a single typed stream element. The sealed-marker
  exception is justified by the closed-set, known-consumer shape. `Inbound` is
  defined ahead of its consumer for the same reason the whole package is — it is
  the neutral contract the downstream inbound parser maps onto (see § The
  permission seam).
- Each marker is implemented on a **value receiver** (`func (TextChunk)
  isTurnEvent() {}`), so `TextChunk{}` — not only `&TextChunk{}` — satisfies the
  interface. The events are pure value types. Compile-time `var _ Event =
  TextChunk{}` (and one per content shape) assertions live alongside the markers.
- **Adding an `Event` variant means checking every production type switch over
  it, not just the totality gate.** `TestTurnMarkFor_TotalOverEveryVariant` is
  AST-derived and proves only that `turnMarkFor` (`cmd/pyry/stream_turn_busy.go`)
  is total — it says nothing about `cmd/pyry`'s `eventKind`
  (`interactive_turn_v2.go`), `interactiveTurnEmitterV2.Handle`, or
  `turnbridge.MapEvent`, none of which are exhaustiveness-checked by the build.
  **A prose claim that counts variants against a total is only good for as long
  as the total doesn't move, and nothing reddens when it does — #2134 is the
  second time this has bitten, and #2252 the third:** adding `SessionFacts` (with
  a `Handle` case) moved `eventKind`'s own literal count of Handle-covered
  variants from "17 of 18" to "18 of 20," corrected in place rather than found by
  the ticket's own Files-read list — the grep-for-the-digits check below is what
  caught it. `eventKind`'s `ModelAnnounced`/`SlashCommandList`
  arms each carried a sentence generalizing from "no production producer emits a
  variant without a `Handle` case", backed by a literal count ("17 of 18
  implementations, the one without is `PermissionRequest`"). `ConversationReset`
  is a variant `internal/streamsup` produces with a deliberately absent `Handle`
  case — the point of its AC4, not an oversight — so both sentences went false
  the moment it shipped, and being comments, neither test caught it. Corrected
  in place (`CORRECTED 2026-09-06 (#2134):`, the same house style
  `ignoredLineTypes` and `emitModelList` use) rather than rewritten, so the
  falsified claim stays legible next to its correction. The count appeared
  exactly once in the tree and the totality test carries no equivalent number,
  so one grep for the digits found the whole blast radius — the reusable check
  for the *next* variant that lands without a `Handle` case, rather than
  re-deriving it from scratch.
  **The AST walk enrolls a variant the moment its marker method is declared,
  independent of which files construct it (#2232).** `ToolCallDenied`'s marker
  lives in `internal/turnevent/event.go` alone; declaring it put a missing row
  in `cmd/pyry/stream_turn_busy_test.go`'s `TestTurnMarkFor_TotalOverEveryVariant`,
  in a package the ticket's own Files-read list never named. The check that
  would have caught it before the RED run: `grep -rn isTurnEvent` outside
  `internal/turnevent`, which finds `turnEventVariants` and its sole caller —
  the blast radius of a new `Event` variant is the set of marker-derived
  guards, wherever they live, not the files that construct the variant.
  `SlashCommandList` (#1854) landed with all four walked by hand: `Handle`'s
  `default` arm is a live drop site for it (no `case` claims the variant),
  `eventKind` got a name-only arm, the totality row asserts the `turnMarkFor`
  whitelist's existing answer, and `MapEvent`'s `default` stays armless. At
  #1854 time all four were *reachable-but-unreached*, since nothing produced the
  variant; `internal/streamsup` became the producer in #1877, and from that
  ticket `Handle`'s default and `MapEvent`'s default were both genuinely
  reached in production — armless is no longer the same claim as unreached, and
  the two doc paragraphs that conflated them (`eventKind`'s `ModelAnnounced` arm,
  in a category clause naming "anything else the production producer emits"
  rather than `SlashCommandList` by name) were the hardest of #1877's eight
  corrections to find by grep. #2003 gave `Handle` its `SlashCommandList` case,
  so `Handle`'s default is no longer a drop site for this variant at all — it
  now routes through `emitMapped` to `MapEvent`'s arm (landed #2001), which is
  itself no longer merely reached-in-principle but actually called. Deriving
  which drop sites are reachable for a new variant from its **turn mark** plus
  whether a `Handle` case claims it is the reliable method — copying a
  neighbouring arm's site-list prose is not: the `ModelList`/`ModelAnnounced`
  arms' own lists still name `acp_turn_stream.go`, deleted with the
  terminal-driving path in #1348.

The outbound `Event` variants themselves — the full field table for all
fourteen, `TurnEnd`'s per-ticket growth history, `Stall`/`ApiRetry`/`Compacting`,
and `Location` — moved to their own document, § Sections below, once this
file reached the 50000-byte package-overview cap; that section was the
majority of the file's bytes and the one still gaining a paragraph almost
every ticket that touches this package.

## The three content shapes (`content.go`) — all implement `ToolContent`

| Type | Fields |
|---|---|
| `TextContent` | `Text string` |
| `DiffContent` | `Path, OldText, NewText string` |
| `TerminalContent` | `TerminalID string` |

A consumer recovers the shape via `switch c := upd.Content.(type)`. `ToolUpdate`'s
`Content` is typed `ToolContent`; a **`nil` value is a legal "no content change"**
(status-only `ToolUpdate`), not an error — pinned by
`TestToolUpdate_NilContentIsStatusOnly`.

## The four enums (`taxonomy.go`)

String-backed (`type ToolKind string`, …): the values **are** the ACP taxonomy
strings, which keeps the model faithful and lets adapters marshal them directly.
Layout mirrors `internal/protocol/codes.go` (grouped, doc-commented const blocks,
named `<Type><Value>`).

| Enum | Exact ACP taxonomy values |
|---|---|
| `ToolKind` | `read, edit, delete, move, search, execute, think, fetch, other` (9) |
| `ToolStatus` | `pending, in_progress, completed, failed` (4) |
| `TurnEndReason` | `end_turn, max_tokens, max_turn_requests, refusal, cancelled` (5) |
| `PermissionOptionKind` (#700) | `allow_once, allow_always, reject_once, reject_always` (4) — the ACP `session/request_permission` kinds |

**Single source of truth → no drift.** For each enum, an **unexported canonical
slice** (`toolKinds`, `toolStatuses`, `turnEndReasons`, `permissionOptionKinds`)
is the one list that both `Valid()` scans and the exactness test asserts against
(deep-equal vs. an independent hardcoded ACP literal + a `len(...) == N` count
guard). A new const without a slice entry — or vice versa — fails the test.
Slice/predicate/const drift is structurally impossible. `PermissionOptionKind`
lives in `taxonomy.go` (not `permission.go`) so all four ACP enums and their SSOT
slices stay visible together and its exactness test sits beside its siblings.

```go
func (k ToolKind) Valid() bool // linear scan of the canonical slice
```

`Valid()` is the **taxonomy-checkability** at the seam (AC#4): the consumer
(#608/#607) calls it to reject an out-of-taxonomy value and map it to its own
wire/sentinel error. Perf is irrelevant — ≤9 elements, runs at seams, not hot
loops. The canonical slices stay **unexported** (no consumer needs to enumerate
yet); #607's wire mapping may add an exported accessor (e.g. `ToolKinds()
[]ToolKind` returning a copy) when it does.

## The permission seam (`permission.go`, #700)

The permission **request/response** pair is the one Phase 3 (#597) internal type
that is genuinely cross-cutting: both the mobile wire adapter (#597) and the
future `pyry acp` adapter (#600) map onto the same daemon-owned shape — ACP's
`session/request_permission` lands on this exact `PermissionRequest` later — so
it is shaped **adapter-neutral**, not mobile-specific. (Reclassifies #606's
tentative grouping, which listed `PermissionRequest` under "internal-only
events"; it follows `Stall`'s #638 precedent and graduates into `Event`.)

**`PermissionRequest`** (outbound `Event`): `RequestID` correlates the request to
its response; `ToolCallID` references the gating tool call by id — the same by-id
reference style `ToolUpdate` uses, *not* re-embedding tool detail — and is empty
when a prompt has no backing tool call; `Title` is the always-present
human-readable context (spanning the tool-triggered and prompt-only cases);
`Options` is an **ordered** `[]PermissionOption` (the consumer renders in order —
a slice, not a map).

**`PermissionOption`**: one selectable answer — `ID` (referenced back by the
response), `Label` (human-readable), `Kind PermissionOptionKind` (the ACP
semantic kind; the field is the enum, so `opt.Kind.Valid()` rejects a fabricated
value at the call site).

**`NewPermissionRequest(requestID, toolCallID, title, options)`**: the
AC-mandated, **non-validating** positional assembler (the three leading strings
are order-sensitive). It is the package's *only* constructor — a 4-field value
assembler, not a long-lived component, so no `Config` struct. It does not
validate; an out-of-taxonomy `Kind` is constructible and caught downstream via
`Valid()`, matching the package convention (see § Why no errors).

**`Inbound` + `PermissionResponse`** (the inbound seam): `PermissionResponse` is
the **first inbound member**, answering a `PermissionRequest` matched by
`RequestID`. `OptionID` names the selected `PermissionOption.ID` — the **option
id, not the kind enum** (the kind is request-side metadata; an easy subtlety to
get wrong in the wire mapping) — and is empty when `Cancelled` is true (the
consumer dismissed the modal without selecting). Exactly one of {`OptionID` set,
`Cancelled`} is meaningful; the package does **not** enforce the exclusivity —
validation is the inbound parser's job, consistent with the package's
construct-then-validate-downstream stance. No `Valid()` on the response (YAGNI
until the inbound parser needs one).

**`Cancel`** (#707, second `Inbound` member): an inbound command to **stop the
current turn** — the neutral form of a remote Esc / ACP `session/cancel` (#600).
**Fieldless** (`type Cancel struct{}`): the daemon has one live turn context, so no
correlation id is needed yet; #600 adds a session/turn id additively if ACP needs
one. The slot was reserved since #700 (the `permission.go:43` comment), and the
marker **stays in `permission.go`** beside its sibling — the relocation-to-own-file
hinted there is optional churn #707 declined. Crucially, `Cancel` is **declared
vocabulary, not a live producer path**: the mobile `interrupt` frame routes to Esc
directly via the `internal/relay` seam (`handleInterrupt`, #707), it does **not**
construct a `Cancel` value — exactly as the mobile `modal_cancel` frame routes to
`ModalResolver.ResolveCancel` rather than building a `PermissionResponse`. The
mobile-wire → neutral-`Cancel` translator is the ACP adapter's job (#600); `Cancel`
exists now so #600 has its target. See [codebase/707.md](../codebase/707.md).

## Why no errors, and no construction-time validation

The package returns **no errors** and does **no construction-time validation**.
Validity is a `bool` predicate (`Valid()`); the *consumer* at the seam rejects an
out-of-taxonomy value and maps it to its own wire/sentinel error. This is the
established project convention — *"refusal-to-wire-code mapping is the consumer's
job, NOT the primitive's"* (PROJECT-MEMORY § Project-level conventions; same
idiom as `internal/protocol`). The types are plain data; constructing an invalid
one is allowed and caught downstream via `Valid()`.

The one constructor — `NewPermissionRequest` (#700, AC-mandated) — is the sole
exception to the package's "no constructors" habit, and it upholds the rule it
lives under: a plain positional value assembler that does **not** validate. Every
other type is still built as a struct literal.

## Import boundary (AC#5)

`boundary_test.go` is a deterministic, stdlib-only safety net proving the package
depends on nothing but the standard library:

- Reads the package directory, parses each non-`_test.go` `.go` file with
  `go/parser.ParseFile(..., parser.ImportsOnly)`.
- Rejects any import whose **first path segment is dotted** — the signature of a
  module path (`github.com/…`, `golang.org/x/…`). A stdlib path's first segment
  (`encoding`, `go`, `os`, …) never contains a `.`.
- A second, redundant-but-clearer assertion names the most likely violation
  directly: no import has prefix `github.com/pyrycode/pyrycode/`.
- A `checked == 0` guard fails loud so the test can't pass vacuously.

This forbids importing **any** transport, relay, wire-protocol, or external
package — not an enumerated denylist — without a third-party linter. It is the
deterministic enforcement of the daemon-core/ACP-churn containment the package
exists to provide.

## Concurrency

**None.** Pure value types; no goroutines, channels, mutexes, or I/O. Values are
passed by value and immutable by convention. Thread-safety of any *stream* of
these events is the consumer's concern (#608 owns the stream); this package
imposes nothing.

## What's deliberately NOT in the package

- **The remaining inbound commands** — `Prompt`, `DropQueued`. #700 established
  the `Inbound` sealed sum type with `PermissionResponse` as its first member;
  **`Cancel` graduated off this list in #707** (see § The permission seam). `Prompt`
  and `DropQueued` join the set when their tickets land — a purely *additive*
  change. (The relocation of `Inbound` to its own `inbound.go` that #700 hinted is
  optional churn #707 declined; the marker stays in `permission.go`.)
- **Non-turn / internal-only events** — `BusyState`, `QueueState`,
  `ScreenSnapshot`. Out of scope for #606; a later ticket gives them a home.
  (Four types have already **graduated off** this list: `Stall` as an
  internal-only `Event` variant in #638, `PermissionRequest` as an outbound
  `Event` variant in #700, and `ApiRetry` / `Compacting` as `Stall`'s
  PTY-derived status-peer siblings in #1074 — see *The outbound `Event`
  variants* / *The permission seam* above.)
- **Any transport / wire / envelope mapping.** `Events()` draining is #608; v2
  wire types are #607; the ACP `stopReason` return conversion is the #600 adapter.
  Parsing the inbound `PermissionResponse` frame + the gate / nonce / deny-on-
  timeout modal loop are the security-sensitive downstream Phase 3 slices.
- **`Validate()` returning `error`, exported enumeration accessors.** YAGNI until
  a consumer needs them. (The package now has exactly one constructor,
  `NewPermissionRequest`, AC-mandated and non-validating — see § Why no errors.)

## Consumers (deferred — none wired in #606 or #700)

- `internal/turnevent` bridge core (#608) — maps tui-driver `Events()` into this
  model; ranges a `[]Event` / `chan Event` stream; the package #608 is blocked on.
- v2 wire types (#607) — type-switch each `Event` to map it OUT to the mobile wire
  (and map `PermissionRequest` onto the modal wire shape; may add an exported
  `PermissionOptionKind` accessor when it needs to enumerate).
- The inbound parser + gate / nonce / modal control loop (security-sensitive
  Phase 3 slices) — parse the inbound `PermissionResponse` frame and answer behind
  `--allow-remote-permissions` + deny-on-timeout + the one-time nonce.
- `pyry acp` adapter (#600) — near pass-through. Its **outbound `session/update`
  half is now built** ([acpbridge-package.md](acpbridge-package.md), #769): the
  pure `acpbridge.MapUpdate` maps `TextChunk`/`ThoughtChunk`/`ToolStart`/`ToolUpdate`
  out to ACP `session/update` payloads, drops `Stall` (and, since #1074, `ApiRetry`
  / `Compacting` the same way — no ACP equivalent for any of the three status
  peers), and reports "no notification" for `TurnEnd` (whose `stopReason` return
  the consumer re-derives from `TurnEnd.Reason`). Still deferred to later slices:
  the streaming consumer (#750), and mapping ACP's `session/request_permission`
  onto `PermissionRequest`.

## Sections

- [The outbound `Event` variants (`event.go`, `permission.go`)](turnevent-package-outbound-event-variants.md) — the full field table for all fourteen variants, `TurnEnd`'s per-ticket growth history (`ModelWindows`, the `result`-line stop shape, `ErrorCategory`, the #2260 duration/turn/cost numbers), `Stall`/`ApiRetry`/`Compacting`, and `Location`.

## Related

- [ADR 025](../decisions/025-mobile-remote-head-interactive-session.md) — mobile
  remote-head plan; § "The event model" + § "Wire-protocol extension".
- [codebase/606.md](../codebase/606.md) — ticket record (patterns + lessons).
- [codebase/638.md](../codebase/638.md) — the `Stall` internal-only variant +
  its v2 `stall` wire peer (data vocabulary; bridge is #624-B / #608).
- [codebase/1074.md](../codebase/1074.md) — `ApiRetry` / `Compacting`, `Stall`'s
  PTY-derived status-peer siblings, threaded through all five bridge layers
  (mapper, outbound adapter, `cmd/pyry` fan-out) in one ticket; unlike `Stall`
  each carries an explicit `Active` falling edge.
- [codebase/700.md](../codebase/700.md) — the permission request/response seam:
  `PermissionRequest` outbound, the `Inbound` sum type + `PermissionResponse`, and
  the fourth `PermissionOptionKind` enum.
- [codebase/707.md](../codebase/707.md) — `Cancel`, the second `Inbound` member
  (the neutral remote-Esc / ACP `session/cancel` target); declared here, routed to
  Esc via the `internal/relay` `Interrupter` seam (the mobile `interrupt` frame
  does not construct a `Cancel` — the `modal_cancel` precedent).
- [protocol-package.md](protocol-package.md) — the sibling pure-data leaf package
  whose const-block layout, drift-detector test pattern, and consumer-owns-wire-
  codes convention this package mirrors.
- [acpbridge-package.md](acpbridge-package.md) (#769) — the outbound ACP adapter
  that maps this model to ACP `session/update` payloads; the ACP mirror of
  `turnbridge`'s outbound `MapEvent`, and the first concrete piece of the `pyry
  acp` adapter (#600). Its kind/status pass-through is identity precisely because
  these enum values ARE the ACP strings.
- [codebase/595.md](../codebase/595.md) — Phase-1 sibling; the coarse `#589`
  fan-out this typed stream eventually replaces (§ Phase 2).
