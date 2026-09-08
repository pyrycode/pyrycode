//go:build e2e_realclaude

package realclaude

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/streamsup"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// Every file-local identifier takes the cedge prefix, for the reason #1260's header
// gives: siblings add files to this package concurrently and a branch-overlap check
// does not catch a same-package identifier collision.
const (
	cedgeWorkdirName = "cedge-work"
	// A fixed literal in a per-test temp $HOME, not a secret, and distinct from
	// every sibling probe's for the reason ccapSessionID gives.
	cedgeSessionID = "6b2f70c1-48ad-4f92-9c15-2e83d604af77"
)

// cedgeCollector is the event sink. Appends run on the os/exec forwarder goroutine
// that drives Parser.Write; the test goroutine polls, so the mutex is load-bearing
// rather than decorative — this is the one place in this test where two goroutines
// touch the same state.
type cedgeCollector struct {
	mu     sync.Mutex
	traces []string
}

// cedgeTrace renders one event as the token this test asserts on. Only the three
// kinds the acceptance criteria name are distinguished; everything else collapses to
// "other" so a turn's ordinary traffic does not have to be enumerated to be ignored.
//
// An Unrecognized carries its SITE and KIND into the token, and that is a scar
// rather than a flourish. The first live lap of this test failed with "2
// unrecognized_message frame(s)" and a trace whose every unrecognized entry read
// alike, so the red said a criterion was unmet and nothing whatever about which
// lines did it; the diagnosis took a separate census recovered from a sibling
// probe's artifact directory. A live gate this test cannot re-run at will has to
// spend its one lap saying something actionable. Site and Kind are the parser's own
// two discriminators and are BOUNDED — a site keyword and a message type, never the
// line — so carrying them cannot turn a failure message into a transcript dump.
func cedgeTrace(ev turnevent.Event) string {
	switch e := ev.(type) {
	case turnevent.Compacting:
		if e.Active {
			return "compacting:true"
		}
		return "compacting:false"
	case turnevent.TurnEnd:
		return "turn_end"
	case turnevent.Unrecognized:
		return fmt.Sprintf("%s%s/%s", cedgeUnrecognized, e.Site, e.Kind)
	default:
		return "other"
	}
}

// cedgeUnrecognized is the prefix every unrecognized token carries, so the AC-2
// assertion counts the class while the trace keeps the discriminators.
const cedgeUnrecognized = "unrecognized:"

func (c *cedgeCollector) add(ev turnevent.Event) {
	c.mu.Lock()
	c.traces = append(c.traces, cedgeTrace(ev))
	c.mu.Unlock()
}

func (c *cedgeCollector) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.traces...)
}

// count returns how many of the traces from index `from` onward equal want.
func (c *cedgeCollector) count(from int, want string) int {
	n := 0
	for _, tr := range c.snapshot()[from:] {
		if tr == want {
			n++
		}
	}
	return n
}

// countPrefix is count's sibling for a token class rather than a token: the
// unrecognized tokens carry their site and kind, so the AC-2 assertion has to
// count the family.
func (c *cedgeCollector) countPrefix(from int, prefix string) int {
	n := 0
	for _, tr := range c.snapshot()[from:] {
		if strings.HasPrefix(tr, prefix) {
			n++
		}
	}
	return n
}

// cedgeAwaitTurnEnds polls until the collector has seen `want` turn_end traces or
// the budget expires, reporting whether it got there. Bounded at the helper, never
// at the caller: an unbounded wait reached from a cleanup is what turned a bounded
// failure into a 20-minute suite kill in #2089.
func cedgeAwaitTurnEnds(c *cedgeCollector, want int, budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if c.count(0, "turn_end") >= want {
			return true
		}
		time.Sleep(ccapPoll)
	}
	return c.count(0, "turn_end") >= want
}

// cedgeAwaitCompactTurn waits for the compact turn to be over on the earliest of
// three conditions, returning which one fired. It deliberately does NOT depend on a
// `result` line: the /clear precedent (interactive_stream_announced_reset_test.go)
// records that a slash command sent as ordinary message text is honoured on this
// input path and that whether such a turn closes on its own is unknowable in
// advance, so a wait that only ended on a result could hang for the whole budget on
// a turn that already finished.
func cedgeAwaitCompactTurn(c *cedgeCollector, wantEnds, sentAt int, quiet, budget time.Duration) string {
	deadline := time.Now().Add(budget)
	lastLen, lastChange := len(c.snapshot()), time.Now()
	for time.Now().Before(deadline) {
		if c.count(0, "turn_end") >= wantEnds {
			return ccapTerminatedResult
		}
		if n := len(c.snapshot()); n != lastLen {
			lastLen, lastChange = n, time.Now()
		}
		if lastLen > sentAt && time.Since(lastChange) >= quiet {
			return ccapTerminatedQuiet
		}
		time.Sleep(ccapPoll)
	}
	return ccapTerminatedBudget
}

// TestRealClaude_CompactingEdges is #2227's live proof, and the two criteria it
// answers are claims about a real turn that no replay can make: exactly one
// compacting frame with active:true and then exactly one with active:false reach the
// event stream a real compacting turn produces (AC 1), and that same turn produces
// zero unrecognized_message frames (AC 2).
//
// IT DELIBERATELY READS NO FIXTURE, and that is a decision rather than a
// convenience. #2229's capture fired against claude 2.1.259 and reported
// shapes=[system/compact_boundary system/status], but the run was the dispatcher's
// gate-only real-claude lap, which verifies from a detached worktree and never runs
// `git add` — so the bytes were written in-repo and discarded with the worktree, and
// #2227's AC 3 named a fixture that does not exist. A live assertion cannot be lost
// that way: it asserts where it runs and reports its verdict through the gate's own
// pass/fail rather than through a file somebody has to commit afterwards.
//
// UNGATED, unlike its capture sibling. TestRealClaude_CompactionCapture arms on its
// fixture's absence and so costs nothing once the bytes land; this one runs on every
// `make e2e-realclaude` because it is a regression test, not a one-off measurement —
// what it watches for is a claude release moving the seam out from under the mapping,
// which is precisely the event a gated test would stop noticing.
//
// THE STAGING IS #2229'S, reused verbatim down to the model and the spawn shape: two
// rig-authored priming turns asking for long deterministic output with no tools, then
// `/compact` as ordinary message text. Keeping it identical is what makes a red here
// diagnosable against that capture rather than a second unexplained variable.
func TestRealClaude_CompactingEdges(t *testing.T) {
	claudeBin := resolveClaudeBin(t)
	home := WithWorktreeAuthenticated(t) // t.Skip when no credentials

	// A fresh EMPTY directory, deliberately not a git repo: no branch names and no
	// file contents can reach claude's context, and therefore none can reach a
	// compact summary.
	workdir := filepath.Join(home, cedgeWorkdirName)
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("#2227: create workdir: %v", err)
	}
	nonce := time.Now().UnixNano()

	collector := &cedgeCollector{}
	// The SHIPPED parser, wired exactly as production wires it (Config.Stdout), so
	// what this test observes is the mapping itself and not a re-implementation of
	// it. Its logger is discarded: the falling edge logs claude's compact_error, and
	// CI output is not where that belongs.
	parser := streamsup.NewParser(collector.add, slog.New(slog.NewTextHandler(io.Discard, nil)))

	runner, err := streamsup.New(streamsup.Config{
		ClaudeBin: claudeBin,
		WorkDir:   workdir,
		SessionID: cedgeSessionID,
		Args:      ccapArgs,
		Stdout:    parser,
	})
	if err != nil {
		t.Fatalf("#2227: streamsup.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = runner.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-runDone:
		case <-time.After(ccapRunExitWait):
			t.Errorf("#2227: streamsup.Run did not return within %s of cancel", ccapRunExitWait)
		}
	})

	stdin := dropcapWaitForChild(runner)
	if stdin == nil {
		t.Fatalf("#2227: no live child within %s: claude never spawned, so nothing was on the "+
			"wire to map", dropcapSpawnWait)
	}

	// --- staging ---------------------------------------------------------------
	primed := 0
	for turn := 0; turn < ccapPrimeTurns; turn++ {
		prompt := ccapPrimePrompt(turn*ccapPrimeSpan+1, (turn+1)*ccapPrimeSpan, nonce)
		if err := streamsup.WriteTurn(ctx, stdin, []byte(prompt)); err != nil {
			t.Fatalf("#2227: writing priming turn %d: %v", turn+1, err)
		}
		if !cedgeAwaitTurnEnds(collector, turn+1, ccapPrimeBudget) {
			break
		}
		primed = turn + 1
	}

	// The cut. Everything before it is staging; the criteria are about what comes
	// after, which is what makes "exactly one" a claim about the compact turn rather
	// than about the whole session.
	sentAt := len(collector.snapshot())

	// --- the compact turn -------------------------------------------------------
	if err := streamsup.WriteTurn(ctx, stdin, []byte(ccapCompactPrompt)); err != nil {
		t.Fatalf("#2227: writing the `/compact` turn: %v", err)
	}
	terminatedOn := cedgeAwaitCompactTurn(collector, primed+1, sentAt, ccapQuiet, ccapCompactBudget)

	turn := collector.snapshot()[sentAt:]
	rising := collector.count(sentAt, "compacting:true")
	falling := collector.count(sentAt, "compacting:false")

	// --- AC 1 --------------------------------------------------------------------
	// A zero-edge run says which of two things happened, copying ccapRecord.stagingVerdict's
	// separation: only the second is a finding about the mapping, and reading them as
	// one is how a rig that failed to stage a compactable conversation gets mistaken
	// for a parser that stopped mapping.
	if rising == 0 {
		t.Fatalf("#2227: the `/compact` turn produced NO compacting:true across %d event(s) "+
			"(primed %d/%d turn(s), terminated_on=%s).\n"+
			"  Read this first: %d priming turn(s) completed. Fewer than %d means the rig never "+
			"staged a compactable conversation and this says nothing about the mapping; %d of %d "+
			"means claude declined to compact or the seam moved, which IS a finding.\n"+
			"  trace: %v",
			len(turn), primed, ccapPrimeTurns, terminatedOn,
			primed, ccapPrimeTurns, primed, ccapPrimeTurns, turn)
	}
	if rising != 1 || falling != 1 {
		t.Fatalf("#2227: the `/compact` turn produced %d compacting:true and %d compacting:false, "+
			"want exactly 1 of each (terminated_on=%s). More than one rising edge means the "+
			"idempotency arm stopped suppressing a repeat; a missing falling edge is the stuck "+
			"banner AC 5 forbids.\n  trace: %v", rising, falling, terminatedOn, turn)
	}
	if err := cedgeAssertOrder(turn); err != nil {
		t.Fatalf("#2227: %v (terminated_on=%s)\n  trace: %v", err, terminatedOn, turn)
	}

	// --- AC 2 --------------------------------------------------------------------
	// Nothing the compact turn emits may reach a client as a noise row. The plan
	// argued this was structural — the compaction lines are all `system` subtypes,
	// and an unmapped one is dropped by ignoredLineTypes rather than surfaced — and
	// the first live lap falsified that: the turn's census was `system/status: 2,
	// system/compact_boundary: 1, system/init: 1, user: 2, result/success: 1`, and
	// the two USER lines were the frames. They are compaction's consequences rather
	// than compaction lines by subtype (claude's summary re-seeding the context, and
	// the slash command's stdout echo), they carry string content so they failed
	// streamLine's decode ahead of the suppression that already covered them, and
	// the frame that reached the wire carried the whole summary as its Raw. See
	// dropHarnessProseLine. This assertion is what holds that closed on the surface
	// where it was found.
	if n := collector.countPrefix(sentAt, cedgeUnrecognized); n != 0 {
		t.Fatalf("#2227: the `/compact` turn produced %d unrecognized_message frame(s), want 0. "+
			"The trace names each one as unrecognized:<site>/<kind> — read those first, since "+
			"site and kind are what say whether a line arrived on an envelope the parser does not "+
			"claim or failed to decode at all.\n  trace: %v", n, turn)
	}
}

// cedgeAssertOrder checks that the rising edge precedes the falling one and that no
// turn_end sits between them. The ORDER is the substance of AC 1 — two frames in the
// wrong order light a banner that was already meant to be dark — and a turn_end
// between them would mean the client closed the turn holding a lit banner, which is
// the stuck banner AC 5 forbids arriving by a different route than a missing edge.
func cedgeAssertOrder(turn []string) error {
	first, last := -1, -1
	for i, tr := range turn {
		if tr == "compacting:true" && first < 0 {
			first = i
		}
		if tr == "compacting:false" {
			last = i
		}
	}
	if first < 0 || last < 0 || first > last {
		return fmt.Errorf("edges out of order: compacting:true at %d, compacting:false at %d",
			first, last)
	}
	for _, tr := range turn[first:last] {
		if tr == "turn_end" {
			return fmt.Errorf("a turn_end arrived between the rising edge at %d and the falling "+
				"edge at %d, so the turn closed with the banner still lit", first, last)
		}
	}
	return nil
}
