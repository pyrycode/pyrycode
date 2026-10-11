package memorycapture

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func testPublish(t *testing.T, s *Store, id Identity, p Payload) *Batch {
	t.Helper()
	b, err := s.Publish(context.Background(), id, p)
	if err != nil || b == nil {
		t.Fatalf("publish: %#v %v", b, err)
	}
	return b
}
func testAck(t *testing.T, s *Store, id Identity, batch string) *Receipt {
	t.Helper()
	r, err := s.Acknowledge(context.Background(), id, batch)
	if err != nil || r == nil {
		t.Fatalf("ack: %#v %v", r, err)
	}
	return r
}

func TestAcknowledgementHistory(t *testing.T) {
	home := t.TempDir()
	s := testStore(t, home)
	ctx := context.Background()
	b := testPublish(t, s, testID, testPayload())
	if err := s.Report(ctx, testID, b.ID, []string{"b.md", "a.md", "b.md"}); err != nil {
		t.Fatal(err)
	}
	s = testStore(t, home)
	before := testPending(t, s, testID)
	for _, id := range []Identity{testID, {Vault: "/vault", Conversation: "unknown", Generation: "g"}} {
		if r, err := s.Acknowledge(ctx, id, "wrong"); r != nil || !errors.Is(err, ErrMismatch) {
			t.Fatalf("wrong ack: %#v %v", r, err)
		}
	}
	if !reflect.DeepEqual(before, testPending(t, s, testID)) {
		t.Fatal("wrong ack changed pending")
	}
	if err := s.Report(ctx, testID, b.ID, nil); err != nil {
		t.Fatal(err)
	}
	want := &Receipt{BatchID: b.ID, Identity: testID, Processed: []MessageRevision{{ID: "chat/1", Revision: "text-v1"}}, LastTextEntry: 7, LastDeliveredEntry: 1, ClosingEntry: b.Payload.ClosingEntry, ChangedPaths: []string{"a.md", "b.md"}}
	got := testAck(t, s, testID, b.ID)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("receipt: %#v want %#v", got, want)
	}
	got.Processed[0].Revision, got.ChangedPaths[0], *got.ClosingEntry = "mutated", "mutated", 99
	s = testStore(t, home)
	if testPending(t, s, testID) != nil {
		t.Fatal("ack left pending")
	}
	if got := testAck(t, s, testID, b.ID); !reflect.DeepEqual(got, want) {
		t.Fatalf("replay: %#v", got)
	}
	if got, err := s.Publish(ctx, testID, b.Payload); err != nil || got != nil {
		t.Fatalf("reissued committed batch: %#v %v", got, err)
	}
	p := testPayload()
	p.Messages[0].Revision, p.Messages[0].Text = "text-v2", "edited"
	newer := testPublish(t, s, testID, p)
	if got := testAck(t, s, testID, b.ID); !reflect.DeepEqual(got, want) {
		t.Fatalf("historical replay: %#v", got)
	}
	if !reflect.DeepEqual(newer, testPending(t, s, testID)) {
		t.Fatal("replay cleared newer pending")
	}
	if err := s.Report(ctx, testID, b.ID, []string{"late.md"}); !errors.Is(err, ErrMismatch) {
		t.Fatal(err)
	}
	if r := testAck(t, s, testID, newer.ID); len(r.ChangedPaths) != 0 {
		t.Fatal("no-change receipt inherited old evidence")
	}
	history, err := testStore(t, home).Receipts(ctx, testID)
	if err != nil || len(history) != 2 || !reflect.DeepEqual(history[0], *want) {
		t.Fatalf("history: %#v %v", history, err)
	}
	history[0].Processed[0].ID, history[0].ChangedPaths[0], *history[0].ClosingEntry = "mutated", "mutated", 98
	again, err := s.Receipts(ctx, testID)
	if err != nil || !reflect.DeepEqual(again[0], *want) {
		t.Fatalf("history mutation persisted: %#v %v", again, err)
	}
}

func TestRevisionCompletion(t *testing.T) {
	s := testStore(t, t.TempDir())
	ctx := context.Background()
	p := testPayload()
	p.ClosingEntry = nil
	p.Messages = append(p.Messages, Message{ID: "assistant/2", Revision: "v1", Text: "old", Speaker: "assistant"})
	b := testPublish(t, s, testID, p)
	current := append([]Message(nil), p.Messages...)
	current[1].Revision, current[1].Text = "v2", "edited old row"
	current = append(current, Message{ID: "user/10", Revision: "v1", Text: "concurrent new row"})
	testAck(t, s, testID, b.ID)
	unprocessed, err := s.Unprocessed(ctx, testID, current)
	if err != nil || !reflect.DeepEqual(unprocessed, current[1:]) {
		t.Fatalf("revision query: %#v %v", unprocessed, err)
	}
	unprocessed[0].Text = "mutated"
	unprocessed, err = s.Unprocessed(ctx, testID, current)
	if err != nil || !reflect.DeepEqual(unprocessed, current[1:]) {
		t.Fatal("query aliased input")
	}
	closing := uint64(9)
	assertComplete := func(messages []Message, closure *uint64, want bool) {
		t.Helper()
		got, e := s.Complete(ctx, testID, messages, closure)
		if e != nil || got != want {
			t.Fatalf("complete=%v want %v: %v", got, want, e)
		}
	}
	assertComplete(current, nil, false)
	assertComplete(current, &closing, false)
	p.Messages, p.LastTextEntry, p.LastDeliveredEntry = current[1:], 10, 10
	testAck(t, s, testID, testPublish(t, s, testID, p).ID)
	assertComplete(current, &closing, false)
	original := append([]Message(nil), current...)
	original[1] = b.Payload.Messages[1]
	if rows, err := s.Unprocessed(ctx, testID, original); err != nil || len(rows) != 0 {
		t.Fatalf("forgot historical revision: %#v %v", rows, err)
	}
	p.Messages, p.ClosingEntry = nil, &closing
	closureBatch := testPublish(t, s, testID, p)
	assertComplete(current, &closing, false)
	closureReceipt := testAck(t, s, testID, closureBatch.ID)
	if len(closureReceipt.Processed) != 0 || len(closureReceipt.ChangedPaths) != 0 || *closureReceipt.ClosingEntry != closing {
		t.Fatalf("closure receipt: %#v", closureReceipt)
	}
	assertComplete(current, &closing, true)
	assertComplete(current, nil, false)
	wrongClosure := closing + 1
	assertComplete(current, &wrongClosure, false)
	late := append(append([]Message(nil), current...), Message{ID: "assistant/11", Revision: "v1", Text: "late"})
	assertComplete(late, &closing, false)
	late[1].Revision, late[1].Text = "v3", "edited again"
	assertComplete(late[:len(current)], &closing, false)
	unprocessed, err = s.Unprocessed(ctx, testID, late)
	if err != nil || !reflect.DeepEqual(unprocessed, []Message{late[1], late[3]}) {
		t.Fatalf("late query: %#v %v", unprocessed, err)
	}
	p.Messages, p.LastTextEntry = unprocessed, 11
	testAck(t, s, testID, testPublish(t, s, testID, p).ID)
	assertComplete(late, &closing, true)
	history, err := s.Receipts(ctx, testID)
	if err != nil || len(history) != 4 || !reflect.DeepEqual(history[2], *closureReceipt) {
		t.Fatalf("late text rewrote closure: %#v %v", history, err)
	}
	// Even a fully processed snapshot is incomplete while additional work is pending.
	p.Model = "different"
	p.Messages = current
	work := testPublish(t, s, testID, p)
	assertComplete(late, &closing, false)
	testAck(t, s, testID, work.ID)
	assertComplete(late, &closing, true)
}

func TestCompletionLedgerValidation(t *testing.T) {
	for _, kind := range []string{"duplicate", "pending-completed", "digest", "paths", "version", "orphan"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			s := testStore(t, home)
			b := testPublish(t, s, testID, testPayload())
			testAck(t, s, testID, b.ID)
			file := filepath.Join(home, ".pyry/memory/capture", recordName(testID))
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var r record
			if err = json.Unmarshal(raw, &r); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "duplicate":
				r.Completed = append(r.Completed, r.Completed[0])
			case "pending-completed":
				r.Batch = r.Completed[0]
			case "digest":
				r.Completed[0].Payload.Messages[0].Text = "changed"
			case "paths":
				r.Completed[0].ChangedPaths = []string{"../escape"}
			case "version":
				r.Version = 1
			case "orphan":
				r.Completed = nil
			}
			r.Checksum = checksum(r)
			raw, err = json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(file, raw, 0600); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if got, err := s.Acknowledge(ctx, testID, b.ID); got != nil || !errors.Is(err, ErrCorrupt) {
				t.Fatalf("accepted ledger: %#v %v", got, err)
			}
			if got, err := s.Receipts(ctx, testID); got != nil || !errors.Is(err, ErrCorrupt) {
				t.Fatalf("queried ledger: %#v %v", got, err)
			}
			if now, err := os.ReadFile(file); err != nil || string(now) != string(raw) {
				t.Fatal("corrupt ledger overwritten")
			}
		})
	}
}

func TestCompletionInvalidInputs(t *testing.T) {
	s := testStore(t, t.TempDir())
	ctx := context.Background()
	for _, m := range []Message{{Revision: "v1"}, {ID: "row"}, {ID: "row", Revision: "v1", Text: string([]byte{255})}} {
		if rows, err := s.Unprocessed(ctx, testID, []Message{m}); rows != nil || !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid revision query: %#v %v", rows, err)
		}
		if complete, err := s.Complete(ctx, testID, []Message{m}, nil); complete || !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid completion query: %v %v", complete, err)
		}
	}
	if r, err := s.Acknowledge(ctx, testID, ""); r != nil || !errors.Is(err, ErrMismatch) {
		t.Fatalf("empty ack: %#v %v", r, err)
	}
}

func TestCompletionIsolation(t *testing.T) {
	s := testStore(t, t.TempDir())
	ctx := context.Background()
	ids := []Identity{testID, {Vault: "/other", Conversation: testID.Conversation, Generation: testID.Generation}, {Vault: testID.Vault, Conversation: "other", Generation: testID.Generation}, {Vault: testID.Vault, Conversation: testID.Conversation, Generation: "generation-2"}}
	b := testPublish(t, s, testID, testPayload())
	testAck(t, s, testID, b.ID)
	for _, id := range ids[1:] {
		rows, err := s.Unprocessed(ctx, id, b.Payload.Messages)
		if err != nil || !reflect.DeepEqual(rows, b.Payload.Messages) {
			t.Fatalf("progress crossed identity: %#v %v", rows, err)
		}
		history, err := s.Receipts(ctx, id)
		if err != nil || len(history) != 0 {
			t.Fatalf("history crossed identity: %#v %v", history, err)
		}
		if complete, err := s.Complete(ctx, id, b.Payload.Messages, b.Payload.ClosingEntry); err != nil || complete {
			t.Fatalf("completion crossed identity: %v %v", complete, err)
		}
		if r, err := s.Acknowledge(ctx, id, b.ID); r != nil || !errors.Is(err, ErrMismatch) {
			t.Fatalf("ack crossed identity: %#v %v", r, err)
		}
		other := testPublish(t, s, id, testPayload())
		if r := testAck(t, s, id, other.ID); r.Identity != id || r.BatchID == b.ID {
			t.Fatalf("receipt crossed identity: %#v", r)
		}
	}
}

func TestAcknowledgementFailures(t *testing.T) {
	for _, stage := range []string{"write", "file-sync", "close", "rename", "dir-sync", "cancel"} {
		t.Run(stage, func(t *testing.T) {
			home := t.TempDir()
			s := testStore(t, home)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			b := testPublish(t, s, testID, testPayload())
			if err := s.Report(ctx, testID, b.ID, []string{"changed.md"}); err != nil {
				t.Fatal(err)
			}
			before := testPending(t, s, testID)
			s.hook = func(at string) error {
				if stage == "cancel" && at == "rename" {
					cancel()
				}
				if at == stage {
					return errors.New("injected")
				}
				return nil
			}
			if r, err := s.Acknowledge(ctx, testID, b.ID); err == nil || r != nil {
				t.Fatalf("fault receipt: %#v %v", r, err)
			}
			reopened := testStore(t, home)
			pending := testPending(t, reopened, testID)
			history, err := reopened.Receipts(context.Background(), testID)
			if err != nil {
				t.Fatal(err)
			}
			rows, err := reopened.Unprocessed(context.Background(), testID, b.Payload.Messages)
			if err != nil {
				t.Fatal(err)
			}
			if stage == "dir-sync" {
				if pending != nil || len(history) != 1 || len(rows) != 0 || !reflect.DeepEqual(history[0].ChangedPaths, before.ChangedPaths) {
					t.Fatalf("torn commit: %#v %#v %#v", pending, history, rows)
				}
			} else if !reflect.DeepEqual(pending, before) || len(history) != 0 || !reflect.DeepEqual(rows, b.Payload.Messages) {
				t.Fatalf("pre-rename advanced: %#v %#v %#v", pending, history, rows)
			}
			testAck(t, reopened, testID, b.ID)
		})
	}
}

func TestVersionOneCompletion(t *testing.T) {
	home := t.TempDir()
	testStore(t, home)
	b := Batch{ID: batchID(testID, testPayload()), Payload: testPayload(), ChangedPaths: []string{"failed.md"}}
	// This schema deliberately has only the original fields, independently of record.
	legacy := struct {
		Version  int
		Identity Identity
		Batch    Batch
		Checksum string
	}{Version: 1, Identity: testID, Batch: b}
	legacy.Checksum = digest(legacy)
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(home, ".pyry/memory/capture", recordName(testID))
	if err = os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	s := testStore(t, home)
	if got := testPending(t, s, testID); !reflect.DeepEqual(got, &b) {
		t.Fatalf("legacy payload: %#v", got)
	}
	history, err := s.Receipts(context.Background(), testID)
	if err != nil || len(history) != 0 {
		t.Fatalf("legacy history: %#v %v", history, err)
	}
	rows, err := s.Unprocessed(context.Background(), testID, b.Payload.Messages)
	if err != nil || !reflect.DeepEqual(rows, b.Payload.Messages) {
		t.Fatalf("legacy progress: %#v %v", rows, err)
	}
	if now, err := os.ReadFile(file); err != nil || string(now) != string(raw) {
		t.Fatal("query rewrote legacy record")
	}
	r := testAck(t, s, testID, b.ID)
	if !reflect.DeepEqual(r.ChangedPaths, b.ChangedPaths) || !reflect.DeepEqual(r, testAck(t, testStore(t, home), testID, b.ID)) {
		t.Fatal("legacy upgrade lost evidence or receipt")
	}
}

func TestCompletionRejectsRecords(t *testing.T) {
	for _, unsafe := range []bool{false, true} {
		t.Run(fmt.Sprint(unsafe), func(t *testing.T) {
			home := t.TempDir()
			s := testStore(t, home)
			b := testPublish(t, s, testID, testPayload())
			file := filepath.Join(home, ".pyry/memory/capture", recordName(testID))
			if unsafe {
				if err := os.Chmod(file, 0644); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(file, []byte("corrupt"), 0600); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if r, err := s.Acknowledge(ctx, testID, b.ID); err == nil || r != nil {
				t.Fatalf("ack accepted record: %#v %v", r, err)
			}
			if r, err := s.Receipts(ctx, testID); err == nil || r != nil {
				t.Fatalf("history accepted record: %#v %v", r, err)
			}
			if r, err := s.Unprocessed(ctx, testID, b.Payload.Messages); err == nil || r != nil {
				t.Fatalf("progress accepted record: %#v %v", r, err)
			}
			if r, err := s.Complete(ctx, testID, b.Payload.Messages, b.Payload.ClosingEntry); err == nil || r {
				t.Fatalf("completion accepted record: %v %v", r, err)
			}
			if after, err := os.ReadFile(file); err != nil || string(after) != string(before) {
				t.Fatal("rejected record overwritten")
			}
		})
	}
}

func TestCompletionHelperProcess(t *testing.T) {
	home := os.Getenv("MEMORY_COMPLETION_TEST_HOME")
	if home == "" {
		return
	}
	s := testStore(t, home)
	ctx := context.Background()
	note := os.Getenv("MEMORY_COMPLETION_TEST_NOTE")
	batch := batchID(testID, testPayload())
	if err := s.Report(ctx, testID, batch, []string{note}); err == nil {
		fmt.Println("accepted", note)
	} else if !errors.Is(err, ErrMismatch) {
		t.Fatal(err)
	}
	testAck(t, s, testID, batch)
}

func TestConcurrentCompletion(t *testing.T) {
	home := t.TempDir()
	s := testStore(t, home)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	b := testPublish(t, s, testID, testPayload())
	var wg sync.WaitGroup
	outputs := make([]string, 4)
	for i := range outputs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCompletionHelperProcess$")
			cmd.Env = append(os.Environ(), "MEMORY_COMPLETION_TEST_HOME="+home, fmt.Sprintf("MEMORY_COMPLETION_TEST_NOTE=%d.md", i))
			raw, err := cmd.CombinedOutput()
			outputs[i] = string(raw)
			if err != nil {
				t.Errorf("process: %s %v", raw, err)
			}
		}(i)
	}
	wg.Wait()
	r := testAck(t, s, testID, b.ID)
	var accepted []string
	for i, out := range outputs {
		if strings.Contains(out, fmt.Sprintf("accepted %d.md", i)) {
			accepted = append(accepted, fmt.Sprintf("%d.md", i))
		}
	}
	if len(accepted) == 0 || !reflect.DeepEqual(r.ChangedPaths, accepted) {
		t.Fatalf("lost accepted evidence: %#v want %#v", r.ChangedPaths, accepted)
	}
	p := testPayload()
	p.Messages[0].Revision = "v2"
	newer := testPublish(t, s, testID, p)
	for i := 0; i < 8; i++ {
		handle := testStore(t, home)
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := handle.Acknowledge(ctx, testID, b.ID)
			if err != nil || !reflect.DeepEqual(got, r) {
				t.Errorf("concurrent replay: %#v %v", got, err)
			}
			if _, err := handle.Acknowledge(ctx, testID, newer.ID); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	history, err := s.Receipts(ctx, testID)
	if err != nil || len(history) != 2 || !reflect.DeepEqual(history[0], *r) || testPending(t, s, testID) != nil {
		t.Fatalf("concurrent history: %#v %v", history, err)
	}
}
