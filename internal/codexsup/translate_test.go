package codexsup

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// TestMethodListsPartitionServerNotifications: every notification that can
// reach OnNotification is mapped, ignored or surfaced — exactly one of the
// three — so nothing is dropped silently and a schema bump forces a decision.
func TestMethodListsPartitionServerNotifications(t *testing.T) {
	assertPartition(t, serverNotifications, map[string][]string{
		"mapped": mappedMethods, "ignored": ignoredMethods, "unrecognized": unrecognizedMethods,
	})
	for _, m := range []string{"model/rerouted", "model/verification"} {
		if len(NewTranslator("m").Translate(m, json.RawMessage(`{}`))) == 0 {
			t.Errorf("%s produced no visible event", m)
		}
	}
}

// TestItemTypesClassified: the same three-way partition over every
// ThreadItem type in the committed schema.
func TestItemTypesClassified(t *testing.T) {
	s := loadSchema(t)
	def, err := s.resolve("#/definitions/v2/ThreadItem")
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, arm := range def["oneOf"].([]any) {
		typ := arm.(map[string]any)["properties"].(map[string]any)["type"].(map[string]any)
		types = append(types, typ["enum"].([]any)[0].(string))
	}
	assertPartition(t, types, map[string][]string{
		"mapped": mappedItemTypes, "ignored": ignoredItemTypes, "unrecognized": unrecognizedItemTypes,
	})
}

func assertPartition(t *testing.T, all []string, lists map[string][]string) {
	t.Helper()
	on := map[string][]string{}
	for name, list := range lists {
		for _, x := range list {
			on[x] = append(on[x], name)
		}
	}
	for _, x := range all {
		if len(on[x]) != 1 {
			t.Errorf("%q is on %d lists %v, want exactly one", x, len(on[x]), on[x])
		}
		delete(on, x)
	}
	for x := range on {
		t.Errorf("%q is classified but not in the schema", x)
	}
}

type frame struct {
	Kind   string          `json:"kind"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func readFrames(t *testing.T, path string) []frame {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []frame
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var fr frame
		if err := json.Unmarshal(sc.Bytes(), &fr); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		out = append(out, fr)
	}
	return out
}

// replay runs a fixture's notifications through a translator for model.
func replay(t *testing.T, path, model string) []turnevent.Event {
	t.Helper()
	tr := NewTranslator(model)
	var events []turnevent.Event
	for _, f := range readFrames(t, path) {
		if f.Kind == "notification" {
			events = append(events, tr.Translate(f.Method, f.Params)...)
		}
	}
	return events
}

func only[T turnevent.Event](events []turnevent.Event) []T {
	var out []T
	for _, e := range events {
		if x, ok := e.(T); ok {
			out = append(out, x)
		}
	}
	return out
}

func text(chunks []turnevent.TextChunk) string {
	var b strings.Builder
	for _, c := range chunks {
		b.WriteString(c.Text)
	}
	return b.String()
}

func singleEnd(t *testing.T, events []turnevent.Event) turnevent.TurnEnd {
	t.Helper()
	ends := only[turnevent.TurnEnd](events)
	if len(ends) != 1 {
		t.Fatalf("got %d TurnEnd, want exactly one: %#v", len(ends), events)
	}
	return ends[0]
}

func TestTranslateCapturedPlainTurn(t *testing.T) {
	events := replay(t, "testdata/capture/plain.jsonl", captureModel)
	chunks := only[turnevent.TextChunk](events)
	if text(chunks) != "hello" || !strings.HasPrefix(chunks[0].MessageID, "msg_") {
		t.Errorf("text chunks %#v", chunks)
	}
	end := singleEnd(t, events)
	want := turnevent.TurnEnd{
		Reason: turnevent.TurnEndReasonEndTurn, InputTokens: 14559 - 11008, CacheReadTokens: 11008, OutputTokens: 5,
		ModelWindows: []turnevent.ModelWindow{{ModelID: captureModel, WindowTokens: 258400}},
	}
	if !reflect.DeepEqual(end, want) {
		t.Errorf("TurnEnd = %#v, want %#v", end, want)
	}
	if u := only[turnevent.Unrecognized](events); len(u) != 0 {
		t.Errorf("a plain turn surfaced %#v", u)
	}
	if !reflect.DeepEqual(events[len(events)-1], turnevent.Event(end)) {
		t.Error("TurnEnd is not the last event")
	}
}

// TestTranslateCapturedCommandTurnUsesTotalDelta: the accepted-command turn
// makes two model calls. `last` holds only the second (5 output tokens); the
// turn's counts are the change in `total` across it.
func TestTranslateCapturedCommandTurnUsesTotalDelta(t *testing.T) {
	events := replay(t, "testdata/capture/command_accepted.jsonl", captureModel)
	end := singleEnd(t, events)
	if end.OutputTokens != 67 || end.CacheReadTokens != 25088 || end.InputTokens != 30395-25088 {
		t.Errorf("counts in=%d cacheRead=%d out=%d, want the whole turn's", end.InputTokens, end.CacheReadTokens, end.OutputTokens)
	}
	u := only[turnevent.Unrecognized](events)
	if len(u) != 1 || u[0].Site != turnevent.UnrecognizedCodexItem || u[0].Kind != "commandExecution" {
		t.Errorf("unrecognized %#v, want one commandExecution item row", u)
	}
	if len(only[turnevent.TextChunk](events)) == 0 {
		t.Error("no text")
	}
}

// TestTurnUsageBaseOnLaterTurn: a second turn on the same thread reports its
// own counts, not the thread's running total.
func TestTurnUsageBaseOnLaterTurn(t *testing.T) {
	tr := NewTranslator("m")
	usage := func(turn string, total, last tokenBreakdown) {
		p, _ := json.Marshal(map[string]any{"threadId": "th", "turnId": turn, "tokenUsage": map[string]any{
			"total": total, "last": last, "modelContextWindow": 1000}})
		tr.Translate("thread/tokenUsage/updated", p)
	}
	usage("t2", tokenBreakdown{InputTokens: 150, CachedInputTokens: 60, OutputTokens: 12}, tokenBreakdown{InputTokens: 50, CachedInputTokens: 20, OutputTokens: 2})
	usage("t2", tokenBreakdown{InputTokens: 210, CachedInputTokens: 100, OutputTokens: 15}, tokenBreakdown{InputTokens: 60, CachedInputTokens: 40, OutputTokens: 3})
	end := singleEnd(t, tr.Translate("turn/completed", json.RawMessage(`{"threadId":"th","turn":{"id":"t2","items":[],"status":"completed"}}`)))
	// Base before t2 = 150-50 input, 60-20 cached, 12-2 output.
	if end.InputTokens != (210-100)-(100-40) || end.CacheReadTokens != 100-40 || end.OutputTokens != 15-10 {
		t.Errorf("counts in=%d cacheRead=%d out=%d", end.InputTokens, end.CacheReadTokens, end.OutputTokens)
	}
}

// TestTurnUsageClampsNegativeCounts: a running total that shrinks, or cached
// input above input, is malformed peer data and yields zero, never negative.
func TestTurnUsageClampsNegativeCounts(t *testing.T) {
	tr := NewTranslator("m")
	for _, total := range []tokenBreakdown{
		{InputTokens: 100, CachedInputTokens: 10, OutputTokens: 50},
		{InputTokens: 20, CachedInputTokens: 90, OutputTokens: 5},
	} {
		p, _ := json.Marshal(map[string]any{"threadId": "th", "turnId": "t", "tokenUsage": map[string]any{
			"total": total, "last": tokenBreakdown{}, "modelContextWindow": 1000}})
		tr.Translate("thread/tokenUsage/updated", p)
	}
	end := singleEnd(t, tr.Translate("turn/completed", json.RawMessage(`{"threadId":"th","turn":{"id":"t","items":[],"status":"completed"}}`)))
	if end.InputTokens != 0 || end.CacheReadTokens != 80 || end.OutputTokens != 0 || end.CacheCreationTokens != 0 {
		t.Errorf("counts in=%d cacheRead=%d cacheWrite=%d out=%d, want no negatives",
			end.InputTokens, end.CacheReadTokens, end.CacheCreationTokens, end.OutputTokens)
	}
}

func TestTranslateCapturedInterruptedTurn(t *testing.T) {
	end := singleEnd(t, replay(t, "testdata/capture/interrupted.jsonl", captureModel))
	if end.Reason != turnevent.TurnEndReasonCancelled || end.IsError {
		t.Errorf("TurnEnd = %#v, want cancelled, not an error", end)
	}
}

func TestTranslateHandBuilt(t *testing.T) {
	for _, tc := range []struct {
		name  string
		model string
		want  []turnevent.Event
	}{
		{name: "reasoning", want: []turnevent.Event{
			turnevent.ThoughtChunk{MessageID: "rs_1", Text: "Multiply 17 by 23."},
			turnevent.ThoughtChunk{MessageID: "rs_1", Text: "17*23=391"},
		}},
		{name: "usage_limit", want: []turnevent.Event{
			turnevent.RateLimited{Status: "rejected"},
			turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn, IsError: true, ErrorCategory: "usageLimitExceeded"},
		}},
		{name: "context_window", model: "gpt-6-astra", want: []turnevent.Event{
			turnevent.TurnEnd{Reason: turnevent.TurnEndReasonMaxTokens, IsError: true, ErrorCategory: "contextWindowExceeded",
				InputTokens: 249000, CacheReadTokens: 1000, OutputTokens: 10,
				ModelWindows: []turnevent.ModelWindow{{ModelID: "gpt-6-astra", WindowTokens: 258400}}},
		}},
		{name: "http_failed", want: []turnevent.Event{
			turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn, IsError: true, ErrorCategory: "httpConnectionFailed"},
		}},
		{name: "reroute", model: "gpt-6-astra", want: []turnevent.Event{
			turnevent.Banner{Level: "warning", Text: "Codex rerouted this turn from gpt-6-astra to gpt-6-luna (highRiskCyberActivity)"},
			turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn, InputTokens: 60, CacheReadTokens: 40, OutputTokens: 7,
				ModelWindows: []turnevent.ModelWindow{{ModelID: "gpt-6-luna", WindowTokens: 128000}}},
		}},
		{name: "compaction", want: []turnevent.Event{
			turnevent.Compacting{Active: true}, turnevent.Compacting{Active: false},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := replay(t, filepath.Join("testdata", "handbuilt", tc.name+".jsonl"), tc.model)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got  %#v\nwant %#v", got, tc.want)
			}
		})
	}
}

// TestTranslateUnrecognizedLanes: an unmapped item surfaces once, on
// item/started; an unmapped method and an item type unknown even to the
// schema surface too.
func TestTranslateUnrecognizedLanes(t *testing.T) {
	for _, tc := range []struct {
		name string
		site turnevent.UnrecognizedSite
		kind string
	}{
		{"unmapped_item", turnevent.UnrecognizedCodexItem, "webSearch"},
		{"model_verification", turnevent.UnrecognizedCodexMethod, "model/verification"},
	} {
		got := only[turnevent.Unrecognized](replay(t, filepath.Join("testdata", "handbuilt", tc.name+".jsonl"), ""))
		if len(got) != 1 || got[0].Site != tc.site || got[0].Kind != tc.kind || got[0].Raw == "" {
			t.Errorf("%s: %#v", tc.name, got)
		}
	}
	got := NewTranslator("").Translate("item/started", json.RawMessage(`{"item":{"type":"futureItem","id":"x"}}`))
	if len(got) != 1 || got[0].(turnevent.Unrecognized).Kind != "futureItem" {
		t.Errorf("unknown item type: %#v", got)
	}
	got = NewTranslator("").Translate("turn/completed", json.RawMessage(`[`))
	if len(got) != 1 || got[0].(turnevent.Unrecognized).Site != turnevent.UnrecognizedUndecodable {
		t.Errorf("undecodable: %#v", got)
	}
	if got := NewTranslator("").Translate("account/rateLimits/updated", json.RawMessage(`{}`)); got != nil {
		t.Errorf("ignored method produced %#v", got)
	}
}

// TestHandBuiltFramesMatchSchema validates every hand-built frame's params
// against its notification's definition in the committed schema.
func TestHandBuiltFramesMatchSchema(t *testing.T) {
	s := loadSchema(t)
	def, err := s.resolve("#/definitions/ServerNotification")
	if err != nil {
		t.Fatal(err)
	}
	refs := map[string]string{}
	for _, a := range def["oneOf"].([]any) {
		props := a.(map[string]any)["properties"].(map[string]any)
		ref, _ := props["params"].(map[string]any)["$ref"].(string)
		refs[props["method"].(map[string]any)["enum"].([]any)[0].(string)] = ref
	}
	paths, _ := filepath.Glob(filepath.Join("testdata", "handbuilt", "*.jsonl"))
	if len(paths) == 0 {
		t.Fatal("no hand-built frames")
	}
	for _, p := range paths {
		for i, f := range readFrames(t, p) {
			sch, err := s.resolve(refs[f.Method])
			if err != nil {
				t.Fatalf("%s:%d %s: %v", p, i, f.Method, err)
			}
			var v any
			if err := json.Unmarshal(f.Params, &v); err != nil {
				t.Fatal(err)
			}
			if err := s.validate(sch, v); err != nil {
				t.Errorf("%s frame %d (%s) does not match %s: %v", p, i, f.Method, refs[f.Method], err)
			}
		}
	}
}

// TestTranslateBounds: oversized peer strings are bounded at construction.
func TestTranslateBounds(t *testing.T) {
	big := strings.Repeat("x", maxUnrecognizedRaw+10)
	u := NewTranslator("").Translate("warning", json.RawMessage(`"`+big+`"`))[0].(turnevent.Unrecognized)
	if !u.Truncated || len(u.Raw) != maxUnrecognizedRaw {
		t.Errorf("raw len %d truncated %v", len(u.Raw), u.Truncated)
	}
	long := strings.Repeat("m", maxModelID+1)
	tr := NewTranslator(long)
	tr.Translate("thread/tokenUsage/updated", json.RawMessage(`{"turnId":"t","tokenUsage":{"total":{},"last":{},"modelContextWindow":5}}`))
	end := singleEnd(t, tr.Translate("turn/completed", json.RawMessage(`{"turn":{"id":"t","status":"failed","error":{"message":"","codexErrorInfo":{"a":{},"b":{}}}}}`)))
	if end.ModelWindows != nil || end.DroppedModelWindows != 1 || end.ErrorCategory != "" {
		t.Errorf("TurnEnd = %#v", end)
	}
	for i := range maxPendingTurns + 5 {
		tr.Translate("thread/tokenUsage/updated", json.RawMessage(`{"turnId":"`+strings.Repeat("t", i+2)+`","tokenUsage":{"total":{},"last":{}}}`))
	}
	if len(tr.usage) > maxPendingTurns {
		t.Errorf("pending usage holds %d turns", len(tr.usage))
	}
}
