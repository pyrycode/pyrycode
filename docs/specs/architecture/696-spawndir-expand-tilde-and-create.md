# Spec #696 — per-conversation scratch `Cwd`: expand `~` to `$HOME` and create the dir before spawn

**Ticket:** #696 (`size:s`, `security-sensitive`) — `fix(daemon): per-conversation scratch Cwd — expand ~ to $HOME and create the dir before spawn (#421 blocker)`

## Files to read first

- `cmd/pyry/main.go:420-503` — `confineWorkdirToHome` (strict, EvalSymlinks-both-sides confine, **left unchanged**), `withinDir` (boundary-aware containment, **reuse**), and `resolveSpawnDir` (the phone path being changed). This is the entire surface you edit on the production side.
- `cmd/pyry/main.go:537-554` — `runSupervisor`'s daemon-bootstrap confine→trust→spawn-in-realpath sequence. The *shared* `confineWorkdirToHome` caller you must **not** regress; daemon startup keeps rejecting a non-existent / `~`-literal `-pyry-workdir` exactly as today.
- `cmd/pyry/main.go:705-722` — `sessionMinter.Create`, the sole production caller of `resolveSpawnDir`. No change needed here; the new behaviour is entirely inside `resolveSpawnDir`.
- `cmd/pyry/conversation_spawndir_test.go` (whole file) — the adapter test file. `installRecordingTrustMark` stub + `t.Setenv("HOME", …)` pattern (non-parallel). You add the new `~`-expansion / create / symlinked-ancestor-not-created cases here, mirroring the 5 existing cases.
- `cmd/pyry/workdir_trust_test.go:128-173` — `TestConfineWorkdirToHome_CanonicalisesBothSides` + `_RejectsSymlinkEscapingHome`. Copy the symlinked-`$HOME` and symlink-escape construction idioms (`os.Symlink`, `filepath.EvalSymlinks` of the expected realpath) for the new tests.
- `internal/relay/handlers/create_conversation.go:36-51,170-179` — `msgCreateConversationCwdRejected` (static phone message, never echoes the path), `ErrSpawnDirRejected` sentinel, and the `errors.Is(err, ErrSpawnDirRejected)` → non-retryable `protocol.malformed` mapping. Confirms the rejection stays content-free end-to-end; **no change here**.
- `docs/knowledge/codebase/685.md` — the slice this reverses for the default-scratch case. § "Lessons learned" pins the confine→trust order, the "non-existent path is rejected" posture #696 deliberately relaxes, and the accepted confine→chdir TOCTOU window #696 must not widen.

## Context

The mobile rung-3 live e2e (pyrycode-mobile#421) hit two daemon bugs in the per-conversation spawn-dir resolution that `create_conversation` runs at `cmd/pyry/main.go:490` (`resolveSpawnDir` → `confineWorkdirToHome`). A phone cannot know the daemon's absolute home, so it sends the default `Cwd` as `~/.pyrycode/scratch` meaning "the daemon's home". Today:

1. **`~` is not expanded.** `confineWorkdirToHome` calls `filepath.Abs("~/.pyrycode/scratch")` → `<cwd>/~/.pyrycode/scratch`, which `lstat`s as a literal `~` segment under the process cwd and rejects.
2. **A missing path is rejected.** Even with `~` expanded, `confineWorkdirToHome` canonicalises with `EvalSymlinks`, which fails for a path that does not yet exist; nothing creates the scratch dir. A first-time user has no `~/.pyrycode`.

Both reject the conversation's `Cwd` with `protocol.malformed`, so the thread never opens. #696 makes the default-scratch `Cwd` resolve under `$HOME` and be created before spawn, **reversing #685's then-correct "a non-existent requested path is rejected" posture for this case** — confirmed live: a local build expanding `~` plus a manual `mkdir -p ~/.pyrycode/scratch` cleared both and #421 went green.

This is `security-sensitive` because creating a directory from a phone-supplied path adds a *write* primitive to the spawn-dir resolver, and the naive ordering the ticket body sketches has a symlinked-ancestor escape (see Design § "Security-load-bearing ordering").

## Design

### Scope decision: change the phone path only

`confineWorkdirToHome` is shared by the phone path (`resolveSpawnDir`) **and** daemon startup (`runSupervisor`, `main.go:547`). Only the phone path has the observed bug, and only the phone path receives a `~`-prefixed / not-yet-existing value (the operator's `-pyry-workdir` is shell-expanded and must already exist — a missing daemon workdir should still be a loud startup failure). **`confineWorkdirToHome` is left byte-for-byte unchanged.** All new behaviour lives in two new cmd-layer helpers consumed only by `resolveSpawnDir`. This keeps `confineWorkdirToHome`'s 8 callers (2 production, 6 test) and the daemon-startup contract untouched, and keeps the blast radius to one production file + one test file.

### Two new unexported helpers in `cmd/pyry/main.go`

Both sit beside `confineWorkdirToHome`/`withinDir` and reuse `withinDir`.

**1. `expandTilde(p string) (string, error)`** — minimal, leading-only home expansion.

- `p == "~"` → `os.UserHomeDir()` (the daemon's `$HOME`).
- `p` has prefix `"~/"` → `filepath.Join(home, p[2:])`.
- anything else (incl. `~user`, `~foo/bar`, an absolute path, a relative path) → returned **verbatim**, no expansion.
- `os.UserHomeDir()` failure → returned error (propagates as a rejection upstream).

No `$VAR` / arbitrary env expansion — out of scope, larger attack surface (ticket Technical Notes). `~user` is intentionally *not* resolved to another user's home; it passes through and fails the later confinement/existence check as a deterministic reject.

**2. `confineWorkdirToHomeCreating(workdir string) (string, error)`** — the create-aware confinement variant. Same return contract as `confineWorkdirToHome` (canonical realpath on success), but tolerates a not-yet-existing leaf/parents by canonicalising the **longest existing ancestor** and creating the rest *only after* the `$HOME` check passes. Algorithm (contract, not code):

1. Resolve `homeReal` = `EvalSymlinks(os.UserHomeDir())` (identical to `confineWorkdirToHome`).
2. `absWork = filepath.Abs(workdir)`.
3. Split `absWork` into `(existing, rest)` where `existing` is the longest leading ancestor that exists on disk by **`os.Lstat`** (see § "Why Lstat" — a symlink counts as existing and must be resolved, not stepped over) and `rest` is the remaining not-yet-existing suffix (`""` when the whole path exists).
4. `existingReal = EvalSymlinks(existing)` — fully resolves every symlink in the existing portion. (A dangling symlink at `existing` → `EvalSymlinks` errors → reject; correct.)
5. `candidate = filepath.Join(existingReal, rest)` — the canonical path the spawn would land in. The only unresolved part is `rest`, which does not exist yet, so it contains no symlinks.
6. **Containment check #1 (pre-creation):** `if !withinDir(homeReal, candidate)` → reject with the same content-free `"… resolves outside the home directory …"` error `confineWorkdirToHome` returns (names the resolved path + the `$HOME` boundary only — never file contents). **No directory is created on this path** (AC#3).
7. If `rest != ""`: `os.MkdirAll(candidate, 0o700)` (operator-private; matches the test fixtures' `0o700`). A MkdirAll failure (e.g. a file where a dir is expected) → reject.
8. **Containment check #2 (post-creation re-confine):** `final = EvalSymlinks(candidate)` (now exists) and `if !withinDir(homeReal, final)` → reject. This is the body's required "re-confine the path claude actually spawns in to `$HOME` *after* creation", and yields the canonical realpath to return.
9. return `final`.

When `rest == ""` (path already exists) this reduces exactly to `confineWorkdirToHome`: `existing == absWork`, `candidate == EvalSymlinks(absWork)`, no MkdirAll, `final == candidate`. So the AC#4 happy path (an existing within-`$HOME` `Cwd`) is byte-identical to today, and the 5 existing `resolveSpawnDir` tests pass unchanged (each has an existing or escaping path → `rest == ""`).

### `resolveSpawnDir` change

`resolveSpawnDir(requested string)` keeps its three-part contract (`""` → `("", nil)`, set → confine+trust, escape → `ErrSpawnDirRejected`, trustMark error → plain). The only change: between the empty-check and the trust-mark, it now expands `~` and uses the create-aware confiner:

```
requested == ""          → ("", nil)                        // unchanged; trustMark NOT called
expanded = expandTilde(requested)                            // new
realpath = confineWorkdirToHomeCreating(expanded)            // was confineWorkdirToHome(requested)
  err    → fmt.Errorf("%w: %v", handlers.ErrSpawnDirRejected, err)   // unchanged wrapping
trustMark(realpath) → return its return verbatim             // unchanged (AC: byte-identical cwd)
  err    → returned plain (retryable)                        // unchanged
```

`expandTilde`'s own error (UserHomeDir failure) is also wrapped as `ErrSpawnDirRejected` — a deterministic resolve failure, consistent with confine errors.

### Data flow

```
phone create_conversation {Cwd:"~/.pyrycode/scratch"}
  → handler forwards raw p.Cwd as spawnDir (unchanged, #685)
    → sessionMinter.Create → resolveSpawnDir(spawnDir)
        → expandTilde:  "~/.pyrycode/scratch" → "/Users/me/.pyrycode/scratch"
        → confineWorkdirToHomeCreating:
            longest existing ancestor "/Users/me" → EvalSymlinks → homeReal
            candidate = homeReal + "/.pyrycode/scratch"  (inside $HOME ✓)
            MkdirAll(candidate, 0700);  re-EvalSymlinks + re-confine → realpath
        → trustMark(realpath) → realpath
    → Pool.CreateIn(label, realpath)   // claude spawns in realpath, trust-marked, byte-identical
```

### Why `os.Lstat` for the ancestor walk (security-load-bearing)

The longest-existing-ancestor probe **must** use `os.Lstat`, not `os.Stat`. `os.Lstat` reports a symlink itself as existing (without following it), so a symlinked ancestor (`~/link -> /tmp/evil`) is selected as `existing`, then `EvalSymlinks` resolves it to `/tmp/evil`, and `candidate = /tmp/evil/<rest>` fails containment check #1 → rejected, **nothing created**. With `os.Stat` a dangling symlink would be reported not-existing, the walk would step over it, and a textual candidate under `$HOME` could pass check #1 before `MkdirAll` follows the symlink outside `$HOME`. `os.Lstat` forces every symlink in the existing portion through `EvalSymlinks` before the bound is applied — the same realpath-before-bound discipline `confineWorkdirToHome` already uses.

### Security-load-bearing ordering (the body's naive order has a gap)

The ticket body's naive "containment-check the `filepath.Abs` (un-symlink-resolved) path, then `MkdirAll`" is exploitable: `~/link -> /tmp/evil`, requested `~/link/scratch`, the textual path `~/link/scratch` *looks* inside `$HOME`, passes the check, and `MkdirAll` creates `/tmp/evil/scratch` outside `$HOME`. The fix is that **containment check #1 runs against the candidate built from the symlink-resolved ancestor** (`existingReal + rest`), not the textual abs path — so a symlinked ancestor is resolved out before the bound, and the dir is never created. Invariants, in order:

1. **Expand `~` first** (so the path is anchored at the real `$HOME`, never under the process cwd).
2. **Resolve symlinks in the existing portion, then confine, then create** — `MkdirAll` runs only after containment check #1 passes on the resolved candidate.
3. **Re-confine after creation** (`EvalSymlinks` the now-existing full path, re-check `$HOME`) — produces the realpath to trust-mark and catches a path that became escaping during creation.

The residual confine→chdir TOCTOU window is the same one #685 already accepts (an attacker who already controls the operator's `$HOME` can swap a suffix component to a symlink between check #2 and claude's `chdir`). `MkdirAll` does **not** widen it: containment check #1 gates creation on the deterministic (symlink-present-at-check-time) case, and check #2 re-confines the realpath before trust+spawn. Closing the residual race entirely (e.g. `openat2(RESOLVE_BENEATH)`) is out of scope and unobserved.

## Concurrency model

None added. `resolveSpawnDir` runs synchronously on the `create_conversation` handler's goroutine (one per request), the same as today. The two new helpers are pure-ish (filesystem reads + one `MkdirAll`); they hold no shared state and no locks. The filesystem itself is the only shared resource, and the TOCTOU discussion above bounds the only race.

## Error handling

| Failure | Result | Retryable? |
|---|---|---|
| `os.UserHomeDir()` fails (`expandTilde` or confiner) | `ErrSpawnDirRejected`-wrapped | No |
| candidate resolves outside `$HOME` (incl. symlinked ancestor) | `ErrSpawnDirRejected`-wrapped, **dir not created** | No |
| `EvalSymlinks` of a dangling existing ancestor fails | `ErrSpawnDirRejected`-wrapped | No |
| `MkdirAll` fails (file in the way, EACCES, etc.) | `ErrSpawnDirRejected`-wrapped | No |
| post-creation re-confine fails (raced symlink) | `ErrSpawnDirRejected`-wrapped | No |
| `trustMark` fails (transient `~/.claude.json` write) | plain error (unchanged) | Yes |

All confine/create failures map to `ErrSpawnDirRejected` → the handler's existing `protocol.malformed` + static `msgCreateConversationCwdRejected` reply (never echoes the path; the path appears only in daemon logs via `%v`). This matches #685's posture exactly — the only change is that a *non-existent default-scratch* path now succeeds-by-creating instead of rejecting.

**Note (deliberate, not a regression):** a transient `MkdirAll` failure (disk full, EACCES) is classified non-retryable, same as every other confine failure today. Distinguishing it would require error inspection out of this slice's lane; the phone re-issuing the same `Cwd` against a full disk failing identically is acceptable. See Open questions.

## Testing strategy

All new cases go in `cmd/pyry/conversation_spawndir_test.go`, reusing `installRecordingTrustMark` + `t.Setenv("HOME", …)` (non-parallel). The 5 existing cases are unchanged and must stay green (they exercise the `rest == ""` reduction). New scenarios (bullet form; developer writes the bodies in the existing idiom):

- **Bare `~` expands to `$HOME`** — `resolveSpawnDir("~")` with `HOME` = a temp dir → returns trustMark's sentinel; `trustMark` called once with `EvalSymlinks(home)` (the home realpath); no `<cwd>/~` literal anywhere. (AC#1)
- **`~/`-prefixed missing path is expanded, created, and resolved** — `HOME` = temp dir with **no** `.pyrycode`; `resolveSpawnDir("~/.pyrycode/scratch")` → returns the trustMark sentinel; the resolved realpath (the arg trustMark received) **exists and is a directory** (`os.Stat` → `IsDir()`), lies under `EvalSymlinks(home)`, and equals `EvalSymlinks(home)/.pyrycode/scratch`; `trustMark` called once. (AC#1 + AC#2)
- **Symlinked ancestor escaping `$HOME` is rejected and the target is NOT created** — under `HOME` create `link -> outside` (a sibling temp dir); `resolveSpawnDir("~/link/scratch")` (suffix does not exist) → `errors.Is(err, ErrSpawnDirRejected)`; `trustMark` called **0** times; **and** `outside/scratch` was never created (`os.Stat` returns `IsNotExist`). This is the key new security regression guard for the body's escape gap. (AC#3)
- **Deep missing chain creates all parents** — `HOME` = temp dir; `resolveSpawnDir("~/a/b/c")` with none of `a/b/c` existing → resolves, creates the full chain `0o700`, returns the realpath. Guards the longest-existing-ancestor walk past one level.
- **Within-`$HOME` existing dir is unchanged (AC#4 explicit)** — the existing `TestResolveSpawnDir_WithinHome_TrustsRealpath` already covers this; no new test, but verify it stays green (it exercises `rest == ""`).
- *(optional)* **`~user` is not expanded** — `resolveSpawnDir("~root/x")` → rejected (`ErrSpawnDirRejected`), proving expansion is leading-`~/`-and-bare-`~` only and `~user` is treated as a literal segment that fails resolution. Pins the minimal-expansion boundary.

Run `go test -race ./cmd/pyry/...` and `go vet ./...`. The symlinked-ancestor test must run on both Linux and macOS (macOS `/var`→`/private/var` is already handled by EvalSymlinks-both-sides). No PTY/TTY dependency.

## Open questions

- **MkdirAll-failure retryability.** Currently lumped non-retryable (deterministic, matches existing confine errors). If a future operator UX wants "transient FS error → retry", split a retryable branch — but that needs the handler to distinguish, out of this slice. Left as-is; flagged for code-review awareness, not a blocker.
- **Recording the realpath vs raw `Cwd` in the conversation row.** Unchanged from #685 — the row still records the raw/defaulted `cwd`, not the created realpath. No consumer needs the row to equal claude's actual cwd; deferred (same as #685's out-of-scope note).

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** The single untrusted→trusted boundary is `resolveSpawnDir` (cmd-layer), unchanged in location from #685. The phone-supplied `Cwd` crosses from untrusted (a raw string forwarded verbatim by `internal/relay/handlers`, which never touches the path) to trusted (a canonical, `$HOME`-confined, trust-marked realpath) entirely inside `resolveSpawnDir` → `confineWorkdirToHomeCreating`. Downstream (`Pool.CreateIn`) receives only the resolved realpath. #696 adds a *write* (`MkdirAll`) at this boundary but does not move or scatter it. No finding.
- **[File operations — path traversal]** MUST-have, addressed in Design: the candidate is built from the **symlink-resolved** longest existing ancestor (`os.Lstat` walk + `EvalSymlinks`), and containment check #1 runs **before** `MkdirAll`. The body's naive "abs-text check then MkdirAll" escape (`~/link -> /tmp/evil`) is closed and has a dedicated regression test (symlinked-ancestor-not-created). No literal `~` ever reaches a path because `expandTilde` anchors at `$HOME` first. No finding.
- **[File operations — TOCTOU]** The confine→`MkdirAll`→re-confine→chdir window is the same residual one #685 documents and accepts; `MkdirAll` does not widen it (creation is gated on check #1; check #2 re-confines the realpath). Attacker must already control `$HOME`. Closing it fully (`openat2`/`RESOLVE_BENEATH`) is OUT OF SCOPE and unobserved — named in Design. No MUST FIX.
- **[File operations — permissions]** Created dirs are `0o700` (operator-private), matching the test fixtures and the daemon-private nature of `~/.pyrycode/scratch`. Explicit in Design step 7. No finding.
- **[File operations — symlink handling]** Symlinks in the existing portion are resolved (`EvalSymlinks`) before the bound; the not-yet-existing suffix contains no symlinks at check time. `os.Lstat` (not `os.Stat`) is mandated so a symlinked ancestor cannot be stepped over. No finding.
- **[Subprocess / external command execution]** No new subprocess and no new argument flows from this change; the resolved realpath feeds `Pool.CreateIn` → `supervisor.Config.WorkDir` as before. `expandTilde` does **not** shell-interpret (no `~user`, no `$VAR`) — it is a pure string/`filepath.Join` operation, so no shell-expansion attack surface. No finding.
- **[Cryptographic primitives]** N/A — no randomness, hashing, or key material in this change.
- **[Network & I/O]** N/A — no new socket reads, no size limits relevant; the `Cwd` string is already bounded by the existing `create_conversation` frame limits upstream (unchanged).
- **[Error messages, logs, telemetry]** Rejections map to `ErrSpawnDirRejected` → the existing static `msgCreateConversationCwdRejected` reply, which never echoes the path or `~/.claude.json`. The resolved path appears only in daemon-side logs (via `%v` in the wrap), identical to #685. No new field leaks. No finding.
- **[Concurrency]** No new goroutines, no new shared state, no locks. The only shared resource is the filesystem; the single race is the TOCTOU window bounded above. No finding.
- **[Threat model alignment]** ADR-025 mobile-remote-head: the phone-chosen workdir keeps the identical `$HOME`-confinement + trust-pre-mark posture as the daemon's own bootstrap workdir; #696 only relaxes the "must pre-exist" constraint for the default-scratch case by creating-within-`$HOME`, which does not widen the trust boundary (creation is `$HOME`-gated). No relay/protocol vocabulary change. No finding.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-06-19
