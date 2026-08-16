//go:build e2e

package e2e

import (
	"bytes"
	"sync"
)

// safeBuffer is a bytes.Buffer guarded by a sync.Mutex so concurrent Write (from
// os/exec's stderr-copy goroutine) and read access (from the test goroutine,
// e.g. inside a t.Fatalf diagnostic) don't race. Same shape as the unexported
// lockedBuffer in net/http/httptest.
//
// It lived beside the auto-attach helpers until #1348 deleted that surface. The
// daemon harness uses it too, so it moved here rather than dying with them.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// String returns a snapshot of the bytes written so far.
func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Bytes returns a snapshot of the bytes written so far. The returned slice is a
// copy and is safe to retain past the next Write call.
func (b *safeBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	src := b.buf.Bytes()
	out := make([]byte, len(src))
	copy(out, src)
	return out
}
