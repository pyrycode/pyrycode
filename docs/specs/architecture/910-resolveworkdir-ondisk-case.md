# 910 — `ResolveWorkdir` canonicalises each path component to its on-disk case

## Files to read first

- `internal/agentrun/workdir.go:15-33` — the function to change. Current body is `filepath.Abs` → `filepath.EvalSymlinks`; the doc comment (line 15-22) claims to "mirror how claude resolves a workdir" but omits case. This is the **only** production file the spec modifies, and the doc comment is AC-4's target.
- `internal/agentrun/workdir_test.go:11-74` — existing tests. Reuse the `t.TempDir()` fixture shape, the `runtime.GOOS` skip pattern (line 12), and the `errors.Is(err, fs.ErrNotExist)` assertion (line 71). AC-5's new test lands here; the four existing tests must still pass unchanged (AC-3).
- `internal/agentrun/trust/trust.go:50-54` and `:100-101` — the **sole production caller**. `markWorkdirTrustedIn` calls `agentrun.ResolveWorkdir(workdir)` (line 51) and writes the result as `projects[realpath]` (line 101). Confirms: fixing `ResolveWorkdir`'s output fixes the trust-map key.
- `cmd/pyry/agent_run.go:299-330` — `runAgentRunPty`. `realpath, err := trustMark(parsed.workdir)` (line 300); that same `realpath` is the trust key **and** `ptyrunner.Config.WorkDir` (line 318, the spawn cwd). One source for both.
- `cmd/pyry/main.go:627-644` — `resolveSpawnDir` (per-conversation daemon flow). `confineWorkdirToHomeCreating` → `trustMark(realpath)` → returns trustMark's realpath (line 643). So the spawn cwd here is also `ResolveWorkdir`'s output.
- `cmd/pyry/main.go:678-695` — daemon bootstrap. `confineWorkdirToHome(*workdir)` → `trustMark(workdirReal)` → `trustedWorkdir` threaded into `Bootstrap.WorkDir`. Same trustMark-return-is-spawn-cwd pattern (comment at line 684-687 states the byte-identical invariant).
- `cmd/pyry/main.go:442-490` — `confineWorkdirToHome` + `withinDir`. The `$HOME`-confinement invariant the security review must confirm is **not** weakened. These helpers are **unchanged** by this ticket; the spec explains why (§ "Why the confine helpers need no change").
- `internal/agentrun/ptyrunner/runner.go:64-67` — `ErrTrustModalDetected`. This is the abort AC-2 prevents: when the trust key misses, claude renders the trust modal, `WaitReady` classifies it (`ready.TrustModal`), and `Run` aborts with this sentinel.
- `docs/specs/architecture/341-agentrun-trust-helper.md` — the original `ResolveWorkdir` + `MarkWorkdirTrusted` spec. Establishes the "mirror claude's path resolution" contract this ticket finally honours, and the security-review section format.

## Context

`ResolveWorkdir` is the canonicaliser that turns a configured workdir into the key claude reads from `~/.claude.json`'s `projects` map (via `trust.MarkWorkdirTrusted`) and into the child's spawn cwd. Its doc comment promises to "mirror how claude resolves a workdir before reading `~/.claude.json`'s projects map," but the body only runs `filepath.Abs` + `filepath.EvalSymlinks`.

On a **case-insensitive filesystem** (default macOS APFS), `filepath.EvalSymlinks` preserves the **input** case of any component that is not itself a symlink — it `lstat`s each component but never reads the parent directory to learn the true on-disk spelling. Claude, by contrast, canonicalises its cwd to the **on-disk** case before the trust lookup. So a workdir configured with the wrong case (the real 2026-05-29 incident on #208: `.../WorkSpace` configured in an agents-fork `.env`, `.../Workspace` on disk) makes `MarkWorkdirTrusted` pre-mark `projects[<wrong-case>]` — a key claude never reads. Claude then renders the trust-folder modal; `ptyrunner.Run`'s startup `WaitReady` classifies it and aborts with `ErrTrustModalDetected`. Every dispatch on that fork parks until an operator diagnoses a path-case typo.

The 2026-05-29 instance was fixed by correcting the `.env`; the robust code fix was never built. Any future fork, workspace move, or `change_workspace` target with mismatched case reproduces the same hard failure.

**Fix, in one sentence:** after `EvalSymlinks`, walk the resolved path component-by-component and replace each component with its actual on-disk spelling, so the returned path (and therefore the trust key and the spawn cwd) matches claude's canonicalised cwd.

## Design

### Why a single-file fix covers every flow

`ResolveWorkdir`'s output reaches claude's trust lookup through exactly one production caller — `trust.MarkWorkdirTrusted` — and **all three** spawn flows route the child's cwd through `MarkWorkdirTrusted`'s return value:

| Flow | Entry | Trust key | Spawn cwd |
|------|-------|-----------|-----------|
| `agent-run` (dispatcher) | `runAgentRunPty` | `trustMark(parsed.workdir)` → `ResolveWorkdir` | same `realpath` → `ptyrunner.Config.WorkDir` |
| daemon bootstrap | `runSupervisor` | `trustMark(workdirReal)` → `ResolveWorkdir` | `trustedWorkdir` → `Bootstrap.WorkDir` |
| per-conversation | `resolveSpawnDir` | `trustMark(realpath)` → `ResolveWorkdir` | returns trustMark's realpath |

In every row the spawn cwd **is** `ResolveWorkdir`'s output. So fixing `ResolveWorkdir` to emit on-disk case fixes the trust key and the spawn cwd together, in all three flows, with no consumer edits. `codegraph_callers ResolveWorkdir` confirms one production caller (`trust`) plus four test callers — no fan-out.

### Why the confine helpers need no change

The two daemon flows resolve the path **twice**: `confineWorkdirToHome{,Creating}` (which uses `EvalSymlinks`, still case-preserving) produces a possibly-wrong-cased realpath, then `trustMark` re-resolves it through the fixed `ResolveWorkdir` and returns the on-disk-cased result — and that result is what becomes the spawn cwd (see the table). The wrong-cased intermediate from the confine step is discarded. So:

- The confine helpers keep their `EvalSymlinks`-based `$HOME`-containment check exactly as today. Case-canonicalisation only re-spells components of the **same physical directory** (a case-insensitive fs maps `.../WorkSpace` and `.../Workspace` to one inode), so it cannot move the path out of `$HOME` after confinement passed. On a case-sensitive fs, `ResolveWorkdir`'s pre-existing `EvalSymlinks` already errors on a wrong-cased input before canonicalisation runs (see § "Error handling"), so there is nothing to re-spell.
- Leaving the confine helpers untouched keeps the change to one production file and avoids re-auditing the `$HOME`-confinement code. The security review (below) confirms the invariant is preserved rather than modified.

### `ResolveWorkdir` — new body shape

Signature unchanged: `func ResolveWorkdir(workdir string) (string, error)`. After the existing `Abs` + `EvalSymlinks` steps, pass the resolved path through a new unexported best-effort helper:

```go
resolved, err := filepath.EvalSymlinks(abs)   // existing
if err != nil { ... }                          // existing
return canonicalCase(resolved), nil            // new
```

`canonicalCase(path string) string` — walks `path` from the filesystem root, rebuilding it with each component's on-disk spelling. **Never returns an error and never fails the call**: `path` is the output of a successful `EvalSymlinks`, so it is already absolute, symlink-free, and confirmed to exist. `canonicalCase` only *improves the casing*; any obstacle (an unreadable ancestor, an ambiguous match) degrades to keeping the input component verbatim — never worse than today's behaviour.

Per-component rule (in a helper `matchEntry(entries []os.DirEntry, target string) string`):

1. **Exact-case entry present** → use it. This is the fast path and the no-regression guarantee: a correctly-cased component (any fs) returns unchanged, and it is what makes case-sensitive-fs sibling-safety structural — an exact match is always preferred over any case-fold.
2. **No exact match, exactly one `strings.EqualFold` match** → use that entry's on-disk name. This is the case-insensitive-fs wrong-case fix (a case-insensitive fs cannot hold two names differing only by case, so the match is unique).
3. **Zero matches, or ≥2 fold-matches with no exact** → keep `target` unchanged (best-effort degradation; the ≥2 case is unreachable when `EvalSymlinks` succeeded but is handled defensively so an arbitrary sibling is never chosen).

Walk skeleton (contract, not final code):

- Split the absolute `path` into components. Seed `canonical` with the root (`string(os.PathSeparator)`; Windows is out of scope per CLAUDE.md, so no volume handling).
- For each component: `os.ReadDir(canonical)`. **On read error, append the original component and continue** (documented best-effort: the path is already validated by `EvalSymlinks`; we merely could not learn the canonical casing here — no new failure mode, no silent-swallow of a *behavioural* error). Otherwise `canonical = filepath.Join(canonical, matchEntry(entries, component))`.
- Return `canonical`.

No symlink can be re-introduced: `EvalSymlinks` already guaranteed every component of `path` is a non-symlink directory, and on a case-insensitive fs the unique fold-match is that same physical (non-symlink) object. The result stays symlink-free.

### Doc comment (AC-4)

Rewrite the `ResolveWorkdir` doc comment so it describes what the code does: resolves relative paths against the cwd (`Abs`), resolves symlinks (`EvalSymlinks`, macOS `/var → /private/var`), **and** canonicalises each path component to its on-disk case so a case-insensitive-filesystem workdir whose configured case differs from disk resolves to the spelling claude derives from its canonicalised cwd. Keep the "sole caller: `internal/agentrun/trust`" note and the `fs.ErrNotExist` wrap note. Drop the promise-vs-reality gap.

## Concurrency model

None. `ResolveWorkdir` and `canonicalCase` are pure, synchronous functions — no goroutines, no shared state, no `context.Context` (unchanged from today; the walk is a bounded sequence of `os.ReadDir` calls over the workdir's ancestors, a handful of directory reads done once at spawn). Concurrent callers are independent.

## Error handling

- **Existing contract preserved.** The only errors `ResolveWorkdir` returns remain the `Abs` and `EvalSymlinks` wraps (`agentrun: resolve workdir %q: %w`), and a missing path still surfaces `fs.ErrNotExist` through the `EvalSymlinks` wrap. `canonicalCase` adds **no** new error path — AC-3's "no regression for correctly-cased inputs" depends on this.
- **Case-sensitive fs, wrong-case input:** `EvalSymlinks` errors (the wrong-cased path does not exist), so `ResolveWorkdir` returns the `fs.ErrNotExist`-wrapped error before `canonicalCase` runs. It never resolves to a differently-cased sibling — the sibling-safety property the ticket's Technical Notes require.
- **Best-effort casing degradation:** an `os.ReadDir` failure on an ancestor (e.g. an execute-only `0711` directory that is searchable by `EvalSymlinks` but not readable) is deliberately not propagated — `canonicalCase` keeps the input component and continues. Documented at the call site with a comment (per CODING-STYLE "document why you ignore an error"): the path is already validated; only the *casing improvement* is skipped. `ResolveWorkdir` has no injected logger, so there is nothing to log — and the package doc-comment forbids logging file contents regardless.

## Testing strategy

`internal/agentrun/workdir_test.go`, same-package, table/scenario style, stdlib `testing`, `t.TempDir()` fixtures. Keep the four existing tests. Add:

- **Case-mismatch folds to on-disk case (AC-5, AC-1)** — *case-insensitive fs only*. Create a real dir `<tmp>/Workspace`. Call `ResolveWorkdir(<tmp>/WorkSpace)` (wrong case). Assert the result's final component is `Workspace`, not `WorkSpace`. Gate at **runtime**, not on `runtime.GOOS`: create `Workspace`, then `os.Stat(<tmp>/WORKSPACE)` — if it fails, the fs is case-sensitive → `t.Skip`. (macOS APFS can be case-sensitive; a runtime probe is the robust gate, unlike the existing darwin test's `GOOS` skip.)
- **Trust key equals resolved path (AC-2)** — reuse the case-mismatch fixture: after `MarkWorkdirTrusted`-style resolution, the key that would be written equals `ResolveWorkdir`'s on-disk-cased output. This can assert at the `ResolveWorkdir` layer (the key *is* its return value per `trust.go:51,101`); a full `trust` round-trip is optional and belongs in `internal/agentrun/trust` if the developer wants defence-in-depth. Same case-insensitive-fs runtime gate.
- **Correctly-cased mixed-case input unchanged (AC-3)** — *any fs*. Create `<tmp>/MixedCaseDir`. `ResolveWorkdir(<tmp>/MixedCaseDir)` returns the same path (modulo the symlink resolution the existing darwin test already covers). Proves canonicalisation does not mangle a correctly-cased component.
- **Sibling-safety on case-sensitive fs (security property)** — *case-sensitive fs only*. Create both `<tmp>/Workspace` and `<tmp>/workspace` (two distinct dirs). `ResolveWorkdir(<tmp>/Workspace)` returns `.../Workspace`, never `.../workspace`. Runtime-gate: attempt both creates; if the second collapses onto the first (case-insensitive fs), `t.Skip`. Pins the exact-match-first rule.
- Existing `TestResolveWorkdir_DarwinRealpath`, `_AlreadyResolved`, `_RelativePath`, `_MissingPath` remain green (AC-3 regression guard).

Describe scenarios as inputs + expected output; write the assertions in the project's test idiom.

## Open questions

- **Best-effort vs strict on `ReadDir` error.** Spec chooses best-effort (keep input component) so the new step can never introduce a failure the old code didn't have — airtight for AC-3. The only downside is that an unreadable-ancestor workdir keeps its input casing (i.e. the bug persists *for that exotic setup only*), which is strictly no worse than today. If a future incident shows a legit workdir with an execute-only-but-not-readable ancestor hitting the case bug, revisit with a platform-specific canonicaliser (macOS `F_GETPATH`) — a separate ticket, not this one.
- **Pure-Go directory walk vs `F_GETPATH`.** Spec uses the stdlib directory walk (no build tags, no new dep, trivially auditable, uniform across Linux + macOS). macOS `fcntl(F_GETPATH)` would need `golang.org/x/sys/unix` + a darwin/linux build-tag split (more files, and a no-op on case-sensitive Linux). Rejected on the project's "stdlib first / simplicity first" principle. Recorded here in case a reviewer wonders why the obvious macOS syscall wasn't used.

## Security review

**Verdict:** PASS

**Scope:** the workdir-trust cluster — `ResolveWorkdir` (changed) and, by reference, the `$HOME`-confinement helpers it feeds (`confineWorkdirToHome{,Creating}`, `withinDir`; unchanged). Per the `security-sensitive` label and the incident note, the audit centres on (a) not resolving to an unintended sibling on a case-sensitive fs, and (b) preserving the `$HOME`-confinement invariant.

**Findings:**

- **[Trust boundaries]** `ResolveWorkdir`'s `workdir` argument is caller-supplied. In the `agent-run` flow it originates from the dispatcher's own `--workdir` (a trusted local worktree, no `$HOME` bound by design — the dispatcher owns the path). In the two daemon flows the phone-supplied path first passes `confineWorkdirToHome{,Creating}`'s `$HOME`-containment check **before** reaching `trustMark`/`ResolveWorkdir`. This ticket does not move that boundary: canonicalisation runs *inside* `trustMark`, after confinement, on the already-confined physical directory.
- **[Sibling resolution on a case-sensitive fs]** The named risk. Neutralised by two independent mechanisms: (1) the pre-existing `EvalSymlinks` errors on a wrong-cased input on a case-sensitive fs, so `canonicalCase` never runs on it; (2) even if reached, `matchEntry` prefers an **exact-case** entry over any fold and folds only on a **unique** `EqualFold` match with no exact match — a case-sensitive fs holding both `Workspace` and `workspace` returns the exact one. The sibling-safety test pins mechanism (2); the `_MissingPath`/case-sensitive-fs behaviour pins (1).
- **[`$HOME`-confinement preservation]** The daemon flows' `$HOME` check (`main.go:472` / `main.go:579,596`, via `withinDir`) is unchanged and still runs on the `EvalSymlinks` output before `trustMark`. Case-canonicalisation only re-spells components of the same inode on a case-insensitive fs, so the post-confinement path is the same physical location — it cannot escape `$HOME` that confinement already accepted. On a case-sensitive fs canonicalisation is a no-op (exact match). No re-audit of the confinement code is needed because it is not touched.
- **[Symlink re-introduction]** None. `canonicalCase` consumes the `EvalSymlinks` output (symlink-free); on a case-insensitive fs the unique fold-match is the same non-symlink object; the result stays symlink-free. No new traversal via a re-introduced symlink.
- **[File operations]** `os.ReadDir` on the workdir's ancestors — read-only, no writes, no creation, no `os.Rename`. Requires read permission on ancestor dirs; a denial degrades to best-effort (keep input case), never a crash or an escape. No TOCTOU of security consequence: the reads only *label* an already-validated, already-confined path; they never re-decide containment.
- **[Tokens, secrets, credentials]** N/A. `ResolveWorkdir` touches directory names only, never file contents; `~/.claude.json` is not read here (that is `trust.MarkWorkdirTrusted`, unchanged, which already forbids logging contents).
- **[Error messages, logs, telemetry]** No new logging. Error wraps still name only the step and the workdir path (derivable, non-sensitive). Directory-entry names are not emitted.
- **[Subprocess / crypto / network]** N/A — none introduced.
- **[Resource exhaustion]** The walk is bounded by the workdir's path depth (a handful of `ReadDir`s, once per spawn). A pathologically deep path is the caller's own directory; same-uid, same trust boundary. No cap needed.

**MUST FIX in spec (resolved inline before this verdict):**

- Exact-match-first in `matchEntry` is **mandatory**, not an optimisation — it is the case-sensitive-fs sibling-safety guarantee. Pinned by the sibling-safety test (§ "Testing strategy").
- Fold only on a **unique** `EqualFold` match; ≥2 matches with no exact match must keep the input component (never choose an arbitrary sibling). Pinned in the per-component rule (§ "ResolveWorkdir — new body shape", rule 3).

**Reviewer:** architect (self-review per `agents/architect/security-review.md`)
**Date:** 2026-07-10
