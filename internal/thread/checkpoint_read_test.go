package thread

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
)

func testCheckpointFrame(t testing.TB, row CheckpointItem) []byte {
	t.Helper()
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	header := make([]byte, 8)
	binary.BigEndian.PutUint32(header, uint32(len(raw)))
	binary.BigEndian.PutUint32(header[4:], crc32.Checksum(raw, crc32.MakeTable(crc32.Castagnoli)))
	return append(header, raw...)
}

func testCheckpointFiles(t testing.TB, f *Fold, rows []CheckpointItem) (*Store, string, checkpointFileState, []byte) {
	t.Helper()
	s := NewStore(history.New(t.TempDir()))
	dir, err := s.history.EnsureLogDir(testStoreA)
	if err != nil {
		t.Fatal(err)
	}
	d, _, err := f.ExportCheckpoint()
	if err != nil {
		t.Fatal(err)
	}
	d.ConversationID = string(testStoreA)
	var index []byte
	for _, row := range rows {
		index = append(index, testCheckpointFrame(t, row)...)
	}
	checksum := ""
	if d.Version > 0 {
		checksum = strings.Repeat("a", 64)
	}
	state := checkpointFileState{Schema: checkpointFileSchema, Generation: "thread-index-" + strings.Repeat("b", 32) + ".bin", ValidLength: int64(len(index)), Certificate: cacheRecord{Schema: cacheSchema, Rules: foldingRules, Epoch: strings.Repeat("c", 32), Version: d.Version, LastShownVersion: d.LastShown, Checksum: checksum, Clean: true}, Fold: d}
	testWriteCheckpointState(t, dir, state)
	if err := os.WriteFile(filepath.Join(dir, state.Generation), index, 0600); err != nil {
		t.Fatal(err)
	}
	return s, dir, state, index
}

func testWriteCheckpointState(t testing.TB, dir string, state any) {
	t.Helper()
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, checkpointStateName), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

type testCheckpointReadSource struct{ t testing.TB }

func (s testCheckpointReadSource) Lookup([]uint64) ([]history.Entry, error) {
	s.t.Fatal("checkpoint reader looked up history")
	return nil, nil
}

type testCheckpointErrorReader struct{ err error }

func (r testCheckpointErrorReader) Read([]byte) (int, error) { return 0, r.err }

func TestReadCheckpointItemsReadErrors(t *testing.T) {
	t.Parallel()
	f, _ := testReferenceFold([]history.Entry{testMessage(1)})
	testFeed(t, f, testMessage(1))
	state, rows, err := f.ExportCheckpoint()
	if err != nil {
		t.Fatal(err)
	}
	frame := testCheckpointFrame(t, rows[0])
	index := append(bytes.Clone(frame), frame...)
	for _, fault := range []struct {
		name string
		err  error
		want error
	}{
		{"eio", syscall.EIO, ErrPersistence},
		{"other-io", errors.New("read failure"), ErrPersistence},
		{"eof", io.EOF, errCacheAbsent},
		{"unexpected-eof", io.ErrUnexpectedEOF, errCacheAbsent},
		{"wrapped-eof", fmt.Errorf("read: %w", io.EOF), errCacheAbsent},
	} {
		for _, cut := range []int{0, 3, checkpointFrameHeader, checkpointFrameHeader + 1} {
			t.Run(fmt.Sprintf("%s/offset-%d", fault.name, cut), func(t *testing.T) {
				// Fail in the second frame so no previously decoded row may escape.
				reader := io.MultiReader(bytes.NewReader(index[:len(frame)+cut]), testCheckpointErrorReader{fault.err})
				got, err := readCheckpointItems(reader, int64(len(index)), state.ItemIDs)
				if !errors.Is(err, fault.want) || got != nil {
					t.Fatalf("rows = %v, error = %v; want nil, %v", got, err, fault.want)
				}
			})
		}
	}
	t.Run("crc", func(t *testing.T) {
		index[len(frame)+7] ^= 1
		got, err := readCheckpointItems(bytes.NewReader(index), int64(len(index)), state.ItemIDs)
		if !errors.Is(err, errCacheAbsent) || got != nil {
			t.Fatalf("rows = %v, error = %v; want nil, %v", got, err, errCacheAbsent)
		}
	})
}

func TestReadCheckpointResume(t *testing.T) {
	t.Parallel()
	histories := [][]history.Entry{testFoldCorpus(151), testReferenceSendEntries(), {
		testChild(1, "assistant_delta", "missing", `,"text":"hidden"`),
		testMain(2, "tool_use", `,"tool_use_id":"missing","name":"Agent"`),
		testChild(3, "assistant_delta", "missing", `,"text":"late"`),
	}}
	comparisons := 0
	for family, entries := range histories {
		for i := range entries {
			entries[i].Payload = bytes.ReplaceAll(entries[i].Payload, []byte(`"conversation_id":"a"`), []byte(`"conversation_id":"`+string(testStoreA)+`"`))
		}
		for _, cut := range []int{0, 1, (len(entries) + 1) / 2, len(entries)} {
			t.Run(fmt.Sprintf("%d/cut-%d", family, cut), func(t *testing.T) {
				baseline, _ := testReferenceFold(entries)
				prefix, source := testReferenceFold(entries)
				baseline.conversationID, prefix.conversationID = string(testStoreA), string(testStoreA)
				var records []CheckpointItem
				// Append complete snapshots in reverse order. Changed IDs repeat,
				// and physical order cannot stand in for manifest order.
				for _, e := range entries[:cut] {
					testFeed(t, prefix, e)
					_, rows, err := prefix.ExportCheckpoint()
					if err != nil {
						t.Fatal(err)
					}
					for i := len(rows) - 1; i >= 0; i-- {
						records = append(records, rows[i])
					}
				}
				s, dir, state, index := testCheckpointFiles(t, prefix, records)
				// Neither a complete newer record nor a torn frame is committed.
				if len(records) > 0 {
					index = append(index, testCheckpointFrame(t, records[0])...)
				}
				index = append(index, 0xff, 0xff, 0xff)
				if err := os.WriteFile(filepath.Join(dir, state.Generation), index, 0600); err != nil {
					t.Fatal(err)
				}
				for _, clean := range []bool{true, false} {
					state.Certificate.Clean = clean
					testWriteCheckpointState(t, dir, state)
					r, saved, err := s.readCheckpoint(testStoreA, testCheckpointReadSource{t})
					if err != nil || r == nil || saved.Certificate.Clean != clean {
						t.Fatalf("restore: %v", err)
					}
					r.source = source
					testFeed(t, baseline, entries[:cut]...)
					p, q := testResumeProof{}, testResumeProof{}
					if p.update(t, baseline, true) != q.update(t, r, true) {
						t.Fatal("immediate mismatch")
					}
					comparisons++
					for _, e := range entries[cut:] {
						testFeed(t, baseline, e)
						testFeed(t, r, e)
						if p.update(t, baseline, false) != q.update(t, r, false) {
							t.Fatalf("suffix mismatch at %d", e.ID)
						}
						comparisons++
					}
					baseline, _ = testReferenceFold(entries)
					baseline.conversationID = string(testStoreA)
				}
				if got, err := os.ReadFile(filepath.Join(dir, state.Generation)); err != nil || !bytes.Equal(got, index) {
					t.Fatal("reader modified index", err)
				}
			})
		}
	}
	t.Logf("%d immediate/suffix comparisons", comparisons)
}

func TestReadCheckpointInvalid(t *testing.T) {
	t.Parallel()
	mutations := map[string]func(map[string]any){
		"persisted-schema":   func(m map[string]any) { m["schema"] = 999 },
		"export-schema":      func(m map[string]any) { m["fold"].(map[string]any)["schema"] = 999 },
		"export-rules":       func(m map[string]any) { m["fold"].(map[string]any)["rules"] = 999 },
		"certificate-schema": func(m map[string]any) { m["certificate"].(map[string]any)["Schema"] = 999 },
		"certificate-rules":  func(m map[string]any) { m["certificate"].(map[string]any)["Rules"] = 999 },
		"owner":              func(m map[string]any) { m["fold"].(map[string]any)["conversation_id"] = string(testStoreB) },
		"version":            func(m map[string]any) { m["certificate"].(map[string]any)["Version"] = 2 },
		"watermark":          func(m map[string]any) { m["certificate"].(map[string]any)["LastShownVersion"] = 0 },
		"checksum":           func(m map[string]any) { m["certificate"].(map[string]any)["Checksum"] = "bad" },
		"epoch":              func(m map[string]any) { m["certificate"].(map[string]any)["Epoch"] = "bad" },
		"length-negative":    func(m map[string]any) { m["valid_length"] = -1 },
		"length-huge":        func(m map[string]any) { m["valid_length"] = int64(1 << 62) },
		"missing-row":        func(m map[string]any) { m["fold"].(map[string]any)["item_ids"] = []int{1, 2} },
		"extra-row":          func(m map[string]any) { m["fold"].(map[string]any)["item_ids"] = nil },
		"duplicate-row":      func(m map[string]any) { m["fold"].(map[string]any)["item_ids"] = []int{1, 1} },
		"invalid-graph":      func(m map[string]any) { m["fold"].(map[string]any)["open_texts"] = []int{999} },
	}
	missingEvidence := make(map[string]func(map[string]any))
	for _, section := range []struct {
		name   string
		fields []string
	}{{"", []string{"valid_length", "fold", "certificate"}}, {"fold", []string{"conversation_id", "version", "last_shown"}}, {"certificate", []string{"Epoch", "Version", "LastShownVersion", "Checksum", "Clean"}}} {
		for _, field := range section.fields {
			for _, null := range []bool{false, true} {
				name := fmt.Sprintf("%s/%s/null-%v", section.name, field, null)
				missingEvidence[name] = func(m map[string]any) {
					if section.name != "" {
						m = m[section.name].(map[string]any)
					}
					if null {
						m[field] = nil
					} else {
						delete(m, field)
					}
				}
				mutations[name] = missingEvidence[name]
			}
		}
	}
	for _, name := range []string{"../thread-index-" + strings.Repeat("b", 32) + ".bin", "/tmp/thread-index-" + strings.Repeat("b", 32) + ".bin", "thread-index-" + strings.Repeat("B", 32) + ".bin", cacheName, "thread-index-short.bin"} {
		mutations["generation/"+name] = func(m map[string]any) { m["generation"] = name }
	}
	for _, empty := range []bool{false, true} {
		cases := mutations
		var entries []history.Entry
		if empty {
			cases = missingEvidence
		} else {
			entries = []history.Entry{testMessage(1)}
		}
		for name, mutate := range cases {
			t.Run(fmt.Sprintf("empty-%t/%s", empty, name), func(t *testing.T) {
				f, _ := testReferenceFold(entries)
				testFeed(t, f, entries...)
				_, rows, _ := f.ExportCheckpoint()
				s, dir, state, _ := testCheckpointFiles(t, f, rows)
				raw, _ := json.Marshal(state)
				var m map[string]any
				if err := json.Unmarshal(raw, &m); err != nil {
					t.Fatal(err)
				}
				mutate(m)
				testWriteCheckpointState(t, dir, m)
				if r, _, err := s.readCheckpoint(testStoreA, testCheckpointReadSource{t}); err == nil || r != nil {
					t.Fatal("invalid state restored", err)
				}
			})
		}
	}
	for _, fault := range []string{"missing-state", "invalid-state", "trailing-state", "missing-index", "short-file", "short-header", "short-payload", "huge-frame", "zero-frame", "crc", "json", "null-row", "invalid-row", "committed-torn-tail", "committed-crc-tail"} {
		t.Run(fault, func(t *testing.T) {
			f, _ := testReferenceFold([]history.Entry{testMessage(1)})
			testFeed(t, f, testMessage(1))
			_, rows, _ := f.ExportCheckpoint()
			s, dir, state, index := testCheckpointFiles(t, f, rows)
			path := filepath.Join(dir, state.Generation)
			switch fault {
			case "missing-state":
				if err := os.Remove(filepath.Join(dir, checkpointStateName)); err != nil {
					t.Fatal(err)
				}
			case "invalid-state", "trailing-state":
				raw := []byte("{")
				if fault == "trailing-state" {
					raw, _ = json.Marshal(state)
					raw = append(raw, []byte(" {}")...)
				}
				if err := os.WriteFile(filepath.Join(dir, checkpointStateName), raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "missing-index":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			default:
				switch fault {
				case "short-file":
					index = index[:len(index)-1]
				case "short-header":
					state.ValidLength = 7
				case "short-payload":
					state.ValidLength--
				case "huge-frame":
					binary.BigEndian.PutUint32(index, ^uint32(0))
				case "zero-frame":
					binary.BigEndian.PutUint32(index, 0)
				case "crc":
					index[7] ^= 1
				case "json", "null-row":
					raw := []byte("broken")
					if fault == "null-row" {
						raw = []byte("null")
					}
					index = make([]byte, 8)
					binary.BigEndian.PutUint32(index, uint32(len(raw)))
					binary.BigEndian.PutUint32(index[4:], crc32.Checksum(raw, crc32.MakeTable(crc32.Castagnoli)))
					index = append(index, raw...)
					state.ValidLength = int64(len(index))
				case "invalid-row":
					rows[0].Recipe.Creating = 0
					index = testCheckpointFrame(t, rows[0])
					state.ValidLength = int64(len(index))
				case "committed-torn-tail", "committed-crc-tail":
					tail := testCheckpointFrame(t, rows[0])
					tail[7] ^= 1
					if fault == "committed-torn-tail" {
						tail = tail[:3]
					}
					index = append(index, tail...)
					state.ValidLength = int64(len(index))
				}
				testWriteCheckpointState(t, dir, state)
				if err := os.WriteFile(path, index, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if r, _, err := s.readCheckpoint(testStoreA, testCheckpointReadSource{t}); err == nil || r != nil {
				t.Fatal("invalid checkpoint restored", err)
			}
		})
	}
}

func TestReadCheckpointSecurity(t *testing.T) {
	t.Parallel()
	for _, fault := range []string{"state-symlink", "state-directory", "state-fifo", "index-symlink", "index-directory", "index-fifo", "outside", "sibling", "generation-escape"} {
		t.Run(fault, func(t *testing.T) {
			f, _ := testReferenceFold(nil)
			s, dir, state, _ := testCheckpointFiles(t, f, nil)
			entries := testStoreAppend(t, s.history, testStoreA, testMessage(1))
			outside := t.TempDir()
			marker := filepath.Join(outside, "unchanged")
			if err := os.WriteFile(marker, []byte("outside"), 0600); err != nil {
				t.Fatal(err)
			}
			segments, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
			before, err := os.ReadFile(segments[0])
			if err != nil {
				t.Fatal(err)
			}
			wantErr := ErrPersistence
			if fault == "generation-escape" {
				state.Generation = marker
				testWriteCheckpointState(t, dir, state)
				wantErr = errCacheAbsent
			} else if fault == "outside" || fault == "sibling" {
				target := outside
				if fault == "sibling" {
					target, err = s.history.EnsureLogDir(testStoreB)
					if err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Rename(dir, dir+"-saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, dir); err != nil {
					t.Fatal(err)
				}
			} else {
				name := checkpointStateName
				if strings.HasPrefix(fault, "index-") {
					name = state.Generation
				}
				path := filepath.Join(dir, name)
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				switch {
				case strings.HasSuffix(fault, "symlink"):
					err = os.Symlink(marker, path)
				case strings.HasSuffix(fault, "directory"):
					err = os.Mkdir(path, 0700)
				default:
					err = syscall.Mkfifo(path, 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if r, _, err := s.readCheckpoint(testStoreA, testCheckpointReadSource{t}); err != wantErr || r != nil {
				t.Fatal("unsafe path admitted", err)
			}
			if fault == "outside" || fault == "sibling" {
				dir += "-saved"
			}
			got, err := os.ReadFile(filepath.Join(dir, filepath.Base(segments[0])))
			if err != nil || !bytes.Equal(got, before) || len(entries) != 1 {
				t.Fatal("history changed", err)
			}
			if got, err := os.ReadFile(marker); err != nil || string(got) != "outside" {
				t.Fatal("outside changed", err)
			}
		})
	}
}

func TestCheckpointLeavesSurviveLegacyCleanup(t *testing.T) {
	t.Parallel()
	f, _ := testReferenceFold(nil)
	s, dir, state, index := testCheckpointFiles(t, f, nil)
	before, err := os.ReadFile(filepath.Join(dir, checkpointStateName))
	if err != nil {
		t.Fatal(err)
	}
	legacy := []string{legacyCacheName, recoveryName, "thread-run-" + strings.Repeat("d", 32) + ".json"}
	for _, name := range legacy {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("legacy"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.removeLegacy(testStoreA); err != nil {
		t.Fatal(err)
	}
	for _, name := range legacy {
		if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatal("legacy retained", err)
		}
	}
	for name, want := range map[string][]byte{checkpointStateName: before, state.Generation: index} {
		if !cacheLeaf(name) {
			t.Fatal("checkpoint leaf refused")
		}
		if got, err := os.ReadFile(filepath.Join(dir, name)); err != nil || !reflect.DeepEqual(got, append([]byte{}, want...)) {
			t.Fatal("checkpoint changed", name, err)
		}
	}
}
