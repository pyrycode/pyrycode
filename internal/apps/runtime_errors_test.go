package apps

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func TestOwnerPreparationErrorContext(t *testing.T) {
	for _, missing := range []string{"builds", "manifest", "entry"} {
		t.Run(missing, func(t *testing.T) {
			r, o := ownerFixture(t)
			dir := ownerBuild(t, r, testApp, "a", "1.0.0", ownerService)
			path, operation := filepath.Join(dir, "app.json"), "open build manifest"
			switch missing {
			case "builds":
				path, operation = filepath.Join(r.root, testApp, "builds"), "open builds directory"
			case "entry":
				path, operation = filepath.Join(dir, "dist/service/main.js"), "open service entry"
			}
			if err := os.RemoveAll(path); err != nil {
				t.Fatal(err)
			}
			err := o.Start(context.Background(), testApp)
			if !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), operation) {
				t.Fatalf("missing operation context or errno: %v", err)
			}
			<-o.generations[testApp].done
			v := ownerRecord(t, r, testApp)
			if v.LastError == nil || v.LastError.Code != "app.unavailable" || v.LastError.Message != "Service could not start" {
				t.Fatal("changed persisted static failure", v)
			}
		})
	}
}

func TestRuntimeFileErrorContext(t *testing.T) {
	dir := testRoot(t)
	if _, err := privateDir(filepath.Join(dir, "missing")); !errors.Is(err, syscall.ENOENT) || !strings.Contains(err.Error(), "open directory component") {
		t.Fatalf("directory error: %v", err)
	}
	f, err := privateDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err = regularAt(f, "missing", false); !errors.Is(err, syscall.ENOENT) || !strings.Contains(err.Error(), "open regular file") {
		t.Fatalf("file error: %v", err)
	}
}

func TestOwnerCleanupErrorContext(t *testing.T) {
	for _, operation := range []string{"stop", "shutdown"} {
		for _, failure := range []struct {
			signal syscall.Signal
			name   string
		}{{syscall.SIGTERM, "terminate"}, {0, "inspect"}, {syscall.SIGKILL, "kill"}} {
			t.Run(operation+"/"+failure.name, func(t *testing.T) {
				r, o := ownerFixture(t)
				o.node = writeNodeDouble(t, "v24.15.0", false)
				ownerBuild(t, r, testApp, "a", "1.0.0", "forced descendants")
				// Perform the actual cleanup before reporting a controlled syscall
				// failure, so an error assertion cannot conceal a leaked real child.
				o.kill = func(pid int, sig syscall.Signal) error {
					err := syscall.Kill(pid, sig)
					if err == nil && sig == failure.signal {
						return syscall.EPERM
					}
					return err
				}
				if err := o.Start(context.Background(), testApp); err != nil {
					t.Fatal(err)
				}
				data := filepath.Join(r.root, testApp, "data")
				pid, err := strconv.Atoi(string(waitFile(t, filepath.Join(data, "ready"))))
				if err != nil {
					t.Fatal(err)
				}
				descendant, err := strconv.Atoi(string(waitFile(t, filepath.Join(data, "descendant"))))
				if err != nil {
					t.Fatal(err)
				}
				if operation == "stop" {
					err = o.Stop(testApp)
				} else {
					err = o.Shutdown()
				}
				if !errors.Is(err, syscall.EPERM) || !strings.Contains(err.Error(), failure.name+" process group") {
					t.Fatalf("cleanup error lost context or errno: %v", err)
				}
				waitGone(t, pid)
				waitGone(t, descendant)
				// The fixture's final shutdown should not report an injected error twice.
				o.mu.Lock()
				o.failures = nil
				o.mu.Unlock()
			})
		}
	}
}
