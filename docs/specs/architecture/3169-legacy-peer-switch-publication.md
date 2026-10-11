# Stabilize legacy-peer switch publication (#3169)

## Files read

- `cmd/pyry/agent_switch_frames_test.go` → `TestRelayAgentSwitchEncryptedFrames`, `gatedRelaySwitch.waitCompleted`: bounded adapter completion currently follows legacy assertions.
- `cmd/pyry/agent_switch_relay.go` → `relayAgentSwitcher.SwitchAgent`: synchronous adapter returns after the primitive switch.
- `cmd/pyry/conversation_agent_switch.go` → `conversationAgentSwitcher.Switch`: deferred publication completes before exclusion releases and the adapter returns.
- `cmd/pyry/session_transition_v2.go` → `sessionTransitionEmitterV2.publishSwitch`, `Run`: publication includes fanout and `FlushPushes`.
- `docs/knowledge/features/conversation-session-binding.md` § Switching to the other agent: joining adapter completion protects both publication and subsequent switch admission.
- `docs/knowledge/features/development-verification.md` § Prove that tests distinguish the change: a queue barrier cannot prove an asynchronous producer has finished enqueueing.
- `CODING-STYLE.md` and `docs/protocol-mobile.md` § Security model: concurrency conventions and encrypted delivery threats.

## Change

Move the existing three-second `gate.waitCompleted(t)` call ahead of the legacy-peer workspace barrier in the shared two-direction switch loop. Adapter completion establishes that all outcome pushes have been published and flushed before the barrier can overtake them. Keep one completion receive per switch and preserve admission synchronization before the next request. Strengthen Claude restoration to require exactly inactive `resetting`, `banner`, `session_transition`, then `conversation_updated`; retain the existing Codex withholding and all capable-peer payload, ordering, persistence, refusal, failure, disconnect and diagnostic assertions. No production code, new types, goroutines or failure modes. No other feature branch touches the test file. Estimated written work: about 60 lines including this plan; 0 exported types, 0 consumer updates, 2 acceptance criteria, 0 new reject branches.

## Testing strategy

Run the existing encrypted-frame test before editing, then strengthen its legacy restoration assertion before moving the completion wait. Exercise Claude→Codex withholding and Codex→Claude restored ordering in `TestRelayAgentSwitchEncryptedFrames` using channel completion and the existing bounded frame reads, without sleeps. Run `go test -race -count=20 -run '^TestRelayAgentSwitchEncryptedFrames$' ./cmd/pyry/...`, `go test -race ./cmd/pyry/...`, `go vet ./...` and `go build ./cmd/pyry` (binary output outside the worktree). The verifier owns the full-module gate. Existing context cancellation and cleanup join the manager and transition emitter.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] The test uses existing Noise-decrypted `switchWire.read` envelopes; no production parsing or capability boundary changes.
- [Tokens, secrets, credentials] `switchWire.open` uses test-only paired tokens and random ephemeral keys; no real credentials or new secret handling.
- [File operations] Existing persistence fixtures remain under `t.TempDir`; no new paths or writes beyond the plan and test source.
- [Subprocesses] The change adds no child process or command construction; existing hermetic fixture lifecycle remains intact.
- [Cryptography] Existing X25519/Noise cipher states remain unchanged and sequentially consumed by `switchWire.read`; no key or nonce reuse is introduced.
- [Network and I/O] In-memory bounded channels carry encrypted fixture traffic. `waitCompleted` and frame reads retain their three-second bounds; no new socket or server.
- [Errors, logs, telemetry] Existing static refusal, failure and private-marker log assertions remain enabled; only test mismatch diagnostics change.
- [Concurrency] Waiting before the barrier prevents a false absence/restoration verdict while fanout is still running. The completion channel is buffered and the output channel holds this fixture's outcome frames; the wait introduces no cyclic dependency.
- [Threat model] Encrypted delivery and legacy capability withholding remain exercised against the existing protocol security model; production authentication, relay metadata and prompt-injection defenses are outside this test-only change.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-11
