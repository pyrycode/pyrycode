package agentrun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestDriveHelperProcess is the fake-claude entry point for Drive's tests.
// The test binary re-execs itself with GO_DRIVE_HELPER=1 and behaviour
// keyed by GO_DRIVE_HELPER_MODE:
//
//   - "capture":   read stdin (PTY slave) into GO_DRIVE_HELPER_STDIN_FILE
//                  for GO_DRIVE_HELPER_LIFETIME then exit 0. Used by the
//                  drive-sequence tests; the lifetime gives both scripted
//                  writes time to land.
//   - "exit1":     after a brief settle, exit with code 1. Used to verify
//                  Drive surfaces *exec.ExitError on non-zero child exit.
//   - "blast":     write GO_DRIVE_HELPER_BLAST_BYTES to stdout then exit.
//                  Used to verify the background drain prevents blocking.
//   - "fast_exit": exit 0 immediately. Used to verify Drive tolerates
//                  PTY write errors when the child has already exited.
func TestDriveHelperProcess(t *testing.T) {
	if os.Getenv("GO_DRIVE_HELPER") != "1" {
		return
	}
	switch os.Getenv("GO_DRIVE_HELPER_MODE") {
	case "capture":
		path := os.Getenv("GO_DRIVE_HELPER_STDIN_FILE")
		if path == "" {
			fmt.Fprintln(os.Stderr, "capture: GO_DRIVE_HELPER_STDIN_FILE unset")
			os.Exit(99)
		}
		lifetime, err := time.ParseDuration(os.Getenv("GO_DRIVE_HELPER_LIFETIME"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "capture: invalid GO_DRIVE_HELPER_LIFETIME: %v\n", err)
			os.Exit(99)
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			fmt.Fprintf(os.Stderr, "capture: open: %v\n", err)
			os.Exit(99)
		}
		done := make(chan struct{})
		go func() {
			_, _ = io.Copy(f, os.Stdin)
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(lifetime):
		}
		_ = f.Sync()
		_ = f.Close()
		os.Exit(0)
	case "exit1":
		// Read whatever's queued briefly so the parent's PTY writes don't
		// fail with EIO before the parent even finishes its sequence.
		go func() { _, _ = io.Copy(io.Discard, os.Stdin) }()
		time.Sleep(200 * time.Millisecond)
		os.Exit(1)
	case "blast":
		go func() { _, _ = io.Copy(io.Discard, os.Stdin) }()
		// Write a large payload to force the drain path.
		n := 256 * 1024 // 256 KiB — comfortably past the kernel PTY buffer.
		buf := bytes.Repeat([]byte("x"), 4096)
		for written := 0; written < n; written += len(buf) {
			if _, err := os.Stdout.Write(buf); err != nil {
				fmt.Fprintf(os.Stderr, "blast: write: %v\n", err)
				os.Exit(99)
			}
		}
		os.Exit(0)
	case "fast_exit":
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown GO_DRIVE_HELPER_MODE: %q\n", os.Getenv("GO_DRIVE_HELPER_MODE"))
		os.Exit(99)
	}
}

// helperDriveCfg returns a DriveConfig wired to TestDriveHelperProcess.
// Tests override TrustDialogDelay / PromptDelay / PromptBytes / Env to
// taste; the function provides only the shared scaffold.
func helperDriveCfg(t *testing.T, mode string, extraEnv ...string) DriveConfig {
	t.Helper()
	env := append([]string{
		"GO_DRIVE_HELPER=1",
		"GO_DRIVE_HELPER_MODE=" + mode,
	}, extraEnv...)
	return DriveConfig{
		ClaudeBin: os.Args[0],
		WorkDir:   t.TempDir(),
		Args: []string{
			"-test.run=TestDriveHelperProcess",
			"--",
		},
		Env: env,
	}
}

func TestDrive_HappyPathDriveSequence(t *testing.T) {
	t.Parallel()
	capturePath := t.TempDir() + "/captured"
	cfg := helperDriveCfg(t, "capture",
		"GO_DRIVE_HELPER_STDIN_FILE="+capturePath,
		"GO_DRIVE_HELPER_LIFETIME=400ms",
	)
	cfg.TrustDialogDelay = 50 * time.Millisecond
	cfg.PromptDelay = 50 * time.Millisecond
	cfg.PromptBytes = []byte("ping")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := Drive(ctx, cfg); err != nil {
		t.Fatalf("Drive: %v", err)
	}

	got, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	// The kernel PTY line discipline translates input CR to NL by default
	// (ONLCR / ICRNL are on). The exact byte sequence the child reads is
	// "\nping\n", not "\rping\r". Assert against that — it's what claude's
	// TUI would see in production too.
	want := []byte("\nping\n")
	if !bytes.Equal(got, want) {
		t.Errorf("captured stdin:\n got  = %q\n want = %q", got, want)
	}
}

// TestDrive_BackgroundDrainPreventsBlock verifies the drain goroutine
// consumes the child's output so the child does not block on a full PTY
// buffer. Without the drain, this test would hang (the kernel buffer fills
// at ~16-64 KiB and the child's write blocks; Drive's Wait blocks; deadlock).
func TestDrive_BackgroundDrainPreventsBlock(t *testing.T) {
	t.Parallel()
	cfg := helperDriveCfg(t, "blast")
	cfg.TrustDialogDelay = 50 * time.Millisecond
	cfg.PromptDelay = 50 * time.Millisecond
	cfg.PromptBytes = []byte("noop")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- Drive(ctx, cfg) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Drive: %v", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("Drive blocked — drain goroutine likely missing")
	}
}

func TestDrive_CtxCancelDuringTrustSleep(t *testing.T) {
	t.Parallel()
	capturePath := t.TempDir() + "/captured"
	cfg := helperDriveCfg(t, "capture",
		"GO_DRIVE_HELPER_STDIN_FILE="+capturePath,
		"GO_DRIVE_HELPER_LIFETIME=2s",
	)
	cfg.TrustDialogDelay = 500 * time.Millisecond
	cfg.PromptDelay = 500 * time.Millisecond
	cfg.PromptBytes = []byte("never-sent")

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond) // well inside TrustDialogDelay
		cancel()
	}()

	// Operator-driven cancel is mapped to nil at the verb level.
	if err := Drive(ctx, cfg); err != nil {
		t.Fatalf("Drive after ctx cancel: %v, want nil", err)
	}

	got, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("captured %d bytes after early cancel, want 0; got = %q", len(got), got)
	}
}

func TestDrive_CtxCancelBetweenWrites(t *testing.T) {
	t.Parallel()
	capturePath := t.TempDir() + "/captured"
	cfg := helperDriveCfg(t, "capture",
		"GO_DRIVE_HELPER_STDIN_FILE="+capturePath,
		"GO_DRIVE_HELPER_LIFETIME=2s",
	)
	cfg.TrustDialogDelay = 50 * time.Millisecond
	cfg.PromptDelay = 500 * time.Millisecond
	cfg.PromptBytes = []byte("never-sent")

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// Cancel after the trust write but well before the prompt write.
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()

	if err := Drive(ctx, cfg); err != nil {
		t.Fatalf("Drive after mid-cancel: %v, want nil", err)
	}

	got, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	// PTY line discipline maps CR → LF on input.
	if !bytes.Equal(got, []byte("\n")) {
		t.Errorf("captured = %q, want %q (single trust-dialog CR, no prompt)", got, []byte("\n"))
	}
}

func TestDrive_ChildExitsNonZero(t *testing.T) {
	t.Parallel()
	cfg := helperDriveCfg(t, "exit1")
	cfg.TrustDialogDelay = 50 * time.Millisecond
	cfg.PromptDelay = 50 * time.Millisecond
	cfg.PromptBytes = []byte("noop")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := Drive(ctx, cfg)
	if err == nil {
		t.Fatal("Drive: got nil, want non-nil error from exit-1 child")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("Drive: err = %v (%T), want *exec.ExitError", err, err)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("ExitCode = %d, want 1", exitErr.ExitCode())
	}
}

// TestDrive_TolerantOfPTYWriteError verifies a write failure (child already
// exited) does NOT propagate from Drive — the eventual Wait surfaces the
// child's exit, and that is the primary error operators care about.
func TestDrive_TolerantOfPTYWriteError(t *testing.T) {
	t.Parallel()
	cfg := helperDriveCfg(t, "fast_exit")
	// Sleep long enough that the child has definitely exited before our
	// scripted writes run; the write should race-lose to the PTY closure.
	cfg.TrustDialogDelay = 300 * time.Millisecond
	cfg.PromptDelay = 50 * time.Millisecond
	cfg.PromptBytes = []byte("noop")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// fast_exit child returns 0; even if writes EIO, Drive should swallow
	// the warning and return the child's clean exit.
	if err := Drive(ctx, cfg); err != nil {
		t.Fatalf("Drive: %v, want nil after fast_exit child", err)
	}
}
