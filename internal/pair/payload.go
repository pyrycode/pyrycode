// Package pair encodes the {server, relay, token} tuple a paired mobile
// device needs to connect through the relay back to this binary, and
// decodes strings produced by the same encoder. Pure functions; no I/O.
//
// The encoded form is a single ASCII string suitable for embedding in a
// QR symbol or for a one-time paste-fallback display. The wire shape is
// the JSON serialization of Payload, then base64url-encoded with the
// URL-safe alphabet and no padding.
//
// The Token field carries the plaintext bearer the mobile client will
// present back on subsequent connections. Hashing for storage happens
// elsewhere (internal/devices). Callers MUST NOT log Payload, Encode
// output, or Decode errors in any context that may persist user-visible
// output: the device-token is one-time-only and its visibility ends
// when the QR symbol is dismissed.
package pair

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/pyrycode/pyrycode/internal/identity"
)

// Payload is the {server, relay, token} tuple emitted by `pyry pair` and
// consumed by the paired mobile device.
//
// Field order is fixed by the protocol-mobile.md appendix; do not reorder.
// All three fields are required; encoders SHOULD reject zero values
// upstream, decoders MUST (see Decode).
//
// Relay is not parsed/validated by this package; semantic validation
// belongs to whichever caller dials the URL. Token is not length- or
// alphabet-checked here; minting contract belongs to the token issuer.
type Payload struct {
	Server identity.ServerID `json:"server"`
	Relay  string            `json:"relay"`
	Token  string            `json:"token"`
}

// ErrInvalidPayload is the sentinel returned (via wrap) for any input
// Decode rejects. Match with errors.Is; do not match on error strings.
// Decode error messages describe the failure category only and never
// include input bytes or decoded field values.
var ErrInvalidPayload = errors.New("pair: invalid payload")

// Encode returns the wire-format string for p: JSON marshal, then
// base64url-encode (URL-safe alphabet, no padding). Encode does NOT
// validate p — empty fields produce an encoded string Decode will
// reject. Callers are expected to construct Payload from validated
// inputs (ServerID minted via identity.NewServerID, token via
// crypto/rand, relay URL from internal/config).
func Encode(p Payload) string {
	b, err := json.Marshal(p)
	if err != nil {
		// json.Marshal of a struct whose fields are stdlib string types
		// with no MarshalJSON method cannot fail; surface the impossible
		// case loudly rather than returning a silently-corrupt string.
		panic(fmt.Errorf("pair: json.Marshal of Payload failed: %w", err))
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Decode parses a string produced by Encode back into a Payload.
//
// Errors wrap ErrInvalidPayload (use errors.Is). The error text is a
// failure-category string only — never input bytes or decoded fields.
//
// Decode returns ErrInvalidPayload-wrapped errors for:
//   - input that is not valid base64url (URL-safe alphabet, no padding)
//   - decoded bytes that are not a JSON object
//   - JSON containing trailing bytes after the top-level object
//   - server/relay/token field missing or the empty string
//   - server field not parseable by identity.ParseServerID
//
// On any error the returned Payload is the zero value.
func Decode(s string) (Payload, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return Payload{}, fmt.Errorf("%w: invalid base64url encoding", ErrInvalidPayload)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	var p Payload
	if err := dec.Decode(&p); err != nil {
		return Payload{}, fmt.Errorf("%w: invalid JSON", ErrInvalidPayload)
	}
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Payload{}, fmt.Errorf("%w: trailing data after JSON object", ErrInvalidPayload)
	}
	if p.Server == "" {
		return Payload{}, fmt.Errorf("%w: missing field: server", ErrInvalidPayload)
	}
	if p.Relay == "" {
		return Payload{}, fmt.Errorf("%w: missing field: relay", ErrInvalidPayload)
	}
	if p.Token == "" {
		return Payload{}, fmt.Errorf("%w: missing field: token", ErrInvalidPayload)
	}
	if _, err := identity.ParseServerID(string(p.Server)); err != nil {
		return Payload{}, fmt.Errorf("%w: invalid server id", ErrInvalidPayload)
	}
	return p, nil
}
