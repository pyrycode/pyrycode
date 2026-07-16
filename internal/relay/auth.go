package relay

import "github.com/coder/websocket"

// StatusUnauthorized is the WS close code the relay sends when the binary
// rejects a phone's device token (docs/protocol-mobile.md § Error codes,
// close-code row 4401). Typed locally so callers don't import the
// websocket package for the value; parallel to statusServerIDConflict in
// connection.go.
const StatusUnauthorized websocket.StatusCode = 4401

// MsgInvalidToken is the fixed user-facing message emitted in the
// auth.invalid_token error payload (docs/protocol-mobile.md § Error codes).
// Defined as an exported const so future integration tests can pin it
// without re-typing the spec sentence.
const MsgInvalidToken = "device token not recognised; re-pair via pyry pair on the binary"
