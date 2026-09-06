// Package control implements pyry's local control plane: a Unix domain socket
// that lets clients (such as `pyry status`) query the running daemon.
//
// The protocol is line-delimited JSON. A client opens a connection, writes
// one JSON-encoded Request, and reads one JSON-encoded Response. The
// connection is then closed. Future verbs (attach, logs, stop) may extend
// this with streaming responses or upgraded connections, but the request
// shape stays JSON for forward compatibility.
package control

import (
	"encoding/json"
	"time"
)

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

	// VerbSessionsList returns a snapshot of every session in the pool.
	// Request carries no payload. Response.SessionsList carries the
	// snapshot. First read-side member of the sessions.<verb> namespace;
	// Pool.List is the only data source the server-side handler calls
	// (see #60 for the underlying primitive's bootstrap-label
	// substitution and sort-order guarantees).
	VerbSessionsList Verb = "sessions.list"

	// VerbSessionsHasID asks whether a session is currently registered
	// with the given UUID. Request.Sessions.ID carries the UUID;
	// Response.SessionsHasID carries the boolean answer. Pure registry
	// read — no claude spawn, no state transition. The 1.3c-2
	// foreground auto-attach path consumes this as a cheap alternative
	// to sessions.list. Empty / malformed input returns Response.Error;
	// a well-formed but absent UUID returns {Has: false}.
	VerbSessionsHasID Verb = "sessions.has-id"

	// VerbRekey asks the daemon to trigger an immediate Noise re-key on
	// the named v2 conn. Request.Rekey carries the conn id;
	// Response.OK acknowledges that the trigger has been accepted by
	// the v2 session manager (the underlying handshake runs
	// asynchronously on the conn's own state machine — this verb does
	// not wait for it).
	//
	// Slice A (#459) lays this wire and the dispatcher. The operator
	// subcommand (`pyry rekey <conn_id>`) and the V2SessionManager
	// implementation that satisfies Rekeyer are slice B (#460); until
	// then every production-path VerbRekey request returns
	// "rekey: no rekeyer configured".
	VerbRekey Verb = "rekey"

	// VerbMCPApprove forwards a claude tool-approval request to the
	// daemon, which registers it with internal/permbridge, blocks for the
	// allow/deny verdict, and returns it. Request.Approve carries the
	// forwarded request; Response.Approve carries the verdict. The daemon
	// makes the human-facing decision (where modal surfacing lives, #1080)
	// rather than the ephemeral `pyry mcp-approve` MCP child that dials
	// this verb.
	//
	// Fail-closed: a socket disconnect, daemon shutdown, or approval
	// timeout mid-wait yields a deny verdict; a nil registry (v1 /
	// foreground, and production until #1080 wires a resolver) returns
	// Response.Error "no approval registry configured". The dotted
	// namespace matches sessions.* — the dot is a documentation
	// convention, not a parser rule.
	VerbMCPApprove Verb = "mcp.approve"

	// VerbAttachFile files a host file claude named — by a filesystem path
	// the model chose — under the conversation bound to the CALLING
	// session, and answers with the daemon-minted id of the stored
	// attachment. Request.AttachFile carries the session id and the path;
	// Response.AttachFile carries the id.
	//
	// The conversation is derived from the named session through the
	// daemon's own conversation registry, never from the request and never
	// from the follow-active cursor (which is stamped at enqueue by the
	// session router, so a file produced by a background conversation's
	// claude would otherwise be filed under whichever chat the operator
	// last messaged). #2143 established the same rule for uploads: the
	// destination travels per transfer, not through the cursor.
	//
	// Fail-closed: every error path answers. The path is confined to the
	// conversation's recorded workspace before it is read, and a nil file
	// attacher (never calling SetFileAttacher, or v1/foreground) returns
	// Response.Error "attachment.file: no file attacher configured" — the
	// same nil-dependency-degrades-cleanly shape as VerbRekey and
	// VerbMCPApprove. The dotted namespace matches sessions.* and
	// mcp.approve; the dot is a documentation convention, not a parser
	// rule.
	VerbAttachFile Verb = "attachment.file"
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

	// ErrCodeConnNotFound is set by the server when Rekeyer.Rekey
	// returns ErrConnNotFound (or any error wrapping it). The client
	// maps this back to the same sentinel so callers can errors.Is
	// against it. Analogue of ErrCodeSessionNotFound for the v2 conn
	// namespace used by VerbRekey.
	ErrCodeConnNotFound ErrorCode = "conn_not_found"
)

// Request is the wire format for a single client request.
type Request struct {
	Verb     Verb             `json:"verb"`
	Sessions *SessionsPayload `json:"sessions,omitempty"` // populated for VerbSessionsNew (Phase 1.1+)
	Rekey    *RekeyPayload    `json:"rekey,omitempty"`    // populated for VerbRekey
	Approve  *ApprovePayload  `json:"approve,omitempty"`  // populated for VerbMCPApprove

	AttachFile *AttachFilePayload `json:"attachFile,omitempty"` // populated for VerbAttachFile
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

// RekeyPayload carries the v2 conn id to re-key. ConnID has no omitempty:
// an empty connID is invalid input and the server-side guard
// (handleRekey) rejects it before calling Rekeyer. The camelCase JSON tag
// matches the control-socket convention (SessionsPayload.ID) — not to be
// confused with RoutingEnvelope.ConnID's snake-case `conn_id`, which is the
// mobile-WS wire and unrelated.
type RekeyPayload struct {
	ConnID string `json:"connID"`
}

// ApprovePayload is the forwarded tool-approval request the `pyry
// mcp-approve` subcommand marshals from claude's --permission-prompt-tool
// call and dials over the control socket. Fields mirror permbridge.Request
// with the snake_case tags of the T1 spike contract
// (fixture-p4-approval-contract.json) so the subcommand marshals straight
// from claude's tool input.
//
// Input is opaque tool-input carried as json.RawMessage so it round-trips
// byte-verbatim into the registry; the daemon never parses or dispatches on
// it. ToolUseID is the registry correlation key (not a credential); the
// server-side guard rejects an empty one. ToolName is echoed only in the
// content-free decision log's absence — it is never logged.
type ApprovePayload struct {
	ToolName  string          `json:"tool_name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
}

// ApproveResult is the allow/deny verdict returned to the socket caller.
// Its field/tag layout is deliberately byte-identical to permbridge.Verdict's
// wire shape so the `pyry mcp-approve` subcommand can marshal it straight to
// claude as the MCP tool result without a second translation. A future drift
// between the two must be caught here (same mirror justification as
// SessionInfo vs sessions.SessionInfo):
//
//	allow → {"behavior":"allow","updatedInput":{…}}
//	deny  → {"behavior":"deny","message":"…"}
type ApproveResult struct {
	Behavior     string          `json:"behavior"`
	UpdatedInput json.RawMessage `json:"updatedInput,omitempty"` // allow only
	Message      string          `json:"message,omitempty"`      // deny only
}

// AttachFilePayload names the session making the call and the host path the
// file to file lives at. Both fields are required and both are rejected empty
// by handleAttachFile's guard.
//
// SessionID is the CALLER's session, and it is the whole destination
// mechanism: the daemon maps it to a conversation through its own registry
// and takes the confinement root from that conversation's recorded workspace.
// Nothing here names a conversation, and naming one would not be honoured if
// it did. An empty SessionID is refused rather than defaulted — the seam it
// would otherwise reach, sessions.Pool.Lookup(""), resolves to the BOOTSTRAP
// session, so a defaulted empty id would silently file claude's bytes under a
// conversation that never asked for them.
//
// Path is CLAUDE-AUTHORED and is the security surface of this verb: every
// other attachment path component is daemon-minted or sanitised from a
// client-declared name, and this one is a filesystem path chosen by the model
// naming a file to read. It is confined, never trusted — made absolute
// against the conversation's workspace, resolved through its symlinks, and
// boundary-tested against that workspace canonicalised the same way, all
// before anything opens it. A relative path resolves against the workspace,
// not against the daemon's process directory.
//
// Neither field is loggable as sent: Path is a host path, and its leaf is the
// filename docs/protocol-mobile.md § Attachments bans logging for a privacy
// reason sanitising does not lift.
type AttachFilePayload struct {
	SessionID string `json:"sessionID"`
	Path      string `json:"path"`
}

// AttachFileResult carries the daemon-minted id of the stored attachment. The
// id is a lowercase UUIDv4 drawn from crypto/rand (conversations.NewID),
// obeying the published attachment_id shape, and is NEVER taken from the
// request — a client-chosen id here would let a caller address, and overwrite,
// storage it did not create.
//
// The id is not a capability: #2054 re-validates it against the conversation
// binding on retrieval, so holding one grants nothing an unannounced id would
// not. Safe to log; nothing else about this exchange is.
type AttachFileResult struct {
	AttachmentID string `json:"attachmentID"`
}

// Response is the wire format for a single server response. On success
// exactly one of the verb-specific fields is populated:
//   - Status: payload for VerbStatus
//   - Logs: payload for VerbLogs
//   - SessionsNew: payload for VerbSessionsNew
//   - SessionsList: payload for VerbSessionsList
//   - SessionsHasID: payload for VerbSessionsHasID
//   - Approve: verdict for VerbMCPApprove (allow or deny)
//   - AttachFile: minted attachment id for VerbAttachFile
//   - OK: success acknowledgment for verbs without a typed payload (e.g. VerbStop)
//
// Error is set when the server rejects the request.
type Response struct {
	Status        *StatusPayload       `json:"status,omitempty"`
	Logs          *LogsPayload         `json:"logs,omitempty"`
	SessionsNew   *SessionsNewResult   `json:"sessionsNew,omitempty"`   // populated for VerbSessionsNew
	SessionsList  *SessionsListPayload `json:"sessionsList,omitempty"`  // populated for VerbSessionsList (1.1b-B1)
	SessionsHasID *SessionsHasIDResult `json:"sessionsHasID,omitempty"` // populated for VerbSessionsHasID (1.3c-1)
	Approve       *ApproveResult       `json:"approve,omitempty"`       // populated for VerbMCPApprove
	AttachFile    *AttachFileResult    `json:"attachFile,omitempty"`    // populated for VerbAttachFile
	OK            bool                 `json:"ok,omitempty"`
	Error         string               `json:"error,omitempty"`
	ErrorCode     ErrorCode            `json:"errorCode,omitempty"` // typed sentinel token (1.1d-B1)
}

// SessionsNewResult carries the result of a successful sessions.new
// request. SessionID is the minted UUID as a string (not the
// sessions.SessionID newtype) so external clients need not import the
// sessions package.
type SessionsNewResult struct {
	SessionID string `json:"sessionID"`
}

// SessionsHasIDResult carries the boolean answer to a sessions.has-id
// query. Has is emitted unconditionally (no omitempty) so the wire
// distinguishes "id absent" ({"has":false}) from a malformed empty
// response ({}). Defined here, in protocol.go, so external Go callers
// don't transitively import internal/sessions.
type SessionsHasIDResult struct {
	Has bool `json:"has"`
}

// SessionsListPayload carries the result of a successful sessions.list
// request: a snapshot of every session in the pool, in the order returned
// by Pool.List (LastActiveAt descending, SessionID ascending tiebreak).
// Final user-facing ordering is the responsibility of the CLI renderer
// (61-B); this layer does not re-sort.
type SessionsListPayload struct {
	Sessions []SessionInfo `json:"sessions"`
}

// SessionInfo is one session's operator-visible metadata as carried on
// the wire. Mirrors sessions.SessionInfo (#60) field-for-field with
// wire-appropriate types: ID is encoded as a plain string (not the
// sessions.SessionID newtype), State as a self-documenting string
// ("active" / "evicted") matching the on-disk registry encoding, and
// LastActive as a time.Time (encoding/json marshals to RFC3339Nano).
//
// Bootstrap carries omitempty so the field elides for non-bootstrap
// entries — the discriminator is only meaningful for the one entry where
// it's true. ID, Label, State, and LastActive are always present.
//
// Defined here, in protocol.go, rather than reusing sessions.SessionInfo
// directly so external Go callers / future hand-written clients of the
// wire don't transitively import internal/sessions for the
// lifecycleState uint8 enum (kept package-private for the same reason).
//
// LastActive: encoding/json strips the monotonic-clock component on
// roundtrip. Tests that compare a pre-encode time.Time with the
// post-decode value must use time.Equal, not == or reflect.DeepEqual
// (see lessons.md § "JSON roundtrip strips monotonic-clock state").
type SessionInfo struct {
	ID         string    `json:"id"`
	Label      string    `json:"label"`
	State      string    `json:"state"`       // "active" | "evicted"
	LastActive time.Time `json:"last_active"` // RFC3339Nano on the wire
	Bootstrap  bool      `json:"bootstrap,omitempty"`
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
	Phase        string `json:"phase"`                  // starting | running | backoff | stopped
	ChildPID     int    `json:"child_pid,omitempty"`    // 0 when no child is running
	StartedAt    string `json:"started_at"`             // RFC3339
	Uptime       string `json:"uptime"`                 // since StartedAt
	RestartCount int    `json:"restart_count"`          // number of times the child has exited
	LastUptime   string `json:"last_uptime,omitempty"`  // duration of the most recent child
	NextBackoff  string `json:"next_backoff,omitempty"` // delay scheduled before the next spawn
}
