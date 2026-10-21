package relay

import (
	"encoding/json"
	"testing"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// nextAppReply consumes exactly one new encrypted reply so subsequent
// requests use the receive cipher's next nonce.
func nextAppReply(t *testing.T, sess *openSession, seen int) protocol.Envelope {
	t.Helper()
	waitForConnNoiseMsg(t, sess.rec, v2TestConnID, seen+1)
	msgs := noiseMsgsForConn(t, sess.rec, v2TestConnID)
	if len(msgs) <= seen {
		t.Fatalf("sealed frame count = %d, want more than %d", len(msgs), seen)
	}
	return decryptAppFrame(t, msgs[seen], sess.initRecv)
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
