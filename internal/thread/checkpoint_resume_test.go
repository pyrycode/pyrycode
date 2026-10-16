package thread

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/history"
)

// testCheckpointResume checks every suffix boundary against one uninterrupted
// reference baseline. Per-item exact hashes include hidden/suppressed rows,
// recipes and publication facts. The affected set updates the aggregate after
// each entry; every import starts by independently hashing the complete inventory.
// Existing semantic/golden tests remain the independent fold-behavior oracle.
func testCheckpointResume(t *testing.T, entries []history.Entry, cuts []int) {
	t.Helper()
	started := time.Now()
	baseline, _ := testReferenceFold(entries)
	proof := testResumeProof{}
	want := make([][32]byte, len(entries)+1)
	want[0] = proof.update(t, baseline, true)
	for i, e := range entries {
		testFeed(t, baseline, e)
		want[i+1] = proof.update(t, baseline, false)
	}
	t.Logf("resume baseline feed/proof=%s", time.Since(started))
	cuts = slices.Clone(cuts)
	slices.Sort(cuts)
	cuts = slices.Compact(cuts)
	wantComparisons := 0
	for _, cut := range cuts {
		if cut < 0 || cut > len(entries) {
			t.Fatal("invalid cut", cut)
		}
		wantComparisons += len(entries) - cut + 1
	}
	if len(cuts) == 0 {
		return
	}
	baseCut := cuts[0]
	if baseCut == 0 && len(cuts) > 1 {
		baseCut = cuts[1]
	}
	started = time.Now()
	prefix, _ := testReferenceFold(entries)
	testFeed(t, prefix, entries[:baseCut]...)
	base, baseRows, err := prefix.ExportCheckpoint()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("resume prefix feed/export=%s", time.Since(started))
	// Each job owns both folds and its source. The shared preparation document
	// is immutable; import gives each worker a detached continuation graph without
	// replaying prefix history.
	jobs := make(chan int)
	abort := make(chan struct{})
	var failed sync.Once
	var workers sync.WaitGroup
	var completed, comparisons atomic.Int64
	workerCount := 1
	if len(entries) >= 1000 {
		workerCount = 8
	}
	started = time.Now()
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for cut := range jobs {
				count := 0
				if !t.Run(fmt.Sprintf("cut-%d", cut), func(cutTest *testing.T) {
					count = testCheckpointResumeCut(cutTest, entries, cut, baseCut, base, baseRows, want)
				}) {
					failed.Do(func() { close(abort) })
					return
				}
				comparisons.Add(int64(count))
				completed.Add(1)
			}
		}()
	}
	defer func() {
		close(jobs)
		workers.Wait()
		if !t.Failed() && completed.Load() != int64(len(cuts)) {
			t.Fatal("resume cuts were dropped")
		}
		if !t.Failed() && comparisons.Load() != int64(wantComparisons) {
			t.Fatal("resume comparisons were dropped")
		}
		t.Logf("resume workers=%d GOMAXPROCS=%d cuts elapsed=%s", workerCount, runtime.GOMAXPROCS(0), time.Since(started))
		t.Logf("resume: %d distinct cuts, %d immediate/suffix comparisons", completed.Load(), comparisons.Load())
	}()
	for _, cut := range cuts {
		select {
		case jobs <- cut:
		case <-abort:
			return
		}
	}
}

func testCheckpointResumeCut(t *testing.T, entries []history.Entry, cut, baseCut int, base CheckpointState, baseRows []CheckpointItem, want [][32]byte) int {
	t.Helper()
	prefix, source := testReferenceFold(entries)
	start := 0
	if cut >= baseCut {
		var err error
		prefix, err = ImportCheckpoint("a", source, base, baseRows)
		if err != nil {
			t.Fatal(err)
		}
		start = baseCut
	}
	testFeed(t, prefix, entries[start:cut]...)
	d, rows, err := prefix.ExportCheckpoint()
	if err != nil {
		t.Fatal(err)
	}
	r, err := ImportCheckpoint("a", source, testCheckpointJSON(t, d), testCheckpointJSON(t, rows))
	if err != nil {
		t.Fatalf("cut %d: %v", cut, err)
	}
	actual := testResumeProof{}
	if actual.update(t, r, true) != want[cut] {
		t.Fatalf("cut %d: immediate mismatch", cut)
	}
	count := 1
	for i := cut; i < len(entries); i++ {
		testFeed(t, r, entries[i])
		if actual.update(t, r, false) != want[i+1] {
			t.Fatalf("cut %d: mismatch after entry %d", cut, entries[i].ID)
		}
		count++
	}
	return count
}

type testResumeProof struct {
	rows     map[int][32]byte
	sum      [32]byte
	selected []Item
	buffer   bytes.Buffer
	encoder  *json.Encoder
	row      testResumeProofRow
}

type testResumeProofRow struct {
	Index                                                     int
	Rendered                                                  Item
	ContentNil                                                bool
	Record                                                    CheckpointItem
	Touch                                                     uint64
	Published, Visible, Touched, PublishedValue, VisibleValue bool
}

func (p *testResumeProof) update(t testing.TB, f *Fold, full bool) [32]byte {
	t.Helper()
	if p.rows == nil {
		p.rows = make(map[int][32]byte)
		p.encoder = json.NewEncoder(&p.buffer)
	}
	indices := f.captureAffected
	p.selected = p.selected[:0]
	if full {
		p.selected = append(p.selected, f.items...)
	} else {
		for _, index := range indices {
			p.selected = append(p.selected, f.items[index])
		}
	}
	// Hash synchronously while the owner holds the fold; detached Content copies
	// are unnecessary. Every full check still independently renders all rows.
	rendered, err := f.renderShared(p.selected)
	if err != nil {
		t.Fatal(err)
	}
	for position, renderedItem := range rendered {
		index := position
		if !full {
			index = indices[position]
		}
		item := f.items[index]
		id := item.ID
		_, published := f.published[id]
		_, visible := f.publicVisible[id]
		_, touched := f.touches[id]
		content := renderedItem.Content
		renderedItem.Content = nil
		p.row = testResumeProofRow{index, renderedItem, content == nil, checkpointRecord(item, f.recipes[id]), f.touches[id], published, visible, touched, f.published[id], f.publicVisible[id]}
		p.buffer.Reset()
		if err := p.encoder.Encode(&p.row); err != nil {
			t.Fatal(err)
		}
		// Length-delimit exact Content after the complete metadata JSON. Feeding
		// RawMessage through JSON would normalize whitespace; base64 would allocate
		// and serialize the same payload again. Scratch belongs to this proof only.
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(content)))
		p.buffer.Write(length[:])
		p.buffer.Write(content)
		next, previous := sha256.Sum256(p.buffer.Bytes()), p.rows[index]
		for i := range p.sum {
			p.sum[i] ^= previous[i] ^ next[i]
		}
		p.rows[index] = next
	}
	clear(p.selected)
	p.row = testResumeProofRow{}
	if f.captureSet == nil {
		f.captureSet = make(map[int]struct{})
	}
	for _, index := range f.captureAffected {
		delete(f.captureSet, index)
	}
	f.captureAffected = f.captureAffected[:0]
	raw, err := json.Marshal(struct {
		Rows                                 [32]byte
		Count                                int
		Version, Shown, Suppressed, Unproven uint64
	}{p.sum, len(f.items), f.version, f.lastShown, f.suppressed, f.unprovenThrough})
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(raw)
}

func TestCheckpointResumeCorpus(t *testing.T) {
	t.Parallel()
	started := time.Now()
	entries := testFoldCorpus(30000)
	t.Logf("resume fixture entries=%d elapsed=%s", len(entries), time.Since(started))
	cuts := []int{0}
	for cut := len(entries) - 199; cut <= len(entries); cut++ {
		cuts = append(cuts, cut)
	}
	testCheckpointResume(t, entries, cuts)
}

func TestCheckpointProofContentBytes(t *testing.T) {
	left := []history.Entry{testEntry(1, "message", `{"role":"user","text":"same"}`)}
	right := []history.Entry{testEntry(1, "message", `{ "role": "user", "text": "same" }`)}
	f, _ := testReferenceFold(left)
	g, _ := testReferenceFold(right)
	testFeed(t, f, left...)
	testFeed(t, g, right...)
	if slices.Equal(f.Items()[0].Content, g.Items()[0].Content) {
		t.Fatal("fixture does not distinguish exact bytes")
	}
	p, q := testResumeProof{}, testResumeProof{}
	if p.update(t, f, true) == q.update(t, g, true) {
		t.Fatal("resume proof normalized rendered Content")
	}
}

func TestCheckpointResumeFocused(t *testing.T) {
	histories := [][]history.Entry{
		{testMain(1, "main_turn_opened", `,"occurred_at":"2026-01-01T00:00:00Z"`), testMain(2, "assistant_delta", `,"text":"unfinished"`), testMain(3, "tool_use", `,"tool_use_id":"read","name":"Read"`), testMain(4, "turn_end", `,"stop_reason":"end_turn"`), testMain(5, "tool_result", `,"tool_use_id":"read","is_error":false`), testMain(6, "assistant_delta", `,"text":"late"`)},
		{testDurable(1, "agent_call_observed", `,"tool_call_id":"c","tool":"Agent"`), testAgentCall(2), testDurable(3, "background_task_observed", `,"task_id":"task"`), testDurable(4, "background_task_linked", `,"task_id":"task","tool_call_id":"c"`), testDurable(5, "agent_ended_with_session", `,"task_id":"task","tool_call_id":"c","cause":"capacity_eviction"`), testTask(6, "background_task_updated", `,"status":"completed"`)},
		{testEntry(1, "compaction_boundary", `{"trigger":"auto","pre_tokens":100}`), testDivider(2, "operator_reset", "s", "n", "claude", "codex"), testEntry(3, "message", `{"role":"user","text":"next"}`)},
		{testShellCall(1), testMain(2, "tool_result", `,"tool_use_id":"c","is_error":false`), testTask(3, "background_task_started", `,"tool_call_id":"c"`), testMain(4, "turn_end", `,"stop_reason":"end_turn"`), testTask(5, "background_task_updated", `,"status":"completed"`)},
		testReferenceSendEntries(), testEndingEntries(), testFoldCorpus(151),
		{testChild(1, "assistant_delta", "missing", `,"text":"hidden"`), testMain(2, "tool_use", `,"tool_use_id":"missing","name":"Agent"`), testChild(3, "assistant_delta", "missing", `,"text":"late"`)},
		{testShellCall(1), testDivider(2, "idle_sleep", "s", "s", "claude", "claude"), testShellCall(3), testDurable(4, "background_task_observed", `,"task_id":"task"`), testDurable(5, "background_task_linked", `,"task_id":"task","tool_call_id":"c"`)},
	}
	for i, entries := range histories {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			cuts := make([]int, len(entries)+1)
			for i := range cuts {
				cuts[i] = i
			}
			testCheckpointResume(t, entries, cuts)
		})
	}
	// Reuse call/task identities across independently recorded sources/lifetimes.
	entries := []history.Entry{testShellCall(1), testDurable(2, "background_task_observed", `,"task_id":"task"`), testDurable(3, "background_task_linked", `,"task_id":"task","tool_call_id":"c"`), testShellCall(4)}
	entries[0].Session = testSource("claude", "old")
	for i := 1; i < len(entries); i++ {
		entries[i].Session = testSource("claude", "new")
	}
	testCheckpointResume(t, entries, []int{0, 1, 2, 3, 4})
}

func TestCheckpointResumeCompanions(t *testing.T) {
	for _, mappedFirst := range []bool{false, true} {
		entries := []history.Entry{testDurable(1, "agent_call_result", `,"tool_call_id":"c","status":"completed"`), testMain(2, "tool_result", `,"tool_use_id":"c","is_error":false`), testMain(3, "main_turn_opened", `,"occurred_at":"2026-01-01T00:00:00Z"`), testShellCall(4)}
		content := uint64(2)
		if mappedFirst {
			entries[0], entries[1] = entries[1], entries[0]
			entries[0].ID, entries[1].ID = 1, 2
			content = 1
		}
		testCheckpointResume(t, entries, []int{0, 1, 2, 3, 4})
		f, source := testReferenceFold(entries)
		testFeed(t, f, entries[:1]...)
		d, rows, _ := f.ExportCheckpoint()
		r, err := ImportCheckpoint("a", source, d, rows)
		if err != nil {
			t.Fatal(err)
		}
		testFeed(t, r, entries[1:]...)
		for _, g := range r.agentGroups {
			for _, c := range g.calls {
				if c.ordinary != nil {
					o := c.ordinary.terminal
					if o == nil || o.id != 1 || o.content != content {
						t.Fatal("terminal order/content conflated", o)
					}
				}
			}
		}
		item := testShell(t, r, 4, "done", false, 0)
		if item.Rev != 4 || r.touches[4] != 4 {
			t.Fatal("creation revision/touch", item, r.touches)
		}
	}
}
