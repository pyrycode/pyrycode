# `internal/agentrun/trust` — pre-answer startup gates on the `~/.claude.json` workdir entry

Pre-answers, on the same `projects[<realpath(workdir)>]` entry in `~/.claude.json`, the startup gates a headless `claude` child cannot answer for itself: `hasTrustDialogAccepted = true` skips the workspace-trust modal, and — widened by [#2451](https://github.com/pyrycode/pyrycode/issues/2451) — `hasClaudeMdExternalIncludesApproved = true` plus `hasClaudeMdExternalIncludesWarningShown = true` let a CLAUDE.md `@` import that resolves outside the session's working directory expand. The dispatcher's automated flow has no human present to answer any of these dialogs; pre-marking side-steps them entirely.

Introduced [#475](https://github.com/pyrycode/pyrycode/issues/475) as a **slimmed resurrection** of the helper [#341](https://github.com/pyrycode/pyrycode/issues/341) shipped and [#392](https://github.com/pyrycode/pyrycode/issues/392) deleted. The 2026-05-19 pivot back to PTY drive ([`codebase/471.md`](../codebase/471.md)) made the modal a problem again. The slimming drops the cross-process flock the original required, because `ptyrunner`'s post-idle `HasTrustModal` detector is the runtime safety net.

## Public API

```go
// MarkWorkdirTrusted ensures, on ~/.claude.json's
// projects[<realpath(workdir)>] entry:
//
//	hasTrustDialogAccepted                  = true
//	hasClaudeMdExternalIncludesApproved     = true
//	hasClaudeMdExternalIncludesWarningShown = true
//
// All three are written unconditionally — an existing false is overwritten.
// Idempotent. Atomic — writes to a tempfile in the same directory then
// renames over the target. Returns the resolved realpath on success.
func MarkWorkdirTrusted(workdir string) (realpath string, err error)
```

No exported types, no constructor, one function. Returns the resolved realpath so a caller can pass it straight to the spawned claude's `cmd.Dir` (or equivalent) without a second `agentrun.ResolveWorkdir` call, keeping the marked key and the child's cwd byte-identical. See § Consumers for who calls this today.

## Internal test seam

The public wrapper is two lines: `os.UserHomeDir()` then delegate to unexported `markWorkdirTrustedIn(homeDir, workdir string) (string, error)`. Tests pass `t.TempDir()` directly as `homeDir`, which lets them run `t.Parallel()` — Go's testing docs forbid `t.Setenv` (the only alternative way to redirect `os.UserHomeDir`) from a `t.Parallel` ancestor. The behavioural matrix targets the parallel-safe seam; a single non-parallel smoke test exercises the public `MarkWorkdirTrusted` via `t.Setenv("HOME", ...)` to pin the `os.UserHomeDir` plumbing.

## Why a subpackage instead of `internal/agentrun/trust.go`

\#341 lived as a sibling file under `internal/agentrun/`. The subpackage layout (`internal/agentrun/trust/`) was chosen for #475 to mirror the sibling spawn primitives `internal/agentrun/ptyrunner/` and `internal/agentrun/streamrunner/`. The parent `internal/agentrun` package now hosts only workdir helpers (`ResolveWorkdir`, `EncodeProjectDir`) that all three subpackages import; spawn concerns and trust concerns are package-scoped, not file-scoped.

## Key shape — realpath, on-disk case, not abspath

`projects` map keys are the **`filepath.EvalSymlinks`-resolved, on-disk-cased** absolute path. The macOS `/var → /private/var` symlink means a non-resolved key never matches claude's lookup. The helper delegates to `agentrun.ResolveWorkdir`, which does `filepath.Abs`, then `filepath.EvalSymlinks`, then (#910) canonicalises each path component to its on-disk spelling — the single pyrycode-wide source of truth for "claude's realpath rule." `internal/sessions/rotation/watcher.go` uses the same helper for path comparison against platform-probe results.

**Why case matters:** `EvalSymlinks` alone preserves the *input* case of a non-symlink component, but claude canonicalises its cwd to the on-disk case before its own trust lookup. Before #910, a workdir configured with the wrong case on a case-insensitive filesystem (macOS APFS) made this helper pre-mark a `projects` key claude never reads — the trust modal rendered anyway and `ptyrunner.Run` aborted with `ErrTrustModalDetected` (the 2026-05-29 incident on #208). See [`codebase/910.md`](../codebase/910.md).

## Pass-through preservation

`~/.claude.json` is not pyry's data — claude stores OAuth tokens, timestamps, counters, and other fields pyry does not own. The helper round-trips through `map[string]any` so unknown fields survive verbatim.

**`json.Decoder.UseNumber()` is mandatory.** Default decoding drops JSON numbers into `float64`, losing precision above 2^53; `~/.claude.json` may carry int64-sized values (e.g. `lastLoginNanos`). The pinning test `TestMarkWorkdirTrusted_PreservesNumericPrecision` writes a known precision-eater (`1763123456789012345`) and asserts the value round-trips byte-identically.

`encoding/json` sorts map keys alphabetically on marshal, so output is deterministic for the same logical content. Combined with `UseNumber`, this gives byte-identical idempotency for repeated calls (pinned by `TestMarkWorkdirTrusted_IdempotentPreservesExtraEntryFields`).

## No lock — best-effort cross-process write

Unlike #341, this helper holds no file-lock. Concurrent invocations against the same `~/.claude.json` (e.g. pyry pre-writing while the user's interactive claude rewrites its own config) can race. The worst case is a lost update — one writer's `projects` entry overwrites the other's — which means pyry's trust pre-mark may not be present when the supervised claude spawns.

The safety net, per this package's own doc-comment, is [`tui-driver`](https://github.com/pyrycode/tui-driver)'s post-idle `HasTrustModal(snap)` detector: if the modal appears because pre-marking lost the race, it is dismissed at runtime rather than wedging the session. `ptyrunner`, the package that originally owned this detector, was deleted in #1348; the detector's current call site was not re-verified for this ticket and is worth confirming before citing a specific symbol here.

**The single-writer atomic-write property is preserved.** A SIGKILL'd pyry mid-write must not leave `~/.claude.json` torn for the user's own interactive claude sessions. Tempfile + `Sync` + `Close` + `os.Rename` guarantees that the rename point is either pre- or post-write; the file is never partial.

If a future incident shows the racing-writers case is painful (e.g. `HasTrustModal`'s detection is too slow under some specific timing), the fix is a follow-up ticket that re-introduces flock — not retrofitting it here. *Evidence-Based Fix Selection*: defend an observed failure, don't pre-defend a hypothetical one.

## Atomic write

Mirrors `internal/devices/registry.go:Save` line-for-line (the canonical pyrycode atomic-write recipe — convention is duplication-not-extraction until a fifth registry forces it):

1. `os.CreateTemp(homeDir, ".claude.json.tmp-*")` — same dir as rename target so the rename is intra-filesystem.
2. `defer os.Remove(tmp.Name())` — best-effort cleanup if any later step fails.
3. `os.Chmod(tmp.Name(), mode)` — preserves the existing file's mode, or `0o600` when creating.
4. `enc.SetIndent("", "  ")` + `enc.Encode(root)`.
5. `tmp.Sync()` → `tmp.Close()` → `os.Rename(tmp.Name(), dataPath)`.

Each step wraps errors as `fmt.Errorf("agentrun/trust: <step>: %w", err)` naming the step (`create temp` / `chmod temp` / `encode` / `fsync` / `close temp` / `rename` / `stat` / `read` / `parse` / `home dir`). They MUST NOT include file bytes or unmarshalled fields.

## File mode

- If `~/.claude.json` already exists, copy its mode to the temp file before rename so we don't quietly tighten or loosen the user's file. Claude defaults to `0o644` in practice; pyry does not override.
- If absent, create at `0o600`.
- Mode is informational, not a security boundary (single-user data inside `$HOME`); a single `os.Stat` before the read is sufficient — no double-stat dance.

Pinned by `TestMarkWorkdirTrusted_PreservesFileMode`.

## Error handling

Three terminal classes:

1. **`fs.ErrNotExist` on the data file** — not an error; helper creates a fresh `{"projects": {<key>: {"hasTrustDialogAccepted": true, "hasClaudeMdExternalIncludesApproved": true, "hasClaudeMdExternalIncludesWarningShown": true}}}` skeleton. Parent directory (`$HOME`) is assumed to exist.
2. **Malformed input** (unparseable JSON, `projects` not an object, `projects[realpath]` not an object) — wrapped error; the file is left untouched (pinned via `bytes.Equal` pre/post in `TestMarkWorkdirTrusted_MalformedJSONFails`, `TestMarkWorkdirTrusted_ProjectsNotObjectFails`, and `TestMarkWorkdirTrusted_EntryNotObjectFails`). The helper refuses to silently destroy state it doesn't understand.
3. **I/O failure** (read, stat, chmod, encode, fsync, close, rename) — wrapped error with the step name.

**Workdir-missing short-circuits via `ResolveWorkdir` BEFORE any `~/.claude.json` access** — pinned by `TestMarkWorkdirTrusted_WorkdirMissingReturnsError`. The error wraps `fs.ErrNotExist` (via `errors.Is`) and `~/.claude.json` is not created. Each caller surfaces the failure in its own idiom (see § Consumers): `runSupervisor` fails the daemon's startup outright, `resolveSpawnDir` returns it plain so the handler classifies it as a retryable per-conversation spawn failure, and `selfcheck.SelfCheckDenyDefault` wraps it into its `Result` error.

No retries. The caller chooses.

## Logging discipline

The package doc-comment is load-bearing:

```
MUST NOT log file contents at any layer. ~/.claude.json may contain
tokens or claude-internal state pyry does not own; the helper takes a
pass-through view (preserve fields verbatim) and emits nothing to logs.
```

- No `slog` calls inside the helper.
- Error wraps name the step + path; never file bytes or unmarshalled fields.
- AC's "MUST NOT log workdir paths beyond the resolved realpath returned" is satisfied trivially — the helper makes no log calls.

## Concurrency model

No goroutines spawned. Purely sequential within an invocation: stat → read → mutate → write. No `context.Context` parameter — the operation is fast-bounded (local filesystem read + write). If a future caller needs cancellable-acquire, add a context-taking sibling without changing this signature.

Cross-process concurrent invocations are explicitly **not** serialised — see § "No lock" above.

## Dependency direction

- Stdlib: `bytes`, `encoding/json`, `errors`, `fmt`, `io/fs`, `os`, `path/filepath`.
- Internal: `github.com/pyrycode/pyrycode/internal/agentrun` (for `ResolveWorkdir` only).
- External: none.

## Testing

`internal/agentrun/trust/trust_test.go` — same-package, stdlib `testing` only, no testify.

Each behavioural test uses `home := t.TempDir()` + `wd := t.TempDir()` and calls `markWorkdirTrustedIn(home, wd)`. Every behavioural test below calls `t.Parallel()` except `TestMarkWorkdirTrusted_PublicSmoke`, which cannot (see below). Two helpers — `writeJSON(t, path, root, mode)` (encode + write + chmod for fixtures) and `readJSON(t, path)` (decode with `UseNumber` for assertions) — keep test bodies focused.

Test cases:

- `TestMarkWorkdirTrusted_CreatesFileWhenMissing` — no pre-existing file → creates mode-`0o600` file with one-entry skeleton.
- `TestMarkWorkdirTrusted_AddsToExistingFileWithoutProjects` — pre-existing top-level fields (`userID`, `telemetry`) preserved; `projects` added.
- `TestMarkWorkdirTrusted_PreservesSiblingProjects` — pre-existing `projects["/some/other/path"]` with its own `hasTrustDialogAccepted: false` + `extra` field survives untouched alongside the new entry.
- `TestMarkWorkdirTrusted_IdempotentPreservesExtraEntryFields` — pre-existing target entry carries all three flags plus an `mcpServers` subfield → repeat call produces byte-identical output (pins idempotency AND within-entry preservation). The fixture was widened to carry the two include keys when [#2451](https://github.com/pyrycode/pyrycode/issues/2451) added them to the write — this is the one existing test that ticket reddened, and the fix was carrying the new keys in the fixture, not loosening the byte-identity assertion.
- `TestMarkWorkdirTrusted_SetsExternalIncludeFlagsOverExistingFalse` — [#2451](https://github.com/pyrycode/pyrycode/issues/2451)'s AC-1 check: target entry pre-set with all three flags explicitly `false` plus an `mcpServers` sibling key, and a second project entry also at `false`. After the call: the target carries all three flags `true` with `mcpServers` intact; the sibling project entry is untouched, still `false`. One test covers every clause — flags set, an existing `false` overwritten, sibling keys preserved.
- `TestMarkWorkdirTrusted_MalformedJSONFails` — pre-existing file containing `"not json"` → non-nil error; file bytes unchanged.
- `TestMarkWorkdirTrusted_WorkdirMissingReturnsError` — workdir does not exist → `errors.Is(err, fs.ErrNotExist)`; `~/.claude.json` not created.
- `TestMarkWorkdirTrusted_WorkdirSymlinkResolvesToRealpath` — `os.Symlink(target, link)`, call with `link` → returned realpath equals `agentrun.ResolveWorkdir(target)` (NOT `link`); `projects` has one entry under the resolved key.
- `TestMarkWorkdirTrusted_PreservesNumericPrecision` — pre-existing `lastLoginNanos: 1763123456789012345` → value round-trips through `json.Number.String()` byte-identically.
- `TestMarkWorkdirTrusted_PreservesFileMode` — pre-existing file at `0o644` → post-rename mode still `0o644`.
- `TestMarkWorkdirTrusted_ProjectsNotObjectFails` — pre-existing `{"projects": "not an object"}` → non-nil error; file untouched.
- `TestMarkWorkdirTrusted_EntryNotObjectFails` — pre-existing `{"projects": {<realpath>: "not an object"}}` → non-nil error; file untouched.
- `TestMarkWorkdirTrusted_PublicSmoke` (non-parallel) — `t.Setenv("HOME", t.TempDir())` → `MarkWorkdirTrusted(wd)` succeeds and writes the expected entry; pins `os.UserHomeDir` plumbing without duplicating the full behavioural matrix.

**Live tier:** `TestClaudeMdExternalIncludes_SubfolderChildGetsRootImports` in `internal/e2e/realclaude/claude_md_external_includes_test.go` (`//go:build e2e_realclaude`, run by `make e2e-realclaude`) is the only test that proves AC-2 of [#2451](https://github.com/pyrycode/pyrycode/issues/2451) — that a real child spawned in a workspace subfolder gets the root CLAUDE.md with every import expanded — since that requires a real claude child and its transcript. Two-arm differential: a marked arm (`trust.MarkWorkdirTrusted` on the workspace root) whose import must expand, and a control arm (root hand-written to the pre-fix state: trusted, includes unapproved) whose import must not. See § "The external-includes gate is keyed on the git root..." above for why both fixture roots must be real git repositories and why each arm also requires a CLAUDE.md-only sentinel as a vacuity guard.

## What this helper deliberately does NOT do

- **No cross-process serialisation.** No flock. Concurrent writers may produce lost updates; the runtime safety net is `HasTrustModal` (see § "No lock" above).
- **No retries.** Caller decides.
- **No logging.** Operator-visible diagnostics happen at the consumer.
- **No size cap on `~/.claude.json`.** A hostile-sized file is the same-uid threat model as pyry; same trust boundary as the running user. Generic hardening, not specific to this helper.

## The external-includes gate is keyed on the git root of the child's cwd, not the cwd itself

This is the fact that makes marking the bootstrap workdir cover every subfolder conversation, rather than being an arbitrary fix. Read directly out of the claude 2.1.259 binary (not inferred) while chasing a live-gate red on [#2451](https://github.com/pyrycode/pyrycode/issues/2451):

- `hasClaudeMdExternalIncludesApproved` and `hasClaudeMdExternalIncludesWarningShown` sit in claude's *project* config defaults on the same `projects[...]` entry as `hasTrustDialogAccepted` — this package's premise that they live on one entry is right.
- An `@` import is gated when it resolves outside the child's cwd, and the gate is lifted by `hasClaudeMdExternalIncludesApproved` read from the *current project config*.
- That config is keyed by **the canonical git root of the cwd, falling back to the cwd itself when no repository encloses it** — not by the cwd directly.

In production the workspace root is a repository, so a child spawned in `<root>/default` is governed by `<root>`'s entry: marking the bootstrap workdir (`<root>`) with `MarkWorkdirTrusted` is therefore sufficient for every child spawned anywhere under it, and marking `<root>/default` directly — which was tried before this fix — does nothing, because claude never consults that entry.

**This caught out the first cut of the live differential test in `internal/e2e/realclaude/claude_md_external_includes_test.go`.** Its workspace was a bare `os.MkdirAll` tree with no repository, so the key degraded to `<root>/default` — an entry neither the test nor `pyry agent-run` ever marks (the verb carries no `trustMark` call; see § Consumers). Neither arm had an approved entry, so neither child expanded its import: the marked arm went red and the control arm went green for the wrong reason. The fix, `makeGitRoot`, gives each fixture workspace root a real `git init` (not a linked worktree — claude only skips a main-repo ancestor's CLAUDE.md from inside a *linked* worktree, which a plain repo is not) to restore the production topology. A second, CLAUDE.md-only sentinel required present by both arms is the accompanying vacuity guard: without it, an arm asserting an import is *absent* passes whenever nothing reached the child at all, which is exactly how the broken fixture produced a "passing" control arm.

**Lesson for any future differential test in this space:** an arm that asserts something is absent only means something if a companion assertion proves the fixture could have made it present.

## Consumers

`trustMark` (a package-level `var` in `cmd/pyry/agent_run.go` aliasing `trust.MarkWorkdirTrusted`, overridable in tests) has three production call sites, all in `cmd/pyry` or `internal/agentrun/selfcheck`. **`pyry agent-run` itself is not one of them** — the verb carries no `trustMark` call. That is a load-bearing negative: an earlier draft of [#2451](https://github.com/pyrycode/pyrycode/issues/2451)'s implementation plan assumed `agent-run` pre-marked the folder it spawns in, which shaped a live-test fixture around a premise that was never true and produced a false-passing control arm — see § "The external-includes gate is keyed on the git root..." above.

- **`pyry` daemon serve path** (`runSupervisor`, #670) — before any spawn, confines `-pyry-workdir` to `$HOME` via `confineWorkdirToHome`, then pre-marks the confined realpath and threads it into `Bootstrap.WorkDir` (→ `cmd.Dir`), so the marked key and the supervised child's cwd are byte-identical. This is the bootstrap workdir [#2451](https://github.com/pyrycode/pyrycode/issues/2451) targets: because the include-approval entry is keyed on the child's git root rather than its cwd, marking this one entry also covers every conversation spawned in a subfolder beneath it. Without the trust pre-write the supervised claude wedged on the trust modal — and unlike agent-run, the daemon has no dispatcher retry: it fell into the #421 clean-exit restart loop (`claude exited cleanly` forever), invisible to the phone (the bridge forwards only transcript events). See [`codebase/670.md`](../codebase/670.md).
- **`resolveSpawnDir`** (`cmd/pyry/main.go`) — validates a phone-requested per-conversation spawn workdir for `create_conversation`. A non-empty request is expanded (`~` → `$HOME`), confined and created under `$HOME` via `confineWorkdirToHomeCreating`, then trust-marked; the empty-request case returns early and never calls `trustMark` (the pool spawns in the shared trusted template workdir instead). Confinement runs strictly before trust-marking, since `trustMark` carries no `$HOME` bound of its own.
- **`internal/agentrun/selfcheck.SelfCheckDenyDefault`** — the `--self-check` diagnostic verb calls `trustMark(cfg.WorkDir)` directly, with **no** `$HOME` confinement ahead of it. This is the unconfined path referenced below.

### `$HOME` confinement is caller-side, not in this helper (#670)

`MarkWorkdirTrusted` performs **no** confinement and must not — it is shared by the unconfined self-check path above. The daemon serve path is `security-sensitive` because it auto-accepts the trust gate for the host that executes phone-originated (untrusted-party) turns, so #670 added a `$HOME` bound as a strict-tightening deny-gate at its own call site (`runSupervisor`), and `resolveSpawnDir` carries the equivalent bound for the per-conversation path: a workdir whose realpath resolves outside `$HOME` is rejected as a loud startup failure (`runSupervisor`) or a non-retryable `handlers.ErrSpawnDirRejected` (`resolveSpawnDir`), never trusted, never launched. The check canonicalises *both* sides (`EvalSymlinks` on `$HOME` and the workdir) and uses a boundary-aware `filepath.Rel` containment test (not a string prefix), so a symlinked home isn't a false reject and `/home/userfoo` isn't treated as inside `/home/user` (the #118/#221 gotcha). Rejection errors name the path and the boundary, never `~/.claude.json` contents. Pushing the bound into this helper would silently confine the self-check path too — which is why it stays at each caller.

## Out of scope

- The `cmd/pyry/agent_run.go` wiring — declares the `trustMark` seam but does not call it; see § Consumers.
- Cross-process concurrency serialisation — explicitly descoped; revisit only on an observed failure.

## Related

- [agentrun-package.md](agentrun-package.md) — surrounding parent package; `ResolveWorkdir` (the realpath rule) lives there.
- [ptyrunner-package.md](ptyrunner-package.md) — the original spawn primitive this trust state was written for; deleted in #1348. Historical only — its runtime `HasTrustModal` safety net has a live successor (see § "No lock" above) that this doc does not yet name precisely.
- [devices-registry.md](devices-registry.md) — the canonical atomic-write recipe this package mirrors.
- [rotation-watcher.md](rotation-watcher.md) — existing user of the same `EvalSymlinks`-via-`ResolveWorkdir` pattern.
- [`codebase/475.md`](../codebase/475.md) — build notes (file inventory, patterns, lessons).
- [`docs/specs/architecture/475-agentrun-trust-helper.md`](../../specs/architecture/475-agentrun-trust-helper.md) — architect spec.
- [`codebase/392.md`](../codebase/392.md) — the deletion this ticket reverses.
- [`codebase/341.md`](../codebase/341.md) — the original (pre-deletion) helper; this slimmed version's contract is a strict subset.
- [`codebase/910.md`](../codebase/910.md) — `ResolveWorkdir`'s on-disk-case canonicalisation fix; closes the case-mismatch gap this doc's "Key shape" section now describes.
