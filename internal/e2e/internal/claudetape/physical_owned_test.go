package claudetape

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func physicalControlHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if os.Chmod(home, 0700) != nil || os.Mkdir(filepath.Join(home, "work"), 0700) != nil {
		t.Fatal("private physical home")
	}
	return home
}
func physicalControlFIFO(t *testing.T, home, name string) (<-chan struct{}, func()) {
	t.Helper()
	path := filepath.Join(home, "work", name)
	if syscall.Mkfifo(path, 0600) != nil {
		t.Fatal("original rig FIFO")
	}
	opened := make(chan *os.File, 1)
	arrived := make(chan struct{})
	go func() {
		file, err := os.OpenFile(path, os.O_WRONLY, 0600)
		if err != nil {
			opened <- nil
			return
		}
		opened <- file
		close(arrived)
	}()
	var once sync.Once
	return arrived, func() {
		once.Do(func() {
			select {
			case file := <-opened:
				if file == nil {
					t.Fatal("real FIFO reader missing")
				}
				if file.Close() != nil {
					t.Fatal("release actual writer")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("no actual FIFO arrival")
			}
		})
	}
}
func physicalControlInput(command string, background bool) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"command": command, "run_in_background": background})
	return raw
}
func physicalControlWaitFile(t *testing.T, path string) []byte {
	t.Helper()
	return physicalControlReadWitness(t, path, os.ReadFile)
}
func physicalControlReadWitness(t *testing.T, path string, readFile func(string) ([]byte, error)) []byte {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := readFile(path); err == nil {
			return raw
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("actual operation never produced witness")
	return nil
}
func TestOwnedPhysicalOperationExecutesOriginalHoldAndActuallyStops(t *testing.T) {
	home := physicalControlHome(t)
	arrived, release := physicalControlFIFO(t, home, "stop-task.fifo")
	defer release()
	s, _ := physicalSpec("stop-hold")
	script := strings.ReplaceAll(s.Script, physicalHome, home)
	if os.WriteFile(filepath.Join(home, "work", "stop-hold"), []byte(script), 0700) != nil {
		t.Fatal("original rig script")
	}
	inspected, err := InspectPhysicalOperation(home, "stop-hold")
	if err != nil || inspected != s {
		t.Fatal("input not captured from original current rig", err)
	}
	op, err := StartPhysicalOperation(home, s, physicalControlInput(physicalQuote(filepath.Join(home, "work", "stop-hold")), true))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := op.Close(); err != nil {
			t.Error(err)
		}
	}()
	if string(physicalControlWaitFile(t, filepath.Join(home, "work", "stop-hold.executed"))) != "x" || !op.Running() {
		t.Fatal("captured text substituted for actual script execution")
	}
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("real FIFO reader never arrived")
	}
	status, err := op.Stop()
	if err != nil || status != (FallbackWorkerStatus{ExitCode: -1, Signal: 9}) || syscall.Kill(op.CurrentPID(), 0) != syscall.ESRCH {
		t.Fatal("actual owned hold not killed/reaped", err)
	}
	if _, err := op.Stop(); err == nil {
		t.Fatal("reused an already reaped PID")
	}
}
func TestOwnedPhysicalComposerStopPreservesRealBackgroundUntilRelease(t *testing.T) {
	home := physicalControlHome(t)
	arrived, release := physicalControlFIFO(t, home, "composer-background.fifo")
	background, _ := InspectPhysicalOperation(home, "composer-background")
	bg, err := StartPhysicalOperation(home, background, physicalControlInput("cat "+filepath.Join(home, "work", "composer-background.fifo"), true))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := bg.Close(); err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("real background FIFO reader never arrived")
	}
	foreground, _ := InspectPhysicalOperation(home, "composer-foreground")
	command := `printf '%s\n' "$$" > ` + physicalQuote(filepath.Join(home, "work", "foreground-started")) + `; while [ ! -f ` + physicalQuote(filepath.Join(home, "work", "foreground-released")) + ` ]; do sleep 0.1; done`
	fg, err := StartPhysicalOperation(home, foreground, physicalControlInput(command, false))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := fg.Close(); err != nil {
			t.Error(err)
		}
	}()
	raw := physicalControlWaitFile(t, filepath.Join(home, "work", "foreground-started"))
	pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	if pid != fg.CurrentPID() || fg.RequireForegroundWitness(home) != nil {
		t.Fatal("foreground PID was fabricated or belongs to another process")
	}
	if _, err := fg.Stop(); err != nil {
		t.Fatal(err)
	}
	if !bg.Running() {
		t.Fatal("foreground stop killed a different owned background group")
	}
	// Only this actual original writer's release lets the real cat complete.
	release()
	status, err := bg.Wait(5 * time.Second)
	if err != nil || status != (FallbackWorkerStatus{ExitCode: 0}) {
		t.Fatal("background did not actually complete after original release", err)
	}
}
func TestOwnedPhysicalOperationRejectsForeignInputWithoutExecuting(t *testing.T) {
	for _, kind := range []string{"command", "foreign-path", "script", "mode", "symlink", "existing-result", "posture", "unknown-input", "fifo", "descriptor"} {
		t.Run(kind, func(t *testing.T) {
			home := physicalControlHome(t)
			s, _ := physicalSpec("stop-hold")
			path := filepath.Join(home, "work", "stop-hold")
			if os.WriteFile(path, []byte(strings.ReplaceAll(s.Script, physicalHome, home)), 0700) != nil || syscall.Mkfifo(filepath.Join(home, "work", "stop-task.fifo"), 0600) != nil {
				t.Fatal("rig")
			}
			command := physicalQuote(path)
			background := true
			switch kind {
			case "command":
				command += "; touch " + filepath.Join(home, "executed-foreign")
			case "foreign-path":
				command = "cat /dev/null"
			case "script":
				if os.WriteFile(path, []byte("#!/bin/sh\ntouch "+filepath.Join(home, "executed-foreign")+"\n"), 0700) != nil {
					t.Fatal("mutate current source input")
				}
			case "mode":
				_ = os.Chmod(path, 0600)
			case "symlink":
				_ = os.Rename(path, path+"-real")
				_ = os.Symlink(path+"-real", path)
			case "existing-result":
				_ = os.WriteFile(filepath.Join(home, "work", "stop-hold.executed"), []byte("x"), 0600)
			case "posture":
				background = false
			case "fifo":
				_ = os.Rename(filepath.Join(home, "work", "stop-task.fifo"), filepath.Join(home, "work", "owned-old-fifo"))
				_ = os.WriteFile(filepath.Join(home, "work", "stop-task.fifo"), nil, 0600)
			case "descriptor":
				s.Script = "foreign script"
			}
			input := physicalControlInput(command, background)
			if kind == "unknown-input" {
				var v map[string]any
				_ = json.Unmarshal(input, &v)
				v["dangerouslyDisableSandbox"] = true
				input, _ = json.Marshal(v)
			}
			if op, err := StartPhysicalOperation(home, s, input); err == nil {
				_ = op.Close()
				t.Fatal("foreign command/input started")
			}
			if _, err := os.Stat(filepath.Join(home, "executed-foreign")); !os.IsNotExist(err) {
				t.Fatal("unapproved operation executed")
			}
		})
	}
}

func TestOwnedPhysicalPositivePathGrammarRejectsShellExpansion(t *testing.T) {
	for _, leaf := range []string{"wild*card", "bracket[xy]", "question?mark", "semi;touch foreign", "dollar$(touch foreign)"} {
		t.Run(leaf, func(t *testing.T) {
			home := filepath.Join(t.TempDir(), leaf)
			if os.Mkdir(home, 0700) != nil || os.Mkdir(filepath.Join(home, "work"), 0700) != nil {
				t.Fatal("private path")
			}
			stop, _ := physicalSpec("stop-hold")
			background, _ := physicalSpec("composer-background")
			if stop.CommandMatches(home, filepath.Join(home, "work", "stop-hold")) || background.CommandMatches(home, "cat "+filepath.Join(home, "work", "composer-background.fifo")) {
				t.Fatal("unquoted shell expansion accepted")
			}
		})
	}
}
func TestOwnedPhysicalGroupRemainsPinnedAfterOperationWait(t *testing.T) {
	home := physicalControlHome(t)
	arrived, release := physicalControlFIFO(t, home, "composer-background.fifo")
	spec, _ := InspectPhysicalOperation(home, "composer-background")
	op, err := StartPhysicalOperation(home, spec, physicalControlInput("cat "+filepath.Join(home, "work", "composer-background.fifo"), true))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := op.Close(); err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("physical reader missing")
	}
	release()
	if status, err := op.Wait(5 * time.Second); err != nil || status != (FallbackWorkerStatus{ExitCode: 0}) {
		t.Fatal("actual operation Wait", err)
	}
	if op.guard.ProcessState != nil || op.guard.Process.Signal(syscall.Signal(0)) != nil {
		t.Fatal("group owner reaped with operation PID")
	}
	// An independently test-owned member makes group draining non-vacuous after
	// the actual main operation has exited and been reaped.
	member := exec.Command("sleep", "30")
	member.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: op.guard.Process.Pid}
	if err := member.Start(); err != nil {
		t.Fatal(err)
	}
	if err := op.Close(); err != nil {
		_ = member.Process.Kill()
		_ = member.Wait()
		t.Fatal(err)
	}
	if err := member.Wait(); err == nil || member.ProcessState.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Fatal("owned group member survived cleanup")
	}
	if !op.retired || op.guard.ProcessState == nil {
		t.Fatal("guard not retired and actually reaped after drain")
	}
	if err := op.signalGroup(); err == nil {
		t.Fatal("delayed cancellation used retired group capability")
	}
}
func TestOwnedPhysicalForegroundWitnessRejectsReleaseAndForeignReads(t *testing.T) {
	for _, kind := range []string{"released", "foreign-home", "symlink", "fifo", "oversize"} {
		t.Run(kind, func(t *testing.T) {
			home := physicalControlHome(t)
			spec, _ := InspectPhysicalOperation(home, "composer-foreground")
			command := `printf '%s\n' "$$" > ` + physicalQuote(filepath.Join(home, "work", "foreground-started")) + `; while [ ! -f ` + physicalQuote(filepath.Join(home, "work", "foreground-released")) + ` ]; do sleep 0.1; done`
			op, err := StartPhysicalOperation(home, spec, physicalControlInput(command, false))
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := op.Close(); err != nil {
					t.Error(err)
				}
			}()
			path := filepath.Join(home, "work", "foreground-started")
			physicalControlWaitFile(t, path)
			inputHome := home
			switch kind {
			case "released":
				_ = os.WriteFile(filepath.Join(home, "work", "foreground-released"), nil, 0600)
			case "foreign-home":
				inputHome = t.TempDir()
			case "symlink":
				_ = os.Rename(path, path+"-original")
				_ = os.Symlink(path+"-original", path)
			case "fifo":
				_ = os.Rename(path, path+"-original")
				if syscall.Mkfifo(path, 0600) != nil {
					t.Fatal("witness mutation")
				}
			case "oversize":
				_ = os.WriteFile(path, []byte(strings.Repeat("1", 65)), 0600)
			}
			started := time.Now()
			if err := op.RequireForegroundWitness(inputHome); err == nil || time.Since(started) > time.Second {
				t.Fatal("released, foreign or unbounded witness accepted/read")
			}
		})
	}
}
