package relay

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestV2Session_MultiAgent_ReachesHandlerAndSurvivesRekey pins where the
// negotiated multi_agent decision lives (#2643): on the session, and on every
// per-frame *dispatch.Conn a handler receives, before AND after a re-key from the
// same initiator static — which, like the interactive decision, never
// re-negotiates capabilities.
func TestV2Session_MultiAgent_ReachesHandlerAndSurvivesRekey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		advertised []string
		want       bool
	}{
		{"advertised", []string{protocol.CapabilityMultiAgent}, true},
		{"not advertised", []string{protocol.CapabilityInteractive}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			respPriv, respPub := genV2Keypair(t)
			initPriv, _ := genV2Keypair(t)
			reg := v2PairedRegistry(t, v2TestToken)

			var mu sync.Mutex
			var seen []bool
			handlers := map[string]dispatch.Handler{
				protocol.TypeListConversations: func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
					mu.Lock()
					seen = append(seen, c.MultiAgent())
					mu.Unlock()
					return c.Reply(ctx, env, protocol.TypeConversations, json.RawMessage(`{}`))
				},
			}

			frames := make(chan protocol.RoutingEnvelope, 3)
			rec := &v2Recorder{}
			sess, _ := driveToOpenCaps(t, V2SessionConfig{
				Frames:     frames,
				Outbound:   rec.outbound,
				StaticPriv: respPriv,
				Devices:    reg,
				ServerID:   v2TestServerID,
				Logger:     silentLogger(),
				Handlers:   handlers,
			}, frames, rec, respPub, initPriv, v2TestToken, tc.advertised)
			t.Cleanup(sess.stop)

			list := func(id uint64) protocol.Envelope {
				return protocol.Envelope{ID: id, Type: protocol.TypeListConversations, TS: time.Now().UTC(), Payload: json.RawMessage(`{}`)}
			}
			frames <- sealAppFrame(t, sess.initSend, list(1))
			waitForEnvelopes(t, rec, 2)

			// Re-key from the SAME initiator static, then a frame under the new keys.
			initiator2, err := noise.NewInitiator(initPriv, respPub)
			if err != nil {
				t.Fatalf("NewInitiator2: %v", err)
			}
			initMsg2, err := initiator2.WriteInit(nil)
			if err != nil {
				t.Fatalf("WriteInit2: %v", err)
			}
			frames <- wrapInnerFrame(t, v2TestConnID, protocol.TypeNoiseInit, initMsg2)
			envs := waitForEnvelopes(t, rec, 3)
			_, initSend2, _, err := initiator2.ReadResp(decodeRespFrame(t, envs[2]))
			if err != nil {
				t.Fatalf("initiator2.ReadResp: %v", err)
			}
			frames <- sealAppFrame(t, initSend2, list(2))
			waitForEnvelopes(t, rec, 4)

			sess.stop()
			mu.Lock()
			defer mu.Unlock()
			if len(seen) != 2 {
				t.Fatalf("handler ran %d times, want 2", len(seen))
			}
			for i, got := range seen {
				if got != tc.want {
					t.Errorf("frame %d: c.MultiAgent() = %v, want %v", i+1, got, tc.want)
				}
			}
			if s := sess.mgr.sessions[v2TestConnID]; s == nil || s.multiAgent != tc.want {
				t.Errorf("session multiAgent after rekey = %v, want %v", s != nil && s.multiAgent, tc.want)
			}
		})
	}
}
