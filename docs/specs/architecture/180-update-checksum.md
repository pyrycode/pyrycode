# Spec: `internal/update` — asset-name templating + SHA-256 checksum verification (#180)

## Files to read first

- `internal/update/version.go` — sister file in the same package (landed in #179). Read it whole; this ticket adds a new file alongside it. Confirms the package doc comment, error-sentinel pattern (`ErrMalformedRelease`, `ErrInvalidVersion` — both `errors.New(...)` wrapped at the return site with `fmt.Errorf("…: %w", sentinel)`), and the table-driven test layout this ticket must mirror.
- `internal/update/version_test.go` — table-driven test layout convention (`t.Parallel()` per subtest, inline assertions, no helpers, `errors.Is` for sentinel-error assertions). Mirror exactly.
- `.goreleaser.yaml:24-46` — build matrix (`goos: [linux, darwin]`, `goarch: [amd64, arm64]`) and `archives.name_template`. The four-combo support set and the OS-capitalisation/arch-rewriting rules come from here verbatim.
- `cmd/pyry/main.go:53-54` — `var Version = "dev"`. Same module-level variable that #179's comparator already sees; the wiring ticket will pass `Version` through `AssetName` after stripping the `dev` sentinel via `CompareVersions`'s error path.
- `CODING-STYLE.md` §§ Naming, Error Handling, Testing — confirms the conventions already followed in `version.go`. Nothing new here; cited so a fresh reader doesn't have to re-derive them.

(QMD search on `pyrycode-docs` for "checksum" / "asset name" / "goreleaser" returns no prior decisions; greenfield within the package.)

## Context

Second pure-function slice of `pyry update`. The HTTP fetcher (sister ticket) needs to:

1. Construct the asset filename for the current host (so it knows which tarball to GET from the release).
2. Parse the `checksums.txt` body that GoReleaser publishes alongside the assets.
3. Verify the downloaded tarball bytes against the expected SHA-256.

All three are deterministic given inputs — pure functions, stdlib only, exhaustively unit-testable. Same layering motivation as #179: keeping the decision/verification logic pure means the I/O layers (HTTP fetcher, tar extractor, atomic-replace) can be tested in isolation against known-good helpers.

The asset-name template is intentionally fragile against `.goreleaser.yaml` changes. We own both files in the same repo; if the template changes, this function changes. There is no upstream-consumer compatibility concern — `pyry update` only ever reads releases produced by this repo's own `release.yml` workflow.

## Design

### Package

New file inside the existing `internal/update` package (introduced in #179):

```
internal/update/
  version.go           // (existing — #179)
  version_test.go      // (existing — #179)
  checksum.go          // NEW — AssetName, ParseChecksumsFile, VerifySHA256, error sentinels
  checksum_test.go     // NEW — table-driven tests for all three functions
```

No package-doc comment in `checksum.go`: the package doc on `version.go` (already written: *"Package update implements pyrycode's self-update logic: release manifest parsing, version comparison, fetch, and replace."*) covers the whole package and forward-stated this very ticket. Don't duplicate it.

### Function signatures

```go
// AssetName returns the GoReleaser-produced tarball filename for the given
// version and host platform, e.g. AssetName("v0.9.1", "darwin", "arm64") →
// "pyry_0.9.1_Darwin_arm64.tar.gz".
//
// A leading "v" on version is stripped (GoReleaser's name template uses the
// bare semver). The os/arch values follow Go's runtime.GOOS / runtime.GOARCH
// vocabulary: only the four combinations actually built by .goreleaser.yaml
// are supported (linux/darwin × amd64/arm64); any other combination returns
// an error wrapping ErrUnsupportedPlatform.
func AssetName(version, goos, goarch string) (string, error)

// ParseChecksumsFile finds the SHA-256 hex digest for assetName inside the
// contents of a GoReleaser-produced checksums.txt. Each non-empty line of
// the file is expected to be "<sha256-hex>  <filename>" (two spaces between
// the digest and the filename, matching `sha256sum` output). Lines that
// don't match are skipped silently — GoReleaser may add trailing blank
// lines or future header comments.
//
// On success returns the lowercase hex digest (64 characters). Returns an
// error wrapping ErrAssetNotInChecksums if no matching line is found, or
// ErrMalformedChecksums if the file is empty or contains no parseable
// lines at all.
func ParseChecksumsFile(text, assetName string) (string, error)

// VerifySHA256 returns nil iff sha256(data) lowercase-hex equals expectedHex.
// On mismatch the error includes both the expected and actual digests for
// diagnostic logging. expectedHex is matched case-insensitively (callers
// pass a value sourced from ParseChecksumsFile, which already lowercases,
// but this guards against future callers that don't).
func VerifySHA256(data []byte, expectedHex string) error
```

### Error contract

Three exported sentinels so callers can branch with `errors.Is`:

```go
// ErrUnsupportedPlatform is returned by AssetName for any os/arch combo
// not built by .goreleaser.yaml.
var ErrUnsupportedPlatform = errors.New("unsupported os/arch")

// ErrAssetNotInChecksums is returned by ParseChecksumsFile when the
// requested asset is not listed in the checksums.txt body.
var ErrAssetNotInChecksums = errors.New("asset not listed in checksums")

// ErrMalformedChecksums is returned by ParseChecksumsFile when the input
// is empty or contains no parseable "<hex>  <name>" lines at all.
var ErrMalformedChecksums = errors.New("malformed checksums file")

// ErrChecksumMismatch is returned by VerifySHA256 when the computed digest
// does not match the expected one.
var ErrChecksumMismatch = errors.New("sha256 checksum mismatch")
```

Wrapped at the return site, mirroring `version.go`:

```go
return "", fmt.Errorf("asset name for %s/%s: %w", goos, goarch, ErrUnsupportedPlatform)
return "", fmt.Errorf("looking up %q: %w", assetName, ErrAssetNotInChecksums)
return fmt.Errorf("expected %s, got %s: %w", expectedHex, actualHex, ErrChecksumMismatch)
```

### Implementation sketch

`AssetName`:

```go
func AssetName(version, goos, goarch string) (string, error) {
    osTitle, ok := osTitles[goos]
    if !ok {
        return "", fmt.Errorf("asset name for %s/%s: %w", goos, goarch, ErrUnsupportedPlatform)
    }
    archName, ok := archNames[goarch]
    if !ok {
        return "", fmt.Errorf("asset name for %s/%s: %w", goos, goarch, ErrUnsupportedPlatform)
    }
    v := strings.TrimPrefix(version, "v")
    return fmt.Sprintf("pyry_%s_%s_%s.tar.gz", v, osTitle, archName), nil
}

var (
    osTitles  = map[string]string{"linux": "Linux", "darwin": "Darwin"}
    archNames = map[string]string{"amd64": "x86_64", "arm64": "arm64"}
)
```

Notes:

- The two maps encode the GoReleaser template literally. They live as package-level `var`s (not `const`s — Go maps can't be const) keyed by `runtime.GOOS` / `runtime.GOARCH` strings so the wiring ticket can pass those directly.
- Empty `version` is permitted; it produces `"pyry__Darwin_arm64.tar.gz"`. The wiring ticket guarantees it has a parseable version before calling (it has already passed `CompareVersions`), so we don't add a length check that callers would have to satisfy redundantly. If a future caller passes empty, the resulting 404 from the HTTP fetcher will be a perfectly serviceable error.
- We do **not** validate that `version` parses as semver. That's `CompareVersions`'s job; double-validating creates two error contracts to keep in sync.
- Both unsupported-OS and unsupported-arch return the same wrapped sentinel — the error message names the inputs so the caller's log lines remain unambiguous, but `errors.Is(err, ErrUnsupportedPlatform)` is the single branch.

`ParseChecksumsFile`:

```go
func ParseChecksumsFile(text, assetName string) (string, error) {
    sawAny := false
    for _, line := range strings.Split(text, "\n") {
        line = strings.TrimRight(line, "\r")
        if line == "" {
            continue
        }
        // Expected: "<64-hex>  <filename>". Use SplitN(line, "  ", 2) so
        // filenames containing single spaces (none of ours do, but be
        // robust to future-asset names) survive.
        parts := strings.SplitN(line, "  ", 2)
        if len(parts) != 2 {
            continue
        }
        sawAny = true
        if parts[1] == assetName {
            return strings.ToLower(parts[0]), nil
        }
    }
    if !sawAny {
        return "", fmt.Errorf("checksums file empty or unparseable: %w", ErrMalformedChecksums)
    }
    return "", fmt.Errorf("looking up %q: %w", assetName, ErrAssetNotInChecksums)
}
```

Notes:

- We don't validate that `parts[0]` is exactly 64 lowercase hex characters during parsing — `VerifySHA256` will fail loudly if a line is malformed in a way that escapes our skip filter, and adding a regex here is YAGNI for a file format we generate ourselves.
- `strings.TrimRight(line, "\r")` handles CRLF defensively. GoReleaser writes Unix line endings; this is one extra string op per line for a vanishingly small future-proofing benefit. Worth keeping.
- The `sawAny` flag distinguishes "checksums.txt was junk" from "checksums.txt was a valid file but didn't list our asset" — different error sentinels because they imply different upstream actions (junk = upstream broken; missing = built without our platform = unrecoverable for this host).

`VerifySHA256`:

```go
func VerifySHA256(data []byte, expectedHex string) error {
    sum := sha256.Sum256(data)
    actualHex := hex.EncodeToString(sum[:])
    if !strings.EqualFold(actualHex, expectedHex) {
        return fmt.Errorf("expected %s, got %s: %w",
            strings.ToLower(expectedHex), actualHex, ErrChecksumMismatch)
    }
    return nil
}
```

Notes:

- `crypto/sha256.Sum256` returns `[32]byte`; `hex.EncodeToString(sum[:])` produces 64 lowercase hex chars. No allocation beyond the result string.
- `strings.EqualFold` accepts mixed-case `expectedHex` cheaply. The error message normalises both to lowercase so logs are consistent regardless of input casing.
- The function takes `[]byte` rather than `io.Reader` because the wiring ticket already needs the full tarball in memory for tar extraction, and our release tarballs are <10 MiB. Switching to streaming hashing would force the wiring ticket to either tee the body or hash twice. If tarballs ever grow large enough to matter, we revisit.

### Data flow

```
┌─ wiring ticket: HTTP GET <release URL>/checksums.txt ─┐
│                                                        ▼
│                                       checksumsTxt string
│                                                        │
│   AssetName(latest, runtime.GOOS, runtime.GOARCH)      │
│                ─────────────────►  assetName           │
│                                                        ▼
│                          ParseChecksumsFile(checksumsTxt, assetName)
│                                                        │
│                                                        ▼
│                                                expectedHex string
│                                                        │
└─ wiring ticket: HTTP GET <release URL>/<assetName> ────┤
                                  │                      ▼
                            tarballBytes []byte ─► VerifySHA256(tarballBytes, expectedHex)
                                                        │
                                                        ▼
                                                  nil | error
                                                        │
                                                        ▼
                                  [next ticket: untar + atomic replace + restart]
```

No state, no I/O, no goroutines, no `context.Context`. Three pure functions composed by the wiring ticket.

## Concurrency model

All three functions are pure and stateless. Safe for concurrent use by definition. No `sync` primitives, no goroutines, no context parameter.

The package-level `osTitles` / `archNames` maps are written once at package init (var declaration) and only read thereafter — Go's memory model permits concurrent reads of an unmutated map.

## Error handling

| Function | Input | Outcome |
|---|---|---|
| `AssetName` | `("v0.9.1", "darwin", "arm64")` | `("pyry_0.9.1_Darwin_arm64.tar.gz", nil)` |
| `AssetName` | `("0.9.1", "linux", "amd64")` | `("pyry_0.9.1_Linux_x86_64.tar.gz", nil)` (no `v` is a no-op) |
| `AssetName` | `("v0.9.1", "windows", "amd64")` | `("", ErrUnsupportedPlatform)` |
| `AssetName` | `("v0.9.1", "linux", "386")` | `("", ErrUnsupportedPlatform)` |
| `AssetName` | `("v0.9.1", "darwin", "riscv64")` | `("", ErrUnsupportedPlatform)` |
| `ParseChecksumsFile` | valid file, asset present | `("<64-hex>", nil)` |
| `ParseChecksumsFile` | valid file, asset missing | `("", ErrAssetNotInChecksums)` |
| `ParseChecksumsFile` | empty string | `("", ErrMalformedChecksums)` |
| `ParseChecksumsFile` | only blank/garbage lines | `("", ErrMalformedChecksums)` |
| `ParseChecksumsFile` | uppercase hex line | `("<lowercased>", nil)` (we normalise) |
| `VerifySHA256` | bytes whose sha256 == hex | `nil` |
| `VerifySHA256` | bytes whose sha256 ≠ hex | `ErrChecksumMismatch` (message includes both digests) |
| `VerifySHA256` | empty `data`, correct hash | `nil` (sanity case in tests) |
| `VerifySHA256` | mixed-case `expectedHex` matching | `nil` (case-insensitive compare) |

`ParseChecksumsFile` returning `""` on error is conventional — callers must check the error first; the value is meaningless when err != nil. Same bias as `CompareVersions` returning `Same` on error: a misuse silently routes through "no match" rather than a wrong match.

## Testing strategy

Single test file `checksum_test.go`, table-driven, stdlib `testing` only. Mirror `version_test.go` exactly: subtest per row, `t.Parallel()` at the top of each, inline assertions, `errors.Is` for sentinel-error checks.

### `TestAssetName` cases

| name | version | goos | goarch | want | want err |
|---|---|---|---|---|---|
| `darwin_arm64` | `v0.9.1` | `darwin` | `arm64` | `pyry_0.9.1_Darwin_arm64.tar.gz` | nil |
| `darwin_amd64` | `v0.9.1` | `darwin` | `amd64` | `pyry_0.9.1_Darwin_x86_64.tar.gz` | nil |
| `linux_arm64` | `v0.9.1` | `linux` | `arm64` | `pyry_0.9.1_Linux_arm64.tar.gz` | nil |
| `linux_amd64` | `v0.9.1` | `linux` | `amd64` | `pyry_0.9.1_Linux_x86_64.tar.gz` | nil |
| `version_no_v_prefix` | `0.9.1` | `darwin` | `arm64` | `pyry_0.9.1_Darwin_arm64.tar.gz` | nil |
| `unsupported_os_windows` | `v0.9.1` | `windows` | `amd64` | — | `ErrUnsupportedPlatform` |
| `unsupported_os_freebsd` | `v0.9.1` | `freebsd` | `amd64` | — | `ErrUnsupportedPlatform` |
| `unsupported_arch_386` | `v0.9.1` | `linux` | `386` | — | `ErrUnsupportedPlatform` |
| `unsupported_arch_riscv` | `v0.9.1` | `linux` | `riscv64` | — | `ErrUnsupportedPlatform` |
| `error_message_names_inputs` | `v0.9.1` | `windows` | `amd64` | — | error message contains `windows` and `amd64` |

(The last row is a separate sub-assertion verifying `err.Error()` contains both bad inputs — required by the AC: *"errors on unsupported os/arch combos with a clear message naming the inputs"*.)

### `TestParseChecksumsFile` cases

Construct a representative fixture inline:

```go
const sample = `abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789  pyry_0.9.1_Darwin_arm64.tar.gz
1111111111111111111111111111111111111111111111111111111111111111  pyry_0.9.1_Darwin_x86_64.tar.gz
2222222222222222222222222222222222222222222222222222222222222222  pyry_0.9.1_Linux_arm64.tar.gz
3333333333333333333333333333333333333333333333333333333333333333  pyry_0.9.1_Linux_x86_64.tar.gz
`
```

| name | text | assetName | want | want err |
|---|---|---|---|---|
| `asset_present_first` | `sample` | `pyry_0.9.1_Darwin_arm64.tar.gz` | `abcdef…` | nil |
| `asset_present_last` | `sample` | `pyry_0.9.1_Linux_x86_64.tar.gz` | `3333…` | nil |
| `asset_missing` | `sample` | `pyry_0.9.1_Linux_riscv64.tar.gz` | — | `ErrAssetNotInChecksums` |
| `empty_input` | `""` | any | — | `ErrMalformedChecksums` |
| `whitespace_only` | `"\n\n  \n"` | any | — | `ErrMalformedChecksums` |
| `no_parseable_lines` | `"hello world\nnot a checksum\n"` | any | — | `ErrMalformedChecksums` |
| `crlf_line_endings` | `sample` rewritten with `\r\n` | `pyry_0.9.1_Darwin_arm64.tar.gz` | `abcdef…` | nil |
| `uppercase_hex_normalised` | line with `ABCDEF…` digest | matching name | lowercase `abcdef…` | nil |
| `trailing_blank_line` | `sample` (already has trailing `\n`, asset present) | `pyry_0.9.1_Darwin_arm64.tar.gz` | `abcdef…` | nil |
| `garbage_then_valid` | `"junk line\n` + `sample` | `pyry_0.9.1_Darwin_arm64.tar.gz` | `abcdef…` | nil (garbage line skipped) |

Use `errors.Is(err, ErrAssetNotInChecksums)` and `errors.Is(err, ErrMalformedChecksums)` as appropriate.

### `TestVerifySHA256` cases

The empty-data SHA-256 is the well-known constant `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`. Use it as the empty-input sanity case (specified in AC).

| name | data | expectedHex | want err |
|---|---|---|---|
| `empty_data_correct_hash` | `[]byte{}` | `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855` | nil |
| `nonempty_data_correct_hash` | `[]byte("hello world")` | `b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9` | nil |
| `mismatch` | `[]byte("hello world")` | `0000000000000000000000000000000000000000000000000000000000000000` | `ErrChecksumMismatch` |
| `mismatch_message_includes_digests` | `[]byte("hello world")` | `00…` | error message contains both `b94d…` and `0000…` |
| `case_insensitive_match` | `[]byte("hello world")` | `B94D27B9934D3E08A52E52D7DA7DABFAC484EFE37A5380EE9088F7ACE2EFCDE9` | nil |
| `empty_expected_hex` | `[]byte("hello world")` | `""` | `ErrChecksumMismatch` (empty ≠ valid digest) |

`mismatch_message_includes_digests` is a substring-on-`err.Error()` assertion verifying the diagnostic is useful.

### Test conventions

- `t.Parallel()` at the top of each subtest.
- Subtests named in `snake_case` matching the table `name` field (`t.Run(tc.name, ...)`).
- No external fixtures — every input is an inline string literal or `[]byte`.
- One assertion helper inline (not a helper file); the table is the documentation.
- Run with `go test -race ./internal/update/...` as the verification command — same as #179.

## Open questions

1. **Whether to also expose a `MapHostPlatform()` helper that wraps `runtime.GOOS` / `runtime.GOARCH` into the enum the wiring ticket cares about.** Out of scope here — the wiring ticket can pass the raw runtime constants, and adding a host-detection helper before the wiring ticket exists is YAGNI.
2. **Streaming hashing for large tarballs.** Today's tarballs are <10 MiB. If they ever exceed memory limits, switch `VerifySHA256` to take an `io.Reader` and update callers. Logged here so the next reader sees the deferred decision rather than re-deriving it.
3. **Should `ParseChecksumsFile` validate the digest length / hex-ness of each line?** Currently no — we generate the file ourselves and `VerifySHA256` will catch any mangling. Adding a regex check would couple two layers without observed benefit. Defer.
4. **GoReleaser template drift.** The `osTitles` / `archNames` maps and the `pyry_<v>_<OS>_<arch>.tar.gz` skeleton are duplicated between `.goreleaser.yaml` and `checksum.go`. This is the *intentionally fragile* coupling called out in the ticket body. If `.goreleaser.yaml` changes, this file changes. No test currently fails on drift — a follow-up could add a CI step that runs `goreleaser release --snapshot` and grep-checks the produced filenames against `AssetName`'s output, but that's out of scope. Logged.

## Out of scope

- HTTP fetching of the release tarball or `checksums.txt` (sister ticket).
- Tar extraction of the downloaded asset (separate ticket).
- Atomic binary replacement / restart detection (separate ticket).
- A `MapHostPlatform()` host-detection helper (next ticket if needed).
- Streaming-hash variant of `VerifySHA256` (deferred until tarball size demands it).
- Any CLI wiring; this ticket adds zero callers.
