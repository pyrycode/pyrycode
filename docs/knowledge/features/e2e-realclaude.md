# `internal/e2e/realclaude` — real-`claude`-binary integration suite

Sibling Go package to [`internal/e2e`](e2e-harness.md), gated by a distinct build tag so the real-`claude` trust-boundary suite is opt-in and never runs under `make test` / `make check`.

## Why a sibling, not part of `internal/e2e`

`internal/e2e` carries `//go:build e2e || e2e_install` and drives `pyry` against a fake-claude (`TestHelperProcess` or shell wrapper). That harness deliberately stops at the trust boundary with the real `claude` binary — useful for control-plane / supervisor coverage, but it can't catch the `/doctor` prompt-poisoning class of bug that broke Phase C on 2026-05-14.

`internal/e2e/realclaude` is the package where tests DO cross that boundary. Keeping it separate means:

- `make test` skips it via tag exclusion alone (no path filter).
- A future `make e2e` that picks up `e2e` / `e2e_install` won't accidentally pull real-claude tests in.
- Each suite's tag set documents its intent at the file header.

## Build tag

All files in the directory carry exactly:

```go
//go:build e2e_realclaude
```

Single tag, no alternation. The `e2e_install` precedent established the `e2e_<purpose>` naming.

## What's there today

## Test infrastructure

`fixtures_test.go` re-execs the test binary as a fake `pyry` when `GO_TEST_HELPER_PROCESS=1` is set (via a `TestMain` branch), and pins `PYRY_E2E_BIN=os.Args[0]` for every other test so `ensurePyryBuilt` short-circuits to the fake. The fake selects behaviour from `PYRY_E2E_FAKE_MODE` (`happy`, `fail`, `sleep`, `argv`). This lets the helper's contract be validated entirely from within the package — no real `claude` and no real `pyry` build are required for the helper's own tests. (The smoke test `TestClaudeBinaryAvailable` from #361 remains the only test in the suite that depends on real `claude` being on PATH.)

## Make target

```make
.PHONY: e2e-realclaude
e2e-realclaude:
	$(GO) test -tags e2e_realclaude ./internal/e2e/realclaude/...
```

No `-race`. These are I/O-bound trust-boundary checks, not goroutine-stress tests; flip on `-race` per-test when a future test in the directory does spin goroutines.

`make check` is unchanged. CI's per-PR `make check` does not run this suite — it stays opt-in for that path.

## CI cadence: code-review phase, no nightly workflow

The real-`claude` suite is NOT wired into GitHub Actions. It runs **locally
during the code-review phase** of every dispatched ticket via the pipeline
— see the code-review agent's `CLAUDE.md` for the invocation contract.

The earlier nightly workflow (`.github/workflows/e2e-realclaude-nightly.yml`, #362) was removed in #379 the same day it landed. CI-side rationale for the
removal:

- GitHub Actions would need an `ANTHROPIC_API_KEY` repo secret; Max-plan
  tokens used locally are free.
- Per-run cost ($0.10–$0.50, scaling with test count) buys nothing local
  runs don't already cover once code-review runs the suite on every PR.
- Failure surface synchronised to dispatch cadence beats unpredictable
  04:00 UTC failures.
- One fewer CI file to keep in lockstep with `self-check-daily.yml`.

The make target is unchanged — `make e2e-realclaude` is still the entry
point, just no longer invoked by CI.

## Verifying tag exclusion

After landing, `make test 2>&1 | grep realclaude` should be empty (or only an `ok ... [no test files]` line) — files with an unsatisfied build tag are dropped at the build stage, so the package compiles to an empty test binary.

## Related


## Sections

This overview is split across the documents below. Each is kept small so
search can reach it.

- [Operator authentication](e2e-realclaude-operator-authentication.md) — `make e2e-realclaude` only exercises the trust-boundary tests if the spawned `claude` subprocess can reach Anthropic's API. 
- [smoke_test.go](e2e-realclaude-smoke-test-go.md) — The composition pattern downstream tests use: `WithWorktree` → `RunPyryAgentRun` → `ReadJSONL`. 
- [allowed_tools_enforcement_test.go](e2e-realclaude-allowed-tools-enforcement-test-go.md) — see the document
- [resilience_test.go](e2e-realclaude-resilience-test-go.md) — see the document
- [sigterm_mid_tool_use_test.go](e2e-realclaude-sigterm-mid-tool-use-test-go.md) — see the document
- [budget_test.go](e2e-realclaude-budget-test-go.md) — consumer** (the only file in the suite that isn't): it drives the **daemon's interactive relay path**, not `pyry agent-run`. 
- [interactive_session_control_liveness_test.go](e2e-realclaude-interactive-session-control-liveness-test-go.md) — coverage of the **session-control respawn verbs**, and the last of the #963 families: `new_session` (rotate via `/clear`) and…
- [interactive_stream_resume_after_eviction_test.go](e2e-realclaude-interactive-stream-resume-after-eviction-tes.md) — real-`claude` proof that an idle-evicted **stream** session resumes via `--resume` with prior context intact, closing the last uncovered…
- [background_reach_probe_test.go](e2e-realclaude-background-reach-probe-test-go.md) — gate**; opt-in behind `PYRY_PROBE_BACKGROUND_REACH=1`, reusing #1223's staging rig verbatim (`background_trigger_probe_test.go` not edited;…
- [teardown_liveness_probe_test.go](e2e-realclaude-teardown-liveness-probe-test-go.md) — opt-in behind `PYRY_PROBE_TEARDOWN_LIVENESS=1` on top of the package's normal auth skip. 
- [trailer_admissibility_test.go](e2e-realclaude-trailer-admissibility-test-go.md) — probe**; two pure predicates that decide whether #1266's trailer scan and
- [trail_run_outcome_test.go](e2e-realclaude-trail-run-outcome-test-go.md) — `trailClassifyRun(trailRunReadings) trailRunOutcome` maps one probe run's raw observations onto exactly one of sixteen outcomes (four…
- [finding_staging_fill_test.go](e2e-realclaude-finding-staging-fill-test-go.md) — run's own transcript.** `finOutcomeStagingGate` (#1284, above) decides all seven staging outcomes from synthetic inputs; this file fills…
- [trailer_terminal_reason_test.go](e2e-realclaude-trailer-terminal-reason-test-go.md) — probe**; consumes #1357's `KeyNames` reading and answers a question neither it nor the decoded scalar can answer alone: what a trailer's…
- [finding_run_gather_test.go](e2e-realclaude-finding-run-gather-test-go.md) — decode #1313, published record's bound proven to be the classified sighting's #1316) — **parameterises `trailRigGather` (#1268) on the two…
- [finding_stage_held_group_test.go](e2e-realclaude-finding-stage-held-group-test-go.md) — (#1281) two parameters from a real held command, not hand-passed integers.** `finStageHeldGroup` stages `sh -c '"$1" "$2"; exit 0'` over a…
- [finding_live_run_test.go](e2e-realclaude-finding-live-run-test-go.md) — staging driver, not a probe of pyry itself**; `finLiveRunStage(t, envDelta) *finLiveRunHandle` spawns pyry on the runner path its caller's…
- [finding_stream_exit_path_probe_test.go](e2e-realclaude-finding-stream-exit-path-probe-test-go.md) — `PYRY_USE_STREAMJSON=1` structural sibling of #1337, and only that path**: the two runners do not write the same trailer and do not present…
- [interactive_stream_inband_model_test.go](e2e-realclaude-interactive-stream-inband-model-test-go.md) — a new feature under test**: #1581 changed `Pool.UpdateSettings` to deliver a model/effort-only change by writing `/model <value>` as an…
- [inband_bypass_revoke_arms_test.go](e2e-realclaude-inband-bypass-revoke-arms-test-go.md) — half of #1643's three-arm substrate: which stored posture each arm launches with.** #1622's `seedBypassRegistry` wrote `yolo:true`…
- [interactive_stream_model_announced_test.go](e2e-realclaude-interactive-stream-model-announced-test-go.md) — real claude's announced model reaches the daemon's **own emitted frame**, not just claude's stdout. 
- [interactive_stream_announced_reset_test.go](e2e-realclaude-interactive-stream-announced-reset-test-go.md) — the real-claude proof that a live `/clear` draws the announced-reset delimiter (one `session_transition{reason:"clear"}`) and the client keeps receiving a live stream afterwards, closing the gap between the fakeclaude-only family (#2134/#2135/#2136) and what a real claude actually does.
- [initialize_control_names_test.go](e2e-realclaude-initialize-control-names-test-go.md) — fourth fixture-name lock in this package. 
- [initialize_control_record_test.go](e2e-realclaude-initialize-control-record-test-go.md) — **the record half of the `initialize` fixture family: fixes the JSON contract #1688's live capture, #1690's decoder and #1692's fake all…
- [initialize_control_window_test.go](e2e-realclaude-initialize-control-window-test-go.md) — send-point window #1723 defined and left unpopulated: a pure, total function over `[]json.RawMessage` and one anchor, returning the…
- [initialize_control_probe_test.go](e2e-realclaude-initialize-control-probe-test-go.md) — **the live run family for the `initialize` fixtures: one real child, one tool-free probe turn, one `control_request` with subtype…
- [initialize_control_redaction_test.go](e2e-realclaude-initialize-control-redaction-test-go.md) — the `initialize` fixture family, proved over raw bytes with no record involved.** `newInitControlRedactor(operatorHome, tempHome, workdir,…
- [initialize_control_probe_test.go](e2e-realclaude-initialize-control-probe-test-go-2.md) — beside the redactor at the capture site and wires it to the fifth field described above.** `newDropcapScanner(home, "", workdir)` — the…
- [initialize_control_compare_test.go](e2e-realclaude-initialize-control-compare-test-go.md) — two arms that send the `initialize` control request compared against the arm that sends none, at the same turn index, over #1763's three…
- [ask_user_question_record_test.go](e2e-realclaude-ask-user-question-record-test-go.md) — the record half of the `AskUserQuestion` capture family: a four-field fixture record, pinned by a hand-written ordered-name literal, proved offline with no claude binary and no credentials.
- [ask_user_question_names_test.go](e2e-realclaude-ask-user-question-names-test-go.md) — the name half of the `AskUserQuestion` capture family: a one-input namer locked against all four committed fixture families, proved offline with no claude binary and no credentials.
- [ask_user_question_writer_test.go](e2e-realclaude-ask-user-question-writer-test-go.md) — the writer half of the `AskUserQuestion` capture family: a directory-injectable writer that refuses a record before any filesystem call when its marshalled bytes carry a denied value, proved offline with no claude binary and no credentials.
- [ask_user_question_shape_test.go](e2e-realclaude-ask-user-question-shape-test-go.md) — the shape half of the `AskUserQuestion` capture family: a findings-returning check over the decoded `tool_input`, its first two checks (tool name, at least one question) each with a negative row, proved offline with no claude binary and no credentials.
- [ask_user_question_capture_test.go](e2e-realclaude-ask-user-question-capture-test-go.md) — the live half of the `AskUserQuestion` capture family: spawns a real claude with the daemon's permission flags, taps the call on the approve path via a stub control socket, denies every call, and commits the record.
- [ask_user_question_reader_test.go](e2e-realclaude-ask-user-question-reader-test-go.md) — the read half of the `AskUserQuestion` capture family: a deterministic, credential-free glob-and-scan-and-decode pass over the committed capture, closing the "gate ran green, artifact never landed" gap the live half alone can't.
- [interactive_stream_question_answer_test.go](e2e-realclaude-interactive-stream-question-answer-test-go.md) — the answer half of the `AskUserQuestion` round trip: drives a real claude to ask, answers it through the daemon's own inbound path, and proves the answers (not just an allow) reached claude.
- [interactive_stream_question_refusal_test.go](e2e-realclaude-interactive-stream-question-refusal-test-go.md) — the refusal half: refuses the surfaced batch through the daemon's own inbound path and proves claude neither answers its own question nor presses on into the work it was blocking.
- [interactive_stream_attachment_read_test.go](e2e-realclaude-interactive-stream-attachment-read-test-go.md) — the live proof that claude opens an attachment's on-host path and echoes its contents, closing the second half of the 2026-05-16 attachment-prompt decision.
- [tool_result_sidecar_probe_test.go](e2e-realclaude-tool-result-sidecar-probe-test-go.md) — the `toolUseResult` sidecar the transcript names arrives on claude's stdout too, but spelled `tool_use_result`, snake_case not camelCase; a one-spelling probe reported the inverse finding first.
- [tool_progress_capture_test.go](e2e-realclaude-tool-progress-capture-test-go.md) — the live `tool_progress` capture: a from-scratch FIFO-hold helper found a `sync.Once`-cleanup hang that killed a 20-minute gate run, and the probe arms on the committed fixture's absence rather than an env var so `make e2e-realclaude` doesn't skip it vacuously.
- [compaction_capture_test.go](e2e-realclaude-compaction-capture-test-go.md) — the live `/compact` capture: confirmed the seam (`system/status` + `system/compact_boundary`) but the dispatcher's gate-only run never commits, so the 2026-09-08 fixture fired and was lost — read before assuming #2227/#2228 have bytes to read.
- [parent_tool_use_capture_test.go](e2e-realclaude-parent-tool-use-capture-test-go.md) — the live `parent_tool_use_id` capture: runs sonnet, not this family's usual haiku, because the probe's whole premise is that claude delegates rather than inlining two reads — a cheaper model produces a green run with no subagent in it. Fixture not yet committed as of #2191's landing.
- [task_notification_capture_test.go](e2e-realclaude-task-notification-capture-test-go.md) — the live `task_notification` capture: a FIFO must be released mid-test, not just held, because the subtype only fires on a background task's terminal state; a wait keyed on the companion subtype's `resultSeen` latch is the wrong wait for an event that outlives the turn; and a record field documenting the deny-scan's own literal needles can fail the scan it describes. Fixture not yet committed as of #2247's landing.
- [effort_init_capture_test.go](e2e-realclaude-effort-init-capture-test-go.md) — the live `system/init` capture with an effort actually set on both of the daemon's paths: **`effort` is absent at 2.1.259 even with `--effort low` on the launch argv and `/effort high` acknowledged in band**, settling #2195/#2252's open question in favour of dropping the field. Also the `dropcapRedactor` late-adder pattern (`addValueClass`/`addPathClass`) for values only claude can supply, and the zero-nonce footgun in `strconv.FormatInt`. Fixture not yet committed as of #2251's landing; the finding above comes from the live gate's log, not committed bytes.
- [operator_system_lines_capture_test.go](e2e-realclaude-operator-system-lines-capture-test-go.md) — the live capture of the four unmapped operator-facing `system` subtypes: **`informational` is observed and decodes cleanly (and proves a `UserPromptSubmit` hook runs under `--dangerously-skip-permissions`), `local_command_output` fires as assistant prose rather than a `system` line at all, `commands_changed` stays inconclusive, `notification` has no known trigger.** Also: claude re-broadcasts the full slash-command inventory on every `system/init` line, not just the first, and a slash-command trigger must be picked from a committed init line's inventory rather than hardcoded. Fixture not yet committed as of #2255's landing — lost in the dispatcher's own gate-only worktree, the fourth capture in this family to land that way.
- [api_retry_capture_test.go](e2e-realclaude-api-retry-capture-test-go.md) — the live `system/api_retry` capture, staged by redirecting `ANTHROPIC_BASE_URL` at a rig-owned listener that answers every request 529: fired, ten lines, `message` absent so the mapping needs no `permission_denied`-style gate, and **a retried-out turn closes as `result`/`success` with `terminal_reason: "api_error"`** — subtype alone misreads it as success. Also: a request count alone is not a retry count (check the spawn floor, and repeats within one endpoint, not the raw total), and this is the first probe in the family whose double artifact-directory write actually got exercised and recovered a fixture the gate's own worktree lost.
- [e2e-harness.md](e2e-realclaude-e2e-harness-md.md) — see the document
- [1415](e2e-realclaude-related-tickets-1415-1439.md) — see the document
- [1440](e2e-realclaude-related-tickets-1440-1447.md) — see the document
- [1448](e2e-realclaude-related-tickets-1448-1458.md) — see the document
- [1459](e2e-realclaude-related-tickets-1459-1463.md) — see the document
- [1353](e2e-realclaude-related-tickets-1353-1428.md) — see the document
