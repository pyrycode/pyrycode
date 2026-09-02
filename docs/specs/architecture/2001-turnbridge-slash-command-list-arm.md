# #2001 — map `turnevent.SlashCommandList` onto the `slash_command_list` wire shape

## Files read

- `internal/turnbridge/outbound.go` → `MapEvent`, its `turnevent.ModelList` arm — the shape this slice mirrors,
  down to which nil is forwarded and which is load-bearing. Its `default` is what drops the variant today.
- `internal/turnbridge/outbound_test.go` → `TestMapEventOutbound` (the struct-level table, including its
  five `ModelList` rows), `TestMapEventModelListOnTheWire` — the two altitudes #1848 asserted at, and the
  pattern for the `notWant` discipline (full `"key":value` needles, never a bare value).
- `internal/protocol/interactive.go` → `SlashCommandListPayload`, its `MarshalJSON`, `SlashCommand`, its
  `MarshalJSON` — the target shape, and the two-marshaller asymmetry AC 2 turns on: the payload normalises a
  nil `Commands` to `[]`, the entry normalises a nil `Aliases` to `[]`, and both deliberately exempt
  `TruncatedFields`.
- `internal/turnevent/event.go` → `SlashCommandList`, `SlashCommand` — the source shape. `SlashCommand`'s five
  fields are declared in `protocol.SlashCommand`'s own order (#1825 closed the set), so the arm is a
  field-for-field copy rather than a reordering.
- `internal/streamsup/parser.go` → `emitSlashCommandList`, `maxSlashCommandName`, `maxSlashCommandDescription`,
  `maxSlashCommandArgumentHint`, `maxSlashCommandAlias`, `maxSlashCommandAliasCount`,
  `maxSlashCommandListEntries` — the producer's caps, which are the numbers the Security review's size
  arithmetic stands on, and the sink enumeration for `Name` that this slice extends by exactly one sink.
- `cmd/pyry/interactive_turn_v2.go` → `eventKind`'s `turnevent.SlashCommandList` arm — its claim that the
  Handle case *and* the wire mapping are both open, half of which this commit falsifies.
- `internal/protocol/interactive_test.go` → `TestSlashCommandListPayload_RoundTrip`'s `dropped_commands`
  paragraph, and the fixture-inventory comment above it — two more copies of the "nothing maps that count"
  claim AC 5 covers.
- `internal/protocol/codes.go` → `TypeSlashCommandList` — the discriminant, and its declare-then-emit block,
  which is about *emitting a frame* and therefore stays standing.
- Ticket #2002 / #2003 / #2005 blocker graph (`blockedBy`) — `#2003 → #2002 → #2001` and `#2005 → #2002`, the
  fact that turns the deferred frame-size bound from a MUST FIX into an enforced deferral.

## Context

`internal/streamsup` decodes claude's `initialize` reply into a `turnevent.SlashCommandList` and
`internal/protocol` declares the wire shape it belongs in, and nothing joins them: `MapEvent`'s switch carries
18 `case turnevent.…` arms with no `SlashCommandList` among them, so the decoded list falls to the default and
is dropped.

This slice adds that one arm. It is the single translation point two later slices consume without forking —
#2003's live-lane emission and #2005's connect-time resolver — which is the sibling's proven shape:
`cmd/pyry`'s `resolveBoundModelList` calls `MapEvent` directly and says so.

No ADR is warranted. The design decision — a verbatim 1:1 mapping with `ConversationID` as the only supplied
value — is #1848's precedent applied unchanged, and it is already recorded on `MapEvent`'s `ModelList` arm.

## Design

One new `case turnevent.SlashCommandList:` in `MapEvent`, placed immediately after the `turnevent.ModelList`
arm and before `default`, so the two `initialize`-reply inventories sit adjacent.

Contract: `MapEvent(turnevent.SlashCommandList, tc) → (protocol.TypeSlashCommandList,
protocol.SlashCommandListPayload, true)`.

The body is a nil-declared local appended into by a read-only range over `e.Commands`, one
`protocol.SlashCommand` per `turnevent.SlashCommand`, all five fields assigned across; then a
`protocol.SlashCommandListPayload` literal carrying `tc.ConversationID`, that local, and `e.DroppedCommands`.

That idiom is what makes AC 2 and AC 4 one thing rather than a tension:

- **Empty list → nil `Commands` (AC 2).** `var commands []protocol.SlashCommand` never appended into stays
  nil, and `SlashCommandListPayload.MarshalJSON` owns nil→`[]` on the wire. Allocating here would produce the
  same bytes while hiding which layer owns the normalisation.
- **Fresh outer slice (AC 4).** `append` into a nil local allocates a backing array this arm owns. Assigning
  the event's slice straight across — the reading that would satisfy AC 2 and break AC 4 — does not even
  typecheck here, the element types differing; the fresh allocation is therefore load-bearing for the
  *inner* rule that follows rather than defended by the type system alone.
- **Read, never write (AC 4).** `for _, c := range e.Commands` reads. The inner slices (`Aliases`,
  `TruncatedFields`) cross as the slice headers they are and go on sharing backing arrays with the event by
  design: carry, never mutate through. A sort, an in-place dedupe or a filter would corrupt the event, and
  #2005 will read the payload on a relay-leg goroutine.
- **`TruncatedFields` stays nil (AC 2).** `SlashCommand.MarshalJSON` deliberately exempts it, so nothing
  normalises it afterwards. Nothing-was-cut is an *absence* and must reach the wire as null; an allocated
  empty slice would tell a client that claude's cut text is complete. `Aliases` one field up has the opposite
  polarity — that marshaller collapses its nil to `[]` — and the asymmetry between two list fields of one
  struct is the point.
- **`DroppedCommands` is carried (AC 2).** The count the decode recorded when `maxSlashCommandListEntries`
  fired. Never recomputed from `len(Commands)` — that yields the wrong number by construction — and never
  zeroed, which would tell a client that a capped menu is the whole menu.
- **Nothing is re-capped, re-ordered, canonicalised or charset-checked (AC 3).** The producer bounded every
  dimension at construction, so a second cap here would be a second place the limit is decided. `Name` is not
  an identifier (`__remote-workflow` is in the committed capture), so no charset assumption belongs here, and
  `maxSummaryLen` / `maxResultSummaryRunes` live in this same file and are *not* applicable bounds — reaching
  for either is the specific mistake to avoid. Entry order is claude's, and so is alias order.
- **No suppression branch, not even on an empty `Commands`.** The gate deciding whether the event exists at
  all is the producer's (`emitSlashCommandList` suppresses the empty list), so a second, differently-shaped
  filter here would silently diverge from it. A zero-value `SlashCommandList` therefore maps.
- **No size bound.** Deliberate, and #2002's — see Security review § 6.

### AC 5 — the falsified-claim sweep

AC 5 says the "MapEvent has no arm for this variant" claim is corrected *wherever it appears*. The ticket's
Technical Notes names two files; the sweep found the same clause in three more. The Notes scope the
correction by **clause**, not by file ("that clause and only that clause goes false"), so the wider file set
is the AC applied rather than the AC widened. The sibling family paid for an undercount here twice (#1861,
#1862); a fifth file is cheaper than a third correction ticket.

Corrected — the MapEvent/mapping half only:

| Site | Falsified clause |
|---|---|
| `protocol.SlashCommandListPayload` doc | "Nothing in the tree constructs this type yet" |
| `protocol.SlashCommandListPayload` doc | "NOTHING WRITES IT — turnbridge.MapEvent has no arm for the variant" |
| `turnevent.SlashCommandList` doc | "the PUBLISH — `MapEvent`'s arm and … Handle case — is #1720's and is still open"; "ONE of the four remains" |
| `turnevent.SlashCommandList` doc | "IT IS PUBLISHED BY NO PATH TODAY. turnbridge.MapEvent has no arm for it" |
| `turnevent.SlashCommandList` doc | "what remains is the mapping between the two fields, which is #1720's" |
| `TestSlashCommandListPayload_RoundTrip`'s fixture inventory | "nothing MAPS that onto this field … #1720 is where the field and that counter meet" |
| `TestSlashCommandListPayload_RoundTrip`'s `dropped_commands` paragraph | "nothing maps that count onto this payload" |
| `emitSlashCommandList` doc | "Handle's case and MapEvent's arm are both #1720's and still open" |
| `maxSlashCommandList`-family record (`parser.go`) | "`DroppedCommands` being unmapped until #1720" |
| `eventKind`'s `SlashCommandList` arm | "whose Handle case and wire mapping are #1720's" |

Left standing, deliberately:

- Every "…and `interactiveTurnEmitterV2.Handle` no case, so no frame of this type is produced at all"
  conclusion. It stays **true** until #2003.
- `emitSlashCommandList`'s "the value never reaches `MapEvent` AT ALL", and `maxSlashCommandName`'s sink
  enumeration saying the same. Both rest on `emitMapped` being reached only from Handle's *typed* arms —
  still true, an arm on `MapEvent` not being a route to it.
- `emitSlashCommandList`'s "nothing publishes this list today". True: an arm is not a publication.
- The frame-level-bound sentences in `turnevent.SlashCommandList.DroppedCommands` and in `parser.go`. #2002
  falsifies those, not this slice — the ticket says so explicitly.
- `TypeSlashCommandList`'s declare-then-emit block and `relay_guard_test.go`'s `excludedTypes` note. Both are
  about *emitting a frame* and about an inbound verb; neither moves here.
- `docs/protocol-mobile.md`. Out of scope entirely; #2010 owns it.

## Concurrency model

No goroutines, no channels, no locks. `MapEvent` is pure and stays pure. The concurrency-relevant property is
the one AC 4 states: the arm must not write the event, because #2005 will read the mapped payload on a
relay-leg goroutine while the retained list is held elsewhere. `sessionModelHold`'s slash-command equivalent
is #2004's and is not in the tree; the rule is enforced by construction here rather than by a lock.

## Error handling

The arm has no failure mode. It performs no decode, no I/O and no allocation that can fail; every field is a
`string`, a `[]string` or an `int` already in memory. `MapEvent`'s `(typ, payload, ok)` contract returns
`ok == true` unconditionally on this arm — the only `false` cases stay `ThoughtChunk` and nil/unknown. The
absence of a reject branch is the design: an event that reached here was already validated and bounded by the
producer, and a second gate would be a second place the decision is made.

## Testing strategy

Three altitudes, mirroring #1848 plus one this slice's AC 4 adds.

**1. `TestMapEventOutbound` — struct level, `reflect.DeepEqual`.** Five rows:

- every field verbatim: two *distinct* entries, aliases on one and not the other, `TruncatedFields` populated
  differently per row, `DroppedCommands` neither `0` nor `len(Commands)` so a constant and a recomputation
  are both caught. `ev` and `wantPayload` carry **separate slice literals** — sharing one backing array would
  let an in-place mutation mutate the expectation alongside the input and stay green.
- nothing-cut row: nil `Aliases` and nil `TruncatedFields` survive as nil (`reflect.DeepEqual` separates nil
  from `[]string{}` where `slices.Equal` would not).
- over-cap row: every bounded text dimension longer than the producer's own cap *and* than this file's
  `maxSummaryLen` / `maxResultSummaryRunes`, so a re-cap mutant at any of them reddens.
- turn-addressing row: a conspicuous `TurnID` and non-zero `Seq` with no field to land in.
- zero value maps rather than dropping, `Commands` staying nil at the struct level.

**2. `TestMapEventSlashCommandListOnTheWire` — byte level**, asserting on `json.Marshal` of the value
`MapEvent` *returned*, never a hand-built payload: with two `MarshalJSON` methods in play a hand-built payload
would prove the marshallers work and say nothing about whether the mapping reached them with the nils intact.
Rows, `want`/`notWant` on full `"key":value` needles:

- nil `Commands` → `"commands":[]`, forbids `"commands":null`.
- populated control, so the row above passes for the right reason (forbids `"commands":[]`).
- nothing cut → `"truncated_fields":null`, forbids `"truncated_fields":[]`.
- nil aliases → `"aliases":[]`, forbids `"aliases":null` — the *opposite* polarity in an adjacent row.
- aliases cross in claude's own order, against the sorted permutation as `notWant`.
- one isolating `truncated_fields` row per name that a sibling row does not already pin, and one row carrying
  all four in producer order as a single needle, so member order is pinned and not merely membership. Each
  isolating row carries exactly one name: an over-determined row would leave a whitelist mutant green.
- `dropped_commands` is the decode's count, with `0` and the row count both as `notWant`.
- no turn addressing on the wire; sentinels chosen to contain no digit so a bare `Seq` can be forbidden
  without a false positive.

**3. `TestMapEventSlashCommandListDoesNotMutateTheEvent` — AC 4.** Builds an event, snapshots it as an
independently-allocated equal value, calls `MapEvent`, then (a) writes a whole element through
`payload.Commands[0]` and asserts the event's row is unchanged — the outer array is this arm's, not the
event's — and (b) `reflect.DeepEqual`s the event against the snapshot, so an arm that wrote through
`e.Commands[i]` reddens.

**Verification:** `go test -race ./internal/turnbridge/... ./internal/protocol/... ./internal/turnevent/...
./internal/streamsup/... ./cmd/pyry/...` (the packages this touches), `go vet ./...`, `go build ./cmd/pyry`.
RED is proven first: the wire test cannot compile-fail, so RED here is `MapEvent` returning `ok == false` and
the `t.Fatal` firing.

## Open questions

1. **Does the AC 5 sweep extend past the two files the Technical Notes names?** Resolved during planning:
   yes, three more files carry the same clause. Recorded in the Design table above with the standing clauses
   enumerated beside it, so the verifier can check the sweep rather than re-derive it.
2. **Does adding the arm redden an existing totality gate?** Resolved during planning: no. `turnbridge` has no
   totality test over `MapEvent`'s variants, and `MapEvent`'s own doc enumerates only `ThoughtChunk` and
   nil/unknown as the `ok == false` set, which this slice does not change.
3. **Is the deferred frame-size bound reachable before #2002 lands?** Resolved during planning: no — see
   Security review § 6.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No new boundary, and one clarification worth stating rather than assuming. Every
  string on this path is *workspace*-authored — a command defined in a repository was written by whoever
  wrote that repository, a lower-trust origin than claude's own strings — and it crossed the subprocess
  boundary at `internal/streamsup`'s `commandEntryLine` decode. This arm neither re-validates nor sanitizes,
  by design: `turnevent.SlashCommandList`'s SECURITY paragraph and `protocol.SlashCommand`'s both assign the
  render boundary to the *client*, and a second cap here would be a second place the limit is decided. The
  arm therefore carries untrusted-but-bounded text across a package boundary and changes nothing about its
  trust class. Downstream knows this because the wire type's own doc says so.
- **[Subprocess / external command execution]** No findings, stated as an enumeration rather than a
  judgement about the bytes — which is the form `maxSlashCommandName`'s doc already established for `Name`,
  and it is the right form precisely because `[`, `*` and `?` are syntax to `filepath.Match` and to `regexp`.
  This slice adds **exactly one sink** to that enumeration: a field of a `protocol.SlashCommandListPayload`.
  It reaches no `exec.Command` argument, no `filepath.Join`, no `filepath.Match`, no `regexp`, no
  `eventring` append. No value from this type is treated as a command vocabulary, and `MapEvent` executes
  nothing.
- **[Error messages, logs, telemetry]** No findings, and the rule is load-bearing rather than incidental: the
  arm emits **no log record at all**, so no workspace-authored string can reach one. `MapEvent` has no
  logger, takes none, and returns no error whose text could carry a `Name` or a `Description`. That is
  consistent with `eventKind`'s `SlashCommandList` arm, which returns the variant *name* only for the same
  reason, and with the #833 posture restated in `internal/relay`'s `v2session_settings.go` and
  `internal/sessions`' `pool.go`. The Phase B rule this implies: no `slog` call and no `fmt.Errorf` naming a
  carried field goes in this arm.
- **[Concurrency]** No findings, held by construction rather than by a lock, and the *inner* sharing is the
  part that needs the argument. The arm builds a fresh outer backing array and reads the event's rows without
  writing them; the inner `Aliases` and `TruncatedFields` slice headers are carried, so the payload and the
  event share those arrays deliberately. That sharing is safe only under the never-mutate-through rule, and
  the reason it is a data-race concern rather than a style rule is #2005: it will read the mapped payload on
  a relay-leg goroutine. The retained-session hold that would make the writer side concrete is #2004's and is
  not in the tree, so the rule is enforced here ahead of the hazard rather than after it — which is the
  correct order. `TestMapEventSlashCommandListDoesNotMutateTheEvent` is the deterministic check, not a
  reviewer's promise.
- **[Network & I/O — resource exhaustion]** **OUT OF SCOPE, deferred to #2002, and the deferral is
  *enforced* rather than merely intended.** The finding is real: the producer's caps bound one entry at
  256 + 256 + 256 + 8×64 = 1280 runes of content, and `maxSlashCommandListEntries` bounds the list at 128
  entries. At one byte per rune plus per-entry keys and punctuation that is roughly 182 KB, and with JSON's
  worst-case six-bytes-per-rune escaping roughly 1.0 MB — against the 65519-byte v2 application-envelope cap.
  So a payload this arm can construct today exceeds the envelope by ~2.8× at best and ~16× at worst, and the
  committed 51-entry capture's 14,277 bytes is not the bound, only an observation. What makes this a
  deferral rather than a MUST FIX is that **no path routes a `SlashCommandList` to `MapEvent`**: `emitMapped`
  is reached only from `interactiveTurnEmitterV2.Handle`'s typed arms and none of those is this variant, and
  `resolveBoundModelList` is handed a `ModelList` explicitly. No frame of this type is produced after this
  commit. The two slices that *would* make one reachable are both blocked behind the bound —
  `#2003 → #2002 → #2001` and `#2005 → #2002`, verified against the issue graph — so the over-cap frame
  cannot ship before its bound does. Adding a bound here anyway would be the second-place-the-limit-is-decided
  mistake, and the ticket forbids it by name.
- **[Tokens, secrets, credentials]** Not applicable, and the reason is the field set rather than an audit: the
  payload's four carried fields are a command name, an argument hint, a description and aliases, none of
  which is a credential. The fifth value, `ConversationID`, is daemon-supplied and already crosses on every
  v2 interactive payload; this arm introduces no new exposure of it and generates, stores and compares no
  secret.
- **[File operations]** Not applicable. The arm opens, stats, creates and renames nothing; it performs no
  filesystem access whatsoever, and no carried string reaches a path constructor (see Subprocess above, which
  enumerates that).
- **[Cryptographic primitives]** Not applicable. No randomness, no hashing, no key material, no comparison
  against a secret. The arm is a struct-to-struct copy.
- **[Threat model alignment]** The relevant `docs/protocol-mobile.md` § Security model threat for an outbound
  push is a hostile *payload* rather than a hostile peer — the frame is sealed and authenticated by the
  transport before it leaves, which this arm does not touch. The payload-side threat is untrusted text
  rendered by a client, which is answered above under Trust boundaries and assigned to the client by the wire
  type's own doc. The envelope-size threat is § 6's, deferred to #2002 with the ordering enforced. No threat
  relevant to this arm is left unassigned.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-02
