# Spec #411 — `agent-run`: forward `--disallowed-tools` into settings `permissions.deny`

**Size:** S · **Security-sensitive:** no (no `security-sensitive` label; trusted-dispatcher input, human-interaction tools, no network/crypto/untrusted surface)

## Files to read first

- `internal/agentrun/settings/settings.go:33-97` — `settingsFile` + `permissions` structs (field-order is **byte-load-bearing**, see the doc comment at :22-32) and the `WriteSettings` body. This is where the deny field and the split into a shared core live.
- `internal/agentrun/settings/settings_test.go:76-113` — `TestWriteSettings_SingleToolGoldenBytes` and `..._PreservesOrderAndDuplicates`: the exact golden-byte + order/dup patterns to **mirror** for the deny path. `:11-74` — empty-input validation the shared core must keep enforcing.
- `cmd/pyry/agent_run.go:27-32` — the `settingsWrite = settings.WriteSettings` test seam (switches to `WriteSettingsWithDeny`). `:82-97` — `splitAllowedTools` (reuse verbatim for deny). `:102-188` — `parseAgentRunArgs` (add the flag + field + tokenise). `:288-323` — `runAgentRunPty`; the writer call is at `:298`.
- `cmd/pyry/agent_run_test.go:696-960` and `:1060-1160` — the six `settingsWrite = func(tools []string) …` mock reassignments (715, 850, 905, 937, 1078, 1141) that must grow a second slice param. `:1245` — `TestSplitAllowedTools` already covers comma/space/mixed tokenisation (deny reuses it).
- `internal/agentrun/selfcheck/selfcheck.go:104,270` — **DO NOT CHANGE.** Confirms selfcheck keeps its own `settingsWrite = settings.WriteSettings` seam calling `settingsWrite(canonicalAllow)` (1-arg). The additive design deliberately leaves this path untouched.
- `internal/e2e/realclaude/ptyrunner_byte_equivalence_test.go:436` — **DO NOT CHANGE.** Stays on `settings.WriteSettings(allowedTools)`.

## Context

`pyry agent-run` drives one headless claude turn under a PTY. Its tool gate is the per-spawn settings file written by `settings.WriteSettings`, passed to claude via `--settings`, with `permissions.defaultMode:"dontAsk"` and a `permissions.allow` whitelist. Anything **not** in `allow` is offered to the model but runtime-denied on call under `dontAsk`.

The problem (origin pyrycode#398, recovery PR #410): claude's human-interaction tools (`AskUserQuestion`, `EnterPlanMode`, `ExitPlanMode`) are still *offered* to non-interactive agents. A call to one wastes a turn on a guaranteed runtime denial, and that denial arms the agent-dispatcher#8 permission-denial watchdog, which force-exits on the next tool call.

The 2026-07-04 spike settled the enforcement lever: **listing those names in the settings file's `permissions.deny` removes them from the model's tool surface entirely** — not offered, not called, no denial event, no watchdog arming. This is *not* a CLI flag forwarded to claude (the ticket title's stale shape, ruled out by the spike). This ticket adds an optional `--disallowed-tools` flag whose tokens land in `permissions.deny`, mirroring the existing `--allowed-tools` → `permissions.allow` wiring.

**Ordering:** the companion agent-dispatcher#7 starts passing `--disallowed-tools …` once live. This pyry side must ship **and `pyry` must be rebuilt/reinstalled** before the dispatcher flips, or every dispatch errors on the unknown flag.

## Design

### Decision: additive entry point, not an atomic signature change

`settings.WriteSettings(allowed []string)` has four caller families: agent-run's PTY path, `selfcheck` (which has no denylist concept), the realclaude e2e byte-equivalence pin, and the package's own golden-byte tests. Changing its signature to `(allowed, disallowed []string)` ripples to **~20 call sites** (2 production + 6 agent-run mocks + 3 selfcheck mocks + 9 direct golden/e2e calls) — over the developer edit-fan-out budget, and it dirties the very byte-equivalence tests that AC3 (back-compat, byte-unchanged) relies on to prove nothing regressed.

Instead: keep `WriteSettings(allowed []string)` byte-identical and add a sibling. Only agent-run's seam moves; **selfcheck, realclaude, and the existing settings golden tests are provably untouched** (which is the cleanest possible evidence for AC3). Forced compile edits drop to 7. This is the ticket's explicitly-sanctioned "additive entry point" option.

### `internal/agentrun/settings/settings.go`

**`permissions` struct** — add one optional field, positioned between `Allow` and `DefaultMode` so the present-case key order reads `allow, deny, defaultMode`:

```go
type permissions struct {
    Allow       []string `json:"allow"`
    Deny        []string `json:"deny,omitempty"` // NEW — omitted entirely when empty/nil
    DefaultMode string   `json:"defaultMode"`
}
```

`omitempty` is load-bearing: nil or `len==0` → the `deny` key is dropped → byte-identical to today for every deny-less caller. This is what makes AC3/AC4 fall out structurally rather than via a special case.

**Three functions** — one private core, two thin public entry points:

- `writeSettings(allowed, disallowed []string) (string, error)` (unexported) — the current `WriteSettings` body, moved verbatim, with `Deny: disallowed` added to the `permissions` literal. Keeps the existing `len(allowed)==0` guard and its exact error string `"agentrun/settings: allowedTools required"` (asserted at `settings_test.go:59`). Deny is **not** validated for emptiness — empty is the legal "no deny key" case.
- `WriteSettings(allowed []string) (string, error)` — wrapper: `return writeSettings(allowed, nil)`. Doc/contract/bytes unchanged for existing callers.
- `WriteSettingsWithDeny(allowed, disallowed []string) (string, error)` — wrapper: `return writeSettings(allowed, disallowed)`. Doc mirrors `WriteSettings` and notes the optional `permissions.deny` list (verbatim round-trip, order + duplicates preserved, omitted when empty).

Update the package/struct doc comments (`:22-32`, `:43-66`) so the documented canonical byte sequence shows the optional `deny` slot; note it appears only when non-empty.

### `cmd/pyry/agent_run.go`

- **Seam** (`:29`): `settingsWrite = settings.WriteSettingsWithDeny`. (Its inferred type becomes `func([]string, []string) (string, error)` — this is the only type change, and the sole driver of the six test-mock edits.)
- **Flag** (in `parseAgentRunArgs`, alongside `:108`): `fs.String("disallowed-tools", "", "comma- or space-separated tool denylist (optional)")`. Self-documents via `fs.PrintDefaults()`; **do not** touch `agentRunUsageDescription` (locked by the #359 prose test — leaving it alone avoids that fan-out).
- **Struct field**: add `disallowedTools []string` to `agentRunArgs` (`:37-46`).
- **Parse** (after the allow block ends at `:156`): `parsed.disallowedTools = splitAllowedTools(*disallowedTools)`. No required/non-empty check — optional. Reuse `splitAllowedTools` as-is (pure tokeniser; its name stays historical — see Open Questions).
- **Call site** (`:298`): `settingsWrite(parsed.allowedTools, parsed.disallowedTools)`.

Legacy `PYRY_USE_STREAMJSON=1` path (`buildStreamRunnerClaudeArgs`) is **out of scope** — it does not offer these tools, so no deny is load-bearing there (ticket-confirmed). Leave it untouched.

### Data flow

```
--disallowed-tools "A,B,C"
  → splitAllowedTools → parsed.disallowedTools=[A,B,C]
  → settingsWrite(allow, [A,B,C]) = WriteSettingsWithDeny
  → writeSettings → permissions{Allow:allow, Deny:[A,B,C], DefaultMode:"dontAsk"}
  → JSON: {"permissions":{"allow":[…],"deny":["A","B","C"],"defaultMode":"dontAsk"},"enableAllProjectMcpServers":true}
  → --settings <path> → claude omits A,B,C from the model's tool surface

absent / empty flag
  → parsed.disallowedTools = nil/[]  → Deny omitted (omitempty)  → bytes identical to today
```

## Concurrency model

N/A. Synchronous CLI parse + single tempfile write. No goroutines, channels, or shared state introduced. Tempfile lifecycle (caller `defer os.Remove(settingsPath)` at `agent_run.go:302`) is unchanged.

## Error handling

- Empty allow-list → shared core returns the existing error (defence-in-depth behind the CLI's own `--allowed-tools` required check). Both public entry points inherit it. Add one test that `WriteSettingsWithDeny(nil, deny)` still errors, proving the core guard applies to the new door.
- Empty/absent deny → **not** an error; produces no `deny` key.
- Encode/close failures → unchanged best-effort tempfile removal in the core.

## Testing strategy

Bullet scenarios; developer writes them in the package's table-driven idiom (stdlib only).

**`internal/agentrun/settings/settings_test.go`** (add; existing tests stay green untouched):
- Deny golden bytes: `WriteSettingsWithDeny(["Bash"], ["AskUserQuestion","EnterPlanMode","ExitPlanMode"])` → exactly `{"permissions":{"allow":["Bash"],"deny":["AskUserQuestion","EnterPlanMode","ExitPlanMode"],"defaultMode":"dontAsk"},"enableAllProjectMcpServers":true}\n`. (Covers AC1.)
- Empty deny omits key: `WriteSettingsWithDeny(["Bash"], nil)` **and** `(["Bash"], []string{})` → byte-identical to the `WriteSettings(["Bash"])` golden (no `deny` key). (Covers AC3/AC4.)
- Deny order + duplicates preserved (mirror `..._PreservesOrderAndDuplicates`).
- Empty allow still errors via `WriteSettingsWithDeny(nil, ["X"])`.

**`cmd/pyry/agent_run_test.go`**:
- Update the six `settingsWrite` mocks (715, 850, 905, 937, 1078, 1141) to the 2-slice signature; where a mock captures `tools` to assert allow reached the writer, capture the deny slice too.
- Parse cases for `--disallowed-tools`: present (`'A,B,C'` and mixed `'A B,C'`) → `[A,B,C]`; absent → nil/empty; `''` → nil/empty (AC2/AC4). Extend the existing parse table.
- One end-to-end-through-the-seam case: given `--disallowed-tools 'A,B,C'`, the mocked writer receives `disallowed == [A,B,C]` in order (AC1 at the CLI boundary).
- `TestSplitAllowedTools` (:1245) already covers comma/space/mixed — reference it rather than duplicating tokeniser cases.

**Gate:** `make check` green — `go vet`, `staticcheck`, `go test -race`. (AC5.)

## Open questions

1. **`deny` JSON position** — spec prescribes between `allow` and `defaultMode` (natural allow/deny pairing). Either position is valid claude-side (parsed by key); whichever is chosen, the golden test pins it. No further decision needed unless the developer sees a reason to reorder.
2. **`splitAllowedTools` name** — reused for the deny flag despite the allow-specific name. Recommend **reuse as-is** (simplicity-first; renaming to a generic `splitToolList` ripples the call site + `TestSplitAllowedTools` for marginal gain). Rename only if the developer already has both files open and it's a clean one-shot.
3. **Usage prose** — recommend **not** amending `agentRunUsageDescription` (the flag self-documents via `PrintDefaults`; the constant is locked by the #359 test). Revisit only if an AC or reviewer explicitly wants the flag in the help body.
