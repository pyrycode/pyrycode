package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
// log captured at Info, the daemon's default level, so a decision line logged
// any quieter fails the tests that look for it.
func newTestPushWaker(conns []relay.ActiveConn, devs []devices.Device) (*pushWaker, *wakeSends, *time.Time, *bytes.Buffer) {
	sends := &wakeSends{}
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	w := newPushWaker(&fakeWakeConns{conns: conns}, &fakeWakeDevices{devs: devs}, sends.send, logger)
	clock := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	w.now = func() time.Time { return clock }
	return w, sends, &clock, &logs
}

func fcmDevice(hash, name, token string) devices.Device {
	return devices.Device{TokenHash: hash, Name: name, Platform: "fcm", PushToken: token}
}

// testWakeCause is the cause the direct wakeAbsent tests run a pass under.
var testWakeCause = pushWakeCause{conversationID: testConvID, trigger: pushWakeTurnEnd}

// runPendingPass runs the one pass a Trigger left pending, as run would.
func runPendingPass(t *testing.T, w *pushWaker) {
	t.Helper()
	select {
	case <-w.trig:
	default:
		t.Fatal("no pending wake pass")
	}
	w.wakeAbsent(context.Background(), w.takeCause())
}

// wakeLines parses the captured log into its push_wake decision lines.
func wakeLines(t *testing.T, logs *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(logs.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line is not JSON: %q: %v", line, err)
		}
		if ev, _ := m["event"].(string); strings.HasPrefix(ev, "push_wake.") {
			out = append(out, m)
		}
	}
	return out
}

// lineFor returns the one decision line naming device, failing on none or more.
func lineFor(t *testing.T, lines []map[string]any, device string) map[string]any {
	t.Helper()
	var found []map[string]any
	for _, l := range lines {
		if l["device_name"] == device {
			found = append(found, l)
		}
	}
	if len(found) != 1 {
		t.Fatalf("decision lines for %q = %d, want 1: %v", device, len(found), lines)
	}
	return found[0]
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
			w.wakeAbsent(context.Background(), testWakeCause)
			if !slices.Equal(sends.tokens, tc.want) {
				t.Errorf("wakes = %v, want %v", sends.tokens, tc.want)
			}
		})
	}
}

// TestPushWaker_NewestPairedFirst pins #2602: the relay drops wakes past its
// per-server-id burst, so a pass sends newest PairedAt first and dead
// registrations from older pairings cannot spend the burst before the live phone.
func TestPushWaker_NewestPairedFirst(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var devs []devices.Device // oldest first, as the registry keeps them
	for i := range 7 {
		tok := fmt.Sprintf("tok-%d", i)
		d := fcmDevice("h"+tok, tok, tok)
		d.PairedAt = base.Add(time.Duration(i) * 24 * time.Hour)
		devs = append(devs, d)
	}
	w, sends, _, _ := newTestPushWaker(nil, devs)
	w.wakeAbsent(context.Background(), testWakeCause)
	want := []string{"tok-6", "tok-5", "tok-4", "tok-3", "tok-2", "tok-1", "tok-0"}
	if !slices.Equal(sends.tokens, want) {
		t.Errorf("wake order = %v, want %v", sends.tokens, want)
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

	w.wakeAbsent(context.Background(), testWakeCause)
	if want := []string{"tok-a"}; !slices.Equal(sends.tokens, want) {
		t.Fatalf("t0 wakes = %v, want %v", sends.tokens, want)
	}

	// B disconnects; 29 s later A is still inside its window, B is not.
	conns.conns = nil
	*clock = t0.Add(pushWakeWindow - time.Second)
	w.wakeAbsent(context.Background(), testWakeCause)
	if want := []string{"tok-a", "tok-b"}; !slices.Equal(sends.tokens, want) {
		t.Fatalf("t0+29s wakes = %v, want %v", sends.tokens, want)
	}

	// At exactly 30 s A's window has elapsed; B's (opened at t0+29s) has not.
	*clock = t0.Add(pushWakeWindow)
	w.wakeAbsent(context.Background(), testWakeCause)
	if want := []string{"tok-a", "tok-b", "tok-a"}; !slices.Equal(sends.tokens, want) {
		t.Fatalf("t0+30s wakes = %v, want %v", sends.tokens, want)
	}
}

// TestPushWaker_DecisionLines pins #2705 AC-1: every pass logs exactly one
// Info line per fcm device with a token — sent, send_err, or skipped with its
// reason — and nothing for a device that is not eligible at all.
func TestPushWaker_DecisionLines(t *testing.T) {
	t.Parallel()
	devs := []devices.Device{
		fcmDevice("h-sent", "Sent", "tok-sent"),
		fcmDevice("h-fail", "Fail", "tok-fail"),
		fcmDevice("h-conn", "Connected", "tok-conn"),
		fcmDevice("h-recent", "Recent", "tok-recent"),
		{TokenHash: "h-apns", Name: "Apns", Platform: "apns", PushToken: "tok-apns"},
		{TokenHash: "h-notok", Name: "NoToken", Platform: "fcm"},
	}
	conns := []relay.ActiveConn{{ConnID: "c1", DeviceTokenHash: "h-conn"}}
	w, _, _, logs := newTestPushWaker(conns, devs)
	w.last["h-recent"] = w.now() // woken inside the window by an earlier pass
	w.send = func(_, token string) error {
		if token == "tok-fail" {
			return errors.New("fcm unavailable")
		}
		return nil
	}

	w.wakeAbsent(context.Background(), testWakeCause)

	lines := wakeLines(t, logs)
	if len(lines) != 4 {
		t.Fatalf("decision lines = %d, want 4 (one per eligible device): %v", len(lines), lines)
	}
	tests := []struct{ device, event, reason string }{
		{"Sent", "push_wake.sent", ""},
		{"Fail", "push_wake.send_err", ""},
		{"Connected", "push_wake.skipped", "device_connected"},
		{"Recent", "push_wake.skipped", "recently_woken"},
	}
	for _, tc := range tests {
		l := lineFor(t, lines, tc.device)
		if l["level"] != "INFO" {
			t.Errorf("%s: level = %v, want INFO", tc.device, l["level"])
		}
		if l["event"] != tc.event {
			t.Errorf("%s: event = %v, want %s", tc.device, l["event"], tc.event)
		}
		if got, _ := l["reason"].(string); got != tc.reason {
			t.Errorf("%s: reason = %q, want %q", tc.device, got, tc.reason)
		}
		if l["conversation_id"] != testConvID || l["trigger"] != "turn_end" {
			t.Errorf("%s: conversation_id/trigger = %v/%v, want %s/turn_end", tc.device, l["conversation_id"], l["trigger"], testConvID)
		}
	}
}

// TestPushWaker_SendFailure_TokenNeverLogged pins #2705 AC-3: even when the send
// error itself carries the token, the log carries neither it, the error text,
// nor any token hash, and names devices only by their registry entry — never by
// the name an open connection claims. A failed send is not a sent wake, so it
// opens no window.
func TestPushWaker_SendFailure_TokenNeverLogged(t *testing.T) {
	t.Parallel()
	const (
		token     = "secret-fcm-token-value"
		errText   = "fcm rejected registration"
		hashPixel = "hash-pixel-7f3a9c"
		hashTab   = "hash-tablet-2b81d4"
	)
	devs := []devices.Device{
		fcmDevice(hashPixel, "Pixel", token),
		fcmDevice(hashTab, "Tablet", "tok-tablet"),
	}
	// The tablet's session claims the absent Pixel's name in its hello.
	conns := []relay.ActiveConn{{ConnID: "c1", DeviceName: "Pixel", DeviceTokenHash: hashTab}}
	w, sends, _, logs := newTestPushWaker(conns, devs)
	sends.err = errors.New(errText + ": " + token)

	w.wakeAbsent(context.Background(), testWakeCause)
	w.wakeAbsent(context.Background(), testWakeCause)

	if want := []string{token, token}; !slices.Equal(sends.tokens, want) {
		t.Errorf("sends = %v, want %v: a failed send must not open the coalescing window", sends.tokens, want)
	}
	lines := wakeLines(t, logs)
	if len(lines) != 4 {
		t.Fatalf("decision lines = %d, want 4 (two devices, two passes): %v", len(lines), lines)
	}
	for _, l := range lines {
		switch l["device_name"] {
		case "Pixel":
			if l["event"] != "push_wake.send_err" {
				t.Errorf("Pixel line = %v, want push_wake.send_err", l)
			}
		case "Tablet":
			if l["event"] != "push_wake.skipped" || l["reason"] != "device_connected" {
				t.Errorf("Tablet line = %v, want push_wake.skipped device_connected", l)
			}
		default:
			t.Errorf("line names device %v, want a registry name: %v", l["device_name"], l)
		}
	}

	// And the success path.
	sends.err = nil
	w.wakeAbsent(context.Background(), testWakeCause)
	for _, secret := range []string{token, errText, hashPixel, hashTab} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("log carries %q: %s", secret, logs.String())
		}
	}
}

// TestPushWaker_CancelledCtx_SendsNothing pins the shutdown guard: a cancelled
// snapshot is empty, and reading it as "nobody connected" would wake everyone.
func TestPushWaker_CancelledCtx_SendsNothing(t *testing.T) {
	t.Parallel()
	w, sends, _, _ := newTestPushWaker(nil, []devices.Device{fcmDevice("h1", "Pixel", "tok-1")})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w.wakeAbsent(ctx, testWakeCause)
	if len(sends.tokens) != 0 {
		t.Errorf("wakes on a cancelled ctx = %v, want none", sends.tokens)
	}
}

// TestPushWaker_Trigger pins that Trigger is inert on a nil waker and never
// blocks the caller: a burst leaves exactly one pending pass, and that pass
// names the most recent trigger.
func TestPushWaker_Trigger(t *testing.T) {
	t.Parallel()
	var nilWaker *pushWaker
	nilWaker.Trigger(testConvID, pushWakeTurnEnd)

	w, _, _, logs := newTestPushWaker(nil, []devices.Device{fcmDevice("h1", "Pixel", "tok-1")})
	w.Trigger("conv-older", pushWakeTurnEnd)
	w.Trigger("conv-newer", pushWakeModalShown)
	if got := len(w.trig); got != 1 {
		t.Fatalf("pending triggers after a burst = %d, want 1", got)
	}
	runPendingPass(t, w)
	l := lineFor(t, wakeLines(t, logs), "Pixel")
	if l["conversation_id"] != "conv-newer" || l["trigger"] != "modal_shown" {
		t.Errorf("collapsed pass names %v/%v, want conv-newer/modal_shown", l["conversation_id"], l["trigger"])
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

	w.Trigger(testConvID, pushWakeTurnEnd)
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
// TurnEnd arm asks for a wake, ordinary turn content does not, and the pass it
// starts names the turn's conversation and turn_end (#2705 AC-2).
func TestInteractiveTurnEmitterV2_TurnEndTriggersWake(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())
	waker, _, _, logs := newTestPushWaker(nil, []devices.Device{fcmDevice("h1", "Pixel", "tok-1")})
	e.waker = waker

	e.Handle(context.Background(), turnevent.TextChunk{Text: "hello"})
	if got := len(e.waker.trig); got != 0 {
		t.Fatalf("pending triggers after a text chunk = %d, want 0", got)
	}
	e.Handle(context.Background(), turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn})
	if got := len(e.waker.trig); got != 1 {
		t.Fatalf("pending triggers after turn end = %d, want 1", got)
	}
	runPendingPass(t, waker)
	l := lineFor(t, wakeLines(t, logs), "Pixel")
	if l["event"] != "push_wake.sent" || l["conversation_id"] != testConvID || l["trigger"] != "turn_end" {
		t.Errorf("turn-end pass line = %v, want push_wake.sent for %s/turn_end", l, testConvID)
	}
}

// TestStreamApprovalBridge_SurfaceTriggersWake pins the permission-prompt
// trigger: the live modal_shown asks for a wake naming its conversation and
// modal_shown (#2705 AC-2), and its dismissal does not ask again.
func TestStreamApprovalBridge_SurfaceTriggersWake(t *testing.T) {
	t.Parallel()
	perm := permbridge.New()
	bridge := newStreamApprovalBridge(perm, modalbridge.New(), oneInteractiveConn("c1"), func() string { return testConvID }, context.Background(), discardLogger())
	waker, _, _, logs := newTestPushWaker(nil, []devices.Device{fcmDevice("h1", "Pixel", "tok-1")})
	bridge.waker = waker

	req, _ := parkApproval(t, perm, "tu-1", "Bash", json.RawMessage(`{"cmd":"ls"}`))
	retire := bridge.Surface(req)
	if got := len(bridge.waker.trig); got != 1 {
		t.Fatalf("pending triggers after Surface = %d, want 1", got)
	}
	runPendingPass(t, waker)
	l := lineFor(t, wakeLines(t, logs), "Pixel")
	if l["event"] != "push_wake.sent" || l["conversation_id"] != testConvID || l["trigger"] != "modal_shown" {
		t.Errorf("modal pass line = %v, want push_wake.sent for %s/modal_shown", l, testConvID)
	}
	retire()
	if got := len(bridge.waker.trig); got != 0 {
		t.Errorf("pending triggers after the dismissal = %d, want 0", got)
	}
}
