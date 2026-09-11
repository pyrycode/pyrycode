package relay

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// This file holds the set_session_settings verb handler plus its model/effort
// validators — the interactive settings-update path — carved out of
// v2session.go (#1024). Pure move: same package, no behaviour change. The
// SettingsUpdate struct, SettingsUpdater seam interface, and its
// V2SessionConfig.SettingsUpdater field stay in v2session.go; the seam
// interfaces are their own later slice. #1021/#1022/#1023 carved out the
// handshake, re-key machinery, and modal+queue handlers before this slice.

// Static reply messages for set_session_settings failures (#845). Each is a FIXED
// constant — never attacker-influenced bytes. encoding/json quotes attacker bytes
// into its decode-error string, so no decode error, payload byte, or model/effort
// value ever reaches these messages, the wire, or a log.
const (
	msgSettingsMalformed       = "malformed set_session_settings request"
	msgSettingsModelNotOffered = "requested model is not offered"
	msgSettingsNotFound        = "unknown session_id"
	msgSettingsUnavailable     = "session settings unavailable"
)

// handleSetSessionSettings applies an inbound interactive set_session_settings
// control frame (#845): it validates the untrusted model/effort at the wire
// boundary, persists the change through the injected SettingsUpdater seam (#840's
// atomic Pool.UpdateSettings), and replies with a deterministic
// session_settings_updated success or a TypeError failure. Intercepted in
// dispatchAppFrame before dispatch.Route, like handleRequestSnapshot, and runs on
// the manager's single Run dispatch goroutine — so the s.interactive read is
// lock-free under the package's single-owner invariant. Unlike the fire-and-forget
// verbs (interrupt / new_session / dequeue_message) the interactive path ALWAYS
// replies. A RUNNING session picks the change up immediately, by a mechanism the
// seam picks on what the frame carried — a model/effort change, or ANY posture
// change including a bypass ENABLE, is written to the live child as command text
// or a control request (#1581, #1604, #2066); only a model or effort cleared back
// to claude's own default live-restarts the session's supervisor (#842). The
// enable used to restart too, because claude refused the escalation over the
// control channel and gated it on the launch argv (#1595); #2065 put that flag on
// every argv and #2060 measured claude accepting the re-escalation on such a
// child, so #2066 routed it in band with the other five. Both mechanisms install
// the recomposed argv (#833's path), so the next spawn carries it too.
//
// Order is load-bearing:
//  1. Capability gate (the authz boundary): a non-interactive conn is fully inert
//     — no decode, no seam call, NO reply. It must not even learn whether a
//     session exists (AC #6). A bare interactive check, matching handleInterrupt —
//     NOT a reusable inbound-gate abstraction (CODING-STYLE: over-DRY).
//  2. Decode: this verb owes a reply, so a decode failure yields a malformed reply
//     and persists nothing (AC #4). NEVER echo the decode error or any payload
//     byte — encoding/json quotes attacker bytes into its error string.
//  3. Validate model/effort/permission mode BEFORE any persistence (AC #5): an
//     invalid value yields a malformed reply, not a persisted bad setting. YOLO
//     needs no value check — a malformed yolo already failed step 2's type-decode,
//     so bypass can never be inferred from a bad value (AC #4). The permission
//     mode (#1687) adds two rejects here, both replying with the same fixed
//     constant so no reject is distinguishable by its reply: a frame carrying BOTH
//     a mode and a YOLO, and a mode outside validPermissionMode's closed five.
//     Neither reject is logged — a permission mode is a settings value and #833
//     keeps those out of the daemon log at every level, so "rejected mode X" must
//     not be added for debuggability.
//  4. Nil-seam guard: a nil SettingsUpdater replies "unavailable" deterministically
//     (foreground / unwired), never a silent drop.
//  5. Validate availability + persist: the injected adapter checks a non-empty
//     model against the retained published vocabulary before Pool.UpdateSettings.
//     A complete-menu absence becomes a fixed non-retryable malformed reply; an
//     incomplete vocabulary becomes retryable model_list.unavailable. Otherwise
//     UpdateSettings merges all fields under one atomic save (rollback on failure),
//     so a partial write is impossible. ErrSessionUnknown becomes session.not_found;
//     any remaining error becomes server-unavailable; nil becomes success.
func (m *V2SessionManager) handleSetSessionSettings(ctx context.Context, s *V2Session, env protocol.Envelope) {
	if !s.interactive {
		return // non-interactive conn: inert, no reply (AC #6 negative path)
	}

	var p protocol.SetSessionSettingsPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		// Owes a reply (unlike the fire-and-forget verbs). NEVER echo err or any
		// payload byte — encoding/json quotes attacker bytes into its error string.
		m.settingsReplyError(ctx, s, env.ID, protocol.CodeProtocolMalformed, msgSettingsMalformed, false)
		return
	}

	// Validate untrusted values before any persistence (AC #5): a rejected value is
	// never stored and never reaches the claude argv (the argv-injection defense).
	if p.Model != nil && !validModel(*p.Model) {
		m.settingsReplyError(ctx, s, env.ID, protocol.CodeProtocolMalformed, msgSettingsMalformed, false)
		return
	}
	if p.Effort != nil && !validEffort(*p.Effort) {
		m.settingsReplyError(ctx, s, env.ID, protocol.CodeProtocolMalformed, msgSettingsMalformed, false)
		return
	}
	// A permission mode and a YOLO bit are two spellings of ONE posture, so a
	// frame carrying both is refused (#1687) — checked BEFORE the mode's value so
	// the refusal holds even for a mode this daemon does not recognise, and so
	// "neither field can win over the other" is unconditional. Refusal rather than
	// precedence, for the reason sessions.ErrPermissionModeConflict records one
	// layer in: letting the mode win downgrades an escalation silently, letting
	// YOLO win GRANTS one from a frame that said false, so neither is fail-safe in
	// both directions. This is deliberately STRICTER than the pool's own rule,
	// which refuses only a CONTRADICTING pair: at the wire there is no precedence
	// question for a client author or a reviewer to answer, and no client sends
	// both.
	if p.PermissionMode != nil && p.YOLO != nil {
		m.settingsReplyError(ctx, s, env.ID, protocol.CodeProtocolMalformed, msgSettingsMalformed, false)
		return
	}
	if p.PermissionMode != nil && !validPermissionMode(*p.PermissionMode) {
		m.settingsReplyError(ctx, s, env.ID, protocol.CodeProtocolMalformed, msgSettingsMalformed, false)
		return
	}

	if m.cfg.SettingsUpdater == nil {
		// Unwired seam (foreground / pre-wire): report unavailable, never drop.
		m.settingsReplyError(ctx, s, env.ID, protocol.CodeServerBinaryOffline, msgSettingsUnavailable, true)
		return
	}

	// Post-validation the payload pointers pass straight through — same presence
	// contract. UpdateSettings applies all present fields atomically (one
	// saveLocked, rollback on failure), so any combination applies or nothing does.
	err := m.cfg.SettingsUpdater.UpdateSettings(p.SessionID, SettingsUpdate{
		Model:          p.Model,
		Effort:         p.Effort,
		YOLO:           p.YOLO,
		PermissionMode: p.PermissionMode,
	})
	if errors.Is(err, ErrSessionUnknown) {
		m.settingsReplyError(ctx, s, env.ID, protocol.CodeSessionNotFound, msgSettingsNotFound, false)
		return
	}
	if errors.Is(err, ErrModelNotOffered) {
		m.cfg.Logger.Info("relay: v2 set_session_settings model not offered",
			"event", "v2.settings.model_not_offered",
			"conn_id", s.connID,
			"session_id", p.SessionID)
		m.settingsReplyError(ctx, s, env.ID, protocol.CodeProtocolMalformed, msgSettingsModelNotOffered, false)
		return
	}
	if errors.Is(err, ErrModelVocabularyUnavailable) {
		m.cfg.Logger.Info("relay: v2 set_session_settings model vocabulary unavailable",
			"event", "v2.settings.model_vocabulary_unavailable",
			"conn_id", s.connID,
			"session_id", p.SessionID)
		m.settingsReplyError(ctx, s, env.ID, protocol.CodeModelListUnavailable, msgModelListUnavailable, true)
		return
	}
	if err != nil {
		// Persist/disk failure: the id matched a real session (UpdateSettings rejects
		// an unknown id before touching disk), so session_id is a confirmed-real
		// routing id, safe to log. Log the failure EVENT only — NEVER the wrapped err
		// (it can quote a path) — and reply with the fixed unavailable message.
		m.cfg.Logger.Warn("relay: v2 set_session_settings persist failed",
			"event", "v2.settings.persist_err",
			"conn_id", s.connID,
			"session_id", p.SessionID)
		m.settingsReplyError(ctx, s, env.ID, protocol.CodeServerBinaryOffline, msgSettingsUnavailable, true)
		return
	}

	// Success (AC #1/#2): the change is persisted atomically and reaches a running
	// claude immediately — in-band as command text or a control request for a
	// model/effort change or ANY posture change, the bypass enable included since
	// #2066 (#1581, #1604); by live restart only for a model or effort cleared back
	// to claude's own default (#842), which is the surviving restart case. Build the
	// reply inline, mirroring handleRequestSnapshot. Echoing p.SessionID is safe —
	// on the nil-error path it matched a real session key exactly, so it is a
	// confirmed-real, non-secret routing id in a typed struct field, not an
	// error-string interpolation.
	updated, merr := json.Marshal(protocol.SessionSettingsUpdatedPayload{SessionID: p.SessionID})
	if merr != nil {
		// A closed struct of one string; marshal cannot fail in practice. Defensive
		// — NEVER echo merr; fall back to the deterministic unavailable reply so the
		// request is still answered, never silently dropped (AC #3).
		m.cfg.Logger.Warn("relay: v2 session_settings_updated marshal failed",
			"event", "v2.settings.marshal_err",
			"conn_id", s.connID,
			"session_id", p.SessionID)
		m.settingsReplyError(ctx, s, env.ID, protocol.CodeServerBinaryOffline, msgSettingsUnavailable, true)
		return
	}
	inReplyTo := env.ID
	reply := protocol.Envelope{
		ID:        1, // non-load-bearing; the phone correlates on InReplyTo.
		Type:      protocol.TypeSessionSettingsUpdated,
		TS:        time.Now().UTC(),
		Payload:   updated,
		InReplyTo: &inReplyTo,
	}
	// One content-free info log: conn_id + session_id only. The model / effort /
	// YOLO / permission mode values are NEVER logged at any level (#833 keeps them
	// out of logs).
	m.cfg.Logger.Info("relay: v2 session settings updated",
		"event", "v2.settings.updated",
		"conn_id", s.connID,
		"session_id", p.SessionID)
	if err := m.forwardEnvelope(ctx, s.connID, reply); err != nil {
		// Unreachable in practice: s is V2StateOpen on the dispatch goroutine.
		// Logged at debug and dropped — the package's outbound-drop posture.
		m.cfg.Logger.Debug("relay: v2 session_settings_updated push dropped",
			"event", "v2.settings.push_err",
			"conn_id", s.connID,
			"err", err)
	}
}

// handleRequestSessionSettings answers an inbound request_session_settings
// control frame with the run configuration of the conversation the client named
// (#491, #1214, #1610): that conversation's bound session id to address changes
// to, the model / effort / YOLO / permission mode in force on it, and its
// context-window occupancy. It is the READ half of the #844 cluster, which
// shipped write-only.
//
// The reported permission mode (#1687) is what lets a client label its menu from
// the daemon's state rather than from the request it last sent. It can name a
// posture the WRITE half refuses to accept — a bypass session reports
// "bypassPermissions" here while only the YOLO bit can set it — because the
// daemon stores mode and YOLO so they cannot disagree, and reporting the posture
// the session is actually in is the point of a read half.
//
// Intercepted in dispatchAppFrame before dispatch.Route, like
// handleSetSessionSettings above, and runs on the manager's single Run dispatch
// goroutine.
//
// It deliberately does NOT consult m.cfg.Snapshotter, and that is the entire
// point of the verb existing. A client used to read these values off
// screen_snapshot's side-load (#848, #857), but that reply is gated on a live
// terminal screen: on the stream-json runner Snapshotter is nil by construction
// (#1077/#1101), so handleRequestSnapshot short-circuits to
// server.binary_offline and takes the settings — which have nothing to do with a
// terminal — down with it. On the runner now in production that left the
// run-configuration UI with no values, no session id and no context figure at
// all. This handler reads one conversation-keyed seam of primitives, so it
// answers identically on both runners.
//
// Order mirrors the write handler, minus the steps a read cannot need:
//  1. Capability gate (the authz boundary): a non-interactive conn is fully
//     inert — no decode, no resolution, no seam call, NO reply, so it cannot
//     even learn whether a session or a conversation exists. Same posture as the
//     write path's AC #6, and it MUST stay ahead of both steps below.
//  2. Decode, tolerated (#1586): the frame names the conversation the client is
//     asking about. A decode failure leaves the id empty, so there is no
//     malformed-payload branch and no error check on the Unmarshal return.
//     NEVER echo or log the decode error; encoding/json quotes attacker bytes
//     into its error string.
//  3. Resolve, or don't (#1610): one conversation-keyed read of RunConfigFor.
//     A request that names no conversation, names one this daemon does not host,
//     or names one bound to no live session resolves NOTHING and falls through
//     with every value at zero.
//  4. No nil-seam error branch: unlike the write path, the seam degrades to its
//     zero value, which the wire contract defines as a real answer
//     ("" ⇒ nothing to address, 0 window ⇒ usage unwired). A read that reports
//     "I have nothing" is more useful than an error, and it keeps the reply
//     shape constant so a client parses one thing. Step 3's unresolvable case
//     reuses that same shape rather than adding a failure branch to a verb
//     documented as always answering.
//
// SCOPE: the reported session id and the reported values describe ONE session —
// the one bound to the conversation the request named — because they arrive
// together as one RunConfig, so a client can never read one session's values and
// write its change to another. An unresolvable request addresses NOTHING: it is
// answered with the zero reply, never the shared bootstrap session's id or
// values, which is the route #678 AC #4 forbids any client-driven verb from
// reaching.
//
// The reply NEVER carries a screen byte, a transcript byte, or a file path —
// only the id, three short enum-ish strings, a bool and two aggregate integers.
// The requested conversation_id reaches neither the reply, a log line, nor an
// error string; it is a lookup key and nothing else.
func (m *V2SessionManager) handleRequestSessionSettings(ctx context.Context, s *V2Session, env protocol.Envelope) {
	if !s.interactive {
		return // non-interactive conn: inert, no reply (mirrors the write path)
	}

	var p protocol.RequestSessionSettingsPayload
	// A decode failure is tolerated: it leaves ConversationID == "", which names
	// no conversation and so addresses nothing — the zero reply below. A bare
	// frame from an un-updated client carries a nil Payload, which Unmarshal
	// rejects while leaving p zeroed, and reaches exactly the same place. So
	// there is no malformed-payload branch and no error check here. The error is
	// never echoed and never logged (encoding/json quotes attacker bytes into
	// it).
	_ = json.Unmarshal(env.Payload, &p)

	// Resolve the named conversation, or report nothing. Three properties, each
	// deliberate:
	//
	// The empty-id guard keeps "an unnamed request addresses nothing" a property
	// of this package alone, provable against any RunConfigFor double rather than
	// inherited from whatever the cmd/pyry producer happens to do; it is also the
	// relay-side half of the observed Pool.Lookup("") == bootstrap hazard the
	// producer already guards (#678).
	//
	// The nil-seam guard is the foreground / v1 case: nothing resolves, so the
	// request fails closed exactly as handleRequestSnapshot's nil seam does.
	//
	// The comma-ok is HONOURED, not discarded. RunConfigFor's doc says a caller
	// MUST NOT read the fields on ok == false, so cfg is assigned only on true.
	// Writing `cfg, _ = …` would happen to work today only because the producer
	// zeroes its refusal return — a property of cmd/pyry, not of this contract.
	var cfg RunConfig
	if p.ConversationID != "" && m.cfg.RunConfigFor != nil {
		if got, ok := m.cfg.RunConfigFor(p.ConversationID); ok {
			cfg = got
		}
	}

	// All seven fields come from the one RunConfig, so the reported id and the
	// reported values always describe the same session — including in the zero
	// case, which the wire contract already defines as a real answer. That extends
	// to the permission mode and the YOLO bit, which the daemon stores so they can
	// never disagree (#1687), so no client can read a posture assembled from two
	// different sessions.
	payload, err := json.Marshal(protocol.SessionSettingsPayload{
		SessionID:      cfg.SessionID,
		Model:          cfg.Model,
		Effort:         cfg.Effort,
		YOLO:           cfg.YOLO,
		PermissionMode: cfg.PermissionMode,
		UsedTokens:     cfg.UsedTokens,
		WindowTokens:   cfg.WindowTokens,
	})
	if err != nil {
		// A closed struct of two strings, a bool and two ints; marshal cannot fail
		// in practice. Defensive — NEVER echo err; answer with the deterministic
		// unavailable reply so the request is still answered, never silently
		// dropped (same posture as the write path's marshal branch).
		m.cfg.Logger.Warn("relay: v2 session_settings marshal failed",
			"event", "v2.settings.read_marshal_err",
			"conn_id", s.connID)
		m.settingsReplyError(ctx, s, env.ID, protocol.CodeServerBinaryOffline, msgSettingsUnavailable, true)
		return
	}

	inReplyTo := env.ID
	reply := protocol.Envelope{
		ID:        1, // non-load-bearing; the phone correlates on InReplyTo.
		Type:      protocol.TypeSessionSettings,
		TS:        time.Now().UTC(),
		Payload:   payload,
		InReplyTo: &inReplyTo,
	}
	// Content-free debug log: conn_id only. The model / effort / YOLO / permission
	// mode values are NEVER logged at any level (#833 keeps them out of logs), and
	// neither are the
	// usage integers or the session id — this is a routine read that can fire on
	// every sheet open, so it logs less than the write path, not more.
	m.cfg.Logger.Debug("relay: v2 session settings reported",
		"event", "v2.settings.reported",
		"conn_id", s.connID)
	if err := m.forwardEnvelope(ctx, s.connID, reply); err != nil {
		// Unreachable in practice: s is V2StateOpen on the dispatch goroutine.
		// Logged at debug and dropped — the package's outbound-drop posture.
		m.cfg.Logger.Debug("relay: v2 session_settings push dropped",
			"event", "v2.settings.read_push_err",
			"conn_id", s.connID,
			"err", err)
	}
}

// settingsReplyError pushes a single TypeError reply to s, correlated to
// inReplyTo, via the same m.forwardEnvelope seal-and-forward path the success
// reply uses (no parallel send path). message MUST be a static constant — never
// attacker-controlled bytes. A third near-identical copy of the snapshot /
// debug-bundle error-reply helper is the established package posture (each
// reply-owing handler owns its helper); do NOT extract a shared one (over-DRY).
func (m *V2SessionManager) settingsReplyError(ctx context.Context, s *V2Session, inReplyTo uint64, code, message string, retryable bool) {
	errPayload, err := json.Marshal(protocol.ErrorPayload{
		Code:      code,
		Message:   message,
		Retryable: retryable,
	})
	if err != nil {
		// A closed struct of strings + bool; marshal cannot fail in practice.
		m.cfg.Logger.Warn("relay: v2 settings error reply marshal failed",
			"event", "v2.settings.err_marshal",
			"conn_id", s.connID,
			"code", code)
		return
	}
	reply := protocol.Envelope{
		ID:        1, // non-load-bearing; the phone correlates on InReplyTo.
		Type:      protocol.TypeError,
		TS:        time.Now().UTC(),
		Payload:   errPayload,
		InReplyTo: &inReplyTo,
	}
	if err := m.forwardEnvelope(ctx, s.connID, reply); err != nil {
		m.cfg.Logger.Debug("relay: v2 settings error reply push dropped",
			"event", "v2.settings.err_push",
			"conn_id", s.connID,
			"code", code,
			"err", err)
	}
}

// validModel reports whether m is an acceptable per-session model value from an
// untrusted set_session_settings frame (#845, widened by #1838). It is a SHAPE
// check, NOT an allowlist: model names churn per claude release, so a fixed
// allowlist would reject a new model and force a code edit per launch. It is also
// stateless by design — validating against a menu this daemon previously
// published would add cross-request state and a TOCTOU window while still needing
// a shape check underneath.
//
// The rule is a GRAMMAR rather than a byte set, because it has to be checkable in
// both directions — what it newly admits, and what it still refuses:
//
//	model    := "" | base variant?
//	base     := alnum wordbyte*
//	variant  := "[" wordbyte+ "]"
//	wordbyte := alnum | "." | "_" | "-"
//	alnum    := [A-Za-z0-9]
//
// plus a 64-byte bound over the WHOLE value, the variant group included — the
// charset widened at #1838, the length did not. "" is accepted (clear to the
// daemon template — claudeSettingsArgs emits no --model for it).
//
// The optional trailing group is the form claude publishes for a variant row in
// its own initialize model menu (claude-fable-5[1m], opus[1m]), which
// protocol.ModelOption carries to a client and which this validator refused until
// #1838. Admitting it is deliberately NARROWER than adding two bytes to the
// charset: the group's interior draws from the same closed class as the base, so
// a value carries at most one group and cannot nest one; the group is non-empty,
// is balanced, and — because its "]" must be the value's last byte — is the
// value's final element, never a leading or interior one.
//
// The two properties the closed class buys are unchanged, and the accepted value
// reaches two sinks, each depending on one of them:
//
//   - No leading-dash value (--foo) can pose as a claude flag, because base's
//     first byte is alphanumeric. That is the ARGV sink: claudeSettingsArgs emits
//     --model and the value as two separate elements of an argv slice and no
//     shell parses either, so "[" and "]" are ordinary bytes to execve.
//   - No shell metachar, whitespace, control byte, separator or byte >= 0x80 (a
//     multi-byte UTF-8 rune's continuation bytes) appears anywhere in an accepted
//     value. The live sink is now a set_model control request whose model is encoded
//     as a JSON string, so structured marshalling — not this grammar — prevents line
//     injection. Keeping the closed class still bounds network-originated values and
//     rejects ambiguous identifiers before they reach either live delivery or argv.
//
// That closure is machine-checked rather than asserted:
// TestValidModel_ByteSetIsClosed walks all 256 byte values in each of the three
// positions.
func validModel(m string) bool {
	if m == "" {
		return true
	}
	if len(m) > 64 {
		return false
	}
	base := m
	if i := strings.IndexByte(m, '['); i >= 0 {
		// Three conditions on the FIRST "[", and between them they rule out a
		// nested group, a second group and a trailing suffix without any of the
		// three needing a check of its own: the value's last byte closes the
		// group, the interior is non-empty, and every interior byte is a
		// wordbyte — which excludes both brackets, so nothing inside can open or
		// close another group.
		if m[len(m)-1] != ']' {
			return false
		}
		inner := m[i+1 : len(m)-1]
		if inner == "" {
			return false
		}
		for j := 0; j < len(inner); j++ {
			if !modelWordByte(inner[j]) {
				return false
			}
		}
		base = m[:i]
	}
	if base == "" {
		return false // a leading group: "[1m]" has no base to carry it
	}
	for i := 0; i < len(base); i++ {
		c := base[i]
		if i == 0 {
			if !modelAlnumByte(c) {
				return false
			}
			continue
		}
		if !modelWordByte(c) {
			return false
		}
	}
	return true
}

// modelAlnumByte reports whether c is in [A-Za-z0-9] — the class validModel's
// grammar demands of a value's FIRST byte, and the whole of the bar that keeps a
// leading-dash value from posing as a claude flag on the argv sink.
func modelAlnumByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// modelWordByte reports whether c is in [A-Za-z0-9._-] — validModel's closed byte
// class, and the ONE place it is written down. Both the base and the variant
// group's interior consult it, so "the group admits nothing the base does not" is
// a property of the code rather than of two lists kept in step by hand.
func modelWordByte(c byte) bool {
	return modelAlnumByte(c) || c == '.' || c == '_' || c == '-'
}

// validEffort reports whether e is an acceptable reasoning-effort value from an
// untrusted set_session_settings frame (#845). "" is accepted (clear to the
// template — claudeSettingsArgs emits no --effort for it); otherwise e is one of
// the closed enum {low, medium, high, xhigh, max}. The set matches
// cmd/pyry/agent_run.go's validEfforts (the established --effort enum) but is
// defined relay-local because internal/relay cannot import cmd/pyry.
func validEffort(e string) bool {
	switch e {
	case "", "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

// validPermissionMode reports whether mode is an acceptable permission posture
// from an untrusted set_session_settings frame (#1687). Like validEffort it is a
// CLOSED ENUM and not validModel's byte-class grammar, because a posture is a
// fixed vocabulary while a model identifier is not: claude's modes are named in
// its own source, a client picks from a menu of them, and a grammar would admit
// values nobody enumerated. It carries validEffort's direction hazard too — a
// closed enum refuses inbound anything claude adds later, and widening it is a
// code edit here plus one in internal/sessions.
//
// A switch and not a package-level slice or map, matching the same decision
// internal/sessions records for its own copy: a mutable package-level collection
// holding a security vocabulary is something any code in this package, a test
// included, could append the escalation onto. Control flow cannot be appended to.
//
// The set is claude's five NON-ESCALATING modes, measured live at 2.1.239 by #2041
// and mirrored from internal/sessions' permissionModeInBand — defined relay-local
// because internal/relay cannot import internal/sessions, the same reason
// validEffort duplicates cmd/pyry's --effort enum. Five is not a claim about what
// claude accepts in band: the daemon has delivered all SIX storable postures on the
// held-open stream since #2066, and permissionModeInBand stayed at five precisely
// so its three non-routing readers keep seeing the non-escalating set. This is a
// WIRE vocabulary, and its count follows from the two omissions below rather than
// from the daemon's. Both are the ticket's decisions rather than oversights:
//
//   - "" is REFUSED, and this is where the three validators in this file
//     deliberately disagree. validModel and validEffort accept "" as "emit no
//     flag, run at claude's own default" — a real value. The default posture is a
//     NAMEABLE mode, so an explicit "" names nothing and has no reading;
//     sessions.Pool.UpdateSettings rejects it for the same reason. Do NOT "fix"
//     this by copying the siblings' first case.
//   - bypassPermissions is REFUSED, so this ticket adds no path to a privilege
//     escalation that did not exist before it. The bypass posture stays reachable
//     only through the YOLO bit, which keeps exactly one spelling for it on the
//     wire. Nothing downstream can derive it from a mode either: the stored
//     posture escalates on the YOLO bit alone, and claudeSettingsArgs composes
//     --dangerously-skip-permissions from that bit and never from the mode.
//
// The accepted value reaches ONE sink, and it is not the one validModel worries
// about: a posture is delivered to the live child as a set_permission_mode
// control request carrying a JSON string field, just as model now uses set_model.
// The free-form model still needs validModel's bounded grammar at the network
// boundary, while this closed posture vocabulary admits only five literals with no byte
// that would matter to it anyway, and no byte that could pose as a claude flag on
// a spawn argv.
func validPermissionMode(mode string) bool {
	switch mode {
	case "default", "acceptEdits", "plan", "auto", "dontAsk":
		return true
	default:
		return false
	}
}
