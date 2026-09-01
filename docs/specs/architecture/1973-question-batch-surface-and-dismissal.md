# #1973 — surface the question batch to interactive clients, dismiss it on every terminal path

## Files read

- `cmd/pyry/modal_resolve_v2.go` → `streamApprovalBridge`, `Surface`, `retire`, `broadcast`, `ResolveStream`, `ApprovalParked`, `ApprovalAnswerable`, `newStreamApprovalBridge` — the seam this slice branches; `broadcast` already takes the envelope type as a parameter, so the #607 capability gate, the shared timestamp and the envelope numbering come free.
- `internal/questionbridge/questionbridge.go` → `ToolName`, `Parse` — the discriminant and the bounded parse (#1965). `Parse` returns the zero payload on both negatives and never distinguishes them.
- `internal/questionbridge/registry.go` → `Record`, `Resolve`, `Lookup`, `Snapshot`, `cloneQuestions` — the nonce mint (#1975) and the one-shot consume that makes "exactly one broadcaster" structural.
- `internal/protocol/questions.go` → `QuestionShownPayload`, `Question`, `QuestionOption`, `QuestionDismissedPayload` — the two wire shapes. `QuestionDismissedPayload.Outcome` is forbidden from carrying a claude-authored option label; `Source` is a plain string whose vocabulary the producer owns.
- `internal/protocol/codes.go` → `TypeQuestionShown`, `TypeQuestionDismissed` — the two envelope types.
- `internal/permbridge/permbridge.go` → `AnswerableFunc`, `SetAnswerable`, `Register`, `expire` — #1912's re-arm. It asks `streamApprovalBridge.ApprovalAnswerable`, which is why a parked batch must be reachable from that report (see Design).
- `internal/control/server.go` → `approvalSurfacer` field doc, `SetApprovalSurfacer`, `handleApprove` — the three "as a modal_shown" comment sites, each now half the truth. `SetApprovalSurfacer`'s `func(permbridge.Request) func()` signature is frozen and stays frozen.
- `cmd/pyry/relay.go` → `startRelayV2Manager`'s `modalReg` mint and the `w.approvals != nil` bridge-wiring block — the single production site, and this file's precedent for setting an optional dependency after construction.
- `cmd/pyry/stream_approval_test.go` → `parkApproval`, `lastModalShown`, `bridgeLen`, `TestStreamApprovalBridge_Surface_BroadcastsPermissionModal`, `TestStreamApprovalBridge_Retire_TimeoutPathBroadcastsDismissal`, `TestStreamApproval_NoBodyLeakInLogs` — the harness #1080 proved itself with, and the shapes the question arm mirrors.
- `internal/audit/audit.go` → `Entry` — `ModalID` / `ModalClass` are the *modal* vocabulary; there is no question-shaped entry, which decides the no-audit question arm below.
- `docs/protocol-mobile.md` § Question, § `question_dismissed`, the application-message-types rows, the Changelog — the two bounded doc classes.
- `docs/knowledge/features/questionbridge-package.md` — the composite-literal drift trap in `Record` and the clone-on-read depth; both are the registry's, not this slice's, but they say what `Record` hands back.
- `CODING-STYLE.md` § Comments — symbol citations only, at any depth.

## Context

The question batch is modelled (#1962/#1963), captured (#1964), parsed (#1965), its dismissal frame declared (#1974) and its nonce mint built (#1975). Nothing emits either frame. `streamApprovalBridge.Surface` still hard-codes `tuidriver.ModalClassPermission` and uses `req.ToolName` as the prompt body, so claude's clarifying question reaches a client as a modal titled "Permission required" whose body is the tool's name and whose only answers are allow and deny.

This slice flips that: a question surfaces as `question_shown`, retires as `question_dismissed` on every no-answer terminal path, and a permission surfaces and retires exactly as it does today. Answering a question is #1907's; the connect-time reconcile is #1928's.

No ADR is warranted — this is a producer landing on an already-published wire contract.

## Design

### Two new fields on `streamApprovalBridge`

- `questions *questionbridge.Registry` — the daemon-singleton parked-batch store. Set **after construction** at the single production site in `cmd/pyry/relay.go`, the shape `modalResolverV2.activeConv` / `notifyBlocked` / `streamApprovals` and the bridge's own `toolCallInFlight` already use. The constructor's fifteen call sites are untouched. `nil` ⇒ every approval takes today's permission path, which is what leaves the fourteen test constructions, PTY mode and v1/foreground semantically unchanged.
- `byQuestion map[string]string` — `question_batch_id → tool_use_id`, made in the constructor beside `byModal` and guarded by the same `mu`.

**`byQuestion` is a second map rather than an extra key space inside `byModal`, and that is load-bearing.** `ResolveStream` treats *any* `byModal` hit as "this id is a stream approval" and resolves the parked completer to allow or deny. A batch id living there would mean a `modal_answer` naming a batch id could allow claude's `AskUserQuestion` call without anybody answering it. Today that path is gated one level up — `ResolveAnswer` looks the id up in `modalbridge` first, and a batch is never recorded there — but a separate map makes the property structural instead of a consequence of another registry's contents, and #1907's answer path needs its own map regardless.

### `Surface` — the discriminant branch

`Surface` gains one branch ahead of its existing body: when a question registry is wired and `questionbridge.Parse(req.ToolName, req.Input)` reports a well-formed in-bounds batch, delegate to `surfaceQuestion`; otherwise fall through to the unchanged permission path. A malformed or out-of-bounds `AskUserQuestion` input therefore still surfaces as a permission modal — `Parse` deliberately does not distinguish its two negatives, and the fail-closed permission prompt is the right degrade for a batch nobody can render.

```go
func (b *streamApprovalBridge) surfaceQuestion(batch protocol.QuestionShownPayload, toolUseID string) (retire func())
```

Behaviour: `questions.Record(batch, b.activeConv())` mints the nonce and stamps both daemon-asserted ids, returning the stamped copy; store `batchID → toolUseID` under `mu`; broadcast one `TypeQuestionShown` envelope carrying **the stamped payload**; return `func() { b.retireQuestion(batchID) }`. On the RNG error nothing is stored, nothing is broadcast, and the returned retire is a no-op — claude then times out to deny, mirroring `Surface`'s existing `modal.Record` degrade.

**The recorded batch and the broadcast batch are the same value.** `Parse` leaves both ids zero because both are daemon-asserted; `Record` is what stamps them on. Broadcasting the pre-record payload would ship an id-less frame no dismissal could correlate to.

### `retireQuestion` — the single no-answer arbiter

```go
func (b *streamApprovalBridge) retireQuestion(batchID string)
```

Two steps with separate arbiters, mirroring `retire`:

1. Unconditionally delete `byQuestion[batchID]` under `mu`. Sole deleter, runs on every terminal path, so the correlation never leaks.
2. `questions.Resolve(batchID)` — a miss means #1907's answer path already consumed and dismissed the batch, so return without broadcasting; a hit means a no-answer terminal path, so broadcast exactly one `TypeQuestionDismissed`. The registry's read-and-delete critical section is the single arbiter of the dismissal broadcast, which is what makes "exactly one broadcaster per outstanding question" structural rather than an agreement between this slice and #1907.

**No audit record on the question arm.** `audit.Entry` carries `ModalID` and `ModalClass` — the modal vocabulary. A batch has no class and its nonce is not a modal id, so writing one would put a question into the modal audit stream under a field name that lies. No acceptance criterion asks for one, and minting a question-shaped audit entry is a deliberate non-goal here.

### Dismissal vocabulary — one sentinel for the whole no-answer class

```go
const (
	outcomeQuestionUnanswered = "unanswered"
	sourceQuestionNoAnswer    = "no_answer"
)
```

Compile-time constants beside `reasonRemoteDeny`, so neither can ever carry a claude-authored option label — the rule `QuestionDismissedPayload.Outcome`'s doc states positively.

**`source` is not `timeout`, and settling that is one of this slice's doc obligations.** The retire closure the control server defers runs identically on all three no-answer terminal paths — the approval window elapsing, the caller disconnecting, the daemon shutting down — and carries nothing that tells them apart. Emitting `timeout` would name a cause that is wrong two paths out of three. `no_answer` states exactly what the producer knows, and a client reads an unrecognised `source` as *resolved, cause unknown, never as an answer*, which is the fail-closed reading § `question_dismissed` already publishes. § Question's carry-over table currently says `timeout` is "the one member a slice in flight will emit"; that row moves.

### The two liveness reports must see a parked question

`permbridge`'s `AnswerableFunc` is wired to `ApprovalAnswerable`, which scans `byModal`'s values for the approval id. A batch stored only in `byQuestion` would read as unanswerable, so #1912's re-arm would not fire and **every question would be denied at the first elapsed window** — the opposite of inheriting the extension. `ApprovalParked` has the same shape and the same reason: a question parks on a human exactly as a permission does.

Both therefore read both maps, through one new helper:

```go
func (b *streamApprovalBridge) parkedToolUseIDs() []string
```

Snapshot both maps under `mu`, release, then ask — the discipline both reports already document, and the reason `mu` stays a leaf lock. `ApprovalAnswerable`'s doc sentence claiming its scan "allocates nothing" moves with the change.

### Data flow

```
claude --AskUserQuestion--> mcp-approve --> control.handleApprove --> permbridge.Register
                                                     |
                                              approvalSurfacer
                                                     v
                                      streamApprovalBridge.Surface
                              questionbridge.Parse(ToolName, Input)
                                    |                        |
                               ok (question)            not a question
                                    v                        v
                     Registry.Record (mint + stamp)    modalbridge.Record
                     byQuestion[batchID]=toolUseID     byModal[modalID]=toolUseID
                     broadcast question_shown          broadcast modal_shown
                                    |                        |
                    <----------- Await returns (any terminal path) ----------->
                                    v                        v
                            retireQuestion(batchID)      retire(modalID)
                        Registry.Resolve one-shot     modalbridge.Resolve one-shot
                        broadcast question_dismissed  audit + modal_dismissed
```

## Concurrency model

No new goroutines. `Surface` / `surfaceQuestion` / `retireQuestion` run on the control-server handler goroutine, concurrent across approve requests; `ApprovalParked` / `ApprovalAnswerable` run on any goroutine but the relay `Run` one. `mu` stays a leaf lock guarding `byModal`, `byQuestion` and `nextID` only — held around O(1) map ops and the counter bump, never across `questions.Record`, `questions.Resolve` or a `Push`, so `bridge.mu → registry.mu` never nests. `questionbridge.Registry` carries its own leaf mutex. Shutdown is unchanged: `broadcast` returns early once the daemon ctx is cancelled, and `ActiveConns` returns nil after `Run` exits, so a shutdown-path retire drops its frame rather than blocking.

## Error handling

| Failure | Behaviour |
|---|---|
| `Parse` rejects (not the question tool, or out of bounds/malformed) | Fall through to today's permission modal — fail-closed, and the only prompt a client can render for a batch that did not parse. |
| `Registry.Record` RNG failure | Store nothing, broadcast nothing, return a no-op retire; log one content-free warn (`event` only). claude times out to deny. |
| `json.Marshal` of the payload fails | `broadcast`'s existing defensive branch: one content-free warn, no frame. |
| `Push` to a conn fails | `broadcast`'s existing tolerant loop: debug with the transport sentinel, `conn_id`, `env_id`. The client re-syncs on reconnect once #1928 lands. |
| `retireQuestion` on an already-resolved batch | `Resolve` misses ⇒ no second dismissal. |
| `retireQuestion` on a never-recorded batch | Delete of an absent key plus a `Resolve` miss ⇒ no broadcast, no panic. |
| `questions` registry nil (any test construction, PTY, v1) | Every approval takes the permission path, exactly as before this slice. |

**No log line on this path carries a byte of the parked input.** `questionbridge` does not import `log/slog` at all; the new call site logs only `event`, and `broadcast` logs only `event`, `conn_id`, `env_id`. The tool name is never logged either — today's permission path does not log it, and the question arm adds no field.

## Testing strategy

Extend `cmd/pyry/stream_approval_test.go`, the harness #1080 proved itself with. New helpers: `lastQuestionShown` (mirrors `lastModalShown`), `questionBridge` (a bridge with a wired `questionbridge.Registry`), and one in-contract batch fixture drawn from the committed capture's shape.

- **Surface broadcasts the batch** — exactly one `question_shown` and **no** `modal_shown`; the frame carries `activeConv()`'s conversation id, a non-empty `question_batch_id` that appears nowhere in claude's tool input, and the questions/options in claude's own order with `multi_select` intact; the batch is recorded in the registry under that id and correlated in `byQuestion`.
- **A question is not a permission modal** — the same surface records nothing in `modalbridge` and `byModal` stays empty, so `ResolveStream` reports `handled=false` for the batch id.
- **Retire broadcasts one dismissal** — exactly one `question_dismissed` naming the same batch id, `source`/`outcome` the two sentinels, the registry entry consumed and `byQuestion` empty.
- **Retire after an answer-path consume broadcasts nothing** — pre-`Resolve` the batch, then retire: no frame, no leak. The single-arbiter property.
- **No leak on the malformed-batch fallback** — an `AskUserQuestion` call whose input fails the parse surfaces one `modal_shown` and no `question_shown`.
- **Permission approvals are unchanged** — the existing permission tests construct the bridge with a nil question registry and must stay green untouched; one table row additionally asserts a wired registry leaves a `Bash` approval byte-identical.
- **No body leak** — a batch whose question text, header, option label and description are unique sentinels, pushed through Surface → retire with a failing `Push`, must leave none of the four in the log buffer.
- **Liveness reports see a parked question** — `ApprovalAnswerable(toolUseID)` is true while the batch is parked and false after retire; `ApprovalParked(conv)` likewise via the tracker fixture.

RED first: every assertion above fails against the current `Surface`, which broadcasts a `modal_shown` for an `AskUserQuestion` request.

Gate: `go test -race ./cmd/pyry/... ./internal/questionbridge/... ./internal/control/...`, `go vet ./...`, `go build ./cmd/pyry`.

## Doc obligations

`docs/protocol-mobile.md`, two bounded classes, swept by reading whole paragraphs:

- **Class 1 (prose falsified by this slice)** — § Question's opening "wire vocabulary only" claim; § Question's "Nothing emits this frame yet" paragraph; § `question_dismissed`'s "so nothing emits it today"; the `source` carry-over table's `timeout` row; and both application-message-types rows. Plus the three `internal/control/server.go` comment sites, each now half the truth.
- **Class 2 (bounds prose written while #1965 was unmerged)** — the **Contract bounds** headline and its first two rows flip to enforced, stated as explicit count checks *inside* the parse taken *after* the decode, one over the batch and one per question, with a failing batch rejected whole. The string-maximum row keeps its no-number posture and gains the reason: there is still no per-string bound, and the one cap that landed is a single pre-decode bound in bytes over the whole raw tool input — a different thing, and not client-observable either way. The **header** row stays exactly as written; only the surrounding paragraph's future tense about what #1965 *will* pick is settled.

Left alone deliberately: the changelog entries at the foot of the file (historical, the convention #1974 followed), and `cmd/pyry/relay_guard_test.go`'s two entries, whose classification is emission-independent and whose "the producer is #1973" reads true the moment this lands. One new short changelog entry is added — a paragraph naming the frames now emitted, the terminal paths and the arbiter, deliberately not at #1974's or #1964's length.

## Open questions

1. **Does the question arm need an audit record?** Resolved in Design: no — `audit.Entry` is modal-shaped and no AC asks for one.
2. **Which `source` value?** Resolved in Design: one sentinel, `no_answer`, because the arbiter cannot distinguish the three no-answer paths.
3. **Where does the registry come from?** Resolved: minted in `cmd/pyry/relay.go` beside `modalReg` and assigned after construction, which keeps the constructor's fifteen call sites untouched and leaves the registry reachable before `mgr` for #1928's connect-time reconcile.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings — the boundary is a single function and the design cannot route around it.** `questionbridge.Parse` is the one place claude's tool input becomes a wire payload, and `surfaceQuestion` is reachable only from a `Parse` that returned `ok`, so no unbounded or malformed batch can reach a client. Both ids are daemon-asserted at the same boundary: `Parse` fills neither and `Registry.Record` overwrites whatever arrived on the payload, so no claude-supplied value can become a routing key or a nonce. The one way to break this is to broadcast `Parse`'s payload instead of `Record`'s return value — an id-less frame no dismissal can correlate to, which presents as a client rendering a panel it can never clear. The plan names `Record`'s return as the broadcast value for that reason.
- **[Trust boundaries] No findings — the confused-deputy path is closed structurally.** `ResolveStream` treats any `byModal` hit as "resolve this parked completer", so a batch id in that map would let a `modal_answer` allow claude's `AskUserQuestion` call with nobody having answered it. `byQuestion` being a separate map makes that unreachable regardless of what `modalbridge` happens to hold.
- **[Tokens] No findings.** `question_batch_id` is a `crypto/rand` UUIDv4 (`newQuestionBatchID`, #1975) — 122 bits, in-memory only, minted at `Record` and revoked by `Resolve`'s one-shot. It is not a bearer credential: an inbound answer is re-resolved server-side against the registry, so possession grants nothing beyond the interactive capability the holder already has — `modal_id`'s posture exactly, which is why logging it would be no worse than today's `"modal_id", modalID` fields. The registry carries no TTL by design, and this slice is what bounds it: `handleApprove` defers the retire closure on every `Await` return, so an outstanding batch always corresponds to a control-socket connection blocked on a human — the same bound `byModal` has.
- **[Tokens] No findings — a batch nobody has seen does not get its deadline extended.** When `Record` fails on RNG, nothing is stored in `byQuestion`, so `ApprovalAnswerable` reports false and permbridge denies within one window. That is the fail-closed direction and mirrors `Surface`'s existing `modal.Record` degrade.
- **[File operations] Not applicable by design** — this slice opens, creates and names no path. The batch lives in memory for the life of one parked approval and is never persisted.
- **[Subprocess execution] Not applicable by design** — no `exec.Command`, no environment handling, no signal work. The subprocess is the source of the untrusted data, which is category 1's finding, not this one's.
- **[Cryptographic primitives] No findings.** The only primitive is the nonce, and it is `crypto/rand`, not `math/rand`. This slice performs no comparison of an attacker-controlled value against a secret — the batch id is a map key on the outbound path only. Constant-time comparison is #1907's question, when an inbound answer first carries a client-supplied id back.
- **[Network & I/O] No findings — measured, not assumed.** `Parse` caps the raw tool input at `maxInputBytes` (16 KiB) *before* the decode, which is what keeps an un-droppable control frame non-inflatable. Against the two downstream bounds: `transport`'s `maxFrameBytes` is 1 MiB and the v2 push queue's `pushQueueByteCeiling` is 32 MiB, so an in-contract batch cannot approach either. No timeout or connection-cap surface is touched.
- **[Error messages, logs, telemetry] SHOULD FIX — the no-body-leak test needs four sentinels, not one.** The existing `TestStreamApproval_NoBodyLeakInLogs` uses one sentinel for the tool name and one for the whole input. On the question arm the input *contains* all four claude-authored strings, so a single input-wide sentinel would pass while a log line echoed only the header, only a label, or only a description. Phase B: give the question fixture four distinct sentinels — question text, header, option label, option description — and assert each is absent from the log buffer independently.
- **[Error messages, logs, telemetry] No findings otherwise.** `questionbridge` does not import `log/slog` at all, so nothing can leak from the parse. The three new event strings are compile-time constants; the new warn carries `event` only, and `broadcast` carries `event`, `conn_id`, `env_id`. The tool name is not logged on this path today and the question arm adds no field.
- **[Concurrency] No findings.** `mu` stays a leaf lock: it is never held across `questions.Record`, `questions.Resolve` or a `Push`, so `bridge.mu → registry.mu` never nests. `retireQuestion` deletes the correlation *before* `Resolve`, matching `retire`'s order, so the window between the two reads "not parked" — the fail-closed direction for a deadline — rather than "parked but already gone". No goroutine is spawned, and teardown is `broadcast`'s existing early return on a cancelled daemon ctx.
- **[Concurrency] No findings — no double-surface race.** `permbridge.Register` rejects a duplicate live `tool_use_id` and `handleApprove` returns before reaching the surfacer, so two `Surface` calls can never park two batches for one approval.
- **[Threat model] SHOULD FIX — this slice is the first to actually land threat 1 on this family, and the dismissal frame is where it would spread.** § Security model's threat 1 (prompt injection, `severity: high`, `mitigation: partial`) reaches a client render surface here for the first time: before this slice a question's body on the wire was the tool's name. The exposure itself is the published, precedented posture — `question_shown`'s four strings are claude-authored, rendered as inert text, sanitized by the client, exactly `model_list`'s tier, which has shipped since #1849 — so no daemon-side sanitizer is added, and adding one would contradict § Question's SECURITY paragraph. What must not spread is the trust tier: `QuestionDismissedPayload` carries no claude-authored byte, and the natural implementation of a dismissal reaches for the chosen option's `label`, because options carry no id. Phase B: both dismissal values are compile-time `const` beside `reasonRemoteDeny`, never derived from the batch, and the leak test asserts none of the four sentinels appears in an emitted `question_dismissed` payload — so this slice sets no precedent #1907 can follow into the wrong tier.
- **[Threat model] OUT OF SCOPE — the inbound half.** Answering a question crosses the boundary in the other direction and is #1907's; the connect-time reconcile that re-scopes an outstanding batch to a reconnecting client is #1928's. Neither is touched here, and this slice grants no inbound capability.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-01
