package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/permbridge"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/relay/handlers"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// The queue accepts the synthetic send; delivery of that turn is outside this
// transport test. The real handler must still produce its authenticated ack.
type testCompactSend struct{}

func (testCompactSend) Route(string) (handlers.TurnWriter, error) { return nil, nil }
func (testCompactSend) EnqueueSent(string, string, string, string, []string, string, string, time.Time) uint64 {
	return 1
}

func testCompactBurstWire(t *testing.T, s *threadShadow) (*switchWire, *relay.V2SessionManager, func()) {
	t.Helper()
	b := testControlBindings(t, testConvID, "a")
	s.registry.Update(testConvID, func(c *conversations.Conversation) { c.CurrentSessionID = "a" })
	b.reg = s.registry
	bridge, modal, questions, bc := testPromptBridge(t, nil, discardLogger())
	bridge.live = b
	w, mgr := testDaemonRelayWire(t, b, relayWiring{}, nil, func(c *relay.V2SessionConfig) {
		installDaemonThreadRelay(c, s)
		c.ModalResolver, c.QuestionResolver = modal, questions
		c.Handlers = map[string]dispatch.Handler{protocol.TypeSendMessage: handlers.SendMessage(testCompactSend{}, testCompactSend{}, nil, nil, "", nil, nil, discardLogger())}
		send := c.Outbound
		c.OutboundContext = func(ctx context.Context, e protocol.RoutingEnvelope) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			return send(e)
		}
	}, func(ctx context.Context, m *relay.V2SessionManager) func() {
		stopThread := startDaemonThreadPublisher(ctx, s, m)
		stopLive := startDaemonLivePublisher(ctx, b.sink.live, m.ActiveConns, m.PushLiveState, func(ctx context.Context, peer string) { _ = m.RetireLiveState(ctx, peer) }, discardLogger())
		return func() { stopThread(); stopLive() }
	})
	notify := func() {
		t.Helper()
		started := time.Now()
		// Establish the running producer before prompts capture its generation.
		b.sink.sinkForSessionTag(newStreamSessionTag("a"), "claude")(turnevent.TextChunk{Text: "main continues"})
		for _, question := range []bool{false, true} {
			tool, input := "Bash", json.RawMessage(`{"command":"true"}`)
			if question {
				tool, input = "AskUserQuestion", json.RawMessage(`{"questions":[{"question":"Choose during burst?","header":"Choice","options":[{"label":"A","description":"A"},{"label":"B","description":"B"}],"multiSelect":false}]}`)
			}
			_, id, retire := testPromptSurface(t, bridge, bc, permbridge.Request{SessionID: "a", ToolName: tool, ToolUseID: tool, Input: input, RequiresUserInteraction: question}, time.Minute)
			t.Cleanup(retire)
			family := protocol.TypeModalShown
			if question {
				family = protocol.TypeQuestionShown
			}
			reading, ok := testLiveReadings(t, b.sink.live, testConvID)[liveReadingKey{family, id}]
			if !ok || reading.Answerable == nil || !*reading.Answerable {
				t.Fatalf("prompt producer failed to retain answerable %s", family)
			}
		}
		testDaemonThreadRequest(t, w, "phone", 99, protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: testConvID, MessageID: "burst-send", Text: "continue"})
		seen := map[string]bool{}
		for len(seen) < 4 {
			if time.Since(started) > 3*time.Second {
				t.Fatalf("live channels stalled behind closed parents: %v", seen)
			}
			frames := testCompactRead(t, w, mgr, "phone", "legacy")
			if time.Since(started) > 3*time.Second {
				t.Fatalf("live channels stalled behind closed parents: %v", seen)
			}
			for _, f := range frames {
				if f.peer != "phone" || f.env.SessionStateCleared {
					continue
				}
				switch f.env.Type {
				case protocol.TypeQuestionShown, protocol.TypeModalShown, protocol.TypeTurnState:
					var scope struct {
						Conversation string `json:"conversation_id"`
					}
					if json.Unmarshal(f.env.Payload, &scope) != nil || scope.Conversation != testConvID {
						t.Fatalf("live state lost conversation: %s", f.env.Payload)
					}
					seen[f.env.Type] = true
				case protocol.TypeAck:
					if f.env.InReplyTo == nil || *f.env.InReplyTo != 99 {
						t.Fatal("send acknowledgement lost correlation")
					}
					seen[f.env.Type] = true
				case protocol.TypeThreadCompactUpdate:
					t.Fatal("publication crossed the burst barrier")
				}
			}
		}
		t.Logf("question, approval, send ack and main-turn state delivered during held burst in %v", time.Since(started))
	}
	return w, mgr, notify
}

func TestDaemonThreadCompactLiveChannels(t *testing.T) {
	s, _, _ := testDaemonExpansionFixture(t, 3, 1, 64)
	w, _, notify := testCompactBurstWire(t, s)
	w.open("phone", false, protocol.CapabilityThread, protocol.CapabilityThreadExpansion, protocol.CapabilityThreadCompact)
	w.open("legacy", false, protocol.CapabilityThread)
	notify()
}
