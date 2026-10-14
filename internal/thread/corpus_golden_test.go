package thread

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"reflect"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
)

const testFoldBaselineCommit = "a920b90cc6bfd2f1fb8461e9f85ce6f19934afa6"

// Explicit generation: go test ./internal/thread -run '^TestFoldBaselines$'
// -args -write-fold-baselines. Ordinary tests only read committed expectations.
// Canonical JSON uses sorted encoding/json map keys, UseNumber (no float64
// rounding), and default JSON string escaping. Every Item field is included.
// Raw Content hashes concatenate uint64 big-endian ID, uint64 byte length,
// then exact Content bytes, in Items creation order. No output is normalized.
var testWriteFoldBaselines = flag.Bool("write-fold-baselines", false, "explicitly replace fold digest expectations")

type testFoldCheckpoint struct {
	Version, LastShownVersion uint64
	Items                     int
	JSON, Content             string
}

// Every checkpoint field is required; explicit numeric zeros remain valid.
func (c *testFoldCheckpoint) UnmarshalJSON(raw []byte) error {
	var fields struct {
		Version, LastShownVersion *uint64
		Items                     *int
		JSON, Content             *string
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	if fields.Version == nil || fields.LastShownVersion == nil || fields.Items == nil || fields.JSON == nil || fields.Content == nil {
		return fmt.Errorf("incomplete fold checkpoint")
	}
	*c = testFoldCheckpoint{*fields.Version, *fields.LastShownVersion, *fields.Items, *fields.JSON, *fields.Content}
	return nil
}

type testFoldGolden struct {
	Commit, Identity, Input, History string
	Count                            int
	Checkpoints                      []testFoldCheckpoint
}

func testJSON(t testing.TB, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var value any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&value); err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func testSHA(raw []byte) string { return fmt.Sprintf("%x", sha256.Sum256(raw)) }

func testFoldDigest(t testing.TB, f *Fold) testFoldCheckpoint {
	t.Helper()
	o := f.Observe()
	h := sha256.New()
	for _, item := range o.Items {
		var frame [16]byte
		binary.BigEndian.PutUint64(frame[:8], item.ID)
		binary.BigEndian.PutUint64(frame[8:], uint64(len(item.Content)))
		h.Write(frame[:])
		h.Write(item.Content)
	}
	return testFoldCheckpoint{o.Version, o.LastShownVersion, len(o.Items), testSHA(testJSON(t, o.Items)), fmt.Sprintf("%x", h.Sum(nil))}
}

func testFoldGoldenCheck(t *testing.T, path, conv, identity string, input []byte, entries []history.Entry, versions []int) {
	t.Helper()
	got := testFoldGolden{Commit: testFoldBaselineCommit, Identity: identity, Input: testSHA(input), History: testSHA(testJSON(t, entries)), Count: len(entries)}
	var want testFoldGolden
	if !*testWriteFoldBaselines {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &want); err != nil {
			t.Fatal(err)
		}
		header := want
		header.Checkpoints = nil
		if !reflect.DeepEqual(header, got) || len(want.Checkpoints) != len(versions) {
			t.Fatal("incomplete or changed baseline metadata", path)
		}
	}
	for i, version := range versions {
		if version < 1 || version > len(entries) {
			t.Fatal("invalid checkpoint", version)
		}
		var expected testFoldCheckpoint
		if !*testWriteFoldBaselines {
			expected = want.Checkpoints[i]
		}
		modes := 3
		if *testWriteFoldBaselines {
			modes = 1
		}
		for mode := 0; mode < modes; mode++ {
			f := New(conv)
			for start, step := 0, 0; start < version; step++ {
				size := version
				if mode == 1 {
					size = []int{7, 31, 113}[step%3]
				}
				if mode == 2 {
					size = 1
				}
				end := min(start+size, version)
				if err := f.Feed(entries[start:end]); err != nil {
					t.Fatal(err)
				}
				start = end
			}
			digest := testFoldDigest(t, f)
			if *testWriteFoldBaselines {
				expected = digest
				got.Checkpoints = append(got.Checkpoints, digest)
			}
			if digest != expected {
				t.Fatalf("%s version %d mode %d: got %+v want %+v", path, version, mode, digest, expected)
			}
		}
	}
	if *testWriteFoldBaselines {
		var out bytes.Buffer
		header := got
		header.Checkpoints = nil
		raw, err := json.Marshal(header)
		if err != nil {
			t.Fatal(err)
		}
		out.Write(raw[:len(raw)-len(`"Checkpoints":null}`)])
		out.WriteString("\"Checkpoints\":[\n")
		for i, row := range got.Checkpoints {
			raw, err := json.Marshal(row)
			if err != nil {
				t.Fatal(err)
			}
			if i > 0 {
				out.WriteString(",\n")
			}
			out.Write(raw)
		}
		out.WriteString("\n]}\n")
		if err := os.WriteFile(path, out.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFoldBaselines(t *testing.T) {
	t.Run("generated", func(t *testing.T) {
		entries := testFoldCorpus(30000)
		testFoldGoldenCheck(t, "testdata/corpus_golden.json", "a", testCorpusIdentity, []byte(testCorpusIdentity), entries, []int{6, 10, 11, 12, 14, 15, 18, 21, 22, 23, 28, 33, 100, 1000, 30000})
	})
	t.Run("shadow", func(t *testing.T) {
		raw, err := os.ReadFile("../e2e/realclaude/testdata/thread_shadow_history.json")
		if err != nil {
			t.Fatal(err)
		}
		var capture struct {
			Conversation string
			Entries      []history.Entry
		}
		if err := json.Unmarshal(raw, &capture); err != nil {
			t.Fatal(err)
		}
		testFoldGoldenCheck(t, "testdata/shadow_golden.json", capture.Conversation, "raw-shadow-v1", raw, capture.Entries, []int{14, 28, 30, 31, 35, 42, 47, 48, 60, 85, 86})
	})
}

func TestFoldDigestCorruption(t *testing.T) {
	f := New("a")
	testFeed(t, f, testFoldCorpus(2)...)
	before := testFoldDigest(t, f)
	f.items[0].Content = append([]byte(" "), f.items[0].Content...)
	after := testFoldDigest(t, f)
	if before.JSON != after.JSON || before.Content == after.Content {
		t.Fatal("raw byte corruption undetected")
	}
	f.items[0].Content = []byte(`{"text":"changed","exact":9007199254740993}`)
	if changed := testFoldDigest(t, f); changed.JSON == before.JSON || changed.Content == before.Content {
		t.Fatal("content corruption undetected")
	}
	if !bytes.Contains(testJSON(t, json.RawMessage(`{"n":9007199254740993}`)), []byte("9007199254740993")) {
		t.Fatal("number rounded")
	}
}

func TestFoldCheckpointRequiredFields(t *testing.T) {
	want := testFoldGolden{Checkpoints: []testFoldCheckpoint{{1, 0, 0, "json", "content"}}}
	var got testFoldGolden
	if err := json.Unmarshal(testJSON(t, want), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("explicit zero checkpoint fields changed: got %+v want %+v", got, want)
	}
	for _, field := range []string{"Version", "LastShownVersion", "Items", "JSON", "Content"} {
		mutations := map[string]json.RawMessage{"omitted": nil, "null": []byte("null")}
		if field == "Version" || field == "LastShownVersion" || field == "Items" {
			mutations["string"] = []byte(`"0"`)
			mutations["boolean"] = []byte("false")
			mutations["fraction"] = []byte("0.5")
			mutations["object"] = []byte("{}")
		}
		for name, value := range mutations {
			t.Run(field+"/"+name, func(t *testing.T) {
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(testJSON(t, want.Checkpoints[0]), &fields); err != nil {
					t.Fatal(err)
				}
				delete(fields, field)
				if value != nil {
					fields[field] = value
				}
				raw := testJSON(t, map[string]any{"Checkpoints": []any{fields}})
				var invalid testFoldGolden
				if err := json.Unmarshal(raw, &invalid); err == nil {
					t.Fatalf("accepted checkpoint with %s %s: %s", name, field, raw)
				}
			})
		}
	}
}
