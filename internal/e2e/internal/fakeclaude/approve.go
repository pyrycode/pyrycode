package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/pyrycode/pyrycode/internal/control"
)

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
// margin is load-bearing for the fail-closed proof.
//
// Since #1932 that margin buys the proof only under the condition the timeout case now
// arranges: NOBODY LEFT WHO COULD ANSWER. The daemon's window is a re-check interval,
// re-armed for as long as an interactive client is connected to a surfaced approval, so
// a no-answer turn with an answerer still attached never denies at all — it parks past
// this ctx and reflects approve-error, which the e2e forbids. The window is the deny
// deadline only once the last answerer is gone.
//
// control.Approve derives no read
// deadline of its own (#1929), so this ctx deadline is the whole of the rider's bound —
// it is what stops a wedged daemon parking fakeclaude for the length of a test run.
const approveDialTimeout = 30 * time.Second

// approveToolName and approveToolInput describe the ONE gated call the rider
// originates per turn. Both the tool_use block writeAssistantToolUse puts on the
// stream and the control.Approve payload dialApproval sends read these, so the two
// paths cannot drift into describing two different calls (#1918) — which is what
// makes a downstream join of the stream feed against the approval meaningful rather
// than coincidental.
//
// approveToolInput stays a string const converted at each use site rather than a
// package-level json.RawMessage: that would be a mutable shared slice read from two
// call sites. The `cmd` key is deliberately NOT the captures' `command` — nothing
// consumes this fake's input, and renaming it would change the approve payload for no
// observable gain.
const (
	approveToolName  = "Bash"
	approveToolInput = `{"cmd":"ls"}`
)

// runStreamJSONApprove is the approve-rider counterpart of runStreamJSON: per
// {"type":"user",…} turn it writes the gated call's assistant tool_use block, then
// originates ONE permission request for that same call via control.Approve (blocking
// until the daemon answers), then writes one assistant echo carrying the verdict
// needle + one result{success} line. runStreamJSON stays byte-identical (the
// send / interrupt / queue siblings depend on it), so the ~15-line read loop is
// duplicated rather than widening its signature — the cheaper trade. The loop returns
// on EOF or the first write error, exactly like runStreamJSON.
//
// NON-USER LINES ARE NOT ALL IGNORED, and this header said they were until #2064. The
// duplication above is of the LOOP, not of the DISPATCH, so every control arm
// runStreamJSON grew — initialize at #1692, set_permission_mode at #2067 — landed on
// one side only and had to be mirrored here by hand. This rider answers
// set_permission_mode (see the arm below for why that one and not initialize); anything
// else non-user is ignored. A future arm added to runStreamJSON has to be considered
// here too, and the cost of missing one is not a skipped assertion: it is a sibling
// test dying on its deadline with a symptom that points somewhere else entirely.
//
// The block goes BEFORE the dial (#1918), which is the position real claude's gate
// has: the deny-default boundary lives between the tool_use emission and its
// tool_result, so a gated call appears on the stream first and is gated second. That
// order is the point rather than a detail — the daemon must hold the call as in
// flight FOR the duration of the approval, not only after it, which is what an
// attribution report joining the stream feed to the approval's tool_use_id depends
// on.
//
// Runs on main()'s single goroutine: one write, then a blocking dial, then two
// writes. Nothing is written WHILE the dial is in flight, and there is no second
// writer of w, so -race cleanliness is unchanged by moving that first write ahead of
// the dial.
func runStreamJSONApprove(r io.Reader, w io.Writer, socketFile string) {
	br := bufio.NewReader(r)
	turn := 0
	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			if _, ok := userTurnText([]byte(line)); ok {
				turn++
				msgID := fmt.Sprintf("m%d", turn)
				// A unique tool_use_id per turn (the registry correlation key), carried
				// onto BOTH paths — the stream block below and the approve payload — so
				// they describe one call rather than two.
				toolUseID := fmt.Sprintf("tu-1139-%d", turn)
				// First write error ends the loop, the rule writeVerdictResponse's call
				// below already follows.
				if werr := writeAssistantToolUse(w, msgID, toolUseID); werr != nil {
					return
				}
				verdict := dialApproval(socketFile, toolUseID)
				if werr := writeVerdictResponse(w, msgID, verdict); werr != nil {
					return
				}
			} else if reqID, mode, ok := setPermissionModeRequest([]byte(line)); ok {
				// The arm the header's "non-user lines are not all ignored" paragraph is
				// about. runStreamJSON learned to ANSWER initialize at #1692 and
				// set_permission_mode at #2067; because the duplication was of the loop
				// rather than of the dispatch, both arms landed on one side only and this
				// rider's child stayed unable to confirm its posture.
				//
				// #2064 is what made that fatal rather than latent: the daemon now writes
				// this request at every spawn and refuses every user turn until the child
				// acks it, so a rider that drops the line refuses the modal test's turn
				// forever and it dies on its deadline having shown no modal — a failure
				// that reads as an approval-wiring fault and is not one.
				//
				// ONLY this subtype is mirrored, not initialize as well. Nothing gates a
				// turn on the initialize ack, so an unanswered one costs this rider a
				// model list it never reads; answering it here would be untested surface
				// added on symmetry alone.
				if werr := writeSetPermissionModeAck(w, reqID, mode); werr != nil {
					return
				}
			} else if reqID, _, ok := setModelRequest([]byte(line)); ok {
				if werr := writeSetModelAck(w, reqID); werr != nil {
					return
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// writeAssistantToolUse writes the single assistant line carrying the gated call's
// tool_use block as message msgID — the line real claude emits before a permission
// gate fires, and the one the daemon's parser maps to turnevent.ToolStart.
//
// The tool name and input are read from approveToolName / approveToolInput rather
// than taken as parameters. That is the mechanism behind "one call, described once":
// the caller has no way to describe a call different from the one dialApproval sends.
// It is also the security posture — Input is emitted verbatim as json.RawMessage
// where every other field is a string json.Marshal escapes, so wiring it to
// caller-controlled bytes would be the stdout line-injection vector
// writeInitializeAck's doc describes. A future change that adds an input parameter
// must revisit that.
//
// msgID is the turn's existing m<N>: the captures show the assistant lines of one
// message sharing a message id, and turnevent.ToolStart carries no message id at all,
// so reusing it is a shape claude produces and is inert to the parser either way.
// Returns the first marshal/write error.
func writeAssistantToolUse(w io.Writer, msgID, toolUseID string) error {
	return writeAssistantToolUseFor(w, msgID, toolUseID, approveToolName, json.RawMessage(approveToolInput))
}

// writeAssistantToolUseFor is the configurable sibling used after a stdio permission
// allow. json.Marshal validates and compacts input, so even hostile-but-valid JSON
// remains inside one physical assistant line; a nil input becomes JSON null.
func writeAssistantToolUseFor(w io.Writer, msgID, toolUseID, toolName string, input json.RawMessage) error {
	return writeJSONLine(w, outAssistantToolUse{
		Type: "assistant",
		Message: outAsstToolMessage{
			ID:   msgID,
			Role: "assistant",
			Content: []outToolUseBlock{{
				Type:  "tool_use",
				ID:    toolUseID,
				Name:  toolName,
				Input: input,
			}},
		},
	})
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
		ToolName:  approveToolName,
		Input:     json.RawMessage(approveToolInput),
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
