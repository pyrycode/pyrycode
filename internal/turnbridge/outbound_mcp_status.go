package turnbridge

import (
	"encoding/json"
	"math"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// maxMCPStatusListBytes bounds the encoded servers array, including brackets,
// separators and JSON escaping. The remaining 2519 envelope bytes reserve a
// 36-byte conversation ID and 256-byte producing-session ID under worst escaping,
// timestamp, maximum identity counters and dropped count. Full-envelope boundary
// tests pin that reserve against protocol.MaxThreadEnvelopeBytes.
const maxMCPStatusListBytes = 63000

// mapMCPStatus retains a whole-row prefix without changing any retained field or
// mutating the event. MCPStatusPayload.MarshalJSON owns nil-to-empty normalization.
func mapMCPStatus(e turnevent.MCPStatus, conversationID string) protocol.MCPStatusPayload {
	var servers []protocol.MCPServerStatus
	size := 2 // array brackets, including for an empty prefix
	for _, server := range e.Servers {
		row := protocol.MCPServerStatus{
			Name: server.Name, Status: server.Status, Error: server.Error,
			Scope: server.Scope, Version: server.Version,
		}
		encoded, err := json.Marshal(row)
		if err != nil {
			break
		}
		cost := len(encoded)
		if len(servers) > 0 {
			cost++ // separator between retained rows
		}
		if cost > maxMCPStatusListBytes-size {
			break
		}
		size += cost
		servers = append(servers, row)
	}
	omitted := len(e.Servers) - len(servers)
	dropped := math.MaxInt
	if e.DroppedServers <= math.MaxInt-omitted {
		dropped = e.DroppedServers + omitted
	}
	return protocol.MCPStatusPayload{
		ConversationID: conversationID, Servers: servers, DroppedServers: dropped,
	}
}
