package supervisor

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"testing"
	"time"
)

// TestHelperProcess is not a real test. It is used as a fake child process by
// integration tests. The test binary re-execs itself with GO_TEST_HELPER_PROCESS=1,
// and the behavior is controlled by GO_TEST_HELPER_MODE:
//
//   - "exit0":     exit immediately with code 0
//   - "exit1":     exit immediately with code 1
//   - "sleep":     sleep for GO_TEST_HELPER_SLEEP duration, then exit 0
//   - "crash":     exit immediately with code 2 (simulates crash)
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_TEST_HELPER_PROCESS") != "1" {
		return
	}

	mode := os.Getenv("GO_TEST_HELPER_MODE")
	switch mode {
	case "exit0":
		os.Exit(0)
	case "exit1":
		os.Exit(1)
	case "crash":
		os.Exit(2)
	case "sleep":
		dur, err := time.ParseDuration(os.Getenv("GO_TEST_HELPER_SLEEP"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid GO_TEST_HELPER_SLEEP: %v\n", err)
			os.Exit(99)
		}
		time.Sleep(dur)
		os.Exit(0)
	case "sleep_then_crash":
		dur, err := time.ParseDuration(os.Getenv("GO_TEST_HELPER_SLEEP"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid GO_TEST_HELPER_SLEEP: %v\n", err)
			os.Exit(99)
		}
		time.Sleep(dur)
		os.Exit(1)
	case "count_exits":
		// Exit with code 0 the first N times, then block until killed.
		// Uses a file to track invocation count.
		countFile := os.Getenv("GO_TEST_HELPER_COUNT_FILE")
		if countFile == "" {
			os.Exit(99)
		}
		maxExits, _ := strconv.Atoi(os.Getenv("GO_TEST_HELPER_MAX_EXITS"))
		if maxExits == 0 {
			maxExits = 2
		}
		count := readCount(countFile)
		writeCount(countFile, count+1)
		if count < maxExits {
			os.Exit(0)
		}
		// Block until killed. Use sleep instead of select{} to avoid
		// Go's deadlock detector panicking in the child process.
		time.Sleep(24 * time.Hour)
	default:
		fmt.Fprintf(os.Stderr, "unknown GO_TEST_HELPER_MODE: %q\n", mode)
		os.Exit(99)
	}
}

func readCount(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(string(data))
	return n
}

func writeCount(path string, n int) {
	_ = os.WriteFile(path, []byte(strconv.Itoa(n)), 0644)
}

// helperConfig returns a Config that uses the test binary as the child process.
func helperConfig(mode string, env ...string) Config {
	allEnv := append([]string{
		"GO_TEST_HELPER_PROCESS=1",
		"GO_TEST_HELPER_MODE=" + mode,
	}, env...)

	return Config{
		ClaudeBin:      os.Args[0],
		ClaudeArgs:     []string{"-test.run=TestHelperProcess", "--"},
		ResumeLast:     false,
		Logger:         slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
		BackoffInitial: 10 * time.Millisecond,
		BackoffMax:     50 * time.Millisecond,
		BackoffReset:   100 * time.Millisecond,
		helperEnv:      allEnv,
	}
}

func TestSupervisor_ChildExitsCleanly(t *testing.T) {
	t.Parallel()

	cfg := helperConfig("count_exits")

	countFile := t.TempDir() + "/count"
	cfg.helperEnv = append(cfg.helperEnv,
		"GO_TEST_HELPER_COUNT_FILE="+countFile,
		"GO_TEST_HELPER_MAX_EXITS=1",
	)

	// Re-exec'ing the test binary as a PTY child has overhead (~200ms per
	// cycle: fork+exec+Go runtime init+test framework+helper+exit+PTY drain).
	// Give enough time for at least 2 full cycles.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sup, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// The child exits once (count_exits mode), then blocks on second run.
	// We cancel the context after enough time for the restart cycle.
	go func() {
		time.Sleep(3 * time.Second)
		cancel()
	}()

	err = sup.Run(ctx)
	if err != nil && !isContextErr(err) {
		t.Fatalf("Run: %v", err)
	}

	count := readCount(countFile)
	if count < 2 {
		t.Errorf("expected at least 2 child invocations, got %d", count)
	}
}

func TestSupervisor_RestartsAfterCrash(t *testing.T) {
	t.Parallel()

	cfg := helperConfig("count_exits")

	countFile := t.TempDir() + "/count"
	cfg.helperEnv = append(cfg.helperEnv,
		"GO_TEST_HELPER_COUNT_FILE="+countFile,
		"GO_TEST_HELPER_MAX_EXITS=3",
	)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	sup, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	go func() {
		time.Sleep(8 * time.Second)
		cancel()
	}()

	err = sup.Run(ctx)
	if err != nil && !isContextErr(err) {
		t.Fatalf("Run: %v", err)
	}

	count := readCount(countFile)
	if count < 3 {
		t.Errorf("expected at least 3 child invocations (crash + restart), got %d", count)
	}
}

func TestSupervisor_GracefulShutdown(t *testing.T) {
	t.Parallel()

	cfg := helperConfig("sleep",
		"GO_TEST_HELPER_SLEEP=10s",
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sup, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Cancel quickly to test graceful shutdown.
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	err = sup.Run(ctx)
	elapsed := time.Since(start)

	if err != nil && !isContextErr(err) {
		t.Fatalf("Run: %v", err)
	}

	// Should exit quickly after cancellation, not wait for the 10s sleep.
	if elapsed > 3*time.Second {
		t.Errorf("shutdown took %v, expected < 3s", elapsed)
	}
}

func isContextErr(err error) bool {
	return err == context.Canceled || err == context.DeadlineExceeded
}
