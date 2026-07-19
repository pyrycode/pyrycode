package streamsup

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestMain re-purposes the test binary as a fake headless claude when
// GO_STREAMSUP_HELPER=1 is set in its environment (the integration tests set it
// via Config.Env). It dispatches BEFORE flag.Parse so the fixed stream-json
// prefix flags the runner prepends (--input-format …, --output-format …,
// --verbose) never reach the test flag set — the child behaves as claude, not
// as `go test` (which would exit 2 on the unknown leading flags). Without the
// env var it runs the package's tests normally.
func TestMain(m *testing.M) {
	if os.Getenv("GO_STREAMSUP_HELPER") == "1" {
		helperChild() // always os.Exits
		return
	}
	os.Exit(m.Run())
}

// helperChild is the fake-claude entry point, keyed by GO_STREAMSUP_HELPER_MODE:
//
//   - "echo_lines":    write "READY", echo every stdin line back as
//                      "ECHO:<line>", and on stdin EOF write "GOT_EOF". Used to
//                      prove stdin is held open (the echo round-trips) and NOT
//                      closed after spawn (GOT_EOF stays absent while the runner
//                      keeps the child alive).
//   - "block_sigterm": install a SIGTERM handler that prints "got SIGTERM" to
//                      stderr and exits 0; otherwise drain stdin and block. Used
//                      by the teardown SIGTERM + descendant-reap tests.
//   - "crash":         append this spawn's argv to GO_STREAMSUP_HELPER_ARGV_FILE
//                      (if set), then exit 1 after a short delay. Used by the
//                      restart-on-crash and resume-id-stable tests to force the
//                      supervise loop to respawn.
func helperChild() {
	switch os.Getenv("GO_STREAMSUP_HELPER_MODE") {
	case "echo_lines":
		fmt.Fprintln(os.Stdout, "READY")
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			fmt.Fprintln(os.Stdout, "ECHO:"+sc.Text())
		}
		fmt.Fprintln(os.Stdout, "GOT_EOF")
		os.Exit(0)
	case "block_sigterm":
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM)
		go func() { _, _ = io.Copy(io.Discard, os.Stdin) }()
		select {
		case <-sigCh:
			fmt.Fprintln(os.Stderr, "got SIGTERM")
			os.Exit(0)
		case <-time.After(30 * time.Second):
			os.Exit(0)
		}
	case "crash":
		if path := os.Getenv("GO_STREAMSUP_HELPER_ARGV_FILE"); path != "" {
			if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
				fmt.Fprintln(f, strings.Join(os.Args, " "))
				_ = f.Sync()
				_ = f.Close()
			}
		}
		time.Sleep(20 * time.Millisecond)
		os.Exit(1)
	default:
		fmt.Fprintf(os.Stderr, "unknown GO_STREAMSUP_HELPER_MODE: %q\n", os.Getenv("GO_STREAMSUP_HELPER_MODE"))
		os.Exit(99)
	}
}
