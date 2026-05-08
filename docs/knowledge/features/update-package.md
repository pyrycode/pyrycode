# `internal/update` — release parsing + version comparison

Pure-function half of pyrycode's self-update logic (`pyry update`). Lands in #179; the HTTP fetcher and binary replacer arrive in sister tickets.

## What it does

- `ParseLatestRelease(jsonBytes []byte) (string, error)` — extracts the `tag_name` field from a GitHub Releases API JSON payload (e.g. the body of `GET /repos/{owner}/{repo}/releases/latest`). Returns the verbatim tag (`"v0.9.1"`) or an error wrapping `ErrMalformedRelease`.
- `CompareVersions(current, latest string) (Comparison, error)` — reports `Older`/`Same`/`Newer` between two semver strings. Returns an error wrapping `ErrInvalidVersion` for inputs that don't parse.

Both are stdlib-only and side-effect-free. No I/O, no goroutines, no `context.Context`.

## Types & errors

```go
type Comparison int

const (
    Older Comparison = -1 // current is older than latest
    Same  Comparison = 0  // versions are equal
    Newer Comparison = 1  // current is newer than latest
)

var ErrMalformedRelease = errors.New("malformed release manifest")
var ErrInvalidVersion   = errors.New("invalid semver version")
```

`Comparison` mirrors `cmp.Compare` / `strings.Compare` conventions — negative/zero/positive for less/equal/greater. No `String()` method (YAGNI; add when a caller needs it for `slog`).

Sentinel errors are exported so callers can branch with `errors.Is`. Both are wrapped with context at the return site (`fmt.Errorf("decoding release JSON: %w", ErrMalformedRelease)`, etc.).

On error, `CompareVersions` returns `Same` rather than `Older`/`Newer` — the safer bias when a caller forgets to check the error: silently reports "no update needed" rather than a spurious downgrade.

## Behaviour

### Parsing

| Input | Outcome |
|-------|---------|
| Valid object with non-empty `tag_name` | `(tag, nil)` |
| Invalid JSON (truncated, garbage, empty bytes) | `ErrMalformedRelease` |
| Non-object top level (`[…]`, `"…"`, scalar) | `ErrMalformedRelease` |
| Object missing `tag_name` | `ErrMalformedRelease` |
| `"tag_name": ""` / `null` / non-string | `ErrMalformedRelease` |

Default `encoding/json` decoder — **not** `DisallowUnknownFields`. GitHub's payload has dozens of fields we don't read; strict decoding would convert "GitHub adds a field" into a load failure (same convention as `sessions.json` — see `lessons.md § Atomic on-disk writes`).

The tag is preserved **verbatim** — leading `v`, whitespace, pre-release / build-metadata suffixes all flow through unchanged. The comparator strips what it needs.

### Comparison

| Tolerance | Examples |
|-----------|----------|
| Leading `v` optional, both sides | `v0.9.1` ↔ `0.9.1` |
| Pre-release suffix stripped at first `-` | `v0.10.0-rc1` → compares as `0.10.0` |
| Build-metadata stripped at first `+` | `v0.10.0+build.5` → compares as `0.10.0` |
| Numeric (not lexical) component compare | `0.10.0 > 0.9.99` |

Rejects: empty string, `"dev"` (the `main.Version` sentinel for unreleased builds), too-few/too-many components, non-numeric components, empty components (`v0..1`), negative components.

The pre-release strip is a deliberate simplification. Pyry's tags are plain `major.minor.patch`; the comparator does not implement SemVer 2.0.0 pre-release precedence. If pyry ever publishes a real pre-release operators must distinguish (a beta channel), revisit this and add proper precedence — until then, defer.

`"dev"` is the documented out-of-band: callers detecting a dev build should special-case it before invoking `CompareVersions`. The wiring ticket will branch on this and print "running a dev build, skipping update check" or similar.

## Data flow

```
GitHub API ──[fetcher: sister ticket]──> []byte
                    │
                    ▼
       ParseLatestRelease(jsonBytes) ──> tag string
                                            │
                              ┌─────────────┘
                              ▼
       CompareVersions(main.Version, tag) ──> Comparison
                                                │
                                                ▼
                              [wiring: sister ticket]
```

No state, no I/O, no concurrency primitives. Safe for concurrent use by definition.

## Files

- `internal/update/version.go` — implementation (~127 LOC)
- `internal/update/version_test.go` — table-driven coverage (~155 LOC, two `t.Parallel` tables)

## Configuration

None. Pure functions take their input via arguments.

## Out of scope (sister tickets)

- HTTP fetcher (GET `releases/latest`, retries, timeouts).
- Checksum verification of the downloaded binary.
- Atomic binary replacement / restart detection.
- `pyry update` CLI verb wiring.
- `Comparison.String()` for `slog`-friendly logging.

## Related

- `cmd/pyry/main.go:53` — `var Version = "dev"` is the input shape that justifies `ErrInvalidVersion` as a clean sentinel for the wiring layer to branch on.
- [`lessons.md § Atomic on-disk writes`](../../lessons.md) — same "default decoder, not strict" rationale applied to `sessions.json`.
- [`internal/sessions/id.go`](../../../internal/sessions/id.go) — convention reference for tiny stdlib-only packages with table-driven tests.
