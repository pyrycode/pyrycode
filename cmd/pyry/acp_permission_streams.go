package main

import (
	"context"
	"time"

	"github.com/pyrycode/pyrycode/internal/supervisor"
	"github.com/pyrycode/pyrycode/internal/turnbridge"
	"github.com/pyrycode/tui-driver/pkg/tuidriver"
)

// acpPermissionTimeout bounds each outbound session/request_permission Call: a
// host that never answers denies-and-unblocks after this window rather than
// wedging claude. Mirrors the daemon's modalDenyTimeout (relay/`ErrTransportDown`)
// for policy consistency across the mobile and ACP permission legs. A package
// const, not a constructor param — the value is fixed policy, so plumbing it
// through newACPTurnStreams would be churn for no configurability.
const acpPermissionTimeout = 2 * time.Minute

// startPermissionProxy live-wires the frozen acpPermissionProxy (#752) into the
// running ACP session — the twin of the turn producer start() spawns. It supplies
// the proxy's three collaborators — m.transport as the permissionCaller, host as
// the modalKeystroker AND the turnbridge.SessionHost, and host's raw tui-driver
// event stream as the modal-event source — then spawns the drain under m.wg so
// wait() joins it alongside the turn producer.
//
// The subscriber is a fixed target identical in shape to the turn stream's: one
// session, no re-key (Switch nil), a FRESH resolveBoundSessionJSONL + Tracker per
// subscription (its own merge loop, no shared parse state with the turn stream's
// Tracker). dir == "" never reaches here — start() returns before calling this —
// so the permission drain is disabled by the same guard that disables the turn
// stream, correct because the drain cannot open the JSONL-gated event stream
// without a resolvable sessions dir.
func (m *acpTurnStreams) startPermissionProxy(host *supervisor.Supervisor, sessionID string) {
	proxy := newACPPermissionProxy(m.transport, host, sessionID, acpPermissionTimeout, m.logger)

	// A fresh Tracker + resolver per session mirrors the turn stream's
	// one-tracker-per-stream: two Session.Events() subscriptions on the same bound
	// session, each with its own independent merge loop and parse state.
	resolve := func(ctx context.Context) (turnbridge.Target, error) {
		return turnbridge.Target{
			Host:    host,
			Resolve: resolveBoundSessionJSONL(m.dir, sessionID),
			Switch:  nil,
		}, nil
	}
	sub := turnbridge.NewTargetSubscriber(resolve, tuidriver.NewTracker(tuidriver.TrackerOpts{}), m.logger)

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		runPermissionModalStream(m.ctx, sub, proxy)
	}()
}

// runPermissionModalStream is the permission-modal drain: the ACP sibling of
// runModalStream, simpler because the proxy's Handle owns the kind filter (it
// no-ops every non-permission event), so this loop hands every raw event straight
// through rather than pre-filtering. It is production code AND the deterministic
// test seam #754 consumes — a test drives it with a scriptedSubscriber + a proxy
// over scripted doubles, injecting a permission-modal event with no live claude.
//
//   - Outer: sub(ctx) errors only on ctx cancel (Subscriber contract) → return.
//     A closed channel (session restart) breaks back to re-subscribe onto the
//     now-live session (follow-restart), same as the turn producer.
//   - Inner: ctx.Done() → return; a received event → proxy.Handle; a closed
//     channel → break to re-subscribe.
//
// It is the SOLE caller of proxy.Handle, so the proxy's single-goroutine invariant
// (p.inflight touched only here) holds. Logs nothing itself (mirrors
// runModalStream): the proxy owns all logging, all content-free.
func runPermissionModalStream(ctx context.Context, sub turnbridge.Subscriber, proxy *acpPermissionProxy) {
	for {
		ch, err := sub(ctx)
		if err != nil {
			return // ctx cancel — the only error Subscribe yields per its contract
		}
	drain:
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-ch:
				if !ok {
					break drain // session restart — re-subscribe onto the now-live session
				}
				proxy.Handle(ctx, ev)
			}
		}
		if ctx.Err() != nil {
			return
		}
	}
}
