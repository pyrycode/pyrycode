# #1610 — answer `request_session_settings` for the conversation the client named

**Size:** S (confirmed; see § Sizing). **Security-sensitive:** yes — § Security review at the end.

## Files to read first

Read these before writing anything. Each entry is a symbol, not a line — resolve it with
`codegraph_search` / `codegraph_node`.

**The handler and its seams (the change site):**

- `internal/relay/v2session_settings.go` → `handleRequestSessionSettings` — the whole doc
  comment plus the `addressable` block and the three seam reads. This is the entire
  production change on the relay side. Its SCOPE paragraph and its inline decode comment
  both assert the retired design.
- `internal/relay/v2session_settings.go` → `handleSetSessionSettings`, `settingsReplyError`
  — the neighbouring write path. Read for the ordering doctrine (capability gate first) and
  the "never echo attacker bytes" posture the read path mirrors. **Do not modify either.**
- `internal/relay/v2session_seams.go` → `V2SessionConfig` fields `BootstrapSessionID`
  (deleted here), `RunConfigFor` (the replacement), `KnownConversation` (its doc names this
  verb as a reader and stops being true), `SnapshotSettings` / `SnapshotUsage` (**unchanged,
  docs unchanged** — `handleRequestSnapshot` still reads them).
- `internal/relay/v2session_seams.go` → `RunConfig` — the six-field value. Its doc states
  why "not addressable" is the comma-ok and never a field: the zero `RunConfig` is
  indistinguishable from a genuine all-defaults session. That is load-bearing for the
  fail-closed rule below.
- `internal/relay/v2session_replay.go` → `handleRequestSnapshot` — the other
  `KnownConversation` reader, and the only reader of `SnapshotSettings` / `SnapshotUsage`
  after this change. **Read to confirm you must not touch it.**

**Production wiring:**

- `cmd/pyry/relay.go` → `startRelayV2` — the `V2SessionConfig` literal: the
  `BootstrapSessionID:` entry and its explanatory comment go; the `RunConfigFor:` entry's
  comment loses its "nothing consults it in this ticket by design" forward reference.
- `cmd/pyry/relay.go` → `runConfigFor` — the composed seam. Read the ordering paragraph:
  usage is consulted only after `resolve` says yes, and only with the id `resolve` returned.
- `cmd/pyry/main.go` → `resolveBoundRunSettings`, `boundRunSettings`, `sessionSettingsReader`
  — the producer's refusal. Extract: an unknown conversation **or** an empty
  `CurrentSessionID` returns `(zero, false)` before the pool is touched, and `SettingsFor`
  has no `""`-is-bootstrap convention. This is why the relay inherits the refusal instead of
  re-deriving one. `cmd/pyry/main.go`'s `relayWiring` literal keeps `bootstrapIDFn` —
  `bootstrapSnapshotUsage` still needs it. **Do not remove it.**
- `cmd/pyry/snapshot_usage.go` → `snapshotUsageFor`, `bootstrapSnapshotUsage` — extract one
  fact for the e2e expectations: every failure collapses to `contextwindow.Read("")`, the
  fresh-session report (zero used, **default non-zero window**). A minted session with no
  transcript therefore reports `used_tokens: 0` and a non-zero `window_tokens`.

**Tests to rework:**

- `internal/relay/v2session_settings_read_test.go` → `readSeams`, `allReadSeams`,
  `countingReadSeams`, `readCounts` (and its `runConfig()` method), `readManagerFor` — the
  fixture layer. Every one of them changes.
- Same file → `TestV2Session_RequestSessionSettings_ReportsRunConfig`,
  `_AnswersWithoutASnapshotter`, `_NilSeamsDegradeToZero`, `_IgnoresAnyPayload`,
  `_ConversationGate`, `_NonInteractiveMakesNoLookup` — the six functions that change.
- `internal/e2e/relay_v2_stream_run_config_test.go` → `TestRelayV2_StreamRequestSessionSettings`
  — the production-wiring proof. Its ~35-line doc comment inverts with it.
- `internal/e2e/per_conversation_eviction_test.go` → `createConversationViaPhone`,
  `drainForReply` — the same-package helper that mints **and binds** a conversation and
  drains past interleaved broadcasts to the correlated reply. Signature takes
  `(*noise.CipherState, *noise.CipherState)`, which is exactly what
  `driveHandshakeToOpenDaemonInteractive` (`internal/e2e/handshake_interactive_helpers_test.go`)
  returns. **A same-named helper with a different signature exists in
  `internal/e2e/realclaude` — that is a different package and not the one to use.**
- `internal/relay/v2session_replay_test.go` and the rest of `internal/relay`'s suite — grep
  for other `BootstrapSessionID:` wirings before deleting the field. As of this spec there is
  exactly one, in `readManagerFor`.

**Docs:**

- `docs/protocol-mobile.md` — five claim sites plus the changelog; see § Protocol doc.
- `docs/knowledge/features/v2-session-manager.md` § the `request_session_settings` handler,
  and `docs/knowledge/features/protocol-package.md` — both carry references to the deleted
  seam. **Read them for context; the documentation phase owns them. Do not edit either.**

## Context

A run-configuration change from a client lands on the **bootstrap** session, whichever
conversation the client is actually in. Every layer below the read is already
conversation-correct: `handleSetSessionSettings` passes the client's `session_id` straight
through, the `cmd/pyry` adapter passes it to the pool, and `Pool.UpdateSettings` is
per-session. The id was wrong before it left the daemon, because
`handleRequestSessionSettings` reads three bootstrap-scoped seams and puts the bootstrap's id
on the reply the client then writes back with.

#1609 built the replacement and wired it: `RunConfigFor(conversationID) (RunConfig, bool)`
reports one conversation's bound session id, model, effort, YOLO **and** context-window
figures together, with comma-ok for whether the conversation resolved. Nothing consults it.
This ticket makes the read verb consult it, and only it.

Two consequences fall out of that and are not optional extras:

1. **The bootstrap session-id seam is retired.** Leaving `BootstrapSessionID` wired-but-unread
   would leave a live route to the exact value AC #2 forbids. Deleting the field also deletes
   its doc block, which is what closes the "has since been revisited" paragraph #1609 left
   behind.
2. **An absent or empty `conversation_id` becomes the "nothing to address" reply.** There is
   no coherent middle: `RunConfigFor`'s producer refuses the empty id by construction, so
   keeping bare → bootstrap would mean *hand-building* an explicit bootstrap fallback inside
   the very handler this ticket exists to fix — the shared-bootstrap route #678 AC #4 forbids.

**No ADR is warranted.** The design decision this records — "the reported id and the reported
values move as one, and that agreement is a property of the `RunConfig` type" — was already
made and recorded by #1609. This ticket executes it. The documentation phase should fold the
handler-side outcome into `docs/knowledge/features/v2-session-manager.md`, which currently
documents the retired table.

## Design

### The handler

`handleRequestSessionSettings` keeps its shape — one reply, always, never an error frame —
and replaces its middle. Order stays load-bearing:

1. **Capability gate.** Unchanged: `if !s.interactive { return }`. Still first, still fully
   inert. AC #4's counter now watches `RunConfigFor` instead of `KnownConversation`.
2. **Decode, tolerated.** Unchanged: `_ = json.Unmarshal(env.Payload, &p)`. A bare frame
   leaves `p.ConversationID == ""`. The error is never echoed and never logged. **The inline
   comment here currently instructs the reader NOT to add an emptiness guard, on the grounds
   that a bare frame is answered as always. That instruction is retired with the design it
   protects and must be rewritten** — a bare frame is now the unaddressable case. What
   survives from it: no malformed-payload branch, no error check on the `Unmarshal` return,
   and the error still never reaches a log or the wire.
3. **Resolve, or don't.** One conversation-keyed read replaces the `addressable` boolean and
   the three seam reads:

   ```go
   var cfg RunConfig
   if p.ConversationID != "" && m.cfg.RunConfigFor != nil {
       if got, ok := m.cfg.RunConfigFor(p.ConversationID); ok {
           cfg = got
       }
   }
   ```

   Three properties, each deliberate:

   - **The empty-id guard stays, and it now means the opposite of what it meant.** Before, it
     short-circuited *to* the bootstrap answer; now it short-circuits *to* the zero answer.
     Keep it rather than letting the seam refuse `""`, for two reasons. It makes "an empty
     `conversation_id` addresses nothing" a property of `internal/relay` alone, provable in
     that package against any `RunConfigFor` double — without it the property would depend on
     what the `cmd/pyry` producer happens to do. And it is the relay-side half of the #678
     `Pool.Lookup("") == bootstrap` hazard the producer already guards; that hazard is
     observed and documented, not speculative.
   - **The nil-seam guard stays.** Foreground / v1 wire no `RunConfigFor`. Nil ⇒ nothing
     resolves ⇒ the zero reply, matching `handleRequestSnapshot`'s nil-seam fail-closed
     posture.
   - **The comma-ok is honoured, not discarded.** `cfg` is assigned only on `ok == true`.
     Writing `cfg, _ = m.cfg.RunConfigFor(...)` would happen to work today because the
     producer zeroes its refusal return, but `RunConfigFor`'s doc says a caller MUST NOT read
     the fields on `ok == false`. Honouring it makes the relay fail closed on its own rather
     than on the producer's good behaviour. § Testing pins this with a poisoned refusal.
4. **Marshal + reply.** Unchanged, sourcing all six fields from `cfg`. The defensive
   marshal-error branch and both log calls stay exactly as they are — **no new log call on any
   reject branch.** This verb fires on every sheet open; adding a per-reject line would log
   more than the write path, and the only thing it could add is the caller's untrusted
   conversation id.

`KnownConversation`, `SnapshotSettings` and `SnapshotUsage` are no longer referenced by this
handler. All three stay on `V2SessionConfig` — `handleRequestSnapshot` reads all three.

### The seam retirement

`V2SessionConfig.BootstrapSessionID` and its doc block are deleted. The full reference set,
verified against `main`:

| Site | Action |
|---|---|
| `V2SessionConfig.BootstrapSessionID` field + doc block (`internal/relay/v2session_seams.go`) | delete both |
| the read in `handleRequestSessionSettings` | replaced by step 3 above |
| the SCOPE paragraph's `(see BootstrapSessionID's seam doc)` in `handleRequestSessionSettings`'s doc | rewritten with the paragraph |
| the cross-reference in `RunConfigFor`'s doc (the routing-key-not-a-secret argument) | restate the argument inline; `RunConfigFor`'s doc must stand alone |
| `RunConfigFor`'s doc "Nothing consults it yet… #1610 makes handleRequestSessionSettings read it" | present tense: this handler is its reader |
| `KnownConversation`'s doc (its `request_session_settings` sentences) | this verb no longer consults it; `handleRequestSnapshot` is the sole reader |
| the `BootstrapSessionID:` wiring + its comment in `startRelayV2` (`cmd/pyry/relay.go`) | delete both |
| the `#1609` forward reference in `startRelayV2`'s `RunConfigFor:` comment | present tense |
| the `BootstrapSessionID:` wiring in `readManagerFor` (`internal/relay/v2session_settings_read_test.go`) | delete |

`cmd/pyry`'s `relayWiring.bootstrapIDFn` and its `main.go` initialiser **stay** —
`bootstrapSnapshotUsage` still consumes them. Deleting them breaks the `screen_snapshot`
usage seam.

### Data flow after the change

```
phone ──request_session_settings{conversation_id}──> handleRequestSessionSettings
                                                       │ interactive? no ─> inert
                                                       │ id == ""      ─> zero reply
                                                       │ seam nil      ─> zero reply
                                                       v
                                          RunConfigFor(conversationID)
                                                       │            (cmd/pyry: runConfigFor)
                                                       │  resolveBoundRunSettings
                                                       │    registry.Get(convID) ─ miss ─> false
                                                       │    conv.CurrentSessionID=="" ─> false
                                                       │    pool.SettingsFor(id)  ─ err ─> false
                                                       │  snapshotUsageFor(dir)(id)   [only on ok]
                                                       v
                                          (RunConfig, true) ─> populated reply
                                          (zero,      false) ─> zero reply
```

The client puts the returned `session_id` on its `set_session_settings`; that write path is
already per-session and is untouched.

### Concurrency

Nothing changes. `handleRequestSessionSettings` runs on the manager's single `Run` dispatch
goroutine, so the `s.interactive` and `m.cfg` reads stay lock-free under the package's
single-owner invariant. The seam call replaces three synchronous closure calls with one; the
producer takes the conversations-registry mutex and one `Pool.SettingsFor` RLock, both
already taken by the seams it replaces. Net lock acquisitions go down, not up. No goroutine is
spawned, none is retired.

The one property worth naming: `resolveBoundRunSettings` reads the bound id and that session's
settings under **one** `SettingsFor` acquisition, so no reported field can describe a session
another field does not — even against a concurrent idle eviction. That is precisely the
agreement the three retired seams could only assert in prose.

### Error handling

Unchanged, and deliberately thin. This verb has no error frame:

| Condition | Reply |
|---|---|
| non-interactive conn | none — inert |
| payload absent, undecodable, or `conversation_id` empty | zero-valued `session_settings` |
| `RunConfigFor` nil (foreground / v1 / unwired) | zero-valued `session_settings` |
| `RunConfigFor` returns `ok == false` | zero-valued `session_settings` |
| `RunConfigFor` returns `ok == true` | that conversation's run configuration |
| `json.Marshal` of the reply fails (defensive, unreachable) | `TypeError` / `server.binary_offline`, message `msgSettingsUnavailable` |

`session_id: ""` is already the wire contract's "the daemon has no session to address"; the
zero rows are a real answer, not an error dressed as one. The final row is the pre-existing
defensive branch and keeps its fixed-constant message.

## Testing strategy

### Unit — `internal/relay/v2session_settings_read_test.go`

**Fixture layer.** Two changes carry the whole file:

- `readSeams` swaps its `sessionID func() string` field for
  `runConfig func(string) (RunConfig, bool)`. It **keeps** `settings`, `usage` and
  `knownConv`: `knownConv` is still needed by `_AnswersWithoutASnapshotter`'s
  `request_snapshot` half, and `settings` / `usage` are now kept **as leak detectors** (below).
- The `settings` / `usage` fixture constants must be given values **distinct from** the
  `runConfig` fixture's, so a re-introduced bootstrap read is visible in the payload as well
  as in a counter. Today they share `readModel` / `readEffort` / `readSessionID`; split them
  into a conversation-keyed set and a bootstrap set.

`readCounts` gains a `runConfigCalls` counter and its `runConfig()` method is re-pointed. Both
of `_ConversationGate`'s counter columns are re-pointed with it, because both go vacuous
otherwise — `wantLookups` counts `KnownConversation`, which this handler stops calling, and
`wantRunConfig` totals the three retired seams:

- `resolves` — calls to `RunConfigFor`. Replaces `wantLookups`. This is the column AC #4
  names.
- `bootstrapReads` — `settings` + `usage` calls. Replaces `wantRunConfig`. Wanted **0 on every
  row**; it is red under exactly one mutation class, a re-introduced bootstrap-scoped read,
  and must not be leaned on for any other property.

**`countingReadSeams`'s refusal must be poisoned.** Its `runConfig` double returns the fixture
`RunConfig` with `true` for the known id, and for every other id returns `(nonZeroRunConfig,
false)` — a distinct marker session id and non-default model/effort/YOLO, **not** the zero
value. Without this, a handler that discarded the comma-ok would still pass every row, because
the production producer happens to zero its refusal return. The poison is what makes
"fail closed on `ok == false`" a tested property of `internal/relay` rather than a property
borrowed from `cmd/pyry`. Keep the marker id an ordinary string — it is a fixture, not a
second path-probe.

**Per-function changes:**

- `_ReportsRunConfig` — the interactive row's frame gains `{"conversation_id": <known>}` and
  expects the conversation-keyed fixture values. The "non-interactive is inert" row is
  unchanged.
- `_AnswersWithoutASnapshotter` — its `request_session_settings` frame gains the known
  `conversation_id`; `knownConv` stays wired true so the `request_snapshot` half still reaches
  its `server.binary_offline` arm. Assertions re-point to the conversation-keyed fixture. The
  desktop#491 contrast this test states is unchanged and must stay stated.
- `_NilSeamsDegradeToZero` — must name a **non-empty** `conversation_id` or it now
  short-circuits at the empty-id guard and never reaches the nil seam it exists to test. (The
  ticket says "known, bound"; with every seam nil there is no registry for it to be known to —
  non-empty is the operative property.) Assertions unchanged: exactly one reply, type
  `session_settings`, zero payload, never an error, never a drop.
- `_IgnoresAnyPayload` — the payload becomes the known `conversation_id` **plus** the existing
  `"session_id":"../../etc/passwd"` and `"not_a_field":true` probes. Its point survives intact
  and is worth keeping: the path-shaped value must still never reach the reply, and unknown
  keys stay ignored. Its doc comment claims the probe "takes the answered-as-today path" and
  needs the same correction as the handler's.
- `_ConversationGate` — five rows, both counter columns re-pointed:

  | Row | payload | `resolves` | `bootstrapReads` | reply |
  |---|---|---|---|---|
  | known, bound conversation | `{"conversation_id":<known>}` | 1 | 0 | that conversation's values |
  | conversation this daemon does not host | `{"conversation_id":"conv-not-hosted"}` | 1 | 0 | zero payload |
  | named id, `RunConfigFor` nil | `{"conversation_id":<known>}` | 0 | 0 | zero payload |
  | empty `conversation_id` | `{"conversation_id":""}` | 0 | 0 | zero payload |
  | no payload at all | `nil` | 0 | 0 | zero payload |

  Every row still asserts exactly one reply of type `session_settings` correlated on
  `in_reply_to` — never a `TypeError`. The bespoke failure message on the lookups column
  ("an unnamed conversation must short-circuit ahead of the registry") stops describing
  anything and must be rewritten for what `resolves` now measures.

  Mutation coverage this table buys — state it in the test's doc comment so a later reader
  does not prune a row as redundant: dropping the empty-id guard reddens rows 4–5 on
  `resolves`; dropping the nil guard panics row 3; discarding the comma-ok reddens row 2 on
  the payload (via the poison); re-adding a bootstrap read reddens rows 2–5 on both
  `bootstrapReads` and the payload.
- `_NonInteractiveMakesNoLookup` — **the counter trap.** Its discriminating assertion is
  currently `counts.lookups == 0`, which counts `KnownConversation`; once the handler stops
  calling it that reads zero unconditionally and the test would pass with the capability gate
  moved *below* resolution — the exact reordering its own doc says "only the counter catches".
  Re-point it to `runConfigCalls == 0` (AC #4's requirement: a counter on the seam the handler
  actually consults), keep the `bootstrapReads == 0` assertion, keep the no-reply assertion.
  **Keep the function name** so no existing doc reference goes stale; correct the doc comment
  to name `RunConfigFor`.

### e2e — `internal/e2e/relay_v2_stream_run_config_test.go`

`TestRelayV2_StreamRequestSessionSettings` is the desktop#491 / #1214 proof against the real
production wiring. **Preserve it by seeding a conversation, do not delete it.** After the
existing pair → daemon → dial → interactive handshake prologue:

- Mint the target with `createConversationViaPhone` — it drains past interleaved broadcasts to
  the correlated `conversation_created` and returns only after the daemon has minted, bound
  and eagerly persisted the dedicated session. That eager persist is why the conversation is
  addressable with no turn driven, which is the desktop#491 case.
- Send `request_session_settings` carrying `{"conversation_id": <minted id>}` and drain to the
  correlated reply with the existing loop.
- Assertions, preserved and one of them **inverted**:
  - not `TypeError` — unchanged, and the message about not gating the run configuration on a
    terminal screen stays.
  - type is `session_settings` — unchanged.
  - `session_id != ""` — unchanged.
  - `session_id != initialUUID` — **inverted** from the current `== initialUUID`. A seeded
    conversation's bound session is a minted session, not the bootstrap, so this is a strictly
    stronger AC #1 proof: the reply describes the conversation the client named. Assert both
    halves (non-empty **and** not the bootstrap id) — non-empty alone would pass on a
    bootstrap leak.
  - `window_tokens != 0` and `used_tokens == 0` — unchanged and still correct: a minted session
    with no transcript collapses to `contextwindow.Read("")`, the fresh-session report.
- Then, on the same open conn, send a **bare** `request_session_settings` and assert the reply
  is `session_settings` with the all-zero payload. This is AC #2's fail-closed direction
  proved against the real `RunConfigFor` producer, which is the wiring where "unresolvable
  addresses nothing" actually matters. Keep it to one extra request and one payload
  comparison.

The ~35-line doc comment argues at length that no conversation is seeded and that "an absent
`conversation_id` cannot be failed closed". It inverts with the test. What must survive: why
this test exists at all (screen_snapshot's side-load is refused on the stream runner), and why
each numeric assertion is what it is.

**No live-claude work.** The `e2e_realclaude` suite does not exercise this verb — its only
`handleRequestSessionSettings` mentions are two comments about the unrelated #833 in-band-model
posture, and its `SessionSettingsPayload` mention is the *write* verb's
`SetSessionSettingsPayload`. No file under `internal/e2e/realclaude` needs an edit.

### Gate

`make check` covers all of it: `internal/relay`'s unit tests and the fake-daemon
`internal/e2e` suite are both in it. `internal/e2e` is behind the `e2e` build tag — run it via
the Makefile target's flags, not a bare `go test ./internal/e2e/`, or zero tests execute and
the exit code lies.

## Protocol doc — `docs/protocol-mobile.md`

Six claim sites plus one new entry. Every one asserts the retired design; a diff that leaves
any of them is AC #5 unmet.

1. The `request_session_settings` row in the envelope-type table — drop "an absent or empty
   `conversation_id` is answered as it always was"; it now names no session and is answered
   with the zero reply.
2. § `request_session_settings`, the `conversation_id` field row — "Empty or absent = no
   conversation named" is still true; what changes is the consequence.
3. § `request_session_settings`, the three answering bullets — the first two are unchanged in
   substance (hosted → its configuration; not hosted → all-zero, never an error). **The third
   inverts**: absent/empty is now the "nothing to address" answer. The fresh-daemon
   justification it carries must be replaced, not merely softened — the accurate replacement
   is that a client with no conversation has nothing it could be configuring (`send_message`
   already refuses an unknown conversation with `conversation.not_found` and an unbound one
   with a retryable `server.binary_offline`, and never falls through to the bootstrap), while
   desktop#491's actual case stays fixed because `create_conversation` mints and binds a
   dedicated session before it replies.
4. § `request_session_settings`, "**The field currently gates whether the answer is populated,
   not which session it describes**" — this is now false in both halves. It selects which
   session is described.
5. § `session_settings`, the Scope paragraph — "the values describe the **bootstrap** session"
   and "Keying the whole set by conversation is a deferred follow-up" are both retired. The
   `session_id: ""` field-row semantics are unchanged and stay.
6. The #1586 changelog entry, whose closing sentence says the reported values "stay
   bootstrap-scoped" and points at #1587. Per repo convention this change adds its own dated
   entry rather than rewriting history — but #1586's entry must stop reading as current
   design. Mark it superseded by this ticket in place; do not restate its content.
7. Add a dated changelog entry for this ticket. **There are two `2026-08-19` entries** — the
   `model_announced` / #1616 one is unrelated; do not touch it.

The client-companion note belongs in the new entry: an un-updated client that sends a bare
frame now gets an inert sheet until it puts `conversation_id` on the payload. That is the
accepted trade in this direction — a visibly inert sheet beats one that silently writes the
user's model / effort / **bypass-permissions** choice into a background session. It is not
lock-step: the field has existed since #1586 and a client that starts sending it today is
answered identically.

## Sizing

Re-checked against this written spec, per the size-S boundary:

| Limit | Boundary | This spec |
|---|---|---|
| Production source files created/modified | ≤ 3 | **3** — `internal/relay/v2session_settings.go`, `internal/relay/v2session_seams.go`, `cmd/pyry/relay.go` |
| Total written work | ≤ 400 | ~355: ~55 production, ~165 unit test, ~100 e2e, ~35 doc |
| New exported types or interfaces | ≤ 5 | **0** |
| Consumer call sites needing simultaneous update | ≤ 10 | **3** `BootstrapSessionID` sites (one wiring, one read, one test wiring) |
| Acceptance criteria | ≤ 5 | **5** |
| Distinct error/reject branches | ≤ 10 | **5** (see § Error handling) |

Not refactor-shaped: `codegraph_impact` on `BootstrapSessionID` plus a repo-wide symbol sweep
found three code sites and no cascade. The binding constraint is the test rework — six unit
functions plus one e2e function — not the production change, which is small. The e2e estimate
is the least certain number here; if it runs long, the bare-frame second request is the one
piece that can be dropped without leaving an AC unproved (`_ConversationGate`'s last two rows
cover the same behaviour at unit level).

## Open questions

- **`docs/protocol-mobile.md` § `set_session_settings`** tells a client it learns its
  `session_id` from `session_settings`, "the request/response route it can drive at any time".
  That stays true but no longer complete — the id it learns is now the named conversation's.
  Left alone deliberately: it makes no scoping claim, and the section it links to carries the
  conversation-keyed contract. Flagging it in case review disagrees.
- **Two client tickets are a scheduling prerequisite, outside this repo's pipeline.**
  `pyrycode-desktop` and `pyrycode-mobile` both send `request_session_settings` and neither
  sends `conversation_id`; neither repo had a ticket for it as of 2026-08-19. The change is a
  one-field payload addition and can ship before this lands.
- **`docs/knowledge/features/v2-session-manager.md` and `protocol-package.md` both reference
  the deleted seam** and document the retired handler table. The documentation phase owns
  both; naming them here so that phase does not have to rediscover them.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. The boundary is explicit and singular:
  `conversation_id` enters at `handleRequestSessionSettings`'s tolerated `json.Unmarshal` and
  crosses to trusted only through `RunConfigFor`, whose producer (`resolveBoundRunSettings`)
  resolves it against the daemon's **own** registry and confirms the pool still holds the
  bound id before reporting anything. Nothing downstream of the seam ever holds the caller's
  string. The design deliberately does **not** re-derive a second refusal in the relay: it
  inherits the producer's, which is stricter (unknown conversation, empty `CurrentSessionID`,
  and pool miss all refuse before any read). The one relay-side guard added on top — the
  non-empty check — is defense in depth for the observed `Pool.Lookup("") == bootstrap` hazard
  (#678), not a competing refusal.
- **[Trust boundaries — fail-closed on refusal]** Addressed in the design, and this was the
  one place the first draft could have been weak. `cfg` is assigned **only** on `ok == true`.
  A `cfg, _ = RunConfigFor(...)` shape would be correct only because the current producer
  zeroes its refusal return — a property of `cmd/pyry`, not of the contract. § Testing pins
  the honoured comma-ok with a **poisoned refusal** (the counting double returns a non-zero
  `RunConfig` alongside `false`), so a regression to the discarding shape reddens
  `_ConversationGate`'s unhosted row. Without the poison the property would be untested and
  the category would be a SHOULD FIX.
- **[Privilege / authz]** No findings, and this is the ticket's whole point. The controls
  reached through the returned `session_id` include YOLO (`--dangerously-skip-permissions`).
  Before this change a client reading the sheet for conversation A was handed the bootstrap's
  id and wrote A's bypass choice into a shared background session. After it, an unresolvable
  request addresses **nothing** — never the bootstrap, never a session the caller named. The
  capability gate is unchanged and stays first: a non-interactive conn is inert before the
  decode, so it cannot learn whether a conversation or a session exists. AC #4's counter moves
  to `RunConfigFor` precisely because the old counter (`KnownConversation`) would read zero
  unconditionally after this change and would silently accept the gate being moved below
  resolution.
- **[Existence oracle]** No findings. Both unresolvable outcomes — unknown conversation and
  known-but-unbound — produce the **identical** zero reply, so the reply distinguishes neither
  from the other nor from an unwired daemon. This is a strict improvement: `KnownConversation`
  was a pure registry-membership check, so a known-but-unbound conversation read as addressable
  through it, which both leaked membership and (AC #2) would have reported bootstrap values.
  The refusal now lives behind one seam with one observable outcome.
- **[Error messages, logs, telemetry]** No findings. `conversation_id` reaches no log line, no
  error string, no filesystem path and not the reply — it is a lookup key and nothing else.
  The spec adds **no** per-reject log call (§ Design step 4), which is the discipline that
  keeps it that way: the only thing such a line could add is the caller's untrusted string.
  The `json.Unmarshal` error stays discarded, never echoed, because `encoding/json` quotes
  attacker bytes into its error text. The two surviving log calls carry `conn_id` only.
  `resolveBoundRunSettings` discards `SettingsFor`'s error for the same reason and must keep
  doing so.
- **[File operations]** No findings — no new path is constructed here. The only filesystem
  touch on this route is `snapshotUsageFor`'s reader, reached **only after** `resolve` says yes
  and **only** with the session id the daemon's own registry returned, never with the caller's
  conversation id and never with `""`. `transcript.StatByID` validates the stem before any path
  join. That ordering is #1609's property, inherited unchanged; the developer must not
  restructure `runConfigFor` to read usage before resolution.
- **[Input validation / size limits]** No findings. `conversation_id` is length-unbounded here
  by design, and that is safe because it is never joined into a path, never logged, and reaches
  only an in-memory registry lookup — a mutex-guarded linear scan over the daemon's own
  conversations, bounded by daemon-owned data, not by the input. The envelope itself is already
  size-capped upstream by the transport. `_IgnoresAnyPayload`'s `"../../etc/passwd"` probe is
  retained to pin that a non-`conversation_id` field can never select another session's data.
- **[Concurrency]** No findings. The handler stays on the single `Run` dispatch goroutine; no
  goroutine is spawned or retired; lock acquisitions decrease (three seam calls become one).
  The TOCTOU that *would* matter — the reported id naming a session the reported settings no
  longer describe, against a live idle eviction — is structurally excluded by
  `resolveBoundRunSettings`'s single `SettingsFor` acquisition. The developer must not
  "simplify" that into a `Lookup`-then-read pair; its doc says why.
- **[Cryptographic primitives]** Not applicable — no randomness, no key material, no
  comparison against a secret on this route. The reply crosses the existing AEAD-sealed push
  path via `forwardEnvelope`, unchanged.
- **[Subprocess execution]** Not applicable — nothing on this route reaches `exec.Command`.
  The values *reported* here can reach a claude argv, but only through the **write** verb
  (`handleSetSessionSettings`), whose `validModel` / `validEffort` shape checks are the
  argv-injection defense and are untouched by this ticket.
- **[Threat model alignment]** Addresses the wrong-session-configured hazard `#678 AC#4` names
  (no route may reach the shared bootstrap session on a client's behalf) at the one remaining
  site that violated it. Out of scope and named as such: `handleRequestSnapshot`'s
  `SnapshotSettings` / `SnapshotUsage` side-load is still bootstrap-scoped. That path is
  unreachable in production — `Snapshotter` is hardcoded `nil`, so the handler short-circuits
  to `server.binary_offline` before reaching them — and re-keying it is deliberately **not**
  this ticket. Do not widen scope to fix, re-key, or delete it.
- **[Downgrade / rollback]** SHOULD FIX, accepted and recorded rather than gated. An
  un-updated client sending a bare frame now receives an inert sheet. That is the fail-closed
  direction and the deliberate trade: an inert sheet beats one that silently writes a
  bypass-permissions choice into a session the user is not looking at. It is not lock-step —
  the wire field has existed since #1586 and a client that starts sending it today is answered
  identically — but the two client tickets in § Open questions are a scheduling prerequisite.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-20
