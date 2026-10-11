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
	"sync"
	"testing"
	"time"
)

var testID = Identity{Vault: "/vault", Conversation: "chat", Generation: "generation-1"}

func testPayload() Payload {
	closing := uint64(9)
	return Payload{Agent: "claude", Model: "model", Messages: []Message{{ID: "chat/1", Revision: "text-v1", Text: " text \n", Speaker: "user", Agent: "claude", Session: "route", Timestamp: "stamp"}}, LastTextEntry: 7, LastDeliveredEntry: 1, ClosingEntry: &closing, Metadata: json.RawMessage(`{"title":"chat"}`)}
}
func testStore(t *testing.T, home string) *Store {
	t.Helper()
	home, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}
func testPending(t *testing.T, s *Store, id Identity) *Batch {
	t.Helper()
	b, err := s.Pending(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func TestImmutableRetry(t *testing.T) {
	for _, closureOnly := range []bool{false, true} {
		t.Run(fmt.Sprint(closureOnly), func(t *testing.T) {
			home := t.TempDir()
			s := testStore(t, home)
			p := testPayload()
			if closureOnly {
				p.Messages = nil
				p.LastTextEntry = 0
				p.LastDeliveredEntry = 0
			}
			wantRaw, _ := json.Marshal(p)
			b, err := s.Publish(context.Background(), testID, p)
			if err != nil {
				t.Fatal(err)
			}
			batchID := b.ID
			if len(p.Messages) > 0 {
				p.Messages[0].Text = "new text"
				p.Messages[0].Revision = "text-v2"
				b.Payload.Messages[0].Text = "returned mutation"
			}
			p.Metadata[0] = 'x'
			*p.ClosingEntry = 100
			*b.Payload.ClosingEntry = 101
			b.Payload.Metadata[0] = 'x'
			b.ID = "changed"
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if got, err := s.Publish(ctx, testID, testPayload()); !errors.Is(err, context.Canceled) || got != nil {
				t.Fatalf("cancel: %#v %v", got, err)
			}
			for _, handle := range []*Store{s, testStore(t, home)} {
				newer := testPayload()
				newer.Agent, newer.Model = "codex", "new"
				newer.Messages[0].Text, newer.Messages[0].Revision = "newer row text", "text-v2"
				newer.LastTextEntry = 10
				retry, err := handle.Publish(context.Background(), testID, newer)
				if err != nil {
					t.Fatal(err)
				}
				raw, _ := json.Marshal(retry.Payload)
				if string(raw) != string(wantRaw) || retry.ID != batchID {
					t.Fatalf("immutable retry: %s %#v", raw, retry)
				}
			}
		})
	}
}
func TestIdentityIsolation(t *testing.T) {
	s := testStore(t, t.TempDir())
	ids := []Identity{testID, {Vault: "/other", Conversation: "chat", Generation: "generation-1"}, {Vault: "/vault", Conversation: "other", Generation: "generation-1"}, {Vault: "/vault", Conversation: "chat", Generation: "generation-2"}}
	for i, id := range ids {
		p := testPayload()
		p.Messages[0].Text = fmt.Sprint(i)
		if _, err := s.Publish(context.Background(), id, p); err != nil {
			t.Fatal(err)
		}
	}
	for i, id := range ids {
		if b := testPending(t, s, id); b.Payload.Messages[0].Text != fmt.Sprint(i) {
			t.Fatalf("isolation: %#v", b)
		}
	}
}
func TestEvidence(t *testing.T) {
	home := t.TempDir()
	s := testStore(t, home)
	ctx := context.Background()
	b, err := s.Publish(ctx, testID, testPayload())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Report(ctx, testID, b.ID, []string{"notes/b.md", "notes/a.md", "notes/b.md"}); err != nil {
		t.Fatal(err)
	}
	if err = testStore(t, home).Report(ctx, testID, b.ID, nil); err != nil {
		t.Fatal(err)
	}
	want := []string{"notes/a.md", "notes/b.md"}
	got := testPending(t, s, testID)
	if !reflect.DeepEqual(got.ChangedPaths, want) {
		t.Fatal(got)
	}
	got.ChangedPaths[0] = "mutation"
	for _, paths := range [][]string{{"accepted.md", "/absolute"}, {"accepted.md", "../escape"}, {"accepted.md", "notes/../../escape"}, {"accepted.md", "notes/../a"}, {"accepted.md", ""}, {"accepted.md", "a\\b"}, {"accepted.md", "."}} {
		if err = s.Report(ctx, testID, b.ID, paths); err == nil {
			t.Fatalf("accepted %q", paths)
		}
		if got := testPending(t, s, testID); !reflect.DeepEqual(got.ChangedPaths, want) {
			t.Fatalf("partial evidence: %#v", got)
		}
	}
	for _, id := range []Identity{testID, {Vault: "/vault", Conversation: "unknown", Generation: "generation-1"}} {
		if err = s.Report(ctx, id, "wrong", []string{"new.md"}); err == nil {
			t.Fatal("accepted mismatch")
		}
	}
	if !reflect.DeepEqual(testPending(t, s, testID).ChangedPaths, want) {
		t.Fatal("mismatch changed evidence")
	}
}
func TestConcurrentStores(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	const n = 12
	handles := make([]*Store, n)
	for i := range handles {
		handles[i] = testStore(t, home)
	}
	batches := make(chan *Batch, n)
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i, s := range handles {
		wg.Add(1)
		go func(i int, s *Store) {
			defer wg.Done()
			p := testPayload()
			p.Model = fmt.Sprint(i)
			b, e := s.Publish(ctx, testID, p)
			batches <- b
			errs <- e
		}(i, s)
	}
	wg.Wait()
	close(batches)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	b := testPending(t, handles[0], testID)
	for got := range batches {
		if !reflect.DeepEqual(got, b) {
			t.Fatalf("publish diverged: %#v %#v", got, b)
		}
	}
	for i, s := range handles {
		wg.Add(1)
		go func(i int, s *Store) {
			defer wg.Done()
			if e := s.Report(ctx, testID, b.ID, []string{fmt.Sprintf("%02d.md", i)}); e != nil {
				t.Error(e)
			}
		}(i, s)
	}
	wg.Wait()
	got := testPending(t, handles[0], testID)
	if len(got.ChangedPaths) != n {
		t.Fatal(got)
	}
}
func TestHelperProcess(t *testing.T) {
	home := os.Getenv("MEMORY_CAPTURE_TEST_HOME")
	if home == "" {
		return
	}
	home, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := testPayload()
	p.Model = os.Getenv("MEMORY_CAPTURE_TEST_NOTE")
	b, err := s.Publish(context.Background(), testID, p)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Report(context.Background(), testID, b.ID, []string{p.Model}); err != nil {
		t.Fatal(err)
	}
}
func TestCrossProcess(t *testing.T) {
	home := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmds := make([]*exec.Cmd, 4)
	for i := range cmds {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHelperProcess$")
		cmd.Env = append(os.Environ(), "MEMORY_CAPTURE_TEST_HOME="+home, fmt.Sprintf("MEMORY_CAPTURE_TEST_NOTE=%d.md", i))
		cmds[i] = cmd
		if err := cmd.Start(); err != nil {
			t.Error(err)
			cmds[i] = nil
			break
		}
	}
	for _, cmd := range cmds {
		if cmd == nil {
			continue
		}
		if err := cmd.Wait(); err != nil {
			t.Error(err)
		}
	}
	if t.Failed() {
		t.FailNow()
	}
	b := testPending(t, testStore(t, home), testID)
	if b == nil || len(b.ChangedPaths) != 4 {
		t.Fatalf("cross-process lost union: %#v", b)
	}
}
func TestPublicationFailures(t *testing.T) {
	for _, stage := range []string{"write", "file-sync", "close", "rename", "dir-sync"} {
		t.Run(stage, func(t *testing.T) {
			home := t.TempDir()
			s := testStore(t, home)
			s.hook = func(at string) error {
				if at == stage {
					return errors.New("injected")
				}
				return nil
			}
			b, err := s.Publish(context.Background(), testID, testPayload())
			if err == nil || b != nil {
				t.Fatalf("failed publication executable: %#v %v", b, err)
			}
			reopened := testStore(t, home)
			old := testPending(t, reopened, testID)
			if stage != "dir-sync" && old != nil {
				t.Fatalf("pre-rename published: %#v", old)
			}
			if stage == "dir-sync" && old == nil {
				t.Fatal("renamed batch missing")
			}
			p := testPayload()
			p.Model = "replacement"
			b, err = reopened.Publish(context.Background(), testID, p)
			if err != nil {
				t.Fatal(err)
			}
			if old != nil && !reflect.DeepEqual(old, b) {
				t.Fatal("retry replaced committed bytes")
			}
		})
	}
}
func TestEvidenceFailures(t *testing.T) {
	for _, stage := range []string{"write", "file-sync", "close", "rename", "dir-sync"} {
		t.Run(stage, func(t *testing.T) {
			home := t.TempDir()
			s := testStore(t, home)
			ctx := context.Background()
			b, err := s.Publish(ctx, testID, testPayload())
			if err != nil {
				t.Fatal(err)
			}
			if err = s.Report(ctx, testID, b.ID, []string{"old.md"}); err != nil {
				t.Fatal(err)
			}
			s.hook = func(at string) error {
				if at == stage {
					return errors.New("injected")
				}
				return nil
			}
			if err = s.Report(ctx, testID, b.ID, []string{"b.md", "a.md"}); err == nil {
				t.Fatal("fault ignored")
			}
			reopened := testStore(t, home)
			got := testPending(t, reopened, testID)
			want := []string{"old.md"}
			if stage == "dir-sync" {
				want = []string{"a.md", "b.md", "old.md"}
			}
			if !reflect.DeepEqual(got.ChangedPaths, want) || !reflect.DeepEqual(got.Payload, b.Payload) {
				t.Fatalf("partial record: %#v", got)
			}
			if err = reopened.Report(ctx, testID, b.ID, nil); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(testPending(t, reopened, testID), got) {
				t.Fatal("empty retry lost committed evidence")
			}
		})
	}
}
func TestCorruption(t *testing.T) {
	for _, body := range []string{"{", `{}`, `{"version":999}`, `null`} {
		t.Run(body, func(t *testing.T) {
			home := t.TempDir()
			s := testStore(t, home)
			if _, err := s.Publish(context.Background(), testID, testPayload()); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(home, ".pyry", "memory", "capture", recordName(testID))
			if err := os.WriteFile(file, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			if b, err := s.Pending(context.Background(), testID); err == nil || b != nil {
				t.Fatal("corruption executable")
			}
			if b, err := s.Publish(context.Background(), testID, testPayload()); err == nil || b != nil {
				t.Fatal("corruption replaced")
			}
			raw, _ := os.ReadFile(file)
			if string(raw) != body {
				t.Fatal("corrupt bytes overwritten")
			}
		})
	}
}

func TestRecordIntegrity(t *testing.T) {
	for _, field := range []string{"identity", "batch", "text", "evidence", "checksum", "unsafe-evidence"} {
		t.Run(field, func(t *testing.T) {
			home := t.TempDir()
			s := testStore(t, home)
			ctx := context.Background()
			b, e := s.Publish(ctx, testID, testPayload())
			if e != nil {
				t.Fatal(e)
			}
			file := filepath.Join(home, ".pyry/memory/capture", recordName(testID))
			raw, e := os.ReadFile(file)
			if e != nil {
				t.Fatal(e)
			}
			var r record
			if e = json.Unmarshal(raw, &r); e != nil {
				t.Fatal(e)
			}
			switch field {
			case "identity":
				r.Identity.Generation = "other"
			case "batch":
				r.Batch.ID = "other"
			case "text":
				r.Batch.Payload.Messages[0].Text = "changed"
			case "evidence":
				r.Batch.ChangedPaths = []string{"new.md"}
			case "checksum":
				r.Checksum = ""
			case "unsafe-evidence":
				r.Batch.ChangedPaths = []string{"../escape"}
				r.Checksum = checksum(r)
			}
			raw, e = json.Marshal(r)
			if e != nil {
				t.Fatal(e)
			}
			if e = os.WriteFile(file, raw, 0600); e != nil {
				t.Fatal(e)
			}
			if got, e := s.Pending(ctx, testID); got != nil || !errors.Is(e, ErrCorrupt) {
				t.Fatalf("%s executable: %#v %v", field, got, e)
			}
			if e = s.Report(ctx, testID, b.ID, []string{"accepted.md"}); !errors.Is(e, ErrCorrupt) {
				t.Fatal(e)
			}
			got, _ := os.ReadFile(file)
			if string(got) != string(raw) {
				t.Fatal("corrupt evidence overwritten")
			}
		})
	}
}
