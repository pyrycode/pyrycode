package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// codexFamilies is a Codex list as codexsup.Client.LatestModels returns it.
func codexFamilies(sol string) []turnevent.ModelOption {
	return []turnevent.ModelOption{
		{Value: "sol", ResolvedModel: sol, EffortLevels: []string{"low", "high"}},
		{Value: "terra", ResolvedModel: "gpt-5.6-terra"},
	}
}

func reloadStore(t *testing.T, path string) *modelVocabularyStore {
	t.Helper()
	s := newModelVocabularyStore(path)
	s.Load()
	return s
}

// The Codex entries survive a restart, and holding only them is not a Claude
// list: ModelList, which feeds the model_list frame, still reports none.
func TestModelVocabularyStore_CodexSurvivesRestart(t *testing.T) {
	t.Parallel()
	path := storePath(t)
	s := newModelVocabularyStore(path)
	s.RetainCodex(codexFamilies("gpt-6-sol"))
	s.Close()

	restored := reloadStore(t, path)
	if got := restored.CodexModels(); !reflect.DeepEqual(got, codexFamilies("gpt-6-sol")) {
		t.Errorf("CodexModels() after restart = %#v", got)
	}
	if list, ok := restored.ModelList(); ok {
		t.Errorf("ModelList() = %#v, true; want no Claude list", list)
	}
}

// A Claude retention keeps the Codex entries and a Codex retention keeps the
// Claude ones, in memory and on disk.
func TestModelVocabularyStore_RetentionsKeepTheOtherAgent(t *testing.T) {
	t.Parallel()
	path := storePath(t)
	claude := sentinelModelList("CLAUDE")
	s := newModelVocabularyStore(path)
	s.RetainCodex(codexFamilies("gpt-5.6-sol"))
	s.Retain(claude)
	if got := s.CodexModels(); !reflect.DeepEqual(got, codexFamilies("gpt-5.6-sol")) {
		t.Errorf("Claude retention dropped the Codex entries: %#v", got)
	}
	s.Close()

	s = reloadStore(t, path)
	s.RetainCodex(codexFamilies("gpt-6-sol"))
	if got, ok := s.ModelList(); !ok || !reflect.DeepEqual(got, claude) {
		t.Errorf("Codex retention changed the Claude list: %#v, %v", got, ok)
	}
	s.Close()

	s = reloadStore(t, path)
	if got, ok := s.ModelList(); !ok || !reflect.DeepEqual(got, claude) {
		t.Errorf("restored Claude list = %#v, %v", got, ok)
	}
	if got := s.CodexModels(); !reflect.DeepEqual(got, codexFamilies("gpt-6-sol")) {
		t.Errorf("restored Codex entries = %#v", got)
	}
}

// preCodexFile is a model_list.json as a daemon from before the Codex entries
// wrote it.
const preCodexFile = `{
  "models": [
    {
      "resolved_model": "claude-opus-4-7",
      "value": "opus",
      "display_name": "Opus",
      "effort_levels": [
        "low",
        "high"
      ],
      "supports_auto_mode": true
    },
    {
      "resolved_model": "claude-haiku-4-5",
      "value": "haiku",
      "display_name": "Haiku"
    }
  ],
  "dropped_models": 1
}
`

// A file written before this change loads, and with no Codex entries held it
// re-encodes to identical bytes: re-reporting it writes nothing.
func TestModelVocabularyStore_PreCodexFileReencodesIdentically(t *testing.T) {
	t.Parallel()
	path := storePath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(preCodexFile), 0o600); err != nil {
		t.Fatal(err)
	}
	s := reloadStore(t, path)
	list, ok := s.ModelList()
	if !ok || len(list.Models) != 2 || list.DroppedModels != 1 {
		t.Fatalf("ModelList() = %#v, %v", list, ok)
	}
	body, err := encodeModelVocabulary(list, s.CodexModels())
	if err != nil || string(body) != preCodexFile {
		t.Fatalf("re-encoded = %q, %v; want the original bytes", body, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Retain(list)
	s.Close()
	after, err := os.Stat(path)
	if err != nil || !after.ModTime().Equal(info.ModTime()) {
		t.Errorf("re-reporting the loaded list rewrote the file")
	}
}

// A Codex section breaking the contract is dropped on load; Claude's loads.
func TestModelVocabularyStore_LoadDropsABadCodexSection(t *testing.T) {
	t.Parallel()
	path := storePath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"models":[{"resolved_model":"r","value":"v","display_name":"d"}],"codex_models":[{"value":"sol","resolved_model":""}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	s := reloadStore(t, path)
	if got := s.CodexModels(); got != nil {
		t.Errorf("CodexModels() = %#v, want nil", got)
	}
	if _, ok := s.ModelList(); !ok {
		t.Error("Claude list dropped with the bad Codex section")
	}
}

// An empty Codex retention keeps what is held; CodexModels is a deep copy; a
// nil store answers none.
func TestModelVocabularyStore_CodexEdges(t *testing.T) {
	t.Parallel()
	s := newModelVocabularyStore(storePath(t))
	defer s.Close()
	s.RetainCodex(codexFamilies("gpt-6-sol"))
	s.RetainCodex(nil)
	got := s.CodexModels()
	if !reflect.DeepEqual(got, codexFamilies("gpt-6-sol")) {
		t.Fatalf("empty retention replaced the held entries: %#v", got)
	}
	got[0].EffortLevels[0] = "MUTATED"
	got[1].Value = "MUTATED"
	if again := s.CodexModels(); !reflect.DeepEqual(again, codexFamilies("gpt-6-sol")) {
		t.Errorf("CodexModels() is not a deep copy: %#v", again)
	}

	var nilStore *modelVocabularyStore
	nilStore.RetainCodex(codexFamilies("gpt-6-sol"))
	if got := nilStore.CodexModels(); got != nil {
		t.Errorf("nil store CodexModels() = %#v", got)
	}
}
