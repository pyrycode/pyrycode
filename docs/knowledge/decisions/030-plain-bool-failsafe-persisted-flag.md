# ADR 030: A persisted security-relevant bool needs no custom decoder — the zero value and the existing strict-parse error path are the entire fail-safe argument

## Status

Accepted (ticket #833).

## Context

#833 adds `YOLO bool` to `internal/sessions`' `registryEntry` — the on-disk
switch that, when true, launches `claude` with `--dangerously-skip-permissions`.
Ticket AC #2 requires: a registry entry written before this field existed (or
with it absent) must load with YOLO **off**, and "a corrupt or missing YOLO
value must never silently enable bypass."

The naive instinct for a security-relevant flag is to reach for a stronger
type — `*bool` to distinguish "unset" from "explicitly false," or a custom
`UnmarshalJSON` that rejects anything but `true`/`false`/absent. Both add code
whose only job is to protect against a failure mode worth checking whether the
existing machinery already rules out.

## Decision

`YOLO` is a plain `bool` (`json:"yolo,omitempty"`), decoded by the registry's
existing lenient `json.Unmarshal` (`registry.go`'s `loadRegistry`) with **no
new decoder, no `*bool`, no new validation branch**.

This works because two properties already held before this ticket:

1. **A missing JSON key decodes a Go `bool` field to its zero value, `false`.**
   Stdlib behaviour, not something this ticket adds.
2. **`loadRegistry` already fails the whole parse on a malformed field**, of
   any type, anywhere in the entry (`registry.go:47-49`) — a pre-existing
   property proven by `TestLoad_MalformedJSON`, extended by this ticket's
   `TestRegistry_CorruptYOLOFailsLoud`. A wrong-typed `"yolo": "maybe"` doesn't
   partially decode; `json.Unmarshal` returns a type error for the whole
   struct, `loadRegistry` wraps and returns it, and `Pool.New` fails at
   startup. No session spawns.

Given those two properties, every reachable state maps to `YOLO == false`
except a fully-valid, fully-parsed `"yolo": true`:

| On-disk state | Outcome |
|---|---|
| Key absent (old registry, or a session that never opted in) | `false` |
| `"yolo": false` | `false` |
| `"yolo": true`, parses cleanly | `true` — the operator's own prior opt-in |
| `"yolo": <wrong type>` | whole load fails, daemon doesn't start, no spawn |
| Torn/truncated file (crash mid-write) | whole load fails (same path) — moot anyway: `saveRegistryLocked`'s `os.CreateTemp` → fsync → `os.Rename` already makes a torn on-disk file unreachable |

There is no code path that decodes a corrupt or absent value to `true`.

The output side has the matching property: `claudeSettingsArgs` only ever
**appends** `--dangerously-skip-permissions` when `YOLO == true`; it never
emits a "disable bypass" flag. Absence of the flag *is* what enforces
permissions, so there is nothing to forget to emit on the safe path either.

## Rationale

**Belt-and-suspenders means different fabric — here neither belt nor
suspenders is stochastic.** The safe default is a language guarantee (Go zero
values), and the corruption guard is a stdlib guarantee (`encoding/json`
whole-value strictness). Both are deterministic code with no discretionary
judgment call anywhere in the path — the thing this project's fabric rule
asks for.

**A `*bool` would add a state with no correct handling.** `nil` would still
need to mean "false" (the fail-safe default), making the extra state pure
overhead — a second representation of the same zero-value case, with the
attendant risk that some future comparison checks `ptr != nil` instead of
`*ptr` and gets it backwards.

**A custom `UnmarshalJSON` would duplicate a check `loadRegistry` already
performs at the wrong granularity.** `loadRegistry`'s existing behavior is
already whole-file strict — adding a per-field validator would either be
redundant (same rejection, extra code) or, worse, tempt a future author into
a partial-decode-and-default-the-bad-field shape that's harder to reason
about than "the whole load either succeeds with a fully-typed struct or
fails outright."

## Consequences

**Going forward:**

- Any future persisted bool whose zero value is the fail-safe state can
  follow the same reasoning: check whether `loadRegistry` (or the equivalent
  loader) already fails the whole parse on a type mismatch before reaching
  for `*bool` or a custom decoder. If the loader is already whole-value
  strict, a plain typed field is sufficient and is the simpler fix per this
  repo's Simplicity First principle.
- The pattern does **not** generalize to a field whose fail-safe default is
  `true` (i.e., "safe" is the non-zero value) — a plain `bool` there would
  make a missing key silently *unsafe*. That shape would need an explicit
  three-state type or a documented default-injection step. No such field
  exists yet in this registry.
- #826b (the wire setter that will let a client flip `YOLO` at runtime) still
  owns validating *untrusted* input at the point it actually crosses a trust
  boundary — this decision only covers the operator-local, 0600-file registry
  read path. Don't assume this ADR validated anything on the wire.

## Related

- [codebase/833.md](../codebase/833.md) — the ticket this decision shipped in.
- [ADR 016](016-bootstrap-ignores-persisted-lifecycle-state.md) — another
  registry-load carve-out in the same `Pool.New` warm-start branch; orthogonal
  to this one (lifecycle state is deliberately overridden, settings are
  deliberately honoured — see codebase/833.md for why these don't conflict).
- [ADR 028](028-ed25519-checksums-signature.md) — another security-sensitive
  ticket whose fail-closed argument rests on an existing primitive (signature
  verification) rather than new bespoke code; same "reuse what's already
  strict" shape.
- `internal/sessions/registry.go` — `registryEntry`, `loadRegistry`,
  `saveRegistryLocked` (the atomic-write guarantee that makes "torn file"
  moot).
