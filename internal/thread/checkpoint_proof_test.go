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
