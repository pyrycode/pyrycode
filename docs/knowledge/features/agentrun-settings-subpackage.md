# `internal/agentrun/settings` — per-spawn deny-default permissions JSON

Writes a `{"permissions":{"allow":[...],"defaultMode":"dontAsk"}}` JSON file to `os.TempDir()` and returns the path. The caller (#470's `runAgentRun`) hands the path to [`ptyrunner.Config.SettingsPath`](ptyrunner-package.md) so PTY-driven interactive `claude` enforces a deny-default tool whitelist — the same semantics `claude -p --allowedTools` had natively in stream-json mode.

Introduced [#476](https://github.com/pyrycode/pyrycode/issues/476) as a **slimmed resurrection** of the helper [#339](https://github.com/pyrycode/pyrycode/issues/339) shipped and [#392](https://github.com/pyrycode/pyrycode/issues/392) deleted. The 2026-05-19 pivot back to PTY drive ([`codebase/471.md`](../codebase/471.md)) made the settings file load-bearing again because Phase A (2026-05-14) found `claude --allowedTools <list>` is **additive** in interactive mode — tools omitted from the flag still run when the model asks for them. Only the `--settings <path>` JSON with `defaultMode: "dontAsk"` replicates the `-p` enforcement contract. The original Phase A spike picked `"deny"` by guessing the literal; claude 2.1.145 rejected that value at startup and silently fell back to `"default"` mode — fixed in [#487](https://github.com/pyrycode/pyrycode/issues/487) by switching to the Anthropic-documented value (see [`agent-sdk/permissions` docs](https://code.claude.com/docs/en/agent-sdk/permissions): *"Don't ask mode (`dontAsk`) converts any permission prompt into a denial"*).

## Public API

```go
// WriteSettings generates a per-spawn deny-default permissions JSON file
// in os.TempDir() and returns the absolute path of the written file. The
// caller is responsible for cleanup via `defer os.Remove(path)`; the
// helper does not register cleanup itself on the success path.
func WriteSettings(allowedTools []string) (string, error)
```

No exported types, no constructor, one function. `allowedTools` is round-tripped verbatim — element order and duplicates preserved, no sorting, no dedup, no canonicalisation.

## JSON shape

Compact, no whitespace, single trailing `\n` from `json.Encoder.Encode`. For `[]string{"Read", "Bash"}` the on-disk bytes are exactly:

```
{"permissions":{"allow":["Read","Bash"],"defaultMode":"dontAsk"}}
```

(plus a trailing `\n`, 67 bytes total).

Two unexported types make the field order load-bearing — Go's struct serialisation produces the canonical sequence without `SetIndent`:

```go
type settingsFile struct {
    Permissions permissions `json:"permissions"`
}

type permissions struct {
    Allow       []string `json:"allow"`
    DefaultMode string   `json:"defaultMode"`
}
```

`DefaultMode` is plain `string`, not a typed enum — pinned by the spec to avoid a layer of indirection that adds nothing.

## Why a subpackage instead of `internal/agentrun/settings.go`

#339 lived as a sibling file under `internal/agentrun/`. The subpackage layout (`internal/agentrun/settings/`) mirrors the sibling primitives [`internal/agentrun/trust/`](agentrun-trust-subpackage.md), [`internal/agentrun/ptyrunner/`](ptyrunner-package.md), [`internal/agentrun/streamrunner/`](streamrunner-package.md), and [`internal/agentrun/jsonl/`](jsonl-reader.md). The parent `internal/agentrun` package hosts only workdir helpers (`ResolveWorkdir`, `EncodeProjectDir`); spawn / trust / settings concerns are package-scoped.

## What changed from #339

The slim resurrection deliberately drops everything the new system makes redundant. Net delta vs the pre-#392 helper:

| Aspect | #339 (deleted in #392) | #476 (this resurrection) |
| --- | --- | --- |
| Package path | `internal/agentrun/settings.go` (sibling file) | `internal/agentrun/settings/settings.go` (subpackage) |
| Signature | `WriteSettings(workdir, allowed) (string, error)` | `WriteSettings(allowedTools) (string, error)` — no workdir |
| Tempfile location | `<workdir>/.pyry-agent-run-settings.json` (fixed name) | `os.TempDir()` with `os.CreateTemp`'s random suffix |
| Empty input | Accepted; produced `"allow":[]` | Rejected with `agentrun/settings: allowedTools required` before any file is created |
| Atomic rename | `os.CreateTemp` → `Chmod` → encode → `Sync` → `Close` → `Rename` | DROPPED — random suffix is per-spawn uniqueness, no pre-existing file to overwrite |
| Lifecycle owner | Helper owned the path-overwrite semantics | Caller owns `defer os.Remove(path)`; helper cleans up only on its own error path |
| Stdout marker line | `settings-file: <path>` printed by the helper's caller | Not owned here — #470 owns any operator-visible signal |
| `0o600` chmod | Explicit `os.Chmod` | DROPPED — `os.CreateTemp`'s Unix default is already `0o600` |
| Nil-to-empty normalisation | Required (`nil → []string{}` at entry) | Not applicable — empty input is now an error |

Everything else (canonical JSON shape, field-order-load-bearing struct, compact encoding, stdlib only, no `log/slog`) carries over.

## No atomic-rename — why the random suffix is enough

`internal/devices/registry.go:Save` is the canonical pyrycode atomic-write recipe (mirrored by [`internal/agentrun/trust`](agentrun-trust-subpackage.md) for `~/.claude.json`). This helper deliberately does not follow it because the crash-safety axis the recipe defends is not present:

- **No pre-existing file to overwrite.** Every invocation gets a fresh random tempfile (`os.CreateTemp` uses `O_CREAT|O_EXCL` under the hood). Concurrent invocations write to distinct paths by construction.
- **No second reader before the writer finishes.** The file is observable in two states only — not-yet-present (before `CreateTemp` returns) and complete-at-close (after `f.Close()` returns). The helper holds the only file descriptor during the intermediate writes; nobody else can stat it under a name they could discover.
- **No `f.Sync()`.** The consuming `claude --settings` invocation happens within the same operator's process tree after the helper returns. A crash between write and claude-read also loses the claude spawn that would have consumed the file; no orphan reader is possible.

This is the *Evidence-Based Fix Selection* principle in action: don't ship a defense for a failure mode that the surrounding system structurally prevents.

## Error path — no leaked paths

The contract: callers get `(path, nil)` on success, `("", err)` on failure. Three failure surfaces:

1. **Empty `allowedTools`** — returns `errors.New("agentrun/settings: allowedTools required")` before any file is created.
2. **`os.CreateTemp` failure** (TMPDIR missing, EACCES, ENOSPC) — wrapped as `agentrun/settings: create temp: %w`. Nothing to clean up.
3. **`json.Encoder.Encode` or `f.Close()` failure** — best-effort `_ = f.Close(); _ = os.Remove(tmpName)` then returns `agentrun/settings: encode: %w` or `agentrun/settings: close: %w`. Callers never see a leaked path.

The empty-input check is defence-in-depth: the CLI parse boundary (#470) already validates non-emptiness, but having both layers means the helper is safe to call from a hypothetical future caller that hasn't done the validation.

`json.Encoder.Encode` failure is effectively unreachable for this payload — string slice + string literal, no `MarshalJSON` that can return an error, no `chan` / `func` fields. The cleanup branch exists for symmetry with `Close` and is visually verifiable. The test matrix does not exercise it (would require refactoring `WriteSettings` to take an `io.Writer` parameter for fault injection — out of proportion for the marginal coverage).

## File mode

`os.CreateTemp` creates files at mode `0o600` on Unix by default (the only platforms pyrycode supports per [CLAUDE.md](../../../CLAUDE.md)). No explicit `os.Chmod` — the default already gives defence-in-depth on shared `/tmp` and the file is in operator-controlled `$TMPDIR`. The mode is informational, not a security boundary; tool names are operational config, not credentials.

## Lifecycle — caller owns cleanup

The helper does not register cleanup on the success path. The cutover caller (#470) wires:

```go
settingsPath, err := settings.WriteSettings(parsed.allowedTools)
if err != nil { /* surface and exit 1 */ }
defer os.Remove(settingsPath)
// ... ptyrunner.Run(ctx, ptyrunner.Config{SettingsPath: settingsPath, ...})
```

A caller that forgets the `defer` leaks a ~60-byte JSON file in `/tmp` per `pyry agent-run` invocation, aging out per OS tmp-cleanup policy. Documented in § Open questions; #470's developer agent is responsible for the defer at the call site.

## Logging discipline

The package doc-comment is load-bearing:

```
MUST NOT log the allowedTools slice, the JSON payload, or the returned
path. Tool names are operational config rather than secret material, but
the parent agentrun package family's no-content-logging discipline
applies to every subpackage uniformly.
```

- No `slog` calls inside the helper.
- Error wraps name the step (`create temp`, `encode`, `close`) and the path only — never the slice contents.
- Operator-visible diagnostics happen at the consumer (#470).

## Concurrency model

No goroutines spawned. Purely sequential within an invocation: validate → create temp → encode → close → return path. The random tempfile suffix makes concurrent invocations collision-free by construction (`O_CREAT|O_EXCL` would fail the create on the astronomically-unlikely collision; surfaces as `agentrun/settings: create temp: %w`).

No `context.Context` parameter — the operation is fast-bounded (local filesystem create + write of ~60 bytes). If a future caller needs cancellable-acquire, add a context-taking sibling without changing this signature.

## Dependency direction

- Stdlib: `encoding/json`, `errors`, `fmt`, `os`. No `path/filepath` (no path construction beyond what `os.CreateTemp` returns).
- Internal: none — does **not** import the parent `internal/agentrun` (no workdir to resolve here; `ResolveWorkdir` / `EncodeProjectDir` are not relevant).
- External: none. No `log/slog`.

## Testing

`internal/agentrun/settings/settings_test.go` — same-package, stdlib `testing` + `encoding/json` only, no testify. Six test functions, all `t.Parallel()`. The helper writes to `os.TempDir()` (not `t.TempDir()`), so each successful-path test owns its `defer os.Remove(path)`. The random tempfile suffix means parallel tests cannot collide.

Test cases:

- `TestWriteSettings_EmptyInputReturnsErrorAndDoesNotWrite` — sub-tests `nil` and `[]string{}`. Snapshots `filepath.Glob(os.TempDir() + "/pyry-agent-run-settings-*.json")` before the call into a set, then asserts no path that wasn't in the before-set appears after — the only safe way to detect a tempfile leak when parallel sibling tests create and clean up their own files in the same directory. Pins the error message substring and `path == ""`.
- `TestWriteSettings_SingleToolGoldenBytes` — input `[]string{"Bash"}`; reads the file and asserts exact bytes `{"permissions":{"allow":["Bash"],"defaultMode":"dontAsk"}}\n`.
- `TestWriteSettings_PreservesOrderAndDuplicates` — input `[]string{"Bash", "Read", "Bash", "Edit"}` (deliberately not sorted, with a duplicate); asserts exact bytes contain `"allow":["Bash","Read","Bash","Edit"]`. Pins both the no-sort and no-dedup contracts.
- `TestWriteSettings_RoundTripParseable` — input `[]string{"Read", "Bash"}`; reads the file and `json.Unmarshal`s into a local mirror struct; asserts `allow` slice equals the input and `defaultMode == "dontAsk"`.
- `TestWriteSettings_PathLocationPrefixSuffix` — input `[]string{"Read"}`; asserts `filepath.Dir(path) == os.TempDir()` (after `filepath.Clean` on both sides for trailing-slash normalisation), `strings.HasPrefix(filepath.Base(path), "pyry-agent-run-settings-")`, and `strings.HasSuffix(path, ".json")`.
- `TestWriteSettings_PathIsAbsolute` — same input; asserts `filepath.IsAbs(path)`. Defensive — `os.CreateTemp` returns absolute paths on Unix by stdlib contract, but ptyrunner's `Config.SettingsPath` only validates non-emptiness, so this test pins the absoluteness the consumer doesn't.

No e2e test. The cutover ticket (#470) owns the e2e smoke that verifies the resulting JSON is accepted by the current claude binary; schema re-validation against the live claude version is explicitly its responsibility (issue body § Out of scope).

## Consumers

- `pyry agent-run` (cutover in #470) — calls `settings.WriteSettings(parsed.allowedTools)` after flag validation, defers `os.Remove(path)`, and passes the path to `ptyrunner.Config.SettingsPath`. Failure here exits the verb with 1 (the helper surfaces the failure; the caller chooses to abort).
- [`ptyrunner.Run`](ptyrunner-package.md) consumes the path verbatim via `Config.SettingsPath` (required, validated non-empty at ptyrunner entry); the path becomes claude's `--settings <path>` argv.

No other consumers. The helper is single-purpose and has no in-process API surface beyond the one function.

## What this helper deliberately does NOT do

- **No workdir parameter.** Tempfile lives in `os.TempDir()`, not `<workdir>/.pyry-agent-run-settings.json` — eliminates the `.gitignore` propagation problem and the fixed-name overwrite contention #339 had.
- **No marker-line printing.** #339 owned `settings-file: <path>` on stdout for dispatcher scraping. #470 owns whatever operator-visible signal it needs; this helper writes nothing to stdout.
- **No `defer os.Remove` registration.** Caller owns lifecycle.
- **No `.gitignore` interaction.** `os.TempDir()` is not in any repo.
- **No retries.** Caller decides.
- **No logging.** See § Logging discipline.

## Out of scope

- The ptyrunner that consumes the path → [`ptyrunner-package.md`](ptyrunner-package.md) (#471 / #472).
- The `cmd/pyry/agent_run.go` wiring + `defer os.Remove(path)` → #470.
- Re-validation of the JSON schema against the current claude binary → e2e smoke in #470.

## Related

- [agentrun-package.md](agentrun-package.md) — surrounding parent package; the post-#392 surface and the table of sibling subpackages.
- [agentrun-trust-subpackage.md](agentrun-trust-subpackage.md) — sibling slim-resurrection landed in #475; same template, complementary concern (workspace-trust pre-write).
- [ptyrunner-package.md](ptyrunner-package.md) — the spawn primitive that consumes the path via `Config.SettingsPath`.
- [`codebase/476.md`](../codebase/476.md) — build notes (file inventory, patterns, lessons).
- [`codebase/487.md`](../codebase/487.md) — `"deny"` → `"dontAsk"` literal fix; the original Phase A spike's guessed value was rejected by claude 2.1.145 and silently fell back to `"default"` mode, reopening `/doctor` poisoning on the ptyrunner path.
- [`docs/specs/architecture/476-agentrun-settings-helper.md`](../../specs/architecture/476-agentrun-settings-helper.md) — architect spec.
- [`codebase/339.md`](../codebase/339.md) — the original (pre-deletion) helper; this slimmed version's contract is a strict subset of the file-writing behaviour and a deliberate descope of the workdir / overwrite / marker-line semantics.
- [`codebase/392.md`](../codebase/392.md) — the deletion this ticket reverses.
- [`codebase/475.md`](../codebase/475.md) — sibling resurrection (trust pre-write); shares the slim-resurrection template.
