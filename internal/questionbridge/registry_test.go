package questionbridge

import (
	"bytes"
	"log/slog"
	"reflect"
	"testing"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// sampleBatch is a parsed, in-contract batch, rebuilt on every call so a row
// that mutates what the registry handed it cannot leak into another row. Both
// nesting levels carry more than one element on purpose: the isolation rows
// mutate a question AND a question's nested option, and a single-element
// fixture would let a shallow clone pass the nested case by coincidence.
func sampleBatch() protocol.QuestionShownPayload {
	return protocol.QuestionShownPayload{Questions: []protocol.Question{
		{
			Text:   "For the in-memory cache, which write strategy should it use?",
			Header: "Write strategy",
			Options: []protocol.QuestionOption{
				{Label: "Write-through", Description: "Writes go to cache and store together."},
				{Label: "Write-behind", Description: "Writes are persisted asynchronously."},
			},
		},
		{
			Text:   "Which eviction policy?",
			Header: "Eviction",
			Options: []protocol.QuestionOption{
				{Label: "LRU", Description: "Least recently used."},
				{Label: "LFU", Description: "Least frequently used."},
			},
			MultiSelect: true,
		},
	}}
}

// parked is sampleBatch as the registry holds it: the same questions under the
// two ids Record asserted. Every equality check compares against this, so a
// hand-out that dropped or reordered a question fails as loudly as a mutated
// one.
func parked(batchID, convID string) protocol.QuestionShownPayload {
	p := sampleBatch()
	p.QuestionBatchID = batchID
	p.ConversationID = convID
	return p
}

func mustRecord(t *testing.T, reg *Registry, convID string) protocol.QuestionShownPayload {
	t.Helper()
	out, err := reg.Record(sampleBatch(), convID)
	if err != nil {
		t.Fatalf("Record(%q): %v", convID, err)
	}
	return out
}

// TestRecord_MintsStampsAndStores is AC 1. The mint-FAILURE half of that
// criterion is deliberately unexercised: crypto/rand.Read does not fail on a
// supported platform, and minting an injectable RNG seam to reach the branch
// would be API nothing consumes — modalbridge's registry leaves the same branch
// untested for the same reason.
func TestRecord_MintsStampsAndStores(t *testing.T) {
	t.Parallel()
	reg := New()

	out, err := reg.Record(sampleBatch(), "conv-A")
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if out.QuestionBatchID == "" {
		t.Fatal("QuestionBatchID must be non-empty")
	}
	if !conversations.ValidID(out.QuestionBatchID) {
		t.Errorf("QuestionBatchID %q is not a canonical UUIDv4", out.QuestionBatchID)
	}
	if !reflect.DeepEqual(out, parked(out.QuestionBatchID, "conv-A")) {
		t.Errorf("returned payload = %+v, want the batch stamped with both ids", out)
	}

	got, ok := reg.Lookup(out.QuestionBatchID)
	if !ok {
		t.Fatalf("Lookup(%q): not found", out.QuestionBatchID)
	}
	if !reflect.DeepEqual(got, parked(out.QuestionBatchID, "conv-A")) {
		t.Errorf("stored payload = %+v, want %+v", got, parked(out.QuestionBatchID, "conv-A"))
	}

	if _, ok := reg.Lookup("nonexistent-id"); ok {
		t.Error("Lookup of an unrecorded id should return false")
	}
}

// TestRecord_OverwritesIncomingIDs pins that neither id is adopted from the
// input. The payload reaches Record straight off claude's tool call, so both
// daemon-asserted fields are written over rather than trusted — a batch stored
// under a caller-supplied id would be resolvable by whoever supplied it.
func TestRecord_OverwritesIncomingIDs(t *testing.T) {
	t.Parallel()
	reg := New()

	in := sampleBatch()
	in.QuestionBatchID = "asserted-elsewhere"
	in.ConversationID = "conv-elsewhere"
	out, err := reg.Record(in, "conv-A")
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if out.QuestionBatchID == "asserted-elsewhere" {
		t.Error("QuestionBatchID was adopted from the input, not minted")
	}
	if out.ConversationID != "conv-A" {
		t.Errorf("ConversationID: got %q, want %q", out.ConversationID, "conv-A")
	}
	if _, ok := reg.Lookup("asserted-elsewhere"); ok {
		t.Error("batch is stored under the input's id")
	}
}

// TestRecord_NonceUniqueness is AC 2: identical batch content every time, so
// only the mint can make the ids differ.
func TestRecord_NonceUniqueness(t *testing.T) {
	t.Parallel()
	reg := New()

	const n = 1000
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		out := mustRecord(t, reg, "conv-A")
		if _, dup := seen[out.QuestionBatchID]; dup {
			t.Fatalf("duplicate question_batch_id minted: %q", out.QuestionBatchID)
		}
		seen[out.QuestionBatchID] = struct{}{}
	}
	if len(seen) != n {
		t.Errorf("got %d distinct ids, want %d", len(seen), n)
	}
	if snap := reg.Snapshot(); len(snap) != n {
		t.Errorf("Snapshot len: got %d, want %d — every batch stays outstanding", len(snap), n)
	}
}

// TestLookup_DoesNotConsume is AC 4's read half: reading by nonce leaves the
// batch outstanding, so the one-shot consume stays Resolve's alone.
func TestLookup_DoesNotConsume(t *testing.T) {
	t.Parallel()
	reg := New()
	out := mustRecord(t, reg, "conv-A")

	for i := 0; i < 3; i++ {
		if _, ok := reg.Lookup(out.QuestionBatchID); !ok {
			t.Fatalf("Lookup #%d: not found", i+1)
		}
	}
	if _, ok := reg.Resolve(out.QuestionBatchID); !ok {
		t.Error("Resolve after repeated Lookup: not found")
	}
}

// TestResolve_OneShot is AC 3. It is what makes "exactly one of the answer path
// and the retire backstop broadcasts the dismissal" structural rather than an
// agreement between #1907 and #1973.
func TestResolve_OneShot(t *testing.T) {
	t.Parallel()
	reg := New()
	out := mustRecord(t, reg, "conv-A")

	got, ok := reg.Resolve(out.QuestionBatchID)
	if !ok {
		t.Fatalf("first Resolve(%q): not found", out.QuestionBatchID)
	}
	if !reflect.DeepEqual(got, parked(out.QuestionBatchID, "conv-A")) {
		t.Errorf("Resolve payload = %+v, want %+v", got, parked(out.QuestionBatchID, "conv-A"))
	}

	for i := 0; i < 2; i++ {
		if _, ok := reg.Resolve(out.QuestionBatchID); ok {
			t.Errorf("Resolve #%d: still present, want absent", i+2)
		}
	}
	if _, ok := reg.Lookup(out.QuestionBatchID); ok {
		t.Error("Lookup after Resolve: still present")
	}
	if snap := reg.Snapshot(); len(snap) != 0 {
		t.Errorf("Snapshot after Resolve: got %d entries, want 0", len(snap))
	}
}

// TestSnapshot_ReEmitsIDsPerBatch is AC 4's whole-registry half: each entry
// carries back the nonce and the conversation id recording stamped on it, which
// is what lets #1928's connect-time reconcile scope a replay exactly as the
// initial broadcast was scoped.
func TestSnapshot_ReEmitsIDsPerBatch(t *testing.T) {
	t.Parallel()
	reg := New()
	a := mustRecord(t, reg, "conv-A")
	b := mustRecord(t, reg, "conv-B")

	snap := reg.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("Snapshot len: got %d, want 2", len(snap))
	}
	byID := make(map[string]protocol.QuestionShownPayload, len(snap))
	for _, p := range snap {
		byID[p.QuestionBatchID] = p
	}
	for _, want := range []protocol.QuestionShownPayload{a, b} {
		got, ok := byID[want.QuestionBatchID]
		if !ok {
			t.Fatalf("Snapshot omits batch %q", want.QuestionBatchID)
		}
		if !reflect.DeepEqual(got, parked(want.QuestionBatchID, want.ConversationID)) {
			t.Errorf("Snapshot entry = %+v, want %+v", got, parked(want.QuestionBatchID, want.ConversationID))
		}
	}
}

func TestSnapshot_EmptyIsNonNil(t *testing.T) {
	t.Parallel()
	snap := New().Snapshot()
	if snap == nil {
		t.Fatal("Snapshot of an empty registry must be non-nil")
	}
	if len(snap) != 0 {
		t.Errorf("Snapshot len: got %d, want 0", len(snap))
	}
}

// TestSnapshot_PureRead pins that the reconcile read mints nothing and retires
// nothing: an outstanding batch survives it, Lookup-able and Resolve-able.
func TestSnapshot_PureRead(t *testing.T) {
	t.Parallel()
	reg := New()
	out := mustRecord(t, reg, "conv-A")

	first := reg.Snapshot()
	second := reg.Snapshot()
	if !reflect.DeepEqual(first, second) {
		t.Errorf("two Snapshots differ: %+v vs %+v", first, second)
	}
	if _, ok := reg.Lookup(out.QuestionBatchID); !ok {
		t.Error("Lookup after Snapshot: not found")
	}
	if _, ok := reg.Resolve(out.QuestionBatchID); !ok {
		t.Error("Resolve after Snapshot: not found")
	}
}

// TestRegistry_HandsOutIsolatedCopies is AC 4's "either nesting level", run over
// all three hand-out sites. The nested row is the one that matters: a
// slices.Clone over the questions slice alone copies the question structs but
// leaves every Question.Options pointing at the stored backing array, so the
// shallow clone that fully isolates modalbridge's flat option slice isolates
// nothing one level down.
func TestRegistry_HandsOutIsolatedCopies(t *testing.T) {
	t.Parallel()

	handOuts := []struct {
		name string
		hand func(t *testing.T, reg *Registry, recorded protocol.QuestionShownPayload) protocol.QuestionShownPayload
	}{
		{
			name: "Record's return",
			hand: func(_ *testing.T, _ *Registry, recorded protocol.QuestionShownPayload) protocol.QuestionShownPayload {
				return recorded
			},
		},
		{
			name: "Lookup",
			hand: func(t *testing.T, reg *Registry, recorded protocol.QuestionShownPayload) protocol.QuestionShownPayload {
				t.Helper()
				got, ok := reg.Lookup(recorded.QuestionBatchID)
				if !ok {
					t.Fatalf("Lookup(%q): not found", recorded.QuestionBatchID)
				}
				return got
			},
		},
		{
			name: "Snapshot",
			hand: func(t *testing.T, reg *Registry, _ protocol.QuestionShownPayload) protocol.QuestionShownPayload {
				t.Helper()
				snap := reg.Snapshot()
				if len(snap) != 1 {
					t.Fatalf("Snapshot len: got %d, want 1", len(snap))
				}
				return snap[0]
			},
		},
	}
	mutations := []struct {
		name   string
		mutate func(protocol.QuestionShownPayload)
	}{
		{
			name:   "outer question",
			mutate: func(p protocol.QuestionShownPayload) { p.Questions[0].Text = "mutated" },
		},
		{
			name:   "nested option",
			mutate: func(p protocol.QuestionShownPayload) { p.Questions[0].Options[0].Label = "mutated" },
		},
	}

	for _, h := range handOuts {
		for _, m := range mutations {
			t.Run(h.name+"/"+m.name, func(t *testing.T) {
				t.Parallel()
				reg := New()
				recorded := mustRecord(t, reg, "conv-A")

				m.mutate(h.hand(t, reg, recorded))

				got, ok := reg.Lookup(recorded.QuestionBatchID)
				if !ok {
					t.Fatalf("Lookup(%q) after mutation: not found", recorded.QuestionBatchID)
				}
				if !reflect.DeepEqual(got, parked(recorded.QuestionBatchID, "conv-A")) {
					t.Errorf("mutating %s reached the parked batch: got %+v, want %+v",
						m.name, got, parked(recorded.QuestionBatchID, "conv-A"))
				}
			})
		}
	}
}

// TestRegistry_EmitsNoLog is AC 5 over the registry surface, the sibling of
// TestParse_EmitsNoLog and for its reason: the package imports no logger, so no
// question text, header, option label or description — and no minted nonce —
// can reach a log from here.
//
// The handler is at slog.LevelDebug ON PURPOSE. At the default level the buffer
// would stay empty without the assertion ever having observed a Debug line, so
// the pin would pass while proving nothing. slog.SetDefault also redirects the
// standard log package, so one buffer covers both routes; it mutates a
// process-wide global, which is why this test does not call t.Parallel.
func TestRegistry_EmitsNoLog(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	reg := New()
	out, err := reg.Record(sampleBatch(), "conv-A")
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	reg.Lookup(out.QuestionBatchID)
	reg.Lookup("nonexistent-id")
	reg.Snapshot()
	reg.Resolve(out.QuestionBatchID)
	reg.Resolve(out.QuestionBatchID)

	if buf.Len() != 0 {
		t.Errorf("the registry wrote %d bytes to the default logger, want none: %s", buf.Len(), buf.String())
	}
}
