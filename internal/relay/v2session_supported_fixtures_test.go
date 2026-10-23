package relay

import (
	"encoding/json"
	"testing"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// startRetainedReconcileManager supplies live-state fixture records through the
// supported thread snapshot seam, preserving their original payloads.
func startRetainedReconcileManager(t *testing.T, cfg V2SessionConfig) (*V2SessionManager, func()) {
	t.Helper()
	cfg.KnownConversation = func(string) bool { return true }
	cfg.ThreadLiveState = func() func() (LiveState, bool) {
		var readings []LiveState
		add := func(kind string, value any, conv, key string, rev uint64) {
			raw, err := json.Marshal(value)
			if err != nil {
				t.Errorf("snapshot marshal: %v", err)
				return
			}
			if rev == 0 {
				rev = 1
			}
			readings = append(readings, LiveState{Envelope: protocol.Envelope{Type: kind, Payload: raw}, ConversationID: conv, ReadingID: key, SessionGeneration: 1, Revision: rev})
		}
		if cfg.OutstandingQuestions != nil {
			for _, p := range cfg.OutstandingQuestions() {
				add(protocol.TypeQuestionShown, p, p.ConversationID, p.QuestionBatchID, 1)
			}
		}
		if cfg.RetainedSlashCommandLists != nil {
			for _, p := range cfg.RetainedSlashCommandLists() {
				add(protocol.TypeSlashCommandList, p, p.ConversationID, "", 1)
			}
		}
		if cfg.RunningTurnPhases != nil {
			for _, p := range cfg.RunningTurnPhases() {
				add(protocol.TypeTurnState, p, p.ConversationID, "", 1)
			}
		}
		if cfg.ReplySuggestions != nil {
			for _, p := range cfg.ReplySuggestions() {
				add(protocol.TypeReplySuggestion, p, p.ConversationID, "", p.Revision)
			}
		}
		return testSnapshotIterator(readings)
	}
	return startManager(t, cfg)
}

// testSnapshotIterator supplies retained live readings through the thread route.
func testSnapshotIterator(readings []LiveState) func() (LiveState, bool) {
	i := 0
	return func() (LiveState, bool) {
		if i == len(readings) {
			return LiveState{}, false
		}
		r := readings[i]
		i++
		return r, true
	}
}

func testSnapshotCount[T any](payloads []T) int {
	conversations := map[string]bool{}
	for _, p := range payloads {
		raw, _ := json.Marshal(p)
		var id struct {
			ConversationID string `json:"conversation_id"`
		}
		_ = json.Unmarshal(raw, &id)
		conversations[id.ConversationID] = true
	}
	return 1 + len(payloads) + len(conversations)
}

func startModalReconcileManager(t *testing.T, cfg V2SessionConfig) (*V2SessionManager, func()) {
	t.Helper()
	cfg.KnownConversation = func(string) bool { return true }
	if cfg.OutstandingModals != nil {
		cfg.ThreadLiveState = func() func() (LiveState, bool) {
			var readings []LiveState
			for _, p := range cfg.OutstandingModals() {
				if p.ConversationID == "" {
					p.ConversationID = "conv-scope"
				}
				raw, err := json.Marshal(p)
				if err != nil {
					t.Fatal(err)
				}
				readings = append(readings, LiveState{Envelope: protocol.Envelope{Type: protocol.TypeModalShown, Payload: raw}, ConversationID: p.ConversationID, ReadingID: p.ModalID, SessionGeneration: 1, Revision: 1})
			}
			return testSnapshotIterator(readings)
		}
	}
	return startManager(t, cfg)
}

func startModelReconcileManager(t *testing.T, cfg V2SessionConfig) (*V2SessionManager, func()) {
	t.Helper()
	cfg.KnownConversation = func(string) bool { return true }
	if cfg.RetainedModelLists != nil {
		cfg.ThreadLiveState = func() func() (LiveState, bool) {
			var readings []LiveState
			for _, p := range cfg.RetainedModelLists(true) {
				raw, err := json.Marshal(p)
				if err != nil {
					t.Fatal(err)
				}
				readings = append(readings, LiveState{Envelope: protocol.Envelope{Type: protocol.TypeModelList, Payload: raw}, ConversationID: p.ConversationID, SessionGeneration: 1, Revision: 1, ModelListUnprojected: true})
			}
			return testSnapshotIterator(readings)
		}
	}
	return startManager(t, cfg)
}
