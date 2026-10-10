package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func (s memoryCredentialStore) platform() string {
	if s.goos != "" {
		return s.goos
	}
	return runtime.GOOS
}
func memoryOSName(generation string) string { return "pyry.memory.openai." + generation }

// memoryOSCommand never returns tool diagnostics. Only successful read stdout
// crosses the boundary, bounded and locally validated by memoryOSRead.
func memoryOSCommand(ctx context.Context, tool string, args []string, input io.Reader, read bool) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, memoryCredentialDeadline)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var output cappedBuffer
	cmd := exec.CommandContext(ctx, tool, args...)
	cmd.Stdin = input
	if read {
		cmd.Stdout = &output
	}
	cmd.Env = withoutAccountToken(os.Environ())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = opWaitDelay
	if err := cmd.Start(); err != nil {
		return nil, errMemoryStorage
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			return nil, errMemoryStorage
		}
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return output.Bytes(), nil
}
func memoryOSWrite(ctx context.Context, backend, generation string, secret io.Reader) error {
	_, err := memoryOSCommand(ctx, "python3", memoryOSArgs(backend, "write", generation), secret, false)
	return err
}
func memoryOSArgs(backend, operation, generation string) []string {
	return []string{"-I", "-c", memoryOSScript, backend, operation, memoryOSName(generation)}
}
func memoryOSRead(ctx context.Context, backend, generation string) (string, error) {
	b, err := memoryOSCommand(ctx, "python3", memoryOSArgs(backend, "read", generation), nil, true)
	if err != nil {
		return "", err
	}
	if len(b) > maxAccountTokenBytes {
		return "", errMemorySelection
	}
	token, failure := parseTokenBytes(b)
	if failure != "" || token != string(b) {
		return "", errMemorySelection
	}
	return token, nil
}
func memoryOSDelete(ctx context.Context, backend, generation string) error {
	_, err := memoryOSCommand(ctx, "python3", memoryOSArgs(backend, "delete", generation), nil, false)
	return err
}

// preferred probes only a disposable memory item, never a user's new token.
func (s memoryCredentialStore) preferred(ctx context.Context) (string, error) {
	backend := "file"
	switch s.platform() {
	case "darwin":
		backend = "keychain"
	case "linux":
		backend = "secret-service"
	default:
		return backend, ctx.Err()
	}
	generation, err := memoryID()
	if err != nil {
		return "", err
	}
	probe, err := memoryID()
	if err != nil {
		return "", err
	}
	defer func() { _ = memoryOSDelete(ctx, backend, generation) }() // best-effort removal of an unselected probe
	if err = memoryOSWrite(ctx, backend, generation, strings.NewReader(probe)); err == nil {
		var got string
		got, err = memoryOSRead(ctx, backend, generation)
		if err == nil && got != probe {
			err = errMemorySelection
		}
		if err == nil {
			err = memoryOSDelete(ctx, backend, generation)
		}
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err == context.Canceled || err == context.DeadlineExceeded {
		return "", err
	}
	if err != nil {
		return "file", nil
	}
	return backend, nil
}
func (s memoryCredentialStore) readSelected(ctx context.Context, sel memoryCredentialSelection, fileToken string) (string, error) {
	if sel.Backend == "file" || sel.Reference == "" {
		return fileToken, nil
	}
	if (sel.Backend == "keychain" && s.platform() != "darwin") || (sel.Backend == "secret-service" && s.platform() != "linux") {
		return "", errMemoryStorage
	}
	return memoryOSRead(ctx, sel.Backend, sel.Generation)
}
func memoryRemoveGeneration(ctx context.Context, dir *os.File, backend, generation string) {
	if generation == "" {
		return
	}
	if backend == "file" {
		_ = unix.Unlinkat(int(dir.Fd()), generation+".token", 0)
	} else {
		_ = memoryOSDelete(ctx, backend, generation)
	} // best-effort cleanup of unselected data
}
