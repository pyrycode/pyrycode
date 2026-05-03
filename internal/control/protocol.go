// Package control implements pyry's local control plane: a Unix domain socket
// that lets clients (such as `pyry status`) query the running daemon.
//
// The protocol is line-delimited JSON. A client opens a connection, writes
// one JSON-encoded Request, and reads one JSON-encoded Response. The
// connection is then closed. Future verbs (attach, logs, stop) may extend
// this with streaming responses or upgraded connections, but the request
// shape stays JSON for forward compatibility.
package control

// Verb identifies a control request.
type Verb string

const (
	// VerbStatus asks for a snapshot of supervisor state.
	VerbStatus Verb = "status"

	// VerbStop asks the daemon to shut down. The server acknowledges with
	// Response.OK before initiating shutdown so the client gets confirmation
	// even though the socket disappears moments later.
	VerbStop Verb = "stop"

	// VerbLogs returns the most recent supervisor log lines from an
	// in-memory ring buffer.
	VerbLogs Verb = "logs"

	// VerbAttach upgrades the connection: after a JSON ack from the server,
	// the rest of the connection is raw bytes bridged to the supervised
	// claude process's PTY. Standard "protocol upgrade" pattern.
	VerbAttach Verb = "attach"

	// VerbResize carries a live window-size update for an attached session.
	// One-shot request/response on a fresh control connection — independent
	// of the (long-lived) attach connection so a malformed resize never
	// disturbs the byte stream.
	VerbResize Verb = "resize"
)

// Request is the wire format for a single client request.
type Request struct {
	Verb   Verb           `json:"verb"`
	Attach *AttachPayload `json:"attach,omitempty"` // populated for VerbAttach
	Resize *ResizePayload `json:"resize,omitempty"` // populated for VerbResize
}

// AttachPayload carries the client's terminal geometry at attach time and
// (Phase 1.1+) selects which session to attach to.
//
// SessionID is a loose-input selector: a full UUID, a unique prefix, or
// empty to mean "the bootstrap session". The server resolves it through
// Pool.ResolveID; see that method for resolution rules. The omitempty tag
// is load-bearing — an empty SessionID must marshal to no field on the
// wire so v0.5.x clients (which don't know the field) keep round-tripping
// byte-identically against a v0.7.x server during the rollover window.
//
// Handshake Cols/Rows are applied to the supervised PTY at attach time via
// Bridge.Resize (see #136). Either dimension being zero is the "unknown /
// don't touch" sentinel — no resize is issued.
//
// Live resize updates while attached are carried by VerbResize on a
// separate control connection (see ResizePayload), emitted from the client
// by the SIGWINCH handler in pyry attach (startWinsizeWatcher).
type AttachPayload struct {
	Cols      int    `json:"cols,omitempty"`
	Rows      int    `json:"rows,omitempty"`
	SessionID string `json:"sessionID,omitempty"`
}

// ResizePayload carries a live window-size update for an attached session.
// SessionID resolution mirrors AttachPayload — empty selects bootstrap, full
// UUID or unique prefix selects a specific session. Cols/Rows are wire ints
// for symmetry with AttachPayload; the server narrows + swaps at the seam
// boundary. Either dimension being zero is the "unknown / don't touch"
// sentinel — no resize is issued (same rule as the handshake path).
type ResizePayload struct {
	SessionID string `json:"sessionID,omitempty"`
	Cols      int    `json:"cols,omitempty"`
	Rows      int    `json:"rows,omitempty"`
}

// Response is the wire format for a single server response. On success
// exactly one of the verb-specific fields is populated:
//   - Status: payload for VerbStatus
//   - Logs: payload for VerbLogs
//   - OK: success acknowledgment for verbs without a typed payload (e.g. VerbStop)
//
// Error is set when the server rejects the request.
type Response struct {
	Status *StatusPayload `json:"status,omitempty"`
	Logs   *LogsPayload   `json:"logs,omitempty"`
	OK     bool           `json:"ok,omitempty"`
	Error  string         `json:"error,omitempty"`
}

// LogsPayload carries recent supervisor log lines, oldest first. Capacity
// is the ring buffer's configured size — useful for the client to know
// whether the response is the full history or a tail of a longer one.
type LogsPayload struct {
	Lines    []string `json:"lines"`
	Capacity int      `json:"capacity"`
}

// StatusPayload describes the supervisor's runtime state. All durations are
// formatted as Go duration strings (e.g. "310ms", "1.5s") so they survive a
// JSON round-trip without losing precision the way nanosecond integers do
// when piped through tools like jq.
type StatusPayload struct {
	Phase        string `json:"phase"`                   // starting | running | backoff | stopped
	ChildPID     int    `json:"child_pid,omitempty"`     // 0 when no child is running
	StartedAt    string `json:"started_at"`              // RFC3339
	Uptime       string `json:"uptime"`                  // since StartedAt
	RestartCount int    `json:"restart_count"`           // number of times the child has exited
	LastUptime   string `json:"last_uptime,omitempty"`   // duration of the most recent child
	NextBackoff  string `json:"next_backoff,omitempty"`  // delay scheduled before the next spawn
}
