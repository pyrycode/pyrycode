# #2124 — Answer a conversation's model list from the daemon-wide source when it has no session

`security-sensitive`. Split from #2084. Blocks #2085, #2125 and pyrycode-desktop#1054.

## Files read

- `cmd/pyry/session_model_list.go` → `resolveBoundModelList`, `retainedModelLists` — the two symbols this ticket changes; both doc blocks carry arguments this ticket inverts.
- `cmd/pyry/session_model_list_test.go` → `newModelListTestPool`, `modelListPlan.arm`, `sentinelModelList`, `assertPayloadCarries`, `indexByConversation` — the whole fixture surface, already sufficient; no new scaffolding needed.
- `internal/sessions/pool.go` → `Pool.Default` (returns `p.sessions[p.bootstrap]`, **nil on a map miss**), `Pool.DefaultSettings` (the comma-ok precedent for "no bootstrap exists to read from"), `Pool.Lookup` (`""` resolves to the bootstrap — the #678 hazard).
- `internal/sessions/session.go` → `Session.Runner` — `func (s *Session) Runner() Runner { return s.sup }`, a **pointer receiver that dereferences**, so a nil `*Session` panics rather than failing an assertion cleanly. This is why the fallback needs an explicit nil check and the twin resolvers do not.
- `cmd/pyry/session_model_window_lookup.go` → `sessionModelWindows` — the same type-assertion-off-`Session.Runner` read one hop up; the shape the new session-level helper mirrors.
- `cmd/pyry/session_slash_command_list.go` → `resolveBoundSlashCommandList`; `cmd/pyry/session_background_task_list.go` → `resolveBoundBackgroundTaskRoster` — the two twins whose doc blocks call this function's refusal "inherited verbatim"; they keep refusing, and their cross-references need one sentence each so they do not read as describing a function that no longer behaves that way.
- `internal/relay/v2session_modelreconcile.go` → `reconcileModelLists` — the sole consumer of the enumeration; unchanged, but its "nothing retained ⇒ nothing sent" early return is what AC 4 rides on.
- `internal/streamsup/parser.go` → the `initialize` reply rungs (`emitModelList`'s false-negative asymmetry argument) — one clause there asserts `resolveBoundModelList` refuses when a session's hold is empty, which this ticket makes conditional.
- `internal/e2e/relay_v2_stream_model_list_reconcile_test.go` → the AC-1 assertion's comment, which reasons from "the bootstrap session contributes nothing (it has no conversation record)".
- `docs/protocol-mobile.md` → § `model_list`, its § Message types row, § Reconnect / Backfill semantics' Mode B bullets and the five-vs-six paragraph; § `slash_command_list`'s **Reconcile on (re)connect** and **What the snapshot does not cover** notes are the shape to mirror.
- `docs/knowledge/features/v2-session-manager-state-machine-connect-time-model-list-reconcile-retain.md` — #1863/#1867's design record, including the measured mutation lesson that the contributing row must be created LAST for the `break`-vs-`continue` mutant to be reachable. That lesson is what this ticket has to replace rather than delete (see § Testing strategy).

## Context

`model_list` is the only wire form of the model/effort vocabulary. It is built from claude's `initialize` control reply, one exchange per child spawn, and retained on `sessionModelHold` for the session's life. `resolveBoundModelList` walks conversation → bound session → that session's retained list and refuses through five arms, so a conversation whose claude has not yet answered `initialize` contributes nothing to `retainedModelLists` and gets no connect-time reconcile frame. A client cannot offer a model or effort menu until claude is already running and has answered.

The vocabulary does not vary by conversation — it varies by machine and account. The committed capture `internal/e2e/realclaude/testdata/initialize_control_v2.1.239.json` was taken with an `argv` carrying `--model claude-haiku-4-5` and still reports six models with `default → claude-sonnet-5`. The `commands` array in that same reply is workspace-scoped; models are not.

The daemon already holds a warm daemon-wide copy: `RequestInitializeOnSpawn` is true for every child this daemon spawns, so the bootstrap child asks at daemon start and its runner's hold retains the reply for the process lifetime. `Pool.Default()` reaches that session.

**This design does not deserve an ADR.** It is one fallback inside one existing resolver, and the decision it records — *vocabulary is machine-scoped, so reading another session's copy is allowed where reading its turn state is not* — belongs in `resolveBoundModelList`'s own doc block, where the rule it qualifies already lives. The documentation phase should fold the lesson into `docs/knowledge/features/v2-session-manager-state-machine-connect-time-model-list-reconcile-retain.md` beside the #1867 mutation notes it already carries.

## Design

### The one behavioural change

`resolveBoundModelList` keeps exactly one hard refusal — the registry lookup — and falls back to the daemon-wide vocabulary on every arm below it.

| Arm | Before | After |
|---|---|---|
| `convReg.Get` misses (empty id included) | refuse | **refuse** — unchanged, and the whole security boundary |
| `conv.CurrentSessionID == ""` | refuse | fall back |
| `pool.Lookup` fails | refuse | fall back |
| runner has no `ModelList` method | refuse | fall back |
| the hold holds nothing | refuse | fall back |
| `MapEvent` miss / payload assertion | refuse | **refuse** — unchanged (a failure to map a list already in hand, not an absence of one) |

Fallback source is `Pool.Default()`'s retained list. When that is also absent — no bootstrap in the map, or its `initialize` reply not yet arrived — the resolver refuses, and `reconcileModelLists`' `len(retained) == 0` early return means no frame is sent. Absence of the frame stays the only "no list" signal.

### Structure

Two unexported helpers beside the resolver in the same file, so the resolver body stays a readable four-step composition and the nil-safety lives in exactly one place:

- `sessionRetainedModelList(sess *sessions.Session) (turnevent.ModelList, bool)` — the type assertion off `Session.Runner` plus the hold's comma-ok, with an explicit `sess == nil` guard in front. Called twice (bound session, bootstrap), which is why it is a function rather than inlined twice.
- `retainedModelVocabulary(pool *sessions.Pool, boundSessionID string) (turnevent.ModelList, bool)` — bound session first, `Pool.Default()` second. Contract: *the daemon-wide copy is read only when the binding yields nothing*, which is AC 2 expressed as control flow rather than as a comment.

`resolveBoundModelList`'s signature, return type, id provenance (`conv.ID`, never the `convID` parameter), `MapEvent` reuse and both trailing type-system refusals are unchanged. `retainedModelLists`' body is unchanged — only its doc block.

The empty-`CurrentSessionID` string check survives inside `retainedModelVocabulary`: it is what keeps `Pool.Lookup("")` from being called at all, so the bootstrap is reached through the *named* `Pool.Default()` accessor rather than through a lookup that silently means "bootstrap". That is the difference between a documented vocabulary exception and the #678 hazard re-opened by accident.

### Doc blocks that must be rewritten, not patched

Three statements in `cmd/pyry/session_model_list.go` become false and are rewritten in the same commit as the code:

1. **`resolveBoundModelList`'s refusal inventory** — "an unknown conversation or an empty `CurrentSessionID` returns (zero, false) BEFORE the pool is touched" and "Unknown conversation, unbound conversation, session gone, runner without the method and nothing reported all answer (zero, false)".
2. **The #678 paragraph.** The guard's rule is about **routing turns**, not vocabulary. The new text states both halves explicitly so the file does not carry two rules that read as contradictory: reading the bootstrap child's *model list* on behalf of a conversation that has none of its own is allowed because the vocabulary is machine- and account-scoped; reading anything else off the bootstrap is not, and `resolveBoundRunner`, `resolveBoundSession`, `resolveBoundRunSettings`, `resolveBoundSlashCommandList`, `resolveBoundBackgroundTaskRoster` and the `cmd/pyry/main.go` twins keep the guard unchanged.
3. **`retainedModelLists`' DOUBLE LOOKUP paragraph.** Its current argument — that forking the guard "would hand an unbound conversation the shared bootstrap child's menu stamped with its own conversation id" — describes what this ticket now does on purpose. The double lookup keeps its justification for a different reason: the fallback decision lives in one function, and reading `CurrentSessionID` off the listed row would fork it. Its next sentence ("the bootstrap contributes nothing") is narrowed rather than deleted: the bootstrap still contributes no payload **of its own** — it has no conversation record, so `Registry.List` never yields it and there is no id to stamp — and what changes is that its retained list is now readable on another conversation's behalf.

Two more sites carry the same claim one package away and get a one-clause qualifier each so the family's cross-references stay honest:

- `resolveBoundSlashCommandList` and `resolveBoundBackgroundTaskRoster` each describe themselves as inheriting this function's refusal verbatim. One sentence each naming the vocabulary fallback as a divergence they deliberately do **not** have.
- `internal/streamsup/parser.go`'s rung-3 false-negative argument asserts that when `sessionModelHold` holds nothing `resolveBoundModelList` refuses. True only when nothing is retained daemon-wide; qualified in place. The argument itself survives — a fallback menu drawn from the same machine and account is still not a *wrong* menu, which is the asymmetry that paragraph rests on.

### Data flow

```
reconcileModelLists (relay, connect-time)
  └─ RetainedModelLists()                     ← retainedModelLists(convReg, pool)
       └─ per registry row: resolveBoundModelList(convReg, pool, id)
            ├─ convReg.Get(id) ──miss──▶ (zero, false)          HARD REFUSAL
            └─ retainedModelVocabulary(pool, conv.CurrentSessionID)
                 ├─ id != "" ∧ Lookup ok ─▶ sessionRetainedModelList(sess) ──ok──▶ list
                 └─ otherwise / no list  ─▶ sessionRetainedModelList(pool.Default())
                                              └─ nil sess ─▶ (zero, false)
            └─ turnbridge.MapEvent(list, TurnContext{ConversationID: string(conv.ID)})
```

## Concurrency model

No goroutine is spawned, joined or leaked; nothing is minted or mutated. The call stays synchronous on the caller's goroutine (the relay manager's `Run` goroutine at handshake time).

Lock count rises from three sequential acquisitions to at most **five**, still strictly sequential and never nested: the registry's, `Pool.Lookup`'s RLock, the bound session's hold mutex, `Pool.Default()`'s RLock, the bootstrap's hold mutex. Adding no nesting is what keeps this off the daemon's lock-order graph, so the body must not be restructured so one read happens inside another's scope — the same rule `sessionModelWindows` states.

The `Lookup` → `Default` window is a second instance of the benign TOCTOU the family already documents. A rotation landing in it yields either the session bound a moment ago or the bootstrap's copy, and both are correct readings, because the vocabulary is a property of the machine and account rather than of one child. No re-read, no retry.

## Error handling

There is no error path. Every failure is the comma-ok, and `Pool.Lookup`'s error stays **discarded rather than wrapped** — `resolveBoundRunSettings`' stated reason: returning it bare is what keeps a hostile or malformed id out of a log line or a wire frame a caller builds from the error.

Failure modes and answers:

| Failure | Answer |
|---|---|
| registry misses the id | `(zero, false)` — never reaches the pool, never reaches the bootstrap |
| bound session gone / unbound / no method / empty hold | daemon-wide vocabulary if one is retained, else `(zero, false)` |
| no bootstrap in the pool (`Pool.Default()` returns nil, e.g. a zero-value `&sessions.Pool{}`) | `(zero, false)` — the explicit nil guard; without it `Session.Runner`'s pointer receiver panics |
| bootstrap constructed evicted (`sessions.Config.BootstrapEvicted`, set by `pyry acp`) | it spawns no claude, so its hold is empty ⇒ `(zero, false)` |
| `MapEvent` miss / payload type assertion | `(zero, false)`, unchanged — a refusal rather than a panic, and no log |

No arm returns `true` with an empty `Models`, and none returns a partially-filled payload. The bool stays the only spelling of "nothing to send"; no empty `models` array ever stands in for "unknown".

## Testing strategy

All in `cmd/pyry/session_model_list_test.go`, over the existing fixtures (`newModelListTestPool`, `plan.arm(pool.BootstrapID(), sentinelModelList(tag))`, `assertPayloadCarries`, `indexByConversation`). No new scaffolding.

**Existing tests that flip and are edited:**

- `TestResolveBoundModelList_RefusesWithoutAList` — re-scoped to the two rows that stay refusing (`unknown conversation`, `empty conversation id`) and renamed for what it now pins: AC 3, the registry miss as the whole security boundary. The bootstrap stays armed and the two leak assertions (`strings.Contains(got.ConversationID, "BOOTSTRAP")`, `len(got.Models) > 0`) stay — they are what make the criterion red-on-mutation rather than decorative, since dropping the hard refusal would hand an untrusted id the bootstrap's menu.
- `TestRetainedModelLists_SkipsEachRefusalAndKeepsGoing` — its premise is gone: with a vocabulary retained, no row reachable from `List` can refuse. **The mutant coverage is replaced, not deleted.** The `break`-instead-of-`continue` mutant is now equivalent (the `continue` is unreachable), so the test becomes a four-row enumeration in which every row contributes — bound-and-armed, unbound, dangling binding, bound-to-a-silent-session — asserting a count of four and one payload per id. That is sole-red for the mutant that still matters, `append` followed by an early exit, and it pins the enumeration-contract change directly.
- `TestRetainedModelLists_NothingToSend` — its `allRefuse` registry stops refusing. Rebuilt as two rows with two pools: `empty registry` (armed bootstrap, no rows — still pins "enumerates the registry, not the pool") and `no vocabulary retained anywhere` (nothing armed, rows in all three fallback states), which is AC 4 at the enumerator.
- `TestRetainedModelLists_LogsNothing` — its expected count moves from 1 to 3 as the two refusing rows start contributing. The log assertions are untouched and still hold; the untrusted-id row's id now appears in a payload but still must not appear in a record.

**Tests that stay green untouched**, and are worth listing because their staying green is the shape check: `TestResolveBoundModelList_UnreportedSessionAnswersNoList` (nothing armed anywhere), `TestResolveBoundModelList_RunnerWithoutTheMethodRefuses` (`newRouterTestPool`'s plain `stubRunner` bootstrap is itself the fallback source and has no method), `TestResolveBoundModelList_ResolvesTheBoundSessionsMenu`, `_IsolatesConversations`, `_LogsNothing`, `TestRetainedModelLists_EnumeratesTheBoundSessionsMenu`, `_ArchivedConversationsContribute`, `_DoesNotCrossConversations`.

**New tests:**

- **AC 1 — identity.** A conversation in each of the three fallback states resolves to a payload whose `Models`, effort levels and `DroppedModels` are **identical** to the payload a conversation bound to the bootstrap gets, differing only in `ConversationID`. Comparing the two resolved payloads against each other (rather than each against the fixture) is what makes "identical to what the same vocabulary produces" the assertion the criterion actually names. The three rows: unbound, binding the pool cannot resolve, and bound to a second pool session with nothing armed — the last needs `runPoolReady` + `pool.Create`, the pattern the enumeration tests already use.
- **AC 2 — the live child wins.** Two sessions armed with *different* sentinel lists (bootstrap `DAEMONWIDE`, a created session `OWN`); a conversation bound to the created session resolves to `OWN`. Sole-red for a fallback that overrides rather than defers, which an ordering mistake produces silently and which no AC-1 row can catch.
- **AC 3 — the boundary holds while a vocabulary is retained.** Covered by the re-scoped table above; the criterion's wording ("no caller-supplied id is ever answered from the bootstrap's menu") is exactly its two leak assertions with the bootstrap armed.
- **AC 4 — nothing retained.** At the resolver: nothing armed anywhere, rows in all three fallback states, each `(zero payload, false)`. Plus the nil-bootstrap row, driven with a zero-value `&sessions.Pool{}` so `Pool.Default()` returns nil — sole-red for dropping the `sess == nil` guard, which reddens as a panic rather than a wrong value.

Not written: an e2e arm. The criteria are all provable hermetically at the resolver and enumerator, and `internal/e2e/relay_v2_stream_model_list_reconcile_test.go` already covers the wire path. Its AC-1 comment gets the one-sentence correction its reasoning needs — the assertion stays correct, but "the bootstrap contributes nothing (it has no conversation record)" now needs the narrowed form. If an e2e arm is ever added, note that two producers emit this variant, so a test that merely waits for a frame passes against a dead seam; it must be pinned against the one-line mutant that unsets the enumeration seam, as #2080 did.

## Documentation

`docs/protocol-mobile.md`, four live-prose sites plus a changelog entry in the file's own style:

1. **§ Message types row for `model_list`** — gains the connect-time-snapshot note its `slash_command_list` neighbour already carries.
2. **§ `model_list`'s own paragraph** — "There is **no connect-time snapshot today**" and "deliberately absent from the Mode B list" are both false and are replaced by a **Reconcile on (re)connect** note in the Mode B neighbours' shape (keyed on `conversation_id`, match-and-replace, snapshot-shaped full state), plus a **what the snapshot covers and does not cover** statement: it covers **every conversation the registry carries** — archived rows included — once **any** vocabulary is retained, and covers **none** when none is. The live-lane frame's `event_id` is distinguished from the reconciled frame's deliberate absence of one (`reconcileModelLists` leaves `EventID` nil, keeping it out of the #647 replay ring and inert to cursor dedup), the way § `slash_command_list` already publishes that distinction. The **"no way to ask for one"** sentence is #2125's to remove and is left standing.
3. **§ Reconnect / Backfill semantics' Mode B bullets** — `model_list` joins both the reconcile-on-connect list and the match-and-replace key list, keyed on `conversation_id`.
4. **The five-vs-six paragraph** — deleted rather than reworded. It exists only to record a gap this ticket closes, and keeping it as a note about a sentence that no longer exists would be a third false statement.

## Open questions

1. **Does the fallback belong behind the empty-`CurrentSessionID` arm only, or behind every arm below the registry?** Resolved in the ticket body before planning: every arm below the registry. Wiring it to the empty arm alone ships no observable change today (`handlers.CreateConversation` binds a session before recording the row, so nothing reaches that arm through the relay), and #2125 answers a client request "whatever its session state" from this same resolver, so a resolver that refuses whenever a binding exists but yields nothing would have to be reworked after shipping.
2. **Do the two twin resolvers and the `parser.go` rung-3 argument need edits?** Yes, one sentence each — recorded in § Design rather than left to Phase B, because they are production files outside the ticket's named scope and the diff should not be the first place the decision appears. They are comment-only and change no behaviour.
3. **Is a `Registry.Get` fast path worth adding while here?** No. The O(rows²) cost `retainedModelLists` documents is unchanged by this ticket and stays microseconds at this daemon's row counts; an id-keyed index inside `internal/conversations` is the fix if one is ever needed, and it is not this ticket's.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings, and the reason is narrowing rather than absence.** `convID` is untrusted network input and the boundary it crosses is one function, `resolveBoundModelList`. This ticket *widens what a resolved id reaches* — a registry hit can now read the bootstrap session's retained state — so the discharge is that the bootstrap `*Session` value never escapes `sessionRetainedModelList`: the helper immediately narrows it through a one-method type assertion to a `turnevent.ModelList`, and nothing else about the bootstrap child (its settings, its runner, its transcript, its session id) is reachable from the call. The id itself still never leaves the function: a lookup key into the daemon's own registry and nothing else, never returned on a refusal, never joined into a path, never wrapped into an error.
- **[Trust boundaries] The hard refusal is the whole boundary, and it depends on a registry invariant worth naming.** An empty or attacker-chosen `convID` is answered only through a resolved registry record, and no conversation carries an empty id because `handlers.CreateConversation` mints every id from `conversations.NewID`. That invariant is now load-bearing in a way it was not before: a hypothetical row with an empty `ID` and no binding would previously have refused at the `CurrentSessionID` guard and now resolves. Not a finding — the registry is the daemon's own, an id is server-minted, and the disclosure would be the same machine-scoped vocabulary — but it is why the two leak assertions in the re-scoped refusal table are kept rather than trimmed, and why the fallback must never be reachable ahead of `Registry.Get`.
- **[Trust boundaries / Concurrency] MUST-FIX-shaped hazard, addressed in the design: `Pool.Lookup("")` returns the bootstrap session with a NIL ERROR.** It is not an error path — `Lookup` special-cases the empty id and returns `p.sessions[p.bootstrap], nil`, which on a pool with no bootstrap is `(nil, nil)`. So the naive shape of this fallback (drop the empty-`CurrentSessionID` guard and let `Lookup` fall through) re-opens #678 *and* hands back a nil `*Session` with no signal, which panics at `Session.Runner`'s pointer receiver (`func (s *Session) Runner() Runner { return s.sup }`). The design keeps the `boundSessionID != ""` check inside `retainedModelVocabulary` so the bootstrap is reached only through the named `Pool.Default()` accessor, and puts an explicit `sess == nil` guard in `sessionRetainedModelList` in front of every `Runner()` call. Both are pinned: the guard by the AC-3 leak assertions, the nil check by the zero-value `&sessions.Pool{}` row, which reddens as a panic rather than as a wrong value.
- **[Subprocess / external command execution] No finding, but the chain is real and is named rather than skipped.** A published `value` is the one field in this family a client is meant to send **back**, and it reaches two sinks on `set_session_settings`: claude's argv (`--model` and the value as separate `execve` elements, no shell) and the live child's turn text as `/model <value>` on one line. This ticket makes more values reachable by more conversations, so the question is whether provenance now matters at that validator. It does not: #845's shape rule is provenance-blind and re-validates every inbound value regardless of which frame published it — the protocol doc states this explicitly ("a published value is still re-validated there rather than trusted"). The fallback vocabulary is drawn from the same claude binary and the same account as the conversation's own child would report, so it introduces no value class the validator has not already been sized against.
- **[Network & I/O] SHOULD FIX — the connect-time frame count changes shape and the doc block must say so.** Connect-time population moves from "conversations whose bound session holds a list" to "every conversation the registry carries, once any vocabulary is retained", and the registry only GROWS (auto-archive sets a flag rather than deleting, and archived rows deliberately contribute). So a handshake that previously pushed few or zero `model_list` frames now pushes one per row. No cap is added: `reconcileModelLists` and `retainedModelLists` both document the count as deliberately uncapped, the shape `outstandingQueues` already ships, and capping here would silently truncate menus while changing wire behaviour nobody asked for. The amplification is bounded by registry size, is reachable only post-handshake and post-token-validation, is unicast to the connection that asked, and each payload is already bounded at construction (`DroppedModels`, `TruncatedFields`). Discharged in Phase B by stating it in `retainedModelLists`' doc block — where the per-row deep copy, not just the O(rows²) `Registry.Get` scan, is now the dominant per-handshake term.
- **[Error messages, logs, telemetry] SHOULD FIX — the fallback creates a new temptation the doc block must close.** Neither new helper takes a logger and neither may grow one; the #833 posture is unchanged. What is new is a plausible-looking "fell back to the daemon-wide vocabulary" debug line, whose only possible content is a conversation id or the model values themselves — exactly the channel #833 closes. Named as forbidden in `resolveBoundModelList`'s doc block in Phase B. `Pool.Lookup`'s error stays discarded rather than wrapped, and both existing log-negative tests stay green unedited.
- **[Concurrency] No findings.** No goroutine is spawned, so none can leak. Lock acquisitions rise from three to at most five and stay strictly sequential and un-nested (registry, `Lookup` RLock, bound hold, `Default` RLock, bootstrap hold), so no edge is added to the daemon's lock-order graph. The new `Lookup` → `Default` window is a second instance of the family's benign TOCTOU: a rotation landing in it yields either the session bound a moment ago or the bootstrap's copy, and both are correct because the vocabulary is a property of the machine and account rather than of one child. Nothing is minted, retired or mutated, so a re-connect re-sends the same snapshot idempotently.
- **[Threat model alignment] The #678 isolation guard is deliberately relaxed for exactly one data class, and the argument is stated in code rather than assumed.** #678 is about **routing turns** — a message must never reach another conversation's child. This ticket reads a *vocabulary* off the bootstrap, which is machine- and account-scoped (the committed `initialize_control_v2.1.239.json` capture reports six models with `default → claude-sonnet-5` under an `argv` carrying `--model claude-haiku-4-5`), carries no conversation state, and is already published to any interactive client the moment its own child answers `initialize`. The relaxation is vocabulary-only: `resolveBoundRunner`, `resolveBoundSession`, `resolveBoundRunSettings`, `resolveBoundSlashCommandList`, `resolveBoundBackgroundTaskRoster` and the `cmd/pyry/main.go` twins keep the guard unchanged, and the two twins' "refusal inherited verbatim" cross-references get a sentence each so the family does not read as having moved together.
- **[Tokens, secrets, credentials] Not applicable by design.** Nothing on this path generates, stores, compares, rotates or revokes a token, and the payload carries no secret — model identifiers, display names and effort level names, all of which already cross this wire on the live interactive lane.
- **[File operations] Not applicable by design.** No path is constructed, opened, created or statted. `convID` never reaches a `filepath.Join`, which is the rule the resolver's existing SECURITY block states and which this change leaves intact.
- **[Cryptographic primitives] Not applicable by design.** No randomness, no key, no comparison against a secret. Nothing minted means nothing to mint securely.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-05
