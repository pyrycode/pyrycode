# #2254 — map the session facts onto the v2 wire and correct the unemitted claims

One `MapEvent` arm joins two halves that already landed, and five live claims that
the frame is unemitted stop being true the moment it does.

## Files read

- `internal/turnbridge/outbound.go` → `MapEvent` — the type switch the new arm joins; its
  `turnevent.ModelAnnounced` arm is the pattern (no re-cap, no charset check, no suppression
  branch) and its `turnevent.RateLimited` arm is the pattern for a nil `TruncatedFields`
  crossing as nil.
- `internal/turnevent/event.go` → `SessionFacts` — the variant's three fields, their verbatim
  rule, the producer's either-field-present gate, and the measured absence of an effort field.
- `internal/protocol/interactive.go` → `SessionFactsPayload` — the four wire keys, and the
  closing paragraph naming #2254 as the producer that has not landed.
- `internal/protocol/codes.go` → `TypeSessionFacts` — the constant, and its closing paragraph
  making the same claim. `TypeModelList`'s closing paragraph is the corrected form to copy.
- `internal/protocol/interactive.go` → `ModelListPayload` — the *producer has since landed*
  paragraph both corrections take.
- `cmd/pyry/interactive_turn_v2.go` → `Handle`, `eventKind` — the `turnevent.SessionFacts`
  arms that #2252 landed. Both carry a paragraph naming this ticket as the mapper.
- `cmd/pyry/interactive_turn_v2_test.go` → `TestInteractiveTurnEmitterV2_SessionFactsNoLifecycleMutation`
  — written to assert *no frames at all* so it reddens here.
  `TestInteractiveTurnEmitterV2_ModelListFansOutToEveryInteractiveConn` is the frame-assertion
  shape to move it toward.
- `internal/turnbridge/outbound_test.go` → `TestMapEventOutbound`, `TestMapEventRateLimitedTruncatedFieldsOnTheWire`
  — the struct-level table and the bytes-level nil-polarity test, the two shapes this arm needs.
- `cmd/pyry/relay_guard_test.go` → `excludedTypes` — the `TypeSessionFacts` entry whose
  parenthetical calls the emission pending.
- `docs/protocol-mobile.md` § `session_facts`, § `model_announced`, § Changelog — the row and
  paragraph forms to take, and the #1860 entry's shape for the correction record.
- `docs/knowledge/features/turnbridge-package.md` — the package is a pure value-to-value
  adapter: no I/O, no state, no envelope minting, no clock read. The arm inherits that.

## Context

`internal/streamsup` has produced `turnevent.SessionFacts` since #2252 and `internal/protocol`
has carried the frozen `session_facts` wire type since #2253, but `MapEvent` has no arm for the
variant, so `emitMapped` takes its unmapped branch and logs one content-free Debug per turn. No
client can see claude's build or the posture claude reports. This ticket adds the one arm and
corrects the five places in live prose and Go doc that state the frame is unemitted.

No ADR is warranted. The design decision this arm embodies — verbatim carry, no second cap, no
second gate — was settled by the `model_announced` family and is stated in the arms it copies.

## Design

`MapEvent` gains one case, placed immediately after the `turnevent.ModelAnnounced` arm so the
switch's order follows the init line's two halves:

```go
case turnevent.SessionFacts:
    return protocol.TypeSessionFacts, protocol.SessionFactsPayload{
        ConversationID:    tc.ConversationID,
        ClaudeCodeVersion: e.ClaudeCodeVersion,
        PermissionMode:    e.PermissionMode,
        TruncatedFields:   e.TruncatedFields,
    }, true
```

Four properties, each inherited from a sibling arm rather than decided here:

- **Conversation identity only.** `tc.TurnID` and `tc.Seq` are ignored; the payload has no field
  either could land in. A build and a posture are properties of the child run, not of a turn.
- **No re-cap.** `internal/streamsup`'s `maxClaudeVersionField` and `maxPermissionModeField`
  bound both strings at construction. A second cap here would be a second place the limit is
  decided. This file's `maxSummaryLen` is the tool-précis cap and is not applicable.
- **No charset check.** `internal/relay`'s `validModel` bounds a phone-supplied override and is
  deliberately a different rule.
- **A nil `TruncatedFields` crosses as nil**, which is what puts `"truncated_fields":null` on
  the wire. `SessionFactsPayload` owns no `MarshalJSON`, so nothing normalises it afterwards.
  Allocating an empty slice would emit `[]` and tell a phone that claude's cut text is complete.
  This is the `RateLimited` arm's polarity, not the `BackgroundTaskRoster` arm's.

**No suppression branch, not even on two empty strings.** The producer's either-field-present
gate is the only thing that decides whether the event exists; a second, differently-shaped
filter here would silently diverge from it.

**No capability gate in the arm.** `emitMapped` hands the mapped payload to `emit`, where the
single interactive-capability gate lives. Every lane that already reaches the variant lights up
at once.

The `cmd/pyry` side needs no production change beyond comment repair: #2252 landed the `Handle`
case and the `eventKind` arm, and `turnMarkFor` already answers `turnMarkNone`.

### The five unemitted claims

Each is a paragraph asserting the frame has no producer. All five become false with the arm.

| Where | Correction |
|---|---|
| `docs/protocol-mobile.md` application-message-type row | *Shape declared by #2253* → the *declared by X, emitted since Y* form the `rate_limited` and `model_announced` rows use |
| `docs/protocol-mobile.md` § `session_facts` paragraph | *Declared by #2253; nothing emits it yet* → the **Emitted since #2254** form § `model_announced` uses |
| `cmd/pyry/interactive_turn_v2.go` `Handle` arm | the *NOTHING IS MAPPED YET* paragraph — the arm now reaches a mapped payload |
| `cmd/pyry/interactive_turn_v2.go` `eventKind` arm | the *THIS ARM IS LIVE FOR THIS FILE'S OWN DEBUG* paragraph — emitMapped's unmapped drop is no longer its site |
| `internal/protocol/codes.go` `TypeSessionFacts` closing paragraph | `TypeModelList`'s *declaring ticket was wire vocabulary only; #N added…* form |
| `internal/protocol/interactive.go` `SessionFactsPayload` closing paragraph | `ModelListPayload`'s *The producer has since landed* form |
| `cmd/pyry/relay_guard_test.go` `excludedTypes` entry | the parenthetical calling the mapping and emission both pending |

The sibling frames' own unemitted claims in `docs/protocol-mobile.md` — `attachment_stored`'s
among them — belong to their own families and stay. So do the historical changelog entries,
including #2253's: rewriting one destroys the record of what was true when it was written, which
is what the #1860 entry spent a ticket establishing.

The Handle arm's paragraph is **repaired in place** rather than left standing beside a
correction. It is a statement about what the tree does right now, not a measurement whose
provenance matters; the CORRECTED-in-place form the same file uses for its variant counts exists
because a count's history is evidence, and a *nothing is mapped yet* sentence has none.

### Changelog entry

One new dated entry at the head of `docs/protocol-mobile.md` § Changelog, in the shape #1860's
entry uses for the same job on `model_list`: it names the false statements, names the slices that
made them false, says which claims elsewhere in the file were deliberately left standing, and
records what did not change. No live count moves — this frame is not a new `turnevent` variant
and adds no `####` heading.

## Concurrency model

None. `MapEvent` is a pure function over values with no I/O, no state and no goroutine. The
slice header carried in `TruncatedFields` is shared with the event, which is the standing rule
for every arm here: the producer allocates per line, retains nothing, and nothing downstream
mutates a payload.

## Error handling

The arm has no failure mode. `MapEvent` returns `ok=false` only for variants it drops; this one
always maps, so a zero-value event maps rather than dropping.

## Testing strategy

RED first: the frame assertion in the rewritten `cmd/pyry` test fails against a tree with no arm.

- **`internal/turnbridge/outbound_test.go`** — four rows added to `TestMapEventOutbound`, mirroring
  the `ModelAnnounced` rows: every field verbatim; over-cap strings crossing uncut (longer than
  both `maxClaudeVersionField` and `maxSummaryLen`, so a re-cap at either reddens); turn addressing
  ignored, with a conspicuous `TurnID` and non-zero `Seq`; and a zero value mapping rather than
  dropping. Sentinels are mixed-case and mutually distinct so a field swap reddens without a
  second assertion.
- **`internal/turnbridge/outbound_test.go`** — a new bytes-level test in
  `TestMapEventRateLimitedTruncatedFieldsOnTheWire`'s shape, asserting on `json.Marshal` of the
  value `MapEvent` returned: a nil report reaches the wire as `null` and forbids `[]`; a populated
  one crosses verbatim and in member order. Needles are full `"key":"value"` pairs, since three of
  the four fields are strings.
- **`cmd/pyry/interactive_turn_v2_test.go`** — `TestInteractiveTurnEmitterV2_SessionFactsNoLifecycleMutation`
  asserts the pushed frame's type and its decoded wire values, and keeps all four lifecycle pins:
  `inTurn` false, empty `turnID`, empty `currentState`, and a fresh turn opening afterwards.
- Touched-scope gate: `go test -race ./internal/turnbridge/... ./internal/protocol/... ./cmd/pyry/...`,
  `go vet ./...`, `go build ./cmd/pyry`.

## Open questions

1. Does the corrected § `session_facts` paragraph publish a delivery window, as #1860's correction
   did for `model_list`? Resolve during implementation by reading what the lane actually
   guarantees; the ACs do not ask for one, so the default is no.
2. Does any drift detector redden on the frame becoming emitted? `relay_guard_test.go`'s
   classification stays `push`, so the expectation is no. Confirm by running the gate.

## Security review

**Verdict:** PASS

This arm IS the trust-boundary widening: two claude-authored strings that stopped at the event
bus now reach a remote client's render surface. That is the ticket, not a side effect, so the
categories below are walked against what the widening exposes rather than against the four lines
of struct literal that do it.

**Findings:**

- **[Trust boundaries] SHOULD FIX.** The boundary is explicit and single — `internal/streamsup`'s
  `emitSessionFacts` bounds both values with `truncateField` and nothing anywhere sanitizes them;
  no control-character or terminal-escape stripping happens on this path, and the render boundary
  owing it is the client's. Downstream holds untrusted text with no type-system signal, so the
  signal is the documentation, and this ticket rewrites exactly that documentation. The obligation:
  AC3's sweep flips the *emission* claim only. `SessionFactsPayload`'s SECURITY paragraph — bounded
  but not sanitized, `permission_mode` a claim rather than a guarantee, never an authorization
  decision — and § `session_facts`'s matching prose must survive the edit intact. A correction that
  tidied them away would publish a newly-live frame with its trust tier deleted.
- **[Tokens, secrets, credentials] No findings.** No token is minted, stored, rotated, compared or
  logged; the arm is a value copy. The adjacent rule that does bind: `eventKind` returns the variant
  name only, per the #833 posture that `permission_mode` is precisely the field a log line would
  reach for. The repair there is comment-only, and `TestInteractiveTurnEmitterV2_SessionFactsEventKindNamesTheVariant`
  already reddens on a return that carried either value.
- **[File operations] No findings, structurally rather than by omission.** The captured init line
  carries `cwd`, `memory_paths`, `messaging_socket_path` and claude's `session_id`; none is declared
  on the producer's decode target `systemInitLine`, so this arm cannot carry an operator filesystem
  path or claude's session identity even by accident. A field never decoded cannot leak.
- **[Subprocess / external command] No findings.** No `exec.Command`, and the direction is outbound
  only. The named hazard is that the mapped `permission_mode` must never become an input to
  `internal/streamsup`'s `permissionModeAllowed`, which bounds what the DAEMON may ask for and is a
  deliberately different rule; the arm copies into a payload and reaches no spawn or control path.
- **[Cryptographic primitives] No findings.** No randomness and no comparison against a secret. The
  envelope's `event_id` is minted by the consumer's `emit`, not here — `internal/turnbridge` mints
  no ids by design.
- **[Network & I/O] No findings, with the size question answered rather than waived.** Both strings
  are capped at 256 at construction and `TruncatedFields` holds at most two fixed daemon-authored
  names, so the frame's serialised size is bounded by construction. That is why this arm needs no
  `FitV2EnvelopeCap` pass, unlike `SlashCommandListPayload`, whose length is claude's to choose;
  `maxPermissionModeField`'s doc carries the envelope arithmetic. Cadence is one event per init line
  and one init line per turn — claude's own, not network-reachable — so the non-droppable queue slot
  the frame holds is bounded by the turn rate, `model_announced`'s standing position.
- **[Error messages, logs, telemetry] SHOULD FIX.** `MapEvent` logs nothing, and the change strictly
  reduces log volume: `emitMapped`'s unmapped-drop Debug stops firing for this variant. The Phase B
  trap is in the test rather than the code — the rewritten frame assertion must read the decoded
  push, never a log buffer. An assertion that found the values in log text would be passing because
  they had leaked into a log, which is the thing the sibling eventKind test forbids.
- **[Concurrency] No findings, verified rather than assumed.** `MapEvent` is a pure function with no
  I/O, state or goroutine. `TruncatedFields` crosses as a shared slice header rather than a deep
  copy, and all three conditions the `RateLimited` arm names are met here: `emitSessionFacts`
  declares `cut` as a fresh local per call, the parser retains no reference to it after `emit`, and
  `SessionFactsPayload` owns no `MarshalJSON` that could mutate what it is handed.
- **[Threat model alignment] No findings.** `docs/protocol-mobile.md` § Security model's threat 1
  (prompt injection, `severity: high`, `mitigation: partial`) lands on this frame in the outward
  direction — claude's own words become text a phone draws — and is already published on it. This
  ticket does not change that standing; it makes it reachable, which is what the first finding's
  obligation protects.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-10

## Revisions

**2026-09-10 — both open questions resolved; the correction sweep grew by two paragraphs.**

- **Open question 1 (delivery window): no, and the changelog says so explicitly.** #1860 published one
  for `model_list` because that frame's once-per-`initialize` cadence made its three loss points worth
  enumerating. This frame rides the per-turn `system/init` line on the same lane as `model_announced`,
  which publishes none either, and a client that misses one gets the next turn's. Recording the
  decision rather than leaving the absence to be read as an oversight.
- **Open question 2 (drift detectors): none redden.** `relay_guard_test.go`'s classification stays
  `push` — a push that started pushing is still a push — and the whole `internal/protocol` package
  passes under `-race` unchanged. The entry's comment was repaired for its stale parenthetical only.
- **Two claims outside AC3's enumeration were repaired**, both inside blocks AC3 does name and both
  false for the same reason: `TypeSessionFacts`' opening paragraph said *no client can see either fact
  today*, and `SessionFactsPayload`'s said *the bridge WILL supply ConversationID*. AC3's governing
  sentence is that no live prose or Go doc still says the frame is unemitted, so leaving either would
  have satisfied the list while failing the rule.
- **One live claim is knowingly left standing**, and it is not this ticket's to fix:
  `docs/knowledge/features/protocol-package-constants-codes-go-envelope-types.md` says the frame was
  *declared unwired in #2253* with the mapping still pending. That path belongs to the documentation
  phase, which this ticket is forbidden to write to; the PR body carries it forward instead. The
  #2253 changelog entry is likewise left standing, per AC4.
- **The security review's two SHOULD FIX items both landed.** The trust-tier statements survived the
  sweep intact — `permission_mode` is still published as a claim rather than a guarantee and both
  strings as bounded-but-not-sanitized, in the payload doc and in § `session_facts` — and the new
  changelog entry restates them rather than trimming them beside the stale sentence. The rewritten
  `cmd/pyry` test reads its values from the decoded push, never from a log buffer.
