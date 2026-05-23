# #508 — agentrun: delete `EncodeProjectDir`; remove redundant `EvalSymlinks` from the JSONL hot path

## Files to read first

- `internal/agentrun/workdir.go` (45 lines) — the two functions under refactor. `EncodeProjectDir` (chains `ResolveWorkdir` → `tuidriver.EncodeCwd`) is deleted; `ResolveWorkdir` is left untouched because `trust.go` is its remaining caller (out of scope per ticket AC2).
- `internal/agentrun/workdir_test.go` (158 lines) — the `TestResolveWorkdir_*` tests stay verbatim; the `TestEncodeProjectDir_*` block (lines 69–157) is deleted. The behaviours those tests exercised are covered by `tuidriver`'s own `cwd_test.go` (canonicalisation rules, dash substitution, darwin realpath).
- `internal/agentrun/jsonl/tail/watcher.go:108-119` — current pattern: call `agentrun.ResolveWorkdir(cfg.Workdir)`, pass `resolved` into `tuidriver.SessionJSONLPath`, then `MkdirAll`. The `ResolveWorkdir` step is redundant — `SessionJSONLPath` internally calls `EncodeCwd`, which already canonicalises (darwin: `F_GETPATH`; linux: `EvalSymlinks`). This spec drops the explicit `ResolveWorkdir` call.
- `internal/agentrun/jsonl/tail/watcher_test.go:92-101` — `expectedEncodedDir` helper currently mirrors the production canonicalisation step via `agentrun.ResolveWorkdir` then `tuidriver.SessionJSONLPath`. Update to match the simplified production call (pass `workdir` straight through).
- `internal/e2e/realclaude/fixtures.go:360-376` — `resolveAndOpenJSONL` calls `agentrun.EncodeProjectDir` then composes the path. The whole composition collapses to `tuidriver.SessionJSONLPath(home, workdir, sessionID)`.
- `internal/e2e/realclaude/fixtures_test.go:295-300, 553-575` — two `agentrun.EncodeProjectDir(workdir)` sites used to compute the expected on-disk path. Both lift to `tuidriver.SessionJSONLPath`.
- `internal/e2e/realclaude/prompt_fidelity_test.go:79-89` — `jsonlPathFor` diagnostic helper. Same lift.
- `internal/agentrun/ptyrunner/runner_test.go:31-62` — `helperRunCfg` precomputes the encoded JSONL path the fake helper child writes into. Switch the encoder call to `tuidriver.EncodeCwd(workdir)` (returns plain `string`, drop the err check).
- `vendor view: github.com/pyrycode/tui-driver@v0.0.0-20260523181457-c2dcd1e49992/pkg/tuidriver/cwd.go:20-35` — `EncodeCwd` contract: input canonicalised via `canonicalisePath`; on resolution failure, the input is encoded **as-passed** (no error path). This is the behaviour shift documented in § Error handling.
- `internal/agentrun/trust/trust.go:50-54` — left untouched. Its `agentrun.ResolveWorkdir` call is the **sole remaining production caller** of `ResolveWorkdir` after this ticket. Out of scope per AC2 (different output shape required — fs path, not dashed name — plus security-boundary audit not in this ticket's budget). A follow-up issue tracks eventual removal.

## Context

#501 made `agentrun.EncodeProjectDir` delegate to `tuidriver.EncodeCwd`, but the wrapper still chains through `agentrun.ResolveWorkdir` first. Since tui-driver #57 shipped `canonicalisePath` inside `EncodeCwd` (darwin: `F_GETPATH`; non-darwin: `EvalSymlinks`), the chain resolves symlinks twice on linux and does an extraneous `EvalSymlinks` before `F_GETPATH` on darwin. Error-path semantics also diverge: `ResolveWorkdir` errors on `fs.ErrNotExist`; `EncodeCwd` silently falls back to encoding the input as-passed.

The redundancy lives in two places now:

1. `EncodeProjectDir` itself (the wrapper).
2. `jsonl/tail/watcher.go` — which #509 changed to call `agentrun.ResolveWorkdir` directly before passing the resolved path into `tuidriver.SessionJSONLPath`. `SessionJSONLPath` internally invokes `EncodeCwd`, so the explicit `ResolveWorkdir` step is the same redundancy in a different shape.

This ticket eliminates both. The remaining `ResolveWorkdir` caller is `trust.go`, which needs the filesystem path as the **key into `~/.claude.json`'s `projects` map** — a different output shape than `EncodeCwd`'s dashed projects-dir name. Touching that caller is a security-boundary change (workspace-trust modal gates command execution) and is explicitly deferred to a follow-up issue.

## Design

### `EncodeProjectDir` — option (a): delete

The wrapper is removed. All callers switch to `tuidriver.EncodeCwd(workdir)` directly — or, where the caller composes the full JSONL path, to `tuidriver.SessionJSONLPath(home, workdir, sessionID)` which already wraps the encoder.

Rationale for option (a) over (b):

- Caller count is small and bounded (5 production + test sites; 6 with `prompt_fidelity_test.go`'s diagnostic). All sites are mechanical signature swaps — `(string, error)` → `string`. No site pattern-matches on `fs.ErrNotExist`, confirmed by audit (see Error handling).
- Keeping a one-line `(workdir string) (string, error)` wrapper that always returns nil error would be a vestigial signature — invites callers to write speculative error handling for a path that cannot fail.
- Each caller already uses `tuidriver.SessionJSONLPath` (or composes a path under `~/.claude/projects/`), so importing `tuidriver` is either already present or a one-import addition with `agentrun` dropping out as the only-use import.

After: `internal/agentrun/workdir.go` contains exactly one exported function (`ResolveWorkdir`) until the follow-up migrates `trust.go`. Package doc comment trimmed to reflect the single-function surface.

### `ResolveWorkdir` — kept, but its `jsonl/tail/watcher.go` caller is dropped

`watcher.go` `New` currently:

```
resolved, err := agentrun.ResolveWorkdir(cfg.Workdir)   // EvalSymlinks
if err != nil { return ... }
expectedPath := tuidriver.SessionJSONLPath(home, resolved, cfg.SessionID)
                                                                 // ↳ EncodeCwd
                                                                 //   ↳ canonicalisePath (F_GETPATH or EvalSymlinks)
```

After:

```
expectedPath := tuidriver.SessionJSONLPath(home, cfg.Workdir, cfg.SessionID)
```

Single canonicalisation, single source of truth. The `expectedEncodedDir` test helper in `watcher_test.go` collapses to match (drops the `ResolveWorkdir` call).

After this ticket `ResolveWorkdir` has exactly one remaining production caller (`trust.go`). The follow-up issue tracks its eventual removal alongside a tuidriver-exposes-canonicalise change or a local `filepath.EvalSymlinks` in `trust.go`.

### Caller switch table

| Site | Current call (returns `(string, error)`) | New call |
|---|---|---|
| `internal/e2e/realclaude/fixtures.go:366` | `enc, err := agentrun.EncodeProjectDir(workdir)` → joined with `home` + `.claude/projects/` + `sessionID+".jsonl"` | `path := tuidriver.SessionJSONLPath(home, workdir, sessionID)` — the entire `path :=` composition collapses to one line. |
| `internal/e2e/realclaude/fixtures_test.go:296` | `enc, _ := agentrun.EncodeProjectDir(workdir)` → joined into `wantPath` | `wantPath := tuidriver.SessionJSONLPath(home, workdir, testSessionID)` |
| `internal/e2e/realclaude/fixtures_test.go:559` | `enc, err := agentrun.EncodeProjectDir(workdir)` → joined into `dir`, `MkdirAll(dir)` | `path := tuidriver.SessionJSONLPath(home, workdir, sessionID); dir := filepath.Dir(path); MkdirAll(dir, 0o700)` |
| `internal/e2e/realclaude/prompt_fidelity_test.go:84` | `enc, err := agentrun.EncodeProjectDir(workdir)` → joined into final path | `path := tuidriver.SessionJSONLPath(home, workdir, sessionID)`; the `<unresolved encoding: ...>` failure sentinel is no longer reachable — drop that arm (HOME-resolution remains the only failure mode). |
| `internal/agentrun/ptyrunner/runner_test.go:35` | `encoded, err := agentrun.EncodeProjectDir(workdir)` → joined into `jsonlPath` | `jsonlPath := tuidriver.SessionJSONLPath(home, workdir, testSessionID)` — composes equivalently and is in tighter alignment with how production composes (`tail.New` uses the same function). |
| `internal/agentrun/jsonl/tail/watcher.go:111` | `resolved, err := agentrun.ResolveWorkdir(cfg.Workdir)` → into `SessionJSONLPath` | Drop the `ResolveWorkdir` call. Pass `cfg.Workdir` directly. |
| `internal/agentrun/jsonl/tail/watcher_test.go:96` | `resolved, err := agentrun.ResolveWorkdir(workdir)` in `expectedEncodedDir` helper | Drop the call; helper becomes `return filepath.Dir(tuidriver.SessionJSONLPath(home, workdir, sessionID))`. |

### Import bookkeeping

After the switches:

- `internal/e2e/realclaude/fixtures.go` — `agentrun` was the only-use import (the `internal/agentrun/jsonl` alias-import is separate and stays). Replace `"github.com/pyrycode/pyrycode/internal/agentrun"` with `"github.com/pyrycode/tui-driver/pkg/tuidriver"`.
- `internal/e2e/realclaude/fixtures_test.go` — same. The top-level `agentrun` import is the only-use; the `agentrun/jsonl` alias-import stays.
- `internal/e2e/realclaude/prompt_fidelity_test.go` — `agentrun` is only-use. Replace with `tuidriver`.
- `internal/agentrun/ptyrunner/runner_test.go` — `agentrun` is only-use. `tuidriver` already imported (line 16). Drop the `agentrun` import.
- `internal/agentrun/jsonl/tail/watcher.go` — `agentrun` is only-use (the package self-imports `agentrun` for `ResolveWorkdir`). `tuidriver` already imported (line 20). Drop the `agentrun` import. Trim the doc comment on line 108-110 (the "Claude resolves symlinks ... has to happen here" paragraph) to reflect the new single-step pattern.
- `internal/agentrun/jsonl/tail/watcher_test.go` — `agentrun` is only-use. `tuidriver` already imported (line 17). Drop the `agentrun` import.

### `internal/agentrun/workdir.go` final shape

After the edit the file contains only `ResolveWorkdir` (body unchanged from current `workdir.go:20-30`) and a trimmed package doc comment. Specifically:

- Delete `EncodeProjectDir` (current lines 32-44) and its preceding doc comment.
- Drop the `"github.com/pyrycode/tui-driver/pkg/tuidriver"` import — no longer needed in this file. Retain `"fmt"` and `"path/filepath"`.
- Rewrite the package doc comment to reflect the single-function surface: explain that this package owns `ResolveWorkdir` (used by `agentrun/trust`) and that JSONL-path encoding lives in `tuidriver`. Keep the `MUST NOT log file contents` invariant.
- `ResolveWorkdir`'s doc comment gets a one-line addendum naming `internal/agentrun/trust` as the sole remaining caller (anchors the follow-up issue tracker and helps the next maintainer find the migration site).

### `workdir_test.go` final shape

Keep the four `TestResolveWorkdir_*` tests verbatim. Delete the five `TestEncodeProjectDir_*` tests (lines 69-157). Remove the `tuidriver` and `strings` imports if they become unused after the deletion — current `strings` is used only by EncodeProjectDir tests; `tuidriver` likewise.

## Concurrency model

No goroutines added, removed, or restructured. The only behavioural shift is the canonicalisation site moves from `agentrun.ResolveWorkdir` (called eagerly at watcher `New`) into `tuidriver.EncodeCwd` (called inline by `SessionJSONLPath`). Both call sites are single-threaded, called once per watcher construction.

## Error handling

The semantic shift this spec accepts:

| Path | Before | After |
|---|---|---|
| `cfg.Workdir` exists and is canonical | `New` returns `(*Watcher, nil)` with the correct encoded path | Same. |
| `cfg.Workdir` exists with symlinks | `New` returns `(*Watcher, nil)` with the symlink-resolved encoded path | Same — `EncodeCwd`'s `canonicalisePath` resolves symlinks the same way. |
| `cfg.Workdir` does not exist | `New` returns `(nil, fmt.Errorf("tail: resolve workdir: %w", fs.ErrNotExist))` | `New` returns `(*Watcher, nil)`. `EncodeCwd` falls back to as-passed encoding. The watcher then `MkdirAll`s a directory under a (possibly wrong) encoded name, and `WaitForSessionJSONL` blocks until ctx-cancel (claude can never write to this wrong path). |

The shift is **acceptable** because the workdir's existence is **structurally guaranteed upstream** before `tail.New` is reached:

- `tail.New` is called from `ptyrunner.runner.go:361`, after `ptyrunner` has already invoked `cmd.Process` in PTY mode with `Dir: cfg.WorkDir` (see `pty.StartWithSize` in ptyrunner). A non-existent `Dir` causes `cmd.Start` to fail before `tail.New` is reached.
- No production caller of `tail.New` constructs the watcher without also chdir'ing-or-spawning into the same workdir.

The defence-in-depth check (ResolveWorkdir's eager fs.ErrNotExist) defended against a scenario the upstream caller already prevents. Per CLAUDE.md "Evidence-Based Fix Selection" — no observed failure mode warrants keeping this dual layer. The follow-up after `trust.go` migrates can also drop `ResolveWorkdir` entirely; until then it stays.

For `EncodeProjectDir`'s callers (e2e fixtures + ptyrunner test): all of them either (a) construct a workdir via `t.TempDir()` (existence guaranteed) or (b) immediately consume the path with `os.Open` / `MkdirAll`, where a wrong-encoded path surfaces as `os.ErrNotExist` at the consumption site. No site loses its error reporting; the error just lands one layer further out. The audit in the ticket body confirms no site pattern-matches on `fs.ErrNotExist` from `EncodeProjectDir` specifically — they pattern-match on the consumption-site error (`os.ErrNotExist` from `os.Open` in `resolveAndOpenJSONL`, etc.), which still fires.

`prompt_fidelity_test.go`'s `jsonlPathFor` previously had two failure sentinels: `<unresolved home: ...>` (UserHomeDir error) and `<unresolved encoding: ...>` (EncodeProjectDir error). After the switch, only the home-resolution arm remains reachable. The encoding arm is removed (along with the inner `if err != nil` block).

## Testing strategy

- **Unit-test invariants stay green via existing surface.** No new tests required for the production path — `tuidriver`'s own `cwd_test.go` already covers `canonicalisePath` rules on darwin + linux, the byte-substitution table, and the "input encoded as-passed on resolution failure" fallback. Re-asserting any of those in `pyrycode` would be drift-prone (the test would live across a package boundary from the implementation).
- **Delete the `TestEncodeProjectDir_*` block** (workdir_test.go:69-157). Five tests removed; each is now redundant with tui-driver's own coverage.
- **Keep all `TestResolveWorkdir_*` tests verbatim** (workdir_test.go:15-67). `ResolveWorkdir` continues to exist for `trust.go`.
- **`expectedEncodedDir` helper update in `watcher_test.go`** is the only test-helper change. It collapses from 9 lines to 3. The tests that depend on it (`TestNew_RealpathEncoding`, `TestWatcher_LateCreate`, `TestWatcher_ExistingFile`, and any others that call `expectedEncodedDir`) continue to assert the same end-to-end behaviour — the directory the watcher computes equals what the test's helper computes.
- **Regression coverage.** The end-to-end byte-equivalence test (`make e2e-realclaude`, #506) is the safety net: it exercises the full path of pyry calling real claude, claude writing a JSONL file, pyry reading it back. If the encoded path diverges from what claude writes, this test fails. Per ticket AC6.

Test scenarios to verify by walking each modified test:

- `internal/agentrun/workdir_test.go` — only `TestResolveWorkdir_*` remain; package-level imports trimmed; `go test ./internal/agentrun/...` passes.
- `internal/agentrun/jsonl/tail/watcher_test.go::TestNew_RealpathEncoding` — darwin-only; asserts the encoded directory base has the `-private-var-folders-` prefix (i.e. the symlink got resolved). Continues to pass because `EncodeCwd`'s `F_GETPATH` does the same resolution.
- `internal/agentrun/jsonl/tail/watcher_test.go::TestWatcher_LateCreate` / `TestWatcher_ExistingFile` — Linux-clean paths; helper computes the same directory the watcher writes to.
- `internal/agentrun/ptyrunner/runner_test.go::TestPtyRunner_*` — the table-driven helper-mode tests that exercise the JSONL write path. The fake helper child writes to `jsonlPath`; the watcher reads from the same path. Both sides now compute it via `tuidriver.SessionJSONLPath` (the test) / `tuidriver.SessionJSONLPath` inside `tail.New` (production). Symmetric by construction.
- `internal/e2e/realclaude/fixtures_test.go::TestResolveAndOpenJSONL_MissingFile` — asserts the missing-file error reports the resolved path. After the switch, the path comes from `tuidriver.SessionJSONLPath` instead of `agentrun.EncodeProjectDir`. The assertion's `wantPath` is computed via the same `tuidriver.SessionJSONLPath` in the test, so the equality continues to hold. The `errors.Is(err, os.ErrNotExist)` arm still fires (the error now originates from `os.Open` rather than `agentrun.ResolveWorkdir`).

## Open questions

- **Follow-up issue text.** This ticket leaves one issue worth filing: "agentrun: delete `ResolveWorkdir` after migrating `trust.go` off it". Body should reference (a) `trust.go`'s use as the projects-map key (different output shape than `EncodeCwd`); (b) the security-boundary audit needed because the modal-skip is workspace-trust gated; (c) candidate fix shapes — either tuidriver exposes `Canonicalise(p) (string, bool)` and `trust.go` calls it, or `trust.go` inlines `filepath.EvalSymlinks` locally with its own error semantics. The developer files this issue as part of this ticket's completion (a stub-creation step, not implementation work).

## Acceptance criteria — mapping

- [x] AC1 — option (a) selected; `EncodeProjectDir` deleted.
- [x] AC2 — `ResolveWorkdir` kept; sole caller becomes `trust.go`; follow-up issue filed (see "Open questions").
- [x] AC3 — `workdir_test.go` updates: `TestResolveWorkdir_*` kept; `TestEncodeProjectDir_*` deleted.
- [x] AC4 — all callers compile and pass; `agentrun` import dropped from each call site that no longer uses it; `tuidriver` import added where missing.
- [x] AC5 — `make check` green (covered by the developer's run).
- [x] AC6 — `make e2e-realclaude` green (covered by the developer's run).

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. The data crossing the boundary is `cfg.Workdir` — a value that originates from `pyry agent-run --workdir` (CLI-supplied) and flows into `ptyrunner.Config.WorkDir`, then into `tail.Config.Workdir`. Before this spec, the boundary was: untrusted CLI string → `agentrun.ResolveWorkdir` (filesystem-resolved) → `tuidriver.EncodeCwd` (encoded). After this spec, the resolve step inside `EncodeCwd` is the single boundary (single function, named type). No data becomes "more trusted" en route. The downstream consumer (`MkdirAll`, `WaitForSessionJSONL`) treats the result as a path to be created/polled, not as a privileged token.
- **[Tokens, secrets, credentials]** Not applicable. No tokens, secrets, or credentials touched by this spec. (The trust-marking helper in `trust.go` does touch `~/.claude.json` which can contain tokens — but `trust.go` is explicitly out of scope; the spec doesn't change its behaviour.)
- **[File operations]** Reviewed in detail:
  - **Path traversal.** The encoder `tuidriver.EncodeCwd` maps every byte outside `[a-zA-Z0-9]` to `'-'`, including `/` and `.`. There is no path-traversal sequence reachable through the encoded form — `../` becomes `----`, an inert directory name segment. `filepath.Join(home, ".claude", "projects", encoded, sid+".jsonl")` cannot escape `home/.claude/projects/` via the encoded component. SessionID is consumed verbatim but is `uuid.Parse`-validated upstream (per #501's caller audit; see `tuidriver/jsonl.go:35` doc comment "consumers do, e.g. via uuid.Parse"). The sole consumer in this spec, `tail.New`, also validates SessionID is non-empty at line 86-88. No filesystem boundary is bypassable through user-controlled workdir or sessionID via this spec's changes.
  - **TOCTOU.** Before: `ResolveWorkdir` did `os.Stat`-equivalent (`EvalSymlinks` performs the existence check) at watcher `New` time, then `tuidriver.EncodeCwd` did it again inside `SessionJSONLPath`, then `MkdirAll`. After: only `EncodeCwd`'s canonicalisation runs, then `MkdirAll`. The TOCTOU window between encode-time and MkdirAll-time is unchanged in shape (was always 2 syscalls apart, now 1 closer). No new check-then-use gap is introduced; the design has fewer races, not more.
  - **Symlink handling.** Before: `filepath.EvalSymlinks` (in `ResolveWorkdir`) resolves symlinks blindly, then `canonicalisePath` does it again. After: `canonicalisePath` alone. On darwin, `F_GETPATH` is the kernel-blessed canonical-path mechanism (used by `os.Getwd` itself) — it follows symlinks but does so via the fd, not the path string, which removes one path-vs-fd race versus `EvalSymlinks`. On linux, `EvalSymlinks` runs once instead of twice. No `O_NOFOLLOW` is needed at this layer — the canonical-form is what claude itself writes to (the JSONL path layout is dictated by claude, not by pyry), so following symlinks IS the correct semantics. A symlink-attack scenario would target `~/.claude/projects/<encoded>/` — but that directory is under `$HOME`, ownership controlled by the same uid running pyry, and the `MkdirAll(dir, 0o700)` enforces the canonical mode regardless of any pre-existing symlink with a different mode.
  - **Permissions.** `MkdirAll(dir, 0o700)` in `watcher.go:117` and `fixtures_test.go` is unchanged by this spec. Mode bits are not re-touched.
  - **Atomic writes.** Not applicable — no on-disk state is mutated by the changed code paths beyond `MkdirAll`, which is idempotent.
- **[Subprocess / external command execution]** Not applicable. No `exec.Command` invocations on the modified paths.
- **[Cryptographic primitives]** Not applicable.
- **[Network & I/O]** Not applicable — local filesystem only.
- **[Error messages, logs, telemetry]** Reviewed:
  - Error messages from the modified paths surface paths (`expectedPath`, `dir`, `home`-prefixed). No path passing through these helpers can contain tokens or secrets — workdir is a user-supplied directory name, sessionID is a UUID, home is the user's HOME. Same surface as before.
  - The package doc comment in `workdir.go` retains the `MUST NOT log file contents` invariant. Watcher.go's doc comment retains the same. No log-site changes proposed; no new log fields introduced.
- **[Concurrency]** No lock changes, no goroutine changes. `tail.New` is a synchronous constructor; the goroutine is owned by `Run`, not by `New`. The spec touches `New` only.
- **[Threat model alignment]** The threat surface here is local-uid filesystem semantics, not cross-process or relay. No `docs/protocol-mobile.md` threats touched. The relevant local-uid threat — "an attacker who can plant a symlink at `~/.claude/projects/<encoded>/<sid>.jsonl` could redirect pyry's read" — is already unmitigated by the existing design (same as on `main`); this spec neither widens nor narrows that surface. The `trust.go` boundary (workspace-trust modal) is left untouched; the security-sensitive label on this ticket reflects the **proximity** of the change to `trust.go`'s primitive (both functions live in `internal/agentrun`), not a direct mutation of `trust.go` itself. No MUST FIX.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-05-23
