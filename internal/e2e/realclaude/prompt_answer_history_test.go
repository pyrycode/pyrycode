//go:build e2e_realclaude

package realclaude

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// requirePromptAnswerHistory inspects raw durable facts, independently of the
// legacy history projection. Partial trailing writes are retried within a bound.
func requirePromptAnswerHistory(t *testing.T, h *perConvHarness, convID, id, decision string, knownSession bool, checks ...func(history.Entry) error) {
	t.Helper()
	dir := filepath.Join(h.home, ".pyry", "test", "conversations", convID, "history")
	deadline := time.Now().Add(5 * time.Second)
	for {
		entries, err := readPromptAnswerHistory(os.DirFS(dir))
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, e := range entries {
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
			for _, check := range checks {
				if err := check(e); err != nil {
					t.Fatal(err)
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

// checkQuestionAnswerHistory compares the original surfaced batch and exact sent
// values with the durable projection, independently of the assistant's prose.
func checkQuestionAnswerHistory(e history.Entry, batch protocol.QuestionShownPayload, answers []protocol.QuestionAnswerEntry) error {
	var fact struct {
		ConversationID string `json:"conversation_id"`
		CorrelationID  string `json:"correlation_id"`
		SessionID      string `json:"session_id"`
		Truncated      bool   `json:"truncated"`
		Context        struct {
			Questions []struct {
				Index       int    `json:"index"`
				Text        string `json:"text"`
				MultiSelect bool   `json:"multi_select"`
				Values      []struct {
					Text string `json:"text"`
				} `json:"values"`
			} `json:"questions"`
		} `json:"context"`
	}
	if err := json.Unmarshal(e.Payload, &fact); err != nil {
		return fmt.Errorf("decode answer context: %w", err)
	}
	if fact.ConversationID != batch.ConversationID || fact.CorrelationID != batch.QuestionBatchID || fact.SessionID != streamModalBootstrapUUID || fact.Truncated || e.Session == nil || e.Session.Kind != "claude" || e.Session.SessionID != streamModalBootstrapUUID {
		return errors.New("answer context lost original conversation, batch or asking session")
	}
	if len(fact.Context.Questions) != len(batch.Questions) || len(answers) != len(batch.Questions) {
		return errors.New("answer context question count mismatch")
	}
	for i, q := range batch.Questions {
		saved, sent := fact.Context.Questions[i], answers[i]
		if saved.Index != i || saved.Text != q.Text || saved.MultiSelect != q.MultiSelect || sent.QuestionIndex != i || len(saved.Values) != len(sent.Values) {
			return fmt.Errorf("answer context question %d mismatch", i)
		}
		for j, value := range sent.Values {
			if saved.Values[j].Text != value {
				return fmt.Errorf("answer context question %d value %d mismatch", i, j)
			}
		}
	}
	return nil
}

// readPromptAnswerHistory selects regular segment leaves using the contracts of
// history's parseSegmentName and listSegments. Thread metadata and publication
// files share the directory but cannot supply durable answer facts.
func readPromptAnswerHistory(fsys fs.FS) ([]history.Entry, error) {
	files, err := fs.ReadDir(fsys, ".")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil // no history yet; the assertion retains its deadline
	}
	if err != nil {
		return nil, fmt.Errorf("read prompt answer history directory: %w", err)
	}
	var entries []history.Entry
	for _, file := range files {
		if !file.Type().IsRegular() {
			continue
		}
		digits, ok := strings.CutPrefix(file.Name(), "segment-")
		if !ok {
			continue
		}
		digits, ok = strings.CutSuffix(digits, ".jsonl")
		if !ok || len(digits) != 20 {
			continue
		}
		if _, err := strconv.ParseUint(digits, 10, 64); err != nil {
			continue
		}
		raw, err := fs.ReadFile(fsys, file.Name())
		if err != nil {
			return nil, fmt.Errorf("read prompt answer history segment %q: %w", file.Name(), err)
		}
		for _, line := range bytes.Split(raw, []byte("\n")) {
			var e history.Entry
			if json.Unmarshal(line, &e) == nil && e.Type == "prompt_answered" {
				entries = append(entries, e)
			}
		}
	}
	return entries, nil
}
