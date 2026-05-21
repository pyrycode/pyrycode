# 498 — ptyrunner: emit `system/init` envelope to match streamrunner wire shape

## Files to read first

- `internal/agentrun/ptyrunner/runner.go:79-163` — `Config` (the type that grows one field) + field-required validation pattern (`MaxTurns required`, etc.) to mirror.
- `internal/agentrun/ptyrunner/runner.go:308-318` — the `streamjson.New` call site (the one production caller; gets new fields populated).
- `internal/agentrun/ptyrunner/runner.go:395-404` — `buildArgs` (interactive-TUI argv; explains why `--allowed-tools` is absent from argv and why ptyrunner has no `AllowedTools` field today — the deny-default settings file is the runtime gate, but the wire-shape envelope needs the human-readable list).
- `internal/agentrun/streamjson/emitter.go:43-113` — `Config` + `New` + `Emitter` state (the symmetric site to the new init synthesis; mirror trailer's `Close` shape).
- `internal/agentrun/streamjson/emitter.go:204-273` — `trailer` struct + `wireFields`. The trailer's tagged field order is the load-bearing JSON key-order convention to mirror for the init struct.
- `internal/agentrun/streamjson/testdata/captured_run.jsonl:1` — wire-shape reference for the init line. Six required fields in this exact key order: `type`, `subtype`, `cwd`, `tools`, `model`, `session_id`.
- `internal/agentrun/streamjson/emitter_test.go:18-46` — `newTestEmitter` (the helper that every existing streamjson test funnels through; receives three new field values).
- `internal/agentrun/jsonl/reader.go:160-213` — `knownKinds` whitelist (explains how `permission-mode` / `file-history-snapshot` / `ai-title` reach stdout today: `Reader.Next` surfaces every well-formed line as an `Event` with `Kind=""` for unrecognised types, then `Emitter.Emit` writes `ev.Raw` verbatim).
- `internal/agentrun/ptyrunner/runner_test.go:30-60` — `helperRunCfg` (the test-side `Config` constructor; one edit adds the new field to every test).
- `internal/agentrun/ptyrunner/runner_test.go:102-146` — `TestRun_HappyPath_EmitsAndEndOfTurn` (the `bytes.HasPrefix(got, []byte(happyPathBody))` assertion is the load-bearing test that must move past the new leading line).
- `internal/agentrun/ptyrunner/runner_test.go:478-525` — `TestRun_MissingRequiredFields` (table to extend for the new nil-`AllowedTools` row).
- `cmd/pyry/agent_run.go:288-322` — `runAgentRunPty` (single new line: pass `parsed.allowedTools` into the new `ptyrunner.Config.AllowedTools`).
- `cmd/pyry/agent_run_test.go:733-758` — `TestRunAgentRun_DispatchesToPtyRunnerByDefault` (extend the captured-Config zero-check).
- `internal/e2e/realclaude/ptyrunner_byte_equivalence_test.go:84-145` — `envelopeShape` + `extractShapes` + the comment block explaining the leading-envelope "normalization" gap (comment block trimmed; `extractShapes` itself stays type+subtype-only).
- `internal/e2e/realclaude/ptyrunner_byte_equivalence_test.go:264-280` — the ptyrunner.Run call site (add `AllowedTools: allowedTools`).
- `internal/e2e/realclaude/ptyrunner_byte_equivalence_test.go:373-397` — `checkInitModel` (the existing field-level init invariant; extended to also assert cwd / tools / session_id).
- `internal/e2e/realclaude/fixtures.go:342-358` — `parseInitSessionID` (AC pins: do NOT modify; the new producer-side init line must make this return non-empty unchanged).

## Context

`pyry agent-run`'s default path (`runAgentRunPty` → `ptyrunner.Run`) spawns interactive claude under a PTY and tails the per-session JSONL file under `~/.claude/projects/<encoded-cwd>/<sid>.jsonl`, re-emitting each line verbatim through `streamjson.Emitter` and composing a trailing `type:"result"` line. The on-disk JSONL starts with non-system events — `permission-mode`, `file-history-snapshot`, `user`, … — because the `system/init` envelope is emitted only by the `-p` (non-interactive) shape of claude that streamrunner drives, not by the interactive TUI.

The [[Drop-In Contract]] for the agent-run migration says ptyrunner's stdout wire shape is byte-equivalent in structure to streamrunner's: same dispatcher parser, no per-runner branching. The missing `system/init` line breaks two consumers:

1. `parseInitSessionID` (`internal/e2e/realclaude/fixtures.go:342`) anchors session-id discovery on `type=system, subtype=init`. With no such line in ptyrunner's output, every test that asserts `result.SessionID != ""` fails on otherwise-successful runs.
2. `TestPtyRunnerVsStreamRunner_StructuralEquivalence` (`internal/e2e/realclaude/ptyrunner_byte_equivalence_test.go`) asserts `pty[0].Type == "system" && pty[0].Subtype == "init"` at line 338. The test is `//go:build e2e_realclaude` and only runs under `make e2e-realclaude`, so the divergence is currently silent in non-realclaude CI; but it is the load-bearing contract test for the migration's claim.

Per the ticket's Technical Notes, the chosen fix is producer-side: ptyrunner synthesizes the envelope from its existing inputs (cwd, model, tools, session_id) and writes it to `cfg.Stdout` strictly before the first JSONL line. The rejected alternative — loosen `parseInitSessionID` to match whatever envelope comes first — admits the divergence and would force every future consumer to special-case ptyrunner.

## Design

### Where the synthesis lives: `streamjson`

The `streamjson` package owns the wire shape — `Close` already composes the trailing `result` envelope; symmetrically, `New` composes the leading `system/init` envelope. One package, both ends of the wire shape, mirroring the trailer's `tagged struct + JSON key order` pattern.

### `streamjson.Config` additions

Three new required fields:

- `Cwd string` — claude's working directory. Required (non-empty). Stamped into init's `cwd`.
- `Tools []string` — tool allowlist (the names that appear in the deny-default settings file). Required (non-nil; empty slice OK). Stamped into init's `tools`.
- `Model string` — model identifier. Required (non-empty). Stamped into init's `model`.

`SessionID` (already present, required) stamps into init's `session_id`. `Type` / `Subtype` are constants `"system"` / `"init"`.

Validation matches the existing nil-Writer / empty-SessionID pattern. Sentinels are bare `errors.New("streamjson: …")` shapes (not exported sentinels — defensive validation; no caller is expected to branch on the exact text).

### Init struct contract

Add an unexported struct in `emitter.go` (sibling to `trailer`) defining the exact JSON key order:

- `Type string \`json:"type"\``
- `Subtype string \`json:"subtype"\``
- `Cwd string \`json:"cwd"\``
- `Tools []string \`json:"tools"\``
- `Model string \`json:"model"\``
- `SessionID string \`json:"session_id"\``

JSON key order is the field-declaration order in the struct — same convention as `trailer` (line 251-266). Reordering the struct breaks byte-equivalence with the captured fixture, even though the resulting JSON is still semantically valid. The fixture (`streamjson/testdata/captured_run.jsonl:1`) is the source of truth; the struct's tag order matches it.

The `Tools []string` field must marshal as `[]` (NOT `null`) when the caller passes an empty allowlist. Go's `encoding/json` already marshals a non-nil empty slice as `[]`, so the wire contract is satisfied as long as Config validation rejects `nil Tools` (and accepts `[]string{}`). Test pins this.

### `streamjson.New` flow

1. Validate Writer, SessionID, Cwd, Tools (non-nil), Model. Errors bubble up unchanged.
2. Apply Now / Logger defaults (unchanged).
3. Capture `start := now()` (unchanged — the init write does NOT advance `start`; duration_ms still measures from construction).
4. Marshal the init struct + append `'\n'`.
5. Write to `cfg.Writer`. On write error: capture into the returned `*Emitter`'s sticky `writeErr` and return `(emitter, fmt.Errorf("streamjson: emit init: %w", err))`. Returning the Emitter alongside the error is a behavior change worth flagging — see Open questions; the alternative (return `nil, err`) is what the existing call-sites assume and is the recommended shape.
6. Return the Emitter (no error).

Recommended shape (matches existing Emitter contract): on init write failure, `New` returns `(nil, err)`. The Emitter is unusable in that case; the caller (`ptyrunner.Run`) already wraps `streamjson.New` failure as `fmt.Errorf("ptyrunner: emitter: %w", err)`.

### Mu / concurrency

The init write happens synchronously inside `New`, before any other goroutine can observe the Emitter. The Emitter's `mu` is NOT held — no other path is reachable until New returns. The init line is structurally the first bytes on the writer because Emit and Close are unreachable beforehand. No new goroutines, no new locks.

### `ptyrunner.Config` addition

One new required field:

- `AllowedTools []string` — the tool allowlist matching the names in the deny-default settings file. Required (non-nil; empty slice OK). Passed through verbatim to `streamjson.Config.Tools`.

Doc-comment cross-references the settings file: this field is the human-readable list for the init envelope; the runtime enforcement is still the deny-default settings file written by `internal/agentrun/settings` and passed via `--settings`. The two are caller-synchronised at the `runAgentRunPty` site.

`Run` adds one new validation line in the same shape as the existing required-field checks (line 210-242):

```
if cfg.AllowedTools == nil {
    return errors.New("ptyrunner: AllowedTools required")
}
```

Note: `nil` is the rejected sentinel; `[]string{}` is accepted (empty allowlist is a valid configuration).

`Run`'s `streamjson.New` call (line 311-315) grows three field assignments: `Cwd: cfg.WorkDir`, `Tools: cfg.AllowedTools`, `Model: cfg.Model`. No reordering of construction or defer-LIFO is needed — `streamjson.New` already runs before the tail watcher and watchdog goroutines start.

### `cmd/pyry/agent_run.go` wiring

One line in `runAgentRunPty` (line 309-321):

```
AllowedTools: parsed.allowedTools,
```

`parsed.allowedTools` is already required at parse time (line 152) and is non-nil for every code path that reaches `runAgentRunPty`.

### Data flow

```
agent-run argv → parsed.allowedTools ─┐
                                      │
                  cfg.WorkDir ────────┤
                  cfg.Model ──────────┼─→ streamjson.New writes the leading
                  cfg.SessionID ──────┤    {"type":"system","subtype":"init",
                  cfg.AllowedTools ───┘     "cwd":...,"tools":[...],
                                            "model":...,"session_id":...}\n
                                            to cfg.Stdout
                                              │
                                              ▼
                                  tail.Watcher OnEvent → emitter.Emit(ev)
                                              │
                                              ▼
                                       end-of-turn → emitter.Close writes
                                                    the trailing result line
```

### Error handling

- Missing Cwd / nil Tools / empty Model: `streamjson.New` returns wrapped via the existing required-field-check pattern (errors.New "streamjson: empty Cwd" / "streamjson: nil Tools" / "streamjson: empty Model").
- JSON marshal of init struct: defensive `fmt.Errorf("streamjson: marshal init: %w", err)`. The struct has no fields whose values can fail to marshal (`[]string` is always marshallable); guard exists for symmetry with `trailer`'s marshal error.
- Writer.Write error on init: `(nil, fmt.Errorf("streamjson: emit init: %w", err))`. Caller (`ptyrunner.Run`) wraps as `fmt.Errorf("ptyrunner: emitter: %w", err)` via the existing path.
- ptyrunner missing AllowedTools: `errors.New("ptyrunner: AllowedTools required")`.

## Testing strategy

### `internal/agentrun/streamjson/emitter_test.go`

Helper update: `newTestEmitter` grows three deterministic field values (e.g. `Cwd: "/tmp/test"`, `Tools: []string{"Read"}`, `Model: "test-model"`). Every existing test that uses the helper then sees an init line as the first stdout line; the existing `bytes.HasPrefix` / `bytes.Equal` assertions on `buf` are updated to strip or skip past the leading init line — typically by comparing against `wantInit + "\n" + tc.ev.Raw + "\n"` or by indexing past the first newline.

New tests:

- `TestNew_WritesInitLineFirst` — calls New with deterministic field values; reads buf; asserts the first newline-terminated line decodes to the init struct shape with `type=system, subtype=init` and all other fields matching the config inputs.
- `TestNew_InitLineKeyOrderMatchesFixture` — byte-compares the init line against `testdata/captured_run.jsonl:1`'s key order. The dynamic field VALUES will differ (different session id, etc.), but the static SHAPE (`{"type":"system","subtype":"init","cwd":"…","tools":[…],"model":"…","session_id":"…"}`) must match key-for-key. Implement by JSON-parsing both lines into `map[string]json.RawMessage` is INSUFFICIENT (map loses order); instead, walk the byte slice and assert the substring `"type":"system","subtype":"init","cwd":` appears at offset 1 (after the leading `{`). The exact byte-level check is what pins the contract; a value-only check would let the trailer-side struct silently reorder.
- `TestNew_EmptyToolsMarshalsAsEmptyArray` — Config.Tools is `[]string{}` (non-nil empty); assert the init line contains `"tools":[]` (NOT `"tools":null`).
- `TestNew_ValidatesNewRequiredFields` — table-driven, mirroring `TestNew_ValidatesConfig`'s shape; cases: empty Cwd, nil Tools, empty Model. Each must produce a non-nil error from `New`.
- `TestNew_InitWriteFailureReturnsError` — Writer is a `failingWriter` (mirror runner_test.go's pattern, or reuse if you extract it); assert New returns a non-nil error wrapping `"streamjson: emit init"`; assert the returned `*Emitter` is `nil` (caller-unsafe shape).

### `internal/agentrun/ptyrunner/runner_test.go`

- `helperRunCfg` adds `AllowedTools: []string{"Read"}` to the returned Config (one line).
- `TestRun_HappyPath_EmitsAndEndOfTurn` — line 119-122 currently asserts `bytes.HasPrefix(got, []byte(happyPathBody))`. Replace with: split `got` by `'\n'`, assert `len(lines) >= 3` (init + assistant + trailer); assert `lines[0]` decodes to the init shape with `Cwd == cfg.WorkDir`, `Tools` deep-equals `cfg.AllowedTools`, `Model == cfg.Model`, `SessionID == cfg.SessionID`, `Type == "system"`, `Subtype == "init"`; assert `lines[1]` equals `happyPathBody`'s line (without the trailing `\n`); the trailer assertions below (lines 124-145) stay unchanged because `parseTrailer` reads the LAST line.
- `TestRun_MissingRequiredFields` (line 478-525) — extend the table with one new case: `name: "missing AllowedTools"`, mutate cfg to set `cfg.AllowedTools = nil`, expected error substring `"AllowedTools required"`.

### `cmd/pyry/agent_run_test.go`

- `TestRunAgentRun_DispatchesToPtyRunnerByDefault` (line 733-758) — extend the zero-field check at line 753-758 to include `captured.AllowedTools == nil` (one additional `|| ...` in the existing `if` block).

### `internal/e2e/realclaude/ptyrunner_byte_equivalence_test.go`

- Line 264-280: add `AllowedTools: allowedTools` to the ptyrunner.Config literal (one new line). `allowedTools` is already declared at line 207.
- Line 86-110 (comment block): trim the bullets that explain ignoring the leading envelope's required-field set. The result-trailer field-set differences are real and the comment about them stays. The shape-comparison rationale stays.
- `checkInitModel` (line 374-397) — extend the decoded struct to include `Cwd string`, `Tools []string`, `SessionID string`; rename to `checkInit`; take new args `wantCwd string, wantTools []string`. Body asserts:
  - `env.Type == "system"`, `env.Subtype == "init"` (these are also asserted in `compareShapes`, but explicit here makes the failure message diagnostic).
  - `env.SessionID != ""` (matches the parseInitSessionID contract).
  - `env.Cwd == wantCwd` (each pipeline passes its own workdir).
  - `env.Model == wantModel` (existing assertion).
  - `reflect.DeepEqual(env.Tools, wantTools)`.
- Call sites for checkInit (replacing the two checkInitModel calls at line 297-298): pass `workdirStream` + `allowedTools` for the streamrunner side; pass `realpath` + `allowedTools` for the ptyrunner side. (Each side's cwd matches the workdir it was invoked with.)

### `internal/e2e/realclaude/fixtures.go`

No change. `parseInitSessionID` (line 342-358) keeps its three-field decode (`type`, `subtype`, `session_id`). The producer-side fix makes it return non-empty on a successful ptyrunner run unchanged.

### Test pinning the wire-shape contract

The byte-level key-order assertion (`TestNew_InitLineKeyOrderMatchesFixture`) is the load-bearing pin: it locks the struct's tag declaration order to the captured fixture. A future contributor reordering struct fields will break only this test, with a diagnostic that points at the fixture as the source of truth. Without this pin, the byte-equivalence test would catch the drift only under `e2e_realclaude` CI (network + ANTHROPIC_API_KEY required); the unit-test pin catches it on every `go test ./internal/agentrun/streamjson/...` run.

## Open questions

1. **`New` return shape on init-write failure.** Recommendation: return `(nil, err)` (Emitter unusable). The alternative — return `(emitter, err)` so the caller can still call `Close` for cleanup — adds caller complexity for negligible benefit (Close on a write-failed Emitter would re-fail). The recommended shape matches the existing convention of New (today's New cannot return `(emitter, err)` because no IO happens). Decision: go with `(nil, err)`. Flagged for visibility, not awaiting input.
2. **Should `Effort` appear in the init envelope?** The captured fixture (`streamjson/testdata/captured_run.jsonl:1`) does NOT include it; the ticket's required-field list does NOT include it. Skip. If a future migration needs `effort` for symmetry with claude's own envelope, file a follow-up; do not extend the contract here.
