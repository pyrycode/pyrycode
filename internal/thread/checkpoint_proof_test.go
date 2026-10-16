package thread

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

func TestCheckpointProofSensitivity(t *testing.T) {
	item := Item{ID: 1, Content: json.RawMessage(`{}`)}
	for i := range reflect.TypeOf(item).NumField() {
		name := reflect.TypeOf(item).Field(i).Name
		t.Run(name, func(t *testing.T) {
			f, g := New("a"), New("a")
			f.items, g.items = []Item{item}, []Item{item}
			v := reflect.ValueOf(&g.items[0]).Elem().Field(i)
			switch v.Kind() {
			case reflect.Uint64:
				v.SetUint(v.Uint() + 1)
			case reflect.String:
				v.SetString("changed")
			case reflect.Bool:
				v.SetBool(!v.Bool())
			case reflect.Slice:
				v.SetBytes([]byte(`{ }`))
			default:
				t.Fatal("unhandled Item field", name)
			}
			p, q := testResumeProof{}, testResumeProof{}
			if p.update(t, f, true) == q.update(t, g, true) {
				t.Fatal("proof omitted", name)
			}
		})
	}
	for _, name := range []string{"published-false", "published-true", "visible-false", "visible-true", "touch-zero", "touch", "version", "shown", "suppressed", "unproven", "recipe"} {
		t.Run(name, func(t *testing.T) {
			f, g := New("a"), New("a")
			f.items, g.items = []Item{item}, []Item{item}
			switch name {
			case "published-false", "published-true":
				g.published[1] = name == "published-true"
			case "visible-false", "visible-true":
				g.publicVisible[1] = name == "visible-true"
			case "touch-zero", "touch":
				g.touches[1] = 0
				if name == "touch" {
					g.touches[1] = 2
				}
			case "version":
				g.version++
			case "shown":
				g.lastShown++
			case "suppressed":
				g.suppressed++
			case "unproven":
				g.unprovenThrough++
			case "recipe":
				g.recipes = map[uint64]contentRecipe{1: {agent: true}}
			}
			p, q := testResumeProof{}, testResumeProof{}
			if p.update(t, f, true) == q.update(t, g, true) {
				t.Fatal("proof omitted", name)
			}
		})
	}
}

func TestCheckpointProofReuse(t *testing.T) {
	entries := testFoldCorpus(151)
	f, _ := testReferenceFold(entries)
	p := testResumeProof{}
	p.update(t, f, true)
	for _, e := range entries {
		testFeed(t, f, e)
		got := p.update(t, f, false)
		fresh := testResumeProof{}
		if got != fresh.update(t, f, true) {
			t.Fatal("reused proof differs after entry", e.ID)
		}
	}
}

func TestCheckpointProofContentIdentity(t *testing.T) {
	seen := make(map[[32]byte]bool)
	for _, content := range []json.RawMessage{nil, {}, []byte(`"x"`), []byte(` "x"`), []byte(`"xx"`)} {
		f := New("a")
		f.items = []Item{{ID: 1, Content: content}}
		p := testResumeProof{}
		got := p.update(t, f, true)
		if seen[got] {
			t.Fatal("proof lost content presence, length or exact bytes")
		}
		seen[got] = true
	}
}

func TestCheckpointProofScratch(t *testing.T) {
	content := append([]byte(`{"text":"`), bytes.Repeat([]byte("x"), 64<<10)...)
	content = append(content, []byte(`"}`)...)
	f := New("a")
	f.items = []Item{{ID: 1, Content: content}}
	p := testResumeProof{}
	want := p.update(t, f, true)
	result := testing.Benchmark(func(b *testing.B) {
		for b.Loop() {
			if p.update(b, f, true) != want {
				b.Fatal("proof changed while reusing scratch")
			}
		}
	})
	t.Logf("proof scratch: %d B/op for %d content bytes", result.AllocedBytesPerOp(), len(content))
	// A warmed proof must not allocate another payload-sized encoding per row.
	if got := result.AllocedBytesPerOp(); got >= int64(len(content)) {
		t.Fatalf("proof scratch allocated %d bytes for %d content bytes", got, len(content))
	}
}

func TestCheckpointProofRestorationReuse(t *testing.T) {
	// Successive restorations may shrink or remove private facts entirely.
	// A full proof must discard every signature from the previous restoration.
	reused := testResumeProof{}
	for _, content := range []json.RawMessage{[]byte(`{ "text": "x" }`), nil, {}, []byte(`{"text":"x"}`)} {
		for _, populated := range []bool{true, false} {
			f := New("a")
			f.items = []Item{{ID: 1, Content: content}}
			if populated {
				f.items = append(f.items, Item{ID: 2, Content: json.RawMessage(`{}`)})
				f.published[2], f.publicVisible[2], f.touches[2] = true, true, 3
			}
			fresh := testResumeProof{}
			want := fresh.update(t, f, true)
			if reused.update(t, f, true) != want {
				t.Fatal("full reused proof retained a prior restoration")
			}
			if len(reused.rows) != len(f.items) {
				t.Fatal("full proof retained removed rows")
			}
		}
	}
}

func TestCheckpointJSONReuse(t *testing.T) {
	scratch := testResumeJSON{}
	states := []CheckpointState{
		{Published: map[uint64]bool{1: false, 2: true}, Touches: map[uint64]uint64{2: 3}, ItemIDs: []uint64{1, 2}},
		{},
		{Published: map[uint64]bool{}, Touches: map[uint64]uint64{}, ItemIDs: []uint64{}},
	}
	var retained []CheckpointState
	for _, state := range states {
		var decoded CheckpointState
		scratch.roundTrip(t, state, &decoded)
		retained = append(retained, decoded)
	}
	if !reflect.DeepEqual(retained, states) {
		t.Fatal("encoding reuse contaminated prior decoded facts or nil/empty values")
	}
	for _, rows := range [][]CheckpointItem{nil, {}, {{Metadata: checkpointMetadata{ID: 1}}}, nil} {
		var decoded []CheckpointItem
		scratch.roundTrip(t, rows, &decoded)
		if !reflect.DeepEqual(decoded, testCheckpointJSON(t, rows)) {
			t.Fatal("reused encoding differs from fresh JSON round trip")
		}
	}
}

func TestCheckpointWorkerPreparation(t *testing.T) {
	entries := testFoldCorpus(151)
	f, source := testReferenceFold(entries)
	proof := testResumeProof{}
	want := make([][32]byte, len(entries)+1)
	want[0] = proof.update(t, f, true)
	for i, entry := range entries {
		testFeed(t, f, entry)
		want[i+1] = proof.update(t, f, false)
	}
	baseFold, _ := testReferenceFold(entries)
	testFeed(t, baseFold, entries[0])
	base, rows, err := baseFold.ExportCheckpoint()
	if err != nil {
		t.Fatal(err)
	}
	worker := testResumeWorker{source: source}
	var preparation *Fold
	for _, cut := range []int{0, 1, 20, 100, 151} {
		worker.check(t, entries, cut, 1, base, rows, want)
		if cut == 0 {
			if worker.prefix != nil {
				t.Fatal("empty cut initialized the nonempty preparation")
			}
			continue
		}
		if preparation != nil && worker.prefix != preparation {
			t.Fatal("worker rebuilt its preparation fold")
		}
		preparation = worker.prefix
		if worker.prefix.Version() != uint64(cut) {
			t.Fatal("restored suffix mutated preparation")
		}
		fresh, _ := testReferenceFold(entries)
		testFeed(t, fresh, entries[:cut]...)
		a, ar, err := worker.prefix.ExportCheckpoint()
		if err != nil {
			t.Fatal(err)
		}
		b, br, err := fresh.ExportCheckpoint()
		if err != nil || !reflect.DeepEqual(a, b) || !reflect.DeepEqual(ar, br) {
			t.Fatal("advanced preparation differs from fresh prefix", err)
		}
	}
}
