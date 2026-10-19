//go:build e2e_realclaude

package realclaude

import (
	"bytes"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func hookBannerIsRefusal(p protocol.BannerPayload, convID string) bool {
	return p.ConversationID == convID && bytes.Contains([]byte(p.Text), []byte(oslcapBlockReason))
}
func hookBannerCheckWindow(convID string, liveReqID uint64, entries []history.Entry, frames []protocol.Envelope) error {
	return scenario.CheckHookBannerWindow(convID, liveReqID, entries, frames)
}
