package thread

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
)

// testCheckpointResume checks every suffix boundary against one uninterrupted
// reference baseline. Per-item exact JSON hashes include hidden/suppressed rows,
// recipes and publication facts. The affected set updates the aggregate after
// each entry; every import starts by independently hashing the complete inventory.
// Existing semantic/golden tests remain the independent fold-behavior oracle.
func testCheckpointResume(t *testing.T, entries []history.Entry, cuts []int) {
	t.Helper()
	baseline, _ := testReferenceFold(entries)
	proof := testResumeProof{}
	want := make([][32]byte, len(entries)+1)
	want[0] = proof.update(t, baseline, true)
	for i, e := range entries {
		testFeed(t, baseline, e)
		want[i+1] = proof.update(t, baseline, false)
	}
	cuts = slices.Clone(cuts)
	slices.Sort(cuts)
	cuts = slices.Compact(cuts)
	prefix, _ := testReferenceFold(entries)
	position := 0
	for _, cut := range cuts {
		if cut < 0 || cut > len(entries) {
			t.Fatal("invalid cut", cut)
		}
		testFeed(t, prefix, entries[position:cut]...)
		position = cut
		d, rows, err := prefix.ExportCheckpoint()
		if err != nil {
			t.Fatal(err)
		}
		_, source := testReferenceFold(entries)
		r, err := ImportCheckpoint("a", source, testCheckpointJSON(t, d), testCheckpointJSON(t, rows))
		if err != nil {
			t.Fatalf("cut %d: %v", cut, err)
		}
		actual := testResumeProof{}
		if actual.update(t, r, true) != want[cut] {
			t.Fatalf("cut %d: immediate mismatch", cut)
		}
		for i := cut; i < len(entries); i++ {
			testFeed(t, r, entries[i])
			if actual.update(t, r, false) != want[i+1] {
				t.Fatalf("cut %d: mismatch after entry %d", cut, entries[i].ID)
			}
		}
	}
}

type testResumeProof struct {
	rows map[int][32]byte
	sum  [32]byte
}

func (p *testResumeProof) update(t *testing.T, f *Fold, full bool) [32]byte {
	t.Helper()
	if p.rows == nil {
		p.rows = make(map[int][32]byte)
	}
	indices := f.captureAffected
	if full {
		indices = make([]int, len(f.items))
		for i := range indices {
			indices[i] = i
		}
	}
	selected := make([]Item, len(indices))
	for i, index := range indices {
		selected[i] = f.items[index]
	}
	rendered, err := f.render(selected)
	if err != nil {
		t.Fatal(err)
	}
	for position, index := range indices {
		item := f.items[index]
		id := item.ID
		_, published := f.published[id]
		_, visible := f.publicVisible[id]
		_, touched := f.touches[id]
		raw, err := json.Marshal(struct {
			Index                                                     int
			Rendered                                                  Item
			ContentBytes                                              []byte
			Record                                                    CheckpointItem
			Touch                                                     uint64
			Published, Visible, Touched, PublishedValue, VisibleValue bool
		}{index, rendered[position], []byte(rendered[position].Content), checkpointRecord(item, f.recipes[id]), f.touches[id], published, visible, touched, f.published[id], f.publicVisible[id]})
		if err != nil {
			t.Fatal(err)
		}
		next, previous := sha256.Sum256(raw), p.rows[index]
		for i := range p.sum {
			p.sum[i] ^= previous[i] ^ next[i]
		}
		p.rows[index] = next
	}
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
	entries := testFoldCorpus(30000)
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
