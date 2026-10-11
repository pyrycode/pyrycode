//go:build e2e_realclaude

package realclaude

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

const promptHistoryConversation = "11111111-1111-4111-8111-111111111111"
const promptHistorySegment = "segment-00000000000000000001.jsonl"

// promptHistorySnapshotFS mutates the directory after returning its snapshot,
// before any file is opened by the reader.
type promptHistorySnapshotFS struct {
	fs.FS
	mutate func([]fs.DirEntry)
}

func (f promptHistorySnapshotFS) ReadDir(name string) ([]fs.DirEntry, error) {
	entries, err := fs.ReadDir(f.FS, name)
	if err == nil {
		f.mutate(entries)
	}
	return entries, err
}

func testPromptAnswerHistoryFixture(t *testing.T) (string, string, history.Entry) {
	t.Helper()
	home := t.TempDir()
	instanceDir := filepath.Join(home, ".pyry", "test")
	ts := time.Date(2026, 10, 11, 1, 2, 3, 0, time.UTC)
	shown := true
	e := history.Entry{
		Type:    "prompt_answered",
		Payload: json.RawMessage(`{"conversation_id":"` + promptHistoryConversation + `","correlation_id":"question","decision":"answer","source":"remote","resolved_at":"2026-10-11T01:02:03Z","context":{"questions":[]}}`),
		TS:      ts, Shown: &shown,
		Session: &history.SessionProvenance{Kind: "claude", SessionID: streamModalBootstrapUUID},
	}
	store := history.New(instanceDir)
	id, err := store.AppendWithMetadata(conversations.ConversationID(promptHistoryConversation), e.Type, e.Payload, e.TS,
		history.Metadata{Session: e.Session, Shown: e.Shown})
	if err != nil {
		t.Fatal(err)
	}
	e.ID = id
	dir := filepath.Join(instanceDir, "conversations", promptHistoryConversation, "history")
	return home, dir, e
}

func testWritePromptHistoryEntry(t *testing.T, path string, e history.Entry) {
	t.Helper()
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPromptAnswerHistoryReaderPublication(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"rename", "remove"} {
		t.Run(action, func(t *testing.T) {
			t.Parallel()
			home, dir, want := testPromptAnswerHistoryFixture(t)
			const tempName = ".thread-publication"
			tempPath := filepath.Join(dir, tempName)
			testWritePromptHistoryEntry(t, tempPath, want)
			mutated := false
			fsys := promptHistorySnapshotFS{FS: os.DirFS(dir), mutate: func(entries []fs.DirEntry) {
				listed := false
				for _, entry := range entries {
					listed = listed || entry.Name() == tempName
				}
				if !listed {
					t.Fatal("publication file absent from directory snapshot")
				}
				var err error
				if action == "rename" {
					err = os.Rename(tempPath, filepath.Join(dir, "thread-cache.json"))
				} else {
					err = os.Remove(tempPath)
				}
				if err != nil {
					t.Fatal(err)
				}
				mutated = true
			}}
			got, err := readPromptAnswerHistory(fsys)
			if err != nil {
				t.Fatal(err)
			}
			if !mutated || !reflect.DeepEqual(got, []history.Entry{want}) {
				t.Fatalf("read after publication = %#v, want committed answer %#v; mutated=%v", got, want, mutated)
			}
			requirePromptAnswerHistory(t, &perConvHarness{home: home}, promptHistoryConversation, "question", "answer", true)
		})
	}
}

func TestPromptAnswerHistoryReaderSelection(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		"segment-00000000000000000000.jsonl", promptHistorySegment, "segment-18446744073709551615.jsonl",
		".thread-publication", "thread-cache.json", "thread-recovery.json", "thread-run-0123456789abcdef0123456789abcdef.json", "notes.jsonl",
		"segment-1.jsonl", "segment-0000000000000000000.jsonl", "segment-000000000000000000000.jsonl",
		"segment-18446744073709551616.jsonl", "segment-0000000000000000000x.jsonl", "segment-+0000000000000000001.jsonl",
		"segment-00000000000000000000.jsonl.tmp", "segment-00000000000000000000.JSONL",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, _, want := testPromptAnswerHistoryFixture(t)
			dir := t.TempDir()
			testWritePromptHistoryEntry(t, filepath.Join(dir, name), want)
			got, err := readPromptAnswerHistory(os.DirFS(dir))
			if err != nil {
				t.Fatal(err)
			}
			if name == promptHistorySegment || name == "segment-00000000000000000000.jsonl" || name == "segment-18446744073709551615.jsonl" {
				if !reflect.DeepEqual(got, []history.Entry{want}) {
					t.Fatalf("selected segment = %#v, want %#v", got, want)
				}
			} else if len(got) != 0 {
				t.Fatalf("non-segment supplied %d answers", len(got))
			}
		})
	}
	for _, kind := range []string{"directory", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			_, dir, want := testPromptAnswerHistoryFixture(t)
			otherDir := t.TempDir()
			path := filepath.Join(otherDir, promptHistorySegment)
			var err error
			if kind == "directory" {
				err = os.Mkdir(path, 0o700)
			} else {
				err = os.Symlink(filepath.Join(dir, promptHistorySegment), path)
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := readPromptAnswerHistory(os.DirFS(otherDir))
			if err != nil || len(got) != 0 {
				t.Fatalf("nonregular leaf = %#v, %v; must not supply %#v", got, err, want)
			}
		})
	}
}

func TestPromptAnswerHistoryReaderSegmentReadFailure(t *testing.T) {
	t.Parallel()
	_, dir, _ := testPromptAnswerHistoryFixture(t)
	fsys := promptHistorySnapshotFS{FS: os.DirFS(dir), mutate: func([]fs.DirEntry) {
		if err := os.Remove(filepath.Join(dir, promptHistorySegment)); err != nil {
			t.Fatal(err)
		}
	}}
	if _, err := readPromptAnswerHistory(fsys); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("selected segment disappearance = %v, want read failure", err)
	}
}

func TestPromptAnswerHistoryReaderPartialTail(t *testing.T) {
	t.Parallel()
	_, dir, want := testPromptAnswerHistoryFixture(t)
	path := filepath.Join(dir, promptHistorySegment)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw[:len(raw)-10], 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readPromptAnswerHistory(os.DirFS(dir))
	if err != nil || len(got) != 0 {
		t.Fatalf("partial tail = %#v, %v; want no complete answer", got, err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = readPromptAnswerHistory(os.DirFS(dir))
	if err != nil || !reflect.DeepEqual(got, []history.Entry{want}) {
		t.Fatalf("completed tail = %#v, %v; want %#v", got, err, want)
	}
	testWritePromptHistoryEntry(t, filepath.Join(dir, "segment-00000000000000000002.jsonl"), want)
	got, err = readPromptAnswerHistory(os.DirFS(dir))
	if err != nil || len(got) != 2 {
		t.Fatalf("duplicate facts = %#v, %v; want both retained for assertion failure", got, err)
	}
}

func TestPromptAnswerHistoryReaderDirectoryErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	got, err := readPromptAnswerHistory(os.DirFS(filepath.Join(dir, "missing")))
	if err != nil || len(got) != 0 {
		t.Fatalf("missing directory = %#v, %v; want no facts yet", got, err)
	}
	path := filepath.Join(dir, "file")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPromptAnswerHistory(os.DirFS(path)); err == nil {
		t.Fatal("directory read failure silently ignored")
	}
}
