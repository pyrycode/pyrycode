package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/permbridge"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/questionbridge"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/thread"
)

// The probe snapshots the registry under the bridge lock during delivery, then
// reacquires the registry lock. Publication cannot retain either lock at a peer.
type promptAnswerBroadcaster struct {
	conns      []relay.ActiveConn
	bridge     *streamApprovalBridge
	mu         sync.Mutex
	envs       []protocol.Envelope
	recipients []string
	dismissed  chan struct{}
}

func (p *promptAnswerBroadcaster) ActiveConns(context.Context) []relay.ActiveConn { return p.conns }
func (p *promptAnswerBroadcaster) Push(_ context.Context, id string, env protocol.Envelope) error {
	p.bridge.mu.Lock()
	registry := p.bridge.perm
	p.bridge.mu.Unlock()
	registry.Lookup("probe")
	p.mu.Lock()
	defer p.mu.Unlock()
	p.envs = append(p.envs, env)
	p.recipients = append(p.recipients, id)
	if env.Type == protocol.TypeQuestionDismissed && p.dismissed != nil {
		p.dismissed <- struct{}{}
	}
	return nil
}

func TestPromptAnswerPublicationRetired(t *testing.T) {
	for _, decision := range []string{"allow", "deny", "cancel", "answer", "refusal"} {
		for _, connected := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/connected=%v", decision, connected), func(t *testing.T) {
				store := history.New(t.TempDir())
				b, m, q, bc := testPromptBridge(t, store, discardLogger())
				req := permbridge.Request{ToolUseID: "tool", ToolName: "Bash", Input: json.RawMessage(`{}`), SessionID: "asking-session"}
				if decision == "answer" || decision == "refusal" {
					req.ToolName = questionbridge.ToolName
					req.Input = questionInput(t, "Pick", "Header", "rewrite", "meaning")
				}
				pending, id, retire := testPromptSurface(t, b, bc, req, time.Hour)
				probe := &promptAnswerBroadcaster{bridge: b, dismissed: make(chan struct{}, 1)}
				if connected {
					probe.conns = []relay.ActiveConn{{ConnID: "phone", Interactive: true}, {ConnID: "noninteractive"}}
				}
				b.bcast = probe
				b.activeConv = func() string { return testConvIDB }
				b.sessionConv = func(string) (string, bool) { return testConvIDB, true }
				switch decision {
				case "allow", "deny":
					option := "allow_once"
					if decision == "deny" {
						option = "reject_once"
					}
					if _, ok := m.ResolveAnswer(id, option, "", eligibleDevice(t)); !ok {
						t.Fatal("resolution failed")
					}
				case "cancel":
					if _, ok := m.ResolveCancel(id, eligibleDevice(t)); !ok {
						t.Fatal("cancel failed")
					}
				case "answer":
					if !q.ResolveAnswer(protocol.QuestionAnswerPayload{QuestionBatchID: id, Answers: []protocol.QuestionAnswerEntry{{QuestionIndex: 0, Values: []string{"rewrite"}}, {QuestionIndex: 1, Values: []string{"main"}}}}, eligibleDevice(t)) {
						t.Fatal("answer failed")
					}
				case "refusal":
					if !q.ResolveRefusal(protocol.QuestionRefusedPayload{QuestionBatchID: id}, eligibleDevice(t)) {
						t.Fatal("refusal failed")
					}
				}
				if connected && (decision == "answer" || decision == "refusal") {
					select {
					case <-probe.dismissed:
					case <-time.After(5 * time.Second):
						t.Fatal("question dismissal missing")
					}
				}
				verdict := pending.Await()
				wantBehavior := "deny"
				if decision == "allow" || decision == "answer" {
					wantBehavior = "allow"
				}
				if verdict.Behavior != wantBehavior {
					t.Fatal("child verdict changed")
				}
				retire()
				m.ResolveAnswer(id, "allow_once", "", eligibleDevice(t))
				q.ResolveRefusal(protocol.QuestionRefusedPayload{QuestionBatchID: id}, eligibleDevice(t))
				entries := historyEntries(t, store, testConvID)
				if len(entries) != 1 || len(historyEntries(t, store, testConvIDB)) != 0 {
					t.Fatal("winning answer duplicated or attributed to successor")
				}
				var fact promptAnswerFact
				if json.Unmarshal(entries[0].Payload, &fact) != nil || fact.Decision != decision || fact.SessionID != "asking-session" {
					t.Fatal("original answer ownership lost")
				}
				fold := thread.New(testConvID)
				if err := fold.Feed(entries); err != nil {
					t.Fatal(err)
				}
				items := fold.Items()
				if len(items) != 1 || items[0].Kind != "notice" || items[0].Session != "asking-session" || items[0].Agent != "claude" || items[0].Active {
					t.Fatal("canonical answer lost original ownership or lifecycle")
				}
				var saved promptAnswerFact
				if json.Unmarshal(items[0].Content, &saved) != nil || saved.Decision != decision || saved.CorrelationID != id {
					t.Fatal("canonical answer content lost")
				}
				probe.mu.Lock()
				defer probe.mu.Unlock()
				for _, env := range probe.envs {
					if env.Type == protocol.TypeBanner {
						t.Fatal("answer published a receipt")
					}
				}
			})
		}
	}
}

func TestPromptAnswerConcurrent(t *testing.T) {
	store := history.New(t.TempDir())
	b, m, _, bc := testPromptBridge(t, store, discardLogger())
	const count = 24
	ids := make([]string, count)
	pending := make([]*permbridge.Pending, count)
	for i := range ids {
		p, id, _ := testPromptSurface(t, b, bc, permbridge.Request{ToolUseID: fmt.Sprint(i), ToolName: "Bash", Input: json.RawMessage(`{}`)}, time.Hour)
		ids[i], pending[i] = id, p
	}
	probe := &promptAnswerBroadcaster{bridge: b, conns: []relay.ActiveConn{{ConnID: "phone", Interactive: true}}}
	b.bcast = probe
	device := eligibleDevice(t)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, id := range ids {
		for range 2 {
			wg.Add(1)
			go func(id string) { defer wg.Done(); <-start; m.ResolveAnswer(id, "reject_once", "", device) }(id)
		}
	}
	close(start)
	wg.Wait()
	for _, p := range pending {
		if p.Await().Behavior != "deny" {
			t.Fatal("verdict changed")
		}
	}
	entries := historyEntries(t, store, testConvID)
	if len(entries) != count {
		t.Fatal("concurrent answer lost or duplicated")
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		var fact promptAnswerFact
		if json.Unmarshal(entry.Payload, &fact) != nil || seen[fact.CorrelationID] {
			t.Fatal("duplicate or malformed answer")
		}
		seen[fact.CorrelationID] = true
	}
	probe.mu.Lock()
	defer probe.mu.Unlock()
	for _, env := range probe.envs {
		if env.Type == protocol.TypeBanner {
			t.Fatal("answer published a receipt")
		}
	}
}
