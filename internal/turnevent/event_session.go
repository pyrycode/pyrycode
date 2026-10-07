package turnevent

// ModelAnnounced reports the model claude says it is running, from claude's
// system/init line. It is the only thing that answers what is running: the
// model on protocol.ScreenSnapshotPayload and protocol.SessionSettingsPayload is
// the per-session override, "" when inherited, so the daemon knows what it asked
// for and only claude knows what it got.
//
// Per line, not per session: claude emits init once per turn, so one session
// produces several and they need not agree. A /model turn's own init still
// reports the old model, so a consumer should render the latest announcement,
// not latch the first. The producer does not dedup, which would need turn state
// the parser does not hold. It opens and closes no turn.
//
// The init line carries 24 keys and this variant one. cwd, memory_paths and
// messaging_socket_path are paths on the operator's host and are not on the
// producer's decode target (streamsup's systemInitLine), so they cannot leak.
// The model is bounded at construction by streamsup's maxModelField.
type ModelAnnounced struct {
	// Model is claude's announced identifier, verbatim: no lowercasing, alias
	// expansion, date-stamping, family mapping or lookup against a published
	// model list. Other fields cite this as ModelAnnounced.Model's rule. Never
	// empty: the producer does not emit on an empty model.
	//
	// claude echoes an identifier at least as specific as the one it was given:
	// it dates a bare alias (haiku → claude-haiku-4-5-20251001) and passes one
	// already fully formed through (claude-haiku-4-5). So the value is not
	// reliably dated and need not appear in any published model list, which is a
	// reason to carry it untouched.
	//
	// Bounded and valid UTF-8 is all it is: streamsup's truncateField scrubs
	// invalid UTF-8, but nothing strips control characters or terminal escapes,
	// and only a phone-supplied override passes internal/relay's validModel.
	// turnbridge.MapEvent publishes it as protocol.ModelAnnouncedPayload, so the
	// client's render boundary owes the sanitization.
	Model string
	// Truncated reports whether Model was cut to fit the producer's cap. A bool
	// rather than a TruncatedFields slice because the payload is a single string,
	// so a slice could only ever be nil or ["model"].
	Truncated bool
}

// SessionFacts reports claude's own build and the permission posture it runs
// under, two more keys of the system/init line ModelAnnounced maps. As with the
// model, the daemon knows what it asked for and only claude knows what it got,
// so an unexpected posture is visible here.
//
// "Session" names the child run these facts describe; the event carries no
// session identity. The name matches the wire type, which follows this
// package's variant names (internal/protocol's codes doc states the rule).
//
// A separate variant rather than a widened ModelAnnounced: widening a shipped
// payload is a wire-compatibility change, and the facts have different
// consumers. A client renders the model every turn, while a version and a
// posture are what an operator checks when something looks wrong.
//
// Per line, not per session, with ModelAnnounced's hazard: one per turn, no
// dedup. The values ordinarily agree; a consumer wanting change detection does
// it itself. The producer emits when either field is present, so either may be
// empty. It opens and closes no turn.
//
// There is no effort field: no init line carries an effort key even with an
// effort set on the argv and in band, which streamsup's effortInitPins test
// pins. The host paths on the init line are excluded as for ModelAnnounced, and
// MCP server status is MCPStatus's.
//
// Both strings are bounded at construction (streamsup's maxClaudeVersionField
// and maxPermissionModeField) and valid UTF-8, nothing more: a client rendering
// either owes the sanitization, as for ModelAnnounced.Model.
type SessionFacts struct {
	// ClaudeCodeVersion is claude's own build string, verbatim per
	// ModelAnnounced.Model's rule: not normalised or parsed into a semver triple.
	// A consumer comparing versions does its own parsing, since a build carrying
	// a suffix, date or channel name would force a parsing producer to invent an
	// answer.
	ClaudeCodeVersion string
	// PermissionMode is the posture claude says the child runs under ("default",
	// "bypassPermissions", "plan" and whatever claude adds), verbatim per
	// ModelAnnounced.Model's rule.
	//
	// An open set on purpose. streamsup's permissionModeAllowed bounds the mode
	// the daemon may ask for, where refusing an unknown value is right. This is
	// claude's report of what it is running, and an allow-list would drop the one
	// report an operator most needs: a mode the daemon has not heard of.
	//
	// It is a claim, not an authorization decision: a buggy or compromised claude
	// can report "default" under any posture. Nothing in the daemon gates on it
	// or feeds it back into a spawn or control request.
	PermissionMode string
	// TruncatedFields names cut fields ("claude_code_version",
	// "permission_mode") per BackgroundTaskStarted.TruncatedFields. A slice rather
	// than ModelAnnounced's bool because there are two candidate fields.
	TruncatedFields []string
}

// ContextUsage is one solicited reading of claude's context-window arithmetic.
// It is informational only: consumers may display it, but it does not replace
// the daemon-owned contextwindow.Read value used for control decisions.
//
// Every claude-authored string and every list is bounded by streamsup at
// construction (maxContextUsageStringBytes, maxContextUsageEntries), and each
// Dropped count reports the entries omitted from its list. Each list is ordered
// by descending token count so a count cut keeps the heaviest entries. The
// scalar integers are claude's own values; the daemon neither recomputes nor
// normalizes them.
type ContextUsage struct {
	Model              string
	TotalTokens        int
	MaxTokens          int
	Percentage         int
	Categories         []ContextUsageCategory
	DroppedCategories  int
	MCPTools           []ContextUsageMCPTool
	DroppedMCPTools    int
	MemoryFiles        []ContextUsageMemoryFile
	DroppedMemoryFiles int
}

// ContextUsageCategory is one named contribution to a ContextUsage reading.
type ContextUsageCategory struct {
	Name   string
	Tokens int
}

// ContextUsageMCPTool is one MCP tool's contribution to a ContextUsage reading.
type ContextUsageMCPTool struct {
	Name       string
	ServerName string
	Tokens     int
}

// ContextUsageMemoryFile is one memory file's contribution to a ContextUsage
// reading. Path is descriptive; nothing on this event path opens it.
type ContextUsageMemoryFile struct {
	Path   string
	Type   string
	Tokens int
}

// MCPStatus is one shape-recognised reading of the MCP servers claude reports,
// once per eligible initialize exchange. turnbridge.MapEvent publishes it as
// protocol.MCPStatusPayload; no daemon behaviour is keyed on it, and it opens and
// closes no turn. An empty Servers is meaningful (claude sent mcpServers:[]), so
// the producer emits it; a missing or unusable payload emits no event.
//
// Servers keeps claude's order and at most streamsup's maxMCPStatusServers
// entries. DroppedServers counts every entry omitted from the tail, so
// len(Servers) + DroppedServers is claude's array length. The count is
// daemon-derived; every string remains claude-authored.
type MCPStatus struct {
	Servers        []MCPServerStatus
	DroppedServers int
}

// MCPServerStatus is one entry of MCPStatus, not an Event. It carries only the
// server object's top-level name, status, error and scope plus
// serverInfo.version. Config, tools and serverInfo.name are not on the decode
// target, so no consumer can recover credentials, argv, tool metadata or a
// second spelling of the server name from it.
//
// Every field is copied without canonicalisation and is untrusted, unsanitized
// text. Error, the one free-form prose field, is cut to streamsup's
// maxMCPStatusError bytes as valid UTF-8. A renderer must treat every string as
// inert claude-authored content, never as an actuator or command argument.
type MCPServerStatus struct {
	Name    string
	Status  string
	Error   string
	Scope   string
	Version string
}

// ConversationReset reports that claude reset the conversation and mounted a
// fresh transcript under a new id. It maps claude's top-level
// conversation_reset line, which claude writes when a /clear, a plan-mode exit or
// a fresh-session flow runs. It opens and closes no turn.
//
// It carries claude's own identity on purpose, the one exception to the
// package rule: following claude to the transcript it just mounted is the
// reason the event exists.
//
// Its consumer is cmd/pyry's sessionResetFollower. interactiveTurnEmitterV2.Handle
// has no case for it and turnbridge.MapEvent does not map it: the boundary a
// client draws comes from the session_transition frame.
type ConversationReset struct {
	// NewConversationID is the id claude says it mounted the fresh transcript
	// under, verbatim.
	//
	// Canonical by construction and never empty: the producer (streamsup's
	// emitConversationReset) runs it through transcript.ValidStem and emits
	// nothing on failure, so a consumer holds a 36-character lowercase
	// hex-and-hyphen stem or no event at all. A resolver of <dir>/<id>.jsonl may
	// use it as a filename component, since the alphabet holds no path
	// metacharacter. The check lives in the producer because this package
	// imports only the standard library. The fixed-length match bounds the value
	// more tightly than a cap would, so there is no cap and no Truncated report.
	//
	// Well-formed is not authentic: a buggy or compromised claude can announce
	// any well-formed stem, including another session's. A consumer that re-keys
	// on it owns the authorization question.
	NewConversationID string
}
