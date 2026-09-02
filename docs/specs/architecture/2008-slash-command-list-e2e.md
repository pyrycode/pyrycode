# #2008 — answer `initialize` with a `commands` array and prove the list reaches a connected client

## Files read

- `internal/e2e/relay_v2_stream_model_list_test.go` → `driveModelListRespawn`,
  `TestRelayV2_StreamModelListReachesConnectedPhone`, `modelListObservation` — the twin
  (#1845). Its header carries THE SEQUENCING PROBLEM and the kill/respawn route this
  slice reuses wholesale; its collection shape (first frame after the kill, count the
  pre-kill ones, milestones asserted fatally first) transfers unchanged.
- `internal/e2e/internal/fakeclaude/main.go` → `writeInitializeAck`, `initializeModels`,
  `writeJSONLine` — the answer this slice widens. The double nesting is
  `response.response`; `commands` sits beside `models` in the inner object.
  `initializeModels`' doc carries the map-per-entry / ABSENCE-is-load-bearing rule the
  new array copies, and the dead `#1683 extends the same answer` pointer this slice
  replaces.
- `internal/e2e/internal/fakeclaude/initialize_control_test.go` →
  `TestRunStreamJSON_InitializeControlAnswer` — its decode target reads
  `Response.Response.Models` and ignores unknown keys, so AC4 holds against the widened
  answer with no edit. This is why the optional capture cross-check is declined below.
- `internal/streamsup/parser.go` → `emitModelList` (its rung 5), `emitSlashCommandList`,
  `commandEntryLine` — the second gate is INDEPENDENT of the models one: a reply
  carrying both arrays emits a `ModelList` and a `SlashCommandList`, ModelList first.
  `emitSlashCommandList` suppresses a zero-length list, which is exactly why the fake's
  present answer produces no frame at all.
- `internal/turnbridge/outbound.go` → `MapEvent`'s `turnevent.SlashCommandList` arm,
  `maxSlashCommandListBytes` — supplies `ConversationID` (the parser's event carries no
  conversation identity) and folds a byte-cut into `DroppedCommands`.
- `cmd/pyry/interactive_turn_v2.go` → `interactiveTurnEmitterV2.Handle`'s
  `turnevent.SlashCommandList` case, `eventKind`'s same case — the live-lane emit
  (#2003). Two facts the test is built on: the bootstrap child's report is dropped
  UNCONDITIONALLY at the no-cursor return, and delivery here is best-effort by
  construction (`turnMarkNone` ⇒ droppable at `droppableCap`).
- `internal/protocol/interactive.go` → `SlashCommandListPayload`, `SlashCommand`, and
  both `MarshalJSON` methods — the wire shape. `SlashCommand.MarshalJSON` normalises a
  nil `Aliases` to `[]`, so a no-alias entry reaches the phone as an empty non-nil
  array, never null.
- `internal/e2e/realclaude/testdata/initialize_control_v2.1.239.json` — the provenance.
  Walked: 51 entries, 9 alias-bearing, no entry publishes an empty `aliases` array, and
  the four entries this slice cans are transcribed from it verbatim.
- `docs/knowledge/features/streamsup-package-producing-turnevent-slashcommandlist.md` —
  the producing side's lessons; confirms the empty-`commands` suppression is a
  precondition of the emitter rather than a guard at either call site.

## Context

Every rung of the slash-command chain has landed and is proved at its own seam against
its own doubles — the mapping (#2001), the frame bound (#2002), the turn-lane emit
(#2003), the retention (#2004), the resolver (#2005), the connect-time reconcile
(#2006) and its enumeration seam (#2007). Nothing proves the CHAIN across process
boundaries, and nothing can: `internal/e2e`'s fakeclaude answers `initialize` with
`models` and no `commands` key, and `emitSlashCommandList` suppresses a zero-length
list, so the whole hermetic e2e tier runs with that arm dark. This slice cans the array
and proves the live-lane half end to end.

The live-lane half only. #2009 — filed, and blocked by this ticket — owns the
late-connecting reconcile half, exactly as the model list has
`relay_v2_stream_model_list_test.go` and `relay_v2_stream_model_list_reconcile_test.go`.

No ADR is warranted: this slice introduces no decision, it transcribes a fixture and
reuses #1845's established observation route.

### Size: the ceiling is exceeded by ~2%, and the ticket is built anyway

Stated rather than rationalised. Re-counting the six size-S numbers against this written
plan: one production source file, no new exported type, no consumer call site, four
acceptance criteria, no reject branch — and roughly 816 lines of total written work
against a 800-line boundary. The overage is one line item and it is not discretionary:
the `## Security review` section below is ~88 lines that the label makes mandatory, and
the nearest analogue was measured without one (#1845 carried a 193-line spec and was not
`security-sensitive`). Net of it this plan sits under the boundary with room.

Three things then point the same way. The depth gate bars a split — `2008 → 1720 →
1683`, verified against the sub-issue graph, not taken from the body. The floor rule
independently forbids the only seam available: cutting the fake's `commands` arm from
the e2e that reads it would produce a child whose sole consumer is its own sibling, and
the floor wins over the ceiling precisely here, because a ceiling miss costs a
continuation leg while a slice nothing else consumes cannot be verified on its own. And
the ticket states the estimate as a contract. `needs-human:sizing` is applied so the
judgement is findable on the board; it is a marker, not a question blocking the build.

## Design

Two files, no production change outside the fake.

### The fake's `commands` arm — `internal/e2e/internal/fakeclaude/main.go`

A new package-level `initializeCommands`, `[]map[string]any`, canned beside
`initializeModels` and threaded into `writeInitializeAck`'s inner `response` object as
one additional key. The map-per-entry choice is `initializeModels`' own and for its
reason: ABSENCE is the load-bearing property of this fake's output, and a map makes it
literal — a struct with `omitempty` would conflate an absent `aliases` with an empty
one, which is the distinction AC2 exists to preserve.

Four entries, transcribed verbatim from the committed capture and kept in the capture's
own relative order:

| # | name | keys | aliases |
|---|---|---|---|
| 0 | `clear` | four | `["reset", "new"]` — two, non-alphabetical |
| 1 | `compact` | three | key OMITTED |
| 2 | `config` | four | `["settings"]` — one |
| 3 | `model` | three | key OMITTED |

The interleaving is deliberate and is what the attribution assertion rests on. Two
alias-bearing entries with DIFFERENT values and DIFFERENT counts, separated by a
no-alias entry: a key-blind flat reading that sprayed every alias onto every row
reddens at indices 1 and 3; a reading that attached the aliases to the wrong owner
reddens at 0 and 2 on both value and count. One alias-bearing entry could not separate
"attached to its owner" from "present somewhere in the payload".

`writeInitializeAck`'s doc block loses its dead `#1683 extends the same answer with
commands when it lands` pointer (#1683 closed `NOT_PLANNED`) and records what is still
omitted: `agents`, `output_style`, `account`, `pid`, `session_state` and the outer
object's two pending-request arrays.

Nothing else in the fake moves. The array is READ-ONLY on the same terms
`initializeModels` states — marshalled from `runStreamJSON`'s single goroutine in
production and from `t.Parallel()` subtests in the package unit test.

### The e2e — `internal/e2e/relay_v2_stream_slash_command_list_test.go`

The twin of `relay_v2_stream_model_list_test.go`, file-named so #2009's reconcile half
sits beside it under the same convention.

- `slashCommandListObservation` — the value `driveModelListRespawn`'s own returns:
  the payload, a found flag, a pre-kill frame count, the two pre-kill milestones
  (`sawEcho`, `sawTurnEnd`) and the two child pids. Milestones are VALUES rather than
  helper-side assertions for `modelListObservation`'s reason: a frame count read off a
  run where nothing completed proves nothing, and a helper that fataled on them would
  leave the caller's checks dead code.
- `driveSlashCommandListRespawn(t) slashCommandListObservation` — pairs, seeds the
  bound conversation, starts a stream-interactive daemon against a fakerelay, dials and
  handshakes an INTERACTIVE v2 client, drives one ordinary turn to stamp the
  active-conversation cursor, SIGKILLs the supervised child, and collects the first
  `slash_command_list` the respawn's `initialize` round trip puts on that still-connected
  client. Fatal on transport and decode faults only; both collection deadlines return
  what they have.
- `TestRelayV2_StreamSlashCommandListReachesConnectedPhone` — the assertions.

Why the kill/respawn and not a rotation: #1845's header settles it and this slice
inherits the answer rather than re-deriving it. The respawn keeps the session id so its
events still match the drain's session-tag gate; after a `new_session` rotation the
runner's sink tag stays on the outgoing id while the conversation rebinds, so every
event the fresh child produces is dropped at that gate.

Why a turn must be driven BEFORE the kill: `activeConversation.set` is called only from
`sessionRouter.Route`'s success path, and until it is, `Handle`'s no-cursor return takes
every event. That same return is why the bootstrap child's own inventory can never
reach the phone by accident.

The two constant families are transcribed literals, as
`relay_v2_stream_unrecognized_test.go`'s needles are and for its reason — the fake is a
separate `main` package, so nothing can be imported from it.

## Concurrency model

None of the test's own. One goroutine drives everything: the control-plane poll
(`waitForRunnerStatus`) and the phone receives never interleave, which is what makes
the deliberate non-reading of the phone across the kill window safe — whatever the
daemon sends meanwhile buffers on the socket.

The receive loop decrypts every `noise_msg` in arrival order and filters AFTER
decrypting: the receive nonce is sequential, so a filter placed before the decrypt
desyncs the `CipherState` and every later decrypt fails with a misleading error.

One deadline per collection window, every receive bounded by what remains of it.
`fakephone.Client.ReceiveBytes` closes the underlying connection on a cancelled read
context, so the first timeout is terminal for the window.

## Error handling

- Transport and decode faults are `t.Fatalf` — an error envelope, a payload that will
  not decode, a receive error that is not the deadline. A `slash_command_list` that
  will not decode IS the defect, not a miss.
- Either collection deadline returns with what it has and logs COUNTS, leaving the
  diagnosis to the caller's assertions.
- The three ways this goes red are hard to tell apart from a bare `want 1, got 0`, so
  each failure message names its own suspect: the pre-kill turn never completing (a
  UUID mismatch between the two seeds), the respawn never happening
  (`waitForRunnerStatus` fatals with the daemon's stderr tail), or the respawn
  happening with no frame arriving — the AC3 failure proper, whose message names the
  emitter arm, the mapper arm, and the upstream droppable refusal in that order.

## Testing strategy

AC1 and AC2 are structural — they are what the fake cans, and AC3's assertions read
them back through the whole chain, so no separate fake-side test is added.

AC3, against `obs.list`:

- `conversation_id` equals the seeded id — the BRIDGE's contribution, since the
  parser's event carries no conversation identity at all.
- `dropped_commands` is 0 and `len(commands)` is 4 — four canned rows against a
  128-entry count cap and a 64000-byte frame bound, so nothing was cut and the list's
  true size is exactly what arrived.
- Each row's `name`, `argument_hint` and `description` match its canned literal, IN
  CLAUDE'S OWN ORDER — `MapEvent` is documented not to sort, so this is an ordered
  comparison by index rather than a set membership test.
- `commands[0].aliases` is exactly `["reset", "new"]` and `commands[2].aliases` exactly
  `["settings"]`, compared as ORDERED slices: the capture's own order is
  non-alphabetical, so order is observable.
- `commands[1].aliases` and `commands[3].aliases` are EMPTY — the "and to no other"
  half of AC3, and the only assertion that separates per-entry attribution from a flat
  key-blind reading.
- Those same two are non-nil, a live claim rather than decoration: `json.Unmarshal`
  yields nil for null and an empty non-nil slice for `[]`, so this pins
  `SlashCommand.MarshalJSON`'s nil→`[]` normalisation through a daemon to a phone where
  the unit tests pin it at the byte level.
- `truncated_fields` is nil on every row — nothing was cut.

AC4: `go test -race ./internal/e2e/... ./internal/e2e/internal/fakeclaude/...` with the
`e2e` tag. The existing suites are not edited; the fake's own
`TestRunStreamJSON_InitializeControlAnswer` decodes into a struct that ignores unknown
keys, so the widened answer is invisible to it. The optional capture cross-check for
`commands` is DECLINED: the binding proof is the e2e, and adding a second walk of the
capture would spend budget on a claim the e2e already carries end to end.

## Open questions

- Does the pre-kill turn's own `initialize` reply put a `slash_command_list` on the
  wire before the kill? It should not — the bootstrap child reports before any
  conversation is routed, so the no-cursor return takes it — but the count is collected
  rather than asserted, exactly as #1845 collects `preKillLists`, because a zero there
  is a claim about the drop's timing rather than about this ticket's behaviour.
  Resolution goes in a `## Revisions` entry if the observed count is non-zero.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No finding, but the posture this test PINS must be stated so a
  later reader does not misread it. A command's name, description, argument hint and
  aliases are WORKSPACE-authored — a lower-trust origin than claude's own strings,
  since a command defined in a repository was written by whoever wrote that repository.
  The production boundary is `commandEntryLine`'s decode plus the per-field caps applied
  at construction in `emitSlashCommandList`, and this slice touches neither. What the
  e2e asserts is that the four canned strings reach the phone VERBATIM, which is the
  documented design — the production path forwards unsanitised and the render boundary
  owing sanitisation is the client's. The test is therefore evidence of faithful
  forwarding and is NOT evidence that anything sanitises; that sentence goes in the
  test's header so the next reader cannot take it for the second claim.
- **[Tokens, secrets, credentials]** No finding. The pairing token and the server static
  pubkey are handled only by the helpers `driveModelListRespawn` already uses
  (`decodePairPayload`, `driveHandshakeToOpenDaemonInteractive`), and neither value is
  printed on any path — the deadline diagnostics print counts and pids only. No token is
  minted, stored or rotated here.
- **[File operations]** No finding. Every path is `shortHome`'s `t.TempDir`-backed home;
  `seedBoundConversation` writes a fixed relative path under it. No filesystem path is
  built from any canned string. Note the near miss deliberately avoided: the capture's
  `__remote-workflow` proves a command name is NOT an identifier and carries no charset
  guarantee, but this fixture cans four plain names, so no path- or charset-shaped value
  enters the test at all.
- **[Subprocess / external command execution]** No finding, and the reason is structural
  rather than incidental. `initializeCommands` is a compile-time constant structure
  marshalled through `writeJSONLine`, so it introduces no path by which inbound bytes
  reach the fake's stdout. `writeInitializeAck`'s existing escaping property — the
  echoed `requestID` is inbound bytes, and `json.Marshal` keeps a newline-carrying id
  from splitting the physical line and fabricating a stream event — is inherited
  unchanged, because the new key rides the same map through the same writer.
- **[Cryptographic primitives]** Not applicable by design: the test performs no crypto of
  its own. The Noise_IK handshake, the sequential receive nonce and the `CipherState`
  are the existing helpers', and the decrypt-before-filter rule that keeps the nonce in
  step is carried in the receive loop.
- **[Network & I/O]** No finding. Both dimensions of the frame are already bounded above
  this test — the entry count by streamsup's `maxSlashCommandListEntries` and the
  serialised size by `maxSlashCommandListBytes` in the mapper — and four canned rows sit
  orders of magnitude under each. The design decision worth naming: `dropped_commands ==
  0` is meaningful only BECAUSE the fixture is far under both caps, so it is a
  nothing-was-cut assertion and not a cap test; a later widening of the fixture toward
  either cap would silently convert it into one.
- **[Error messages, logs, telemetry]** **SHOULD FIX** — assert per field, never by
  dumping the decoded payload. The unconditional deadline `t.Logf`s print counts, pids
  and discriminants only, which satisfies the #833 posture directly. The failure path is
  the subtler half: naming an expected and a got value is safe here because both are the
  fake's own canned literals, already committed in the fake's source, but a whole-payload
  `%+v` dump of the kind `TestRelayV2_StreamModelListReachesConnectedPhone` uses for its
  models would print every command string it received. That is harmless against
  fakeclaude and would NOT be harmless in `internal/e2e/realclaude`, where the same
  payload carries the operator's real workspace. Since #2009 and any live-lane sibling
  will copy this file's shape, Phase B writes per-field got/want messages with no
  payload dump, and says why in the test. The verifier must check this landed.
- **[Concurrency]** **SHOULD FIX** — `initializeCommands` must carry `initializeModels`'
  READ-ONLY discipline explicitly. It is marshalled from `runStreamJSON`'s single
  goroutine in production and from `t.Parallel()` subtests in the fake's package test; a
  future test that appended to it or reassigned it would race, and `-race` catches that
  only when the runs happen to overlap, so the guarantee has to be a stated rule rather
  than an observed green. Phase B states it in the var's doc block. The test's own
  concurrency is a single goroutine, so no lock ordering or shutdown question arises.
- **[Threat model alignment]** No finding. The relevant threat in
  `docs/protocol-mobile.md` § Security model is a hostile or compromised workspace
  injecting content into a client's rendered menu. This slice neither introduces nor
  mitigates it; the mitigation is client-side rendering discipline and is OUT OF SCOPE
  here, owned by the desktop client rather than by this repository. What this slice does
  is make the forwarding observable, which is a precondition for auditing that threat
  rather than an answer to it.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-02

## Revisions

### 2026-09-02 — Phase B

**The Open Question is resolved, and the design did not change.** The RED run observed
`pre-kill slash_command_list=0` with both milestones met, so the pre-kill turn's own
`initialize` reply put nothing on the wire — the bootstrap child reports before any
conversation is routed and `Handle`'s no-cursor return takes it, exactly as predicted.
The count stays COLLECTED rather than asserted, as planned: a zero there is a claim
about that drop's timing rather than about this ticket's behaviour, and pinning it would
make an unrelated timing change present as this test's failure.

**Both `## Security review` SHOULD FIX items landed.** Assertions are per field with no
payload dump — including the decode-failure path, where the model-list twin prints its
raw bytes and this file deliberately does not, since the shape is what
`internal/e2e/realclaude` siblings will copy. And `initializeCommands` carries the
READ-ONLY discipline in its doc block, stated as a rule rather than rested on an
observed green.

**One thing the RED run proved that no assertion states.** The failing run took 32.5s
and the passing one 3.0s, the difference being the full post-kill deadline the RED run
waited out. So the frame arrives promptly rather than marginally, which is the honest
reading of the droppable classification here: `turnMarkNone` makes delivery best-effort
by construction, and a quiet hermetic run is nowhere near `droppableCap`.
