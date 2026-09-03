# #2045 — a one-run drift verdict for phase 2's B1/B2

## Files read

- `internal/e2e/realclaude/interactive_stream_inband_model_test.go` →
  `TestInteractiveStream_InBandModelChange_LiveChildReportsNewModel` (B1/B2 and the
  `t.Logf("#1838: menu %+v", …)` that already puts the live menu in a red run's output),
  `inbandMenuRow`, `inbandTapRecorder.consume`, `inbandPickBracketedValue`,
  `inbandWaitMenu` — the phase this ticket changes, the row type it reuses, and the
  nesting the live decode already reads.
- `internal/e2e/realclaude/initialize_control_names_test.go` → `initControlFixtureName`
  (the one-input namer minting `initialize_control_v<slug>.json`, i.e. the committed
  one-arm capture's name) and `initControlArmFixtureName` (the two-input namer, which is
  **not** what this ticket wants — it mints the three arm captures). The header states
  why the `initialize_control_v` prefix must never be interpolated by hand.
- `internal/e2e/realclaude/initialize_control_compare_test.go` → `initControlDiscoverArms`
  — the package's only precedent for an offline file that legitimately reads
  `testdata/`, including its relative-glob argument (`go test` runs in the package
  source directory, so no `packageDir` call is needed) and its
  name-binds-to-content discipline.
- `internal/e2e/realclaude/offline_exec_ban_test.go` → `finOfflineExecBans`,
  `TestFinOfflineFilesReachNoExecHelper` — the per-file AST ban list a new offline file
  must join, and the entry shape to copy.
- `internal/e2e/realclaude/initialize_control_record_test.go` → `initControlFixtureRecord`
  — `ControlResponses []json.RawMessage` and `ClaudeVersion`; the compare file's rule is
  to decode through this record rather than a parallel struct, because a parallel shape
  is how a field rename lands as a silent zero value.
- `internal/e2e/realclaude/permission_protocol_spike_test.go` → `captureClaudeVersion`
  (the package's single `claude --version` exec, returning the token every fixture name
  is minted from), `versionSlug`, `packageDir`.
- `internal/e2e/realclaude/resilience_test.go` → `resolveClaudeBin` — it honours
  `PYRY_CLAUDE_BIN` where `captureClaudeVersion` always execs bare `claude`. Recorded
  under **Open questions**; it is why the verdict names the version it used.
- `internal/e2e/realclaude/testdata/initialize_control_v2.1.239.json` — the committed
  baseline. Confirmed by hand: `control_responses` is a one-element array whose
  `response.response.models` carries six rows, `claude-fable-5[1m]` → `claude-fable-5`
  among them, and `opus` → `claude-opus-5` with no bracket.
- `docs/knowledge/features/e2e-realclaude-interactive-stream-inband-model-test-go.md` —
  the #2041 entry records that the *same* drift (`claude-fable-5-1[1m]` live vs
  `claude-fable-5[1m]` committed, one binary version) already forced
  `permission_mode_switch_probe_test.go` to pick its model out of a live discovery
  child rather than a table. That is the lesson this ticket generalises: the committed
  capture is a baseline to compare against, never a source to read a live value from.
- `CODING-STYLE.md` § Comments — Citing Other Code; `CLAUDE.md` § Testing (a red run
  commits nothing, so no capture file may be written on the failing path).

## Context

On 2026-09-02 phase 2's B1/B2 went red on #2041, a branch touching zero production
source files, because claude published `claude-fable-5-1[1m]` → `claude-fable-5-1` in
that run and `claude-fable-5[1m]` → `claude-fable-5` eight minutes later at the same
binary version, and did not apply the first form in band. The assertions were right to
redden — the defect is claude's — but a reader of a single red run cannot tell that from
a branch-caused regression without re-running and hoping to catch the flap again. That
cost a rework leg.

A red run already carries the live menu: phase 2 logs it unconditionally before the
assertions, and Go prints a failing test's buffered log. What it cannot say is whether
that menu is the one claude published when the baseline was taken. This repo commits
that baseline per claude version, so the missing step is a comparison, not a capture.

No production source file is touched. This ticket does not weaken B1/B2, does not retry
them, does not refresh the committed capture, and does not touch `internal/relay`'s
`validModel`.

**No ADR is warranted.** The finding itself is already written up in the package
overview and must not be re-recorded; this is one comparator inside one live test.

## Design

Two files, one new.

### New: `internal/e2e/realclaude/interactive_stream_inband_menu_drift_test.go`

Offline, credential-free, reads `testdata/` and never writes it.

```go
type inbandDriftOutcome string

const (
	inbandDriftMatch     inbandDriftOutcome = "match"
	inbandDriftDrift     inbandDriftOutcome = "drift"
	inbandDriftNoCapture inbandDriftOutcome = "no-capture"
)

// inbandMenuCapturePath returns the RELATIVE path of the committed capture for
// versionToken, through initControlFixtureName.
func inbandMenuCapturePath(versionToken string) string

// inbandCommittedMenu reads the model menu out of that capture. It returns the
// path in every case, so a caller can name what it looked for whether or not the
// read succeeded.
func inbandCommittedMenu(versionToken string) (path string, menu []inbandMenuRow, err error)

// inbandCompareMenu classifies the row phase 2 chose against the committed
// capture and renders the operator-facing verdict.
func inbandCompareMenu(versionToken string, chosen inbandMenuRow, live []inbandMenuRow) (inbandDriftOutcome, string)
```

**The path is built through `initControlFixtureName`, never by interpolating the
`initialize_control_v` prefix.** That prefix is load-bearing for three foreign fixture
globs, and both namer locks exist to keep it out of reach of any input.

**The one-input namer, not the arm-carrying one.** `initControlArmFixtureName` mints the
three arm captures of #1763's measurement; #1688's single capture is what carries the
menu this ticket compares against, and it is the file the ticket names.

**The path is relative.** `go test` runs in the package source directory, which is
exactly the argument `initControlArmFixtureGlob` already relies on, so no `packageDir`
call is needed and none is made — that name stays banned for this file.

**Addressed by exact name, never by a glob.** A glob would find a capture at a
*different* version and answer "match" against the wrong baseline, silently. The
absence of a capture at the running version is a verdict this ticket wants reported
(AC 2), not a hole for a neighbouring version to fill.

**Decode:** outer through `initControlFixtureRecord`, so a field rename in the record
cannot land here as a silent zero value; inner through an anonymous struct declaring
`response.response.models` as `[]inbandMenuRow`, the same nesting and the same row type
`inbandTapRecorder.consume` decodes off the live tap. The first `control_responses`
element carrying a non-empty `models` array wins, mirroring `consume`'s non-empty guard
rather than a subtype check.

**Comparison:** the chosen row matches when some committed row has BOTH the same
`value` and the same `resolvedModel`. Not the whole menu — the chosen row is what B1/B2
acted on, and a menu that grew an unrelated row is not the drift being diagnosed.

**Verdict text**, all three carrying `#2045` and the version token:

| outcome | names | also lists |
|---|---|---|
| `match` | version, capture path | — (and says a red B1/B2 is therefore not this flap) |
| `drift` | version, capture path | committed published values AND live published values |
| `no-capture` | version, the path it looked for | — (and says the test is not failed on this account) |

An unreadable or undecodable capture folds into `no-capture` with the read error named,
so the function is total over whatever is on disk.

**The table test** — `TestInbandMenuDrift_ClassifiesTheChosenRowAgainstTheCommittedCapture`
— is table-driven over the real committed capture and one uncaptured version token. No
credentials, no subprocess, no live child, no write. Rows as bullet-pointed scenarios:

- **match** — `2.1.239`, chosen `{claude-fable-5[1m], claude-fable-5}`. Doubles as the
  liveness control: delete or rename the capture and this row reports `no-capture`.
- **the 2026-09-02 incident** — `2.1.239`, chosen
  `{claude-fable-5-1[1m], claude-fable-5-1}`. Both fields moved; this is the literal
  observed pair.
- **resolution moved under an unchanged value** — chosen
  `{claude-fable-5[1m], claude-fable-5-1}`. The SOLE red for a comparator keying on
  `value` alone.
- **value moved onto a published resolution** — chosen
  `{claude-fable-5-1[1m], claude-fable-5}`. The SOLE red for a comparator keying on
  `resolvedModel` alone. One clause per reject row, so neither is over-determined.
- **no capture at this version** — `0.0.0-uncaptured`.

Each row asserts the outcome AND that the rendered text contains its load-bearing
substrings. A guard rejects an empty needle: `strings.Contains(s, "")` is true for every
`s`, so an empty expectation would pass unconditionally.

**`finOfflineExecBans` gains an entry for the new file**: `initialize_control_compare_test.go`'s,
copied, with `filepath.Glob` and `packageDir` kept BANNED rather than dropped —
this file addresses one capture by exact name and needs neither, and a glob is precisely
the wrong-baseline hazard above. Only `os.ReadFile` is permitted. Every write name stays
banned; a red run must commit nothing.

### Modified: `interactive_stream_inband_model_test.go` (phase 2 only)

Phase 1 (A1–A4) and the header's § Evidence are unchanged byte-for-byte. Additive:

- B1 and B2 set a local flag alongside their existing `t.Errorf` calls; their messages
  are unchanged.
- Guarded by that flag: capture the version token, call `inbandCompareMenu` with the
  chosen row and the live menu, and `t.Logf` the outcome and the verdict. Both passing
  ⇒ nothing is emitted and no `claude --version` is spent.
- A new header section records what the verdict is for and points at the mutant recipe
  for producing the red on demand.

## Concurrency model

None added. Everything here is a pure function plus one `os.ReadFile`, called from the
test goroutine after `inbandWaitMenu` has already returned a snapshot copy of the menu
under `inbandTapRecorder`'s mutex. Nothing is shared with the runner goroutine, no
goroutine is started, and `-race` has nothing new to see.

## Error handling

Nothing here may fail the test, which is the whole point of AC 2 — a missing baseline is
a fact to report, not a defect. Concretely:

- The capture absent ⇒ `no-capture`, `fs.ErrNotExist` recognised via `errors.Is` and
  reported as "no capture is committed for this version".
- The capture unreadable or undecodable ⇒ `no-capture` with the wrapped error named.
- The capture readable but carrying no `models` array ⇒ `no-capture`, reported as such.
- `captureClaudeVersion` `t.Fatalf`s if `claude --version` fails — acceptable, because it
  is reached only on a path where B1 or B2 has already failed the test.

Errors are wrapped with `fmt.Errorf("…: %w", err)` per house style; nothing panics.

## Testing strategy

- `go vet ./...` and `go build ./cmd/pyry` — the standard gate. `make check` cannot
  compile this package (`e2e_realclaude` build tag), so its green says nothing here.
- `go test -tags e2e_realclaude -race -count=1 -run 'TestInbandMenuDrift_|TestFinOfflineFilesReachNoExecHelper' ./internal/e2e/realclaude/`
  must report **PASS, not SKIP**, on a machine with no claude and no credentials. Read
  the count of `=== RUN` lines, never the exit code: this package exits 0 both on a
  build failure and on a full credentials skip.
- AC 3's failing half is shown by mutant, not by waiting for claude to flap: the file
  header already documents `go test -overlay=<abs>/overlay.json` runs against this exact
  file, so an overlay whose phase 2 forces B1 red produces the verdict on demand with no
  mutated source written into the worktree. The passing half is shown by an ordinary
  green run carrying no verdict line.
- The full `make e2e-realclaude` suite is the dispatcher's gate, judged by its `=== RUN`
  count.

## Open questions

1. **Does `captureClaudeVersion` name the same binary `resolveClaudeBin` ran?** Not
   always — `resolveClaudeBin` honours `PYRY_CLAUDE_BIN` and `captureClaudeVersion`
   always execs bare `claude`. Resolved by using `captureClaudeVersion` anyway and
   naming the version in the verdict: every fixture under `testdata/` was named from
   that primitive, so anchoring the comparison anywhere else would look for a file the
   family never mints. A reader who set `PYRY_CLAUDE_BIN` sees which version was
   compared and can discount the verdict.
2. **Should a drift verdict fail the test?** No, and deliberately: B1/B2 already failed
   it. A second red would double-count one finding, and a drift verdict on a *green*
   phase would be a new failure mode this ticket was not asked for.
3. **Should the comparison run when B1 and B2 both pass?** No — AC 3 forbids it. Settled
   in the design above by the guard flag.
