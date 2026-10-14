package history

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
)

type testSyncTrace struct {
	t                *testing.T
	paths            []string
	failOp, failPath string
	err              error
}

func testSyncRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func testObserveSync(t *testing.T, s *Store) *testSyncTrace {
	t.Helper()
	x := &testSyncTrace{t: t, err: errors.New("sync fault")}
	s.syncIO = syncIO{
		open: func(path string, flags int, mode fs.FileMode) (*os.File, error) {
			if flags&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_TRUNC) != 0 {
				t.Errorf("sync open is not read-only: %x", flags)
			}
			if x.failOp == "open" && filepath.Base(path) == x.failPath {
				return nil, x.err
			}
			return os.OpenFile(path, flags, mode)
		},
		sync: func(f *os.File) error {
			x.paths = append(x.paths, f.Name())
			if x.failOp == "sync" && filepath.Base(f.Name()) == x.failPath {
				return x.err
			}
			return f.Sync()
		},
		close: func(f *os.File) error {
			err := f.Close()
			if x.failOp == "close" && filepath.Base(f.Name()) == x.failPath {
				return errors.Join(err, x.err)
			}
			return err
		},
	}
	return x
}

func (x *testSyncTrace) want(paths ...string) {
	x.t.Helper()
	if !reflect.DeepEqual(x.paths, paths) {
		x.t.Fatalf("synced %v, want %v", x.paths, paths)
	}
	x.paths = nil
}

func testSyncPaths(t *testing.T, s *Store, id conversations.ConversationID) []string {
	t.Helper()
	dir := historyDir(s.instanceDir, id)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, e := range entries {
		if _, ok := parseSegmentName(e.Name()); ok {
			paths = append(paths, filepath.Join(dir, e.Name()))
		}
	}
	return paths
}

func testSyncCall(t *testing.T, s *Store, id conversations.ConversationID) {
	t.Helper()
	if err := s.Sync(id); err != nil {
		t.Fatal(err)
	}
}

func TestSyncTargetsAndSideEffects(t *testing.T) {
	t.Parallel()
	s := newStore(testSyncRoot(t), testSegmentBytes)
	x := testObserveSync(t, s)
	appendN(t, s, convA, 0, 1)
	x.want() // Append does no sync.
	dir := historyDir(s.instanceDir, convA)
	first := filepath.Join(dir, segmentName(1))
	wake, unfollow := s.follow(convA)
	defer unfollow()
	before, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	c := *s.convs[convA]
	readBytes, opened := s.readBytes, s.segmentsOpened
	testSyncCall(t, s, convA)
	x.want(first, dir)
	after, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || s.convs[convA].lastID != c.lastID || s.convs[convA].segBytes != c.segBytes || s.convs[convA].loaded != c.loaded || s.readBytes != readBytes || s.segmentsOpened != opened {
		t.Fatal("sync changed bytes, append cursor or read counters")
	}
	select {
	case <-wake:
		t.Fatal("sync notified follower")
	default:
	}
	testSyncCall(t, s, convA)
	x.want()
	entries := appendN(t, s, convA, 1, 1)
	if entries[0].ID != 2 || s.convs[convA].seg != 1 {
		t.Fatal("sync changed ID allocation or fixture rolled early")
	}
	testSyncCall(t, s, convA)
	x.want(first) // Existing segment needs no directory sync.
	appendN(t, s, convA, 2, 20)
	x.want()
	paths := testSyncPaths(t, s, convA)
	if len(paths) < 3 {
		t.Fatal("fixture needs multiple rolls")
	}
	testSyncCall(t, s, convA)
	x.want(append(paths, dir)...)
	testSyncCall(t, s, convA)
	x.want()
}

func TestSyncMultipleRollsAndIsolation(t *testing.T) {
	t.Parallel()
	s := newStore(testSyncRoot(t), testSegmentBytes)
	x := testObserveSync(t, s)
	appendN(t, s, convA, 0, 24)
	appendN(t, s, convB, 0, 12)
	x.want()
	for _, id := range []conversations.ConversationID{convA, convB} {
		paths := testSyncPaths(t, s, id)
		if len(paths) < 3 {
			t.Fatal("fixture needs multiple pending rolls")
		}
		testSyncCall(t, s, id)
		x.want(append(paths, historyDir(s.instanceDir, id))...)
		testSyncCall(t, s, id)
		x.want()
	}
}

func TestSyncFirstAndEmpty(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"cold", "reopened", "read loaded", "append loaded", "missing", "empty", "empty segment"} {
		t.Run(mode, func(t *testing.T) {
			root := testSyncRoot(t)
			s := newStore(root, testSegmentBytes)
			dir := historyDir(root, convA)
			switch mode {
			case "cold", "reopened", "read loaded", "append loaded":
				appendN(t, s, convA, 0, 16)
				testSyncCall(t, s, convA)
				s = newStore(root, testSegmentBytes)
				if mode == "read loaded" {
					if _, err := s.LatestEntryID(convA); err != nil {
						t.Fatal(err)
					}
					if _, err := s.Page(convA, "", 2); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "append loaded" {
					appendN(t, s, convA, 16, 1)
				}
				if mode == "reopened" {
					s = newStore(root, testSegmentBytes)
				}
			case "empty", "empty segment":
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if mode == "empty segment" {
					if err := os.WriteFile(filepath.Join(dir, segmentName(1)), nil, 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			x := testObserveSync(t, s)
			readBytes, opened := s.readBytes, s.segmentsOpened
			testSyncCall(t, s, convA)
			if mode == "missing" || mode == "empty" {
				x.want()
			} else {
				x.want(append(testSyncPaths(t, s, convA), dir)...)
			}
			if s.readBytes != readBytes || s.segmentsOpened != opened {
				t.Fatal("sync read entries")
			}
			testSyncCall(t, s, convA)
			x.want()
			if mode == "missing" {
				if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
					t.Fatal("sync created a missing log")
				}
			}
			if mode == "missing" || mode == "empty" {
				appendN(t, s, convA, 0, 1)
				testSyncCall(t, s, convA)
				x.want(filepath.Join(dir, segmentName(1)), dir)
			}
		})
	}
	s := New(testSyncRoot(t))
	if err := s.Sync("../bad"); !errors.Is(err, ErrInvalidID) {
		t.Fatal(err)
	}
}

func TestSyncFailuresRetry(t *testing.T) {
	t.Parallel()
	for _, op := range []string{"open", "sync", "close"} {
		for _, target := range []string{"file", "directory"} {
			t.Run(op+" "+target, func(t *testing.T) {
				s := newStore(testSyncRoot(t), testSegmentBytes)
				appendN(t, s, convA, 0, 16)
				paths := testSyncPaths(t, s, convA)
				dir := historyDir(s.instanceDir, convA)
				want := append(paths, dir)
				x := testObserveSync(t, s)
				x.failOp, x.failPath = op, filepath.Base(paths[len(paths)-1])
				if target == "directory" {
					x.failPath = "history"
				}
				if err := s.Sync(convA); !errors.Is(err, x.err) {
					t.Fatalf("fault lost: %v", err)
				}
				x.paths, x.failOp = nil, ""
				testSyncCall(t, s, convA)
				x.want(want...)
				testSyncCall(t, s, convA)
				x.want()
				// Also retry failures after an established first-success boundary.
				appendN(t, s, convA, 16, 1)
				firstPending := s.convs[convA].seg
				appendN(t, s, convA, 17, 19)
				var pending []string
				for _, path := range testSyncPaths(t, s, convA) {
					n, _ := parseSegmentName(filepath.Base(path))
					if n >= firstPending {
						pending = append(pending, path)
					}
				}
				x.failOp, x.failPath = op, filepath.Base(pending[len(pending)-1])
				if target == "directory" {
					x.failPath = "history"
				}
				if err := s.Sync(convA); !errors.Is(err, x.err) {
					t.Fatal(err)
				}
				x.paths, x.failOp = nil, ""
				testSyncCall(t, s, convA)
				x.want(append(pending, dir)...)
			})
		}
	}
}

func TestSyncMissingAndUnsafeTargets(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"missing file", "missing directory", "symlink", "directory leaf", "fifo", "sibling redirect", "outside redirect", "unsafe after noop", "sibling redirect after noop", "outside redirect after noop", "read loaded missing", "first failure then missing"} {
		t.Run(mode, func(t *testing.T) {
			s := newStore(testSyncRoot(t), testSegmentBytes)
			appendN(t, s, convA, 0, 16)
			x := testObserveSync(t, s)
			dir := historyDir(s.instanceDir, convA)
			paths := testSyncPaths(t, s, convA)
			path := paths[0]
			wantErr := error(nil)
			noop := strings.HasSuffix(mode, " after noop")
			if noop {
				testSyncCall(t, s, convA)
				x.paths = nil
			}
			mode = strings.TrimSuffix(mode, " after noop")
			if mode == "read loaded missing" {
				s = newStore(s.instanceDir, testSegmentBytes)
				if _, err := s.LatestEntryID(convA); err != nil {
					t.Fatal(err)
				}
				x = testObserveSync(t, s)
			}
			if mode == "first failure then missing" {
				s = newStore(s.instanceDir, testSegmentBytes)
				x = testObserveSync(t, s)
				x.failOp, x.failPath = "sync", filepath.Base(paths[len(paths)-1])
				if err := s.Sync(convA); !errors.Is(err, x.err) {
					t.Fatal(err)
				}
				x.paths, x.failOp = nil, ""
			}
			switch mode {
			case "missing directory", "sibling redirect", "outside redirect":
				if err := os.Rename(dir, dir+"-saved"); err != nil {
					t.Fatal(err)
				}
				if mode != "missing directory" {
					target := t.TempDir()
					if mode == "sibling redirect" {
						appendN(t, s, convB, 0, 1)
						target = historyDir(s.instanceDir, convB)
					}
					if err := os.Symlink(target, dir); err != nil {
						t.Fatal(err)
					}
					wantErr = ErrNotContained
				} else {
					wantErr = fs.ErrNotExist
				}
			default:
				if err := os.Rename(path, path+"-saved"); err != nil {
					t.Fatal(err)
				}
				switch mode {
				case "symlink", "unsafe":
					if err := os.Symlink(path+"-saved", path); err != nil {
						t.Fatal(err)
					}
					wantErr = ErrCorruptSegment
				case "directory leaf":
					if err := os.Mkdir(path, 0o700); err != nil {
						t.Fatal(err)
					}
					wantErr = ErrCorruptSegment
				case "fifo":
					if err := syscall.Mkfifo(path, 0o600); err != nil {
						t.Fatal(err)
					}
					wantErr = ErrCorruptSegment
				default:
					wantErr = fs.ErrNotExist
				}
			}
			if err := s.Sync(convA); err == nil || !errors.Is(err, wantErr) {
				t.Fatalf("sync error %v, want %v", err, wantErr)
			}
			if mode == "missing directory" || strings.Contains(mode, "redirect") {
				if mode != "missing directory" {
					if err := os.Remove(dir); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Rename(dir+"-saved", dir); err != nil {
					t.Fatal(err)
				}
			} else {
				if mode != "missing file" && mode != "read loaded missing" && mode != "first failure then missing" {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Rename(path+"-saved", path); err != nil {
					t.Fatal(err)
				}
			}
			x.paths = nil
			testSyncCall(t, s, convA)
			if noop {
				x.want()
			} else {
				x.want(append(paths, dir)...)
			}
		})
	}
}

func TestSyncFailedAppendRecovery(t *testing.T) {
	t.Parallel()
	s := newStore(testSyncRoot(t), testSegmentBytes)
	want := appendN(t, s, convA, 0, 1)
	testSyncCall(t, s, convA)
	want = append(want, appendN(t, s, convA, 1, 1)...)
	dir := historyDir(s.instanceDir, convA)
	path := filepath.Join(dir, segmentName(1))
	if err := os.Rename(path, path+"-saved"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(convA, "x", []byte(`{}`), testTS); err == nil {
		t.Fatal("append should fail")
	}
	if err := os.Rename(path+"-saved", path); err != nil {
		t.Fatal(err)
	}
	// A failed write whose rollback failed can leave a torn tail.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"id":3`); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	entries := appendN(t, s, convA, 2, 1)
	if entries[0].ID != 3 || s.convs[convA].seg != 2 {
		t.Fatal("failed-write recovery changed")
	}
	x := testObserveSync(t, s)
	testSyncCall(t, s, convA)
	x.want(path, filepath.Join(dir, segmentName(2)), dir)
	assertReversed(t, walkAll(t, s, convA, 2), append(want, entries...))
}

func TestSyncSerializesAppend(t *testing.T) {
	t.Parallel()
	s := newStore(testSyncRoot(t), testSegmentBytes)
	appendN(t, s, convA, 0, 1)
	x := testObserveSync(t, s)
	entered, release := make(chan struct{}), make(chan struct{})
	syncFile := s.syncIO.sync
	s.syncIO.sync = func(f *os.File) error {
		select {
		case <-entered:
		default:
			close(entered)
			<-release
		}
		return syncFile(f)
	}
	synced := make(chan error, 1)
	go func() { synced <- s.Sync(convA) }()
	<-entered
	if s.mu.TryLock() {
		s.mu.Unlock()
		close(release)
		<-synced
		t.Fatal("sync released append serialization at its captured boundary")
	}
	appended := make(chan error, 1)
	started := make(chan struct{})
	go func() { close(started); _, err := s.Append(convA, "x", []byte(`{}`), testTS); appended <- err }()
	<-started
	select {
	case err := <-appended:
		close(release)
		<-synced
		t.Fatalf("append crossed sync boundary: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-synced; err != nil {
		t.Fatal(err)
	}
	if err := <-appended; err != nil {
		t.Fatal(err)
	}
	dir := historyDir(s.instanceDir, convA)
	x.want(filepath.Join(dir, segmentName(1)), dir)
	testSyncCall(t, s, convA)
	x.want(filepath.Join(dir, segmentName(1)))
}

func TestSyncRechecksOpenedLeaf(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"symlink", "fifo", "closed descriptor"} {
		t.Run(mode, func(t *testing.T) {
			s := newStore(testSyncRoot(t), testSegmentBytes)
			appendN(t, s, convA, 0, 1)
			x := testObserveSync(t, s)
			dir := historyDir(s.instanceDir, convA)
			path := filepath.Join(dir, segmentName(1))
			open := s.syncIO.open
			s.syncIO.open = func(name string, flags int, modeBits fs.FileMode) (*os.File, error) {
				if mode == "closed descriptor" {
					f, err := open(name, flags, modeBits)
					if err == nil {
						err = f.Close()
					}
					return f, err
				}
				if err := os.Rename(path, path+"-saved"); err != nil {
					return nil, err
				}
				var err error
				if mode == "fifo" {
					err = syscall.Mkfifo(path, 0o600)
				} else {
					err = os.Symlink(path+"-saved", path)
				}
				if err != nil {
					return nil, err
				}
				return open(name, flags, modeBits)
			}
			if err := s.Sync(convA); err == nil {
				t.Fatal("unsafe opened leaf succeeded")
			}
			x.want()
			s.syncIO.open = open
			if mode != "closed descriptor" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(path+"-saved", path); err != nil {
					t.Fatal(err)
				}
			}
			testSyncCall(t, s, convA)
			x.want(path, dir)
		})
	}
}
