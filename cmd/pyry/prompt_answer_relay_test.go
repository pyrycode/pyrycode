package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/permbridge"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/questionbridge"
	"github.com/pyrycode/pyrycode/internal/relay"
)

func TestPromptAnswerRelayRemainsResponsive(t *testing.T) {
	for _, decision := range []string{"allow", "deny", "cancel", "answer", "refusal"} {
		t.Run(decision, func(t *testing.T) {
			b, m, q, bc := testPromptBridge(t, history.New(t.TempDir()), discardLogger())
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			b.ctx = ctx
			bindings := testControlBindings(t, testConvID, "asking-session")
			b.live = bindings
			req := permbridge.Request{ToolUseID: "tool", ToolName: "Bash", Input: json.RawMessage(`{}`), SessionID: "asking-session"}
			if decision == "answer" || decision == "refusal" {
				req.ToolName = questionbridge.ToolName
				req.Input = questionInput(t, "Pick", "Header", "rewrite", "meaning")
			}
			pending, id, _ := testPromptSurface(t, b, bc, req, time.Minute)
			w, mgr := testDaemonRelayWire(t, bindings, relayWiring{}, nil, func(cfg *relay.V2SessionConfig) { cfg.ModalResolver = m; cfg.QuestionResolver = q })
			b.bcast = mgr
			w.open("phone", false)
			typ := protocol.TypeModalAnswer
			var payload any = protocol.ModalAnswerPayload{ModalID: id, OptionID: "allow_once"}
			switch decision {
			case "deny":
				payload = protocol.ModalAnswerPayload{ModalID: id, OptionID: "reject_once"}
			case "cancel":
				typ = protocol.TypeModalCancel
				payload = protocol.ModalCancelPayload{ModalID: id}
			case "answer":
				typ = protocol.TypeQuestionAnswer
				payload = protocol.QuestionAnswerPayload{QuestionBatchID: id, Answers: []protocol.QuestionAnswerEntry{{QuestionIndex: 0, Values: []string{"rewrite"}}, {QuestionIndex: 1, Values: []string{"main"}}}}
			case "refusal":
				typ = protocol.TypeQuestionRefused
				payload = protocol.QuestionRefusedPayload{QuestionBatchID: id}
			}
			testDaemonWireRequest(t, w, 10, typ, payload)
			verdict := make(chan string, 1)
			go func() { verdict <- pending.Await().Behavior }()
			select {
			case <-verdict:
			case <-time.After(time.Second):
				t.Fatal("answer did not reach child")
			}
			probe, stop := context.WithTimeout(ctx, time.Second)
			defer stop()
			if got := mgr.ActiveConns(probe); len(got) != 1 {
				t.Fatal("answer blocked relay dispatch")
			}
			seen := map[string]bool{}
			for !(seen[protocol.TypeModalDismissed] || seen[protocol.TypeQuestionDismissed]) {
				_, e := w.read()
				if e.Type == protocol.TypeBanner {
					t.Fatal("retired receipt received")
				}
				seen[e.Type] = true
			}
			w.open("again", false)
		})
	}
}

// Delivery is held at the real manager query boundary while later answers arrive.
// No lock needed by answer recording may remain held across that query.
type heldPromptConnections struct {
	*promptAnswerBroadcaster
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *heldPromptConnections) ActiveConns(ctx context.Context) []relay.ActiveConn {
	p.once.Do(func() { close(p.entered) })
	select {
	case <-p.release:
		return p.conns
	case <-ctx.Done():
		return nil
	}
}

func TestPromptAnswerBlockedDeliveryDoesNotBlockNextAnswer(t *testing.T) {
	for _, mode := range []string{"healthy", "cancelled", "failed", "failed-cancelled"} {
		t.Run(mode, func(t *testing.T) {
			cancelled, failed := strings.Contains(mode, "cancelled"), strings.HasPrefix(mode, "failed")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			root := t.TempDir()
			if failed {
				root += "/occupied"
				if err := os.WriteFile(root, nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			logger, logs := auditLogger()
			b, m, _, bc := testPromptBridge(t, history.New(root), logger)
			b.ctx = ctx
			ids := make([]string, 2)
			pending := make([]*permbridge.Pending, 2)
			for i := range ids {
				pending[i], ids[i], _ = testPromptSurface(t, b, bc, permbridge.Request{ToolUseID: fmt.Sprint(i), ToolName: "Bash", Input: json.RawMessage(`{}`)}, time.Minute)
			}
			probe := &heldPromptConnections{promptAnswerBroadcaster: &promptAnswerBroadcaster{bridge: b, conns: []relay.ActiveConn{{ConnID: "phone", Interactive: true}}}, entered: make(chan struct{}), release: make(chan struct{})}
			b.bcast = probe
			// Hold an unrelated connection query while answer recording proceeds.
			queryDone := make(chan struct{})
			go func() { defer close(queryDone); probe.ActiveConns(ctx) }()
			t.Cleanup(func() { cancel(); <-queryDone })
			<-probe.entered
			device := eligibleDevice(t)
			resolved := make(chan bool, 1)
			go func() { _, ok := m.ResolveAnswer(ids[0], "allow_once", "", device); resolved <- ok }()
			select {
			case <-probe.entered:
			case <-time.After(time.Second):
				t.Fatal("delivery never started")
			}
			select {
			case ok := <-resolved:
				if !ok {
					t.Fatal("first answer rejected")
				}
			case <-time.After(time.Second):
				t.Fatal("delivery blocked first answer")
			}
			if cancelled {
				cancel()
			}
			go func() { _, ok := m.ResolveAnswer(ids[1], "reject_once", "", device); resolved <- ok }()
			select {
			case ok := <-resolved:
				if !ok {
					t.Fatal("second answer rejected")
				}
			case <-time.After(time.Second):
				t.Fatal("delivery blocked next answer")
			}
			if !failed && len(historyEntries(t, b.hist, testConvID)) != 2 {
				t.Fatal("answers not retained during stalled discovery")
			}
			if failed && strings.Count(logs.String(), "prompt_answer.history_append_err") != 2 {
				t.Fatal("failed answers did not attempt each append once")
			}
			if pending[0].Await().Behavior != "allow" || pending[1].Await().Behavior != "deny" {
				t.Fatal("winning child verdict changed")
			}
			if cancelled {
				cancel()
			} else {
				close(probe.release)
			}
			<-queryDone
			probe.mu.Lock()
			defer probe.mu.Unlock()
			for _, env := range probe.envs {
				if env.Type == protocol.TypeBanner {
					t.Fatal("retired receipt delivered")
				}
			}
		})
	}
}
