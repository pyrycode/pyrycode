package scenario

import (
	"encoding/json"
	"testing"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// Transitions reads retained session facts without manufacturing app frames.
func (snapshot SourceFacts) Transitions(t *testing.T, conv string) []protocol.SessionTransitionPayload {
	t.Helper()
	var result []protocol.SessionTransitionPayload
	for _, entry := range snapshot(conv) {
		if entry.Type != protocol.TypeSessionTransition {
			continue
		}
		var p protocol.SessionTransitionPayload
		if json.Unmarshal(entry.Payload, &p) != nil || p.ConversationID != conv {
			t.Fatal("invalid retained transition ownership")
		}
		result = append(result, p)
	}
	return result
}
