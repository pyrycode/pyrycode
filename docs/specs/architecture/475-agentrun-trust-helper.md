# 475 — `internal/agentrun/trust/` helper (slimmed)

## Files to read first

- `internal/agentrun/workdir.go:21-31` — existing `agentrun.ResolveWorkdir(workdir string) (string, error)`. The trust subpackage MUST import and call this; do NOT re-implement `Abs` + `EvalSymlinks` inline. This is the single source of truth for "claude's realpath rule" in pyrycode and the AC's "Resolves `workdir` via `filepath.EvalSymlinks`" is satisfied by delegating here.
- `internal/agentrun/workdir.go:1-7` — package doc-comment establishes the "MUST NOT log file contents" convention for the `agentrun` package family. The new `trust` subpackage doc-comment mirrors and tightens this for `~/.claude.json` contents.
- `internal/devices/registry.go:55-107` — canonical pyrycode atomic-write recipe (`os.CreateTemp` in dir → `os.Chmod` → encode → `f.Sync()` → `f.Close()` → `os.Rename`). Mirror this shape line-for-line; per `docs/PROJECT-MEMORY.md` § "Atomic-write recipe for on-disk registries" the convention is duplication-not-extraction until a fifth registry forces it.
- `docs/specs/architecture/341-agentrun-trust-helper.md` — the original (closed) spec being slimmed. § "ResolveWorkdir — body shape", § "Write step", § "File mode", § "Numeric precision (preservation)", § "Idempotency", § "Logging discipline", and the test list are all still in force; the slimming removes the lock + the `homeDir` parameter only (see "What changes vs #341" below). Do not re-derive the design that #341 already pinned — read #341 first, then read this delta.
- `internal/sessions/rotation/watcher.go:108-115` — example of pyrycode's "EvalSymlinks before comparing against claude's path key" idiom in another consumer. Read for context only; the trust helper does not call this code path.
- `docs/PROJECT-MEMORY.md` § "Atomic-write recipe for on-disk registries" — convention statement; spec just points at it.

## Context

The 2026-05-19 pivot back to PTY drive (#329 tracking; ptyrunner in #471/#472; cutover in #470) re-introduces the workspace-trust modal problem: interactive `claude` shows a modal the first time a workdir is opened, and the dispatcher's automated flow has no human to dismiss it. Pre-writing `projects[<realpath(workdir)>].hasTrustDialogAccepted = true` in `~/.claude.json` side-steps the modal entirely.

The original helper landed in #341 and was deleted in #392 when stream-json subprocess mode replaced PTY drive (no modal in pipe mode). The pivot back to PTY drive needs the helper resurrected, but **slimmed**: tui-driver's `HasTrustModal(snap)` provides a runtime safety net, so the helper only needs best-effort pre-write, not the atomic-or-die-under-flock behaviour the original required.

This ticket lands the package and tests only. No caller in-diff. #470 (cutover) wires `MarkWorkdirTrusted` into `cmd/pyry/agent_run.go::runAgentRun` immediately after this lands.

## What changes vs #341

The contract is a strict subset of #341's. Diff is:

| Aspect | #341 (deleted in #392) | #475 (this ticket) |
| --- | --- | --- |
| Signature | `MarkWorkdirTrusted(homeDir, workdir string) error` | `MarkWorkdirTrusted(workdir string) (realpath string, err error)` |
| Home directory | Explicit parameter | `os.UserHomeDir()` inside the helper |
| flock on sibling `.lock` file | Required across read-modify-write | DROPPED |
| Cross-process serialization | Required (concurrent `pyry agent-run` + foreground claude) | Not required (best-effort; tui-driver dismisses modal if race occurs) |
| Package path | `internal/agentrun/trust.go` (sibling file) | `internal/agentrun/trust/trust.go` (subpackage) |
| Return value | `error` only | `(realpath, error)` — caller (#470) uses realpath as `ptyrunner.Config.WorkDir` |
| Atomic tempfile + rename | Required | Required (unchanged) |
| `UseNumber` + `map[string]any` preservation | Required | Required (unchanged) |
| Refuse non-object types under `projects` or `projects[key]` | Required | Required (unchanged) |
| File-mode preservation | Required | Required (unchanged) |
| No file-content logging | Required | Required (unchanged) |

Everything not in the table is unchanged from #341. Read #341 first — this spec only documents the delta.

## Design

### Package boundary

New subpackage `internal/agentrun/trust`. Files:

- `internal/agentrun/trust/trust.go` — production
- `internal/agentrun/trust/trust_test.go` — tests (same package; white-box if needed but in practice public API is enough)

Imports (stdlib only): `encoding/json`, `errors`, `fmt`, `io/fs`, `os`, `path/filepath`, plus `github.com/pyrycode/pyrycode/internal/agentrun` for `ResolveWorkdir`.

No `log/slog`. The eventual caller (#470's `runAgentRun`) logs success at the verb level.

### Public API

```go
// Package trust pre-marks a workdir as trusted in ~/.claude.json so
// interactive claude (spawned via PTY drive) skips the workspace-trust modal.
//
// Best-effort: no file lock. A concurrent writer may produce a lost update;
// tui-driver's HasTrustModal(snap) provides the runtime safety net that
// dismisses the modal if pre-marking lost the race. The helper is still
// atomic on the *single*-writer axis (tempfile + rename) so a crashed pyry
// mid-write does not leave ~/.claude.json in a broken state for the user's
// own interactive claude sessions.
//
// MUST NOT log file contents at any layer. ~/.claude.json may contain
// tokens or claude-internal state pyry does not own; the helper takes a
// pass-through view (preserve fields verbatim) and emits nothing to logs.

// MarkWorkdirTrusted ensures
//   ~/.claude.json :: projects[<realpath(workdir)>].hasTrustDialogAccepted = true
// Idempotent. Atomic — writes to a tempfile in the same directory then
// renames over the target. Returns the resolved realpath on success.
//
// On absent ~/.claude.json the helper creates it with mode 0o600 and a
// minimal skeleton: {"projects": {<realpath>: {"hasTrustDialogAccepted": true}}}.
// On existing ~/.claude.json the helper preserves all other top-level
// fields, the `projects` map's sibling entries, and any extra keys on the
// target entry verbatim — including numeric precision (no float64 round-trip
// of int64-sized timestamps).
func MarkWorkdirTrusted(workdir string) (realpath string, err error)
```

No exported types. No constructor. One function. Internal helper (unexported) documented below for testability.

### Internal seam — explicit homeDir for tests

Public `MarkWorkdirTrusted` calls `os.UserHomeDir()` then delegates to an unexported sibling that takes `homeDir` explicitly:

```go
func markWorkdirTrustedIn(homeDir, workdir string) (realpath string, err error)
```

**Why the seam:** #341's spec called this out — tests using `t.Setenv("HOME", ...)` to redirect `os.UserHomeDir` cannot call `t.Parallel()` (Go's testing docs: `t.Setenv` is process-global and forbids parallel ancestors). With explicit `homeDir`, tests pass `t.TempDir()` directly and run in parallel.

The AC's public-API contract (`MarkWorkdirTrusted(workdir string)`) is satisfied; `markWorkdirTrustedIn` is unexported and exists for parallel-test ergonomics. The public wrapper is two lines:

```go
home, err := os.UserHomeDir()
if err != nil { return "", fmt.Errorf("agentrun/trust: home dir: %w", err) }
return markWorkdirTrustedIn(home, workdir)
```

### `markWorkdirTrustedIn` — body shape

Per #341's § "MarkWorkdirTrusted — body shape", with **two deletions**:

1. Resolve key: `realpath, err := agentrun.ResolveWorkdir(workdir)`. Return on error. (Wrap with `agentrun/trust:` prefix instead of #341's `agentrun:`.)
2. Compute path: `dataPath := filepath.Join(homeDir, ".claude.json")`. **(#341 step 2 also computed `lockPath`; DROPPED.)**
3. **(#341 step 3 acquired flock; DROPPED.)**
4. Stat `dataPath` to capture existing mode (if present). On `fs.ErrNotExist`, the create-mode default is `0o600`. On other stat error, return wrapped.
5. Read `dataPath`. On `fs.ErrNotExist`, start with an empty root `map[string]any{}`. On other read error, return wrapped.
6. If the read returned bytes: decode with `json.Decoder` having `UseNumber()` set, into `map[string]any`. Empty file → empty map. Malformed JSON → return wrapped error.
7. Extract or create the `projects` sub-map. If `root["projects"]` exists but is not a `map[string]any`, return wrapped error (refuse to overwrite an unknown schema).
8. Extract or create the entry for `realpath`. Same type-assertion discipline; refuse to overwrite a non-object entry.
9. Set `entry["hasTrustDialogAccepted"] = true`. Re-assign back into `projects[realpath]` (and `projects` back into `root` — necessary if either map was newly created).
10. Atomic write per § "Write step" below.
11. Return `realpath, nil`.

Total: ~70 lines of body.

### Write step

Per #341 § "Write step", with one minor change: the temp file lives in `homeDir` (the rename target's directory), filename pattern `.claude.json.tmp-*`.

Sketch (sequence, not literal code; mirror `internal/devices/registry.go:80-105`):

- `os.CreateTemp(homeDir, ".claude.json.tmp-*")` → `f`.
- `defer os.Remove(f.Name())` (best-effort cleanup if rename never happens).
- `os.Chmod(f.Name(), mode)` where `mode` is the captured existing mode or `0o600` on create.
- `enc := json.NewEncoder(f); enc.SetIndent("", "  ")`; `enc.Encode(root)`.
- `f.Sync()`, `f.Close()`.
- `os.Rename(f.Name(), dataPath)`.

Each step wraps errors with `fmt.Errorf("agentrun/trust: <step>: %w", err)`. Wraps name the step (`encode`, `fsync`, `rename`, `create temp`, `chmod`) and the path; they MUST NOT echo file bytes or unmarshalled values into the chain.

### File mode

Identical to #341 § "File mode":

- If `~/.claude.json` already exists, copy its mode to the temp file before rename so we don't quietly tighten or loosen permissions on the user's file.
- If it doesn't exist, create at `0o600`.
- A single `os.Stat(dataPath)` before the read is enough; no double-stat dance (this is single-user data, the mode is informational, not a security boundary).

### Numeric precision (preservation)

Identical to #341 § "Numeric precision". `json.Decoder.UseNumber()` is **mandatory**. Without it, `~/.claude.json`'s int64-sized values (timestamps, counters claude writes) silently corrupt on round-trip, violating the AC's "Preserves all existing top-level + nested fields verbatim". One dedicated test pins it.

### Idempotency

Identical to #341 § "Idempotency". Map-key sort + `UseNumber` preservation → byte-identical output for the same logical content on a repeat call. The "file exists, this workdir already trusted → idempotent (no error)" AC row drives the test.

### Concurrency model

No goroutines spawned. The helper is purely sequential within a single invocation: read → mutate → write.

**Concurrent invocations are explicitly NOT serialized** by this helper. This is the slimming. Rationale:

1. The ptyrunner spawns claude once per `pyry agent-run` invocation; concurrent `pyry agent-run` against the *same* workdir is rare in practice (the dispatcher runs at most one agent run per worktree at a time).
2. Concurrent `pyry agent-run` against *different* workdirs hits different `projects[realpath]` keys; the worst case is a lost update where one writer's added `projects` entry overwrites the other's — both losers will have the modal appear and tui-driver's `HasTrustModal(snap)` dismisses it. No correctness regression, just a brief PTY-driven dismissal.
3. A simultaneous foreground `claude` writing `~/.claude.json` can clobber pyry's write (or vice versa). Same outcome: tui-driver's safety net catches the modal if pyry's write was clobbered. The foreground claude's own state is preserved — pyry's `map[string]any` pass-through doesn't drop the user's fields, and a lost pyry write just means the trust mark isn't there, which tui-driver handles.

The atomic tempfile + rename is preserved because it protects the **single-writer** axis: a crashed pyry mid-write must not leave `~/.claude.json` in a torn state that breaks the user's own interactive claude. That's a hard requirement (constraint in the AC). Concurrent-writer serialization is a different axis and the safety net moves it.

If a future incident shows the racing-writers case is actually painful (e.g. tui-driver's modal-dismiss is too slow under some specific timing), the fix is a follow-up ticket that re-introduces flock — not retrofitting it here. Per the pipeline's [[Evidence-Based Fix Selection]] principle: defend an observed failure, don't pre-defend a hypothetical one.

No `context.Context` parameter. The helper is fast-bounded (filesystem read + write); no network or blocking I/O.

### Error handling

Identical taxonomy to #341 § "Error handling":

1. **`fs.ErrNotExist` on the data file** — not an error; helper creates a fresh skeleton.
2. **Malformed input** (unparseable JSON, `projects` not an object, `projects[realpath]` not an object) — return wrapped error. Refuse to silently destroy state we don't understand. AC's "File exists, malformed JSON → returns error, leaves file untouched" row asserts this.
3. **Workdir missing** — `agentrun.ResolveWorkdir` returns `fs.ErrNotExist`-wrapped; the helper propagates without touching `~/.claude.json`. AC's "Workdir doesn't exist → returns error before touching `~/.claude.json`" row asserts this. **Implementation note:** the AC requires the helper to short-circuit BEFORE the stat+read on `~/.claude.json`. Step 1 (`ResolveWorkdir`) returns first, before step 4 (`os.Stat(dataPath)`), so this is structurally guaranteed by the body ordering.
4. **I/O failure** (read, fsync, rename, stat, chmod) — return wrapped error with step name.

No retries. Caller chooses.

### Logging discipline

Identical to #341 § "Logging discipline":

- Package doc-comment (above) forbids file-content logging at any layer.
- Error wraps may name step + path; they MUST NOT include file bytes or unmarshalled fields beyond the `workdir` key the caller already supplied.
- No `slog` calls inside the helper.
- The AC's "MUST NOT log workdir paths beyond the resolved realpath returned" is satisfied trivially: the helper makes no log calls.

## Testing strategy

`internal/agentrun/trust/trust_test.go` — table-driven, stdlib `testing`, no testify. Same-package tests.

Each test uses `t.TempDir()` as the explicit `homeDir` passed to `markWorkdirTrustedIn`. Public `MarkWorkdirTrusted` is exercised in **one** smoke test that uses `t.Setenv("HOME", t.TempDir())` (non-parallel) to confirm the `os.UserHomeDir` plumbing works; all other behavioural tests target the parallel-safe internal seam.

Real `~/.claude.json` is never touched.

### `markWorkdirTrustedIn` — behavioural tests

For each row below: `home := t.TempDir()`; create a real workdir via `wd := t.TempDir()` (so `EvalSymlinks` doesn't fail); call `markWorkdirTrustedIn(home, wd)`; read back `~/.claude.json` and assert. Each test calls `t.Parallel()`.

Test rows (mapped from AC):

- **AC "File doesn't exist → creates skeleton"** — no pre-existing `~/.claude.json`. After call: file exists with mode `0o600`, parses as JSON, `projects[<realpath(wd)>].hasTrustDialogAccepted == true`, `projects` has exactly one entry. Returned `realpath` equals `agentrun.ResolveWorkdir(wd)`.
- **AC "File exists, no projects entry → adds + preserves"** — pre-write `~/.claude.json` containing `{"userID": "abc", "telemetry": {"enabled": false}}` (no `projects` key). After call: target entry added under `projects`; `userID` and `telemetry` unchanged.
- **AC "File exists, projects has other workdirs → adds new + preserves"** — pre-write `{"projects": {"/some/other/path": {"hasTrustDialogAccepted": false, "extra": "field"}}}`. After call: both project entries present; the sibling's `hasTrustDialogAccepted` is still `false` and its `extra` field still present.
- **AC "File exists, this workdir already trusted → idempotent"** — pre-write the file with the target key already present and `hasTrustDialogAccepted: true` plus additional keys on its entry (e.g. `"mcpServers": {…}`). Capture file bytes A. Call the helper. Capture bytes B. Assert `bytes.Equal(A, B)`. Pins both idempotency AND within-entry field preservation.
- **AC "File exists, malformed JSON → returns error, leaves file untouched"** — pre-write the file containing `"not json"`. Capture file bytes A. Call the helper; assert returns non-nil error and `bytes.Equal(<file bytes after>, A)`.
- **AC "Workdir doesn't exist → returns error before touching `~/.claude.json`"** — `home := t.TempDir()`; do NOT pre-write the data file; pass `missing := filepath.Join(t.TempDir(), "does-not-exist")` as workdir. Assert: non-nil error wrapping `fs.ErrNotExist` (via `errors.Is`); `~/.claude.json` was NOT created (`os.Stat` on it returns `fs.ErrNotExist`).
- **AC "Workdir is a symlink → uses resolved realpath as map key"** — create a real workdir `target := t.TempDir()`; create `link := filepath.Join(home, "link")` via `os.Symlink(target, link)`; call helper with `link`. Assert: returned `realpath` equals `agentrun.ResolveWorkdir(target)` (NOT `link`); `projects` map's sole key equals that realpath. On macOS the `t.TempDir()` realpath rules (`/private/var/...`) apply transitively.

Additional rows (covering #341-era invariants the slimmed helper still owes):

- **Preserves numeric precision** — pre-write `{"projects": {}, "lastLoginNanos": 1763123456789012345}` (exceeds float64 mantissa). After call: parse the written file with `UseNumber` and assert the value's `String()` is `"1763123456789012345"`. Pins the `UseNumber` requirement.
- **File mode preserved on existing file** — pre-write the data file with mode `0o644`. After call: `os.Stat(dataPath).Mode().Perm() == 0o644`. (Stat the file *post*-rename; the temp file's mode was Chmod'd before rename, so the post-rename file inherits the chosen mode.)
- **`projects` not an object fails** — pre-write `{"projects": "not an object"}`. Assert non-nil error; file untouched (byte-equal).
- **Existing entry not an object fails** — first call `agentrun.ResolveWorkdir(wd)` to compute the key, then pre-write `{"projects": {<key>: "not an object"}}`. Assert non-nil error; file untouched (byte-equal).

### `MarkWorkdirTrusted` (public) — single smoke test

One non-parallel test (because `t.Setenv` forbids parallel ancestors):

- `tmp := t.TempDir(); t.Setenv("HOME", tmp); wd := t.TempDir()`. Call `MarkWorkdirTrusted(wd)`. Assert: returned `realpath` equals `agentrun.ResolveWorkdir(wd)`; `<tmp>/.claude.json` exists with `projects[<realpath>].hasTrustDialogAccepted == true`.

This pins the `os.UserHomeDir` wiring without duplicating the full behavioural matrix on the public surface.

### What NOT to test in #475

- Cross-process concurrency. Explicitly out of scope per the slimming — no flock, so there's no serialization claim to verify. The lost-update case is documented in § "Concurrency model" as accepted, with tui-driver's `HasTrustModal(snap)` as the runtime safety net.
- The `cmd/pyry/agent_run.go` wiring → #470.
- The ptyrunner spawn that consumes the returned realpath → #471/#472.
- Specific JSON formatting beyond what idempotency requires — Go's `encoding/json` output is an implementation detail; tests parse + assert structure, not raw bytes (except the idempotency byte-equality check, which is intra-test).
- Lock file existence — there is no lock file in this slimmed version.

## Open questions

- **`os.UserHomeDir` returning an error on the production path.** `os.UserHomeDir` fails when `$HOME` is unset on Unix (rare for a user-spawned `pyry`; impossible for the systemd/launchd installation because the unit files pin `$HOME`). Spec returns the error wrapped (`agentrun/trust: home dir: %w`). #470's `runAgentRun` is expected to bubble this to a `pyry agent-run` exit-1 with a clear message. If the failure mode is ever observed in dispatcher runs, file a follow-up to pin the home dir at install time (parallel to how the installer pins workdir in #177).
- **What if `~/.claude.json` is a symlink?** `os.Rename` replaces the symlink with a regular file, breaking the symlink. Same trade-off as #341's open question — the helper takes ownership of the path; same-uid, intentional, matches every atomic-rename writer in the project (including claude itself when claude rewrites its own config). Not a security issue.
- **Should the helper fail-open if `~/.claude.json` is unreadable (permission denied) but the workdir exists?** Spec says no — return the wrapped error. The eventual caller (#470) decides whether a trust pre-mark failure should abort the agent run; the helper's job is to surface the failure, not paper over it. If experience shows pre-mark failures should be soft (modal still gets dismissed by tui-driver), a follow-up ticket can add a `MarkWorkdirTrustedBestEffort` variant — but not in #475.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** Single explicit boundary at `MarkWorkdirTrusted`'s call site. The helper trusts its `workdir` argument and stamps the realpath as a JSON map key. The eventual caller (#470) is responsible for validating `workdir` via flag parsing + `os.Stat` regular-dir before calling. Inside the helper the boundary widens to include `~/.claude.json`'s on-disk content — and that's handled by the `map[string]any` + `UseNumber` pass-through discipline (preserve everything verbatim, refuse to overwrite unknown shapes).
- **[Tokens, secrets, credentials]** `~/.claude.json` is not pyry's data — it may contain claude OAuth tokens or other sensitive fields claude wrote. Addressed inline: § "Logging discipline" mandates no file-content logging at any layer; error wraps name step + path but never echo bytes or unmarshalled values. The `map[string]any` + `UseNumber` discipline preserves the secrets through round-trip without pyry ever needing to know what they are. No no-op write either — if the entry is already trusted, the helper still re-writes the file (byte-identically), so secrets-at-rest are unchanged.
- **[File operations]**
  - **Path traversal:** both paths (`<home>/.claude.json` and the tempfile) join under the resolved `homeDir` (from `os.UserHomeDir`, not user input). Production callers cannot inject; tests pass `t.TempDir()`. No traversal risk.
  - **TOCTOU:** the helper deliberately does NOT serialize cross-process (slimming). The TOCTOU surface is the gap between `os.Stat(dataPath)` (for mode capture), the read, and the rename. The worst case is a concurrent writer's update lost OR pyry's mode-capture stale-by-rename. Both outcomes are acceptable per § "Concurrency model": tui-driver's `HasTrustModal(snap)` dismisses any modal that appears because pre-marking lost the race. **Not exploitable** — same-uid trust boundary, same-uid writer; the user's own claude is the only entity contending for this file under the threat model.
  - **Symlinks on the data file:** `os.Rename` replaces a symlinked target with a regular file. Same trade-off as #341 (open questions); same-uid, intentional. Not an exploit vector.
  - **Symlinks on the workdir:** `agentrun.ResolveWorkdir` resolves them explicitly via `filepath.EvalSymlinks`; the resolved path is what becomes the map key. This is required for correctness (claude's keying rule) AND removes the "attacker swaps the workdir's symlink between EvalSymlinks and the JSON write" footgun — there is no gap; the resolved string is stamped into the JSON and the workdir itself is not re-touched after that.
  - **Permissions:** existing file mode preserved; new file at `0o600`. Spec is explicit (§ "File mode"). Note: this is intentionally NOT tightening claude's chosen mode (claude defaults to `0o644` in practice). If a future security review wants `0o600` enforced regardless of claude's choice, that's a separate ticket and a separate trade-off conversation.
  - **Atomic writes:** yes, mandatory, mirrors `internal/devices/registry.go:Save` (§ "Write step"). The single-writer crash-safety axis is preserved even though cross-process serialization was removed.
- **[Subprocess / external command execution]** N/A — no subprocess.
- **[Cryptographic primitives]** N/A — no crypto.
- **[Network & I/O]** No network. Local file. **Resource exhaustion:** a hostile-sized `~/.claude.json` would be read entirely into memory via the default `json.Decoder` (this slimmed version still calls `os.ReadFile` shape; the alternative would be a streaming decoder with a bounded read). The attacker who can grow that file to GB scale is the same uid as pyry; same trust boundary as the running user. No size cap needed (out of scope: a future per-file size cap would be a generic hardening, not specific to this helper).
- **[Error messages, logs, telemetry]** Addressed inline (§ "Logging discipline"). Error wraps name the step (`encode` / `fsync` / `rename` / `read` / `parse` / `stat` / `chmod` / `home dir` / `create temp`) and the path. They MUST NOT include file bytes or unmarshalled fields beyond the workdir key the caller already supplied. No `slog` calls inside the helper.
- **[Concurrency]** No locks, no goroutines, no shared in-process state. Cross-process serialization explicitly **descoped** with documented rationale (§ "Concurrency model"): tui-driver's `HasTrustModal(snap)` is the runtime safety net. The single-writer crash-safety property remains (tempfile + rename + fsync). **This is the meaningful spec change from #341 and the highest-risk decision in the slimming** — flagged here because the security review must surface design-level concurrency decisions explicitly, not just implementation concurrency bugs. Verdict: **acceptable risk** under the threat model (local, single-uid, with downstream safety net).
- **[Threat model alignment]** Local-file CLI helper; threat model is "the user's own machine, the user's own uid". Cross-uid: home-dir permissions already protect against other-uid readers; orthogonal to this helper. Same-uid: trusted per the Unix model. No new attack surface vs the existing `internal/devices/registry.Save` pattern this helper mirrors.

**MUST FIX in spec:** None.

**SHOULD FIX in spec (resolved inline before this verdict):**

- The concurrency-model descope from #341 (dropping flock) is the load-bearing decision in this ticket; § "Concurrency model" documents the rationale + the safety net + the follow-up trigger. Required for an informed code review; resolved inline.
- The logging-discipline carry-forward from #341 is restated in the package doc-comment (§ "Public API") AND the body discipline (§ "Logging discipline" + the step-naming list in § "Write step"). Prevents accidental token leakage via debug logs in #470's wire-up.

**OUT OF SCOPE:**

- Cross-process serialization of concurrent writers — explicitly descoped; tui-driver's `HasTrustModal(snap)` is the runtime safety net. Re-introduce only if a follow-up ticket documents an observed failure where the safety net is insufficient ([[Evidence-Based Fix Selection]]).
- File size caps on `~/.claude.json` — generic hardening, not specific to this helper.

**Reviewer:** architect (self-review per `agents/architect/security-review.md`)
**Date:** 2026-05-19
