//go:build e2e_realclaude

package dispatch

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestReadinessRequestObservation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private.jsonl")
	t.Setenv("PYRY_E2E_READINESS_RECORD", path)
	outbound := make(chan protocol.RoutingEnvelope, 1)
	conn := NewTestConn("private-device", outbound, nil)
	handlers := map[string]Handler{protocol.TypeSendMessage: func(ctx context.Context, c *Conn, e protocol.Envelope) error {
		return c.Reply(ctx, e, protocol.TypeError, json.RawMessage(`{"code":"protocol.malformed","message":"private-payload"}`))
	}}
	Route(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), conn, handlers, json.RawMessage(`{"id":4611686018427387921,"type":"send_message","payload":{"conversation_id":"private-conversation","text":"private-payload"}}`))
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("request and reply observations missing")
	}
	if strings.Contains(string(b), "private-payload") {
		t.Fatal("observer retained authored content")
	}
	if !strings.Contains(string(b), `"phase":"host_received"`) || !strings.Contains(string(b), `"phase":"outbound_queued"`) || !strings.Contains(string(b), `"reply_to":4611686018427387921`) {
		t.Fatal("correlated request boundaries missing")
	}
	if len(outbound) != 1 {
		t.Fatal("observation changed reply behavior")
	}
}
