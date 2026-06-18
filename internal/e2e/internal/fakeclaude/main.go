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
//	PYRY_FAKE_CLAUDE_JSONL_TRIGGER  optional path watched in parallel with
//	                               the others. When the file appears, its
//	                               contents (claude-format JSONL lines) are
//	                               appended verbatim to the live session
//	                               JSONL, fsynced, then the trigger is
//	                               removed. Used by the structured-receive
//	                               e2e (#642) to feed real turn events into
//	                               the daemon's structured-turn producer,
//	                               which tails this file. Default off — when
//	                               unset, no watch.
//	PYRY_FAKE_CLAUDE_TUI           optional; when set to any non-empty
//	                               value, fakeclaude emits claude's idle-
//	                               prompt glyph (U+276F) once at startup and
//	                               its thinking-spinner glyph (U+273B) once
//	                               on the first stdin bytes, so tui-driver's
//	                               IsIdle/IsThinking detection — and the #594
//	                               WaitReady→DeliverPrompt→commit contract —
//	                               can confirm a turn against fakeclaude.
//	                               Default off — when unset, fakeclaude emits
//	                               no TUI substrate and is byte-identical to
//	                               its pre-#603 behaviour.
//
// The binary lives under internal/e2e/internal/ to visibility-fence it from
// non-e2e callers. Because TUI mode makes this file carry claude-TUI
// substrate glyphs, it is on the cmd/substrate-guard allowlist (#603),
// mirroring the sanctioned internal/agentrun/ptyrunner/helper_test.go
// exemption.
package main

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

const (
	envSessionsDir       = "PYRY_FAKE_CLAUDE_SESSIONS_DIR"
	envInitialUUID       = "PYRY_FAKE_CLAUDE_INITIAL_UUID"
	envTrigger           = "PYRY_FAKE_CLAUDE_TRIGGER"
	envStdinLog          = "PYRY_FAKE_CLAUDE_STDIN_LOG"
	envAssistantTrigger  = "PYRY_FAKE_CLAUDE_ASSISTANT_TRIGGER"
	envJSONLTrigger      = "PYRY_FAKE_CLAUDE_JSONL_TRIGGER"
	envTUI               = "PYRY_FAKE_CLAUDE_TUI"
	assistantMaxBytes    = 64 * 1024
	pollInterval         = 50 * time.Millisecond
)

// idleGlyph and spinnerGlyph are claude's TUI substrate runes that
// tui-driver's IsIdle / IsThinking detect (U+276F at idle, U+273B while
// thinking). fakeclaude emits them only in TUI mode (envTUI) so the #594
// WaitReady->DeliverPrompt->commit contract can confirm a turn against it.
// They live here, not via a tui-driver import, to keep this stand-in
// zero-dependency; the file is on the cmd/substrate-guard allowlist because it
// carries these glyphs.
var (
	idleGlyph    = []byte("❯") // U+276F idle input prompt
	spinnerGlyph = []byte("✻") // U+273B thinking spinner
)

// stdoutMu serializes every write to os.Stdout. In TUI mode the main goroutine
// (startup idle glyph + emitAssistantIfTriggered) and the stdin goroutine
// (thinking spinner) both write os.Stdout; without serialization a spinner
// write could interleave mid-assistant-chunk and corrupt the marker the echo
// tests match on.
var stdoutMu sync.Mutex

// turnPending signals — from the stdin-reader goroutine to the main poll loop —
// that a user turn was delivered (stdin bytes arrived), so the main goroutine
// can grow the live session JSONL and let the daemon's #668 transcript-growth
// commit-confirm (confirmViaTranscriptGrowth) observe it. It is a signal only:
// the stdin reader never writes f, preserving the single-writer-of-f invariant;
// the main goroutine performs the actual append (appendTurnGrowth). Store/Swap
// are race-free, so no mutex on f is needed.
var turnPending atomic.Bool

// writeStdout writes p to os.Stdout under stdoutMu and fsyncs. Best-effort:
// errors are silenced, mirroring emitAssistantIfTriggered — the e2e asserts
// downstream (the phone receives the bytes), never on the write itself.
func writeStdout(p []byte) {
	stdoutMu.Lock()
	defer stdoutMu.Unlock()
	_, _ = os.Stdout.Write(p)
	_ = os.Stdout.Sync()
}

func main() {
	dir := mustEnv(envSessionsDir)
	initU := mustEnv(envInitialUUID)
	trig := mustEnv(envTrigger)

	tui := os.Getenv(envTUI) != ""

	// The stdin reader is the only stdin consumer (a second reader would race
	// it for bytes). It runs when a stdin-log path is configured OR TUI mode is
	// on — TUI mode needs stdin read independently of logging so the spinner
	// fires even if STDIN_LOG is unset.
	logPath := os.Getenv(envStdinLog)
	if logPath != "" || tui {
		startStdinReader(logPath, tui)
	}

	asstTrig := os.Getenv(envAssistantTrigger)
	jsonlTrig := os.Getenv(envJSONLTrigger)

	f := openSession(dir, initU)

	// TUI mode: seed the idle-prompt glyph once. tui-driver's rolling snapshot
	// buffer holds the single write, so IsIdle stays true until the spinner
	// lands — no continuous redraw needed (each restored flow drives exactly
	// one idle->thinking transition).
	if tui {
		writeStdout(idleGlyph)
	}

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
		// A delivered turn (stdin bytes, signalled by the reader) grows the live
		// session JSONL so the daemon's #668 commit-confirm observes growth and
		// acks. Swap collapses chunked stdin into one append per poll cycle and
		// re-arms for a later turn. Targets the current f (post-rotate).
		if turnPending.Swap(false) {
			appendTurnGrowth(f)
		}
		if asstTrig != "" {
			emitAssistantIfTriggered(asstTrig)
		}
		if jsonlTrig != "" {
			emitStructuredJSONLIfTriggered(f, jsonlTrig)
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
	writeStdout(data)
	_ = os.Remove(path)
}

// emitStructuredJSONLIfTriggered checks for the structured-JSONL trigger
// file. When present, reads its contents (capped at assistantMaxBytes) and
// appends them verbatim to f — the live session JSONL the daemon's
// structured-turn producer (cmd/pyry/interactive_turn_stream_v2.go) tails —
// then fsyncs and removes the trigger. The trigger file's contents ARE the
// claude-format JSONL lines to append (same "contents are the payload" shape
// as emitAssistantIfTriggered). The f.Sync() is load-bearing: the daemon's
// tail is a separate process and macOS APFS otherwise defers cross-process
// visibility (mirrors the stdin reader's per-write fsync). Errors are
// silenced — the e2e asserts downstream (the interactive phone receives the
// structured envelopes), and a missing trigger is the steady state. Only the
// main goroutine writes f, so this never races the stdin reader.
func emitStructuredJSONLIfTriggered(f *os.File, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if len(data) > assistantMaxBytes {
		data = data[:assistantMaxBytes]
	}
	if _, err := f.Write(data); err != nil {
		return
	}
	_ = f.Sync()
	_ = os.Remove(path)
}

// appendTurnGrowth grows the current session JSONL f by one inert line so the
// daemon's #668 transcript-growth commit-confirm (confirmViaTranscriptGrowth)
// observes growth past its pre-delivery baseline and acks the delivered turn —
// the same signal real claude produces when it commits a turn to its session
// JSONL. Runs ONLY on the main goroutine, preserving the single-writer-of-f
// invariant (see turnPending). The line is the inert "{}\n" openSession already
// writes: the turnbridge mapper maps an empty/typeless line to (nil, false), so
// it injects no structured event — invisible to every assertion except "the
// file grew". Best-effort, mirroring emitStructuredJSONLIfTriggered; a
// persistently-failing write surfaces as the daemon's loud ErrTurnNotCommitted,
// never a false ack.
func appendTurnGrowth(f *os.File) {
	if _, err := f.WriteString("{}\n"); err != nil {
		return
	}
	_ = f.Sync()
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

// startStdinReader starts the single stdin-consuming goroutine. When logPath
// is non-empty it appends every byte read from os.Stdin to that file, fsyncing
// after each write so a sibling test process polling the file sees bytes
// promptly (macOS APFS otherwise defers cross-process visibility). When tui is
// true it emits the thinking-spinner glyph once on the first stdin bytes —
// claude's "prompt received, turn started" signal, which tui-driver IsThinking
// reads as the DeliverPrompt commit confirmation. It never echoes stdin
// content to os.Stdout: TUI mode writes only the fixed spinner glyph, never the
// phone-controlled prompt bytes.
func startStdinReader(logPath string, tui bool) {
	var logF *os.File
	if logPath != "" {
		f, err := os.OpenFile(logPath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
		if err != nil {
			fatalf("open stdin log %s: %v", logPath, err)
		}
		logF = f
	}
	go func() {
		buf := make([]byte, 4096)
		spinnerEmitted := false
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				// Signal the main goroutine that a turn was delivered so it can
				// grow the live session JSONL (appendTurnGrowth). Signal only:
				// this goroutine must never write f (single-writer-of-f).
				turnPending.Store(true)
				if logF != nil {
					if _, werr := logF.Write(buf[:n]); werr != nil {
						return
					}
					if serr := logF.Sync(); serr != nil {
						return
					}
				}
				if tui && !spinnerEmitted {
					writeStdout(spinnerGlyph)
					spinnerEmitted = true
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
