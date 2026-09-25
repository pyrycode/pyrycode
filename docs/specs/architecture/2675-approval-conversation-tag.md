# #2675 — Tag approval prompts and question batches with the conversation that asked

## Files read

- `internal/permbridge/permbridge.go` → `Request` — the parked value both handlers build and `Surface` receives. Gains a `json:"-"` `SessionID`.
- `cmd/pyry/streamsup_runner.go` → `stdioPermissionHandler`, `newStdioPermissionHandler`, `handle`, `newStreamRunnerFactory` — the Claude park site; the factory holds the runner's `streamSessionTag` (`tag`).
- `cmd/pyry/codex_runner.go` → `codexApprovals`, `newCodexApprovals`, `handle`, the codex `RunnerFactory` closure — the Codex park site; the factory holds `tag` and passes it as `codexRunnerConfig.Tag`.
- `cmd/pyry/stream_turn_drain.go` → `streamSessionTag.ID` — the live id, moved by `Rotate`/`CompareAndSwap`.
- `cmd/pyry/modal_resolve_v2.go` → `streamApprovalBridge`, `Surface`, `surfaceQuestion`, `ApprovalParked` — where the tag is chosen today (`b.activeConv()`); `ApprovalParked`'s doc already records why the cursor misattributes.
- `cmd/pyry/relay.go` → the `newStreamApprovalBridge` wiring block in the v2 leg, `conversationForSession`, `codexConversation` — where the resolver is set, the resolver itself (matches current id and `SessionHistory`), and the #2644 seam the delivery gate consumes.
- `cmd/pyry/main.go` → `activeConversation.set`, `CurrentConversation` — the cursor the session router moves on every routed message.
- `internal/relay/handlers/list_conversations.go` → `AgentOf`, `SessionHarnessFunc` — the rule `codexConversation` applies.
- `docs/specs/architecture/2644-codex-frames-multi-agent-only.md` — `withheldFromConn` withholds a pushed frame whose `conversation_id` the `CodexConversation` seam reports Codex; `modal_shown` and `question_shown` are keyed on `conversation_id`.
- Tests: `cmd/pyry/stream_approval_test.go` → `newQuestionFixture`, `questionInput`, `lastModalShown`, `lastQuestionShown`, `fakeInteractiveBcast`; `cmd/pyry/codex_approval_test.go` → `newApprovalCodexRunner`; `cmd/pyry/codex_runner_test.go` → `newTestCodexRunner`, `fakeCodexBin`; `cmd/pyry/stream_context_usage_test.go` → the shell-script fake child driven through `newStreamRunnerFactory`; `cmd/pyry/codex_conversation_test.go` → registry setup.

No in-flight feature branch touches these files.

## Context

`Surface` and `surfaceQuestion` stamp `b.activeConv()` — the conversation the router last routed a message to — onto `modal_shown` and `question_shown`. Since #2644 that stamp also decides delivery: a frame tagged with a Codex conversation is withheld from conns without `multi_agent`. A Codex approval parked while the cursor sits on a Claude conversation is therefore tagged Claude and reaches an old client (which may answer it since #2605), and a Claude approval parked while the cursor sits on a Codex conversation is withheld from that client. The parked request never says which session asked, although each handler belongs to exactly one runner.

## Design

### `permbridge.Request.SessionID string \`json:"-"\``

The pool session id of the runner that parked the request, set by the daemon-side handler at park time. `json:"-"` keeps it off every wire and out of every decode. The control-socket approval path (`pyry mcp-approve`) builds its `Request` field by field from the control protocol's own struct, which has no session field, so it always leaves it empty. Doc: empty means "no session known" and the surfacer falls back to its default.

### Handlers carry their runner's tag

- `newStdioPermissionHandler(registry, timeout, surface, tag *streamSessionTag)`; the factory passes its `tag`. `handle` sets `parked.SessionID = h.tag.ID()` before `Register`.
- `newCodexApprovals(registry, timeout, surface, tag *streamSessionTag)`; the codex factory passes its `tag`. `handle` sets `parked.SessionID` the same way.
- Read at park, not at construction, so a rotated session reports its current id. A nil tag (unit tests that do not care) leaves the field empty — one `if tag != nil` guard per handler.

### Bridge resolves the id

- New field `streamApprovalBridge.sessionConv func(sessionID string) (conversationID string, ok bool)`, set after construction at the one production site, beside `questions` and `waker`, for their reason (constructor call-site count). nil ⇒ today's behaviour.
- New method `conversationFor(sessionID string) string`: `sessionConv(sessionID)` when the field is set and reports ok; otherwise `b.activeConv()`.
- `Surface` stamps `b.conversationFor(req.SessionID)` in `RecordWithContext`, and passes the resolved id into `surfaceQuestion`, which gains a `conversationID string` parameter and stamps it in `questions.Record` instead of calling `activeConv()`.
- `ApprovalParked` is unchanged.

### Wiring (`relay.go`)

`bridge.sessionConv = func(sid string) (string, bool) { return conversationForSession(w.convReg, sid) }` — the same closure shape the session-transition producer uses in the same leg. `conversationForSession` returns `("", false)` for an empty id, so a request with no session (control-socket path, nil tag) and a session bound to no conversation (the bootstrap before its conversation exists) both fall back to the cursor, which is today's tag.

## Concurrency model

No goroutines, no new locks. `sessionConv` runs on the control-server/await goroutine that calls `Surface`, before `b.mu` is taken, and takes only the conversations registry mutex inside `List()`. Nothing holds that mutex while calling into the bridge, so no ordering edge appears. `tag.ID()` is an atomic load. The field is written once before `mgr.Run` starts, like `questions`.

## Error handling

No new error values. Every miss (nil resolver, empty id, unbound session, unknown session) falls back to `activeConv()`, i.e. exactly today's behaviour. No new log lines.

## Testing strategy

New file `cmd/pyry/approval_conversation_tag_test.go`:

- `TestApprovalConversationTag_FollowsParkingSession` (AC 1–3). A real conversations registry holding `conv-claude` bound to `s-claude` and `conv-codex` bound to the codex runner's session. A real `activeConversation`, `set("conv-claude")` as the router does on a message to the Claude conversation. A bridge over a recording broadcaster with `questions` and `sessionConv` wired as production does; one `approvalSurfaceReport` set to `bridge.Surface`, shared by both runners.
  - Codex: `newTestCodexRunner` with `Approvals` from `newCodexApprovals(..., tag)`; a `[fakecodex:approval]` turn parks a command approval.
  - Claude: `newStreamRunnerFactory` with `stdio: true` over a shell-script fake child that emits one `can_use_tool` `AskUserQuestion` control request after `initialize`.
  - Assert the `modal_shown` carries `conv-codex` and the `question_shown` carries `conv-claude`. On main the modal carries `conv-claude` (RED).
  - AC 3: feed both stamped ids through `codexConversation(reg, harnessFor)` — the exact seam `withheldFromConn` consults — with `harnessFor` reporting the codex session as `codex`: the modal's id reads Codex (withheld from a conn without `multi_agent`), the question's reads Claude (delivered). #2644's relay tests already prove the gate withholds exactly the frames that seam reports.
- `TestStreamApprovalBridge_ConversationFallsBackToCursor` (AC 4), table over `Surface` for both a permission and a question: resolver unset; `SessionID` empty; `SessionID` bound to no conversation — each stamps the cursor's id.
- Existing call sites of `newStdioPermissionHandler` and `newCodexApprovals` in tests pass `nil` for the tag.

## Open questions

- Does the fake Claude child need to answer the `initialize` request before the parser routes `can_use_tool`? Resolve while writing the test; the script can emit the request right after reading the first line either way.

## Revisions

- 2026-09-25, Phase B: the open question is resolved. The fake Claude child does not need to answer `initialize`; the parser routes the `can_use_tool` request the script prints right after reading it. No design change. The test reuses `chanBcast` from `stream_turn_drain_test.go` rather than adding a second channel broadcaster.

## Documentation handoff (pending — documentation stage)

None required by the ticket. `docs/protocol-mobile.md` already says `modal_shown`/`question_shown` carry the conversation they concern; this makes that true. The documentation stage may note in the relay/permbridge overview that the stamp is the parking session's conversation, with the cursor as fallback only.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. `Request.SessionID` is set only by `stdioPermissionHandler.handle` and `codexApprovals.handle` from their own runner's `streamSessionTag`. It is tagged `json:"-"`, and the control server builds its `Request` field by field from a wire struct with no session field, so a local control-socket client cannot supply one; that path falls back to the cursor exactly as today. Nothing claude- or Codex-authored reaches the field: the tag is the pool's id.
- [Trust boundaries — direction of failure] No findings, with one residual stated. A resolver miss falls back to `activeConv()`, today's tag. The only way a Codex approval could still be tagged with a Claude conversation is a Codex session bound to no conversation; Codex sessions are created bound to their conversation, so none exists today. If one ever appears it is no worse than main. Failing closed (withholding on a miss) would hide the bootstrap's Claude approvals from old clients, which AC 4 forbids.
- [Rotation] No findings. The id is read at park time, and `conversationForSession` matches `SessionHistory` as well as the current id, so a rotation after park still resolves to the same conversation. A rotation racing the park can only produce an id not yet rebound, which misses and falls back to today's tag.
- [Tokens, secrets, credentials] No findings. None touched.
- [File operations] No findings. None.
- [Subprocess execution] No findings. No new process, no argv change.
- [Cryptographic primitives] No findings. Modal and batch id minting unchanged.
- [Network & I/O] No findings. Same frames, same payload shapes; only the `conversation_id` value changes to the correct one, which narrows what an old conn receives for Codex and restores what it receives for Claude.
- [Error messages, logs, telemetry] No findings. No log line is added; the resolver path logs no conversation id, session id or request content.
- [Concurrency] No findings. The resolver runs before `b.mu` is taken and takes only the conversations registry mutex, which is never held while calling into the bridge. `tag.ID()` is atomic. The field is written before `mgr.Run` starts.
- [Threat model alignment] No findings. Authentication, E2E encryption and pairing are untouched; the change makes the #2644 delivery gate act on the right conversation.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-25
