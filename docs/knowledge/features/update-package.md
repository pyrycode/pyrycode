# `internal/update` — release parsing, version comparison, asset naming, SHA-256 verification, restart-command detection, HTTP fetcher, tar.gz extraction

Pyrycode's self-update logic (`pyry update`), assembled across several pure-function tickets and one network-I/O ticket. #179 landed the JSON parser + semver comparator; #180 added asset-name templating + checksum parsing + verification; #181 added restart-command detection; #182 added the HTTP fetcher; #186 added tar.gz binary extraction. Atomic on-disk replacement, the restart probe, and `pyry update` CLI verb wiring land in sister tickets.

## What it does

### Release manifest + version comparison (#179)

- `ParseLatestRelease(jsonBytes []byte) (string, error)` — extracts the `tag_name` field from a GitHub Releases API JSON payload (e.g. the body of `GET /repos/{owner}/{repo}/releases/latest`). Returns the verbatim tag (`"v0.9.1"`) or an error wrapping `ErrMalformedRelease`.
- `CompareVersions(current, latest string) (Comparison, error)` — reports `Older`/`Same`/`Newer` between two semver strings. Returns an error wrapping `ErrInvalidVersion` for inputs that don't parse.

### Asset naming + checksum verification (#180)

- `AssetName(version, goos, goarch string) (string, error)` — returns the GoReleaser-produced tarball filename, e.g. `AssetName("v0.9.1", "darwin", "arm64")` → `"pyry_0.9.1_Darwin_arm64.tar.gz"`. Strips a leading `v`; only the four `linux/darwin × amd64/arm64` combos built by `.goreleaser.yaml` are supported.
- `ParseChecksumsFile(text, assetName string) (string, error)` — given a GoReleaser-produced `checksums.txt` body and a target asset name, returns the lowercase SHA-256 hex digest for that asset.
- `VerifySHA256(data []byte, expectedHex string) error` — returns `nil` iff `sha256(data)` lowercase-hex equals `expectedHex`. Mismatch error includes both digests for diagnostic logging.

### Restart-command detection (#181)

- `RestartProbe` struct — bool fields `LaunchdPlistExists` and `SystemdUnitExists` plus a `UID` string templated into the launchctl gui/<uid>/… domain. The wiring ticket fills these from `os.Stat` on the platform-specific service file paths and `strconv.Itoa(os.Getuid())`.
- `DetectRestartCommand(probe RestartProbe) []string` — returns the argv (program plus args) that restarts a managed pyry daemon, or `nil` when no managed daemon is detected (foreground / unknown — caller should print "restart your pyry yourself" guidance).

### HTTP fetcher (#182)

- `Fetcher` struct — zero-value-usable wrapper over `net/http`. Three configurable fields: `BaseURL` (defaults to `https://api.github.com`), `HTTPClient` (defaults to `http.DefaultClient`), `UserAgent` (defaults to `pyry/dev`; the wiring layer sets `"pyry/" + main.Version`).
- `(*Fetcher).FetchLatestRelease(ctx, repo) ([]byte, error)` — GETs `<BaseURL>/repos/<repo>/releases/latest`, returns the body verbatim. `repo` is `"owner/name"`. Output suitable for `ParseLatestRelease`.
- `(*Fetcher).FetchAsset(ctx, url) ([]byte, error)` — GETs the URL, returns the body verbatim. Caller passes a fully-formed URL (typically `assets[].browser_download_url` extracted from the release JSON or the URL of the `checksums.txt` asset).

### Tar.gz binary extraction (#186)

- `ExtractBinary(tgzData []byte, binaryName string) ([]byte, error)` — reads a gzipped tar archive entirely from memory and returns the bytes of the regular-file entry whose tar header `Name` (under `filepath.Base`) matches `binaryName`. No temp files, no streaming-to-disk. Skips non-regular entries (directories, symlinks, hard links). Returns the first match. Wraps `ErrMalformedArchive` for invalid gzip or unparseable tar; wraps `ErrBinaryNotInArchive` if no regular-file entry matches.

The seven pure functions (`ParseLatestRelease`, `CompareVersions`, `AssetName`, `ParseChecksumsFile`, `VerifySHA256`, `DetectRestartCommand`, `ExtractBinary`) are stdlib-only and side-effect-free — no I/O, no goroutines, no `context.Context`. The `Fetcher` adds two stdlib-only methods that perform HTTP I/O — no retries, no caller-imposed timeouts (caller owns `*http.Client`), no body parsing.

## Types & errors

```go
type Comparison int

const (
    Older Comparison = -1 // current is older than latest
    Same  Comparison = 0  // versions are equal
    Newer Comparison = 1  // current is newer than latest
)

var ErrMalformedRelease     = errors.New("malformed release manifest")
var ErrInvalidVersion       = errors.New("invalid semver version")
var ErrUnsupportedPlatform  = errors.New("unsupported os/arch")
var ErrAssetNotInChecksums  = errors.New("asset not listed in checksums")
var ErrMalformedChecksums   = errors.New("malformed checksums file")
var ErrChecksumMismatch     = errors.New("sha256 checksum mismatch")
var ErrBinaryNotInArchive   = errors.New("binary not found in archive")
var ErrMalformedArchive     = errors.New("malformed tar.gz archive")
```

The fetcher introduces no new sentinel errors — the wiring ticket cannot retry (forbidden by AC) and therefore cannot branch on transient-vs-permanent, so a typed sentinel would be unused. Non-2xx responses surface as `fmt.Errorf("GET %s: unexpected status %d", url, resp.StatusCode)` (no `%w` — no inner error). Transport failures wrap the underlying `*url.Error`, which already participates in `errors.Is` traversal for `context.Canceled` / `context.DeadlineExceeded`. If a future caller needs typed branching, add `ErrUnexpectedStatus` then — defer until observed.

`Comparison` mirrors `cmp.Compare` / `strings.Compare` conventions — negative/zero/positive for less/equal/greater. No `String()` method (YAGNI; add when a caller needs it for `slog`).

All sentinels are exported so callers can branch with `errors.Is`. Each is wrapped with context at the return site (`fmt.Errorf("decoding release JSON: %w", ErrMalformedRelease)`, `fmt.Errorf("asset name for %s/%s: %w", goos, goarch, ErrUnsupportedPlatform)`, etc.).

On error, `CompareVersions` returns `Same` rather than `Older`/`Newer` — the safer bias when a caller forgets to check the error: silently reports "no update needed" rather than a spurious downgrade. `ParseChecksumsFile` returns `""` on error (callers must check the error first; the value is meaningless when `err != nil`).

## Behaviour

### Release JSON parsing

| Input | Outcome |
|-------|---------|
| Valid object with non-empty `tag_name` | `(tag, nil)` |
| Invalid JSON (truncated, garbage, empty bytes) | `ErrMalformedRelease` |
| Non-object top level (`[…]`, `"…"`, scalar) | `ErrMalformedRelease` |
| Object missing `tag_name` | `ErrMalformedRelease` |
| `"tag_name": ""` / `null` / non-string | `ErrMalformedRelease` |

Default `encoding/json` decoder — **not** `DisallowUnknownFields`. GitHub's payload has dozens of fields we don't read; strict decoding would convert "GitHub adds a field" into a load failure (same convention as `sessions.json` — see `lessons.md § Atomic on-disk writes`).

The tag is preserved **verbatim** — leading `v`, whitespace, pre-release / build-metadata suffixes all flow through unchanged. The comparator strips what it needs.

### Version comparison

| Tolerance | Examples |
|-----------|----------|
| Leading `v` optional, both sides | `v0.9.1` ↔ `0.9.1` |
| Pre-release suffix stripped at first `-` | `v0.10.0-rc1` → compares as `0.10.0` |
| Build-metadata stripped at first `+` | `v0.10.0+build.5` → compares as `0.10.0` |
| Numeric (not lexical) component compare | `0.10.0 > 0.9.99` |

Rejects: empty string, `"dev"` (the `main.Version` sentinel for unreleased builds), too-few/too-many components, non-numeric components, empty components (`v0..1`), negative components.

The pre-release strip is a deliberate simplification. Pyry's tags are plain `major.minor.patch`; the comparator does not implement SemVer 2.0.0 pre-release precedence. If pyry ever publishes a real pre-release operators must distinguish (a beta channel), revisit this and add proper precedence — until then, defer.

`"dev"` is the documented out-of-band: callers detecting a dev build should special-case it before invoking `CompareVersions`. The wiring ticket will branch on this and print "running a dev build, skipping update check" or similar.

### Asset name templating

| Input | Output |
|-------|--------|
| `("v0.9.1", "darwin", "arm64")` | `"pyry_0.9.1_Darwin_arm64.tar.gz"` |
| `("v0.9.1", "darwin", "amd64")` | `"pyry_0.9.1_Darwin_x86_64.tar.gz"` |
| `("v0.9.1", "linux", "arm64")` | `"pyry_0.9.1_Linux_arm64.tar.gz"` |
| `("v0.9.1", "linux", "amd64")` | `"pyry_0.9.1_Linux_x86_64.tar.gz"` |
| `("0.9.1", …)` (no `v`) | same as `"v0.9.1"` (no-op `TrimPrefix`) |
| `(…, "windows", "amd64")` | `("", err)` wrapping `ErrUnsupportedPlatform` |
| `(…, "linux", "386"|"riscv64")` | `("", err)` wrapping `ErrUnsupportedPlatform` |

The two lookup tables `osTitles` (`linux→Linux`, `darwin→Darwin`) and `archNames` (`amd64→x86_64`, `arm64→arm64`) mirror `.goreleaser.yaml`'s `archives.name_template` verbatim. **Intentionally fragile coupling** — we own both files, so if the template changes, this file changes (called out in the architect's spec, ticket body, and below under "GoReleaser drift"). Both unsupported-OS and unsupported-arch wrap the same sentinel; the error message names the inputs so callers can log them, but `errors.Is(err, ErrUnsupportedPlatform)` is the single branch.

`AssetName` does **not** validate that `version` parses as semver — that's `CompareVersions`'s job, and double-validating would create two error contracts to keep in sync. The wiring ticket guarantees `version` has already passed `CompareVersions` before calling `AssetName`.

### Checksums.txt parsing

Format is one line per asset, matching `sha256sum` output: `<sha256-hex>  <filename>` (two spaces). GoReleaser writes Unix line endings; we defensively `TrimRight` `\r` per line.

| Input | Outcome |
|-------|---------|
| Valid file, asset present | `(lowercase-hex, nil)` |
| Valid file, asset missing | `("", err)` wrapping `ErrAssetNotInChecksums` |
| Empty string | `("", err)` wrapping `ErrMalformedChecksums` |
| Whitespace-only / no parseable lines | `("", err)` wrapping `ErrMalformedChecksums` |
| CRLF line endings | parsed (`\r` trimmed) |
| Uppercase hex digest | normalised to lowercase on return |
| Garbage line followed by valid lines | garbage skipped silently |

The `sawAny` flag distinguishes "checksums.txt was junk" (`ErrMalformedChecksums` — upstream broken) from "checksums.txt was a valid file but didn't list our asset" (`ErrAssetNotInChecksums` — built without our platform, unrecoverable for this host). Different sentinels because they imply different upstream actions.

Lines that don't split into exactly two non-empty parts on `"  "` are skipped silently (forward-compatible with future header comments or trailing blanks). Per-line digest length / hex-ness is **not** validated — `VerifySHA256` will catch any mangling, and a regex check would couple two layers without observed benefit.

### Restart-command detection

| Probe | Result |
|-------|--------|
| `LaunchdPlistExists: true, UID: "501"` | `["launchctl", "kickstart", "-k", "gui/501/dev.pyrycode.pyry"]` |
| `SystemdUnitExists: true` | `["systemctl", "--user", "restart", "pyry"]` |
| Both true (tie-breaker) | launchd command — macOS is pyrycode's primary daily-driver platform; a stray systemd user unit on a Mac is more likely cruft than the active manager. The reverse case (a launchd plist on Linux) cannot occur — `launchctl` does not exist on Linux, so the probe returns false. |
| Neither true | `nil` — caller prints guidance |

`launchctl kickstart -k` SIGTERMs the running instance and starts a fresh one in a single command. `unload`/`load` would round-trip the plist and race with `KeepAlive=true`. `systemctl --user restart` is unconditional (matches the user's intent: "the binary changed; restart it"); `try-restart` would silently no-op if the unit is inactive, and `reload` requires `ExecReload=` which the unit file doesn't define.

`RestartProbe` is a struct rather than three positional bools so the call site labels each signal and survives signal additions in future tickets (e.g. an SMF unit on illumos, a Windows service entry) without breaking existing callers.

No `runtime.GOOS` filter inside `DetectRestartCommand` — the probe **is** the OS filter. The wiring ticket may simply not call `os.Stat` on the launchd path under Linux, in which case `LaunchdPlistExists` stays false and the systemd branch wins. That's a wiring decision, not a decision-half decision.

### Tar.gz extraction

| Input | Outcome |
|-------|---------|
| Tarball containing `pyry` plus other files | `(pyryBytes, nil)` — bytes returned unchanged |
| Tarball missing the requested binary | `nil` + err wrapping `ErrBinaryNotInArchive` |
| Garbage data (not a valid gzip stream) | `nil` + err wrapping `ErrMalformedArchive` |
| Valid gzip wrapping non-tar payload | `nil` + err wrapping `ErrMalformedArchive` |
| Empty input | `nil` + err wrapping `ErrMalformedArchive` |

Matching is `filepath.Base(hdr.Name) == binaryName`. GoReleaser lays files at the archive root (`pyry`, `LICENSE`, `docs/INSTALL.md`, …), so callers pass the bare filename. The `filepath.Base` strip is robust against a future GoReleaser config change that wraps everything in a top-level `pyry_v0.9.1/` directory; the cost is that a hypothetical `subdir/pyry` would also match — accepted because GoReleaser does not produce such archives, and a stricter exact-`hdr.Name` check would force this slice to be revisited the moment the archive layout changes.

Non-regular entries are skipped (`hdr.Typeflag != tar.TypeReg` continues the loop). Defends against a malicious/quirky archive that names its top-level directory `pyry/` (which would otherwise be returned as a zero-length "binary"). Cheap check, silent-and-weird failure mode without it.

No size cap (`io.LimitReader`) and no streaming variant. Release tarballs are ~10–20 MiB; the wiring ticket already holds `tgzData` in memory for checksum verification, and checksum-verifies before extraction, which forecloses the "attacker-controlled tarball" path. Revisit only if pyry ever ships a 1 GiB binary.

### SHA-256 verification

| Input | Outcome |
|-------|---------|
| `data` whose `sha256` matches `expectedHex` | `nil` |
| Mismatch | `err` wrapping `ErrChecksumMismatch`, message includes both digests |
| Empty `data` + correct empty-hash (`e3b0c442…b855`) | `nil` |
| Mixed-case `expectedHex` matching | `nil` (`strings.EqualFold`) |
| Empty `expectedHex` | `ErrChecksumMismatch` |

Takes `[]byte`, not `io.Reader`. The wiring ticket already needs the full tarball in memory for tar extraction (release tarballs are <10 MiB), so streaming would force the wiring ticket to either tee the body or hash twice. Switch to `io.Reader` only if tarball size ever demands it.

`crypto/sha256.Sum256` returns `[32]byte`; `hex.EncodeToString(sum[:])` produces 64 lowercase hex chars. No allocation beyond the result string. `strings.EqualFold` accepts mixed-case `expectedHex` cheaply; the error message normalises both to lowercase so logs are consistent regardless of input casing.

### HTTP fetcher

| Trigger | Returned error |
|---------|----------------|
| Invalid URL (unparseable) | `building GET <url>: <inner>` |
| DNS / connection failure | `GET <url>: <*url.Error>` |
| Non-2xx response (404, 500, …) | `GET <url>: unexpected status <code>` |
| Body read interrupted mid-stream | `reading response body from <url>: <inner>` |
| `ctx` cancelled before response | `GET <url>: <*url.Error wrapping context.Canceled>` |
| `ctx` cancelled during body read | `reading response body from <url>: <wrapped context.Canceled>` |

Both ctx-cancel rows satisfy `errors.Is(err, context.Canceled)` because `*url.Error.Unwrap` and `fmt.Errorf("…: %w", …)` participate in the unwrap chain. Tests assert the predicate, not the message.

`http.NewRequestWithContext` (not `http.NewRequest`) is the constructor — it propagates `ctx` to the transport so cancellation interrupts an in-flight read, not just a queued send. On the non-2xx path the body is drained up to 1 KiB via `io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))` so the underlying TCP connection can be reused; the bound caps reading megabytes of error HTML from a misconfigured server.

`io.ReadAll` on the response body is acceptable despite the tarball being 10–30 MiB — the wiring ticket holds the bytes in memory anyway to compute SHA-256 (`VerifySHA256(data []byte, …)`) and to feed tar extraction. A streaming `io.Reader` return would force the wiring layer to either tee or hash twice; revisit only if profiling shows memory pressure.

GitHub's API blanket-rejects requests with no `User-Agent`, which is why the header is unconditionally set rather than optional. The `pyry/<Version>` shape (vs `pyry/v<Version>`) is whatever `cmd/pyry/main.go`'s `Version` token expands to — match the binary's self-report so log correlation stays grep-friendly.

## Data flow

```
GitHub API
    │
    │ Fetcher.FetchLatestRelease(ctx, "owner/name")
    │   GET <BaseURL>/repos/<owner>/<name>/releases/latest
    │   User-Agent: pyry/<Version>
    ▼
[]byte (release JSON) ──> ParseLatestRelease(jsonBytes) ──> tag string
                                                              │
                                              ┌───────────────┴───────────────┐
                                              ▼                               ▼
                          CompareVersions(main.Version, tag)   AssetName(tag, GOOS, GOARCH)
                                              │                               │
                                              ▼                               ▼
                                       Older/Same/Newer                  assetName string
                                                                              │
                                Fetcher.FetchAsset(ctx, <release>/checksums.txt)
                                                                              │
                                                                              ▼
                                                      ParseChecksumsFile(body, assetName)
                                                                              │
                                                                              ▼
                                                                       expectedHex string
                                                                              │
                                  Fetcher.FetchAsset(ctx, <release>/<assetName>)
                                                                              │
                                                                  tarballBytes []byte
                                                                              │
                                                                              ▼
                                                      VerifySHA256(tarballBytes, expectedHex)
                                                                              │
                                                                              ▼
                                                    ExtractBinary(tarballBytes, "pyry")
                                                                              │
                                                                              ▼
                                                                       pyryBytes []byte
                                                                              │
                                                                              ▼
                                                  [next ticket: atomic on-disk replace]
                                                                              │
                                                                              ▼
              [wiring: probe ~/Library/LaunchAgents/dev.pyrycode.pyry.plist
                       + ~/.config/systemd/user/pyry.service + os.Getuid()]
                                                                              │
                                                                              ▼
                                                      DetectRestartCommand(probe)
                                                                              │
                                                                              ▼
                                  argv ([]string) or nil → exec.Command(argv[0], argv[1:]...)
```

The pure functions hold no state. The fetcher is also stateless across calls: each `Fetch*` constructs a fresh `*http.Request`; the `*http.Client` handles connection pooling internally. `*Fetcher` is safe for concurrent use — fields are read-only after construction, `*http.Client.Do` is concurrency-safe, no mutexes or goroutines internally. Package-level `osTitles` / `archNames` are written once at var-decl time and only read thereafter — Go's memory model permits concurrent reads of an unmutated map.

## Files

- `internal/update/version.go` — `ParseLatestRelease`, `CompareVersions`, `Comparison`, `ErrMalformedRelease`, `ErrInvalidVersion` (~127 LOC). Holds the package doc comment.
- `internal/update/version_test.go` — table-driven coverage for the two #179 functions (~155 LOC, two `t.Parallel` tables).
- `internal/update/checksum.go` — `AssetName`, `ParseChecksumsFile`, `VerifySHA256`, `ErrUnsupportedPlatform`, `ErrAssetNotInChecksums`, `ErrMalformedChecksums`, `ErrChecksumMismatch`, plus the `osTitles` / `archNames` lookup tables (~102 LOC). No package-doc comment — `version.go` already covers the package.
- `internal/update/checksum_test.go` — table-driven coverage for the three #180 functions (~219 LOC, three `t.Parallel` tables).
- `internal/update/restart.go` — `RestartProbe`, `DetectRestartCommand` (~37 LOC). Same shape as `checksum.go`'s pure-function siblings.
- `internal/update/restart_test.go` — table-driven coverage for the four AC cases (~48 LOC, single `t.Parallel` table; uses `slices.Equal` for argv comparison).
- `internal/update/fetch.go` — `Fetcher` struct + `FetchLatestRelease` + `FetchAsset` + private `get` helper + zero-value default helpers (`baseURL`, `httpClient`, `userAgent`) (~115 LOC). No package-doc comment — `version.go` covers the package.
- `internal/update/fetch_test.go` — `httptest.NewServer`-driven coverage for the two public methods (~158 LOC). Each subtest constructs its own server with `t.Cleanup(ts.Close)`.
- `internal/update/install.go` — `ExtractBinary`, `ErrBinaryNotInArchive`, `ErrMalformedArchive` (~63 LOC). Stdlib-only (`archive/tar`, `bytes`, `compress/gzip`, `errors`, `fmt`, `io`, `path/filepath`).
- `internal/update/install_test.go` — table-driven coverage for the AC's three cases plus two sentinel-coverage extensions (`valid_gzip_garbage_tar`, `empty_input`); ~120 LOC. Inline `buildTarGz(t, map[string][]byte) []byte` + `gzipOf(t, []byte) []byte` helpers construct fixtures via `bytes.Buffer` → `gzip.Writer` → `tar.Writer` — no `testdata/` directory.

## Configuration

The pure functions take their input via arguments. The fetcher's `Fetcher` struct is zero-value-usable; the wiring ticket sets `UserAgent = "pyry/" + main.Version` and typically installs an `HTTPClient = &http.Client{Timeout: 60*time.Second}`. `BaseURL` stays at the default in production; tests set it to `httptest.NewServer.URL`.

## GoReleaser drift

`osTitles` / `archNames` and the `pyry_<v>_<OS>_<arch>.tar.gz` skeleton in `checksum.go` duplicate `.goreleaser.yaml`'s `archives.name_template`. This is the **intentionally fragile** coupling called out in the ticket body — both files live in this repo, and `pyry update` only ever reads releases produced by this repo's own `release.yml` workflow, so there is no upstream-consumer compatibility concern. If `.goreleaser.yaml` changes, `checksum.go` changes.

No CI step currently fails on drift. A follow-up could run `goreleaser release --snapshot` and grep-check the produced filenames against `AssetName`'s output, but that's deferred until drift actually bites.

## Out of scope (sister tickets)

- Atomic binary replacement (the wiring ticket lands in `install.go` alongside `ExtractBinary` and consumes its `[]byte` output via `os.CreateTemp` + `os.Rename`).
- The probe half of restart detection — the wiring ticket performs `os.Stat` on the launchd plist + systemd unit file paths and `os.Getuid()`, then calls `DetectRestartCommand` with the results.
- Actually running the restart argv (`exec.Command`) and watching for restart success.
- `pyry update` CLI verb wiring + `--dry-run` flag.
- A `MapHostPlatform()` helper that wraps `runtime.GOOS` / `runtime.GOARCH` (the wiring ticket can pass the raw runtime constants — adding a host-detection helper before the wiring ticket exists is YAGNI).
- Streaming-hash variant of `VerifySHA256` (deferred until tarball size demands it).
- `Comparison.String()` for `slog`-friendly logging.
- SemVer 2.0.0 pre-release precedence (defer until pyry ships a real pre-release).
- Retry / exponential backoff on the HTTP fetcher (deliberately omitted; AC explicitly forbids retries — flaky network → operator re-runs `pyry update`).
- Caller-side timeouts inside the fetcher (caller's responsibility via `*http.Client`).
- `If-None-Match` / ETag-based caching against GitHub's anonymous 60/hr limit (interactive `pyry update` won't hit it; revisit if a future automated caller does).
- Exposing GitHub's 4xx body content in fetcher errors (the URL is in the message — operators can `curl` it themselves; revisit if real failures generate "404 unhelpful" complaints).
- A `Fetcherer` / `Mockable` interface — tests use `httptest.NewServer` directly to exercise the real `Fetcher` end-to-end. The wiring ticket may introduce a small consumer-defined interface there if it needs one.

## Related

- `cmd/pyry/main.go:53` — `var Version = "dev"` is the input shape that justifies `ErrInvalidVersion` as a clean sentinel for the wiring layer to branch on, the token the fetcher's `UserAgent` field embeds (`"pyry/" + Version`), and the value the wiring ticket will pass through `AssetName` after stripping the `dev` sentinel via `CompareVersions`'s error path.
- `.goreleaser.yaml:24-46` — build matrix and `archives.name_template` that `osTitles` / `archNames` mirror verbatim.
- [`lessons.md § Atomic on-disk writes`](../../lessons.md) — same "default decoder, not strict" rationale applied to `sessions.json`.
- [`internal/sessions/id.go`](../../../internal/sessions/id.go) — convention reference for tiny stdlib-only packages with table-driven tests.
- [`docs/specs/architecture/179-update-version-parsing.md`](../../specs/architecture/179-update-version-parsing.md), [`docs/specs/architecture/180-update-checksum.md`](../../specs/architecture/180-update-checksum.md), [`docs/specs/architecture/181-update-restart-detect.md`](../../specs/architecture/181-update-restart-detect.md), [`docs/specs/architecture/182-update-http-fetcher.md`](../../specs/architecture/182-update-http-fetcher.md), [`docs/specs/architecture/186-update-extract-binary.md`](../../specs/architecture/186-update-extract-binary.md) — build-time architecture specs.
- `launchd/dev.pyrycode.pyry.plist`, `systemd/pyry.service` — the service files whose presence the wiring-ticket probe checks; `pyry install-service` writes them.
