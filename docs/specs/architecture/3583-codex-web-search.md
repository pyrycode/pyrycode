# #3583 — Codex web-search tool rows

## Files read

- `internal/codexsup/translate.go` → `Translator.item`, `commandItem`, `fileChangeItem`, `truncate`: dispatch, one-result contract and UTF-8-safe bounds.
- `internal/codexsup/translate_test.go` → `replay`, `TestTranslateUnrecognizedLanes`, `TestHandBuiltFramesMatchSchema`, `TestItemTypesClassified`: fixture replay and schema/partition gates.
- `internal/codexsup/capture_live_test.go` → `TestCaptureLive`, `captureTurn`, `writeCapture`: native capture, default declines and identity/path scrubbing.
- `internal/codexsup/capture_test.go` → `TestCaptureFixturesScrubbed`: mandatory capture leak check.
- `internal/codexsup/codex_app_server_protocol.schemas.json` → `ThreadItem`, `WebSearchAction`: four action arms and nullable optional fields; no web-search status.
- `internal/turnevent/event_turn.go` → `ToolStart`, `ToolUpdate`: content versus daemon-composed ResultDetail.
- `docs/knowledge/features/codexsup-package.md` → three-list partition and capture gaps: every update becomes a separate result; schema evidence does not establish live field timing.
- `docs/knowledge/features/development-verification-captures-and-live-evidence.md`: count actual paired frames and commit usable evidence.
- `CODING-STYLE.md`: local changes, table-driven tests, contract comments.

## Context

Web-search starts currently produce an unrecognized card and completions disappear.
Map this one behavior to existing tool events without changing clients. Useful
completion fields are schema-based assumptions until a native capture proves them.
No decision record is needed. No overlapping feature branches were found after fetch.

Sizing, checked against both sketch and final plan: approximately 450 written lines
including tests, capture support, fixtures and this plan; zero exported types,
zero consumer updates, five acceptance criteria, one decoding rejection branch.
This is one deliverable with its capture evidence and tests.

## Design

Add `webSearchItem(method, params)` in `translate.go` and place `webSearch` only
in `mappedItemTypes`. Start emits one fetch `ToolStart`, item identity intact,
title `web_search`, no input or locations, irrespective of populated fields.
Completion emits one completed `ToolUpdate` with empty `ResultDetail`.

Display text joins nonempty values with newlines. Search prefers nonempty plural
queries in order, then singular query, then the top-level query. Open-page uses
only URL; find-in-page uses URL then pattern, each when present. Missing/null/other
action uses top-level query. Missing open/find fields invent no fallback. Empty
combined text means nil content. Cap combined text at 16 KiB using `truncate`.
Do not decode or forward opaque results.

Add focused hand-built frames and table-driven translator cases. Use `imageView`
for the remaining unmapped fixture while retaining the unknown-item assertion.
Expose `TestCaptureLiveWebSearch` independently of existing capture scenarios,
sharing the driver and scrubber. Require a same-id started/completed pair before
writing `testdata/capture/web_search.jsonl`. If obtained, add a mandatory offline
replay and record version and observed fields. Otherwise record the exact capture
limitation and retain explicit schema-only assumptions.

## Concurrency model

No new production goroutine or mutable state. Translator remains called serially.
The capture reuses the existing mutex-protected recorder and client teardown.

## State transitions and identity reuse

| Event | Contract | Race-detector test |
| --- | --- | --- |
| Start then complete | One start and one result with the same item id | `TestTranslateWebSearch` |
| Repeated pair with the same id | No retained payload or suppression across pairs | `TestTranslateWebSearchIdentityReuse` |

## Error handling

Malformed typed JSON follows the existing undecodable lane. Missing/null optional
fields contribute nothing. Completion status derives from the notification method,
since the schema has no status. Capture without a usable pair fails explicitly;
unset capture environment skips with an explanation and confirms no live behavior.

## Testing strategy

Write translator tests first and observe the unmapped behavior fail. Cover all action
arms, optional absent/null fields, precedence, empty/status-only results, opaque
results omission and oversized combined multibyte text. Schema-validate all hand-built
frames, run affected package tests under race through test admission, tagged focused
capture and scrub checks, `go vet ./...`, and `go build -o /tmp/builder-3583/pyry ./cmd/pyry`.
Linux is this ticket's verification OS. Full-module and final live gates belong to
the dispatcher; retain `needs-real-claude`.

## Open questions

- Can this host's focused capture produce a pair, and which completion fields are
  actually populated? Resolve with the attempt and record the evidence in Revisions.

## Documentation handoff

Pending for the documentation stage in `docs/knowledge/features/codexsup-package.md`:
- Under **The translator's three-list partition**, describe `webSearch` as mapped:
  fixed `web_search` fetch row on start, query/URL/pattern in its single completed
  result, fallback rules, 16 KiB UTF-8-safe cap, and omitted opaque results.
- Under **The capture's live gaps, and what they mean for a recapture**, record either
  the committed capture/version and observed fields or the remaining completion-field
  assumption. Any usable later live-gate frames must return to the builder for commit.

## Security review

**Verdict:** PASS

**Findings:**
- [Trust boundaries] Peer strings remain model-authored `TextContent`, never tool
  title, `RawInput`, routing data or `ResultDetail`; schema fixtures test the boundary.
- [Tokens] No credential access is added; capture uses only explicitly configured
  isolated home and existing scrubber. Run `TestCaptureFixturesScrubbed` on artifacts.
- [File operations] Mapping does no I/O. Capture writes only its fixed fixture path
  via `writeCapture`; peer URLs/queries cannot select a path.
- [Subprocesses] Mapping executes nothing. Capture reuses the existing configured
  client and default declines, with timeout and deferred Stop.
- [Cryptography] No cryptographic operations or key lifecycle changes.
- [Network and I/O] Mapping performs no fetch. Existing transport frame cap bounds
  input; a separate combined 16 KiB text cap bounds output.
- [Errors, logs, telemetry] No content logging added. Opaque results are ignored;
  malformed JSON retains the existing bounded undecodable behavior.
- [Concurrency] No new production concurrency or shared state. Capture retains the
  recorder mutex, bounded channel and existing shutdown path.
- [Threat model] Rendered text remains untrusted under the existing client rendering
  contract. No permissions, relay, filesystem or execution capability is added.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-17

## Revisions

- 2026-10-17: Focused capture invocation on Linux was compiled and selected:
  `python3 scripts/test-admission.py -- go test -timeout=30m -tags codex_capture -run '^TestCaptureLiveWebSearch$' -v ./internal/codexsup/`.
  It skipped because both `PYRY_CODEX_CAPTURE_BIN` and `PYRY_CODEX_CAPTURE_HOME`
  are unset. The installed CLI reports 0.159.2, while the schema remains pinned
  to 0.156.1. Zero live turns ran and no pair/artifact was obtained. The open
  question is resolved as unavailable evidence on this host, not confirmation.
  Completion population/timing of top-level `query`, `action.type`, search
  `queries`/`query`, open-page `url`, and find-in-page `url`/`pattern` remain
  schema-based assumptions. If the dispatcher later obtains a usable pair,
  return it to the builder to commit `testdata/capture/web_search.jsonl` with
  a mandatory offline replay and observed version/field assertions.

- 2026-10-18: Artifact return now has retained Linux evidence from recovery run
  `f21617d94966678497163f99d1826fc07941624f346caa14331e472f9091a443`, captured
  against `ce414973b7d4c3f45666bab0a6a2b35cd5cf363a` with Codex **0.159.2**.
  `TestCaptureLiveWebSearch` executed once, passed once, and skipped zero times.
  Preserve only the two unchanged web-search item frames in
  `internal/codexsup/testdata/capture/web_search.jsonl` (SHA-256
  `16a6d05118fa805f8c936b4fdc00c1e628812ad8a6526cabe22eb1e927c5b533`).
  The complete recorded stream includes unrelated account/credit/rate-limit
  metadata and remains private; it is not a publishable replay fixture. Codex
  **0.159.2** is recorded in the PR provenance from the retained version output,
  rather than requiring bootstrap or account frames in the fixture.
  The single same-id pair starts with an explicit empty query and null
  action/results. Completion has `action.type=search`, a populated singular
  `action.query` matching the top-level query, explicit null `action.queries`,
  and populated opaque results. This confirms field timing for singular search
  only; populated plural queries, openPage, findInPage and other/fallback live
  shapes remain unobserved. The schema stays pinned to 0.156.1.
  `TestTranslateCapturedWebSearch` in `translate_web_search_capture_test.go`
  must fail for a missing fixture or any extra bootstrap/account frames, assert
  the raw observed fields, validate both web-search items against the pinned schema,
  and replay exactly one fetch start and one query-only completed result.
  Existing fixture scrub checks cover the new artifact; no new I/O or execution
  capability enters production. The merge resolution preserves main's MCP
  mapping alongside web search. Repair scope remains this behavior's evidence
  and regression proof, approximately 630 total written lines across the ticket,
  no new exported types or consumer updates, and no additional rejection branch.
  Documentation stage: replace the unavailable-evidence account under
  **The capture's live gaps, and what they mean for a recapture** with these
  observed fields/version and the remaining unobserved variants.
