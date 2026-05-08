# `internal/update` — release parsing, version comparison, asset naming, SHA-256 verification + restart-command detection

Pure-function half of pyrycode's self-update logic (`pyry update`). #179 landed the JSON parser + semver comparator; #180 added asset-name templating + checksum parsing + verification; #181 adds restart-command detection. The HTTP fetcher, tar extractor, atomic-replace, and restart probe land in sister tickets.

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

All six are stdlib-only and side-effect-free. No I/O, no goroutines, no `context.Context`.

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
```

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

## Data flow

```
GitHub API ──[fetcher: sister ticket]──> []byte (release JSON)
                    │
                    ▼
       ParseLatestRelease(jsonBytes) ──> tag string
                                            │
                              ┌─────────────┴─────────────┐
                              ▼                           ▼
       CompareVersions(main.Version, tag)   AssetName(tag, GOOS, GOARCH)
                    │                                     │
                    ▼                                     ▼
              Older/Same/Newer                       assetName string
                                                          │
                       [fetcher: GET <release>/checksums.txt]
                                                          │
                                                          ▼
                                  ParseChecksumsFile(body, assetName)
                                                          │
                                                          ▼
                                                  expectedHex string
                                                          │
                       [fetcher: GET <release>/<assetName>]
                                                          │
                                          tarballBytes []byte
                                                          │
                                                          ▼
                                  VerifySHA256(tarballBytes, expectedHex)
                                                          │
                                                          ▼
                          [next ticket: untar + atomic replace]
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

No state, no I/O, no concurrency primitives. Safe for concurrent use by definition. Package-level `osTitles` / `archNames` are written once at var-decl time and only read thereafter — Go's memory model permits concurrent reads of an unmutated map.

## Files

- `internal/update/version.go` — `ParseLatestRelease`, `CompareVersions`, `Comparison`, `ErrMalformedRelease`, `ErrInvalidVersion` (~127 LOC). Holds the package doc comment.
- `internal/update/version_test.go` — table-driven coverage for the two #179 functions (~155 LOC, two `t.Parallel` tables).
- `internal/update/checksum.go` — `AssetName`, `ParseChecksumsFile`, `VerifySHA256`, `ErrUnsupportedPlatform`, `ErrAssetNotInChecksums`, `ErrMalformedChecksums`, `ErrChecksumMismatch`, plus the `osTitles` / `archNames` lookup tables (~102 LOC). No package-doc comment — `version.go` already covers the package.
- `internal/update/checksum_test.go` — table-driven coverage for the three #180 functions (~219 LOC, three `t.Parallel` tables).
- `internal/update/restart.go` — `RestartProbe`, `DetectRestartCommand` (~37 LOC). Same shape as `checksum.go`'s pure-function siblings.
- `internal/update/restart_test.go` — table-driven coverage for the four AC cases (~48 LOC, single `t.Parallel` table; uses `slices.Equal` for argv comparison).

## Configuration

None. Pure functions take their input via arguments.

## GoReleaser drift

`osTitles` / `archNames` and the `pyry_<v>_<OS>_<arch>.tar.gz` skeleton in `checksum.go` duplicate `.goreleaser.yaml`'s `archives.name_template`. This is the **intentionally fragile** coupling called out in the ticket body — both files live in this repo, and `pyry update` only ever reads releases produced by this repo's own `release.yml` workflow, so there is no upstream-consumer compatibility concern. If `.goreleaser.yaml` changes, `checksum.go` changes.

No CI step currently fails on drift. A follow-up could run `goreleaser release --snapshot` and grep-check the produced filenames against `AssetName`'s output, but that's deferred until drift actually bites.

## Out of scope (sister tickets)

- HTTP fetcher (GET `releases/latest`, GET `<release>/checksums.txt`, GET `<release>/<assetName>`, retries, timeouts, `context.Context`).
- Tar extraction of the downloaded asset.
- Atomic binary replacement.
- The probe half of restart detection — the wiring ticket performs `os.Stat` on the launchd plist + systemd unit file paths and `os.Getuid()`, then calls `DetectRestartCommand` with the results.
- Actually running the restart argv (`exec.Command`) and watching for restart success.
- `pyry update` CLI verb wiring + `--dry-run` flag.
- A `MapHostPlatform()` helper that wraps `runtime.GOOS` / `runtime.GOARCH` (the wiring ticket can pass the raw runtime constants — adding a host-detection helper before the wiring ticket exists is YAGNI).
- Streaming-hash variant of `VerifySHA256` (deferred until tarball size demands it).
- `Comparison.String()` for `slog`-friendly logging.
- SemVer 2.0.0 pre-release precedence (defer until pyry ships a real pre-release).

## Related

- `cmd/pyry/main.go:53` — `var Version = "dev"` is the input shape that justifies `ErrInvalidVersion` as a clean sentinel for the wiring layer to branch on, and the value the wiring ticket will pass through `AssetName` after stripping the `dev` sentinel via `CompareVersions`'s error path.
- `.goreleaser.yaml:24-46` — build matrix and `archives.name_template` that `osTitles` / `archNames` mirror verbatim.
- [`lessons.md § Atomic on-disk writes`](../../lessons.md) — same "default decoder, not strict" rationale applied to `sessions.json`.
- [`internal/sessions/id.go`](../../../internal/sessions/id.go) — convention reference for tiny stdlib-only packages with table-driven tests.
- [`docs/specs/architecture/179-update-version-parsing.md`](../../specs/architecture/179-update-version-parsing.md), [`docs/specs/architecture/180-update-checksum.md`](../../specs/architecture/180-update-checksum.md), [`docs/specs/architecture/181-update-restart-detect.md`](../../specs/architecture/181-update-restart-detect.md) — build-time architecture specs.
- `launchd/dev.pyrycode.pyry.plist`, `systemd/pyry.service` — the service files whose presence the wiring-ticket probe checks; `pyry install-service` writes them.
