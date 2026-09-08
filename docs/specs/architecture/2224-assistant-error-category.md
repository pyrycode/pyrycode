# Carry the assistant-level API error category on turn_end (#2224)

## Files read

- `internal/streamsup/parser.go` → `maxTurnEndStopField`, `boundStopField` — the cap
  and the drop-not-cut helper this ticket reuses rather than re-mints; the constant's
  doc carries the field count and envelope arithmetic that go stale here.
- `internal/streamsup/parser.go` → `decodeStopShape`, `resultStopLine` — the
  second-decode-target shape (#2223) this ticket copies verbatim for a third key.
- `internal/streamsup/parser.go` → `consumeLine` — the `assistant`, `user` and
  `result` arms: where the raw line is already passed alongside the decoded message,
  and the parser's one reset point.
- `internal/streamsup/parser.go` → `Parser`, `emitBackgroundTaskRoster` — the two
  cross-line-state claims that move with this ticket.
- `internal/streamsup/parser.go` → `emitAssistant`, `emitUser`, `streamLine`,
  `systemTaskStartedLine` — the emit that cannot see the wrapper, the raw-line
  precedent, the segmentation struct that must not widen, and the
  "exactly what the capture shows" discipline this ticket departs from.
- `internal/turnevent/event.go` → `TurnEnd` — the variant the field rides and the
  published-vs-unpublished split its doc states.
- `internal/protocol/interactive.go` → `TurnEndPayload` — the wire struct and this
  file's no-`omitempty` rule.
- `internal/turnbridge/outbound.go` → `MapEvent` — the field-by-field `TurnEnd` arm.
- `cmd/pyry/session_model_hold.go` → `newSessionParser`, and
  `cmd/pyry/streamsup_runner.go` → `newStreamRunnerFactory` — the parser's lifetime:
  one per runner, reused across child respawns. This is what makes the residual real.
- `docs/knowledge/features/streamsup-package-result-stop-shape-second-decode-target-and-dr.md`
  — #2223's recorded lesson: widening a field's *readership* needs the same bound
  review a new decode does.
- `docs/protocol-mobile.md` § `turn_end` — the published shape and its security
  paragraph.

## Context

Each `assistant` line can carry an `error` at the **wrapper** level — a sibling of
`message`, never inside `message.content` — naming why the API call for that
assistant turn failed: `rate_limit`, `overloaded`, `authentication_failed`,
`billing_error`, `account_on_hold` and seven more. Nothing in the tree declares a
`json:"error"` field, so a rate-limited turn is not merely unpublished, it is never
decoded, and reaches a client as a turn that simply ended. The operator re-sends the
prompt instead of fixing the account.

No ADR is owed: this is #2223's decision set applied once more, not a new one.

**Sizing, stated rather than left implicit.** Against the one-ticket boundary this
measures 4 production files, 0 consumer call sites, 0 new types, 5 AC and 1 reject
branch — all inside their lines — and roughly 820 lines of total written work against a
ceiling of 800, mostly because the mandatory security review adds ~70 lines to the spec
and three doc sites have to be argued rather than edited. It is built at that overage
deliberately: every available seam (the decode without its publication, the wire field
without its producer) yields a child whose only consumer is its sibling, which the
floor rule forbids, and the floor outranks the ceiling — a ticket that cannot be
verified on its own is the failure no continuation leg repairs.

## Design

### The decode

A third decode target, sibling to `resultStopLine` and for its reason verbatim
(failure isolation — a hostile shape in one key cannot blank another, and nothing
about either can reach segmentation):

```go
type assistantErrorLine struct {
    Error string `json:"error"`
}
```

read by a pure function `decodeAssistantError(line []byte) string` — no receiver, no
parser state, nothing logged on any path, the bound applied **inside** so no caller
can publish an unbounded value by forgetting to. A decode failure returns `""`, which
is a complete answer to "claude said nothing usable".

`streamLine` stays at Type/Subtype/Message; `TestStreamLine_StaysSegmentationOnly`
enforces it and this ticket does not touch it.

### The cap: reuse, do not mint

`maxTurnEndStopField` (256) and `boundStopField` are reused unchanged. The
constant's own doc argues one constant over the fields because they are "short
open-set tokens off a single line, matched by a consumer against a known list rather
than read as prose" — which describes this category exactly. The bound **drops rather
than cuts**, so an over-long value arrives as `""`.

Measured against the documented set, as `maxModelResolved`'s doc requires: the longest
values are `authentication_failed` and `oauth_org_not_allowed` at 21 bytes, so 256 is
~12x the observation.

### Where the value lives — and the latch

`emitAssistant` takes only the decoded `*streamMessage`, so the wrapper key is not
reachable from it. The write therefore sits in `consumeLine`'s `assistant` arm,
**beside** the `emitAssistant` call rather than inside it — `consumeLine`'s `user`
arm, which passes the raw line for its `tool_use_result` sidecar, is the in-file
precedent. Sitting outside the emit is load-bearing, not stylistic: `emitAssistant`
returns early on a nil message and emits nothing for a message with no mappable
blocks, so a write inside it would miss exactly the error-bearing line that carries no
blocks — the case the ticket names.

New `Parser` field, `assistantErrorCategory string`. It is the parser's **first**
cross-line state that remembers content, which is what makes the two doc sites below
move.

**Every `assistant` line writes it, including an absent key writing `""`.** This is a
latch, not a sticky set, and it is the answer to the residual:

- The `result` arm's clear (below) handles the ordinary turn boundary. It cannot
  handle a child that dies mid-turn *without* a `result` line — `newSessionParser`
  mints one parser per runner and streamsup respawns the child under it, so the
  parser outlives the death. A remembered category has no bound like
  `thinkingSinceEmit`'s: it would attribute one turn's `rate_limit` to a later turn's
  `turn_end`, a **wrong claim** rather than an early event.
- The latch narrows that residual to "the next turn emits no `assistant` line at all
  before its `result`". Any assistant output overwrites it. This costs no second reset
  point — it is the same single write site behaving as last-writer-wins.
- The latch's own failure case is an error-bearing assistant line **followed** by a
  clean one inside the same turn, which drops the category. Unobserved, and no live
  capture can settle it (see Testing strategy). It is chosen as the fail-closed
  direction on `emitUser`'s multi-block precedent: a dropped category is today's
  behaviour exactly — a turn indistinguishable from one that ended — while a stale one
  sends the operator to fix an account that is fine. Failing back to the status quo
  beats failing to a false statement.

### The reset

No second reset point. `consumeLine`'s `result` arm reads-and-clears in one step,
unconditionally and before the emit, beside `p.thinkingSinceEmit = 0`; the local it
reads into is passed **into** the same emit, so no decode or read outcome can reorder,
duplicate or suppress the boundary. Both result subtypes go through that arm, so a
cancelled turn leaks no residual either.

### Publication

- `turnevent.TurnEnd.ErrorCategory string` — carried verbatim, open set, empty means
  absent-or-over-cap (one reading, as `Outcome`'s doc argues).
- `protocol.TurnEndPayload.ErrorCategory string` → wire `error_category`, **no
  `omitempty`** per `internal/protocol/interactive.go`'s stated rule.
  `internal/protocol/testdata/turn_end.json` moves with the payload.
- `turnbridge.MapEvent`'s `TurnEnd` arm maps it straight through, **uncapped**: the
  producer bounded it at construction, and a second bound here is a number to keep in
  step with one that already holds.

Named `error_category` rather than `error`: `Error` on a Go struct reads as an error
value, and an `error` key beside `is_error` on the same frame reads as its detail. The
daemon already renames on this path — `outcome` is claude's `subtype`, and claude's own
`stop_reason` is not forwarded at all.

### Doc corrections (AC 5)

1. `maxTurnEndStopField` — "BOTH claude-authored strings" becomes three, and
   `2 * 256 = 512` bytes / 0.8% becomes `3 * 256 = 768` / ~1.2% of the 65519-byte v2
   application-envelope cap.
2. `Parser` — a dated amendment: the parser now holds a second piece of cross-line
   state, and unlike the counter it **is** a memory of what a line said, so the
   "remembers nothing any line SAID" clause is false and is corrected rather than
   left standing. It states the residual explicitly and what bounds it (the latch),
   rather than inheriting the counter's argument by resemblance.
3. `emitBackgroundTaskRoster` — a third dated re-scope, in the style of its
   AMENDED 2026-08-09 and RE-SCOPED 2026-09-03 entries: the refusal to synthesize a
   task-finish event survives, and by the same explicit rule the #2077 entry already
   needed — the new state remembers no task and no roster, so a disappearance is still
   not derivable. What is no longer available is the *amnesia* half of the argument.
4. `docs/protocol-mobile.md` § `turn_end` — a table row, the optional/open-set/absent-
   decodes-to-`""` rule, and the field's inclusion in the existing bounded-not-
   sanitized security paragraph. Plus the § Application message types row and a
   dated changelog entry.

## Concurrency model

No new goroutines and no lock. The new field is read and written only from `Write` →
`consumeLine`, which `Parser`'s single-writer invariant already covers: os/exec drives
Config.Stdout from exactly one goroutine and nothing else reads parser state. The
field is the second one that invariant does real work for, and the plan adds it to the
list its guard would have to cover if a concurrent reader ever appears.

## Error handling

Every failure returns a value, never an error. A line that will not decode into
`assistantErrorLine` yields `""`; a non-string `error` value fails the whole decode and
takes the same path, which is the conservative direction. Nothing is logged on any
path, the decode error included and specifically: `encoding/json` quotes the offending
input into its error text, so logging it would route claude's own token into the daemon
log through a channel no per-attribute check can see (AC 3).

## Testing strategy

New `internal/streamsup/assistant_error_test.go`, plus small additions to the protocol
and turnbridge tests. Scenarios:

- `error:"rate_limit"` on an assistant line → that turn's `TurnEnd.ErrorCategory`;
  a turn with no such line → `""` (AC 1).
- An error-bearing line carrying **no content blocks** emits no other event and still
  categorises its `turn_end` — the case a write inside `emitAssistant` would miss.
- Categorised turn then clean turn → second `turn_end` empty (AC 2); the same across a
  `error_during_execution` result, and with no intervening assistant line at all.
- A value claude's set does not name rides through as sent; a 257-byte value arrives
  as `""`, a 256-byte one is carried (AC 3).
- The latch: an assistant line with no `error` key after one with it clears the field.
- `stop_reason`, `outcome`, `is_error`, `terminal_reason` unchanged across the matrix
  (AC 4), and no claude-authored byte in the log — the package's existing
  content-free-log assertion shape.
- Wire: the `turn_end.json` fixture round-trip and `MapEvent`'s pass-through.

**The fixture is a synthesized `assistant` line inside the unit tests**, and this is a
deliberate, named departure from the "declare exactly the fields the committed capture
shows and nothing invented" discipline `systemTaskStartedLine` and `resultLine` both
state. The justification is the published SDK type definition
(`@anthropic-ai/claude-agent-sdk@0.3.263`, `sdk.d.ts`) plus the Claude Code headless
docs, and the reason the discipline cannot apply is that **no capture is obtainable**:
rate limits, overload, auth failure and billing errors are not provokable on demand, so
a live test for them would skip — and a skipped live test reports success while proving
nothing. No `internal/streamsup/testdata/` directory is created, which would divide
these fixtures from the committed captures under `internal/e2e/realclaude/testdata/`.
For the same reason this ticket carries no `needs-real-claude` label: there is no live
behaviour for a gate to observe.

Gate: `go test -race ./internal/streamsup/... ./internal/turnevent/... ./internal/protocol/... ./internal/turnbridge/...`,
`go vet ./...`, `go build ./cmd/pyry`. The fake-daemon e2e suite (AC 4) is the
verifier's full-module gate.

## Open questions

1. Whether an assistant line may carry `error` and be followed by another assistant
   line in the same turn. Unobservable here; settled by the latch's fail-closed
   direction above rather than left open, and the choice is recorded at the field so a
   future capture can overturn it with evidence.
2. Whether the residual should instead be cleared on a child-restart signal. Closed by
   the ticket: no second reset point. Recorded in the `Parser` doc as an accepted,
   named limitation rather than an oversight.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] SHOULD FIX.** The boundary itself is explicit and single —
  `decodeAssistantError`, with `boundStopField` applied inside it — but this is the
  first field on this frame that reports an **account/API state** rather than a turn
  state. `outcome` and `terminal_reason` describe how *the turn* stopped;
  `account_on_hold`, `billing_error` and `authentication_failed` are claude-authored
  strings a client could render as an authoritative statement about the operator's
  account, and a model that can emit an assistant line can fabricate one. Phase B must
  mark provenance explicitly in `docs/protocol-mobile.md`'s row and in
  `turnevent.TurnEnd.ErrorCategory`'s doc — **claude's report of why its API call
  failed, never the daemon's verification of account state** — on `attachment_offered`'s
  `filename` precedent, where publishing a claude-authored string as daemon-asserted is
  named as handing a client trusted chrome. The plan's design constraint that keeps
  this recoverable: **nothing in the daemon acts on the value.** No retry, no backoff,
  no teardown is keyed on it; it is carried, not consumed.
- **[Tokens, secrets, credentials] No findings.** Nothing is generated, stored,
  compared or transported. The values *name* credential-shaped failures but carry no
  credential material, and `assistantErrorLine` declares exactly one key, so nothing
  else off the line can be captured by this decode.
- **[File operations] Not applicable, by a design property rather than absence of
  intent.** `Parser`'s doc states its only input is the `Write` bytes — no transcript
  file is opened, watched or resolved — and this plan adds no path, no open, no write.
- **[Subprocess] No findings on argv, env or signals** — nothing changes about how
  claude is spawned. One cost named rather than waved past: `decodeAssistantError` adds
  a **second full unmarshal of every `assistant` line**, a more frequent line type than
  `result`. Accepted on `emitUser`'s precedent, which pays the identical second
  unmarshal on every `user` line — a larger line class, since it carries
  `tool_use_result`. Bounded per line by `defaultMaxParseBuf`, and with no super-linear
  step: unlike `decodeModelWindows` the target holds one scalar and no collection.
- **[Cryptographic primitives] Not applicable, and there is no comparison surface at
  all** — the open-set rule carries the value verbatim with no `switch` and no equality
  test against any daemon constant, so not even a timing side channel exists to audit.
- **[Network & I/O] No findings.** The line is capped upstream by `defaultMaxParseBuf`;
  the published value is capped **inside** the decode, so no caller can publish an
  unbounded one. Envelope arithmetic recomputed: worst case 3 × 256 = 768 bytes, ~1.2%
  of the 65519-byte v2 application-envelope cap. The stale `2 * 256 = 512` / 0.8% at
  `maxTurnEndStopField` is corrected under AC 5 rather than left reading as true.
- **[Error messages, logs, telemetry] No findings — verified, not asserted.**
  `decodeAssistantError` logs nothing on any path and discards the decode error rather
  than wrapping it, `decodeStopShape`'s rule verbatim, because `encoding/json` quotes
  offending input into its error text. The question this field raises that #2223's did
  not is **retention**: the value now sits in a struct field between two lines, so a
  state dump would leak it. Checked — nothing formats a `*Parser` (no `%+v`/`%#v` on one
  anywhere in the package) and `internal/debugbundle` reaches no streamsup parser
  state; a SIGQUIT dump prints stacks, not struct fields.
- **[Concurrency] No findings**, re-checked for this field specifically because it now
  spans a **child respawn**: `runner.go` re-binds `cmd.Stdout = r.cfg.Stdout` on every
  spawn and the same parser is reused, so a second os/exec forwarder goroutine writes
  into it after the first. The happens-before edge is `cmd.Wait()`, which returns only
  after the copying goroutine has finished and precedes the next spawn in the same
  loop. `thinkingSinceEmit` has crossed that edge since #1385, so this adds no new
  property — only a second field the single-writer invariant covers. No lock is added
  and none is owed; a future concurrent reader guards both.
- **[Threat model alignment] No findings beyond the first.** `docs/protocol-mobile.md`
  § Security model threat 1 lands in the **outward** direction — model-influenced text
  flowing toward a render surface — the reading `attachment_offered`'s `filename` and
  #2223's two strings already carry. The daemon bounds and does not sanitize; the
  render boundary owing control-character and terminal-escape stripping is the
  **client's**, stated in the row rather than left to be inferred.
- **[Out of scope]** Any daemon behaviour keyed on the category — a backoff on
  `rate_limit` or `overloaded`, a teardown on `account_on_hold`. That would turn a
  claude-authored string into an actuator and needs its own review. #1237, the named
  consumer, draws it on the desktop timeline and does not act on it either.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-08
</content>
</invoke>
