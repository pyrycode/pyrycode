package apps

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func writeNodeDouble(t *testing.T, version string, failSpawn bool) string {
	t.Helper()
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(t.TempDir(), "node")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	removal := ""
	if failSpawn {
		removal = "rm -- " + quote(path) + "\n"
	}
	script := "#!/bin/sh\nif [ \"$1\" = '--version' ]; then\nprintf '%s\\n' " + quote(version) + "\n" + removal + "exit 0\nfi\nexec " + quote(exe) + " -test.run '^TestOwnerHelperProcess$' -- \"$@\"\n"
	if e = os.WriteFile(path, []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	return path
}

// PID markers become visible only after their complete contents are written.
func publishOwnerPID(path string) error {
	pending := path + ".tmp"
	if err := os.WriteFile(pending, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		return err
	}
	return os.Rename(pending, path)
}

func TestOwnerHelperProcess(t *testing.T) {
	index := -1
	for i, v := range os.Args {
		if v == "--" {
			index = i
			break
		}
	}
	if index < 0 {
		return
	}
	if os.Args[index+1] == "version" {
		base := os.Args[index+2]
		if e := os.WriteFile(base+".entered", nil, 0600); e != nil {
			os.Exit(9)
		}
		for {
			if _, e := os.Stat(base + ".release"); e == nil {
				fmt.Println("v24.15.0")
				os.Exit(0)
			}
			time.Sleep(time.Millisecond)
		}
	}
	data := os.Getenv("PYRY_APP_DATA_DIR")
	descendant := len(os.Args) > index+1 && os.Args[index+1] == "descendant"
	forced := descendant && len(os.Args) > index+2 && os.Args[index+2] == "forced"
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM)
	if descendant {
		if e := publishOwnerPID(filepath.Join(data, "descendant")); e != nil {
			os.Exit(3)
		}
	} else {
		script, e := os.ReadFile("dist/service/main.js")
		if e != nil {
			os.Exit(4)
		}
		if strings.Contains(string(script), "descendants") {
			mode := "graceful"
			if strings.Contains(string(script), "forced") {
				mode = "forced"
			}
			exe, e := os.Executable()
			if e != nil {
				os.Exit(5)
			}
			cmd := exec.Command(exe, "-test.run", "^TestOwnerHelperProcess$", "--", "descendant", mode)
			cmd.Env = os.Environ()
			if e = cmd.Start(); e != nil {
				os.Exit(6)
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				if _, e = os.Stat(filepath.Join(data, "descendant")); e == nil {
					break
				}
				if time.Now().After(deadline) {
					os.Exit(7)
				}
				time.Sleep(time.Millisecond)
			}
		}
		if e = os.WriteFile(filepath.Join(data, fmt.Sprintf("launch-%d", os.Getpid())), nil, 0600); e != nil {
			os.Exit(10)
		}
		if e = publishOwnerPID(filepath.Join(data, "ready")); e != nil {
			os.Exit(8)
		}
	}
	for {
		select {
		case <-signals:
			if !forced {
				_ = os.WriteFile(filepath.Join(data, fmt.Sprintf("terminated-%d", os.Getpid())), []byte("term"), 0600)
				os.Exit(0)
			}
		case <-time.After(5 * time.Millisecond):
		}
		if !descendant {
			if _, e := os.Stat(filepath.Join(data, "exit")); e == nil {
				os.Exit(0)
			}
		}
	}
}
func childGone(pid int) bool {
	if syscall.Kill(pid, 0) == syscall.ESRCH {
		return true
	}
	// Linux container init may retain an orphan zombie after SIGKILL. A zombie
	// cannot hold ports, execute code or receive signals; the owned leader is reaped.
	if b, e := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); e == nil {
		end := strings.LastIndex(string(b), ")")
		if end >= 0 && strings.HasPrefix(string(b[end+1:]), " Z") {
			return true
		}
	}
	return false
}
func waitGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if childGone(pid) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("child survived", pid)
}
func TestOwnerGroupCleanup(t *testing.T) {
	for _, mode := range []string{"graceful descendants", "forced descendants", "leader exits forced descendants"} {
		t.Run(mode, func(t *testing.T) {
			r, o := ownerFixture(t)
			o.node = writeNodeDouble(t, "v24.15.0", false)
			ownerBuild(t, r, testApp, "a", "1.0.0", mode)
			if e := o.Start(context.Background(), testApp); e != nil {
				t.Fatal(e)
			}
			data := filepath.Join(r.root, testApp, "data")
			leader, e := strconv.Atoi(string(waitFile(t, filepath.Join(data, "ready"))))
			if e != nil {
				t.Fatal(e)
			}
			desc, e := strconv.Atoi(string(waitFile(t, filepath.Join(data, "descendant"))))
			if e != nil {
				t.Fatal(e)
			}
			if strings.HasPrefix(mode, "leader exits") {
				if e = os.WriteFile(filepath.Join(data, "exit"), nil, 0600); e != nil {
					t.Fatal(e)
				}
				<-o.generations[testApp].done
				v := ownerRecord(t, r, testApp)
				if v.State != "failed" || v.LastError.Message != "Service exited unexpectedly" {
					t.Fatal(v)
				}
			} else {
				started := time.Now()
				if e = o.Stop(testApp); e != nil {
					t.Fatal(e)
				}
				if strings.HasPrefix(mode, "forced") && time.Since(started) < o.grace {
					t.Fatal("skipped grace")
				}
			}
			waitGone(t, leader)
			waitGone(t, desc)
			if strings.HasPrefix(mode, "graceful") {
				waitFile(t, filepath.Join(data, fmt.Sprintf("terminated-%d", desc)))
			}
		})
	}
}
