# Spec: `pyry update` subcommand wiring (#189)

## Dependency note (read before starting)

This ticket consumes two `internal/update` primitives that were not yet on `main` at architect time:

- `update.Fetcher` (struct + `FetchLatestRelease` + `FetchAsset`) — open in PR #193 (#182).
- `update.AtomicReplace(target, data, mode)` — open in PR #195 (#187).

Both have stable contracts (frozen by their issue bodies) and the spec below is written against them. Before starting implementation, run `git pull origin main` in your worktree and confirm `internal/update/fetch.go` and `internal/update/replace.go` both exist. If either is missing, stop and route the ticket back via `needs-rework:po` with a `blockedBy` link — do not stub the primitives locally, and do not invent surrogate signatures.

## Files to read first

- `cmd/pyry/main.go:140-172` — the `run()` switch where `case "update":` lands; mirror the `runX(os.Args[2:])` calling convention used by `runStatus`, `runStop`, `runAttach`, `runSessions`, `runInstallService`. Errors return up; `main()` already wraps them with `"pyry: <err>"` to stderr and exits 1, so `runUpdate` returns plain `error`.
- `cmd/pyry/main.go:1060-1174` — `runInstallService` is the closest sibling: same shape (`flag.NewFlagSet("pyry <verb>", flag.ContinueOnError)`, plain `fmt.Printf` for progress lines, `fmt.Errorf("install-service: …", err)` wrapping). `runUpdate` reuses this voice — no logger, no slog; the `update` verb is a one-shot CLI command, not part of the supervisor.
- `cmd/pyry/main.go:1176-1230` — `printHelp()` body. New `pyry update` line goes alphabetically between `pyry stop` and `pyry status` in the verb listing block (line ~1188). One-line summary mirrors the existing voice ("download and install the latest release").
- `cmd/pyry/main.go:53` — `var Version = "dev"` is the input to `runUpdate`; `internal/update.CompareVersions` rejects `"dev"` with `ErrInvalidVersion`. Branch on `errors.Is(err, update.ErrInvalidVersion)` to print "running a dev build, skipping update check" and return nil; do not attempt a self-update of a `-dev` build (would clobber `go install` output with a release tarball).
- `internal/update/version.go:62-110` — `ParseLatestRelease` returns the raw `tag_name` (e.g. `"v0.9.2"`, leading `v` preserved). `CompareVersions` strips the leading `v` and returns `Older`/`Same`/`Newer` plus an error. Compare via this function, not raw `==`, so `v0.9.1` and `0.9.1` collapse correctly.
- `internal/update/checksum.go:35-56` — `AssetName(version, runtime.GOOS, runtime.GOARCH)` produces the GoReleaser tarball filename. Pass the bare semver or the `v`-prefixed form interchangeably; the function strips the prefix.
- `internal/update/checksum.go:58-90` — `ParseChecksumsFile(text, assetName)` returns the lowercase hex digest for the named asset. Wraps `ErrAssetNotInChecksums` and `ErrMalformedChecksums` for `errors.Is`.
- `internal/update/checksum.go:92-105` — `VerifySHA256(data, expectedHex)` returns nil on match, `ErrChecksumMismatch` (wrapped) otherwise.
- `internal/update/install.go:35-65` — `ExtractBinary(tgzData, "pyry")` returns the binary's bytes from the in-memory tarball.
- `internal/update/fetch.go` (#182, PR #193) — `Fetcher` struct: zero-value `BaseURL` defaults to `https://api.github.com`, zero-value `HTTPClient` defaults to `http.DefaultClient`, zero-value `UserAgent` defaults to `pyry/dev`. Set `UserAgent: "pyry/" + Version` from `cmd/pyry`. `FetchLatestRelease(ctx, "pyrycode/pyrycode")` and `FetchAsset(ctx, url)` are the two methods you call.
- `internal/update/replace.go` (#187, PR #195) — `AtomicReplace(targetPath, newData, mode)`. Pass `0o755` for mode. Same-filesystem caveat is on the function, not on you — `os.Executable()` returns an absolute path that's already on the install filesystem.
- `docs/specs/architecture/186-update-extract-binary.md` — sibling-spec voice (sentinel errors, doc-comment shape, "evidence-based fix" framing). Mirror this style in production-side comments; the wiring file should feel like a continuation of the package, not a new dialect.
- `docs/PROJECT-MEMORY.md:8-50` — the four landed primitives' "Out of scope" lists collectively define what this ticket is responsible for. The `dev` sentinel handling and `MapHostPlatform` (use `runtime.GOOS`/`runtime.GOARCH` directly) are explicitly punted to here.
- `docs/guide.md:1-66` — voice and section style for the new "Updating pyry" paragraph (after "Installing", before "Foreground mode" — alphabetical-ish flow from install → update → run).
- Issue #189 body — the five acceptance criteria are the contract, including the verbatim progress lines (`==> Current version:`, `==> Latest version:`, `==> Downloading <asset>...`, `==> Verifying SHA-256... ok`, `==> Replacing <path>...`, `==> Updated to <v>.`).

No prior knowledge doc on the wiring layer exists — the four primitive specs cover the lower half. This spec is the cap.

## Context

`pyry update` is the user-facing command that ties together the four pure-function primitives (`internal/update`) plus the two I/O primitives (Fetcher #182, AtomicReplace #187) into a single update flow. The wiring matches the documented UX from the issue body: print current version, resolve target, optionally short-circuit, fetch + verify + extract + replace, print "Updated to <v>." on success.

Scope is deliberately narrow: **binary swap only**. Daemon restart is split off into a follow-up ticket — the user manually `launchctl kickstart`s or `systemctl --user restart pyry`s after `pyry update` returns. `restart.go`'s `DetectRestartCommand` is therefore unused by this ticket, even though it lives in the same package.

The Homebrew hint (AC #1) is a courtesy: a Homebrew-installed pyry will hit `AtomicReplace` and successfully overwrite the binary in `/opt/homebrew/bin/pyry`, but the user's *next* `brew upgrade` will revert it (or worse, complain about a tampered cellar). Printing a one-line `Hint: brew upgrade pyry` before the flow runs nudges them to the right command without forcing pyry to detect-and-refuse — refusing would block the rare Homebrew user who knowingly wants the GitHub-release version.

## Design

### Package placement

New file `cmd/pyry/update.go` with `runUpdate(args []string) error` as the single exported (package-internal) entry point. The function dispatches to a private `doUpdate(ctx, opts) error` whose options struct is the integration-test seam (see Testing strategy). One-line wiring in `cmd/pyry/main.go`'s switch, one-line addition to `printHelp`. No new sub-package; no new exported types outside `cmd/pyry`.

### Wiring in `main.go`

```go
// in run(), case ordering preserves the existing alphabetical-ish flow
case "update":
    return runUpdate(os.Args[2:])
```

In `printHelp`, between the `stop` and `status` lines (line ~1188-1189):

```
  pyry update [--check] [--version <v>]          download and install the latest release
                                                  (--check: print versions only;
                                                   --version <v>: pin a specific tag)
```

That's the entire `main.go` change: 2 lines in `run`, 3-4 lines in `printHelp`'s inline doc string.

### `runUpdate` body — outline

```go
func runUpdate(args []string) error {
    fs := flag.NewFlagSet("pyry update", flag.ContinueOnError)
    checkOnly := fs.Bool("check", false, "print current and latest versions, then exit")
    pinVersion := fs.String("version", "", "install this version instead of the latest release")
    if err := fs.Parse(args); err != nil {
        return err
    }

    return doUpdate(context.Background(), updateOptions{
        // defaults; tests override
        currentVersion: Version,
        goos:           runtime.GOOS,
        goarch:         runtime.GOARCH,
        repo:           "pyrycode/pyrycode",
        releaseBaseURL: "https://github.com/pyrycode/pyrycode/releases/download",
        fetcher: &update.Fetcher{
            UserAgent:  "pyry/" + Version,
            HTTPClient: &http.Client{Timeout: 60 * time.Second},
        },
        executablePath: resolveExecutable,
        replace:        update.AtomicReplace,
        out:            os.Stdout,
        checkOnly:      *checkOnly,
        pinVersion:     *pinVersion,
    })
}

func resolveExecutable() string {
    if exe, err := os.Executable(); err == nil {
        return exe
    }
    return os.Args[0]
}
```

### `doUpdate` — flow

```go
type updateOptions struct {
    currentVersion string
    goos, goarch   string
    repo           string
    releaseBaseURL string                                       // "https://github.com/<owner>/<repo>/releases/download"
    fetcher        *update.Fetcher
    executablePath func() string
    replace        func(target string, data []byte, mode os.FileMode) error
    out            io.Writer
    checkOnly      bool
    pinVersion     string
}

func doUpdate(ctx context.Context, o updateOptions) error {
    // 1. Brew hint (non-blocking). os.Executable() resolved once via o.executablePath().
    target := o.executablePath()
    if strings.HasPrefix(target, "/opt/homebrew/") {
        fmt.Fprintln(o.out, "Hint: this pyry was installed via Homebrew; consider 'brew upgrade pyry' instead.")
    }

    // 2. Print current version.
    fmt.Fprintf(o.out, "==> Current version: %s\n", o.currentVersion)

    // 3. Resolve target tag.
    var targetVer string
    if o.pinVersion != "" {
        targetVer = o.pinVersion
    } else {
        body, err := o.fetcher.FetchLatestRelease(ctx, o.repo)
        if err != nil {
            return fmt.Errorf("update: fetch latest release: %w", err)
        }
        tag, err := update.ParseLatestRelease(body)
        if err != nil {
            return fmt.Errorf("update: parse latest release: %w", err)
        }
        targetVer = tag
    }
    fmt.Fprintf(o.out, "==> Latest version:  %s\n", targetVer)

    // 4. Already up to date? Use CompareVersions for v-prefix tolerance.
    cmp, err := update.CompareVersions(o.currentVersion, targetVer)
    switch {
    case errors.Is(err, update.ErrInvalidVersion):
        // Most likely currentVersion == "dev". Skip the self-update; a dev
        // build came from `go install` or local make, and overwriting it
        // with a release tarball would silently revert the developer's
        // working copy.
        fmt.Fprintf(o.out, "==> Running a development build (%s); skipping update.\n", o.currentVersion)
        return nil
    case err != nil:
        return fmt.Errorf("update: compare versions: %w", err)
    case cmp == update.Same:
        fmt.Fprintf(o.out, "==> Current version: %s — already at latest.\n", o.currentVersion)
        return nil
    }

    // 5. --check stops here.
    if o.checkOnly {
        return nil
    }

    // 6. Asset name + URLs.
    asset, err := update.AssetName(targetVer, o.goos, o.goarch)
    if err != nil {
        return fmt.Errorf("update: %w", err)
    }
    tarballURL := fmt.Sprintf("%s/%s/%s", o.releaseBaseURL, targetVer, asset)
    checksumsURL := fmt.Sprintf("%s/%s/checksums.txt", o.releaseBaseURL, targetVer)

    // 7. Download tarball.
    fmt.Fprintf(o.out, "==> Downloading %s...\n", asset)
    tgz, err := o.fetcher.FetchAsset(ctx, tarballURL)
    if err != nil {
        return fmt.Errorf("update: download tarball: %w", err)
    }

    // 8. Download checksums.
    sumsBytes, err := o.fetcher.FetchAsset(ctx, checksumsURL)
    if err != nil {
        return fmt.Errorf("update: download checksums: %w", err)
    }
    digest, err := update.ParseChecksumsFile(string(sumsBytes), asset)
    if err != nil {
        return fmt.Errorf("update: parse checksums: %w", err)
    }

    // 9. Verify.
    fmt.Fprint(o.out, "==> Verifying SHA-256... ")
    if err := update.VerifySHA256(tgz, digest); err != nil {
        fmt.Fprintln(o.out, "FAIL")
        return fmt.Errorf("update: verify checksum: %w", err)
    }
    fmt.Fprintln(o.out, "ok")

    // 10. Extract.
    bin, err := update.ExtractBinary(tgz, "pyry")
    if err != nil {
        return fmt.Errorf("update: extract binary: %w", err)
    }

    // 11. Replace.
    fmt.Fprintf(o.out, "==> Replacing %s...\n", target)
    if err := o.replace(target, bin, 0o755); err != nil {
        return fmt.Errorf("update: replace binary: %w", err)
    }

    fmt.Fprintf(o.out, "==> Updated to %s.\n", targetVer)
    return nil
}
```

That's the whole flow. ~110 lines of production code including the options struct, `runUpdate`, `resolveExecutable`, and `doUpdate`.

### Why a struct-based seam (`updateOptions`)

The four field-level seams that the integration test needs are: (a) `Fetcher.BaseURL` for the latest-release call (already a Fetcher field), (b) `releaseBaseURL` for the tarball + checksums URL templating (would otherwise be a hard-coded `https://github.com/...` constant), (c) `executablePath()` so the test can point at a tempdir file rather than the real `/usr/local/bin/pyry`, and (d) `out` so `httptest`-driven assertions can scan the captured progress lines without racing stdout. Bundling these into one `updateOptions` struct keeps the `runUpdate` → `doUpdate` boundary single-argument and lets every default land in one place. No `init()`, no global vars, no `httptest` baked into production code.

### Why URL-template the asset URLs instead of using `assets[*].browser_download_url` from the release JSON

The release JSON parser (`ParseLatestRelease`) extracts `tag_name` only. Adding asset-URL extraction would expand the parser's surface (and #179's tests) for no value: the URL template `https://github.com/<owner>/<repo>/releases/download/<tag>/<asset>` is GitHub's documented stable scheme and matches what `.goreleaser.yaml` produces. The same `.goreleaser.yaml`-coupling caveat that already applies to `AssetName` covers this — both values live in this repo, both move together if GitHub ever changes the scheme. Tests stub the base URL via `releaseBaseURL`.

### Why a 60-second HTTP timeout

The fetcher exposes `HTTPClient` for caller-supplied timeouts; the wiring caller picks a sane default. 60 s is generous for a ~20 MB tarball on a slow connection but bounded enough that a hung `pyry update` doesn't sit forever. Configurable via `--http-timeout` is YAGNI; users can re-run if they need a higher value, and the value isn't load-bearing on any AC.

### Why short-circuit on "dev" rather than fail loud

`Version == "dev"` is the build-from-source sentinel; users in this state have a working `git clone` and can `make build` themselves. Returning silently after a one-line "skipping update" message is the user-friendly path. Failing with `ErrInvalidVersion` would force every `pyry update` invocation in a development worktree to exit 1, which fights the developer's muscle memory ("I run `pyry update` to refresh"). This matches PROJECT-MEMORY's already-stated plan for how to consume `ErrInvalidVersion`.

### Why no `--no-restart` flag, no restart attempt

Out of scope per the issue body; restart wiring is the explicit follow-up. `runUpdate` returns after the binary is in place; the user runs `launchctl kickstart -k gui/$(id -u)/dev.pyrycode.pyry` (macOS) or `systemctl --user restart pyry` (Linux) themselves. The "Updating pyry" guide section documents these as the post-update step.

### Why no rollback / backup-of-old-binary

Out of scope (deferred entirely per the issue body). `AtomicReplace` already provides crash-safety up to the rename; partial-write corruption is structurally unreachable. Rollback after a successful but undesired update is a separate feature with its own design (where to store the old binary, how to garbage-collect, how the user invokes it).

### Why `fmt.Fprint` / `fmt.Fprintf` rather than a `slog.Logger`

This is a one-shot CLI verb, not a daemon. The output is human-facing progress lines on stdout, not structured-logging events for log aggregation. `runInstallService` already uses `fmt.Printf` for the same reason. Keep the convention.

## Concurrency model

Sequential. One goroutine (the calling one). One `context.Context` (`context.Background()` from `runUpdate`; tests pass their own `t.Context()` or a cancellable derivative). No locks, no channels, no goroutine fan-out.

The two HTTP fetches (tarball + checksums) are issued back-to-back rather than in parallel. Parallelising would shave maybe a second on a fast connection, requires an `errgroup`, and complicates the "==> Downloading <asset>..." progress-line ordering — not worth the complexity for a one-shot user command.

Cancellation propagation is via `context.Background()` today; if a future ticket adds Ctrl-C handling for `pyry update`, swap in `signal.NotifyContext(ctx, os.Interrupt)`. The Fetcher already honours context cancellation per #182 AC.

## Error handling

All errors propagate up to `main()`'s wrapper at `cmd/pyry/main.go:141-144`, which prints `pyry: <err>` to stderr and exits 1. Every `runUpdate`/`doUpdate` error is wrapped with a verb-prefix (`"update: <step>: %w"`) so the user sees `pyry: update: download tarball: GET https://...: 404 Not Found` rather than a bare HTTP error.

Specific branch:

- `errors.Is(err, update.ErrInvalidVersion)` from `CompareVersions` with `currentVersion == "dev"` (or any other unparseable value) → print "running a development build" and return nil (exit 0). This is the only case where a primitive's error is converted to a non-error path.
- `errors.Is(err, update.ErrUnsupportedPlatform)` from `AssetName` → propagates as a wrapped error. Operator running `pyry update` on `freebsd/amd64` sees `pyry: update: asset name for freebsd/amd64: unsupported os/arch` and re-runs with `--version` only after a manual download — this is acceptable; the four supported combos cover the project's published targets.
- `errors.Is(err, update.ErrChecksumMismatch)` → propagates as a wrapped error. The user sees `==> Verifying SHA-256... FAIL` followed by the stderr line; they re-run `pyry update`. No retry, no fallback — a checksum miss after a clean download is either GitHub serving a corrupt asset or a truly hostile MITM, and silently retrying masks both.
- `errors.Is(err, update.ErrBinaryNotInArchive)` / `ErrMalformedArchive` → propagates. Indicates an upstream packaging regression; the user sees the wrapped error and re-files an issue.
- `errors.Is(err, context.Canceled)` from the fetcher (test-only path until Ctrl-C handling lands) → propagates verbatim.

No partial-failure cleanup: `AtomicReplace` is the only filesystem-mutating step, and it's all-or-nothing (the temp file is removed on its own error paths per #187).

## Testing strategy

`cmd/pyry/update_test.go`, table-driven in spirit but not in form (each AC case has a distinct setup). Two scenarios from the AC plus a small handful of supporting sub-cases.

### Test fixtures

A single helper builds the fake release on-the-fly in each test:

```go
// fakeRelease produces a tarball, its SHA-256 digest, and a matching
// checksums.txt body, all tied to a single asset name.
func fakeRelease(t *testing.T, version, goos, goarch string) (asset string, tgz []byte, checksums string) {
    t.Helper()
    asset, err := update.AssetName(version, goos, goarch)
    if err != nil { t.Fatalf("AssetName: %v", err) }

    // Inline tar.gz with a single "pyry" entry (reuse the test idiom from
    // internal/update/install_test.go — buildTarGz helper).
    tgz = buildTarGz(t, map[string][]byte{"pyry": []byte("\x7fELF...new pyry bytes...")})

    sum := sha256.Sum256(tgz)
    checksums = fmt.Sprintf("%x  %s\n", sum[:], asset)
    return
}
```

`buildTarGz` is the same shape as `internal/update/install_test.go`'s helper (gzip → tar → write). Inline-copy it; sharing across packages would force an `internal/update/internal/testutil` package and that's overkill for ~10 lines.

### `httptest.NewServer` setup

```go
func newFakeReleaseServer(t *testing.T, latest string, asset string, tgz, checksums []byte) *httptest.Server {
    mux := http.NewServeMux()
    mux.HandleFunc("/repos/pyrycode/pyrycode/releases/latest", func(w http.ResponseWriter, r *http.Request) {
        fmt.Fprintf(w, `{"tag_name":%q}`, latest)
    })
    mux.HandleFunc("/releases/download/"+latest+"/"+asset, func(w http.ResponseWriter, r *http.Request) {
        w.Write(tgz)
    })
    mux.HandleFunc("/releases/download/"+latest+"/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
        w.Write(checksums)
    })
    s := httptest.NewServer(mux)
    t.Cleanup(s.Close)
    return s
}
```

The fetcher's `BaseURL` is overridden to `s.URL`; `releaseBaseURL` in `updateOptions` is overridden to `s.URL + "/releases/download"`. Both are direct field assignments on the test-side `updateOptions` literal — no production-side env-var seam, no `httptest` import in non-test code.

### Test cases

```go
func TestUpdate_Success(t *testing.T) {
    asset, tgz, checksums := fakeRelease(t, "v0.9.2", runtime.GOOS, runtime.GOARCH)
    srv := newFakeReleaseServer(t, "v0.9.2", asset, tgz, []byte(checksums))

    // tempdir-based install path
    tmp := t.TempDir()
    targetPath := filepath.Join(tmp, "pyry")
    if err := os.WriteFile(targetPath, []byte("OLD pyry bytes"), 0o755); err != nil {
        t.Fatal(err)
    }

    var out bytes.Buffer
    err := doUpdate(t.Context(), updateOptions{
        currentVersion: "0.9.1",
        goos:           runtime.GOOS,
        goarch:         runtime.GOARCH,
        repo:           "pyrycode/pyrycode",
        releaseBaseURL: srv.URL + "/releases/download",
        fetcher:        &update.Fetcher{BaseURL: srv.URL, UserAgent: "pyry/test"},
        executablePath: func() string { return targetPath },
        replace:        update.AtomicReplace,
        out:            &out,
    })
    if err != nil { t.Fatal(err) }

    // Binary on disk swapped.
    got, _ := os.ReadFile(targetPath)
    if !bytes.Equal(got, []byte("\x7fELF...new pyry bytes...")) {
        t.Errorf("on-disk binary not replaced: %q", got)
    }
    // Final progress line printed.
    if !strings.Contains(out.String(), "==> Updated to v0.9.2.") {
        t.Errorf("missing success line; output:\n%s", out.String())
    }
    // Checksum-verify success line printed.
    if !strings.Contains(out.String(), "==> Verifying SHA-256... ok") {
        t.Errorf("missing verify-ok line; output:\n%s", out.String())
    }
}

func TestUpdate_AlreadyAtLatest(t *testing.T) {
    asset, tgz, checksums := fakeRelease(t, "v0.9.1", runtime.GOOS, runtime.GOARCH)
    srv := newFakeReleaseServer(t, "v0.9.1", asset, tgz, []byte(checksums))

    var out bytes.Buffer
    err := doUpdate(t.Context(), updateOptions{
        currentVersion: "0.9.1",
        goos:           runtime.GOOS, goarch: runtime.GOARCH,
        repo:           "pyrycode/pyrycode",
        releaseBaseURL: srv.URL + "/releases/download",
        fetcher:        &update.Fetcher{BaseURL: srv.URL, UserAgent: "pyry/test"},
        executablePath: func() string { return "/dev/null/never-touched" },
        replace: func(string, []byte, os.FileMode) error {
            t.Fatalf("AtomicReplace should not run on already-at-latest path")
            return nil
        },
        out: &out,
    })
    if err != nil { t.Fatal(err) }
    if !strings.Contains(out.String(), "already at latest") {
        t.Errorf("missing already-at-latest line; output:\n%s", out.String())
    }
}
```

Two cases match the AC verbatim. A third optional case for `--check` (`checkOnly: true`) confirms the early return; a fourth optional for `--version v0.9.0` (downgrade-pin) confirms `pinVersion` short-circuits the `FetchLatestRelease` call. Add both if they fit under ~150 LOC of test code; skip if not.

### What the tests deliberately don't cover

- The `dev`-build branch — verified at unit level by `TestCompareVersions` already covering `ErrInvalidVersion`. Re-asserting at the wiring layer would just confirm `errors.Is` works.
- Brew-hint output — a one-line `strings.HasPrefix("/opt/homebrew/")` check too trivial to merit a full table row. If the maintainer wants belt-and-suspenders, add a sub-test that sets `executablePath: func() string { return "/opt/homebrew/bin/pyry" }` and greps for "Hint:".
- Real GitHub API. `httptest` is the only network seam; CI runs offline.
- `os.Executable()` behaviour — wrapped in `resolveExecutable()`, tests bypass the wrapper.

Test file size target: ~140 LOC including helpers. Above that, lift `buildTarGz` and `fakeRelease` into a small unexported helper file `cmd/pyry/update_test_helpers.go` (still `_test.go`-only via build tag or a `_test.go` suffix on the helpers).

### Guide section

Append to `docs/guide.md` after the "Installing" section, before "Foreground mode":

````markdown
## Updating pyry

Run `pyry update` to download and install the latest GitHub release in place:

```
pyry update
```

The command fetches the release manifest, verifies the tarball's SHA-256 against the published `checksums.txt`, and atomically replaces the binary at the path returned by `os.Executable()`. After it returns, restart the daemon if pyry is running as a service:

- macOS (launchd): `launchctl kickstart -k gui/$(id -u)/dev.pyrycode.pyry`
- Linux (systemd): `systemctl --user restart pyry`

Use `pyry update --check` to print the current and latest versions without downloading. Use `pyry update --version <tag>` to install a specific release (including downgrades). If pyry was installed via Homebrew, prefer `brew upgrade pyry` to keep the cellar consistent — `pyry update` will print a hint to that effect but still proceed if you ask it to.
````

One paragraph per the AC, three commands referenced inline, no new sub-sections.

## Open questions

- **Subcommand-vs-flag bikeshed for `--check`.** The AC says `--check`. An alternative spelling is `pyry update check` (separate verb, mirrors `pyry sessions list` style). Sticking with the AC; revisit only if the user-facing UX clusters tip toward verbs everywhere.
- **`Hint: brew upgrade pyry` exact wording.** AC says "a one-line `brew upgrade pyry` hint prints before the rest of the flow runs" — the spec writes `Hint: this pyry was installed via Homebrew; consider 'brew upgrade pyry' instead.`; trim to a barer `Hint: brew upgrade pyry` if the maintainer prefers AC-verbatim. Either fits "one-line".
- **Whether to extend `ParseLatestRelease` to also return `assets[*].browser_download_url`.** Decided against above on YAGNI grounds; revisit if a future ticket needs the published asset list (e.g., for a `pyry update --list` feature).
