package thread

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	runtimeTrace "runtime/trace"
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
	ctx    context.Context
	mu     sync.Mutex
	start  time.Time
	events []string
}

// testRecordCompactTrace keeps recording alive through all child cleanups.
func testRecordCompactTrace(t *testing.T) {
	t.Helper()
	dir := os.Getenv("PYRY_COMPACT_TRACE_DIR")
	if dir == "" {
		return
	}
	file, err := os.CreateTemp(dir, "compact-capture-*.trace")
	if err != nil {
		t.Fatal("create compact recording:", err)
	}
	writer := bufio.NewWriter(file)
	if err := runtimeTrace.Start(writer); err != nil {
		if closeErr := file.Close(); closeErr != nil {
			t.Error("close unstarted compact recording:", closeErr)
		}
		t.Fatal("start compact recording:", err)
	}
	t.Logf("compact recording started path=%s time=%s", file.Name(), time.Now().UTC().Format(time.RFC3339Nano))
	t.Cleanup(func() {
		runtimeTrace.Stop()
		if err := writer.Flush(); err != nil {
			t.Error("write compact recording:", err)
		}
		if err := file.Close(); err != nil {
			t.Error("close compact recording:", err)
		}
		t.Logf("compact recording stopped path=%s time=%s", file.Name(), time.Now().UTC().Format(time.RFC3339Nano))
	})
}

func TestCompactTraceRecording(t *testing.T) {
	if runtimeTrace.IsEnabled() {
		t.Skip("runtime tracing is already owned by the caller")
	}
	dir := t.TempDir()
	t.Setenv("PYRY_COMPACT_TRACE_DIR", dir)
	for range 2 {
		t.Run("record", func(t *testing.T) {
			testRecordCompactTrace(t)
			if !runtimeTrace.IsEnabled() {
				t.Fatal("runtime recording did not start")
			}
		})
		if runtimeTrace.IsEnabled() {
			t.Fatal("runtime recording survived cleanup")
		}
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 2 {
		t.Fatal("recordings must have unique paths", files, err)
	}
	for _, file := range files {
		info, err := file.Info()
		if err != nil || info.Size() <= 16 || info.Mode().Perm() != 0600 {
			t.Fatal("incomplete or non-private recording", info, err)
		}
	}
}

func testTraceCompactStore(t *testing.T, s *Store) *testCompactTrace {
	t.Helper()
	ctx, task := runtimeTrace.NewTask(context.Background(), t.Name())
	x := &testCompactTrace{ctx: ctx, start: time.Now()}
	t.Cleanup(task.End)
	dir, err := s.history.EnsureLogDir(testStoreA)
	if err != nil {
		t.Fatal(err)
	}
	var fs syscall.Statfs_t
	if err := syscall.Statfs(dir, &fs); err != nil {
		t.Fatal("stat fixture filesystem:", err)
	}
	t.Logf("runtime=%s os=%s arch=%s procs=%d fixture=%s temp=%s filesystem=%#x", runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.GOMAXPROCS(0), dir, os.TempDir(), fs.Type)
	environment := func() {
		if runtime.GOOS == "linux" {
			for _, name := range []string{"/proc/loadavg", "/proc/pressure/cpu", "/proc/pressure/io", "/proc/self/io"} {
				if raw, err := os.ReadFile(name); err == nil {
					t.Logf("%s=%s", name, strings.TrimSpace(string(raw)))
				}
			}
			if raw, err := os.ReadFile("/proc/mounts"); err == nil {
				mount := ""
				mountLine := ""
				for _, line := range strings.Split(string(raw), "\n") {
					fields := strings.Fields(line)
					if len(fields) >= 4 && (fields[1] == "/" || dir == fields[1] || strings.HasPrefix(dir, fields[1]+"/")) && len(fields[1]) > len(mount) {
						mount, mountLine = fields[1], line
					}
				}
				t.Logf("fixture-mount=%s", mountLine)
			}
		}
	}
	environment()
	t.Cleanup(func() { _ = s.Shutdown(); environment(); x.dump(t) })
	s.phase = func(id conversations.ConversationID, phase string) func() {
		s.mu.Lock()
		worker := s.workers[id]
		s.mu.Unlock()
		label := fmt.Sprintf("conversation=%s worker=%p %s", id, worker, phase)
		if phase == "snapshot" {
			status := s.Status(id)
			x.mark(fmt.Sprintf("%s readiness=%d/%d", label, status.State, status.Version))
		}
		return x.begin(label)
	}
	s.checkpointStep = func(stage string, action func() error) error {
		defer x.begin("checkpoint/" + stage)()
		return action()
	}
	lookup := s.lookup
	s.lookup = func(ctx context.Context, id conversations.ConversationID, ids []uint64) ([]history.Entry, error) {
		defer s.observePhase(id, fmt.Sprintf("lookup/%v", ids))()
		return lookup(ctx, id, ids)
	}
	replace := s.replace
	s.replace = func(src, dst string) error {
		defer s.observePhase(conversations.ConversationID(filepath.Base(filepath.Dir(filepath.Dir(dst)))), "replace")()
		return replace(src, dst)
	}
	syncDir := s.syncDir
	s.syncDir = func(dir string) error {
		defer s.observePhase(conversations.ConversationID(filepath.Base(filepath.Dir(dir))), "directory-sync")()
		return syncDir(dir)
	}
	forward := s.forward
	s.forward = func(id conversations.ConversationID) (storeReader, error) {
		defer s.observePhase(id, "forward")()
		return forward(id)
	}
	return x
}

func (x *testCompactTrace) mark(event string) {
	runtimeTrace.Log(x.ctx, "compact", event)
	x.mu.Lock()
	defer x.mu.Unlock()
	x.events = append(x.events, fmt.Sprintf("%s %s", time.Since(x.start), event))
}

func (x *testCompactTrace) begin(phase string) func() {
	region := runtimeTrace.StartRegion(x.ctx, phase)
	started := time.Now()
	x.mark("begin " + phase)
	return func() {
		x.mark(fmt.Sprintf("end %s elapsed=%s", phase, time.Since(started)))
		region.End()
	}
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

// testCompactOverlap aligns real fixture work only in an explicitly requested diagnostic.
func testCompactOverlap(t *testing.T, role string) {
	t.Helper()
	address := os.Getenv("PYRY_COMPACT_OVERLAP_SOCKET")
	if address == "" {
		return
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", address)
	if err != nil {
		t.Fatal("connect overlap rendezvous:", err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			t.Error("close overlap rendezvous:", err)
		}
	}()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		t.Fatal("bound overlap rendezvous:", err)
	}
	if _, err := io.WriteString(conn, role+"\n"); err != nil {
		t.Fatal("announce overlap participant:", err)
	}
	var release [1]byte
	if _, err := io.ReadFull(conn, release[:]); err != nil || release[0] != 1 {
		t.Fatal("release overlap rendezvous:", err, release)
	}
	t.Logf("overlap released role=%s time=%s", role, time.Now().UTC().Format(time.RFC3339Nano))
}
