# #2007 — enumerate the retained slash-command lists onto the connect-time reconcile seam

`security-sensitive`. Fills `V2SessionConfig.RetainedSlashCommandLists` (#2006, shipped
unwired) with the daemon-side producer, bridging it to `resolveBoundSlashCommandList`
(#2005, shipped with no production caller). This is the second half of the
enumerate-all/conversation-keyed mismatch both siblings' doc blocks name.

## Files read

Production, in the order the design depends on them:

- `cmd/pyry/session_model_list.go` → `retainedModelLists` — **the analogue this slice
  reproduces**: the closure shape, the double lookup, the enumerate-all posture and the
  comma-ok-is-the-only-filter rule all come from here verbatim. Also
  `resolveBoundModelList`, whose refusal contract the enumerator leans on.
- `cmd/pyry/session_slash_command_list.go` → `resolveBoundSlashCommandList` — the
  resolver this slice adapts. Its doc block states the three ways it diverges from the
  model-list twin; two of them (the producer-sourced non-empty guarantee, the two-source
  `DroppedCommands`) change what the enumerator's doc may claim.
- `internal/relay/v2session_seams.go` → `RetainedSlashCommandLists` — the seam's written
  contract: enumerate-all, order not a contract, already-bounded payloads only, bounded
  time on the Run goroutine, `nil ⇒ no reconcile`. Also `RetainedModelLists` for the
  precedent wording.
- `internal/relay/v2session_slashreconcile.go` → `reconcileSlashCommandLists` — the sole
  consumer. Confirms this slice builds **no envelope**: the type stamp, the shared batch
  timestamp, the nil `EventID` and the `Push` are all already there.
- `cmd/pyry/relay.go` → `relayWiring` (the `retainedModelLists` field), `startRelayV2`
  (the `RetainedModelLists:` assignment in the `V2SessionConfig` literal) — the two edit
  points, and the file whose import block deliberately omits `internal/sessions`.
- `cmd/pyry/main.go` → `runSupervisor`'s `relayWiring` literal — the composition root,
  where `retainedModelLists(convReg, pool)` is constructed.
- `cmd/pyry/relay_guard_test.go` → `TestOutstandingQuestionsWiredToSurfacerRegistry` —
  read to establish that **no guard update is owed**: its header states in terms that the
  sibling seams are deliberately unpinned at their assignment sites and that
  `TestRetainedModelLists_*` stays green if the `V2SessionConfig` line is deleted.
- `cmd/pyry/session_model_list_test.go` → `indexByConversation`,
  `TestRetainedModelLists_EnumeratesTheBoundSessionsMenu` and its four siblings — the
  test suite this slice reproduces one-for-one.
- `cmd/pyry/session_slash_command_list_test.go` → `newSlashCommandListTestPool`,
  `slashCommandListPlan`, `sentinelSlashCommandList`, `assertSlashCommandsCarry` — the
  rig already in place; **every fixture this slice needs already exists**, which is the
  whole reason the test half costs test bodies and no new rig.

Knowledge, and what each changed in the design:

- `docs/knowledge/features/v2-session-manager-state-machine-connect-time-slash-command-list-reconcile-retain.md`
  — states that #2005's conversation-keyed resolver "is the obvious thing to reach for
  here and is the wrong shape", that bridging is this ticket's job, and that the
  aggregate cardinality bound lives in two other packages. It also assigns the
  `docs/protocol-mobile.md` § Reconnect / Backfill Mode B note to this ticket; see
  **Context** for why that is not taken here.
- `docs/knowledge/features/sessions-package-key-types-runner-interface-runnerfactory.md`
  — **"a twin's stated reason does not transfer just because its conclusion does"**
  (#2005). Directly binding: `retainedModelLists`' doc block justifies dropping a
  `len(Models) > 0` check by citing `turnevent.ModelList.Models` being "never empty", and
  that sentence is **false** for `turnevent.SlashCommandList.Commands`. The conclusion
  survives, sourced from the producer instead. Same doc records that
  `resolveBoundSlashCommandList` currently has no production caller and that this slice
  is it.
- `CODING-STYLE.md` — define interfaces where consumed; stdlib-only tests; table-driven.
- `docs/knowledge/architecture/system-overview.md` — the `cmd/pyry` composition-root
  posture that keeps `internal/sessions` out of `relay.go`.

## Context

`RetainedSlashCommandLists` (#2006) is a `V2SessionConfig` field that nothing assigns, so
`reconcileSlashCommandLists` returns at its own nil guard on every handshake. The
reconcile is correct and inert. `resolveBoundSlashCommandList` (#2005) answers one
conversation's payload and has no production caller. This slice is the wire between them
and turns the path on.

The enumeration is **forced, not chosen**. A relay `V2Session` holds `connID`, `state`,
`resp`, `send`, `recv`, `device`, `interactive` and `peerStatic` — no conversation id — so
there is nothing to key a conversation-scoped resolver on at connect time, and the only
way to fill a conversation-scoped seam is to walk the conversation registry. That is what
#1867 landed for the model-list sibling, and both the seam's doc block and the resolver's
say so independently.

**Out of scope, stated rather than silently skipped.** The #2006 package overview assigns
the `docs/protocol-mobile.md` § Reconnect / Backfill Mode B list note to this ticket. It
is not taken here for two reasons: this ticket's acceptance criteria contain no
documentation clause and its size estimate names three production files, and — more
substantively — that list is **already stale for the model-list twin**: it names only the
modal, queue and question reconciles, and a separate paragraph in the same document still
asserts there is "no connect-time snapshot today" for `model_list`, which #1863/#1867
falsified. #1867 did not fix it either. Adding `slash_command_list` alone would leave the
document internally inconsistent in a new way. This wants its own docs ticket covering
both frames; recording it here is how it stays findable.

**No ADR.** This slice introduces no decision — every choice it makes is inherited
verbatim from `retainedModelLists` (#1867) and named in the seam's own doc block.

## Design

One new unexported function and two wiring lines. No new type, no new interface, no new
import in any file.

### The enumerator — `cmd/pyry/session_slash_command_list.go`

```go
func retainedSlashCommandLists(convReg *conversations.Registry, pool *sessions.Pool) func() []protocol.SlashCommandListPayload
```

Placed **beside its resolver** in the topic file, exactly as `retainedModelLists` sits
beside `resolveBoundModelList`: adding it manufactures no merge conflict in the high-churn
wiring file. The body is `retainedModelLists`' body with the payload type substituted —
`convReg.List()`, a slice pre-sized to `len(convs)`, one `resolveBoundSlashCommandList`
call per row, `continue` on the comma-ok's false, append on true. Roughly a dozen lines
under a doc block that carries the reasoning.

Load-bearing properties, each of which a test pins:

- **The comma-ok is the only filter.** The loop reads no field of `Conversation` but `ID`
  and inspects no payload it is about to append. Every refusal rule stays in the resolver.
  In particular there is deliberately **no** `len(Commands) > 0` check.
- **The double lookup is deliberate.** `Registry.List` hands back each row with its
  `CurrentSessionID` and the loop throws that away so the resolver can `Get` the row
  again. Reading the binding off the listed row and calling `Pool.Lookup` directly forks
  the empty-`CurrentSessionID` guard — the #678 isolation enforcement point, where
  `Pool.Lookup("")` returns the **bootstrap** session — and a drifted fork hands an
  unbound conversation the shared bootstrap child's menu stamped with its own
  conversation id.
- **Unfiltered.** `List` is called with no `ListFilter`, so **archived** conversations
  contribute: `Registry.SetArchived` writes one flag and never unbinds
  `CurrentSessionID`, the reconcile asserts current control truth, and the client decides
  what to show.
- **No logger, and it must not grow one.** The only thing a "why did this row not
  contribute" line could carry is a conversation id or the workspace's own command
  strings.
- **Order is `List`'s and is not a contract.** Callers correlate by `conversation_id`.

**The one place the twin's doc must not be copied.** `retainedModelLists` justifies the
absent emptiness check by citing `turnevent.ModelList.Models` as documented "never empty".
`turnevent.SlashCommandList.Commands` carries no such type-level guarantee — it is
documented nil for a zero-length list and defers to its producer. The conclusion survives
for the **producer's** reason: streamsup's `emitSlashCommandList` returns early on a
zero-length entry list (#1877), so nothing empty ever reaches the retention, and
`resolveBoundSlashCommandList`'s own doc block states it that way. The enumerator's doc
inherits the resolver's wording, not the twin's.

### The relay-side field — `cmd/pyry/relay.go`

A new `relayWiring` field:

```go
retainedSlashCommandLists func() []protocol.SlashCommandListPayload
```

and, in `startRelayV2`'s `V2SessionConfig` literal, `RetainedSlashCommandLists:
w.retainedSlashCommandLists` — **assigned straight through, never wrapped in a closure**.
A wrapper is non-nil even when the underlying value is nil and would silently defeat the
seam's `nil ⇒ no reconcile` contract that every foreground/v1 and test wiring relies on.
`protocol` is already imported both sides, so the field crosses with no new import.

**Built at the composition root, not inline here.** `relay.go` deliberately does not
import `internal/sessions`. `outstandingQueues` *is* built inline in `startRelayV2`, but
only because that file already imports `internal/msgqueue` — it is the precedent for the
closure's *shape* and not for its wiring location. `runSettings` and `retainedModelLists`
are the placement precedent.

### The composition root — `cmd/pyry/main.go`

One entry in `runSupervisor`'s `relayWiring` literal:
`retainedSlashCommandLists: retainedSlashCommandLists(convReg, pool)`, beside
`retainedModelLists`. `convReg` and `pool` are already in scope there.

### Data flow

`handleNoiseInit` interactive-open tail → `reconcileSlashCommandLists` → seam →
`retainedSlashCommandLists` closure → `Registry.List` → per row
`resolveBoundSlashCommandList` → `Registry.Get` → `Pool.Lookup` → runner type assertion →
`sessionSlashCommandHold.SlashCommandList` (deep copy) → `turnbridge.MapEvent` →
`protocol.SlashCommandListPayload`. The reconcile marshals and pushes; this slice's half
ends at the payload slice.

## Concurrency model

No goroutine is spawned, so there is nothing to leak and nothing to join. The closure runs
**synchronously on the relay manager's Run goroutine**, which the seam's BOUNDED TIME
clause makes an obligation rather than an observation.

**Lock order.** The per-row `Get` runs **outside** `List`'s lock scope, because the loop
iterates the copy `List` returns. Do not restructure through `Registry.Update` or any
registry-held callback: that would both add a registry→pool lock edge the daemon does not
have and deadlock against `Update`'s no-re-entry rule. Within one row the resolver takes
the registry lock, the pool's `RLock` and the hold's leaf mutex **sequentially, never
nested** — its doc block states this and the enumerator adds no nesting of its own.

**Cost.** `Registry.Get` is a linear scan, so N rows cost N scans: O(rows²) comparisons
plus one deep copy per contributing session. This runs once per interactive handshake and
never per turn, so at the tens-to-hundreds of rows this daemon carries it is microseconds.
Stated because the registry only **grows** — auto-archive sets a flag rather than deleting
and archived rows are deliberately enumerated. The fix, if one is ever needed, is an
id-keyed index inside `internal/conversations`, **not** reading the binding off the listed
row.

**TOCTOU.** The `List` → `Get` window is benign and inherited: a row created, deleted,
rebound or rotated inside it either resolves to the inventory of the session bound a
moment ago or refuses, and both are correct. No re-read, no retry, no re-list.

## Error handling

This path has **no error values**. It returns a slice and nothing else; the resolver's
comma-ok is the sole failure channel and every refusal is a skipped row.

| Failure mode | Behaviour |
|---|---|
| Registry empty | Empty (non-nil, zero-length) slice; the reconcile's `len == 0` early return keeps it inert. |
| Row unbound (`CurrentSessionID == ""`) | Resolver refuses **before** the pool is touched — the #678 guard. Row skipped, enumeration continues. |
| Row's binding names a session the pool lacks | `Pool.Lookup` errors; the resolver discards the error rather than wrapping it. Row skipped. |
| Bound session's runner lacks `SlashCommandList` | Type assertion fails cleanly (a nil `Runner` fails it too). Row skipped. |
| Bound session reported nothing | Comma-ok false. Row skipped. |
| Mapping arms | Unreachable by construction; the resolver answers a refusal rather than panicking. Row skipped. |

A refusal must never abort the enumeration, never append a zero payload, and never carry a
prior row's payload forward — the loop `continue`s and nothing outside it is mutated.

## Testing strategy

All in `cmd/pyry/session_slash_command_list_test.go`, reusing the rig already there.
`sentinelSlashCommandList` values are conspicuous `ZZ…ZZ` sentinels precisely so the log
negative's `strings.Contains` cannot false-positive on ordinary prose. One new helper
mirroring `indexByConversation`: keys a result on `ConversationID` and `Fatal`s on a
duplicate, which is the shape a loop that appended a row twice takes.

Scenarios, each a separate test function:

- **AC1 — enumerates the bound session's inventory.** One conversation bound to an armed
  session yields exactly one payload carrying that conversation's id, that session's five
  fields per entry in order, and the armed `DroppedCommands` (asserted against the literal
  `4`, so a value recomputed from `len(Commands)` reddens).
- **AC4 — each refusal skips and the enumeration keeps going.** All four rows in **one**
  registry: unbound, dangling binding, runner-implements-but-reported-nothing, and the
  survivor **created last**. That order is load-bearing — a mutant that `break`s on the
  first refusal instead of `continue`ing would still return a survivor placed first.
  Asserts exactly one payload, that it is the survivor's, and that no payload carries zero
  commands.
- **AC4 — nothing to send.** Table of two registries (empty; every row refuses), each
  enumerating to zero payloads and no panic. The bootstrap is armed but **unbound**, so a
  mutant enumerating the *pool* instead of the registry reddens here. Split from the test
  above because that one always has a survivor and so cannot distinguish "skipped the
  refusals" from "returned the survivor and stopped".
- **AC3 — archived conversations contribute.** One archived row bound to an armed session
  yields its payload. This is the sole red for a mutant narrowing the call to
  `List(ListFilter{IsArchived: &f})`; every other test builds unarchived rows.
- **AC2 — does not cross conversations.** Two conversations bound to two **different** pool
  sessions holding distinguishable inventories, across **one** enumeration. Indexed by
  conversation id, never by position. Carries an explicit "both resolved to the same
  inventory" assertion so the failure it exists to catch is legible without comparing two
  sentinel tags. The resolver's own isolation test makes the point one call at a time;
  only the enumerator can cross two rows inside a single result slice. Needs
  `t.Setenv("HOME", …)` for `Pool.Create`, and therefore no `t.Parallel`.
- **AC5 — logs nothing.** `slog.SetDefault` over a buffer at `LevelDebug`, registry
  holding **both** a contributing row and refusing ones, because the "why did this row not
  contribute" line is exactly where such a call would be added. Asserts the buffer is
  empty, then belt-and-braces that no command string appears in it — each guarded by
  `v != ""`, since `strings.Contains(logs, "")` is unconditionally true and would make the
  loop assert nothing. No `t.Parallel`: `slog.SetDefault` is process-global. The pool is
  built **before** the default is swapped so `sessions.New`'s own diagnostics cannot land
  in the buffer.

Honest limit, stated rather than left implicit: the structural half of AC5 is enforced by
the signature (the function takes no `*slog.Logger`) and by review. The test catches the
one real regression shape — reaching for package-level `slog.Info` / `slog.Default()`.

**No guard test for the `V2SessionConfig` assignment.**
`TestOutstandingQuestionsWiredToSurfacerRegistry`'s header records that it is
first-of-its-kind and that the sibling seams — `RetainedModelLists` included — are
deliberately unpinned at their assignment sites. Matching the family is the in-scope
choice; extending the guard to a fifth seam is its own ticket.

**Gate:** `go test -race ./cmd/pyry/...`, `go vet ./...`, `go build ./cmd/pyry`.

## Open questions

1. **Does `docs/protocol-mobile.md`'s Mode B list belong to this ticket?** Resolved
   during planning: no — see **Context**. The list is already stale for the model-list
   twin, this ticket's AC has no documentation clause, and the named analogue #1867 did
   not touch it. Recorded as wanting its own ticket covering both frames.
2. **Does the stale `#2007` reference in `resolveBoundSlashCommandList`'s inline comment
   need correcting?** Resolved: no. The ticket names it explicitly — it predates #2006 and
   attributes the envelope construction to this slice — and instructs only that it not be
   acted on. Editing a production comment for a fact this slice does not change is
   out-of-scope churn in a file the diff already touches; the enumerator this slice adds
   sits directly below it and is the correction a reader needs.
3. **Is a payload count cap wanted?** Resolved: no. The seam's SECURITY clause makes the
   bound the producer's obligation and names `pushQueueByteCeiling` as the relay-side
   backstop; a second cap here would be a second place the limit is decided.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings — and the one hazard that would be a MUST FIX is
  foreclosed by the double lookup, verified in code.** The workspace→daemon boundary was
  crossed upstream at streamsup's decode (#2004); this slice introduces no new input and
  moves already-mapped payloads only. Every `convID` it hands the resolver came out of
  `Registry.List` and is server-minted (`conversations.NewID` at creation), so
  `resolveBoundSlashCommandList`'s untrusted-id arm is unreachable from this entry point
  and no second guard is owed — a decision, not an omission. The hazard worth naming is
  the "simplification" available in Phase B: reading `CurrentSessionID` off the listed row
  and calling `Pool.Lookup` directly. Read rather than assumed, `Pool.Lookup` returns
  `p.sessions[p.bootstrap]` **with a nil error** for the empty id, so that shortcut would
  hand every unbound conversation the shared bootstrap child's command menu stamped with
  its own `conversation_id` — a real cross-conversation disclosure, and precisely the
  failure AC2 pins. The double lookup is what keeps the #678 guard un-forked; it is a
  security control, not redundancy.
- **[Tokens, secrets, credentials] No findings — the path holds no credential material.**
  It mints no id, no token and no Noise nonce. Nonce discipline is unchanged: `Push` only
  buffers and `drainOnce` consults `Connected` before sealing, so no send-nonce is burned
  for a frame that cannot reach the phone (#2006's property; this slice changes only how
  many payloads reach it).
- **[File operations] No findings — the path performs no file I/O, verified.**
  `Pool.Lookup` is an in-memory `RLock` map read; `Registry.List` and `Registry.Get` are
  in-memory under `r.mu`. No path is joined, no file opened, so traversal, TOCTOU-on-path,
  permissions and symlink handling have no surface here.
- **[Subprocess / external command execution] No findings — nothing is executed.** No
  `exec.Command`, no environment read or write, no signal handling.
- **[Cryptographic primitives] No findings — no primitive is used.** No RNG, no key or
  nonce derivation, and no comparison against a secret, so constant-time discipline does
  not arise.
- **[Network & I/O] OUT OF SCOPE — aggregate cardinality is uncapped, named rather than
  capped here; owner is the producer per the seam's SECURITY clause, escalation path is
  #829.** This slice is what makes the seam return a non-empty slice in production for the
  first time, and the count is `O(conversations)`. The bounds that make it safe live in
  two other packages: `maxSlashCommandListBytes` bounds one payload's serialised
  `commands` array (worst case measured 63224 B against the 65519 B v2 envelope cap), and
  `pushQueueByteCeiling` (32 MiB) is the relay-side backstop over a queue created a few
  statements earlier in the same `handleNoiseInit`. Tripping it from cold needs roughly
  512 worst-case lists across all five reconciles — a deterministic, already-tested
  `StatusQueueOverflow` teardown of the offending conn alone, not unbounded growth.
  Amplification is below 1: creating a conversation requires a paired, authenticated
  device, and a row only contributes if its bound session holds a list, which means a real
  claude child that answered `initialize` — costlier for the attacker than for the daemon.
  A cap here would be a second place the limit is decided and could silently disagree with
  the producer's.
- **[Error messages, logs, telemetry] No findings — nothing is logged at any level, and
  the threat is one step sharper than the model-list twin's.** `Name`, `ArgumentHint`,
  `Description` and every `Aliases` entry are workspace-authored: whoever wrote a
  repository controls them, a lower-trust origin than claude-authored text. The function
  signature takes no `*slog.Logger` and must not grow one; there is no error value on the
  path to leak, since the resolver discards `Pool.Lookup`'s error rather than wrapping it
  precisely so a malformed id is never reflected into a record. The AC5 test's registry
  deliberately holds a contributing row **and** refusing ones, because the "why did this
  row not contribute" line is exactly where such a call would be added.
- **[Concurrency] No findings — one written obligation discharged by verification, one new
  aliasing question answered.** (a) The seam's BOUNDED TIME clause names this ticket
  explicitly, warning that walking a registry under its mutex "is exactly the shape that
  can block" the manager's Run goroutine. Checked rather than assumed: `Registry.Save`
  snapshots under `r.mu` and releases it **before** `MkdirAll`, write and rename, so none
  of the N per-row `Get` calls can ever queue behind a disk fsync. The work is O(N²)
  short-string comparisons with no I/O and no blocking call. (b) A question the resolver
  never raises, because the enumerator is the first thing to call it twice in one result:
  do two conversations bound to the **same** session hand back payloads that alias a
  backing array? No — `sessionSlashCommandHold.SlashCommandList` deep-copies per call and
  `turnbridge.MapEvent` allocates a fresh outer slice, so each payload owns its slices
  outright and the reconcile's marshal never writes through a producer's array. (c) Lock
  order adds no edge: the per-row `Get` runs outside `List`'s scope because `List` returns
  a fresh `[]Conversation` under `r.mu` and the loop iterates that copy. No goroutine is
  spawned, so no lifecycle or leak question arises.
- **[Threat model alignment] OUT OF SCOPE — no per-device confinement, and this slice is
  what makes it real for slash commands.** `docs/protocol-mobile.md` § Security model: a
  paired interactive conn is unicast **every** retained list, including conversations
  bound to other workspaces. Until now the seam was nil and nothing was disclosed; turning
  the path on is what gives the gap effect. Not closed here for the family's stated reason
  — pairing is the trust boundary (a paired device already receives modals, queue
  backlogs and conversation listings), and confinement in one of five Mode B instances
  would be a second place the rule is decided. It belongs to the Mode B umbrella #829,
  which is where #2006's own security review assigned the identical finding. The two
  structural gates the reconcile does enforce are unchanged and inherited:
  authentication (post-handshake, post-token-validation) and capability (`!s.interactive`).

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-02
