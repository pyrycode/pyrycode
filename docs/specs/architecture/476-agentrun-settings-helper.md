# 476 — `internal/agentrun/settings/` helper (slimmed, per-spawn deny-default permissions JSON)

## Files to read first

- `internal/agentrun/trust/trust.go:1-46` — sibling subpackage pattern landed by #475. **Mirror this shape:** subpackage `package <name>` under `internal/agentrun/`, package doc-comment that opens with the package's one-line purpose and ends with the "MUST NOT log file contents" discipline note (tightened for this package's payload), free function returning `(result, error)`, error-prefix convention `agentrun/<subpkg>: <step>: %w`.
- `internal/agentrun/trust/trust_test.go:1-60` — test conventions to copy: `t.Parallel()` on every test; small inline `readJSON(t, path)` / file-byte helpers; stdlib `testing` + `encoding/json` only; no testify. The `t.TempDir()` / `os.TempDir()` distinction matters — see § Testing strategy.
- `internal/agentrun/ptyrunner/runner.go:77-79` and `runner.go:161-163` — `Config.SettingsPath` field (required, validated for non-emptiness at ptyrunner entry). This is the consumer signature this helper feeds; the helper's return value is the literal string the caller will assign to `cfg.SettingsPath`.
- `internal/agentrun/workdir.go:1-7` — parent-package doc comment establishes the `agentrun` package family's "MUST NOT log file contents" rule. The new `settings` subpackage doc comment mirrors this. (No re-use of code from `workdir.go` — this helper has no workdir parameter.)
- `docs/specs/architecture/339-agent-run-settings-file.md` § "Internal payload type" (lines 97-110) and § "Byte-for-byte JSON shape" (lines 111-130) — the JSON shape, field-order rule, and `nil`-vs-`[]string{}` consideration. **Read once; this spec re-pins only the deltas vs #339.** The #339 shape (`{"permissions":{"allow":[...],"defaultMode":"deny"}}` compact, no `SetIndent`) is unchanged.
- `docs/specs/architecture/475-agentrun-trust-helper.md` § "What changes vs #341" — the "slimmed re-introduction" template this spec mirrors for #339 → #476.
- `docs/PROJECT-MEMORY.md` § "Atomic-write recipe for on-disk registries" — convention statement. **This helper does NOT need the atomic rename**; the `os.CreateTemp` random suffix is the per-spawn uniqueness primitive and there is no pre-existing file to overwrite. Noted here so the developer does not mechanically import the recipe.

## Context

The 2026-05-19 pivot back to PTY drive (#329 tracking; ptyrunner in #471/#472; cutover in #470) re-introduces the per-spawn deny-default permission contract that #392 deleted alongside stream-json mode.

Phase A spike (2026-05-14) found that in interactive mode `claude --allowedTools <list>` is **additive** ("no-prompt" set), not exclusive — tools omitted from the list still run when the model asks for them. The empirical fallback that does enforce a deny-default whitelist is a per-spawn `--settings <path>` JSON file with shape `{"permissions": {"allow": [...], "defaultMode": "deny"}}`. Ptyrunner consumes this via `Config.SettingsPath`.

#339 originally implemented this helper as a sibling file inside `package agentrun` writing to a fixed `<workdir>/.pyry-agent-run-settings.json` with atomic-rename semantics. #392 deleted it. The slimmed re-introduction here changes four things:

| Aspect | #339 (deleted in #392) | #476 (this ticket) |
| --- | --- | --- |
| Package path | `internal/agentrun/settings.go` (sibling file) | `internal/agentrun/settings/settings.go` (subpackage) |
| Signature | `WriteSettings(workdir string, allowed []string) (string, error)` | `WriteSettings(allowedTools []string) (string, error)` |
| Tempfile location | `<workdir>/<fixed-name>` | `os.TempDir()` with random suffix |
| Empty input | Accepted; produced `"allow":[]` | Rejected with error before writing |
| Atomic rename | Required | DROPPED — random suffix is per-spawn uniqueness |
| File-lifecycle owner | Helper owns the path-overwrite semantics | Caller owns `defer os.Remove(path)`; helper only owns cleanup on the error path |
| Stdout marker line | Owned here | Not owned here — #470 owns the wiring + any operator-visible logging |
| `0o600` chmod | Explicit `os.Chmod` | DROPPED — `os.CreateTemp` default on Unix is 0o600 |

Everything else (JSON shape, compact encoding, field-order-load-bearing struct, `nil`→`[]string{}` is moot now that empty is an error, no `log/slog`) carries over from #339.

This ticket lands the package and tests only. No caller in-diff. #470 (cutover) wires `settings.WriteSettings` into `cmd/pyry/agent_run.go::runAgentRun` and pairs it with `defer os.Remove(path)`. #471/#472's ptyrunner consumes the path verbatim via `Config.SettingsPath`.

## Design

### Package boundary

New subpackage `internal/agentrun/settings`. Files:

- `internal/agentrun/settings/settings.go` — production
- `internal/agentrun/settings/settings_test.go` — tests

Imports (stdlib only): `encoding/json`, `errors`, `fmt`, `os`. No `path/filepath` (no path construction beyond what `os.CreateTemp` returns). No `log/slog`. No external deps.

No import of the parent `internal/agentrun` package (the helper has no workdir to resolve; the `agentrun` parent's `ResolveWorkdir` / `EncodeProjectDir` are not relevant here).

### Public API

```go
// Package settings writes per-spawn deny-default permission JSON files for
// interactive claude spawned via PTY drive. The file replicates the tool
// whitelist that `claude -p --allowedTools` enforced before the 2026-05-19
// pivot back to PTY drive (--allowedTools is additive in interactive mode;
// only --settings with defaultMode:"deny" replicates -p semantics — see
// Phase A spike, 2026-05-14).
//
// MUST NOT log the allowedTools slice, the JSON payload, or the returned
// path. Tool names are operational config rather than secret material, but
// the parent `agentrun` package family's no-content-logging discipline
// applies to every subpackage uniformly.
package settings

// WriteSettings generates a per-spawn deny-default permissions JSON file
// in os.TempDir() and returns the absolute path of the written file. The
// caller is responsible for cleanup via `defer os.Remove(path)`; the
// helper does not register cleanup itself on the success path.
//
// JSON shape (compact, no whitespace, trailing \n from json.Encoder.Encode):
//
//   {"permissions":{"allow":[<allowedTools>],"defaultMode":"deny"}}
//
// allowedTools is round-tripped verbatim — element order and any duplicates
// are preserved. The helper performs no deduplication, no sorting, and no
// canonicalisation.
//
// Validation: returns a non-nil error before any file is created when
// len(allowedTools) == 0. The caller has already validated non-emptiness at
// the CLI parse boundary (#470); this check is defence-in-depth.
//
// Tempfile naming: written via os.CreateTemp(os.TempDir(),
// "pyry-agent-run-settings-*.json"); the random infix is owned by the OS.
//
// Error path: if any operation after os.CreateTemp returns successfully
// fails (Encode, Close), the helper best-effort removes the tempfile
// before returning the error. Callers never see a leaked path on the
// error path — they only get a path on success.
func WriteSettings(allowedTools []string) (string, error)
```

### Internal payload type

Two unexported types, fields ordered so Go's struct-field-order serialization produces the canonical byte sequence:

```go
type settingsFile struct {
    Permissions permissions `json:"permissions"`
}

type permissions struct {
    Allow       []string `json:"allow"`
    DefaultMode string   `json:"defaultMode"`
}
```

Both unexported. `DefaultMode` is plain `string`, not a custom type — see #339's spec § "Internal payload type" (line 109) for the explicit warning against introducing a typed enum here.

### Implementation outline

`WriteSettings(allowedTools)`:

1. **Validate.** If `len(allowedTools) == 0`, return `nil, errors.New("agentrun/settings: allowedTools required")` immediately. No file is created.
2. **Create the tempfile.** `f, err := os.CreateTemp("", "pyry-agent-run-settings-*.json")` — the empty `dir` argument resolves to `os.TempDir()` per stdlib contract. Wrap a non-nil error with `"agentrun/settings: create temp: %w"`.
3. **Track the name for the error-path cleanup.** Capture `tmpName := f.Name()` immediately after the success branch of `CreateTemp`.
4. **Encode.** `enc := json.NewEncoder(f); if err := enc.Encode(&settingsFile{Permissions: permissions{Allow: allowedTools, DefaultMode: "deny"}}); err != nil { ... }`. **Do NOT call `enc.SetIndent`.** The trailing `\n` from `Encode` is part of the on-disk bytes and the test pins it.

   On error: best-effort cleanup — `_ = f.Close(); _ = os.Remove(tmpName)`. Return `fmt.Errorf("agentrun/settings: encode: %w", err)`. Note: Encode failure is effectively impossible for this payload (string slice + literal string), but the branch must be exercised by an injected-failure test (see § Testing).
5. **Close.** `if err := f.Close(); err != nil { _ = os.Remove(tmpName); return "", fmt.Errorf("agentrun/settings: close: %w", err) }`. Close-after-write failure on Linux/macOS surfaces buffered-write errors; we surface and clean up.
6. **Return.** `return tmpName, nil`. `os.CreateTemp` returns an absolute path on Unix (per stdlib contract — `TMPDIR` defaults to `/tmp`); no additional resolution required.

No `f.Sync()`. The helper is not implementing the atomic-write recipe — there is no rename, and the consuming `claude --settings` invocation happens within the same operator's process tree after the helper returns. Sync would be defensive against operator-process crash between write and claude-read, but the entire pyry agent-run lifetime is bounded by the same parent process; a crash that loses the buffered write also loses the claude spawn that would have consumed it.

No `os.Chmod`. `os.CreateTemp` creates files with mode 0o600 on Unix by default (the only supported platforms per `CLAUDE.md`). The file is in `os.TempDir()`, which is operator-owned; defense-in-depth is already satisfied by the OS default.

### Byte-for-byte JSON shape

Golden bytes for input `[]string{"Read", "Bash"}`:

```
{"permissions":{"allow":["Read","Bash"],"defaultMode":"deny"}}
```

plus the trailing `\n` from `json.Encoder.Encode` (total 64 bytes). The test (§ Testing) pins this byte sequence.

Golden bytes for input `[]string{"Bash"}` (single-tool AC scenario):

```
{"permissions":{"allow":["Bash"],"defaultMode":"deny"}}
```

(55 bytes plus trailing `\n`).

Order preservation: input `[]string{"Bash", "Read"}` produces `"allow":["Bash","Read"]` (not `["Read","Bash"]`). The helper does not sort.

### What this helper does NOT do

These are intentional non-features that the slim contract pushed out:

- **No workdir parameter.** Caller does not need to plumb `--workdir` here; the tempfile lives in `os.TempDir()`.
- **No fixed filename, no overwrite semantics.** Each invocation creates a new file with a fresh random suffix. No "previous spawn's file" exists to overwrite.
- **No marker-line printing.** #339 owned `settings-file: <path>` on stdout. #470 owns whatever operator-visible signal it needs.
- **No `defer os.Remove` registration.** Caller owns lifecycle. The helper's cleanup runs only on its own error path.
- **No `.gitignore` interaction.** `os.TempDir()` is not in any repo.

## Concurrency model

None within `settings.go`. `WriteSettings` is a single-threaded function with no goroutines, no channels, no locks. The random tempfile suffix guarantees concurrent invocations write to distinct paths.

## Error handling

Three failure surfaces:

1. **Empty input.** `len(allowedTools) == 0` returns `errors.New("agentrun/settings: allowedTools required")`. No file is created.
2. **`os.CreateTemp` failure** (TMPDIR missing, EACCES, ENOSPC). Wrap with `"agentrun/settings: create temp: %w"`. No cleanup needed — nothing was created.
3. **`json.Encoder.Encode` failure or `f.Close()` failure.** Best-effort cleanup: `_ = os.Remove(tmpName)`. Wrap with `"agentrun/settings: encode: %w"` or `"agentrun/settings: close: %w"`.

On every error path, the returned `path` is `""` — callers never see a leaked path. The doc comment makes this contract explicit.

No retry loop. The caller is `runAgentRun` (a CLI verb); the user retries by re-running.

## Testing strategy

`internal/agentrun/settings/settings_test.go` — table-driven where shape allows, stdlib `testing`, no testify.

All tests use `t.Parallel()` — the helper writes to `os.TempDir()` (not `t.TempDir()`), and the random suffix means parallel tests cannot collide. Each test that creates a file is responsible for `defer os.Remove(path)` after `WriteSettings` returns success (mirrors the production caller's lifecycle contract).

**Mandatory tests (one function per row, or grouped table where named):**

- **Empty slice → error before writing.** Inputs `nil` and `[]string{}` (separate subtests). Assert error is non-nil, the message contains `"agentrun/settings: allowedTools required"`, and the returned path is `""`. No tempfile leaked: count `pyry-agent-run-settings-*.json` files under `os.TempDir()` before and after the call; the delta is 0. (Use `filepath.Glob` for the count.)
- **Single tool → exact bytes.** Input `[]string{"Bash"}`. Read the returned path; assert on-disk bytes equal `[]byte("{\"permissions\":{\"allow\":[\"Bash\"],\"defaultMode\":\"deny\"}}\n")`. Defer-remove the path.
- **Multiple tools → caller-supplied order preserved, no dedup.** Input `[]string{"Bash", "Read", "Bash", "Edit"}`. Assert on-disk bytes contain `"allow":["Bash","Read","Bash","Edit"]` — duplicates and order are both verified. Defer-remove the path.
- **Round-trip parseability.** Input `[]string{"Read", "Bash"}`. Read the returned path; `json.Unmarshal` into a generic `map[string]any` (or into a mirrored test-local struct). Assert the unmarshalled `permissions.allow` equals the input slice and `permissions.defaultMode` equals `"deny"`. Defer-remove the path.
- **Path is in `os.TempDir()` with the documented prefix and suffix.** Input `[]string{"Read"}`. Assert `filepath.Dir(path) == os.TempDir()` (after `filepath.Clean` on both sides to normalise trailing slashes), `strings.HasPrefix(filepath.Base(path), "pyry-agent-run-settings-")`, and `strings.HasSuffix(path, ".json")`. Defer-remove the path.
- **Path absoluteness.** Same input as above. Assert `filepath.IsAbs(path)`. (Defensive — `os.CreateTemp` returns absolute paths on Unix, and pyrycode is Unix-only per `CLAUDE.md`, but the consumer ptyrunner's `cfg.SettingsPath` validation only checks non-emptiness so this test pins the absoluteness ptyrunner currently doesn't.)

**Optional test for the error-path cleanup branch (Encode failure):** Skippable. The Encode branch is effectively unreachable for the `settingsFile` shape (no `MarshalJSON` methods that can fail, no `chan` / `func` fields). The branch is exercised only by injecting a write-failing `io.Writer`, which would require refactoring `WriteSettings` to take an `io.Writer` parameter — out of proportion for the marginal coverage. Skip; the cleanup logic is one line and visually verifiable.

**Test helpers:** keep inline. The trust subpackage's `readJSON(t, path)` / `writeJSON(t, path, ...)` shape is too heavy for this package — each test reads at most one file, so a one-line `os.ReadFile` is clearer than a wrapper.

No e2e test is required. The cutover ticket (#470) owns the e2e smoke that verifies the resulting JSON is accepted by the current claude binary.

## Open questions

- **Schema validity against claude 2.1.143+ (#470 owns).** Phase A verified empirically on 2026-05-14; the schema may have drifted since. Re-validation is explicitly out of scope (issue body § Out of scope) and lands as e2e smoke in #470.
- **Tempfile leak if caller forgets `defer os.Remove(path)`.** The contract puts cleanup on the caller. #470's developer agent must wire the defer at the call site. Worst-case leak is a 60-byte JSON file in `/tmp` per agent-run invocation, ageing out per OS tmp-cleanup policy. Acceptable.
- **No `f.Sync()`.** Documented in § Implementation outline. Reviewer: confirm during code-review whether the dispatcher's expected crash-recovery story (claude spawn lost = whole agent-run lost) still holds; if pyry agent-run grows a resume-after-crash mode, revisit.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. The function has one input: the `allowedTools []string` slice from the dispatcher (operator-supplied via `--allowed-tools` on the parent `pyry agent-run` invocation, parsed and validated upstream by #470's CLI layer). No data crosses from network or untrusted process. The output (a tempfile path) is consumed only by the same operator's supervised `claude` instance via `--settings`.

- **[Tokens / secrets]** No findings. The settings JSON contains tool names — operational config, not credentials. `os.CreateTemp`'s default 0o600 is defense-in-depth, not because the file holds secrets.

- **[File operations]**
  - **Path traversal:** Filename is `os.CreateTemp(os.TempDir(), "pyry-agent-run-settings-*.json")`. Both the prefix and suffix are constant literals; the `*` is replaced by OS-generated random bytes. No caller input contributes to the filename. Tool-name contents go into the JSON payload, never into the path. Structurally impossible to traverse.
  - **TOCTOU:** No stat-then-open. `os.CreateTemp` is atomic-or-fail (`O_CREAT|O_EXCL` under the hood). Even if `os.TempDir()` is swapped between calls within a single helper invocation, the swap would only affect the next call, not the in-flight one.
  - **Permissions:** 0o600 by `os.CreateTemp` default on Unix (the only supported platform per `CLAUDE.md`). Defense-in-depth against world-readable `/tmp` on shared systems.
  - **Symlink handling:** Resolution is the OS's responsibility. `os.TempDir()` returns `$TMPDIR` (or `/tmp`); if an attacker has pre-planted a symlink replacing `$TMPDIR` in the operator's environment, they have already compromised the operator's session and pyry's settings file is not the load-bearing exposure.
  - **TMPDIR predictability:** `os.CreateTemp`'s random suffix (8 hex chars via `runtime.fastrand`) precludes filename-prediction attacks. Pre-planting `pyry-agent-run-settings-<exact-name>.json` is not feasible without colliding with the random infix; `O_EXCL` would fail the create in that unlikely case, surfacing as a `create temp:` error — fail-closed.
  - **Atomic writes:** N/A — no rename. The helper is the sole writer of its tempfile (no shared name, no contention). The randomly-named file is observable in two states: not-yet-present (before `CreateTemp` returns) and complete-at-close (after `f.Close()` returns). Partial states are only observable to the helper itself, which holds the only file descriptor.

- **[Subprocess / external command execution]** N/A — no subprocess in this ticket.

- **[Cryptographic primitives]** N/A — no crypto in this ticket. (`os.CreateTemp`'s random infix is `runtime.fastrand`, not crypto-grade, which is correct: this is filename uniqueness, not unguessability against a determined attacker.)

- **[Network & I/O]** N/A — no network.

- **[Error messages, logs, telemetry]** No findings. The helper does no logging. Error messages wrap underlying OS errors with the failing step name (`create temp`, `encode`, `close`) but do not include the `allowedTools` payload or the file path beyond what `os.CreateTemp` returns. The doc comment forbids logging the slice / payload / path at any consumer layer; #470's reviewer must enforce this when wiring the caller.

- **[Concurrency]** No findings. The random tempfile suffix makes concurrent invocations collision-free by construction. No goroutines, no locks, no shared state.

- **[Threat model alignment]** No findings.
  - **Schema drift between this helper's emitted JSON and the current claude binary** is the primary residual risk and is correctly externalised — covered by #470's e2e smoke (issue body § Out of scope) and operationally by the boot-time self-check in the broader agent-run pipeline.
  - **File tampering between emit and claude-read** is within the operator's trust boundary (same user, same process tree, 0o600 perms in operator-controlled `$TMPDIR`).
  - **Filename collision attack** is structurally impossible (see "TMPDIR predictability" above).
  - **Caller-leaked tempfile** is documented and accepted (§ Open questions); worst case is a 60-byte file in `/tmp` per invocation. Not a security issue.

**Reviewer:** architect (self-review per `agents/architect/security-review.md`)
**Date:** 2026-05-19
