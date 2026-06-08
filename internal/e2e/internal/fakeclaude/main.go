// Command fakeclaude is a test-only stand-in for the real `claude` CLI used
// by Pyrycode's e2e harness. It opens a JSONL session file under a
// configured sessions directory, then watches a trigger file: on first
// appearance it closes the original fd, opens a fresh <uuid>.jsonl in the
// same directory, removes the trigger, and idles forever. Subsequent
// triggers are ignored. The strict close-OLD-before-open-NEW order is what
// makes the consumer ticket's exact-match probe check deterministic.
//
// Configuration is env-only:
//
//	PYRY_FAKE_CLAUDE_SESSIONS_DIR  directory that must already exist
//	PYRY_FAKE_CLAUDE_INITIAL_UUID  stem for the first <uuid>.jsonl
//	PYRY_FAKE_CLAUDE_TRIGGER       path watched for the rotation signal
//	PYRY_FAKE_CLAUDE_STDIN_LOG     optional filesystem path; when set,
//	                               every byte read from os.Stdin is
//	                               appended (with fsync per write) so the
//	                               e2e harness can observe what the
//	                               supervisor wrote to the PTY. Default
//	                               off — when unset, stdin is not read.
//	PYRY_FAKE_CLAUDE_ASSISTANT_TRIGGER  optional path watched in parallel
//	                               with PYRY_FAKE_CLAUDE_TRIGGER. When the
//	                               file appears, its contents are written
//	                               to os.Stdout (the supervisor's PTY),
//	                               then the trigger is removed. Used by
//	                               the assistant-turn e2e (#311) to script
//	                               a scripted assistant chunk on demand.
//	                               Default off — when unset, no watch.
//
// The binary lives under internal/e2e/internal/ to visibility-fence it from
// non-e2e callers.
package main

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	envSessionsDir       = "PYRY_FAKE_CLAUDE_SESSIONS_DIR"
	envInitialUUID       = "PYRY_FAKE_CLAUDE_INITIAL_UUID"
	envTrigger           = "PYRY_FAKE_CLAUDE_TRIGGER"
	envStdinLog          = "PYRY_FAKE_CLAUDE_STDIN_LOG"
	envAssistantTrigger  = "PYRY_FAKE_CLAUDE_ASSISTANT_TRIGGER"
	assistantMaxBytes    = 64 * 1024
	pollInterval         = 50 * time.Millisecond
)

func main() {
	dir := mustEnv(envSessionsDir)
	initU := mustEnv(envInitialUUID)
	trig := mustEnv(envTrigger)

	fmt.Print("\u276f ")
	startStdinConsumer(os.Getenv(envStdinLog))

	asstTrig := os.Getenv(envAssistantTrigger)

	f := openSession(dir, initU)
	rotated := false
	for {
		if !rotated {
			if _, err := os.Stat(trig); err == nil {
				_ = f.Close()
				newU := uuidV4()
				f = openSession(dir, newU)
				_ = os.Remove(trig)
				rotated = true
			}
		}
		if asstTrig != "" {
			emitAssistantIfTriggered(asstTrig)
		}
		time.Sleep(pollInterval)
	}
}

// emitAssistantIfTriggered checks for the assistant-trigger file. When
// present, reads its contents (capped at assistantMaxBytes), writes them
// to os.Stdout, and removes the trigger. Repeat-firing is supported —
// callers can drop the trigger multiple times to script multiple
// assistant chunks. Errors are silenced; the e2e asserts on the
// downstream side (the phone receives the message) and a missing trigger
// is the steady state.
func emitAssistantIfTriggered(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if len(data) > assistantMaxBytes {
		data = data[:assistantMaxBytes]
	}
	_, _ = os.Stdout.Write(data)
	_ = os.Stdout.Sync()
	_ = os.Remove(path)
}

func openSession(dir, uuid string) *os.File {
	path := filepath.Join(dir, uuid+".jsonl")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		fatalf("open %s: %v", path, err)
	}
	if _, err := f.WriteString("{}\n"); err != nil {
		fatalf("write %s: %v", path, err)
	}
	if err := f.Sync(); err != nil {
		fatalf("fsync %s: %v", path, err)
	}
	return f
}

// startStdinConsumer reads from stdin to detect when a turn arrives, emitting the
// thinking spinner so DeliverPrompt confirms a commit. If path is non-empty, it
// also fsyncs every byte read so the e2e harness can observe the PTY.
func startStdinConsumer(path string) {
	var f *os.File
	if path != "" {
		var err error
		f, err = os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
		if err != nil {
			fatalf("open stdin log %s: %v", path, err)
		}
	}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				fmt.Print("\u273b ")
				if f != nil {
					if _, werr := f.Write(buf[:n]); werr != nil {
						return
					}
					if serr := f.Sync(); serr != nil {
						return
					}
				}
			}
			if err != nil {
				return
			}
		}
	}()
}

func uuidV4() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		fatalf("rand: %v", err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		fatalf("missing env %s", k)
	}
	return v
}

func fatalf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "fakeclaude: "+format+"\n", a...)
	os.Exit(1)
}
