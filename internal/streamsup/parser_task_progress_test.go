package streamsup

import (
	"encoding/json"
	"log/slog"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// taskProgressEvents filters a driven event stream down to the progress variant.
// The other variants are not asserted away here: a line replay that also produced,
// say, a TurnEnd is legitimate, and the rate assertions are about how many PROGRESS
// events fell out.
func taskProgressEvents(evs []turnevent.Event) []turnevent.BackgroundTaskProgress {
	var out []turnevent.BackgroundTaskProgress
	for _, ev := range evs {
		if p, ok := ev.(turnevent.BackgroundTaskProgress); ok {
			out = append(out, p)
		}
	}
	return out
}

// taskProgressLineFixture builds a system/task_progress line from the mapped keys
// plus the nested usage object, in the capture's own shape.
//
// SYNTHESIZED LINES ARE FOR THE RULE, NEVER FOR THE FIELD SET. The mapping itself is
// proven against the committed capture in TestParser_TaskProgressMapsFromCapture;
// what needs synthesis is the counter SEQUENCES a two-line record cannot exhibit —
// a backwards counter, an extreme one, nine concurrent tasks. Building those from
// claude's bytes is impossible, and building the field set from a literal would be
// declaring a shape from an author's expectation, which is the move this family
// refuses.
func taskProgressLineFixture(taskID, description, subagentType, lastToolName string, totalTokens, toolUses, durationMS int) string {
	payload := map[string]any{
		"type":           "system",
		"subtype":        "task_progress",
		"task_id":        taskID,
		"description":    description,
		"subagent_type":  subagentType,
		"last_tool_name": lastToolName,
		"usage": map[string]any{
			"total_tokens": totalTokens,
			"tool_uses":    toolUses,
			"duration_ms":  durationMS,
		},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		panic("building a task_progress fixture: " + err.Error())
	}
	return string(b)
}

// taskProgressToolUseLine is the fixture reduced to the one field the rate bound
// reads, for the rule tables where every other field is noise. The task id stays a
// parameter because the bound is keyed on it.
func taskProgressToolUseLine(taskID string, toolUses int) string {
	return taskProgressLineFixture(taskID, "d", "general-purpose", "Read", 100, toolUses, 10)
}

// TestParser_TaskProgressMapsFromCapture is #2246's central assertion and carries
// AC1, AC2 and AC4 together on claude's own bytes.
//
// AC1: replaying a captured system/task_progress line produces a bounded progress
// report carrying the task id and that line's counters, and no Unrecognized.
// AC2: the capture holds TWO lines for one task and they produce ONE event, so the
// first accumulated and emitted nothing.
// AC4: the emitted counters are the SECOND line's own, not a sum of both.
//
// The field assertions are DERIVED from the emitting line's payload rather than
// pinned, for TestParser_TaskNotificationMapsFromCapture's reason: the capture is
// redacted, so a pinned expectation risks pinning a placeholder while a derived one
// still catches a field swap. The counters carry the pinned literals instead —
// they are the values the rate rule is argued from, they are not redactable, and
// pinning them is what makes the AC4 assertion below discriminating rather than
// tautological.
func TestParser_TaskProgressMapsFromCapture(t *testing.T) {
	t.Parallel()
	lines := capturedTaskProgressLines(t)
	if len(lines) != 2 {
		t.Fatalf("the capture holds %d task_progress lines, want 2 — #2246's rate argument and every "+
			"count below rest on the two-line staging; a record with a different count needs the "+
			"argument re-derived, not the assertion relaxed", len(lines))
	}

	strs := make([]string, len(lines))
	for i, l := range lines {
		strs[i] = string(l)
	}
	evs := collectEventsFromLines(strs)
	for _, ev := range evs {
		if un, ok := ev.(turnevent.Unrecognized); ok {
			t.Fatalf("a captured task_progress line reached the unrecognized lane as %q — keeping "+
				"system whole on ignoredLineTypes is what makes that impossible", un.Kind)
		}
	}
	got := taskProgressEvents(evs)
	if len(got) != 1 {
		t.Fatalf("progress events: got %d, want 1 — the capture's two lines advance tool_uses by one "+
			"each, so at minTaskToolCallsPerEvent the first accumulates and the second emits (%#v)", len(got), got)
	}
	ev := got[0]

	// The EMITTING line is the second one. Deriving the expectation from it rather
	// than from the first is what makes this AC4 rather than a shape check: an
	// implementation that accumulated across lines, or that published the first
	// line's values on the second line's event, fails here.
	var payload map[string]any
	if err := json.Unmarshal(lines[1], &payload); err != nil {
		t.Fatalf("decoding the emitting line's payload: %v", err)
	}
	for _, r := range []struct {
		field     string
		claudeKey string
		got       string
	}{
		{"TaskID", "task_id", ev.TaskID},
		{"Description", "description", ev.Description},
		{"SubagentType", "subagent_type", ev.SubagentType},
		{"LastToolName", "last_tool_name", ev.LastToolName},
	} {
		want, _ := payload[r.claudeKey].(string)
		if want == "" {
			t.Fatalf("the capture carries no string %q, so the routing of %s cannot be proven against it",
				r.claudeKey, r.field)
		}
		if r.got != want {
			t.Errorf("%s: got %q, want the emitting line's %s = %q", r.field, r.got, r.claudeKey, want)
		}
	}

	// AC4, and the reason the counters are pinned rather than derived. The two
	// captured lines carry DIFFERENT values in all three, so a parser that summed
	// them, or that carried the first line's forward, produces numbers that are not
	// these. A derived assertion against the emitting line would pass for the sum
	// only by coincidence; these literals refuse it outright.
	for _, c := range []struct {
		field string
		got   int
		want  int
	}{
		{"TotalTokens", ev.TotalTokens, 16246},
		{"ToolUses", ev.ToolUses, 2},
		{"DurationMS", ev.DurationMS, 4546},
	} {
		if c.got != c.want {
			t.Errorf("%s: got %d, want %d — the emitting line's own reading, never a total the parser "+
				"accumulated across lines", c.field, c.got, c.want)
		}
	}
	// The canary: proves the reader selected task_progress frames rather than some
	// other system line decoding into the same shape. tool_uses reading 2 on the
	// second of two lines is the tool-call relationship the whole rate argument rests
	// on, so if it ever stops holding, the bound needs re-deriving.
	if ev.ToolUses != len(lines) {
		t.Errorf("ToolUses is %d across %d captured lines; the bound is argued from the counter "+
			"tracking the line count 1:1 on this staging", ev.ToolUses, len(lines))
	}
	if ev.TruncatedFields != nil {
		t.Errorf("TruncatedFields: got %v, want nil — the captured lines are far under every cap",
			ev.TruncatedFields)
	}
}

// TestParser_TaskProgressRateRule is AC2 as a table over the counter sequences a
// two-line capture cannot exhibit. Each case names the tool_uses values claude sends
// for ONE task, in order, and the indices of the lines that must emit.
//
// The expectations are POSITIONAL rather than counts, following
// replayThinkingRuleAt's shape: a count alone would pass for a rule that emitted the
// right number of events at the wrong lines, which is exactly what a rate bound gets
// wrong.
func TestParser_TaskProgressRateRule(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		toolUses []int
		wantAt   []int
	}{
		{
			// The captured shape: one tool call per line, so every other line emits.
			name:     "one advance per line emits at every second line",
			toolUses: []int{1, 2, 3, 4, 5, 6},
			wantAt:   []int{1, 3, 5},
		},
		{
			// A task whose first progress line already reports a full quantum emits at
			// once: prev is 0 for an untracked task, so the bound is crossed on arrival.
			name:     "a first line at the bound emits immediately",
			toolUses: []int{2},
			wantAt:   []int{0},
		},
		{
			name:     "a first line below the bound emits nothing",
			toolUses: []int{1},
			wantAt:   nil,
		},
		{
			// The property the bound is NOT: it gates the counter's advance, not the
			// line count, so a doubled advance emits on every line. Recorded here as a
			// measured behaviour rather than left for someone to discover — see
			// minTaskToolCallsPerEvent's own statement of the same limit.
			name:     "an advance of two per line emits on every line",
			toolUses: []int{2, 4, 6},
			wantAt:   []int{0, 1, 2},
		},
		{
			// Absent, zero and negative are one reading: nothing to report, nothing
			// remembered. The advance that follows is measured from the last EMITTED
			// value and not from the refused one.
			name:     "a non-positive counter is refused and does not disturb the next advance",
			toolUses: []int{2, 0, -5, 3, 4},
			wantAt:   []int{0, 4},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			lines := make([]string, len(tc.toolUses))
			for i, n := range tc.toolUses {
				// The description carries the line's index so the emitted events can be
				// matched back to the lines that produced them — the positional
				// assertion below is what a bare count could not make.
				lines[i] = taskProgressLineFixture("t", "line-"+strconv.Itoa(i), "general-purpose", "Read", 100, n, 10)
			}
			var gotAt []int
			for _, ev := range taskProgressEvents(collectEventsFromLines(lines)) {
				idx, err := strconv.Atoi(strings.TrimPrefix(ev.Description, "line-"))
				if err != nil {
					t.Fatalf("an event's Description is %q, which does not name a line index: %v", ev.Description, err)
				}
				gotAt = append(gotAt, idx)
			}
			if !slices.Equal(gotAt, tc.wantAt) {
				t.Errorf("lines that emitted: got %v, want %v (tool_uses %v at bound %d)",
					gotAt, tc.wantAt, tc.toolUses, taskToolCallsPerEventFixture)
			}
		})
	}
}

// TestParser_TaskProgressBoundIsPerTask proves the bound is keyed rather than
// turn-wide, which is the departure from minThinkingTokensPerEvent that forced a map.
//
// Interleaved so a turn-wide counter is FORCED to differ: two tasks alternate, each
// advancing by one per line. Under a single shared counter the combined advance
// reaches the bound on every second LINE regardless of which task sent it, so both
// tasks would emit early and often. Keyed, each task's own advance is what counts.
func TestParser_TaskProgressBoundIsPerTask(t *testing.T) {
	t.Parallel()
	lines := []string{
		taskProgressToolUseLine("alpha", 1),
		taskProgressToolUseLine("beta", 1),
		taskProgressToolUseLine("alpha", 2),
		taskProgressToolUseLine("beta", 2),
	}
	got := taskProgressEvents(collectEventsFromLines(lines))
	if len(got) != 2 {
		t.Fatalf("progress events: got %d, want 2 — one per task, each on its own second line (%#v)", len(got), got)
	}
	for i, want := range []string{"alpha", "beta"} {
		if got[i].TaskID != want {
			t.Errorf("event %d TaskID: got %q, want %q — a task's own counter decides its own emits",
				i, got[i].TaskID, want)
		}
		if got[i].ToolUses != 2 {
			t.Errorf("event %d ToolUses: got %d, want 2", i, got[i].ToolUses)
		}
	}
}

// TestParser_TaskProgressBackwardsCounterCannotSilenceATask covers the case claude
// demonstrably produces on the neighbouring subtype — a cumulative counter restarting
// — and the design decision it forced.
//
// THE DISCRIMINATING ASSERTION IS THE SECOND EVENT, not the first, following
// TestParser_ThinkingAccumulatorSurvivesAnExtremeDelta's shape. An implementation
// that ignored a backwards counter instead of re-baselining still emits the first
// event exactly as this one does; it is only the emit AFTER the restart that
// separates them, because the stale high-water mark is what no realistic advance
// climbs out of.
func TestParser_TaskProgressBackwardsCounterCannotSilenceATask(t *testing.T) {
	t.Parallel()
	lines := []string{
		taskProgressToolUseLine("t", 10), // emits: 10 - 0 crosses the bound
		taskProgressToolUseLine("t", 1),  // the restart: re-baselines, emits nothing
		taskProgressToolUseLine("t", 2),  // one advance past the new baseline
		taskProgressToolUseLine("t", 3),  // two: emits, and this is the assertion
	}
	got := taskProgressEvents(collectEventsFromLines(lines))
	if len(got) != 2 {
		t.Fatalf("progress events: got %d, want 2 — an implementation that ignored the restart instead "+
			"of re-baselining emits only the first, because a stale high-water mark of 10 silences "+
			"every advance a restarted counter can make (%#v)", len(got), got)
	}
	if got[1].ToolUses != 3 {
		t.Errorf("the second event's ToolUses: got %d, want 3 — the emit after the restart is what "+
			"this test discriminates on", got[1].ToolUses)
	}
}

// TestParser_TaskProgressSurvivesAnExtremeCounter is the overflow pin, and it is the
// obvious refactor's failure made observable.
//
// The crossing test must stay SUBTRACTED. Its additive form, `prev + bound <= seen`,
// is arithmetically identical for every value that fits and overflows for one that
// does not, and the retained operand here is the daemon's own rather than one claude
// supplies fresh on each line. Its DISCRIMINATING assertion is again the event after
// the extreme line: an implementation that let a huge counter stick would emit the
// first two and then nothing, because no later value can exceed it.
func TestParser_TaskProgressSurvivesAnExtremeCounter(t *testing.T) {
	t.Parallel()
	// Built through the fixture's int rather than as a literal, so the value is the
	// platform's own maximum and the test cannot drift from it.
	const extreme = math.MaxInt
	lines := []string{
		taskProgressToolUseLine("t", 1),
		taskProgressToolUseLine("t", 2),       // emits
		taskProgressToolUseLine("t", extreme), // emits, and parks the remembered value at the maximum
		taskProgressToolUseLine("t", 3),       // re-baselines off the maximum
		taskProgressToolUseLine("t", 4),
		taskProgressToolUseLine("t", 5), // emits — the assertion
	}
	got := taskProgressEvents(collectEventsFromLines(lines))
	if len(got) != 3 {
		t.Fatalf("progress events: got %d, want 3 — the third is the one after the extreme line, and "+
			"a parser that could not climb back off MaxInt emits only two (%#v)", len(got), got)
	}
	if got[2].ToolUses != 5 {
		t.Errorf("the third event's ToolUses: got %d, want 5 — the emit after the extreme counter is "+
			"what this test discriminates on", got[2].ToolUses)
	}
}

// TestParser_TaskProgressStateIsBoundedAndReleased is AC3 in all three dimensions.
//
// CARDINALITY: a turn carrying more task ids than the cap tracks maxTaskProgressTasks
// of them and consumes the rest content-free.
// PER-KEY SIZE: the map key is the id the event PUBLISHED, after maxTaskFieldID's cut,
// so two ids differing only past the cap collapse onto one key. That is asserted
// through behaviour rather than by reaching into parser state: a second id sharing a
// truncated prefix inherits the first's counter and therefore does not emit.
// RELEASE: a `result` line clears the map, so a task the cap had shut out is tracked
// again in the next turn.
func TestParser_TaskProgressStateIsBoundedAndReleased(t *testing.T) {
	t.Parallel()

	t.Run("cardinality and release", func(t *testing.T) {
		t.Parallel()
		// Every task sends a full quantum on its first line, so each one emits unless
		// the cap shut it out — which makes the event count read the map's capacity
		// directly rather than the rate rule.
		var lines []string
		for i := 0; i <= taskProgressTasksCapFixture; i++ {
			lines = append(lines, taskProgressToolUseLine("task-"+strconv.Itoa(i), taskToolCallsPerEventFixture))
		}
		overflowID := "task-" + strconv.Itoa(taskProgressTasksCapFixture)

		got := taskProgressEvents(collectEventsFromLines(lines))
		if len(got) != taskProgressTasksCapFixture {
			t.Fatalf("progress events: got %d, want %d — the %dth task is past the cap and must be "+
				"consumed silently, not emitted (%#v)", len(got), taskProgressTasksCapFixture, len(lines), got)
		}
		for _, ev := range got {
			if ev.TaskID == overflowID {
				t.Errorf("the task past the cap emitted; growth must stop at maxTaskProgressTasks")
			}
		}

		// RELEASE: the same overflowed task, after a turn boundary, is tracked again.
		withReset := append(append([]string{}, lines...),
			`{"type":"result","subtype":"success"}`,
			taskProgressToolUseLine(overflowID, taskToolCallsPerEventFixture),
		)
		after := taskProgressEvents(collectEventsFromLines(withReset))
		if len(after) != taskProgressTasksCapFixture+1 {
			t.Fatalf("progress events across the boundary: got %d, want %d — the `result` arm's reset "+
				"is what releases the map, and without it the shut-out task stays shut out forever (%#v)",
				len(after), taskProgressTasksCapFixture+1, after)
		}
		if last := after[len(after)-1]; last.TaskID != overflowID {
			t.Errorf("the event after the boundary is for %q, want %q", last.TaskID, overflowID)
		}
	})

	t.Run("per-key size", func(t *testing.T) {
		t.Parallel()
		// Two ids identical for maxTaskFieldID bytes and different past it. Both are
		// cut to the same key, so the second inherits the first's counter.
		shared := strings.Repeat("a", taskFieldIDCapFixture)
		first, second := shared+"-first", shared+"-second"
		got := taskProgressEvents(collectEventsFromLines([]string{
			taskProgressToolUseLine(first, 2),  // emits; remembers the CUT id
			taskProgressToolUseLine(second, 3), // 3 - 2 is under the bound: silent, because the key is shared
		}))
		if len(got) != 1 {
			t.Fatalf("progress events: got %d, want 1 — two ids differing only past maxTaskFieldID must "+
				"share one map key, so an id too long to publish is also too long to remember (%#v)",
				len(got), got)
		}
		if got[0].TaskID != shared {
			t.Errorf("TaskID: got a %d-byte id, want the %d-byte cut one — the key is the value the "+
				"event published", len(got[0].TaskID), len(shared))
		}
		if !slices.Contains(got[0].TruncatedFields, "task_id") {
			t.Errorf("TruncatedFields: got %v, want it to name task_id", got[0].TruncatedFields)
		}
	})
}

// TestParser_TaskProgressCaps proves each claude-authored string is cut at its own
// cap and named in TruncatedFields in DECLARATION ORDER.
//
// The over-long values are built per field rather than all at once, so a cap wired to
// the wrong field is caught by the name in the list rather than only by a length.
func TestParser_TaskProgressCaps(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		build func() string
		field string
		limit int
	}{
		{"task_id", func() string {
			return taskProgressLineFixture(strings.Repeat("i", taskFieldIDCapFixture+1), "d", "s", "l", 1, 2, 3)
		}, "task_id", taskFieldIDCapFixture},
		{"description", func() string {
			return taskProgressLineFixture("t", strings.Repeat("d", taskDescriptionCapFixture+1), "s", "l", 1, 2, 3)
		}, "description", taskDescriptionCapFixture},
		{"subagent_type", func() string {
			return taskProgressLineFixture("t", "d", strings.Repeat("s", taskFieldIDCapFixture+1), "l", 1, 2, 3)
		}, "subagent_type", taskFieldIDCapFixture},
		{"last_tool_name", func() string {
			return taskProgressLineFixture("t", "d", "s", strings.Repeat("l", taskFieldIDCapFixture+1), 1, 2, 3)
		}, "last_tool_name", taskFieldIDCapFixture},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := taskProgressEvents(collectEventsFromLines([]string{tc.build()}))
			if len(got) != 1 {
				t.Fatalf("progress events: got %d, want 1", len(got))
			}
			if !slices.Equal(got[0].TruncatedFields, []string{tc.field}) {
				t.Errorf("TruncatedFields: got %v, want exactly [%s]", got[0].TruncatedFields, tc.field)
			}
			for _, f := range []struct {
				name  string
				value string
			}{
				{"TaskID", got[0].TaskID},
				{"Description", got[0].Description},
				{"SubagentType", got[0].SubagentType},
				{"LastToolName", got[0].LastToolName},
			} {
				if len(f.value) > tc.limit && len(f.value) > taskFieldIDCapFixture {
					t.Errorf("%s is %d bytes, past every cap this line's fields carry", f.name, len(f.value))
				}
			}
		})
	}

	t.Run("declaration order", func(t *testing.T) {
		t.Parallel()
		line := taskProgressLineFixture(
			strings.Repeat("i", taskFieldIDCapFixture+1),
			strings.Repeat("d", taskDescriptionCapFixture+1),
			strings.Repeat("s", taskFieldIDCapFixture+1),
			strings.Repeat("l", taskFieldIDCapFixture+1),
			1, 2, 3)
		got := taskProgressEvents(collectEventsFromLines([]string{line}))
		if len(got) != 1 {
			t.Fatalf("progress events: got %d, want 1", len(got))
		}
		want := []string{"task_id", "description", "subagent_type", "last_tool_name"}
		if !slices.Equal(got[0].TruncatedFields, want) {
			t.Errorf("TruncatedFields: got %v, want %v — the order is the sequence of bound() calls, "+
				"which is why they are separate statements rather than a composite literal",
				got[0].TruncatedFields, want)
		}
	})
}

// TestParser_TaskProgressDropsClaudesUnmappedKeys is the family's standing-omission
// assertion, made at TWO levels because either alone is weaker than it looks.
//
// The STRUCTURAL half is the guarantee: systemTaskProgressLine declares no field
// whose json tag is session_id, uuid, tool_use_id or summary, so no code path in the
// daemon ever holds one. A field that is never declared cannot leak, which is
// stronger than any sweep of an emitted value — and it is the only half that says
// anything at all about summary, whose value the capture does not carry.
//
// The VALUE half sweeps every string field of the emitted event for the captured
// values of the three keys that ARE present, by reflection, so a field added later is
// covered without anyone remembering to extend a list.
func TestParser_TaskProgressDropsClaudesUnmappedKeys(t *testing.T) {
	t.Parallel()

	t.Run("structural", func(t *testing.T) {
		t.Parallel()
		declared := map[string]bool{}
		lt := reflect.TypeOf(systemTaskProgressLine{})
		for i := range lt.NumField() {
			declared[strings.Split(lt.Field(i).Tag.Get("json"), ",")[0]] = true
		}
		for _, key := range []string{"session_id", "uuid", "tool_use_id", "summary"} {
			if declared[key] {
				t.Errorf("systemTaskProgressLine declares %q. Absence from the DECODE TARGET is the "+
					"guarantee — see turnevent.BackgroundTaskUpdated's omission set for the first "+
					"three, and this variant's doc for why summary is a measurement rather than a gap",
					key)
			}
		}
	})

	t.Run("values", func(t *testing.T) {
		t.Parallel()
		lines := capturedTaskProgressLines(t)
		got := taskProgressEvents(collectEventsFromLines([]string{string(lines[0]), string(lines[1])}))
		if len(got) != 1 {
			t.Fatalf("progress events: got %d, want 1", len(got))
		}
		var payload map[string]any
		if err := json.Unmarshal(lines[1], &payload); err != nil {
			t.Fatalf("decoding the emitting line: %v", err)
		}

		ev := reflect.ValueOf(got[0])
		for _, key := range []string{"session_id", "uuid", "tool_use_id"} {
			unmapped, _ := payload[key].(string)
			if unmapped == "" {
				t.Fatalf("the capture carries no string %q, so sweeping for its value would match every "+
					"empty field and prove nothing", key)
			}
			for i := range ev.NumField() {
				f := ev.Field(i)
				if f.Kind() == reflect.String && f.String() == unmapped {
					t.Errorf("%s carries claude's %s. That key is a standing omission of this family",
						ev.Type().Field(i).Name, key)
				}
			}
		}
	})
}

// TestParser_TaskProgressDropIsLoggedContentFree pins the package's standing rule at
// the site it is most tempting to break. The subtype keyword is a message NAME, the
// same class as sl.Type in the neighbouring drop logs; a task id, a counter or a
// description is content, and description here names a file the subagent is reading.
//
// The SILENT branches are swept too, and that is the half a copy of the neighbouring
// tests would miss: four of this arm's five rejects log nothing at all, and a drop
// with no diagnostic is where "just the task id" gets added later.
//
// It sweeps CAPTURED RECORDS rather than a rendered handler dump, and that is #2472
// rather than a style preference. A TextHandler opens every record with its own
// `time=`, a rendered millisecond field holds two consecutive nines often enough to
// fail 3.5% of runs (.991, .399, .099), and the counter token is a bare "99" — so the
// assertion fired on the handler's framing, never on anything the parser wrote. The
// forbidden set is what must NOT give: dropping the counter would end the flake and
// quietly lose a third of the coverage, so it is the SURFACE that narrows, to what the
// parser actually logged. Do not put a rendering handler back.
//
// Attr KEYS are swept alongside values, which the dump covered for free and the
// mirrored TestParser_HarnessNudgeDropIsLoggedContentFree does not: p.log.Debug(msg,
// tl.TaskID, "x") is the one shape that lands claude's text in a key.
func TestParser_TaskProgressDropIsLoggedContentFree(t *testing.T) {
	t.Parallel()
	const secretID = "task-id-that-must-not-be-logged"
	const secretDescription = "Reading /Users/somebody/secrets.txt"
	// The fixture's usage.total_tokens, named so the forbidden token below is derived
	// from the value the rows actually send rather than re-typed beside them. A
	// hand-written "99" goes stale in silence the day this number changes, leaving a
	// guard that scans for a counter no row carries.
	const counterFixture = 99

	tests := []struct {
		name string
		line string
		// wantDrops is the positive control. Without it a recorder that captured
		// nothing — mis-wired, or a handler that stopped being Enabled at Debug —
		// leaves the sweep below scanning an empty slice, and the subtest passes for
		// the worst available reason. Only the undecodable arm logs; the other two
		// rows are two of the four silent rejects.
		wantDrops int
	}{
		{"undecodable", `{"type":"system","subtype":"task_progress","task_id":` +
			strconv.Quote(secretID) + `,"description":` + strconv.Quote(secretDescription) +
			`,"usage":"not-an-object"}`, 1},
		{"non-positive counter", taskProgressLineFixture(secretID, secretDescription, "s", "l", counterFixture, 0, 3), 0},
		{"below the bound", taskProgressLineFixture(secretID, secretDescription, "s", "l", counterFixture, 1, 3), 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := &logRecorder{}
			p := NewParser(func(turnevent.Event) {}, slog.New(rec))
			if _, err := p.Write([]byte(tc.line + "\n")); err != nil {
				t.Fatalf("Write err = %v, want nil", err)
			}

			if drops := rec.withMessage(undecodableSystemLineMsgFixture); len(drops) != tc.wantDrops {
				t.Errorf("records with message %q: got %d, want %d (all records: %+v)",
					undecodableSystemLineMsgFixture, len(drops), tc.wantDrops, rec.all())
			}
			forbidden := []string{secretID, secretDescription, strconv.Itoa(counterFixture)}
			for _, r := range rec.all() {
				for _, f := range forbidden {
					if strings.Contains(r.msg, f) {
						t.Errorf("a record message carries %q. Nothing derived from claude's output "+
							"reaches a log in this package: %q", f, r.msg)
					}
					for k, v := range r.attrs {
						if strings.Contains(k, f) || strings.Contains(v, f) {
							t.Errorf("record %q attr %q=%q carries %q. Nothing derived from claude's "+
								"output reaches a log in this package", r.msg, k, v, f)
						}
					}
				}
			}
		})
	}
}
