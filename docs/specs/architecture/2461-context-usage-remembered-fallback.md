# #2461 — answer `request_context_usage` from the remembered reading

## Files read

- `cmd/pyry/relay_context_usage.go` → `contextUsageResolve`, `contextUsageResolver.Get`,
  `contextUsageResolver.fly`, `contextUsageRecorder.record` — the two layers the fallback sits
  between, and the settle in `fly` that is the reason the fallback must never become a flight.
- `internal/protocol/interactive.go` → `ContextUsagePayload`, its `MarshalJSON`, and the file
  header's standing no-`omitempty` rule — the key set `as_of` joins and the rule it becomes the
  first exception to.
- `internal/protocol/handshake.go` → `HelloClientPayload.LastSeenTS` — the `*time.Time` +
  `omitempty` precedent the ticket names, and the only shape whose absence `omitempty` actually
  tests.
- `internal/relay/v2session_seams.go` → `ContextUsageFor` — the contract block that today forbids
  exactly this fallback ("never substitutes a zero or retained reading").
- `internal/relay/v2session_contextusage.go` → `handleRequestContextUsage`,
  `rejectContextUsageConversationNotFound`, `rejectContextUsageUnavailable` — the two reject arms
  AC-3 freezes, and the numbered step list that stays true unedited.
- `internal/conversations/conversation.go` → `ContextUsageReading`, `Conversation.LastContextUsage`
  — the five stored values, and why the three inventories are deliberately absent from them.
- `internal/conversations/registry.go` → `Registry.Get`, `Registry.SetLastContextUsage` — `Get`
  returns a row copy that shares the `*ContextUsageReading` pointee, and the setter replaces that
  pointer under `r.mu` rather than mutating it in place.
- `cmd/pyry/relay.go` → `startRelayV2` — where `contextUsageRec` is built and `resolver.rec`
  assigned; the registry handle this ticket needs is already there.
- `internal/protocol/interactive_test.go` → `contextUsageWireKeys`,
  `TestContextUsagePayload_ZeroValueEncoding`, `TestContextUsagePayload_RoundTrip` — `assertWireKeys`
  rejects an unexpected key as well as a missing one, which is what makes the shape choice below
  load-bearing rather than cosmetic.
- `cmd/pyry/relay_context_usage_test.go` → `ctxUsageResolverFor`, `ctxUsageRecorderFor`,
  `fakeContextUsageQuerier`, `fakeClock` — the doubles the new tests reuse unchanged.
- `docs/knowledge/features/conversations-registry-crud.md` § `SetLastContextUsage` — last write
  wins, no ordering machinery, and `contextUsageRecorder` is the registry's sole caller for this
  field. That is why the read side belongs on the recorder too.

## Context

`request_context_usage` (#2431) can only answer from a live child. `contextUsageResolve` returns
not-ok whenever `resolveBoundRunner` finds nothing bound, and `fly` settles a flight as a refusal
when the child never answers, so `handleRequestContextUsage` rejects with
`CodeContextUsageUnavailable`. A dormant conversation — and every conversation between a daemon
restart and its first turn end — has no answer to give even though #2460 now makes the registry
remember one.

This ticket spends that memory. When no fresh reading can be taken and the conversation's registry
row holds a `LastContextUsage`, the resolver answers from it, stamped with `as_of` so a client can
tell a remembered answer from a live one. That stamp is not decoration: #2460 stores no categories,
no MCP tools and no memory files, and `ContextUsagePayload`'s own doc says an empty inventory is a
*positive* reading. Without `as_of`, a stored summary and a live reading whose breakdown was
genuinely empty are byte-identical.

No ADR: this changes no boundary. It adds one optional key to a published payload and one fallback
branch beneath an existing seam.

## Design

### 1. `protocol.ContextUsagePayload` gains `AsOf *time.Time` (`as_of,omitempty`)

```go
AsOf *time.Time `json:"as_of,omitempty"`
```

Three shape decisions, each forced:

- **Pointer, not a value.** `omitempty` does not test a `time.Time` — a zero `time.Time` is a
  struct, and `encoding/json` would encode `"0001-01-01T00:00:00Z"` on every live frame.
  `HelloClientPayload.LastSeenTS` is this repo's precedent for exactly that reasoning.
- **`omitempty`, and it is this file's first.** The header rule ("No field carries omitempty: every
  field is always present on the wire…") has to be amended, because AC-1 freezes the bytes both
  existing producers emit today. `CompactionBoundaryPayload`'s two counts are the file's other
  departure and they went the other way — pointers with *no* `omitempty`, marshalling a literal
  `null` — but that frame was new, so an always-present key cost nothing. This frame ships from two
  producers already, and both `internal/protocol/testdata/context_usage.json` and
  `context_usage_empty.json` pin live readings whose bytes must not move. A `null` key would move
  all of them.
- **RFC 3339 UTC comes from the stored value, not from a formatter here.** `SetLastContextUsage`
  normalises `AsOf` to UTC at the one door, so `time.Time`'s own marshaller writes the `Z` form
  with no help from this layer.

The doc block states the contract AC-1 pins: absent on a live reading, present on a remembered one,
and a client must read it *before* trusting the three inventories, because a remembered answer's
inventories are empty for a reason that has nothing to do with what claude reported.
`MarshalJSON` is untouched — nil inventories already normalise to `[]`.

`turnbridge.MapEvent`'s `ContextUsage` arm needs no edit: it builds a composite literal that simply
never names the new field, so every fresh reading omits the key by construction rather than by a
rule someone has to remember.

### 2. `contextUsageResolve` tells "not hosted" from "hosted, nothing to ask"

`resolveBoundRunner` collapses three outcomes into one false, and only the first — no registry row
— may stay a hard refusal, because a conversation with no row has neither a canonical id to key on
nor a reading to fall back to. The signature of `contextUsageResolveFunc` does not change; the
hosted-but-unaskable cases return **the canonical id with a nil querier and a true bool**:

| registry row | bound session + live child | runner implements `contextUsageQuerier` | returns |
|---|---|---|---|
| absent | — | — | `nil, "", false` |
| present | no | — | `nil, conv.ID, true` |
| present | yes | no | `nil, conv.ID, true` |
| present | yes | yes | `querier, conv.ID, true` |

The registry `Get` that establishes hosted-ness is this function's own; `resolveBoundRunner` keeps
its `CurrentSessionID == ""` guard and its #678 isolation role untouched, and stays the only thing
that decides what "bound" means.

### 3. `contextUsageResolver.Get` grows one fallback point, not four

`Get` today has four exits that can report a refusal (cached-settled inside the window, joined
in-flight, awaited after installing, and the unresolvable arm). Bolting the fallback onto each is
four places to keep in agreement. Instead `Get` splits:

- `Get(ctx, conversationID)` — resolve, then **one** `fresh` call, then **one** `remembered` call.
- `fresh(ctx, querier, canonicalID) (ContextUsagePayload, bool)` — today's collapse-map body,
  unchanged in behaviour, returning `(zero, false)` immediately when `querier == nil`.
- `remembered(canonicalID) (ContextUsagePayload, bool)` — reads the stored summary and maps it.

```go
querier, canonicalID, ok := r.resolve(conversationID)
if !ok {
    return protocol.ContextUsagePayload{}, false   // permanent conversation.not_found
}
if payload, ok := r.fresh(ctx, querier, canonicalID); ok {
    return payload, true                            // live: no as_of
}
return r.remembered(canonicalID)                    // stored, or the retryable refusal
```

**A nil querier never reaches the flight map.** The fallback is a registry read costing no tokens,
so there is nothing to collapse; and a flight settled `ok` would hand the remembered reading to
`fly`'s deferred `r.rec.record`, writing it straight back with a fresh `as_of` — a stale figure
that renews itself on every ask and can never look stale again. Only a reading claude produced
reaches `record`, and the structural guarantee is that the fallback is not a flight at all.

**A departed caller is answered from memory like any other.** `fresh` returns false both when a
flight settled not-ok and when the caller's own context ended; the two are not separated. Telling
them apart would mean `await` reporting three states, and the seam's contract is about whether
there is an answer, not about who is still listening. Naming it here so the verifier reads it as a
decision rather than an oversight.

### 4. `contextUsageRecorder` gains the read side: `last`

```go
func (r *contextUsageRecorder) last(id conversations.ConversationID) (conversations.ContextUsageReading, bool)
```

nil receiver or nil registry → false, matching `record`'s inert posture, so the unwired daemon and
every existing test that builds a resolver without a recorder keep behaving exactly as they do now.
The recorder already owns the write door for this field; putting the read on the same type keeps
the registry handle in one place and means `contextUsageResolver` still holds no registry of its
own.

Returning a value dereferenced from `conv.LastContextUsage` is race-free for a structural reason
worth writing down: `SetLastContextUsage` copies into a fresh local and replaces the *pointer*
under `r.mu`, never mutating a published pointee, and `Registry.Get` publishes it under the same
lock.

`remembered` maps the five stored values into the payload, takes the address of a local copy of
`AsOf`, and leaves all three inventories nil and all three dropped counts zero.
`MarshalJSON` turns the nils into `[]`, so there is nothing to fill.

### 5. The `ContextUsageFor` seam doc block

Its comma-ok paragraph currently promises the implementation "never substitutes a zero or retained
reading", which this ticket contradicts. Amended in this change rather than left disagreeing with
the code: the relay still substitutes nothing and still translates a false to retryable
`context_usage.unavailable`; what changes is that an implementation MAY answer from a reading it
stored earlier, and when it does it MUST stamp `AsOf`. "CURRENT breakdown" in the opening line
becomes current-or-last-known. The zero-payload argument stays, because it is the reason `as_of`
exists rather than a zero-filled answer. `handleRequestContextUsage`'s numbered steps and
`emitContextUsageReply` are untouched — both stay true as written.

## Concurrency model

No new goroutine, no new lock, no change to `r.mu`'s scope. The fallback runs synchronously on the
calling `appFrameWorker` and performs one `Registry.Get` — a bounded in-memory scan under the
registry's own mutex. Lock order is unchanged: `remembered` is reached only after `r.mu` has been
released by `fresh`, so the registry lock is never taken while the resolver's is held.

`fly` and its settle are untouched, so every existing property — shared flights, daemon-context
detachment, one record per settle — holds unedited.

## Error handling

| state | outcome |
|---|---|
| no registry row | `false` → permanent `conversation.not_found` (unchanged) |
| hosted, no bound session or no live child, stored reading | payload + `as_of` |
| hosted, runner is not a `contextUsageQuerier`, stored reading | payload + `as_of` |
| hosted, flight settled not-ok, stored reading | payload + `as_of` |
| hosted, no stored reading | `false` → retryable `context_usage.unavailable` (unchanged) |
| unwired resolver / nil receiver | `false` (unchanged) |

Nothing on the new path logs. `last` returns a comma-ok and no error, so there is no error value
carrying a model string or a timestamp for a caller to log, and no new log call is added anywhere.

## Testing strategy

`internal/protocol`:

- `as_of` is absent from a zero-valued payload and from both existing fixtures — pinned already by
  `contextUsageWireKeys` through `assertWireKeys`, which rejects unexpected keys. The three existing
  tests are the regression guard for AC-1 and must stay green **unedited**.
- New: a payload carrying `AsOf` encodes exactly one extra key, whose value is the RFC 3339 `Z`
  form of the instant, and decodes back to an equal pointer.

`cmd/pyry` (beside the existing cases in `relay_context_usage_test.go`, using a registry-backed
resolver like `TestContextUsageResolver_SettledFlightRecordsReading`):

- Hosted with a stored reading and **no bound runner** → payload carries the canonical id, the five
  stored values, `as_of`, `[]`×3 and zero dropped counts; the querier is asked nothing.
- Hosted with a stored reading and a **runner that is not a `contextUsageQuerier`** → same answer.
- Hosted with a stored reading and a **flight that settles not-ok** → same answer, and the querier
  was asked exactly once (the fallback adds no second round trip).
- Not in the registry → still refused, and the flight map still holds zero entries.
- Hosted, askable, no stored reading → still refused (AC-3's second half).
- A remembered answer does not shadow a live one: refuse first, flip the child to answering, advance
  past `contextUsageCollapseWindow`, ask again → the live numbers with a nil `AsOf`.
- A remembered answer does not age itself: ask twice from memory with the clock advanced between,
  then read the registry back — the stored reading and its `AsOf` are byte-identical to what was
  stored, and no registry file is written at all.
- No log record is emitted on any fallback arm (`ctxUsageLogRecords` over the recorder's logger).

`internal/relay`: no test change. The seam's edit is a doc block.

## Open questions

1. Does a caller whose own context ended deserve a remembered answer, or a plain refusal?
   Resolved in Design §3: answered from memory, uniformly, and stated there as a decision.
2. Should the remembered answer be cached in the flight map for the collapse window?
   Resolved in Design §3: no — it costs no tokens, and any flight settled `ok` would be re-recorded
   by `fly`'s settle.

## Documentation handoff

Owned by the documentation stage, not this ticket. All in `docs/protocol-mobile.md`, and all
pending:

- § `context_usage` field table gains `as_of`: absent on a live reading, present on a remembered
  one, and the reason a client must read it before trusting the three inventories — a remembered
  answer's inventories are empty because they were never stored, not because claude reported none.
- § *Asking for a context usage reading on demand*: the reject table's second row now fires only
  when there is no stored reading either, and the surrounding prose should say a dormant
  conversation is answered from memory rather than refused.
- § *Error codes*, the `context_usage.unavailable` row, carries the same correction.
- One dated changelog entry.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. The one boundary is `contextUsageResolve`, which turns the
  client's untrusted string into a registry-canonical `conversations.ConversationID` through a
  byte-exact `Registry.Get` and nothing else — no path join, no logging, no error wrapping. The
  new hosted-but-unaskable arm returns `conv.ID`, never the request string, so `remembered` stamps
  the payload's `conversation_id` from the daemon's own record; an echo would be a fresh way for a
  paired client to have its own bytes reflected back to it. `handleRequestContextUsage`'s
  `KnownConversation` gate still runs first and is unedited.

- **[Concurrency]** SHOULD FIX — the nil-querier check must be the **first statement of `fresh`,
  before `r.mu.Lock()`**, not a condition somewhere inside it. `fly` dereferences `querier`
  unconditionally, so a nil one reaching the flight map would install an entry and then panic on a
  nil-interface call; before this ticket a nil querier only ever arrived with `ok == false`, so the
  invariant is newly load-bearing. Phase B carries a comment naming the panic it prevents, and an
  assertion on `len(r.flights) == 0` after a hosted-but-unaskable ask — an assertion on the
  invariant itself rather than on the behaviour it produces.

- **[Concurrency]** No findings on the read. `remembered` cannot observe a torn reading:
  `SetLastContextUsage` copies into a fresh local and replaces the *pointer* under the registry's
  `r.mu`, never mutating a published pointee, and `Registry.Get` publishes it under that same lock;
  dereferencing yields a value copy. Lock ordering is unchanged and one-deep — `remembered` runs
  after `fresh` has released the resolver's mutex, so the registry lock is never taken beneath it.
  The child-comes-up-between-resolve-and-read window is real and benign: one ask is answered from
  memory that could have been live, the next is live, and `as_of` makes the staleness legible.

- **[Concurrency]** No findings, and one structural property worth not optimising away: because
  `remembered` performs its **own** `Registry.Get` rather than carrying `resolve`'s row forward, a
  conversation deleted between the two calls is refused rather than answered from a row that no
  longer exists. Threading the row through to save a scan would convert that fail-closed edge into
  a fail-open one.

- **[Network & I/O]** SHOULD FIX — verify rather than assume that the new key does not eat the
  v2 application-envelope reserve. `TestMapEventContextUsageWorstCaseAgainstV2EnvelopeCap` drives
  the real `MapEvent` and measures serialised envelope bytes; `AsOf` is nil on that path so the key
  is absent and the measurement should not move, but Phase B must run `internal/turnbridge` and
  `internal/relay` in its touched scope to prove it, not just the two packages being edited.
  Separately, the remembered `Model` needs no new bound: it can only have arrived through
  `SetLastContextUsage`, whose sole caller feeds it from streamsup's `decodeContextUsage` — the
  same capped decoder the live path uses.

- **[Error messages, logs, telemetry]** No findings, and AC-5 is enforceable because of a shape
  choice: `last` returns a comma-ok rather than an error, so no reading is ever wrapped into an
  error string that a caller might log. The new path adds no log call at any level, the relay's
  two reject messages stay fixed constants derived from nothing, and the fallback tests assert zero
  records on the recorder's logger.

- **[File operations]** No findings — the fallback performs no file operation at all. It must
  never call `Registry.Save` or `SetLastContextUsage`: besides re-recording a stale figure (the
  ticket's own prohibition), a read path that saved would let a paired client drive an fsync per
  request. The conversation id never becomes a path component on this verb; that is
  `handleRequestHistory`'s hazard, and step 3 of `handleRequestContextUsage`'s block already states
  why it does not transfer here.

- **[Resource exhaustion]** No findings, cost named. A dormant ask now costs three bounded
  in-memory registry scans (`contextUsageResolve`'s, `resolveBoundRunner`'s, `remembered`'s) where
  it cost one, over a slice of tens of rows, and the collapse window never bounded that cost on
  main either — `resolve` runs before the collapse check on every ask today. Same order, same
  authenticated-client-only reach; not worth distorting the design for, and see the fail-closed
  property above for why the obvious de-duplication is the wrong trade.

- **[Subprocess execution]** Not applicable by design, and it is the point of the ticket: the
  fallback path touches no child, spawns nothing, and builds no argv. AC-2's "the child is asked
  nothing extra on this path" is asserted directly through the fake querier's call count.

- **[Tokens, secrets, credentials]** Not applicable. `ContextUsageReading` holds one bounded model
  string, three ints and a timestamp — by its own doc it deliberately stores no category names, MCP
  server names or memory-file paths — so the memory this ticket reads cannot hold credential
  material to leak.

- **[Cryptographic primitives]** Not applicable. No randomness, no comparison against a secret, no
  key. The reply still leaves through `handleRequestContextUsage` → `forwardToRun`, so sealing
  stays on Run under the single-owner send CipherState; this ticket adds no emit path.

- **[Threat model alignment]** No findings against `docs/protocol-mobile.md` § Security model.
  Answering from memory discloses, to a paired client, a reading it would have received live while
  the child was up, plus an `as_of` that reveals when the daemon last recorded a reading for a
  conversation whose live turn events that client already receives — no new disclosure class, and
  pairing remains the authorization boundary. Conversations the daemon does not host are still
  refused permanently and still leave no flight-map entry. **Out of scope, named by the ticket:**
  the desktop labelling an `as_of`-carrying reading as remembered rather than live is an explicit
  follow-up, not this ticket.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-16

## Revisions

**2026-09-16 — implementation.** No design departure: every interface, split and decision above
landed as written, and both Open Questions were resolved in the plan rather than during the build.
Two things are worth recording.

- **The security review's first SHOULD FIX was demonstrated, not hypothesised.** The RED run — the
  fallback tests against the unmodified resolver — panicked with a nil-pointer dereference inside
  `fly`, reached from `Get`'s flight installation, exactly as the finding predicted. The guard is
  therefore the first statement of `fresh`, ahead of the mutex, and
  `TestContextUsageResolver_MemoryInstallsNoFlight` asserts the invariant directly. The second
  SHOULD FIX checked out: `internal/turnbridge` is green in the touched scope, so the v2
  application-envelope reserve is unmoved — `AsOf` is nil on the `MapEvent` path.

- **Measured size: ~924 lines of total written work** (617 in the implementation commit, ~307 in
  this plan) against a one-ticket ceiling of 800, and the ticket's own estimate of ~850. The
  overage is stated rather than fixed, per the floor-beats-ceiling rule: the only cut available is
  the `as_of` field from the fallback that is its sole consumer, which would produce a child
  nothing outside the family calls. Every other line of the table holds with room — 3 production
  files, no new exported type, no consumer call site updated, 5 criteria, no new reject branch.
