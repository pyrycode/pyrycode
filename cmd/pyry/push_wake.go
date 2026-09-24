package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/relay"
)

// pushWakeWindow is the per-device coalescing window (#2564): after a wake is
// sent to a device, no further wake goes to it until this much time has passed.
// The relay rate-limits wakes per server-id too, so daemon-side coalescing keeps
// one chatty conversation from spending that budget on a single phone.
const pushWakeWindow = 30 * time.Second

// pushWakePlatform is the only platform a wake is sent for today; an apns device
// has no client to receive it (docs/protocol-mobile.md § push_wake).
const pushWakePlatform = "fcm"

// pushWakeConns is the open-session snapshot the waker suppresses on —
// *relay.V2SessionManager in production.
type pushWakeConns interface {
	ActiveConns(ctx context.Context) []relay.ActiveConn
}

// pushWakeDevices is the paired-device list — *devices.Registry in production.
type pushWakeDevices interface {
	List() []devices.Device
}

// pushWaker asks the relay to wake each absent phone when a turn ends or a
// permission prompt is shown (#2564). Triggers come from the two existing fan-out
// points, interactiveTurnEmitterV2's TurnEnd arm and streamApprovalBridge.Surface;
// the work runs on the waker's own goroutine so neither fan-out ever waits on a
// snapshot, a registry read or a relay write.
//
// A device is woken when it is on fcm with a push token, no open v2 session
// belongs to it, and it was not woken inside pushWakeWindow. "Belongs to it" is
// decided by ActiveConn.DeviceTokenHash — the device the handshake authenticated —
// and never by the hello-claimed DeviceName, which a phone could set to another
// device's name to silence that device's wakes.
//
// SECURITY: the push token reaches send and nothing else. No log line here
// carries a token, a token hash, a device name or a send error.
type pushWaker struct {
	conns  pushWakeConns
	devs   pushWakeDevices
	send   func(platform, token string) error // (*relay.Connection).SendPushWake
	now    func() time.Time
	logger *slog.Logger

	// trig holds at most one pending pass: a burst of triggers while a pass runs
	// collapses into one more pass against the then-current state.
	trig chan struct{}

	// last maps a device's TokenHash to its last SUCCESSFUL wake. Read and
	// written only on the run goroutine.
	last map[string]time.Time
}

func newPushWaker(conns pushWakeConns, devs pushWakeDevices, send func(platform, token string) error, logger *slog.Logger) *pushWaker {
	return &pushWaker{
		conns:  conns,
		devs:   devs,
		send:   send,
		now:    time.Now,
		logger: logger,
		trig:   make(chan struct{}, 1),
		last:   make(map[string]time.Time),
	}
}

// Trigger requests a wake pass. It never blocks, and a nil waker — every
// emitter and bridge built without one — does nothing.
func (w *pushWaker) Trigger() {
	if w == nil {
		return
	}
	select {
	case w.trig <- struct{}{}:
	default:
	}
}

// run serves triggers until ctx is cancelled.
func (w *pushWaker) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.trig:
			w.wakeAbsent(ctx)
		}
	}
}

// wakeAbsent sends one push_wake to every eligible device. A failed send is
// logged content-free and dropped: it opens no window, and nothing retries it
// except the next trigger.
func (w *pushWaker) wakeAbsent(ctx context.Context) {
	conns := w.conns.ActiveConns(ctx)
	// A cancelled snapshot is empty, not "nobody connected": waking every device
	// on the way down would be exactly wrong.
	if ctx.Err() != nil {
		return
	}
	open := make(map[string]bool, len(conns))
	for _, c := range conns {
		if c.DeviceTokenHash != "" {
			open[c.DeviceTokenHash] = true
		}
	}

	now := w.now()
	for hash, at := range w.last {
		if now.Sub(at) >= pushWakeWindow {
			delete(w.last, hash)
		}
	}

	for _, d := range w.devs.List() {
		if d.Platform != pushWakePlatform || d.PushToken == "" || open[d.TokenHash] {
			continue
		}
		if _, recent := w.last[d.TokenHash]; recent {
			continue
		}
		if err := w.send(pushWakePlatform, d.PushToken); err != nil {
			w.logger.Debug("relay: push_wake send dropped",
				"event", "push_wake.send_err")
			continue
		}
		w.last[d.TokenHash] = now
		w.logger.Debug("relay: push_wake sent", "event", "push_wake.sent")
	}
}
