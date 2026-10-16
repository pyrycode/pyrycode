package thread

import (
	"errors"
	"reflect"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
)

func TestCheckpointImport(t *testing.T) {
	entries := testFoldCorpus(151)
	f, source := testReferenceFold(entries)
	testFeed(t, f, entries...)
	d, rows, err := f.ExportCheckpoint()
	if err != nil {
		t.Fatal(err)
	}
	lookup := source.lookup
	source.lookup = func([]uint64) ([]history.Entry, error) { t.Fatal("import looked up history"); return nil, nil }
	r, err := ImportCheckpoint("a", source, testCheckpointJSON(t, d), testCheckpointJSON(t, rows))
	if err != nil {
		t.Fatal(err)
	}
	source.lookup = lookup
	got, gotRows, err := r.ExportCheckpoint()
	if err != nil || !reflect.DeepEqual(d, got) || !reflect.DeepEqual(rows, gotRows) {
		t.Fatal("restored state differs", err)
	}
	left, err := f.Render()
	if err != nil {
		t.Fatal(err)
	}
	right, err := r.Render()
	if err != nil || !reflect.DeepEqual(left, right) {
		t.Fatal("restored render differs", err)
	}
	d.Schema++
	r, err = ImportCheckpoint("a", source, d, rows)
	var incompatible *CheckpointCompatibilityError
	if r != nil || !errors.As(err, &incompatible) {
		t.Fatal("missing typed incompatibility", err)
	}
}
