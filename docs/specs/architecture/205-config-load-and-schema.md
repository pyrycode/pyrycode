# #205 — `internal/config` package: typed schema + overlay loader

**Size:** S (architect-confirmed). Single new package `internal/config` with
two new files (`config.go`, `config_test.go`). Production code is ~30 lines
(one struct, one constructor, one loader). No consumers — daemon startup
and `pyry pair` wire `Load` from their own tickets. Within all S red lines
(≤3 new files, ≤150 prod lines, 3 new exported names: `Config`,
`DefaultConfig`, `Load`).

**Status:** ready for development.

**Depends on:** nothing. The package is a leaf — no imports beyond
stdlib.

## Files to read first

The developer's turn-1 data load. Each entry is paged in deliberately —
don't grep for them.

- `internal/sessions/registry.go:31-51` — `loadRegistry`. The
  reference implementation for "read file → on `fs.ErrNotExist` return
  zero-value-as-default, otherwise unmarshal, otherwise wrap and
  surface". Copy the wrap shape (`fmt.Errorf("registry: read %s: %w",
  path, err)`); just rename the prefix to `config:`.
- `internal/sessions/registry.go:17-29` — the `registryFile` /
  `registryEntry` struct shape. Mirror the JSON-tag style: snake_case
  (`json:"relay_url"`), no `omitempty` on required-with-default fields
  (we want the field to round-trip even when set to its zero value, so
  a future operator-written file isn't surprising).
- `CODING-STYLE.md` § "Error Handling" — `fmt.Errorf("X: %w", err)`
  wrap shape. § "Testing" — table-driven, stdlib `testing` only,
  `t.Parallel()`, `t.Helper()` for shared assertions, no testify.
  § "Naming" — `RelayURL` (acronym all-caps in compounds).
- The ticket body itself (#205) — five AC items, each maps directly to
  one test case.

That's the read budget. The whole package is ~30 lines.

## Context

Phase 3 (mobile + relay) needs a place to put user-configurable values.
The first one is the relay URL — `pyry pair` (separate ticket) lets a
user point their daemon at a self-hosted relay without rebuilding the
binary. Future Phase 3 fields land additively in the same struct.

This ticket introduces only the package and schema. There is intentionally
no consumer change in this ticket — the daemon doesn't read the config
yet, `pyry pair` doesn't read it yet. That's separate tickets, by design,
to keep this slice small and the seam reviewable on its own.

## Design

### Package placement

Flat package: `internal/config`. Per CODING-STYLE: "one package per
concern", "avoid `pkg/`, `util/`, `common/`". Config is its own
concern — owned by neither `supervisor` (process lifecycle) nor
`control` (Unix socket protocol) nor `sessions` (registry).

Two new files:

```
internal/config/
  config.go        Config struct, DefaultConfig, Load
  config_test.go   Same-package tests, table-driven
```

No subpackages. No `internal/config/loader.go` split — the production
file is small enough that splitting is premature.

### Exported surface

Three new exports in `internal/config`:

```go
// Config is the on-disk schema for ~/.pyry/config.json. Fields are
// added additively over time; consumers reading an older file see
// missing-field defaults via DefaultConfig + overlay-decode in Load.
type Config struct {
    RelayURL string `json:"relay_url"`
}

// DefaultConfig returns the built-in defaults. Used directly when no
// config file exists, and as the overlay base when a partial file is
// present so absent fields keep their default values.
func DefaultConfig() Config

// Load reads the config file at path and returns a Config with defaults
// filled in for absent fields. A missing file returns DefaultConfig()
// with no error. A malformed file returns a wrapped error (no silent
// fallback — operator must fix or remove the file). On any error the
// returned Config is the zero value; callers must check err.
func Load(path string) (Config, error)
```

No exported errors, no sentinel `ErrConfigMissing`, no `Save` —
read-only this ticket. If a future ticket needs `Save`, it lands then.
(Compare `internal/sessions/registry.go`, where `loadRegistry` shipped
without `saveRegistry` for the slice that needed only reads.)

### Default value

```go
func DefaultConfig() Config {
    return Config{
        RelayURL: "wss://relay.pyrycode.dev",
    }
}
```

The placeholder domain is fine — the AC explicitly says "real domain
TBD". When the real relay is provisioned, that ticket changes the
constant; existing users with no `~/.pyry/config.json` pick up the new
default automatically on the next daemon start. Users who pinned a value
in their config file are unaffected (overlay-decode preserves their
explicit setting).

The default is built into the function body, not a package-level
`const` or `var`. Two reasons: (1) the surface stays minimal — one
exported function, no exported constant for callers to confuse with
"the current value"; (2) when more fields land, the constructor grows
naturally to a struct literal with several lines, which is the
canonical Go idiom for default-with-options. No need to over-structure
the single-field case.

### Loader body

```go
func Load(path string) (Config, error) {
    cfg := DefaultConfig()
    data, err := os.ReadFile(path)
    if err != nil {
        if errors.Is(err, fs.ErrNotExist) {
            return cfg, nil
        }
        return Config{}, fmt.Errorf("config: read %s: %w", path, err)
    }
    if err := json.Unmarshal(data, &cfg); err != nil {
        return Config{}, fmt.Errorf("config: parse %s: %w", path, err)
    }
    return cfg, nil
}
```

Three behaviors, each mapped to one AC bullet:

1. **Missing file → defaults, no error.** `errors.Is(err, fs.ErrNotExist)`
   is the canonical predicate; works for both `os.PathError` wrapping
   `syscall.ENOENT` and the abstract `fs.ErrNotExist`. Same predicate
   `loadRegistry` uses.

2. **Existing file → overlay-decoded over defaults.** This is the
   load-bearing trick: `cfg` is initialized to `DefaultConfig()`
   *before* `json.Unmarshal`. `encoding/json` only writes fields present
   in the JSON document — absent fields keep their pre-decode value. So
   a file containing `{}` returns `DefaultConfig()`, a file containing
   `{"relay_url": "wss://my-relay.example/"}` returns defaults with
   only `RelayURL` overridden. This is the property AC#2 ("absent
   fields keep default values") tests.

3. **Malformed JSON → wrapped error.** No silent fallback to defaults —
   the AC is explicit on this. Wrap shape matches `loadRegistry`:
   `fmt.Errorf("config: parse %s: %w", path, err)`. Operators see the
   path so they know which file to fix.

4. **Other read errors → wrapped error.** Permission denied, IO error,
   etc. Not silently fallback'd — same reasoning as malformed JSON.

5. **Empty file (0 bytes) → wrapped JSON-parse error.** This falls out
   of the body above naturally: `os.ReadFile` succeeds with `data` of
   length 0, `json.Unmarshal([]byte{}, ...)` returns
   `unexpected end of JSON input`, which we wrap. This is intentional
   — config is user-owned; an empty file is an operator error, not a
   "fresh install" signal (a fresh install has no file at all).
   `loadRegistry` treats empty-as-missing because the registry is
   pyry-owned; the asymmetry is correct.

   This isn't a separate AC bullet, just a corollary of "malformed
   JSON returns a wrapped error". Don't add a special case for it —
   one less branch, one less test.

### Imports

```go
package config

import (
    "encoding/json"
    "errors"
    "fmt"
    "io/fs"
    "os"
)
```

Stdlib only. No new module dependencies — `go.mod` unchanged.

### What this ticket does NOT do

- **No path resolution.** `Load` takes an absolute (or relative) `path`
  string. The "where does `~/.pyry/config.json` live" question is the
  caller's. The daemon-startup consumer ticket will add a
  `resolveConfigPath()` helper alongside `resolveSocketPath` /
  `resolveRegistryPath` in `cmd/pyry/main.go`. Doing it here would
  bind the package to `os.UserHomeDir` semantics that some future
  caller (a CLI subcommand with `--config` override) wants to compose
  differently.
- **No `Save`.** Read-only. Future ticket adds atomic-rename write
  primitive if/when needed.
- **No watcher / reload.** Daemon reads once at startup. If a future
  ticket needs hot reload, that's a separate seam (file watcher, signal
  handler, etc.) not bolted onto `Load`.
- **No schema versioning.** Per ticket: "Schema versioning is out of
  scope — if `Config` ever grows incompatibly, version it at that
  point." `encoding/json`'s default lenient handling (unknown JSON
  fields ignored, missing struct fields → zero values) covers
  backward-additive changes for free. This is exactly what the
  registry does (`registryFile` has no `Version` migration logic
  either; the field is reserved but unused).
- **No URL validation.** `RelayURL` is a `string`. Validation
  (scheme = `wss` or `ws`, parseable as `net/url.URL`, etc.) is the
  consumer's job — `pyry pair` will validate before connecting. The
  config package's contract is "decode JSON into a struct"; semantic
  validation is layered above.

### Why overlay-decode beats two-pass merge

Alternative considered: decode into a `map[string]json.RawMessage`,
then field-by-field merge over defaults. Rejected. Adds code (a merge
loop), adds an intermediate representation, and gains nothing —
`encoding/json`'s in-place unmarshal already implements exactly this
semantic. The overlay pattern is one-line in the body and zero-line in
the design.

Alternative considered: pointer fields (`RelayURL *string`) so absent
= `nil` and present-but-empty = `*""`, distinguished. Rejected. The
AC doesn't ask for "explicit empty string overrides default to empty"
— and arguably it shouldn't, since an empty relay URL is a
configuration error, not a deliberate setting. If a future field needs
that distinction, that field is the place to introduce a pointer; the
package doesn't pay the syntactic tax up front for everyone.

## Concurrency model

None. `Load` is a single synchronous call: one `os.ReadFile`, one
`json.Unmarshal`, return. No goroutines, no shared state, no mutexes.

The daemon will call `Load` once at startup, before any goroutines
spawn. Race-detector clean by construction.

If a future ticket adds hot-reload, that ticket designs the synchronization
model — most likely a `sync/atomic.Pointer[Config]` swapped on
file-watcher events. Not this ticket's problem.

## Error handling

Three error paths, all wrapped via `fmt.Errorf("config: %s %s: %w", ...)`
so callers can `errors.Is(err, fs.ErrPermission)` /
`errors.Is(err, fs.ErrInvalid)` etc. without losing context. The
`config:` prefix matches the convention `loadRegistry` established
(`registry:`).

| Condition                      | Returns                                              |
|--------------------------------|------------------------------------------------------|
| File doesn't exist             | `DefaultConfig(), nil`                               |
| File exists, valid JSON        | `mergedConfig, nil`                                  |
| File exists, valid JSON, `{}`  | `DefaultConfig(), nil` (overlay no-op)               |
| File exists, malformed JSON    | `Config{}, fmt.Errorf("config: parse %s: %w", ...)`  |
| File exists, empty (0 bytes)   | `Config{}, fmt.Errorf("config: parse %s: %w", ...)`  |
| File exists, read fails (perms)| `Config{}, fmt.Errorf("config: read %s: %w", ...)`   |

The zero `Config{}` on the error paths is deliberate. Callers who
ignore the error and use the value will see an empty `RelayURL`,
which is *less* friendly than seeing the placeholder default — they're
forced to handle the error. (Returning `DefaultConfig()` on error
would mask real problems.)

## Testing strategy

### Layout

`internal/config/config_test.go`, `package config` (same-package, per
CODING-STYLE: no `_test` suffix unless you specifically need to test
external behavior). Tests use `t.TempDir()` for fixture files — no
checked-in golden files, no `testdata/` directory.

`t.Parallel()` on every test case (no shared state across cases —
each writes to its own `t.TempDir()`).

### Test cases

Five cases mapped 1:1 onto the AC's enumeration. One table-driven
top-level `TestLoad` covering the path-based cases (1-4), one
straight-line `TestDefaultConfig` covering case 5.

**`TestDefaultConfig`** — direct check. One assertion:

```go
got := DefaultConfig()
want := Config{RelayURL: "wss://relay.pyrycode.dev"}
if got != want {
    t.Errorf("DefaultConfig() = %+v, want %+v", got, want)
}
```

Pins the literal default. When the real relay domain lands, this test
fails loudly — that's the right signal.

**`TestLoad`** — table-driven. Each case sets up its own file (or
non-file) under `t.TempDir()`, calls `Load`, asserts on the returned
`Config` and `error`. Sketch of the table shape:

```go
type wantErr int
const (
    wantNoErr wantErr = iota
    wantParseErr
)

cases := []struct {
    name       string
    fileBody   *string  // nil = file does not exist; else contents to write
    want       Config
    wantErr    wantErr
    errSubstr  string   // substring required in err.Error() when wantErr != wantNoErr
}{
    {
        name:    "missing file returns defaults",
        fileBody: nil,
        want:    Config{RelayURL: "wss://relay.pyrycode.dev"},
        wantErr: wantNoErr,
    },
    {
        name:    "valid full file overrides default",
        fileBody: ptr(`{"relay_url": "wss://my-relay.example/"}`),
        want:    Config{RelayURL: "wss://my-relay.example/"},
        wantErr: wantNoErr,
    },
    {
        name:    "partial file with missing fields keeps defaults",
        fileBody: ptr(`{}`),
        want:    Config{RelayURL: "wss://relay.pyrycode.dev"},
        wantErr: wantNoErr,
    },
    {
        name:      "malformed JSON returns wrapped error",
        fileBody:  ptr(`{not json`),
        want:      Config{}, // zero on error
        wantErr:   wantParseErr,
        errSubstr: "config: parse",
    },
}
```

(`ptr` is a tiny `func ptr[T any](v T) *T { return &v }` helper at
file scope, or a literal `func() *string { s := "..."; return &s }()`
inline — use whichever the developer finds cleaner. Don't import a
generics utility lib.)

In the loop:
- `path := filepath.Join(t.TempDir(), "config.json")`
- If `tc.fileBody != nil`, `os.WriteFile(path, []byte(*tc.fileBody), 0o600)`.
- `got, err := Load(path)`
- Assert `got` equals `tc.want` (Config is a small comparable struct
  — direct `==` works, no `reflect.DeepEqual` needed).
- Assert error shape per `tc.wantErr`. For `wantParseErr` also check
  `strings.Contains(err.Error(), tc.errSubstr)` so the wrap prefix
  is pinned.

### "Partial file" — what's it really testing?

Today, `Config` has one field. A "partial file" with that field absent
is `{}`, and the case looks degenerate. Keep it anyway — it's the
regression guard for the overlay-decode property. When Phase 3 adds
a second field (say `PairingTimeout time.Duration`), this case is
already there to catch a regression where the loader stops merging
over defaults. The test is cheap; the alternative (adding it later
when the bug bites) is expensive.

### What NOT to test

- Don't test `os.ReadFile`'s permission handling. Stdlib.
- Don't test `json.Unmarshal`'s every error variant. Stdlib.
- Don't test path resolution. Out of scope for this package.
- Don't add a `TestLoad_PermissionDenied` case. Hard to make portable
  (root reads everything; CI runs as varied users), low marginal
  value — the read-error wrap is one line and covered by inspection.
- Don't test that `Load` is goroutine-safe or that two concurrent
  calls return the same result. It's a pure read; the question doesn't
  arise.

### Race detector

`go test -race ./internal/config/...` must pass. Trivially does — no
goroutines.

## Open questions

None. The package is a stdlib-shaped JSON loader; the design space is
fully constrained by AC. Developer should write the file + tests and
ship.

## Acceptance check (for the developer)

Walk down the AC list before pushing:

- [ ] AC#1 `internal/config/config.go` exists, exports `Config` (with
  `RelayURL string`), `DefaultConfig() Config`, `Load(path string) (Config, error)`.
- [ ] AC#2 missing file → `DefaultConfig()`, no error. Existing file
  decoded over `DefaultConfig()`. Malformed JSON → wrapped error
  (no silent fallback). Verify by inspection of `Load`'s body and
  by the four `TestLoad` cases.
- [ ] AC#3 `DefaultConfig().RelayURL == "wss://relay.pyrycode.dev"`.
  Pinned by `TestDefaultConfig`.
- [ ] AC#4 Table-driven tests cover: missing file, valid full file,
  partial file (`{}`), malformed JSON, `DefaultConfig` values.
  Five cases total across `TestLoad` (4) + `TestDefaultConfig` (1).

Build / lint:
- `go build ./...` — package compiles.
- `go vet ./...` — clean.
- `staticcheck ./...` — clean. Watch for `staticcheck`'s preference
  about pointer-vs-direct comparison on small structs; the
  `Config` direct-comparison (`got != want`) is fine because
  `Config` contains only a `string`.
- `go test -race ./internal/config/...` — all five cases pass.
- `go test -race ./...` — wider suite unchanged. (No consumers of
  this package yet, so the only signal here is "I didn't accidentally
  break something else by, e.g., introducing a circular import." Very
  low-risk for a leaf package.)
