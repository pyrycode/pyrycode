# #2556 — retarget cmd/pyry comments that cite the deleted ACP lane

Short plan: comment-only change, no new type, state or failure mode.

## Files read

- `cmd/pyry/interactive_turn_v2.go` → `eventKind` — the ToolCallDenied, CompactionBoundary, Banner, BackgroundTaskProgress, ThinkingProgress, RateLimited, ModelAnnounced, SessionFacts, ModelList and ContextUsage arms cite `acp_turn_stream.go` / `acpbridge`. The live `eventKind` callers are this file (the no-cursor drop, the unknown-event Debug, emitMapped's unmapped drop), `stream_turn_busy.go` and `stream_turn_drain.go`.
- `cmd/pyry/interactive_turn_v2_test.go` → comments above `TestInteractiveTurnEmitterV2_ThinkingProgressEventKindNamesTheVariant`, the rate-limited, model_announced, SessionFacts and ModelList eventKind tests.
- `cmd/pyry/jsonrpc_stdio.go` → `serveJSONRPCStdio` — closer and bridge goroutine comments cite `runACP`, `pyry acp` and `serveACP`, and the doc comment says "its one remaining caller is the daemon's MCP approval server". Callers are `runMCPApprove` (`mcp_approve.go`) and `runMCPFiles` (`mcp_files.go`), both one-shot stdio subprocesses with `defer cancel()` on a `signal.NotifyContext`.
- `cmd/pyry/mcp_approve.go` → stderr-logger comment ending "mirroring runACP".
- `cmd/pyry/args_test.go` → `TestParseClientFlags_ReturnsRest` says "the seam that runAttach relies on"; `parseClientFlags`'s rest is consumed by `runSessions` and every other client verb.
- `cmd/pyry/main.go` → `runSessionsRm` — the "Mirrors runAttach's exit-2 policy" sentence is already absent on main, so no edit is needed there.

## Change

Rewrite each stale reason to cite only sites that exist. In the `eventKind` arms and the test comments, "acp_turn_stream.go, stream_turn_busy.go, stream_turn_drain.go" becomes "stream_turn_busy.go and stream_turn_drain.go". The two "the ACP surface drops it via acpbridge's own default" sentences become the no-cursor drop plus those two files. In `serveJSONRPCStdio`, the doc comment names both callers, the closer waits for the `defer cancel()` in `runMCPApprove` or `runMCPFiles`, and the bridge justification names those one-shot verbs instead of `pyry acp` and `serveACP`. The `mcp_approve.go` logger comment says the setup matches `mcp-files` rather than `runACP`. `TestParseClientFlags_ReturnsRest` names the client verbs that apply arity rules to rest, such as `runSessions`. No code moves.

The references to `pyry acp` in `session_model_list.go` are outside this ticket's four names and its enumerated comments. They are left alone and mentioned in the PR.

## Testing strategy

No new logic. `go vet ./...`, `go build ./cmd/pyry`, and `go test -race ./cmd/pyry/...` confirm that nothing but comments changed. The acceptance grep is `rg -n 'acp_turn_stream|acpbridge|runACP|runAttach' cmd/pyry`, which should return nothing that presents those names as current.

## Documentation handoff

None. The ticket has no documentation handoff section.
