package relay

import (
	"context"
	"encoding/json"
	"errors"
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
	msgSettingsMalformed   = "malformed set_session_settings request"
	msgSettingsNotFound    = "unknown session_id"
	msgSettingsUnavailable = "session settings unavailable"
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
// replies. A RUNNING session picks the change up immediately, by one of two
// mechanisms the seam picks on which fields the frame carried: a model/effort-only
// change is written to the live child as a /model or /effort command (#1581), and
// every other change live-restarts the session's supervisor (#842). Both install
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
//  3. Validate model/effort BEFORE any persistence (AC #5): an invalid value
//     yields a malformed reply, not a persisted bad setting. YOLO needs no value
//     check — a malformed yolo already failed step 2's type-decode, so bypass can
//     never be inferred from a bad value (AC #4).
//  4. Nil-seam guard: a nil SettingsUpdater replies "unavailable" deterministically
//     (foreground / unwired), never a silent drop.
//  5. Persist + reply: UpdateSettings merges all present fields under one atomic
//     save (rollback on failure), so a partial write is impossible (AC #1).
//     ErrSessionUnknown ⇒ session.not_found; any other error ⇒ a server-unavailable
//     reply whose message is the fixed constant (never the wrapped err, which can
//     quote a path); nil error ⇒ the success reply.
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

	if m.cfg.SettingsUpdater == nil {
		// Unwired seam (foreground / pre-wire): report unavailable, never drop.
		m.settingsReplyError(ctx, s, env.ID, protocol.CodeServerBinaryOffline, msgSettingsUnavailable, true)
		return
	}

	// Post-validation the payload pointers pass straight through — same presence
	// contract. UpdateSettings applies all present fields atomically (one
	// saveLocked, rollback on failure), so any combination applies or nothing does.
	err := m.cfg.SettingsUpdater.UpdateSettings(p.SessionID, SettingsUpdate{
		Model:  p.Model,
		Effort: p.Effort,
		YOLO:   p.YOLO,
	})
	if errors.Is(err, ErrSessionUnknown) {
		m.settingsReplyError(ctx, s, env.ID, protocol.CodeSessionNotFound, msgSettingsNotFound, false)
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
	// claude immediately — in-band as command text for a model/effort-only change
	// (#1581), by live restart otherwise (#842). Build the reply inline, mirroring
	// handleRequestSnapshot. Echoing p.SessionID is safe — on the nil-error path it
	// matched a real session key exactly, so it is a confirmed-real, non-secret
	// routing id in a typed struct field, not an error-string interpolation.
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
	// YOLO values are NEVER logged at any level (#833 keeps them out of logs).
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
// control frame with the current run configuration (#491, #1214): the session id
// to address changes to, the model / effort / YOLO in force, and the
// context-window occupancy. It is the READ half of the #844 cluster, which
// shipped write-only. Intercepted in dispatchAppFrame before dispatch.Route,
// like handleSetSessionSettings above, and runs on the manager's single Run
// dispatch goroutine.
//
// It deliberately does NOT consult m.cfg.Snapshotter, and that is the entire
// point of the verb existing. A client used to read these values off
// screen_snapshot's side-load (#848, #857), but that reply is gated on a live
// terminal screen: on the stream-json runner Snapshotter is nil by construction
// (#1077/#1101), so handleRequestSnapshot short-circuits to
// server.binary_offline and takes the settings — which have nothing to do with a
// terminal — down with it. On the runner now in production that left the
// run-configuration UI with no values, no session id and no context figure at
// all. This handler reads only the three primitive seams, so it answers
// identically on both runners.
//
// Order mirrors the write handler, minus the steps a read cannot need:
//  1. Capability gate (the authz boundary): a non-interactive conn is fully
//     inert — no decode, no registry lookup, no seam call, NO reply, so it
//     cannot even learn whether a session or a conversation exists. Same posture
//     as the write path's AC #6, and it MUST stay ahead of both steps below.
//  2. Decode + conversation gate (#1586): the frame names the conversation the
//     client is asking about. A decode failure is TOLERATED — it leaves the id
//     empty, so there is no malformed-payload branch and a bare frame from an
//     un-updated client is never refused. NEVER echo or log the decode error;
//     encoding/json quotes attacker bytes into its error string. An empty id
//     short-circuits ahead of the registry rather than failing closed: on a
//     fresh daemon conversations.Load seeds nothing, so no conversation id
//     exists to name, and rejecting it would leave the run-configuration sheet
//     inert before the user's first message — the desktop#491 defect returning.
//     A named id the registry does not know, or any named id while the seam is
//     unwired, is NOT addressable and falls through to step 3's zero values.
//  3. No nil-seam error branch: unlike the write path, every seam here degrades
//     to its zero value, which the wire contract defines as a real answer
//     ("" ⇒ nothing to address, 0 window ⇒ usage unwired). A read that reports
//     "I have nothing" is more useful than an error, and it keeps the reply
//     shape constant so a client parses one thing. Step 2's unaddressable case
//     reuses that same shape rather than adding a failure branch to a verb
//     documented as always answering.
//
// SCOPE: the reported values are the bootstrap session's whether or not the
// request named a conversation — the id gates WHETHER the answer is populated,
// not WHICH session it describes. Making the values follow the named
// conversation is #1587, and the reported id and the reported values must move
// in one step or a client would read one session and write to another (see
// BootstrapSessionID's seam doc).
//
// The reply NEVER carries a screen byte, a transcript byte, or a file path —
// only the id, two short enum-ish strings, a bool and two aggregate integers.
// The requested conversation_id reaches neither the reply, a log line, nor an
// error string; it is a lookup key and nothing else.
func (m *V2SessionManager) handleRequestSessionSettings(ctx context.Context, s *V2Session, env protocol.Envelope) {
	if !s.interactive {
		return // non-interactive conn: inert, no reply (mirrors the write path)
	}

	var p protocol.RequestSessionSettingsPayload
	// A decode failure is tolerated: it leaves ConversationID == "", the
	// answered-as-today case. A bare frame from an un-updated client carries a
	// nil Payload, which Unmarshal rejects while leaving p zeroed — that is what
	// makes the change additive on the wire, so do NOT add an emptiness guard, an
	// error check, or a malformed-payload branch here. The error is never echoed
	// and never logged (encoding/json quotes attacker bytes into it).
	_ = json.Unmarshal(env.Payload, &p)

	// The conversation gate. Short-circuit order is load-bearing twice over: the
	// empty-id test first means an unnamed request never reaches the registry at
	// all (the fresh-daemon case, which has no id to name), and the nil-seam test
	// second means a named id cannot be validated when the registry is unwired,
	// so it fails closed exactly as handleRequestSnapshot's nil seam does.
	addressable := p.ConversationID == "" ||
		(m.cfg.KnownConversation != nil && m.cfg.KnownConversation(p.ConversationID))

	// Each seam is optional and each degrades to its zero value. Read them
	// through nil guards rather than requiring production to wire all three:
	// the foreground and pre-wire cases have none of them. An unaddressable
	// request consults NONE of them and falls through with every value at zero —
	// the same reply shape, which the wire contract already defines as a real
	// answer.
	var sessionID string
	var model, effort string
	var yolo bool
	var usedTokens, windowTokens int
	if addressable {
		if m.cfg.BootstrapSessionID != nil {
			sessionID = m.cfg.BootstrapSessionID()
		}
		if m.cfg.SnapshotSettings != nil {
			model, effort, yolo = m.cfg.SnapshotSettings()
		}
		if m.cfg.SnapshotUsage != nil {
			usedTokens, windowTokens = m.cfg.SnapshotUsage()
		}
	}

	payload, err := json.Marshal(protocol.SessionSettingsPayload{
		SessionID:    sessionID,
		Model:        model,
		Effort:       effort,
		YOLO:         yolo,
		UsedTokens:   usedTokens,
		WindowTokens: windowTokens,
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
	// Content-free debug log: conn_id only. The model / effort / YOLO values are
	// NEVER logged at any level (#833 keeps them out of logs), and neither are the
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
// untrusted set_session_settings frame (#845). It is a SHAPE check, NOT an
// allowlist: model names churn per claude release, so a fixed allowlist would
// reject a new model and force a code edit per launch. "" is accepted (clear to
// the daemon template — claudeSettingsArgs emits no --model for it); otherwise the
// value is 1..64 bytes, its first byte alphanumeric, and every byte in
// [A-Za-z0-9._-]. This is the argv-injection defense: the first-byte-alphanumeric
// rule bars a leading-dash value (--foo) from posing as a claude flag, and the
// closed charset admits no shell metachar, whitespace, or control byte (a
// multi-byte UTF-8 rune's continuation bytes are >= 0x80 and are rejected).
func validModel(m string) bool {
	if m == "" {
		return true
	}
	if len(m) > 64 {
		return false
	}
	for i := 0; i < len(m); i++ {
		c := m[i]
		alnum := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if i == 0 {
			if !alnum {
				return false
			}
			continue
		}
		if !alnum && c != '.' && c != '_' && c != '-' {
			return false
		}
	}
	return true
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
