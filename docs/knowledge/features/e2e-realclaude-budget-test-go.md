# budget_test.go
- `budget_test.go` (#385) — seventh consumer of the trio. **Budget guardrails.** Two top-level tests pinning cost-relevant guarantees the suite did not previously exercise end-to-end. `TestRealClaude_CacheHitWarmsAcrossRuns` runs the same `RunOpts` skeleton twice through `RunPyryAgentRun` against `WithWorktreeAuthenticated(t)` and asserts the second invocation's trailer reports `Usage.CacheReadInputTokens > 0` (primary, `t.Fatalf`) — pinning Anthropic prompt-cache alignment within the 1-hour TTL when (system-prompt, allowed-tools, model, effort) are identical. A regression that breaks cache-key alignment (dynamic content in the system prompt, per-invocation tool-list churn) will show `== 0` here. A diagnostic-only check on the first run's `CacheCreationInputTokens > 0` uses `t.Errorf` (not `t.Fatalf`) so a sub-threshold system prompt surfaces as a soft signal rather than masking the primary on a passing run; the interpretation matrix (0+pass vs. 0+0) is documented in a comment above the check. `cacheHitSystemPrompt` is a deterministic 5-sentence string concatenation (~300 tokens) sized to clear Haiku 4.5's ~2048-token implicit-cache minimum by margin; the doc comment names the "no dynamic content" constraint (date, run id) explicitly because that is the regression class the test catches. `TestRealClaude_MaxTurnsHonored` runs `pyry agent-run --max-turns=2` against a prompt that natural-completion would require ≥5 turns (numbered 5-line Bash sequence with explicit "do NOT combine" guidance) and asserts five fields on the trailer: `Subtype == "error_max_turns"`, `TerminalReason == "max_turns"`, `NumTurns == 2` (exact — off-by-one fires here), `StopReason != "end_turn"`, `IsError == true`. **`ExitCode == 0` is correct** — `pyry agent-run` exits 0 on a successfully-emitted result trailer regardless of trailer `is_error`; the budget-exhaustion signal lives in the trailer fields, not the subprocess exit code, and a code comment pins this so a future maintainer doesn't "fix" the assertion to `!= 0`. Tool-call-collapse risk pinned in a comment above `maxTurnsPrompt`: if a future haiku revision is smart enough to fire all five `echo`s in one tool_use block (or otherwise complete in ≤2 turns naturally), the assertions fail loudly and the right fix is to bump the prompt to force more turns (e.g. 8 sequential `read X.txt` calls), not to weaken the assertion. Three-field extension to the `resultTrailer` struct in `tool_loop_test.go` adds `IsError bool`, `TerminalReason string`, and `Usage resultTrailerUsage` (plus the new `resultTrailerUsage` sub-struct emitting all four token-count fields); all `omitempty`-tagged so pre-#385 consumers (#376/#381/#382/#384) decode unchanged. **Fifth consumer of `parseResultTrailer`**. File-local `truncateStdout([]byte) string` mirrors the inline pattern from `tool_loop_test.go:127` (the existing file-local `truncate` from #381 stays untouched because the spec forbids touching `fixtures.go` and the existing helper is its own file's private). `RunOpts`: cache-hit uses `MaxTurns=1, AllowedTools=["Read"]`; max-turns uses `MaxTurns=2, AllowedTools=["Bash"]`; both `Effort="low", Model="claude-haiku-4-5"`. Three real haiku calls per run (2+1), ~$0.04 total with cache warm, matching the AC estimate. `t.Parallel()` is NOT called — matches the existing realclaude convention; also load-bearing on the cache-hit test where concurrent runs would muddy the "cache warmed by run 1 specifically" diagnostic. ~199 LoC, zero edits to `fixtures.go`, zero production-source changes. See [`codebase/385.md`](../codebase/385.md) for the design rationale.

  **#2223 needed a committed capture of this exact shape (`error_max_turns`,
  `TerminalReason == "max_turns"`) to pin `internal/streamsup`'s decode of
  claude's stop shape, and built no new probe for it.** `TestRealClaude_MaxTurnsHonored`
  proves this shape is still live and provokable, but a census of the
  already-committed `internal/e2e/realclaude/testdata/` found the same shape
  already sitting in `permission_mode_switch_v2.1.239_plan.json` (its `plan`
  arm happens to run `claude --max-turns 4`) — see
  [permission-mode-switch-inband-probe.md § A second, unrelated consumer](permission-mode-switch-inband-probe.md#a-second-unrelated-consumer-of-the-plan-capture-2223).
  **Before extending a live test to write a new capture, census the existing
  ones for the shape an AC actually names** — a fixture some other ticket
  already committed, for a different reason, can satisfy an evidence AC for
  free, and skips a live-gate ordering hazard: a reader that fatals on a
  fixture only the *live gate* produces reddens `make check`, which runs
  first (the trap #2089 hit and answered with a temporary skip). A capture's
  fitness for reuse turns on its **argv**, not the probe it was captured
  for — it must run the `claude` binary directly, never `pyry agent-run`,
  or the trailer could be pyry's own synthesised one rather than claude's.

- `interactive_bootstrap_liveness_test.go` (#854) — **not a `RunPyryAgentRun` trio
  consumer** (the only file in the suite that isn't): it drives the **daemon's
  interactive relay path**, not `pyry agent-run`. `TestInteractiveBootstrapLiveness`
  spawns a fresh real `pyry` daemon (real `claude --model haiku`) in an isolated
  authenticated HOME, seeds a deterministic bootstrap pool id + bound conversation
  (`seedBootstrapRegistry`/`seedBoundConversation`, transcribed from
  `internal/e2e/harness.go` per #861), pairs a headless phone over the encrypted
  Noise_IK v2 wire, and drives **two** turns — Turn 1 proves the bootstrap child
  creates its transcript and the reply bridge binds, Turn 2 proves the bridge
  survives past the first turn (the rotation/offset path). Each turn asserts only
  liveness (a non-empty streamed `assistant_delta` within a generous timeout;
  never claude's words). This is the RED/GREEN oracle for the #854 fix: on
  pre-fix `main` the bootstrap-bound reply resolves via a by-id resolver keyed on
  a pool id that never has a matching on-disk transcript, so Turn 1 times out.
  The daemon spawn routes its control socket through a transcribed
  `shortSocketPath` (the #860 `sun_path`-limit fix) — load-bearing for RED, since
  a `<home>/pyry.sock` under the long authenticated `t.TempDir()` HOME would
  overflow macOS's 104-byte limit and the daemon would never reach readiness,
  masking the deadlock. Known non-blocking gap (code review SHOULD FIX, not yet
  addressed): the drain correlates a turn by conversation id + non-empty text,
  not by `AssistantDeltaPayload.TurnID` — see [`codebase/854.md`](../codebase/854.md)
  for the failure scenario and the fix shape. Zero edits to `fixtures.go`. See
  [`codebase/854.md`](../codebase/854.md) for the production-fix half (which
  lives in `cmd/pyry`, not this package).

- `interactive_per_conversation_liveness_test.go` (#997) — sibling of #854,
  same interactive-relay shape but drives `create_conversation` over the wire
  first (`startPerConversationHarness`, `createConversationViaPhone`) rather
  than seeding a bound conversation, then proves liveness on the freshly
  created conversation. Exports the harness (`startPerConversationHarness`,
  `createConversationViaPhone`, `sealEnvelope`, `drainForReply`) that #1028
  below reuses for its verb-drive spine.

- `interactive_conversation_lifecycle_test.go` (#1028) — first real-`claude`
  coverage of the **conversation-management verbs**, not just liveness.
  `TestInteractiveConversationLifecycle` drives create → rename → archive →
  unarchive → delete on ONE conversation over the same encrypted channel
  against a freshly-spawned daemon on real `claude --model haiku`, with a
  `send_message` liveness turn inserted **between unarchive and delete**
  (deliberate: the metadata verbs then run on a quiescent wire, and proving
  liveness after the archive round-trip is the stronger claim that the live
  session survived it). Each verb's effect is asserted from an
  operator-observable signal — the `conversation_updated` reply's
  `Name`/`IsArchived` fields, and for delete both the `conversation_deleted`
  reply id and the on-disk registry no longer holding the row (new
  `readConversationIDsOnDisk` helper, ~25 lines). Test-only: reuses the #997
  harness (`startPerConversationHarness`, `createConversationViaPhone`,
  `sealEnvelope`, `drainForReply`) and the #854 turn drive/drain
  (`sealSendMessage`, `drainForAssistantReply`) verbatim; zero production
  files touched. The fake tier (`relay_v2_rename_test.go` #974,
  `relay_v2_delete_test.go` #975, `relay_v2_archive_test.go` #976) owns the
  verbs' detailed shape — this test is liveness/observable-state shaped only,
  proving the real interactive stack executes the verbs at all. See
  [`codebase/1028.md`](../codebase/1028.md).

- `interactive_modal_resolution_test.go` (#1030) — first real-`claude`
  coverage of the **modal-resolution verbs**, and the hardest of the #963
  families: the trigger is not a test env-var but real claude choosing to
  call a gated tool under default permission mode. `TestInteractiveModalResolution`
  drives one daemon spawned via the new `spawnPermissionDaemon` (byte-identical
  to `spawnBootstrapDaemon` #854 minus `--dangerously-skip-permissions` — that
  one omission is the entire trigger) through two sequential real-permission-modal
  cycles over one encrypted channel: **Phase A (answer)** sends a Bash-triggering
  prompt, drains to `modal_shown{Class:"permission"}` (asserted before
  resolving — non-vacuity), sends `modal_answer{allow_once}`, then proves the
  session proceeded via a subsequent non-empty `assistant_delta`; **Phase B
  (cancel)** raises a second modal and sends `modal_cancel`, observing the
  `modal_dismissed{cancelled,remote}` **broadcast** (`modal_cancel` is
  fire-and-broadcast — no reply is correlated to the cancel request). The new
  `drainForControlEvent` helper is a Type-only broadcast-drain sibling of
  #1028's `InReplyTo`-correlated `drainForReply`. The phone pairs WITH
  `--allow-remote-permissions` (the answer-side device gate). Reuses the
  #854/#997/#1028 harness (`bootstrapDaemon` plumbing, `driveHandshakeInteractive`,
  `sealSendMessage`, `sealEnvelope`, `drainForAssistantReply`) unchanged; zero
  production files touched. The fake tier (`relay_v2_modal_answer_test.go`
  #791, `relay_v2_modal_cancel_test.go` #1003, trust-class #993) owns the
  verbs' detailed shape and keystroke fidelity — this test is
  liveness/observable-state shaped only, proving the real interactive stack
  raises and resolves an actual permission modal at all. The `#798` daemon
  modal surfacer (see [modalbridge-package.md § Live daemon wiring (#798)](modalbridge-package.md#live-daemon-wiring-798))
  needed zero production changes. See [`codebase/1030.md`](../codebase/1030.md).
