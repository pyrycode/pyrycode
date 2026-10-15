package apps

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const candidateVersionTimeout = 5 * time.Second

type candidateVersionBuffer struct{ bytes []byte }

// candidateRetirementError means a command's writers may still be live.
// Callers must preserve recovery evidence and must not replace their data.
type candidateRetirementError struct{ error }

func (e *candidateRetirementError) Unwrap() error { return e.error }

func (b *candidateVersionBuffer) Write(p []byte) (int, error) {
	if len(b.bytes) < 128 {
		b.bytes = append(b.bytes, p[:min(len(p), 128-len(b.bytes))]...)
	}
	return len(p), nil
}

// Each command owns a group even after its leader exits. No artifacts are read
// until the leader is joined and cleanup confirms that no live group work remains.
func candidateCommand(ctx context.Context, stage string, env []string, node string, args []string, version bool) (string, error) {
	return candidateCommandWithCleanup(ctx, stage, env, node, args, version, candidateCleanup)
}

func candidateCommandWithCleanup(ctx context.Context, stage string, env []string, node string, args []string, version bool, cleanup func(int) error) (string, error) {
	diagnostic := candidateDiagnostic{Command: candidateCommandName(args), ExitCode: -1}
	defer func() {
		if observe, ok := ctx.Value(candidateDiagnosticKey{}).(func(candidateDiagnostic)); ok {
			observe(diagnostic)
		}
	}()
	if err := ctx.Err(); err != nil {
		diagnostic.Cancel = err
		return "", err
	}
	cmd := exec.Command(node, args...)
	cmd.Dir = stage
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var output candidateVersionBuffer
	if version {
		cmd.Stdout = &output
		cmd.WaitDelay = 100 * time.Millisecond
	}
	if err := cmd.Start(); err != nil {
		diagnostic.Wait = err
		return "", err
	}
	diagnostic.PID = cmd.Process.Pid
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	var err error
	select {
	case err = <-waited:
	case <-ctx.Done():
		// Killing the group also closes inherited pipes held by descendants.
		killErr := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		err = errors.Join(ctx.Err(), <-waited)
		if killErr != nil && !errors.Is(killErr, syscall.ESRCH) {
			err = errors.Join(err, killErr)
		}
	}
	cleanupErr := cleanup(cmd.Process.Pid)
	diagnostic.ExitCode, diagnostic.Wait = cmd.ProcessState.ExitCode(), err
	diagnostic.Cleanup, diagnostic.Cancel = cleanupErr, ctx.Err()
	if cleanupErr != nil {
		cleanupErr = &candidateRetirementError{cleanupErr}
	}
	if err = errors.Join(err, cleanupErr, ctx.Err()); err != nil {
		return "", err
	}
	if len(output.bytes) == 128 {
		return "", ErrInvalidManifest
	}
	return strings.TrimSpace(string(output.bytes)), nil
}
func candidateCleanup(pid int) error {
	return candidateCleanupWithKill(pid, syscall.Kill)
}

func candidateCleanupWithKill(pid int, kill func(int, syscall.Signal) error) error {
	return candidateCleanupWithProbe(pid, kill, candidateGroupLive)
}

func candidateCleanupWithProbe(pid int, kill func(int, syscall.Signal) error, probe func(int) (bool, error)) error {
	var probeErr error
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL} {
		if err := kill(-pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("apps: signal process group: %w", err)
		}
		deadline := time.Now().Add(100 * time.Millisecond)
		if sig == syscall.SIGKILL {
			deadline = time.Now().Add(time.Second)
		}
		for time.Now().Before(deadline) {
			// Inspection errors leave retirement uncertain. Only a later positive
			// absence result can clear them, including after forced termination.
			live, err := probe(pid)
			probeErr = err
			if err != nil {
				probeErr = fmt.Errorf("apps: inspect process group: %w", err)
			} else if !live {
				return nil
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	return errors.Join(ErrUnavailable, probeErr)
}
func candidateGroupLive(pid int) (bool, error) {
	if runtime.GOOS != "linux" {
		err := syscall.Kill(-pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return false, nil
		}
		return err == nil, err
	}
	// Container init may retain orphan zombies indefinitely. They cannot execute
	// build work; live members, including descendants, must still be retired.
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if os.IsNotExist(err) || errors.Is(err, syscall.ESRCH) {
			continue
		}
		if err != nil {
			return false, err
		}
		end := strings.LastIndex(string(b), ") ")
		if end < 0 {
			return false, ErrInvalidStorage
		}
		fields := strings.Fields(string(b)[end+2:])
		if len(fields) < 3 {
			return false, ErrInvalidStorage
		}
		group, err := strconv.Atoi(fields[2])
		if err != nil {
			return false, err
		}
		if group == pid && fields[0] != "Z" && fields[0] != "X" {
			return true, nil
		}
	}
	return false, nil
}
