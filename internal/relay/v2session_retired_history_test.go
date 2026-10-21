package relay

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestV2Session_RetiredHistoryQuery(t *testing.T) {
	t.Parallel()
	priv, pub := genV2Keypair(t)
	frames := make(chan protocol.RoutingEnvelope, 16)
	rec := &v2Recorder{}
	logger, logs := bufferLogger()
	mgr, stop := startManager(t, V2SessionConfig{
		Frames: frames, Outbound: rec.outbound, StaticPriv: priv,
		Devices: v2PairedRegistry(t, v2TestToken), ServerID: v2TestServerID, Logger: logger,
		KnownConversation: func(string) bool { t.Error("retired query consulted history membership"); return false },
	})
	t.Cleanup(stop)
	send, recv := openModalConn(t, mgr, frames, rec, pub, "requester", []string{protocol.CapabilityInteractive})
	openModalConn(t, mgr, frames, rec, pub, "peer", []string{protocol.CapabilityInteractive})
	for i, tc := range []struct {
		raw, code, message string
		correlated         bool
	}{
		{`{"id":3715,"type":"request_history","payload":{"conversation_id":"11111111-1111-4111-8111-111111111111","cursor":"PRIVATE_CURSOR","limit":1}}`, protocol.CodeProtocolUnknownType, "unknown envelope type", true},
		{`{"id":3716,"type":"request_history","payload":"PRIVATE_PAYLOAD"}`, protocol.CodeProtocolUnknownType, "unknown envelope type", true},
		{`{"id":3717,"type":"request_history","payload":{},"payload_encrypted":true}`, protocol.CodeProtocolUnsupported, "unsupported envelope feature", true},
		{`{"id":"PRIVATE_ID","type":"request_history","payload":{}}`, protocol.CodeProtocolMalformed, "malformed envelope", false},
	} {
		sealed, err := send.Encrypt([]byte(tc.raw))
		if err != nil {
			t.Fatal(err)
		}
		frames <- wrapInnerFrame(t, "requester", protocol.TypeNoiseMsg, sealed)
		env := waitMCPStatusReply(t, rec, "requester", recv, i)
		if env.Type != protocol.TypeError || (env.InReplyTo != nil) != tc.correlated || tc.correlated && *env.InReplyTo != uint64(3715+i) {
			t.Fatalf("reply %d: %#v", i, env)
		}
		var payload map[string]any
		if err := json.Unmarshal(env.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{"code": tc.code, "message": tc.message, "retryable": false}
		if !reflect.DeepEqual(payload, want) || env.EventID != nil || env.HistoryEntryID != nil {
			t.Fatalf("reply %d payload: %s", i, env.Payload)
		}
	}
	if got := len(noiseMsgsForConn(t, rec, "peer")); got != 0 {
		t.Fatalf("peer received %d replies", got)
	}
	if got := len(noiseMsgsForConn(t, rec, "requester")); got != 4 {
		t.Fatalf("requester received %d replies, want 4", got)
	}
	if strings.Contains(logs.String(), "PRIVATE_") {
		t.Fatal("request data leaked to logs")
	}
}
