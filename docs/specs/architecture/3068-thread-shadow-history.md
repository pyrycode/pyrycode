# Retained daemon shadow history evidence

## Files read
- `internal/e2e/realclaude/interactive_stream_running_turn_test.go` → `startStreamRunningTurnHarness`: authenticated daemon and legacy phone, isolated HOME and cleanup.
- `internal/e2e/realclaude/interactive_stream_subagent_text_test.go` → `nextSubagentTextEnvelope`: continuous receive nonce and parent-lane observations.
- `internal/e2e/realclaude/ordinary_queue_placement_test.go` → `TestRealClaude_OrdinaryQueuedPlacement`: ordinary acceptance and post-turn delivery.
- `internal/e2e/realclaude/prompt_answer_history_test.go` → `requirePromptAnswerHistory`: raw history inspection, including hidden facts.
- `internal/history/log.go` → `Entry`, `AppendWithMetadata`: source metadata and stable durable IDs.
- `internal/thread/fold.go`, `sends.go`, `store.go` → `Items`, `sendFact`, `Load`, `Snapshot`, `Shutdown`: acceptance identity, raw replay and joined workers.
- `docs/knowledge/features/thread-package-main-thread-folding.md` and `thread-package-agents-and-background-work.md`: recorded provenance and closure semantics; visible rows alone cannot seed continuation.
- `docs/knowledge/features/e2e-realclaude.md`, `development-verification.md`: missing evidence must fail offline; generated records require commit and counted results.
- `docs/knowledge/decisions/042-daemon-built-thread.md` → Migration order: shadow publishes no items.
- `internal/e2e/realclaude/dropped_line_capture_test.go` → `newDropcapScanner`: complete serialized-byte credential/path denial.

## Context
One evidence deliverable checks the existing daemon-built thread against authenticated legacy observations. No production changes or fold-rule changes. Existing agent/shell recorded replays retain their provenance and synthetic-context limits. Feature branches #2873 and #2882 do not touch the proposed new files.

## Design
New test-only files in `internal/e2e/realclaude` share a capture-pair schema, replay checks and deny scan. The tagged `TestThreadShadowHistoryCapture` arms when either exact artifact is absent. It reuses the existing harness, asks for main text/Bash/text and a foreground Agent child's marker, queues an ordinary marked message while Bash runs, waits for delivered reply and closes the session through the existing client verb. Selected legacy content envelopes independently witness the scenarios. Raw hidden facts establish checkpoint versions: acceptance before delivery, first main completion, linked delivery outcome and session divider. Store snapshots saved at those prefixes form exact regression baselines; independent marker, tool result, queue identity, parent and lifecycle assertions prevent fold-only self-certification.

The untagged committed-fixture reader requires both records and matching provenance, content digest, nonzero named passed checks and a durable GitHub gate-report URL. Capture records deliberately leave the report URL empty until builder recovery pins the dispatcher's counted report in both files. Untagged offline readers fail until that second pass; tagged capture checks validate usable records before promotion without claiming the pending pin. JSON artifacts use compact formatting to bound retained size.

## Concurrency model
No new goroutines. Existing daemon harness owns child/network cleanup. Prefix validation loads Store workers and joins each with Shutdown; bounded snapshot polling waits for the exact version.

## State transitions and identity reuse
| Event | Race test |
| --- | --- |
| Accepted queued message becomes delivered, retaining creating ID | `TestThreadShadowRetainedEvidence` |
| First main completion settles its own work, child remains parent-owned | `TestThreadShadowRetainedEvidence` |
| Session closure settles affected active work before divider | `TestThreadShadowRetainedEvidence` |
| Fresh Store prefix replay vs full Fold replay and revisions | `TestThreadShadowRetainedEvidence` |
The capture's named subchecks also exercise these transitions during authenticated generation. No synthetic records are committed as live evidence.

## Error handling
Missing, malformed, mismatched, unpinned or incomplete evidence fails readers. Missing credentials can skip the harness but create no records or passing capture counts. Missing required legacy scenarios fail capture. Sanitization preserves numerical IDs and joins, replaces host paths consistently in both raw and observed payloads, then scans complete serialized bytes before fixed-path atomic replacement. No raw payloads in failure diagnostics.

## Testing strategy
Write reader rejection and sanitizer regressions first and observe failure before helpers. Race-run credential-free schema/checker tests, existing `TestThreadAgentRecordedReplay` and `TestThreadShellRecordedReplay`, touched offline package tests; vet all packages, build pyry and compile/vet the tagged suite. Authenticated capture and final committed-pair race validation remain dispatcher/builder-return work under needs-live-artifacts.

## Open questions
None. The gate report URL is supplied on recovery, never invented by the capture.

## Documentation handoff
Pending documentation stage: complete “Shadow lifecycle and evidence” in `docs/knowledge/features/thread-package.md` with both `internal/e2e/realclaude/testdata/thread_shadow_history.json` and `internal/e2e/realclaude/testdata/thread_shadow_expected.json`, scenario/checkpoint coverage, counted provenance and recorded-versus-synthetic limits. State no app receives items yet. In `docs/knowledge/features/e2e-realclaude.md`, link `TestThreadShadowHistoryCapture` and describe missing-artifact arming, strict offline readers and builder recovery/commit requirements.

## Security review
**Verdict:** PASS
- Trust boundaries: raw subprocess/history facts and legacy payloads remain inert JSON; schema/checkpoint validation establishes evidence, not production authorization.
- Tokens: SHOULD FIX implemented in build: full-byte deny scan reuses dynamic credential needles and fixed credential/path prefixes; diagnostics contain no captured content.
- Files: fixed artifact leaves only; temporary 0600 files and rename; reject nonregular existing leaves. Pair digest detects interruption/mixed generation. No credential files copied.
- Subprocesses: existing authenticated harness owns isolated HOME and shutdown; only fixed `git rev-parse HEAD` and existing version helper added.
- Cryptography: SHA-256 binds retained raw entries to the expected record, not authentication; existing Noise handshake unchanged.
- Network/I/O: existing bounded harness receive deadlines; artifact reads bounded to 8 MiB.
- Errors/logs: fixed error descriptions and check names only; no raw content or paths in new diagnostics.
- Concurrency: Store cleanup joins workers; serial capture writes no shared process environment beyond existing harness behavior.
- Threat model: no production trust-boundary changes; host-path/credential retention is the relevant exposure and is denied before artifact writes.
**Reviewer:** builder (self-review)
**Date:** 2026-10-09

Sizing: four acceptance criteria, approximately 580 test/helper lines + 60 plan lines + up to 150 compact artifact lines = 790 total; zero exported types, zero changed consumers, no production state-machine branches.

## Revisions
2026-10-09: Independent raw text-run/tool-result checks and a third active turn before reset make content loss and pre-divider interruption non-vacuous. The closure checkpoint is the raw `session_divider`, before its legacy companion. Credential-free fixture checks use `!e2e_realclaude`; the tagged capture validates generated usable records while builder recovery supplies the pending counted report pin. Approximately 830 total lines exceed the ceiling; actual parentage is #3068 → #3049 → #2961, so the grandchild rule requires continuing under `needs-human:sizing`. Capture and replay readers form one coupled evidence deliverable.
Final sizing recount: 918 plan/test lines before the compact observed artifacts. Additional negative provenance controls and exact raw-to-legacy text equality account for the increase; `needs-human:sizing` remains applied under the same grandchild rule.

2026-10-09, verifier finding 1: The unconditional fixture reader made the deterministic gate fail before the dispatcher could reach authenticated capture. During the initial `needs-live-artifacts` handoff, `TestThreadShadowRetainedEvidence` lives in `thread_shadow_retained_test.go` under `thread_shadow_evidence && !e2e_realclaude`; selecting it with `go test -race -tags thread_shadow_evidence -run '^TestThreadShadowRetainedEvidence$' ./internal/e2e/realclaude/...` still fails for missing, incomplete or unpinned records. Default offline tests exercise the unchanged strict readers, including either missing half and all existing provenance/content rejection controls. No missing-record skip or synthetic capture is introduced. `TestThreadShadowHistoryCapture` still arms under plain `make e2e-realclaude` without another flag. On return, the builder must recover and pin both exact artifacts to the counted dispatcher report, change the retained reader's constraint to `!e2e_realclaude` and remove its tag-specific comment in the same commit as the pair, then rerun the unconditional offline race checks, recorded replays and tagged compilation before removing `needs-live-artifacts`. The explicit retained gate remains pending and its exclusion from the first deterministic gate is not evidence of a passed capture. Security self-review: PASS; validation, sanitization, deny scanning and capture I/O are unchanged, and the added temporary JSON files are inert synthetic rejection controls. Rework touches only the reader tests and plan; no overlapping feature branches were found for these paths.
