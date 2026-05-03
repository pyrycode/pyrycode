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

	// VerbSessionsNew creates a new session. Request.Sessions carries an
	// optional human-friendly label; Response.SessionsNew carries the
	// minted session UUID. First member of the Phase 1.1 sessions.* verb
	// family — the dot in the verb string is a documentation convention,
	// not a parser rule.
	VerbSessionsNew Verb = "sessions.new"

	// VerbSessionsRm removes an existing session. Request.Sessions carries
	// the session ID and JSONL disposition policy; Response.OK acknowledges
	// success. Typed errors from the pool (ErrSessionNotFound,
	// ErrCannotRemoveBootstrap) propagate through Response.ErrorCode so the
	// CLI can match them with errors.Is.
	VerbSessionsRm Verb = "sessions.rm"

	// VerbSessionsRename updates an existing session's human-friendly
	// label. Request.Sessions carries the session ID and the new label
	// (empty newLabel clears the on-disk label, per Pool.Rename's
	// contract); Response.OK acknowledges success. The typed
	// ErrSessionNotFound from the pool propagates through
	// Response.ErrorCode == ErrCodeSessionNotFound so the CLI can match
	// it with errors.Is. No new ErrorCode constants are introduced —
	// ErrCodeSessionNotFound (1.1d-B1) is reused.
	VerbSessionsRename Verb = "sessions.rename"
)

// JSONLPolicy is the wire-level enum selecting how the daemon disposes of a
// removed session's on-disk JSONL transcript file. Empty string is treated
// as JSONLPolicyLeave (backward-compat / zero-value ergonomics, same default
// as sessions.JSONLLeave).
//
// Kept distinct from sessions.JSONLPolicy (a uint8) so protocol.go stays
// import-free and the wire bytes are jq-debuggable strings rather than
// integers.
type JSONLPolicy string

const (
	JSONLPolicyLeave   JSONLPolicy = "leave"
	JSONLPolicyArchive JSONLPolicy = "archive"
	JSONLPolicyPurge   JSONLPolicy = "purge"
)

// ErrorCode is a stable wire token identifying a typed server-side error.
// Empty when the response carries no typed sentinel; the server still
// populates Response.Error with the human-readable message in every error
// case. Decoupling the token from the message string lets the client map
// it back to a Go sentinel for errors.Is matching without coupling the
// wire contract to error message text.
type ErrorCode string

const (
	// ErrCodeSessionNotFound is set by the server when Pool.Remove returns
	// sessions.ErrSessionNotFound. The client maps this back to the same
	// sentinel so callers can errors.Is against it.
	ErrCodeSessionNotFound ErrorCode = "session_not_found"

	// ErrCodeCannotRemoveBootstrap is set by the server when Pool.Remove
	// returns sessions.ErrCannotRemoveBootstrap.
	ErrCodeCannotRemoveBootstrap ErrorCode = "cannot_remove_bootstrap"
)

// Request is the wire format for a single client request.
type Request struct {
	Verb     Verb             `json:"verb"`
	Attach   *AttachPayload   `json:"attach,omitempty"`   // populated for VerbAttach
	Resize   *ResizePayload   `json:"resize,omitempty"`   // populated for VerbResize
	Sessions *SessionsPayload `json:"sessions,omitempty"` // populated for VerbSessionsNew (Phase 1.1+)
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

// SessionsPayload carries arguments shared across the sessions.* verb
// family. Today Label is used by sessions.new; ID and JSONLPolicy are used
// by sessions.rm; ID and NewLabel are used by sessions.rename. Phase 1.1e
// (attach) will add further omitempty fields to the same struct.
//
// Label is the human-friendly name supplied by the client. Empty maps to
// a no-label session — Pool.Create accepts it verbatim and the registry
// stores ""; not an error.
//
// ID is populated for VerbSessionsRm and VerbSessionsRename.
//
// JSONLPolicy is populated for VerbSessionsRm. Empty JSONLPolicy is
// treated by the server as JSONLPolicyLeave.
//
// NewLabel is populated for VerbSessionsRename. An empty NewLabel on the
// wire (omitted via omitempty) is forwarded to Pool.Rename as the empty
// string and clears the on-disk label per #62's contract.
type SessionsPayload struct {
	Label       string      `json:"label,omitempty"`       // sessions.new
	ID          string      `json:"id,omitempty"`          // sessions.rm, sessions.rename
	JSONLPolicy JSONLPolicy `json:"jsonlPolicy,omitempty"` // sessions.rm
	NewLabel    string      `json:"newLabel,omitempty"`    // sessions.rename
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
//   - SessionsNew: payload for VerbSessionsNew
//   - OK: success acknowledgment for verbs without a typed payload (e.g. VerbStop)
//
// Error is set when the server rejects the request.
type Response struct {
	Status      *StatusPayload     `json:"status,omitempty"`
	Logs        *LogsPayload       `json:"logs,omitempty"`
	SessionsNew *SessionsNewResult `json:"sessionsNew,omitempty"` // populated for VerbSessionsNew
	OK          bool               `json:"ok,omitempty"`
	Error       string             `json:"error,omitempty"`
	ErrorCode   ErrorCode          `json:"errorCode,omitempty"` // typed sentinel token (1.1d-B1)
}

// SessionsNewResult carries the result of a successful sessions.new
// request. SessionID is the minted UUID as a string (not the
// sessions.SessionID newtype) so external clients need not import the
// sessions package.
type SessionsNewResult struct {
	SessionID string `json:"sessionID"`
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
