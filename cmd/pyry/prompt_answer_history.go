package main

import (
	"bytes"
	"encoding/json"
	"sync"
	"time"

	"github.com/pyrycode/pyrycode/internal/eventring"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/permbridge"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

const historyPromptAnswered = "prompt_answered"
const maxPromptAnswerProjection = 16 * 1024

// promptAnswerPublisher owns append/publication ordering independently of the
// bridge and registry locks. Its ring is installed before answer workers start.
type promptAnswerPublisher struct {
	mu     sync.Mutex
	ring   *eventring.Ring
	nextID uint64
}

// promptHistoryOwner is immutable after surfacing. It contains no permission
// inputs, credentials or transport envelopes and survives retirement by value.
type promptHistoryOwner struct {
	conversationID, sessionID string
	source                    history.SessionProvenance
	live                      daemonLiveSource
	tool, class               string
	questions                 []protocol.Question
}

type promptAnswerFact struct {
	ConversationID string          `json:"conversation_id"`
	CorrelationID  string          `json:"correlation_id"`
	SessionID      string          `json:"session_id,omitempty"`
	ResolvedAt     time.Time       `json:"resolved_at"`
	Source         string          `json:"source"`
	Decision       string          `json:"decision"`
	Behavior       string          `json:"behavior"`
	SessionGrant   bool            `json:"session_grant"`
	Context        json.RawMessage `json:"context"`
	Truncated      bool            `json:"truncated"`
}

type promptAnswerProjection struct {
	Tool      string                 `json:"tool,omitempty"`
	Class     string                 `json:"class,omitempty"`
	Questions []promptQuestionAnswer `json:"questions,omitempty"`
}

type promptQuestionAnswer struct {
	Index       int                 `json:"index"`
	Text        string              `json:"text"`
	MultiSelect bool                `json:"multi_select"`
	Values      []promptAnswerValue `json:"values,omitempty"`
}

type promptAnswerValue struct {
	Text    string `json:"text"`
	Meaning string `json:"meaning,omitempty"`
}

func (b *streamApprovalBridge) promptOwner(req permbridge.Request, conversationID string) promptHistoryOwner {
	owner := promptHistoryOwner{conversationID: conversationID, sessionID: req.SessionID, tool: req.ToolName}
	if req.SessionID != "" && b.sessionHarness != nil {
		kind, ok := b.sessionHarness(req.SessionID)
		if ok && (kind == "claude" || kind == "codex") {
			owner.source = history.SessionProvenance{Kind: kind, SessionID: req.SessionID}
		}
	}
	owner.live = b.live.capture(conversationID, req.SessionID, false)
	return owner
}

// recordPromptAnswer runs only for a winning parked approval, outside all bridge
// and registry locks. A failed append never retries the answer or publishes it.
func (b *streamApprovalBridge) recordPromptAnswer(id string, owner promptHistoryOwner, decision string, verdict permbridge.Verdict, answers []protocol.QuestionAnswerEntry) {
	if b.hist == nil {
		return
	}
	if b.historyPublisher != nil {
		b.historyPublisher.mu.Lock()
		defer b.historyPublisher.mu.Unlock()
	}
	projection := promptAnswerProjection{Tool: owner.tool, Class: owner.class}
	truncated := false
	for i, q := range owner.questions {
		saved := promptQuestionAnswer{Index: i, Text: q.Text, MultiSelect: q.MultiSelect}
		for _, answer := range answers {
			if answer.QuestionIndex != i {
				continue
			}
			values := answer.Values
			// Array overhead is bounded too, including arbitrarily many empty values.
			if len(values) > 128 {
				values = values[:128]
				truncated = true
			}
			for _, value := range values {
				selected := promptAnswerValue{Text: value}
				for _, option := range q.Options {
					if option.Label == value {
						selected.Meaning = option.Description
						break
					}
				}
				saved.Values = append(saved.Values, selected)
			}
		}
		projection.Questions = append(projection.Questions, saved)
	}
	raw, cut := boundPromptAnswerProjection(projection)
	ts := time.Now().UTC()
	fact := promptAnswerFact{ConversationID: owner.conversationID, CorrelationID: id, SessionID: owner.sessionID, ResolvedAt: ts, Source: "remote", Decision: decision, Behavior: verdict.Behavior, SessionGrant: verdict.ForSession || len(verdict.UpdatedPermissions) > 0, Context: raw, Truncated: truncated || cut}
	payload, err := json.Marshal(fact)
	if err != nil {
		b.logger.Warn("relay: prompt answer history marshal failed", "event", "prompt_answer.marshal_err")
		return
	}
	historyID := appendConversationHistory(b.hist, b.logger, "prompt_answer.history_append_err", owner.conversationID, historyPromptAnswered, payload, ts, owner.source)
	receipt, ok := legacyRuntimeReceipt(owner.conversationID, historyPromptAnswered, payload, historyID, ts)
	if ok && b.historyPublisher != nil {
		p := b.historyPublisher
		publishLegacyHistory(b.ctx, b.bcast, p.ring, b.logger, &p.nextID, owner.conversationID, protocol.TypeBanner, receipt, ts, historyID)
	}
}

// boundPromptAnswerProjection budgets encoded bytes, including JSON escaping.
// It changes only this private projection, never questions or child answer input.
func boundPromptAnswerProjection(p promptAnswerProjection) (json.RawMessage, bool) {
	truncated := false
	for limit := maxPromptAnswerProjection; ; limit /= 2 {
		bound := func(s string) string {
			if len(s) > limit {
				truncated = true
				return truncateForLog(s, limit)
			}
			return s
		}
		p.Tool, p.Class = bound(p.Tool), bound(p.Class)
		for i := range p.Questions {
			q := &p.Questions[i]
			q.Text = bound(q.Text)
			for j := range q.Values {
				q.Values[j].Text = bound(q.Values[j].Text)
				q.Values[j].Meaning = bound(q.Values[j].Meaning)
			}
		}
		raw, err := json.Marshal(p)
		if err == nil && len(raw) <= maxPromptAnswerProjection {
			return raw, truncated
		}
		// Parsed batches contain at most four questions; bounded value counts mean
		// even their empty-string encoding fits the cap, so the loop terminates.
		truncated = true
	}
}

// validPromptAnswerFact recognizes original producer facts, independently of
// visibility and today's session binding. Context is checked but never forwarded.
func validPromptAnswerFact(convID string, raw json.RawMessage, ts time.Time) bool {
	if !promptObjectFields(raw, "conversation_id", "correlation_id", "resolved_at", "source", "decision", "behavior", "session_grant", "context", "truncated") {
		return false
	}
	var f promptAnswerFact
	if json.Unmarshal(raw, &f) != nil || f.ConversationID != convID || f.CorrelationID == "" || f.Source != "remote" || f.ResolvedAt.IsZero() || !f.ResolvedAt.Equal(ts) {
		return false
	}
	switch f.Decision {
	case "allow", "answer":
		if f.Behavior != "allow" {
			return false
		}
	case "deny", "cancel", "refusal":
		if f.Behavior != "deny" || f.SessionGrant {
			return false
		}
	default:
		return false
	}
	if len(f.Context) > maxPromptAnswerProjection || !promptObjectFields(f.Context) {
		return false
	}
	var p promptAnswerProjection
	decoder := json.NewDecoder(bytes.NewReader(f.Context))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&p) != nil || len(p.Questions) > 4 {
		return false
	}
	var contextFields struct {
		Questions []json.RawMessage `json:"questions"`
	}
	if json.Unmarshal(f.Context, &contextFields) != nil {
		return false
	}
	for i, q := range p.Questions {
		if q.Index != i || len(q.Values) > 128 || !promptObjectFields(contextFields.Questions[i], "index", "text", "multi_select") {
			return false
		}
		var fields struct {
			Values []json.RawMessage `json:"values"`
		}
		if json.Unmarshal(contextFields.Questions[i], &fields) != nil {
			return false
		}
		for _, v := range fields.Values {
			if !promptObjectFields(v, "text") {
				return false
			}
		}
	}
	return true
}

// promptObjectFields rejects null fields as well as missing required fields;
// decoding into Go zero values alone cannot establish producer field presence.
func promptObjectFields(raw json.RawMessage, required ...string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return false
	}
	for _, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return false
		}
	}
	for _, key := range required {
		if _, ok := fields[key]; !ok {
			return false
		}
	}
	return true
}
