# Spec: install-service WorkingDirectory defaults to cwd (#177)

## Files to read first

- `internal/install/install.go:64-151` — `Options` struct + `Install()` setup; the `WorkDir` default at line 136-143 is what changes.
- `internal/install/install.go:160-175` — how `templateData.WorkDir` flows into the unit file path picking and template rendering.
- `internal/install/install_test.go:12-179` — existing table-driven tests; new tests follow the same shape (in-memory `Options`, write to `t.TempDir()`-rooted home, read file, substring-assert).
- `internal/install/install_test.go:289-306` — `TestInstall_InheritsEnvPath` is the closest analogue: pure-Options input, no environment patching, asserts on file body. Mirror its structure.
- `cmd/pyry/main.go:1064-1154` — `runInstallService` body: flag parsing, the `install.Install(...)` call site, and the existing "Inherited PATH" print block. The new "WorkingDirectory:" line goes above it (line 1118).
- `cmd/pyry/main.go:1082` — current `--workdir` flag definition; the help string changes from `~/pyry-workspace` to `current directory`.

No need to read launchd / systemd templates — they already substitute `{{.WorkDir}}` literally; the change is what string we pass in, not how it's rendered.

## Context

`pyry install-service` today bakes `~/pyry-workspace` (expanded for launchd, `%h/pyry-workspace` for systemd) as `WorkingDirectory` regardless of where the user ran it. On a fresh machine without that directory pre-created, launchd `chdir`s, fails with code 78, and the daemon never binds its socket — surfaced 2026-05-08 setting up the Mac Claudian daemon.

The fix: default `WorkingDirectory` to the cwd at install time (matches `pyry start`'s mental model — "set up the daemon HERE"), keep `--workdir <path>` as the explicit override, and warn (don't abort) if the resolved directory doesn't exist.

## Design

### Resolution layer

Add an unexported helper in `internal/install`:

```go
// resolveWorkDir returns the absolute, canonicalised WorkingDirectory to
// bake into the unit file, given the user-supplied --workdir flag value
// (possibly empty) and the install-time cwd. If flag is empty, cwd is
// used. A leading "~/" or "~" is expanded via homeDir. The result is
// always absolute (via filepath.Abs).
//
// homeDir defaults to os.UserHomeDir() at the call site; injected here
// for testability.
func resolveWorkDir(flag, cwd, homeDir string) (string, error)
```

Behaviour:

| `flag`            | result                                          |
|-------------------|-------------------------------------------------|
| `""`              | `filepath.Abs(cwd)`                             |
| `~` or `~/sub`    | `filepath.Abs(filepath.Join(homeDir, "sub"))`   |
| absolute path     | `filepath.Clean(flag)` (already absolute)       |
| relative path     | `filepath.Abs(flag)` (resolves against cwd)     |

Errors only on `filepath.Abs` failure (essentially never, since cwd was readable enough to launch the process). `~` expansion needs no error path beyond what the caller already hits when resolving `homeDir`.

This helper does **not** touch the `Options` struct — `Install()` continues to receive a fully-resolved `WorkDir` string. The struct's existing default branch (`install.go:136-143`) is **deleted**: the CLI is the only caller and now always supplies a resolved `WorkDir`. Tests that previously relied on the `WorkDir == ""` default (e.g. `TestInstall_Systemd_BareTemplate` at line 12-59 asserting `WorkingDirectory=%h/pyry-workspace`, and `TestInstall_Launchd_BareTemplate` at line 114-149 asserting `filepath.Join(home, "pyry-workspace")`) get updated to pass an explicit `WorkDir` value.

### CLI layer (`cmd/pyry/main.go`)

In `runInstallService` (around line 1098-1106, before the `install.Install` call):

1. Resolve the working directory:
   ```go
   cwd, err := os.Getwd()
   if err != nil { return fmt.Errorf("install-service: get cwd: %w", err) }
   homeDir, err := os.UserHomeDir()
   if err != nil { return fmt.Errorf("install-service: home dir: %w", err) }
   resolvedWorkDir, err := install.ResolveWorkDir(*workdir, cwd, homeDir)
   if err != nil { return fmt.Errorf("install-service: resolve workdir: %w", err) }
   ```
   `resolveWorkDir` is exported as `ResolveWorkDir` for the CLI to call. Keeping the exported surface to one helper keeps the package's public API tight.

2. Print the resolved value before the Install call (i.e. before any file I/O — the user sees the effective value even if Install errors):
   ```go
   fmt.Printf("WorkingDirectory: %s\n", resolvedWorkDir)
   ```
   This goes immediately above the existing "Inherited PATH" block (line 1118-1126). The blank-line spacing in the existing output remains untouched.

3. Stat-check the directory. If it doesn't exist, print a warning and continue:
   ```go
   if _, err := os.Stat(resolvedWorkDir); errors.Is(err, fs.ErrNotExist) {
       fmt.Printf("warning: %s does not exist; create it with: mkdir -p %s\n",
           resolvedWorkDir, resolvedWorkDir)
   }
   ```
   (Other Stat errors — permission denied, etc. — are ignored. Don't gate install on them; the daemon's own error at runtime will be clearer than a guess here.)

4. Pass `WorkDir: resolvedWorkDir` into `install.Install`'s Options.

5. Update the `--workdir` flag default-value docstring at line 1082 from `~/pyry-workspace` to `current directory`.

### Data flow

```
$ pyry install-service [--workdir PATH]
       │
       ▼
runInstallService:
  os.Getwd() ──┐
  os.UserHomeDir() ──┐
  flag --workdir ──┐  │  │
                   ▼  ▼  ▼
            install.ResolveWorkDir() → resolvedWorkDir (absolute)
                   │
                   ├─→ print "WorkingDirectory: <path>"
                   ├─→ os.Stat(); print warning if missing
                   ▼
            install.Install(Options{WorkDir: resolvedWorkDir, ...})
                   │
                   ▼
            template renders {{.WorkDir}} verbatim → unit file on disk
```

No goroutines, no context plumbing — single synchronous CLI command.

## Concurrency model

None. `runInstallService` is straight-line synchronous code. No locks, no goroutines, no shared state.

## Error handling

| Failure                                  | Behaviour                                                       |
|------------------------------------------|-----------------------------------------------------------------|
| `os.Getwd()` fails                       | Return `fmt.Errorf("install-service: get cwd: %w", err)`        |
| `os.UserHomeDir()` fails (only if `~` in flag, but called unconditionally) | Return `fmt.Errorf("install-service: home dir: %w", err)` |
| `filepath.Abs` fails inside `ResolveWorkDir` | Wrap and return                                              |
| Resolved directory does not exist        | Print warning to stdout, continue with install                  |
| `os.Stat` fails for a reason other than ENOENT | Silently ignore; install proceeds                          |
| Existing `install.Install` errors        | Unchanged — bubble up as before                                 |

The "missing directory ⇒ warn, don't abort" rule is in the AC (#177 AC-4) and matches user intent: they may want to mkdir with specific perms after.

## Testing strategy

Add to `internal/install/install_test.go`:

1. **`TestResolveWorkDir`** — table-driven, exercises all four `flag` cases above:
   - empty → cwd-equivalent
   - `~/sub` → `homeDir/sub`
   - `~` → `homeDir`
   - `/abs/path` → unchanged
   - `relative/path` → `cwd/relative/path` (joined and cleaned)
   Inputs: `(flag, cwd, homeDir)`. Compare to expected absolute path string.

2. **`TestInstall_Systemd_CwdWorkDir`** — call `Install` with `WorkDir: "/home/test/projects/foo"` (an absolute path simulating what the CLI would pass after resolving cwd) and assert the rendered unit contains `WorkingDirectory=/home/test/projects/foo` literally — no `%h` substitution.

3. **`TestInstall_Launchd_CwdWorkDir`** — same shape, asserts `<string>/Users/test/projects/foo</string>` appears in the plist (under the `WorkingDirectory` key).

4. **Update existing tests** that asserted on the old defaults:
   - `TestInstall_Systemd_BareTemplate` (line 40): change `WorkingDirectory=%h/pyry-workspace` to a passed-in absolute path.
   - `TestInstall_Launchd_BareTemplate` (line 141): change `filepath.Join(home, "pyry-workspace")` to a passed-in absolute path.
   - Any other test that omitted `WorkDir` and implicitly relied on the default: pass an explicit `WorkDir` value.

The CLI-level cwd capture is not unit-tested directly (no test infra for `cmd/pyry`'s `runInstallService` exists, and adding one is out of scope per the "simplicity first" principle). The behaviour is covered transitively: `ResolveWorkDir("", cwd, home)` returns cwd, and `Install` is tested to bake whatever it's given. The `os.Stat` warning is straightforward enough that exhaustive testing isn't worth the harness cost.

## Open questions

- **Should we abort if `--workdir` is supplied but doesn't exist?** AC-4 says no — warn and continue. We'll respect that. (The argument for aborting: if the user typed an explicit path, mistyping it is a real risk. Counter: the warning surfaces the typo, and the cost of fixing it post-install is `mkdir -p` or rerunning with `--force`. Not worth gating.)
- **Trailing slash on `~/`?** `filepath.Join(home, "")` = `home`, `filepath.Join(home, "sub")` = `home/sub`. Both are fine after `filepath.Clean`. No special-casing needed.
- **Symlinks in cwd?** `os.Getwd()` returns whatever the kernel gives us — may include or exclude symlinks depending on how the shell got there. We bake the literal string. If users want canonical paths, they pass `--workdir "$(pwd -P)"` explicitly.
