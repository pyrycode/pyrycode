# #2256 — the `banner` frame for claude's operator-facing text

Ships the wire type, its payload, the `turnevent` variant, the `turnbridge` outbound
arm and the two arms in the v2 interactive emitter. **No producer**: the `streamsup`
mappings are #2257 (`informational`) and #2258 (`notification`), and the 4 KiB cap
lands with the first of those.

## Files read

- `internal/protocol/codes.go` → `TypeCompactionBoundary`, `TypeToolDenied`,
  `TypeSlashCommandList` — the const-block-plus-doc shape a new outbound v2 type takes,
  and the standing "the NAME is the daemon's, not claude's" argument each repeats.
- `internal/protocol/interactive.go` → `CompactionBoundaryPayload`, `CompactingPayload`,
  `ToolDeniedPayload` — the three docs this ticket's field docs are built from.
  `CompactionBoundaryPayload.Trigger` argues the drop-versus-cut split in full;
  `CompactingPayload.ErrorText` carries the SECURITY paragraph on claude-authored prose;
  `ToolDeniedPayload` states the one-cap-site rule.
- `internal/turnevent/event.go` → `CompactionBoundary`, `ModelRefusalFallback`, the
  `isTurnEvent` marker block and the `var _ Event` assertions — the variant shape, and
  the marker list the totality guard derives its expected set from.
- `internal/turnbridge/outbound.go` → `MapEvent`'s `CompactionBoundary` arm — the
  conversation-only mapping, and its statement that a second bound in the adapter would
  be a number to keep in step with one that already holds.
- `cmd/pyry/interactive_turn_v2.go` → `Handle`, `eventKind`, `emitMapped` — the
  no-lifecycle-mutation arm shape, and the rule that nothing derived from claude's text
  reaches a log field.
- `cmd/pyry/stream_turn_busy_test.go` → `TestTurnMarkFor_TotalOverEveryVariant`,
  `turnEventVariants` — the marker-derived guard. It reddens on the new marker alone,
  so it is this ticket's RED.
- `cmd/pyry/relay_guard_test.go` → `excludedTypes`; `internal/protocol/compat_test.go` →
  `v2OnlyTypes` and the `all` slice — the other two mechanical rows.
- `internal/protocol/interactive_test.go` → `TestSlashCommandListType_IsNotClaudesVocabulary`,
  `TestCompactionBoundaryPayload_RoundTrip`, `readFixture`, `roundTripEnvelope` — the
  pin this ticket copies the *shape* of and must not copy the *check list* of.
- `internal/e2e/realclaude/testdata/operator_system_lines_v2.1.259.json` → the single
  captured `informational` line: `level` `warning`, `prevent_continuation` `true`, and a
  `content` that embeds a host path and echoes the operator's own prompt back.
- `docs/protocol-mobile.md` § `compaction_boundary` — the section shape: field table,
  then the paragraphs, then a changelog entry.
- `docs/knowledge/features/protocol-package-interactive-event-payloads.md` — the
  one-cap-site lesson (`RateLimitedPayload` precedent) and the report-slice-vocabulary
  lesson. Neither applies to a frame with no report slice, which is itself worth
  stating: `truncated` is a bool, not a `[]string`, so no key-translation seam exists.
- `docs/knowledge/features/turnbridge-package.md` § "Not `security-sensitive`" — the
  bridge is a pure value-to-value mapper and the capability decision lives in the
  emitter. That is why this ticket's `security-sensitive` weight sits in `cmd/pyry`.

## Context

Nothing on the v2 wire carries free operator-facing text. `unrecognized_message` is a
parser-gap diagnostic, `compacting` and `rate_limited` each report one machine state,
and the assistant stream carries only what the model said. Text claude prints *about*
the session — a hook's block reason, a local command's output, a loop notification —
is dropped.

This ticket gives that text one typed place to arrive, with no producer wired. The
frame lands ahead of its producers on this repo's established sequencing (#2052→#2054,
#1983→#1984), which lets the client slices start against a published contract.

No ADR is owed. Every design choice below is an application of a rule already recorded
at a sibling symbol, and the one genuinely new decision (the `banner`/`Banner`
vocabulary split, below) belongs in the type's own doc comment where a reader meets it.

## Design

### The wire type — `protocol.TypeBanner = "banner"`

A const block of its own in `codes.go`, grouped alone rather than with
`api_retry`/`compacting`. Those are show/clear sub-states with two edges; this is a
single report with none, and its producer set spans two unrelated claude subtypes.

The name is the daemon's. claude's words on this path are the subtypes
`informational` and `notification`, and the payload keys `content`, `level` and
`prevent_continuation`. `banner` derives from none of them — which is what keeps a
claude rename landing in one place instead of breaking every client at once.

**The `banner` / `Banner` split is deliberate and gets a sentence in the type's doc.**
`turnevent.ModelRefusalFallback.Banner` already renames a *different* claude `content`
to `banner` as a FIELD. Here `banner` is the FRAME and the field is `text` — the frame
names what the thing IS to a client, the field what it HOLDS. A reader meeting the
second rename needs that stated rather than inferred. The word also appears in the
emitter's `ApiRetry`/`Compacting` arm meaning tui-driver screen chrome; that sentence
stays true of those two variants and is not edited.

### The variant — `turnevent.Banner`

```go
type Banner struct {
    Level     string // claude's own key, adopted verbatim; open set
    Text      string // claude's content, renamed
    Truncated bool   // whether the PRODUCER cut Text
    StopsTurn bool   // claude's prevent_continuation, renamed
}
```

Plus `func (Banner) isTurnEvent() {}` and its `var _ Event = Banner{}` assertion. No
conversation identity of the daemon's — the bridge injects that, as every sibling does.

`Level` and `Text` take **opposite** bound rules, and the rules belong to the producer.
`Level` is a token a client MATCHES, so an over-long one is DROPPED — a cut token
matches no known value while still looking like one. `Text` is prose, so an over-long
one is CUT — a cut sentence still reads as what it is. That is
`CompactionBoundaryPayload.Trigger`'s argument applied to a pair on one frame instead
of across two. An unrecognised `Level` is not an error: `TurnEnd.Outcome`'s open-set
rule.

`Truncated` carries the producer's answer about `Text` and nothing else. No companion
`DroppedFields` slice: `Text` is the only field the daemon cuts, and an emptied `Level`
is directly observable, exactly as `CompactionBoundary.Trigger`'s unreported drop is.

### The payload — `protocol.BannerPayload`

Keys `conversation_id`, `level`, `text`, `truncated`, `stops_turn`. No `omitempty` on
any, per this file's rule as stated at `ToolResultPayload`. **No `turn_id`**, and the
reason is the producer set rather than taste: a prompt a hook refuses is never
answered, and `notification` belongs to claude's own queue and rides no turn, so there
is no turn the daemon could honestly attribute the frame to. `compacting` and
`unrecognized_message` are the conversation-scoped precedents.

Every field is a value type — two strings and two bools, no pointer and no slice —
unlike `CompactionBoundaryPayload`'s counts and `ToolDeniedPayload`'s report slices. So
this frame has no aliasing question at either seam and needs no defensive copy.

**The attribution obligation is part of the contract, not an aside**, and the payload
doc and `docs/protocol-mobile.md` § `banner` both carry it in the terms the security
review below fixes: this is the first v2 frame whose whole purpose is arbitrary
claude-authored prose with no machine state anchoring it, so a client renders it as a
first-class notice. Text impersonating daemon chrome at `level: "warning"` is the
realistic abuse, and nothing else on the wire contradicts it. Render it as **claude's
assertion**, attributed to claude, never as the daemon's own finding —
`QuestionDismissedPayload`'s trap — and as inert text on
`UnrecognizedMessagePayload.Raw`'s rule, never an HTML sink, an attribute or a URL.

**`stops_turn` is a REPORT, never an actuator, and the constraint is stated AT the
field** in both the variant and the payload rather than only at the type. The field
NAME is what invites the mistake — it reads like a lever, and a daemon that ever keyed
on it would hand claude a self-service turn abort. Nothing in the daemon acts on it or
on any other field here, which is what keeps a fabricated banner a misleading label.

**The 4 KiB bound on `text` is a contract the PRODUCER owes**, named at the field with
its ticket (#2257) so the obligation is written down rather than assumed. This ticket
ships no producer, so nothing today can construct an oversized payload; the field doc
is what stops the producer landing with no cap site at all.

### The bridge arm — `turnbridge.MapEvent`

Conversation identity only; `tc.TurnID` and `tc.Seq` are ignored and the payload has no
field for either. All four claude-side values cross **verbatim**: nothing is bounded,
dropped, cut, defaulted or recomputed here. The cap is decided once, at the producer
(#2257), on `ToolDeniedPayload`'s stated rule — a second cap site is a second place the
limit is decided, and the two could disagree silently. In particular the bridge does
**not** recompute `truncated` from `len(Text)`: that would make the bridge a second
authority on a fact the producer already established.

### The emitter arms — `cmd/pyry/interactive_turn_v2.go`

`Handle` gains a case that flushes any pending delta (so buffered text keeps its wire
position ahead of the report) and calls `emitMapped`. **No turn-lifecycle mutation** —
no `startTurnIfNeeded`, no `transitionTo`, no `endTurn`; `inTurn`, `turnID` and
`currentState` untouched. Kept a separate case from the `CompactionBoundary` and
`ApiRetry`/`Compacting` arms despite the identical body, following that switch's own
rule that it merges arms sharing a REASON: those report machine states and history
marks the daemon observed, this carries text claude wrote for a person.

Opening a turn here would wedge the conversation exactly as opening one on an
unrecognized message would — a `notification` rides no turn, so no turn end follows.
`turnMarkFor`'s whitelist default already answers `turnMarkNone` and needs no arm.

`eventKind` gains a case returning `"banner"` — the variant name only. `Text` and
`Level` are exactly what a log line explaining a banner would reach for, and neither is
returned: this feeds log fields, and the package rule is that nothing derived from
claude's output reaches a log.

### The three mechanical rows

- `cmd/pyry/stream_turn_busy_test.go` — a `turnMarkNone` row in
  `TestTurnMarkFor_TotalOverEveryVariant`. Marker-derived, so it fails without the row.
- `cmd/pyry/relay_guard_test.go` — `"TypeBanner": "push"` in `excludedTypes`.
- `internal/protocol/compat_test.go` — `TypeBanner: true` in `v2OnlyTypes`, and the
  entry in the `all` slice.

## Error handling

No failure modes are introduced. `MapEvent` is pure and total over its input, so the arm
cannot error, and `json.Marshal` on a closed string/bool struct cannot fail in practice
(`emit`'s existing defensive branch covers it and is untouched).

The one real hazard is silence: a missing arm in either emitter switch drops the frame
with nothing red. That is why AC 2 exists, and why the emitter test asserts the frame
ARRIVES at a connected client rather than asserting the arm compiles.

## Concurrency model

None added. `MapEvent` stays pure and synchronous. The emitter arm runs on the existing
`Handle` goroutine and takes no lock its neighbouring cases do not.

## Testing strategy

RED first: adding the `isTurnEvent` marker reddens
`TestTurnMarkFor_TotalOverEveryVariant` before any other line is written.

- **`internal/protocol`** — `TestBannerPayload_RoundTrip` against a committed
  `testdata/banner.json`, byte-exact via `roundTripEnvelope`. Values follow the
  capture's shape (`level: "warning"`, `stops_turn: true`) with a SHORT synthetic
  `text`: the captured prose embeds a host path and echoes the operator's prompt, and a
  protocol fixture needs neither to pin a shape.
- **`internal/protocol`** — `TestBannerType_IsNotClaudesVocabulary`. Name half: not
  equal to and not derived from `informational`, `notification`, `content`,
  `prevent_continuation`; plus the exact-equality pin on `"banner"`. Payload half: a
  populated payload's bytes carry neither `content` nor `prevent_continuation`.
  **`level` is in NEITHER list** — it is adopted verbatim and is this payload's own wire
  key, so checking it would be RED against the correct implementation. That is the trap
  the sibling's doc records for `commands`, and the doc comment says so.
- **`internal/turnbridge`** — the mapping test, and AC 3 is its load-bearing half: an
  unrecognised `level` token and a `text` longer than 4 KiB both arrive byte-identical,
  with `truncated` carrying the value the EVENT held. The discriminating row is a long
  `text` with `Truncated: false` — a bridge that recomputed the flag from length would
  pass every other row and die only on that one.
- **`cmd/pyry`** — the emitter test: a `Banner` event reaches a connected client as a
  `banner` envelope with the payload intact, a pending delta is flushed ahead of it, and
  `inTurn`/`turnID`/`currentState` are unchanged across the call. Both halves of AC 2
  are asserted through the wire rather than through the switch.

## Documentation

`docs/protocol-mobile.md` gains three things, following `compaction_boundary`'s shape:
a row in the frame index table, a `#### banner` section (field table, then the
no-`turn_id` argument, then the opposite bound rules, then the attribution paragraph
the security review requires), and a changelog entry. § Security model's threat 1 is
cited in the outward direction, as `compaction_boundary`'s section cites it.

The section states that the frame carries **no data class an interactive grant does not
already receive** — the captured text embeds a host path and echoes the operator's own
prompt, and `tool_use`'s verbatim input fields already carry both to the same grant —
which is why no narrower capability gate is introduced.

## Open questions

1. Whether the emitter arm merges with the `CompactionBoundary` case. Resolved in
   Design: separate, on the switch's reason-not-body rule.
2. Whether `truncated` needs a `dropped_fields` companion for an emptied `level`.
   Resolved: no — an empty scalar is directly observable, `CompactionBoundary.Trigger`'s
   answer.
3. Whether the fixture carries the capture's real prose. Resolved in Testing strategy:
   no.

## Security review

**Verdict:** PASS (second pass; the first returned FAIL on the trust-boundary finding
below, and the Design and Documentation sections above are the revision that answers it)

**Findings:**

- **[Trust boundaries] MUST FIX — addressed in this revision.** This is the first v2
  frame whose entire purpose is arbitrary claude-authored prose with no machine state
  anchoring it. `CompactingPayload.ErrorText` is prose attached to a compaction the
  daemon itself observed; `UnrecognizedMessagePayload.Raw` is prose explicitly labelled
  a parser gap. A client renders this one as a first-class notice, so text impersonating
  daemon chrome at `level: "warning"` is the realistic abuse, and — as with
  `CompactionBoundaryPayload` — nothing else on the wire contradicts a fabricated one.
  The first draft cited `CompactingPayload.ErrorText`'s SECURITY paragraph in the
  reading list but committed neither the payload doc nor the docs section to stating the
  attribution obligation. A wire contract that does not tell client implementers this
  text is untrusted IS the exploitable gap. The revision commits both, in
  `QuestionDismissedPayload`'s attribution terms and
  `UnrecognizedMessagePayload.Raw`'s inert-render terms.
- **[Trust boundaries] No further findings on the boundary's shape.** Two seams,
  each explicit and single: `turnbridge.MapEvent` is the one translation point and the
  emitter's `emit` the one wire point. The type system does NOT signal untrustedness —
  `Text string` is indistinguishable from a daemon-authored string — and the mitigation
  is a doc comment. That is the package's established posture at every sibling payload,
  not a gap this ticket introduces, and changing it is out of scope.
- **[Trust boundaries] The subprocess → parent boundary is not crossed here.** The bridge
  and emitter receive an already-constructed `turnevent.Banner`; the stdout decode is
  #2257's and #2258's.
- **[Error messages, logs, telemetry] No findings.** `eventKind` returns the variant name
  only — not `Text`, not `Level` — which matters because three other call sites
  (`acp_turn_stream.go`, `stream_turn_busy.go`, `stream_turn_drain.go`) feed its result
  into log fields. `emitMapped`'s drop Debug logs `eventKind` alone, and `emit`'s
  marshal-failure branch already declines to echo the payload; neither is modified.
- **[Network & I/O] SHOULD FIX — addressed in this revision.** No bound is enforced
  anywhere on this ticket's path, deliberately: the cap belongs to the producer on
  `ToolDeniedPayload`'s one-cap-site rule, and no producer exists yet, so no input path
  can construct an oversized payload today. The risk is the opposite of a disagreeing
  second cap — a producer landing with NO cap site — so the 4 KiB bound is now named at
  the field with its ticket (#2257) as a written obligation. No envelope-fit test is
  owed: every `FitV2EnvelopeCap` test in the repo guards an aggregate whose per-field
  caps compose badly, and one 4 KiB string against a 65519-byte envelope is not that
  shape; a copy would carry the cap as a second literal that could not catch the
  constant moving.
- **[Threat model alignment] No findings.** § Security model's threat 1 lands in the
  outward direction and the docs section cites it, as `compaction_boundary`'s does. The
  capability question was checked rather than assumed: the frame carries no data class an
  interactive grant does not already receive — the captured text embeds a host path and
  echoes the operator's prompt, and `ToolUsePayload.Input`'s verbatim top-level fields
  already carry both to the same grant — so no narrower gate is warranted.
- **[Concurrency] No findings.** `MapEvent` stays pure and synchronous; the emitter arm
  runs on the existing `Handle` goroutine and takes no lock its neighbours do not. No
  goroutine is spawned, so there is no lifecycle to leak. Every field is a value type,
  so unlike `CompactionBoundaryPayload`'s `*int` pair and `ToolDeniedPayload`'s report
  slices there is no aliasing question at either seam.
- **[File operations] Not applicable by design.** The ticket opens, creates, reads and
  writes no file. The one committed artifact is a static test fixture under
  `internal/protocol/testdata/`, hand-authored rather than produced by a run, and it
  deliberately omits the captured prose (which embeds a host path and the operator's own
  prompt) in favour of a short synthetic string.
- **[Subprocess / external command execution] Not applicable by design.** No
  `exec.Command`, no shell, no environment handling; the subprocess seam is the
  producer's and lands with #2257.
- **[Cryptographic primitives] Not applicable by design.** No randomness, no key
  material, no comparison against a secret. The envelope's AEAD seal is `emit`'s and is
  untouched.
- **[Tokens, secrets, credentials] Not applicable by design.** No credential is
  generated, stored, logged, rotated or revoked on this path.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-10

## Notes on process

**Sizing: over the 800-line ceiling, built anyway on the floor rule.** The re-count at
plan-commit time lands near 830 lines of total written work — roughly 600 of code, tests
and docs, plus this plan. Five production files, two new exported types, four consumer
rows, four acceptance criteria and no reject branches all sit inside their limits; only
the line total is over, by about 4%.

No split is proposed, and the reason is the floor rather than the depth gate — the gate
does not fire here (parent #2197, no grandparent). Every seam available to cut is a
one-consumer seam inside this family: a payload whose only caller is the bridge arm, a
bridge arm whose only caller is the emitter. AC 2 names the consequence directly — both
switches default silently, so a frame whose arms land in a separate ticket drops with
nothing red and cannot be verified on its own. The floor beats the ceiling: a budget
miss costs one continuation leg, an unverifiable slice is not fixed by any resume. The
#1720 family is the measured precedent for cutting one-consumer pairs apart to stay
under a ceiling and doubling the ticket count for it.

The § A2 overlap sweep found `origin/feature/449` touching `internal/protocol/codes.go`.
Not a blocker and no `blockedBy` was set: issue #449 is CLOSED (2026-05-17), the branch
has no PR, and it is not an ancestor of `main` — an abandoned branch that can never
reach integration, not in-flight work.
