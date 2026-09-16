package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

const ctxUsageTestConv = conversations.ConversationID("CONVERSATION_2431")

// fakeContextUsageQuerier counts round trips and reports what it was asked for. The
// detail it records is the assertion that the daemon — not the client — chooses it.
type fakeContextUsageQuerier struct {
	calls   atomic.Int64
	details chan string

	// block, when non-nil, holds each call until closed. It is how a test keeps a
	// flight in flight while a second ask arrives, with no sleep anywhere.
	block chan struct{}

	usage turnevent.ContextUsage
	ok    bool

	// ctxErr records whether the query's own context was already done when the
	// querier ran — the observable half of "the deadline is real".
	ctxErr atomic.Bool
}

func (f *fakeContextUsageQuerier) QueryContextUsage(ctx context.Context, detail string) (turnevent.ContextUsage, bool) {
	f.calls.Add(1)
	select {
	case f.details <- detail:
	default:
	}
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			f.ctxErr.Store(true)
			return turnevent.ContextUsage{}, false
		}
	}
	if ctx.Err() != nil {
		f.ctxErr.Store(true)
	}
	return f.usage, f.ok
}

func newFakeContextUsageQuerier(ok bool) *fakeContextUsageQuerier {
	return &fakeContextUsageQuerier{
		details: make(chan string, 8),
		ok:      ok,
		usage: turnevent.ContextUsage{
			Model:       "MODEL_2431",
			TotalTokens: 31337,
			MaxTokens:   200000,
			Percentage:  16,
		},
	}
}

// fakeClock is the injected time source. Advancing it is how the collapse window is
// crossed without sleeping on a wall clock, which is exactly what AC-2 asks for.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func ctxUsageResolverFor(t *testing.T, q *fakeContextUsageQuerier, busy *turnBusyTracker, clock *fakeClock) *contextUsageResolver {
	t.Helper()
	resolve := func(convID string) (contextUsageQuerier, conversations.ConversationID, bool) {
		if convID != string(ctxUsageTestConv) {
			return nil, "", false
		}
		return q, ctxUsageTestConv, true
	}
	return newContextUsageResolver(context.Background(), resolve, busy, clock.now)
}

// TestContextUsageResolver_AsksAtFullDetail pins that the daemon asks for the
// EXPENSIVE reading. The cheap "summary" is what the automatic post-turn path
// already publishes; a verb that asked for it too would cost a round trip to
// deliver what the client had moments ago.
func TestContextUsageResolver_AsksAtFullDetail(t *testing.T) {
	t.Parallel()
	q := newFakeContextUsageQuerier(true)
	r := ctxUsageResolverFor(t, q, nil, newFakeClock())

	if _, ok := r.Get(context.Background(), string(ctxUsageTestConv)); !ok {
		t.Fatal("Get refused a resolvable conversation")
	}
	select {
	case got := <-q.details:
		if got != "full" {
			t.Errorf("asked claude for detail %q, want %q", got, "full")
		}
	default:
		t.Fatal("querier was never asked")
	}
}

// TestContextUsageResolver_CollapsesInFlight is AC-2's first side: a second ask
// arriving while the first is still in flight joins it rather than writing a second
// request to the child. No sleep — the second ask is released by the barrier the
// first is parked on.
func TestContextUsageResolver_CollapsesInFlight(t *testing.T) {
	t.Parallel()
	q := newFakeContextUsageQuerier(true)
	q.block = make(chan struct{})
	r := ctxUsageResolverFor(t, q, nil, newFakeClock())

	type result struct {
		payload protocol.ContextUsagePayload
		ok      bool
	}
	results := make(chan result, 2)
	// The first ask parks inside the querier; the second must find its flight.
	go func() {
		p, ok := r.Get(context.Background(), string(ctxUsageTestConv))
		results <- result{p, ok}
	}()
	// Wait until the querier is actually running, so the second ask cannot win the
	// race to install the flight — the barrier replaces a sleep here too.
	select {
	case got := <-q.details:
		if got != "full" {
			t.Errorf("detail = %q, want full", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first ask never reached the querier")
	}
	go func() {
		p, ok := r.Get(context.Background(), string(ctxUsageTestConv))
		results <- result{p, ok}
	}()

	close(q.block)
	var got []result
	for range 2 {
		select {
		case res := <-results:
			got = append(got, res)
		case <-time.After(3 * time.Second):
			t.Fatal("an ask never returned")
		}
	}

	if calls := q.calls.Load(); calls != 1 {
		t.Errorf("querier called %d times, want exactly 1 — closely-spaced asks collapse to one round trip", calls)
	}
	for i, res := range got {
		if !res.ok {
			t.Errorf("ask %d refused; both must be answered from the one result", i)
		}
		if res.payload.TotalTokens != 31337 {
			t.Errorf("ask %d payload = %+v, want the flight's reading", i, res.payload)
		}
	}
}

// TestContextUsageResolver_CollapseWindow is AC-2's second side, both halves of it:
// a second ask just after the first COMPLETED is answered from its result, and one
// past the window earns a fresh round trip. The clock is injected, so the boundary is
// crossed by advancing it rather than by sleeping on it.
func TestContextUsageResolver_CollapseWindow(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		advance   time.Duration
		wantCalls int64
	}{
		{"inside the window reuses the reading", contextUsageCollapseWindow - time.Millisecond, 1},
		{"at the window boundary asks again", contextUsageCollapseWindow, 2},
		{"past the window asks again", contextUsageCollapseWindow + time.Second, 2},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			q := newFakeContextUsageQuerier(true)
			clock := newFakeClock()
			r := ctxUsageResolverFor(t, q, nil, clock)

			if _, ok := r.Get(context.Background(), string(ctxUsageTestConv)); !ok {
				t.Fatal("first ask refused")
			}
			clock.advance(tc.advance)
			if _, ok := r.Get(context.Background(), string(ctxUsageTestConv)); !ok {
				t.Fatal("second ask refused")
			}
			if got := q.calls.Load(); got != tc.wantCalls {
				t.Errorf("querier called %d times, want %d", got, tc.wantCalls)
			}
		})
	}
}

// TestContextUsageResolver_CollapsesRefusalsToo pins the decision recorded in the
// plan: a refusal is cached for the window like any other result. The alternative —
// caching only successes — would leave a conversation whose child cannot answer free
// to be re-asked as fast as a client can type.
func TestContextUsageResolver_CollapsesRefusalsToo(t *testing.T) {
	t.Parallel()
	q := newFakeContextUsageQuerier(false)
	r := ctxUsageResolverFor(t, q, nil, newFakeClock())

	for i := range 3 {
		if _, ok := r.Get(context.Background(), string(ctxUsageTestConv)); ok {
			t.Fatalf("ask %d succeeded against a refusing querier", i)
		}
	}
	if got := q.calls.Load(); got != 1 {
		t.Errorf("querier called %d times, want exactly 1 — a refusal collapses like any other result", got)
	}
}

// TestContextUsageResolver_UnresolvableInstallsNoFlight is the containment property
// from the security pass: a conversation the daemon cannot resolve must not create a
// map entry, or a client could mint them by naming ids that do not exist.
//
// It asserts on the FLIGHT MAP rather than on the refusal, because the refusal is
// what a resolver keying the map on the request string would also produce.
func TestContextUsageResolver_UnresolvableInstallsNoFlight(t *testing.T) {
	t.Parallel()
	q := newFakeContextUsageQuerier(true)
	r := ctxUsageResolverFor(t, q, nil, newFakeClock())

	for _, id := range []string{"NOT_HOSTED_2431", "", "../../etc/passwd"} {
		if _, ok := r.Get(context.Background(), id); ok {
			t.Errorf("Get(%q) succeeded, want a refusal", id)
		}
	}
	r.mu.Lock()
	n := len(r.flights)
	r.mu.Unlock()
	if n != 0 {
		t.Errorf("flight map holds %d entries after unresolvable asks, want 0 — entries are keyed by the resolved id and installed only past resolution", n)
	}
	if got := q.calls.Load(); got != 0 {
		t.Errorf("querier called %d times for unresolvable conversations, want 0", got)
	}
}

// TestContextUsageResolver_FlightKeyedByCanonicalID pins the other half of that
// finding: the key is the id the REGISTRY returned, not the string the client sent.
// The resolver double below accepts a differently-spelled request and canonicalises
// it, which is what a normalising registry would do — a resolver keying on the
// request string would install two flights and make two round trips.
func TestContextUsageResolver_FlightKeyedByCanonicalID(t *testing.T) {
	t.Parallel()
	q := newFakeContextUsageQuerier(true)
	resolve := func(string) (contextUsageQuerier, conversations.ConversationID, bool) {
		return q, ctxUsageTestConv, true
	}
	r := newContextUsageResolver(context.Background(), resolve, nil, newFakeClock().now)

	for _, spelling := range []string{"spelling-one", "spelling-two"} {
		if _, ok := r.Get(context.Background(), spelling); !ok {
			t.Fatalf("Get(%q) refused", spelling)
		}
	}
	if got := q.calls.Load(); got != 1 {
		t.Errorf("querier called %d times, want 1 — two spellings of one conversation collapse together", got)
	}
	r.mu.Lock()
	n := len(r.flights)
	r.mu.Unlock()
	if n != 1 {
		t.Errorf("flight map holds %d entries, want 1 keyed by the canonical id", n)
	}
}

// TestContextUsageResolver_DefersUntilTurnEnds is AC-4: a request arriving while a
// turn is in flight writes NOTHING to the child until that turn ends, and is answered
// after it. The tracker is the real one, so the deferral is pinned against the
// primitive that actually ships.
func TestContextUsageResolver_DefersUntilTurnEnds(t *testing.T) {
	t.Parallel()
	busy := newTurnBusyTracker(func(string) (string, bool) { return "", false }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	undo := busy.openForDelivery(string(ctxUsageTestConv))
	if !busy.Busy(string(ctxUsageTestConv)) {
		t.Fatal("tracker did not report the conversation busy")
	}

	q := newFakeContextUsageQuerier(true)
	r := ctxUsageResolverFor(t, q, busy, newFakeClock())

	done := make(chan bool, 1)
	go func() {
		_, ok := r.Get(context.Background(), string(ctxUsageTestConv))
		done <- ok
	}()

	// While the turn is open the child must not be written to at all. A negative is
	// only as good as the window it is observed over; this one is bounded by the
	// tracker's own state, which nothing but `undo` can change.
	select {
	case detail := <-q.details:
		t.Fatalf("querier was asked for %q while a turn was open; the request must wait", detail)
	case <-time.After(150 * time.Millisecond):
	}
	if got := q.calls.Load(); got != 0 {
		t.Fatalf("querier called %d times mid-turn, want 0", got)
	}

	undo()
	select {
	case ok := <-done:
		if !ok {
			t.Error("the deferred request was refused; it must be answered after the turn ends")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the deferred request never completed after the turn closed")
	}
	if got := q.calls.Load(); got != 1 {
		t.Errorf("querier called %d times after the turn closed, want 1", got)
	}
}

// TestContextUsageResolver_NilTrackerIsNotDereferenced pins the PTY wiring, where the
// tracker is never constructed. WaitIdle carries no nil-receiver guard of its own, so
// the resolver owns this check.
func TestContextUsageResolver_NilTrackerIsNotDereferenced(t *testing.T) {
	t.Parallel()
	q := newFakeContextUsageQuerier(true)
	r := ctxUsageResolverFor(t, q, nil, newFakeClock())

	if _, ok := r.Get(context.Background(), string(ctxUsageTestConv)); !ok {
		t.Fatal("Get refused with a nil tracker; a nil tracker means nothing is busy")
	}
}

// TestContextUsageResolver_CallerDepartureLeavesFlightRunning is the security pass's
// second MUST FIX, pinned. A flight is SHARED: if it ran under the first caller's
// context, that client disconnecting would cancel a round trip other clients are
// waiting on and — refusals being cached — hold the resulting refusal for the whole
// window. One client's departure must not deny another's answer.
func TestContextUsageResolver_CallerDepartureLeavesFlightRunning(t *testing.T) {
	t.Parallel()
	q := newFakeContextUsageQuerier(true)
	q.block = make(chan struct{})
	r := ctxUsageResolverFor(t, q, nil, newFakeClock())

	leaver, cancelLeaver := context.WithCancel(context.Background())
	left := make(chan bool, 1)
	go func() {
		_, ok := r.Get(leaver, string(ctxUsageTestConv))
		left <- ok
	}()
	select {
	case <-q.details:
	case <-time.After(3 * time.Second):
		t.Fatal("the first ask never reached the querier")
	}

	// A second client joins the flight the first installed, then the first leaves.
	stayed := make(chan bool, 1)
	go func() {
		_, ok := r.Get(context.Background(), string(ctxUsageTestConv))
		stayed <- ok
	}()
	cancelLeaver()
	select {
	case ok := <-left:
		if ok {
			t.Error("the departing caller reported success; its own ctx ended first")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the departing caller never returned")
	}

	// The flight itself must still be alive: releasing the child now answers the
	// caller that stayed.
	close(q.block)
	select {
	case ok := <-stayed:
		if !ok {
			t.Error("the remaining caller was refused — one client's departure cancelled a shared flight")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the remaining caller never returned; the flight died with the departing caller")
	}
	if got := q.ctxErr.Load(); got {
		t.Error("the query's context was cancelled by a caller's departure; the flight runs under the daemon context")
	}
}

// TestContextUsageResolver_NilSeamsRefuse pins the unwired postures — foreground and
// v1 leave these nil — as refusals rather than panics.
func TestContextUsageResolver_NilSeamsRefuse(t *testing.T) {
	t.Parallel()
	var nilResolver *contextUsageResolver
	if _, ok := nilResolver.Get(context.Background(), string(ctxUsageTestConv)); ok {
		t.Error("a nil resolver answered a request")
	}
	r := newContextUsageResolver(context.Background(), nil, nil, nil)
	if _, ok := r.Get(context.Background(), string(ctxUsageTestConv)); ok {
		t.Error("a resolver with no conversation source answered a request")
	}
}

// --- #2460: the registry memory -----------------------------------------------

const ctxUsageRecordedConv = conversations.ConversationID("11111111-2222-4333-8444-555555555555")

// ctxUsageRecorderFor builds a recorder over a registry holding one row for
// ctxUsageRecordedConv, a registry path inside a fresh temp dir, a pinned clock,
// and a capturing logger.
func ctxUsageRecorderFor(t *testing.T) (*contextUsageRecorder, string, *fakeClock, *bytes.Buffer) {
	t.Helper()
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{
		ID:         ctxUsageRecordedConv,
		Cwd:        "/home/user/project",
		LastUsedAt: time.Date(2026, 9, 14, 11, 0, 0, 0, time.UTC),
	})
	path := filepath.Join(t.TempDir(), "conversations.json")
	clock := newFakeClock()
	logger, buf := auditLogger()
	return &contextUsageRecorder{reg: reg, path: path, logger: logger, now: clock.now}, path, clock, buf
}

// ctxUsageLogRecords parses every JSON log line in buf.
func ctxUsageLogRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var recs []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line is not JSON: %q: %v", line, err)
		}
		recs = append(recs, rec)
	}
	return recs
}

// TestContextUsageRecorder_RecordsSummaryAndPersists is AC-1 and AC-4 at the daemon
// boundary: the five summary values reach the file with as_of from the daemon
// clock, and NONE of the reading's category names, MCP server names or memory-file
// paths do. The needles come from #2371's fixture, so a cross-wired field reddens.
func TestContextUsageRecorder_RecordsSummaryAndPersists(t *testing.T) {
	t.Parallel()
	rec, path, clock, buf := ctxUsageRecorderFor(t)

	rec.record(ctxUsageRecordedConv, emitterContextUsageFixture)

	back, err := conversations.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, ok := back.Get(ctxUsageRecordedConv)
	if !ok {
		t.Fatal("row missing after reload")
	}
	if got.LastContextUsage == nil {
		t.Fatal("LastContextUsage = nil on disk")
	}
	want := conversations.ContextUsageReading{
		Model:       emitterContextUsageFixture.Model,
		TotalTokens: emitterContextUsageFixture.TotalTokens,
		MaxTokens:   emitterContextUsageFixture.MaxTokens,
		Percentage:  emitterContextUsageFixture.Percentage,
		AsOf:        clock.now().UTC(),
	}
	if *got.LastContextUsage != want {
		t.Errorf("stored reading = %+v, want %+v", *got.LastContextUsage, want)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}
	for _, needle := range contextUsageNeedles() {
		if needle == emitterContextUsageFixture.Model {
			continue // the model IS stored, by AC-1
		}
		if strings.Contains(string(data), needle) {
			t.Errorf("registry file carries %q — the three inventories must never reach disk", needle)
		}
	}
	if recs := ctxUsageLogRecords(t, buf); len(recs) != 0 {
		t.Errorf("a successful record logged %d records, want 0: %v", len(recs), recs)
	}
}

// TestContextUsageRecorder_UnknownConversationWritesNothing is AC-2's second half
// in its strongest available form: an id with no row leaves no registry file on
// disk at all, so "writes nothing AND saves nothing" is one assertion.
func TestContextUsageRecorder_UnknownConversationWritesNothing(t *testing.T) {
	t.Parallel()
	rec, path, _, buf := ctxUsageRecorderFor(t)

	rec.record("99999999-0000-4000-8000-000000000000", emitterContextUsageFixture)

	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Stat(%s) err = %v, want ErrNotExist — a miss must not Save", path, err)
	}
	if recs := ctxUsageLogRecords(t, buf); len(recs) != 0 {
		t.Errorf("a miss logged %d records, want 0: %v", len(recs), recs)
	}
}

// TestContextUsageRecorder_SaveFailureLogsIDAndErrorOnly is AC-4's log side: the
// failure record carries the conversation id and the registry error, and nothing
// else — no category name, no MCP server name, no memory-file path.
func TestContextUsageRecorder_SaveFailureLogsIDAndErrorOnly(t *testing.T) {
	t.Parallel()
	rec, path, _, buf := ctxUsageRecorderFor(t)
	// A regular file where Save wants a directory component: MkdirAll fails with
	// ENOTDIR, deterministically and without a permission dance.
	blocker := filepath.Join(filepath.Dir(path), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	rec.path = filepath.Join(blocker, "sub", "conversations.json")

	rec.record(ctxUsageRecordedConv, emitterContextUsageFixture)

	recs := ctxUsageLogRecords(t, buf)
	if len(recs) != 1 {
		t.Fatalf("save failure logged %d records, want 1: %v", len(recs), recs)
	}
	got := recs[0]
	if got["conversation_id"] != string(ctxUsageRecordedConv) {
		t.Errorf("conversation_id = %v, want %q", got["conversation_id"], ctxUsageRecordedConv)
	}
	if _, ok := got["err"]; !ok {
		t.Errorf("record carries no err: %v", got)
	}
	wantKeys := map[string]bool{"time": true, "level": true, "msg": true, "event": true, "conversation_id": true, "err": true}
	for key := range got {
		if !wantKeys[key] {
			t.Errorf("record carries unexpected field %q — AC-4 allows the id and the registry error and nothing else: %v", key, got)
		}
	}
	for _, needle := range contextUsageNeedles() {
		if strings.Contains(buf.String(), needle) {
			t.Errorf("save-failure record carries %q from the reading", needle)
		}
	}

	// The in-memory row still holds the reading: the write is what failed, not the
	// set, so the next settle retries against a row that is already current.
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Stat(%s) err = %v, want ErrNotExist", path, err)
	}
}

// TestContextUsageRecorder_NilIsInert pins the posture every optional seam in this
// binary keeps, and is why every existing emitter and resolver test stays green
// with no edit: an unwired daemon records nothing and panics on nothing.
func TestContextUsageRecorder_NilIsInert(t *testing.T) {
	t.Parallel()
	var nilRec *contextUsageRecorder
	nilRec.record(ctxUsageRecordedConv, emitterContextUsageFixture)

	logger, buf := auditLogger()
	unwired := &contextUsageRecorder{path: filepath.Join(t.TempDir(), "conversations.json"), logger: logger}
	unwired.record(ctxUsageRecordedConv, emitterContextUsageFixture)

	if _, err := os.Stat(unwired.path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Stat err = %v, want ErrNotExist — a nil registry must not Save", err)
	}
	if recs := ctxUsageLogRecords(t, buf); len(recs) != 0 {
		t.Errorf("an unwired recorder logged %d records, want 0: %v", len(recs), recs)
	}
}

// TestContextUsageResolver_SettledFlightRecordsReading is AC-3: a reading the
// on-demand verb obtained is remembered, so the stored value is whichever reading
// claude produced last rather than whichever producer happens to be wired.
func TestContextUsageResolver_SettledFlightRecordsReading(t *testing.T) {
	t.Parallel()
	q := newFakeContextUsageQuerier(true)
	q.usage = emitterContextUsageFixture
	rec, path, clock, _ := ctxUsageRecorderFor(t)

	resolve := func(convID string) (contextUsageQuerier, conversations.ConversationID, bool) {
		if convID != string(ctxUsageRecordedConv) {
			return nil, "", false
		}
		return q, ctxUsageRecordedConv, true
	}
	r := newContextUsageResolver(context.Background(), resolve, nil, clock.now)
	r.rec = rec

	if _, ok := r.Get(context.Background(), string(ctxUsageRecordedConv)); !ok {
		t.Fatal("Get refused a resolvable conversation")
	}

	// The record lands inside the settle defer, after close(done) — so the Get
	// above may return before the write. Wait on the observable outcome rather
	// than on a sleep.
	var got conversations.Conversation
	for range 200 {
		back, err := conversations.Load(path)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if row, ok := back.Get(ctxUsageRecordedConv); ok && row.LastContextUsage != nil {
			got = row
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got.LastContextUsage == nil {
		t.Fatal("a settled ok flight recorded no reading")
	}
	want := conversations.ContextUsageReading{
		Model:       emitterContextUsageFixture.Model,
		TotalTokens: emitterContextUsageFixture.TotalTokens,
		MaxTokens:   emitterContextUsageFixture.MaxTokens,
		Percentage:  emitterContextUsageFixture.Percentage,
		AsOf:        clock.now().UTC(),
	}
	if *got.LastContextUsage != want {
		t.Errorf("stored reading = %+v, want %+v", *got.LastContextUsage, want)
	}
}

// TestContextUsageResolver_RefusedFlightRecordsNothing is AC-3's other side: the
// memory holds readings claude produced, never the absence of one. A refusal that
// wrote would blank a client's display on a transient child failure.
func TestContextUsageResolver_RefusedFlightRecordsNothing(t *testing.T) {
	t.Parallel()
	q := newFakeContextUsageQuerier(false)
	rec, path, clock, buf := ctxUsageRecorderFor(t)

	resolve := func(convID string) (contextUsageQuerier, conversations.ConversationID, bool) {
		if convID != string(ctxUsageRecordedConv) {
			return nil, "", false
		}
		return q, ctxUsageRecordedConv, true
	}
	r := newContextUsageResolver(context.Background(), resolve, nil, clock.now)
	r.rec = rec

	if _, ok := r.Get(context.Background(), string(ctxUsageRecordedConv)); ok {
		t.Fatal("Get answered from a refusing querier")
	}
	// The flight has settled by the time Get returns; the record, if any, would be
	// on the same goroutine immediately after. Give it a window it cannot use.
	time.Sleep(20 * time.Millisecond)

	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Stat(%s) err = %v, want ErrNotExist — a refusal must record nothing", path, err)
	}
	if recs := ctxUsageLogRecords(t, buf); len(recs) != 0 {
		t.Errorf("a refusal logged %d records, want 0: %v", len(recs), recs)
	}
}
