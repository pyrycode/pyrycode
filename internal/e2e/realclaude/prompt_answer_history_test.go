//go:build e2e_realclaude

package realclaude

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/history"
)

// requirePromptAnswerHistory inspects raw durable facts, independently of the
// legacy history projection. Partial trailing writes are retried within a bound.
func requirePromptAnswerHistory(t *testing.T, h *perConvHarness, convID, id, decision string, knownSession bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		paths, err := filepath.Glob(filepath.Join(h.home, ".pyry", "test", "conversations", convID, "history", "*"))
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, path := range paths {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range bytes.Split(raw, []byte("\n")) {
				var e history.Entry
				if json.Unmarshal(line, &e) != nil || e.Type != "prompt_answered" {
					continue
				}
				var fact struct {
					ConversationID string          `json:"conversation_id"`
					CorrelationID  string          `json:"correlation_id"`
					Decision       string          `json:"decision"`
					Source         string          `json:"source"`
					ResolvedAt     time.Time       `json:"resolved_at"`
					Context        json.RawMessage `json:"context"`
				}
				if err := json.Unmarshal(e.Payload, &fact); err != nil {
					t.Fatal(err)
				}
				if fact.CorrelationID != id {
					continue
				}
				count++
				if fact.ConversationID != convID || fact.Decision != decision || fact.Source != "remote" || !fact.ResolvedAt.Equal(e.TS) || fact.ResolvedAt.IsZero() || e.Shown == nil || !*e.Shown || len(fact.Context) == 0 || len(fact.Context) > 16*1024 {
					t.Fatal("resolved prompt fact has incorrect ownership/decision/visibility")
				}
				if knownSession && (e.Session == nil || e.Session.Kind != "claude" || e.Session.SessionID != streamModalBootstrapUUID) {
					t.Fatal("resolved prompt fact lost asking session")
				}
			}
		}
		if count == 1 {
			return
		}
		if count > 1 || time.Now().After(deadline) {
			t.Fatalf("durable answer facts for resolved prompt=%d, want one", count)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
