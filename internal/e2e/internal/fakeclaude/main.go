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
//	                               supervisor wrote to the PTY. In
//	                               stream-json mode the value is a path
//	                               STEM rather than the file: each child
//	                               tees its own stdin to
//	                               <stem>.<its session id> — the value
//	                               after the last --session-id/--resume on
//	                               its own argv, or <stem>.unattributed
//	                               when argv carries none (#1137,
//	                               per-child since #1331). The PTY path is
//	                               unchanged: there the value is still the
//	                               literal file every child appends to.
//	                               Default off — when unset, stdin is not
//	                               read.
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
//	PYRY_FAKE_CLAUDE_IDLE_TRIGGER  optional path watched in parallel with the
//	                               others. When set, fakeclaude starts BUSY:
//	                               it emits no startup idle glyph and never the
//	                               thinking spinner, so tui-driver's IsIdle
//	                               stays false and the supervisor's WaitReady
//	                               blocks (claude "busy"). On the trigger
//	                               file's first appearance it emits the idle
//	                               glyph (U+276F) once — flipping IsIdle true so
//	                               WaitReady returns — then removes the trigger;
//	                               claude then stays idle for every later turn
//	                               (nothing overwrites the bottom status region,
//	                               and the delivered prompt is not echoed to
//	                               stdout), so a queued backlog drains
//	                               back-to-back. Used by the queue-drain e2e
//	                               (#792) to open a controllable busy window.
//	                               Mutually exclusive with PYRY_FAKE_CLAUDE_TUI,
//	                               whose startup idle glyph would defeat the
//	                               busy window. Default off — when unset, no
//	                               watch and startup behaviour is unchanged.
//	PYRY_FAKE_CLAUDE_MODAL_TRIGGER optional path watched in parallel with the
//	                               others. When set, fakeclaude emits the idle
//	                               glyph (U+276F) once at startup — a baseline
//	                               non-modal screen, so tui-driver classifies the
//	                               child as Unknown/idle — and never the thinking
//	                               spinner. On the trigger file's first appearance
//	                               it writes a permission-modal screen once
//	                               (fsync'd) whose bottom region carries the exact
//	                               "Do you want to proceed?" anchor, then removes
//	                               the trigger. tui-driver's merge loop detects the
//	                               Unknown->Permission class transition on the next
//	                               poll tick, so the daemon's #798 modal producer
//	                               surfaces a modal_shown to interactive phones.
//	                               One-shot: later trigger drops are ignored, and
//	                               the fake never clears the modal or reads the
//	                               answer keystroke for coordination (each #791
//	                               test raises exactly one modal and ends). Used by
//	                               the remote-permission e2e (#791). Mutually
//	                               exclusive with PYRY_FAKE_CLAUDE_TUI and
//	                               PYRY_FAKE_CLAUDE_IDLE_TRIGGER, whose startup
//	                               spinner / busy window would perturb the baseline.
//	                               Default off — when unset, no watch and startup
//	                               behaviour is unchanged.
//	PYRY_FAKE_CLAUDE_ESC_ENDS_TURN optional; when set to any non-empty value,
//	                               fakeclaude puts stdin into raw mode (like modal
//	                               mode) so the lone interrupt ESC is not withheld by
//	                               the canonical line discipline, and the stdin
//	                               reader watches the byte stream for a bare ESC — a
//	                               0x1b not part of a CSI/bracketed-paste sequence
//	                               (i.e. not immediately followed by '[').
//	                               That bare ESC is the remote interrupt keystroke
//	                               (supervisor.SendEsc writes a lone 0x1b). On the
//	                               first bare ESC fakeclaude appends claude's own
//	                               interruption marker (interruptMarkerLine) to the
//	                               live session JSONL and fsyncs, so the daemon's
//	                               structured-turn producer maps it to a
//	                               turn_end{cancelled} — making the Esc the CAUSE of
//	                               the turn ending (the interrupt-live e2e #794
//	                               asserts a turn_end whose only source is this
//	                               handler; #1244 additionally asserts the reason).
//	                               One-shot: a second
//	                               ESC is inert, matching claude's own re-interrupt
//	                               no-op. Unlike the idle/modal triggers this
//	                               coexists with PYRY_FAKE_CLAUDE_TUI (the two touch
//	                               different bytes: TUI emits the startup glyph +
//	                               spinner, this scans for the bare ESC). Default off
//	                               — when unset, fakeclaude is byte-identical to its
//	                               prior behaviour.
//	PYRY_FAKE_CLAUDE_MODAL_CLEAR_ON_ANSWER  optional; EXTENDS modal mode (requires
//	                               PYRY_FAKE_CLAUDE_MODAL_TRIGGER — a no-op otherwise).
//	                               When set, after the modal has been shown the first
//	                               stdin bytes fakeclaude reads (the local pyry attach
//	                               head's answer keystroke) cause it to write a
//	                               modal-clearing screen once (modalClearScreen): a run
//	                               of newlines that scrolls the "Do you want to
//	                               proceed?" anchor above the permission detection
//	                               window plus a trailing idle glyph, so tui-driver's
//	                               detected class transitions Permission->Unknown and
//	                               the merge loop fires EventKindPtyModalHidden. That
//	                               makes the local keystroke the CAUSE of the modal
//	                               vanishing, which the daemon's #706 local arm resolves
//	                               into modal_dismissed{local} (the two-head
//	                               first-answer-wins e2e #793). The stdin reader only
//	                               signals (clearPending); the main goroutine writes the
//	                               clear, preserving the single-writer discipline.
//	                               One-shot: a second keystroke is inert. Gated on the
//	                               modal having been shown, so a pre-modal keystroke
//	                               cannot clear early. Byte-identical to today when
//	                               unset — #791 sets only MODAL_TRIGGER and its fake
//	                               never clears, so its remote arm stays unaffected.
//	PYRY_FAKE_CLAUDE_CLEAR_ROTATES optional; when set to any non-empty value,
//	                               fakeclaude watches its stdin for the "/clear"
//	                               slash-command bytes and, on the first match,
//	                               rotates its live session JSONL once — closing
//	                               the current <uuid>.jsonl and opening a fresh
//	                               <uuid>.jsonl in the same dir, exactly as the
//	                               file trigger does. That mirrors real claude:
//	                               typing "/clear" starts a new session and
//	                               rotates the on-disk session UUID, which pyry's
//	                               rotation watcher follows into the registry.
//	                               supervisor.StartNewSession types the "/clear"
//	                               (ClearInputLine + TypePrompt) when a phone sends
//	                               a new_session control frame, so the rotation the
//	                               daemon observes is CAUSED by that frame (the
//	                               new_session rotation e2e #1004 asserts a registry
//	                               id change whose only source is this handler).
//	                               Detection is discipline-agnostic: the reader
//	                               accumulates stdin across reads, so a "/clear"
//	                               split byte-by-byte under raw mode and the single
//	                               "/clear\n" line the default canonical discipline
//	                               delivers both match. Signal-only from the reader
//	                               (it sets clearRotatePending; the main goroutine
//	                               performs the rotation), preserving the single-
//	                               writer-of-f invariant like envEscEndsTurn.
//	                               One-shot via the shared `rotated` gate: a second
//	                               "/clear" is inert, matching claude's own re-clear
//	                               no-op, and the file trigger and this mode never
//	                               both fire. Does NOT enter raw mode — the trailing
//	                               "\r" commits the canonical line, so a single read
//	                               already carries "/clear". Default off — when
//	                               unset, fakeclaude is byte-identical to its prior
//	                               behaviour.
//	PYRY_FAKE_CLAUDE_TRUST_TRIGGER optional path watched in parallel with the
//	                               others. A sibling of the modal trigger for
//	                               claude's STARTUP trust-folder dialog (the #988
//	                               no-auto-trust gate). fakeclaude comes up idle
//	                               (startup glyph); on the trigger's first appearance
//	                               it writes trustScreen once — the "Quick safety
//	                               check" dialog whose ❯-marked option row satisfies
//	                               tui-driver's gridHasTrustDialog — so DetectModalClass
//	                               transitions Unknown->TrustFolder (the daemon surfaces
//	                               modal_shown{trust}) AND WaitReady returns
//	                               Readiness{TrustModal:true} (the supervisor HOLDS the
//	                               queued turn with ErrTrustModalPending, #1013). It
//	                               then watches stdin to discriminate the two
//	                               resolutions (the load-bearing part): the accept
//	                               keystroke ("1\r" from Supervisor.AcceptTrust — the
//	                               first post-trust stdin bytes that are NOT a bare
//	                               ESC) clears the dialog once (trustClearScreen), so
//	                               WaitReady returns clean and the held turn delivers;
//	                               a bare ESC deny (Supervisor.SendEsc, via a trust
//	                               modal_answer{exit}/timeout) is IGNORED — the dialog
//	                               stays up, the turn stays held forever, and no reply
//	                               is produced (the daemon's typed "folder not trusted"
//	                               session_error is emitted at the resolver regardless
//	                               of child behaviour, #1014). Reuses containsBareESC to
//	                               distinguish; the stdin reader only signals
//	                               (trustAcceptPending), the main goroutine is the sole
//	                               writer of f/stdout (single-writer discipline). Raw
//	                               mode required so "1\r" and the bare ESC reach the
//	                               reader verbatim/unbuffered. One-shot show + one-shot
//	                               clear. Used only by the untrusted-cwd trust e2e
//	                               (#993). Mutually exclusive with PYRY_FAKE_CLAUDE_TUI /
//	                               _IDLE_TRIGGER / _MODAL_TRIGGER, whose startup
//	                               spinner / busy window / permission screen would
//	                               perturb the baseline. Default off — when unset, no
//	                               watch and startup behaviour is unchanged.
//	PYRY_FAKE_CLAUDE_STREAM_JSON   optional; when set to any non-empty value,
//	                               fakeclaude speaks line-delimited stream-json
//	                               instead of the PTY/TUI surface: it reads
//	                               {"type":"user",…} turn envelopes on stdin and,
//	                               for each, writes one assistant text line (echoing
//	                               the prompt) followed by one result{subtype:
//	                               "success"} line to stdout — one response per
//	                               received user turn. Non-user lines (a
//	                               control_request interrupt, a blank line) are read
//	                               and ignored. This is the fake the daemon's
//	                               interactive_runner stream path drives: claude
//	                               spawned headless over a pipe, no PTY and no
//	                               transcript. Checked FIRST in main(), above the
//	                               mustEnv(SESSIONS_DIR/…) calls, so it binds no
//	                               sessions dir and opens no <uuid>.jsonl, and it
//	                               short-circuits before every other mode — making it
//	                               mutually exclusive with all of them by
//	                               construction (if both this and a PTY mode are set,
//	                               stream wins and the other is inert). Default off —
//	                               when unset, fakeclaude is byte-identical to its
//	                               prior behaviour.
//	PYRY_FAKE_CLAUDE_SESSION_ID_FROM_ARGV  optional; when set to any non-empty
//	                               value, fakeclaude takes the stem for its INITIAL
//	                               <uuid>.jsonl from its own argv — the value after
//	                               the last "--session-id" or "--resume"
//	                               (argvSessionID) — instead of
//	                               PYRY_FAKE_CLAUDE_INITIAL_UUID. That env is
//	                               process-wide, so every child of one daemon
//	                               inherits it identically; a MINTED
//	                               per-conversation session therefore wrote
//	                               <sharedDir>/<INITIAL_UUID>.jsonl while the daemon
//	                               tailed <convDir>/<mintedSessionID>.jsonl
//	                               (resolveBoundSessionJSONL) — the two could never
//	                               agree, so conversation-scoped turn lifecycle was
//	                               observable on ZERO PTY-tier tests (#1195). The
//	                               spawn argv is the one per-child channel that
//	                               already carries the id the daemon tails
//	                               (sessions.buildSession bakes in
//	                               "--session-id <pool id>"), which is what real
//	                               claude honours too. The value is adopted only if
//	                               it is a safe filename stem (see argvSessionID);
//	                               no flag, or an unsafe value, falls back to
//	                               PYRY_FAKE_CLAUDE_INITIAL_UUID rather than exiting,
//	                               so a legacy unpinned spawn still works and the
//	                               e2e fails on its assertion rather than on a dead
//	                               child. A later /clear rotation still mints a fresh
//	                               random uuid (rotateSession), as real claude does.
//	                               NOTE the flag's presence is NOT a minted/bootstrap
//	                               discriminator — since #839 every bootstrap spawn is
//	                               pinned too — so this must stay explicitly selected:
//	                               keying the behaviour on argv alone would re-point
//	                               every existing bootstrap child's transcript.
//	                               Default off — when unset, fakeclaude is
//	                               byte-identical to its prior behaviour, including
//	                               for a child whose argv carries --session-id or
//	                               --resume.
//	PYRY_FAKE_CLAUDE_JSONL_TRIGGER_DIR  optional directory watched in parallel with
//	                               the others. When set, fakeclaude watches
//	                               <dir>/<its own initial stem>.jsonl.trig and, on
//	                               each appearance, appends its contents verbatim to
//	                               the live session JSONL and fsyncs — the same
//	                               consume as PYRY_FAKE_CLAUDE_JSONL_TRIGGER, on a
//	                               PER-CHILD path. The shared-path trigger cannot
//	                               serve a multi-child process tree: its claim
//	                               protocol is sound only because each fakeclaude
//	                               runs a single poll goroutine, so with a bootstrap
//	                               child polling the same path whichever child claims
//	                               first appends the line to ITS OWN transcript — a
//	                               coin flip. Keying the path on the child's own stem
//	                               makes ownership structural: a test drops
//	                               <dir>/<mintedID>.jsonl.trig and only the minted
//	                               child can ever claim it, while the bootstrap child
//	                               polls <dir>/<initialUUID>.jsonl.trig, which the
//	                               test never creates. The path is computed once from
//	                               the stem openSession used, so per-child isolation
//	                               is exactly as good as the stem divergence
//	                               PYRY_FAKE_CLAUDE_SESSION_ID_FROM_ARGV provides
//	                               (the two are typically set together, but neither
//	                               requires the other). Independent of, and not
//	                               disabled by, PYRY_FAKE_CLAUDE_JSONL_TRIGGER; a
//	                               test may set both. Not one-shot — the consume
//	                               re-fires on every re-drop, which is what the #929
//	                               subscription-offset kicker needs. Used by the
//	                               minted per-conversation turn-lifecycle e2e
//	                               (#1195). Default off — when unset, no watch and
//	                               fakeclaude is byte-identical to its prior
//	                               behaviour.
//
// The binary lives under internal/e2e/internal/ to visibility-fence it from
// non-e2e callers. Because TUI mode makes this file carry claude-TUI
// substrate glyphs, it is on the cmd/substrate-guard allowlist (#603),
// mirroring the sanctioned internal/agentrun/ptyrunner/helper_test.go
// exemption.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/term"

	"github.com/pyrycode/pyrycode/internal/control"
)

const (
	envSessionsDir       = "PYRY_FAKE_CLAUDE_SESSIONS_DIR"
	envInitialUUID       = "PYRY_FAKE_CLAUDE_INITIAL_UUID"
	envTrigger           = "PYRY_FAKE_CLAUDE_TRIGGER"
	envStdinLog          = "PYRY_FAKE_CLAUDE_STDIN_LOG"
	envAssistantTrigger  = "PYRY_FAKE_CLAUDE_ASSISTANT_TRIGGER"
	envJSONLTrigger      = "PYRY_FAKE_CLAUDE_JSONL_TRIGGER"
	envTUI               = "PYRY_FAKE_CLAUDE_TUI"
	envIdleTrigger       = "PYRY_FAKE_CLAUDE_IDLE_TRIGGER"
	envModalTrigger      = "PYRY_FAKE_CLAUDE_MODAL_TRIGGER"
	envEscEndsTurn       = "PYRY_FAKE_CLAUDE_ESC_ENDS_TURN"
	envModalClearOnAns   = "PYRY_FAKE_CLAUDE_MODAL_CLEAR_ON_ANSWER"
	envClearRotates      = "PYRY_FAKE_CLAUDE_CLEAR_ROTATES"
	envTrustTrigger      = "PYRY_FAKE_CLAUDE_TRUST_TRIGGER"
	envSessionIDFromArgv = "PYRY_FAKE_CLAUDE_SESSION_ID_FROM_ARGV"
	envJSONLTriggerDir   = "PYRY_FAKE_CLAUDE_JSONL_TRIGGER_DIR"
	envStreamJSON        = "PYRY_FAKE_CLAUDE_STREAM_JSON"
	envStreamInterrupt   = "PYRY_FAKE_CLAUDE_STREAM_INTERRUPT"
	envStreamHold        = "PYRY_FAKE_CLAUDE_STREAM_HOLD"
	envStreamApprove     = "PYRY_FAKE_CLAUDE_STREAM_APPROVE"
	envStreamBogus       = "PYRY_FAKE_CLAUDE_STREAM_BOGUS"
	envStreamRateLimit   = "PYRY_FAKE_CLAUDE_STREAM_RATE_LIMIT"
	envApproveSocketFile = "PYRY_FAKE_CLAUDE_APPROVE_SOCKET_FILE"
	assistantMaxBytes    = 64 * 1024
	pollInterval         = 50 * time.Millisecond
)

// modalScreen is the compact plaintext permission-modal screen fakeclaude writes
// on the modal trigger's first appearance (envModalTrigger). Its bottom region
// carries the exact anchor tui-driver's DetectModalClass keys on for the
// permission class ("Do you want to proceed?") AND, directly below it, a
// pointer-marked numbered option row (❯ 1. Yes). Both are required: tui-driver
// #242 (v1.10.0) gated the permission class on the option-row dialog shape, not
// the anchor phrase alone, so the anchor by itself now classifies as Unknown. No
// higher-priority anchor is present (no "Manage MCP servers", "Agents"+tab,
// "Enter to select", "Quick safety check", or /-prefixed picker row), so a
// rendered snapshot classifies as ModalClassPermission. The producer's own
// permission option set is fixed and screen-independent (only the Title is lifted
// from the screen); the option row here exists solely to satisfy the dialog-shape
// gate. Emitted after the startup idle glyph so the class transitions
// Unknown->Permission. This file is on the cmd/substrate-guard allowlist (a
// file-level exemption), so the ❯ glyph and the "Do you want to proceed?" phrase
// are both covered.
const modalScreen = "Tool request: run a shell command\r\n" +
	"\r\n" +
	"Do you want to proceed?\r\n" +
	"❯ 1. Yes\r\n" +
	"  2. No\r\n"

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

// modalClearScrollRows is how many blank lines clearModalScreen scrolls after the
// permission modal so the "Do you want to proceed?" anchor and the ❯ option row
// below it leave the bottom permissionRegionRows (12) window DetectModalClass
// scans. modalScreen now renders 5 rows with the anchor at row 2 and the option
// row at row 3; the trailing idle glyph then lands at row 5+N, making an
// (N+6)-row grid whose bottom-12 window starts at row N-6, so any N>9 excludes
// both the anchor and the option row. 16 leaves comfortable margin and never
// scrolls the 40-row (DefaultPtyRows) screen. Pinned by modal_detect_test.go.
const modalClearScrollRows = 16

// modalClearScreen is what clearModalScreen writes (once, on the local answer
// keystroke) in modal-clear-on-answer mode (envModalClearOnAns) to make the
// permission modal vanish: modalClearScrollRows blank lines that scroll the anchor
// above the bottom-12 detection window, then the idle glyph as a non-empty final
// row. The trailing glyph is load-bearing — without a non-empty last row
// trimGridText drops the blank lines and the anchor re-enters the window, so the
// screen would still classify as Permission. Rendered after modalScreen the
// combined screen must classify as NOT-Permission (the modal_detect_test.go
// assertion). Reuses the idle glyph, so it is covered by this file's existing
// cmd/substrate-guard allowlist entry.
var modalClearScreen = strings.Repeat("\r\n", modalClearScrollRows) + string(idleGlyph)

// trustScreen is claude's STARTUP trust-folder dialog (#988), a sibling of
// modalScreen for the trust class. It is the single screen shape that makes BOTH
// tui-driver entry points fire: DetectModalClass returns ModalClassTrustFolder
// (the daemon's #708 producer surfaces modal_shown{class:"trust"}) AND
// gridHasTrustDialog/Readiness.TrustModal is true (the supervisor holds the
// queued turn with ErrTrustModalPending, #1013). gridHasTrustDialog (tui-driver
// v1.10.0 #219) requires the "Quick safety check" header AND, within 3 rows
// below, a ❯-marked numbered option row — so the header phrase alone (a source
// quotation) no longer classifies. The ❯ on the option row also carries IsIdle
// (WaitReady waits for idle before classifying), so the held turn hits the trust
// gate rather than blocking. Emitted after the startup idle glyph so the class
// transitions Unknown->TrustFolder. Both the ❯ glyph and the header phrase are
// covered by this file's cmd/substrate-guard allowlist entry. Pinned by
// trust_detect_test.go.
const trustScreen = "Quick safety check: Is this a project you trust?\r\n" +
	"❯ 1. Yes, I trust this folder\r\n" +
	"  2. No, take me back\r\n"

// trustClearScrollRows is how many blank lines clearTrustScreen scrolls to push
// the "Quick safety check" header off the ENTIRE rendered grid. Unlike the
// permission clear (modalClearScrollRows, which only clears the bottom-12
// permissionRegionRows window), gridHasTrustDialog scans EVERY grid row, so the
// header must scroll out of the whole DefaultPtyRows (40) screen: the header sits
// at row 0, so any scroll count >= 37 (40 - 3, the trust screen's own rows)
// evicts it. 40 clears the full grid with margin and never leaves the header in
// scrollback-visible range. Pinned by trust_detect_test.go.
const trustClearScrollRows = 40

// trustClearScreen is what clearTrustScreen writes (once, on the accept
// keystroke) to make the trust dialog vanish: trustClearScrollRows blank lines
// that scroll the "Quick safety check" header off the whole grid, then the idle
// glyph as a non-empty final row. The trailing glyph is load-bearing — without a
// non-empty last row IsIdle would go false after the clear (no ❯ anywhere) and
// WaitReady would block instead of delivering the held turn. Rendered after
// trustScreen the combined screen must classify as NOT-TrustFolder AND
// HasTrustModal==false (the trust_detect_test.go assertions). Reuses the idle
// glyph, so it is covered by this file's existing cmd/substrate-guard allowlist
// entry.
var trustClearScreen = strings.Repeat("\r\n", trustClearScrollRows) + string(idleGlyph)

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

// clearRotatePending signals — from the stdin-reader goroutine to the main poll
// loop — that the "/clear" slash-command bytes were read in clear-rotate mode
// (envClearRotates), so the main goroutine can rotate the live session JSONL
// (rotateSession). Signal only, exactly like escPending / turnPending: the reader
// never writes f (single-writer-of-f). Set only in clear-rotate mode; untouched
// otherwise.
var clearRotatePending atomic.Bool

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
// turnbridge's mapper maps to turnevent.TurnEnd{cancelled} (#1243,
// internal/turnbridge/`mapEntry`). It is inert JSONL data, not a TUI substrate
// glyph, so the cmd/substrate-guard allowlist is unaffected.
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
// Two absences are load-bearing, both silent if broken (the mapper simply produces
// no turn_end, and the consuming e2e times out 15 s later):
//
//   - NO top-level "permissionMode" key. isInterruptMarker requires
//     !userAuthored(e), and userAuthored is the PRESENCE of that key
//     (mapper.go:163-166). One extra key and the line reads as a human prompt.
//   - NO tool_result content block. mapEntry's ParseToolResult branch precedes the
//     marker check and returns (mapper.go:80-86), so any entry carrying one becomes
//     a ToolUpdate and never reaches the prose matcher.
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
	// Stream-json mode (envStreamJSON) is checked FIRST — above the
	// mustEnv(envSessionsDir/…) calls — so it binds no sessions dir and opens no
	// transcript (the daemon's streamsup path deliberately watches no <uuid>.jsonl),
	// and short-circuits before every PTY/TUI mode below, making stream mode
	// mutually exclusive with all of them by construction. When unset, control falls
	// straight through and fakeclaude is byte-identical to its prior behaviour.
	//
	// The interrupt mode (envStreamInterrupt, default-off) is a rider on stream mode:
	// it withholds the per-turn result (the turn stays in flight) and honours an
	// interrupt control_request. Passed as a value so runStreamJSON stays a pure I/O
	// seam; unset keeps every other stream rider on the untouched default.
	//
	// PYRY_FAKE_CLAUDE_STDIN_LOG (default-off) tees every stdin byte the daemon
	// writes to this stream child — user-turn envelopes, interrupt control_requests —
	// to the log so the e2e can assert what reached the child (e.g. #1137: a stream
	// new_session is a fresh SPAWN, so the child NEVER receives a typed "/clear"). The
	// tee lives at the call site so runStreamJSON keeps its pure I/O signature (the
	// #1140 unit test and #1136 interrupt rider are untouched). Append-mode + per-write
	// Sync mirror the PTY reader's log (startStdinReader); unset is byte-identical to
	// prior behaviour (#1141/#1136 set no such env).
	//
	// In stream mode the env value is a path STEM, not the file: each child appends to
	// <stem>.<its own session id> (streamStdinLogPath). One shared file was the prior
	// contract, and it could not carry the claim its consumer makes — the daemon's
	// process env is inherited identically by the bootstrap child and the fresh
	// post-rotation child, so a needle in that file proved only that SOME child received
	// a turn, never which one, and a turn delivered to the OUTGOING child read as green
	// (#1331). This is the move PYRY_FAKE_CLAUDE_JSONL_TRIGGER_DIR already made for the
	// per-child trigger (#1195): keying the path on the child's own stem makes ownership
	// structural rather than a coin flip. The key is the SESSION ID rather than a pid or
	// a spawn ordinal because that is what the assertion is about — "the child the daemon
	// spawned with --session-id <newID>" is a session, not a process — so a crash-respawn
	// of the same session (--resume <sameID>) re-opens and appends to the same file,
	// which is why append-mode stays load-bearing here.
	if os.Getenv(envStreamJSON) != "" {
		stdin := io.Reader(os.Stdin)
		if logStem := os.Getenv(envStdinLog); logStem != "" {
			logPath := streamStdinLogPath(logStem, os.Args[1:])
			f, err := os.OpenFile(logPath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
			if err != nil {
				fatalf("open stream stdin log %s: %v", logPath, err)
			}
			stdin = io.TeeReader(os.Stdin, syncWriter{f})
		}
		// Startup hold (envStreamHold, default-off): block BEFORE consuming any
		// stdin until the trigger file appears (#1138). The daemon's queued user
		// turns buffer in this child's stdin pipe during the hold — WriteTurn
		// returns on write, not on the child reading — so on release they drain in
		// FIFO order and the client observes their turns in submission order. Unset
		// ⟹ byte-identical (the tee above and runStreamJSON are untouched).
		if hold := os.Getenv(envStreamHold); hold != "" {
			waitForTriggerFile(hold)
		}
		// Approve rider (envStreamApprove, default-off): a rider on stream mode for
		// the permission round-trip e2e (#1139). Instead of echoing the prompt, the
		// fake ORIGINATES one permission request per user turn via control.Approve —
		// blocking until the daemon answers allow/deny — and reflects the daemon's
		// verdict into its assistant echo, so the e2e observes modal_shown /
		// modal_answer / verdict / deny-on-timeout end-to-end. Mutually exclusive with
		// the interrupt rider (a turn either does the approval dance or the plain
		// echo); single-consumer, so it lives here, not in the shared harness. The
		// socket path rides in via a file (envApproveSocketFile) the test writes after
		// startup (the socket is a random per-spawn path). Unset ⟹ byte-identical
		// (runStreamJSON untouched).
		if os.Getenv(envStreamApprove) != "" {
			runStreamJSONApprove(stdin, os.Stdout, os.Getenv(envApproveSocketFile))
			return
		}
		// Bogus rider (envStreamBogus, default-off): prepend one line of a
		// top-level type the parser has never seen, and one assistant message
		// carrying a block type it has never seen, ahead of the normal per-turn
		// reply. Both are the two drop sites the unrecognized-message diagnostic
		// exists to surface, so the e2e can assert they reach a client instead of
		// vanishing. Passed as a value so runStreamJSON stays a pure I/O seam;
		// unset ⟹ byte-identical to prior behaviour.
		//
		// Rate-limit rider (envStreamRateLimit, default-off): prepend one top-level
		// rate_limit_event line — the payload claude emits once per run whatever the
		// state of the usage-limit window — ahead of the normal per-turn reply, so an
		// e2e can drive the daemon's gate on it end-to-end. Unlike the bogus rider
		// this knob carries a VALUE, not a boolean: the value IS the line's
		// rate_limit_info.status, which is the gate's sole discriminator, so the
		// benign case and its arrival control differ in exactly that one string and
		// are provably on the same path. Empty ⟹ off ⟹ byte-identical.
		runStreamJSON(stdin, os.Stdout, os.Getenv(envStreamInterrupt) != "", os.Getenv(envStreamBogus) != "",
			os.Getenv(envStreamRateLimit))
		return
	}

	dir := mustEnv(envSessionsDir)
	initU := mustEnv(envInitialUUID)
	trig := mustEnv(envTrigger)

	// Stem-from-argv mode (envSessionIDFromArgv): envInitialUUID is process-wide,
	// so every child of one daemon opens the SAME <uuid>.jsonl — but the daemon
	// tails a minted per-conversation session at <mintedSessionID>.jsonl. Take the
	// stem from this spawn's own argv instead, which is the one per-child channel
	// already carrying that id (#1195). mustEnv above is deliberately unchanged:
	// the knob picks which value is USED, never whether the env is required, so a
	// harness that forgets INITIAL_UUID still fails loudly. No flag, or a value
	// that fails the stem guard, falls back to initU.
	if os.Getenv(envSessionIDFromArgv) != "" {
		if id, ok := argvSessionID(os.Args[1:]); ok {
			initU = id
		}
	}

	tui := os.Getenv(envTUI) != ""
	modalTrig := os.Getenv(envModalTrigger)
	escEndsTurn := os.Getenv(envEscEndsTurn) != ""
	// clearOnAnswer extends modal mode: after the modal is shown, the first stdin
	// bytes (the local pyry attach head's answer keystroke) clear the modal so
	// tui-driver fires EventKindPtyModalHidden and the daemon's #706 local arm
	// resolves it. Only meaningful with envModalTrigger; a no-op (byte-identical)
	// otherwise, so setting it without modal mode changes nothing.
	clearOnAnswer := modalTrig != "" && os.Getenv(envModalClearOnAns) != ""
	// clearRotates: watch stdin for the "/clear" slash command (typed by
	// supervisor.StartNewSession on a phone's new_session frame) and rotate the
	// live session JSONL once on the first match, so pyry's rotation watcher
	// follows the fresh <uuid>.jsonl into the registry. Additive and off by
	// default (byte-identical when unset), like escEndsTurn.
	clearRotates := os.Getenv(envClearRotates) != ""
	// trustTrig: the startup trust-folder dialog simulation (#993). A sibling of
	// modalTrig — come up idle, raise trustScreen on the trigger, then clear it on
	// the accept keystroke (and only the accept, never a bare ESC deny). Additive
	// and off by default (byte-identical when unset), like modalTrig / escEndsTurn.
	trustTrig := os.Getenv(envTrustTrigger)

	// Modal mode, Esc-ends-turn mode, and trust mode all put stdin into raw mode (as
	// the real claude TUI does) BEFORE the stdin reader starts, so a lone ESC
	// keystroke — the modal deny-on-timeout actuation, the remote interrupt, or the
	// trust deny, a bare 0x1b with no line terminator — reaches read() verbatim and
	// unbuffered, and so the trust accept "1\r" preserves its CR. The default
	// canonical line discipline would otherwise withhold a bare ESC indefinitely (it
	// buffers input until a newline) and map a "1\r" answer to "1\n" (ICRNL). Scoped
	// to these modes so every other mode stays byte-identical to today.
	if modalTrig != "" || escEndsTurn || trustTrig != "" {
		enterRawMode()
	}

	// The stdin reader is the only stdin consumer (a second reader would race
	// it for bytes). It runs when a stdin-log path is configured OR TUI mode is
	// on OR Esc-ends-turn mode is on OR clear-rotate mode is on OR trust mode is on
	// — TUI mode needs stdin read independently of logging so the spinner fires even
	// if STDIN_LOG is unset, Esc-ends-turn mode needs it to scan for the bare
	// interrupt ESC, clear-rotate mode needs it to scan for the "/clear" slash
	// command, and trust mode needs it to distinguish the accept "1\r" from a bare
	// ESC deny.
	logPath := os.Getenv(envStdinLog)
	if logPath != "" || tui || escEndsTurn || clearOnAnswer || clearRotates || trustTrig != "" {
		startStdinReader(logPath, tui, escEndsTurn, clearOnAnswer, clearRotates, trustTrig != "")
	}

	asstTrig := os.Getenv(envAssistantTrigger)
	jsonlTrig := os.Getenv(envJSONLTrigger)
	idleTrig := os.Getenv(envIdleTrigger)

	f := openSession(dir, initU)

	// Per-child JSONL trigger (envJSONLTriggerDir): built from the SAME stem
	// openSession just used, so each child of one daemon watches a distinct path
	// (and a distinct claimTrigger sidecar) and only the child a test names can
	// claim its drop. Computed once, before the loop — a later /clear rotation
	// mints a fresh random uuid but leaves this path on the initial stem, which is
	// the stem every test that sets this knob addresses.
	perChildJSONLTrig := ""
	if trigDir := os.Getenv(envJSONLTriggerDir); trigDir != "" {
		perChildJSONLTrig = filepath.Join(trigDir, initU+".jsonl.trig")
	}

	// TUI mode, modal-trigger mode, and trust-trigger mode all seed the idle-prompt
	// glyph once at startup. tui-driver's rolling snapshot buffer holds the single
	// write, so IsIdle stays true (and the modal class stays Unknown) until a later
	// write lands — no continuous redraw needed. For modal / trust mode the baseline
	// idle glyph is what makes the later modal-screen / trustScreen write a genuine
	// Unknown->Permission / Unknown->TrustFolder class transition (envModalTrigger and
	// envTrustTrigger are each mutually exclusive with envTUI, so this never
	// double-emits).
	if tui || modalTrig != "" || trustTrig != "" {
		writeStdout(idleGlyph)
	}

	rotated := false
	idled := false
	modalShown := false
	modalCleared := false
	escEnded := false
	trustShown := false
	trustCleared := false
	for {
		if !rotated {
			if _, err := os.Stat(trig); err == nil {
				f = rotateSession(f, dir)
				_ = os.Remove(trig)
				rotated = true
			}
		}
		// Idle-trigger mode (envIdleTrigger): claude came up busy (no startup
		// idle glyph); on the trigger's first appearance emit the idle glyph
		// once so WaitReady returns and the queued backlog drains. One-shot,
		// mirroring the rotation gate above.
		if idleTrig != "" && !idled {
			if emitIdleIfTriggered(idleTrig) {
				idled = true
			}
		}
		// Modal-trigger mode (envModalTrigger): the child came up idle (startup
		// idle glyph above); on the trigger's first appearance write the
		// permission-modal screen once so tui-driver detects an Unknown->Permission
		// transition and the daemon surfaces modal_shown. One-shot, mirroring the
		// rotation / idle gates above.
		if modalTrig != "" && !modalShown {
			if emitModalIfTriggered(modalTrig) {
				modalShown = true
			}
		}
		// Trust-trigger mode (envTrustTrigger): the child came up idle (startup idle
		// glyph above); on the trigger's first appearance write the startup
		// trust-folder dialog once so tui-driver detects an Unknown->TrustFolder
		// transition — the daemon surfaces modal_shown{trust} AND WaitReady holds the
		// queued turn (Readiness.TrustModal, #1013). One-shot, mirroring the modal
		// gate above.
		if trustTrig != "" && !trustShown {
			if emitTrustIfTriggered(trustTrig) {
				trustShown = true
			}
		}
		// A delivered turn (stdin bytes, signalled by the reader) grows the live
		// session JSONL so the daemon's #668 commit-confirm observes growth and
		// acks. Swap collapses chunked stdin into one append per poll cycle and
		// re-arms for a later turn. Targets the current f (post-rotate).
		if turnPending.Swap(false) {
			appendTurnGrowth(f)
		}
		// Esc-ends-turn mode (envEscEndsTurn): the stdin reader signalled a bare
		// ESC — the remote interrupt keystroke. Append one canned assistant
		// end_turn line so the daemon's structured-turn producer maps it to a
		// turn_end, making the Esc the CAUSE of the turn ending. One-shot,
		// mirroring the rotation / idle / modal gates: a second ESC is inert (a
		// re-interrupt of an already-ended turn is a no-op, matching claude).
		if escEndsTurn && !escEnded && escPending.Swap(false) {
			appendTurnEnd(f)
			escEnded = true
		}
		// Clear-rotate mode (envClearRotates): the stdin reader signalled the
		// "/clear" slash command was read — supervisor.StartNewSession typed it in
		// response to a phone's new_session frame. Rotate the live session JSONL
		// once so pyry's rotation watcher follows the fresh <uuid>.jsonl into the
		// registry, making the new_session frame the CAUSE of the on-disk rotation.
		// One-shot via the shared `rotated` gate: a second /clear is inert, and the
		// file trigger + this mode never both fire — mirroring the gates above.
		if clearRotates && !rotated && clearRotatePending.Swap(false) {
			f = rotateSession(f, dir)
			rotated = true
		}
		// Modal-clear-on-answer mode (envModalClearOnAns): the stdin reader
		// signalled the local head's answer keystroke arrived. Gated on modalShown
		// so a pre-modal keystroke cannot clear early — the short-circuit leaves
		// clearPending set until the modal is up, then fires on the first post-modal
		// keystroke. Clear the permission modal once so tui-driver fires
		// EventKindPtyModalHidden and the daemon's #706 local arm broadcasts
		// modal_dismissed{local}. One-shot via modalCleared, mirroring the gates above.
		if clearOnAnswer && modalShown && !modalCleared && clearPending.Swap(false) {
			clearModalScreen()
			modalCleared = true
		}
		// Trust-trigger mode (envTrustTrigger): the stdin reader signalled the trust
		// ACCEPT keystroke arrived (the first post-trust stdin bytes that were NOT a
		// bare ESC — Supervisor.AcceptTrust's "1\r"). Gated on trustShown so a
		// pre-trust keystroke cannot clear early (the && short-circuit leaves
		// trustAcceptPending set until the dialog is up). Clear the trust dialog once
		// so HasTrustModal goes false, WaitReady returns clean, and the held turn
		// delivers. A bare ESC deny never sets trustAcceptPending (the reader
		// distinguishes via containsBareESC), so the dialog stays up, the turn stays
		// held, and no reply is produced. One-shot via trustCleared, mirroring the
		// modal-clear gate above.
		if trustTrig != "" && trustShown && !trustCleared && trustAcceptPending.Swap(false) {
			clearTrustScreen()
			trustCleared = true
		}
		if asstTrig != "" {
			emitAssistantIfTriggered(asstTrig)
		}
		if jsonlTrig != "" {
			emitStructuredJSONLIfTriggered(f, jsonlTrig)
		}
		// Per-child JSONL trigger (envJSONLTriggerDir): the same consume as the
		// shared trigger above, on this child's own path. Watched independently —
		// neither knob disables the other, so a test may set both.
		if perChildJSONLTrig != "" {
			emitStructuredJSONLIfTriggered(f, perChildJSONLTrig)
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

// emitStructuredJSONLIfTriggered consumes the structured-JSONL trigger and
// appends its contents verbatim to f — the live session JSONL the daemon's
// structured-turn producer (cmd/pyry/interactive_turn_stream_v2.go) tails. The
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

// appendTurnEnd grows the current session JSONL f by claude's interruption marker
// (interruptMarkerLine) so the daemon's structured-turn producer maps it to
// turnevent.TurnEnd{cancelled} -> a turn_end envelope: the daemon's "turn stopped"
// signal after a remote interrupt. Runs ONLY on the main goroutine, preserving the
// single-writer-of-f invariant (see escPending / turnPending). Best-effort + fsync,
// mirroring emitStructuredJSONLIfTriggered; the e2e asserts downstream (the phone
// receives turn_end), never on the write itself.
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

// containsClearCommand reports whether buf contains the "/clear" slash-command
// bytes — the remote new-session keystroke supervisor.StartNewSession types
// (ClearInputLine's Ctrl-U, consumed by the canonical line discipline as VKILL,
// then TypePrompt's "/clear" + trailing "\r"). In clear-rotate mode
// (envClearRotates) the only stdin fakeclaude receives is that keystroke
// sequence, so a substring match cannot false-positive. The caller accumulates
// across reads before calling, so a "/clear" split byte-by-byte under raw
// discipline still matches once the full command has arrived.
func containsClearCommand(buf []byte) bool {
	return bytes.Contains(buf, []byte("/clear"))
}

// rotateSession closes the current session JSONL f and opens a fresh
// <uuid>.jsonl in the same dir — the /clear-driven UUID rotation both the file
// trigger and clear-rotate mode (envClearRotates) perform. Returns the new
// *os.File. Runs ONLY on the main goroutine, preserving the single-writer-of-f
// invariant. The old-fd close is best-effort: each write is fsynced by
// openSession / appendTurn*, so a failed close cannot lose committed data.
func rotateSession(f *os.File, dir string) *os.File {
	_ = f.Close()
	return openSession(dir, uuidV4())
}

// argvSessionID returns the session id the daemon pinned this spawn to — the
// value following the LAST "--session-id" or "--resume" in args — and whether
// one was found and is safe to use as a filename stem. Pure; never reads the
// environment. args excludes the program name (callers pass os.Args[1:]).
//
// Both flags are accepted because both name the same transcript stem: a create
// spawn gets "--session-id <id>" and a warm reattach gets "--resume <id>"
// (supervisor.buildClaudeArgs, #1164), so handling only one would leave the
// stem knob silently blind on warm starts. The LAST occurrence wins:
// buildClaudeArgs appends the flag at the end of argv, so the spawn-time value
// is authoritative over anything a template contributed. Only the two-token
// form is parsed — neither call site emits "--flag=value", so an = parser would
// be dead code.
//
// The stem guard is the security-relevant part: the returned value reaches
// filepath.Join in openSession and in the per-child JSONL trigger path, so a
// value carrying a separator or a dot could steer a write out of the sessions
// dir. This is the fake-side mirror of internal/transcript.ValidStem, which the
// daemon applies to the symmetric join (cmd/pyry/interactive_turn_stream_v2.go's
// resolveBoundSessionJSONL) and documents as a defense-in-depth branch selector
// for a value that is already trusted; a test fake that could be steered outside
// its sandbox is a worse place to skip it, not a better one. Inlined rather than
// importing internal/transcript to keep this stand-in near-zero-dependency —
// revisit if a second stem-validating site ever appears here. A rejected value
// reports not-found so the caller falls back to PYRY_FAKE_CLAUDE_INITIAL_UUID;
// it never falls back to an earlier occurrence, so the resolved stem is always
// either the spawn-time id or the env's.
func argvSessionID(args []string) (string, bool) {
	id := ""
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--session-id" || args[i] == "--resume" {
			id = args[i+1]
		}
	}
	if id == "" || strings.ContainsAny(id, `/\.`) {
		return "", false
	}
	return id, true
}

// unattributedStdinLogStem is the id component of the per-child stream stdin log
// of a child whose argv carried no usable session id. Unreachable through the
// daemon (streamsup.buildArgs ends every spawn's argv in "--session-id <id>" or
// "--resume <id>"), so this is a RENDERING choice rather than a defense: such a
// child stays alive and its bytes land under a name that says what happened,
// instead of the process dying or the bytes vanishing. It can never manufacture a
// green — the e2e reads the post-rotation id's file for its "which child" claim,
// and spans every file including this one for its no-/clear claim.
//
// The spelling cannot be a UUID — a UUID is hex digits and dashes, and this word
// carries 'u', 'n', 't', 'r' and 'i', none of which are hex — so it cannot collide
// with an id any path reaching here mints. Legible AND unambiguous, which is why no
// collision guard is specified.
const unattributedStdinLogStem = "unattributed"

// streamStdinLogPath returns the per-child path the stream tee appends to: the stem
// the env supplied, plus "." and the session id THIS spawn was pinned to (the value
// after the last --session-id/--resume on its argv). Pure over (stem, args); never
// reads the environment.
//
// Splicing an argv value into a path is safe here only because argvSessionID's stem
// guard already refuses any value containing "/", "\" or "." — the guard exists for
// exactly this kind of join (openSession, the per-child JSONL trigger path) — so the
// appended component can neither introduce a separator nor traverse, and the result
// always sits beside the stem in the same directory. Do not re-implement that guard:
// a rejected value arrives here as not-found and takes the unattributed arm.
func streamStdinLogPath(stem string, args []string) string {
	if id, ok := argvSessionID(args); ok {
		return stem + "." + id
	}
	return stem + "." + unattributedStdinLogStem
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
// permission modal — signal only, never writing stdout. When clearRotates is true
// it accumulates stdin across reads and, on the first "/clear" match, signals the
// main goroutine via clearRotatePending to rotate the session JSONL — signal only,
// never writing f. When trustTrig is true it scans each read for a bare ESC and,
// on a read that is NOT a bare ESC (the trust ACCEPT keystroke "1\r"), signals the
// main goroutine via trustAcceptPending to clear the trust dialog — signal only,
// never writing stdout; a bare ESC deny is left unsignalled so the dialog stays up.
func startStdinReader(logPath string, tui, escEndsTurn, clearOnAnswer, clearRotates, trustTrig bool) {
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
		// clearAcc accumulates stdin across reads for clear-rotate mode so a
		// "/clear" split byte-by-byte (raw discipline) still matches once whole;
		// clearDetected stops accumulating after the first match, bounding it.
		var clearAcc []byte
		clearDetected := false
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
				// Clear-rotate mode: the "/clear" slash command may span reads when
				// typed byte-by-byte under raw discipline and arrives as a single
				// "/clear\n" line under the default canonical discipline. Accumulate
				// across reads and, on the first complete match, signal the main
				// goroutine to rotate the session JSONL. Signal only (single-writer-
				// of-f), like escPending.
				if clearRotates && !clearDetected {
					clearAcc = append(clearAcc, buf[:n]...)
					if containsClearCommand(clearAcc) {
						clearRotatePending.Store(true)
						clearDetected = true
						clearAcc = nil
					}
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

// --- stream-json mode ------------------------------------------------------
//
// The types and functions below implement PYRY_FAKE_CLAUDE_STREAM_JSON: the
// line-delimited stream-json I/O the daemon's interactive_runner path drives. The
// wire shapes are hand-mirrored (not imported from internal/streamsup) to keep
// this stand-in zero-dependency, exactly like interruptMarkerLine. The inbound
// decode mirrors streamsup.userTurn (envelope.go); the outbound lines are
// byte-compatible with what streamsup.Parser (parser.go) maps to
// turnevent.TextChunk + turnevent.TurnEnd.

// streamSessionID is the fixed session_id fakeclaude stamps on every result line.
// streamsup.Parser.streamLine never decodes session_id, so the value is cosmetic —
// a literal avoids threading the daemon's injected --session-id through the argv
// the fake deliberately ignores.
const streamSessionID = "fake-stream"

// inUserTurn is the minimal decode of one inbound stdin line — only the fields the
// fake reads (the top-level type and the message's text content). It mirrors
// streamsup.userTurn (envelope.go), which is unexported there.
type inUserTurn struct {
	Type    string `json:"type"`
	Message struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
}

// inControlRequest is the minimal decode of one inbound control_request line — only
// the fields the interrupt mode reads (the top-level type and the request subtype).
// It mirrors streamsup.controlRequest (envelope.go), which is unexported there.
type inControlRequest struct {
	Type    string `json:"type"`
	Request struct {
		Subtype string `json:"subtype"`
	} `json:"request"`
}

// outAssistant / outResult are the two outbound stdout lines fakeclaude writes per
// user turn. Field order/tags produce shapes byte-compatible with
// stream_turn_drain_test.go's assistantTextLine / resultLine, so streamsup.Parser
// maps them to turnevent.TextChunk then turnevent.TurnEnd{end_turn}.
type outAssistant struct {
	Type    string         `json:"type"`
	Message outAsstMessage `json:"message"`
}

type outAsstMessage struct {
	ID      string         `json:"id"`
	Role    string         `json:"role"`
	Content []outTextBlock `json:"content"`
}

type outTextBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type outResult struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
}

// syncWriter is an io.Writer that fsyncs after every Write, mirroring the PTY
// stdin reader's per-write Sync (startStdinReader): a cross-process reader (the
// e2e polling PYRY_FAKE_CLAUDE_STDIN_LOG) then sees each turn's bytes promptly. It
// wraps the stream-mode stdin tee (main()); nothing else writes f, so no lock is
// needed.
type syncWriter struct{ f *os.File }

func (w syncWriter) Write(p []byte) (int, error) {
	n, err := w.f.Write(p)
	if err != nil {
		return n, err
	}
	return n, w.f.Sync()
}

// runStreamJSON reads line-delimited stream-json user-turn envelopes from r and,
// for each {"type":"user",…} line, writes one assistant text line (echoing the
// prompt) followed by one result line to w — one response per received user turn.
// Non-user lines (a blank line, an unparsable line) are read and ignored, mirroring
// streamsup.Parser's per-line resilience. Returns on r EOF (the daemon closed the
// child's stdin during teardown) or the first write error (the daemon's read end is
// gone — treat like EOF). Runs entirely on main()'s goroutine — a single reader, no
// shared state — so -race is clean by construction. The io.Reader/io.Writer seam is
// what lets the package unit test drive read→emit against in-memory buffers.
//
// honorInterrupt selects the interrupt mode (#1136, default-off): a user turn emits
// the assistant echo line ONLY (the result is withheld, so the turn stays in flight),
// and an interrupt control_request emits a result{error_during_execution} — which the
// daemon's parser maps to TurnEnd{cancelled}, ending the in-flight turn interrupted.
// With honorInterrupt false the control_request is ignored (the default the send /
// new_session / queue riders depend on staying byte-identical). The mode is stateless:
// it emits an interrupted result on each interrupt control_request.
//
// rateLimitStatus selects the rate-limit rider (#1411, default-off): non-empty
// prepends one top-level rate_limit_event line carrying that string as its
// rate_limit_info.status, ahead of the normal reply. Empty means off — which is why
// the parser's third rung (an absent or empty rate_limit_info) is deliberately not
// reachable through this seam and stays parser-tier.
func runStreamJSON(r io.Reader, w io.Writer, honorInterrupt, emitBogus bool, rateLimitStatus string) {
	// bufio.ReadString (not bufio.Scanner) so an arbitrarily long line — a
	// stream-json envelope carries a whole prompt — is never truncated by a token
	// cap, and the final non-newline-terminated bytes at EOF are still processed.
	br := bufio.NewReader(r)
	turn := 0
	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			b := []byte(line)
			if text, ok := userTurnText(b); ok {
				turn++
				msgID := fmt.Sprintf("m%d", turn)
				// The bogus rider emits its two unmappable shapes BEFORE the real
				// reply, so a test that waits on the reply has necessarily already
				// seen them — no ordering race to tune.
				if emitBogus {
					if werr := writeBogusLines(w, msgID); werr != nil {
						return
					}
				}
				// The rate-limit rider writes on the same terms and for the same
				// reason: BEFORE the reply, so turn_end reaching a client implies the
				// fed line has already been through the parser — no sleep, no poll,
				// no ordering race to tune.
				if rateLimitStatus != "" {
					if werr := writeRateLimitEvent(w, rateLimitStatus); werr != nil {
						return
					}
				}
				// Default mode ends the turn (echo + result{success}); interrupt mode
				// echoes ONLY, withholding the result so the turn stays in flight.
				var werr error
				if honorInterrupt {
					werr = writeAssistantEcho(w, msgID, text)
				} else {
					werr = writeStreamResponse(w, msgID, text)
				}
				if werr != nil {
					return
				}
			} else if honorInterrupt && interruptControlRequest(b) {
				// The daemon routed a phone interrupt to this child as a control_request:
				// end the in-flight turn with result{error_during_execution}.
				if werr := writeInterruptedResult(w); werr != nil {
					return
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// userTurnText decodes one inbound line and, when it is a {"type":"user",…}
// envelope, returns the first text block's text (empty string if the turn carries
// no text block) and true. A line that fails to decode or is not a user turn
// returns ("", false) — the caller skips it, producing no response.
func userTurnText(line []byte) (string, bool) {
	var in inUserTurn
	if err := json.Unmarshal(line, &in); err != nil {
		return "", false
	}
	if in.Type != "user" {
		return "", false
	}
	for _, block := range in.Message.Content {
		if block.Type == "text" {
			return block.Text, true
		}
	}
	return "", true
}

// interruptControlRequest reports whether line is the interrupt control_request the
// daemon writes to the child's stdin on a phone interrupt
// (streamsup.marshalInterruptEnvelope:
// {"type":"control_request",…"request":{"subtype":"interrupt"}}). It mirrors
// userTurnText's decode discipline: a minimal struct, and a line that fails to
// decode or is not an interrupt control_request returns false — the caller ignores
// it, preserving the parser's per-line resilience.
func interruptControlRequest(line []byte) bool {
	var in inControlRequest
	if err := json.Unmarshal(line, &in); err != nil {
		return false
	}
	return in.Type == "control_request" && in.Request.Subtype == "interrupt"
}

// writeStreamResponse writes fakeclaude's canned reply to one user turn: one
// assistant text line carrying text (the echoed prompt), then one
// result{subtype:"success"} line. Both are json.Marshal-encoded from local structs
// (never string-concatenated) so text — caller-controlled bytes — is escaped and
// each object is exactly one physical line regardless of prompt content. Returns
// the first marshal/write error.
func writeStreamResponse(w io.Writer, msgID, text string) error {
	if err := writeAssistantEcho(w, msgID, text); err != nil {
		return err
	}
	return writeJSONLine(w, outResult{
		Type:      "result",
		Subtype:   "success",
		SessionID: streamSessionID,
	})
}

// writeAssistantEcho writes the single assistant text line echoing text (the
// prompt) as message msgID — the assistant half of writeStreamResponse. It is split
// out so the interrupt mode (#1136) can emit the assistant line WITHOUT the trailing
// result, keeping the turn in flight; the line is byte-identical to the assistant
// line writeStreamResponse emits, so the default path is unchanged.
func writeAssistantEcho(w io.Writer, msgID, text string) error {
	return writeJSONLine(w, outAssistant{
		Type: "assistant",
		Message: outAsstMessage{
			ID:      msgID,
			Role:    "assistant",
			Content: []outTextBlock{{Type: "text", Text: text}},
		},
	})
}

// writeBogusLines writes the two shapes the daemon's stream parser has no
// mapping for: one top-level line of an invented type, and one assistant message
// whose single content block is of an invented type. They exercise the two drop
// sites that used to vanish into a debug log the production daemon never prints,
// so an e2e can assert both now reach a client as unrecognized_message frames.
//
// The needles are deliberately distinctive strings so the assertion cannot pass
// on some other frame's content.
func writeBogusLines(w io.Writer, msgID string) error {
	if err := writeJSONLine(w, map[string]any{
		"type":   bogusLineType,
		"detail": bogusLineNeedle,
	}); err != nil {
		return err
	}
	return writeJSONLine(w, map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"id":   msgID + "-bogus",
			"role": "assistant",
			"content": []any{map[string]any{
				"type":   bogusBlockType,
				"detail": bogusBlockNeedle,
			}},
		},
	})
}

// The invented type names and needles the bogus rider emits. Exported-in-spirit
// constants rather than inline literals so the e2e asserts against the same
// strings the fake writes.
const (
	bogusLineType    = "fake_future_event"
	bogusLineNeedle  = "bogus-line-needle"
	bogusBlockType   = "fake_future_block"
	bogusBlockNeedle = "bogus-block-needle"
)

// writeRateLimitEvent writes one top-level rate_limit_event line carrying the
// captured rate_limit_info object with `status` substituted for the caller's value.
// It is the fake half of #1411's two-tier proof: the daemon's gate reads status and
// nothing else, so one writer driven at two statuses puts the benign case and its
// arrival control on one path, differing in exactly that string.
//
// The object is transcribed from the committed capture
// (internal/e2e/realclaude/testdata/dropped_lines_v2.1.220.json, claude 2.1.220) —
// ALL SIX keys, not just the three streamsup.rateLimitInfo declares. Carrying the
// three overage keys costs nothing and makes "match the capture" literally true,
// while also feeding the parser keys its decode target deliberately omits.
//
// uuid and session_id are the capture's ENVELOPE identifiers, templated there
// ($SESSION_ID) rather than values to copy: the fake supplies its own. Neither is in
// the parser's decode target, so neither can reach the gate.
//
// A map[string]any like writeBogusLines: keys marshal sorted, so the line is
// deterministic without declaring a struct for a shape nothing else reads. Returns
// the first marshal/write error.
func writeRateLimitEvent(w io.Writer, status string) error {
	return writeJSONLine(w, map[string]any{
		"type": "rate_limit_event",
		"rate_limit_info": map[string]any{
			"status":                status,
			"resetsAt":              rateLimitResetsAt,
			"rateLimitType":         rateLimitLimitType,
			"overageStatus":         "rejected",
			"overageDisabledReason": "org_level_disabled",
			"isUsingOverage":        false,
		},
		"uuid":       rateLimitUUID,
		"session_id": streamSessionID,
	})
}

// The captured rate_limit_info values the rate-limit rider writes verbatim, plus the
// synthetic uuid it stamps in place of the capture's. Constants rather than inline
// literals for the bogus needles' reason: the e2e asserts against the same values the
// fake writes, across a main-package boundary it cannot import.
const (
	rateLimitResetsAt  = int64(1785699000)
	rateLimitLimitType = "five_hour"
	rateLimitUUID      = "44444444-4444-4444-8444-444444444444"
)

// writeInterruptedResult writes a single result{subtype:"error_during_execution"}
// line — the stream-json shape claude emits for an interrupt-terminated turn.
// streamsup.Parser maps this subtype to turnevent.TurnEnd{TurnEndReasonCancelled}
// (parser.go resultTurnEndReason), so the daemon reports the in-flight turn ended
// cancelled. Used only by the interrupt mode (#1136).
func writeInterruptedResult(w io.Writer) error {
	return writeJSONLine(w, outResult{
		Type:      "result",
		Subtype:   "error_during_execution",
		SessionID: streamSessionID,
	})
}

// writeJSONLine marshals v and writes it to w followed by a single '\n', so the
// emitted object is exactly one stream-json physical line.
func writeJSONLine(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := w.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
}

// --- stream-json approve rider (#1139) -------------------------------------
//
// The approve rider (envStreamApprove) makes fakeclaude ORIGINATE a permission
// request per user turn — the final leg of the stream permission round-trip. On the
// interactive stream runner claude would ask a daemon-hosted MCP approval tool and
// block until allow/deny; the fake drives the IDENTICAL daemon surface (mcp.approve →
// permbridge park → modal_shown → answer → verdict → deny-on-timeout) by calling
// control.Approve directly — the same client `pyry mcp-approve` calls — then reflects
// the daemon's verdict into its assistant echo so the e2e observes the loop
// end-to-end. Single-consumer to #1139, so it lives here, not in the shared harness.

// approveVerdict is the tri-state fakeclaude reflects from one control.Approve round
// trip. verdictError is the fail-closed client-side failure (socket unreachable / ctx
// expiry / unrecognised behavior) — tagged DISTINCTLY from verdictDeny so the e2e can
// tell a genuine daemon deny (the fail-closed proof) from a client error, which must
// never appear in a green run.
type approveVerdict int

const (
	verdictAllow approveVerdict = iota
	verdictDeny
	verdictError
)

// The needles fakeclaude writes into its assistant echo, one per verdict. The
// daemon's stream parser turns the assistant text into an assistant_delta the phone
// observes, so these ARE the e2e's verdict oracle. approveErrorNeedle must never
// appear in a green run — its distinctness is what makes the fail-closed proof
// airtight (a client-side error can never masquerade as a daemon deny).
const (
	approveAllowNeedle = "approve-allow"
	approveDenyNeedle  = "approve-deny"
	approveErrorNeedle = "approve-error"
)

// approveDialTimeout bounds fakeclaude's control.Approve wait. It is deliberately far
// ABOVE the daemon's approval window (PYRY_APPROVAL_TIMEOUT, ~2s in the timeout case),
// so the deny fakeclaude receives on a no-answer turn is the DAEMON's permbridge timer
// firing — not a fakeclaude self-timeout that would mask the daemon verdict. This
// margin is load-bearing for the fail-closed proof; control.Approve also requires a ctx
// deadline >= the daemon window (client.go), so it doubles as that patient-read line.
const approveDialTimeout = 30 * time.Second

// runStreamJSONApprove is the approve-rider counterpart of runStreamJSON: per
// {"type":"user",…} turn it originates ONE permission request via control.Approve
// (blocking until the daemon answers) and writes one assistant echo carrying the
// verdict needle + one result{success} line. runStreamJSON stays byte-identical (the
// send / interrupt / queue siblings depend on it), so the ~15-line read loop is
// duplicated rather than widening its signature — the cheaper trade. Non-user lines
// are ignored, and the loop returns on EOF or the first write error, exactly like
// runStreamJSON. Runs on main()'s single goroutine — control.Approve blocks it until
// the verdict lands, so no assistant output is written mid-approval; -race clean by
// construction.
func runStreamJSONApprove(r io.Reader, w io.Writer, socketFile string) {
	br := bufio.NewReader(r)
	turn := 0
	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			if _, ok := userTurnText([]byte(line)); ok {
				turn++
				msgID := fmt.Sprintf("m%d", turn)
				// A unique tool_use_id per turn (the registry correlation key).
				toolUseID := fmt.Sprintf("tu-1139-%d", turn)
				verdict := dialApproval(socketFile, toolUseID)
				if werr := writeVerdictResponse(w, msgID, verdict); werr != nil {
					return
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// dialApproval originates one approval against the daemon control socket and maps the
// outcome to an approveVerdict. The socket path is read LAZILY from socketFile (the
// test writes h.SocketPath there after startup — the socket is a random per-spawn path
// unknown before spawn, so it cannot ride an env VALUE directly). Any failure — no /
// empty socket file, unreachable socket, ctx expiry, or an unrecognised behavior — maps
// to verdictError: fail-closed (never verdictAllow), mirroring `pyry mcp-approve`'s
// error→deny, but tagged distinctly so the e2e surfaces a misconfiguration loudly
// instead of false-passing.
func dialApproval(socketFile, toolUseID string) approveVerdict {
	socket, err := readApproveSocket(socketFile)
	if err != nil {
		return verdictError
	}
	ctx, cancel := context.WithTimeout(context.Background(), approveDialTimeout)
	defer cancel()
	res, err := control.Approve(ctx, socket, control.ApprovePayload{
		ToolName:  "Bash",
		Input:     json.RawMessage(`{"cmd":"ls"}`),
		ToolUseID: toolUseID,
	})
	if err != nil {
		return verdictError
	}
	// ApproveResult.Behavior is the fixed wire vocabulary ("allow"/"deny",
	// control/protocol.go); anything else is contract drift → fail-closed error.
	switch res.Behavior {
	case "allow":
		return verdictAllow
	case "deny":
		return verdictDeny
	default:
		return verdictError
	}
}

// readApproveSocket reads the one-line socket-path file the test wrote and returns the
// trimmed path. A missing/empty file is an error (→ verdictError, fail-closed) —
// unreachable in a correct run (the test writes the file before sending the triggering
// turn), but it degrades to a loud approve-error, never a hang or an allow.
func readApproveSocket(socketFile string) (string, error) {
	if socketFile == "" {
		return "", errors.New("no approve socket file configured")
	}
	data, err := os.ReadFile(socketFile)
	if err != nil {
		return "", err
	}
	socket := strings.TrimSpace(string(data))
	if socket == "" {
		return "", errors.New("approve socket file is empty")
	}
	return socket, nil
}

// writeVerdictResponse writes fakeclaude's canned reply for one originated approval:
// one assistant text line carrying the verdict needle, then one result{success} line
// (the same shape writeStreamResponse emits, so the daemon's parser maps them to
// TextChunk + TurnEnd). The needle is the e2e's verdict oracle. verdictError maps to
// the distinct approveErrorNeedle (the default). Returns the first marshal/write error.
func writeVerdictResponse(w io.Writer, msgID string, verdict approveVerdict) error {
	needle := approveErrorNeedle
	switch verdict {
	case verdictAllow:
		needle = approveAllowNeedle
	case verdictDeny:
		needle = approveDenyNeedle
	}
	if err := writeAssistantEcho(w, msgID, needle); err != nil {
		return err
	}
	return writeJSONLine(w, outResult{
		Type:      "result",
		Subtype:   "success",
		SessionID: streamSessionID,
	})
}
