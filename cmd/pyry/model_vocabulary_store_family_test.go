package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// pyryboxSavedModelList is pyrybox's model_list.json as Claude Code 2.1.289 left
// it on 2026-10-05, effort levels shortened: five family rows, five pinned rows,
// and two more pinned rows the entry cap cut.
const pyryboxSavedModelList = `{
  "models": [
    {"resolved_model": "claude-opus-5-5", "value": "default", "display_name": "Default (recommended)", "effort_levels": ["low", "max"], "supports_auto_mode": true},
    {"resolved_model": "claude-opus-5-5", "value": "opus", "display_name": "Opus 5.5", "effort_levels": ["low", "max"], "supports_auto_mode": true},
    {"resolved_model": "claude-fable-5-1", "value": "fable", "display_name": "Fable 5.1", "effort_levels": ["low", "max"], "supports_auto_mode": true},
    {"resolved_model": "claude-sonnet-5-5", "value": "sonnet", "display_name": "Sonnet 5.5", "effort_levels": ["low", "max"], "supports_auto_mode": true},
    {"resolved_model": "claude-haiku-4-5-20251001", "value": "haiku", "display_name": "Haiku 4.5"},
    {"resolved_model": "claude-sonnet-5", "value": "claude-sonnet-5", "display_name": "Sonnet 5", "effort_levels": ["low", "max"], "supports_auto_mode": true},
    {"resolved_model": "claude-opus-5", "value": "claude-opus-5", "display_name": "Opus 5", "effort_levels": ["low", "max"], "supports_auto_mode": true},
    {"resolved_model": "claude-fable-5", "value": "claude-fable-5", "display_name": "Fable 5", "effort_levels": ["low", "max"], "supports_auto_mode": true},
    {"resolved_model": "claude-opus-4-8", "value": "claude-opus-4-8", "display_name": "Opus 4.8", "effort_levels": ["low", "max"], "supports_auto_mode": true},
    {"resolved_model": "claude-opus-4-7", "value": "claude-opus-4-7", "display_name": "Opus 4.7", "effort_levels": ["low", "max"], "supports_auto_mode": true}
  ],
  "dropped_models": 2
}`

// TestModelVocabularyStore_LoadReducesASavedListToFamilies: a file saved before
// the parser reduced claude's list is reduced on load, so a cold daemon offers
// one row per family before any child has reported. The count of rows the cap
// cut is carried as loaded: the store cannot know what those rows were.
func TestModelVocabularyStore_LoadReducesASavedListToFamilies(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "model_list.json")
	if err := os.WriteFile(path, []byte(pyryboxSavedModelList), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newModelVocabularyStore(path)
	s.Load()
	list, ok := s.ModelList()
	if !ok {
		t.Fatal("ModelList() reported no list after Load")
	}
	var values []string
	for _, m := range list.Models {
		values = append(values, m.Value)
	}
	if want := []string{"default", "opus", "fable", "sonnet", "haiku"}; !slices.Equal(values, want) {
		t.Errorf("loaded values = %q, want %q", values, want)
	}
	if list.DroppedModels != 2 {
		t.Errorf("DroppedModels = %d, want the file's 2", list.DroppedModels)
	}
}

// TestModelVocabularyStore_LoadKeepsACutValue: a row whose value was cut by the
// text cap is not read as a pinned id, so it is never reduced away on the strength
// of a prefix.
func TestModelVocabularyStore_LoadKeepsACutValue(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "model_list.json")
	body := `{"models":[
		{"resolved_model":"claude-opus-5-5","value":"opus","display_name":"Opus 5.5"},
		{"resolved_model":"claude-opus-4-7","value":"claude-opus-4-7","display_name":"Opus 4.7","truncated_fields":["value"]}
	],"dropped_models":0}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newModelVocabularyStore(path)
	s.Load()
	list, ok := s.ModelList()
	if !ok || len(list.Models) != 2 {
		t.Fatalf("loaded %d rows (ok %v), want both: a cut value is kept as it is", len(list.Models), ok)
	}
}
