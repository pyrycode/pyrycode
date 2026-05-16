# Spec: e2e/realclaude permission-denied user-facing signal (#420)

## Files to read first

- `internal/e2e/realclaude/allowed_tools_enforcement_test.go:1-94` — the test being extended. Mirror its build-tag header, four-phase shape (worktree → run → run-level assert → JSONL walk), failure-message style, and the `bashInvokedInRaw` inline-detector pattern. The new assertion lives AFTER the existing gate-held loop, gated on the existing loop completing without fatal.
- `internal/e2e/realclaude/fixtures.go:80-188` — `RunOpts` shape (no changes needed), `RunResult.Stdout` (the stream-json stdout the new structured-signal detector parses), `RunPyryAgentRun` (no changes). The new test uses only the existing `Stdout` field; no new helpers required.
- `internal/e2e/realclaude/fixtures.go:276-296` — `parseInitSessionID`. Pattern to copy for scanning stdout line-by-line into a narrow JSON-decoded struct with the "non-JSON line → skip silently" policy. The new structured-signal detector follows the same idiom.
- `internal/agentrun/jsonl/reader.go:45-83` — `Event` struct. The two fields the new text-channel detector touches: `Kind` (filter `"assistant"`) and `Raw` (decode `message.content[i].type=="text"` for `.text` string).
- `internal/e2e/realclaude/permission_protocol_regression_test.go:96-161` — pattern for narrow-struct unmarshal walking an events list (the `system/init` / `result/success` walk). The structured-signal detector uses the same minimal-struct-per-pass technique.
- `internal/e2e/realclaude/testdata/permission_protocol_v2.1.143_default.json:304-372` — concrete shape of a `result` envelope: top-level `type:"result"`, `subtype:"success"`, `is_error:false`, `permission_denials:[]`. Confirms where the structured-signal fields live in stream-json stdout.
- `internal/agentrun/selfcheck/selfcheck.go:217-219` — security comment about not logging raw assistant bytes. Same discipline applies to the new failure message: cite JSONL path, never echo `Raw` bytes or text-block content (even though the test prompt is synthetic).
- `internal/agentrun/streamjson/testdata/captured_run.jsonl` — additional confirmation that `permission_denials` on a `result` line is the canonical channel and is present even when empty.

## Context

`internal/e2e/realclaude/allowed_tools_enforcement_test.go:56-70` asserts the gate held — no Bash `tool_use` in JSONL — but says nothing about whether the user perceived the denial. A regression that silently no-ops on a denied call (no assistant turn, no structured event, no diagnostic) would pass the existing test while leaving the operator staring at a hung-looking session.

The contract being guarded by this ticket is "user gets *some* interpretable signal on denial," not "user gets *this specific* signal." The disjunctive shape is load-bearing: a regression that silently swallows the refusal fails; a regression that swaps channels (text ↔ structured) still passes, because either alone is sufficient.

Companion to:
- #365 — gate-held contract (existing test).
- #418 — null-findings fixture-walk regression (sibling, no overlap; different argv).

## Design

### Shape: extension to the existing test, not a sibling

The ticket leaves "sibling test or extension" to architect discretion. The decision is **extension to `TestRealClaude_AllowedToolsEnforcement`**, on two grounds:

1. **Cost**: the new assertion needs exactly the same argv, same model, same prompt, same `RunResult` as the existing test. A sibling test would re-run the same haiku call (~$0.01) and re-pay subprocess startup latency for no behavioural difference in the run, just to assert on different fields of the same output stream. Extension keeps one model call per CI run.

2. **Coupling**: the two assertions guard one contract (`pyry agent-run --allowed-tools=Read` is a deny-by-default gate that surfaces SOME signal to the operator). They are not independent properties of independent code paths. Splitting them would invite asymmetric drift — a future maintainer fixing one prompt for one assertion would have to remember the other test exists. A single test with both assertions makes the contract visible in one place.

The test name `TestRealClaude_AllowedToolsEnforcement` remains accurate — the additional assertion is still about enforcement behavior, just on a different axis.

### Test flow after extension

The four existing phases stay verbatim; phases 5 and 6 are new:

1. **Allocate worktree** — unchanged.
2. **Run pyry agent-run** — unchanged (same prompt, system prompt, AllowedTools=`["Read"]`, MaxTurns=2, Effort=low, Model=`claude-haiku-4-5`).
3. **Assert run-level success** — unchanged (`ExitCode==0`, `SessionID!=""`).
4. **Assert no Bash `tool_use` in JSONL** — unchanged. **NEW: do not `return` after the loop completes (currently there is no return; the function falls through to the implicit return-at-end-of-block).** The body of the loop continues to use `t.Fatalf` on a positive hit, so phases 5–6 are only reached when the gate held.
5. **NEW: detect signal in assistant text** — walk the same `events` slice. For each `e.Kind=="assistant"`, decode `e.Raw` into a narrow struct exposing `message.content[]` with `type` and `text` fields. For every content block with `type=="text"`, run `strings.Contains(strings.ToLower(text), kw)` against each keyword in the fixed keyword set; first match short-circuits with `textHit = true`. JSON unmarshal errors on a line skip that line silently (selfcheck discipline mirrors the existing `bashInvokedInRaw` loop at :60-67).
6. **NEW: detect signal in stdout `result` envelope** — scan `result.Stdout` line-by-line with `bufio.Scanner`. For each line, decode into a narrow struct exposing top-level `Type`, `IsError`, and `PermissionDenials` (raw-message slice). If `Type=="result"` AND (`len(PermissionDenials) > 0` OR `IsError == true`), set `structHit = true` and break. Non-JSON lines and non-`result` lines are skipped silently. Use the same scanner buffer-cap idiom as `parseInitSessionID` (default cap is fine — stream-json result lines do not approach 64 KiB).
7. **NEW: combine** — `if !textHit && !structHit { t.Fatalf(...) }`. The failure message is in § Error handling.

### Keyword set (text channel)

```go
var denialKeywords = []string{
    "cannot",
    "can't",
    "unable",
    "not allowed",
    "permission",
}
```

Lower-case substring match against `strings.ToLower(text)`. The set is the ticket's example list verbatim. Rationale:

- All five are high-signal refusal markers in the prompt's specific context: the user asked for `Bash`, the model has only `Read`, the only natural English the model produces is "I cannot use Bash / I'm unable to / not allowed to / I don't have permission."
- The set is intentionally narrow — broader keywords like "available" or "access" appear in non-refusal contexts (e.g. "the available files are..."). The disjunctive design tolerates the text channel missing if the structured channel fires.
- The match is case-insensitive (`ToLower` once per text block) to absorb the model's choice of casing at sentence start.
- Apostrophe in `can't` is plain ASCII; if the model uses Unicode right-single-quote (U+2019), the other four keywords cover it.

If this test flakes in practice because a future haiku version uses outside-the-set language (e.g. "I lack access to Bash") AND simultaneously emits no structured denial signal, the fix is to expand the keyword set in a follow-up — not to weaken the assertion.

### Structured-signal detector shape

```go
type resultEnvelope struct {
    Type              string            `json:"type"`
    IsError           bool              `json:"is_error"`
    PermissionDenials []json.RawMessage `json:"permission_denials"`
}
```

Decoded once per stdout line. Hit condition:

```
env.Type == "result" && (len(env.PermissionDenials) > 0 || env.IsError)
```

Two reasons for the `IsError` fallback alongside `PermissionDenials`:

- `permission_denials` is the canonical channel today (confirmed in fixture v2.1.143 and `streamjson/testdata/captured_run.jsonl`), but the ticket explicitly asks for an "error-shaped envelope, or another machine-readable denial indicator" as a co-equal acceptable signal.
- A future claude release could route denial through `is_error=true` + a subtype change (e.g. `subtype:"error_permission"`) without touching `permission_denials`. The detector tolerates that without code changes.

Don't widen further (e.g. don't also accept `subtype != "success"`) — that would let unrelated failure modes (max-turns hit, timeout, API rejection) masquerade as denial signals.

### File-local helpers

Two new file-private helpers in `allowed_tools_enforcement_test.go`, sitting next to `bashInvokedInRaw`:

- `func assistantTextRefusalHit(events []JSONLEntry) bool` — walks events, returns true on first text-block keyword hit. Internal JSON unmarshal errors are swallowed (return false for that line, continue). Signature contract: never returns `error`; the test never wants to fail-the-test on a malformed line, and there's no log channel to use. Uses the package-private `denialKeywords` slice declared at file scope.
- `func structuredDenialHit(stdout []byte) bool` — `bufio.NewScanner(bytes.NewReader(stdout))`, default buffer (`result` lines are well under 64 KiB), `json.Unmarshal` into `resultEnvelope`, returns true on first matching `result` envelope.

Both are exact analogues of `bashInvokedInRaw` (file-local, signature-minimal, no error surfacing).

### File header comment

Append one sentence to the existing top-of-file comment block describing the new "interpretable signal" assertion and that the two assertions are intentionally co-located (same argv, same model call). Don't expand into a full paragraph — the existing header already establishes the test's purpose and the obsolete-matrix history.

### No new exports, no production changes, no fixture changes

The extension touches one file (`allowed_tools_enforcement_test.go`). No additions to `fixtures.go`. No new `testdata/` fixtures (the stream-json stdout the new detector parses is captured live by `RunPyryAgentRun`'s existing `Stdout` capture; no replay needed).

## Concurrency model

None. Single-goroutine test that subprocess-execs `pyry agent-run`, then performs two synchronous reads (existing `ReadJSONL` of the session file, new in-memory walk of `result.Stdout`).

## Error handling

All paths in the new code are deterministic. The non-obvious diagnostics:

- **Both channels miss → `t.Fatalf`.** Message template:

  > `permission gate held but produced no operator-visible signal: assistant text contained none of [cannot, can't, unable, not allowed, permission] across N assistant entries; stdout result envelope had permission_denials empty and is_error=false across M lines. path: <jsonl-path>`

  Substitute `N` (assistant-entry count seen during the text-channel walk) and `M` (total stdout line count). The path is the resolved JSONL path computed via `jsonlPathFor`. The brackets-list keyword echo is intentional — it tells a future maintainer what the detector accepted without forcing them to read the source.

  **Do NOT include** raw assistant-text bytes, raw stdout bytes, or raw `result` envelope bytes. Selfcheck discipline at `selfcheck.go:217-219` — even though the prompt is synthetic for this test, keep the pattern consistent.

- **Inner JSON unmarshal errors in either detector → skip silently.** A single malformed line must not turn a PASS into an inconclusive (selfcheck.go:283). No log; the test has no logger.

- **Empty `result.Stdout`** (would be a structural failure inside `RunPyryAgentRun`) is unreachable from here — `parseInitSessionID` already finds the init envelope, so stdout is non-empty by the time we get past the SessionID check. No defensive handling needed.

## Testing strategy

The test IS the test. Verification at implementation time:

- `go vet -tags=e2e_realclaude ./internal/e2e/realclaude/...` — passes.
- `go build -tags=e2e_realclaude ./internal/e2e/realclaude/...` — compiles.
- `make e2e-realclaude` (or `go test -tags=e2e_realclaude -run TestRealClaude_AllowedToolsEnforcement ./internal/e2e/realclaude/...`) — passes against a working `claude` binary and a live API key. One real haiku call per run, same cost as today.
- `make test` — still passes (build-tag gating unchanged).

Manual regression rehearsal during implementation, optional but recommended:

- Temporarily mutate the text-channel keyword set to `[]string{"xyzzy"}` (won't match) AND mutate the structured-channel hit condition to `false` — the test should fail with the new error message on a previously-green run. Revert. Confirms the assertion has teeth.

If the test fails reproducibly on green code (because, e.g., haiku declines without using any of the five keywords AND `permission_denials` stays empty), the fix is one of:

1. The system prompt may need a clarifying nudge — though the ticket discourages this (resists model variance).
2. The keyword set expands by one or two well-chosen markers (e.g. add `"don't have"` if observed).
3. The structured detector accepts an additional envelope shape if claude has changed its denial channel.

Do not weaken the test by reducing the keyword set OR by adding overly-broad fallbacks (e.g. "any non-empty assistant text counts as a signal"). The whole point is to fail on silent no-ops.

## Open questions

None blocking. Two judgement calls flagged for the developer:

- **Capturing `N` and `M` for the failure message.** Spec prescribes counting (a) assistant entries seen during the text walk and (b) total stdout lines for the structured walk. If the developer finds this awkward, they may drop both counters and use a simpler error message — the path alone is enough to diagnose.
- **Helper-function placement.** Spec puts `denialKeywords`, `assistantTextRefusalHit`, and `structuredDenialHit` file-locally in `allowed_tools_enforcement_test.go` next to `bashInvokedInRaw`. If the developer believes any of these would be reused in a near-future test (e.g. companion #418 work spilling), promoting one of them to package-scope in `fixtures.go` is fine — but YAGNI applies; file-local is the default.

## Out of scope

- Pinning a specific assistant-text wording. Loose keyword set only.
- Pinning a specific structured-envelope shape beyond `permission_denials` and `is_error`. Detector accepts either; widening further muddles the contract.
- A separate fixture-walk regression (the #418 model). This test exercises live behavior under the agent-run argv; #418 covers the captured-trace direct-`claude` argv. Different argv → different test.
- Boot-time selfcheck argv coverage (#336's domain).
- Asserting "the model picked Read instead" or "the assistant turn was non-empty in some other sense." The contract is `text-channel-hit || structured-channel-hit`, nothing more.

## Security review

Not applicable — this ticket does not carry the `security-sensitive` label. The extension touches one test file, introduces no new trust boundary (synthetic test prompt, no operator input, all argv values are Go string literals under the test author's control), and follows the existing selfcheck no-raw-bytes-in-error-messages discipline that the parent test (#365) already established.
