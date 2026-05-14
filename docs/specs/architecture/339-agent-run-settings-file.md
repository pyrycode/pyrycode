# 339 — `pyry agent-run` per-spawn settings file (deny-default permission whitelist)

## Files to read first

- `cmd/pyry/agent_run.go:170-182` — the existing `runAgentRun` body. The new wiring slots in between `parseAgentRunArgs` returning success and the current confirmation `fmt.Println`; the marker line `settings-file: <path>` *replaces* (not augments) the scaffold's "no spawn yet" line so the dispatcher's scrape regex sees a stable single-line contract.
- `cmd/pyry/agent_run.go:38-51` — `splitAllowedTools` and the parsed-struct field names; the helper consumes `parsed.allowedTools` ([]string, non-empty by parse-time invariant) and `parsed.workdir` (existing absolute or relative directory path).
- `internal/devices/registry.go:63-107` — **canonical atomic-write recipe to copy.** `os.CreateTemp` in the same directory → `os.Chmod(tmp, 0o600)` → `json.Encoder.Encode` → `f.Sync()` → `f.Close()` → `os.Rename`, with `defer os.Remove(tmp)` covering the abort paths. Mirror the error-wrapping style; differ only in payload type.
- `internal/conversations/registry.go:75-115` — second instance of the same recipe; cross-reference if anything in `devices/registry.go` is unclear.
- `docs/PROJECT-MEMORY.md` § "Project-level conventions" → **Atomic-write recipe for on-disk registries.** The line-item this spec is enforcing.
- `internal/agentrun/trust.go` (on branch `origin/feature/341`, PR #343 open) — **sibling work in flight.** This file owns the `package agentrun` doc comment ("Package agentrun provides helpers used by…"). When writing `settings.go`, DO NOT add a duplicate package-level doc comment. See § "Package doc-comment coordination" below.

## Context

Phase A spike (#329) proved that `claude`'s interactive mode treats `--allowedTools` as additive, not exclusive — passing `--allowedTools "Read"` does NOT block `Bash`. The only mechanism that replicates the deny-default semantics natively available in `claude -p` mode is the `--settings <path>` JSON file with shape `{"permissions": {"allow": [...], "defaultMode": "deny"}}`. The dispatcher composes the deny-default permission contract by:

1. Emitting this file per-spawn (THIS ticket).
2. Passing `--settings <path>` to the supervised `claude` (sibling #332).
3. Self-checking at boot that the schema still enforces the boundary (sibling #336).

#339 is step 1 only. It writes the file, prints its resolved path on stdout behind a stable marker, and returns. No spawn, no `--settings` arg wiring, no schema self-check.

Adjacent in-flight work, useful to keep in head but NOT a dependency of #339:
- **#341 / PR #343** — introduces `internal/agentrun` with `trust.go` + `ResolveWorkdir` / `MarkWorkdirTrusted`. The package directory and `package agentrun` doc comment originate there. #339 lands a *second* file in the same package; the developer must coordinate the package comment (single rule below).
- **#332** — consumer wiring (`--settings <path>` arg + dispatcher scrape). Reads the marker line; field names and the path-printing format are this ticket's stable contract.
- **#336** — boot-time self-check that the schema still enforces deny-default against a live `claude` invocation. Independent of this file's writer.

## Design

### Package boundary

New file in the existing-but-still-in-flight `internal/agentrun` package (introduced by sibling #341 / PR #343):

```
internal/agentrun/
  trust.go           (#341 / PR #343 — already on branch)
  trust_test.go      (#341 / PR #343 — already on branch)
  settings.go        (THIS ticket)
  settings_test.go   (THIS ticket)
```

The two helpers are independent — `trust.go` mutates `~/.claude.json` under a file lock; `settings.go` writes a workdir-local file with no shared state. They cohabit the package because both are "primitives used by `pyry agent-run` to set up claude's environment before spawn." No new exported types are required by either to call the other.

### Package doc-comment coordination

`trust.go` (on branch `origin/feature/341`) already opens with:

```go
// Package agentrun provides helpers used by the `pyry agent-run` verb (wired
// in #338B) and the JSONL watcher (#333) to interoperate with claude's
// on-disk state.
//
// MUST NOT log file contents at any layer. ~/.claude.json may contain
// tokens or claude-internal state pyry does not own; the helper takes a
// pass-through view (preserve fields verbatim) and emits a key+verdict on
// success, not the underlying bytes.
package agentrun
```

`settings.go` MUST NOT include a `// Package agentrun ...` doc-comment block. It opens directly with `package agentrun` (no preceding doc comment). Rationale: Go permits multiple package-doc comments across files but it's idiomatic to keep exactly one; whichever ticket lands second adopts the bare-`package` form to avoid a duplicated paragraph at code-review.

If at code-review/merge time PR #343 has not yet landed and `settings.go` would be the first file in the package, the developer should request the resolution in PR comments — do NOT silently add a competing package doc comment in this ticket.

### Files touched

| File | Change |
| --- | --- |
| `internal/agentrun/settings.go` | NEW. Exports `WriteSettings(workdir string, allowed []string) (string, error)` and one unexported helper type for the JSON payload. ~50 production lines including doc comment. |
| `internal/agentrun/settings_test.go` | NEW. Table-driven tests for the byte-for-byte JSON shape, empty-slice round-trip, and overwrite-of-existing-file behaviour. |
| `cmd/pyry/agent_run.go` | EDIT. After successful `parseAgentRunArgs`, call `agentrun.WriteSettings(parsed.workdir, parsed.allowedTools)` and print `settings-file: <path>` on a line of its own. **Replace** (not augment) the existing scaffold confirmation `fmt.Println` so the dispatcher sees exactly one well-known line on stdout. |
| `cmd/pyry/agent_run_test.go` | EDIT. Append one test row exercising `runAgentRun` end-to-end: a valid argv whose `--workdir` points at `t.TempDir()`; capture stdout; assert that the settings file exists with the expected JSON content and that stdout contains exactly the marker line `settings-file: <abs-path>\n`. |

No edits to `cmd/pyry/main.go` — the verb dispatch already routes to `runAgentRun` from #337.

### Public API of `settings.go`

```go
// WriteSettings emits the per-spawn claude settings JSON inside workdir
// and returns the resolved absolute path.
//
// The shape is exactly {"permissions": {"allow": <allowed>, "defaultMode": "deny"}}.
// allowed is round-tripped verbatim — caller is responsible for non-emptiness
// (the pyry agent-run flag parser enforces this at the CLI boundary); a nil or
// empty slice is accepted and produces "allow": [] cleanly (NOT "allow": null).
//
// The file lives at <workdir>/.pyry-agent-run-settings.json. The dispatcher
// gitignores this filename in its repo; pyry does not modify any .gitignore.
//
// Written atomically via the project's standard recipe (os.CreateTemp in the
// same directory → encode → Sync → Close → Rename), at mode 0o600. Overwrite
// of a prior file is safe; the rename is the commit point.
func WriteSettings(workdir string, allowed []string) (string, error)
```

Signature deliberately mirrors the helper-package convention in `internal/agentrun/trust.go` (free function, returns `(result, error)`, no constructor / no state). Caller passes the workdir explicitly so test code is trivial — no `$HOME` or `os.Getwd()` plumbing.

### Internal payload type

```go
type settingsFile struct {
    Permissions permissions `json:"permissions"`
}

type permissions struct {
    Allow       []string `json:"allow"`
    DefaultMode string   `json:"defaultMode"`
}
```

Both types unexported. The fields are ordered to match the spec's `{"permissions": {"allow": [...], "defaultMode": "deny"}}` so Go's struct-field-order serialization produces the canonical byte sequence. `DefaultMode` is a plain `string`, not a typed enum — the literal `"deny"` is set inside `WriteSettings` and is not configurable by the caller. **Do not introduce a `DefaultMode` type.** The byte-for-byte AC requires `"deny"` (string) in the output; a custom type without an explicit `MarshalJSON` would serialise as the underlying integer.

### Byte-for-byte JSON shape

The atomic-write encoder is `json.NewEncoder(f)`. **Do NOT call `enc.SetIndent`** — the spec's golden bytes are the compact form (no whitespace), trailing newline appended by `json.Encoder.Encode`. The dispatcher reads the path off stdout and consumes the file via `claude --settings`, neither of which depends on indentation; the test, however, pins the exact byte sequence to guard against an accidental indentation change.

Golden bytes for input `[]string{"Read", "Bash"}`:

```
{"permissions":{"allow":["Read","Bash"],"defaultMode":"deny"}}
```

(plus the trailing `\n` from `json.Encoder.Encode` — total 64 bytes). Test asserts the on-disk content equals this byte-for-byte.

Golden bytes for input `nil` or `[]string{}` (the "empty slice cleanly" AC):

```
{"permissions":{"allow":[],"defaultMode":"deny"}}
```

To force the `[]` (not `null`) shape regardless of whether the caller passed `nil` or `[]string{}`, the helper normalises at the top: if `allowed == nil`, replace with `[]string{}`. One line — see "Implementation outline" below. Without this normalisation, `nil` serialises to `null` and the golden test fails.

### Implementation outline

`WriteSettings(workdir, allowed)`:

1. If `allowed == nil`, set `allowed = []string{}`. (Single line; gives the canonical `"allow": []` shape.)
2. Compute the target path: `path := filepath.Join(workdir, ".pyry-agent-run-settings.json")`. No call to `filepath.Abs` — the caller (the parsed `--workdir` flag) is the source of truth for whether the path is absolute. **If it's relative, `path` stays relative**, the rename succeeds in the caller's working directory, and the marker line prints the relative path. This is fine; `--workdir` validation in #337 only requires the directory exists, not that the path is absolute. Sibling #332 will document that the dispatcher passes an absolute `--workdir` (and #341's `MarkWorkdirTrusted` resolves through `EvalSymlinks` anyway).
3. Atomic write per the registry recipe:
   - `f, err := os.CreateTemp(workdir, ".pyry-agent-run-settings-*.tmp")`
   - `defer os.Remove(f.Name())` — fires on abort paths; harmless after a successful rename (the temp name no longer exists).
   - `os.Chmod(f.Name(), 0o600)` (settings JSON isn't a secret per se, but tight perms match the rest of the project's on-disk artefacts and reduce ambient blast radius if `--workdir` is mis-pointed).
   - `enc := json.NewEncoder(f); enc.Encode(&settingsFile{...})` — no `SetIndent`.
   - `f.Sync()`, `f.Close()`, `os.Rename(f.Name(), path)`.
   - Each error wrapped with the prefix `"agentrun: write settings:"` plus a step name, matching the wrapping in `devices/registry.go`.
4. Return `path, nil`.

### Wiring in `runAgentRun`

```go
func runAgentRun(args []string) error {
    parsed, err := parseAgentRunArgs(args)
    if err != nil {
        return err
    }
    path, err := agentrun.WriteSettings(parsed.workdir, parsed.allowedTools)
    if err != nil {
        return fmt.Errorf("agent-run: %w", err)
    }
    fmt.Printf("settings-file: %s\n", path)
    return nil
}
```

This deletes the scaffold's "flag set valid; scaffold-only ticket #337 — no spawn yet" `fmt.Println`. The dispatcher contract is now the marker line.

### Marker-line contract

The stdout format is the dispatcher's parse target — sibling #332 will scrape it with a `^settings-file: (.+)$` regex (or equivalent). Format is exactly:

```
settings-file: <path>
```

- Literal prefix `settings-file:` followed by a single space.
- One path, printed verbatim from `WriteSettings`'s return value (no quoting, no escaping — paths are workdir-controlled and won't contain control chars in practice; if they ever do, the regex needs revisiting in #332).
- Trailing `\n` from `fmt.Printf`.
- No other line printed to stdout from a successful `runAgentRun` after this ticket. **The pre-existing "flag set valid…" `fmt.Println` is removed.** (Re-confirming because this is the single most likely source of a dispatcher-side regression: if both lines are emitted, the scrape might match the scaffold line.)

Document the contract in the package doc-comment for `WriteSettings` (path policy) and inline next to the `fmt.Printf` in `runAgentRun` (format).

## Concurrency model

None within `settings.go`. `WriteSettings` is a single-threaded function with no goroutines, no channels, no locks. Each `pyry agent-run` invocation owns its own settings file at its own workdir, and concurrent invocations against the *same* workdir would race for the same path — but that race is inherent to the dispatcher's design (it would also race for `--settings` consumption from a stale file). Out of scope for #339.

No lock around the write. Unlike `~/.claude.json` (the `trust.go` target, where a sibling claude process can race with `pyry`), `.pyry-agent-run-settings.json` is written by `pyry agent-run` exclusively. The dispatcher is the only producer; the file is created fresh per invocation; no concurrent reader exists until the supervised `claude` is spawned (sibling #332, after the marker line). The atomic rename is sufficient.

## Error handling

Three failure surfaces:

1. **`os.CreateTemp` failure** (workdir disappeared between flag-parse and the helper, EACCES on the workdir, ENOSPC). Wrap with `"agentrun: write settings: create temp:"` plus `%w`. Caller in `runAgentRun` re-wraps with `"agent-run:"` so the user sees `pyry: agent-run: agentrun: write settings: create temp: ...`. One-liner, names the operation that failed.
2. **`json.Encoder.Encode` failure.** Effectively impossible for this payload shape (string slice + literal string), but propagate anyway via `"agentrun: write settings: encode:"` for symmetry with the registries.
3. **`os.Rename` failure** (cross-device, EACCES). Wrap with `"agentrun: write settings: rename:"`.

On any of these, `defer os.Remove(f.Name())` cleans up the temp; the existing settings file (if any) is left untouched (the rename is the commit point). No retry loop — the caller is a CLI, the user retries by re-running.

No exit-code differentiation. `runAgentRun` returning a non-nil error already maps to exit 1 via `cmd/pyry/main.go:157-160`.

## Testing strategy

`internal/agentrun/settings_test.go` — table-driven, stdlib `testing`, no testify (per `CODING-STYLE.md`):

- **byte-for-byte JSON shape** — input `[]string{"Read", "Bash"}`; assert on-disk bytes equal `[]byte("{\"permissions\":{\"allow\":[\"Read\",\"Bash\"],\"defaultMode\":\"deny\"}}\n")` (note the trailing `\n` from `json.Encoder.Encode`).
- **empty slice (`[]string{}`) round-trip** — input `[]string{}`; assert `"allow":[]` in output (NOT `"allow":null`).
- **nil slice round-trip** — input `nil`; assert same `"allow":[]` shape as the empty-slice row. (Pins the nil→[] normalisation.)
- **single-tool slice** — input `[]string{"Read"}`; assert `"allow":["Read"]`.
- **overwrite existing file** — create a pre-existing `.pyry-agent-run-settings.json` with a different `"allow"` list; call `WriteSettings` with new tools; assert the file content equals the new shape exactly and is exactly one file (no stray `.tmp` leftover from previous runs). Atomic-rename guarantees no half-written state.
- **return value** — assert returned `path` equals `filepath.Join(workdir, ".pyry-agent-run-settings.json")` for the workdir the test passed in.
- **file mode** — `os.Stat` the result; assert `info.Mode().Perm() == 0o600`.
- **workdir does not exist** — pass `filepath.Join(t.TempDir(), "nope")` as workdir; assert error is non-nil and contains the substring `agentrun: write settings:` (don't pin the OS-specific tail).

Test helper: a small `mkTempWorkdir(t)` wrapper returning `t.TempDir()` — kept inline since there's only one helper. Tests use real filesystem (no fakefs) because the recipe's correctness depends on real `os.CreateTemp` + `os.Rename` semantics.

`cmd/pyry/agent_run_test.go` — add one new test row (or test function), pattern: capture stdout via `os.Pipe()` redirection (the `pair_test.go` and `update_test.go` files demonstrate this idiom — pick the lighter-weight one). Drive `runAgentRun` with a valid argv where every required flag points at `t.TempDir()`-created scratch files; assert:

- `runAgentRun` returns `nil`.
- The settings file exists at `<workdir>/.pyry-agent-run-settings.json` with the expected JSON content.
- Captured stdout contains exactly `settings-file: <abs path of workdir>/.pyry-agent-run-settings.json\n` and nothing else.

No e2e (`internal/e2e/...`) test is required for this ticket; the unit-level `runAgentRun` test plus the package-level `WriteSettings` tests cover the AC. An e2e equivalent can land alongside #332 when the file is actually consumed.

## Open questions

- **`.gitignore` propagation.** AC explicitly defers; the helper's doc-comment notes the filename (`<workdir>/.pyry-agent-run-settings.json`) so the dispatcher's repo can gitignore it. This ticket does not touch any `.gitignore` in the pyrycode repo. If the agent-dispatcher repo's `.gitignore` needs updating, that's the dispatcher's PR, not this one.
- **Per-spawn uniqueness vs. fixed filename.** The filename is fixed (no PID, no nonce). Concurrent `pyry agent-run` invocations against the same workdir would clobber each other's settings — but the dispatcher is the only producer and serialises per-workdir already (one supervised `claude` per agent-run). If this changes, a `.pyry-agent-run-settings-<nonce>.json` variant is the obvious follow-up. Not in scope for #339.
- **Indented vs. compact JSON.** Chose compact (no `SetIndent`) so the byte-for-byte test pins something stable. If a future operator wants pretty-printed settings for human debugging, the change is one line — but the AC test would need updating. Recommendation: keep compact; the file is machine-consumed.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. The function has one input boundary (the parsed `--allowed-tools` slice + `--workdir` from `cmd/pyry/agent_run.go`'s flag set, both validated at parse time per #337). No data crosses from network or untrusted process; both inputs are operator-supplied via the dispatcher's argv. The output (a workdir-local JSON file) is consumed only by the same operator's supervised `claude` instance.
- **[Tokens / secrets]** No findings — the settings JSON contains no tokens, no credentials, no keys. The allowlist names are operational config, not secret material. `0o600` mode is applied as defence-in-depth (the workdir may be world-readable; the file's contents are not), not because the file holds secrets.
- **[File operations]** No findings.
  - **Path traversal:** The filename is a constant literal `".pyry-agent-run-settings.json"` joined to caller-supplied `workdir`. No user input contributes to the filename, so traversal via `../` in a tools entry is structurally impossible. The `workdir` itself is the caller's responsibility; `--workdir` validation in #337 requires it to be an existing directory but does not constrain its location — that's by design for a user-process CLI (the user can always write anywhere they have permission, with or without pyry).
  - **TOCTOU:** `--workdir` flag-parse does `os.Stat` then this helper does `os.CreateTemp(workdir, …)`. If the workdir is swapped between those calls (e.g. dir replaced with a symlink to `/etc`), `CreateTemp` would try to write into the swap target. **Acceptable for this design** because (a) the operator owns both the flag value and the filesystem state, (b) any swap requires the operator's own privileges or a compromise outside the trust boundary, (c) the worst outcome is writing a 64-byte JSON file readable only by the operator (`0o600`) into a directory they targeted. No privilege escalation, no information disclosure beyond what the operator can already cause directly.
  - **Permissions:** `0o600` explicitly set via `os.Chmod(tmp, 0o600)` between `CreateTemp` and `Encode`, matching `devices/registry.go:86`. The workdir's mode is not modified.
  - **Symlink handling:** None — the helper writes through whatever path `--workdir` resolves to. If the operator wants symlink resolution, they pre-resolve and pass an absolute path. This matches `internal/sessions` and `internal/devices`, which similarly delegate path canonicalisation to the caller.
  - **Atomic writes:** Yes, per the project's standard recipe; the rename is the commit point; no partial file is observable.
- **[Subprocess / external command execution]** N/A — no subprocess in this ticket.
- **[Cryptographic primitives]** N/A — no crypto in this ticket.
- **[Network & I/O]** N/A — no network in this ticket.
- **[Error messages, logs, telemetry]** No findings. The stdout marker line `settings-file: <path>` is the dispatcher's parse contract; the path it prints is the same one the dispatcher just supplied via `--workdir` (no new information disclosed). Error messages from `WriteSettings` wrap underlying OS errors and name the failing step (`create temp`, `encode`, `rename`), but contain no token / payload / allowlist content. The allowlist itself is non-sensitive operational config; even if it leaked into a log it would not be a security finding.
- **[Concurrency]** SHOULD FIX (deferred to dispatcher) — concurrent `pyry agent-run` invocations against the same `--workdir` race for the same filename. The atomic rename ensures no half-written file is observable, but a later invocation can overwrite an earlier invocation's settings *before* claude reads them. This is the dispatcher's responsibility (it owns serialisation per-workdir), not the helper's. Noted in § "Open questions" so #332's consumer-wiring reviewer can confirm.
- **[Threat model alignment]** No findings. This file is the load-bearing mechanism for the deny-default permission boundary verified empirically in #329. The threat model is:
  - **Boot-time schema drift** — covered by sibling #336 (boot-time self-check). Out of scope here.
  - **File tampering between emit and claude-read** — within the dispatcher trust boundary (same user, same workdir, same process tree). Out of scope.
  - **Filename collision attack** — an attacker writing `.pyry-agent-run-settings.json` into the workdir before `pyry agent-run` runs is overwritten by the atomic rename. No bypass possible; the file pyry writes is the file claude reads.

**Reviewer:** architect (self-review per `agents/architect/security-review.md`)
**Date:** 2026-05-14
