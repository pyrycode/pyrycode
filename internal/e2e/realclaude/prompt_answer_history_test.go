//go:build e2e_realclaude

package realclaude

import (
	"encoding/json"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// requirePromptAnswerHistory inspects raw durable facts, independently of the
// legacy history projection. Partial trailing writes are retried within a bound.
func requirePromptAnswerHistory(t *testing.T, h *perConvHarness, convID, id, decision string, knownSession bool, checks ...func(history.Entry) error) {
	t.Helper()
	session := ""
	if knownSession {
		session = streamModalBootstrapUUID
	}
	scenario.RequirePromptAnswerHistory(t, h.home, convID, id, decision, session)
	if len(checks) == 0 {
		return
	}
	entries, err := readPromptAnswerHistory(os.DirFS(filepath.Join(h.home, ".pyry", "test", "conversations", convID, "history")))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, e := range entries {
		var fact struct {
			CorrelationID string `json:"correlation_id"`
		}
		if err := json.Unmarshal(e.Payload, &fact); err != nil {
			t.Fatal(err)
		}
		if fact.CorrelationID != id {
			continue
		}
		count++
		t.Logf("correlated answer fact: %q", truncateString(string(e.Payload), questionTextLogCap))
		for _, check := range checks {
			if err := check(e); err != nil {
				t.Fatal(err)
			}
		}
	}
	if count != 1 {
		t.Fatalf("durable answer facts for exact context=%d, want one", count)
	}
}

// checkQuestionAnswerHistory compares the original surfaced batch and exact sent
// values with the durable projection, independently of the assistant's prose.
func checkQuestionAnswerHistory(e history.Entry, batch protocol.QuestionShownPayload, answers []protocol.QuestionAnswerEntry) error {
	return scenario.CheckQuestionAnswerHistory(e, batch, answers, streamModalBootstrapUUID)
}

// readPromptAnswerHistory selects regular segment leaves using the contracts of
// history's parseSegmentName and listSegments. Thread metadata and publication
// files share the directory but cannot supply durable answer facts.
func readPromptAnswerHistory(fsys fs.FS) ([]history.Entry, error) {
	return scenario.ReadPromptAnswerHistory(fsys)
}
