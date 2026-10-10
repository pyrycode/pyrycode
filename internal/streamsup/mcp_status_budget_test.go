package streamsup

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/turnbridge"
)

func TestMCPStatusDecoderAndMapperOmissions(t *testing.T) {
	t.Parallel()
	fill := strings.Repeat("<", 256)
	servers := make([]map[string]any, maxMCPStatusServers+3)
	for i := range servers {
		servers[i] = map[string]any{
			"name": fill, "status": fill, "scope": fill,
			"error": fill + "💥tail", "serverInfo": map[string]any{"version": fill},
		}
	}
	status := requireOneMCPStatus(t, mcpStatusLineFixture(t, "success", servers))
	if len(status.Servers) != maxMCPStatusServers || status.DroppedServers != 3 {
		t.Fatalf("producer kept/drop = (%d,%d)", len(status.Servers), status.DroppedServers)
	}
	_, value, ok := turnbridge.MapEvent(status, turnbridge.TurnContext{ConversationID: strings.Repeat("<", 36)})
	if !ok {
		t.Fatal("mapping failed")
	}
	payload := value.(protocol.MCPStatusPayload)
	if len(payload.Servers) != 8 || payload.DroppedServers != 11 {
		t.Fatalf("mapped kept/drop = (%d,%d), want (8,11)", len(payload.Servers), payload.DroppedServers)
	}
	for _, row := range payload.Servers {
		if row.Error != fill || row.Name != fill || row.Status != fill || row.Scope != fill || row.Version != fill {
			t.Fatal("mapped fields differ from the producer's retained values")
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	session, err := json.Marshal(strings.Repeat("<", 256))
	if err != nil {
		t.Fatal(err)
	}
	maxID := uint64(math.MaxUint64)
	encoded, err := json.Marshal(protocol.Envelope{ID: maxID, Type: protocol.TypeMCPStatus, Payload: raw, SessionID: session, InReplyTo: &maxID, EventID: &maxID, HistoryEntryID: &maxID})
	if err != nil || len(encoded) > protocol.MaxThreadEnvelopeBytes {
		t.Fatalf("complete decoded/mapped envelope = %d bytes, error %v", len(encoded), err)
	}
}
