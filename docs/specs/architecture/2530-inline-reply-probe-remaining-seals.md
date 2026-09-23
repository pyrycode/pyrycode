# #2530 — probe transport-down at the four remaining inline reply seals

## Files read

- `internal/relay/v2session_replay.go` → `dropInlineReplyIfDown` — the #1526 pre-seal probe reused verbatim; logs slug, conn-id and reason only.
- `internal/relay/v2session_modal.go` → `newSessionReplyWorkspaceRefused`, `answerNewSession`, `handleNewSessionDone`, `deferNewSessionOutcome` — the late (#2477) path reaches the seal from Run's `m.newSessionDone` arm after the rotation.
- `internal/relay/v2session_systemprompt.go` → `emitSystemPromptReply` — push-failure branch deliberately logs no `err`; the drop line must stay equally content-free.
- `internal/relay/v2session_modelrequest.go` → `emitModelListReply`, `modelListReplyError`, `rejectModelListRequest` — the two model_list seals.
- `internal/relay/v2session_inlinereply_test.go` → `TestV2Session_InlineReply_TransportDown_BurnsNoNonce`, `runInlineReplyDownRow`, `inlineReplyRow` — the row harness and its content-free token check.
- `internal/relay/v2session_newsession_test.go` → `fakeLateSessionStarter`, `awaitCallback` — the late-starter double reused for the new_session row.
- `internal/relay/v2session_modelrequest_test.go` → `hostedConversations`, `resolveFixtureModelList`, `modelReqBoundConvID` — seams that reach the model_list report arm.

## Change

Add `if m.dropInlineReplyIfDown(s, "<slug>") { return }` at each of the four sites, after the reply envelope is built and before `forwardEnvelope`, never inside `forwardEnvelope` (#1526 AC3):

| Site | Slug |
|---|---|
| `newSessionReplyWorkspaceRefused` | `v2.new_session.err_dropped_transport_down` |
| `emitSystemPromptReply` | `v2.systemprompt.request.dropped_transport_down` |
| `emitModelListReply` | `v2.modellist.request.reply_dropped_transport_down` |
| `modelListReplyError` | `v2.modellist.request.err_dropped_transport_down` |

`emitSystemPromptReply` and `emitModelListReply` each log a Debug "served/reported" line just before `forwardEnvelope`. The probe goes **above** that line, so "served" still means "handed to the seal". Only a log call separates the probe from `forwardEnvelope`. It stays at the call site, as AC1 intends.

Nothing else moves. `dropInlineReplyIfDown` is unchanged. Its doc comment lists the sites it guards ("snapshot, settings, bundle-error, resync"). Refresh that list in the same edit so the comment does not go stale.

## Testing strategy

Extend `TestV2Session_InlineReply_TransportDown_BurnsNoNonce` with four rows under the existing nonce oracle (`runInlineReplyDownRow`):

- **system_prompt**: interactive caps, no `SystemPromptFor` wired (quiet reply is still a reply), trigger `request_system_prompt`.
- **model_list report**: interactive caps, `KnownConversation: hostedConversations`, `ModelListFor: resolveFixtureModelList`, trigger `request_model_list` for `modelReqBoundConvID`.
- **model_list error**: interactive caps, no `KnownConversation` ⇒ not-found reject ⇒ `modelListReplyError`.
- **new_session late**: interactive caps, `SessionStarter: newFakeLateSessionStarter()`. The trigger is sent with the leg **up**; `handleNewSession` seals nothing and parks the callback. The leg is then flipped down, and the callback fires `&RotatedWithoutWorkspaceError{…}` from the test goroutine. The outcome reaches Run through `m.newSessionDone` while the leg is down. This models the window the ticket names: the leg drops during the rotation.

Harness changes (test-only):
- A new optional row field `late func(t *testing.T, down func())`. When it is set, the harness sends the trigger with the leg up and hands the row a `down` closure. The row awaits the parked callback, calls `down()`, then fires the outcome.
- The content-free check removes the `event=<slug>` token from the line before it scans for forbidden tokens. The ticket's technical note says to narrow the check this way: `model` in `v2.modellist.…` would otherwise trip it. Every other field is still scanned for every token.

RED: rows run before the production edits, and each should fail on its own drop-line wait. GREEN after. Gate: `go test -race ./internal/relay/...`, `go vet ./...`, `go build ./cmd/pyry`.

## Documentation handoff

None. The ticket has no documentation-only acceptance criteria.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. The change adds no parsing and reads no inbound data. Each probe reads only `m.transportDown()`, which is daemon-owned, and `s.connID`. The untrusted inputs (the conversation ids in `request_system_prompt` / `request_model_list` / `new_session`) are handled upstream, and their handling is unchanged.
- [Tokens, secrets] No findings. The drop line carries no key material. `dropInlineReplyIfDown` logs slug, conn-id and reason only.
- [File operations] N/A. The design adds no filesystem access.
- [Subprocess] N/A. The design adds no exec.
- [Crypto primitives] This is the category the ticket exists for. The defect is nonce desync: a seal while down advances `s.send`'s nonce for a frame never delivered, and the next delivered frame fails AEAD (WS 4421). The probe runs before `forwardEnvelope` seals, so the down path never touches the CipherState. The nonce oracle in `runInlineReplyDownRow` asserts this per site. No new key or nonce use.
- [Network & I/O] No findings. Dropping a reply the relay could not have delivered changes no size limit or timeout. The phone's request goes unanswered, which is the existing posture for the six #1526 sites and the only outcome available while the leg is down.
- [Logs] No findings, with one constraint I am enforcing. The system_prompt site's rule is that the prompt reaches no log on any path. The model_list site's rule is that model values are never logged. Both hold because the drop line has three fixed fields. The test's content-free check still scans every non-slug field for `model`, `payload`, `err=` and the others. Narrowing it only removes the constant slug the daemon wrote itself.
- [Concurrency] No findings. All four seals run on Run, which is the single owner of `s.send`. The late new_session path reaches Run through `m.newSessionDone`, and `handleNewSessionDone`'s staleness guard still runs first. The probe needs no lock. The single-frame TOCTOU at the up→down instant carries over from #874 and is accepted there.
- [Threat model] No findings. After this change, I enumerated every `m.forwardEnvelope(` caller. `drainOnce` has the #874 probe. The snapshot, settings, bundle-error and resync sites have #1526's. These four have this ticket's. That leaves `drainReplayOnce`, and the `dropInlineReplyIfDown` doc explains why it is deliberately excluded: it handles transport-down on its own path so that it does not abandon the replay tail. No unprobed seal on Run remains.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-23
