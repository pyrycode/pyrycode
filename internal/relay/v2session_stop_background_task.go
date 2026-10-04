package relay

import (
	"context"
	"encoding/json"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// handleStopBackgroundTask runs on appFrameWorker after Run's inert gates. The
// seam owns conversation resolution; this handler never consults membership or
// selects the cursor. Decode errors may quote remote input and are never logged.
func (m *V2SessionManager) handleStopBackgroundTask(ctx context.Context, s *V2Session, plaintext []byte) {
	var env protocol.Envelope
	if err := json.Unmarshal(plaintext, &env); err != nil {
		return // Run decoded these same bytes before enqueueing them.
	}
	var req protocol.StopBackgroundTaskPayload
	if err := json.Unmarshal(env.Payload, &req); err != nil || req.ConversationID == "" {
		return
	}
	if req.TaskID != "" {
		outcome := m.cfg.BackgroundTaskStopper.StopBackgroundTask(ctx, req.ConversationID, req.TaskID)
		if outcome != BackgroundTaskStopRefused {
			return
		}
	}
	// The requested conversation id is correlation only. Even a missing-task
	// refusal asserts nothing about conversation membership and reveals no task id.
	body, err := json.Marshal(protocol.ErrorPayload{
		Code:           protocol.CodeStopBackgroundTaskRefused,
		Message:        "background task stop refused",
		Retryable:      false,
		ConversationID: req.ConversationID,
	})
	if err != nil {
		m.cfg.Logger.Warn("relay: background task stop reply marshal failed",
			"event", "v2.stop_background_task.marshal_err", "conn_id", s.connID)
		return
	}
	if !m.forwardMCPStatusReply(ctx, s, env.ID, protocol.TypeError, body) {
		m.cfg.Logger.Debug("relay: background task stop reply dropped; session tearing down",
			"event", "v2.stop_background_task.reply_dropped", "conn_id", s.connID)
	}
}
