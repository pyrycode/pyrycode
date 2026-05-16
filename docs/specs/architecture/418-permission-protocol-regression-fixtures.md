# Spec: Fixture-walk regression test for permission-protocol-spike null findings (#418)

## Files to read first

- `internal/e2e/realclaude/permission_protocol_spike_test.go` — the spike that produced the fixtures. Build tag, package, fixture-record shape, fixture filename pattern.
- `internal/e2e/realclaude/testdata/permission_protocol_v2.1.143_default.json:1-377` — one full fixture so the parse shape is concrete (top-level keys; `stdout_events[0]` = `system/init` with `tools`, `permissionMode`; trailing `stdout_events[N-1]` = `result/success` with `permission_denials`).
- `internal/e2e/realclaude/fixtures_test.go:1-100` — package conventions: build tag header, table-driven test layout, `t.Fatalf` patterns, no testify.
- `internal/e2e/realclaude/allowed_tools_enforcement_test.go:75-94` — pattern for parsing only the JSON keys we need into a narrow struct (mirrors `selfcheck` style). The regression test uses the same minimal-struct technique.
- `docs/knowledge/features/permission-protocol-spike.md:55-75` — the four null findings being pinned. Findings 1-3 map 1:1 to AC#2-#4-#5; finding 4 is the "init.tools is the full registry" observation that AC#3 leans on (Bash present).
- `Makefile` § `e2e-realclaude` — confirms the build-tag gate is what excludes the new test from `make check`.

## Context

#383 captured six per-mode fixtures at `claude v2.1.143` to document that `--permission-prompt-tool stdio` does not emit any permission-gate event and `--allowed-tools` is not enforced under that argv. The spike test PASSES on rerun regardless of whether the protocol still behaves that way — it pins the on-disk artefact, not its shape. If a future `claude` release starts emitting permission events on stdio, the spike-runner regenerates fixtures per the manual matrix-sweep discipline, the file contents change, no test fails, and the mobile-relay design implication (documented in `features/permission-protocol-spike.md`) silently flips.

This ticket adds a cheap fixture-walk regression test that pins the **null findings** as a contract. Pure `json.Unmarshal` on committed `testdata/` — no API call, no subprocess. Cost: a few ms per fixture, run only under the `e2e_realclaude` build tag (so excluded from `make check`).

When `claude` updates and the spike-runner regenerates fixtures, this test either passes (findings still hold) or fails with a message naming the offending fixture filename and which finding flipped, at which point the mobile-relay design implication gets revisited intentionally.

## Design

### New file

`internal/e2e/realclaude/permission_protocol_regression_test.go`

Single file. Build tag `//go:build e2e_realclaude`, package `realclaude`. No new helpers in `fixtures.go` — this test depends only on stdlib (`encoding/json`, `path/filepath`, `regexp`, `testing`).

### Test function

```go
func TestRealClaude_PermissionProtocol_RegressionFixtures(t *testing.T)
```

The function:

1. Calls `filepath.Glob("testdata/permission_protocol_v*_*.json")` — the spec's literal glob. Fails fast (`t.Fatalf`) if zero matches; a deleted fixture set must be a loud failure, not a silent green.
2. For each fixture path, opens a `t.Run(filepath.Base(path), …)` subtest. Subtest naming uses the bare filename so the failure header in `go test` output names the offending fixture directly (AC#7).
3. Inside each subtest: read the file, `json.Unmarshal` into a narrow struct (only the fields asserted on), extract the expected mode from the filename, run the six assertions below.

### Narrow parse struct

The fixture has many fields the regression test does not care about. Parse only what the assertions need:

```go
type regressionFixture struct {
    StdoutEvents           []json.RawMessage `json:"stdout_events"`
    ExitCode               int               `json:"exit_code"`
    ContextDeadlineTripped bool              `json:"context_deadline_tripped"`
}
```

Then walk `StdoutEvents` once, identifying:

- The `system/init` event — first event with `type=="system"` and `subtype=="init"`. Re-parse its raw bytes into a second narrow struct (`Tools []string`, `PermissionMode string`).
- The `result/success` event — first event with `type=="result"` and `subtype=="success"`. Re-parse into a third narrow struct (`PermissionDenials []json.RawMessage`).
- Any forbidden-typed event — any event whose top-level `type` is one of `control_request`, `permission_request`, `tool_permission_request`. Capture all hits, not just the first, so the failure message can list them.

The walk uses a single small struct `{Type, Subtype string}` to dispatch by event kind.

### Filename → expected mode parser

A package-private regex pulls the mode token out of the filename:

```
^permission_protocol_v[^_]+_(?P<mode>[A-Za-z]+)\.json$
```

Helper signature: `expectedPermissionModeFromFilename(name string) (string, error)`. The `auto` → `default` rule is applied here (finding #3 from the spike doc), so the assertion site compares apples-to-apples against `init.permissionMode`.

If the regex does not match, the helper returns an error and the subtest fails with a message that includes the filename — covers the "future spike rerun added a fixture with an unexpected filename shape" case.

### Assertions (mapping to ACs)

Each assertion uses `t.Errorf` (not `t.Fatalf`) so a single subtest can report multiple flipped findings in one run. The subtest as a whole still fails on any error.

| AC | Assertion |
|----|-----------|
| #2 | No event in `stdout_events` has `type` ∈ {`control_request`, `permission_request`, `tool_permission_request`}. On failure, error message lists each offending event's index and type. |
| #3 | `init.tools` contains `"Bash"` (linear scan, `slices.Contains` is fine). |
| #4 | `result.permission_denials` is empty (`len(...) == 0`). |
| #5 | `init.permissionMode == expectedPermissionModeFromFilename(name)`. The helper handles the `auto` → `default` synonym. |
| #6 | `exit_code == 0`, `context_deadline_tripped == false`, `len(stdout_events) >= 7`. |
| #7 | Each `t.Errorf` message starts with the fixture filename and names which finding flipped. |

### Failure-message shape

Every assertion's error message follows the same template so a CI log diff after a `claude` update is grep-able:

```
<fixture-filename>: <finding-name>: <expected> vs <observed>
```

Concrete examples (one per assertion):

- `permission_protocol_v2.1.143_default.json: forbidden event type observed: stdout_events[3].type = "control_request" (spike finding #1 flipped)`
- `permission_protocol_v2.1.143_default.json: init.tools missing "Bash" (spike finding #4: full-registry expectation flipped, --allowed-tools may now gate)`
- `permission_protocol_v2.1.143_default.json: result.permission_denials non-empty: len=2 (spike finding #1/#2 flipped: gate fired and denied something)`
- `permission_protocol_v2.1.143_auto.json: init.permissionMode = "auto", want "default" (spike finding #3 flipped: auto is no longer a synonym for default)`
- `permission_protocol_v2.1.143_default.json: exit_code = 1, want 0 (structural sanity check)`

The "spike finding #N flipped" suffix is the breadcrumb that points the spike-runner back to `features/permission-protocol-spike.md` to revisit the mobile-relay design implication.

### Concurrency

Each subtest is independent. `t.Parallel()` on the inner `t.Run` is fine and matches the package's general convention; the I/O is cheap so the speedup is marginal but the lack of shared state makes it free.

### Why no per-mode hardcoding

AC explicitly forbids hardcoding the six mode names. The filename glob is the source of truth. Adding `--permission-mode foo` to the spike's matrix later means a new fixture file appears; this test picks it up automatically. The `expectedPermissionModeFromFilename` regex tolerates any `[A-Za-z]+` token, including future modes claude may add.

## Error handling

| Failure mode | Behavior |
|---|---|
| `filepath.Glob` returns zero matches | `t.Fatalf("no fixtures matched glob: %s", glob)`. Deleted fixtures must be loud. |
| Fixture file unreadable | Inside subtest: `t.Fatalf("read %s: %v", path, err)`. |
| Fixture top-level JSON malformed | `t.Fatalf("unmarshal %s: %v", path, err)`. |
| `system/init` event missing | `t.Errorf` naming the fixture; subtest continues to check what it can (result, forbidden events, exit code). |
| `result/success` event missing | Same — `t.Errorf`, continue. |
| Filename does not match regex | `t.Fatalf` inside subtest naming the fixture and the regex. |
| Inner event JSON malformed | `t.Errorf` with the event index; do not abort the whole subtest — a single bad event line shouldn't mask the other assertions. (Mirrors `allowed_tools_enforcement_test.go:60-67`.) |

## Testing strategy

This test IS the test. There are no unit tests for the test itself — it operates on committed fixtures and is exercised by `make e2e-realclaude` (or `go test -tags e2e_realclaude ./internal/e2e/realclaude/...`).

Manual verification at implementation time:

- Run `make e2e-realclaude` against the current fixture set: expect PASS across all six fixtures.
- Temporarily edit one fixture (e.g. add `"type":"control_request"` to `stdout_events`): expect FAIL with a message naming that fixture and "forbidden event type observed". Revert.
- Confirm `make check` does NOT run the new test (build-tag gating works).
- Confirm `go vet ./...` and `staticcheck ./...` pass without complaints. The test file should compile cleanly with the `e2e_realclaude` tag and be invisible without it.

## Open questions

None blocking. Two judgement calls flagged for the developer:

- **`t.Errorf` vs `t.Fatalf` for the per-finding assertions inside a subtest.** Spec prescribes `Errorf` so a single subtest run reports multiple flipped findings simultaneously (useful diagnostic when claude changes several behaviours in one release). If the developer disagrees on simplicity grounds, `t.Fatalf` is acceptable.
- **`t.Parallel()` on inner subtests.** Recommended (independent, cheap, no shared state) but not required. Skip if the developer prefers the simpler sequential read.

## Out of scope

- Re-running the real-API spike — `TestRealClaude_PermissionProtocol_Spike` covers that.
- Asserting on `tool_result` payload bodies — those reflect the spike-runner's machine-local POSIX environment per the spike doc.
- The assertion-based test referenced by #383 AC#4 — that follow-up is only filed if a real permission event fires; it has not.
- Updating `features/permission-protocol-spike.md` — the spike doc is correct as-is; this test pins what it already documents. If the test fails in the future, the spike-runner updates that doc as part of the response.
