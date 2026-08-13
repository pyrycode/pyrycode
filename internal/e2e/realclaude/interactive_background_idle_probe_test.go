//go:build e2e_realclaude

package realclaude

// Evidence probe for #1240 — does the interactive turn-state surface report a
// turn idle while a backgrounded Bash command is still alive?
//
// This file is NOT a regression gate. It asserts nothing about claude's
// behaviour: it stages ONE turn on the production stream-json interactive
// daemon, records every frame the phone received, and classifies the run into
// one of five outcomes. #1241 draws the client-facing conclusion from it and
// reuses bgIdleRecordTurn.
//
// # The question, and why the code does not answer it
//
// cmd/pyry/interactive_turn_v2.go:208-217 emits turn_end and then transitions
// to idle in the turnevent.TurnEnd arm, unconditionally — nothing consults
// whether the tool left work behind. Since claude returns a tool_result the
// moment a Bash command's timeout expires and leaves the command running in
// the background (claude 2.1.220, observed 2026-07-27), that code PREDICTS a
// client is told the turn finished while the work it described is still
// running. A prediction about code is not a measurement of the wire: it says
// nothing about what the surrounding frames carried, and nothing about whether
// the command was alive at that instant rather than killed.
//
// TestInteractiveStreamRunningTurn already treats an early idle as a TEST
// HAZARD and steers around it (runningTurnPrompt, ":225 — do NOT run it in the
// background"). Whether the same early idle is a defect for real clients has
// never been settled. That is what this probe records.
//
// # The lever
//
// BASH_DEFAULT_TIMEOUT_MS=5000 on the process that spawns claude — #1223's
// established trigger (7 firing runs out of 7 against claude 2.1.220).
// BASH_MAX_TIMEOUT_MS alone does not fire and is not set. `cat <fifo>` is not
// sleep-leading, so it is never auto-backgrounded for the other reason. The
// prompt says nothing about timeouts or backgrounding: that is the measured
// axis.
//
// Two hops carry the lever, both os.Environ()-based, so it reaches claude with
// no production change: test -> daemon (interactive_bootstrap_liveness_test.go
// :403) -> claude (internal/streamsup/runner.go:555). The second hop's append
// sits inside `if r.cfg.Env != nil`, which LOOKS like a broken chain, but
// passthrough survives both arms — non-nil appends to os.Environ(), and nil
// leaves cmd.Env nil, which makes os/exec inherit the parent environment
// verbatim. Do not "fix" it. Whether the lever actually fires is what this
// probe measures, and bgIdleDidNotFire is a legitimate result.
//
// # The liveness pair (AC2): a flip observed in THIS rig
//
// Holding the FIFO's write end proves only that the command could not have
// FINISHED. A command that was KILLED records identically and would make the
// headline claim false, so it is excluded separately by fifoLiveRead.
//
// fifoLiveRead proved it FLIPS in its own self-check (#1239,
// TestFIFOReaderLiveness_FlipsAcrossOneReaderLifetime). That proof does not
// transfer here: a read wired to the wrong path, or to a FIFO something else
// holds open, produces the same lone reader-present. So the flip is
// re-established on THIS path across THIS command's lifetime:
//
//   - pre-rendezvous: holdProbeFIFO has created the FIFO and its goroutine is
//     parked in the blocking O_WRONLY open, but no reader has ever opened it.
//     The read MUST return no-reader (ENXIO, mode prw-------).
//   - at the instant idle is recorded: reader-present iff the backgrounded
//     command is still alive.
//
// Two separately-constructed FIFOs could not establish that. A pre-rendezvous
// read that does NOT return no-reader means the instrument is not
// discriminating in this rig, and every outcome collapses to bgIdleUnresolved.
// Measured during refinement, do not re-derive: three consecutive
// pre-rendezvous reads returned no-reader and did NOT perturb the parked
// writer — a failed ENXIO open creates no fd, so there is nothing to close.
//
// DO NOT RELEASE THE FIFO BEFORE BOTH READS. fifoLiveRead opens a transient
// SECOND write end and closes it immediately; POSIX delivers EOF to a FIFO
// reader only when the LAST writer closes, so while holdProbeFIFO's end is
// held the reader never notices. A read taken after that hold is released
// makes its own Close the last writer closing — it would kill the very command
// it is measuring. The t.Cleanup LIFO below already gives the right order
// (FIFO released -> record written -> phone closed -> daemon stopped); this is
// why it matters. Nothing here kills the command, so #1239's
// kill-is-not-the-sync-point hazard is a thing to avoid, not a step to perform.
//
// # Why the recorded idle belongs to THIS turn (AC3(b))
//
// turn_state{idle} reaches the wire from exactly one site:
// `Handle` in interactive_turn_v2.go, inside the turnevent.TurnEnd arm, which is
// entered only when inTurn is true (:209) and which immediately calls
// endTurn(). inTurn is set only by startTurnIfNeeded (:267), which every
// content arm calls together with a transitionTo(thinking|responding) — so no
// idle can reach the wire without a preceding non-idle turn_state for the same
// conversation, in the same record. One turn is driven, into one conversation
// seeded fresh in a per-test authenticated HOME, over one phone that is the
// only interactive conn. Every frame carries conversation_id verbatim and the
// record is ordered by receive time, so this is refuted by the record itself
// rather than assumed.
//
// # Attribution (AC3(c)) — see bgIdleCountFIFONaming
//
// fifoLiveRead reports on the PIPE, not on a pid, so it cannot by itself
// attribute the held read end to the joined tool_use_id: a second `cat` on the
// same path reads identically. The gap is closed by COUNTING frames already
// recorded, not by a third pid matcher — the package already carries
// probeDescendantsFromPS and #1235 is building another.
//
// # Everything published here is allowlisted — see bgIdleFrameFromEnvelope
//
// All payload extraction happens in ONE five-arm switch, so a field not
// extracted there can never reach the artefact. EnvDelta is a fixed literal:
// nothing in this file may call os.Environ() or read any child's environment,
// because the test process and the daemon both carry CLAUDE_CODE_OAUTH_TOKEN /
// ANTHROPIC_API_KEY (fixtures.go:98-99) and this record is pasted into a PUBLIC
// issue.
//
// # Running it
//
//	PYRY_PROBE_INTERACTIVE_BG_IDLE=1 go test -tags e2e_realclaude -timeout 10m -v \
//	  -run '^TestInteractiveStreamBackgroundIdleProbe$' ./internal/e2e/realclaude/
//
// TestBgIdleCountFIFONaming runs offline, with no claude, no credentials and
// no gate.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// bgIdleEnableEnv gates the live probe. `make e2e-realclaude` runs the whole
// package glob, and this probe asserts nothing about pyry, so an ungated live
// turn would buy preship cost and no signal.
const bgIdleEnableEnv = "PYRY_PROBE_INTERACTIVE_BG_IDLE"

// The lever, the fixture names and the record's fixed metadata. Every symbol
// in this file takes the bgIdle prefix: #1226, #1230, #1234, #1235 and this
// family's siblings all add files to this package concurrently, and a
// branch-overlap check does NOT catch a same-package identifier collision
// (the branches compile independently and collide only once both are on main).
const (
	bgIdleBashTimeoutEnv = "BASH_DEFAULT_TIMEOUT_MS"
	bgIdleBashTimeoutMS  = "5000"
	bgIdleFIFOName       = "bgidle-hold"
	bgIdleRecordName     = "interactive-bg-idle.json"
	bgIdleArtifactPrefix = "pyry-1240-probe-*"
	bgIdleModel          = "haiku"
	bgIdleSendID         = 2 // first post-handshake message, matching every sibling
	bgIdleMessageID      = "m-1"
	bgIdleStateIdle      = "idle"
)

// bgIdleMaxFrames caps the record. A cap is never silent: past it the recorder
// stops appending, counts into FramesDropped and KEEPS draining, because the
// terminal-idle detection and the idle-instant read must still fire. A
// truncated record that read as "that is all the phone received" would
// misstate AC1.
const bgIdleMaxFrames = 2000

// The truncation tell (AC3(c)). turnbridge/outbound.go:47 caps a summary at
// maxSummaryLen = 200 runes and :189-194 appends one ellipsis rune past the
// cap, so a truncated summary is EXACTLY 201 runes ending "…". A truncated
// input_summary that cut off the FIFO path is an UNRESOLVED match, not a
// non-match.
const (
	bgIdleSummaryRunes          = 200
	bgIdleTruncatedSummaryRunes = bgIdleSummaryRunes + 1
	bgIdleEllipsis              = "…"
)

// Which of the two terminal conditions ended the drain. The budget arm is not
// optional: a drain that can only end on idle cannot record the did-not-fire
// outcome AC4 sanctions.
const (
	bgIdleTerminatedIdle   = "terminal-idle"
	bgIdleTerminatedBudget = "budget-expired"
)

// The five outcomes. bgIdleEarlyIdle is the headline finding; every other
// value is a legitimate, publishable result. No conclusion is ever drawn from
// a broken instrument.
const (
	bgIdleUnresolved      = "unresolved"
	bgIdleDidNotFire      = "did-not-fire"
	bgIdleEarlyIdle       = "early-idle"
	bgIdleCommandNotAlive = "command-not-alive"
	bgIdleNoEarlyIdle     = "no-early-idle"
)

// bgIdleStructuralArgument is AC3(b): why the recorded idle cannot belong to
// an earlier turn or another conversation. Carried in the artefact so the
// published evidence states it, rather than leaving it to the reader.
const bgIdleStructuralArgument = "turn_state{idle} reaches the wire from exactly one site — " +
	"cmd/pyry/interactive_turn_v2.go:216, inside the turnevent.TurnEnd arm, which is entered " +
	"only when inTurn is true (:209) and which immediately calls endTurn(). inTurn is set only " +
	"by startTurnIfNeeded (:267), which every content arm calls together with a " +
	"transitionTo(thinking|responding), so no idle can reach the wire without a preceding " +
	"non-idle turn_state for the same conversation, in the same record. One turn is driven, " +
	"into one conversation seeded fresh in a per-test authenticated HOME, over one phone that " +
	"is the only interactive conn. Every frame carries conversation_id verbatim and the record " +
	"is ordered by receive time, so this is refuted by the record itself rather than assumed."

// The runner path and its attribution status (AC4), taken deliberately on the
// criterion's SECOND arm. selectInteractiveRunner (cmd/pyry/main.go:667) never
// logs its choice, so the config value only echoes what the rig wrote. The one
// non-authored artefact — the MCP-approve config main.go:795-801 writes iff
// InteractiveRunner == "stream-json" — is created by os.CreateTemp("",
// "pyry-mcp-approve-*.json") (cmd/pyry/`writeMCPApproveConfig`) in a SHARED $TMPDIR
// where any other stream-json pyry on the operator's machine also has one; its
// only discriminator is the embedded socket path, which perConvHarness does
// not carry and which #1240 forbids adding. Reconstructing an attribution by
// set-differencing /tmp/pyry-sock-* across the harness call would be ~45 lines
// whose own failure mode is exactly the failure AC4 exists to prevent. An
// instrument that can misattribute is worse than an honestly-stated absence.
const (
	bgIdleRunnerPath        = "stream-json"
	bgIdleRunnerAttribution = "RIG-AUTHORED, no non-authored corroboration recorded: " +
		"writeStreamInteractiveConfig (interactive_stream_liveness_test.go:155) wrote " +
		`{"interactive_runner":"stream-json"} into <home>/.pyry/config.json before the daemon ` +
		"spawned. selectInteractiveRunner (cmd/pyry/main.go:667) never logs its choice, so " +
		"citing the config value merely echoes the rig. The daemon starting at all is NOT " +
		"corroboration either: an unrecognised value fails fast (main.go:675, no silent PTY " +
		"fallback), but both \"pty\" and \"stream-json\" start fine, so a clean start proves " +
		"only that the value parsed."
)

// bgIdleFrame is one frame the phone received, transcribed through the single
// extraction allowlist in bgIdleFrameFromEnvelope.
//
// IsError is a *bool, not a bool. is_error:false is the load-bearing value
// here — turnbridge/outbound.go:84 sets it from Status == ToolStatusFailed, so
// the background path carries the SAME false a clean success carries.
// omitempty on a plain bool would erase exactly the field AC3(a) needs.
//
// assistant_delta.text is NEVER recorded, only its length:
// interactive_turn_v2.go:70-75 states the package rule that application output
// never reaches a log, and this record is pasted into a public issue.
type bgIdleFrame struct {
	Index       int    `json:"index"`
	At          string `json:"at"`
	SinceSendMS int64  `json:"since_send_ms"`
	Type        string `json:"type"`

	ConversationID string `json:"conversation_id,omitempty"`
	TurnID         string `json:"turn_id,omitempty"`

	// turn_state
	State string `json:"state,omitempty"`

	// tool_use (ToolUseID is shared with tool_result — it is AC3(a)'s join key)
	ToolUseID    string `json:"tool_use_id,omitempty"`
	Name         string `json:"name,omitempty"`
	InputSummary string `json:"input_summary,omitempty"`

	// tool_result
	IsError       *bool  `json:"is_error,omitempty"`
	ResultSummary string `json:"result_summary,omitempty"`

	// turn_end
	StopReason string `json:"stop_reason,omitempty"`

	// assistant_delta — LENGTH ONLY.
	TextLen int `json:"text_len,omitempty"`

	// PayloadDecodeFailed flags a payload that did not unmarshal into its own
	// wire struct. A boolean rather than the error text: an encoding/json
	// syntax error echoes a byte of the payload, and nothing from a payload
	// reaches this artefact except through the allowlist above.
	PayloadDecodeFailed bool `json:"payload_decode_failed,omitempty"`
}

// bgIdleRecord is the whole artefact: one JSON file, no sibling verbatim
// captures (the wire values are already summaries).
type bgIdleRecord struct {
	Ticket                string   `json:"ticket"`
	ClaudeVersion         string   `json:"claude_version"`
	RunnerPath            string   `json:"runner_path"`
	RunnerPathAttribution string   `json:"runner_path_attribution"`
	Model                 string   `json:"model"`
	EnvDelta              []string `json:"env_delta"`
	Prompt                string   `json:"prompt"`
	ConversationID        string   `json:"conversation_id"`
	Workdir               string   `json:"workdir"`
	FIFOPath              string   `json:"fifo_path"`

	SentAt string `json:"sent_at"`
	// RendezvousAt is the instant the command actually BEGAN — holdProbeFIFO's
	// blocking open returning, not the send. nil when it never fired.
	RendezvousAt *string `json:"rendezvous_at"`

	// The AC2 pair, recorded whole rather than summarised to a boolean. A read
	// not taken is nil, never a zero-value verdict.
	PreRendezvousRead *fifoLiveOutcome `json:"pre_rendezvous_read"`
	IdleLiveRead      *fifoLiveOutcome `json:"idle_live_read"`
	IdleReadAt        string           `json:"idle_read_at,omitempty"`

	TerminatedOn  string        `json:"terminated_on"`
	Frames        []bgIdleFrame `json:"frames"`
	FramesDropped int           `json:"frames_dropped"`

	FIFONamingMatches             int `json:"fifo_naming_matches"`
	FIFONamingTruncatedCandidates int `json:"fifo_naming_truncated_candidates"`

	StructuralArgument string `json:"structural_argument"`

	Outcome       string `json:"outcome"`
	OutcomeDetail string `json:"outcome_detail"`
}

// TestInteractiveStreamBackgroundIdleProbe stages one turn on the production
// stream-json interactive daemon around a Bash command claude backgrounds on
// timeout expiry, records every frame, and classifies the run.
//
// Nothing after the harness call is fatal except a decrypt/decode failure in
// the recorder (which makes the whole record untrustworthy). did-not-fire,
// unresolved attribution and a broken instrument are all publishable data.
func TestInteractiveStreamBackgroundIdleProbe(t *testing.T) {
	if os.Getenv(bgIdleEnableEnv) != "1" {
		t.Skipf("#1240 interactive background-idle probe: skipped because %s != 1.\n"+
			"This is an EVIDENCE PROBE, not a regression gate — a skip here is the normal "+
			"`make e2e-realclaude` outcome and carries no signal about pyry's behaviour. It "+
			"costs one live claude turn.\n"+
			"Run it explicitly:\n"+
			"  %s=1 go test -tags e2e_realclaude -timeout 10m -v \\\n"+
			"    -run '^TestInteractiveStreamBackgroundIdleProbe$' ./internal/e2e/realclaude/",
			bgIdleEnableEnv, bgIdleEnableEnv)
	}

	// Deliberately NOT t.TempDir(): that is removed when the test ends, and the
	// operator needs these files afterwards to compose AC4's comment.
	artifactDir, err := os.MkdirTemp("", bgIdleArtifactPrefix)
	if err != nil {
		t.Fatalf("#1240: create artifact dir: %v", err)
	}
	t.Logf("#1240 probe artifacts: %s", bgIdleRedact(artifactDir))

	// MUST precede the harness: spawnBootstrapDaemon snapshots os.Environ() at
	// `spawnBootstrapDaemon`, so a later Setenv never
	// reaches the daemon — and the daemon passes its environment to claude
	// verbatim.
	t.Setenv(bgIdleBashTimeoutEnv, bgIdleBashTimeoutMS)

	h, convID := startStreamRunningTurnHarness(t)

	rec := &bgIdleRecord{
		Ticket:                "1240",
		ClaudeVersion:         probeClaudeVersion(resolveClaudeBin(t)),
		RunnerPath:            bgIdleRunnerPath,
		RunnerPathAttribution: bgIdleRunnerAttribution,
		Model:                 bgIdleModel,
		// A FIXED LITERAL, never harvested. os.Environ() is not called
		// anywhere in this file — see the header's allowlist note.
		EnvDelta:           []string{bgIdleBashTimeoutEnv + "=" + bgIdleBashTimeoutMS},
		ConversationID:     convID,
		Workdir:            h.workdir,
		StructuralArgument: bgIdleStructuralArgument,
		Outcome:            bgIdleUnresolved,
		OutcomeDetail:      "did not reach a classification point",
	}
	// Registered FIRST in this body so t.Cleanup's LIFO runs it LAST of ours:
	// a structural t.Fatalf below still leaves partial evidence on disk. The
	// resulting teardown order is FIFO released -> record written -> phone
	// closed -> daemon stopped, so the turn gets a real chance to end on its
	// own before the daemon is signalled.
	t.Cleanup(func() { bgIdleWriteRecord(t, artifactDir, rec) })

	// MUST follow the harness (it needs h.workdir), which is also what yields
	// the LIFO above.
	fifoPath := filepath.Join(h.workdir, bgIdleFIFOName)
	rec.FIFOPath = fifoPath
	rendezvous := holdProbeFIFO(t, fifoPath)

	// AC2's first read. After holdProbeFIFO (the path must exist, else the read
	// fails on the Lstat arm) and before the send (no `cat` can exist yet), so
	// the only correct answer is no-reader.
	preRead := fifoLiveRead(fifoPath)
	rec.PreRendezvousRead = &preRead
	t.Logf("#1240 pre-rendezvous liveness read: %s (%s)", preRead.Verdict, preRead.Detail)

	// The rendezvous is stamped on its own goroutine into a BUFFERED(1)
	// channel, never into rec: a direct field write would race the test
	// goroutine under -race. Buffer size 1 is load-bearing twice — it prevents
	// that race, and it prevents a goroutine leak past test end. If the
	// rendezvous never fires, this goroutine stays parked until holdProbeFIFO's
	// cleanup opens the read end non-blockingly to release its own parked
	// writer, which CLOSES rendezvous (`holdProbeFIFO`,
	// closed rather than sent on, so a second receiver is safe); the send then
	// lands in the buffer with no reader left and the goroutine retires.
	rendezvousAt := make(chan time.Time, 1)
	go func() {
		<-rendezvous
		rendezvousAt <- time.Now()
	}()

	nonce := time.Now().UnixNano()
	rec.Prompt = bgIdlePrompt(fifoPath, nonce)

	sentAt := time.Now()
	rec.SentAt = sentAt.Format(time.RFC3339Nano)
	sealSendMessage(t, h.phone, h.initSend, bgIdleSendID, convID, bgIdleMessageID, rec.Prompt)

	// AC2's second read, taken synchronously at the instant idle is recorded.
	atIdle := func() {
		idleRead := fifoLiveRead(fifoPath)
		rec.IdleLiveRead = &idleRead
		rec.IdleReadAt = time.Now().Format(time.RFC3339Nano)
	}

	frames, dropped, terminatedOn := bgIdleRecordTurn(t, h.phone, h.initRecv, convID,
		sentAt, perTurnReplyBudget, atIdle)
	rec.Frames = frames
	rec.FramesDropped = dropped
	rec.TerminatedOn = terminatedOn

	select {
	case ts := <-rendezvousAt:
		at := ts.Format(time.RFC3339Nano)
		rec.RendezvousAt = &at
	default:
		// Never fired: the command never started. Recorded as nil, not stamped.
	}

	rec.FIFONamingMatches, rec.FIFONamingTruncatedCandidates =
		bgIdleCountFIFONaming(frames, fifoPath)

	bgIdleClassify(rec)
	t.Logf("#1240 outcome=%s terminated_on=%s frames=%d dropped=%d matched=%d truncated_candidates=%d\n  %s",
		rec.Outcome, rec.TerminatedOn, len(rec.Frames), rec.FramesDropped,
		rec.FIFONamingMatches, rec.FIFONamingTruncatedCandidates, bgIdleRedact(rec.OutcomeDetail))
}

// bgIdlePrompt drives exactly one Bash call running `cat <fifoPath>` verbatim.
//
// It says NOTHING about timeouts or backgrounding — that is the measured axis,
// and #1223's system prompt was deliberately silent on it for the same reason.
// "Exactly once, verbatim, nothing else" is setup, not measurement. This is the
// opposite posture from runningTurnPrompt
// (`runningTurnPrompt`), which steers claude AWAY from
// backgrounding and must never be reused or edited here.
//
// Only two values are interpolated: fifoPath, a t.TempDir()-derived absolute
// path plus a fixed const basename carrying no shell metacharacters, and
// nonce. nonce is a CACHE-BUSTER, not security randomness — it must stay
// wall-clock, must not be "upgraded" to crypto/rand, and must not be read as
// though it were a token.
func bgIdlePrompt(fifoPath string, nonce int64) string {
	return fmt.Sprintf("Use the Bash tool exactly once to run this command verbatim: "+
		"cat %s. Do not chain it with && or ;, do not add any flags or redirections, "+
		"do not comment on it, and do nothing else. run=%d", fifoPath, nonce)
}

// bgIdleRecordTurn reads binary->phone frames until one of exactly TWO terminal
// conditions and records every one it decrypts.
//
// The decrypt discipline is transcribed from drainForResponding
// (interactive_stream_running_turn_test.go:242-290) and is load-bearing: the
// receive nonce is sequential, so EVERY noise_msg must be decrypted in receive
// order or the CipherState desyncs, while a non-noise_msg control frame must be
// skipped WITHOUT decrypting so it does not advance the nonce. Getting that
// wrong fails as a decrypt error mid-record, not as a missing frame.
//
// Terminal conditions: a turn_state{idle} for convID (bgIdleTerminatedIdle), or
// budget elapsing (bgIdleTerminatedBudget). A fakephone.ErrReceiveTimeout
// re-loops into the budget check — a blocked `cat` produces no frames, and that
// is a legitimate path to the budget arm.
//
// On the terminal idle the frame is appended FIRST, then atIdle runs
// synchronously, then the drain returns. Both instants are recorded, so "at the
// instant idle was recorded" is a fact in the artefact rather than a claim in
// prose. atIdle may be nil — that is the seam #1241 reuses with a different
// hook, or none.
//
// A decrypt or JSON-decode failure is t.Fatalf, matching every existing drain
// in this package: a nonce desync makes the whole record untrustworthy, and
// that is a broken instrument, never a datum about claude. The message carries
// the error ONLY — adding the raw or sealed frame "to help debug it" would put
// ciphertext and payload into CI logs.
func bgIdleRecordTurn(t *testing.T, phone *fakephone.Client, cs *noise.CipherState,
	convID string, sentAt time.Time, budget time.Duration, atIdle func()) (frames []bgIdleFrame, dropped int, terminatedOn string) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return frames, dropped, bgIdleTerminatedBudget
		}
		raw, err := phone.ReceiveBytes(remaining)
		if err != nil {
			if errors.Is(err, fakephone.ErrReceiveTimeout) {
				continue // no frames while `cat` blocks — re-loop into the budget check
			}
			t.Fatalf("phone receive (recording turn): %v", err)
		}
		var inner protocol.InnerFrameV2
		if err := json.Unmarshal(raw, &inner); err != nil {
			t.Fatalf("decode inner frame: %v", err)
		}
		if inner.Type != protocol.TypeNoiseMsg {
			continue // non-noise_msg control frame does not advance the receive nonce
		}
		cipher, err := base64.StdEncoding.DecodeString(inner.Data)
		if err != nil {
			t.Fatalf("decode inner data: %v", err)
		}
		plain, err := cs.Decrypt(cipher)
		if err != nil {
			t.Fatalf("phone decrypt (receive-nonce desync?): %v", err)
		}
		var env protocol.Envelope
		if err := json.Unmarshal(plain, &env); err != nil {
			t.Fatalf("decode envelope: %v", err)
		}

		at := time.Now()
		f := bgIdleFrameFromEnvelope(env)
		f.At = at.Format(time.RFC3339Nano)
		f.SinceSendMS = at.Sub(sentAt).Milliseconds()
		if len(frames) < bgIdleMaxFrames {
			f.Index = len(frames)
			frames = append(frames, f)
		} else {
			// The cap is never silent, and draining continues past it so the
			// terminal-idle detection and atIdle still fire.
			dropped++
		}

		if f.Type == protocol.TypeTurnState && f.ConversationID == convID && f.State == bgIdleStateIdle {
			if atIdle != nil {
				atIdle()
			}
			return frames, dropped, bgIdleTerminatedIdle
		}
	}
}

// bgIdleFrameFromEnvelope is the ONE extraction site: a five-arm allowlist plus
// a default that records the envelope type and its conversation id and stops. A
// field that is not extracted here can never reach the published artefact —
// which is the property that keeps unrecognized_message.Raw (claude's verbatim
// offending message, protocol/interactive.go:118-145) and assistant_delta.text
// out of a public issue by construction rather than by care.
func bgIdleFrameFromEnvelope(env protocol.Envelope) bgIdleFrame {
	f := bgIdleFrame{Type: env.Type}
	if len(env.Payload) == 0 {
		return f
	}
	switch env.Type {
	case protocol.TypeTurnState:
		var p protocol.TurnStatePayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			f.PayloadDecodeFailed = true
			return f
		}
		f.ConversationID = p.ConversationID
		f.State = p.State
	case protocol.TypeToolUse:
		var p protocol.ToolUsePayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			f.PayloadDecodeFailed = true
			return f
		}
		f.ConversationID = p.ConversationID
		f.TurnID = p.TurnID
		f.ToolUseID = p.ToolUseID
		f.Name = p.Name
		f.InputSummary = p.InputSummary
	case protocol.TypeToolResult:
		var p protocol.ToolResultPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			f.PayloadDecodeFailed = true
			return f
		}
		f.ConversationID = p.ConversationID
		f.TurnID = p.TurnID
		f.ToolUseID = p.ToolUseID
		isErr := p.IsError
		f.IsError = &isErr // pointer: false is the load-bearing value here
		f.ResultSummary = p.ResultSummary
	case protocol.TypeTurnEnd:
		var p protocol.TurnEndPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			f.PayloadDecodeFailed = true
			return f
		}
		f.ConversationID = p.ConversationID
		f.TurnID = p.TurnID
		f.StopReason = p.StopReason
	case protocol.TypeAssistantDelta:
		var p protocol.AssistantDeltaPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			f.PayloadDecodeFailed = true
			return f
		}
		f.ConversationID = p.ConversationID
		f.TurnID = p.TurnID
		f.TextLen = len(p.Text) // LENGTH ONLY — the text is never recorded
	default:
		// ack, stall, api_retry, compacting, unrecognized_message, … — decoded
		// into a shape that can hold NOTHING but the conversation id.
		var p struct {
			ConversationID string `json:"conversation_id"`
		}
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			f.PayloadDecodeFailed = true
			return f
		}
		f.ConversationID = p.ConversationID
	}
	return f
}

// bgIdleCountFIFONaming closes AC3(c)'s attribution gap with frames already
// recorded rather than with a pid.
//
// matched counts tool_use frames whose input_summary contains the FIFO path.
// truncatedCandidates counts tool_use frames that do NOT contain it but whose
// input_summary is exactly bgIdleTruncatedSummaryRunes runes ending in the
// ellipsis — the wire tell that turnbridge/outbound.go's truncate cut the
// summary. Such a frame may have had the path cut off, so it is an UNRESOLVED
// match, not a non-match.
//
// Attribution is sound iff matched == 1 && truncatedCandidates == 0.
//
// The match is deliberately NOT gated on Name == "Bash": ToolUsePayload.Name
// comes from turnevent.ToolStart.Title (outbound.go:74), a value this design
// has not measured, and gating on it would add a second unvetted matcher whose
// failure is silent. The FIFO path in the summary is the discriminator; Name is
// recorded verbatim so a non-Bash tool naming the path is visible to a reader.
func bgIdleCountFIFONaming(frames []bgIdleFrame, fifoPath string) (matched, truncatedCandidates int) {
	for _, f := range frames {
		if f.Type != protocol.TypeToolUse {
			continue
		}
		if strings.Contains(f.InputSummary, fifoPath) {
			matched++
			continue
		}
		if utf8.RuneCountInString(f.InputSummary) == bgIdleTruncatedSummaryRunes &&
			strings.HasSuffix(f.InputSummary, bgIdleEllipsis) {
			truncatedCandidates++
		}
	}
	return matched, truncatedCandidates
}

// bgIdleClassify fills rec.Outcome and rec.OutcomeDetail. Clauses are evaluated
// in order and the first match wins; the detail always names the clause that
// fired and carries the joined tool_use_id, the matched tool_result's is_error
// and turn_end.stop_reason where present.
//
// Two clauses are worth spelling out.
//
// The PRE-RENDEZVOUS clause runs before everything: AC2 requires the read to be
// shown to discriminate IN THIS RIG, and a pre-rendezvous read that does not
// answer no-reader — when nothing has ever opened the read end — means the
// instrument is answering wrong about a known ground truth. A read pinned to
// reader-present would answer the idle-instant read identically, which is the
// exact false positive the pair exists to prevent, so no outcome survives it.
//
// The matched==0 SPLIT: with no terminal idle it is bgIdleDidNotFire (the lever
// did not fire — a legitimate, publishable result AC4 sanctions); with one it
// is bgIdleUnresolved, because an idle not preceded by any FIFO-naming tool_use
// cannot be the headline.
//
// Why a model-authored string cannot manufacture the headline: input_summary is
// written by the model, so a decoy tool_use that merely MENTIONS the path
// inflates matched. Both readings are safe. Decoy plus the real `cat` gives
// matched == 2 -> unresolved. Decoy only gives matched == 1, but nothing holds
// the read end, so the idle-instant read returns no-reader ->
// bgIdleCommandNotAlive. bgIdleEarlyIdle needs matched == 1 AND a
// reader-present read on that same path, and no single model-authored string
// satisfies both. The frame count is a COMPLEMENT to the liveness pair, never a
// substitute for it.
func bgIdleClassify(rec *bgIdleRecord) {
	var (
		matched   = rec.FIFONamingMatches
		truncated = rec.FIFONamingTruncatedCandidates
		sawIdle   = rec.TerminatedOn == bgIdleTerminatedIdle
		pre       = rec.PreRendezvousRead
		idleRead  = rec.IdleLiveRead
	)

	// The Bash tool_use naming the FIFO, and the tool_result joined to it BY
	// tool_use_id — never by prose (#563 and #1219 each paid once for prose
	// matching). The recorder returns the instant it records the terminal idle,
	// so that idle is the last frame in the record and "preceded by" reduces to
	// "present in the record at all".
	toolUseID := ""
	for _, f := range rec.Frames {
		if f.Type == protocol.TypeToolUse && strings.Contains(f.InputSummary, rec.FIFOPath) {
			toolUseID = f.ToolUseID
			break
		}
	}
	var result *bgIdleFrame
	if toolUseID != "" {
		for i := range rec.Frames {
			if rec.Frames[i].Type == protocol.TypeToolResult && rec.Frames[i].ToolUseID == toolUseID {
				result = &rec.Frames[i]
				break
			}
		}
	}
	stopReason := "<no turn_end recorded>"
	for _, f := range rec.Frames {
		if f.Type == protocol.TypeTurnEnd {
			stopReason = f.StopReason
			break
		}
	}
	joined := fmt.Sprintf("tool_use_id=%q tool_result.is_error=%s turn_end.stop_reason=%q "+
		"matched=%d truncated_candidates=%d terminated_on=%s",
		toolUseID, bgIdleIsErrorText(result), stopReason, matched, truncated, rec.TerminatedOn)

	set := func(outcome, format string, args ...any) {
		rec.Outcome = outcome
		rec.OutcomeDetail = fmt.Sprintf(format, args...) + " | " + joined
	}

	switch {
	case pre == nil:
		set(bgIdleUnresolved, "the pre-rendezvous liveness read was never taken, so the read "+
			"was not shown to discriminate in this rig")
	case pre.Verdict == fifoLiveInstrumentFailed:
		set(bgIdleUnresolved, "the pre-rendezvous liveness read returned %q (%s); a broken "+
			"instrument is never recorded as a dead command", pre.Verdict, pre.Detail)
	case pre.Verdict != fifoLiveNoReader:
		set(bgIdleUnresolved, "the pre-rendezvous liveness read returned %q on %s although no "+
			"reader had ever opened it (%s); the read is not discriminating in this rig, and one "+
			"pinned to %q would answer the idle-instant read the same way",
			pre.Verdict, rec.FIFOPath, pre.Detail, fifoLiveReaderPresent)
	case sawIdle && idleRead == nil:
		set(bgIdleUnresolved, "the terminal turn_state{idle} was recorded but the idle-instant "+
			"liveness read was never taken")
	case idleRead != nil && idleRead.Verdict == fifoLiveInstrumentFailed:
		set(bgIdleUnresolved, "the idle-instant liveness read returned %q (%s); a broken "+
			"instrument is never recorded as a dead command", idleRead.Verdict, idleRead.Detail)
	case truncated > 0:
		set(bgIdleUnresolved, "%d tool_use input_summary value(s) are exactly %d runes ending "+
			"%q, so a truncated summary may have cut off %s — an unresolved match, not a "+
			"non-match", truncated, bgIdleTruncatedSummaryRunes, bgIdleEllipsis, rec.FIFOPath)
	case matched == 0 && !sawIdle:
		set(bgIdleDidNotFire, "no tool_use named %s and no terminal turn_state{idle} arrived "+
			"within the budget — the background lever did not fire", rec.FIFOPath)
	case matched == 0:
		set(bgIdleUnresolved, "a terminal turn_state{idle} was recorded but no tool_use in the "+
			"record names %s, so the idle cannot be attributed to the command", rec.FIFOPath)
	case matched > 1:
		set(bgIdleUnresolved, "%d tool_use frames name %s, so the held read end cannot be "+
			"attributed to one tool_use_id", matched, rec.FIFOPath)
	case result == nil && !sawIdle:
		set(bgIdleDidNotFire, "the tool_use naming %s never got a tool_result sharing its "+
			"tool_use_id and no terminal turn_state{idle} arrived — the call was still open and "+
			"`cat` still blocked", rec.FIFOPath)
	case result == nil:
		set(bgIdleUnresolved, "a terminal turn_state{idle} was recorded but no tool_result "+
			"shares the tool_use's tool_use_id, so the turn's end cannot be tied to the command")
	case sawIdle && idleRead.Verdict == fifoLiveReaderPresent:
		set(bgIdleEarlyIdle, "turn_state{idle} was emitted while a process still held the read "+
			"end of %s: the surface reported the turn finished while the work it described was "+
			"still running", rec.FIFOPath)
	case sawIdle && idleRead.Verdict == fifoLiveNoReader:
		set(bgIdleCommandNotAlive, "turn_state{idle} was recorded but nothing held the read end "+
			"of %s at that instant, so the command was not alive and this run does not support "+
			"the headline claim", rec.FIFOPath)
	case !sawIdle:
		set(bgIdleNoEarlyIdle, "the matched tool_result landed but no terminal turn_state{idle} "+
			"arrived within the budget — the surface did NOT report idle while the command ran")
	default:
		set(bgIdleUnresolved, "no classification clause matched")
	}
}

// bgIdleIsErrorText renders the joined tool_result's is_error for the outcome
// detail. A pointer that is non-nil and false is the value AC3(a) needs, so
// "false" and "no matching tool_result" must never render the same.
func bgIdleIsErrorText(result *bgIdleFrame) string {
	if result == nil || result.IsError == nil {
		return "<no matching tool_result>"
	}
	return fmt.Sprintf("%t", *result.IsError)
}

// bgIdleWriteRecord is the ONE redaction choke point. It marshals, replaces the
// operator's home with $HOME across the whole blob, logs the redacted bytes and
// only then writes them at 0600. One choke point catches every field, and the
// attribution counting in bgIdleCountFIFONaming runs on the raw in-memory
// values so redaction cannot perturb it.
//
// The log precedes the write so the bytes survive in the test output even when
// the write fails; the write failing is t.Errorf, not t.Fatalf, because the
// run's evidence is the deliverable and the remaining cleanups must still run.
func bgIdleWriteRecord(t *testing.T, dir string, rec *bgIdleRecord) {
	t.Helper()
	blob, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Errorf("#1240: marshal record: %v", err)
		return
	}
	redacted := append([]byte(bgIdleRedact(string(blob))), '\n')
	t.Logf("#1240 record:\n%s", redacted)

	path := filepath.Join(dir, bgIdleRecordName)
	if err := os.WriteFile(path, redacted, 0o600); err != nil {
		t.Errorf("#1240: write record %s: %v", bgIdleRedact(path), err)
	}
}

// bgIdleRedact replaces the operator's home (`realHome`, captured at
// package load before any t.Setenv) with $HOME.
//
// The empty-realHome guard is load-bearing, not defensive noise:
// strings.ReplaceAll(s, "", "$HOME") inserts $HOME between EVERY character of
// the record.
func bgIdleRedact(s string) string {
	if realHome == "" {
		return s
	}
	return strings.ReplaceAll(s, realHome, "$HOME")
}

// --- offline self-check -----------------------------------------------------

// TestBgIdleCountFIFONaming pins AC3(c)'s discriminator. It runs offline: no
// claude, no credentials, no env gate, no skip.
//
//	go test -tags e2e_realclaude -run '^TestBgIdleCountFIFONaming$' -v ./internal/e2e/realclaude/
//
// The last two rows are the pair that discriminates. A matcher that treated
// every non-match as a plain non-match would pass every other row and silently
// assert a false attribution: a summary truncated at the wire cap may have had
// the FIFO path cut off, so it is UNRESOLVED, not absent. 200-versus-201 and
// "the ellipsis is one rune, three bytes" are exactly the boundary that is
// wrong on first write, so the 201-rune fixture asserts its own rune count —
// a future change to maxSummaryLen fails here rather than in a live run — and
// is built from multibyte runes, because turnbridge's truncate is rune-aware
// and a byte-length implementation must not pass.
func TestBgIdleCountFIFONaming(t *testing.T) {
	t.Parallel()

	const fifoPath = "/var/folders/zz/T/pyry-home/work/" + bgIdleFIFOName

	// Exactly bgIdleTruncatedSummaryRunes runes, ending in the ellipsis, and
	// carrying no occurrence of fifoPath: the wire shape of a summary the
	// producer cut at the cap.
	truncatedSummary := strings.Repeat("é", bgIdleSummaryRunes) + bgIdleEllipsis
	if got := utf8.RuneCountInString(truncatedSummary); got != bgIdleTruncatedSummaryRunes {
		t.Fatalf("truncated fixture is %d runes; want %d — the wire tell is a rune count, and "+
			"a fixture that does not match it tests nothing", got, bgIdleTruncatedSummaryRunes)
	}
	if strings.Contains(truncatedSummary, fifoPath) {
		t.Fatalf("truncated fixture contains the FIFO path; the row only discriminates while it does not")
	}

	// bgIdleSummaryRunes runes, no ellipsis: an uncut summary that simply does
	// not name the path.
	plainSummary := strings.Repeat("ü", bgIdleSummaryRunes)
	if got := utf8.RuneCountInString(plainSummary); got != bgIdleSummaryRunes {
		t.Fatalf("plain fixture is %d runes; want %d", got, bgIdleSummaryRunes)
	}

	toolUse := func(summary string) bgIdleFrame {
		return bgIdleFrame{Type: protocol.TypeToolUse, Name: "Bash", InputSummary: summary}
	}
	naming := toolUse(`{"command":"cat ` + fifoPath + `"}`)

	tests := []struct {
		name          string
		frames        []bgIdleFrame
		wantMatched   int
		wantTruncated int
		why           string
	}{
		{
			name: "one tool_use names the path",
			frames: []bgIdleFrame{
				{Type: protocol.TypeTurnState, State: "responding"},
				naming,
				{Type: protocol.TypeToolResult, ToolUseID: "toolu-1"},
			},
			wantMatched: 1,
			why:         "exactly one match is what makes the attribution sound",
		},
		{
			name: "no tool_use names the path",
			frames: []bgIdleFrame{
				{Type: protocol.TypeTurnState, State: "responding"},
				toolUse(`{"command":"echo hello"}`),
			},
			why: "a short summary that does not name the path is a plain non-match",
		},
		{
			name:        "two tool_use frames name the path",
			frames:      []bgIdleFrame{naming, toolUse(`{"command":"echo ` + fifoPath + `"}`)},
			wantMatched: 2,
			why:         "a decoy naming the path must inflate matched so the run is unresolved",
		},
		{
			name:          "a truncated summary is an unresolved match",
			frames:        []bgIdleFrame{toolUse(truncatedSummary)},
			wantTruncated: 1,
			why: "the producer cut this summary at the cap, so it may have carried the path — " +
				"recording it as a non-match would assert a false attribution",
		},
		{
			name:   "an uncut summary at the cap is a plain non-match",
			frames: []bgIdleFrame{toolUse(plainSummary)},
			why: "exactly the cap with no ellipsis was never cut, so nothing can have been " +
				"hidden by truncation",
		},
		{
			name: "non-tool_use frames are never counted",
			frames: []bgIdleFrame{
				{Type: protocol.TypeToolResult, ResultSummary: "moved to the background: " + fifoPath},
				{Type: protocol.TypeAssistantDelta, TextLen: 12},
				{Type: protocol.TypeTurnEnd, StopReason: "end_turn"},
			},
			why: "only a tool_use announces the command; a result summary naming the path is " +
				"not a second invocation",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			matched, truncated := bgIdleCountFIFONaming(tt.frames, fifoPath)
			if matched != tt.wantMatched {
				t.Errorf("matched = %d; want %d — %s", matched, tt.wantMatched, tt.why)
			}
			if truncated != tt.wantTruncated {
				t.Errorf("truncatedCandidates = %d; want %d — %s", truncated, tt.wantTruncated, tt.why)
			}
		})
	}
}
