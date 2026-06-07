package protocol

import "time"

// CapabilityInteractive is the wire vocabulary string a phone advertises in
// its hello.payload.capabilities to opt into the v2 interactive event
// stream, and that the daemon echoes in hello_ack.payload.capabilities when
// it supports it (docs/protocol-mobile.md § Capability negotiation). This
// is pure vocabulary — the trust decision (the daemon intersecting the
// phone's advertised set with its own supported set, never blindly
// mirroring the phone's claims) lives in the consumer, #608, not here.
const CapabilityInteractive = "interactive"

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
//
// Capabilities is the phone's advertised feature set (e.g.
// [CapabilityInteractive]); omitempty so a phone advertising nothing
// round-trips byte-identically with the v1 hello shape (the key is absent,
// not null). The daemon intersecting this with its own supported set is
// #608's job — this field is the advertisement only, no enforcement here.
type HelloClientPayload struct {
	Role             string     `json:"role"`
	DeviceName       string     `json:"device_name"`
	ClientVersion    string     `json:"client_version"`
	ProtocolVersions []string   `json:"protocol_versions"`
	LastSeenTS       *time.Time `json:"last_seen_ts,omitempty"`
	Token            string     `json:"token,omitempty"`
	Capabilities     []string   `json:"capabilities,omitempty"`
}

// HelloAckPayload is the body of a "hello_ack" envelope sent in response
// to "hello" (docs/protocol-mobile.md § Message types). ConnID echoes the
// relay-assigned id back to the phone for diagnostics only.
//
// Capabilities is the daemon's supported feature set echoed back to the
// phone; omitempty so a daemon advertising nothing round-trips
// byte-identically with the v1 hello_ack shape. The daemon MUST echo only
// what it itself supports (the intersection with the phone's advertised
// set, never a blind mirror of the phone's claims) — that trust decision
// is #608's, not this wire-type layer's.
type HelloAckPayload struct {
	ProtocolVersion string   `json:"protocol_version"`
	ServerID        string   `json:"server_id"`
	ConnID          string   `json:"conn_id"`
	Capabilities    []string `json:"capabilities,omitempty"`
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
