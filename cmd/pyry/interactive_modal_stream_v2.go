package main

import (
	"context"
	"log/slog"

	"github.com/pyrycode/pyrycode/internal/modalbridge"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/sessions/rotation"
	"github.com/pyrycode/pyrycode/internal/supervisor"
	"github.com/pyrycode/pyrycode/internal/turnbridge"
	"github.com/pyrycode/tui-driver/pkg/tuidriver"
)

// screenSnapshotter is the bound host's rendered-screen seam. *supervisor.Supervisor
// satisfies it (`readyForDelivery`); asserted from turnbridge.SessionHost so the
// shared turnbridge interface stays screen-free. Mirrors relay.ScreenSnapshotter
// (v2session.go:480), which the manager consumes for request_snapshot.
type screenSnapshotter interface {
	ScreenSnapshot() (text string, live bool)
}

// startInteractiveModalStreamV2 live-wires the frozen interactiveModalEmitterV2
// (#716/#717/#725/#706) into the running daemon so a real claude permission/trust
// prompt surfaces as a modal_shown, arms the fail-closed deny-on-timeout, and — on
// a local-attach dismissal — broadcasts modal_dismissed{local}. It mirrors
// startInteractiveTurnStreamV2: construct the emitter over the daemon-singleton
// modal registry (the same modalReg the inbound resolver consumes) with mgr as
// both the capability-gated broadcaster (ActiveConns/Push) and the deny-on-timeout
// armer (ArmModalTimeout), reuse the follow-active resolveTarget +
// NewTargetSubscriber (which yields RAW tuidriver.Events — the turnbridge modal-
// dropping mapper lives downstream in Producer.drain, which this stream does not
// use), spawn one goroutine over a mapper-free drain, and return a cleanup that
// blocks until that goroutine exits.
//
// This opens a SECOND Session.Events() subscription on the bound *tuidriver.Session
// alongside the turn stream's; tui-driver blesses that explicitly (events.go:160 —
// two Events() calls spawn two independent merge loops, each over its own JSONL
// tail + poll loop, sharing no mutable state). The modal drain ignores every
// non-modal event, so the extra JSONL tail bytes are never inspected.
//
// sup is the pre-route bootstrap host (AC4), both the follow-active resolver's
// bootstrap SessionHost and the bootstrap screenSnapshotter. Since #678 routes to
// bound-session supervisors, a modal raised on a bound session resolves that
// host's own event stream AND its own screen (AC3).
func startInteractiveModalStreamV2(
	ctx context.Context,
	sup *supervisor.Supervisor,
	active *activeConversation,
	boundHost boundHostFunc,
	mgr *relay.V2SessionManager,
	modalReg *modalbridge.Registry,
	claudeSessionsDir string,
	probe rotation.Probe,
	pidFn func() int,
	bootstrapIDFn func() string,
	logger *slog.Logger,
) func() {
	// mgr is both the interactiveBroadcaster (ActiveConns/Push, already the turn
	// emitter's broadcaster) and the modalTimeoutArmer (ArmModalTimeout, #725).
	emitter := newInteractiveModalEmitterV2(modalReg, mgr, mgr, logger)

	resolve := resolveTarget(active, boundHost, sup, claudeSessionsDir, probe, pidFn, bootstrapIDFn)
	// Screen-only subscription (#1070): a modal comes off the PTY grid, not the
	// JSONL, so the modal stream must NOT wait for a session transcript. A minted
	// per-conversation session blocked on a permission prompt writes no transcript,
	// so a transcript-gated subscription would never open — deadlocking the very
	// modal that would unblock it.
	sub := turnbridge.NewScreenTargetSubscriber(resolve, logger)
	screenFor := boundScreenText(active, boundHost, sup)

	done := make(chan struct{})
	go func() {
		defer close(done)
		runModalStream(ctx, sub, screenFor, emitter)
	}()
	return func() { <-done }
}

// boundScreenText yields the current active conversation id paired with that
// bound host's rendered screen text — BOTH from a SINGLE active.CurrentConversation()
// read (#1065). Pairing them is the security core: the screen the modal carries
// and the conversation_id it is stamped for derive from one cursor value, so they
// cannot diverge (a re-read could stamp conversation A's screen with B's id — the
// exact leak). It mirrors resolveTarget's host-selection branch so the paired
// screen (AC3) comes from the SAME supervisor as the modal event:
//
//   - convID == "" (no route yet) → ("", bootstrap screen), the pre-route path
//     that matches resolveTarget's bootstrap branch (AC4 consistency). There are
//     no concurrent per-conversation sessions to leak to yet.
//   - convID != "" and boundHost ok → (convID, that host's screen text) via a
//     type-assert of the returned SessionHost to screenSnapshotter (in production
//     it is *supervisor.Supervisor, which satisfies it).
//   - boundHost miss, or the host does not satisfy screenSnapshotter (defensive)
//     → (convID, ""). An empty screen is safe: the emitter surfaces a modal with
//     an empty Title and the security-critical fields (modal_id, class, options,
//     conversation_id) are screen-independent.
//
// Only text is used from ScreenSnapshot; the live bool is ignored (an empty text
// is the natural degrade). NEVER logs the returned text. active never resets to
// "" once a route lands (activeConversation.set only advances to non-empty ids),
// so a modal on a bound session always resolves that bound host's screen, never a
// stale bootstrap.
func boundScreenText(active *activeConversation, boundHost boundHostFunc, bootstrap screenSnapshotter) func() (string, string) {
	return func() (string, string) {
		convID := active.CurrentConversation()
		if convID == "" {
			text, _ := bootstrap.ScreenSnapshot()
			return "", text
		}
		host, _, _, ok := boundHost(convID)
		if !ok {
			return convID, ""
		}
		snap, ok := host.(screenSnapshotter)
		if !ok {
			return convID, ""
		}
		text, _ := snap.ScreenSnapshot()
		return convID, text
	}
}

// runModalStream is the bespoke follow-active drain: the outer re-subscribe loop +
// inner drain that mirrors turnbridge Producer.Run/drain but over RAW
// tuidriver.Events (no turnbridge.Producer, no mapper). It is the SOLE caller of
// emitter.Handle, so the emitter's unguarded counters (nextID, outstandingID,
// outstandingClass) never race — the single-Run-goroutine invariant the emitter
// documents (`interactiveModalEmitterV2`). The one field a second goroutine touches
// is the modal registry (shared with the inbound resolver), which carries its own
// mutex (#717/#706, unchanged).
//
//   - Outer: sub(ctx) errors only on ctx cancel (Subscriber contract) → return.
//     A closed channel (session restart) breaks back to re-subscribe onto the
//     now-active target (follow-active).
//   - Inner: on EventKindPtyModalShown evaluate screenFor ONLY here — so no
//     ScreenSnapshot render happens on the 50 ms idle/thinking/JSONL ticks — and
//     hand its paired (convID, screen) to Handle from ONE cursor read (#1065), so
//     the modal's content and its scope stamp cannot diverge.
//     EventKindPtyModalHidden needs neither (the body is already gone;
//     interactive_modal_v2.go). Every other kind is ignored.
func runModalStream(ctx context.Context, sub turnbridge.Subscriber, screenFor func() (string, string), emitter *interactiveModalEmitterV2) {
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
					break drain // session restart — re-subscribe onto the now-active target
				}
				switch ev.Kind {
				case tuidriver.EventKindPtyModalShown:
					convID, screen := screenFor() // ONE active.CurrentConversation() read
					emitter.Handle(ctx, ev, convID, screen)
				case tuidriver.EventKindPtyModalHidden:
					emitter.Handle(ctx, ev, "", "")
				}
			}
		}
		if ctx.Err() != nil {
			return
		}
	}
}
