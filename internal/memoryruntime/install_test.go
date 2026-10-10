package memoryruntime

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func testArchive(t *testing.T, entries ...*tar.Header) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for _, h := range entries {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			if _, err := tw.Write(bytes.Repeat([]byte("x"), int(h.Size))); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func testDigest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func testEngine(t *testing.T) (engine, Options, *atomic.Int32) {
	t.Helper()
	payload := testArchive(t, &tar.Header{Name: "python/bin/python3.12", Mode: 0700, Size: 1})
	hits := new(atomic.Int32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); _, _ = w.Write(payload) }))
	t.Cleanup(srv.Close)
	e := engine{pins: lock{Python: "3.12.11", Bootstrap: "24.3.1", Runtime: artifact{File: "python.tar.gz", URL: srv.URL, SHA256: testDigest(payload)}}, client: srv.Client()}
	e.provision = func(ctx context.Context, dir string, p lock) error {
		return os.WriteFile(filepath.Join(dir, "python/bin/memsearch"), []byte("verified"), 0700)
	}
	e.probe = func(ctx context.Context, dir string, p lock) (map[string]string, error) {
		b, err := os.ReadFile(filepath.Join(dir, "python/bin/memsearch"))
		if err != nil || string(b) != "verified" {
			return nil, ErrProbe
		}
		return map[string]string{"python": p.Python}, nil
	}
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return e, Options{Home: home, Mode: "openai"}, hits
}
func testInstall(t *testing.T, e engine, o Options) Runtime {
	t.Helper()
	r, err := e.install(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func testNoPublished(t *testing.T, o Options) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(o.Home, ".pyry/memory/runtime/current.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial publication: %v", err)
	}
}

func TestRepeatAndReplacement(t *testing.T) {
	e, o, hits := testEngine(t)
	first := testInstall(t, e, o)
	second := testInstall(t, e, o)
	if first.Launch != second.Launch || hits.Load() != 1 {
		t.Fatalf("reuse failed: %v %v downloads=%d", first, second, hits.Load())
	}
	e.pins.Python = "3.12.12"
	third := testInstall(t, e, o)
	if third.Launch == first.Launch {
		t.Fatal("replacement reused old generation")
	}
	if b, err := os.ReadFile(first.Launch); err != nil || string(b) != "verified" {
		t.Fatalf("old launch changed: %s %v", b, err)
	}
	if err := os.WriteFile(third.Launch, []byte("corrupt"), 0700); err != nil {
		t.Fatal(err)
	}
	fourth := testInstall(t, e, o)
	if fourth.Launch == third.Launch {
		t.Fatal("corrupt tree reused")
	}
	filepath.WalkDir(filepath.Join(o.Home, ".pyry"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			t.Fatal(err)
		}
		info, err := d.Info()
		if err != nil {
			t.Fatal(err)
		}
		if d.IsDir() && info.Mode().Perm() != 0700 {
			t.Errorf("directory permissions %s %o", path, info.Mode().Perm())
		}
		if filepath.Base(path) == "current.json" && info.Mode().Perm() != 0600 {
			t.Errorf("metadata permissions %o", info.Mode().Perm())
		}
		return nil
	})
}
func TestConcurrentAndCancelledWaiter(t *testing.T) {
	e, o, hits := testEngine(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var active atomic.Int32
	base := e.provision
	e.provision = func(ctx context.Context, dir string, p lock) error {
		if active.Add(1) != 1 {
			t.Error("overlapping installs")
		}
		defer active.Add(-1)
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		return base(ctx, dir, p)
	}
	done := make(chan error, 1)
	go func() { _, err := e.install(context.Background(), o); done <- err }()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waiting := make(chan struct{})
	waiterDone := make(chan error, 1)
	waiterOptions := o
	waiterOptions.Progress = func(stage string) {
		if stage == "waiting" {
			close(waiting)
		}
	}
	go func() { _, err := e.install(ctx, waiterOptions); waiterDone <- err }()
	<-waiting
	time.Sleep(30 * time.Millisecond)
	cancel()
	if err := <-waiterDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter: %v", err)
	}
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.install(context.Background(), o); err != nil {
				t.Error(err)
			}
		}()
	}
	close(release)
	wg.Wait()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 {
		t.Fatalf("downloads=%d", hits.Load())
	}
}
func TestFailuresRetry(t *testing.T) {
	for _, stage := range []string{"install", "probe", "cancel"} {
		t.Run(stage, func(t *testing.T) {
			e, o, hits := testEngine(t)
			baseInstall, baseProbe := e.provision, e.probe
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch stage {
			case "install":
				e.provision = func(context.Context, string, lock) error { return errors.New("private subprocess output") }
			case "probe":
				e.probe = func(context.Context, string, lock) (map[string]string, error) { return nil, ErrProbe }
			case "cancel":
				e.provision = func(ctx context.Context, _ string, _ lock) error { cancel(); return ctx.Err() }
			}
			if _, err := e.install(ctx, o); err == nil {
				t.Fatal("failure returned success")
			}
			testNoPublished(t, o)
			e.provision, e.probe = baseInstall, baseProbe
			testInstall(t, e, o)
			if hits.Load() != 1 {
				t.Fatalf("verified download not reused: %d", hits.Load())
			}
		})
	}
	e, o, _ := testEngine(t)
	old := testInstall(t, e, o)
	e.pins.Python = "3.12.12"
	e.probe = func(context.Context, string, lock) (map[string]string, error) { return nil, ErrProbe }
	if _, err := e.install(context.Background(), o); err == nil {
		t.Fatal("replacement probe passed")
	}
	if _, err := os.Stat(old.Launch); err != nil {
		t.Fatal("old generation lost", err)
	}
}
func TestDownloadIntegrity(t *testing.T) {
	e, o, hits := testEngine(t)
	good := e.pins.Runtime.SHA256
	e.pins.Runtime.SHA256 = testDigest([]byte("bad"))
	if _, err := e.install(context.Background(), o); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("integrity: %v", err)
	}
	testNoPublished(t, o)
	e.pins.Runtime.SHA256 = good
	testInstall(t, e, o)
	cached := filepath.Join(o.Home, ".pyry/memory/runtime/cache", good, "python.tar.gz")
	if err := os.WriteFile(cached, []byte("truncated"), 0600); err != nil {
		t.Fatal(err)
	}
	e.pins.Python = "3.12.12"
	testInstall(t, e, o)
	if hits.Load() != 3 {
		t.Fatalf("corrupt cache not redownloaded: %d", hits.Load())
	}
}
func TestUnsafeDestinations(t *testing.T) {
	for _, kind := range []string{"symlink", "shared", "foreign", "hardlink", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			e, o, _ := testEngine(t)
			outside := t.TempDir()
			marker := filepath.Join(outside, "marker")
			if err := os.WriteFile(marker, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(o.Home, ".pyry")
			switch kind {
			case "symlink":
				if err := os.Symlink(outside, target); err != nil {
					t.Fatal(err)
				}
			case "shared":
				if err := os.Mkdir(target, 0777); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(target, 0777); err != nil {
					t.Fatal(err)
				}
			case "foreign":
				if os.Getuid() != 0 {
					o.Home = "/usr"
					break
				}
				if err := os.Mkdir(target, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chown(target, 65534, 65534); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.MkdirAll(filepath.Join(target, "memory/runtime"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(marker, filepath.Join(target, "memory/runtime/install.lock")); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := os.MkdirAll(filepath.Join(target, "memory/runtime"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := syscall.Mkfifo(filepath.Join(target, "memory/runtime/current.json"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := e.install(context.Background(), o); !errors.Is(err, ErrUnsafe) {
				t.Fatalf("unsafe %s accepted: %v", kind, err)
			}
			b, err := os.ReadFile(marker)
			if err != nil || string(b) != "keep" {
				t.Fatal("outside target changed")
			}
		})
	}
}
func TestArchiveConfinement(t *testing.T) {
	for _, h := range []*tar.Header{{Name: "../outside", Mode: 0600, Size: 1}, {Name: "/outside", Mode: 0600, Size: 1}, {Name: "link", Typeflag: tar.TypeSymlink, Linkname: "../outside"}, {Name: "link", Typeflag: tar.TypeLink, Linkname: "/outside"}, {Name: "fifo", Typeflag: tar.TypeFifo}} {
		t.Run(h.Name+string(h.Typeflag), func(t *testing.T) {
			dir := t.TempDir()
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			if err := root.Mkdir("gen", 0700); err != nil {
				t.Fatal(err)
			}
			if err := root.WriteFile("archive", testArchive(t, h), 0600); err != nil {
				t.Fatal(err)
			}
			if err := extract(context.Background(), root, "gen", "archive"); err == nil {
				t.Fatal("unsafe archive accepted")
			}
		})
	}
}

func TestHelperProcess(t *testing.T) {
	home := os.Getenv("MEMORY_RUNTIME_HELPER_HOME")
	if home == "" {
		return
	}
	data, err := os.ReadFile(filepath.Join(home, "helper-pins.json"))
	if err != nil {
		os.Exit(2)
	}
	var pins lock
	if err := json.Unmarshal(data, &pins); err != nil {
		os.Exit(3)
	}
	e := engine{pins: pins, client: &http.Client{Timeout: time.Minute}}
	e.provision = func(ctx context.Context, dir string, p lock) error {
		if err := os.WriteFile(filepath.Join(home, "held"), []byte("yes"), 0600); err != nil {
			return err
		}
		for {
			if _, err := os.Stat(filepath.Join(home, "release")); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		return os.WriteFile(filepath.Join(dir, "python/bin/memsearch"), []byte("verified"), 0700)
	}
	e.probe = func(ctx context.Context, dir string, p lock) (map[string]string, error) {
		b, err := os.ReadFile(filepath.Join(dir, "python/bin/memsearch"))
		if err != nil || string(b) != "verified" {
			return nil, ErrProbe
		}
		return map[string]string{"python": p.Python}, nil
	}
	if _, err := e.install(context.Background(), Options{Home: home, Mode: "openai"}); err != nil {
		os.Exit(4)
	}
	os.Exit(0)
}
func testHolder(t *testing.T, e engine, o Options) *exec.Cmd {
	t.Helper()
	data, err := json.Marshal(e.pins)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(o.Home, "helper-pins.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(), "MEMORY_RUNTIME_HELPER_HOME="+o.Home)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(o.Home, "held")); err == nil {
			return cmd
		}
		if time.Now().After(deadline) {
			t.Fatal("helper did not start installation")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
func TestCrossProcessExclusion(t *testing.T) {
	e, o, hits := testEngine(t)
	cmd := testHolder(t, e, o)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := e.install(ctx, o); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cross-process waiter: %v", err)
	}
	if hits.Load() != 1 {
		t.Fatal("waiter downloaded under another installer process lock")
	}
	if err := os.WriteFile(filepath.Join(o.Home, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	testInstall(t, e, o)
	if hits.Load() != 1 {
		t.Fatal("second process did not reuse published installation")
	}
}
func TestInterruptedRetry(t *testing.T) {
	e, o, hits := testEngine(t)
	cmd := testHolder(t, e, o)
	dir := filepath.Join(o.Home, ".pyry/memory/runtime/install-interrupted")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	testNoPublished(t, o)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	testInstall(t, e, o)
	if hits.Load() != 1 {
		t.Fatal("interrupted setup lost verified download")
	}
}
func TestUnsupported(t *testing.T) {
	for _, mode := range []string{"local", "unknown", ""} {
		_, err := Install(context.Background(), Options{Home: t.TempDir(), Mode: mode})
		if !errors.Is(err, ErrUnsupported) {
			t.Fatalf("mode %q: %v", mode, err)
		}
	}
}
func TestFailureIsBounded(t *testing.T) {
	f := &Failure{Stage: "install", Err: errors.New("secret-value")}
	if bytes.Contains([]byte(f.Error()), []byte("secret")) {
		t.Fatal("failure exposes private output")
	}
	if !errors.Is(f, f.Err) {
		t.Fatal("cause lost")
	}
}
func TestShippedLock(t *testing.T) {
	var p lock
	if err := json.Unmarshal(lockData, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Wheels) != 32 || p.Python != "3.12.11" || p.Bootstrap != "24.3.1" {
		t.Fatal("incomplete pins")
	}
	for _, a := range append([]artifact{p.Runtime}, p.Wheels...) {
		if len(a.SHA256) != 64 || a.URL == "" {
			t.Fatal("missing integrity metadata")
		}
	}
}

func TestDependencyIntegrityBeforeExecution(t *testing.T) {
	e, o, _ := testEngine(t)
	e.pins.Wheels = []artifact{{Name: "dependency", Version: "1", File: "dependency-1-py3-none-any.whl", URL: e.pins.Runtime.URL, SHA256: testDigest([]byte("different wheel"))}}
	e.provision = func(context.Context, string, lock) error { t.Fatal("unverified package code executed"); return nil }
	if _, err := e.install(context.Background(), o); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("dependency integrity: %v", err)
	}
	testNoPublished(t, o)
}
func TestGenerationSymlinkRejected(t *testing.T) {
	e, o, _ := testEngine(t)
	old := testInstall(t, e, o)
	root, err := os.OpenRoot(o.Home)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	data, err := root.ReadFile(managed + "/current.json")
	if err != nil {
		t.Fatal(err)
	}
	var pub publication
	if err := json.Unmarshal(data, &pub); err != nil {
		t.Fatal(err)
	}
	if err := root.Symlink(pub.Generation, managed+"/install-alias"); err != nil {
		t.Fatal(err)
	}
	pub.Generation = "install-alias"
	data, err = json.Marshal(pub)
	if err != nil {
		t.Fatal(err)
	}
	if err := root.WriteFile(managed+"/current.json", data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.install(context.Background(), o); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("generation alias accepted: %v", err)
	}
	if b, err := os.ReadFile(old.Launch); err != nil || string(b) != "verified" {
		t.Fatal("other installation changed")
	}
}
func TestIncompleteDownloadRetry(t *testing.T) {
	e, o, _ := testEngine(t)
	good := testArchive(t, &tar.Header{Name: "python/bin/python3.12", Mode: 0700, Size: 1})
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.Header().Set("Content-Length", "9999")
			_, _ = w.Write([]byte("incomplete"))
			return
		}
		_, _ = w.Write(good)
	}))
	defer srv.Close()
	e.pins.Runtime.URL = srv.URL
	e.pins.Runtime.SHA256 = testDigest(good)
	if _, err := e.install(context.Background(), o); err == nil {
		t.Fatal("incomplete download accepted")
	}
	testNoPublished(t, o)
	testInstall(t, e, o)
	if hits.Load() != 2 {
		t.Fatal("incomplete download reused")
	}
}

func TestCommandProcess(t *testing.T) {
	mode := os.Args[len(os.Args)-1]
	switch {
	case mode == "env":
		if os.Getenv("OPENAI_API_KEY") != "" || os.Getenv("PYTHONPATH") != "" {
			os.Exit(2)
		}
		_, _ = os.Stdout.WriteString("sanitized")
		os.Exit(0)
	case mode == "overflow":
		_, _ = os.Stdout.Write(bytes.Repeat([]byte("x"), 65<<10))
		os.Exit(0)
	case mode == "block":
		if err := os.WriteFile("running", []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
			os.Exit(3)
		}
		for {
			time.Sleep(time.Hour)
		}
	case strings.HasPrefix(mode, "supervisor:"):
		_, _ = run(context.Background(), strings.TrimPrefix(mode, "supervisor:"), os.Args[0], "-test.run=^TestCommandProcess$", "--", "block")
		os.Exit(0)
	}
}
func TestManagedCommandBoundary(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "must-not-inherit")
	t.Setenv("PYTHONPATH", "must-not-inherit")
	dir := t.TempDir()
	out, err := run(context.Background(), dir, os.Args[0], "-test.run=^TestCommandProcess$", "--", "env")
	if err != nil || string(out) != "sanitized" {
		t.Fatalf("child environment: %q %v", out, err)
	}
	if _, err := run(context.Background(), dir, os.Args[0], "-test.run=^TestCommandProcess$", "--", "overflow"); err == nil {
		t.Fatal("unbounded child output accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := run(ctx, dir, os.Args[0], "-test.run=^TestCommandProcess$", "--", "block")
		done <- err
	}()
	testWaitFile(t, filepath.Join(dir, "running"))
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("active child cancellation: %v", err)
	}
}
func testWaitFile(t *testing.T, path string) []byte {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			return data
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
func TestParentDeathStopsCommand(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux parent-death contract")
	}
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestCommandProcess$", "--", "supervisor:"+dir)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	pidData := testWaitFile(t, filepath.Join(dir, "running"))
	pid, err := strconv.Atoi(string(pidData))
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	deadline := time.Now().Add(5 * time.Second)
	for {
		status, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if errors.Is(err, os.ErrNotExist) || bytes.Contains(status, []byte(") Z ")) {
			return
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatal("managed child survived installer process")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestWheelConfinement(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode os.FileMode
		ok   bool
	}{{"package/module.py", 0600, true}, {"../outside", 0600, false}, {"/outside", 0600, false}, {"package/link", os.ModeSymlink | 0700, false}} {
		t.Run(tc.name, func(t *testing.T) {
			var data bytes.Buffer
			zw := zip.NewWriter(&data)
			h := &zip.FileHeader{Name: tc.name}
			h.SetMode(tc.mode)
			w, err := zw.CreateHeader(h)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write([]byte("payload")); err != nil {
				t.Fatal(err)
			}
			if err := zw.Close(); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			if err := root.WriteFile("wheel", data.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			err = validateWheel(root, "wheel")
			if (err == nil) != tc.ok {
				t.Fatalf("wheel confinement: %v", err)
			}
		})
	}
}
