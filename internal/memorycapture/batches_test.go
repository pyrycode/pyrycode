package memorycapture

import (
	"bytes"
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
	"unicode/utf8"
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	testPlatform(t, ctx)
	const n = 12
	ready := make(chan struct{}, n)
	start := make(chan struct{})
	handles := make([]*Store, n)
	for i := range handles {
		handles[i] = testStore(t, home)
		var once sync.Once
		handles[i].hook = func(stage string) error {
			if stage == "lock-open" {
				once.Do(func() {
					ready <- struct{}{}
					select {
					case <-start:
					case <-ctx.Done():
					}
				})
			}
			return ctx.Err()
		}
	}
	if _, err := os.Lstat(filepath.Join(home, ".pyry/memory/capture/.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("first publication requires absent lock: %v", err)
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
			errs <- testOperationError("publish", fmt.Sprint(i), e)
		}(i, s)
	}
waiting:
	for range n {
		select {
		case <-ready:
		case <-ctx.Done():
			t.Error("lock-open barrier:", ctx.Err())
			break waiting
		}
	}
	close(start)
	wg.Wait()
	close(batches)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Error(e)
		}
	}
	if t.Failed() {
		t.FailNow()
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
			if e := s.Report(ctx, testID, b.ID, []string{fmt.Sprintf("%02d.md", i), "shared.md", fmt.Sprintf("%02d.md", i)}); e != nil {
				t.Error(testOperationError("report", fmt.Sprint(i), e))
			}
		}(i, s)
	}
	wg.Wait()
	got := testPending(t, testStore(t, home), testID)
	want := make([]string, 0, n+1)
	for i := range n {
		want = append(want, fmt.Sprintf("%02d.md", i))
	}
	want = append(want, "shared.md")
	if !reflect.DeepEqual(got.ChangedPaths, want) || got.ID != b.ID || !reflect.DeepEqual(got.Payload, b.Payload) {
		t.Fatalf("reopened evidence/payload diverged: %#v", got)
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
		t.Fatal(testOperationError("publish", p.Model, err))
	}
	if err = s.Report(context.Background(), testID, b.ID, []string{p.Model}); err != nil {
		t.Fatal(testOperationError("report", p.Model, err))
	}
}
func TestCrossProcess(t *testing.T) {
	home := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmds := make([]*exec.Cmd, 4)
	outputs := make([]*testProcessOutput, len(cmds))
	for i := range cmds {
		cmd, output := testProcessCommand(ctx, home, fmt.Sprintf("%d.md", i))
		cmds[i] = cmd
		outputs[i] = output
		if err := cmd.Start(); err != nil {
			t.Errorf("worker=%d helper start: %v\n%s", i, err, output.String())
			cmds[i] = nil
			break
		}
	}
	for i, cmd := range cmds {
		if cmd == nil {
			continue
		}
		if err := cmd.Wait(); err != nil {
			t.Errorf("worker=%d helper wait: %v\n%s", i, err, outputs[i].String())
		}
	}
	if t.Failed() {
		t.FailNow()
	}
	b := testPending(t, testStore(t, home), testID)
	if b == nil || !reflect.DeepEqual(b.ChangedPaths, []string{"0.md", "1.md", "2.md", "3.md"}) {
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

func TestSplitUTF8InputRejectedBeforePublication(t *testing.T) {
	tests := []struct {
		name string
		pair func(*Identity, *Payload) (*string, *string)
	}{
		{"vault-conversation", func(id *Identity, _ *Payload) (*string, *string) { return &id.Vault, &id.Conversation }},
		{"conversation-generation", func(id *Identity, _ *Payload) (*string, *string) { return &id.Conversation, &id.Generation }},
		{"agent-model", func(_ *Identity, p *Payload) (*string, *string) { return &p.Agent, &p.Model }},
		{"message-id-revision", func(_ *Identity, p *Payload) (*string, *string) { return &p.Messages[0].ID, &p.Messages[0].Revision }},
		{"message-revision-text", func(_ *Identity, p *Payload) (*string, *string) { return &p.Messages[0].Revision, &p.Messages[0].Text }},
		{"message-text-timestamp", func(_ *Identity, p *Payload) (*string, *string) { return &p.Messages[0].Text, &p.Messages[0].Timestamp }},
		{"message-timestamp-speaker", func(_ *Identity, p *Payload) (*string, *string) {
			return &p.Messages[0].Timestamp, &p.Messages[0].Speaker
		}},
		{"message-speaker-agent", func(_ *Identity, p *Payload) (*string, *string) { return &p.Messages[0].Speaker, &p.Messages[0].Agent }},
		{"message-agent-session", func(_ *Identity, p *Payload) (*string, *string) { return &p.Messages[0].Agent, &p.Messages[0].Session }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			s := testStore(t, home)
			id, p := testID, testPayload()
			left, right := tt.pair(&id, &p)
			*left += "\xc3"
			*right = "\xa9" + *right
			if utf8.ValidString(*left) || utf8.ValidString(*right) || !utf8.ValidString(*left+*right) {
				t.Fatal("fixture must be invalid fields whose concatenation is valid")
			}
			if b, err := s.Publish(context.Background(), id, p); b != nil || !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid fields executable: %#v %v", b, err)
			}
			entries, err := os.ReadDir(filepath.Join(home, ".pyry/memory/capture"))
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if filepath.Ext(entry.Name()) == ".json" {
					t.Fatalf("invalid fields published record %s", entry.Name())
				}
			}
		})
	}
}

func TestRawInvalidUTF8RecordRejectedWithoutOverwrite(t *testing.T) {
	home := t.TempDir()
	s := testStore(t, home)
	ctx := context.Background()
	p := testPayload()
	p.Messages[0].Text = "\ufffd"
	b, err := s.Publish(ctx, testID, p)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(home, ".pyry/memory/capture", recordName(testID))
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := bytes.Replace(raw, []byte("\ufffd"), []byte{0xff}, 1)
	if bytes.Equal(corrupt, raw) || utf8.Valid(corrupt) {
		t.Fatal("fixture must replace a stored replacement rune with malformed bytes")
	}
	if err = os.WriteFile(file, corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"Pending", "Publish", "Report"} {
		t.Run(operation, func(t *testing.T) {
			reopened := testStore(t, home)
			var got *Batch
			var err error
			switch operation {
			case "Pending":
				got, err = reopened.Pending(ctx, testID)
			case "Publish":
				got, err = reopened.Publish(ctx, testID, testPayload())
			case "Report":
				err = reopened.Report(ctx, testID, b.ID, []string{"new.md"})
			}
			if got != nil || !errors.Is(err, ErrCorrupt) {
				t.Fatalf("malformed record accepted: %#v %v", got, err)
			}
			after, err := os.ReadFile(file)
			if err != nil || !bytes.Equal(after, corrupt) {
				t.Fatalf("malformed record overwritten: %v", err)
			}
		})
	}
}
