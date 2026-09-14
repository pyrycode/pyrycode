# 2418 — streamsup: actuate an MCP reconnect or toggle on the live child and correlate its ack

## Files read

- `internal/streamsup/runner.go` → `QueryMCPStatus` — the snapshot/register/recheck/write/wait
  shape this slice copies for a different payload; `mcpStatusQueryIDPrefix` and `nextControlID` —
  the namespace the new one stays disjoint from and the single counter both draw on; `takeStdin`,
  `setStdin` and `mcpStatusEligible` (field and free function) — the child-binding generation and
  the teardown hook a pending actuation retires on.
- `internal/streamsup/parser.go` → `mcpStatusQueries`, `pendingMCPStatusQuery` and
  `claimMCPStatusQuery` — the correlator being mirrored, `written()` gate included;
  `beginMCPStatusChild` — the pre-spawn hook actuations join; `consumeLine`'s `control_response`
  arm — where the claim sits, ahead of `emitContextUsage`, `noteControlAck`, `emitModelList` and
  `decodeMCPStatus`; `controlAckLine` — a `subtype`+`request_id` target with no payload key,
  reused rather than re-declared (see Design).
- `internal/streamsup/envelope.go` → `WriteMCPReconnect`, `WriteMCPToggle` and
  `marshalMCPToggleEnvelope` — the two unwired writers this slice calls, and the pointer-typed
  `Enabled` that keeps `false` explicit on the wire.
- `internal/streamsup/mcp_status_query_test.go` → `mcpStatusQueryRunner`,
  `newMCPStatusQueryWriter`, `mcpStatusQueryResponse` — same-package helpers the new test file
  reuses instead of re-declaring.
- `docs/knowledge/features/streamsup-package.md` § "Turn I/O — envelope write + stdout parser",
  the `Runner.QueryMCPStatus` and `Parser.claimMCPStatusQuery` paragraphs — the recorded contract
  the new pair reads as a sibling of, and where the documentation stage adds this slice's lines.
- `docs/knowledge/features/e2e-realclaude-mcp-status-capture-test-go.md` § "Readiness belongs to
  the control replies", and `internal/e2e/realclaude/testdata/mcp_status_v2.1.259.json` — the
  observed `error` reply to reconnecting a broken server, and the capture showing both verbs
  answered by a bare `control_response` with no server list.

## Context

`WriteMCPReconnect` and `WriteMCPToggle` shipped in #2273 with no caller. Nothing in the daemon
can actuate an MCP change today, and nothing can learn whether a child accepted one. This slice
adds the two runner methods above those writers and the correlator that makes their acks
answerable, so #2419's relay verbs and #2420's audit record have something truthful to report.

The committed capture settles the payload question before the design starts: both verbs are
answered by a bare `control_response` with no server list, so the ack is a boolean and a fresh
inventory is always a separate `QueryMCPStatus`; the same capture's `error` answer to reconnecting
a broken server is why not-accepted is an observed arm rather than a defensive one.

This design records no ADR-worthy decision — it copies an existing correlator rather than
establishing a boundary — so no new file under `docs/knowledge/` is warranted.

**Sizing.** Projected total written work is ~850 lines (≈220 production, ≈320 test, this plan),
above the 800-line boundary. It ships whole rather than split, under the floor-beats-ceiling rule:
every seam available here — correlator from callers, reconnect from toggle — yields a child whose
only consumer is its sibling in this same family, and a one-consumer slice cannot be verified on
its own. The ceiling costs at most a continuation leg; the floor cannot be repaired by one. The
binding constraints are all well inside their limits: 2 production files, 0 new exported types, 0
consumer call sites (no interface method, no adapter forwarder), 5 acceptance criteria. The
overage is plan prose and test functions, one per AC clause, not edit fan-out.

## Design

Two runner methods, one private correlator, no exported types.

### Runner surface

```go
func (r *Runner) ReconnectMCPServer(ctx context.Context, serverName string) bool
func (r *Runner) SetMCPServerEnabled(ctx context.Context, serverName string, enabled bool) bool
```

`bool` is the whole return. Per the ticket, a refusal and an unavailable are one outcome here:
#2419 publishes a single merged reject and #2420's audit record names no reason, so nothing above
this slice can distinguish them and a reason would be a contract with no reader. What the failure
arm buys is the narrower property that matching an id must not by itself read as accepted.

`SetMCPServerEnabled` rather than a `Toggle` spelling: the wire subtype is `mcp_toggle` but the
request carries an explicit flag, and the caller's flag is passed through for both `true` and
`false` rather than one being fixed or derived.

Both delegate to one unexported helper:

```go
func (r *Runner) actuateMCP(ctx context.Context, write func(io.Writer, string) error) bool
```

The closure receives the bound writer and the minted id. Behaviour, in `QueryMCPStatus`'s order:
reject a nil parser or an already-cancelled context; snapshot writer, eligibility and
`childGeneration` under `r.mu`; mint the id and register outside that leaf lock; re-acquire and
re-check the binding is the same generation, still eligible and not rotating; write; wait on the
pending result or the context.

**Not shared with `QueryMCPStatus`.** The two differ in result type and in what a claimed line
means, and a generic pending type would put the shipped status path inside this diff for no
behavioural gain. Deliberate duplication beats refactoring a proven correlator — stated here so
the verifier reads it as a decision, not an oversight.

### Id namespace

```go
const mcpActuationIDPrefix = "mcp-actuate-"
```

Its own namespace, and the disjointness from `mcpStatusQueryIDPrefix` is load-bearing rather than
cosmetic. `mcpStatusQueries.claim` treats **any** unregistered id carrying its prefix as claimed
and consumed, and `claimMCPStatusQuery` runs first in the `control_response` arm — so an
actuation minting under that prefix would have every ack swallowed before its own correlator saw
one, and the symptom is a hang, not a miss. Neither constant is a prefix of the other; the
constant's doc comment says why.

Both draw ids from `nextControlID`, the single runner-wide sequence. A second counter would
collide on its first id.

### Parser correlator

Mirrors `mcpStatusQueries` with a narrower result:

- `pendingMCPActuation` — `writeDone`/`writeOK`/`writeOnce`, `result chan bool`, `resultOnce`.
  `written()` blocks until the write outcome is known, which is what lets a write failure beat a
  reply the parser already claimed.
- `mcpActuations` — `mu`, `pending map[string]*pendingMCPActuation`, with `register`, `claim`,
  `remove` and `failAll`. `claim` returns claimed for any unregistered id carrying the actuation
  prefix, so a duplicate or a reply whose waiter is gone is consumed rather than forwarded.
- `Parser.mcpActuations` field, plus `registerMCPActuation`, `removeMCPActuation` and
  `failMCPActuations` beside their status counterparts.

`claimMCPActuation(line []byte) bool` decodes into **`controlAckLine`**, the existing target that
declares `subtype` and `request_id` and no payload key at all. Reusing it rather than declaring a
third identical struct is what makes AC5 structural: server names and claude-authored error text
are unreachable from this path by construction, not by remembering not to log them. Acceptance is
`subtype == controlResponseSuccess`; every other value, an absent subtype included, is
not-accepted — the same total classification `emitModelList`'s nak rung uses.

Placement: in `consumeLine`'s `control_response` arm, immediately after `claimMCPStatusQuery` and
before `emitContextUsage`. A claim returns from the arm, so a claimed ack reaches no shared
consumer — no event of any kind, and no `logControlResponse` record either, since that is
`emitModelList`'s and never runs.

### Child boundaries

Both existing hooks gain the actuation map:

- `beginMCPStatusChild` calls `mcpActuations.failAll()` beside the status one. It runs before
  `cmd.Start`, so no waiter can outlive the child it targeted.
- `takeStdin` calls `failMCPActuations` beside `failMCPStatusQueries`. Teardown fails every
  pending actuation, and the prefix keeps any already-buffered late reply private afterwards.

The `mcpStatusPolicy` install inside `beginMCPStatusChild` is untouched, so the automatic
once-per-eligible-child status path is unchanged.

Eligibility stays the `mcpStatusEligible` verdict for the exact running child's spawn argv, not a
mutable next-spawn setting. Server membership and device authorization are not added here; both
land in #2420, as `WriteMCPReconnect`'s own doc block already states.

## Concurrency model

No new goroutine. The caller's goroutine blocks in `actuateMCP`; the parser's stdout forwarder
completes it. `mcpActuations` carries its own mutex for exactly that pair, matching the field
comment on `mcpStatusQueries`.

`r.mu` stays a leaf: registration happens between two short acquisitions, never inside one, and
the generation re-check closes the snapshot-to-registration gap without nesting locks. No channel
operation, log call or write happens under it.

Every pending actuation has a terminal path — an exact ack, a write failure, a cancelled context,
`beginMCPStatusChild`, or `takeStdin`. `resultOnce` makes the first terminal and the buffered
result channel means no completer blocks, so neither waiter nor completer can leak.

## Error handling

| Condition | Result | Write attempted |
|---|---|---|
| Nil parser, or context already cancelled | `false` | no |
| No bound child (`stdin == nil`) | `false` | no |
| Child not strict-config eligible | `false` | no |
| Rotation in flight (`rotating`) | `false` | no |
| Binding generation changed during registration | `false` | no |
| Stdin write error | `false` | attempted, correlation removed |
| Ack `subtype != "success"` | `false` | yes |
| Ack `subtype == "success"` | `true` | yes |
| Context cancelled while waiting | `false` | yes, correlation removed |
| Child replaced or torn down while pending | `false` | yes, correlation failed |

No error value is returned and none is logged on this path: the outcomes above collapse by
design, and a log record is where a server name would leak.

## Testing strategy

New file `internal/streamsup/mcp_actuation_test.go`, reusing the same-package helpers
`mcpStatusQueryRunner`, `newMCPStatusQueryWriter` and `mcpStatusQueryResponse` rather than
re-declaring them. Scenarios:

- Reconnect writes one `mcp_reconnect` carrying the actuation prefix to the bound writer, and a
  `success` ack for that exact id returns `true`.
- Toggle, table-driven over `enabled` true and false: the decoded request's `enabled` matches the
  caller's flag in both rows and the subtype is `mcp_toggle`. (AC1's pass-through.)
- An `error` ack for a pending id returns `false`. (AC2.)
- An automatic numeric-id status response completes no pending actuation and still reaches the
  shared sink as an `MCPStatus`; an unknown actuation-prefixed id completes nothing either, and
  the pending actuation still completes on its own id afterwards. (AC2, and AC3's "unchanged".)
- Overlapping reconnect and toggle correlate independently; each completes only on its own id, and
  a second response for a retired id is inert.
- No event of any kind reaches the sink for a claimed ack — asserted for a success ack, an error
  ack, and a success ack carrying an `mcpServers` payload the shape decoder would else emit. (AC3.)
- Unavailable table — no parser, no bound child, ineligible child, rotating child — each returns
  `false` with zero writes. (AC4.)
- A write failure returns `false` even when the parser claims an early reply first, using the
  blocked-writer probe shape from the status query's own write-failure test. (AC4.)
- Cancellation returns `false` and leaves no waiter; a late ack is inert. (AC4.)
- Child replacement via `beginMCPStatusChild` and child exit via `takeStdin` each fail a pending
  actuation, and a post-boundary ack completes nothing and reaches no sink. (AC4.)

AC5 is structural rather than log-scanned: `controlAckLine` cannot carry a server name or error
text, and a claimed line returns before any logging consumer. The file builds its parser with
`discardLogger` as its siblings do.

Gate: `go test -race ./internal/streamsup/...`, `go vet ./...`, `go build ./cmd/pyry`.

## Open questions

- Whether `actuateMCP`'s closure should carry the writer or read it from a captured snapshot. Led
  with the parameter so the helper owns the binding re-check and neither public method can write
  to a stale handle; resolve at implementation if the signature reads worse than it sounds here.
- Whether the automatic-path assertion belongs in this file or in `mcp_status_policy_test.go`.
  Led with this file, since what is being proven is that the new claim did not disturb it.

## Documentation handoff

Pending for the documentation stage, from the ticket's own section. In
`docs/knowledge/features/streamsup-package.md`, § "Turn I/O — envelope write + stdout parser",
**added to the existing `Runner.QueryMCPStatus` paragraph** rather than as a new section — that
file is within a few kilobytes of the 50000-byte package-overview cap:

- both actuation acks carry no server list, so a fresh inventory is always a separate status
  query;
- an unclaimed ack would otherwise reach the shared turn sink by shape.

This slice's builder does not edit that file.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** The boundary is subprocess stdout → parent state, and it is a single
  explicit one: `claimMCPActuation`'s decode of the **top-level line bytes** into
  `controlAckLine`. Named finding: **the request id is guessable.** `nextControlID` is a
  monotonic counter, so a child emitting a top-level `control_response` for a guessed
  `mcp-actuate-N` can make a pending actuation report accepted. Not a MUST FIX, for two reasons
  stated here rather than assumed. The only writer of top-level lines on that stream is the child
  the daemon spawned, so the attacker this contemplates already owns the channel — and the
  identical property is relied on by three shipped correlators (`PostureGate`'s
  `noteControlAck`, `contextUsageRequests`, `mcpStatusQueries`); changing it is an id-scheme
  change across all four, not this slice's. What a forged ack can reach is also bounded: it
  changes the **reported outcome only**, never whether the configuration actually changed, since
  the write to the child happened or did not independently of what comes back. The forging that
  would matter — a nested tool result whose text is a control shape — is excluded structurally by
  decoding top-level bytes, which is `controlAckLine`'s own documented guarantee.
- **[Trust boundaries]** `serverName` is caller-supplied and unvalidated on this path — no
  membership check, no device gate. **OUT OF SCOPE, deferred to #2420**, and the reason it is safe
  to defer was verified rather than assumed: the two methods are added to the concrete
  `*streamsup.Runner` and NOT to the `sessions.Runner` interface, and the only holder of the
  concrete type outside this package is `streamRunner`'s unexported field in
  `cmd/pyry/streamsup_runner.go`, which forwards nothing here. No relay handler, control verb or
  CLI path can reach either method until #2419 adds a forwarder together with its gate. If Phase B
  finds itself adding an interface method or an adapter forwarder, this deferral expires and the
  ticket is wrong.
- **[Trust boundaries]** A `serverName` containing a newline, quote or control character cannot
  split or forge a stdin line: `marshalMCPReconnectEnvelope` and `marshalMCPToggleEnvelope` build
  a struct and `json.Marshal` it, so every byte is escaped and the request stays one physical
  line. Pinned by #2273's `TestWriteMCPControlRequests`, not re-proved here.
- **[Tokens, secrets, credentials]** None on this path — nothing is minted, stored, rotated or
  compared. Stated explicitly because the request id looks like one and is not: it is a
  correlation nonce, and per the first finding **no authorization decision rests on its
  unguessability**. It must not later be treated as a capability.
- **[File operations]** Not applicable by design: the whole path is one stdin write and one
  stdout read. No path is constructed, no file opened, stat'd or created. `mcpStatusEligible`
  compares an argv string against `Config.MCPStatusConfigPath` without touching the filesystem.
- **[Subprocess execution]** No argv is constructed and no process spawned. Positive finding worth
  naming: eligibility reads the **immutable spawn snapshot** of the running child, so a caller
  cannot flip a mutable next-spawn setting to make an already-running non-strict child eligible —
  the anti-TOCTOU property `mcpStatusEligible` was given in #2374 and which this slice inherits
  unchanged.
- **[Cryptographic primitives]** No randomness and no secret comparison. The only comparison is
  `subtype == controlResponseSuccess`, against a daemon-authored constant rather than a secret, so
  constant-time comparison is not the relevant discipline; totality of the classification is, and
  every non-`success` value takes the not-accepted arm.
- **[Network & I/O]** No socket, and no unbounded field is decoded: `controlAckLine` declares no
  payload key, so unlike `decodeMCPStatus` there is nothing here needing a `maxMCPStatusServers` /
  `maxMCPStatusError` style cap. `mcpActuations.pending` is self-limiting — every entry is owned by
  exactly one blocked in-flight caller and removed on all six exit paths (recheck, context,
  write error, ack, cancellation, child boundary), so it cannot grow beyond the number of
  concurrent callers. Bounding the number of *callers* is a caller-boundary concern and belongs
  with #2419's verb and #2420's gate; noted as deferred rather than silently assumed.
- **[Error messages, logs, telemetry]** This is AC5's category and the design satisfies it
  structurally: `controlAckLine` cannot carry a server name or claude-authored error text, and a
  claimed line returns from the `control_response` arm before `logControlResponse`, which belongs
  to `emitModelList`. The returned `bool` carries no child-authored text either, and
  `actuateMCP` deliberately **discards** the writer's error rather than propagating it — that
  discard is the guarantee, not sloppiness, and a reviewer must not "improve" it into a returned
  error. **SHOULD FIX for Phase B:** the one edit that breaks AC5 is adding a Debug diagnostic in
  `actuateMCP` naming the server for debuggability. Both method doc comments must state that no
  record is emitted on this path and why; the verifier checks it landed.
- **[Concurrency]** `r.mu` and `mcpActuations.mu` are never held simultaneously — registration
  sits deliberately between two short `r.mu` acquisitions — so no lock order exists to get wrong,
  and `r.mu` stays a leaf. The check-then-use gap is closed by re-reading `childGeneration` AFTER
  registration; the specific scenario that makes the ordering load-bearing: a caller snapshots a
  live binding, `takeStdin` then bumps the generation and runs `failAll`, and the caller registers
  after that `failAll` — the entry survives, and only the post-registration re-check retires it.
  Registering before the re-check, or dropping the re-check, converts that race into a permanent
  hang. Every pending actuation has a terminal path and `resultOnce` makes the first terminal; the
  result channel is buffered, so no completer blocks and no goroutine leaks. **SHOULD FIX for
  Phase B:** with a live child that simply never answers, the caller's context is the *only* bound
  on the wait — the child-boundary hooks cover death and replacement but not silence. Both method
  doc comments must state that the caller supplies the deadline, so #2419 passes one rather than
  `context.Background()`. Same exposure as the shipped `QueryMCPStatus`, which is why it is a
  documented contract rather than an internal timeout invented here.
- **[Threat model alignment]** `docs/protocol-mobile.md` § Security model governs relay-reachable
  surface; this slice adds none, per the verified second finding above, so no threat in it becomes
  newly reachable. The two threats this work will eventually raise — an unauthenticated device
  changing MCP configuration, and an audit record that cannot attribute a change — are named in
  #2419 and #2420 respectively and are not silently dropped here.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-14
