package streamsup

import (
	"encoding/json"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// The tool_progress arm's tests (#2089). Their own file, not an append to
// parser_test.go: feature/2087 adds 283 lines at that file's END, so an append
// here would be an add/add conflict at the same anchor for no gain.
//
// "tool_progress is not on ignoredLineTypes" — AC1's structural half — is
// already pinned, and more strongly than a membership check could:
// TestParser_IgnoredLineTypesIsTheMeasuredSet asserts the whole map equals
// {"system": true} by DeepEqual, so putting this type on the list fails there.
// A second assertion here would be a weaker restatement of it.

// toolProgressDropMsg is the Debug message the arm's drop site emits, as a
// literal rather than the production constant for harnessNudgeDropMsg's reason:
// a fixture built from the thing it validates follows an edit green.
const toolProgressDropMsg = "streamsup: dropping tool_progress"

// toolProgressLine builds a tool_progress line carrying the fixed identifier
// fields every variety has, plus whatever marker JSON the case under test adds.
// markers is spliced in as raw JSON so a case can pin a VALUE's type — the whole
// point of the tolerance table — which a map[string]any fixture could not do
// (Go's encoder would render 1 and true identically per their Go types, never
// per the bytes claude sends).
func toolProgressLine(markers string) string {
	const fixed = `"type":"tool_progress",` +
		`"tool_use_id":"toolu_01BpNmXQZ8h55XsJmmBauLve-heartbeat-0",` +
		`"tool_name":"Bash","parent_tool_use_id":null,"elapsed_time_seconds":30`
	if markers == "" {
		return "{" + fixed + "}"
	}
	return "{" + fixed + "," + markers + "}"
}

// TestParser_ToolProgressMarkerTolerance is the matcher's tolerance table: which
// shapes it CONSUMES and which it lets through to the unrecognized lane.
//
// Synthesized lines are legitimate here and are NOT legitimate for the heartbeat
// proof below. These cases assert our own rule — how strictly a marker is
// matched — which is a decision this package owns. What claude actually SPELLS is
// not ours to assert, and a synthesized fixture keyed on a spelling claude does
// not use agrees with itself forever; that is what the capture-driven test is
// for.
func TestParser_ToolProgressMarkerTolerance(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		markers  string
		consumed bool
	}{
		{name: "heartbeat true is the heartbeat variety", markers: `"heartbeat":true`, consumed: true},
		{name: "subagent_type present is the retry variety", markers: `"subagent_type":"general-purpose"`, consumed: true},
		{name: "repl_call present is the repl variety", markers: `"repl_call":{"inner_tool_name":"Bash","phase":"start"}`, consumed: true},
		{
			// The unresolved retry frame. Both markers, one match, still exactly
			// one consumption and no double drop.
			name:     "subagent_type and subagent_retry together",
			markers:  `"subagent_type":"general-purpose","subagent_retry":{"attempt":1,"max_retries":3}`,
			consumed: true,
		},

		// Type-strictness. A string, a number and the false literal are each a
		// DIFFERENT value from JSON true, and a matcher that folded any of them in
		// would be reading a shape claude has never been observed to send.
		{name: "heartbeat as a string is not a match", markers: `"heartbeat":"true"`, consumed: false},
		{name: "heartbeat as a number is not a match", markers: `"heartbeat":1`, consumed: false},
		{name: "heartbeat false is not a match", markers: `"heartbeat":false`, consumed: false},
		{name: "heartbeat null is not a match", markers: `"heartbeat":null`, consumed: false},

		// A null marker is not a marker: the pointer target stays nil, which is
		// the present-vs-absent rule jsonKey's doc states.
		{name: "subagent_type null is not a match", markers: `"subagent_type":null`, consumed: false},
		{name: "repl_call null is not a match", markers: `"repl_call":null`, consumed: false},

		// subagent_retry is NOT read. Every agent_api_retry frame sets
		// subagent_type, so reading the retry object as well would buy nothing and
		// widen the matched set beyond what was measured.
		{name: "subagent_retry alone is not a match", markers: `"subagent_retry":{"attempt":1}`, consumed: false},

		// The residual bash/powershell-progress shape: no marker at all. It MUST
		// reach the lane — that is the open question the capture answers, and
		// swallowing it here would answer it wrong in silence.
		{name: "no marker at all reaches the lane", markers: "", consumed: false},

		// Forgery: markers nested below the top level. streamLine's rule is that
		// control shapes are read from the top level only, so claude's own tool
		// output cannot mint a consumption.
		{name: "a nested heartbeat is not a top-level marker", markers: `"payload":{"heartbeat":true}`, consumed: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := collectEvents(toolProgressLine(tc.markers))
			if tc.consumed {
				if len(got) != 0 {
					t.Fatalf("consumed frame emitted %d event(s), want 0: %+v", len(got), got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("unmatched frame emitted %d event(s), want exactly 1: %+v", len(got), got)
			}
			unrec, ok := got[0].(turnevent.Unrecognized)
			if !ok {
				t.Fatalf("event type = %T, want turnevent.Unrecognized", got[0])
			}
			if unrec.Site != turnevent.UnrecognizedLineType {
				t.Errorf("Site = %q, want %q", unrec.Site, turnevent.UnrecognizedLineType)
			}
			if unrec.Kind != "tool_progress" {
				t.Errorf("Kind = %q, want %q", unrec.Kind, "tool_progress")
			}
		})
	}
}

// TestParser_ToolProgressDropIsLoggedContentFree pins the drop site's record.
// The `marker` attribute is a constant this package chose from a closed set of
// three, never a byte derived from the line — the same class as the `type` the
// existing drop branch logs.
func TestParser_ToolProgressDropIsLoggedContentFree(t *testing.T) {
	t.Parallel()
	const secret = "toolu_01BpNmXQZ8h55XsJmmBauLve-heartbeat-0"
	rec := &logRecorder{}
	p := NewParser(func(turnevent.Event) {}, slog.New(rec))
	if _, err := p.Write([]byte(toolProgressLine(`"heartbeat":true`) + "\n")); err != nil {
		t.Fatalf("Write err = %v, want nil", err)
	}

	drops := rec.withMessage(toolProgressDropMsg)
	if len(drops) != 1 {
		t.Fatalf("records with message %q: got %d, want 1 (all records: %+v)",
			toolProgressDropMsg, len(drops), rec.all())
	}
	wantAttrs := map[string]string{"type": "tool_progress", "marker": "heartbeat"}
	if !reflect.DeepEqual(drops[0].attrs, wantAttrs) {
		t.Errorf("drop attrs: got %v, want exactly %v", drops[0].attrs, wantAttrs)
	}
	for _, r := range rec.all() {
		if strings.Contains(r.msg, secret) {
			t.Errorf("record message carries a payload identifier: %q", r.msg)
		}
		for k, v := range r.attrs {
			if strings.Contains(v, secret) {
				t.Errorf("record %q attr %q carries a payload identifier; the drop site logs "+
					"type and marker only", r.msg, k)
			}
		}
	}
}

// TestParser_ToolProgressMarkerDiscriminatorIsPerVariety keeps the `marker`
// attribute from going vacuous. Three varieties share ONE drop site, so a
// discriminator that reported the same value for all three would look like
// evidence while distinguishing nothing.
func TestParser_ToolProgressMarkerDiscriminatorIsPerVariety(t *testing.T) {
	t.Parallel()
	tests := []struct{ markers, want string }{
		{`"heartbeat":true`, "heartbeat"},
		{`"subagent_type":"general-purpose"`, "subagent_type"},
		{`"repl_call":{"inner_tool_name":"Bash"}`, "repl_call"},
	}
	seen := map[string]bool{}
	for _, tc := range tests {
		rec := &logRecorder{}
		p := NewParser(func(turnevent.Event) {}, slog.New(rec))
		if _, err := p.Write([]byte(toolProgressLine(tc.markers) + "\n")); err != nil {
			t.Fatalf("Write err = %v, want nil", err)
		}
		drops := rec.withMessage(toolProgressDropMsg)
		if len(drops) != 1 {
			t.Fatalf("%s: records with message %q: got %d, want 1", tc.markers, toolProgressDropMsg, len(drops))
		}
		if got := drops[0].attrs["marker"]; got != tc.want {
			t.Errorf("%s: marker = %q, want %q", tc.markers, got, tc.want)
		}
		seen[drops[0].attrs["marker"]] = true
	}
	if len(seen) != len(tests) {
		t.Errorf("the three varieties produced %d distinct marker value(s), want %d: %v",
			len(seen), len(tests), seen)
	}
}

// TestParser_ToolProgressCapturedFramesEmitHeartbeatEvents is AC1's capture proof,
// and the reason it is driven from committed capture bytes rather than a hand-written
// line: a production decoder keyed on a spelling claude does not use is dead code,
// and no fixture written by the same hand that wrote the decoder can catch it.
// That is the tool_use_result / toolUseResult failure userToolResultLine records.
//
// Two assertions, and the second is what makes the first non-vacuous:
//
//   - every captured heartbeat emits one ToolProgress carrying the real parent
//     call id and its elapsed reading, never the synthetic heartbeat id;
//   - at least one captured frame carries `heartbeat` equal to JSON true, decoded
//     HERE out of the raw bytes and never by the production matcher. Zero such
//     frames means the marker set is keyed on a spelling claude does not send,
//     and this fatals rather than passing green on a decoder that never fires.
func TestParser_ToolProgressCapturedFramesEmitHeartbeatEvents(t *testing.T) {
	t.Parallel()
	frames := capturedToolProgressLines(t)
	const capturedToolCallID = "toolu_01V6TGAWoiykrgeJNzTqX1dG"

	heartbeats := 0
	for i, raw := range frames {
		var probe struct {
			Heartbeat    json.RawMessage `json:"heartbeat"`
			ToolUseID    string          `json:"tool_use_id"`
			ParentToolID string          `json:"parent_tool_use_id"`
			Elapsed      int             `json:"elapsed_time_seconds"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil {
			t.Fatalf("captured frame %d: independent decode: %v", i, err)
		}
		if string(probe.Heartbeat) != "true" {
			continue
		}
		heartbeats++
		got := collectEvents(string(raw))
		if len(got) != 1 {
			t.Fatalf("captured heartbeat %d emitted %d event(s), want 1: %+v", i, len(got), got)
		}
		progress, ok := got[0].(turnevent.ToolProgress)
		if !ok {
			t.Fatalf("captured heartbeat %d event type = %T, want turnevent.ToolProgress", i, got[0])
		}
		if progress.ToolCallID != probe.ParentToolID {
			t.Errorf("captured heartbeat %d ToolCallID = %q, want parent_tool_use_id %q",
				i, progress.ToolCallID, probe.ParentToolID)
		}
		if progress.ToolCallID != capturedToolCallID {
			t.Errorf("captured heartbeat %d ToolCallID = %q, want captured Bash call id %q",
				i, progress.ToolCallID, capturedToolCallID)
		}
		if progress.ToolCallID == probe.ToolUseID || strings.Contains(progress.ToolCallID, "-heartbeat-") {
			t.Errorf("captured heartbeat %d ToolCallID = %q, want real parent id, not synthetic tool_use_id %q",
				i, progress.ToolCallID, probe.ToolUseID)
		}
		if progress.ElapsedSeconds != probe.Elapsed {
			t.Errorf("captured heartbeat %d ElapsedSeconds = %d, want %d",
				i, progress.ElapsedSeconds, probe.Elapsed)
		}
		wantElapsed := heartbeats * 30
		if progress.ElapsedSeconds != wantElapsed {
			t.Errorf("captured heartbeat %d ElapsedSeconds = %d, want cadence value %d",
				i, progress.ElapsedSeconds, wantElapsed)
		}
	}
	if heartbeats == 0 {
		t.Fatalf("%s: %d captured tool_progress frames, NONE carrying `heartbeat` equal to JSON true.\n"+
			"The heartbeat marker is keyed on a field name claude does not send on this surface, so "+
			"consumeToolProgress's heartbeat arm can never fire and the ticket's own symptom is unfixed. "+
			"Re-read the frames' key set rather than loosening the match",
			toolProgressCapturePath, len(frames))
	}
	t.Logf("%d captured tool_progress frame(s), %d carrying heartbeat:true", len(frames), heartbeats)
}

// TestParser_ToolProgressHeartbeatRequiresJoinableParentID pins the only field
// gate on heartbeat emission. Every row is still consumed: zero events means the
// invalid heartbeat did not leak into the unrecognized lane.
func TestParser_ToolProgressHeartbeatRequiresJoinableParentID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		parent string
	}{
		{name: "absent", parent: ""},
		{name: "empty", parent: `"parent_tool_use_id":"",`},
		{name: "non-string", parent: `"parent_tool_use_id":42,`},
		{name: "over cap", parent: `"parent_tool_use_id":"` + strings.Repeat("x", maxTaskFieldID+1) + `",`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			line := toolProgressLine(`"heartbeat":true`)
			line = strings.Replace(line, `"parent_tool_use_id":null,`, tc.parent, 1)
			if got := collectEvents(line); len(got) != 0 {
				t.Fatalf("emitted %d event(s), want consumed without event: %+v", len(got), got)
			}
		})
	}
}

func TestParser_ToolProgressHeartbeatCarriesSignedElapsedReading(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		elapsed string
		want    int
	}{
		{name: "positive", elapsed: `,"elapsed_time_seconds":30`, want: 30},
		{name: "zero", elapsed: `,"elapsed_time_seconds":0`, want: 0},
		{name: "negative", elapsed: `,"elapsed_time_seconds":-7`, want: -7},
		{name: "absent", elapsed: "", want: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			line := toolProgressLine(`"heartbeat":true`)
			line = strings.Replace(line, `"parent_tool_use_id":null`, `"parent_tool_use_id":"toolu-real"`, 1)
			line = strings.Replace(line, `,"elapsed_time_seconds":30`, tc.elapsed, 1)
			got := collectEvents(line)
			if len(got) != 1 {
				t.Fatalf("emitted %d event(s), want 1: %+v", len(got), got)
			}
			progress, ok := got[0].(turnevent.ToolProgress)
			if !ok {
				t.Fatalf("event type = %T, want turnevent.ToolProgress", got[0])
			}
			if progress.ToolCallID != "toolu-real" || progress.ElapsedSeconds != tc.want {
				t.Errorf("progress = %+v, want ToolCallID toolu-real and ElapsedSeconds %d", progress, tc.want)
			}
		})
	}
}

func TestParser_ToolProgressHeartbeatAcceptsParentIDAtCap(t *testing.T) {
	t.Parallel()
	id := strings.Repeat("x", maxTaskFieldID)
	line := toolProgressLine(`"heartbeat":true`)
	line = strings.Replace(line, `"parent_tool_use_id":null`, `"parent_tool_use_id":"`+id+`"`, 1)
	got := collectEvents(line)
	if len(got) != 1 {
		t.Fatalf("emitted %d event(s), want 1: %+v", len(got), got)
	}
	if progress, ok := got[0].(turnevent.ToolProgress); !ok || progress.ToolCallID != id {
		t.Fatalf("event = %#v, want ToolProgress with exact-cap id", got[0])
	}
}

func TestParser_ToolProgressHeartbeatMalformedElapsedStaysConsumed(t *testing.T) {
	t.Parallel()
	line := toolProgressLine(`"heartbeat":true`)
	line = strings.Replace(line, `"parent_tool_use_id":null`, `"parent_tool_use_id":"toolu-real"`, 1)
	line = strings.Replace(line, `"elapsed_time_seconds":30`, `"elapsed_time_seconds":"thirty"`, 1)
	if got := collectEvents(line); len(got) != 0 {
		t.Fatalf("emitted %d event(s), want consumed without event: %+v", len(got), got)
	}
}
