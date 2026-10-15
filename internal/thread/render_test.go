package thread

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"unsafe"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

func testReferenceFold(entries []history.Entry) (*Fold, *recentSource) {
	s := newRecentSource(func(ids []uint64) ([]history.Entry, error) {
		var out []history.Entry
		for _, id := range ids {
			if i, ok := slices.BinarySearchFunc(entries, id, func(e history.Entry, id uint64) int { return cmp.Compare(e.ID, id) }); ok {
				out = append(out, entries[i])
			}
		}
		return out, nil
	})
	return newReferenceFold("a", s), s
}

func TestReferenceRetainedBytes(t *testing.T) {
	entries := testFoldCorpus(6738)
	legacy := New("a")
	f, source := testReferenceFold(entries)
	testFeed(t, legacy, entries...)
	source.chunk = entries
	testFeed(t, f, entries...)
	source.remember(entries)
	source.chunk = nil
	var before, after, summaryBefore, summaryAfter, deferred, assistantBefore, assistantAfter, childBefore, childAfter, recipeBytes, parentEndings int
	countRecipe := func(r contentRecipe) {
		recipeBytes += int(unsafe.Sizeof(r)) + len(r.text)*8
		for key, field := range r.fields {
			recipeBytes += len(key) + int(unsafe.Sizeof(field))
			if key == "parent_ending" {
				parentEndings += len(field.value)
			} else if len(field.value) != 0 {
				t.Fatal("unexpected copied enrichment")
			}
		}
	}
	for i, item := range f.items {
		if f.recipes[item.ID].creating != 0 {
			if f.texts[i] != nil {
				t.Fatal("reference item retained an inline append buffer")
			}
			countRecipe(f.recipes[item.ID])
			before += len(legacy.items[i].Content)
			after += len(item.Content)
			if item.Kind == "user_message" {
				summaryBefore += len(legacy.items[i].Summary)
				summaryAfter += len(item.Summary)
			}
			if item.Kind == "assistant_message" {
				assistantBefore += len(legacy.items[i].Summary)
				assistantAfter += len(item.Summary)
			}
			if state := f.children[i]; state != nil {
				old := legacy.children[i]
				childBefore += len(old.base.Content) + len(old.previous.Content)
				childAfter += len(state.base.Content) + len(state.previous.Content) + len(state.endingBase) + len(state.endingContent)
				if item.Kind == "assistant_message" {
					assistantBefore += len(old.base.Summary) + len(old.previous.Summary)
					assistantAfter += len(state.base.Summary) + len(state.previous.Summary)
				}
				countRecipe(state.baseRecipe)
				countRecipe(state.previousRecipe)
			}
		} else {
			deferred += len(item.Content)
		}
	}
	if before == 0 || after != 0 || childBefore == 0 || childAfter != 0 || assistantBefore == 0 || assistantAfter != 0 || summaryBefore == 0 || summaryAfter != 0 || source.bytes > recentEntryBudget {
		t.Fatal("reference retention did not drop", before, after, source.bytes)
	}
	seen := make(map[*mainOutcome]bool)
	private := 0
	countOutcome := func(o *mainOutcome) {
		if o != nil && !seen[o] {
			seen[o] = true
			private += len(o.raw)
		}
	}
	countTurn := func(st *mainTurn) {
		countOutcome(st.end)
		for _, call := range st.calls {
			countOutcome(call.terminal)
		}
	}
	for _, st := range f.turns {
		countTurn(st)
	}
	for _, st := range f.openings {
		countTurn(st)
	}
	for _, st := range f.childTurns {
		countTurn(st)
	}
	for _, o := range f.childReports {
		countOutcome(o)
	}
	t.Logf("generated entries=%d converted Content before=%d after=%d child-copy Content before=%d after=%d assistant Summary (including child copies) before=%d after=%d user Summary before=%d after=%d; recipes=%d logical recipe bytes=%d (includes child slots, excludes map/slice overhead, may share ID storage); bounded recent cache=%d; deferred private outcomes=%d inline parent-ending bytes across recipe slots=%d inline item Content=%d; Store snapshots excluded", len(entries), before, after, childBefore, childAfter, assistantBefore, assistantAfter, summaryBefore, summaryAfter, len(f.recipes), recipeBytes, source.bytes, private, parentEndings, deferred)
}

func TestReferenceSummaryDeferred(t *testing.T) {
	f, _ := testReferenceFold(nil)
	e := testMessage(1)
	item := Item{ID: 1, Kind: "user_message", Summary: strings.Repeat("a\n", 10000)}
	f.addItem(e, item) // Warm the metadata storage before measuring normalization.
	allocs := testing.AllocsPerRun(10, func() {
		f.items = f.items[:0]
		f.addItem(e, item)
	})
	if allocs != 0 {
		t.Fatalf("reference creation normalized discarded user summary: %g allocations", allocs)
	}
}

func TestReferenceRendering(t *testing.T) {
	entries := testFoldCorpus(101)
	entries[0] = testEntry(1, "message", "{ \"role\": \"user\", \"text\": \"  hi\\nthere\\u0001  \", \"future\": [1, 2] }")
	for _, size := range []int{1, 7, len(entries)} {
		legacy := New("a")
		f, source := testReferenceFold(entries)
		for start := 0; start < len(entries); start += size {
			chunk := entries[start:min(start+size, len(entries))]
			source.chunk = chunk
			testFeed(t, f, chunk...)
			testFeed(t, legacy, chunk...)
			got, err := f.Render()
			if err != nil || !reflect.DeepEqual(got, legacy.Items()) {
				t.Fatalf("chunk %d/%d differs: %v", start, size, err)
			}
			source.remember(chunk)
			source.chunk = nil
		}
		if f.items[0].Content != nil || f.items[0].Summary != "" || f.recipes[1].creating != 1 {
			t.Fatal("plain message retained content or summary")
		}
		got, err := f.Render()
		if err != nil || !bytes.Equal(got[0].Content, entries[0].Payload) || got[0].Summary != "hi there" {
			t.Fatal("raw bytes or summary changed", err)
		}
		got[0].Content[0] = 'X'
		again, _ := f.Render()
		if !bytes.Equal(again[0].Content, entries[0].Payload) {
			t.Fatal("renderer leaked content ownership")
		}
	}
	f, source := testReferenceFold([]history.Entry{testEntry(1, "message", `{"role":"user","text":"\n\u0001"}`)})
	source.chunk = []history.Entry{testEntry(1, "message", `{"role":"user","text":"\n\u0001"}`)}
	testFeed(t, f, source.chunk...)
	items, err := f.Render()
	if err != nil || items[0].Summary != "user message" {
		t.Fatal("empty summary fallback changed", err)
	}
}

func TestReferenceSourceOrder(t *testing.T) {
	var requested []uint64
	s := newRecentSource(func(ids []uint64) ([]history.Entry, error) {
		requested = append([]uint64(nil), ids...)
		return []history.Entry{testMessage(3)}, nil
	})
	s.remember([]history.Entry{testMessage(2)})
	s.chunk = []history.Entry{testMessage(1)}
	entries, err := s.Lookup([]uint64{3, 2, 1, 3, 2})
	if err != nil || len(entries) != 3 || !reflect.DeepEqual(requested, []uint64{3}) {
		t.Fatal("lookup did not exclude supplied IDs or normalize", requested, err)
	}
	s.lookup = func([]uint64) ([]history.Entry, error) { t.Fatal("supplied ID caused segment lookup"); return nil, nil }
	if _, err := s.Lookup([]uint64{2, 1, 2}); err != nil {
		t.Fatal(err)
	}
}

func TestReferenceSourceBatch(t *testing.T) {
	s := newRecentSource(func([]uint64) ([]history.Entry, error) { t.Fatal("all-hit batch read history"); return nil, nil })
	const count = 36000
	s.chunk = make([]history.Entry, count)
	ids := make([]uint64, count+1)
	for i := range s.chunk {
		s.chunk[i] = testMessage(uint64(i + 1))
		ids[count-i] = uint64(i + 1)
	}
	ids[0] = 1
	s.remember([]history.Entry{testEntry(1, "message", `{"text":"stale"}`)})
	entries, err := s.Lookup(ids)
	if err != nil || !reflect.DeepEqual(entries, s.chunk) {
		t.Fatal("large unordered duplicate request lost feed precedence", err)
	}
}

func TestReferenceSourceBound(t *testing.T) {
	s := newRecentSource(func([]uint64) ([]history.Entry, error) { return nil, nil })
	base := int(unsafe.Sizeof(*s))
	e := testMessage(1)
	e.Payload = make([]byte, 250000, 2<<20)
	e.Type = strings.Repeat("t", 3000)
	e.Session = testSource(strings.Repeat("k", 3000), strings.Repeat("s", 3000))
	e.Shown = new(bool)
	s.remember([]history.Entry{e})
	if s.bytes <= base+len(e.Payload)+len(e.Type) || s.bytes > recentEntryBudget {
		t.Fatal("metadata/backing storage not accounted", s.bytes)
	}
	e.Payload[0] = 'X'
	e.Session.Kind = "changed"
	got, _ := s.Lookup([]uint64{1})
	if got[0].Payload[0] == 'X' || got[0].Session.Kind == "changed" || cap(got[0].Payload) > len(got[0].Payload) {
		t.Fatal("cache retained input backing or metadata")
	}
	for i := 2; i <= 8; i++ {
		e.ID = uint64(i)
		s.remember([]history.Entry{e})
		if s.bytes > recentEntryBudget {
			t.Fatal("cache exceeded bound", s.bytes)
		}
	}
	got, _ = s.Lookup([]uint64{1})
	if len(got) != 0 {
		t.Fatal("old entry was not evicted")
	}
	before := s.bytes
	e.Payload = make([]byte, recentEntryBudget)
	s.remember([]history.Entry{e})
	if s.bytes != before {
		t.Fatal("oversized entry was retained")
	}
}

func TestReferenceMissingAtomic(t *testing.T) {
	entries := []history.Entry{testMessage(1), testMessage(2)}
	f, source := testReferenceFold(entries)
	testFeed(t, f, entries...)
	for _, lookup := range []func([]uint64) ([]history.Entry, error){
		func([]uint64) ([]history.Entry, error) { return entries[:1], nil },
		func([]uint64) ([]history.Entry, error) { return nil, errors.New("private source path") },
	} {
		source.lookup = lookup
		if items, err := f.Render(); err == nil || items != nil {
			t.Fatal("partial rendering escaped", items, err)
		}
	}
}

func TestReferenceDeliveryFailure(t *testing.T) {
	for _, missing := range []uint64{1, 2} {
		for _, failure := range []string{"missing", "unreadable", "malformed", "null"} {
			t.Run(fmt.Sprint(missing, failure), func(t *testing.T) {
				entries := testReferenceSendEntries()
				f, source := testReferenceFold(entries)
				testFeed(t, f, entries[:2]...)
				before := slices.Clone(f.items)
				lookup := source.lookup
				source.lookup = func(ids []uint64) ([]history.Entry, error) {
					out, err := lookup(ids)
					if failure == "unreadable" {
						return nil, errors.New("private unreadable delivery")
					}
					if failure == "missing" {
						return slices.DeleteFunc(out, func(e history.Entry) bool { return e.ID == missing }), err
					}
					for i := range out {
						if out[i].ID == missing {
							out[i].Payload = []byte(`{"private"`)
							if failure == "null" {
								out[i].Payload = []byte(`null`)
							}
						}
					}
					return out, err
				}
				if err := f.Feed(entries[2:]); err != ErrUnavailable {
					t.Fatal("claimed delivery ignored source failure", err)
				}
				if !reflect.DeepEqual(f.items, before) || f.messages[2] < 0 || f.recipes[1].delivery != 0 || len(f.recipes[1].fields) != 0 {
					t.Fatal("failed delivery altered acceptance or suppressed message")
				}
			})
		}
	}
}

func TestReferenceDeliveryEvicted(t *testing.T) {
	entries := []history.Entry{testAcceptance(1, "phone"), testEntry(2, "message", `{"role":"user","text":"safe\ndelivered","future":[1, 2],"queued_msg_id":90}`)}
	entries[0].Payload = []byte(strings.TrimSuffix(string(entries[0].Payload), "}") + `,"queued_msg_id":8}`)
	entries[0].Session, entries[1].Session = testSource("claude", "sender"), testSource("codex", "receiver")
	for id := uint64(3); id <= 258; id++ {
		entries = append(entries, testEntry(id, "ignored", `{}`))
	}
	entries = append(entries, testSendOutcome(259, "send_delivered", `,"accepted_entry_id":1,"delivery_entry_id":2`, "delivered"))
	hidden := false
	entries[len(entries)-1].Shown = &hidden
	f, source := testReferenceFold(entries)
	legacy := New("a")
	lookup := source.lookup
	var requested []uint64
	source.lookup = func(ids []uint64) ([]history.Entry, error) {
		requested = append(requested, ids...)
		return lookup(ids)
	}
	for i, e := range entries {
		source.chunk = entries[i : i+1]
		testFeed(t, f, e)
		testFeed(t, legacy, e)
		source.remember(source.chunk)
		source.chunk = nil
	}
	if !reflect.DeepEqual(requested, []uint64{1, 2}) {
		t.Fatal("delivery did not resolve evicted entry", requested)
	}
	items, err := f.Render()
	if err != nil || !reflect.DeepEqual(items, legacy.Items()) || len(items) != 1 || f.messages[2] != -1 || f.lastShown != legacy.lastShown || f.Version() != legacy.Version() || !reflect.DeepEqual(f.touches, legacy.touches) {
		t.Fatal("evicted delivery changed acceptance, suppression or watermarks", err, items)
	}
	if f.items[1].Content != nil || f.items[1].Summary != "" {
		t.Fatal("delivery acquired retained payload or summary")
	}
}

func TestStoreReferenceRetry(t *testing.T) {
	testStoreReferenceRetry(t, []history.Entry{testMessage(1)}, 1)
}

func TestStoreReferenceWorkRetry(t *testing.T) {
	entries := []history.Entry{
		testMain(1, "tool_use", `,"tool_use_id":"parent","name":"Agent","input":{}`),
		testMain(2, "assistant_delta", `,"parent_tool_use_id":"parent","text":"a"`),
		testMain(3, "assistant_delta", `,"parent_tool_use_id":"parent","text":"b"`),
		testMain(4, "tool_use", `,"parent_tool_use_id":"parent","tool_use_id":"r","name":"Read"`),
		testMain(5, "tool_result", `,"parent_tool_use_id":"parent","tool_use_id":"r","is_error":false`),
	}
	for _, missing := range []uint64{3, 5} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) { testStoreReferenceRetry(t, entries, missing) })
	}
}

func testStoreReferenceRetry(t *testing.T, initial []history.Entry, missing uint64, phases ...string) {
	t.Helper()
	if len(phases) == 0 {
		phases = []string{"replay", "tail"}
	}
	for _, phase := range phases {
		for _, failure := range []string{"missing", "unreadable"} {
			t.Run(phase+"/"+failure, func(t *testing.T) {
				s, h := testThreadStore(t)
				first := testMessage(1)
				if len(initial) > 1 {
					first = initial[0]
				}
				target := missing
				if phase == "tail" && len(initial) == 1 {
					first = testAcceptance(1, "phone")
				}
				entries := testStoreAppend(t, h, testStoreA, first)
				if phase == "tail" && len(initial) == 1 {
					entries = append(entries, testStoreAppend(t, h, testStoreA, testMessage(0))...)
					target = 2
				}
				deferredSend := phase == "tail" && len(initial) > 1 && initial[0].Type == "send_accepted"
				end := len(initial)
				if deferredSend {
					end-- // Resolve the send only after its source entries are evicted.
				}
				entries = append(entries, testStoreAppend(t, h, testStoreA, slices.Clone(initial[1:end])...)...)
				var armed atomic.Bool
				armed.Store(phase == "replay")
				base := s.lookup
				s.lookup = func(ctx context.Context, id conversations.ConversationID, ids []uint64) ([]history.Entry, error) {
					if !armed.Load() || !slices.Contains(ids, target) {
						return base(ctx, id, ids)
					}
					if failure == "missing" {
						resolved, err := base(ctx, id, ids)
						return slices.DeleteFunc(resolved, func(e history.Entry) bool { return e.ID == target }), err
					}
					return nil, errors.New("private unreadable source")
				}
				if phase == "tail" {
					if err := s.Load(context.Background(), testStoreA); err != nil {
						t.Fatal(err)
					}
					testStoreWait(t, s, testStoreA, StateUsable, uint64(len(entries)))
					armed.Store(true)
				}
				// Later facts evict the creating message; neither the current feed
				// chunk nor the recent cache can mask the failed authoritative read.
				padding := history.MaxPageEntries
				if deferredSend {
					padding = 256 // Fill the FIFO before appending the terminal fact.
				}
				for range padding {
					entries = append(entries, testStoreAppend(t, h, testStoreA, testEntry(0, "ignored", `{}`))...)
				}
				if phase == "tail" {
					if deferredSend {
						testStoreWait(t, s, testStoreA, StateUsable, uint64(len(entries)))
						entries = append(entries, testStoreAppend(t, h, testStoreA, initial[len(initial)-1])...)
					} else {
						// Unchanged publications need no source reread. A delivery join
						// must resolve its evicted message before publishing progress.
						entries = append(entries, testStoreAppend(t, h, testStoreA, testSendOutcome(0, "send_delivered", `,"accepted_entry_id":1,"delivery_entry_id":2`, "delivered"))...)
						if len(initial) > 1 {
							// Ancestor closure revisits both child recipes, including a
							// completed tool whose intrinsic result remains unchanged.
							entries = append(entries, testStoreAppend(t, h, testStoreA, testMain(0, "tool_result", `,"tool_use_id":"parent","is_error":false`))...)
						}
					}
				}
				if err := s.Load(context.Background(), testStoreA); err != nil {
					t.Fatal(err)
				}
				snap := testStoreWait(t, s, testStoreA, StateUnavailable, 0)
				if snap.Err != ErrUnavailable || snap.Items != nil || snap.Version != 0 || snap.LastShownVersion != 0 || snap.Epoch != "" {
					t.Fatal("unsafe partial publication", snap)
				}
				s.mu.Lock()
				done := s.workers[testStoreA].done
				s.mu.Unlock()
				<-done
				armed.Store(false)
				if err := s.Retry(context.Background(), testStoreA); err != nil {
					t.Fatal(err)
				}
				testStoreEqual(t, testStoreWait(t, s, testStoreA, StateUsable, uint64(len(entries))), testStoreA, entries)
			})
		}
	}
}
