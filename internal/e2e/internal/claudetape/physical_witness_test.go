package claudetape

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestPhysicalForegroundWitnessWaitsForPublication(t *testing.T) {
	path := filepath.Join(physicalControlHome(t), "work", "foreground-started")
	readyR, readyW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { readyR.Close(); readyW.Close() })
	gateR, gateW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { gateR.Close(); gateW.Close() })
	// Opening the redirected descriptor precedes publication, just as it does
	// in the original foreground command. Pipes hold that boundary explicitly.
	cmd := exec.Command("sh", "-c", `exec 5>"$1"; printf ready >&3; IFS= read -r token <&4 || exit 1; printf '%s\n' "$$" >&5; IFS= read -r token <&4`, "witness", path)
	cmd.ExtraFiles = []*os.File{readyW, gateR}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill() // bounded cleanup of the test-owned shell
			_ = cmd.Wait()
		}
	})
	readyW.Close()
	gateR.Close()
	if err := readyR.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := gateW.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(readyR, make([]byte, len("ready"))); err != nil {
		t.Fatal("shell never opened its witness", err)
	}
	reads := 0
	raw := physicalControlReadWitness(t, path, func(path string) ([]byte, error) {
		raw, err := os.ReadFile(path)
		reads++
		if reads == 1 {
			if err != nil || len(raw) != 0 {
				t.Fatal("did not force the empty publication boundary", err)
			}
			if _, err := io.WriteString(gateW, "publish\n"); err != nil {
				t.Fatal(err)
			}
		}
		return raw, err
	}, func(raw []byte) bool { return string(raw) == fmt.Sprintf("%d\n", cmd.Process.Pid) })
	if string(raw) != fmt.Sprintf("%d\n", cmd.Process.Pid) || reads < 2 {
		t.Fatalf("foreground readiness returned incomplete witness %q after %d reads", raw, reads)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("shell completed without cancellation")
	}
	status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL || cmd.ProcessState.ExitCode() != -1 {
		t.Fatal("missing actual shell Wait9", cmd.ProcessState)
	}
}
