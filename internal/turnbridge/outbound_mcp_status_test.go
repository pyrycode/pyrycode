package turnbridge

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func testMappedMCPStatus(t *testing.T, ev turnevent.MCPStatus) protocol.MCPStatusPayload {
	t.Helper()
	typ, value, ok := MapEvent(ev, TurnContext{ConversationID: strings.Repeat("<", 36)})
	if !ok || typ != protocol.TypeMCPStatus {
		t.Fatalf("MCP mapping = (%q, %v), want successful mcp_status", typ, ok)
	}
	return value.(protocol.MCPStatusPayload)
}

func testMCPEnvelopeFits(t *testing.T, p protocol.MCPStatusPayload) {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	session, err := json.Marshal(strings.Repeat("<", 256))
	if err != nil {
		t.Fatal(err)
	}
	maxID := uint64(math.MaxUint64)
	for _, sid := range []json.RawMessage{session, nil, json.RawMessage(`null`)} {
		env := protocol.Envelope{
			ID: maxID, Type: protocol.TypeMCPStatus, Payload: raw,
			TS:        time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.FixedZone("max", 23*3600+59*60)),
			InReplyTo: &maxID, EventID: &maxID, HistoryEntryID: &maxID, SessionID: sid,
		}
		encoded, err := json.Marshal(env)
		if err != nil {
			t.Fatal(err)
		}
		if sid != nil && string(sid) != "null" {
			t.Logf("complete envelope with worst metadata: %d bytes", len(encoded))
		}
		if len(encoded) > protocol.MaxThreadEnvelopeBytes {
			t.Fatalf("complete MCP envelope = %d bytes, cap %d", len(encoded), protocol.MaxThreadEnvelopeBytes)
		}
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	if len(keys["dropped_servers"]) == 0 || string(keys["dropped_servers"]) == "null" {
		t.Fatalf("dropped_servers must be a present number: %s", raw)
	}
	var wire struct {
		Servers []map[string]json.RawMessage `json:"servers"`
		Dropped int                          `json:"dropped_servers"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Servers == nil || wire.Dropped != p.DroppedServers {
		t.Fatalf("wire must carry servers array and numeric drop count: %s", raw)
	}
	for _, row := range wire.Servers {
		if len(row) != 5 {
			t.Fatalf("row keys = %v, want all five", row)
		}
		for _, key := range []string{"name", "status", "error", "scope", "version"} {
			if _, ok := row[key]; !ok {
				t.Fatalf("row missing %q", key)
			}
		}
	}
}

func TestMapEventMCPStatusEncodedBudget(t *testing.T) {
	t.Parallel()
	const budget = 63000 // The public mapping contract counts the entire servers array.
	emptyRow, err := json.Marshal(protocol.MCPServerStatus{})
	if err != nil {
		t.Fatal(err)
	}
	nameBytes := budget - 2 - len(emptyRow)
	ordinary := turnevent.MCPServerStatus{Name: "alpha", Status: "connected", Error: "", Scope: "dynamic", Version: "dev"}
	fill := strings.Repeat("<", 256)
	hostileRow := turnevent.MCPServerStatus{Name: fill, Status: fill, Error: fill, Scope: fill, Version: fill}
	hostile := make([]turnevent.MCPServerStatus, 16)
	for i := range hostile {
		hostile[i] = hostileRow
	}
	mixed := strings.Repeat("<>&\"\\\n\u2028💥é", 400)
	escaped := turnevent.MCPServerStatus{Name: mixed, Status: mixed, Error: "💥é", Scope: mixed, Version: mixed}
	// The second row fills the remainder exactly, including its separating comma.
	first, err := json.Marshal(protocol.MCPServerStatus{Name: "first"})
	if err != nil {
		t.Fatal(err)
	}
	secondBytes := budget - 2 - len(first) - 1 - len(emptyRow)
	tests := []struct {
		name    string
		servers []turnevent.MCPServerStatus
		base    int
		kept    int
		dropped int
	}{
		{"nil", nil, 0, 0, 0},
		{"empty", []turnevent.MCPServerStatus{}, 7, 0, 7},
		{"ordinary", []turnevent.MCPServerStatus{ordinary, {}}, 3, 2, 3},
		{"hostile sixteen", hostile, 4, 8, 12},
		{"escaping and multibyte", []turnevent.MCPServerStatus{escaped, ordinary}, 1, 2, 1},
		{"exact array boundary", []turnevent.MCPServerStatus{{Name: strings.Repeat("a", nameBytes)}}, math.MaxInt, 1, math.MaxInt},
		{"one byte over", []turnevent.MCPServerStatus{{Name: strings.Repeat("a", nameBytes+1)}, ordinary}, 5, 0, 7},
		{"comma exact boundary", []turnevent.MCPServerStatus{{Name: "first"}, {Name: strings.Repeat("b", secondBytes)}}, 0, 2, 0},
		{"comma one byte over stops prefix", []turnevent.MCPServerStatus{{Name: "first"}, {Name: strings.Repeat("b", secondBytes+1)}, ordinary}, 2, 1, 4},
		{"escaping exact boundary", []turnevent.MCPServerStatus{{Name: strings.Repeat("<", nameBytes/6) + strings.Repeat("a", nameBytes%6)}}, 0, 1, 0},
		{"escaping exceeds boundary", []turnevent.MCPServerStatus{{Name: strings.Repeat("<", nameBytes/6+1)}, ordinary}, 0, 0, 2},
		{"exact max drop sum", []turnevent.MCPServerStatus{{Name: strings.Repeat("x", budget)}, ordinary}, math.MaxInt - 2, 0, math.MaxInt},
		{"overflow drop sum", []turnevent.MCPServerStatus{{Name: strings.Repeat("x", budget)}, ordinary}, math.MaxInt - 1, 0, math.MaxInt},
		{"already saturated", hostile, math.MaxInt, 8, math.MaxInt},
	}
	for _, field := range []string{"Name", "Status", "Error", "Scope", "Version"} {
		row := turnevent.MCPServerStatus{}
		reflect.ValueOf(&row).Elem().FieldByName(field).SetString(strings.Repeat("💥", budget/4+1))
		tests = append(tests, struct {
			name                string
			servers             []turnevent.MCPServerStatus
			base, kept, dropped int
		}{"oversized " + field, []turnevent.MCPServerStatus{row, ordinary}, 9, 0, 11})
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev := turnevent.MCPStatus{Servers: tc.servers, DroppedServers: tc.base}
			before, err := json.Marshal(ev)
			if err != nil {
				t.Fatal(err)
			}
			got := testMappedMCPStatus(t, ev)
			if len(got.Servers) != tc.kept || got.DroppedServers != tc.dropped {
				t.Fatalf("kept/drop = (%d,%d), want (%d,%d)", len(got.Servers), got.DroppedServers, tc.kept, tc.dropped)
			}
			for i, row := range got.Servers {
				source := ev.Servers[i]
				want := protocol.MCPServerStatus{Name: source.Name, Status: source.Status, Error: source.Error, Scope: source.Scope, Version: source.Version}
				if row != want {
					t.Fatalf("retained row %d changed", i)
				}
			}
			testMCPEnvelopeFits(t, got)
			array := got.Servers
			if array == nil {
				array = []protocol.MCPServerStatus{}
			}
			encoded, err := json.Marshal(array)
			if err != nil {
				t.Fatal(err)
			}
			if len(encoded) > budget {
				t.Fatalf("servers array = %d bytes, budget %d", len(encoded), budget)
			}
			if strings.Contains(tc.name, "exact boundary") || tc.name == "exact array boundary" {
				if len(encoded) != budget {
					t.Fatalf("boundary fixture = %d, want %d", len(encoded), budget)
				}
			}
			after, err := json.Marshal(ev)
			if err != nil || string(after) != string(before) {
				t.Fatal("mapping changed the event")
			}
			if len(got.Servers) > 0 {
				got.Servers[0].Name = "changed payload"
				if ev.Servers[0].Name == "changed payload" {
					t.Fatal("retained rows alias event storage")
				}
			}
		})
	}
}
