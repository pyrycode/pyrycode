package streamsup

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// rosterEntryFixture is one synthesized tasks[] entry. Its three fields are
// exactly the per-entry keys the capture shows, and nothing invented.
type rosterEntryFixture struct {
	taskID      string
	taskType    string
	description string
}

// backgroundTaskRosterLineFixture builds a system/background_tasks_changed line.
// It invents NO field structure — the line-level key is the capture's `tasks`
// and the per-entry keys are the capture's three — and exists only to vary the
// COUNT and the VALUES, which is what the two-dimension bound proof needs and
// what the capture cannot supply: the captured line is 212 bytes carrying ONE
// entry with a 9-byte description, far under either bound.
//
// A nil entries slice omits the `tasks` key entirely; an empty non-nil slice
// emits `"tasks":[]`. Those are different inputs, and both are asserted.
func backgroundTaskRosterLineFixture(t *testing.T, entries []rosterEntryFixture) string {
	t.Helper()
	line := map[string]any{
		"type":    "system",
		"subtype": "background_tasks_changed",
	}
	if entries != nil {
		tasks := make([]map[string]string, 0, len(entries))
		for _, e := range entries {
			tasks = append(tasks, map[string]string{
				"task_id":     e.taskID,
				"task_type":   e.taskType,
				"description": e.description,
			})
		}
		line["tasks"] = tasks
	}
	b, err := json.Marshal(line)
	if err != nil {
		t.Fatalf("marshalling background_tasks_changed fixture: %v", err)
	}
	return string(b)
}

// rosterEntriesFixture builds n entries, each identifiable by its task_id so
// tail-truncation is PINNED rather than assumed.
func rosterEntriesFixture(n int) []rosterEntryFixture {
	out := make([]rosterEntryFixture, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, rosterEntryFixture{
			taskID:      fmt.Sprintf("task-%03d", i),
			taskType:    "local_bash",
			description: fmt.Sprintf("cat /tmp/fifo-%03d", i),
		})
	}
	return out
}

// backgroundTaskRosterEvent drives one line through the shipped parser and
// returns the single BackgroundTaskRoster it must emit.
func backgroundTaskRosterEvent(t *testing.T, line string) turnevent.BackgroundTaskRoster {
	t.Helper()
	got := collectEvents(line)
	if len(got) != 1 {
		t.Fatalf("event count: got %d, want 1 (%#v)", len(got), got)
	}
	ev, ok := got[0].(turnevent.BackgroundTaskRoster)
	if !ok {
		t.Fatalf("event type: got %T, want turnevent.BackgroundTaskRoster", got[0])
	}
	return ev
}

// TestParser_BackgroundTaskRosterMapsFromCapture is #1381's central assertion:
// the CAPTURED system/background_tasks_changed line becomes one
// turnevent.BackgroundTaskRoster carrying the tasks array's entries.
//
// This line used to be a row in TestParser_IgnoredLineTypesStaySilent, asserting
// the opposite. Moving it here is the change; #1380 put it there deliberately
// because the subtype was genuinely still dropped until this ticket, and it was
// the LAST captured system subtype on that table.
//
// The field assertions are DERIVED from the capture's own payload rather than
// pinned, for TestParser_TaskStartedMapsFromCapture's reason: the capture is
// redacted (session_id reads $SESSION_ID and the description reads `cat $FIFO`),
// so a pinned expectation risks pinning a placeholder, while a derived one still
// catches a field swap. task_type carries the one pinned literal, as a canary
// that the reader picked the right record at all.
func TestParser_BackgroundTaskRosterMapsFromCapture(t *testing.T) {
	t.Parallel()
	line := capturedSystemLine(t, "background_tasks_changed")

	var payload map[string]any
	if err := json.Unmarshal(line, &payload); err != nil {
		t.Fatalf("decoding the captured payload: %v", err)
	}
	rawTasks, _ := payload["tasks"].([]any)
	if len(rawTasks) != 1 {
		t.Fatalf("the capture's tasks array holds %d entries, want 1", len(rawTasks))
	}
	entry, _ := rawTasks[0].(map[string]any)
	if entry == nil {
		t.Fatalf("the capture's tasks[0] is not an object, so no route can be proven against it")
	}

	ev := backgroundTaskRosterEvent(t, string(line))

	if len(ev.Tasks) != 1 || ev.DroppedTasks != 0 {
		t.Fatalf("roster shape: got %d tasks / %d dropped, want 1 / 0", len(ev.Tasks), ev.DroppedTasks)
	}
	task := ev.Tasks[0]

	routes := []struct {
		field     string
		claudeKey string
		got       string
	}{
		{"TaskID", "task_id", task.TaskID},
		{"TaskType", "task_type", task.TaskType},
		{"Description", "description", task.Description},
	}
	for _, r := range routes {
		want, _ := entry[r.claudeKey].(string)
		if want == "" {
			t.Fatalf("the capture's entry carries no string %q, so the routing of %s cannot be proven against it", r.claudeKey, r.field)
		}
		if r.got != want {
			t.Errorf("%s: got %q, want the capture's %s = %q", r.field, r.got, r.claudeKey, want)
		}
	}

	// The canary: proves the reader selected a background-task record at all
	// rather than some other system line that decodes into the same shape.
	if task.TaskType != "local_bash" {
		t.Errorf("TaskType: got %q, want %q (the capture's one observed value)", task.TaskType, "local_bash")
	}
	if task.TruncatedFields != nil {
		t.Errorf("TruncatedFields: got %v, want nil — the captured line is 212 bytes on the wire, far under every cap", task.TruncatedFields)
	}

	// The cross-record JOIN proof. All three captured records describe one task,
	// which is what makes the family docs' repeated "task_id is the join key"
	// claim falsifiable here rather than decorative — this is the only place it is
	// asserted at all.
	var started map[string]any
	if err := json.Unmarshal(capturedSystemLine(t, "task_started"), &started); err != nil {
		t.Fatalf("decoding the captured task_started payload: %v", err)
	}
	startedID, _ := started["task_id"].(string)
	if startedID == "" {
		t.Fatalf("the captured task_started carries no string task_id, so the join assertion would be vacuous")
	}
	if task.TaskID != startedID {
		t.Errorf("TaskID: the roster's %q does not join back to the captured task_started's %q", task.TaskID, startedID)
	}

	// The two deliberate drops (see BackgroundTaskRoster's doc): claude's
	// session_id is NOT the daemon's conversation identity, and uuid has no reader
	// in the daemon. Neither is even declared on systemBackgroundTasksLine, so
	// this sweep guards the event against a LATER field.
	//
	// The TRAVERSAL is what differs from the siblings' sweeps. Top-level
	// reflection visits ZERO string fields here, because every carried value lives
	// inside a []BackgroundTask — so this descends into a slice-of-struct field
	// and counts at both levels. Both values are read from the decoded payload
	// rather than pinned, so the assertion stays honest if the capture is ever
	// re-taken unredacted.
	var swept int
	var visit func(sv reflect.Value, key, v string)
	visit = func(sv reflect.Value, key, v string) {
		for i := 0; i < sv.NumField(); i++ {
			f := sv.Field(i)
			switch {
			case f.Kind() == reflect.String:
				swept++
				if f.String() == v {
					t.Errorf("field %s carries claude's %s (%q); it is deliberately NOT on this event",
						sv.Type().Field(i).Name, key, v)
				}
			case f.Kind() == reflect.Slice && f.Type().Elem().Kind() == reflect.Struct:
				for j := 0; j < f.Len(); j++ {
					visit(f.Index(j), key, v)
				}
			}
		}
	}
	for _, key := range []string{"session_id", "uuid"} {
		v, _ := payload[key].(string)
		if v == "" {
			t.Fatalf("the capture carries no string %q, so the drop assertion would be vacuous", key)
		}
		visit(reflect.ValueOf(ev), key, v)
	}
	// The floor is a LITERAL, following #1382's correction rather than #1380's
	// len(routes)*2: 1 entry x 3 string fields x 2 dropped keys = 6. It is
	// load-bearing in a way neither sibling's was — a top-level-only sweep visits
	// 0 string fields on this event and would otherwise pass in SILENCE, so this
	// number is exactly the guard that catches "the reflection never descended".
	// Re-taking the capture with more entries only raises the count; a field
	// leaving string kind, or the descent regressing, drops below 6 and goes red.
	const wantSwept = 1 * 3 * 2
	if swept < wantSwept {
		t.Errorf("the drop sweep visited %d string fields, want at least %d — a sweep that never descended into Tasks visits none",
			swept, wantSwept)
	}
}

// TestParser_BackgroundTaskRosterBounds pins the construction-time bound in BOTH
// dimensions. It is applied before the event reaches the sink, so an oversized
// payload never enters the event stream, a queue, or a log.
//
// The capture proves the mapping and cannot prove either bound: one entry, 212
// bytes. So these lines are SYNTHESIZED, which invents no field structure — the
// keys are the capture's, only the count and the values are oversized.
//
// The count dimension is the one with no precedent in this package. A per-entry
// text cap alone leaves the event's total size a function of a number claude
// chooses, which is why both halves are asserted here and why the last subtest
// drives them at once.
func TestParser_BackgroundTaskRosterBounds(t *testing.T) {
	t.Parallel()

	const (
		smallID   = "bybi8g8i8"
		smallType = "local_bash"
		smallDesc = "cat /tmp/fifo"
	)

	t.Run("the entry count is bounded and the overflow is reported", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			name        string
			entries     []rosterEntryFixture
			wantLen     int
			wantDropped int
		}{
			{
				name:    "one over the cap drops one",
				entries: rosterEntriesFixture(taskRosterEntriesCapFixture + 1),
				wantLen: taskRosterEntriesCapFixture, wantDropped: 1,
			},
			{
				// The <= boundary, matching truncateField's convention.
				name:    "exactly at the cap carries every entry",
				entries: rosterEntriesFixture(taskRosterEntriesCapFixture),
				wantLen: taskRosterEntriesCapFixture, wantDropped: 0,
			},
			{
				// The row that makes the report a COUNT rather than a flag: a flag
				// cannot tell 1 lost from 92, and the roster's true size is only
				// recoverable as len(Tasks) + DroppedTasks.
				name:    "a large roster reports how many were lost",
				entries: rosterEntriesFixture(100),
				wantLen: taskRosterEntriesCapFixture, wantDropped: 100 - taskRosterEntriesCapFixture,
			},
			{
				// An empty roster is MEANINGFUL — it says nothing is alive — so it is
				// still emitted. Dropping it would discard the most informative case.
				name:    "an empty tasks array still emits one event",
				entries: []rosterEntryFixture{},
				wantLen: 0, wantDropped: 0,
			},
			{
				// A DIFFERENT input from the empty array: the key is absent entirely.
				name:    "an absent tasks key still emits one event",
				entries: nil,
				wantLen: 0, wantDropped: 0,
			},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				ev := backgroundTaskRosterEvent(t, backgroundTaskRosterLineFixture(t, tc.entries))

				if len(ev.Tasks) != tc.wantLen {
					t.Fatalf("len(Tasks): got %d, want %d", len(ev.Tasks), tc.wantLen)
				}
				if ev.DroppedTasks != tc.wantDropped {
					t.Errorf("DroppedTasks: got %d, want %d", ev.DroppedTasks, tc.wantDropped)
				}
				if tc.wantLen == 0 && ev.Tasks != nil {
					t.Errorf("Tasks: got %#v, want nil — never an empty non-nil slice", ev.Tasks)
				}
				// Truncation is from the TAIL, preserving claude's order: no ranking is
				// invented, because claude's ordering semantics are unobserved. Pinned
				// per entry rather than assumed from the count.
				for i := range ev.Tasks {
					if ev.Tasks[i].TaskID != tc.entries[i].taskID {
						t.Errorf("Tasks[%d].TaskID: got %q, want %q — the survivors are claude's first %d, in order",
							i, ev.Tasks[i].TaskID, tc.entries[i].taskID, tc.wantLen)
					}
				}
			})
		}
	})

	t.Run("each entry's text is bounded and the cut is reported on that entry", func(t *testing.T) {
		t.Parallel()
		overID := strings.Repeat("i", taskFieldIDCapFixture+1)
		overType := strings.Repeat("k", taskFieldIDCapFixture+64)
		overDesc := strings.Repeat("d", taskRosterDescriptionCapFixture+1)

		tests := []struct {
			name               string
			entry              rosterEntryFixture
			wantCut            []string
			wantLenTaskID      int
			wantLenTaskType    int
			wantLenDescription int
		}{
			{
				// The report names the field that was cut, and the other two are both
				// intact and unnamed — a cut must not smear across the entry.
				name:    "description over cap is cut and reported",
				entry:   rosterEntryFixture{taskID: smallID, taskType: smallType, description: overDesc},
				wantCut: []string{"description"}, wantLenTaskID: len(smallID),
				wantLenTaskType: len(smallType), wantLenDescription: taskRosterDescriptionCapFixture,
			},
			{
				name:    "task_id over cap is cut and reported",
				entry:   rosterEntryFixture{taskID: overID, taskType: smallType, description: smallDesc},
				wantCut: []string{"task_id"}, wantLenTaskID: taskFieldIDCapFixture,
				wantLenTaskType: len(smallType), wantLenDescription: len(smallDesc),
			},
			{
				name:    "task_type over cap is cut and reported",
				entry:   rosterEntryFixture{taskID: smallID, taskType: overType, description: smallDesc},
				wantCut: []string{"task_type"}, wantLenTaskID: len(smallID),
				wantLenTaskType: taskFieldIDCapFixture, wantLenDescription: len(smallDesc),
			},
			{
				// All three at once, named in DECLARATION order, which is what makes the
				// value deterministic and pinnable.
				name:    "every field over cap is named in declaration order",
				entry:   rosterEntryFixture{taskID: overID, taskType: overType, description: overDesc},
				wantCut: []string{"task_id", "task_type", "description"}, wantLenTaskID: taskFieldIDCapFixture,
				wantLenTaskType: taskFieldIDCapFixture, wantLenDescription: taskRosterDescriptionCapFixture,
			},
			{
				// The <= boundary: a field of exactly its cap is not truncated, and
				// nothing is reported.
				name: "every field exactly at its cap is not truncated",
				entry: rosterEntryFixture{
					taskID:      strings.Repeat("i", taskFieldIDCapFixture),
					taskType:    strings.Repeat("k", taskFieldIDCapFixture),
					description: strings.Repeat("d", taskRosterDescriptionCapFixture),
				},
				wantCut: nil, wantLenTaskID: taskFieldIDCapFixture,
				wantLenTaskType: taskFieldIDCapFixture, wantLenDescription: taskRosterDescriptionCapFixture,
			},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				ev := backgroundTaskRosterEvent(t, backgroundTaskRosterLineFixture(t, []rosterEntryFixture{tc.entry}))

				if len(ev.Tasks) != 1 {
					t.Fatalf("len(Tasks): got %d, want 1", len(ev.Tasks))
				}
				if ev.DroppedTasks != 0 {
					t.Errorf("DroppedTasks: got %d, want 0 — a text cut must not report a count drop", ev.DroppedTasks)
				}
				task := ev.Tasks[0]
				if !reflect.DeepEqual(task.TruncatedFields, tc.wantCut) {
					t.Errorf("TruncatedFields: got %#v, want %#v", task.TruncatedFields, tc.wantCut)
				}
				lens := []struct {
					field string
					got   int
					want  int
				}{
					{"TaskID", len(task.TaskID), tc.wantLenTaskID},
					{"TaskType", len(task.TaskType), tc.wantLenTaskType},
					{"Description", len(task.Description), tc.wantLenDescription},
				}
				for _, l := range lens {
					if l.got != l.want {
						t.Errorf("len(%s): got %d, want %d", l.field, l.got, l.want)
					}
				}
			})
		}
	})

	t.Run("a cut mid-rune yields valid UTF-8", func(t *testing.T) {
		t.Parallel()
		// The cap is a BYTE slice, so it can land inside a multi-byte rune. The
		// value rides a JSON string field downstream, where an invalid sequence
		// would be silently replaced, so the producer scrubs it here instead.
		desc := strings.Repeat("d", taskRosterDescriptionCapFixture-1) + strings.Repeat("€", 8)
		ev := backgroundTaskRosterEvent(t, backgroundTaskRosterLineFixture(t,
			[]rosterEntryFixture{{taskID: smallID, taskType: smallType, description: desc}}))

		if len(ev.Tasks) != 1 {
			t.Fatalf("len(Tasks): got %d, want 1", len(ev.Tasks))
		}
		task := ev.Tasks[0]
		if !reflect.DeepEqual(task.TruncatedFields, []string{"description"}) {
			t.Fatalf("TruncatedFields: got %#v, want [description]", task.TruncatedFields)
		}
		if !utf8.ValidString(task.Description) {
			t.Errorf("Description is not valid UTF-8 after a mid-rune cut")
		}
		// Short of the cap, not at it: the empty replacement DELETES the partial
		// rune rather than replacing it.
		if len(task.Description) > taskRosterDescriptionCapFixture {
			t.Errorf("len(Description): got %d, want <= %d", len(task.Description), taskRosterDescriptionCapFixture)
		}
	})

	t.Run("both dimensions cut at once and neither report smears into the other", func(t *testing.T) {
		t.Parallel()
		// The concrete evidence for "bounded in BOTH dimensions": an entry inside
		// the SURVIVING prefix carries its own text cut while the roster reports its
		// count drop, and the two reports are independent.
		entries := rosterEntriesFixture(taskRosterEntriesCapFixture + 1)
		entries[0].description = strings.Repeat("d", taskRosterDescriptionCapFixture+1)
		ev := backgroundTaskRosterEvent(t, backgroundTaskRosterLineFixture(t, entries))

		if len(ev.Tasks) != taskRosterEntriesCapFixture {
			t.Fatalf("len(Tasks): got %d, want %d", len(ev.Tasks), taskRosterEntriesCapFixture)
		}
		if ev.DroppedTasks != 1 {
			t.Errorf("DroppedTasks: got %d, want 1", ev.DroppedTasks)
		}
		if !reflect.DeepEqual(ev.Tasks[0].TruncatedFields, []string{"description"}) {
			t.Errorf("Tasks[0].TruncatedFields: got %#v, want [description]", ev.Tasks[0].TruncatedFields)
		}
		if len(ev.Tasks[0].Description) != taskRosterDescriptionCapFixture {
			t.Errorf("len(Tasks[0].Description): got %d, want %d", len(ev.Tasks[0].Description), taskRosterDescriptionCapFixture)
		}
		for i := 1; i < len(ev.Tasks); i++ {
			if ev.Tasks[i].TruncatedFields != nil {
				t.Errorf("Tasks[%d].TruncatedFields: got %#v, want nil — one entry's cut must not smear onto its neighbours",
					i, ev.Tasks[i].TruncatedFields)
			}
		}
	})
}

// TestParser_BackgroundTaskRosterSynthesizesNoTerminalEvent proves the clause the
// capture cannot: the capturing probe deliberately ended its turn with the task
// still ALIVE, so nothing on record shows a task finishing.
//
// A task's disappearance from a later roster is the available finish signal, but
// that transition has never been observed, and the daemon does not report a
// finish it cannot detect. The second line is SYNTHESIZED and invents no field
// structure — the keys are the capture's, only the array contents differ.
func TestParser_BackgroundTaskRosterSynthesizesNoTerminalEvent(t *testing.T) {
	t.Parallel()

	// Both lines go through ONE parser, so a hypothetical cross-line roster diff
	// would have the state it needs to fire. Separate parsers would make the
	// assertion vacuous.
	drive := func(t *testing.T, second string) []turnevent.Event {
		t.Helper()
		var events []turnevent.Event
		p := NewParser(func(ev turnevent.Event) { events = append(events, ev) }, discardLogger())
		captured := capturedSystemLine(t, "background_tasks_changed")
		if _, err := p.Write(append(append([]byte(nil), captured...), '\n')); err != nil {
			t.Fatalf("Write err = %v, want nil", err)
		}
		if len(events) != 1 {
			t.Fatalf("the captured roster emitted %d events, want 1 — this test's premise", len(events))
		}
		before := len(events)
		if _, err := p.Write([]byte(second + "\n")); err != nil {
			t.Fatalf("Write err = %v, want nil", err)
		}
		return events[before:]
	}

	tests := []struct {
		name   string
		second []rosterEntryFixture
	}{
		{
			name:   "the captured task is replaced by a different one",
			second: []rosterEntryFixture{{taskID: "z9kq2f1x0", taskType: "local_bash", description: "sleep 60"}},
		},
		{
			// The shape an implementation is most tempted to special-case into a
			// finish, which is why it is asserted rather than left to the first.
			name:   "the roster goes empty",
			second: []rosterEntryFixture{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := drive(t, backgroundTaskRosterLineFixture(t, tc.second))

			// Asserted on the WHOLE slice rather than on got[0]: a test that inspects
			// only the first event cannot see a finish appended beside it, which is
			// the exact failure this test exists to catch.
			if len(got) != 1 {
				t.Fatalf("the second roster emitted %d events, want exactly 1 — no finish/terminal event may accompany it (%#v)",
					len(got), got)
			}
			ev, ok := got[0].(turnevent.BackgroundTaskRoster)
			if !ok {
				t.Fatalf("event type: got %T, want turnevent.BackgroundTaskRoster", got[0])
			}
			// The array's contents are carried STRUCTURALLY; the daemon infers
			// nothing from the disappearance beyond what the snapshot already says.
			if len(ev.Tasks) != len(tc.second) {
				t.Errorf("len(Tasks): got %d, want %d", len(ev.Tasks), len(tc.second))
			}
			if ev.DroppedTasks != 0 {
				t.Errorf("DroppedTasks: got %d, want 0", ev.DroppedTasks)
			}
		})
	}
}

// TestParser_BackgroundTaskRosterDropIsLoggedContentFree is the package's
// standing rule applied to the third subtype: nothing derived from claude's
// output reaches a log. The content crosses the wire, not the log.
//
// A roster is the most attractive thing to dump while debugging a malformed line
// — it is a list, and lists read as diagnostics — which is why both the success
// and the decode-failure path are driven here. The ENTRY COUNT is covered too:
// it is derived from claude's output as much as the text is, and "just the
// length" is the leak a content-free rule is most often bent for. That half is
// closed by asserting the exact record set rather than by string matching, which
// no count could be searched for reliably.
func TestParser_BackgroundTaskRosterDropIsLoggedContentFree(t *testing.T) {
	t.Parallel()

	captured := capturedSystemLine(t, "background_tasks_changed")
	var payload map[string]any
	if err := json.Unmarshal(captured, &payload); err != nil {
		t.Fatalf("decoding the captured payload: %v", err)
	}
	capturedSession, _ := payload["session_id"].(string)
	capturedUUID, _ := payload["uuid"].(string)
	if capturedSession == "" || capturedUUID == "" {
		t.Fatalf("the capture carries no string session_id/uuid, so the leak sweep would be vacuous")
	}

	// A line the shape cannot decode (tasks is an object, not an array), so the
	// decode-failure path runs. Its description is a distinctive literal, and the
	// whole point is that it must appear in no log record.
	const malformedDesc = "rm -rf /tmp/never-log-this-roster"
	malformed := `{"type":"system","subtype":"background_tasks_changed",` +
		`"tasks":{"task_id":"x","task_type":"local_bash","description":"` + malformedDesc + `"}}`

	rec := &logRecorder{}
	var events []turnevent.Event
	p := NewParser(func(ev turnevent.Event) { events = append(events, ev) }, slog.New(rec))
	if _, err := p.Write(append(append([]byte(nil), captured...), '\n')); err != nil {
		t.Fatalf("Write err = %v, want nil", err)
	}
	if _, err := p.Write([]byte(malformed + "\n")); err != nil {
		t.Fatalf("Write err = %v, want nil", err)
	}

	// The malformed line is dropped, and NOT as an Unrecognized: keeping `system`
	// whole on ignoredLineTypes is what makes "no system line reaches the
	// unrecognized lane" structural, and that guarantee outranks surfacing a
	// malformed line of a subtype we already know.
	if len(events) != 1 {
		t.Fatalf("event count: got %d, want 1 (the captured line only) — %#v", len(events), events)
	}
	ev, ok := events[0].(turnevent.BackgroundTaskRoster)
	if !ok {
		t.Fatalf("event type: got %T, want turnevent.BackgroundTaskRoster", events[0])
	}
	// The description as the parser itself carried it, so the leak candidate is
	// derived from the shipped mapping rather than transcribed.
	if len(ev.Tasks) != 1 || ev.Tasks[0].Description == "" {
		t.Fatalf("the mapped roster carries no description, so the leak sweep over it would be vacuous")
	}

	// Exactly one record, and it is the decode-failure drop carrying the subtype
	// KEYWORD only. This is the assertion that closes the count leak: nothing else
	// is logged on either path, so no entry count can ride along.
	const dropMsg = "streamsup: dropping undecodable system line"
	all := rec.all()
	if len(all) != 1 {
		t.Fatalf("log records: got %d, want exactly 1 (the undecodable drop) — %+v", len(all), all)
	}
	if all[0].msg != dropMsg {
		t.Fatalf("record message: got %q, want %q — the decode-failure path never ran, so this sweep would be vacuous", all[0].msg, dropMsg)
	}
	if !reflect.DeepEqual(all[0].attrs, map[string]string{"subtype": "background_tasks_changed"}) {
		t.Errorf("the undecodable-drop record carries %#v, want the subtype keyword only", all[0].attrs)
	}

	for _, r := range all {
		for _, leak := range []string{ev.Tasks[0].Description, malformedDesc, capturedSession, capturedUUID} {
			if strings.Contains(r.msg, leak) {
				t.Errorf("record message carries claude-derived content: %q", r.msg)
			}
			for k, v := range r.attrs {
				if strings.Contains(v, leak) {
					t.Errorf("record %q attr %q carries claude-derived content; this path logs the subtype only", r.msg, k)
				}
			}
		}
	}
}
