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
	envSessionsDir = "PYRY_FAKE_CLAUDE_SESSIONS_DIR"
	envInitialUUID = "PYRY_FAKE_CLAUDE_INITIAL_UUID"
	envTrigger     = "PYRY_FAKE_CLAUDE_TRIGGER"
	pollInterval   = 50 * time.Millisecond
)

func main() {
	dir := mustEnv(envSessionsDir)
	initU := mustEnv(envInitialUUID)
	trig := mustEnv(envTrigger)

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
		time.Sleep(pollInterval)
	}
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
