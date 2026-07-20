package streamsup

import (
	"bufio"
	"encoding/json"
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
//   - "record_block":  append this spawn's argv to GO_STREAMSUP_HELPER_ARGV_FILE
//                      (like "crash"), then block until SIGTERM and exit 0 —
//                      staying alive so a live Restart must KILL it (it never
//                      self-exits). Used by the live-restart test to prove the
//                      first child was terminated by Restart, not by its own exit.
//   - "stream_json":   read newline-delimited user-turn envelopes from the
//                      held-open stdin; for each one, decode its prompt text and
//                      emit a canned stream-json turn (system/init → assistant
//                      text echoing the prompt → result/success). The session_id
//                      stays constant across turns while init repeats per turn
//                      (spike § 1). Used by the turn-I/O round-trip test.
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
	case "record_block":
		if path := os.Getenv("GO_STREAMSUP_HELPER_ARGV_FILE"); path != "" {
			if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
				fmt.Fprintln(f, strings.Join(os.Args, " "))
				_ = f.Sync()
				_ = f.Close()
			}
		}
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM)
		go func() { _, _ = io.Copy(io.Discard, os.Stdin) }()
		select {
		case <-sigCh:
			os.Exit(0)
		case <-time.After(30 * time.Second):
			os.Exit(0)
		}
	case "stream_json":
		sc := bufio.NewScanner(os.Stdin)
		sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
		for sc.Scan() {
			// Decode the prompt text out of the user-turn envelope so the
			// emitted assistant text echoes it back — the round-trip test uses
			// distinct per-turn markers to assert attribution.
			var env struct {
				Message struct {
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"message"`
			}
			marker := ""
			if err := json.Unmarshal(sc.Bytes(), &env); err == nil && len(env.Message.Content) > 0 {
				marker = env.Message.Content[0].Text
			}
			// init fires once per turn (spike § 1: NOT a session-open marker).
			fmt.Fprintln(os.Stdout, `{"type":"system","subtype":"init","session_id":"S"}`)
			asst, _ := json.Marshal(map[string]any{
				"type": "assistant",
				"message": map[string]any{
					"id":      "msg",
					"role":    "assistant",
					"content": []map[string]any{{"type": "text", "text": marker}},
				},
			})
			os.Stdout.Write(append(asst, '\n'))
			fmt.Fprintln(os.Stdout, `{"type":"result","subtype":"success","session_id":"S"}`)
		}
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown GO_STREAMSUP_HELPER_MODE: %q\n", os.Getenv("GO_STREAMSUP_HELPER_MODE"))
		os.Exit(99)
	}
}
