# 2090 — `last_seen_ts` drives no backfill, and § Mode A says it does

Documentation-only correction to `docs/protocol-mobile.md`, plus the one Go doc
comment carrying the same false claim. No wire field, fixture, test or behaviour
changes.

## Files read

- `docs/protocol-mobile.md` — the five `last_seen_ts` sites, § Reconnect /
  Backfill semantics in full, § `hello` (v2-specific note), and the Changelog's
  head. This is the document under correction.
- `internal/protocol/handshake.go` → `HelloClientPayload` — the struct's doc
  comment asserts "when present it triggers a backfill". The declaration and
  tags are out of scope; only the comment moves.
- `internal/protocol/handshake_test.go` → the `LastSeenTS` assertions — proves
  the field is still decoded and round-tripped, which is *why* the honest
  wording is "accepted and ignored" rather than "unsupported". Not modified.
- `internal/relay/v2session_handshake.go`, `internal/relay/v2session_replay.go`
  → `replayMissed` — the replay that actually exists. Its doc block states the
  three properties Mode A must publish: bounded by `MaxEventsPerConversation`,
  scoped to the daemon-resolved conversation via `cursor()`, never to one the
  phone names.
- `internal/eventring/ring.go` → package doc + `MaxEventsPerConversation` — the
  in-memory claim ("deliberately does NOT survive a full daemon-process
  restart") and the bound, 1024. Also the eviction *policy*, which the ticket
  does not state: the oldest `assistant_delta` is evicted first and
  control-class events are retained in preference, so the bound is not a plain
  FIFO age-out.
- `internal/protocol/testdata/hello_client.json` — carries `last_seen_ts`;
  unchanged, and the reason the example block keeps the key.
- `CODING-STYLE.md` § Comments — Citing Other Code — every citation in this plan
  and in the Go comment names a symbol, never a line.

## Context

`docs/protocol-mobile.md` publishes in four places that `last_seen_ts` triggers
a bulk-history backfill. Nothing reads it. Verified this session: `LastSeenTS`
occurs in exactly two Go files — its declaration in `HelloClientPayload` and the
round-trip assertions in `handshake_test.go`. There is no consumer in
`internal/relay` or anywhere else. A client that sets the field gets
byte-for-byte the behaviour of one that omits it, and the failure is silent: the
field is accepted, decoded, and dropped.

The replay that does exist is driven by `last_event_id` through `replayMissed`,
and it is a mid-outage catch-up mechanism rather than history. Publishing it as
"bulk transcript content" oversells it in the direction that already cost real
work — the claim was read as a working daemon capability during a 2026-09-04
desktop investigation and used to size client work as "the daemon half already
exists".

Same shape as #1860 and #2010, which corrected published claims in this file
that had gone false, and it takes the same treatment: correct the live prose,
leave earlier changelog entries standing as history, add one dated entry.

No ADR is warranted. Whether `last_seen_ts` should be implemented or removed is
#2091's decision (real history, from a daemon-owned log); this ticket only stops
the document from claiming a capability that does not exist.

## Design

### The one design decision: Mode A keeps its name

Removing the `last_seen_ts` bullet leaves Mode A defined by nothing, because the
bullet was what made "bulk transcript content" true. The naive repair — rename
Mode A away from "cursor backfill" — is wrong, and this is the trap worth
recording: **two live sites outside the section cite Mode A by that name** and
would be orphaned by a rename. In § `model_list`, the paragraph beginning *"A
client that missed the frame has no snapshot to ask for"*; in
§ `slash_command_list`, the paragraph beginning *"The delivery window is
narrower than 'emitted' suggests"*. Both say "Mode A, a cursor backfill", both
describe the `last_event_id` ring replay, and both are **already accurate** under
the corrected framing — they are left untouched.

So: **"cursor backfill" survives as Mode A's name** (a ring replay is literally
driven by a cursor); **"bulk transcript content", "History" and "bulk-history"
go**. The two-mode split holds — the axis is still data class, not outage length
— it is only Mode A's membership that shrinks to one bounded in-memory
mechanism.

### The five `last_seen_ts` sites

Located by `grep -n last_seen_ts docs/protocol-mobile.md`; anchors are
authoritative, line numbers drift.

| Site | Anchor | Disposition |
|---|---|---|
| § Connection lifecycle → Phone, step 3 | *"Include `last_seen_ts` … to trigger backfill."* | **Rewrite.** A false instruction in the procedure a client author follows. Replaced by: send the hello with the device token; a phone reconnecting mid-turn includes `last_event_id`, with a link to § Reconnect replay & resync. |
| § Application message types, `hello` row | *"Includes device-token, last_seen_ts; optional `last_event_id` …"* | **Rewrite.** Not literally false but lists the field as a peer of the device-token and gives only its neighbour a purpose. Must name it accepted-and-ignored. |
| § `hello` (v2-specific note), example JSON | `"last_seen_ts": "2026-05-08T08:14:02Z"` | **Key stays** — still accepted vocabulary, and removing it would imply a decoder rejects it. |
| § `hello` prose, below the block | — (today documents `last_event_id` at length, `last_seen_ts` not at all) | **New short paragraph.** This is where the inertness lands for the example block, satisfying AC1's "the prose owning each says the field is inert". |
| § Reconnect / Backfill semantics → Mode A | *"`last_seen_ts` drives the v1 bulk-history backfill …"* | **Delete the bullet.** The primary false claim. |
| § Worked example, wire trace comment | *"…token + last_seen_ts"* | **Stands as-is.** A trace of bytes genuinely on the wire; no drive claim. Covered by the § `hello` prose above. |

### § Reconnect / Backfill semantics — four coordinated edits

The section's intro, Mode A's heading, Mode A's bullets and the closing
two-modes paragraph must agree; three of the four assert the removed claim
independently, so editing only the bullet leaves the section incoherent.

1. **Intro** — the axis sentence *"bulk transcript content keeps a cursor
   backfill; control state is always a cheap current-state snapshot"* restates
   Mode A's data class. Reword so Mode A's class is the interactive event
   stream, keeping the data-type-not-outage-length axis intact.
2. **Mode A heading** — drop "bulk transcript content" and the "History and"
   clause; name the class as the interactive event stream, keep "cursor
   backfill".
3. **Mode A body** — the `last_seen_ts` bullet goes. The surviving
   `last_event_id` bullet gains the three properties AC2 names: in-memory (lost
   on daemon restart), bounded at `MaxEventsPerConversation` = 1024 per
   conversation with `resync` as the age-out answer, and scoped to the
   conversation the daemon resolves rather than one the client names. State
   them compactly and link § Reconnect replay & resync, which owns the full
   enumeration — do not restate its five cases. Add the forward pointer to
   #2091 for real history, and say plainly that Mode A carries no history and
   no transcript content.
4. **Closing paragraph** — *"a cursor backfill for transcript content"* becomes
   the corrected class. The complements-not-alternatives point and the
   mechanism-chosen-by-data-kind point both survive unchanged.

**Left standing deliberately:** the note about the missing sixth reconcile
(`model_list`) — it belongs to a sibling family. Every changelog entry below the
new one, per #1860's and #2010's precedent: rewriting history destroys the
record of what each ticket left standing.

### `internal/protocol/handshake.go`

`HelloClientPayload`'s doc comment currently reads "LastSeenTS is optional; when
present it triggers a backfill". Replaced by wording that says the field is
accepted, decoded and has no consumer, that a client setting it gets identical
behaviour to one omitting it, and that the replay a reconnecting phone wants is
`LastEventID`. The existing `LastEventID` paragraph in the same block already
describes that replay and is not touched.

**The diff on this file is comment-only** (AC3): no declaration, tag, field
order or any non-comment line changes. The struct keeps `LastSeenTS *time.Time`
with its `omitempty` exactly as it stands. Verified by reading the diff before
commit.

### Changelog

One entry at the head of § Changelog (newest-first), in the established shape:
`` - `2026-09-05`: **<bold headline>** (#2090). `` — recording which claim was
false, that the field is accepted and ignored, what replaced the claim, which
sites moved and which were left standing on purpose, and that the diff is
documentation-only.

## Concurrency model

None. No runtime code changes.

## Error handling

None. No runtime code changes; no failure modes introduced.

## Testing strategy

No new tests, and no test changes (AC5). The claim under correction is a
documentation claim, and the code it describes is unchanged, so there is no
behaviour to assert. Verification is:

- `grep -n last_seen_ts docs/protocol-mobile.md` returns the same five hits;
  each is read and none states or implies a drive claim (AC1).
- `git diff internal/protocol/handshake.go` shows only `//`-comment lines
  changed (AC3) — checked explicitly, not assumed.
- `git status` shows `internal/protocol/testdata/hello_client.json` and every
  `_test.go` unmodified (AC5).
- `go test -race ./internal/protocol/...` stays green (the fixture and its
  round-trip assertions are untouched), `go vet ./...`, `go build ./cmd/pyry`.
- `make cite-guard` is exercised by the verifier's gate; this plan and the Go
  comment cite symbols only, so nothing new can trip it.

## Open questions

1. **Does the corrected Mode A still deserve to be a "mode" at all, given its
   membership is one mechanism?** Resolved in Design: yes. The two live sites
   citing "Mode A, a cursor backfill" by name make the taxonomy load-bearing
   outside the section, and the axis (data class, not outage length) is what the
   section exists to publish. Collapsing it would orphan both sites and lose the
   axis.
2. **Does the § Worked example wire trace need an inline note?** Resolved in
   Design: no. AC1 permits the two illustrative sites to keep the key provided
   the prose owning each says the field is inert; the new § `hello` paragraph is
   that prose for both.
