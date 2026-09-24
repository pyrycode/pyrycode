package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/modalbridge"
	"github.com/pyrycode/pyrycode/internal/permbridge"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// fakeWakeConns returns a fixed open-conn snapshot.
type fakeWakeConns struct{ conns []relay.ActiveConn }

func (f *fakeWakeConns) ActiveConns(ctx context.Context) []relay.ActiveConn {
	if ctx.Err() != nil {
		return nil // mirrors V2SessionManager.ActiveConns on a cancelled ctx
	}
	return slices.Clone(f.conns)
}

// fakeWakeDevices returns a fixed device list.
type fakeWakeDevices struct{ devs []devices.Device }

func (f *fakeWakeDevices) List() []devices.Device { return slices.Clone(f.devs) }

// wakeSends records every push_wake send and fails with err when set.
type wakeSends struct {
	tokens []string
	err    error
}

func (w *wakeSends) send(platform, token string) error {
	if platform != "fcm" {
		return errors.New("unexpected platform " + platform)
	}
	w.tokens = append(w.tokens, token)
	return w.err
}

// newTestPushWaker builds a waker over fakes with a controllable clock and a
// captured debug log.
func newTestPushWaker(conns []relay.ActiveConn, devs []devices.Device) (*pushWaker, *wakeSends, *time.Time, *bytes.Buffer) {
	sends := &wakeSends{}
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	w := newPushWaker(&fakeWakeConns{conns: conns}, &fakeWakeDevices{devs: devs}, sends.send, logger)
	clock := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	w.now = func() time.Time { return clock }
	return w, sends, &clock, &logs
}

func fcmDevice(hash, name, token string) devices.Device {
	return devices.Device{TokenHash: hash, Name: name, Platform: "fcm", PushToken: token}
}

// TestPushWaker_Eligibility pins AC-1's device selection: only an FCM device
// with a token and no open session is woken, and "open session" is decided by
// the authenticated device hash, never the hello-claimed name.
func TestPushWaker_Eligibility(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		conns []relay.ActiveConn
		dev   devices.Device
		want  []string
	}{
		{"fcm with token and no session is woken", nil, fcmDevice("h1", "Pixel", "tok-1"), []string{"tok-1"}},
		{"open session suppresses the wake",
			[]relay.ActiveConn{{ConnID: "c1", DeviceTokenHash: "h1"}}, fcmDevice("h1", "Pixel", "tok-1"), nil},
		{"apns is skipped", nil, devices.Device{TokenHash: "h1", Platform: "apns", PushToken: "tok-1"}, nil},
		{"fcm without a token is skipped", nil, devices.Device{TokenHash: "h1", Platform: "fcm"}, nil},
		{"no platform is skipped", nil, devices.Device{TokenHash: "h1", PushToken: "tok-1"}, nil},
		{"a conn claiming the device's name does not suppress it",
			[]relay.ActiveConn{{ConnID: "c1", DeviceName: "Pixel", DeviceTokenHash: "h-attacker"}},
			fcmDevice("h1", "Pixel", "tok-1"), []string{"tok-1"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w, sends, _, _ := newTestPushWaker(tc.conns, []devices.Device{tc.dev})
			w.wakeAbsent(context.Background())
			if !slices.Equal(sends.tokens, tc.want) {
				t.Errorf("wakes = %v, want %v", sends.tokens, tc.want)
			}
		})
	}
}

// TestPushWaker_CoalescesPerDevice pins AC-2 on an injected clock: one wake per
// device per 30 s window, and one device's window does not hold another's wake.
func TestPushWaker_CoalescesPerDevice(t *testing.T) {
	t.Parallel()
	a := fcmDevice("ha", "A", "tok-a")
	b := fcmDevice("hb", "B", "tok-b")
	// B is connected for the first pass, so only A is woken then.
	conns := &fakeWakeConns{conns: []relay.ActiveConn{{ConnID: "cb", DeviceTokenHash: "hb"}}}
	w, sends, clock, _ := newTestPushWaker(nil, []devices.Device{a, b})
	w.conns = conns
	t0 := *clock

	w.wakeAbsent(context.Background())
	if want := []string{"tok-a"}; !slices.Equal(sends.tokens, want) {
		t.Fatalf("t0 wakes = %v, want %v", sends.tokens, want)
	}

	// B disconnects; 29 s later A is still inside its window, B is not.
	conns.conns = nil
	*clock = t0.Add(pushWakeWindow - time.Second)
	w.wakeAbsent(context.Background())
	if want := []string{"tok-a", "tok-b"}; !slices.Equal(sends.tokens, want) {
		t.Fatalf("t0+29s wakes = %v, want %v", sends.tokens, want)
	}

	// At exactly 30 s A's window has elapsed; B's (opened at t0+29s) has not.
	*clock = t0.Add(pushWakeWindow)
	w.wakeAbsent(context.Background())
	if want := []string{"tok-a", "tok-b", "tok-a"}; !slices.Equal(sends.tokens, want) {
		t.Fatalf("t0+30s wakes = %v, want %v", sends.tokens, want)
	}
}

// TestPushWaker_SendFailure_TokenNeverLogged pins AC-3 on the failure path: even
// when the send error itself carries the token, the log does not. A failed send
// is not a sent wake, so it opens no window.
func TestPushWaker_SendFailure_TokenNeverLogged(t *testing.T) {
	t.Parallel()
	const token = "secret-fcm-token-value"
	w, sends, _, logs := newTestPushWaker(nil, []devices.Device{fcmDevice("h1", "Pixel", token)})
	sends.err = errors.New("send failed for " + token)

	w.wakeAbsent(context.Background())
	w.wakeAbsent(context.Background())

	if len(sends.tokens) != 2 {
		t.Errorf("sends = %d, want 2: a failed send must not open the coalescing window", len(sends.tokens))
	}
	if !strings.Contains(logs.String(), "push_wake.send_err") {
		t.Errorf("log lacks the push_wake.send_err event: %s", logs.String())
	}
	if strings.Contains(logs.String(), token) {
		t.Errorf("log carries the push token: %s", logs.String())
	}

	// And the success path.
	sends.err = nil
	w.wakeAbsent(context.Background())
	if strings.Contains(logs.String(), token) {
		t.Errorf("log carries the push token after a successful send: %s", logs.String())
	}
}

// TestPushWaker_CancelledCtx_SendsNothing pins the shutdown guard: a cancelled
// snapshot is empty, and reading it as "nobody connected" would wake everyone.
func TestPushWaker_CancelledCtx_SendsNothing(t *testing.T) {
	t.Parallel()
	w, sends, _, _ := newTestPushWaker(nil, []devices.Device{fcmDevice("h1", "Pixel", "tok-1")})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w.wakeAbsent(ctx)
	if len(sends.tokens) != 0 {
		t.Errorf("wakes on a cancelled ctx = %v, want none", sends.tokens)
	}
}

// TestPushWaker_Trigger pins that Trigger is inert on a nil waker and never
// blocks the caller: a burst leaves exactly one pending pass.
func TestPushWaker_Trigger(t *testing.T) {
	t.Parallel()
	var nilWaker *pushWaker
	nilWaker.Trigger()

	w, _, _, _ := newTestPushWaker(nil, nil)
	w.Trigger()
	w.Trigger()
	if got := len(w.trig); got != 1 {
		t.Errorf("pending triggers after a burst = %d, want 1", got)
	}
}

// TestPushWaker_Run_WakesOnTrigger drives the goroutine end to end: a trigger
// produces a pass, and run returns when ctx is cancelled.
func TestPushWaker_Run_WakesOnTrigger(t *testing.T) {
	t.Parallel()
	sent := make(chan string, 1)
	w := newPushWaker(&fakeWakeConns{}, &fakeWakeDevices{devs: []devices.Device{fcmDevice("h1", "Pixel", "tok-1")}},
		func(_, token string) error { sent <- token; return nil }, discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); w.run(ctx) }()

	w.Trigger()
	select {
	case got := <-sent:
		if got != "tok-1" {
			t.Errorf("woken token = %q, want tok-1", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no wake after Trigger")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after ctx cancel")
	}
}

// TestInteractiveTurnEmitterV2_TurnEndTriggersWake pins the turn trigger: the
// TurnEnd arm asks for a wake, and ordinary turn content does not.
func TestInteractiveTurnEmitterV2_TurnEndTriggersWake(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())
	e.waker, _, _, _ = newTestPushWaker(nil, nil)

	e.Handle(context.Background(), turnevent.TextChunk{Text: "hello"})
	if got := len(e.waker.trig); got != 0 {
		t.Fatalf("pending triggers after a text chunk = %d, want 0", got)
	}
	e.Handle(context.Background(), turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn})
	if got := len(e.waker.trig); got != 1 {
		t.Errorf("pending triggers after turn end = %d, want 1", got)
	}
}

// TestStreamApprovalBridge_SurfaceTriggersWake pins the permission-prompt
// trigger: the live modal_shown asks for a wake, and its dismissal does not.
func TestStreamApprovalBridge_SurfaceTriggersWake(t *testing.T) {
	t.Parallel()
	perm := permbridge.New()
	bridge := newStreamApprovalBridge(perm, modalbridge.New(), oneInteractiveConn("c1"), func() string { return testConvID }, context.Background(), discardLogger())
	bridge.waker, _, _, _ = newTestPushWaker(nil, nil)

	req, _ := parkApproval(t, perm, "tu-1", "Bash", json.RawMessage(`{"cmd":"ls"}`))
	retire := bridge.Surface(req)
	if got := len(bridge.waker.trig); got != 1 {
		t.Fatalf("pending triggers after Surface = %d, want 1", got)
	}
	<-bridge.waker.trig
	retire()
	if got := len(bridge.waker.trig); got != 0 {
		t.Errorf("pending triggers after the dismissal = %d, want 0", got)
	}
}
