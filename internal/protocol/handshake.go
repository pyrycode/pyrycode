package protocol

import "time"

// HelloServerPayload is the body of a "hello" envelope sent by the binary
// after WS upgrade (docs/protocol-mobile.md § Message types). Role is
// always "server".
type HelloServerPayload struct {
	Role             string   `json:"role"`
	ServerID         string   `json:"server_id"`
	BinaryVersion    string   `json:"binary_version"`
	ProtocolVersions []string `json:"protocol_versions"`
}

// HelloClientPayload is the body of a "hello" envelope sent by the phone
// after WS upgrade (docs/protocol-mobile.md § Message types). Role is
// always "client". LastSeenTS is optional; when present it triggers a
// backfill (docs/protocol-mobile.md § Backfill semantics).
//
// Token is the in-band carrier of the device-pairing token under v2
// (docs/protocol-mobile.md § Authentication, line 420). Empty under v1
// (carried as RoutingEnvelope.Token instead); the omitempty keeps v1
// round-trip byte-identical for existing fixtures. SECURITY: Token is
// plaintext credential material — MUST NOT be logged at any level.
type HelloClientPayload struct {
	Role             string     `json:"role"`
	DeviceName       string     `json:"device_name"`
	ClientVersion    string     `json:"client_version"`
	ProtocolVersions []string   `json:"protocol_versions"`
	LastSeenTS       *time.Time `json:"last_seen_ts,omitempty"`
	Token            string     `json:"token,omitempty"`
}

// HelloAckPayload is the body of a "hello_ack" envelope sent in response
// to "hello" (docs/protocol-mobile.md § Message types). ConnID echoes the
// relay-assigned id back to the phone for diagnostics only.
type HelloAckPayload struct {
	ProtocolVersion string `json:"protocol_version"`
	ServerID        string `json:"server_id"`
	ConnID          string `json:"conn_id"`
}

// ErrorPayload is the body of an "error" envelope (docs/protocol-mobile.md
// § Message types, § Error codes). RetryAfterS is optional and advisory;
// it is meaningful only when Retryable is true.
type ErrorPayload struct {
	Code        string `json:"code"`
	Message     string `json:"message"`
	Retryable   bool   `json:"retryable"`
	RetryAfterS *int   `json:"retry_after_s,omitempty"`
}

// AckPayload is the body of a generic "ack" envelope; empty by spec
// (docs/protocol-mobile.md § Message types).
type AckPayload struct{}
