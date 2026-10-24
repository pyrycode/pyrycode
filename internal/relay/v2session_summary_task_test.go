package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/thread"
)

func TestSummaryTaskWire(t *testing.T) {
	for _, value := range []string{"null", `""`, `"  exact task 🐈  "`} {
		t.Run(value, func(t *testing.T) {
			provider := func() thread.CompactQueryResult {
				result := testCompactResult(catchUpConversation, 0)
				if err := json.Unmarshal([]byte(`{"TaskID":`+value+`}`), &result.Headers[0]); err != nil {
					t.Fatal(err)
				}
				return result
			}
			p := testCompactPeer(t, &testCompactCalls{}, compactCaps,
				func(protocol.ThreadCatchUpPayload) thread.CompactQueryResult { return provider() },
				func(protocol.ThreadPagePayload) thread.CompactQueryResult { return provider() })
			for _, older := range []bool{false, true} {
				if older {
					p.request(41, protocol.TypeThreadPage, fmt.Sprintf(`{"conversation_id":%q,"epoch":"epoch","before_order":91,"limit":5,"projection":"compact-v1"}`, catchUpConversation))
				} else {
					testCompactCatchUp(p, 41, catchUpConversation, 0, protocol.ThreadProjectionCompact)
				}
				typ, final, _, headers := testCompactReply(t, p, 41, protocol.ThreadProjectionCompact)
				if typ != protocol.TypeThreadItems || !final.Final || len(headers) != 1 {
					t.Fatal(typ, final, headers)
				}
				testSummaryTaskWireFact(t, headers[0], value)
			}
			// Direct-child delivery uses the same query translator but a separate
			// completion marker and does not establish compact coverage.
			child := testChildrenPeer(t, func(_ context.Context, r protocol.ThreadExpansionRequest) (thread.ChildPageResult, error) {
				out := testChildrenResult(r)
				out.Items = provider().Headers
				out.Items[0].Parent = r.ItemID
				return out, nil
			}, nil)
			child.request(42, protocol.TypeThreadReadChildren, fmt.Sprintf(`{"conversation_id":%q,"epoch":"epoch","item_id":1,"expected_rev":2,"limit":5}`, catchUpConversation))
			got, _ := testChildrenRead(t, child, 42)
			if len(got.Children) != 1 || !got.Final {
				t.Fatal(got)
			}
			testSummaryTaskWireFact(t, got.Children[0], value)
		})
	}
}

func testSummaryTaskWireFact(t *testing.T, h protocol.ThreadSummary, value string) {
	t.Helper()
	raw, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if value == "null" {
		if _, ok := fields["task_id"]; ok {
			t.Fatal("unknown association serialized", string(raw))
		}
	} else if string(fields["task_id"]) != value {
		t.Fatal("association changed", string(raw))
	}
	for _, key := range []string{"content", "task_link", "task_report", "children", "descendants"} {
		if _, ok := fields[key]; ok {
			t.Fatal("closed detail leaked", key)
		}
	}
}
