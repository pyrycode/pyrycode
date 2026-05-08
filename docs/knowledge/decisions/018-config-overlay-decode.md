# ADR 018 — `internal/config.Load` uses overlay-decode over defaults

## Context

Phase 3 (mobile + relay) needs a place for user-configurable values. The first one is the relay URL — `pyry pair` (separate ticket) lets a user point their daemon at a self-hosted relay without rebuilding. Future fields land additively in the same struct.

The acceptance criterion at the heart of this decision: "existing file is decoded over `DefaultConfig()` so absent fields keep default values." A user who pins `relay_url` today must not lose tomorrow's new defaults when a Phase 3 field is introduced; conversely a user with `{}` on disk should get all defaults.

## Decision

`Load(path)` initializes `cfg := DefaultConfig()` *before* `json.Unmarshal(data, &cfg)`. `encoding/json` only writes fields present in the input document — absent fields keep their pre-decode value. One call, no merge logic.

```go
cfg := DefaultConfig()
data, err := os.ReadFile(path)
// ... handle ErrNotExist → return cfg, nil; other errors → wrap
if err := json.Unmarshal(data, &cfg); err != nil {
    return Config{}, fmt.Errorf("config: parse %s: %w", path, err)
}
return cfg, nil
```

## Alternatives considered

**Two-pass merge** — decode into `map[string]json.RawMessage`, then field-by-field merge over defaults. Rejected: adds a merge loop, adds an intermediate representation, and gains nothing — `encoding/json`'s in-place unmarshal already implements exactly this semantic. Overlay is one line in the body and zero lines in the design.

**Pointer fields** (`RelayURL *string`) so absent = `nil` and present-but-empty = `*""` are distinguishable. Rejected: the AC doesn't ask for "explicit empty string overrides default to empty," and arguably it shouldn't — an empty relay URL is a configuration error, not a deliberate setting. If a future field needs that distinction, that field is the place to introduce a pointer; the package shouldn't pay the syntactic tax up front for everyone.

**Silent fallback on malformed JSON.** Rejected by the ticket: malformed JSON returns a wrapped error so the operator must fix or remove the file. Returning `DefaultConfig()` on any error would mask real problems — if the operator has a typo in their `relay_url`, they want to hear about it, not silently keep talking to the placeholder relay.

## Consequences

- Adding a Phase 3 field is an additive change: append to the struct, append a default in `DefaultConfig()`. Every existing config file picks up the new default automatically; users who pinned the new field override it. No migration code, no schema version bump.
- Empty file (0 bytes) returns a wrapped JSON-parse error (falls out of the body naturally — `json.Unmarshal([]byte{}, ...)` returns `unexpected end of JSON input`). This is intentional: a fresh install has no file at all, so an empty file is operator error.
- Schema versioning is not pre-built. If `Config` ever needs an incompatible change, version it then — this is the same posture `internal/sessions/registry.go` takes (`registryFile.Version` is reserved but unused).
- The error path returns `Config{}`, not `DefaultConfig()`. Callers that ignore `err` see an empty `RelayURL` and break loudly, rather than silently using a default that masks the real problem.
