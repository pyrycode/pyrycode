package thread

import (
	"encoding/json"
	"testing"
)

// BenchmarkCheckpointResumePhases separates fixture/fold preparation from the
// work repeated at every cut. Use -benchtime=1x for complete 30,000-entry phases.
func BenchmarkCheckpointResumePhases(b *testing.B) {
	b.Run("fixture", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if len(testFoldCorpus(30000)) != 30000 {
				b.Fatal("fixture count")
			}
		}
	})
	entries := testFoldCorpus(30000)
	f, source := testReferenceFold(entries)
	if err := f.Feed(entries); err != nil {
		b.Fatal(err)
	}
	d, rows, err := f.ExportCheckpoint()
	if err != nil {
		b.Fatal(err)
	}
	b.Run("feed", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			fold, _ := testReferenceFold(entries)
			if err := fold.Feed(entries); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("export", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, _, err := f.ExportCheckpoint(); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("json", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			stateJSON, err := json.Marshal(d)
			if err != nil {
				b.Fatal(err)
			}
			rowsJSON, err := json.Marshal(rows)
			if err != nil {
				b.Fatal(err)
			}
			var state CheckpointState
			var records []CheckpointItem
			if err := json.Unmarshal(stateJSON, &state); err != nil {
				b.Fatal(err)
			}
			if err := json.Unmarshal(rowsJSON, &records); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("import", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := ImportCheckpoint("a", source, d, rows); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("render", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			// Include hidden and delivery-suppressed rows just as the proof does.
			if _, err := f.render(f.items); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("proof", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			p := testResumeProof{}
			p.update(b, f, true)
		}
	})
}
