# `SessionID`

```go
type SessionID string

func NewID() (SessionID, error)
func ValidID(s string) bool
```

A 36-char canonical UUIDv4 (`8-4-4-4-12` hex with dashes), drawn from `crypto/rand`. No external UUID library — stdlib only, ~15 lines. The version (`b[6] = b[6]&0x0f | 0x40`) and variant (`b[8] = b[8]&0x3f | 0x80`) bits are set explicitly.

The empty `SessionID` (`""`) is the **unset sentinel**, never a valid generated ID. `Pool.Lookup("")` resolves to the default entry — the mechanism that lets future handlers call `Lookup(req.SessionID)` against an empty wire field without a special case.

`ValidID` (1.3b) reports whether `s` matches the canonical shape `NewID` produces: 36 chars, lowercase hex, dashes at positions 8/13/18/23, version-4 nibble (`'4'`) at position 14, RFC 4122 variant (`'8'`/`'9'`/`'a'`/`'b'`) at position 19. Empty input returns false. Lives next to `NewID` so producer + validator share one file. Used by `Pool.GetOrCreate` to reject caller-supplied ids that aren't canonical UUIDv4s. The version + variant checks are belt-and-suspenders: SDK-produced UUIDs are uuidv4 by construction, so the cost is nil and a future contributor passing a v3/v5 id gets a clean error. See [ADR 014](../decisions/014-get-or-create-take-or-create.md).
