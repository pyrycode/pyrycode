# 2209 — fan a `workspace_updated` out to every other connected interactive client

## Files read

- `cmd/pyry/conversation_update_v2.go` → `conversationUpdateEmitterV2`, `newConversationUpdateEmitterV2`, `announce` — the emitter this slice copies: leaf mutex around a per-emitter envelope-ID counter, one shared timestamp per announcement, the `if !c.Interactive { continue }` gate, a push loop that tolerates a torn-down conn, and an early return on `ctx.Err()`.
- `cmd/pyry/conversation_update_v2_test.go` → its seven test funcs — the case list and the never-log assertion shape the new emitter's test mirrors.
- `cmd/pyry/interactive_turn_v2.go` → `interactiveBroadcaster` — the two-method seam (`ActiveConns`/`Push`) the emitter consumes; `*relay.V2SessionManager` satisfies it.
- `cmd/pyry/interactive_turn_v2_test.go` → `fakeInteractiveBcast`, `recordedPush` — the existing test double. Reused verbatim; this slice adds no new fake.
- `cmd/pyry/relay.go` → `startRelayV2` — the `relay.V2SessionConfig` literal holding the handler table, and the post-construction assignment window after `NewV2SessionManager` returns and before `mgr.Run`'s goroutine starts (`announce`, `announceConversation`, `modalResolver.streamApprovals`, `bridge.questions` are all written there).
- `cmd/pyry/relay_guard_test.go` → `excludedTypes` — the `"TypeWorkspaceUpdated"` entry, whose comment already names this ticket as the second producer and states nothing goes red when it lands.
- `internal/relay/handlers/rename_workspace.go` → `RenameWorkspace`, `WorkspaceLabeler` — the call site of the fan-out and the doc paragraph that goes false.
- `internal/relay/handlers/rename_workspace_test.go` → `newRenameWsConn`, `newRenameWsReg` — the harness the new cases extend; the sole non-production call site of the changed signature.
- `internal/relay/v2session.go` → `ActiveConn`, `ActiveConns` — the snapshot type (`ConnID`, `Interactive`) and the doc comment whose "any goroutine other than the dispatch goroutine" wording the ticket body corrects: a map-dispatched verb runs on `appFrameWorker`, off `Run`.
- `internal/protocol/workspace.go` → `WorkspaceUpdatedPayload`, `RenameWorkspacePayload` — the announced record, and the doc paragraph that also goes false.
- `docs/protocol-mobile.md` § Renaming a workspace, and the v2 type table's `workspace_updated` and `conversation_updated` rows — the prose to replace and the two-producer row to copy.
- `docs/knowledge/features/v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md` § `ActiveConns`/`ActiveConn` — records that `if !c.Interactive { continue }` is the load-bearing gate in all four existing emitters, and that `interactive` is the only capability that gates a fan-out; `question` and `model_list` are detection-only.

## Context

`rename_workspace` (#2207, merged) replies to its requester and to nobody else. Every other connected client keeps showing the old label until it happens to re-list, which nothing prompts. This slice pushes the same record, unsolicited, to the other interactive conns — the point of storing the label in the daemon rather than in a client.

#2156 built this exact shape for `conversation_updated` on a host-side create. Two things differ. First, that announcement has **no requester at all** (a control-socket verb), so it fans to everyone; this one has one, and must exclude it — the requester already has its correlated reply, and a second copy would arrive as an unsolicited duplicate. Second, #2156's hook had to reach the composition root, so `startRelayV2` returns it; this hook never leaves `startRelayV2`, so it is a local variable and the function's signature is unchanged.

No ADR is warranted: the emitter shape, the capability gate and the dual-classification convention are all established, and this slice adds the fifth instance of each rather than a new decision.

**Sizing note — the total-written-work line is at its ceiling and may cross it.** Counted against this plan: 4 production source files, 2 call sites for the changed signature, 1 new exported type, 5 acceptance criteria, no new reject branches — every one of those comfortably inside the boundary. Total written work estimates at ~800 lines including this document, with the uncertainty straddling the line. It is built as one ticket anyway, and the reason is the floor rather than an argument that the count is really smaller: the only seam this slice has is emitter / wiring, and an emitter nothing calls is the "ships unwired" shape — a child that no sibling outside the family consumes and that cannot be verified on its own. The floor beats the ceiling, so the overage is stated here rather than traded for a slice that could not be checked.

## Design

### The emitter — `cmd/pyry/workspace_update_v2.go` (new)

```go
type workspaceUpdateEmitterV2 struct {
    bcast  interactiveBroadcaster
    ctx    context.Context
    logger *slog.Logger

    mu     sync.Mutex // leaf lock, guards nextID and nothing else
    nextID uint64
}

func newWorkspaceUpdateEmitterV2(bcast interactiveBroadcaster, ctx context.Context, logger *slog.Logger) *workspaceUpdateEmitterV2

func (e *workspaceUpdateEmitterV2) announce(p protocol.WorkspaceUpdatedPayload, excludeConnID string)
```

`announce` marshals `p` once, takes one `time.Now().UTC()` for the whole fan-out, then walks `ActiveConns(e.ctx)`, skipping a conn when `c.ConnID == excludeConnID` **or** `!c.Interactive`, and `Push`es an envelope with a monotonic per-emitter id, `protocol.TypeWorkspaceUpdated`, the shared timestamp and no `InReplyTo`. A push error returns early when `ctx.Err() != nil` (teardown) and is otherwise logged and skipped. It returns nothing.

**Exclusion is by conn id, not by device.** `excludeConnID` is compared against `ActiveConn.ConnID` — the same string space the handler reads from `dispatch.Conn.ConnID()`, so no mapping exists to get wrong. A requester holding a second conn is pushed on that one, which is the correct behaviour and is pinned by a test rather than left to be discovered.

**The exclusion is checked before the interactive gate**, and the order is immaterial to the outcome — a conn excluded by either test is skipped — but it reads as "not the requester, and interactive" which is the sentence the AC states.

**Envelope ids are minted only for conns that are actually pushed.** Bumping the counter inside the skip arms would leave gaps that mean nothing; the counter's contract is "strictly increasing across this emitter's frames", which holds either way, but minting per push keeps the id count equal to the push count and makes the monotonic assertion readable.

### The handler's new parameter — `internal/relay/handlers/rename_workspace.go`

```go
// WorkspaceAnnouncer fans a workspace_updated record to every connected
// interactive client except the one named by excludeConnID. Returns nothing.
type WorkspaceAnnouncer func(p protocol.WorkspaceUpdatedPayload, excludeConnID string)

func RenameWorkspace(reg WorkspaceLabeler, registryPath string, announce WorkspaceAnnouncer, logger *slog.Logger) dispatch.Handler
```

The one new exported type, and named rather than inline so the exclusion contract has a doc-comment home the call sites can be read against. `logger` stays last, matching every sibling handler.

The success arm hoists the payload into a variable so the reply and the announcement carry one value, then:

1. `reg.SetWorkspaceLabel` (unchanged)
2. eager `reg.Save` (unchanged)
3. marshal, log `rename_workspace.applied` (unchanged)
4. `err := c.Reply(...)`
5. `if announce != nil { announce(payload, c.ConnID()) }`
6. `return err`

**The fan-out runs after the reply and its return value is the reply's error, unchanged.** A `nil` announce skips step 5 and leaves every other line identical, which is how a caller with no relay leg behaves.

**The announcement fires even when the reply errored.** A failed reply means the requester's own conn went away between the store and the ack; the store still landed, so the other clients' view is still stale and still needs correcting. Suppressing the fan-out on that branch would make one client's disconnect silently withhold the update from every other client.

Every reject branch returns before step 5, so nothing is announced when nothing was stored — the same structural property the existing comment claims for the store.

### The wiring — `cmd/pyry/relay.go`

The handler table is a field of the `relay.V2SessionConfig` literal passed to `relay.NewV2SessionManager`, so at the `TypeRenameWorkspace` entry there is no manager in scope to close over. A `var announceWorkspace func(protocol.WorkspaceUpdatedPayload, string)` is declared ahead of the literal; the handler is handed a closure that reads it and returns early when it is nil; and the real emitter's `announce` is assigned into it inside the post-construction window `startRelayV2` already uses four times — after `NewV2SessionManager` returns, before `mgr.Run`'s goroutine starts.

Unlike `announce` and `announceConversation`, this hook is **not** a named return value: nothing outside `startRelayV2` invokes it, so `startRelayV2`'s and `startRelay`'s signatures and their `return` statements are untouched.

## Concurrency model

No goroutine is created or ended by this slice.

- **The write to `announceWorkspace`** happens on the `startRelayV2` goroutine, before `mgr.Run`'s goroutine is started. Every reader is a conn's `appFrameWorker`, created by machinery that goroutine starts, so the goroutine-creation edge orders the write before every read. This is the same argument the four existing post-construction assignments rest on, and it is why no lock is needed.
- **`announce` runs on the requester conn's `appFrameWorker` goroutine**, not on `Run`. A `rename_workspace` frame is map-dispatched, so `dispatchAppFrame` falls through to `enqueueAppFrame`; `ActiveConns` funnels onto `Run` through the manager's snapshot channel and `Run` is free to service it while the handler waits. There is no deadlock here and no hand-off channel is built, unlike `queueStateEmitterV2` and `sessionErrorEmitterV2`, whose seams fire from a mix of goroutines including `Run` itself.
- **`e.mu` is a leaf lock** taken around the counter bump alone — never across `ActiveConns` or a `Push` — so it cannot nest inside the manager's own `pushMu` and there is no lock order to reason about. It is load-bearing rather than copied: two paired clients can each rename concurrently, each on its own `appFrameWorker`, through one emitter.
- **Shutdown:** on daemon-context cancellation `ActiveConns` answers empty and a racing `Push` returns its error, on which the loop returns immediately. A late announcement fans out to nobody rather than blocking teardown.

## Error handling

| Failure | Handling |
|---|---|
| `announce` hook nil | Handler skips the call; store, persist, reply and returned error unchanged. |
| Payload will not marshal | Defensive only — a closed struct of a string and a `*string`. Logged at Warn with `event` and the requester's conn id; the payload and `err.Error()` are never echoed (`encoding/json` quotes offending input into its error, and one of these fields is a host path). Returns without pushing. |
| `Push` returns `relay.ErrConnNotFound` | The conn closed between the snapshot and the push. Logged at Debug and skipped; iteration continues to the remaining conns. No re-sync path — a client that missed it re-lists on its next connect. |
| `Push` returns any other error, daemon ctx alive | Same Debug branch. One client's failure never affects another's delivery. |
| Daemon ctx cancelled | Returns immediately without logging: a daemon going away is not a dropped push. |
| `c.Reply` errors | Its error is still what the handler returns; the fan-out has already run and does not change it. |

## Testing strategy

New `cmd/pyry/workspace_update_v2_test.go`, over the existing `fakeInteractiveBcast` (no new double):

- **Exclusion and the interactive gate**, table-driven: the requester's conn is skipped; a non-interactive conn is skipped; a mixed set delivers only to the interactive non-requesters; an empty `excludeConnID` delivers to everyone; **a requester holding a second conn is pushed on that second conn** — exclusion is per conn, not per device; no conns at all.
- **No `in_reply_to`** on the pushed envelope — what makes this a push rather than a second reply.
- **One shared timestamp per announcement, strictly increasing envelope ids** across two announcements to two conns.
- **A cleared label fans out as a JSON `null`**, asserted on the raw pushed bytes rather than only on a decoded `*string`, so an `omitempty` regression that drops the key is caught.
- **A torn-down conn is logged and skipped**, the next conn still receives its copy, and the log names the transport sentinel.
- **Teardown returns early** — the second conn gets no push at all, and no push-error line is logged.
- **No branch logs the path or the label**, over all four push outcomes, with a fragment check as well as a whole-value check.

Extending `internal/relay/handlers/rename_workspace_test.go`:

- A successful rename calls `announce` **exactly once**, with the payload projected from the matched row and with the requester's own conn id as the exclusion key.
- The announcement carries the same `Path` and `Label` the reply does, including the nil `Label` of a clear.
- **Ordering:** the recorded call observes the reply already on the conn's outbound channel, pinning "fan out after the correlated reply" deterministically.
- **Every reject branch announces nothing** — malformed, blank label, over-bound label, not found.
- A **nil announce** leaves the store, the persist, the reply and the returned error exactly as they are.

Verification per § B2: `go test -race ./cmd/pyry/... ./internal/relay/... ./internal/protocol/...`, `go vet ./...`, `go build ./cmd/pyry`.

## Docs and classification changes

- `cmd/pyry/relay_guard_test.go`'s `excludedTypes`: `"TypeWorkspaceUpdated"` moves from `"reply"` to `"reply+push"`, with prose matching `TypeConversationUpdated`'s dual entry. Confirmed rather than assumed: no assertion parses either map's values, so nothing goes red on the change itself.
- `docs/protocol-mobile.md`: the § Renaming a workspace paragraph beginning "**This reply goes to the requester only.**" is **replaced** (not supplemented) by the fan-out description; the v2 type table's `workspace_updated` row gains the two-kinds-of-producer language its `conversation_updated` neighbour already carries.
- `internal/relay/handlers/rename_workspace.go`: `RenameWorkspace`'s "It replies to the REQUESTER ONLY" paragraph is rewritten rather than left standing beside the new parameter.
- `internal/protocol/workspace.go`: `WorkspaceUpdatedPayload`'s closing paragraph makes the same false claim and is rewritten the same way.

## Open questions

1. **Does the announcement fire when `c.Reply` returns an error?** Resolved in Design: yes. The store landed, so the other clients are stale regardless of the requester conn's health.
2. **Named `WorkspaceAnnouncer` type or an inline func parameter?** Resolved: named, so the exclusion contract has one doc-comment home. It is the slice's only new exported type.
3. **Does the guard map's `TypeWorkspaceUpdated` entry need an assertion change?** To confirm during Phase B by running the guard test after the value edit; the entry's own comment predicts green.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** The boundary is `RenameWorkspace`'s `json.Unmarshal` of `RenameWorkspacePayload`, unchanged by this slice, and the record that crosses outward is built where it already was. The one new property is a widening of the audience: an operator-supplied opaque string that reached exactly one authenticated conn now reaches every authenticated interactive conn. That is not a boundary crossing — every recipient is inside the same paired-Noise-session trust boundary the requester is, holding a device token this daemon issued, and each could already read the same label back through `list_conversations` (#2208). No finding. What would be one, and is structurally prevented: the announced `Path` is projected from `matched` — the row's own stored `Cwd` — not from `p.Path`, so no request byte reaches the fan-out on any branch. The emitter re-marshals the same typed struct rather than accepting caller-supplied bytes, so there is no route for an unvalidated encoding to reach the wire.
- **[Tokens, secrets, credentials]** Not applicable by construction: the emitter's only state is a `uint64` counter and a `context.Context`, and its payload is a path and a label. It reads no device registry, no static key, and no token. The envelope-id counter is not a secret and is not required to be unguessable — every existing v2 emitter numbers its own frames from 1 and clients order on it.
- **[File operations]** No finding: this slice performs no filesystem operation at all. `Save` is untouched, still takes the daemon-derived `registryPath`, and still runs before the fan-out, so no ordering change puts a client byte near a path. The `Path` field is carried as an opaque string — never resolved, joined, stat-ed, or opened — exactly as the handler already documents for its lookup key.
- **[Subprocess execution]** Not applicable — no `exec.Command`, no argv, no environment. The label reaches no process boundary.
- **[Cryptographic primitives]** Not applicable directly: the emitter hands plaintext envelopes to `V2SessionManager.Push`, which seals each under that session's own Noise send state. Nothing here selects a primitive, a key or a nonce, and one payload marshalled once and pushed to N conns is sealed N times under N distinct session states — no key or nonce is reused across recipients as a consequence of sharing the plaintext.
- **[Network & I/O]** The one real question in this category is unbounded amplification: one inbound frame now produces N outbound frames. It is bounded on both factors. N is the count of open, token-validated, interactive sessions — `ActiveConns` excludes `V2StateAwaitingInit` and `V2StateHandshakeComplete`, so an unauthenticated peer cannot inflate it. The payload is bounded by `protocol.MaxWorkspaceLabelBytes` (128, enforced before the store) plus a stored `Cwd`. `Push` enqueues without blocking, so a wedged phone cannot hold the verb open and cannot slow the fan-out to the others. No inbound rate limit is added and none is claimed: the trigger is an authenticated paired client, and refusing it would be a new policy this slice has no mandate for.
- **[Error messages, logs, telemetry]** The strongest finding in the pass, and it shapes the design rather than being deferred. `RenameWorkspace` logs `event` and `conn_id` and nothing else — deliberately stricter than `rename_conversation` — and an emitter copied from `conversationUpdateEmitterV2` would inherit a `conversation_id` field that has **no safe analogue here**. This payload has no daemon-minted identifier at all: `Path` is a host filesystem location and `Label` is operator content. So neither field is logged on any branch; the two log lines name `event`, the daemon-minted conn ids and `env_id`, plus the transport `err` on the push branch. `err` is a transport sentinel from `Push`, not an `encoding/json` error, so it carries no payload bytes. The marshal branch logs no `err` for the opposite reason. This is pinned by a test asserting all four branches over a distinctive path and label, plus a shared fragment, and it is why the never-log assertion is a whole test rather than a line in another.
- **[Concurrency]** Two findings, both addressed in the Concurrency model. (a) `e.mu` is a leaf lock held across the counter bump only, never across `ActiveConns` or `Push`, so it cannot nest inside the manager's `pushMu` and no lock order exists to get wrong. (b) The `announceWorkspace` hook is a plain variable read from `appFrameWorker` goroutines and written on the `startRelayV2` goroutine; the write is ordered before every read by `mgr.Run`'s goroutine creation, the same edge the four existing post-construction assignments rely on. A read racing the write is unreachable because no frame can dispatch before `mgr.Run` starts. No goroutine is created, so none can leak. No shared mutable state is introduced: the `*string` label is read-only after the handler builds it, and the marshalled bytes are shared across pushes but never written.
- **[Threat model alignment]** `docs/protocol-mobile.md` § Security model's relevant threat is a paired client receiving data it should not. Every recipient here is a paired, token-validated, interactive conn, and the frame's content is already readable by each of them through `list_conversations`. The unpaired and mid-handshake cases are excluded by `ActiveConns`. **Out of scope and named:** per-conn authorisation — there is no notion of a client authorised for one workspace and not another anywhere in this protocol, so this slice does not invent one; if that distinction is ever introduced, this fan-out is one of the sites that must gain a filter, alongside `list_conversations` and the four `conversation_updated` producers.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-07

## Revisions

### 2026-09-07 — implementation

The design landed as planned; the two entries below record where the plan's own claims moved.

**Sizing: the total-written-work estimate was low by about a quarter.** The plan predicted ~800 lines including itself and flagged the line as at its ceiling. Measured: 839 insertions across 8 files plus a 173-line plan, ~1010 in total. The two tests are where it went — the emitter's at 358 against ~290 projected, the handler's additions at 196 against ~130 — because both are table-driven over cases the acceptance criteria name individually, and each row costs its comment as well as its data. Every other line of the boundary held with room: 4 production source files, 2 call sites for the changed signature, 1 new exported type, 5 acceptance criteria, no new reject branches. The floor argument in § Context is unchanged and is why this shipped as one ticket rather than being re-split at implementation time: the only seam available is emitter / wiring, and an emitter nothing calls cannot be verified on its own. Recorded here rather than argued away — a builder sizing the next slice of this family should read ~1000, not ~625, for a copy-an-emitter-and-wire-it ticket with per-AC test tables.

**Open questions, resolved.**

1. *Does the announcement fire when `c.Reply` returns an error?* Yes, as designed. The reply's error is captured into `replyErr`, the fan-out runs, and `replyErr` is returned unchanged — so the handler's contract with the dispatcher is byte-identical to before this slice.
2. *Named `WorkspaceAnnouncer` type or an inline func parameter?* Named, as designed. It carries the exclusion contract, the per-conn-not-per-device rule and the nil-means-no-fan-out rule in one doc comment that both call sites can be read against.
3. *Does the guard map's `TypeWorkspaceUpdated` entry need an assertion change?* No — confirmed rather than assumed. The value moved from `"reply"` to `"reply+push"` and the entry moved out of the reply block to sit beside `TypeConversationUpdated`; `go test ./cmd/pyry/` stayed green through the edit, exactly as #2207's own comment predicted.
