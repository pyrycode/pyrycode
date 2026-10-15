package thread

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
)

// TestReferenceDiskHeap isolates GC accounting from all parallel package tests.
// The same file runs unchanged on the pre-family revision for comparison.
func TestReferenceDiskHeap(t *testing.T) {
	if os.Getenv("PYRY_THREAD_HEAP_CHILD") == "" {
		binary, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(binary, "-test.run=^TestReferenceDiskHeap$", "-test.v")
		cmd.Env = append(os.Environ(), "PYRY_THREAD_HEAP_CHILD=1")
		out, err := cmd.CombinedOutput()
		t.Logf("%s", out)
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	path := filepath.Join(t.TempDir(), "history.jsonl")
	const count, padding, chunkSize = 10000, 8192, 100
	logBytes := testWriteHeapHistory(t, path, count, padding)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var offsets []int64
	source := newRecentSource(func(ids []uint64) ([]history.Entry, error) {
		out := make([]history.Entry, 0, len(ids))
		for _, id := range ids {
			if id == 0 || int(id) >= len(offsets) {
				return nil, fmt.Errorf("missing fixture entry")
			}
			raw := make([]byte, offsets[id]-offsets[id-1])
			if _, err := file.ReadAt(raw, offsets[id-1]); err != nil {
				return nil, err
			}
			var entry history.Entry
			if err := json.Unmarshal(raw, &entry); err != nil {
				return nil, err
			}
			out = append(out, entry)
		}
		return out, nil
	})
	f := newReferenceFold("a", source)
	reader := bufio.NewReader(file)
	offsets = append(offsets, 0)
	var chunk []history.Entry
	for {
		raw, err := reader.ReadBytes('\n')
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		var entry history.Entry
		if err := json.Unmarshal(raw, &entry); err != nil {
			t.Fatal(err)
		}
		offsets = append(offsets, offsets[len(offsets)-1]+int64(len(raw)))
		chunk = append(chunk, entry)
		if len(chunk) == chunkSize {
			source.chunk = chunk
			if err := f.Feed(chunk); err != nil {
				t.Fatal(err)
			}
			source.remember(chunk)
			source.chunk, chunk = nil, nil
		}
	}
	if f.Version() != count || len(chunk) != 0 {
		t.Fatal("incomplete heap fixture")
	}
	testHeapRenderAndRelease(t, f)
	reader = nil
	runtime.GC()
	runtime.ReadMemStats(&after)
	delta := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	t.Logf("generator=fold-scenarios-v1+unknown-padding entries=%d padding=%d chunk=%d encoded-log-bytes=%d pre-fold-heap=%d post-GC-heap=%d retained-delta=%d ratio=%.4f%% cache=%d; fold/source and disk offset index live; Store publications excluded", count, padding, chunkSize, logBytes, before.HeapAlloc, after.HeapAlloc, delta, 100*float64(delta)/float64(logBytes), source.bytes)
	if os.Getenv("PYRY_THREAD_HEAP_BASELINE") == "" && delta >= logBytes/10 {
		t.Fatal("fold/source retained at least 10% of encoded history")
	}
	runtime.KeepAlive(f)
	runtime.KeepAlive(source)
}

func testHeapRenderAndRelease(t *testing.T, f *Fold) {
	t.Helper()
	items, err := f.Render()
	if err != nil || len(items) == 0 {
		t.Fatal("disk-backed rendering failed", err)
	}
}

func testWriteHeapHistory(t *testing.T, path string, count, padding int) int64 {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	for _, entry := range testFoldCorpus(count) {
		entry.Payload = append(entry.Payload[:len(entry.Payload)-1], []byte(`,"heap_padding":"`+strings.Repeat("x", padding)+`"}`)...)
		if err := encoder.Encode(entry); err != nil {
			t.Fatal(err)
		}
	}
	stat, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return stat.Size()
}
