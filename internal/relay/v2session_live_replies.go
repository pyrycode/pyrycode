package relay

import (
	"context"
	"encoding/json"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// pushLiveReply adds correlation without changing supplied source evidence. The
// bounded queue returns all sealing and ordering decisions to Run.
func (m *V2SessionManager) pushLiveReply(ctx context.Context, s *V2Session, requestID uint64, reading LiveState) {
	reading.Envelope.InReplyTo = &requestID
	if err := m.PushLiveState(ctx, s.connID, reading); err != nil {
		m.cfg.Logger.Debug("relay: supplied live reply dropped", "event", "v2.live_reply.dropped", "conn_id", s.connID)
		return
	}
	m.cfg.Logger.Debug("relay: supplied live reply queued", "event", "v2.live_reply.queued", "conn_id", s.connID)
}

func (m *V2SessionManager) pushSettingsReading(ctx context.Context, s *V2Session, requestID uint64, reading LiveState, multiAgent bool) {
	// Explicit clears already have their complete empty payload and need no queries.
	if reading.Envelope.SessionStateCleared {
		m.pushLiveReply(ctx, s, requestID, reading)
		return
	}
	var base protocol.SessionSettingsPayload
	if err := json.Unmarshal(reading.Envelope.Payload, &base); err != nil {
		m.settingsReadReplyError(ctx, s, requestID)
		return
	}
	// Only optional enrichment fields change; unrelated encoded fields survive.
	var marshalErr error
	set := func(field string, value any) {
		raw, err := json.Marshal(value)
		if err != nil {
			marshalErr = err
		} else {
			reading.Envelope.Payload = replaceObjectField(reading.Envelope.Payload, field, raw)
		}
	}
	if m.cfg.EffectiveEffortFor != nil {
		if effort, ok := m.cfg.EffectiveEffortFor(ctx, reading.ConversationID); ok {
			set("effective_effort", effort)
		}
	}
	if multiAgent && m.cfg.CapabilitiesFor != nil {
		if agent, ok := m.cfg.CapabilitiesFor(base.SessionID, base.Model); ok {
			set("capabilities", sessionCapabilities(agent))
		}
	}
	if m.cfg.MemorySearchFor != nil {
		report, err := m.cfg.MemorySearchFor(ctx, reading.ConversationID, base.SessionID)
		if err != nil {
			report = protocol.MemorySearchReport{Availability: "unknown", Providers: []protocol.MemorySearchProvider{}}
		}
		set("memory_search", report)
	}
	if marshalErr != nil {
		m.settingsReadReplyError(ctx, s, requestID)
		return
	}
	m.pushLiveReply(ctx, s, requestID, reading)
}
