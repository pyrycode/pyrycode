# Spec: `internal/update` — atomic in-place binary replace (#187)

## Files to read first

- `internal/sessions/registry.go:53-92` — `saveRegistryLocked`. The repo's existing temp-file + fsync + rename pattern. Mirror its structure (CreateTemp in same dir → Chmod → write → Sync → Close → Rename, with a `defer` that best-effort-removes the temp path so error paths leave nothing behind). Differences from this ticket: registry creates the parent dir with `MkdirAll` (we don't — see "Out of scope") and writes via a JSON encoder (we write raw bytes).
- `internal/update/checksum.go` — sister file in the same package. Confirms the package doc comment (already on `version.go`, no need to repeat), the error-sentinel + wrapping convention (`var ErrFoo = errors.New(...)`, returned as `fmt.Errorf("doing X: %w", ErrFoo)`), and that `internal/update` is stdlib-only.
- `internal/update/checksum_test.go` — table-driven test layout (`t.Parallel()` per subtest, inline assertions, `errors.Is` for sentinel errors, no helpers, no `testdata/`). Mirror exactly.
- `internal/update/restart.go` — sister file landed in #181. Confirms the "pure helper, no wiring" shape this ticket follows.
- `CODING-STYLE.md` §§ Naming, Error Handling, Testing — already followed by `version.go` / `checksum.go` / `restart.go`. Cited so a fresh reader doesn't have to re-derive.

(QMD search on `pyrycode-docs` for "atomic replace" / "fsync rename" returns only `internal/sessions/registry.go`'s pattern, already cited above. No prior ADR.)

## Context

Third I/O slice of the `pyry update` work, sibling to:

- #186 — in-memory tar extraction → produces `newData []byte` (the new pyry binary's bytes)
- #181 — restart-command detection (already merged)
- #182 — HTTP fetcher (in flight on PR #193, no file overlap)

The wiring ticket will compose: fetch → verify checksum → extract bytes → **`AtomicReplace(installedPath, newBytes, 0o755)`** → run restart command. This ticket lands the replace primitive only.

A naive `os.WriteFile` over a live binary is unsafe in two ways:

1. **Truncation window.** `os.WriteFile` calls `os.Create` (which truncates), then writes. A SIGKILL between truncate and the final `Write` returning leaves the user with a 0-byte or partial executable. Their next `pyry` invocation fails with `exec format error`.
2. **Concurrent execution.** On Linux the kernel returns `ETXTBSY` if you try to write to a file that's currently mmap'd for execution as a binary; `os.WriteFile` would fail mid-update with no rollback. macOS has weaker semantics but the same hazard in spirit.

Both are eliminated by the standard POSIX-safe pattern: write a fresh file in the same directory, fsync, then `os.Rename` over the target. `rename(2)` on a same-filesystem path is atomic — observers see either the old inode or the new inode, never a partial file. On Linux it also handles the "target is currently executing" case cleanly: the rename unlinks the old inode while leaving its open-for-execution refcount intact, so running processes keep their old binary mapped and the on-disk path now points to the new one.

The same-filesystem caveat is non-negotiable: `rename(2)` across mount points returns `EXDEV` and is not atomic (it would degrade to copy-then-unlink). Creating the temp file in `filepath.Dir(targetPath)` makes this structurally impossible.

This slice was deliberately split from #186 (in-memory tar extraction) so the fsync/rename concerns are isolated, file-level non-overlapping, and either child can land first. They share zero file paths.

## Design

### Package

New file inside the existing `internal/update` package:

```
internal/update/
  version.go           // (existing — #179)
  version_test.go      // (existing — #179)
  checksum.go          // (existing — #180)
  checksum_test.go     // (existing — #180)
  restart.go           // (existing — #181)
  restart_test.go      // (existing — #181)
  replace.go           // NEW — AtomicReplace
  replace_test.go      // NEW — table + targeted subtests
```

No package-doc comment in `replace.go`: `version.go`'s package doc (*"Package update implements pyrycode's self-update logic: release manifest parsing, version comparison, fetch, and replace."*) already names the replace half. Don't duplicate.

### Function signature

```go
// AtomicReplace overwrites targetPath with newData using the standard POSIX
// write-temp + fsync + rename dance, so an interruption mid-write leaves
// targetPath pointing at either the old contents or the new contents — never
// a truncated file.
//
// The new file is created with the supplied mode (after umask, applied via an
// explicit Chmod since os.CreateTemp opens at 0o600). targetPath does not
// need to exist beforehand; if it does, it is replaced in place.
//
// SAME-FILESYSTEM CAVEAT: rename(2) is only atomic when source and destination
// are on the same filesystem. AtomicReplace creates its temp file in
// filepath.Dir(targetPath) precisely so the eventual rename never crosses a
// mount point. Callers MUST therefore pass an absolute (or working-directory-
// relative) path whose parent directory already exists and is writable;
// AtomicReplace does not MkdirAll the parent.
//
// On any error path before the successful rename, the temp file is removed so
// no .pyry-*.tmp stragglers accumulate in the install directory.
func AtomicReplace(targetPath string, newData []byte, mode os.FileMode) error
```

### Implementation sketch

```go
func AtomicReplace(targetPath string, newData []byte, mode os.FileMode) error {
    dir := filepath.Dir(targetPath)
    base := filepath.Base(targetPath)

    f, err := os.CreateTemp(dir, "."+base+".*.tmp")
    if err != nil {
        return fmt.Errorf("atomic replace: create temp in %s: %w", dir, err)
    }
    tmp := f.Name()

    // Best-effort cleanup on any error path. After a successful os.Rename the
    // temp name no longer exists, so this Remove is a no-op — harmless.
    renamed := false
    defer func() {
        if !renamed {
            _ = os.Remove(tmp)
        }
    }()

    if _, err := f.Write(newData); err != nil {
        _ = f.Close()
        return fmt.Errorf("atomic replace: write temp: %w", err)
    }
    if err := f.Chmod(mode); err != nil {
        _ = f.Close()
        return fmt.Errorf("atomic replace: chmod temp: %w", err)
    }
    if err := f.Sync(); err != nil {
        _ = f.Close()
        return fmt.Errorf("atomic replace: fsync temp: %w", err)
    }
    if err := f.Close(); err != nil {
        return fmt.Errorf("atomic replace: close temp: %w", err)
    }
    if err := os.Rename(tmp, targetPath); err != nil {
        return fmt.Errorf("atomic replace: rename %s -> %s: %w", tmp, targetPath, err)
    }
    renamed = true
    return nil
}
```

Notes:

- **Temp name pattern `"." + base + ".*.tmp"`.** The leading dot keeps stragglers (which only appear in failure modes) hidden from `ls`. Embedding `base` makes accidental collisions during concurrent updates structurally impossible and gives ops a hint about origin if one ever does leak. `os.CreateTemp` substitutes a random suffix for `*`.
- **Chmod via `f.Chmod`, not `os.Chmod`.** Operating on the open file descriptor avoids a TOCTOU between create and chmod (no other process can swap the file underneath us between the two calls because we hold the fd). Equivalent to `fchmod(2)`.
- **Chmod *before* fsync, after write.** Order: write contents → chmod → fsync → close → rename. Chmod-then-fsync ensures the mode change is durably persisted along with the contents in the same fsync. (The reverse — fsync-then-chmod — would leave the mode change un-fsynced, a needless durability gap.)
- **`renamed` flag controls cleanup.** After `os.Rename` succeeds, `tmp` no longer names a file; calling `os.Remove(tmp)` would either fail with `ENOENT` (harmless) or, in a freak race, remove a *different* file that some unrelated process subsequently created at the same temp name. The flag closes that gate cheaply.
- **Each f-method failure closes the file before returning.** Standard belt-and-suspenders against fd leaks; `defer f.Close()` would also work but the explicit closes make the order with respect to the cleanup `defer` unambiguous.
- **Stdlib only** (`os`, `path/filepath`, `fmt`). Per AC.

### What we deliberately don't do

- **Parent-directory fsync.** A purist's answer would `open(dir, O_RDONLY)` + `fsync` after the rename to make the directory entry crash-durable. The existing `saveRegistryLocked` doesn't do this either, and the AC frames the threat model as "process interrupted mid-write" (signal/SIGKILL) — not power loss. Adding a directory fsync now would diverge from the in-tree pattern without an observed failure. Logged as an open question.
- **`os.MkdirAll` on the parent.** The wiring ticket guarantees the install directory already exists (it's where `pyry` is currently running from). Creating it here would mask a configuration bug in the caller. Documented in the function doc; tested by the "parent missing" error case.
- **Backup / rollback of the old binary.** Out of scope per ticket body — left to the wiring layer, which can `AtomicReplace` to a `.bak` path first if it wants belt-and-suspenders rollback.
- **Sentinel errors.** None of the AC's error cases need `errors.Is`-style branching — the wiring ticket will log-and-fail uniformly. Adding sentinels speculatively violates "Don't add ... for scenarios that can't happen". If a future caller needs to branch on, say, "parent dir missing" specifically, they can `errors.Is(err, fs.ErrNotExist)` against the wrapped `*PathError` from `CreateTemp`.

### Data flow

```
[wiring ticket: extract pyry binary from tar.gz] ──► newData []byte
                                                          │
                                                          ▼
                       AtomicReplace(installPath, newData, 0o755)
                                                          │
                  ┌───────────────────────────────────────┤
                  │ same dir as installPath               │
                  ▼                                       │
            os.CreateTemp(.pyry.*.tmp)                    │
                  │                                       │
                  ▼                                       │
            Write(newData) → Chmod(0o755) → Sync → Close  │
                  │                                       │
                  ▼                                       │
            os.Rename(.pyry.XXX.tmp → pyry)  ◄── atomic ──┘
                  │
                  ▼
            nil  │  err (temp removed by defer)
                  │
                  ▼
       [wiring ticket: run RestartProbe-derived restart command]
```

No goroutines. No state across calls. Caller-driven.

## Concurrency model

`AtomicReplace` is reentrant in the formal sense (no package-level state), but two concurrent calls *with the same `targetPath`* race at the rename step: whichever rename runs second wins, and the loser's temp file is already gone (it was the one renamed-then-displaced — wait no, both temp names are unique random suffixes from `CreateTemp`, so each has its own temp; whichever Rename runs *last* wins on `targetPath`). The earlier rename's bytes are immediately overwritten and its inode is unlinked. No corruption, but a "lost update".

Update flow callers will only ever invoke this from a single goroutine in `cmd/pyry update`, so this isn't worth defending against. The function doc doesn't promise anything about concurrent calls; we don't add a lock.

No `context.Context` parameter: a single sub-megabyte buffered write is non-cancellable in any meaningful sense, and the wiring ticket can wrap the whole update with its own ctx.

## Error handling

| Input shape | Outcome |
|---|---|
| Target dir exists, target file missing, dir writable | New file created with `newData` and `mode`, returns `nil`. |
| Target dir exists, target file exists with old contents | New file replaces old atomically, returns `nil`. Old inode unlinked when last open fd closes — running pyry processes keep their mmap'd image. |
| Target dir does not exist | `CreateTemp` returns `*PathError` wrapping `ENOENT`. We wrap as `"atomic replace: create temp in <dir>: ..."`. No temp file exists to clean up. |
| Target dir exists but not writable | `CreateTemp` returns `*PathError` wrapping `EACCES`. Same wrapping. |
| Target dir on tmpfs running out of space | `Write` returns `ENOSPC`. We wrap and the deferred `os.Remove(tmp)` reclaims the partial bytes. |
| `mode` is `0o000` | File created with mode 0; subsequent re-execution of `pyry` would fail with EACCES. Caller's bug, not ours; we honour the mode they asked for. |

The "no stray temp file in the surrounding tempdir on the parent-missing error path" assertion in the AC is satisfied trivially: `CreateTemp` failed, no fd was opened, the `defer` runs but `tmp` is the empty string (never set) — the `defer` should guard that. Sketch:

```go
defer func() {
    if !renamed && tmp != "" {
        _ = os.Remove(tmp)
    }
}()
```

Actually the cleaner pattern is to set `tmp` only after `CreateTemp` succeeds (as in the sketch above — `tmp := f.Name()` only runs after the err check). Then there's no need for a `tmp != ""` guard: the `defer` is registered before that line, but `tmp`'s zero value would be `""` and `os.Remove("")` returns an error that we ignore anyway. Even cleaner: register the `defer` *after* the successful `CreateTemp`. Implementer's choice; the test doesn't care about the mechanism, only the outcome (no stragglers).

## Testing strategy

Single test file `replace_test.go`, table-driven where it adds clarity, targeted subtests where each case has different setup. Mirror `checksum_test.go`'s style: `t.Parallel()` at the top of every subtest, inline assertions, `errors.Is` only when an error sentinel exists (none here — use `if err == nil` / `if err != nil` and substring checks).

All tests use `t.TempDir()` exclusively. No `testdata/`. No global fixtures.

### Test cases

```go
func TestAtomicReplace_CreatesNewFile(t *testing.T) {
    t.Parallel()
    dir := t.TempDir()
    target := filepath.Join(dir, "pyry")
    want := []byte("new binary contents")

    if err := update.AtomicReplace(target, want, 0o755); err != nil {
        t.Fatalf("AtomicReplace: %v", err)
    }

    got, err := os.ReadFile(target)
    if err != nil { t.Fatalf("read back: %v", err) }
    if !bytes.Equal(got, want) {
        t.Errorf("contents = %q, want %q", got, want)
    }
}
```

```go
func TestAtomicReplace_OverwritesExistingFile(t *testing.T) {
    t.Parallel()
    dir := t.TempDir()
    target := filepath.Join(dir, "pyry")
    if err := os.WriteFile(target, []byte("OLD"), 0o644); err != nil {
        t.Fatalf("seed: %v", err)
    }
    want := []byte("NEW")

    if err := update.AtomicReplace(target, want, 0o755); err != nil {
        t.Fatalf("AtomicReplace: %v", err)
    }

    got, err := os.ReadFile(target)
    if err != nil { t.Fatalf("read back: %v", err) }
    if !bytes.Equal(got, want) {
        t.Errorf("contents = %q, want %q", got, want)
    }
}
```

```go
func TestAtomicReplace_PreservesMode(t *testing.T) {
    t.Parallel()
    dir := t.TempDir()
    target := filepath.Join(dir, "pyry")

    if err := update.AtomicReplace(target, []byte("x"), 0o755); err != nil {
        t.Fatalf("AtomicReplace: %v", err)
    }

    info, err := os.Stat(target)
    if err != nil { t.Fatalf("stat: %v", err) }
    if got := info.Mode().Perm(); got != 0o755 {
        t.Errorf("mode = %o, want %o", got, 0o755)
    }
}
```

```go
func TestAtomicReplace_ParentDirMissing(t *testing.T) {
    t.Parallel()
    dir := t.TempDir()
    // Parent "nope" does not exist inside dir.
    target := filepath.Join(dir, "nope", "pyry")

    err := update.AtomicReplace(target, []byte("x"), 0o755)
    if err == nil {
        t.Fatalf("expected error, got nil")
    }

    // Surrounding tempdir must contain no stray temp files. Read dir entries
    // and assert the slice is empty (the only thing that should ever appear
    // here is one of our ".pyry.*.tmp" leaks, which would be the bug).
    entries, err := os.ReadDir(dir)
    if err != nil { t.Fatalf("readdir: %v", err) }
    if len(entries) != 0 {
        names := make([]string, 0, len(entries))
        for _, e := range entries { names = append(names, e.Name()) }
        t.Errorf("expected empty tempdir, got entries: %v", names)
    }
}
```

### Test conventions

- Package `update_test` is fine (external) or `update` (internal); `checksum_test.go` uses internal — match it. `update` it is, so we don't have to import-prefix every call.
- Package-prefixed call sites in the snippets above (`update.AtomicReplace`) are illustrative — drop the prefix when the test lives in `package update`.
- No mode test for *not preserving* mode — only the requested mode matters.
- Table-driven would work for the three success cases (collapse into one table with `want` bytes, `wantMode`, and "seed existing" toggle), but writing them as separate `Test*` functions reads more naturally for the error case which has different setup. Implementer may collapse the three success cases into one `TestAtomicReplace` table if preferred — both shapes are idiomatic.
- Run with `go test -race ./internal/update/...`.

### Why no umask test

Test process umask is inherited from the shell (typically `0o022`). Asking for `0o755` and getting `0o755 &^ umask = 0o755` works on a `0o022` umask machine. Asking for `0o777` and asserting `0o755` would test the umask, not our code. We assert `os.Stat().Mode().Perm() == 0o755` after requesting `0o755`, which works under any sane umask (`0o022`, `0o002`, `0o077` all preserve the requested 0o755 — wait, `0o077` would mask the group/other bits and produce `0o700`). To stay robust we *could* `syscall.Umask(0)` for the duration of the test; the AC doesn't demand that and CI runs with default umask. Implementer's call. Logged in open questions.

## Open questions

1. **Parent-directory fsync after rename.** The standard "crash-durable atomic rename" pattern includes `open(dir) + fsync + close` after the rename so the directory entry survives a power loss between rename and the next dirent flush. `saveRegistryLocked` omits it; we omit it; both rest on "process interrupt" being the threat model rather than "power loss". If/when a `pyry update` user reports a missing binary after a power outage, revisit. Adding it later is a one-liner and the AC unchanged.
2. **Umask interference in `TestAtomicReplace_PreservesMode`.** A maintainer running CI with an unusual umask (`0o077`) would see this test fail spuriously. Hardening with `syscall.Umask(0)` + restore in `t.Cleanup` is cheap; deferred to implementer judgment given default-umask CI.
3. **Linux `renameat2(RENAME_NOREPLACE)` for new-file case.** The "new file" path (target doesn't exist) currently lets a concurrent writer create the same path between our `CreateTemp` and our `Rename` — the rename would silently overwrite. Not in threat model (single-goroutine update flow); flagged for completeness.
4. **`io.Reader` variant.** Today's binary is ~12 MiB and the wiring ticket already has it as `[]byte` from #186's tar extraction. Streaming would force #186 to expose a reader instead. Not worth the contract change.

## Out of scope

- The wiring (cmd/pyry update verb) that calls `AtomicReplace` — separate ticket.
- Tar extraction that produces the input bytes (#186).
- HTTP fetching of the tarball (#182, in flight on PR #193).
- Restart-command invocation (#181 already shipped the *detection*; the *invocation* is the wiring ticket's job).
- Backup of the old binary as a rollback artifact — wiring layer's call.
- Cross-filesystem rename support. Documented as the same-filesystem caveat; structurally prevented by `CreateTemp(filepath.Dir(targetPath), ...)`.
- Sentinel errors. Not needed by any current caller; can be added later without breaking the signature.
