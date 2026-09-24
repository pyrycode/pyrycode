# #2590 — fake Codex app-server binary (`fakecodex`)

## Files read

- `internal/codexsup/codex_app_server_protocol.schemas.json` → `definitions.ClientRequest`, `ClientNotification`, `ServerRequest`, `ServerNotification` (each a `oneOf` whose arms carry `properties.method.enum`); `InitializeResponse`, `CommandExecutionRequestApprovalParams`/`Response`, `CommandExecutionApprovalDecision`; `v2.ThreadStartResponse`, `v2.ThreadResumeParams`/`Response`, `v2.Thread`, `v2.Turn`, `v2.TurnStatus`, `v2.ThreadItem` (`agentMessage`, `commandExecution` arms), `v2.CommandExecutionStatus`, `v2.ItemStartedNotification`, `v2.ItemCompletedNotification`, `v2.AgentMessageDeltaNotification`, `v2.ServerRequestResolvedNotification`, `v2.TurnInterruptParams`. Every field shape below comes from these, not from the alpha.
- `internal/codexsup/SCHEMA.md` — pinned version (0.156.1) and regenerate command; the bundle is already committed (`ff85654e`) and is not touched here.
- `internal/e2e/internal/fakeclaude/main.go` → package comment — the env-only configuration style and the "document each knob in the package comment" convention the new fake mirrors.
- `internal/e2e/internal/fakeclaude/main_test.go` → `TestFakeClaude_OpensInitialAndRotatesOnTrigger` — the build-with-`go build`-then-exec pattern the fake's tests mirror.
- `internal/e2e/harness.go` → `ensureFakeClaudeBuilt` area — how consumers build a fake by import path. Not changed here (wiring is the consumer tickets' job: #2591, #2584, #2585).

In-flight overlap: none (no other remote feature branch touches `internal/e2e/internal/fakecodex` or `internal/codexsup`).

## Context

Codex support (#2583 family) needs a Codex stand-in so tests run without a Codex account, pinned to the 0.156.1 schema so the fake cannot drift from the version pyrycode targets. This ticket ships the fake and its own tests only. No ADR needed.

## Design

One production file, `internal/e2e/internal/fakecodex/main.go`, `package main`, stdlib only. `main` exits with the code returned by `run(os.Stdin, os.Stdout, codexHome)`.

### Wire

Line-delimited JSON, no `jsonrpc` field, matching the real server:

- request: `{"id":…, "method":…, "params":…}`
- response: `{"id":…, "result":…}` or `{"id":…, "error":{"code":…, "message":…}}`
- notification: `{"method":…, "params":…}`

A decoded incoming frame is classified by which of `id` / `method` it carries: both → client request; `method` only → client notification; `id` only → the client's response to one of the fake's own server requests.

### Configuration (env-only, documented in the package comment)

- `CODEX_HOME` — echoed as `initialize`'s `codexHome`. When unset, `$HOME/.codex` (the real default).

No other env knob. Per-turn behaviour is selected by markers in the turn's input text (below), so one fake process can serve both plain and approval turns.

### Operations

Dispatch is a `map[string]handler` keyed by client-request method (`requestHandlers`); the method-name test reads its keys.

| Client frame | Fake's answer |
|---|---|
| `initialize` request | `{codexHome, platformFamily:"unix", platformOs, userAgent:"pyry_fakecodex/0.156.1"}` |
| any other request before `initialize` | error `-32002` "Not initialized" |
| `initialized` notification | accepted, no reply |
| `thread/start` | `ThreadStartResponse` with a freshly minted UUID thread id |
| `thread/resume` | `ThreadResumeResponse` whose `thread.id` is the given `threadId`; `excludeTurns` accepted (turns are always `[]`) |
| `turn/start` | `{turn:{id, items:[], status:"inProgress"}}`, then the turn streams (below) |
| `turn/interrupt` | `{}` and the named running turn ends with `turn/completed` status `interrupted`; error when no such turn is running |
| unknown request method | error `-32601` |
| unknown notification | ignored |

Response objects fill every field the schema marks required (`approvalPolicy` echoed from params or `on-request`, `approvalsReviewer:"user"`, `sandbox:{type:"readOnly"}`, `cwd` from params when absolute else the fake's working directory, `model`, `modelProvider`, and a `Thread` with all its required fields).

### Turn stream

After the `turn/start` response the turn runs on its own goroutine and emits, in order:

1. `turn/started` `{threadId, turn:{id, items:[], status:"inProgress"}}`
2. *approval turns only* — see below
3. `item/started` agentMessage item → `item/agentMessage/delta` `{threadId, turnId, itemId, delta}` → `item/completed` agentMessage item with the full `text`
4. `turn/completed` `{threadId, turn:{id, items:[], status:"completed"}}`

### Markers (documented in the package comment)

- `[fakecodex:approval]` in any text input of the turn → before the agent message: `item/started` commandExecution item (`status:"inProgress"`), then one `item/commandExecution/requestApproval` server request (`{threadId, turnId, itemId, startedAtMs, command, cwd}`), wait for the client's response, `serverRequest/resolved` `{threadId, requestId}`, then `item/completed` for the command item: `accept` / `acceptForSession` → `status:"completed"`, `exitCode:0`, `aggregatedOutput`; any other decision → `status:"declined"`.
- `[fakecodex:hold]` → after `turn/started` the turn blocks until `turn/interrupt` names it. This is what gives `turn/interrupt` a running turn to end; it is an addition to the ticket's marker list, needed to make the interrupt operation deterministic.

Without a marker a turn never sends a server request.

## Concurrency model

- The main goroutine reads stdin with a `bufio.Scanner` (buffer raised to handle large frames) and handles each frame in order.
- Each turn runs on its own goroutine. Running turns are tracked in a mutex-guarded `map[turnID]*turn`; a turn carries an `interrupt` channel (closed by `turn/interrupt`) and, while awaiting approval, a response channel registered in a mutex-guarded `map[requestID]chan` that the main loop feeds on an `id`-only frame.
- All stdout writes go through one mutex-guarded `send`, one marshalled frame plus `\n` per call, so frames never interleave.
- Shutdown: on stdin EOF `run` returns 0 and `main` exits; turn goroutines die with the process (a held turn has nothing to flush). A scan error returns 1.
- An interrupt during an approval wait ends the turn `interrupted`; the abandoned server request is dropped.

## Error handling

- Malformed JSON line → logged to stderr, skipped (the process stays up, as a real server would).
- Request error replies use JSON-RPC codes: `-32601` unknown method, `-32602` bad params (unparseable, or `thread/resume` without `threadId`, `turn/interrupt` for a turn not running), `-32002` not initialized.
- A write error to stdout makes `run` return 1.

## Testing strategy

`internal/e2e/internal/fakecodex/main_test.go`, untagged (runs in `make test` and `make e2e`). `TestMain` builds the binary once with `go build` into a temp dir; each test starts it with `CODEX_HOME` set to a temp dir and speaks raw frames over its stdin/stdout. A helper reads each stdout frame with a timeout, and **asserts every method it sees on the wire, in both directions, against the schema group it belongs to** (client→server requests/notifications, server→client requests/notifications). Each test ends by closing stdin and asserting exit status 0.

Scenarios:

- initialize: `codexHome` equals `CODEX_HOME`, `userAgent` carries `0.156.1`; then `initialized`; EOF → exit 0.
- request before initialize → error; unknown method → error `-32601`.
- `thread/start` → non-empty minted id; `thread/resume` with that id and `excludeTurns:true` → same id.
- plain turn: exact notification order `turn/started`, `item/started`(agentMessage), `item/agentMessage/delta`, `item/completed`, `turn/completed`(completed); no server request.
- hold turn + `turn/interrupt` → `{}` result and `turn/completed` status `interrupted`; interrupt of an unknown turn → error.
- approval turn, table over `decline` / `accept`: server request carries `itemId`/`threadId`/`turnId`; after the reply, `serverRequest/resolved` with the request's id, command `item/completed` `declined` resp. `completed` + `exitCode 0`, then agent message and `turn/completed` completed.
- static schema check: every key of `requestHandlers`, every notification the fake accepts, and every method in the fake's emitted-method list is present in the matching definition's `method` enums. A test fixture method name (e.g. a deliberate typo) would fail it — verified once by hand during RED.

## Documentation handoff

None required by the ticket. The documentation stage may add a `fakecodex` package overview beside `docs/knowledge/features/fakeclaude-binary.md` — pending for the documentation stage.

## Open questions

- Does the real server reply to `turn/interrupt` before emitting `turn/completed interrupted`? The fake replies first; consumers should not depend on the order. Resolve by recording what the implementation does.
