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
// replies. The change takes effect on the session's NEXT spawn (#833's argv path);
// making a running session pick it up is the sibling live-restart ticket #842.
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

	// Success (AC #1/#2): the change is persisted atomically and reaches claude on
	// the session's next spawn. Build the reply inline, mirroring
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
