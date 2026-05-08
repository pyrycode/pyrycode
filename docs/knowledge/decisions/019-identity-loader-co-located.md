# 019 — `LoadOrCreate` lives in `internal/identity`, not a sibling `internal/identitystore`

## Context

#206 landed the pure type + generator + parser for `ServerID` in a new
`internal/identity` package whose package doc-comment declared "Pure types
and validation; no I/O." #207 added the binary-startup bootstrap on top:
*on first run, mint and persist; on subsequent runs, load and validate.*

The ticket-issue's technical-notes paragraph explicitly flagged the package
boundary as a judgment call: "the architect should either relax that
doc-comment or place the loader in a sibling package (e.g.
`internal/identitystore`). Either is acceptable; pick one and state the
rationale in the spec."

## Decision

Place `LoadOrCreate` in `internal/identity/store.go`. **Relax** the package
doc-comment so it no longer claims "no I/O":

```go
// Package identity owns typed identifiers that span subsystems — server-id
// today; potential future device-id, paired-device-id — and the bootstrap
// of those identifiers from disk. The pure types and validation live next
// to the I/O wrapper that mints and persists them on first run.
package identity
```

## Rationale

1. **The loader has no API surface beyond the type.** Its only job is to
   produce a `ServerID`. Splitting into two packages forces every caller
   to import both (`identity.ServerID` + `identitystore.LoadOrCreate`)
   for no abstraction win — the loader is not interchangeable, the type
   is not separately useful at the daemon-startup site.

2. **Future identifier-loaders compose linearly.** Phase 3 will likely
   add an analogous loader for `~/.pyry/devices.json` (paired-device
   list). Keeping each type's persistence next to its type
   (`devices.go` + `devices_store.go`) scales by adding files within
   `internal/identity` (and `internal/devices`); a parallel
   `internal/identitystore` / `internal/devicestore` would replicate the
   package boundary for the same reason.

3. **"No I/O" was a #206-scope claim, not a permanent invariant.** The
   package's real contract is "owns identity types"; persisting an
   identity to disk is a natural extension of that contract.

## Consequences

- `internal/identity` now owns I/O. Callers bound for `LoadOrCreate` at
  daemon startup import this one package.
- The package doc-comment is the contract; future I/O additions in this
  package (e.g. a mirror for `devices.json`) require no further
  package-boundary debate within the same identifier domain.
- The `crypto/rand` panic discipline of `NewServerID` is now adjacent to
  a function that performs disk I/O. The two failure-mode regimes
  (panic-on-rng vs. error-return-on-fs) are distinct and documented at
  each function's signature; co-location does not blur them.
- The atomic-write recipe in `store.go` is structurally identical to
  `internal/sessions/registry.go:53-92` (`MkdirAll(0o700)` →
  `CreateTemp` → `Chmod(0o600)` → write → `Sync` → `Close` → `Rename`).
  A future regression in the registry's recipe should be mirrored here
  (and vice versa).

## Alternatives considered

- **Sibling `internal/identitystore`.** Rejected — see rationale above.
  Forces dual import for one function with no abstraction win, scales
  by package proliferation rather than file addition.
- **Loader in `cmd/pyry/main.go` directly.** Rejected — the atomic-write
  recipe is non-trivial and identical to the sessions-registry recipe;
  packaging it ensures uniform tests under `go test -race ./...`.

## Related

- Ticket #207 — server-id persistence + first-run bootstrap.
- ADR 018 — overlay-decode pattern in `internal/config` (sibling Phase-3
  foundation slice).
- `internal/sessions/registry.go:53-92` — canonical atomic-write recipe.
- `docs/lessons.md` § "Atomic on-disk writes" — load-bearing invariants.
