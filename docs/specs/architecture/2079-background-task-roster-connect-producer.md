# #2079 — Resolve and enumerate retained background-task rosters onto the connect-time reconcile seam

**Ticket:** pyrycode/pyrycode#2079 (`size:s`, `security-sensitive`, split from #1626)
**Package:** `cmd/pyry`
**Seam filled:** `internal/relay`'s `V2SessionConfig.RetainedBackgroundTaskRosters`

## Files read

- `internal/relay/v2session_seams.go` → `V2SessionConfig.RetainedBackgroundTaskRosters` — the seam's doc block states this slice's contract in full: enumerate-all (a `V2Session` carries no conversation id), order not a contract, BOUNDED TIME on the manager's Run goroutine, already-bounded payloads only, `nil ⇒ no reconcile`, and the empty-roster-is-a-positive-statement rule that inverts every twin's answer. It names this ticket three times.
- `internal/relay/v2session_rosterreconcile.go` → `reconcileBackgroundTaskRosters` — the consumer. Guard (`!s.interactive || cfg.RetainedBackgroundTaskRosters == nil`), snapshot, one envelope per payload with a nil `EventID`. It owns the type stamp, the batch timestamp and the `Push`; this slice ends at the payload slice and builds no envelope.
- `cmd/pyry/session_slash_command_list.go` → `resolveBoundSlashCommandList`, `retainedSlashCommandLists` — the DIRECT TWIN pair, structure copied wholesale: Get → empty-`CurrentSessionID` guard → `Pool.Lookup` → type assertion → hold read → `turnbridge.MapEvent` → payload assertion, and `List` → per-row resolver → `continue` on the comma-ok.
- `cmd/pyry/session_model_list.go` → `resolveBoundModelList`, `retainedModelLists` — the older instance of the same pair; the wiring precedent both this and the slash twin follow.
- `cmd/pyry/session_background_task_hold.go` → `sessionBackgroundTaskHold.BackgroundTaskRoster`, `cloneBackgroundTaskRoster` — the retention #2077 shipped. Its doc block is where THE BOOL IS LOAD-BEARING is derived: the producer emits an event for an empty tasks array on purpose, so `len(Tasks) == 0` does not mean unreported and only the bool separates the two. The read returns a deep copy the caller solely owns.
- `cmd/pyry/streamsup_runner.go` → `streamRunner.BackgroundTaskRoster` — the sixth concrete method OFF the un-widened `sessions.Runner` interface, reached by type assertion. Its doc names this ticket's resolver as the consumer and restates that the two false-y answers are not the same answer.
- `internal/turnbridge/outbound.go` → `MapEvent`'s `turnevent.BackgroundTaskRoster` arm — forwards a nil `Tasks` and returns true rather than suppressing it, deliberately NOT pre-allocating, leaving the normalisation to the payload type. This is what makes the empty case reachable without forking the mapping.
- `internal/protocol/interactive.go` → `BackgroundTaskRosterPayload`, `BackgroundTaskRosterPayload.MarshalJSON` — value receiver, nil `Tasks` → `"tasks":[]`. `DroppedTasks` is the variant's only truncation report; there is no top-level `truncated_fields`.
- `internal/turnevent/event.go` → `BackgroundTaskRoster`, `BackgroundTask` — `Tasks` is nil for an empty roster and never an empty non-nil slice; every string is claude-authored and bounded at construction; a `local_bash` `Description` is a literal command line.
- `cmd/pyry/relay.go` → `relayWiring.retainedSlashCommandLists` (field) and the `RetainedSlashCommandLists:` assignment in `startRelayV2` — the two edit points, and the stated reason for assigning straight through rather than wrapping.
- `cmd/pyry/main.go` → the `retainedSlashCommandLists: retainedSlashCommandLists(convReg, pool)` line in the `relayWiring` literal — the composition root, where the `internal/sessions` dependency stays because `relay.go` deliberately does not import it.
- `cmd/pyry/session_slash_command_list_test.go` → `slashCommandListPlan`, `newSlashCommandListTestPool`, `sentinelSlashCommandList`, `indexSlashCommandsByConversation` — the test rig this file reproduces rather than shares (this family's stated convention), and the arm-by-id-after-`New` indirection the bootstrap session forces.
- `docs/knowledge/features/v2-session-manager-state-machine-connect-time-background-task-roster-reconcile-retain.md` — #2078's overview. Two lessons change how this is built: the round-trip asymmetry (a nil-`Tasks` payload decodes back as an empty non-nil slice, so a `reflect.DeepEqual` across a JSON hop is false — normalise the WANT side), and the re-derive-don't-pattern-match instruction for the empty case.
- `docs/knowledge/features/v2-session-manager-state-machine-connect-time-slash-command-list-reconcile-retain.md` — #2006/#2007's overview. Sources the double-lookup-is-a-security-control argument, the aliasing question only a fan-out caller can raise, and `Registry.Save` releasing its lock before the fsync as what discharges BOUNDED TIME.
- `CODING-STYLE.md`, `docs/PROJECT-MEMORY.md` — conventions; the refusal-mapping-is-the-consumer's-job rule and the read-only status of the memory doc.

## Context

Two halves have landed and neither is observable alone. #2078 shipped `reconcileBackgroundTaskRosters` behind a seam wired to nothing. #2077 shipped `sessionBackgroundTaskHold`, a per-session retention nothing reads. This slice joins them, and is what makes the capability real: a client opening an interactive session is unicast one `background_task_roster` per conversation whose bound session holds a roster, instead of an empty background-task panel that fills only when claude next CHANGES the roster — which on a quiet long-running session may never happen (#1240's symptom).

One deliverable in two statements, both in `cmd/pyry` and both in one new file: a conversation-keyed resolver, and the enumeration over the conversations registry that the seam calls. The resolver's only consumer is the enumeration beside it. The slash-command family split this same pair across #2005 and #2007 and #2007's commit message records the cost — *"That resolver had no production caller until now."*

No ADR is warranted: every design choice here is inherited from a documented twin, and the one divergence (§ Design, "The empty case") is already stated by `RetainedBackgroundTaskRosters`' own doc block and by #2078's overview entry. The documentation phase should fold this ticket's lessons into the background-task-roster reconcile overview named above.

### Size

The six size-`s` boundaries, re-counted against this written plan:

| Limit | Boundary | This plan |
|---|---|---|
| Production source files created or modified | ≤ 5 | **3** — `session_background_task_list.go` (new), `relay.go`, `main.go` |
| Total written work | ≤ 800 | **~1200** — over |
| New exported types or interfaces | ≤ 5 | **0** |
| Consumer call sites needing simultaneous update | ≤ 10 | **0** — purely additive; no signature changes, no renames |
| Acceptance criteria | ≤ 5 | **5** |
| Distinct error/reject branches | ≤ 10 | **6**, none of which logs |

Only the line ceiling is exceeded, at roughly 1.5×, and **the floor rule resolves it in favour of building rather than splitting**: the only seam available is between the resolver and the enumeration, and the resolver's sole consumer is that enumeration — a child that nothing outside the family calls. Where floor and ceiling disagree the floor wins, so the overage is stated here and the ticket is built as one. The refiner reached the same conclusion independently (`Estimate:` ~1250 lines, 3 production files) and named the two sibling analogues that landed clean in a single builder run: #2077 at 1136 total and #2078 at 1123.

Split depth checked: parent #1626, grandparent none — a split would have been permitted. It is declined on the floor rule, not on the depth cap.

## Design

Both functions live in a new `cmd/pyry/session_background_task_list.go`, unexported, taking no logger.

### `resolveBoundBackgroundTaskRoster(convReg *conversations.Registry, pool *sessions.Pool, convID string) (protocol.BackgroundTaskRosterPayload, bool)`

The SIXTH member of the conversation-keyed resolver family, after `resolveBoundRunner`, `resolveBoundSession`, `resolveBoundRunSettings`, `resolveBoundModelList` and `resolveBoundSlashCommandList`. Structure inherited verbatim from the last of those:

1. `convReg.Get(...)` — refuse on `!ok` **or** on an empty `CurrentSessionID`, BEFORE the pool is touched. That second clause is the #678 isolation enforcement point, not a redundancy: `Pool.Lookup("")` returns the BOOTSTRAP session with a nil error, so an unbound conversation reaching a lookup would be handed the shared bootstrap child's roster stamped with its own conversation id. No third `convID == ""` pre-check — an empty id lands in the first guard.
2. `pool.Lookup(...)` — refuse on error, DISCARDING it rather than wrapping, so a hostile or malformed id is never reflected into anything a caller builds from the error.
3. Type-assert `sess.Runner()` to `interface{ BackgroundTaskRoster() (turnevent.BackgroundTaskRoster, bool) }` — the un-widened-interface rule `streamRunner.BackgroundTaskRoster`'s own doc states. A runner without the method is a REFUSAL, not a panic, and a nil `Runner` fails the assertion cleanly so no nil check is needed.
4. Read the hold through that method — refuse on `!ok`.
5. `turnbridge.MapEvent(roster, turnbridge.TurnContext{ConversationID: string(conv.ID)})`, unforked. `TurnID` and `Seq` stay zero: the roster is conversation-scoped, not turn-scoped, and the arm ignores both.
6. Assert `payload.(protocol.BackgroundTaskRosterPayload)` — the ONLY discriminant; `typ` is discarded, since comparing it to `protocol.TypeBackgroundTaskRoster` is a strictly weaker second spelling of the same check.

The reported id comes from the RESOLVED RECORD (`conv.ID`), never reflected from the `convID` parameter, so provenance stays correct if `Registry.Get` ever loosens.

Arms 5 and 6 are the type system's rather than states this daemon can reach — `MapEvent`'s roster arm has no suppression branch and returns true by construction — and are answered with a refusal rather than a panic or a log. No fixture reaches them, deliberately.

**The empty case, and it is the OPPOSITE of both twins'.** There is deliberately **no** `len(Tasks) == 0` check at any step. `emitBackgroundTaskRoster` emits an event for an absent or empty tasks array ON PURPOSE, so a reported roster with no entries is a real value a reader can be handed and it positively says nothing is alive — the #1240 signal. `resolveBoundModelList` leans on `turnevent.ModelList.Models` being documented "never empty" and `resolveBoundSlashCommandList` on `emitSlashCommandList`'s early return (#1877); **both of those sentences are FALSE here**, and `slash_command_list` two sections away in the protocol doc states the reverse rule. This one is re-derived from `BackgroundTaskRoster.Tasks`' own doc, not pattern-matched.

The consequence rides all the way out: `MapEvent` forwards the nil `Tasks` rather than pre-allocating, so the payload leaves this function with `Tasks == nil` and `BackgroundTaskRosterPayload.MarshalJSON` (value receiver, and this path returns a value) renders `"tasks":[]` on the wire. **Do not pre-allocate to get the same bytes** — that hides the normalisation the payload type owns. Silence is reserved for three states only: unbound, session gone, nothing ever reported.

`DroppedTasks` rides through UNTOUCHED — never recomputed from `len(Tasks)`, never zeroed. It is this variant's only truncation report (there is no top-level `TruncatedFields`), so shipping 0 would tell a client that a capped roster is the whole roster.

Value ownership: `sessionBackgroundTaskHold.BackgroundTaskRoster` hands back a deep copy this function solely owns, and `MapEvent` allocates a fresh outer `Tasks` slice while each row's `TruncatedFields` crosses by reference from that copy. Per-call and never retained ⇒ the payload owns its slices outright; do not add a second clone.

### `retainedBackgroundTaskRosters(convReg *conversations.Registry, pool *sessions.Pool) func() []protocol.BackgroundTaskRosterPayload`

`retainedSlashCommandLists`' body with the payload type substituted: `convReg.List()`, one resolver call per row keyed on `string(c.ID)`, `continue` on the comma-ok, append on success.

- **Enumeration is forced, not chosen.** A relay `V2Session` carries no conversation id, so a conversation-keyed resolver has nothing to key on at connect time; the walk has to happen daemon-side. The seam says so from its own side and names the conversation-keyed resolver as the obvious wrong shape.
- **The comma-ok is the only filter.** This loop reads no field of `Conversation` but `ID` and inspects no payload it appends. Every refusal rule stays in the resolver. In particular **no `len(p.Tasks) > 0` filter** — that would delete the payoff of the whole feature, and it is where such a filter would naturally be written, so it is the one thing the enumeration-level tests must redden.
- **The double lookup is a security control, not redundancy.** `Registry.List` already returns `CurrentSessionID`; this loop throws it away so the resolver can `Get` the row again. Reading the binding off the listed row would fork the empty-`CurrentSessionID` guard — the #678 enforcement point — and a drifted fork hands an unbound conversation the bootstrap child's roster under its own id. The bootstrap session needs no special case for the same reason: it has no conversation record, so it never appears in `List`.
- **Enumerate-all, unfiltered, so ARCHIVED conversations contribute.** `Registry.SetArchived` writes one field and never unbinds `CurrentSessionID`; the reconcile asserts current control truth and the client decides what to show. Neither adding nor omitting a `conversations.ListFilter` is a free edit.
- **Cost** is O(rows²) comparisons (`Registry.Get` is a linear scan) plus one deep copy per contributing session. Once per interactive handshake, never per turn. Stated because the registry only grows. The fix, if ever needed, is an id-keyed index inside `internal/conversations` — not reading the binding off the listed row.
- **Order is `List`'s and is NOT a contract.** Callers and tests correlate by `conversation_id`; the envelope id the reconcile stamps is fixed and non-load-bearing.

### Wiring

Three edits, mirroring `retainedSlashCommandLists` exactly:

1. `relayWiring` in `relay.go` gains `retainedBackgroundTaskRosters func() []protocol.BackgroundTaskRosterPayload`, documented as the twin's field is: built at `main.go` over the registry and `*sessions.Pool` because this file deliberately does not import `internal/sessions`; already primitive to `internal/relay` (both sides import `protocol`) so it crosses unwrapped; nil in foreground/v1 ⇒ no reconcile.
2. `startRelayV2`'s `V2SessionConfig` literal gains `RetainedBackgroundTaskRosters: w.retainedBackgroundTaskRosters` — **assigned straight through, never wrapped in a closure.** A wrapper is non-nil even when the field is nil and would silently defeat the seam's `nil ⇒ no-reconcile` contract that every foreground/v1 and test wiring relies on.
3. `main.go`'s `relayWiring` literal gains `retainedBackgroundTaskRosters: retainedBackgroundTaskRosters(convReg, pool)`, beside the two existing `retained*` lines.

## Concurrency model

No goroutine is spawned, so there is nothing to leak or join. Both functions are synchronous reads on the caller's goroutine, which for the seam is the relay manager's Run goroutine.

**Lock order: unchanged.** Three locks are involved — the conversations registry's mutex, `Pool`'s RLock, and the hold's leaf mutex — and they are acquired **sequentially and never nested**. Do not restructure the body so one lookup happens inside another's scope. The hold's own doc names this resolver as the reader its mutex exists for: one writer (the parser forwarder, serialised across respawns by `cmd.Wait`) against many readers on other goroutines.

**BOUNDED TIME holds, and is discharged rather than asserted.** The per-row `Get` runs OUTSIDE `List`'s lock scope because the loop iterates the copy `List` returns, and `Registry.Save` takes its snapshot under the registry mutex and releases it BEFORE any disk write — so no `Get` here can queue behind an fsync. Do not restructure through `Registry.Update` or any registry-held callback: that would add a registry→pool lock edge the daemon does not have and deadlock against `Update`'s no-re-entry rule.

**The `List` → `Get` and `Get` → `Lookup` windows are the benign TOCTOU every sibling shares.** A row created, deleted, rebound or rotated inside one either resolves to the roster of the session bound a moment ago or refuses, and both are correct — a roster is a property of one child's report, and a rotation's fresh child reports its own. No re-read, no retry, no re-list.

## Error handling

There are no `error` returns and no sentinels: every failure mode is a refusal spelled as the comma-ok bool, which is the whole surface the seam consumes. Six refusal arms:

| Arm | Reachable? | Why a refusal |
|---|---|---|
| Conversation unknown | yes | Untrusted id; nothing to resolve |
| `CurrentSessionID` empty | yes | The #678 guard — the one arm whose deletion is a disclosure, not a nil |
| `Pool.Lookup` error | yes | Dangling binding; the error is discarded, never wrapped |
| Runner lacks the method | yes | A non-stream-json runner; a refusal, never a panic |
| Nothing ever reported | yes | The load-bearing bool — NOT the same as an empty roster |
| `MapEvent` / payload assertion | no | Type-system arms; a refusal keeps the contract without a panic |

`Pool.Lookup`'s error is discarded deliberately: returning it bare is what keeps a hostile id out of a log line or a wire frame built from it. Nothing here logs, at any level (§ Security review).

## Testing strategy

One new table-ish test file, `cmd/pyry/session_background_task_list_test.go`, reproducing the twin's rig rather than sharing it (this family's stated convention for keeping each twin byte-stable).

Fixtures:
- `backgroundTaskRosterPlan` — a mutex-guarded `map[sessions.SessionID]turnevent.BackgroundTaskRoster` read at CALL time, because `sessions.New` invokes the runner factory while building the bootstrap and the test cannot know that id until `New` returns. A session with no entry reports the unreported state; **the plan must be able to arm an EMPTY roster distinguishably from having no entry at all**, which is this rig's one divergence from `slashCommandListPlan` and is exactly the distinction the load-bearing bool encodes.
- `backgroundTaskRosterRunner` — `stubRunner` plus the one concrete method, so plain `stubRunner` (via `newRouterTestPool`) stays the ready-made lacks-the-method fixture.
- `newBackgroundTaskRosterTestPool` — a real `*sessions.Pool` with a temp `RegistryPath`, every session's runner answering from the plan.
- `sentinelBackgroundTaskRoster(tag)` — two entries differing in every field, every string carrying `tag` in a conspicuous `ZZ…ZZ` form so the log negative's `strings.Contains` cannot match unrelated text, entry 2 deliberately awkward (nil `TruncatedFields`), non-zero `DroppedTasks`.
- `assertRosterCarries` — compares a payload against the SOURCE event field by field, `reflect.DeepEqual` on `TruncatedFields` so nil and empty do NOT compare equal.
- `indexRostersByConversation` — keys a result by `ConversationID` and fails a duplicate.

Scenarios, each rule proved ONCE at the level that can show it (the ticket's scope note — #2005 and #2007 each proved refusal-per-arm, archived-contributes, no-cross-conversation and logs-nothing at their own level; here they collapse):

*Resolver level — where the refusal arms live:*
1. Resolves the bound session's roster: one payload, this conversation's id, tasks in claude's order, `DroppedTasks` asserted against a LITERAL so a count recomputed from `len(Tasks)` reddens. (AC 1, AC 3)
2. Refusal table — unknown / unbound / empty id / dangling binding — with the bootstrap armed with a distinguishable roster, which is what makes the #678 guard's mutant SOLE-RED rather than invisible: delete the `CurrentSessionID == ""` clause and the unbound and empty rows flip to `ok == true` carrying `ZZ…BOOTSTRAPZZ` tasks. Each refusal must return the ZERO payload. (AC 2, AC 5)
3. A session whose runner implements the method but has reported nothing answers no roster — the sole red for a `roster, _ := reader.BackgroundTaskRoster()` simplification. (AC 2)
4. A runner without the method refuses rather than panicking. (AC 2)
5. **The empty roster.** A session that reported an empty roster resolves to `ok == true` carrying an empty task list — `len(Tasks) == 0`, `Tasks == nil` (the sole red for a resolver that forks the mapping to pre-allocate), and `json.Marshal` of the payload contains `"tasks":[]` and not `"tasks":null`, proving the wire contract rather than stopping at a Go value that could serialise either way. This test is the heart of the ticket. (AC 2)

*Enumeration level — where only a fan-out caller can show it:*
6. One bound, armed conversation yields exactly one payload with its own id and `DroppedTasks` carried. (AC 1, AC 3)
7. Refusing rows are skipped WITHOUT aborting the enumeration or contaminating a sibling, and the unbound row is never handed the bootstrap's roster stamped with its own id — one registry holding silent / unbound / dangling rows with the contributing row created LAST, so a mutant that broke out of the loop on the first refusal still returns nothing. This is the sole red for the double-lookup fork. (AC 2, AC 5)
8. **A row whose session reported an EMPTY roster contributes a payload**, alongside a silent row that does not — the sole red for a `len(p.Tasks) > 0 { continue }` filter in the loop, invisible to every other test here. (AC 2)
9. Two conversations bound to two DIFFERENT sessions each carry their own roster across ONE enumeration, and the two payloads do not share a backing array — the aliasing question only a fan-out caller raises. (AC 3, AC 5)
10. An ARCHIVED conversation whose bound session holds a roster contributes — the sole red for a `List(ListFilter{IsArchived: &f})` mutant. (AC 3)
11. **Two calls with no roster change between them return equal payloads and change no daemon state**: same slice contents both times, the hold still reports the same roster afterwards, the pool's session set is unchanged and no id was minted; mutating the first result's `Tasks` does not disturb the second. (AC 4)
12. An empty registry and a registry whose every row refuses each enumerate to no payloads and no panic — split from 7, which always has a survivor and so cannot distinguish "skipped the refusals" from "returned the survivor and stopped".
13. Log negative at BOTH levels in one test (the merge the scope note asks for): the enumeration exercises the resolver, and a registry holding a contributing row, an empty-roster row and refusing rows is exactly where a "why did this row skip" line would be added. Asserts the captured default logger is byte-empty, then belt-and-braces that no `TaskID`, `TaskType`, `Description` or truncated-field name and no skipped conversation's id appears in it. The `v != ""` guard is not decorative — `strings.Contains(logs, "")` is unconditionally true. Must not call `t.Parallel` (`slog.SetDefault` is process-global) and must build the pool BEFORE swapping the default.

`go test -race ./cmd/pyry/...`, `go vet ./...`, `go build ./cmd/pyry` are the verification gate; the whole-module race suite is the verifier's.

## Open questions

- **Does the twin's `sentinel…` tag discipline survive a roster whose `TaskType` is a closed vocabulary?** `turnevent.BackgroundTask.TaskType` carries claude's own type string (`local_bash` among them). If any consumer or assertion in `cmd/pyry` compares it against a known value, a `ZZ…ZZ` sentinel would be rejected. Resolve by reading `BackgroundTask`'s field doc before writing the fixture; if the field is opaque on this path (expected — the mapping copies it verbatim), sentinels stand.
- **Whether `TestRetainedBackgroundTaskRosters_TwoCallsAgreeAndMutateNothing` can pin "mints no id" positively**, or only negatively via an unchanged pool session set. Resolve in Phase B; if only the negative form is available, say so in the test's header rather than overclaiming.

Each is resolved during implementation and, if the resolution changes the design above, recorded in a `## Revisions` entry.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. The design has exactly one boundary and it is a single named function, not scattered: `resolveBoundBackgroundTaskRoster`. Two flows cross it. (a) `convID` is untrusted network input in the general case; it is used as a lookup key into the daemon's own registry and NOTHING else — never returned on a refusal (every arm hands back the zero payload), never joined into a path, never wrapped into an error, and *not even echoed on success*, because the reported id comes from the resolved record's `conv.ID` rather than from the parameter. From the seam's entry point that arm is unreachable anyway: every id `retainedBackgroundTaskRosters` passes came out of `Registry.List()` and is server-minted (`conversations.NewID`), so no second id guard is added here. (b) Roster text crosses subprocess-stdout → parent state, and it arrives ALREADY BOUNDED at construction by `internal/streamsup` in both dimensions. This slice re-validates nothing and sanitises nothing, deliberately — the render boundary is the CLIENT's, which is `turnevent.BackgroundTask`'s own documented decision, not an omission here. Downstream callers are signalled by the payload type's own doc block rather than by the type system.
- **[Tokens, secrets, credentials]** Not applicable, for a structural reason rather than by inspection: this call graph reaches `conversations.Registry`, `sessions.Pool`, `sessionBackgroundTaskHold` and `turnbridge.MapEvent`, none of which touches a pairing token, a Noise key or a device credential. Nothing here authenticates, re-authenticates, mints, stores, rotates or revokes anything — the seam is consumed only from `reconcileBackgroundTaskRosters`, which runs post-handshake and post-token-validation on a leg `internal/relay` has already authenticated.
- **[File operations]** Not applicable. No path is constructed, opened, created or written on this path, so mode, symlink and atomic-write questions do not arise. `Registry.Get`/`List` are in-memory reads over an already-loaded registry and `Pool.Lookup` is an in-memory map read; `Registry.Save` is NOT called — the enumeration is a pure read. The one check-then-use window that does exist is on shared memory, not on a path, and is analysed under Concurrency.
- **[Subprocess / external command execution]** No findings, and the important content of this category is a NEGATIVE rule rather than an absence. There is no `exec.Command` and no `sh -c` here — but for claude's `local_bash` task type a `BackgroundTask.Description` **is a literal command line**, and this slice hands out a whole LIST of them, which `BackgroundTask`'s own doc grades as the more tempting shape of this family. Both functions must copy a `Description` into a payload field and do nothing else with it: never execute it, never re-shell it, never feed it to a structured sink. A future edit that re-ran a description to check whether a task is still alive is the exact shape this rule exists to stop.
- **[Cryptographic primitives]** Not applicable. No randomness is drawn (no id is minted — see AC 4), no key or nonce is used, and the one comparison on this path is `Registry.Get`'s byte-exact `c.ID == id`. That is not a secret comparison and does not need `crypto/subtle`: conversation ids are server-minted, non-secret routing identifiers, and the secret that gates this whole path is the pairing token, validated in `internal/relay` before the seam is ever called.
- **[Network & I/O]** OUT OF SCOPE, deferred to the Mode B umbrella #829 with the arithmetic stated so the deferral is checkable rather than asserted. This path applies NO bound of its own — not per row, not on the number of payloads returned — and that is deliberate: a second cap would be a second place the limit is decided and the two could disagree silently. Per payload the bound is the producer's: `maxTaskRosterEntries` (8) × (`maxTaskFieldID` 256 + `maxTaskFieldID` 256 + `maxTaskRosterDescription` 512) ≈ 8 KiB of text. Cardinality is uncapped and the registry only GROWS (auto-archive sets a flag rather than deleting, and archived rows deliberately contribute), so the relay-side backstop is `pushQueueByteCeiling` (32 MiB) over a queue created a few statements earlier in the same `handleNoiseInit` — reaching it from a cold start needs on the order of thousands of maximally-full retained rosters across all six reconciles combined, and the outcome is the deterministic, already-tested `StatusQueueOverflow` teardown rather than unbounded growth. The second, larger item deferred to the same umbrella: no per-device confinement — turning this path on is what gives that effect, since a paired interactive conn is now unicast EVERY retained roster, including conversations bound to other workspaces. That belongs to #829, not to one of its six instances.
- **[Error messages, logs, telemetry]** No findings, and this is the category the design is most load-bearing on. Neither function takes a `*slog.Logger` and neither may grow one: all four strings a task row carries are claude-authored untrusted text, and `emitBackgroundTaskRoster`'s own drop path already refuses to log even the entry COUNT — "just the length" being the leak a content-free rule is most often bent for. There are no `error` returns to leak through, and `Pool.Lookup`'s error is DISCARDED rather than wrapped precisely so a hostile or malformed `convID` cannot be reflected into a record a caller builds from it. `conversation_id` would be safe to log (server-minted), but there is nothing here to log it from. Enforcement is belt-and-suspenders with different fabric: the no-logger signature is the structural half, and scenario 13's log negative is a DETERMINISTIC test against the one real regression shape a signature cannot stop — someone reaching for package-level `slog.Info` / `slog.Default()` to explain why a row did not contribute, in a loop that is exactly where such a line gets added.
- **[Concurrency]** One SHOULD FIX, no MUST FIX. Lock order is unchanged: three locks (registry mutex, pool RLock, hold leaf mutex) taken sequentially and never nested, so no new edge enters the daemon's lock order. Nothing is spawned, so no goroutine can leak; nothing is mutated, so a mid-call teardown leaves no partial state to recover. Two check-then-use windows exist and neither is exploitable. The `Get` → `Lookup` window is closed by construction: the guard (`CurrentSessionID == ""`) and the lookup key are read off the SAME `Get` result, which is what the double lookup preserves and what reading the binding off the listed row would fork — the #678 disclosure. The `List` → `Get` window is genuinely open, and the worst an attacker who can force a rebind inside it achieves is a conversation being answered with the roster of the session it was bound to a moment earlier or later, which is the contract, because `CurrentSessionID` is always read off that conversation's OWN row and never off a sibling's. **SHOULD FIX:** `conversations.Registry.Create` explicitly does not validate that `c.ID` is unique or non-empty ("Caller owns uniqueness"), and `Get` returns the FIRST match — so a registry holding duplicate ids would enumerate one payload per row, all resolving the first row's roster under the shared id. That is not a cross-identity disclosure (the id is what confines the data, and it is identical for both), but the test helper `indexRostersByConversation` fails a duplicated id, so the assumption is load-bearing for the tests. State it in that helper's header as the registry's documented caller obligation rather than adding a uniqueness check here. Likewise an empty-id row, were one ever created, resolves its own binding under its own empty id and still runs the `CurrentSessionID` guard — so it is not the bootstrap-leak shape, and no id-shape check is added here because well-formedness is the registry's to own.
- **[Threat model alignment]** No findings. The two structural gates that protect this data are `internal/relay`'s and are deliberately NOT re-derived here: authentication (the seam is called only from `handleNoiseInit`'s success tail, post-handshake and post-token-validation) and capability (`!s.interactive` returns early). Re-implementing either at this end would be a second place the rule is decided. `docs/protocol-mobile.md` § Security model's relevant threats are otherwise covered above — untrusted claude text reaching a sink under Trust boundaries / Subprocess / Logs, resource exhaustion under Network & I/O. The one threat this ticket makes live rather than mitigates is cross-workspace visibility for a paired device, named OUT OF SCOPE to #829 above.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-05

## Revisions

### 2026-09-05 — Phase B

**Neither open question changed the design**, so nothing above is superseded; both are recorded here so the resolutions are findable rather than merely absent.

- **`TaskType` sentinels.** Resolved as the expected case: `turnevent.BackgroundTask.TaskType` is documented as "a plain string rather than a closed enum", and nothing on this path compares it against claude's `local_bash`. Conspicuous `ZZ…ZZ` sentinels stand, and `sentinelBackgroundTaskRoster`'s header records why.
- **"Mints no id" in the idempotence test.** Only the NEGATIVE form was available, as the plan anticipated: there is no mint counter to assert against, so the property is pinned as the pool's session set and the registry's row set being unchanged across both enumerations. `TestRetainedBackgroundTaskRosters_TwoCallsAgreeAndMutateNothing`'s header states plainly that this cannot see an id minted and immediately discarded, only one that reached daemon state — stated rather than overclaimed.

**The four sole-red claims the testing strategy makes were verified by mutation** (`go test -overlay`, no worktree writes), not assumed:

| Mutant | Reddened |
|---|---|
| `len(payload.Tasks) == 0 { continue }` added to the enumeration loop | `TestRetainedBackgroundTaskRosters_EmptyRosterRowContributes` **only** |
| Resolver forks the mapping to pre-allocate `Tasks` | `TestResolveBoundBackgroundTaskRoster_EmptyRosterResolvesToAnEmptyTaskList` **only** |
| `List(ListFilter{IsArchived: &f})` narrowing the enumeration | `TestRetainedBackgroundTaskRosters_ArchivedConversationsContribute` **only** |
| The `conv.CurrentSessionID == ""` guard deleted (#678) | 5 tests, including the unbound refusal row and the bootstrap-leak pin — appropriately broad for a disclosure rather than sole-red |

One detail worth carrying forward from that run: with the #678 guard deleted, the refusal table's **empty conversation id** row stays GREEN, because `Registry.Get("")` misses one step earlier. The guard's sole red is the *unbound* row, not the empty-id one — so a future variant of this test that dropped the unbound row while keeping the empty-id row would pin nothing at all.
