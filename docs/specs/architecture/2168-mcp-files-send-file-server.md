# #2168 — `pyry mcp-files`: the `send_file` MCP stdio server forwarding over the control socket

## Files read

- `cmd/pyry/mcp_approve.go` → `runMCPApprove`, `newMCPApproveServer`, `approveServer.register`,
  `approveServer.initialize`, `approveServer.toolsList`, `approveServer.toolsCall`,
  `approveServer.deny`, `approveServer.logVerdict`, `denyResult`, plus the shared MCP wire types
  (`mcpInitializeParams`, `mcpInitializeResult`, `mcpCapabilities`, `mcpToolsCapability`,
  `mcpServerInfo`, `mcpTool`, `mcpToolsListResult`, `mcpToolCallParams`, `mcpTextContent`,
  `mcpToolResult`) and `defaultMCPProtocolVersion` — the subcommand shape this ticket copies, and
  the frame types it reuses rather than re-declares.
- `cmd/pyry/jsonrpc_stdio.go` → `serveJSONRPCStdio` — the server loop; blocks until stdin EOF or
  signal, wires handlers through a `register` callback before `Serve`.
- `cmd/pyry/main.go` → `runArgs` (the dispatch switch), `parseClientFlags` (`-pyry-name` /
  `-pyry-socket`), `printHelp`, `envApprovalTimeout` (the naming precedent for a `PYRY_`-prefixed
  package constant).
- `cmd/pyry/attach_file.go` → `fileAttacher`, `maxAttachFileBytes` (16 MiB), `readChecked` — the
  destination this forwards to, and the size bound the tool description derives from.
- `cmd/pyry/mcp_config.go` → `approveToolRef`, `permissionArgs`, `renderMCPApproveConfig`,
  `mcpServerSpec` — the consumers of `mcpServerName`/`approveToolName`, which is why those two
  constants stay approve-specific and this ticket adds its own pair rather than generalising them.
- `internal/control/client.go` → `AttachFile`, `request`, `DialTimeout` (5s) — the client verb, its
  bound, and its doc's ruling that errors are returned verbatim with no sentinel mapping.
- `internal/control/protocol.go` → `AttachFilePayload`, `AttachFileResult`, `VerbAttachFile` — the
  wire shape; `SessionID` is the whole destination mechanism and empty is refused, not defaulted.
- `internal/control/server.go` → `handleAttachFile`, `sessionOpTimeout` (30s), `sessionOpConnGrace`
  (5s) — the daemon-side guards and the daemon's own conn deadline.
- `cmd/pyry/mcp_approve_test.go` → `startApprovePeer`, `replyPeer`, `hangUpPeer`, `shortTempDir`,
  `testLogger`, `invokeToolsCallCtx` — the fake-control-socket-peer test shape, reusable verbatim
  from this package.
- `docs/knowledge/features/pyry-mcp-approve-command.md` § Tests, § Concurrency — the analogue's full
  MCP surface and its error→result table. **Load-bearing lesson carried into this design:** a
  constructor that reads the environment (the pre-#1929 `newMCPApproveServer`) forces every test
  through `t.Setenv` and so bans `t.Parallel()`; this server therefore takes the session id as a
  constructor *parameter* and `runMCPFiles` does the `os.Getenv`.
- `docs/knowledge/features/control-plane-attachment-file-confine-and-store-a-claude-named-path.md`
  → why the refusals are static, content-free sentences; the accepted existence-oracle residual;
  the `conv.Cwd`-vs-live-workdir gap whose stated remedy is claude re-writing the file into the
  recorded workspace after reading a refusal — which is only possible if the refusal reaches claude
  intact, i.e. exactly what this ticket must not re-word.

## Context

#2164 shipped the daemon half — the `attachment.file` verb, its client `control.AttachFile`, and
`fileAttacher` installed at the composition root — and shipped it deliberately inert: nothing claude
can invoke reaches it. This ticket is the caller, as a standalone subcommand drivable by hand.

Registering the server on the interactive spawn, putting the per-spawn session identity on the
claude child, the bypass boundary and the live-claude proof are the sibling ticket. When this lands,
nothing spawns this server and no session id is on any child's environment, so the server refuses
every call. That is the intended state, not an oversight — it is the same "ships live but inert"
shape #1104 used for `mcp.approve`.

No ADR is warranted. The design decisions here (a second MCP server rather than a second tool on
`pyry_approve`; the identity on the environment rather than in the tool input) are both already
argued in the ticket body and recorded in this plan; neither establishes a pattern beyond the two
subcommands that exist.

## Sizing — over the line ceiling, built as one ticket anyway

Re-counted against this written plan, five of the six size-S numbers hold comfortably: 2 production
source files (`cmd/pyry/mcp_files.go` new, `cmd/pyry/main.go` modified), 0 new exported types
(everything is package-private in `package main`), 0 consumer call sites to update simultaneously
(the change is purely additive), 4 acceptance criteria, and 6 reject branches in `toolsCall`.

The total-written-work line does not: this spec is 395 lines and the implementation plus its test
estimate at ~600, for ~995 against a ceiling of 800. Two independent rules say build it as one
ticket regardless, and they agree:

- **Split depth.** #2168 → parent #2165 → grandparent #2083. The chain is already two deep, so the
  cap forbids proposing a third split; the ticket is marked `needs-human:sizing` and built.
- **The floor.** Every candidate slice fails it. Cutting server-from-dispatch-arm, or
  handshake-from-`tools/call`, yields a child whose only consumer is its sibling in the same family
  — the one-consumer shape the floor rule says to merge back, and the floor wins over the ceiling
  because a ticket that cannot be verified on its own is not fixable by a resume.

Worth naming for whoever reads the label: the overage is entirely in the spec, not the code. 395 of
the ~995 is this document, of which roughly 90 lines are the `## Security review` section the
`security-sensitive` label makes mandatory — a fixed cost the refiner's `Estimate:` line (~700)
did not carry. The code-and-test half sits inside the ceiling on its own.

## Design

One new file, `cmd/pyry/mcp_files.go`, in `package main`, plus a dispatch arm and a help line in
`cmd/pyry/main.go`. Nothing under `internal/` changes.

### Constants

| Constant | Value | Why it is a constant |
|---|---|---|
| `mcpFilesServerName` | `pyry_files` | Forms the `mcp__pyry_files__send_file` reference the sibling writes. |
| `sendFileToolName` | `send_file` | Same; and `tools/call` rejects any other name. |
| `envSessionID` | `PYRY_SESSION_ID` | The environment contract the sibling ticket writes. Spelled once, mirroring `envApprovalTimeout`. |

`mcpServerName` / `approveToolName` stay as they are. They are approve-specific despite the generic
spelling of the first, and `approveToolRef` in `mcp_config.go` derives the permission-bridge tool
reference from them; renaming either to make room here would be a behaviour-adjacent edit to the
permission bridge for a cosmetic gain, which the ticket already rejected as its third option.

### Reused, not re-declared

The MCP frame types listed in § Files read live in this same package and are generic MCP wire
shapes, not approve-specific: `mcpToolCallParams`, `mcpToolResult`, `mcpTextContent`, `mcpTool`,
`mcpToolsListResult`, the `initialize` request/result trio, and `defaultMCPProtocolVersion`. A
second copy of any of them would be a drift hazard for zero gain, so this file declares none of
them.

Deliberately **not** shared: the `initialize` handler body. Sharing it would mean editing
`approveServer.initialize` to call a new free function — an edit to the permission bridge in service
of this ticket. A twelve-line handler duplicated is the smaller change than a refactor of the
permission gate, so `filesServer.initialize` carries its own copy of the version-echo logic.

### `filesServer`

```go
type filesServer struct {
    socketPath string       // resolved control socket to forward to
    sessionID  string       // the CALLER's session, from the environment; "" ⇒ every call refuses
    log        *slog.Logger // stderr only
}
```

Read-only after construction; no locks, no shared mutable state — same posture as `approveServer`.

`newMCPFilesServer(socketPath, sessionID string, log *slog.Logger) *filesServer` is the sole
production construction site. **It takes the session id rather than reading the environment**, for
the reason recorded in § Files read: an environment-reading constructor is untestable in parallel.
`runMCPFiles` performs the single `os.Getenv(envSessionID)`.

### `runMCPFiles(args []string) error`

Copies `runMCPApprove` wholesale: `parseClientFlags("pyry mcp-files", args)`, reject positionals,
`signal.NotifyContext` for SIGINT/SIGTERM, a **stderr** `slog` text handler, then
`serveJSONRPCStdio(ctx, os.Stdin, os.Stdout, logger, s.register)`.

**An empty `PYRY_SESSION_ID` does not abort startup.** The server starts and answers `initialize`
and `tools/list` normally, refusing each `tools/call`. Exiting at startup would make claude report a
broken MCP server; refusing per call is a sentence claude reads. This is the inert state the ticket
asks for.

### Handler contracts

- `register(t *acp.Transport)` — binds `initialize`, `tools/list`, `tools/call`. Notifications need
  no handler (`dispatchNotification` drops unregistered ones silently).
- `initialize(ctx, params) (any, error)` — echoes a non-empty client `protocolVersion`, else
  `defaultMCPProtocolVersion`; `serverInfo.name` is `mcpFilesServerName`. A params that is not a
  JSON object → `acp.CodeInvalidParams` (shape gate); absent/empty tolerated. No params bytes logged.
- `toolsList(ctx, _) (any, error)` — exactly one `mcpTool`, and no other.
- `toolsCall(ctx, params) (any, error)` — always returns `mcpToolResult{IsError: false}`. Never a
  JSON-RPC error, never a hang. Detailed below.

### The tool: description and schema are the whole contract

Unlike `approve`, whose input schema its own doc calls advisory because claude's permission path
fills a fixed shape regardless, here the description and schema are all claude reads before
deciding what to pass. So they state the confinement rule up front, in the order a caller needs it:
the path must be inside this conversation's workspace directory, a relative path resolves against
that workspace (not the daemon's directory), the file is read once and copied so later edits are not
reflected, and files above the byte bound are refused.

The byte bound is **interpolated from `maxAttachFileBytes`** (same package) rather than spelled as a
literal, so the sentence cannot drift from the constant that enforces it. The description is
therefore a package-level `var` built once, not a `const`.

`sendFileInputSchema` is a static `json.RawMessage` literal — clearer than a nest of schema structs,
following `approveInputSchema`:

```
{"type":"object","properties":{"path":{"type":"string","description":"…"}},"required":["path"]}
```

### `sendFileArgs` — the leak barrier is a type, not a check

```go
type sendFileArgs struct {
    Path string `json:"path"`
}
```

`call.Arguments` unmarshals into **this** type, never into `control.AttachFilePayload`. That
distinction is the whole of AC#4's structural half. `AttachFilePayload` has a `SessionID` field with
a `json:"sessionID"` tag; unmarshalling model-controlled bytes straight into it would populate that
field from the tool's input, leaving "the daemon's `SessionID` is the environment's" resting on an
unconditional overwrite that a later edit can silently drop. `sendFileArgs` has no field a
model-supplied session id can land in, so the guarantee is enforced by the type system rather than
by an assignment that has to keep being correct.

The payload is then assembled explicitly:
`control.AttachFilePayload{SessionID: s.sessionID, Path: args.Path}`.

### `toolsCall` — the fail-closed core

Every step below terminates in a `mcpToolResult{IsError:false}` whose single text block is one
sentence. The order is: parse the call, confirm it is ours, confirm we have an identity, parse the
arguments, forward.

| # | Condition | Result text |
|---|---|---|
| 1 | `params` will not unmarshal | self-originated, fixed: malformed request |
| 2 | `call.Name != sendFileToolName` | self-originated, fixed: unknown tool |
| 3 | `s.sessionID == ""` | self-originated, fixed: this server has no session identity — **no dial happens** |
| 4 | `arguments` will not unmarshal into `sendFileArgs` | self-originated, fixed: malformed request |
| 5 | `control.AttachFile` returns an error | **the error's text verbatim** |
| 6 | `res == nil` with a nil error | self-originated, fixed (belt-and-suspenders; `AttachFile` already errors on this) |
| 7 | otherwise | the file was handed over, naming the minted attachment id |

An empty `path` is **not** pre-checked here and is forwarded as-is: `handleAttachFile` refuses it
with its own sentence, which is the actionable one. Adding a local guard would only substitute a
worse sentence for a better one. (The empty *session id* is different — it is guarded here because
this seam is the only one that can distinguish "absent from the environment" from "the caller sent
one", and because both downstream seams treat `""` as a wildcard.)

**Row 5 forwards `err.Error()` verbatim, with no classification.** This is the AC's explicit
instruction — "not re-worded, classified or mapped to a code" — and it is also what
`control.AttachFile`'s own doc rules: any error is returned verbatim because the daemon's refusals
are static prose for claude to act on, not tokens for a caller to branch on. A classifier here
would have to guess which errors are the daemon's and would re-word the rest; the design declines to
have one. The residual is that a dial failure's text names the socket path — see § Security review,
[Error messages].

The `ctx` `toolsCall` receives from `Serve` is passed to `control.AttachFile` **unwrapped**, with no
per-call deadline added — matching `toolsCall` in `mcp_approve.go` and the ticket's technical note.
`AttachFile` is built on `request`, so an undeadlined ctx bounds the whole exchange at
`control.DialTimeout` (5s). See § Error handling for the accepted residual that gives.

### Logging

stdout is exclusively the JSON-RPC frame stream; every diagnostic goes to stderr. One decision log
line per call, carrying:

- the minted `attachment_id` on success — the one value `control.AttachFile`'s doc calls safe to log;
- a fixed, self-originated `stage` word on refusal (which of rows 1–6 fired) and **nothing else** —
  in particular never the refusal text, which on row 5 is the daemon's sentence on the daemon
  branch and a dial error naming the socket path on the transport branch.

No branch logs the requested path, the arguments bytes, or the params bytes. The daemon's own
posture is that refusals are not logged at all; the fixed stage word adds a diagnosable stderr trail
without adding a byte derived from the request.

## Concurrency model

`acp.Transport.Serve` runs one read-loop goroutine dispatching inline. A `tools/call` blocks that
loop for the duration of one bounded control round trip (≤ `DialTimeout` on an undeadlined ctx),
which is safe: claude calls a tool and waits for its result before emitting a further frame, and the
handler issues no outbound `Transport.Call`, so there is no read-loop deadlock. Unlike the approve
path there is no wait on a human, so the blocking window is sub-second in the normal case rather
than open-ended.

Goroutines this ticket spawns: none. `serveJSONRPCStdio` owns the two it starts (the ctx closer and
the stdin bridge) and documents their exits.

Shutdown: SIGINT/SIGTERM cancels the `signal.NotifyContext` ctx → `serveJSONRPCStdio` closes the
in-memory pipe, unblocking a parked read → `Serve` returns → `runMCPFiles` returns nil. A signal
arriving mid-forward reaches the control read through the ctx deadline `request` installed, and the
call terminates in row 5.

## Error handling

Failure modes and their handling are the seven rows above. Three of them deserve naming as design
positions rather than mechanics:

- **Socket unreachable.** `dial` fails within `dialRetryBudget` (~1.5s), independent of any ctx
  deadline, so a missing daemon yields a bounded refusal rather than a hang.
- **Conn ends without a reply.** `Decode` returns EOF → row 5, immediately.
- **Accepted residual — the client's 5s bound is shorter than the daemon's 35s.** With an
  undeadlined ctx, `request` sets the conn deadline at `now + DialTimeout` (5s), while
  `handleAttachFile` sets its own at `sessionOpTimeout + sessionOpConnGrace` (35s). A file whose
  read-plus-store exceeds 5s would therefore refuse on this side while the daemon completes and
  mints an id — a filed attachment claude is told about as a refusal. Accepted: the bound is 16 MiB
  and the work is a local read plus a rename, orders of magnitude under 5s; and the alternative is
  to pick some other number here, which the ticket's technical note explicitly declines by pinning
  the `request`-based bound. The failure is fail-safe in the direction that matters (a spurious
  refusal, never a spurious success), and the remedy is a retry, which is idempotent because each
  call mints a fresh id.

## Testing strategy

`cmd/pyry/mcp_files_test.go`, stdlib `testing`, table-driven where the rows share a shape,
`t.Parallel()` throughout (possible precisely because the constructor reads no environment). It
reuses this package's existing fake-control-socket-peer helpers — `startApprovePeer`, `replyPeer`,
`hangUpPeer`, `shortTempDir`, `testLogger` — rather than declaring second copies.

Scenarios:

- **Handshake.** `initialize` echoes a non-empty client `protocolVersion`; falls back to
  `defaultMCPProtocolVersion` when absent or empty; `serverInfo.name` is `mcpFilesServerName`; a
  non-object `params` is `acp.CodeInvalidParams`.
- **`tools/list` advertises exactly one tool.** Length is 1, name is `sendFileToolName`; the
  description names the workspace-confinement rule and carries the bound derived from
  `maxAttachFileBytes`; the schema requires `path`.
- **Success round trip.** Peer replies an `AttachFileResult`; assert the text names the minted id,
  `isError` is false, and — from the captured `control.Request` — that the verb is `VerbAttachFile`
  and the payload carries the constructor's session id and the path byte-verbatim.
- **The session id is never taken from the tool input.** Call with
  `{"path":"notes.md","sessionID":"attacker-chosen"}` against a server built with a different id;
  assert the forwarded payload's `SessionID` is the server's. This is the row that reddens if
  `sendFileArgs` is ever replaced by `control.AttachFilePayload`.
- **Empty session identity refuses without dialling.** Build with `""` against a peer that counts
  connections; assert a refusal result and **zero** connections accepted.
- **The daemon's sentence passes through byte-exact.** Peer replies
  `Response{Error: "attachment.file: the path is outside this conversation's workspace"}`; assert
  the result text equals that string exactly, prefix included — the assertion that reddens on any
  re-wording, trimming or classification.
- **Transport termini.** Socket unreachable (no peer at all), and `hangUpPeer` (request accepted,
  conn closed with no reply): both a refusal, neither a hang, neither a JSON-RPC error.
- **Cancelled ctx.** An already-cancelled ctx into `toolsCall` yields a refusal.
- **Malformed and misdirected calls.** Non-JSON `params`, non-JSON `arguments`, and
  `name != "send_file"`: each a refusal, no panic, and — for the wrong-name row — **no connection
  accepted**, which is what pins the fail-closed rejection rather than a forward that happens to
  fail.
- **No-byte-leak logging.** Drive every branch with a distinctive path literal and filename against
  a logger writing to a buffer; assert the buffer contains neither, nor the socket path, on any
  branch — including the two branches whose result text legitimately carries one.
- **Dispatch.** `runArgs([]string{"pyry", "mcp-files", "bogus"})` returns the positional-rejection
  error. It proves the arm exists and routes to `runMCPFiles` without touching `os.Stdin`, so no
  test needs a TTY or a real fd.

RED is confirmed before implementation: the file does not compile until `mcp_files.go` exists, which
is the correct first red for a new-surface ticket. The behavioural rows are then each confirmed to
fail for the right reason against a stub before the handler bodies land.

## Open questions

1. Does `printHelp` need the new verb, and in what wording? (Resolve by reading the block around
   the existing `pyry mcp-approve` line and matching it.)
2. Is `startApprovePeer`'s name a problem when a second, non-approve test file uses it? Leaning no —
   renaming a shared helper means editing `mcp_approve_test.go`, which is out of this ticket's
   scope; a doc line at its one new call site is the cheaper fix.
3. Does the reused `shortTempDir` prefix (`pyryapprove`) matter for a files test? Leaning no —
   it only shortens the socket path under the macOS `sun_path` limit.

Each is resolved in Phase B; anything that changes the design above is recorded in a `## Revisions`
entry.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. The design has exactly one boundary — `filesServer.toolsCall`
  — and it is a type, not a check: model-controlled `arguments` unmarshal into `sendFileArgs`, which
  has no field a caller-supplied session id can land in, so the payload's `SessionID` can only ever
  come from the constructor. The rejected alternative (unmarshalling into `control.AttachFilePayload`
  and overwriting `SessionID`) puts the same guarantee on an assignment a later edit can silently
  drop. The other model-controlled values are bounded the same way: `call.Name` is compared to a
  constant and never dispatched on, `args.Path` is forwarded opaquely and confined by `confineFile`
  daemon-side, and `initialize`'s echoed `protocolVersion` is escaped by the transport's outer
  `json.Marshal` (inherited behaviour, unchanged from `approveServer.initialize`).
- **[Trust boundaries]** No finding, verified rather than assumed: an environment-supplied
  `PYRY_SESSION_ID` cannot traverse anything daemon-side. In `fileAttacher` the id reaches only the
  liveness check and `conversationForCurrentSession`; every path component of the destination comes
  from `conv.ID` and the freshly minted attachment id, never from the id this server forwards. A
  hostile value therefore fails `live` or fails to resolve, and refuses — the third guard on the
  hazard, after `handleAttachFile`'s and `fileAttacher`'s own empty-id checks.
- **[File operations]** No findings, and one deliberate absence worth stating: this ticket opens no
  file, joins no path, and adds **no client-side confinement check**. Canonicalisation of both ends,
  the `withinDir` boundary test, the `O_NONBLOCK`/`O_NOFOLLOW`/`os.SameFile` TOCTOU defence and the
  16 MiB bound all live in `confineFile`/`readChecked` (#2164) and stay there. A second check here
  would be a weaker boundary that can diverge from the enforcing one, and it would replace the
  daemon's actionable sentence with a worse one. This is also why an empty `path` is forwarded
  rather than guarded locally.
- **[Tokens, secrets, credentials]** **SHOULD FIX** — the decision log must **not** carry the session
  id, on any branch. It is the natural correlation key to reach for (`mcp-approve`'s log line uses
  `tool_use_id` for exactly that), and it is the one value in this exchange that confers authority:
  holding it is what lets a call file into a conversation. This server's stderr is captured by
  whatever forked it, i.e. claude. Log the minted `attachment_id` (documented safe) and a fixed
  stage word, and nothing else. The verifier should check no branch logs `s.sessionID`.
- **[Subprocess / external command execution]** Not applicable by design: this subcommand execs
  nothing — it *is* the forked child. No `sh -c`, no argv built from model input. It inherits the
  full environment and reads exactly one variable from it; since it spawns nothing, there is no
  environment to scrub for a descendant.
- **[Cryptographic primitives]** Not applicable: no randomness, no comparison against a secret, no
  key material. The one random value in the exchange, the attachment id, is minted daemon-side from
  `crypto/rand` via `conversations.NewID` and is documented as not-a-capability (#2054 re-validates
  it against the conversation binding on retrieval).
- **[Network & I/O]** No findings. Input from stdin is capped by the transport's `maxLineBytes`
  (16 MiB, `internal/acp`), so an over-long frame breaks the stream rather than exhausting memory.
  The control leg is a `0600` unix socket to a same-user daemon, bounded at `DialTimeout` with the
  5s-vs-35s residual named in § Error handling; its failure direction is a spurious refusal, never a
  spurious success. One call is in flight at a time and this ticket opens no listener, so there is
  no connection-count or per-peer limit to set.
- **[Error messages, logs, telemetry]** **SHOULD FIX** — the log must not carry `call.Name` on the
  unknown-tool branch. It is model-controlled and would put attacker-chosen bytes into stderr; the
  fixed stage word already identifies the branch. Accepted residual, not a finding: on the transport
  branch the *result text* (not the log) carries a dial error naming the socket path. It discloses
  nothing new — the peer is claude, running as the same user, and the path is in this process's own
  argv, which the sibling ticket wrote — and the alternative is the classifier the AC forbids. No
  branch logs the requested path, the arguments bytes or the params bytes; refusal text is never
  logged at all, matching the daemon's own posture.
- **[Error messages, logs, telemetry]** **SHOULD FIX** — the tool description must say *this
  conversation's workspace directory*, not "the current directory". The enforced root is `conv.Cwd`,
  which can lag the live child's actual working directory in the `change_workspace` window (#1475).
  Worded as "current directory" the contract is subtly false in that window and a refusal reads as a
  contradiction; worded as the conversation's workspace it reads as the documented case, whose
  stated remedy is exactly what claude then does — re-write the file into the recorded workspace and
  retry. For prose that is "the entire contract", the accuracy is load-bearing.
- **[Concurrency]** No findings. `filesServer` is immutable after construction: no locks, so no lock
  ordering; no check-then-mutate on shared state; no goroutines spawned by this ticket
  (`serveJSONRPCStdio` owns the two it starts and documents their exits). The safety does **not**
  rest on `acp.Transport` dispatching inline on one goroutine — a concurrent dispatcher would still
  be race-free against a read-only value. Nothing is written to disk, so a signal mid-call leaves no
  partial state; it terminates the call in the fail-closed error row.
- **[Threat model alignment]** `docs/protocol-mobile.md` § Attachments' ban on logging filenames is
  honoured on every branch. The confinement threat is #2164's and is not weakened here.
- **[Threat model alignment]** **OUT OF SCOPE** — the bypass boundary, deferred to **#2169** by the
  ticket body. Nothing stops claude from running `pyry mcp-files` (or the subcommand's control verb)
  directly from a shell with a `PYRY_SESSION_ID` of its choosing, so the MCP tool is not the only
  route to this verb. The design that makes the identity un-forgeable — where the value is written,
  and whether the shell path is closed — belongs to the ticket that puts it on the child.
- **[Trust boundaries]** **OUT OF SCOPE** — also **#2169**. An environment variable is inherited by
  every descendant, so once the sibling puts `PYRY_SESSION_ID` on the claude child, every subprocess
  claude spawns can read a value that is authority-shaped for filing into that conversation. It
  grants nothing claude does not already hold directly, which is why it is a deferral and not a
  finding against this design, but the choice of carrier is the sibling's to defend.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-06

## Revisions

### 2026-09-06 — Phase B

**No design change.** The implementation landed as specified; this entry records the Open Questions'
resolutions and the evidence gathered for the review's SHOULD FIX items.

**Open questions, resolved:**

1. `printHelp` does take the verb — added beside the existing `pyry mcp-approve` block, matching its
   wrapping and naming `PYRY_SESSION_ID` so the environment contract is discoverable from `--help`.
2. `startApprovePeer`'s name is not a problem. It, `replyPeer` and `hangUpPeer` are peer/transport
   shapes rather than approve semantics, so the new tests reuse them; a comment at the top of
   `mcp_files_test.go` records why the name still says "approve" and that renaming a helper with a
   live caller in another file is out of scope. `driveMCP` was the one helper that could **not** be
   reused — it is typed to `*approveServer` — so this file carries its own `driveFilesMCP` rather
   than generalising a permission-bridge test helper.
3. `shortTempDir`'s prefix is irrelevant; it exists only to keep the socket path under the macOS
   `sun_path` limit.

**Both logging SHOULD FIX items landed**, and one is behaviourally pinned:
`TestMCPFiles_LogsCarryNoRequestBytes` drives all six branches and asserts stderr carries neither the
host path, the filename, the session id, nor the socket path. `call.Name` is never logged either —
`refuse` takes only a fixed stage word. The third SHOULD FIX, the description's wording, is pinned by
`TestMCPFiles_ToolsList` asserting the description names the workspace rule and carries the bound
derived from `maxAttachFileBytes`.

**Mutation evidence** (run over `go test -overlay`, no worktree writes), confirming the three
load-bearing rows fail for the intended reason rather than passing vacuously:

| Mutant | Died on |
|---|---|
| `sendFileArgs` replaced by `control.AttachFilePayload` as the unmarshal target, overwrite dropped | `TestMCPFiles_SessionIDNeverFromToolInput` (forwarded `SessionID` became the caller's) **and** `TestMCPFiles_HandOverRoundTrip` — two independent witnesses |
| the `s.sessionID == ""` guard deleted | `TestMCPFiles_NoSessionIdentity_RefusesWithoutDialling`, on **both** the text and the connection count |
| the daemon's sentence prefixed with `"send_file failed: "` | `TestMCPFiles_DaemonRefusalVerbatim` |
