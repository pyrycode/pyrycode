# #2005 — resolve a session's retained slash-command list into a wire-ready payload

Fifth member of `cmd/pyry`'s conversation-keyed resolver family, after
`resolveBoundRunner`, `resolveBoundSession`, `resolveBoundRunSettings` and
`resolveBoundModelList` (#1857). Composition and refusal: every part it joins is
already on `main`.

## Files read

- `cmd/pyry/session_model_list.go` → `resolveBoundModelList` — the direct twin.
  Its body is the shape to mirror arm-for-arm; its doc is the wording to inherit
  everywhere the three divergences below do not apply.
- `cmd/pyry/session_model_list_test.go` → `modelListPlan`, `modelListRunner`,
  `newModelListTestPool`, `sentinelModelList`, `assertPayloadCarries`, and the
  six `TestResolveBoundModelList_*` cases — the test rig to mirror. The
  `retainedModelLists` half of that file is #1863's and has no counterpart here;
  the enumeration seam is #2007's.
- `cmd/pyry/streamsup_runner.go` → `streamRunner.SlashCommandList` — the
  accessor this resolver asserts for (#2004). Its doc already names this ticket
  as its consumer and states the reach-by-assertion rule.
- `cmd/pyry/session_slash_command_hold.go` → `sessionSlashCommandHold.SlashCommandList`,
  `cloneSlashCommandList` — the deep copy this function solely owns, and the
  producer-sourced non-emptiness wording to inherit (divergence 1).
- `internal/turnbridge/outbound.go` → the `case turnevent.SlashCommandList:` arm
  of `MapEvent`, `maxSlashCommandListBytes` — the unforked mapping, its absent
  suppression branch (divergence 2) and its two-term `DroppedCommands`
  (divergence 3).
- `internal/protocol/interactive.go` → `SlashCommandListPayload`, `SlashCommand`
  — the payload shape and its `MarshalJSON` normalisation, which owns nil→[] for
  `Commands` and `Aliases` and deliberately exempts `TruncatedFields`.
- `internal/turnevent/event.go` → `SlashCommandList`, `SlashCommand` — the source
  event; `Commands` is documented nil for a zero-length list, which is why the
  twin's type-level non-emptiness sentence does not transfer.
- `cmd/pyry/session_router_test.go` → `stubRunner`, `newRouterTestPool` — the
  ready-made runner-without-the-method fixture; `stubRunner` implements
  `sessions.Runner` and nothing more.
- `cmd/pyry/session_revive_test.go` → `runPoolReady` — needed by the isolation
  test, which calls `Pool.Create` and so needs the pool running.

## Context

A session now retains its child's slash-command inventory (#2004), but nothing
turns "this conversation id" into "this marshal-ready payload, or nothing". This
adds that one function. Its consumer is #2007, already open and already blocked
on this ticket; the connect-time seam is that ticket's deliverable and is not
anticipated here. Same two-ticket shape as #1857 → #1867.

No ADR is warranted: every decision here is inherited from `resolveBoundModelList`
and is recorded in that function's own doc.

## Design

One new file, `cmd/pyry/session_slash_command_list.go`, holding one unexported
function and no types:

```go
func resolveBoundSlashCommandList(convReg *conversations.Registry, pool *sessions.Pool, convID string) (protocol.SlashCommandListPayload, bool)
```

Data flow, six sequential arms, each refusing with `(protocol.SlashCommandListPayload{}, false)`:

1. `convReg.Get(conversations.ConversationID(convID))` — miss → refuse.
2. `conv.CurrentSessionID == ""` → refuse, **before the pool is touched**. This
   is the #678 isolation enforcement point: `Pool.Lookup("")` returns the
   bootstrap session, so an unbound conversation reaching a lookup would be
   handed the shared bootstrap child's menu stamped with its own conversation
   id. Folded into arm 1's condition exactly as the twin folds it — one `if`,
   two clauses; no third `convID == ""` pre-check, since an empty id lands in
   arm 1 already.
3. `pool.Lookup(sessions.SessionID(conv.CurrentSessionID))` — error → refuse,
   **discarding** the error rather than wrapping it.
4. Type assertion on `sess.Runner()` for the anonymous interface
   `interface{ SlashCommandList() (turnevent.SlashCommandList, bool) }` — not ok
   → refuse. A nil runner fails the assertion cleanly, so no nil check.
5. `lister.SlashCommandList()` — not ok (nothing reported) → refuse.
6. `turnbridge.MapEvent(list, turnbridge.TurnContext{ConversationID: string(conv.ID)})`
   — comma-ok false → refuse; then the assertion back to
   `protocol.SlashCommandListPayload` — not ok → refuse. Both are type-system
   arms (divergence 2 below), answered rather than reached.

Otherwise return the payload and true. `typ` is discarded, the twin's stated
reason: comparing it to `protocol.TypeSlashCommandList` would be a strictly
weaker second spelling of the assertion that already discriminates.

`TurnID` and `Seq` stay zero — the mapping's arm ignores both and the payload has
no field for either.

### The three divergences from the fourth twin

1. **Non-emptiness has a different source, and the twin's sentence about it is
   false here.** `resolveBoundModelList`'s doc leans on `turnevent.ModelList.Models`
   being documented "Never empty". `turnevent.SlashCommandList.Commands` carries
   no such type-level guarantee — it is documented nil for a zero-length list and
   defers the question to its producer. The conclusion still holds (the bool
   remains the only spelling of "nothing to send") for the **producer's** reason:
   streamsup's `emitSlashCommandList` suppresses the empty list (#1877), so
   nothing with zero entries ever reaches the retention.
   `sessionSlashCommandHold.SlashCommandList`'s doc states this in full — the doc
   here inherits **that** wording, not the twin's.
2. **`MapEvent`'s slash-command arm has no suppression branch** and maps even a
   zero-value `SlashCommandList`, returning true. Arm 6's two checks are
   therefore type-system arms, not states this daemon can reach. They still need
   an answer and a refusal is the only one that keeps the contract — not a panic,
   and no log. No fixture is built to reach them; the twin's tests do not either.
3. **`DroppedCommands` is a sum of two sources.** The mapping computes
   `e.DroppedCommands + (len(e.Commands) - len(commands))` — the decode's own
   drops plus whatever #2002's 64000-byte serialised bound cut from the tail. It
   rides through untouched: never recomputed from `len(Commands)`, never zeroed.
   The twin's `DroppedModels` rule with one more contributing term.

### Transfers verbatim from the twin

- **Value ownership.** The retention hands back a deep copy this function solely
  owns (`cloneSlashCommandList` is three levels deep) and the mapping allocates a
  fresh outer slice, so the payload owns its slices outright. No second clone.
- **The reported id is `conv.ID`**, from the resolved record, never reflected
  from the parameter. `conversations.Registry.Get` compares byte-exactly today,
  so no test can separate the two spellings; it is a deliberate provenance choice
  that keeps the id correct if the lookup ever loosens. Not a testable behaviour
  — the twin documents the same dead end and none is hunted for here.
- **`Get → guard → Lookup` duplication with the four siblings is accepted**, for
  `resolveBoundSession`'s reason: keeping each twin byte-stable beats folding
  them.

## Concurrency model

Spawns nothing, mutates nothing, joins nothing — a synchronous read on the
caller's goroutine. Three locks (the registry's, the pool's RLock, the hold's
leaf mutex) are acquired **sequentially and never nested**, so this adds no edge
to the daemon's lock order. The body must not be restructured so one lookup
happens inside another's scope.

The `Get → Lookup` window is the benign TOCTOU all four siblings share. A
rotation landing in it leaves the id either resolvable (we answer the inventory
of the session bound a moment ago) or not (we refuse), and both are correct: a
slash-command list is a property of one child's `initialize` reply, and a
rotation's fresh child reports its own. No re-read, no retry.

## Error handling

Every failure is the same value — the zero payload and `false`. There is no error
return and no partially-filled payload. `Pool.Lookup`'s error is **discarded**
rather than wrapped, `resolveBoundRunSettings`' stated reason: returning it bare
is what keeps a hostile or malformed id from being reflected into a log line or a
wire frame a caller builds from it.

Seven reject conditions across six branches: unknown conversation, empty
`convID` (lands in the same branch), empty `CurrentSessionID`, session absent
from the pool, runner without the method, nothing retained, and the two
unreachable type-system arms.

The function takes no `*slog.Logger` and must not grow one.

## Testing strategy

New file `cmd/pyry/session_slash_command_list_test.go`, mirroring the resolver
half of `session_model_list_test.go`. Fixtures: a `slashCommandListPlan`
(mutex-guarded, id-keyed answer table armed **after** `sessions.New` returns,
since the bootstrap id is unknowable until then), a `slashCommandListRunner`
embedding `stubRunner` plus the one asserted method, `newSlashCommandListTestPool`,
and a `sentinelSlashCommandList(tag)` whose every string carries `tag` in a
conspicuous `ZZ…ZZ` form — a natural value like `commit` would be a substring of
unrelated log text and would redden the AC 4 negative against a correct
implementation. Its second entry is deliberately awkward (nil `Aliases`, nil
`TruncatedFields`) so an allocation or a normalisation here is visible, and its
`DroppedCommands` is non-zero so a hard-coded `0` reddens.

Scenarios:

- **AC 1** — a conversation bound to a session holding a reported list resolves
  to a payload carrying that conversation's id and that session's commands.
  `ConversationID` and `DroppedCommands` asserted against literals, not against
  the fixture's own fields, so the two values a caller could plausibly re-derive
  are pinned to what was configured.
- **AC 2 (refusals), table-driven** — unknown conversation, unbound conversation,
  empty conversation id, binding naming a session the pool lacks. The bootstrap
  is armed with a distinguishably-tagged list in every row, which is what makes
  the isolation guard's mutant **sole-red**: delete the `CurrentSessionID == ""`
  clause and `Pool.Lookup("")` hands back the bootstrap, flipping the unbound and
  empty-id rows to true carrying the bootstrap's tag. An unarmed bootstrap would
  flip them to false and pin nothing.
- **AC 2 (nothing reported)** — separate from the table because resolution
  succeeds all the way down to the hold and refuses there; the sole red for a
  `list, _ := lister.SlashCommandList()` simplification.
- **AC 2 (runner without the method)** — `newRouterTestPool`'s plain `stubRunner`
  is the ready-made fixture; a refusal, not a panic.
- **AC 3** — two conversations bound to two different pool sessions each get
  their own session's inventory and never the sibling's, with the cross-answer
  stated as its own assertion rather than left implicit in two sentinel
  comparisons. Uses `runPoolReady` + `Pool.Create`; `t.Setenv` forbids
  `t.Parallel` here.
- **AC 4** — with `slog.Default` swapped for a buffer (pool built **before** the
  swap, so the pool's own diagnostics cannot land in it), one happy-path and one
  refused resolution write zero bytes of log, and neither a command name,
  argument hint, description, alias nor the untrusted conversation id appears in
  the capture. Honest scope: the function takes no logger, so the structural half
  of AC 4 is enforced by the signature; what this catches is a reach for the
  package-level `slog.Default()`. Empty strings are skipped in the containment
  loop — an empty needle makes the assertion vacuous.

`DroppedCommands` passthrough is asserted at AC 1 against the armed literal; the
mapping's own two-term arithmetic is `internal/turnbridge`'s to pin and is not
re-tabled here.

## Open questions

- Whether `assertPayloadCarries`' slash-command counterpart should compare
  `TruncatedFields` with `reflect.DeepEqual` (nil-vs-empty significant) or by
  length. Resolve during Phase B: the mapping's arm states that a nil
  `TruncatedFields` is load-bearing and must not be normalised, so `DeepEqual`
  is the expected answer — confirm against the payload's `MarshalJSON` exemption
  before settling.

## Revisions

**2026-09-02 (Phase B)** — the sole Open Question resolved as predicted, so no
design changed. `assertSlashCommandsCarry` compares both `Aliases` and
`TruncatedFields` with `reflect.DeepEqual`: `protocol.SlashCommand.MarshalJSON`
normalises `Aliases` but deliberately **exempts** `TruncatedFields`, so a nil
there has to survive the whole path, and `slices.Equal(nil, []string{})` reports
true — a length or `slices.Equal` comparison would carry the difference
invisibly. Recorded because the reason is stronger than the plan stated: the
exemption makes the choice mandatory for one of the two fields rather than
merely expected for both.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. There are two untrusted inputs and each has a
  named boundary. `convID` is network-origin and crosses at exactly one point —
  `conversations.Registry.Get` — as a lookup key and nothing else; it is never
  returned, never joined into a path, never wrapped into an error, and the
  reported id is taken from the resolved record (`conv.ID`) instead, so nothing
  a caller supplied is reflected back. The command strings are **workspace**-origin
  (lower trust than claude's own strings: whoever controls a repository controls
  them) and were bounded at construction by streamsup's four per-field caps plus
  its alias-count and entry caps, then bounded again on the frame axis by
  `maxSlashCommandListBytes`. They are bounded but **not sanitized**, and the
  render boundary owing that is the client's, as both types' SECURITY paragraphs
  assign. This function adds no sink for either.
- [Tokens, secrets, credentials] Not applicable by design: the function mints,
  reads and stores no credential. The only identifiers it touches are a
  conversation id and a session id, neither of which is a bearer secret; the
  session id is an internal hop and is never surfaced.
- [File operations] Not applicable by design: no path is constructed, opened,
  stat-ed or written. `convID` reaches no `filepath.Join`, `filepath.Match` or
  `os` call — the enumeration of sinks in the mapping's arm is unchanged by this
  slice.
- [Subprocess / external command execution] Not applicable by design. A slash
  command's `Name` is *not* an identifier — `__remote-workflow` is in the
  committed capture — and it reaches no `exec.Command` argument here. This
  resolver spawns nothing.
- [Cryptographic primitives] Not applicable by design: no randomness, no
  hashing, no comparison against a secret. The one equality this path performs is
  `Registry.Get`'s byte-exact id match on a non-secret identifier, where a
  constant-time compare would buy nothing.
- [Network & I/O] No findings. The function reads no socket and applies no bound
  of its own, deliberately: every content dimension was bounded by the producer
  and the frame axis by `maxSlashCommandListBytes` inside `MapEvent`, which is
  the one place both wire consumers (#2003's live-lane emission and this
  resolver) pass through. A second cap here would be a second place the limit is
  decided and the two could disagree silently — the specific mistake the
  mapping's arm names. The payload a caller receives is therefore already
  frame-bounded.
- [Error messages, logs, telemetry] No findings, enforced structurally. The
  function takes no logger and MUST NOT grow one; `Pool.Lookup`'s error is
  discarded rather than wrapped precisely so a hostile or malformed id cannot be
  reflected into a log line or an error string a caller builds a frame from. The
  only things a "why did it not resolve" line could add are the untrusted id or
  the workspace-authored command strings — exactly the channel the #833 posture
  closes, and the channel `sessionSlashCommandHold` closes on its own side by
  having no logger field. AC 4's test pins the reachable half (a reach for the
  package-level `slog.Default()`); the structural half is the signature.
- [Concurrency] No findings. Three locks acquired sequentially, never nested, so
  no new edge enters the daemon's lock order; the hold's mutex is a leaf and is
  never held across a channel send. No goroutine is spawned, so none can leak. The
  `Get → Lookup` TOCTOU is benign and analysed under Concurrency model above:
  both outcomes of a rotation landing in the window are correct answers. No
  shared state is mutated, so there is no check-then-mutate to hold a lock across
  and nothing to leave partial if the process is signalled mid-call. The deep
  copy the retention hands back means two concurrent readers never share a
  backing array.
- [Threat model alignment] The relevant threat is `docs/protocol-mobile.md`
  § Security model's untrusted-client-input case, addressed above: an id from the
  wire is a lookup key into the daemon's own registry and nothing more, and the
  #678 isolation requirement — one conversation never observing another's session
  state — is enforced by the empty-`CurrentSessionID` guard firing *before* the
  pool is touched, since `Pool.Lookup("")` returns the bootstrap session.
  Transport authentication, per-connection rate limiting and the client-side
  render boundary for workspace-authored strings are out of scope for this slice:
  the first two are the relay's and predate it, and the third is assigned to the
  client by both types' SECURITY paragraphs.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-02
