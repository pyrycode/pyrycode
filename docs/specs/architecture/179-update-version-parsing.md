# Spec: `internal/update` — release JSON parsing + semver comparison (#179)

## Files to read first

- `cmd/pyry/main.go:53-54` — `var Version = "dev"`. This is the string the wiring ticket will pass into `CompareVersions(current=Version, latest=…)`. The `"dev"` value (no `v` prefix, not semver) is a real input shape the comparator must reject cleanly so the caller can branch.
- `cmd/pyry/main.go:151` — `fmt.Println("pyry", Version)` for the `--version` output. Confirms the bare token form (no `v` prefix in our binary's self-report).
- `internal/sessions/id.go` (any of the small files) — example of a tiny, focused stdlib-only package with table-driven tests as the convention reference.
- `CODING-STYLE.md` §§ Naming, Error Handling, Testing — package naming (lowercase, single word), error wrapping (`fmt.Errorf("…: %w", err)`), table-driven tests with `name` field, stdlib `testing` only.
- `docs/PROJECT-MEMORY.md` — current package layout (`internal/{control,e2e,install,sessions,supervisor}`); confirms `internal/update` is a brand-new sibling.

(No prior decisions in `pyrycode-docs` on update logic; this is greenfield.)

## Context

First slice of `pyry update`. The HTTP fetcher (sister ticket) will GET `https://api.github.com/repos/pyrycode/pyrycode/releases/latest` and pass the raw bytes into `ParseLatestRelease`. The wiring ticket will compare `main.Version` against the parsed tag and decide whether to fetch/install a newer build.

Isolating these two functions matters because:

- They are 100% deterministic given input bytes — exhaustively unit-testable.
- The HTTP fetcher and the install/restart flow are both side-effecting and platform-dependent; keeping the decision logic pure means those layers can be tested in isolation against a known-good comparator.
- Same shape as `internal/sessions/id.go` (`NewID`, `ValidID` — pure, stdlib only, fully tested).

## Design

### Package

New package `internal/update`, single file `version.go`, single test file `version_test.go`.

```
internal/update/
  version.go        // ParseLatestRelease, CompareVersions, Comparison, exported errors
  version_test.go   // table-driven tests for both functions
```

Package doc comment on `version.go` (one sentence): *"Package update implements pyrycode's self-update logic: release manifest parsing, version comparison, fetch, and replace."* Even though only the parsing/comparison piece lands in #179, the doc comment is forward-stated so the package's purpose is legible from day one (the fetcher and replacer arrive in sister tickets).

### Types

```go
// Comparison is the result of comparing two semver versions.
type Comparison int

const (
    Older Comparison = -1 // current is older than latest
    Same  Comparison = 0  // versions are equal
    Newer Comparison = 1  // current is newer than latest
)
```

Rationale:

- `int` typed enum with explicit `-1 / 0 / 1` mirrors `cmp.Compare`, `bytes.Compare`, `strings.Compare`. Idiomatic Go.
- The `String()` method is **not** required (not in AC). Skip it — adding it now is YAGNI.

### Function signatures

```go
// ParseLatestRelease extracts the tag name from a GitHub Releases API JSON
// payload (e.g. the response body of GET /repos/{owner}/{repo}/releases/latest).
//
// On success it returns the value of the top-level "tag_name" field (e.g.
// "v0.9.1" — the leading "v" is preserved verbatim; CompareVersions strips it).
//
// Returns an error if jsonBytes is not valid JSON, if the document is not a
// JSON object, if "tag_name" is absent, or if "tag_name" is not a string.
// The empty string is also rejected as an absent tag.
func ParseLatestRelease(jsonBytes []byte) (string, error)

// CompareVersions compares two semver version strings and reports whether
// current is older than, equal to, or newer than latest.
//
// Both arguments may carry a leading "v" (e.g. "v0.9.1") or omit it
// ("0.9.1") — the prefix is stripped before parsing. Pre-release and
// build-metadata suffixes are stripped at the first '-' or '+' before
// numeric parsing (e.g. "v0.10.0-rc1" is compared as "0.10.0"). This is a
// deliberate simplification: pyry's tags are plain major.minor.patch and
// the comparator does not need to implement SemVer 2.0.0 precedence rules
// for pre-releases.
//
// Returns an error if either argument cannot be parsed into three
// non-negative integers separated by dots after the prefix and suffix
// stripping above. The sentinel main.Version value "dev" is one such case
// — callers detecting a "dev" build should special-case it before
// invoking CompareVersions.
func CompareVersions(current, latest string) (Comparison, error)
```

### Error contract

Two exported sentinel errors so callers can branch on them with `errors.Is`:

```go
// ErrMalformedRelease is returned by ParseLatestRelease when the JSON is
// invalid, not an object, or missing/empty "tag_name".
var ErrMalformedRelease = errors.New("malformed release manifest")

// ErrInvalidVersion is returned by CompareVersions when either argument is
// not a parseable semver string.
var ErrInvalidVersion = errors.New("invalid semver version")
```

Both errors are wrapped with context at the return site:

```go
return "", fmt.Errorf("decoding release JSON: %w", ErrMalformedRelease)
return "", fmt.Errorf("missing tag_name field: %w", ErrMalformedRelease)
return Same, fmt.Errorf("parsing %q: %w", input, ErrInvalidVersion)
```

This matches the project convention (see `CODING-STYLE.md` §Error Handling) and lets the wiring ticket distinguish "GitHub returned junk" from "our binary's `Version` is `dev`" with `errors.Is` rather than string matching.

### Implementation sketch

`ParseLatestRelease`:

```go
func ParseLatestRelease(jsonBytes []byte) (string, error) {
    var payload struct {
        TagName string `json:"tag_name"`
    }
    if err := json.Unmarshal(jsonBytes, &payload); err != nil {
        return "", fmt.Errorf("decoding release JSON: %w", ErrMalformedRelease)
    }
    if payload.TagName == "" {
        return "", fmt.Errorf("missing tag_name field: %w", ErrMalformedRelease)
    }
    return payload.TagName, nil
}
```

Notes:

- Default `encoding/json` decoder. **Do not** use `DisallowUnknownFields` — GitHub's release payload has dozens of fields we don't care about (see lessons.md §"Atomic on-disk writes" for the same convention applied to `sessions.json`).
- A non-object JSON document (e.g. `[1,2,3]` or `null` or `"hello"`) trips `json.Unmarshal` into the struct and either errors (array, string) or yields `TagName == ""` (null, empty object) — both paths return `ErrMalformedRelease`. No separate validation needed.
- Whitespace in `tag_name` (e.g. `" v0.9.1 "`) is **not** trimmed — this is GitHub's own value, never operator-typed, and trimming would mask a real upstream bug. Document this behaviour in the doc comment if a reviewer asks; otherwise implicit.

`CompareVersions`:

```go
func CompareVersions(current, latest string) (Comparison, error) {
    cMaj, cMin, cPat, err := parseSemver(current)
    if err != nil {
        return Same, err
    }
    lMaj, lMin, lPat, err := parseSemver(latest)
    if err != nil {
        return Same, err
    }
    switch {
    case cMaj != lMaj:
        return cmpInt(cMaj, lMaj), nil
    case cMin != lMin:
        return cmpInt(cMin, lMin), nil
    default:
        return cmpInt(cPat, lPat), nil
    }
}

func parseSemver(s string) (maj, min, pat int, err error) {
    s = strings.TrimPrefix(s, "v")
    // Strip pre-release ("-rc1") and build-metadata ("+build.5") suffixes.
    if i := strings.IndexAny(s, "-+"); i >= 0 {
        s = s[:i]
    }
    parts := strings.Split(s, ".")
    if len(parts) != 3 {
        return 0, 0, 0, fmt.Errorf("parsing %q: %w", s, ErrInvalidVersion)
    }
    // … strconv.Atoi each part; reject negatives; wrap into ErrInvalidVersion on failure.
}

func cmpInt(a, b int) Comparison {
    switch {
    case a < b:
        return Older
    case a > b:
        return Newer
    default:
        return Same
    }
}
```

Notes:

- `strings.TrimPrefix` is a no-op when the prefix is absent — handles both `"v0.9.1"` and `"0.9.1"` symmetrically.
- The suffix strip happens **before** the dot split. `"v0.10.0-rc1"` → `"0.10.0-rc1"` → `"0.10.0"` → `["0","10","0"]`.
- Negative components (`"v1.-1.0"`) are rejected via the `strconv.Atoi` result `< 0` check. (Atoi *will* parse `-1`; we reject explicitly.)
- Empty components after split (`"v1..0"`) trip `strconv.Atoi("")` which returns an error.
- Leading zeros (`"v01.02.03"`) are accepted by `strconv.Atoi` and we don't reject them. SemVer 2.0.0 forbids leading zeros; we don't care — our tags don't use them, and the wiring ticket will only ever pass GitHub-supplied tags through here.

### Data flow

```
GitHub API ──[fetcher: sister ticket]──> []byte
                                             │
                                             ▼
                              ParseLatestRelease(jsonBytes)
                                             │
                                             ▼
                                      tagName string ("v0.9.1")
                                             │
                              ┌──────────────┘
                              │
                              ▼
              CompareVersions(main.Version, tagName)
                              │
                              ▼
                     Comparison + error
                              │
                              ▼
                  [wiring: sister ticket — decide whether to fetch/install]
```

No state, no I/O, no goroutines, no context. Pure functions returning value+error.

## Concurrency model

Both functions are pure and stateless. Safe for concurrent use by definition. No `sync` primitives, no goroutines, no `context.Context` parameter. (The HTTP fetcher in the sister ticket will take a `context.Context`; this layer doesn't need one because it never blocks.)

## Error handling

Failure modes and responses:

| Input | `ParseLatestRelease` outcome |
|-------|------------------------------|
| Valid JSON object with non-empty `"tag_name"` | `(tag, nil)` |
| Invalid JSON (e.g. `"{"` or random bytes) | `("", err)` wrapping `ErrMalformedRelease` |
| JSON array or scalar at top level | `("", err)` wrapping `ErrMalformedRelease` |
| JSON object missing `"tag_name"` | `("", err)` wrapping `ErrMalformedRelease` |
| JSON object with `"tag_name": ""` | `("", err)` wrapping `ErrMalformedRelease` |
| JSON object with `"tag_name": 42` (wrong type) | `("", err)` wrapping `ErrMalformedRelease` (decode error) |

| Input | `CompareVersions` outcome |
|-------|---------------------------|
| `("v0.9.0", "v0.9.1")` | `(Older, nil)` |
| `("0.9.1", "0.9.1")` | `(Same, nil)` |
| `("v1.0.0", "v0.9.9")` | `(Newer, nil)` |
| `("v0.9.0", "v0.10.0")` | `(Older, nil)` (numeric, not lexical) |
| `("v0.10.0-rc1", "v0.10.0")` | `(Same, nil)` (suffix stripped — documented behaviour) |
| `("dev", "v0.9.1")` | `(Same, ErrInvalidVersion)` |
| `("v0.9", "v0.9.1")` | `(Same, ErrInvalidVersion)` (only two components) |
| `("v0.9.1.2", "v0.9.1")` | `(Same, ErrInvalidVersion)` (four components) |
| `("v-1.0.0", …)` | `(Same, ErrInvalidVersion)` (negative rejected) |

The `Same` zero-value on error is conventional — callers must check the error first; the comparison value is meaningless when err != nil. Returning `Same` rather than e.g. `Older` makes a misuse (caller forgets to check err) more likely to silently report "no update needed" than "downgrade available," which is the safer bias.

## Testing strategy

Single test file `version_test.go`, table-driven, stdlib `testing` only.

### `TestParseLatestRelease` cases

| name | input | want tag | want err |
|------|-------|----------|----------|
| `valid_release` | `{"tag_name":"v0.9.1","name":"Release v0.9.1","draft":false}` | `"v0.9.1"` | nil |
| `extra_fields` | `{"tag_name":"v0.9.1","assets":[{"name":"pyry"}],"author":{"login":"x"}}` | `"v0.9.1"` | nil (proves we tolerate unknown fields) |
| `tag_without_v` | `{"tag_name":"0.9.1"}` | `"0.9.1"` | nil (don't add a `v`; preserve verbatim) |
| `prerelease_tag` | `{"tag_name":"v0.10.0-rc1"}` | `"v0.10.0-rc1"` | nil (parser preserves verbatim; comparator handles the strip) |
| `malformed_json` | `not json` | — | `ErrMalformedRelease` |
| `truncated_json` | `{"tag_name":` | — | `ErrMalformedRelease` |
| `top_level_array` | `[1,2,3]` | — | `ErrMalformedRelease` |
| `top_level_string` | `"hello"` | — | `ErrMalformedRelease` |
| `missing_tag_name` | `{"name":"v0.9.1"}` | — | `ErrMalformedRelease` |
| `empty_tag_name` | `{"tag_name":""}` | — | `ErrMalformedRelease` |
| `wrong_type_tag_name` | `{"tag_name":42}` | — | `ErrMalformedRelease` |
| `null_tag_name` | `{"tag_name":null}` | — | `ErrMalformedRelease` (decodes to `""` → empty path) |
| `empty_input` | `` (empty bytes) | — | `ErrMalformedRelease` |

Use `errors.Is(err, ErrMalformedRelease)` for every error-case assertion.

### `TestCompareVersions` cases

| name | current | latest | want | want err |
|------|---------|--------|------|----------|
| `equal_with_v` | `v0.9.1` | `v0.9.1` | `Same` | nil |
| `equal_no_v` | `0.9.1` | `0.9.1` | `Same` | nil |
| `mixed_prefix` | `0.9.1` | `v0.9.1` | `Same` | nil |
| `older_patch` | `v0.9.0` | `v0.9.1` | `Older` | nil |
| `older_minor` | `v0.8.99` | `v0.9.0` | `Older` | nil |
| `older_major` | `v0.99.99` | `v1.0.0` | `Older` | nil |
| `newer_patch` | `v0.9.2` | `v0.9.1` | `Newer` | nil |
| `newer_minor` | `v0.10.0` | `v0.9.99` | `Newer` | nil (numeric not lexical) |
| `newer_major` | `v1.0.0` | `v0.99.99` | `Newer` | nil |
| `prerelease_current_stripped` | `v0.10.0-rc1` | `v0.10.0` | `Same` | nil (documented) |
| `prerelease_latest_stripped` | `v0.10.0` | `v0.10.0-rc1` | `Same` | nil |
| `build_metadata_stripped` | `v0.10.0+build.5` | `v0.10.0` | `Same` | nil |
| `dev_current` | `dev` | `v0.9.1` | — | `ErrInvalidVersion` |
| `dev_latest` | `v0.9.1` | `dev` | — | `ErrInvalidVersion` |
| `too_few_parts` | `v0.9` | `v0.9.1` | — | `ErrInvalidVersion` |
| `too_many_parts` | `v0.9.1.2` | `v0.9.1` | — | `ErrInvalidVersion` |
| `non_numeric` | `v0.9.x` | `v0.9.1` | — | `ErrInvalidVersion` |
| `empty_component` | `v0..1` | `v0.9.1` | — | `ErrInvalidVersion` |
| `negative` | `v-1.0.0` | `v0.0.0` | — | `ErrInvalidVersion` |
| `empty_string` | `` | `v0.9.1` | — | `ErrInvalidVersion` |

Use `errors.Is(err, ErrInvalidVersion)` for error-case assertions. Each subtest runs via `t.Run(tc.name, …)` so failure messages name the offending case.

### Test conventions

- `t.Parallel()` at the top of each subtest (pure functions; no shared state).
- One assertion helper inline (not a helper file); the table is the documentation.
- No external fixtures — the JSON test inputs are short enough to live as Go string literals in the table.
- Run with `go test -race ./internal/update/...` as the verification command.

## Open questions

1. **`main.Version == "dev"` handling at the wiring layer.** This ticket only ensures `CompareVersions("dev", …)` returns `ErrInvalidVersion` cleanly. The wiring ticket must decide what to print: probably "running a dev build, skipping update check" or similar. Out of scope here; logged for the wiring ticket's spec.
2. **Whether the parser should also accept GitHub's draft/prerelease flags.** The `releases/latest` endpoint already excludes drafts and prereleases on GitHub's side, so the parser can ignore the `"draft"` and `"prerelease"` boolean fields. If a future change moves to `/releases` (the list endpoint) and filters client-side, this assumption breaks — but that's a sister-ticket concern.
3. **Pre-release ordering.** Today: `v0.10.0-rc1 == v0.10.0` after suffix strip. If pyry ever publishes a real pre-release operators are expected to install (e.g. announce `v0.10.0-rc1` on a beta channel), the comparator needs SemVer 2.0.0 precedence rules. Defer until observed — we don't ship pre-releases today, and the deferred behaviour is documented in the function's doc comment so reviewers see it.
4. **Should `Comparison` implement `String()`?** Not in AC. Skip. Add in a follow-up ticket if the wiring layer needs `slog` to log it readably (it can format the int directly otherwise).

## Out of scope

- HTTP fetcher (sister ticket).
- Checksum verification of the downloaded binary (separate ticket).
- Atomic binary replacement / restart detection (separate ticket).
- A `pyry update --dry-run` flag (wiring ticket).
- `Comparison.String()` method.
- Any CLI wiring; this ticket adds zero callers.
