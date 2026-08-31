# #1918 — Emit the gated tool's `tool_use` block from fakeclaude's approve rider

**Size:** S · **Labels:** `enhancement`, `security-sensitive` · Split from #1914

## Files to read first

Generated from `codegraph_context` + `codegraph_impact`, pruned to what this change actually needs. This is the turn-1 data load; nothing below this list needs discovering by grep.

| Path | Symbol | What to extract |
|---|---|---|
| `internal/e2e/internal/fakeclaude/main.go` | `runStreamJSONApprove` | The read loop, the per-turn `msgID` / `toolUseID` mint, and the doc-comment sentence about "no assistant output is written mid-approval" that this change must keep true |
| `internal/e2e/internal/fakeclaude/main.go` | `dialApproval` | Where `ToolName` / `Input` are hardcoded today — both become consts read by two paths |
| `internal/e2e/internal/fakeclaude/main.go` | `writeVerdictResponse`, `writeAssistantEcho`, `writeJSONLine` | The existing writer chain; `writeAssistantEcho` and its three callers must end up **untouched** |
| `internal/e2e/internal/fakeclaude/main.go` | `outAssistant`, `outAsstMessage`, `outTextBlock` | The echo's envelope shape the new line mirrors — and the shared types the new line must not modify |
| `internal/e2e/internal/fakeclaude/main.go` | `writeInitializeAck`, `writeInterruptAck` | The file's `map[string]any`-through-`writeJSONLine` idiom, and the "escape via `json.Marshal`, never `fmt.Sprintf`" rule for anything reflected onto stdout |
| `internal/e2e/internal/fakeclaude/approve_test.go` | `TestWriteVerdictResponse` | The house style for these tests: assert through the **real** `streamsup.Parser`, never a hand-written decoder |
| `internal/e2e/internal/fakeclaude/stream_detect_test.go` | `parseEmitted` | The parser harness the new tests reuse verbatim |
| `internal/streamsup/parser.go` | `emitAssistant`, `streamBlock` | Exactly which keys a `tool_use` block must carry to become `turnevent.ToolStart` — `type`, `id`, `name`, `input`; everything else on the message is ignored |
| `internal/turnbridge/outbound.go` | `MapEvent` (the `turnevent.ToolStart` arm) | `ToolStart` → `protocol.TypeToolUse` / `ToolUsePayload{ToolUseID: e.ToolCallID, Name: e.Title}` — the frame the e2e asserts on |
| `internal/protocol/interactive.go` | `ToolUsePayload` | Field names for the e2e assertion, and the SECURITY paragraph on what `Input` values are (display strings, never capabilities) |
| `cmd/pyry/interactive_turn_v2.go` | `startTurnIfNeeded`, the emitter's `ToolStart` arm | Why a `ToolStart` opens the turn and the later text joins it — nothing new is needed to deliver the frame |
| `cmd/pyry/stream_turn_drain.go` | `startStreamTurnDrainV2` | The active-session gate the frame passes because the e2e already calls `seedBoundConversation` |
| `cmd/pyry/stream_turn_busy.go` | `toolCallDeltaFor`, `setBusy` | #1917's retention: `ToolStart` adds, the close arm sweeps — verified, so the missing `tool_result` leaks nothing |
| `internal/e2e/relay_v2_stream_modal_test.go` | `TestRelayV2_StreamModalPermissionRoundTrip` | The three await loops and the single `nextEnv` decrypt point — the one place a new frame can be recorded without an ordering assumption |
| `internal/control/server_test.go` | `shortTempDir` | `os.MkdirTemp("/tmp", …)` — the recipe that keeps a test's unix socket inside macOS's 104-byte `sun_path` limit |
| `internal/control/client_test.go` | `startMisbehavingServer` | The one-connection stub-socket idiom the new ordering test copies |
| `internal/control/protocol.go` | `Request`, `Response`, `ApprovePayload`, `ApproveResult` | What the stub server decodes and answers |
| `docs/knowledge/features/fakeclaude-binary-stream-json-mode.md` § "Approve rider" | — | The rider's house rules: default-off, `runStreamJSON` stays byte-identical, duplicate rather than widen |
| `docs/knowledge/features/agentrun-selfcheck-package.md` | — | The quoted permissions reference: the deny-default boundary lives *between* the `tool_use` emission and its `tool_result` — the position evidence |
| `docs/knowledge/features/permission-protocol-spike.md` | — | Why the committed captures pin only the block's *shape*: no gate fired in any of them |

## Context

`runStreamJSONApprove` originates one `control.Approve` per user turn and writes a verdict needle plus a `result` line. It emits **no** assistant `tool_use` block at all, so the daemon's parser produces no `turnevent.ToolStart`, and #1917's `turnBusyTracker` retention (`setBusy`'s in-flight map, fed via `toolCallDeltaFor`) stays empty for the whole approval.

That makes the fake tier structurally unable to prove the thing the next slices exist to prove. Any assertion of the form "this approval is parked on a conversation with that tool call in flight" would report negative whether the daemon is right or wrong — a test that cannot go positive, which reads as a pass. The same gap blocks the bridge-side report and #1911's delivery hold.

Real claude emits the block first and is gated second: the permissions reference quoted in `agentrun-selfcheck-package.md` puts the deny-default boundary between the `tool_use` emission and its `tool_result`, and every committed `permission_protocol_*` capture carries the Bash call as its own assistant line ahead of the `tool_result`. This slice adds that one line to the rider — not the capture's full `tool_use → tool_result → text → result` sequence.

No ADR is warranted: this is a harness fidelity fix inside an existing, documented rider.

### What the captures actually show

Re-read from `permission_protocol_v2.1.143_default.json` while writing this spec, because two details steer the design:

- The `tool_use` block rides **its own assistant line**, whose `content` is a single-element array. The message also carries `model`, `usage`, `stop_reason` and five more keys the rider will keep omitting — the parser's `streamMessage` reads only `id`, `role` and `content`, and the existing echo already omits them.
- Assistant lines belonging to **one** message share a message id (the capture's thinking-block line and its `tool_use` line both carry `msg_014Pkq…`). So reusing the turn's existing `m<N>` for the new line is a shape real claude produces, not an invention.

## Design

One production file changes: `internal/e2e/internal/fakeclaude/main.go`. Nothing outside the rider is touched.

### 1. One call, described once — two consts

`dialApproval` hardcodes its `ToolName` and its `Input` raw-string literal inline. Hoist both to package consts beside `approveDialTimeout` and the needles:

```go
// approveToolName / approveToolInput are the ONE gated call the rider describes.
// Both the tool_use block on the stream and the control.Approve payload read
// these, so the two paths cannot drift into describing two different calls.
const (
	approveToolName  = "Bash"
	approveToolInput = `{"cmd":"ls"}`
)
```

`approveToolInput` stays a `string` const, converted at each use site (`json.RawMessage(approveToolInput)`); a package-level `json.RawMessage` var would be a mutable shared slice. `dialApproval` reads the consts instead of its literals — its behaviour and its emitted payload bytes are unchanged.

**The `cmd` key is deliberately left as-is.** The captures spell Bash's input key `command`; nothing consumes this fake's input, and changing it would alter the approve payload for no observable gain. Out of scope, not an oversight.

### 2. A duplicated envelope for the `tool_use` line

`outAsstMessage.Content` is `[]outTextBlock`, and `writeAssistantEcho` — the only constructor of `outAssistant`, with three callers (`runStreamJSON`, `writeStreamResponse`, `writeVerdictResponse`) — is where that bites. Add a parallel typed family rather than widening the shared one:

```go
type outToolUseBlock struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}
```

plus the two envelope structs it needs (`outAsstToolMessage` with `id`/`role`/`content []outToolUseBlock`, and `outAssistantToolUse` with `type`/`message`), mirroring `outAssistant` / `outAsstMessage` field-for-field.

**Alternatives considered, so review need not re-litigate:**

- *Widen `outAsstMessage.Content` to `[]any`.* Marshals byte-identically for the echo, but AC 3's "bytes unchanged" would then rest on a marshal-equivalence argument rather than on nothing shared having been touched. Duplication buys the structural guarantee.
- *Build the line as a nested `map[string]any`*, the idiom `writeInterruptAck` / `writeInitializeAck` / `writeRateLimitEvent` use. Also leaves shared types untouched and is ~15 lines shorter, but Go sorts map keys, so the block would ship `id, input, name, type` instead of the captures' order, and the shape stops being compile-checked. Those writers use maps because they need absent-vs-present-false key semantics; this block has a fixed four-key shape and no such need.

The chosen shape is the same trade this file already made when it duplicated `runStreamJSON`'s read loop rather than widening its signature: duplicate over touching a byte-sensitive shared seam.

### 3. `writeAssistantToolUse` — the new writer

```go
func writeAssistantToolUse(w io.Writer, msgID, toolUseID string) error
```

Writes exactly one line: `{"type":"assistant","message":{"id":…,"role":"assistant","content":[{"type":"tool_use","id":…,"name":…,"input":…}]}}`, through `writeJSONLine`. Returns the first marshal/write error.

**The tool name and input are read from the consts, not taken as parameters.** That is the mechanism behind AC 1: the caller has no way to describe a different call from the one `dialApproval` sends. It is also the security posture — see § Security review, finding 1.

`msgID` is the turn's existing `m<N>`, per the capture note above. `turnevent.ToolStart` carries no message id at all, so the value is inert to the parser; reusing it avoids minting a second id scheme with no observable difference.

### 4. The rider

Inside `runStreamJSONApprove`'s user-turn arm, between the `toolUseID` mint and `dialApproval`:

- `writeAssistantToolUse(w, msgID, toolUseID)`; on error, `return` — same first-write-error-ends-the-loop rule the existing `writeVerdictResponse` call follows.
- then `dialApproval(socketFile, toolUseID)` and `writeVerdictResponse` exactly as today.

`writeAssistantEcho`, `writeStreamResponse`, `runStreamJSON` and every other mode are untouched.

**The doc comment must be corrected, not left to rot.** Its current last sentence — "control.Approve blocks it until the verdict lands, so no assistant output is written mid-approval; -race clean by construction" — describes code that no longer exists once one write moves ahead of the dial. Restate what stays true: the `tool_use` line is written **before** the dial and the verdict lines after it, so nothing is written *while* the dial is in flight and the single-goroutine `-race` property is unchanged. Add why the order is the point (the daemon must hold the retained call for the duration of the approval, not after it).

### Data flow, once the line exists

```
rider: tool_use line ─┐
                      │ child stdout (pipe)
                      ▼
        streamsup.Parser.emitAssistant  →  turnevent.ToolStart{ToolCallID: "tu-1139-1", Title: "Bash"}
                      │
                      ├─→ turnBusyTracker.observe  →  setBusy  →  retained in flight (#1917)
                      │
                      └─→ active-session gate  →  emitter ToolStart arm (opens the turn)
                                  │
                                  └─→ turnbridge.MapEvent  →  protocol.TypeToolUse{ToolUseID: "tu-1139-1"}  →  phone

rider: control.Approve(tool_use_id: "tu-1139-1") ── control socket ──→ permbridge → modal_shown → phone
```

Two transports, joined only by the id. Nothing orders them relative to each other, which is why the e2e asserts arrival and identity, never sequence.

## Concurrency model

Unchanged in production: `runStreamJSONApprove` still runs entirely on `main()`'s goroutine, one write, then a blocking dial, then two writes. No new goroutine, no shared state, no lock.

The only new concurrency is in the ordering test (§ Testing strategy, test 2): a stub unix-socket server on its own goroutine that snapshots the rider's output writer at request-arrival time. Two constraints, both load-bearing for `-race`:

- The writer handed to the rider must be **mutex-guarded**, with `Write` and the snapshot both under the lock — the rider writes from the test goroutine while the server reads from its own.
- The server goroutine must be **joined** (channel or `sync.WaitGroup`) before any assertion reads what it recorded, and its listener closed via `t.Cleanup` so the accept never outlives the test.

## Error handling

No new failure modes. The block's write joins the existing "first write error ends the loop" rule. A malformed `approveToolInput` const would surface as a `json.Marshal` error from `writeJSONLine` — compile-time-fixed content, so unreachable in practice, and it degrades to the loop returning rather than to a malformed line.

The fail-closed contract is untouched: `dialApproval` still maps every failure to `verdictError` → `approveErrorNeedle`, never `verdictAllow`. The block now precedes that mapping, so an approval that fails still leaves a `tool_use` on the stream — correct, and the same thing real claude does when a gate denies.

## Testing strategy

Scenarios, not code. All three live in existing files; no new test file.

**1. `internal/e2e/internal/fakeclaude/approve_test.go` — the rider's emitted shape, offline.**

Drive `runStreamJSONApprove` with one user-turn line, an empty `socketFile`, and an in-memory writer. `readApproveSocket` refuses an empty path immediately, so `dialApproval` returns `verdictError` with no dial and no timeout — deterministic and fast. Feed the output through `parseEmitted` and assert:

- exactly three events, in order: `turnevent.ToolStart`, `turnevent.TextChunk`, `turnevent.TurnEnd`
- `ToolStart.ToolCallID == "tu-1139-1"`, `ToolStart.Title == approveToolName`, and `ToolStart.RawInput` decodes to the same object as `approveToolInput`
- `TextChunk.Text == approveErrorNeedle` — the fail-closed path still maps an approval failure to the error needle, not to allow (AC 3)

Asserting through the real `streamsup.Parser` rather than a hand-decoder is `TestWriteVerdictResponse`'s discipline and the reason this pins AC 2: a block missing `name`, or nested wrongly, produces no `ToolStart` at all.

**2. `internal/e2e/internal/fakeclaude/approve_test.go` — the block precedes the approval, and describes the same call.**

The byte order alone cannot pin AC 1's "before it dials": a mutant that dials first, then writes the block, then the verdict, emits identical bytes. Observe from the socket side instead. Stand up a one-connection stub server (`startMisbehavingServer`'s shape) on a socket under an `os.MkdirTemp("/tmp", …)` dir (`shortTempDir`'s recipe — macOS caps `sun_path` at 104 bytes and `t.TempDir()` embeds the test name), write its path into the socket file, and in the handler:

- snapshot the mutex-guarded writer **first**, before decoding
- decode the `control.Request`, answer `Response{Approve: &ApproveResult{Behavior: "allow"}}`

Then, after joining the goroutine, assert:

- the snapshot already contains a parsed `tool_use` block — parse the snapshot through `parseEmitted` and require a `ToolStart`; a snapshot with no `ToolStart` is the mutant's signature
- that block's id, name and input equal the request's `ToolUseID`, `ToolName` and `Input` — the direct pin for "one call, not two"
- the rider's full output ends in the `approveAllowNeedle` echo, so the verdict path still works with the block in front of it

**3. `internal/e2e/relay_v2_stream_modal_test.go` — the frame reaches a paired client (AC 4).**

The frame almost certainly arrives **before** `modal_shown`, and every await loop in this test `continue`s past frames it does not match — so a naive assertion added to the drain loop would never see it. Record it at the single decrypt point instead:

- declare `toolUse protocol.ToolUsePayload` and `sawToolUse bool` above `nextEnv`
- inside `nextEnv`, immediately after `decryptInnerEnvelope` and before the return, record the first `protocol.TypeToolUse` envelope's payload
- after the existing drain loop completes, assert `sawToolUse`, `toolUse.ToolUseID == "tu-1139-1"`, `toolUse.Name == "Bash"`, and `toolUse.ConversationID == knownConvID`

Recording centrally is what makes this ordering-independent, per the ticket's explicit instruction not to assert the frame arrives before `modal_shown`. The post-drain assertion is nonetheless non-flaky: the block and the verdict text ride the same child stdout in that order, through one parser, one drain goroutine and one conn, and `nextEnv` is the single reader — so the frame is necessarily consumed before the needle that ends the loop.

`"tu-1139-1"` is a literal because the e2e cannot import `package main`; the test already hardcodes the rider's needles for the same reason. Comment it as `runStreamJSONApprove`'s per-turn id scheme at turn 1.

**No new test for AC 3's byte-identity half.** The design touches no symbol any other mode reaches, and the existing guards — `TestRunStreamJSON_BogusRiderOffIsByteIdentical`, `TestRunStreamJSON_RateLimitRiderOffIsByteIdentical`, `TestWriteStreamResponse_Shape`, `TestRunStreamJSON_SingleTurn` — already fail if the echo's bytes move. Adding a fourth would be a fifth place the same claim is made.

Gate: `make check` (all three tiers here are hermetic; the e2e runs under the fake-daemon suite).

## Open questions

- **Should a later slice add the `tool_result` line?** Not needed here — `setBusy`'s close arm sweeps the conversation's retained calls on `TurnEnd`, which the rider's existing `result` line produces, so the entry cannot leak. Verified against `setBusy` rather than assumed. A slice that wants to observe a call *resolving* rather than *starting* would add it then.
- **The `cmd` vs `command` input key** (§ Design 1) — left alone deliberately. If a future slice asserts on `ToolUsePayload.Input` field names, it aligns the key then.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings — by construction, and the construction is the point.** The child's stdout is a real boundary: bytes fakeclaude writes become `turnevent.ToolStart` in `emitAssistant`, land in `turnBusyTracker`'s retained set, and are forwarded to a paired phone as `protocol.ToolUsePayload`. `writeAssistantToolUse` takes **no** tool-name and **no** input parameter — both come from consts — so nothing inbound can steer either. The one parameter that varies, `toolUseID`, is minted by `fmt.Sprintf` from an `int` counter, never from a decoded line.
- **[Trust boundaries] MUST NOT regress — `Input` is the only unescaped field on the new line.** `json.RawMessage` is emitted verbatim; every other field is a `string` that `json.Marshal` escapes. Wiring that field to caller-controlled bytes would be a stdout line-injection vector — the failure `writeInitializeAck`'s `request_id` echo already documents ("built by string concatenation instead, that same value would split the output and fabricate an extra stream line the parser would consume as real"). The design forecloses it by making the field unreachable from any parameter; a future change that adds an `input` parameter must revisit this finding. Note that `writeAssistantEcho` **does** echo caller-controlled text — safely, because that field is a `string` — so the contrast is a live one in this file, not hypothetical.
- **[Subprocess / external command execution] No findings — nothing executes, and no one may start.** `approveToolInput` puts a shell-command-shaped literal on the wire for the first time from the fake tier. Nothing in this path runs it: fakeclaude does not, the daemon is a supervisor and never executes claude's tools, and `ToolUsePayload`'s own SECURITY paragraph binds a client to render such values as inert text and never open, execute, re-shell, or feed them to an HTML sink, an attribute, or a URL. `ls` under a key (`cmd`) no tool reads makes the payload inert in every direction.
- **[Concurrency] SHOULD FIX — the ordering test introduces the only cross-goroutine read in this change.** The stub server reads the rider's output while the rider writes it. § Concurrency model mandates a mutex-guarded writer with the snapshot taken under the lock, and a joined goroutine plus a `t.Cleanup` listener close. Without both, `make check`'s `-race` arm is the thing that catches it — which is why this is SHOULD FIX rather than MUST FIX, but code review should confirm the guard exists rather than the test merely passing once. Production concurrency is unchanged: one goroutine, no shared state.
- **[Resource exhaustion] No findings — verified, not assumed.** A `ToolStart` with no matching `tool_result` would pin an entry in `turnBusyTracker.inflight` forever if nothing swept it. `setBusy`'s close arm deletes the conversation's whole retained set on a closing turn, and the rider's existing `result` line produces that `TurnEnd`; the outer map key exists only while its inner set is non-empty. Read at `setBusy` while writing this spec.
- **[Error messages, logs, telemetry] No findings.** The drain logs `eventKind` — a variant name, content-free — and the new event adds no field. The new tests must keep failure messages to ids and event types; the daemon's socket path and any decoded payload stay out of CI output, matching `initialize_control_test.go`'s key-names-only discipline.
- **[File operations] No findings.** `readApproveSocket` is untouched. The tests create a socket dir via `os.MkdirTemp` (0700) with `t.Cleanup` removal; no user-controlled path is concatenated anywhere.
- **[Tokens, secrets, credentials] Not applicable.** No secret is minted, stored, logged or compared. `tool_use_id` is a correlation key, not a capability — the daemon authorizes an approval by `ModalID` validity plus the per-device gate, never by this id.
- **[Cryptographic primitives] Not applicable.** No randomness and no comparison against a secret is introduced. The e2e's Noise handshake path is unmodified.
- **[Network & I/O] No findings.** One additional stdout line per turn, of fixed constant size, through the parser's existing line handling. No socket read, no cap to choose, no timeout to set.
- **[Threat model alignment] No findings.** `tool_use` frames already ship to paired phones on every real turn, so this introduces no new client-visible data class — it makes the fake tier produce the one it was already missing. `docs/protocol-mobile.md` § `tool_use` is the governing contract and is unchanged by this slice.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-09-01
