//go:build e2e_realclaude

package realclaude

// TestInteractiveStreamDefaultPostureOutsideWorkspaceRead is the #2474 deliverable
// (split from #2468): one live claude, running under the `default` permission mode as
// written IN BAND, asked to read a file at an absolute path outside its workspace — in
// the directory shape a handoff note occupies — and a record of whether that raised a
// permission modal.
//
// # THE QUESTION
//
// #2467 shipped the handoff-note store: a note lives at
// <data dir>/handoff-notes/<conversation id>.txt, which is not inside any session's
// workspace. #2475 wants to hand a successor conversation that path and let it decide
// whether to read — which works only if the read is UNGATED. If it prompts, the
// successor cannot reach its own handoff without an operator answering a modal, and
// #2475 becomes the inline fallback instead. The design forks on the answer, so the
// answer is measured here rather than assumed.
//
// # WHY THE OBVIOUS ANSWER IS NOT EVIDENCE
//
// TestInteractiveStreamAttachmentRead (#2039) already had a live claude read an
// absolute path outside its workspace cwd and observed exactly one Read tool call, no
// permission modal, and the file's contents echoed back byte-exact. That harness spawns
// through spawnBootstrapDaemon, which passes --dangerously-skip-permissions, so what it
// measured is that a BYPASSED claude does not hesitate. It says nothing about `default`,
// and it is the single most likely source of a wrong assumption here, because the
// finding is written down in terms that read like a general answer.
//
// The adjacent SAME-POSTURE measurement points the other way.
// TestInteractiveStreamStdioModalResolution's outside-directory arm recorded claude
// 2.1.259 raising a modal with reason_type `workingDir` for a WRITE outside the working
// directory, under this very harness. So the two nearest data points disagree, and they
// differ in both posture and permission class. Whether a READ is gated the way that
// Write was is exactly what no run had measured.
//
// # THE POSTURE IS NOT WHAT THE ARGV SAYS
//
// Since #2065 EVERY child the daemon spawns launches with
// --dangerously-skip-permissions, and the flag no longer decides the running posture:
// claudeSettingsArgs documents that the daemon walks the session back to its stored
// mode in band, via the spawn-time set_permission_mode request SpawnPermissionMode
// describes, before any user turn reaches the child. A probe that measured under the
// launch argv would measure bypass and report a confident wrong answer. So the posture
// is WITNESSED here rather than assumed — see awaitDefaultPostureAck — and this file
// fails rather than measuring if the child never acknowledged `default`.
//
// # MEASURED
//
// PENDING THE LIVE GATE. This block is filled from the run's own
// `#2474 finding:` log line, which carries the question, the answer, the claude version
// and the date in one greppable line. The probe refuses to run without a claude version
// to name (see startDefaultPostureReadProbe), because a finding that cannot say what it
// was measured against is not a usable record.
//
// # Running it
//
//	go test -tags e2e_realclaude -count=1 -v \
//	  -run '^TestInteractiveStreamDefaultPostureOutsideWorkspaceRead$' ./internal/e2e/realclaude/
//
// It costs one live claude turn and needs real credentials; a skip without them is the
// correct outcome and carries no signal. READ THE COUNT OF TESTS EXECUTED, NEVER THE
// EXIT CODE: with no credentials every test skips and exits 0, and when the package
// fails to build zero tests run and it still exits 0 through a shell wrapper.
// `make check` never compiles this package — `make preship` is the gate that does.
// Count the `=== RUN` lines.

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// postureObserverEnv switches on the observer's control_response arm. It is set by
// this probe alone and read once by runPermissionObserver.
//
// THE GATE IS THE POINT, not a convenience. startPermissionObserver's observations
// channel holds 16 and its accept loop RETURNS PERMANENTLY when that buffer fills —
// so broadening what crosses it unconditionally could silently end observation for
// TestInteractiveStreamStdioModalResolution and
// TestInteractiveStreamStdioAlwaysAllowIsSessionScoped, whose own assertions would
// then fail on a missing source observation for a reason nothing names. Gated, only
// this run carries the extra traffic, and this run drains its ack before the turn.
const postureObserverEnv = "PYRY_PERMISSION_OBSERVER_POSTURE"

// The two strings the posture ack is recognised by. Local copies of streamsup's
// controlResponseSuccess and permissionModeDefault, which are unexported there; the
// daemon writes `default` through SetPermissionMode and claude echoes it back.
const (
	postureAckSuccess = "success"
	postureAckMode    = "default"
)

// outsideReadTurnBudget bounds the whole turn under test, and its SIZE is part of the
// assertion (#2416). The production timeout it keeps a margin against is
// mcpApprovalTimeout — ten minutes unless PYRY_APPROVAL_TIMEOUT overrides it — which
// is how long an UNANSWERED permission modal parks the turn before the daemon gives
// up. At 120s the margin is 5x, so a park fails here as a budget miss naming the
// milestone it was waiting on, instead of as a bare ten-minute wall clock in the one
// tier where a red run is most expensive to re-read. It is generous for what it
// actually measures: one cold spawn, at most one modal round trip, one small file read
// and one short reply. Do not widen it to "fix" a flake — a turn that needs longer
// than this is the finding.
const outsideReadTurnBudget = 120 * time.Second

// postureProbeModalCap bounds how many permission modals this probe will answer before
// declaring that claude is reissuing past the cap. denyModalsUntilIdle takes the same
// stance in the opposite verb: whether a turn that keeps re-asking ever terminates is a
// product question to raise separately, not a test flake to widen a cap for.
const postureProbeModalCap = 3

// postureProbeTokenBytes is the entropy behind one run's witness, in raw bytes before
// hex. 12 bytes is 96 bits — past any argument that an assistant guessed it — and 24
// hex characters is short enough that a model echoes it back without mangling. #2039
// measured a token of this size round-tripping byte-exact, which is why the assertion
// below is a strict strings.Contains with no case-folding fallback.
const postureProbeTokenBytes = 12

// The envelope ids this run correlates on: the message, then the modal answers.
const (
	postureProbeSendEnvID   uint64 = 2
	postureProbeAnswerEnvID uint64 = 3
)

func TestInteractiveStreamDefaultPostureOutsideWorkspaceRead(t *testing.T) {
	h, convID, observations, claudeVersion := startDefaultPostureReadProbe(t)

	// ── The witness, and the directory that makes it mean something ──
	//
	// THE DIRECTORY IS THE POINT, not tidiness. The note goes under the daemon's DATA
	// DIR in handoffNotePathFor's shape, which is a sibling of claude's workspace and
	// not inside it. Written under the workdir instead, an ordinary directory listing
	// would find it and this probe would green with the named absolute path playing no
	// part at all — the one vacuity that would not announce itself (#2039 records the
	// same trap from the other side).
	token := mintPostureProbeToken(t)
	notePath := writeHandoffShapedNote(t, h.home, convID, token)

	// Ask for the CONTENTS, not for a confirmation: a claude that opened the file and
	// replied "I've read it" would red this for the wrong reason. The no-retry clause is
	// the #2416 rule — a denied or failed call that leaves the turn free to try again
	// raises a second modal, and a hang looks exactly like the parked-completer bug.
	prompt := "Use the Read tool once to read the file at " + notePath +
		", then reply with its exact contents and nothing else. Do not use any other tool. " +
		"If the read is denied or fails, do not retry it and reply with the single word blocked."
	if strings.Contains(prompt, token) {
		t.Fatal("the prompt carries the token, so a reply echoing it would prove nothing about " +
			"whether the file was opened; the token must exist only inside the note")
	}
	sealSendMessage(t, h.phone, h.initSend, postureProbeSendEnvID, convID, "m-2474", prompt)

	// ── AC 2: the posture under test, witnessed rather than assumed ──
	//
	// Drained BEFORE the turn's frames and on a different transport, so nothing is lost
	// either way. This ordering is correct whether the bootstrap child spawns eagerly at
	// daemon start or lazily on this first turn, which is why the probe does not depend
	// on knowing which. The claim that the read happened UNDER `default` rests on the
	// runner's own contract — the posture write precedes any user turn reaching the
	// child — which this ack confirms the child accepted rather than NAK'd.
	awaitDefaultPostureAck(t, observations)

	reply, tools, modal := driveOutsideWorkspaceRead(t, h, convID)

	// ── AC 3: ungated, or never attempted? ──
	//
	// The token separates them. It exists only inside a file outside the workspace, so a
	// reply carrying it is a file that was opened; a silent refusal and a lucky guess
	// both land here instead.
	if !strings.Contains(reply, token) {
		t.Fatalf("the reply does not carry the token that existed only inside %s, so nothing here "+
			"shows claude opened it — this measures neither an ungated read nor a gated one. "+
			"Modal raised: %t. Reply (%d bytes): %q. Tools called: %v",
			filepath.Base(notePath), modal != nil, len(reply),
			truncateString(reply, questionTextLogCap), tools)
	}

	// ── AC 1 + AC 4: the finding, on whichever branch it fell ──
	//
	// ONE greppable line carrying everything the record needs: the question, the answer,
	// the claude version and the date. The MEASURED block at the top of this file is
	// filled from it, and the PR body restates it. Neither branch is a failure — which
	// is why the token and the posture ack above have to carry the whole weight of
	// non-vacuity.
	answer := "NO MODAL — the read was ungated"
	detail := ""
	if modal != nil {
		answer = "MODAL RAISED — the read was gated"
		detail = fmt.Sprintf(" reason_type=%q blocked_path_present=%t default_to_no=%t",
			stdioPermissionSafeLabel(modal.ReasonType), modal.BlockedPath != "", modal.DefaultToNo)
	}
	t.Logf("#2474 finding: does a Read of an absolute path outside the workspace prompt under the "+
		"in-band `default` posture? %s.%s claude_version=%s measured=%s tools=%v reply_bytes=%d",
		answer, detail, claudeVersion, time.Now().UTC().Format(time.DateOnly), tools, len(reply))
}

// --- the posture witness ------------------------------------------------------

// isDefaultPostureAck reports whether obs is claude's SUCCESS control_response to the
// spawn's set_permission_mode request naming `default`.
//
// THE DISCRIMINANT IS A SHAPE, and parser.go's emitModelList is where the three sibling
// shapes are told apart: a set_permission_mode success carries `mode` ALONE, a NAK
// carries an `error` string and no inner response, and an interrupt ack carries no inner
// response at all. The initialize ack written at the same spawn off the same counter
// carries `models`/`commands` and no `mode`. So subtype-plus-mode excludes every sibling
// on record, per key rather than by analogy.
//
// It recognises a shape, not a correlated reply: nothing here checks the request_id
// against the one the daemon minted, because the observer sees the child's stdout and
// has no view of what the daemon wrote. A future claude that put a `mode` inside some
// other success ack would be read as a posture ack. What that would cost is bounded —
// this gate would report `default` for a child that might be in another posture — and
// the alternative, correlating an id this process never saw, is not available.
//
// Both strings go through stdioPermissionSafeLabel BEFORE they are compared, not just
// before they are logged. They are claude-authored and cross a subprocess boundary; the
// allowlist caps them at 64 bytes of [A-Za-z0-9_.-], so neither can forge a log line
// through the failure message below, and a hostile value collapses to `<invalid>` and
// simply fails to match.
func isDefaultPostureAck(obs permissionObservation) bool {
	if obs.Type != "control_response" || obs.Response == nil {
		return false
	}
	return stdioPermissionSafeLabel(obs.Response.Subtype) == postureAckSuccess &&
		stdioPermissionSafeLabel(obs.Response.Response.Mode) == postureAckMode
}

// awaitDefaultPostureAck blocks until the child acknowledges `default`, discarding the
// sibling acks that reach the same channel.
//
// IT IS AC 2's FAIL-CLOSED BRANCH, and the failure covers both ways the posture can be
// wrong. A NAK arrives with a non-success subtype and never satisfies the discriminant,
// so it times out here — and a NAK'd child is one still running in the bypass its argv
// launched it with, per SetPermissionMode's recovery paragraph. Silence times out the
// same way. Either way the read that follows would be a read under an unknown posture,
// and "no modal" from such a run is the confident wrong answer this whole ticket exists
// to avoid.
func awaitDefaultPostureAck(t *testing.T, observations <-chan permissionObservation) {
	t.Helper()
	timer := time.NewTimer(outsideReadTurnBudget)
	defer timer.Stop()
	seen := 0
	for {
		select {
		case obs := <-observations:
			if isDefaultPostureAck(obs) {
				return
			}
			seen++
		case <-timer.C:
			t.Fatalf("no set_permission_mode success naming %q within %s after %d other observation(s) "+
				"— the child never acknowledged the daemon's in-band posture write, so it may still be "+
				"running in the bypass its launch argv carries (#2065) and anything this probe went on "+
				"to measure would be a measurement of the wrong posture",
				postureAckMode, outsideReadTurnBudget, seen)
		}
	}
}

// --- the turn under test ------------------------------------------------------

// postureProbeModal is the part of a raised permission modal this probe records. It is
// the FINDING's payload on the gated branch and never an assertion: what is being
// measured is whether a modal appeared at all, not what claude chose to say in it.
type postureProbeModal struct {
	ReasonType  string
	BlockedPath string
	DefaultToNo bool
}

// driveOutsideWorkspaceRead drains the turn to terminal idle, ANSWERING each permission
// modal allow_once, and returns the accumulated assistant text, the distinct tool names
// seen, and the first modal raised (nil when none was).
//
// ALLOW, NOT DENY, and that choice is what makes the probe report on both branches. A
// denied read produces no token, and the run could then not tell a gated read from a
// read that never happened — collapsing AC 1 and AC 3 into one unanswerable result. By
// allowing, the read proceeds whichever way the gate fell, the token witnesses that the
// named path was actually opened, and the modal's presence alone carries the finding.
// Answering needs the phone paired --allow-remote-permissions, which
// startObservedPermissionHarness does.
//
// IDLE IS ONLY TERMINAL ONCE A DELTA HAS BEEN SEEN. Accepting an earlier idle
// unconditionally would turn a stray frame into an empty accumulation and a token
// failure that blamed claude for a wire bug; gating on sawDelta makes the deadline
// message name which milestone was missed instead.
//
// The frame loop is the package's standing one: read binary→phone frames in receive
// order, decrypt EVERY noise_msg to keep the sequential receive nonce in lockstep, and
// skip a non-noise_msg control frame WITHOUT decrypting. The phone is the run's only
// reader; a second concurrent one desynchronises that nonce into a decrypt failure that
// reads like a daemon bug.
func driveOutsideWorkspaceRead(t *testing.T, h *perConvHarness, convID string) (string, []string, *postureProbeModal) {
	t.Helper()
	var (
		text     strings.Builder
		tools    []string
		seenTool = map[string]struct{}{}
		sawDelta bool
		first    *postureProbeModal
		answered int
	)
	reqID := postureProbeAnswerEnvID
	deadline := time.Now().Add(outsideReadTurnBudget)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			if !sawDelta {
				t.Fatalf("no non-empty assistant_delta for %q within %s (modals answered: %d) — the "+
					"message naming the note never produced a turn, so this run measured nothing",
					convID, outsideReadTurnBudget, answered)
			}
			t.Fatalf("claude replied for %q but the turn never reached terminal turn_state{idle} "+
				"within %s; %d byte(s) so far, %d modal(s) answered, tools %v",
				convID, outsideReadTurnBudget, text.Len(), answered, tools)
		}
		raw, err := h.phone.ReceiveBytes(remaining)
		if err != nil {
			if errors.Is(err, fakephone.ErrReceiveTimeout) {
				continue // deadline reached — re-loop into the Fatalf above
			}
			t.Fatalf("phone receive (outside-workspace read): %v", err)
		}
		var inner protocol.InnerFrameV2
		if err := json.Unmarshal(raw, &inner); err != nil {
			t.Fatalf("decode inner frame (outside-workspace read): %v", err)
		}
		if inner.Type != protocol.TypeNoiseMsg {
			// A non-noise_msg control frame (e.g. rekey) carries no envelope and does
			// not advance the receive nonce — skip without decrypting.
			continue
		}
		cipher, err := base64.StdEncoding.DecodeString(inner.Data)
		if err != nil {
			t.Fatalf("decode inner data (outside-workspace read): %v", err)
		}
		plain, err := h.initRecv.Decrypt(cipher)
		if err != nil {
			t.Fatalf("phone decrypt (receive-nonce desync?): %v", err)
		}
		var env protocol.Envelope
		if err := json.Unmarshal(plain, &env); err != nil {
			t.Fatalf("decode envelope (outside-workspace read): %v", err)
		}

		switch env.Type {
		case protocol.TypeError:
			var ep protocol.ErrorPayload
			if err := json.Unmarshal(env.Payload, &ep); err != nil {
				t.Fatalf("the message was refused and its error payload did not decode: %v", err)
			}
			t.Fatalf("the message naming the note was refused with code %q (retryable=%v) — the "+
				"refusal is the daemon's, never claude's, so check the seeded conversation binding "+
				"before reading anything into it", ep.Code, ep.Retryable)

		case protocol.TypeModalShown:
			var shown protocol.ModalShownPayload
			if err := json.Unmarshal(env.Payload, &shown); err != nil {
				t.Fatalf("decode modal_shown payload: %v", err)
			}
			if shown.Class != "permission" || shown.ModalID == "" {
				t.Fatalf("modal_shown class = %q with modal_id present = %t, want a permission modal "+
					"with a non-empty id", stdioPermissionSafeLabel(shown.Class), shown.ModalID != "")
			}
			if answered >= postureProbeModalCap {
				t.Fatalf("claude raised more than %d permission modals for %q without reaching idle — "+
					"it keeps reissuing the tool past the cap despite the prompt's no-retry clause; "+
					"that is a finding to raise separately, not a cap to widen", postureProbeModalCap, convID)
			}
			if first == nil {
				first = &postureProbeModal{
					ReasonType:  shown.ReasonType,
					BlockedPath: shown.BlockedPath,
					DefaultToNo: shown.DefaultToNo,
				}
				t.Logf("#2474: permission modal raised for the outside-workspace read "+
					"(reason_type=%q, default_to_no=%t) — answering allow_once so the read proceeds "+
					"and the token can still witness it", stdioPermissionSafeLabel(shown.ReasonType),
					shown.DefaultToNo)
			}
			answered++
			sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{
				ID:   reqID,
				Type: protocol.TypeModalAnswer,
				TS:   time.Now().UTC(),
				Payload: mustJSON(t, protocol.ModalAnswerPayload{
					ModalID:  shown.ModalID,
					OptionID: string(turnevent.PermissionOptionKindAllowOnce),
					// A client-minted idempotency key, NOT authorization — authorization is
					// ModalID validity plus the paired device's remote-permission grant.
					AnswerToken: "e2e-2474-posture-probe",
				}),
			})
			reqID++

		case protocol.TypeToolUse:
			var p protocol.ToolUsePayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode tool_use payload: %v", err)
			}
			if p.ConversationID != convID || p.Name == "" {
				continue
			}
			if _, dup := seenTool[p.Name]; dup || len(tools) >= attachReadToolLogCap {
				continue
			}
			seenTool[p.Name] = struct{}{}
			tools = append(tools, truncateString(p.Name, questionLabelLogCap))

		case protocol.TypeAssistantDelta:
			var p protocol.AssistantDeltaPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode assistant_delta payload: %v", err)
			}
			if p.ConversationID == convID && strings.TrimSpace(p.Text) != "" {
				sawDelta = true
				text.WriteString(p.Text)
			}

		case protocol.TypeTurnState:
			var st protocol.TurnStatePayload
			if err := json.Unmarshal(env.Payload, &st); err != nil {
				t.Fatalf("decode turn_state payload: %v", err)
			}
			if st.State != "idle" || st.ConversationID != convID || !sawDelta {
				continue
			}
			return text.String(), tools, first
		}
	}
}

// --- fixture ------------------------------------------------------------------

// mintPostureProbeToken returns this run's witness: 24 lowercase hex characters over 96
// bits from crypto/rand.
//
// crypto/rand and NOT time.Now().UnixNano(), which is what this package reaches for when
// it wants a run nonce. The difference is load-bearing exactly once, in AC 3's claim: a
// pass must not be reachable by an assistant producing a plausible value, and a
// nanosecond timestamp is such a value. It fails rather than falling back on a weaker
// source — a token this run could not mint is a run that cannot make its claim.
//
// It AUTHORISES NOTHING. It is a witness, not a credential, so rotation, revocation and
// expiry do not apply to it, and it is deliberately fine for it to reach a failure log.
// The comparison against it is strings.Contains and must stay one:
// crypto/subtle.ConstantTimeCompare would be wrong here on both counts — nothing secret
// is being compared, and the operands differ in length.
func mintPostureProbeToken(t *testing.T) string {
	t.Helper()
	buf := make([]byte, postureProbeTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("mint posture-probe token: %v — without crypto/rand entropy this run cannot claim "+
			"the token was unguessable, which is the whole basis of its assertion", err)
	}
	return hex.EncodeToString(buf)
}

// writeHandoffShapedNote writes the token to <data dir>/handoff-notes/<convID>.txt and
// returns that absolute path.
//
// THE SHAPE IS handoffNotePathFor's, deliberately: that helper derives a note's path as
// <data dir>/handoff-notes/<conversation id>.txt, where the data dir is the directory
// holding the session registry — here <home>/.pyry/test, which seedBootstrapRegistry
// writes sessions.json into. So the file sits where #2475's successor would find its own
// handoff, one directory up and across from claude's workspace at <home>/work.
//
// It writes the note itself rather than driving #2467's store. What is under test is the
// READ, and a probe that also exercised the store would fail two ways for one reason.
//
// Modes are stated rather than inherited: 0700 on the directory and 0600 on the note,
// matching seedBootstrapRegistry's registry write and the mode attachments.Store uses
// for the daemon-side copy of a user's own bytes. The path is filepath.Join over the
// harness home and a canonical conversation id the fixture chose, so no caller-supplied
// component reaches it and there is nothing to canonicalise or bound.
func writeHandoffShapedNote(t *testing.T, home, convID, token string) string {
	t.Helper()
	dir := filepath.Join(home, ".pyry", "test", "handoff-notes")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create handoff-note directory: %v", err)
	}
	path := filepath.Join(dir, convID+".txt")
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		t.Fatalf("write handoff-shaped note: %v", err)
	}
	return path
}

// --- harness ------------------------------------------------------------------

// startDefaultPostureReadProbe stands up the observed permission harness with the
// observer's control_response arm switched on, and returns the harness, the bound
// conversation, the observation channel and the claude version under measurement.
//
// THE POSTURE IS INHERITED, NOT CONSTRUCTED. startObservedPermissionHarness seeds a
// registry with no stored bypass, so the daemon walks the child back to `default` in
// band — which is why that harness's sibling Write arm raises a modal at all. Building a
// bespoke harness here would put the posture under this file's control, and a probe that
// arranges its own answer is not a measurement.
//
// It FAILS RATHER THAN MEASURING when the claude version is unavailable. A finding that
// cannot name what it was measured against is not a usable record — permission behaviour
// is version-dependent, which is the whole reason the 2.1.143 spike and 2.1.259 disagree
// — so AC 4 cannot be satisfied by that run and there is no point spending the turn.
func startDefaultPostureReadProbe(t *testing.T) (*perConvHarness, string, <-chan permissionObservation, string) {
	t.Helper()
	observations, configure, claudeVersion := startPermissionObserver(t)
	withPosture := func(realBin string) string {
		// Both this switch and configure's own variables must be in the environment
		// before spawnPermissionDaemon inherits it, which is the one ordering that
		// matters; startObservedPermissionHarness calls this hook just ahead of that
		// spawn, so setting it here is what puts it on the daemon's environment at all.
		t.Setenv(postureObserverEnv, "1")
		return configure(realBin)
	}
	h, convID, _ := startObservedPermissionHarness(t, permissionDaemonModel, true, withPosture)
	version := claudeVersion()
	if version == "" || version == "<empty>" || version == "<invalid>" {
		t.Fatalf("claude version unavailable (%q) — permission behaviour is version-dependent, so a "+
			"finding that cannot name the version it holds for is not a usable record", version)
	}
	return h, convID, observations, version
}

// --- offline ------------------------------------------------------------------

// TestDefaultPostureAckDiscriminant pins isDefaultPostureAck against the four
// control_response shapes on record, so the posture witness is not itself stochastic.
//
// It needs no claude and no credentials: it is the deterministic half under AC 2's live
// half, and it is what makes a green live run mean something. Without it, a
// discriminant that accepted the initialize ack would report `default` for any spawn at
// all, and the live gate could not tell the difference.
func TestDefaultPostureAckDiscriminant(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
		want bool
	}{
		{
			name: "set_permission_mode success naming default",
			line: `{"type":"control_response","response":{"subtype":"success","request_id":"1","response":{"mode":"default"}}}`,
			want: true,
		},
		{
			name: "set_permission_mode success naming another mode",
			line: `{"type":"control_response","response":{"subtype":"success","request_id":"1","response":{"mode":"bypassPermissions"}}}`,
			want: false,
		},
		{
			// A NAK leaves the child in the posture its launch argv carries, so it must
			// never satisfy the witness — this is the corner AC 2 fails closed on.
			name: "set_permission_mode NAK",
			line: `{"type":"control_response","response":{"subtype":"error","request_id":"1","error":"nope"}}`,
			want: false,
		},
		{
			// Written at the SAME spawn off the SAME counter, so it reaches the observer
			// alongside the posture ack and must be discarded rather than accepted.
			name: "initialize ack",
			line: `{"type":"control_response","response":{"subtype":"success","request_id":"2","response":{"models":[{"value":"haiku"}],"commands":[]}}}`,
			want: false,
		},
		{
			name: "interrupt ack, no inner response",
			line: `{"type":"control_response","response":{"subtype":"success","request_id":"3"}}`,
			want: false,
		},
		{
			name: "a can_use_tool request is not an ack",
			line: `{"type":"control_request","request":{"subtype":"can_use_tool","tool_name":"Read"}}`,
			want: false,
		},
		{
			// The allowlist collapses a hostile subtype to <invalid>, so it fails to match
			// rather than reaching a log intact.
			name: "hostile subtype collapses",
			line: `{"type":"control_response","response":{"subtype":"success\nforged: line","response":{"mode":"default"}}}`,
			want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var obs permissionObservation
			if err := json.Unmarshal([]byte(tc.line), &obs); err != nil {
				t.Fatalf("decode recorded shape: %v", err)
			}
			if got := isDefaultPostureAck(obs); got != tc.want {
				t.Fatalf("isDefaultPostureAck = %t, want %t", got, tc.want)
			}
		})
	}
}

// TestPermissionObservation_OmitsAbsentPostureResponse pins the widening's inertness:
// an observation with no posture response must marshal to the same bytes it did before
// the field existed.
//
// THAT IS A CONSTRAINT, not a preference. writePermissionOfferDiagnostic marshals this
// type into a published artifact, so a value-typed field would add a `"response":{...}`
// key to every diagnostic ever written and to the round-trip
// TestPermissionObservation_PreservesAlwaysAllowSource performs. A nil pointer under
// omitempty is what keeps every existing path's bytes unchanged.
func TestPermissionObservation_OmitsAbsentPostureResponse(t *testing.T) {
	var obs permissionObservation
	obs.Type = "result"
	blob, err := json.Marshal(obs)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), `"response"`) {
		t.Fatalf("an observation with no posture response still marshalled a response key: %s", blob)
	}
	if err := json.Unmarshal([]byte(`{"type":"control_response","response":{"subtype":"success","response":{"mode":"default"}}}`), &obs); err != nil {
		t.Fatal(err)
	}
	if obs.Response == nil || obs.Response.Response.Mode != postureAckMode {
		t.Fatalf("posture response did not survive the observer's re-encode boundary: %+v", obs.Response)
	}
}
