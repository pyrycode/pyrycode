# #2430 — hand a waiting caller the context reading it asked for

Adds `QueryContextUsage` to `internal/streamsup`'s `Runner`: an await-shaped peer to
the existing fire-and-forget `RequestContextUsage`, correlating the child's reply back
to the exact caller that asked for it. `QueryMCPStatus` is the shape being copied.

## Files read

- `internal/streamsup/runner.go` → `QueryMCPStatus` — the await-shaped template: snapshot
  the binding under the leaf mutex, register outside it, re-check the generation, write,
  wait. Every step of that order is reused verbatim.
- `internal/streamsup/runner.go` → `actuateMCP` — the second instance of that same order,
  which is what establishes it as the package's pattern rather than one method's quirk.
  Its doc block also records the contract this slice inherits: the caller's context is the
  only bound on a silent child.
- `internal/streamsup/runner.go` → `RequestContextUsage` — the fire-and-forget peer that
  stays unchanged. Its `pendingContextUsageRequest` resolves the *write* outcome only.
- `internal/streamsup/runner.go` → `mcpActuationIDPrefix` — the doc block stating why
  prefix disjointness is load-bearing and that a collision costs a hang, not a miss.
- `internal/streamsup/runner.go` → `takeStdin`, `setStdin`, `mcpStatusEligible` — the
  child-binding boundary. `mcpStatusEligible` is the part of `QueryMCPStatus`'s gate that
  does **not** carry over; `rotating` and `childGeneration` are the parts that do.
- `internal/streamsup/parser.go` → `mcpStatusQueries`, `pendingMCPStatusQuery`,
  `claimMCPStatusQuery` — the correlator being mirrored, including the `written()` gate
  and the claim-an-unregistered-id-in-my-namespace rule.
- `internal/streamsup/parser.go` → `emitContextUsage` — the shared-sink path whose payload
  decoding this slice extracts so both consumers read one decoder.
- `internal/streamsup/parser.go` → `beginMCPStatusChild` — the child-replacement boundary
  where the existing correlators retire their waiters.
- `internal/streamsup/parser.go` → `contextUsageRequests`, `contextUsageResponseIDLine` —
  the existing write-outcome correlator and the narrow id-only decode target, both reused.
- `internal/streamsup/envelope.go` → `contextUsageDetailAllowed`, `WriteContextUsage`,
  `ErrUnsupportedContextUsageDetail` — the settled detail vocabulary and its refusal
  contract. This slice does not widen it.
- `internal/e2e/internal/fakeclaude/main.go` → `contextUsageRequestID`,
  `writeContextUsageAck` — the fake child's existing arm. It keys on subtype and detail
  only and echoes whatever id it was handed, so it already answers a query-minted id.
- `internal/streamsup/mcp_status_query_test.go` → `mcpStatusQueryRunner`,
  `queryMCPStatusAsync`, `awaitMCPStatusRequest` — the test harness shape to mirror.
- `docs/knowledge/features/streamsup-package.md` § Public API, § Sections — the overview's
  map; the control-request family and its per-child boundaries.

## Context

`RequestContextUsage` writes the request and returns only whether the write succeeded
(#2352). The reading itself lands later on the shared event sink, where the automatic
post-turn `summary` (#2353) publishes too (#2371). A caller holding an inbound request to
answer cannot correlate off that lane: the next `context_usage` frame for a conversation
may be the cadence's, not its own.

`QueryMCPStatus` already solved exactly this for MCP status, and its fire-and-forget peer
`RequestMCPStatus` survived beside it untouched. The same pairing is what this adds.

No caller is wired in this slice. `turnEndContextUsageRequester` in `cmd/pyry` keeps
calling the fire-and-forget method, and no interface gains a method — `QueryContextUsage`
lands on the concrete runner only, as `ReconnectMCPServer` did.

**No ADR is warranted.** The correlation design is not a new boundary; it is the third
instance of one `QueryMCPStatus` established and `actuateMCP` confirmed.

## Design

Three pieces, in `internal/streamsup` only.

### 1. `decodeContextUsage` — one decoder, two consumers

`emitContextUsage` today does id-retirement, write-gate, payload decode, bounding and
emit in one body. The payload half moves out:

```go
// decodeContextUsage decodes one control_response into a bounded reading. It reads no
// request id: correlation is the caller's, exactly as decodeMCPStatus splits it.
func decodeContextUsage(line []byte) (turnevent.ContextUsage, bool)
```

Behaviour is preserved exactly: the subtype gate, the nil-payload gate, the
`maxContextUsageStringBytes` model-length gate and all three `boundContextUsageEntries`
calls move across unchanged, and each existing rejection becomes `(zero, false)`.
`emitContextUsage` keeps its id-take, its `written()` gate and its `p.emit`; it simply
calls the decoder in between. This mirrors `decodeMCPStatus`, which `claimMCPStatusQuery`
and the shared-sink arm already share.

### 2. `contextUsageQueries` — the parser-side correlator

A second map on `Parser`, beside `contextUsageRequests` rather than merged into it: the
two carry different payloads (a write verdict versus a reading) and, decisively,
different namespaces. Types mirror `pendingMCPStatusQuery` / `mcpStatusQueries`:

- `pendingContextUsageQuery` — `writeDone`/`writeOK`/`writeOnce` plus a buffered
  `result chan contextUsageQueryResult`, with `resolveWrite`, `written`, `complete`.
- `contextUsageQueries` — `register`, `claim`, `remove`, `failAll`.

`claim` returns `(pending, claimed)`. An **unregistered** id carrying
`contextUsageQueryIDPrefix` is still claimed and consumed — it is a duplicate, or a reply
whose waiter was canceled, failed, or retired at a child boundary — which is the rule
that keeps a late private reply off the shared sink.

Parser wrappers `registerContextUsageQuery`, `removeContextUsageQuery`,
`failContextUsageQueries` sit beside the existing `…ContextUsageRequest` pair.

`claimContextUsageQuery(line []byte) bool` decodes `contextUsageResponseIDLine`, claims,
and then: no pending → consumed; `!written()` → `complete(zero, false)`; otherwise
`complete(decodeContextUsage(line))`. Identical rung order to `claimMCPStatusQuery`.

### 3. `QueryContextUsage` — the runner method

```go
const contextUsageQueryIDPrefix = "context-usage-query-"

func (r *Runner) QueryContextUsage(ctx context.Context, detail string) (turnevent.ContextUsage, bool)
```

Order, which is `QueryMCPStatus`'s with one gate removed and one added:

1. `contextUsageDetailAllowed(detail)` **first**, before the context check, the child
   lookup and the id mint — mirroring `RequestContextUsage` and `WriteContextUsage`, whose
   doc block records why vocabulary refusal precedes the no-child refusal.
2. `ctx.Err()` / `r.parser == nil` → not answered.
3. Under `r.mu`: reject on `stdin == nil || rotating`; snapshot `w` and `childGeneration`.
   **`mcpStatusEligible` is not consulted** — it reports whether argv confines the child to
   the daemon's one MCP config, which says nothing about whether a context reading can be
   asked for. A live, non-rotating child is askable.
4. Mint `contextUsageQueryIDPrefix + r.nextControlID()`; register **outside** the leaf
   mutex; re-acquire and re-check `childGeneration` to close the snapshot-to-registration
   gap. Registering before the re-check is what makes the re-check meaningful.
5. `WriteContextUsage(w, id, detail)`; `resolveWrite(err == nil)`; on error or cancel,
   remove, complete false, return.
6. `select` on `pending.result` versus `ctx.Done()`.

**Prefix disjointness.** `context-usage-query-` shares no prefix with
`mcp-status-query-`, with `mcp-actuate-`, or with the bare decimal sequence ids
`RequestContextUsage` mints — none of which can start with a letter. This is checked in
both directions because each correlator claims every unregistered id in its own namespace,
so an overlap silently swallows the other path's replies and the symptom is a hang.

### 4. The `control_response` arm

`claimContextUsageQuery` joins the two existing claims, above `emitContextUsage`:

```go
if p.claimMCPStatusQuery(line) { return }
if p.claimMCPActuation(line) { return }
if p.claimContextUsageQuery(line) { return }
p.emitContextUsage(line)
```

Its position relative to `emitContextUsage` is load-bearing and is the reason the comment
beside `claimMCPStatusQuery` gives: a claimed reply must reach no sink and no record.
Order among the three claims is free — their namespaces are disjoint.

### 5. Child boundaries

- `beginMCPStatusChild` — add `p.contextUsageQueries.failAll()` beside the two existing
  `failAll` calls. Its doc block already generalises to "a waiter must not outlive the
  exact child it targeted"; the comment gains the third correlator by name.
- `takeStdin` — add `r.parser.failContextUsageQueries()` beside the two existing calls.

`contextUsageRequests` is deliberately **not** given a retirement boundary here. It
resolves a write outcome that is already known by the time the runner returns, and
changing it is outside this slice.

### What does not change

`RequestContextUsage`, `WriteContextUsage`, `contextUsageDetailAllowed`, the automatic
post-turn requester in `cmd/pyry`, `sessions.Runner`, and the fake child's arm. The
`emitContextUsage` gate — reading admitted only for an id the daemon minted *and*
successfully wrote — is preserved by construction, since its id-take and `written()` check
are untouched.

## Concurrency model

No goroutine is created; no shutdown path is added.

Three mutexes are involved and **never nested**: `Runner.mu` (leaf — released before every
call-out, including registration and the write), `contextUsageQueries.mu` (leaf), and the
parser's single-writer stdout forwarder, which needs no lock of its own.

- **Register outside `r.mu`, then re-check `childGeneration`.** The window between the
  snapshot and the registration is closed by the re-check, not by holding the lock across a
  call-out. A child replaced in that gap retires an entry that already exists; the reverse
  order would leave a registration nothing can find.
- **`written()` is the write/claim race resolver.** The forwarder can claim a reply while
  the writer is still failing. Blocking on `writeDone` before deciding means a failed write
  cannot be reported as an answer.
- **`complete` is `sync.Once` over a buffered channel of one**, so a claim, a `failAll` and
  a cancel can race without blocking the forwarder or double-sending.
- **Cancel path.** `ctx.Done()` removes the registration and returns; a reply arriving after
  that is claimed-and-dropped by the unregistered-id-in-my-namespace rule, so it never
  reaches the shared sink.

## Error handling

One `bool` collapses every not-answered outcome — no live child, a rotation in flight, a
child replaced before answering, a write failure, a caller whose context ended, an
unusable payload, and a refused detail. That is `QueryMCPStatus`'s contract and
`actuateMCP`'s stated decision, and this slice has no caller above it to consume a richer
one. The doc block will say so explicitly and name `RequestContextUsage` /
`ErrUnsupportedContextUsageDetail` as where a caller learns the vocabulary. Revisit when a
wire-facing caller lands that must distinguish "bad request" from "no answer".

Nothing on this path logs. The values in scope are the detail, the minted id and the
writer's error; `emitModelList` remains the sole control-response record owner, and a
claimed line returns above it.

**The caller's context is the only bound on a silent child.** Child death and replacement
are covered by `takeStdin` and `beginMCPStatusChild`; a live child that simply never
answers is not. Callers must pass a deadline, exactly as `QueryMCPStatus` and `actuateMCP`
require; no timeout is invented at this layer.

## Testing strategy

New file `internal/streamsup/context_usage_query_test.go`, mirroring
`mcp_status_query_test.go`'s harness (a recording `io.WriteCloser`, a runner with `stdin`
installed directly, an async query, a bounded await). RED first: every case fails to
compile before the method exists, so the first run is watched failing for that reason, then
each assertion is watched failing on behaviour before its code lands.

- **Answered at each allowed detail.** `summary` and `full` each reach the caller's reading.
  The written envelope is decoded and asserted to carry `get_context_usage`, the given
  detail, and an id under the query prefix.
- **A reading claimed by a waiter never reaches the sink.** Parser built with a recording
  sink; after the query completes, the sink has seen no `turnevent.ContextUsage`.
- **The automatic path still publishes.** A `RequestContextUsage("summary")` reply, minted
  under a bare sequence id, reaches the sink — asserted in the same file so the two lanes
  are pinned against each other rather than in isolation.
- **The admission gate is not loosened.** A reply carrying an id the daemon never minted,
  and one whose write failed, each reach neither a waiter nor the sink.
- **Refused detail.** An unsupported value returns not-answered with **zero writes** on the
  recorder — the assertion that proves no id was minted and no request written.
- **Unserviceable asks.** No live child; a child replaced mid-flight (via `takeStdin` and
  via `beginMCPStatusChild`, since they are separate boundaries); an already-canceled
  context; a context canceled while waiting. Each returns promptly rather than blocking.
- **Prefix disjointness.** A table asserting the three prefixes are pairwise non-prefixing
  and that none is a decimal string — the hang this slice must not introduce, pinned as a
  cheap unit assertion rather than left to review.
- **Decoder preservation.** The existing `context_usage_event_test.go` and
  `context_usage_bound_test.go` suites cover `decodeContextUsage`'s behaviour through
  `emitContextUsage` unchanged; they are the regression proof for the extraction and must
  stay green untouched.

Gate: `go test -race ./internal/streamsup/... ./internal/e2e/...`, `go vet ./...`,
`go build ./cmd/pyry`.

## Open questions

1. **Does the fake child need a new arm?** Reading says no — `contextUsageRequestID` keys on
   subtype and detail only and `writeContextUsageAck` echoes the id it is handed. Confirm in
   Phase B by running the `internal/e2e` suite; if a live round trip through the fake proves
   otherwise, the arm change lands here and a `## Revisions` entry records it.
2. **Does `decodeContextUsage` want the runner's cancel check after `select`?**
   `QueryMCPStatus` re-checks `ctx.Err()` after receiving a result and discards a reading
   that arrived after cancellation; `actuateMCP` folds the same check into its return.
   Resolve in Phase B by matching `QueryMCPStatus` exactly, since it is the named template.

## Documentation handoff

None. The ticket's own Documentation handoff section states this slice changes no
wire-visible behaviour and leaves `docs/protocol-mobile.md` unchanged. No entry is pending
for the documentation stage beyond folding the PR's Lessons learned into
`docs/knowledge/features/streamsup-package.md` as usual.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. Two boundaries, each a single named symbol. Outbound:
  `contextUsageDetailAllowed` is the closed allowlist the caller-supplied `detail` crosses
  before anything is minted or written, and `WriteContextUsage` repeats it — the one value
  in this design that travels *into* the child. Inbound: the child's `control_response` is
  untrusted, and `decodeContextUsage` is the single place its payload becomes a
  `turnevent.ContextUsage`. Naming the risk the design excludes rather than claiming none
  exists: **a request id correlates, it does not authenticate.** A child that answers the
  wrong id with the wrong payload hands a waiter a wrong-but-bounded reading — the same
  trust the shared-sink path already extends, unchanged and unwidened here, since only the
  daemon's own child writes that stream.
- **[Network & I/O — the review's load-bearing finding]** No MUST FIX, *because of the
  single-decoder design.* The real vulnerability available here was writing a **second**
  payload decoder for the claim path: it would have silently bypassed
  `maxContextUsageStringBytes`, `maxContextUsageEntries` and `boundContextUsageEntries`'s
  per-entry rejection, handing a waiter unbounded child-authored strings and an unbounded
  list while every existing bound test stayed green. Extracting `decodeContextUsage` and
  sharing it is what makes the bound structural rather than duplicated. **This is the
  constraint the verifier should check first.** Line-length bounding upstream is unchanged:
  the parser's existing `maxBuf` scanner still caps the reply line itself.
- **[Error messages, logs, telemetry]** SHOULD FIX. Nothing on the new path logs, and
  `emitModelList` stays the sole control-response record owner — but the value now *returned
  to a caller* carries child-authored strings including `MemoryFiles[].Path`, which are real
  paths from the user's filesystem. In Phase B, `QueryContextUsage`'s doc block must state
  that the reading carries such content and must not be logged wholesale by a caller. No new
  exposure class is created (#2371 already publishes the same fields on the shared sink);
  the risk is a future caller treating a returned struct as diagnostic-safe.
- **[Concurrency]** No MUST FIX, and three specific reasons rather than a general
  assurance. (a) **Lock ordering:** `r.mu` is never held across `contextUsageQueries.mu` —
  registration, removal and the write all happen after the unlock, and
  `failContextUsageQueries` goes *after* `r.mu.Unlock()` in `takeStdin`, beside the two
  existing calls. Placing it inside the acquisition would nest a leaf lock under another
  and is the one arrangement Phase B must not produce. (b) **TOCTOU:** the
  snapshot → register-outside-the-lock → re-check-`childGeneration` order is the mitigation,
  not an ordering preference; registering after the re-check would leave a registration
  nothing can find. (c) **The buffered-one result channel is load-bearing:** `claim`,
  `failAll` and a `ctx.Done()` removal can race, and `sync.Once` plus the buffer means the
  stdout forwarder can never block sending to a waiter that has already walked away.
- **[Concurrency — resource exhaustion]** No MUST FIX. Every exit path removes its
  registration or has already had it taken (`claim`, `failAll`), so the pending map does not
  grow under normal use. The residual is a caller that passes `context.Background()` to a
  live, silent child — the contract `QueryMCPStatus` and `actuateMCP` already state and this
  method inherits verbatim. It is bounded further than a bare doc contract: `takeStdin` and
  `beginMCPStatusChild` drain the map at every child boundary, so a leak cannot outlive the
  child it targeted.
- **[Subprocess / external command execution]** No findings, by construction rather than by
  audit: this slice adds no `exec.Command` call and `detail` never reaches argv. It is
  written into an already-open stdin pipe as a JSON string field by
  `marshalContextUsageEnvelope`, so no shell interpretation exists to escape. The allowlist
  is what keeps that true if a later slice ever formats the value elsewhere.
- **[File operations]** No findings — no path is constructed, opened, created or statted on
  any code path in this design.
- **[Cryptographic primitives]** No findings, and the design decision that makes the
  category inapplicable is worth stating explicitly rather than skipping: `nextControlID`
  is an atomic counter, not an RNG, and predictable ids are not a weakness here because the
  child is handed the id in the request and is the only party that can write the stream. No
  secret is compared, derived or stored. What *does* matter about ids is disjointness, not
  entropy — see the prefix check below.
- **[Tokens, secrets, credentials]** No secrets. The one id-related hazard is a **namespace
  collision**, whose cost `mcpActuationIDPrefix`'s doc block records as a hang rather than a
  miss: a correlator claims every unregistered id in its own namespace, so an overlapping
  prefix swallows another path's replies. `contextUsageQueryIDPrefix` is checked pairwise
  against `mcpStatusQueryIDPrefix`, `mcpActuationIDPrefix` and the bare decimal sequence, in
  both directions, and the Testing strategy pins it as a unit assertion so the check does not
  rest on review.
- **[Threat model alignment]** OUT OF SCOPE, named rather than deferred silently. A
  phone-reachable verb that asks the child for a context breakdown would expose memory-file
  paths and model identity to a remote client, which `docs/protocol-mobile.md` § Security
  model governs. This slice deliberately opens no such surface: `QueryContextUsage` lands on
  the concrete `*streamsup.Runner` and **not** on `sessions.Runner`, the same posture
  `ReconnectMCPServer` documents, so nothing outside the daemon can reach it. The future
  slice that wires a caller must bring its own gate; it is not inherited from here.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-14

## Revisions

### 2026-09-14 — Open questions resolved in Phase B

Neither resolution changed the design; both are recorded so the questions are visibly
answered rather than dropped.

1. **The fake child needs no new arm — confirmed.** `contextUsageRequestID` keys on
   subtype and detail only and `writeContextUsageAck` echoes whatever id it is handed, so
   a query-minted id is already answered. The tagged e2e suite's context-usage test passes
   untouched, which also re-proves that the automatic post-turn lane still publishes.
   `internal/e2e/internal/fakeclaude/main.go` drops out of the ticket's estimated file
   list, leaving **two** production files.
2. **The post-`select` cancel re-check is `QueryMCPStatus`'s — matched exactly.** A
   reading that arrives after the caller's context ended is discarded rather than
   returned, so a caller cannot act on a value it stopped waiting for.

### 2026-09-14 — one assertion added beyond the written strategy

The Testing strategy's "a claimed reading does not reach the shared sink" is, on its own,
a **vacuous** assertion for this design: a query-minted id is never registered in
`contextUsageRequests`, so `emitContextUsage` could not have published it wherever the
claim sat in the arm. The ordering the ticket calls for is really about what runs *below*
the claims — `emitModelList` owns the control-response record.
`TestRunner_QueryContextUsage_ClaimedReadingLeavesNoControlResponseRecord` pins it with a
capturing logger, and was verified non-vacuous by temporarily moving the claim below
`emitModelList` and watching it fail on that record before reverting.
