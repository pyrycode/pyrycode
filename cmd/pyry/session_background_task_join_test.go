package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnbridge"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

type joinedRosterRunner struct {
	stubRunner
	hold *sessionBackgroundTaskHold
}

func (r joinedRosterRunner) BackgroundTaskRoster() (turnevent.BackgroundTaskRoster, bool) {
	return r.hold.BackgroundTaskRoster()
}

func TestBackgroundTaskJoin_CaptureOrders(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		file, key, id string
		startFirst    bool
	}{
		{"dropped_lines_v2.1.220.json", "dropped_lines", "toolu_01ENMNP5P4d3pqjTg9LgCxgZ", true},
		{"roster_after_finish_v2.1.280.json", "frames", "toolu_01CX7tQrqBqfeSv8Aik9zPtT", false},
	} {
		t.Run(tc.file, func(t *testing.T) {
			t.Parallel()
			raw, err := os.ReadFile(filepath.Join("../../internal/e2e/realclaude/testdata", tc.file))
			if err != nil {
				t.Fatal(err)
			}
			var capture map[string]json.RawMessage
			if err := json.Unmarshal(raw, &capture); err != nil {
				t.Fatal(err)
			}
			var frames []struct{ Subtype, Payload string }
			if err := json.Unmarshal(capture[tc.key], &frames); err != nil {
				t.Fatal(err)
			}
			var seen recordingSink
			parser, held := newSessionParser(seen.sink, discardLogger())
			pool, err := sessions.New(sessions.Config{Bootstrap: sessions.SessionConfig{ClaudeBin: os.Args[0]}, RegistryPath: filepath.Join(t.TempDir(), "sessions.json"), RunnerFactory: func(sessions.RunnerConfig) (sessions.Runner, error) { return joinedRosterRunner{hold: held.tasks}, nil }})
			if err != nil {
				t.Fatal(err)
			}
			reg := &conversations.Registry{}
			reg.Create(conversations.Conversation{ID: "conv", CurrentSessionID: string(pool.BootstrapID())})
			count := 0
			for _, frame := range frames {
				if frame.Subtype != "task_started" && frame.Subtype != "background_tasks_changed" {
					continue
				}
				if _, err := parser.Write([]byte(frame.Payload + "\n")); err != nil {
					t.Fatal(err)
				}
				count++
				if count == 2 {
					break
				}
			}
			events := seen.events()
			if len(events) != 2 {
				t.Fatalf("events = %d, want 2", len(events))
			}
			rosterIndex := 0
			if tc.startFirst {
				rosterIndex = 1
			}
			roster := events[rosterIndex].(turnevent.BackgroundTaskRoster)
			wantLive := ""
			if tc.startFirst {
				wantLive = tc.id
			}
			_, mapped, _ := turnbridge.MapEvent(roster, turnbridge.TurnContext{ConversationID: "conv"})
			if got := mapped.(protocol.BackgroundTaskRosterPayload).Tasks[0].ToolCallID; got != wantLive {
				t.Fatalf("live id = %q, want %q", got, wantLive)
			}
			start := events[1-rosterIndex].(turnevent.BackgroundTaskStarted)
			if start.ToolCallID != tc.id {
				t.Fatalf("forwarded start = %#v", start)
			}
			got, ok := resolveBoundBackgroundTaskRoster(reg, pool, "conv")
			if !ok || got.Tasks[0].ToolCallID != tc.id {
				t.Fatalf("connect roster = %#v, ok=%v", got, ok)
			}
		})
	}
}

func TestBackgroundTaskJoin_TruncationAndCopies(t *testing.T) {
	t.Parallel()
	var seen recordingSink
	parser, held := newSessionParser(seen.sink, discardLogger())
	line := fmt.Sprintf(`{"type":"system","subtype":"task_started","task_id":"a","tool_use_id":%q}`, strings.Repeat("x", 257))
	if _, err := parser.Write([]byte(line + "\n")); err != nil {
		t.Fatal(err)
	}
	held.tasks.Sink(holdBackgroundTaskRosterFixture("a"))
	start := seen.events()[0].(turnevent.BackgroundTaskStarted)
	want := []string{"description", "tool_call_id"}
	for i := 0; i < 3; i++ {
		got, _ := held.tasks.BackgroundTaskRoster()
		if len(got.Tasks[0].ToolCallID) != 256 || got.Tasks[0].ToolCallID != start.ToolCallID || !reflect.DeepEqual(got.Tasks[0].TruncatedFields, want) {
			t.Fatalf("row = %#v", got.Tasks[0])
		}
		got.Tasks[0].ToolCallID = "mutated"
		got.Tasks[0].TruncatedFields[0] = "mutated"
	}
	live := seen.events()[1].(turnevent.BackgroundTaskRoster)
	live.Tasks[0].TruncatedFields[0] = "live-mutated"
	held.tasks.childExited()
	got, _ := held.tasks.BackgroundTaskRoster()
	if got.Tasks[0].ToolCallID != "" || !reflect.DeepEqual(got.Tasks[0].TruncatedFields, []string{"description"}) {
		t.Fatalf("stale annotations: %#v", got)
	}
}

func TestBackgroundTaskJoin_PruneOverflowIsolation(t *testing.T) {
	t.Parallel()
	h := newSessionBackgroundTaskHold(nil)
	ids := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	h.Sink(holdBackgroundTaskRosterFixture(ids...))
	for _, id := range ids {
		h.Sink(turnevent.BackgroundTaskStarted{TaskID: id, ToolCallID: "tool-" + id})
	}
	for i := 0; i < maxBackgroundTaskJoins; i++ {
		h.Sink(turnevent.BackgroundTaskStarted{TaskID: fmt.Sprint(i), ToolCallID: fmt.Sprint(i), TruncatedFields: []string{"tool_call_id"}})
	}
	if len(h.joins) != maxBackgroundTaskJoins {
		t.Fatalf("joins = %d", len(h.joins))
	}
	roster, _ := h.BackgroundTaskRoster()
	for _, row := range roster.Tasks {
		if row.ToolCallID != "tool-"+row.TaskID {
			t.Fatalf("protected match lost: %#v", row)
		}
	}
	// Oldest pending was evicted; newest survives the next roster; all others are pruned.
	h.Sink(holdBackgroundTaskRosterFixture("0", fmt.Sprint(maxBackgroundTaskJoins-1)))
	roster, _ = h.BackgroundTaskRoster()
	if roster.Tasks[0].ToolCallID != "" || roster.Tasks[1].ToolCallID != fmt.Sprint(maxBackgroundTaskJoins-1) || len(h.joins) != 1 {
		t.Fatalf("overflow/prune: %#v, joins=%d", roster, len(h.joins))
	}
	h.Sink(holdBackgroundTaskRosterFixture("a"))
	roster, _ = h.BackgroundTaskRoster()
	if roster.Tasks[0].ToolCallID != "" || slices.Contains(roster.Tasks[0].TruncatedFields, "tool_call_id") {
		t.Fatalf("reused pruned task: %#v", roster)
	}
	other := newSessionBackgroundTaskHold(nil)
	other.Sink(holdBackgroundTaskRosterFixture("a"))
	h.Sink(turnevent.BackgroundTaskStarted{TaskID: "a", ToolCallID: "new"})
	roster, _ = other.BackgroundTaskRoster()
	if roster.Tasks[0].ToolCallID != "" {
		t.Fatal("cross-session match")
	}
	h.Sink(turnevent.BackgroundTaskStarted{TaskID: "pending", ToolCallID: "old", TruncatedFields: []string{"tool_call_id"}})
	h.Sink(turnevent.BackgroundTaskRoster{})
	h.Sink(holdBackgroundTaskRosterFixture("pending"))
	roster, _ = h.BackgroundTaskRoster()
	if roster.Tasks[0].ToolCallID != "" || slices.Contains(roster.Tasks[0].TruncatedFields, "tool_call_id") {
		t.Fatalf("pending survived prune: %#v", roster)
	}
}

func TestBackgroundTaskJoin_Concurrent(t *testing.T) {
	t.Parallel()
	h := newSessionBackgroundTaskHold(func(turnevent.Event) {})
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				switch worker {
				case 0:
					h.Sink(turnevent.BackgroundTaskStarted{TaskID: "a", ToolCallID: "tool", TruncatedFields: []string{"tool_call_id"}})
				case 1:
					h.Sink(holdBackgroundTaskRosterFixture("a"))
				case 2:
					if roster, ok := h.BackgroundTaskRoster(); ok && len(roster.Tasks) > 0 {
						roster.Tasks[0].TruncatedFields[0] = "mutated"
					}
				case 3:
					h.childExited()
				}
			}
		}(worker)
	}
	wg.Wait()
}

func TestBackgroundTaskJoin_FactoryChildExit(t *testing.T) {
	t.Parallel()
	for _, stdio := range []bool{false, true} {
		t.Run(fmt.Sprint(stdio), func(t *testing.T) {
			t.Parallel()
			bin, release := fakeExitingClaude(t, t.TempDir())
			script, err := os.ReadFile(bin)
			if err != nil {
				t.Fatal(err)
			}
			// The replacement child reuses the task id, reporting roster first.
			replacementRelease := filepath.Join(filepath.Dir(release), "replacement-release")
			replacement := fmt.Sprintf("while [ ! -e %q ]; do sleep 0.05; done; ", replacementRelease) + `printf '%s\n' '{"type":"system","subtype":"background_tasks_changed","tasks":[{"task_id":"a"}]}' '{"type":"system","subtype":"task_started","task_id":"a","tool_use_id":"replacement"}'; exec sleep 3600`
			script = []byte(strings.Replace(string(script), "exec sleep 3600", replacement, 1))
			if err := os.WriteFile(bin, script, 0755); err != nil {
				t.Fatal(err)
			}
			sink := newStreamTurnSink(8, discardLogger())
			runner, err := newStreamRunnerFactory(sink, "", nil, streamApprovalConfig{stdio: stdio})(sessions.RunnerConfig{ClaudeBin: bin, WorkDir: t.TempDir(), SessionID: "session", Logger: discardLogger()})
			if err != nil {
				t.Fatal(err)
			}
			sr := runner.(streamRunner)
			sr.tasks.Sink(turnevent.BackgroundTaskStarted{TaskID: "a", ToolCallID: "old", TruncatedFields: []string{"tool_call_id"}})
			sr.tasks.Sink(holdBackgroundTaskRosterFixture("a"))
			// Seeding through the hold also forwards two events; drain those before
			// waiting for the child's own stdout as the readiness signal.
			<-sink.ch
			<-sink.ch
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- runner.Run(ctx) }()
			t.Cleanup(func() { cancel(); <-done })
			select {
			case <-sink.ch:
			case <-time.After(5 * time.Second):
				t.Fatal("child stdout did not arrive")
			}
			if err := os.WriteFile(release, nil, 0600); err != nil {
				t.Fatal(err)
			}
			select {
			case exit := <-sink.ch:
				if !exit.exit {
					t.Fatalf("exit callback lost: %#v", exit)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("exit callback did not arrive")
			}
			roster, _ := sr.BackgroundTaskRoster()
			if roster.Tasks[0].ToolCallID != "" || slices.Contains(roster.Tasks[0].TruncatedFields, "tool_call_id") {
				t.Fatalf("stale child link: %#v", roster)
			}
			if err := os.WriteFile(replacementRelease, nil, 0600); err != nil {
				t.Fatal(err)
			}

			for i := 0; i < 2; i++ {
				select {
				case frame := <-sink.ch:
					if i == 0 {
						row := frame.ev.(turnevent.BackgroundTaskRoster).Tasks[0]
						if row.ToolCallID != "" || slices.Contains(row.TruncatedFields, "tool_call_id") {
							t.Fatalf("replacement inherited link: %#v", row)
						}
					}
				case <-time.After(5 * time.Second):
					t.Fatal("replacement child did not report")
				}
			}
			roster, _ = sr.BackgroundTaskRoster()
			if roster.Tasks[0].ToolCallID != "replacement" || slices.Contains(roster.Tasks[0].TruncatedFields, "tool_call_id") {
				t.Fatalf("replacement reuse: %#v", roster)
			}
		})
	}
}

func TestBackgroundTaskJoin_ParserIgnoresRosterID(t *testing.T) {
	t.Parallel()
	parser, held := newSessionParser(nil, discardLogger())
	line := `{"type":"system","subtype":"background_tasks_changed","tasks":[{"task_id":"a","tool_use_id":"spoof","tool_call_id":"spoof"}]}`
	if _, err := parser.Write([]byte(line + "\n")); err != nil {
		t.Fatal(err)
	}
	roster, _ := held.tasks.BackgroundTaskRoster()
	if roster.Tasks[0].ToolCallID != "" || len(held.tasks.joins) != 0 {
		t.Fatalf("roster acquired provenance: %#v", roster)
	}
}

func TestBackgroundTaskJoin_StartForwardAndUnlockedSink(t *testing.T) {
	t.Parallel()
	start := turnevent.BackgroundTaskStarted{TaskID: "a", ToolCallID: "tool", TaskType: "agent", Description: "label", TruncatedFields: []string{"tool_call_id"}}
	var h *sessionBackgroundTaskHold
	var seen []turnevent.Event
	h = newSessionBackgroundTaskHold(func(ev turnevent.Event) { h.BackgroundTaskRoster(); seen = append(seen, ev) })
	h.Sink(start)
	if len(seen) != 1 || !reflect.DeepEqual(seen[0], start) {
		t.Fatalf("start changed or extra event: %#v", seen)
	}
	row := holdBackgroundTaskRosterFixture("a")
	row.Tasks[0].TruncatedFields = append(row.Tasks[0].TruncatedFields, "tool_call_id")
	h.Sink(row)
	for i := 0; i < 2; i++ {
		got, _ := h.BackgroundTaskRoster()
		if !reflect.DeepEqual(got.Tasks[0].TruncatedFields, []string{"description", "tool_call_id"}) {
			t.Fatalf("duplicate marker: %#v", got)
		}
	}
}
