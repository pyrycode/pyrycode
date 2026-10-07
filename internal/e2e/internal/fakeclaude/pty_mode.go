package main

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"golang.org/x/term"
)

// turnPending signals — from the stdin-reader goroutine to the main poll loop —
// that a user turn was delivered (stdin bytes arrived), so the main goroutine
// can grow the live session JSONL and let the daemon's #668 transcript-growth
// commit-confirm (confirmViaTranscriptGrowth) observe it. It is a signal only:
// the stdin reader never writes f, preserving the single-writer-of-f invariant;
// the main goroutine performs the actual append (appendTurnGrowth). Store/Swap
// are race-free, so no mutex on f is needed.
var turnPending atomic.Bool

// escPending signals — from the stdin-reader goroutine to the main poll loop —
// that a bare ESC (the remote interrupt keystroke) was read, so the main
// goroutine can append the canned end-of-turn line (appendTurnEnd). Signal only,
// exactly like turnPending: the reader never writes f (single-writer-of-f). Set
// only in Esc-ends-turn mode (envEscEndsTurn); untouched otherwise.
var escPending atomic.Bool

// clearPending signals — from the stdin-reader goroutine to the main poll loop —
// that stdin bytes arrived (the local pyry attach head's answer keystroke) in
// modal-clear-on-answer mode (envModalClearOnAns), so the main goroutine can clear
// the permission modal (clearModalScreen). Signal only, exactly like turnPending /
// escPending: the reader never writes stdout or f (single-writer discipline). Set
// only in clear-on-answer mode; untouched otherwise.
var clearPending atomic.Bool

// trustAcceptPending signals — from the stdin-reader goroutine to the main poll
// loop — that the trust-folder ACCEPT keystroke arrived in trust mode
// (envTrustTrigger): the first post-trust stdin bytes that are NOT a bare ESC
// (Supervisor.AcceptTrust writes "1\r"). The main goroutine then clears the trust
// dialog (clearTrustScreen), so WaitReady returns clean and the held turn
// delivers. A bare ESC deny (Supervisor.SendEsc) never sets it — the reader
// distinguishes via containsBareESC, leaving the dialog up so the turn stays held
// and no reply is produced. Signal only, exactly like clearPending / escPending:
// the reader never writes stdout or f (single-writer discipline). Set only in
// trust mode; untouched otherwise.
var trustAcceptPending atomic.Bool

// interruptMarkerLine is the claude-format JSONL line appendTurnEnd writes when
// Esc-ends-turn mode (envEscEndsTurn) detects the remote interrupt keystroke: the
// interruption marker claude itself records when a turn is interrupted, which
// the PTY path's session-JSONL mapper turned into turnevent.TurnEnd{cancelled}
// (#1243) until that mapper was deleted with the PTY path (#1543). It is inert
// JSONL data, not a TUI substrate glyph, so the cmd/substrate-guard allowlist is
// unaffected.
//
// PROVENANCE: arm (b), DERIVED — not captured. The base line is
// internal/agentrun/jsonl/testdata/no_end_turn.jsonl:53 (claude 2.1.128), the only
// recorded interruption entry in the repo. Preserved verbatim from it: the full
// 13-key top-level set and its order, and the values of isSidechain, type, message,
// userType, entrypoint, version. Substituted span, shape-preserving: the
// session-identity fields parentUuid, promptId, uuid, timestamp, cwd, sessionId,
// gitBranch — nothing on this path reads any of them, and the base line's real
// values name a developer worktree and a real session.
//
// Two absences were load-bearing, both silent if broken. The consumer that made
// them so, the PTY path's session-JSONL mapper, was deleted with that path
// (#1543), so nothing reads them now; they are kept because they match the
// captured base line:
//
//   - NO top-level "permissionMode" key. The mapper treated that key's PRESENCE
//     as the mark of a human-authored prompt, so one extra key made the line
//     read as a human prompt rather than an interruption.
//   - NO tool_result content block. The mapper's tool_result branch ran before
//     the marker check and returned, so any entry carrying one became a
//     ToolUpdate and never reached the prose matcher.
//
// The full key set is deliberate rather than a minimal {"type":"user","message":…}:
// tui-driver's parseEntry builds Raw from the WHOLE line, so a 2-key line would make
// permissionMode's absence an absence among 2 keys where production sees an absence
// among 13.
const interruptMarkerLine = `{"parentUuid":"00000000-0000-4000-8000-000000000001","isSidechain":false,` +
	`"promptId":"00000000-0000-4000-8000-000000000002","type":"user",` +
	`"message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user]"}]},` +
	`"uuid":"00000000-0000-4000-8000-000000000003","timestamp":"2026-01-01T00:00:00.000Z",` +
	`"userType":"external","entrypoint":"code-review","cwd":"/tmp/fake-claude",` +
	`"sessionId":"00000000-0000-4000-8000-000000000004","version":"2.1.128","gitBranch":"fake-claude"}` + "\n"

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

// emitStructuredJSONLIfTriggered consumes the structured-JSONL trigger and
// appends its contents verbatim to f — the live session JSONL. The daemon reader
// that tailed it was deleted with the PTY path (#1543). The
// trigger file's contents ARE the claude-format JSONL lines to append (same
// "contents are the payload" shape as emitAssistantIfTriggered).
//
// It CLAIMS the trigger with an atomic os.Rename (claimTrigger) before reading,
// so the removal that ends a consume targets only the inode it actually read —
// never a value a producer wrote to the shared trigger name in between. This
// closes the residual #984 read-then-remove TOCTOU that #958's empty-read gate
// did not: the producer (relay_two_phone_structured_test.go) re-drops the kicker
// line every 250 ms and, once A observes the live stream, drops the full fixture
// exactly once via sync.Once. A plain read-by-name / remove-by-name consumer
// could unlink that fixture if the drop landed in the shared name between the
// consumer's read and its remove (a wide window — the load-bearing f.Sync() is a
// real fsync, slow under -race + full-suite I/O contention), permanently losing
// the only tool_use/turn_end and leaving A a partial structured set (the exact
// observed failure). Claiming first makes the consume atomic against concurrent
// producer writes, so the remove can never destroy a newer drop.
//
// Only the main poll goroutine calls this, serially, so the fixed claim sidecar
// is always absent at a cycle's start. Errors are silenced — the e2e asserts
// downstream (the interactive phone receives the structured envelopes), and a
// missing trigger is the steady state.
func emitStructuredJSONLIfTriggered(f *os.File, path string) {
	claimed, ok := claimTrigger(path)
	if !ok {
		return
	}
	consumeStructuredClaim(f, path, claimed)
}

// claimTrigger atomically renames the trigger at path to a fixed sidecar
// (path + ".consuming") so the consumer that follows operates only on the inode
// it claimed — the shared name path is then free for producers, and the eventual
// removal can never unlink a value a producer wrote afterward. Reports the
// claimed path and true on success, or "" and false when no trigger is present
// (os.Rename ENOENT — the steady state). fakeclaude runs a single poll goroutine
// and every consume completes before the next claim, so the fixed sidecar name
// needs no uniquification and is absent at each cycle's start.
func claimTrigger(path string) (string, bool) {
	claimed := path + ".consuming"
	if err := os.Rename(path, claimed); err != nil {
		return "", false
	}
	return claimed, true
}

// consumeStructuredClaim finishes a consume of a trigger already claimed by
// claimTrigger: it reads the claimed inode, appends it to f (the session JSONL),
// fsyncs, and removes the claim. The f.Sync() is load-bearing: the daemon's tail
// is a separate process and macOS APFS otherwise defers cross-process visibility
// (mirrors the stdin reader's per-write fsync).
//
// An empty claim is a producer mid-O_TRUNC write (os.WriteFile = open(O_CREATE|
// O_TRUNC) then a single write; between the truncate and the write the file
// exists but is empty). The claimed inode — which the producer still holds open
// by fd — is handed back under the shared name via os.Rename so the pending write
// stays reachable, and the next poll consumes the completed content. This is the
// #958 "leave it for the next poll" behaviour, now expressed through the claim,
// which is why the empty-is-skipped contract still holds. (A kicker line can be
// orphaned only if two kicker writes straddle a rename-back; the kicker re-drops
// every 250 ms and that loss never touches the sync.Once fixture, so it is
// unobservable — not defended against, per evidence-based fix selection.)
func consumeStructuredClaim(f *os.File, path, claimed string) {
	data, err := os.ReadFile(claimed)
	if err != nil {
		_ = os.Remove(claimed)
		return
	}
	if len(data) == 0 {
		_ = os.Rename(claimed, path)
		return
	}
	if len(data) > assistantMaxBytes {
		data = data[:assistantMaxBytes]
	}
	if _, err := f.Write(data); err != nil {
		_ = os.Remove(claimed)
		return
	}
	_ = f.Sync()
	_ = os.Remove(claimed)
}

// emitIdleIfTriggered checks for the idle-trigger file
// (PYRY_FAKE_CLAUDE_IDLE_TRIGGER). When present it writes the idle-prompt glyph
// to os.Stdout once — flipping tui-driver's IsIdle true so the supervisor's
// WaitReady returns and a queued backlog drains — removes the trigger, and
// reports true. When absent it reports false. Gated in main by a one-shot
// `idled` bool exactly like the `rotated` gate, so the glyph is emitted at most
// once; thereafter nothing overwrites the bottom status region (the delivered
// prompt is not echoed to stdout), so claude stays idle and every subsequent
// queued turn drains back-to-back. Runs only on the main poll goroutine, so it
// never races the stdin reader for os.Stdout (and in this mode — envTUI off —
// the reader emits no spinner, so the main goroutine is the sole stdout writer).
// Errors are silenced, mirroring the sibling emit* helpers: the e2e asserts
// downstream (the backlog reaches claude in order), never on the write itself.
func emitIdleIfTriggered(path string) bool {
	if _, err := os.Stat(path); err != nil {
		return false
	}
	writeStdout(idleGlyph)
	_ = os.Remove(path)
	return true
}

// waitForTriggerFile blocks until path exists, polling os.Stat every pollInterval.
// It backs the stream-mode startup hold (envStreamHold, #1138): the child is parked
// here BEFORE it reads any stdin, so the daemon's queued user turns accumulate in the
// child's stdin pipe during the hold and drain in FIFO order once the trigger drops.
// Mirrors the emit*IfTriggered poll shape (os.Stat + time.Sleep(pollInterval)); any
// stat error is treated as "not yet" and re-polls (the test's writer creates the
// trigger exactly once), and the trigger is left in place — nothing re-reads it.
func waitForTriggerFile(path string) {
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(pollInterval)
	}
}

// emitModalIfTriggered checks for the modal-trigger file
// (PYRY_FAKE_CLAUDE_MODAL_TRIGGER). When present it writes the permission-modal
// screen (modalScreen) to os.Stdout once — flipping tui-driver's detected modal
// class from Unknown to Permission so the daemon's #798 modal producer surfaces a
// modal_shown to interactive phones — removes the trigger, and reports true. When
// absent it reports false. Gated in main by a one-shot `modalShown` bool exactly
// like the `idled` / `rotated` gates, so the screen is written at most once. By
// default the fake never clears the modal afterwards (each #791 test raises one
// modal and ends, and the answer/deny keystrokes are fire-and-forget with no
// dismissal re-read, so nothing blocks on the modal disappearing); only
// modal-clear-on-answer mode (envModalClearOnAns, the #793 two-head e2e) clears it
// on the local answer keystroke via clearModalScreen. Runs only on the main
// poll goroutine, so it never races the stdin reader for os.Stdout (in modal mode
// — envTUI off — the reader emits no spinner, so the main goroutine is the sole
// stdout writer). Errors are silenced, mirroring the sibling emit* helpers: the
// e2e asserts downstream (the phone receives modal_shown), never on the write.
func emitModalIfTriggered(path string) bool {
	if _, err := os.Stat(path); err != nil {
		return false
	}
	writeStdout([]byte(modalScreen))
	_ = os.Remove(path)
	return true
}

// clearModalScreen writes modalClearScreen to os.Stdout once (fsync'd) so
// tui-driver's detected class transitions Permission->Unknown and the merge loop
// fires EventKindPtyModalHidden — the daemon's #706 local arm then Resolve()s the
// modal and broadcasts modal_dismissed{local}. Runs ONLY on the main poll
// goroutine (like emitModalIfTriggered): the stdin reader only signals via
// clearPending, never writes stdout, so the single-writer discipline holds.
// Best-effort per writeStdout; the e2e asserts downstream (the phone receives
// modal_dismissed), never on the write itself.
func clearModalScreen() {
	writeStdout([]byte(modalClearScreen))
}

// emitTrustIfTriggered checks for the trust-trigger file
// (PYRY_FAKE_CLAUDE_TRUST_TRIGGER). When present it writes the startup
// trust-folder dialog (trustScreen) to os.Stdout once — flipping tui-driver's
// detected class from Unknown to TrustFolder so the daemon's #708 producer
// surfaces modal_shown{trust} AND the supervisor's WaitReady returns
// Readiness{TrustModal:true} (the queued turn is held with ErrTrustModalPending,
// #1013) — removes the trigger, and reports true. When absent it reports false.
// Gated in main by a one-shot `trustShown` bool exactly like the `modalShown` /
// `idled` / `rotated` gates, so the screen is written at most once. The fake does
// NOT clear the dialog here; only the accept keystroke clears it (clearTrustScreen
// via trustAcceptPending), and a bare ESC deny leaves it up. Runs only on the main
// poll goroutine, so it never races the stdin reader for os.Stdout (in trust mode
// — envTUI off — the reader emits no spinner, so the main goroutine is the sole
// stdout writer). Errors are silenced, mirroring the sibling emit* helpers: the
// e2e asserts downstream (the phone receives modal_shown), never on the write.
func emitTrustIfTriggered(path string) bool {
	if _, err := os.Stat(path); err != nil {
		return false
	}
	writeStdout([]byte(trustScreen))
	_ = os.Remove(path)
	return true
}

// clearTrustScreen writes trustClearScreen to os.Stdout once (fsync'd) so
// tui-driver's detected class transitions TrustFolder->Unknown and
// HasTrustModal/Readiness.TrustModal go false — the held turn then delivers on the
// next WriteUserTurn retry. Runs ONLY on the main poll goroutine (like
// emitTrustIfTriggered / clearModalScreen): the stdin reader only signals via
// trustAcceptPending, never writes stdout, so the single-writer discipline holds.
// Best-effort per writeStdout; the e2e asserts downstream (the turn delivers /
// queue drains), never on the write itself.
func clearTrustScreen() {
	writeStdout([]byte(trustClearScreen))
}

// enterRawMode puts fakeclaude's stdin (the PTY slave) into raw mode, matching
// what the real claude TUI does on startup. The tui-driver PTY leaves the slave
// in the default canonical (cooked) discipline, which buffers input until a line
// terminator and maps CR->NL: a bare ESC keystroke (the deny-on-timeout
// actuation, a lone 0x1b) would be withheld from read() indefinitely, and a
// "2\r" answer would surface as "2\n". Raw mode (VMIN=1, ICANON/ICRNL cleared)
// delivers every keystroke byte immediately and verbatim, so the stdin log
// observes exactly what the supervisor wrote — "2\r" for an answer, a bare 0x1b
// for the ESC deny or the remote interrupt (Esc-ends-turn mode). Best-effort,
// mirroring the rest of this stand-in's silent-error posture: on failure the
// default discipline remains and the e2e's ESC assertion flags it downstream. The
// prior terminal state is intentionally not restored — the PTY is torn down with
// the process.
func enterRawMode() {
	_, _ = term.MakeRaw(int(os.Stdin.Fd()))
}

// appendTurnGrowth grows the current session JSONL f by one inert line so the
// daemon's #668 transcript-growth commit-confirm (confirmViaTranscriptGrowth)
// observes growth past its pre-delivery baseline and acks the delivered turn —
// the same signal real claude produces when it commits a turn to its session
// JSONL. Runs ONLY on the main goroutine, preserving the single-writer-of-f
// invariant (see turnPending). The line is the inert "{}\n" openSession already
// writes: a typeless line that carries no turn event — invisible to every
// assertion except "the file grew". Best-effort, mirroring emitStructuredJSONLIfTriggered; a
// persistently-failing write surfaces as the daemon's loud ErrTurnNotCommitted,
// never a false ack.
func appendTurnGrowth(f *os.File) {
	if _, err := f.WriteString("{}\n"); err != nil {
		return
	}
	_ = f.Sync()
}

// appendTurnEnd grows the current session JSONL f by claude's interruption marker
// (interruptMarkerLine). The daemon reader that mapped it to a turn_end envelope
// was deleted with the PTY path (#1543), so the write reaches no consumer. Runs
// ONLY on the main goroutine, preserving the single-writer-of-f invariant (see
// escPending / turnPending). Best-effort + fsync, mirroring
// emitStructuredJSONLIfTriggered.
func appendTurnEnd(f *os.File) {
	if _, err := f.WriteString(interruptMarkerLine); err != nil {
		return
	}
	_ = f.Sync()
}

// containsBareESC reports whether buf holds a bare ESC — the remote interrupt
// keystroke (supervisor.SendEsc writes a lone 0x1b), as distinct from the ESC
// that leads a CSI / bracketed-paste sequence. In this harness the stdin stream
// carries exactly three ESC sources: the bracketed-paste open (ESC[200~) and
// close (ESC[201~) that wrap a delivered prompt, and the interrupt's lone ESC.
// Both paste markers are 0x1b immediately followed by '[' (0x5b); the delivered
// prompt content between them is raw-ESC/C0-free (#749's paste-content guard),
// so it contributes no 0x1b. Therefore a 0x1b NOT immediately followed by 0x5b is
// the interrupt and nothing else. Rule: a 0x1b at index i is bare iff it is the
// buffer's last byte (i == len(buf)-1) OR buf[i+1] != 0x5b. The last-byte arm is
// safe because tui-driver writes each paste marker as one writeRaw of the whole
// ESC[200~…ESC[201~\r unit, so a paste marker's 0x1b is never the last byte of a
// read (its '[' always follows in the same read); SendEsc writes the lone 0x1b as
// its own PTY write, so it arrives standalone or trailing. (A future multi-KiB
// prompt whose paste could split across reads on a marker's 0x1b would need a
// one-byte cross-buffer carry; not built — this harness controls the prompt size.)
func containsBareESC(buf []byte) bool {
	for i, b := range buf {
		if b != 0x1b {
			continue
		}
		if i == len(buf)-1 || buf[i+1] != 0x5b {
			return true
		}
	}
	return false
}

// rotateSession closes the current session JSONL f and opens a fresh
// <uuid>.jsonl in the same dir — the UUID rotation the file trigger performs.
// Returns the new *os.File. It had a second caller until #2456 retired the
// clear-rotate mode, whose own driver (the terminal supervisor typing "/clear"
// into a PTY) #1348 had already deleted; the file trigger is now the only one.
// Runs ONLY on the main goroutine, preserving the single-writer-of-f
// invariant. The old-fd close is best-effort: each write is fsynced by
// openSession / appendTurn*, so a failed close cannot lose committed data.
func rotateSession(f *os.File, dir string) *os.File {
	_ = f.Close()
	return openSession(dir, uuidV4())
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
// phone-controlled prompt bytes. When escEndsTurn is true it also scans each read
// for a bare ESC (the remote interrupt keystroke) and, on finding one, signals
// the main goroutine via escPending — signal only, never writing f. When
// clearOnAnswer is true it likewise signals the main goroutine via clearPending on
// every read (the local head's answer keystroke), so the main loop can clear the
// permission modal — signal only, never writing stdout. When trustTrig is true
// it scans each read for a bare ESC and,
// on a read that is NOT a bare ESC (the trust ACCEPT keystroke "1\r"), signals the
// main goroutine via trustAcceptPending to clear the trust dialog — signal only,
// never writing stdout; a bare ESC deny is left unsignalled so the dialog stays up.
func startStdinReader(logPath string, tui, escEndsTurn, clearOnAnswer, trustTrig bool) {
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
				// Esc-ends-turn mode: a bare ESC in this read is the remote
				// interrupt keystroke; signal the main goroutine to append the
				// end_turn line. Signal only (single-writer-of-f), like turnPending.
				if escEndsTurn && containsBareESC(buf[:n]) {
					escPending.Store(true)
				}
				// Clear-on-answer mode: signal the main goroutine that stdin bytes
				// arrived so it can clear the modal. The main loop's modalShown gate
				// restricts the actual clear to a post-modal keystroke. Signal only
				// (single-writer discipline), like escPending.
				if clearOnAnswer {
					clearPending.Store(true)
				}
				// Trust mode: discriminate the trust ACCEPT keystroke from a bare ESC
				// deny (the load-bearing part of #993). A read that is NOT a bare ESC
				// is the accept "1\r" (Supervisor.AcceptTrust) — signal the main
				// goroutine to clear the trust dialog (gated on trustShown there). A
				// bare ESC (Supervisor.SendEsc deny) is left unsignalled, so the dialog
				// stays up and the turn stays held. Signal only (single-writer
				// discipline), like clearPending. The post-clear delivered prompt (a
				// bracketed paste, ESC[…, not a BARE ESC) re-signals harmlessly — the
				// main loop's one-shot trustCleared gate ignores it.
				if trustTrig && !containsBareESC(buf[:n]) {
					trustAcceptPending.Store(true)
				}
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
