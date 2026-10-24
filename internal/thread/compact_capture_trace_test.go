package thread

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

// testCompactTrace observes real fixture operations without replacing their I/O.
type testCompactTrace struct {
	mu     sync.Mutex
	start  time.Time
	events []string
}

func testTraceCompactStore(t *testing.T, s *Store) *testCompactTrace {
	t.Helper()
	x := &testCompactTrace{start: time.Now()}
	var fs syscall.Statfs_t
	if err := syscall.Statfs(os.TempDir(), &fs); err != nil {
		t.Fatal("stat fixture filesystem:", err)
	}
	t.Logf("runtime=%s os=%s arch=%s procs=%d temp=%s filesystem=%#x", runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.GOMAXPROCS(0), os.TempDir(), fs.Type)
	if runtime.GOOS == "linux" {
		if raw, err := os.ReadFile("/proc/loadavg"); err == nil {
			t.Logf("loadavg=%s", strings.TrimSpace(string(raw)))
		}
		if raw, err := os.ReadFile("/proc/mounts"); err == nil {
			for _, line := range strings.Split(string(raw), "\n") {
				fields := strings.Fields(line)
				if len(fields) >= 4 && fields[1] == "/" {
					t.Logf("root-filesystem=%s volatile-sync=%t", fields[2], strings.Contains(","+fields[3]+",", ",fsync=volatile,"))
				}
			}
		}
	}
	t.Cleanup(func() { x.dump(t) })
	s.checkpointStep = func(stage string, action func() error) error {
		defer x.begin("checkpoint/" + stage)()
		return action()
	}
	lookup := s.lookup
	s.lookup = func(ctx context.Context, id conversations.ConversationID, ids []uint64) ([]history.Entry, error) {
		defer x.begin(fmt.Sprintf("lookup/%s/%v", id, ids))()
		return lookup(ctx, id, ids)
	}
	replace := s.replace
	s.replace = func(src, dst string) error {
		defer x.begin("replace")()
		return replace(src, dst)
	}
	syncDir := s.syncDir
	s.syncDir = func(dir string) error {
		defer x.begin("directory-sync")()
		return syncDir(dir)
	}
	forward := s.forward
	s.forward = func(id conversations.ConversationID) (storeReader, error) {
		defer x.begin("forward/" + string(id))()
		return forward(id)
	}
	return x
}

func (x *testCompactTrace) mark(event string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.events = append(x.events, fmt.Sprintf("%s %s", time.Since(x.start), event))
}

func (x *testCompactTrace) begin(phase string) func() {
	started := time.Now()
	x.mark("begin " + phase)
	return func() { x.mark(fmt.Sprintf("end %s elapsed=%s", phase, time.Since(started))) }
}

func (x *testCompactTrace) wait(t *testing.T, s *Store, id conversations.ConversationID, version uint64) Snapshot {
	t.Helper()
	defer x.begin(fmt.Sprintf("usable/%s/%d", id, version))()
	defer func() {
		if t.Failed() {
			x.dump(t)
			stacks := make([]byte, 128<<10)
			n := runtime.Stack(stacks, true)
			t.Logf("reload timeout goroutines:\n%s", stacks[:n])
		}
	}()
	return testStoreWait(t, s, id, StateUsable, version)
}

func (x *testCompactTrace) dump(t *testing.T) {
	t.Helper()
	x.mu.Lock()
	defer x.mu.Unlock()
	for _, event := range x.events {
		t.Log("compact trace:", event)
	}
}
