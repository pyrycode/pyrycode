# #2669 — conversation_updated carries agent to multi_agent clients

## Files read

- `internal/protocol/conversations_write.go` → `ConversationUpdatedPayload` — the record every producer marshals; gains `Agent`.
- `internal/relay/v2session.go` → `forwardEnvelope`, `mergedForConn`, `forwardAppReply` — the two seal sites a `conversation_updated` reaches; `mergedForConn` is the per-conn-copy precedent (#2652).
- `internal/relay/v2session_agentgate.go` → `withheldFromConn`, `pushedConversationID` — #2644's Run-goroutine gate on `s.multiAgent`; the new helper lives beside it.
- `internal/relay/v2session_seams.go` → `V2SessionConfig.CodexConversation`, `MergedModelOptions` — the optional-seam shape the new seam mirrors.
- `internal/relay/handlers/list_conversations.go` → `AgentOf` — the one resolution rule for a conversation's agent.
- `cmd/pyry/relay.go` → `codexConversation` and its wiring in the V2SessionConfig literal — the builder the new seam's constructor mirrors.
- `cmd/pyry/codex_conversation_test.go` → `TestCodexConversation_FromRealPool` — the real-pool test the new constructor's test copies.
- `internal/relay/v2session_agentgate_test.go` → `openGateConn`, `gateFramesUntil`, `gateCodexSeam` — the harness for the push-side tests.
- `internal/relay/v2session_modal_test.go` → `openModalConn`, `sealAppFrameConn` — send+recv conn helpers for the reply-side test.

Overlap: stale branch `feature/449` (2026-05) touches `v2session.go`; no dependency, edits here are local.

## Context

`list_conversations` rows carry `agent` for capable clients since #2643. `conversation_updated` does not, so a client that replaces its row with each update loses the agent on a rename, and the switch verb (#2674) needs this frame to announce an agent change. Eight sites build the frame; tagging it at the two seal sites instead keeps one rule and makes an old conn's bytes untouchable by construction.

## Design

**Protocol.** `ConversationUpdatedPayload` gains `Agent string \`json:"agent,omitempty"\``. No producer sets it, so every frame a producer marshals is byte-identical to today.

**Seam.** `V2SessionConfig.ConversationAgent func(conversationID string) (agent string, ok bool)`. `ok` false for an unknown conversation. Optional: nil changes nothing. Called on Run, per `conversation_updated` to a capable conn.

**Wiring.** `cmd/pyry/relay.go` gains `conversationAgent(convReg *conversations.Registry, harnessFor handlers.SessionHarnessFunc) func(string) (string, bool)` beside `codexConversation`: nil when either half is nil; else a registry `Get`, then `handlers.AgentOf`. Wired as `ConversationAgent: conversationAgent(w.convReg, w.sessionHarness)`.

**Relay, push side.** `agentTaggedForConn(s *V2Session, env protocol.Envelope) protocol.Envelope` in `v2session_agentgate.go`: returns env unchanged unless `s.multiAgent`, the seam is wired and `env.Type == conversation_updated`. Decodes the payload into `ConversationUpdatedPayload`, asks the seam for `p.ID`, sets `Agent`, re-marshals into a fresh `env.Payload` (never writes through the shared bytes). A payload that does not decode, a seam miss, or a marshal failure returns env unchanged. No log. `forwardEnvelope` calls it after `mergedForConn`.

**Relay, reply side.** `agentTaggedReply(s *V2Session, frame json.RawMessage) json.RawMessage`: the same three-part gate first (so an old conn's reply is never even decoded); decodes the frame as `protocol.Envelope`, returns frame unchanged unless its type is `conversation_updated`; runs `agentTaggedForConn`, re-marshals the envelope. Any decode/marshal failure returns the original frame. `forwardAppReply` applies it to `reply.Frame` just before `Encrypt`, after the not-open and transport-down drops.

## Concurrency model

Both helpers run on Run, where `s.multiAgent` is owned (as in `withheldFromConn`). The seam takes the registry mutex and the pool's harness read, the same two locks `CodexConversation` already takes on Run. No new goroutines.

## Error handling

Every failure (undecodable payload or frame, unknown conversation, marshal error) delivers the frame as it was — the pre-#2669 behaviour. Nothing is logged: the payload holds user-authored names.

## Testing strategy

- `internal/relay/v2session_agentgate_test.go`:
  - pushed `conversation_updated` for a Claude and a Codex conversation, capable + old conn on one daemon: capable conn's payload carries the seam's agent; old conn's payload is byte-identical to the pushed bytes (also proves no write-through).
  - rename-shaped reply: a stub handler replies `conversation_updated` for the conversation named in the request; for Claude and Codex, capable conn sees `agent`, old conn sees the handler's exact bytes.
  - unwired seam: capable conn receives the pushed bytes unchanged.
- `cmd/pyry` test: `conversationAgent` over a real pool (as `TestCodexConversation_FromRealPool`): Claude-bound, Codex-bound, unbound → claude, unknown → miss; nil halves → nil seam.

## Open questions

- Unknown conversation: leave the frame untagged (seam `ok` false) rather than defaulting to Claude — a frame about a conversation the registry no longer holds has no truthful agent. Resolved as such.

## Documentation handoff (pending — documentation stage)

- `docs/protocol-mobile.md` § `conversation_updated` row: every frame of both kinds carries `agent` to a conn that negotiated `multi_agent`, resolved as the `conversations` row's `agent` is; absent for an older conn.
- A changelog entry.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — both decoded inputs are daemon-authored: pushed envelopes come from the relay's own emitters, and `forwardAppReply`'s frame is a handler's marshalled reply, never the phone's bytes. The conversation id the seam is asked about is the one the producer wrote from the registry. The helpers only add a field; they never widen who receives a frame.
- [Disclosure] No findings — the agent is already on every capable conn's `list_conversations` row (#2643), so tagging widens no audience. An old conn is gated out before any decode (`agentTaggedForConn` / `agentTaggedReply` check `s.multiAgent` first), so its bytes cannot change; `withheldFromConn` runs before `agentTaggedForConn` in `forwardEnvelope`, so a Codex frame an old conn must not see is still dropped untouched.
- [Tokens, secrets] No findings — no key, token or nonce material is touched; tagging happens on plaintext before `Encrypt`.
- [File operations / subprocess] No findings — neither exists in the design.
- [Cryptographic primitives] No findings — in `forwardAppReply` the tag runs after the not-open and transport-down drops, so a dropped reply spends no extra work and no nonce; every failure path returns the original plaintext for the same single `Encrypt`, never an unsealed frame.
- [Network & I/O] No findings — the frame grows by the ~18-byte `"agent":"claude"` key; the payload's other fields are bounded on their write paths, so the sealed frame stays far under the app-envelope cap.
- [Logs] SHOULD FIX (implement in Phase B) — neither helper logs anything; the payload carries user-authored names (#833). Verifier checks the helpers contain no logger call.
- [Concurrency] No findings — Run-goroutine only, like `mergedForConn`; the pushed envelope is shared with other conns and the replay ring, so the capable conn's copy is built from fresh bytes and `env.Payload` is only reassigned, never written through. The seam takes the registry mutex and then the pool's harness read sequentially (`AgentOf` runs after `Registry.Get` returns), the same pair `CodexConversation` already takes on Run — no nesting, no new ordering.
- [Threat model] OUT OF SCOPE — the agent-switch announcement itself (#2674) consumes this frame; its own review covers switch authorisation.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-25
