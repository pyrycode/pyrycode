# Spec: server-id type + generation (#206)

## Files to read first

- `internal/sessions/id.go:1-69` — the established pattern for a UUIDv4-shaped
  string newtype with `crypto/rand` generation + canonical-shape validation
  (`SessionID`, `NewID`, `ValidID`). Mirror this pattern almost verbatim;
  divergences below are deliberate.
- `internal/sessions/id_test.go:1-46` — the format + uniqueness test pair.
  Reuse the structure (`uuidPattern` regexp, 1000-iteration uniqueness loop).
- `docs/protocol-mobile.md:61` — wire contract: server-id is "UUIDv4
  (canonical hex form)" minted by the binary on first run, surfaced in QR
  codes and unencrypted on WS upgrade. The relay treats it as the routing
  key; first-claim-wins is the entire authorization model. Reading this row
  fixes the canonicalization rule (lowercase hex, version-4 nibble, RFC 4122
  variant) for the binary side.
- `docs/protocol-mobile.md:575-583` — security framing: server-ids carry ~122
  bits of entropy and unguessability is the security model. `crypto/rand` is
  not optional. **Never** fall back to `math/rand`.
- `CODING-STYLE.md` (root) — naming, error wrapping, package layout
  conventions. The new package is internal; no exports leak outside the
  module.

## Context

Phase 3 introduces the relay-routed mobile protocol. The server-id is the
public routing identifier for one pyrycode-binary instance — it appears in
the QR pairing code, in the binary→relay `x-pyrycode-server` upgrade header,
and in every phone→relay connect targeting this binary. The protocol doc
already pins its shape (UUIDv4, canonical hex form). This ticket lands the
**type + generation + validation** in a fresh package; persistence (writing
the minted id to disk so it survives restarts) is a sibling ticket.

The package is brand-new — `internal/identity` doesn't exist on `main` yet.
No consumers in this ticket; the type is a pre-requisite for the persistence
sibling and for Phase 3 wiring (pairing payloads, relay handshake).

## Design

### Package layout

```
internal/identity/
  server_id.go        — ServerID type, NewServerID, ParseServerID
  server_id_test.go   — table-driven Parse tests + format/uniqueness tests
```

`internal/identity` is the home for typed identifiers that span subsystems
(server-id today; potential future device-id, paired-device-id). Splitting
from `internal/sessions` keeps the sessions package focused on the
supervised-claude lifecycle and avoids a misleading import (`sessions.ServerID`
would suggest the server-id is per-session, which it isn't — one per binary).

### Public API

```go
// Package identity owns typed identifiers for routing and pairing.
package identity

// ServerID is the public routing identifier for one pyrycode-binary instance.
// Canonical form is a UUIDv4 lowercase hex string (8-4-4-4-12, 36 chars).
// Surfaced on the wire in pairing payloads and in the relay handshake's
// x-pyrycode-server header.
//
// The empty ServerID ("") is the unset sentinel and is never a valid
// generated id.
type ServerID string

// NewServerID returns a fresh UUIDv4-shaped ServerID drawn from crypto/rand.
// crypto/rand.Read on Go 1.24+ does not return an error; if the system rng
// is unavailable the runtime aborts, which is the right failure mode for an
// unguessability-dependent identifier.
func NewServerID() ServerID

// ParseServerID validates that s is a canonical UUIDv4 string and returns
// it as a ServerID. Returns ErrInvalidServerID for empty or malformed input.
// Use this at every wire/disk boundary that accepts an externally-supplied
// server-id; do not construct ServerID values via direct conversion outside
// this package.
func ParseServerID(s string) (ServerID, error)

// ErrInvalidServerID indicates a string failed canonical UUIDv4 validation.
// Wrapped errors from ParseServerID match this with errors.Is.
var ErrInvalidServerID = errors.New("identity: invalid server id")
```

### Three deliberate divergences from `sessions.NewID`

1. **`NewServerID` returns `ServerID` (no error).** AC #2 specifies this
   shape. `crypto/rand.Read` has been documented as infallible since Go
   1.24 — the function reads from a system rng that the runtime treats as
   non-recoverable when absent. The implementation calls `rand.Read` and,
   in the impossible-on-supported-platforms branch where it errors, calls
   `panic(fmt.Errorf("identity: crypto/rand failed: %w", err))`. Operators
   will never see this panic in practice; if they do, the alternative
   (silently returning a zero-entropy id) is strictly worse than aborting.
   Document the panic with one short comment.

2. **`ParseServerID` returns `(ServerID, error)`, not a bool.** AC #3
   specifies parser semantics. The validation logic itself is identical to
   `sessions.ValidID` (length, dash positions, version nibble, variant
   nibble, lowercase hex). On success return `ServerID(s)`; on failure
   return `("", ErrInvalidServerID)`. No `fmt.Errorf` wrapping with the
   input string — the caller has the input and can include it in their own
   error context if needed; embedding caller-supplied bytes in the error
   message is a needless log-injection vector for a function that may run
   on relay-supplied input.

3. **Package boundary forbids `ServerID(rawString)` outside this package.**
   `ServerID` is an exported newtype, so Go's type system can't strictly
   prevent direct conversion in another package. Document the rule in the
   type comment (already drafted above) and rely on review. The internal/
   visibility boundary contains the exposure to the pyrycode module itself,
   which is sufficient — there is no public API surface here.

### Implementation sketch (`server_id.go`)

```go
package identity

import (
	"crypto/rand"
	"errors"
	"fmt"
)

type ServerID string

var ErrInvalidServerID = errors.New("identity: invalid server id")

func NewServerID() ServerID {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand.Read is documented as infallible on supported
		// platforms (Go 1.24+). If it ever errors, the system rng is
		// unavailable — we cannot mint an unguessable id, and silently
		// degrading is worse than aborting.
		panic(fmt.Errorf("identity: crypto/rand failed: %w", err))
	}
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80 // variant RFC 4122
	return ServerID(fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]))
}

func ParseServerID(s string) (ServerID, error) {
	if !validUUIDv4(s) {
		return "", ErrInvalidServerID
	}
	return ServerID(s), nil
}

func validUUIDv4(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < 36; i++ {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		case 14:
			if c != '4' {
				return false
			}
		case 19:
			if !(c == '8' || c == '9' || c == 'a' || c == 'b') {
				return false
			}
		default:
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return false
			}
		}
	}
	return true
}
```

The unexported `validUUIDv4` is local to the package. Don't import
`sessions.ValidID` to share — `internal/identity` must not depend on
`internal/sessions` (the dependency direction is wrong: sessions are an
implementation detail that should be free to import identity later, not the
reverse). The duplication is six lines of obvious switch/case; deliberate
cost.

## Data flow

Pure functions, no I/O, no state. No goroutines, no channels, no context.
Callers in future tickets:

- Persistence sibling: `id := identity.NewServerID()` on first run, write to
  disk; on subsequent runs, read raw string and `identity.ParseServerID(raw)`.
- Phase 3 pairing payload: marshal `ServerID` as JSON string (it's a string
  newtype — `encoding/json` handles it natively).
- Phase 3 relay handshake: `req.Header.Set("x-pyrycode-server", string(id))`.

None of those land in this ticket.

## Concurrency model

Stateless. All three exported names are safe for concurrent use by definition
(they own no shared state). `crypto/rand.Read` is goroutine-safe per its
package docs. No documentation note required.

## Error handling

- `NewServerID` cannot fail in the contract. The defensive panic on
  `rand.Read` error is a runtime-abort, not a returned error.
- `ParseServerID` returns `ErrInvalidServerID` for any malformed input. No
  wrapping. Callers that need richer context wrap themselves.

## Testing strategy

`server_id_test.go` covers:

1. **`TestNewServerID_Format`** — generate one id; assert `len == 36` and
   matches the canonical regexp `^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`
   (note the version + variant constraints — tighter than `sessions/id_test.go`'s
   regexp because `ParseServerID` will enforce both).

2. **`TestNewServerID_Unique`** — 1000 iterations; assert no duplicates.
   Mirrors `sessions/id_test.go:TestNewID_Unique`. Catches a constant-zero
   rng wiring bug.

3. **`TestParseServerID`** — table-driven. Cases:
   - **valid**: `"550e8400-e29b-41d4-a716-446655440000"` → `(ServerID(...), nil)`
   - **valid (different variant nibble)**: `"550e8400-e29b-41d4-9716-446655440000"` (variant `9`)
   - **valid (variant `b`)**: `"550e8400-e29b-41d4-b716-446655440000"`
   - **empty**: `""` → `ErrInvalidServerID`
   - **wrong length**: `"550e8400"` → `ErrInvalidServerID`
   - **wrong length (one char short)**: 35-char string → `ErrInvalidServerID`
   - **wrong length (one char long)**: 37-char string → `ErrInvalidServerID`
   - **uppercase**: `"550E8400-E29B-41D4-A716-446655440000"` → `ErrInvalidServerID` (lowercase only)
   - **wrong version (v1)**: `"550e8400-e29b-11d4-a716-446655440000"` (`1` at pos 14) → `ErrInvalidServerID`
   - **wrong variant**: `"550e8400-e29b-41d4-7716-446655440000"` (variant `7`) → `ErrInvalidServerID`
   - **non-hex char**: `"550e8400-e29b-41d4-a716-44665544000g"` (`g`) → `ErrInvalidServerID`
   - **missing dash**: `"550e8400e29b-41d4-a716-446655440000"` → `ErrInvalidServerID`
   - **dash at wrong position**: `"550e840-0e29b-41d4-a716-446655440000"` → `ErrInvalidServerID`

   Use `errors.Is(err, ErrInvalidServerID)` for the negative assertions;
   verifies the sentinel is reachable, not just "some error returned."

4. **`TestNewServerID_RoundTripsParseServerID`** — generate an id with
   `NewServerID`, feed it through `ParseServerID`, assert `nil` error and
   the round-tripped value equals the original. The round-trip property is
   AC #4's first item; this test is its direct expression.

All tests `t.Parallel()`. No fixtures, no helpers — table-driven and short.

## Open questions

None worth deferring. The shape is fully constrained by the AC and the
existing `sessions/id.go` precedent. The three documented divergences are
all forced by the AC's signatures.

## Out of scope

- Persistence — sibling ticket loads the raw string from disk and feeds
  it to `ParseServerID`.
- JSON round-trip tests — `encoding/json` on a string newtype is library
  behavior; no value in re-testing it here. The persistence sibling will
  cover JSON disk format.
- Human-label suffix — `protocol-mobile.md:61` notes a "may have a human
  label suffix in the QR for UX" possibility. That's a QR-encoding concern,
  not an id-type concern; defer to whichever Phase 3 ticket builds the QR.
- CLI surface (`pyry server-id` to print the value) — defer.
