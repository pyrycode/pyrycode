package protocol

// Claude account source status frames are pure wire data. They are wired in
// handlers.RequestClaudeAccount (#2839). request_claude_account uses
// authenticated paired-client map dispatch, with no conversation/session lookup
// and no interactive-capability gate.
//
// Each request is answered by exactly one unicast claude_account correlated by
// Envelope.InReplyTo, never broadcast. A client refreshes by asking again. A
// failed source read is reported in-band as state "failed" with a short
// daemon-authored reason, not as an error frame. No key carries the token, the
// source path or a secret reference such as a 1Password URI. These are
// consumer obligations, not validation by these DTOs.

// ClaudeAccountPayload.Kind values. A client must accept an unknown kind.
const (
	// ClaudeAccountKindMachineLogin means no source is configured: the daemon
	// uses the Claude login it inherited from the machine.
	ClaudeAccountKindMachineLogin = "machine_login"
	ClaudeAccountKindFile         = "file"
	ClaudeAccountKindOnePassword  = "1password"
	// ClaudeAccountKindOSKeychain has no daemon source yet (#2815); it is
	// declared so clients can be built against it.
	ClaudeAccountKindOSKeychain = "os_keychain"
)

// ClaudeAccountPayload.State values.
const (
	ClaudeAccountStateReady         = "ready"
	ClaudeAccountStateFailed        = "failed"
	ClaudeAccountStateNotConfigured = "not_configured"
)

// RequestClaudeAccountPayload is the body of request_claude_account, client →
// daemon. It encodes as {} and reads the connected daemon's account source.
type RequestClaudeAccountPayload struct{}

// ClaudeAccountPayload is the body of claude_account, daemon → client. All four
// keys always serialize: Label is the operator-given account label and Reason
// the short failure reason, each "" when absent. Correlation is solely
// Envelope.InReplyTo.
type ClaudeAccountPayload struct {
	Kind   string `json:"kind"`
	Label  string `json:"label"`
	State  string `json:"state"`
	Reason string `json:"reason"`
}
