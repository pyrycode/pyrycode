package main

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// fakeCodexFamilies is what the fake Codex's model/list fixture reduces to.
var fakeCodexFamilies = []turnevent.ModelOption{
	{Value: "sol", ResolvedModel: "gpt-6-sol", EffortLevels: []string{"low", "medium", "high", "xhigh"}},
	{Value: "luna", ResolvedModel: "gpt-6-luna", EffortLevels: []string{"minimal", "low", "medium"}},
	{Value: "terra", ResolvedModel: "gpt-5.6-terra", EffortLevels: []string{"low", "medium"}},
	{Value: "astra", ResolvedModel: "gpt-6-astra", EffortLevels: []string{"medium", "high"}},
}

// A Codex spawn reads model/list and the daemon's store holds the newest
// version per family beside Claude's list, which it leaves alone.
func TestCodexRunnerFactory_RetainsModelFamilies(t *testing.T) {
	store := newModelVocabularyStore(storePath(t))
	t.Cleanup(store.Close)
	claude := sentinelModelList("CLAUDE")
	store.Retain(claude)
	factory := newCodexRunnerFactory(codexHarness{bin: fakeCodexBin(t), home: t.TempDir(), sink: newStreamTurnSink(0, nil), vocab: store})
	runner, err := factory(sessions.RunnerConfig{SessionID: "s-1", Harness: harnessCodex, WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("factory = %v", err)
	}
	h := &codexHarnessT{r: runner.(*codexRunner)}
	h.run(t)
	deadline := time.Now().Add(10 * time.Second)
	for store.CodexModels() == nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := store.CodexModels(); !reflect.DeepEqual(got, fakeCodexFamilies) {
		t.Fatalf("CodexModels() = %#v\nwant %#v", got, fakeCodexFamilies)
	}
	if got, ok := store.ModelList(); !ok || !reflect.DeepEqual(got, claude) {
		t.Errorf("ModelList() = %#v, %v; want Claude's list unchanged", got, ok)
	}
}

// A failed model/list read reports nothing and does not fail the spawn: the
// client binds and runs a turn.
func TestCodexRunner_ModelListFailureKeepsSpawn(t *testing.T) {
	t.Setenv("FAKECODEX_MODEL_LIST_FAIL", "1")
	h := newTestCodexRunner(t)
	reported := make(chan []turnevent.ModelOption, 4)
	h.r.cfg.Models = func(m []turnevent.ModelOption) { reported <- m }
	h.run(t)
	client, _ := h.bound(t, nil)
	h.turn(t, "hello")
	h.await(t, "turn end", isTurnEnd)
	// The spawn's own read may still be in flight; this one is synchronous.
	h.r.readModels(context.Background(), client)
	select {
	case m := <-reported:
		t.Fatalf("failed read reported %#v", m)
	default:
	}
}
