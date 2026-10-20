package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/streamsup"
	"github.com/pyrycode/pyrycode/internal/turncommit"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

const fixtureStallIdle = 200 * time.Millisecond

type streamWatchdogFixture struct {
	t       *testing.T
	r       streamRunner
	sink    *streamTurnSink
	resolve *stubActiveSession
	busy    *turnBusyTracker
	bcast   *chanBcast
	records chan slog.Record
	cancel  context.CancelFunc
	done    chan error
	dir     string
}

func newStreamWatchdogFixture(t *testing.T, approval streamApprovalConfig) *streamWatchdogFixture {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	// Like fakeExitingClaude, this executable accepts the production Claude argv.
	script := fmt.Sprintf(`#!/bin/sh
while IFS= read -r line; do
  case "$line" in
    *'"type":"user"'*)
      case "$line" in
        *'owe'*) printf '%%s\n' '%s' '{"type":"tool_result"}' ;;
        *'tool'*) printf '%%s\n' '%s' ;;
        *'progress'*) printf '%%s\n' '{"type":"system","subtype":"notice"}' ;;
        *'reply'*) printf '%%s\n' '%s' '{"type":"result","subtype":"success","is_error":false,"result":"done"}' ;;
        *'close-input'*) exec 0<&-; printf '%%s\n' '%s'; exec sleep 3600 ;;
        *'exit'*) exit 0 ;;
      esac ;;
  esac
done
`, assistantTextLine("owe", "working"), assistantTextLine("tool", "tool wait"), assistantTextLine("reply", "resumed"), assistantTextLine("closed", "closed input"))
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	f := &streamWatchdogFixture{t: t, resolve: &stubActiveSession{}, records: make(chan slog.Record, 128), bcast: newChanBcast("interactive")}
	f.dir = dir
	f.resolve.bind("watch-a", testConvID)
	f.resolve.bind("watch-b", testConvIDB)
	logger := slog.New(dropWatcher{recs: f.records})
	f.sink = newStreamTurnSink(128, logger)
	f.sink.live = newDaemonLiveState(f.resolve.get)
	f.busy = newTurnBusyTracker(f.resolve.get, logger)
	cur := &stubCursor{}
	cur.set(testConvIDB)
	emitter := newInteractiveTurnEmitterV2(cur, f.bcast, logger)
	ctx, cancel := context.WithCancel(t.Context())
	join := startStreamTurnDrainV2(ctx, f.sink, emitter, f.resolve.get, f.busy, logger)
	t.Cleanup(func() { cancel(); join() })
	approval.watchdogIdle = fixtureStallIdle
	r, err := newStreamRunnerFactory(f.sink, "", nil, approval)(sessions.RunnerConfig{
		ClaudeBin: bin, WorkDir: dir, SessionID: "watch-a", Logger: logger,
		BackoffInitial: time.Millisecond, BackoffMax: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.r = r.(streamRunner)
	t.Cleanup(func() { f.stop() })
	return f
}

func watchdogEventually(t *testing.T, predicate func() bool) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for !predicate() {
		select {
		case <-deadline.C:
			t.Fatal("condition did not become true")
		case <-tick.C:
		}
	}
}

func (f *streamWatchdogFixture) start() {
	f.t.Helper()
	ctx, cancel := context.WithCancel(f.t.Context())
	f.cancel, f.done = cancel, make(chan error, 1)
	go func() { f.done <- f.r.Run(ctx) }()
	watchdogEventually(f.t, func() bool { return f.r.State().Phase == sessions.PhaseRunning })
}

func (f *streamWatchdogFixture) stop() {
	if f.cancel == nil {
		return
	}
	f.cancel()
	select {
	case err := <-f.done:
		if !errors.Is(err, context.Canceled) {
			f.t.Errorf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		f.t.Error("Run did not join")
	}
	f.cancel = nil
}

func (f *streamWatchdogFixture) send(command string) {
	f.t.Helper()
	if err := f.r.WriteUserTurn(f.t.Context(), testConvID, []byte(command)); err != nil {
		f.t.Fatal(err)
	}
}

func (f *streamWatchdogFixture) frame(kind string) protocol.Envelope {
	f.t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case e := <-f.bcast.pushed:
			if e.Type == kind {
				return e
			}
		case <-deadline.C:
			f.t.Fatalf("no production %s publication", kind)
			return protocol.Envelope{}
		}
	}
}

func (f *streamWatchdogFixture) quiet() {
	f.t.Helper()
	deadline := time.NewTimer(3 * fixtureStallIdle)
	defer deadline.Stop()
	for {
		select {
		case e := <-f.bcast.pushed:
			if e.Type == protocol.TypeStall {
				f.t.Fatal("unexpected stall")
			}
		case <-deadline.C:
			return
		}
	}
}

func (f *streamWatchdogFixture) stall(session string) {
	f.t.Helper()
	e := f.frame(protocol.TypeStall)
	var payload protocol.StallPayload
	if err := json.Unmarshal(e.Payload, &payload); err != nil || payload.ConversationID != testConvID {
		f.t.Fatalf("stall attribution: %s (%v)", e.Payload, err)
	}
	r, ok := testLiveReadings(f.t, f.sink.live, testConvID)[liveReadingKey{protocol.TypeStall, ""}]
	if !ok || r.Envelope.SessionStateCleared || string(r.Envelope.SessionID) != fmt.Sprintf("%q", session) {
		f.t.Fatalf("retained stall source: %+v", r)
	}
	if _, ok := testLiveReadings(f.t, f.sink.live, testConvIDB)[liveReadingKey{protocol.TypeStall, ""}]; ok {
		f.t.Fatal("stall leaked to active conversation")
	}
}

func TestStreamWatchdog_ProductionLifecycle(t *testing.T) {
	f := newStreamWatchdogFixture(t, streamApprovalConfig{})
	f.start()
	f.quiet() // a newly activated child owes nothing
	before := f.r.State()
	f.send("owe")
	watchdogEventually(t, func() bool { return f.busy.Busy(testConvID) })
	f.stall("watch-a")
	after := f.r.State()
	if after.ChildPID != before.ChildPID || after.RestartCount != before.RestartCount || !f.busy.Busy(testConvID) {
		t.Fatalf("stall changed child/busy: before=%+v after=%+v", before, after)
	}
	f.quiet() // onset only, even over several more idle thresholds
	f.responsive()
	f.send("reply")
	f.frame(protocol.TypeTurnEnd)
	watchdogEventually(t, func() bool { return !f.busy.Busy(testConvID) })
	f.quiet() // poll observes progress and disarms
	f.send("owe")
	f.stall("watch-a")
	f.send("exit")
	watchdogEventually(t, func() bool {
		return f.r.State().ChildPID != before.ChildPID && f.r.State().Phase == sessions.PhaseRunning && !f.busy.Busy(testConvID)
	})
	f.quiet() // a respawn inherits no debt
	f.resolve.bind("watch-rotated", testConvID)
	f.r.RestartFresh("watch-rotated")
	pid := f.r.State().ChildPID
	watchdogEventually(t, func() bool { return f.r.State().Phase == sessions.PhaseRunning && f.r.State().ChildPID != pid })
	f.quiet()
	f.send("owe")
	f.stall("watch-rotated")
	f.stop()
	f.start() // Session.runActive reuses this exact adapter
	f.quiet()
	f.send("owe")
	f.stall("watch-rotated")
}

func TestStreamWatchdog_Controls(t *testing.T) {
	f := newStreamWatchdogFixture(t, streamApprovalConfig{})
	if err := f.r.WriteUserTurn(t.Context(), testConvID, []byte("silent")); !errors.Is(err, streamsup.ErrNoLiveChild) {
		t.Fatalf("no-child refusal: %v", err)
	}
	f.start()
	ctx := turncommit.With(t.Context(), func() bool { return false })
	if err := f.r.WriteUserTurn(ctx, testConvID, []byte("silent")); !errors.Is(err, turncommit.ErrDropped) {
		t.Fatalf("gate refusal: %v", err)
	}
	abort := f.r.BeginRotation()
	if err := f.r.WriteUserTurn(t.Context(), testConvID, []byte("silent")); !errors.Is(err, streamsup.ErrNoLiveChild) {
		t.Fatalf("rotation refusal: %v", err)
	}
	abort()
	f.quiet()
	f.send("tool")
	f.frame(protocol.TypeAssistantDelta)
	f.quiet() // assistant output makes subsequent tool silence healthy
	f.send("close-input")
	f.frame(protocol.TypeAssistantDelta)
	if err := f.r.WriteUserTurn(t.Context(), testConvID, []byte("silent")); err == nil {
		t.Fatal("closed child input accepted turn")
	}
	f.quiet() // a failed pipe write cannot arm the detector
	f.r.BeginTeardown()
	if err := f.r.WriteUserTurn(t.Context(), testConvID, []byte("silent")); err == nil {
		t.Fatal("teardown accepted turn")
	}
	f.quiet()
	f.stop()
	if err := f.r.WriteUserTurn(t.Context(), testConvID, []byte(strings.Repeat("x", 128))); err == nil {
		t.Fatal("stopped write accepted")
	}
}

type watchdogControlSession struct{ streamRunner }

func (s watchdogControlSession) Activate(ctx context.Context) error { return s.WaitForPTY(ctx) }

type watchdogControlResolver struct{ r streamRunner }

func (s watchdogControlResolver) Lookup(sessions.SessionID) (control.Session, error) {
	return watchdogControlSession{s.r}, nil
}
func (s watchdogControlResolver) ResolveID(string) (sessions.SessionID, error) { return "watch-a", nil }

func (f *streamWatchdogFixture) responsive() {
	f.t.Helper()
	socket := filepath.Join(shortTempDir(f.t), "watch.sock")
	srv := control.NewServer(socket, watchdogControlResolver{f.r}, nil, nil, discardLogger(), nil)
	if err := srv.Listen(); err != nil {
		f.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(f.t.Context())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	defer func() { cancel(); _ = srv.Close(); <-done }()
	status, err := control.Status(ctx, socket)
	if err != nil || status == nil {
		f.t.Fatalf("stalled control socket: %v", err)
	}
	other, err := newStreamRunnerFactory(f.sink, "", nil, streamApprovalConfig{watchdogIdle: fixtureStallIdle})(sessions.RunnerConfig{
		ClaudeBin: filepath.Join(f.dir, "claude"), WorkDir: f.dir, SessionID: "watch-b", Logger: discardLogger(),
	})
	if err != nil {
		f.t.Fatal(err)
	}
	otherDone := make(chan error, 1)
	go func() { otherDone <- other.Run(ctx) }()
	defer func() { cancel(); <-otherDone }()
	watchdogEventually(f.t, func() bool { return other.State().Phase == sessions.PhaseRunning })
	if err := other.WriteUserTurn(ctx, testConvIDB, []byte("reply")); err != nil {
		f.t.Fatal(err)
	}
	e := f.frame(protocol.TypeTurnEnd)
	if !strings.Contains(string(e.Payload), testConvIDB) {
		f.t.Fatalf("other conversation unresponsive: %s", e.Payload)
	}
}

func TestStreamWatchdog_WriteExitOrdering(t *testing.T) {
	f := newStreamWatchdogFixture(t, streamApprovalConfig{})
	f.start()
	delivered, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	// Pause after the real pipe write succeeded, before successful-write arming.
	go func() {
		finished <- f.r.watchdog.write(func() error {
			err := f.r.r.WriteUserTurn(t.Context(), testConvID, []byte("silent"))
			close(delivered)
			<-release
			return err
		})
	}()
	select {
	case <-delivered:
	case <-time.After(5 * time.Second):
		t.Fatal("write not delivered")
	}
	pid := f.r.State().ChildPID
	f.r.RestartFresh("watch-a")
	watchdogEventually(t, func() bool { return f.r.r.Stdin() == nil })
	unblock()
	if err := <-finished; err != nil {
		t.Fatalf("predecessor write: %v", err)
	}
	watchdogEventually(t, func() bool { return f.r.State().Phase == sessions.PhaseRunning && f.r.State().ChildPID != pid })
	f.quiet()
	f.send("silent")
	f.stall("watch-a")
}

func TestStreamWatchdog_ResponseBeforeArming(t *testing.T) {
	for _, tc := range []struct {
		command string
		frame   string
		busy    bool
	}{
		{"tool", protocol.TypeAssistantDelta, true},
		{"reply", protocol.TypeTurnEnd, false},
	} {
		t.Run(tc.command, func(t *testing.T) {
			f := newStreamWatchdogFixture(t, streamApprovalConfig{})
			f.start()
			before := f.r.State()
			release, finished := make(chan struct{}), make(chan error, 1)
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(func() {
				unblock()
				select {
				case <-finished:
				case <-time.After(5 * time.Second):
					t.Error("delivery did not join")
				}
			})
			go func() {
				defer close(finished)
				finished <- f.r.watchdog.write(func() error {
					err := f.r.r.WriteUserTurn(t.Context(), testConvID, []byte(tc.command))
					<-release // hold successful delivery before watchdog arming
					return err
				})
			}()
			// The factory feeds the tracker before the parser; publication proves
			// stdout has already changed owed state while delivery is held.
			f.frame(tc.frame)
			unblock()
			select {
			case err := <-finished:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("delivery did not return")
			}
			f.quiet()
			after := f.r.State()
			if after.ChildPID != before.ChildPID || after.RestartCount != before.RestartCount || f.busy.Busy(testConvID) != tc.busy {
				t.Fatalf("response changed child/busy: before=%+v after=%+v", before, after)
			}
			f.send("silent")
			f.stall("watch-a") // preserving progress must not disable later arming
		})
	}
}

func TestStreamWatchdog_ApprovalScope(t *testing.T) {
	for _, own := range []bool{false, true} {
		t.Run(fmt.Sprintf("own=%t", own), func(t *testing.T) {
			a := newApprovalReport(t, testConvIDB, discardLogger())
			session, tool := "sess-b", "tu-b"
			if own {
				session, tool = "sess-a", "tu-a"
			}
			a.tr.observe(session, turnevent.ToolStart{ToolCallID: tool, Title: "Read"})
			retire := a.park(t, tool)
			defer retire()
			report := &approvalParkedReport{}
			report.set(a.bridge.ApprovalParked)
			reg := &conversations.Registry{}
			reg.Create(conversations.Conversation{ID: conversations.ConversationID(testConvID), CurrentSessionID: "watch-a"})
			reg.Create(conversations.Conversation{ID: conversations.ConversationID(testConvIDB), CurrentSessionID: "watch-b"})
			pending := streamPendingPermission(reg, report)
			if pending("unbound") {
				t.Fatal("unbound session inherited active approval")
			}
			f := newStreamWatchdogFixture(t, streamApprovalConfig{pendingPermission: pending})
			f.start()
			if !reg.RebindSession("watch-a", "watch-rotated") {
				t.Fatal("rebind failed")
			}
			f.resolve.bind("watch-rotated", testConvID)
			pid := f.r.State().ChildPID
			f.r.RestartFresh("watch-rotated")
			watchdogEventually(t, func() bool { return f.r.State().Phase == sessions.PhaseRunning && f.r.State().ChildPID != pid })
			f.send("silent")
			f.stall("watch-rotated")
			found := false
			for !found {
				select {
				case record := <-f.records:
					if !strings.Contains(record.Message, "idle stream stall") {
						continue
					}
					fields := map[string]any{}
					record.Attrs(func(attr slog.Attr) bool { fields[attr.Key] = attr.Value.Any(); return true })
					if len(fields) != 2 || fields["pending_permission"] != own {
						t.Fatalf("diagnostic leaked content or wrong approval: %v", fields)
					}
					found = true
				case <-time.After(5 * time.Second):
					t.Fatal("missing stall diagnostic")
				}
			}
			if !a.bridge.ApprovalParked(map[bool]string{true: testConvID, false: testConvIDB}[own]) {
				t.Fatal("stall consumed approval")
			}
		})
	}
}
