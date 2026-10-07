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
//	                               removed. Built for the structured-receive
//	                               e2e (#642), which fed turn events to a
//	                               daemon reader of this file; that reader
//	                               was deleted with the PTY path (#1543), so
//	                               nothing in the daemon maps these lines any
//	                               more and no in-tree test sets this.
//	                               Default off — when unset, no watch.
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
//	                               live session JSONL and fsyncs. The daemon reader
//	                               that mapped it to a turn_end{cancelled} (for the
//	                               interrupt-live e2e #794 and #1244) was deleted
//	                               with the PTY path (#1543); the mode now has no
//	                               driver and no consumer.
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
//	PYRY_FAKE_CLAUDE_STREAM_REPLAY_FIRST optional path to raw stream-json stdout
//	                               bytes. When set in stream mode, the first user
//	                               envelope writes these bytes once instead of the
//	                               canned echo/result pair. The bytes are not parsed
//	                               or normalized; stream mode still opens no PTY or
//	                               transcript. Default off.
//	PYRY_FAKE_CLAUDE_STREAM_REPLAY_SECOND optional path to a second raw fragment.
//	                               Requires STREAM_REPLAY_RELEASE and is emitted
//	                               once when that signal appears, after the first
//	                               fragment and without another user envelope.
//	PYRY_FAKE_CLAUDE_STREAM_REPLAY_RELEASE optional filesystem signal paired with
//	                               STREAM_REPLAY_SECOND. Recognized control requests
//	                               remain serviceable while the fake waits for it.
//	PYRY_FAKE_CLAUDE_INITIALIZE_MODELS optional path to a JSON array used as the
//	                               initialize response's model menu in stream-json
//	                               mode. Unset uses the canned initializeModels.
//	PYRY_FAKE_CLAUDE_STREAM_CAN_USE_TOOL optional JSON object whose keys are the
//	                               inner can_use_tool request fields. In stream-json
//	                               mode, each user turn emits the configured request
//	                               and waits for its correlated control_response before
//	                               ending the turn. Invalid JSON or an unknown key
//	                               disables the rider. Default off.
//	PYRY_FAKE_CLAUDE_STREAM_EXIT_AFTER_CAN_USE_TOOL optional; "1" makes the
//	                               stream child exit immediately after emitting the
//	                               configured can_use_tool request. It drives
//	                               origin-child teardown while the ask is parked.
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
//	PYRY_FAKE_CLAUDE_REJECT_ABSENT_RESUME  optional directory. When set, a
//	                               stream-mode spawn whose argv's LAST id flag is
//	                               "--resume <id>" and for which <dir>/<id>.jsonl
//	                               does not exist writes real claude's own refusal
//	                               to stderr and exits 1, instead of serving the
//	                               turn. That is the never-established-session
//	                               crash-loop #1631 closes, made observable in the
//	                               fake-daemon tier: a session that launched but ran
//	                               no turn establishes no transcript, so every
//	                               --resume respawn is refused forever on a widening
//	                               backoff until the daemon's by-id probe switches
//	                               the flag to --session-id. Stream mode only, by
//	                               construction — the check lives inside the
//	                               PYRY_FAKE_CLAUDE_STREAM_JSON branch, so no
//	                               PTY-tier test can observe it even if the env
//	                               leaked. Default off — when unset, byte-identical
//	                               to prior behaviour.
//	PYRY_FAKE_CLAUDE_STREAM_ROSTER optional decimal COUNT. When it parses to a
//	                               positive number, each stream-mode user turn is
//	                               preceded by one system/background_tasks_changed
//	                               line canning that many task entries (#2080) —
//	                               claude's mid-turn report of what is running in
//	                               the background, which nothing else in this file
//	                               produces. See writeBackgroundTaskRoster for the
//	                               fixture and for why the knob carries a count
//	                               rather than a boolean. Stream mode only.
//	                               Unset, empty, unparsable or non-positive ⟹ off
//	                               ⟹ byte-identical to prior behaviour, which is
//	                               fail-CLOSED: a typo silently disables the rider
//	                               rather than enabling some default roster, so a
//	                               miswired test fails as "no frame arrived"
//	                               instead of passing on a fixture nobody chose.
//	PYRY_FAKE_CLAUDE_STREAM_MODEL_WINDOWS
//	                               optional. When non-empty, every stream-mode
//	                               result line carries a modelUsage map reporting
//	                               a per-model context window (#2107) — the field
//	                               claude uses to say how large each model's
//	                               window actually is, which nothing else in this
//	                               file produces. See riderModelUsage for the
//	                               fixture and its provenance. A boolean rather
//	                               than a value, unlike the two knobs above,
//	                               because there is nothing here to count or to
//	                               choose: the fixture is one canned map, its
//	                               numbers copied from a committed capture and one
//	                               key's variant suffix from #2118's live probe.
//	                               Stream mode only.
//	                               Unset or empty ⟹ off ⟹ byte-identical to prior
//	                               behaviour (the field is omitempty).
//	PYRY_FAKE_CLAUDE_STREAM_SESSION_FACTS
//	                               optional. When non-empty, every stream-mode
//	                               user turn is preceded by one system/init line
//	                               (#2315) — the line claude opens each turn with,
//	                               and the one this file wrote no form of before.
//	                               See writeSystemInitLine for the fixture and its
//	                               provenance. A boolean like the knob above and
//	                               for its reason: the fixture is one canned line,
//	                               with nothing to count and nothing to choose.
//	                               Stream mode only.
//	                               Unset or empty ⟹ off ⟹ byte-identical to prior
//	                               behaviour.
//	PYRY_FAKE_CLAUDE_MCP_STATUS     optional. When non-empty, answer an inbound
//	                               mcp_status control request with one canned,
//	                               fully populated server row. The daemon sends
//	                               this request only after an eligible child's
//	                               initialize reply. Unset or empty leaves the
//	                               request unanswered, preserving prior hermetic
//	                               test traffic. Stream mode only.
//
// The binary lives under internal/e2e/internal/ to visibility-fence it from
// non-e2e callers. Because TUI mode makes this file carry claude-TUI
// substrate glyphs, it is on the cmd/substrate-guard allowlist (#603),
// mirroring the sanctioned internal/agentrun/ptyrunner/helper_test.go
// exemption.
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	envSessionsDir        = "PYRY_FAKE_CLAUDE_SESSIONS_DIR"
	envInitialUUID        = "PYRY_FAKE_CLAUDE_INITIAL_UUID"
	envTrigger            = "PYRY_FAKE_CLAUDE_TRIGGER"
	envStdinLog           = "PYRY_FAKE_CLAUDE_STDIN_LOG"
	envAssistantTrigger   = "PYRY_FAKE_CLAUDE_ASSISTANT_TRIGGER"
	envJSONLTrigger       = "PYRY_FAKE_CLAUDE_JSONL_TRIGGER"
	envTUI                = "PYRY_FAKE_CLAUDE_TUI"
	envIdleTrigger        = "PYRY_FAKE_CLAUDE_IDLE_TRIGGER"
	envModalTrigger       = "PYRY_FAKE_CLAUDE_MODAL_TRIGGER"
	envEscEndsTurn        = "PYRY_FAKE_CLAUDE_ESC_ENDS_TURN"
	envModalClearOnAns    = "PYRY_FAKE_CLAUDE_MODAL_CLEAR_ON_ANSWER"
	envTrustTrigger       = "PYRY_FAKE_CLAUDE_TRUST_TRIGGER"
	envSessionIDFromArgv  = "PYRY_FAKE_CLAUDE_SESSION_ID_FROM_ARGV"
	envJSONLTriggerDir    = "PYRY_FAKE_CLAUDE_JSONL_TRIGGER_DIR"
	envStreamJSON         = "PYRY_FAKE_CLAUDE_STREAM_JSON"
	envStreamInterrupt    = "PYRY_FAKE_CLAUDE_STREAM_INTERRUPT"
	envStreamHold         = "PYRY_FAKE_CLAUDE_STREAM_HOLD"
	envStreamApprove      = "PYRY_FAKE_CLAUDE_STREAM_APPROVE"
	envStreamBogus        = "PYRY_FAKE_CLAUDE_STREAM_BOGUS"
	envStreamRateLimit    = "PYRY_FAKE_CLAUDE_STREAM_RATE_LIMIT"
	envStreamWithholdMode = "PYRY_FAKE_CLAUDE_STREAM_WITHHOLD_MODE_ACK"
	envStreamRoster       = "PYRY_FAKE_CLAUDE_STREAM_ROSTER"
	envStreamModelWindows = "PYRY_FAKE_CLAUDE_STREAM_MODEL_WINDOWS"
	envStreamResetTo      = "PYRY_FAKE_CLAUDE_STREAM_RESET_TO"
	envStreamSessionFacts = "PYRY_FAKE_CLAUDE_STREAM_SESSION_FACTS"
	envStreamMCPStatus    = "PYRY_FAKE_CLAUDE_MCP_STATUS"
	envStreamCanUseTool   = "PYRY_FAKE_CLAUDE_STREAM_CAN_USE_TOOL"
	envStreamReplayFirst  = "PYRY_FAKE_CLAUDE_STREAM_REPLAY_FIRST"
	envStreamReplaySecond = "PYRY_FAKE_CLAUDE_STREAM_REPLAY_SECOND"
	envStreamReplaySignal = "PYRY_FAKE_CLAUDE_STREAM_REPLAY_RELEASE"
	envInitializeModels   = "PYRY_FAKE_CLAUDE_INITIALIZE_MODELS"
	envApproveSocketFile  = "PYRY_FAKE_CLAUDE_APPROVE_SOCKET_FILE"
	envRejectAbsentResume = "PYRY_FAKE_CLAUDE_REJECT_ABSENT_RESUME"
	assistantMaxBytes     = 64 * 1024
	pollInterval          = 50 * time.Millisecond
)

const envStreamExitAfterCanUseTool = "PYRY_FAKE_CLAUDE_STREAM_EXIT_AFTER_CAN_USE_TOOL"

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
	// Argv fidelity (#2446), ABOVE every mode branch and every env read: real
	// claude parses its flags before it decides what to be, so an argv it refuses
	// must be refused here whatever mode this process was going to serve, and
	// every later e2e inherits the guard without opting in. Ungated for the reason
	// argvIDFlag's stem guard is ungated — it fires only on an argv the daemon
	// should never compose, so there is nothing for a test to switch off.
	if refusesIDPair(os.Args[1:]) {
		fatalf("%s", refusedIDPairMessage)
	}
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
		// Reject rider (envRejectAbsentResume, default-off): the value is the
		// directory claude would keep this session's <id>.jsonl in. A spawn whose
		// winning id flag is --resume against an id with no transcript there is
		// refused exactly as real claude refuses it — stderr line, exit 1 — instead
		// of serving turns. Checked FIRST, above the stdin tee and the startup hold,
		// because a refused spawn must consume no stdin and observe no trigger; a
		// child that got as far as either would have serviced the daemon in a state
		// real claude never reaches. Empty ⟹ off ⟹ byte-identical.
		//
		// The stat is by-id and never a scan, mirroring the daemon-side probe it
		// exists to exercise (streamsup.useCreateForm): the id is spliced into a
		// path only after argvIDFlag's stem guard has refused any value carrying a
		// separator or a dot.
		if dir := os.Getenv(envRejectAbsentResume); dir != "" {
			if id, resume, ok := argvIDFlag(os.Args[1:]); ok && resume {
				if _, err := os.Stat(filepath.Join(dir, id+".jsonl")); err != nil {
					fatalf("No conversation found with session ID: %s", id)
				}
			}
		}
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
		//
		// Withheld-mode-ack rider (envStreamWithholdMode, default-off): read the
		// set_permission_mode control_request as usual but emit no ack, so a caller
		// can drive a child that never confirms its posture (#2067). Unlike every
		// rider above this one SUPPRESSES an answer rather than adding a line, which
		// is why it is a rider at all: the answer itself is unconditional, on the
		// `initialize` arm's terms. Unset ⟹ off ⟹ the ack is emitted.
		//
		// Roster rider (envStreamRoster, default-off): prepend one
		// system/background_tasks_changed line canning N task rows ahead of the
		// normal per-turn reply (#2080). Like the rate-limit knob it carries a
		// VALUE rather than a boolean, and here the value is the ROW COUNT —
		// writeBackgroundTaskRoster gives both reasons. Parsed rather than
		// tested for emptiness, and the parse FAILS CLOSED: strconv.Atoi's error
		// value is 0, which is off, so a typo disables the rider instead of
		// enabling some default roster nobody chose. Non-positive ⟹ off ⟹
		// byte-identical.
		//
		// Model-window rider (envStreamModelWindows, default-off): ride a
		// modelUsage map onto each turn's result line, reporting a per-model
		// context window (#2107). Tested for emptiness rather than parsed, the
		// envStreamWithholdMode spelling, because there is nothing to count and
		// nothing to choose — the fixture is one canned map. Unset ⟹ off ⟹
		// byte-identical, the field being omitempty.
		//
		// Init rider (envStreamSessionFacts, default-off): prepend one
		// system/init line — the line claude opens every turn with — ahead of the
		// normal per-turn reply (#2315), so an e2e can drive the daemon's
		// session_facts frame end-to-end. Tested for emptiness rather than
		// parsed, the envStreamModelWindows spelling and for its reason: the
		// fixture is one canned line, with nothing to count and nothing to
		// choose. Unset ⟹ off ⟹ byte-identical.
		replay, err := loadStreamReplay(os.Getenv(envStreamReplayFirst), os.Getenv(envStreamReplaySecond),
			os.Getenv(envStreamReplaySignal))
		if err != nil {
			fatalf("load stream replay: %v", err)
		}
		rosterTasks, _ := strconv.Atoi(os.Getenv(envStreamRoster))
		runStreamJSONConfigured(stdin, os.Stdout, streamRunConfig{
			honorInterrupt:  os.Getenv(envStreamInterrupt) != "",
			emitBogus:       os.Getenv(envStreamBogus) != "",
			rateLimitStatus: os.Getenv(envStreamRateLimit),
			withholdModeAck: os.Getenv(envStreamWithholdMode) != "",
			rosterTasks:     rosterTasks,
			modelWindows:    os.Getenv(envStreamModelWindows) != "",
			resetToID:       os.Getenv(envStreamResetTo),
			emitInit:        os.Getenv(envStreamSessionFacts) != "",
			replay:          replay,
			inputCloser:     os.Stdin,
		})
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
	// on OR Esc-ends-turn mode is on OR trust mode is on — TUI mode needs stdin
	// read independently of logging so the spinner fires even if STDIN_LOG is
	// unset, Esc-ends-turn mode needs it to scan for the bare interrupt ESC, and
	// trust mode needs it to distinguish the accept "1\r" from a bare ESC deny.
	logPath := os.Getenv(envStdinLog)
	if logPath != "" || tui || escEndsTurn || clearOnAnswer || trustTrig != "" {
		startStdinReader(logPath, tui, escEndsTurn, clearOnAnswer, trustTrig != "")
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
		// ESC — the remote interrupt keystroke. Append claude's interruption
		// marker (interruptMarkerLine) to the session JSONL; no daemon reader maps
		// it any more (deleted with the PTY path, #1543). One-shot,
		// mirroring the rotation / idle / modal gates: a second ESC is inert (a
		// re-interrupt of an already-ended turn is a no-op, matching claude).
		if escEndsTurn && !escEnded && escPending.Swap(false) {
			appendTurnEnd(f)
			escEnded = true
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
