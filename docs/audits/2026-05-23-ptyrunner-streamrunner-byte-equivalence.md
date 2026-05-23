# Byte-equivalence audit: ptyrunner vs streamrunner

**Ticket:** [#503](https://github.com/pyrycode/pyrycode/issues/503)
**Date:** 2026-05-23
**Method:** operator-direct read of dispatcher source + cross-reference against catalogue in
[`internal/e2e/realclaude/ptyrunner_byte_equivalence_test.go`](../../internal/e2e/realclaude/ptyrunner_byte_equivalence_test.go).
**Conclusion:** every catalogued divergence is **acceptable as documented omission**. No production-fix tickets filed. [#505](https://github.com/pyrycode/pyrycode/issues/505) updates the test allowlist to cite this report inline.

---

## Method

Per [#503](https://github.com/pyrycode/pyrycode/issues/503) AC: "Read the dispatcher's TS parser. Enumerate every field it reads from each stream-json event type."

Dispatcher source enumerated:

- `~/WorkSpace/Projects/agent-dispatcher/src/dispatch.ts` (main stream consumer)
- `~/WorkSpace/Projects/agent-dispatcher/src/agent-runtime.ts` (permission-denial watchdog)

Search strategy: `grep -nE` for each catalogued field name; full read of the `case` arms in `logStreamMessage` (dispatch.ts:212–245) and the result-trailer destructuring (dispatch.ts:562–577); full read of `detectPermissionDenial` + `advancePermissionDenialState` (agent-runtime.ts:545–700); confirmation that `rawResult.<field>` is grepped 0 times anywhere outside test source.

---

## Dispatcher's complete read surface

### From streamed events (via `dispatch.ts:212-245` switch on `msg.type`)

| Event | Field reads | Side effect |
|---|---|---|
| `system` | `session_id` | log line |
| `assistant` | `message.content[].type` / `.name` / `.input` (tool_use) / `.text` (text) | log line per block; permission-denial watchdog state transitions |
| `user` | `message.content[].type` / `.is_error` / `.content` (tool_result substring match) | permission-denial detection |
| `result` | `subtype`, `num_turns`, `total_cost_usd`, `session_id` (log line) plus the destructuring at line 562 — see next table |
| *default* | `type` (echoed only); first 300 chars JSON-stringified | log preview only — **no semantic consumption** |

### From the `result` trailer (via `dispatch.ts:562-577` destructuring on close)

| Field | Reads | Downstream branch |
|---|---|---|
| `result` | `output` | post-run comment body, log dump |
| `session_id` | `sessionId` | post-run comment, usage summary, denial recovery |
| `is_error` | `isError` | error-branch routing |
| `num_turns` | `numTurns` | usage summary, max-turns salvage comment |
| `total_cost_usd` | `totalCostUsd` | usage summary, cost log |
| `duration_ms` | `durationMs` | usage summary |
| `usage.input_tokens` | nested at `dispatch.ts:1885` | usage summary |
| `usage.output_tokens` | nested at `dispatch.ts:1886` | usage summary |
| `usage.cache_read_input_tokens` | nested at `dispatch.ts:1887` | usage summary |
| `usage.cache_creation_input_tokens` | nested at `dispatch.ts:1888` | usage summary |
| `terminal_reason` | `terminalReason` | `=== "max_turns"` salvage gate (`dispatch.ts:1799, 1840`); other values flow into error-comment text only |
| `subtype` | logged at `dispatch.ts:237` | log line only — **no semantic branch** |
| `rawResult` | captured at `dispatch.ts:573` | **never reached into anywhere in non-test source** (grep `rawResult\.` returns 0 in `src/`) |

The `terminal_reason` value space the dispatcher actually branches on is a single literal: `"max_turns"`. Other values (including the literal `"permission_denied"` synthesized at `dispatch.ts:591` when no result event arrived) are only echoed back into comment/log text.

---

## Per-field / per-event decisions

The test allowlist in [`ptyrunner_byte_equivalence_test.go:73-97`](../../internal/e2e/realclaude/ptyrunner_byte_equivalence_test.go#L73) enumerates 1 event + 7 result-trailer fields streamrunner emits that ptyrunner doesn't, and 0 ptyrunner-only emissions. The audit's per-item decisions:

### Streamrunner-only events

| Event | Dispatcher reads? | Decision | Rationale |
|---|---|---|---|
| `rate_limit_event` | NO | **Document as omission** | Falls into `dispatch.ts:240` default case — preview-logged only, no semantic consumption. Architecturally unreachable from ptyrunner regardless: the event lives in claude's API-side stream, not in the per-session local JSONL ptyrunner tails. Surfacing it would require a separate observability channel (claude API direct, or a tui-driver banner detector if claude renders a TUI-side rate-limit hint). Filing-time judgment: not worth a fix until a dispatcher consumer actually reads it. |

### Streamrunner-only result-trailer fields

All 7 fall into the same category: **never read by the dispatcher anywhere in non-test source**. The audit decision for each: **document as omission**, no production fix.

| Field | Dispatcher reads? | Source observability | Decision |
|---|---|---|---|
| `api_error_status` | NO | API-only (claude's HTTP layer) | Document as omission |
| `duration_api_ms` | NO | API-only | Document as omission |
| `fast_mode_state` | NO | API-only | Document as omission |
| `modelUsage` | NO | API-only (per-model breakdown) | Document as omission |
| `permission_denials` | NO | API-only (claude's gate result) | Document as omission |
| `ttft_ms` | NO | API-only (time to first token) | Document as omission |
| `uuid` | NO | API-side session UUID — distinct from `session_id`, which IS read | Document as omission. Dispatcher uses `session_id` everywhere; `uuid` would be a duplicate signal. |
| `usage.server_tool_use` | NO | Nested key on `usage` block — dispatcher reads only the 4 token-count keys (`input_tokens`, `output_tokens`, `cache_read_input_tokens`, `cache_creation_input_tokens`) | Document as omission |

### Ptyrunner-only events

The `expectedPtyRunnerOnly` table starts empty in the test. Ptyrunner does emit four claude-local-JSONL housekeeping envelopes that streamrunner doesn't surface — `permission-mode`, `file-history-snapshot`, `skill_listing`, `ai-title` — but these all fall into the dispatcher's default case (`dispatch.ts:240`), preview-logged only with no semantic consumption.

| Event | Dispatcher reads? | Decision |
|---|---|---|
| `permission-mode` | NO (default-case preview) | Tolerate; add to `expectedPtyRunnerOnly` |
| `file-history-snapshot` | NO (default-case preview) | Tolerate; add to `expectedPtyRunnerOnly` |
| `skill_listing` | NO (default-case preview) | Tolerate; add to `expectedPtyRunnerOnly` |
| `ai-title` | NO (default-case preview) | Tolerate; add to `expectedPtyRunnerOnly` |

These are noise in the dispatcher log preview but not a correctness issue. They could be filtered at the streamjson emitter for cleaner logs (separate, optional, non-blocking ticket if anyone cares about log volume).

---

## Subtle case — assistant envelope shape matches

Worth confirming because it's load-bearing: dispatch.ts at line 220 has `case "assistant":` reading `(msg as any).message?.content[]`. The permission-denial watchdog (`agent-runtime.ts:683`) gates on `m.type === "assistant"` and walks the same `message.content[]` blocks.

ptyrunner emits these envelopes verbatim from claude's per-session JSONL via `streamjson/emitter.go:195` (`line := append([]byte(nil), ev.Raw...)`). Claude's local JSONL uses the same `{"type": "assistant", "message": {...}}` shape (per `tuidriver/pkg/tuidriver/jsonl.go:85` docstring) — so the dispatcher's `case "assistant"` arm lights up identically for both runners. The byte-equivalence test's per-line `(type, subtype)` envelope comparison (`extractShapes` at line 147) implicitly already enforces this — if ptyrunner re-shaped `assistant` to `message`, the test would have failed long before this audit.

The permission-denial watchdog is therefore **fully functional under ptyrunner**.

---

## Recommended follow-ups

| Ticket | Scope | Status |
|---|---|---|
| **[#505](https://github.com/pyrycode/pyrycode/issues/505)** — already filed | Update `envelopeShape` doc-comment (lines 84–97 of the test) with per-field rationale citing this report; promote the four ptyrunner-only events into `expectedPtyRunnerOnly` with this report cited as rationale | Unblocked by this audit — can dispatch |
| *(no others)* | — | No "must close" items surfaced. No production-fix tickets to file. |

---

## Followup considerations (not tickets)

- **`rawResult` is dead weight.** The dispatcher captures it on every result envelope (`dispatch.ts:573`) but no consumer ever reaches in. Could be removed in a future dispatcher cleanup; out of scope here. Filing it as a dispatcher ticket would generate review traffic for a 1-LOC deletion that doesn't matter.
- **If a future dispatcher change starts consuming one of the 7 omitted result fields** (e.g. someone wires `permission_denials` into the salvage path, or `ttft_ms` into perf logs), this audit goes stale and the affected field flips from "document as omission" to "must close." Re-run the per-field grep against `agent-dispatcher/src/` before relying on the conclusion past, say, 2026-09-01.
- **`rate_limit_event` is structurally unreachable from ptyrunner.** Worth naming explicitly in [[Drop-In Contract]] so any future operator-direct decision to add subscription-tier observability has the reasoning logged in one place.
