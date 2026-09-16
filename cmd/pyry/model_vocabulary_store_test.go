package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// storePath returns a path to model_list.json inside a fresh temp dir whose
// INSTANCE DIRECTORY DOES NOT EXIST — the state writeMCPSettings documents as
// reachable, since the pool's registry save is what creates it and need not have
// run by the first write.
func storePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "instance", "model_list.json")
}

// retainAndClose drives one store through its whole write path and joins the
// writer, so every assertion below reads a settled file rather than polling for
// one. Close is the join point; a test that slept instead would be flaky by
// construction.
func retainAndClose(t *testing.T, path string, lists ...turnevent.ModelList) {
	t.Helper()
	s := newModelVocabularyStore(path)
	for _, list := range lists {
		s.Retain(list)
	}
	s.Close()
}

// AC 1 + AC 3 + the round-trip rule: every field of a retained list survives the
// write and comes back out of a FRESH store's Load, DroppedModels and
// TruncatedFields included. sentinelModelList's second entry is the awkward one —
// nil EffortLevels, nil TruncatedFields, false SupportsAutoMode — so a store that
// allocates empty slices on either side is visible.
func TestModelVocabularyStore_RoundTripsTheWholeValue(t *testing.T) {
	t.Parallel()
	path := storePath(t)
	want := sentinelModelList("SAVED")
	retainAndClose(t, path, want)

	restored := newModelVocabularyStore(path)
	restored.Load()
	got, ok := restored.ModelList()
	if !ok {
		t.Fatal("ModelList() reported no list after Load; the write or the read dropped it")
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("restored list = %#v, want %#v", got, want)
	}
	if got.DroppedModels != want.DroppedModels {
		t.Errorf("DroppedModels = %d, want %d — a truncated list must not come back reading as complete", got.DroppedModels, want.DroppedModels)
	}
}

// The newest value wins and the writer coalesces: three retentions leave the file
// holding the third, not the first and not a concatenation.
func TestModelVocabularyStore_LastRetentionWins(t *testing.T) {
	t.Parallel()
	path := storePath(t)
	want := sentinelModelList("THIRD")
	retainAndClose(t, path, sentinelModelList("FIRST"), sentinelModelList("SECOND"), want)

	restored := newModelVocabularyStore(path)
	restored.Load()
	got, ok := restored.ModelList()
	if !ok {
		t.Fatal("ModelList() reported no list")
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("restored list = %#v, want the last retention %#v", got, want)
	}
}

// AC 4: absent, unreadable, undecodable, oversized and empty-models files all
// answer "no list" and the daemon still starts. The never-empty arm is a CONTRACT
// check rather than a bound — turnevent.ModelList.Models is documented never
// empty, so a list with no models is not a value any reader may be handed.
func TestModelVocabularyStore_LoadRefusesAndStarts(t *testing.T) {
	t.Parallel()
	oversized := `{"models":[{"resolved_model":"` + strings.Repeat("a", 70*1024) + `"}]}`
	for _, tc := range []struct {
		name    string
		write   string
		absent  bool
		asOwnFn func(t *testing.T, path string)
	}{
		{name: "absent", absent: true},
		{name: "malformed json", write: `{"models":[`},
		{name: "not json at all", write: "\x00\x01not json"},
		{name: "empty models", write: `{"models":[]}`},
		{name: "null models", write: `{"models":null}`},
		{name: "oversized", write: oversized},
		{name: "a directory in its place", asOwnFn: func(t *testing.T, path string) {
			if err := os.MkdirAll(path, 0o700); err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := storePath(t)
			switch {
			case tc.asOwnFn != nil:
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatalf("MkdirAll: %v", err)
				}
				tc.asOwnFn(t, path)
			case !tc.absent:
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatalf("MkdirAll: %v", err)
				}
				if err := os.WriteFile(path, []byte(tc.write), 0o600); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
			}
			s := newModelVocabularyStore(path)
			s.Load()
			if got, ok := s.ModelList(); ok {
				t.Fatalf("ModelList() = (%+v, true), want the no-list answer", got)
			}
		})
	}
}

// An empty non-nil list in the FILE decodes to nil, because turnevent spells
// "nothing to report" as nil and a [] would otherwise reach a consumer as a shape
// the producer never emits.
func TestModelVocabularyStore_LoadNormalisesEmptySlicesToNil(t *testing.T) {
	t.Parallel()
	path := storePath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	body := `{"models":[{"resolved_model":"r","value":"v","display_name":"d","effort_levels":[],"truncated_fields":[]}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	s := newModelVocabularyStore(path)
	s.Load()
	got, ok := s.ModelList()
	if !ok {
		t.Fatal("ModelList() reported no list")
	}
	if got.Models[0].EffortLevels != nil {
		t.Errorf("EffortLevels = %#v, want nil", got.Models[0].EffortLevels)
	}
	if got.Models[0].TruncatedFields != nil {
		t.Errorf("TruncatedFields = %#v, want nil", got.Models[0].TruncatedFields)
	}
}

// The file is 0600 inside a 0700 directory the store created itself, and no
// scratch file survives a successful write.
func TestModelVocabularyStore_WritesPrivatelyAndLeavesNoScratch(t *testing.T) {
	t.Parallel()
	path := storePath(t)
	retainAndClose(t, path, sentinelModelList("MODE"))

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("file mode = %v, want 0600", got)
	}
	di, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	if got := di.Mode().Perm(); got != 0o700 {
		t.Errorf("instance dir mode = %v, want 0700", got)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "model_list.json" {
			t.Errorf("leftover %q beside the store; a successful write must remove its scratch file", e.Name())
		}
	}
}

// The value handed out is a DEEP copy, so one reader mutating it cannot corrupt
// the retained value or another reader's copy — sessionModelHold.ModelList's
// contract, inherited because this source is read through the same method.
func TestModelVocabularyStore_ModelListIsADeepCopy(t *testing.T) {
	t.Parallel()
	s := newModelVocabularyStore(storePath(t))
	s.Retain(sentinelModelList("COPY"))
	defer s.Close()

	first, ok := s.ModelList()
	if !ok {
		t.Fatal("ModelList() reported no list")
	}
	first.Models[0].ResolvedModel = "MUTATED"
	first.Models[0].EffortLevels[0] = "MUTATED"
	first.Models[0].TruncatedFields[0] = "MUTATED"
	first.DroppedModels = 99

	second, ok := s.ModelList()
	if !ok {
		t.Fatal("second ModelList() reported no list")
	}
	if !reflect.DeepEqual(second, sentinelModelList("COPY")) {
		t.Errorf("second copy = %#v; a reader's mutation reached the retained value", second)
	}
}

// A nil store answers the unreported state rather than panicking, mirroring
// sessionModelHold.ModelList — which is what lets a daemon with no store be two
// sources rather than a special case at every arm.
func TestModelVocabularyStore_NilReceiverAnswersUnreported(t *testing.T) {
	t.Parallel()
	var s *modelVocabularyStore
	if got, ok := s.ModelList(); ok {
		t.Fatalf("(*modelVocabularyStore)(nil).ModelList() = (%+v, true), want the unreported state", got)
	}
	s.Retain(sentinelModelList("NIL"))
	s.Load()
	s.Close()
}

// sinkFor is a non-retaining DECORATOR: it retains ModelList and forwards every
// event of every variant unchanged, ModelList included. A nil next forwards
// nothing, matching the hold chain's test convenience.
func TestModelVocabularyStore_SinkForForwardsEveryVariant(t *testing.T) {
	t.Parallel()
	s := newModelVocabularyStore(storePath(t))
	defer s.Close()

	var seen []turnevent.Event
	sink := s.sinkFor(func(ev turnevent.Event) { seen = append(seen, ev) })
	want := sentinelModelList("SINK")
	events := []turnevent.Event{
		turnevent.TextChunk{},
		want,
		turnevent.TurnEnd{},
	}
	for _, ev := range events {
		sink(ev)
	}
	if len(seen) != len(events) {
		t.Fatalf("forwarded %d events, want %d — the decorator must swallow nothing", len(seen), len(events))
	}
	if !reflect.DeepEqual(seen[1], want) {
		t.Errorf("forwarded ModelList = %#v, want it unchanged %#v", seen[1], want)
	}
	got, ok := s.ModelList()
	if !ok {
		t.Fatal("the decorator forwarded the list but retained nothing")
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("retained %#v, want %#v", got, want)
	}

	// A nil next still Retains, so this store starts a drain writer like any
	// other and has to be joined like any other: an unjoined one can create
	// instance/ and its scratch file inside this TempDir after cleanup's
	// RemoveAll has walked it (#2480). The defer runs before that cleanup.
	nilStore := newModelVocabularyStore(storePath(t))
	defer nilStore.Close()
	nilNext := nilStore.sinkFor(nil)
	nilNext(want) // must not panic
}

// A retention identical to what is already on disk writes nothing — the
// write-amplification finding from the plan's security review. Asserted through
// the file's ModTime, which a skipped write leaves alone.
func TestModelVocabularyStore_IdenticalRetentionSkipsTheWrite(t *testing.T) {
	t.Parallel()
	path := storePath(t)
	list := sentinelModelList("SAME")

	s := newModelVocabularyStore(path)
	s.Retain(list)
	s.Close()
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}

	again := newModelVocabularyStore(path)
	again.Load()
	again.Retain(list)
	again.Close()
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat after: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("a retention identical to the loaded value rewrote the file; the encoded-bytes comparison did not fire")
	}
}

// Concurrent retention and reading from several goroutines, for -race. One
// writer per child is production's shape; this runs many so the leaf mutex and
// the single-flight writer are exercised together.
func TestModelVocabularyStore_ConcurrentRetainAndRead(t *testing.T) {
	t.Parallel()
	s := newModelVocabularyStore(storePath(t))
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			s.Retain(sentinelModelList("RACE"))
		}(i)
		go func() {
			defer wg.Done()
			s.ModelList()
		}()
	}
	wg.Wait()
	s.Close()

	got, ok := s.ModelList()
	if !ok {
		t.Fatal("ModelList() reported no list after concurrent retention")
	}
	if !reflect.DeepEqual(got, sentinelModelList("RACE")) {
		t.Errorf("retained %#v, want the sentinel", got)
	}
}

// #2450 AC 1 through the REAL production composition: a runner built by
// newStreamRunnerFactory with a store attached answers one real initialize control
// request and the store both retains the decoded list AND lands it on disk.
//
// This is the ONLY test that can redden a factory which forgot to chain the
// decorator. Everything else in this file exercises the store through its own API,
// so the store could be perfect while nothing ever fed it — the silent failure this
// ticket's whole value depends on not having. It is
// TestNewSessionParser_DecodesAndRetains' argument (prove the WIRING, not the
// contract) carried one layer out, to the factory that owns this chain link.
//
// The fake child is TestStreamRunnerFactory_RequestsSummaryAfterTurnEnd's shape: a
// shell script that reads the initialize control_request off stdin, answers it, and
// then sleeps so the runner stays up while the assertion runs.
func TestStreamRunnerFactory_PersistsTheModelVocabulary(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	childPath := filepath.Join(dir, "fake-claude.sh")
	path := filepath.Join(dir, "instance", "model_list.json")
	const reply = `{"type":"control_response","response":{"subtype":"success","request_id":"1",` +
		`"response":{"models":[` +
		`{"resolvedModel":"claude-opus-5","value":"opus","displayName":"Opus"},` +
		`{"resolvedModel":"claude-sonnet-5","value":"sonnet","displayName":"Sonnet"}]}}}`
	script := "#!/bin/sh\nIFS= read -r initialize\nprintf '%s\\n' '" + reply + "'\nexec sleep 3600\n"
	if err := os.WriteFile(childPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}

	store := newModelVocabularyStore(path)
	runner, err := newStreamRunnerFactory(newStreamTurnSink(8, discardLogger()), "", store, streamApprovalConfig{})(sessions.RunnerConfig{
		ClaudeBin: childPath,
		WorkDir:   dir,
		SessionID: "factory-session-2450",
		Logger:    discardLogger(),
	})
	if err != nil {
		t.Fatalf("newStreamRunnerFactory: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	// The retention is what the decorator does synchronously on the child's stdout
	// goroutine; the child is asynchronous, so this waits for it rather than for a
	// timer.
	var got turnevent.ModelList
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if list, ok := store.ModelList(); ok {
			got = list
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(got.Models) != 2 {
		t.Fatalf("store retained %d entries after the child's initialize reply, want 2 — the factory did not chain the persist decorator", len(got.Models))
	}
	if got.Models[0].Value != "opus" || got.Models[1].Value != "sonnet" {
		t.Errorf("retained values = %q, %q; want %q, %q", got.Models[0].Value, got.Models[1].Value, "opus", "sonnet")
	}

	// Close joins the writer, so the file below is settled rather than raced.
	store.Close()
	restored := newModelVocabularyStore(path)
	restored.Load()
	fromFile, ok := restored.ModelList()
	if !ok {
		t.Fatal("nothing was persisted; the retention reached memory but not the file")
	}
	if !reflect.DeepEqual(fromFile, got) {
		t.Errorf("restored %#v, want what was retained %#v", fromFile, got)
	}
}
