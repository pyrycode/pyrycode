package main

import (
	"context"
	"log/slog"
	"slices"
	"sync"
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

// pushWakeTrigger names the event that started a wake pass, logged on every
// decision line so the journal ties a wake to what caused it (#2705).
type pushWakeTrigger string

const (
	pushWakeTurnEnd    pushWakeTrigger = "turn_end"
	pushWakeModalShown pushWakeTrigger = "modal_shown"
)

// pushWakeCause is the conversation and trigger a pass runs for: the most
// recent Trigger before the pass started.
type pushWakeCause struct {
	conversationID string
	trigger        pushWakeTrigger
}

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
// Every pass logs one Info line per fcm device with a token (#2705): sent,
// send_err, or skipped with its reason, each naming the device, the conversation
// and the trigger, so the journal alone says whether a wake was asked for.
//
// SECURITY: the push token reaches send and nothing else. A log line here may
// carry the device's name from its devices.Registry entry, as the v2 handshake
// accept line does, but never a token, a token hash or a send error, whose text
// can echo the token. Nor the hello-claimed ActiveConn.DeviceName, which a phone
// chooses for itself.
type pushWaker struct {
	conns  pushWakeConns
	devs   pushWakeDevices
	send   func(platform, token string) error // (*relay.Connection).SendPushWake
	now    func() time.Time
	logger *slog.Logger

	// trig holds at most one pending pass: a burst of triggers while a pass runs
	// collapses into one more pass against the then-current state.
	trig chan struct{}

	// pendMu guards pending, the cause the next pass logs. Trigger overwrites it
	// before signalling, so a collapsed burst names its most recent trigger.
	pendMu  sync.Mutex
	pending pushWakeCause

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

// Trigger requests a wake pass for conversationID's trigger. It never blocks,
// and a nil waker — every emitter and bridge built without one — does nothing.
func (w *pushWaker) Trigger(conversationID string, trigger pushWakeTrigger) {
	if w == nil {
		return
	}
	w.pendMu.Lock()
	w.pending = pushWakeCause{conversationID: conversationID, trigger: trigger}
	w.pendMu.Unlock()
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
			w.wakeAbsent(ctx, w.takeCause())
		}
	}
}

// takeCause returns the cause of the most recent Trigger.
func (w *pushWaker) takeCause() pushWakeCause {
	w.pendMu.Lock()
	defer w.pendMu.Unlock()
	return w.pending
}

// wakeAbsent sends one push_wake to every eligible device and logs the decision
// for each fcm device with a token. A failed send is logged without its error
// and dropped: it opens no window, and nothing retries it except the next trigger.
func (w *pushWaker) wakeAbsent(ctx context.Context, cause pushWakeCause) {
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

	// Newest pairing first (#2602): the relay drops wakes past its per-server-id
	// burst, and dead tokens from older pairings are never pruned, so walking
	// oldest first let them spend the burst before the live phone.
	devs := w.devs.List()
	slices.SortStableFunc(devs, func(a, b devices.Device) int {
		return b.PairedAt.Compare(a.PairedAt)
	})
	for _, d := range devs {
		if d.Platform != pushWakePlatform || d.PushToken == "" {
			continue
		}
		if open[d.TokenHash] {
			w.logDecision("relay: push_wake skipped", d, cause, "push_wake.skipped", "device_connected")
			continue
		}
		if _, recent := w.last[d.TokenHash]; recent {
			w.logDecision("relay: push_wake skipped", d, cause, "push_wake.skipped", "recently_woken")
			continue
		}
		if err := w.send(pushWakePlatform, d.PushToken); err != nil {
			w.logDecision("relay: push_wake send dropped", d, cause, "push_wake.send_err", "")
			continue
		}
		w.last[d.TokenHash] = now
		w.logDecision("relay: push_wake sent", d, cause, "push_wake.sent", "")
	}
}

// logDecision logs one device's wake decision at Info, with reason only on a
// skip. The field set is fixed here so no call site can add a token, a token
// hash or an error.
func (w *pushWaker) logDecision(msg string, d devices.Device, cause pushWakeCause, event, reason string) {
	attrs := []any{"event", event}
	if reason != "" {
		attrs = append(attrs, "reason", reason)
	}
	attrs = append(attrs,
		"device_name", d.Name,
		"conversation_id", cause.conversationID,
		"trigger", string(cause.trigger))
	w.logger.Info(msg, attrs...)
}
