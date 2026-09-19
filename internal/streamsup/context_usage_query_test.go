package streamsup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// The await-shaped context-usage query (#2430). The suite pins the two lanes
// AGAINST EACH OTHER rather than in isolation: a claimed reading must reach its
// caller and no sink, while the automatic post-turn reading must still reach the
// sink, and both assertions live here so a change that merges the lanes cannot
// leave one file green.

type contextUsageQueryOutcome struct {
	usage turnevent.ContextUsage
	ok    bool
}

func contextUsageQueryRunner(t *testing.T, p *Parser, w *mcpStatusQueryWriteCloser) *Runner {
	t.Helper()
	// eligible=false deliberately: mcpStatusEligible is MCP provenance and carries no
	// authority over whether a context reading may be asked for. A suite that passed
	// true here would still pass if the gate were wrongly copied across.
	return mcpStatusQueryRunner(t, p, w, false)
}

func queryContextUsageAsync(ctx context.Context, r *Runner, detail string) <-chan contextUsageQueryOutcome {
	done := make(chan contextUsageQueryOutcome, 1)
	go func() {
		usage, ok := r.QueryContextUsage(ctx, detail)
		done <- contextUsageQueryOutcome{usage: usage, ok: ok}
	}()
	return done
}

func awaitContextUsageRequest(t *testing.T, w *mcpStatusQueryWriteCloser) controlRequest {
	t.Helper()
	select {
	case raw := <-w.wrote:
		var req controlRequest
		if err := json.Unmarshal(bytes.TrimSpace(raw), &req); err != nil {
			t.Fatalf("decode context usage request: %v", err)
		}
		if req.Request.Subtype != "get_context_usage" {
			t.Fatalf("request subtype = %q, want get_context_usage", req.Request.Subtype)
		}
		return req
	case <-time.After(3 * time.Second):
		t.Fatal("context usage request was not written")
		return controlRequest{}
	}
}

func awaitContextUsageOutcome(t *testing.T, done <-chan contextUsageQueryOutcome) contextUsageQueryOutcome {
	t.Helper()
	select {
	case got := <-done:
		return got
	case <-time.After(3 * time.Second):
		t.Fatal("context usage query did not complete")
		return contextUsageQueryOutcome{}
	}
}

// contextUsageQueryResponse builds one control_response carrying a reading tagged
// with tag, sized to exceed every list bound so the shared decoder's ranking and
// drop counting are observable through whichever lane returned the value.
func contextUsageQueryResponse(t *testing.T, requestID, subtype, tag string) []byte {
	t.Helper()
	categories := make([]map[string]any, maxContextUsageEntries+2)
	for i := range categories {
		categories[i] = map[string]any{
			"name":   tag + "-category-" + string(rune('a'+i%26)),
			"tokens": len(categories) - i,
		}
	}
	response := map[string]any{"subtype": subtype, "request_id": requestID}
	if subtype == controlResponseSuccess {
		response["response"] = map[string]any{
			"model":       tag + "-model",
			"totalTokens": 1234,
			"maxTokens":   200000,
			"percentage":  7,
			"categories":  categories,
			"mcpTools": []map[string]any{
				{"name": tag + "-tool", "serverName": tag + "-server", "tokens": 11},
			},
			"memoryFiles": []map[string]any{
				{"path": tag + "-path", "type": "project", "tokens": 22},
			},
		}
	}
	raw, err := json.Marshal(map[string]any{"type": "control_response", "response": response})
	if err != nil {
		t.Fatalf("marshal context usage response: %v", err)
	}
	return append(raw, '\n')
}

func assertNoContextUsageSinkEvent(t *testing.T, sink <-chan turnevent.Event) {
	t.Helper()
	select {
	case ev := <-sink:
		t.Fatalf("claimed context reading reached shared sink as %T: %+v", ev, ev)
	default:
	}
}

func TestRunner_QueryContextUsage_AllowedDetailReachesTheCaller(t *testing.T) {
	for _, detail := range []string{"summary", "full"} {
		t.Run(detail, func(t *testing.T) {
			sink := make(chan turnevent.Event, 4)
			p := NewParser(func(ev turnevent.Event) { sink <- ev }, discardLogger())
			w := newMCPStatusQueryWriter()
			r := contextUsageQueryRunner(t, p, w)

			done := queryContextUsageAsync(context.Background(), r, detail)
			req := awaitContextUsageRequest(t, w)
			if req.Request.Detail != detail {
				t.Fatalf("request detail = %q, want %q", req.Request.Detail, detail)
			}
			if !strings.HasPrefix(req.RequestID, contextUsageQueryIDPrefix) {
				t.Fatalf("request_id = %q, want prefix %q", req.RequestID, contextUsageQueryIDPrefix)
			}
			if _, err := p.Write(contextUsageQueryResponse(t, req.RequestID, controlResponseSuccess, detail)); err != nil {
				t.Fatalf("Parser.Write: %v", err)
			}

			got := awaitContextUsageOutcome(t, done)
			if !got.ok {
				t.Fatal("exact successful response reported unavailable")
			}
			if got.usage.Model != detail+"-model" || got.usage.TotalTokens != 1234 ||
				got.usage.MaxTokens != 200000 || got.usage.Percentage != 7 {
				t.Fatalf("reading header = %+v, want the %q response", got.usage, detail)
			}
			// The shared decoder's bounds must apply to the claimed lane too: a second
			// decoder written for this path is exactly the defect this asserts against.
			if len(got.usage.Categories) != maxContextUsageEntries || got.usage.DroppedCategories != 2 {
				t.Fatalf("categories size/drop = (%d,%d), want (%d,2)",
					len(got.usage.Categories), got.usage.DroppedCategories, maxContextUsageEntries)
			}
			if got.usage.Categories[0].Tokens != maxContextUsageEntries+2 {
				t.Fatalf("heaviest category = %+v, want the top-ranked entry", got.usage.Categories[0])
			}
			if len(got.usage.MCPTools) != 1 || got.usage.MCPTools[0].ServerName != detail+"-server" {
				t.Fatalf("mcp tools = %+v", got.usage.MCPTools)
			}
			if len(got.usage.MemoryFiles) != 1 || got.usage.MemoryFiles[0].Path != detail+"-path" {
				t.Fatalf("memory files = %+v", got.usage.MemoryFiles)
			}
			assertNoContextUsageSinkEvent(t, sink)
		})
	}
}

// A claimed reading reaches no shared consumer AT ALL, which is what pins the claim's
// position above emitContextUsage in the control_response arm rather than merely beside
// it. The sink is not the whole test: emitModelList owns the control-response record and
// runs below the claims, so a claim placed under it would leave a record for a reply the
// daemon promised to keep private.
func TestRunner_QueryContextUsage_ClaimedReadingLeavesNoControlResponseRecord(t *testing.T) {
	var logs bytes.Buffer
	sink := make(chan turnevent.Event, 4)
	p := NewParser(
		func(ev turnevent.Event) { sink <- ev },
		slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	)
	w := newMCPStatusQueryWriter()
	r := contextUsageQueryRunner(t, p, w)

	done := queryContextUsageAsync(context.Background(), r, "full")
	req := awaitContextUsageRequest(t, w)
	_, _ = p.Write(contextUsageQueryResponse(t, req.RequestID, controlResponseSuccess, "private"))
	if got := awaitContextUsageOutcome(t, done); !got.ok {
		t.Fatalf("claimed reading was not returned: %+v", got)
	}

	assertNoContextUsageSinkEvent(t, sink)
	if strings.Contains(logs.String(), controlResponseMsg) {
		t.Fatalf("claimed reading produced a control-response record: %s", logs.String())
	}
	if strings.Contains(logs.String(), "private") {
		t.Fatalf("claimed reading's payload reached a record: %s", logs.String())
	}
}

func TestRunner_QueryContextUsage_OverlappingQueriesCorrelateIndependently(t *testing.T) {
	sink := make(chan turnevent.Event, 4)
	p := NewParser(func(ev turnevent.Event) { sink <- ev }, discardLogger())
	w := newMCPStatusQueryWriter()
	r := contextUsageQueryRunner(t, p, w)

	firstDone := queryContextUsageAsync(context.Background(), r, "summary")
	firstReq := awaitContextUsageRequest(t, w)
	secondDone := queryContextUsageAsync(context.Background(), r, "full")
	secondReq := awaitContextUsageRequest(t, w)
	if firstReq.RequestID == secondReq.RequestID {
		t.Fatalf("overlapping queries reused request id %q", firstReq.RequestID)
	}

	_, _ = p.Write(contextUsageQueryResponse(t, secondReq.RequestID, controlResponseSuccess, "second"))
	second := awaitContextUsageOutcome(t, secondDone)
	if !second.ok || second.usage.Model != "second-model" {
		t.Fatalf("second query result = %+v, want the second response", second)
	}
	select {
	case got := <-firstDone:
		t.Fatalf("first query completed from the second id: %+v", got)
	default:
	}

	_, _ = p.Write(contextUsageQueryResponse(t, firstReq.RequestID, controlResponseSuccess, "first"))
	first := awaitContextUsageOutcome(t, firstDone)
	if !first.ok || first.usage.Model != "first-model" {
		t.Fatalf("first query result = %+v, want the first response", first)
	}
	assertNoContextUsageSinkEvent(t, sink)
}

// The automatic post-turn lane is unchanged: RequestContextUsage's bare-sequence id
// is not in the query namespace, so its reply falls past the claim to emitContextUsage
// and publishes, while a query in flight stays unanswered by it.
func TestRunner_QueryContextUsage_AutomaticReadingStillReachesTheSink(t *testing.T) {
	sink := make(chan turnevent.Event, 4)
	p := NewParser(func(ev turnevent.Event) { sink <- ev }, discardLogger())
	w := newMCPStatusQueryWriter()
	r := contextUsageQueryRunner(t, p, w)

	queryDone := queryContextUsageAsync(context.Background(), r, "full")
	queryReq := awaitContextUsageRequest(t, w)

	if err := r.RequestContextUsage("summary"); err != nil {
		t.Fatalf("RequestContextUsage: %v", err)
	}
	automaticReq := awaitContextUsageRequest(t, w)
	if strings.HasPrefix(automaticReq.RequestID, contextUsageQueryIDPrefix) {
		t.Fatalf("automatic request_id = %q, want a bare sequence id", automaticReq.RequestID)
	}

	_, _ = p.Write(contextUsageQueryResponse(t, automaticReq.RequestID, controlResponseSuccess, "automatic"))
	select {
	case ev := <-sink:
		usage, ok := ev.(turnevent.ContextUsage)
		if !ok || usage.Model != "automatic-model" {
			t.Fatalf("automatic response emitted %T: %+v", ev, ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("automatic reading no longer reaches the shared sink")
	}
	select {
	case got := <-queryDone:
		t.Fatalf("automatic reading completed a waiting query: %+v", got)
	default:
	}

	_, _ = p.Write(contextUsageQueryResponse(t, queryReq.RequestID, controlResponseSuccess, "query"))
	got := awaitContextUsageOutcome(t, queryDone)
	if !got.ok || got.usage.Model != "query-model" {
		t.Fatalf("query result = %+v, want the query response", got)
	}
	assertNoContextUsageSinkEvent(t, sink)
}

// The admission gate is unchanged in both directions: a reply whose id the daemon
// never minted, and one whose write failed, reach neither a waiter nor the sink.
func TestRunner_QueryContextUsage_UnadmittedResponseReachesNeitherWaiterNorSink(t *testing.T) {
	t.Run("id never minted", func(t *testing.T) {
		sink := make(chan turnevent.Event, 4)
		p := NewParser(func(ev turnevent.Event) { sink <- ev }, discardLogger())
		w := newMCPStatusQueryWriter()
		r := contextUsageQueryRunner(t, p, w)

		done := queryContextUsageAsync(context.Background(), r, "summary")
		req := awaitContextUsageRequest(t, w)

		// One forged id in the private query namespace, one bare sequence id the
		// daemon never registered. Neither may publish or complete the waiter.
		_, _ = p.Write(contextUsageQueryResponse(t, contextUsageQueryIDPrefix+"9999", controlResponseSuccess, "forged"))
		_, _ = p.Write(contextUsageQueryResponse(t, "987654", controlResponseSuccess, "unminted"))
		select {
		case got := <-done:
			t.Fatalf("unminted id completed the query: %+v", got)
		default:
		}
		assertNoContextUsageSinkEvent(t, sink)

		_, _ = p.Write(contextUsageQueryResponse(t, req.RequestID, controlResponseSuccess, "exact"))
		got := awaitContextUsageOutcome(t, done)
		if !got.ok || got.usage.Model != "exact-model" {
			t.Fatalf("exact response result = %+v", got)
		}
	})

	t.Run("write failed", func(t *testing.T) {
		sink := make(chan turnevent.Event, 4)
		p := NewParser(func(ev turnevent.Event) { sink <- ev }, discardLogger())
		w := newMCPStatusQueryWriter()
		w.write = func([]byte) (int, error) { return 0, errors.New("stdin closed") }
		r := contextUsageQueryRunner(t, p, w)

		done := queryContextUsageAsync(context.Background(), r, "summary")
		req := awaitContextUsageRequest(t, w)
		got := awaitContextUsageOutcome(t, done)
		if got.ok {
			t.Fatalf("failed write reported a reading: %+v", got)
		}

		_, _ = p.Write(contextUsageQueryResponse(t, req.RequestID, controlResponseSuccess, "late"))
		assertNoContextUsageSinkEvent(t, sink)
	})
}

// A matched reply is terminal whatever it carries: an error subtype and an unusable
// payload each return not-answered rather than leaving the caller waiting.
func TestRunner_QueryContextUsage_FirstMatchedResponseIsTerminal(t *testing.T) {
	tests := []struct {
		name    string
		subtype string
	}{
		{name: "error subtype", subtype: "error"},
		{name: "absent subtype", subtype: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sink := make(chan turnevent.Event, 4)
			p := NewParser(func(ev turnevent.Event) { sink <- ev }, discardLogger())
			w := newMCPStatusQueryWriter()
			r := contextUsageQueryRunner(t, p, w)

			done := queryContextUsageAsync(context.Background(), r, "summary")
			req := awaitContextUsageRequest(t, w)
			_, _ = p.Write(contextUsageQueryResponse(t, req.RequestID, tc.subtype, "rejected"))

			got := awaitContextUsageOutcome(t, done)
			if got.ok {
				t.Fatalf("unusable response reported a reading: %+v", got)
			}
			_, _ = p.Write(contextUsageQueryResponse(t, req.RequestID, controlResponseSuccess, "second"))
			assertNoContextUsageSinkEvent(t, sink)
		})
	}
}

// A detail outside the settled vocabulary is refused with nothing minted and nothing
// written. The write count is the assertion that proves both.
func TestRunner_QueryContextUsage_UnsupportedDetailWritesNothing(t *testing.T) {
	for _, detail := range []string{"", "SUMMARY", "verbose", "full "} {
		t.Run("detail="+detail, func(t *testing.T) {
			sink := make(chan turnevent.Event, 4)
			p := NewParser(func(ev turnevent.Event) { sink <- ev }, discardLogger())
			w := newMCPStatusQueryWriter()
			r := contextUsageQueryRunner(t, p, w)

			usage, ok := r.QueryContextUsage(context.Background(), detail)
			if ok {
				t.Fatalf("unsupported detail %q reported a reading: %+v", detail, usage)
			}
			if w.count() != 0 {
				t.Fatalf("unsupported detail %q wrote %d requests, want 0", detail, w.count())
			}
			assertNoContextUsageSinkEvent(t, sink)
		})
	}
}

// Every ask that cannot be served returns rather than blocking. awaitContextUsageOutcome
// is the bound: a blocking arm fails the suite on its timeout, not on a race.
func TestRunner_QueryContextUsage_UnserviceableAskReturnsNotAnswered(t *testing.T) {
	t.Run("no live child", func(t *testing.T) {
		p := NewParser(func(turnevent.Event) {}, discardLogger())
		w := newMCPStatusQueryWriter()
		r := contextUsageQueryRunner(t, p, w)
		if old := r.takeStdin(); old != nil {
			_ = old.Close()
		}

		got := awaitContextUsageOutcome(t, queryContextUsageAsync(context.Background(), r, "summary"))
		if got.ok {
			t.Fatalf("query without a live child reported a reading: %+v", got)
		}
		if w.count() != 0 {
			t.Fatalf("query without a live child wrote %d requests, want 0", w.count())
		}
	})

	t.Run("rotation in flight", func(t *testing.T) {
		p := NewParser(func(turnevent.Event) {}, discardLogger())
		w := newMCPStatusQueryWriter()
		r := contextUsageQueryRunner(t, p, w)
		r.mu.Lock()
		r.rotating = true
		r.mu.Unlock()

		got := awaitContextUsageOutcome(t, queryContextUsageAsync(context.Background(), r, "summary"))
		if got.ok {
			t.Fatalf("query during a rotation reported a reading: %+v", got)
		}
		if w.count() != 0 {
			t.Fatalf("query during a rotation wrote %d requests, want 0", w.count())
		}
	})

	t.Run("context already ended", func(t *testing.T) {
		p := NewParser(func(turnevent.Event) {}, discardLogger())
		w := newMCPStatusQueryWriter()
		r := contextUsageQueryRunner(t, p, w)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		got := awaitContextUsageOutcome(t, queryContextUsageAsync(ctx, r, "summary"))
		if got.ok {
			t.Fatalf("query with an ended context reported a reading: %+v", got)
		}
		if w.count() != 0 {
			t.Fatalf("query with an ended context wrote %d requests, want 0", w.count())
		}
	})

	t.Run("context ends while waiting", func(t *testing.T) {
		p := NewParser(func(turnevent.Event) {}, discardLogger())
		w := newMCPStatusQueryWriter()
		r := contextUsageQueryRunner(t, p, w)
		ctx, cancel := context.WithCancel(context.Background())

		done := queryContextUsageAsync(ctx, r, "summary")
		awaitContextUsageRequest(t, w)
		cancel()
		got := awaitContextUsageOutcome(t, done)
		if got.ok {
			t.Fatalf("canceled wait reported a reading: %+v", got)
		}
	})

	// The two child boundaries are separate code paths and are asserted separately:
	// takeStdin retires on teardown, beginMCPStatusChild on the next spawn's install.
	t.Run("child torn down while waiting", func(t *testing.T) {
		p := NewParser(func(turnevent.Event) {}, discardLogger())
		w := newMCPStatusQueryWriter()
		r := contextUsageQueryRunner(t, p, w)

		done := queryContextUsageAsync(context.Background(), r, "summary")
		awaitContextUsageRequest(t, w)
		if old := r.takeStdin(); old != nil {
			_ = old.Close()
		}
		got := awaitContextUsageOutcome(t, done)
		if got.ok {
			t.Fatalf("query survived its child's teardown: %+v", got)
		}
	})

	t.Run("child replaced while waiting", func(t *testing.T) {
		p := NewParser(func(turnevent.Event) {}, discardLogger())
		w := newMCPStatusQueryWriter()
		r := contextUsageQueryRunner(t, p, w)

		done := queryContextUsageAsync(context.Background(), r, "summary")
		req := awaitContextUsageRequest(t, w)
		p.beginMCPStatusChild(false, func() error { return nil })
		got := awaitContextUsageOutcome(t, done)
		if got.ok {
			t.Fatalf("query survived its child's replacement: %+v", got)
		}

		// The predecessor's late reply stays private after its waiter is gone: the
		// prefix keeps it claimed, so it never reaches the successor's sink.
		sink := make(chan turnevent.Event, 4)
		p.sink = func(ev turnevent.Event) { sink <- ev }
		_, _ = p.Write(contextUsageQueryResponse(t, req.RequestID, controlResponseSuccess, "late"))
		assertNoContextUsageSinkEvent(t, sink)
	})
}

// The four private control-id namespaces sharing the control_response arm must stay
// pairwise disjoint. A collision costs a hang rather than a miss, because each
// correlator claims every unregistered id carrying its own prefix.
func TestContextUsageQueryIDPrefix_DisjointFromSiblingNamespaces(t *testing.T) {
	prefixes := map[string]string{
		"applied settings query": appliedSettingsQueryIDPrefix,
		"context usage query":    contextUsageQueryIDPrefix,
		"mcp status query":       mcpStatusQueryIDPrefix,
		"mcp actuation":          mcpActuationIDPrefix,
	}
	for name, prefix := range prefixes {
		if prefix == "" {
			t.Fatalf("%s prefix is empty", name)
		}
		// The bare sequence RequestContextUsage and RequestMCPStatus mint is decimal,
		// so no prefixed id can ever collide with one.
		if strings.IndexFunc(prefix, func(r rune) bool { return r < '0' || r > '9' }) < 0 {
			t.Fatalf("%s prefix %q is all digits and collides with the bare sequence", name, prefix)
		}
		for otherName, other := range prefixes {
			if name == otherName {
				continue
			}
			if strings.HasPrefix(prefix, other) {
				t.Fatalf("%s prefix %q starts with the %s prefix %q", name, prefix, otherName, other)
			}
		}
	}
}
